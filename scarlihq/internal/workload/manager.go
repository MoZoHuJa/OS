package workload

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
	resourcecontract "github.com/MoZoHuJa/OS/scarlihq/internal/contract"
)

// AllocationStore is the minimal durable store surface required by the lifecycle manager.
type AllocationStore interface {
	Get(id string) (*allocation.Allocation, error)
	Update(id string, modify func(*allocation.Allocation) error) error
}

// Manager coordinates durable allocation transitions with an injected, trusted workload driver.
// It never runs host commands itself. Release of reserved resources remains Phase 6 work.
type Manager struct {
	store  AllocationStore
	driver Driver
}

func NewManager(store AllocationStore, driver Driver) (*Manager, error) {
	if store == nil {
		return nil, errors.New("workload: allocation store is required")
	}
	if driver == nil {
		return nil, errors.New("workload: trusted driver is required")
	}
	return &Manager{store: store, driver: driver}, nil
}

// Result is the safe, minimal execution outcome returned to trusted callers.
type Result struct {
	AllocationID  string
	State         allocation.State
	WorkloadRef   string
	WorkloadOwned bool
}

// Start moves RESERVED → STARTING, asks the driver to start/locate the assigned workload,
// records the observed reference, then moves STARTING → READY → RUNNING. A driver error or
// ambiguous readiness outcome is fail-closed into CLEANUP_REQUIRED.
func (m *Manager) Start(ctx context.Context, allocationID, principal string) (Result, error) {
	var request StartRequest
	err := m.store.Update(allocationID, func(record *allocation.Allocation) error {
		if err := verifyOwner(record, principal); err != nil {
			return err
		}
		if record.State != allocation.StateReserved {
			return fmt.Errorf("%w: Start requires RESERVED, got %s", ErrInvalidState, record.State)
		}
		var err error
		request, err = startRequest(record)
		if err != nil {
			return err
		}
		_, err = record.Transition(allocation.StateStarting, "workload start requested")
		return err
	})
	if err != nil {
		return Result{}, err
	}

	started, startErr := m.driver.Start(ctx, request)
	if startErr != nil || !started.Ready || strings.TrimSpace(started.Ref) == "" {
		if markErr := m.markStartUncertain(allocationID, principal, started); markErr != nil {
			// Do not hide a failed persistence attempt: the external side effect may
			// exist while durable state still says STARTING. This is a higher-severity
			// reconciliation case and must be surfaced to the caller.
			return Result{}, fmt.Errorf("%w: uncertain start could not be persisted", ErrReconciliationRequired)
		}
		if startErr != nil {
			return Result{}, fmt.Errorf("%w: workload driver start failed", ErrReconciliationRequired)
		}
		return Result{}, fmt.Errorf("%w: driver did not return a ready workload with a reference", ErrReconciliationRequired)
	}

	if err := m.store.Update(allocationID, func(record *allocation.Allocation) error {
		if err := verifyOwner(record, principal); err != nil {
			return err
		}
		if record.State != allocation.StateStarting {
			return fmt.Errorf("%w: expected STARTING while recording workload, got %s", ErrInvalidState, record.State)
		}
		record.WorkloadRef = started.Ref
		record.WorkloadOwned = started.Owned
		record.CleanupStatus = ""
		record.CleanupVerified = false
		_, err := record.Transition(allocation.StateReady, "driver confirmed workload ready")
		return err
	}); err != nil {
		// The external workload may be live but the reference/state may not be durable.
		return Result{}, fmt.Errorf("%w: workload started but READY state could not be persisted", ErrReconciliationRequired)
	}

	if err := m.store.Update(allocationID, func(record *allocation.Allocation) error {
		if err := verifyOwner(record, principal); err != nil {
			return err
		}
		if record.State != allocation.StateReady {
			return fmt.Errorf("%w: expected READY before RUNNING, got %s", ErrInvalidState, record.State)
		}
		_, err := record.Transition(allocation.StateRunning, "workload running confirmed")
		return err
	}); err != nil {
		return Result{}, fmt.Errorf("%w: READY workload could not transition to RUNNING", ErrReconciliationRequired)
	}

	record, err := m.store.Get(allocationID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: RUNNING state could not be re-read", ErrReconciliationRequired)
	}
	if err := verifyOwner(record, principal); err != nil {
		return Result{}, err
	}
	return resultFor(record), nil
}

// Stop asks the driver to stop only an allocation-owned workload. Shared/pre-existing
// runtimes must not be terminated by this operation. A verified stop remains RELEASING;
// Phase 6 owns reservation release and the final RELEASED transition.
func (m *Manager) Stop(ctx context.Context, allocationID, principal string) (Result, error) {
	var request StopRequest
	err := m.store.Update(allocationID, func(record *allocation.Allocation) error {
		if err := verifyOwner(record, principal); err != nil {
			return err
		}
		switch record.State {
		case allocation.StateReady, allocation.StateRunning, allocation.StateStarting,
			allocation.StateFailed, allocation.StateCancelled, allocation.StateCleanupRequired:
			if _, err := record.Transition(allocation.StateReleasing, "workload stop requested"); err != nil {
				return err
			}
		case allocation.StateReleasing:
			// Idempotent retry after a crash or uncertain response.
		default:
			return fmt.Errorf("%w: Stop does not accept state %s", ErrInvalidState, record.State)
		}
		request = StopRequest{
			AllocationID: record.AllocationID,
			RuntimeID:    firstNonEmpty(record.AssignedRuntime, record.SelectedRuntime),
			ModelID:      firstNonEmpty(record.AssignedModel, record.SelectedModel),
			Ref:          record.WorkloadRef,
			Owned:        record.WorkloadOwned,
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	// Defense in depth: a shared/pre-existing runtime is not owned by this
	// allocation, so the manager must not invoke the host driver to stop it.
	// There is no allocation-owned process to clean up; Phase 6 may release
	// this allocation's reservation without terminating the shared runtime.
	stopped := StopResult{Verified: !request.Owned}
	var stopErr error
	if request.Owned {
		stopped, stopErr = m.driver.Stop(ctx, request)
	}
	if stopErr != nil || !stopped.Verified {
		if err := m.store.Update(allocationID, func(record *allocation.Allocation) error {
			if err := verifyOwner(record, principal); err != nil {
				return err
			}
			if record.State != allocation.StateReleasing {
				return fmt.Errorf("%w: expected RELEASING while recording cleanup failure, got %s", ErrInvalidState, record.State)
			}
			record.CleanupStatus = "failed"
			record.CleanupVerified = false
			record.LastError = "workload stop not verified; reconciliation required"
			_, transitionErr := record.Transition(allocation.StateCleanupRequired, "workload cleanup could not be verified")
			return transitionErr
		}); err != nil {
			return Result{}, fmt.Errorf("%w: stop failed and cleanup state could not be persisted", ErrReconciliationRequired)
		}
		return Result{}, fmt.Errorf("%w: workload stop was not verified", ErrReconciliationRequired)
	}

	record, err := m.store.Get(allocationID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: stopped workload state could not be re-read", ErrReconciliationRequired)
	}
	if err := verifyOwner(record, principal); err != nil {
		return Result{}, err
	}
	if err := m.store.Update(allocationID, func(current *allocation.Allocation) error {
		if err := verifyOwner(current, principal); err != nil {
			return err
		}
		if current.State != allocation.StateReleasing {
			return fmt.Errorf("%w: expected RELEASING after verified stop, got %s", ErrInvalidState, current.State)
		}
		current.CleanupStatus = "verified"
		current.CleanupVerified = true
		current.LastError = ""
		return nil
	}); err != nil {
		return Result{}, fmt.Errorf("%w: verified stop could not be persisted", ErrReconciliationRequired)
	}
	record, err = m.store.Get(allocationID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: final stop state could not be re-read", ErrReconciliationRequired)
	}
	return resultFor(record), nil
}

func (m *Manager) markStartUncertain(id, principal string, started StartResult) error {
	return m.store.Update(id, func(record *allocation.Allocation) error {
		if err := verifyOwner(record, principal); err != nil {
			return err
		}
		if record.State != allocation.StateStarting {
			return fmt.Errorf("%w: expected STARTING while recording uncertain start, got %s", ErrInvalidState, record.State)
		}
		if strings.TrimSpace(started.Ref) != "" {
			record.WorkloadRef = started.Ref
		}
		record.WorkloadOwned = started.Owned
		record.CleanupStatus = "pending"
		record.CleanupVerified = false
		record.LastError = "workload start outcome uncertain; reconciliation required"
		_, err := record.Transition(allocation.StateCleanupRequired, "workload start outcome uncertain")
		return err
	})
}

func startRequest(record *allocation.Allocation) (StartRequest, error) {
	runtimeID := strings.TrimSpace(record.AssignedRuntime)
	modelID := strings.TrimSpace(record.AssignedModel)
	if runtimeID == "" || modelID == "" || record.AssignedPort < 1 || record.AssignedPort > 65535 {
		return StartRequest{}, ErrInvalidAllocation
	}
	if !resourcecontract.IsSupportedRuntime(runtimeID) {
		return StartRequest{}, fmt.Errorf("%w: unsupported assigned runtime %q", ErrInvalidAllocation, runtimeID)
	}
	if len(runtimeID) > 64 || len(modelID) > 128 || strings.ContainsAny(modelID, "\x00\r\n") {
		return StartRequest{}, ErrInvalidAllocation
	}
	return StartRequest{
		AllocationID: record.AllocationID,
		RuntimeID:    runtimeID,
		ModelID:      modelID,
		GPUID:        record.AssignedGPU,
		Port:         record.AssignedPort,
	}, nil
}

func verifyOwner(record *allocation.Allocation, principal string) error {
	if record == nil {
		return errors.New("workload: allocation record is nil")
	}
	if strings.TrimSpace(principal) == "" || record.Owner == "" || record.Owner != principal {
		return ErrOwnerMismatch
	}
	return nil
}

func resultFor(record *allocation.Allocation) Result {
	return Result{
		AllocationID:  record.AllocationID,
		State:         record.State,
		WorkloadRef:   record.WorkloadRef,
		WorkloadOwned: record.WorkloadOwned,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
