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
type stubInventoryProvider struct {
	snap InventorySnapshot
}

func (s *stubInventoryProvider) GetSnapshot() InventorySnapshot {
	return s.snap
}

func newStubInventory() *stubInventoryProvider {
	return &stubInventoryProvider{
		snap: NewInventorySnapshot("gen-1", time.Now().UTC(), []string{
			"gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc",
			"gpu.nvidia.GPU-87654321-dcba-4321-dcba-987654321cba",
			"runtime.sglang",
			"runtime.vllm",
			"model.qwen3-14b",
			"port.30000",
			"port.30001",
		}),
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

// stubCleanupVerifier implements CleanupVerifier for testing.
// P1-01 (v19.2.8): Replaces caller-supplied CleanupEvidence.
type stubCleanupVerifier struct {
	verified bool
	clock    Clock
}

func (s *stubCleanupVerifier) VerifyCleanup(resourceID string, reconciler Identity) (CleanupEvidence, error) {
	ts := s.clock.Now()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return newCleanupEvidence(resourceID, "op-test", ts, s.verified, reconciler.Principal()), nil
}

func newTestManager(t *testing.T) (*Manager, *FakeClock, *stubInventoryProvider) {
	t.Helper()
	store := newTestStore(t)
	clock := &FakeClock{T: time.Now().UTC()}
	inv := newStubInventory()
	// Refresh snapshot AsOf to current fake time.
	inv.snap = NewInventorySnapshot("gen-1", clock.Now(), []string{
		"gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc",
		"gpu.nvidia.GPU-87654321-dcba-4321-dcba-987654321cba",
		"runtime.sglang",
		"runtime.vllm",
		"model.qwen3-14b",
		"port.30000",
		"port.30001",
	})
	allocVal := newStubAllocValidator("alloc-1", "alloc-A", "alloc-B")
	cleanupVer := &stubCleanupVerifier{verified: true, clock: clock}
	mgr, err := NewManager(store, DefaultPolicy(), clock, inv, allocVal, cleanupVer)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, clock, inv
}

const testGPUUUID0 = "gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc"
const testGPUUUID1 = "gpu.nvidia.GPU-87654321-dcba-4321-dcba-987654321cba"

func testUser(principal string) Identity { return NewUserIdentity(principal) }
func testReconciler() Identity           { return newReconcilerIdentity("system.reconciler") }

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

// P2-03: Invalid persisted records rejected on load.
func TestStore_Load_RejectsInvalidRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "res.jsonl")
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

// P1-04: GPU UUID case canonicalization.
func TestCanonicalResourceID_GPUUUIDCaseCanonicalized(t *testing.T) {
	// Uppercase hex variant.
	upper := "gpu.nvidia.GPU-ABCDEF12-ABCD-1234-ABCD-123456789ABC"
	canon, err := CanonicalResourceID(upper, ResourceTypeGPUDevice)
	if err != nil {
		t.Fatalf("Valid uppercase UUID rejected: %v", err)
	}
	// Should be canonicalized to lowercase hex (except "GPU-" prefix).
	expected := "gpu.nvidia.GPU-abcdef12-abcd-1234-abcd-123456789abc"
	if canon != expected {
		t.Errorf("Canon = %q, want %q (lowercase hex)", canon, expected)
	}
}

// P1-04: Case variants normalize to same ID.
func TestCanonicalResourceID_CaseVariantsSameCanon(t *testing.T) {
	upper := "gpu.nvidia.GPU-ABCDEF12-ABCD-1234-ABCD-123456789ABC"
	lower := "gpu.nvidia.GPU-abcdef12-abcd-1234-abcd-123456789abc"
	canonUpper, _ := CanonicalResourceID(upper, ResourceTypeGPUDevice)
	canonLower, _ := CanonicalResourceID(lower, ResourceTypeGPUDevice)
	if canonUpper != canonLower {
		t.Errorf("Case variants not canonicalized to same ID: %q vs %q", canonUpper, canonLower)
	}
}

// P1-05: Invalid GPU UUID rejected.
func TestCanonicalResourceID_InvalidGPUUUID(t *testing.T) {
	_, err := CanonicalResourceID("gpu.nvidia.GPU-uuid-0", ResourceTypeGPUDevice)
	if err == nil {
		t.Fatal("Invalid GPU UUID should be rejected")
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

// P1-05: Invalid port.
func TestCanonicalResourceID_InvalidPort(t *testing.T) {
	invalidPorts := []string{"port.0", "port.65536", "port.abc", "port.030000"}
	for _, p := range invalidPorts {
		_, err := CanonicalResourceID(p, ResourceTypePort)
		if err == nil {
			t.Errorf("Port %q should be rejected", p)
		}
	}
}

// P1-03: NewInventorySnapshot exported constructor.
func TestNewInventorySnapshot_Exported(t *testing.T) {
	snap := NewInventorySnapshot("gen-1", time.Now().UTC(), []string{"gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc"})
	if !snap.ResourceExists("gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc") {
		t.Error("ResourceExists should return true for listed resource")
	}
	if snap.ResourceExists("gpu.nvidia.GPU-nonexistent") {
		t.Error("ResourceExists should return false for unlisted resource")
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
	_, err := NewManager(nil, DefaultPolicy(), nil, nil, nil, nil)
	if err == nil {
		t.Fatal("Nil deps should be rejected")
	}
}

// P1-01: NewManager rejects nil cleanupVer.
func TestNewManager_RejectsNilCleanupVerifier(t *testing.T) {
	store := newTestStore(t)
	inv := newStubInventory()
	allocVal := newStubAllocValidator("alloc-1")
	_, err := NewManager(store, DefaultPolicy(), nil, inv, allocVal, nil)
	if err == nil {
		t.Fatal("Nil cleanupVer should be rejected")
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

// P1-01/P2-02: ConfirmRelease uses CleanupVerifier (no caller evidence).
func TestManager_RequestRelease_ConfirmRelease(t *testing.T) {
	mgr, _, _ := newTestManager(t)
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
	// ConfirmRelease now takes only reconciler identity (no evidence argument).
	r2, err := mgr.ConfirmRelease(result.Reservation.ReservationID, testReconciler())
	if err != nil {
		t.Fatalf("ConfirmRelease: %v", err)
	}
	if r2.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r2.Status)
	}
}

// P2-02: Idempotent ConfirmRelease returns non-nil.
func TestManager_ConfirmRelease_IdempotentNonNil(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, _ = mgr.RequestRelease(result.Reservation.ReservationID, testUser("agent"))
	_, _ = mgr.ConfirmRelease(result.Reservation.ReservationID, testReconciler())
	// Second confirm — should return existing record, not nil.
	r2, err := mgr.ConfirmRelease(result.Reservation.ReservationID, testReconciler())
	if err != nil {
		t.Fatalf("Idempotent confirm: %v", err)
	}
	if r2 == nil {
		t.Fatal("P2-02: Idempotent ConfirmRelease returned nil (should be non-nil)")
	}
	if r2.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r2.Status)
	}
}

// P1-01: Non-reconciler identity cannot ConfirmRelease.
func TestManager_ConfirmRelease_NonReconcilerRejected(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, _ = mgr.RequestRelease(result.Reservation.ReservationID, testUser("agent"))
	_, err := mgr.ConfirmRelease(result.Reservation.ReservationID, testUser("agent"))
	if err == nil {
		t.Fatal("Non-reconciler should be rejected")
	}
}

// P1-01: NewReconcilerIdentity is NOT exported (cannot forge from outside).
// This test verifies that only newReconcilerIdentity (package-private) can
// create a reconciler identity. NewUserIdentity always has IsReconciler=false.
func TestIdentity_CannotForgeReconciler(t *testing.T) {
	id := NewUserIdentity("system.reconciler")
	if id.IsReconciler() {
		t.Fatal("NewUserIdentity should NOT be able to create reconciler identity")
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
}

func TestManager_Reserve_IdempotencyKeyDifferentResource(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL:       5 * time.Minute,
		IdempotencyKey: "idem-1",
	})
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

// P2-01: ReconcileExpired returns non-nil (uses CleanupVerifier).
func TestManager_ReconcileExpired_ReturnsNonNil(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()
	// ReconcileExpired now takes only reconciler identity (no evidence argument).
	r, err := mgr.ReconcileExpired(result.Reservation.ReservationID, testReconciler())
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

// P2-01: ReconcileExpired with unverified cleanup → CONFLICT.
func TestManager_ReconcileExpired_ConflictNonNil(t *testing.T) {
	store := newTestStore(t)
	clock := &FakeClock{T: time.Now().UTC()}
	inv := newStubInventory()
	inv.snap = NewInventorySnapshot("gen-1", clock.Now(), []string{testGPUUUID0})
	allocVal := newStubAllocValidator("alloc-1")
	cleanupVer := &stubCleanupVerifier{verified: false, clock: clock} // cleanup fails
	mgr, _ := NewManager(store, DefaultPolicy(), clock, inv, allocVal, cleanupVer)

	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, Identity: testUser("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()

	r, err := mgr.ReconcileExpired(result.Reservation.ReservationID, testReconciler())
	if err != nil {
		t.Fatalf("ReconcileExpired: %v", err)
	}
	if r == nil {
		t.Fatal("ReconcileExpired returned nil")
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
	r1, _ := mgr.RequestRelease(result.Reservation.ReservationID, testUser("agent"))
	rev1 := r1.Revision
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
