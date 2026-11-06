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

// probeRoundBudget bounds one BACKGROUND loop round (KPK-04, v19.6.4):
// deliberately 2x the per-probe timeout so the two clocks are never
// ambiguous — a slow tier is classified by the probe's OWN client timeout
// (UNAVAILABLE, genuine, parent context live), and only a pathologically
// stuck round hits the round budget (the round aborts, nothing is
// recorded, last-known-good health stands). Caller-facing rounds
// (POST /api/ai/probe) carry the caller's own context — the KPK-04 guard
// makes a cancelled caller record nothing at all.
const probeRoundBudget = 2 * DefaultProbeTimeout

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
	// preCommit is the KPK-19.6.4-P2-02 test seam: when non-nil it runs
	// BETWEEN ProbeAll completion and the health-table batch commit —
	// exactly the window where a cancellation used to race past the
	// round-end guard and still land observations. Production leaves it
	// nil (zero overhead, zero behavior change); the deterministic
	// regression test sets it to cancel the caller's context inside
	// that window, proving the commit boundary catches what the
	// round-end guard cannot. Never read or written concurrently with
	// a running round (set before the round, like every test field).
	preCommit func(observations []airuntime.Observation)
}

// NewProber constructs the prober over a COMPLETE endpoint set (exactly
// the closed four-provider stack — validated, fail-closed) with the
// given per-attempt timeout. The HTTP client is the local-only shape
// (newLocalClient — no redirects, no ambient proxy, dial-guarded over
// the endpoint set's hosts; KPK-01, v19.6.3): probes carry no chat
// transcript, but the local-only contract is a contract — the process
// must not be steered to a destination outside the declared local
// endpoint set by anything, a redirect answer included. v19.6.4
// (KPK-03): opts declare the trusted dial networks for resolved name
// destinations (no option → the Docker default pools).
func NewProber(endpoints map[airuntime.Provider]Endpoint, timeout time.Duration, opts ...DialOption) (*Prober, error) {
	if err := validateEndpointSet(endpoints); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("airt: probe timeout must be > 0 (got %s)", timeout)
	}
	trustedCIDRs := resolveDialCIDRs(opts)
	if err := validateNameEndpointDialTrust(endpoints, trustedCIDRs); err != nil {
		return nil, err
	}
	return &Prober{
		endpoints: endpoints,
		client:    newLocalClient(timeout, endpointHostsOf(endpoints), trustedCIDRs),
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
		// KPK-04 (v19.6.4): a transport failure while the CALLER'S context is
		// done is not evidence about the tier — the browser closed, the
		// proxy aborted, the deadline expired at the round level. The
		// honest answer is an ERROR (no observation), so the health
		// table's last-known-good stands; a genuine refusal/timeout/
		// unreachable (the context still live) is UNAVAILABLE below.
		if cerr := ctx.Err(); cerr != nil {
			return airuntime.Observation{}, fmt.Errorf("airt: %s: probe aborted — the caller's context is done (%w); a cancelled observation is not evidence about the provider, the last-known-good health stands (KPK-04)", provider, cerr)
		}
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
//
// KPK-04 (v19.6.4): a CANCELLED round is not evidence about the providers.
// When the caller's context is done at round end (browser closed, reverse
// proxy aborted, deadline expired), the round is aborted and the context
// error is returned — NO observations are returned and NOTHING is recorded
// by ProbeAllAndObserve: the caller cancelled the observation, the providers
// were not proven unavailable, and the health table's last-known-good
// stands. Genuine per-provider failures (refused, timed out, unreachable)
// are preserved exactly when the parent context remains live — the
// client's own per-probe timeout classifies those, distinct from the
// caller's cancellation.
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
				// becomes UNKNOWN, which never serves. A caller-cancellation
				// abort (KPK-04) lands here too — and the round-end check
				// below discards the whole round, so no synthetic state
				// survives it.
				obs = airuntime.Observation{Provider: provider, State: airuntime.HealthUnknown, At: time.Now().UTC()}
			}
			observations[i] = obs
		}(i, provider)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		// KPK-04: abort the round — no observations, nothing recorded. A
		// cancelled caller must not overwrite last-known-good health with
		// fabricated UNAVAILABLE states; errors.Is(err, context.Canceled /
		// context.DeadlineExceeded) keeps working through the %w wrap.
		return nil, fmt.Errorf("airt: probe round aborted — the caller's context is done (%w); a cancelled round is not evidence about the providers, nothing is recorded and the last-known-good health stands (KPK-04)", err)
	}
	return observations, nil
}

// ProbeAllAndObserve probes all providers and records every observation
// into the health table, ATOMICALLY (v19.6.5, KPK-19.6.4-P2-02): the whole
// round commits through HealthTable.ObserveBatch — one lock, one commit
// boundary — never a per-provider Observe loop that a cancellation could
// interrupt halfway leaving a partial round visible.
//
// The linearization rule, stated once: cancellation BEFORE the batch
// commit means ZERO writes (the caller walked away — the round is not
// evidence about the providers, last-known-good states AND timestamps
// stand); cancellation AFTER the commit does not retroactively undo a
// completed observation round (the round was live end-to-end — its
// evidence stands, honestly). A caller cancelling exactly between probe
// completion and the commit gets the context error back, errors.Is-
// identifiable, with the table untouched.
//
// KPK-04 (v19.6.4): a CANCELLED round records NOTHING — when ProbeAll
// aborts (the caller's context done at round end), this function returns
// the error BEFORE the record path runs. Only a fully completed round
// with a live parent context reaches the commit boundary.
func (p *Prober) ProbeAllAndObserve(ctx context.Context, table *airuntime.HealthTable) ([]airuntime.Observation, error) {
	if table == nil {
		return nil, fmt.Errorf("%w: health table is required", ErrNilRuntimeInput)
	}
	observations, err := p.ProbeAll(ctx)
	if err != nil {
		return nil, err
	}
	// The KPK-19.6.4-P2-02 window, made deterministic in tests: the seam
	// runs after the round-end guard has passed and before the commit —
	// a cancellation here is caught by ObserveBatch's commit boundary,
	// the one the round-end guard structurally cannot see.
	if p.preCommit != nil {
		p.preCommit(observations)
	}
	if err := table.ObserveBatch(ctx, observations); err != nil {
		return nil, fmt.Errorf("airt: committing the probe round's observations failed: %w", err)
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
				// KPK-04 (v19.6.4): the round deadline carries a MARGIN
				// beyond the per-probe client timeout (2x) so the two are
				// never ambiguous — a slow tier is classified by the
				// probe's OWN timeout (UNAVAILABLE, genuine, parent live),
				// while the round deadline only aborts a pathologically
				// stuck round (record nothing, keep last-known-good).
				ctx, cancel := context.WithTimeout(context.Background(), probeRoundBudget)
				_, _ = p.ProbeAllAndObserve(ctx, table) //nolint:errcheck — a failed round is retried by the next tick; observations are the record
				cancel()
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }, nil
}
