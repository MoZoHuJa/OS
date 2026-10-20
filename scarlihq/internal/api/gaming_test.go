package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
)

// v19.3.1: /api/gaming contract tests (Gaming Stack Integration Plan,
// Phase 4). The endpoint is read-only: GET-only, auth-gated, always returns
// a JSON array. These tests pin that contract so it cannot silently become
// a write/launch surface.

func newGamingTestHandler(t *testing.T) (*Handler, *http.ServeMux) {
	t.Helper()
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.3.1")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux
}

func TestGamingInventoryRequiresAuth(t *testing.T) {
	_, mux := newGamingTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/gaming", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 without bearer token", rec.Code)
	}
}

func TestGamingInventoryRejectsNonGET(t *testing.T) {
	_, mux := newGamingTestHandler(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/gaming", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status=%d, want 405 (gaming inventory is read-only)", method, rec.Code)
		}
	}
}

func TestGamingInventoryReturnsArray(t *testing.T) {
	_, mux := newGamingTestHandler(t)
	// No host-status.json on the test machine → ReadOrStale returns a stale
	// zero-value status. The endpoint must still answer 200 with a JSON array
	// (never null) plus the envelope fields.
	req := httptest.NewRequest(http.MethodGet, "/api/gaming", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Gaming    []json.RawMessage `json:"gaming"`
		Timestamp string            `json:"timestamp"`
		Stale     bool              `json:"stale"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON envelope: %v", err)
	}
	if payload.Gaming == nil {
		t.Fatalf("gaming must be a JSON array, got null (body=%s)", rec.Body.String())
	}
}

func TestGamingInventoryEntryShape(t *testing.T) {
	// The Entry wire shape is pinned in the gaming package contract tests;
	// here we pin the HTTP envelope keys so clients cannot break silently.
	_, mux := newGamingTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/gaming", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var envelope map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{"gaming", "timestamp", "stale"} {
		if _, ok := envelope[key]; !ok {
			t.Fatalf("envelope missing key %q (body=%s)", key, rec.Body.String())
		}
	}
}
