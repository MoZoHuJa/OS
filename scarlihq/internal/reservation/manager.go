package reservation

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
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
	resourceLock *ResourceLock
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
	resourceLock, err := NewResourceLock(store)
	if err != nil {
		return nil, err
	}
	return &Manager{store: store, resourceLock: resourceLock, policy: policy, clock: clock, inventory: inventory, allocVal: allocVal, cleanupVer: cleanupVer, idProv: idProv}, nil
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

// Reserve creates one reservation. It remains source-compatible with the original API;
// use ReserveBundle when one allocation must atomically claim multiple resources.
func (m *Manager) Reserve(req ReserveRequest, token string) (*ReserveResult, error) {
	bundle, err := m.ReserveBundle([]ReserveRequest{req}, token)
	if bundle == nil {
		return nil, err
	}
	if len(bundle.Reservations) != 1 {
		return nil, fmt.Errorf("reservation: internal error: single-resource call returned %d records", len(bundle.Reservations))
	}
	return &ReserveResult{Reservation: bundle.Reservations[0], Created: bundle.Created}, err
}

// ReserveBundleResult is the outcome of an atomic multi-resource reservation.
type ReserveBundleResult struct {
	Reservations []*Reservation
	Created      bool
}

type normalizedRequest struct {
	request   ReserveRequest
	canonical string
}

// ReserveBundle reserves all resources for one allocation under one global
// reservation lock and one atomic reservation-store rewrite. The allocation
// claim is made only after idempotency has been checked, so a replay does not
// attempt to claim an allocation that is already RESERVED.
//
// A successful durable reservation is never rolled back merely because the
// allocation-store finalization failed. In that case the result is returned
// with ErrReconciliationRequired and can be repaired by ReconcilePending.
func (m *Manager) ReserveBundle(requests []ReserveRequest, token string) (result *ReserveBundleResult, retErr error) {
	identity, err := m.resolveIdentity(token)
	if err != nil {
		return nil, fmt.Errorf("reservation: identity resolution failed: %w", err)
	}
	normalized, err := normalizeRequests(requests, m.policy)
	if err != nil {
		return nil, err
	}
	allocationID := normalized[0].request.AllocationID
	idempotencyKey := normalized[0].request.IdempotencyKey
	fingerprint := bundleFingerprint(normalized)

	snap := m.inventory.GetSnapshot()
	if err := m.validateInventorySnapshot(snap); err != nil {
		return nil, fmt.Errorf("reservation: %w", err)
	}
	for _, item := range normalized {
		if !snap.ResourceExists(item.canonical) {
			return nil, fmt.Errorf("%w: %s", ErrResourceNotFound, item.canonical)
		}
	}

	unlock, err := m.resourceLock.Lock()
	if err != nil {
		return nil, fmt.Errorf("reservation: cannot acquire resource lock: %w", err)
	}
	defer unlock()

	records, corrupt, err := m.store.loadLocked()
	if err != nil {
		return nil, fmt.Errorf("reservation: cannot load store: %w", err)
	}
	if len(corrupt) > 0 {
		return nil, fmt.Errorf("%w: %d corrupt lines", ErrReservationStoreCorrupt, len(corrupt))
	}

	// Idempotency lookup precedes BeginReservation. Existing bundle records are
	// validated as a set, not just by finding one matching line.
	if idempotencyKey != "" {
		existing := make([]*Reservation, 0, len(normalized))
		for _, record := range records {
			if record.IdempotencyKey == idempotencyKey && record.Owner == identity.Principal() {
				existing = append(existing, record)
			}
		}
		if len(existing) > 0 {
			if err := validateIdempotentBundle(existing, normalized, identity.Principal(), allocationID, fingerprint); err != nil {
				return nil, err
			}
			for _, record := range existing {
				if record.Status == StatusReleased {
					return nil, fmt.Errorf("%w: idempotency key cannot be reused after release", ErrIdempotencyConflict)
				}
			}
			sortReservations(existing)
			if err := m.finalizeAllocation(allocationID, existing); err != nil {
				return &ReserveBundleResult{Reservations: existing, Created: false},
					fmt.Errorf("%w: existing reservation bundle is persisted but allocation finalization is pending: %w", ErrReconciliationRequired, err)
			}
			return &ReserveBundleResult{Reservations: existing, Created: false}, nil
		}
	}

	// Revalidate inventory while holding the cross-process reservation lock.
	snap2 := m.inventory.GetSnapshot()
	if err := m.validateInventorySnapshot(snap2); err != nil {
		return nil, fmt.Errorf("reservation: %w", err)
	}
	if snap.Generation != snap2.Generation {
		return nil, ErrStaleInventory
	}
	for _, item := range normalized {
		if !snap2.ResourceExists(item.canonical) {
			return nil, fmt.Errorf("%w: %s", ErrResourceNotFound, item.canonical)
		}
	}

	if err := m.allocVal.BeginReservation(allocationID); err != nil {
		return nil, fmt.Errorf("reservation: cannot begin reservation for allocation: %w", err)
	}
	claimOpen := true
	committed := false
	defer func() {
		if claimOpen && !committed {
			if rollbackErr := m.allocVal.EndReservation(allocationID, false); rollbackErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("reservation: allocation rollback/failure transition failed: %w", rollbackErr))
			}
		}
	}()

	if err := m.allocVal.ValidateForReservation(allocationID); err != nil {
		return nil, fmt.Errorf("reservation: allocation validation failed: %w", err)
	}

	now := m.clock.Now().UTC()
	leaseExpiry := now.Add(normalized[0].request.LeaseTTL)
	allReservations := make([]*Reservation, 0, len(records)+len(normalized))
	for _, record := range records {
		allReservations = append(allReservations, record)
	}
	created := make([]*Reservation, 0, len(normalized))
	for _, item := range normalized {
		switch item.request.ResourceType {
		case ResourceTypeGPUDevice:
			if !m.policy.CanReserveGPU(item.canonical, allReservations) {
				return nil, &ReservationError{Err: ErrReservationConflict, ResourceID: item.canonical, Owner: identity.Principal(), Detail: "GPU already reserved"}
			}
		case ResourceTypeRuntimeSlot:
			if !m.policy.CanReserveRuntimeSlot(item.canonical, allReservations) {
				return nil, &ReservationError{Err: ErrCapacityExceeded, ResourceID: item.canonical, Owner: identity.Principal(), Detail: "runtime slot capacity exceeded"}
			}
		case ResourceTypeModelSlot:
			if !m.policy.CanReserveModelSlot(item.canonical, allReservations) {
				return nil, &ReservationError{Err: ErrCapacityExceeded, ResourceID: item.canonical, Owner: identity.Principal(), Detail: "model slot capacity exceeded"}
			}
		case ResourceTypePort:
			if !m.policy.CanReservePort(item.canonical, allReservations) {
				return nil, &ReservationError{Err: ErrReservationConflict, ResourceID: item.canonical, Owner: identity.Principal(), Detail: "port already reserved"}
			}
		default:
			return nil, fmt.Errorf("reservation: unsupported resource type %q", item.request.ResourceType)
		}

		record := &Reservation{
			ReservationID:       generateID(),
			AllocationID:        allocationID,
			SchemaVersion:       ReservationSchemaVersion,
			ResourceID:          item.canonical,
			ResourceType:        item.request.ResourceType,
			Owner:               identity.Principal(),
			LeaseID:             generateID(),
			LeaseExpiry:         leaseExpiry,
			LastRenewedAt:       now,
			InventoryGeneration: snap2.Generation,
			Status:              StatusHeld,
			CreatedAt:            now,
			UpdatedAt:            now,
			IdempotencyKey:       idempotencyKey,
			RequestFingerprint:   fingerprint,
			BundleSize:           len(normalized),
			Revision:             0,
		}
		created = append(created, record)
		allReservations = append(allReservations, record)
	}

	for _, record := range created {
		records[record.ReservationID] = record
	}
	if err := m.store.rewriteAll(records); err != nil {
		return nil, fmt.Errorf("%w: cannot persist reservation bundle: %w", ErrPersistence, err)
	}

	committed = true
	claimOpen = false
	sortReservations(created)
	result = &ReserveBundleResult{Reservations: created, Created: true}
	if err := m.finalizeAllocation(allocationID, created); err != nil {
		return result, fmt.Errorf("%w: reservation bundle for allocation %s is persisted but allocation finalization is pending: %w", ErrReconciliationRequired, allocationID, err)
	}
	return result, nil
}

func normalizeRequests(requests []ReserveRequest, policy ReservationPolicy) ([]normalizedRequest, error) {
	if len(requests) == 0 {
		return nil, fmt.Errorf("reservation: at least one resource is required")
	}
	result := make([]normalizedRequest, 0, len(requests))
	allocationID, idempotencyKey := requests[0].AllocationID, requests[0].IdempotencyKey
	ttl := requests[0].LeaseTTL
	seen := make(map[string]bool, len(requests))
	for _, req := range requests {
		if req.AllocationID == "" || req.AllocationID != allocationID {
			return nil, fmt.Errorf("reservation: all resources must have the same non-empty allocation_id")
		}
		if req.IdempotencyKey != idempotencyKey {
			return nil, fmt.Errorf("reservation: all resources in a bundle must use the same idempotency key")
		}
		if req.ResourceID == "" {
			return nil, fmt.Errorf("reservation: resource_id is required")
		}
		if !req.ResourceType.IsValid() {
			return nil, fmt.Errorf("reservation: invalid resource type %q", req.ResourceType)
		}
		canonical, err := CanonicalResourceID(req.ResourceID, req.ResourceType)
		if err != nil {
			return nil, fmt.Errorf("reservation: invalid resource_id: %w", err)
		}
		if req.LeaseTTL <= 0 || !policy.ValidateLeaseTTL(req.LeaseTTL) {
			return nil, fmt.Errorf("reservation: lease TTL %v outside range [%v, %v]", req.LeaseTTL, policy.LeaseMinTTL, policy.LeaseMaxTTL)
		}
		if req.LeaseTTL != ttl {
			return nil, fmt.Errorf("reservation: all resources in a bundle must use the same lease TTL")
		}
		key := string(req.ResourceType) + "\x00" + canonical
		if seen[key] {
			return nil, fmt.Errorf("reservation: duplicate resource in bundle: %s", canonical)
		}
		seen[key] = true
		result = append(result, normalizedRequest{request: req, canonical: canonical})
	}
	return result, nil
}

func bundleFingerprint(requests []normalizedRequest) string {
	if len(requests) == 1 {
		req := requests[0].request
		req.ResourceID = requests[0].canonical
		return computeFingerprint(req)
	}
	sorted := append([]normalizedRequest(nil), requests...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].request.ResourceType != sorted[j].request.ResourceType {
			return sorted[i].request.ResourceType < sorted[j].request.ResourceType
		}
		return sorted[i].canonical < sorted[j].canonical
	})
	h := sha256.New()
	for _, item := range sorted {
		fmt.Fprintf(h, "%d:%s%d:%s", len(item.request.ResourceType), item.request.ResourceType, len(item.canonical), item.canonical)
	}
	fmt.Fprintf(h, "%d:%s%d:%d", len(sorted[0].request.AllocationID), sorted[0].request.AllocationID, len(sorted[0].request.LeaseTTL.String()), sorted[0].request.LeaseTTL)
	return hex.EncodeToString(h.Sum(nil))
}

func validateIdempotentBundle(existing []*Reservation, requests []normalizedRequest, owner, allocationID, fingerprint string) error {
	if len(existing) != len(requests) {
		return ErrIdempotencyConflict
	}
	expected := make(map[string]normalizedRequest, len(requests))
	for _, item := range requests {
		expected[string(item.request.ResourceType)+"\x00"+item.canonical] = item
	}
	for _, record := range existing {
		key := string(record.ResourceType) + "\x00" + record.ResourceID
		item, ok := expected[key]
		if !ok || record.Owner != owner || record.AllocationID != allocationID {
			return ErrIdempotencyConflict
		}
		if record.BundleSize != 0 && record.BundleSize != len(requests) {
			return ErrIdempotencyConflict
		}
		if record.RequestFingerprint != fingerprint {
			if len(requests) != 1 {
				return ErrIdempotencyConflict
			}
			legacy := computeFingerprint(item.request)
			persistedTTL := record.LeaseExpiry.Sub(record.LastRenewedAt)
			persisted := ReserveRequest{AllocationID: record.AllocationID, ResourceID: record.ResourceID, ResourceType: record.ResourceType, LeaseTTL: persistedTTL}
			if record.RequestFingerprint != legacy && computeFingerprint(persisted) != fingerprint {
				return ErrIdempotencyConflict
			}
		}
		delete(expected, key)
	}
	if len(expected) != 0 {
		return ErrIdempotencyConflict
	}
	return nil
}

func sortReservations(records []*Reservation) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].ResourceType != records[j].ResourceType {
			return records[i].ResourceType < records[j].ResourceType
		}
		return records[i].ResourceID < records[j].ResourceID
	})
}


type reservationAssignmentFinalizer interface {
	FinalizeReservation(allocationID string, reservations []*Reservation) error
}

func (m *Manager) finalizeAllocation(allocationID string, reservations []*Reservation) error {
	if finalizer, ok := m.allocVal.(reservationAssignmentFinalizer); ok {
		return finalizer.FinalizeReservation(allocationID, reservations)
	}
	return m.allocVal.EndReservation(allocationID, true)
}

// ReconcilePending completes allocation finalization for a reservation bundle
// that was durably committed before the process failed. It is restricted to
// trusted reconcilers and only accepts a complete bundle of active HELD leases.
func (m *Manager) ReconcilePending(allocationID, token string) error {
	identity, err := m.resolveIdentity(token)
	if err != nil {
		return fmt.Errorf("reservation: identity resolution failed: %w", err)
	}
	if !identity.IsReconciler() {
		return fmt.Errorf("reservation: only trusted reconcilers can reconcile pending allocations")
	}
	if allocationID == "" {
		return fmt.Errorf("reservation: allocation_id is required")
	}
	unlock, err := m.resourceLock.Lock()
	if err != nil {
		return fmt.Errorf("reservation: cannot acquire resource lock: %w", err)
	}
	defer unlock()

	records, corrupt, err := m.store.loadLocked()
	if err != nil {
		return err
	}
	if len(corrupt) > 0 {
		return fmt.Errorf("%w: %d corrupt lines", ErrReservationStoreCorrupt, len(corrupt))
	}
	var bundle []*Reservation
	for _, record := range records {
		if record.AllocationID != allocationID {
			continue
		}
		if record.Status != StatusHeld {
			return fmt.Errorf("%w: allocation %s has reservation %s in status %s", ErrReconciliationRequired, allocationID, record.ReservationID, record.Status)
		}
		bundle = append(bundle, record)
	}
	if len(bundle) == 0 {
		// Crash recovery: if the allocation was claimed but the atomic
		// reservation-store commit never happened, fail the orphaned claim.
		if err := m.allocVal.EndReservation(allocationID, false); err != nil {
			return fmt.Errorf("%w: no reservation records for allocation %s and orphan claim could not be aborted: %w", ErrReconciliationRequired, allocationID, err)
		}
		return nil
	}
	expectedBundleSize := 0
	for _, record := range bundle {
		if record.BundleSize > expectedBundleSize {
			expectedBundleSize = record.BundleSize
		}
	}
	if expectedBundleSize > 0 && len(bundle) != expectedBundleSize {
		return fmt.Errorf("%w: allocation %s has %d/%d reservation records", ErrReconciliationRequired, allocationID, len(bundle), expectedBundleSize)
	}
	sortReservations(bundle)
	if err := m.finalizeAllocation(allocationID, bundle); err != nil {
		return fmt.Errorf("%w: cannot finalize allocation %s: %w", ErrReconciliationRequired, allocationID, err)
	}
	return nil
}

// ReapExpiredOnce marks expired leases and asks the trusted cleanup verifier
// to reconcile each candidate. Errors are aggregated so one broken resource
// does not prevent other expired leases from being processed.
func (m *Manager) ReapExpiredOnce(reconcilerToken string) (int, error) {
	if _, err := m.resolveReconciler(reconcilerToken); err != nil {
		return 0, err
	}
	marked, err := m.MarkExpiredCandidates()
	if err != nil {
		return 0, err
	}
	candidates, err := m.store.ListByStatus(StatusExpiredPendingReconciliation)
	if err != nil {
		return marked, err
	}
	var failures []error
	for _, candidate := range candidates {
		if _, err := m.ReconcileExpired(candidate.ReservationID, reconcilerToken); err != nil {
			failures = append(failures, fmt.Errorf("reservation %s: %w", candidate.ReservationID, err))
		}
	}
	return marked, errors.Join(failures...)
}

func (m *Manager) resolveReconciler(token string) (Identity, error) {
	identity, err := m.resolveIdentity(token)
	if err != nil {
		return Identity{}, fmt.Errorf("reservation: identity resolution failed: %w", err)
	}
	if !identity.IsReconciler() {
		return Identity{}, fmt.Errorf("reservation: only trusted reconcilers can perform this operation")
	}
	return identity, nil
}


type ReaperReport func(error)

// RunLeaseReaper periodically expires stale leases and reconciles cleanup.
// The report callback receives each pass error; a single bad resource does not
// stop subsequent passes. Cancellation returns ctx.Err().
func (m *Manager) RunLeaseReaper(ctx context.Context, reconcilerToken string, interval time.Duration, report ReaperReport) error {
	if interval <= 0 {
		return fmt.Errorf("reservation: reaper interval must be positive")
	}
	if report == nil {
		return fmt.Errorf("reservation: reaper error callback is required")
	}
	if _, err := m.resolveReconciler(reconcilerToken); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := m.ReapExpiredOnce(reconcilerToken); err != nil {
				report(err)
			}
		}
	}
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
