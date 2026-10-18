package hostbridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

type recoverableFake struct {
	mu         sync.Mutex
	resources  map[string]workload.StartResult
	startCalls int
	stopCalls  int
	startErr   error
}

func (f *recoverableFake) Start(_ context.Context, r workload.StartRequest) (workload.StartResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	if f.startErr != nil {
		return workload.StartResult{}, f.startErr
	}
	result := workload.StartResult{Ref: "container-" + r.AllocationID, Ready: true, Owned: true}
	if f.resources == nil {
		f.resources = map[string]workload.StartResult{}
	}
	f.resources[r.AllocationID] = result
	return result, nil
}

func (f *recoverableFake) Stop(_ context.Context, r workload.StopRequest) (workload.StopResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	delete(f.resources, r.AllocationID)
	return workload.StopResult{Verified: true}, nil
}

func (f *recoverableFake) Find(_ context.Context, id string) (workload.StartResult, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.resources[id]
	return result, ok, nil
}

func newTestIdempotent(t *testing.T, inner RecoverableExecutor) (*IdempotentExecutor, *Ledger) {
	t.Helper()
	dir := t.TempDir()
	ledger, err := NewLedger(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewIdempotentExecutor(inner, ledger)
	if err != nil {
		t.Fatal(err)
	}
	return executor, ledger
}

func testStartRequest() workload.StartRequest {
	return workload.StartRequest{AllocationID: "alloc-1", RuntimeID: "sglang", ModelID: "qwen3-14b-awq", GPUID: "GPU-abc", Port: 30000}
}

func TestIdempotentExecutorStartIsIdempotent(t *testing.T) {
	inner := &recoverableFake{}
	executor, ledger := newTestIdempotent(t, inner)
	first, err := executor.Start(context.Background(), testStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	second, err := executor.Start(context.Background(), testStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if first.Ref != second.Ref || inner.startCalls != 1 {
		t.Fatalf("duplicate start created another workload: first=%+v second=%+v calls=%d", first, second, inner.startCalls)
	}
	entry, ok, err := ledger.Get("alloc-1")
	if err != nil || !ok || entry.State != "RUNNING" || !entry.Owned {
		t.Fatalf("unexpected durable entry: %+v ok=%v err=%v", entry, ok, err)
	}
}

func TestIdempotentExecutorRejectsConflictingAssignment(t *testing.T) {
	inner := &recoverableFake{}
	executor, _ := newTestIdempotent(t, inner)
	if _, err := executor.Start(context.Background(), testStartRequest()); err != nil {
		t.Fatal(err)
	}
	conflict := testStartRequest()
	conflict.ModelID = "different-model"
	if _, err := executor.Start(context.Background(), conflict); err == nil {
		t.Fatal("expected conflicting allocation assignment to fail")
	}
	if inner.startCalls != 1 {
		t.Fatalf("conflicting request reached executor: calls=%d", inner.startCalls)
	}
}

func TestIdempotentExecutorStopRequiresLedgerOwnershipAndIsRetrySafe(t *testing.T) {
	inner := &recoverableFake{}
	executor, _ := newTestIdempotent(t, inner)
	started, err := executor.Start(context.Background(), testStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	req := workload.StopRequest{AllocationID: "alloc-1", RuntimeID: "sglang", ModelID: "qwen3-14b-awq", Ref: started.Ref, Owned: true}
	if _, err := executor.Stop(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Stop(context.Background(), req); err != nil {
		t.Fatalf("repeated stop should verify already-absent workload: %v", err)
	}
	if inner.stopCalls != 1 {
		t.Fatalf("stop calls=%d, want 1", inner.stopCalls)
	}
}

func TestIdempotentExecutorFailsClosedAfterUncertainStart(t *testing.T) {
	inner := &recoverableFake{startErr: errors.New("injected timeout")}
	executor, ledger := newTestIdempotent(t, inner)
	if _, err := executor.Start(context.Background(), testStartRequest()); err == nil {
		t.Fatal("expected uncertain start error")
	}
	entry, ok, err := ledger.Get("alloc-1")
	if err != nil || !ok || entry.State != "RECONCILE_REQUIRED" {
		t.Fatalf("expected durable reconciliation state, entry=%+v ok=%v err=%v", entry, ok, err)
	}
	inner.startErr = nil
	if _, err := executor.Start(context.Background(), testStartRequest()); err == nil {
		t.Fatal("must not blindly retry an unresolved allocation")
	}
	if inner.startCalls != 1 {
		t.Fatalf("unresolved allocation was retried: calls=%d", inner.startCalls)
	}
}

func TestLedgerRejectsUnsafeParentAndCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLedger(filepath.Join(dir, "ledger.json")); err == nil {
		t.Fatal("expected group/other-accessible directory to be rejected")
	}
	dir2 := t.TempDir()
	path := filepath.Join(dir2, "ledger.json")
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	ledger, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Get("alloc-1"); err == nil {
		t.Fatal("expected corrupt ledger to fail closed")
	}
}
