package api

// CF-2 (v19.6.1): Universal AI Runtime REST surface — the normalized LLM
// layer becomes reachable. Design rules (docs/AI_RUNTIME_CF2_DESIGN.md):
//
//   - every route is behind the standard Bearer auth middleware (h.auth),
//     exactly like every other surface; 503 until ConfigureAIRuntime has
//     been called (the SCARLIHQ_AIRUNTIME_ENABLED=true opt-in);
//   - GET /api/ai/health and GET /api/ai/models are READ-ONLY views over
//     the CF-1 state (health table + model registry);
//   - POST /api/ai/models is the operator registration act (the registry
//     IS the routing surface — no auto-discovery exists to call);
//   - POST /api/ai/probe triggers one probe round NOW and records it
//     (freshness is the prober's job — here the operator can demand it);
//   - POST /api/ai/chat is the failover chat: the runtime plans with the
//     pure CF-1 router, walks the plan, attributes the answer and returns
//     the failed attempts alongside (fail-closed, not fail-blind). Chat
//     is TRANSPORT ONLY — it mutates nothing on the host; an LLM response
//     becoming a fabric proposal is CF-3, behind the fabric's approvals.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airt"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
)

// MaxChatBodyBytes bounds the chat request body. The decodeOneJSON 4 KiB
// cap fits the small admin surfaces, not chat: the contract itself allows
// 32 messages × 4096 runes — the bound leaves the full legitimate range
// reachable while keeping a runaway body out of memory.
const MaxChatBodyBytes = 1 << 20

// aiWriteMargin is the serialization/network margin the /api/ai/chat and
// /api/ai/propose write path keeps BEYOND the chat budget when it extends
// the per-request write deadline (http.ResponseController, KPK-02,
// v19.6.3): the runtime must be able to WRITE the answer it is still
// allowed to wait for. The server's 30s WriteTimeout default keeps
// serving every other surface unchanged — the AI paths carry their own
// deliberate, budget-derived deadline instead of conflicting with it.
const aiWriteMargin = 10 * time.Second

// ConfigureAIRuntime attaches the CF-2 Universal AI Runtime (opt-in from
// main.go, SCARLIHQ_AIRUNTIME_ENABLED=true). Nil is rejected — there is
// no half-configured runtime.
func (h *Handler) ConfigureAIRuntime(rt *airt.Runtime) error {
	if rt == nil {
		return errors.New("api: ai runtime is required")
	}
	h.aiRuntime = rt
	return nil
}

func (h *Handler) aiRuntimeDisabled(w http.ResponseWriter) bool {
	if h.aiRuntime == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "universal ai runtime is disabled")
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// GET /api/ai/health — the tier-ordered health view (read-only)
// ---------------------------------------------------------------------------

type aiHealthEntryView struct {
	Provider     string `json:"provider"`
	Tier         int    `json:"tier"`
	State        string `json:"state"`
	LastObserved string `json:"last_observed"`
	Serves       bool   `json:"serves"`
}

func (h *Handler) aiHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	if h.aiRuntimeDisabled(w) {
		return
	}
	entries := h.aiRuntime.HealthView()
	views := make([]aiHealthEntryView, 0, len(entries))
	for _, e := range entries {
		views = append(views, aiHealthEntryView{
			Provider:     string(e.Provider),
			Tier:         int(e.Tier),
			State:        string(e.State),
			LastObserved: e.LastObserved,
			Serves:       e.Serves,
		})
	}
	writeJSON(w, map[string]interface{}{
		"providers": views,
		"count":     len(views),
	})
}

// ---------------------------------------------------------------------------
// GET/POST /api/ai/models — the registry view + the operator act
// ---------------------------------------------------------------------------

type aiModelsEntryView struct {
	Provider string   `json:"provider"`
	Tier     int      `json:"tier"`
	Models   []string `json:"models"`
}

type aiModelRegisterRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (h *Handler) aiModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.aiModelsList(w, r)
	case http.MethodPost:
		h.aiModelsRegister(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "use GET or POST")
	}
}

func (h *Handler) aiModelsList(w http.ResponseWriter, r *http.Request) {
	if h.aiRuntimeDisabled(w) {
		return
	}
	entries := h.aiRuntime.RegistryView()
	views := make([]aiModelsEntryView, 0, len(entries))
	for _, e := range entries {
		models := make([]string, 0, len(e.Models))
		tier, err := e.Provider.Tier()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "registry carries an invalid provider")
			return
		}
		for _, m := range e.Models {
			models = append(models, string(m))
		}
		views = append(views, aiModelsEntryView{Provider: string(e.Provider), Tier: int(tier), Models: models})
	}
	writeJSON(w, map[string]interface{}{
		"providers": views,
		"count":     len(views),
	})
}

func (h *Handler) aiModelsRegister(w http.ResponseWriter, r *http.Request) {
	if h.aiRuntimeDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &aiModelRegisterRequest{} })
	if !ok {
		return
	}
	req := request.(*aiModelRegisterRequest)
	provider := airuntime.Provider(req.Provider)
	model := airuntime.ModelID(req.Model)
	if err := h.aiRuntime.Register(provider, model); err != nil {
		if errors.Is(err, airuntime.ErrDuplicateRegistration) {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, airuntime.ErrModelLimit) {
			writeJSONError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	tier, err := provider.Tier()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "registered provider lost its tier")
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{
		"provider": string(provider),
		"tier":     int(tier),
		"model":    string(model),
	})
}

// ---------------------------------------------------------------------------
// POST /api/ai/probe — one probe round, now, recorded
// ---------------------------------------------------------------------------

type aiObservationView struct {
	Provider string `json:"provider"`
	Tier     int    `json:"tier"`
	State    string `json:"state"`
	Observed string `json:"observed"`
}

func (h *Handler) aiProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.aiRuntimeDisabled(w) {
		return
	}
	observations, err := h.aiRuntime.ProbeAll(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("probe round failed: %v", err))
		return
	}
	views := make([]aiObservationView, 0, len(observations))
	for _, o := range observations {
		tier, err := o.Provider.Tier()
		if err != nil {
			continue // cannot happen for enumerated providers — honest guard
		}
		views = append(views, aiObservationView{
			Provider: string(o.Provider),
			Tier:     int(tier),
			State:    string(o.State),
			Observed: o.At.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	writeJSON(w, map[string]interface{}{
		"observations": views,
		"count":        len(views),
	})
}

// ---------------------------------------------------------------------------
// POST /api/ai/chat — the normalized failover chat
// ---------------------------------------------------------------------------

type aiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiChatRequest struct {
	Model    string          `json:"model"`
	Messages []aiChatMessage `json:"messages"`
	// Profile is the OPTIONAL profile reference (v19.6.6, KPK-19.6.5-P1-03):
	// when set (body field or X-Scarlix-Profile header), the chat is policy
	// DECIDED server-side — the ai_chat permission and the content-filter
	// blocklist are enforced HERE, not by a dashboard hiding controls.
	Profile string `json:"profile,omitempty"`
}

type aiUsageView struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type aiAttemptView struct {
	Provider string `json:"provider"`
	Tier     int    `json:"tier"`
	Role     string `json:"role"`
	Error    string `json:"error"`
}

type aiChatResponse struct {
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	Message      aiChatMessage   `json:"message"`
	FinishReason string          `json:"finish_reason"`
	Usage        aiUsageView     `json:"usage"`
	Timestamp    string          `json:"timestamp"`
	Attempts     []aiAttemptView `json:"attempts"` // failed tiers, plan order — attribution survives failover
}

// decodeChatJSON mirrors decodeOneJSON with the chat-sized body bound
// (MaxChatBodyBytes, not 4 KiB): exactly one JSON object, unknown fields
// rejected — the same closed-decode discipline, the right magnitude.
func decodeChatJSON(w http.ResponseWriter, r *http.Request, factory func() any) (any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxChatBodyBytes)
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

// aiChatContext imposes the END-TO-END timeout contract on one AI chat
// request (KPK-02, v19.6.3): the request context carries the runtime's
// chat budget as its deadline (every adapter attempt and probe operation
// propagates it — sequential failover steps share the SAME budget, never
// an independent per-tier allowance), and the HTTP write path is extended
// beyond the budget with the write margin so a legitimate slow answer is
// written by the server, never cut by the socket write deadline. Behind a
// ResponseWriter that cannot set deadlines (httptest's recorder) the
// extension degrades honestly — the budget itself rides the context
// regardless.
func (h *Handler) aiChatContext(r *http.Request) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(r.Context(), h.aiRuntime.ChatBudget())
	return ctx, cancel
}

func (h *Handler) aiChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.aiRuntimeDisabled(w) {
		return
	}
	// KPK-02 (v19.6.3): the end-to-end budget — context deadline for the
	// whole failover walk, write deadline beyond it for the answer.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(h.aiRuntime.ChatBudget() + aiWriteMargin)) //nolint:errcheck — a recorder cannot set deadlines; the budget rides the context regardless
	ctx, cancel := h.aiChatContext(r)
	defer cancel()
	request, ok := decodeChatJSON(w, r, func() any { return &aiChatRequest{} })
	if !ok {
		return
	}
	req := request.(*aiChatRequest)
	// v19.6.6 (KPK-19.6.5-P1-03): the profile reference (body field or the
	// X-Scarlix-Profile header) makes the chat DECIDABLE against the
	// declared family policy — SERVER-SIDE, at the action boundary:
	//   1. the ai_chat permission is REQUIRED (a profile without it never
	//      reaches a model, whatever the dashboard shows);
	//   2. an ENABLED content filter blocklist is enforced on the chat input
	//      (keyword-level, honestly documented — a semantic classifier is a
	//      CF-4+ item, never a silent claim);
	//   3. an unloadable profile reference FAILS CLOSED — a broken policy
	//      file is never an unenforced bypass.
	// The household Bearer token still gates the route; identity stays the
	// caller's declaration until per-profile authentication (CF-4+, the
	// honest residual in the SECURITY_MAP).
	profileRef := req.Profile
	if profileRef == "" {
		profileRef = r.Header.Get("X-Scarlix-Profile")
	}
	if profileRef != "" {
		p, perr := h.profiles.Load(profileRef)
		if perr != nil {
			writeJSONError(w, http.StatusBadRequest, perr.Error())
			return
		}
		if perr := p.RequirePermission("ai_chat"); perr != nil {
			writeJSONError(w, http.StatusForbidden, perr.Error())
			return
		}
		for _, m := range req.Messages {
			if category, blocked := p.ContentBlocked(m.Content); blocked {
				writeJSONError(w, http.StatusForbidden, fmt.Sprintf("%v: category %q (profile %s's content filter is enforced server-side on the chat input)", profiles.ErrContentBlocked, category, p.Name))
				return
			}
		}
	}
	messages := make([]airuntime.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, airuntime.Message{Role: airuntime.Role(m.Role), Content: m.Content})
	}
	outcome, err := h.aiRuntime.Chat(ctx, airuntime.Request{
		Model:    airuntime.ModelID(req.Model),
		Messages: messages,
	})
	if err != nil {
		// The named errors ride their explanations: the skips on no-route,
		// the attempts on all-failed, the attempts on budget expiry —
		// fail-closed, not fail-blind. The status is set BEFORE the body
		// (writeJSONStatus), exactly like writeJSONError — a body-first
		// write would swallow the status.
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
			// Request-shape validation: the contract's own bounds (roles,
			// content runes, message count) with the contract's own words.
			writeJSONError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	resp := outcome.Response
	attempts := attemptViews(outcome.Attempts)
	if attempts == nil {
		attempts = []aiAttemptView{} // a stable JSON shape, not a null
	}
	writeJSON(w, aiChatResponse{
		Provider:     string(resp.Provider),
		Model:        string(resp.Model),
		Message:      aiChatMessage{Role: string(resp.Message.Role), Content: resp.Message.Content},
		FinishReason: string(resp.Finish),
		Usage:        aiUsageView{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens},
		Timestamp:    resp.Timestamp.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Attempts:     attempts,
	})
}

// writeJSONStatus mirrors writeJSONError for structured bodies: the
// status is set BEFORE the body is written (a body-first write commits
// an implicit 200 and swallows the intended status).
func writeJSONStatus(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}

func attemptViews(attempts []airt.Attempt) []aiAttemptView {
	if len(attempts) == 0 {
		return nil
	}
	views := make([]aiAttemptView, 0, len(attempts))
	for _, a := range attempts {
		views = append(views, aiAttemptView{
			Provider: string(a.Provider),
			Tier:     int(a.Tier),
			Role:     string(a.Role),
			Error:    a.Error,
		})
	}
	return views
}

type aiSkipView struct {
	Provider string `json:"provider"`
	Tier     int    `json:"tier"`
	Health   string `json:"health"`
	Rule     string `json:"rule"`
}

func skipViews(skips []airuntime.Skip) []aiSkipView {
	if len(skips) == 0 {
		return nil
	}
	views := make([]aiSkipView, 0, len(skips))
	for _, s := range skips {
		views = append(views, aiSkipView{
			Provider: string(s.Provider),
			Tier:     int(s.Tier),
			Health:   string(s.Health),
			Rule:     s.Rule,
		})
	}
	return views
}
