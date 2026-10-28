package api

import (
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
)

// BF-4 (v19.5.3): the operator-approval surface test matrix — the 202
// request flow (params captured, submitter labeled), the approve flow
// (full BF-3 composition + the operator's yes), the unconditional reject,
// withdrawal on cancel, the one-request-per-command invariant, honest
// 409/400/404 discipline, list+counts+filters, restart persistence, the
// bus signals and the reconciliation path.

// apprRequest posts a coding execute (the approval-request branch).
func apprRequest(t *testing.T, mux *http.ServeMux, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return barRequest(t, mux, http.MethodPost, "/api/bar/commands/execute", body)
}

func apprReject(t *testing.T, mux *http.ServeMux, approvalID, approver, reason string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return barRequest(t, mux, http.MethodPost, "/api/bar/approvals/reject",
		fmt.Sprintf(`{"approval_id":%q,"approver":%q,"reason":%q}`, approvalID, approver, reason))
}

// apprRequestFor creates a DECIDED mode.request command and requests its
// approval; returns (commandID, approvalID).
func apprRequestFor(t *testing.T, mux *http.ServeMux, h *Handler, agent string) (string, string) {
	t.Helper()
	mustCreateActiveSession(t, h, agent, agentkernel.CapabilityModeRequest)
	id := execSubmitDecided(t, mux, agent, "mode.request")
	rec, body := apprRequest(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"ai"}}`, id))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("execute (approval request): %d: %s", rec.Code, rec.Body.String())
	}
	apvID, _ := body["id"].(string)
	if apvID == "" {
		t.Fatalf("approval id missing: %v", body)
	}
	return id, apvID
}

func TestApprovalsAuthGated(t *testing.T) {
	mux, _, _ := newExecTestServer(t)
	for _, path := range []string{"/api/bar/approvals", "/api/bar/approvals/approve", "/api/bar/approvals/reject"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without token = %d, want 401", path, rec.Code)
		}
	}
}

func TestApprovalsDisabledWithoutStore(t *testing.T) {
	// Fabric on, executions on, approvals NOT configured — the approval
	// surface 503s AND a coding execute refuses (never a silent bypass of
	// the human gate).
	mux, h, _ := newExecTestServer(t)
	h.agentApprovals = nil
	mustCreateActiveSession(t, h, "coder.bare", agentkernel.CapabilityModeRequest)
	id := execSubmitDecided(t, mux, "coder.bare", "mode.request")
	rec, _ := apprRequest(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"ai"}}`, id))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("coding execute without the approval store = %d, want 503 (fail-closed)", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/approvals", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /api/bar/approvals without store = %d, want 503", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/approvals/approve", `{"approval_id":"apv-`+strings.Repeat("a", 32)+`","approver":"op"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("approve without store = %d, want 503", rec.Code)
	}
}

func TestApprovalRequestCapturesParamsAndSubmitter(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "coder.cap", agentkernel.CapabilityModeRequest)
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		`{"intent":"switch to ai","agent_id":"coder.cap","capability":"mode.request","submitter":"zuzka"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: %d: %s", rec.Code, rec.Body.String())
	}
	id, _ := body["id"].(string)

	rec, body = apprRequest(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"ai"}}`, id))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("execute: %d: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "PENDING" {
		t.Fatalf("state = %v, want PENDING", body["state"])
	}
	if body["submitter"] != "zuzka" {
		t.Errorf("submitter = %v, want zuzka (the label rides the request)", body["submitter"])
	}
	if body["runtime"] != "coding" {
		t.Errorf("runtime = %v, want coding", body["runtime"])
	}
	if body["capability"] != "mode.request" {
		t.Errorf("capability = %v", body["capability"])
	}
	params, _ := body["params"].(map[string]any)
	if params["mode"] != "ai" {
		t.Errorf("captured params = %v, want mode=ai", body["params"])
	}
	// The composed command view carries the additive approval field.
	rec, body = barRequest(t, mux, http.MethodGet, "/api/bar/commands?limit=10", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	commands, _ := body["commands"].([]any)
	for _, c := range commands {
		cm, _ := c.(map[string]any)
		if cm["id"] == id {
			apv, _ := cm["approval"].(map[string]any)
			if apv == nil || apv["state"] != "PENDING" {
				t.Fatalf("command view approval = %v, want PENDING (additive field)", cm["approval"])
			}
			if cm["submitter"] != "zuzka" {
				t.Errorf("command view submitter = %v", cm["submitter"])
			}
			return
		}
	}
	t.Fatalf("command %s not found in list", id)
}

func TestApprovalRequestOnePerCommand(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	id, apvID := apprRequestFor(t, mux, h, "coder.one")
	if apvID == "" {
		t.Fatal("no approval id")
	}
	// A second execute for the same command → 409 (awaiting the operator).
	rec, body := apprRequest(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"ai"}}`, id))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate request = %d: %s", rec.Code, rec.Body.String())
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "awaiting the operator") {
		t.Errorf("duplicate message = %q, want the awaiting hint", msg)
	}
}

func TestApprovalApproveRunsTheAct(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	sink := attachAuditSink(t, h)
	id, apvID := apprRequestFor(t, mux, h, "coder.yes")

	rec, body := execApprove(t, mux, apvID, "op-xena")
	if rec.Code != http.StatusCreated {
		t.Fatalf("approve: %d: %s", rec.Code, rec.Body.String())
	}
	approval, _ := body["approval"].(map[string]any)
	if approval["state"] != "APPROVED" {
		t.Fatalf("approval state = %v, want APPROVED", approval["state"])
	}
	if approval["approver"] != "op-xena" {
		t.Errorf("approver = %v, want op-xena", approval["approver"])
	}
	if approval["execution_id"] == "" {
		t.Errorf("execution_id missing on the approval view")
	}
	exec, _ := body["execution"].(map[string]any)
	if exec["state"] != "COMPLETED" {
		t.Fatalf("execution state = %v, want COMPLETED: %v", exec["state"], body)
	}
	// The fresh evaluation was audited BEFORE the execution record.
	lines, readErrs, err := sink.ReadLast(10)
	if err != nil || len(readErrs) != 0 {
		t.Fatalf("audit read: %v / %v", err, readErrs)
	}
	found := false
	for _, l := range lines {
		if l.Type == agentkernel.AuditTypeEvaluate {
			found = true
		}
	}
	if !found {
		t.Errorf("no evaluate audit line for the approved act (audit-before-record)")
	}
	// Re-approve → 409 (terminal).
	rec, _ = execApprove(t, mux, apvID, "op-xena")
	if rec.Code != http.StatusConflict {
		t.Errorf("re-approve = %d, want 409 (terminal decision)", rec.Code)
	}
	// The execute surface now answers already-executed (the terminal record
	// exists; the approval closed with it).
	rec, _ = apprRequest(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"ai"}}`, id))
	if rec.Code != http.StatusConflict {
		t.Errorf("execute after approve = %d, want 409 (already executed)", rec.Code)
	}
}

func TestApprovalApproveFreshDenialIsSkipped(t *testing.T) {
	// SUSPEND the session BETWEEN request and approve — the world moved;
	// the fresh evaluation denies and the act never runs. APPROVED +
	// SKIPPED is the honest composition (the operator allowed it, the
	// kernel did not).
	mux, h, _ := newExecTestServer(t)
	sink := attachAuditSink(t, h)
	sessionID := mustCreateActiveSession(t, h, "coder.stale", agentkernel.CapabilityModeRequest)
	id := execSubmitDecided(t, mux, "coder.stale", "mode.request")
	rec, body := apprRequest(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"ai"}}`, id))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("request: %d: %s", rec.Code, rec.Body.String())
	}
	apvID, _ := body["id"].(string)

	suspendSession(t, h, sessionID)

	rec, body = execApprove(t, mux, apvID, "op-xena")
	if rec.Code != http.StatusCreated {
		t.Fatalf("approve: %d: %s", rec.Code, rec.Body.String())
	}
	approval, _ := body["approval"].(map[string]any)
	if approval["state"] != "APPROVED" {
		t.Fatalf("approval state = %v, want APPROVED (the operator said yes)", approval["state"])
	}
	exec, _ := body["execution"].(map[string]any)
	if exec["state"] != "SKIPPED" {
		t.Fatalf("execution state = %v, want SKIPPED (the fresh kernel denial)", exec["state"])
	}
	if allow, _ := exec["allow"].(bool); allow {
		t.Errorf("allow = true, want false (fresh denial)")
	}
	reason, _ := exec["reason"].(string)
	if !strings.Contains(reason, "session-state") {
		t.Errorf("reason = %q, want the fresh session-state rule", reason)
	}
	// The fresh denial is audited.
	lines, _, err := sink.ReadLast(10)
	if err != nil {
		t.Fatalf("audit read: %v", err)
	}
	denied := false
	for _, l := range lines {
		if l.Type == agentkernel.AuditTypeEvaluate && strings.Contains(l.Reason, "session-state") {
			denied = true
		}
	}
	if !denied {
		t.Errorf("fresh denial not audited (audit-before-record)")
	}
}

func TestApprovalRejectIsTerminal(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	id, apvID := apprRequestFor(t, mux, h, "coder.no")

	rec, body := apprReject(t, mux, apvID, "op-mira", "not on this host")
	if rec.Code != http.StatusCreated {
		t.Fatalf("reject: %d: %s", rec.Code, rec.Body.String())
	}
	if body["state"] != "REJECTED" {
		t.Fatalf("state = %v, want REJECTED", body["state"])
	}
	if body["approver"] != "op-mira" {
		t.Errorf("approver = %v", body["approver"])
	}
	if body["reason"] != "not on this host" {
		t.Errorf("reason = %v", body["reason"])
	}
	if body["execution_id"] != nil {
		t.Errorf("execution_id = %v, want absent (no act ever ran)", body["execution_id"])
	}
	// Execute after rejection → 409 (the command's execution path closed).
	rec, body = apprRequest(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"ai"}}`, id))
	if rec.Code != http.StatusConflict {
		t.Fatalf("execute after reject = %d: %s", rec.Code, rec.Body.String())
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "REJECTED") {
		t.Errorf("message = %q, want the REJECTED closure", msg)
	}
	// Approve after rejection → 409 (terminal).
	rec, _ = execApprove(t, mux, apvID, "op-xena")
	if rec.Code != http.StatusConflict {
		t.Errorf("approve after reject = %d, want 409", rec.Code)
	}
	// Reject again → 409.
	rec, _ = apprReject(t, mux, apvID, "op-mira", "")
	if rec.Code != http.StatusConflict {
		t.Errorf("re-reject = %d, want 409", rec.Code)
	}
}

func TestApprovalPendingRequestSurvivesIntentCancellationAttempt(t *testing.T) {
	// There is deliberately NO withdrawn state: a PENDING request's
	// command is DECIDED — terminal in the command lifecycle — so the
	// cancel surface refuses (409) and the request stays ANSWERABLE
	// (approve or reject) until the operator decides. A state no surface
	// can reach would be a lie in the enumeration.
	mux, h, _ := newExecTestServer(t)
	id, apvID := apprRequestFor(t, mux, h, "coder.wd")

	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands/cancel", fmt.Sprintf(`{"command_id":%q}`, id))
	if rec.Code != http.StatusConflict {
		t.Fatalf("cancel on DECIDED = %d, want 409 (DECIDED is terminal — the request stays answerable)", rec.Code)
	}
	// The request is STILL PENDING and the operator can still decide.
	if rec2, body2 := apprReject(t, mux, apvID, "op-late", "changed my mind via reject"); rec2.Code != http.StatusCreated {
		t.Fatalf("reject after failed cancel = %d: %s (the operator's no is always valid)", rec2.Code, rec2.Body.String())
	} else if body2["state"] != "REJECTED" {
		t.Fatalf("state = %v, want REJECTED", body2["state"])
	}
	_ = body
}

func TestApprovalShapesAndValidation(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	_, apvID := apprRequestFor(t, mux, h, "coder.shapes")

	// Malformed approval id → 400; well-formed unknown → 404.
	rec, _ := execApprove(t, mux, "apv-nope", "op")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed id = %d, want 400", rec.Code)
	}
	rec, _ = execApprove(t, mux, "apv-"+strings.Repeat("9", 32), "op")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", rec.Code)
	}
	// Approver label: empty / whitespace / over-bound / control chars → 400.
	for _, bad := range []string{"", "   ", strings.Repeat("x", 65), "a\nb"} {
		rec, _ = execApprove(t, mux, apvID, bad)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("approver %q = %d, want 400", bad, rec.Code)
		}
	}
	// Unknown request fields → 400 (closed shape).
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/approvals/approve",
		fmt.Sprintf(`{"approval_id":%q,"approver":"op","params":{"mode":"turbo"}}`, apvID))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("approve with params (re-steering) = %d, want 400 (the approved thing is the requested thing)", rec.Code)
	}
	// Reject reason bounds → 400.
	rec, _ = apprReject(t, mux, apvID, "op", strings.Repeat("x", 300))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("over-bound reject reason = %d, want 400", rec.Code)
	}
	// The request is STILL PENDING (no partial decisions).
	rec, body := barRequest(t, mux, http.MethodGet, "/api/bar/approvals?state=PENDING", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	approvals, _ := body["approvals"].([]any)
	if len(approvals) != 1 {
		t.Fatalf("PENDING approvals = %d, want 1 (no partial writes)", len(approvals))
	}
}

func TestApprovalResearchExecutesDirectly(t *testing.T) {
	// Research acts are read-only by construction — NO approval request is
	// ever created for them (the unchanged BF-3 flow).
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "research.direct", agentkernel.CapabilityStatusRead)
	id := execSubmitDecided(t, mux, "research.direct", "status.read")
	rec, body := apprRequest(t, mux, fmt.Sprintf(`{"command_id":%q}`, id))
	if rec.Code != http.StatusCreated {
		t.Fatalf("research execute = %d: %s (want the direct BF-3 flow)", rec.Code, rec.Body.String())
	}
	if body["state"] != "COMPLETED" {
		t.Fatalf("state = %v, want COMPLETED (no approval gate for research)", body["state"])
	}
	rec, body = barRequest(t, mux, http.MethodGet, "/api/bar/approvals", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if total, _ := body["total"].(float64); total != 0 {
		t.Errorf("total approvals = %v, want 0 (research never requests)", total)
	}
}

func TestApprovalsListCountsAndFilters(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	id1, apv1 := apprRequestFor(t, mux, h, "coder.l1")
	id2, apv2 := apprRequestFor(t, mux, h, "coder.l2")
	if rec, _ := execApprove(t, mux, apv1, "op-a"); rec.Code != http.StatusCreated {
		t.Fatalf("approve l1: %d", rec.Code)
	}
	if rec, _ := apprReject(t, mux, apv2, "op-b", "no"); rec.Code != http.StatusCreated {
		t.Fatalf("reject l2")
	}

	// Counts always present.
	rec, body := barRequest(t, mux, http.MethodGet, "/api/bar/approvals", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	counts, _ := body["counts"].(map[string]any)
	if counts["PENDING"].(float64) != 0 || counts["APPROVED"].(float64) != 1 || counts["REJECTED"].(float64) != 1 {
		t.Errorf("counts = %v, want APPROVED=1 REJECTED=1", counts)
	}
	if _, hasWithdrawn := counts["WITHDRAWN"]; hasWithdrawn {
		t.Errorf("counts carry WITHDRAWN — the closed enumeration is PENDING/APPROVED/REJECTED")
	}
	if total, _ := body["total"].(float64); total != 2 {
		t.Errorf("total = %v, want 2", total)
	}
	if _, ok := body["store_path"]; !ok {
		t.Errorf("store_path missing (operators surface)")
	}

	// State filter: unknown → 400.
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/approvals?state=APPROVING", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown state filter = %d, want 400", rec.Code)
	}
	// command_id: malformed → 400; unknown → 404; known → the single record.
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/approvals?command_id=cmd-nope", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed command filter = %d, want 400", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/approvals?command_id=cmd-"+strings.Repeat("9", 32), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown command filter = %d, want 404", rec.Code)
	}
	rec, body = barRequest(t, mux, http.MethodGet, "/api/bar/approvals?command_id="+id2+"&state=REJECTED", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered list: %d", rec.Code)
	}
	approvals, _ := body["approvals"].([]any)
	if len(approvals) != 1 {
		t.Fatalf("filtered approvals = %d, want 1", len(approvals))
	}
	apv, _ := approvals[0].(map[string]any)
	if apv["command_id"] != id2 || apv["state"] != "REJECTED" {
		t.Errorf("filtered record = %v", apv)
	}
	// A real-but-never-requested command → the honest empty list.
	rec, body = barRequest(t, mux, http.MethodGet, "/api/bar/approvals?command_id="+id1, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list by command: %d", rec.Code)
	}
	approvals, _ = body["approvals"].([]any)
	if len(approvals) != 1 {
		t.Fatalf("approvals for id1 = %d, want 1", len(approvals))
	}
	// limit bounds.
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/approvals?limit=0", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("limit=0 = %d, want 400", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/bar/approvals?limit=201", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("limit=201 = %d, want 400", rec.Code)
	}
	// Method discipline.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/bar/approvals", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST list = %d, want 405", rec.Code)
	}
}

func TestApprovalRestartPersistenceAndComposedViews(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	mux, h, _ := newExecTestServerOnDir(t, dir)
	id1, apv1 := apprRequestFor(t, mux, h, "coder.r1")
	_, apv2 := apprRequestFor(t, mux, h, "coder.r2")
	if rec, _ := execApprove(t, mux, apv1, "op-restart"); rec.Code != http.StatusCreated {
		t.Fatalf("approve before restart: %d", rec.Code)
	}
	if rec, _ := apprReject(t, mux, apv2, "op-restart", "no"); rec.Code != http.StatusCreated {
		t.Fatalf("reject before restart")
	}

	// RESTART: fresh stores over the same files (the file is the authority).
	fabricStore, err := agentfabric.NewCommandStore(filepath.Join(dir, "commands.json"), agentfabric.DefaultMaxCommands)
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	if err := fabricStore.Load(); err != nil {
		t.Fatalf("commands load: %v", err)
	}
	if err := h.ConfigureAgentFabric(fabricStore); err != nil {
		t.Fatalf("configure fabric: %v", err)
	}
	execStore, err := agentfabric.NewExecutionStore(filepath.Join(dir, "executions.json"), agentfabric.DefaultMaxExecutions)
	if err != nil {
		t.Fatalf("executions: %v", err)
	}
	if err := execStore.Load(); err != nil {
		t.Fatalf("executions load: %v", err)
	}
	if err := h.ConfigureAgentFabricExecutions(execStore, agentfabric.NewFabricBus()); err != nil {
		t.Fatalf("configure executions: %v", err)
	}
	apvStore, err := agentfabric.NewApprovalStore(filepath.Join(dir, "approvals.json"), agentfabric.DefaultMaxApprovals)
	if err != nil {
		t.Fatalf("approvals: %v", err)
	}
	if err := apvStore.Load(); err != nil {
		t.Fatalf("approvals load: %v (the history must survive)", err)
	}
	if err := h.ConfigureAgentFabricApprovals(apvStore); err != nil {
		t.Fatalf("configure approvals: %v", err)
	}

	rec, body := barRequest(t, mux, http.MethodGet, "/api/bar/approvals", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list after restart: %d", rec.Code)
	}
	counts, _ := body["counts"].(map[string]any)
	if counts["APPROVED"].(float64) != 1 || counts["REJECTED"].(float64) != 1 {
		t.Errorf("counts after restart = %v, want APPROVED=1 REJECTED=1", counts)
	}
	// The composed command view survives (approval + execution fields).
	rec, body = barRequest(t, mux, http.MethodGet, "/api/bar/commands?limit=50", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("commands after restart: %d", rec.Code)
	}
	commands, _ := body["commands"].([]any)
	var cm map[string]any
	for _, c := range commands {
		if m, _ := c.(map[string]any); m["id"] == id1 {
			cm = m
		}
	}
	if cm == nil {
		t.Fatalf("command %s not found after restart", id1)
	}
	apv, _ := cm["approval"].(map[string]any)
	if apv == nil || apv["state"] != "APPROVED" {
		t.Fatalf("composed approval after restart = %v", cm["approval"])
	}
	exec, _ := cm["execution"].(map[string]any)
	if exec == nil || exec["state"] != "COMPLETED" {
		t.Fatalf("composed execution after restart = %v", cm["execution"])
	}
	// The one-request-per-command invariant survives (409 on re-request).
	rec, _ = apprRequest(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"ai"}}`, id1))
	if rec.Code != http.StatusConflict {
		t.Errorf("re-request after restart = %d, want 409 (one request per command)", rec.Code)
	}
}

func TestApprovalBusSignals(t *testing.T) {
	mux, h, bus := newExecTestServer(t)
	_, ch, ok := bus.Subscribe()
	if !ok {
		t.Fatal("Subscribe failed")
	}
	mustCreateActiveSession(t, h, "coder.sig", agentkernel.CapabilityModeRequest)
	id := execSubmitDecided(t, mux, "coder.sig", "mode.request")

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
	// Drain the submit/route/decide signals first.
	collect(3, time.Second)

	rec, body := apprRequest(t, mux, fmt.Sprintf(`{"command_id":%q,"params":{"mode":"ai"}}`, id))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("request: %d: %s", rec.Code, rec.Body.String())
	}
	apvID, _ := body["id"].(string)
	sigs := collect(1, 2*time.Second)
	if len(sigs) != 1 || sigs[0].Type != agentfabric.SignalApprovalRequested || sigs[0].Ref != apvID {
		t.Fatalf("approval.requested signal = %+v, want {approval.requested %s}", sigs, apvID)
	}
	if recSig, _ := execApprove(t, mux, apvID, "op-sig"); recSig.Code != http.StatusCreated {
		t.Fatalf("approve: %d", recSig.Code)
	}
	sigs = collect(3, 2*time.Second) // execution.recorded + approval.decided
	seen := map[agentfabric.SignalType]bool{}
	for _, s := range sigs {
		seen[s.Type] = true
	}
	if !seen[agentfabric.SignalExecutionRecorded] || !seen[agentfabric.SignalApprovalDecided] {
		t.Fatalf("approve signals = %v, want execution.recorded + approval.decided", sigs)
	}
}

func TestApprovalApproveReconciliation(t *testing.T) {
	// The crashed-approve path: the execution exists, the approval is
	// STILL PENDING — a retry approve closes the approval APPROVED
	// referencing the EXISTING execution, no new act.
	mux, h, _ := newExecTestServer(t)
	id, apvID := apprRequestFor(t, mux, h, "coder.rec")

	// Simulate the crash between the execution persist and the approval
	// close: write the terminal execution directly through the store.
	cmdRec, ok := h.agentFabric.Get(agentfabric.CommandID(id))
	if !ok {
		t.Fatalf("command %s vanished", id)
	}
	now := time.Now().UTC()
	rec := agentfabric.ExecutionRecord{
		ID:         "afx-" + strings.Repeat("a", 32),
		CommandID:  id,
		AgentID:    "coder.rec",
		Capability: agentkernel.CapabilityModeRequest,
		SessionID:  cmdRec.SessionID,
		Runtime:    agentfabric.RuntimeCoding,
		State:      agentfabric.ExecutionCompleted,
		Allow:      true,
		Outcome:    "mode request accepted: desired-mode=ai (reconciled fixture)",
		Reason:     "grant/default: allowed",
		Intent:     "do the thing",
		ExecutedAt: now,
	}
	if _, err := h.agentExecutions.Record(rec); err != nil {
		t.Fatalf("fixture execution record: %v", err)
	}

	resp, body := execApprove(t, mux, apvID, "op-reconcile")
	if resp.Code != http.StatusCreated {
		t.Fatalf("reconciling approve = %d: %s", resp.Code, resp.Body.String())
	}
	if reconciled, _ := body["reconciled"].(bool); !reconciled {
		t.Errorf("reconciled flag = %v, want true (no new act ran)", body["reconciled"])
	}
	approval, _ := body["approval"].(map[string]any)
	if approval["state"] != "APPROVED" {
		t.Fatalf("approval state = %v, want APPROVED", approval["state"])
	}
	exec, _ := body["execution"].(map[string]any)
	if exec["id"] != rec.ID {
		t.Errorf("execution id = %v, want the EXISTING record %s (no new act)", exec["id"], rec.ID)
	}
	// Exactly ONE execution exists (the invariant holds).
	if total := h.agentExecutions.Count(); total != 1 {
		t.Errorf("executions = %d, want 1 (reconciliation must not double-act)", total)
	}
}

func TestApprovalSubmitterValidationOnSubmit(t *testing.T) {
	mux, h, _ := newExecTestServer(t)
	mustCreateActiveSession(t, h, "coder.lbl", agentkernel.CapabilityModeRequest)
	// Whitespace-only trims to UNLABELED (the optional-field normalization —
	// 201); over-bound and control-carrying labels are 400s.
	for _, bad := range []string{strings.Repeat("x", 65), "a\nb"} {
		rec, _ := barRequest(t, mux, http.MethodPost, "/api/bar/commands",
			fmt.Sprintf(`{"intent":"i","agent_id":"coder.lbl","capability":"mode.request","submitter":%q}`, bad))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("submitter %q = %d, want 400", bad, rec.Code)
		}
	}
	rec, body := barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		`{"intent":"i2","agent_id":"coder.lbl","capability":"mode.request","submitter":"  "}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("whitespace-only submitter = %d, want 201 (normalizes to unlabeled)", rec.Code)
	} else if _, has := body["submitter"]; has {
		t.Errorf("normalized submitter echoed: %v, want the field absent", body["submitter"])
	}
	rec, body = barRequest(t, mux, http.MethodPost, "/api/bar/commands",
		`{"intent":"i3","agent_id":"coder.lbl","capability":"mode.request","submitter":"zuzka"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid submitter = %d: %s", rec.Code, rec.Body.String())
	}
	if body["submitter"] != "zuzka" {
		t.Errorf("submitter = %v", body["submitter"])
	}
}
