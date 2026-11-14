// v20.0.0 (roadmap §10) — the control-plane surfaces' proofs: the
// resource accounting's CLOSED label vocabulary (observed / estimated /
// reserved / allocatable / unknown / stale — the §10.2 DoD row), the
// allocatable math (observed free − policy headroom − reserved, with the
// KV counted exactly once the way the admission gate counts it), the
// stale/unknown honesty (allocatable is NULL there, never a guess), the
// fabric node census with the retained-snapshot rule, and the
// capability summary's honest states.
package api

import (
	"encoding/json"
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

// ---- the fixtures ------------------------------------------------------------

// resourcesFixture: a handler with the inventory seam (+ optionally the
// admission gate with a node seam), one fresh local snapshot carrying
// the same shape the admission tests use.
type resourcesFixture struct {
	h        *Handler
	mux      *http.ServeMux
	observed time.Time
}

func newResourcesFixture(t *testing.T, withAdmission bool) *resourcesFixture {
	t.Helper()
	observed := time.Now().UTC()
	snap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "snap0000000000000000000000000001",
		ObservedAt:    observed,
		Host:          storage.HostResources{CPUThreads: 16, RAMTotalBytes: 32 << 30, RAMAvailableBytes: 16 << 30},
		GPUs: []inventory.GPU{
			{ID: "gpu.nvidia.0", UUID: "GPU-aaaa", VRAMTotalMB: 16384, VRAMFreeMB: 16384, Healthy: true},
		},
		Storage: []storage.Device{{
			ID: "storage.nvme.0", Kind: storage.DeviceNVMe, CapacityBytes: 2 << 40, AvailableBytes: 1 << 40,
			MountPoints: []string{"/"}, Healthy: true,
			ReadProfile: &storage.ReadProfile{SequentialReadMBps: 3200, BlockSizeBytes: 1 << 20, MeasuredAt: observed.Add(-time.Hour), ProfileID: "bench-1"},
		}},
		Runtimes: []inventory.Runtime{{ID: "sglang", Running: true, Healthy: true}},
		Models:   []inventory.ModelExecutionView{},
	}
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "20.0.0")
	h.ConfigureInventoryV2(func() (*inventory.InventorySnapshotV2, error) { return snap, nil })
	if withAdmission {
		h.ConfigureAdmission(admissionTestManager(t))
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return &resourcesFixture{h: h, mux: mux, observed: observed}
}

func (f *resourcesFixture) get(t *testing.T, path string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	var doc map[string]interface{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("response is not JSON: %s", rec.Body.String())
		}
	}
	return rec, doc
}

// nodesOf extracts the nodes array (typed accessors over interface{}).
func nodesOf(doc map[string]interface{}) []map[string]interface{} {
	raw, _ := doc["nodes"].([]interface{})
	out := make([]map[string]interface{}, 0, len(raw))
	for _, n := range raw {
		m, _ := n.(map[string]interface{})
		out = append(out, m)
	}
	return out
}

// ---- the resources proofs ---------------------------------------------------

// TestResourcesRouteRequiresInventory: the surface fails closed —
// without the inventory seam it is the NAMED 503, never an empty view.
func TestResourcesRouteRequiresInventory(t *testing.T) {
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "20.0.0")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/compute/resources", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s (want 503 — the fail-closed contract)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "inventory source not configured") {
		t.Fatalf("the 503 must name the seam: %s", rec.Body.String())
	}
}

// TestResourcesLabelsContract: the CLOSED vocabulary is carried
// machine-readably (the §10.2 DoD row: "UI distinguishes
// observed/estimated/reserved/allocatable") — the local node's view
// carries every label in its place.
func TestResourcesLabelsContract(t *testing.T) {
	f := newResourcesFixture(t, true)
	rec, doc := f.get(t, "/api/compute/resources")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	labels, _ := doc["labels"].([]interface{})
	want := map[string]bool{"observed": false, "estimated": false, "reserved": false, "allocatable": false, "unknown": false, "stale": false}
	for _, l := range labels {
		if s, ok := l.(string); ok {
			if _, known := want[s]; known {
				want[s] = true
			}
		}
	}
	for l, seen := range want {
		if !seen {
			t.Fatalf("the closed label vocabulary must carry %q (the §10.2 contract): %v", l, labels)
		}
	}
	nodes := nodesOf(doc)
	if len(nodes) != 1 || nodes[0]["node_id"] != "local" {
		t.Fatalf("the local node must render, got %v", nodes)
	}
	local := nodes[0]
	if local["node_state"] != "local" {
		t.Fatalf("node_state=%v", local["node_state"])
	}
	// The observed/reserved/allocatable views are present and the
	// freshness window is the local 60s.
	fresh, _ := local["freshness"].(map[string]interface{})
	if fresh["window"] != "local" {
		t.Fatalf("freshness window=%v", fresh["window"])
	}
	if _, ok := local["observed"]; !ok {
		t.Fatal("the observed view must be present")
	}
	if _, ok := local["reserved"]; !ok {
		t.Fatal("the reserved view must be present")
	}
	if _, ok := local["allocatable"]; !ok {
		t.Fatal("the allocatable view must be present (a fresh node)")
	}
	// Reservations are configured → the honest true.
	if doc["reservations_configured"] != true {
		t.Fatalf("reservations_configured=%v", doc["reservations_configured"])
	}
	if policy, _ := doc["policy"].(map[string]interface{}); policy["source"] != "active" {
		t.Fatalf("policy source=%v (the admission gate is enabled here)", policy["source"])
	}
}

// TestResourcesAllocatableMath: the computed label's arithmetic —
// allocatable = observed free − policy headroom − reserved, with the KV
// counted EXACTLY once (the admission gate's own accounting), and the
// observed/reserved numbers the decision named.
func TestResourcesAllocatableMath(t *testing.T) {
	f := newResourcesFixture(t, true)
	mgr := f.h.admission
	// One hold: GPU-aaaa 8 GiB VRAM + 2 GiB KV (charged to the GPU),
	// 4 GiB RAM, 4 CPU threads, 100 GiB on the NVMe device.
	dec := mgr.Admit(admission.AdmissionRequest{
		RequestID: "req-1", Principal: "operator", ModelID: "qwen3-14b-awq", BackendID: "sglang",
		Requirements: admission.ResourceVector{
			GPU:          []admission.GPURequest{{GPUUUID: "GPU-aaaa", VRAMBytes: 8 << 30, Exclusive: true}},
			KVCacheBytes: 2 << 30,
			RAMBytes:     4 << 30,
			CPUThreads:   4,
			Storage:      []admission.StorageRequest{{DeviceID: "storage.nvme.0", MinimumFreeBytes: 100 << 30}},
			Ports:        []int{30000},
		},
	})
	if !dec.Allowed {
		t.Fatalf("the fixture hold must be admitted: %s — %s", dec.ReasonCode, dec.Explanation)
	}
	_, doc := f.get(t, "/api/compute/resources")
	local := nodesOf(doc)[0]

	policy := admission.DefaultPolicy()
	reserved, _ := local["reserved"].(map[string]interface{})
	if reserved["holds"] != float64(1) {
		t.Fatalf("holds=%v", reserved["holds"])
	}
	if reserved["ram_bytes"] != float64(4<<30) {
		t.Fatalf("reserved ram=%v", reserved["ram_bytes"])
	}

	alloc, _ := local["allocatable"].(map[string]interface{})
	// GPU: free 16 GiB − safety 5% of TOTAL (0.8 GiB) − reserved VRAM 8
	// GiB − KV 2 GiB (the gate counts KV against the GPU exactly once —
	// the same accounting the view must show).
	gpus, _ := alloc["gpus"].([]interface{})
	first, _ := gpus[0].(map[string]interface{})
	wantVRAM := uint64(16384<<20) - uint64(float64(uint64(16384<<20))*policy.VRAMSafetyFraction) - 8<<30 - 2<<30
	if uint64(first["vram_bytes"].(float64)) != wantVRAM {
		t.Fatalf("gpu allocatable=%v want %d (free − 5%% total-safety − 8GiB VRAM − 2GiB KV)", first["vram_bytes"], wantVRAM)
	}
	// RAM: available 16 GiB − 10% safety − 4 GiB reserved.
	wantRAM := uint64(16<<30) - uint64(float64(uint64(16<<30))*policy.RAMSafetyFraction) - 4<<30
	if uint64(alloc["ram_bytes"].(float64)) != wantRAM {
		t.Fatalf("ram allocatable=%v want %d", alloc["ram_bytes"], wantRAM)
	}
	// CPU: 16 − 2 OS safety − 4 reserved.
	if alloc["cpu_threads"] != float64(10) {
		t.Fatalf("cpu allocatable=%v want 10", alloc["cpu_threads"])
	}
	// Storage: available 1 TiB − 10% of 2 TiB capacity − 100 GiB reserved.
	wantStorage := uint64(1<<40) - uint64(float64(uint64(2<<40))*policy.StorageSafetyFraction) - 100<<30
	storages, _ := alloc["storage"].([]interface{})
	st, _ := storages[0].(map[string]interface{})
	if uint64(st["bytes"].(float64)) != wantStorage {
		t.Fatalf("storage allocatable=%v want %d", st["bytes"], wantStorage)
	}
}

// TestResourcesStaleSnapshotNeverGuesses: a stale local snapshot renders
// OBSERVED + STALE with the allocatable NULL — aged free figures are
// never reused as capacity (KPK-19.7.0-P2-01's rule at the view layer).
func TestResourcesStaleSnapshotNeverGuesses(t *testing.T) {
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "20.0.0")
	old := time.Now().UTC().Add(-2 * time.Minute) // stale past the 60s window
	snap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2, SnapshotID: "stale000000000000000000000001",
		ObservedAt: old,
		Host:       storage.HostResources{CPUThreads: 16, RAMTotalBytes: 32 << 30, RAMAvailableBytes: 16 << 30},
	}
	h.ConfigureInventoryV2(func() (*inventory.InventorySnapshotV2, error) { return snap, nil })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/compute/resources", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var doc map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	local := nodesOf(doc)[0]
	labels, _ := local["labels"].([]interface{})
	hasStale := false
	for _, l := range labels {
		if l == "stale" {
			hasStale = true
		}
	}
	if !hasStale {
		t.Fatalf("a stale node must carry the stale label: %v", labels)
	}
	if _, has := local["allocatable"]; has {
		t.Fatal("a stale node's allocatable must be NULL — never a guess from aged figures")
	}
	fresh, _ := local["freshness"].(map[string]interface{})
	if fresh["stale"] != true {
		t.Fatalf("freshness.stale=%v", fresh["stale"])
	}
}

// TestResourcesUnconfiguredAdmissionHonestZero: without the admission
// gate the reserved view is the honest ZERO with
// reservations_configured=false (nothing can reserve without the gate —
// zero is a fact) and the policy source is labeled "default".
func TestResourcesUnconfiguredAdmissionHonestZero(t *testing.T) {
	f := newResourcesFixture(t, false)
	_, doc := f.get(t, "/api/compute/resources")
	if doc["reservations_configured"] != false {
		t.Fatalf("reservations_configured=%v", doc["reservations_configured"])
	}
	if policy, _ := doc["policy"].(map[string]interface{}); policy["source"] != "default" {
		t.Fatalf("policy source=%v", policy["source"])
	}
	local := nodesOf(doc)[0]
	reserved, _ := local["reserved"].(map[string]interface{})
	if reserved["holds"] != float64(0) {
		t.Fatalf("holds=%v (nothing can reserve without the gate)", reserved["holds"])
	}
}

// TestResourcesFabricNodesCensus: the fabric nodes render with their own
// labels — a fresh remote node carries allocatable under the STRICTER
// remote window; an unreachable node renders capacity UNKNOWN with the
// retained snapshot's own labels and allocatable NULL (the §9.5 rule:
// never recycled, never forgotten).
func TestResourcesFabricNodesCensus(t *testing.T) {
	f := newResourcesFixture(t, false)
	// Wire a minimal fabric: an enrolled active node with a fresh
	// observation + a node that went unreachable after its observation.
	dir := t.TempDir()
	clock := &fixedFabricClock{now: time.Now().UTC()}
	nodeReg, err := nodeidentityForFabric(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	fabricReg, err := fabricRegistryForCensus(nodeReg, clock)
	if err != nil {
		t.Fatal(err)
	}
	freshSnap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2, SnapshotID: "remote00000000000000000000001",
		ObservedAt: clock.now,
		Host:       storage.HostResources{CPUThreads: 8, RAMTotalBytes: 16 << 30, RAMAvailableBytes: 8 << 30},
		GPUs:       []inventory.GPU{{ID: "gpu.remote.0", UUID: "GPU-rrrr", VRAMTotalMB: 8192, VRAMFreeMB: 8192, Healthy: true}},
	}
	if err := fabricReg.ObserveInventory("node-r", freshSnap, "A"); err != nil {
		t.Fatal(err)
	}
	retainedSnap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2, SnapshotID: "retained0000000000000000000001",
		ObservedAt: clock.now.Add(-10 * time.Second),
		Host:       storage.HostResources{CPUThreads: 8, RAMTotalBytes: 16 << 30, RAMAvailableBytes: 8 << 30},
	}
	if err := fabricReg.ObserveInventory("node-u", retainedSnap, "B"); err != nil {
		t.Fatal(err)
	}
	fabricReg.MarkUnreachable("node-u")
	// The view-level wiring: the handler fields set directly (the view
	// rides the registries; the planner construction is the plan route's
	// own surface, covered by the fabric plan tests).
	f.h.fabricNodes = nodeReg
	f.h.fabricRegistry = fabricReg

	_, doc := f.get(t, "/api/compute/resources")
	nodes := nodesOf(doc)
	if len(nodes) != 3 {
		t.Fatalf("local + node-r + node-u expected, got %d nodes", len(nodes))
	}
	byID := map[string]map[string]interface{}{}
	for _, n := range nodes {
		byID[n["node_id"].(string)] = n
	}
	// The fresh remote node: allocatable present, window "remote".
	if _, has := byID["node-r"]["allocatable"]; !has {
		t.Fatal("a fresh remote node must carry allocatable")
	}
	if fresh, _ := byID["node-r"]["freshness"].(map[string]interface{}); fresh["window"] != "remote" {
		t.Fatalf("remote window=%v", fresh["window"])
	}
	// The unreachable node: state + note + NULL allocatable.
	if byID["node-u"]["node_state"] != "unreachable" {
		t.Fatalf("node_state=%v", byID["node-u"]["node_state"])
	}
	if _, has := byID["node-u"]["allocatable"]; has {
		t.Fatal("an unreachable node's allocatable must be NULL (capacity UNKNOWN)")
	}
	note, _ := byID["node-u"]["note"].(string)
	if !strings.Contains(note, "reservations stay HELD") {
		t.Fatalf("the §9.5 note must stand: %q", note)
	}
}

// ---- the controlplane proofs ------------------------------------------------

// TestControlplaneCapabilityStates: the capability rows are honest —
// without the admission gate the row says unconfigured; with the gate
// AND the seam the node-tagged row says configured (the v20.0.0 live
// path); the fabric census counts the states; the labels vocabulary
// rides along.
func TestControlplaneCapabilityStates(t *testing.T) {
	f := newResourcesFixture(t, true)
	// The node-tagged row WITHOUT the seam attached — must say
	// unconfigured (the honest state; the flip test below attaches it).
	_, doc := f.get(t, "/api/controlplane")
	rows := capabilityRows(doc)
	for _, name := range []string{"versioned-inventory", "model-artifact-registry", "backend-capability-registry", "deterministic-planner", "aggregate-admission", "durable-reservations", "node-tagged-admission", "fabric", "typed-workload-lifecycle", "infer-ssd", "expert-packs", "ai-proposal-workflow", "approvals", "audit", "ai-runtime", "recovery-and-rollback", "resource-accounting"} {
		if _, ok := rows[name]; !ok {
			t.Fatalf("the §10.1 checklist must carry row %q: %v", name, rows)
		}
	}
	if rows["aggregate-admission"] != "configured" {
		t.Fatalf("admission is wired in this fixture: %v", rows["aggregate-admission"])
	}
	if rows["node-tagged-admission"] != "unconfigured" {
		t.Fatalf("the seam is NOT attached in this fixture — the row must say unconfigured: %v", rows["node-tagged-admission"])
	}
	if adm, _ := doc["admission"].(map[string]interface{}); adm["node_tagged"] != false {
		t.Fatalf("admission census node_tagged=%v", adm["node_tagged"])
	}
}

// TestControlplaneNodeTaggedRowFlips: with the seam attached the
// node-tagged row + the census flip to configured/true (the production
// act made visible — the monitor's own signal).
func TestControlplaneNodeTaggedRowFlips(t *testing.T) {
	f := newResourcesFixture(t, true)
	nodeSnap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2, SnapshotID: "nodeq00000000000000000000001",
		ObservedAt: f.observed,
		Host:       storage.HostResources{CPUThreads: 8, RAMTotalBytes: 16 << 30, RAMAvailableBytes: 8 << 30},
	}
	f.h.admission.SetNodeSnapshots(apiNodeSeam{snaps: map[string]*inventory.InventorySnapshotV2{"node-q": nodeSnap}})
	_, doc := f.get(t, "/api/controlplane")
	rows := capabilityRows(doc)
	if rows["node-tagged-admission"] != "configured" {
		t.Fatalf("the seam is attached — the row must say configured: %v", rows["node-tagged-admission"])
	}
	if adm, _ := doc["admission"].(map[string]interface{}); adm["node_tagged"] != true {
		t.Fatalf("admission census node_tagged=%v", adm["node_tagged"])
	}
}

// TestControlplaneFabricCensus: the census counts the node states and
// the runbook row stays documented (the operator's acts are never
// claimed as process state).
func TestControlplaneFabricCensus(t *testing.T) {
	f := newResourcesFixture(t, false)
	dir := t.TempDir()
	clock := &fixedFabricClock{now: time.Now().UTC()}
	nodeReg, err := nodeidentityForFabric(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	fabricReg, err := fabricRegistryForCensus(nodeReg, clock)
	if err != nil {
		t.Fatal(err)
	}
	snap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2, SnapshotID: "census0000000000000000000001",
		ObservedAt: clock.now,
		Host:       storage.HostResources{CPUThreads: 8},
	}
	if err := fabricReg.ObserveInventory("node-r", snap, "A"); err != nil {
		t.Fatal(err)
	}
	if err := fabricReg.ObserveInventory("node-u", snap, "B"); err != nil {
		t.Fatal(err)
	}
	fabricReg.MarkUnreachable("node-u")
	f.h.fabricNodes = nodeReg
	f.h.fabricRegistry = fabricReg
	_, doc := f.get(t, "/api/controlplane")
	fab, _ := doc["fabric"].(map[string]interface{})
	counts, _ := fab["counts"].(map[string]interface{})
	if counts["active"] != float64(1) || counts["unreachable"] != float64(1) {
		t.Fatalf("census counts=%v", counts)
	}
	rows := capabilityRows(doc)
	if rows["recovery-and-rollback"] != "documented" {
		t.Fatalf("the runbook row must stay documented: %v", rows["recovery-and-rollback"])
	}
}

// TestControlplaneAuthAndMethods: the READ gate (401 without the token)
// + the method guard (405 on POST).
func TestControlplaneAuthAndMethods(t *testing.T) {
	f := newResourcesFixture(t, false)
	req := httptest.NewRequest(http.MethodGet, "/api/controlplane", nil)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d (want 401 — the READ gate)", rec.Code)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/api/controlplane", nil)
	req2.Header.Set("Authorization", "Bearer secret")
	rec2 := httptest.NewRecorder()
	f.mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d (want 405 — read-only)", rec2.Code)
	}
	req3 := httptest.NewRequest(http.MethodPost, "/api/compute/resources", nil)
	req3.Header.Set("Authorization", "Bearer secret")
	rec3 := httptest.NewRecorder()
	f.mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d (want 405 — read-only)", rec3.Code)
	}
}

// ---- helpers -----------------------------------------------------------------

func capabilityRows(doc map[string]interface{}) map[string]string {
	rows := map[string]string{}
	raw, _ := doc["capabilities"].([]interface{})
	for _, r := range raw {
		m, _ := r.(map[string]interface{})
		if m == nil {
			continue
		}
		rows[m["name"].(string)] = m["state"].(string)
	}
	return rows
}

// nodeidentityForFabric builds a registry with one enrolled ACTIVE node
// pair (node-r + node-u) for the census fixtures.
func nodeidentityForFabric(dir string, clock *fixedFabricClock) (*nodeidentity.Registry, error) {
	reg, err := nodeidentity.NewRegistry(nodeidentity.NewStore(filepath.Join(dir, "nodes.jsonl")), clock, nodeidentity.RegistryConfig{})
	if err != nil {
		return nil, err
	}
	for _, id := range []string{"node-r", "node-u"} {
		inv, err := reg.CreateInvite("admin-op", id, 30*time.Minute)
		if err != nil {
			return nil, err
		}
		if _, err := reg.Enroll(id, inv.Secret, "nonce-"+id); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

// fabricRegistryForCensus builds the registry over the identity source.
func fabricRegistryForCensus(nodeReg *nodeidentity.Registry, clock *fixedFabricClock) (*fabric.Registry, error) {
	return fabric.NewRegistry(clock, identityStatusSource{reg: nodeReg}, 0)
}
