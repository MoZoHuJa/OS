// Package inventory — GPU discovery and telemetry collector (v19.0.8).
//
// This file implements the runtime population of the GPU struct defined in
// types.go (v19.0.7). It is the Go-side counterpart of the bash `scarlix gpu
// status` subcommand — both invoke `nvidia-smi --query-gpu=...` with the same
// field list, but the Go collector emits the normalized `inventory.GPU` JSON
// shape that downstream consumers (ScarliHQ resource view v19.1.6, future
// compute fabric v19.2.x, and `scarlix --json gpu status`) require.
//
// Scope rules (v19.0.8 task 3-a):
//
//   - READ-ONLY. No GPU reset, no persistence, no mode switch.
//   - Graceful degradation: if nvidia-smi is missing/hangs/returns garbage,
//     the collector returns an empty (non-nil) `[]GPU{}` so callers can always
//     JSON-marshal to `[]` safely.
//   - Only NVIDIA is currently supported (Vendor == "nvidia"). AMD (rocm-smi)
//     and Intel (intel_gpu_top) collection is future work; the Vendor field
//     on the struct anticipates this.
//   - CUDA version is host-global, not per-GPU. CollectGPUs leaves the `CUDA`
//     field empty; a future `CollectSystemInfo()` (v19.1.x) will populate it
//     from the nvidia-smi banner ("CUDA Version: X.Y"). The Driver field IS
//     per-GPU in the struct shape (it just happens to be identical across
//     GPUs on a host — we query it once and apply to all to save a fork).
//
// Complements (does NOT replace) the existing internal/status package, which
// reads /var/lib/scarlix/host-status.json (the host-bridge → dashboard pipe).
// The status package is the lower-level read; inventory is the higher-level
// normalized view that v19.1.6+ will compose from multiple sources.
package inventory

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// nvidiaSmiTimeout bounds any nvidia-smi invocation. 5s is generous —
// nvidia-smi typically returns in <100ms on a healthy driver; a hang
// indicates a stuck driver (which the collector should not propagate).
const nvidiaSmiTimeout = 5 * time.Second

// nvidiaSmiQuery runs `nvidia-smi --query-gpu=<fields> --format=csv,noheader,nounits`
// and returns parsed CSV rows (one slice per GPU, fields in the order requested).
// Returns nil if nvidia-smi is unavailable, times out, exits non-zero, or
// produces no parseable output. Callers MUST treat nil as "no data" and
// surface an empty slice to their consumers (the []not-nil contract).
//
// The function is safe to call concurrently (each invocation builds its own
// context + exec.Cmd). It does not mutate any package-level state.
func nvidiaSmiQuery(fields string) [][]string {
	binary, err := exec.LookPath("nvidia-smi")
	if err != nil {
		// nvidia-smi not installed on this host (e.g. dev sandbox without
		// NVIDIA drivers). This is the common case for CI — return nil.
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), nvidiaSmiTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary,
		"--query-gpu="+fields,
		"--format=csv,noheader,nounits",
	)
	out, err := cmd.Output()
	if err != nil {
		// Covers: non-zero exit (driver error), context deadline (hang),
		// signal (SIGKILL'd). In all cases, "no data" is the safe answer.
		return nil
	}

	// nvidia-smi with --format=csv,noheader,nounits emits one line per GPU,
	// comma-separated, with no quoting. Trailing whitespace/newlines stripped.
	out = []byte(strings.TrimRight(string(out), "\n\r "))
	if len(out) == 0 {
		return nil
	}

	lines := strings.Split(string(out), "\n")
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// nvidia-smi never quotes CSV fields when --format=csv,noheader,nounits
		// is used, so a simple split is safe (encoding/csv would also work
		// but adds overhead for no benefit). Trailing-space-tolerant.
		raw := strings.Split(line, ",")
		row := make([]string, len(raw))
		for i, f := range raw {
			row[i] = strings.TrimSpace(f)
		}
		rows = append(rows, row)
	}

	if len(rows) == 0 {
		return nil
	}
	return rows
}

// CollectGPUs queries nvidia-smi and returns normalized GPU state.
//
// Returns an empty slice (not nil) if nvidia-smi is unavailable or reports
// no GPUs — callers can always JSON-marshal to `[]` safely (stability
// contract §5: "all arrays must serialize as [], never null").
//
// The 10 query fields match the bash CLI's `gpu status` pattern (scarlix L358):
//
//	index,name,memory.total,memory.used,memory.free,utilization.gpu,
//	temperature.gpu,power.draw,driver_version,compute_cap
//
// Per-row column order is fixed by the query string above; do not reorder
// the field parsing below without also reordering the query.
//
// Field population rules (see docs/SCARLIX_DATA_CONTRACTS.md §2):
//   - ID        = "gpu.nvidia.<index>" (stable identifier for cross-ref)
//   - Vendor    = "nvidia" (hardcoded — only NVIDIA supported in v19.0.8)
//   - PowerW    = 0.0 when nvidia-smi reports "[N/A]" (idle/PowerManagement)
//   - Driver    = the driver_version field (host-global; identical across GPUs
//     but taken per-row so future heterogeneous-driver setups still work)
//   - CUDA      = "" (empty — host-global property; future CollectSystemInfo
//     will populate from nvidia-smi banner. Documented here so callers don't
//     expect per-GPU CUDA.)
//   - Healthy   = true (placeholder; CollectGPUHealth refines)
//
// Parse-error policy: if a single field fails to parse (e.g. nvidia-smi
// emitted a malformed value), that GPU is skipped and a warning is logged
// to stderr — but the rest of the GPUs are still returned.
func CollectGPUs() []GPU {
	const fields = "index,name,memory.total,memory.used,memory.free," +
		"utilization.gpu,temperature.gpu,power.draw,driver_version,compute_cap"

	rows := nvidiaSmiQuery(fields)
	if rows == nil {
		// nvidia-smi missing, hung, or empty — return empty slice (not nil).
		return []GPU{}
	}

	gpus := make([]GPU, 0, len(rows))
	for _, row := range rows {
		// Each row must have at least 10 columns; if not, skip with a stderr
		// note (don't crash the whole collector for one malformed GPU).
		if len(row) < 10 {
			fmt.Fprintf(os.Stderr, "inventory: skipping malformed nvidia-smi row (got %d fields, want 10): %v\n", len(row), row)
			continue
		}

		idx, err := strconv.Atoi(row[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "inventory: skipping GPU row with bad index %q: %v\n", row[0], err)
			continue
		}

		// VRAM / util / temp are all integer fields in nvidia-smi output
		// (no decimals because we passed --format=...,nounits).
		vramTotal := atoiOr(row[2], 0, "memory.total", idx)
		vramUsed := atoiOr(row[3], 0, "memory.used", idx)
		vramFree := atoiOr(row[4], 0, "memory.free", idx)
		util := atoiOr(row[5], 0, "utilization.gpu", idx)
		temp := atoiOr(row[6], 0, "temperature.gpu", idx)

		// power.draw can be "[N/A]" on idle GPUs (PM disabled) — normalise.
		power := parsePowerW(row[7], idx)

		// driver_version is identical across GPUs on a host, but we take the
		// per-row value rather than caching because future heterogeneous-driver
		// setups (multi-NVIDIA-driver MIG) could differ.
		driver := row[8]
		computeCap := row[9]

		gpus = append(gpus, GPU{
			ID:             fmt.Sprintf("gpu.nvidia.%d", idx),
			Index:          idx,
			Vendor:         "nvidia",
			Name:           row[1],
			VRAMTotalMB:    vramTotal,
			VRAMUsedMB:     vramUsed,
			VRAMFreeMB:     vramFree,
			UtilizationPct: util,
			TemperatureC:   temp,
			PowerW:         power,
			Driver:         driver,
			CUDA:           "", // host-global — see package doc; future CollectSystemInfo.
			ComputeCap:     computeCap,
			Healthy:        true, // refined by CollectGPUHealth
		})
	}

	return gpus
}

// CollectGPUHealth derives a Health entry for each GPU in gpus and mutates
// the `Healthy` field on each GPU to match the derived state.
//
// Health rule (v19.0.8):
//
//	"healthy"   if VRAMTotalMB > 0 AND UtilizationPct >= 0
//	            (nvidia-smi reported a usable VRAM size; util is a sane
//	            unsigned value — nvidia-smi never reports negative util,
//	            so this is mostly a paranoia check.)
//	"unhealthy" otherwise — typically VRAMTotalMB == 0, which means
//	            nvidia-smi could not read the BAR1 / FB memory info
//	            (driver issue or GPU is in a reset state).
//
// Returns []Health (never nil) — one entry per input GPU, in the same order.
// Component identifier matches GPU.ID ("gpu.nvidia.0") so scheduler
// health lookups via s.health[gpu.ID] work correctly.
// v19.1.15 P1-3: Was fmt.Sprintf("gpu.%d", gpus[i].Index) → mismatch
// with gpu.ID ("gpu.nvidia.0") → health never matched → unhealthy GPUs
// could be selected by scheduler.
// types.go: "gpu.0"). CheckedAt is RFC 3339 UTC.
//
// Mutation contract: the input slice's backing array IS modified in place
// (Go slice semantics — slice header passed by value, backing array shared).
// Callers that want to preserve the original Healthy values should pass a copy.
//
// This is a quick heuristic, not a driver health probe. A future version may
// probe ECC error counts, XID events from the kernel log, or nvidia-smi's
// --query-gpu=ecc.errors.uncorrected.aggregate.total — but for v19.0.8 we
// keep it simple and predictable.
func CollectGPUHealth(gpus []GPU) []Health {
	healths := make([]Health, 0, len(gpus))
	now := time.Now().UTC().Format(time.RFC3339)

	for i := range gpus {
		var state, msg string
		if gpus[i].VRAMTotalMB > 0 && gpus[i].UtilizationPct >= 0 {
			state = "healthy"
			// empty message — Health.Message has `omitempty`, so it is
			// omitted from JSON entirely when state is healthy.
		} else {
			state = "unhealthy"
			msg = "VRAM or utilization out of range"
		}
		healths = append(healths, Health{
			Component: gpus[i].ID, // v19.1.15 P1-3: use gpu.ID ("gpu.nvidia.0") not "gpu.%d"
			State:     state,
			Message:   msg,
			CheckedAt: now,
		})
		// Mirror state back into the GPU slice so callers using either API
		// (the []GPU or the []Health) see consistent Healthy values. The
		// mutation is visible to the caller because we share the backing
		// array (Go slice semantics).
		gpus[i].Healthy = (state == "healthy")
	}

	return healths
}

// ----- internal helpers ----------------------------------------------------

// atoiOr parses s to int, returning fallback on error and logging a warning
// to stderr with the field name + GPU index for diagnosis. Treats "[N/A]"
// and "N/A" (nvidia-smi's "value not available" markers) as the fallback.
func atoiOr(s string, fallback int, fieldName string, gpuIdx int) int {
	s = strings.TrimSpace(s)
	if s == "" || s == "[N/A]" || s == "N/A" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inventory: GPU %d field %s parse error %q: %v — using %d\n",
			gpuIdx, fieldName, s, err, fallback)
		return fallback
	}
	return v
}

// parsePowerW parses a power.draw value, returning 0.0 for "[N/A]" / "N/A" /
// empty (nvidia-smi emits these on idle GPUs or when PowerManagement is off).
func parsePowerW(s string, gpuIdx int) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "[N/A]" || s == "N/A" {
		return 0.0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inventory: GPU %d power.draw parse error %q: %v — using 0.0\n",
			gpuIdx, s, err)
		return 0.0
	}
	return v
}
