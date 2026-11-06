package api

// CF-3 (v19.6.2): the propose surface — the bridge between the Universal
// AI Runtime and the Agent Fabric. Design rules
// (docs/AI_RUNTIME_CF3_DESIGN.md):
//
//   - POST /api/ai/propose takes EXACTLY the chat request shape and runs
//     it through the CF-2 runtime unchanged (failover, attribution, the
//     same error mapping). The response content is then parsed by the
//     PURE agentfabric.ParseProposal for exactly ONE structured
//     ```scarlix-proposal block. An LLM response becomes a fabric
//     PROPOSAL — never an auto-submitted command: the propose endpoint
//     captures (PENDING, attribution: which provider AND which model
//     proposed), and NOTHING else. The operator — and only the operator
//     — accepts or dismisses;
//   - zero blocks → 200 with "proposal": null (the model answered and
//     proposed nothing — null IS the answer, not an error); a malformed
//     block → 422, NOTHING captured, with the named reason + the
//     provider/model attribution + the response content (fail-closed,
//     not fail-blind); a well-formed block → 200 + the PENDING proposal;
//   - the surface requires BOTH opt-ins fail-closed (503): the AI
//     runtime (SCARLIHQ_AIRUNTIME_ENABLED=true) and the fabric's
//     proposal store (SCARLIHQ_AGENTFABRIC_ENABLED=true — the proposal
//     is a fabric entity; proposals.json rides the fabric opt-in,
//     exactly like approvals.json);
//   - accept is the ONLY door from a proposal to a command: the BF-1
//     capture path (submitter = the accepting operator), the bar's own
//     routing question (an ACTIVE session resolves → route+decide; none
//     → SUBMITTED, the intent is never lost), and the reconciliation
//     path for a crashed accept (GetByProposal — no intent is captured
//     twice). Accept carries NO params — the accepted thing is the
//     proposed thing;
//   - dismiss is the operator's unconditional no (terminal, no command
//     ever; no kernel evaluation gates a dismissal — nothing runs);
//   - every route is behind the standard Bearer auth middleware (h.auth),
//     exactly like every other surface. Labels are attribution, never
//     credentials.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentfabric"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airt"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// ConfigureAgentFabricProposals wires the proposal store into the
// handler. It rides the fabric opt-in (main.go constructs it whenever the
// fabric is enabled — no separate flag, exactly like the approval store).
func (h *Handler) ConfigureAgentFabricProposals(store *agentfabric.ProposalStore) error {
	if store == nil {
		return errors.New("api: agent fabric proposal store is required")
	}
	h.agentProposals = store
	return nil
}

// proposalsDisabled reports (and writes) whether the proposal review
// surface is unavailable: no proposal store (503 — the fabric opt-in
// carries it) or no command store + kernel for the accept path (503).
func (h *Handler) proposalsDisabled(w http.ResponseWriter) bool {
	if h.agentProposals == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric proposals are disabled")
		return true
	}
	if h.agentFabric == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "proposal accept requires the agent fabric (SCARLIHQ_AGENTFABRIC_ENABLED=true) — a proposal becomes a command through the bar's capture path")
		return true
	}
	if h.agentStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric requires the agent kernel (SCARLIHQ_AGENTKERNEL_ENABLED=true) — accepted proposals route through kernel sessions")
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// POST /api/ai/propose — chat + the pure parser + the capture
// ---------------------------------------------------------------------------

// aiProposeRequest is EXACTLY the chat request shape (the closed decode
// + the 1 MiB bound ride decodeChatJSON): the propose surface IS chat,
// plus the capture.
type aiProposeRequest = aiChatRequest

// aiProposalView is the user-visible proposal status (listed at
// /api/ai/proposals and returned by propose/accept/dismiss). Proposer is
// the attribution "provider/model" — attribution survives failover: the
// record says WHICH tier answered and WHICH model proposed.
type aiProposalView struct {
	ID         string `json:"id"`
	Intent     string `json:"intent"`
	AgentID    string `json:"agent_id"`
	Capability string `json:"capability"`
	Proposer   string `json:"proposer"`
	State      string `json:"state"`
	Operator   string `json:"operator,omitempty"`
	Reason     string `json:"reason,omitempty"`
	CommandID  string `json:"command_id,omitempty"`
	CapturedAt string `json:"captured_at"`
	DecidedAt  string `json:"decided_at,omitempty"`
	UpdatedAt  string `json:"updated_at"`
}

// aiProposeResponse is the chat response with the proposal riding along:
// the view when a well-formed block was captured, null when the model
// proposed nothing. The chat fields are identical to /api/ai/chat.
type aiProposeResponse struct {
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	Message      aiChatMessage   `json:"message"`
	FinishReason string          `json:"finish_reason"`
	Usage        aiUsageView     `json:"usage"`
	Timestamp    string          `json:"timestamp"`
	Attempts     []aiAttemptView `json:"attempts"`
	Proposal     *aiProposalView `json:"proposal"`
}

func viewFromAIProposal(p agentfabric.ProposalRecord) aiProposalView {
	v := aiProposalView{
		ID:         p.ID,
		Intent:     p.Intent,
		AgentID:    string(p.AgentID),
		Capability: string(p.Capability),
		Proposer:   string(p.ProposerProvider) + "/" + string(p.ProposerModel),
		State:      string(p.State),
		Operator:   p.Operator,
		Reason:     p.Reason,
		CommandID:  p.CommandID,
		CapturedAt: p.CapturedAt.Format(time.RFC3339),
		UpdatedAt:  p.UpdatedAt.Format(time.RFC3339),
	}
	if p.DecidedAt != nil {
		v.DecidedAt = p.DecidedAt.Format(time.RFC3339)
	}
	return v
}

// aiPropose: the chat request runs through the CF-2 runtime; the response
// content is parsed by the PURE parser; a well-formed block is captured
// as a PENDING proposal (attribution: provider + model). NOTHING else
// happens — no routing, no decision, no execution, no approval request:
// the operator holds the only pen that writes into the fabric.
func (h *Handler) aiPropose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.aiRuntimeDisabled(w) {
		return
	}
	if h.agentProposals == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "propose surface requires the agent fabric (SCARLIHQ_AGENTFABRIC_ENABLED=true) — a proposal is a fabric entity; the runtime alone never captures one")
		return
	}
	// KPK-02 (v19.6.3): the propose surface rides the SAME end-to-end
	// timeout contract as /api/ai/chat — the budget context for the whole
	// failover walk, the write deadline extended beyond it for the answer
	// (a proposal capture that outlives the server's write path would be
	// the same KPK-02 failure wearing a CF-3 coat).
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(h.aiRuntime.ChatBudget() + aiWriteMargin)) //nolint:errcheck — a recorder cannot set deadlines; the budget rides the context regardless
	ctx, cancel := h.aiChatContext(r)
	defer cancel()
	request, ok := decodeChatJSON(w, r, func() any { return &aiProposeRequest{} })
	if !ok {
		return
	}
	req := request.(*aiProposeRequest)
	messages := make([]airuntime.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, airuntime.Message{Role: airuntime.Role(m.Role), Content: m.Content})
	}
	outcome, err := h.aiRuntime.Chat(ctx, airuntime.Request{
		Model:    airuntime.ModelID(req.Model),
		Messages: messages,
	})
	if err != nil {
		// The named errors ride their explanations exactly like
		// /api/ai/chat (the transport is the same; only the capture is
		// new — a transport failure never reaches the parser).
		switch {
		case errors.Is(err, airuntime.ErrUnknownModel):
			writeJSONError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, airuntime.ErrNoRoute):
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]interface{}{"error": err.Error(), "skips": skipViews(outcome.Skips)})
		case errors.Is(err, airt.ErrBudgetExpired):
			// The deliberate timeout status (KPK-02): the end-to-end budget
			// expired — 504, the attempts that consumed it riding along.
			writeJSONStatus(w, http.StatusGatewayTimeout, map[string]interface{}{"error": err.Error(), "attempts": attemptViews(outcome.Attempts)})
		case errors.Is(err, airt.ErrAllAdaptersFailed):
			writeJSONStatus(w, http.StatusBadGateway, map[string]interface{}{"error": err.Error(), "attempts": attemptViews(outcome.Attempts)})
		default:
			writeJSONError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	resp := outcome.Response

	draft, perr := agentfabric.ParseProposal(resp.Message.Content)
	if perr != nil {
		if errors.Is(perr, agentfabric.ErrNoProposal) {
			// The model answered and proposed nothing — the honest
			// null, never an error (null IS the answer).
			attempts := attemptViews(outcome.Attempts)
			if attempts == nil {
				attempts = []aiAttemptView{}
			}
			writeJSON(w, aiProposeResponse{
				Provider:     string(resp.Provider),
				Model:        string(resp.Model),
				Message:      aiChatMessage{Role: string(resp.Message.Role), Content: resp.Message.Content},
				FinishReason: string(resp.Finish),
				Usage:        aiUsageView{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens},
				Timestamp:    resp.Timestamp.UTC().Format("2006-01-02T15:04:05Z07:00"),
				Attempts:     attempts,
				Proposal:     nil,
			})
			return
		}
		// Malformed: NOTHING is captured (fail-closed — never a partial
		// guess), and the explanation rides along (fail-closed, not
		// fail-blind): the named reason + WHICH tier said it + the
		// content itself (a re-run is another generation; the refused
		// response is shown, not lost).
		writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]interface{}{
			"error":     perr.Error(),
			"provider":  string(resp.Provider),
			"model":     string(resp.Model),
			"message":   aiChatMessage{Role: string(resp.Message.Role), Content: resp.Message.Content},
			"timestamp": resp.Timestamp.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
		return
	}

	// A well-formed block: capture it — PENDING, with the runtime's own
	// typed attribution (which tier answered, which model proposed).
	id, err := agentfabric.NewProposalID()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "proposal id generation failed")
		return
	}
	now := time.Now().UTC()
	captured, err := h.agentProposals.Capture(agentfabric.ProposalRecord{
		ID:               id,
		Intent:           draft.Intent,
		AgentID:          draft.AgentID,
		Capability:       draft.Capability,
		ProposerProvider: resp.Provider,
		ProposerModel:    resp.Model,
		State:            agentfabric.ProposalPending,
		CapturedAt:       now,
		UpdatedAt:        now,
	})
	if err != nil {
		if errors.Is(err, agentfabric.ErrProposalLimit) {
			writeJSONError(w, http.StatusServiceUnavailable, "proposal store at capacity — archive and rotate the proposals file deliberately")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "proposal capture failed")
		return
	}
	h.fabricPublish(agentfabric.SignalProposalCaptured, captured.ID)

	attempts := attemptViews(outcome.Attempts)
	if attempts == nil {
		attempts = []aiAttemptView{} // a stable JSON shape, not a null
	}
	view := viewFromAIProposal(captured)
	writeJSON(w, aiProposeResponse{
		Provider:     string(resp.Provider),
		Model:        string(resp.Model),
		Message:      aiChatMessage{Role: string(resp.Message.Role), Content: resp.Message.Content},
		FinishReason: string(resp.Finish),
		Usage:        aiUsageView{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens},
		Timestamp:    resp.Timestamp.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Attempts:     attempts,
		Proposal:     &view,
	})
}

// ---------------------------------------------------------------------------
// GET /api/ai/proposals — the bounded review surface
// ---------------------------------------------------------------------------

// aiProposals lists proposals newest-first, bounded (?limit=1..200,
// default 200), with the closed ?state= filter (PENDING|ACCEPTED|
// DISMISSED — unknown → 400) and the counts contract (all states always
// present) — the approvals read surface's discipline, exactly.
func (h *Handler) aiProposals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	if h.agentProposals == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric proposals are disabled")
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
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	var records []agentfabric.ProposalRecord
	if state == "" {
		records = h.agentProposals.ListLast(limit)
	} else {
		switch agentfabric.ProposalState(state) {
		case agentfabric.ProposalPending, agentfabric.ProposalAccepted, agentfabric.ProposalDismissed:
			records = h.agentProposals.ListLastState(limit, agentfabric.ProposalState(state))
		default:
			writeJSONError(w, http.StatusBadRequest, "state must be PENDING, ACCEPTED or DISMISSED (closed enumeration)")
			return
		}
	}
	views := make([]aiProposalView, 0, len(records))
	for _, rec := range records {
		views = append(views, viewFromAIProposal(rec))
	}
	writeJSON(w, map[string]interface{}{
		"proposals":  views,
		"count":      len(views),
		"counts":     h.agentProposals.Counts(),
		"limit":      limit,
		"store_path": h.agentProposals.Path(),
		"total":      h.agentProposals.Count(),
	})
}

// ---------------------------------------------------------------------------
// POST /api/ai/proposals/accept — the ONLY door from a proposal to a command
// ---------------------------------------------------------------------------

// aiProposalAcceptRequest: one JSON object, unknown fields rejected. The
// operator label is REQUIRED (attribution — who accepted; a label, never
// a credential). NO params: the accepted thing is the proposed thing (a
// changed intent is a NEW proposal or a direct bar submission).
type aiProposalAcceptRequest struct {
	ProposalID string `json:"proposal_id"`
	Operator   string `json:"operator"`
}

// aiProposalDismissRequest: one JSON object, unknown fields rejected. The
// operator label is REQUIRED; the reason is optional (bounded — the why,
// for the reviewable history).
type aiProposalDismissRequest struct {
	ProposalID string `json:"proposal_id"`
	Operator   string `json:"operator"`
	Reason     string `json:"reason,omitempty"`
}

// aiProposalDecisionView: the accept/dismiss response — the proposal and
// (accept only) the command the proposal became, plus the reconciliation
// marker (a crashed accept retried into the EXISTING command — no intent
// captured twice).
type aiProposalDecisionView struct {
	Proposal   aiProposalView  `json:"proposal"`
	Command    *barCommandView `json:"command,omitempty"`
	Reconciled bool            `json:"reconciled"`
}

// aiProposalAccept is the operator's yes. The flow, in order:
//
//  1. ProposalID shape first (400 vs 404), PENDING gate (409 on
//     terminal), operator label required (400).
//  2. RECONCILIATION: a command already carries this proposal's id (a
//     crashed accept AFTER the command persist) → the proposal closes
//     ACCEPTED referencing the EXISTING command, no new command,
//     reconciled=true.
//  3. The BF-1 capture path: SubmitWithProposal (submitter = the
//     accepting operator; the linkage is durable on the command).
//  4. The bar's own routing question: an ACTIVE session of the agent
//     resolves → route + kernel decide (synchronously — the unchanged
//     routeAndDecide composition); none → the command stays SUBMITTED
//     (the intent is never lost).
//  5. The proposal closes ACCEPTED referencing the command. Ordering is
//     honest: command persist → [route+decide] → proposal close →
//     response; a crash between the lasts takes the reconciliation path
//     on retry.
func (h *Handler) aiProposalAccept(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.proposalsDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &aiProposalAcceptRequest{} })
	if !ok {
		return
	}
	req := request.(*aiProposalAcceptRequest)

	id := agentfabric.ProposalID(strings.TrimSpace(req.ProposalID))
	if err := id.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "proposal id must be prp- + 32 hex chars")
		return
	}
	operator := strings.TrimSpace(req.Operator)
	if operator == "" {
		writeJSONError(w, http.StatusBadRequest, "operator label is required (attribution — who accepted the proposal)")
		return
	}
	if err := agentfabric.ValidatePrincipalLabel(operator); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	rec, ok := h.agentProposals.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown proposal")
		return
	}
	if rec.State != agentfabric.ProposalPending {
		writeJSONError(w, http.StatusConflict, "proposal is "+string(rec.State)+" (terminal) — only PENDING proposals can be accepted")
		return
	}
	now := time.Now().UTC()

	// 2. Reconciliation: the crash window between the command persist and
	// the proposal close. The DURABLE linkage (the command's proposal_id)
	// answers deterministically — no second command is ever captured for
	// one proposal.
	if cmd, linked := h.agentFabric.GetByProposal(id); linked {
		closed, err := h.agentProposals.Accept(id, operator, cmd.ID, now)
		if err != nil {
			h.aiProposalMutationError(w, err)
			return
		}
		h.fabricPublish(agentfabric.SignalProposalDecided, closed.ID)
		commandView := h.viewWithExecution(cmd)
		writeJSONStatus(w, http.StatusCreated, aiProposalDecisionView{
			Proposal:   viewFromAIProposal(closed),
			Command:    &commandView,
			Reconciled: true,
		})
		return
	}

	// 3. The BF-1 capture path, exactly: the submitter is the ACCEPTING
	// operator (the human act that captured the intent into the bar; the
	// model attribution rides the proposal and the composed view).
	cmd, err := h.agentFabric.SubmitWithProposal(rec.Intent, rec.AgentID, rec.Capability, operator, id, now)
	if err != nil {
		h.barCommandMutationError(w, err)
		return
	}
	h.fabricPublish(agentfabric.SignalCommandSubmitted, cmd.ID)

	// 4. The bar's own routing question — resolveActiveSession (unscoped:
	// proposals never carry a workspace, by design). No session → the
	// intent is captured, status visible (SUBMITTED).
	sessionID := h.resolveActiveSession(cmd.AgentID, "")
	if sessionID == "" {
		closed, err := h.agentProposals.Accept(id, operator, cmd.ID, now)
		if err != nil {
			h.aiProposalMutationError(w, err)
			return
		}
		h.fabricPublish(agentfabric.SignalProposalDecided, closed.ID)
		commandView := h.viewWithExecution(cmd)
		writeJSONStatus(w, http.StatusCreated, aiProposalDecisionView{
			Proposal:   viewFromAIProposal(closed),
			Command:    &commandView,
			Reconciled: false,
		})
		return
	}

	// Route + evaluate + decide, synchronously (the unchanged composition
	// — CF-3 adds NO new routing semantics). A failure here writes the
	// error itself; the command exists and the proposal stays PENDING —
	// an honest intermediate: retry accept, the reconciliation path
	// closes the proposal against the EXISTING command.
	decided, ok := h.routeAndDecide(w, agentfabric.CommandID(cmd.ID), sessionID)
	if !ok {
		return
	}
	closed, err := h.agentProposals.Accept(id, operator, decided.ID, now)
	if err != nil {
		h.aiProposalMutationError(w, err)
		return
	}
	h.fabricPublish(agentfabric.SignalProposalDecided, closed.ID)
	commandView := h.viewWithExecution(decided)
	writeJSONStatus(w, http.StatusCreated, aiProposalDecisionView{
		Proposal:   viewFromAIProposal(closed),
		Command:    &commandView,
		Reconciled: false,
	})
}

// aiProposalDismiss is the operator's unconditional no — terminal, no
// command ever. No kernel evaluation gates a dismissal (nothing runs,
// there is nothing to evaluate); the optional bounded reason carries the
// why for the reviewable history.
func (h *Handler) aiProposalDismiss(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.agentProposals == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent fabric proposals are disabled")
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &aiProposalDismissRequest{} })
	if !ok {
		return
	}
	req := request.(*aiProposalDismissRequest)

	id := agentfabric.ProposalID(strings.TrimSpace(req.ProposalID))
	if err := id.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "proposal id must be prp- + 32 hex chars")
		return
	}
	operator := strings.TrimSpace(req.Operator)
	if operator == "" {
		writeJSONError(w, http.StatusBadRequest, "operator label is required (attribution — who dismissed the proposal)")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	closed, err := h.agentProposals.Dismiss(id, operator, reason, time.Now().UTC())
	if err != nil {
		if errors.Is(err, agentfabric.ErrUnknownProposal) {
			writeJSONError(w, http.StatusNotFound, "unknown proposal")
			return
		}
		if errors.Is(err, agentfabric.ErrProposalState) {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.fabricPublish(agentfabric.SignalProposalDecided, closed.ID)
	writeJSONStatus(w, http.StatusCreated, aiProposalDecisionView{
		Proposal: viewFromAIProposal(closed), // never a command reference — the validate invariant
	})
}

// aiProposalMutationError maps proposal close errors to HTTP statuses
// (the barCommandMutationError genre).
func (h *Handler) aiProposalMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agentfabric.ErrUnknownProposal):
		writeJSONError(w, http.StatusNotFound, "unknown proposal")
	case errors.Is(err, agentfabric.ErrProposalState):
		// A concurrent decision won the race (single-operator
		// deployments make this theoretical); the command, once
		// captured, is a fact — visible, cancellable before decision.
		writeJSONError(w, http.StatusConflict, err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, err.Error())
	}
}
