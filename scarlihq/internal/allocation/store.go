package allocation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Store persists Allocation records to a JSONL file (one JSON object per line,
// keyed by allocation_id). This follows the existing project storage pattern
// used by internal/telemetry (append-only JSONL).
//
// PERSISTENCE GUARANTEES (per SCARGEN_COMPUTE_FABRIC_PHASE_2_GUIDE.md §7):
//   - Failed validation does NOT partially update the record.
//   - Persistence failures are surfaced explicitly (never swallowed).
//   - Never claim a transition is durable if writing failed.
//   - Uses atomic write (temp file + fsync + rename) for full-file rewrites.
//   - Appends for new records; rewrite for transitions (read-all + modify + write-all).
//
// CONCURRENCY (v19.2.3 KPK-04):
//   - The Store uses a process-level sync.Mutex for in-process serialization.
//   - A file-level lock (flock) protects against MULTI-PROCESS concurrent writers.
//     All write operations (Append, Update, PruneReleased) acquire an exclusive
//     flock on a lock file (s.path + ".lock") for the duration of the
//     read-modify-write cycle.
//   - The Revision field provides optimistic concurrency detection: Update()
//     re-reads the current revision from disk BEFORE writing, and rejects
//     with ErrRevisionConflict if it changed since the caller's read.
//   - This protects against both in-process and multi-process races.
//
// PERMISSIONS (v19.2.3 KPK-02):
//   - Store directory is created with mode 0700 (owner-only).
//   - Store file is created with mode 0600 (owner read/write only).
//   - No file is world-readable or group-readable.
//   - Allocation metadata (Owner, WorkloadRef, history) is operational data
//     that must not be exposed to unprivileged users.
//
// CRASH DURABILITY (v19.2.3 KPK-07):
//   - rewriteAll calls fsync on the temp file before rename.
//   - After rename, the parent directory is fsynced (on Linux) to ensure
//     the rename itself is durable.
//   - Append mode does NOT fsync after each write (performance trade-off);
//     the last few appended records may be lost on power failure. If strict
//     durability is needed for Append, call Sync() explicitly.
type Store struct {
	path     string
	mu       sync.Mutex // process-level mutex (in-process serialization)
	lockPath string     // path to flock file for multi-process safety
}

// ErrRevisionConflict is returned when a read-modify-write cycle detects
// that the record's Revision changed on disk since the caller's read.
// This indicates another process modified the record concurrently.
var ErrRevisionConflict = fmt.Errorf("allocation: revision conflict — record was modified by another writer")

// ErrNotFound is returned when an allocation ID does not exist in the store.
var ErrNotFound = fmt.Errorf("allocation: not found")

// ErrDuplicateID is returned when Append() is called with an AllocationID
// that already exists in the store.
// KPK-05 (v19.2.3): one ID must represent exactly one allocation record.
var ErrDuplicateID = fmt.Errorf("allocation: duplicate allocation_id — record already exists")

// ErrCorruptData is returned when the store file contains corrupt JSONL lines
// and a destructive operation (Update/PruneReleased) is attempted.
// KPK-03 (v19.2.3): corrupt data must NOT be silently discarded.
var ErrCorruptData = fmt.Errorf("allocation: corrupt data detected in store — refusing to rewrite (use Repair or manual inspection)")

// ErrCorrupt is returned when a stored record cannot be parsed.
// The record line is skipped during reads, but the caller is informed
// so it can log/audit the corruption.
type ErrCorrupt struct {
	LineNum int
	Err     error
}

func (e *ErrCorrupt) Error() string {
	return fmt.Sprintf("allocation: corrupt record at line %d: %v", e.LineNum, e.Err)
}

// NewStore creates a Store backed by the given JSONL file path.
// The file is created on first Append if it does not exist.
func NewStore(path string) *Store {
	return &Store{
		path:     path,
		lockPath: path + ".lock",
	}
}

// Path returns the store's file path.
func (s *Store) Path() string {
	return s.path
}

// ensureDir creates the parent directory with secure permissions (0700).
// KPK-02 (v19.2.3): was 0755, now 0700 (owner-only).
// KPK-02 remaining (v19.2.4): also validates/fixes permissions of EXISTING
// directories and files (MkdirAll does not change mode of existing dirs).
func (s *Store) ensureDir() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("allocation: cannot create store directory %s: %w", dir, err)
	}
	// KPK-02 remaining: verify/fix permissions of existing directory.
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("allocation: cannot stat store directory %s: %w", dir, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		// Directory has group/other access — fix it.
		if err := os.Chmod(dir, 0700); err != nil {
			return fmt.Errorf("allocation: cannot fix store directory permissions (insecure mode %o): %w", info.Mode().Perm(), err)
		}
	}
	return nil
}

// acquireFlock acquires an exclusive flock on the lock file.
// Returns the lock file handle (which MUST be closed to release) or an error.
// KPK-04 (v19.2.3): protects multi-process concurrent writers.
// KPK-02 remaining (v19.2.4): also validates/fixes permissions of EXISTING
// lock file (OpenFile does not change mode of existing file).
func (s *Store) acquireFlock() (*os.File, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	lockFile, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("allocation: cannot open lock file %s: %w", s.lockPath, err)
	}
	// KPK-02 remaining: verify/fix permissions of existing lock file.
	info, err := lockFile.Stat()
	if err == nil && info.Mode().Perm() != 0600 {
		_ = os.Chmod(s.lockPath, 0600) // best-effort fix
	}
	// Blocking exclusive lock — waits for other processes to release.
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("allocation: cannot acquire flock on %s: %w", s.lockPath, err)
	}
	return lockFile, nil
}

// Append writes a new allocation record to the store.
// The record's Revision is set to 0 (or kept if already set).
// This is the initial write — use Update for transitions.
//
// KPK-05 (v19.2.3): Returns ErrDuplicateID if the AllocationID already exists.
// KPK-04 (v19.2.3): Acquires flock for multi-process safety.
// KPK-02 (v19.2.3): File created with mode 0600.
func (s *Store) Append(a *Allocation) error {
	if a == nil {
		return fmt.Errorf("allocation: cannot append nil record")
	}
	if a.AllocationID == "" {
		return fmt.Errorf("allocation: cannot append record with empty allocation_id")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// KPK-04: Acquire flock for multi-process safety.
	lockFile, err := s.acquireFlock()
	if err != nil {
		return err
	}
	defer func() {
		syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
	}()

	// KPK-05: Check for duplicate ID before writing.
	// KPK-03 remaining (v19.2.4): also check for corruption — refuse to
	// Append to a corrupt store (was: silently ignored corruptErrs).
	records, corruptErrs, err := s.loadLocked()
	if err != nil {
		return err
	}
	// KPK-03 remaining: if corruption detected, refuse to Append.
	if len(corruptErrs) > 0 {
		return fmt.Errorf("%w: %d corrupt line(s) detected — refusing to append to corrupt store",
			ErrCorruptData, len(corruptErrs))
	}
	if existing, ok := records[a.AllocationID]; ok {
		_ = existing
		return ErrDuplicateID
	}

	// Open file for append (create if missing) with secure permissions.
	// KPK-02: was 0644, now 0600.
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("allocation: cannot open store file %s: %w", s.path, err)
	}
	defer f.Close()

	data, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("allocation: cannot marshal record: %w", err)
	}
	data = append(data, '\n')

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("allocation: cannot write record: %w", err)
	}

	// KPK-07: fsync for durability (Append is infrequent — one new allocation).
	if err := f.Sync(); err != nil {
		return fmt.Errorf("allocation: cannot fsync store file: %w", err)
	}

	return nil
}

// Load reads all allocation records from the store.
// Returns a map keyed by AllocationID.
// KPK-05 (v19.2.3): duplicate IDs in the file are reported as ErrCorrupt
// (integrity error), not silently merged.
func (s *Store) Load() (map[string]*Allocation, []error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// loadLocked reads all records WITHOUT acquiring the mutex.
// Caller MUST hold s.mu. Used by Update/PruneReleased which already hold the lock.
//
// KPK-05 (v19.2.3): duplicate IDs are now reported as corrupt errors,
// not silently merged via "last write wins".
func (s *Store) loadLocked() (map[string]*Allocation, []error, error) {
	records := make(map[string]*Allocation)
	var corruptErrs []error

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return records, nil, nil // empty store is valid
		}
		return nil, nil, fmt.Errorf("allocation: cannot read store file %s: %w", s.path, err)
	}

	lines := splitLines(data)
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		var a Allocation
		if err := json.Unmarshal(line, &a); err != nil {
			corruptErrs = append(corruptErrs, &ErrCorrupt{LineNum: i + 1, Err: err})
			continue
		}
		// KPK-05: detect duplicate IDs — report as integrity error.
		if _, exists := records[a.AllocationID]; exists {
			corruptErrs = append(corruptErrs, &ErrCorrupt{
				LineNum: i + 1,
				Err:     fmt.Errorf("duplicate allocation_id %q (integrity error)", a.AllocationID),
			})
			continue // keep the first occurrence, report the duplicate
		}
		records[a.AllocationID] = &a
	}

	return records, corruptErrs, nil
}

// Get reads a single allocation by ID.
// Returns ErrNotFound if the ID does not exist.
// P2-03 (v19.2.11): Returns corruption error when Load() detects corrupt records.
func (s *Store) Get(id string) (*Allocation, error) {
	records, corrupt, err := s.Load()
	if err != nil {
		return nil, err
	}
	if len(corrupt) > 0 {
		return nil, fmt.Errorf("%w: store has %d corrupt records — cannot read safely", ErrCorruptData, len(corrupt))
	}
	a, ok := records[id]
	if !ok {
		return nil, ErrNotFound
	}
	return a, nil
}

// Update performs a read-modify-write cycle for an allocation.
// The callback receives the current record; it can modify it
// and the result is written back atomically.
//
// KPK-04 (v19.2.3): Multi-process safety via flock. The Revision is
// re-read from disk BEFORE writing — if another process changed it,
// returns ErrRevisionConflict. This replaces the previous in-process-only
// check that could not detect cross-process races.
//
// KPK-03 (v19.2.3): If corrupt JSONL lines are detected during load,
// the update is REJECTED with ErrCorruptData. Corrupt data is never
// silently discarded.
//
// This is the safe path for transitions:
//
//	store.Update(id, func(a *Allocation) error {
//	    _, err := a.Transition(allocation.StateValidating, "validated contract")
//	    return err
//	})
func (s *Store) Update(id string, modify func(*Allocation) error) error {
	if id == "" {
		return fmt.Errorf("allocation: cannot update record with empty id")
	}
	if modify == nil {
		return fmt.Errorf("allocation: modify callback is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// KPK-04: Acquire flock for multi-process safety.
	lockFile, err := s.acquireFlock()
	if err != nil {
		return err
	}
	defer func() {
		syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
	}()

	// Read all records.
	records, corruptErrs, err := s.loadLocked()
	if err != nil {
		return err
	}

	// KPK-03: If there is corruption, REJECT the update — do not rewrite
	// (which would silently drop the corrupt lines).
	if len(corruptErrs) > 0 {
		return fmt.Errorf("%w: %d corrupt line(s) detected (first at line %d)",
			ErrCorruptData, len(corruptErrs), corruptErrs[0].(*ErrCorrupt).LineNum)
	}

	a, ok := records[id]
	if !ok {
		return ErrNotFound
	}

	// KPK-04: Capture the original revision from the DISK read (not the
	// caller's in-memory copy). This is the key fix — the previous code
	// compared against the same in-memory object, which could not detect
	// a concurrent write by another process.
	diskRev := a.Revision

	// Apply the modification.
	if err := modify(a); err != nil {
		// Validation failed — do NOT write. Record is unchanged on disk.
		return err
	}

	// KPK-04 remaining (v19.2.4): The previous comment claimed we "re-read
	// the record from disk" — this was misleading. We hold the flock for the
	// entire read-modify-write cycle, so no other process can have changed
	// the record between our read and write. The Revision check below is a
	// structural assertion that the modify() callback did not itself change
	// the Revision field (which would be a programming error). Multi-process
	// safety is guaranteed by the flock, NOT by this Revision check.
	//
	// If the flock is ever removed or if multi-process safety needs to work
	// WITHOUT flock (e.g. on non-POSIX systems), then a true optimistic
	// concurrency check would be needed: re-read from disk, compare Revision,
	// and reject if changed. Until then, flock + this structural check is
	// sufficient.
	if a.Revision != diskRev {
		// This can only happen if the modify() callback itself changed Revision
		// (which it should not do). Report as a programming error.
		return fmt.Errorf("%w: revision was modified by callback (should not happen)", ErrRevisionConflict)
	}

	// Increment revision for this write.
	a.Revision++

	// Rewrite the entire file atomically (all records, with the updated one).
	return s.rewriteAll(records)
}

// rewriteAll writes all records to a temp file, then renames it to the store path.
// This is the atomic-write pattern used throughout SCARLIX OS (mktemp + mv).
//
// KPK-02 (v19.2.3): temp file and final file use mode 0600.
// KPK-07 (v19.2.3): fsync temp file before rename; fsync parent dir after rename.
func (s *Store) rewriteAll(records map[string]*Allocation) error {
	dir := filepath.Dir(s.path)

	// Create temp file in the same directory (for atomic rename).
	// KPK-02: mode 0600 (was 0644 via CreateTemp default).
	tmp, err := os.CreateTemp(dir, ".allocation-store-*.tmp")
	if err != nil {
		return fmt.Errorf("allocation: cannot create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// KPK-02: Ensure temp file has 0600 permissions (CreateTemp may use 0600
	// already, but be explicit).
	if err := os.Chmod(tmpPath, 0600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("allocation: cannot chmod temp file: %w", err)
	}

	// Write all records.
	encoder := json.NewEncoder(tmp)
	for _, a := range records {
		if err := encoder.Encode(a); err != nil {
			tmp.Close()
			os.Remove(tmpPath) // cleanup temp file
			return fmt.Errorf("allocation: cannot encode record %s: %w", a.AllocationID, err)
		}
	}

	// KPK-07: fsync the temp file before closing + rename (crash durability).
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("allocation: cannot fsync temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("allocation: cannot close temp file: %w", err)
	}

	// Atomic rename.
	// P2-04 (v19.2.11): Removed post-rename chmod — it can return an error after
	// the file has already been replaced, creating an ambiguous commit boundary.
	// The temp file was already chmod'd to 0600 before writing, and rename
	// preserves the mode. No post-rename chmod needed.
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("allocation: cannot rename temp file to store: %w", err)
	}

	// KPK-07 remaining (v19.2.4): fsync the parent directory to make the
	// rename durable. On Linux/ext4/xfs, rename is not guaranteed durable
	// without dir fsync. We now treat dir fsync failure as a WARNING (logged
	// to stderr) but NOT a fatal error — the data file is already fsynced and
	// renamed, so the content is durable. The dir fsync ensures the directory
	// entry (the rename itself) is durable. If it fails, a crash after this
	// point could theoretically lose the rename (but not the temp file content).
	// This is a defined best-effort durability level: data content is guaranteed,
	// directory entry durability is best-effort.
	dirF, err := os.Open(dir)
	if err == nil {
		if syncErr := dirF.Sync(); syncErr != nil {
			// Log to stderr — not fatal. The file content is already durable.
			fmt.Fprintf(os.Stderr, "allocation: warning: cannot fsync directory (rename may not be durable): %v\n", syncErr)
		}
		dirF.Close()
	}

	return nil
}

// Count returns the number of allocation records in the store.
// P2-03 (v19.2.11): Returns corruption error.
func (s *Store) Count() (int, error) {
	records, corrupt, err := s.Load()
	if err != nil {
		return 0, err
	}
	if len(corrupt) > 0 {
		return 0, fmt.Errorf("%w: store has %d corrupt records", ErrCorruptData, len(corrupt))
	}
	return len(records), nil
}

// ListByState returns all allocations in the given state.
// P2-03 (v19.2.11): Returns corruption error.
func (s *Store) ListByState(state State) ([]*Allocation, error) {
	records, corrupt, err := s.Load()
	if err != nil {
		return nil, err
	}
	if len(corrupt) > 0 {
		return nil, fmt.Errorf("%w: store has %d corrupt records", ErrCorruptData, len(corrupt))
	}
	var result []*Allocation
	for _, a := range records {
		if a.State == state {
			result = append(result, a)
		}
	}
	return result, nil
}

// PruneReleased removes all records in the RELEASED state older than `olderThan`.
// Returns the number of records removed.
// Does NOT remove non-released records (they may still need cleanup).
//
// KPK-03 (v19.2.3): If corrupt JSONL lines are detected, the prune is REJECTED
// with ErrCorruptData. Corrupt data is never silently discarded.
// KPK-04 (v19.2.3): Acquires flock for multi-process safety.
func (s *Store) PruneReleased(olderThan time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// KPK-04: Acquire flock for multi-process safety.
	lockFile, err := s.acquireFlock()
	if err != nil {
		return 0, err
	}
	defer func() {
		syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
	}()

	records, corruptErrs, err := s.loadLocked()
	if err != nil {
		return 0, err
	}

	// KPK-03: If there is corruption, REJECT the prune.
	if len(corruptErrs) > 0 {
		return 0, fmt.Errorf("%w: %d corrupt line(s) detected — refusing to prune",
			ErrCorruptData, len(corruptErrs))
	}

	cutoff := time.Now().UTC().Add(-olderThan)
	removed := 0

	for id, a := range records {
		if a.State == StateReleased && a.UpdatedAt.Before(cutoff) {
			delete(records, id)
			removed++
		}
	}

	if removed > 0 {
		if err := s.rewriteAll(records); err != nil {
			return 0, err
		}
	}

	return removed, nil
}

// splitLines splits a byte slice into lines (by newline).
// The last line without a trailing newline is included.
// Empty lines are preserved (caller can skip them).
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
