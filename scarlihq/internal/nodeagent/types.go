// Package nodeagent — the remote node's NARROW TYPED execution boundary
// (v19.9.0, Compute Fabric Phase 5, roadmap §9.3 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md,
// design: docs/SCARLIX_COMPUTE_FABRIC_PHASE_5_DISTRIBUTED_DESIGN.md §3).
//
// CHARTER (the non-negotiable contract):
//
//   - The agent exposes EXACTLY the typed operations of §9.3: inventory
//     snapshot, health, reservation mirror notify, start approved
//     profile, stop owned allocation, reconcile. There is NO arbitrary
//     shell, NO arbitrary Docker API, NO arbitrary file access, NO
//     arbitrary port publishing — structurally: the request types carry
//     only structured identifiers, and the only execution boundary is
//     the injected AgentExecutor (the workload.Driver discipline).
//   - The agent is NON-AUTHORITATIVE (design §1): its mirror of
//     control-plane reservations exists to VALIDATE starts — a start
//     referencing a reservation the ledger never made is refused (the
//     rogue-start abuse case closed at the node itself); its execution
//     records are the reconciliation evidence, never a capacity claim.
//   - Fail-closed outcomes: an ambiguous start/stop records
//     start-uncertain/stop-uncertain (the vocabulary the reconciliation
//     engine consumes) — never a fabricated success.
//   - Stop ownership: the agent stops ONLY allocations it itself
//     started (checked against its own execution records) — a foreign or
//     shared workload is refused by name.
//   - Tailscale/LAN transport is NOT authorization: every request is
//     authenticated with the control-plane credential (constant-time
//     compare), and the start/stop requests additionally carry request
//     IDs whose responses are cached for bounded idempotent retry.
package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Clock is the injected time source.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// AgentExecutionState is the agent's reported state for one allocation —
// the same closed vocabulary the fabric reconciliation engine consumes
// (fabric.AgentExecutionState's strings; duplicated here so the agent
// package owns its own contract — the values are the wire vocabulary).
type AgentExecutionState string

const (
	AgentExecRunning        AgentExecutionState = "running"
	AgentExecStopped        AgentExecutionState = "stopped"
	AgentExecStartUncertain AgentExecutionState = "start-uncertain"
	AgentExecStopUncertain  AgentExecutionState = "stop-uncertain"
	AgentExecNeverStarted   AgentExecutionState = "never-started"
)

// ReservationNotice is the control plane's mirror notification: the
// ledger made this reservation FOR THIS NODE. The mirror is explicitly
// non-authoritative — it validates starts, it never grants capacity.
type ReservationNotice struct {
	ReservationID string    `json:"reservation_id"`
	AllocationID  string    `json:"allocation_id"`
	NodeID        string    `json:"node_id"`
	Released      bool      `json:"released,omitempty"`
	NotifiedAt    time.Time `json:"notified_at"`
}

// StartCommand is the typed start ask (structured identifiers only — no
// commands, no environments, no paths, no images from the caller).
type StartCommand struct {
	RequestID     string `json:"request_id"` // the CP's idempotency identity
	AllocationID  string `json:"allocation_id"`
	ReservationID string `json:"reservation_id"`
	RuntimeID     string `json:"runtime_id"`
	ModelID       string `json:"model_id"`
	GPUID         string `json:"gpu_id,omitempty"`
	Port          int    `json:"port,omitempty"`
}

// StopCommand is the typed stop ask.
type StopCommand struct {
	RequestID    string `json:"request_id"`
	AllocationID string `json:"allocation_id"`
}

// ExecutionRecord is the agent's durable evidence of what it did — the
// reconciliation input (design §4.2). Digest-free, secret-free.
type ExecutionRecord struct {
	AllocationID  string              `json:"allocation_id"`
	ReservationID string              `json:"reservation_id,omitempty"`
	RuntimeID     string              `json:"runtime_id,omitempty"`
	ModelID       string              `json:"model_id,omitempty"`
	State         AgentExecutionState `json:"state"`
	StartedAt     time.Time           `json:"started_at,omitempty"`
	StoppedAt     time.Time           `json:"stopped_at,omitempty"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

// StartOutcome / StopOutcome are the executor's answers (the
// workload.Driver discipline: positive observation, Owned semantics,
// bounded opaque Ref).
type StartOutcome struct {
	Ref   string // opaque, bounded driver reference
	Ready bool   // true only after POSITIVE readiness observation
	Owned bool   // false for pre-existing/shared workloads
}

type StopOutcome struct {
	Verified bool // true only after POSITIVE cleanup verification
}

// AgentExecutor is the ONLY execution boundary the service may call —
// the same rules as workload.Driver: idempotent by AllocationID, runtime
// allowlists enforced, fail closed, never caller-supplied commands.
// The production implementation crosses the node's own hardened host
// bridge (the cmd/scarlix-host-workload-bridge pattern).
type AgentExecutor interface {
	Start(ctx context.Context, cmd StartCommand) (StartOutcome, error)
	Stop(ctx context.Context, allocationID, ref string) (StopOutcome, error)
}

// Sentinel errors (typed refusals — the wire layer maps them to codes).
var (
	ErrReservationUnknown  = errors.New("nodeagent: the reservation is not in this agent's mirror — the ledger never told this node about it (rogue start refused)")
	ErrReservationReleased = errors.New("nodeagent: the reservation was released by the ledger — no start is permitted against it")
	ErrRuntimeNotAllowed   = errors.New("nodeagent: runtime ID outside the closed allowlist")
	ErrAlreadyRunning      = errors.New("nodeagent: the allocation is already running here")
	ErrStartConflict       = errors.New("nodeagent: the allocation is known with different parameters — a conflicting start is refused")
	ErrAllocationNotOwned  = errors.New("nodeagent: the agent did not start this allocation — stop is refused (foreign/shared workload)")
	ErrAllocationUnknown   = errors.New("nodeagent: no execution record for this allocation")
	ErrInvalidCommand      = errors.New("nodeagent: malformed typed command")
)

// validateIdentifier enforces the closed ID shape (bounded, no control
// characters, no whitespace-only).
func validateIdentifier(field, value string, max int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalidCommand, field)
	}
	if len(value) > max {
		return fmt.Errorf("%w: %s exceeds %d characters", ErrInvalidCommand, field, max)
	}
	for _, c := range value {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("%w: %s contains control characters", ErrInvalidCommand, field)
		}
	}
	return nil
}

// Validate checks the start command's closed shape.
func (c StartCommand) Validate() error {
	if err := validateIdentifier("request_id", c.RequestID, 128); err != nil {
		return err
	}
	if err := validateIdentifier("allocation_id", c.AllocationID, 128); err != nil {
		return err
	}
	if err := validateIdentifier("reservation_id", c.ReservationID, 128); err != nil {
		return err
	}
	if err := validateIdentifier("runtime_id", c.RuntimeID, 64); err != nil {
		return err
	}
	if err := validateIdentifier("model_id", c.ModelID, 128); err != nil {
		return err
	}
	if c.GPUID != "" {
		if err := validateIdentifier("gpu_id", c.GPUID, 128); err != nil {
			return err
		}
	}
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("%w: port %d outside [0,65535]", ErrInvalidCommand, c.Port)
	}
	return nil
}

// Validate checks the stop command's closed shape.
func (c StopCommand) Validate() error {
	if err := validateIdentifier("request_id", c.RequestID, 128); err != nil {
		return err
	}
	if err := validateIdentifier("allocation_id", c.AllocationID, 128); err != nil {
		return err
	}
	return nil
}

// Validate checks the notice's closed shape.
func (n ReservationNotice) Validate() error {
	if err := validateIdentifier("reservation_id", n.ReservationID, 128); err != nil {
		return err
	}
	if err := validateIdentifier("allocation_id", n.AllocationID, 128); err != nil {
		return err
	}
	if err := validateIdentifier("node_id", n.NodeID, 64); err != nil {
		return err
	}
	return nil
}
