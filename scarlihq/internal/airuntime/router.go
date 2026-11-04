package airuntime

import (
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------
// The deterministic router (pure — it never executes anything)
// ---------------------------------------------------------------------------

var (
	// ErrUnknownModel is returned when the model is not in the registry.
	// The router never guesses a model — an unregistered id is an error,
	// not a creative route.
	ErrUnknownModel = errors.New("airuntime: model is not registered (the router never guesses a model)")

	// ErrNoRoute is returned when every candidate is non-serving. An
	// empty plan is never returned as success — the named skips return
	// alongside the error (fail-closed, not fail-blind).
	ErrNoRoute = errors.New("airuntime: no serving provider for the model (fail-closed, not fail-blind)")

	// ErrNilRouterInput is returned when the registry or the health table
	// is nil — the router plans from REAL state, never from defaults.
	ErrNilRouterInput = errors.New("airuntime: nil router input (registry and health table are required)")
)

// PlanRole labels a step's position in the failover plan: the first
// serving candidate (tier order) is primary, every later serving
// candidate is a fallback.
type PlanRole string

const (
	PlanPrimary  PlanRole = "primary"
	PlanFallback PlanRole = "fallback"
)

// PlanStep is one ordered attempt: which provider, at which tier, under
// the health state it was PLANNED with, and its role in the plan.
type PlanStep struct {
	Provider Provider
	Tier     Tier
	Health   HealthState
	Role     PlanRole
}

// Skip is a candidate that did not become a step, with the rule that
// fired. UNKNOWN and UNAVAILABLE candidates are skipped, never silently
// dropped — the rule names the provider and the reason.
type Skip struct {
	Provider Provider
	Tier     Tier
	Health   HealthState
	Rule     string
}

// Plan is the ordered failover plan plus the skips that explain the
// absences. Deterministic: same registry + health + model, same plan,
// byte for byte.
type Plan struct {
	Model ModelID
	Steps []PlanStep
	Skips []Skip
}

// Router is the pure deterministic planner. Candidates are walked in
// DefaultFailoverOrder (the v19.0.6-locked tier order); every step cites
// the health state it was planned under; every skip fires a named rule
// (rule/health-unknown / rule/health-unavailable). It has no state, no
// clock, no randomness, no network, no LLM — and it NEVER executes
// anything: a plan is an ordered list of attempts, nothing more.
// Execution is the CF-2 adapters' job, behind the fabric's approvals.
type Router struct{}

// Plan computes the failover plan for model over the registry and the
// health table. Same inputs, same plan, byte for byte. Fail-closed
// errors: ErrUnknownModel (the router never guesses a model) and
// ErrNoRoute (all candidates non-serving — never an empty plan as
// success; the named skips return alongside the error).
func (Router) Plan(registry *ModelRegistry, health *HealthTable, model ModelID) (Plan, error) {
	if registry == nil || health == nil {
		return Plan{}, ErrNilRouterInput
	}
	if err := model.Validate(); err != nil {
		return Plan{}, err
	}
	plan := Plan{Model: model}
	servingSeen := false
	anyCandidate := false
	for _, p := range DefaultFailoverOrder {
		if !registry.ServesModel(p, model) {
			continue
		}
		anyCandidate = true
		tier, err := p.Tier()
		if err != nil {
			return Plan{}, err // cannot happen for enumerated providers — honest guard
		}
		obs := health.Snapshot(p)
		switch obs.State {
		case HealthHealthy, HealthDegraded:
			role := PlanFallback
			if !servingSeen {
				role = PlanPrimary
				servingSeen = true
			}
			plan.Steps = append(plan.Steps, PlanStep{Provider: p, Tier: tier, Health: obs.State, Role: role})
		case HealthUnknown:
			plan.Skips = append(plan.Skips, Skip{
				Provider: p,
				Tier:     tier,
				Health:   obs.State,
				Rule:     fmt.Sprintf("rule/health-unknown: %s never observed — UNKNOWN never serves", p),
			})
		case HealthUnavailable:
			plan.Skips = append(plan.Skips, Skip{
				Provider: p,
				Tier:     tier,
				Health:   obs.State,
				Rule:     fmt.Sprintf("rule/health-unavailable: %s observed UNAVAILABLE at %s", p, obs.At.UTC().Format("2006-01-02T15:04:05Z07:00")),
			})
		default:
			return Plan{}, fmt.Errorf("airuntime: provider %s carries unknown health state %q (closed enumeration violated)", p, obs.State)
		}
	}
	if !anyCandidate {
		return Plan{}, fmt.Errorf("%w: %q", ErrUnknownModel, model)
	}
	if len(plan.Steps) == 0 {
		return plan, fmt.Errorf("%w: model %q — %d candidates, all skipped (rules: see plan skips)", ErrNoRoute, model, len(plan.Skips))
	}
	return plan, nil
}
