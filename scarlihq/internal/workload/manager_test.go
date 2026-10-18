package workload

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
	resourcecontract "github.com/MoZoHuJa/OS/scarlihq/internal/contract"
)

type fakeDriver struct {
	startResult StartResult
	startErr    error
	stopResult  StopResult
	stopErr     error
	startCalls  int
	stopCalls   int
	lastStart   StartRequest
	lastStop    StopRequest
}

func (f *fakeDriver) Start(_ context.Context, request StartRequest) (StartResult, error) {
	f.startCalls++
	f.lastStart = request
	return f.startResult, f.startErr
}

func (f *fakeDriver) Stop(_ context.Context, request StopRequest) (StopResult, error) {
	f.stopCalls++
	f.lastStop = request
	return f.stopResult, f.stopErr
}

func newReservedAllocation(t *testing.T, state allocation.State) (*allocation.Store, string) {
	t.Helper()
	store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
	record := allocation.NewAllocation(
		"allocation-test-1",
		string(resourcecontract.VersionV1),
		resourcecontract.TaskTypeCoding,
		resourcecontract.PriorityInteractive,
		"agent.test",
	)
	for _, next := range []allocation.State{
		allocation.StateValidating,
		allocation.StatePlanned,
		allocation.StateReserving,
		allocation.StateReserved,
	} {
		if _, err := record.Transition(next, "test setup"); err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}
	record.AssignedRuntime = "sglang"
	record.AssignedModel = "qwen3-14b-awq"
	record.AssignedGPU = "GPU-test-uuid"
	record.AssignedPort = 30000
	if err := store.Append(record); err != nil {
		t.Fatalf("append allocation: %v", err)
	}
	if state != allocation.StateReserved {
		if err := store.Update(record.AllocationID, func(current *allocation.Allocation) error {
			_, err := current.Transition(state, "test setup")
			return err
		}); err != nil {
			t.Fatalf("transition stored allocation to %s: %v", state, err)
		}
	}
	return store, record.AllocationID
}

func TestStartTransitionsReservedToRunning(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateReserved)
	driver := &fakeDriver{startResult: StartResult{Ref: "container:scarlix-test", Ready: true, Owned: true}}
	manager, err := NewManager(store, driver)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Start(context.Background(), id, "agent.test")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.State != allocation.StateRunning || result.WorkloadRef != "container:scarlix-test" || !result.WorkloadOwned {
		t.Fatalf("unexpected result: %+v", result)
	}
	if driver.startCalls != 1 {
		t.Fatalf("driver start calls = %d, want 1", driver.startCalls)
	}
	if driver.lastStart.RuntimeID != "sglang" || driver.lastStart.ModelID != "qwen3-14b-awq" || driver.lastStart.Port != 30000 {
		t.Fatalf("unexpected start request: %+v", driver.lastStart)
	}
}

func TestStartFailsClosedWhenDriverOutcomeIsUncertain(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateReserved)
	driver := &fakeDriver{startErr: errors.New("internal host error")}
	manager, err := NewManager(store, driver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Start(context.Background(), id, "agent.test")
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("Start() error = %v, want reconciliation required", err)
	}
	record, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != allocation.StateCleanupRequired || record.CleanupVerified || record.CleanupStatus != "pending" {
		t.Fatalf("uncertain start not fail-closed: %+v", record)
	}
	if record.LastError == "internal host error" {
		t.Fatal("raw driver error should not be persisted")
	}
}

func TestStartRejectsWrongOwnerBeforeDriverCall(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateReserved)
	driver := &fakeDriver{startResult: StartResult{Ref: "x", Ready: true}}
	manager, err := NewManager(store, driver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Start(context.Background(), id, "agent.attacker")
	if !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("Start() error = %v, want owner mismatch", err)
	}
	if driver.startCalls != 0 {
		t.Fatalf("driver was called %d times before owner check", driver.startCalls)
	}
}

func TestStartRejectsIncompleteAssignmentWithoutSideEffects(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateReserved)
	if err := store.Update(id, func(record *allocation.Allocation) error {
		record.AssignedPort = 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	driver := &fakeDriver{startResult: StartResult{Ref: "x", Ready: true}}
	manager, err := NewManager(store, driver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Start(context.Background(), id, "agent.test")
	if !errors.Is(err, ErrInvalidAllocation) {
		t.Fatalf("Start() error = %v, want invalid allocation", err)
	}
	record, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != allocation.StateReserved || driver.startCalls != 0 {
		t.Fatalf("invalid assignment caused side effects: state=%s driverCalls=%d", record.State, driver.startCalls)
	}
}

func TestStopDoesNotReleaseAllocationBeforePhase6(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateRunning)
	if err := store.Update(id, func(record *allocation.Allocation) error {
		record.WorkloadRef = "container:scarlix-test"
		record.WorkloadOwned = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	driver := &fakeDriver{stopResult: StopResult{Verified: true}}
	manager, err := NewManager(store, driver)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Stop(context.Background(), id, "agent.test")
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if result.State != allocation.StateReleasing {
		t.Fatalf("state = %s, want RELEASING (Phase 6 owns final release)", result.State)
	}
	record, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !record.CleanupVerified || record.CleanupStatus != "verified" {
		t.Fatalf("verified cleanup not persisted: %+v", record)
	}
	if !driver.lastStop.Owned || driver.lastStop.Ref != "container:scarlix-test" {
		t.Fatalf("unexpected stop request: %+v", driver.lastStop)
	}
}

func TestStopFailureRequiresReconciliation(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateRunning)
	if err := store.Update(id, func(record *allocation.Allocation) error {
		record.WorkloadRef = "container:scarlix-test"
		record.WorkloadOwned = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	driver := &fakeDriver{stopErr: errors.New("docker unavailable")}
	manager, err := NewManager(store, driver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Stop(context.Background(), id, "agent.test")
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("Stop() error = %v, want reconciliation required", err)
	}
	record, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != allocation.StateCleanupRequired || record.CleanupVerified || record.CleanupStatus != "failed" {
		t.Fatalf("stop failure not fail-closed: %+v", record)
	}
}

func TestStopNeverStopsSharedWorkload(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateRunning)
	if err := store.Update(id, func(record *allocation.Allocation) error {
		record.WorkloadRef = "container:shared-runtime"
		record.WorkloadOwned = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	driver := &fakeDriver{stopResult: StopResult{Verified: true}}
	manager, err := NewManager(store, driver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Stop(context.Background(), id, "agent.test"); err != nil {
		t.Fatal(err)
	}
	if driver.lastStop.Owned {
		t.Fatal("manager marked shared workload as allocation-owned")
	}
}
