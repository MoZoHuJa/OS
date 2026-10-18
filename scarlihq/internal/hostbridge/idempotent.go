package hostbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

// RecoverableExecutor is required for durable idempotency. Find must query the
// actual runtime by trusted allocation metadata; a local ledger alone cannot
// prove that a timed-out create did not succeed.
type RecoverableExecutor interface {
	Executor
	Find(context.Context, string) (workload.StartResult, bool, error)
}

// IdempotentExecutor serializes lifecycle calls with a durable ledger. It
// deliberately refuses to wrap an Executor that cannot reconcile external state.
type IdempotentExecutor struct {
	inner RecoverableExecutor
	ledger *Ledger
	mu sync.Mutex
}

func NewIdempotentExecutor(inner RecoverableExecutor, ledger *Ledger) (*IdempotentExecutor, error) {
	if inner == nil || ledger == nil {
		return nil, errors.New("hostbridge: recoverable executor and durable ledger are required")
	}
	return &IdempotentExecutor{inner: inner, ledger: ledger}, nil
}

func (e *IdempotentExecutor) Start(ctx context.Context, req workload.StartRequest) (workload.StartResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fingerprint := requestFingerprint(req)
	entry, exists, err := e.ledger.Get(req.AllocationID)
	if err != nil {
		return workload.StartResult{}, err
	}
	if exists {
		if entry.Fingerprint != fingerprint {
			return workload.StartResult{}, errors.New("hostbridge: allocation id reused with conflicting assignment")
		}
		if entry.State == "STOPPED" {
			return workload.StartResult{}, errors.New("hostbridge: allocation was already stopped")
		}
		observed, found, findErr := e.inner.Find(ctx, req.AllocationID)
		if findErr != nil {
			_ = e.ledger.SetState(req.AllocationID, "RECONCILE_REQUIRED", entry.Ref, entry.Owned)
			return workload.StartResult{}, errors.New("hostbridge: runtime reconciliation failed")
		}
		if found {
			if !observed.Ready || observed.Ref == "" {
				_ = e.ledger.SetState(req.AllocationID, "RECONCILE_REQUIRED", observed.Ref, observed.Owned)
				return workload.StartResult{}, errors.New("hostbridge: existing workload is not ready")
			}
			if err := e.ledger.SetState(req.AllocationID, "RUNNING", observed.Ref, observed.Owned); err != nil {
				return workload.StartResult{}, errors.New("hostbridge: could not persist reconciled workload")
			}
			return observed, nil
		}
		// Only an interrupted STARTING intent may retry after the runtime positively
		// confirms absence. RUNNING/RECONCILE_REQUIRED must fail closed.
		if entry.State != "STARTING" {
			return workload.StartResult{}, errors.New("hostbridge: existing allocation is not safely retryable; reconciliation required")
		}
		if err := e.ledger.SetState(req.AllocationID, "STARTING", "", false); err != nil {
			return workload.StartResult{}, errors.New("hostbridge: could not persist retry intent")
		}
	} else {
		if err := e.ledger.Create(req); err != nil {
			return workload.StartResult{}, err
		}
	}

	result, startErr := e.inner.Start(ctx, req)
	if startErr != nil || !result.Ready || result.Ref == "" {
		observed, found, findErr := e.inner.Find(ctx, req.AllocationID)
		if findErr == nil && found && observed.Ready && observed.Ref != "" {
			if persistErr := e.ledger.SetState(req.AllocationID, "RUNNING", observed.Ref, observed.Owned); persistErr != nil {
				return workload.StartResult{}, errors.New("hostbridge: runtime started but durable state update failed")
			}
			return observed, nil
		}
		_ = e.ledger.SetState(req.AllocationID, "RECONCILE_REQUIRED", result.Ref, result.Owned)
		if startErr != nil {
			return workload.StartResult{}, errors.New("hostbridge: start outcome uncertain; reconciliation required")
		}
		return workload.StartResult{}, errors.New("hostbridge: start was not positively confirmed")
	}
	if err := e.ledger.SetState(req.AllocationID, "RUNNING", result.Ref, result.Owned); err != nil {
		return workload.StartResult{}, errors.New("hostbridge: runtime started but durable state update failed")
	}
	return result, nil
}

func (e *IdempotentExecutor) Stop(ctx context.Context, req workload.StopRequest) (workload.StopResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry, exists, err := e.ledger.Get(req.AllocationID)
	if err != nil || !exists {
		return workload.StopResult{}, errors.New("hostbridge: allocation has no durable ownership record")
	}
	if !entry.Owned || !req.Owned || entry.Ref == "" || entry.Ref != req.Ref ||
		entry.RuntimeID != req.RuntimeID || entry.ModelID != req.ModelID {
		return workload.StopResult{}, errors.New("hostbridge: stop request does not match owned workload record")
	}
	observed, found, err := e.inner.Find(ctx, req.AllocationID)
	if err != nil {
		_ = e.ledger.SetState(req.AllocationID, "RECONCILE_REQUIRED", entry.Ref, entry.Owned)
		return workload.StopResult{}, errors.New("hostbridge: cannot reconcile workload before stop")
	}
	if found && observed.Ref != entry.Ref {
		return workload.StopResult{}, errors.New("hostbridge: runtime reference does not match ownership record")
	}
	if !found {
		if err := e.ledger.SetState(req.AllocationID, "STOPPED", entry.Ref, true); err != nil {
			return workload.StopResult{}, errors.New("hostbridge: absence verified but ledger update failed")
		}
		return workload.StopResult{Verified: true}, nil
	}
	result, stopErr := e.inner.Stop(ctx, req)
	if stopErr != nil || !result.Verified {
		_ = e.ledger.SetState(req.AllocationID, "RECONCILE_REQUIRED", entry.Ref, entry.Owned)
		return workload.StopResult{}, errors.New("hostbridge: cleanup outcome uncertain; reconciliation required")
	}
	_, stillFound, findErr := e.inner.Find(ctx, req.AllocationID)
	if findErr != nil || stillFound {
		_ = e.ledger.SetState(req.AllocationID, "RECONCILE_REQUIRED", entry.Ref, entry.Owned)
		return workload.StopResult{}, errors.New("hostbridge: stop was not verified by runtime reconciliation")
	}
	if err := e.ledger.SetState(req.AllocationID, "STOPPED", entry.Ref, true); err != nil {
		return workload.StopResult{}, errors.New("hostbridge: cleanup verified but ledger update failed")
	}
	return workload.StopResult{Verified: true}, nil
}

func requestFingerprint(r workload.StartRequest) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", r.AllocationID, r.RuntimeID, r.ModelID, r.GPUID, r.Port)
}

// ReconcileOne is an operator/daemon recovery hook. It only observes state;
// it never starts or stops a workload automatically.
func (e *IdempotentExecutor) ReconcileOne(ctx context.Context, allocationID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry, ok, err := e.ledger.Get(allocationID)
	if err != nil || !ok {
		return errors.New("hostbridge: allocation ledger entry not found")
	}
	if entry.State == "STOPPED" {
		return nil
	}
	observed, found, err := e.inner.Find(ctx, allocationID)
	if err != nil {
		_ = e.ledger.SetState(allocationID, "RECONCILE_REQUIRED", entry.Ref, entry.Owned)
		return errors.New("hostbridge: runtime lookup failed")
	}
	if !found {
		if err := e.ledger.SetState(allocationID, "STOPPED", entry.Ref, entry.Owned); err != nil {
			return err
		}
		return nil
	}
	if !observed.Ready || observed.Ref == "" {
		_ = e.ledger.SetState(allocationID, "RECONCILE_REQUIRED", observed.Ref, observed.Owned)
		return errors.New("hostbridge: runtime resource exists but readiness is not confirmed")
	}
	return e.ledger.SetState(allocationID, "RUNNING", observed.Ref, observed.Owned)
}

// WithTimeout gives daemon call sites a standard bounded reconciliation window.
func WithTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 30*time.Second)
}
