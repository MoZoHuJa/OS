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

// BF-1 (v19.5.0): Agent Fabric REST surface — the ScarliBar backend.
// Design rules (docs/AGENT_FABRIC_BF1_DESIGN.md):
//
//   - every route is behind the standard Bearer auth middleware (h.auth);
//   - ScarliBar owns intent capture and user-visible command status (P4);
//     it never executes anything itself — the capability question goes to
//     the deterministic Agent Kernel, and the decision (allow AND deny) is
//     recorded on the command (the kernel's denial-is-200 contract);
//   - an intent is never lost: submission persists the command even when
//     no ACTIVE session exists yet (SUBMITTED / "awaiting routing");
//   - ordering discipline (the AK-4 contract extended to the fabric): the
//     kernel decision is persisted to the audit sink BEFORE the command
//     transitions to DECIDED — an unauditable decision is never served.
//     A commands.json persist failure AFTER a successful audit append
//     leaves the command ROUTED (honest intermediate state — no
//     cross-file atomicity is claimed); re-routing re-evaluates and
//     appends a fresh audit line (reconciliation, surfaced in the design).
//   - bounded reads (?limit=1..200, default 200, newest-first — the audit
//     read surface's discipline); the FILE is the history, only the read
//     surface is bounded.
func (h *Handler) ConfigureAgentFabric(store *agentfabric.CommandStore) error {
	if store == nil {
		return errors.New("api: agent fabric command store is required")
	}
	h.agentFabric = store
	return nil
}

// agentFabricDisabled reports (and writes) whether the fabric surface is
// unavailable: not configured (503) or configured without the kernel (503 —
// the fabric routes through the Agent Kernel, it has no standalone mode).
func (h *Handler) agentFabricDisabled(w http.ResponseWriter) bool {
	if h.agentFabric == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric is disabled")
		return true
	}
	if h.agentStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric requires the agent kernel (SCARLIHQ_AGENTKERNEL_ENABLED=true) — it routes intents through kernel sessions")
		return true
	}
	return false
}

// barCommandSubmitRequest: one JSON object, unknown fields rejected.
// SessionID is optional: empty means "resolve the newest ACTIVE session
// of the agent"; absent session → the command is captured SUBMITTED
// (awaiting routing — the intent is never lost). WorkspaceID (BF-2) is
// optional: empty = unscoped (the BF-1 behavior); non-empty = the command
// is submitted into that workspace's isolation scope and routes ONLY
// through its member sessions.
type barCommandSubmitRequest struct {
	Intent      string `json:"intent"`
	AgentID     string `json:"agent_id"`
	Capability  string `json:"capability"`
	SessionID   string `json:"session_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type barCommandRouteRequest struct {
	CommandID string `json:"command_id"`
}

type barCommandCancelRequest struct {
	CommandID string `json:"command_id"`
}

// barCommandView is the user-visible command status (P4: ScarliBar's
// ownership). Decision fields are present exactly when state=DECIDED.
// WorkspaceID is present exactly when the command is workspace-scoped.
type barCommandView struct {
	ID          string `json:"id"`
	Intent      string `json:"intent"`
	AgentID     string `json:"agent_id"`
	Capability  string `json:"capability"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	State       string `json:"state"`
	Allow       *bool  `json:"allow,omitempty"`
	Reason      string `json:"reason,omitempty"`
	SubmittedAt string `json:"submitted_at"`
	RoutedAt    string `json:"routed_at,omitempty"`
	DecidedAt   string `json:"decided_at,omitempty"`
	CancelledAt string `json:"cancelled_at,omitempty"`
	UpdatedAt   string `json:"updated_at"`
}

func viewFromCommand(c agentfabric.CommandRecord) barCommandView {
	v := barCommandView{
		ID:          c.ID,
		Intent:      c.Intent,
		AgentID:     string(c.AgentID),
		Capability:  string(c.Capability),
		WorkspaceID: c.WorkspaceID,
		SessionID:   c.SessionID,
		State:       string(c.State),
		SubmittedAt: c.SubmittedAt.Format(time.RFC3339),
		UpdatedAt:   c.UpdatedAt.Format(time.RFC3339),
	}
	if c.RoutedAt != nil {
		v.RoutedAt = c.RoutedAt.Format(time.RFC3339)
	}
	if c.DecidedAt != nil {
		v.DecidedAt = c.DecidedAt.Format(time.RFC3339)
	}
	if c.CancelledAt != nil {
		v.CancelledAt = c.CancelledAt.Format(time.RFC3339)
	}
	if c.Decision != nil {
		allow := c.Decision.Allow
		v.Allow = &allow
		v.Reason = c.Decision.Reason
	}
	return v
}

// resolveActiveSession deterministically resolves the session a command
// should route through: the named session (validated: must exist, belong
// to the agent and be ACTIVE) or, when none is named, the NEWEST ACTIVE
// session of the agent (kernel List order: CreatedAt then ID — the last
// ACTIVE match). Returns "" when no ACTIVE session exists (the command
// stays SUBMITTED — intent capture must not fail on that).
func (h *Handler) resolveActiveSession(agent agentkernel.AgentID, named string) string {
	sessions := h.agentStore.List()
	if named != "" {
		for _, rec := range sessions {
			if rec.Session.ID == named {
				if rec.Agent.ID == agent && rec.Session.State == agentkernel.SessionActive {
					return rec.Session.ID
				}
				return "" // named session exists but is not routable for this intent
			}
		}
		return "" // unknown named session
	}
	resolved := ""
	for _, rec := range sessions {
		if rec.Agent.ID == agent && rec.Session.State == agentkernel.SessionActive {
			resolved = rec.Session.ID // List is oldest-first → last ACTIVE = newest
		}
	}
	return resolved
}

// routeAndDecide routes the command to the given session and records the
// kernel's decision. Ordering: kernel Evaluate → audit append (fail-closed
// — an unauditable decision is never served; the command stays ROUTED and
// can be re-routed later) → DECIDED persist. Registry policy applies when
// the grant registry is configured (runtime kill-switches take effect on
// the next evaluation — no restart).
func (h *Handler) routeAndDecide(w http.ResponseWriter, id agentfabric.CommandID, sessionID string) (agentfabric.CommandRecord, bool) {
	now := time.Now().UTC()
	routed, err := h.agentFabric.Route(id, sessionID, now)
	if err != nil {
		h.barCommandMutationError(w, err)
		return agentfabric.CommandRecord{}, false
	}

	policy := h.agentPolicy
	if h.agentGrants != nil {
		policy = h.agentGrants.Policy()
	}
	event, decision, err := h.agentStore.Evaluate(sessionID, policy, routed.Capability, now)
	if err != nil {
		// The session vanished between resolution and evaluation (kernel
		// sessions are never deleted — this is only possible for a stale
		// ID; treat as a client error, the command stays ROUTED).
		if errors.Is(err, agentkernel.ErrUnknownSession) {
			writeJSONError(w, http.StatusBadRequest, "session vanished before evaluation — re-route the command")
			return agentfabric.CommandRecord{}, false
		}
		writeJSONError(w, http.StatusInternalServerError, "evaluation failed")
		return agentfabric.CommandRecord{}, false
	}

	// AK-4 discipline extended to the fabric: persist the decision to the
	// audit sink BEFORE it becomes the command's DECIDED state (before it
	// is served). A failure leaves the command ROUTED — honest, retryable.
	if h.agentAudit != nil {
		if err := h.agentAudit.Append(agentkernel.NewAuditLineFromEvent(event)); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "audit write failed — decision not served (fail-closed); the command stays ROUTED, re-route to retry")
			return agentfabric.CommandRecord{}, false
		}
	}

	decided, err := h.agentFabric.Decide(id, decision, now)
	if err != nil {
		h.barCommandMutationError(w, err)
		return agentfabric.CommandRecord{}, false
	}
	return decided, true
}

// barCommandMutationError maps fabric store errors to HTTP statuses.
func (h *Handler) barCommandMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agentfabric.ErrUnknownCommand):
		writeJSONError(w, http.StatusNotFound, "unknown command")
	case errors.Is(err, agentfabric.ErrCommandState):
		// Illegal transition / terminal state / backwards clock → 409
		// (state conflict — the kernel transition surface's discipline).
		writeJSONError(w, http.StatusConflict, err.Error())
	case errors.Is(err, agentfabric.ErrCommandLimit):
		writeJSONError(w, http.StatusServiceUnavailable, "command store at capacity — archive and rotate the commands file deliberately")
	case errors.Is(err, agentfabric.ErrRNG):
		writeJSONError(w, http.StatusInternalServerError, "command id generation failed")
	default:
		writeJSONError(w, http.StatusBadRequest, err.Error())
	}
}

// barCommands handles GET (list, newest-first, bounded; optional
// workspace_id filter) and POST (submit).
func (h *Handler) barCommands(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if h.agentFabricDisabled(w) {
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
		// BF-2: optional workspace scope filter. "" (absent) = all commands
		// (the BF-1 surface). A well-formed non-empty id selects that
		// workspace's commands — the special literal "unscoped" selects the
		// unscoped namespace (the dashboard's "— unscoped —" view).
		scope := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
		var scopeID agentfabric.WorkspaceID
		switch {
		case scope == "":
			// all commands
		case scope == "unscoped":
			scopeID = agentfabric.WorkspaceID("") // sentinel match on empty scope
		default:
			scopeID = agentfabric.WorkspaceID(scope)
			if err := scopeID.Validate(); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			// The filter must reference a real workspace — a typo'd id is a
			// client error, not an silently-empty list.
			if h.agentWorkspaces == nil {
				writeJSONError(w, http.StatusServiceUnavailable, "agent fabric workspaces are disabled")
				return
			}
			if _, ok := h.agentWorkspaces.Get(scopeID); !ok {
				writeJSONError(w, http.StatusNotFound, "unknown workspace")
				return
			}
		}
		if scope == "" {
			records := h.agentFabric.ListLast(limit)
			views := make([]barCommandView, 0, len(records))
			for _, rec := range records {
				views = append(views, viewFromCommand(rec))
			}
			writeJSON(w, map[string]interface{}{
				"commands":   views,
				"count":      len(views),
				"counts":     h.agentFabric.Counts(),
				"limit":      limit,
				"store_path": h.agentFabric.Path(),
				"total":      h.agentFabric.Count(),
			})
			return
		}
		records := h.agentFabric.ListLastWorkspace(limit, scopeID)
		views := make([]barCommandView, 0, len(records))
		for _, rec := range records {
			views = append(views, viewFromCommand(rec))
		}
		writeJSON(w, map[string]interface{}{
			"commands":   views,
			"count":      len(views),
			"counts":     h.agentFabric.CountsWorkspace(scopeID),
			"limit":      limit,
			"store_path": h.agentFabric.Path(),
			"total":      h.agentFabric.CountWorkspace(scopeID),
		})
	case http.MethodPost:
		h.barCommandSubmit(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "use GET or POST")
	}
}

func (h *Handler) barCommandSubmit(w http.ResponseWriter, r *http.Request) {
	if h.agentFabricDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &barCommandSubmitRequest{} })
	if !ok {
		return
	}
	req := request.(*barCommandSubmitRequest)

	intent := strings.TrimSpace(req.Intent)
	agent := agentkernel.AgentID(strings.TrimSpace(req.AgentID))
	capability := agentkernel.Capability(strings.TrimSpace(req.Capability))
	namedSession := strings.TrimSpace(req.SessionID)
	workspaceID := agentfabric.WorkspaceID(strings.TrimSpace(req.WorkspaceID))

	// Early validation for clean 400s (the store re-validates — defense
	// in depth; the store is the authority).
	if err := agentfabric.ValidateIntent(intent); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := agent.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !capability.IsValid() {
		writeJSONError(w, http.StatusBadRequest, "unknown capability (closed enumeration)")
		return
	}
	if namedSession != "" {
		if _, ok := h.agentStore.Get(namedSession); !ok {
			writeJSONError(w, http.StatusBadRequest, "named session does not exist")
			return
		}
	}
	// BF-2: a workspace-scoped submission requires a REAL, ACTIVE
	// workspace — checked BEFORE the intent is captured (a bad scope is a
	// client error, like a bad capability; the never-lost contract covers
	// routability, not malformed requests). An ARCHIVED workspace is a
	// 409 state conflict: the isolation context is closed.
	var scoped *agentfabric.WorkspaceRecord
	if workspaceID != "" {
		if err := workspaceID.Validate(); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if h.agentWorkspaces == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "agent fabric workspaces are disabled")
			return
		}
		ws, ok := h.agentWorkspaces.Get(workspaceID)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "unknown workspace")
			return
		}
		if ws.State == agentfabric.WorkspaceArchived {
			writeJSONError(w, http.StatusConflict, "workspace "+ws.ID+" is ARCHIVED — it accepts no new commands (its history stays visible)")
			return
		}
		if namedSession != "" && !ws.IsMember(namedSession) {
			writeJSONError(w, http.StatusBadRequest, "named session is not a member of workspace "+ws.ID+" — join it first (POST /api/bar/workspaces/sessions)")
			return
		}
		scoped = &ws
	}

	// 1. Capture the intent — persisted BEFORE any routing attempt. An
	//    intent is never lost, even when nothing is routable right now.
	cmd, err := h.agentFabric.Submit(intent, agent, capability, workspaceID, time.Now().UTC())
	if err != nil {
		h.barCommandMutationError(w, err)
		return
	}
	id := agentfabric.CommandID(cmd.ID)

	// 2. Resolve a routable session (named or newest ACTIVE; scoped
	//    commands resolve only MEMBER sessions). A named session that is
	//    not routable for this intent is a client error — the captured
	//    intent stays SUBMITTED (re-route when live).
	var sessionID string
	if scoped != nil {
		sessionID = h.resolveWorkspaceScopedSession(*scoped, agent, namedSession)
	} else {
		sessionID = h.resolveActiveSession(agent, namedSession)
	}
	if namedSession != "" && sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "named session is not an ACTIVE session of this agent — the intent was captured as "+cmd.ID+" (SUBMITTED); re-route when a session is active")
		return
	}

	// 3. No routable session: the intent is captured, status visible.
	if sessionID == "" {
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, viewFromCommand(cmd))
		return
	}

	// 4. Route + evaluate + decide, synchronously.
	decided, ok := h.routeAndDecide(w, id, sessionID)
	if !ok {
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, viewFromCommand(decided))
}

// barCommandRoute re-attempts routing+evaluation for a SUBMITTED (or
// ROUTED — re-bind) command: resolves the newest ACTIVE session of the
// command's agent (workspace-scoped commands resolve only MEMBER
// sessions of an ACTIVE workspace) and runs the full
// route→evaluate→decide chain.
func (h *Handler) barCommandRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.agentFabricDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &barCommandRouteRequest{} })
	if !ok {
		return
	}
	req := request.(*barCommandRouteRequest)

	id := agentfabric.CommandID(strings.TrimSpace(req.CommandID))
	cmd, exists := h.agentFabric.Get(id)
	if !exists {
		writeJSONError(w, http.StatusNotFound, "unknown command")
		return
	}
	if cmd.State == agentfabric.CommandDecided || cmd.State == agentfabric.CommandCancelled {
		writeJSONError(w, http.StatusConflict, "command is "+string(cmd.State)+" (terminal) — only SUBMITTED or ROUTED commands can be routed")
		return
	}

	// BF-2: a workspace-scoped command routes only inside its scope — and
	// only while that scope is ACTIVE. An ARCHIVED workspace closes
	// routing for its live commands (they stay visible and cancellable).
	var sessionID string
	if cmd.WorkspaceID != "" {
		if h.agentWorkspaces == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "agent fabric workspaces are disabled")
			return
		}
		wsID := agentfabric.WorkspaceID(cmd.WorkspaceID)
		ws, ok := h.agentWorkspaces.Get(wsID)
		if !ok {
			// Cannot happen through the API (submission validates existence)
			// — but a hand-edited commands file could produce it. Fail
			// closed: routing into a vanished scope is refused.
			writeJSONError(w, http.StatusConflict, "workspace "+cmd.WorkspaceID+" does not exist — routing refused (fail-closed)")
			return
		}
		if ws.State == agentfabric.WorkspaceArchived {
			writeJSONError(w, http.StatusConflict, "workspace "+ws.ID+" is ARCHIVED — routing is closed (the command stays "+string(cmd.State)+", cancellable, visible)")
			return
		}
		sessionID = h.resolveWorkspaceScopedSession(ws, cmd.AgentID, "")
		if sessionID == "" {
			writeJSONError(w, http.StatusConflict, "no ACTIVE member session for agent "+string(cmd.AgentID)+" in workspace "+ws.ID+" — join a session (POST /api/bar/workspaces/sessions) or activate one")
			return
		}
	} else {
		sessionID = h.resolveActiveSession(cmd.AgentID, "")
		if sessionID == "" {
			writeJSONError(w, http.StatusConflict, "no ACTIVE session for agent "+string(cmd.AgentID)+" — start/activate a session first (POST /api/agent/sessions/transition)")
			return
		}
	}

	decided, ok := h.routeAndDecide(w, id, sessionID)
	if !ok {
		return
	}
	writeJSON(w, viewFromCommand(decided))
}

// barCommandCancel withdraws a command before a decision.
func (h *Handler) barCommandCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.agentFabricDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &barCommandCancelRequest{} })
	if !ok {
		return
	}
	req := request.(*barCommandCancelRequest)

	id := agentfabric.CommandID(strings.TrimSpace(req.CommandID))
	cmd, err := h.agentFabric.Cancel(id, time.Now().UTC())
	if err != nil {
		h.barCommandMutationError(w, err)
		return
	}
	writeJSON(w, viewFromCommand(cmd))
}
