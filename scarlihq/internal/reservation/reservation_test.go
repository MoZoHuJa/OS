package reservation

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return NewStore(filepath.Join(dir, "reservations.jsonl"))
}

func newTestManager(t *testing.T) (*Manager, *FakeClock) {
	t.Helper()
	store := newTestStore(t)
	clock := &FakeClock{T: time.Now().UTC()}
	mgr, err := NewManager(store, DefaultPolicy(), clock, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, clock
}

func testIdentity(principal string) Identity {
	return Identity{Principal: principal}
}

func reconcilerIdentity() Identity {
	return Identity{Principal: "system.reconciler", IsReconciler: true}
}

// --- Store tests ---

func TestStore_AppendAndLoad(t *testing.T) {
	store := newTestStore(t)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, "gpu.nvidia.0", "agent")
	if err := store.Append(r); err != nil {
		t.Fatalf("Append: %v", err)
	}
	records, _, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("Load returned %d, want 1", len(records))
	}
}

func TestStore_Append_RejectsDuplicate(t *testing.T) {
	store := newTestStore(t)
	r := NewReservation("res-dup", "alloc-1", ResourceTypeGPUDevice, "gpu.nvidia.0", "agent")
	_ = store.Append(r)
	err := store.Append(r)
	if err != ErrDuplicateReservationID {
		t.Errorf("Append duplicate: got %v, want ErrDuplicateReservationID", err)
	}
}

func TestStore_Append_RejectsCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "res.jsonl")
	store := NewStore(path)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, "gpu.nvidia.0", "agent")
	_ = store.Append(r)
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	f.WriteString("{corrupt}\n")
	f.Close()
	r2 := NewReservation("res-2", "alloc-2", ResourceTypeGPUDevice, "gpu.nvidia.1", "agent")
	err := store.Append(r2)
	if err == nil {
		t.Fatal("Append to corrupt store should fail")
	}
}

func TestStore_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "res.jsonl")
	store := NewStore(path)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, "gpu.nvidia.0", "agent")
	_ = store.Append(r)
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("File mode %o, want 0600", info.Mode().Perm())
	}
}

// --- Policy tests ---

func TestPolicy_ValidateLeaseTTL(t *testing.T) {
	p := DefaultPolicy()
	if !p.ValidateLeaseTTL(5 * time.Minute) {
		t.Error("5min should be valid TTL")
	}
	if p.ValidateLeaseTTL(1 * time.Second) {
		t.Error("1s should be too short")
	}
}

func TestPolicy_Validate_RejectsInvalid(t *testing.T) {
	bad := DefaultPolicy()
	bad.MaxGPUPerReservation = 0
	if err := bad.Validate(); err == nil {
		t.Error("Policy with MaxGPUPerReservation=0 should be invalid")
	}
}

func TestPolicy_CanReserveGPU_Exclusive(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "gpu.nvidia.0", ResourceType: ResourceTypeGPUDevice,
			AllocationID: "alloc-A", Status: StatusHeld},
	}
	if p.CanReserveGPU("gpu.nvidia.0", existing, "alloc-B") {
		t.Error("Should not reserve GPU already held")
	}
	if !p.CanReserveGPU("gpu.nvidia.0", existing, "alloc-A") {
		t.Error("Should allow renewal by same allocation")
	}
}

// KPK-01: RELEASING blocks resource.
func TestPolicy_CanReserveGPU_ReleasingBlocks(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "gpu.nvidia.0", ResourceType: ResourceTypeGPUDevice,
			AllocationID: "alloc-A", Status: StatusReleasing},
	}
	if p.CanReserveGPU("gpu.nvidia.0", existing, "alloc-B") {
		t.Error("RELEASING should block new reservation")
	}
}

// KPK-01: EXPIRED_PENDING_RECONCILIATION blocks resource.
func TestPolicy_CanReserveGPU_ExpiredBlocks(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "gpu.nvidia.0", ResourceType: ResourceTypeGPUDevice,
			AllocationID: "alloc-A", Status: StatusExpiredPendingReconciliation},
	}
	if p.CanReserveGPU("gpu.nvidia.0", existing, "alloc-B") {
		t.Error("EXPIRED_PENDING_RECONCILIATION should block")
	}
}

// KPK-01: CONFLICT blocks resource.
func TestPolicy_CanReserveGPU_ConflictBlocks(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "gpu.nvidia.0", ResourceType: ResourceTypeGPUDevice,
			AllocationID: "alloc-A", Status: StatusConflict},
	}
	if p.CanReserveGPU("gpu.nvidia.0", existing, "alloc-B") {
		t.Error("CONFLICT should block")
	}
}

// KPK-01: RELEASED does NOT block.
func TestPolicy_CanReserveGPU_ReleasedDoesNotBlock(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "gpu.nvidia.0", ResourceType: ResourceTypeGPUDevice,
			AllocationID: "alloc-A", Status: StatusReleased},
	}
	if !p.CanReserveGPU("gpu.nvidia.0", existing, "alloc-B") {
		t.Error("RELEASED should NOT block")
	}
}

// KPK-02: Port reservation.
func TestPolicy_CanReservePort(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "port.30000", ResourceType: ResourceTypePort,
			AllocationID: "alloc-A", Status: StatusHeld},
	}
	if p.CanReservePort("port.30000", existing, "alloc-B") {
		t.Error("Same port should conflict")
	}
	if !p.CanReservePort("port.30001", existing, "alloc-B") {
		t.Error("Different port should succeed")
	}
}

// --- Manager tests ---

func TestManager_Reserve_Success(t *testing.T) {
	mgr, _ := newTestManager(t)
	result, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Identity:     testIdentity("agent"),
		LeaseTTL:     5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !result.Created {
		t.Error("Should be newly created")
	}
	if result.Reservation.Status != StatusHeld {
		t.Errorf("Status = %s, want HELD", result.Reservation.Status)
	}
}

func TestManager_Reserve_Conflict(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-B", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	if err == nil {
		t.Fatal("Should conflict on same GPU")
	}
}

// KPK-01: RELEASING reservation blocks new reservation for same GPU.
func TestManager_Reserve_ReleasingBlocks(t *testing.T) {
	mgr, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	// Transition to RELEASING without cleanup confirmed.
	_, _ = mgr.Release(result.Reservation.ReservationID, testIdentity("agent"), false)

	// New reservation for same GPU should fail.
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-B", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	if err == nil {
		t.Fatal("Should block when existing reservation is RELEASING")
	}
}

// KPK-02: Port reservation via manager.
func TestManager_Reserve_PortConflict(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: "port.30000",
		ResourceType: ResourceTypePort, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-B", ResourceID: "port.30000",
		ResourceType: ResourceTypePort, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	if err == nil {
		t.Fatal("Should conflict on same port")
	}
}

// KPK-06: Idempotency with fingerprint.
func TestManager_Reserve_Idempotent(t *testing.T) {
	mgr, _ := newTestManager(t)
	req := ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL:           5 * time.Minute,
		IdempotencyKey:     "idem-1",
		RequestFingerprint: "fp-123",
	}
	result1, err := mgr.Reserve(req)
	if err != nil {
		t.Fatalf("First reserve: %v", err)
	}
	if !result1.Created {
		t.Error("First should be created")
	}
	result2, err := mgr.Reserve(req)
	if err != nil {
		t.Fatalf("Idempotent reserve: %v", err)
	}
	if result2.Created {
		t.Error("Second should NOT be created")
	}
	if result2.Reservation.ReservationID != result1.Reservation.ReservationID {
		t.Error("Should return same reservation ID")
	}
}

// KPK-06: Idempotency key with empty fingerprint is rejected.
func TestManager_Reserve_IdempotencyKeyRequiresFingerprint(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL:       5 * time.Minute,
		IdempotencyKey: "idem-1",
		// RequestFingerprint intentionally empty
	})
	if err == nil {
		t.Fatal("Should reject idempotency key without fingerprint")
	}
}

// KPK-06: Same key + different fingerprint → conflict.
func TestManager_Reserve_IdempotencyConflict(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL:           5 * time.Minute,
		IdempotencyKey:     "idem-1",
		RequestFingerprint: "fp-A",
	})
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL:           5 * time.Minute,
		IdempotencyKey:     "idem-1",
		RequestFingerprint: "fp-B",
	})
	if err != ErrIdempotencyConflict {
		t.Errorf("Expected ErrIdempotencyConflict, got %v", err)
	}
}

// KPK-07: Zero TTL is rejected.
func TestManager_Reserve_ZeroTTLRejected(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 0, // zero — should be rejected
	})
	if err == nil {
		t.Fatal("Zero TTL should be rejected")
	}
}

// KPK-07: Negative TTL is rejected.
func TestManager_Reserve_NegativeTTLRejected(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: -1 * time.Minute,
	})
	if err == nil {
		t.Fatal("Negative TTL should be rejected")
	}
}

// KPK-08: Renewal after expiry is rejected.
func TestManager_RenewLease_AfterExpiryRejected(t *testing.T) {
	mgr, clock := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	// Advance clock past expiry.
	clock.Advance(10 * time.Minute)
	// Attempt renewal — should fail with ErrLeaseExpired.
	_, err := mgr.RenewLease(result.Reservation.ReservationID, testIdentity("agent"), 10*time.Minute)
	if err != ErrLeaseExpired {
		t.Errorf("Expected ErrLeaseExpired, got %v", err)
	}
}

func TestManager_RenewLease_Success(t *testing.T) {
	mgr, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	originalExpiry := result.Reservation.LeaseExpiry
	updated, err := mgr.RenewLease(result.Reservation.ReservationID, testIdentity("agent"), 10*time.Minute)
	if err != nil {
		t.Fatalf("RenewLease: %v", err)
	}
	if !updated.LeaseExpiry.After(originalExpiry) {
		t.Error("LeaseExpiry should be extended")
	}
}

func TestManager_RenewLease_NotOwned(t *testing.T) {
	mgr, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent-A"),
		LeaseTTL: 5 * time.Minute,
	})
	_, err := mgr.RenewLease(result.Reservation.ReservationID, testIdentity("agent-B"), 10*time.Minute)
	if err != ErrLeaseNotOwned {
		t.Errorf("Expected ErrLeaseNotOwned, got %v", err)
	}
}

// KPK-09: Release returns non-nil result for idempotent call.
func TestManager_Release_IdempotentReturnsNonNil(t *testing.T) {
	mgr, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	// First release with cleanup.
	r1, err := mgr.Release(result.Reservation.ReservationID, testIdentity("agent"), true)
	if err != nil {
		t.Fatalf("First release: %v", err)
	}
	if r1 == nil {
		t.Fatal("First release returned nil result")
	}
	if r1.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r1.Status)
	}
	// Second release (idempotent).
	r2, err := mgr.Release(result.Reservation.ReservationID, testIdentity("agent"), true)
	if err != nil {
		t.Errorf("Idempotent release: %v", err)
	}
	if r2 == nil {
		t.Fatal("Idempotent release returned nil result (KPK-09)")
	}
}

func TestManager_Release_Success(t *testing.T) {
	mgr, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	r1, err := mgr.Release(result.Reservation.ReservationID, testIdentity("agent"), false)
	if err != nil {
		t.Fatalf("Release (no cleanup): %v", err)
	}
	if r1.Status != StatusReleasing {
		t.Errorf("Status = %s, want RELEASING", r1.Status)
	}
	r2, err := mgr.Release(result.Reservation.ReservationID, testIdentity("agent"), true)
	if err != nil {
		t.Fatalf("Release (cleanup): %v", err)
	}
	if r2.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r2.Status)
	}
}

func TestManager_MarkExpiredCandidates(t *testing.T) {
	mgr, clock := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	marked, err := mgr.MarkExpiredCandidates()
	if err != nil {
		t.Fatalf("MarkExpiredCandidates: %v", err)
	}
	if marked != 1 {
		t.Errorf("Marked %d, want 1", marked)
	}
	loaded, _ := mgr.Get(result.Reservation.ReservationID)
	if loaded.Status != StatusExpiredPendingReconciliation {
		t.Errorf("Status = %s, want EXPIRED_PENDING_RECONCILIATION", loaded.Status)
	}
}

// KPK-10: ReconcileExpired requires trusted reconciler identity.
func TestManager_ReconcileExpired_NonReconcilerRejected(t *testing.T) {
	mgr, clock := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()

	// Non-reconciler should be rejected.
	_, err := mgr.ReconcileExpired(result.Reservation.ReservationID, testIdentity("agent"), CleanupEvidence{
		ResourceID:  "gpu.nvidia.0",
		OperationID: "op-1",
		ObservedAt:  clock.Now(),
		Verified:    true,
	})
	if err == nil {
		t.Fatal("Non-reconciler should be rejected")
	}
}

// KPK-10: ReconcileExpired with trusted reconciler + verified evidence → RELEASED.
func TestManager_ReconcileExpired_Released(t *testing.T) {
	mgr, clock := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()

	r, err := mgr.ReconcileExpired(result.Reservation.ReservationID, reconcilerIdentity(), CleanupEvidence{
		ResourceID:   "gpu.nvidia.0",
		OperationID:  "op-1",
		ObservedAt:   clock.Now(),
		Verified:     true,
		ReconcilerID: "system.reconciler",
	})
	if err != nil {
		t.Fatalf("ReconcileExpired: %v", err)
	}
	if r.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r.Status)
	}
}

// KPK-10: ReconcileExpired with unverified evidence → CONFLICT.
func TestManager_ReconcileExpired_Conflict(t *testing.T) {
	mgr, clock := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()

	r, err := mgr.ReconcileExpired(result.Reservation.ReservationID, reconcilerIdentity(), CleanupEvidence{
		ResourceID:   "gpu.nvidia.0",
		OperationID:  "op-2",
		ObservedAt:   clock.Now(),
		Verified:     false, // cleanup failed
		ReconcilerID: "system.reconciler",
	})
	if err != nil {
		t.Fatalf("ReconcileExpired: %v", err)
	}
	if r.Status != StatusConflict {
		t.Errorf("Status = %s, want CONFLICT", r.Status)
	}
}

// KPK-10: Evidence with mismatched resource_id is rejected.
func TestManager_ReconcileExpired_MismatchedResourceRejected(t *testing.T) {
	mgr, clock := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()

	_, err := mgr.ReconcileExpired(result.Reservation.ReservationID, reconcilerIdentity(), CleanupEvidence{
		ResourceID:  "gpu.nvidia.WRONG", // mismatch
		OperationID: "op-3",
		ObservedAt:  clock.Now(),
		Verified:    true,
	})
	if err == nil {
		t.Fatal("Mismatched resource_id should be rejected")
	}
}

// KPK-13: String() doesn't panic on short/empty IDs.
func TestReservation_String_PanicSafe(t *testing.T) {
	r := &Reservation{ReservationID: "", AllocationID: "x"}
	s := r.String()
	if s == "" {
		t.Error("String() returned empty")
	}
	// Also test nil receiver.
	var nilR *Reservation
	nilS := nilR.String()
	if nilS != "Reservation(nil)" {
		t.Errorf("Nil String() = %q, want 'Reservation(nil)'", nilS)
	}
}

// --- Concurrency tests ---

func TestManager_ConcurrentReserve_SameGPU(t *testing.T) {
	mgr, _ := newTestManager(t)
	var wg sync.WaitGroup
	successes := 0
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := mgr.Reserve(ReserveRequest{
				AllocationID: "alloc-" + string(rune('A'+i)),
				ResourceID:   "gpu.nvidia.0",
				ResourceType: ResourceTypeGPUDevice,
				Identity:     testIdentity("agent"),
				LeaseTTL:     5 * time.Minute,
			})
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if successes != 1 {
		t.Errorf("Concurrent reserve: %d successes, want exactly 1", successes)
	}
}

func TestManager_ConcurrentReserve_DifferentGPUs(t *testing.T) {
	mgr, _ := newTestManager(t)
	var wg sync.WaitGroup
	successes := 0
	var mu sync.Mutex
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := mgr.Reserve(ReserveRequest{
				AllocationID: "alloc-" + string(rune('A'+i)),
				ResourceID:   "gpu.nvidia." + string(rune('0'+i)),
				ResourceType: ResourceTypeGPUDevice,
				Identity:     testIdentity("agent"),
				LeaseTTL:     5 * time.Minute,
			})
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if successes != 5 {
		t.Errorf("Concurrent reserve different GPUs: %d successes, want 5", successes)
	}
}

// KPK-14: Renewal after scan prevents expiry marking.
func TestManager_MarkExpired_RenewedBeforeMark(t *testing.T) {
	mgr, clock := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	// Advance clock close to expiry but not past.
	clock.Advance(4 * time.Minute)
	// Renew the lease.
	_, _ = mgr.RenewLease(result.Reservation.ReservationID, testIdentity("agent"), 10*time.Minute)
	// Advance past the original expiry but within the renewed expiry.
	clock.Advance(2 * time.Minute)
	// MarkExpiredCandidates should NOT mark this as expired (it was renewed).
	marked, err := mgr.MarkExpiredCandidates()
	if err != nil {
		t.Fatalf("MarkExpiredCandidates: %v", err)
	}
	if marked != 0 {
		t.Errorf("Marked %d, want 0 (was renewed before marking)", marked)
	}
}
