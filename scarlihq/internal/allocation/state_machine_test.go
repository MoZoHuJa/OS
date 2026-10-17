package allocation

import (
	"testing"
)

// TestAllStates_KnownCount verifies that the state list has the expected
// 13 states from the Phase 2 guide §4.
func TestAllStates_KnownCount(t *testing.T) {
	states := AllStates()
	expected := 13
	if len(states) != expected {
		t.Errorf("AllStates() returned %d states, want %d", len(states), expected)
	}
}

// TestState_IsValid verifies known and unknown states.
func TestState_IsValid(t *testing.T) {
	valid := []State{
		StateRequested, StateValidating, StatePlanned, StateReserving,
		StateReserved, StateStarting, StateReady, StateRunning,
		StateReleasing, StateReleased, StateFailed, StateCancelled,
		StateCleanupRequired,
	}
	for _, s := range valid {
		if !s.IsValid() {
			t.Errorf("State %q should be valid", s)
		}
	}

	invalid := []State{"", "UNKNOWN", "requested", "PENDING", "DONE", "COMPLETED"}
	for _, s := range invalid {
		if s.IsValid() {
			t.Errorf("State %q should be invalid", s)
		}
	}
}

// TestState_IsTerminal verifies that only RELEASED is terminal.
func TestState_IsTerminal(t *testing.T) {
	if !StateReleased.IsTerminal() {
		t.Error("RELEASED should be terminal")
	}
	// All other states should NOT be terminal.
	nonTerminal := []State{
		StateRequested, StateValidating, StatePlanned, StateReserving,
		StateReserved, StateStarting, StateReady, StateRunning,
		StateReleasing, StateFailed, StateCancelled, StateCleanupRequired,
	}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("State %q should NOT be terminal", s)
		}
	}
}

// TestCanTransition_LegalTransitions verifies every documented legal transition
// from the Phase 2 guide §4 transition table.
func TestCanTransition_LegalTransitions(t *testing.T) {
	legal := []struct {
		from, to State
	}{
		// REQUESTED
		{StateRequested, StateValidating},
		{StateRequested, StateCancelled},
		{StateRequested, StateFailed},
		// VALIDATING
		{StateValidating, StatePlanned},
		{StateValidating, StateCancelled},
		{StateValidating, StateFailed},
		// PLANNED
		{StatePlanned, StateReserving},
		{StatePlanned, StateCancelled},
		{StatePlanned, StateFailed},
		// RESERVING
		{StateReserving, StateReserved},
		{StateReserving, StateFailed},
		{StateReserving, StateCancelled},
		{StateReserving, StateCleanupRequired},
		// RESERVED
		{StateReserved, StateStarting},
		{StateReserved, StateReleasing},
		{StateReserved, StateFailed},
		{StateReserved, StateCancelled},
		// STARTING
		{StateStarting, StateReady},
		{StateStarting, StateFailed},
		{StateStarting, StateCancelled},
		{StateStarting, StateReleasing},
		{StateStarting, StateCleanupRequired},
		// READY
		{StateReady, StateRunning},
		{StateReady, StateReleasing},
		{StateReady, StateFailed},
		{StateReady, StateCancelled},
		// RUNNING
		{StateRunning, StateReleasing},
		{StateRunning, StateFailed},
		{StateRunning, StateCancelled},
		// RELEASING
		{StateReleasing, StateReleased},
		{StateReleasing, StateCleanupRequired},
		{StateReleasing, StateFailed},
		// FAILED
		{StateFailed, StateReleasing},
		{StateFailed, StateCleanupRequired},
		// CANCELLED
		{StateCancelled, StateReleasing},
		{StateCancelled, StateCleanupRequired},
		// CLEANUP_REQUIRED
		{StateCleanupRequired, StateReleasing},
		{StateCleanupRequired, StateReleased},
	}

	for _, tt := range legal {
		t.Run(string(tt.from)+"_to_"+string(tt.to), func(t *testing.T) {
			ok, err := CanTransition(tt.from, tt.to)
			if !ok {
				t.Errorf("CanTransition(%s, %s) = false, want true; err=%v", tt.from, tt.to, err)
			}
			if err != nil {
				t.Errorf("CanTransition(%s, %s) returned error: %v", tt.from, tt.to, err)
			}
		})
	}
}

// TestCanTransition_IllegalTransitions verifies representative illegal transitions.
func TestCanTransition_IllegalTransitions(t *testing.T) {
	illegal := []struct {
		from, to State
	}{
		// No skipping: REQUESTED → PLANNED (must go through VALIDATING)
		{StateRequested, StatePlanned},
		// No skipping: REQUESTED → RESERVED
		{StateRequested, StateReserved},
		// No skipping: REQUESTED → RUNNING (the key shortcut we prevent)
		{StateRequested, StateRunning},
		// No skipping: VALIDATING → RESERVED
		{StateValidating, StateReserved},
		// No reverse: PLANNED → REQUESTED (cannot reset to erase history)
		{StatePlanned, StateRequested},
		// No reverse: RESERVED → PLANNED
		{StateReserved, StatePlanned},
		// No reverse: RUNNING → READY
		{StateRunning, StateReady},
		// No jumping to terminal without RELEASING: RUNNING → RELEASED
		{StateRunning, StateReleased},
		// No jumping to terminal without RELEASING: RESERVED → RELEASED
		{StateReserved, StateReleased},
		// No invalid self-loops on terminal-ish states
		{StateFailed, StateRunning},
		{StateCancelled, StateRunning},
		{StateCleanupRequired, StateRunning},
	}

	for _, tt := range illegal {
		t.Run(string(tt.from)+"_to_"+string(tt.to), func(t *testing.T) {
			ok, err := CanTransition(tt.from, tt.to)
			if ok {
				t.Errorf("CanTransition(%s, %s) = true, want false", tt.from, tt.to)
			}
			if err == nil {
				t.Errorf("CanTransition(%s, %s) returned nil error, want TransitionError", tt.from, tt.to)
			}
		})
	}
}

// TestCanTransition_AllTransitionsFromReleasedRejected verifies that RELEASED
// (terminal) rejects ALL transitions.
func TestCanTransition_AllTransitionsFromReleasedRejected(t *testing.T) {
	for _, target := range AllStates() {
		if target == StateReleased {
			continue // self-transition makes no sense
		}
		ok, err := CanTransition(StateReleased, target)
		if ok {
			t.Errorf("CanTransition(RELEASED, %s) = true, want false (RELEASED is terminal)", target)
		}
		if err == nil {
			t.Errorf("CanTransition(RELEASED, %s) returned nil error", target)
		}
	}
}

// TestCanTransition_UnknownStatesRejected verifies unknown states are rejected.
func TestCanTransition_UnknownStatesRejected(t *testing.T) {
	unknown := []State{"", "UNKNOWN", "PENDING", "DONE", "COMPLETED", "WAITING"}

	for _, s := range unknown {
		ok, err := CanTransition(StateRequested, s)
		if ok {
			t.Errorf("CanTransition(REQUESTED, %q) = true, want false (unknown target)", s)
		}
		if err == nil {
			t.Errorf("CanTransition(REQUESTED, %q) returned nil error", s)
		}

		ok2, err2 := CanTransition(s, StateValidating)
		if ok2 {
			t.Errorf("CanTransition(%q, VALIDATING) = true, want false (unknown source)", s)
		}
		if err2 == nil {
			t.Errorf("CanTransition(%q, VALIDATING) returned nil error", s)
		}
	}
}

// TestAllocation_Transition_MutatesRecordOnSuccess verifies that a successful
// transition updates the record's state and timestamps.
func TestAllocation_Transition_MutatesRecordOnSuccess(t *testing.T) {
	a := NewAllocation("alloc-1", "v1", "coding", "interactive", "agent.coder")
	originalChangedAt := a.StateChangedAt

	result, err := a.Transition(StateValidating, "contract parsed")
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}
	if result == nil {
		t.Fatal("Transition returned nil result")
	}
	if a.State != StateValidating {
		t.Errorf("State = %q, want VALIDATING", a.State)
	}
	if result.NewState != StateValidating {
		t.Errorf("result.NewState = %q, want VALIDATING", result.NewState)
	}
	if !a.StateChangedAt.After(originalChangedAt) {
		t.Error("StateChangedAt was not updated")
	}
	if len(a.History) != 1 {
		t.Errorf("History length = %d, want 1", len(a.History))
	}
	ev := a.History[0]
	if ev.FromState != StateRequested || ev.ToState != StateValidating {
		t.Errorf("History[0] = %s → %s, want REQUESTED → VALIDATING", ev.FromState, ev.ToState)
	}
}

// TestAllocation_Transition_DoesNotMutateOnFailure verifies that an illegal
// transition leaves the record unchanged.
func TestAllocation_Transition_DoesNotMutateOnFailure(t *testing.T) {
	a := NewAllocation("alloc-2", "v1", "coding", "interactive", "agent.coder")
	originalState := a.State
	originalChangedAt := a.StateChangedAt
	originalHistoryLen := len(a.History)

	_, err := a.Transition(StateRunning, "illegal shortcut")
	if err == nil {
		t.Fatal("Transition to RUNNING from REQUESTED should fail, but returned nil error")
	}

	if a.State != originalState {
		t.Errorf("State was mutated on failed transition: %q (want %q)", a.State, originalState)
	}
	if a.StateChangedAt != originalChangedAt {
		t.Error("StateChangedAt was mutated on failed transition")
	}
	if len(a.History) != originalHistoryLen {
		t.Errorf("History was mutated on failed transition: len=%d (want %d)", len(a.History), originalHistoryLen)
	}
}

// TestAllocation_Transition_ReasonSanitized verifies that newlines in reason
// are stripped (log injection prevention).
func TestAllocation_Transition_ReasonSanitized(t *testing.T) {
	a := NewAllocation("alloc-3", "v1", "coding", "interactive", "agent.coder")
	malicious := "validated\nFAKE LOG LINE\nrm -rf /"
	_, err := a.Transition(StateValidating, malicious)
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}
	ev := a.History[0]
	if containsCI(ev.Reason, "\n") {
		t.Errorf("Reason contains newline after sanitization: %q", ev.Reason)
	}
	if containsCI(ev.Reason, "FAKE LOG LINE") {
		// The reason text itself is preserved (minus newlines) — this is intentional.
		// We strip newlines but don't censor the content.
	}
}

// TestAllocation_Transition_ReasonTruncated verifies long reasons are bounded.
func TestAllocation_Transition_ReasonTruncated(t *testing.T) {
	a := NewAllocation("alloc-4", "v1", "coding", "interactive", "agent.coder")
	longReason := make([]byte, 1000)
	for i := range longReason {
		longReason[i] = 'x'
	}
	_, err := a.Transition(StateValidating, string(longReason))
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}
	ev := a.History[0]
	if len(ev.Reason) > 500 {
		t.Errorf("Reason length = %d, want <= 500", len(ev.Reason))
	}
}

// TestAllocation_Transition_RunningNotReachableByShortcut verifies the key
// safety property: RUNNING cannot be reached without going through READY.
func TestAllocation_Transition_RunningNotReachableByShortcut(t *testing.T) {
	shortcuts := []State{
		StateRequested, StateValidating, StatePlanned,
		StateReserving, StateReserved, StateStarting,
	}
	for _, from := range shortcuts {
		t.Run(string(from)+"_to_RUNNING", func(t *testing.T) {
			a := NewAllocation("alloc-shortcut", "v1", "coding", "interactive", "agent.coder")
			a.State = from // simulate being in this state
			ok, err := CanTransition(from, StateRunning)
			if ok {
				t.Errorf("CanTransition(%s, RUNNING) = true, want false (shortcut)", from)
			}
			if err == nil {
				t.Errorf("CanTransition(%s, RUNNING) returned nil error", from)
			}
		})
	}
}

// TestAllocation_Transition_CleanupRequiredNotReleased verifies that
// CLEANUP_REQUIRED is never treated as RELEASED.
func TestAllocation_Transition_CleanupRequiredNotReleased(t *testing.T) {
	a := NewAllocation("alloc-cleanup", "v1", "coding", "interactive", "agent.coder")
	a.State = StateCleanupRequired

	if a.State.IsTerminal() {
		t.Error("CLEANUP_REQUIRED should NOT be terminal (only RELEASED is)")
	}

	// Can go to RELEASING or RELEASED (only after verified).
	ok1, _ := CanTransition(StateCleanupRequired, StateReleasing)
	if !ok1 {
		t.Error("CLEANUP_REQUIRED → RELEASING should be legal")
	}
	ok2, _ := CanTransition(StateCleanupRequired, StateReleased)
	if !ok2 {
		t.Error("CLEANUP_REQUIRED → RELEASED should be legal (after cleanup verified)")
	}
	// But NOT to RUNNING (cannot resume from cleanup-required).
	ok3, _ := CanTransition(StateCleanupRequired, StateRunning)
	if ok3 {
		t.Error("CLEANUP_REQUIRED → RUNNING should be illegal")
	}
}

// TestAllowedTransitions_ReturnsValidSet verifies AllowedTransitions returns
// the correct set for a few key states.
func TestAllowedTransitions_ReturnsValidSet(t *testing.T) {
	cases := []struct {
		from        State
		expectedSet []State
	}{
		{StateRequested, []State{StateValidating, StateCancelled, StateFailed}},
		{StateRunning, []State{StateReleasing, StateFailed, StateCancelled}},
		{StateReleased, []State{}}, // terminal
	}
	for _, tt := range cases {
		t.Run(string(tt.from), func(t *testing.T) {
			allowed := AllowedTransitions(tt.from)
			if len(allowed) != len(tt.expectedSet) {
				t.Errorf("AllowedTransitions(%s) returned %d states, want %d",
					tt.from, len(allowed), len(tt.expectedSet))
				return
			}
			// Check each expected state is present.
			for _, expected := range tt.expectedSet {
				found := false
				for _, actual := range allowed {
					if actual == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("AllowedTransitions(%s) missing %s", tt.from, expected)
				}
			}
		})
	}
}

// TestAllowedTransitions_UnknownStateReturnsEmpty verifies unknown states
// return an empty slice (not nil).
func TestAllowedTransitions_UnknownStateReturnsEmpty(t *testing.T) {
	result := AllowedTransitions(State("UNKNOWN"))
	if result == nil {
		t.Error("AllowedTransitions(UNKNOWN) returned nil, want empty slice")
	}
	if len(result) != 0 {
		t.Errorf("AllowedTransitions(UNKNOWN) returned %d items, want 0", len(result))
	}
}

// TestAllocation_Transition_NilReceiver verifies nil receiver is handled safely.
func TestAllocation_Transition_NilReceiver(t *testing.T) {
	var a *Allocation
	_, err := a.Transition(StateValidating, "test")
	if err == nil {
		t.Fatal("Transition on nil receiver should return error")
	}
}

// TestTransitionError_Error verifies the error message format.
func TestTransitionError_Error(t *testing.T) {
	e := &TransitionError{
		From:   StateRequested,
		To:     StateRunning,
		Reason: "shortcut not allowed",
	}
	msg := e.Error()
	if msg == "" {
		t.Error("Error() returned empty string")
	}
	// Check it contains both states and the reason.
	for _, substr := range []string{string(StateRequested), string(StateRunning), "shortcut"} {
		if !containsCI(msg, substr) {
			t.Errorf("Error() = %q, missing %q", msg, substr)
		}
	}
}
