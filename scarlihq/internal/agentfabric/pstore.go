// CF-3 (v19.6.2): the persisted proposal store — proposals.json, the
// commands.json/workspaces.json/executions.json/approvals.json
// discipline, exactly:
//
//   - JSON file with an explicit schema version (1); Load rejects
//     anything else — a future schema change is an explicit migration.
//   - 0600 file / 0700 directory — checked at load AND before every
//     persist; a widened mode is tampering until an operator fixes it.
//   - Atomic persistence: temp file in the same directory (0600 by
//     construction, fsync) + rename; a failed persist rolls back the
//     in-memory mutation (state and file stay in sync).
//   - Proposals are never deleted — the file is the reviewable history
//     of every model proposal, acceptance and dismissal (there is
//     deliberately no withdrawal and no expiry: a PENDING proposal
//     stays answerable until the operator decides). Rotating the file
//     is an operator act.
//   - Bounded: DefaultMaxProposals records total; at capacity Capture
//     fails with ErrProposalLimit (the HTTP layer reports 503).
//   - ONE COMMAND REFERENCE PER PROPOSAL (an ACCEPTED proposal names
//     its command — and only the API layer's accept path sets it),
//     enforced on the mutation side; Load additionally aborts on a file
//     that carries two proposals referencing the SAME command — a
//     hand-edited duplicate is corruption, not "some proposals work".
//     The mirror direction (one proposal reference per command) is
//     enforced by the COMMAND store's Load (duplicate linkage = one
//     command born twice from one proposal = corruption).
//   - Proposal IDs are "prp-" + 16 crypto-random bytes hex; RNG failure
//     aborts the record (fail-closed).
//   - The state machine is PENDING-only-in-flight: Accept/Dismiss
//     operate exclusively on PENDING records — a terminal decision is
//     never re-decided (the file is the history).
//
// The store does NOT call the AI runtime and does NOT create commands:
// the API layer composes the chat transport, the parser, the BF-1
// submission path and the reconciliation (separate concerns, composed
// at the boundary — the P4 ownership table).
package agentfabric

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Sentinel errors for the proposal store.
var (
	// ErrProposalRNG: proposal-ID generation failed — fail-closed.
	ErrProposalRNG = errors.New("agentfabric: RNG unavailable (proposals)")
	// ErrUnknownProposal: no proposal with the requested ID.
	ErrUnknownProposal = errors.New("agentfabric: unknown proposal")
	// ErrProposalLimit: the store is at capacity (bounded history).
	ErrProposalLimit = errors.New("agentfabric: proposal store at capacity")
	// ErrProposalFilePermissions: the proposals file (or directory) is
	// looser than 0600/0700 — fail-closed.
	ErrProposalFilePermissions = errors.New("agentfabric: proposals file permissions too loose")
	// ErrProposalState: the record is not PENDING — terminal decisions
	// are never re-decided.
	ErrProposalState = errors.New("agentfabric: proposal is not PENDING — the decision is terminal")
)

// DefaultMaxProposals bounds the total number of proposal records (ALL
// states). Model proposals are bounded by chat volume; the bound keeps
// the file honest. At the bound, capturing stops (503) until the
// operator archives the file deliberately.
const DefaultMaxProposals = 256

// proposalsFileVersion is the on-disk schema version. Load rejects
// anything else — a future schema change must be an explicit migration,
// not a guess.
const proposalsFileVersion = 1

// proposalsFile is the on-disk representation. Proposals are stored in
// capture order — ListLast reverses for newest-first.
type proposalsFile struct {
	Version   int              `json:"version"`
	Proposals []ProposalRecord `json:"proposals"`
}

// ProposalStore is the persisted, bounded, mutex-guarded proposal store.
type ProposalStore struct {
	mu           sync.Mutex
	path         string
	maxProposals int
	proposals    []ProposalRecord
	byCommand    map[string]int // command id → index into proposals (ACCEPTED records only)
}

// NewProposalStore constructs an empty in-memory store bound to a file
// path. It does NOT touch the file — call Load to read (or initialize)
// it. Construction is fail-closed: an empty path or non-positive bound
// is a construction error.
func NewProposalStore(path string, maxProposals int) (*ProposalStore, error) {
	if path == "" {
		return nil, fmt.Errorf("agentfabric: proposals file path is required")
	}
	if maxProposals <= 0 {
		return nil, fmt.Errorf("agentfabric: maxProposals must be > 0 (got %d)", maxProposals)
	}
	return &ProposalStore{
		path:         path,
		maxProposals: maxProposals,
		proposals:    make([]ProposalRecord, 0),
		byCommand:    make(map[string]int),
	}, nil
}

// Load reads and validates the proposals file, or initializes it empty
// on first run. Validation is fail-closed: ANY malformed record (ID
// shapes, the bar's own intent/agent/capability validators, typed
// proposer attribution, closed enumerations, state↔fields coherence,
// monotone timestamps), ANY duplicate proposal ID, ANY duplicate command
// reference, ANY count above the bound, a wrong schema version, or
// looser-than-0600/0700 permissions aborts the load. A corrupt or
// tampered proposals file must never degrade into "some proposals work".
func (s *ProposalStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := os.Stat(s.path)
	switch {
	case err == nil:
		// exists — permissions checked below
	case errors.Is(err, os.ErrNotExist):
		// First run: initialize an empty, valid, 0600 file.
		if err := s.ensureDirLocked(); err != nil {
			return err
		}
		empty := proposalsFile{Version: proposalsFileVersion, Proposals: []ProposalRecord{}}
		if err := s.persistLocked(empty); err != nil {
			return fmt.Errorf("agentfabric: cannot initialize proposals file %s: %w", s.path, err)
		}
		s.proposals = make([]ProposalRecord, 0)
		s.byCommand = make(map[string]int)
		return nil
	default:
		return fmt.Errorf("agentfabric: cannot stat proposals file %s: %w", s.path, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: %s has mode %o (want 0600)", ErrProposalFilePermissions, s.path, info.Mode().Perm())
	}
	if err := s.checkDirPermsLocked(); err != nil {
		return err
	}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("agentfabric: cannot read proposals file %s: %w", s.path, err)
	}
	var f proposalsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("agentfabric: proposals file %s is not valid JSON: %w", s.path, err)
	}
	if f.Version != proposalsFileVersion {
		return fmt.Errorf("agentfabric: proposals file %s has schema version %d (want %d) — explicit migration required", s.path, f.Version, proposalsFileVersion)
	}
	if len(f.Proposals) > s.maxProposals {
		return fmt.Errorf("agentfabric: proposals file %s carries %d records (bound %d) — archive and rotate the file deliberately", s.path, len(f.Proposals), s.maxProposals)
	}
	seenIDs := make(map[string]bool, len(f.Proposals))
	byCommand := make(map[string]int, len(f.Proposals))
	for i, rec := range f.Proposals {
		if err := rec.validate(); err != nil {
			return fmt.Errorf("agentfabric: proposals file %s record %d: %w", s.path, i, err)
		}
		if seenIDs[rec.ID] {
			return fmt.Errorf("agentfabric: proposals file %s record %d: duplicate proposal id %s", s.path, i, rec.ID)
		}
		seenIDs[rec.ID] = true
		if rec.CommandID != "" {
			if _, dup := byCommand[rec.CommandID]; dup {
				return fmt.Errorf("agentfabric: proposals file %s record %d: command %s is referenced TWICE — one command reference per proposal, a duplicate is corruption (fail-closed)", s.path, i, rec.CommandID)
			}
			byCommand[rec.CommandID] = i
		}
	}
	// Copy in (defensive: never alias the decoded slice).
	s.proposals = make([]ProposalRecord, len(f.Proposals))
	copy(s.proposals, f.Proposals)
	s.byCommand = byCommand
	return nil
}

// ensureDirLocked creates the parent directory 0700 if missing.
func (s *ProposalStore) ensureDirLocked() error {
	dir := filepath.Dir(s.path)
	info, err := os.Stat(dir)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("agentfabric: %s is not a directory", dir)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("agentfabric: cannot create directory %s: %w", dir, err)
		}
	default:
		return fmt.Errorf("agentfabric: cannot stat directory %s: %w", dir, err)
	}
	return nil
}

// checkDirPermsLocked rejects a directory looser than 0700 (tamper
// discipline — no auto-widen, no auto-fix).
func (s *ProposalStore) checkDirPermsLocked() error {
	dir := filepath.Dir(s.path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("agentfabric: cannot stat directory %s: %w", dir, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: directory %s has mode %o (want 0700)", ErrProposalFilePermissions, dir, info.Mode().Perm())
	}
	return nil
}

// persistLocked writes the file atomically: temp file in the same
// directory (0600 by construction, fsync) + rename. On any error the
// caller is expected to roll back the in-memory state.
func (s *ProposalStore) persistLocked(f proposalsFile) error {
	if err := s.ensureDirLocked(); err != nil {
		return err
	}
	if err := s.checkDirPermsLocked(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("agentfabric: cannot encode proposals file: %w", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".proposals-*.tmp")
	if err != nil {
		return fmt.Errorf("agentfabric: cannot create temp file next to %s: %w", s.path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("agentfabric: cannot write temp file %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("agentfabric: cannot chmod temp file %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("agentfabric: cannot fsync temp file %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("agentfabric: cannot close temp file %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("agentfabric: cannot rename %s → %s: %w", tmpName, s.path, err)
	}
	return nil
}

// snapshotLocked returns the on-disk representation of the current state.
func (s *ProposalStore) snapshotLocked() proposalsFile {
	f := proposalsFile{Version: proposalsFileVersion, Proposals: make([]ProposalRecord, len(s.proposals))}
	copy(f.Proposals, s.proposals)
	return f
}

// Capture appends one PENDING proposal record and persists it. The
// record is validated (the store never holds a record it would refuse
// to load) and must be PENDING — capture is the propose endpoint's only
// write; decisions are Accept/Dismiss. At capacity ErrProposalLimit. On
// persist failure the in-memory append is rolled back (state and file
// stay in sync).
func (s *ProposalStore) Capture(rec ProposalRecord) (ProposalRecord, error) {
	if err := ProposalID(rec.ID).Validate(); err != nil {
		return ProposalRecord{}, err
	}
	if rec.State != ProposalPending {
		return ProposalRecord{}, fmt.Errorf("agentfabric: capture requires a PENDING record (got %s — decisions are Accept/Dismiss)", rec.State)
	}
	if err := rec.validate(); err != nil {
		return ProposalRecord{}, err
	}
	if rec.CapturedAt.IsZero() {
		return ProposalRecord{}, fmt.Errorf("agentfabric: capture time is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.proposals) >= s.maxProposals {
		return ProposalRecord{}, ErrProposalLimit
	}
	// Collision against an existing ID: cryptographically impossible, but
	// the check is free and the invariant (unique IDs) is load-enforced.
	for _, existing := range s.proposals {
		if existing.ID == rec.ID {
			return ProposalRecord{}, fmt.Errorf("agentfabric: proposal id collision (rng reuse) — capture aborted")
		}
	}
	s.proposals = append(s.proposals, rec)
	if err := s.persistLocked(s.snapshotLocked()); err != nil {
		s.proposals = s.proposals[:len(s.proposals)-1] // rollback
		return ProposalRecord{}, err
	}
	return copyProposalRecord(rec), nil
}

// Accept closes a PENDING proposal as ACCEPTED with the captured
// command's id — the operator's yes. The command reference is validated
// (one command reference per proposal, enforced here on the mutation
// side). A terminal proposal is never re-decided.
func (s *ProposalStore) Accept(id ProposalID, operator string, commandID string, decidedAt time.Time) (ProposalRecord, error) {
	if err := ValidatePrincipalLabel(operator); err != nil {
		return ProposalRecord{}, fmt.Errorf("agentfabric: operator: %v", err)
	}
	if err := CommandID(commandID).Validate(); err != nil {
		return ProposalRecord{}, err
	}
	if decidedAt.IsZero() {
		return ProposalRecord{}, fmt.Errorf("agentfabric: decision time is required")
	}
	return s.decide(id, func(rec ProposalRecord) ProposalRecord {
		rec.State = ProposalAccepted
		rec.Operator = operator
		rec.Reason = ""
		rec.CommandID = commandID
		rec.DecidedAt = &decidedAt
		return rec
	})
}

// Dismiss closes a PENDING proposal as DISMISSED (terminal, no command
// ever). The operator's no is unconditional — no kernel evaluation
// gates a dismissal (no act runs, there is nothing to evaluate). An
// optional bounded reason carries the why.
func (s *ProposalStore) Dismiss(id ProposalID, operator string, reason string, decidedAt time.Time) (ProposalRecord, error) {
	if err := ValidatePrincipalLabel(operator); err != nil {
		return ProposalRecord{}, fmt.Errorf("agentfabric: operator: %v", err)
	}
	if reason != "" {
		if err := ValidateExecutionReason(reason); err != nil {
			return ProposalRecord{}, err
		}
	}
	if decidedAt.IsZero() {
		return ProposalRecord{}, fmt.Errorf("agentfabric: decision time is required")
	}
	return s.decide(id, func(rec ProposalRecord) ProposalRecord {
		rec.State = ProposalDismissed
		rec.Operator = operator
		if reason != "" {
			rec.Reason = reason
		}
		rec.CommandID = ""
		rec.DecidedAt = &decidedAt
		return rec
	})
}

// decide applies fn to the PENDING record with the given id and
// persists the terminal decision (rollback on persist failure). Terminal
// records are never re-decided (ErrProposalState); unknown ids fail
// closed.
func (s *ProposalStore) decide(id ProposalID, fn func(ProposalRecord) ProposalRecord) (ProposalRecord, error) {
	if err := id.Validate(); err != nil {
		return ProposalRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, rec := range s.proposals {
		if rec.ID == string(id) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ProposalRecord{}, ErrUnknownProposal
	}
	if s.proposals[idx].State != ProposalPending {
		return ProposalRecord{}, fmt.Errorf("%w: %s is %s", ErrProposalState, id, s.proposals[idx].State)
	}
	updated := fn(copyProposalRecord(s.proposals[idx]))
	if err := updated.validate(); err != nil {
		return ProposalRecord{}, err
	}
	before := s.proposals[idx]
	s.proposals[idx] = updated
	if err := s.persistLocked(s.snapshotLocked()); err != nil {
		s.proposals[idx] = before // rollback
		return ProposalRecord{}, err
	}
	if before.CommandID == "" && updated.CommandID != "" {
		s.byCommand[updated.CommandID] = idx
	}
	return copyProposalRecord(updated), nil
}

// Get returns a copy of the record by proposal ID.
func (s *ProposalStore) Get(id ProposalID) (ProposalRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.proposals {
		if rec.ID == string(id) {
			return copyProposalRecord(rec), true
		}
	}
	return ProposalRecord{}, false
}

// GetByCommand returns the proposal record for a command id, when an
// ACCEPTED proposal references it (one command reference per proposal —
// the only match). The composed command view's additive proposal field
// rides this lookup.
func (s *ProposalStore) GetByCommand(commandID CommandID) (ProposalRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byCommand[string(commandID)]
	if !ok {
		return ProposalRecord{}, false
	}
	return copyProposalRecord(s.proposals[idx]), true
}

// ListLast returns the last n proposals newest-first (capture order
// reversed — deterministic). n < 1 is clamped to 1.
func (s *ProposalStore) ListLast(n int) []ProposalRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 1 {
		n = 1
	}
	if n > len(s.proposals) {
		n = len(s.proposals)
	}
	out := make([]ProposalRecord, 0, n)
	for i := len(s.proposals) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, copyProposalRecord(s.proposals[i]))
	}
	return out
}

// ListLastState returns the last n proposals of ONE state, newest-first.
// An unknown state is a caller bug — it yields an empty slice (the API
// layer validates the filter before calling).
func (s *ProposalStore) ListLastState(n int, state ProposalState) []ProposalRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 1 {
		n = 1
	}
	out := make([]ProposalRecord, 0)
	for i := len(s.proposals) - 1; i >= 0 && len(out) < n; i-- {
		if s.proposals[i].State == state {
			out = append(out, copyProposalRecord(s.proposals[i]))
		}
	}
	return out
}

// Counts returns the number of proposals per state (all states always
// present in the map — the read surface's counts contract).
func (s *ProposalStore) Counts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{
		string(ProposalPending):   0,
		string(ProposalAccepted):  0,
		string(ProposalDismissed): 0,
	}
	for _, rec := range s.proposals {
		if _, known := counts[string(rec.State)]; known {
			counts[string(rec.State)]++
		}
	}
	return counts
}

// Count returns the total number of proposals (all states).
func (s *ProposalStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.proposals)
}

// Path returns the store's file path (surfaced by the API for operators).
func (s *ProposalStore) Path() string {
	return s.path
}
