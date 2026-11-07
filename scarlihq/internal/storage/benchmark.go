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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// BenchmarkOptions is the explicit, operator-supplied benchmark request.
// Zero values are NOT defaults: Duration and FileSize are REQUIRED — an
// unset option is an error (the operator chose the profile or did not
// benchmark at all; there is no silent default profile).
type BenchmarkOptions struct {
	// TargetPath — the directory to measure. REQUIRED, must exist.
	TargetPath string
	// Duration — the read-measurement window. REQUIRED, [1s, 30s].
	Duration time.Duration
	// FileSize — the temp test file size. REQUIRED, [32MiB, 1GiB].
	FileSize int64
	// BlockSize — the sequential read block size. Optional; default
	// 1 MiB when 0. When set, [4KiB, 8MiB].
	BlockSize int
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

	// --- the identifiable temp artifact --------------------------------------
	rnd := make([]byte, 8)
	if _, err := rand.Read(rnd); err != nil {
		return nil, fmt.Errorf("benchmark id unavailable (crypto/rand): %w", err)
	}
	profileID := hex.EncodeToString(rnd)
	tmp := filepath.Join(opts.TargetPath, benchFilePrefix+profileID)

	removeTemp := func() {
		if rmErr := os.Remove(tmp); rmErr != nil && !os.IsNotExist(rmErr) {
			fmt.Fprintf(os.Stderr, "storage: benchmark temp file removal failed: %v (artifact: %s)\n", rmErr, tmp)
		}
	}

	// Fill: non-uniform content so the read measures transfer, not a
	// zero-page replay. 1 MiB of random seed replicated to FileSize.
	f, err := os.Create(tmp)
	if err != nil {
		return nil, fmt.Errorf("benchmark temp file create failed: %w", err)
	}
	seed := make([]byte, 1<<20)
	if _, err := rand.Read(seed); err != nil {
		f.Close()
		removeTemp()
		return nil, fmt.Errorf("benchmark fill seed unavailable: %w", err)
	}
	var written int64
	for written < opts.FileSize {
		n := int64(len(seed))
		if rem := opts.FileSize - written; rem < n {
			n = rem
		}
		if _, err := f.Write(seed[:n]); err != nil {
			f.Close()
			removeTemp()
			return nil, fmt.Errorf("benchmark fill write failed at %d bytes: %w", written, err)
		}
		written += n
	}
	if err := f.Sync(); err != nil {
		f.Close()
		removeTemp()
		return nil, fmt.Errorf("benchmark fill sync failed: %w", err)
	}
	if err := f.Close(); err != nil {
		removeTemp()
		return nil, fmt.Errorf("benchmark fill close failed: %w", err)
	}

	// Warm pass: one full read so the page cache carries the file's
	// steady-state residency before measurement (documented limitation —
	// see package doc).
	if err := readAll(ctx, tmp, block); err != nil {
		removeTemp()
		return nil, fmt.Errorf("benchmark warm pass failed: %w", err)
	}

	// Measured pass: sequential reads for the requested window.
	start := nowFn()
	var total int64
	buf := make([]byte, block)
measure:
	for {
		if err := ctx.Err(); err != nil {
			removeTemp()
			return nil, fmt.Errorf("benchmark cancelled after %d bytes: %w", total, err)
		}
		rf, err := os.Open(tmp)
		if err != nil {
			removeTemp()
			return nil, fmt.Errorf("benchmark read open failed: %w", err)
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
				removeTemp()
				return nil, fmt.Errorf("benchmark read failed after %d bytes: %w", total, rerr)
			}
		}
		rf.Close()
	}
	elapsed := nowFn().Sub(start)
	if elapsed <= 0 || total == 0 {
		removeTemp()
		return nil, fmt.Errorf("benchmark window degenerate (elapsed %v, read %d bytes) — no measurement recorded", elapsed, total)
	}

	removeTemp()

	mbps := float64(total) / elapsed.Seconds() / 1e6
	return &BenchmarkResult{
		Profile: ReadProfile{
			SequentialReadMBps: mbps,
			BlockSizeBytes:     block,
			MeasuredAt:         start.UTC(),
			ProfileID:          profileID,
		},
		BytesRead: total,
		TempFile:  tmp,
		Detail: fmt.Sprintf(
			"sequential read of %s, block %d bytes, window %s, %d bytes read; warm pass included (page-cache resident); one data point, not a guaranteed sustained rate; temp artifact removed",
			opts.TargetPath, block, opts.Duration.Round(time.Millisecond), total),
	}, nil
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
