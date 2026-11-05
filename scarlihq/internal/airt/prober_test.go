package airt

import (
	"context"
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
