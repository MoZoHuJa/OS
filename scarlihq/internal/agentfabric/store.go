// BF-1 (v19.5.0): the persisted command store — the Agent Fabric's durable
// half. Store discipline copied from the AK-3 grant registry (grants.go):
//
//   - JSON file (default /var/lib/scarlix-agentfabric/commands.json) with
//     an explicit schema version; Load rejects anything else.
//   - The file and its directory are 0600/0700 — checked at load, never
//     silently widened (a widened mode is tampering until an operator
//     fixes it deliberately).
//   - Writes go to a temp file in the same directory (0600 by
//     construction, fsync) followed by rename — atomic on POSIX. A failed
//     persist rolls back the in-memory state: state and file stay in sync.
//   - Commands are never silently deleted — the file is the reviewable
//     history of every captured intent and its outcome (cancellations
//     included). Rotating the file is an operator act, like audit rotation.
//   - Bounded: DefaultMaxCommands records total. At capacity Submit fails
//     with ErrCommandLimit (the HTTP layer reports 503) — the operator
//     archives the file deliberately.
//   - Command IDs are "cmd-" + 16 crypto-random bytes hex; RNG failure
//     aborts creation (fail-closed — never a predictable ID).
//
// The store does NOT know about the Agent Kernel: routing decisions and
// kernel evaluation are composed by the API layer (the fabric owns command
// lifecycle; the kernel owns permission evaluation — separate concerns,
// composed at the boundary, per the P4 ownership table).
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
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

// Sentinel errors for the command store.
var (
	// ErrRNG: command-ID generation failed — fail-closed, never predictable.
	ErrRNG = errors.New("agentfabric: RNG unavailable")
	// ErrUnknownCommand: no command with the requested ID.
	ErrUnknownCommand = errors.New("agentfabric: unknown command")
	// ErrCommandState: the transition is illegal for the command's state
	// (terminal states, illegal moves, backwards clocks).
	ErrCommandState = errors.New("agentfabric: illegal command state transition")
	// ErrCommandLimit: the store is at capacity (bounded history).
	ErrCommandLimit = errors.New("agentfabric: command store at capacity")
	// ErrCommandFilePermissions: the commands file (or directory) is looser
	// than 0600/0700 — fail-closed, the operator must fix it.
	ErrCommandFilePermissions = errors.New("agentfabric: commands file permissions too loose")
	// ErrEmptyDecisionReason: a DECIDED record must name the kernel rule.
	ErrEmptyDecisionReason = errors.New("agentfabric: decision reason is required (kernel decisions name their rule)")
)

// DefaultMaxCommands bounds the total number of command records (ALL
// states — the history is real state, not free). Intents are cheap but
// not free: when the bound is reached, submission stops (503) until the
// operator archives the file. Deliberately larger than the grants bound:
// commands are user acts, grants are admin acts.
const DefaultMaxCommands = 512

// commandsFileVersion is the on-disk schema version. Load rejects anything
// else — a future schema change must be an explicit migration, not a guess.
const commandsFileVersion = 1

// commandsFile is the on-disk representation. Commands are stored in
// submission order (append order) — ListLast reverses for newest-first.
type commandsFile struct {
	Version  int             `json:"version"`
	Commands []CommandRecord `json:"commands"`
}

// CommandStore is the persisted, bounded, mutex-guarded command store.
type CommandStore struct {
	mu          sync.Mutex
	path        string
	maxCommands int
	commands    []CommandRecord
}

// NewCommandStore constructs an empty in-memory store bound to a file path.
// It does NOT touch the file — call Load to read (or initialize) it.
// Construction is fail-closed: an empty path or non-positive bound is a
// construction error.
func NewCommandStore(path string, maxCommands int) (*CommandStore, error) {
	if path == "" {
		return nil, fmt.Errorf("agentfabric: commands file path is required")
	}
	if maxCommands <= 0 {
		return nil, fmt.Errorf("agentfabric: maxCommands must be > 0 (got %d)", maxCommands)
	}
	return &CommandStore{
		path:        path,
		maxCommands: maxCommands,
		commands:    make([]CommandRecord, 0),
	}, nil
}

// rngCommandID generates "cmd-" + 16 crypto-random bytes hex. Package-level
// variable so tests can inject RNG failure and assert the fail-closed path
// (the kernel store's rngSessionID precedent).
var rngCommandID = func() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", ErrRNG
	}
	return commandIDPrefix + hex.EncodeToString(b), nil
}

// Load reads and validates the commands file, or initializes it empty on
// first run. Validation is fail-closed: ANY malformed record (ID shape,
// intent bounds, agent shape, closed-enumeration capability, state/field
// invariants, monotone timestamps), ANY duplicate ID, ANY record count
// above the bound, a wrong schema version, or looser-than-0600/0700
// permissions abort the load. A corrupt or tampered commands file must
// never degrade into "some commands work".
func (s *CommandStore) Load() error {
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
		empty := commandsFile{Version: commandsFileVersion, Commands: []CommandRecord{}}
		if err := s.persistLocked(empty); err != nil {
			return fmt.Errorf("agentfabric: cannot initialize commands file %s: %w", s.path, err)
		}
		s.commands = make([]CommandRecord, 0)
		return nil
	default:
		return fmt.Errorf("agentfabric: cannot stat commands file %s: %w", s.path, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: %s has mode %o (want 0600)", ErrCommandFilePermissions, s.path, info.Mode().Perm())
	}
	if err := s.checkDirPermsLocked(); err != nil {
		return err
	}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("agentfabric: cannot read commands file %s: %w", s.path, err)
	}
	var f commandsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("agentfabric: commands file %s is not valid JSON: %w", s.path, err)
	}
	if f.Version != commandsFileVersion {
		return fmt.Errorf("agentfabric: commands file %s has schema version %d (want %d) — explicit migration required", s.path, f.Version, commandsFileVersion)
	}
	if len(f.Commands) > s.maxCommands {
		return fmt.Errorf("agentfabric: commands file %s carries %d records (bound %d) — archive and rotate the file deliberately", s.path, len(f.Commands), s.maxCommands)
	}
	seen := make(map[string]bool, len(f.Commands))
	for i, c := range f.Commands {
		if err := c.validate(); err != nil {
			return fmt.Errorf("agentfabric: commands file %s record %d: %w", s.path, i, err)
		}
		if seen[c.ID] {
			return fmt.Errorf("agentfabric: commands file %s record %d: duplicate command id %s", s.path, i, c.ID)
		}
		seen[c.ID] = true
	}
	// Copy in (defensive: never alias the decoded slice).
	s.commands = make([]CommandRecord, len(f.Commands))
	copy(s.commands, f.Commands)
	return nil
}

// ensureDirLocked creates the parent directory 0700 if missing.
func (s *CommandStore) ensureDirLocked() error {
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
func (s *CommandStore) checkDirPermsLocked() error {
	dir := filepath.Dir(s.path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("agentfabric: cannot stat directory %s: %w", dir, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: directory %s has mode %o (want 0700)", ErrCommandFilePermissions, dir, info.Mode().Perm())
	}
	return nil
}

// persistLocked writes the file atomically: temp file in the same
// directory (0600 by construction, fsync) + rename. On any error the
// caller is expected to roll back the in-memory state (the grants.go
// discipline: state and file stay in sync).
func (s *CommandStore) persistLocked(f commandsFile) error {
	if err := s.ensureDirLocked(); err != nil {
		return err
	}
	if err := s.checkDirPermsLocked(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("agentfabric: cannot encode commands file: %w", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".commands-*.tmp")
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
func (s *CommandStore) snapshotLocked() commandsFile {
	f := commandsFile{Version: commandsFileVersion, Commands: make([]CommandRecord, len(s.commands))}
	copy(f.Commands, s.commands)
	return f
}

// mutate runs fn under the lock against a private copy of the record,
// then persists the new state. On persist failure the in-memory state is
// rolled back (the mutation never happened) — state and file stay in sync.
func (s *CommandStore) mutate(id CommandID, fn func(rec *CommandRecord) error) (CommandRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.commands {
		if s.commands[i].ID == string(id) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return CommandRecord{}, ErrUnknownCommand
	}
	before := s.commands[idx]
	candidate := before // value copy; pointer fields are re-created by fn, never mutated through
	if err := fn(&candidate); err != nil {
		// Transition errors (illegal move, terminal state, backwards clock)
		// are state conflicts — wrapped so the HTTP layer can map them to 409
		// (the kernel transition endpoint's discipline).
		return CommandRecord{}, fmt.Errorf("%w: %v", ErrCommandState, err)
	}
	if err := candidate.validate(); err != nil {
		return CommandRecord{}, fmt.Errorf("%w: %v", ErrCommandState, err)
	}
	// Persist first (with the mutation applied), then commit in-memory.
	saved := s.commands[idx]
	s.commands[idx] = candidate
	if err := s.persistLocked(s.snapshotLocked()); err != nil {
		s.commands[idx] = saved // rollback — state and file stay in sync
		return CommandRecord{}, err
	}
	return copyCommandRecord(candidate), nil
}

// Submit captures a new intent as a SUBMITTED command and persists it.
// The intent, agent ID and capability are re-validated here (defense in
// depth — the HTTP layer validates early for clean 400 messages; the
// store is the authority). Fail-closed: RNG failure aborts submission
// (never a predictable ID); at capacity ErrCommandLimit.
func (s *CommandStore) Submit(intent string, agent agentkernel.AgentID, capability agentkernel.Capability, now time.Time) (CommandRecord, error) {
	if err := ValidateIntent(intent); err != nil {
		return CommandRecord{}, err
	}
	if err := agent.Validate(); err != nil {
		return CommandRecord{}, err
	}
	if !capability.IsValid() {
		return CommandRecord{}, fmt.Errorf("agentfabric: unknown capability %q (closed enumeration)", capability)
	}
	if now.IsZero() {
		return CommandRecord{}, fmt.Errorf("agentfabric: submission time is required")
	}
	id, err := rngCommandID()
	if err != nil {
		return CommandRecord{}, err
	}
	rec := CommandRecord{
		ID:          id,
		Intent:      intent,
		AgentID:     agent,
		Capability:  capability,
		State:       CommandSubmitted,
		SubmittedAt: now.UTC(),
		UpdatedAt:   now.UTC(),
	}
	// Defense in depth: the store never holds a record it would refuse to load.
	if err := rec.validate(); err != nil {
		return CommandRecord{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.commands) >= s.maxCommands {
		return CommandRecord{}, ErrCommandLimit
	}
	// Collision against an existing ID: cryptographically impossible, but
	// the check is free and the invariant (unique IDs) is load-enforced.
	for _, existing := range s.commands {
		if existing.ID == id {
			return CommandRecord{}, fmt.Errorf("agentfabric: command id collision (rng reuse) — submission aborted")
		}
	}
	s.commands = append(s.commands, rec)
	if err := s.persistLocked(s.snapshotLocked()); err != nil {
		s.commands = s.commands[:len(s.commands)-1] // rollback
		return CommandRecord{}, err
	}
	return copyCommandRecord(rec), nil
}

// Route binds the command to an agent session (SUBMITTED → ROUTED, or
// ROUTED → ROUTED re-bind). The session itself is kernel-owned: the
// caller resolves and validates it; the store records the binding.
func (s *CommandStore) Route(id CommandID, sessionID string, now time.Time) (CommandRecord, error) {
	if sessionID == "" {
		return CommandRecord{}, fmt.Errorf("agentfabric: session id is required for routing")
	}
	if now.IsZero() {
		return CommandRecord{}, fmt.Errorf("agentfabric: routing time is required")
	}
	return s.mutate(id, func(rec *CommandRecord) error {
		if err := rec.transition(CommandRouted, now); err != nil {
			return err
		}
		rec.SessionID = sessionID
		routed := now.UTC()
		rec.RoutedAt = &routed
		return nil
	})
}

// Decide records the kernel's decision on the command (ROUTED → DECIDED).
// A decision without a reason is rejected — every kernel decision names
// its rule ("dôvod výberu aj odmietnutia").
func (s *CommandStore) Decide(id CommandID, decision agentkernel.Decision, now time.Time) (CommandRecord, error) {
	if now.IsZero() {
		return CommandRecord{}, fmt.Errorf("agentfabric: decision time is required")
	}
	if decision.Reason == "" {
		return CommandRecord{}, ErrEmptyDecisionReason
	}
	return s.mutate(id, func(rec *CommandRecord) error {
		if err := rec.transition(CommandDecided, now); err != nil {
			return err
		}
		d := decision
		rec.Decision = &d
		decided := now.UTC()
		rec.DecidedAt = &decided
		return nil
	})
}

// Cancel withdraws the command before a decision (SUBMITTED/ROUTED →
// CANCELLED). DECIDED commands cannot be cancelled — a kernel decision is
// a fact, not a wish.
func (s *CommandStore) Cancel(id CommandID, now time.Time) (CommandRecord, error) {
	if now.IsZero() {
		return CommandRecord{}, fmt.Errorf("agentfabric: cancellation time is required")
	}
	return s.mutate(id, func(rec *CommandRecord) error {
		if err := rec.transition(CommandCancelled, now); err != nil {
			return err
		}
		cancelled := now.UTC()
		rec.CancelledAt = &cancelled
		return nil
	})
}

// copyCommandRecord returns a deep copy of the record: pointer fields
// (RoutedAt/DecidedAt/CancelledAt/Decision) are re-allocated so callers
// can never mutate store state through a returned record (the kernel
// store's copy() discipline — test-asserted).
func copyCommandRecord(r CommandRecord) CommandRecord {
	out := r
	if r.RoutedAt != nil {
		t := *r.RoutedAt
		out.RoutedAt = &t
	}
	if r.DecidedAt != nil {
		t := *r.DecidedAt
		out.DecidedAt = &t
	}
	if r.CancelledAt != nil {
		t := *r.CancelledAt
		out.CancelledAt = &t
	}
	if r.Decision != nil {
		d := *r.Decision
		out.Decision = &d
	}
	return out
}

// Get returns a deep copy of the record (callers never see live pointers).
func (s *CommandStore) Get(id CommandID) (CommandRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.commands {
		if rec.ID == string(id) {
			return copyCommandRecord(rec), true
		}
	}
	return CommandRecord{}, false
}

// ListLast returns the last n commands newest-first (submission order
// reversed — deterministic, no map iteration). n < 1 is clamped to 1.
func (s *CommandStore) ListLast(n int) []CommandRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 1 {
		n = 1
	}
	if n > len(s.commands) {
		n = len(s.commands)
	}
	out := make([]CommandRecord, 0, n)
	for i := len(s.commands) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, copyCommandRecord(s.commands[i]))
	}
	return out
}

// Counts returns the number of commands per state (deterministic order:
// the state enumeration order). All states are always present in the map.
func (s *CommandStore) Counts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{
		string(CommandSubmitted): 0,
		string(CommandRouted):    0,
		string(CommandDecided):   0,
		string(CommandCancelled): 0,
	}
	for _, rec := range s.commands {
		if _, known := counts[string(rec.State)]; known {
			counts[string(rec.State)]++
		}
	}
	return counts
}

// Count returns the total number of commands (all states).
func (s *CommandStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.commands)
}

// Path returns the store's file path (surfaced by the API for operators).
func (s *CommandStore) Path() string {
	return s.path
}
