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
// by construction — no redirects, no ambient proxy, no foreign dials.
// v19.6.4 (KPK-03): the dial guard grew teeth — a NAME is now resolved,
// every answered address is validated against the trusted local networks,
// and the connection is PINNED to a validated address.
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

// TrustedCIDRsEnvVar is the operator surface of the KPK-03 destination
// trust: a comma-separated list of CIDRs the resolved addresses of the
// configured endpoint names must land in (e.g.
// "172.18.0.0/16,fd00::/8"). Loopback (127.0.0.0/8, ::1) is ALWAYS
// trusted and never needs declaring. Unset/empty → Docker's documented
// DEFAULT address pool, enumerated as fifteen canonical /16 networks
// (172.17.0.0/16 … 172.31.0.0/16 — see dockerDefaultPoolCIDRs) — v19.6.6
// (KPK-19.6.5-P2-01): the non-canonical "172.17.0.0/12" shorthand is
// gone, it masked to 172.16.0.0/12 and trusted 172.16.0.0/16 (not a
// Docker default-pool range) — v19.6.5 (KPK-19.6.4-P2-01) history:
// deliberately NOT the whole RFC1918 space and deliberately NOT the
// home-LAN range 192.168.0.0/16 — that range is operator territory
// (routers, NAS, workstations — unrelated LAN hosts a poisoned resolver
// could have steered prompts to); an operator whose scarlix-net lives
// there DECLARES it explicitly. 10.0.0.0/8 is likewise out until
// declared. An explicitly declared set REPLACES the default (the operator
// takes full control of the non-loopback networks). A UNIVERSAL route
// (0.0.0.0/0, ::/0) is refused — a declaration that would nullify the
// boundary is a configuration error, not a posture.
const TrustedCIDRsEnvVar = "SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS"

// ipResolverFunc resolves one host to its addresses — injectable so the
// KPK-03 policy is testable without touching real DNS (the production
// implementation is the process's own resolver: Docker's embedded DNS
// inside the container, the host resolver on bare metal).
type ipResolverFunc func(ctx context.Context, host string) ([]net.IP, error)

// dialFunc dials one validated address — injectable so the KPK-03 pinning
// is testable without real networks (the production implementation is a
// bounded net.Dialer).
type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// defaultIPResolver is the production resolver: the process's own
// (Docker DNS / host resolv.conf) — the TRUST does not live in the
// resolver, it lives in the validation that follows it (KPK-03: a
// compromised or poisoned resolver can only produce REFUSED dials now).
func defaultIPResolver(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

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
//   - DIAL GUARD (KPK-01 + KPK-03): the transport's DialContext refuses
//     any destination outside loopback and the configured endpoint hosts
//     (the hostname allowlist, unchanged), and — for a NAME — RESOLVES it
//     through the process's own resolver, validates EVERY answered
//     address against the trusted local networks, and PINNS the
//     connection to a validated address. The IP that is dialed is the IP
//     that was validated: there is no validate-then-resolve gap. A mixed
//     answer set (one approved + one foreign address) is refused WHOLE —
//     the documented fail-closed policy, never a filtered guess.
//
// The client timeout is the PER-ATTEMPT bound; the end-to-end budget
// rides the request context (KPK-02) — both apply, whichever is shorter.
func newLocalClient(timeout time.Duration, allowedHosts map[string]struct{}, trustedCIDRs []*net.IPNet) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: newLocalTransport(allowedHosts, trustedCIDRs),
	}
}

// newLocalTransport builds the dial-guarded, proxy-free transport with
// the production resolver and dialer. The local stack is plain HTTP/1.1
// on loopback or service names — no HTTP/2 negotiation, a small bounded
// connection pool (the closed set is four tiers; idle connections to
// dead tiers must not linger).
func newLocalTransport(allowedHosts map[string]struct{}, trustedCIDRs []*net.IPNet) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   localDialTimeout,
		KeepAlive: 30 * time.Second,
	}
	return newLocalTransportDialing(allowedHosts, trustedCIDRs, defaultIPResolver, dialer.DialContext)
}

// newLocalTransportDialing is the injectable core of the dial guard (the
// KPK-03 test surface: a fake resolver and a recording dialer prove the
// policy deterministically — no real DNS, no real network). The Host
// header of an HTTP request is set from the request URL and is unaffected
// by the pinned dial address; should TLS ever be introduced on the local
// stack, the TLS server name must be pinned the same way (out of scope —
// the endpoint shape rejects https by design).
func newLocalTransportDialing(allowedHosts map[string]struct{}, trustedCIDRs []*net.IPNet, resolve ipResolverFunc, dial dialFunc) *http.Transport {
	return &http.Transport{
		Proxy: nil, // no ambient proxy inheritance — the local-only contract (KPK-01)
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil || !isLocalDialTarget(host, allowedHosts) {
				return nil, fmt.Errorf("airt: dial %s refused — the local-only transport dials loopback and the configured endpoint hosts, nothing else (KPK-01: the redirect/proxy boundary is closed at the dial layer too)", addr)
			}
			hostNorm := strings.ToLower(strings.Trim(host, "[]"))
			// An IP literal: the destination IS the address — validate it
			// directly, no resolution, no pinning needed (loopback literals
			// are always trusted; a foreign literal is refused HERE).
			if ip := net.ParseIP(hostNorm); ip != nil {
				if !ipIsTrusted(ip, trustedCIDRs) {
					return nil, fmt.Errorf("airt: dial %s refused — the address is outside the trusted local networks (KPK-03: the dialed destination is validated, never just the name)", addr)
				}
				return dial(ctx, network, addr)
			}
			// A NAME (localhost or a configured service name): KPK-03 —
			// resolve, validate EVERY answer, pin. The hostname allowlist
			// passed above; the ADDRESS the connection actually goes to is
			// what matters now: a poisoned resolver, a compromised Docker
			// DNS or a doctored /etc/hosts can only produce REFUSED dials.
			ips, rerr := resolve(ctx, hostNorm)
			if rerr != nil {
				return nil, fmt.Errorf("airt: resolve %s refused — name resolution failed, fail closed (KPK-03): %w", hostNorm, rerr)
			}
			approved := approvedResolvedIPs(ips, trustedCIDRs)
			if len(approved) == 0 {
				return nil, fmt.Errorf("airt: dial %s refused — resolution answered %v and the answer set is not wholly inside the trusted local networks (KPK-03: fail closed on mixed or empty answers — the dialed destination is validated, never just the name)", hostNorm, ips)
			}
			// PIN: dial only validated addresses, in resolver order (the
			// first that connects wins). Every dialed IP is a member of the
			// validated set — a DNS answer that changes between validation
			// and dial cannot steer this connection anywhere.
			var lastErr error
			for _, ip := range approved {
				conn, derr := dial(ctx, network, net.JoinHostPort(ip.String(), port))
				if derr == nil {
					return conn, nil
				}
				lastErr = derr
			}
			return nil, fmt.Errorf("airt: dial %s failed on every validated address (KPK-03: the pinned destinations %v were tried, in resolver order): %w", hostNorm, approved, lastErr)
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
// This is the LAYER-ONE hostname allowlist; the address-level validation
// (KPK-03) happens after it, at resolution time.
func isLocalDialTarget(host string, allowedHosts map[string]struct{}) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	_, ok := allowedHosts[strings.ToLower(host)]
	return ok
}

// ipIsTrusted reports whether ip may be a connection destination: loopback
// is ALWAYS trusted (definitionally local — the bare-metal default
// endpoints), plus membership in the declared trusted networks. This is
// the KPK-03 address-level boundary.
func ipIsTrusted(ip net.IP, trustedCIDRs []*net.IPNet) bool {
	if ip.IsLoopback() {
		return true
	}
	for _, n := range trustedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// approvedResolvedIPs validates a whole resolution answer: EVERY address
// must be trusted, else nil — the documented fail-closed policy for MIXED
// answers (one approved + one foreign address refuses the dial whole;
// the implementation never filters a foreign address out and guesses).
// An empty answer is likewise refused: no address, no dial.
func approvedResolvedIPs(ips []net.IP, trustedCIDRs []*net.IPNet) []net.IP {
	if len(ips) == 0 {
		return nil
	}
	for _, ip := range ips {
		if !ipIsTrusted(ip, trustedCIDRs) {
			return nil
		}
	}
	return ips
}

// dockerDefaultPoolCIDRs is the EXPLICIT enumeration of Docker's documented
// default address pool (172.17.0.0 through 172.31.255.255) as fifteen
// CANONICAL /16 networks. v19.6.6 (KPK-19.6.5-P2-01): the v19.6.5 default
// was the string "172.17.0.0/12" — a NON-canonical network address whose
// /12 mask PARSES (and masks) to 172.16.0.0/12, silently trusting the whole
// 172.16.0.0–172.31.255.255 range including 172.16.0.0/16, which is NOT
// part of Docker's default pool (the pool begins at 172.17.0.0) and is
// commonly corporate-VPN/overlay space. The same class of trust-scope
// mistake v19.6.5 removed for 192.168.0.0/16, in a different private
// range. The remediation follows the independent audit's backward-
// compatibility option: enumerate the exact documented pool CIDRs as
// SEPARATE entries — each canonical, each justifiable, no supernet that
// includes unrelated address space. 172.16.0.0/16 is now REJECTED by
// default (regression-tested by name); every entry below survives
// net.ParseCIDR unchanged (canonical network == the literal itself), so
// the trust range stated here is the trust range enforced in code.
// Loopback is always trusted (ipIsTrusted) and is not listed. A fresh
// slice every call — no shared mutable state.
var dockerDefaultPoolCIDRs = []string{
	"172.17.0.0/16", // Docker default bridge network (the pool's first /16)
	"172.18.0.0/16", // user-defined networks carved from the default pool
	"172.19.0.0/16",
	"172.20.0.0/16",
	"172.21.0.0/16",
	"172.22.0.0/16",
	"172.23.0.0/16",
	"172.24.0.0/16",
	"172.25.0.0/16",
	"172.26.0.0/16",
	"172.27.0.0/16",
	"172.28.0.0/16",
	"172.29.0.0/16",
	"172.30.0.0/16",
	"172.31.0.0/16", // the pool's last /16 — 172.32.0.0 and beyond are OUT
}

// DefaultTrustedDialCIDRs returns the trusted local networks applied when
// SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS is unset: Docker's documented DEFAULT
// address pool, enumerated as fifteen canonical /16 networks
// (172.17.0.0/16 … 172.31.0.0/16 — see dockerDefaultPoolCIDRs). v19.6.6
// (KPK-19.6.5-P2-01): the non-canonical "172.17.0.0/12" shorthand is GONE —
// it masked to 172.16.0.0/12 and trusted 172.16.0.0/16 (not a Docker
// default-pool range) by accident of CIDR normalization; the enumerated
// /16s state the ACTUAL trust range and nothing wider. v19.6.5
// (KPK-19.6.4-P2-01) history: the home-LAN pool 192.168.0.0/16 was removed
// from the implicit default — it is operator territory, declared, never
// implicit. 10.0.0.0/8 is likewise out until declared. An operator whose
// scarlix-net lives outside the default pool (custom address pools,
// 192.168.x, 10.x) declares the ACTUAL subnet via
// SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS (obtain it with
// `docker network inspect scarlix-net`) — explicit, visible, auditable.
// The operator-facing WARNING for service-name endpoints without a
// declaration is UndeclaredServiceNameEndpoints (main.go logs it).
func DefaultTrustedDialCIDRs() []*net.IPNet {
	return parseCIDRsOrPanic(dockerDefaultPoolCIDRs...)
}

// UndeclaredServiceNameEndpoints returns the providers whose endpoint host
// is a service NAME (not "localhost", not an IP literal) — the endpoints
// whose resolution can only land inside explicitly trusted networks.
// v19.6.6 (the SK-audit P2-01 guidance): when such endpoints exist while
// SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS is UNSET, the caller is riding the
// Docker default pool enumeration; a scarlix-net created OUTSIDE that pool
// (custom address pools, 192.168.x, 10.x) will fail at DIAL time, not at
// boot. main.go surfaces this as a loud startup WARNING with the exact
// derivation command — the operator declares the actual subnet before
// production, explicit, visible, auditable (the fail-closed construction
// for loopback-only trust is unchanged and separate).
func UndeclaredServiceNameEndpoints(endpoints map[airuntime.Provider]Endpoint) []airuntime.Provider {
	var names []airuntime.Provider
	for _, p := range airuntime.AllProviders() {
		e, ok := endpoints[p]
		if !ok {
			continue
		}
		u, err := url.Parse(string(e))
		if err != nil || u.Hostname() == "" {
			continue // the endpoint validator already refuses this shape
		}
		host := strings.ToLower(u.Hostname())
		if host == "localhost" || net.ParseIP(host) != nil {
			continue // loopback name or an IP literal — resolution lands where the trust already is
		}
		names = append(names, p)
	}
	return names
}

// parseCIDRsOrPanic turns literal CIDR strings into networks. The inputs
// are compile-time constants (the Docker default pools) — a malformed one
// is a programming error, not an operator input.
func parseCIDRsOrPanic(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			panic(fmt.Sprintf("airt: malformed constant CIDR %q (programming error)", c))
		}
		out = append(out, ipnet)
	}
	return out
}

// isUniversalCIDR reports whether a declared network would cover EVERY
// destination of its address family (0.0.0.0/0, ::/0 — a universal
// route). v19.6.5 (the independent v19.6.4 audit's hardening note):
// such a declaration does not NARROW the boundary, it NULLIFIES it —
// every answered address lands "inside" the trust set and the KPK-03
// destination validation becomes a no-op. It is refused as a
// configuration error (fail-closed, the env discipline of every
// surface); an operator with an unusual topology declares the ACTUAL
// networks, never the whole address space.
func isUniversalCIDR(ipnet *net.IPNet) bool {
	if ipnet == nil {
		return false
	}
	ones, bits := ipnet.Mask.Size()
	if bits == 0 {
		return false
	}
	return ones == 0
}

// TrustedDialCIDRsFromEnv parses SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS: a
// comma-separated list of CIDRs. Unset/blank → (nil, nil) — the caller
// applies the defaults. A malformed entry is an ERROR (fail-closed, the
// env discipline of every surface); a UNIVERSAL route (0.0.0.0/0, ::/0)
// is likewise an ERROR — v19.6.5: a declaration that would nullify the
// destination boundary is rejected, not applied (the independent audit's
// hardening recommendation, adopted as policy). The result REPLACES the
// default pool: an operator declaring networks takes full control of the
// non-loopback trust. Loopback never needs declaring (always trusted).
func TrustedDialCIDRsFromEnv(getenv func(string) string) ([]*net.IPNet, error) {
	raw := strings.TrimSpace(getenv(TrustedCIDRsEnvVar))
	if raw == "" {
		return nil, nil
	}
	var cidrs []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("airt: %s: %q is not a valid CIDR (comma-separated, e.g. 172.18.0.0/16 — loopback is always trusted and never needs declaring)", TrustedCIDRsEnvVar, part)
		}
		if isUniversalCIDR(ipnet) {
			return nil, fmt.Errorf("airt: %s: %q is a UNIVERSAL route — declaring the whole address space would nullify the destination boundary (KPK-03); declare the ACTUAL deployment networks instead (e.g. the scarlix-net subnet from 'docker network inspect scarlix-net')", TrustedCIDRsEnvVar, part)
		}
		cidrs = append(cidrs, ipnet)
	}
	return cidrs, nil
}

// DialOption carries one constructor-level dial policy choice. Exactly
// one option exists today: the trusted dial networks (KPK-03). A nil
// option slice — every pre-v19.6.4 call site — applies
// DefaultTrustedDialCIDRs() (the Docker default address pool, v19.6.6:
// enumerated canonical /16s 172.17.0.0/16 … 172.31.0.0/16, no
// non-canonical supernet); WithTrustedDialCIDRs REPLACES the default
// with the declared set — a nil or EMPTY declared set trusts loopback
// ONLY, which fails closed at construction for service-name endpoints
// (use IP literals or declare the networks instead).
type DialOption struct {
	trustedCIDRs     []*net.IPNet
	declaredNetworks bool
}

// WithTrustedDialCIDRs declares the trusted local networks for resolved
// endpoint-name destinations, REPLACING the Docker default pool. The
// natural source is TrustedDialCIDRsFromEnv (operator env) — main.go
// wires exactly that. Callers wanting the default pass NO option; an
// empty declared set is the explicit loopback-only posture.
func WithTrustedDialCIDRs(cidrs []*net.IPNet) DialOption {
	return DialOption{trustedCIDRs: cidrs, declaredNetworks: true}
}

// resolveDialCIDRs computes the effective trusted network set: the LAST
// declared option wins (a nil or empty declared set = loopback only); no
// declaration at all → the Docker default address pool (the enumerated
// canonical /16s, v19.6.6: no 172.16.0.0/16, no home-LAN range by
// default).
func resolveDialCIDRs(opts []DialOption) []*net.IPNet {
	for i := len(opts) - 1; i >= 0; i-- {
		if opts[i].declaredNetworks {
			return opts[i].trustedCIDRs
		}
	}
	return DefaultTrustedDialCIDRs()
}

// hasNonLoopbackNetwork reports whether any trusted network is outside
// loopback — the precondition for resolving a service NAME (a name can
// land anywhere the resolver says; only a declared non-loopback network
// says where it is SUPPOSED to land).
func hasNonLoopbackNetwork(trustedCIDRs []*net.IPNet) bool {
	for _, n := range trustedCIDRs {
		if !n.IP.IsLoopback() {
			return true
		}
	}
	return false
}

// validateNameEndpointDialTrust fail-closes construction when an
// endpoint carries a service NAME but the trusted network set cannot
// carry a name resolution: a name that is not "localhost" (which resolves
// to the always-trusted loopback) needs at least one non-loopback trusted
// network — declared via SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS or covered by
// the Docker default pools. The error is a CONSTRUCTION error, never a
// per-request surprise: the runtime never boots half-trusting its own
// dial policy.
func validateNameEndpointDialTrust(endpoints map[airuntime.Provider]Endpoint, trustedCIDRs []*net.IPNet) error {
	for _, p := range airuntime.AllProviders() {
		e, ok := endpoints[p]
		if !ok {
			continue
		}
		u, err := url.Parse(string(e))
		if err != nil || u.Hostname() == "" {
			continue // the endpoint validator already refuses this shape
		}
		host := strings.ToLower(u.Hostname())
		if host == "localhost" || net.ParseIP(host) != nil {
			continue // loopback name or an IP literal — no non-loopback trust needed
		}
		if !hasNonLoopbackNetwork(trustedCIDRs) {
			return fmt.Errorf("airt: endpoint for %s uses the service name %q, but no non-loopback trusted dial network is declared (SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS — the Docker default address pool 172.17.0.0/16…172.31.0.0/16 applies when unset; an empty declaration trusts loopback only and cannot resolve a name; use an IP literal or declare the network, e.g. the scarlix-net subnet from 'docker network inspect scarlix-net')", p, host)
		}
	}
	return nil
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
