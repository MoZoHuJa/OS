package airt

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
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
	transport := newLocalTransport(map[string]struct{}{}, DefaultTrustedDialCIDRs())
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
	client := newLocalClient(15*time.Second, nil, nil)
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

// ---------------------------------------------------------------------------
// KPK-03 (v19.6.4): the dial guard resolves, validates and PINS. Every
// test here is a REGRESSION for the audit finding — the guard used to
// validate the hostname STRING and then let net.Dialer resolve it to
// ANYWHERE (a poisoned resolver / Docker DNS / hosts file could send the
// POST body — the private prompts — to a public IP with every hostname
// check passing). The fake resolver + recording dialer make the policy
// deterministic: no real DNS, no real network.
// ---------------------------------------------------------------------------

// recordingDial captures every address the transport hands to the dialer
// and answers with a live net.Pipe conn (a real conn, never nil — the
// transport must not fabricate success or failure beyond the policy).
type recordingDial struct {
	dialed []string
	fail   bool
}

func (r *recordingDial) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	r.dialed = append(r.dialed, addr)
	if r.fail {
		return nil, fmt.Errorf("recording dialer: refusing on purpose")
	}
	server, client := net.Pipe()
	go func() { io.Copy(io.Discard, server); server.Close() }() // drain so Close is clean
	return client, nil
}

// staticResolver answers a fixed list of addresses per host.
type staticResolver map[string][]net.IP

func (s staticResolver) resolve(ctx context.Context, host string) ([]net.IP, error) {
	if ips, ok := s[host]; ok {
		return ips, nil
	}
	return nil, fmt.Errorf("static resolver: no answer for %s", host)
}

func mustCIDR(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("test CIDR %q: %v", cidr, err)
	}
	return ipnet
}

func mustIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("test IP %q does not parse", s)
	}
	return ip
}

// TestDialGuardResolvesValidatesAndPins: an allowed service name whose
// resolution lands in a trusted network — the connection is PINNED to the
// validated address (the dialer receives the IP:port, never the name),
// and the address would have been refused had it arrived as a literal.
func TestDialGuardResolvesValidatesAndPins(t *testing.T) {
	allowed := map[string]struct{}{"sglang": {}}
	trusted := []*net.IPNet{mustCIDR(t, "172.18.0.0/16")}
	resolver := staticResolver{"sglang": {mustIP(t, "172.18.0.9")}}
	rec := &recordingDial{}
	transport := newLocalTransportDialing(allowed, trusted, resolver.resolve, rec.dial)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := transport.DialContext(ctx, "tcp", "sglang:30000")
	if err != nil {
		t.Fatalf("an approved resolution must dial, pinned: %v", err)
	}
	defer conn.Close()
	if len(rec.dialed) != 1 || rec.dialed[0] != "172.18.0.9:30000" {
		t.Fatalf("the dialer must receive the PINNED validated address 172.18.0.9:30000, got %v (KPK-03: the IP that is dialed is the IP that was validated)", rec.dialed)
	}
}

// TestDialGuardPinsAcrossApprovedAnswers: a resolution answering SEVERAL
// approved addresses dials them in resolver order — every dialed address
// is a member of the validated set (the multi-address resilience of the
// plain dialer, without ever dialing an unvalidated destination).
func TestDialGuardPinsAcrossApprovedAnswers(t *testing.T) {
	allowed := map[string]struct{}{"ollama-agent": {}}
	trusted := []*net.IPNet{mustCIDR(t, "172.18.0.0/16")}
	resolver := staticResolver{"ollama-agent": {mustIP(t, "172.18.0.9"), mustIP(t, "172.18.0.10")}}
	rec := &recordingDial{}
	transport := newLocalTransportDialing(allowed, trusted, resolver.resolve, rec.dial)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := transport.DialContext(ctx, "tcp", "ollama-agent:11434")
	if err != nil {
		t.Fatalf("the first approved address must connect: %v", err)
	}
	defer conn.Close()
	if len(rec.dialed) != 1 || rec.dialed[0] != "172.18.0.9:11434" {
		t.Fatalf("resolver order — the first approved address dials first, got %v", rec.dialed)
	}
	// A dead first address falls through to the second APPROVED one —
	// never to an unvalidated destination.
	rec2 := &recordingDial{}
	deadFirst := func(ctx context.Context, network, addr string) (net.Conn, error) {
		rec2.dialed = append(rec2.dialed, addr)
		if addr == "172.18.0.9:11434" {
			return nil, fmt.Errorf("connection refused (first approved address dead)")
		}
		server, client := net.Pipe()
		go func() { io.Copy(io.Discard, server); server.Close() }()
		return client, nil
	}
	transport2 := newLocalTransportDialing(allowed, trusted, resolver.resolve, deadFirst)
	conn2, err := transport2.DialContext(ctx, "tcp", "ollama-agent:11434")
	if err != nil {
		t.Fatalf("the second approved address must carry the connection: %v", err)
	}
	defer conn2.Close()
	if len(rec2.dialed) != 2 || rec2.dialed[0] != "172.18.0.9:11434" || rec2.dialed[1] != "172.18.0.10:11434" {
		t.Fatalf("approved addresses dial in resolver order until one connects, got %v", rec2.dialed)
	}
}

// TestDialGuardRefusesPublicResolution: an allowed service name whose
// resolution answers a PUBLIC address — the dialer is NEVER called (zero
// packets, zero prompt bytes), the refusal names KPK-03. This is the exact
// audit finding closed: the poisoned-resolver exfiltration path.
func TestDialGuardRefusesPublicResolution(t *testing.T) {
	allowed := map[string]struct{}{"sglang": {}}
	trusted := []*net.IPNet{mustCIDR(t, "172.18.0.0/16")}
	resolver := staticResolver{"sglang": {mustIP(t, "93.184.216.34")}}
	rec := &recordingDial{}
	transport := newLocalTransportDialing(allowed, trusted, resolver.resolve, rec.dial)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := transport.DialContext(ctx, "tcp", "sglang:30000")
	if err == nil {
		conn.Close()
		t.Fatal("a service name resolving to a PUBLIC address must be refused (KPK-03: hostname checks passing is not destination trust)")
	}
	if len(rec.dialed) != 0 {
		t.Fatalf("the dialer must see ZERO dials for a public resolution, got %v (KPK-03: zero packets, zero prompt bytes)", rec.dialed)
	}
	if !strings.Contains(err.Error(), "KPK-03") {
		t.Fatalf("the refusal must name KPK-03 (the destination-level boundary), got: %v", err)
	}
}

// TestDialGuardFailClosedOnMixedResolution: a resolution answering one
// approved AND one public address is refused WHOLE — the documented
// fail-closed policy for mixed answers (no filtering, no guessing which
// address the connection "really" wanted).
func TestDialGuardFailClosedOnMixedResolution(t *testing.T) {
	allowed := map[string]struct{}{"sglang": {}}
	trusted := []*net.IPNet{mustCIDR(t, "172.18.0.0/16")}
	resolver := staticResolver{"sglang": {mustIP(t, "172.18.0.9"), mustIP(t, "93.184.216.34")}}
	rec := &recordingDial{}
	transport := newLocalTransportDialing(allowed, trusted, resolver.resolve, rec.dial)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := transport.DialContext(ctx, "tcp", "sglang:30000"); err == nil {
		t.Fatal("a MIXED answer set (approved + public) must be refused whole (KPK-03 fail-closed)")
	}
	if len(rec.dialed) != 0 {
		t.Fatalf("no address of a mixed set may be dialed, got %v", rec.dialed)
	}
}

// TestDialGuardRefusesEmptyAndFailedResolution: an empty answer set and a
// resolver failure are both refused fail-closed — no dial, no guess.
func TestDialGuardRefusesEmptyAndFailedResolution(t *testing.T) {
	allowed := map[string]struct{}{"sglang": {}}
	trusted := []*net.IPNet{mustCIDR(t, "172.18.0.0/16")}
	for name, resolver := range map[string]ipResolverFunc{
		"empty answer": staticResolver{"sglang": {}}.resolve,
		"resolver failure": func(ctx context.Context, host string) ([]net.IP, error) {
			return nil, fmt.Errorf("DNS is down")
		},
	} {
		rec := &recordingDial{}
		transport := newLocalTransportDialing(allowed, trusted, resolver, rec.dial)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if _, err := transport.DialContext(ctx, "tcp", "sglang:30000"); err == nil {
			cancel()
			t.Fatalf("%s: must be refused fail-closed", name)
		}
		if len(rec.dialed) != 0 {
			cancel()
			t.Fatalf("%s: the dialer must see zero dials, got %v", name, rec.dialed)
		}
		cancel()
	}
}

// TestDialGuardTOCTOUResolutionChange: the resolver answering DIFFERENTLY
// on successive dials cannot bypass the pin — each dial validates the
// answers it sees, and the dialed address is always a member of THAT
// validated set. First dial approves and pins 172.18.0.9; the resolver
// then turns hostile (public answer) — the second dial is refused whole,
// and the first connection was never steered anywhere but its pinned,
// validated address.
func TestDialGuardTOCTOUResolutionChange(t *testing.T) {
	allowed := map[string]struct{}{"sglang": {}}
	trusted := []*net.IPNet{mustCIDR(t, "172.18.0.0/16")}
	hostile := &switchingResolver{
		answers: [][]net.IP{
			{mustIP(t, "172.18.0.9")},
			{mustIP(t, "93.184.216.34")},
		},
	}
	rec := &recordingDial{}
	transport := newLocalTransportDialing(allowed, trusted, hostile.resolve, rec.dial)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := transport.DialContext(ctx, "tcp", "sglang:30000")
	if err != nil {
		t.Fatalf("the first (approved) answer must dial pinned: %v", err)
	}
	if rec.dialed[len(rec.dialed)-1] != "172.18.0.9:30000" {
		t.Fatalf("the first dial must be pinned to the validated 172.18.0.9:30000, got %v", rec.dialed)
	}
	// The resolver turns hostile AFTER validation+dial — the existing
	// connection stays where it is; the NEXT dial refuses the public answer.
	conn.Close()
	if _, err := transport.DialContext(ctx, "tcp", "sglang:30000"); err == nil {
		t.Fatal("the hostile (public) answer on the second resolution must be refused (KPK-03: each dial validates its own answers)")
	}
	if len(rec.dialed) != 1 {
		t.Fatalf("only the first validated dial may reach the dialer, got %v", rec.dialed)
	}
}

// switchingResolver serves a scripted sequence of answers (hostile DNS
// simulation — the answer CHANGES between validation and the next dial).
type switchingResolver struct {
	answers [][]net.IP
	calls   int
}

func (s *switchingResolver) resolve(ctx context.Context, host string) ([]net.IP, error) {
	if s.calls < len(s.answers) {
		answer := s.answers[s.calls]
		s.calls++
		return answer, nil
	}
	return s.answers[len(s.answers)-1], nil
}

// TestDialGuardLoopbackLiteralSkipsResolution: an IP-literal destination
// never consults the resolver (no resolution, no pinning needed — the
// destination IS the address) and dials as-is; loopback literals are
// always trusted.
func TestDialGuardLoopbackLiteralSkipsResolution(t *testing.T) {
	allowed := map[string]struct{}{}
	called := false
	resolver := func(ctx context.Context, host string) ([]net.IP, error) {
		called = true
		return nil, fmt.Errorf("must not be called for literals")
	}
	rec := &recordingDial{}
	transport := newLocalTransportDialing(allowed, nil, resolver, rec.dial)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := transport.DialContext(ctx, "tcp", "127.0.0.1:30000")
	if err != nil {
		t.Fatalf("a loopback literal dials directly (always trusted): %v", err)
	}
	defer conn.Close()
	if called {
		t.Fatal("the resolver must not be consulted for an IP literal — the destination IS the address")
	}
	if len(rec.dialed) != 1 || rec.dialed[0] != "127.0.0.1:30000" {
		t.Fatalf("the literal dials as-is, got %v", rec.dialed)
	}
}

// TestDialGuardForeignLiteralRefusedByTrustedSet: a non-loopback IP
// literal inside a trusted network dials; one OUTSIDE every trusted
// network is refused at the dial layer (destination validation, not just
// name validation).
func TestDialGuardForeignLiteralRefusedByTrustedSet(t *testing.T) {
	allowed := map[string]struct{}{"172.18.0.9": {}} // a future bug smuggling a literal into the allowed set
	trusted := []*net.IPNet{mustCIDR(t, "172.18.0.0/16")}
	rec := &recordingDial{}
	transport := newLocalTransportDialing(allowed, trusted, staticResolver{}.resolve, rec.dial)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := transport.DialContext(ctx, "tcp", "172.18.0.9:30000")
	if err != nil {
		t.Fatalf("a literal inside a trusted network dials (destination validated): %v", err)
	}
	defer conn.Close()
	if _, err := transport.DialContext(ctx, "tcp", "93.184.216.34:443"); err == nil {
		t.Fatal("a public literal must be refused at the dial layer even if the hostname allowlist passes (KPK-03 defense in depth)")
	}
	if len(rec.dialed) != 1 {
		t.Fatalf("only the trusted literal may dial, got %v", rec.dialed)
	}
}

// TestIPIsTrusted: the address-level boundary's own truth table — loopback
// ALWAYS (the bare-metal default endpoints), declared networks, nothing
// else.
func TestIPIsTrusted(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "172.18.0.0/16"), mustCIDR(t, "10.0.0.0/8")}
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.8", true}, // any 127/8 — loopback is a range, definitionally local
		{"::1", true},
		{"172.18.0.9", true},
		{"172.18.255.255", true},
		{"172.17.0.9", false}, // inside the DOCKER DEFAULT pool but not THIS declared set
		{"10.42.0.1", true},
		{"192.168.1.1", false},
		{"93.184.216.34", false},
		{"8.8.8.8", false},
	}
	for _, c := range cases {
		if got := ipIsTrusted(mustIP(t, c.ip), trusted); got != c.want {
			t.Fatalf("ipIsTrusted(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// TestApprovedResolvedIPs: the fail-closed WHOLE-SET policy — every
// address trusted or the answer is nil (mixed refused, empty refused).
func TestApprovedResolvedIPs(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "172.18.0.0/16")}
	if got := approvedResolvedIPs(nil, trusted); got != nil {
		t.Fatalf("an empty answer set approves nothing, got %v", got)
	}
	if got := approvedResolvedIPs([]net.IP{mustIP(t, "172.18.0.9")}, trusted); len(got) != 1 {
		t.Fatalf("a wholly approved set returns itself, got %v", got)
	}
	if got := approvedResolvedIPs([]net.IP{mustIP(t, "172.18.0.9"), mustIP(t, "8.8.8.8")}, trusted); got != nil {
		t.Fatalf("a MIXED set must be refused whole (fail-closed), got %v", got)
	}
	if got := approvedResolvedIPs([]net.IP{mustIP(t, "127.0.0.1")}, nil); len(got) != 1 {
		t.Fatalf("loopback answers are always approved (loopback-only trust), got %v", got)
	}
	if got := approvedResolvedIPs([]net.IP{mustIP(t, "172.18.0.9")}, nil); got != nil {
		t.Fatalf("a non-loopback answer with loopback-only trust must be refused, got %v", got)
	}
}

// TestDefaultTrustedDialCIDRs: the defaults are the Docker pools — and
// deliberately NOT the whole RFC1918 space (10.0.0.0/8 absent until an
// operator declares it).
func TestDefaultTrustedDialCIDRs(t *testing.T) {
	cidrs := DefaultTrustedDialCIDRs()
	if len(cidrs) != 2 {
		t.Fatalf("the defaults are exactly the two Docker pools, got %v", cidrs)
	}
	for _, c := range cidrs {
		if c.IP.IsLoopback() {
			t.Fatalf("loopback is always trusted implicitly and must not be listed, got %v", c)
		}
	}
	if !ipIsTrusted(mustIP(t, "172.17.0.9"), cidrs) || !ipIsTrusted(mustIP(t, "172.31.255.1"), cidrs) {
		t.Fatal("the 172.17.0.0/12 pool must cover the default bridge range")
	}
	if !ipIsTrusted(mustIP(t, "192.168.5.1"), cidrs) {
		t.Fatal("the 192.168.0.0/16 pool must be trusted by default")
	}
	if ipIsTrusted(mustIP(t, "10.0.0.1"), cidrs) {
		t.Fatal("10.0.0.0/8 must NOT be trusted by default — an operator declares it explicitly (KPK-03: no blind RFC1918 trust)")
	}
	if ipIsTrusted(mustIP(t, "93.184.216.34"), cidrs) {
		t.Fatal("public addresses are never trusted")
	}
}

// TestTrustedDialCIDRsFromEnv: the operator surface parses fail-closed —
// unset/blank → defaults (nil), valid CIDRs → the declared set, garbage →
// an error naming the variable and the entry.
func TestTrustedDialCIDRsFromEnv(t *testing.T) {
	if cidrs, err := TrustedDialCIDRsFromEnv(func(string) string { return "" }); err != nil || cidrs != nil {
		t.Fatalf("unset/blank must mean the defaults (nil, nil), got (%v, %v)", cidrs, err)
	}
	if cidrs, err := TrustedDialCIDRsFromEnv(func(string) string { return "   " }); err != nil || cidrs != nil {
		t.Fatalf("whitespace-only must mean the defaults, got (%v, %v)", cidrs, err)
	}
	cidrs, err := TrustedDialCIDRsFromEnv(func(k string) string {
		if k == TrustedCIDRsEnvVar {
			return "172.18.0.0/16, 10.0.0.0/8"
		}
		return ""
	})
	if err != nil || len(cidrs) != 2 {
		t.Fatalf("a valid declaration parses (comma-separated, whitespace-tolerant), got (%v, %v)", cidrs, err)
	}
	if !ipIsTrusted(mustIP(t, "10.42.0.1"), cidrs) || !ipIsTrusted(mustIP(t, "172.18.3.3"), cidrs) {
		t.Fatalf("the declared networks must be trusted, got %v", cidrs)
	}
	if ipIsTrusted(mustIP(t, "172.17.0.9"), cidrs) {
		t.Fatal("a declared set REPLACES the defaults — the 172.17 pool is out once 172.18 is declared")
	}
	if _, err := TrustedDialCIDRsFromEnv(func(k string) string {
		if k == TrustedCIDRsEnvVar {
			return "172.18.0.0/16,not-a-cidr"
		}
		return ""
	}); err == nil {
		t.Fatal("a malformed entry must be an ERROR (fail-closed env discipline)")
	}
	if _, err := TrustedDialCIDRsFromEnv(func(k string) string {
		if k == TrustedCIDRsEnvVar {
			return "8.8.8.8"
		}
		return ""
	}); err == nil {
		t.Fatal("a bare IP is not a CIDR — refused (declare networks, not addresses)")
	}
}

// TestResolveDialCIDRsOptionPrecedence: no option → the Docker defaults;
// a declared option REPLACES them (empty declaration = loopback only);
// the LAST declaration wins.
func TestResolveDialCIDRsOptionPrecedence(t *testing.T) {
	if got := resolveDialCIDRs(nil); len(got) != 2 {
		t.Fatalf("no option → the Docker default pools, got %v", got)
	}
	declared := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}
	got := resolveDialCIDRs([]DialOption{WithTrustedDialCIDRs(declared)})
	if len(got) != 1 || !got[0].Contains(mustIP(t, "10.1.2.3")) {
		t.Fatalf("a declared option replaces the defaults, got %v", got)
	}
	if got := resolveDialCIDRs([]DialOption{WithTrustedDialCIDRs(nil)}); len(got) != 0 {
		t.Fatalf("an EMPTY declaration trusts loopback only, got %v", got)
	}
	last := []*net.IPNet{mustCIDR(t, "192.168.0.0/16")}
	got = resolveDialCIDRs([]DialOption{WithTrustedDialCIDRs(declared), WithTrustedDialCIDRs(last)})
	if len(got) != 1 || !got[0].Contains(mustIP(t, "192.168.1.1")) {
		t.Fatalf("the LAST declaration wins, got %v", got)
	}
}

// TestNameEndpointDialTrustConstruction: a service-name endpoint with a
// trusted network set constructs; with LOOPBACK-ONLY trust it fails AT
// CONSTRUCTION (fail-closed, loud — never a per-request surprise);
// localhost endpoints construct under loopback-only trust (localhost
// resolves into the always-trusted loopback).
func TestNameEndpointDialTrustConstruction(t *testing.T) {
	nameEndpoints := map[airuntime.Provider]Endpoint{
		airuntime.ProviderSGLang:   "http://sglang:30000",
		airuntime.ProviderVLLM:     "http://vllm:8000",
		airuntime.ProviderBeeLlama: "http://beellama:8080",
		airuntime.ProviderOllama:   "http://ollama-agent:11434",
	}
	// No option → the Docker default pools carry the names.
	if _, _, err := NewLocalStack(nameEndpoints, DefaultAdapterTimeout); err != nil {
		t.Fatalf("service-name endpoints construct over the default pools: %v", err)
	}
	// A declared network carries the names.
	declared := []*net.IPNet{mustCIDR(t, "172.18.0.0/16")}
	if _, _, err := NewLocalStack(nameEndpoints, DefaultAdapterTimeout, WithTrustedDialCIDRs(declared)); err != nil {
		t.Fatalf("service-name endpoints construct over a declared network: %v", err)
	}
	// Loopback-only trust cannot resolve a name — construction FAILS.
	if _, _, err := NewLocalStack(nameEndpoints, DefaultAdapterTimeout, WithTrustedDialCIDRs(nil)); err == nil {
		t.Fatal("a service-name endpoint with loopback-only trust must fail AT CONSTRUCTION (KPK-03: the runtime never boots half-trusting its own dial policy)")
	} else if !strings.Contains(err.Error(), "SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS") {
		t.Fatalf("the construction error must name the operator surface, got: %v", err)
	}
	// localhost resolves into the always-trusted loopback — constructs.
	localhostEndpoints := map[airuntime.Provider]Endpoint{
		airuntime.ProviderSGLang:   "http://localhost:30000",
		airuntime.ProviderVLLM:     "http://localhost:8000",
		airuntime.ProviderBeeLlama: "http://localhost:8080",
		airuntime.ProviderOllama:   "http://localhost:11434",
	}
	if _, _, err := NewLocalStack(localhostEndpoints, DefaultAdapterTimeout, WithTrustedDialCIDRs(nil)); err != nil {
		t.Fatalf("localhost endpoints construct under loopback-only trust: %v", err)
	}
	// IP-literal endpoints never resolve — any trust set constructs.
	literalEndpoints := map[airuntime.Provider]Endpoint{
		airuntime.ProviderSGLang:   "http://127.0.0.1:30000",
		airuntime.ProviderVLLM:     "http://127.0.0.1:8000",
		airuntime.ProviderBeeLlama: "http://127.0.0.1:8080",
		airuntime.ProviderOllama:   "http://127.0.0.1:11434",
	}
	if _, _, err := NewLocalStack(literalEndpoints, DefaultAdapterTimeout, WithTrustedDialCIDRs(nil)); err != nil {
		t.Fatalf("IP-literal endpoints construct under any trust set: %v", err)
	}
}

// TestLocalAdapterLocalhostNameThroughRealResolver: the PRODUCTION path
// end-to-end — a NAME endpoint ("localhost"), the real process resolver,
// real validation (loopback), real pinning, real dial, real HTTP chat.
// The pipeline resolve → validate → pin → dial → POST is exercised with
// nothing injected.
func TestLocalAdapterLocalhostNameThroughRealResolver(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"pinned"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer server.Close()
	// Re-point the endpoint at the NAME "localhost" on the same port —
	// the real resolver answers loopback, validation approves, the dial pins.
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	adapter, err := NewLocalAdapter(airuntime.ProviderSGLang, Endpoint("http://localhost:"+u.Port()), DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	resp, err := adapter.Chat(context.Background(), chatRequest())
	if err != nil {
		t.Fatalf("the production resolve-validate-pin-dial path must carry the chat: %v", err)
	}
	if resp.Message.Content != "pinned" {
		t.Fatalf("the pinned round trip must answer, got %q", resp.Message.Content)
	}
}
