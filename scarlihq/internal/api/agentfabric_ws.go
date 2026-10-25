package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentfabric"
	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

// BF-2 (v19.5.1): Agent Fabric workspace REST surface. A workspace is a
// named isolation context: kernel sessions attach to it (at most one
// workspace per session) and commands submitted into it route ONLY
// through its member sessions. Unscoped commands keep the exact BF-1
// behavior — the surface is strictly additive.
//
// Design rules (the BF-1 set, unchanged):
//   - every route is behind the standard Bearer auth middleware (h.auth);
//   - the bar NEVER executes anything — routing still ends in a kernel
//     evaluation (routeAndDecide); workspaces only narrow WHICH sessions
//     are routable;
//   - the workspace store is persisted (workspaces.json, the grants.json
//     discipline: fail-closed load, atomic persist, bounded, 0600/0700);
//   - ARCHIVED is a terminal tombstone: readable history, closed routing.
//     Submitting into an archived workspace is a 409 state conflict;
//     archiving an archived workspace is a 409; membership changes on an
//     archived workspace are 409s;
//   - existence of a NAMED session is the kernel's business: join rejects
//     unknown session IDs with a 400 (the store only checks the aks-
//     shape; the API layer resolves existence — separate concerns,
//     composed at the boundary);
//   - workspace acts (create/archive/join/leave) are NOT kernel audit
//     events: they are persisted topology, reviewable in workspaces.json
//     itself (the kernel audit sink stays for evaluations and admin acts
//     on grants — honestly scoped, no mixed semantics).

// ConfigureAgentWorkspaces wires the workspace store into the handler.
// The store is constructed and loaded by main.go whenever the fabric is
// enabled (workspaces ride the fabric opt-in — no separate flag).
func (h *Handler) ConfigureAgentWorkspaces(store *agentfabric.WorkspaceStore) error {
	if store == nil {
		return errors.New("api: agent fabric workspace store is required")
	}
	h.agentWorkspaces = store
	return nil
}

// workspacesDisabled reports (and writes) whether the workspace surface is
// unavailable: not configured (503) or the fabric itself is down (503 —
// workspaces without commands are half a feature; they arrive together).
func (h *Handler) workspacesDisabled(w http.ResponseWriter) bool {
	if h.agentWorkspaces == nil || h.agentFabric == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric workspaces are disabled")
		return true
	}
	if h.agentStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric requires the agent kernel (SCARLIHQ_AGENTKERNEL_ENABLED=true) — it routes intents through kernel sessions")
		return true
	}
	return false
}

type barWorkspaceCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type barWorkspaceArchiveRequest struct {
	WorkspaceID string `json:"workspace_id"`
}

type barWorkspaceJoinRequest struct {
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
}

// barWorkspaceView is the user-visible workspace status. Sessions carries
// the member session IDs (join order); commands_count comes from the
// command store (merged by the handler — the read surface composes the
// two stores, mutations never do).
type barWorkspaceView struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	State         string   `json:"state"`
	Sessions      []string `json:"sessions"`
	SessionCount  int      `json:"session_count"`
	CommandsCount int      `json:"commands_count"`
	CreatedAt     string   `json:"created_at"`
	ArchivedAt    string   `json:"archived_at,omitempty"`
	UpdatedAt     string   `json:"updated_at"`
}

func viewFromWorkspace(ws agentfabric.WorkspaceRecord, commandsCount int) barWorkspaceView {
	sessions := ws.Sessions
	if sessions == nil {
		sessions = []string{}
	}
	v := barWorkspaceView{
		ID:            ws.ID,
		Name:          ws.Name,
		Description:   ws.Description,
		State:         string(ws.State),
		Sessions:      sessions,
		SessionCount:  len(sessions),
		CommandsCount: commandsCount,
		CreatedAt:     ws.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     ws.UpdatedAt.Format(time.RFC3339),
	}
	if ws.ArchivedAt != nil {
		v.ArchivedAt = ws.ArchivedAt.Format(time.RFC3339)
	}
	return v
}

// barWorkspaceMutationError maps workspace store errors to HTTP statuses.
func (h *Handler) barWorkspaceMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agentfabric.ErrUnknownWorkspace):
		writeJSONError(w, http.StatusNotFound, "unknown workspace")
	case errors.Is(err, agentfabric.ErrWorkspaceState):
		writeJSONError(w, http.StatusConflict, err.Error())
	case errors.Is(err, agentfabric.ErrWorkspaceLimit):
		writeJSONError(w, http.StatusServiceUnavailable, "workspace store at capacity — archive and rotate the workspaces file deliberately")
	case errors.Is(err, agentfabric.ErrSessionLimit):
		writeJSONError(w, http.StatusServiceUnavailable, "workspace membership at capacity (32 sessions)")
	case errors.Is(err, agentfabric.ErrSessionAlreadyMember),
		errors.Is(err, agentfabric.ErrSessionClaimed),
		errors.Is(err, agentfabric.ErrSessionNotMember):
		// Membership conflicts are state conflicts → 409.
		writeJSONError(w, http.StatusConflict, err.Error())
	case errors.Is(err, agentfabric.ErrSessionShape):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, agentfabric.ErrWorkspaceRNG):
		writeJSONError(w, http.StatusInternalServerError, "workspace id generation failed")
	default:
		writeJSONError(w, http.StatusBadRequest, err.Error())
	}
}

// barWorkspaces handles GET (list, newest-first) and POST (create).
func (h *Handler) barWorkspaces(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if h.workspacesDisabled(w) {
			return
		}
		records := h.agentWorkspaces.List() // oldest-first (creation order)
		counts := h.agentFabric.WorkspaceCounts()
		views := make([]barWorkspaceView, 0, len(records))
		// Newest-first: reverse the creation order (stable, deterministic).
		for i := len(records) - 1; i >= 0; i-- {
			views = append(views, viewFromWorkspace(records[i], counts[records[i].ID]))
		}
		writeJSON(w, map[string]interface{}{
			"workspaces":                 views,
			"count":                      len(views),
			"total":                      len(views),
			"store_path":                 h.agentWorkspaces.Path(),
			"max_sessions_per_workspace": agentfabric.MaxSessionsPerWorkspace,
		})
	case http.MethodPost:
		h.barWorkspaceCreate(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "use GET or POST")
	}
}

func (h *Handler) barWorkspaceCreate(w http.ResponseWriter, r *http.Request) {
	if h.workspacesDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &barWorkspaceCreateRequest{} })
	if !ok {
		return
	}
	req := request.(*barWorkspaceCreateRequest)

	name := strings.TrimSpace(req.Name)
	description := strings.TrimSpace(req.Description)
	// Early validation for clean 400s (the store re-validates — defense in
	// depth; the store is the authority).
	if err := agentfabric.ValidateWorkspaceName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := agentfabric.ValidateWorkspaceDescription(description); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	ws, err := h.agentWorkspaces.Create(name, description, time.Now().UTC())
	if err != nil {
		h.barWorkspaceMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, viewFromWorkspace(ws, 0))
}

// barWorkspaceArchive tombstones a workspace (ACTIVE → ARCHIVED, terminal).
func (h *Handler) barWorkspaceArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.workspacesDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &barWorkspaceArchiveRequest{} })
	if !ok {
		return
	}
	req := request.(*barWorkspaceArchiveRequest)

	id := agentfabric.WorkspaceID(strings.TrimSpace(req.WorkspaceID))
	if err := id.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	ws, err := h.agentWorkspaces.Archive(id, time.Now().UTC())
	if err != nil {
		h.barWorkspaceMutationError(w, err)
		return
	}
	counts := h.agentFabric.WorkspaceCounts()
	writeJSON(w, viewFromWorkspace(ws, counts[ws.ID]))
}

// barWorkspaceJoin attaches a kernel session to an ACTIVE workspace. The
// session must EXIST in the kernel session store (resolved here — the
// store checks only the aks- shape) and must not be claimed by another
// workspace (one workspace per session).
func (h *Handler) barWorkspaceJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.workspacesDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &barWorkspaceJoinRequest{} })
	if !ok {
		return
	}
	req := request.(*barWorkspaceJoinRequest)

	id := agentfabric.WorkspaceID(strings.TrimSpace(req.WorkspaceID))
	if err := id.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	// Kernel resolution: existence is the kernel's business. ANY state is
	// accepted at join time (a DORMANT session may join; routing still
	// requires ACTIVE — the routing resolver checks state at route time).
	if _, ok := h.agentStore.Get(sessionID); !ok {
		writeJSONError(w, http.StatusBadRequest, "session does not exist in the agent kernel (create it via POST /api/agent/sessions)")
		return
	}

	ws, err := h.agentWorkspaces.JoinSession(id, sessionID, time.Now().UTC())
	if err != nil {
		h.barWorkspaceMutationError(w, err)
		return
	}
	counts := h.agentFabric.WorkspaceCounts()
	writeJSON(w, viewFromWorkspace(ws, counts[ws.ID]))
}

// barWorkspaceLeave detaches a member session from an ACTIVE workspace.
func (h *Handler) barWorkspaceLeave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.workspacesDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &barWorkspaceJoinRequest{} })
	if !ok {
		return
	}
	req := request.(*barWorkspaceJoinRequest)

	id := agentfabric.WorkspaceID(strings.TrimSpace(req.WorkspaceID))
	if err := id.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	ws, err := h.agentWorkspaces.LeaveSession(id, sessionID, time.Now().UTC())
	if err != nil {
		h.barWorkspaceMutationError(w, err)
		return
	}
	counts := h.agentFabric.WorkspaceCounts()
	writeJSON(w, viewFromWorkspace(ws, counts[ws.ID]))
}

// resolveWorkspaceScopedSession resolves the session a workspace-scoped
// command should route through: the named session (must exist, belong to
// the agent, be ACTIVE and be a MEMBER of the workspace) or, when none
// is named, the NEWEST ACTIVE member session of the agent. Returns ""
// when nothing satisfies all four conditions (the command stays
// SUBMITTED — intent capture must not fail on routability, the BF-1
// contract carried into scoped commands).
func (h *Handler) resolveWorkspaceScopedSession(ws agentfabric.WorkspaceRecord, agent agentkernel.AgentID, named string) string {
	sessions := h.agentStore.List() // kernel order: CreatedAt then ID (oldest-first)
	if named != "" {
		if !ws.IsMember(named) {
			return "" // not a member — not routable in this workspace
		}
		for _, rec := range sessions {
			if rec.Session.ID == named {
				if rec.Agent.ID == agent && rec.Session.State == agentkernel.SessionActive {
					return rec.Session.ID
				}
				return "" // member but not ACTIVE for this agent
			}
		}
		return "" // member but vanished from the kernel (impossible — never deleted)
	}
	resolved := ""
	for _, rec := range sessions {
		if rec.Agent.ID == agent && rec.Session.State == agentkernel.SessionActive && ws.IsMember(rec.Session.ID) {
			resolved = rec.Session.ID // oldest-first → last match = newest
		}
	}
	return resolved
}
