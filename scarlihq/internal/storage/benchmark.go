// Package storage — the opt-in, bounded read benchmark (v19.7.0,
// roadmap §5.2 implementation rules 2-4).
//
// The benchmark is an OPERATOR DIAGNOSTIC, never part of discovery:
//
//   - RunReadBenchmark is not referenced by CollectDevices/
//     CollectHostResources (or any other discovery path); the only
//     in-repo caller is the scarlix-inventory CLI's --storage-benchmark
//     flag, which an operator must invoke explicitly with a target path.
//   - Bounds are enforced fail-closed: duration, test-file size, and
//     block size each carry a min/max; an option outside the bound is an
//     error, never a silent clamp (a silent clamp would let a caller
//     believe it measured something it did not).
//   - The benchmark WRITES one temporary file into the target directory
//     (a read benchmark needs data to read), reads it back sequentially,
//     then REMOVES it. The write is why the target choice is the
//     operator's responsibility: roadmap rule 4 — "do not benchmark a
//     mounted production model store without approval". The temp-file
//     name carries a .scarlix-bench- prefix so an interrupted run leaves
//     an identifiable artifact, and the measured READ phase happens after
//     an explicit non-uniform fill (so the read measures device transfer,
//     not a zero-page replay).
//   - Result honesty: sequential-read throughput is ONE measurement under
//     the recorded profile (block size, duration, path) — a data point,
//     never a guaranteed sustained rate (roadmap §5.2 rule 6). The
//     ReadProfile records the provenance; consumers (a future admission
//     gate) must apply their own conservative safety fraction.
//   - No cache dropping: the process cannot drop the page cache without
//     root. The benchmark WARMS the file (writes it, reads it once) and
//     then measures a second full pass — the steady-state page-cache
//     residency is part of what is measured. This limitation is stated in
//     the result Detail, not hidden: a cold-cache measurement requires
//     the operator-side diagnostic (echo 3 > drop_caches) and is out of
//     scope for an unprivileged library call.
package storage

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	mathrand "math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Benchmark bounds (roadmap §5.2 rule 3: bounded duration, bounded test
// file size, explicit target path). The ranges are deliberately tight —
// this is a diagnostic, not a load test.
const (
	MinBenchmarkDuration = 1 * time.Second
	MaxBenchmarkDuration = 30 * time.Second
	MinBenchmarkFileSize = 32 << 20 // 32 MiB
	MaxBenchmarkFileSize = 1 << 30  // 1 GiB
	MinBenchmarkBlock    = 4 << 10  // 4 KiB
	MaxBenchmarkBlock    = 8 << 20  // 8 MiB
)

// ErrBenchmarkOption wraps every out-of-bound option with a named,
// actionable message (fail-closed: the caller learns the bound it broke).
type benchmarkOptionError struct{ msg string }

func (e *benchmarkOptionError) Error() string { return e.msg }

func errBound(name string, got, min, max interface{}) error {
	return &benchmarkOptionError{fmt.Sprintf(
		"benchmark option %s out of bounds: got %v, allowed [%v, %v] — adjust the explicit request, bounds are never silently clamped",
		name, got, min, max)}
}

// ErrBenchmarkTarget is returned when the target path is not an existing
// directory. The target must be explicit (roadmap rule 3) and must be a
// directory (the temp file is created inside it — benchmarking a specific
// FILE is out of scope for v19.7.0).
var ErrBenchmarkTarget = errors.New("benchmark target must be an existing directory")

// ErrBenchmarkTargetDenied is returned when the target path lies under a
// well-known MODEL STORE root (v19.7.1, KPK-19.7.0-P2-05: "do not
// benchmark a mounted production model store without approval" — the
// refusal is the approval door). The default denylist is "/models" (the
// repo's canonical model store root, models.yaml model_path base);
// SCARLIHQ_BENCHMARK_DENY_ROOTS overrides it with a colon-separated list
// of absolute roots. An EXPLICIT EMPTY value (SCARLIHQ_BENCHMARK_DENY_ROOTS="")
// disables the denylist — the operator's deliberate, documented decision to
// benchmark a production store anyway.
var ErrBenchmarkTargetDenied = errors.New("benchmark target is under a well-known model store root")

// benchmarkDenyRoots resolves the denylist. Unset → the default ["/models"].
// An EXPLICIT empty value → nil (the operator's documented decision to
// disable the model-store refusal — the env knob is the approval door).
func benchmarkDenyRoots() []string {
	raw, ok := os.LookupEnv("SCARLIHQ_BENCHMARK_DENY_ROOTS")
	if !ok {
		return []string{"/models"}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil // explicit empty — denylist deliberately disabled
	}
	var roots []string
	for _, r := range strings.Split(raw, ":") {
		if r = strings.TrimSpace(r); r != "" {
			roots = append(roots, r)
		}
	}
	return roots
}

// targetUnderRoot reports whether the cleaned target path lies under root
// (or equals it). Both sides are evaluated through filepath.Clean; a
// symlinked target is resolved first when possible (a bind-mounted model
// store under an alias path is still a model store).
func targetUnderRoot(target, root string) bool {
	resolved := target
	if t, err := filepath.EvalSymlinks(target); err == nil {
		resolved = t
	}
	resolved = filepath.Clean(resolved)
	root = filepath.Clean(root)
	if resolved == root {
		return true
	}
	return strings.HasPrefix(resolved, root+string(filepath.Separator))
}

// checkBenchmarkTargetDenylist refuses targets under any denied root,
// naming the matched root and the override knob in the error.
func checkBenchmarkTargetDenylist(target string) error {
	for _, root := range benchmarkDenyRoots() {
		if targetUnderRoot(target, root) {
			return fmt.Errorf("%w: %s is under %s — a production model store is not benchmarked without approval; set SCARLIHQ_BENCHMARK_DENY_ROOTS to an explicit empty value to override deliberately (KPK-19.7.0-P2-05)",
				ErrBenchmarkTargetDenied, target, root)
		}
	}
	return nil
}

// BenchmarkOptions is the explicit, operator-supplied benchmark request.
// Zero values are NOT defaults: Duration and FileSize are REQUIRED — an
// unset option is an error (the operator chose the profile or did not
// benchmark at all; there is no silent default profile).
type BenchmarkOptions struct {
	// TargetPath — the directory to measure. REQUIRED, must exist.
	TargetPath string
	// Duration — the read-measurement window (applies to the sequential
	// phase AND, when requested, the random phase — each phase runs its
	// own full window). REQUIRED, [1s, 30s].
	Duration time.Duration
	// FileSize — the temp test file size. REQUIRED, [32MiB, 1GiB].
	FileSize int64
	// BlockSize — the sequential read block size. Optional; default
	// 1 MiB when 0. When set, [4KiB, 8MiB].
	BlockSize int
	// RandomReads — v19.8.0 (roadmap §8.6, SSD performance
	// qualification): ALSO run a random-read phase after the sequential
	// phase, populating RandomReadIOPS + the p50/p95/p99 latency
	// percentiles. Opt-in: a v19.7.0-shaped request (false) measures
	// sequential throughput only, byte-for-byte the v19.7.0 behavior.
	// The random phase reads the SAME warmed temp file at RandomBlock-
	// sized, fixed-seed pseudo-random offsets (synchronous, queue depth
	// 1 — the profile records it; a queued-depth sweep is a future
	// diagnostic, not silently implied).
	RandomReads bool
	// RandomBlock — the random-read block size. Optional; default 4 KiB
	// when 0 (the expert-paging-relevant size — roadmap §8.6: "random
	// read IOPS at relevant block sizes"). When set, [4KiB, 8MiB] — the
	// same bounds as the sequential block; an out-of-bound value is a
	// NAMED error, never a clamp.
	RandomBlock int
}

// BenchmarkResult carries the measurement + the honest caveats.
type BenchmarkResult struct {
	Profile ReadProfile
	// BytesRead — total bytes read during the measured window (the
	// throughput numerator).
	BytesRead int64
	// TempFile — the temp file path used (removed on success; on failure
	// the removal was attempted and the path is reported so the operator
	// can clean up the identifiable artifact).
	TempFile string
	// Detail — the provenance + limitations note (cache residency, warm
	// pass, profile bounds).
	Detail string
}

// benchFilePrefix marks every artifact this benchmark creates.
const benchFilePrefix = ".scarlix-bench-"

// RunReadBenchmark measures sequential-read throughput of TargetPath
// with the requested bounded profile, and returns the measurement with
// full provenance. The context bounds the WHOLE run (fill + warm + read);
// cancellation removes the temp file before returning.
//
// Fail-closed contract: any option out of bounds, any I/O failure, or a
// context cancellation returns an error and leaves no temp file behind
// (best-effort removal; a failed removal is reported inside the error).
func RunReadBenchmark(ctx context.Context, opts BenchmarkOptions) (*BenchmarkResult, error) {
	// --- option validation (fail-closed, named errors) ----------------------
	if opts.TargetPath == "" {
		return nil, fmt.Errorf("%w: empty target path", ErrBenchmarkTarget)
	}
	info, err := os.Stat(opts.TargetPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBenchmarkTarget, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %q is not a directory", ErrBenchmarkTarget, opts.TargetPath)
	}
	// v19.7.1 (KPK-19.7.0-P2-05): the well-known model-store roots are
	// refused by default — the operator must explicitly lift the denylist
	// to benchmark a production model store.
	if err := checkBenchmarkTargetDenylist(opts.TargetPath); err != nil {
		return nil, err
	}
	if opts.Duration < MinBenchmarkDuration || opts.Duration > MaxBenchmarkDuration {
		return nil, errBound("duration", opts.Duration, MinBenchmarkDuration, MaxBenchmarkDuration)
	}
	if opts.FileSize < MinBenchmarkFileSize || opts.FileSize > MaxBenchmarkFileSize {
		return nil, errBound("file_size", opts.FileSize, MinBenchmarkFileSize, MaxBenchmarkFileSize)
	}
	block := opts.BlockSize
	if block == 0 {
		block = 1 << 20 // 1 MiB default (only when UNSET — set values are validated)
	}
	if block < MinBenchmarkBlock || block > MaxBenchmarkBlock {
		return nil, errBound("block_size", block, MinBenchmarkBlock, MaxBenchmarkBlock)
	}
	// v19.8.0 (§8.6): the random-read block carries the same fail-closed
	// bounds discipline — a named error, never a silent clamp.
	randomBlock := opts.RandomBlock
	if randomBlock == 0 {
		randomBlock = 4 << 10 // 4 KiB default (only when UNSET — the expert-paging-relevant size)
	}
	if randomBlock < MinBenchmarkBlock || randomBlock > MaxBenchmarkBlock {
		return nil, errBound("random_block", randomBlock, MinBenchmarkBlock, MaxBenchmarkBlock)
	}
	// The context bounds the WHOLE run (fill + warm + measured read) —
	// an already-cancelled context refuses BEFORE anything is written
	// (KPK-19.7.0-P2-03: "cancel before fill starts → no temp file").
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("benchmark refused before start: %w", err)
	}

	// --- the identifiable temp artifact --------------------------------------
	rnd := make([]byte, 8)
	if _, err := cryptorand.Read(rnd); err != nil {
		return nil, fmt.Errorf("benchmark id unavailable (crypto/rand): %w", err)
	}
	profileID := hex.EncodeToString(rnd)
	tmp := filepath.Join(opts.TargetPath, benchFilePrefix+profileID)

	// removeTemp removes the artifact and RETURNS the failure (v19.7.1,
	// KPK-19.7.0-P2-03: a failed cleanup is reported to the caller in the
	// returned error — never only a stderr print the caller cannot see).
	removeTemp := func() error {
		if rmErr := os.Remove(tmp); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("benchmark temp file removal failed (artifact left at %s): %w", tmp, rmErr)
		}
		return nil
	}

	// Fill: non-uniform content so the read measures transfer, not a
	// zero-page replay. 1 MiB of random seed replicated to FileSize.
	//
	// KPK-19.7.0-P2-03: EXCLUSIVE creation (O_CREATE|O_EXCL) — a same-name
	// artifact can never be TRUNCATED-over, and the fill loop checks the
	// context BEFORE every chunk and BEFORE Sync, so a cancelled benchmark
	// stops writing promptly, cleans up, and returns the wrapped context
	// error (plus any cleanup failure).
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("benchmark temp file create failed: %w", err)
	}
	seed := make([]byte, 1<<20)
	if _, err := cryptorand.Read(seed); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("benchmark fill seed unavailable: %w", joinErr(removeTemp(), err))
	}
	var written int64
	for written < opts.FileSize {
		if cerr := ctx.Err(); cerr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("benchmark cancelled during fill at %d/%d bytes: %w", written, opts.FileSize, joinErr(removeTemp(), cerr))
		}
		n := int64(len(seed))
		if rem := opts.FileSize - written; rem < n {
			n = rem
		}
		if _, err := f.Write(seed[:n]); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("benchmark fill write failed at %d bytes: %w", written, joinErr(removeTemp(), err))
		}
		written += n
	}
	if cerr := ctx.Err(); cerr != nil {
		// Cancellation landed between the last chunk and the sync — the file
		// is complete but unflushed; do NOT sync it (the acceptance case
		// "cancellation immediately before sync → no sync of a fully filled
		// test file").
		_ = f.Close()
		return nil, fmt.Errorf("benchmark cancelled before sync after filling %d bytes: %w", written, joinErr(removeTemp(), cerr))
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("benchmark fill sync failed: %w", joinErr(removeTemp(), err))
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("benchmark fill close failed: %w", joinErr(removeTemp(), err))
	}

	// Warm pass: one full read so the page cache carries the file's
	// steady-state residency before measurement (documented limitation —
	// see package doc).
	if err := readAll(ctx, tmp, block); err != nil {
		return nil, fmt.Errorf("benchmark warm pass failed: %w", joinErr(removeTemp(), err))
	}

	// Measured pass: sequential reads for the requested window.
	start := nowFn()
	var total int64
	buf := make([]byte, block)
measure:
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("benchmark cancelled after %d bytes: %w", total, joinErr(removeTemp(), err))
		}
		rf, err := os.Open(tmp)
		if err != nil {
			return nil, fmt.Errorf("benchmark read open failed: %w", joinErr(removeTemp(), err))
		}
		for {
			if nowFn().Sub(start) >= opts.Duration {
				rf.Close()
				break measure
			}
			n, rerr := io.ReadFull(rf, buf)
			total += int64(n)
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break // wrap around: re-open and keep reading
			}
			if rerr != nil {
				rf.Close()
				return nil, fmt.Errorf("benchmark read failed after %d bytes: %w", total, joinErr(removeTemp(), rerr))
			}
		}
		rf.Close()
	}
	elapsed := nowFn().Sub(start)
	if elapsed <= 0 || total == 0 {
		return nil, fmt.Errorf("benchmark window degenerate (elapsed %v, read %d bytes) — no measurement recorded: %w", elapsed, total, removeTemp())
	}

	// --- the v19.8.0 random-read phase (roadmap §8.6) ------------------------
	//
	// SSD-backed expert paging is sensitive to RANDOM reads and latency,
	// not only advertised sequential MB/s — the qualification needs IOPS
	// and the p50/p95/p99 percentiles. The phase is OPT-IN
	// (BenchmarkOptions.RandomReads): a v19.7.0-shaped request never
	// enters it. The read pattern is a FIXED-SEED pseudo-random offset
	// sequence over the SAME warmed temp file (synchronous, queue depth
	// 1): deterministic, reproducible, and honestly recorded as a
	// single-queue-depth data point — never presented as a queued-depth
	// sweep.
	var randomProfile randomReadStats
	if opts.RandomReads {
		randomProfile, err = runRandomReadPhase(ctx, tmp, opts.FileSize, randomBlock, opts.Duration)
		if err != nil {
			return nil, fmt.Errorf("benchmark random-read phase failed: %w", joinErr(removeTemp(), err))
		}
	}

	if rerr := removeTemp(); rerr != nil {
		// The measurement stands; the cleanup failure is still reported
		// (never silently swallowed) — the operator learns the artifact path.
		return nil, fmt.Errorf("benchmark completed but cleanup failed: %w", rerr)
	}

	mbps := float64(total) / elapsed.Seconds() / 1e6
	profile := ReadProfile{
		SequentialReadMBps: mbps,
		BlockSizeBytes:     block,
		MeasuredAt:         start.UTC(),
		ProfileID:          profileID,
	}
	detail := fmt.Sprintf(
		"sequential read of %s, block %d bytes, window %s, %d bytes read; warm pass included (page-cache resident); one data point, not a guaranteed sustained rate; temp artifact removed",
		opts.TargetPath, block, opts.Duration.Round(time.Millisecond), total)
	if opts.RandomReads {
		profile.RandomReadIOPS = randomProfile.iops
		profile.RandomReadP50Ms = randomProfile.p50ms
		profile.RandomReadP95Ms = randomProfile.p95ms
		profile.RandomReadP99Ms = randomProfile.p99ms
		profile.RandomBlockBytes = randomBlock
		profile.QueueDepth = 1 // synchronous single reader — recorded, never implied
		detail = fmt.Sprintf(
			"%s; random-read phase: %d ops at block %d bytes over window %s (fixed-seed offsets, synchronous queue depth 1) — p50 %.3f ms / p95 %.3f ms / p99 %.3f ms, %.1f IOPS; one data point per phase, not a guaranteed sustained rate",
			detail, randomProfile.ops, randomBlock, opts.Duration.Round(time.Millisecond), randomProfile.p50ms, randomProfile.p95ms, randomProfile.p99ms, randomProfile.iops)
	}
	return &BenchmarkResult{
		Profile:   profile,
		BytesRead: total,
		TempFile:  tmp,
		Detail:    detail,
	}, nil
}

// randomReadStats is the random-read phase's measurement.
type randomReadStats struct {
	ops   int64
	iops  float64
	p50ms float64
	p95ms float64
	p99ms float64
}

// runRandomReadPhase measures random-read IOPS + latency percentiles at
// the given block size over the warmed temp file, for the requested
// window, with a FIXED-SEED offset generator (reproducible pattern — the
// seed is a named constant, not time-derived). Context cancellation
// aborts the phase (the caller folds the cleanup into the error).
func runRandomReadPhase(ctx context.Context, path string, fileSize int64, block int, window time.Duration) (randomReadStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return randomReadStats{}, err
	}
	defer f.Close()
	buf := make([]byte, block)
	// rand.NewSource(1) — the FIXED seed: identical requests produce the
	// identical offset sequence (deterministic diagnostics; the seed is
	// part of the measurement's provenance, documented here).
	rng := mathrand.New(mathrand.NewSource(1))
	start := nowFn()
	var ops int64
	latencies := make([]time.Duration, 0, 1<<16)
	maxOffset := fileSize - int64(block)
	if maxOffset < 0 {
		maxOffset = 0
	}
	for {
		if err := ctx.Err(); err != nil {
			return randomReadStats{}, err
		}
		if nowFn().Sub(start) >= window {
			break
		}
		off := int64(0)
		if maxOffset > 0 {
			off = rng.Int63n(maxOffset)
		}
		opStart := nowFn()
		_, rerr := f.ReadAt(buf, off)
		latencies = append(latencies, nowFn().Sub(opStart))
		if rerr != nil && rerr != io.EOF {
			return randomReadStats{}, rerr
		}
		ops++
		if ops >= 1<<26 { // 67M ops safety valve — a runaway window cannot loop forever
			break
		}
	}
	elapsed := nowFn().Sub(start)
	if elapsed <= 0 || ops == 0 {
		return randomReadStats{}, fmt.Errorf("random-read window degenerate (elapsed %v, %d ops) — no measurement recorded", elapsed, ops)
	}
	sortDurations(latencies)
	return randomReadStats{
		ops:   ops,
		iops:  float64(ops) / elapsed.Seconds(),
		p50ms: percentileMs(latencies, 0.50),
		p95ms: percentileMs(latencies, 0.95),
		p99ms: percentileMs(latencies, 0.99),
	}, nil
}

// sortDurations sorts latencies ascending (in place) for percentile
// evaluation.
func sortDurations(ds []time.Duration) { sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] }) }

// percentileMs evaluates the p-th percentile latency in milliseconds
// (nearest-rank method on the sorted slice — simple, deterministic,
// documented; 0 < p <= 1). An empty slice is the caller's degenerate
// window error, not a zero answer here.
func percentileMs(sorted []time.Duration, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p * float64(len(sorted))))
	if idx < 1 {
		idx = 1
	}
	if idx > len(sorted) {
		idx = len(sorted)
	}
	return float64(sorted[idx-1].Nanoseconds()) / 1e6
}

// readAll performs one full sequential read of path (the warm pass).
func readAll(ctx context.Context, path string, block int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, block)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, rerr := io.ReadFull(f, buf)
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// joinErr folds a cleanup failure into the primary error so BOTH reach
// the caller (KPK-19.7.0-P2-03: "if cleanup fails, return the cleanup
// failure alongside the cancellation error"). The primary error stays
// the outer one — errors.Is(err, context.Canceled) keeps working.
func joinErr(cleanupErr, primary error) error {
	if cleanupErr == nil {
		return primary
	}
	return fmt.Errorf("%v (cleanup also failed: %v)", primary, cleanupErr)
}
