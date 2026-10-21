package api

// AK-2 (v19.4.1): Agent Kernel REST surface tests — auth-gating, request
// validation, the full session lifecycle over HTTP, and the denial-is-200
// evaluate contract.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
)

// testTimeAK gives the api package a fixed clock for deterministic store setup.
func testTimeAK() time.Time {
	return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
}

func newAgentKernelTestServer(t *testing.T, store *agentkernel.Store, policy agentkernel.Policy) *http.ServeMux {
	t.Helper()
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.4.2")
	if store != nil {
		if err := h.ConfigureAgentKernel(store, policy); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func agentRequest(t *testing.T, mux *http.ServeMux, method, path, body string) (int, map[string]interface{}) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]interface{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("non-JSON response (%d): %s", rec.Code, rec.Body.String())
		}
	}
	return rec.Code, out
}

func TestAgentKernelRoutesDisabledByDefault(t *testing.T) {
	mux := newAgentKernelTestServer(t, nil, agentkernel.Policy{})
	for _, path := range []string{"/api/agent/sessions", "/api/agent/sessions/transition", "/api/agent/sessions/evaluate"} {
		code, body := agentRequest(t, mux, http.MethodPost, path, `{"session_id":"x"}`)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("%s: expected 503 when unconfigured, got %d (%v)", path, code, body)
		}
	}
}

func TestAgentKernelRoutesRequireAuth(t *testing.T) {
	mux := newAgentKernelTestServer(t, nil, agentkernel.Policy{})
	req := httptest.NewRequest(http.MethodGet, "/api/agent/sessions", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}
	// Wrong token also rejected (constant-time compare path).
	req = httptest.NewRequest(http.MethodGet, "/api/agent/sessions", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", rec.Code)
	}
}

func agentTestStore(t *testing.T) *agentkernel.Store {
	t.Helper()
	store, err := agentkernel.NewStore(8)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAgentSessionLifecycleOverHTTP(t *testing.T) {
	mux := newAgentKernelTestServer(t, agentTestStore(t), agentkernel.Policy{})

	// 1. Create — happy path.
	code, created := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions",
		`{"agent_id":"coder.agent-one","owner":"zmor","capabilities":["status.read","compute.plan"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d (%v)", code, created)
	}
	if created["state"] != "CREATED" {
		t.Fatalf("create: expected CREATED, got %v", created["state"])
	}
	sessionID, _ := created["session_id"].(string)
	if !strings.HasPrefix(sessionID, "aks-") {
		t.Fatalf("create: expected aks- session id, got %q", sessionID)
	}
	capsList, _ := created["capabilities"].([]interface{})
	if len(capsList) != 2 {
		t.Fatalf("create: expected 2 capabilities, got %v", created["capabilities"])
	}

	// 2. Evaluate while CREATED → denial (200, allow=false — a denial is a
	// successful evaluation, not an HTTP error).
	code, decision := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate",
		`{"session_id":"`+sessionID+`","capability":"status.read"}`)
	if code != http.StatusOK {
		t.Fatalf("evaluate on CREATED: expected 200, got %d (%v)", code, decision)
	}
	if allow, _ := decision["allow"].(bool); allow {
		t.Fatalf("evaluate on CREATED: expected denial, got %v", decision)
	}

	// 3. Activate.
	code, active := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/transition",
		`{"session_id":"`+sessionID+`","to":"ACTIVE"}`)
	if code != http.StatusOK || active["state"] != "ACTIVE" {
		t.Fatalf("activate: expected 200/ACTIVE, got %d (%v)", code, active)
	}

	// 4. Evaluate granted capability → allow.
	code, decision = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate",
		`{"session_id":"`+sessionID+`","capability":"status.read"}`)
	if code != http.StatusOK {
		t.Fatalf("evaluate granted: expected 200, got %d", code)
	}
	if allow, _ := decision["allow"].(bool); !allow {
		t.Fatalf("evaluate granted: expected allow, got %v", decision)
	}

	// 5. Evaluate NOT-granted capability → 200 + allow=false + rule reason.
	code, decision = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate",
		`{"session_id":"`+sessionID+`","capability":"compute.reserve"}`)
	if code != http.StatusOK {
		t.Fatalf("evaluate not-granted: expected 200, got %d", code)
	}
	if allow, _ := decision["allow"].(bool); allow {
		t.Fatal("evaluate not-granted: expected denial")
	}
	reason, _ := decision["reason"].(string)
	if !strings.Contains(reason, "rule/capability-not-granted") {
		t.Fatalf("evaluate not-granted: reason must cite the rule, got %q", reason)
	}

	// 6. List shows the session.
	code, list := agentRequest(t, mux, http.MethodGet, "/api/agent/sessions", "")
	if code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", code)
	}
	if count, _ := list["count"].(float64); count != 1 {
		t.Fatalf("list: expected count=1, got %v", list["count"])
	}

	// 7. Close + terminal check.
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/transition",
		`{"session_id":"`+sessionID+`","to":"CLOSED"}`)
	if code != http.StatusOK {
		t.Fatalf("close: expected 200, got %d", code)
	}
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/transition",
		`{"session_id":"`+sessionID+`","to":"ACTIVE"}`)
	if code != http.StatusConflict {
		t.Fatalf("transition from CLOSED: expected 409, got %d", code)
	}
}

func TestAgentSessionCreateValidation(t *testing.T) {
	mux := newAgentKernelTestServer(t, agentTestStore(t), agentkernel.Policy{})

	cases := []struct {
		name string
		body string
	}{
		{"shell.exec capability rejected", `{"agent_id":"coder.agent-one","owner":"zmor","capabilities":["shell.exec"]}`},
		{"unknown capability rejected", `{"agent_id":"coder.agent-one","owner":"zmor","capabilities":["made.up"]}`},
		{"no capabilities rejected", `{"agent_id":"coder.agent-one","owner":"zmor","capabilities":[]}`},
		{"invalid agent id rejected", `{"agent_id":"BAD ID","owner":"zmor","capabilities":["status.read"]}`},
		{"no owner rejected", `{"agent_id":"coder.agent-one","owner":"","capabilities":["status.read"]}`},
		{"unknown JSON field rejected", `{"agent_id":"coder.agent-one","owner":"zmor","capabilities":["status.read"],"extra":1}`},
		{"two JSON objects rejected", `{"agent_id":"coder.agent-one","owner":"zmor","capabilities":["status.read"]}{"agent_id":"x.y"}`},
		{"malformed JSON rejected", `{not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions", tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%v)", code, body)
			}
		})
	}
}

func TestAgentSessionEvaluateValidation(t *testing.T) {
	store := agentTestStore(t)
	mux := newAgentKernelTestServer(t, store, agentkernel.Policy{})
	agent, _ := agentkernel.NewAgentIdentity("coder.agent-one", "zmor", testTimeAK())
	caps, _ := agentkernel.NewCapabilitySet(agentkernel.CapabilityStatusRead)
	rec, _ := store.Create(agent, caps, testTimeAK())
	_, _ = store.Transition(rec.Session.ID, agentkernel.SessionActive, testTimeAK().Add(1))

	// Unknown capability → 400 (closed enumeration).
	code, _ := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate",
		`{"session_id":"`+rec.Session.ID+`","capability":"shell.exec"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown capability: expected 400, got %d", code)
	}
	// Unknown session → 404.
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate",
		`{"session_id":"aks-does-not-exist","capability":"status.read"}`)
	if code != http.StatusNotFound {
		t.Fatalf("unknown session: expected 404, got %d", code)
	}
	// Illegal transition target → 400.
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/transition",
		`{"session_id":"`+rec.Session.ID+`","to":"CREATED"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("illegal transition target: expected 400, got %d", code)
	}
	// Method checks.
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate", "")
	if code != http.StatusBadRequest {
		t.Fatalf("empty evaluate body: expected 400, got %d", code)
	}
	code, _ = agentRequest(t, mux, http.MethodGet, "/api/agent/sessions/transition", "")
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on transition: expected 405, got %d", code)
	}
	code, _ = agentRequest(t, mux, http.MethodDelete, "/api/agent/sessions", "")
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE on sessions: expected 405, got %d", code)
	}
}
