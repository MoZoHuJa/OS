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

// Transport bounds. Per-attempt 30s matches the stack's own routing
// precedent (ai/smg config.yaml timeout_ms: 30000); the response body is
// read through a 1 MiB limit — a runaway response must not eat memory
// (the content itself is bounded to 4096 runes by the contract; the
// envelope leaves room for the servers' own decorations).
const (
	DefaultAdapterTimeout = 30 * time.Second
	MaxResponseBytes      = 1 << 20
)

// LocalAdapter is the real transport for one provider tier: POST
// {endpoint}/v1/chat/completions over the OpenAI-compatible wire the
// whole real local stack speaks. No auth header is sent — the documented
// local stack (SCARLIX_API_MAP.md) authenticates nobody; the endpoint
// shape itself (loopback or service name) is the boundary.
type LocalAdapter struct {
	provider airuntime.Provider
	endpoint Endpoint
	client   *http.Client
}

// NewLocalAdapter constructs the transport for one tier. The provider
// and the endpoint are validated (closed shapes, fail-closed); the
// timeout bounds every attempt (context deadlines still apply on top).
func NewLocalAdapter(provider airuntime.Provider, endpoint Endpoint, timeout time.Duration) (*LocalAdapter, error) {
	if err := provider.Validate(); err != nil {
		return nil, err
	}
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("airt: adapter timeout must be > 0 (got %s)", timeout)
	}
	return &LocalAdapter{
		provider: provider,
		endpoint: endpoint,
		client:   &http.Client{Timeout: timeout},
	}, nil
}

// Provider identifies the tier this adapter serves.
func (a *LocalAdapter) Provider() airuntime.Provider {
	return a.provider
}

// Chat performs one normalized chat completion: validate (defense in
// depth — the runtime validated too), encode onto the wire, POST, read
// the bounded body, normalize fail-closed. A non-2xx status, a timeout,
// a malformed or over-bound body — every one is an ERROR the executor
// skips past. Attribution survives failover: the response names THIS
// provider, stamped in UTC.
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
		return airuntime.Response{}, fmt.Errorf("airt: %s: chat completions answered HTTP %d (2xx required — a non-2xx tier is an adapter error the plan skips past)", a.provider, resp.StatusCode)
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

// NewLocalStack builds the full four-tier transport from the endpoints:
// one LocalAdapter per provider (DefaultAdapterTimeout) plus the health
// prober (DefaultProbeTimeout). All four tiers are ALWAYS present — the
// closed set — because health gates routing, construction must not (a
// missing tier would be a silent fail-open hole in failover).
func NewLocalStack(endpoints map[airuntime.Provider]Endpoint) (map[airuntime.Provider]Adapter, *Prober, error) {
	if err := validateEndpointSet(endpoints); err != nil {
		return nil, nil, err
	}
	adapters := make(map[airuntime.Provider]Adapter, len(airuntime.AllProviders()))
	for _, p := range airuntime.AllProviders() {
		adapter, err := NewLocalAdapter(p, endpoints[p], DefaultAdapterTimeout)
		if err != nil {
			return nil, nil, err
		}
		adapters[p] = adapter
	}
	prober, err := NewProber(endpoints, DefaultProbeTimeout)
	if err != nil {
		return nil, nil, err
	}
	return adapters, prober, nil
}
