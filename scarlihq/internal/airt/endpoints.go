package airt

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// ---------------------------------------------------------------------------
// Endpoints (the documented local stack — loopback or service names only)
// ---------------------------------------------------------------------------

// Endpoint is one provider's base URL: scheme://host:port and NOTHING
// more. The closed shape is enforced by Validate: http only (the local
// stack is plain HTTP — no TLS anywhere in it), no userinfo, no path,
// no query, no fragment, an explicit port (the stack never serves on
// :80), and a host that is either a loopback address or a single-label
// docker service name. A DOTTED host is the cloud-gateway shape —
// rejected by design: the runtime is not a cloud-LLM gateway.
type Endpoint string

// DefaultEndpoints are the SCARLIX_API_MAP.md documented addresses of the
// real local stack (release-locked like DefaultFailoverOrder):
//
//	sglang    http://127.0.0.1:30000   (Tier 1, GPU 0)
//	vllm      http://127.0.0.1:8089    (host port → container :8000, Tier 2)
//	beellama  http://127.0.0.1:11438   (host port → container :8080, Tier 3)
//	ollama    http://127.0.0.1:11435   (host port → container :11434, Tier 4)
//
// Inside a container on scarlix-net the same stack is reachable by
// service name — that is what the env overrides exist for
// (EndpointsFromEnv); the defaults describe the bare-metal host view.
func DefaultEndpoints() map[airuntime.Provider]Endpoint {
	return map[airuntime.Provider]Endpoint{
		airuntime.ProviderSGLang:   "http://127.0.0.1:30000",
		airuntime.ProviderVLLM:     "http://127.0.0.1:8089",
		airuntime.ProviderBeeLlama: "http://127.0.0.1:11438",
		airuntime.ProviderOllama:   "http://127.0.0.1:11435",
	}
}

// isLoopbackHost reports whether host is a loopback literal: 127.0.0.1,
// localhost, ::1 or [::1]. Anything else must pass the service-name
// shape to be accepted.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// isServiceName reports whether host is a single-label docker service
// name: lowercase [a-z0-9-], no dots, 1..63 runes — the scarlix-net
// container names (sglang, vllm, beellama, ollama-agent). A dotted host
// (api.openai.com, sglang.internal) is NOT this shape and is rejected:
// the runtime is not a cloud-LLM gateway.
func isServiceName(host string) bool {
	if host == "" || len(host) > 63 {
		return false
	}
	for _, r := range host {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return host[0] != '-' && host[len(host)-1] != '-'
}

// Validate checks the closed endpoint shape. Rejected, by name of rule:
// non-http schemes (https included — the local stack is plain HTTP),
// userinfo, hosts that are neither loopback nor a single-label service
// name (the cloud-gateway guard — it fires BEFORE the cosmetic rules so
// a cloud URL is named as a cloud URL, not as a "path" complaint),
// paths, queries, fragments, implicit ports, non-numeric or
// out-of-range ports.
func (e Endpoint) Validate() error {
	s := string(e)
	if s == "" {
		return fmt.Errorf("airt: endpoint is empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("airt: endpoint %q does not parse as a URL: %v", s, err)
	}
	if u.Scheme != "http" {
		return fmt.Errorf("airt: endpoint %q must use http (the local stack is plain HTTP, no TLS anywhere in it)", s)
	}
	if u.User != nil {
		return fmt.Errorf("airt: endpoint %q carries userinfo — the local stack has no auth in the URL", s)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("airt: endpoint %q has no host", s)
	}
	if !isLoopbackHost(host) && !isServiceName(host) {
		return fmt.Errorf("airt: endpoint %q: host %q is neither loopback nor a single-label service name — the runtime is not a cloud-LLM gateway (dotted public hosts are rejected by design)", s, host)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("airt: endpoint %q carries a path %q — an endpoint is scheme://host:port, nothing more", s, u.Path)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.RawFragment != "" {
		return fmt.Errorf("airt: endpoint %q carries a query or fragment — an endpoint is scheme://host:port, nothing more", s)
	}
	port := u.Port()
	if port == "" {
		return fmt.Errorf("airt: endpoint %q has no explicit port (the local stack never serves on the implicit port)", s)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("airt: endpoint %q port %q is not a valid port number", s, port)
	}
	return nil
}

// URL returns the endpoint joined with path (e.g. "/v1/chat/completions").
// The endpoint must validate — a malformed base never reaches the wire.
func (e Endpoint) URL(path string) string {
	return strings.TrimRight(string(e), "/") + path
}

// endpointEnvVar maps each provider to its override variable. The four
// names are the documented compose surface (SCARLIHQ_AIRUNTIME_*_URL).
func endpointEnvVar(p airuntime.Provider) string {
	switch p {
	case airuntime.ProviderSGLang:
		return "SCARLIHQ_AIRUNTIME_SGLANG_URL"
	case airuntime.ProviderVLLM:
		return "SCARLIHQ_AIRUNTIME_VLLM_URL"
	case airuntime.ProviderBeeLlama:
		return "SCARLIHQ_AIRUNTIME_BEELLAMA_URL"
	case airuntime.ProviderOllama:
		return "SCARLIHQ_AIRUNTIME_OLLAMA_URL"
	}
	return ""
}

// EndpointsFromEnv reads the per-provider endpoint overrides over the
// defaults. getenv is injected (tests); the four documented variables are
// SCARLIHQ_AIRUNTIME_SGLANG_URL / _VLLM_URL / _BEELLAMA_URL / _OLLAMA_URL.
// Every provided value is validated — an invalid override is an ERROR,
// never a silent fall-back to the default (a typo must fail loudly).
func EndpointsFromEnv(getenv func(string) string) (map[airuntime.Provider]Endpoint, error) {
	endpoints := DefaultEndpoints()
	for _, p := range airuntime.AllProviders() {
		raw := strings.TrimSpace(getenv(endpointEnvVar(p)))
		if raw == "" {
			continue
		}
		e := Endpoint(raw)
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("airt: %s: %w", endpointEnvVar(p), err)
		}
		endpoints[p] = e
	}
	return endpoints, nil
}

// validateEndpointSet checks that endpoints cover EXACTLY the closed
// four-provider stack — every tier present, nothing foreign. Missing or
// extra entries are named in the error (fail-closed construction).
func validateEndpointSet(endpoints map[airuntime.Provider]Endpoint) error {
	if endpoints == nil {
		return fmt.Errorf("airt: endpoints are required (nil map)")
	}
	for _, p := range airuntime.AllProviders() {
		e, ok := endpoints[p]
		if !ok {
			return fmt.Errorf("%w: no endpoint for %s", ErrIncompleteAdapterSet, p)
		}
		if err := e.Validate(); err != nil {
			return fmt.Errorf("airt: endpoint for %s: %w", p, err)
		}
	}
	for p := range endpoints {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("airt: endpoint set carries a foreign provider %q: %w", p, err)
		}
	}
	return nil
}
