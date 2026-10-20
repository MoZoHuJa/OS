package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/gaming"
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

// v19.3.1: gaming inventory parse tests (Gaming Stack Integration Plan,
// Phase 4). The host bridge publishes a "gaming" array; pre-19.3.1 status
// files have no such field and must keep parsing with an empty inventory.

func TestReadGamingField(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "host-status.json")
	now := time.Now().Format(time.RFC3339)
	data := `{
                "timestamp": "` + now + `",
                "version": "19.3.1",
                "mode": "ai",
                "gpus": [],
                "containers": [],
                "gaming": [
                        {
                                "id": "steam",
                                "name": "Steam",
                                "kind": "launcher",
                                "source": "pacman",
                                "installed": true,
                                "version": "1.0.0.87-3",
                                "running": false,
                                "health": "not_applicable",
                                "optional": false,
                                "last_checked": "` + now + `"
                        },
                        {
                                "id": "heroic-games-launcher-bin",
                                "name": "Heroic Games Launcher",
                                "kind": "launcher",
                                "source": "aur",
                                "installed": false,
                                "running": false,
                                "health": "not_installed",
                                "optional": true,
                                "last_checked": "` + now + `"
                        },
                        {
                                "id": "sunshine",
                                "name": "Sunshine (native, game mode)",
                                "kind": "streaming",
                                "source": "systemd",
                                "installed": true,
                                "running": true,
                                "health": "running",
                                "optional": true,
                                "last_checked": "` + now + `"
                        }
                ]
        }`
	os.WriteFile(f, []byte(data), 0644)
	testStatusFile = f
	defer func() { testStatusFile = "" }()
	s, err := Read()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if len(s.Gaming) != 3 {
		t.Fatalf("gaming entries = %d, want 3", len(s.Gaming))
	}
	steam := s.Gaming[0]
	if steam.ID != "steam" || !steam.Installed || steam.Version != "1.0.0.87-3" ||
		steam.Kind != gaming.KindLauncher || steam.Source != gaming.SourcePacman ||
		steam.Health != gaming.HealthNotApplicable || steam.Optional {
		t.Fatalf("steam entry mismatch: %+v", steam)
	}
	heroic := s.Gaming[1]
	if heroic.Installed || heroic.Health != gaming.HealthNotInstalled || !heroic.Optional {
		t.Fatalf("heroic entry mismatch: %+v", heroic)
	}
	sunshine := s.Gaming[2]
	if !sunshine.Running || sunshine.Health != gaming.HealthRunning || sunshine.Source != gaming.SourceSystemd {
		t.Fatalf("sunshine entry mismatch: %+v", sunshine)
	}
}

func TestReadLegacyStatusWithoutGamingField(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "host-status.json")
	now := time.Now().Format(time.RFC3339)
	// Exactly the pre-19.3.1 shape (no "gaming" key).
	data := `{
                "timestamp": "` + now + `",
                "version": "19.2.13",
                "mode": "game",
                "gpus": [],
                "containers": [{"name": "sunshine-tv", "status": "Up 2 hours", "ports": ""}]
        }`
	os.WriteFile(f, []byte(data), 0644)
	testStatusFile = f
	defer func() { testStatusFile = "" }()
	s, err := Read()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if s.Gaming != nil {
		t.Fatalf("legacy status should parse with nil Gaming, got %+v", s.Gaming)
	}
	if len(s.Containers) != 1 || s.Containers[0].Name != "sunshine-tv" {
		t.Fatalf("legacy containers mismatch: %+v", s.Containers)
	}
}
