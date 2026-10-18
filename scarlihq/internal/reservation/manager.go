package reservation

import (
	"crypto/rand"
	"crypto/sha256"
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

// Manager is the single domain entry point for reserve/renew/release operations.
// P1-01 (v19.2.7): Store mutation methods are package-private; all mutations
// go through Manager. Identity fields are unexported (caller-forgeable
// boolean removed).
type Manager struct {
	store      *Store
	policy     ReservationPolicy
	clock      Clock
	inventory  InventoryProvider
	allocVal   AllocationValidator
	cleanupVer CleanupVerifier // P1-01 (v19.2.8): trusted cleanup verifier
}

// NewManager creates a Manager.
// P1-01 (v19.2.8): cleanupVer is mandatory for ConfirmRelease/ReconcileExpired.
func NewManager(store *Store, policy ReservationPolicy, clock Clock, inventory InventoryProvider, allocVal AllocationValidator, cleanupVer CleanupVerifier) (*Manager, error) {
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
	if cleanupVer == nil {
		return nil, fmt.Errorf("reservation: cleanup verifier is required (nil rejected)")
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("reservation: invalid policy: %w", err)
	}
	return &Manager{store: store, policy: policy, clock: clock, inventory: inventory, allocVal: allocVal, cleanupVer: cleanupVer}, nil
}

func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// computeFingerprint computes a canonical SHA256 fingerprint from request fields.
// P1-04 (v19.2.7): Computed INSIDE the manager — caller cannot supply a fake.
func computeFingerprint(req ReserveRequest) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%v", req.AllocationID, req.ResourceID, req.ResourceType, req.Identity.Principal(), req.LeaseTTL)
	return hex.EncodeToString(h.Sum(nil))
}

// ReserveRequest contains parameters for creating a reservation.
type ReserveRequest struct {
	AllocationID   string
	ResourceID     string
	ResourceType   ResourceType
	Identity       Identity
	LeaseTTL       time.Duration
	IdempotencyKey string
	// RequestFingerprint is now IGNORED — computed internally (P1-04).
	// Kept for backward compat but not trusted.
	RequestFingerprint string
}

// ReserveResult is the outcome of a Reserve operation.
type ReserveResult struct {
	Reservation *Reservation
	Created     bool
}

// Reserve creates a reservation.
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
	if req.Identity.Principal() == "" {
		return nil, fmt.Errorf("reservation: identity is required")
	}

	// P1-05: Canonicalize resource ID.
	canonID, err := CanonicalResourceID(req.ResourceID, req.ResourceType)
	if err != nil {
		return nil, fmt.Errorf("reservation: invalid resource_id: %w", err)
	}

	// KPK-06: If idempotency key is provided, fingerprint is computed internally.
	if req.IdempotencyKey != "" {
		// P1-04: fingerprint is computed by manager — caller cannot fake it.
		// RequestFingerprint field is ignored.
	}

	// KPK-07: LeaseTTL must be bounded.
	if req.LeaseTTL <= 0 {
		return nil, fmt.Errorf("reservation: lease TTL must be > 0 (got %v)", req.LeaseTTL)
	}
	if !m.policy.ValidateLeaseTTL(req.LeaseTTL) {
		return nil, fmt.Errorf("reservation: lease TTL %v outside allowed range [%v, %v]",
			req.LeaseTTL, m.policy.LeaseMinTTL, m.policy.LeaseMaxTTL)
	}

	// P1-03: Get one coherent inventory snapshot.
	snap := m.inventory.GetSnapshot()

	// P2-01 (v19.2.8): Reject future-dated/zero AsOf snapshots.
	if snap.AsOf.IsZero() {
		return nil, fmt.Errorf("reservation: inventory snapshot AsOf is zero")
	}
	if snap.Generation == "" {
		return nil, fmt.Errorf("reservation: inventory snapshot Generation is empty")
	}
	now := m.clock.Now()
	if snap.AsOf.After(now.Add(1 * time.Second)) {
		return nil, fmt.Errorf("reservation: inventory snapshot AsOf is in the future")
	}

	// P1-03: Check freshness from snapshot.
	snapAge := now.Sub(snap.AsOf)
	// P2-01 (v19.2.8): Reject negative age (future timestamp already rejected above,
	// but this is defense-in-depth against clock skew).
	if snapAge < 0 {
		return nil, fmt.Errorf("reservation: inventory snapshot has negative age (clock skew?)")
	}
	if m.policy.IsInventoryStale(snapAge) {
		return nil, ErrStaleInventory
	}
	// P1-03: Validate resource existence from snapshot.
	if !snap.ResourceExists(canonID) {
		return nil, ErrResourceNotFound
	}

	leaseExpiry := m.clock.Now().Add(req.LeaseTTL)

	// Acquire flock.
	lockFile, err := m.store.acquireFlock()
	if err != nil {
		return nil, fmt.Errorf("reservation: cannot acquire flock: %w", err)
	}
	defer func() {
		syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
	}()

	// P1-02: Revalidate allocation INSIDE the flock (was: before lock).
	if err := m.allocVal.ValidateForReservation(req.AllocationID); err != nil {
		return nil, fmt.Errorf("reservation: allocation validation failed: %w", err)
	}

	// P1-03: Revalidate inventory inside the critical section.
	snap2 := m.inventory.GetSnapshot()
	snapAge2 := m.clock.Now().Sub(snap2.AsOf)
	if m.policy.IsInventoryStale(snapAge2) {
		return nil, ErrStaleInventory
	}
	if !snap2.ResourceExists(canonID) {
		return nil, ErrResourceNotFound
	}
	// P1-03: Check generation didn't change between pre-lock and in-lock.
	if snap.Generation != snap2.Generation {
		return nil, ErrStaleInventory
	}

	records, corrupt, err := m.store.loadLocked()
	if err != nil {
		return nil, fmt.Errorf("reservation: cannot load: %w", err)
	}
	if len(corrupt) > 0 {
		return nil, fmt.Errorf("%w: %d corrupt lines", ErrReservationStoreCorrupt, len(corrupt))
	}

	// P1-04: Compute fingerprint internally.
	fingerprint := computeFingerprint(req)

	// KPK-06: Idempotency check (under flock).
	if req.IdempotencyKey != "" {
		for _, existing := range records {
			if existing.IdempotencyKey == req.IdempotencyKey && existing.Owner == req.Identity.Principal() {
				// P1-04: Compare computed fingerprint.
				if existing.RequestFingerprint != fingerprint {
					return nil, ErrIdempotencyConflict
				}
				// P1-04: Terminal replay — if released, don't return as active.
				if existing.Status == StatusReleased {
					return nil, fmt.Errorf("reservation: idempotency key reused after release — create a new reservation")
				}
				return &ReserveResult{Reservation: existing, Created: false}, nil
			}
		}
	}

	// Build all reservations list.
	allReservations := make([]*Reservation, 0, len(records))
	for _, r := range records {
		allReservations = append(allReservations, r)
	}

	// Policy check — NO excludeAllocationID (P1-05 from v19.2.6).
	switch req.ResourceType {
	case ResourceTypeGPUDevice:
		if !m.policy.CanReserveGPU(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrReservationConflict, ResourceID: canonID, Owner: req.Identity.Principal(), Detail: "GPU already reserved"}
		}
	case ResourceTypeRuntimeSlot:
		if !m.policy.CanReserveRuntimeSlot(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrCapacityExceeded, ResourceID: canonID, Owner: req.Identity.Principal(), Detail: "runtime slot capacity exceeded"}
		}
	case ResourceTypeModelSlot:
		if !m.policy.CanReserveModelSlot(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrCapacityExceeded, ResourceID: canonID, Owner: req.Identity.Principal(), Detail: "model slot capacity exceeded"}
		}
	case ResourceTypePort:
		if !m.policy.CanReservePort(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrReservationConflict, ResourceID: canonID, Owner: req.Identity.Principal(), Detail: "port already reserved"}
		}
	default:
		return nil, fmt.Errorf("reservation: unsupported resource type %q", req.ResourceType)
	}

	// Create reservation.
	resID := generateID()
	leaseID := generateID()
	r := &Reservation{
		ReservationID:       resID,
		AllocationID:        req.AllocationID,
		SchemaVersion:       ReservationSchemaVersion,
		ResourceID:          canonID,
		ResourceType:        req.ResourceType,
		Owner:               req.Identity.Principal(),
		LeaseID:             leaseID,
		LeaseExpiry:         leaseExpiry,
		LastRenewedAt:       m.clock.Now(),
		InventoryGeneration: snap2.Generation, // P1-03: exact snapshot generation
		Status:              StatusHeld,
		CreatedAt:           m.clock.Now(),
		UpdatedAt:           m.clock.Now(),
		IdempotencyKey:      req.IdempotencyKey,
		RequestFingerprint:  fingerprint, // P1-04: internally computed
		Revision:            0,
	}

	// Persist.
	records[r.ReservationID] = r
	if err := m.store.rewriteAll(records); err != nil {
		return nil, fmt.Errorf("reservation: cannot persist: %w", err)
	}

	return &ReserveResult{Reservation: r, Created: true}, nil
}

// RenewLease extends the lease TTL.
func (m *Manager) RenewLease(reservationID string, identity Identity, newTTL time.Duration) (*Reservation, error) {
	if !m.policy.ValidateLeaseTTL(newTTL) {
		return nil, fmt.Errorf("reservation: lease TTL %v outside allowed range", newTTL)
	}
	var updated *Reservation
	err := m.store.update(reservationID, func(r *Reservation) error {
		if r.Owner != identity.Principal() {
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
// P2-04 (v19.2.7): No-op calls (already RELEASING/RELEASED) skip revision increment.
func (m *Manager) RequestRelease(reservationID string, identity Identity) (*Reservation, error) {
	var updated *Reservation
	err := m.store.update(reservationID, func(r *Reservation) error {
		if r.Owner != identity.Principal() && !identity.IsReconciler() {
			return ErrLeaseNotOwned
		}

		now := m.clock.Now()

		// P2-04: Track whether state actually changed.
		switch r.Status {
		case StatusHeld, StatusExpiredPendingReconciliation, StatusConflict:
			if err := r.transition(StatusReleasing, now, "release requested"); err != nil {
				return err
			}
			updated = r
		case StatusReleasing, StatusReleased:
			// P2-04: No-op — don't increment revision or rewrite.
			updated = r
			return errNoChange
		default:
			return fmt.Errorf("reservation: cannot release from status %s", r.Status)
		}
		return nil
	})
	if err != nil && err != errNoChange {
		return nil, err
	}
	return updated, nil
}

// ConfirmRelease transitions RELEASING → RELEASED using verified CleanupEvidence.
// P1-01 (v19.2.8): Evidence is created by CleanupVerifier (injected), NOT by caller.
// The caller passes the reconciler Identity; the Manager calls cleanupVer.VerifyCleanup()
// to independently verify the resource is free.
// P2-02 (v19.2.8): Idempotent repeat (already RELEASED) returns the existing record, not nil.
func (m *Manager) ConfirmRelease(reservationID string, reconciler Identity) (*Reservation, error) {
	if reconciler.Principal() == "" || !reconciler.IsReconciler() {
		return nil, fmt.Errorf("reservation: only trusted reconcilers can confirm release")
	}

	// Read the reservation to get the resource ID for cleanup verification.
	r, err := m.store.Get(reservationID)
	if err != nil {
		return nil, err
	}
	if r.Status != StatusReleasing && r.Status != StatusReleased {
		return nil, fmt.Errorf("reservation: cannot confirm release from status %s (must be RELEASING)", r.Status)
	}

	// If already released, return the existing record (P2-02: non-nil idempotent).
	if r.Status == StatusReleased {
		return r, nil
	}

	// P1-01 (v19.2.8): Use CleanupVerifier to independently verify cleanup.
	// Evidence is created by the trusted verifier, NOT by the caller.
	evidence, err := m.cleanupVer.VerifyCleanup(r.ResourceID, reconciler)
	if err != nil {
		return nil, fmt.Errorf("reservation: cleanup verification failed: %w", err)
	}

	// Validate evidence is from this reconciler.
	if evidence.ReconcilerID() != reconciler.Principal() {
		return nil, fmt.Errorf("reservation: evidence reconciler_id mismatch (internal error)")
	}
	now := m.clock.Now()
	if evidence.ObservedAt().IsZero() {
		return nil, fmt.Errorf("reservation: evidence observed_at is zero (internal error)")
	}
	if evidence.ObservedAt().After(now.Add(1 * time.Second)) {
		return nil, fmt.Errorf("reservation: evidence observed_at is in the future (internal error)")
	}
	if now.Sub(evidence.ObservedAt()) > 5*time.Minute {
		return nil, fmt.Errorf("reservation: evidence is stale (internal error)")
	}

	var updated *Reservation
	err = m.store.update(reservationID, func(r *Reservation) error {
		if evidence.ResourceID() != r.ResourceID {
			return fmt.Errorf("reservation: evidence resource_id mismatch")
		}
		now := m.clock.Now()
		switch r.Status {
		case StatusReleasing:
			if evidence.Verified() {
				if err := r.transition(StatusReleased, now, "cleanup confirmed: "+evidence.OperationID()); err != nil {
					return err
				}
			} else {
				if err := r.transition(StatusConflict, now, "cleanup failed: "+evidence.OperationID()); err != nil {
					return err
				}
			}
		case StatusReleased:
			// P2-02 (v19.2.8): Already released — return existing record (non-nil).
		default:
			return fmt.Errorf("reservation: cannot confirm release from status %s", r.Status)
		}
		// P2-02: Always set updated = r (including idempotent path).
		updated = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// MarkExpiredCandidates marks expired reservations.
// P2-02 (v19.2.6): Accurate count via 'changed' flag.
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
// P1-01 (v19.2.8): Uses CleanupVerifier (injected) to independently verify cleanup.
// Caller passes reconciler Identity only; evidence is created by the verifier.
func (m *Manager) ReconcileExpired(reservationID string, reconciler Identity) (*Reservation, error) {
	if reconciler.Principal() == "" || !reconciler.IsReconciler() {
		return nil, fmt.Errorf("reservation: only trusted reconcilers can reconcile")
	}

	// Read reservation to get resource ID.
	r, err := m.store.Get(reservationID)
	if err != nil {
		return nil, err
	}
	if r.Status != StatusExpiredPendingReconciliation {
		return nil, fmt.Errorf("reservation: cannot reconcile from status %s", r.Status)
	}

	// P1-01 (v19.2.8): Use CleanupVerifier to independently verify cleanup.
	evidence, err := m.cleanupVer.VerifyCleanup(r.ResourceID, reconciler)
	if err != nil {
		return nil, fmt.Errorf("reservation: cleanup verification failed: %w", err)
	}
	if evidence.ReconcilerID() != reconciler.Principal() {
		return nil, fmt.Errorf("reservation: evidence reconciler_id mismatch (internal error)")
	}
	now := m.clock.Now()
	if evidence.ObservedAt().IsZero() {
		return nil, fmt.Errorf("reservation: evidence observed_at is zero (internal error)")
	}
	if evidence.ObservedAt().After(now.Add(1 * time.Second)) {
		return nil, fmt.Errorf("reservation: evidence observed_at is in the future (internal error)")
	}
	if now.Sub(evidence.ObservedAt()) > 5*time.Minute {
		return nil, fmt.Errorf("reservation: evidence is stale (internal error)")
	}

	var updated *Reservation
	err = m.store.update(reservationID, func(r *Reservation) error {
		if r.Status != StatusExpiredPendingReconciliation {
			return fmt.Errorf("reservation: cannot reconcile from status %s", r.Status)
		}
		if evidence.ResourceID() != r.ResourceID {
			return fmt.Errorf("reservation: evidence resource_id mismatch")
		}
		now := m.clock.Now()
		if evidence.Verified() {
			if err := r.transition(StatusReleased, now, "reconciler confirmed: "+evidence.OperationID()); err != nil {
				return err
			}
		} else {
			if err := r.transition(StatusConflict, now, "reconciler reported failure: "+evidence.OperationID()); err != nil {
				return err
			}
		}
		// P2-01: Always set updated = r after transition.
		updated = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (m *Manager) Get(id string) (*Reservation, error) {
	return m.store.Get(id)
}

func (m *Manager) ListByAllocation(allocID string) ([]*Reservation, error) {
	return m.store.ListByAllocation(allocID)
}

// errNoChange is a sentinel error indicating a no-op update (P2-04).
// Store.update recognizes this and skips revision increment + rewrite.
var errNoChange = fmt.Errorf("reservation: no change")
