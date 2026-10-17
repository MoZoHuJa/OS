package reservation

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"syscall"
	"time"
)

// Clock is an injectable clock for testing.
type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type FakeClock struct {
	T time.Time
}

func (f *FakeClock) Now() time.Time          { return f.T }
func (f *FakeClock) Set(t time.Time)         { f.T = t }
func (f *FakeClock) Advance(d time.Duration) { f.T = f.T.Add(d) }

// InventoryProvider supplies trusted inventory data to the reservation manager.
// P1-06 (v19.2.6): MANDATORY — nil provider is rejected at construction.
// The manager calls this inside the reservation critical section.
type InventoryProvider interface {
	SnapshotGeneration() string
	SnapshotAge() time.Duration
	ResourceExists(resourceID string, resourceType ResourceType) bool
}

// AllocationValidator validates that an allocation is eligible for reservation.
// P1-07 (v19.2.6): MANDATORY — validates allocation existence and state.
type AllocationValidator interface {
	// ValidateForReservation checks that allocID exists and is in a state
	// eligible for reservation (e.g. PLANNED or RESERVING).
	ValidateForReservation(allocID string) error
}

// Manager is the single domain entry point for reserve/renew/release operations.
// P1-01 (v19.2.6): Store mutation methods are package-private; all mutations
// go through Manager which enforces invariants.
type Manager struct {
	store     *Store
	policy    ReservationPolicy
	clock     Clock
	inventory InventoryProvider
	allocVal  AllocationValidator
}

// NewManager creates a Manager with the given dependencies.
// P1-06 (v19.2.6): InventoryProvider is mandatory (nil rejected).
// P1-07 (v19.2.6): AllocationValidator is mandatory (nil rejected).
// P2-05 (v19.2.6): Store is mandatory (nil rejected).
func NewManager(store *Store, policy ReservationPolicy, clock Clock, inventory InventoryProvider, allocVal AllocationValidator) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("reservation: store is required (nil rejected)")
	}
	if clock == nil {
		clock = RealClock{}
	}
	if inventory == nil {
		return nil, fmt.Errorf("reservation: inventory provider is required (nil rejected)")
	}
	if allocVal == nil {
		return nil, fmt.Errorf("reservation: allocation validator is required (nil rejected)")
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("reservation: invalid policy: %w", err)
	}
	return &Manager{
		store:     store,
		policy:    policy,
		clock:     clock,
		inventory: inventory,
		allocVal:  allocVal,
	}, nil
}

func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ReserveRequest contains parameters for creating a reservation.
type ReserveRequest struct {
	AllocationID       string
	ResourceID         string
	ResourceType       ResourceType
	Identity           Identity
	LeaseTTL           time.Duration
	IdempotencyKey     string
	RequestFingerprint string
}

// ReserveResult is the outcome of a Reserve operation.
type ReserveResult struct {
	Reservation *Reservation
	Created     bool
}

// Reserve creates a reservation.
// P1-01 (v19.2.6): Only entry point — store.append is package-private.
// P1-04 (v19.2.6): No boolean cleanupConfirmed — separate ConfirmRelease.
// P1-05 (v19.2.6): No excludeAllocationID — all blocking reservations count.
// P1-06 (v19.2.6): Inventory revalidated inside flock critical section.
// P1-07 (v19.2.6): Allocation validated before reservation.
// P2-03 (v19.2.6): Resource ID canonicalized.
func (m *Manager) Reserve(req ReserveRequest) (*ReserveResult, error) {
	// 1. Validate request.
	if req.AllocationID == "" {
		return nil, fmt.Errorf("reservation: allocation_id is required")
	}
	if req.ResourceID == "" {
		return nil, fmt.Errorf("reservation: resource_id is required")
	}
	if !req.ResourceType.IsValid() {
		return nil, fmt.Errorf("reservation: invalid resource type %q", req.ResourceType)
	}
	if req.Identity.Principal == "" {
		return nil, fmt.Errorf("reservation: identity is required")
	}

	// P2-03: Canonicalize resource ID.
	canonID, err := CanonicalResourceID(req.ResourceID, req.ResourceType)
	if err != nil {
		return nil, fmt.Errorf("reservation: invalid resource_id: %w", err)
	}

	// KPK-06: If idempotency key is provided, fingerprint must be non-empty.
	if req.IdempotencyKey != "" && req.RequestFingerprint == "" {
		return nil, fmt.Errorf("reservation: idempotency key requires non-empty request fingerprint")
	}

	// KPK-07: LeaseTTL must be bounded.
	if req.LeaseTTL <= 0 {
		return nil, fmt.Errorf("reservation: lease TTL must be > 0 (got %v)", req.LeaseTTL)
	}
	if !m.policy.ValidateLeaseTTL(req.LeaseTTL) {
		return nil, fmt.Errorf("reservation: lease TTL %v outside allowed range [%v, %v]",
			req.LeaseTTL, m.policy.LeaseMinTTL, m.policy.LeaseMaxTTL)
	}

	// P1-07: Validate allocation before reservation.
	if err := m.allocVal.ValidateForReservation(req.AllocationID); err != nil {
		return nil, fmt.Errorf("reservation: allocation validation failed: %w", err)
	}

	// P1-06: Check inventory freshness from trusted provider (pre-lock).
	age := m.inventory.SnapshotAge()
	if m.policy.IsInventoryStale(age) {
		return nil, ErrStaleInventory
	}
	// P1-06: Validate resource existence (pre-lock).
	if !m.inventory.ResourceExists(canonID, req.ResourceType) {
		return nil, ErrResourceNotFound
	}

	leaseExpiry := m.clock.Now().Add(req.LeaseTTL)

	// Acquire flock for the entire check-then-write cycle.
	lockFile, err := m.store.acquireFlock()
	if err != nil {
		return nil, fmt.Errorf("reservation: cannot acquire flock: %w", err)
	}
	defer func() {
		syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
	}()

	// P1-06: Revalidate inventory inside the critical section.
	age2 := m.inventory.SnapshotAge()
	if m.policy.IsInventoryStale(age2) {
		return nil, ErrStaleInventory
	}
	if !m.inventory.ResourceExists(canonID, req.ResourceType) {
		return nil, ErrResourceNotFound
	}

	records, corrupt, err := m.store.loadLocked()
	if err != nil {
		return nil, fmt.Errorf("reservation: cannot load: %w", err)
	}
	if len(corrupt) > 0 {
		return nil, fmt.Errorf("%w: %d corrupt lines", ErrReservationStoreCorrupt, len(corrupt))
	}

	// KPK-06: Idempotency check (under flock).
	if req.IdempotencyKey != "" {
		for _, existing := range records {
			if existing.IdempotencyKey == req.IdempotencyKey && existing.Owner == req.Identity.Principal {
				if existing.RequestFingerprint != req.RequestFingerprint {
					return nil, ErrIdempotencyConflict
				}
				return &ReserveResult{Reservation: existing, Created: false}, nil
			}
		}
	}

	// Build all reservations list for policy check.
	allReservations := make([]*Reservation, 0, len(records))
	for _, r := range records {
		allReservations = append(allReservations, r)
	}

	// P1-05: Policy check — NO excludeAllocationID. ALL blocking reservations count.
	switch req.ResourceType {
	case ResourceTypeGPUDevice:
		if !m.policy.CanReserveGPU(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrReservationConflict, ResourceID: canonID, Owner: req.Identity.Principal, Detail: "GPU already reserved"}
		}
	case ResourceTypeRuntimeSlot:
		if !m.policy.CanReserveRuntimeSlot(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrCapacityExceeded, ResourceID: canonID, Owner: req.Identity.Principal, Detail: "runtime slot capacity exceeded"}
		}
	case ResourceTypeModelSlot:
		if !m.policy.CanReserveModelSlot(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrCapacityExceeded, ResourceID: canonID, Owner: req.Identity.Principal, Detail: "model slot capacity exceeded"}
		}
	case ResourceTypePort:
		if !m.policy.CanReservePort(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrReservationConflict, ResourceID: canonID, Owner: req.Identity.Principal, Detail: "port already reserved"}
		}
	default:
		return nil, fmt.Errorf("reservation: unsupported resource type %q", req.ResourceType)
	}

	// Create reservation.
	resID := generateID()
	leaseID := generateID()
	now := m.clock.Now()

	r := &Reservation{
		ReservationID:       resID,
		AllocationID:        req.AllocationID,
		SchemaVersion:       ReservationSchemaVersion,
		ResourceID:          canonID,
		ResourceType:        req.ResourceType,
		Owner:               req.Identity.Principal,
		LeaseID:             leaseID,
		LeaseExpiry:         leaseExpiry,
		LastRenewedAt:       now,
		InventoryGeneration: m.inventory.SnapshotGeneration(),
		Status:              StatusHeld,
		CreatedAt:           now,
		UpdatedAt:           now,
		IdempotencyKey:      req.IdempotencyKey,
		RequestFingerprint:  req.RequestFingerprint,
		Revision:            0,
	}

	// Persist (P1-01: store.append is package-private — only Manager calls it).
	records[r.ReservationID] = r
	if err := m.store.rewriteAll(records); err != nil {
		return nil, fmt.Errorf("reservation: cannot persist: %w", err)
	}

	return &ReserveResult{Reservation: r, Created: true}, nil
}

// RenewLease extends the lease TTL.
// KPK-08: Rejects if lease has expired.
func (m *Manager) RenewLease(reservationID string, identity Identity, newTTL time.Duration) (*Reservation, error) {
	if !m.policy.ValidateLeaseTTL(newTTL) {
		return nil, fmt.Errorf("reservation: lease TTL %v outside allowed range", newTTL)
	}
	var updated *Reservation
	err := m.store.update(reservationID, func(r *Reservation) error {
		if r.Owner != identity.Principal {
			return ErrLeaseNotOwned
		}
		if r.Status != StatusHeld {
			return fmt.Errorf("reservation: cannot renew in status %s", r.Status)
		}
		if r.IsExpired(m.clock.Now()) {
			return ErrLeaseExpired
		}
		r.LeaseExpiry = m.clock.Now().Add(newTTL)
		r.LastRenewedAt = m.clock.Now()
		r.UpdatedAt = m.clock.Now()
		updated = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// RequestRelease starts the release process (HELD/EXPIRED/CONFLICT → RELEASING).
// P1-04 (v19.2.6): Does NOT accept a cleanupConfirmed boolean.
// Use ConfirmRelease to transition RELEASING → RELEASED with verified evidence.
// P2-01 (v19.2.6): Handles all eligible source states.
func (m *Manager) RequestRelease(reservationID string, identity Identity) (*Reservation, error) {
	var updated *Reservation
	err := m.store.update(reservationID, func(r *Reservation) error {
		if r.Owner != identity.Principal && !identity.IsReconciler {
			return ErrLeaseNotOwned
		}

		now := m.clock.Now()
		oldStatus := r.Status

		// P2-01: Handle all eligible source states.
		switch r.Status {
		case StatusHeld, StatusExpiredPendingReconciliation, StatusConflict:
			if err := r.transition(StatusReleasing, now, "release requested"); err != nil {
				return err
			}
		case StatusReleasing:
			// Already releasing — no-op (idempotent).
		case StatusReleased:
			// Already released — no-op (idempotent).
		default:
			return fmt.Errorf("reservation: cannot release from status %s", r.Status)
		}

		_ = oldStatus
		updated = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// ConfirmRelease transitions RELEASING → RELEASED using verified CleanupEvidence.
// P1-04 (v19.2.6): Replaces bare boolean. Evidence must come from trusted
// reconciler and match the reservation's resource.
// P1-03 (v19.2.6): Reconciler identity must match evidence.ReconcilerID.
func (m *Manager) ConfirmRelease(reservationID string, reconciler Identity, evidence CleanupEvidence) (*Reservation, error) {
	// P1-03: Require non-empty principal.
	if reconciler.Principal == "" {
		return nil, fmt.Errorf("reservation: reconciler principal is required")
	}
	if !reconciler.IsReconciler {
		return nil, fmt.Errorf("reservation: only trusted reconcilers can confirm release")
	}

	// P1-03: Evidence must be bound to this reconciler.
	if evidence.ReconcilerID != reconciler.Principal {
		return nil, fmt.Errorf("reservation: evidence reconciler_id %q does not match reconciler principal %q",
			evidence.ReconcilerID, reconciler.Principal)
	}

	// P1-03: Reject zero/future timestamps.
	if evidence.ObservedAt.IsZero() {
		return nil, fmt.Errorf("reservation: evidence observed_at is zero")
	}
	now := m.clock.Now()
	if evidence.ObservedAt.After(now.Add(1 * time.Second)) {
		return nil, fmt.Errorf("reservation: evidence observed_at is in the future")
	}
	if now.Sub(evidence.ObservedAt) > 5*time.Minute {
		return nil, fmt.Errorf("reservation: evidence is stale (observed %v ago)", now.Sub(evidence.ObservedAt))
	}

	var updated *Reservation
	err := m.store.update(reservationID, func(r *Reservation) error {
		// P1-03: Validate evidence belongs to this reservation's resource.
		if evidence.ResourceID != r.ResourceID {
			return fmt.Errorf("reservation: evidence resource_id %q does not match reservation resource_id %q",
				evidence.ResourceID, r.ResourceID)
		}

		now := m.clock.Now()
		switch r.Status {
		case StatusReleasing:
			if evidence.Verified {
				if err := r.transition(StatusReleased, now, "cleanup confirmed: "+evidence.OperationID); err != nil {
					return err
				}
			} else {
				if err := r.transition(StatusConflict, now, "cleanup failed: "+evidence.OperationID); err != nil {
					return err
				}
			}
		case StatusReleased:
			// Already released — idempotent.
		default:
			return fmt.Errorf("reservation: cannot confirm release from status %s (must be RELEASING)", r.Status)
		}
		updated = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// MarkExpiredCandidates marks expired reservations.
// P2-02 (v19.2.6): Accurate count via callback — checks if status actually changed.
func (m *Manager) MarkExpiredCandidates() (int, error) {
	records, corrupt, err := m.store.Load()
	if err != nil {
		return 0, err
	}
	if len(corrupt) > 0 {
		return 0, fmt.Errorf("%w: %d corrupt lines", ErrReservationStoreCorrupt, len(corrupt))
	}

	now := m.clock.Now()
	marked := 0
	for _, r := range records {
		if r.Status != StatusHeld || !r.IsExpired(now) {
			continue
		}
		// P2-02: Track whether the callback actually changed the status.
		changed := false
		err := m.store.update(r.ReservationID, func(r2 *Reservation) error {
			if r2.Status != StatusHeld {
				return nil
			}
			if !r2.IsExpired(m.clock.Now()) {
				return nil
			}
			if err := r2.transition(StatusExpiredPendingReconciliation, m.clock.Now(), "lease expired"); err != nil {
				return err
			}
			changed = true
			return nil
		})
		if err != nil {
			return marked, err
		}
		if changed {
			marked++
		}
	}
	return marked, nil
}

// ReconcileExpired transitions EXPIRED_PENDING_RECONCILIATION.
// P1-03 (v19.2.6): Reconciler identity bound to evidence.
func (m *Manager) ReconcileExpired(reservationID string, reconciler Identity, evidence CleanupEvidence) (*Reservation, error) {
	if reconciler.Principal == "" || !reconciler.IsReconciler {
		return nil, fmt.Errorf("reservation: only trusted reconcilers can reconcile")
	}
	if evidence.ReconcilerID != reconciler.Principal {
		return nil, fmt.Errorf("reservation: evidence reconciler_id does not match reconciler principal")
	}
	if evidence.ObservedAt.IsZero() {
		return nil, fmt.Errorf("reservation: evidence observed_at is zero")
	}
	now := m.clock.Now()
	if evidence.ObservedAt.After(now.Add(1 * time.Second)) {
		return nil, fmt.Errorf("reservation: evidence observed_at is in the future")
	}
	if now.Sub(evidence.ObservedAt) > 5*time.Minute {
		return nil, fmt.Errorf("reservation: evidence is stale")
	}

	var updated *Reservation
	err := m.store.update(reservationID, func(r *Reservation) error {
		if r.Status != StatusExpiredPendingReconciliation {
			return fmt.Errorf("reservation: cannot reconcile from status %s", r.Status)
		}
		if evidence.ResourceID != r.ResourceID {
			return fmt.Errorf("reservation: evidence resource_id mismatch")
		}
		now := m.clock.Now()
		if evidence.Verified {
			return r.transition(StatusReleased, now, "reconciler confirmed: "+evidence.OperationID)
		}
		return r.transition(StatusConflict, now, "reconciler reported failure: "+evidence.OperationID)
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// Get retrieves a reservation by ID.
func (m *Manager) Get(id string) (*Reservation, error) {
	return m.store.Get(id)
}

// ListByAllocation returns all reservations for an allocation.
func (m *Manager) ListByAllocation(allocID string) ([]*Reservation, error) {
	return m.store.ListByAllocation(allocID)
}
