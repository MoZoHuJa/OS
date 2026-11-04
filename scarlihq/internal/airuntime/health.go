package airuntime

import (
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Observed health (fail-closed: UNKNOWN never serves)
// ---------------------------------------------------------------------------

// HealthState is a closed enumeration of what a probe observed. There is
// deliberately no "starting" state — health is what the LAST probe saw.
type HealthState string

const (
	HealthUnknown     HealthState = "UNKNOWN"
	HealthHealthy     HealthState = "HEALTHY"
	HealthDegraded    HealthState = "DEGRADED"
	HealthUnavailable HealthState = "UNAVAILABLE"
)

// IsValid reports whether h is part of the closed enumeration.
func (h HealthState) IsValid() bool {
	switch h {
	case HealthUnknown, HealthHealthy, HealthDegraded, HealthUnavailable:
		return true
	}
	return false
}

// Serves reports whether a provider in this state may be routed to.
// HEALTHY and DEGRADED serve (a degraded tier serves honestly, cited in
// the plan); UNKNOWN and UNAVAILABLE do not — UNKNOWN never serves:
// trust is earned by observation, not assumed by default (fail-closed).
func (h HealthState) Serves() bool {
	return h == HealthHealthy || h == HealthDegraded
}

// Observation is one probe result: the state a provider was seen in, and
// when. The timestamp is mandatory — a probe without a time cannot be
// ordered, and monotonicity is the table's core discipline.
type Observation struct {
	Provider Provider
	State    HealthState
	At       time.Time
}

// Validate checks the observation shape: closed enumeration, provider
// membership in the local stack, non-zero timestamp.
func (o Observation) Validate() error {
	if err := o.Provider.Validate(); err != nil {
		return err
	}
	if !o.State.IsValid() {
		return fmt.Errorf("airuntime: health state %q is not in the closed enumeration (UNKNOWN/HEALTHY/DEGRADED/UNAVAILABLE)", o.State)
	}
	if o.At.IsZero() {
		return fmt.Errorf("airuntime: observation for %s has no timestamp (observation time is monotone — it is required)", o.Provider)
	}
	return nil
}

// HealthTable holds the LATEST observation per provider. The table never
// expires entries on its own — freshness is the CF-2 prober's job (stated,
// not hidden): an old HEALTHY stays HEALTHY until a newer probe says
// otherwise.
type HealthTable struct {
	mu     sync.Mutex
	latest map[Provider]Observation
}

// NewHealthTable constructs an empty table. Every provider starts UNKNOWN
// (fail-closed) — the table records what was OBSERVED, nothing more.
func NewHealthTable() *HealthTable {
	return &HealthTable{latest: make(map[Provider]Observation)}
}

// Observe records a probe result. Observation time is MONOTONIC: a probe
// older than the recorded one is rejected BY NAME (a late-arriving older
// probe must not overwrite a newer one); re-observing at the same time is
// allowed (the later call wins at equal times).
func (t *HealthTable) Observe(o Observation) error {
	if t == nil {
		return fmt.Errorf("airuntime: nil health table")
	}
	if err := o.Validate(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.latest[o.Provider]; ok && prev.At.After(o.At) {
		return fmt.Errorf("airuntime: stale probe rejected by name: %s already observed at %s, refusing older probe at %s (observation time is monotone)", o.Provider, prev.At.UTC().Format(time.RFC3339Nano), o.At.UTC().Format(time.RFC3339Nano))
	}
	t.latest[o.Provider] = o
	return nil
}

// Snapshot returns the latest observation for p. A never-observed
// provider snapshots to UNKNOWN with a zero timestamp — the caller can
// distinguish "never observed" from "observed" by the time, and the state
// is fail-closed either way.
func (t *HealthTable) Snapshot(p Provider) Observation {
	if t == nil {
		return Observation{Provider: p, State: HealthUnknown}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if o, ok := t.latest[p]; ok {
		return o
	}
	return Observation{Provider: p, State: HealthUnknown}
}

// Serves consults the latest observation: a miss is UNKNOWN, and UNKNOWN
// never serves.
func (t *HealthTable) Serves(p Provider) bool {
	return t.Snapshot(p).State.Serves()
}
