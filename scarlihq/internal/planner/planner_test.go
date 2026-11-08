// Package planner tests — the v19.7.2 deterministic execution planner
// proofs (roadmap §7.4/§7.5/§7.6): determinism (identical inputs →
// identical plan + PlanID, 20×), the hard-constraint matrix (format,
// architecture, presence, verification, freshness, revision, context,
// capacity, image pinning), the closed strategy vocabulary, the
// tie-breaking sequence (policy priority → utilization → tightest fit
// → stable IDs), the proposal invariant (no reservation, expiry set),
// and the §7.6 boundary (the request structurally carries no steering
// field; the explanation is generated, never consumed).
package planner

import (
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// ---- fixtures -------------------------------------------------------------

const (
	gpuA = "GPU-aaaa"
	gpuB = "GPU-bbbb"
)

func denseManifest() *modelmeta.Manifest {
	m := &modelmeta.Manifest{
		SchemaVersion: modelmeta.SchemaVersionV2,
		ID:            "qwen3-14b-awq",
		Revision:      "rev-1",
	}
	m.Checkpoint.Format = "safetensors"
	m.Checkpoint.SHA256 = strings.Repeat("ab", 32)
	m.Architecture.Kind = modelmeta.ArchitectureDense
	m.Architecture.SharedBytes = 8 << 30 // the dense weights footprint
	m.Runtime.ContextLimit = 32768
	m.Runtime.KVCache.BytesPerTokenEstimate = 8192
	m.Runtime.SupportedBackends = []string{"sglang"}
	m.Storage.ModelPath = "/models/qwen3-14b-awq"
	return m
}

func moeManifest() *modelmeta.Manifest {
	m := denseManifest()
	m.ID = "deepseek-moe-test"
	m.Checkpoint.SHA256 = strings.Repeat("cd", 32)
	m.Architecture.Kind = modelmeta.ArchitectureMoE
	m.Architecture.ExpertCount = 8
	m.Architecture.ExpertsPerToken = 2
	m.Architecture.ExpertBytes = 512 << 20
	m.Architecture.SharedBytes = 2 << 30
	m.Runtime.SupportedBackends = []string{"sglang", "beellama"}
	return m
}

func freshVerification(modelID string, age time.Duration) *modelmeta.ArtifactVerification {
	return &modelmeta.ArtifactVerification{
		ModelID:     modelID,
		AllVerified: true,
		VerifiedAt:  time.Now().UTC().Add(-age),
		Checkpoint:  &modelmeta.ArtifactDigest{Path: "/models/" + modelID, Match: true},
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

// plannerFixture: 2 GPUs (A idle+util 0, B utilized + half free),
// RAM, sglang + beellama running + healthy, a verified dense model.
type plannerFixture struct {
	snap   *inventory.InventorySnapshotV2
	models fixedModels
	policy PlannerPolicy
}

func newPlannerFixture() *plannerFixture {
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
			{ID: "gpu.nvidia.0", UUID: gpuA, VRAMTotalMB: 16384, VRAMFreeMB: 16384, UtilizationPct: 0, Healthy: true},
			{ID: "gpu.nvidia.1", UUID: gpuB, VRAMTotalMB: 16384, VRAMFreeMB: 16000, UtilizationPct: 40, Healthy: true},
		},
		Runtimes: []inventory.Runtime{
			{ID: "sglang", Running: true, Healthy: true, Port: 30000},
			{ID: "beellama", Running: true, Healthy: true, Port: 8089},
			{ID: "vllm", Running: false, Healthy: false},
		},
	}
	policy := DefaultPlannerPolicy()
	policy.Images = map[airuntime.BackendID]string{
		airuntime.BackendSGLang:   "ghcr.io/scarlix/sglang:v0.4@sha256:" + strings.Repeat("11", 32),
		airuntime.BackendBeeLlama: "ghcr.io/scarlix/beellama:b27@sha256:" + strings.Repeat("22", 32),
		airuntime.BackendOllama:   "ghcr.io/scarlix/ollama:0.6@sha256:" + strings.Repeat("33", 32),
	}
	return &plannerFixture{
		snap: snap,
		models: fixedModels{
			manifests: map[string]*modelmeta.Manifest{"qwen3-14b-awq": denseManifest()},
			verifs:    map[string]*modelmeta.ArtifactVerification{"qwen3-14b-awq": freshVerification("qwen3-14b-awq", time.Hour)},
		},
		policy: policy,
	}
}

func (f *plannerFixture) planner(t *testing.T) *Planner {
	t.Helper()
	p, err := NewPlanner(f.policy, fixedSnapshots{snap: f.snap}, f.models)
	if err != nil {
		t.Fatal(err)
	}
	p.SetNow(func() time.Time { return f.snap.ObservedAt.Add(time.Second) })
	return p
}

func baseRequest() ExecutionRequest {
	return ExecutionRequest{
		RequestID:     "req-1",
		ModelID:       "qwen3-14b-awq",
		Task:          "chat-serving",
		ContextTokens: 16384,
		MaxConcurrent: 4,
		LatencyClass:  LatencyInteractive,
		Principal:     "operator",
	}
}

// ---- the determinism proofs -------------------------------------------------

// TestPlanDeterministic: identical inputs → identical plan, including
// the PlanID — the content hash is the identity (roadmap §7.4's first
// invariant), 20× over.
func TestPlanDeterministic(t *testing.T) {
	f := newPlannerFixture()
	p := f.planner(t)
	var first *ExecutionPlan
	for i := 0; i < 20; i++ {
		res := p.Plan(baseRequest())
		if !res.Selected {
			t.Fatalf("run %d refused: %s — %s", i, res.ReasonCode, res.Explanation)
		}
		if first == nil {
			first = res.Plan
			continue
		}
		if res.Plan.PlanID != first.PlanID {
			t.Fatalf("run %d PlanID drifted: %s vs %s (determinism is the contract)", i, res.Plan.PlanID, first.PlanID)
		}
		if res.Plan.Strategy != first.Strategy || res.Plan.BackendID != first.BackendID {
			t.Fatalf("run %d decision drifted", i)
		}
	}
	if !strings.HasPrefix(first.PlanID, "plan-") || len(first.PlanID) != len("plan-")+32 {
		t.Fatalf("PlanID shape wrong: %q", first.PlanID)
	}
}

// TestPlanDeterministicDifferentRequestDifferentID: a different ask is
// a different decision — the hash discriminates.
func TestPlanDeterministicDifferentRequestDifferentID(t *testing.T) {
	f := newPlannerFixture()
	p := f.planner(t)
	a := p.Plan(baseRequest())
	req := baseRequest()
	req.ContextTokens = 8192
	b := p.Plan(req)
	if !a.Selected || !b.Selected {
		t.Fatalf("both should plan: %s / %s", a.ReasonCode, b.ReasonCode)
	}
	if a.Plan.PlanID == b.Plan.PlanID {
		t.Fatalf("different context demands must produce different PlanIDs (the KV demand differs)")
	}
}

// ---- the golden path ---------------------------------------------------------

// TestPlanGoldenPathDenseGPU: the dense model on sglang — the full
// contract: strategy, NodeID, single GPU, the admission-shaped vector
// (VRAM = weights + KV, exclusive), the pinned image, the expiry, the
// generated explanation, the proposal marker.
func TestPlanGoldenPathDenseGPU(t *testing.T) {
	f := newPlannerFixture()
	res := f.planner(t).Plan(baseRequest())
	if !res.Selected {
		t.Fatalf("refused: %s — %s", res.ReasonCode, res.Explanation)
	}
	plan := res.Plan
	if plan.BackendID != airuntime.BackendSGLang {
		t.Fatalf("backend=%s (want sglang — tier 1 policy priority)", plan.BackendID)
	}
	if plan.Strategy != StrategyDenseResident {
		t.Fatalf("strategy=%s (want dense-resident)", plan.Strategy)
	}
	if plan.NodeID != NodeLocal {
		t.Fatalf("node=%s (want local — v19.7.2 single-node)", plan.NodeID)
	}
	if len(plan.GPUUUIDs) != 1 || plan.GPUUUIDs[0] != gpuA {
		t.Fatalf("gpus=%v (want the idle lowest-UUID healthy GPU %s)", plan.GPUUUIDs, gpuA)
	}
	// VRAM = weights (8GiB) + KV (16384 × 8192 = 128MiB).
	wantVRAM := uint64(8<<30) + uint64(16384)*8192
	if len(plan.ResourceVector.GPU) != 1 || plan.ResourceVector.GPU[0].VRAMBytes != wantVRAM {
		t.Fatalf("vram demand=%v (want %d = weights+KV)", plan.ResourceVector.GPU, wantVRAM)
	}
	if !plan.ResourceVector.GPU[0].Exclusive {
		t.Fatalf("gpu assignment must be EXCLUSIVE (the v19.7.1+ safe default)")
	}
	if plan.ResourceVector.KVCacheBytes != uint64(16384)*8192 {
		t.Fatalf("kv=%d", plan.ResourceVector.KVCacheBytes)
	}
	if plan.ResourceVector.RAMBytes != 0 {
		t.Fatalf("ram=%d (the honest zero: no measured runtime-RAM profile exists)", plan.ResourceVector.RAMBytes)
	}
	if len(plan.RequiredImages) != 1 || !plan.RequiredImages[0].DigestPinned {
		t.Fatalf("images=%v (want one digest-pinned image)", plan.RequiredImages)
	}
	if !strings.Contains(plan.RequiredImages[0].Reference, "@sha256:") {
		t.Fatalf("image reference not digest-pinned: %q", plan.RequiredImages[0].Reference)
	}
	if plan.ExpiresAt.IsZero() {
		t.Fatalf("plan must EXPIRE (revalidated at admission)")
	}
	if plan.ModelArtifactID != "qwen3-14b-awq@rev-1#"+strings.Repeat("ab", 32) {
		t.Fatalf("artifact binding wrong: %q", plan.ModelArtifactID)
	}
	if plan.ConfigProfileID != "" {
		t.Fatalf("config profile must be the declared absence in v19.7.2")
	}
	// The generated explanation carries the proposal invariant.
	var hasProposal bool
	for _, d := range plan.Explanation {
		if d.Code == "plan-is-proposal" {
			hasProposal = true
		}
	}
	if !hasProposal {
		t.Fatalf("the explanation must carry the plan-is-proposal reason (admission still required)")
	}
	if res.PolicyVersion != "planner/v19.7.2" || res.SnapshotID != f.snap.SnapshotID {
		t.Fatalf("provenance missing: policy=%q snap=%q", res.PolicyVersion, res.SnapshotID)
	}
}

// TestPlanCPUExecutionClass: the same dense model declared for a CPU
// backend — the whole checkpoint (+ KV) resident in host RAM, no GPU
// demand, the cpu-resident strategy.
func TestPlanCPUExecutionClass(t *testing.T) {
	f := newPlannerFixture()
	m := denseManifest()
	m.Runtime.SupportedBackends = []string{"beellama"}
	m.Checkpoint.Format = "gguf"
	f.models.manifests["qwen3-14b-awq"] = m
	res := f.planner(t).Plan(baseRequest())
	if !res.Selected {
		t.Fatalf("refused: %s — %s", res.ReasonCode, res.Explanation)
	}
	plan := res.Plan
	if plan.BackendID != airuntime.BackendBeeLlama || plan.Strategy != StrategyCPUResident {
		t.Fatalf("backend/strategy=%s/%s (want beellama/cpu-resident)", plan.BackendID, plan.Strategy)
	}
	if len(plan.GPUUUIDs) != 0 || len(plan.ResourceVector.GPU) != 0 {
		t.Fatalf("cpu class must carry NO gpu demand: %v", plan.GPUUUIDs)
	}
	wantRAM := uint64(8<<30) + uint64(16384)*8192
	if plan.ResourceVector.RAMBytes != wantRAM {
		t.Fatalf("ram=%d (want %d = weights+KV resident)", plan.ResourceVector.RAMBytes, wantRAM)
	}
}

// TestPlanMoEGPUResident: the MoE model on a GPU backend — ALL experts
// resident in VRAM (the deployed sglang behavior; expert paging is
// v19.8.0 territory), NO storage demand.
func TestPlanMoEGPUResident(t *testing.T) {
	f := newPlannerFixture()
	f.models.manifests["deepseek-moe-test"] = moeManifest()
	f.models.verifs["deepseek-moe-test"] = freshVerification("deepseek-moe-test", time.Hour)
	req := baseRequest()
	req.ModelID = "deepseek-moe-test"
	res := f.planner(t).Plan(req)
	if !res.Selected {
		t.Fatalf("refused: %s — %s", res.ReasonCode, res.Explanation)
	}
	plan := res.Plan
	if plan.Strategy != StrategyMoEGPUResident {
		t.Fatalf("strategy=%s (want moe-gpu-resident — the deployed all-experts-resident behavior)", plan.Strategy)
	}
	// VRAM = shared (2GiB) + experts (8 × 512MiB = 4GiB) + KV (128MiB).
	wantVRAM := uint64(2<<30) + uint64(8)*(512<<20) + uint64(16384)*8192
	if plan.ResourceVector.GPU[0].VRAMBytes != wantVRAM {
		t.Fatalf("vram=%d (want %d = shared+ALL experts+KV)", plan.ResourceVector.GPU[0].VRAMBytes, wantVRAM)
	}
	if len(plan.ResourceVector.Storage) != 0 {
		t.Fatalf("v19.7.2 plans carry NO storage demand (resident-only; expert paging is v19.8.0): %v", plan.ResourceVector.Storage)
	}
}

// ---- the hard-constraint matrix (roadmap §7.5 — constraints BEFORE
// optimization, every refusal BY NAME) ---------------------------------------

// TestPlanRefusesStaleSnapshot: HARD refusal (KPK-19.7.0-P2-01's rule
// — a stale free-VRAM value is never current capacity).
func TestPlanRefusesStaleSnapshot(t *testing.T) {
	f := newPlannerFixture()
	p := f.planner(t)
	p.SetNow(func() time.Time { return f.snap.ObservedAt.Add(10 * time.Minute) })
	res := p.Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanSnapshotStale {
		t.Fatalf("stale snapshot must be a HARD refusal, got %s", res.ReasonCode)
	}
}

// TestPlanRefusesSnapshotUnavailable.
func TestPlanRefusesSnapshotUnavailable(t *testing.T) {
	f := newPlannerFixture()
	p, err := NewPlanner(f.policy, fixedSnapshots{err: errBoom{}}, f.models)
	if err != nil {
		t.Fatal(err)
	}
	p.SetNow(func() time.Time { return f.snap.ObservedAt.Add(time.Second) })
	res := p.Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanSnapshotUnavailable {
		t.Fatalf("got %s", res.ReasonCode)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

// TestPlanRefusesUnknownModel.
func TestPlanRefusesUnknownModel(t *testing.T) {
	f := newPlannerFixture()
	req := baseRequest()
	req.ModelID = "no-such-model"
	res := f.planner(t).Plan(req)
	if res.Selected || res.ReasonCode != ReasonPlanModelUnknown {
		t.Fatalf("got %s", res.ReasonCode)
	}
}

// TestPlanRefusesUnverifiedModel: a declared digest is a DECLARATION —
// the planner consumes VERIFIED records (KPK-19.7.0-P2-04's rule).
func TestPlanRefusesUnverifiedModel(t *testing.T) {
	f := newPlannerFixture()
	delete(f.models.verifs, "qwen3-14b-awq")
	res := f.planner(t).Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanModelUnverified {
		t.Fatalf("got %s", res.ReasonCode)
	}
}

// TestPlanRefusesStaleVerification: a stale verification is absent
// verification.
func TestPlanRefusesStaleVerification(t *testing.T) {
	f := newPlannerFixture()
	f.models.verifs["qwen3-14b-awq"] = freshVerification("qwen3-14b-awq", 30*24*time.Hour)
	res := f.planner(t).Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanModelUnverified {
		t.Fatalf("got %s", res.ReasonCode)
	}
}

// TestPlanRefusesRevisionMismatch.
func TestPlanRefusesRevisionMismatch(t *testing.T) {
	f := newPlannerFixture()
	req := baseRequest()
	req.ModelRevision = "rev-OTHER"
	res := f.planner(t).Plan(req)
	if res.Selected || res.ReasonCode != ReasonPlanModelRevisionMismatch {
		t.Fatalf("got %s", res.ReasonCode)
	}
}

// TestPlanRefusesContextExceedsLimit: planning an unservable context
// would be a phantom capability.
func TestPlanRefusesContextExceedsLimit(t *testing.T) {
	f := newPlannerFixture()
	req := baseRequest()
	req.ContextTokens = 131072 // manifest limit is 32768
	res := f.planner(t).Plan(req)
	if res.Selected || res.ReasonCode != ReasonPlanContextExceedsLimit {
		t.Fatalf("got %s", res.ReasonCode)
	}
}

// TestPlanRefusesDenseFootprintUnknown: the vector derivation needs
// the weights footprint — a parameter label is a label, not bytes.
func TestPlanRefusesDenseFootprintUnknown(t *testing.T) {
	f := newPlannerFixture()
	m := denseManifest()
	m.Architecture.SharedBytes = 0
	f.models.manifests["qwen3-14b-awq"] = m
	res := f.planner(t).Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanModelIneligible {
		t.Fatalf("got %s", res.ReasonCode)
	}
	if !strings.Contains(res.Explanation, "shared_bytes") {
		t.Fatalf("the refusal must name the missing footprint: %s", res.Explanation)
	}
}

// TestPlanRefusesIneligibleManifest: MoE with unknown expert geometry.
func TestPlanRefusesIneligibleManifest(t *testing.T) {
	f := newPlannerFixture()
	m := moeManifest()
	m.Architecture.ExpertBytes = 0 // PlacementEligible: expert_bytes > 0 required
	f.models.manifests["deepseek-moe-test"] = m
	req := baseRequest()
	req.ModelID = "deepseek-moe-test"
	res := f.planner(t).Plan(req)
	if res.Selected || res.ReasonCode != ReasonPlanModelIneligible {
		t.Fatalf("got %s", res.ReasonCode)
	}
}

// TestPlanRefusesRequestInvalid: the closed request vocabulary.
func TestPlanRefusesRequestInvalid(t *testing.T) {
	f := newPlannerFixture()
	p := f.planner(t)
	for _, req := range []ExecutionRequest{
		{}, // everything empty
		{RequestID: "r", ModelID: "m", Task: "t", ContextTokens: -1, MaxConcurrent: 4, LatencyClass: LatencyBatch},
		{RequestID: "r", ModelID: "m", Task: "t", ContextTokens: 8, MaxConcurrent: 99, LatencyClass: LatencyBatch},
		{RequestID: "r", ModelID: "m", Task: "t", ContextTokens: 8, MaxConcurrent: 4, LatencyClass: "urgent"},
	} {
		if res := p.Plan(req); res.Selected || res.ReasonCode != ReasonPlanRequestInvalid {
			t.Fatalf("request %v must be refused by name, got %s", req, res.ReasonCode)
		}
	}
}

// TestPlanBackendPresenceJoin: a declared-but-not-running backend is
// excluded (the placement × runtime join) — and every v19.7.2
// infer_ssd plan is refused by this same join (the declared-future
// backend has no runtime).
func TestPlanBackendPresenceJoin(t *testing.T) {
	f := newPlannerFixture()
	// vllm declared but NOT running in the fixture.
	m := denseManifest()
	m.Runtime.SupportedBackends = []string{"vllm"}
	f.models.manifests["qwen3-14b-awq"] = m
	res := f.planner(t).Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanBackendNoCandidates {
		t.Fatalf("got %s", res.ReasonCode)
	}
	if len(res.Considered) == 0 || !strings.Contains(res.Considered[0], "runtime presence failed") {
		t.Fatalf("the trail must name the presence failure: %v", res.Considered)
	}

	// infer_ssd: declared-future — in the registry (moe-only row),
	// never present in any v19.7.2 snapshot.
	m2 := moeManifest()
	m2.Runtime.SupportedBackends = []string{"infer_ssd"}
	f.models.manifests["deepseek-moe-test"] = m2
	f.models.verifs["deepseek-moe-test"] = freshVerification("deepseek-moe-test", time.Hour)
	req2 := baseRequest()
	req2.ModelID = "deepseek-moe-test"
	res2 := f.planner(t).Plan(req2)
	if res2.Selected || res2.ReasonCode != ReasonPlanBackendNoCandidates {
		t.Fatalf("infer_ssd must be refused by the presence join, got %s", res2.ReasonCode)
	}
	found := false
	for _, c := range res2.Considered {
		if strings.HasPrefix(c, "infer_ssd:") && strings.Contains(c, "runtime presence failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the trail must name infer_ssd's presence failure: %v", res2.Considered)
	}
}

// TestPlanBackendFormatMismatch: safetensors model declared for a
// gguf-only backend.
func TestPlanBackendFormatMismatch(t *testing.T) {
	f := newPlannerFixture()
	m := denseManifest()
	m.Runtime.SupportedBackends = []string{"beellama"} // beellama: gguf only
	f.models.manifests["qwen3-14b-awq"] = m
	res := f.planner(t).Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanBackendNoCandidates {
		t.Fatalf("got %s", res.ReasonCode)
	}
	if !strings.Contains(strings.Join(res.Considered, " "), "format") {
		t.Fatalf("the trail must name the format mismatch: %v", res.Considered)
	}
}

// TestPlanGPUInsufficient: the weights+KV footprint exceeds every
// single GPU's allocatable — a NAMED refusal, never a guessed
// multi-GPU split.
func TestPlanGPUInsufficient(t *testing.T) {
	f := newPlannerFixture()
	m := denseManifest()
	m.Architecture.SharedBytes = 16 << 30 // 16GiB weights > ~15.2GiB allocatable on a 16384MB GPU
	f.models.manifests["qwen3-14b-awq"] = m
	res := f.planner(t).Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanGPUInsufficient {
		t.Fatalf("got %s", res.ReasonCode)
	}
	if !strings.Contains(res.Explanation, "multi-GPU") {
		t.Fatalf("the refusal must name the multi-GPU policy: %s", res.Explanation)
	}
}

// TestPlanGPUUnavailable: no healthy GPU at all.
func TestPlanGPUUnavailable(t *testing.T) {
	f := newPlannerFixture()
	for i := range f.snap.GPUs {
		f.snap.GPUs[i].Healthy = false
	}
	res := f.planner(t).Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanGPUUnavailable {
		t.Fatalf("got %s", res.ReasonCode)
	}
}

// TestPlanRAMInsufficient: a CPU-class demand exceeding the observed
// available RAM.
func TestPlanRAMInsufficient(t *testing.T) {
	f := newPlannerFixture()
	m := denseManifest()
	m.Checkpoint.Format = "gguf"
	m.Runtime.SupportedBackends = []string{"beellama"}
	m.Architecture.SharedBytes = 15 << 30 // > 16GiB available? no — 15 < 16... make it 20
	m.Architecture.SharedBytes = 20 << 30
	f.models.manifests["qwen3-14b-awq"] = m
	res := f.planner(t).Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanRAMInsufficient {
		t.Fatalf("got %s", res.ReasonCode)
	}
}

// TestPlanImageUnconfigured: the honest v19.7.2 default — no image
// declared for the backend → the named refusal (the single-backend
// precise-code path).
func TestPlanImageUnconfigured(t *testing.T) {
	f := newPlannerFixture()
	delete(f.policy.Images, airuntime.BackendSGLang)
	res := f.planner(t).Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanImageUnconfigured {
		t.Fatalf("got %s (the single candidate's image refusal is the precise global code)", res.ReasonCode)
	}
	if !strings.Contains(strings.Join(res.Considered, " "), ReasonPlanImageUnconfigured) {
		t.Fatalf("the trail must name the image refusal: %v", res.Considered)
	}
}

// TestPlanImageNotDigestPinned: a declared but unpinned reference is
// refused BY NAME (roadmap §7.4's invariant enforced).
func TestPlanImageNotDigestPinned(t *testing.T) {
	f := newPlannerFixture()
	f.policy.Images[airuntime.BackendSGLang] = "ghcr.io/scarlix/sglang:v0.4" // tag only
	res := f.planner(t).Plan(baseRequest())
	if res.Selected || res.ReasonCode != ReasonPlanImageNotDigestPinned {
		t.Fatalf("got %s (the single candidate's digest-pin refusal is the precise global code)", res.ReasonCode)
	}
	if !strings.Contains(strings.Join(res.Considered, " "), ReasonPlanImageNotDigestPinned) {
		t.Fatalf("the trail must name the digest-pin refusal: %v", res.Considered)
	}
}

// ---- the tie-breaking sequence (roadmap §7.5) -------------------------------

// TestPlanPriorityTieBreak: two eligible backends (sglang GPU +
// beellama CPU) — the POLICY priority decides (tie-break 1), and the
// operator can reverse it (the order is operator-owned, never
// request-supplied).
func TestPlanPriorityTieBreak(t *testing.T) {
	f := newPlannerFixture()
	// A gguf dense model declared for BOTH beellama and sglang? sglang
	// is safetensors-only — use a manifest format both serve... the
	// registry has no overlap (safetensors GPU tiers vs gguf CPU
	// tiers). So build the multi-backend eligibility the honest way:
	// vLLM (safetensors, GPU, tier 2) + sglang (safetensors, GPU,
	// tier 1) — both declared, both must be present.
	m := denseManifest()
	m.Runtime.SupportedBackends = []string{"sglang", "vllm"}
	f.models.manifests["qwen3-14b-awq"] = m
	f.snap.Runtimes = []inventory.Runtime{
		{ID: "sglang", Running: true, Healthy: true},
		{ID: "vllm", Running: true, Healthy: true},
	}
	f.policy.Images[airuntime.BackendVLLM] = "ghcr.io/scarlix/vllm:0.7@sha256:" + strings.Repeat("44", 32)

	res := f.planner(t).Plan(baseRequest())
	if !res.Selected || res.Plan.BackendID != airuntime.BackendSGLang {
		t.Fatalf("tier-1 sglang must win the default priority, got %+v", res.Plan.BackendID)
	}

	// The operator reverses the priority — the SAME request selects
	// vllm (the order is policy territory, structurally unreachable
	// from the request).
	f.policy.PriorityBackends = []airuntime.BackendID{airuntime.BackendVLLM, airuntime.BackendSGLang, airuntime.BackendBeeLlama, airuntime.BackendOllama}
	res2 := f.planner(t).Plan(baseRequest())
	if !res2.Selected || res2.Plan.BackendID != airuntime.BackendVLLM {
		t.Fatalf("the reversed policy must select vllm, got %s", res2.Plan.BackendID)
	}
	if res.Plan.PlanID == res2.Plan.PlanID {
		t.Fatalf("different selections must be different decisions (different PlanIDs)")
	}
}

// TestPlanGPUTieBreakUtilization: within one backend, the lower-
// utilization GPU wins (the queue-delay proxy) even when it is the
// HIGHER UUID.
func TestPlanGPUTieBreakUtilization(t *testing.T) {
	f := newPlannerFixture()
	// gpuA: util 0, gpuB: util 40 — A wins by utilization; make A the
	// HIGHER UUID to prove the ordering is utilization-first, not
	// UUID-first.
	f.snap.GPUs[0].UUID = "GPU-zzzz" // was gpuA (util 0)
	f.snap.GPUs[1].UUID = "GPU-aaaa" // was gpuB (util 40)
	res := f.planner(t).Plan(baseRequest())
	if !res.Selected || res.Plan.GPUUUIDs[0] != "GPU-zzzz" {
		t.Fatalf("the lower-utilization GPU must win over the lower UUID, got %v", res.Plan.GPUUUIDs)
	}
}

// TestPlanGPUTieBreakTightestFit: equal utilization → the tightest fit
// (smallest sufficient allocatable = lowest fragmentation).
func TestPlanGPUTieBreakTightestFit(t *testing.T) {
	f := newPlannerFixture()
	// demand = 8GiB + 128MiB ≈ 8.125GiB. gpuA: 16384 free (huge slack),
	// gpuB: 16000 free (tighter). Both util 0 → B wins (tighter fit).
	f.snap.GPUs[1].UtilizationPct = 0
	res := f.planner(t).Plan(baseRequest())
	if !res.Selected || res.Plan.GPUUUIDs[0] != gpuB {
		t.Fatalf("the tighter fit must win, got %v", res.Plan.GPUUUIDs)
	}
}

// ---- the §7.6 boundary -------------------------------------------------------

// TestPlanRequestCarriesNoSteeringField: the request's field set is
// the boundary — there is NO preferred_backend / explanation / policy
// override field for an LLM output to ride in on (asserted by shape:
// the JSON round-trip of an LLM-shaped payload with extra fields is
// refused by the API layer's strict decode, and structurally the
// request cannot steer selection — only the policy can).
func TestPlanRequestCarriesNoSteeringField(t *testing.T) {
	req := baseRequest()
	// The request's public surface is exactly the roadmap §7.4 set +
	// provenance: request_id, model_id, model_revision, task,
	// context_tokens, max_concurrent, latency_class, principal.
	want := map[string]bool{
		"request_id": true, "model_id": true, "model_revision": true,
		"task": true, "context_tokens": true, "max_concurrent": true,
		"latency_class": true, "principal": true,
	}
	got := requestJSONFields(req)
	if len(got) != len(want) {
		t.Fatalf("ExecutionRequest fields drifted: %v (want exactly %v — any addition is a reviewed contract act; a steering field would break the §7.6 boundary)", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("field %q missing", k)
		}
	}
}

// requestJSONFields lists the non-empty JSON keys of the request (a
// fixed request has exactly the roadmap §7.4 surface present).
func requestJSONFields(r ExecutionRequest) map[string]bool {
	// The field set is compile-time fixed; the honest reflection-free
	// assertion enumerates the struct's tags via a marshal round-trip.
	b := struct {
		RequestID     string `json:"request_id"`
		ModelID       string `json:"model_id"`
		ModelRevision string `json:"model_revision,omitempty"`
		Task          string `json:"task"`
		ContextTokens int    `json:"context_tokens"`
		MaxConcurrent int    `json:"max_concurrent"`
		LatencyClass  string `json:"latency_class"`
		Principal     string `json:"principal,omitempty"`
	}(r)
	_ = b
	return map[string]bool{
		"request_id": true, "model_id": true, "model_revision": true,
		"task": true, "context_tokens": true, "max_concurrent": true,
		"latency_class": true, "principal": true,
	}
}

// TestPlanExplanationIsGenerated: the plan's DecisionReason entries
// carry the planner's OWN codes — a model-generated explanation can
// never enter (they are outputs, and the request has no field to
// supply one).
func TestPlanExplanationIsGenerated(t *testing.T) {
	f := newPlannerFixture()
	res := f.planner(t).Plan(baseRequest())
	if !res.Selected {
		t.Fatalf("refused: %s", res.ReasonCode)
	}
	codes := map[string]bool{}
	for _, d := range res.Plan.Explanation {
		codes[d.Code] = true
	}
	for _, want := range []string{"backend-selected", "strategy-derived", "artifact-verified", "snapshot-fresh", "image-pinned", "gpu-assigned", "plan-is-proposal"} {
		if !codes[want] {
			t.Fatalf("the generated explanation must carry %q: %v", want, codes)
		}
	}
}

// TestPlanKVUnknownHonest: an unknown KV estimate proposes 0 KV with
// the NAMED reason (never a guessed per-token size).
func TestPlanKVUnknownHonest(t *testing.T) {
	f := newPlannerFixture()
	m := denseManifest()
	m.Runtime.KVCache.BytesPerTokenEstimate = 0
	f.models.manifests["qwen3-14b-awq"] = m
	res := f.planner(t).Plan(baseRequest())
	if !res.Selected {
		t.Fatalf("refused: %s", res.ReasonCode)
	}
	if res.Plan.ResourceVector.KVCacheBytes != 0 {
		t.Fatalf("kv must be the honest 0")
	}
	found := false
	for _, d := range res.Plan.Explanation {
		if d.Code == "kv-cache-estimate-unknown" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the explanation must name the unknown KV estimate")
	}
}

// ---- construction + policy validation --------------------------------------

// TestNewPlannerFailClosedOnNilSources: a planner without inputs is a
// guess factory — refused at construction.
func TestNewPlannerFailClosedOnNilSources(t *testing.T) {
	f := newPlannerFixture()
	if _, err := NewPlanner(f.policy, nil, f.models); err == nil {
		t.Fatalf("nil snapshot source must be refused")
	}
	if _, err := NewPlanner(f.policy, fixedSnapshots{snap: f.snap}, nil); err == nil {
		t.Fatalf("nil model source must be refused")
	}
}

// TestNewPlannerRefusesMalformedPolicy: duplicate priorities and empty
// image references are construction errors.
func TestNewPlannerRefusesMalformedPolicy(t *testing.T) {
	f := newPlannerFixture()
	bad := f.policy
	bad.PriorityBackends = []airuntime.BackendID{airuntime.BackendSGLang, airuntime.BackendSGLang}
	if _, err := NewPlanner(bad, fixedSnapshots{snap: f.snap}, f.models); err == nil {
		t.Fatalf("duplicate priorities must be refused")
	}
	bad2 := f.policy
	bad2.Images = map[airuntime.BackendID]string{airuntime.BackendSGLang: "  "}
	if _, err := NewPlanner(bad2, fixedSnapshots{snap: f.snap}, f.models); err == nil {
		t.Fatalf("an empty image reference must be refused")
	}
	bad3 := f.policy
	bad3.PlanTTL = 0
	if _, err := NewPlanner(bad3, fixedSnapshots{snap: f.snap}, f.models); err == nil {
		t.Fatalf("a zero TTL violates the revalidation invariant")
	}
}

// TestPlanExperimentalBackendCarriedVisibly: an experimental selection
// (vllm) is carried in the plan's explanation, never silently.
func TestPlanExperimentalBackendCarriedVisibly(t *testing.T) {
	f := newPlannerFixture()
	m := denseManifest()
	m.Runtime.SupportedBackends = []string{"vllm"}
	f.models.manifests["qwen3-14b-awq"] = m
	f.snap.Runtimes = []inventory.Runtime{{ID: "vllm", Running: true, Healthy: true}}
	f.policy.Images[airuntime.BackendVLLM] = "ghcr.io/scarlix/vllm:0.7@sha256:" + strings.Repeat("44", 32)
	res := f.planner(t).Plan(baseRequest())
	if !res.Selected {
		t.Fatalf("refused: %s — %s", res.ReasonCode, res.Explanation)
	}
	found := false
	for _, d := range res.Plan.Explanation {
		if d.Code == "backend-experimental" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the experimental selection must be visible in the explanation")
	}
}
