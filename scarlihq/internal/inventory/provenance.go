// Package inventory — v2 provenance-aware collectors (v19.7.1,
// KPK-19.7.0-P2-02).
//
// The v19.7.0 defect: BuildSnapshotV2 labelled every empty GPU result
// "empty" — collapsing "nvidia-smi not installed", "nvidia-smi failed
// (driver/daemon trouble)", and "ran successfully, reported zero GPUs"
// into one machine-readable status — and labelled the runtime source
// "ok" whenever the static configured catalog was non-empty, even when
// Docker could not be queried at all. A consumer could not distinguish
// "genuinely none" from "could not observe" — exactly the distinction
// the v2 provenance contract (`ok|partial|empty|unavailable`) exists to
// make.
//
// This file adds the provenance-aware v2 collectors:
//
//   - CollectGPUsWithStatus — binary missing / exec failed →
//     "unavailable" (state UNKNOWN, not "no GPUs"); ran clean with zero
//     rows → "empty" (an honest empty); malformed rows alongside valid
//     ones → "partial".
//   - CollectRuntimesWithStatus — the static configured-runtime catalog
//     is REPORTED as metadata, never as live observation; the Docker
//     observation is a separate fact: docker missing / every query
//     failed → "unavailable"; queried clean with zero running → "empty"
//     (confirmed stopped — distinct from daemon failure); some queries
//     failed → "partial".
//
// The FROZEN v1 collectors (CollectGPUs / CollectRuntimes) keep their
// exact v1 behavior (empty-but-non-nil slices, errors swallowed) — the
// provenance is a v2-only addition, and v1 JSON keys are unchanged.
//
// EXEC SEAMS: lookPathFn and runCmdFn are package-level function
// variables so tests can fake nvidia-smi/docker outputs without a real
// binary — the same seam discipline the storage package uses. These are
// the ONLY exec doors in this package; gpu.go and runtime.go route
// their commands through them.
package inventory

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// lookPathFn resolves a binary on PATH (exec seam — test injection).
var lookPathFn = exec.LookPath

// runCmdFn runs a bounded command and returns its stdout (exec seam —
// test injection). Production value: exec.CommandContext(...).Output().
var runCmdFn = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// gpuQueryFields is the shared nvidia-smi field list (the v1 collector's
// list, unchanged — v2 asks the same question and adds provenance).
const gpuQueryFields = "index,uuid,name,memory.total,memory.used,memory.free," +
	"utilization.gpu,temperature.gpu,power.draw,driver_version,compute_cap"

// CollectGPUsWithStatus is the v2 GPU collector: the frozen v1 GPU
// records PLUS the honest source status (KPK-19.7.0-P2-02).
func CollectGPUsWithStatus() ([]GPU, storage.SourceStatus) {
	rows, outcome := nvidiaSmiQueryEx(gpuQueryFields)
	switch outcome {
	case probeBinaryMissing:
		return []GPU{}, storage.SourceStatus{
			Component: "gpus",
			Source:    "nvidia-smi",
			Status:    "unavailable",
			Detail:    "nvidia-smi is not on PATH — no NVIDIA tooling on this host; GPU state is UNKNOWN (unavailable ≠ no GPUs)",
		}
	case probeExecFailed:
		return []GPU{}, storage.SourceStatus{
			Component: "gpus",
			Source:    "nvidia-smi",
			Status:    "unavailable",
			Detail:    "nvidia-smi exited non-zero, timed out, or was signalled (driver error or stuck daemon); GPU state is UNKNOWN (unavailable ≠ no GPUs)",
		}
	case probeNoOutput:
		return []GPU{}, storage.SourceStatus{
			Component: "gpus",
			Source:    "nvidia-smi",
			Status:    "empty",
			Detail:    "nvidia-smi ran successfully and reported zero GPUs (CPU-only host or no driver devices) — an honest empty, distinct from unavailable",
		}
	}

	gpus, malformed := parseGPURows(rows)
	if malformed > 0 {
		status := storage.SourceStatus{
			Component: "gpus",
			Source:    "nvidia-smi",
			Status:    "partial",
			Detail: fmt.Sprintf("%d GPUs observed; %d malformed nvidia-smi rows skipped (named degradation, never silent)",
				len(gpus), malformed),
		}
		if len(gpus) == 0 {
			status.Detail = fmt.Sprintf("every nvidia-smi row was malformed (%d skipped) — no GPU records parsed; state is unreliable", malformed)
		}
		return gpus, status
	}
	return gpus, storage.SourceStatus{
		Component: "gpus",
		Source:    "nvidia-smi",
		Status:    "ok",
		Detail:    fmt.Sprintf("%d GPUs observed", len(gpus)),
	}
}

// dockerOutcome distinguishes the docker observation results.
type dockerOutcome int

const (
	dockerMissing dockerOutcome = iota // docker binary not on PATH
	dockerFailed                       // query errored (daemon down/stuck/timeout)
	dockerNoMatch                      // query OK, no container matched
	dockerRunning                      // query OK, container is Up
	dockerDown                         // query OK, container exists but not Up
)

// CollectRuntimesWithStatus is the v2 runtime collector: the frozen v1
// Runtime records (static catalog metadata + live Running flags) PLUS
// the source status that SEPARATES the configured catalog from the live
// Docker observation (KPK-19.7.0-P2-02: a non-empty static registry is
// never labelled "ok" live observation).
func CollectRuntimesWithStatus() ([]Runtime, storage.SourceStatus) {
	out := make([]Runtime, 0, len(runtimeMetadata))
	missing, failed, queried, running := 0, 0, 0, 0
	for _, m := range runtimeMetadata {
		_, outcome := dockerInspectEx(m.Container)
		runningFlag := outcome == dockerRunning
		if runningFlag {
			running++
		}
		switch outcome {
		case dockerMissing:
			missing++
		case dockerFailed:
			failed++
		default:
			queried++
		}
		out = append(out, buildRuntimeRecord(m, runningFlag))
	}

	total := len(runtimeMetadata)
	base := fmt.Sprintf("%d runtimes configured (static catalog from the compose files); docker observation: %d/%d queried successfully, %d running",
		total, queried, total, running)

	switch {
	case missing == total:
		// Docker itself absent — the static catalog is reported as METADATA,
		// the live states are UNKNOWN. Not "empty" (that would claim a
		// successful observation of zero).
		return out, storage.SourceStatus{
			Component: "runtimes",
			Source:    "docker ps (CollectRuntimes)",
			Status:    "unavailable",
			Detail:    "docker is not on PATH — the static configured-runtime catalog is reported as metadata; live container states are UNKNOWN (unavailable ≠ no runtimes)",
		}
	case failed == total:
		return out, storage.SourceStatus{
			Component: "runtimes",
			Source:    "docker ps (CollectRuntimes)",
			Status:    "unavailable",
			Detail:    "every docker query failed (daemon down, stuck, or timed out) — live container states are UNKNOWN; the static catalog is metadata only",
		}
	case failed > 0 || missing > 0:
		return out, storage.SourceStatus{
			Component: "runtimes",
			Source:    "docker ps (CollectRuntimes)",
			Status:    "partial",
			Detail: fmt.Sprintf("%s; %d queries failed and %d saw no docker binary — mixed observation quality",
				base, failed, missing),
		}
	case running == 0:
		return out, storage.SourceStatus{
			Component: "runtimes",
			Source:    "docker ps (CollectRuntimes)",
			Status:    "empty",
			Detail:    fmt.Sprintf("%s — confirmed STOPPED (a successful observation of zero, distinct from daemon failure)", base),
		}
	default:
		return out, storage.SourceStatus{
			Component: "runtimes",
			Source:    "docker ps (CollectRuntimes)",
			Status:    "ok",
			Detail:    base,
		}
	}
}

// buildRuntimeRecord assembles the frozen v1 Runtime record from static
// metadata + the live running flag (shared by the v1 and v2 collectors —
// one record shape, two provenance policies).
func buildRuntimeRecord(m runtimeMeta, running bool) Runtime {
	gpuIDs := make([]string, 0, 1)
	if m.GPUIndex >= 0 {
		gpuIDs = append(gpuIDs, fmt.Sprintf("gpu.nvidia.%d", m.GPUIndex))
	}
	// Capabilities is a []string — always non-nil so JSON marshals as
	// [...] not null. Build a fresh copy so the caller can mutate freely.
	caps := make([]string, len(m.Capabilities))
	copy(caps, m.Capabilities)

	return Runtime{
		ID:           m.ID,
		Version:      imageTag(m.Image),
		Enabled:      true,
		Running:      running,
		Healthy:      false, // refined by CollectRuntimeHealth
		Protocol:     m.Protocol,
		Port:         m.Port,
		GPUIDs:       gpuIDs,
		Image:        m.Image,
		Capabilities: caps,
	}
}

// dockerInspectEx runs `docker ps --filter name=<container> --format {{.Status}}`
// and reports WHY it concluded what (the v2 provenance input). The FROZEN
// v1 dockerInspect keeps collapsing every failure to ("", nil).
func dockerInspectEx(containerName string) (status string, outcome dockerOutcome) {
	binary, lookErr := lookPathFn("docker")
	if lookErr != nil {
		// docker not installed — common in CI / dev sandboxes. The live
		// state is UNKNOWN (not "not running").
		return "", dockerMissing
	}

	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()

	out, runErr := runCmdFn(ctx, binary,
		"ps", "--filter", "name="+containerName,
		"--format", "{{.Status}}",
	)
	if runErr != nil {
		// Covers: non-zero exit (dockerd broken), context deadline (hang),
		// signal. The live state is UNKNOWN.
		return "", dockerFailed
	}

	line := strings.TrimSpace(string(out))
	if line == "" {
		// No container matched the name filter — not running.
		return "", dockerNoMatch
	}
	if strings.HasPrefix(line, "Up") {
		return "running", dockerRunning
	}
	// "Exited", "Restarting", "Created", "Paused", "Removing", "Dead" —
	// the container exists but isn't serving traffic. Treat as not running.
	return "down", dockerDown
}

// BackendPresence is the declared-vs-observed join for ONE manifest-
// declared backend (v19.7.1, KPK-19.7.0-P2-04 of the KpK audit: an
// operator seeing placement.eligible=true must NOT conclude the backend
// runs — eligibility answers METADATA completeness; presence answers
// LIVE RUNTIME observation, and the two facts travel separately).
type BackendPresence struct {
	// Backend — the declared backend slug (from the manifest's
	// runtime.supported_backends; an open set — a future backend like
	// "infer_ssd" declares before it exists and is honestly absent).
	Backend string `json:"backend"`
	// RuntimeUp — a live runtime with this ID was observed RUNNING in
	// this snapshot (docker observation; false when the runtime is
	// stopped, unobserved, or the backend is not a configured runtime).
	RuntimeUp bool `json:"runtime_up"`
	// RuntimeHealthy — the runtime's HTTP healthcheck passed (probed by
	// CollectRuntimeHealth in the same snapshot).
	RuntimeHealthy bool `json:"runtime_healthy"`
}

// backendPresenceFor computes the declared-vs-observed join for every
// declared backend against the snapshot's live runtimes (and their
// health states). Non-nil, deterministic order = declared order.
func backendPresenceFor(declared []string, runtimes []Runtime, healths []Health) []BackendPresence {
	healthByComponent := make(map[string]string, len(healths))
	for _, h := range healths {
		healthByComponent[h.Component] = h.State
	}
	up := make(map[string]bool, len(runtimes))
	for _, rt := range runtimes {
		up[rt.ID] = rt.Running
	}

	out := make([]BackendPresence, 0, len(declared))
	for _, b := range declared {
		out = append(out, BackendPresence{
			Backend:        b,
			RuntimeUp:      up[b],
			RuntimeHealthy: healthByComponent[b] == "healthy",
		})
	}
	return out
}
