package airt

import (
	"context"
	"fmt"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// ---------------------------------------------------------------------------
// The failover executor (the CF-1 router's plan, actually executed)
// ---------------------------------------------------------------------------

// Attempt is one FAILED execution step: which tier was tried, at which
// tier position, in which plan role, and why it was skipped past. The
// successful tier is the response's own provider attribution — attempts
// record only the failures (attribution survives failover: the caller
// sees WHICH tier answered and which tiers did not).
type Attempt struct {
	Provider airuntime.Provider
	Tier     airuntime.Tier
	Role     airuntime.PlanRole
	Error    string
}

// ChatOutcome is the full result of one chat execution: the response
// (zero value when every step failed), the failed attempts in plan
// order, and the plan's skips (non-serving candidates with their named
// rules) — fail-closed, not fail-blind: the explanation always rides
// along, on success AND on failure.
type ChatOutcome struct {
	Response airuntime.Response
	Attempts []Attempt
	Skips    []airuntime.Skip
}

// Runtime composes the CF-1 contract (registry, health table, router)
// with the CF-2 transports (adapters, prober) into the executing LLM
// layer. The router stays PURE and decides the plan; this type walks the
// plan and attributes the answer — the LLM never decides anything here:
// Chat produces normalized text, nothing else (proposals to the fabric
// are CF-3, behind its approvals).
type Runtime struct {
	registry *airuntime.ModelRegistry
	health   *airuntime.HealthTable
	router   airuntime.Router
	prober   *Prober
	adapters map[airuntime.Provider]Adapter
}

// NewRuntime constructs the executing runtime. Fail-closed: the
// registry, the health table and the prober are required (nil rejected
// by name), and the adapter set must cover EXACTLY the closed
// four-provider stack — all four tiers always present, health gates
// routing (a missing tier would be a silent fail-open hole in failover).
func NewRuntime(registry *airuntime.ModelRegistry, health *airuntime.HealthTable, prober *Prober, adapters map[airuntime.Provider]Adapter) (*Runtime, error) {
	if registry == nil {
		return nil, fmt.Errorf("%w: model registry is required", ErrNilRuntimeInput)
	}
	if health == nil {
		return nil, fmt.Errorf("%w: health table is required", ErrNilRuntimeInput)
	}
	if prober == nil {
		return nil, fmt.Errorf("%w: prober is required", ErrNilRuntimeInput)
	}
	if adapters == nil {
		return nil, fmt.Errorf("%w: adapters are required", ErrNilRuntimeInput)
	}
	for _, p := range airuntime.AllProviders() {
		adapter, ok := adapters[p]
		if !ok || adapter == nil {
			return nil, fmt.Errorf("%w: no adapter for %s", ErrIncompleteAdapterSet, p)
		}
		if adapter.Provider() != p {
			return nil, fmt.Errorf("%w: adapter for slot %s claims provider %q", ErrIncompleteAdapterSet, p, adapter.Provider())
		}
	}
	for p := range adapters {
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("airt: adapter set carries a foreign provider %q: %w", p, err)
		}
	}
	return &Runtime{
		registry: registry,
		health:   health,
		router:   airuntime.Router{},
		prober:   prober,
		adapters: adapters,
	}, nil
}

// Register is the operator act "provider p serves model m" — a direct,
// validated passthrough onto the CF-1 registry (duplicates rejected,
// distinct-model bound enforced, no auto-discovery: the registry IS the
// routing surface).
func (rt *Runtime) Register(p airuntime.Provider, m airuntime.ModelID) error {
	return rt.registry.Register(p, m)
}

// Chat executes one normalized chat request. The flow, in order:
// validate the request shape; PLAN with the pure CF-1 router (same
// inputs, same plan — the router is unchanged and still decides); walk
// the plan steps in tier order; the first adapter that answers wins and
// its attribution rides the response; every failed step is recorded as
// an Attempt with its error. Errors:
//
//	validation       → the request shape (caller maps to 400)
//	ErrUnknownModel  → the router never guesses a model (404)
//	ErrNoRoute       → all candidates non-serving; outcome.Skips says
//	                   why, by named rule (503)
//	ErrAllAdaptersFailed → every step failed; outcome.Attempts says
//	                   which tiers and why (502)
//
// The plan is replanned per request from LIVE state — a probe recorded
// between two requests changes the second request's plan (health gates
// routing, always freshly).
func (rt *Runtime) Chat(ctx context.Context, req airuntime.Request) (ChatOutcome, error) {
	if err := req.Validate(); err != nil {
		return ChatOutcome{}, err
	}
	plan, err := rt.router.Plan(rt.registry, rt.health, req.Model)
	if err != nil {
		// ErrUnknownModel or ErrNoRoute — the plan (and its skips) rides
		// along in the outcome: fail-closed, not fail-blind.
		return ChatOutcome{Skips: plan.Skips}, err
	}
	outcome := ChatOutcome{Skips: plan.Skips}
	for _, step := range plan.Steps {
		resp, err := rt.adapters[step.Provider].Chat(ctx, req.Clone())
		if err != nil {
			outcome.Attempts = append(outcome.Attempts, Attempt{
				Provider: step.Provider,
				Tier:     step.Tier,
				Role:     step.Role,
				Error:    err.Error(),
			})
			continue // failover: the plan's next step, in tier order
		}
		outcome.Response = resp
		return outcome, nil
	}
	return outcome, fmt.Errorf("%w: model %q — %d steps, all failed", ErrAllAdaptersFailed, req.Model, len(outcome.Attempts))
}

// ProbeAll triggers one probe round NOW and records it into the health
// table — the operator's refresh (POST /api/ai/probe). Observations
// return in tier order; a dead tier is observed UNAVAILABLE, never
// omitted.
func (rt *Runtime) ProbeAll(ctx context.Context) ([]airuntime.Observation, error) {
	return rt.prober.ProbeAllAndObserve(ctx, rt.health)
}

// StartProber starts the background freshness loop over the runtime's
// own health table (the Prober.Start contract: runs for the process
// lifetime; the stop handle exists for tests).
func (rt *Runtime) StartProber(interval time.Duration) (stop func(), err error) {
	return rt.prober.Start(interval, rt.health)
}

// HealthEntry is one provider's slice of the health view: the latest
// observed state and when (empty when never observed — UNKNOWN, which
// never serves).
type HealthEntry struct {
	Provider     airuntime.Provider
	Tier         airuntime.Tier
	State        airuntime.HealthState
	LastObserved string
	Serves       bool
}

// HealthView returns the tier-ordered health snapshot for every provider
// in the closed stack — UNKNOWN included (never-observed is a state
// worth seeing, not a gap).
func (rt *Runtime) HealthView() []HealthEntry {
	entries := make([]HealthEntry, 0, len(airuntime.AllProviders()))
	for _, p := range airuntime.AllProviders() {
		tier, err := p.Tier()
		if err != nil {
			continue // cannot happen for enumerated providers — honest guard
		}
		obs := rt.health.Snapshot(p)
		last := ""
		if !obs.At.IsZero() {
			last = obs.At.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		entries = append(entries, HealthEntry{
			Provider:     p,
			Tier:         tier,
			State:        obs.State,
			LastObserved: last,
			Serves:       obs.State.Serves(),
		})
	}
	return entries
}

// RegistryView returns the CF-1 registry listing (tier-ordered
// providers, ascending models) — a passthrough read for the REST
// surface; the registry itself stays the single routing surface.
func (rt *Runtime) RegistryView() []airuntime.RegistryEntry {
	return rt.registry.List()
}
