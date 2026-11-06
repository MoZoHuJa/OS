package airt

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// ---------------------------------------------------------------------------
// The health prober (freshness is THIS package's job — stated in CF-1)
// ---------------------------------------------------------------------------

// DefaultProbeTimeout bounds one probe attempt. Probes must be fast —
// they run on a loop and on demand; a slow tier must not hold the
// operator's refresh hostage (the adapters carry the long 30s bound).
const DefaultProbeTimeout = 5 * time.Second

// probePath is the health surface per provider, exactly as the stack
// documents it (SCARLIX_API_MAP.md + the compose healthchecks): the
// three GPU/CPU servers expose /health; ollama exposes /api/tags (its
// own healthcheck is `ollama list`, the HTTP shape of that is the tags
// listing).
func probePath(p airuntime.Provider) string {
	switch p {
	case airuntime.ProviderOllama:
		return "/api/tags"
	default:
		return "/health"
	}
}

// Prober produces the observations the CF-1 health table feeds on. The
// honesty matrix, stated once: a 2xx answer is HEALTHY, a
// standing-but-unready server is DEGRADED, a dead one is UNAVAILABLE —
// and a never-probed provider stays UNKNOWN (the table's fail-closed
// default; nothing here fabricates health). Probing NEVER marks a
// provider healthy without asking it.
type Prober struct {
	endpoints map[airuntime.Provider]Endpoint
	client    *http.Client
}

// NewProber constructs the prober over a COMPLETE endpoint set (exactly
// the closed four-provider stack — validated, fail-closed) with the
// given per-attempt timeout. The HTTP client is the local-only shape
// (newLocalClient — no redirects, no ambient proxy, dial-guarded over
// the endpoint set's hosts; KPK-01, v19.6.3): probes carry no chat
// transcript, but the local-only contract is a contract — the process
// must not be steered to a destination outside the declared local
// endpoint set by anything, a redirect answer included.
func NewProber(endpoints map[airuntime.Provider]Endpoint, timeout time.Duration) (*Prober, error) {
	if err := validateEndpointSet(endpoints); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("airt: probe timeout must be > 0 (got %s)", timeout)
	}
	return &Prober{
		endpoints: endpoints,
		client:    newLocalClient(timeout, endpointHostsOf(endpoints)),
	}, nil
}

// Probe asks one provider how it is doing and returns the observation,
// stamped now (UTC — a probe without a time cannot be ordered, the CF-1
// monotone discipline). The result matrix:
//
//	HTTP 2xx          → HEALTHY    (the tier answered, ready to serve)
//	any other status  → DEGRADED   (standing but unready — e.g. 503 while
//	                                 a model loads; DEGRADED serves and the
//	                                 adapter failure path handles the rest;
//	                                 a 3xx lands here too — the local-only
//	                                 client hands it back unfollowed, KPK-01)
//	transport error   → UNAVAILABLE (refused, timed out, unreachable —
//	                                 the tier is not answering at all)
//
// An error is returned only for an invalid provider (no observation is
// fabricated for a name outside the closed enumeration).
func (p *Prober) Probe(ctx context.Context, provider airuntime.Provider) (airuntime.Observation, error) {
	if err := provider.Validate(); err != nil {
		return airuntime.Observation{}, err
	}
	state := airuntime.HealthHealthy
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoints[provider].URL(probePath(provider)), nil)
	if err != nil {
		return airuntime.Observation{}, fmt.Errorf("airt: %s: probe request construction failed: %w", provider, err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// The tier did not answer: dead, not degraded.
		state = airuntime.HealthUnavailable
	} else {
		// The tier answered. 2xx = ready; anything else = standing but
		// unready (e.g. 503 while the model loads) — DEGRADED serves
		// honestly and the failover execution handles the remainder.
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			state = airuntime.HealthDegraded
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) //nolint:errcheck — drain to reuse the connection; a drain error changes nothing
		resp.Body.Close()                                    //nolint:errcheck — already drained; Close on a drained body cannot fail meaningfully
	}
	return airuntime.Observation{
		Provider: provider,
		State:    state,
		At:       time.Now().UTC(),
	}, nil
}

// ProbeAll probes all four providers CONCURRENTLY (bounded by the closed
// set — four goroutines, no more) and returns the observations in tier
// order (DefaultFailoverOrder): probing is concurrent, the RESULT is
// deterministic in shape and order. Every provider gets an observation —
// a dead tier is observed UNAVAILABLE, never omitted (fail-closed, not
// fail-blind).
func (p *Prober) ProbeAll(ctx context.Context) ([]airuntime.Observation, error) {
	providers := airuntime.AllProviders()
	observations := make([]airuntime.Observation, len(providers))
	var wg sync.WaitGroup
	for i, provider := range providers {
		wg.Add(1)
		go func(i int, provider airuntime.Provider) {
			defer wg.Done()
			obs, err := p.Probe(ctx, provider)
			if err != nil {
				// Cannot happen for enumerated providers (Probe validates
				// membership first) — the honest guard: an unprobeable name
				// becomes UNKNOWN, which never serves.
				obs = airuntime.Observation{Provider: provider, State: airuntime.HealthUnknown, At: time.Now().UTC()}
			}
			observations[i] = obs
		}(i, provider)
	}
	wg.Wait()
	return observations, nil
}

// ProbeAllAndObserve probes all providers and records every observation
// into the health table (the table's own monotone-time discipline
// applies — fresh probe times satisfy it). Returns the observations in
// tier order, as ProbeAll does.
func (p *Prober) ProbeAllAndObserve(ctx context.Context, table *airuntime.HealthTable) ([]airuntime.Observation, error) {
	if table == nil {
		return nil, fmt.Errorf("%w: health table is required", ErrNilRuntimeInput)
	}
	observations, err := p.ProbeAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, obs := range observations {
		if err := table.Observe(obs); err != nil {
			return nil, fmt.Errorf("airt: recording observation for %s failed: %w", obs.Provider, err)
		}
	}
	return observations, nil
}

// Start runs the background freshness loop: every interval, probe all
// providers and record into the table (ProbeAllAndObserve). The loop
// runs for the process lifetime — CF-1 stated that freshness is the
// prober's job, this is that job running unattended. The returned stop
// function halts the loop (the operator-facing wiring never calls it;
// tests do).
func (p *Prober) Start(interval time.Duration, table *airuntime.HealthTable) (stop func(), err error) {
	if interval <= 0 {
		return nil, fmt.Errorf("airt: probe interval must be > 0 (got %s)", interval)
	}
	if table == nil {
		return nil, fmt.Errorf("%w: health table is required", ErrNilRuntimeInput)
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// A failed round keeps the previous observations — the
				// table never expires entries on its own (the CF-1
				// discipline); the next round tries again. The context is
				// the loop's own: a probe round must not be cancelled by
				// whoever stopped the loop MID-round.
				ctx, cancel := context.WithTimeout(context.Background(), DefaultProbeTimeout)
				_, _ = p.ProbeAllAndObserve(ctx, table) //nolint:errcheck — a failed round is retried by the next tick; observations are the record
				cancel()
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }, nil
}
