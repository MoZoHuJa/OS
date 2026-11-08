package storage

import (
	"context"
	"errors"
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

// errAfterN is a deterministic context fake for KPK-19.7.0-P2-03: Err()
// returns nil for the first liveCalls calls, then the planted error on
// every subsequent call — a cancellation that lands mid-fill WITHOUT
// timing dependencies on disk speed.
type errAfterN struct {
	liveCalls int
	err       error
	fired     bool
}

func (c *errAfterN) Deadline() (deadline time.Time, ok bool) { return time.Time{}, false }
func (c *errAfterN) Done() <-chan struct{}                   { return nil }
func (c *errAfterN) Value(key any) any                       { return nil }
func (c *errAfterN) Err() error {
	if c.fired {
		return c.err
	}
	c.liveCalls--
	if c.liveCalls <= 0 {
		c.fired = true
		return c.err
	}
	return nil
}

// TestBenchmarkCancelDuringFill (KPK-19.7.0-P2-03 acceptance): a
// cancellation landing AFTER the first fill chunk exits PROMPTLY, removes
// the temp file, and returns an error wrapping context.Canceled.
func TestBenchmarkCancelDuringFill(t *testing.T) {
	dir := t.TempDir()
	// Err() call sequence: #1 pre-start, #2 first-chunk guard (one MiB
	// written), #3 second-chunk guard → cancelled. 32 MiB file, 1 MiB
	// chunks — cancellation fires ~1-2 MiB into a 32 MiB fill.
	ctx := &errAfterN{liveCalls: 2, err: context.Canceled}
	_, err := RunReadBenchmark(ctx, BenchmarkOptions{
		TargetPath: dir,
		Duration:   1 * time.Second,
		FileSize:   32 << 20,
	})
	if err == nil {
		t.Fatal("mid-fill cancellation must error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error must wrap context.Canceled, got: %v", err)
	}
	if !strings.Contains(err.Error(), "cancelled during fill") {
		t.Errorf("error must name the fill phase, got: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("mid-fill-cancelled benchmark left artifacts: %d entries (prompt cleanup required — up to 1 GiB must not be written/synced after cancellation)", len(entries))
	}
}

// TestBenchmarkDeadlineDuringFill (KPK-19.7.0-P2-03 acceptance): a
// deadline expiring during the fill phase behaves like cancellation —
// prompt exit + cleanup + errors.Is(context.DeadlineExceeded).
func TestBenchmarkDeadlineDuringFill(t *testing.T) {
	dir := t.TempDir()
	ctx := &errAfterN{liveCalls: 2, err: context.DeadlineExceeded}
	_, err := RunReadBenchmark(ctx, BenchmarkOptions{
		TargetPath: dir,
		Duration:   1 * time.Second,
		FileSize:   32 << 20,
	})
	if err == nil {
		t.Fatal("mid-fill deadline must error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error must wrap context.DeadlineExceeded, got: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("deadline-expired benchmark left artifacts: %d entries", len(entries))
	}
}

// TestBenchmarkExclusiveCreateSourceLevel (KPK-19.7.0-P2-03): the temp
// artifact is created EXCLUSIVELY (O_CREATE|O_EXCL — never truncating an
// existing same-name file) and the fill loop guards the context BEFORE
// each write chunk. Source-level assertion (the repo's "prose cannot
// mask code" discipline — the creation mode is not observable through
// the public API without a predictable random ID).
func TestBenchmarkExclusiveCreateSourceLevel(t *testing.T) {
	raw, err := os.ReadFile("benchmark.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, "os.O_CREATE|os.O_EXCL|os.O_WRONLY") {
		t.Error("the temp artifact must be created with O_CREATE|O_EXCL — a same-name artifact can never be truncated over")
	}
	fillIdx := strings.Index(src, "for written < opts.FileSize {")
	writeIdx := strings.Index(src, "f.Write(seed[:n])")
	guardIdx := strings.Index(src, "if cerr := ctx.Err(); cerr != nil {")
	if fillIdx < 0 || writeIdx < 0 || guardIdx < 0 {
		t.Fatal("fill loop structure not found")
	}
	if !(guardIdx > fillIdx && guardIdx < writeIdx) {
		t.Error("the fill loop must check ctx.Err() BEFORE the write call (cancellation is honored during the most write-intensive phase)")
	}
}

// TestBenchmarkTargetUnderRoot — the denylist path matcher: prefix
// matching respects path boundaries ("/modelsx" is NOT under "/models").
func TestBenchmarkTargetUnderRoot(t *testing.T) {
	cases := []struct {
		target, root string
		want         bool
	}{
		{"/models", "/models", true},
		{"/models/", "/models", true},
		{"/models/qwen3-14b", "/models", true},
		{"/modelsx", "/models", false},
		{"/modelsx/qwen", "/models", false},
		{"/var/tmp", "/models", false},
		{"/", "/models", false},
		{"/srv/models", "/models", false},
	}
	for _, c := range cases {
		if got := targetUnderRoot(c.target, c.root); got != c.want {
			t.Errorf("targetUnderRoot(%q, %q)=%v, want %v", c.target, c.root, got, c.want)
		}
	}
}

// TestBenchmarkDenylist (KPK-19.7.0-P2-05): a target under a denied
// model-store root is REFUSED with the named error; an explicit empty
// denylist env lifts the refusal (the operator's approval door).
func TestBenchmarkDenylist(t *testing.T) {
	root := t.TempDir() // stands in for the model store root
	target := filepath.Join(root, "qwen")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SCARLIHQ_BENCHMARK_DENY_ROOTS", root)
	_, err := RunReadBenchmark(context.Background(), BenchmarkOptions{
		TargetPath: target,
		Duration:   1 * time.Second,
		FileSize:   32 << 20,
	})
	if !errors.Is(err, ErrBenchmarkTargetDenied) {
		t.Fatalf("target under the denied root must be refused with ErrBenchmarkTargetDenied, got: %v", err)
	}
	entries, _ := os.ReadDir(target)
	if len(entries) != 0 {
		t.Errorf("refused benchmark left artifacts: %d entries", len(entries))
	}

	// The explicit empty override is the approval door — the benchmark
	// then runs (and cleans up) normally.
	t.Setenv("SCARLIHQ_BENCHMARK_DENY_ROOTS", "")
	res, err := RunReadBenchmark(context.Background(), BenchmarkOptions{
		TargetPath: target,
		Duration:   1 * time.Second,
		FileSize:   32 << 20,
	})
	if err != nil {
		t.Fatalf("explicit empty denylist must allow the benchmark: %v", err)
	}
	if _, err := os.Stat(res.TempFile); !os.IsNotExist(err) {
		t.Errorf("temp artifact %s still present", res.TempFile)
	}
}

// TestBenchmarkDenylistDefaultRoots: unset env → the default /models
// root; explicit empty → nil (disabled).
func TestBenchmarkDenylistDefaultRoots(t *testing.T) {
	t.Setenv("SCARLIHQ_BENCHMARK_DENY_ROOTS", "")
	if got := benchmarkDenyRoots(); got != nil {
		t.Errorf("explicit empty denylist must disable the refusal, got %v", got)
	}
	// unset via a subprocess-free trick: os.Unsetenv + LookupEnv
	os.Unsetenv("SCARLIHQ_BENCHMARK_DENY_ROOTS")
	got := benchmarkDenyRoots()
	if len(got) != 1 || got[0] != "/models" {
		t.Errorf("unset denylist must default to /models, got %v", got)
	}
	t.Setenv("SCARLIHQ_BENCHMARK_DENY_ROOTS", "/a:/b")
	got = benchmarkDenyRoots()
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Errorf("colon-separated denylist parse failed, got %v", got)
	}
}
