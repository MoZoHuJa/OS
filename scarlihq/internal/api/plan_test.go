// Package api tests — the deterministic execution planner HTTP surface
// (v19.7.2, Compute Fabric Phase 3): the 503-before-configuration
// contract, the 401 auth gate, the 400 strict-decode refusal (a typo'd
// field — and an LLM-shaped payload with steering fields — is a
// refusal, never a silent default), the 405 method guard, the
// RESULT-as-data contract (a refusal is 200 + selected=false + the
// machine-readable reason_code), the server-side principal stamp, the
// golden path with the full plan contract, and HTTP-level determinism
// (identical POSTs → identical PlanIDs).
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
	"github.com/MoZoHuJa/OS/scarlihq/internal/backends/infer_ssd"
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
	"github.com/MoZoHuJa/OS/scarlihq/internal/planner"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// ---- the planner HTTP fixture ------------------------------------------------

type planSnapshots struct {
	snap *inventory.InventorySnapshotV2
}

func (s planSnapshots) Current() (*inventory.InventorySnapshotV2, error) {
	return s.snap, nil
}

type planModels struct {
	manifests map[string]*modelmeta.Manifest
	verifs    map[string]*modelmeta.ArtifactVerification
}

func (s planModels) Manifest(id string) (*modelmeta.Manifest, bool) {
	m, ok := s.manifests[id]
	return m, ok
}

func (s planModels) Verification(id string) (*modelmeta.ArtifactVerification, bool) {
	v, ok := s.verifs[id]
	return v, ok
}

func planTestPlanner(t *testing.T) *planner.Planner {
	t.Helper()
	observed := time.Now().UTC()
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
	m.Runtime.KVCache.BytesPerTokenEstimate = 8192
	m.Runtime.SupportedBackends = []string{"sglang"}
	m.Storage.ModelPath = "/models/qwen3-14b-awq"

	snap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "snap0000000000000000000000000001",
		ObservedAt:    observed,
		Host:          storage.HostResources{CPUThreads: 16, RAMTotalBytes: 32 << 30, RAMAvailableBytes: 16 << 30},
		GPUs: []inventory.GPU{
			{ID: "gpu.nvidia.0", UUID: "GPU-aaaa", VRAMTotalMB: 16384, VRAMFreeMB: 16384, UtilizationPct: 0, Healthy: true},
		},
		Runtimes: []inventory.Runtime{{ID: "sglang", Running: true, Healthy: true, Port: 30000}},
	}
	policy := planner.DefaultPlannerPolicy()
	policy.Images = map[airuntime.BackendID]string{
		airuntime.BackendSGLang: "ghcr.io/scarlix/sglang:v0.4@sha256:" + strings.Repeat("11", 32),
	}
	p, err := planner.NewPlanner(policy, planSnapshots{snap: snap}, planModels{
		manifests: map[string]*modelmeta.Manifest{"qwen3-14b-awq": m},
		verifs: map[string]*modelmeta.ArtifactVerification{"qwen3-14b-awq": {
			ModelID: "qwen3-14b-awq", ManifestFingerprint: modelmeta.ManifestFingerprint(m), AllVerified: true, VerifiedAt: observed.Add(-time.Hour),
			Checkpoint: &modelmeta.ArtifactDigest{Path: "/models/qwen3-14b-awq", Match: true},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.SetNow(func() time.Time { return observed.Add(time.Second) })
	return p
}

// planTestPlannerWithSSDProfile builds the standard planner fixture
// with an ENABLED InferSSD runtime profile riding the policy (the
// v19.8.0 view test's fixture — the profile rides the policy, the
// planner validates it fail-closed).
func planTestPlannerWithSSDProfile(t *testing.T) *planner.Planner {
	t.Helper()
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
	profile.Resources.Storage.DeviceID = "storage.nvme.0"
	profile.Resources.Storage.ModelPath = "/var/lib/scarlix/models/x"
	profile.Resources.Storage.ExpertPath = "/var/lib/scarlix/experts/x"
	profile.Resources.Storage.MinimumFreeBytes = 64 << 30
	profile.Resources.Storage.MinimumReadMBps = 2000
	profile.Resources.Storage.MinimumRandomReadIOPS = 50000
	profile.Resources.Storage.MaximumP95LatencyMs = 1.5
	profile.Network.NetworkProfile = "scarlix-compute-internal"
	profile.Health.Type = "http"
	profile.Health.Path = "/health"
	profile.Health.StartupTimeoutSeconds = 600

	observed := time.Now().UTC()
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
	m.Runtime.KVCache.BytesPerTokenEstimate = 8192
	m.Runtime.SupportedBackends = []string{"sglang"}
	m.Storage.ModelPath = "/models/qwen3-14b-awq"
	snap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "snap0000000000000000000000000001",
		ObservedAt:    observed,
		Host:          storage.HostResources{CPUThreads: 16, RAMTotalBytes: 32 << 30, RAMAvailableBytes: 16 << 30},
		GPUs: []inventory.GPU{
			{ID: "gpu.nvidia.0", UUID: "GPU-aaaa", VRAMTotalMB: 16384, VRAMFreeMB: 16384, UtilizationPct: 0, Healthy: true},
		},
		Runtimes: []inventory.Runtime{{ID: "sglang", Running: true, Healthy: true, Port: 30000}},
	}
	policy := planner.DefaultPlannerPolicy()
	policy.Images = map[airuntime.BackendID]string{
		airuntime.BackendSGLang: "ghcr.io/scarlix/sglang:v0.4@sha256:" + strings.Repeat("11", 32),
	}
	policy.SSDProfile = profile
	p, err := planner.NewPlanner(policy, planSnapshots{snap: snap}, planModels{
		manifests: map[string]*modelmeta.Manifest{"qwen3-14b-awq": m},
		verifs: map[string]*modelmeta.ArtifactVerification{"qwen3-14b-awq": {
			ModelID: "qwen3-14b-awq", ManifestFingerprint: modelmeta.ManifestFingerprint(m), AllVerified: true, VerifiedAt: observed.Add(-time.Hour),
			Checkpoint: &modelmeta.ArtifactDigest{Path: "/models/qwen3-14b-awq", Match: true},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.SetNow(func() time.Time { return observed.Add(time.Second) })
	return p
}

func planTestServer(t *testing.T) *http.ServeMux {
	t.Helper()
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.7.2")
	h.ConfigurePlanner(planTestPlanner(t))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func planDo(t *testing.T, mux *http.ServeMux, method, path, body string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var doc map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	return rec, doc
}

const planAllowedBody = `{
        "request_id": "req-1",
        "model_id": "qwen3-14b-awq",
        "task": "chat-serving",
        "context_tokens": 16384,
        "max_concurrent": 4,
        "latency_class": "interactive"
}`

// ---- the proofs ---------------------------------------------------------------

// TestPlanRouteDisabledWithoutPlanner: the surface answers 503 with
// the named guidance until ConfigurePlanner is called — never a silent
// no-op.
func TestPlanRouteDisabledWithoutPlanner(t *testing.T) {
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.7.2")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/compute/plan", strings.NewReader(planAllowedBody))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s (want 503 — the configuration contract)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "execution planner not configured") {
		t.Fatalf("the 503 must name the surface: %s", rec.Body.String())
	}
}

// TestPlanRequiresAuth: no token → 401 (the read gate still gates —
// observation grants no planning either without identity).
func TestPlanRequiresAuth(t *testing.T) {
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.7.2")
	h.ConfigurePlanner(planTestPlanner(t))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/compute/plan", strings.NewReader(planAllowedBody))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d (want 401)", rec.Code)
	}
}

// TestPlanStrictDecodeRefusesSteeringFields: a typo'd field is 400 —
// and so is an LLM-shaped payload carrying steering fields the request
// contract deliberately does not have (roadmap §7.6's boundary,
// enforced at the decode): preferred_backend, explanation, policy.
func TestPlanStrictDecodeRefusesSteeringFields(t *testing.T) {
	mux := planTestServer(t)
	for _, body := range []string{
		`{"request_id":"r","modle_id":"typo"}`,
		`{"request_id":"r","model_id":"m","task":"t","context_tokens":8,"max_concurrent":4,"latency_class":"batch","preferred_backend":"vllm"}`,
		`{"request_id":"r","model_id":"m","task":"t","context_tokens":8,"max_concurrent":4,"latency_class":"batch","explanation":"the model suggests vllm"}`,
		`{"request_id":"r","model_id":"m","task":"t","context_tokens":8,"max_concurrent":4,"latency_class":"batch","policy":{"priority":["ollama"]}}`,
	} {
		rec, _ := planDo(t, mux, http.MethodPost, "/api/compute/plan", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status=%d (want 400 — unknown fields are refusals, the §7.6 boundary)", body, rec.Code)
		}
	}
}

// TestPlanMethodGuard: unsupported methods are 405 BY NAME.
func TestPlanMethodGuard(t *testing.T) {
	mux := planTestServer(t)
	rec, _ := planDo(t, mux, http.MethodDelete, "/api/compute/plan", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d (want 405 — plan supports GET and POST)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "GET") || !strings.Contains(rec.Body.String(), "POST") {
		t.Fatalf("the 405 must name the supported methods: %s", rec.Body.String())
	}
}

// TestPlanGetRegistryView: GET reports the policy + the CLOSED
// capability registry (5 rows, sorted, each with its adapter note).
func TestPlanGetRegistryView(t *testing.T) {
	mux := planTestServer(t)
	rec, doc := planDo(t, mux, http.MethodGet, "/api/compute/plan", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	backends, ok := doc["backends"].([]interface{})
	if !ok || len(backends) != 5 {
		t.Fatalf("the registry view must carry the 5 closed rows, got %v", doc["backends"])
	}
	first, _ := backends[0].(map[string]interface{})
	if first["backend_id"] != "beellama" {
		t.Fatalf("the view must be sorted: %v", first["backend_id"])
	}
	if _, ok := doc["policy"]; !ok {
		t.Fatalf("the policy view is missing: %v", doc)
	}
}

// TestPlanGoldenPath: an admissible ask produces the full plan contract
// — selected=true, the plan with backend/strategy/node/vector/images/
// expiry, the snapshot + policy provenance, and the server-side
// principal stamp.
func TestPlanGoldenPath(t *testing.T) {
	mux := planTestServer(t)
	rec, doc := planDo(t, mux, http.MethodPost, "/api/compute/plan", planAllowedBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if doc["selected"] != true || doc["reason_code"] != "ok" {
		t.Fatalf("selected=%v reason=%v body=%s", doc["selected"], doc["reason_code"], rec.Body.String())
	}
	plan, _ := doc["plan"].(map[string]interface{})
	if plan == nil {
		t.Fatalf("the plan object is missing")
	}
	if plan["backend_id"] != "sglang" || plan["strategy"] != "dense-resident" || plan["node_id"] != "local" {
		t.Fatalf("plan core wrong: %v / %v / %v", plan["backend_id"], plan["strategy"], plan["node_id"])
	}
	if plan["principal"] != "household" {
		t.Fatalf("the principal must be stamped SERVER-SIDE (the household token resolves to the household admin principal), got %v", plan["principal"])
	}
	if plan["config_profile_id"] != "" {
		t.Fatalf("config profile must be the declared absence")
	}
	gpus, _ := plan["gpu_uuids"].([]interface{})
	if len(gpus) != 1 || gpus[0] != "GPU-aaaa" {
		t.Fatalf("gpu_uuids wrong: %v", gpus)
	}
	images, _ := plan["required_images"].([]interface{})
	if len(images) != 1 {
		t.Fatalf("required_images wrong: %v", images)
	}
	if doc["snapshot_id"] != "snap0000000000000000000000000001" || doc["policy_version"] != "planner/v19.8.1" {
		t.Fatalf("provenance missing: %v / %v", doc["snapshot_id"], doc["policy_version"])
	}
}

// TestPlanRefusalIsData: a refusal is 200 + selected=false + the
// machine-readable reason_code (the planner working, never an HTTP
// error).
func TestPlanRefusalIsData(t *testing.T) {
	mux := planTestServer(t)
	rec, doc := planDo(t, mux, http.MethodPost, "/api/compute/plan", `{
                "request_id": "req-2",
                "model_id": "no-such-model",
                "task": "chat-serving",
                "context_tokens": 16384,
                "max_concurrent": 4,
                "latency_class": "interactive"
        }`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d (a refusal is DATA, never an HTTP error)", rec.Code)
	}
	if doc["selected"] != false {
		t.Fatalf("selected=%v (want false)", doc["selected"])
	}
	if doc["reason_code"] != planner.ReasonPlanModelUnknown {
		t.Fatalf("reason_code=%v (want %s)", doc["reason_code"], planner.ReasonPlanModelUnknown)
	}
	if doc["plan"] != nil {
		t.Fatalf("a refusal carries no plan object")
	}
}

// TestPlanDeterministicOverHTTP: two identical POSTs → identical
// PlanIDs (the determinism contract, visible at the surface).
func TestPlanDeterministicOverHTTP(t *testing.T) {
	mux := planTestServer(t)
	_, doc1 := planDo(t, mux, http.MethodPost, "/api/compute/plan", planAllowedBody)
	_, doc2 := planDo(t, mux, http.MethodPost, "/api/compute/plan", planAllowedBody)
	p1, _ := doc1["plan"].(map[string]interface{})
	p2, _ := doc2["plan"].(map[string]interface{})
	if p1 == nil || p2 == nil {
		t.Fatalf("both must plan")
	}
	if p1["plan_id"] != p2["plan_id"] {
		t.Fatalf("identical requests must produce identical PlanIDs: %v vs %v", p1["plan_id"], p2["plan_id"])
	}
}

// TestPlanRequestInvalidIsData: a malformed request (unknown latency
// class) is 200 + the named refusal, not a 400 (shape-valid JSON,
// contract-invalid values are the planner's named refusals).
func TestPlanRequestInvalidIsData(t *testing.T) {
	mux := planTestServer(t)
	rec, doc := planDo(t, mux, http.MethodPost, "/api/compute/plan", `{
                "request_id": "req-3",
                "model_id": "qwen3-14b-awq",
                "task": "chat-serving",
                "context_tokens": 16384,
                "max_concurrent": 4,
                "latency_class": "urgent"
        }`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d (contract-invalid values are named refusals)", rec.Code)
	}
	if doc["reason_code"] != planner.ReasonPlanRequestInvalid {
		t.Fatalf("reason_code=%v", doc["reason_code"])
	}
}

// ---- the v19.8.0 Phase 4 views (roadmap §8) -------------------------------

// TestPlanStateCarriesSSDProfileView: the GET view carries the compact
// InferSSD profile audit view — JSON null when not configured (§8.8's
// honest initial status), the profile facts when an enabled profile
// rides the policy.
func TestPlanStateCarriesSSDProfileView(t *testing.T) {
	// Not configured: ssd_profile is JSON null (the honest absence).
	mux := planTestServer(t)
	_, doc := planDo(t, mux, http.MethodGet, "/api/compute/plan", "")
	if v, ok := doc["ssd_profile"]; !ok || v != nil {
		t.Fatalf("ssd_profile must be present-and-null when not configured: %v (%T)", v, v)
	}

	// Configured: the compact audit view carries the §8 facts.
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.8.0")
	h.ConfigurePlanner(planTestPlannerWithSSDProfile(t))
	mux2 := http.NewServeMux()
	h.RegisterRoutes(mux2)
	_, doc2 := planDo(t, mux2, http.MethodGet, "/api/compute/plan", "")
	profile, ok := doc2["ssd_profile"].(map[string]interface{})
	if !ok {
		t.Fatalf("ssd_profile must be the compact view: %v", doc2["ssd_profile"])
	}
	if profile["profile_id"] != "infer-ssd-moe-experimental" || profile["enabled"] != true || profile["experimental"] != true {
		t.Fatalf("the audit view must carry the enablement + experimental facts: %v", profile)
	}
	if profile["strategy"] != "ssd-backed-moe" || profile["storage_device_id"] != "storage.nvme.0" {
		t.Fatalf("the audit view must carry the §8 facts: %v", profile)
	}
}

// TestPlanStateCarriesExpertPacks: the GET view carries the expert pack
// manifests when a loader is wired (bound + unbound verdicts), and
// OMITS the field entirely when none is (the honest absence, never an
// empty masquerade).
func TestPlanStateCarriesExpertPacks(t *testing.T) {
	// No loader: the field is absent.
	mux := planTestServer(t)
	_, doc := planDo(t, mux, http.MethodGet, "/api/compute/plan", "")
	if _, ok := doc["expert_packs"]; ok {
		t.Fatalf("expert_packs must be omitted without a loader: %v", doc["expert_packs"])
	}

	// A loader: the bound + unbound verdicts ride the view.
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.8.0")
	h.ConfigurePlanner(planTestPlanner(t))
	h.ConfigureExpertPacks(func() []modelmeta.BoundExpertPack {
		bound := modelmeta.ExpertPackManifest{}
		bound.SchemaVersion = modelmeta.SchemaVersionArtifact
		bound.ArtifactID = "pack-a"
		bound.ArtifactType = modelmeta.ArtifactTypeSGLangExpertPack
		bound.BackendID = modelmeta.BackendIDSGLang
		unbound := bound
		unbound.ArtifactID = "pack-orphan"
		unbound.Model.ID = "unknown-model"
		return []modelmeta.BoundExpertPack{
			{Manifest: bound, Bound: true, BoundModelID: "deepseek-moe-test"},
			{Manifest: unbound, Bound: false, BindingReasons: []string{"no model manifest declares model.id \"unknown-model\""}},
		}
	})
	mux2 := http.NewServeMux()
	h.RegisterRoutes(mux2)
	_, doc2 := planDo(t, mux2, http.MethodGet, "/api/compute/plan", "")
	packs, ok := doc2["expert_packs"].([]interface{})
	if !ok || len(packs) != 2 {
		t.Fatalf("expert_packs must carry both verdicts: %v", doc2["expert_packs"])
	}
	first, _ := packs[0].(map[string]interface{})
	if first["bound"] != true || first["bound_model_id"] != "deepseek-moe-test" {
		t.Fatalf("the bound verdict must ride the view: %v", first)
	}
	second, _ := packs[1].(map[string]interface{})
	if second["bound"] != false {
		t.Fatalf("the unbound verdict must ride the view: %v", second)
	}
	manifest, _ := first["manifest"].(map[string]interface{})
	if manifest == nil || manifest["backend_id"] != "sglang" {
		t.Fatalf("the pack manifest must ride the view: %v", first["manifest"])
	}
}
