package reservation

import (
	"errors"
	"fmt"
)

// Typed errors for the reservation subsystem.
// All errors are safe to log — they never contain secrets, raw prompts,
// or full request bodies.

var (
	// ErrUnauthenticated means the supplied bearer token is not configured.
	ErrUnauthenticated = errors.New("reservation: unauthenticated")

	// ErrResourceNotFound means the requested resource does not exist in inventory.
	ErrResourceNotFound = errors.New("reservation: resource not found")

	// ErrResourceUnavailable means the resource exists but cannot be reserved
	// (e.g. GPU health is not "healthy").
	ErrResourceUnavailable = errors.New("reservation: resource unavailable")

	// ErrCapacityExceeded means the resource is at capacity (e.g. all runtime
	// slots are in use).
	ErrCapacityExceeded = errors.New("reservation: capacity exceeded")

	// ErrReservationConflict means a conflicting reservation already exists
	// for the same exclusive resource.
	ErrReservationConflict = errors.New("reservation: conflict — resource already reserved")

	// ErrIdempotencyConflict means an idempotency key was reused with a
	// different request fingerprint.
	ErrIdempotencyConflict = errors.New("reservation: idempotency key conflict")

	// ErrLeaseExpired means the lease has expired and the reservation
	// requires reconciliation.
	ErrLeaseExpired = errors.New("reservation: lease expired")

	// ErrLeaseNotOwned means the caller attempted to renew/release a lease
	// they do not own.
	ErrLeaseNotOwned = errors.New("reservation: lease not owned by caller")

	// ErrStaleInventory means the inventory snapshot is older than the
	// configured freshness limit.
	ErrStaleInventory = errors.New("reservation: inventory snapshot is stale")

	// ErrStoreCorrupt means the reservation store has corrupt data.
	ErrStoreCorrupt = errors.New("reservation: store corrupt")

	// ErrPersistence means a write to the store failed.
	ErrPersistence = errors.New("reservation: persistence failure")

	// ErrReconciliationRequired means the reservation is in a state that
	// requires manual or automated reconciliation before it can be used.
	ErrReconciliationRequired = errors.New("reservation: reconciliation required")

	// ErrAllocationReconciliationPending means the reservation itself was
	// durably mutated (e.g. released), but the allocation lifecycle record
	// could not be reconciled in the same operation. The reservation store
	// remains authoritative; callers must surface this to the operator and
	// the startup recovery scan (Recover/ReconcileAllPending) must converge
	// the allocation state. Never swallow this error.
	// KPK-19.2.12-P1-01.
	ErrAllocationReconciliationPending = errors.New("reservation: allocation reconciliation pending")
)

// ReservationError wraps a typed error with additional context.
type ReservationError struct {
	Err        error
	ResourceID string
	Owner      string
	Detail     string
}

func (e *ReservationError) Error() string {
	if e.ResourceID != "" {
		return fmt.Sprintf("%v: resource=%s owner=%s: %s", e.Err, e.ResourceID, e.Owner, e.Detail)
	}
	return fmt.Sprintf("%v: %s", e.Err, e.Detail)
}

func (e *ReservationError) Unwrap() error {
	return e.Err
}
