// BF-2 (v19.5.1): the persisted workspace store — the same discipline as
// the BF-1 command store and the AK-3 grant registry before it:
//
//   - JSON file (default /var/lib/scarlix-agentfabric/workspaces.json,
//     derived from the commands file's directory) with an explicit schema
//     version; Load rejects anything else.
//   - 0600 file / 0700 directory — checked at load AND before every
//     persist; a widened mode is tampering until an operator fixes it.
//   - Atomic persistence: temp file in the same directory (0600 by
//     construction, fsync) + rename; a failed persist rolls back the
//     in-memory mutation (state and file stay in sync).
//   - Workspaces are never silently deleted — ARCHIVED is the terminal
//     tombstone, the file is the reviewable history.
//   - Bounded: DefaultMaxWorkspaces records, MaxSessionsPerWorkspace
//     members per workspace. Bounds are real state, not free.
//   - Workspace IDs are "ws-" + 16 crypto-random bytes hex; RNG failure
//     aborts creation (fail-closed).
//   - A session belongs to at most ONE workspace — enforced across all
//     records at join time and re-validated at load (an ambiguous routing
//     namespace is never persisted).
//
// Like the command store, this store does NOT know the Agent Kernel:
// session existence and ACTIVITY are resolved by the API layer (the
// workspace owns isolation topology; the kernel owns sessions — separate
// concerns, composed at the boundary).
package agentfabric

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Sentinel errors for the workspace store.
var (
	// ErrWorkspaceRNG: workspace-ID generation failed — fail-closed.
	ErrWorkspaceRNG = errors.New("agentfabric: RNG unavailable (workspaces)")
	// ErrUnknownWorkspace: no workspace with the requested ID.
	ErrUnknownWorkspace = errors.New("agentfabric: unknown workspace")
	// ErrWorkspaceState: the workspace is ARCHIVED (terminal) — no
	// submissions, no routing, no membership changes.
	ErrWorkspaceState = errors.New("agentfabric: workspace is ARCHIVED (terminal)")
	// ErrWorkspaceLimit: the store is at capacity (bounded history).
	ErrWorkspaceLimit = errors.New("agentfabric: workspace store at capacity")
	// ErrWorkspaceFilePermissions: the workspaces file (or directory) is
	// looser than 0600/0700 — fail-closed.
	ErrWorkspaceFilePermissions = errors.New("agentfabric: workspaces file permissions too loose")
	// ErrSessionLimit: the workspace membership is at capacity.
	ErrSessionLimit = errors.New("agentfabric: workspace membership at capacity")
	// ErrSessionAlreadyMember: the session already belongs to this workspace.
	ErrSessionAlreadyMember = errors.New("agentfabric: session is already a member")
	// ErrSessionClaimed: the session already belongs to ANOTHER workspace
	// (one workspace per session — unambiguous routing namespace).
	ErrSessionClaimed = errors.New("agentfabric: session already belongs to another workspace")
	// ErrSessionNotMember: leave requested for a non-member.
	ErrSessionNotMember = errors.New("agentfabric: session is not a member of this workspace")
	// ErrSessionShape: the session ID does not have the aks- shape.
	ErrSessionShape = errors.New("agentfabric: session id must be an aks- id (join through the kernel session surface)")
)

// DefaultMaxWorkspaces bounds the total number of workspace records (ALL
// states). Workspaces are long-lived contexts, deliberately scarcer than
// commands: at capacity Create fails with ErrWorkspaceLimit (HTTP 503) —
// the operator archives the file deliberately, like every other bound.
const DefaultMaxWorkspaces = 64

// MaxSessionsPerWorkspace bounds one workspace's membership.
const MaxSessionsPerWorkspace = 32

// workspacesFileVersion is the on-disk schema version. Load rejects
// anything else — a future schema change must be an explicit migration.
const workspacesFileVersion = 1

// workspacesFile is the on-disk representation. Workspaces are stored in
// creation order (append order) — List returns oldest-first and callers
// reverse for newest-first views.
type workspacesFile struct {
	Version    int               `json:"version"`
	Workspaces []WorkspaceRecord `json:"workspaces"`
}

// WorkspaceStore is the persisted, bounded, mutex-guarded workspace store.
type WorkspaceStore struct {
	mu            sync.Mutex
	path          string
	maxWorkspaces int
	workspaces    []WorkspaceRecord
}

// NewWorkspaceStore constructs an empty in-memory store bound to a file
// path. It does NOT touch the file — call Load to read (or initialize)
// it. Fail-closed construction: empty path or non-positive bound is an
// error.
func NewWorkspaceStore(path string, maxWorkspaces int) (*WorkspaceStore, error) {
	if path == "" {
		return nil, fmt.Errorf("agentfabric: workspaces file path is required")
	}
	if maxWorkspaces <= 0 {
		return nil, fmt.Errorf("agentfabric: maxWorkspaces must be > 0 (got %d)", maxWorkspaces)
	}
	return &WorkspaceStore{
		path:          path,
		maxWorkspaces: maxWorkspaces,
		workspaces:    make([]WorkspaceRecord, 0),
	}, nil
}

// rngWorkspaceID generates "ws-" + 16 crypto-random bytes hex.
// Package-level variable so tests can inject RNG failure (the cmd-/aks-
// fail-closed precedent).
var rngWorkspaceID = func() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", ErrWorkspaceRNG
	}
	return workspaceIDPrefix + hex.EncodeToString(b), nil
}

// Load reads and validates the workspaces file, or initializes it empty
// on first run. Fail-closed: ANY malformed record (ID shape, text bounds,
// closed-enumeration state, membership invariants, monotone timestamps),
// ANY duplicate workspace ID, ANY session claimed by two workspaces, ANY
// count above a bound, a wrong schema version, or looser-than-0600/0700
// permissions abort the load. A tampered workspaces file must never
// degrade into "some workspaces work".
func (s *WorkspaceStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := os.Stat(s.path)
	switch {
	case err == nil:
		// exists — permissions checked below
	case errors.Is(err, os.ErrNotExist):
		if err := s.ensureDirLocked(); err != nil {
			return err
		}
		empty := workspacesFile{Version: workspacesFileVersion, Workspaces: []WorkspaceRecord{}}
		if err := s.persistLocked(empty); err != nil {
			return fmt.Errorf("agentfabric: cannot initialize workspaces file %s: %w", s.path, err)
		}
		s.workspaces = make([]WorkspaceRecord, 0)
		return nil
	default:
		return fmt.Errorf("agentfabric: cannot stat workspaces file %s: %w", s.path, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: %s has mode %o (want 0600)", ErrWorkspaceFilePermissions, s.path, info.Mode().Perm())
	}
	if err := s.checkDirPermsLocked(); err != nil {
		return err
	}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("agentfabric: cannot read workspaces file %s: %w", s.path, err)
	}
	var f workspacesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("agentfabric: workspaces file %s is not valid JSON: %w", s.path, err)
	}
	if f.Version != workspacesFileVersion {
		return fmt.Errorf("agentfabric: workspaces file %s has schema version %d (want %d) — explicit migration required", s.path, f.Version, workspacesFileVersion)
	}
	if len(f.Workspaces) > s.maxWorkspaces {
		return fmt.Errorf("agentfabric: workspaces file %s carries %d records (bound %d) — archive and rotate the file deliberately", s.path, len(f.Workspaces), s.maxWorkspaces)
	}
	seen := make(map[string]bool, len(f.Workspaces))
	claimed := make(map[string]string) // session id → workspace id
	for i, ws := range f.Workspaces {
		if err := ws.validate(); err != nil {
			return fmt.Errorf("agentfabric: workspaces file %s record %d: %w", s.path, i, err)
		}
		if seen[ws.ID] {
			return fmt.Errorf("agentfabric: workspaces file %s record %d: duplicate workspace id %s", s.path, i, ws.ID)
		}
		seen[ws.ID] = true
		for _, sid := range ws.Sessions {
			if owner, ok := claimed[sid]; ok {
				return fmt.Errorf("agentfabric: workspaces file %s: session %s is claimed by both %s and %s (one workspace per session)", s.path, sid, owner, ws.ID)
			}
			claimed[sid] = ws.ID
		}
	}
	// Copy in (defensive: never alias the decoded slice).
	s.workspaces = make([]WorkspaceRecord, len(f.Workspaces))
	copy(s.workspaces, f.Workspaces)
	return nil
}

// ensureDirLocked creates the parent directory 0700 if missing.
func (s *WorkspaceStore) ensureDirLocked() error {
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

// checkDirPermsLocked rejects a directory looser than 0700.
func (s *WorkspaceStore) checkDirPermsLocked() error {
	dir := filepath.Dir(s.path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("agentfabric: cannot stat directory %s: %w", dir, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: directory %s has mode %o (want 0700)", ErrWorkspaceFilePermissions, dir, info.Mode().Perm())
	}
	return nil
}

// persistLocked writes the file atomically: temp file in the same
// directory (0600 by construction, fsync) + rename. On any error the
// caller rolls back the in-memory state.
func (s *WorkspaceStore) persistLocked(f workspacesFile) error {
	if err := s.ensureDirLocked(); err != nil {
		return err
	}
	if err := s.checkDirPermsLocked(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("agentfabric: cannot encode workspaces file: %w", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".workspaces-*.tmp")
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
func (s *WorkspaceStore) snapshotLocked() workspacesFile {
	f := workspacesFile{Version: workspacesFileVersion, Workspaces: make([]WorkspaceRecord, len(s.workspaces))}
	copy(f.Workspaces, s.workspaces)
	return f
}

// Create records a new ACTIVE workspace. Name/description are re-validated
// here (defense in depth — the HTTP layer validates early for clean 400s;
// the store is the authority). Fail-closed: RNG failure aborts creation;
// at capacity ErrWorkspaceLimit.
func (s *WorkspaceStore) Create(name, description string, now time.Time) (WorkspaceRecord, error) {
	if err := ValidateWorkspaceName(name); err != nil {
		return WorkspaceRecord{}, err
	}
	if err := ValidateWorkspaceDescription(description); err != nil {
		return WorkspaceRecord{}, err
	}
	if now.IsZero() {
		return WorkspaceRecord{}, fmt.Errorf("agentfabric: creation time is required")
	}
	id, err := rngWorkspaceID()
	if err != nil {
		return WorkspaceRecord{}, err
	}
	rec := WorkspaceRecord{
		ID:          id,
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		State:       WorkspaceActive,
		Sessions:    []string{},
		CreatedAt:   now.UTC(),
		UpdatedAt:   now.UTC(),
	}
	if err := rec.validate(); err != nil {
		return WorkspaceRecord{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.workspaces) >= s.maxWorkspaces {
		return WorkspaceRecord{}, ErrWorkspaceLimit
	}
	// Collision against an existing ID: cryptographically impossible, but
	// the check is free and the invariant (unique IDs) is load-enforced.
	for _, existing := range s.workspaces {
		if existing.ID == id {
			return WorkspaceRecord{}, fmt.Errorf("agentfabric: workspace id collision (rng reuse) — creation aborted")
		}
	}
	s.workspaces = append(s.workspaces, rec)
	if err := s.persistLocked(s.snapshotLocked()); err != nil {
		s.workspaces = s.workspaces[:len(s.workspaces)-1] // rollback
		return WorkspaceRecord{}, err
	}
	return copyWorkspaceRecord(rec), nil
}

// mutate runs fn under the lock against a private copy of the record,
// then persists the new state. On persist failure the in-memory state is
// rolled back — state and file stay in sync.
func (s *WorkspaceStore) mutate(id WorkspaceID, fn func(rec *WorkspaceRecord) error) (WorkspaceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.workspaces {
		if s.workspaces[i].ID == string(id) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return WorkspaceRecord{}, ErrUnknownWorkspace
	}
	before := s.workspaces[idx]
	candidate := copyWorkspaceRecord(before) // deep copy — Sessions re-allocated
	if err := fn(&candidate); err != nil {
		return WorkspaceRecord{}, err
	}
	if err := candidate.validate(); err != nil {
		return WorkspaceRecord{}, err
	}
	// Cross-record invariant: one workspace per session (re-checked here so
	// a mutation can never steal a session another record already claims).
	for i, other := range s.workspaces {
		if i == idx {
			continue
		}
		for _, sid := range candidate.Sessions {
			if other.IsMember(sid) {
				return WorkspaceRecord{}, fmt.Errorf("%w: %s belongs to workspace %s", ErrSessionClaimed, sid, other.ID)
			}
		}
	}
	saved := s.workspaces[idx]
	s.workspaces[idx] = candidate
	if err := s.persistLocked(s.snapshotLocked()); err != nil {
		s.workspaces[idx] = saved // rollback — state and file stay in sync
		return WorkspaceRecord{}, err
	}
	return copyWorkspaceRecord(candidate), nil
}

// Archive tombstones the workspace (ACTIVE → ARCHIVED, terminal). Its
// commands stay visible and cancellable; new submissions and routing are
// closed from the next request on.
func (s *WorkspaceStore) Archive(id WorkspaceID, now time.Time) (WorkspaceRecord, error) {
	if now.IsZero() {
		return WorkspaceRecord{}, fmt.Errorf("agentfabric: archive time is required")
	}
	return s.mutate(id, func(rec *WorkspaceRecord) error {
		if rec.State == WorkspaceArchived {
			return fmt.Errorf("%w: workspace %s is already ARCHIVED", ErrWorkspaceState, rec.ID)
		}
		rec.State = WorkspaceArchived
		archived := now.UTC()
		rec.ArchivedAt = &archived
		rec.UpdatedAt = archived
		return nil
	})
}

// JoinSession attaches a kernel session to an ACTIVE workspace. The
// session must have the aks- shape (existence/ACTIVITY are the API
// layer's check), must not already be a member, and must not be claimed
// by another workspace (one workspace per session).
func (s *WorkspaceStore) JoinSession(id WorkspaceID, sessionID string, now time.Time) (WorkspaceRecord, error) {
	if !validSessionIDShape(sessionID) {
		return WorkspaceRecord{}, fmt.Errorf("%w: %q", ErrSessionShape, sessionID)
	}
	if now.IsZero() {
		return WorkspaceRecord{}, fmt.Errorf("agentfabric: join time is required")
	}
	return s.mutate(id, func(rec *WorkspaceRecord) error {
		if rec.State == WorkspaceArchived {
			return fmt.Errorf("%w: workspace %s accepts no membership changes", ErrWorkspaceState, rec.ID)
		}
		if rec.IsMember(sessionID) {
			return fmt.Errorf("%w: in workspace %s", ErrSessionAlreadyMember, rec.ID)
		}
		if len(rec.Sessions) >= MaxSessionsPerWorkspace {
			return fmt.Errorf("%w: workspace %s holds %d of %d members", ErrSessionLimit, rec.ID, len(rec.Sessions), MaxSessionsPerWorkspace)
		}
		rec.Sessions = append(rec.Sessions, sessionID)
		rec.UpdatedAt = now.UTC()
		return nil
	})
}

// LeaveSession detaches a member session from an ACTIVE workspace.
func (s *WorkspaceStore) LeaveSession(id WorkspaceID, sessionID string, now time.Time) (WorkspaceRecord, error) {
	if sessionID == "" {
		return WorkspaceRecord{}, fmt.Errorf("agentfabric: session id is required for leave")
	}
	if now.IsZero() {
		return WorkspaceRecord{}, fmt.Errorf("agentfabric: leave time is required")
	}
	return s.mutate(id, func(rec *WorkspaceRecord) error {
		if rec.State == WorkspaceArchived {
			return fmt.Errorf("%w: workspace %s accepts no membership changes", ErrWorkspaceState, rec.ID)
		}
		idx := -1
		for i, sid := range rec.Sessions {
			if sid == sessionID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("%w: %s in workspace %s", ErrSessionNotMember, sessionID, rec.ID)
		}
		rec.Sessions = append(rec.Sessions[:idx], rec.Sessions[idx+1:]...)
		if rec.Sessions == nil {
			rec.Sessions = []string{}
		}
		rec.UpdatedAt = now.UTC()
		return nil
	})
}

// Get returns a deep copy of the record (callers never see live pointers).
func (s *WorkspaceStore) Get(id WorkspaceID) (WorkspaceRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.workspaces {
		if rec.ID == string(id) {
			return copyWorkspaceRecord(rec), true
		}
	}
	return WorkspaceRecord{}, false
}

// List returns ALL workspaces oldest-first (creation order — stable and
// deterministic, no map iteration). The read surface is bounded by the
// store bound itself.
func (s *WorkspaceStore) List() []WorkspaceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WorkspaceRecord, 0, len(s.workspaces))
	for _, rec := range s.workspaces {
		out = append(out, copyWorkspaceRecord(rec))
	}
	return out
}

// SessionWorkspace returns the (unique) workspace a session belongs to —
// the routing-namespace lookup for workspace-scoped commands. Sessions
// may belong to no workspace at all (unscoped, BF-1 behavior).
func (s *WorkspaceStore) SessionWorkspace(sessionID string) (WorkspaceRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.workspaces {
		if rec.IsMember(sessionID) {
			return copyWorkspaceRecord(rec), true
		}
	}
	return WorkspaceRecord{}, false
}

// Count returns the total number of workspaces (all states).
func (s *WorkspaceStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.workspaces)
}

// Path returns the store's file path (surfaced by the API for operators).
func (s *WorkspaceStore) Path() string {
	return s.path
}
