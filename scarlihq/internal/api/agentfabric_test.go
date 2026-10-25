package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentfabric"
	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
)

// newFabricTestServer builds a mux with ALL routes registered and the
// Agent Kernel (legacy mode) + Agent Fabric (commands + workspaces)
// configured — the BF-2 fixture (the established agent-kernel test-server
// pattern) on its own fresh temp dir.
func newFabricTestServer(t *testing.T) (*http.ServeMux, *Handler) {
	t.Helper()
	dir := t.TempDir()
	return newFabricTestServerOnDir(t, dir)
}

// newFabricTestServerOnDir is the same fixture over an EXPLICIT directory
// — the reload/restart tests point a second handler at the same files.
func newFabricTestServerOnDir(t *testing.T, dir string) (*http.ServeMux, *Handler) {
	t.Helper()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "test-token", "v-test")

	kernelStore, err := agentkernel.NewStore(agentkernel.DefaultMaxSessions)
	if err != nil {
		t.Fatalf("kernel store: %v", err)
	}
	if err := h.ConfigureAgentKernel(kernelStore, agentkernel.Policy{}); err != nil {
		t.Fatalf("configure kernel: %v", err)
	}

	fabricStore, err := agentfabric.NewCommandStore(filepath.Join(dir, "commands.json"), agentfabric.DefaultMaxCommands)
	if err != nil {
		t.Fatalf("fabric store: %v", err)
	}
	if err := fabricStore.Load(); err != nil {
		t.Fatalf("fabric load: %v", err)
	}
	if err := h.ConfigureAgentFabric(fabricStore); err != nil {
		t.Fatalf("configure fabric: %v", err)
	}
	// BF-2: the workspace store rides the fabric wiring (the main.go
	// contract — no separate opt-in; the two stores arrive together).
	wsStore, err := agentfabric.NewWorkspaceStore(filepath.Join(dir, "workspaces.json"), agentfabric.DefaultMaxWorkspaces)
	if err != nil {
		t.Fatalf("workspace store: %v", err)
	}
	if err := wsStore.Load(); err != nil {
		t.Fatalf("workspace load: %v", err)
	}
	if err := h.ConfigureAgentWorkspaces(wsStore); err != nil {
		t.Fatalf("configure workspaces: %v", err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, h
}

// mustCreateActiveSession creates an ACTIVE kernel session for the agent
// with the given capabilities (legacy mode: request-supplied grants).
func mustCreateActiveSession(t *testing.T, h *Handler, agent string, caps ...agentkernel.Capability) string {
	t.Helper()
	set, err := agentkernel.NewCapabilitySet(caps...)
	if err != nil {
		t.Fatalf("caps: %v", err)
	}
	identity, err := agentkernel.NewAgentIdentity(agentkernel.AgentID(agent), "zmor", time.Now().UTC())
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	rec, err := h.agentStore.Create(identity, set, time.Now().UTC())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	rec, err = h.agentStore.Transition(rec.Session.ID, agentkernel.SessionActive, time.Now().UTC())
	if err != nil {
		t.Fatalf("activate session: %v", err)
	}
	return rec.Session.ID
}

// barRequest performs one authed request against the test mux and decodes
// the JSON body (nil for empty responses).
func barRequest(t *testing.T, mux *http.ServeMux, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer test-token")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("non-JSON response (%d): %s", rec.Code, rec.Body.String())
		}
	}
	return rec, out
}

func TestBarCommandsAuthGated(t *testing.T) {
	mux, _ := newFabricTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/bar/commands", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/bar/commands", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", rec.Code)
	}
}

func TestBarCommandsDisabledByDefault(t *testing.T) {
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "test-token", "v-test")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec, _ := barRequest(t, mux, http.MethodGet, "/api/bar/commands", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("fabric not configured: got %d, want 503", rec.Code)
	}
}

func TestBarCommandsRequiresKernel(t *testing.T) {
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "test-token", "v-test")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	store, err := agentfabric.NewCommandStore(filepath.Join(dir, "commands.json"), 16)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := h.ConfigureAgentFabric(store); err != nil {
		t.Fatalf("configure: %v", err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec, _ := barRequest(t, mux, http.MethodGet, "/api/bar/commands", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("fabric without kernel: got %d, want 503 (the fabric routes through the kernel)", rec.Code)
	}
}

func TestBarSubmitValidationMatrix(t *testing.T) {
	mux, _ := newFabricTestServer(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"empty intent", `{"intent":"","agent_id":"coder.agent-one","capability":"status.read"}`, http.StatusBadRequest},
		{"control char intent", `{"intent":"bad\nintent","agent_id":"coder.agent-one","capability":"status.read"}`, http.StatusBadRequest},
		{"bad agent", `{"intent":"i","agent_id":"nope","capability":"status.read"}`, http.StatusBadRequest},
		{"unknown capability", `{"intent":"i","agent_id":"coder.agent-one","capability":"shell.exec"}`, http.StatusBadRequest},
		{"unknown field", `{"intent":"i","agent_id":"coder.agent-one","capability":"status.read","evil":true}`, http.StatusBadRequest},
		{"malformed json", `{not json`, http.StatusBadRequest},
		{"unknown named session", `{"intent":"i","agent_id":"coder.agent-one","capability":"status.read","session_id":"aks-nope"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := barRequest(t, mux, http.MethodPost, "/api/bar/commands", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("got %d, want %d (body: %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestBarSubmitCapturedWithoutSession(t *testing.T) {
	mux, _ := newFabricTestServer(t)
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"check status","agent_id":"coder.agent-one","capability":"status.read"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (intent captured even without a session — never lost): %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "SUBMITTED" {
		t.Errorf("state = %v, want SUBMITTED (no ACTIVE session)", body["state"])
	}
	if _, has := body["allow"]; has {
		t.Error("SUBMITTED view must not carry a decision")
	}
}

func TestBarSubmitFullFlowDecided(t *testing.T) {
	mux, h := newFabricTestServer(t)
	sessionID := mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)

	// 1. Submit with an ACTIVE session → synchronous route+evaluate+decide.
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"read the status","agent_id":"coder.agent-one","capability":"status.read"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: got %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "DECIDED" {
		t.Fatalf("state = %v, want DECIDED (ACTIVE session resolved synchronously)", body["state"])
	}
	allow, _ := body["allow"].(bool)
	if !allow {
		t.Errorf("allow = %v, want true (session holds status.read)", body["allow"])
	}
	if body["session_id"] != sessionID {
		t.Errorf("session_id = %v, want %s", body["session_id"], sessionID)
	}

	// 2. The decision is visible in the list (newest-first).
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/commands?limit=5", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var list struct {
		Commands []map[string]any `json:"commands"`
		Count    int              `json:"count"`
		Counts   map[string]int   `json:"counts"`
		Total    int              `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("list json: %v", err)
	}
	if list.Count != 1 || list.Total != 1 {
		t.Fatalf("count/total = %d/%d, want 1/1", list.Count, list.Total)
	}
	if list.Counts["DECIDED"] != 1 {
		t.Errorf("counts[DECIDED] = %d, want 1", list.Counts["DECIDED"])
	}
	if len(list.Commands) != 1 || list.Commands[0]["state"] != "DECIDED" {
		t.Fatalf("list content = %+v", list.Commands)
	}
}

func TestBarDenialIsADecision(t *testing.T) {
	mux, h := newFabricTestServer(t)
	// Session WITHOUT status.read → the kernel denies; the denial is a
	// successful, auditable evaluation recorded on the command (denial-is-200).
	mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityGamingRead)
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"read the status","agent_id":"coder.agent-one","capability":"status.read"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: got %d, want 201 (a denial is a decision, not an HTTP error): %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "DECIDED" {
		t.Fatalf("state = %v, want DECIDED", body["state"])
	}
	if allow, _ := body["allow"].(bool); allow {
		t.Error("allow = true, want false (capability not granted)")
	}
	if reason, _ := body["reason"].(string); !strings.Contains(reason, "rule/capability-not-granted") {
		t.Errorf("reason = %q, want the fired rule", reason)
	}
}

func TestBarRouteEndpoint(t *testing.T) {
	mux, h := newFabricTestServer(t)
	// 1. Submit without a session → SUBMITTED.
	rec, cmd := barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"check status","agent_id":"coder.agent-one","capability":"status.read"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: %d", rec.Code)
	}
	id, _ := cmd["id"].(string)

	// 2. Route without any ACTIVE session → 409 (retryable client state).
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/commands/route", `{"command_id":"`+id+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("route without session: got %d, want 409: %s", rec.Code, rec.Body.String())
	}

	// 3. Activate a session → route succeeds → DECIDED.
	mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands/route", `{"command_id":"`+id+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("route: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "DECIDED" {
		t.Fatalf("state = %v, want DECIDED", body["state"])
	}

	// 4. Re-route a DECIDED command → 409 (terminal).
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/commands/route", `{"command_id":"`+id+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-route DECIDED: got %d, want 409", rec.Code)
	}

	// 5. Unknown command → 404.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/commands/route", `{"command_id":"cmd-00000000000000000000000000000000"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("route unknown: got %d, want 404", rec.Code)
	}
}

func TestBarNamedSessionRouting(t *testing.T) {
	mux, h := newFabricTestServer(t)
	older := mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	time.Sleep(10 * time.Millisecond) // distinct CreatedAt
	newer := mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)

	// No named session → the NEWEST ACTIVE session is chosen deterministically.
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"i","agent_id":"coder.agent-one","capability":"status.read"}`)
	if body["session_id"] != newer {
		t.Errorf("auto-resolved session = %v, want the newest (%s, not %s)", body["session_id"], newer, older)
	}

	// Named session wins.
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"i2","agent_id":"coder.agent-one","capability":"status.read","session_id":"`+older+`"}`)
	if rec.Code != http.StatusCreated || body["session_id"] != older {
		t.Fatalf("named session: %d %v", rec.Code, body["session_id"])
	}

	// Named session of ANOTHER agent → 400, intent stays captured SUBMITTED.
	other := mustCreateActiveSession(t, h, "research.agent-two", agentkernel.CapabilityGamingRead)
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"i3","agent_id":"coder.agent-one","capability":"status.read","session_id":"`+other+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("foreign session: got %d, want 400: %s", rec.Code, rec.Body.String())
	}
	// The captured intent is recoverable from the list (never lost).
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/commands?limit=10", "")
	var list struct {
		Commands []map[string]any `json:"commands"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, c := range list.Commands {
		if c["intent"] == "i3" && c["state"] == "SUBMITTED" {
			found = true
		}
	}
	if !found {
		t.Error("intent i3 lost after foreign-session rejection — must stay SUBMITTED")
	}
}

func TestBarCancelEndpoint(t *testing.T) {
	mux, _ := newFabricTestServer(t)
	rec, cmd := barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"cancel me","agent_id":"coder.agent-one","capability":"status.read"}`)
	id, _ := cmd["id"].(string)

	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands/cancel", `{"command_id":"`+id+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: got %d: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "CANCELLED" {
		t.Fatalf("state = %v, want CANCELLED", body["state"])
	}

	// Double cancel → 409 (terminal).
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/commands/cancel", `{"command_id":"`+id+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("double cancel: got %d, want 409", rec.Code)
	}

	// Unknown → 404.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/commands/cancel", `{"command_id":"cmd-00000000000000000000000000000000"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cancel unknown: got %d, want 404", rec.Code)
	}
}

func TestBarListLimitValidation(t *testing.T) {
	mux, _ := newFabricTestServer(t)
	for _, limit := range []string{"0", "-1", "201", "abc"} {
		rec, _ := barRequest(t, mux, http.MethodGet, "/api/bar/commands?limit="+limit, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%q: got %d, want 400", limit, rec.Code)
		}
	}
	rec, _ := barRequest(t, mux, http.MethodGet, "/api/bar/commands?limit=1", "")
	if rec.Code != http.StatusOK {
		t.Errorf("limit=1: got %d, want 200", rec.Code)
	}
}

func TestBarMethodDiscipline(t *testing.T) {
	mux, _ := newFabricTestServer(t)
	rec, _ := barRequest(t, mux, http.MethodDelete, "/api/bar/commands", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE commands: got %d, want 405", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/commands/route", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET route: got %d, want 405", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/commands/cancel", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET cancel: got %d, want 405", rec.Code)
	}
}

func TestBarDecisionsPersistedToAuditBeforeServe(t *testing.T) {
	mux, h := newFabricTestServer(t)
	// Attach a real audit sink (AK-4 discipline — fail-closed on write).
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	sink, err := agentkernel.NewAuditSink(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("sink: %v", err)
	}
	if err := h.ConfigureAgentAudit(sink); err != nil {
		t.Fatalf("configure audit: %v", err)
	}

	mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"audited intent","agent_id":"coder.agent-one","capability":"status.read"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: %d: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "DECIDED" {
		t.Fatalf("state = %v, want DECIDED", body["state"])
	}

	// The decision was persisted BEFORE it was served: the sink holds the
	// evaluate line (allow) with the session id the command routed through.
	lines, readErrs, err := sink.ReadLast(10)
	if err != nil {
		t.Fatalf("audit read: %v", err)
	}
	if len(readErrs) != 0 {
		t.Fatalf("audit read errors: %v", readErrs)
	}
	found := false
	for _, line := range lines {
		if line.Type == agentkernel.AuditTypeEvaluate && line.SessionID == body["session_id"] && line.Allow != nil && *line.Allow {
			found = true
		}
	}
	if !found {
		t.Errorf("evaluate line for the fabric decision missing from the audit sink (decision-persisted-before-serve): %+v", lines)
	}
}

func TestBarOversizedBodyRejected(t *testing.T) {
	mux, _ := newFabricTestServer(t)
	big := strings.Repeat("a", 8192) // decodeOneJSON caps bodies at 4096
	body := `{"intent":"` + big + `","agent_id":"coder.agent-one","capability":"status.read"}`
	rec, _ := barRequest(t, mux, http.MethodPost, "/api/bar/commands", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body: got %d, want 400 (bounded decode)", rec.Code)
	}
}

func TestBarPersistenceAcrossHandlerReload(t *testing.T) {
	mux, h := newFabricTestServer(t)
	mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	rec, cmd := barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"survive the restart","agent_id":"coder.agent-one","capability":"status.read"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: %d", rec.Code)
	}
	id, _ := cmd["id"].(string)

	// A NEW store instance over the SAME file sees the command (sessions
	// are in-memory kernel state — the command file is the durable history).
	reloaded, err := agentfabric.NewCommandStore(h.agentFabric.Path(), agentfabric.DefaultMaxCommands)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if err := reloaded.Load(); err != nil {
		t.Fatalf("reload load: %v", err)
	}
	got, ok := reloaded.Get(agentfabric.CommandID(id))
	if !ok {
		t.Fatal("command lost across reload")
	}
	if got.State != agentfabric.CommandDecided || got.Decision == nil || !got.Decision.Allow {
		t.Fatalf("reloaded = %+v, want DECIDED allow", got)
	}
}
