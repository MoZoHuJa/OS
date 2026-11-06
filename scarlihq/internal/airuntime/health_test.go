package airuntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

var probeT0 = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func TestHealthEnumeration(t *testing.T) {
	for _, h := range []HealthState{HealthUnknown, HealthHealthy, HealthDegraded, HealthUnavailable} {
		if !h.IsValid() {
			t.Fatalf("health state %s must be part of the closed enumeration", h)
		}
	}
	// There is deliberately no "starting" state — health is what the LAST
	// probe saw.
	for _, h := range []HealthState{"starting", "STARTING", "healthy ", "error", ""} {
		if h.IsValid() {
			t.Fatalf("health state %q must NOT be valid (closed enumeration, no starting, no error)", h)
		}
	}
}

func TestHealthServesMatrix(t *testing.T) {
	cases := []struct {
		state  HealthState
		serves bool
	}{
		{HealthHealthy, true},
		{HealthDegraded, true}, // a degraded tier serves honestly, cited in the plan
		{HealthUnknown, false}, // UNKNOWN never serves — fail-closed
		{HealthUnavailable, false},
	}
	for _, c := range cases {
		if got := c.state.Serves(); got != c.serves {
			t.Fatalf("Serves(%s) = %v, want %v", c.state, got, c.serves)
		}
	}
}

func TestObservationValidation(t *testing.T) {
	valid := Observation{Provider: ProviderSGLang, State: HealthHealthy, At: probeT0}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}
	for _, o := range []Observation{
		{Provider: "openai", State: HealthHealthy, At: probeT0},                         // cloud provider
		{Provider: ProviderSGLang, State: HealthHealthy, At: probeT0.Add(-time.Second)}, // any valid time is fine
	} {
		if o.Provider == "openai" && o.Validate() == nil {
			t.Fatal("observation with cloud provider must be rejected")
		}
	}
	if err := (Observation{Provider: ProviderSGLang, State: "starting", At: probeT0}).Validate(); err == nil {
		t.Fatal("observation with unenumerated state must be rejected")
	}
	if err := (Observation{Provider: ProviderSGLang, State: HealthHealthy}).Validate(); err == nil {
		t.Fatal("observation without timestamp must be rejected (monotonicity needs time)")
	}
}

func TestObserveAndSnapshot(t *testing.T) {
	table := NewHealthTable()
	// A never-observed provider is UNKNOWN — the fail-closed default.
	if got := table.Snapshot(ProviderSGLang); got.State != HealthUnknown {
		t.Fatalf("never-observed provider snapshots to %s, want UNKNOWN", got.State)
	}
	if table.Serves(ProviderSGLang) {
		t.Fatal("never observed must never serve")
	}
	if err := table.Observe(Observation{Provider: ProviderSGLang, State: HealthHealthy, At: probeT0}); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if got := table.Snapshot(ProviderSGLang); got.State != HealthHealthy || !got.At.Equal(probeT0) {
		t.Fatalf("snapshot after observe = %+v", got)
	}
	if !table.Serves(ProviderSGLang) {
		t.Fatal("HEALTHY serves")
	}
	// Latest per provider: a newer observation replaces an older one.
	newer := probeT0.Add(time.Minute)
	if err := table.Observe(Observation{Provider: ProviderSGLang, State: HealthUnavailable, At: newer}); err != nil {
		t.Fatalf("newer observe: %v", err)
	}
	if table.Serves(ProviderSGLang) {
		t.Fatal("latest observation UNAVAILABLE must not serve")
	}
	// A different provider stays UNKNOWN (latest-PER-PROVIDER).
	if table.Serves(ProviderOllama) {
		t.Fatal("other providers stay UNKNOWN (fail-closed)")
	}
}

func TestMonotoneObservationTime(t *testing.T) {
	table := NewHealthTable()
	t1 := probeT0
	t2 := probeT0.Add(time.Minute)
	if err := table.Observe(Observation{Provider: ProviderVLLM, State: HealthHealthy, At: t2}); err != nil {
		t.Fatalf("observe at t2: %v", err)
	}
	// A late-arriving older probe is rejected BY NAME.
	err := table.Observe(Observation{Provider: ProviderVLLM, State: HealthDegraded, At: t1})
	if err == nil {
		t.Fatal("a stale (older) probe must be rejected — it must not overwrite the newer observation")
	}
	if !strings.Contains(err.Error(), "stale probe rejected by name") {
		t.Fatalf("the rejection must name the rule, got: %v", err)
	}
	if !strings.Contains(err.Error(), "vllm") {
		t.Fatalf("the rejection must name the provider, got: %v", err)
	}
	// The newer observation survived the rejected probe.
	if got := table.Snapshot(ProviderVLLM); got.State != HealthHealthy {
		t.Fatalf("stale probe overwrote newer state: %s", got.State)
	}
	// Same-time re-observation is allowed (the later call wins at equal times).
	if err := table.Observe(Observation{Provider: ProviderVLLM, State: HealthDegraded, At: t2}); err != nil {
		t.Fatalf("same-time re-observation must be allowed: %v", err)
	}
	if got := table.Snapshot(ProviderVLLM); got.State != HealthDegraded {
		t.Fatalf("same-time re-observation must win: got %s", got.State)
	}
}

func TestSnapshotDeterminism(t *testing.T) {
	table := NewHealthTable()
	_ = table.Observe(Observation{Provider: ProviderSGLang, State: HealthDegraded, At: probeT0})
	_ = table.Observe(Observation{Provider: ProviderBeeLlama, State: HealthHealthy, At: probeT0})
	first := table.Snapshot(ProviderSGLang)
	for i := 0; i < 20; i++ {
		again := table.Snapshot(ProviderSGLang)
		if again.State != first.State || !again.At.Equal(first.At) || again.Provider != first.Provider {
			t.Fatalf("snapshot drifted at repetition %d: %+v vs %+v", i, again, first)
		}
	}
}

func TestHealthTableNeverExpires(t *testing.T) {
	// The table never expires entries on its own — freshness is the CF-2
	// prober's job (stated, not hidden): an old HEALTHY stays HEALTHY.
	table := NewHealthTable()
	old := probeT0.Add(-24 * time.Hour)
	if err := table.Observe(Observation{Provider: ProviderSGLang, State: HealthHealthy, At: old}); err != nil {
		t.Fatalf("observe old: %v", err)
	}
	if !table.Serves(ProviderSGLang) {
		t.Fatal("the table must not expire entries on its own (the CF-2 prober owns freshness)")
	}
}

// ---------------------------------------------------------------------------
// KPK-19.6.4-P2-02 (v19.6.5): the ATOMIC, context-aware batch commit.
// Every test here is a REGRESSION for the independent v19.6.4 audit
// finding — ProbeAllAndObserve used to write observations one-by-one
// (table.Observe in a loop), so a cancellation arriving between probe
// completion and the first write — or mid-write-loop — could land a
// partial round, violating the "cancelled round records nothing" contract.
// ---------------------------------------------------------------------------

// TestObserveBatchCommitsWholeRound: a valid batch lands completely —
// every provider's state AND timestamp, in one atomic commit.
func TestObserveBatchCommitsWholeRound(t *testing.T) {
	table := NewHealthTable()
	t1 := probeT0
	batch := []Observation{
		{Provider: ProviderSGLang, State: HealthHealthy, At: t1},
		{Provider: ProviderVLLM, State: HealthDegraded, At: t1},
		{Provider: ProviderBeeLlama, State: HealthHealthy, At: t1},
		{Provider: ProviderOllama, State: HealthUnavailable, At: t1},
	}
	if err := table.ObserveBatch(context.Background(), batch); err != nil {
		t.Fatalf("a valid round must commit whole: %v", err)
	}
	want := []struct {
		p    Provider
		s    HealthState
		svcs bool
	}{
		{ProviderSGLang, HealthHealthy, true},
		{ProviderVLLM, HealthDegraded, true},
		{ProviderBeeLlama, HealthHealthy, true},
		{ProviderOllama, HealthUnavailable, false},
	}
	for _, w := range want {
		if got := table.Snapshot(w.p); got.State != w.s || !got.At.Equal(t1) {
			t.Fatalf("%s after the batch = %s @ %v, want %s @ %v", w.p, got.State, got.At, w.s, t1)
		}
		if table.Serves(w.p) != w.svcs {
			t.Fatalf("Serves(%s) = %v, want %v", w.p, table.Serves(w.p), w.svcs)
		}
	}
}

// TestObserveBatchAtomicOnInvalidMember: a batch with ONE invalid member
// (bad shape) is rejected WHOLE — nothing is applied, the table keeps its
// previous observations and timestamps (a partially applied round is
// never visible).
func TestObserveBatchAtomicOnInvalidMember(t *testing.T) {
	table := NewHealthTable()
	seed := probeT0
	if err := table.Observe(Observation{Provider: ProviderSGLang, State: HealthHealthy, At: seed}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	later := seed.Add(time.Minute)
	bad := []Observation{
		{Provider: ProviderVLLM, State: HealthHealthy, At: later},
		{Provider: ProviderSGLang, State: "starting", At: later}, // unenumerated state
		{Provider: ProviderBeeLlama, State: HealthHealthy, At: later},
	}
	if err := table.ObserveBatch(context.Background(), bad); err == nil {
		t.Fatal("a batch with an invalid member must be rejected")
	}
	// NOTHING applied: the seeded observation stands, the others stay UNKNOWN.
	if got := table.Snapshot(ProviderSGLang); got.State != HealthHealthy || !got.At.Equal(seed) {
		t.Fatalf("the seeded observation must survive a rejected batch, got %s @ %v", got.State, got.At)
	}
	for _, p := range []Provider{ProviderVLLM, ProviderBeeLlama, ProviderOllama} {
		if got := table.Snapshot(p); got.State != HealthUnknown {
			t.Fatalf("%s must stay UNKNOWN after a rejected batch (atomicity — no partial round), got %s", p, got.State)
		}
	}
}

// TestObserveBatchAtomicOnStaleMember: a batch with ONE stale member
// (older than the table's current observation for that provider) is
// rejected WHOLE — the monotone discipline is all-or-nothing across the
// batch, exactly the single-Observe rule carried over.
func TestObserveBatchAtomicOnStaleMember(t *testing.T) {
	table := NewHealthTable()
	now := probeT0
	if err := table.Observe(Observation{Provider: ProviderSGLang, State: HealthHealthy, At: now}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	older := now.Add(-time.Minute)
	stale := []Observation{
		{Provider: ProviderVLLM, State: HealthHealthy, At: older},       // fresh for vllm…
		{Provider: ProviderSGLang, State: HealthUnavailable, At: older}, // …but stale for sglang
	}
	err := table.ObserveBatch(context.Background(), stale)
	if err == nil {
		t.Fatal("a batch with a stale member must be rejected whole (monotone, atomic)")
	}
	if !strings.Contains(err.Error(), "stale probe rejected by name") {
		t.Fatalf("the rejection must name the monotone rule, got: %v", err)
	}
	if got := table.Snapshot(ProviderSGLang); got.State != HealthHealthy || !got.At.Equal(now) {
		t.Fatalf("the newer observation must survive the stale batch, got %s @ %v", got.State, got.At)
	}
	if got := table.Snapshot(ProviderVLLM); got.State != HealthUnknown {
		t.Fatalf("the fresh-but-batched member must NOT land when a sibling is stale (atomicity), got %s", got.State)
	}
}

// TestObserveBatchCancelledAtCommitBoundary: the audit's exact acceptance
// — cancel the caller's context BETWEEN probe completion and the commit:
// the batch records NOTHING, the error is context-identifiable, and every
// previous state and timestamp is preserved. The counterweight: a LIVE
// context with the same batch commits normally.
func TestObserveBatchCancelledAtCommitBoundary(t *testing.T) {
	table := NewHealthTable()
	seed := probeT0
	seeded := []Observation{
		{Provider: ProviderSGLang, State: HealthHealthy, At: seed},
		{Provider: ProviderVLLM, State: HealthHealthy, At: seed},
		{Provider: ProviderBeeLlama, State: HealthHealthy, At: seed},
		{Provider: ProviderOllama, State: HealthHealthy, At: seed},
	}
	if err := table.ObserveBatch(context.Background(), seeded); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	later := seed.Add(time.Minute)
	cancelled := []Observation{
		{Provider: ProviderSGLang, State: HealthUnavailable, At: later},
		{Provider: ProviderVLLM, State: HealthUnavailable, At: later},
		{Provider: ProviderBeeLlama, State: HealthUnavailable, At: later},
		{Provider: ProviderOllama, State: HealthUnavailable, At: later},
	}
	ctx, cancel := context.WithCancel(context.Background())
	// The caller walks away exactly between the probes and the commit.
	cancel()
	err := table.ObserveBatch(ctx, cancelled)
	if err == nil {
		t.Fatal("a cancelled context at the commit boundary must reject the batch (KPK-19.6.4-P2-02)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the rejection must stay context-identifiable (errors.Is context.Canceled), got: %v", err)
	}
	if !strings.Contains(err.Error(), "commit boundary") {
		t.Fatalf("the rejection must name the commit boundary, got: %v", err)
	}
	// Every previous state AND timestamp preserved — zero writes.
	for _, o := range seeded {
		if got := table.Snapshot(o.Provider); got.State != HealthHealthy || !got.At.Equal(seed) {
			t.Fatalf("KPK-19.6.4-P2-02 regression: %s moved (%s @ %v) — a cancelled commit records NOTHING", o.Provider, got.State, got.At)
		}
	}
	// The counterweight: the SAME batch against a LIVE context commits.
	live := append([]Observation(nil), cancelled...)
	if err := table.ObserveBatch(context.Background(), live); err != nil {
		t.Fatalf("the live counterweight must commit: %v", err)
	}
	for _, o := range live {
		if got := table.Snapshot(o.Provider); got.State != HealthUnavailable || !got.At.Equal(later) {
			t.Fatalf("the live counterweight must land whole, %s = %s @ %v", o.Provider, got.State, got.At)
		}
	}
}

// TestObserveBatchDeadlineAtCommitBoundary: the same boundary under a
// deadline that expired after the probes — DeadlineExceeded, equally
// identifiable, equally zero-write.
func TestObserveBatchDeadlineAtCommitBoundary(t *testing.T) {
	table := NewHealthTable()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	time.Sleep(20 * time.Millisecond) // the deadline lapses AFTER the (simulated) probes
	batch := []Observation{{Provider: ProviderSGLang, State: HealthHealthy, At: probeT0}}
	err := table.ObserveBatch(ctx, batch)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an expired deadline at the commit boundary must reject the batch identifiably, got: %v", err)
	}
	if got := table.Snapshot(ProviderSGLang); got.State != HealthUnknown {
		t.Fatalf("nothing may land from an expired-deadline commit, got %s", got.State)
	}
}

// TestObserveBatchCancelledWhileWaitingForLock: the v19.6.6 regression
// proof (KPK-19.6.5-P2-02) — the context is checked UNDER THE LOCK. The
// v19.6.5 defect: ctx.Err() ran BEFORE t.mu.Lock(), so a cancellation
// arriving while ObserveBatch was BLOCKED on a contended lock was never
// re-checked and the batch committed anyway. This test reconstructs the
// EXACT audited window deterministically: another goroutine HOLDS the
// table mutex, ObserveBatch starts and blocks on the lock, the caller
// cancels (and separately: a deadline lapses), the mutex is released —
// and the batch must record NOTHING, the error must stay
// context-identifiable. The live-context counterweight commits the same
// batch through the same contended window.
func TestObserveBatchCancelledWhileWaitingForLock(t *testing.T) {
	table := NewHealthTable()
	seeded := []Observation{{Provider: ProviderSGLang, State: HealthHealthy, At: probeT0}}
	if err := table.ObserveBatch(context.Background(), seeded); err != nil {
		t.Fatalf("seed: %v", err)
	}
	later := probeT0.Add(time.Minute)
	batch := []Observation{{Provider: ProviderSGLang, State: HealthUnavailable, At: later}}

	// Hold the table's mutex — ObserveBatch will block on the lock after
	// passing its (now lock-side) commit-boundary position check.
	table.mu.Lock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- table.ObserveBatch(ctx, batch) }()

	// Wait until the batch call is BLOCKED on the lock (it cannot have
	// returned: the mutex is held here). A short poll proves liveness of
	// the goroutine without timing dependence on its completion.
	blockDeadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("ObserveBatch returned (%v) while the table mutex was still held — it cannot have committed", err)
		default:
		}
		if time.Now().After(blockDeadline) {
			t.Fatal("ObserveBatch never reached the lock wait — goroutine appears stalled")
		}
		time.Sleep(2 * time.Millisecond)
		break // one liveness sample is enough: it has not returned yet
	}
	time.Sleep(20 * time.Millisecond) // let the goroutine park on t.mu.Lock()

	// The caller walks away WHILE ObserveBatch is blocked on the lock.
	cancel()
	// Release the mutex — the lock hand-off happens with a done context.
	table.mu.Unlock()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancellation while waiting for the health-table lock must reject the batch (KPK-19.6.5-P2-02: the check lives UNDER the lock)")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the rejection must stay context-identifiable, got: %v", err)
		}
		if !strings.Contains(err.Error(), "commit boundary") {
			t.Fatalf("the rejection must still name the commit boundary, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ObserveBatch did not return after the mutex was released")
	}
	// Zero writes: the seeded state AND timestamp stand untouched.
	if got := table.Snapshot(ProviderSGLang); got.State != HealthHealthy || !got.At.Equal(probeT0) {
		t.Fatalf("KPK-19.6.5-P2-02 regression: the table moved (%s @ %v) — a cancellation in the lock-wait window records NOTHING", got.State, got.At)
	}

	// The counterweight: the SAME batch, the SAME contended window, a
	// LIVE context — the whole batch commits normally.
	table.mu.Lock()
	liveDone := make(chan error, 1)
	go func() { liveDone <- table.ObserveBatch(context.Background(), batch) }()
	time.Sleep(20 * time.Millisecond) // parked on the lock again
	table.mu.Unlock()
	if err := <-liveDone; err != nil {
		t.Fatalf("the live-context counterweight through the contended window must commit: %v", err)
	}
	if got := table.Snapshot(ProviderSGLang); got.State != HealthUnavailable || !got.At.Equal(later) {
		t.Fatalf("the live counterweight must land whole, got %s @ %v", got.State, got.At)
	}
}

// TestObserveBatchDeadlineWhileWaitingForLock: the same waiting-window
// discipline under a deadline — the deadline lapses while ObserveBatch is
// blocked on the contended lock; the hand-off must refuse the batch
// identifiably with zero writes (the audit's acceptance list: cancellation
// AND deadline expiry while waiting).
func TestObserveBatchDeadlineWhileWaitingForLock(t *testing.T) {
	table := NewHealthTable()
	seeded := []Observation{{Provider: ProviderVLLM, State: HealthDegraded, At: probeT0}}
	if err := table.ObserveBatch(context.Background(), seeded); err != nil {
		t.Fatalf("seed: %v", err)
	}
	later := probeT0.Add(time.Minute)
	batch := []Observation{{Provider: ProviderVLLM, State: HealthUnavailable, At: later}}

	table.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- table.ObserveBatch(ctx, batch) }()
	time.Sleep(25 * time.Millisecond) // the deadline lapses WHILE blocked on the lock
	table.mu.Unlock()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a deadline expired while waiting for the lock must reject the batch identifiably, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ObserveBatch did not return after the mutex was released")
	}
	if got := table.Snapshot(ProviderVLLM); got.State != HealthDegraded || !got.At.Equal(probeT0) {
		t.Fatalf("an expired-deadline waiting window records NOTHING, got %s @ %v", got.State, got.At)
	}
}

// TestObserveBatchConcurrentRoundsMonotone: two observation rounds racing
// — the newer round wins per provider, the older round is discarded WHOLE
// (never partially applied over the newer one), and the table never shows
// a torn state: every snapshot is either all-old or all-new per provider.
func TestObserveBatchConcurrentRoundsMonotone(t *testing.T) {
	table := NewHealthTable()
	olderAt := probeT0
	newerAt := olderAt.Add(2 * time.Second)
	olderRound := []Observation{
		{Provider: ProviderSGLang, State: HealthDegraded, At: olderAt},
		{Provider: ProviderVLLM, State: HealthDegraded, At: olderAt},
		{Provider: ProviderBeeLlama, State: HealthDegraded, At: olderAt},
		{Provider: ProviderOllama, State: HealthDegraded, At: olderAt},
	}
	newerRound := []Observation{
		{Provider: ProviderSGLang, State: HealthHealthy, At: newerAt},
		{Provider: ProviderVLLM, State: HealthHealthy, At: newerAt},
		{Provider: ProviderBeeLlama, State: HealthHealthy, At: newerAt},
		{Provider: ProviderOllama, State: HealthHealthy, At: newerAt},
	}
	var wg sync.WaitGroup
	rounds := [][]Observation{olderRound, newerRound, olderRound, newerRound}
	for _, round := range rounds {
		wg.Add(1)
		go func(r []Observation) {
			defer wg.Done()
			_ = table.ObserveBatch(context.Background(), r) //nolint:errcheck — a stale racing round legitimately rejects
		}(round)
	}
	wg.Wait()
	// The NEWER round's evidence stands for every provider — an older
	// racing round cannot partially overwrite it (atomic monotone).
	for _, o := range newerRound {
		if got := table.Snapshot(o.Provider); got.State != HealthHealthy || !got.At.Equal(newerAt) {
			t.Fatalf("concurrent rounds: %s ended as %s @ %v — the newer round must win whole, never torn", o.Provider, got.State, got.At)
		}
	}
}

// TestObserveBatchEdges: the honest edges — a nil table and a nil context
// are named errors; an EMPTY batch is a no-op success (nothing to commit,
// nothing to corrupt).
func TestObserveBatchEdges(t *testing.T) {
	var nilTable *HealthTable
	if err := nilTable.ObserveBatch(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "nil health table") {
		t.Fatalf("a nil table must be a named error, got: %v", err)
	}
	table := NewHealthTable()
	if err := table.ObserveBatch(nil, []Observation{{Provider: ProviderSGLang, State: HealthHealthy, At: probeT0}}); err == nil || !strings.Contains(err.Error(), "nil context") {
		t.Fatalf("a nil context must be a named error (the commit boundary needs the caller's context), got: %v", err)
	}
	if err := table.ObserveBatch(context.Background(), nil); err != nil {
		t.Fatalf("an empty batch is a no-op success, got: %v", err)
	}
	if got := table.Snapshot(ProviderSGLang); got.State != HealthUnknown {
		t.Fatalf("an empty batch must write nothing, got %s", got.State)
	}
}
