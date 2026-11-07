package api

import (
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
)

// ---------------------------------------------------------------------------
// Identity-bound request authorization (v19.6.7, KPK-19.6.6-P1-01).
//
// The v19.6.6 residual: the family-profile policy was decided against a
// CALLER-DECLARED reference (body field / X-Scarlix-Profile header) while
// the auth boundary knew one household Bearer token — a holder could omit
// the reference (skip the policy) or self-select the zmor admin profile.
//
// v19.6.7: the auth middleware resolves EVERY request's principal from the
// presented Bearer token (per-profile token index first, the household
// token = the admin identity) and stores it in the request context. The
// policy-aware boundaries (/api/mode POST, /api/ai/chat, MCP
// scarlix_mode_set) all decide through the ONE shared
// profiles.Authenticator.Authorize — identity is bound, references are
// checked against it, omissions are DERIVED from it.
//
// Mutating operator surfaces (compute, agent kernel/fabric, approvals,
// probes, proposals, model registration) additionally require the ADMIN
// principal — a family member's per-profile token is refused there, not
// silently honored.
// ---------------------------------------------------------------------------

// ConfigureProfileAuthz activates per-profile authentication (multi-user
// mode): the token index binds profile identities to their own Bearer
// tokens. Nil is rejected — there is no half-bound identity.
func (h *Handler) ConfigureProfileAuthz(index *profiles.TokenIndex) error {
	if index == nil {
		return errors.New("api: the profile token index is required")
	}
	if h.authz == nil {
		return errors.New("api: the household token is not configured — per-profile auth cannot bind to it")
	}
	if err := h.authz.SetTokenIndex(index); err != nil {
		return err
	}
	return nil
}

// ConfigureBudgetLedger activates the ENFORCED monthly token budget
// (KPK-19.6.6-P2-02): /api/ai/chat reserves before forwarding and settles
// the provider-reported usage after. Without a ledger the declared budgets
// degrade to informational metadata (a loud startup warning + a
// request-stream warning say so — the documented degraded policy).
func (h *Handler) ConfigureBudgetLedger(ledger *profiles.BudgetLedger) error {
	if ledger == nil {
		return errors.New("api: the budget ledger is required")
	}
	h.budget = ledger
	h.budgetEnforced = true
	return nil
}

// nowUTC is the single clock read for budget accounting (the monthly
// window is a UTC calendar-month key, explicitly).
func nowUTC() time.Time { return time.Now().UTC() }

// authorizeProfile is the REST boundary's entry into the ONE shared
// authorization function (profiles.Authenticator.Authorize) — the same
// decision code serves /api/mode, /api/ai/chat and MCP. declared may be
// empty (the profile is DERIVED from the authenticated principal). The
// returned bool is false when the decision was a refusal already written
// to the response; a nil profile with true is the explicit single-user
// legacy contract (no profile files at all).
func (h *Handler) authorizeProfile(w http.ResponseWriter, r *http.Request, declared, action string) (*profiles.Profile, bool) {
	if h.authz == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "profile authorization is unavailable (SCARLIHQ_TOKEN not configured)")
		return nil, false
	}
	principal := profiles.PrincipalFromContext(r.Context())
	p, decision := h.authz.Authorize(principal, declared, action)
	if decision.Deny != nil {
		// The named refusals carry their HTTP class: a broken/unloadable
		// DECLARED profile file is the 400 fail-closed (the v19.6.6
		// contract, preserved); an identity mismatch or an unavailable
		// DERIVED policy is 403; an unauthenticated context is 401.
		switch {
		case errors.Is(decision.Deny, profiles.ErrProfileMismatch), errors.Is(decision.Deny, profiles.ErrProfileUnavailable):
			writeJSONError(w, http.StatusForbidden, decision.Deny.Error())
		case principal == nil:
			writeJSONError(w, http.StatusUnauthorized, decision.Deny.Error())
		default:
			writeJSONError(w, http.StatusBadRequest, decision.Deny.Error())
		}
		return nil, false
	}
	return p, true
}

// budgetDegradedWarned serializes the one-time degraded-mode notice.
var budgetDegradedWarned sync.Once

// budgetReserve enforces the profile's monthly budget on one chat request
// (KPK-19.6.6-P2-02): reserve-before-forward with the estimate; the
// refusal is a STABLE 429 carrying the quota state. When no ledger is
// configured the budget is informational — the documented degraded policy
// (stated once, loudly, in the request stream). The second return value
// is true ONLY when the reservation was refused and the refusal was
// already written to the response — the caller must stop there.
func (h *Handler) budgetReserve(w http.ResponseWriter, p *profiles.Profile, messageContents []string) (*profiles.Reservation, bool) {
	if p == nil || p.TokenBudget.Unlimited || p.TokenBudget.Monthly <= 0 {
		return nil, false // no applicable budget — nothing to enforce
	}
	if !h.budgetEnforced || h.budget == nil {
		budgetDegradedWarned.Do(func() {
			log.Printf("WARNING: profile %s declares a monthly token budget (%d) but the usage ledger is NOT configured — the budget is INFORMATIONAL metadata in this deployment until SCARLIHQ_AIRUNTIME_BUDGET_LEDGER is writable (KPK-19.6.6-P2-02: an unaccountable budget is not a budget)", p.Name, p.TokenBudget.Monthly)
		})
		return nil, false
	}
	estimate := profiles.EstimateChatTokens(messageContents)
	res, err := h.budget.Reserve(p.Name, p.TokenBudget.Monthly, estimate, nowUTC())
	if err != nil {
		if errors.Is(err, profiles.ErrQuotaExceeded) {
			state := h.budget.State(p.Name, p.TokenBudget.Monthly, nowUTC())
			writeJSONStatus(w, http.StatusTooManyRequests, map[string]interface{}{
				"status":  "error",
				"message": err.Error(),
				"quota":   state,
			})
			return nil, true
		}
		// Ledger unavailability fails CLOSED: an unaccountable budget is
		// not a budget (the audit's requirement).
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return nil, true
	}
	return res, false
}
