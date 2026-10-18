package reservation

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
)

func newPlannedAllocation(t *testing.T, store *allocation.Store, id string) {
	t.Helper()
	record := allocation.NewAllocation(id, "v1", "coding", "interactive", "agent")
	if err := store.Append(record); err != nil {
		t.Fatalf("Append allocation: %v", err)
	}
	if err := store.Update(id, func(record *allocation.Allocation) error {
		_, err := record.Transition(allocation.StateValidating, "test validation")
		return err
	}); err != nil {
		t.Fatalf("transition VALIDATING: %v", err)
	}
	if err := store.Update(id, func(record *allocation.Allocation) error {
		_, err := record.Transition(allocation.StatePlanned, "test plan")
		return err
	}); err != nil {
		t.Fatalf("transition PLANNED: %v", err)
	}
}

func TestAllocationReservationAdapter_FinalizeReservation(t *testing.T) {
	store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
	newPlannedAllocation(t, store, "alloc-1")
	adapter, err := NewAllocationReservationAdapter(store)
	if err != nil {
		t.Fatalf("NewAllocationReservationAdapter: %v", err)
	}
	if err := adapter.BeginReservation("alloc-1"); err != nil {
		t.Fatalf("BeginReservation: %v", err)
	}
	if err := adapter.ValidateForReservation("alloc-1"); err != nil {
		t.Fatalf("ValidateForReservation: %v", err)
	}
	reservations := []*Reservation{
		{AllocationID: "alloc-1", ResourceID: "gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc", ResourceType: ResourceTypeGPUDevice, LeaseID: "lease-bundle", LeaseExpiry: time.Now().Add(5 * time.Minute)},
		{AllocationID: "alloc-1", ResourceID: "runtime.sglang", ResourceType: ResourceTypeRuntimeSlot, LeaseID: "lease-bundle", LeaseExpiry: time.Now().Add(5 * time.Minute)},
		{AllocationID: "alloc-1", ResourceID: "model.qwen3-14b", ResourceType: ResourceTypeModelSlot, LeaseID: "lease-bundle", LeaseExpiry: time.Now().Add(5 * time.Minute)},
		{AllocationID: "alloc-1", ResourceID: "port.30000", ResourceType: ResourceTypePort, LeaseID: "lease-bundle", LeaseExpiry: time.Now().Add(5 * time.Minute)},
	}
	if err := adapter.FinalizeReservation("alloc-1", reservations); err != nil {
		t.Fatalf("FinalizeReservation: %v", err)
	}
	got, err := store.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	if got.State != allocation.StateReserved {
		t.Fatalf("state=%s, want RESERVED", got.State)
	}
	if got.AssignedGPU != reservations[0].ResourceID || got.AssignedRuntime != "sglang" || got.AssignedModel != "qwen3-14b" || got.AssignedPort != 30000 {
		t.Fatalf("assignment fields not populated: %+v", got)
	}
	if got.LeaseID != "lease-bundle" || got.LeaseExpiry.IsZero() {
		t.Fatalf("lease metadata not populated: lease=%q expiry=%v", got.LeaseID, got.LeaseExpiry)
	}
	if err := adapter.FinalizeReservation("alloc-1", reservations); err != nil {
		t.Fatalf("idempotent FinalizeReservation: %v", err)
	}
}

func TestAllocationReservationAdapter_RejectsDifferentOwner(t *testing.T) {
	store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
	newPlannedAllocation(t, store, "alloc-1")
	adapter, err := NewAllocationReservationAdapter(store)
	if err != nil {
		t.Fatalf("NewAllocationReservationAdapter: %v", err)
	}
	if err := adapter.ValidateAllocationOwner("alloc-1", "attacker"); err == nil {
		t.Fatal("expected foreign allocation owner to be rejected")
	}
	if err := adapter.ValidateAllocationOwner("alloc-1", "agent"); err != nil {
		t.Fatalf("valid owner rejected: %v", err)
	}
}

func TestAllocationReservationAdapter_ListsPendingClaims(t *testing.T) {
	store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
	newPlannedAllocation(t, store, "alloc-1")
	adapter, err := NewAllocationReservationAdapter(store)
	if err != nil {
		t.Fatalf("NewAllocationReservationAdapter: %v", err)
	}
	if err := adapter.BeginReservation("alloc-1"); err != nil {
		t.Fatalf("BeginReservation: %v", err)
	}
	ids, err := adapter.ListPendingReservationAllocations()
	if err != nil {
		t.Fatalf("ListPendingReservationAllocations: %v", err)
	}
	if len(ids) != 1 || ids[0] != "alloc-1" {
		t.Fatalf("pending IDs=%v, want [alloc-1]", ids)
	}
}

func TestAllocationReservationAdapter_AbortOrphanedClaim(t *testing.T) {
	store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
	newPlannedAllocation(t, store, "alloc-1")
	adapter, err := NewAllocationReservationAdapter(store)
	if err != nil {
		t.Fatalf("NewAllocationReservationAdapter: %v", err)
	}
	if err := adapter.BeginReservation("alloc-1"); err != nil {
		t.Fatalf("BeginReservation: %v", err)
	}
	if err := adapter.EndReservation("alloc-1", false); err != nil {
		t.Fatalf("EndReservation(false): %v", err)
	}
	got, err := store.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	if got.State != allocation.StateFailed {
		t.Fatalf("state=%s, want FAILED", got.State)
	}
}
