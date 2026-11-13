package fabric

import (
	"sort"
	"time"
)

// The reconciliation engine (design §4.2, roadmap §9.5's "Node reappears:
// reconcile allocations before admitting new workloads" and "Remote stop
// ambiguous: preserve reservation until node reconciliation proves
// cleanup").
//
// The engine is PURE: (the agent's reported execution records) × (the
// ledger's held reservations for the node) → typed decisions, sorted by
// allocation ID (deterministic — repeated reconciliation converges; the
// idempotency proof). It releases NOTHING itself: every decision names the
// trusted path that must apply it (the single ledger's transitions).

// AgentExecutionState is the node agent's reported state for one
// allocation it knows about (its OWN execution records — never a claim
// about the ledger).
type AgentExecutionState string

const (
	// AgentExecRunning — the agent affirms the workload is running.
	AgentExecRunning AgentExecutionState = "running"
	// AgentExecStopped — the agent affirms the workload is stopped and
	// cleanup was verified by the agent.
	AgentExecStopped AgentExecutionState = "stopped"
	// AgentExecStartUncertain — a start was requested; the outcome is
	// ambiguous (fail-closed vocabulary, mirrors allocation semantics).
	AgentExecStartUncertain AgentExecutionState = "start-uncertain"
	// AgentExecStopUncertain — a stop was requested; the outcome is
	// ambiguous (§9.5: preserve the reservation until proven).
	AgentExecStopUncertain AgentExecutionState = "stop-uncertain"
	// AgentExecNeverStarted — the agent affirms it never started this
	// allocation.
	AgentExecNeverStarted AgentExecutionState = "never-started"
)

// AgentExecutionRecord is one entry of the agent's execution report.
type AgentExecutionRecord struct {
	AllocationID     string              `json:"allocation_id"`
	State            AgentExecutionState `json:"state"`
	RuntimeProfileID string              `json:"runtime_profile_id,omitempty"`
	ModelID          string              `json:"model_id,omitempty"`
	ObservedAt       time.Time           `json:"observed_at"`
}

// LedgerReservationView is the ledger's answer for one allocation ON THE
// RECONCILED NODE: does the single authoritative ledger hold live
// reservations for it? Constructed by the wiring layer from the
// reservation store (the fabric engine never reads the store itself).
type LedgerReservationView struct {
	AllocationID string `json:"allocation_id"`
	Held         bool   `json:"held"`
}

// ReconcileAction is the typed decision for one allocation.
type ReconcileAction string

const (
	// ActionReservationStands — ledger held + agent running: the normal
	// case; admission on the node may resume.
	ActionReservationStands ReconcileAction = "reservation-stands"
	// ActionReconcileRequired — ledger held but the agent affirms
	// stopped/never-started, or reports an uncertain outcome, or knows
	// nothing about the allocation: the allocation moves to
	// RECONCILE_REQUIRED and the release happens ONLY through the trusted
	// reconciliation path (never a blind recycle).
	ActionReconcileRequired ReconcileAction = "reconcile-required"
	// ActionStopForeignExecution — the agent reports running work with NO
	// ledger reservation: un-ledgered execution is stopped and reported —
	// never silently admitted retroactively.
	ActionStopForeignExecution ReconcileAction = "stop-foreign-execution"
)

// ReconcileDecision is one allocation's typed outcome.
type ReconcileDecision struct {
	AllocationID string          `json:"allocation_id"`
	Action       ReconcileAction `json:"action"`
	Reason       string          `json:"reason"`
}

// ReconcileNode compares one node's agent execution report against the
// ledger's held reservations for that node. Pure and deterministic:
// decisions are emitted sorted by allocation ID; identical inputs always
// produce the identical report (the convergence/idempotency guarantee).
//
// Semantics (design §4.2):
//
//	ledger HELD + agent running                    → reservation-stands
//	ledger HELD + agent stopped/never-started      → reconcile-required
//	ledger HELD + agent start/stop-uncertain       → reconcile-required (fail-closed)
//	ledger HELD + NO agent record at all            → reconcile-required (unknown)
//	agent running + NO ledger reservation          → stop-foreign-execution
//	agent stopped + NO ledger reservation          → (no decision — nothing to do)
func ReconcileNode(agentRecords []AgentExecutionRecord, ledger []LedgerReservationView) []ReconcileDecision {
	agentBy := make(map[string]AgentExecutionRecord, len(agentRecords))
	for _, rec := range agentRecords {
		agentBy[rec.AllocationID] = rec
	}
	ledgerBy := make(map[string]LedgerReservationView, len(ledger))
	for _, view := range ledger {
		ledgerBy[view.AllocationID] = view
	}

	// The decision universe: every ledger allocation + every agent record,
	// de-duplicated, then sorted (determinism).
	seen := make(map[string]bool, len(ledgerBy)+len(agentBy))
	ids := make([]string, 0, len(ledgerBy)+len(agentBy))
	for id := range ledgerBy {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for id := range agentBy {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	out := make([]ReconcileDecision, 0, len(ids))
	for _, id := range ids {
		led, ledExists := ledgerBy[id]
		agent, agentExists := agentBy[id]

		switch {
		case ledExists && led.Held:
			switch {
			case agentExists && agent.State == AgentExecRunning:
				out = append(out, ReconcileDecision{
					AllocationID: id,
					Action:       ActionReservationStands,
					Reason:       "the agent affirms the workload running — the ledger's reservation stands; admission may resume",
				})
			case agentExists && (agent.State == AgentExecStopped || agent.State == AgentExecNeverStarted):
				out = append(out, ReconcileDecision{
					AllocationID: id,
					Action:       ActionReconcileRequired,
					Reason:       "the ledger holds the reservation but the agent affirms " + string(agent.State) + " — release ONLY through the trusted reconciliation path, never a blind recycle",
				})
			case agentExists:
				// start-uncertain / stop-uncertain — §9.5's preserve-until-proven.
				out = append(out, ReconcileDecision{
					AllocationID: id,
					Action:       ActionReconcileRequired,
					Reason:       "the agent reports " + string(agent.State) + " — the outcome is ambiguous; the reservation is PRESERVED until reconciliation proves cleanup (roadmap §9.5)",
				})
			default:
				out = append(out, ReconcileDecision{
					AllocationID: id,
					Action:       ActionReconcileRequired,
					Reason:       "the ledger holds the reservation but the agent reports NO record for this allocation — unknown execution state fails closed to reconciliation",
				})
			}
		case agentExists && agent.State == AgentExecRunning:
			// No live ledger reservation (either absent or not held).
			out = append(out, ReconcileDecision{
				AllocationID: id,
				Action:       ActionStopForeignExecution,
				Reason:       "the agent reports running work with NO ledger reservation — un-ledgered execution is stopped and reported, never silently admitted retroactively",
			})
		default:
			// agent stopped/uncertain + no live reservation: nothing to
			// decide — no decision emitted (the honest absence).
		}
	}
	return out
}
