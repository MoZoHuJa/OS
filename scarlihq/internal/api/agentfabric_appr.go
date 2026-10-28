package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentfabric"
	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

// BF-4 (v19.5.3): the Agent Fabric operator-approval surface — the human
// gate for MUTATING acts. Design rules (docs/AGENT_FABRIC_BF4_DESIGN.md):
//
//   - every route is behind the standard Bearer auth middleware (h.auth);
//   - the approval boundary IS the runtime boundary: ApprovalRequiredFor
//     Capability answers exactly the coding-runtime capabilities (bridged
//     acts that mutate host state). Research acts are read-only by
//     construction — they execute directly (the unchanged BF-3 flow), no
//     approval request is ever created for them;
//   - ONE APPROVAL REQUEST PER COMMAND, ever: the execute surface creates
//     the request (202 + PENDING view — the params captured, the submitter
//     labeled); a second request is a 409; a REJECTED request closes the
//     command's execution path (retry is a NEW intent at the bar);
//   - APPROVE runs the act: the full BF-3 execute flow (command gates,
//     ARCHIVED tombstone, the STALE-ALLOW GUARD fresh kernel evaluation,
//     audit-before-record, the bounded act, the terminal execution record)
//     composed with the operator's yes — the approval record closes
//     APPROVED referencing the terminal execution (ALWAYS, even the
//     audited SKIPPED of a fresh denial: APPROVED + SKIPPED is the honest
//     "the operator allowed it, the kernel did not");
//   - approve CANNOT re-steer the act: the act re-uses the params captured
//     at REQUEST time (the stored closed shape — the approved thing is the
//     requested thing);
//   - REJECT is unconditional: the operator's no needs no kernel
//     evaluation and no command gates — terminal, no act ever runs;
//   - reconciliation: an approve on a PENDING request whose command is
//     already executed (a crashed approve-after-execution-persist) closes
//     the approval APPROVED referencing the EXISTING execution — no new
//     act, the file is the authority;
//   - the read surface mirrors /api/bar/executions exactly: bounded
//     newest-first, closed filters (?state=, ?command_id=), counts always;
//   - labels (submitter/approver) are ATTRIBUTION, never credentials —
//     the bearer token remains the only authentication boundary.

// ConfigureAgentFabricApprovals wires the approval store into the
// handler. It rides the fabric opt-in (main.go constructs it whenever the
// fabric is enabled — no separate flag, exactly like the execution store).
func (h *Handler) ConfigureAgentFabricApprovals(store *agentfabric.ApprovalStore) error {
	if store == nil {
		return errors.New("api: agent fabric approval store is required")
	}
	h.agentApprovals = store
	return nil
}

// approvalsDisabled reports (and writes) whether the approval surface is
// unavailable: fabric off (503), approval store off (503), kernel off
// (503 — approve runs a fresh kernel evaluation; reject/list do NOT need
// the kernel, but the routes share the gate for one honest surface).
func (h *Handler) approvalsDisabled(w http.ResponseWriter) bool {
	if h.agentFabric == nil || h.agentApprovals == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric approvals are disabled")
		return true
	}
	if h.agentStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric requires the agent kernel (SCARLIHQ_AGENTKERNEL_ENABLED=true) — approvals gate kernel-evaluated acts")
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Request / view shapes (closed — unknown fields rejected)
// ---------------------------------------------------------------------------

// barApprovalApproveRequest: one JSON object, unknown fields rejected.
// The approver label is REQUIRED (attribution — who answered; a label,
// never a credential). No params: the act re-uses the params captured at
// request time (an approval decision cannot re-steer the act).
type barApprovalApproveRequest struct {
	ApprovalID string `json:"approval_id"`
	Approver   string `json:"approver"`
}

// barApprovalRejectRequest: one JSON object, unknown fields rejected.
// The approver label is REQUIRED; the reason is optional (bounded — the
// why, for the reviewable history).
type barApprovalRejectRequest struct {
	ApprovalID string `json:"approval_id"`
	Approver   string `json:"approver"`
	Reason     string `json:"reason,omitempty"`
}

// barApprovalParamsView echoes the captured params (the closed shape —
// exactly the fields the capability takes).
type barApprovalParamsView struct {
	Mode         string `json:"mode,omitempty"`
	AllocationID string `json:"allocation_id,omitempty"`
}

// barApprovalView is the user-visible approval status (listed at
// /api/bar/approvals, composed onto command views as the additive
// "approval" field summary, and returned by request/approve/reject).
// The composed execution view is present exactly when the approval is
// APPROVED and its execution record exists.
type barApprovalView struct {
	ID          string                 `json:"id"`
	CommandID   string                 `json:"command_id"`
	AgentID     string                 `json:"agent_id"`
	Capability  string                 `json:"capability"`
	WorkspaceID string                 `json:"workspace_id,omitempty"`
	SessionID   string                 `json:"session_id"`
	Runtime     string                 `json:"runtime"`
	Submitter   string                 `json:"submitter,omitempty"`
	Params      *barApprovalParamsView `json:"params,omitempty"`
	State       string                 `json:"state"`
	Approver    string                 `json:"approver,omitempty"`
	Reason      string                 `json:"reason,omitempty"`
	ExecutionID string                 `json:"execution_id,omitempty"`
	Intent      string                 `json:"intent"`
	RequestedAt string                 `json:"requested_at"`
	DecidedAt   string                 `json:"decided_at,omitempty"`
	Execution   *barExecutionView      `json:"execution,omitempty"`
}

// viewFromApproval renders the full approval view, composing the terminal
// execution when the approval is APPROVED and the execution store carries
// the referenced record (nil-safe).
func (h *Handler) viewFromApproval(a agentfabric.ApprovalRecord) barApprovalView {
	v := barApprovalView{
		ID:          a.ID,
		CommandID:   a.CommandID,
		AgentID:     string(a.AgentID),
		Capability:  string(a.Capability),
		WorkspaceID: a.WorkspaceID,
		SessionID:   a.SessionID,
		Runtime:     string(a.Runtime),
		Submitter:   a.Submitter,
		State:       string(a.State),
		Approver:    a.Approver,
		Reason:      a.Reason,
		ExecutionID: a.ExecutionID,
		Intent:      a.Intent,
		RequestedAt: a.RequestedAt.Format(time.RFC3339),
	}
	if a.Params.Mode != "" || a.Params.AllocationID != "" {
		v.Params = &barApprovalParamsView{
			Mode:         a.Params.Mode,
			AllocationID: a.Params.AllocationID,
		}
	}
	if a.DecidedAt != nil {
		v.DecidedAt = a.DecidedAt.Format(time.RFC3339)
	}
	if a.ExecutionID != "" && h.agentExecutions != nil {
		if exec, ok := h.agentExecutions.Get(agentfabric.ExecutionID(a.ExecutionID)); ok {
			v.Execution = executionViewPtr(viewFromExecution(exec))
		}
	}
	return v
}

// barApprovalSummary is the composed approval view on a command (the
// additive BF-4 field): present exactly when the command carries an
// approval request (coding acts only).
type barApprovalSummary struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Submitter   string `json:"submitter,omitempty"`
	Approver    string `json:"approver,omitempty"`
	Reason      string `json:"reason,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
	RequestedAt string `json:"requested_at"`
}

// executionViewPtr is a tiny helper (the view is a value type; the
// composed field is a pointer for omitempty).
func executionViewPtr(v barExecutionView) *barExecutionView {
	return &v
}

// ---------------------------------------------------------------------------
// The request creation (composed into POST /api/bar/commands/execute)
// ---------------------------------------------------------------------------

// barApprovalRequest is the execute-surface branch for approval-required
// (coding) capabilities: create the ONE approval request for the command
// (202 + PENDING view — the params captured, the submitter labeled), or
// answer the honest 409 for an existing request. The approval store is
// REQUIRED (503 when missing — a mutating act never silently bypasses the
// human gate).
func (h *Handler) barApprovalRequest(w http.ResponseWriter, cmd agentfabric.CommandRecord, params agentfabric.RuntimeParams) {
	if h.agentApprovals == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric approvals are disabled — a mutating (coding) act cannot execute without the operator approval surface")
		return
	}
	id := agentfabric.CommandID(cmd.ID)
	if existing, ok := h.agentApprovals.GetByCommand(id); ok {
		switch existing.State {
		case agentfabric.ApprovalPending:
			writeJSONError(w, http.StatusConflict, "approval already requested ("+existing.ID+") — awaiting the operator (GET /api/bar/approvals, POST /api/bar/approvals/approve|reject)")
		case agentfabric.ApprovalApproved:
			// Unreachable in practice (the already-executed gate fires
			// first); fail-closed regardless.
			writeJSONError(w, http.StatusConflict, "approval already decided (APPROVED) — the execution is recorded; retry is a new intent")
		case agentfabric.ApprovalRejected:
			writeJSONError(w, http.StatusConflict, "approval was REJECTED — the command's execution path is closed; retry is a NEW intent at the bar")
		default:
			writeJSONError(w, http.StatusConflict, "approval is "+string(existing.State))
		}
		return
	}

	apvID, err := agentfabric.NewApprovalID()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "approval id generation failed")
		return
	}
	rec := agentfabric.ApprovalRecord{
		ID:          apvID,
		CommandID:   cmd.ID,
		AgentID:     cmd.AgentID,
		Capability:  cmd.Capability,
		WorkspaceID: cmd.WorkspaceID,
		SessionID:   cmd.SessionID,
		Runtime:     agentfabric.RuntimeID(""),
		Submitter:   cmd.Submitter,
		Params: agentfabric.ApprovalParams{
			Mode:         params.Mode,
			AllocationID: params.AllocationID,
		},
		State:       agentfabric.ApprovalPending,
		Intent:      cmd.Intent,
		RequestedAt: time.Now().UTC(),
	}
	if rt, ok := agentfabric.RuntimeForCapability(cmd.Capability); ok {
		rec.Runtime = rt
	}
	saved, err := h.agentApprovals.Request(rec)
	if err != nil {
		switch {
		case errors.Is(err, agentfabric.ErrAlreadyRequested):
			writeJSONError(w, http.StatusConflict, "approval already requested — one request per command")
		case errors.Is(err, agentfabric.ErrApprovalLimit):
			writeJSONError(w, http.StatusServiceUnavailable, "approval store at capacity — archive and rotate the approvals file deliberately")
		case errors.Is(err, agentfabric.ErrApprovalRNG):
			writeJSONError(w, http.StatusInternalServerError, "approval id generation failed")
		default:
			writeJSONError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	h.fabricPublish(agentfabric.SignalApprovalRequested, saved.ID)
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, h.viewFromApproval(saved))
}

// ---------------------------------------------------------------------------
// POST /api/bar/approvals/approve
// ---------------------------------------------------------------------------

// barApprovalApprove closes a PENDING request as APPROVED and RUNS THE
// ACT — the full BF-3 execute flow composed with the operator's yes.
// Response semantics:
//
//	201 + {approval, execution} — the act ran (COMPLETED/FAILED) or the
//	      fresh evaluation denied (audited SKIPPED — the honest
//	      "operator allowed, kernel did not")
//	201 + {approval} (reconciliation) — the command was already executed
//	      (a crashed approve after the execution persist); the approval
//	      closes APPROVED referencing the EXISTING execution, no new act
//	400 — malformed approval id / approver label / unknown fields
//	404 — unknown approval id
//	409 — not PENDING, command vanished/not DECIDED/denied/ARCHIVED
//	503 — fabric/approvals/kernel not configured
func (h *Handler) barApprovalApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.approvalsDisabled(w) {
		return
	}
	if h.agentExecutions == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric executions are disabled — approve runs the act")
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &barApprovalApproveRequest{} })
	if !ok {
		return
	}
	req := request.(*barApprovalApproveRequest)

	// ApprovalID SHAPE first (the filter precedent: 400 vs 404).
	apvID := agentfabric.ApprovalID(strings.TrimSpace(req.ApprovalID))
	if err := apvID.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	approval, ok := h.agentApprovals.Get(apvID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown approval")
		return
	}
	if approval.State != agentfabric.ApprovalPending {
		writeJSONError(w, http.StatusConflict, "approval is "+string(approval.State)+" — the decision is terminal (one decision per request)")
		return
	}
	approver := strings.TrimSpace(req.Approver)
	if err := agentfabric.ValidatePrincipalLabel(approver); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// The command gates (the BF-3 execute discipline, re-checked — the
	// world moved between request and approve).
	cmdID := agentfabric.CommandID(approval.CommandID)
	cmd, exists := h.agentFabric.Get(cmdID)
	if !exists {
		writeJSONError(w, http.StatusConflict, "command vanished before approval — commands are never deleted; a rotated file is an operator act (fail-closed, no execution)")
		return
	}
	if cmd.State != agentfabric.CommandDecided {
		writeJSONError(w, http.StatusConflict, "command is "+string(cmd.State)+" — only DECIDED commands execute")
		return
	}
	if cmd.Decision == nil || !cmd.Decision.Allow {
		writeJSONError(w, http.StatusConflict, "command is DECIDED with allow=false — a denial is not executed")
		return
	}

	// RECONCILIATION: the command already carries a terminal execution
	// (a crashed approve AFTER the execution persist) — close the
	// approval APPROVED referencing the existing record. No new act;
	// the file is the authority.
	if existing, already := h.agentExecutions.GetByCommand(cmdID); already {
		saved, err := h.agentApprovals.Approve(apvID, approver, existing.ID, time.Now().UTC())
		if err != nil {
			h.barApprovalMutationError(w, err)
			return
		}
		h.fabricPublish(agentfabric.SignalApprovalDecided, saved.ID)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]interface{}{
			"approval":   h.viewFromApproval(saved),
			"execution":  viewFromExecution(existing),
			"reconciled": true,
		})
		return
	}

	// ARCHIVED scope closes execution (the tombstone contract).
	if cmd.WorkspaceID != "" {
		if h.agentWorkspaces == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "agent fabric workspaces are disabled")
			return
		}
		ws, ok := h.agentWorkspaces.Get(agentfabric.WorkspaceID(cmd.WorkspaceID))
		if !ok {
			writeJSONError(w, http.StatusConflict, "workspace "+cmd.WorkspaceID+" does not exist — execution refused (fail-closed)")
			return
		}
		if ws.State == agentfabric.WorkspaceArchived {
			writeJSONError(w, http.StatusConflict, "workspace "+ws.ID+" is ARCHIVED — execution is closed (the command stays DECIDED, visible, cancellable)")
			return
		}
	}

	// The act re-uses the STORED params — an approval decision cannot
	// re-steer the act (the approved thing is the requested thing).
	params := agentfabric.RuntimeParams{
		Mode:         approval.Params.Mode,
		AllocationID: approval.Params.AllocationID,
	}

	// ------------------------------------------------------------------
	// STALE-ALLOW GUARD (the BF-3 heart, unchanged at approve time):
	// a FRESH kernel evaluation. The world moved since the request.
	// ------------------------------------------------------------------
	now := time.Now().UTC()
	policy := h.agentPolicy
	if h.agentGrants != nil {
		policy = h.agentGrants.Policy()
	}
	event, decision, err := h.agentStore.Evaluate(cmd.SessionID, policy, cmd.Capability, now)
	if err != nil {
		if errors.Is(err, agentkernel.ErrUnknownSession) {
			writeJSONError(w, http.StatusConflict, "session vanished before approval — the command is DECIDED but its session no longer exists (fail-closed; no execution record)")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "fresh evaluation failed")
		return
	}

	// AK-4 discipline: the fresh evaluation is audited BEFORE any
	// execution record exists (fail-closed — an unauditable execution
	// decision is never recorded).
	if h.agentAudit != nil {
		if err := h.agentAudit.Append(agentkernel.NewAuditLineFromEvent(event)); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "audit write failed — execution not recorded (fail-closed)")
			return
		}
	}

	execID, err := agentfabric.NewExecutionID()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "execution id generation failed")
		return
	}

	var rec agentfabric.ExecutionRecord
	if !decision.Allow {
		// Fresh DENIAL → audited SKIPPED. APPROVED + SKIPPED is the
		// honest composition: the operator allowed it, the kernel
		// did not.
		rec = agentfabric.ExecutionRecord{
			ID:          execID,
			CommandID:   cmd.ID,
			AgentID:     cmd.AgentID,
			Capability:  cmd.Capability,
			WorkspaceID: cmd.WorkspaceID,
			SessionID:   cmd.SessionID,
			Runtime:     approval.Runtime,
			State:       agentfabric.ExecutionSkipped,
			Allow:       false,
			Reason:      decision.Reason,
			Intent:      cmd.Intent,
			ExecutedAt:  now,
		}
	} else {
		outcome, actErr := h.runtimeAct(cmd, params)
		if actErr != nil {
			rec = agentfabric.ExecutionRecord{
				ID:          execID,
				CommandID:   cmd.ID,
				AgentID:     cmd.AgentID,
				Capability:  cmd.Capability,
				WorkspaceID: cmd.WorkspaceID,
				SessionID:   cmd.SessionID,
				Runtime:     approval.Runtime,
				State:       agentfabric.ExecutionFailed,
				Allow:       true,
				Reason:      truncateRunes(actErr.Error(), agentfabric.MaxReasonRunes),
				Intent:      cmd.Intent,
				ExecutedAt:  time.Now().UTC(),
			}
		} else {
			rec = agentfabric.ExecutionRecord{
				ID:          execID,
				CommandID:   cmd.ID,
				AgentID:     cmd.AgentID,
				Capability:  cmd.Capability,
				WorkspaceID: cmd.WorkspaceID,
				SessionID:   cmd.SessionID,
				Runtime:     approval.Runtime,
				State:       agentfabric.ExecutionCompleted,
				Allow:       true,
				Outcome:     truncateRunes(outcome, agentfabric.MaxOutcomeRunes),
				Reason:      decision.Reason,
				Intent:      cmd.Intent,
				ExecutedAt:  time.Now().UTC(),
			}
		}
	}

	// The terminal execution record is persisted BEFORE the approval
	// closes (a crash between the two persists is the reconciliation
	// path above — the approval re-approves against the existing
	// execution; no act runs twice through that door).
	saved, err := h.agentExecutions.Record(rec)
	if err != nil {
		switch {
		case errors.Is(err, agentfabric.ErrAlreadyExecuted):
			writeJSONError(w, http.StatusConflict, "command already executed — the execution record is terminal")
		case errors.Is(err, agentfabric.ErrExecutionLimit):
			writeJSONError(w, http.StatusServiceUnavailable, "execution store at capacity — archive and rotate the executions file deliberately")
		default:
			writeJSONError(w, http.StatusInternalServerError, "execution persist failed — the act "+map[bool]string{true: "ran", false: "did not run"}[decision.Allow]+" but no record was written (fail-closed)")
		}
		return
	}

	decided, err := h.agentApprovals.Approve(apvID, approver, saved.ID, time.Now().UTC())
	if err != nil {
		// The execution exists on disk, the approval is still PENDING —
		// surfaced honestly; the operator's retry approve takes the
		// reconciliation path (no new act).
		h.barApprovalMutationError(w, err)
		return
	}

	h.fabricPublish(agentfabric.SignalExecutionRecorded, saved.ID)
	h.fabricPublish(agentfabric.SignalApprovalDecided, decided.ID)

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{
		"approval":  h.viewFromApproval(decided),
		"execution": viewFromExecution(saved),
	})
}

// ---------------------------------------------------------------------------
// POST /api/bar/approvals/reject
// ---------------------------------------------------------------------------

// barApprovalReject closes a PENDING request as REJECTED — terminal, no
// act ever runs, the command's execution path is closed (retry is a NEW
// intent at the bar). The operator's no is UNCONDITIONAL: no kernel
// evaluation gates a rejection (no act runs, there is nothing to
// evaluate). Response semantics:
//
//	201 + approval view (REJECTED)
//	400 — malformed approval id / approver or reason label / unknown fields
//	404 — unknown approval id
//	409 — not PENDING (the decision is terminal)
//	503 — fabric/approvals not configured
func (h *Handler) barApprovalReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.agentFabric == nil || h.agentApprovals == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric approvals are disabled")
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &barApprovalRejectRequest{} })
	if !ok {
		return
	}
	req := request.(*barApprovalRejectRequest)

	apvID := agentfabric.ApprovalID(strings.TrimSpace(req.ApprovalID))
	if err := apvID.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := h.agentApprovals.Get(apvID); !ok {
		writeJSONError(w, http.StatusNotFound, "unknown approval")
		return
	}
	approver := strings.TrimSpace(req.Approver)
	if err := agentfabric.ValidatePrincipalLabel(approver); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason != "" {
		// Bounded at the store; the early check keeps the 400 clean.
		if err := agentfabric.ValidateExecutionReason(reason); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	rejected, err := h.agentApprovals.Reject(apvID, approver, reason, time.Now().UTC())
	if err != nil {
		h.barApprovalMutationError(w, err)
		return
	}
	h.fabricPublish(agentfabric.SignalApprovalDecided, rejected.ID)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, h.viewFromApproval(rejected))
}

// barApprovalMutationError maps approval-store mutation errors to honest
// HTTP answers.
func (h *Handler) barApprovalMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agentfabric.ErrUnknownApproval):
		writeJSONError(w, http.StatusNotFound, "unknown approval")
	case errors.Is(err, agentfabric.ErrApprovalState):
		writeJSONError(w, http.StatusConflict, err.Error())
	case errors.Is(err, agentfabric.ErrApprovalLimit):
		writeJSONError(w, http.StatusServiceUnavailable, "approval store at capacity — archive and rotate the approvals file deliberately")
	default:
		writeJSONError(w, http.StatusInternalServerError, "approval persist failed — no decision was recorded (fail-closed)")
	}
}

// ---------------------------------------------------------------------------
// GET /api/bar/approvals
// ---------------------------------------------------------------------------

// barApprovals lists approval records, newest-first, bounded
// (?limit=1..200, default 200 — the executions read surface's
// discipline), with optional closed filters:
//
//	?state=PENDING|APPROVED|REJECTED            (unknown → 400)
//	?command_id=<cmd-…>                          (malformed → 400; unknown command → 404)
//
// Counts (per state, all states always present) are always included.
func (h *Handler) barApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	if h.agentFabric == nil || h.agentApprovals == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric approvals are disabled")
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeJSONError(w, http.StatusBadRequest, "limit must be an integer between 1 and 200")
			return
		}
		limit = n
	}

	// Closed state filter.
	stateRaw := strings.TrimSpace(r.URL.Query().Get("state"))
	var stateFilter agentfabric.ApprovalState
	if stateRaw != "" {
		switch agentfabric.ApprovalState(stateRaw) {
		case agentfabric.ApprovalPending, agentfabric.ApprovalApproved, agentfabric.ApprovalRejected:
			stateFilter = agentfabric.ApprovalState(stateRaw)
		default:
			writeJSONError(w, http.StatusBadRequest, "state must be one of PENDING, APPROVED, REJECTED (closed enumeration)")
			return
		}
	}

	// Command filter: shape first (400), then existence in the command
	// store (404 — the executions surface's discipline).
	commandRaw := strings.TrimSpace(r.URL.Query().Get("command_id"))
	var commandFilter agentfabric.CommandID
	if commandRaw != "" {
		commandFilter = agentfabric.CommandID(commandRaw)
		if err := commandFilter.Validate(); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, exists := h.agentFabric.Get(commandFilter); !exists {
			writeJSONError(w, http.StatusNotFound, "unknown command")
			return
		}
	}

	var records []agentfabric.ApprovalRecord
	switch {
	case commandFilter != "" && stateFilter != "":
		rec, ok := h.agentApprovals.GetByCommand(commandFilter)
		if ok && rec.State == stateFilter {
			records = []agentfabric.ApprovalRecord{rec}
		} else {
			records = []agentfabric.ApprovalRecord{}
		}
	case commandFilter != "":
		if rec, ok := h.agentApprovals.GetByCommand(commandFilter); ok {
			records = []agentfabric.ApprovalRecord{rec}
		} else {
			records = []agentfabric.ApprovalRecord{}
		}
	case stateFilter != "":
		records = h.agentApprovals.ListLastState(limit, stateFilter)
	default:
		records = h.agentApprovals.ListLast(limit)
	}

	views := make([]barApprovalView, 0, len(records))
	for _, rec := range records {
		views = append(views, h.viewFromApproval(rec))
	}
	writeJSON(w, map[string]interface{}{
		"approvals":  views,
		"count":      len(views),
		"counts":     h.agentApprovals.Counts(),
		"limit":      limit,
		"store_path": h.agentApprovals.Path(),
		"total":      h.agentApprovals.Count(),
	})
}
