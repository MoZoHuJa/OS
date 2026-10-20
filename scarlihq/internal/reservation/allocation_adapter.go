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
// KPK-19.2.12-P2-02: the whole bundle is validated for shared invariants
// (owner, lease, bundle size, duplicates) before any assignment is written,
// so a logically inconsistent bundle can never be finalized as one atomic
// lease and the last record can no longer silently win the lease metadata.
func (a *AllocationReservationAdapter) FinalizeReservation(id string, reservations []*Reservation) error {
	if len(reservations) == 0 {
		return fmt.Errorf("reservation: cannot finalize an empty reservation bundle")
	}
	if err := validateActiveBundle(reservations, id); err != nil {
		return err
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

// BeginAllocationRelease persists cleanup progress for an allocation whose
// reservation bundle is being released. KPK-19.2.12-P1-01: the first release
// event transitions RESERVED → RELEASING so the allocation record can never
// claim unassigned resources are still owned while its reservations are
// being released.
//
// Idempotency: RELEASING and RELEASED are accepted as no-ops so repeated
// release events and restart recovery converge without spurious errors.
func (a *AllocationReservationAdapter) BeginAllocationRelease(id string) error {
	return a.store.Update(id, func(record *allocation.Allocation) error {
		switch record.State {
		case allocation.StateReleasing, allocation.StateReleased:
			return nil // idempotent — cleanup progress already persisted
		case allocation.StateCleanupRequired:
			// Already operator-visible and blocked: keep the stricter state.
			return nil
		case allocation.StateReserved, allocation.StateStarting, allocation.StateReady, allocation.StateRunning, allocation.StateFailed, allocation.StateCancelled:
			_, err := record.Transition(allocation.StateReleasing, "reservation bundle release in progress")
			return err
		default:
			return fmt.Errorf("allocation %s cannot begin release from %s", id, record.State)
		}
	})
}

// CompleteAllocationRelease finishes the release lifecycle once every
// reservation bundle member is RELEASED with verified cleanup evidence.
// KPK-19.2.12-P1-01: RELEASING → RELEASED clears the assignment fields and
// lease metadata so a released GPU/runtime/model/port can never remain
// visibly assigned to the allocation.
// KPK-19.2.13-P1-02: FAILED and CANCELLED allocations with a reservation
// bundle can no longer jump directly to RELEASED — the frozen transition
// table forbids FAILED → RELEASED and CANCELLED → RELEASED (only
// FAILED/CANCELLED → RELEASING/CLEANUP_REQUIRED are legal). They now walk
// through RELEASING first, exactly like the RESERVED and CLEANUP_REQUIRED
// paths, so the terminal transition below is always table-legal.
//
// A crash between the reservation release and this call is repaired by the
// startup recovery scan: RESERVED → RELEASING → RELEASED is walked inside a
// single store transaction when the allocation is still RESERVED.
func (a *AllocationReservationAdapter) CompleteAllocationRelease(id string) error {
	return a.store.Update(id, func(record *allocation.Allocation) error {
		switch record.State {
		case allocation.StateReleased:
			// Idempotent replay: repair any stale assignment fields left by an
			// interrupted earlier completion, then stop — RELEASED is terminal.
			record.AssignedGPU = ""
			record.AssignedRuntime = ""
			record.AssignedModel = ""
			record.AssignedPort = 0
			record.LeaseID = ""
			record.LeaseExpiry = time.Time{}
			return nil
		case allocation.StateReserved:
			// Crash-recovery path: the allocation never observed the first
			// release event. Walk RESERVED → RELEASING first, then complete.
			if _, err := record.Transition(allocation.StateReleasing, "release completion replay"); err != nil {
				return err
			}
		case allocation.StateFailed, allocation.StateCancelled:
			// KPK-19.2.13-P1-02: the frozen table does NOT permit
			// FAILED/CANCELLED → RELEASED. Walk through RELEASING first —
			// both FAILED → RELEASING and CANCELLED → RELEASING are legal —
			// so the terminal transition below is table-legal for every source
			// state this method accepts.
			if _, err := record.Transition(allocation.StateReleasing, "release completion for failed/cancelled allocation with reservation bundle"); err != nil {
				return err
			}
		case allocation.StateReleasing:
			// Direct legal path into the terminal release below.
		case allocation.StateCleanupRequired:
			// Reached when every bundle member was subsequently released with
			// verified cleanup evidence. The frozen table allows
			// CLEANUP_REQUIRED → RELEASING → RELEASED; completing is justified
			// because each member's release required positive cleanup evidence.
			if _, err := record.Transition(allocation.StateReleasing, "cleanup evidence satisfied for every bundle member"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("allocation %s cannot complete release from %s", id, record.State)
		}
		// KPK-01 (v19.2.3) invariant: CleanupVerified must be true BEFORE the
		// transition to RELEASED. CompleteAllocationRelease is only invoked when
		// every bundle member is durably RELEASED — each of those releases
		// required positive cleanup evidence at the reservation layer — so the
		// evidence chain justifies recording the verified flag here, before the
		// terminal transition.
		record.CleanupStatus = "verified"
		record.CleanupVerified = true
		if _, err := record.Transition(allocation.StateReleased, "reservation bundle fully released and cleanup verified"); err != nil {
			return err
		}
		record.AssignedGPU = ""
		record.AssignedRuntime = ""
		record.AssignedModel = ""
		record.AssignedPort = 0
		record.LeaseID = ""
		record.LeaseExpiry = time.Time{}
		return nil
	})
}

// MarkAllocationCleanupRequired persists an operator-visible blocked state
// for an allocation whose reservation bundle cannot be safely released
// (e.g. a member is in CONFLICT or cleanup could not be verified).
// KPK-19.2.12-P1-01: never report the allocation fully released when a
// bundle member is blocked.
func (a *AllocationReservationAdapter) MarkAllocationCleanupRequired(id, reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "cleanup required"
	}
	return a.store.Update(id, func(record *allocation.Allocation) error {
		switch record.State {
		case allocation.StateCleanupRequired:
			record.CleanupStatus = "pending"
			return nil // idempotent — already blocked for the operator
		case allocation.StateReleased:
			return fmt.Errorf("allocation %s is already RELEASED — cannot mark cleanup required", id)
		case allocation.StateReserved, allocation.StateStarting, allocation.StateReady, allocation.StateRunning:
			// Walk through RELEASING first: the frozen transition table does
			// not allow a direct RESERVED → CLEANUP_REQUIRED jump.
			if _, err := record.Transition(allocation.StateReleasing, "release interrupted: "+reason); err != nil {
				return err
			}
		}
		_, err := record.Transition(allocation.StateCleanupRequired, reason)
		return err
	})
}

// ListReleasingAllocations returns allocation IDs whose release lifecycle
// must be completed or kept blocked by the recovery scan: RELEASING,
// CLEANUP_REQUIRED, and — KPK-19.2.13-P1-02 — FAILED and CANCELLED.
// A FAILED/CANCELLED allocation that still has a reservation bundle with
// release progress is an interrupted release in a legal source state of the
// frozen release lifecycle; without this scan it could never converge
// (ListReservedAllocations only covers RESERVED).
// KPK-19.2.12-P1-01: ReconcileAllPending only enumerated RESERVING
// allocations, so an allocation interrupted mid-release could never be
// discovered again.
func (a *AllocationReservationAdapter) ListReleasingAllocations() ([]string, error) {
	records, corrupt, err := a.store.Load()
	if err != nil {
		return nil, err
	}
	if len(corrupt) > 0 {
		return nil, fmt.Errorf("%w: allocation store has %d corrupt records", ErrReconciliationRequired, len(corrupt))
	}
	ids := make([]string, 0)
	for _, record := range records {
		if record == nil {
			continue
		}
		switch record.State {
		case allocation.StateReleasing, allocation.StateCleanupRequired, allocation.StateFailed, allocation.StateCancelled:
			if record.AllocationID != "" {
				ids = append(ids, record.AllocationID)
			}
		}
	}
	return ids, nil
}

// ListReservedAllocations returns allocation IDs currently in RESERVED —
// the input set for the reserved-lease repair scan.
// KPK-19.2.12-P1-02: replaces the previous direct store access from the
// manager with an explicit, testable adapter capability.
func (a *AllocationReservationAdapter) ListReservedAllocations() ([]string, error) {
	records, corrupt, err := a.store.Load()
	if err != nil {
		return nil, err
	}
	if len(corrupt) > 0 {
		return nil, fmt.Errorf("%w: allocation store has %d corrupt records", ErrReconciliationRequired, len(corrupt))
	}
	ids := make([]string, 0)
	for _, record := range records {
		if record == nil {
			continue
		}
		if record.State == allocation.StateReserved && record.AllocationID != "" {
			ids = append(ids, record.AllocationID)
		}
	}
	return ids, nil
}

// GetAllocation exposes the durable allocation record for reconciliation
// comparisons (lease repair reads the current lease metadata without
// mutating it).
func (a *AllocationReservationAdapter) GetAllocation(id string) (*allocation.Allocation, error) {
	return a.getSafe(id)
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
