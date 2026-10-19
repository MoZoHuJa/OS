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

// Manager is the single domain entry point for all reservation operations.
// P1-01 (v19.2.9): All mutation methods accept a token string which is
// resolved to an Identity via TrustedIdentityProvider. Callers cannot
// forge identity.
type Manager struct {
	store      *Store
	policy     ReservationPolicy
	clock      Clock
	inventory  InventoryProvider
	allocVal   AllocationValidator
	cleanupVer CleanupVerifier
	idProv     TrustedIdentityProvider // P1-01 (v19.2.9): resolves tokens to identities
}

// NewManager creates a Manager.
// P1-01 (v19.2.9): idProv is mandatory — resolves tokens to verified identities.
func NewManager(store *Store, policy ReservationPolicy, clock Clock, inventory InventoryProvider, allocVal AllocationValidator, cleanupVer CleanupVerifier, idProv TrustedIdentityProvider) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("reservation: store is required")
	}
	if clock == nil {
		clock = RealClock{}
	}
	if inventory == nil {
		return nil, fmt.Errorf("reservation: inventory provider is required")
	}
	if allocVal == nil {
		return nil, fmt.Errorf("reservation: allocation validator is required")
	}
	if cleanupVer == nil {
		return nil, fmt.Errorf("reservation: cleanup verifier is required")
	}
	if idProv == nil {
		return nil, fmt.Errorf("reservation: identity provider is required")
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("reservation: invalid policy: %w", err)
	}
	return &Manager{store: store, policy: policy, clock: clock, inventory: inventory, allocVal: allocVal, cleanupVer: cleanupVer, idProv: idProv}, nil
}

func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// computeFingerprint computes a canonical SHA256 fingerprint from request fields.
func computeFingerprint(req ReserveRequest) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%v", req.AllocationID, req.ResourceID, req.ResourceType, req.LeaseTTL)
	return hex.EncodeToString(h.Sum(nil))
}

// validateInventorySnapshot validates a snapshot consistently.
// P2-01 (v19.2.9): Extracted helper — called on BOTH pre-lock and in-lock snapshots.
func (m *Manager) validateInventorySnapshot(snap InventorySnapshot) error {
	if snap.AsOf.IsZero() {
		return fmt.Errorf("inventory snapshot AsOf is zero")
	}
	if snap.Generation == "" {
		return fmt.Errorf("inventory snapshot Generation is empty")
	}
	now := m.clock.Now()
	if snap.AsOf.After(now.Add(1 * time.Second)) {
		return fmt.Errorf("inventory snapshot AsOf is in the future")
	}
	snapAge := now.Sub(snap.AsOf)
	if snapAge < 0 {
		return fmt.Errorf("inventory snapshot has negative age (clock skew?)")
	}
	if m.policy.IsInventoryStale(snapAge) {
		return ErrStaleInventory
	}
	return nil
}

// resolveIdentity resolves a token to a verified Identity.
// P1-01 (v19.2.9): Central resolution point — no caller can bypass.
func (m *Manager) resolveIdentity(token string) (Identity, error) {
	return m.idProv.ResolveIdentity(token)
}

// ReserveRequest contains parameters for creating a reservation.
type ReserveRequest struct {
	AllocationID   string
	ResourceID     string
	ResourceType   ResourceType
	LeaseTTL       time.Duration
	IdempotencyKey string
}

// ReserveResult is the outcome of a Reserve operation.
type ReserveResult struct {
	Reservation *Reservation
	Created     bool
}

// Reserve creates a reservation.
// P1-02 (v19.2.9): Uses BeginReservation/EndReservation for atomicity.
func (m *Manager) Reserve(req ReserveRequest, token string) (*ReserveResult, error) {
	identity, err := m.resolveIdentity(token)
	if err != nil {
		return nil, fmt.Errorf("reservation: identity resolution failed: %w", err)
	}

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
	canonID, err := CanonicalResourceID(req.ResourceID, req.ResourceType)
	if err != nil {
		return nil, fmt.Errorf("reservation: invalid resource_id: %w", err)
	}
	if req.LeaseTTL <= 0 {
		return nil, fmt.Errorf("reservation: lease TTL must be > 0")
	}
	if !m.policy.ValidateLeaseTTL(req.LeaseTTL) {
		return nil, fmt.Errorf("reservation: lease TTL %v outside range [%v, %v]",
			req.LeaseTTL, m.policy.LeaseMinTTL, m.policy.LeaseMaxTTL)
	}

	// P1-03/P2-01: First inventory snapshot — full validation.
	snap := m.inventory.GetSnapshot()
	if err := m.validateInventorySnapshot(snap); err != nil {
		return nil, fmt.Errorf("reservation: %w", err)
	}
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

	// P1-02 (v19.2.9): BeginReservation — atomically claim allocation.
	// This prevents the allocation from transitioning to terminal state.
	if err := m.allocVal.BeginReservation(req.AllocationID); err != nil {
		return nil, fmt.Errorf("reservation: cannot begin reservation for allocation: %w", err)
	}

	// Ensure EndReservation is called on all exit paths.
	reservationSuccess := false
	defer func() {
		if !reservationSuccess {
			_ = m.allocVal.EndReservation(req.AllocationID, false)
		}
	}()

	// P1-02: Validate allocation INSIDE flock.
	if err := m.allocVal.ValidateForReservation(req.AllocationID); err != nil {
		return nil, fmt.Errorf("reservation: allocation validation failed: %w", err)
	}

	// P2-01: Second inventory snapshot — SAME validation as first.
	snap2 := m.inventory.GetSnapshot()
	if err := m.validateInventorySnapshot(snap2); err != nil {
		return nil, fmt.Errorf("reservation: %w", err)
	}
	if snap.Generation != snap2.Generation {
		return nil, ErrStaleInventory
	}
	if !snap2.ResourceExists(canonID) {
		return nil, ErrResourceNotFound
	}

	records, corrupt, err := m.store.loadLocked()
	if err != nil {
		return nil, fmt.Errorf("reservation: cannot load: %w", err)
	}
	if len(corrupt) > 0 {
		return nil, fmt.Errorf("%w: %d corrupt lines", ErrReservationStoreCorrupt, len(corrupt))
	}

	// Idempotency check. Fingerprint canonical request fields so equivalent resource IDs
	// map to the same idempotency identity. Accept the legacy raw-input fingerprint
	// for reservations written by earlier versions.
	canonicalReq := req
	canonicalReq.ResourceID = canonID
	fingerprint := computeFingerprint(canonicalReq)
	legacyFingerprint := computeFingerprint(req)
	if req.IdempotencyKey != "" {
		for _, existing := range records {
			if existing.IdempotencyKey == req.IdempotencyKey && existing.Owner == identity.Principal() {
				if existing.RequestFingerprint != fingerprint && existing.RequestFingerprint != legacyFingerprint {
					return nil, ErrIdempotencyConflict
				}
				if existing.Status == StatusReleased {
					return nil, fmt.Errorf("reservation: idempotency key reused after release")
				}
				// The reservation already exists durably. Never roll its allocation
				// claim back if finalization fails; report the pending repair explicitly.
				reservationSuccess = true
				if err := m.allocVal.EndReservation(req.AllocationID, true); err != nil {
					return nil, fmt.Errorf("reservation: existing reservation %s is persisted but allocation finalization is pending: %w", existing.ReservationID, err)
				}
				return &ReserveResult{Reservation: existing, Created: false}, nil
			}
		}
	}

	allReservations := make([]*Reservation, 0, len(records))
	for _, r := range records {
		allReservations = append(allReservations, r)
	}

	switch req.ResourceType {
	case ResourceTypeGPUDevice:
		if !m.policy.CanReserveGPU(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrReservationConflict, ResourceID: canonID, Detail: "GPU already reserved"}
		}
	case ResourceTypeRuntimeSlot:
		if !m.policy.CanReserveRuntimeSlot(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrCapacityExceeded, ResourceID: canonID, Detail: "runtime slot capacity exceeded"}
		}
	case ResourceTypeModelSlot:
		if !m.policy.CanReserveModelSlot(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrCapacityExceeded, ResourceID: canonID, Detail: "model slot capacity exceeded"}
		}
	case ResourceTypePort:
		if !m.policy.CanReservePort(canonID, allReservations) {
			return nil, &ReservationError{Err: ErrReservationConflict, ResourceID: canonID, Detail: "port already reserved"}
		}
	default:
		return nil, fmt.Errorf("reservation: unsupported resource type %q", req.ResourceType)
	}

	now := m.clock.Now()
	r := &Reservation{
		ReservationID:       generateID(),
		AllocationID:        req.AllocationID,
		SchemaVersion:       ReservationSchemaVersion,
		ResourceID:          canonID,
		ResourceType:        req.ResourceType,
		Owner:               identity.Principal(),
		LeaseID:             generateID(),
		LeaseExpiry:         leaseExpiry,
		LastRenewedAt:       now,
		InventoryGeneration: snap2.Generation,
		Status:              StatusHeld,
		CreatedAt:           now,
		UpdatedAt:           now,
		IdempotencyKey:      req.IdempotencyKey,
		RequestFingerprint:  fingerprint,
		Revision:            0,
	}

	records[r.ReservationID] = r
	if err := m.store.rewriteAll(records); err != nil {
		return nil, fmt.Errorf("reservation: cannot persist: %w", err)
	}

	// The reservation is durably committed. From this point onward, a failure to
	// finalize the allocation is a recoverable reconciliation condition, NOT a
	// reason to roll the allocation claim back.
	reservationSuccess = true
	if err := m.allocVal.EndReservation(req.AllocationID, true); err != nil {
		return nil, fmt.Errorf("reservation: reservation %s is persisted but allocation finalization is pending: %w", r.ReservationID, err)
	}

	return &ReserveResult{Reservation: r, Created: true}, nil
}

// RenewLease extends the lease TTL.
func (m *Manager) RenewLease(reservationID, token string, newTTL time.Duration) (*Reservation, error) {
	identity, err := m.resolveIdentity(token)
	if err != nil {
		return nil, err
	}
	if !m.policy.ValidateLeaseTTL(newTTL) {
		return nil, fmt.Errorf("reservation: lease TTL %v outside range", newTTL)
	}
	var updated *Reservation
	err = m.store.update(reservationID, func(r *Reservation) error {
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

// RequestRelease starts the release process.
func (m *Manager) RequestRelease(reservationID, token string) (*Reservation, error) {
	identity, err := m.resolveIdentity(token)
	if err != nil {
		return nil, err
	}
	var updated *Reservation
	err = m.store.update(reservationID, func(r *Reservation) error {
		if r.Owner != identity.Principal() && !identity.IsReconciler() {
			return ErrLeaseNotOwned
		}
		now := m.clock.Now()
		switch r.Status {
		case StatusHeld, StatusExpiredPendingReconciliation, StatusConflict:
			if err := r.transition(StatusReleasing, now, "release requested"); err != nil {
				return err
			}
		case StatusReleasing, StatusReleased:
			updated = r
			return errNoChange
		default:
			return fmt.Errorf("reservation: cannot release from status %s", r.Status)
		}
		updated = r
		return nil
	})
	if err != nil && err != errNoChange {
		return nil, err
	}
	return updated, nil
}

// ConfirmRelease transitions RELEASING → RELEASED using CleanupVerifier.
// P1-01 (v19.2.9): Accepts token — resolved to Identity internally.
func (m *Manager) ConfirmRelease(reservationID, token string) (*Reservation, error) {
	reconciler, err := m.resolveIdentity(token)
	if err != nil {
		return nil, err
	}
	if !reconciler.IsReconciler() {
		return nil, fmt.Errorf("reservation: only trusted reconcilers can confirm release")
	}

	r, err := m.store.Get(reservationID)
	if err != nil {
		return nil, err
	}
	if r.Status == StatusReleased {
		return r, nil // P2-02: idempotent non-nil
	}
	if r.Status != StatusReleasing {
		return nil, fmt.Errorf("reservation: must be RELEASING (got %s)", r.Status)
	}

	evidence, err := m.cleanupVer.VerifyCleanup(r.ResourceID, reconciler)
	if err != nil {
		return nil, fmt.Errorf("reservation: cleanup verification failed: %w", err)
	}
	if evidence.ReconcilerID() != reconciler.Principal() {
		return nil, fmt.Errorf("reservation: evidence reconciler_id mismatch (internal)")
	}
	now := m.clock.Now()
	if evidence.ObservedAt().IsZero() || evidence.ObservedAt().After(now.Add(1*time.Second)) || now.Sub(evidence.ObservedAt()) > 5*time.Minute {
		return nil, fmt.Errorf("reservation: evidence timestamp invalid (internal)")
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
			// idempotent
		default:
			return fmt.Errorf("reservation: cannot confirm from status %s", r.Status)
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
			if r2.Status != StatusHeld || !r2.IsExpired(m.clock.Now()) {
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
func (m *Manager) ReconcileExpired(reservationID, token string) (*Reservation, error) {
	reconciler, err := m.resolveIdentity(token)
	if err != nil {
		return nil, err
	}
	if !reconciler.IsReconciler() {
		return nil, fmt.Errorf("reservation: only trusted reconcilers can reconcile")
	}

	r, err := m.store.Get(reservationID)
	if err != nil {
		return nil, err
	}
	if r.Status != StatusExpiredPendingReconciliation {
		return nil, fmt.Errorf("reservation: must be EXPIRED_PENDING_RECONCILIATION (got %s)", r.Status)
	}

	evidence, err := m.cleanupVer.VerifyCleanup(r.ResourceID, reconciler)
	if err != nil {
		return nil, fmt.Errorf("reservation: cleanup verification failed: %w", err)
	}
	if evidence.ReconcilerID() != reconciler.Principal() {
		return nil, fmt.Errorf("reservation: evidence reconciler_id mismatch (internal)")
	}
	now := m.clock.Now()
	if evidence.ObservedAt().IsZero() || evidence.ObservedAt().After(now.Add(1*time.Second)) || now.Sub(evidence.ObservedAt()) > 5*time.Minute {
		return nil, fmt.Errorf("reservation: evidence timestamp invalid (internal)")
	}

	var updated *Reservation
	err = m.store.update(reservationID, func(r *Reservation) error {
		if r.Status != StatusExpiredPendingReconciliation {
			return fmt.Errorf("reservation: cannot reconcile from %s", r.Status)
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

var errNoChange = fmt.Errorf("reservation: no change")
