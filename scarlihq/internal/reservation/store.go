package reservation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Store persists Reservation records to a JSONL file.
// Follows the same pattern as allocation.Store: JSONL + flock + atomic rewrite.
//
// KPK fixes from v19.2.3/v19.2.4 are applied:
//   - 0700 directory, 0600 file, 0600 lock file
//   - flock for multi-process safety
//   - ErrCorruptData rejection on corrupt lines
//   - fsync for crash durability
//   - duplicate ID detection
type Store struct {
	path     string
	lockPath string
	mu       sync.Mutex
}

// ErrDuplicateReservationID means a reservation with the same ID already exists.
var ErrDuplicateReservationID = fmt.Errorf("reservation: duplicate reservation_id")

// ErrReservationNotFound means the reservation ID does not exist.
var ErrReservationNotFound = fmt.Errorf("reservation: not found")

// ErrReservationStoreCorrupt means corrupt JSONL lines were detected.
var ErrReservationStoreCorrupt = fmt.Errorf("reservation: store corrupt — refusing to operate on corrupt data")

type ErrCorruptLine struct {
	LineNum int
	Err     error
}

func (e *ErrCorruptLine) Error() string {
	return fmt.Sprintf("reservation: corrupt line %d: %v", e.LineNum, e.Err)
}

// NewStore creates a reservation Store.
func NewStore(path string) *Store {
	return &Store{
		path:     path,
		lockPath: path + ".lock",
	}
}

// Path returns the store file path.
func (s *Store) Path() string { return s.path }

func (s *Store) ensureDir() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("reservation: cannot create dir %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("reservation: cannot stat dir: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		if err := os.Chmod(dir, 0700); err != nil {
			return fmt.Errorf("reservation: cannot fix dir perms %o: %w", info.Mode().Perm(), err)
		}
	}
	return nil
}

func (s *Store) acquireFlock() (*os.File, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("reservation: cannot open lock file: %w", err)
	}
	if info, err := f.Stat(); err == nil && info.Mode().Perm() != 0600 {
		_ = os.Chmod(s.lockPath, 0600)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("reservation: cannot acquire flock: %w", err)
	}
	return f, nil
}

// Append writes a new reservation. Rejects duplicates and corruption.
func (s *Store) append(r *Reservation) error {
	if r == nil || r.ReservationID == "" {
		return fmt.Errorf("reservation: cannot append nil or empty-id record")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	lockFile, err := s.acquireFlock()
	if err != nil {
		return err
	}
	defer func() {
		syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
	}()

	records, corrupt, err := s.loadLocked()
	if err != nil {
		return err
	}
	if len(corrupt) > 0 {
		return fmt.Errorf("%w: %d corrupt lines", ErrReservationStoreCorrupt, len(corrupt))
	}
	if _, exists := records[r.ReservationID]; exists {
		return ErrDuplicateReservationID
	}

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("reservation: cannot open store file: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("reservation: marshal error: %w", err)
	}
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("reservation: write error: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("reservation: fsync error: %w", err)
	}
	return nil
}

// Load reads all reservations.
func (s *Store) Load() (map[string]*Reservation, []error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (map[string]*Reservation, []error, error) {
	records := make(map[string]*Reservation)
	var corrupt []error

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return records, nil, nil
		}
		return nil, nil, fmt.Errorf("reservation: cannot read store: %w", err)
	}

	lines := splitLines(data)
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		var r Reservation
		if err := json.Unmarshal(line, &r); err != nil {
			corrupt = append(corrupt, &ErrCorruptLine{LineNum: i + 1, Err: err})
			continue
		}
		// P2-03 (v19.2.7): Validate record invariants on load.
		if err := r.ValidateRecord(); err != nil {
			corrupt = append(corrupt, &ErrCorruptLine{LineNum: i + 1, Err: fmt.Errorf("invalid record: %w", err)})
			continue
		}
		if _, exists := records[r.ReservationID]; exists {
			corrupt = append(corrupt, &ErrCorruptLine{
				LineNum: i + 1,
				Err:     fmt.Errorf("duplicate reservation_id %q", r.ReservationID),
			})
			continue
		}
		records[r.ReservationID] = &r
	}
	return records, corrupt, nil
}

// Get reads a single reservation by ID.
func (s *Store) Get(id string) (*Reservation, error) {
	records, _, err := s.Load()
	if err != nil {
		return nil, err
	}
	r, ok := records[id]
	if !ok {
		return nil, ErrReservationNotFound
	}
	return r, nil
}

// Update performs a read-modify-write cycle under flock.
func (s *Store) update(id string, modify func(*Reservation) error) error {
	if id == "" || modify == nil {
		return fmt.Errorf("reservation: invalid update args")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	lockFile, err := s.acquireFlock()
	if err != nil {
		return err
	}
	defer func() {
		syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
	}()

	records, corrupt, err := s.loadLocked()
	if err != nil {
		return err
	}
	if len(corrupt) > 0 {
		return fmt.Errorf("%w: %d corrupt lines", ErrReservationStoreCorrupt, len(corrupt))
	}

	r, ok := records[id]
	if !ok {
		return ErrReservationNotFound
	}

	diskRev := r.Revision
	if err := modify(r); err != nil {
		// P2-04 (v19.2.7): errNoChange is a sentinel for no-op — don't
		// increment revision or rewrite.
		if err == errNoChange {
			return nil
		}
		return err
	}
	if r.Revision != diskRev {
		return fmt.Errorf("reservation: revision modified by callback")
	}
	// P2-04: Increment revision and rewrite only if the callback made a change.
	r.Revision++
	return s.rewriteAll(records)
}

// ListByStatus returns all reservations with the given status.
func (s *Store) ListByStatus(status ReservationStatus) ([]*Reservation, error) {
	records, _, err := s.Load()
	if err != nil {
		return nil, err
	}
	var result []*Reservation
	for _, r := range records {
		if r.Status == status {
			result = append(result, r)
		}
	}
	return result, nil
}

// ListByAllocation returns all reservations for the given allocation ID.
func (s *Store) ListByAllocation(allocID string) ([]*Reservation, error) {
	records, _, err := s.Load()
	if err != nil {
		return nil, err
	}
	var result []*Reservation
	for _, r := range records {
		if r.AllocationID == allocID {
			result = append(result, r)
		}
	}
	return result, nil
}

// FindByIdempotencyKey returns the reservation with the given key, if any.
func (s *Store) FindByIdempotencyKey(key, owner string) (*Reservation, error) {
	records, _, err := s.Load()
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if r.IdempotencyKey == key && r.Owner == owner {
			return r, nil
		}
	}
	return nil, ErrReservationNotFound
}

func (s *Store) rewriteAll(records map[string]*Reservation) error {
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".reservation-store-*.tmp")
	if err != nil {
		return fmt.Errorf("reservation: cannot create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if err := os.Chmod(tmpPath, 0600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("reservation: chmod temp: %w", err)
	}

	encoder := json.NewEncoder(tmp)
	for _, r := range records {
		if err := encoder.Encode(r); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("reservation: encode %s: %w", r.ReservationID, err)
		}
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("reservation: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("reservation: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("reservation: rename: %w", err)
	}
	if err := os.Chmod(s.path, 0600); err != nil {
		return fmt.Errorf("reservation: chmod store: %w", err)
	}

	// Best-effort dir fsync.
	if dirF, err := os.Open(dir); err == nil {
		if syncErr := dirF.Sync(); syncErr != nil {
			fmt.Fprintf(os.Stderr, "reservation: warning: dir fsync failed: %v\n", syncErr)
		}
		dirF.Close()
	}
	return nil
}

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

// PruneReleased removes RELEASED reservations older than olderThan.
// Rejects if corruption is detected (KPK-03 pattern).
func (s *Store) pruneReleased(olderThan time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lockFile, err := s.acquireFlock()
	if err != nil {
		return 0, err
	}
	defer func() {
		syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
	}()

	records, corrupt, err := s.loadLocked()
	if err != nil {
		return 0, err
	}
	if len(corrupt) > 0 {
		return 0, fmt.Errorf("%w: %d corrupt lines", ErrReservationStoreCorrupt, len(corrupt))
	}

	cutoff := time.Now().UTC().Add(-olderThan)
	removed := 0
	for id, r := range records {
		if r.Status == StatusReleased && r.UpdatedAt.Before(cutoff) {
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
