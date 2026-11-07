package workload

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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

	// v19.6.8: store + stateAtStop capture the durable allocation state
	// observed DURING the driver stop call — the ordering proof that the
	// STOP_REQUESTED record is durable before the driver is invoked.
	store       *allocation.Store
	stateAtStop allocation.State
}

func (f *fakeDriver) Start(_ context.Context, request StartRequest) (StartResult, error) {
	f.startCalls++
	f.lastStart = request
	return f.startResult, f.startErr
}

func (f *fakeDriver) Stop(_ context.Context, request StopRequest) (StopResult, error) {
	f.stopCalls++
	f.lastStop = request
	if f.store != nil {
		if record, err := f.store.Get(request.AllocationID); err == nil {
			f.stateAtStop = record.State
		}
	}
	return f.stopResult, f.stopErr
}

type failingUpdateStore struct {
	AllocationStore
	calls  int
	failAt int
}

func (s *failingUpdateStore) Update(id string, modify func(*allocation.Allocation) error) error {
	s.calls++
	if s.calls == s.failAt {
		return errors.New("simulated durable store failure")
	}
	return s.AllocationStore.Update(id, modify)
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
	states := []allocation.State{
		allocation.StateValidating,
		allocation.StatePlanned,
		allocation.StateReserving,
		allocation.StateReserved,
	}
	switch state {
	case allocation.StateReserved:
	case allocation.StateStarting:
		states = append(states, allocation.StateStarting)
	case allocation.StateReady:
		states = append(states, allocation.StateStarting, allocation.StateReady)
	case allocation.StateRunning:
		states = append(states, allocation.StateStarting, allocation.StateReady, allocation.StateRunning)
	case allocation.StateStopRequested:
		// v19.6.8: the durable stop-request marker (a crash between the
		// request and the verified outcome leaves the allocation here).
		states = append(states, allocation.StateStarting, allocation.StateReady, allocation.StateRunning, allocation.StateStopRequested)
	case allocation.StateReconcileRequired:
		// v19.6.8: the ambiguous-outcome marker.
		states = append(states, allocation.StateStarting, allocation.StateReady, allocation.StateRunning, allocation.StateStopRequested, allocation.StateReconcileRequired)
	case allocation.StateFailed:
		states = append(states, allocation.StateFailed)
	case allocation.StateCancelled:
		states = append(states, allocation.StateCancelled)
	case allocation.StateCleanupRequired:
		states = append(states, allocation.StateStarting, allocation.StateCleanupRequired)
	default:
		t.Fatalf("unsupported test setup state %s", state)
	}
	for _, next := range states {
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
	if driver.stopCalls != 0 {
		t.Fatalf("manager called host driver %d times for shared workload; want 0", driver.stopCalls)
	}
}

func TestStartFailsClosedWhenDriverDoesNotConfirmReadiness(t *testing.T) {
	cases := []struct {
		name   string
		result StartResult
	}{
		{name: "not ready", result: StartResult{Ref: "container:uncertain", Ready: false, Owned: true}},
		{name: "missing reference", result: StartResult{Ready: true, Owned: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, id := newReservedAllocation(t, allocation.StateReserved)
			driver := &fakeDriver{startResult: tc.result}
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
			if record.State != allocation.StateCleanupRequired || record.CleanupVerified {
				t.Fatalf("uncertain readiness was not failed closed: %+v", record)
			}
		})
	}
}

func TestStartRejectsUnsupportedRuntimeBeforeDriverCall(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateReserved)
	if err := store.Update(id, func(record *allocation.Allocation) error {
		record.AssignedRuntime = "arbitrary-runtime"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	driver := &fakeDriver{startResult: StartResult{Ref: "container:should-not-exist", Ready: true, Owned: true}}
	manager, err := NewManager(store, driver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Start(context.Background(), id, "agent.test")
	if !errors.Is(err, ErrInvalidAllocation) {
		t.Fatalf("Start() error = %v, want invalid allocation", err)
	}
	if driver.startCalls != 0 {
		t.Fatalf("driver called %d times for unsupported runtime", driver.startCalls)
	}
}

func TestStartSurfacesFailureToPersistUncertainOutcome(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateReserved)
	failingStore := &failingUpdateStore{AllocationStore: store, failAt: 2}
	driver := &fakeDriver{startErr: errors.New("simulated host timeout")}
	manager, err := NewManager(failingStore, driver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Start(context.Background(), id, "agent.test")
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("Start() error = %v, want reconciliation required", err)
	}
	if !strings.Contains(err.Error(), "uncertain start could not be persisted") {
		t.Fatalf("Start() error = %v, want persistence failure surfaced", err)
	}
	record, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != allocation.StateStarting {
		t.Fatalf("state = %s, want STARTING because simulated persistence failed", record.State)
	}
}

// v19.6.8 (roadmap §4.3 — the lifecycle vocabulary): the stop REQUEST for a
// live workload is durably recorded as STOP_REQUESTED BEFORE the driver is
// called — a crash between the request and the outcome leaves an honest
// marker, and the driver-call ordering is observable proof.
func TestStopRecordsStopRequestedBeforeDriverCall(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateRunning)
	if err := store.Update(id, func(record *allocation.Allocation) error {
		record.WorkloadRef = "container:scarlix-test"
		record.WorkloadOwned = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	driver := &fakeDriver{stopResult: StopResult{Verified: true}, store: store}
	manager, err := NewManager(store, driver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Stop(context.Background(), id, "agent.test"); err != nil {
		t.Fatal(err)
	}
	if driver.stateAtStop != allocation.StateStopRequested {
		t.Fatalf("allocation state observed during driver stop = %s, want STOP_REQUESTED (the request must be durable before the driver call)", driver.stateAtStop)
	}
}

// v19.6.8 (roadmap §4.3): an AMBIGUOUS stop outcome — the driver answered
// but could not verify — becomes RECONCILE_REQUIRED, not CLEANUP_REQUIRED:
// a reconciler must decide, never a blind retry. Cleanup stays pending, the
// KPK-01 gate stays closed.
func TestStopAmbiguousOutcomeBecomesReconcileRequired(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateRunning)
	if err := store.Update(id, func(record *allocation.Allocation) error {
		record.WorkloadRef = "container:scarlix-test"
		record.WorkloadOwned = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The driver answers without an error but cannot verify the stop —
	// exactly the ambiguous outcome the roadmap's vocabulary exists for.
	driver := &fakeDriver{stopResult: StopResult{Verified: false}}
	manager, err := NewManager(store, driver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Stop(context.Background(), id, "agent.test")
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("Stop() error = %v, want reconciliation required", err)
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("Stop() error = %v, want the ambiguity named", err)
	}
	record, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != allocation.StateReconcileRequired {
		t.Fatalf("state = %s, want RECONCILE_REQUIRED", record.State)
	}
	if record.CleanupVerified || record.CleanupStatus != "pending" {
		t.Fatalf("ambiguous stop must stay pending and unverified: %+v", record)
	}
}

// v19.6.8: the retry path accepts STOP_REQUESTED — an allocation left at
// the durable stop-request marker (crash between request and outcome)
// resumes and completes through the verified-stop path.
func TestStopRetryFromStopRequestedCompletes(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateStopRequested)
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
		t.Fatalf("Stop() retry error = %v", err)
	}
	if result.State != allocation.StateReleasing {
		t.Fatalf("state = %s, want RELEASING (retry completes the verified stop)", result.State)
	}
	record, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !record.CleanupVerified || record.CleanupStatus != "verified" {
		t.Fatalf("verified cleanup not persisted on retry: %+v", record)
	}
}

// v19.6.8: the already-down entry path (FAILED/CANCELLED/CLEANUP_REQUIRED)
// keeps the pre-v19.6.8 direct RELEASING mapping — no stop request is
// recorded for a workload that was never running.
func TestStopFromFailedGoesDirectlyToReleasing(t *testing.T) {
	store, id := newReservedAllocation(t, allocation.StateFailed)
	if err := store.Update(id, func(record *allocation.Allocation) error {
		record.WorkloadRef = "container:scarlix-test"
		record.WorkloadOwned = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	driver := &fakeDriver{stopResult: StopResult{Verified: true}, store: store}
	manager, err := NewManager(store, driver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Stop(context.Background(), id, "agent.test"); err != nil {
		t.Fatal(err)
	}
	if driver.stateAtStop != allocation.StateReleasing {
		t.Fatalf("allocation state observed during driver stop = %s, want RELEASING (already-down path skips STOP_REQUESTED)", driver.stateAtStop)
	}
}
