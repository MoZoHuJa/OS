package agentkernel

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// --- Store construction ---

func TestNewStoreValidation(t *testing.T) {
	if _, err := NewStore(0); err == nil {
		t.Fatal("maxSessions=0 must be rejected (unbounded store is a construction error)")
	}
	if _, err := NewStore(-1); err == nil {
		t.Fatal("negative maxSessions must be rejected")
	}
	if _, err := NewStore(1); err != nil {
		t.Fatalf("valid maxSessions rejected: %v", err)
	}
}

// --- Create ---

func TestStoreCreate(t *testing.T) {
	store, _ := NewStore(8)
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	caps, _ := NewCapabilitySet(CapabilityStatusRead, CapabilityComputePlan)

	rec, err := store.Create(agent, caps, testNow)
	if err != nil {
		t.Fatalf("valid create rejected: %v", err)
	}
	if !strings.HasPrefix(rec.Session.ID, "aks-") {
		t.Fatalf("session ID must be aks-prefixed, got %q", rec.Session.ID)
	}
	if rec.Session.State != SessionCreated {
		t.Fatalf("new session must be CREATED, got %s", rec.Session.State)
	}
	if rec.Session.AgentID != agent.ID {
		t.Fatalf("session agent mismatch: %s", rec.Session.AgentID)
	}
	if !rec.Caps.Has(CapabilityStatusRead) {
		t.Fatal("grants must be carried on the record")
	}
	// Distinct IDs.
	rec2, _ := store.Create(agent, caps, testNow)
	if rec2.Session.ID == rec.Session.ID {
		t.Fatal("session IDs must be unique")
	}
}

func TestStoreCreateRejections(t *testing.T) {
	store, _ := NewStore(8)
	caps, _ := NewCapabilitySet(CapabilityStatusRead)
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)

	// nil capability set: nothing granted implicitly — be explicit.
	if _, err := store.Create(agent, nil, testNow); err == nil {
		t.Fatal("nil capability set must be rejected at creation")
	}
	// empty capability set: same rule.
	if _, err := store.Create(agent, CapabilitySet{}, testNow); err == nil {
		t.Fatal("empty capability set must be rejected at creation")
	}
	// invalid agent (defense in depth — re-validated by the store).
	if _, err := store.Create(AgentIdentity{ID: "bad"}, caps, testNow); err == nil {
		t.Fatal("invalid agent identity must be re-validated by the store")
	}
	// zero timestamp.
	if _, err := store.Create(agent, caps, time.Time{}); err == nil {
		t.Fatal("zero creation time must be rejected")
	}
}

func TestStoreCreateRNGFailClosed(t *testing.T) {
	// v18.7.4-P0 precedent (WS tickets): RNG failure must fail-closed with a
	// distinct error — a predictable all-zeros ID is never issued.
	store, _ := NewStore(8)
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	caps, _ := NewCapabilitySet(CapabilityStatusRead)

	orig := rngSessionID
	rngSessionID = func() (string, error) { return "", ErrRNG }
	defer func() { rngSessionID = orig }()

	if _, err := store.Create(agent, caps, testNow); !errors.Is(err, ErrRNG) {
		t.Fatalf("RNG failure must fail-closed with ErrRNG, got %v", err)
	}
	if store.Count() != 0 {
		t.Fatal("failed create must not leave a session behind")
	}
}

func TestStoreCapacity(t *testing.T) {
	store, _ := NewStore(2)
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	caps, _ := NewCapabilitySet(CapabilityStatusRead)

	if _, err := store.Create(agent, caps, testNow); err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	if _, err := store.Create(agent, caps, testNow); err != nil {
		t.Fatalf("second create failed: %v", err)
	}
	_, err := store.Create(agent, caps, testNow)
	if !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("third create must hit ErrSessionLimit, got %v", err)
	}
	// Closing one session frees a slot (bounded, not leaky).
	if _, err := store.Transition(mustFirstID(store), SessionClosed, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	// CLOSED records stay in the store (audit/observability) — the cap still
	// counts them. This is the honest contract: the cap bounds records, not
	// "active" sessions; document it in the API.
	if _, err := store.Create(agent, caps, testNow.Add(2*time.Minute)); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("cap must count closed records too, got %v", err)
	}
}

func mustFirstID(s *Store) string {
	list := s.List()
	return list[0].Session.ID
}

// --- Get / List ---

func TestStoreGetListCopies(t *testing.T) {
	store, _ := NewStore(8)
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	caps, _ := NewCapabilitySet(CapabilityStatusRead)

	created, _ := store.Create(agent, caps, testNow)
	rec, ok := store.Get(created.Session.ID)
	if !ok {
		t.Fatal("created session must be retrievable")
	}
	// Mutating the returned record must NOT touch the store (deep copy).
	rec.Caps[CapabilityComputeReserve] = true
	if storeRec, _ := store.Get(created.Session.ID); storeRec.Caps.Has(CapabilityComputeReserve) {
		t.Fatal("returned records must be copies — grant mutation leaked into the store")
	}

	list := store.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
}

func TestStoreListDeterministicOrder(t *testing.T) {
	store, _ := NewStore(16)
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	caps, _ := NewCapabilitySet(CapabilityStatusRead)
	for i := 0; i < 5; i++ {
		if _, err := store.Create(agent, caps, testNow.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("create %d failed: %v", i, err)
		}
	}
	first := store.List()
	for i := 0; i < 20; i++ {
		again := store.List()
		for j := range first {
			if first[j].Session.ID != again[j].Session.ID {
				t.Fatalf("List order is not deterministic at index %d", j)
			}
		}
	}
}

// --- Transition ---

func TestStoreTransitions(t *testing.T) {
	store, _ := NewStore(8)
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	caps, _ := NewCapabilitySet(CapabilityStatusRead)
	rec, _ := store.Create(agent, caps, testNow)

	// CREATED -> SUSPENDED is illegal (never activated).
	if _, err := store.Transition(rec.Session.ID, SessionSuspended, testNow.Add(time.Minute)); err == nil {
		t.Fatal("CREATED -> SUSPENDED must be rejected")
	}
	// CREATED -> ACTIVE ok.
	if _, err := store.Transition(rec.Session.ID, SessionActive, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("CREATED -> ACTIVE must succeed: %v", err)
	}
	// Backwards clock rejected.
	if _, err := store.Transition(rec.Session.ID, SessionSuspended, testNow); err == nil {
		t.Fatal("backwards clock must be rejected")
	}
	// ACTIVE -> CLOSED, then terminal.
	if _, err := store.Transition(rec.Session.ID, SessionClosed, testNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("ACTIVE -> CLOSED must succeed: %v", err)
	}
	if _, err := store.Transition(rec.Session.ID, SessionActive, testNow.Add(3*time.Minute)); err == nil {
		t.Fatal("CLOSED is terminal")
	}
	// Unknown session.
	if _, err := store.Transition("aks-does-not-exist", SessionActive, testNow); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("unknown session must return ErrUnknownSession, got %v", err)
	}
}

// --- Evaluate ---

func TestStoreEvaluate(t *testing.T) {
	store, _ := NewStore(8)
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	caps, _ := NewCapabilitySet(CapabilityStatusRead)
	rec, _ := store.Create(agent, caps, testNow)
	policy := Policy{}

	// CREATED session cannot act.
	ev, d, err := store.Evaluate(rec.Session.ID, policy, CapabilityStatusRead, testNow)
	if err != nil || d.Allow {
		t.Fatalf("CREATED session must not act: %v %+v", err, d)
	}
	if ev.Decision != d || ev.SessionID != rec.Session.ID {
		t.Fatalf("audit event must carry the decision and session: %+v", ev)
	}

	// Activate, then allowed.
	if _, err := store.Transition(rec.Session.ID, SessionActive, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("activate failed: %v", err)
	}
	_, d, err = store.Evaluate(rec.Session.ID, policy, CapabilityStatusRead, testNow.Add(time.Minute))
	if err != nil || !d.Allow {
		t.Fatalf("ACTIVE + granted must allow: %v %+v", err, d)
	}

	// Not granted (session holds status.read only).
	_, d, err = store.Evaluate(rec.Session.ID, policy, CapabilityComputeReserve, testNow.Add(time.Minute))
	if err != nil || d.Allow {
		t.Fatalf("not-granted capability must deny: %v %+v", err, d)
	}
	if !strings.Contains(d.Reason, "rule/capability-not-granted") {
		t.Fatalf("denial must cite the rule, got %q", d.Reason)
	}

	// Admin-disabled capability.
	disabled := Policy{Disabled: map[Capability]bool{CapabilityStatusRead: true}}
	_, d, err = store.Evaluate(rec.Session.ID, disabled, CapabilityStatusRead, testNow.Add(time.Minute))
	if err != nil || d.Allow || !strings.Contains(d.Reason, "rule/capability-disabled") {
		t.Fatalf("disabled capability must deny with the rule: %v %+v", err, d)
	}

	// Unknown session.
	if _, _, err := store.Evaluate("aks-nope", policy, CapabilityStatusRead, testNow); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("unknown session must return ErrUnknownSession, got %v", err)
	}
}
