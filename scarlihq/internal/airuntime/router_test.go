package airuntime

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStack(t *testing.T) (*ModelRegistry, *HealthTable) {
	t.Helper()
	registry, err := NewModelRegistry(16)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	// One model served by all four tiers — the full failover surface.
	for _, p := range DefaultFailoverOrder {
		if err := registry.Register(p, "qwen3-14b"); err != nil {
			t.Fatalf("register %s: %v", p, err)
		}
	}
	return registry, NewHealthTable()
}

func healthyAll(t *testing.T, table *HealthTable) {
	t.Helper()
	for i, p := range DefaultFailoverOrder {
		if err := table.Observe(Observation{Provider: p, State: HealthHealthy, At: probeT0.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatalf("observe %s healthy: %v", p, err)
		}
	}
}

func TestPlanNilInputRejection(t *testing.T) {
	registry, table := newTestStack(t)
	router := Router{}
	if _, err := router.Plan(nil, table, "qwen3-14b"); !errors.Is(err, ErrNilRouterInput) {
		t.Fatalf("nil registry must be rejected, got %v", err)
	}
	if _, err := router.Plan(registry, nil, "qwen3-14b"); !errors.Is(err, ErrNilRouterInput) {
		t.Fatalf("nil health table must be rejected, got %v", err)
	}
}

func TestPlanUnknownModel(t *testing.T) {
	registry, table := newTestStack(t)
	healthyAll(t, table)
	plan, err := Router{}.Plan(registry, table, "never-registered")
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("unknown model must be ErrUnknownModel (the router never guesses a model), got %v", err)
	}
	if plan.Steps != nil || plan.Skips != nil {
		t.Fatal("no plan may be fabricated for an unknown model")
	}
	// A malformed model id is a validation error, never a guess either.
	_, err = Router{}.Plan(registry, table, "Bad/Model")
	if err == nil {
		t.Fatal("malformed model id must be rejected")
	}
}

func TestPlanPrimaryFallbackInTierOrder(t *testing.T) {
	registry, table := newTestStack(t)
	healthyAll(t, table)
	plan, err := Router{}.Plan(registry, table, "qwen3-14b")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	wantProviders := []Provider{ProviderSGLang, ProviderVLLM, ProviderBeeLlama, ProviderOllama}
	wantTiers := []Tier{1, 2, 3, 4}
	if len(plan.Steps) != 4 {
		t.Fatalf("plan has %d steps, want 4 (all serving)", len(plan.Steps))
	}
	for i := range wantProviders {
		step := plan.Steps[i]
		if step.Provider != wantProviders[i] || step.Tier != wantTiers[i] {
			t.Fatalf("step %d = %s/t%d, want %s/t%d (tier order)", i, step.Provider, step.Tier, wantProviders[i], wantTiers[i])
		}
		if step.Health != HealthHealthy {
			t.Fatalf("step %d must cite the health state it was planned under, got %s", i, step.Health)
		}
		wantRole := PlanFallback
		if i == 0 {
			wantRole = PlanPrimary
		}
		if step.Role != wantRole {
			t.Fatalf("step %d role = %s, want %s", i, step.Role, wantRole)
		}
	}
	if len(plan.Skips) != 0 {
		t.Fatalf("all-serving plan must carry no skips, got %d", len(plan.Skips))
	}
}

func TestPlanDegradedServes(t *testing.T) {
	registry, table := newTestStack(t)
	if err := table.Observe(Observation{Provider: ProviderSGLang, State: HealthDegraded, At: probeT0}); err != nil {
		t.Fatalf("observe degraded: %v", err)
	}
	for _, p := range []Provider{ProviderVLLM, ProviderBeeLlama, ProviderOllama} {
		if err := table.Observe(Observation{Provider: p, State: HealthHealthy, At: probeT0.Add(time.Minute)}); err != nil {
			t.Fatalf("observe %s: %v", p, err)
		}
	}
	plan, err := Router{}.Plan(registry, table, "qwen3-14b")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Steps) != 4 {
		t.Fatalf("degraded tier must still serve (honestly), want 4 steps, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Provider != ProviderSGLang || plan.Steps[0].Health != HealthDegraded || plan.Steps[0].Role != PlanPrimary {
		t.Fatalf("degraded primary must be sglang citing DEGRADED, got %+v", plan.Steps[0])
	}
}

func TestPlanUnknownFailClosed(t *testing.T) {
	registry, table := newTestStack(t)
	// sglang NEVER observed (UNKNOWN); the rest healthy.
	for _, p := range []Provider{ProviderVLLM, ProviderBeeLlama, ProviderOllama} {
		if err := table.Observe(Observation{Provider: p, State: HealthHealthy, At: probeT0}); err != nil {
			t.Fatalf("observe %s: %v", p, err)
		}
	}
	plan, err := Router{}.Plan(registry, table, "qwen3-14b")
	if err != nil {
		t.Fatalf("plan must succeed with a fallback route: %v", err)
	}
	if plan.Steps[0].Provider != ProviderVLLM || plan.Steps[0].Role != PlanPrimary {
		t.Fatalf("UNKNOWN tier skipped to fallback: got %+v", plan.Steps[0])
	}
	if len(plan.Skips) != 1 {
		t.Fatalf("the skipped UNKNOWN tier must be visible, want 1 skip, got %d", len(plan.Skips))
	}
	skip := plan.Skips[0]
	if skip.Provider != ProviderSGLang || skip.Health != HealthUnknown {
		t.Fatalf("skip must name the UNKNOWN tier, got %+v", skip)
	}
	if !strings.Contains(skip.Rule, "rule/health-unknown") {
		t.Fatalf("skip must fire the named rule rule/health-unknown, got %q", skip.Rule)
	}
	if !strings.Contains(skip.Rule, "sglang") {
		t.Fatalf("the skip rule must name the provider, got %q", skip.Rule)
	}
}

func TestPlanUnavailableSkipsToFallback(t *testing.T) {
	registry, table := newTestStack(t)
	if err := table.Observe(Observation{Provider: ProviderSGLang, State: HealthUnavailable, At: probeT0}); err != nil {
		t.Fatalf("observe sglang unavailable: %v", err)
	}
	if err := table.Observe(Observation{Provider: ProviderVLLM, State: HealthHealthy, At: probeT0.Add(time.Minute)}); err != nil {
		t.Fatalf("observe vllm: %v", err)
	}
	// beellama/ollama never observed → UNKNOWN skips.
	plan, err := Router{}.Plan(registry, table, "qwen3-14b")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Steps[0].Provider != ProviderVLLM {
		t.Fatalf("UNAVAILABLE tier must skip to fallback, got %s", plan.Steps[0].Provider)
	}
	if len(plan.Skips) != 3 {
		t.Fatalf("sglang UNAVAILABLE + beellama/ollama UNKNOWN = 3 skips, got %d", len(plan.Skips))
	}
	var unavailableRule, unknownRule string
	for _, skip := range plan.Skips {
		if skip.Provider == ProviderSGLang {
			unavailableRule = skip.Rule
		}
		if skip.Provider == ProviderBeeLlama {
			unknownRule = skip.Rule
		}
		if !strings.Contains(skip.Rule, "rule/health-") {
			t.Fatalf("every skip fires a named rule, got %q", skip.Rule)
		}
	}
	if !strings.Contains(unavailableRule, "rule/health-unavailable") {
		t.Fatalf("UNAVAILABLE skip must fire rule/health-unavailable, got %q", unavailableRule)
	}
	if !strings.Contains(unknownRule, "rule/health-unknown") {
		t.Fatalf("UNKNOWN skip must fire rule/health-unknown, got %q", unknownRule)
	}
	if unavailableRule == unknownRule {
		t.Fatal("the two skip flavors must be distinguishable (unknown ≠ unavailable)")
	}
}

func TestPlanAllNonServingErrNoRoute(t *testing.T) {
	registry, table := newTestStack(t)
	// Every candidate non-serving: sglang+vllm UNAVAILABLE, beellama+ollama UNKNOWN.
	if err := table.Observe(Observation{Provider: ProviderSGLang, State: HealthUnavailable, At: probeT0}); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if err := table.Observe(Observation{Provider: ProviderVLLM, State: HealthUnavailable, At: probeT0}); err != nil {
		t.Fatalf("observe: %v", err)
	}
	plan, err := Router{}.Plan(registry, table, "qwen3-14b")
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("all-non-serving must be ErrNoRoute — never an empty plan as success, got %v", err)
	}
	// Fail-closed, not fail-blind: the named skips return alongside the error.
	if len(plan.Skips) != 4 {
		t.Fatalf("ErrNoRoute must carry all 4 skips, got %d", len(plan.Skips))
	}
	if len(plan.Steps) != 0 {
		t.Fatalf("no step may exist when nothing serves, got %d", len(plan.Steps))
	}
}

func TestPlanDeterminism100x(t *testing.T) {
	registry, table := newTestStack(t)
	if err := table.Observe(Observation{Provider: ProviderSGLang, State: HealthHealthy, At: probeT0}); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if err := table.Observe(Observation{Provider: ProviderVLLM, State: HealthDegraded, At: probeT0.Add(time.Second)}); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if err := table.Observe(Observation{Provider: ProviderBeeLlama, State: HealthUnavailable, At: probeT0.Add(2 * time.Second)}); err != nil {
		t.Fatalf("observe: %v", err)
	}
	router := Router{}
	first, firstErr := router.Plan(registry, table, "qwen3-14b")
	for i := 0; i < 100; i++ {
		again, againErr := router.Plan(registry, table, "qwen3-14b")
		if (firstErr == nil) != (againErr == nil) {
			t.Fatalf("error determinism broke at repetition %d", i)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("plan drifted at repetition %d: %+v vs %+v", i, first, again)
		}
	}
	if len(first.Steps) != 2 || len(first.Skips) != 2 {
		t.Fatalf("mixed plan shape: %d steps + %d skips", len(first.Steps), len(first.Skips))
	}
}

func TestConcurrentRegisterObservePlan(t *testing.T) {
	// Race gate: registration, observation and planning run concurrently —
	// the registry/table stay consistent, plans stay deterministic once the
	// inputs settle.
	registry, table := newTestStack(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			model := ModelID(string(rune('a'+n)) + "-model")
			for _, p := range DefaultFailoverOrder {
				_ = registry.Register(p, model)
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p := DefaultFailoverOrder[n%len(DefaultFailoverOrder)]
			_ = table.Observe(Observation{Provider: p, State: HealthHealthy, At: probeT0.Add(time.Duration(n) * time.Second)})
		}(i)
	}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Router{}.Plan(registry, table, "qwen3-14b")
		}()
	}
	wg.Wait()
	// After the dust settles, the plan over the settled inputs is deterministic.
	first, err := Router{}.Plan(registry, table, "qwen3-14b")
	if err != nil {
		t.Fatalf("plan after concurrency: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, againErr := Router{}.Plan(registry, table, "qwen3-14b")
		if againErr != nil || !reflect.DeepEqual(first, again) {
			t.Fatalf("post-concurrency determinism broke at repetition %d", i)
		}
	}
}
