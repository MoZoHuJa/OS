package reservation

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

func TestManager_ConcurrentReserveAcrossManagersSameGPU(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reservations.jsonl")
	store1 := NewStore(path)
	store2 := NewStore(path)
	clock := RealClock{}
	inv := newStubInventory()
	cleanup := &stubCleanupVerifier{verified: true, clock: clock}
	identity := newStubIdentityProvider()
	alloc1 := newStubAllocValidator("alloc-1")
	alloc2 := newStubAllocValidator("alloc-2")
	mgr1, err := NewManager(store1, DefaultPolicy(), clock, inv, alloc1, cleanup, identity)
	if err != nil {
		t.Fatal(err)
	}
	mgr2, err := NewManager(store2, DefaultPolicy(), clock, inv, alloc2, cleanup, identity)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i, mgr := range []*Manager{mgr1, mgr2} {
		wg.Add(1)
		go func(i int, mgr *Manager) {
			defer wg.Done()
			_, err := mgr.Reserve(ReserveRequest{
				AllocationID: fmt.Sprintf("alloc-%d", i+1), ResourceID: testGPUUUID0,
				ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
			}, "user-token")
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(i, mgr)
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("cross-manager successes=%d, want 1", successes)
	}
	records, corrupt, err := store1.Load()
	if err != nil || len(corrupt) != 0 {
		t.Fatalf("Load: records=%d corrupt=%d err=%v", len(records), len(corrupt), err)
	}
	if len(records) != 1 {
		t.Fatalf("persisted reservations=%d, want 1", len(records))
	}
}

func TestManager_ReapExpiredOnce(t *testing.T) {
	mgr, clock, _ := newTestManager(t)
	result, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	clock.Advance(10 * time.Minute)
	marked, err := mgr.ReapExpiredOnce("reconciler-token")
	if err != nil {
		t.Fatalf("ReapExpiredOnce: %v", err)
	}
	if marked != 1 {
		t.Fatalf("marked=%d, want 1", marked)
	}
	record, err := mgr.Get(result.Reservation.ReservationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if record.Status != StatusReleased {
		t.Fatalf("status=%s, want RELEASED", record.Status)
	}
}

func TestManager_RejectsSecondBundleForSameAllocation(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	if _, err := mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: testGPUUUID0,
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token"); err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	_, err := mgr.ReserveBundle([]ReserveRequest{{
		AllocationID: "alloc-1", ResourceID: "runtime.sglang",
		ResourceType: ResourceTypeRuntimeSlot, LeaseTTL: 5 * time.Minute, IdempotencyKey: "second-bundle",
	}}, "user-token")
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("second independent bundle error=%v, want ErrIdempotencyConflict", err)
	}
}

func TestManager_Reserve_RejectsGPUOutsideAllocationPlan(t *testing.T) {
	clock := &FakeClock{T: time.Now().UTC()}
	allocStore := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
	newPlannedAllocation(t, allocStore, "alloc-1")
	if err := allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		record.SelectedGPU = "gpu.nvidia.0"
		return nil
	}); err != nil {
		t.Fatalf("set allocation plan: %v", err)
	}
	adapter, err := NewAllocationReservationAdapter(allocStore)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewInventorySnapshotFromInventory("source-1", clock.Now(), []inventory.GPU{
		{ID: "gpu.nvidia.0", UUID: "GPU-12345678-abcd-1234-abcd-123456789abc", Vendor: "nvidia", Healthy: true},
		{ID: "gpu.nvidia.1", UUID: "GPU-87654321-dcba-4321-dcba-987654321cba", Vendor: "nvidia", Healthy: true},
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	inv := &stubInventoryProvider{snap: snapshot}
	store := NewStore(filepath.Join(t.TempDir(), "reservations.jsonl"))
	mgr, err := NewManager(store, DefaultPolicy(), clock, inv, adapter, &stubCleanupVerifier{verified: true, clock: clock}, newStubIdentityProvider())
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Reserve(ReserveRequest{
		AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-87654321-dcba-4321-dcba-987654321cba",
		ResourceType: ResourceTypeGPUDevice, LeaseTTL: 5 * time.Minute,
	}, "user-token")
	if err == nil {
		t.Fatal("reservation for a GPU outside the allocation plan must be rejected")
	}
	records, corrupt, loadErr := store.Load()
	if loadErr != nil || len(corrupt) != 0 {
		t.Fatalf("Load: records=%d corrupt=%d err=%v", len(records), len(corrupt), loadErr)
	}
	if len(records) != 0 {
		t.Fatalf("unexpected reservation records persisted: %d", len(records))
	}
	allocationRecord, err := allocStore.Get("alloc-1")
	if err != nil {
		t.Fatal(err)
	}
	if allocationRecord.State != allocation.StatePlanned {
		t.Fatalf("allocation state=%s, want PLANNED", allocationRecord.State)
	}
}
