package reservation

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"syscall"
	"time"
)

// Clock is an injectable clock for testing. Production uses RealClock.
type Clock interface {
	Now() time.Time
}

// RealClock returns the real wall-clock time.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

// FakeClock returns a fixed time. Used in tests.
type FakeClock struct {
	T time.Time
}

func (f *FakeClock) Now() time.Time { return f.T }

// Set advances the fake clock.
func (f *FakeClock) Set(t time.Time) { f.T = t }

// Advance moves the fake clock forward.
func (f *FakeClock) Advance(d time.Duration) { f.T = f.T.Add(d) }

// Manager is the single domain entry point for create/reserve/renew/release
// operations. All mutations go through the Manager.
//
// Per Phase 3 guide §7: "ReservationManager: revalidate inventory snapshot,
// acquire resource lock(s), check current reservations, enforce capacity/
// exclusivity, persist reservation + lease, transition to RESERVED."
//
// Phase 3 does NOT start/stop runtimes or call Docker. It only manages
// resource ownership and leases.
type Manager struct {
	store  *Store
	policy ReservationPolicy
	clock  Clock
}

// NewManager creates a Manager with the given store and policy.
func NewManager(store *Store, policy ReservationPolicy, clock Clock) *Manager {
	if clock == nil {
		clock = RealClock{}
	}
	return &Manager{store: store, policy: policy, clock: clock}
}

// generateID generates a random hex ID (16 bytes = 32 hex chars).
func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use timestamp (should not happen on Linux with /dev/urandom).
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ReserveRequest contains the parameters for creating a reservation.
type ReserveRequest struct {
	AllocationID   string
	ResourceID     string
	ResourceType   ResourceType
	Owner          string
	LeaseTTL       time.Duration
	IdempotencyKey string
	// RequestFingerprint is a normalized hash of the request parameters.
	// Used to detect idempotency key reuse with different content.
	RequestFingerprint string
	// InventoryGeneration is the snapshot generation from inventory.
	// Used to detect stale inventory.
	InventoryGeneration string
	// InventorySnapshotAge is how old the inventory snapshot is.
	// If zero, the freshness check is skipped.
	InventorySnapshotAge time.Duration
}

// ReserveResult is the outcome of a Reserve operation.
type ReserveResult struct {
	Reservation *Reservation
	Created     bool // true if newly created, false if returned existing (idempotent)
}

// Reserve creates a reservation for the given request.
//
// Algorithm (per Phase 3 guide §9):
// 1. Validate request.
// 2. Check idempotency key — if exists with same fingerprint, return existing.
// 3. Check inventory freshness.
// 4. Check policy (capacity, exclusivity) — UNDER FLOCK to prevent races.
// 5. Create reservation + lease.
// 6. Persist atomically — UNDER THE SAME FLOCK.
// 7. Return result.
//
// The entire check-then-write cycle is serialized via the store's flock
// to prevent concurrent reservations for the same exclusive resource.
//
// Does NOT start runtimes or call Docker (Phase 5 scope).
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
	if req.Owner == "" {
		return nil, fmt.Errorf("reservation: owner is required")
	}

	// 3. Inventory freshness check (before locking — read-only check).
	if req.InventorySnapshotAge > 0 && m.policy.IsInventoryStale(req.InventorySnapshotAge) {
		return nil, ErrStaleInventory
	}

	// Validate lease TTL if provided (before locking — pure check).
	var leaseExpiry time.Time
	if req.LeaseTTL > 0 {
		if !m.policy.ValidateLeaseTTL(req.LeaseTTL) {
			return nil, fmt.Errorf("reservation: lease TTL %v outside allowed range [%v, %v]",
				req.LeaseTTL, m.policy.LeaseMinTTL, m.policy.LeaseMaxTTL)
		}
		leaseExpiry = m.clock.Now().Add(req.LeaseTTL)
	}

	// The entire idempotency-check + policy-check + persist cycle must be
	// serialized. We acquire the store's flock for the full duration.
	// This prevents two concurrent goroutines from both passing the policy
	// check and both appending.
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

	// 2. Idempotency check (under flock).
	if req.IdempotencyKey != "" {
		for _, existing := range records {
			if existing.IdempotencyKey == req.IdempotencyKey && existing.Owner == req.Owner {
				if existing.RequestFingerprint != req.RequestFingerprint {
					return nil, ErrIdempotencyConflict
				}
				return &ReserveResult{Reservation: existing, Created: false}, nil
			}
		}
	}

	// 4. Policy check (under flock — this is the critical section).
	activeReservations := make([]*Reservation, 0, len(records))
	for _, r := range records {
		if r.Status == StatusHeld {
			activeReservations = append(activeReservations, r)
		}
	}

	switch req.ResourceType {
	case ResourceTypeGPUDevice:
		if !m.policy.CanReserveGPU(req.ResourceID, activeReservations, req.AllocationID) {
			return nil, &ReservationError{
				Err: ErrReservationConflict, ResourceID: req.ResourceID,
				Owner: req.Owner, Detail: "GPU already exclusively reserved",
			}
		}
	case ResourceTypeRuntimeSlot:
		if !m.policy.CanReserveRuntimeSlot(req.ResourceID, activeReservations, req.AllocationID) {
			return nil, &ReservationError{
				Err: ErrCapacityExceeded, ResourceID: req.ResourceID,
				Owner: req.Owner, Detail: "runtime slot capacity exceeded",
			}
		}
	case ResourceTypeModelSlot:
		if !m.policy.CanReserveModelSlot(req.ResourceID, activeReservations, req.AllocationID) {
			return nil, &ReservationError{
				Err: ErrCapacityExceeded, ResourceID: req.ResourceID,
				Owner: req.Owner, Detail: "model slot capacity exceeded",
			}
		}
	}

	// 5. Create reservation.
	resID := generateID()
	leaseID := ""
	if req.LeaseTTL > 0 {
		leaseID = generateID()
	}
	now := m.clock.Now()

	r := &Reservation{
		ReservationID:       resID,
		AllocationID:        req.AllocationID,
		SchemaVersion:       ReservationSchemaVersion,
		ResourceID:          req.ResourceID,
		ResourceType:        req.ResourceType,
		Owner:               req.Owner,
		LeaseID:             leaseID,
		LeaseExpiry:         leaseExpiry,
		LastRenewedAt:       now,
		InventoryGeneration: req.InventoryGeneration,
		Status:              StatusHeld,
		CreatedAt:           now,
		UpdatedAt:           now,
		IdempotencyKey:      req.IdempotencyKey,
		RequestFingerprint:  req.RequestFingerprint,
		Revision:            0,
	}

	// 6. Persist (still under flock — append will re-acquire, but flock is
	// reentrant for the same process via the same fd... actually it's NOT
	// reentrant in Go's syscall.Flock. So we write directly instead of
	// calling store.Append which would try to acquire the flock again.)
	// We use store.rewriteAll after adding the new record to the map.
	records[r.ReservationID] = r
	if err := m.store.rewriteAll(records); err != nil {
		return nil, fmt.Errorf("reservation: cannot persist: %w", err)
	}

	return &ReserveResult{Reservation: r, Created: true}, nil
}

// RenewLease extends the lease TTL for a reservation.
// Only the owner can renew.
func (m *Manager) RenewLease(reservationID, owner string, newTTL time.Duration) (*Reservation, error) {
	if !m.policy.ValidateLeaseTTL(newTTL) {
		return nil, fmt.Errorf("reservation: lease TTL %v outside allowed range", newTTL)
	}

	var updated *Reservation
	err := m.store.Update(reservationID, func(r *Reservation) error {
		if r.Owner != owner {
			return ErrLeaseNotOwned
		}
		if r.Status != StatusHeld {
			return fmt.Errorf("reservation: cannot renew reservation in status %s", r.Status)
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

// Release transitions a reservation to RELEASING then RELEASED.
// Only the owner can release.
// cleanupConfirmed must be true to reach RELEASED (KPK-01 pattern).
func (m *Manager) Release(reservationID, owner string, cleanupConfirmed bool) (*Reservation, error) {
	var updated *Reservation
	err := m.store.Update(reservationID, func(r *Reservation) error {
		if r.Owner != owner {
			return ErrLeaseNotOwned
		}
		if r.Status == StatusReleased {
			return nil // already released (idempotent)
		}
		if r.Status != StatusHeld && r.Status != StatusReleasing {
			return fmt.Errorf("reservation: cannot release from status %s", r.Status)
		}

		// If already RELEASING, only transition to RELEASED if cleanup confirmed.
		if r.Status == StatusReleasing {
			if !cleanupConfirmed {
				return nil // already releasing, no change (idempotent)
			}
			r.Status = StatusReleased
			r.UpdatedAt = m.clock.Now()
			updated = r
			return nil
		}

		// Status is HELD — transition to RELEASING.
		r.Status = StatusReleasing
		r.UpdatedAt = m.clock.Now()

		// If cleanup confirmed, transition to RELEASED.
		if cleanupConfirmed {
			r.Status = StatusReleased
			r.UpdatedAt = m.clock.Now()
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
// as EXPIRED_PENDING_RECONCILIATION. Does NOT release the resource — that
// requires ReconcileExpired to confirm the resource is actually free.
//
// Per Phase 3 guide §11.6: "Expirácia lease znamená 'vyžaduje sa kontrola',
// nie 'zdroj je určite voľný'."
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
		if r.Status == StatusHeld && r.IsExpired(now) {
			if err := m.store.Update(r.ReservationID, func(r2 *Reservation) error {
				r2.Status = StatusExpiredPendingReconciliation
				r2.UpdatedAt = m.clock.Now()
				return nil
			}); err != nil {
				return marked, err
			}
			marked++
		}
	}
	return marked, nil
}

// ReconcileExpired transitions EXPIRED_PENDING_RECONCILIATION reservations.
// If cleanupConfirmed is true, the reservation is released.
// If false, it is marked CONFLICT (fail-closed).
func (m *Manager) ReconcileExpired(reservationID string, cleanupConfirmed bool) (*Reservation, error) {
	var updated *Reservation
	err := m.store.Update(reservationID, func(r *Reservation) error {
		if r.Status != StatusExpiredPendingReconciliation {
			return fmt.Errorf("reservation: cannot reconcile from status %s", r.Status)
		}
		if cleanupConfirmed {
			r.Status = StatusReleased
		} else {
			r.Status = StatusConflict
		}
		r.UpdatedAt = m.clock.Now()
		updated = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// getActiveReservations returns all HELD reservations.
func (m *Manager) getActiveReservations() ([]*Reservation, error) {
	return m.store.ListByStatus(StatusHeld)
}

// Get retrieves a reservation by ID.
func (m *Manager) Get(id string) (*Reservation, error) {
	return m.store.Get(id)
}

// ListByAllocation returns all reservations for an allocation.
func (m *Manager) ListByAllocation(allocID string) ([]*Reservation, error) {
	return m.store.ListByAllocation(allocID)
}
