// Package api — the v20.0.0 control-plane CAPABILITY SUMMARY surface
// (roadmap §10.1 "a v20.0.0 candidate should provide…"):
//
//	GET /api/controlplane — the machine-readable answer to "which
//	    capability is configured, and what is its state": one row per
//	    §10.1 capability with state + detail, the fabric's node census,
//	    the admission ledger's live counts, and the freshness windows.
//
// The route is READ-gated (observation grade). Every row is honest:
// "unconfigured" is a valid answer (the opt-in surfaces say so), and
// the rows never claim an operator-owned act (hardware acceptance,
// real multi-host acceptance) — those live in the acceptance matrix,
// not in process state.
package api

import (
	"net/http"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/admission"
)

// capabilityState — the closed state vocabulary for a capability row.
const (
	CapStateConfigured   = "configured"   // wired and live
	CapStateUnconfigured = "unconfigured" // the opt-in env is not set (the honest absent)
	CapStateExperimental = "experimental" // configured AND experimental (InferSSD-style)
	CapStateDocumented   = "documented"   // a documentation deliverable (the runbook)
)

// controlplaneResponse — the GET /api/controlplane answer.
type controlplaneResponse struct {
	Version     string    `json:"version"`
	GeneratedAt time.Time `json:"generated_at"`
	// Capabilities — the §10.1 checklist, one row each.
	Capabilities []capabilityView `json:"capabilities"`
	// Fabric — the node census (nil-shaped when the fabric is not
	// enabled — the honest absent).
	Fabric *fabricCensus `json:"fabric,omitempty"`
	// Admission — the ledger's live counts (nil when not enabled).
	Admission *admissionCensus `json:"admission,omitempty"`
	// Windows — the freshness bounds (the same numbers the resources
	// view's allocatable math uses).
	Windows windowsView `json:"windows"`
	// Labels — the value-label vocabulary the resources view carries
	// (the §10.2 contract, repeated here so a monitor can render the
	// legend without a second fetch).
	Labels []string `json:"labels"`
}

type capabilityView struct {
	// Name — the stable machine-readable capability id.
	Name string `json:"name"`
	// State — configured | unconfigured | experimental | documented.
	State string `json:"state"`
	// Detail — the numbers/pointers behind the state (held counts,
	// backend lists, document paths — never a marketing sentence).
	Detail string `json:"detail"`
}

type fabricCensus struct {
	// Nodes — per-node state (sorted by id).
	Nodes []fabricNodeState `json:"nodes"`
	// Counts — the census roll-up.
	Counts map[string]int `json:"counts"`
	// RemoteFreshnessSeconds — the stricter remote window (#6).
	RemoteFreshnessSeconds float64 `json:"remote_freshness_seconds"`
}

type fabricNodeState struct {
	NodeID       string `json:"node_id"`
	State        string `json:"state"` // active | unreachable | quarantined
	LatencyClass string `json:"latency_class,omitempty"`
}

type admissionCensus struct {
	// Held / Active — the live capacity-holding record counts.
	Held   int `json:"held"`
	Active int `json:"active"`
	// NodeTagged — whether the per-node seam is attached (v20.0.0: the
	// authenticated multi-node execution path is LIVE).
	NodeTagged bool `json:"node_tagged"`
	// PolicyVersion — the live policy provenance.
	PolicyVersion string `json:"policy_version"`
}

type windowsView struct {
	LocalFreshnessSeconds  float64 `json:"local_freshness_seconds"`
	RemoteFreshnessSeconds float64 `json:"remote_freshness_seconds"`
}

// controlplaneRoute serves the GET (READ-gated; observation grade).
func (h *Handler) controlplaneRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "controlplane is read-only (GET)")
		return
	}
	now := time.Now().UTC()

	resp := controlplaneResponse{
		Version:      h.version,
		GeneratedAt:  now,
		Labels:       []string{LabelObserved, LabelEstimated, LabelReserved, LabelAllocatable, LabelUnknown, LabelStale},
		Capabilities: []capabilityView{},
		Windows: windowsView{
			LocalFreshnessSeconds: admission.DefaultPolicy().MaxSnapshotAge.Seconds(),
		},
	}
	if h.admission != nil {
		resp.Windows.LocalFreshnessSeconds = h.admission.Policy().MaxSnapshotAge.Seconds()
	}
	if h.fabricRegistry != nil {
		resp.Windows.RemoteFreshnessSeconds = h.fabricRegistry.RemoteSnapshotMaxAge().Seconds()
	}

	// ---- the §10.1 checklist ------------------------------------------------
	resp.Capabilities = append(resp.Capabilities,
		capabilityView{Name: "versioned-inventory", State: capState(h.inventoryV2 != nil),
			Detail: "GPU/CPU/RAM/storage/network inventory with per-source provenance (GET /api/inventory/v2)"},
		capabilityView{Name: "model-artifact-registry", State: capState(h.inventoryV2 != nil),
			Detail: "verified model and artifact registry (manifests + recorded verifications; GET /api/inventory/v2/models)"},
		capabilityView{Name: "backend-capability-registry", State: capState(h.executionPlanner != nil),
			Detail: "the closed administrator-owned backend registry, validated at construction (GET /api/compute/plan)"},
		capabilityView{Name: "deterministic-planner", State: capState(h.executionPlanner != nil),
			Detail: "pure, side-effect-free, content-hash PlanID determinism (POST /api/compute/plan)"},
		capabilityView{Name: "aggregate-admission", State: capState(h.admission != nil),
			Detail: "multi-dimensional atomic admission with the durable ledger (POST /api/compute/admission)"},
		capabilityView{Name: "durable-reservations", State: capState(h.admission != nil),
			Detail: "leases, boot reconciliation, crash recovery, renew + confirm-started lifecycle"},
	)
	if h.admission != nil {
		nodeTagged := h.admission.HasNodeSnapshots()
		state := CapStateConfigured
		detail := "the per-node snapshot seam attached — node_id admissions gate against the node's VERIFIED fabric observation (v20.0.0)"
		if !nodeTagged {
			state = CapStateUnconfigured
			detail = "the admission gate is enabled but the fabric is not (SCARLIHQ_FABRIC_ENABLED) — node-tagged admission refuses BY NAME"
		}
		resp.Capabilities = append(resp.Capabilities, capabilityView{
			Name: "node-tagged-admission", State: state, Detail: detail,
		})
	} else {
		resp.Capabilities = append(resp.Capabilities, capabilityView{
			Name: "node-tagged-admission", State: CapStateUnconfigured,
			Detail: "the admission gate is not enabled — set SCARLIHQ_ADMISSION_ENABLED + SCARLIHQ_FABRIC_ENABLED",
		})
	}
	resp.Capabilities = append(resp.Capabilities,
		capabilityView{Name: "fabric", State: capState(h.fabricRegistry != nil),
			Detail: "node identity/trust + the observational registry + reconciliation (GET /api/fabric/nodes)"},
		capabilityView{Name: "typed-workload-lifecycle", State: capState(h.computeManager != nil),
			Detail: "reserve → start → stop → release through the typed Host Bridge contract"},
	)
	// InferSSD — the honest three states (§8.8: nothing ships enabled).
	inferState := CapStateUnconfigured
	inferDetail := "SCARLIHQ_INFERSSD_PROFILE is not set (the honest §8.8 default; hardware acceptance stays the operator's act — KPK-06)"
	if h.executionPlanner != nil {
		if prof := h.executionPlanner.Policy().SSDProfile; prof != nil {
			inferState = CapStateExperimental
			inferDetail = "the runtime profile is loaded (experimental — hardware acceptance stays the operator's act, KPK-06)"
			if !prof.Enabled {
				inferState = CapStateUnconfigured
				inferDetail = "the runtime profile is loaded but DISABLED (honest documentation, §8.8)"
			}
		}
	}
	resp.Capabilities = append(resp.Capabilities,
		capabilityView{Name: "infer-ssd", State: inferState, Detail: inferDetail},
		capabilityView{Name: "expert-packs", State: capState(h.expertPacks != nil),
			Detail: "the SGLang Expert Pack artifact tier (bound/unbound verdicts on GET /api/compute/plan)"},
		capabilityView{Name: "ai-proposal-workflow", State: capState(h.agentProposals != nil),
			Detail: "LLM output becomes a PROPOSAL, never an auto-submitted command (POST /api/ai/propose)"},
		capabilityView{Name: "approvals", State: capState(h.agentApprovals != nil),
			Detail: "the operator gate — ScarliCenter's approve/reject surface (GET /api/bar/approvals)"},
		capabilityView{Name: "audit", State: capState(h.agentAudit != nil),
			Detail: "the append-only audit sink (GET /api/agent/audit)"},
		capabilityView{Name: "ai-runtime", State: capState(h.aiRuntime != nil),
			Detail: "the normalized LLM transport layer with probing + failover"},
		capabilityView{Name: "recovery-and-rollback", State: CapStateDocumented,
			Detail: "the operator runbook: docs/OPERATOR_RUNBOOK_v20.0.0.md (recovery, rollback, and the acceptance matrices)"},
		capabilityView{Name: "resource-accounting", State: CapStateConfigured,
			Detail: "this surface + GET /api/compute/resources (the observed/estimated/reserved/allocatable/unknown/stale labels)"},
	)

	// ---- the fabric census ---------------------------------------------------
	if h.fabricRegistry != nil {
		census := &fabricCensus{
			Nodes:                  []fabricNodeState{},
			Counts:                 map[string]int{"active": 0, "unreachable": 0, "quarantined": 0},
			RemoteFreshnessSeconds: h.fabricRegistry.RemoteSnapshotMaxAge().Seconds(),
		}
		for _, obs := range h.fabricRegistry.Observations() {
			state := "active"
			if h.fabricNodes != nil {
				if view, err := h.fabricNodes.Record(obs.NodeID); err == nil && view.Status == "QUARANTINED" {
					state = "quarantined"
				}
			}
			if !obs.Reachable {
				state = "unreachable"
			}
			census.Nodes = append(census.Nodes, fabricNodeState{
				NodeID:       obs.NodeID,
				State:        state,
				LatencyClass: string(obs.LatencyClass),
			})
			census.Counts[state]++
		}
		resp.Fabric = census
	}

	// ---- the admission census --------------------------------------------------
	if h.admission != nil {
		held, active := 0, 0
		for _, rec := range h.admission.Held() {
			switch rec.Status {
			case admission.StatusActive:
				active++
			default:
				held++
			}
		}
		resp.Admission = &admissionCensus{
			Held:          held,
			Active:        active,
			NodeTagged:    h.admission.HasNodeSnapshots(),
			PolicyVersion: h.admission.Policy().PolicyVersion,
		}
	}

	writeJSON(w, resp)
}

// capState maps a wired boolean onto the capability vocabulary.
func capState(configured bool) string {
	if configured {
		return CapStateConfigured
	}
	return CapStateUnconfigured
}
