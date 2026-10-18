package reservation

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
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
