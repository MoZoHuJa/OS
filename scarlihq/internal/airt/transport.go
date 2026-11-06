package airt

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// ---------------------------------------------------------------------------
// The local-only HTTP client (v19.6.3, KPK-01): ONE constructor, closed
// by construction — no redirects, no ambient proxy, no foreign dials
// ---------------------------------------------------------------------------

// MaxAdapterTimeout is the upper bound of a per-attempt adapter timeout
// (the SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT operator surface). The 30s default
// is the stack's own routing precedent (ai/smg timeout_ms: 30000); an
// operator may widen it for big local generations — never unboundedly:
// the per-attempt cap and the end-to-end budget (ChatBudget) stay named,
// bounded numbers.
const MaxAdapterTimeout = 300 * time.Second

// localDialTimeout bounds one TCP dial inside the local-only transport:
// a dead-but-listening port must fail FAST — failover must not wait on a
// stalled connect (the same reason probes carry their own short bound).
const localDialTimeout = 5 * time.Second

// newLocalClient builds THE ONLY http.Client shape this package ever
// constructs. The local-only contract, closed at THREE layers (KPK-01,
// v19.6.3 — defense in depth, each layer would hold alone):
//
//   - NO REDIRECTS: CheckRedirect returns http.ErrUseLastResponse, so a
//     307/308 is handed back AS the response — the adapter's 2xx rule and
//     the prober's honesty matrix then classify it as the failure it is.
//     Go's default client FOLLOWS redirects and would replay the POST body
//     (the whole normalized conversation — private prompts) onto whatever
//     Location a compromised or misconfigured local tier answers. The
//     endpoint allowlist constrains the CONFIGURED destination, never a
//     redirect destination — only a no-follow policy closes that hole.
//   - NO AMBIENT PROXY: Transport.Proxy is explicitly nil — HTTP_PROXY /
//     HTTPS_PROXY environment variables never steer local AI traffic
//     through a third party. Proxy support is not a feature of this
//     transport and cannot become one by accident; it would be a separate,
//     explicit, reviewed act.
//   - DIAL GUARD: the transport's DialContext refuses any destination
//     outside loopback and the configured endpoint hosts. Hostname syntax
//     at the endpoint layer does not prove where a connection actually
//     goes (a smuggled URL, a future redirect path); the dial layer does.
//
// The client timeout is the PER-ATTEMPT bound; the end-to-end budget
// rides the request context (KPK-02) — both apply, whichever is shorter.
func newLocalClient(timeout time.Duration, allowedHosts map[string]struct{}) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: newLocalTransport(allowedHosts),
	}
}

// newLocalTransport builds the dial-guarded, proxy-free transport. The
// local stack is plain HTTP/1.1 on loopback or service names — no HTTP/2
// negotiation, a small bounded connection pool (the closed set is four
// tiers; idle connections to dead tiers must not linger).
func newLocalTransport(allowedHosts map[string]struct{}) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   localDialTimeout,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		Proxy: nil, // no ambient proxy inheritance — the local-only contract (KPK-01)
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil || !isLocalDialTarget(host, allowedHosts) {
				return nil, fmt.Errorf("airt: dial %s refused — the local-only transport dials loopback and the configured endpoint hosts, nothing else (KPK-01: the redirect/proxy boundary is closed at the dial layer too)", addr)
			}
			return dialer.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:   false, // plain HTTP/1.1 — the local stack carries no h2
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
	}
}

// isLocalDialTarget reports whether host may be dialed by the local-only
// transport: a loopback literal, or one of the configured endpoint hosts
// (the single-label service names the endpoint validator already vetted).
func isLocalDialTarget(host string, allowedHosts map[string]struct{}) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	_, ok := allowedHosts[strings.ToLower(host)]
	return ok
}

// endpointHostSet extracts the single host of one endpoint — the
// allowed-dial set of a one-tier adapter (the endpoint's own Validate
// already vetted the shape; the dial guard re-asserts the destination).
func endpointHostSet(e Endpoint) map[string]struct{} {
	hosts := map[string]struct{}{}
	if u, err := url.Parse(string(e)); err == nil && u.Hostname() != "" {
		hosts[strings.ToLower(u.Hostname())] = struct{}{}
	}
	return hosts
}

// endpointHostsOf extracts every host of an endpoint set — the
// allowed-dial set of the prober (all four tiers of the closed stack).
func endpointHostsOf(endpoints map[airuntime.Provider]Endpoint) map[string]struct{} {
	hosts := map[string]struct{}{}
	for _, e := range endpoints {
		if u, err := url.Parse(string(e)); err == nil && u.Hostname() != "" {
			hosts[strings.ToLower(u.Hostname())] = struct{}{}
		}
	}
	return hosts
}
