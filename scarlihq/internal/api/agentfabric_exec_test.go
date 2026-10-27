package api

import (
	"encoding/json"
	"fmt"
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

// newExecTestServer builds the BF-3 fixture: the BF-2 fabric fixture
// (kernel legacy mode + commands + workspaces) PLUS the execution store
// and the change-signal bus — the main.go BF-3 wiring contract (they ride
// the fabric opt-in, no separate flag). An optional audit sink and a
// temp-file mode manager make the audit and act assertions deterministic.
func newExecTestServer(t *testing.T) (*http.ServeMux, *Handler, *agentfabric.FabricBus) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return newExecTestServerOnDir(t, dir)
}

func newExecTestServerOnDir(t *testing.T, dir string) (*http.ServeMux, *Handler, *agentfabric.FabricBus) {
	t.Helper()
	// NewForTest points the mode bridge at a temp desired-mode file — the
	// mode.request act becomes deterministic (no /var/lib dependency).
	mode := scarlix_mode.NewForTest(filepath.Join(dir, "desired-mode"))
	h := NewHandler(guard.New(), mode, profiles.New(t.TempDir()), "test-token", "v-test")

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
	execStore, err := agentfabric.NewExecutionStore(filepath.Join(dir, "executions.json"), agentfabric.DefaultMaxExecutions)
	if err != nil {
		t.Fatalf("execution store: %v", err)
	}
	if err := execStore.Load(); err != nil {
		t.Fatalf("execution load: %v", err)
	}
	bus := agentfabric.NewFabricBus()
	if err := h.ConfigureAgentFabricExecutions(execStore, bus); err != nil {
		t.Fatalf("configure executions: %v", err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, h, bus
}

// execSubmitDecided submits an intent and returns the DECIDED command id
// (the agent holds the capability — synchronous route+decide).
func execSubmitDecided(t *testing.T, mux *http.ServeMux, agent, capability string) string {
	t.Helper()
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		fmt.Sprintf(`{"intent":"do the thing","agent_id":%q,"capability":%q}`, agent, capability))
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: %d: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "DECIDED" {
		t.Fatalf("state = %v, want DECIDED (an ACTIVE session exists)", body["state"])
	}
	id, _ := body["id"].(string)
	return id
}

func execCommand(t *testing.T, mux *http.ServeMux, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return barRequest(t, mux, http.MethodPost, "/api/bar/commands/execute", body)
}

// ---------------------------------------------------------------------------
// The execute happy path (research runtime)
// ---------------------------------------------------------------------------

func TestExecuteResearchCompleted(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "research.agent-one", agentkernel.CapabilityStatusRead)
	id := execSubmitDecided(t, mux, "research.agent-one", "status.read")

	rec, body := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, id))
	if rec.Code != http.StatusCreated {
		t.Fatalf("execute: %d: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "COMPLETED" {
		t.Fatalf("state = %v, want COMPLETED", body["state"])
	}
	if allow, _ := body["allow"].(bool); !allow {
		t.Errorf("allow = %v, want true (the fresh evaluation allowed)", body["allow"])
	}
	if body["runtime"] != "research" {
		t.Errorf("runtime = %v, want research (closed mapping)", body["runtime"])
	}
	outcome, _ := body["outcome"].(string)
	if !strings.Contains(outcome, "status snapshot") {
		t.Errorf("outcome = %q, want the status snapshot line", outcome)
	}
	if body["command_id"] != id {
		t.Errorf("command_id = %v, want %s", body["command_id"], id)
	}
	if _, ok := body["executed_at"].(string); !ok {
		t.Errorf("executed_at missing: %v", body)
	}
	execID, _ := body["id"].(string)
	if !strings.HasPrefix(execID, "afx-") {
		t.Errorf("execution id = %q, want afx- prefix", execID)
	}
}

func TestExecuteComputePlanDryRun(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "research.planner", agentkernel.CapabilityComputePlan)
	id := execSubmitDecided(t, mux, "research.planner", "compute.plan")
	rec, body := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{}}`, id))
	if rec.Code != http.StatusCreated {
		t.Fatalf("execute: %d: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "COMPLETED" {
		t.Fatalf("state = %v, want COMPLETED", body["state"])
	}
	outcome, _ := body["outcome"].(string)
	if !strings.Contains(outcome, "no allocation created") {
		t.Errorf("outcome = %q — the dry-run must state its side-effect freedom", outcome)
	}
}

// ---------------------------------------------------------------------------
// The stale-allow guard (the BF-3 heart)
// ---------------------------------------------------------------------------

func TestExecuteStaleAllowGuardSkipped(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	sink := attachAuditSink(t, h)
	sessionID := mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	id := execSubmitDecided(t, mux, "coder.agent-one", "status.read")

	// SUSPEND the session BETWEEN decide and execute — the world moved.
	suspendSession(t, h, sessionID)

	rec, body := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, id))
	if rec.Code != http.StatusCreated {
		t.Fatalf("execute: %d: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "SKIPPED" {
		t.Fatalf("state = %v, want SKIPPED (the fresh evaluation denied)", body["state"])
	}
	if allow, _ := body["allow"].(bool); allow {
		t.Errorf("allow = %v, want false (the fresh denial)", body["allow"])
	}
	reason, _ := body["reason"].(string)
	if !strings.Contains(reason, "session-state") {
		t.Errorf("reason = %q, want the fresh session-state rule", reason)
	}
	if outcome, _ := body["outcome"].(string); outcome != "" {
		t.Errorf("outcome = %q, want empty — the act never ran", outcome)
	}
	// The fresh denial is AUDITED (an evaluate line with allow=false).
	lines, readErrs, err := sink.ReadLast(10)
	if err != nil || len(readErrs) != 0 {
		t.Fatalf("audit read: %v / %v", err, readErrs)
	}
	found := false
	for _, line := range lines {
		if line.Type == agentkernel.AuditTypeEvaluate && line.SessionID == sessionID && line.Allow != nil && !*line.Allow {
			found = true
		}
	}
	if !found {
		t.Errorf("fresh-denial evaluate line missing from the audit sink: %+v", lines)
	}
}

// attachAuditSink wires a real AK-4 sink into the handler.
func attachAuditSink(t *testing.T, h *Handler) *agentkernel.AuditSink {
	t.Helper()
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
	return sink
}

// suspendSession transitions a session ACTIVE → SUSPENDED through the
// kernel store (the honest API path is the transition route; the store is
// the same authority the route uses).
func suspendSession(t *testing.T, h *Handler, sessionID string) {
	t.Helper()
	if _, ok := h.agentStore.Get(sessionID); !ok {
		t.Fatalf("session %s vanished", sessionID)
	}
	if _, err := h.agentStore.Transition(sessionID, agentkernel.SessionSuspended, time.Now().UTC()); err != nil {
		t.Fatalf("suspend: %v", err)
	}
}

// ---------------------------------------------------------------------------
// State conflicts (409s)
// ---------------------------------------------------------------------------

func TestExecuteStateConflicts(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	// SUBMITTED (no session at all — the intent is captured, awaiting routing).
	mustCreateActiveSession(t, h, "coder.held", agentkernel.CapabilityGamingRead)
	sink, err := agentkernel.NewStore(agentkernel.DefaultMaxSessions)
	if err != nil {
		t.Fatalf("kernel store: %v", err)
	}
	_ = sink
	// Submit with NO active session for a DIFFERENT agent → SUBMITTED.
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"no session yet","agent_id":"coder.ghost","capability":"gaming.read"}`)
	if rec.Code != http.StatusCreated || body["state"] != "SUBMITTED" {
		t.Fatalf("submit: %d %v", rec.Code, body["state"])
	}
	submittedID, _ := body["id"].(string)
	rec, _ = execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, submittedID))
	if rec.Code != http.StatusConflict {
		t.Errorf("execute SUBMITTED: %d, want 409 (only DECIDED commands execute)", rec.Code)
	}

	// CANCELLED (a SUBMITTED command — DECIDED is terminal and cannot be
	// cancelled; the never-lost contract keeps unrouted intents live).
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"no session yet either","agent_id":"coder.ghost2","capability":"gaming.read"}`)
	if rec.Code != http.StatusCreated || body["state"] != "SUBMITTED" {
		t.Fatalf("submit for cancel: %d %v", rec.Code, body["state"])
	}
	cancelID, _ := body["id"].(string)
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/commands/cancel", fmt.Sprintf(`{"command_id":%q}`, cancelID))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d", rec.Code)
	}
	rec, _ = execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, cancelID))
	if rec.Code != http.StatusConflict {
		t.Errorf("execute CANCELLED: %d, want 409", rec.Code)
	}

	// DENIED (DECIDED with allow=false — the capability is not granted).
	mustCreateActiveSession(t, h, "coder.bare", agentkernel.CapabilityStatusRead)
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/commands", `{"intent":"want gaming","agent_id":"coder.bare","capability":"gaming.read"}`)
	if rec.Code != http.StatusCreated || body["state"] != "DECIDED" {
		t.Fatalf("submit denied: %d %v", rec.Code, body["state"])
	}
	if allow, _ := body["allow"].(bool); allow {
		t.Fatalf("allow = true — the session lacks gaming.read")
	}
	deniedID, _ := body["id"].(string)
	rec, _ = execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, deniedID))
	if rec.Code != http.StatusConflict {
		t.Errorf("execute DENIED: %d, want 409 (a denial is not executed)", rec.Code)
	}

	// Already executed (one execution per command, ever).
	doneID := execSubmitDecided(t, mux, "coder.held", "gaming.read")
	if rec, _ = execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, doneID)); rec.Code != http.StatusCreated {
		t.Fatalf("first execute: %d", rec.Code)
	}
	rec, _ = execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, doneID))
	if rec.Code != http.StatusConflict {
		t.Errorf("re-execute: %d, want 409 (terminal)", rec.Code)
	}
}

func TestExecuteArchivedWorkspaceRefused(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	sessionID := mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)

	// Create a workspace, join the session, submit scoped (DECIDED).
	rec, wsBody := barRequest(t, mux, http.MethodPost, "/api/bar/workspaces", `{"name":"exec scope"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create ws: %d: %s", rec.Code, rec.Body.String())
	}
	wsID, _ := wsBody["id"].(string)
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/sessions", fmt.Sprintf(`{"workspace_id":%q,"session_id":%q}`, wsID, sessionID))
	if rec.Code != http.StatusOK {
		t.Fatalf("join: %d", rec.Code)
	}
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		fmt.Sprintf(`{"intent":"scoped execution","agent_id":"coder.agent-one","capability":"status.read","workspace_id":%q}`, wsID))
	if rec.Code != http.StatusCreated || body["state"] != "DECIDED" {
		t.Fatalf("scoped submit: %d %v", rec.Code, body["state"])
	}
	cmdID, _ := body["id"].(string)

	// Archive the workspace — execution closes (the tombstone contract).
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/workspaces/archive", fmt.Sprintf(`{"workspace_id":%q}`, wsID))
	if rec.Code != http.StatusOK {
		t.Fatalf("archive: %d", rec.Code)
	}
	rec, _ = execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, cmdID))
	if rec.Code != http.StatusConflict {
		t.Errorf("execute in ARCHIVED workspace: %d, want 409 (execution is closed)", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Command ID shapes (400 vs 404 — the filter precedent)
// ---------------------------------------------------------------------------

func TestExecuteCommandIDShapes(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	rec, _ := execCommand(t, mux, `{"command_id":"malformed"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed id: %d, want 400 (shape validated BEFORE the lookup)", rec.Code)
	}
	rec, _ = execCommand(t, mux, `{"command_id":"cmd-"+""}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty id: %d, want 400", rec.Code)
	}
	rec, _ = execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, "cmd-"+strings.Repeat("9", 32)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("well-formed unknown id: %d, want 404", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Params (closed shapes — 400s)
// ---------------------------------------------------------------------------

func TestExecuteParamsClosedShapes(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "coder.multi", agentkernel.CapabilityStatusRead, agentkernel.CapabilityModeRequest, agentkernel.CapabilityComputeReserve)

	// Research capability with params → 400.
	id := execSubmitDecided(t, mux, "coder.multi", "status.read")
	rec, _ := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"ai"}}`, id))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("research with params: %d, want 400", rec.Code)
	}

	// mode.request without mode → 400.
	id = execSubmitDecided(t, mux, "coder.multi", "mode.request")
	rec, _ = execCommand(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{}}`, id))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("mode.request without mode: %d, want 400", rec.Code)
	}

	// compute.reserve without allocation_id → 400.
	id = execSubmitDecided(t, mux, "coder.multi", "compute.reserve")
	rec, _ = execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, id))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("compute.reserve without allocation_id: %d, want 400", rec.Code)
	}

	// Unknown field in params → 400.
	id = execSubmitDecided(t, mux, "coder.multi", "status.read")
	rec, _ = execCommand(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"depth":2}}`, id))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown params field: %d, want 400 (DisallowUnknownFields)", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// The coding runtime: mode.request executes through the mode bridge
// ---------------------------------------------------------------------------

func TestExecuteModeRequestAct(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "coder.act", agentkernel.CapabilityModeRequest)
	id := execSubmitDecided(t, mux, "coder.act", "mode.request")

	rec, body := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"ai"}}`, id))
	if rec.Code != http.StatusCreated {
		t.Fatalf("execute: %d: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "COMPLETED" {
		t.Fatalf("state = %v, want COMPLETED: %v", body["state"], body)
	}
	if body["runtime"] != "coding" {
		t.Errorf("runtime = %v, want coding (closed mapping)", body["runtime"])
	}
	outcome, _ := body["outcome"].(string)
	if !strings.Contains(outcome, "desired-mode=ai") {
		t.Errorf("outcome = %q, want the mode-bridge acceptance line", outcome)
	}
}

func TestExecuteCodingActFailureIsHonestFailedRecord(t *testing.T) {
	// An INVALID mode: the fresh evaluation allows, the mode bridge
	// rejects — the honest terminal outcome is FAILED (201), never a 5xx.
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "coder.act2", agentkernel.CapabilityModeRequest)
	id := execSubmitDecided(t, mux, "coder.act2", "mode.request")
	rec, body := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"warp9"}}`, id))
	if rec.Code != http.StatusCreated {
		t.Fatalf("execute: %d: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "FAILED" {
		t.Fatalf("state = %v, want FAILED (act error is an honest terminal outcome)", body["state"])
	}
	if allow, _ := body["allow"].(bool); !allow {
		t.Errorf("allow = %v, want true (the fresh evaluation allowed; the ACT failed)", body["allow"])
	}
	reason, _ := body["reason"].(string)
	if !strings.Contains(reason, "invalid mode") {
		t.Errorf("reason = %q, want the act error named", reason)
	}
	if outcome, _ := body["outcome"].(string); outcome != "" {
		t.Errorf("outcome = %q, want empty — a failed act has no result", outcome)
	}
}

// ---------------------------------------------------------------------------
// The executions read surface
// ---------------------------------------------------------------------------

func TestExecutionsListFiltersAndCounts(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "research.agent-one", agentkernel.CapabilityStatusRead, agentkernel.CapabilityGamingRead)
	doneA := execSubmitDecided(t, mux, "research.agent-one", "status.read")
	doneB := execSubmitDecided(t, mux, "research.agent-one", "gaming.read")
	if rec, _ := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, doneA)); rec.Code != http.StatusCreated {
		t.Fatalf("exec A: %d", rec.Code)
	}
	if rec, _ := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, doneB)); rec.Code != http.StatusCreated {
		t.Fatalf("exec B: %d", rec.Code)
	}

	// Full list.
	rec, body := barRequest(t, mux, http.MethodGet, "/api/bar/executions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if body["total"] != float64(2) {
		t.Errorf("total = %v, want 2", body["total"])
	}
	counts, _ := body["counts"].(map[string]any)
	if counts["COMPLETED"] != float64(2) || counts["FAILED"] != float64(0) || counts["SKIPPED"] != float64(0) {
		t.Errorf("counts = %v, want COMPLETED=2 (all states always present)", counts)
	}

	// command_id filter (one execution per command).
	rec, body = barRequest(t, mux, http.MethodGet, "/api/bar/executions?command_id="+doneA, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("filter by command: %d", rec.Code)
	}
	execs, _ := body["executions"].([]any)
	if len(execs) != 1 {
		t.Fatalf("filtered executions = %d, want 1", len(execs))
	}
	first, _ := execs[0].(map[string]any)
	if first["command_id"] != doneA {
		t.Errorf("filtered command_id = %v, want %s", first["command_id"], doneA)
	}

	// Unknown state filter → 400 (closed enumeration).
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/executions?state=RUNNING", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("state=RUNNING: %d, want 400", rec.Code)
	}
	// Malformed command_id filter → 400.
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/executions?command_id=garbage", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("command_id=garbage: %d, want 400 (shape first)", rec.Code)
	}
	// Well-formed unknown command filter → 404 (not a silently-empty list).
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/executions?command_id="+"cmd-"+strings.Repeat("9", 32), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown command filter: %d, want 404", rec.Code)
	}
	// Bad limit → 400.
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/executions?limit=0", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("limit=0: %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// The composed execution field on command views (additive)
// ---------------------------------------------------------------------------

func TestComposedExecutionFieldOnCommands(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "research.agent-one", agentkernel.CapabilityStatusRead)
	id := execSubmitDecided(t, mux, "research.agent-one", "status.read")

	// Before execution: no execution field.
	_, body := barRequest(t, mux, http.MethodGet, "/api/bar/commands?limit=10", "")
	cmds, _ := body["commands"].([]any)
	found := false
	for _, raw := range cmds {
		c, _ := raw.(map[string]any)
		if c["id"] == id {
			found = true
			if _, has := c["execution"]; has {
				t.Errorf("execution field present before execution: %v", c["execution"])
			}
		}
	}
	if !found {
		t.Fatal("command missing from the list")
	}

	// Execute, then the view composes the terminal record.
	if rec, _ := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, id)); rec.Code != http.StatusCreated {
		t.Fatalf("execute: %d", rec.Code)
	}
	_, body = barRequest(t, mux, http.MethodGet, "/api/bar/commands?limit=10", "")
	cmds, _ = body["commands"].([]any)
	found = false
	for _, raw := range cmds {
		c, _ := raw.(map[string]any)
		if c["id"] == id {
			found = true
			exec, has := c["execution"].(map[string]any)
			if !has {
				t.Fatalf("execution field missing after execution: %v", c)
			}
			if exec["state"] != "COMPLETED" || exec["runtime"] != "research" {
				t.Errorf("composed execution = %v, want COMPLETED/research", exec)
			}
		}
	}
	if !found {
		t.Fatal("command missing from the list after execution")
	}
}

// ---------------------------------------------------------------------------
// 503s (unconfigured surfaces) + restart persistence
// ---------------------------------------------------------------------------

func TestExecutionsDisabledWithoutStore(t *testing.T) {
	// The BF-2-era wiring: fabric + workspaces configured, executions NOT
	// (a partial upgrade / test fixture) — execute and executions answer
	// 503, the command surface still works.
	mux, h := newFabricTestServer(t)
	mustCreateActiveSession(t, h, "coder.agent-one", agentkernel.CapabilityStatusRead)
	id := execSubmitDecided(t, mux, "coder.agent-one", "status.read")
	rec, _ := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, id))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("execute without execution store: %d, want 503", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/executions", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("executions without execution store: %d, want 503", rec.Code)
	}
}

func TestExecutionsRestartPersistence(t *testing.T) {
	// One server over an explicit dir, execute, RESTART over the same dir
	// — the history, the composed views and the one-per-command invariant
	// survive (the file is the authority).
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	mux, h, _ := newExecTestServerOnDir(t, dir)
	sessionID := mustCreateActiveSession(t, h, "research.agent-one", agentkernel.CapabilityStatusRead)
	id := execSubmitDecided(t, mux, "research.agent-one", "status.read")
	if rec, _ := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, id)); rec.Code != http.StatusCreated {
		t.Fatalf("execute: %d", rec.Code)
	}

	// RESTART: a fresh handler over the same files (sessions are in-memory
	// and die with the process — the honest contract; the FILES survive).
	mux2, h2, _ := newExecTestServerOnDir(t, dir)
	_ = sessionID
	rec, body := barRequest(t, mux2, http.MethodGet, "/api/bar/executions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list after restart: %d", rec.Code)
	}
	if body["total"] != float64(1) {
		t.Fatalf("total after restart = %v, want 1 (the file is the history)", body["total"])
	}
	// The composed view survives.
	_, body = barRequest(t, mux2, http.MethodGet, "/api/bar/commands?limit=10", "")
	cmds, _ := body["commands"].([]any)
	for _, raw := range cmds {
		c, _ := raw.(map[string]any)
		if c["id"] == id {
			if _, has := c["execution"].(map[string]any); !has {
				t.Errorf("composed execution lost after restart: %v", c)
			}
		}
	}
	// The command is still DECIDED and NOT re-executable (one execution per
	// command survives the restart — the store refuses the duplicate).
	_ = h2
	rec, _ = execCommand(t, mux2, fmt.Sprintf(`{"command_id":%q}`, id))
	if rec.Code != http.StatusConflict {
		t.Errorf("re-execute after restart: %d, want 409 (the invariant is load-enforced)", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// The fabric bus signals on command mutations + execution
// ---------------------------------------------------------------------------

func TestFabricBusSignalsOnMutations(t *testing.T) {
	mux, h, bus := newExecTestServer(t)
	_, ch, ok := bus.Subscribe()
	if !ok {
		t.Fatal("Subscribe failed")
	}
	mustCreateActiveSession(t, h, "research.agent-one", agentkernel.CapabilityStatusRead)
	id := execSubmitDecided(t, mux, "research.agent-one", "status.read")

	collect := func(n int, d time.Duration) []agentfabric.Signal {
		got := make([]agentfabric.Signal, 0, n)
		deadline := time.After(d)
		for len(got) < n {
			select {
			case sig := <-ch:
				got = append(got, sig)
			case <-deadline:
				return got
			}
		}
		return got
	}
	// submit → command.submitted, command.routed, command.decided.
	sigs := collect(3, 2*time.Second)
	seen := map[agentfabric.SignalType]bool{}
	for _, s := range sigs {
		seen[s.Type] = true
		if s.Ref != id {
			t.Errorf("signal ref = %q, want the command id %s", s.Ref, id)
		}
	}
	for _, want := range []agentfabric.SignalType{agentfabric.SignalCommandSubmitted, agentfabric.SignalCommandRouted, agentfabric.SignalCommandDecided} {
		if !seen[want] {
			t.Errorf("missing %s signal (got %v)", want, sigs)
		}
	}
	// execute → execution.recorded (ref = the execution id).
	if rec, _ := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, id)); rec.Code != http.StatusCreated {
		t.Fatalf("execute: %d: %s", rec.Code, rec.Body.String())
	}
	sigs = collect(1, 2*time.Second)
	if len(sigs) != 1 || sigs[0].Type != agentfabric.SignalExecutionRecorded {
		t.Fatalf("execution.recorded signal missing: %v", sigs)
	}
	if !strings.HasPrefix(sigs[0].Ref, "afx-") {
		t.Errorf("execution.recorded ref = %q, want the afx- execution id", sigs[0].Ref)
	}
}

// ---------------------------------------------------------------------------
// ConfigureAgentFabricExecutions argument validation
// ---------------------------------------------------------------------------

func TestConfigureAgentFabricExecutionsValidation(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	_ = mux
	bus := agentfabric.NewFabricBus()
	if err := h.ConfigureAgentFabricExecutions(nil, bus); err == nil {
		t.Error("nil store accepted")
	}
	if err := h.ConfigureAgentFabricExecutions(h.agentExecutions, nil); err == nil {
		t.Error("nil bus accepted")
	}
}

// TestExecuteJSONBodyShapes pins the request/response JSON contracts.
func TestExecuteJSONBodyShapes(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "research.agent-one", agentkernel.CapabilityStatusRead)
	id := execSubmitDecided(t, mux, "research.agent-one", "status.read")
	rec, _ := execCommand(t, mux, fmt.Sprintf(`{"command_id":%q}`, id))
	if rec.Code != http.StatusCreated {
		t.Fatalf("execute: %d: %s", rec.Code, rec.Body.String())
	}
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("non-JSON response: %v", err)
	}
	for _, key := range []string{"id", "command_id", "agent_id", "capability", "session_id", "runtime", "state", "allow", "intent", "executed_at"} {
		if _, ok := view[key]; !ok {
			t.Errorf("execution view misses key %q: %v", key, view)
		}
	}
}
