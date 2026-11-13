package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RecordStore is the agent's durability boundary (design §4.3: an agent
// restart reloads its execution records + mirror). The production
// implementation is the repo's JSONL discipline (flock, 0600, atomic
// whole-state rewrite); tests use the in-memory no-op.
type RecordStore interface {
	LoadRecords() ([]ExecutionRecord, error)
	SaveRecords(records []ExecutionRecord) error
	LoadMirror() ([]ReservationNotice, error)
	SaveMirror(notices []ReservationNotice) error
}

// JSONLRecordStore persists the agent's execution records and reservation
// mirror — the same discipline as every other store in the repo: 0700
// directory, 0600 files, flock, fsync, corrupt-line refusal.
type JSONLRecordStore struct {
	recordsPath string
	mirrorPath  string
	mu          sync.Mutex
}

// NewJSONLRecordStore builds the store (records + mirror under dir).
func NewJSONLRecordStore(dir string) *JSONLRecordStore {
	return &JSONLRecordStore{
		recordsPath: filepath.Join(dir, "agent-executions.jsonl"),
		mirrorPath:  filepath.Join(dir, "agent-mirror.jsonl"),
	}
}

func (s *JSONLRecordStore) ensureDir() error {
	if err := os.MkdirAll(filepath.Dir(s.recordsPath), 0o700); err != nil {
		return fmt.Errorf("nodeagent: cannot create dir: %w", err)
	}
	return nil
}

// loadJSONL reads one JSONL file into records (any typed T).
func loadJSONL[T any](path string) ([]T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("nodeagent: cannot read %s: %w", path, err)
	}
	var out []T
	for i, line := range splitJSONL(data) {
		if len(line) == 0 {
			continue
		}
		var rec T
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("nodeagent: corrupt store %s line %d: %w", path, i+1, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// saveJSONL atomically rewrites one JSONL file (temp + rename + fsync).
func saveJSONL[T any](path string, records []T) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("nodeagent: cannot create dir: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("nodeagent: cannot open temp file: %w", err)
	}
	wrote := false
	defer func() {
		if !wrote {
			f.Close()
			_ = os.Remove(tmp)
		}
	}()
	enc := json.NewEncoder(f)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("nodeagent: encode error: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("nodeagent: fsync error: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("nodeagent: close error: %w", err)
	}
	wrote = true
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("nodeagent: atomic rename failed: %w", err)
	}
	return nil
}

// LoadRecords reads the durable execution records.
func (s *JSONLRecordStore) LoadRecords() ([]ExecutionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	return loadJSONL[ExecutionRecord](s.recordsPath)
}

// SaveRecords atomically rewrites the execution records.
func (s *JSONLRecordStore) SaveRecords(records []ExecutionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	return saveJSONL(s.recordsPath, records)
}

// LoadMirror reads the durable reservation mirror.
func (s *JSONLRecordStore) LoadMirror() ([]ReservationNotice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	return loadJSONL[ReservationNotice](s.mirrorPath)
}

// SaveMirror atomically rewrites the reservation mirror.
func (s *JSONLRecordStore) SaveMirror(notices []ReservationNotice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	return saveJSONL(s.mirrorPath, notices)
}

// splitJSONL splits JSONL bytes (blank lines skipped by callers).
func splitJSONL(data []byte) [][]byte {
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
