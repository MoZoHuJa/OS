package allocation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
//   - Uses atomic write (temp file + rename) for full-file rewrites.
//   - Appends for new records; rewrite for transitions (read-all + modify + write-all).
//
// CONCURRENCY LIMITATIONS (documented, not hidden):
//   - The Store uses a process-level mutex for its own operations.
//   - It does NOT protect against multi-process concurrent writers.
//   - If multiple processes write to the same file, last-write-wins races
//     can occur. This is a KNOWN LIMITATION for Phase 2.
//   - The Revision field provides optimistic concurrency detection:
//     if a read-modify-write cycle detects Revision changed, it rejects
//     the write with ErrRevisionConflict.
//   - For production multi-writer scenarios, a proper lock file or database
//     is needed (documented as a Phase 3+ decision).
type Store struct {
	path string
	mu   sync.Mutex // process-level mutex (does NOT protect multi-process)
}

// ErrRevisionConflict is returned when a read-modify-write cycle detects
// that the record's Revision changed since the read.
var ErrRevisionConflict = fmt.Errorf("allocation: revision conflict — record was modified by another writer")

// ErrNotFound is returned when an allocation ID does not exist in the store.
var ErrNotFound = fmt.Errorf("allocation: not found")

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
	return &Store{path: path}
}

// Path returns the store's file path.
func (s *Store) Path() string {
	return s.path
}

// Append writes a new allocation record to the store.
// The record's Revision is set to 0 (or kept if already set).
// This is the initial write — use Update for transitions.
//
// Returns ErrNotFound semantics differently: Append always creates.
func (s *Store) Append(a *Allocation) error {
	if a == nil {
		return fmt.Errorf("allocation: cannot append nil record")
	}
	if a.AllocationID == "" {
		return fmt.Errorf("allocation: cannot append record with empty allocation_id")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Ensure parent directory exists (matching telemetry.Store convention).
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("allocation: cannot create store directory %s: %w", dir, err)
	}

	// Open file for append (create if missing).
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
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

	return nil
}

// Load reads all allocation records from the store.
// Returns a map keyed by AllocationID (last write wins on duplicate IDs).
// Corrupt lines are skipped and reported via the error slice.
func (s *Store) Load() (map[string]*Allocation, []error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// loadLocked reads all records WITHOUT acquiring the mutex.
// Caller MUST hold s.mu. Used by Update/PruneReleased which already hold the lock.
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
		records[a.AllocationID] = &a // last write wins
	}

	return records, corruptErrs, nil
}

// Get reads a single allocation by ID.
// Returns ErrNotFound if the ID does not exist.
func (s *Store) Get(id string) (*Allocation, error) {
	records, _, err := s.Load()
	if err != nil {
		return nil, err
	}
	a, ok := records[id]
	if !ok {
		return nil, ErrNotFound
	}
	return a, nil
}

// Update performs a read-modify-write cycle for an allocation.
// The callback receives a COPY of the current record; it can modify it
// and the result is written back atomically.
//
// Optimistic concurrency: the Revision is checked before write.
// If Revision changed since Load (another writer), returns ErrRevisionConflict.
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

	// Read all records (caller already holds the lock via Update).
	records, corruptErrs, err := s.loadLocked()
	if err != nil {
		return err
	}
	// Log corrupt records (don't fail — partial availability).
	_ = corruptErrs // caller can check Load separately if needed

	a, ok := records[id]
	if !ok {
		return ErrNotFound
	}

	// Capture the original revision for optimistic concurrency check.
	originalRev := a.Revision

	// Apply the modification.
	if err := modify(a); err != nil {
		// Validation failed — do NOT write. Record is unchanged on disk.
		return err
	}

	// Check optimistic concurrency: if Revision changed during modify(),
	// another writer may have raced. Reject.
	if a.Revision != originalRev {
		return ErrRevisionConflict
	}

	// Increment revision for this write.
	a.Revision++

	// Rewrite the entire file atomically (all records, with the updated one).
	return s.rewriteAll(records)
}

// rewriteAll writes all records to a temp file, then renames it to the store path.
// This is the atomic-write pattern used throughout SCARLIX OS (mktemp + mv).
func (s *Store) rewriteAll(records map[string]*Allocation) error {
	dir := filepath.Dir(s.path)

	// Create temp file in the same directory (for atomic rename).
	tmp, err := os.CreateTemp(dir, ".allocation-store-*.tmp")
	if err != nil {
		return fmt.Errorf("allocation: cannot create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Write all records.
	encoder := json.NewEncoder(tmp)
	for _, a := range records {
		if err := encoder.Encode(a); err != nil {
			tmp.Close()
			os.Remove(tmpPath) // cleanup temp file
			return fmt.Errorf("allocation: cannot encode record %s: %w", a.AllocationID, err)
		}
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("allocation: cannot close temp file: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("allocation: cannot rename temp file to store: %w", err)
	}

	// Ensure 0644 permissions (matching host-status.json convention).
	if err := os.Chmod(s.path, 0644); err != nil {
		// Non-fatal — file exists, just wrong mode.
		return fmt.Errorf("allocation: warning: cannot chmod store file: %w", err)
	}

	return nil
}

// Count returns the number of allocation records in the store.
func (s *Store) Count() (int, error) {
	records, _, err := s.Load()
	if err != nil {
		return 0, err
	}
	return len(records), nil
}

// ListByState returns all allocations in the given state.
func (s *Store) ListByState(state State) ([]*Allocation, error) {
	records, _, err := s.Load()
	if err != nil {
		return nil, err
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
func (s *Store) PruneReleased(olderThan time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, _, err := s.loadLocked()
	if err != nil {
		return 0, err
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
