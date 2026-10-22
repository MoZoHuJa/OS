package api

// AK-4 (v19.4.5): append-only audit sink REST surface tests — auth-gating,
// 503 default, evaluations persisted before serving (fail-closed), admin
// acts appended after their persist, the bounded read endpoint (newest
// first, limit validation, malformed lines reported), and sink persistence
// across handler instances.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
)

// newAgentAuditTestServer builds a server with store + registry + audit
// sink (the AK-4 deployment shape) and returns the mux + sink.
func newAgentAuditTestServer(t *testing.T) (*http.ServeMux, *agentkernel.AuditSink) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg, err := agentkernel.NewGrantRegistry(filepath.Join(dir, "grants.json"), []string{"zmor"}, agentkernel.DefaultMaxGrants, agentkernel.DefaultMaxAgents)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(); err != nil {
		t.Fatal(err)
	}
	sink, err := agentkernel.NewAuditSink(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sink.Close() })
	store, err := agentkernel.NewStore(agentkernel.DefaultMaxSessions)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.4.5")
	if err := h.ConfigureAgentKernel(store, reg.Policy()); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureAgentGrants(reg); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureAgentAudit(sink); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, sink
}

func TestAgentAuditRouteRequireAuth(t *testing.T) {
	mux, _ := newAgentAuditTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/agent/audit", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}
}

func TestAgentAuditRouteDisabledByDefault(t *testing.T) {
	mux := newAgentKernelTestServer(t, nil, agentkernel.Policy{})
	code, body := agentRequest(t, mux, http.MethodGet, "/api/agent/audit", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("audit without sink: expected 503, got %d (%v)", code, body)
	}
}

func TestAgentAuditEvaluationsPersistedBeforeServing(t *testing.T) {
	mux, sink := newAgentAuditTestServer(t)

	// Grant + create + activate + evaluate (allow) → the evaluate line MUST
	// be in the sink BEFORE the response is served (asserted post-response:
	// the sink read sees the line immediately — no async flush window).
	code, _ := agentRequest(t, mux, http.MethodPost, "/api/agent/grants", `{"agent_id":"coder.agent-one","capability":"status.read","owner":"zmor","granted_by":"admin.zmor"}`)
	if code != http.StatusCreated {
		t.Fatalf("grant: %d", code)
	}
	code, body := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.agent-one"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d (%v)", code, body)
	}
	sid, _ := body["session_id"].(string)
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/transition", `{"session_id":"`+sid+`","to":"ACTIVE"}`)
	if code != http.StatusOK {
		t.Fatalf("activate: %d", code)
	}
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate", `{"session_id":"`+sid+`","capability":"status.read"}`)
	if code != http.StatusOK || body["allow"] != true {
		t.Fatalf("evaluate allow: %d (%v)", code, body)
	}
	// Denial (not-granted capability) is ALSO persisted (denial-is-200 twin).
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate", `{"session_id":"`+sid+`","capability":"compute.reserve"}`)
	if code != http.StatusOK {
		t.Fatalf("evaluate deny: %d", code)
	}

	lines, errs, err := sink.ReadLast(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("malformed audit lines: %v", errs)
	}
	// 1 grant act + 2 evaluations (allow + deny), newest first.
	if len(lines) != 3 {
		t.Fatalf("audit lines = %d, want 3 (grant + allow + deny)", len(lines))
	}
	if lines[0].Type != agentkernel.AuditTypeEvaluate {
		t.Fatalf("newest must be the deny evaluation, got %s", lines[0].Type)
	}
	var allowCount, denyCount int
	for _, l := range lines {
		if l.Type == agentkernel.AuditTypeEvaluate {
			if l.SessionID != sid {
				t.Fatalf("evaluate line lost session id: %+v", l)
			}
			if l.Allow != nil && *l.Allow {
				allowCount++
			} else {
				denyCount++
			}
			if l.Reason == "" {
				t.Fatal("evaluate line without reason")
			}
		}
	}
	if allowCount != 1 || denyCount != 1 {
		t.Fatalf("allow/deny counts = %d/%d, want 1/1", allowCount, denyCount)
	}
}

func TestAgentAuditAdminActsAppended(t *testing.T) {
	mux, sink := newAgentAuditTestServer(t)

	// grant → revoke → disable → enable: each act lands in the sink.
	code, _ := agentRequest(t, mux, http.MethodPost, "/api/agent/grants", `{"agent_id":"coder.agent-one","capability":"status.read","owner":"zmor","granted_by":"admin.zmor"}`)
	if code != http.StatusCreated {
		t.Fatalf("grant: %d", code)
	}
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/grants/revoke", `{"agent_id":"coder.agent-one","capability":"status.read","revoked_by":"admin.zmor"}`)
	if code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/capabilities/disable", `{"capability":"compute.reserve","disabled_by":"admin.zmor","reason":"freeze"}`)
	if code != http.StatusOK {
		t.Fatalf("disable: %d", code)
	}
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/capabilities/enable", `{"capability":"compute.reserve","enabled_by":"admin.zmor"}`)
	if code != http.StatusOK {
		t.Fatalf("enable: %d", code)
	}

	lines, _, err := sink.ReadLast(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 4 {
		t.Fatalf("audit lines = %d, want 4 admin acts", len(lines))
	}
	wantOrder := []agentkernel.AuditLineType{agentkernel.AuditTypeEnable, agentkernel.AuditTypeDisable, agentkernel.AuditTypeRevoke, agentkernel.AuditTypeGrant}
	for i, want := range wantOrder {
		if lines[i].Type != want {
			t.Fatalf("newest-first order violated at %d: got %s, want %s", i, lines[i].Type, want)
		}
		if lines[i].Admin == "" {
			t.Fatalf("%s line without admin: %+v", lines[i].Type, lines[i])
		}
	}
}

func TestAgentAuditReadEndpoint(t *testing.T) {
	mux, sink := newAgentAuditTestServer(t)

	// Empty sink: zero lines, no errors.
	code, body := agentRequest(t, mux, http.MethodGet, "/api/agent/audit", "")
	if code != http.StatusOK {
		t.Fatalf("audit read: %d (%v)", code, body)
	}
	if body["count"].(float64) != 0 {
		t.Fatalf("empty sink count: %v", body["count"])
	}
	if _, has := body["audit_read_errors"]; has {
		t.Fatalf("no read errors expected: %v", body["audit_read_errors"])
	}

	// Populate: 2 grants + 1 evaluate.
	agentRequest(t, mux, http.MethodPost, "/api/agent/grants", `{"agent_id":"coder.agent-one","capability":"status.read","owner":"zmor","granted_by":"a"}`)
	agentRequest(t, mux, http.MethodPost, "/api/agent/grants", `{"agent_id":"coder.agent-two","capability":"gaming.read","owner":"zmor","granted_by":"a"}`)
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.agent-one"}`)
	if code != http.StatusCreated {
		t.Fatal("create failed")
	}
	sid, _ := body["session_id"].(string)
	agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/transition", `{"session_id":"`+sid+`","to":"ACTIVE"}`)
	agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate", `{"session_id":"`+sid+`","capability":"status.read"}`)

	// Read with limit=2 → newest first, count=2.
	code, body = agentRequest(t, mux, http.MethodGet, "/api/agent/audit?limit=2", "")
	if code != http.StatusOK {
		t.Fatalf("audit read limit: %d", code)
	}
	if body["count"].(float64) != 2 {
		t.Fatalf("limit=2 count: %v", body["count"])
	}
	audit := body["audit"].([]interface{})
	first := audit[0].(map[string]interface{})
	if first["type"] != "evaluate" {
		t.Fatalf("newest first violated: %v", first["type"])
	}

	// Invalid limits → 400.
	for _, lim := range []string{"0", "-1", "abc", "999999"} {
		code, _ = agentRequest(t, mux, http.MethodGet, "/api/agent/audit?limit="+lim, "")
		if code != http.StatusBadRequest {
			t.Fatalf("limit=%s: expected 400, got %d", lim, code)
		}
	}

	// Method discipline: POST → 405.
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/audit", "")
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("POST audit: expected 405, got %d", code)
	}

	// Malformed lines are REPORTED, not hidden: tamper directly with the file.
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(sink.Path(), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("garbage\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	// Re-open a fresh sink over the same file via a fresh handler (the old
	// one is closed — the endpoint uses the handler-bound sink).
	h2 := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.4.5")
	sink2, err := agentkernel.NewAuditSink(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sink2.Close() })
	if err := h2.ConfigureAgentAudit(sink2); err != nil {
		t.Fatal(err)
	}
	mux2 := http.NewServeMux()
	h2.RegisterRoutes(mux2)
	code, body = agentRequest(t, mux2, http.MethodGet, "/api/agent/audit", "")
	if code != http.StatusOK {
		t.Fatalf("audit read after tamper: %d", code)
	}
	if errs, ok := body["audit_read_errors"].([]interface{}); !ok || len(errs) == 0 {
		t.Fatalf("malformed line must be reported in audit_read_errors, got %v", body)
	}
	if body["count"].(float64) != 3 {
		t.Fatalf("valid lines must still be returned (3), got %v", body["count"])
	}
}
