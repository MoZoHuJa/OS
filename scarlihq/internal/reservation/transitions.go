package reservation

import (
	"fmt"
	"time"
)

// reservationTransitions is the FROZEN transition table for reservation statuses.
// KPK-09 (v19.2.5): Centralized transition validation.
//
// Key rules:
//   - RELEASED is terminal (no outgoing transitions).
//   - HELD → RELEASING (start cleanup)
//   - HELD → EXPIRED_PENDING_RECONCILIATION (lease expired)
//   - RELEASING → RELEASED (cleanup confirmed, requires CleanupEvidence via Manager)
//   - RELEASING → CONFLICT (cleanup failed/ambiguous)
//   - EXPIRED_PENDING_RECONCILIATION → RELEASED (reconciler confirmed cleanup)
//   - EXPIRED_PENDING_RECONCILIATION → CONFLICT (ambiguous ownership)
//   - EXPIRED_PENDING_RECONCILIATION → RELEASING (start cleanup after expiry)
//   - CONFLICT → RELEASING (start conflict resolution cleanup)
//   - CONFLICT → RELEASED (conflict resolved, cleanup confirmed)
var reservationTransitions = map[ReservationStatus]map[ReservationStatus]bool{
	StatusHeld: {
		StatusReleasing:                    true,
		StatusExpiredPendingReconciliation: true,
	},
	StatusReleasing: {
		StatusReleased: true, // requires cleanupConfirmed
		StatusConflict: true, // cleanup failed/ambiguous
	},
	StatusExpiredPendingReconciliation: {
		StatusReleased:  true, // reconciler confirmed cleanup
		StatusConflict:  true, // ambiguous ownership
		StatusReleasing: true, // start cleanup after expiry
	},
	StatusConflict: {
		StatusReleasing: true,
		StatusReleased:  true, // conflict resolved + cleanup confirmed
	},
	StatusReleased: {}, // terminal
}

// CanTransition reports whether transitioning from `from` to `to` is legal.
func canTransition(from, to ReservationStatus) bool {
	if !from.IsValid() || !to.IsValid() {
		return false
	}
	allowed, exists := reservationTransitions[from]
	if !exists {
		return false
	}
	return allowed[to]
}

// TransitionError is a typed error for illegal reservation transitions.
type TransitionError struct {
	From   ReservationStatus
	To     ReservationStatus
	Reason string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("reservation: illegal transition %s → %s: %s", e.From, e.To, e.Reason)
}

// transition performs a validated state transition on a reservation.
// KPK-09 (v19.2.5): Centralized transition logic.
// Does NOT mutate the record if validation fails.
func (r *Reservation) transition(to ReservationStatus, now time.Time, reason string) error {
	if r == nil {
		return &TransitionError{From: "", To: to, Reason: "nil reservation"}
	}
	from := r.Status
	if !canTransition(from, to) {
		return &TransitionError{From: from, To: to, Reason: "transition not in legal set"}
	}
	r.Status = to
	r.UpdatedAt = now
	r.LastReason = sanitizeReason(reason)
	return nil
}

// sanitizeReason bounds and sanitizes the reason string.
func sanitizeReason(reason string) string {
	if len(reason) > 500 {
		reason = reason[:500]
	}
	cleaned := make([]byte, 0, len(reason))
	for _, b := range []byte(reason) {
		if b == '\n' || b == '\r' {
			continue
		}
		cleaned = append(cleaned, b)
	}
	return string(cleaned)
}
