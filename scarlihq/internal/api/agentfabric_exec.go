package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentfabric"
	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
	"github.com/MoZoHuJa/OS/scarlihq/internal/status"
	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

// BF-3 (v19.5.2): Agent Fabric execution surface — the coding/research
// agent runtimes. Design rules (docs/AGENT_FABRIC_BF3_DESIGN.md):
//
//   - every route is behind the standard Bearer auth middleware (h.auth);
//   - an ALLOWED decision is still not a licence: the execute surface
//     runs a FRESH kernel evaluation (the STALE-ALLOW GUARD — the
//     command's decision may be old; sessions get suspended, grants get
//     revoked, capabilities get disabled). A fresh denial is an AUDITED
//     SKIPPED record (201 — the execution IS the result, the kernel's
//     denial-is-200 contract carried to executions); a fresh allow runs
//     the act;
//   - one execution per command, ever: the record is terminal
//     (COMPLETED/FAILED/SKIPPED — there is no in-flight state, the
//     execute call is synchronous and bounded). A second execute is a
//     409; retry-after-failure is a NEW intent at the bar (the file is
//     the history);
//   - ordering discipline (AK-4 carried to executions): the fresh
//     evaluation is persisted to the audit sink BEFORE the execution
//     record is created (an unauditable execution decision is never
//     recorded); the execution record is persisted BEFORE the response
//     is served (a served view always exists on disk);
//   - the runtimes are CLOSED: RuntimeForCapability binds every
//     capability to exactly one runtime (research = read-only acts,
//     coding = bridged acts); params are closed shapes
//     (DisallowUnknownFields — a param shape is a contract, not a
//     suggestion);
//   - an ARCHIVED workspace closes execution for its scoped commands
//     (409 — the same tombstone contract that closes routing);
//   - the command view composes the execution (additive field — BF-1/BF-2
//     files and clients are unaffected).

// ConfigureAgentFabricExecutions wires the execution store + the fabric
// event bus into the handler. Both ride the fabric opt-in (main.go
// constructs them whenever the fabric is enabled — no separate flag).
func (h *Handler) ConfigureAgentFabricExecutions(store *agentfabric.ExecutionStore, bus *agentfabric.FabricBus) error {
	if store == nil {
		return errors.New("api: agent fabric execution store is required")
	}
	if bus == nil {
		return errors.New("api: agent fabric event bus is required")
	}
	h.agentExecutions = store
	h.agentBus = bus
	return nil
}

// executionsDisabled reports (and writes) whether the execution surface
// is unavailable: fabric off (503), kernel off (503 — the fresh guard
// evaluates through the kernel), execution store off (503).
func (h *Handler) executionsDisabled(w http.ResponseWriter) bool {
	if h.agentFabric == nil || h.agentExecutions == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric executions are disabled")
		return true
	}
	if h.agentStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric requires the agent kernel (SCARLIHQ_AGENTKERNEL_ENABLED=true) — executions run a fresh kernel evaluation")
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Request / view shapes (closed — unknown fields rejected)
// ---------------------------------------------------------------------------

// barCommandExecuteRequest: one JSON object, unknown fields rejected.
// Params is capability-specific (closed shapes, validated by
// ParseRuntimeParams): research capabilities take NO params; mode.request
// takes {"mode"}; compute.reserve/release take {"allocation_id"}.
type barCommandExecuteRequest struct {
	CommandID string          `json:"command_id"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// barExecutionView is the user-visible execution status (composed onto
// command views as the additive "execution" field, and listed at
// /api/bar/executions).
type barExecutionView struct {
	ID          string `json:"id"`
	CommandID   string `json:"command_id"`
	AgentID     string `json:"agent_id"`
	Capability  string `json:"capability"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	SessionID   string `json:"session_id"`
	Runtime     string `json:"runtime"`
	State       string `json:"state"`
	Allow       bool   `json:"allow"`
	Outcome     string `json:"outcome,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Intent      string `json:"intent"`
	ExecutedAt  string `json:"executed_at"`
}

func viewFromExecution(e agentfabric.ExecutionRecord) barExecutionView {
	return barExecutionView{
		ID:          e.ID,
		CommandID:   e.CommandID,
		AgentID:     string(e.AgentID),
		Capability:  string(e.Capability),
		WorkspaceID: e.WorkspaceID,
		SessionID:   e.SessionID,
		Runtime:     string(e.Runtime),
		State:       string(e.State),
		Allow:       e.Allow,
		Outcome:     e.Outcome,
		Reason:      e.Reason,
		Intent:      e.Intent,
		ExecutedAt:  e.ExecutedAt.Format(time.RFC3339),
	}
}

// ---------------------------------------------------------------------------
// Runtime acts (deterministic — no LLM in the decision OR act path)
// ---------------------------------------------------------------------------

// runtimeAct runs the capability's act and returns the bounded outcome
// line. The fresh evaluation has ALREADY allowed the capability; the act
// itself can still fail (bridge rejection, disabled dependency) — an
// error produces a FAILED record, never an HTTP error (a failed act is
// an honest terminal outcome the bar surfaces).
//
//   - research acts observe and report (status snapshot, gaming inventory
//     snapshot, compute plan dry-run — read-only by construction);
//   - coding acts perform through the EXISTING bridged surfaces: the mode
//     bridge (desired-mode write) and the workload bridge (compute
//     reserve/release under the agent's registry-declared owner, the
//     AK-5 composition — never a caller-supplied principal).
func (h *Handler) runtimeAct(cmd agentfabric.CommandRecord, params agentfabric.RuntimeParams) (string, error) {
	switch cmd.Capability {
	case agentkernel.CapabilityStatusRead:
		s := status.ReadOrStale()
		return fmt.Sprintf("status snapshot: mode=%s timestamp=%s stale=%v (read-only)", s.Mode, s.Timestamp, s.Stale), nil
	case agentkernel.CapabilityGamingRead:
		s := status.ReadOrStale()
		return fmt.Sprintf("gaming inventory snapshot: %d entries, timestamp=%s stale=%v (read-only)", len(s.Gaming), s.Timestamp, s.Stale), nil
	case agentkernel.CapabilityComputePlan:
		s := status.ReadOrStale()
		return fmt.Sprintf("compute plan dry-run: %d GPUs visible, mode=%s — no allocation created (compute.plan is side-effect free by construction)", len(s.GPUs), s.Mode), nil
	case agentkernel.CapabilityModeRequest:
		if h.mode == nil {
			return "", errors.New("mode bridge unavailable — the mode request act cannot run")
		}
		if err := h.mode.Request(params.Mode); err != nil {
			return "", fmt.Errorf("mode bridge rejected the request: %v", err)
		}
		return fmt.Sprintf("mode request accepted: desired-mode=%s (the host bridge applies it on its next tick)", params.Mode), nil
	case agentkernel.CapabilityComputeReserve, agentkernel.CapabilityComputeRelease:
		return h.computeBridgeAct(cmd, params)
	default:
		// Unreachable: the closed mapping is total and the execute surface
		// validated the capability. Fail closed regardless.
		return "", fmt.Errorf("no act is wired for capability %q — the closed mapping must be extended deliberately", cmd.Capability)
	}
}

// computeBridgeAct runs the compute reserve/release act through the
// workload bridge under the agent's REGISTRY-DECLARED owner (the AK-5
// composition — a caller-supplied principal is exactly what it prevents).
// The bridge enforces allocation ownership; the act reports the bounded
// outcome line.
func (h *Handler) computeBridgeAct(cmd agentfabric.CommandRecord, params agentfabric.RuntimeParams) (string, error) {
	if h.computeManager == nil {
		return "", errors.New("compute lifecycle is disabled (SCARLIHQ_COMPUTE_ENABLED=true required) — the compute act cannot run")
	}
	if h.agentGrants == nil {
		return "", errors.New("compute execution requires the grant registry (SCARLIHQ_AGENT_GRANTS_FILE + SCARLIHQ_AGENT_PRINCIPALS) — the acting principal must be admin-verified")
	}
	owner, ok := h.agentGrants.AgentOwner(cmd.AgentID)
	if !ok || strings.TrimSpace(owner) == "" {
		return "", errors.New("agent owner resolution failed — the agent has no registry-declared owner principal")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var result workload.Result
	var err error
	action := "reserve"
	if cmd.Capability == agentkernel.CapabilityComputeReserve {
		result, err = h.computeManager.Start(ctx, params.AllocationID, owner)
	} else {
		action = "release"
		result, err = h.computeManager.Stop(ctx, params.AllocationID, owner)
	}
	if err != nil {
		return "", fmt.Errorf("workload bridge rejected the %s: %v", action, err)
	}
	if action == "release" {
		// The honest AK-5/direct-stop contract: cleanup verified, the
		// reservation bundle stays RELEASING until reconciliation.
		return fmt.Sprintf("allocation %s release requested: cleanup verified, reservation pending reconciliation (principal=%s)", result.AllocationID, owner), nil
	}
	return fmt.Sprintf("allocation %s reserved: state=%s workload_ref=%s owned=%v (principal=%s)", result.AllocationID, result.State, result.WorkloadRef, result.WorkloadOwned, owner), nil
}

// ---------------------------------------------------------------------------
// POST /api/bar/commands/execute
// ---------------------------------------------------------------------------

// barCommandExecute executes a DECIDED, kernel-ALLOWED command under its
// runtime, with a FRESH kernel evaluation first (the stale-allow guard).
// Response semantics:
//
//	201 + COMPLETED view — the act ran and succeeded
//	201 + FAILED view    — the fresh evaluation allowed, the act failed
//	201 + SKIPPED view   — the fresh evaluation DENIED (audited; the act never ran)
//	400 — malformed command id / malformed params / unknown fields
//	404 — unknown command id
//	409 — not DECIDED, DENIED, already executed, ARCHIVED scope, vanished session
//	503 — fabric/kernel/executions not configured
func (h *Handler) barCommandExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.executionsDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &barCommandExecuteRequest{} })
	if !ok {
		return
	}
	req := request.(*barCommandExecuteRequest)

	// CommandID SHAPE first (the filter precedent: a malformed id is a
	// 400 client error; a well-formed UNKNOWN id is a 404 — never the
	// other way around).
	id := agentfabric.CommandID(strings.TrimSpace(req.CommandID))
	if err := id.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	cmd, exists := h.agentFabric.Get(id)
	if !exists {
		writeJSONError(w, http.StatusNotFound, "unknown command")
		return
	}

	// State gate: only a DECIDED, ALLOWED command executes.
	if cmd.State != agentfabric.CommandDecided {
		writeJSONError(w, http.StatusConflict, "command is "+string(cmd.State)+" — only DECIDED commands execute (route/decide it first)")
		return
	}
	if cmd.Decision == nil || !cmd.Decision.Allow {
		writeJSONError(w, http.StatusConflict, "command is DECIDED with allow=false — a denial is not executed (capture a new intent if the policy changes)")
		return
	}

	// One execution per command, ever (terminal records, no in-flight).
	if _, already := h.agentExecutions.GetByCommand(id); already {
		writeJSONError(w, http.StatusConflict, "command already executed — the execution record is terminal (capture a new intent to retry)")
		return
	}

	// ARCHIVED scope closes execution (the tombstone contract that closes
	// routing; live commands stay visible and cancellable, never
	// executable inside a closed context).
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

	// Closed params shapes (research: none; mode.request: mode; compute:
	// allocation_id). Unknown/malformed fields are 400s.
	params, err := agentfabric.ParseRuntimeParams(cmd.Capability, req.Params)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// The runtime is closed and total (validated at load; re-resolved
	// here — the record carries the runtime it executed under).
	runtimeID, ok := agentfabric.RuntimeForCapability(cmd.Capability)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "capability has no runtime mapping — the closed mapping drifted (fail-closed)")
		return
	}

	// ------------------------------------------------------------------
	// STALE-ALLOW GUARD: a FRESH kernel evaluation. The command's
	// decision may be arbitrarily old; the world moved. The fresh
	// evaluation (registry policy when configured — runtime kill-switches
	// apply NOW) is the one that counts.
	// ------------------------------------------------------------------
	now := time.Now().UTC()
	policy := h.agentPolicy
	if h.agentGrants != nil {
		policy = h.agentGrants.Policy()
	}
	event, decision, err := h.agentStore.Evaluate(cmd.SessionID, policy, cmd.Capability, now)
	if err != nil {
		if errors.Is(err, agentkernel.ErrUnknownSession) {
			writeJSONError(w, http.StatusConflict, "session vanished before execution — the command is DECIDED but its session no longer exists (fail-closed; no execution record)")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "fresh evaluation failed")
		return
	}

	// AK-4 discipline carried to executions: the fresh evaluation (allow
	// AND deny) is persisted to the audit sink BEFORE any execution
	// record exists (an unauditable execution decision is never recorded).
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
		// Fresh DENIAL → audited SKIPPED. The act never runs; the record
		// names the fresh rule that fired.
		rec = agentfabric.ExecutionRecord{
			ID:          execID,
			CommandID:   cmd.ID,
			AgentID:     cmd.AgentID,
			Capability:  cmd.Capability,
			WorkspaceID: cmd.WorkspaceID,
			SessionID:   cmd.SessionID,
			Runtime:     runtimeID,
			State:       agentfabric.ExecutionSkipped,
			Allow:       false,
			Reason:      decision.Reason,
			Intent:      cmd.Intent,
			ExecutedAt:  now,
		}
	} else {
		// Fresh ALLOW → run the act. A failed act is an honest terminal
		// outcome (FAILED), never an HTTP error: the bar surfaces it.
		outcome, actErr := h.runtimeAct(cmd, params)
		if actErr != nil {
			rec = agentfabric.ExecutionRecord{
				ID:          execID,
				CommandID:   cmd.ID,
				AgentID:     cmd.AgentID,
				Capability:  cmd.Capability,
				WorkspaceID: cmd.WorkspaceID,
				SessionID:   cmd.SessionID,
				Runtime:     runtimeID,
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
				Runtime:     runtimeID,
				State:       agentfabric.ExecutionCompleted,
				Allow:       true,
				Outcome:     truncateRunes(outcome, agentfabric.MaxOutcomeRunes),
				Reason:      decision.Reason,
				Intent:      cmd.Intent,
				ExecutedAt:  time.Now().UTC(),
			}
		}
	}

	// Persist the terminal record (atomic, rollback on failure) BEFORE
	// the response is served — a served view always exists on disk.
	saved, err := h.agentExecutions.Record(rec)
	if err != nil {
		switch {
		case errors.Is(err, agentfabric.ErrAlreadyExecuted):
			writeJSONError(w, http.StatusConflict, "command already executed — the execution record is terminal")
		case errors.Is(err, agentfabric.ErrExecutionLimit):
			writeJSONError(w, http.StatusServiceUnavailable, "execution store at capacity — archive and rotate the executions file deliberately")
		case errors.Is(err, agentfabric.ErrExecutionRNG):
			writeJSONError(w, http.StatusInternalServerError, "execution id generation failed")
		default:
			writeJSONError(w, http.StatusInternalServerError, "execution persist failed — the act "+map[bool]string{true: "ran", false: "did not run"}[decision.Allow]+" but no record was written (fail-closed)")
		}
		return
	}

	// Change signal (drop-not-block; the polling fallback covers a drop).
	h.agentBus.Publish(agentfabric.Signal{
		Type: agentfabric.SignalExecutionRecorded,
		Ref:  saved.ID,
		At:   time.Now().UTC(),
	})

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, viewFromExecution(saved))
}

// truncateRunes bounds a string to max runes (acts and bridge errors can
// exceed the record's bounds — the record never holds what it would
// refuse to load; truncation is the honest boundary, the full text lives
// in the audit/log trails).
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// ---------------------------------------------------------------------------
// GET /api/bar/executions
// ---------------------------------------------------------------------------

// barExecutions lists terminal execution records, newest-first, bounded
// (?limit=1..200, default 200 — the commands read surface's discipline),
// with optional closed filters:
//
//	?state=COMPLETED|FAILED|SKIPPED   (unknown → 400)
//	?command_id=<cmd-…>               (malformed → 400; unknown command → 404)
//
// Counts (per state, all states always present) are always included.
func (h *Handler) barExecutions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	if h.executionsDisabled(w) {
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
	var stateFilter agentfabric.ExecutionState
	if stateRaw != "" {
		switch agentfabric.ExecutionState(stateRaw) {
		case agentfabric.ExecutionCompleted, agentfabric.ExecutionFailed, agentfabric.ExecutionSkipped:
			stateFilter = agentfabric.ExecutionState(stateRaw)
		default:
			writeJSONError(w, http.StatusBadRequest, "state must be one of COMPLETED, FAILED, SKIPPED (closed enumeration)")
			return
		}
	}

	// Command filter: shape first (400), then existence in the command
	// store (404 — a typo'd id is a client error, not a silently-empty
	// list; a command that exists but was never executed IS an empty
	// list, the honest answer).
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

	var records []agentfabric.ExecutionRecord
	switch {
	case commandFilter != "" && stateFilter != "":
		rec, ok := h.agentExecutions.GetByCommand(commandFilter)
		if ok && rec.State == stateFilter {
			records = []agentfabric.ExecutionRecord{rec}
		} else {
			records = []agentfabric.ExecutionRecord{}
		}
	case commandFilter != "":
		if rec, ok := h.agentExecutions.GetByCommand(commandFilter); ok {
			records = []agentfabric.ExecutionRecord{rec}
		} else {
			records = []agentfabric.ExecutionRecord{}
		}
	case stateFilter != "":
		records = h.agentExecutions.ListLastState(limit, stateFilter)
	default:
		records = h.agentExecutions.ListLast(limit)
	}

	views := make([]barExecutionView, 0, len(records))
	for _, rec := range records {
		views = append(views, viewFromExecution(rec))
	}
	writeJSON(w, map[string]interface{}{
		"executions": views,
		"count":      len(views),
		"counts":     h.agentExecutions.Counts(),
		"limit":      limit,
		"store_path": h.agentExecutions.Path(),
		"total":      h.agentExecutions.Count(),
	})
}
