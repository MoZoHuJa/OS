// BF-4 (v19.5.3): the persisted approval store — approvals.json, the
// commands.json/workspaces.json/executions.json discipline, exactly:
//
//   - JSON file with an explicit schema version (1); Load rejects
//     anything else — a future schema change is an explicit migration.
//   - 0600 file / 0700 directory — checked at load AND before every
//     persist; a widened mode is tampering until an operator fixes it.
//   - Atomic persistence: temp file in the same directory (0600 by
//     construction, fsync) + rename; a failed persist rolls back the
//     in-memory mutation (state and file stay in sync).
//   - Approvals are never deleted — the file is the reviewable history
//     of every request, approval and rejection (there is deliberately no
//     withdrawal: a PENDING request stays answerable until decided).
//     Rotating the file is an operator act.
//   - Bounded: DefaultMaxApprovals records total; at capacity Request
//     fails with ErrApprovalLimit (the HTTP layer reports 503).
//   - ONE APPROVAL REQUEST PER COMMAND, enforced twice: Request refuses
//     a second request for a command id (ErrAlreadyRequested), and Load
//     aborts on a file that carries two approvals for the same command —
//     a hand-edited duplicate is corruption, not "some approvals work".
//   - Approval IDs are "apv-" + 16 crypto-random bytes hex; RNG failure
//     aborts the record (fail-closed).
//   - The state machine is PENDING-only-in-flight: Approve/Reject
//     operate exclusively on PENDING records — a terminal approval is
//     never re-decided (the file is the history).
//
// The store does NOT talk to the Agent Kernel and does NOT run acts: the
// API layer composes the fresh evaluation, the act and the records
// (separate concerns, composed at the boundary — the P4 ownership table).
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

// Sentinel errors for the approval store.
var (
	// ErrApprovalRNG: approval-ID generation failed — fail-closed.
	ErrApprovalRNG = errors.New("agentfabric: RNG unavailable (approvals)")
	// ErrUnknownApproval: no approval with the requested ID.
	ErrUnknownApproval = errors.New("agentfabric: unknown approval")
	// ErrAlreadyRequested: the command already carries an approval
	// request — one request per command, ever.
	ErrAlreadyRequested = errors.New("agentfabric: command already has an approval request (one request per command — the decision is terminal)")
	// ErrApprovalLimit: the store is at capacity (bounded history).
	ErrApprovalLimit = errors.New("agentfabric: approval store at capacity")
	// ErrApprovalFilePermissions: the approvals file (or directory) is
	// looser than 0600/0700 — fail-closed.
	ErrApprovalFilePermissions = errors.New("agentfabric: approvals file permissions too loose")
	// ErrApprovalState: the record is not PENDING — terminal decisions
	// are never re-decided.
	ErrApprovalState = errors.New("agentfabric: approval is not PENDING — the decision is terminal")
)

// DefaultMaxApprovals bounds the total number of approval records (ALL
// states). Approvals are sparser than executions (coding acts only); the
// bound keeps the file honest. At the bound, requesting stops (503) until
// the operator archives the file deliberately.
const DefaultMaxApprovals = 256

// approvalsFileVersion is the on-disk schema version. Load rejects
// anything else — a future schema change must be an explicit migration,
// not a guess.
const approvalsFileVersion = 1

// approvalsFile is the on-disk representation. Approvals are stored in
// append order — ListLast reverses for newest-first.
type approvalsFile struct {
	Version   int              `json:"version"`
	Approvals []ApprovalRecord `json:"approvals"`
}

// ApprovalStore is the persisted, bounded, mutex-guarded approval store.
type ApprovalStore struct {
	mu           sync.Mutex
	path         string
	maxApprovals int
	approvals    []ApprovalRecord
	byCommand    map[string]int // command id → index into approvals
}

// NewApprovalStore constructs an empty in-memory store bound to a file
// path. It does NOT touch the file — call Load to read (or initialize)
// it. Construction is fail-closed: an empty path or non-positive bound is
// a construction error.
func NewApprovalStore(path string, maxApprovals int) (*ApprovalStore, error) {
	if path == "" {
		return nil, fmt.Errorf("agentfabric: approvals file path is required")
	}
	if maxApprovals <= 0 {
		return nil, fmt.Errorf("agentfabric: maxApprovals must be > 0 (got %d)", maxApprovals)
	}
	return &ApprovalStore{
		path:         path,
		maxApprovals: maxApprovals,
		approvals:    make([]ApprovalRecord, 0),
		byCommand:    make(map[string]int),
	}, nil
}

// Load reads and validates the approvals file, or initializes it empty
// on first run. Validation is fail-closed: ANY malformed record (ID
// shapes, closed enumerations, runtime↔capability coherence, params↔
// capability coherence, state↔fields coherence), ANY duplicate approval
// ID, ANY duplicate command reference, ANY count above the bound, a wrong
// schema version, or looser-than-0600/0700 permissions aborts the load.
// A corrupt or tampered approvals file must never degrade into "some
// approvals work".
func (s *ApprovalStore) Load() error {
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
		empty := approvalsFile{Version: approvalsFileVersion, Approvals: []ApprovalRecord{}}
		if err := s.persistLocked(empty); err != nil {
			return fmt.Errorf("agentfabric: cannot initialize approvals file %s: %w", s.path, err)
		}
		s.approvals = make([]ApprovalRecord, 0)
		s.byCommand = make(map[string]int)
		return nil
	default:
		return fmt.Errorf("agentfabric: cannot stat approvals file %s: %w", s.path, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: %s has mode %o (want 0600)", ErrApprovalFilePermissions, s.path, info.Mode().Perm())
	}
	if err := s.checkDirPermsLocked(); err != nil {
		return err
	}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("agentfabric: cannot read approvals file %s: %w", s.path, err)
	}
	var f approvalsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("agentfabric: approvals file %s is not valid JSON: %w", s.path, err)
	}
	if f.Version != approvalsFileVersion {
		return fmt.Errorf("agentfabric: approvals file %s has schema version %d (want %d) — explicit migration required", s.path, f.Version, approvalsFileVersion)
	}
	if len(f.Approvals) > s.maxApprovals {
		return fmt.Errorf("agentfabric: approvals file %s carries %d records (bound %d) — archive and rotate the file deliberately", s.path, len(f.Approvals), s.maxApprovals)
	}
	seenIDs := make(map[string]bool, len(f.Approvals))
	byCommand := make(map[string]int, len(f.Approvals))
	for i, rec := range f.Approvals {
		if err := rec.validate(); err != nil {
			return fmt.Errorf("agentfabric: approvals file %s record %d: %w", s.path, i, err)
		}
		if seenIDs[rec.ID] {
			return fmt.Errorf("agentfabric: approvals file %s record %d: duplicate approval id %s", s.path, i, rec.ID)
		}
		seenIDs[rec.ID] = true
		if _, dup := byCommand[rec.CommandID]; dup {
			return fmt.Errorf("agentfabric: approvals file %s record %d: command %s is requested TWICE — one approval per command, a duplicate is corruption (fail-closed)", s.path, i, rec.CommandID)
		}
		byCommand[rec.CommandID] = i
	}
	// Copy in (defensive: never alias the decoded slice).
	s.approvals = make([]ApprovalRecord, len(f.Approvals))
	copy(s.approvals, f.Approvals)
	s.byCommand = byCommand
	return nil
}

// ensureDirLocked creates the parent directory 0700 if missing.
func (s *ApprovalStore) ensureDirLocked() error {
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
func (s *ApprovalStore) checkDirPermsLocked() error {
	dir := filepath.Dir(s.path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("agentfabric: cannot stat directory %s: %w", dir, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: directory %s has mode %o (want 0700)", ErrApprovalFilePermissions, dir, info.Mode().Perm())
	}
	return nil
}

// persistLocked writes the file atomically: temp file in the same
// directory (0600 by construction, fsync) + rename. On any error the
// caller is expected to roll back the in-memory state.
func (s *ApprovalStore) persistLocked(f approvalsFile) error {
	if err := s.ensureDirLocked(); err != nil {
		return err
	}
	if err := s.checkDirPermsLocked(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("agentfabric: cannot encode approvals file: %w", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".approvals-*.tmp")
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
func (s *ApprovalStore) snapshotLocked() approvalsFile {
	f := approvalsFile{Version: approvalsFileVersion, Approvals: make([]ApprovalRecord, len(s.approvals))}
	copy(f.Approvals, s.approvals)
	return f
}

// Request appends one PENDING approval record and persists it. The
// record is validated (the store never holds a record it would refuse to
// load); a second request for the same command is refused
// (ErrAlreadyRequested — one request per command, ever); at capacity
// ErrApprovalLimit. On persist failure the in-memory append is rolled
// back (state and file stay in sync).
func (s *ApprovalStore) Request(rec ApprovalRecord) (ApprovalRecord, error) {
	if err := ApprovalID(rec.ID).Validate(); err != nil {
		return ApprovalRecord{}, err
	}
	if err := rec.validate(); err != nil {
		return ApprovalRecord{}, err
	}
	if rec.RequestedAt.IsZero() {
		return ApprovalRecord{}, fmt.Errorf("agentfabric: request time is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, already := s.byCommand[rec.CommandID]; already {
		return ApprovalRecord{}, ErrAlreadyRequested
	}
	if len(s.approvals) >= s.maxApprovals {
		return ApprovalRecord{}, ErrApprovalLimit
	}
	// Collision against an existing ID: cryptographically impossible, but
	// the check is free and the invariant (unique IDs) is load-enforced.
	for _, existing := range s.approvals {
		if existing.ID == rec.ID {
			return ApprovalRecord{}, fmt.Errorf("agentfabric: approval id collision (rng reuse) — request aborted")
		}
	}
	s.approvals = append(s.approvals, rec)
	if err := s.persistLocked(s.snapshotLocked()); err != nil {
		s.approvals = s.approvals[:len(s.approvals)-1] // rollback
		return ApprovalRecord{}, err
	}
	s.byCommand[rec.CommandID] = len(s.approvals) - 1
	return copyApprovalRecord(rec), nil
}

// Approve closes a PENDING request as APPROVED with the executed act's
// terminal execution reference (the act ran — even an audited SKIPPED of
// a fresh kernel denial is an execution record; APPROVED + SKIPPED is the
// honest composition). A terminal approval is never re-decided.
func (s *ApprovalStore) Approve(id ApprovalID, approver string, executionID string, decidedAt time.Time) (ApprovalRecord, error) {
	if err := ValidatePrincipalLabel(approver); err != nil {
		return ApprovalRecord{}, fmt.Errorf("agentfabric: approver: %v", err)
	}
	if err := ExecutionID(executionID).Validate(); err != nil {
		return ApprovalRecord{}, err
	}
	if decidedAt.IsZero() {
		return ApprovalRecord{}, fmt.Errorf("agentfabric: decision time is required")
	}
	return s.decide(id, func(rec ApprovalRecord) ApprovalRecord {
		rec.State = ApprovalApproved
		rec.Approver = approver
		rec.ExecutionID = executionID
		rec.Reason = ""
		rec.DecidedAt = &decidedAt
		return rec
	})
}

// Reject closes a PENDING request as REJECTED (terminal, no act ever
// ran). The operator's no is unconditional — no kernel evaluation gates
// a rejection. An optional bounded reason carries the why.
func (s *ApprovalStore) Reject(id ApprovalID, approver string, reason string, decidedAt time.Time) (ApprovalRecord, error) {
	if err := ValidatePrincipalLabel(approver); err != nil {
		return ApprovalRecord{}, fmt.Errorf("agentfabric: approver: %v", err)
	}
	if reason != "" {
		if err := ValidateExecutionReason(reason); err != nil {
			return ApprovalRecord{}, err
		}
	}
	if decidedAt.IsZero() {
		return ApprovalRecord{}, fmt.Errorf("agentfabric: decision time is required")
	}
	return s.decide(id, func(rec ApprovalRecord) ApprovalRecord {
		rec.State = ApprovalRejected
		rec.Approver = approver
		if reason != "" {
			rec.Reason = reason
		}
		rec.ExecutionID = ""
		rec.DecidedAt = &decidedAt
		return rec
	})
}

// decide applies fn to the PENDING record with the given id and persists
// the terminal decision (rollback on persist failure). Terminal records
// are never re-decided (ErrApprovalState); unknown ids fail closed.
func (s *ApprovalStore) decide(id ApprovalID, fn func(ApprovalRecord) ApprovalRecord) (ApprovalRecord, error) {
	if err := id.Validate(); err != nil {
		return ApprovalRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, rec := range s.approvals {
		if rec.ID == string(id) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ApprovalRecord{}, ErrUnknownApproval
	}
	if s.approvals[idx].State != ApprovalPending {
		return ApprovalRecord{}, fmt.Errorf("%w: %s is %s", ErrApprovalState, id, s.approvals[idx].State)
	}
	updated := fn(copyApprovalRecord(s.approvals[idx]))
	if err := updated.validate(); err != nil {
		return ApprovalRecord{}, err
	}
	before := s.approvals[idx]
	s.approvals[idx] = updated
	if err := s.persistLocked(s.snapshotLocked()); err != nil {
		s.approvals[idx] = before // rollback
		return ApprovalRecord{}, err
	}
	return copyApprovalRecord(updated), nil
}

// Get returns a copy of the record by approval ID.
func (s *ApprovalStore) Get(id ApprovalID) (ApprovalRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.approvals {
		if rec.ID == string(id) {
			return copyApprovalRecord(rec), true
		}
	}
	return ApprovalRecord{}, false
}

// GetByCommand returns the approval record for a command id, when the
// command has a request (one approval per command — the only match).
func (s *ApprovalStore) GetByCommand(commandID CommandID) (ApprovalRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byCommand[string(commandID)]
	if !ok {
		return ApprovalRecord{}, false
	}
	return copyApprovalRecord(s.approvals[idx]), true
}

// ListLast returns the last n approvals newest-first (append order
// reversed — deterministic). n < 1 is clamped to 1.
func (s *ApprovalStore) ListLast(n int) []ApprovalRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 1 {
		n = 1
	}
	if n > len(s.approvals) {
		n = len(s.approvals)
	}
	out := make([]ApprovalRecord, 0, n)
	for i := len(s.approvals) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, copyApprovalRecord(s.approvals[i]))
	}
	return out
}

// ListLastState returns the last n approvals of ONE state, newest-first.
// An unknown state is a caller bug — it yields an empty slice (the API
// layer validates the filter before calling).
func (s *ApprovalStore) ListLastState(n int, state ApprovalState) []ApprovalRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 1 {
		n = 1
	}
	out := make([]ApprovalRecord, 0)
	for i := len(s.approvals) - 1; i >= 0 && len(out) < n; i-- {
		if s.approvals[i].State == state {
			out = append(out, copyApprovalRecord(s.approvals[i]))
		}
	}
	return out
}

// Counts returns the number of approvals per state (all states always
// present in the map — the read surface's counts contract).
func (s *ApprovalStore) Counts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{
		string(ApprovalPending):  0,
		string(ApprovalApproved): 0,
		string(ApprovalRejected): 0,
	}
	for _, rec := range s.approvals {
		if _, known := counts[string(rec.State)]; known {
			counts[string(rec.State)]++
		}
	}
	return counts
}

// Count returns the total number of approvals (all states).
func (s *ApprovalStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.approvals)
}

// Path returns the store's file path (surfaced by the API for operators).
func (s *ApprovalStore) Path() string {
	return s.path
}
