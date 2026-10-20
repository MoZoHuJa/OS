package reservation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

// newRealAdapterHarness builds a Manager wired to the PRODUCTION
// AllocationReservationAdapter over real file-backed stores, so the release
// lifecycle is exercised exactly as it persists on disk.
// KPK-19.2.12-P2-03: focused regression tests for the release/lease-repair
// state matrix use this harness instead of the stub validator.
type realAdapterHarness struct {
	manager      *Manager
	clock        *FakeClock
	allocStore   *allocation.Store
	adapter      *AllocationReservationAdapter
	reservations []*Reservation
}

func newRealAdapterHarness(t *testing.T, cleanupVerified bool) *realAdapterHarness {
	t.Helper()
	dir := t.TempDir()
	clock := &FakeClock{T: time.Now().UTC()}
	allocStore := allocation.NewStore(filepath.Join(dir, "allocations.jsonl"))
	adapter, err := NewAllocationReservationAdapter(allocStore)
	if err != nil {
		t.Fatalf("NewAllocationReservationAdapter: %v", err)
	}
	snapshot, err := NewInventorySnapshotFromInventory("source-1", clock.Now(), []inventory.GPU{
		{ID: "gpu.nvidia.0", UUID: "GPU-12345678-abcd-1234-abcd-123456789abc", Vendor: "nvidia", Healthy: true},
		{ID: "gpu.nvidia.1", UUID: "GPU-87654321-dcba-4321-dcba-987654321cba", Vendor: "nvidia", Healthy: true},
	}, []inventory.Runtime{
		{ID: "sglang", Enabled: true, Healthy: true, Port: 30000},
	}, []inventory.Model{
		{ID: "qwen3-14b", Path: "/models/Qwen3-14B-AWQ", Format: "safetensors", Present: true},
	}, nil)
	if err != nil {
		t.Fatalf("NewInventorySnapshotFromInventory: %v", err)
	}
	inv := &stubInventoryProvider{snap: snapshot}
	store := NewStore(filepath.Join(dir, "reservations.jsonl"))
	cleanupVer := &stubCleanupVerifier{verified: cleanupVerified, clock: clock}
	mgr, err := NewManager(store, DefaultPolicy(), clock, inv, adapter, cleanupVer, newStubIdentityProvider())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return &realAdapterHarness{manager: mgr, clock: clock, allocStore: allocStore, adapter: adapter}
}

// reserveFullBundle creates a planned allocation with a GPU+runtime+model
// plan and reserves the full bundle through the production manager.
func (h *realAdapterHarness) reserveFullBundle(t *testing.T, allocationID string) *ReserveBundleResult {
	t.Helper()
	record := allocation.NewAllocation(allocationID, "v1", "coding", "interactive", "agent")
	if err := h.allocStore.Append(record); err != nil {
		t.Fatalf("Append allocation: %v", err)
	}
	for _, transition := range []struct {
		to     allocation.State
		reason string
	}{{allocation.StateValidating, "test validation"}, {allocation.StatePlanned, "test plan"}} {
		if err := h.allocStore.Update(allocationID, func(record *allocation.Allocation) error {
			_, err := record.Transition(transition.to, transition.reason)
			return err
		}); err != nil {
			t.Fatalf("transition %s: %v", transition.to, err)
		}
	}
	if err := h.allocStore.Update(allocationID, func(record *allocation.Allocation) error {
		record.SelectedGPU = "gpu.nvidia.0"
		record.SelectedRuntime = "sglang"
		record.SelectedModel = "qwen3-14b"
		return nil
	}); err != nil {
		t.Fatalf("set allocation plan: %v", err)
	}
	result, err := h.manager.ReserveBundle([]ReserveRequest{
		{AllocationID: allocationID, ResourceID: testGPUUUID0, ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute, IdempotencyKey: "idem-" + allocationID},
		{AllocationID: allocationID, ResourceID: "runtime.sglang", ResourceType: ResourceTypeRuntimeSlot, LeaseTTL: 5 * time.Minute, IdempotencyKey: "idem-" + allocationID},
		{AllocationID: allocationID, ResourceID: "model.qwen3-14b", ResourceType: ResourceTypeModelSlot, LeaseTTL: 5 * time.Minute, IdempotencyKey: "idem-" + allocationID},
	}, "user-token")
	if err != nil {
		t.Fatalf("ReserveBundle: %v", err)
	}
	h.reservations = result.Reservations
	return result
}

func (h *realAdapterHarness) allocationState(t *testing.T, allocationID string) allocation.State {
	t.Helper()
	record, err := h.allocStore.Get(allocationID)
	if err != nil {
		t.Fatalf("Get allocation %s: %v", allocationID, err)
	}
	return record.State
}

// failingAllocationStore wraps a real allocation store and fails every
// Update once the fail flag is raised — fault injection for the
// write-failure-after-reservation-release regression tests.
type failingAllocationStore struct {
	inner *allocation.Store
	fail  bool
}

func (f *failingAllocationStore) Load() (map[string]*allocation.Allocation, []error, error) {
	return f.inner.Load()
}

func (f *failingAllocationStore) Update(id string, modify func(*allocation.Allocation) error) error {
	if f.fail {
		return fmt.Errorf("injected allocation-store write failure")
	}
	return f.inner.Update(id, modify)
}

// --- KPK-19.2.12-P1-01: release lifecycle state matrix ---

// RESERVED allocation + all reservation records RELEASED → allocation
// reaches RELEASED, assignments cleared.
func TestReleaseReconciliation_AllReleased_AllocationReleasedAndCleared(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	h.reserveFullBundle(t, "alloc-1")

	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReserved {
		t.Fatalf("precondition: allocation state=%s, want RESERVED", got)
	}

	// Expire the lease and run the reaper: every member routes to
	// EXPIRED_PENDING_RECONCILIATION → verified cleanup → RELEASED.
	h.clock.Advance(10 * time.Minute)
	if _, err := h.manager.ReapExpiredOnce("reconciler-token"); err != nil {
		t.Fatalf("ReapExpiredOnce: %v", err)
	}

	record, err := h.allocStore.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	if record.State != allocation.StateReleased {
		t.Fatalf("allocation state=%s, want RELEASED", record.State)
	}
	if record.AssignedGPU != "" || record.AssignedRuntime != "" || record.AssignedModel != "" || record.AssignedPort != 0 {
		t.Fatalf("assignments not cleared: %+v", record)
	}
	if record.LeaseID != "" || !record.LeaseExpiry.IsZero() {
		t.Fatalf("lease metadata not cleared: lease=%q expiry=%v", record.LeaseID, record.LeaseExpiry)
	}
	if !record.CleanupVerified || record.CleanupStatus != "verified" {
		t.Fatalf("cleanup evidence not recorded: verified=%v status=%q", record.CleanupVerified, record.CleanupStatus)
	}
	// The allocation must never remain RESERVED after all reservations released.
	for _, r := range h.reservations {
		got, err := h.manager.Get(r.ReservationID)
		if err != nil {
			t.Fatalf("Get reservation: %v", err)
		}
		if got.Status != StatusReleased {
			t.Fatalf("reservation %s status=%s, want RELEASED", r.ReservationID, got.Status)
		}
	}
}

// RESERVED allocation + one member released and one still held → allocation
// enters RELEASING; assignments are NOT falsely cleared.
func TestReleaseReconciliation_PartialRelease_AllocationReleasing(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	// Release exactly one member through the user-initiated path.
	member := result.Reservations[0]
	if _, err := h.manager.RequestRelease(member.ReservationID, "user-token"); err != nil {
		t.Fatalf("RequestRelease: %v", err)
	}
	if _, err := h.manager.ConfirmRelease(member.ReservationID, "reconciler-token"); err != nil {
		t.Fatalf("ConfirmRelease: %v", err)
	}

	record, err := h.allocStore.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	if record.State != allocation.StateReleasing {
		t.Fatalf("allocation state=%s, want RELEASING after partial release", record.State)
	}
	if record.AssignedGPU == "" && record.AssignedRuntime == "" && record.AssignedModel == "" {
		t.Fatalf("assignments falsely cleared during partial release: %+v", record)
	}

	// Complete the release of the remaining members.
	for _, r := range result.Reservations[1:] {
		if _, err := h.manager.RequestRelease(r.ReservationID, "user-token"); err != nil {
			t.Fatalf("RequestRelease: %v", err)
		}
		if _, err := h.manager.ConfirmRelease(r.ReservationID, "reconciler-token"); err != nil {
			t.Fatalf("ConfirmRelease: %v", err)
		}
	}
	record, err = h.allocStore.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	if record.State != allocation.StateReleased {
		t.Fatalf("allocation state=%s, want RELEASED after full release", record.State)
	}
	if record.AssignedGPU != "" || record.AssignedRuntime != "" || record.AssignedModel != "" {
		t.Fatalf("assignments not cleared after full release: %+v", record)
	}
}

// One member CONFLICT → allocation becomes CLEANUP_REQUIRED; the overall
// release operation reports a typed error — no false success.
func TestReleaseReconciliation_ConflictMember_BlocksAllocationNoFalseSuccess(t *testing.T) {
	h := newRealAdapterHarness(t, false) // cleanup verifier reports failure
	h.reserveFullBundle(t, "alloc-1")

	h.clock.Advance(10 * time.Minute)
	_, err := h.manager.ReapExpiredOnce("reconciler-token")
	if err == nil {
		t.Fatal("ReapExpiredOnce must surface the blocked release (no false success)")
	}
	if !errors.Is(err, ErrReconciliationRequired) && !errors.Is(err, ErrAllocationReconciliationPending) {
		t.Fatalf("ReapExpiredOnce error=%v, want typed reconciliation error", err)
	}

	record, err := h.allocStore.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	if record.State != allocation.StateCleanupRequired {
		t.Fatalf("allocation state=%s, want CLEANUP_REQUIRED (operator-visible blocked state)", record.State)
	}
	// A blocked release must retain the assignment evidence for the operator.
	if record.AssignedGPU == "" {
		t.Fatalf("blocked allocation lost assignment evidence: %+v", record)
	}
	for _, r := range h.reservations {
		got, err := h.manager.Get(r.ReservationID)
		if err != nil {
			t.Fatalf("Get reservation: %v", err)
		}
		if got.Status != StatusConflict {
			t.Fatalf("reservation %s status=%s, want CONFLICT", r.ReservationID, got.Status)
		}
	}
}

// Allocation-store write failure after reservation release → typed
// reconciliation error is observable; restart repair finds the allocation
// and converges it.
func TestReleaseReconciliation_WriteFailure_TypedErrorAndRestartRepair(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	h.reserveFullBundle(t, "alloc-1")

	// Inject allocation-store write failures AFTER the bundle is committed.
	failing := &failingAllocationStore{inner: h.allocStore, fail: true}
	failingAdapter, err := NewAllocationReservationAdapter(failing)
	if err != nil {
		t.Fatalf("NewAllocationReservationAdapter: %v", err)
	}
	h.manager.allocVal = failingAdapter

	h.clock.Advance(10 * time.Minute)
	_, err = h.manager.ReapExpiredOnce("reconciler-token")
	if err == nil {
		t.Fatal("write failure after reservation release must surface a typed error")
	}
	if !errors.Is(err, ErrAllocationReconciliationPending) {
		t.Fatalf("error=%v, want ErrAllocationReconciliationPending", err)
	}
	// The reservation store is still authoritative: all members are RELEASED.
	for _, r := range h.reservations {
		got, err := h.manager.Get(r.ReservationID)
		if err != nil {
			t.Fatalf("Get reservation: %v", err)
		}
		if got.Status != StatusReleased {
			t.Fatalf("reservation %s status=%s, want RELEASED (durable reservation outcome)", r.ReservationID, got.Status)
		}
	}

	// Restart: the allocation store recovers, the adapter is healthy again.
	h.manager.allocVal = h.adapter
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReserved {
		t.Fatalf("pre-repair state=%s, want RESERVED (interrupted before completion persisted)", got)
	}
	if err := h.manager.ReconcileAllPending("reconciler-token"); err != nil {
		t.Fatalf("restart repair: %v", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleased {
		t.Fatalf("post-repair state=%s, want RELEASED (restart repair must converge)", got)
	}
}

// Restart after durable write boundaries: repeated reconciliation is
// idempotent — terminal states are preserved, no spurious errors.
func TestReleaseReconciliation_RestartIdempotent(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	h.reserveFullBundle(t, "alloc-1")
	h.clock.Advance(10 * time.Minute)
	if _, err := h.manager.ReapExpiredOnce("reconciler-token"); err != nil {
		t.Fatalf("first reaper pass: %v", err)
	}

	// Repeated recovery passes must converge to the same terminal state.
	for i := 0; i < 3; i++ {
		if err := h.manager.ReconcileAllPending("reconciler-token"); err != nil {
			t.Fatalf("repeated recovery pass %d: %v", i+1, err)
		}
		if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleased {
			t.Fatalf("repeated recovery pass %d: state=%s, want RELEASED", i+1, got)
		}
	}
}

// --- KPK-19.2.12-P1-02: reserved-lease reconciliation ---

// Renewal committed to the reservation store, allocation metadata stale →
// ReconcileReservedLeases repairs the allocation from the authoritative
// bundle.
func TestReconcileReservedLeases_RepairsStaleAllocationLease(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	// Simulate a failed allocation-store mirror write after a renewal: the
	// durable bundle carries the new lease, the allocation still has stale
	// metadata.
	h.clock.Advance(1 * time.Minute)
	if err := h.manager.RenewBundleLease("alloc-1", "user-token", 10*time.Minute); err != nil {
		t.Fatalf("RenewBundleLease: %v", err)
	}
	if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		record.LeaseID = "stale-lease"
		record.LeaseExpiry = h.clock.Now().Add(-time.Hour)
		return nil
	}); err != nil {
		t.Fatalf("corrupt allocation lease: %v", err)
	}

	if err := h.manager.ReconcileReservedLeases("reconciler-token"); err != nil {
		t.Fatalf("ReconcileReservedLeases: %v", err)
	}
	alloc, err := h.allocStore.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	bundleLease := result.Reservations[0].LeaseID
	bundleExpiry := result.Reservations[0].LeaseExpiry
	// Renewal rewrote the durable bundle; re-read it as the authority.
	got, err := h.manager.Get(result.Reservations[0].ReservationID)
	if err != nil {
		t.Fatalf("Get reservation: %v", err)
	}
	bundleLease = got.LeaseID
	bundleExpiry = got.LeaseExpiry
	if alloc.LeaseID != bundleLease || !alloc.LeaseExpiry.Equal(bundleExpiry) {
		t.Fatalf("allocation lease not converged: alloc=(%s,%s) bundle=(%s,%s)",
			alloc.LeaseID, alloc.LeaseExpiry, bundleLease, bundleExpiry)
	}
}

// Mismatched bundle lease IDs are rejected — the repair never trusts
// bundle[0] over an inconsistent bundle.
func TestReconcileReservedLeases_MismatchedBundle_RejectedNotRepaired(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	h.reserveFullBundle(t, "alloc-1")

	// Corrupt one bundle member's lease ID in the reservation store,
	// simulating an inconsistent multi-record bundle.
	first := h.reservations[0]
	if err := h.manager.store.update(first.ReservationID, func(r *Reservation) error {
		r.LeaseID = "divergent-lease"
		return nil
	}); err != nil {
		t.Fatalf("corrupt bundle member: %v", err)
	}
	if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		record.LeaseID = "original-lease"
		return nil
	}); err != nil {
		t.Fatalf("set allocation lease: %v", err)
	}

	err := h.manager.ReconcileReservedLeases("reconciler-token")
	if err == nil {
		t.Fatal("inconsistent bundle must be rejected, not silently repaired")
	}
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("error=%v, want ErrReconciliationRequired", err)
	}
	alloc, err := h.allocStore.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	if alloc.LeaseID != "original-lease" {
		t.Fatalf("allocation lease was overwritten from an inconsistent bundle: %q", alloc.LeaseID)
	}
}

// A validator lacking the scanning capability fails closed — "repair not
// supported" is never reported as success.
func TestReconcileReservedLeases_FailsClosedWithoutCapability(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	clock.Advance(time.Minute)
	err := mgr.ReconcileReservedLeases("reconciler-token")
	if err == nil {
		t.Fatal("missing scanning capability must fail closed, not return nil")
	}
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("error=%v, want ErrReconciliationRequired", err)
	}
}

// Corrupt allocation store → non-nil error (fail-closed read path).
func TestReconcileReservedLeases_CorruptAllocationStore_FailsClosed(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	h.reserveFullBundle(t, "alloc-1")

	// Append a corrupt line to the allocation store.
	f, err := os.OpenFile(h.allocStore.Path(), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open allocation store: %v", err)
	}
	if _, err := f.WriteString("{not-json}\n"); err != nil {
		t.Fatalf("corrupt allocation store: %v", err)
	}
	f.Close()

	if err := h.manager.ReconcileReservedLeases("reconciler-token"); err == nil {
		t.Fatal("corrupt allocation store must produce a non-nil error")
	}
}

// --- KPK-19.2.12-P2-01: expired/blocked routing during pending reconciliation ---

// Expired HELD bundle members are durably routed to the cleanup workflow
// (EXPIRED_PENDING_RECONCILIATION) and the allocation is persisted in an
// operator-visible blocked state instead of staying RESERVING forever; the
// subsequent reaper pass converges the full release.
func TestReconcilePending_ExpiredHeld_RoutedToCleanupThenConverges(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	h.reserveFullBundle(t, "alloc-1")

	// Simulate a crash between the durable reservation commit and the
	// allocation finalization: regress the allocation to RESERVING.
	if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		record.State = allocation.StateReserving
		return nil
	}); err != nil {
		t.Fatalf("regress allocation to RESERVING: %v", err)
	}
	// Expire the leases.
	h.clock.Advance(10 * time.Minute)

	err := h.manager.ReconcilePending("alloc-1", "reconciler-token")
	if err == nil {
		t.Fatal("expired bundle must refuse finalization")
	}
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("error=%v, want ErrReconciliationRequired", err)
	}

	// The expired members were durably routed to cleanup.
	for _, r := range h.reservations {
		got, err := h.manager.Get(r.ReservationID)
		if err != nil {
			t.Fatalf("Get reservation: %v", err)
		}
		if got.Status != StatusExpiredPendingReconciliation {
			t.Fatalf("reservation %s status=%s, want EXPIRED_PENDING_RECONCILIATION (routed to cleanup)", r.ReservationID, got.Status)
		}
	}
	// The allocation is operator-visible blocked, not silently RESERVING.
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateCleanupRequired {
		t.Fatalf("allocation state=%s, want CLEANUP_REQUIRED after expiry routing", got)
	}

	// The reaper pass verifies cleanup and completes the release — recovery
	// converges instead of retrying the same rejection forever.
	if _, err := h.manager.ReapExpiredOnce("reconciler-token"); err != nil {
		t.Fatalf("reaper convergence pass: %v", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleased {
		t.Fatalf("allocation state=%s, want RELEASED after convergence", got)
	}
}

// A CONFLICT member during pending reconciliation persists an
// operator-visible blocked state.
func TestReconcilePending_ConflictMember_BlocksOperatorVisible(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	h.reserveFullBundle(t, "alloc-1")

	// One member ends in CONFLICT (e.g. an earlier ambiguous cleanup).
	first := h.reservations[0]
	if err := h.manager.store.update(first.ReservationID, func(r *Reservation) error {
		return r.transition(StatusExpiredPendingReconciliation, h.clock.Now(), "test expiry")
	}); err != nil {
		t.Fatalf("mark expired: %v", err)
	}
	if err := h.manager.store.update(first.ReservationID, func(r *Reservation) error {
		return r.transition(StatusConflict, h.clock.Now(), "test ambiguity")
	}); err != nil {
		t.Fatalf("mark conflict: %v", err)
	}
	// Regress the allocation to RESERVING (crash simulation).
	if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		record.State = allocation.StateReserving
		return nil
	}); err != nil {
		t.Fatalf("regress allocation: %v", err)
	}

	err := h.manager.ReconcilePending("alloc-1", "reconciler-token")
	if err == nil || !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("error=%v, want ErrReconciliationRequired", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateCleanupRequired {
		t.Fatalf("allocation state=%s, want CLEANUP_REQUIRED (conflict is operator-visible)", got)
	}
}

// --- KPK-19.2.12-P2-02: strict central bundle validation ---

// One-field mismatches in every shared invariant → renewal is rejected and
// NEITHER store is written.
func TestBundleValidator_OneFieldMismatches_RejectRenewalWithoutWrites(t *testing.T) {
	baseBundle := func() []*Reservation {
		expiry := time.Now().UTC().Add(5 * time.Minute)
		return []*Reservation{
			{ReservationID: "res-a", AllocationID: "alloc-1", SchemaVersion: ReservationSchemaVersion, ResourceID: testGPUUUID0, ResourceType: ResourceTypeGPUDevice, Owner: "agent", Status: StatusHeld, LeaseID: "lease-1", LeaseExpiry: expiry, BundleSize: 2, IdempotencyKey: "idem-1", RequestFingerprint: "fp-1"},
			{ReservationID: "res-b", AllocationID: "alloc-1", SchemaVersion: ReservationSchemaVersion, ResourceID: "runtime.sglang", ResourceType: ResourceTypeRuntimeSlot, Owner: "agent", Status: StatusHeld, LeaseID: "lease-1", LeaseExpiry: expiry, BundleSize: 2, IdempotencyKey: "idem-1", RequestFingerprint: "fp-1"},
		}
	}

	// Every mutation must produce a fail-closed rejection (either through the
	// early per-record guards in RenewBundleLease or through the central
	// bundle validator) and NEITHER store may be written. The store-level
	// ValidateRecord additionally rejects empty owners at append time.
	mutations := map[string]func(bundle []*Reservation){
		"owner mismatch":           func(b []*Reservation) { b[1].Owner = "other-agent" },
		"lease id mismatch":        func(b []*Reservation) { b[1].LeaseID = "lease-2" },
		"lease expiry mismatch":    func(b []*Reservation) { b[1].LeaseExpiry = b[1].LeaseExpiry.Add(time.Minute) },
		"bundle size mismatch":     func(b []*Reservation) { b[1].BundleSize = 3 },
		"member status mismatch":   func(b []*Reservation) { b[1].Status = StatusReleasing },
		"duplicate resource":       func(b []*Reservation) { b[1].ResourceID = testGPUUUID0; b[1].ResourceType = ResourceTypeGPUDevice },
		"fingerprint mismatch":     func(b []*Reservation) { b[1].RequestFingerprint = "fp-2" },
		"idempotency key mismatch": func(b []*Reservation) { b[1].IdempotencyKey = "idem-2" },
		"allocation id mismatch":   func(b []*Reservation) { b[1].AllocationID = "alloc-2" },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			h := newRealAdapterHarness(t, true)
			bundle := baseBundle()
			mutate(bundle)
			// Persist the inconsistent bundle directly.
			for _, r := range bundle {
				r.CreatedAt = h.clock.Now()
				r.UpdatedAt = h.clock.Now()
				r.LastRenewedAt = h.clock.Now()
				if err := h.manager.store.append(r); err != nil {
					t.Fatalf("append reservation: %v", err)
				}
			}
			revisions := map[string]int64{}
			for _, r := range bundle {
				got, err := h.manager.Get(r.ReservationID)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				revisions[r.ReservationID] = got.Revision
			}

			err := h.manager.RenewBundleLease("alloc-1", "user-token", 10*time.Minute)
			if err == nil {
				t.Fatalf("%s: renewal of an inconsistent bundle must be rejected", name)
			}
			if !errors.Is(err, ErrReconciliationRequired) && !errors.Is(err, ErrLeaseNotOwned) {
				t.Fatalf("%s: error=%v, want a typed fail-closed rejection", name, err)
			}
			// Neither store may be written: revisions unchanged.
			for id, want := range revisions {
				got, err := h.manager.Get(id)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if got.Revision != want {
					t.Fatalf("%s: reservation %s revision=%d, want unchanged %d", name, id, got.Revision, want)
				}
			}
		})
	}
}

// FinalizeReservation rejects a logically inconsistent bundle before any
// assignment is written.
func TestFinalizeReservation_InconsistentBundle_Rejected(t *testing.T) {
	store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
	newPlannedAllocation(t, store, "alloc-1")
	if err := store.Update("alloc-1", func(record *allocation.Allocation) error {
		_, err := record.Transition(allocation.StateReserving, "test")
		return err
	}); err != nil {
		t.Fatalf("transition RESERVING: %v", err)
	}
	adapter, err := NewAllocationReservationAdapter(store)
	if err != nil {
		t.Fatalf("NewAllocationReservationAdapter: %v", err)
	}
	expiry := time.Now().Add(5 * time.Minute)
	// Two members with different lease IDs — individually valid, logically
	// inconsistent as one atomic bundle.
	reservations := []*Reservation{
		{AllocationID: "alloc-1", ResourceID: testGPUUUID0, ResourceType: ResourceTypeGPUDevice, Owner: "agent", Status: StatusHeld, LeaseID: "lease-a", LeaseExpiry: expiry, BundleSize: 2},
		{AllocationID: "alloc-1", ResourceID: "runtime.sglang", ResourceType: ResourceTypeRuntimeSlot, Owner: "agent", Status: StatusHeld, LeaseID: "lease-b", LeaseExpiry: expiry, BundleSize: 2},
	}
	if err := adapter.FinalizeReservation("alloc-1", reservations); err == nil {
		t.Fatal("inconsistent bundle must be rejected by FinalizeReservation")
	}
	record, err := store.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	if record.State != allocation.StateReserving {
		t.Fatalf("allocation state=%s, want RESERVING (no assignment written)", record.State)
	}
	if record.AssignedGPU != "" || record.AssignedRuntime != "" {
		t.Fatalf("assignments written from inconsistent bundle: %+v", record)
	}
}

// --- KPK-19.2.12-P1-02/P2-01: startup recovery orchestration ---

// Crash between the durable reservation commit and the allocation
// finalization → Recover finalizes the bundle and reports it in the summary.
func TestRecover_CrashBeforeFinalize_FinalizesBundle(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	h.reserveFullBundle(t, "alloc-1")

	// Simulate the crash: reservation bundle committed, allocation still
	// RESERVING (finalization write never happened).
	if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		record.State = allocation.StateReserving
		record.AssignedGPU = ""
		record.AssignedRuntime = ""
		record.AssignedModel = ""
		record.AssignedPort = 0
		return nil
	}); err != nil {
		t.Fatalf("regress allocation: %v", err)
	}

	summary, err := h.manager.Recover("reconciler-token")
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReserved {
		t.Fatalf("allocation state=%s, want RESERVED after recovery", got)
	}
	record, err := h.allocStore.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	if record.AssignedGPU == "" || record.AssignedRuntime == "" || record.AssignedModel == "" {
		t.Fatalf("assignments not restored by recovery: %+v", record)
	}
	if len(summary.Finalized) != 1 || summary.Finalized[0] != "alloc-1" {
		t.Fatalf("summary.Finalized=%v, want [alloc-1]", summary.Finalized)
	}
	if len(summary.Failed) != 0 {
		t.Fatalf("summary.Failed=%v, want empty", summary.Failed)
	}
}

// Lease drift between the reservation store and the allocation record →
// Recover repairs it and reports LeaseRepaired.
func TestRecover_LeaseDrift_RepairedInSummary(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	// Simulate the failed mirror write: allocation keeps a stale lease.
	if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		record.LeaseID = "stale-lease"
		record.LeaseExpiry = h.clock.Now().Add(-time.Hour)
		return nil
	}); err != nil {
		t.Fatalf("corrupt allocation lease: %v", err)
	}

	summary, err := h.manager.Recover("reconciler-token")
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	alloc, err := h.allocStore.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	got, err := h.manager.Get(result.Reservations[0].ReservationID)
	if err != nil {
		t.Fatalf("Get reservation: %v", err)
	}
	if alloc.LeaseID != got.LeaseID || !alloc.LeaseExpiry.Equal(got.LeaseExpiry) {
		t.Fatalf("lease not repaired: alloc=(%s,%s) bundle=(%s,%s)", alloc.LeaseID, alloc.LeaseExpiry, got.LeaseID, got.LeaseExpiry)
	}
	if len(summary.LeaseRepaired) != 1 || summary.LeaseRepaired[0] != "alloc-1" {
		t.Fatalf("summary.LeaseRepaired=%v, want [alloc-1]", summary.LeaseRepaired)
	}
}

// RESERVED allocation with an entirely RELEASED bundle (interrupted
// release whose allocation writes all failed) → Recover finds and converges
// it through the reserved-allocation scan.
func TestRecover_InterruptedRelease_ConvergesFromReserved(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	h.reserveFullBundle(t, "alloc-1")

	// Release every reservation member, but simulate total allocation-store
	// failure during the release events.
	failing := &failingAllocationStore{inner: h.allocStore, fail: true}
	failingAdapter, err := NewAllocationReservationAdapter(failing)
	if err != nil {
		t.Fatalf("NewAllocationReservationAdapter: %v", err)
	}
	h.manager.allocVal = failingAdapter
	h.clock.Advance(10 * time.Minute)
	if _, err := h.manager.ReapExpiredOnce("reconciler-token"); err == nil {
		t.Fatal("expected typed reconciliation error under injected write failures")
	}

	// All reservations are durably RELEASED; the allocation is still RESERVED.
	for _, r := range h.reservations {
		got, err := h.manager.Get(r.ReservationID)
		if err != nil {
			t.Fatalf("Get reservation: %v", err)
		}
		if got.Status != StatusReleased {
			t.Fatalf("reservation %s status=%s, want RELEASED", r.ReservationID, got.Status)
		}
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReserved {
		t.Fatalf("pre-recovery state=%s, want RESERVED (allocation writes failed)", got)
	}

	// Restart with a healthy adapter: recovery must find the allocation via
	// the reserved-allocation scan and complete the release.
	h.manager.allocVal = h.adapter
	if err := h.manager.ReconcileAllPending("reconciler-token"); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleased {
		t.Fatalf("post-recovery state=%s, want RELEASED", got)
	}
}
