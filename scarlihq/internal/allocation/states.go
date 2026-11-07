// Package allocation defines the Compute Fabric allocation lifecycle — the
// canonical state machine for resource allocation requests.
//
// The domain model (states, transitions, the Allocation record type, JSONL
// persistence) landed in Phase 2 (v19.2.2); the reservation (Phase 3),
// workload-execution (Phase 5) and release/reconciliation (Phase 6) paths
// now drive it through their own packages — allocation remains the single
// lifecycle authority they all transition through (the v19.6.8 canonical
// architecture docs under docs/architecture/ describe the wiring).
//
// Stability contract:
//   - State names are FROZEN in v19.2.2. Future versions may ADD states but
//     MUST NOT rename or remove existing ones without a v2 migration.
//   - The transition table is FROZEN. Additions require a new state or an
//     explicit documented amendment.
//   - The Allocation record schema is v1 (additive-only).
//
// Relationship to existing packages:
//   - contract.ResourceContract: the REQUEST side (what the agent wants).
//   - scheduler.Plan: the DRY-RUN scoring output (what the planner recommends).
//   - allocation.Allocation: the LIFECYCLE side (what happened to the request
//     over time — states, transitions, ownership, cleanup status).
package allocation

// State represents a lifecycle state of a Compute Fabric allocation.
//
// States are ordered by the lifecycle progression but the numeric values
// are NOT sequential — they are grouped by phase to allow future insertions
// without renumbering.
//
// FROZEN in v19.2.2 — do not renumber existing values.
type State string

const (
	// Phase: request intake
	StateRequested State = "REQUESTED"

	// Phase: validation + planning
	StateValidating State = "VALIDATING"
	StatePlanned    State = "PLANNED"

	// Phase: reservation (Phase 3+ — no side effects in Phase 2)
	StateReserving State = "RESERVING"
	StateReserved  State = "RESERVED"

	// Phase: execution (Phase 5+ — no side effects in Phase 2)
	StateStarting State = "STARTING"
	StateReady    State = "READY"
	StateRunning  State = "RUNNING"

	// Phase: release + cleanup (Phase 6+ — no side effects in Phase 2)
	StateReleasing State = "RELEASING"
	StateReleased  State = "RELEASED"

	// Terminal-ish: failure / cancellation / cleanup uncertainty
	StateFailed          State = "FAILED"
	StateCancelled       State = "CANCELLED"
	StateCleanupRequired State = "CLEANUP_REQUIRED"

	// Phase: stop-request / ambiguous-outcome vocabulary (v19.6.8 — the
	// Compute Fabric Phase 0 lifecycle reconciliation, roadmap §4.3/§4.4).
	//
	// STOP_REQUESTED: a stop has been REQUESTED for a live workload
	// (READY/RUNNING/STARTING) but the driver stop has not yet been
	// verified. The stop request is recorded durably BEFORE the driver
	// call, so a crash between the request and the verified outcome leaves
	// an honest marker the retry path accepts idempotently. STOP requests
	// cleanup; it does not release capacity by itself (the reservation
	// bundle is released only by the trusted reconciliation path).
	//
	// RECONCILE_REQUIRED: an operation's outcome is AMBIGUOUS (e.g. the
	// driver answered but could not verify the stop) — a reconciler must
	// decide, never a blind retry. Distinct from CLEANUP_REQUIRED, which
	// means cleanup is affirmatively needed (an explicit stop failure or
	// an uncertain start: a workload may exist externally and must be
	// removed). The frozen-contract amendment is ADDITIVE: existing state
	// names and transitions are untouched; old persisted records remain
	// valid.
	StateStopRequested     State = "STOP_REQUESTED"
	StateReconcileRequired State = "RECONCILE_REQUIRED"
)

// AllStates returns every known allocation state.
// Used by tests to verify transition table coverage.
func AllStates() []State {
	return []State{
		StateRequested,
		StateValidating,
		StatePlanned,
		StateReserving,
		StateReserved,
		StateStarting,
		StateReady,
		StateRunning,
		StateStopRequested,
		StateReleasing,
		StateReleased,
		StateFailed,
		StateCancelled,
		StateCleanupRequired,
		StateReconcileRequired,
	}
}

// IsTerminal reports whether a state is terminal (no legal outgoing transitions).
// RELEASED is the only truly terminal state.
// FAILED, CANCELLED, and CLEANUP_REQUIRED are NOT terminal — they can transition
// to RELEASING or RELEASED during cleanup.
func (s State) IsTerminal() bool {
	return s == StateReleased
}

// IsValid reports whether s is a known allocation state.
func (s State) IsValid() bool {
	for _, known := range AllStates() {
		if s == known {
			return true
		}
	}
	return false
}

// String implements fmt.Stringer.
func (s State) String() string {
	return string(s)
}
