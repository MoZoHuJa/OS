package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/MoZoHuJa/OS/scarlihq/internal/contract"
)

// AgentService is the node-side domain logic: the reservation mirror,
// the durable execution records, and the typed start/stop/reconcile
// operations with every §3 gate. It is deliberately transport-free —
// server.go binds it to HTTP, tests bind it directly.
type AgentService struct {
	mu       sync.Mutex
	clock    Clock
	executor AgentExecutor
	mirror   map[string]*mirrorEntry // reservationID → notice
	records  map[string]*ExecutionRecord
	persist  RecordStore // optional durability (nil = in-memory only, tests)
}

type mirrorEntry struct {
	notice ReservationNotice
}

// NewAgentService constructs the agent's domain core. Nil executor is
// refused (fail-closed: an agent that cannot execute anything is not an
// agent).
func NewAgentService(clock Clock, executor AgentExecutor, persist RecordStore) (*AgentService, error) {
	if clock == nil {
		return nil, fmt.Errorf("nodeagent: clock is required")
	}
	if executor == nil {
		return nil, fmt.Errorf("nodeagent: an executor is required — the typed boundary is the point")
	}
	s := &AgentService{
		clock:    clock,
		executor: executor,
		mirror:   make(map[string]*mirrorEntry),
		records:  make(map[string]*ExecutionRecord),
		persist:  persist,
	}
	if persist != nil {
		records, err := persist.LoadRecords()
		if err != nil {
			return nil, fmt.Errorf("nodeagent: cannot load execution records: %w", err)
		}
		for i := range records {
			rec := records[i]
			s.records[rec.AllocationID] = &rec
		}
		notices, err := persist.LoadMirror()
		if err != nil {
			return nil, fmt.Errorf("nodeagent: cannot load the reservation mirror: %w", err)
		}
		for i := range notices {
			n := notices[i]
			s.mirror[n.ReservationID] = &mirrorEntry{notice: n}
		}
	}
	return s, nil
}

// NotifyReservation records/updates the control plane's mirror notice
// (idempotent per reservation ID; a Released notice deactivates it).
func (s *AgentService) NotifyReservation(notice ReservationNotice) error {
	if err := notice.Validate(); err != nil {
		return err
	}
	if notice.NotifiedAt.IsZero() {
		notice.NotifiedAt = s.clock.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mirror[notice.ReservationID] = &mirrorEntry{notice: notice}
	return s.persistMirrorLocked()
}

// Mirror returns the mirror notices (sorted by reservation ID —
// deterministic reconcile output).
func (s *AgentService) Mirror() []ReservationNotice {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ReservationNotice, 0, len(s.mirror))
	ids := make([]string, 0, len(s.mirror))
	for id := range s.mirror {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, s.mirror[id].notice)
	}
	return out
}

// Records returns the execution records (sorted by allocation ID).
func (s *AgentService) Records() []ExecutionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ExecutionRecord, 0, len(s.records))
	ids := make([]string, 0, len(s.records))
	for id := range s.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, *s.records[id])
	}
	return out
}

// Start executes the typed start with every §3 gate, in order:
// (1) shape, (2) mirror — the ledger must hold the reservation live for
// THIS allocation, (3) the closed runtime allowlist, (4) idempotency /
// conflict against the agent's own records, (5) the executor — with
// fail-closed outcome recording.
func (s *AgentService) Start(ctx context.Context, cmd StartCommand) (ExecutionRecord, error) {
	if err := cmd.Validate(); err != nil {
		return ExecutionRecord{}, err
	}
	now := s.clock.Now()

	s.mu.Lock()
	mirror := s.mirror[cmd.ReservationID]
	existing := s.records[cmd.AllocationID]
	s.mu.Unlock()

	// Gate 2 — the mirror: the reservation must exist and belong to THIS
	// allocation, and must not be released.
	if mirror == nil {
		return ExecutionRecord{}, fmt.Errorf("%w (reservation %s, allocation %s)", ErrReservationUnknown, cmd.ReservationID, cmd.AllocationID)
	}
	if mirror.notice.AllocationID != cmd.AllocationID {
		return ExecutionRecord{}, fmt.Errorf("%w (reservation %s belongs to allocation %s, not %s)", ErrReservationUnknown, cmd.ReservationID, mirror.notice.AllocationID, cmd.AllocationID)
	}
	if mirror.notice.Released {
		return ExecutionRecord{}, fmt.Errorf("%w (reservation %s)", ErrReservationReleased, cmd.ReservationID)
	}

	// Gate 3 — the closed runtime allowlist (the Resource Contract's
	// allowlist — sglang/vllm/beellama/ollama/litellm; never a caller
	// choice).
	if !allowedRuntime(cmd.RuntimeID) {
		return ExecutionRecord{}, fmt.Errorf("%w: %q", ErrRuntimeNotAllowed, cmd.RuntimeID)
	}

	// Gate 4 — idempotency and conflict against the agent's own records.
	if existing != nil {
		switch existing.State {
		case AgentExecRunning:
			if existing.ReservationID == cmd.ReservationID && existing.RuntimeID == cmd.RuntimeID && existing.ModelID == cmd.ModelID {
				return *existing, nil // the idempotent replay of the SAME start
			}
			return ExecutionRecord{}, fmt.Errorf("%w (running with different parameters: reservation=%s runtime=%s model=%s)", ErrStartConflict, existing.ReservationID, existing.RuntimeID, existing.ModelID)
		case AgentExecStartUncertain, AgentExecStopUncertain:
			// An ambiguous record is NEVER blindly retried — the
			// reconciliation path decides (fail-closed vocabulary).
			return ExecutionRecord{}, fmt.Errorf("%w (state %s — reconcile first)", ErrStartConflict, existing.State)
		case AgentExecStopped, AgentExecNeverStarted:
			// Re-start after a clean stop/never-started: fall through to a
			// fresh executor call (the ledger drove the sequence).
		}
	}

	// Gate 5 — the executor, fail-closed outcome recording.
	outcome, err := s.executor.Start(ctx, cmd)
	if errors.Is(err, ErrExecutionUnavailable) {
		// A DETERMINISTIC refusal (the node's execution boundary is
		// unavailable — no side effect is possible): refuse WITHOUT
		// recording uncertainty (a deterministic refusal must not poison
		// reconciliation; uncertainty is for genuinely ambiguous effects).
		return ExecutionRecord{}, err
	}
	rec := &ExecutionRecord{
		AllocationID:  cmd.AllocationID,
		ReservationID: cmd.ReservationID,
		RuntimeID:     cmd.RuntimeID,
		ModelID:       cmd.ModelID,
		UpdatedAt:     now,
	}
	switch {
	case err != nil:
		// An executor error is AMBIGUOUS, not a failure: the side effect
		// may have happened. Fail closed to start-uncertain.
		rec.State = AgentExecStartUncertain
	case outcome.Ready && outcome.Owned:
		rec.State = AgentExecRunning
		rec.StartedAt = now
	case outcome.Ready && !outcome.Owned:
		// A pre-existing/shared workload: the agent did NOT start it —
		// never a candidate for this agent's stop.
		rec.State = AgentExecNeverStarted
	default:
		// Ready=false without error: the driver could not positively
		// observe readiness — ambiguous, fail closed.
		rec.State = AgentExecStartUncertain
	}

	s.mu.Lock()
	s.records[cmd.AllocationID] = rec
	if err2 := s.persistRecordsLocked(); err2 != nil {
		return ExecutionRecord{}, fmt.Errorf("nodeagent: start decided (%s) but persistence failed: %w", rec.State, err2)
	}
	s.mu.Unlock()
	if err != nil {
		// The DECISION is recorded durably; the caller learns the executor
		// error for diagnostics (the record's state is the authority).
		return *rec, fmt.Errorf("nodeagent: the executor start was ambiguous — recorded %s (reconciliation decides, never a blind retry): %w", rec.State, err)
	}
	return *rec, nil
}

// Stop executes the typed stop with the ownership gate: the agent stops
// ONLY allocations it itself started. A shared/pre-existing workload and
// an unknown allocation are both refused by name. Ambiguous outcomes
// record stop-uncertain (§9.5: preserve the reservation until proven).
func (s *AgentService) Stop(ctx context.Context, cmd StopCommand) (ExecutionRecord, error) {
	if err := cmd.Validate(); err != nil {
		return ExecutionRecord{}, err
	}
	now := s.clock.Now()

	s.mu.Lock()
	rec := s.records[cmd.AllocationID]
	s.mu.Unlock()

	if rec == nil {
		return ExecutionRecord{}, fmt.Errorf("%w (allocation %s)", ErrAllocationUnknown, cmd.AllocationID)
	}
	switch rec.State {
	case AgentExecRunning, AgentExecStartUncertain:
		// Stoppable: positively owned states.
	case AgentExecNeverStarted:
		return ExecutionRecord{}, fmt.Errorf("%w (allocation %s is a shared/pre-existing workload this agent did not start)", ErrAllocationNotOwned, cmd.AllocationID)
	case AgentExecStopped, AgentExecStopUncertain:
		// Idempotent-ish: a stopped record reports its state; a stop-
		// uncertain record is NOT blindly re-stopped (reconcile decides).
		if rec.State == AgentExecStopped {
			return *rec, nil
		}
		return ExecutionRecord{}, fmt.Errorf("%w (state %s — reconcile first)", ErrStartConflict, rec.State)
	}

	outcome, err := s.executor.Stop(ctx, cmd.AllocationID, "")
	if errors.Is(err, ErrExecutionUnavailable) {
		// Deterministic refusal — no record change (see Start's note).
		return ExecutionRecord{}, err
	}
	switch {
	case err != nil:
		rec.State = AgentExecStopUncertain
	case outcome.Verified:
		rec.State = AgentExecStopped
		rec.StoppedAt = now
	default:
		rec.State = AgentExecStopUncertain
	}
	rec.UpdatedAt = now

	s.mu.Lock()
	s.records[cmd.AllocationID] = rec
	if err2 := s.persistRecordsLocked(); err2 != nil {
		return ExecutionRecord{}, fmt.Errorf("nodeagent: stop decided (%s) but persistence failed: %w", rec.State, err2)
	}
	s.mu.Unlock()
	if err != nil {
		return *rec, fmt.Errorf("nodeagent: the executor stop was ambiguous — recorded %s (the reservation is preserved until reconciliation proves cleanup; roadmap §9.5): %w", rec.State, err)
	}
	return *rec, nil
}

// persistRecordsLocked / persistMirrorLocked: durable snapshots (small
// N, whole-state rewrite). Callers hold the mutex.
func (s *AgentService) persistRecordsLocked() error {
	if s.persist == nil {
		return nil
	}
	out := make([]ExecutionRecord, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, *rec)
	}
	return s.persist.SaveRecords(out)
}

func (s *AgentService) persistMirrorLocked() error {
	if s.persist == nil {
		return nil
	}
	out := make([]ReservationNotice, 0, len(s.mirror))
	for _, m := range s.mirror {
		out = append(out, m.notice)
	}
	return s.persist.SaveMirror(out)
}

// allowedRuntime is the closed runtime allowlist — delegated to the
// Resource Contract (the repo's single source of truth: sglang, vllm,
// beellama, ollama, litellm; the allowlist is CODE, never config).
func allowedRuntime(id string) bool {
	return contract.IsSupportedRuntime(id)
}
