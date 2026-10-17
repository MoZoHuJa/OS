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
// KPK-04 (v19.2.5): Replaces caller-supplied InventorySnapshotAge.
// The manager calls this interface inside the reservation critical section
// to get a fresh, trusted snapshot.
type InventoryProvider interface {
	// SnapshotGeneration returns the current inventory generation identifier.
	SnapshotGeneration() string
	// SnapshotAge returns how old the current inventory snapshot is.
	SnapshotAge() time.Duration
	// ResourceExists checks if a resource exists in inventory and is healthy.
	// KPK-12 (v19.2.5): Validates resource existence and health.
	ResourceExists(resourceID string, resourceType ResourceType) bool
}

// Manager is the single domain entry point for reserve/renew/release operations.
type Manager struct {
	store     *Store
	policy    ReservationPolicy
	clock     Clock
	inventory InventoryProvider // KPK-04: may be nil (disables freshness check)
}

// NewManager creates a Manager with the given store and policy.
// KPK-15 (v19.2.5): Validates policy at construction.
func NewManager(store *Store, policy ReservationPolicy, clock Clock, inventory InventoryProvider) (*Manager, error) {
	if clock == nil {
		clock = RealClock{}
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("reservation: invalid policy: %w", err)
	}
	return &Manager{
		store:     store,
		policy:    policy,
		clock:     clock,
		inventory: inventory,
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
// KPK-05 (v19.2.5): Owner is replaced by Identity at the Manager level.
type ReserveRequest struct {
	AllocationID       string
	ResourceID         string
	ResourceType       ResourceType
	Identity           Identity // KPK-05: verified identity, not raw string
	LeaseTTL           time.Duration
	IdempotencyKey     string
	RequestFingerprint string
}

// ReserveResult is the outcome of a Reserve operation.
type ReserveResult struct {
	Reservation *Reservation
	Created     bool
}

// Reserve creates a reservation for the given request.
//
// KPK fixes applied:
// - KPK-02: Port resource type now has explicit policy branch.
// - KPK-04: Inventory freshness from trusted provider, not caller-supplied.
// - KPK-05: Owner derived from Identity, not raw string.
// - KPK-06: Idempotency key requires non-empty fingerprint.
// - KPK-07: LeaseTTL must be bounded (zero/negative rejected).
// - KPK-09: Uses transition table for status changes.
// - KPK-12: Resource existence validated via InventoryProvider (if available).
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

	// KPK-06: If idempotency key is provided, fingerprint must be non-empty.
	if req.IdempotencyKey != "" && req.RequestFingerprint == "" {
		return nil, fmt.Errorf("reservation: idempotency key requires non-empty request fingerprint")
	}

	// KPK-07: LeaseTTL must be bounded — zero/negative is rejected.
	if req.LeaseTTL <= 0 {
		return nil, fmt.Errorf("reservation: lease TTL must be > 0 (got %v)", req.LeaseTTL)
	}
	if !m.policy.ValidateLeaseTTL(req.LeaseTTL) {
		return nil, fmt.Errorf("reservation: lease TTL %v outside allowed range [%v, %v]",
			req.LeaseTTL, m.policy.LeaseMinTTL, m.policy.LeaseMaxTTL)
	}

	// KPK-04: Check inventory freshness from trusted provider.
	if m.inventory != nil {
		age := m.inventory.SnapshotAge()
		if m.policy.IsInventoryStale(age) {
			return nil, ErrStaleInventory
		}
		// KPK-12: Validate resource existence.
		if !m.inventory.ResourceExists(req.ResourceID, req.ResourceType) {
			return nil, ErrResourceNotFound
		}
	}

	// Compute lease expiry from trusted clock.
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

	// Load all records under the flock.
	records, corrupt, err := m.store.loadLocked()
	if err != nil {
		return nil, fmt.Errorf("reservation: cannot load: %w", err)
	}
	if len(corrupt) > 0 {
		return nil, fmt.Errorf("%w: %d corrupt lines", ErrReservationStoreCorrupt, len(corrupt))
	}

	// KPK-04: Revalidate inventory freshness inside the critical section.
	if m.inventory != nil {
		age := m.inventory.SnapshotAge()
		if m.policy.IsInventoryStale(age) {
			return nil, ErrStaleInventory
		}
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

	// KPK-01: Build active reservations using BlocksResource() (not just HELD).
	allReservations := make([]*Reservation, 0, len(records))
	for _, r := range records {
		allReservations = append(allReservations, r)
	}

	// Policy check — KPK-02: explicit branch for every resource type.
	canReserve := false
	switch req.ResourceType {
	case ResourceTypeGPUDevice:
		canReserve = m.policy.CanReserveGPU(req.ResourceID, allReservations, req.AllocationID)
		if !canReserve {
			return nil, &ReservationError{
				Err: ErrReservationConflict, ResourceID: req.ResourceID,
				Owner: req.Identity.Principal, Detail: "GPU already reserved (blocked)",
			}
		}
	case ResourceTypeRuntimeSlot:
		canReserve = m.policy.CanReserveRuntimeSlot(req.ResourceID, allReservations, req.AllocationID)
		if !canReserve {
			return nil, &ReservationError{
				Err: ErrCapacityExceeded, ResourceID: req.ResourceID,
				Owner: req.Identity.Principal, Detail: "runtime slot capacity exceeded",
			}
		}
	case ResourceTypeModelSlot:
		canReserve = m.policy.CanReserveModelSlot(req.ResourceID, allReservations, req.AllocationID)
		if !canReserve {
			return nil, &ReservationError{
				Err: ErrCapacityExceeded, ResourceID: req.ResourceID,
				Owner: req.Identity.Principal, Detail: "model slot capacity exceeded",
			}
		}
	case ResourceTypePort:
		// KPK-02: Explicit port policy branch.
		canReserve = m.policy.CanReservePort(req.ResourceID, allReservations, req.AllocationID)
		if !canReserve {
			return nil, &ReservationError{
				Err: ErrReservationConflict, ResourceID: req.ResourceID,
				Owner: req.Identity.Principal, Detail: "port already reserved",
			}
		}
	default:
		// KPK-02: Fail-closed on unknown resource types.
		return nil, fmt.Errorf("reservation: unsupported resource type %q", req.ResourceType)
	}

	// Create reservation.
	resID := generateID()
	leaseID := generateID()
	now := m.clock.Now()

	var invGen string
	if m.inventory != nil {
		invGen = m.inventory.SnapshotGeneration()
	}

	r := &Reservation{
		ReservationID:       resID,
		AllocationID:        req.AllocationID,
		SchemaVersion:       ReservationSchemaVersion,
		ResourceID:          req.ResourceID,
		ResourceType:        req.ResourceType,
		Owner:               req.Identity.Principal, // KPK-05: derived from Identity
		LeaseID:             leaseID,
		LeaseExpiry:         leaseExpiry,
		LastRenewedAt:       now,
		InventoryGeneration: invGen,
		Status:              StatusHeld,
		CreatedAt:           now,
		UpdatedAt:           now,
		IdempotencyKey:      req.IdempotencyKey,
		RequestFingerprint:  req.RequestFingerprint,
		Revision:            0,
	}

	// Persist atomically (still under flock).
	records[r.ReservationID] = r
	if err := m.store.rewriteAll(records); err != nil {
		return nil, fmt.Errorf("reservation: cannot persist: %w", err)
	}

	return &ReserveResult{Reservation: r, Created: true}, nil
}

// RenewLease extends the lease TTL for a reservation.
// KPK-05: Only the verified identity owner can renew.
// KPK-08 (v19.2.5): Rejects renewal if the lease has already expired.
func (m *Manager) RenewLease(reservationID string, identity Identity, newTTL time.Duration) (*Reservation, error) {
	if !m.policy.ValidateLeaseTTL(newTTL) {
		return nil, fmt.Errorf("reservation: lease TTL %v outside allowed range", newTTL)
	}

	var updated *Reservation
	err := m.store.Update(reservationID, func(r *Reservation) error {
		// KPK-05: Verify ownership via Identity.
		if r.Owner != identity.Principal {
			return ErrLeaseNotOwned
		}
		if r.Status != StatusHeld {
			return fmt.Errorf("reservation: cannot renew reservation in status %s", r.Status)
		}
		// KPK-08: Reject renewal if lease has already expired.
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

// Release transitions a reservation toward RELEASED.
// KPK-05: Only the verified identity owner can release.
// KPK-09 (v19.2.5): Uses transition table. Returns non-nil result.
// cleanupConfirmed must be true to reach RELEASED (KPK-01 pattern).
func (m *Manager) Release(reservationID string, identity Identity, cleanupConfirmed bool) (*Reservation, error) {
	var updated *Reservation
	err := m.store.Update(reservationID, func(r *Reservation) error {
		// KPK-05: Verify ownership.
		if r.Owner != identity.Principal && !identity.IsReconciler {
			return ErrLeaseNotOwned
		}

		now := m.clock.Now()

		// KPK-09: Use transition table for all state changes.
		if r.Status == StatusReleased {
			// Already released — idempotent. KPK-09: return non-nil.
			updated = r
			return nil
		}

		if r.Status == StatusHeld {
			// HELD → RELEASING (start cleanup).
			if err := r.transition(StatusReleasing, now, "release requested"); err != nil {
				return err
			}
		}

		if r.Status == StatusReleasing && cleanupConfirmed {
			// RELEASING → RELEASED (cleanup confirmed).
			if err := r.transition(StatusReleased, now, "cleanup confirmed"); err != nil {
				return err
			}
		} else if r.Status == StatusReleasing && !cleanupConfirmed {
			// Stay in RELEASING — cleanup not yet confirmed.
			// Resource still blocks (KPK-01).
		}

		updated = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// MarkExpiredCandidates finds reservations with expired leases and marks them
// as EXPIRED_PENDING_RECONCILIATION.
// KPK-14 (v19.2.5): Re-checks expiry inside the locked update callback.
// KPK-14 fix: Does NOT hold a separate flock — each Update call acquires its
// own flock, and the update callback re-checks expiry inside that lock.
func (m *Manager) MarkExpiredCandidates() (int, error) {
	// Load to find candidates (no flock — Update will re-validate under lock).
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
		// Quick pre-check (without lock) — may be stale, that's OK.
		if r.Status != StatusHeld || !r.IsExpired(now) {
			continue
		}
		// KPK-14: Update acquires its own flock and re-checks inside the callback.
		err := m.store.Update(r.ReservationID, func(r2 *Reservation) error {
			// Re-check: status must still be HELD and lease must still be expired.
			if r2.Status != StatusHeld {
				return nil // no longer HELD (was renewed or changed)
			}
			if !r2.IsExpired(m.clock.Now()) {
				return nil // no longer expired (was renewed)
			}
			// KPK-09: Use transition table.
			return r2.transition(StatusExpiredPendingReconciliation, m.clock.Now(), "lease expired")
		})
		if err != nil {
			return marked, err
		}
		// Check if the transition actually happened.
		updated, _ := m.store.Get(r.ReservationID)
		if updated != nil && updated.Status == StatusExpiredPendingReconciliation {
			marked++
		}
	}
	return marked, nil
}

// ReconcileExpired transitions EXPIRED_PENDING_RECONCILIATION reservations.
// KPK-10 (v19.2.5): Requires trusted CleanupEvidence from a reconciler Identity.
// Bare boolean is replaced with typed evidence containing resource ID, operation
// ID, observation time, and verified outcome.
func (m *Manager) ReconcileExpired(reservationID string, reconciler Identity, evidence CleanupEvidence) (*Reservation, error) {
	// KPK-10: Only trusted reconcilers can reconcile.
	if !reconciler.IsReconciler {
		return nil, fmt.Errorf("reservation: only trusted reconcilers can reconcile expired reservations")
	}

	// KPK-10: Validate evidence is non-empty and recent.
	if evidence.ResourceID == "" || evidence.OperationID == "" {
		return nil, fmt.Errorf("reservation: cleanup evidence must contain resource_id and operation_id")
	}
	if m.clock.Now().Sub(evidence.ObservedAt) > 5*time.Minute {
		return nil, fmt.Errorf("reservation: cleanup evidence is stale (observed %v ago)", m.clock.Now().Sub(evidence.ObservedAt))
	}

	var updated *Reservation
	err := m.store.Update(reservationID, func(r *Reservation) error {
		if r.Status != StatusExpiredPendingReconciliation {
			return fmt.Errorf("reservation: cannot reconcile from status %s", r.Status)
		}

		// KPK-10: Validate evidence belongs to this reservation's resource.
		if evidence.ResourceID != r.ResourceID {
			return fmt.Errorf("reservation: cleanup evidence resource_id %q does not match reservation resource_id %q",
				evidence.ResourceID, r.ResourceID)
		}

		now := m.clock.Now()
		if evidence.Verified {
			// Cleanup confirmed → RELEASED.
			if err := r.transition(StatusReleased, now, "reconciler confirmed cleanup: "+evidence.OperationID); err != nil {
				return err
			}
		} else {
			// Cleanup failed → CONFLICT (fail-closed).
			if err := r.transition(StatusConflict, now, "reconciler reported cleanup failure: "+evidence.OperationID); err != nil {
				return err
			}
		}
		updated = r
		return nil
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
