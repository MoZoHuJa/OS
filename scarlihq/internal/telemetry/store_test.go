package telemetry

import (
        "encoding/json"
        "os"
        "path/filepath"
        "testing"
        "time"
)

func TestStore_AppendAndCount(t *testing.T) {
        tmp := t.TempDir()
        store := New(filepath.Join(tmp, "telemetry.jsonl"))

        m := Measurement{
                Timestamp:      time.Now().UTC().Format(time.RFC3339),
                GPUIndex:       0,
                GPUUtilPct:     45.2,
                GPUVRAMUsedMB:  8192,
                GPUVRAMTotalMB: 16384,
                GPUTempC:       65,
                Runtime:        "sglang",
                Model:          "qwen3-14b-awq",
                LatencyMs:      120.5,
                TokensPerSec:   85.3,
        }

        if err := store.Append(m); err != nil {
                t.Fatalf("Append failed: %v", err)
        }

        count, err := store.Count()
        if err != nil {
                t.Fatalf("Count failed: %v", err)
        }
        if count != 1 {
                t.Errorf("expected count 1, got %d", count)
        }
}

func TestStore_AppendAutoTimestamp(t *testing.T) {
        tmp := t.TempDir()
        store := New(filepath.Join(tmp, "tel.jsonl"))

        m := Measurement{} // no timestamp
        if err := store.Append(m); err != nil {
                t.Fatalf("Append failed: %v", err)
        }

        results, err := store.Query(time.Time{}, time.Time{})
        if err != nil {
                t.Fatalf("Query failed: %v", err)
        }
        if len(results) != 1 {
                t.Fatalf("expected 1 result, got %d", len(results))
        }
        if results[0].Timestamp == "" {
                t.Error("timestamp should be auto-populated")
        }
}

func TestStore_QueryEmpty(t *testing.T) {
        tmp := t.TempDir()
        store := New(filepath.Join(tmp, "nonexistent.jsonl"))

        results, err := store.Query(time.Time{}, time.Time{})
        if err != nil {
                t.Fatalf("Query on nonexistent should not error: %v", err)
        }
        if results == nil {
                t.Error("results should be empty slice, not nil")
        }
        if len(results) != 0 {
                t.Errorf("expected 0 results, got %d", len(results))
        }
}

func TestStore_QueryTimeRange(t *testing.T) {
        tmp := t.TempDir()
        store := New(filepath.Join(tmp, "tel.jsonl"))

        now := time.Now().UTC()
        old := now.Add(-2 * time.Hour)
        recent := now.Add(-5 * time.Minute)

        // Append 3 measurements: old, recent, now
        store.Append(Measurement{Timestamp: old.Format(time.RFC3339), GPUUtilPct: 10})
        store.Append(Measurement{Timestamp: recent.Format(time.RFC3339), GPUUtilPct: 20})
        store.Append(Measurement{Timestamp: now.Format(time.RFC3339), GPUUtilPct: 30})

        // Query last hour (should get recent + now, not old)
        from := now.Add(-1 * time.Hour)
        results, err := store.Query(from, time.Time{})
        if err != nil {
                t.Fatalf("Query failed: %v", err)
        }
        if len(results) != 2 {
                t.Errorf("expected 2 results in last hour, got %d", len(results))
        }
}

func TestStore_QuerySortedAscending(t *testing.T) {
        tmp := t.TempDir()
        store := New(filepath.Join(tmp, "tel.jsonl"))

        now := time.Now().UTC()
        t3 := now.Add(-1 * time.Minute)
        t1 := now.Add(-3 * time.Minute)
        t2 := now.Add(-2 * time.Minute)

        // Append out of order
        store.Append(Measurement{Timestamp: t3.Format(time.RFC3339)})
        store.Append(Measurement{Timestamp: t1.Format(time.RFC3339)})
        store.Append(Measurement{Timestamp: t2.Format(time.RFC3339)})

        results, err := store.Query(time.Time{}, time.Time{})
        if err != nil {
                t.Fatalf("Query failed: %v", err)
        }
        if len(results) != 3 {
                t.Fatalf("expected 3 results, got %d", len(results))
        }
        // Should be sorted: t1, t2, t3
        if results[0].Timestamp != t1.Format(time.RFC3339) {
                t.Errorf("first should be t1 (oldest), got %s", results[0].Timestamp)
        }
        if results[2].Timestamp != t3.Format(time.RFC3339) {
                t.Errorf("last should be t3 (newest), got %s", results[2].Timestamp)
        }
}

func TestStore_Prune(t *testing.T) {
        tmp := t.TempDir()
        store := New(filepath.Join(tmp, "tel.jsonl"))

        now := time.Now().UTC()
        old := now.Add(-2 * time.Hour)
        recent := now.Add(-5 * time.Minute)

        store.Append(Measurement{Timestamp: old.Format(time.RFC3339)})
        store.Append(Measurement{Timestamp: recent.Format(time.RFC3339)})

        // Prune entries older than 1 hour
        removed, err := store.Prune(1 * time.Hour)
        if err != nil {
                t.Fatalf("Prune failed: %v", err)
        }
        if removed != 1 {
                t.Errorf("expected 1 removed, got %d", removed)
        }

        count, _ := store.Count()
        if count != 1 {
                t.Errorf("after prune, count should be 1, got %d", count)
        }
}

func TestStore_CreatesParentDir(t *testing.T) {
        tmp := t.TempDir()
        nestedPath := filepath.Join(tmp, "subdir", "nested", "tel.jsonl")
        store := New(nestedPath)

        if err := store.Append(Measurement{}); err != nil {
                t.Fatalf("Append should create parent dirs, got: %v", err)
        }

        if _, err := os.Stat(nestedPath); err != nil {
                t.Errorf("file should exist at %s: %v", nestedPath, err)
        }
}

func TestStore_MalformedLinesSkipped(t *testing.T) {
        tmp := t.TempDir()
        path := filepath.Join(tmp, "tel.jsonl")

        // Write a file with mixed valid + malformed lines
        content := `{"timestamp":"2026-01-01T00:00:00Z","gpu_index":0}
this is not json
{"timestamp":"2026-01-01T01:00:00Z","gpu_index":1}
{"broken":}
`
        os.WriteFile(path, []byte(content), 0644)

        store := New(path)
        results, err := store.Query(time.Time{}, time.Time{})
        if err != nil {
                t.Fatalf("Query should not fail on malformed lines: %v", err)
        }
        if len(results) != 2 {
                t.Errorf("expected 2 valid results (skipping 2 malformed), got %d", len(results))
        }
}

func TestStore_DefaultPath(t *testing.T) {
        // Test that New("") uses the env var or default
        os.Setenv("SCARLIX_TELEMETRY_FILE", "/tmp/test-telemetry-env.jsonl")
        defer os.Unsetenv("SCARLIX_TELEMETRY_FILE")

        store := New("")
        if store.path != "/tmp/test-telemetry-env.jsonl" {
                t.Errorf("expected env var path, got %s", store.path)
        }
}

func TestMeasurement_JSONRoundTrip(t *testing.T) {
        m := Measurement{
                Timestamp:      "2026-10-06T10:00:00Z",
                GPUIndex:       0,
                GPUUtilPct:     55.5,
                GPUVRAMUsedMB:  12000,
                GPUVRAMTotalMB: 16384,
                GPUTempC:       72,
                Runtime:        "sglang",
                Model:          "qwen3-14b-awq",
                LatencyMs:      150.2,
                TokensPerSec:   92.1,
        }

        data, err := json.Marshal(m)
        if err != nil {
                t.Fatalf("marshal failed: %v", err)
        }

        var m2 Measurement
        if err := json.Unmarshal(data, &m2); err != nil {
                t.Fatalf("unmarshal failed: %v", err)
        }

        if m2.GPUUtilPct != m.GPUUtilPct {
                t.Errorf("GPUUtilPct mismatch: %f != %f", m2.GPUUtilPct, m.GPUUtilPct)
        }
        if m2.Runtime != m.Runtime {
                t.Errorf("Runtime mismatch: %s != %s", m2.Runtime, m.Runtime)
        }
}
