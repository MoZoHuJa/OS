package nodeidentity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
)

// Store persists node records to a JSONL file — the same discipline as the
// reservation/allocation stores: 0700 directory, 0600 files, flock for
// multi-process safety, fsync for crash durability, corrupt-line refusal.
// The file holds the FULL registry state (one record per node); SaveAll is
// an atomic rewrite (temp file + rename), so a crash mid-write can never
// leave a half-persisted registry.
type Store struct {
	path     string
	lockPath string
	mu       sync.Mutex
}

// ErrStoreCorrupt means corrupt JSONL lines were detected — the registry
// refuses to operate on corrupt identity data.
var ErrStoreCorrupt = fmt.Errorf("nodeidentity: store corrupt — refusing to operate on corrupt data")

// NewStore creates a nodeidentity Store at path (e.g.
// <data-dir>/fabric/nodes.jsonl).
func NewStore(path string) *Store {
	return &Store{path: path, lockPath: path + ".lock"}
}

// Path returns the store file path.
func (s *Store) Path() string { return s.path }

func (s *Store) ensureDir() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("nodeidentity: cannot create dir %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("nodeidentity: cannot stat dir: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("nodeidentity: cannot fix dir perms %o: %w", info.Mode().Perm(), err)
		}
	}
	return nil
}

func (s *Store) acquireFlock() (*os.File, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("nodeidentity: cannot open lock file: %w", err)
	}
	if info, err := f.Stat(); err == nil && info.Mode().Perm() != 0o600 {
		_ = os.Chmod(s.lockPath, 0o600)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("nodeidentity: cannot acquire flock: %w", err)
	}
	return f, nil
}

// Load reads every persisted node record (sorted by NodeID — deterministic
// reload). Corrupt or duplicate lines are a hard failure: identity data
// does not get best-effort loaded.
func (s *Store) Load() ([]NodeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lockFile, err := s.acquireFlock()
	if err != nil {
		return nil, err
	}
	defer func() {
		syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
	}()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("nodeidentity: cannot read store: %w", err)
	}
	seen := make(map[string]bool)
	var records []NodeRecord
	for i, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var rec NodeRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("%w: line %d: %v", ErrStoreCorrupt, i+1, err)
		}
		if seen[rec.NodeID] {
			return nil, fmt.Errorf("%w: duplicate record for node %q", ErrStoreCorrupt, rec.NodeID)
		}
		seen[rec.NodeID] = true
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].NodeID < records[j].NodeID })
	return records, nil
}

// SaveAll atomically rewrites the whole registry state.
func (s *Store) SaveAll(records []NodeRecord) error {
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

	seen := make(map[string]bool)
	for _, rec := range records {
		if seen[rec.NodeID] {
			return fmt.Errorf("nodeidentity: refusing to save duplicate record for node %q", rec.NodeID)
		}
		seen[rec.NodeID] = true
	}

	tmpPath := s.path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("nodeidentity: cannot open temp store file: %w", err)
	}
	wrote := false
	defer func() {
		if !wrote {
			f.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	buf := make([]byte, 0, 4096)
	for _, rec := range records {
		line, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("nodeidentity: marshal error: %w", err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	if _, err := f.Write(buf); err != nil {
		return fmt.Errorf("nodeidentity: write error: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("nodeidentity: fsync error: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("nodeidentity: close error: %w", err)
	}
	wrote = true
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("nodeidentity: atomic rename failed: %w", err)
	}
	// fsync the directory so the rename itself is durable.
	if dir, err := os.Open(filepath.Dir(s.path)); err == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}

// splitLines splits JSONL bytes (tolerates a trailing newline; blank lines
// are skipped by the caller).
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	return lines
}
