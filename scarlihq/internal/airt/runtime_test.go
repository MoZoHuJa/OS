package airt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// fakeAdapter is an in-memory Adapter: no network, a configurable
// response or error. The executor is thereby testable WITHOUT transport
// — the interface exists for exactly this.
type fakeAdapter struct {
	provider airuntime.Provider
	response airuntime.Response
	err      error
	calls    int
	mu       sync.Mutex
}

func (f *fakeAdapter) Provider() airuntime.Provider { return f.provider }

func (f *fakeAdapter) Chat(ctx context.Context, req airuntime.Request) (airuntime.Response, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return airuntime.Response{}, f.err
	}
	// Attribution discipline: the fake stamps itself as the answering
	// tier, exactly like the real transport does.
	return airuntime.Response{
		Provider:  f.provider,
		Model:     req.Model,
		Message:   f.response.Message,
		Finish:    f.response.Finish,
		Usage:     f.response.Usage,
		Timestamp: time.Now().UTC(),
	}, nil
}

func (f *fakeAdapter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newFakeStack builds the runtime over four fake adapters with the model
// registered on all tiers and all tiers observed HEALTHY.
func newFakeStack(t *testing.T, fakes map[airuntime.Provider]*fakeAdapter) (*Runtime, *airuntime.ModelRegistry, *airuntime.HealthTable) {
	t.Helper()
	registry, err := airuntime.NewModelRegistry(16)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, p := range airuntime.AllProviders() {
		if err := registry.Register(p, "qwen3-14b"); err != nil {
			t.Fatalf("register %s: %v", p, err)
		}
	}
	table := airuntime.NewHealthTable()
	for i, p := range airuntime.AllProviders() {
		if err := table.Observe(airuntime.Observation{Provider: p, State: airuntime.HealthHealthy, At: time.Now().UTC().Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatalf("observe %s: %v", p, err)
		}
	}
	adapters := make(map[airuntime.Provider]Adapter, len(fakes))
	for p, f := range fakes {
		adapters[p] = f
	}
	prober, err := NewProber(DefaultEndpoints(), DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	runtime, err := NewRuntime(registry, table, prober, adapters)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	return runtime, registry, table
}

func fourFakes() map[airuntime.Provider]*fakeAdapter {
	respond := func(p airuntime.Provider) *fakeAdapter {
		return &fakeAdapter{provider: p, response: airuntime.Response{
			Message: airuntime.Message{Role: airuntime.RoleAssistant, Content: fmt.Sprintf("answer from %s", p)},
			Finish:  airuntime.FinishStop,
			Usage:   airuntime.Usage{InputTokens: 5, OutputTokens: 2},
		}}
	}
	return map[airuntime.Provider]*fakeAdapter{
		airuntime.ProviderSGLang:   respond(airuntime.ProviderSGLang),
		airuntime.ProviderVLLM:     respond(airuntime.ProviderVLLM),
		airuntime.ProviderBeeLlama: respond(airuntime.ProviderBeeLlama),
		airuntime.ProviderOllama:   respond(airuntime.ProviderOllama),
	}
}

func validChatRequest() airuntime.Request {
	return airuntime.Request{
		Model:    "qwen3-14b",
		Messages: []airuntime.Message{{Role: airuntime.RoleUser, Content: "hello"}},
	}
}

func TestRuntimeConstructionFailClosed(t *testing.T) {
	fakes := fourFakes()
	adapters := make(map[airuntime.Provider]Adapter)
	for p, f := range fakes {
		adapters[p] = f
	}
	prober, err := NewProber(DefaultEndpoints(), DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	registry, _ := airuntime.NewModelRegistry(4)
	table := airuntime.NewHealthTable()
	// Nil inputs, each named.
	if _, err := NewRuntime(nil, table, prober, adapters); !errors.Is(err, ErrNilRuntimeInput) {
		t.Fatalf("nil registry must be ErrNilRuntimeInput, got %v", err)
	}
	if _, err := NewRuntime(registry, nil, prober, adapters); !errors.Is(err, ErrNilRuntimeInput) {
		t.Fatalf("nil health table must be ErrNilRouterInput, got %v", err)
	}
	if _, err := NewRuntime(registry, table, nil, adapters); !errors.Is(err, ErrNilRuntimeInput) {
		t.Fatalf("nil prober must be ErrNilRuntimeInput, got %v", err)
	}
	if _, err := NewRuntime(registry, table, prober, nil); !errors.Is(err, ErrNilRuntimeInput) {
		t.Fatalf("nil adapters must be ErrNilRuntimeInput, got %v", err)
	}
	// Missing tier: named, fail-closed (health gates routing, construction must not).
	incomplete := make(map[airuntime.Provider]Adapter)
	for p, f := range fakes {
		if p == airuntime.ProviderBeeLlama {
			continue
		}
		incomplete[p] = f
	}
	_, err = NewRuntime(registry, table, prober, incomplete)
	if !errors.Is(err, ErrIncompleteAdapterSet) || !strings.Contains(err.Error(), "beellama") {
		t.Fatalf("a missing tier must fail closed and be named, got %v", err)
	}
	// A foreign provider in the set: rejected.
	foreign := make(map[airuntime.Provider]Adapter)
	for p, f := range fakes {
		foreign[p] = f
	}
	foreign["openai"] = &fakeAdapter{provider: "openai"}
	if _, err := NewRuntime(registry, table, prober, foreign); err == nil {
		t.Fatal("a foreign provider in the adapter set must be rejected (the closed set only)")
	}
	// An adapter claiming the wrong provider for its slot: rejected.
	mislabeled := make(map[airuntime.Provider]Adapter)
	for p, f := range fakes {
		mislabeled[p] = f
	}
	mislabeled[airuntime.ProviderSGLang] = &fakeAdapter{provider: airuntime.ProviderVLLM}
	if _, err := NewRuntime(registry, table, prober, mislabeled); err == nil {
		t.Fatal("an adapter claiming another provider's slot must be rejected")
	}
}

func TestRuntimeChatPrimaryAnswers(t *testing.T) {
	fakes := fourFakes()
	runtime, _, _ := newFakeStack(t, fakes)
	outcome, err := runtime.Chat(context.Background(), validChatRequest())
	if err != nil {
		t.Fatalf("chat must succeed through the healthy primary: %v", err)
	}
	if outcome.Response.Provider != airuntime.ProviderSGLang {
		t.Fatalf("the primary tier must answer, got %s", outcome.Response.Provider)
	}
	if len(outcome.Attempts) != 0 {
		t.Fatalf("a successful primary leaves no failed attempts, got %+v", outcome.Attempts)
	}
	if outcome.Response.Message.Content != "answer from sglang" {
		t.Fatalf("content must come from the answering tier, got %q", outcome.Response.Message.Content)
	}
	if outcome.Response.Timestamp.IsZero() {
		t.Fatal("attribution + required timestamp must ride the response")
	}
	// Only the primary was asked.
	for p, f := range fakes {
		want := 0
		if p == airuntime.ProviderSGLang {
			want = 1
		}
		if got := f.callCount(); got != want {
			t.Fatalf("%s must have been called %d times, got %d", p, want, got)
		}
	}
}

func TestRuntimeChatFailoverToFallback(t *testing.T) {
	fakes := fourFakes()
	// The primary fails; the plan walks to the next serving tier.
	fakes[airuntime.ProviderSGLang].err = errors.New("boom: HTTP 500")
	runtime, _, _ := newFakeStack(t, fakes)
	outcome, err := runtime.Chat(context.Background(), validChatRequest())
	if err != nil {
		t.Fatalf("failover must answer through the fallback: %v", err)
	}
	if outcome.Response.Provider != airuntime.ProviderVLLM {
		t.Fatalf("the tier-2 fallback must answer, got %s", outcome.Response.Provider)
	}
	if len(outcome.Attempts) != 1 {
		t.Fatalf("the failed primary must be recorded as exactly one attempt, got %+v", outcome.Attempts)
	}
	attempt := outcome.Attempts[0]
	if attempt.Provider != airuntime.ProviderSGLang || attempt.Tier != airuntime.TierSGLang || attempt.Role != airuntime.PlanPrimary {
		t.Fatalf("the attempt must name the failed primary w/ tier + role, got %+v", attempt)
	}
	if !strings.Contains(attempt.Error, "boom") {
		t.Fatalf("the attempt must carry the adapter error, got %q", attempt.Error)
	}
	if outcome.Attempts[0].Error == "" {
		t.Fatal("fail-closed, not fail-blind: the attempt error is required")
	}
}

func TestRuntimeChatMultiTierFailover(t *testing.T) {
	fakes := fourFakes()
	fakes[airuntime.ProviderSGLang].err = errors.New("down")
	fakes[airuntime.ProviderVLLM].err = errors.New("down")
	fakes[airuntime.ProviderBeeLlama].err = errors.New("down")
	runtime, _, _ := newFakeStack(t, fakes)
	outcome, err := runtime.Chat(context.Background(), validChatRequest())
	if err != nil {
		t.Fatalf("the tier-4 CPU fallback must catch the stack: %v", err)
	}
	if outcome.Response.Provider != airuntime.ProviderOllama {
		t.Fatalf("ollama (tier 4) must answer, got %s", outcome.Response.Provider)
	}
	if len(outcome.Attempts) != 3 {
		t.Fatalf("three failed tiers must be recorded in plan order, got %d", len(outcome.Attempts))
	}
	want := []airuntime.Provider{airuntime.ProviderSGLang, airuntime.ProviderVLLM, airuntime.ProviderBeeLlama}
	for i, p := range want {
		if outcome.Attempts[i].Provider != p {
			t.Fatalf("attempt %d must be %s (plan order), got %s", i, p, outcome.Attempts[i].Provider)
		}
	}
}

func TestRuntimeChatAllAdaptersFailed(t *testing.T) {
	fakes := fourFakes()
	for _, f := range fakes {
		f.err = errors.New("down")
	}
	runtime, _, _ := newFakeStack(t, fakes)
	outcome, err := runtime.Chat(context.Background(), validChatRequest())
	if !errors.Is(err, ErrAllAdaptersFailed) {
		t.Fatalf("all tiers failing must be ErrAllAdaptersFailed, got %v", err)
	}
	if len(outcome.Attempts) != 4 {
		t.Fatalf("the attempts return alongside the error (fail-closed, not fail-blind), got %d", len(outcome.Attempts))
	}
	if outcome.Response.Provider != "" {
		t.Fatal("no response may be fabricated when every step failed")
	}
}

func TestRuntimeChatUnknownModel(t *testing.T) {
	fakes := fourFakes()
	runtime, _, _ := newFakeStack(t, fakes)
	req := validChatRequest()
	req.Model = "never-registered"
	outcome, err := runtime.Chat(context.Background(), req)
	if !errors.Is(err, airuntime.ErrUnknownModel) {
		t.Fatalf("an unregistered model must surface the router's ErrUnknownModel (the runtime never guesses either), got %v", err)
	}
	// Nothing was executed.
	for p, f := range fakes {
		if got := f.callCount(); got != 0 {
			t.Fatalf("%s must not have been called for an unknown model, got %d calls", p, got)
		}
	}
	_ = outcome
}

func TestRuntimeChatNoRouteCarriesSkips(t *testing.T) {
	fakes := fourFakes()
	registry, err := airuntime.NewModelRegistry(4)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	table := airuntime.NewHealthTable()
	for _, p := range airuntime.AllProviders() {
		if err := registry.Register(p, "qwen3-14b"); err != nil {
			t.Fatalf("register %s: %v", p, err)
		}
	}
	// NO observations: every tier UNKNOWN — the router refuses.
	adapters := make(map[airuntime.Provider]Adapter)
	for p, f := range fakes {
		adapters[p] = f
	}
	prober, _ := NewProber(DefaultEndpoints(), DefaultProbeTimeout)
	runtime, err := NewRuntime(registry, table, prober, adapters)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	outcome, err := runtime.Chat(context.Background(), validChatRequest())
	if !errors.Is(err, airuntime.ErrNoRoute) {
		t.Fatalf("all-UNKNOWN must surface the router's ErrNoRoute, got %v", err)
	}
	if len(outcome.Skips) != 4 {
		t.Fatalf("the skips ride along in the outcome (fail-closed, not fail-blind), got %d", len(outcome.Skips))
	}
	for _, skip := range outcome.Skips {
		if !strings.Contains(skip.Rule, "rule/health-unknown") {
			t.Fatalf("each skip must cite the named rule, got %q", skip.Rule)
		}
	}
	// Nothing was executed.
	for p, f := range fakes {
		if got := f.callCount(); got != 0 {
			t.Fatalf("%s must not have been called when no route exists, got %d calls", p, got)
		}
	}
}

func TestRuntimeChatInvalidRequest(t *testing.T) {
	fakes := fourFakes()
	runtime, _, _ := newFakeStack(t, fakes)
	bad := airuntime.Request{Model: "qwen3-14b", Messages: nil}
	if _, err := runtime.Chat(context.Background(), bad); err == nil {
		t.Fatal("an invalid request must be rejected before planning")
	}
}

func TestRuntimeChatReplansFromLiveState(t *testing.T) {
	fakes := fourFakes()
	runtime, _, table := newFakeStack(t, fakes)
	// First chat: the primary answers.
	if _, err := runtime.Chat(context.Background(), validChatRequest()); err != nil {
		t.Fatalf("first chat: %v", err)
	}
	// The primary goes UNAVAILABLE between requests (a fresh probe would
	// record exactly this).
	if err := table.Observe(airuntime.Observation{Provider: airuntime.ProviderSGLang, State: airuntime.HealthUnavailable, At: time.Now().UTC().Add(time.Minute)}); err != nil {
		t.Fatalf("observe: %v", err)
	}
	outcome, err := runtime.Chat(context.Background(), validChatRequest())
	if err != nil {
		t.Fatalf("second chat: %v", err)
	}
	if outcome.Response.Provider != airuntime.ProviderVLLM {
		t.Fatalf("the replanned request must skip the unavailable tier, answered by %s", outcome.Response.Provider)
	}
	if got := fakes[airuntime.ProviderSGLang].callCount(); got != 1 {
		t.Fatalf("the unavailable tier must not be re-attempted (health gates routing, freshly), sglang calls=%d", got)
	}
}

// mutatingAdapter mutates the request it was handed — the copy-
// isolation proof in the OTHER direction: an adapter corrupting its
// request must never reach the caller's original (the runtime hands out
// a clone, exactly like the CF-1 contract demands).
type mutatingAdapter struct {
	provider airuntime.Provider
}

func (m *mutatingAdapter) Provider() airuntime.Provider { return m.provider }

func (m *mutatingAdapter) Chat(ctx context.Context, req airuntime.Request) (airuntime.Response, error) {
	req.Messages[0].Content = "corrupted-by-the-adapter" // would corrupt the caller if the slice were shared
	req.Model = "also-corrupted"
	return airuntime.Response{
		Provider:  m.provider,
		Model:     "qwen3-14b",
		Message:   airuntime.Message{Role: airuntime.RoleAssistant, Content: "ok"},
		Finish:    airuntime.FinishStop,
		Timestamp: time.Now().UTC(),
	}, nil
}

func TestRuntimeChatRequestCopyIsolation(t *testing.T) {
	fakes := fourFakes()
	adapters := make(map[airuntime.Provider]Adapter)
	for p, f := range fakes {
		adapters[p] = f
	}
	adapters[airuntime.ProviderSGLang] = &mutatingAdapter{provider: airuntime.ProviderSGLang}
	prober, err := NewProber(DefaultEndpoints(), DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	registry, err := airuntime.NewModelRegistry(4)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, p := range airuntime.AllProviders() {
		if err := registry.Register(p, "qwen3-14b"); err != nil {
			t.Fatalf("register %s: %v", p, err)
		}
	}
	table := airuntime.NewHealthTable()
	for i, p := range airuntime.AllProviders() {
		if err := table.Observe(airuntime.Observation{Provider: p, State: airuntime.HealthHealthy, At: time.Now().UTC().Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatalf("observe %s: %v", p, err)
		}
	}
	runtime, err := NewRuntime(registry, table, prober, adapters)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	req := validChatRequest()
	if _, err := runtime.Chat(context.Background(), req); err != nil {
		t.Fatalf("chat: %v", err)
	}
	// The adapter corrupted ITS copy; the caller's request must be
	// immune — the runtime hands the plan a clone, not the original.
	if req.Messages[0].Content != "hello" {
		t.Fatalf("the caller's request must be immune to adapter-side mutation (copy isolation), got %q", req.Messages[0].Content)
	}
	if req.Model != "qwen3-14b" {
		t.Fatalf("the caller's model must be immune to adapter-side mutation, got %q", req.Model)
	}
}

func TestRuntimeChatConcurrent(t *testing.T) {
	fakes := fourFakes()
	// The primary fails under concurrency: every caller failovers.
	fakes[airuntime.ProviderSGLang].err = errors.New("primary flaky")
	runtime, _, _ := newFakeStack(t, fakes)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcome, err := runtime.Chat(context.Background(), validChatRequest())
			if err != nil {
				t.Errorf("concurrent chat %d: %v", i, err)
				return
			}
			if outcome.Response.Provider != airuntime.ProviderVLLM {
				t.Errorf("concurrent chat %d must failover to vllm, got %s", i, outcome.Response.Provider)
			}
		}(i)
	}
	wg.Wait()
	if got := fakes[airuntime.ProviderVLLM].callCount(); got != 20 {
		t.Fatalf("vllm must have answered all 20 concurrent chats, got %d", got)
	}
}

func TestRuntimeRegisterPassthrough(t *testing.T) {
	fakes := fourFakes()
	runtime, _, _ := newFakeStack(t, fakes) // qwen3-14b on all four tiers
	if err := runtime.Register(airuntime.ProviderOllama, "qwen3:4b"); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Duplicate: the registry's own rejection surfaces unchanged.
	if err := runtime.Register(airuntime.ProviderOllama, "qwen3:4b"); !errors.Is(err, airuntime.ErrDuplicateRegistration) {
		t.Fatalf("duplicate registration must surface ErrDuplicateRegistration, got %v", err)
	}
	// Invalid provider: closed enumeration holds through the passthrough.
	if err := runtime.Register("openai", "gpt-4"); err == nil {
		t.Fatal("a foreign provider must be rejected through the passthrough")
	}
	listing := runtime.RegistryView()
	if len(listing) != 4 {
		t.Fatalf("registry view must list all four serving providers, got %d", len(listing))
	}
	// Tier order.
	for i, p := range airuntime.AllProviders() {
		if listing[i].Provider != p {
			t.Fatalf("registry view must be tier-ordered at %d: want %s, got %s", i, p, listing[i].Provider)
		}
	}
	// Ollama serves both models, ascending lexical order.
	ollama := listing[3]
	if len(ollama.Models) != 2 || string(ollama.Models[0]) != "qwen3-14b" || string(ollama.Models[1]) != "qwen3:4b" {
		t.Fatalf("ollama must list its models ascending, got %v", ollama.Models)
	}
}

func TestRuntimeHealthView(t *testing.T) {
	fakes := fourFakes()
	runtime, _, table := newFakeStack(t, fakes)
	// Fresh view: all four HEALTHY, tier order, all serving.
	view := runtime.HealthView()
	if len(view) != 4 {
		t.Fatalf("the health view covers the whole closed stack, got %d", len(view))
	}
	for i, p := range airuntime.AllProviders() {
		if view[i].Provider != p {
			t.Fatalf("health view must be tier-ordered at %d: want %s, got %s", i, p, view[i].Provider)
		}
		if view[i].State != airuntime.HealthHealthy || !view[i].Serves {
			t.Fatalf("%s must show HEALTHY + serving, got %s/serves=%v", p, view[i].State, view[i].Serves)
		}
		if view[i].LastObserved == "" {
			t.Fatalf("%s must show its observation time", p)
		}
	}
	// A never-observed provider shows UNKNOWN with an empty time.
	if err := table.Observe(airuntime.Observation{Provider: airuntime.ProviderSGLang, State: airuntime.HealthUnavailable, At: time.Now().UTC().Add(time.Hour)}); err == nil {
		_ = runtime // silence unused in the unhappy path
	}
	view = runtime.HealthView()
	if view[0].State != airuntime.HealthUnavailable || view[0].Serves {
		t.Fatalf("the latest observation must lead the view, got %s", view[0].State)
	}
	// A fresh table: UNKNOWN everywhere, empty time, never serving.
	fresh, _ := airuntime.NewModelRegistry(4)
	prober, _ := NewProber(DefaultEndpoints(), DefaultProbeTimeout)
	adapters := make(map[airuntime.Provider]Adapter)
	for p, f := range fakes {
		adapters[p] = f
	}
	rt2, err := NewRuntime(fresh, airuntime.NewHealthTable(), prober, adapters)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	for _, entry := range rt2.HealthView() {
		if entry.State != airuntime.HealthUnknown || entry.Serves || entry.LastObserved != "" {
			t.Fatalf("never-observed must show UNKNOWN/empty/never-serving, got %+v", entry)
		}
	}
}
