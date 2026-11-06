package airt

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// endpointsFromServers maps live test servers onto a complete endpoint
// set (the closed four-tier stack over loopback — httptest binds
// 127.0.0.1).
func endpointsFromServers(t *testing.T, sglang, vllm, beellama, ollama string) map[airuntime.Provider]Endpoint {
	t.Helper()
	endpoints := map[airuntime.Provider]Endpoint{
		airuntime.ProviderSGLang:   Endpoint(sglang),
		airuntime.ProviderVLLM:     Endpoint(vllm),
		airuntime.ProviderBeeLlama: Endpoint(beellama),
		airuntime.ProviderOllama:   Endpoint(ollama),
	}
	if err := validateEndpointSet(endpoints); err != nil {
		t.Fatalf("test endpoints must validate: %v", err)
	}
	return endpoints
}

func TestProbeHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	endpoints := endpointsFromServers(t, server.URL, server.URL, server.URL, server.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	for _, p := range []airuntime.Provider{airuntime.ProviderSGLang, airuntime.ProviderVLLM, airuntime.ProviderBeeLlama} {
		obs, err := prober.Probe(context.Background(), p)
		if err != nil {
			t.Fatalf("probe %s: %v", p, err)
		}
		if obs.State != airuntime.HealthHealthy {
			t.Fatalf("a 2xx answer is HEALTHY — %s observed %s", p, obs.State)
		}
		if obs.At.IsZero() {
			t.Fatalf("the observation must be stamped (a probe without a time cannot be ordered)")
		}
	}
}

func TestProbeOllamaUsesTagsSurface(t *testing.T) {
	// Ollama exposes /api/tags (its healthcheck is `ollama list`); /health
	// does not exist there — the prober must ask the documented surface.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"models":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	endpoints := endpointsFromServers(t, server.URL, server.URL, server.URL, server.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	obs, err := prober.Probe(context.Background(), airuntime.ProviderOllama)
	if err != nil {
		t.Fatalf("probe ollama: %v", err)
	}
	if obs.State != airuntime.HealthHealthy {
		t.Fatalf("a 2xx /api/tags answer is HEALTHY — observed %s", obs.State)
	}
}

func TestProbeDegraded(t *testing.T) {
	// A standing-but-unready server: 503 while the model loads — DEGRADED
	// (it serves honestly; the adapter failure path handles the rest).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "loading", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	endpoints := endpointsFromServers(t, server.URL, server.URL, server.URL, server.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	obs, err := prober.Probe(context.Background(), airuntime.ProviderVLLM)
	if err != nil {
		t.Fatalf("probe vllm: %v", err)
	}
	if obs.State != airuntime.HealthDegraded {
		t.Fatalf("a non-2xx ANSWERING server is DEGRADED (standing but unready) — observed %s", obs.State)
	}
	if !obs.State.Serves() {
		t.Fatal("DEGRADED must serve (the CF-1 contract: honestly, cited in the plan)")
	}
}

func TestProbeUnavailableRefused(t *testing.T) {
	// A dead port: connection refused — UNAVAILABLE, never DEGRADED.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := "http://" + listener.Addr().String()
	listener.Close()
	endpoints := endpointsFromServers(t, dead, dead, dead, dead)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	obs, err := prober.Probe(context.Background(), airuntime.ProviderSGLang)
	if err != nil {
		t.Fatalf("probe sglang: %v", err)
	}
	if obs.State != airuntime.HealthUnavailable {
		t.Fatalf("a refused connection is UNAVAILABLE (a dead one, not a degraded one) — observed %s", obs.State)
	}
	if obs.State.Serves() {
		t.Fatal("UNAVAILABLE must not serve")
	}
}

func TestProbeUnavailableTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	endpoints := endpointsFromServers(t, server.URL, server.URL, server.URL, server.URL)
	prober, err := NewProber(endpoints, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	obs, err := prober.Probe(context.Background(), airuntime.ProviderBeeLlama)
	if err != nil {
		t.Fatalf("probe beellama: %v", err)
	}
	if obs.State != airuntime.HealthUnavailable {
		t.Fatalf("a timed-out probe is UNAVAILABLE — observed %s", obs.State)
	}
}

func TestProbeInvalidProvider(t *testing.T) {
	prober, err := NewProber(DefaultEndpoints(), DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	if _, err := prober.Probe(context.Background(), "openai"); err == nil {
		t.Fatal("a provider outside the closed enumeration must be rejected — no observation is fabricated for a foreign name")
	}
}

func TestProbeAllTierOrderAndCompleteness(t *testing.T) {
	// One healthy tier, one degraded, one dead, one ollama-healthy: the
	// round must return ALL FOUR observations in tier order.
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer healthy.Close()
	degraded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", http.StatusBadGateway) }))
	defer degraded.Close()
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + listener.Addr().String()
	listener.Close()
	ollamaOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer ollamaOK.Close()

	endpoints := endpointsFromServers(t, healthy.URL, degraded.URL, dead, ollamaOK.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	observations, err := prober.ProbeAll(context.Background())
	if err != nil {
		t.Fatalf("probe all: %v", err)
	}
	if len(observations) != 4 {
		t.Fatalf("every provider gets an observation — a dead tier is observed UNAVAILABLE, never omitted; got %d", len(observations))
	}
	wantOrder := []airuntime.Provider{airuntime.ProviderSGLang, airuntime.ProviderVLLM, airuntime.ProviderBeeLlama, airuntime.ProviderOllama}
	wantState := []airuntime.HealthState{airuntime.HealthHealthy, airuntime.HealthDegraded, airuntime.HealthUnavailable, airuntime.HealthHealthy}
	for i, p := range wantOrder {
		if observations[i].Provider != p {
			t.Fatalf("observations must return in tier order: position %d must be %s, got %s", i, p, observations[i].Provider)
		}
		if observations[i].State != wantState[i] {
			t.Fatalf("%s must observe %s, got %s", p, wantState[i], observations[i].State)
		}
	}
}

func TestProbeAllAndObserveRecords(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer healthy.Close()
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + listener.Addr().String()
	listener.Close()
	endpoints := endpointsFromServers(t, healthy.URL, dead, dead, dead)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	table := airuntime.NewHealthTable()
	// Before the round: UNKNOWN, never serves.
	if table.Serves(airuntime.ProviderSGLang) {
		t.Fatal("an unprobed provider must stay UNKNOWN and never serve")
	}
	if _, err := prober.ProbeAllAndObserve(context.Background(), table); err != nil {
		t.Fatalf("probe-and-observe: %v", err)
	}
	if !table.Serves(airuntime.ProviderSGLang) {
		t.Fatal("after a HEALTHY round the tier must serve (trust is earned by observation)")
	}
	if table.Serves(airuntime.ProviderOllama) {
		t.Fatal("after an UNAVAILABLE round the tier must not serve")
	}
	// A nil table is rejected by name.
	if _, err := prober.ProbeAllAndObserve(context.Background(), nil); err == nil {
		t.Fatal("a nil health table must be rejected")
	}
}

func TestProberStart(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer healthy.Close()
	endpoints := endpointsFromServers(t, healthy.URL, healthy.URL, healthy.URL, healthy.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	table := airuntime.NewHealthTable()
	stop, err := prober.Start(5*time.Millisecond, table)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer stop()
	// The loop must record within a generous deadline (eventual, but
	// bounded — a freshness loop that never observes is broken).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if table.Serves(airuntime.ProviderSGLang) {
			return // observed — the loop does its job
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the background loop must observe within the deadline (freshness is the prober's job — stated in CF-1, running here)")
}

func TestProberStartValidation(t *testing.T) {
	prober, err := NewProber(DefaultEndpoints(), DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	if _, err := prober.Start(0, airuntime.NewHealthTable()); err == nil {
		t.Fatal("a non-positive interval must be rejected")
	}
	if _, err := prober.Start(time.Second, nil); err == nil {
		t.Fatal("a nil table must be rejected")
	}
}

func TestProberConstruction(t *testing.T) {
	if _, err := NewProber(nil, DefaultProbeTimeout); err == nil {
		t.Fatal("a nil endpoint set must be rejected")
	}
	partial := DefaultEndpoints()
	delete(partial, airuntime.ProviderOllama)
	if _, err := NewProber(partial, DefaultProbeTimeout); err == nil {
		t.Fatal("an incomplete endpoint set must be rejected (all four tiers always)")
	}
	if _, err := NewProber(DefaultEndpoints(), 0); err == nil {
		t.Fatal("a non-positive timeout must be rejected")
	}
}

// ---------------------------------------------------------------------------
// KPK-04 (v19.6.4): a CANCELLED round is not evidence about the providers.
// Every test here is a REGRESSION for the audit finding — a cancelled
// probe round used to convert context.Canceled into UNAVAILABLE
// observations and overwrite all four providers' last-known-good health.
// ---------------------------------------------------------------------------

// TestProbeCancelledContextRefusesToFabricate: a single probe against an
// already-cancelled context must return an ERROR (KPK-04), never an
// UNAVAILABLE observation — a cancelled observation is not evidence about
// the provider. The error must keep errors.Is(err, context.Canceled)
// working for callers that reason about cancellation.
func TestProbeCancelledContextRefusesToFabricate(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer healthy.Close()
	endpoints := endpointsFromServers(t, healthy.URL, healthy.URL, healthy.URL, healthy.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	obs, err := prober.Probe(ctx, airuntime.ProviderSGLang)
	if err == nil {
		t.Fatal("a probe against a cancelled context must return an ERROR, not an observation (KPK-04 — a cancelled observation is not evidence about the provider)")
	}
	if obs.State != "" {
		t.Fatalf("no observation may be fabricated for a cancelled probe, got state %q (KPK-04 — the zero observation, nothing else)", obs.State)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancellation must stay identifiable through the wrap (errors.Is context.Canceled), got: %v", err)
	}
}

// TestProbeAllCancelledRoundPreservesLastKnownGood: the KPK-04 acceptance
// path end-to-end — seed all four providers HEALTHY through a real round,
// then start a round against DELAYED endpoints and cancel the caller's
// context before the probes complete: ProbeAllAndObserve must return
// context.Canceled and the health table must still contain the ORIGINAL
// observations (states AND timestamps — a cancelled caller overwrites
// nothing).
func TestProbeAllCancelledRoundPreservesLastKnownGood(t *testing.T) {
	// Seed: a fast healthy round first — all four tiers observed HEALTHY.
	seed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer seed.Close()
	endpoints := endpointsFromServers(t, seed.URL, seed.URL, seed.URL, seed.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	table := airuntime.NewHealthTable()
	if _, err := prober.ProbeAllAndObserve(context.Background(), table); err != nil {
		t.Fatalf("seed round: %v", err)
	}
	seededAt := map[airuntime.Provider]time.Time{}
	for _, p := range airuntime.AllProviders() {
		if !table.Serves(p) {
			t.Fatalf("seed: %s must serve after a HEALTHY round", p)
		}
		seededAt[p] = table.Snapshot(p).At
	}

	// The attack surface: delayed endpoints + a caller that walks away
	// mid-round (browser closed / proxy aborted).
	delayed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer delayed.Close()
	delayedEndpoints := endpointsFromServers(t, delayed.URL, delayed.URL, delayed.URL, delayed.URL)
	delayedProber, err := NewProber(delayedEndpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("delayed prober: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond) // mid-round, probes still in flight
		cancel()
	}()
	observations, err := delayedProber.ProbeAllAndObserve(ctx, table)
	if err == nil {
		t.Fatal("a cancelled round must return the context error — the round is aborted, not answered (KPK-04)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancellation must stay identifiable (errors.Is context.Canceled), got: %v", err)
	}
	if observations != nil {
		t.Fatalf("a cancelled round must return NO observations, got %d (KPK-04 — nothing is fabricated)", len(observations))
	}
	// The health table: last-known-good STANDS — states and timestamps.
	for _, p := range airuntime.AllProviders() {
		if !table.Serves(p) {
			t.Fatalf("KPK-04 regression: a cancelled round marked %s non-serving — the caller cancelled the observation, the providers were not proven unavailable", p)
		}
		if got := table.Snapshot(p).At; !got.Equal(seededAt[p]) {
			t.Fatalf("KPK-04 regression: %s observation timestamp moved (%v -> %v) — a cancelled round must not touch the table", p, seededAt[p], got)
		}
	}
}

// TestProbeAllDeadlineExpiredRoundAborts: a round whose parent context
// DEADLINE expires mid-round aborts the same way (context.DeadlineExceeded
// — distinct from Canceled but equally not evidence about the providers);
// nothing is recorded, last-known-good stands.
func TestProbeAllDeadlineExpiredRoundAborts(t *testing.T) {
	seed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer seed.Close()
	endpoints := endpointsFromServers(t, seed.URL, seed.URL, seed.URL, seed.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	table := airuntime.NewHealthTable()
	if _, err := prober.ProbeAllAndObserve(context.Background(), table); err != nil {
		t.Fatalf("seed round: %v", err)
	}
	for _, p := range airuntime.AllProviders() {
		if !table.Serves(p) {
			t.Fatalf("seed: %s must serve", p)
		}
	}

	delayed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer delayed.Close()
	delayedEndpoints := endpointsFromServers(t, delayed.URL, delayed.URL, delayed.URL, delayed.URL)
	delayedProber, err := NewProber(delayedEndpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("delayed prober: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := delayedProber.ProbeAllAndObserve(ctx, table); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an expired round deadline must abort with context.DeadlineExceeded, got: %v", err)
	}
	for _, p := range airuntime.AllProviders() {
		if !table.Serves(p) {
			t.Fatalf("KPK-04: an expired round deadline marked %s non-serving — it is not evidence about the provider", p)
		}
	}
}

// TestProbeAllGenuineFailureOnlyThatProvider: the KPK-04 counterweight —
// with the parent context LIVE, a genuinely dead endpoint IS observed
// UNAVAILABLE and recorded (the honest matrix is preserved: cancellation
// stops poisoning the table, real failures still land in it). Only the
// dead tier goes non-serving; the three live tiers stay HEALTHY.
func TestProbeAllGenuineFailureOnlyThatProvider(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer healthy.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := "http://" + listener.Addr().String()
	listener.Close()
	endpoints := endpointsFromServers(t, dead, healthy.URL, healthy.URL, healthy.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	table := airuntime.NewHealthTable()
	observations, err := prober.ProbeAllAndObserve(context.Background(), table)
	if err != nil {
		t.Fatalf("a live round with one dead tier must complete (fail-closed, not fail-blind): %v", err)
	}
	if len(observations) != 4 || observations[0].State != airuntime.HealthUnavailable {
		t.Fatalf("the dead tier must be observed UNAVAILABLE (the parent context is live — a genuine failure, recorded), got %d observations", len(observations))
	}
	if table.Serves(airuntime.ProviderSGLang) {
		t.Fatal("the dead tier must not serve after a genuine UNAVAILABLE observation")
	}
	for _, p := range []airuntime.Provider{airuntime.ProviderVLLM, airuntime.ProviderBeeLlama, airuntime.ProviderOllama} {
		if !table.Serves(p) {
			t.Fatalf("the live tiers must stay serving (only the dead tier is UNAVAILABLE) — %s", p)
		}
	}
}

// TestProberLoopSlowTierStaysGenuine: the background loop's round budget
// (probeRoundBudget = 2x the per-probe timeout, KPK-04) keeps the two
// clocks unambiguous — a tier slower than the probe timeout is classified
// by the probe's OWN timeout as UNAVAILABLE (genuine, recorded by the
// loop), not as a cancelled round that records nothing.
func TestProberLoopSlowTierStaysGenuine(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer fast.Close()
	// Probe timeout 50ms, round budget 100ms: the slow tier's client
	// timeout (50ms) fires well before the round budget — UNAVAILABLE.
	endpoints := endpointsFromServers(t, slow.URL, fast.URL, fast.URL, fast.URL)
	prober, err := NewProber(endpoints, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	table := airuntime.NewHealthTable()
	stop, err := prober.Start(20*time.Millisecond, table)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer stop()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !table.Serves(airuntime.ProviderSGLang) && table.Snapshot(airuntime.ProviderSGLang).State == airuntime.HealthUnavailable {
			if table.Serves(airuntime.ProviderVLLM) && table.Serves(airuntime.ProviderBeeLlama) && table.Serves(airuntime.ProviderOllama) {
				return // the loop classified the slow tier genuinely, fast tiers healthy
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the loop must record the slow tier UNAVAILABLE (genuine per-probe timeout, parent live) while the fast tiers stay HEALTHY — the round budget margin keeps the clocks unambiguous")
}

// ---------------------------------------------------------------------------
// KPK-19.6.4-P2-02 (v19.6.5): the commit-boundary race, made deterministic.
// The independent v19.6.4 audit found the window the v19.6.4 round-end
// guard structurally cannot close: all probes complete, ProbeAll's ctx
// check passes, the caller cancels, and the write loop still lands its
// observations (or a partial subset of them). The preCommit seam pins a
// cancellation into EXACTLY that window; the ObserveBatch commit boundary
// must refuse the whole round.
// ---------------------------------------------------------------------------

// TestProbeAllCancelBetweenRoundAndCommit: the audit's exact acceptance —
// cancel the caller's context IMMEDIATELY AFTER all probe goroutines
// complete but BEFORE the batch commit: ProbeAllAndObserve must return a
// context-identifiable error and the health table must preserve every
// previous state and timestamp (zero writes, no partial batch visible).
func TestProbeAllCancelBetweenRoundAndCommit(t *testing.T) {
	// Seed: a fast healthy round first — all four tiers observed HEALTHY.
	seed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer seed.Close()
	endpoints := endpointsFromServers(t, seed.URL, seed.URL, seed.URL, seed.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	table := airuntime.NewHealthTable()
	if _, err := prober.ProbeAllAndObserve(context.Background(), table); err != nil {
		t.Fatalf("seed round: %v", err)
	}
	seededAt := map[airuntime.Provider]time.Time{}
	for _, p := range airuntime.AllProviders() {
		if !table.Serves(p) {
			t.Fatalf("seed: %s must serve after a HEALTHY round", p)
		}
		seededAt[p] = table.Snapshot(p).At
	}

	// The attacked window, deterministically: fast endpoints (the round
	// completes instantly), a seam that cancels the caller's context
	// AFTER ProbeAll's round-end check has passed and BEFORE the commit.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prober.preCommit = func(observations []airuntime.Observation) {
		if len(observations) != 4 {
			t.Errorf("the seam must see the completed round's four observations, got %d", len(observations))
		}
		cancel() // the caller walks away exactly here — after the probes, before the commit
	}
	observations, err := prober.ProbeAllAndObserve(ctx, table)
	if err == nil {
		t.Fatal("a cancellation between round completion and the commit must abort the RECORDING (KPK-19.6.4-P2-02: the check-to-write race is closed by the commit boundary)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the abort must stay context-identifiable (errors.Is context.Canceled), got: %v", err)
	}
	if observations != nil {
		t.Fatalf("an aborted commit must return NO observations, got %d", len(observations))
	}
	// Zero writes, no partial batch: states AND timestamps unchanged.
	for _, p := range airuntime.AllProviders() {
		if !table.Serves(p) {
			t.Fatalf("KPK-19.6.4-P2-02 regression: the aborted commit marked %s non-serving — a cancelled commit is not evidence about the providers", p)
		}
		if got := table.Snapshot(p).At; !got.Equal(seededAt[p]) {
			t.Fatalf("KPK-19.6.4-P2-02 regression: %s timestamp moved (%v -> %v) — a cancelled commit records NOTHING", p, seededAt[p], got)
		}
	}
}

// TestProbeAllCommitAfterSeamStillCommits: the seam's counterweight — a
// seam that does NOT cancel leaves the round committing normally through
// the batch path (the seam itself never swallows a round; only a done
// context at the boundary does).
func TestProbeAllCommitAfterSeamStillCommits(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer healthy.Close()
	endpoints := endpointsFromServers(t, healthy.URL, healthy.URL, healthy.URL, healthy.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	prober.preCommit = func(observations []airuntime.Observation) {
		if len(observations) != 4 {
			t.Errorf("the seam must see the completed round, got %d", len(observations))
		}
	}
	table := airuntime.NewHealthTable()
	if _, err := prober.ProbeAllAndObserve(context.Background(), table); err != nil {
		t.Fatalf("a live round through the seam must commit normally: %v", err)
	}
	for _, p := range airuntime.AllProviders() {
		if !table.Serves(p) {
			t.Fatalf("%s must serve after a live round through the seam", p)
		}
	}
}
