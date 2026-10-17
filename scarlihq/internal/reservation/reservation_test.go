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
	mgr := NewManager(store, DefaultPolicy(), clock)
	return mgr, clock
}

// --- Store tests ---

func TestStore_AppendAndLoad(t *testing.T) {
	store := newTestStore(t)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, "gpu.nvidia.0", "agent.coder")
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

	// Append corrupt line directly.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	f.WriteString("{corrupt}\n")
	f.Close()

	// Next Append must fail.
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

func TestStore_Update_TransitionPersists(t *testing.T) {
	store := newTestStore(t)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, "gpu.nvidia.0", "agent")
	_ = store.Append(r)

	_ = store.Update("res-1", func(r *Reservation) error {
		r.Status = StatusReleasing
		return nil
	})

	loaded, _ := store.Get("res-1")
	if loaded.Status != StatusReleasing {
		t.Errorf("Status after reload = %s, want RELEASING", loaded.Status)
	}
	if loaded.Revision != 1 {
		t.Errorf("Revision = %d, want 1", loaded.Revision)
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
	if p.ValidateLeaseTTL(2 * time.Hour) {
		t.Error("2h should be too long")
	}
}

func TestPolicy_IsInventoryStale(t *testing.T) {
	p := DefaultPolicy()
	if !p.IsInventoryStale(60 * time.Second) {
		t.Error("60s should be stale (limit 30s)")
	}
	if p.IsInventoryStale(10 * time.Second) {
		t.Error("10s should not be stale")
	}
}

func TestPolicy_CanReserveGPU_Exclusive(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "gpu.nvidia.0", ResourceType: ResourceTypeGPUDevice,
			AllocationID: "alloc-A", Status: StatusHeld},
	}
	// Same GPU, different allocation → conflict.
	if p.CanReserveGPU("gpu.nvidia.0", existing, "alloc-B") {
		t.Error("Should not reserve GPU already held by another alloc")
	}
	// Same GPU, same allocation (renewal) → OK.
	if !p.CanReserveGPU("gpu.nvidia.0", existing, "alloc-A") {
		t.Error("Should allow renewal by same allocation")
	}
	// Different GPU → OK.
	if !p.CanReserveGPU("gpu.nvidia.1", existing, "alloc-B") {
		t.Error("Should reserve different GPU")
	}
}

// --- Manager tests ---

func TestManager_Reserve_Success(t *testing.T) {
	mgr, _ := newTestManager(t)
	result, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Owner:        "agent.coder",
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
	if result.Reservation.LeaseExpiry.IsZero() {
		t.Error("LeaseExpiry should be set")
	}
}

func TestManager_Reserve_Conflict(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Owner:        "agent",
		LeaseTTL:     5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("First reserve: %v", err)
	}

	// Second reserve for same GPU, different allocation → conflict.
	_, err = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-B",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Owner:        "agent",
		LeaseTTL:     5 * time.Minute,
	})
	if err == nil {
		t.Fatal("Should conflict on same GPU")
	}
}

func TestManager_Reserve_Idempotent(t *testing.T) {
	mgr, _ := newTestManager(t)
	req := ReserveRequest{
		AllocationID:       "alloc-1",
		ResourceID:         "gpu.nvidia.0",
		ResourceType:       ResourceTypeGPUDevice,
		Owner:              "agent",
		LeaseTTL:           5 * time.Minute,
		IdempotencyKey:     "idem-1",
		RequestFingerprint: "fp-123",
	}
	result1, err := mgr.Reserve(req)
	if err != nil {
		t.Fatalf("First reserve: %v", err)
	}
	if !result1.Created {
		t.Error("First should be newly created")
	}

	// Second reserve with same key + fingerprint → return existing.
	result2, err := mgr.Reserve(req)
	if err != nil {
		t.Fatalf("Idempotent reserve: %v", err)
	}
	if result2.Created {
		t.Error("Second should NOT be newly created")
	}
	if result2.Reservation.ReservationID != result1.Reservation.ReservationID {
		t.Error("Should return same reservation ID")
	}
}

func TestManager_Reserve_IdempotencyConflict(t *testing.T) {
	mgr, _ := newTestManager(t)
	req1 := ReserveRequest{
		AllocationID:       "alloc-1",
		ResourceID:         "gpu.nvidia.0",
		ResourceType:       ResourceTypeGPUDevice,
		Owner:              "agent",
		LeaseTTL:           5 * time.Minute,
		IdempotencyKey:     "idem-1",
		RequestFingerprint: "fp-A",
	}
	_, err := mgr.Reserve(req1)
	if err != nil {
		t.Fatalf("First reserve: %v", err)
	}

	// Same key, different fingerprint → conflict.
	req2 := req1
	req2.RequestFingerprint = "fp-B"
	_, err = mgr.Reserve(req2)
	if err != ErrIdempotencyConflict {
		t.Errorf("Expected ErrIdempotencyConflict, got %v", err)
	}
}

func TestManager_Reserve_StaleInventory(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID:         "alloc-1",
		ResourceID:           "gpu.nvidia.0",
		ResourceType:         ResourceTypeGPUDevice,
		Owner:                "agent",
		LeaseTTL:             5 * time.Minute,
		InventorySnapshotAge: 60 * time.Second, // stale (>30s limit)
	})
	if err != ErrStaleInventory {
		t.Errorf("Expected ErrStaleInventory, got %v", err)
	}
}

func TestManager_Reserve_InvalidTTL(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Owner:        "agent",
		LeaseTTL:     1 * time.Second, // too short (min 30s)
	})
	if err == nil {
		t.Fatal("Should reject too-short TTL")
	}
}

func TestManager_RenewLease_Success(t *testing.T) {
	mgr, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Owner:        "agent",
		LeaseTTL:     5 * time.Minute,
	})
	originalExpiry := result.Reservation.LeaseExpiry

	updated, err := mgr.RenewLease(result.Reservation.ReservationID, "agent", 10*time.Minute)
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
		AllocationID: "alloc-1",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Owner:        "agent-A",
		LeaseTTL:     5 * time.Minute,
	})
	_, err := mgr.RenewLease(result.Reservation.ReservationID, "agent-B", 10*time.Minute)
	if err != ErrLeaseNotOwned {
		t.Errorf("Expected ErrLeaseNotOwned, got %v", err)
	}
}

func TestManager_Release_Success(t *testing.T) {
	mgr, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Owner:        "agent",
		LeaseTTL:     5 * time.Minute,
	})

	// Release without cleanup confirmed → RELEASING.
	r1, err := mgr.Release(result.Reservation.ReservationID, "agent", false)
	if err != nil {
		t.Fatalf("Release (no cleanup): %v", err)
	}
	if r1.Status != StatusReleasing {
		t.Errorf("Status = %s, want RELEASING", r1.Status)
	}

	// Release with cleanup confirmed → RELEASED.
	r2, err := mgr.Release(result.Reservation.ReservationID, "agent", true)
	if err != nil {
		t.Fatalf("Release (cleanup confirmed): %v", err)
	}
	if r2.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r2.Status)
	}
}

func TestManager_Release_Idempotent(t *testing.T) {
	mgr, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Owner:        "agent",
	})
	// First release with cleanup.
	_, _ = mgr.Release(result.Reservation.ReservationID, "agent", true)
	// Second release should not error (idempotent).
	_, err := mgr.Release(result.Reservation.ReservationID, "agent", true)
	if err != nil {
		t.Errorf("Idempotent release: %v", err)
	}
}

func TestManager_MarkExpiredCandidates(t *testing.T) {
	mgr, clock := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Owner:        "agent",
		LeaseTTL:     5 * time.Minute,
	})

	// Advance clock past lease expiry.
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

func TestManager_ReconcileExpired_Released(t *testing.T) {
	mgr, clock := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Owner:        "agent",
		LeaseTTL:     5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()

	// Reconcile with cleanup confirmed → RELEASED.
	r, err := mgr.ReconcileExpired(result.Reservation.ReservationID, true)
	if err != nil {
		t.Fatalf("ReconcileExpired: %v", err)
	}
	if r.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r.Status)
	}
}

func TestManager_ReconcileExpired_Conflict(t *testing.T) {
	mgr, clock := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.nvidia.0",
		ResourceType: ResourceTypeGPUDevice,
		Owner:        "agent",
		LeaseTTL:     5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()

	// Reconcile without cleanup confirmed → CONFLICT (fail-closed).
	r, err := mgr.ReconcileExpired(result.Reservation.ReservationID, false)
	if err != nil {
		t.Fatalf("ReconcileExpired: %v", err)
	}
	if r.Status != StatusConflict {
		t.Errorf("Status = %s, want CONFLICT", r.Status)
	}
}

// --- Concurrency test ---

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
				Owner:        "agent",
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
				Owner:        "agent",
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
