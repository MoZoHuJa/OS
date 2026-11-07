package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// newInventoryV2TestHandler builds a handler with the collector seam
// injected to a fixed snapshot — the HTTP surface is what is under test,
// not the collectors (their own packages carry their tests).
func newInventoryV2TestHandler(t *testing.T, snap *inventory.InventorySnapshotV2) (*Handler, *http.ServeMux) {
	t.Helper()
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.7.0")
	h.ConfigureInventoryV2(func() (*inventory.InventorySnapshotV2, error) {
		return snap, nil
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux
}

func fixedSnapshot(age time.Duration) *inventory.InventorySnapshotV2 {
	observed := time.Now().UTC().Add(-age)
	return &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "fixed0000000000000000000000000001",
		ObservedAt:    observed,
		Host: storage.HostResources{
			CPUThreads:        12,
			CPUModel:          "Test CPU",
			RAMTotalBytes:     16 << 30,
			RAMAvailableBytes: 8 << 30,
		},
		GPUs:     []inventory.GPU{},
		Storage:  []storage.Device{{ID: "storage.nvme.0", Kind: storage.DeviceNVMe, MountPoints: []string{"/"}}},
		Runtimes: []inventory.Runtime{},
		Models: []inventory.ModelExecutionView{{
			Catalog:        inventory.Model{ID: "sglang"},
			ManifestStatus: "none",
			Placement:      modelmeta.PlacementEligibility{Eligible: false, Reasons: []string{"no verified manifest"}},
		}},
		Health: []inventory.Health{},
		Sources: []storage.SourceStatus{
			{Component: "storage", Source: "/proc/self/mounts", Status: "ok"},
			{Component: "storage.host", Source: "/proc/meminfo", Status: "ok"},
			{Component: "gpus", Source: "nvidia-smi", Status: "empty"},
			{Component: "models", Source: "models.yaml", Status: "ok"},
			{Component: "models.manifests", Source: "models.d", Status: "empty"},
		},
	}
}

func TestInventoryV2RequiresAuth(t *testing.T) {
	for _, path := range []string{"/api/inventory/v2", "/api/inventory/v2/storage", "/api/inventory/v2/models"} {
		_, mux := newInventoryV2TestHandler(t, fixedSnapshot(0))
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s status=%d, want 401 without bearer token", path, rec.Code)
		}
	}
}

// TestInventoryV2RejectsNonGET — the read-only negative control: no
// write surface exists on ANY of the three endpoints (405 BY NAME).
func TestInventoryV2RejectsNonGET(t *testing.T) {
	for _, path := range []string{"/api/inventory/v2", "/api/inventory/v2/storage", "/api/inventory/v2/models"} {
		_, mux := newInventoryV2TestHandler(t, fixedSnapshot(0))
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			req := httptest.NewRequest(method, path, strings.NewReader("{}"))
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s status=%d, want 405 (inventory is read-only)", method, path, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "inventory is read-only") {
				t.Fatalf("%s %s 405 body must name the read-only refusal: %s", method, path, rec.Body.String())
			}
		}
	}
}

func TestInventoryV2FullShape(t *testing.T) {
	_, mux := newInventoryV2TestHandler(t, fixedSnapshot(0))
	req := httptest.NewRequest(http.MethodGet, "/api/inventory/v2", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var env map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// The flattened snapshot + the freshness envelope keys.
	for _, key := range []string{"schema_version", "snapshot_id", "observed_at",
		"host", "gpus", "storage", "runtimes", "models", "health", "sources",
		"freshness"} {
		if _, ok := env[key]; !ok {
			t.Errorf("envelope missing key %q (body=%s)", key, rec.Body.String())
		}
	}
	fr := env["freshness"].(map[string]interface{})
	for _, key := range []string{"observed_at", "age_seconds", "max_age_seconds", "stale"} {
		if _, ok := fr[key]; !ok {
			t.Errorf("freshness missing key %q: %v", key, fr)
		}
	}
	if env["schema_version"] != "scarlix.inventory/v2" {
		t.Errorf("schema_version=%v", env["schema_version"])
	}
}

func TestInventoryV2StaleMarking(t *testing.T) {
	// A 5-minute-old snapshot under the 60s default policy → stale=true.
	_, mux := newInventoryV2TestHandler(t, fixedSnapshot(5*time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/api/inventory/v2", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var env struct {
		Freshness inventory.Freshness `json:"freshness"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !env.Freshness.Stale {
		t.Error("a 5-minute-old snapshot must be marked stale under the 60s policy")
	}
	if env.Freshness.AgeSeconds < 299 {
		t.Errorf("age_seconds=%v, want >= 300", env.Freshness.AgeSeconds)
	}
}

func TestInventoryV2StorageSection(t *testing.T) {
	_, mux := newInventoryV2TestHandler(t, fixedSnapshot(0))
	req := httptest.NewRequest(http.MethodGet, "/api/inventory/v2/storage", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Freshness inventory.Freshness    `json:"freshness"`
		Host      storage.HostResources  `json:"host"`
		Storage   []storage.Device       `json:"storage"`
		Sources   []storage.SourceStatus `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if env.Storage == nil || len(env.Storage) != 1 {
		t.Fatalf("storage section wrong: %+v", env.Storage)
	}
	if env.Storage[0].ID != "storage.nvme.0" {
		t.Errorf("device id=%q", env.Storage[0].ID)
	}
	// Section sources are filtered: storage + storage.host ONLY.
	for _, s := range env.Sources {
		if s.Component != "storage" && s.Component != "storage.host" {
			t.Errorf("unfiltered source %q in the storage section", s.Component)
		}
	}
	if len(env.Sources) != 2 {
		t.Errorf("sources=%d, want 2 (storage + storage.host)", len(env.Sources))
	}
	if env.Host.CPUThreads != 12 {
		t.Errorf("host cpu_threads=%d", env.Host.CPUThreads)
	}
}

func TestInventoryV2ModelsSection(t *testing.T) {
	_, mux := newInventoryV2TestHandler(t, fixedSnapshot(0))
	req := httptest.NewRequest(http.MethodGet, "/api/inventory/v2/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Freshness inventory.Freshness            `json:"freshness"`
		Models    []inventory.ModelExecutionView `json:"models"`
		Sources   []storage.SourceStatus         `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(env.Models) != 1 {
		t.Fatalf("models=%d", len(env.Models))
	}
	if env.Models[0].Catalog.ID != "sglang" {
		t.Errorf("model id=%q", env.Models[0].Catalog.ID)
	}
	if env.Models[0].ManifestStatus != "none" {
		t.Errorf("manifest_status=%q", env.Models[0].ManifestStatus)
	}
	if env.Models[0].Placement.Eligible {
		t.Error("a model with no manifest must be placement-ineligible")
	}
	for _, s := range env.Sources {
		if s.Component != "models" && s.Component != "models.manifests" {
			t.Errorf("unfiltered source %q in the models section", s.Component)
		}
	}
}

func TestInventoryV2CollectorError(t *testing.T) {
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.7.0")
	h.ConfigureInventoryV2(func() (*inventory.InventorySnapshotV2, error) {
		return nil, errFixed
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/inventory/v2", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d, want 500 when the collector fails (never a fabricated snapshot)", rec.Code)
	}
}

var errFixed = &fixedError{}

type fixedError struct{}

func (*fixedError) Error() string { return "fixed collector failure" }

// TestInventoryV2MaxAgeEnv — the policy knob parses; garbage falls back
// to the 60s default (informational marker semantics, documented).
func TestInventoryV2MaxAgeEnv(t *testing.T) {
	t.Setenv("SCARLIHQ_INVENTORY_MAX_AGE", "120")
	if d, ok := inventoryV2MaxAge(); !ok || d != 120*time.Second {
		t.Errorf("maxAge=%v ok=%v, want 120s", d, ok)
	}
	t.Setenv("SCARLIHQ_INVENTORY_MAX_AGE", "garbage")
	if d, ok := inventoryV2MaxAge(); ok || d != inventoryMaxAgeDef {
		t.Errorf("maxAge=%v ok=%v, want the 60s default on garbage", d, ok)
	}
	t.Setenv("SCARLIHQ_INVENTORY_MAX_AGE", "-5")
	if d, ok := inventoryV2MaxAge(); ok || d != inventoryMaxAgeDef {
		t.Errorf("maxAge=%v ok=%v, want the 60s default on a negative value", d, ok)
	}
}

// TestInventoryV2RealCollector — with NO seam injected, the handler
// serves the REAL read-only snapshot (built from the live /proc: always
// valid, possibly empty sections with honest source statuses).
func TestInventoryV2RealCollector(t *testing.T) {
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.7.0")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/inventory/v2", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		SchemaVersion string              `json:"schema_version"`
		SnapshotID    string              `json:"snapshot_id"`
		Storage       []json.RawMessage   `json:"storage"`
		Sources       []json.RawMessage   `json:"sources"`
		Freshness     inventory.Freshness `json:"freshness"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if env.SchemaVersion != "scarlix.inventory/v2" {
		t.Errorf("schema_version=%q", env.SchemaVersion)
	}
	if len(env.SnapshotID) != 32 {
		t.Errorf("snapshot_id=%q", env.SnapshotID)
	}
	if env.Storage == nil || env.Sources == nil {
		t.Error("real collector must honor the []-not-null contract")
	}
}
