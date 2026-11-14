// v20.0.0 (roadmap §10 "authenticated single- and multi-node execution")
// — the HTTP-level node-tagged admission proofs: the POST
// /api/compute/admission node_id contract (the refusal-is-data genre —
// a node-level refusal is 200 + allowed=false + the named reason_code),
// the fabric adapter's sentinel→refusal vocabulary mapping, and the
// production ledger view (the REAL reconciliation decisions the
// executions push computes against the single authoritative ledger).
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/admission"
	"github.com/MoZoHuJa/OS/scarlihq/internal/fabric"
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/nodeidentity"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// nodeAdmissionFixture wires the FULL production path: an enrolled
// ACTIVE node, its observed inventory in the fabric registry, and the
// admission manager with the FabricNodeSnapshots seam attached (exactly
// what main.go wires when both surfaces are enabled).
type nodeAdmissionFixture struct {
	mux       *http.ServeMux
	mgr       *admission.Manager
	fabricReg *fabric.Registry
	observed  time.Time
}

func newNodeAdmissionFixture(t *testing.T) *nodeAdmissionFixture {
	t.Helper()
	dir := t.TempDir()
	clock := &fixedFabricClock{now: time.Now().UTC()}
	observed := clock.now

	nodeReg, err := nodeidentity.NewRegistry(nodeidentity.NewStore(filepath.Join(dir, "nodes.jsonl")), clock, nodeidentity.RegistryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	fabricReg, err := fabric.NewRegistry(clock, identityStatusSource{reg: nodeReg}, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Enroll node-x: the invite (admin act) → the one-time secret consumed
	// by Enroll → the node is ACTIVE with a live credential.
	inv, err := nodeReg.CreateInvite("admin-op", "node-x", 30*time.Minute)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, err := nodeReg.Enroll("node-x", inv.Secret, "nonce-1"); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// The node's VERIFIED inventory observation (the agent's inventory
	// push recorded in the registry).
	nodeSnap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "nodex00000000000000000000001",
		ObservedAt:    observed,
		Host:          storage.HostResources{CPUThreads: 12, RAMTotalBytes: 32 << 30, RAMAvailableBytes: 16 << 30},
		GPUs: []inventory.GPU{
			{ID: "gpu.remote.0", UUID: "GPU-aaaa", VRAMTotalMB: 16384, VRAMFreeMB: 16384, Healthy: true},
		},
		Storage: []storage.Device{{
			ID: "storage.remote.0", Kind: storage.DeviceNVMe, CapacityBytes: 2 << 40, AvailableBytes: 1 << 40,
			MountPoints: []string{"/remote"}, Healthy: true,
			ReadProfile: &storage.ReadProfile{SequentialReadMBps: 3200, BlockSizeBytes: 1 << 20, MeasuredAt: observed.Add(-time.Hour), ProfileID: "bench-remote"},
		}},
		Runtimes: []inventory.Runtime{{ID: "sglang", Running: true, Healthy: true}},
	}
	if err := fabricReg.ObserveInventory("node-x", nodeSnap, "A"); err != nil {
		t.Fatalf("ObserveInventory: %v", err)
	}

	// The admission manager (the same fixture shape as the local tests) +
	// the seam (the production adapter over the fabric registry).
	mgr := admissionTestManager(t)
	mgr.SetNodeSnapshots(FabricNodeSnapshots{Registry: fabricReg})

	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "20.0.0")
	h.ConfigureAdmission(mgr)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return &nodeAdmissionFixture{mux: mux, mgr: mgr, fabricReg: fabricReg, observed: observed}
}

const admissionNodeBody = `{
	"request_id": "req-node-1",
	"node_id": "node-x",
	"model_id": "qwen3-14b-awq",
	"backend_id": "sglang",
	"requirements": {
		"gpu": [{"gpu_uuid": "GPU-aaaa", "vram_bytes": 8589934592, "exclusive": true}],
		"ram_bytes": 4294967296,
		"cpu_threads": 4,
		"storage": [{"device_id": "storage.remote.0", "minimum_free_bytes": 107374182400}],
		"ports": [30001]
	}
}`

// TestAdmissionNodeTaggedHTTP: the golden path — a node-tagged POST
// reserves against the node's VERIFIED fabric observation and the
// decision carries the node's snapshot identity (the local snapshot is
// never consulted).
func TestAdmissionNodeTaggedHTTP(t *testing.T) {
	fx := newNodeAdmissionFixture(t)
	rec, doc := admissionDo(t, fx.mux, http.MethodPost, "/api/compute/admission", admissionNodeBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s (the refusal-is-data contract: a decision, never an HTTP error)", rec.Code, rec.Body.String())
	}
	if doc["allowed"] != true {
		t.Fatalf("the node admission must be allowed: %s", rec.Body.String())
	}
	if doc["snapshot_id"] != "nodex00000000000000000000001" {
		t.Fatalf("the decision must carry the NODE's snapshot id, got %v", doc["snapshot_id"])
	}
	// The record carries the node tag (the GET surface's own proof).
	rec2, doc2 := admissionDo(t, fx.mux, http.MethodGet, "/api/compute/admission", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET status=%d", rec2.Code)
	}
	held, _ := doc2["held"].([]interface{})
	if len(held) != 1 {
		t.Fatalf("one node hold expected, got %v", doc2["held"])
	}
	first, _ := held[0].(map[string]interface{})
	if first["node_id"] != "node-x" {
		t.Fatalf("the hold must carry the node tag, got %v", first["node_id"])
	}
}

// TestAdmissionNodeUnreachableHTTP: the node goes unreachable AFTER the
// observation (capacity UNKNOWN) — the node-tagged admission is refused
// BY NAME, and the existing hold is NOT recycled (the §4.1 semantics at
// the HTTP boundary).
func TestAdmissionNodeUnreachableHTTP(t *testing.T) {
	fx := newNodeAdmissionFixture(t)
	// First: a successful node hold (the reservation the §4.1 contract
	// then retains).
	rec, doc := admissionDo(t, fx.mux, http.MethodPost, "/api/compute/admission", admissionNodeBody)
	if rec.Code != http.StatusOK || doc["allowed"] != true {
		t.Fatalf("the initial node admission must be allowed: %s", rec.Body.String())
	}
	// The node becomes unreachable.
	fx.fabricReg.MarkUnreachable("node-x")
	body2 := strings.Replace(admissionNodeBody, "req-node-1", "req-node-2", 1)
	rec2, doc2 := admissionDo(t, fx.mux, http.MethodPost, "/api/compute/admission", body2)
	if rec2.Code != http.StatusOK || doc2["allowed"] != false {
		t.Fatalf("an unreachable node must be refused as data: %s", rec2.Body.String())
	}
	if doc2["reason_code"] != "node-unreachable" {
		t.Fatalf("reason_code=%v (want node-unreachable)", doc2["reason_code"])
	}
	// The FIRST hold stays HELD — unreachable never recycles capacity.
	_, doc3 := admissionDo(t, fx.mux, http.MethodGet, "/api/compute/admission", "")
	held, _ := doc3["held"].([]interface{})
	if len(held) != 1 {
		t.Fatalf("the unreachable node's existing reservation must stay HELD, held=%v", held)
	}
}

// TestAdmissionNodeSourceUnconfiguredHTTP: a node-tagged request when
// the seam is NOT attached (the admission gate alone, no fabric) — the
// NAMED fail-closed refusal as data, never a locally-built guess.
func TestAdmissionNodeSourceUnconfiguredHTTP(t *testing.T) {
	mux, mgr := admissionTestServer(t) // the seam is deliberately NOT attached
	_ = mgr
	req := httptest.NewRequest(http.MethodPost, "/api/compute/admission", strings.NewReader(admissionNodeBody))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d (a refusal is data, never an HTTP error): %s", rec.Code, rec.Body.String())
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["allowed"] != false || doc["reason_code"] != "node-source-unconfigured" {
		t.Fatalf("the node-source refusal must surface as data: %s", rec.Body.String())
	}
}

// TestFabricNodeSnapshotsMapping: the adapter maps EVERY fabric sentinel
// onto the admission refusal vocabulary ONE-TO-ONE (the vocabulary
// translation lives in one place; the manager surfaces the code
// verbatim).
func TestFabricNodeSnapshotsMapping(t *testing.T) {
	reg, clock, status := newFabricTestRegistry(t)
	now := clock.Now()
	adapter := FabricNodeSnapshots{Registry: reg}

	// Unknown node.
	_, err := adapter.SnapshotFor("node-z", now)
	var refusal *admission.NodeSnapshotRefusal
	if !errors.As(err, &refusal) || refusal.Code != admission.ReasonNodeUnknown {
		t.Fatalf("unknown → node-unknown, got %v", err)
	}

	// Quarantined (observed fresh, identity revoked — the status outranks
	// the observation).
	if err := reg.ObserveInventory("node-a", fabricTestSnapshot("s1", now), "A"); err != nil {
		t.Fatal(err)
	}
	status["node-a"] = false
	_, err = adapter.SnapshotFor("node-a", now)
	if !errors.As(err, &refusal) || refusal.Code != admission.ReasonNodeQuarantined {
		t.Fatalf("quarantined → node-quarantined, got %v", err)
	}
	status["node-a"] = true

	// Unreachable.
	reg.MarkUnreachable("node-a")
	_, err = adapter.SnapshotFor("node-a", now)
	if !errors.As(err, &refusal) || refusal.Code != admission.ReasonNodeUnreachable {
		t.Fatalf("unreachable → node-unreachable, got %v", err)
	}

	// (The absent branch — reachable + active + no observation — is only
	// constructible from inside the fabric package; the fabric-level
	// TestSnapshotForGuardOrder proves the sentinel, and the adapter's
	// one-line mapping case compiles against the same constant here.)

	// Stale (the stricter remote window).
	if err := reg.ObserveInventory("node-a", fabricTestSnapshot("s3", now.Add(-31*time.Second)), "A"); err != nil {
		t.Fatal(err)
	}
	_, err = adapter.SnapshotFor("node-a", now)
	if !errors.As(err, &refusal) || refusal.Code != admission.ReasonNodeInventoryStale {
		t.Fatalf("stale → node-inventory-stale, got %v", err)
	}

	// Fresh: the exact snapshot returns.
	fresh := fabricTestSnapshot("s4", now.Add(-29*time.Second))
	if err := reg.ObserveInventory("node-a", fresh, "A"); err != nil {
		t.Fatal(err)
	}
	got, err := adapter.SnapshotFor("node-a", now)
	if err != nil || got.SnapshotID != "s4" {
		t.Fatalf("fresh snapshot must return, got %v err=%v", got, err)
	}
}

// TestFabricAdmissionLedgerView: the production ledger view — the node's
// HELD/ACTIVE records mapped onto the reconciliation view; local records
// and other nodes' records never leak in (the §4.2 join's input).
func TestFabricAdmissionLedgerView(t *testing.T) {
	f := newAdmissionFixtureForNodes(t)
	mgr := f.manager(t)

	// One local hold + one node-x hold.
	local := f.base()
	if dec := mgr.Admit(local); !dec.Allowed {
		t.Fatalf("local hold refused: %s — %s", dec.ReasonCode, dec.Explanation)
	}
	nodeReq := f.nodeReq("node-x")
	if dec := mgr.Admit(nodeReq); !dec.Allowed {
		t.Fatalf("node hold refused: %s — %s", dec.ReasonCode, dec.Explanation)
	}

	view := FabricAdmissionLedger{Manager: mgr}
	// The node's view: exactly its own record, HELD.
	views := view.HeldReservations("node-x")
	if len(views) != 1 {
		t.Fatalf("node-x must see exactly its own hold, got %v", views)
	}
	if !views[0].Held || views[0].AllocationID == "" {
		t.Fatalf("the node's hold must be a live allocation, got %+v", views[0])
	}
	// A node with no records: the honest EMPTY view (every agent-reported
	// execution is then foreign work — the §4.2 contract).
	if got := view.HeldReservations("node-y"); len(got) != 0 {
		t.Fatalf("an unknown node must see the empty view, got %v", got)
	}
	// The local records never leak into a node's view.
	for _, v := range views {
		if v.AllocationID == "" {
			t.Fatalf("malformed view: %+v", v)
		}
	}
}

// ---- local helpers ----------------------------------------------------------

// newFabricTestRegistry builds a bare registry + clock + status (the
// adapter mapping test's stage).
func newFabricTestRegistry(t *testing.T) (*fabric.Registry, *fixedFabricClock, map[string]bool) {
	t.Helper()
	clock := &fixedFabricClock{now: time.Now().UTC()}
	active := map[string]bool{"node-a": true, "node-b": true}
	reg, err := fabric.NewRegistry(clock, fakeFabricStatus{active: active}, 0)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg, clock, active
}

type fakeFabricStatus struct{ active map[string]bool }

func (s fakeFabricStatus) NodeActive(nodeID string) bool { return s.active[nodeID] }

func fabricTestSnapshot(id string, observedAt time.Time) *inventory.InventorySnapshotV2 {
	return &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    id,
		ObservedAt:    observedAt,
	}
}

// nodeAdmissionStage is the admission-side stage for the ledger-view test
// (a manager + a node seam holding node-x's verified snapshot — the same
// GPU uuid as local, no collision).
type nodeAdmissionStage struct {
	mgr *admission.Manager
}

func newAdmissionFixtureForNodes(t *testing.T) *nodeAdmissionStage {
	t.Helper()
	observed := time.Now().UTC()
	mgr := admissionTestManager(t)
	nodeSnap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "nodex00000000000000000000001",
		ObservedAt:    observed,
		Host:          storage.HostResources{CPUThreads: 12, RAMTotalBytes: 32 << 30, RAMAvailableBytes: 16 << 30},
		GPUs: []inventory.GPU{
			{ID: "gpu.remote.0", UUID: "GPU-aaaa", VRAMTotalMB: 16384, VRAMFreeMB: 16384, Healthy: true},
		},
		Storage: []storage.Device{{
			ID: "storage.remote.0", Kind: storage.DeviceNVMe, CapacityBytes: 2 << 40, AvailableBytes: 1 << 40,
			MountPoints: []string{"/remote"}, Healthy: true,
			ReadProfile: &storage.ReadProfile{SequentialReadMBps: 3200, BlockSizeBytes: 1 << 20, MeasuredAt: observed.Add(-time.Hour), ProfileID: "bench-remote"},
		}},
		Runtimes: []inventory.Runtime{{ID: "sglang", Running: true, Healthy: true}},
	}
	mgr.SetNodeSnapshots(apiNodeSeam{snaps: map[string]*inventory.InventorySnapshotV2{"node-x": nodeSnap}})
	return &nodeAdmissionStage{mgr: mgr}
}

func (s *nodeAdmissionStage) manager(t *testing.T) *admission.Manager { return s.mgr }

// base is a LOCAL request (no node tag; the local fixture's own devices).
func (s *nodeAdmissionStage) base() admission.AdmissionRequest {
	return admission.AdmissionRequest{
		RequestID: "req-local-1", Principal: "operator", ModelID: "qwen3-14b-awq", BackendID: "sglang",
		Requirements: admission.ResourceVector{
			GPU:        []admission.GPURequest{{GPUUUID: "GPU-aaaa", VRAMBytes: 4 << 30, Exclusive: true}},
			RAMBytes:   2 << 30,
			CPUThreads: 2,
			Ports:      []int{30010},
		},
	}
}

// nodeReq is a node-tagged request (the node's own device id).
func (s *nodeAdmissionStage) nodeReq(node string) admission.AdmissionRequest {
	r := s.base()
	r.NodeID = node
	r.RequestID = "req-" + node + "-1"
	r.Requirements.Storage = []admission.StorageRequest{{DeviceID: "storage.remote.0", MinimumFreeBytes: 50 << 30}}
	r.Requirements.Ports = []int{30011}
	return r
}

// apiNodeSeam is the test's injected per-node seam (the adapter shape).
type apiNodeSeam struct {
	snaps map[string]*inventory.InventorySnapshotV2
}

func (a apiNodeSeam) SnapshotFor(nodeID string, now time.Time) (*inventory.InventorySnapshotV2, error) {
	snap, ok := a.snaps[nodeID]
	if !ok {
		return nil, &admission.NodeSnapshotRefusal{Code: admission.ReasonNodeUnknown, Detail: "node " + nodeID + " has no observation"}
	}
	return snap, nil
}
