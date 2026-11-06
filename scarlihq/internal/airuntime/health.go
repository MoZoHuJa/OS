package airuntime

import (
	"context"
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

// ObserveBatch commits a WHOLE probe round atomically (v19.6.5,
// KPK-19.6.4-P2-02; v19.6.6, KPK-19.6.5-P2-02): every observation is
// validated, the batch is monotonicity-checked against the current state,
// and the writes are applied under ONE lock — either the entire round
// lands or none of it does; a partially applied round is never visible to
// a concurrent reader. The commit boundary is CONTEXT-AWARE and lives
// UNDER THE LOCK (v19.6.6, KPK-19.6.5-P2-02: the v19.6.5 check ran
// BEFORE mutex acquisition — a cancellation arriving while ObserveBatch
// was BLOCKED waiting for a contended lock was never re-checked, and the
// batch committed anyway; now the caller's context is checked immediately
// AFTER the lock is acquired, so a cancellation in the waiting window
// records NOTHING), so a cancellation that arrives after the probes
// completed but before the commit records NOTHING — the context error
// returns errors.Is-identifiable and the table's previous observations
// and timestamps stand.
//
// The linearization rule (one sentence, deliberate): the successful
// context check UNDER the lock is the commit acceptance point —
// cancellation BEFORE that point means ZERO writes; cancellation AFTER it
// does not retroactively undo a completed observation round.
//
// A batch containing an invalid observation (bad shape) or a STALE one
// (older than what the table already holds for that provider) is
// rejected WHOLE — atomicity is all-or-nothing, the single-observation
// Observe discipline carried over unchanged. An empty batch is a no-op
// success (nothing to commit, nothing to corrupt).
func (t *HealthTable) ObserveBatch(ctx context.Context, observations []Observation) error {
	if t == nil {
		return fmt.Errorf("airuntime: nil health table")
	}
	if ctx == nil {
		return fmt.Errorf("airuntime: nil context — the batch commit boundary needs the caller's context (KPK-19.6.4-P2-02)")
	}
	// Validate every observation BEFORE any state change — a shape
	// error rejects the batch whole (no partial application).
	for _, o := range observations {
		if err := o.Validate(); err != nil {
			return err
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// The COMMIT BOUNDARY (KPK-19.6.4-P2-02 + KPK-19.6.5-P2-02): the
	// context is checked UNDER THE LOCK, immediately after acquisition —
	// a done context here means the caller walked away (between probe
	// completion and the commit, OR while this call was blocked waiting
	// for a contended lock): zero writes, the context error returns
	// identifiably, the last-known-good observations stand. A cancelled
	// commit is not evidence about the providers. THIS check is the
	// linearization point: a cancellation racing the lock hand-off can no
	// longer slip a write through the waiting window (the v19.6.5 defect —
	// the check lived before the lock and the waiting gap was unchecked).
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("airuntime: observation batch rejected at the commit boundary (under the health-table lock) — the caller's context is done (%w); a cancelled commit records nothing, the previous observations stand (KPK-19.6.4-P2-02, KPK-19.6.5-P2-02)", err)
	}
	// Monotonicity for the WHOLE batch under the lock: any stale member
	// rejects the batch whole — a concurrent NEWER round that committed
	// first makes this round stale, and it is discarded completely,
	// never partially applied over the newer observations.
	for _, o := range observations {
		if prev, ok := t.latest[o.Provider]; ok && prev.At.After(o.At) {
			return fmt.Errorf("airuntime: stale probe rejected by name: %s already observed at %s, refusing older batch probe at %s (observation time is monotone, batches are atomic)", o.Provider, prev.At.UTC().Format(time.RFC3339Nano), o.At.UTC().Format(time.RFC3339Nano))
		}
	}
	for _, o := range observations {
		t.latest[o.Provider] = o
	}
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
