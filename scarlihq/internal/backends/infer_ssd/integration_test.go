// Package infer_ssd_test — the §8 integration proofs: the adapter's
// OWN estimate agrees with the planner's deterministic ssd-backed-moe
// derivation (two independent implementations of the same §8.4 numbers
// must not drift), and the qualification + GPU compatibility joins hold
// against realistic fixtures. The EXTERNAL test package exists because
// the planner imports infer_ssd (the acyclic split) — an internal test
// file could not import the planner.
package infer_ssd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/backends/infer_ssd"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
	"github.com/MoZoHuJa/OS/scarlihq/internal/planner"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// ssdFixture builds the golden §8 deployment state: an enabled profile,
// a bound MoE model, an observed running+healthy infer_ssd runtime, an
// observed NVMe device with a MEASURED read profile (sequential +
// random + p95), and a §8.2-compatible GPU.
type ssdFixture struct {
	snap    *inventory.InventorySnapshotV2
	profile *infer_ssd.RuntimeProfile
	model   *modelmeta.Manifest
}

func gpuUUIDFixture() string { return "GPU-0000-0000-0000-000000000001" }

func newSSDFixture() *ssdFixture {
	profile := &infer_ssd.RuntimeProfile{
		SchemaVersion: "scarlix.runtime-profile/v1",
		ProfileID:     "infer-ssd-moe-experimental",
		BackendID:     "infer_ssd",
		Enabled:       true,
		Experimental:  true,
	}
	profile.Image.Reference = "ghcr.io/scarlix/infer-ssd:v0.1@sha256:" + strings.Repeat("aa", 32)
	profile.Compatibility.ModelManifest = "deepseek-moe-test"
	profile.Compatibility.CheckpointSHA256 = strings.Repeat("cd", 32)
	profile.Compatibility.CUDA = "12.8"
	profile.Compatibility.DriverMinimum = "570.00"
	profile.Compatibility.GPUComputeCapabilities = []string{"sm_120"}
	profile.Execution.Strategy = infer_ssd.StrategySSDBackedMoE
	profile.Execution.ContextLimit = 8192
	profile.Execution.MaxConcurrentRequests = 1
	profile.Resources.GPU.Mode = "exclusive"
	profile.Resources.GPU.VRAMReserveBytes = 1 << 30
	profile.Resources.HostRAMBytes = 4 << 30
	profile.Resources.CPUThreads = 8
	profile.Resources.Storage.DeviceID = "storage.nvme.0"
	profile.Resources.Storage.ModelPath = "/var/lib/scarlix/models/deepseek-moe-test"
	profile.Resources.Storage.ExpertPath = "/var/lib/scarlix/experts/deepseek-moe-test"
	profile.Resources.Storage.MinimumFreeBytes = 64 << 30
	profile.Resources.Storage.MinimumReadMBps = 2000
	profile.Resources.Storage.MinimumRandomReadIOPS = 50000
	profile.Resources.Storage.MaximumP95LatencyMs = 1.5
	profile.Network.NetworkProfile = "scarlix-compute-internal"
	profile.Health.Type = "http"
	profile.Health.Path = "/health"
	profile.Health.StartupTimeoutSeconds = 600

	m := &modelmeta.Manifest{
		SchemaVersion: "scarlix.model/v2",
		ID:            "deepseek-moe-test",
		Revision:      "rev-1",
	}
	m.Checkpoint.Format = "safetensors"
	m.Checkpoint.SHA256 = strings.Repeat("cd", 32)
	m.Architecture.Kind = modelmeta.ArchitectureMoE
	m.Architecture.ExpertCount = 8
	m.Architecture.ExpertsPerToken = 2
	m.Architecture.ExpertBytes = 512 << 20
	m.Architecture.SharedBytes = 2 << 30
	m.Runtime.ContextLimit = 32768
	m.Runtime.KVCache.BytesPerTokenEstimate = 8192
	m.Runtime.SupportedBackends = []string{"infer_ssd"}

	observed := time.Now().UTC()
	snap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "snapssd00000000000000000000001",
		ObservedAt:    observed,
		Host: storage.HostResources{
			CPUThreads:        32,
			RAMTotalBytes:     64 << 30,
			RAMAvailableBytes: 32 << 30,
		},
		GPUs: []inventory.GPU{
			{ID: "gpu.nvidia.0", UUID: gpuUUIDFixture(), Vendor: "nvidia", Name: "RTX 5060 Ti", VRAMTotalMB: 16384, VRAMFreeMB: 16000, UtilizationPct: 5, Driver: "570.86", CUDA: "12.8", ComputeCap: "12.0", Healthy: true},
		},
		Storage: []storage.Device{
			{
				ID:             "storage.nvme.0",
				Kind:           storage.DeviceNVMe,
				CapacityBytes:  2 << 40,
				AvailableBytes: 1 << 40,
				Healthy:        true,
				ReadProfile: &storage.ReadProfile{
					SequentialReadMBps: 3500,
					RandomReadIOPS:     120000,
					RandomReadP50Ms:    0.05,
					RandomReadP95Ms:    0.8,
					RandomReadP99Ms:    1.2,
					BlockSizeBytes:     1 << 20,
					RandomBlockBytes:   4 << 10,
					QueueDepth:         1,
					MeasuredAt:         observed,
					ProfileID:          "bench0001",
				},
			},
		},
		Runtimes: []inventory.Runtime{
			{ID: "infer_ssd", Running: true, Healthy: true, Port: 30100},
		},
	}
	return &ssdFixture{snap: snap, profile: profile, model: m}
}

// fixedModels satisfies the planner's ModelSource over the fixture.
type fixedModels struct {
	manifest *modelmeta.Manifest
	verifs   map[string]*modelmeta.ArtifactVerification
}

func (f fixedModels) Manifest(id string) (*modelmeta.Manifest, bool) {
	if id == f.manifest.ID {
		return f.manifest, true
	}
	return nil, false
}

func (f fixedModels) Verification(id string) (*modelmeta.ArtifactVerification, bool) {
	v, ok := f.verifs[id]
	return v, ok
}

type fixedSnapshots struct {
	snap *inventory.InventorySnapshotV2
}

func (s fixedSnapshots) Current() (*inventory.InventorySnapshotV2, error) {
	return s.snap, nil
}

// TestAdapterEstimateAgreesWithPlanner — the §8.4 numbers derived by
// the planner's deterministic evaluation and by the adapter's OWN
// estimate are IDENTICAL for the same model × request (two independent
// implementations of one contract must never drift silently).
func TestAdapterEstimateAgreesWithPlanner(t *testing.T) {
	f := newSSDFixture()
	policy := planner.DefaultPlannerPolicy()
	policy.SSDProfile = f.profile
	p, err := planner.NewPlanner(policy, fixedSnapshots{snap: f.snap}, fixedModels{
		manifest: f.model,
		verifs:   map[string]*modelmeta.ArtifactVerification{"deepseek-moe-test": {ModelID: "deepseek-moe-test", AllVerified: true, VerifiedAt: f.snap.ObservedAt.Add(-time.Hour)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.SetNow(func() time.Time { return f.snap.ObservedAt.Add(time.Second) })

	req := planner.ExecutionRequest{
		RequestID:     "req-ssd-1",
		ModelID:       "deepseek-moe-test",
		Task:          "chat-serving",
		ContextTokens: 4096,
		MaxConcurrent: 4, // the §8.8 cap (1) must bound it
		LatencyClass:  planner.LatencyInteractive,
		Principal:     "operator",
	}
	res := p.Plan(req)
	if !res.Selected || res.ReasonCode != planner.ReasonPlanOK {
		t.Fatalf("the golden ssd path must plan: %s (%v)", res.ReasonCode, res.Considered)
	}
	plan := res.Plan
	if plan.Strategy != infer_ssd.StrategySSDBackedMoE {
		t.Fatalf("strategy: %s, want ssd-backed-moe", plan.Strategy)
	}
	if plan.ConfigProfileID != f.profile.ProfileID {
		t.Fatalf("the plan's config profile must be the runtime profile: %q", plan.ConfigProfileID)
	}
	if plan.MaxConcurrent != 1 {
		t.Fatalf("the §8.8 cap must bound max_concurrent: %d", plan.MaxConcurrent)
	}

	// The adapter's OWN estimate for the same inputs.
	adapter, err := infer_ssd.NewAdapter(f.profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	est, err := adapter.Estimate(f.model.Execution(""), infer_ssd.EstimateRequest{ContextTokens: req.ContextTokens, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}

	// The numbers must agree: VRAM, KV, RAM, threads, storage demand +
	// floors (the GPU UUID differs by design — the adapter's estimate
	// is placement-independent).
	if plan.ResourceVector.GPU[0].VRAMBytes != est.GPU[0].VRAMBytes {
		t.Fatalf("VRAM drift: planner %d vs adapter %d", plan.ResourceVector.GPU[0].VRAMBytes, est.GPU[0].VRAMBytes)
	}
	if plan.ResourceVector.KVCacheBytes != est.KVCacheBytes {
		t.Fatalf("KV drift: planner %d vs adapter %d", plan.ResourceVector.KVCacheBytes, est.KVCacheBytes)
	}
	if plan.ResourceVector.RAMBytes != est.RAMBytes {
		t.Fatalf("RAM drift: planner %d vs adapter %d", plan.ResourceVector.RAMBytes, est.RAMBytes)
	}
	if plan.ResourceVector.CPUThreads != est.CPUThreads {
		t.Fatalf("CPU threads drift: planner %v vs adapter %v", plan.ResourceVector.CPUThreads, est.CPUThreads)
	}
	if plan.ResourceVector.Storage[0].MinimumFreeBytes != est.Storage[0].MinimumFreeBytes {
		t.Fatalf("storage free-bytes drift: planner %d vs adapter %d", plan.ResourceVector.Storage[0].MinimumFreeBytes, est.Storage[0].MinimumFreeBytes)
	}
	if plan.ResourceVector.Storage[0].MinimumReadMBps != est.Storage[0].MinimumReadMBps ||
		plan.ResourceVector.Storage[0].MinimumRandomReadIOPS != est.Storage[0].MinimumRandomReadIOPS ||
		plan.ResourceVector.Storage[0].MaximumP95LatencyMs != est.Storage[0].MaximumP95LatencyMs {
		t.Fatalf("storage floors drift: planner %+v vs adapter %+v", plan.ResourceVector.Storage[0], est.Storage[0])
	}

	// The generated explanation carries the §8 entries (the profile
	// binding, the GPU join, the measured qualification, the cap).
	codes := map[string]bool{}
	for _, e := range plan.Explanation {
		codes[e.Code] = true
	}
	for _, want := range []string{"ssd-profile-bound", "ssd-gpu-compatibility-joined", "ssd-qualified-measured", "ssd-concurrency-capped", "backend-experimental"} {
		if !codes[want] {
			t.Errorf("the explanation must carry %q; got %v", want, codes)
		}
	}
}

// TestQualifySSDFloors — the §8.6 qualification joins against measured
// devices: qualified, unmeasured (named refusal), floors unmet, free
// bytes insufficient.
func TestQualifySSDFloors(t *testing.T) {
	f := newSSDFixture()
	dev := f.snap.Storage[0]
	if q := infer_ssd.QualifySSD(f.profile, &dev, 4<<30); !q.Qualified {
		t.Fatalf("the golden device must qualify: %+v", q)
	}
	// No measurement → the named refusal (never schedule from unverified
	// estimates).
	unmeasured := dev
	unmeasured.ReadProfile = nil
	if q := infer_ssd.QualifySSD(f.profile, &unmeasured, 4<<30); q.Qualified || q.Code != infer_ssd.QualificationNoReadProfile {
		t.Fatalf("an unmeasured device must refuse by name: %+v", q)
	}
	// v19.7.0-shaped measurement (sequential only — no random phase):
	// the random floor refuses the qualification.
	seqOnly := dev
	rp := *dev.ReadProfile
	rp.RandomReadIOPS = 0
	rp.RandomReadP95Ms = 0
	seqOnly.ReadProfile = &rp
	if q := infer_ssd.QualifySSD(f.profile, &seqOnly, 4<<30); q.Qualified || q.Code != infer_ssd.QualificationFloorsUnmet {
		t.Fatalf("a sequential-only measurement must refuse the random floors: %+v", q)
	}
	// Floors unmet.
	slow := dev
	rp2 := *dev.ReadProfile
	rp2.RandomReadIOPS = 10000 // below the 50000 floor
	slow.ReadProfile = &rp2
	if q := infer_ssd.QualifySSD(f.profile, &slow, 4<<30); q.Qualified || q.Code != infer_ssd.QualificationFloorsUnmet {
		t.Fatalf("unmet floors must refuse: %+v", q)
	}
	// Free bytes insufficient.
	full := dev
	full.AvailableBytes = 8 << 30 // below experts (4GiB) + floor (64GiB)
	if q := infer_ssd.QualifySSD(f.profile, &full, 4<<30); q.Qualified || q.Code != infer_ssd.QualificationFreeBytesInsufficient {
		t.Fatalf("insufficient free bytes must refuse: %+v", q)
	}
	// Unknown device.
	if q := infer_ssd.QualifySSD(f.profile, nil, 4<<30); q.Qualified || q.Code != infer_ssd.QualificationDeviceUnknown {
		t.Fatalf("an unknown device must refuse: %+v", q)
	}
}

// TestGPUCompatibilityJoin — the §8.2 GPU join: the golden observed
// GPU passes; capability, CUDA, and driver mismatches each refuse by
// name.
func TestGPUCompatibilityJoin(t *testing.T) {
	f := newSSDFixture()
	gpu := f.snap.GPUs[0]
	if ok, detail := infer_ssd.GPUCompatibility(f.profile, gpu); !ok {
		t.Fatalf("the golden GPU must pass the join: %s", detail)
	}
	// Capability mismatch: sm_89 observed vs sm_120 declared.
	otherCap := gpu
	otherCap.ComputeCap = "8.9"
	if ok, detail := infer_ssd.GPUCompatibility(f.profile, otherCap); ok || !strings.Contains(detail, "not in the profile's declared capabilities") {
		t.Fatalf("a capability mismatch must refuse by name: %s", detail)
	}
	// Unparseable capability.
	bad := gpu
	bad.ComputeCap = "unknown"
	if ok, _ := infer_ssd.GPUCompatibility(f.profile, bad); ok {
		t.Fatalf("an unparseable capability must refuse")
	}
	// CUDA mismatch (exact-match policy).
	otherCUDA := gpu
	otherCUDA.CUDA = "12.6"
	if ok, detail := infer_ssd.GPUCompatibility(f.profile, otherCUDA); ok || !strings.Contains(detail, "does not exactly match") {
		t.Fatalf("a CUDA mismatch must refuse by name: %s", detail)
	}
	// Driver below the minimum.
	oldDriver := gpu
	oldDriver.Driver = "560.00"
	if ok, detail := infer_ssd.GPUCompatibility(f.profile, oldDriver); ok || !strings.Contains(detail, "below the profile's declared minimum") {
		t.Fatalf("an old driver must refuse by name: %s", detail)
	}
	// The architectural-suffix normalization: "12.0a" → sm_120.
	suffixed := gpu
	suffixed.ComputeCap = "12.0a"
	if ok, detail := infer_ssd.GPUCompatibility(f.profile, suffixed); !ok {
		t.Fatalf("the 12.0a suffix must normalize to sm_120: %s", detail)
	}
}
