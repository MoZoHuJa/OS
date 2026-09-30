package status

import (
        "encoding/json"
        "os"
        "path/filepath"
        "testing"
        "time"
)

// v18.8.2 P2: Unit tests for status.Read() stale detection
// (was: no tests — stale logic could regress silently)

func init() {
        // Override status file path for all tests in this package.
        // Use a fixed path (t.TempDir not available in init).
        testStatusFile = "/tmp/scarlix-test-host-status.json"
        os.Remove(testStatusFile)
}

func TestReadMissingFile(t *testing.T) {
        f := filepath.Join(t.TempDir(), "missing.json")
        testStatusFile = f
        defer func() { testStatusFile = "" }()
        // Read() returns err + zero-value HostStatus (Stale=false) for missing file.
        // ReadOrStale() is the wrapper that sets Stale=true on error — that's what
        // API handlers use. Test both behaviors.
        s, err := Read()
        if err == nil {
                t.Error("expected error for missing file, got nil")
        }
        // ReadOrStale should return Stale=true
        s = ReadOrStale()
        if !s.Stale {
                t.Error("ReadOrStale should return Stale=true for missing file")
        }
}

func TestReadCorruptJSON(t *testing.T) {
        dir := t.TempDir()
        f := filepath.Join(dir, "host-status.json")
        os.WriteFile(f, []byte("{corrupt json"), 0644)
        testStatusFile = f
        defer func() { testStatusFile = "" }()
        s, err := Read()
        if err == nil {
                t.Error("expected error for corrupt JSON")
        }
        if !s.Stale {
                t.Error("expected Stale=true for corrupt JSON")
        }
}

func TestReadFreshStatus(t *testing.T) {
        dir := t.TempDir()
        f := filepath.Join(dir, "host-status.json")
        now := time.Now().Format(time.RFC3339)
        data, _ := json.Marshal(HostStatus{
                Timestamp: now,
                Mode:      "ai",
        })
        os.WriteFile(f, data, 0644)
        testStatusFile = f
        defer func() { testStatusFile = "" }()
        s, err := Read()
        if err != nil {
                t.Fatalf("Read failed: %v", err)
        }
        if s.Stale {
                t.Error("fresh status should NOT be stale")
        }
        if s.Mode != "ai" {
                t.Errorf("expected mode 'ai', got %q", s.Mode)
        }
}

func TestReadOldStatusStale(t *testing.T) {
        dir := t.TempDir()
        f := filepath.Join(dir, "host-status.json")
        // 2 minutes ago = > 60s threshold = stale
        old := time.Now().Add(-2 * time.Minute).Format(time.RFC3339)
        data, _ := json.Marshal(HostStatus{
                Timestamp: old,
                Mode:      "ai",
        })
        os.WriteFile(f, data, 0644)
        testStatusFile = f
        defer func() { testStatusFile = "" }()
        s, err := Read()
        if err != nil {
                t.Fatalf("Read failed: %v", err)
        }
        if !s.Stale {
                t.Error("2-minute-old status should be stale (>60s threshold)")
        }
}

func TestReadFutureTimestampStale(t *testing.T) {
        dir := t.TempDir()
        f := filepath.Join(dir, "host-status.json")
        // 1 minute in the future = clock skew = stale
        future := time.Now().Add(time.Minute).Format(time.RFC3339)
        data, _ := json.Marshal(HostStatus{
                Timestamp: future,
                Mode:      "ai",
        })
        os.WriteFile(f, data, 0644)
        testStatusFile = f
        defer func() { testStatusFile = "" }()
        s, err := Read()
        if err != nil {
                t.Fatalf("Read failed: %v", err)
        }
        if !s.Stale {
                t.Error("future timestamp should be stale (clock skew protection)")
        }
}

func TestReadEmptyTimestampStale(t *testing.T) {
        dir := t.TempDir()
        f := filepath.Join(dir, "host-status.json")
        data, _ := json.Marshal(HostStatus{
                Timestamp: "",
                Mode:      "ai",
        })
        os.WriteFile(f, data, 0644)
        testStatusFile = f
        defer func() { testStatusFile = "" }()
        s, err := Read()
        if err != nil {
                t.Fatalf("Read failed: %v", err)
        }
        if !s.Stale {
                t.Error("empty timestamp should be stale")
        }
}

func TestReadOrStaleReturnsStaleOnError(t *testing.T) {
        testStatusFile = filepath.Join(t.TempDir(), "nonexistent.json")
        defer func() { testStatusFile = "" }()
        s := ReadOrStale()
        if !s.Stale {
                t.Error("ReadOrStale should return Stale=true on error")
        }
}
