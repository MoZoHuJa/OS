// Package infer_ssd — health.go: the health probe contract (roadmap
// §8.4's health block: type http, path /health, startup timeout) behind
// an injectable seam. The ADAPTER never dials on its own: the probe
// runs only when a caller supplies a HealthProber, and the endpoint
// URL is bound by the HOST BRIDGE path (the launcher owns endpoint
// assignment) — a probe without a bound endpoint is a NAMED refusal,
// never a guess.
package infer_ssd

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// HealthResult is one probe verdict (the honest observation: a
// timestamp, a boolean, and the detail that produced it — never a
// bare bool).
type HealthResult struct {
	Healthy   bool      `json:"healthy"`
	Detail    string    `json:"detail"`
	CheckedAt time.Time `json:"checked_at"`
}

// HealthProber is the injectable health-check seam (the airt.Prober
// discipline: nil in production wiring means the caller has not bound a
// prober, and Probe refuses BY NAME; tests inject deterministic
// fakes). The production implementation is HTTPHealthProber below.
type HealthProber interface {
	// CheckHealth probes one approved runtime profile's bound endpoint.
	// The context bounds the probe; a cancelled context is a probe
	// failure with the context error in the detail.
	CheckHealth(ctx context.Context, target ApprovedRuntimeProfile) HealthResult
}

// HTTPHealthProber is the default HealthProber: an HTTP GET of the
// profile's health path against the endpoint the Host Bridge bound
// (200 = healthy; anything else — including transport errors, redirects
// and cancellations — is unhealthy with the named detail). The client
// FOLLOWS NO REDIRECTS (the airt transport discipline: a 3xx on a
// health endpoint is a misconfigured or compromised tier answering
// from somewhere else — it is the failure it is, never followed).
type HTTPHealthProber struct {
	// client — the injectable transport (nil → a default no-redirect
	// client constructed once; tests inject deterministic clients).
	client *http.Client
}

// NewHTTPHealthProber constructs the default prober. The client's
// timeouts are per-probe; the CALLER's context remains the outer bound.
func NewHTTPHealthProber() *HTTPHealthProber {
	return &HTTPHealthProber{}
}

// CheckHealth implements HealthProber (see the type's contract).
func (h *HTTPHealthProber) CheckHealth(ctx context.Context, target ApprovedRuntimeProfile) HealthResult {
	checkedAt := time.Now().UTC()
	if target.HealthEndpointURL == "" {
		return HealthResult{Healthy: false, Detail: "no health endpoint bound — the Host Bridge path owns endpoint assignment (roadmap §8.3: the adapter returns a validated profile, it never launches or guesses endpoints)", CheckedAt: checkedAt}
	}
	client := h.client
	if client == nil {
		client = &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // no redirects — a 3xx is the failure it is
			},
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.HealthEndpointURL+target.HealthPath, nil)
	if err != nil {
		return HealthResult{Healthy: false, Detail: fmt.Sprintf("health request construction failed: %v", err), CheckedAt: checkedAt}
	}
	resp, err := client.Do(req)
	if err != nil {
		return HealthResult{Healthy: false, Detail: fmt.Sprintf("health probe failed: %v", err), CheckedAt: checkedAt}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HealthResult{Healthy: false, Detail: fmt.Sprintf("health endpoint answered HTTP %d — a health endpoint answers 200 or the backend is unhealthy, never a redirect or an error page", resp.StatusCode), CheckedAt: checkedAt}
	}
	return HealthResult{Healthy: true, Detail: fmt.Sprintf("health endpoint %s answered 200", target.HealthEndpointURL+target.HealthPath), CheckedAt: checkedAt}
}
