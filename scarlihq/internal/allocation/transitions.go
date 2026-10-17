package allocation

import (
	"fmt"
	"time"
)

// legalTransitions is the FROZEN transition table for v19.2.2.
// Key = current state. Value = set of allowed next states.
//
// Rules enforced by this table (per SCARGEN_COMPUTE_FABRIC_PHASE_2_GUIDE.md §4):
//   - RELEASED is terminal (no entries).
//   - CLEANUP_REQUIRED → RELEASING or RELEASED (only after cleanup verified).
//   - No arbitrary state jumps based on caller-supplied target.
//   - RUNNING cannot be reached via an invalid shortcut
//     (must come from READY only).
//
// Do NOT mutate this map at runtime. It is a package-level constant.
var legalTransitions = map[State]map[State]bool{
	StateRequested: {
		StateValidating: true,
		StateCancelled:  true,
		StateFailed:     true,
	},
	StateValidating: {
		StatePlanned:   true,
		StateCancelled: true,
		StateFailed:    true,
	},
	StatePlanned: {
		StateReserving: true,
		StateCancelled: true,
		StateFailed:    true,
	},
	StateReserving: {
		StateReserved:        true,
		StateFailed:          true,
		StateCancelled:       true,
		StateCleanupRequired: true,
	},
	StateReserved: {
		StateStarting:  true,
		StateReleasing: true,
		StateFailed:    true,
		StateCancelled: true,
	},
	StateStarting: {
		StateReady:           true,
		StateFailed:          true,
		StateCancelled:       true,
		StateReleasing:       true,
		StateCleanupRequired: true,
	},
	StateReady: {
		StateRunning:   true,
		StateReleasing: true,
		StateFailed:    true,
		StateCancelled: true,
	},
	StateRunning: {
		StateReleasing: true,
		StateFailed:    true,
		StateCancelled: true,
	},
	StateReleasing: {
		StateReleased:        true,
		StateCleanupRequired: true,
		StateFailed:          true,
	},
	StateFailed: {
		StateReleasing:       true,
		StateCleanupRequired: true,
	},
	StateCancelled: {
		StateReleasing:       true,
		StateCleanupRequired: true,
	},
	StateCleanupRequired: {
		StateReleasing: true,
		StateReleased:  true, // only after cleanup is positively verified
	},
	StateReleased: {}, // terminal — no transitions
}

// TransitionError is a typed error returned when a state transition is illegal.
// It carries enough context for callers to distinguish validation failures
// from unknown states and terminal-state violations.
type TransitionError struct {
	From   State
	To     State
	Reason string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("allocation: illegal transition %s → %s: %s", e.From, e.To, e.Reason)
}

// CanTransition reports whether transitioning from `from` to `to` is legal
// according to the transition table. It is the authoritative domain-level
// transition validator.
//
// This function is pure — it does not mutate any record or persisted state.
// It performs no authorization checks (ownership is enforced by the caller).
//
// Returns:
//   - true, nil  if the transition is legal
//   - false, *TransitionError  if the transition is illegal (with a reason)
func CanTransition(from, to State) (bool, error) {
	// 1. Reject unknown states.
	if !from.IsValid() {
		return false, &TransitionError{From: from, To: to, Reason: "unknown source state"}
	}
	if !to.IsValid() {
		return false, &TransitionError{From: from, To: to, Reason: "unknown target state"}
	}

	// 2. Reject all transitions from RELEASED (terminal).
	if from == StateReleased {
		return false, &TransitionError{From: from, To: to, Reason: "RELEASED is terminal — no transitions allowed"}
	}

	// 3. Check the transition table.
	allowed, exists := legalTransitions[from]
	if !exists {
		// This should never happen if AllStates() and legalTransitions are in sync.
		return false, &TransitionError{From: from, To: to, Reason: "source state has no transition entry (table bug)"}
	}

	if !allowed[to] {
		return false, &TransitionError{From: from, To: to, Reason: "transition not in legal set for this state"}
	}

	return true, nil
}

// AllowedTransitions returns the set of states that `from` can legally
// transition to. Returns an empty (non-nil) slice for terminal states.
// Used by API/CLI for showing valid options to operators.
func AllowedTransitions(from State) []State {
	if !from.IsValid() {
		return []State{}
	}
	allowed := legalTransitions[from]
	result := make([]State, 0, len(allowed))
	for s := range allowed {
		result = append(result, s)
	}
	return result
}

// TransitionResult captures the outcome of a successful transition.
// It is returned by Transition() so callers can inspect the new state
// and timestamp without re-reading the record.
type TransitionResult struct {
	NewState   State
	OccurredAt time.Time
}

// Transition performs a validated state transition on an Allocation record.
//
// This is the ONLY method that should be used to change an Allocation's state.
// It enforces:
//  1. The transition is legal (via CanTransition).
//  2. The record is NOT mutated if validation fails.
//  3. Timestamps are updated (UpdatedAt + StateChangedAt).
//  4. A transition event is appended to the record's history.
//
// Authorization/ownership is NOT checked here — the caller must verify
// that the initiating actor is permitted to cause this transition.
// State transitions are not authorization (see guide §10).
//
// Returns:
//   - *TransitionResult, nil  on success (record mutated in place)
//   - nil, *TransitionError   if the transition is illegal (record UNCHANGED)
func (a *Allocation) Transition(to State, reason string) (*TransitionResult, error) {
	if a == nil {
		return nil, &TransitionError{From: "", To: to, Reason: "nil allocation receiver"}
	}

	from := a.State
	ok, err := CanTransition(from, to)
	if !ok {
		// Record is NOT mutated — validation failed.
		return nil, err
	}

	now := time.Now().UTC()
	a.State = to
	a.UpdatedAt = now
	a.StateChangedAt = now

	// Append to transition history.
	a.History = append(a.History, TransitionEvent{
		FromState:  from,
		ToState:    to,
		OccurredAt: now,
		Reason:     sanitizeReason(reason),
	})

	return &TransitionResult{NewState: to, OccurredAt: now}, nil
}

// sanitizeReason bounds and sanitizes the reason string to prevent
// log injection and unbounded memory growth. Max 500 chars (matching
// scarlix-host-bridge's last_error truncation convention).
func sanitizeReason(reason string) string {
	if len(reason) > 500 {
		reason = reason[:500]
	}
	// Strip newlines to prevent log injection (matching host-bridge convention).
	cleaned := make([]byte, 0, len(reason))
	for _, b := range []byte(reason) {
		if b == '\n' || b == '\r' {
			continue
		}
		cleaned = append(cleaned, b)
	}
	return string(cleaned)
}
