package reservation

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
)

// AllocationStore is the minimal allocation-store surface required by the
// reservation adapter. It is intentionally narrower than allocation.Store.
type AllocationStore interface {
	Load() (map[string]*allocation.Allocation, []error, error)
	Update(id string, modify func(*allocation.Allocation) error) error
}

// AllocationReservationAdapter binds Phase 3 reservations to the Phase 2
// allocation state machine. Allocation transitions remain validated by the
// frozen allocation.Transition table and are persisted under the allocation
// store's own cross-process lock.

type ReservationAssignmentFinalizer interface {
	FinalizeReservation(allocationID string, reservations []*Reservation) error
}

type AllocationReservationAdapter struct {
	store AllocationStore
}

func NewAllocationReservationAdapter(store AllocationStore) (*AllocationReservationAdapter, error) {
	if store == nil {
		return nil, fmt.Errorf("reservation: allocation store is required")
	}
	return &AllocationReservationAdapter{store: store}, nil
}

// ListPendingReservationAllocations finds claims interrupted between
// PLANNED → RESERVING and durable reservation commit/finalization.
func (a *AllocationReservationAdapter) ListPendingReservationAllocations() ([]string, error) {
	records, corrupt, err := a.store.Load()
	if err != nil {
		return nil, err
	}
	if len(corrupt) > 0 {
		return nil, fmt.Errorf("%w: allocation store has %d corrupt records", ErrReconciliationRequired, len(corrupt))
	}
	ids := make([]string, 0)
	for _, record := range records {
		if record != nil && record.State == allocation.StateReserving && record.AllocationID != "" {
			ids = append(ids, record.AllocationID)
		}
	}
	return ids, nil
}

func (a *AllocationReservationAdapter) getSafe(id string) (*allocation.Allocation, error) {
	records, corrupt, err := a.store.Load()
	if err != nil {
		return nil, err
	}
	if len(corrupt) > 0 {
		return nil, fmt.Errorf("%w: allocation store has %d corrupt records", ErrReconciliationRequired, len(corrupt))
	}
	record, ok := records[id]
	if !ok {
		return nil, fmt.Errorf("allocation %s not found: %w", id, allocation.ErrNotFound)
	}
	return record, nil
}

// ValidateAllocationOwner prevents a caller from reserving resources for an
// allocation owned by another authenticated principal.
func (a *AllocationReservationAdapter) ValidateAllocationOwner(id, principal string) error {
	record, err := a.getSafe(id)
	if err != nil {
		return err
	}
	if principal == "" || record.Owner != principal {
		return fmt.Errorf("reservation: allocation %s is not owned by the authenticated principal", id)
	}
	return nil
}

// ValidateReservationPlan enforces that the resources being reserved match
// the immutable allocation plan. GPU UUIDs are resolved back to inventory IDs
// so a caller cannot silently reserve a different GPU than the planner selected.
// P1-01 (v19.2.11): Now also verifies that ALL required plan resources are
// present in the bundle — rejects missing GPU, runtime, or model.
func (a *AllocationReservationAdapter) ValidateReservationPlan(id string, requests []normalizedRequest, snapshot InventorySnapshot) error {
	record, err := a.getSafe(id)
	if err != nil {
		return err
	}
	if record.State != allocation.StatePlanned && record.State != allocation.StateReserving && record.State != allocation.StateReserved {
		return fmt.Errorf("allocation %s cannot validate a reservation plan from %s", id, record.State)
	}

	// P1-01 (v19.2.11): Track which plan resources have been matched.
	hasGPU := false
	hasRuntime := false
	hasModel := false
	portCount := 0

	for _, item := range requests {
		switch item.request.ResourceType {
		case ResourceTypeGPUDevice:
			displayID := snapshot.DisplayIDForResource(item.canonical)
			if displayID == "" {
				return fmt.Errorf("%w: no stable UUID-to-inventory mapping for GPU %s", ErrReconciliationRequired, item.canonical)
			}
			if record.SelectedGPU == "" || record.SelectedGPU != displayID {
				return fmt.Errorf("reservation: GPU %s does not match allocation plan %q", item.canonical, record.SelectedGPU)
			}
			hasGPU = true
		case ResourceTypeRuntimeSlot:
			selected := strings.TrimPrefix(item.canonical, "runtime.")
			if record.SelectedRuntime == "" || record.SelectedRuntime != selected {
				return fmt.Errorf("reservation: runtime %s does not match allocation plan %q", selected, record.SelectedRuntime)
			}
			hasRuntime = true
		case ResourceTypeModelSlot:
			selected := strings.TrimPrefix(item.canonical, "model.")
			if record.SelectedModel == "" || record.SelectedModel != selected {
				return fmt.Errorf("reservation: model %s does not match allocation plan %q", selected, record.SelectedModel)
			}
			hasModel = true
		case ResourceTypePort:
			// P2-01 (v19.2.11): Reject multiple ports — allocation schema has single AssignedPort.
			portCount++
			if portCount > 1 {
				return fmt.Errorf("reservation: allocation %s plan does not support multiple port reservations", id)
			}
		}
	}

	// P1-01 (v19.2.11): Verify all required plan resources are present.
	if record.SelectedGPU != "" && !hasGPU {
		return fmt.Errorf("reservation: allocation %s plan requires GPU %s but bundle is missing it", id, record.SelectedGPU)
	}
	if record.SelectedRuntime != "" && !hasRuntime {
		return fmt.Errorf("reservation: allocation %s plan requires runtime %s but bundle is missing it", id, record.SelectedRuntime)
	}
	if record.SelectedModel != "" && !hasModel {
		return fmt.Errorf("reservation: allocation %s plan requires model %s but bundle is missing it", id, record.SelectedModel)
	}

	return nil
}

func (a *AllocationReservationAdapter) ValidateForReservation(id string) error {
	record, err := a.getSafe(id)
	if err != nil {
		return err
	}
	if record.State != allocation.StateReserving {
		return fmt.Errorf("allocation %s must be RESERVING, got %s", id, record.State)
	}
	if record.Owner == "" {
		return fmt.Errorf("allocation %s has no owner", id)
	}
	return nil
}

func (a *AllocationReservationAdapter) BeginReservation(id string) error {
	return a.store.Update(id, func(record *allocation.Allocation) error {
		switch record.State {
		case allocation.StateReserving:
			return nil // recover/retry an interrupted claim
		case allocation.StatePlanned:
			_, err := record.Transition(allocation.StateReserving, "phase 3 resource reservation started")
			return err
		default:
			return fmt.Errorf("allocation %s cannot begin reservation from %s", id, record.State)
		}
	})
}

func (a *AllocationReservationAdapter) EndReservation(id string, success bool) error {
	if success {
		return a.store.Update(id, func(record *allocation.Allocation) error {
			if record.State == allocation.StateReserved {
				return nil
			}
			if record.State != allocation.StateReserving {
				return fmt.Errorf("allocation %s cannot finalize reservation from %s", id, record.State)
			}
			_, err := record.Transition(allocation.StateReserved, "phase 3 resource reservation committed")
			return err
		})
	}
	return a.store.Update(id, func(record *allocation.Allocation) error {
		if record.State == allocation.StateFailed || record.State == allocation.StateCancelled {
			return nil
		}
		if record.State != allocation.StateReserving {
			return fmt.Errorf("allocation %s cannot abort reservation from %s", id, record.State)
		}
		_, err := record.Transition(allocation.StateFailed, "phase 3 resource reservation aborted")
		return err
	})
}

// FinalizeReservation commits the assignment and RESERVED state in one
// allocation-store transaction. This is preferred to EndReservation(true).
func (a *AllocationReservationAdapter) FinalizeReservation(id string, reservations []*Reservation) error {
	if len(reservations) == 0 {
		return fmt.Errorf("reservation: cannot finalize an empty reservation bundle")
	}
	return a.store.Update(id, func(record *allocation.Allocation) error {
		if record.State != allocation.StateReserving && record.State != allocation.StateReserved {
			return fmt.Errorf("allocation %s cannot finalize reservation from %s", id, record.State)
		}
		record.AssignedGPU = ""
		record.AssignedRuntime = ""
		record.AssignedModel = ""
		record.AssignedPort = 0
		for _, reservation := range reservations {
			if reservation == nil || reservation.AllocationID != id {
				return fmt.Errorf("reservation: invalid reservation assignment for allocation %s", id)
			}
			switch reservation.ResourceType {
			case ResourceTypeGPUDevice:
				record.AssignedGPU = reservation.ResourceID
			case ResourceTypeRuntimeSlot:
				record.AssignedRuntime = strings.TrimPrefix(reservation.ResourceID, "runtime.")
			case ResourceTypeModelSlot:
				record.AssignedModel = strings.TrimPrefix(reservation.ResourceID, "model.")
			case ResourceTypePort:
				port, err := strconv.Atoi(strings.TrimPrefix(reservation.ResourceID, "port."))
				if err != nil {
					return fmt.Errorf("reservation: invalid persisted port %q: %w", reservation.ResourceID, err)
				}
				record.AssignedPort = port
			default:
				return fmt.Errorf("reservation: unsupported resource type %q", reservation.ResourceType)
			}
			record.LeaseID = reservation.LeaseID
			record.LeaseExpiry = reservation.LeaseExpiry
		}
		if record.State == allocation.StateReserved {
			return nil // idempotent replay also repairs assigned fields
		}
		_, err := record.Transition(allocation.StateReserved, "phase 3 reservation bundle committed")
		return err
	})
}

var _ AllocationValidator = (*AllocationReservationAdapter)(nil)
var _ ReservationAssignmentFinalizer = (*AllocationReservationAdapter)(nil)

// UpdateReservationLease mirrors the durable bundle lease to the allocation
// record. Reservation storage remains authoritative if this second write fails.
func (a *AllocationReservationAdapter) UpdateReservationLease(id, leaseID string, expiry time.Time) error {
	return a.store.Update(id, func(record *allocation.Allocation) error {
		if record.State != allocation.StateReserved {
			return fmt.Errorf("allocation %s cannot update lease from %s", id, record.State)
		}
		record.LeaseID = leaseID
		record.LeaseExpiry = expiry
		return nil
	})
}
