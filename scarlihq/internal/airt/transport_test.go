package airt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// ---------------------------------------------------------------------------
// KPK-01 (v19.6.3): the local-only transport boundary — no redirects, no
// ambient proxy, no foreign dials. Every test here is a REGRESSION for a
// reproduced audit finding (a default http.Client followed 307 and
// forwarded the POST body to an external destination).
// ---------------------------------------------------------------------------

// TestLocalAdapterChatRefusesRedirects: a local tier answering 307/308
// with an external Location must NOT be followed — the destination
// server receives ZERO requests and ZERO prompt bytes (KPK-01
// acceptance 1+2: the conversation never leaves the configured
// endpoint, whatever a compromised tier answers).
func TestLocalAdapterChatRefusesRedirects(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var destHits atomic.Int64
			var destBytes atomic.Int64
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				destHits.Add(1)
				raw, _ := io.ReadAll(r.Body)
				destBytes.Add(int64(len(raw)))
				io.WriteString(w, `{}`)
			}))
			defer destination.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, destination.URL, status)
			}))
			defer origin.Close()

			adapter, err := NewLocalAdapter(airuntime.ProviderSGLang, Endpoint(origin.URL), DefaultAdapterTimeout)
			if err != nil {
				t.Fatalf("adapter: %v", err)
			}
			_, err = adapter.Chat(context.Background(), chatRequest())
			if err == nil {
				t.Fatalf("a %d answer must be an adapter error (a 3xx is never followed — the local-only client has no redirect policy to violate)", status)
			}
			if destHits.Load() != 0 || destBytes.Load() != 0 {
				t.Fatalf("the redirect destination received %d requests / %d body bytes — ZERO is the only acceptable number (KPK-01: the POST body is the private conversation)", destHits.Load(), destBytes.Load())
			}
		})
	}
}

// TestLocalAdapterChatRefusesCrossOriginRedirect: even a redirect whose
// Location carries the prompt in the QUERY cannot smuggle content out —
// but here the point is the destination: an origin redirecting to ITSELF
// on a different path is still a non-2xx answer, never a followed hop.
func TestLocalAdapterChatRefusesCrossOriginRedirect(t *testing.T) {
	var followedHits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			http.Redirect(w, r, "/leak", http.StatusFound)
			return
		}
		followedHits.Add(1)
		io.WriteString(w, `{}`)
	}))
	defer origin.Close()
	adapter, err := NewLocalAdapter(airuntime.ProviderVLLM, Endpoint(origin.URL), DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if _, err := adapter.Chat(context.Background(), chatRequest()); err == nil {
		t.Fatal("a 302 answer must be an adapter error — no redirect status is special, none is followed")
	}
	if followedHits.Load() != 0 {
		t.Fatalf("the redirect target path was reached %d times — redirects are never followed", followedHits.Load())
	}
}

// TestProbeDoesNotFollowRedirects: the prober must not follow a redirect
// either (KPK-01 acceptance 3) — probes carry no transcript, but the
// process must not be steered outside the declared endpoint set by
// anything. A 3xx answer is DEGRADED (standing but unready), the honest
// classification of a tier answering anything but 2xx.
func TestProbeDoesNotFollowRedirects(t *testing.T) {
	var destHits atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/health", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	endpoints := endpointsFromServers(t, origin.URL, origin.URL, origin.URL, origin.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	obs, err := prober.Probe(context.Background(), airuntime.ProviderSGLang)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if obs.State != airuntime.HealthDegraded {
		t.Fatalf("a redirect answer must classify DEGRADED (standing but unready — never followed), got %s", obs.State)
	}
	if destHits.Load() != 0 {
		t.Fatalf("the probe redirect destination received %d requests — the prober must not be steered off the endpoint set", destHits.Load())
	}
}

// TestLocalTransportIgnoresAmbientProxy: HTTP_PROXY / HTTPS_PROXY point
// at a counting proxy — local adapter and prober traffic must NOT
// traverse it (KPK-01 acceptance 4: Proxy is explicitly nil on the
// local-only transport; ambient proxy inheritance is not a feature and
// cannot become one by accident).
func TestLocalTransportIgnoresAmbientProxy(t *testing.T) {
	var proxyHits atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "proxy refused", http.StatusForbidden)
	}))
	defer proxy.Close()
	// Ambient proxy environment — restored exactly (tests are not
	// parallel in this package; the race detector observes the client,
	// not the env).
	oldHTTP, hadHTTP := os.LookupEnv("HTTP_PROXY")
	oldHTTPS, hadHTTPS := os.LookupEnv("HTTPS_PROXY")
	oldLower, hadLower := os.LookupEnv("http_proxy")
	os.Setenv("HTTP_PROXY", proxy.URL)
	os.Setenv("HTTPS_PROXY", proxy.URL)
	os.Unsetenv("http_proxy")
	defer func() {
		if hadHTTP {
			os.Setenv("HTTP_PROXY", oldHTTP)
		} else {
			os.Unsetenv("HTTP_PROXY")
		}
		if hadHTTPS {
			os.Setenv("HTTPS_PROXY", oldHTTPS)
		} else {
			os.Unsetenv("HTTPS_PROXY")
		}
		if hadLower {
			os.Setenv("http_proxy", oldLower)
		} else {
			os.Unsetenv("http_proxy")
		}
	}()

	// The adapter: a healthy local tier answers directly — the proxy
	// must see nothing.
	tier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"direct"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer tier.Close()
	adapter, err := NewLocalAdapter(airuntime.ProviderOllama, Endpoint(tier.URL), DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if _, err := adapter.Chat(context.Background(), chatRequest()); err != nil {
		t.Fatalf("direct local traffic must succeed with ambient proxy env set (Proxy=nil never consults it): %v", err)
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("the ambient proxy received %d requests — local AI traffic must never traverse it (KPK-01)", proxyHits.Load())
	}

	// The prober: same contract on the probe path.
	endpoints := endpointsFromServers(t, tier.URL, tier.URL, tier.URL, tier.URL)
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("prober: %v", err)
	}
	if _, err := prober.Probe(context.Background(), airuntime.ProviderBeeLlama); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("the ambient proxy received probe traffic — must be zero (KPK-01)")
	}
}

// TestLocalTransportDialGuard: the dial layer refuses any destination
// outside loopback and the configured endpoint hosts — defense in depth
// (KPK-01: hostname syntax at the endpoint layer does not prove where a
// connection actually goes; the dial layer does). The guard fires BEFORE
// any network activity, so a foreign address is refused deterministically.
func TestLocalTransportDialGuard(t *testing.T) {
	transport := newLocalTransport(map[string]struct{}{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, addr := range []string{"93.184.216.34:443", "evil.example:80", "10.0.0.5:30000", "172.17.0.9:8000"} {
		conn, err := transport.DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			t.Fatalf("dial %s must be refused — the local-only transport dials loopback and the configured endpoint hosts, nothing else", addr)
		}
	}
	// Loopback literals are always local — the guard passes them through
	// to the real dialer (a refused loopback port is a transport error,
	// not a guard rejection: the two must stay distinguishable by text).
	conn, err := transport.DialContext(ctx, "tcp", "127.0.0.1:1")
	if err == nil {
		conn.Close()
		t.Fatalf("port 1 on loopback must be refused by the OS, not accepted")
	}
	if conn != nil {
		t.Fatalf("a refused dial must return a nil conn")
	}
}

// TestIsLocalDialTarget: the guard's own truth table — loopback literals
// always; configured single-label service names when configured; dotted
// public hosts and unconfigured names never.
func TestIsLocalDialTarget(t *testing.T) {
	allowed := map[string]struct{}{"sglang": {}, "ollama-agent": {}}
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"localhost", true},
		{"::1", true},
		{"[::1]", true},
		{"sglang", true},
		{"SGLANG", true}, // case-folded, like the endpoint validator
		{"ollama-agent", true},
		{"vllm", false},           // a service name, but NOT configured here
		{"api.openai.com", false}, // the cloud-gateway shape, never
		{"172.17.0.9", false},     // a container IP is not a loopback literal
		{"", false},
	}
	for _, c := range cases {
		if got := isLocalDialTarget(c.host, allowed); got != c.want {
			t.Fatalf("isLocalDialTarget(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// TestEndpointHostSets: the allowed-dial sets extract exactly the
// configured hosts (one for a single-tier adapter, all four for the
// prober).
func TestEndpointHostSets(t *testing.T) {
	single := endpointHostSet(Endpoint("http://sglang:30000"))
	if _, ok := single["sglang"]; !ok || len(single) != 1 {
		t.Fatalf("endpointHostSet must extract exactly the endpoint's host, got %v", single)
	}
	all := endpointHostsOf(DefaultEndpoints())
	if len(all) != 1 { // the defaults are all 127.0.0.1 — one distinct host
		t.Fatalf("the default endpoint set dials exactly one distinct host, got %v", all)
	}
	if _, ok := all["127.0.0.1"]; !ok {
		t.Fatalf("the defaults must allow 127.0.0.1, got %v", all)
	}
}

// TestNewLocalClientShape: the ONE client constructor this package ever
// builds — no-redirect policy, per-attempt timeout, a transport that is
// not the ambient default (Proxy=nil, no shared global state).
func TestNewLocalClientShape(t *testing.T) {
	client := newLocalClient(15*time.Second, nil)
	if client.Timeout != 15*time.Second {
		t.Fatalf("the per-attempt timeout must ride the client, got %s", client.Timeout)
	}
	if client.CheckRedirect == nil {
		t.Fatal("the no-redirect policy must be explicit — a nil CheckRedirect is the default client's follow-everything behavior")
	}
	// The policy itself: ErrUseLastResponse, verbatim.
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/x", nil)
	if err := client.CheckRedirect(req, nil); err != http.ErrUseLastResponse {
		t.Fatalf("the redirect policy must return http.ErrUseLastResponse (hand the 3xx back as the response), got %v", err)
	}
	if client.Transport == nil || client.Transport == http.DefaultTransport {
		t.Fatal("the transport must be the local-only dial-guarded transport, never the ambient default")
	}
}

// TestLocalAdapterTimeoutUpperBound: the per-attempt timeout is bounded
// by MaxAdapterTimeout — a named number, never an unbounded one (the
// SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT surface validates the same bound in
// main.go, fail-closed).
func TestLocalAdapterTimeoutUpperBound(t *testing.T) {
	if _, err := NewLocalAdapter(airuntime.ProviderSGLang, "http://127.0.0.1:30000", MaxAdapterTimeout+time.Second); err == nil {
		t.Fatalf("a per-attempt timeout above %s must be rejected at construction", MaxAdapterTimeout)
	}
	if _, err := NewLocalAdapter(airuntime.ProviderSGLang, "http://127.0.0.1:30000", MaxAdapterTimeout); err != nil {
		t.Fatalf("exactly %s must be accepted (the bound, inclusive)", MaxAdapterTimeout)
	}
}

// TestLocalStackShareNoClientState: two stacks over different endpoints
// carry independent transports — no shared connection pool, no shared
// allowed-dial sets (a foreign dial through one stack's pool cannot ride
// another's).
func TestLocalStackShareNoClientState(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }))
	defer second.Close()
	epsA := map[airuntime.Provider]Endpoint{
		airuntime.ProviderSGLang:   Endpoint(first.URL),
		airuntime.ProviderVLLM:     Endpoint(first.URL),
		airuntime.ProviderBeeLlama: Endpoint(first.URL),
		airuntime.ProviderOllama:   Endpoint(first.URL),
	}
	epsB := map[airuntime.Provider]Endpoint{
		airuntime.ProviderSGLang:   Endpoint(second.URL),
		airuntime.ProviderVLLM:     Endpoint(second.URL),
		airuntime.ProviderBeeLlama: Endpoint(second.URL),
		airuntime.ProviderOllama:   Endpoint(second.URL),
	}
	adaptersA, proberA, err := NewLocalStack(epsA, DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("stack A: %v", err)
	}
	adaptersB, proberB, err := NewLocalStack(epsB, DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("stack B: %v", err)
	}
	a := adaptersA[airuntime.ProviderSGLang].(*LocalAdapter)
	b := adaptersB[airuntime.ProviderSGLang].(*LocalAdapter)
	if a.client == b.client || a.client.Transport == b.client.Transport {
		t.Fatal("stacks must not share clients or transports — each carries its own allowed-dial set")
	}
	if a.client.Transport == proberA.client.Transport {
		t.Fatal("the adapter and the prober may share the constructor, not the transport instance")
	}
	_ = proberB
}
