package reservation

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// --- Test helpers ---

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return NewStore(filepath.Join(dir, "reservations.jsonl"))
}

// stubInventoryProvider implements InventoryProvider with coherent snapshots.
// P1-03 (v19.2.7): Uses InventorySnapshot (one coherent object).
type stubInventoryProvider struct {
	snap InventorySnapshot
}

func (s *stubInventoryProvider) GetSnapshot() InventorySnapshot {
	return s.snap
}

func newStubInventory() *stubInventoryProvider {
	return &stubInventoryProvider{
		snap: InventorySnapshot{
			Generation: "gen-1",
			AsOf:       time.Now().UTC(),
			resources: map[string]bool{
				"gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc": true,
				"gpu.nvidia.GPU-87654321-dcba-4321-dcba-987654321cba": true,
				"runtime.sglang":  true,
				"runtime.vllm":    true,
				"model.qwen3-14b": true,
				"port.30000":      true,
				"port.30001":      true,
			},
		},
	}
}

type stubAllocationValidator struct {
	valid map[string]bool
}

func (s *stubAllocationValidator) ValidateForReservation(allocID string) error {
	if s.valid[allocID] {
		return nil
	}
	return fmt.Errorf("allocation %q not found or not eligible", allocID)
}

func newStubAllocValidator(validIDs ...string) *stubAllocationValidator {
	m := make(map[string]bool)
	for _, id := range validIDs {
		m[id] = true
	}
	return &stubAllocationValidator{valid: m}
}

func newTestManager(t *testing.T) (*Manager, *FakeClock, *stubInventoryProvider) {
	t.Helper()
	store := newTestStore(t)
	clock := &FakeClock{T: time.Now().UTC()}
	inv := newStubInventory()
	// Refresh snapshot AsOf to current fake time.
	inv.snap.AsOf = clock.Now()
	allocVal := newStubAllocValidator("alloc-1", "alloc-A", "alloc-B")
	mgr, err := NewManager(store, DefaultPolicy(), clock, inv, allocVal)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, clock, inv
}

// Valid GPU UUID for tests (P1-05: proper format).
const testGPUUUID0 = "gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc"
const testGPUUUID1 = "gpu.nvidia.GPU-87654321-dcba-4321-dcba-987654321cba"

func testUser(principal string) Identity { return NewUserIdentity(principal) }
func testReconciler() Identity           { return NewReconcilerIdentity("system.reconciler") }

func validEvidence(resourceID string, clock Clock) CleanupEvidence {
	return NewCleanupEvidence(resourceID, "op-1", clock.Now(), true, "system.reconciler")
}

// --- Store tests ---

func TestStore_AppendAndLoad(t *testing.T) {
	store := newTestStore(t)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, testGPUUUID0, "agent")
	if err := store.append(r); err != nil {
		t.Fatalf("append: %v", err)
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
	r := NewReservation("res-dup", "alloc-1", ResourceTypeGPUDevice, testGPUUUID0, "agent")
	_ = store.append(r)
	err := store.append(r)
	if err != ErrDuplicateReservationID {
		t.Errorf("got %v, want ErrDuplicateReservationID", err)
	}
}

func TestStore_Append_RejectsCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "res.jsonl")
	store := NewStore(path)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, testGPUUUID0, "agent")
	_ = store.append(r)
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	f.WriteString("{corrupt}\n")
	f.Close()
	r2 := NewReservation("res-2", "alloc-2", ResourceTypeGPUDevice, testGPUUUID1, "agent")
	err := store.append(r2)
	if err == nil {
		t.Fatal("append to corrupt store should fail")
	}
}

func TestStore_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "res.jsonl")
	store := NewStore(path)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, testGPUUUID0, "agent")
	_ = store.append(r)
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("File mode %o, want 0600", info.Mode().Perm())
	}
}

// P2-03: Invalid persisted records are rejected on load.
func TestStore_Load_RejectsInvalidRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "res.jsonl")
	// Write a record with invalid schema version directly.
	f, _ := os.Create(path)
	f.WriteString(`{"reservation_id":"r1","allocation_id":"a1","schema_version":"v99","resource_id":"x","resource_type":"gpu.device","owner":"o","status":"HELD","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","revision":0}` + "\n")
	f.Close()
	store := NewStore(path)
	_, corrupt, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(corrupt) == 0 {
		t.Error("Expected corrupt records for invalid schema_version")
	}
}

// --- Policy tests ---

func TestPolicy_ValidateLeaseTTL(t *testing.T) {
	p := DefaultPolicy()
	if !p.ValidateLeaseTTL(5 * time.Minute) {
		t.Error("5min should be valid TTL")
	}
}

func TestPolicy_Validate_RejectsInvalid(t *testing.T) {
	bad := DefaultPolicy()
	bad.MaxRuntimeSlotsPerRuntime = 0
	if err := bad.Validate(); err == nil {
		t.Error("Should reject MaxRuntimeSlotsPerRuntime=0")
	}
}

func TestPolicy_CanReserveGPU_Exclusive(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: testGPUUUID0, AllocationID: "alloc-A", Status: StatusHeld},
	}
	if p.CanReserveGPU(testGPUUUID0, existing) {
		t.Error("Should not reserve already held GPU")
	}
	if !p.CanReserveGPU(testGPUUUID1, existing) {
		t.Error("Should allow different GPU")
	}
}

// P1-02: Unknown status blocks.
func TestPolicy_CanReserveGPU_UnknownStatusBlocks(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: testGPUUUID0, AllocationID: "alloc-A", Status: ReservationStatus("UNKNOWN")},
	}
	if p.CanReserveGPU(testGPUUUID0, existing) {
		t.Error("Unknown status should block (fail-closed)")
	}
}

// P1-05: CanonicalResourceID with valid GPU UUID.
func TestCanonicalResourceID_ValidGPUUUID(t *testing.T) {
	id := testGPUUUID0
	canon, err := CanonicalResourceID(id, ResourceTypeGPUDevice)
	if err != nil {
		t.Fatalf("Valid GPU UUID rejected: %v", err)
	}
	if canon != id {
		t.Errorf("Canon = %q, want %q", canon, id)
	}
}

// P1-05: Invalid GPU UUID rejected.
func TestCanonicalResourceID_InvalidGPUUUID(t *testing.T) {
	_, err := CanonicalResourceID("gpu.nvidia.GPU-uuid-0", ResourceTypeGPUDevice)
	if err == nil {
		t.Fatal("Invalid GPU UUID should be rejected")
	}
}

// P1-05: Port canonicalization (leading zeros removed).
func TestCanonicalResourceID_PortLeadingZeros(t *testing.T) {
	canon, err := CanonicalResourceID("port.030000", ResourceTypePort)
	if err == nil {
		t.Fatalf("Leading zeros should be rejected: got %q", canon)
	}
}

// P1-05: Port normalization.
func TestCanonicalResourceID_PortNormalized(t *testing.T) {
	canon, err := CanonicalResourceID("port.30000", ResourceTypePort)
	if err != nil {
		t.Fatalf("Valid port rejected: %v", err)
	}
	if canon != "port.30000" {
		t.Errorf("Canon = %q, want port.30000", canon)
	}
}

// P1-05: Invalid port (0, 65536, non-numeric).
func TestCanonicalResourceID_InvalidPort(t *testing.T) {
	invalidPorts := []string{"port.0", "port.65536", "port.abc", "port.-1"}
	for _, p := range invalidPorts {
		_, err := CanonicalResourceID(p, ResourceTypePort)
		if err == nil {
			t.Errorf("Port %q should be rejected", p)
		}
	}
}

// P1-05: Runtime name validation.
func TestCanonicalResourceID_InvalidRuntimeName(t *testing.T) {
	_, err := CanonicalResourceID("runtime.SGLang", ResourceTypeRuntimeSlot) // uppercase
	if err == nil {
		t.Error("Uppercase runtime name should be rejected")
	}
}

// --- Manager tests ---

func TestManager_Reserve_Success(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice,
		Identity:     testUser("agent"),
		LeaseTTL:     5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !result.Created {
		t.Error("Should be newly created")
	}
}

func TestManager_Reserve_NonCanonicalResourceIDRejected(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.0",
		ResourceType: ResourceTypeGPUDevice,
		Identity:     testUser("agent"),
		LeaseTTL:     5 * time.Minute,
	})
	if err == nil {
		t.Fatal("Non-canonical resource ID should be rejected")
	}
}

func TestManager_Reserve_InvalidAllocationRejected(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "nonexistent",
		ResourceID:   testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice,
		Identity:     testUser("agent"),
		LeaseTTL:     5 * time.Minute,
	})
	if err == nil {
		t.Fatal("Nonexistent allocation should be rejected")
	}
}

func TestNewManager_RejectsNilDeps(t *testing.T) {
	store := newTestStore(t)
	_, err := NewManager(nil, DefaultPolicy(), nil, nil, nil)
	if err == nil {
		t.Fatal("Nil deps should be rejected")
	}
	_, err = NewManager(store, DefaultPolicy(), nil, nil, nil)
	if err == nil {
		t.Fatal("Nil inventory/allocVal should be rejected")
	}
}

func TestManager_Reserve_Conflict(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-B", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	if err == nil {
		t.Fatal("Should conflict on same GPU")
	}
}

// P1-05: Same allocation cannot bypass capacity.
func TestManager_Reserve_SameAllocationCannotBypassCapacity(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: "runtime.sglang",
		ResourceType: ResourceTypeRuntimeSlot, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: "runtime.sglang",
		ResourceType: ResourceTypeRuntimeSlot, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	if err == nil {
		t.Fatal("Same allocation should NOT bypass capacity")
	}
}

// P1-04: RequestRelease/ConfirmRelease.
func TestManager_RequestRelease_ConfirmRelease(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	r1, err := mgr.RequestRelease(result.Reservation.ReservationID, testUser("agent"))
	if err != nil {
		t.Fatalf("RequestRelease: %v", err)
	}
	if r1.Status != StatusReleasing {
		t.Errorf("Status = %s, want RELEASING", r1.Status)
	}
	r2, err := mgr.ConfirmRelease(result.Reservation.ReservationID, testReconciler(),
		validEvidence(testGPUUUID0, clock))
	if err != nil {
		t.Fatalf("ConfirmRelease: %v", err)
	}
	if r2.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r2.Status)
	}
}

// P1-01: Forged reconciler identity rejected (unforgeable struct).
func TestManager_ConfirmRelease_NonReconcilerRejected(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, _ = mgr.RequestRelease(result.Reservation.ReservationID, testUser("agent"))
	// Non-reconciler identity.
	_, err := mgr.ConfirmRelease(result.Reservation.ReservationID, testUser("agent"),
		validEvidence(testGPUUUID0, clock))
	if err == nil {
		t.Fatal("Non-reconciler should be rejected")
	}
}

// P1-03: Reconciler identity must match evidence.
func TestManager_ConfirmRelease_ReconcilerMismatch(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, _ = mgr.RequestRelease(result.Reservation.ReservationID, testUser("agent"))
	ev := validEvidence(testGPUUUID0, clock)
	// Use mismatched reconciler ID.
	ev = NewCleanupEvidence(testGPUUUID0, "op-1", clock.Now(), true, "different.reconciler")
	_, err := mgr.ConfirmRelease(result.Reservation.ReservationID, testReconciler(), ev)
	if err == nil {
		t.Fatal("Reconciler ID mismatch should be rejected")
	}
}

// P1-04: Idempotency fingerprint computed internally.
func TestManager_Reserve_Idempotent(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	req := ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL:       5 * time.Minute,
		IdempotencyKey: "idem-1",
	}
	result1, err := mgr.Reserve(req)
	if err != nil {
		t.Fatalf("First: %v", err)
	}
	if !result1.Created {
		t.Error("First should be created")
	}
	result2, err := mgr.Reserve(req)
	if err != nil {
		t.Fatalf("Idempotent: %v", err)
	}
	if result2.Created {
		t.Error("Second should NOT be created")
	}
	if result2.Reservation.ReservationID != result1.Reservation.ReservationID {
		t.Error("Should return same ID")
	}
}

// P1-04: Same key + different resource → conflict.
func TestManager_Reserve_IdempotencyKeyDifferentResource(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL:       5 * time.Minute,
		IdempotencyKey: "idem-1",
	})
	// Same key, different resource → fingerprint will differ → conflict.
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID1,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL:       5 * time.Minute,
		IdempotencyKey: "idem-1",
	})
	if err != ErrIdempotencyConflict {
		t.Errorf("Expected ErrIdempotencyConflict, got %v", err)
	}
}

func TestManager_Reserve_ZeroTTLRejected(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 0,
	})
	if err == nil {
		t.Fatal("Zero TTL should be rejected")
	}
}

func TestManager_RenewLease_AfterExpiryRejected(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, err := mgr.RenewLease(result.Reservation.ReservationID, testUser("agent"), 10*time.Minute)
	if err != ErrLeaseExpired {
		t.Errorf("Expected ErrLeaseExpired, got %v", err)
	}
}

// P2-01: ReconcileExpired returns non-nil (was: nil, nil).
func TestManager_ReconcileExpired_ReturnsNonNil(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()

	// Verified evidence → RELEASED.
	r, err := mgr.ReconcileExpired(result.Reservation.ReservationID, testReconciler(),
		validEvidence(testGPUUUID0, clock))
	if err != nil {
		t.Fatalf("ReconcileExpired: %v", err)
	}
	if r == nil {
		t.Fatal("P2-01: ReconcileExpired returned nil result (should be non-nil)")
	}
	if r.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r.Status)
	}
}

// P2-01: ReconcileExpired with unverified evidence → CONFLICT (also non-nil).
func TestManager_ReconcileExpired_ConflictNonNil(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()

	ev := NewCleanupEvidence(testGPUUUID0, "op-2", clock.Now(), false, "system.reconciler")
	r, err := mgr.ReconcileExpired(result.Reservation.ReservationID, testReconciler(), ev)
	if err != nil {
		t.Fatalf("ReconcileExpired: %v", err)
	}
	if r == nil {
		t.Fatal("P2-01: ReconcileExpired returned nil result")
	}
	if r.Status != StatusConflict {
		t.Errorf("Status = %s, want CONFLICT", r.Status)
	}
}

// P2-04: No-op RequestRelease doesn't increment revision.
func TestManager_RequestRelease_NoOpNoRevisionIncrement(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	// First release → RELEASING.
	r1, _ := mgr.RequestRelease(result.Reservation.ReservationID, testUser("agent"))
	rev1 := r1.Revision
	// Second release → no-op (already RELEASING).
	r2, _ := mgr.RequestRelease(result.Reservation.ReservationID, testUser("agent"))
	if r2.Revision != rev1 {
		t.Errorf("Revision changed on no-op: %d → %d (P2-04)", rev1, r2.Revision)
	}
}

func TestReservation_String_PanicSafe(t *testing.T) {
	r := &Reservation{ReservationID: "", AllocationID: "x"}
	s := r.String()
	if s == "" {
		t.Error("String() returned empty")
	}
	var nilR *Reservation
	if nilS := nilR.String(); nilS != "Reservation(nil)" {
		t.Errorf("Nil String() = %q", nilS)
	}
}

// --- Concurrency tests ---

func TestManager_ConcurrentReserve_SameGPU(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	allocVal := mgr.allocVal.(*stubAllocationValidator)
	for i := 0; i < 10; i++ {
		allocVal.valid[fmt.Sprintf("alloc-%d", i)] = true
	}
	var wg sync.WaitGroup
	successes := 0
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := mgr.Reserve(ReserveRequest{
				AllocationID: fmt.Sprintf("alloc-%d", i),
				ResourceID:   testGPUUUID0,
				ResourceType: ResourceTypeGPUDevice,
				Identity:     testUser("agent"),
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
		t.Errorf("Concurrent reserve: %d successes, want 1", successes)
	}
}

// P2-02: MarkExpiredCandidates accurate count.
func TestManager_MarkExpiredCandidates_AccurateCount(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	marked, _ := mgr.MarkExpiredCandidates()
	if marked != 1 {
		t.Errorf("Marked %d, want 1", marked)
	}
	marked2, _ := mgr.MarkExpiredCandidates()
	if marked2 != 0 {
		t.Errorf("Second mark: %d, want 0", marked2)
	}
}
