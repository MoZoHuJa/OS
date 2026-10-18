package reservation

import (
	"errors"
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

type stubInventoryProvider struct {
	snap InventorySnapshot
}

func (s *stubInventoryProvider) GetSnapshot() InventorySnapshot { return s.snap }

func newStubInventory() *stubInventoryProvider {
	return &stubInventoryProvider{
		snap: NewInventorySnapshot("gen-1", time.Now().UTC(), []string{
			"gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc",
			"gpu.nvidia.GPU-87654321-dcba-4321-dcba-987654321cba",
			"runtime.sglang", "runtime.vllm",
			"model.qwen3-14b", "port.30000", "port.30001",
		}),
	}
}

// stubAllocationValidator implements the extended AllocationValidator.
// P1-02 (v19.2.9): Includes BeginReservation/EndReservation.
type stubAllocationValidator struct {
	mu         sync.Mutex
	valid      map[string]bool
	claimed    map[string]bool
	endErr     error
	beginCalls int
}

func (s *stubAllocationValidator) ValidateForReservation(allocID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.valid[allocID] {
		return fmt.Errorf("allocation %q not eligible", allocID)
	}
	return nil
}

func (s *stubAllocationValidator) BeginReservation(allocID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beginCalls++
	if !s.valid[allocID] {
		return fmt.Errorf("allocation %q not eligible for BeginReservation", allocID)
	}
	s.claimed[allocID] = true
	return nil
}

func (s *stubAllocationValidator) EndReservation(allocID string, success bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimed[allocID] = false
	if success && s.endErr != nil {
		return s.endErr
	}
	if success {
		// In a real implementation, this would transition to RESERVED.
	}
	return nil
}

func newStubAllocValidator(validIDs ...string) *stubAllocationValidator {
	m := make(map[string]bool)
	for _, id := range validIDs {
		m[id] = true
	}
	return &stubAllocationValidator{valid: m, claimed: make(map[string]bool)}
}

type stubCleanupVerifier struct {
	verified bool
	clock    Clock
}

func (s *stubCleanupVerifier) VerifyCleanup(resourceID string, reconciler Identity) (CleanupEvidence, error) {
	ts := s.clock.Now()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return NewCleanupEvidence(resourceID, "op-test", ts, s.verified, reconciler.Principal()), nil
}

// stubIdentityProvider implements TrustedIdentityProvider for testing.
// P1-01 (v19.2.9): Resolves tokens to identities.
type stubIdentityProvider struct {
	tokens map[string]Identity
}

func (s *stubIdentityProvider) ResolveIdentity(token string) (Identity, error) {
	id, ok := s.tokens[token]
	if !ok {
		return Identity{}, fmt.Errorf("unknown token %q", token)
	}
	return id, nil
}

func newStubIdentityProvider() *stubIdentityProvider {
	return &stubIdentityProvider{
		tokens: map[string]Identity{
			"user-token":       NewUserIdentity("agent"),
			"reconciler-token": newReconcilerIdentity("system.reconciler"),
		},
	}
}

func newTestManager(t *testing.T) (*Manager, *FakeClock, *stubInventoryProvider) {
	t.Helper()
	store := newTestStore(t)
	clock := &FakeClock{T: time.Now().UTC()}
	inv := newStubInventory()
	inv.snap = NewInventorySnapshot("gen-1", clock.Now(), []string{
		"gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc",
		"gpu.nvidia.GPU-87654321-dcba-4321-dcba-987654321cba",
		"runtime.sglang", "runtime.vllm",
		"model.qwen3-14b", "port.30000", "port.30001",
	})
	allocVal := newStubAllocValidator("alloc-1", "alloc-A", "alloc-B")
	cleanupVer := &stubCleanupVerifier{verified: true, clock: clock}
	idProv := newStubIdentityProvider()
	mgr, err := NewManager(store, DefaultPolicy(), clock, inv, allocVal, cleanupVer, idProv)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, clock, inv
}

const testGPUUUID0 = "gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc"
const testGPUUUID1 = "gpu.nvidia.GPU-87654321-dcba-4321-dcba-987654321cba"

// --- Store tests ---

func TestStore_AppendAndLoad(t *testing.T) {
	store := newTestStore(t)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, testGPUUUID0, "agent")
	// Set lease fields so ValidateRecord passes.
	r.LeaseID = "lease-1"
	r.LeaseExpiry = time.Now().Add(5 * time.Minute)
	r.LastRenewedAt = time.Now()
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
	r.LeaseID = "lease-1"
	r.LeaseExpiry = time.Now().Add(5 * time.Minute)
	r.LastRenewedAt = time.Now()
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
	r.LeaseID = "l1"
	r.LeaseExpiry = time.Now().Add(5 * time.Minute)
	r.LastRenewedAt = time.Now()
	_ = store.append(r)
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	f.WriteString("{corrupt}\n")
	f.Close()
	r2 := NewReservation("res-2", "alloc-2", ResourceTypeGPUDevice, testGPUUUID1, "agent")
	r2.LeaseID = "l2"
	r2.LeaseExpiry = time.Now().Add(5 * time.Minute)
	r2.LastRenewedAt = time.Now()
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
	r.LeaseID = "l1"
	r.LeaseExpiry = time.Now().Add(5 * time.Minute)
	r.LastRenewedAt = time.Now()
	_ = store.append(r)
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("File mode %o, want 0600", info.Mode().Perm())
	}
}

// P2-03: Read methods return corruption error.
func TestStore_Get_ReturnsCorruptionError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "res.jsonl")
	store := NewStore(path)
	r := NewReservation("res-1", "alloc-1", ResourceTypeGPUDevice, testGPUUUID0, "agent")
	r.LeaseID = "l1"
	r.LeaseExpiry = time.Now().Add(5 * time.Minute)
	r.LastRenewedAt = time.Now()
	_ = store.append(r)
	// Corrupt the file.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	f.WriteString("{corrupt}\n")
	f.Close()
	// Get should fail with corruption error.
	_, err := store.Get("res-1")
	if err == nil {
		t.Fatal("Get on corrupt store should fail")
	}
}

// P1-03: Non-canonical record rejected on load.
func TestStore_Load_RejectsNonCanonicalID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "res.jsonl")
	// Write a record with uppercase GPU UUID hex (non-canonical).
	f, _ := os.Create(path)
	f.WriteString(`{"reservation_id":"r1","allocation_id":"a1","schema_version":"v1","resource_id":"gpu.nvidia.GPU-ABCDEF12-ABCD-1234-ABCD-123456789ABC","resource_type":"gpu.device","owner":"o","status":"HELD","lease_id":"l1","lease_expiry":"2026-01-01T00:00:00Z","last_renewed_at":"2026-01-01T00:00:00Z","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","revision":0}` + "\n")
	f.Close()
	store := NewStore(path)
	_, corrupt, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(corrupt) == 0 {
		t.Error("Expected corrupt records for non-canonical GPU UUID")
	}
}

// --- Policy tests ---

func TestPolicy_ValidateLeaseTTL(t *testing.T) {
	p := DefaultPolicy()
	if !p.ValidateLeaseTTL(5 * time.Minute) {
		t.Error("5min should be valid TTL")
	}
}

func TestPolicy_CanReserveGPU_Exclusive(t *testing.T) {
	p := DefaultPolicy()
	existing := []*Reservation{
		{ResourceID: testGPUUUID0, AllocationID: "alloc-A", Status: StatusHeld},
	}
	if p.CanReserveGPU(testGPUUUID0, existing) {
		t.Error("Should not reserve held GPU")
	}
	if !p.CanReserveGPU(testGPUUUID1, existing) {
		t.Error("Should allow different GPU")
	}
}

// P1-04: GPU UUID case canonicalization.
func TestCanonicalResourceID_GPUUUIDCaseCanonicalized(t *testing.T) {
	upper := "gpu.nvidia.GPU-ABCDEF12-ABCD-1234-ABCD-123456789ABC"
	canon, err := CanonicalResourceID(upper, ResourceTypeGPUDevice)
	if err != nil {
		t.Fatalf("Valid UUID rejected: %v", err)
	}
	expected := "gpu.nvidia.GPU-abcdef12-abcd-1234-abcd-123456789abc"
	if canon != expected {
		t.Errorf("Canon = %q, want %q", canon, expected)
	}
}

// P1-04: Case variants normalize to same ID.
func TestCanonicalResourceID_CaseVariantsSameCanon(t *testing.T) {
	upper := "gpu.nvidia.GPU-ABCDEF12-ABCD-1234-ABCD-123456789ABC"
	lower := "gpu.nvidia.GPU-abcdef12-abcd-1234-abcd-123456789abc"
	canonUpper, _ := CanonicalResourceID(upper, ResourceTypeGPUDevice)
	canonLower, _ := CanonicalResourceID(lower, ResourceTypeGPUDevice)
	if canonUpper != canonLower {
		t.Errorf("Case variants not canonicalized: %q vs %q", canonUpper, canonLower)
	}
}

// P2-04: NewInventorySnapshot canonicalizes resource IDs.
func TestNewInventorySnapshot_CanonicalizesIDs(t *testing.T) {
	// Provide uppercase UUID — should be canonicalized to lowercase.
	snap := NewInventorySnapshot("gen-1", time.Now().UTC(), []string{
		"gpu.nvidia.GPU-ABCDEF12-ABCD-1234-ABCD-123456789ABC",
	})
	// The canonical lowercase version should exist.
	if !snap.ResourceExists("gpu.nvidia.GPU-abcdef12-abcd-1234-abcd-123456789abc") {
		t.Error("Snapshot should contain canonicalized lowercase UUID")
	}
}

// --- Manager tests ---

func TestManager_Reserve_Success(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1",
		ResourceID:   testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice,
		LeaseTTL:     5 * time.Minute,
	}, "user-token")
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !result.Created {
		t.Error("Should be created")
	}
}

func TestManager_Reserve_Conflict(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-B", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	if err == nil {
		t.Fatal("Should conflict")
	}
}

// P1-01: Non-reconciler token cannot ConfirmRelease.
func TestManager_ConfirmRelease_NonReconcilerRejected(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	_, _ = mgr.RequestRelease(result.Reservation.ReservationID, "user-token")
	// "user-token" resolves to non-reconciler.
	_, err := mgr.ConfirmRelease(result.Reservation.ReservationID, "user-token")
	if err == nil {
		t.Fatal("Non-reconciler should be rejected")
	}
}

// P1-01: Reconciler token can ConfirmRelease.
func TestManager_RequestRelease_ConfirmRelease(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	_, _ = mgr.RequestRelease(result.Reservation.ReservationID, "user-token")
	// "reconciler-token" resolves to reconciler identity.
	r, err := mgr.ConfirmRelease(result.Reservation.ReservationID, "reconciler-token")
	if err != nil {
		t.Fatalf("ConfirmRelease: %v", err)
	}
	if r.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r.Status)
	}
}

// P2-02: Idempotent ConfirmRelease returns non-nil.
func TestManager_ConfirmRelease_IdempotentNonNil(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	_, _ = mgr.RequestRelease(result.Reservation.ReservationID, "user-token")
	_, _ = mgr.ConfirmRelease(result.Reservation.ReservationID, "reconciler-token")
	r2, err := mgr.ConfirmRelease(result.Reservation.ReservationID, "reconciler-token")
	if err != nil {
		t.Fatalf("Idempotent: %v", err)
	}
	if r2 == nil {
		t.Fatal("P2-02: Idempotent ConfirmRelease returned nil")
	}
	if r2.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r2.Status)
	}
}

func TestManager_Reserve_SameAllocationCannotBypassCapacity(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: "runtime.sglang",
		ResourceType: ResourceTypeRuntimeSlot, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: "runtime.sglang",
		ResourceType: ResourceTypeRuntimeSlot, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	if err == nil {
		t.Fatal("Same allocation should NOT bypass capacity")
	}
}

// KPK v19.2.10: canonical-equivalent resource IDs must share an idempotency fingerprint.
func TestManager_Reserve_IdempotencyCanonicalResourceID(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	upper := ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-12345678-ABCD-1234-ABCD-123456789ABC",
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute, IdempotencyKey: "idem-canonical",
	}
	lower := upper
	lower.ResourceID = "gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc"

	first, err := mgr.Reserve(upper, "user-token")
	if err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	second, err := mgr.Reserve(lower, "user-token")
	if err != nil {
		t.Fatalf("canonical-equivalent replay: %v", err)
	}
	if second.Created {
		t.Fatal("canonical-equivalent replay must not create a second reservation")
	}
	if first.Reservation.ReservationID != second.Reservation.ReservationID {
		t.Fatalf("reservation IDs differ: %s vs %s", first.Reservation.ReservationID, second.Reservation.ReservationID)
	}
}

func TestManager_Reserve_FinalizationFailureKeepsDurableReservation(t *testing.T) {
	store := newTestStore(t)
	clock := &FakeClock{T: time.Now().UTC()}
	inv := newStubInventory()
	inv.snap.AsOf = clock.Now()
	allocVal := newStubAllocValidator("alloc-1")
	allocVal.endErr = fmt.Errorf("simulated allocation finalization failure")
	cleanupVer := &stubCleanupVerifier{verified: true, clock: clock}
	idProv := newStubIdentityProvider()
	mgr, err := NewManager(store, DefaultPolicy(), clock, inv, allocVal, cleanupVer, idProv)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	result, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	if err == nil {
		t.Fatal("expected allocation finalization error")
	}
	if result == nil {
		t.Fatalf("expected persisted reservation result with pending-finalization error, got nil result: %v", err)
	}
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("expected ErrReconciliationRequired after durable commit, got: %v", err)
	}

	got, getErr := store.ListByAllocation("alloc-1")
	if getErr != nil {
		t.Fatalf("reservation must remain readable after finalization failure: %v", getErr)
	}
	if len(got) != 1 {
		t.Fatalf("durable reservation count = %d, want 1; reserve error=%v", len(got), err)
	}
	if got[0].Status != StatusHeld {
		t.Fatalf("durable reservation status = %s, want HELD", got[0].Status)
	}
}

func TestManager_ReserveBundle_ConflictDoesNotPersistPartialBundle(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	first, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-A", ResourceID: "runtime.sglang",
		ResourceType: ResourceTypeRuntimeSlot, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	if err != nil {
		t.Fatalf("seed reservation: %v", err)
	}
	_, err = mgr.ReserveBundle([]ReserveRequest{
		{AllocationID: "alloc-1", ResourceID: testGPUUUID0, ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute, IdempotencyKey: "bundle-1"},
		{AllocationID: "alloc-1", ResourceID: "runtime.sglang", ResourceType: ResourceTypeRuntimeSlot, LeaseTTL: 5 * time.Minute, IdempotencyKey: "bundle-1"},
	}, "user-token")
	if err == nil {
		t.Fatal("expected runtime capacity conflict")
	}
	all, _, loadErr := mgr.store.Load()
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if len(all) != 1 {
		t.Fatalf("partial bundle persisted: got %d reservations, want only seed reservation", len(all))
	}
	if _, ok := all[first.Reservation.ReservationID]; !ok {
		t.Fatal("seed reservation disappeared")
	}
}

func TestManager_ReserveBundle_IdempotentReplayDoesNotBeginClaimAgain(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	validator := mgr.allocVal.(*stubAllocationValidator)
	requests := []ReserveRequest{
		{AllocationID: "alloc-1", ResourceID: testGPUUUID0, ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute, IdempotencyKey: "bundle-replay"},
		{AllocationID: "alloc-1", ResourceID: "runtime.vllm", ResourceType: ResourceTypeRuntimeSlot, LeaseTTL: 5 * time.Minute, IdempotencyKey: "bundle-replay"},
	}
	first, err := mgr.ReserveBundle(requests, "user-token")
	if err != nil {
		t.Fatalf("first bundle reserve: %v", err)
	}
	if len(first.Reservations) != 2 || !first.Created {
		t.Fatalf("unexpected first result: %+v", first)
	}
	validator.mu.Lock()
	firstBeginCalls := validator.beginCalls
	validator.mu.Unlock()

	second, err := mgr.ReserveBundle([]ReserveRequest{requests[1], requests[0]}, "user-token")
	if err != nil {
		t.Fatalf("idempotent bundle replay: %v", err)
	}
	if second.Created || len(second.Reservations) != 2 {
		t.Fatalf("unexpected replay result: %+v", second)
	}
	validator.mu.Lock()
	defer validator.mu.Unlock()
	if validator.beginCalls != firstBeginCalls {
		t.Fatalf("idempotent replay re-claimed allocation: BeginReservation calls %d → %d", firstBeginCalls, validator.beginCalls)
	}
}

func TestManager_RenewBundleLease_ExtendsAllReservationsTogether(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	requests := []ReserveRequest{
		{AllocationID: "alloc-1", ResourceID: testGPUUUID0, ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute, IdempotencyKey: "lease-bundle"},
		{AllocationID: "alloc-1", ResourceID: "runtime.vllm", ResourceType: ResourceTypeRuntimeSlot, LeaseTTL: 5 * time.Minute, IdempotencyKey: "lease-bundle"},
	}
	result, err := mgr.ReserveBundle(requests, "user-token")
	if err != nil {
		t.Fatalf("ReserveBundle: %v", err)
	}
	clock.Advance(time.Minute)
	if err := mgr.RenewBundleLease("alloc-1", "user-token", 10*time.Minute); err != nil {
		t.Fatalf("RenewBundleLease: %v", err)
	}
	records, err := mgr.store.ListByAllocation("alloc-1")
	if err != nil {
		t.Fatalf("ListByAllocation: %v", err)
	}
	if len(records) != len(result.Reservations) || len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	wantExpiry := clock.Now().Add(10 * time.Minute)
	for _, record := range records {
		if !record.LeaseExpiry.Equal(wantExpiry) {
			t.Errorf("reservation %s expiry=%v, want %v", record.ReservationID, record.LeaseExpiry, wantExpiry)
		}
		if record.LeaseID != records[0].LeaseID {
			t.Errorf("bundle lease IDs differ: %q vs %q", record.LeaseID, records[0].LeaseID)
		}
	}
}

func TestManager_ReconcilePendingRepairsFinalizationFailure(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	validator := mgr.allocVal.(*stubAllocationValidator)
	validator.endErr = fmt.Errorf("simulated allocation finalization failure")
	result, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute, IdempotencyKey: "pending-1",
	}, "user-token")
	if err == nil || result == nil {
		t.Fatalf("expected a persisted result plus pending-finalization error; result=%+v err=%v", result, err)
	}
	validator.mu.Lock()
	validator.endErr = nil
	validator.mu.Unlock()
	if err := mgr.ReconcilePending("alloc-1", "reconciler-token"); err != nil {
		t.Fatalf("ReconcilePending: %v", err)
	}
}
func TestManager_Reserve_Idempotent(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	req := ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
		IdempotencyKey: "idem-1",
	}
	r1, err := mgr.Reserve(req, "user-token")
	if err != nil {
		t.Fatalf("First: %v", err)
	}
	if !r1.Created {
		t.Error("First should be created")
	}
	r2, err := mgr.Reserve(req, "user-token")
	if err != nil {
		t.Fatalf("Idempotent: %v", err)
	}
	if r2.Created {
		t.Error("Second should NOT be created")
	}
}

func TestManager_Reserve_ZeroTTLRejected(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	_, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 0,
	}, "user-token")
	if err == nil {
		t.Fatal("Zero TTL should be rejected")
	}
}

func TestManager_RenewLease_AfterExpiryRejected(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	clock.Advance(10 * time.Minute)
	_, err := mgr.RenewLease(result.Reservation.ReservationID, "user-token", 10*time.Minute)
	if err != ErrLeaseExpired {
		t.Errorf("Expected ErrLeaseExpired, got %v", err)
	}
}

func TestManager_ReconcileExpired_ReturnsNonNil(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	clock.Advance(10 * time.Minute)
	_, _ = mgr.MarkExpiredCandidates()
	r, err := mgr.ReconcileExpired(result.Reservation.ReservationID, "reconciler-token")
	if err != nil {
		t.Fatalf("ReconcileExpired: %v", err)
	}
	if r == nil {
		t.Fatal("ReconcileExpired returned nil")
	}
	if r.Status != StatusReleased {
		t.Errorf("Status = %s, want RELEASED", r.Status)
	}
}

func TestManager_RequestRelease_NoOpNoRevisionIncrement(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	result, _ := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	r1, _ := mgr.RequestRelease(result.Reservation.ReservationID, "user-token")
	rev1 := r1.Revision
	r2, _ := mgr.RequestRelease(result.Reservation.ReservationID, "user-token")
	if r2.Revision != rev1 {
		t.Errorf("Revision changed on no-op: %d → %d", rev1, r2.Revision)
	}
}

func TestReservation_String_PanicSafe(t *testing.T) {
	r := &Reservation{}
	s := r.String()
	if s == "" {
		t.Error("String() returned empty")
	}
	var nilR *Reservation
	if nilS := nilR.String(); nilS != "Reservation(nil)" {
		t.Errorf("Nil String() = %q", nilS)
	}
}

func TestManager_ConcurrentReserve_SameGPU(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	allocVal := mgr.allocVal.(*stubAllocationValidator)
	allocVal.mu.Lock()
	for i := 0; i < 10; i++ {
		allocVal.valid[fmt.Sprintf("alloc-%d", i)] = true
	}
	allocVal.mu.Unlock()
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
				LeaseTTL:     5 * time.Minute,
			}, "user-token")
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if successes != 1 {
		t.Errorf("Concurrent: %d successes, want 1", successes)
	}
}

func TestManager_MarkExpiredCandidates_AccurateCount(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	_, _ = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	clock.Advance(10 * time.Minute)
	marked, _ := mgr.MarkExpiredCandidates()
	if marked != 1 {
		t.Errorf("Marked %d, want 1", marked)
	}
	marked2, _ := mgr.MarkExpiredCandidates()
	if marked2 != 0 {
		t.Errorf("Second: %d, want 0", marked2)
	}
}
