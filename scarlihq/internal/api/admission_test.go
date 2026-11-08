// Package api tests — the admission HTTP surface (v19.7.1, Compute
// Fabric Phase 2): the 503-before-configuration contract, the 401 auth
// gate, the 400 malformed-request refusal, the 405 method guard, the
// DECISION-as-data contract (a refusal is 200 + allowed=false, never an
// HTTP error), the principal stamp (SERVER-side, never caller-supplied),
// and the release 409 on unknown IDs.
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
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// ---- the admission HTTP fixture (a working gate, one allowed ask) ----

type fixedSnapshots struct {
	snap *inventory.InventorySnapshotV2
}

func (s fixedSnapshots) Current() (*inventory.InventorySnapshotV2, error) {
	return s.snap, nil
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

func admissionTestManager(t *testing.T) *admission.Manager {
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
	m.Runtime.SupportedBackends = []string{"sglang"}
	m.Storage.ModelPath = "/models/qwen3-14b-awq"

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
	}
	ledger, err := admission.NewLedger(filepath.Join(t.TempDir(), "admission-ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	mgr := admission.NewManager(admission.DefaultPolicy(), fixedSnapshots{snap: snap}, fixedModels{
		manifests: map[string]*modelmeta.Manifest{"qwen3-14b-awq": m},
		verifs: map[string]*modelmeta.ArtifactVerification{"qwen3-14b-awq": {
			ModelID: "qwen3-14b-awq", AllVerified: true, VerifiedAt: observed.Add(-time.Hour),
			Checkpoint: &modelmeta.ArtifactDigest{Path: "/models/qwen3-14b-awq", Match: true},
		}},
	}, ledger)
	mgr.SetNow(func() time.Time { return observed.Add(time.Second) })
	return mgr
}

const admissionAllowedBody = `{
	"request_id": "req-1",
	"model_id": "qwen3-14b-awq",
	"backend_id": "sglang",
	"requirements": {
		"gpu": [{"gpu_uuid": "GPU-aaaa", "vram_bytes": 8589934592, "exclusive": true}],
		"ram_bytes": 4294967296,
		"cpu_threads": 4,
		"storage": [{"device_id": "storage.nvme.0", "minimum_free_bytes": 107374182400}],
		"ports": [30000]
	}
}`

func admissionTestServer(t *testing.T) (*http.ServeMux, *admission.Manager) {
	t.Helper()
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.7.1")
	mgr := admissionTestManager(t)
	h.ConfigureAdmission(mgr)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, mgr
}

func admissionDo(t *testing.T, mux *http.ServeMux, method, path, body string) (*httptest.ResponseRecorder, map[string]interface{}) {
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

// ---- the proofs -------------------------------------------------------------

// TestAdmissionRoutesDisabledByDefault: the surface is OPT-IN — until
// ConfigureAdmission is called the routes answer 503 with the named
// guidance, never a silent no-op.
func TestAdmissionRoutesDisabledByDefault(t *testing.T) {
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.7.1")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/compute/admission", strings.NewReader(admissionAllowedBody))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s (want 503 — the opt-in contract)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "admission gate not configured") {
		t.Fatalf("the 503 must name the gate and the enablement env: %s", rec.Body.String())
	}
}

// TestAdmissionRequiresAuth: no token → 401 (the admin gate).
func TestAdmissionRequiresAuth(t *testing.T) {
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.7.1")
	h.ConfigureAdmission(admissionTestManager(t))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/compute/admission", strings.NewReader(admissionAllowedBody))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s (want 401 — admission RESERVES capacity, the admin gate applies)", rec.Code, rec.Body.String())
	}
}

// TestAdmissionMalformedJSONRefused: a typo'd field is a 400 (strict
// decode — DisallowUnknownFields — never a silent default).
func TestAdmissionMalformedJSONRefused(t *testing.T) {
	mux, _ := admissionTestServer(t)
	rec, _ := admissionDo(t, mux, http.MethodPost, "/api/compute/admission", `{"request_id":"req-1","modle_id":"typo"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s (want 400 — a typo'd dimension is a refusal)", rec.Code, rec.Body.String())
	}
}

// TestAdmissionMethodGuard: unsupported methods are 405 BY NAME.
func TestAdmissionMethodGuard(t *testing.T) {
	mux, _ := admissionTestServer(t)
	rec, _ := admissionDo(t, mux, http.MethodDelete, "/api/compute/admission", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d (want 405 — admission supports GET and POST)", rec.Code)
	}
}

// TestAdmissionDecideAllowed: the golden path — an admissible ask is
// allowed=true with the assignment, lease, snapshot + policy provenance.
func TestAdmissionDecideAllowed(t *testing.T) {
	mux, mgr := admissionTestServer(t)
	rec, doc := admissionDo(t, mux, http.MethodPost, "/api/compute/admission", admissionAllowedBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if doc["allowed"] != true {
		t.Fatalf("allowed=%v body=%s", doc["allowed"], rec.Body.String())
	}
	if doc["reason_code"] != admission.ReasonOK {
		t.Fatalf("reason_code=%v", doc["reason_code"])
	}
	if id, _ := doc["admission_id"].(string); id == "" {
		t.Fatalf("the allowed decision must carry the admission_id: %s", rec.Body.String())
	}
	if _, ok := doc["assignment"].(map[string]interface{}); !ok {
		t.Fatalf("the allowed decision must carry the assignment: %s", rec.Body.String())
	}
	if _, ok := doc["lease_expires_at"].(string); !ok {
		t.Fatalf("the allowed decision must carry the lease expiry: %s", rec.Body.String())
	}
	if pv, _ := doc["policy_version"].(string); pv != mgr.Policy().PolicyVersion {
		t.Fatalf("policy_version=%v (want %s)", pv, mgr.Policy().PolicyVersion)
	}
	// The hold is visible in the state surface.
	_, state := admissionDo(t, mux, http.MethodGet, "/api/compute/admission", "")
	held, _ := state["held"].([]interface{})
	if len(held) != 1 {
		t.Fatalf("held=%d after one allowed admission (want 1): %s", len(held), rec.Body.String())
	}
}

// TestAdmissionRefusalIsData: a VRAM-exhausted ask is 200 +
// allowed=false + the machine-readable reason_code — the gate working,
// never an HTTP error.
func TestAdmissionRefusalIsData(t *testing.T) {
	mux, _ := admissionTestServer(t)
	greedy := strings.Replace(admissionAllowedBody, `"vram_bytes": 8589934592`, `"vram_bytes": 9792525655024`, 1)
	rec, doc := admissionDo(t, mux, http.MethodPost, "/api/compute/admission", greedy)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s (want 200 — the refusal IS the answer)", rec.Code, rec.Body.String())
	}
	if doc["allowed"] != false {
		t.Fatalf("allowed=%v (want false)", doc["allowed"])
	}
	if rc, _ := doc["reason_code"].(string); rc != admission.ReasonGPUVRAMExhausted {
		t.Fatalf("reason_code=%v (want %s)", rc, admission.ReasonGPUVRAMExhausted)
	}
	if _, ok := doc["admission_id"].(string); ok {
		t.Fatalf("a refusal must NOT carry an admission_id")
	}
	// And NOTHING was reserved.
	_, state := admissionDo(t, mux, http.MethodGet, "/api/compute/admission", "")
	held, _ := state["held"].([]interface{})
	if len(held) != 0 {
		t.Fatalf("held=%d after a refused admission (want 0 — failure reserves nothing)", len(held))
	}
}

// TestAdmissionIdempotencyReplay: the same idempotency key replays the
// ORIGINAL decision — one hold, not two.
func TestAdmissionIdempotencyReplay(t *testing.T) {
	mux, _ := admissionTestServer(t)
	withKey := strings.Replace(admissionAllowedBody, `"request_id": "req-1"`, `"request_id": "req-1", "idempotency_key": "key-42"`, 1)
	rec1, doc1 := admissionDo(t, mux, http.MethodPost, "/api/compute/admission", withKey)
	if rec1.Code != http.StatusOK || doc1["allowed"] != true {
		t.Fatalf("first: status=%d allowed=%v", rec1.Code, doc1["allowed"])
	}
	rec2, doc2 := admissionDo(t, mux, http.MethodPost, "/api/compute/admission", withKey)
	if rec2.Code != http.StatusOK || doc2["allowed"] != true {
		t.Fatalf("replay: status=%d allowed=%v", rec2.Code, doc2["allowed"])
	}
	if doc1["admission_id"] != doc2["admission_id"] {
		t.Fatalf("the replay must return the ORIGINAL admission_id (%v vs %v)", doc1["admission_id"], doc2["admission_id"])
	}
	_, state := admissionDo(t, mux, http.MethodGet, "/api/compute/admission", "")
	held, _ := state["held"].([]interface{})
	if len(held) != 1 {
		t.Fatalf("held=%d after an idempotent replay (want 1)", len(held))
	}
}

// TestAdmissionReleaseFlow: releasing a held admission works; releasing
// an unknown ID is a NAMED 409, never a silent no-op.
func TestAdmissionReleaseFlow(t *testing.T) {
	mux, _ := admissionTestServer(t)
	_, doc := admissionDo(t, mux, http.MethodPost, "/api/compute/admission", admissionAllowedBody)
	id, _ := doc["admission_id"].(string)
	if id == "" {
		t.Fatalf("no admission_id in the allowed decision")
	}
	rec, rdoc := admissionDo(t, mux, http.MethodPost, "/api/compute/admission/release", `{"admission_id":"`+id+`"}`)
	if rec.Code != http.StatusOK || rdoc["status"] != "released" {
		t.Fatalf("release: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// A double release is a 409 with the named error.
	rec2, _ := admissionDo(t, mux, http.MethodPost, "/api/compute/admission/release", `{"admission_id":"`+id+`"}`)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("double release: status=%d (want 409)", rec2.Code)
	}
	// An unknown ID is a 409 too.
	rec3, _ := admissionDo(t, mux, http.MethodPost, "/api/compute/admission/release", `{"admission_id":"no-such-id"}`)
	if rec3.Code != http.StatusConflict {
		t.Fatalf("unknown release: status=%d (want 409)", rec3.Code)
	}
}

// TestAdmissionReleaseValidation: an empty body is a 400, and GET on the
// release route is a 405.
func TestAdmissionReleaseValidation(t *testing.T) {
	mux, _ := admissionTestServer(t)
	rec, _ := admissionDo(t, mux, http.MethodPost, "/api/compute/admission/release", `{"admission_id":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty admission_id: status=%d (want 400)", rec.Code)
	}
	rec2, _ := admissionDo(t, mux, http.MethodGet, "/api/compute/admission/release", "")
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on release: status=%d (want 405)", rec2.Code)
	}
}
