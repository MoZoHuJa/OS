package agentkernel

import (
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// --- Agent identity ---

func TestAgentIDValidation(t *testing.T) {
	valid := []AgentID{"coder.agent-one", "ops.backup", "a.1", "review.pr-checker"}
	for _, id := range valid {
		if err := id.Validate(); err != nil {
			t.Errorf("valid id %q rejected: %v", id, err)
		}
	}
	invalid := []AgentID{
		"", "nodot", ".leading", "trailing.", "UPPER.agent", "agent with space",
		"agent@host", "sh;rm -rf", AgentID(strings.Repeat("x", 65) + ".toolong"),
	}
	for _, id := range invalid {
		if err := id.Validate(); err == nil {
			t.Errorf("invalid id %q accepted", id)
		}
	}
}

func TestNewAgentIdentity(t *testing.T) {
	if _, err := NewAgentIdentity("coder.agent-one", "zmor", testNow); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	if _, err := NewAgentIdentity("coder.agent-one", "", testNow); err == nil {
		t.Fatal("ownerless agent must be rejected")
	}
	if _, err := NewAgentIdentity("coder.agent-one", "zmor", time.Time{}); err == nil {
		t.Fatal("zero creation time must be rejected")
	}
}

// --- Capability model ---

func TestCapabilityClosedEnumeration(t *testing.T) {
	for _, c := range AllCapabilities() {
		if !c.IsValid() {
			t.Errorf("AllCapabilities contains invalid entry %q", c)
		}
	}
	bad := Capability("shell.exec")
	if bad.IsValid() {
		t.Fatal("shell.exec must NEVER be a valid capability — no free shell (strategy p. 22)")
	}
	if _, err := NewCapabilitySet(CapabilityStatusRead); err != nil {
		t.Fatalf("valid set rejected: %v", err)
	}
	if _, err := NewCapabilitySet(Capability("made.up")); err == nil {
		t.Fatal("unknown capability must be rejected (closed enumeration)")
	}
	if _, err := NewCapabilitySet(CapabilityStatusRead, CapabilityStatusRead); err == nil {
		t.Fatal("duplicate capability must be rejected")
	}
}

// --- Session lifecycle ---

func TestSessionTransitions(t *testing.T) {
	s := &Session{ID: "s1", AgentID: "coder.agent-one", State: SessionCreated, CreatedAt: testNow, UpdatedAt: testNow}

	steps := []struct {
		to  SessionState
		err bool
	}{
		{SessionActive, false},    // CREATED → ACTIVE
		{SessionSuspended, false}, // ACTIVE → SUSPENDED
		{SessionActive, false},    // SUSPENDED → ACTIVE
		{SessionClosed, false},    // ACTIVE → CLOSED
		{SessionActive, true},     // CLOSED is terminal
		{SessionClosed, true},     // still terminal
	}
	for _, st := range steps {
		err := s.Transition(st.to, testNow.Add(time.Minute))
		if st.err && err == nil {
			t.Fatalf("transition to %s must fail", st.to)
		}
		if !st.err && err != nil {
			t.Fatalf("transition to %s must succeed: %v", st.to, err)
		}
	}
	if s.State != SessionClosed {
		t.Fatalf("expected CLOSED, got %s", s.State)
	}
}

func TestSessionIllegalTransitions(t *testing.T) {
	s := &Session{ID: "s1", AgentID: "coder.agent-one", State: SessionCreated, CreatedAt: testNow, UpdatedAt: testNow}
	// CREATED → SUSPENDED is illegal (never activated)
	if err := s.Transition(SessionSuspended, testNow.Add(time.Minute)); err == nil {
		t.Fatal("CREATED → SUSPENDED must be illegal")
	}
	// backwards clock
	if err := s.Transition(SessionActive, testNow.Add(-time.Minute)); err == nil {
		t.Fatal("timestamp before UpdatedAt must be rejected")
	}
	// unknown state
	bad := &Session{ID: "s2", AgentID: "coder.agent-one", State: SessionState("WAT"), CreatedAt: testNow, UpdatedAt: testNow}
	if err := bad.Transition(SessionActive, testNow); err == nil {
		t.Fatal("unknown session state must be rejected")
	}
}

// --- Permission engine ---

func activeSession(agent AgentID) *Session {
	return &Session{ID: "sess-1", AgentID: agent, State: SessionActive, CreatedAt: testNow, UpdatedAt: testNow}
}

func TestPolicyEvaluate_Allow(t *testing.T) {
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	caps, err := NewCapabilitySet(CapabilityStatusRead, CapabilityComputePlan)
	if err != nil {
		t.Fatalf("valid capability set rejected: %v", err)
	}
	policy := Policy{}
	d := policy.Evaluate(agent, activeSession(agent.ID), caps, CapabilityStatusRead)
	if !d.Allow {
		t.Fatalf("expected allow, got %+v", d)
	}
	if !strings.HasPrefix(d.Reason, "grant:") {
		t.Fatalf("allow must cite the grant, got %q", d.Reason)
	}
}

func TestPolicyEvaluate_Denials(t *testing.T) {
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	policy := Policy{Disabled: map[Capability]bool{CapabilityModeRequest: true}}
	session := activeSession(agent.ID)
	fullCaps, _ := NewCapabilitySet(CapabilityStatusRead, CapabilityModeRequest, CapabilityComputePlan)
	readOnlyCaps, _ := NewCapabilitySet(CapabilityStatusRead)

	cases := []struct {
		name       string
		session    *Session
		caps       CapabilitySet
		capability Capability
	}{
		{"no session", nil, fullCaps, CapabilityStatusRead},
		{"foreign session", activeSession("other.agent"), fullCaps, CapabilityStatusRead},
		{"created state", &Session{ID: "s", AgentID: agent.ID, State: SessionCreated, CreatedAt: testNow, UpdatedAt: testNow}, fullCaps, CapabilityStatusRead},
		{"suspended state", &Session{ID: "s", AgentID: agent.ID, State: SessionSuspended, CreatedAt: testNow, UpdatedAt: testNow}, fullCaps, CapabilityStatusRead},
		{"closed state", &Session{ID: "s", AgentID: agent.ID, State: SessionClosed, CreatedAt: testNow, UpdatedAt: testNow}, fullCaps, CapabilityStatusRead},
		{"unknown capability", session, fullCaps, Capability("shell.exec")},
		{"disabled capability", session, fullCaps, CapabilityModeRequest},
		{"not granted capability", session, readOnlyCaps, CapabilityComputePlan},
		{"nil capability set", session, nil, CapabilityStatusRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := policy.Evaluate(agent, tc.session, tc.caps, tc.capability)
			if d.Allow {
				t.Fatalf("expected denial, got %+v", d)
			}
			if !strings.Contains(d.Reason, "rule/") {
				t.Fatalf("denial must name the fired rule, got %q", d.Reason)
			}
		})
	}
}

func TestPolicyEvaluate_NotGrantedReason(t *testing.T) {
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	policy := Policy{}
	readOnlyCaps, _ := NewCapabilitySet(CapabilityStatusRead)
	d := policy.Evaluate(agent, activeSession(agent.ID), readOnlyCaps, CapabilityComputeReserve)
	if d.Allow {
		t.Fatal("compute.reserve must be denied to a read-only agent")
	}
	if !strings.Contains(d.Reason, "rule/capability-not-granted") {
		t.Fatalf("denial must cite the not-granted rule, got %q", d.Reason)
	}
	if !strings.Contains(d.Reason, "compute.reserve") {
		t.Fatalf("denial must name the capability, got %q", d.Reason)
	}
}

func TestPolicyEvaluate_Deterministic(t *testing.T) {
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	policy := Policy{}
	session := activeSession(agent.ID)
	caps, _ := NewCapabilitySet(CapabilityComputePlan)
	first := policy.Evaluate(agent, session, caps, CapabilityComputePlan)
	for i := 0; i < 100; i++ {
		if got := policy.Evaluate(agent, session, caps, CapabilityComputePlan); got != first {
			t.Fatalf("Evaluate is not deterministic: %+v vs %+v", got, first)
		}
	}
}

// --- Audit ---

func TestAuditEvent(t *testing.T) {
	agent, _ := NewAgentIdentity("coder.agent-one", "zmor", testNow)
	policy := Policy{}
	session := activeSession(agent.ID)
	caps, _ := NewCapabilitySet(CapabilityStatusRead)
	decision := policy.Evaluate(agent, session, caps, CapabilityStatusRead)

	if _, err := NewAuditEvent(time.Time{}, agent, session, CapabilityStatusRead, decision); err == nil {
		t.Fatal("zero timestamp must be rejected")
	}
	if _, err := NewAuditEvent(testNow, agent, &Session{ID: "", AgentID: agent.ID}, CapabilityStatusRead, decision); err == nil {
		t.Fatal("empty session id must be rejected")
	}
	event, err := NewAuditEvent(testNow, agent, session, CapabilityStatusRead, decision)
	if err != nil {
		t.Fatalf("valid audit event rejected: %v", err)
	}
	if event.AgentID != agent.ID || event.SessionID != "sess-1" || event.Decision != decision {
		t.Fatalf("audit event fields mismatch: %+v", event)
	}
}
