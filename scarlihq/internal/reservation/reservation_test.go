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

// stubInventoryProvider implements InventoryProvider for testing.
type stubInventoryProvider struct {
	gen    string
	age    time.Duration
	exists map[string]bool
}

func (s *stubInventoryProvider) SnapshotGeneration() string { return s.gen }
func (s *stubInventoryProvider) SnapshotAge() time.Duration { return s.age }
func (s *stubInventoryProvider) ResourceExists(id string, rt ResourceType) bool {
	return s.exists[id]
}

func newStubInventory() *stubInventoryProvider {
	return &stubInventoryProvider{
		gen: "gen-1",
		age: 1 * time.Second,
		exists: map[string]bool{
			"gpu.nvidia.GPU-uuid-0": true,
			"gpu.nvidia.GPU-uuid-1": true,
			"runtime.sglang":        true,
			"model.qwen3-14b-awq":   true,
			"port.30000":            true,
			"port.30001":            true,
		},
	}
}

// stubAllocationValidator implements AllocationValidator for testing.
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
	allocVal := newStubAllocValidator("alloc-1", "alloc-A", "alloc-B")
	mgr, err := NewManager(store, DefaultPolicy(), clock, inv, allocVal)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, clock, inv
}

func testIdentity(principal string) Identity {
	return Identity{Principal: principal}
}

func reconcilerIdentity() Identity {
	return Identity{Principal: "system.reconciler", IsReconciler: true}
}

func validEvidence(resourceID string, clock Clock) CleanupEvidence {
	return CleanupEvidence{
		ResourceID:   resourceID,
		OperationID:  "op-1",
		ObservedAt:   clock.Now(),
		Verified:     true,
		ReconcilerID: "system.reconciler",
	}
}

// --- Store tests ---

func TestStore_AppendAndLoad(t *testing.T) {
	store := newTestStore(t)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, "gpu.nvidia.0", "agent")
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
	r := NewReservation("res-dup", "alloc-1", ResourceTypeGPUDevice, "gpu.nvidia.0", "agent")
	_ = store.append(r)
	err := store.append(r)
	if err != ErrDuplicateReservationID {
		t.Errorf("append duplicate: got %v, want ErrDuplicateReservationID", err)
	}
}

func TestStore_Append_RejectsCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "res.jsonl")
	store := NewStore(path)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, "gpu.nvidia.0", "agent")
	_ = store.append(r)
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	f.WriteString("{corrupt}\n")
	f.Close()
	r2 := NewReservation("res-2", "alloc-2", ResourceTypeGPUDevice, "gpu.nvidia.1", "agent")
	err := store.append(r2)
	if err == nil {
		t.Fatal("append to corrupt store should fail")
	}
}

func TestStore_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "res.jsonl")
	store := NewStore(path)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, "gpu.nvidia.0", "agent")
	_ = store.append(r)
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("File mode %o, want 0600", info.Mode().Perm())
	}
}

// P1-01: Store mutation methods are package-private.
func TestStore_MutationMethodsArePackagePrivate(t *testing.T) {
	// This is a compile-time check: if Append/Update/PruneReleased were
	// exported, external packages could call them. Since they're now
	// lowercase (package-private), only Manager and tests in this package
	// can call them. This test verifies the methods exist with correct names.
	store := newTestStore(t)
	_ = store.append        // must be lowercase
	_ = store.update        // must be lowercase
	_ = store.pruneReleased // must be lowercase
	// If these compile, the methods are package-private (correct).
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
	bad.MaxRuntimeSlotsPerRuntime = 0
	if err := bad.Validate(); err == nil {
		t.Error("Policy with MaxRuntimeSlotsPerRuntime=0 should be invalid")
	}
}

func TestPolicy_CanReserveGPU_Exclusive(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "gpu.nvidia.0", ResourceType: ResourceTypeGPUDevice,
			AllocationID: "alloc-A", Status: StatusHeld},
	}
	if p.CanReserveGPU("gpu.nvidia.0", existing) {
		t.Error("Should not reserve GPU already held")
	}
	if !p.CanReserveGPU("gpu.nvidia.1", existing) {
		t.Error("Should allow different GPU")
	}
}

// P1-02: Unknown status blocks.
func TestPolicy_CanReserveGPU_UnknownStatusBlocks(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "gpu.nvidia.0", ResourceType: ResourceTypeGPUDevice,
			AllocationID: "alloc-A", Status: ReservationStatus("UNKNOWN_FUTURE")},
	}
	if p.CanReserveGPU("gpu.nvidia.0", existing) {
		t.Error("Unknown status should block (fail-closed)")
	}
}

// P1-02: Empty status blocks.
func TestPolicy_CanReserveGPU_EmptyStatusBlocks(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "gpu.nvidia.0", ResourceType: ResourceTypeGPUDevice,
			AllocationID: "alloc-A", Status: ""},
	}
	if p.CanReserveGPU("gpu.nvidia.0", existing) {
		t.Error("Empty status should block (fail-closed)")
	}
}

// P1-01/KPK-01: RELEASING blocks.
func TestPolicy_CanReserveGPU_ReleasingBlocks(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "gpu.nvidia.0", AllocationID: "alloc-A", Status: StatusReleasing},
	}
	if p.CanReserveGPU("gpu.nvidia.0", existing) {
		t.Error("RELEASING should block")
	}
}

// P1-01/KPK-01: RELEASED does NOT block.
func TestPolicy_CanReserveGPU_ReleasedDoesNotBlock(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "gpu.nvidia.0", AllocationID: "alloc-A", Status: StatusReleased},
	}
	if !p.CanReserveGPU("gpu.nvidia.0", existing) {
		t.Error("RELEASED should NOT block")
	}
}

// P1-05: Repeated reservations by same allocation do NOT bypass capacity.
func TestPolicy_CanReserveRuntimeSlot_SameAllocationCounts(t *testing.T) {
	p := DefaultPolicy() // MaxRuntimeSlotsPerRuntime = 1
	existing := []*Reservation{
		{ResourceID: "runtime.sglang", ResourceType: ResourceTypeRuntimeSlot,
			AllocationID: "alloc-A", Status: StatusHeld},
	}
	// Same allocation trying to reserve again — should fail (capacity=1, already 1).
	if p.CanReserveRuntimeSlot("runtime.sglang", existing) {
		t.Error("Same allocation should NOT bypass capacity")
	}
}

// KPK-02: Port reservation.
func TestPolicy_CanReservePort(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: "port.30000", ResourceType: ResourceTypePort,
			AllocationID: "alloc-A", Status: StatusHeld},
	}
	if p.CanReservePort("port.30000", existing) {
		t.Error("Same port should conflict")
	}
	if !p.CanReservePort("port.30001", existing) {
		t.Error("Different port should succeed")
	}
}

// P2-03: Canonical resource ID validation.
func TestCanonicalResourceID(t *testing.T) {
	cases := []struct {
		id    string
		rt    ResourceType
		valid bool
	}{
		{"gpu.nvidia.GPU-uuid-0", ResourceTypeGPUDevice, true},
		{"gpu.nvidia.", ResourceTypeGPUDevice, false}, // missing uuid
		{"gpu.0", ResourceTypeGPUDevice, false},       // wrong prefix
		{"runtime.sglang", ResourceTypeRuntimeSlot, true},
		{"sglang", ResourceTypeRuntimeSlot, false}, // wrong prefix
		{"model.qwen3", ResourceTypeModelSlot, true},
		{"port.30000", ResourceTypePort, true},
		{"port", ResourceTypePort, false},
		{"", ResourceTypePort, false},
	}
	for _, c := range cases {
		_, err := CanonicalResourceID(c.id, c.rt)
		if (err == nil) != c.valid {
			t.Errorf("CanonicalResourceID(%q, %q): valid=%v, want %v, err=%v", c.id, c.rt, err == nil, c.valid, err)
		}
	}
}

// --- Manager tests ---

func TestManager_Reserve_Success(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.nvidia.GPU-uuid-0",
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

// P2-03: Non-canonical resource ID is rejected.
func TestManager_Reserve_NonCanonicalResourceIDRejected(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   "gpu.0", // wrong prefix
		ResourceType: ResourceTypeGPUDevice,
		Identity:     testIdentity("agent"),
		LeaseTTL:     5 * time.Minute,
	})
	if err == nil {
		t.Fatal("Non-canonical resource ID should be rejected")
	}
}

// P1-07: Allocation validation.
func TestManager_Reserve_InvalidAllocationRejected(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "nonexistent-alloc",
		ResourceID:   "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice,
		Identity:     testIdentity("agent"),
		LeaseTTL:     5 * time.Minute,
	})
	if err == nil {
		t.Fatal("Nonexistent allocation should be rejected")
	}
}

// P1-06: Nil inventory provider rejected at construction.
func TestNewManager_RejectsNilInventory(t *testing.T) {
	store := newTestStore(t)
	_, err := NewManager(store, DefaultPolicy(), nil, nil, nil)
	if err == nil {
		t.Fatal("Nil inventory provider should be rejected")
	}
}

// P2-05: Nil store rejected.
func TestNewManager_RejectsNilStore(t *testing.T) {
	_, err := NewManager(nil, DefaultPolicy(), nil, newStubInventory(), newStubAllocValidator("alloc-1"))
	if err == nil {
		t.Fatal("Nil store should be rejected")
	}
}

func TestManager_Reserve_Conflict(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-B", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
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
		ResourceType: ResourceTypeRuntimeSlot, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	// Same allocation, same runtime — should fail (capacity=1, already 1).
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: "runtime.sglang",
		ResourceType: ResourceTypeRuntimeSlot, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	if err == nil {
		t.Fatal("Same allocation should NOT bypass capacity")
	}
}

// P1-04: Release uses RequestRelease/ConfirmRelease (no boolean).
func TestManager_RequestRelease_ConfirmRelease(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})

	// Request release → RELEASING.
	r1, err := mgr.RequestRelease(result.Reservation.ReservationID, testIdentity("agent"))
	if err != nil {
		t.Fatalf("RequestRelease: %v", err)
	}
	if r1.Status != StatusReleasing {
		t.Errorf("Status = %s, want RELEASING", r1.Status)
	}

	// Confirm release with evidence → RELEASED.
	r2, err := mgr.ConfirmRelease(result.Reservation.ReservationID, reconcilerIdentity(),
		validEvidence("gpu.nvidia.GPU-uuid-0", clock))
	if err != nil {
		t.Fatalf("ConfirmRelease: %v", err)
	}
	if r2.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r2.Status)
	}
}

// P1-04: ConfirmRelease without evidence fails.
func TestManager_ConfirmRelease_RequiresEvidence(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, _ = mgr.RequestRelease(result.Reservation.ReservationID, testIdentity("agent"))

	// Non-reconciler should be rejected.
	_, err := mgr.ConfirmRelease(result.Reservation.ReservationID, testIdentity("agent"), CleanupEvidence{
		ResourceID: "gpu.nvidia.GPU-uuid-0", OperationID: "op-1",
		ObservedAt: time.Now(), Verified: true, ReconcilerID: "agent",
	})
	if err == nil {
		t.Fatal("Non-reconciler should be rejected")
	}
}

// P1-03: Reconciler identity must match evidence.
func TestManager_ConfirmRelease_ReconcilerMismatch(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, _ = mgr.RequestRelease(result.Reservation.ReservationID, testIdentity("agent"))

	// ReconcilerID mismatch.
	ev := validEvidence("gpu.nvidia.GPU-uuid-0", clock)
	ev.ReconcilerID = "different.reconciler"
	_, err := mgr.ConfirmRelease(result.Reservation.ReservationID, reconcilerIdentity(), ev)
	if err == nil {
		t.Fatal("Reconciler ID mismatch should be rejected")
	}
}

// P1-03: Future timestamp rejected.
func TestManager_ConfirmRelease_FutureTimestampRejected(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, _ = mgr.RequestRelease(result.Reservation.ReservationID, testIdentity("agent"))

	ev := validEvidence("gpu.nvidia.GPU-uuid-0", clock)
	ev.ObservedAt = clock.Now().Add(1 * time.Hour) // future
	_, err := mgr.ConfirmRelease(result.Reservation.ReservationID, reconcilerIdentity(), ev)
	if err == nil {
		t.Fatal("Future timestamp should be rejected")
	}
}

// P1-03: Zero timestamp rejected.
func TestManager_ConfirmRelease_ZeroTimestampRejected(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	_, _ = mgr.RequestRelease(result.Reservation.ReservationID, testIdentity("agent"))

	ev := CleanupEvidence{
		ResourceID: "gpu.nvidia.GPU-uuid-0", OperationID: "op-1",
		ObservedAt: time.Time{}, // zero
		Verified:   true, ReconcilerID: "system.reconciler",
	}
	_, err := mgr.ConfirmRelease(result.Reservation.ReservationID, reconcilerIdentity(), ev)
	if err == nil {
		t.Fatal("Zero timestamp should be rejected")
	}
}

// KPK-06: Idempotency with fingerprint.
func TestManager_Reserve_Idempotent(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	req := ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
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
}

// KPK-06: Empty fingerprint with key rejected.
func TestManager_Reserve_IdempotencyKeyRequiresFingerprint(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL:       5 * time.Minute,
		IdempotencyKey: "idem-1",
	})
	if err == nil {
		t.Fatal("Should reject idempotency key without fingerprint")
	}
}

// KPK-07: Zero TTL rejected.
func TestManager_Reserve_ZeroTTLRejected(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 0,
	})
	if err == nil {
		t.Fatal("Zero TTL should be rejected")
	}
}

// KPK-08: Renewal after expiry rejected.
func TestManager_RenewLease_AfterExpiryRejected(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, err := mgr.RenewLease(result.Reservation.ReservationID, testIdentity("agent"), 10*time.Minute)
	if err != ErrLeaseExpired {
		t.Errorf("Expected ErrLeaseExpired, got %v", err)
	}
}

func TestManager_RenewLease_Success(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
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

// P2-01: RequestRelease from EXPIRED state.
func TestManager_RequestRelease_FromExpired(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()

	r, err := mgr.RequestRelease(result.Reservation.ReservationID, testIdentity("agent"))
	if err != nil {
		t.Fatalf("RequestRelease from EXPIRED: %v", err)
	}
	if r.Status != StatusReleasing {
		t.Errorf("Status = %s, want RELEASING", r.Status)
	}
}

// P2-02: MarkExpiredCandidates accurate count.
func TestManager_MarkExpiredCandidates_AccurateCount(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
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
	// Second call should mark 0 (already marked).
	marked2, _ := mgr.MarkExpiredCandidates()
	if marked2 != 0 {
		t.Errorf("Second mark: %d, want 0", marked2)
	}
}

// P2-02: Renewed reservation not marked expired.
func TestManager_MarkExpired_RenewedBeforeMark(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-uuid-0",
		ResourceType: ResourceTypeGPUDevice, Identity: testIdentity("agent"),
		LeaseTTL: 5 * time.Minute,
	})
	clock.Advance(4 * time.Minute)
	_, _ = mgr.RenewLease(result.Reservation.ReservationID, testIdentity("agent"), 10*time.Minute)
	clock.Advance(2 * time.Minute) // past original expiry but within renewed
	marked, _ := mgr.MarkExpiredCandidates()
	if marked != 0 {
		t.Errorf("Marked %d, want 0 (was renewed)", marked)
	}
}

// KPK-13: String() panic-safe.
func TestReservation_String_PanicSafe(t *testing.T) {
	r := &Reservation{ReservationID: "", AllocationID: "x"}
	s := r.String()
	if s == "" {
		t.Error("String() returned empty")
	}
	var nilR *Reservation
	nilS := nilR.String()
	if nilS != "Reservation(nil)" {
		t.Errorf("Nil String() = %q", nilS)
	}
}

// --- Concurrency tests ---

func TestManager_ConcurrentReserve_SameGPU(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	// Pre-register all allocation IDs before spawning goroutines (avoid map race).
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
				ResourceID:   "gpu.nvidia.GPU-uuid-0",
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
