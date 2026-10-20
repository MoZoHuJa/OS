package reservation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
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
	store        *Store
	resourceLock *ResourceLock
	policy       ReservationPolicy
	clock        Clock
	inventory    InventoryProvider
	allocVal     AllocationValidator
	cleanupVer   CleanupVerifier
	idProv       TrustedIdentityProvider // P1-01 (v19.2.9): resolves tokens to identities
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
	if strings.TrimSpace(identity.Principal()) == "" {
		return nil, fmt.Errorf("reservation: identity provider returned an empty principal")
	}
	if ownerValidator, ok := m.allocVal.(allocationOwnershipValidator); ok {
		if err := ownerValidator.ValidateAllocationOwner(normalized[0].request.AllocationID, identity.Principal()); err != nil {
			return nil, err
		}
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
	// Re-check ownership inside the same critical section as reservation commit
	// to close the validation-to-claim race.
	if ownerValidator, ok := m.allocVal.(allocationOwnershipValidator); ok {
		if err := ownerValidator.ValidateAllocationOwner(allocationID, identity.Principal()); err != nil {
			return nil, err
		}
	}
	if validator, ok := m.allocVal.(allocationPlanValidator); ok {
		if err := validator.ValidateReservationPlan(allocationID, normalized, snap); err != nil {
			return nil, err
		}
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
				if record.Status != StatusHeld {
					return nil, fmt.Errorf("%w: idempotent reservation %s is in non-active status %s", ErrReconciliationRequired, record.ReservationID, record.Status)
				}
				if record.IsExpired(m.clock.Now()) {
					return nil, ErrLeaseExpired
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

	// An allocation owns exactly one durable reservation bundle. If a request
	// did not match the existing idempotency identity above, it must not append
	// a second independent bundle to the same allocation.
	for _, existing := range records {
		if existing.AllocationID == allocationID {
			return nil, fmt.Errorf("%w: allocation %s already has a reservation bundle; retry with the original idempotency key", ErrIdempotencyConflict, allocationID)
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
	if validator, ok := m.allocVal.(allocationPlanValidator); ok {
		if err := validator.ValidateReservationPlan(allocationID, normalized, snap2); err != nil {
			return nil, err
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
	// Re-check the immutable plan after the allocation claim transitions to
	// RESERVING, closing a plan-change race between validation and BeginReservation.
	if validator, ok := m.allocVal.(allocationPlanValidator); ok {
		if err := validator.ValidateReservationPlan(allocationID, normalized, snap2); err != nil {
			return nil, err
		}
	}

	now := m.clock.Now().UTC()
	leaseExpiry := now.Add(normalized[0].request.LeaseTTL)
	bundleLeaseID := generateID()
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
			LeaseID:             bundleLeaseID,
			LeaseExpiry:         leaseExpiry,
			LastRenewedAt:       now,
			InventoryGeneration: snap2.Generation,
			Status:              StatusHeld,
			CreatedAt:           now,
			UpdatedAt:           now,
			IdempotencyKey:      idempotencyKey,
			RequestFingerprint:  fingerprint,
			BundleSize:          len(normalized),
			Revision:            0,
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

type allocationOwnershipValidator interface {
	ValidateAllocationOwner(allocationID, principal string) error
}

type allocationPlanValidator interface {
	ValidateReservationPlan(allocationID string, requests []normalizedRequest, snapshot InventorySnapshot) error
}

type pendingAllocationScanner interface {
	ListPendingReservationAllocations() ([]string, error)
}

// allocationReleaseReconciler is the allocation-side capability required to
// converge the allocation lifecycle when reservation bundles are released.
// KPK-19.2.12-P1-01: replaces the v19.2.11 misuse of EndReservation(false).
type allocationReleaseReconciler interface {
	// BeginAllocationRelease persists RESERVED → RELEASING cleanup progress.
	BeginAllocationRelease(id string) error
	// CompleteAllocationRelease finishes RELEASING → RELEASED and clears
	// assignment fields and lease metadata.
	CompleteAllocationRelease(id string) error
	// MarkAllocationCleanupRequired persists an operator-visible blocked
	// state for allocations whose bundle cannot be safely released.
	MarkAllocationCleanupRequired(id, reason string) error
}

// releasingAllocationScanner enumerates allocations whose release lifecycle
// must be completed or kept blocked by the startup recovery scan.
type releasingAllocationScanner interface {
	ListReleasingAllocations() ([]string, error)
}

// reservedAllocationLister enumerates RESERVED allocations and exposes the
// durable allocation record for lease-repair comparisons.
// KPK-19.2.12-P1-02: explicit interfaces replace the v19.2.11 concrete-type
// assertions and silently-skipped no-op scans.
type reservedAllocationLister interface {
	ListReservedAllocations() ([]string, error)
	GetAllocation(id string) (*allocation.Allocation, error)
}

// reservationLeaseUpdater mirrors durable bundle lease metadata into the
// allocation record.
type reservationLeaseUpdater interface {
	UpdateReservationLease(allocationID, leaseID string, expiry time.Time) error
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
//
// KPK-19.2.12-P2-01: expired-HELD and blocked records are no longer merely
// rejected — they are actively routed to the cleanup workflow. Expired HELD
// records are durably marked EXPIRED_PENDING_RECONCILIATION (inside the same
// critical section and atomic rewrite) so the lease reaper can process them,
// and the allocation is persisted in an operator-visible blocked state
// (CLEANUP_REQUIRED) instead of being left in RESERVING forever. The caller
// still receives ErrReconciliationRequired — finalization is refused — but
// recovery now converges instead of retrying the same rejection forever.
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
	var expired []*Reservation
	blocked := ReservationStatus("")
	for _, record := range records {
		if record.AllocationID != allocationID {
			continue
		}
		// P1-03 (v19.2.11): An expired HELD reservation must not be
		// finalized to RESERVED.
		// KPK-19.2.12-P2-01: route it to the lease reaper instead of
		// only refusing — see the durable marking below.
		if record.Status == StatusHeld && record.IsExpired(m.clock.Now()) {
			expired = append(expired, record)
			continue
		}
		if record.Status != StatusHeld {
			if record.Status == StatusExpiredPendingReconciliation || record.Status == StatusConflict {
				blocked = record.Status
			}
			continue
		}
		bundle = append(bundle, record)
	}
	if len(bundle) == 0 && len(expired) == 0 && blocked == "" {
		// Crash recovery: if the allocation was claimed but the atomic
		// reservation-store commit never happened, fail the orphaned claim.
		if err := m.allocVal.EndReservation(allocationID, false); err != nil {
			return fmt.Errorf("%w: no reservation records for allocation %s and orphan claim could not be aborted: %w", ErrReconciliationRequired, allocationID, err)
		}
		return nil
	}

	// KPK-19.2.12-P2-01: durably route expired-HELD records to the cleanup
	// workflow. Marking happens inside the already-held critical section and
	// is persisted with one atomic rewrite — the lease reaper
	// (ReapExpiredOnce / ReconcileExpired) will verify cleanup and release.
	if len(expired) > 0 {
		for _, record := range expired {
			if err := record.transition(StatusExpiredPendingReconciliation, m.clock.Now(), "lease expired during pending reconciliation"); err != nil {
				return fmt.Errorf("%w: cannot route expired reservation %s to cleanup: %w", ErrReconciliationRequired, record.ReservationID, err)
			}
		}
		if err := m.store.rewriteAll(records); err != nil {
			return fmt.Errorf("%w: cannot persist expiry routing for allocation %s: %w", ErrReconciliationRequired, allocationID, err)
		}
	}

	// KPK-19.2.12-P2-01: persist an operator-visible blocked state when the
	// bundle cannot be finalized. The allocation must not remain RESERVING
	// retrying the same rejection forever.
	if blocked != "" || len(expired) > 0 {
		reason := "reservation bundle blocked"
		if blocked != "" {
			reason = fmt.Sprintf("reservation bundle member in %s", blocked)
		} else {
			reason = "reservation bundle has expired leases"
		}
		if marker, ok := m.allocVal.(allocationReleaseReconciler); ok {
			if err := marker.MarkAllocationCleanupRequired(allocationID, reason); err != nil {
				return fmt.Errorf("%w: allocation %s has expired/blocked reservations and could not be marked CLEANUP_REQUIRED: %w", ErrReconciliationRequired, allocationID, err)
			}
		}
		return fmt.Errorf("%w: allocation %s routed to cleanup reconciliation (%d expired, blocked=%q)", ErrReconciliationRequired, allocationID, len(expired), blocked)
	}

	// KPK-19.2.12-P2-02: strict, central bundle validation replaces the
	// previous max-BundleSize comparison. All shared invariants (owner,
	// lease id/expiry, idempotency key, fingerprint, bundle size, duplicate
	// resources, allowed statuses) must hold before finalization.
	sortReservations(bundle)
	if err := validateBundleConsistency(bundle, allocationID, activeBundleStatuses); err != nil {
		return fmt.Errorf("%w: %w", ErrReconciliationRequired, err)
	}
	if err := m.finalizeAllocation(allocationID, bundle); err != nil {
		return fmt.Errorf("%w: cannot finalize allocation %s: %w", ErrReconciliationRequired, allocationID, err)
	}
	return nil
}

// RecoverySummary is the structured result of the startup recovery pass.
// KPK-19.2.12-P2-01: operators must see repaired, blocked and failed
// allocation IDs instead of a single opaque error.
type RecoverySummary struct {
	// Finalized lists RESERVING allocations whose durable bundle was completed
	// to RESERVED.
	Finalized []string
	// Aborted lists orphaned RESERVING claims safely aborted to FAILED (no
	// reservation record was ever committed).
	Aborted []string
	// Released lists allocations whose interrupted release was completed to
	// RELEASED.
	Released []string
	// CleanupBlocked lists allocations persisted in CLEANUP_REQUIRED — an
	// operator must inspect these; they are never auto-released.
	CleanupBlocked []string
	// LeaseRepaired lists RESERVED allocations whose stale lease metadata was
	// converged with the authoritative reservation bundle.
	LeaseRepaired []string
	// Failed lists allocation IDs whose recovery attempt returned an error.
	Failed []string
}

// Recover is the complete startup recovery sequence for the Compute Fabric
// reservation/allocation layer. KPK-19.2.12-P1-02/P2-01: the v19.2.11
// startup contract only ran the RESERVING scan; the lease-repair method
// existed but was never invoked, and interrupted releases were invisible.
// Recover orchestrates the full state-dependent path:
//
//  1. RESERVING scan — finalize complete HELD bundles, abort orphaned
//     claims, durably route expired/blocked bundles to cleanup.
//  2. Expired-lease reaper pass — mark expired HELD reservations, verify
//     cleanup and release them (which also converges allocations).
//  3. RELEASING/CLEANUP_REQUIRED scan — complete interrupted allocation
//     releases; keep CONFLICT-blocked allocations operator-visible.
//  4. RESERVED lease repair — converge allocation lease metadata with the
//     authoritative reservation store.
//
// Each phase aggregates its errors; a failure in one phase never prevents
// the remaining phases from running. The returned error joins every failure
// so nothing is silently skipped.
func (m *Manager) Recover(token string) (*RecoverySummary, error) {
	if _, err := m.resolveReconciler(token); err != nil {
		return nil, err
	}
	summary := &RecoverySummary{}
	var failures []error

	// Phase 1: RESERVING allocations.
	scanner, ok := m.allocVal.(pendingAllocationScanner)
	if !ok {
		failures = append(failures, fmt.Errorf("%w: allocation validator does not support startup scanning", ErrReconciliationRequired))
	} else {
		ids, err := scanner.ListPendingReservationAllocations()
		if err != nil {
			failures = append(failures, fmt.Errorf("%w: cannot list pending allocations: %w", ErrReconciliationRequired, err))
		} else {
			sort.Strings(ids)
			stateLister, hasLister := m.allocVal.(reservedAllocationLister)
			for _, id := range ids {
				if err := m.ReconcilePending(id, token); err != nil {
					summary.Failed = append(summary.Failed, id)
					failures = append(failures, fmt.Errorf("pending allocation %s: %w", id, err))
					continue
				}
				// Classify the converged outcome for the operator.
				if hasLister {
					if record, err := stateLister.GetAllocation(id); err == nil && record != nil {
						switch record.State {
						case allocation.StateFailed:
							summary.Aborted = append(summary.Aborted, id)
						default:
							summary.Finalized = append(summary.Finalized, id)
						}
						continue
					}
				}
				summary.Finalized = append(summary.Finalized, id)
			}
		}
	}

	// Phase 2: expired-lease reaper pass. MarkExpiredCandidates routes expired
	// HELD reservations to cleanup; ReconcileExpired verifies cleanup evidence
	// and releases. Allocation convergence happens inside ReconcileExpired via
	// reconcileAllocationAfterRelease.
	if _, err := m.ReapExpiredOnce(token); err != nil {
		failures = append(failures, fmt.Errorf("expired-lease reconciliation: %w", err))
	}

	// Phase 3: interrupted allocation releases.
	if releaseScanner, ok := m.allocVal.(releasingAllocationScanner); ok {
		ids, err := releaseScanner.ListReleasingAllocations()
		if err != nil {
			failures = append(failures, fmt.Errorf("%w: cannot list releasing allocations: %w", ErrReconciliationRequired, err))
		} else {
			sort.Strings(ids)
			for _, id := range ids {
				alloc, err := m.recoverReleasingAllocation(id, token)
				if err != nil {
					summary.Failed = append(summary.Failed, id)
					failures = append(failures, fmt.Errorf("releasing allocation %s: %w", id, err))
					continue
				}
				switch alloc {
				case allocation.StateReleased:
					summary.Released = append(summary.Released, id)
				case allocation.StateCleanupRequired:
					summary.CleanupBlocked = append(summary.CleanupBlocked, id)
				}
			}
		}
	}

	// Phase 4: reserved-lease metadata repair.
	if lister, ok := m.allocVal.(reservedAllocationLister); ok {
		ids, err := lister.ListReservedAllocations()
		if err != nil {
			failures = append(failures, fmt.Errorf("%w: cannot list reserved allocations: %w", ErrReconciliationRequired, err))
		} else {
			sort.Strings(ids)
			updater, updaterOK := m.allocVal.(reservationLeaseUpdater)
			for _, id := range ids {
				before, beforeErr := lister.GetAllocation(id)
				if err := m.reconcileAllocationLease(id, lister, updaterOrZero(updater, updaterOK)); err != nil {
					summary.Failed = append(summary.Failed, id)
					failures = append(failures, fmt.Errorf("lease repair allocation %s: %w", id, err))
					continue
				}
				if beforeErr != nil {
					continue
				}
				after, afterErr := lister.GetAllocation(id)
				if afterErr != nil {
					continue
				}
				if before != nil && after != nil && (before.LeaseID != after.LeaseID || !before.LeaseExpiry.Equal(after.LeaseExpiry)) {
					if after.State == allocation.StateReleased {
						// The phase-4 routing completed an interrupted
						// release (write-failure recovery) — classify honestly.
						summary.Released = append(summary.Released, id)
					} else {
						summary.LeaseRepaired = append(summary.LeaseRepaired, id)
					}
				}
			}
		}
	}

	return summary, errors.Join(failures...)
}

// updaterOrZero returns the updater when available, or nil when the adapter
// does not support lease updates (reconcileAllocationLease will then fail
// closed through a nil-interface check performed by the caller path).
func updaterOrZero(updater reservationLeaseUpdater, ok bool) reservationLeaseUpdater {
	if !ok {
		return nil
	}
	return updater
}

// recoverReleasingAllocation completes or keeps blocked one allocation found
// in RELEASING/CLEANUP_REQUIRED. The authoritative reservation bundle
// decides the outcome; the method is idempotent so repeated startup passes
// converge.
func (m *Manager) recoverReleasingAllocation(allocationID, token string) (allocation.State, error) {
	if _, err := m.resolveReconciler(token); err != nil {
		return "", err
	}
	reservations, err := m.store.ListByAllocation(allocationID)
	if err != nil {
		return "", fmt.Errorf("cannot list reservations: %w", err)
	}
	lister, ok := m.allocVal.(reservedAllocationLister)
	if !ok {
		return "", fmt.Errorf("%w: allocation validator does not support allocation reads", ErrReconciliationRequired)
	}
	record, err := lister.GetAllocation(allocationID)
	if err != nil {
		return "", fmt.Errorf("%w: cannot load allocation: %w", ErrReconciliationRequired, err)
	}

	anyConflict := false
	allReleased := len(reservations) > 0
	for _, r := range reservations {
		if r.Status == StatusConflict {
			anyConflict = true
		}
		if r.Status != StatusReleased {
			allReleased = false
		}
	}

	// Blocked bundles stay operator-visible — never auto-release.
	if anyConflict {
		if record.State != allocation.StateCleanupRequired {
			releaser, ok := m.allocVal.(allocationReleaseReconciler)
			if !ok {
				return "", fmt.Errorf("%w: allocation validator does not support release reconciliation", ErrReconciliationRequired)
			}
			if err := releaser.MarkAllocationCleanupRequired(allocationID, "conflicted bundle member discovered during recovery"); err != nil {
				return "", fmt.Errorf("cannot mark allocation CLEANUP_REQUIRED: %w", err)
			}
			return allocation.StateCleanupRequired, nil
		}
		return allocation.StateCleanupRequired, nil
	}

	// No reservation records for a RELEASING allocation: the bundle store is
	// authoritative — if it proves no member was ever committed, the release
	// can be completed (the RESERVING-scan orphan abort owns the no-claim case).
	if len(reservations) == 0 {
		allReleased = false
	}

	if allReleased {
		if err := m.reconcileAllocationAfterRelease(allocationID); err != nil {
			return "", err
		}
		after, err := lister.GetAllocation(allocationID)
		if err != nil {
			return "", fmt.Errorf("%w: cannot reload allocation: %w", ErrReconciliationRequired, err)
		}
		return after.State, nil
	}

	// Partial release or missing records: keep the current blocked/releasing
	// state — the reaper will release the remaining members and a later pass
	// will complete the allocation.
	return record.State, nil
}

// ReconcileAllPending repairs interrupted Phase 3 claims on startup. The
// allocation adapter supplies allocation IDs left in RESERVING; each is
// finalized from its complete durable reservation bundle or safely aborted
// when no reservation record was committed.
//
// KPK-19.2.12-P1-02: this is now a thin compatibility wrapper over the full
// Recover sequence, so existing startup callers get the complete recovery
// orchestration (RESERVING scan + expired-lease reaper + interrupted-release
// completion + reserved-lease repair) instead of only the first phase.
func (m *Manager) ReconcileAllPending(token string) error {
	_, err := m.Recover(token)
	return err
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
			// KPK-19.2.12-P1-02: controlled periodic lease-repair path.
			// A failed allocation-store mirror write during
			// RenewBundleLease is repaired here within one reaper
			// interval instead of persisting indefinitely.
			if err := m.ReconcileReservedLeases(reconcilerToken); err != nil {
				report(err)
			}
		}
	}
}

// RenewLease renews the entire allocation's reservation bundle. The
// reservationID identifies the bundle; no member can outlive its siblings.
func (m *Manager) RenewLease(reservationID, token string, newTTL time.Duration) (*Reservation, error) {
	record, err := m.store.Get(reservationID)
	if err != nil {
		return nil, err
	}
	if err := m.RenewBundleLease(record.AllocationID, token, newTTL); err != nil {
		return nil, err
	}
	return m.store.Get(reservationID)
}

// RenewBundleLease atomically extends every HELD reservation for an allocation.
// The reservation store is authoritative; allocation metadata is updated after
// the durable bundle rewrite and is repairable through ReconcilePending.
func (m *Manager) RenewBundleLease(allocationID, token string, newTTL time.Duration) error {
	identity, err := m.resolveIdentity(token)
	if err != nil {
		return fmt.Errorf("reservation: identity resolution failed: %w", err)
	}
	if allocationID == "" {
		return fmt.Errorf("reservation: allocation_id is required")
	}
	if !m.policy.ValidateLeaseTTL(newTTL) {
		return fmt.Errorf("reservation: lease TTL %v outside range [%v, %v]", newTTL, m.policy.LeaseMinTTL, m.policy.LeaseMaxTTL)
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
		if record.Owner != identity.Principal() {
			return ErrLeaseNotOwned
		}
		if record.Status != StatusHeld {
			return fmt.Errorf("%w: cannot renew bundle with reservation %s in status %s", ErrReconciliationRequired, record.ReservationID, record.Status)
		}
		if record.IsExpired(m.clock.Now()) {
			return ErrLeaseExpired
		}
		bundle = append(bundle, record)
	}
	if len(bundle) == 0 {
		return ErrResourceNotFound
	}
	// KPK-19.2.12-P2-02: strict, central bundle validation. The previous
	// max-BundleSize comparison accepted logically inconsistent bundles
	// (mixed lease IDs, owners, fingerprints) and renewed them as one
	// atomic lease. Fail closed and preserve the records for repair.
	sortReservations(bundle)
	if err := validateBundleConsistency(bundle, allocationID, activeBundleStatuses); err != nil {
		return fmt.Errorf("%w: %w", ErrReconciliationRequired, err)
	}
	now := m.clock.Now().UTC()
	expiry := now.Add(newTTL)
	for _, record := range bundle {
		record.LeaseExpiry = expiry
		record.LastRenewedAt = now
		record.UpdatedAt = now
		record.Revision++
	}
	if err := m.store.rewriteAll(records); err != nil {
		return fmt.Errorf("%w: cannot persist lease renewal: %w", ErrPersistence, err)
	}
	// KPK-19.2.12-P1-02: the lease-update capability is now an explicit
	// interface; a missing capability is a fail-closed reconciliation
	// error, not a silently skipped mirror write.
	updater, ok := m.allocVal.(reservationLeaseUpdater)
	if !ok {
		return fmt.Errorf("%w: lease renewed in reservation store but allocation validator cannot mirror lease metadata", ErrReconciliationRequired)
	}
	if err := updater.UpdateReservationLease(allocationID, bundle[0].LeaseID, expiry); err != nil {
		return fmt.Errorf("%w: lease renewed in reservation store but allocation metadata is pending repair: %w", ErrReconciliationRequired, err)
	}
	return nil
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
	// KPK-19.2.12-P1-01: converge the allocation lifecycle after a
	// user-initiated release too — the invariant "an allocation never
	// remains RESERVED after all its reservations are RELEASED" must hold
	// on every release path, not only the expiry-reaper path. The typed
	// ErrAllocationReconciliationPending outcome is surfaced, never
	// swallowed.
	if updated != nil && updated.Status == StatusReleased && updated.AllocationID != "" {
		if err := m.reconcileAllocationAfterRelease(updated.AllocationID); err != nil {
			return updated, fmt.Errorf("%w: reservation %s released but allocation %s reconciliation pending: %w",
				ErrAllocationReconciliationPending, reservationID, updated.AllocationID, err)
		}
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
// P1-02 (v19.2.11): After releasing a reservation, also updates the allocation
// lifecycle: transitions RESERVED → RELEASING, and if all bundle members are
// released, transitions to RELEASED and clears assignment fields.
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

	// P1-02 (v19.2.11) → KPK-19.2.12-P1-01: Allocation-aware cleanup after
	// reservation release. Check if ALL reservations for this allocation are
	// now RELEASED. If so, transition the allocation to RELEASED and clear
	// assignments. If not all are released, persist RELEASING cleanup
	// progress. A CONFLICT member blocks the allocation in
	// CLEANUP_REQUIRED. The error is NEVER swallowed: the reservation is
	// durably RELEASED (returned to the caller) but the allocation
	// reconciliation outcome is surfaced as ErrAllocationReconciliationPending
	// so operators and the startup recovery scan can converge it.
	if r.AllocationID != "" {
		if err := m.reconcileAllocationAfterRelease(r.AllocationID); err != nil {
			return updated, fmt.Errorf("%w: reservation %s released but allocation %s reconciliation pending: %w",
				ErrAllocationReconciliationPending, reservationID, r.AllocationID, err)
		}
	}

	return updated, nil
}

func (m *Manager) Get(id string) (*Reservation, error) {
	return m.store.Get(id)
}

func (m *Manager) ListByAllocation(allocID string) ([]*Reservation, error) {
	return m.store.ListByAllocation(allocID)
}

// reconcileAllocationAfterRelease converges the allocation lifecycle with
// the authoritative reservation bundle after a reservation release event.
// KPK-19.2.12-P1-01: the v19.2.11 implementation called
// EndReservation(allocationID, false), which only aborts a RESERVING
// allocation and rejects RESERVED — exactly the state being released. The
// partial-release branch was a comment-only no-op. This implementation uses
// dedicated, allocation-aware transitions:
//
//   - any member CONFLICT            → CLEANUP_REQUIRED (operator-visible)
//   - all members RELEASED            → RELEASED, assignments cleared
//   - some members still blocking     → RELEASING (cleanup progress persisted)
//
// and returns an error whenever the allocation cannot be reconciled, so the
// caller can surface a typed result instead of reporting false success.
func (m *Manager) reconcileAllocationAfterRelease(allocationID string) error {
	reservations, err := m.store.ListByAllocation(allocationID)
	if err != nil {
		return fmt.Errorf("cannot list reservations for allocation %s: %w", allocationID, err)
	}
	if len(reservations) == 0 {
		return nil // no reservations, nothing to reconcile
	}

	releaser, ok := m.allocVal.(allocationReleaseReconciler)
	if !ok {
		return fmt.Errorf("%w: allocation validator does not support release reconciliation", ErrReconciliationRequired)
	}

	allReleased := true
	anyConflict := false
	for _, r := range reservations {
		if r.Status == StatusConflict {
			anyConflict = true
		}
		if r.Status != StatusReleased {
			allReleased = false
		}
	}

	// A conflicted member means cleanup could not be positively verified:
	// persist an operator-visible blocked state and surface a typed error —
	// never report the allocation fully released (no false success).
	if anyConflict {
		if err := releaser.MarkAllocationCleanupRequired(allocationID, "reservation bundle member in CONFLICT after release"); err != nil {
			return fmt.Errorf("cannot mark allocation %s CLEANUP_REQUIRED: %w", allocationID, err)
		}
		return fmt.Errorf("%w: allocation %s blocked in CLEANUP_REQUIRED: reservation bundle member in CONFLICT", ErrReconciliationRequired, allocationID)
	}

	// All bundle members are durably RELEASED with verified cleanup
	// evidence (the reservation transition itself required the cleanup
	// verifier): complete the allocation release and clear assignments.
	if allReleased {
		if err := releaser.CompleteAllocationRelease(allocationID); err != nil {
			return fmt.Errorf("cannot complete release of allocation %s: %w", allocationID, err)
		}
		return nil
	}

	// Partial release: persist cleanup progress so a crash between bundle
	// member releases is discoverable by the startup recovery scan.
	if err := releaser.BeginAllocationRelease(allocationID); err != nil {
		return fmt.Errorf("cannot begin release of allocation %s: %w", allocationID, err)
	}
	return nil
}

// ReconcileReservedLeases repairs stale lease metadata on RESERVED
// allocations by comparing it with the authoritative reservation bundle.
// P2-02 (v19.2.11) → KPK-19.2.12-P1-02: the v19.2.11 version was never
// wired into any startup or periodic path, silently returned nil when
// capabilities were missing, skipped per-allocation store errors,
// discarded lease-update errors and trusted bundle[0] without validating
// the bundle. This version:
//
//   - fails closed when the adapter lacks scanning or lease-update capability,
//   - validates the ENTIRE bundle (shared lease id/expiry, owner, statuses,
//     bundle size, duplicates) before selecting authoritative metadata,
//   - aggregates every per-allocation failure with errors.Join — a skipped
//     repair is never reported as success,
//   - is invoked by the startup recovery sequence (Recover) and by the
//     periodic lease reaper (RunLeaseReaper).
func (m *Manager) ReconcileReservedLeases(token string) error {
	if _, err := m.resolveReconciler(token); err != nil {
		return err
	}

	lister, ok := m.allocVal.(reservedAllocationLister)
	if !ok {
		return fmt.Errorf("%w: allocation validator does not support reserved-allocation scanning", ErrReconciliationRequired)
	}
	updater, ok := m.allocVal.(reservationLeaseUpdater)
	if !ok {
		return fmt.Errorf("%w: allocation validator does not support lease updates", ErrReconciliationRequired)
	}
	ids, err := lister.ListReservedAllocations()
	if err != nil {
		return fmt.Errorf("%w: cannot list reserved allocations: %w", ErrReconciliationRequired, err)
	}
	sort.Strings(ids)
	var failures []error
	for _, id := range ids {
		if err := m.reconcileAllocationLease(id, lister, updater); err != nil {
			failures = append(failures, fmt.Errorf("allocation %s: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

// reconcileAllocationLease converges one allocation's lease metadata with
// its durable reservation bundle. The bundle is fully validated before any
// metadata is trusted; a mismatched or inconsistent bundle fails closed and
// is preserved for operator repair.
func (m *Manager) reconcileAllocationLease(allocationID string, lister reservedAllocationLister, updater reservationLeaseUpdater) error {
	bundle, err := m.store.ListByAllocation(allocationID)
	if err != nil {
		return fmt.Errorf("%w: cannot load reservation bundle: %w", ErrReconciliationRequired, err)
	}
	if len(bundle) == 0 {
		// No reservation records: the pending/aborted-allocation scans
		// own this case. Nothing to repair here.
		return nil
	}
	// KPK-19.2.12-P1-01 (restart repair): a RESERVED allocation whose entire
	// bundle is RELEASED (or conflicted) is an interrupted release — the
	// allocation-store writes failed between the reservation release and
	// the allocation completion. Restart repair must find and converge it,
	// not fail bundle validation on the released statuses.
	allReleased := true
	anyConflict := false
	for _, r := range bundle {
		if r.Status == StatusConflict {
			anyConflict = true
		}
		if r.Status != StatusReleased {
			allReleased = false
		}
	}
	if allReleased || anyConflict {
		return m.reconcileAllocationAfterRelease(allocationID)
	}
	if err := validateActiveBundle(bundle, allocationID); err != nil {
		return fmt.Errorf("%w: bundle validation failed: %w", ErrReconciliationRequired, err)
	}
	record, err := lister.GetAllocation(allocationID)
	if err != nil {
		return fmt.Errorf("%w: cannot load allocation: %w", ErrReconciliationRequired, err)
	}
	leaseID := bundle[0].LeaseID
	expiry := bundle[0].LeaseExpiry
	if record.LeaseID == leaseID && record.LeaseExpiry.Equal(expiry) {
		return nil // already converged
	}
	if updater == nil {
		return fmt.Errorf("%w: allocation validator does not support lease updates", ErrReconciliationRequired)
	}
	if err := updater.UpdateReservationLease(allocationID, leaseID, expiry); err != nil {
		return fmt.Errorf("%w: cannot repair allocation lease metadata: %w", ErrReconciliationRequired, err)
	}
	return nil
}

var errNoChange = fmt.Errorf("reservation: no change")
