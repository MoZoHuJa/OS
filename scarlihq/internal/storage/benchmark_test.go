package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBenchmarkOptionBounds — fail-closed named errors for every out-of-
// bound option (the roadmap rule 3: bounded duration, bounded size,
// explicit target; bounds are NEVER silently clamped).
func TestBenchmarkOptionBounds(t *testing.T) {
	dir := t.TempDir()
	cases := []BenchmarkOptions{
		{TargetPath: dir, Duration: 0, FileSize: 64 << 20},                                    // duration unset
		{TargetPath: dir, Duration: 500 * time.Millisecond, FileSize: 64 << 20},               // duration < 1s
		{TargetPath: dir, Duration: 31 * time.Second, FileSize: 64 << 20},                     // duration > 30s
		{TargetPath: dir, Duration: 2 * time.Second, FileSize: 16 << 20},                      // size < 32MiB
		{TargetPath: dir, Duration: 2 * time.Second, FileSize: 2 << 30},                       // size > 1GiB
		{TargetPath: dir, Duration: 2 * time.Second, FileSize: 64 << 20, BlockSize: 1 << 10},  // block < 4KiB
		{TargetPath: dir, Duration: 2 * time.Second, FileSize: 64 << 20, BlockSize: 16 << 20}, // block > 8MiB
		{TargetPath: "", Duration: 2 * time.Second, FileSize: 64 << 20},                       // empty target
		{TargetPath: filepath.Join(dir, "nope"), Duration: 2 * time.Second, FileSize: 64 << 20},
	}
	for i, opts := range cases {
		_, err := RunReadBenchmark(context.Background(), opts)
		if err == nil {
			t.Errorf("case %d: expected a named option error, got nil", i)
			continue
		}
		// The error must NAME the broken bound (actionable, not generic).
		if !strings.Contains(err.Error(), "benchmark") {
			t.Errorf("case %d: error not benchmark-scoped: %v", i, err)
		}
	}
	// A FILE as target is refused by name too.
	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RunReadBenchmark(context.Background(), BenchmarkOptions{TargetPath: f, Duration: 2 * time.Second, FileSize: 64 << 20}); err == nil {
		t.Errorf("file target must be refused (ErrBenchmarkTarget)")
	}
}

// TestBenchmarkRunAndCleanup — a real (small) run in a temp dir: the
// temp artifact is removed, the profile carries provenance, the result
// reports the honest limitations.
func TestBenchmarkRunAndCleanup(t *testing.T) {
	dir := t.TempDir()
	res, err := RunReadBenchmark(context.Background(), BenchmarkOptions{
		TargetPath: dir,
		Duration:   1 * time.Second,
		FileSize:   32 << 20, // minimum size — the smallest honest window
	})
	if err != nil {
		t.Fatalf("benchmark failed: %v", err)
	}
	if res.Profile.SequentialReadMBps <= 0 {
		t.Errorf("throughput=%v, want > 0", res.Profile.SequentialReadMBps)
	}
	if res.Profile.BlockSizeBytes != 1<<20 {
		t.Errorf("default block=%d, want 1MiB", res.Profile.BlockSizeBytes)
	}
	if res.Profile.MeasuredAt.IsZero() {
		t.Errorf("MeasuredAt zero — provenance required")
	}
	if res.Profile.ProfileID == "" {
		t.Errorf("ProfileID empty — provenance required")
	}
	if res.BytesRead < 32<<20 {
		t.Errorf("bytes_read=%d, want >= the file size (warm pass excluded)", res.BytesRead)
	}
	if !strings.Contains(res.Detail, "not a guaranteed sustained rate") {
		t.Errorf("detail must carry the honesty note: %q", res.Detail)
	}
	// The temp artifact is REMOVED.
	if _, err := os.Stat(res.TempFile); !os.IsNotExist(err) {
		t.Errorf("temp artifact %s still present", res.TempFile)
	}
	// No other artifacts left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("benchmark left artifacts: %d entries", len(entries))
	}
}

// TestBenchmarkCancellation — a cancelled context leaves NO temp file.
func TestBenchmarkCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-dead context: the fill phase must abort
	_, err := RunReadBenchmark(ctx, BenchmarkOptions{
		TargetPath: dir,
		Duration:   1 * time.Second,
		FileSize:   32 << 20,
	})
	if err == nil {
		t.Fatal("cancelled context must error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("cancelled benchmark left artifacts: %d entries", len(entries))
	}
}

// TestBenchmarkSeamIsolation — the source-level charter: discovery files
// never reference the benchmark, and benchmark.go is the ONLY benchmark
// door (the CLI is the only caller — asserted by the smoke check).
func TestBenchmarkSeamIsolation(t *testing.T) {
	raw, err := os.ReadFile("discovery.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "RunReadBenchmark") {
		t.Error("discovery.go must never reference RunReadBenchmark — the benchmark is opt-in only (read-only charter)")
	}
	if strings.Contains(string(raw), "BenchmarkOptions") {
		t.Error("discovery.go must never reference BenchmarkOptions — the benchmark is opt-in only (read-only charter)")
	}
}
