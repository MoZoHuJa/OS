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
			ModelID: "qwen3-14b-awq", AllVerified: true, VerifiedAt: observed.Add(-time.Hour),
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
	if doc["snapshot_id"] != "snap0000000000000000000000000001" || doc["policy_version"] != "planner/v19.7.2" {
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
