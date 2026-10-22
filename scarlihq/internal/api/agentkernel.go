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

// AK-2 (v19.4.1): Agent Kernel REST surface — the first place the kernel becomes
// reachable. Design rules (docs/AGENT_KERNEL_AK1_DESIGN.md):
//
//   - every route is behind the standard Bearer auth middleware (h.auth);
//   - the surface is deliberately minimal: create/list/transition/evaluate.
//     There is no delete, no "reset" — grants are fixed for a session's
//     lifetime; AK-3 (v19.4.4) adds the persisted, admin-owned grant
//     registry as separate reviewed acts (grant/revoke/disable/enable);
//   - evaluate returns the Decision (200) for BOTH allow and deny — a denial
//     is a successful, auditable evaluation, not an HTTP error. Only
//     unknown sessions and malformed requests are 4xx;
//   - the owner principal in the create request is caller-supplied ONLY in
//     legacy mode (no registry configured). AK-3 registry mode replaces
//     this: the owner comes from the grant registry (the reservation
//     governor's user_tokens principal space), and the request MUST NOT
//     carry capabilities — grants are admin-owned, not request-supplied.
func (h *Handler) ConfigureAgentKernel(store *agentkernel.Store, policy agentkernel.Policy) error {
	if store == nil {
		return errors.New("api: agent kernel store is required")
	}
	h.agentStore = store
	h.agentPolicy = policy
	return nil
}

// ConfigureAgentGrants attaches the AK-3 persisted grant registry (admin-
// owned grants + disable switches). When set, session creation derives
// grants from the registry and evaluation consults the registry policy.
func (h *Handler) ConfigureAgentGrants(registry *agentkernel.GrantRegistry) error {
	if registry == nil {
		return errors.New("api: agent kernel grant registry is required")
	}
	h.agentGrants = registry
	return nil
}

// agentSessionCreateRequest: one JSON object, unknown fields rejected.
// AK-3: Capabilities is a POINTER so legacy mode can require it and registry
// mode can FORBID it — presence is distinguishable from absence.
type agentSessionCreateRequest struct {
	AgentID      string    `json:"agent_id"`
	Owner        string    `json:"owner"`
	Capabilities *[]string `json:"capabilities"`
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

	// AK-3 registry mode: grants are admin-owned — the request MUST NOT carry
	// a capabilities field, the owner MUST match the registry's declared
	// owner, and the capability set comes from the persisted grants.
	if h.agentGrants != nil {
		if req.Capabilities != nil {
			writeJSONError(w, http.StatusBadRequest, "capabilities are admin-owned (grant registry configured) — remove the capabilities field; grants are managed via /api/agent/grants")
			return
		}
		if strings.TrimSpace(req.Owner) != "" {
			declared, ok := h.agentGrants.AgentOwner(agentkernel.AgentID(strings.TrimSpace(req.AgentID)))
			if !ok {
				// Fall through to the unknown-agent rejection below (the agent
				// has no grants at all — nothing is granted implicitly).
				_ = declared
			} else if strings.TrimSpace(req.Owner) != declared {
				writeJSONError(w, http.StatusBadRequest, "owner mismatch: agent's declared owner is the registry principal — remove the owner field or pass the declared owner")
				return
			}
		}
		caps := h.agentGrants.GrantsFor(agentkernel.AgentID(strings.TrimSpace(req.AgentID)))
		if len(caps) == 0 {
			writeJSONError(w, http.StatusBadRequest, "agent has no active grants in the registry — nothing is granted implicitly; grant capabilities first via /api/agent/grants")
			return
		}
		owner, _ := h.agentGrants.AgentOwner(agentkernel.AgentID(strings.TrimSpace(req.AgentID)))
		agent, err := agentkernel.NewAgentIdentity(agentkernel.AgentID(strings.TrimSpace(req.AgentID)), owner, time.Now().UTC())
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		rec, err := h.agentStore.Create(agent, caps, time.Now().UTC())
		if err != nil {
			h.agentStoreCreateError(w, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, viewFromRecord(rec))
		return
	}

	// AK-2 legacy mode (no registry configured): request-supplied grants —
	// the documented transitional contract, unchanged.
	if req.Capabilities == nil || len(*req.Capabilities) == 0 {
		writeJSONError(w, http.StatusBadRequest, "capabilities are required in legacy mode (no grant registry configured; set SCARLIHQ_AGENT_GRANTS_FILE for admin-owned grants)")
		return
	}
	// Agent identity: validated by the kernel (typed ID + mandatory owner).
	agent, err := agentkernel.NewAgentIdentity(agentkernel.AgentID(req.AgentID), req.Owner, time.Now().UTC())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Capability set: closed enumeration — unknown capabilities (incl.
	// shell.exec and friends) are rejected here with 400.
	caps := make([]agentkernel.Capability, 0, len(*req.Capabilities))
	for _, c := range *req.Capabilities {
		caps = append(caps, agentkernel.Capability(c))
	}
	set, err := agentkernel.NewCapabilitySet(caps...)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	rec, err := h.agentStore.Create(agent, set, time.Now().UTC())
	if err != nil {
		h.agentStoreCreateError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, viewFromRecord(rec))
}

// agentStoreCreateError maps session-store creation errors to HTTP statuses
// (shared by legacy and registry mode).
func (h *Handler) agentStoreCreateError(w http.ResponseWriter, err error) {
	if errors.Is(err, agentkernel.ErrSessionLimit) {
		writeJSONError(w, http.StatusServiceUnavailable, "session store at capacity")
		return
	}
	if errors.Is(err, agentkernel.ErrRNG) {
		writeJSONError(w, http.StatusInternalServerError, "session id generation failed")
		return
	}
	writeJSONError(w, http.StatusBadRequest, err.Error())
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

	// AK-3: registry mode evaluates against the registry policy so runtime
	// disable toggles take effect on the NEXT evaluation (no restart).
	policy := h.agentPolicy
	if h.agentGrants != nil {
		policy = h.agentGrants.Policy()
	}

	// Denials are 200 responses with allow=false — a denial is a successful,
	// auditable evaluation. Unknown session is a 404 client error.
	event, decision, err := h.agentStore.Evaluate(strings.TrimSpace(req.SessionID), policy, capability, time.Now().UTC())
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

// ---------------------------------------------------------------------------
// AK-3 (v19.4.4): persisted, admin-owned grant registry surface
// ---------------------------------------------------------------------------

// agentGrantCreateRequest: one JSON object, unknown fields rejected.
type agentGrantCreateRequest struct {
	AgentID    string `json:"agent_id"`
	Capability string `json:"capability"`
	Owner      string `json:"owner"`
	GrantedBy  string `json:"granted_by"`
}

// agentGrantRevokeRequest: one JSON object, unknown fields rejected.
type agentGrantRevokeRequest struct {
	AgentID    string `json:"agent_id"`
	Capability string `json:"capability"`
	RevokedBy  string `json:"revoked_by"`
}

// agentCapabilityDisableRequest: one JSON object, unknown fields rejected.
type agentCapabilityDisableRequest struct {
	Capability string `json:"capability"`
	DisabledBy string `json:"disabled_by"`
	Reason     string `json:"reason"`
}

// agentCapabilityEnableRequest: one JSON object, unknown fields rejected.
type agentCapabilityEnableRequest struct {
	Capability string `json:"capability"`
	EnabledBy  string `json:"enabled_by"`
}

func (h *Handler) agentGrantsDisabled(w http.ResponseWriter) bool {
	if h.agentGrants == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent kernel grant registry is disabled")
		return true
	}
	return false
}

// agentGrantView is the JSON representation of one grant record (tombstones
// included — the registry IS the reviewable history).
type agentGrantView struct {
	AgentID    string `json:"agent_id"`
	Capability string `json:"capability"`
	Owner      string `json:"owner"`
	GrantedBy  string `json:"granted_by"`
	GrantedAt  string `json:"granted_at"`
	Active     bool   `json:"active"`
	RevokedAt  string `json:"revoked_at,omitempty"`
	RevokedBy  string `json:"revoked_by,omitempty"`
}

func viewFromGrant(g agentkernel.GrantRecord) agentGrantView {
	v := agentGrantView{
		AgentID:    string(g.AgentID),
		Capability: string(g.Capability),
		Owner:      g.Owner,
		GrantedBy:  g.GrantedBy,
		GrantedAt:  g.GrantedAt.Format(time.RFC3339),
		Active:     g.Active(),
	}
	if g.RevokedAt != nil {
		v.RevokedAt = g.RevokedAt.Format(time.RFC3339)
		v.RevokedBy = g.RevokedBy
	}
	return v
}

// agentDisableView is the JSON representation of one disable switch.
type agentDisableView struct {
	Capability string `json:"capability"`
	DisabledBy string `json:"disabled_by"`
	DisabledAt string `json:"disabled_at"`
	Reason     string `json:"reason"`
}

// agentGrantsRoute handles GET (list) and POST (grant) on /api/agent/grants.
// (Named *Route to avoid the field-vs-method name collision with the
// Handler.agentGrants registry field — Go forbids same-name field+method.)
func (h *Handler) agentGrantsRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if h.agentGrantsDisabled(w) {
			return
		}
		grants := h.agentGrants.ListGrants()
		views := make([]agentGrantView, 0, len(grants))
		for _, g := range grants {
			views = append(views, viewFromGrant(g))
		}
		disables := make([]agentDisableView, 0)
		for _, d := range h.agentGrants.ListDisables() {
			disables = append(disables, agentDisableView{
				Capability: string(d.Capability),
				DisabledBy: d.DisabledBy,
				DisabledAt: d.DisabledAt.Format(time.RFC3339),
				Reason:     d.Reason,
			})
		}
		active, total, agents, disabledCount := h.agentGrants.Counts()
		writeJSON(w, map[string]interface{}{
			"grants":           views,
			"disables":         disables,
			"active_grants":    active,
			"total_records":    total,
			"agents":           agents,
			"disabled_caps":    disabledCount,
			"registry_version": 1,
		})
	case http.MethodPost:
		h.agentGrantCreate(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "use GET or POST")
	}
}

func (h *Handler) agentGrantCreate(w http.ResponseWriter, r *http.Request) {
	if h.agentGrantsDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &agentGrantCreateRequest{} })
	if !ok {
		return
	}
	req := request.(*agentGrantCreateRequest)

	rec, err := h.agentGrants.Grant(
		agentkernel.AgentID(strings.TrimSpace(req.AgentID)),
		agentkernel.Capability(strings.TrimSpace(req.Capability)),
		strings.TrimSpace(req.Owner),
		strings.TrimSpace(req.GrantedBy),
		time.Now().UTC(),
	)
	if err != nil {
		h.agentRegistryError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, viewFromGrant(rec))
}

func (h *Handler) agentGrantRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.agentGrantsDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &agentGrantRevokeRequest{} })
	if !ok {
		return
	}
	req := request.(*agentGrantRevokeRequest)

	rec, err := h.agentGrants.Revoke(
		agentkernel.AgentID(strings.TrimSpace(req.AgentID)),
		agentkernel.Capability(strings.TrimSpace(req.Capability)),
		strings.TrimSpace(req.RevokedBy),
		time.Now().UTC(),
	)
	if err != nil {
		h.agentRegistryError(w, err)
		return
	}
	writeJSON(w, viewFromGrant(rec))
}

func (h *Handler) agentCapabilityDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.agentGrantsDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &agentCapabilityDisableRequest{} })
	if !ok {
		return
	}
	req := request.(*agentCapabilityDisableRequest)

	rec, err := h.agentGrants.Disable(
		agentkernel.Capability(strings.TrimSpace(req.Capability)),
		strings.TrimSpace(req.DisabledBy),
		strings.TrimSpace(req.Reason),
		time.Now().UTC(),
	)
	if err != nil {
		h.agentRegistryError(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{
		"capability":  string(rec.Capability),
		"disabled_by": rec.DisabledBy,
		"disabled_at": rec.DisabledAt.Format(time.RFC3339),
		"reason":      rec.Reason,
	})
}

func (h *Handler) agentCapabilityEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.agentGrantsDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &agentCapabilityEnableRequest{} })
	if !ok {
		return
	}
	req := request.(*agentCapabilityEnableRequest)

	if err := h.agentGrants.Enable(
		agentkernel.Capability(strings.TrimSpace(req.Capability)),
		strings.TrimSpace(req.EnabledBy),
		time.Now().UTC(),
	); err != nil {
		h.agentRegistryError(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{
		"capability": strings.TrimSpace(req.Capability),
		"enabled":    true,
	})
}

// agentRegistryError maps grant-registry errors to HTTP statuses. Registry
// bound errors are 503 (back off), everything else is a 400 client error —
// the registry never turns a validation failure into a 5xx.
func (h *Handler) agentRegistryError(w http.ResponseWriter, err error) {
	if errors.Is(err, agentkernel.ErrGrantLimit) || errors.Is(err, agentkernel.ErrAgentLimit) {
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSONError(w, http.StatusBadRequest, err.Error())
}
