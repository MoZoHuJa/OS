// multinode_test.go — the v19.9.0 network-aware multi-node planner
// proofs (roadmap §9.4 exit criteria: "network-aware planning
// deterministic"; design §5): the no-remote regression pin (zero remote
// views ≡ the single-node planner, PlanID included), determinism across
// nodes (identical inputs → identical plan + node placement, and
// input-order independence), the transfer-cost rule (local preferred
// when the artifact is absent remotely and the bounded transfer exceeds
// the operator bound), the latency-class gate, the data-sensitivity
// gate, the STRICTER remote freshness window (decision #6), and the
// cross-node artifact-identity binding (KPK-19.8.0-P1-02 at N nodes).
package planner

import (
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
)

// ---- fixtures ---------------------------------------------------------------

type fixedFabric struct {
	views []FabricNodeView
}

func (f fixedFabric) Views() []FabricNodeView { return f.views }

// remoteSnapshot clones the local fixture's capability shape (healthy
// sglang + idle GPUs) under a fresh snapshot identity, optionally with
// the model present + the node's own verification bound to the manifest
// fingerprint the control plane carries.
func remoteSnapshot(t *testing.T, snapID string, observedAt time.Time, modelID string, present bool, nodeVerif *modelmeta.ArtifactVerification, gpuUtil int) *inventory.InventorySnapshotV2 {
	t.Helper()
	base := newPlannerFixture().snap
	snap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    snapID,
		ObservedAt:    observedAt,
		Host:          base.Host,
		GPUs: []inventory.GPU{
			{ID: "gpu.nvidia.0", UUID: "GPU-remote0", VRAMTotalMB: 16384, VRAMFreeMB: 16384, UtilizationPct: gpuUtil, Healthy: true},
		},
		Runtimes: []inventory.Runtime{
			{ID: "sglang", Running: true, Healthy: true, Port: 30000},
		},
	}
	if modelID != "" {
		snap.Models = []inventory.ModelExecutionView{{
			Catalog:              inventory.Model{ID: modelID, Present: present},
			ArtifactVerification: nodeVerif,
		}}
	}
	return snap
}

func fabricFixture(t *testing.T, remotes ...FabricNodeView) (*FabricPlanner, ExecutionRequest) {
	t.Helper()
	f := newPlannerFixture()
	p := f.planner(t)
	fp, err := NewFabricPlanner(p, DefaultNetworkPolicy(), fixedFabric{views: append([]FabricNodeView{{NodeID: NodeLocal, Snapshot: f.snap, LatencyClass: "A"}}, remotes...)})
	if err != nil {
		t.Fatal(err)
	}
	return fp, baseRequest()
}

// nodeView builds a remote view with the model present + the node's own
// verification of the SAME manifest identity (the honest cross-node
// binding).
func nodeView(t *testing.T, nodeID, class string, observedAt time.Time, gpuUtil int) FabricNodeView {
	t.Helper()
	m := denseManifest()
	verif := freshVerification(m, time.Hour)
	snap := remoteSnapshot(t, "snap-node-"+nodeID, observedAt, "qwen3-14b-awq", true, verif, gpuUtil)
	return FabricNodeView{NodeID: nodeID, Snapshot: snap, LatencyClass: class, Remote: true}
}

// ---- the regression pin ------------------------------------------------------

// TestFabricPlanNoRemoteViewsEquivSingleNode: with ZERO remote views the
// fabric planner produces EXACTLY the single-node planner's decision —
// same selection, same PlanID (the content hash rides the same fields;
// NodeID is "local" on both paths). The single-node behavior is
// preserved by construction, not by hope.
func TestFabricPlanNoRemoteViewsEquivSingleNode(t *testing.T) {
	f := newPlannerFixture()
	p := f.planner(t)
	fp, req := fabricFixture(t)
	single := p.Plan(req)
	fabric := fp.Plan(req)
	if !single.Selected || !fabric.Selected {
		t.Fatalf("both planners must select: single=%v fabric=%v", single.Selected, fabric.Selected)
	}
	if single.Plan.PlanID != fabric.Plan.PlanID {
		t.Fatalf("PlanID drift: single=%s fabric=%s", single.Plan.PlanID, fabric.Plan.PlanID)
	}
	if fabric.Plan.NodeID != NodeLocal {
		t.Fatalf("no remote views → the local node, got %q", fabric.Plan.NodeID)
	}
}

// ---- the determinism proofs --------------------------------------------------

// TestFabricPlanDeterministic: identical inputs → identical plan,
// including node placement + PlanID, 20× over (roadmap §9.4's exit
// criterion, verbatim).
func TestFabricPlanDeterministic(t *testing.T) {
	now := time.Now().UTC()
	remote := nodeView(t, "node-a", "A", now, 0)
	fp, req := fabricFixture(t, remote)
	var first *ExecutionPlan
	for i := 0; i < 20; i++ {
		res := fp.Plan(req)
		if !res.Selected {
			t.Fatalf("iteration %d: %s — %s", i, res.ReasonCode, res.Explanation)
		}
		if first == nil {
			first = res.Plan
			continue
		}
		if res.Plan.PlanID != first.PlanID || res.Plan.NodeID != first.NodeID {
			t.Fatalf("iteration %d: plan identity drifted (plan %s→%s, node %s→%s)", i, first.PlanID, res.Plan.PlanID, first.NodeID, res.Plan.NodeID)
		}
	}
}

// TestFabricPlanInputOrderIndependent: the fabric source's view order
// must not steer the decision — the candidate sort is the only ordering
// authority (reversed remote order → the same plan). The local GPU runs
// 40% utilized so the idle remote node-b wins on the utilization
// tie-break (locality/latency tied first).
func TestFabricPlanInputOrderIndependent(t *testing.T) {
	f := newPlannerFixture()
	f.snap.GPUs = []inventory.GPU{{ID: "gpu.nvidia.1", UUID: gpuB, VRAMTotalMB: 16384, VRAMFreeMB: 16000, UtilizationPct: 40, Healthy: true}}
	p := f.planner(t)
	now := p.now()
	a := nodeView(t, "node-a", "A", now, 10)
	b := nodeView(t, "node-b", "A", now, 0)
	forward, err := NewFabricPlanner(p, DefaultNetworkPolicy(), fixedFabric{views: []FabricNodeView{{NodeID: NodeLocal, Snapshot: f.snap}, a, b}})
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := NewFabricPlanner(p, DefaultNetworkPolicy(), fixedFabric{views: []FabricNodeView{{NodeID: NodeLocal, Snapshot: f.snap}, b, a}})
	if err != nil {
		t.Fatal(err)
	}
	req := baseRequest()
	rf, rr := forward.Plan(req), reverse.Plan(req)
	if !rf.Selected || !rr.Selected {
		t.Fatalf("forward=%v reverse=%v", rf.Selected, rr.Selected)
	}
	if rf.Plan.PlanID != rr.Plan.PlanID || rf.Plan.NodeID != rr.Plan.NodeID {
		t.Fatalf("input order steered the decision: %s/%s vs %s/%s", rf.Plan.PlanID, rf.Plan.NodeID, rr.Plan.PlanID, rr.Plan.NodeID)
	}
	if rr.Plan.NodeID != "node-b" {
		t.Fatalf("the idle remote GPU should win, got %s (util tie-break)", rr.Plan.NodeID)
	}
}

// ---- the network gates -------------------------------------------------------

// TestFabricPlanPrefersLocalWhenRemoteRequiresTransfer: roadmap §9.4's
// rule verbatim — "prefer local execution when the expected transfer cost
// exceeds the benefit of remote compute." Local (artifact present, GPU
// 40% utilized) still beats a REMOTE idle node without the artifact
// whose transfer is within the bound: locality outranks utilization.
func TestFabricPlanPrefersLocalWhenRemoteRequiresTransfer(t *testing.T) {
	f := newPlannerFixture()
	// Make the local candidate run on the utilized GPU (rank the same
	// backend, observable difference): drop the idle local GPU.
	f.snap.GPUs = []inventory.GPU{{ID: "gpu.nvidia.1", UUID: gpuB, VRAMTotalMB: 16384, VRAMFreeMB: 16000, UtilizationPct: 40, Healthy: true}}
	p := f.planner(t)

	now := p.now()
	// Remote: artifact ABSENT but transfer-eligible (8 GiB dense weights
	// within the 10 GiB default bound) + idle GPU + class A.
	absent := remoteSnapshot(t, "snap-absent", now.Add(-time.Second), "qwen3-14b-awq", false, nil, 0)
	remote := FabricNodeView{NodeID: "node-far", Snapshot: absent, LatencyClass: "A", Remote: true}

	fp, err := NewFabricPlanner(p, DefaultNetworkPolicy(), fixedFabric{views: []FabricNodeView{{NodeID: NodeLocal, Snapshot: f.snap}, remote}})
	if err != nil {
		t.Fatal(err)
	}
	res := fp.Plan(baseRequest())
	if !res.Selected {
		t.Fatalf("a plan must exist (local eligible): %s — %s", res.ReasonCode, res.Explanation)
	}
	if res.Plan.NodeID != NodeLocal {
		t.Fatalf("local must win when the remote needs a transfer: got %s", res.Plan.NodeID)
	}
}

// TestFabricPlanTransferCostExceeded: a remote node whose artifact is
// absent AND whose size exceeds the operator bound is EXCLUDED BY NAME
// (the §9.4 rule, fail-closed) — with the local runtime stopped, the
// named refusal IS the answer.
func TestFabricPlanTransferCostExceeded(t *testing.T) {
	f := newPlannerFixture()
	// Local runtime not running: the local view contributes no candidate
	// (the only candidate would be the oversized remote).
	f.snap.Runtimes = []inventory.Runtime{{ID: "sglang", Running: false, Healthy: false}}
	p := f.planner(t)

	now := p.now()
	network := DefaultNetworkPolicy()
	network.MaxArtifactTransferBytes = 1 << 30 // 1 GiB — the dense 8 GiB weights exceed it
	absent := remoteSnapshot(t, "snap-absent2", now.Add(-time.Second), "qwen3-14b-awq", false, nil, 0)
	remote := FabricNodeView{NodeID: "node-big", Snapshot: absent, LatencyClass: "A", Remote: true}
	fp, err := NewFabricPlanner(p, network, fixedFabric{views: []FabricNodeView{{NodeID: NodeLocal, Snapshot: f.snap}, remote}})
	if err != nil {
		t.Fatal(err)
	}
	res := fp.Plan(baseRequest())
	if res.Selected {
		t.Fatalf("an oversized transfer must produce NO plan, got %s on %s", res.Plan.PlanID, res.Plan.NodeID)
	}
	if res.ReasonCode != "NODE_TRANSFER_COST_EXCEEDED" {
		t.Fatalf("the named transfer refusal is the answer, got %q (%s)", res.ReasonCode, res.Explanation)
	}
}

// TestFabricPlanLatencyClassGate: an interactive request never lands on a
// C-class node; a batch request may.
func TestFabricPlanLatencyClassGate(t *testing.T) {
	now := time.Now().UTC()
	cNode := nodeView(t, "node-c-class", "C", now, 0)
	fp, req := fabricFixture(t, cNode)
	res := fp.Plan(req)
	if res.Selected && res.Plan.NodeID == "node-c-class" {
		t.Fatal("an interactive request must not land on a C-class node")
	}
	if res.Selected && res.Plan.NodeID != NodeLocal {
		t.Fatalf("expected local fallback, got %s", res.Plan.NodeID)
	}
	// Batch may use the C node (its idle GPU beats the local 0%-util one
	// only via utilization after the locality/latency tie... local is
	// rank-0 too, class A — the C node only wins if local is weaker. Pin
	// the EXCLUSION instead: the trail names it for interactive, silent
	// for batch.
	if !containsTrail(res.Considered, "NODE_LATENCY_CLASS_INSUFFICIENT") {
		t.Fatalf("interactive trail must name the latency exclusion: %v", res.Considered)
	}
	batch := baseRequest()
	batch.LatencyClass = LatencyBatch
	resBatch := fp.Plan(batch)
	if containsTrail(resBatch.Considered, "NODE_LATENCY_CLASS_INSUFFICIENT") {
		t.Fatalf("batch must not name a latency exclusion: %v", resBatch.Considered)
	}
}

// TestFabricPlanSensitivityLocalOnly: a local-only request NEVER leaves
// the local node — even when a remote idle GPU would win on utilization.
func TestFabricPlanSensitivityLocalOnly(t *testing.T) {
	now := time.Now().UTC()
	remote := nodeView(t, "node-secret", "A", now, 0)
	fp, req := fabricFixture(t, remote)
	req.DataSensitivity = SensitivityLocalOnly
	res := fp.Plan(req)
	if !res.Selected {
		t.Fatalf("local-only must still plan locally: %s — %s", res.ReasonCode, res.Explanation)
	}
	if res.Plan.NodeID != NodeLocal {
		t.Fatalf("local-only request planned on %s", res.Plan.NodeID)
	}
	if !containsTrail(res.Considered, "never leaves the local node") {
		t.Fatalf("the trail must name the sensitivity exclusion: %v", res.Considered)
	}
	// The closed vocabulary: an unknown value is a named request refusal.
	bad := baseRequest()
	bad.DataSensitivity = "super-secret"
	if reasons := bad.Validate(); len(reasons) == 0 {
		t.Fatal("an unknown data_sensitivity must fail validation")
	}
}

// TestFabricPlanRemoteFreshnessStricter: a remote snapshot 31s old is
// INSIDE the local 60s window but OUTSIDE the remote 30s window
// (decision #6) — the remote node is excluded by name while the local
// node plans normally.
func TestFabricPlanRemoteFreshnessStricter(t *testing.T) {
	f := newPlannerFixture()
	p := f.planner(t)
	now := p.now()
	stale := nodeView(t, "node-stale", "A", now.Add(-31*time.Second), 0)
	fp, err := NewFabricPlanner(p, DefaultNetworkPolicy(), fixedFabric{views: []FabricNodeView{{NodeID: NodeLocal, Snapshot: f.snap}, stale}})
	if err != nil {
		t.Fatal(err)
	}
	res := fp.Plan(baseRequest())
	if !res.Selected {
		t.Fatalf("local must plan: %s", res.Explanation)
	}
	if res.Plan.NodeID != NodeLocal {
		t.Fatalf("the stale remote must not win: %s", res.Plan.NodeID)
	}
	if !containsTrail(res.Considered, "INVENTORY_STALE") {
		t.Fatalf("the trail must name the remote staleness: %v", res.Considered)
	}
	// 20s old — inside BOTH windows → the remote idle GPU wins on
	// utilization (locality + latency tie first; the local GPU runs
	// 40% utilized in this fixture variant).
	f2 := newPlannerFixture()
	f2.snap.GPUs = []inventory.GPU{{ID: "gpu.nvidia.1", UUID: gpuB, VRAMTotalMB: 16384, VRAMFreeMB: 16000, UtilizationPct: 40, Healthy: true}}
	p2 := f2.planner(t)
	fresh := nodeView(t, "node-fresh", "A", p2.now().Add(-20*time.Second), 0)
	fp2, err := NewFabricPlanner(p2, DefaultNetworkPolicy(), fixedFabric{views: []FabricNodeView{{NodeID: NodeLocal, Snapshot: f2.snap}, fresh}})
	if err != nil {
		t.Fatal(err)
	}
	res2 := fp2.Plan(baseRequest())
	if !res2.Selected || res2.Plan.NodeID != "node-fresh" {
		t.Fatalf("the fresh remote idle GPU should win, got selected=%v node=%s", res2.Selected, nodeOf(res2))
	}
}

// TestFabricPlanCrossNodeVerificationBinding: a remote node whose
// catalog lists the model present but whose OWN verification record is
// bound to a DIFFERENT manifest identity is not a candidate — a node
// that verified a different revision of the same ID must never execute
// the control plane's plan (KPK-19.8.0-P1-02 at N nodes).
func TestFabricPlanCrossNodeVerificationBinding(t *testing.T) {
	f := newPlannerFixture()
	p := f.planner(t)
	now := p.now()
	// A different manifest identity: same model ID, different revision.
	other := denseManifest()
	other.Revision = "rev-OTHER"
	mismatchedVerif := freshVerification(other, time.Hour)

	// Case 1: present, node verification bound to the OTHER identity →
	// excluded by name even though present.
	mismatched := remoteSnapshot(t, "snap-mismatch", now.Add(-time.Second), "qwen3-14b-awq", true, mismatchedVerif, 0)
	fp, err := NewFabricPlanner(p, DefaultNetworkPolicy(), fixedFabric{views: []FabricNodeView{{NodeID: NodeLocal, Snapshot: f.snap}, {NodeID: "node-mismatch", Snapshot: mismatched, LatencyClass: "A", Remote: true}}})
	if err != nil {
		t.Fatal(err)
	}
	res := fp.Plan(baseRequest())
	if res.Selected && res.Plan.NodeID == "node-mismatch" {
		t.Fatal("a fingerprint-mismatched node must never win")
	}
	if !containsTrail(res.Considered, "NODE_ARTIFACT_UNVERIFIED") {
		t.Fatalf("the trail must name the cross-node verification exclusion: %v", res.Considered)
	}

	// Case 2: present, NO node-side verification at all → same exclusion.
	unverified := remoteSnapshot(t, "snap-unver", now.Add(-time.Second), "qwen3-14b-awq", true, nil, 0)
	fp2, err := NewFabricPlanner(p, DefaultNetworkPolicy(), fixedFabric{views: []FabricNodeView{{NodeID: NodeLocal, Snapshot: f.snap}, {NodeID: "node-unver", Snapshot: unverified, LatencyClass: "A", Remote: true}}})
	if err != nil {
		t.Fatal(err)
	}
	res2 := fp2.Plan(baseRequest())
	if res2.Selected && res2.Plan.NodeID == "node-unver" {
		t.Fatal("an unverified remote node must never win")
	}
	if !containsTrail(res2.Considered, "NODE_ARTIFACT_UNVERIFIED") {
		t.Fatalf("the trail must name the unverified exclusion: %v", res2.Considered)
	}
}

// TestFabricPlanStagingNoteIsHonest: a transfer-eligible WINNING remote
// candidate carries the artifact-staging-required explanation — the plan
// says the artifact is absent and must be present before start (v19.9.0
// models the transfer, it does not execute it).
func TestFabricPlanStagingNoteIsHonest(t *testing.T) {
	f := newPlannerFixture()
	// Local has NO candidate at all (runtime stopped) so the
	// transfer-eligible remote is the ONLY survivor — and it must WIN
	// with the staging note (within the default 10 GiB bound).
	f.snap.Runtimes = []inventory.Runtime{{ID: "sglang", Running: false, Healthy: false}}
	p := f.planner(t)
	now := p.now()
	absent := remoteSnapshot(t, "snap-absent3", now.Add(-time.Second), "qwen3-14b-awq", false, nil, 0)
	remote := FabricNodeView{NodeID: "node-stage", Snapshot: absent, LatencyClass: "A", Remote: true}
	fp, err := NewFabricPlanner(p, DefaultNetworkPolicy(), fixedFabric{views: []FabricNodeView{{NodeID: NodeLocal, Snapshot: f.snap}, remote}})
	if err != nil {
		t.Fatal(err)
	}
	res := fp.Plan(baseRequest())
	if !res.Selected || res.Plan.NodeID != "node-stage" {
		t.Fatalf("the transfer-eligible remote must win here: selected=%v node=%s reason=%s", res.Selected, nodeOf(res), res.ReasonCode)
	}
	found := false
	for _, r := range res.Plan.Explanation {
		if r.Code == "artifact-staging-required" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the plan must carry the artifact-staging-required reason: %+v", res.Plan.Explanation)
	}
}

// ---- construction ------------------------------------------------------------

func TestNewFabricPlannerFailClosed(t *testing.T) {
	f := newPlannerFixture()
	p := f.planner(t)
	if _, err := NewFabricPlanner(nil, DefaultNetworkPolicy(), fixedFabric{}); err == nil {
		t.Fatal("a nil single-node planner must be refused")
	}
	if _, err := NewFabricPlanner(p, DefaultNetworkPolicy(), nil); err == nil {
		t.Fatal("a nil fabric source must be refused")
	}
	for _, bad := range []NetworkPolicy{
		{PolicyVersion: "x", MaxArtifactTransferBytes: 0, RemoteSnapshotMaxAge: time.Second, InteractiveMaxLatencyClass: "B", BatchMaxLatencyClass: "C"},
		{PolicyVersion: "x", MaxArtifactTransferBytes: 1, RemoteSnapshotMaxAge: 0, InteractiveMaxLatencyClass: "B", BatchMaxLatencyClass: "C"},
		{PolicyVersion: "x", MaxArtifactTransferBytes: 1, RemoteSnapshotMaxAge: time.Second, InteractiveMaxLatencyClass: "D", BatchMaxLatencyClass: "C"},
		{PolicyVersion: "x", MaxArtifactTransferBytes: 1, RemoteSnapshotMaxAge: time.Second, InteractiveMaxLatencyClass: "B", BatchMaxLatencyClass: "z"},
		{PolicyVersion: "", MaxArtifactTransferBytes: 1, RemoteSnapshotMaxAge: time.Second, InteractiveMaxLatencyClass: "B", BatchMaxLatencyClass: "C"}, // empty → defaults kick in → valid; see below
	} {
		if bad.PolicyVersion == "" {
			// The empty version falls back to the DEFAULT policy (documented).
			if _, err := NewFabricPlanner(p, bad, fixedFabric{}); err != nil {
				t.Fatalf("empty network policy must adopt the default, got %v", err)
			}
			continue
		}
		if _, err := NewFabricPlanner(p, bad, fixedFabric{}); err == nil {
			t.Fatalf("malformed network policy must be refused: %+v", bad)
		}
	}
}

// ---- helpers -----------------------------------------------------------------

func containsTrail(trail []string, needle string) bool {
	for _, e := range trail {
		if strings.Contains(e, needle) {
			return true
		}
	}
	return false
}

func nodeOf(res PlanResult) string {
	if res.Plan == nil {
		return "<none>"
	}
	return res.Plan.NodeID
}
