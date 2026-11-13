package fabric

import (
	"reflect"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeStatusSource struct{ active map[string]bool }

func (s *fakeStatusSource) NodeActive(nodeID string) bool { return s.active[nodeID] }

func newTestRegistry(t *testing.T) (*Registry, *fakeClock, *fakeStatusSource) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
	status := &fakeStatusSource{active: map[string]bool{"node-a": true, "node-b": true}}
	reg, err := NewRegistry(clock, status, 0)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg, clock, status
}

func testSnapshot(id string, observedAt time.Time) *inventory.InventorySnapshotV2 {
	return &inventory.InventorySnapshotV2{
		SchemaVersion: "scarlix.inventory/v2",
		SnapshotID:    id,
		ObservedAt:    observedAt,
	}
}

func TestObserveInventoryHappyPath(t *testing.T) {
	reg, clock, _ := newTestRegistry(t)
	snap := testSnapshot("snap-1", clock.Now())
	if err := reg.ObserveInventory("node-a", snap, "A"); err != nil {
		t.Fatalf("ObserveInventory: %v", err)
	}
	obs, ok := reg.Observation("node-a")
	if !ok || !obs.Reachable || obs.Snapshot != snap {
		t.Fatalf("observation = %+v ok=%v", obs, ok)
	}
	if obs.LatencyClass != LatencyClassA {
		t.Fatalf("latency class = %q", obs.LatencyClass)
	}
	// Deterministic listing.
	if got := reg.Observations(); len(got) != 1 || got[0].NodeID != "node-a" {
		t.Fatalf("observations = %+v", got)
	}
}

func TestObserveInventoryRefusesNonActiveAndBadSnapshots(t *testing.T) {
	reg, clock, _ := newTestRegistry(t)
	if err := reg.ObserveInventory("node-z", testSnapshot("s", clock.Now()), "A"); err == nil {
		t.Fatal("a non-ACTIVE (unknown) node must be refused")
	}
	if err := reg.ObserveInventory("node-a", nil, "A"); err == nil {
		t.Fatal("a nil snapshot must be refused")
	}
	if err := reg.ObserveInventory("node-a", &inventory.InventorySnapshotV2{}, "A"); err == nil {
		t.Fatal("a zero-ObservedAt snapshot must be refused")
	}
}

func TestUnreachableRetainsSnapshotAndBlocksAdmission(t *testing.T) {
	reg, clock, _ := newTestRegistry(t)
	snap := testSnapshot("snap-2", clock.Now())
	if err := reg.ObserveInventory("node-a", snap, "B"); err != nil {
		t.Fatalf("ObserveInventory: %v", err)
	}
	reg.MarkUnreachable("node-a")

	obs, ok := reg.Observation("node-a")
	if !ok {
		t.Fatal("the node must remain in the registry")
	}
	if obs.Reachable {
		t.Fatal("reachability must be false")
	}
	if obs.Snapshot != snap {
		t.Fatal("the last snapshot must be RETAINED (capacity unknown ≠ forgotten)")
	}
	// Admission guard: unreachable → refused.
	if err := reg.AdmissionGuard("node-a", clock.Now()); err == nil {
		t.Fatal("admission on an unreachable node must be refused")
	}
	// The node returns: a fresh observation re-opens admission.
	clock.now = clock.now.Add(2 * time.Second)
	if err := reg.ObserveInventory("node-a", testSnapshot("snap-3", clock.Now()), "B"); err != nil {
		t.Fatalf("re-observe: %v", err)
	}
	if err := reg.AdmissionGuard("node-a", clock.Now()); err != nil {
		t.Fatalf("admission after return: %v", err)
	}
}

func TestAdmissionGuardMatrix(t *testing.T) {
	reg, clock, status := newTestRegistry(t)

	// Unknown node.
	if err := reg.AdmissionGuard("ghost", clock.Now()); err == nil {
		t.Fatal("unknown node must be refused")
	}
	// No snapshot yet.
	if err := reg.AdmissionGuard("node-a", clock.Now()); err == nil {
		t.Fatal("a node with no observed inventory must be refused")
	}
	// Quarantine AFTER an observation: the identity flip alone must
	// quarantine admission (defense in depth under the API gate).
	if err := reg.ObserveInventory("node-a", testSnapshot("s1", clock.Now()), "A"); err != nil {
		t.Fatalf("observe: %v", err)
	}
	status.active["node-a"] = false
	if err := reg.AdmissionGuard("node-a", clock.Now()); err == nil {
		t.Fatal("a quarantined node must be refused even with a live observation")
	}
	// Observations are refused for quarantined nodes too.
	if err := reg.ObserveInventory("node-a", testSnapshot("s2", clock.Now()), "A"); err == nil {
		t.Fatal("a quarantined node must not push new observations")
	}
	status.active["node-a"] = true

	// Freshness: the REMOTE window (default 30s) is stricter than local.
	if err := reg.ObserveInventory("node-a", testSnapshot("s3", clock.Now()), "A"); err != nil {
		t.Fatalf("observe: %v", err)
	}
	clock.now = clock.now.Add(31 * time.Second)
	if err := reg.AdmissionGuard("node-a", clock.Now()); err == nil {
		t.Fatal("a stale remote snapshot must be refused (remote window 30s)")
	}
	clock.now = clock.now.Add(-15 * time.Second) // back inside the window
	if err := reg.AdmissionGuard("node-a", clock.Now()); err != nil {
		t.Fatalf("a fresh remote snapshot must pass: %v", err)
	}
}

func TestLatencyClassNormalization(t *testing.T) {
	for in, want := range map[string]LatencyClass{
		"A": LatencyClassA, "a": LatencyClassA, " b ": LatencyClassB,
		"": LatencyClassC, "X": LatencyClassC, "unknown": LatencyClassC,
	} {
		if got := NormalizeLatencyClass(in); got != want {
			t.Fatalf("NormalizeLatencyClass(%q) = %q, want %q", in, got, want)
		}
	}
	if LatencyClassA.Rank() >= LatencyClassB.Rank() || LatencyClassB.Rank() >= LatencyClassC.Rank() {
		t.Fatal("latency ranks must be strictly ordered A < B < C")
	}
}

func TestReconcileNodeSemanticsMatrix(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	agent := []AgentExecutionRecord{
		{AllocationID: "alloc-running", State: AgentExecRunning, ObservedAt: now},
		{AllocationID: "alloc-stopped", State: AgentExecStopped, ObservedAt: now},
		{AllocationID: "alloc-never", State: AgentExecNeverStarted, ObservedAt: now},
		{AllocationID: "alloc-start-unclear", State: AgentExecStartUncertain, ObservedAt: now},
		{AllocationID: "alloc-stop-unclear", State: AgentExecStopUncertain, ObservedAt: now},
		{AllocationID: "alloc-foreign-running", State: AgentExecRunning, ObservedAt: now},
		{AllocationID: "alloc-foreign-stopped", State: AgentExecStopped, ObservedAt: now},
	}
	ledger := []LedgerReservationView{
		{AllocationID: "alloc-running", Held: true},
		{AllocationID: "alloc-stopped", Held: true},
		{AllocationID: "alloc-never", Held: true},
		{AllocationID: "alloc-start-unclear", Held: true},
		{AllocationID: "alloc-stop-unclear", Held: true},
		{AllocationID: "alloc-ledger-only", Held: true},
		{AllocationID: "alloc-released", Held: false},
	}

	got := ReconcileNode(agent, ledger)
	by := make(map[string]ReconcileDecision, len(got))
	for _, d := range got {
		by[d.AllocationID] = d
	}

	want := map[string]ReconcileAction{
		"alloc-running":         ActionReservationStands,
		"alloc-stopped":         ActionReconcileRequired,
		"alloc-never":           ActionReconcileRequired,
		"alloc-start-unclear":   ActionReconcileRequired,
		"alloc-stop-unclear":    ActionReconcileRequired,
		"alloc-ledger-only":     ActionReconcileRequired,
		"alloc-foreign-running": ActionStopForeignExecution,
	}
	for id, action := range want {
		d, ok := by[id]
		if !ok {
			t.Fatalf("missing decision for %s", id)
		}
		if d.Action != action {
			t.Fatalf("%s: action = %q, want %q", id, d.Action, action)
		}
	}
	// The agent stopped work with NO live ledger record → no decision.
	if _, exists := by["alloc-foreign-stopped"]; exists {
		t.Fatal("a stopped agent record with no live reservation needs no decision")
	}
	// A ledger record that is not held + no agent record → no decision.
	if _, exists := by["alloc-released"]; exists {
		t.Fatal("a non-held ledger record needs no decision")
	}

	// Deterministic ordering (sorted by allocation ID).
	sorted := append([]ReconcileDecision(nil), got...)
	for i := 1; i < len(sorted); i++ {
		if sorted[i-1].AllocationID > sorted[i].AllocationID {
			t.Fatal("decisions must be sorted by allocation ID")
		}
	}
}

func TestReconcileNodeDeterministicAndIdempotent(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	agent := []AgentExecutionRecord{
		{AllocationID: "b", State: AgentExecRunning, ObservedAt: now},
		{AllocationID: "a", State: AgentExecStopped, ObservedAt: now},
		{AllocationID: "c", State: AgentExecStopUncertain, ObservedAt: now},
	}
	ledger := []LedgerReservationView{
		{AllocationID: "a", Held: true},
		{AllocationID: "b", Held: true},
		{AllocationID: "c", Held: true},
		{AllocationID: "d", Held: true},
	}
	first := ReconcileNode(agent, ledger)
	// Same inputs (shuffled ordering) → the same report (content AND order).
	shuffledAgent := []AgentExecutionRecord{agent[2], agent[0], agent[1]}
	shuffledLedger := []LedgerReservationView{ledger[3], ledger[1], ledger[0], ledger[2]}
	second := ReconcileNode(shuffledAgent, shuffledLedger)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("reconciliation must be input-order independent:\nfirst=%+v\nsecond=%+v", first, second)
	}
	// Repeated application converges (idempotency): the third run equals
	// the second — the engine is stateless, so "applying" it twice is
	// asking twice; convergence is structural.
	third := ReconcileNode(agent, ledger)
	if !reflect.DeepEqual(second, third) {
		t.Fatal("repeated reconciliation must converge")
	}
}

func TestReconcileNodeEmptyInputs(t *testing.T) {
	if got := ReconcileNode(nil, nil); len(got) != 0 {
		t.Fatalf("empty inputs must produce no decisions, got %+v", got)
	}
	agent := []AgentExecutionRecord{{AllocationID: "x", State: AgentExecStopped, ObservedAt: time.Now()}}
	if got := ReconcileNode(agent, nil); len(got) != 0 {
		t.Fatalf("stopped agent work with no ledger needs no decisions, got %+v", got)
	}
}
