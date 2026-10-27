// BF-3 (v19.5.2): the persisted execution store — executions.json, the
// commands.json/workspaces.json/grants.json discipline, exactly:
//
//   - JSON file with an explicit schema version (1); Load rejects
//     anything else — a future schema change is an explicit migration.
//   - 0600 file / 0700 directory — checked at load AND before every
//     persist; a widened mode is tampering until an operator fixes it.
//   - Atomic persistence: temp file in the same directory (0600 by
//     construction, fsync) + rename; a failed persist rolls back the
//     in-memory append (state and file stay in sync).
//   - Executions are never deleted — the file is the reviewable history
//     of every executed (and skipped, and failed) intent. Rotating the
//     file is an operator act.
//   - Bounded: DefaultMaxExecutions records total; at capacity Record
//     fails with ErrExecutionLimit (the HTTP layer reports 503).
//   - ONE EXECUTION PER COMMAND, enforced twice: Record refuses a second
//     execution for a command id (ErrAlreadyExecuted), and Load aborts on
//     a file that carries two executions for the same command — a
//     hand-edited duplicate is corruption, not "some executions work".
//   - Execution IDs are "afx-" + 16 crypto-random bytes hex; RNG failure
//     aborts the record (fail-closed).
//   - The runtime mapping invariant is checked at Load (a drifted
//     kernel/mapping pair aborts startup — the fabric never runs with a
//     partial mapping).
//
// The store does NOT talk to the Agent Kernel and does NOT run acts: the
// API layer composes the fresh evaluation, the act and the record
// (separate concerns, composed at the boundary — the P4 ownership table).
package agentfabric

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Sentinel errors for the execution store.
var (
	// ErrExecutionRNG: execution-ID generation failed — fail-closed.
	ErrExecutionRNG = errors.New("agentfabric: RNG unavailable (executions)")
	// ErrUnknownExecution: no execution with the requested ID.
	ErrUnknownExecution = errors.New("agentfabric: unknown execution")
	// ErrAlreadyExecuted: the command already carries a terminal
	// execution — one execution per command, ever.
	ErrAlreadyExecuted = errors.New("agentfabric: command already executed (one execution per command — the record is terminal)")
	// ErrExecutionLimit: the store is at capacity (bounded history).
	ErrExecutionLimit = errors.New("agentfabric: execution store at capacity")
	// ErrExecutionFilePermissions: the executions file (or directory) is
	// looser than 0600/0700 — fail-closed.
	ErrExecutionFilePermissions = errors.New("agentfabric: executions file permissions too loose")
)

// DefaultMaxExecutions bounds the total number of execution records (ALL
// states). Same magnitude as commands: executions are the user-act
// history. At the bound, execution stops (503) until the operator
// archives the file deliberately.
const DefaultMaxExecutions = 512

// executionsFileVersion is the on-disk schema version. Load rejects
// anything else — a future schema change must be an explicit migration,
// not a guess.
const executionsFileVersion = 1

// executionsFile is the on-disk representation. Executions are stored in
// append order — ListLast reverses for newest-first.
type executionsFile struct {
	Version    int               `json:"version"`
	Executions []ExecutionRecord `json:"executions"`
}

// ExecutionStore is the persisted, bounded, mutex-guarded execution store.
type ExecutionStore struct {
	mu            sync.Mutex
	path          string
	maxExecutions int
	executions    []ExecutionRecord
	byCommand     map[string]int // command id → index into executions
}

// NewExecutionStore constructs an empty in-memory store bound to a file
// path. It does NOT touch the file — call Load to read (or initialize)
// it. Construction is fail-closed: an empty path or non-positive bound is
// a construction error.
func NewExecutionStore(path string, maxExecutions int) (*ExecutionStore, error) {
	if path == "" {
		return nil, fmt.Errorf("agentfabric: executions file path is required")
	}
	if maxExecutions <= 0 {
		return nil, fmt.Errorf("agentfabric: maxExecutions must be > 0 (got %d)", maxExecutions)
	}
	return &ExecutionStore{
		path:          path,
		maxExecutions: maxExecutions,
		executions:    make([]ExecutionRecord, 0),
		byCommand:     make(map[string]int),
	}, nil
}

// rngExecutionID generates "afx-" + 16 crypto-random bytes hex.
// Package-level variable so tests can inject RNG failure (the cmd-/ws-
// fail-closed precedent).
var rngExecutionID = func() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", ErrExecutionRNG
	}
	return executionIDPrefix + hex.EncodeToString(b), nil
}

// Load reads and validates the executions file, or initializes it empty
// on first run. Validation is fail-closed: ANY malformed record (ID
// shapes, closed enumerations, runtime↔capability coherence, state↔
// allow↔fields coherence), ANY duplicate execution ID, ANY duplicate
// command reference, ANY count above the bound, a wrong schema version,
// a drifted runtime mapping, or looser-than-0600/0700 permissions abort
// the load. A corrupt or tampered executions file must never degrade
// into "some executions work".
func (s *ExecutionStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ValidateRuntimeMapping(); err != nil {
		return fmt.Errorf("agentfabric: execution store refuses to load: %v", err)
	}

	info, err := os.Stat(s.path)
	switch {
	case err == nil:
		// exists — permissions checked below
	case errors.Is(err, os.ErrNotExist):
		// First run: initialize an empty, valid, 0600 file.
		if err := s.ensureDirLocked(); err != nil {
			return err
		}
		empty := executionsFile{Version: executionsFileVersion, Executions: []ExecutionRecord{}}
		if err := s.persistLocked(empty); err != nil {
			return fmt.Errorf("agentfabric: cannot initialize executions file %s: %w", s.path, err)
		}
		s.executions = make([]ExecutionRecord, 0)
		s.byCommand = make(map[string]int)
		return nil
	default:
		return fmt.Errorf("agentfabric: cannot stat executions file %s: %w", s.path, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: %s has mode %o (want 0600)", ErrExecutionFilePermissions, s.path, info.Mode().Perm())
	}
	if err := s.checkDirPermsLocked(); err != nil {
		return err
	}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("agentfabric: cannot read executions file %s: %w", s.path, err)
	}
	var f executionsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("agentfabric: executions file %s is not valid JSON: %w", s.path, err)
	}
	if f.Version != executionsFileVersion {
		return fmt.Errorf("agentfabric: executions file %s has schema version %d (want %d) — explicit migration required", s.path, f.Version, executionsFileVersion)
	}
	if len(f.Executions) > s.maxExecutions {
		return fmt.Errorf("agentfabric: executions file %s carries %d records (bound %d) — archive and rotate the file deliberately", s.path, len(f.Executions), s.maxExecutions)
	}
	seenIDs := make(map[string]bool, len(f.Executions))
	byCommand := make(map[string]int, len(f.Executions))
	for i, rec := range f.Executions {
		if err := rec.validate(); err != nil {
			return fmt.Errorf("agentfabric: executions file %s record %d: %w", s.path, i, err)
		}
		if seenIDs[rec.ID] {
			return fmt.Errorf("agentfabric: executions file %s record %d: duplicate execution id %s", s.path, i, rec.ID)
		}
		seenIDs[rec.ID] = true
		if _, dup := byCommand[rec.CommandID]; dup {
			return fmt.Errorf("agentfabric: executions file %s record %d: command %s is executed TWICE — one execution per command, a duplicate is corruption (fail-closed)", s.path, i, rec.CommandID)
		}
		byCommand[rec.CommandID] = i
	}
	// Copy in (defensive: never alias the decoded slice).
	s.executions = make([]ExecutionRecord, len(f.Executions))
	copy(s.executions, f.Executions)
	s.byCommand = byCommand
	return nil
}

// ensureDirLocked creates the parent directory 0700 if missing.
func (s *ExecutionStore) ensureDirLocked() error {
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
func (s *ExecutionStore) checkDirPermsLocked() error {
	dir := filepath.Dir(s.path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("agentfabric: cannot stat directory %s: %w", dir, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: directory %s has mode %o (want 0700)", ErrExecutionFilePermissions, dir, info.Mode().Perm())
	}
	return nil
}

// persistLocked writes the file atomically: temp file in the same
// directory (0600 by construction, fsync) + rename. On any error the
// caller is expected to roll back the in-memory state.
func (s *ExecutionStore) persistLocked(f executionsFile) error {
	if err := s.ensureDirLocked(); err != nil {
		return err
	}
	if err := s.checkDirPermsLocked(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("agentfabric: cannot encode executions file: %w", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".executions-*.tmp")
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
func (s *ExecutionStore) snapshotLocked() executionsFile {
	f := executionsFile{Version: executionsFileVersion, Executions: make([]ExecutionRecord, len(s.executions))}
	copy(f.Executions, s.executions)
	return f
}

// Record appends one TERMINAL execution record and persists it. The
// record is validated (the store never holds a record it would refuse to
// load); a second execution for the same command is refused
// (ErrAlreadyExecuted — one execution per command, ever); RNG failure
// aborts (never a predictable ID); at capacity ErrExecutionLimit. On
// persist failure the in-memory append is rolled back (state and file
// stay in sync).
func (s *ExecutionStore) Record(rec ExecutionRecord) (ExecutionRecord, error) {
	if err := ExecutionID(rec.ID).Validate(); err != nil {
		return ExecutionRecord{}, err
	}
	if err := rec.validate(); err != nil {
		return ExecutionRecord{}, err
	}
	if rec.ExecutedAt.IsZero() {
		return ExecutionRecord{}, fmt.Errorf("agentfabric: execution time is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, already := s.byCommand[rec.CommandID]; already {
		return ExecutionRecord{}, ErrAlreadyExecuted
	}
	if len(s.executions) >= s.maxExecutions {
		return ExecutionRecord{}, ErrExecutionLimit
	}
	// Collision against an existing ID: cryptographically impossible, but
	// the check is free and the invariant (unique IDs) is load-enforced.
	for _, existing := range s.executions {
		if existing.ID == rec.ID {
			return ExecutionRecord{}, fmt.Errorf("agentfabric: execution id collision (rng reuse) — record aborted")
		}
	}
	s.executions = append(s.executions, rec)
	if err := s.persistLocked(s.snapshotLocked()); err != nil {
		s.executions = s.executions[:len(s.executions)-1] // rollback
		return ExecutionRecord{}, err
	}
	s.byCommand[rec.CommandID] = len(s.executions) - 1
	return copyExecutionRecord(rec), nil
}

// NewExecutionID generates a fresh execution ID (exposed for the API
// layer's Record composition — fail-closed RNG).
func NewExecutionID() (string, error) {
	return rngExecutionID()
}

// Get returns a copy of the record by execution ID.
func (s *ExecutionStore) Get(id ExecutionID) (ExecutionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.executions {
		if rec.ID == string(id) {
			return copyExecutionRecord(rec), true
		}
	}
	return ExecutionRecord{}, false
}

// GetByCommand returns the execution record for a command id, when the
// command has been executed (one execution per command — the only match).
func (s *ExecutionStore) GetByCommand(commandID CommandID) (ExecutionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byCommand[string(commandID)]
	if !ok {
		return ExecutionRecord{}, false
	}
	return copyExecutionRecord(s.executions[idx]), true
}

// ListLast returns the last n executions newest-first (append order
// reversed — deterministic). n < 1 is clamped to 1.
func (s *ExecutionStore) ListLast(n int) []ExecutionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 1 {
		n = 1
	}
	if n > len(s.executions) {
		n = len(s.executions)
	}
	out := make([]ExecutionRecord, 0, n)
	for i := len(s.executions) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, copyExecutionRecord(s.executions[i]))
	}
	return out
}

// ListLastState returns the last n executions of ONE state, newest-first.
// An unknown state is a caller bug — it yields an empty slice (the API
// layer validates the filter before calling).
func (s *ExecutionStore) ListLastState(n int, state ExecutionState) []ExecutionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 1 {
		n = 1
	}
	out := make([]ExecutionRecord, 0)
	for i := len(s.executions) - 1; i >= 0 && len(out) < n; i-- {
		if s.executions[i].State == state {
			out = append(out, copyExecutionRecord(s.executions[i]))
		}
	}
	return out
}

// Counts returns the number of executions per state (all states always
// present in the map — the read surface's counts contract).
func (s *ExecutionStore) Counts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{
		string(ExecutionCompleted): 0,
		string(ExecutionFailed):    0,
		string(ExecutionSkipped):   0,
	}
	for _, rec := range s.executions {
		if _, known := counts[string(rec.State)]; known {
			counts[string(rec.State)]++
		}
	}
	return counts
}

// Count returns the total number of executions (all states).
func (s *ExecutionStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.executions)
}

// Path returns the store's file path (surfaced by the API for operators).
func (s *ExecutionStore) Path() string {
	return s.path
}
