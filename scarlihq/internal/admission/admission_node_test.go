// v20.0.0 (roadmap §10 "authenticated single- and multi-node execution"):
// the node-tagged admission proofs — the per-node snapshot seam (the
// fabric's §4.1 guards surfaced verbatim as reason codes), the PER-NODE
// capacity totals (the ledger is ONE authority, the node tag is the
// routing key — two nodes may carry the same GPU UUID without
// colliding), the node_id idempotency fingerprint binding, and the
// regression pin: a LOCAL request's decision path is byte-identical to
// v19.9.0 whether or not the seam is attached.
package admission

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// ---- fixtures -------------------------------------------------------------

// fixedNodeSnapshots is the injected per-node seam: the node's verified
// snapshot or its typed refusal (the fabric Registry's adapter shape).
type fixedNodeSnapshots struct {
	snaps map[string]*inventory.InventorySnapshotV2
	errs  map[string]error
}

func (s fixedNodeSnapshots) SnapshotFor(nodeID string, now time.Time) (*inventory.InventorySnapshotV2, error) {
	if err, ok := s.errs[nodeID]; ok {
		return nil, err
	}
	snap, ok := s.snaps[nodeID]
	if !ok {
		return nil, &NodeSnapshotRefusal{Code: ReasonNodeUnknown, Detail: "node " + nodeID + " has no observation"}
	}
	return snap, nil
}

// nodeSnapshot builds ONE node's verified observation: the same GPU UUID
// as the local host carries (the collision the per-node totals must
// survive), its own RAM/CPU/storage, its own snapshot identity.
func nodeSnapshot(id string, observed time.Time) *inventory.InventorySnapshotV2 {
	snap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "node" + id + "0000000000000000000001",
		ObservedAt:    observed,
		Host: storage.HostResources{
			CPUThreads:        12,
			RAMTotalBytes:     32 << 30,
			RAMAvailableBytes: 16 << 30,
		},
		GPUs: []inventory.GPU{
			// The SAME uuid as the local fixture's gpuA — two nodes, one
			// UUID, zero collision (the per-node totals key on the node).
			{ID: "gpu.remote.0", UUID: gpuA, VRAMTotalMB: 16384, VRAMFreeMB: 16384, Healthy: true},
		},
		Storage: []storage.Device{
			{
				ID: "storage.remote.0", Kind: storage.DeviceNVMe,
				CapacityBytes: 2 << 40, AvailableBytes: 1 << 40,
				MountPoints: []string{"/remote"},
				Healthy:     true,
				ReadProfile: &storage.ReadProfile{SequentialReadMBps: 3200, BlockSizeBytes: 1 << 20, MeasuredAt: observed.Add(-time.Hour), ProfileID: "bench-remote"},
			},
		},
		Runtimes: []inventory.Runtime{{ID: "sglang", Running: true, Healthy: true}},
		Models:   []inventory.ModelExecutionView{},
		Health:   []inventory.Health{},
		Sources:  []storage.SourceStatus{},
	}
	return snap
}

// nodeRequest adapts the base request onto the node's snapshot (the node's
// OWN device id; same GPU uuid — the collision the totals must survive).
func nodeRequest(node string) AdmissionRequest {
	req := baseRequest()
	req.NodeID = node
	req.Requirements.Storage = []StorageRequest{{DeviceID: "storage.remote.0", MinimumFreeBytes: 100 << 30}}
	return req
}

func (f *admissionFixture) nodeManager(t *testing.T, seam NodeSnapshots) *Manager {
	t.Helper()
	m := f.manager(t)
	m.SetNodeSnapshots(seam)
	return m
}

// ---- the proofs ------------------------------------------------------------

// TestAdmitNodeTaggedHappyPath: the node request routes to the node's
// verified snapshot, reserves, and the record carries the node tag.
func TestAdmitNodeTaggedHappyPath(t *testing.T) {
	f := newAdmissionFixture()
	observed := f.snap.ObservedAt
	m := f.nodeManager(t, fixedNodeSnapshots{
		snaps: map[string]*inventory.InventorySnapshotV2{"node-x": nodeSnapshot("x", observed)},
	})
	dec := m.Admit(nodeRequest("node-x"))
	if !dec.Allowed || dec.ReasonCode != ReasonOK {
		t.Fatalf("node-tagged admission refused: %s — %s", dec.ReasonCode, dec.Explanation)
	}
	if dec.SnapshotID != "nodex00000000000000000000001" && dec.SnapshotID != nodeSnapshot("x", observed).SnapshotID {
		t.Fatalf("decision must carry the NODE's snapshot id, got %s", dec.SnapshotID)
	}
	held := m.Held()
	if len(held) != 1 {
		t.Fatalf("one hold expected, got %d", len(held))
	}
	if held[0].NodeID != "node-x" {
		t.Fatalf("the record must carry the node tag, got %q", held[0].NodeID)
	}
}

// TestAdmitNodeSourceUnconfigured: a node request without the seam is
// the NAMED fail-closed refusal — never a locally-built guess.
func TestAdmitNodeSourceUnconfigured(t *testing.T) {
	f := newAdmissionFixture()
	m := f.manager(t) // NO seam attached
	req := baseRequest()
	req.NodeID = "node-x"
	dec := m.Admit(req)
	if dec.Allowed || dec.ReasonCode != ReasonNodeSourceUnconfigured {
		t.Fatalf("expected node-source-unconfigured, got %s", dec.ReasonCode)
	}
	if len(m.Held()) != 0 {
		t.Fatal("the refusal must reserve nothing")
	}
}

// TestAdmitNodeRefusalMapping: each typed seam refusal surfaces VERBATIM
// as the decision's reason_code; a plain error degrades to the generic
// node-snapshot-unavailable (still fail-closed).
func TestAdmitNodeRefusalMapping(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&NodeSnapshotRefusal{Code: ReasonNodeUnknown, Detail: "no observation"}, ReasonNodeUnknown},
		{&NodeSnapshotRefusal{Code: ReasonNodeQuarantined, Detail: "revoked"}, ReasonNodeQuarantined},
		{&NodeSnapshotRefusal{Code: ReasonNodeUnreachable, Detail: "capacity unknown"}, ReasonNodeUnreachable},
		{&NodeSnapshotRefusal{Code: ReasonNodeInventoryAbsent, Detail: "no snapshot yet"}, ReasonNodeInventoryAbsent},
		{&NodeSnapshotRefusal{Code: ReasonNodeInventoryStale, Detail: "31s old"}, ReasonNodeInventoryStale},
		{fmt.Errorf("an unexpected I/O failure"), ReasonNodeSnapshotUnavailable},
	}
	for i, tc := range cases {
		f := newAdmissionFixture()
		m := f.nodeManager(t, fixedNodeSnapshots{errs: map[string]error{"node-x": tc.err}})
		req := baseRequest()
		req.NodeID = "node-x"
		dec := m.Admit(req)
		if dec.Allowed || dec.ReasonCode != tc.want {
			t.Fatalf("case %d: expected %s, got %s (%s)", i, tc.want, dec.ReasonCode, dec.Explanation)
		}
	}
}

// TestAdmitNodeTaggedIsolatedTotals: the PER-NODE capacity accounting —
// a local hold never consumes a node's allocatable and vice versa (the
// same GPU UUID on both sides must not collide), while the node's OWN
// holds do consume the node's allocatable.
func TestAdmitNodeTaggedIsolatedTotals(t *testing.T) {
	f := newAdmissionFixture()
	observed := f.snap.ObservedAt
	m := f.nodeManager(t, fixedNodeSnapshots{
		snaps: map[string]*inventory.InventorySnapshotV2{"node-x": nodeSnapshot("x", observed)},
	})

	// (a) a LOCAL hold on gpuA (exclusive, 8 GiB).
	localReq := baseRequest() // gpuA, exclusive
	if dec := m.Admit(localReq); !dec.Allowed {
		t.Fatalf("local hold refused: %s — %s", dec.ReasonCode, dec.Explanation)
	}

	// (b) the node holds the SAME gpuA uuid (exclusive, 8 GiB) — no
	// collision with the local hold; the node's own observation rules.
	nodeReq := nodeRequest("node-x")
	nodeReq.RequestID = "req-node-1"
	nodeReq.Requirements.Ports = []int{30001} // different port (local holds 30000)
	if dec := m.Admit(nodeReq); !dec.Allowed {
		t.Fatalf("node hold refused (local hold must not collide): %s — %s", dec.ReasonCode, dec.Explanation)
	}

	// (c) a SECOND node hold on gpuA → the node's OWN exclusive conflict.
	nodeReq2 := nodeReq
	nodeReq2.RequestID = "req-node-2"
	if dec := m.Admit(nodeReq2); dec.Allowed || dec.ReasonCode != ReasonGPUExclusiveConflict {
		t.Fatalf("expected the node's own gpu-exclusive-conflict, got %s", dec.ReasonCode)
	}

	// (d) a SECOND local hold on gpuB still fits — the node's holds never
	// consumed the LOCAL totals (node RAM 12 GiB was never subtracted from
	// the local 16 GiB available).
	localReq2 := baseRequest()
	localReq2.RequestID = "req-local-2"
	localReq2.Requirements.GPU = []GPURequest{{GPUUUID: gpuB, VRAMBytes: 4 << 30, Exclusive: true}}
	localReq2.Requirements.Ports = []int{30002}
	if dec := m.Admit(localReq2); !dec.Allowed {
		t.Fatalf("local hold #2 refused (node holds must not consume local capacity): %s — %s", dec.ReasonCode, dec.Explanation)
	}

	// (e) the node's RAM is its own: a node request demanding 8 GiB RAM
	// still fits after the node's first hold took 4 GiB (16 − 4 − 1.6
	// safety = 10.4 allocatable), while the LOCAL holds took 4 GiB from
	// the local 16 GiB — the node's demand never saw the local holds.
	nodeReq3 := nodeRequest("node-x")
	nodeReq3.RequestID = "req-node-3"
	nodeReq3.Requirements.GPU = []GPURequest{} // no GPU — RAM-only demand
	nodeReq3.Requirements.RAMBytes = 8 << 30
	nodeReq3.Requirements.Ports = []int{30003}
	if dec := m.Admit(nodeReq3); !dec.Allowed {
		t.Fatalf("node RAM accounting wrong (node holds vs local holds crossed): %s — %s", dec.ReasonCode, dec.Explanation)
	}
}

// TestAdmitNodeLocalRegressionPin: the LOCAL decision path is
// byte-identical whether or not the seam is attached (v19.9.0
// behavior preserved; the record gains the explicit "local" tag).
func TestAdmitNodeLocalRegressionPin(t *testing.T) {
	build := func(withSeam bool) AdmissionDecision {
		f := newAdmissionFixture()
		observed := f.snap.ObservedAt
		var m *Manager
		if withSeam {
			m = f.nodeManager(t, fixedNodeSnapshots{
				snaps: map[string]*inventory.InventorySnapshotV2{"node-x": nodeSnapshot("x", observed)},
			})
		} else {
			m = f.manager(t)
		}
		return m.Admit(baseRequest())
	}
	without := build(false)
	with := build(true)
	if without.Allowed != true || with.Allowed != true {
		t.Fatalf("both local admits must be allowed: %v / %v", without.Allowed, with.Allowed)
	}
	if without.ReasonCode != with.ReasonCode || without.SnapshotID != with.SnapshotID ||
		without.PolicyVersion != with.PolicyVersion {
		t.Fatalf("local decision shape drifted with the seam attached: %+v vs %+v", without, with)
	}
	if (without.Assignment == nil) != (with.Assignment == nil) || without.Assignment.RAMBytes != with.Assignment.RAMBytes ||
		len(without.Assignment.GPUs) != len(with.Assignment.GPUs) {
		t.Fatalf("local assignment drifted with the seam attached")
	}
	// The explicit forms normalize to the same routing key.
	for _, id := range []string{"", "local", "LOCAL", " local "} {
		f := newAdmissionFixture()
		m := f.manager(t)
		req := baseRequest()
		req.NodeID = id
		dec := m.Admit(req)
		if !dec.Allowed {
			t.Fatalf("node_id %q must stay a local admission, got %s", id, dec.ReasonCode)
		}
		if held := m.Held(); len(held) != 1 || held[0].NodeID != "local" {
			t.Fatalf("node_id %q must record the normalized local tag, got %+v", id, held)
		}
	}
}

// TestAdmitNodeIdempotencyNodeBound: the idempotency fingerprint covers
// node_id — an exact replay (same node) returns the ORIGINAL decision;
// the same key against a DIFFERENT node is the NAMED conflict.
func TestAdmitNodeIdempotencyNodeBound(t *testing.T) {
	f := newAdmissionFixture()
	observed := f.snap.ObservedAt
	m := f.nodeManager(t, fixedNodeSnapshots{
		snaps: map[string]*inventory.InventorySnapshotV2{
			"node-x": nodeSnapshot("x", observed),
			"node-y": nodeSnapshot("y", observed),
		},
	})
	first := nodeRequest("node-x")
	first.IdempotencyKey = "key-1"
	dec1 := m.Admit(first)
	if !dec1.Allowed {
		t.Fatalf("first admission refused: %s", dec1.ReasonCode)
	}

	// The exact replay: the original decision stands, nothing new held.
	replay := first
	replay.RequestID = first.RequestID
	dec2 := m.Admit(replay)
	if !dec2.Allowed || dec2.AdmissionID != dec1.AdmissionID {
		t.Fatalf("replay must return the original admission %s, got %s", dec1.AdmissionID, dec2.AdmissionID)
	}
	if len(m.Held()) != 1 {
		t.Fatalf("replay must not double-hold, held=%d", len(m.Held()))
	}

	// The same key against a different node: the NAMED conflict.
	sameKey := first
	sameKey.NodeID = "node-y"
	dec3 := m.Admit(sameKey)
	if dec3.Allowed || dec3.ReasonCode != ReasonIdempotencyConflict {
		t.Fatalf("expected idempotency-conflict for the different-node replay, got %s", dec3.ReasonCode)
	}
	if len(m.Held()) != 1 {
		t.Fatalf("the conflict must reserve nothing, held=%d", len(m.Held()))
	}
}

// TestNodeHeldNormalization: the per-node totals filter keys on the
// NORMALIZED node id — a pre-v20.0.0 record (empty NodeID) counts as
// local alongside an explicit "local"; a node record counts only for
// its own node.
func TestNodeHeldNormalization(t *testing.T) {
	legacy := Record{NodeID: ""}        // a pre-v20.0.0 record
	explicit := Record{NodeID: "local"} // an explicit local record
	remote := Record{NodeID: "node-x"}  // a node record
	all := []Record{legacy, explicit, remote}

	if got := len(nodeHeld(all, "local")); got != 2 {
		t.Fatalf("local totals must cover the legacy + explicit records, got %d", got)
	}
	if got := len(nodeHeld(all, "node-x")); got != 1 {
		t.Fatalf("node-x totals must cover only its own record, got %d", got)
	}
	if got := len(nodeHeld(all, "node-y")); got != 0 {
		t.Fatalf("an unknown node honestly totals zero, got %d", got)
	}
	for _, id := range []string{"", "local", "Local", " LOCAL "} {
		if normalizeNodeID(id) != "local" {
			t.Fatalf("normalizeNodeID(%q) must be local", id)
		}
	}
	if normalizeNodeID(" Node-X ") != "node-x" {
		t.Fatalf("node ids normalize case-insensitively, got %q", normalizeNodeID(" Node-X "))
	}
}

// TestAdmitNodeLedgerRestart: the node tag SURVIVES the ledger round
// trip — a restarted manager reconstructs the node-tagged hold and still
// accounts it against the node's totals (the durable authority).
func TestAdmitNodeLedgerRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	f := newAdmissionFixture()
	observed := f.snap.ObservedAt

	l1, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	m1 := NewManager(DefaultPolicy(), fixedSnapshots{snap: f.snap}, f.models, l1)
	m1.SetNow(func() time.Time { return observed.Add(time.Second) })
	m1.SetNodeSnapshots(fixedNodeSnapshots{
		snaps: map[string]*inventory.InventorySnapshotV2{"node-x": nodeSnapshot("x", observed)},
	})
	if dec := m1.Admit(nodeRequest("node-x")); !dec.Allowed {
		t.Fatalf("node admission refused: %s — %s", dec.ReasonCode, dec.Explanation)
	}

	// Restart: a NEW manager over the same ledger file.
	l2, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	m2 := NewManager(DefaultPolicy(), fixedSnapshots{snap: f.snap}, f.models, l2)
	m2.SetNow(func() time.Time { return observed.Add(2 * time.Second) })
	m2.SetNodeSnapshots(fixedNodeSnapshots{
		snaps: map[string]*inventory.InventorySnapshotV2{"node-x": nodeSnapshot("x", observed)},
	})
	held := m2.Held()
	if len(held) != 1 || held[0].NodeID != "node-x" {
		t.Fatalf("the node-tagged hold must survive the restart, got %+v", held)
	}
	// The reconstructed node hold still consumes the NODE's allocatable
	// (a fresh node admission on the same GPU is the exclusive conflict).
	req2 := nodeRequest("node-x")
	req2.RequestID = "req-node-after-restart"
	req2.Requirements.Ports = []int{30004}
	if dec := m2.Admit(req2); dec.Allowed || dec.ReasonCode != ReasonGPUExclusiveConflict {
		t.Fatalf("the restarted node hold must still gate the node's GPU, got %s", dec.ReasonCode)
	}
	// And a LOCAL admission on the same UUID still fits (no cross-node
	// collision after the restart).
	req3 := baseRequest()
	req3.RequestID = "req-local-after-restart"
	req3.Requirements.Ports = []int{30005}
	if dec := m2.Admit(req3); !dec.Allowed {
		t.Fatalf("local capacity must be untouched by the node hold, got %s — %s", dec.ReasonCode, dec.Explanation)
	}
}
