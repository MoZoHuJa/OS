package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

// BF-2: the workspace REST surface — create/list/archive/join/leave plus
// workspace-scoped command submission and filtering. Every route is
// auth-gated, 503 without the fabric, and honors the isolation contract
// (scoped commands route ONLY through member sessions).

func TestBarWorkspacesAuthGated(t *testing.T) {
	mux, _ := newFabricTestServer(t)
	for _, path := range []string{
		"/api/bar/workspaces",
		"/api/bar/workspaces/archive",
		"/api/bar/workspaces/sessions",
		"/api/bar/workspaces/sessions/leave",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("no token on %s: got %d, want 401", path, rec.Code)
		}
	}
}

func TestBarWorkspacesDisabledByDefault(t *testing.T) {
	// A server WITHOUT ConfigureAgentWorkspaces answers 503 on every
	// workspace route (the opt-in wiring contract).
	mux, h := newFabricTestServer(t)
	h.agentWorkspaces = nil
	rec, _ := barRequest(t, mux, http.MethodGet, "/api/bar/workspaces", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("workspaces without the store: got %d, want 503", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces", `{"name":"x"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("create without the store: got %d, want 503", rec.Code)
	}
}

func mustCreateWorkspace(t *testing.T, mux *http.ServeMux, name string) map[string]any {
	t.Helper()
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/workspaces", `{"name":"`+name+`","description":"`+name+` context"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create workspace %q: got %d (%v), want 201", name, rec.Code, body)
	}
	return body
}

func TestBarWorkspaceCreateValidation(t *testing.T) {
	mux, _ := newFabricTestServer(t)
	cases := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"ok", `{"name":"relwork"}`, http.StatusCreated},
		{"empty name", `{"name":""}`, http.StatusBadRequest},
		{"whitespace name", `{"name":"   "}`, http.StatusBadRequest},
		{"name too long", `{"name":"` + mustRepeat('x', 65) + `"}`, http.StatusBadRequest},
		{"control char in name", "{\"name\":\"line\\nbreak\"}", http.StatusBadRequest},
		{"description too long", `{"name":"ok","description":"` + mustRepeat('d', 257) + `"}`, http.StatusBadRequest},
		{"unknown field", `{"name":"ok","bogus":1}`, http.StatusBadRequest},
		{"malformed json", `{`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec, _ := barRequest(t, mux, http.MethodPost, "/api/bar/workspaces", tc.body)
		if rec.Code != tc.wantCode {
			t.Errorf("%s: got %d, want %d", tc.name, rec.Code, tc.wantCode)
		}
	}
}

func mustRepeat(r rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}

func TestBarWorkspaceListAndLifecycle(t *testing.T) {
	mux, h := newFabricTestServer(t)

	a := mustCreateWorkspace(t, mux, "alpha")
	b := mustCreateWorkspace(t, mux, "beta")

	rec, body := barRequest(t, mux, http.MethodGet, "/api/bar/workspaces", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200", rec.Code)
	}
	list := body["workspaces"].([]any)
	if len(list) != 2 {
		t.Fatalf("list length = %d, want 2", len(list))
	}
	// Newest-first.
	if list[0].(map[string]any)["name"] != "beta" || list[1].(map[string]any)["name"] != "alpha" {
		t.Errorf("list order = [%v, %v], want newest-first [beta, alpha]",
			list[0].(map[string]any)["name"], list[1].(map[string]any)["name"])
	}
	wsA := a["id"].(string)

	// Join: session must exist in the kernel.
	sid := mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions",
		`{"workspace_id":"`+wsA+`","session_id":"`+sid+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("join: got %d (%v), want 200", rec.Code, body)
	}
	if int(body["session_count"].(float64)) != 1 {
		t.Errorf("session_count after join = %v, want 1", body["session_count"])
	}

	// Duplicate join → 409.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions",
		`{"workspace_id":"`+wsA+`","session_id":"`+sid+`"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate join: got %d, want 409", rec.Code)
	}

	// Claim from another workspace → 409 (one workspace per session).
	wsB := b["id"].(string)
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions",
		`{"workspace_id":"`+wsB+`","session_id":"`+sid+`"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("cross-claim join: got %d, want 409", rec.Code)
	}

	// Unknown session → 400 (existence is the kernel's business).
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions",
		`{"workspace_id":"`+wsA+`","session_id":"aks-00000000000000000000000000000000"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("join with an unknown session: got %d, want 400", rec.Code)
	}

	// Unknown workspace → 404.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions",
		`{"workspace_id":"ws-00000000000000000000000000000000","session_id":"`+sid+`"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("join into an unknown workspace: got %d, want 404", rec.Code)
	}

	// Leave → session free again.
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions/leave",
		`{"workspace_id":"`+wsA+`","session_id":"`+sid+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("leave: got %d, want 200", rec.Code)
	}
	if int(body["session_count"].(float64)) != 0 {
		t.Errorf("session_count after leave = %v, want 0", body["session_count"])
	}
	// Leave of a non-member → 409.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions/leave",
		`{"workspace_id":"`+wsA+`","session_id":"`+sid+`"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("leave of a non-member: got %d, want 409", rec.Code)
	}

	// Archive → terminal; second archive → 409; join into archived → 409.
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/archive", `{"workspace_id":"`+wsA+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive: got %d, want 200", rec.Code)
	}
	if body["state"] != "ARCHIVED" {
		t.Errorf("archived state = %v, want ARCHIVED", body["state"])
	}
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/archive", `{"workspace_id":"`+wsA+`"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("double archive: got %d, want 409", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions",
		`{"workspace_id":"`+wsA+`","session_id":"`+sid+`"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("join into archived: got %d, want 409", rec.Code)
	}

	// Unknown workspace on archive → 404.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/archive", `{"workspace_id":"ws-00000000000000000000000000000000"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("archive unknown: got %d, want 404", rec.Code)
	}

	// Method discipline.
	rec, _ = barRequest(t, mux, http.MethodDelete, "/api/bar/workspaces", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /api/bar/workspaces: got %d, want 405", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/workspaces/archive", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET archive: got %d, want 405", rec.Code)
	}
}

// nil2h removed: sessions must live in the SAME handler the routes use.

func TestBarWorkspaceScopedSubmissionAndRouting(t *testing.T) {
	mux, h := newFabricTestServer(t)

	// A member session of coder.agent-one (created FIRST — older) and an
	// outsider ACTIVE session (created SECOND — NEWEST). Scoped routing
	// must pick the MEMBER despite the newer outsider; unscoped routing
	// must pick the NEWEST (the outsider) — both contracts in one fixture.
	member := mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	outsider := mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)

	ws := mustCreateWorkspace(t, mux, "relwork")
	wsID := ws["id"].(string)
	if _, body := barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions",
		`{"workspace_id":"`+wsID+`","session_id":"`+member+`"}`); body["id"] == nil {
		t.Fatalf("join failed")
	}

	// Scoped submission with no member session would stay SUBMITTED; with
	// the member ACTIVE it routes + decides synchronously THROUGH THE MEMBER.
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		`{"intent":"check the inventory","agent_id":"coder.agent-one","capability":"status.read","workspace_id":"`+wsID+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("scoped submit: got %d (%v), want 201", rec.Code, body)
	}
	if body["workspace_id"] != wsID {
		t.Errorf("scoped command carries workspace_id %v, want %q", body["workspace_id"], wsID)
	}
	if body["session_id"] != member {
		t.Errorf("scoped command routed through %v, want the MEMBER session %s (the newer outsider %s must be ignored)",
			body["session_id"], member, outsider)
	}
	if body["state"] != "DECIDED" {
		t.Fatalf("scoped command state = %v, want DECIDED", body["state"])
	}

	// Unscoped submission still routes through the NEWEST ACTIVE session
	// of the agent (the BF-1 behavior is unchanged).
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		`{"intent":"unscoped check","agent_id":"coder.agent-one","capability":"status.read"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("unscoped submit: got %d, want 201", rec.Code)
	}
	if body["session_id"] != outsider {
		t.Errorf("unscoped command routed through %v, want the newest ACTIVE %s (BF-1 behavior)", body["session_id"], outsider)
	}
	if body["workspace_id"] != nil && body["workspace_id"] != "" {
		t.Errorf("unscoped command carries workspace_id %v, want none", body["workspace_id"])
	}

	// Named session that is NOT a member of the scope → 400, intent captured
	// SUBMITTED (never lost).
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		`{"intent":"wrong member","agent_id":"coder.agent-one","capability":"status.read","workspace_id":"`+wsID+`","session_id":"`+outsider+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("named non-member submit: got %d (%v), want 400", rec.Code, body)
	}
	// The intent was NOT captured for a malformed request (the contract:
	// bad scope/named-session is a client error, not a routability gap).
	if body["id"] != nil {
		t.Errorf("malformed scoped request captured a command (%v) — the never-lost contract covers routability, not malformed requests", body["id"])
	}

	// Scope filter on the read surface.
	rec, body = barRequest(t, mux, http.MethodGet, "/api/bar/commands?workspace_id="+wsID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered list: got %d, want 200", rec.Code)
	}
	if int(body["total"].(float64)) != 1 {
		t.Errorf("scoped total = %v, want 1", body["total"])
	}
	if body["workspace_id"] != nil {
		// (not asserted on the response shape — sanity only)
	}

	// Unknown workspace filter → 404 (a typo is a client error).
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/commands?workspace_id=ws-00000000000000000000000000000000", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown workspace filter: got %d, want 404", rec.Code)
	}

	// Submit into an ARCHIVED workspace → 409; the live command of an
	// archived workspace stays route-refused (409) but cancellable.
	mustArchive := func() {
		rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/archive", `{"workspace_id":"`+wsID+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("archive: got %d (%v)", rec.Code, body)
		}
	}
	// Capture a scoped command with no member session first (leave + deactivate
	// is not needed: create a scope with no ACTIVE member).
	// Simpler: submit into ws with only DORMANT members is impossible to set up
	// here — instead verify the archived-workspace submit refusal directly.
	mustArchive()
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		`{"intent":"into the tomb","agent_id":"coder.agent-one","capability":"status.read","workspace_id":"`+wsID+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("submit into archived workspace: got %d (%v), want 409", rec.Code, body)
	}
}

func TestBarWorkspaceScopedRouteEndpoint(t *testing.T) {
	mux, h := newFabricTestServer(t)

	// A scoped command captured with NO member session: SUBMITTED, then
	// route refuses while the scope has no ACTIVE member; join + activate
	// makes it routable; the routed decision lands on the member.
	ws := mustCreateWorkspace(t, mux, "late")
	wsID := ws["id"].(string)

	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		`{"intent":"waiting for a member","agent_id":"coder.agent-one","capability":"status.read","workspace_id":"`+wsID+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("scoped submit without a member: got %d, want 201", rec.Code)
	}
	cmdID := body["id"].(string)
	if body["state"] != "SUBMITTED" {
		t.Fatalf("state = %v, want SUBMITTED (no member session — the intent is captured, never lost)", body["state"])
	}

	// The agent HAS an ACTIVE unscoped session — routing must still refuse:
	// the scope has no members.
	outsider := mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/commands/route", `{"command_id":"`+cmdID+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("route with an unscoped-only session: got %d, want 409 (scoped commands route ONLY through members)", rec.Code)
	}
	_ = outsider

	// Join a member → route succeeds and binds the member.
	member := mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	if rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions",
		`{"workspace_id":"`+wsID+`","session_id":"`+member+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("join: got %d (%v)", rec.Code, body)
	}
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/commands/route", `{"command_id":"`+cmdID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("route after join: got %d (%v), want 200", rec.Code, body)
	}
	if body["session_id"] != member {
		t.Errorf("routed through %v, want the member %s", body["session_id"], member)
	}
	if body["state"] != "DECIDED" {
		t.Errorf("state after route = %v, want DECIDED", body["state"])
	}

	// An archived workspace closes routing for its LIVE commands.
	ws2 := mustCreateWorkspace(t, mux, "tomb")
	ws2ID := ws2["id"].(string)
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		`{"intent":"never routed","agent_id":"coder.agent-one","capability":"status.read","workspace_id":"`+ws2ID+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit into ws2: got %d, want 201", rec.Code)
	}
	cmd2 := body["id"].(string)
	if rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/archive", `{"workspace_id":"`+ws2ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("archive ws2: got %d (%v)", rec.Code, body)
	}
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/commands/route", `{"command_id":"`+cmd2+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("route inside an archived workspace: got %d, want 409", rec.Code)
	}
	// Cancellation of that live command still works (a user act on visible
	// state, not a routing event).
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/commands/cancel", `{"command_id":"`+cmd2+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel inside an archived workspace: got %d (%v), want 200 — cancellation is a user act, not routing", rec.Code, body)
	}
	if body["state"] != "CANCELLED" {
		t.Errorf("state after cancel = %v, want CANCELLED", body["state"])
	}
}

func TestBarWorkspacePersistenceAcrossReload(t *testing.T) {
	// Both stores live in one shared temp dir; a SECOND handler built on
	// the same paths is the main.go restart story.
	dir := t.TempDir()
	if err := osChmod700(dir); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	mux, h := newFabricTestServerOnDir(t, dir)
	sid := mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	ws := mustCreateWorkspace(t, mux, "durable")
	wsID := ws["id"].(string)
	if rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions",
		`{"workspace_id":"`+wsID+`","session_id":"`+sid+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("join: got %d (%v)", rec.Code, body)
	}
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		`{"intent":"persisted scoped intent","agent_id":"coder.agent-one","capability":"status.read","workspace_id":"`+wsID+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("scoped submit: got %d, want 201", rec.Code)
	}

	// Fresh handler, same files (the restart).
	mux2, h2 := newFabricTestServerOnDir(t, dir)
	_ = h2
	rec, body = barRequest(t, mux2, http.MethodGet, "/api/bar/workspaces", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list after reload: got %d, want 200", rec.Code)
	}
	list := body["workspaces"].([]any)
	if len(list) != 1 {
		t.Fatalf("workspaces after reload = %d, want 1", len(list))
	}
	reloaded := list[0].(map[string]any)
	if reloaded["name"] != "durable" {
		t.Errorf("reloaded name = %v, want durable", reloaded["name"])
	}
	if int(reloaded["session_count"].(float64)) != 1 {
		t.Errorf("reloaded session_count = %v, want 1 (membership is persisted state)", reloaded["session_count"])
	}
	if int(reloaded["commands_count"].(float64)) != 1 {
		t.Errorf("reloaded commands_count = %v, want 1 (the read surface composes the two stores)", reloaded["commands_count"])
	}

	// The scoped command survives with its scope.
	rec, body = barRequest(t, mux2, http.MethodGet, "/api/bar/commands?workspace_id="+wsID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered list after reload: got %d, want 200", rec.Code)
	}
	if int(body["total"].(float64)) != 1 {
		t.Errorf("scoped total after reload = %v, want 1", body["total"])
	}
}

// osChmod700 is a tiny indirection so the reload test reads cleanly.
func osChmod700(dir string) error {
	return os.Chmod(dir, 0o700)
}
