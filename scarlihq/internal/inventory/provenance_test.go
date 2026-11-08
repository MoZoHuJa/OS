// Package inventory tests — the v2 provenance collectors (v19.7.1,
// KPK-19.7.0-P2-02): empty and unavailable are DIFFERENT facts, and the
// static runtime catalog is never labelled live observation.
package inventory

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// withProbeSeams swaps the exec seams for deterministic fakes.
func withProbeSeams(t *testing.T, look func(name string) (string, error), run func(ctx context.Context, name string, args ...string) ([]byte, error)) {
	t.Helper()
	origLook, origRun := lookPathFn, runCmdFn
	lookPathFn = look
	runCmdFn = run
	t.Cleanup(func() { lookPathFn, runCmdFn = origLook, origRun })
}

func TestCollectGPUsWithStatus_BinaryMissing(t *testing.T) {
	withProbeSeams(t,
		func(name string) (string, error) { return "", os.ErrNotExist },
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			t.Fatal("runCmd must not be called when the binary is missing")
			return nil, nil
		})
	gpus, status := CollectGPUsWithStatus()
	if len(gpus) != 0 || gpus == nil {
		t.Fatalf("gpus=%v, want empty non-nil", gpus)
	}
	if status.Status != "unavailable" {
		t.Errorf("status=%q, want unavailable (no nvidia-smi ≠ no GPUs)", status.Status)
	}
	if !strings.Contains(status.Detail, "UNKNOWN") {
		t.Errorf("detail must state the state is unknown: %q", status.Detail)
	}
}

func TestCollectGPUsWithStatus_ExecFailed(t *testing.T) {
	withProbeSeams(t,
		func(name string) (string, error) { return "/usr/bin/nvidia-smi", nil },
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("exit status 1") // driver error / timeout / signal
		})
	gpus, status := CollectGPUsWithStatus()
	if len(gpus) != 0 {
		t.Fatalf("gpus=%d, want 0", len(gpus))
	}
	if status.Status != "unavailable" {
		t.Errorf("status=%q, want unavailable (probe failure ≠ no GPUs)", status.Status)
	}
}

func TestCollectGPUsWithStatus_HonestEmpty(t *testing.T) {
	withProbeSeams(t,
		func(name string) (string, error) { return "/usr/bin/nvidia-smi", nil },
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(""), nil // ran successfully, reported nothing
		})
	gpus, status := CollectGPUsWithStatus()
	if len(gpus) != 0 {
		t.Fatalf("gpus=%d, want 0", len(gpus))
	}
	if status.Status != "empty" {
		t.Errorf("status=%q, want empty (a successful observation of zero — distinct from unavailable)", status.Status)
	}
}

func TestCollectGPUsWithStatus_PartialRows(t *testing.T) {
	goodRow := "0, GPU-uuid-0, RTX 5060 Ti, 16384, 13600, 2784, 42, 68, 165.5, 570.86.15, 12.0"
	badRow := "garbage" // < 11 fields
	withProbeSeams(t,
		func(name string) (string, error) { return "/usr/bin/nvidia-smi", nil },
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(goodRow + "\n" + badRow + "\n"), nil
		})
	gpus, status := CollectGPUsWithStatus()
	if len(gpus) != 1 {
		t.Fatalf("gpus=%d, want 1 (the valid row survives; the malformed one is counted)", len(gpus))
	}
	if status.Status != "partial" {
		t.Errorf("status=%q, want partial (one malformed row alongside a valid one)", status.Status)
	}
	if !strings.Contains(status.Detail, "1 malformed") {
		t.Errorf("detail must count the malformed rows: %q", status.Detail)
	}
}

func TestCollectGPUsWithStatus_OK(t *testing.T) {
	row := "0, GPU-uuid-0, RTX 5060 Ti, 16384, 13600, 2784, 42, 68, 165.5, 570.86.15, 12.0"
	withProbeSeams(t,
		func(name string) (string, error) { return "/usr/bin/nvidia-smi", nil },
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(row + "\n"), nil
		})
	gpus, status := CollectGPUsWithStatus()
	if len(gpus) != 1 {
		t.Fatalf("gpus=%d, want 1", len(gpus))
	}
	if status.Status != "ok" {
		t.Errorf("status=%q, want ok", status.Status)
	}
}

func TestCollectRuntimesWithStatus_DockerMissing(t *testing.T) {
	withProbeSeams(t,
		func(name string) (string, error) { return "", os.ErrNotExist },
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			t.Fatal("runCmd must not be called when docker is missing")
			return nil, nil
		})
	runtimes, status := CollectRuntimesWithStatus()
	if len(runtimes) != len(runtimeMetadata) {
		t.Fatalf("runtimes=%d, want the full static catalog (%d)", len(runtimes), len(runtimeMetadata))
	}
	for _, rt := range runtimes {
		if rt.Running {
			t.Errorf("runtime %s Running=true with docker missing — must be false (unknown, reported as not running)", rt.ID)
		}
	}
	if status.Status != "unavailable" {
		t.Errorf("status=%q, want unavailable (docker missing ≠ no runtimes; the catalog is metadata)", status.Status)
	}
	if !strings.Contains(status.Detail, "static") {
		t.Errorf("detail must name the static catalog as metadata: %q", status.Detail)
	}
}

func TestCollectRuntimesWithStatus_DaemonFailed(t *testing.T) {
	withProbeSeams(t,
		func(name string) (string, error) { return "/usr/bin/docker", nil },
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("daemon not running") // every query fails
		})
	runtimes, status := CollectRuntimesWithStatus()
	if len(runtimes) != len(runtimeMetadata) {
		t.Fatalf("runtimes=%d, want the static catalog", len(runtimes))
	}
	if status.Status != "unavailable" {
		t.Errorf("status=%q, want unavailable (daemon failure — live states unknown)", status.Status)
	}
}

func TestCollectRuntimesWithStatus_EmptyObservation(t *testing.T) {
	withProbeSeams(t,
		func(name string) (string, error) { return "/usr/bin/docker", nil },
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(""), nil // query OK, no container matched
		})
	runtimes, status := CollectRuntimesWithStatus()
	if len(runtimes) != len(runtimeMetadata) {
		t.Fatalf("runtimes=%d, want the static catalog", len(runtimes))
	}
	if status.Status != "empty" {
		t.Errorf("status=%q, want empty (successful observation of zero running — distinct from daemon failure)", status.Status)
	}
	if !strings.Contains(status.Detail, "STOPPED") {
		t.Errorf("detail must say confirmed-stopped: %q", status.Detail)
	}
}

func TestCollectRuntimesWithStatus_OK(t *testing.T) {
	withProbeSeams(t,
		func(name string) (string, error) { return "/usr/bin/docker", nil },
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// Every configured container Up.
			return []byte("Up 2 hours"), nil
		})
	runtimes, status := CollectRuntimesWithStatus()
	if len(runtimes) != len(runtimeMetadata) {
		t.Fatalf("runtimes=%d", len(runtimes))
	}
	for _, rt := range runtimes {
		if !rt.Running {
			t.Errorf("runtime %s Running=false, want true", rt.ID)
		}
	}
	if status.Status != "ok" {
		t.Errorf("status=%q, want ok", status.Status)
	}
}

func TestCollectRuntimesWithStatus_Partial(t *testing.T) {
	withProbeSeams(t,
		func(name string) (string, error) { return "/usr/bin/docker", nil },
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// The sglang container (first queried) fails; the rest are Up.
			if strings.Contains(strings.Join(args, " "), "name=sglang") {
				return nil, errors.New("timeout")
			}
			return []byte("Up 2 hours"), nil
		})
	_, status := CollectRuntimesWithStatus()
	if status.Status != "partial" {
		t.Errorf("status=%q, want partial (one query failed, the rest observed)", status.Status)
	}
}

// TestBackendPresenceFor — the declared-vs-observed join (KPK-19.7.0-P2-04
// of the KpK audit: eligible ≠ running).
func TestBackendPresenceFor(t *testing.T) {
	runtimes := []Runtime{
		{ID: "sglang", Running: true},
		{ID: "vllm", Running: false},
	}
	healths := []Health{
		{Component: "sglang", State: "healthy"},
		{Component: "vllm", State: "down"},
	}
	got := backendPresenceFor([]string{"sglang", "vllm", "infer_ssd"}, runtimes, healths)
	if len(got) != 3 {
		t.Fatalf("presence=%+v, want 3 entries (declared order)", got)
	}
	if !got[0].RuntimeUp || !got[0].RuntimeHealthy {
		t.Errorf("sglang presence=%+v, want up+healthy", got[0])
	}
	if got[1].RuntimeUp || got[1].RuntimeHealthy {
		t.Errorf("vllm presence=%+v, want down", got[1])
	}
	if got[2].RuntimeUp || got[2].RuntimeHealthy {
		t.Errorf("infer_ssd (future backend) presence=%+v, want honestly absent", got[2])
	}
}
