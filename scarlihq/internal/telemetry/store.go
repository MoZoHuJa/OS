// Package telemetry implements lightweight measurement persistence for
// ScarliMonitor. Per ScaRgeN master guide section 16: "Persist lightweight
// measurements. Do not create a heavy analytics database unless necessary."
//
// Storage format: JSON Lines (one JSON object per line) in a single file.
// This avoids a SQLite dependency while providing append-only writes + simple
// time-range queries. Each line is a self-contained Measurement.
//
// Default storage path: /var/lib/scarlix/telemetry.jsonl
// Override via SCARLIX_TELEMETRY_FILE env var.
package telemetry

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Measurement is a single telemetry data point. Persisted as one JSON line.
// Field names are FROZEN in v19.1.5 (additive changes only without v2).
type Measurement struct {
	Timestamp      string  `json:"timestamp"`    // ISO 8601 UTC
	GPUIndex       int     `json:"gpu_index"`    // 0, 1, ...
	GPUUtilPct     float64 `json:"gpu_util_pct"` // 0-100
	GPUVRAMUsedMB  int     `json:"gpu_vram_used_mb"`
	GPUVRAMTotalMB int     `json:"gpu_vram_total_mb"`
	GPUTempC       int     `json:"gpu_temp_c"`     // GPU temperature °C
	Runtime        string  `json:"runtime"`        // "sglang", "vllm", etc. (or "" if none running)
	Model          string  `json:"model"`          // model ID (or "")
	LatencyMs      float64 `json:"latency_ms"`     // last request latency (0 if none)
	TokensPerSec   float64 `json:"tokens_per_sec"` // last request throughput (0 if none)
}

// Store is the telemetry persistence layer. Thread-safe for concurrent appends
// (uses file-level append). Query/Prune read the whole file.
type Store struct {
	path string
}

// New creates a Store at the given path. If path is empty, uses default
// (/var/lib/scarlix/telemetry.jsonl or $SCARLIX_TELEMETRY_FILE).
func New(path string) *Store {
	if path == "" {
		path = defaultPath()
	}
	return &Store{path: path}
}

func defaultPath() string {
	if p := os.Getenv("SCARLIX_TELEMETRY_FILE"); p != "" {
		return p
	}
	return "/var/lib/scarlix/telemetry.jsonl"
}

// Append writes a single measurement to the store. Creates the file + parent
// directory if they don't exist. Append-only — never overwrites existing data.
func (s *Store) Append(m Measurement) error {
	dir := dirOf(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("telemetry: create dir %s: %w", dir, err)
	}

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("telemetry: open %s: %w", s.path, err)
	}
	defer f.Close()

	if m.Timestamp == "" {
		m.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("telemetry: marshal: %w", err)
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("telemetry: write: %w", err)
	}

	return nil
}

// Query returns measurements within a time range [from, to]. If from/to are
// zero times, no bound is applied on that side. Results are sorted by
// timestamp ascending. Returns empty slice (never nil) if no matches.
func (s *Store) Query(from, to time.Time) ([]Measurement, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Measurement{}, nil
		}
		return nil, fmt.Errorf("telemetry: open %s: %w", s.path, err)
	}
	defer f.Close()

	var results []Measurement
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var m Measurement
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}

		ts, err := time.Parse(time.RFC3339, m.Timestamp)
		if err != nil {
			continue
		}

		if !from.IsZero() && ts.Before(from) {
			continue
		}
		if !to.IsZero() && ts.After(to) {
			continue
		}

		results = append(results, m)
	}

	if err := scanner.Err(); err != nil {
		return results, fmt.Errorf("telemetry: scan: %w", err)
	}

	if results == nil {
		results = []Measurement{}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp < results[j].Timestamp
	})

	return results, nil
}

// Path returns the storage file path (for logging/debugging).
func (s *Store) Path() string {
	return s.path
}

// Count returns the total number of measurements in the store.
func (s *Store) Count() (int, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("telemetry: open: %w", err)
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			count++
		}
	}
	return count, scanner.Err()
}

// Prune removes measurements older than the given duration. Returns the
// number of entries removed.
func (s *Store) Prune(olderThan time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan)

	all, err := s.Query(time.Time{}, time.Time{})
	if err != nil {
		return 0, err
	}

	kept := make([]Measurement, 0, len(all))
	removed := 0
	for _, m := range all {
		ts, err := time.Parse(time.RFC3339, m.Timestamp)
		if err != nil {
			kept = append(kept, m)
			continue
		}
		if ts.Before(cutoff) {
			removed++
		} else {
			kept = append(kept, m)
		}
	}

	if removed == 0 {
		return 0, nil
	}

	f, err := os.Create(s.path)
	if err != nil {
		return 0, fmt.Errorf("telemetry: rewrite: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, m := range kept {
		data, err := json.Marshal(m)
		if err != nil {
			continue
		}
		w.Write(append(data, '\n'))
	}
	w.Flush()

	return removed, nil
}

// dirOf extracts the directory portion of a path.
func dirOf(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return "."
	}
	return p[:idx]
}
