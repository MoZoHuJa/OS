package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

// AK-2: Agent Kernel REST surface — the first place the kernel becomes
// reachable. Design rules (docs/AGENT_KERNEL_AK1_DESIGN.md):
//
//   - every route is behind the standard Bearer auth middleware (h.auth);
//   - the surface is deliberately minimal: create/list/transition/evaluate.
//     There is no delete, no "reset", no capability mutation after creation —
//     grants are fixed for a session's lifetime (AK-3 introduces persisted,
//     admin-owned grants as a separate reviewed act);
//   - evaluate returns the Decision (200) for BOTH allow and deny — a denial
//     is a successful, auditable evaluation, not an HTTP error. Only
//     unknown sessions and malformed requests are 4xx;
//   - the owner principal in the create request is caller-supplied: the trust
//     boundary is the authenticated ScarliHQ principal (the bearer-token
//     holder — same trust level as /api/compute/start|stop). AK-3 ties grants
//     to the reservation governor's user_tokens principal space with
//     admin-owned persistence.
func (h *Handler) ConfigureAgentKernel(store *agentkernel.Store, policy agentkernel.Policy) error {
	if store == nil {
		return errors.New("api: agent kernel store is required")
	}
	h.agentStore = store
	h.agentPolicy = policy
	return nil
}

// agentSessionCreateRequest: one JSON object, unknown fields rejected.
type agentSessionCreateRequest struct {
	AgentID      string   `json:"agent_id"`
	Owner        string   `json:"owner"`
	Capabilities []string `json:"capabilities"`
}

type agentSessionView struct {
	SessionID    string   `json:"session_id"`
	AgentID      string   `json:"agent_id"`
	Owner        string   `json:"owner"`
	State        string   `json:"state"`
	Capabilities []string `json:"capabilities"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

func viewFromRecord(rec agentkernel.SessionRecord) agentSessionView {
	caps := make([]string, 0, len(rec.Caps))
	// Deterministic order: iterate the closed enumeration, not the map.
	for _, c := range agentkernel.AllCapabilities() {
		if rec.Caps.Has(c) {
			caps = append(caps, string(c))
		}
	}
	return agentSessionView{
		SessionID:    rec.Session.ID,
		AgentID:      string(rec.Agent.ID),
		Owner:        rec.Agent.Owner,
		State:        string(rec.Session.State),
		Capabilities: caps,
		CreatedAt:    rec.Session.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    rec.Session.UpdatedAt.Format(time.RFC3339),
	}
}

func (h *Handler) agentStoreDisabled(w http.ResponseWriter) bool {
	if h.agentStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent kernel sessions are disabled")
		return true
	}
	return false
}

// agentSessions handles GET (list) and POST (create).
func (h *Handler) agentSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if h.agentStoreDisabled(w) {
			return
		}
		sessions := h.agentStore.List()
		views := make([]agentSessionView, 0, len(sessions))
		for _, rec := range sessions {
			views = append(views, viewFromRecord(rec))
		}
		writeJSON(w, map[string]interface{}{
			"sessions": views,
			"count":    len(views),
		})
	case http.MethodPost:
		h.agentSessionCreate(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "use GET or POST")
	}
}

func (h *Handler) agentSessionCreate(w http.ResponseWriter, r *http.Request) {
	if h.agentStoreDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &agentSessionCreateRequest{} })
	if !ok {
		return
	}
	req := request.(*agentSessionCreateRequest)

	// Agent identity: validated by the kernel (typed ID + mandatory owner).
	agent, err := agentkernel.NewAgentIdentity(agentkernel.AgentID(req.AgentID), req.Owner, time.Now().UTC())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Capability set: closed enumeration — unknown capabilities (incl.
	// shell.exec and friends) are rejected here with 400.
	caps := make([]agentkernel.Capability, 0, len(req.Capabilities))
	for _, c := range req.Capabilities {
		caps = append(caps, agentkernel.Capability(c))
	}
	set, err := agentkernel.NewCapabilitySet(caps...)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	rec, err := h.agentStore.Create(agent, set, time.Now().UTC())
	if err != nil {
		if errors.Is(err, agentkernel.ErrSessionLimit) {
			writeJSONError(w, http.StatusServiceUnavailable, "session store at capacity")
			return
		}
		if errors.Is(err, agentkernel.ErrRNG) {
			writeJSONError(w, http.StatusInternalServerError, "session id generation failed")
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, viewFromRecord(rec))
}

type agentSessionTransitionRequest struct {
	SessionID string `json:"session_id"`
	To        string `json:"to"`
}

func (h *Handler) agentSessionTransition(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.agentStoreDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &agentSessionTransitionRequest{} })
	if !ok {
		return
	}
	req := request.(*agentSessionTransitionRequest)

	to := agentkernel.SessionState(strings.TrimSpace(req.To))
	switch to {
	case agentkernel.SessionActive, agentkernel.SessionSuspended, agentkernel.SessionClosed:
		// legal targets — everything else is rejected below
	default:
		writeJSONError(w, http.StatusBadRequest, "transition target must be ACTIVE, SUSPENDED or CLOSED")
		return
	}

	rec, err := h.agentStore.Transition(strings.TrimSpace(req.SessionID), to, time.Now().UTC())
	if err != nil {
		if errors.Is(err, agentkernel.ErrUnknownSession) {
			writeJSONError(w, http.StatusNotFound, "unknown session")
			return
		}
		// Illegal transition / terminal state / backwards clock → 409
		// (state conflict, exactly like the allocation lifecycle surface).
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, viewFromRecord(rec))
}

type agentSessionEvaluateRequest struct {
	SessionID  string `json:"session_id"`
	Capability string `json:"capability"`
}

func (h *Handler) agentSessionEvaluate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.agentStoreDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &agentSessionEvaluateRequest{} })
	if !ok {
		return
	}
	req := request.(*agentSessionEvaluateRequest)

	capability := agentkernel.Capability(strings.TrimSpace(req.Capability))
	if !capability.IsValid() {
		writeJSONError(w, http.StatusBadRequest, "unknown capability (closed enumeration)")
		return
	}

	// Denials are 200 responses with allow=false — a denial is a successful,
	// auditable evaluation. Unknown session is a 404 client error.
	event, decision, err := h.agentStore.Evaluate(strings.TrimSpace(req.SessionID), h.agentPolicy, capability, time.Now().UTC())
	if err != nil {
		if errors.Is(err, agentkernel.ErrUnknownSession) {
			writeJSONError(w, http.StatusNotFound, "unknown session")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "evaluation failed")
		return
	}
	writeJSON(w, map[string]interface{}{
		"session_id": event.SessionID,
		"agent_id":   string(event.AgentID),
		"capability": string(event.Capability),
		"allow":      decision.Allow,
		"reason":     decision.Reason,
		"timestamp":  event.Timestamp.Format(time.RFC3339),
	})
}

// decodeOneJSON decodes exactly one JSON object with unknown-field rejection
// and a bounded body — the compute-lifecycle request discipline.
func decodeOneJSON(w http.ResponseWriter, r *http.Request, factory func() any) (any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	target := factory()
	if err := decoder.Decode(target); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON request")
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "request must contain exactly one JSON object")
		return nil, false
	}
	return target, true
}
