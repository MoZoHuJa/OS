package airt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// ---------------------------------------------------------------------------
// The real adapter (one local-stack tier behind the contract)
// ---------------------------------------------------------------------------

// Transport bounds. The 30s per-attempt default matches the stack's own
// routing precedent (ai/smg config.yaml timeout_ms: 30000; the operator
// may widen it through SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT up to
// MaxAdapterTimeout — a named upper bound, never unbounded); the response
// body is read through a 1 MiB limit — a runaway response must not eat
// memory (the content itself is bounded to 4096 runes by the contract;
// the envelope leaves room for the servers' own decorations).
//
// The END-TO-END budget for one AI request is ChatBudget(perAttempt) —
// per-attempt × the closed four-tier set, the complete honest failover
// walk (KPK-02, v19.6.3). The API layer imposes it as the request
// context deadline; every attempt propagates it; the HTTP write path for
// /api/ai/chat and /api/ai/propose is extended beyond it with a write
// margin (see internal/api/airuntime.go) — the server's 30s default
// WriteTimeout keeps serving every other surface unchanged.
const (
	DefaultAdapterTimeout = 30 * time.Second
	MaxResponseBytes      = 1 << 20
)

// ChatBudget returns the END-TO-END budget for one AI chat request:
// exactly the per-attempt timeout × the closed four-tier set — the
// complete honest failover walk, sequential attempts and all. One
// coherent number, documented once, imposed at the API layer (the
// request context deadline) and propagated through every adapter and
// probe operation (KPK-02, v19.6.3 — the per-attempt allowance and the
// total budget are the SAME contract, never two unrelated numbers).
func ChatBudget(perAttempt time.Duration) time.Duration {
	return perAttempt * time.Duration(len(airuntime.AllProviders()))
}

// LocalAdapter is the real transport for one provider tier: POST
// {endpoint}/v1/chat/completions over the OpenAI-compatible wire the
// whole real local stack speaks. No auth header is sent — the documented
// local stack (SCARLIX_API_MAP.md) authenticates nobody; the endpoint
// shape itself (loopback or service name) is the boundary. The HTTP
// client is the local-only shape (newLocalClient — no redirects, no
// ambient proxy, dial-guarded; KPK-01, v19.6.3): a 3xx answer is an
// adapter error, never a followed hop, so a compromised tier cannot
// bounce the conversation (the POST body — private prompts) anywhere.
type LocalAdapter struct {
	provider airuntime.Provider
	endpoint Endpoint
	client   *http.Client
}

// NewLocalAdapter constructs the transport for one tier. The provider
// and the endpoint are validated (closed shapes, fail-closed); the
// timeout bounds every attempt (the end-to-end budget rides the request
// context on top; both apply, whichever is shorter). v19.6.4 (KPK-03):
// opts declare the trusted dial networks for resolved name destinations
// (no option → the Docker default pools; a declared set replaces them —
// see WithTrustedDialCIDRs and TrustedDialCIDRsFromEnv).
func NewLocalAdapter(provider airuntime.Provider, endpoint Endpoint, timeout time.Duration, opts ...DialOption) (*LocalAdapter, error) {
	if err := provider.Validate(); err != nil {
		return nil, err
	}
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("airt: adapter timeout must be > 0 (got %s)", timeout)
	}
	if timeout > MaxAdapterTimeout {
		return nil, fmt.Errorf("airt: adapter timeout must be <= %s (got %s — SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT is bounded, never unbounded)", MaxAdapterTimeout, timeout)
	}
	trustedCIDRs := resolveDialCIDRs(opts)
	if err := validateNameEndpointDialTrust(map[airuntime.Provider]Endpoint{provider: endpoint}, trustedCIDRs); err != nil {
		return nil, err
	}
	return &LocalAdapter{
		provider: provider,
		endpoint: endpoint,
		client:   newLocalClient(timeout, endpointHostSet(endpoint), trustedCIDRs),
	}, nil
}

// Provider identifies the tier this adapter serves.
func (a *LocalAdapter) Provider() airuntime.Provider {
	return a.provider
}

// Chat performs one normalized chat completion: validate (defense in
// depth — the runtime validated too), encode onto the wire, POST, read
// the bounded body, normalize fail-closed. A non-2xx status (a refused
// 3xx included — the local-only client never follows redirects, KPK-01),
// a timeout, a malformed or over-bound body — every one is an ERROR the
// executor skips past. Attribution survives failover: the response names
// THIS provider, stamped in UTC. The error never carries the request or
// response body: prompt content must not leak through failure paths.
func (a *LocalAdapter) Chat(ctx context.Context, req airuntime.Request) (airuntime.Response, error) {
	if err := req.Validate(); err != nil {
		return airuntime.Response{}, err
	}
	payload, err := json.Marshal(encodeWireRequest(req))
	if err != nil {
		return airuntime.Response{}, fmt.Errorf("airt: %s: request encoding failed: %w", a.provider, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint.URL("/v1/chat/completions"), bytes.NewReader(payload))
	if err != nil {
		return airuntime.Response{}, fmt.Errorf("airt: %s: request construction failed: %w", a.provider, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return airuntime.Response{}, fmt.Errorf("airt: %s: transport failed: %w", a.provider, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return airuntime.Response{}, fmt.Errorf("airt: %s: chat completions answered HTTP %d (2xx required — a non-2xx tier is an adapter error the plan skips past; a 3xx is never followed: the local-only client has no redirect policy to violate)", a.provider, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return airuntime.Response{}, fmt.Errorf("airt: %s: response read failed: %w", a.provider, err)
	}
	if len(body) > MaxResponseBytes {
		return airuntime.Response{}, fmt.Errorf("airt: %s: response body exceeds the %d byte transport bound", a.provider, MaxResponseBytes)
	}
	return decodeWireResponse(a.provider, req.Model, body, time.Now().UTC())
}

// NewLocalStack builds the full four-tier transport from the endpoints
// with the given per-attempt chat timeout (DefaultAdapterTimeout is the
// stack's own precedent; SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT widens it within
// MaxAdapterTimeout — main.go validates the env once, fail-closed): one
// LocalAdapter per tier plus the health prober (DefaultProbeTimeout).
// All four tiers are ALWAYS present — the closed set — because health
// gates routing, construction must not (a missing tier would be a silent
// fail-open hole in failover). v19.6.4 (KPK-03): opts declare the trusted
// dial networks (no option → the Docker default pools).
func NewLocalStack(endpoints map[airuntime.Provider]Endpoint, chatTimeout time.Duration, opts ...DialOption) (map[airuntime.Provider]Adapter, *Prober, error) {
	if err := validateEndpointSet(endpoints); err != nil {
		return nil, nil, err
	}
	adapters := make(map[airuntime.Provider]Adapter, len(airuntime.AllProviders()))
	for _, p := range airuntime.AllProviders() {
		adapter, err := NewLocalAdapter(p, endpoints[p], chatTimeout, opts...)
		if err != nil {
			return nil, nil, err
		}
		adapters[p] = adapter
	}
	prober, err := NewProber(endpoints, DefaultProbeTimeout, opts...)
	if err != nil {
		return nil, nil, err
	}
	return adapters, prober, nil
}
