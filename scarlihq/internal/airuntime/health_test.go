package airuntime

import (
	"strings"
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
