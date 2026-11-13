// multinode.go — the v19.9.0 network-aware multi-node planner (Compute
// Fabric Phase 5, roadmap §9.4 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md,
// design: docs/SCARLIX_COMPUTE_FABRIC_PHASE_5_DISTRIBUTED_DESIGN.md §5).
//
// CHARTER (extends the package's standing contract to N nodes):
//
//   - The planner stays PURE and SIDE-EFFECT-FREE: node views arrive
//     through the constructor-injected FabricSource; the output is a
//     proposal (admission still required); nothing is reserved, staged or
//     launched here.
//   - DETERMINISTIC for identical inputs (roadmap §9.4's exit criterion
//     "network-aware planner is deterministic"): the same request × the
//     same node views × the same policy → the same plan, including the
//     same PlanID (NodeID has been part of the content hash since
//     v19.7.2). The candidate ordering extends §7.5's tie-break sequence
//     with the network dimensions inserted where §5.2 of the design
//     fixes them: (1) policy priority, (2) artifact locality (present
//     beats transfer-eligible), (3) latency class (A before B before C),
//     (4) observed utilization, (5) tightest fit, (6) stable backend +
//     node + GPU IDs.
//   - HARD NETWORK GATES BEFORE OPTIMIZATION (design §5.2 rule 2): the
//     transfer-cost rule (roadmap §9.4's "prefer local execution when
//     the expected transfer cost exceeds the benefit of remote compute",
//     operationalized: remote nodes without the artifact are only
//     eligible when the artifact's verified size is KNOWN and within the
//     operator's MaxArtifactTransferBytes bound — an unknown size fails
//     closed), the latency-class gate, the data-sensitivity gate
//     (local-only requests never leave the local node).
//   - REMOTE FRESHNESS IS STRICTER (design decision #6): the remote
//     window (default 30s) is separate from and tighter than the local
//     window (default 60s) — remote capacity ages in transit before the
//     control plane observes it.
//   - REMOTE ARTIFACT IDENTITY IS CROSS-NODE BOUND (v19.8.1's
//     KPK-19.8.0-P1-02 discipline extended to N nodes): a remote node's
//     own verification record must carry the SAME manifest fingerprint
//     the control plane planned against — a node that verified a
//     different revision of the "same" model ID is NOT a candidate.
//   - SINGLE-NODE BEHAVIOR PRESERVED: with zero remote node views the
//     fabric planner evaluates exactly the local view — identical
//     decisions to the single-node Planner.Plan (the regression pin).

package planner

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
)

// ---------------------------------------------------------------------------
// The network-aware admission policy (§9.4 — operator-owned, validated at
// construction; the ONLY network-steering input)
// ---------------------------------------------------------------------------

// LatencyClassRankLen bounds the closed latency vocabulary ("A", "B", "C").
const latencyClassClosed = "ABC"

// NetworkPolicy is the v19.9.0 network-aware admission policy — a SEPARATE
// operator-owned struct (the PlannerPolicy shape is untouched; additive
// evolution, no v19.8.x break).
type NetworkPolicy struct {
	// PolicyVersion — the network policy's own identity (rides the
	// explanation trail; changes here change decisions, so it is versioned).
	PolicyVersion string
	// MaxArtifactTransferBytes — the transfer-cost bound (design D5-5):
	// a remote node without the artifact is only transfer-eligible while
	// the artifact's verified size is known and ≤ this bound. Default
	// 10 GiB. An UNKNOWN size always fails closed (never a guess).
	MaxArtifactTransferBytes uint64
	// RemoteSnapshotMaxAge — the REMOTE admission freshness window
	// (design decision #6: default 30s, STRICTER than the local 60s).
	RemoteSnapshotMaxAge time.Duration
	// InteractiveMaxLatencyClass — the worst node class an interactive
	// request may be planned on ("A" or "B"; default "B").
	InteractiveMaxLatencyClass string
	// BatchMaxLatencyClass — the worst node class for throughput/batch
	// requests (default "C" — any).
	BatchMaxLatencyClass string
}

// DefaultNetworkPolicy is the documented v19.9.0 policy (design §5.2 +
// decision #6: transfer bound 10 GiB, remote freshness 30s, interactive
// ≤ B, batch ≤ C).
func DefaultNetworkPolicy() NetworkPolicy {
	return NetworkPolicy{
		PolicyVersion:              "planner-network/v19.9.0",
		MaxArtifactTransferBytes:   10 * 1024 * 1024 * 1024,
		RemoteSnapshotMaxAge:       30 * time.Second,
		InteractiveMaxLatencyClass: "B",
		BatchMaxLatencyClass:       "C",
	}
}

// validate checks the closed shapes (fail-closed at construction — an
// inconsistent network policy is a construction error, never a
// per-request surprise).
func (n NetworkPolicy) validate() error {
	if strings.TrimSpace(n.PolicyVersion) == "" {
		return fmt.Errorf("planner: network policy version is required")
	}
	if n.MaxArtifactTransferBytes == 0 {
		return fmt.Errorf("planner: MaxArtifactTransferBytes must be > 0 (a zero bound is a full-ban misconfigured as a default)")
	}
	if n.RemoteSnapshotMaxAge <= 0 {
		return fmt.Errorf("planner: RemoteSnapshotMaxAge must be > 0")
	}
	for _, c := range []string{n.InteractiveMaxLatencyClass, n.BatchMaxLatencyClass} {
		if len(c) != 1 || !strings.Contains(latencyClassClosed, strings.ToUpper(c)) {
			return fmt.Errorf("planner: latency class %q is not one of A/B/C", c)
		}
	}
	return nil
}

// worstAllowedClass resolves the request's worst-allowed node class.
func (n NetworkPolicy) worstAllowedClass(req ExecutionRequest) byte {
	switch req.LatencyClass {
	case LatencyInteractive:
		return n.InteractiveMaxLatencyClass[0]
	default: // throughput / batch
		return n.BatchMaxLatencyClass[0]
	}
}

// ---------------------------------------------------------------------------
// The fabric source (the planner's view of N nodes — structural interface,
// same discipline as SnapshotSource/ModelSource)
// ---------------------------------------------------------------------------

// FabricNodeView is ONE node's planning input: its identity, its own
// inventory snapshot, its operator-declared latency class, and whether it
// is the local control-plane host. The view contract is deliberately
// minimal — presence and verification derive from the SNAPSHOT (the
// single source of truth), never from side-loaded maps.
type FabricNodeView struct {
	// NodeID — the node's identity ("local" for the control-plane host).
	NodeID string
	// Snapshot — the node's last observed inventory (nil = never observed;
	// the fabric source simply omits such nodes — an unobserved node is not
	// a planning input).
	Snapshot *inventory.InventorySnapshotV2
	// LatencyClass — "A" (best) / "B" / "C" (worst/unknown). Unknown
	// declarations normalize to C (fail-closed).
	LatencyClass string
	// Remote — false only for the local control-plane host.
	Remote bool
}

// FabricSource produces the node views the planner computes against
// (production: an adapter over the fabric registry + the local snapshot
// source; tests: fixtures). The contract: deterministic order — the local
// view FIRST, then remote views sorted by NodeID (the same inputs always
// arrive in the same order; the planner re-sorts candidates anyway, the
// source order only keeps the trail readable).
type FabricSource interface {
	Views() []FabricNodeView
}

// ---------------------------------------------------------------------------
// The fabric planner
// ---------------------------------------------------------------------------

// FabricPlanner is the multi-node network-aware planner (v19.9.0). It
// wraps a fully-constructed single-node Planner — the §7.4/§7.5 gates
// (modelGates, joinBackends, assemblePlan) are the SAME code, applied per
// node view; only the network dimension is new.
type FabricPlanner struct {
	planner *Planner
	network NetworkPolicy
	fabric  FabricSource
}

// NewFabricPlanner wires the fabric planner. Fail-closed: nil planner /
// nil source / malformed network policy refuse construction.
func NewFabricPlanner(p *Planner, network NetworkPolicy, fabric FabricSource) (*FabricPlanner, error) {
	if p == nil {
		return nil, fmt.Errorf("planner: the fabric planner requires a constructed single-node planner")
	}
	if fabric == nil {
		return nil, fmt.Errorf("planner: a fabric source is required (a planner without node views is a guess factory)")
	}
	if network.PolicyVersion == "" {
		network = DefaultNetworkPolicy()
	}
	if err := network.validate(); err != nil {
		return nil, fmt.Errorf("planner: network policy malformed: %w", err)
	}
	return &FabricPlanner{planner: p, network: network, fabric: fabric}, nil
}

// fabricCandidate is one node's one-backend survivor of the full join,
// carrying the network dimensions the §5.2 ordering needs.
type fabricCandidate struct {
	candidate
	node         FabricNodeView
	localityRank int  // 0 = artifact present on the node; 1 = transfer-eligible (absent, bounded)
	latencyClass byte // normalized A/B/C
}

// fabricLess is the §5.2 deterministic ordering: (1) policy priority,
// (2) artifact locality, (3) latency class, (4) observed utilization,
// (5) tightest fit, (6) stable backend + node + GPU IDs.
func (fp *FabricPlanner) fabricLess(a, b fabricCandidate) bool {
	pa, pb := fp.planner.priorityOf(a.backend), fp.planner.priorityOf(b.backend)
	if pa != pb {
		return pa < pb
	}
	if a.localityRank != b.localityRank {
		return a.localityRank < b.localityRank
	}
	if a.latencyClass != b.latencyClass {
		return a.latencyClass < b.latencyClass
	}
	if a.util != b.util {
		return a.util < b.util
	}
	if a.frag != b.frag {
		return a.frag > b.frag // tighter fit wins
	}
	if a.backend != b.backend {
		return a.backend < b.backend
	}
	if a.node.NodeID != b.node.NodeID {
		return a.node.NodeID < b.node.NodeID
	}
	return gpuUUID(a.gpu) < gpuUUID(b.gpu)
}

// Plan computes the network-aware multi-node plan for req (or the named
// refusal). The §7.4/§7.5 pipeline per node view + the §5.2 network
// gates and ordering. PURE: same inputs → same plan (PlanID included).
func (fp *FabricPlanner) Plan(req ExecutionRequest) PlanResult {
	now := fp.planner.now()

	// 1. request shape (the shared single-node validation, including the
	// v19.9.0 data-sensitivity vocabulary).
	if reasons := req.Validate(); len(reasons) > 0 {
		return fp.planner.refuse(ReasonPlanRequestInvalid, fmt.Sprintf("request invalid: %s", strings.Join(reasons, "; ")), nil)
	}

	// 2. the node views (deterministic order: local first, remotes by ID).
	views := fp.fabric.Views()
	var local *FabricNodeView
	for i := range views {
		if !views[i].Remote {
			local = &views[i]
			break
		}
	}
	if local == nil || local.Snapshot == nil {
		return fp.planner.refuse(ReasonPlanSnapshotUnavailable, "the fabric source carries no local node view with a snapshot — the local host is the planning baseline; refusing to fabricate one", nil)
	}
	// Local freshness: the EXISTING single-node window (decision #6 keeps
	// the local 60s default untouched).
	if age := now.Sub(local.Snapshot.ObservedAt); age > fp.planner.policy.MaxSnapshotAge {
		return fp.planner.refuse(ReasonPlanSnapshotStale, fmt.Sprintf("local snapshot %s is %s old (bound %s) — a stale free-VRAM/free-space value is never current capacity (KPK-19.7.0-P2-01, HARD refusal)", local.Snapshot.SnapshotID, age.Truncate(time.Second), fp.planner.policy.MaxSnapshotAge), local.Snapshot)
	}
	base := PlanResult{SnapshotID: local.Snapshot.SnapshotID, PolicyVersion: fp.planner.policy.PolicyVersion}

	// 3. the model gates (SHARED with the single-node path — node
	// independent by construction).
	manifest, verif, refusal, ok := fp.planner.modelGates(req, base, now)
	if !ok {
		return refusal
	}

	// 4-6. per node view: the network gates, then the §7.4/§7.5 backend
	// join against that node's snapshot.
	var complete []fabricCandidate
	var trail []string
	var downstream []string
	sensitivity := req.DataSensitivity
	worstClass := fp.network.worstAllowedClass(req)
	transferBytes, transferKnown := artifactTransferBytes(manifest)

	for _, view := range views {
		if view.NodeID == "" || view.Snapshot == nil {
			continue // an unobserved node is not a planning input
		}
		if !view.Remote {
			// The local view: artifact present by construction (the
			// control plane's verification covers its own disk — the
			// resident strategies' standing assumption), class A (zero
			// network).
			c, nodeTrail, nodeDownstream := fp.planner.joinBackends(manifest, view.Snapshot, req)
			trail = append(trail, prefixTrail(view.NodeID, nodeTrail)...)
			downstream = append(downstream, nodeDownstream...)
			for _, cand := range c {
				complete = append(complete, fabricCandidate{candidate: cand, node: view, localityRank: 0, latencyClass: 'A'})
			}
			continue
		}

		// --- the REMOTE network gates (design §5.2 rule 2, in order) ---
		if sensitivity == SensitivityLocalOnly {
			trail = append(trail, fmt.Sprintf("%s: excluded — data_sensitivity %s never leaves the local node (roadmap §9.4)", view.NodeID, SensitivityLocalOnly))
			continue
		}
		// Remote freshness (decision #6: the STRICTER separate window).
		if age := now.Sub(view.Snapshot.ObservedAt); age > fp.network.RemoteSnapshotMaxAge {
			trail = append(trail, fmt.Sprintf("%s: excluded — INVENTORY_STALE (snapshot %s is %s old, remote bound %s — the remote window is stricter than local; remote capacity ages in transit)", view.NodeID, view.Snapshot.SnapshotID, age.Truncate(time.Second), fp.network.RemoteSnapshotMaxAge))
			downstream = append(downstream, "INVENTORY_STALE")
			continue
		}
		// Latency-class gate.
		nodeClass := normalizeNodeLatencyClass(view.LatencyClass)
		if nodeClass > worstClass {
			trail = append(trail, fmt.Sprintf("%s: excluded — NODE_LATENCY_CLASS_INSUFFICIENT (class %s exceeds the request's worst-allowed class %s; roadmap §9.4's inter-node latency constraint; unknown declarations are C, fail-closed)", view.NodeID, string(nodeClass), string(worstClass)))
			downstream = append(downstream, "NODE_LATENCY_CLASS_INSUFFICIENT")
			continue
		}
		// Artifact locality + cross-node verification binding.
		present, nodeVerified := remoteArtifactState(view.Snapshot, req.ModelID, verif.ManifestFingerprint)
		localityRank := 1 // transfer-eligible
		if present {
			if !nodeVerified {
				trail = append(trail, fmt.Sprintf("%s: excluded — NODE_ARTIFACT_UNVERIFIED (the node's catalog lists model %q present but its own artifact verification is absent/not-fully-verified/fingerprint-mismatched — a node that verified a different revision of the same ID is not a candidate; KPK-19.8.0-P1-02 extended to N nodes)", view.NodeID, req.ModelID))
				downstream = append(downstream, "NODE_ARTIFACT_UNVERIFIED")
				continue
			}
			localityRank = 0 // artifact present + the node verified the SAME identity
		} else {
			// The transfer-cost rule (§9.4 operationalized, design D5-5):
			// remote admission is REFUSED when the artifact is absent and
			// the size is unknown (fail-closed) or exceeds the bound.
			if !transferKnown {
				trail = append(trail, fmt.Sprintf("%s: excluded — NODE_TRANSFER_COST_UNKNOWN (model %q absent on the node and the manifest declares no known artifact size — an UNKNOWN transfer cost exceeds any remote benefit, fail closed; roadmap §9.4)", view.NodeID, req.ModelID))
				downstream = append(downstream, "NODE_TRANSFER_COST_UNKNOWN")
				continue
			}
			if transferBytes > fp.network.MaxArtifactTransferBytes {
				trail = append(trail, fmt.Sprintf("%s: excluded — NODE_TRANSFER_COST_EXCEEDED (model %q absent on the node and its artifact size %d bytes exceeds MaxArtifactTransferBytes %d — prefer local execution when the transfer cost exceeds the remote benefit; roadmap §9.4)", view.NodeID, req.ModelID, transferBytes, fp.network.MaxArtifactTransferBytes))
				downstream = append(downstream, "NODE_TRANSFER_COST_EXCEEDED")
				continue
			}
			// Transfer-eligible: plannable, staged-before-start (the
			// agent's start gate enforces presence — the plan says so).
		}

		// The shared backend join against the REMOTE snapshot.
		c, nodeTrail, nodeDownstream := fp.planner.joinBackends(manifest, view.Snapshot, req)
		trail = append(trail, prefixTrail(view.NodeID, nodeTrail)...)
		downstream = append(downstream, nodeDownstream...)
		for _, cand := range c {
			complete = append(complete, fabricCandidate{candidate: cand, node: view, localityRank: localityRank, latencyClass: nodeClass})
		}
	}
	base.Considered = trail

	if len(complete) == 0 {
		// The precise-code path (mirrors the single-node rule): exactly one
		// downstream refusal IS the answer; several aggregate honestly.
		if len(downstream) == 1 {
			return fp.planner.refuseWith(base, downstream[0], fmt.Sprintf("no node/backend survived the network-aware join — %s — trail: %s", downstream[0], strings.Join(trail, " | ")))
		}
		return fp.planner.refuseWith(base, ReasonPlanBackendNoCandidates, fmt.Sprintf("no node/backend survived the capability × presence × capacity × image × network join — trail: %s", strings.Join(trail, " | ")))
	}

	// 7. the deterministic network-aware ordering (§5.2).
	sort.SliceStable(complete, func(i, j int) bool {
		return fp.fabricLess(complete[i], complete[j])
	})
	winner := complete[0]

	// 8. assembly: the plan is a PROPOSAL (admission still required); the
	// NodeID rides the content hash (same inputs → same PlanID, node
	// placement included — the §9.4 determinism proof).
	plan := fp.planner.assemblePlan(req, manifest, verif, winner.candidate, winner.node.Snapshot, now, winner.node.NodeID)
	if winner.node.Remote && winner.localityRank == 1 {
		// The honest staging note: transfer-eligible ≠ transfer-executed.
		// v19.9.0 has no artifact-transfer pipeline — the plan proposes
		// remote placement CONTINGENT on the operator/agent staging the
		// artifact first; the agent's start gate enforces presence (it
		// refuses to start weights that are not on its disk).
		plan.Explanation = append(plan.Explanation, DecisionReason{Code: "artifact-staging-required", Detail: fmt.Sprintf("the artifact (%d bytes, within the %d-byte transfer bound) is ABSENT on node %s — v19.9.0 models the transfer, it does not execute it: the artifact must be present on the node before start; the node agent's start gate enforces presence", transferBytes, fp.network.MaxArtifactTransferBytes, winner.node.NodeID)})
	}
	out := base
	out.Selected = true
	out.ReasonCode = ReasonPlanOK
	out.Explanation = fmt.Sprintf("plan %s: model %s on %s/%s (strategy %s, tier %d, policy priority %d, latency class %s, artifact %s) — a PROPOSAL: admission is still required; the plan expires at %s", plan.PlanID, req.ModelID, winner.node.NodeID, winner.backend, winner.strategy, tierOf(winner.backend), fp.planner.priorityOf(winner.backend)+1, string(winner.latencyClass), localityWord(winner.localityRank), plan.ExpiresAt.UTC().Format(time.RFC3339))
	out.Plan = plan
	return out
}

// prefixTrail namespaces one node's trail entries with its identity.
func prefixTrail(nodeID string, entries []string) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = nodeID + ": " + e
	}
	return out
}

func localityWord(rank int) string {
	if rank == 0 {
		return "present-on-node"
	}
	return "transfer-eligible"
}

// normalizeNodeLatencyClass maps a view's declared class into the closed
// A/B/C byte; unknown (including "") is C — fail-closed.
func normalizeNodeLatencyClass(s string) byte {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "A":
		return 'A'
	case "B":
		return 'B'
	default:
		return 'C'
	}
}

// artifactTransferBytes derives the artifact's transfer size from the
// manifest (the DECLARED footprint — dense: shared bytes; MoE: experts +
// shared). false means UNKNOWN (fail closed: never a guess).
func artifactTransferBytes(m *modelmeta.Manifest) (uint64, bool) {
	switch m.Architecture.Kind {
	case modelmeta.ArchitectureDense:
		if m.Architecture.SharedBytes == 0 {
			return 0, false
		}
		return m.Architecture.SharedBytes, true
	case modelmeta.ArchitectureMoE:
		if m.Architecture.ExpertCount <= 0 || m.Architecture.ExpertBytes == 0 {
			return 0, false
		}
		if m.Architecture.ExpertCount > 1<<20 { // sanity bound against overflow garbage
			return 0, false
		}
		return uint64(m.Architecture.ExpertCount)*m.Architecture.ExpertBytes + m.Architecture.SharedBytes, true
	default:
		return 0, false
	}
}

// remoteArtifactState answers, for one remote node's snapshot: is the
// model present in the node's catalog, and did the NODE's own recorded
// verification verify the SAME manifest identity the control plane
// planned against (cross-node binding — KPK-19.8.0-P1-02 at N nodes)?
func remoteArtifactState(snap *inventory.InventorySnapshotV2, modelID, wantFingerprint string) (present, verified bool) {
	if snap == nil {
		return false, false
	}
	for i := range snap.Models {
		mv := &snap.Models[i]
		if mv.Catalog.ID != modelID {
			continue
		}
		present = mv.Catalog.Present
		if mv.ArtifactVerification != nil &&
			mv.ArtifactVerification.AllVerified &&
			mv.ArtifactVerification.ManifestFingerprint != "" &&
			mv.ArtifactVerification.ManifestFingerprint == wantFingerprint {
			verified = true
		}
		return present, verified
	}
	return false, false
}
