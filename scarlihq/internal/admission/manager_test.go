// Package admission tests — the v19.7.1 aggregate admission proofs:
// the roadmap §6 checklist (resource vector, atomic admission, per-UUID
// GPU capacity, RAM/KV/SSD accounting, exclusive-GPU default,
// reconciliation + lease expiry, verified release) and the KpK audit's
// hard rules (stale refusal, verified-artifacts-only, placement ×
// runtime join, never-schedule-from-unverified-estimates).
package admission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// ---- fixtures -------------------------------------------------------------

const (
	gpuA = "GPU-aaaa"
	gpuB = "GPU-bbbb"
)

func testManifest() *modelmeta.Manifest {
	m := &modelmeta.Manifest{
		SchemaVersion: modelmeta.SchemaVersionV2,
		ID:            "qwen3-14b-awq",
		Revision:      "rev-1",
	}
	m.Checkpoint.Format = "safetensors"
	m.Checkpoint.SHA256 = strings.Repeat("ab", 32)
	m.Architecture.Kind = modelmeta.ArchitectureDense
	m.Architecture.SharedBytes = 8 << 30
	m.Runtime.ContextLimit = 32768
	m.Runtime.SupportedBackends = []string{"sglang"}
	m.Storage.ModelPath = "/models/qwen3-14b-awq"
	return m
}

// freshVerification builds a record BOUND to testManifest()'s identity
// (v19.8.1, KPK-19.8.0-P1-02: the fingerprint is what admission matches).
func freshVerification(age time.Duration) *modelmeta.ArtifactVerification {
	return &modelmeta.ArtifactVerification{
		ModelID:             "qwen3-14b-awq",
		ManifestFingerprint: modelmeta.ManifestFingerprint(testManifest()),
		AllVerified:         true,
		VerifiedAt:          time.Now().UTC().Add(-age),
		Checkpoint:          &modelmeta.ArtifactDigest{Path: "/models/qwen3-14b-awq", Match: true},
	}
}

type fixedSnapshots struct {
	snap *inventory.InventorySnapshotV2
	err  error
}

func (s fixedSnapshots) Current() (*inventory.InventorySnapshotV2, error) {
	return s.snap, s.err
}

type fixedModels struct {
	manifests map[string]*modelmeta.Manifest
	verifs    map[string]*modelmeta.ArtifactVerification
}

func (s fixedModels) Manifest(id string) (*modelmeta.Manifest, bool) {
	m, ok := s.manifests[id]
	return m, ok
}

func (s fixedModels) Verification(id string) (*modelmeta.ArtifactVerification, bool) {
	v, ok := s.verifs[id]
	return v, ok
}

// admissionFixture is a full working gate: 2 GPUs (A empty, B half used),
// RAM, a benchmarked NVMe device, sglang running+healthy, a verified model.
type admissionFixture struct {
	snap   *inventory.InventorySnapshotV2
	models fixedModels
}

func newAdmissionFixture() *admissionFixture {
	observed := time.Now().UTC()
	snap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "snap0000000000000000000000000001",
		ObservedAt:    observed,
		Host: storage.HostResources{
			CPUThreads:        16,
			RAMTotalBytes:     32 << 30,
			RAMAvailableBytes: 16 << 30,
		},
		GPUs: []inventory.GPU{
			{ID: "gpu.nvidia.0", UUID: gpuA, VRAMTotalMB: 16384, VRAMFreeMB: 16384, Healthy: true},
			{ID: "gpu.nvidia.1", UUID: gpuB, VRAMTotalMB: 16384, VRAMFreeMB: 8000, Healthy: true},
		},
		Storage: []storage.Device{
			{
				ID:             "storage.nvme.0",
				Kind:           storage.DeviceNVMe,
				CapacityBytes:  2 << 40,
				AvailableBytes: 1 << 40,
				Filesystems: []storage.Filesystem{{
					Source: "/dev/nvme0n1p2", MountPoints: []string{"/"},
					FSType: "ext4", CapacityBytes: 2 << 40, AvailableBytes: 1 << 40, Healthy: true,
				}},
				MountPoints: []string{"/"},
				Healthy:     true,
				ReadProfile: &storage.ReadProfile{SequentialReadMBps: 3200, BlockSizeBytes: 1 << 20, MeasuredAt: observed.Add(-time.Hour), ProfileID: "bench-1"},
			},
			{
				ID: "storage.sata.1", Kind: storage.DeviceSATA, CapacityBytes: 1 << 40,
				AvailableBytes: 500 << 30, MountPoints: []string{"/data"}, Healthy: true,
				// NO ReadProfile — the unbenchmarked device.
			},
		},
		Runtimes: []inventory.Runtime{
			{ID: "sglang", Running: true, Healthy: true},
			{ID: "vllm", Running: false, Healthy: false},
		},
		Models:  []inventory.ModelExecutionView{},
		Health:  []inventory.Health{},
		Sources: []storage.SourceStatus{},
	}
	return &admissionFixture{
		snap: snap,
		models: fixedModels{
			manifests: map[string]*modelmeta.Manifest{"qwen3-14b-awq": testManifest()},
			verifs:    map[string]*modelmeta.ArtifactVerification{"qwen3-14b-awq": freshVerification(time.Hour)},
		},
	}
}

func (f *admissionFixture) manager(t *testing.T) *Manager {
	t.Helper()
	l, err := NewLedger(filepath.Join(t.TempDir(), "admission-ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(DefaultPolicy(), fixedSnapshots{snap: f.snap}, f.models, l)
	m.SetNow(func() time.Time { return f.snap.ObservedAt.Add(time.Second) })
	return m
}

func baseRequest() AdmissionRequest {
	return AdmissionRequest{
		RequestID: "req-1",
		Principal: "operator",
		ModelID:   "qwen3-14b-awq",
		BackendID: "sglang",
		Requirements: ResourceVector{
			GPU:        []GPURequest{{GPUUUID: gpuA, VRAMBytes: 8 << 30, Exclusive: true}},
			RAMBytes:   4 << 30,
			CPUThreads: 4,
			Storage:    []StorageRequest{{DeviceID: "storage.nvme.0", MinimumFreeBytes: 100 << 30}},
			Ports:      []int{30000},
		},
	}
}

// ---- the admission proofs ---------------------------------------------------

func TestAdmitAllDimensions(t *testing.T) {
	f := newAdmissionFixture()
	m := f.manager(t)
	d := m.Admit(baseRequest())
	if !d.Allowed {
		t.Fatalf("admission refused: %s — %s", d.ReasonCode, d.Explanation)
	}
	if d.AdmissionID == "" || d.Assignment == nil {
		t.Fatalf("decision missing the record identity/assignment")
	}
	if len(d.Assignment.GPUs) != 1 || d.Assignment.GPUs[0].GPUUUID != gpuA || d.Assignment.GPUs[0].VRAMBytes != 8<<30 {
		t.Fatalf("gpu assignment wrong: %+v", d.Assignment.GPUs)
	}
	if d.Assignment.RAMBytes != 4<<30 || d.Assignment.CPUThreads != 4 {
		t.Fatalf("ram/cpu assignment wrong: %+v", d.Assignment)
	}
	if len(d.Assignment.Storage) != 1 || d.Assignment.Storage[0].ReservedBytes != 100<<30 {
		t.Fatalf("storage assignment wrong: %+v", d.Assignment.Storage)
	}
	if d.LeaseExpiresAt.IsZero() {
		t.Fatalf("lease expiry missing")
	}
	if d.SnapshotID != f.snap.SnapshotID || d.PolicyVersion != "admission/v19.8.1" {
		t.Fatalf("decision provenance missing: snap=%q policy=%q", d.SnapshotID, d.PolicyVersion)
	}
}

func TestAdmitStaleSnapshotHardRefused(t *testing.T) {
	// KPK-19.7.0-P2-01: admission odmietne freshness.stale=true — HARD.
	f := newAdmissionFixture()
	f.snap.ObservedAt = time.Now().UTC().Add(-10 * time.Minute)
	m := f.manager(t)
	d := m.Admit(baseRequest())
	if d.Allowed || d.ReasonCode != ReasonSnapshotStale {
		t.Fatalf("decision=%+v, want stale hard-refusal", d)
	}
	if !strings.Contains(d.Explanation, "HARD-REFUSED") {
		t.Errorf("explanation must name the hard rule: %q", d.Explanation)
	}
}

func TestAdmitModelUnknown(t *testing.T) {
	f := newAdmissionFixture()
	req := baseRequest()
	req.ModelID = "ghost-model"
	d := f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonModelUnknown {
		t.Fatalf("decision=%+v, want model-unknown", d)
	}
}

func TestAdmitModelUnverified(t *testing.T) {
	// KPK-19.7.0-P2-04: no verification record → refused (a declared
	// digest is a declaration, not a verified fingerprint).
	f := newAdmissionFixture()
	f.models.verifs = map[string]*modelmeta.ArtifactVerification{}
	d := f.manager(t).Admit(baseRequest())
	if d.Allowed || d.ReasonCode != ReasonModelUnverified {
		t.Fatalf("decision=%+v, want model-unverified (no record)", d)
	}

	// A non-matching record is equally refused.
	f2 := newAdmissionFixture()
	f2.models.verifs["qwen3-14b-awq"] = &modelmeta.ArtifactVerification{
		ModelID: "qwen3-14b-awq", AllVerified: false,
		Checkpoint: &modelmeta.ArtifactDigest{Match: false, ComputedSHA256: strings.Repeat("cd", 32)},
	}
	d2 := f2.manager(t).Admit(baseRequest())
	if d2.Allowed || d2.ReasonCode != ReasonModelUnverified {
		t.Fatalf("decision=%+v, want model-unverified (mismatch)", d2)
	}

	// A STALE record is absent verification.
	f3 := newAdmissionFixture()
	f3.models.verifs["qwen3-14b-awq"] = freshVerification(30 * 24 * time.Hour)
	d3 := f3.manager(t).Admit(baseRequest())
	if d3.Allowed || d3.ReasonCode != ReasonModelUnverified {
		t.Fatalf("decision=%+v, want model-unverified (stale record)", d3)
	}
}

func TestAdmitModelIneligible(t *testing.T) {
	// Metadata incomplete (MoE without expert geometry) → ineligible.
	f := newAdmissionFixture()
	m := testManifest()
	m.Architecture.Kind = modelmeta.ArchitectureMoE
	m.Architecture.ExpertCount = 8
	m.Architecture.ExpertsPerToken = 2
	m.Architecture.ExpertBytes = 0 // unknown expert geometry
	f.models.manifests["qwen3-14b-awq"] = m
	d := f.manager(t).Admit(baseRequest())
	if d.Allowed || d.ReasonCode != ReasonModelIneligible {
		t.Fatalf("decision=%+v, want model-ineligible", d)
	}
}

func TestAdmitBackendGates(t *testing.T) {
	// Not declared by the manifest.
	f := newAdmissionFixture()
	req := baseRequest()
	req.BackendID = "ollama"
	d := f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonBackendNotDeclared {
		t.Fatalf("decision=%+v, want backend-not-declared", d)
	}

	// Declared but NOT running (the placement × runtime join:
	// eligible metadata does not start a runtime).
	req.BackendID = "sglang"
	f.snap.Runtimes[0].Running = false
	d = f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonBackendUnavailable {
		t.Fatalf("decision=%+v, want backend-unavailable (stopped)", d)
	}

	// Running but unhealthy — equally refused.
	f.snap.Runtimes[0].Running = true
	f.snap.Runtimes[0].Healthy = false
	d = f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonBackendUnavailable {
		t.Fatalf("decision=%+v, want backend-unavailable (unhealthy)", d)
	}
}

func TestAdmitGPUExclusiveOnly(t *testing.T) {
	// Roadmap §6: "Exclusive GPU mode remains default" — shared asks are
	// refused BY NAME.
	f := newAdmissionFixture()
	req := baseRequest()
	req.Requirements.GPU[0].Exclusive = false
	d := f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonRequestInvalid {
		t.Fatalf("decision=%+v, want request-invalid for a shared GPU ask", d)
	}
	if !strings.Contains(d.Explanation, "EXCLUSIVE") {
		t.Errorf("explanation must name the exclusive rule: %q", d.Explanation)
	}
}

func TestAdmitGPUExclusiveConflict(t *testing.T) {
	f := newAdmissionFixture()
	m := f.manager(t)
	if d := m.Admit(baseRequest()); !d.Allowed {
		t.Fatalf("first admission failed: %s", d.Explanation)
	}
	// A second admission on the SAME uuid → exclusive conflict (even with
	// plenty of VRAM left).
	req2 := baseRequest()
	req2.RequestID = "req-2"
	req2.Requirements.GPU[0].VRAMBytes = 1 << 30
	d2 := m.Admit(req2)
	if d2.Allowed || d2.ReasonCode != ReasonGPUExclusiveConflict {
		t.Fatalf("decision=%+v, want gpu-exclusive-conflict", d2)
	}
}

func TestAdmitGPUVRAMExhausted(t *testing.T) {
	f := newAdmissionFixture()
	req := baseRequest()
	// gpuB has 8000 MiB free; demand 12 GiB + safety → refused.
	req.Requirements.GPU[0].GPUUUID = gpuB
	req.Requirements.GPU[0].VRAMBytes = 12 << 30
	d := f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonGPUVRAMExhausted {
		t.Fatalf("decision=%+v, want gpu-vram-exhausted", d)
	}
}

func TestAdmitKVCountsAgainstGPU(t *testing.T) {
	f := newAdmissionFixture()
	req := baseRequest()
	// VRAM 15 GiB + KV 1 GiB = 16 GiB demand on a 16 GiB card with 5%
	// safety → refused (the KV cache counts, conservatively).
	req.Requirements.GPU[0].VRAMBytes = 15 << 30
	req.Requirements.KVCacheBytes = 1 << 30
	d := f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonGPUVRAMExhausted {
		t.Fatalf("decision=%+v, want vram exhaustion incl. KV", d)
	}
}

func TestAdmitAnyGPUDeterministicPick(t *testing.T) {
	f := newAdmissionFixture()
	req := baseRequest()
	req.Requirements.GPU[0].GPUUUID = "" // any GPU
	req.Requirements.GPU[0].VRAMBytes = 7 << 30
	m := f.manager(t)
	d1 := m.Admit(req)
	if !d1.Allowed {
		t.Fatalf("admission refused: %s", d1.Explanation)
	}
	// Deterministic: the LOWEST uuid with capacity (gpuA) is picked.
	if d1.Assignment.GPUs[0].GPUUUID != gpuA {
		t.Fatalf("picked %q, want the lowest-uuid candidate %q", d1.Assignment.GPUs[0].GPUUUID, gpuA)
	}
	// gpuA now exclusively held → the next any-GPU request picks gpuB.
	req2 := req
	req2.RequestID = "req-2"
	req2.Requirements.Ports = []int{30001}
	d2 := m.Admit(req2)
	if !d2.Allowed {
		t.Fatalf("second admission refused: %s", d2.Explanation)
	}
	if d2.Assignment.GPUs[0].GPUUUID != gpuB {
		t.Fatalf("picked %q, want %q (A exclusively held)", d2.Assignment.GPUs[0].GPUUUID, gpuB)
	}
}

func TestAdmitRAMExhausted(t *testing.T) {
	f := newAdmissionFixture()
	req := baseRequest()
	req.Requirements.RAMBytes = 20 << 30 // observed 16 GiB − 10% safety
	d := f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonRAMExhausted {
		t.Fatalf("decision=%+v, want ram-exhausted", d)
	}
}

func TestAdmitPinnedRAMRefusedByDefault(t *testing.T) {
	// The honest default: no pinned-RAM observation → no pinned admission.
	f := newAdmissionFixture()
	req := baseRequest()
	req.Requirements.RAMBytes = 8 << 30
	req.Requirements.PinnedRAMBytes = 1 << 30
	d := f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonPinnedRAMUnavailable {
		t.Fatalf("decision=%+v, want pinned-ram-unavailable", d)
	}
}

func TestAdmitStorageNoReadProfileEnforced(t *testing.T) {
	// The release's must-not-do ENFORCED: a measured floor without a
	// measurement is refused — never scheduled from unverified estimates.
	f := newAdmissionFixture()
	req := baseRequest()
	req.Requirements.Storage[0].DeviceID = "storage.sata.1" // unbenchmarked
	req.Requirements.Storage[0].MinimumReadMBps = 100
	d := f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonStorageNoReadProfile {
		t.Fatalf("decision=%+v, want storage-no-read-profile", d)
	}
}

func TestAdmitStorageReadProfileInsufficient(t *testing.T) {
	f := newAdmissionFixture()
	req := baseRequest()
	req.Requirements.Storage[0].MinimumReadMBps = 5000 // measured 3200
	d := f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonStorageReadProfileInsufficient {
		t.Fatalf("decision=%+v, want storage-read-profile-insufficient", d)
	}
}

func TestAdmitStorageExhausted(t *testing.T) {
	f := newAdmissionFixture()
	req := baseRequest()
	req.Requirements.Storage[0].MinimumFreeBytes = 2 << 40 // available 1 TiB − 10% safety
	d := f.manager(t).Admit(req)
	if d.Allowed || d.ReasonCode != ReasonStorageExhausted {
		t.Fatalf("decision=%+v, want storage-exhausted", d)
	}
}

func TestAdmitPortConflict(t *testing.T) {
	f := newAdmissionFixture()
	m := f.manager(t)
	if d := m.Admit(baseRequest()); !d.Allowed {
		t.Fatalf("first admission failed: %s", d.Explanation)
	}
	// gpuA held → ask gpuB (fits: small VRAM) with the SAME port.
	req2 := baseRequest()
	req2.RequestID = "req-2"
	req2.Requirements.GPU[0].GPUUUID = gpuB
	req2.Requirements.GPU[0].VRAMBytes = 2 << 30
	req2.Requirements.Ports = []int{30000}
	d2 := m.Admit(req2)
	if d2.Allowed || d2.ReasonCode != ReasonPortConflict {
		t.Fatalf("decision=%+v, want port-conflict", d2)
	}
}

func TestAdmitFailureReservesNothing(t *testing.T) {
	// Atomicity: a request whose LAST dimension fails must hold NOTHING
	// (the earlier dimensions are not leaked into the ledger).
	f := newAdmissionFixture()
	m := f.manager(t)
	req := baseRequest()
	req.Requirements.Storage = append(req.Requirements.Storage,
		StorageRequest{DeviceID: "storage.ghost.9", MinimumFreeBytes: 1}) // unknown device
	d := m.Admit(req)
	if d.Allowed || d.ReasonCode != ReasonStorageUnknownDevice {
		t.Fatalf("decision=%+v, want storage-unknown-device", d)
	}
	if held := m.Held(); len(held) != 0 {
		t.Fatalf("a failed admission left %d holds — nothing must be reserved", len(held))
	}
	// The full request still succeeds afterwards (nothing was consumed).
	if d := m.Admit(baseRequest()); !d.Allowed {
		t.Fatalf("post-failure admission refused: %s", d.Explanation)
	}
}

func TestAdmitConcurrentCannotOverspend(t *testing.T) {
	// Roadmap §6: "Aggregate admission is atomic" — concurrent admits on
	// ONE GPU/RAM pool cannot collectively overspend.
	f := newAdmissionFixture()
	m := f.manager(t)
	const workers = 8
	allowed := make([]bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := baseRequest()
			req.RequestID = "req-concurrent"
			req.Requirements.GPU[0].GPUUUID = "" // any GPU — two GPUs, 8 workers
			req.Requirements.GPU[0].VRAMBytes = 4 << 30
			req.Requirements.Ports = []int{31000 + i} // distinct ports
			d := m.Admit(req)
			allowed[i] = d.Allowed
		}(i)
	}
	wg.Wait()
	granted := 0
	for _, a := range allowed {
		if a {
			granted++
		}
	}
	if granted != 2 { // exactly one exclusive hold per GPU
		t.Fatalf("granted=%d, want exactly 2 (one exclusive hold per GPU)", granted)
	}
}

func TestAdmitIdempotentReplay(t *testing.T) {
	f := newAdmissionFixture()
	m := f.manager(t)
	req := baseRequest()
	req.IdempotencyKey = "idem-1"
	d1 := m.Admit(req)
	if !d1.Allowed {
		t.Fatalf("first admission failed: %s", d1.Explanation)
	}
	d2 := m.Admit(req)
	if !d2.Allowed || d2.AdmissionID != d1.AdmissionID {
		t.Fatalf("idempotent replay created a second hold: %+v vs %+v", d1, d2)
	}
	if held := m.Held(); len(held) != 1 {
		t.Fatalf("holds=%d, want 1 (idempotency)", len(held))
	}
}

func TestReleaseVerifiedCleanup(t *testing.T) {
	f := newAdmissionFixture()
	m := f.manager(t)
	d := m.Admit(baseRequest())
	if !d.Allowed {
		t.Fatalf("admission refused: %s", d.Explanation)
	}
	rec, err := m.Release(d.AdmissionID)
	if err != nil {
		t.Fatalf("release failed: %v", err)
	}
	if rec.Status != StatusReleased || rec.CloseReason == "" {
		t.Fatalf("released record incomplete: %+v", rec)
	}
	// Double release → NAMED error (no silent no-op).
	if _, err := m.Release(d.AdmissionID); err == nil {
		t.Fatal("double release must be a named error (verified cleanup)")
	}
	// Capacity is free again — the same request re-admits.
	req2 := baseRequest()
	req2.RequestID = "req-2"
	if d2 := m.Admit(req2); !d2.Allowed {
		t.Fatalf("post-release admission refused (capacity not freed): %s", d2.Explanation)
	}
}

func TestLeaseExpiryReconciliation(t *testing.T) {
	// Roadmap §6: "Reconciliation and lease expiry tested."
	f := newAdmissionFixture()
	m := f.manager(t)
	d := m.Admit(baseRequest())
	if !d.Allowed {
		t.Fatalf("admission refused: %s", d.Explanation)
	}

	// Advance the clock past the lease TTL and sweep.
	leaseExpiry := d.LeaseExpiresAt
	m.SetNow(func() time.Time { return leaseExpiry.Add(time.Minute) })
	closed := m.Reconcile()
	if closed != 1 {
		t.Fatalf("reconcile closed=%d, want 1", closed)
	}
	if len(m.Held()) != 0 {
		t.Fatalf("the expired admission is still HELD: %+v", m.Held())
	}
	// Capacity returned.
	req2 := baseRequest()
	req2.RequestID = "req-2"
	if d2 := m.Admit(req2); !d2.Allowed {
		t.Fatalf("post-expiry admission refused (capacity not freed): %s", d2.Explanation)
	}
}

func TestAdmissionDeterministic(t *testing.T) {
	// Roadmap §6.4: identical inputs + identical snapshots → identical
	// decisions. Two independent managers over identical fixtures.
	f1 := newAdmissionFixture()
	f2 := newAdmissionFixture()
	d1 := f1.manager(t).Admit(baseRequest())
	d2 := f2.manager(t).Admit(baseRequest())
	if d1.Allowed != d2.Allowed || d1.ReasonCode != d2.ReasonCode ||
		d1.Assignment.GPUs[0].GPUUUID != d2.Assignment.GPUs[0].GPUUUID ||
		d1.Assignment.RAMBytes != d2.Assignment.RAMBytes {
		t.Fatalf("decisions diverged:\n%+v\n%+v", d1, d2)
	}
}

func TestLedgerCrashRecovery(t *testing.T) {
	// A hold from a dead process (foreign boot identity) is released at
	// ledger reconstruction — on the record.
	path := filepath.Join(t.TempDir(), "admission-ledger.json")
	f := newAdmissionFixture()
	l1, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(DefaultPolicy(), fixedSnapshots{snap: f.snap}, f.models, l1)
	m.SetNow(func() time.Time { return f.snap.ObservedAt.Add(time.Second) })
	d := m.Admit(baseRequest())
	if !d.Allowed {
		t.Fatalf("admission refused: %s", d.Explanation)
	}

	// Forge the record's boot id — the durable view of "another process".
	rewriteBootID(t, path, "boot-deadbeef")

	l2, err := NewLedger(path)
	if err != nil {
		t.Fatalf("reconstruction failed: %v", err)
	}
	m2 := NewManager(DefaultPolicy(), fixedSnapshots{snap: f.snap}, f.models, l2)
	m2.SetNow(func() time.Time { return f.snap.ObservedAt.Add(2 * time.Second) })
	for _, rec := range m2.Held() {
		if rec.AdmissionID == d.AdmissionID {
			t.Fatalf("the dead process's hold survived reconstruction: %+v", rec)
		}
	}
	// The capacity is free: the same request admits again.
	req2 := baseRequest()
	req2.RequestID = "req-2"
	if d2 := m2.Admit(req2); !d2.Allowed {
		t.Fatalf("post-crash admission refused (capacity not recovered): %s", d2.Explanation)
	}
}

func TestLedgerPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admission-ledger.json")
	f := newAdmissionFixture()
	l1, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(DefaultPolicy(), fixedSnapshots{snap: f.snap}, f.models, l1)
	m.SetNow(func() time.Time { return f.snap.ObservedAt.Add(time.Second) })
	d := m.Admit(baseRequest())
	if !d.Allowed {
		t.Fatalf("admission refused: %s", d.Explanation)
	}

	// SAME boot identity (same process): reconstruction keeps the hold.
	rewriteBootID(t, path, BootIdentity())
	l2, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range l2.Records() {
		if rec.AdmissionID == d.AdmissionID && rec.Status == StatusHeld {
			found = true
		}
	}
	if !found {
		t.Fatalf("the same-boot hold was lost on reconstruction")
	}
}

// ---- vector validation ------------------------------------------------------

func TestResourceVectorValidate(t *testing.T) {
	cases := []struct {
		name string
		vec  ResourceVector
		bad  bool
		note string
	}{
		{"empty", ResourceVector{}, false, ""},
		{"gpu-zero-vram", ResourceVector{GPU: []GPURequest{{VRAMBytes: 0}}}, true, "vram"},
		{"gpu-share-range", ResourceVector{GPU: []GPURequest{{VRAMBytes: 1, MinimumComputeShare: 1.5}}}, true, "share"},
		{"gpu-dup-uuid", ResourceVector{GPU: []GPURequest{{GPUUUID: "x", VRAMBytes: 1}, {GPUUUID: "x", VRAMBytes: 1}}}, true, "dup"},
		{"pinned-over-ram", ResourceVector{RAMBytes: 100, PinnedRAMBytes: 200}, true, "pinned"},
		{"cpu-negative", ResourceVector{CPUThreads: -1}, true, "cpu"},
		{"storage-all-zero", ResourceVector{Storage: []StorageRequest{{}}}, true, "zero"},
		{"storage-dup-dev", ResourceVector{Storage: []StorageRequest{{DeviceID: "d", MinimumFreeBytes: 1}, {DeviceID: "d", MinimumFreeBytes: 1}}}, true, "dup"},
		{"port-range", ResourceVector{Ports: []int{70000}}, true, "port"},
		{"port-dup", ResourceVector{Ports: []int{80, 80}}, true, "dup"},
		{"valid-full", ResourceVector{
			GPU:      []GPURequest{{GPUUUID: "u", VRAMBytes: 1, Exclusive: true}},
			RAMBytes: 1, CPUThreads: 1, KVCacheBytes: 1,
			Storage: []StorageRequest{{DeviceID: "d", MinimumFreeBytes: 1}},
			Ports:   []int{8080},
		}, false, ""},
	}
	for _, c := range cases {
		reasons := c.vec.Validate()
		if c.bad && len(reasons) == 0 {
			t.Errorf("%s: expected a refusal, got none", c.name)
		}
		if !c.bad && len(reasons) > 0 {
			t.Errorf("%s: unexpected refusals %v", c.name, reasons)
		}
		if c.bad && len(reasons) > 0 && !strings.Contains(strings.Join(reasons, ";"), c.note) {
			t.Errorf("%s: reason %v does not mention %q", c.name, reasons, c.note)
		}
	}
}

// ---- the v19.8.1 remediation proofs -------------------------------------------

// TestAdmitRandomReadFloorsEnforced — KPK-19.8.0-P1-01: the admission
// gate enforces the FULL measured contract, not only the sequential
// floor. Sequential passes + random IOPS below the floor → refused BY
// NAME; the random phase missing entirely while a random floor is
// demanded → refused; p95 above the ceiling → refused; all metrics pass
// → admission proceeds.
func TestAdmitRandomReadFloorsEnforced(t *testing.T) {
	f := newAdmissionFixture()
	// Give the fixture's benchmarked device a recorded random phase.
	rp := f.snap.Storage[0].ReadProfile
	rp.RandomReadIOPS = 40000
	rp.RandomReadP50Ms = 0.08
	rp.RandomReadP95Ms = 0.25
	rp.RandomReadP99Ms = 0.9
	rp.RandomBlockBytes = 4096

	// (1) sequential passes, random IOPS below the floor.
	req := baseRequest()
	req.Requirements.Storage[0].MinimumRandomReadIOPS = 50000
	if d := f.manager(t).Admit(req); d.Allowed || d.ReasonCode != ReasonStorageReadProfileInsufficient {
		t.Fatalf("decision=%+v, want storage-read-profile-insufficient (random IOPS below the floor)", d)
	}
	// (2) IOPS passes but p95 exceeds the ceiling.
	req2 := baseRequest()
	req2.Requirements.Storage[0].MinimumRandomReadIOPS = 30000
	req2.Requirements.Storage[0].MaximumP95LatencyMs = 0.1
	if d := f.manager(t).Admit(req2); d.Allowed || d.ReasonCode != ReasonStorageReadProfileInsufficient {
		t.Fatalf("decision=%+v, want storage-read-profile-insufficient (p95 above the ceiling)", d)
	}
	// (3) the random phase is MISSING while a random floor is demanded.
	req3 := baseRequest()
	req3.Requirements.Storage[0].MinimumRandomReadIOPS = 30000
	f2 := newAdmissionFixture() // the fixture's profile has NO random phase
	if d := f2.manager(t).Admit(req3); d.Allowed || d.ReasonCode != ReasonStorageReadProfileInsufficient {
		t.Fatalf("decision=%+v, want storage-read-profile-insufficient (unmeasured random floor)", d)
	}
	// (4) all metrics pass → admission proceeds.
	req4 := baseRequest()
	req4.Requirements.Storage[0].MinimumRandomReadIOPS = 30000
	req4.Requirements.Storage[0].MaximumP95LatencyMs = 0.5
	if d := f.manager(t).Admit(req4); !d.Allowed {
		t.Fatalf("all-metrics-pass admission refused: %s — %s", d.ReasonCode, d.Explanation)
	}
}

// TestAdmitBenchmarkStaleRefused — the SK audit's P2-04: a benchmark
// older than the policy's MaxBenchmarkAge is a named refusal (a
// benchmark describes the device it ran on, at the time it ran).
func TestAdmitBenchmarkStaleRefused(t *testing.T) {
	f := newAdmissionFixture()
	rp := f.snap.Storage[0].ReadProfile
	rp.RandomReadIOPS = 40000
	rp.RandomReadP95Ms = 0.25
	rp.MeasuredAt = f.snap.ObservedAt.Add(-40 * 24 * time.Hour) // bound is 30 days
	req := baseRequest()
	req.Requirements.Storage[0].MinimumReadMBps = 100 // a measured floor demanded
	if d := f.manager(t).Admit(req); d.Allowed || d.ReasonCode != ReasonStorageReadProfileStale {
		t.Fatalf("decision=%+v, want storage-read-profile-stale", d)
	}
}

// TestAdmitVerificationIdentityMismatch — KPK-19.8.0-P1-02: the
// verification record is bound to the manifest identity; a manifest that
// changed after verification is UNVERIFIED until re-verified, and a
// pre-v19.8.1 record (no fingerprint) is equally refused.
func TestAdmitVerificationIdentityMismatch(t *testing.T) {
	// (1) the manifest's revision changed after verification.
	f := newAdmissionFixture()
	m := testManifest()
	m.Revision = "rev-2" // same id, different identity
	f.models.manifests["qwen3-14b-awq"] = m
	if d := f.manager(t).Admit(baseRequest()); d.Allowed || d.ReasonCode != ReasonModelVerificationIdentityMismatch {
		t.Fatalf("decision=%+v, want model-verification-identity-mismatch (revision changed)", d)
	}

	// (2) the checkpoint digest changed.
	f2 := newAdmissionFixture()
	m2 := testManifest()
	m2.Checkpoint.SHA256 = strings.Repeat("cd", 32)
	f2.models.manifests["qwen3-14b-awq"] = m2
	if d := f2.manager(t).Admit(baseRequest()); d.Allowed || d.ReasonCode != ReasonModelVerificationIdentityMismatch {
		t.Fatalf("decision=%+v, want model-verification-identity-mismatch (checkpoint changed)", d)
	}

	// (3) a pre-v19.8.1 record (no fingerprint) is refused as unverified.
	f3 := newAdmissionFixture()
	f3.models.verifs["qwen3-14b-awq"].ManifestFingerprint = ""
	if d := f3.manager(t).Admit(baseRequest()); d.Allowed || d.ReasonCode != ReasonModelUnverified {
		t.Fatalf("decision=%+v, want model-unverified (no fingerprint)", d)
	}
}

// TestAdmitKVCountedExactlyOnce — KPK-19.8.0-P2-03: VRAMBytes + KV
// → the assigned GPU's hold is EXACTLY the sum, never 2× the KV bytes.
func TestAdmitKVCountedExactlyOnce(t *testing.T) {
	f := newAdmissionFixture()
	req := baseRequest()
	req.Requirements.GPU[0].VRAMBytes = 8 << 30
	req.Requirements.KVCacheBytes = 1 << 30
	d := f.manager(t).Admit(req)
	if !d.Allowed {
		t.Fatalf("admission refused: %s", d.Explanation)
	}
	if got := d.Assignment.GPUs[0].VRAMBytes; got != 9<<30 {
		t.Fatalf("GPU hold = %d, want EXACTLY %d (VRAM 8 GiB + KV 1 GiB once — the double-count defect reserved %d)", got, 9<<30, 10<<30)
	}
	if d.Assignment.GPUs[0].KVCacheBytes != 1<<30 || d.Assignment.KVCacheBytes != 1<<30 {
		t.Fatalf("KV accounting drift: %+v", d.Assignment)
	}
}

// TestAdmitIdempotencyConflict — KPK-19.8.0-P2-04: the same key with a
// DIFFERENT request (model, vector, principal) is the NAMED conflict,
// never the prior allocation wearing the new request's name; the exact
// replay still returns the ORIGINAL decision.
func TestAdmitIdempotencyConflict(t *testing.T) {
	f := newAdmissionFixture()
	m := f.manager(t)
	req := baseRequest()
	req.IdempotencyKey = "idem-42"
	if d := m.Admit(req); !d.Allowed {
		t.Fatalf("first admission failed: %s", d.Explanation)
	}

	// Same key, different resource vector → conflict.
	req2 := baseRequest()
	req2.RequestID = "req-2"
	req2.IdempotencyKey = "idem-42"
	req2.Requirements.GPU[0].VRAMBytes = 6 << 30
	if d := m.Admit(req2); d.Allowed || d.ReasonCode != ReasonIdempotencyConflict {
		t.Fatalf("decision=%+v, want idempotency-conflict (different vector)", d)
	}
	// Same key, different model → conflict.
	req3 := baseRequest()
	req3.IdempotencyKey = "idem-42"
	req3.ModelID = "ghost-model"
	req3.RequestID = "req-3"
	if d := m.Admit(req3); d.Allowed || d.ReasonCode != ReasonIdempotencyConflict {
		t.Fatalf("decision=%+v, want idempotency-conflict (different model)", d)
	}
	// Same key, different principal → conflict.
	req4 := baseRequest()
	req4.IdempotencyKey = "idem-42"
	req4.Principal = "intruder"
	req4.RequestID = "req-4"
	if d := m.Admit(req4); d.Allowed || d.ReasonCode != ReasonIdempotencyConflict {
		t.Fatalf("decision=%+v, want idempotency-conflict (different principal)", d)
	}
	// The EXACT replay still returns the original decision.
	if d := m.Admit(req); !d.Allowed || d.AdmissionID == "" {
		t.Fatalf("exact replay refused: %+v", d)
	}
	// And only ONE hold exists.
	if held := m.Held(); len(held) != 1 {
		t.Fatalf("holds=%d, want 1 (the conflicts reserved nothing)", len(held))
	}
}

// TestAdmitStaleSnapshotRefusedOnSSDPath — the SK audit's P2-05
// regression: a stale snapshot is refused by FRESHNESS even when the
// request carries the full random-read SSD floor set (the freshness
// gate precedes every dimension check, including the measured floors).
func TestAdmitStaleSnapshotRefusedOnSSDPath(t *testing.T) {
	f := newAdmissionFixture()
	f.snap.ObservedAt = time.Now().UTC().Add(-10 * time.Minute)
	req := baseRequest()
	req.Requirements.Storage[0].MinimumReadMBps = 100
	req.Requirements.Storage[0].MinimumRandomReadIOPS = 30000
	req.Requirements.Storage[0].MaximumP95LatencyMs = 0.5
	if d := f.manager(t).Admit(req); d.Allowed || d.ReasonCode != ReasonSnapshotStale {
		t.Fatalf("decision=%+v, want snapshot-stale on the SSD path (freshness precedes the floors)", d)
	}
}

// TestAdmissionRenewExtendsLease — KPK-19.8.0-P2-05: Renew extends the
// HELD lease durably (past the original expiry); renewing a foreign
// principal's hold is a NAMED error.
func TestAdmissionRenewExtendsLease(t *testing.T) {
	f := newAdmissionFixture()
	m := f.manager(t)
	d := m.Admit(baseRequest())
	if !d.Allowed {
		t.Fatalf("admission refused: %s", d.Explanation)
	}
	// Advance the clock past the ORIGINAL expiry and renew.
	m.SetNow(func() time.Time { return d.LeaseExpiresAt.Add(-time.Minute) })
	rec, err := m.Renew(d.AdmissionID, "operator")
	if err != nil {
		t.Fatalf("renew failed: %v", err)
	}
	if !rec.LeaseExpiresAt.After(d.LeaseExpiresAt) {
		t.Fatalf("renewal did not extend the lease: %v ≤ %v", rec.LeaseExpiresAt, d.LeaseExpiresAt)
	}
	// Advance past the ORIGINAL expiry; the renewed hold survives.
	m.SetNow(func() time.Time { return d.LeaseExpiresAt.Add(time.Minute) })
	if closed := m.Reconcile(); closed != 0 {
		t.Fatalf("reconcile closed %d holds, want 0 (the renewal stands)", closed)
	}
	// A foreign principal cannot renew.
	if _, err := m.Renew(d.AdmissionID, "intruder"); err == nil {
		t.Fatal("a foreign principal renewing the hold must be a named error")
	}
}

// TestAdmissionConfirmStartedLifecycle — KPK-19.8.0-P2-05: HELD →
// ACTIVE is principal-bound and idempotent; an ACTIVE record survives
// lease expiry (capacity still counted), and release closes it.
func TestAdmissionConfirmStartedLifecycle(t *testing.T) {
	f := newAdmissionFixture()
	m := f.manager(t)
	d := m.Admit(baseRequest())
	if !d.Allowed {
		t.Fatalf("admission refused: %s", d.Explanation)
	}

	// A foreign principal cannot confirm.
	if _, err := m.ConfirmStarted(d.AdmissionID, "intruder"); err == nil {
		t.Fatal("a foreign principal confirming the start must be a named error")
	}
	// The owner confirms; the record becomes ACTIVE.
	rec, err := m.ConfirmStarted(d.AdmissionID, "operator")
	if err != nil || rec.Status != StatusActive {
		t.Fatalf("confirm-started: %v status=%s", err, rec.Status)
	}
	if rec.StartedAt == nil {
		t.Fatalf("the ACTIVE record must carry started_at")
	}
	// Confirmation is IDEMPOTENT (retry-safe).
	rec2, err := m.ConfirmStarted(d.AdmissionID, "operator")
	if err != nil || rec2.Status != StatusActive {
		t.Fatalf("idempotent confirm-started: %v status=%s", err, rec2.Status)
	}

	// Lease expiry no longer frees the hold: advance past the expiry and
	// sweep — the ACTIVE record stands with its capacity counted.
	m.SetNow(func() time.Time { return d.LeaseExpiresAt.Add(time.Hour) })
	if closed := m.Reconcile(); closed != 0 {
		t.Fatalf("reconcile closed %d ACTIVE holds, want 0 (a timer alone never frees confirmed-start capacity)", closed)
	}
	if held := m.Held(); len(held) != 1 || held[0].Status != StatusActive {
		t.Fatalf("the ACTIVE hold vanished or lost its status: %+v", held)
	}

	// The capacity is still counted: the SAME GPU is exclusively held.
	req2 := baseRequest()
	req2.RequestID = "req-2"
	if d2 := m.Admit(req2); d2.Allowed || d2.ReasonCode != ReasonGPUExclusiveConflict {
		t.Fatalf("decision=%+v, want gpu-exclusive-conflict (ACTIVE still consumes the GPU)", d2)
	}

	// Release closes the ACTIVE hold (explicit teardown).
	if _, err := m.Release(d.AdmissionID); err != nil {
		t.Fatalf("releasing an ACTIVE hold failed: %v", err)
	}
	if d3 := m.Admit(req2); !d3.Allowed {
		t.Fatalf("post-release admission refused (capacity not freed): %s", d3.Explanation)
	}
}

// rewriteBootID rewrites every record's boot_id in the durable file
// (the test stand-in for "a hold created by another process").
func rewriteBootID(t *testing.T, path, bootID string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	var recsRaw []json.RawMessage
	if err := json.Unmarshal(generic["records"], &recsRaw); err != nil {
		t.Fatal(err)
	}
	for i := range recsRaw {
		var r map[string]interface{}
		if err := json.Unmarshal(recsRaw[i], &r); err != nil {
			t.Fatal(err)
		}
		if b, ok := r["boot_id"].(string); ok && b != "" {
			r["boot_id"] = bootID
		}
		nb, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		recsRaw[i] = nb
	}
	recsOut, err := json.Marshal(recsRaw)
	if err != nil {
		t.Fatal(err)
	}
	generic["records"] = recsOut
	out, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}
