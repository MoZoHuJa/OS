// Command scarlix-inventory prints normalized system inventory as JSON.
//
// This is the Go-backed producer of the v19.0.7+ data contract for the
// runtime + model + (composed) system-status views. The bash `scarlix` CLI
// delegates `--json runtime list` and `--json model list` to this binary
// when present, so the JSON output matches the frozen schema in
// docs/SCARLIX_DATA_CONTRACTS.md exactly (no hand-rolled JSON string
// concatenation in bash). Human-readable output (`scarlix runtime list`
// without `--json`) still uses the bash CLI's table formatting.
//
// Usage:
//
//	scarlix-inventory               # print full SystemStatus JSON
//	scarlix-inventory --gpus        # print {"gpus":[...],"health":[...]}
//	scarlix-inventory --runtimes    # print {"runtimes":[...],"health":[...]}
//	scarlix-inventory --models      # print []Model
//
// v19.7.0 (Compute Fabric Phase 1) adds the unified read-only v2 views
// and the EXPLICIT, bounded storage benchmark:
//
//	scarlix-inventory --v2              # the full InventorySnapshotV2 + freshness
//	scarlix-inventory --v2-storage      # the storage + host section + freshness
//	scarlix-inventory --v2-models       # the model execution views + freshness
//	scarlix-inventory --storage-benchmark <dir> --duration 5s --size 256MiB
//	                                    # the OPT-IN bounded sequential-read
//	                                    # diagnostic (writes+removes ONE temp
//	                                    # file in <dir>; never run by discovery)
//
// The default (no-flag) form emits the SystemStatus root object with
// version (from /etc/scarlix/VERSION), mode (from /var/lib/scarlix/
// current-mode), GPUs, runtimes, models, and health. Services is left
// empty (no collector implemented in v19.0.9).
//
// Exit codes:
//
//	0 — success (including all "no data → print []" cases)
//	1 — JSON encode failure (should never happen with well-formed types)
//	2 — flag parse error
//
// Build (from scarlihq module root):
//
//	CGO_ENABLED=0 go build -o /usr/local/bin/scarlix-inventory ./cmd/scarlix-inventory
//
// Deployment (v19.0.9 task 3-b): install.sh Phase 4 copies this binary to
// /usr/local/bin/scarlix-inventory (root:root, mode 755). It is invoked by
// the `scarlix` bash CLI's `--json runtime list` / `--json model list`
// paths; falls back to the bash-native JSON if absent.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/registry"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

func main() {
	gpusFlag := flag.Bool("gpus", false, "print only []GPU + GPU health")
	runtimesFlag := flag.Bool("runtimes", false, "print only []Runtime + runtime health")
	modelsFlag := flag.Bool("models", false, "print only []Model")
	// v19.1.3: inspect/status/health for single entries
	runtimeInspect := flag.String("runtime-inspect", "", "print single Runtime JSON by ID")
	runtimeStatus := flag.String("runtime-status", "", "print single runtime Health JSON by ID")
	modelInspect := flag.String("model-inspect", "", "print single Model JSON by ID")
	modelHealth := flag.String("model-health", "", "print single model Health JSON by ID")
	// v19.7.0 (Phase 1): the unified read-only v2 views + the opt-in
	// bounded benchmark. --storage-benchmark is the ONLY benchmark door —
	// an operator diagnostic, never a discovery side effect.
	v2Flag := flag.Bool("v2", false, "print the unified InventorySnapshotV2 + freshness")
	v2StorageFlag := flag.Bool("v2-storage", false, "print the storage + host section + freshness")
	v2ModelsFlag := flag.Bool("v2-models", false, "print the model execution views + freshness")
	benchTarget := flag.String("storage-benchmark", "", "run the OPT-IN bounded sequential-read benchmark on this directory (operator diagnostic; writes+removes one temp file)")
	benchDuration := flag.Duration("duration", 0, "benchmark read window (required with --storage-benchmark; 1s..30s)")
	benchSize := flag.Int64("size", 0, "benchmark temp file size in bytes (required with --storage-benchmark; 32MiB..1GiB)")
	benchBlock := flag.Int("block", 0, "benchmark read block size in bytes (optional with --storage-benchmark; 4KiB..8MiB; default 1MiB)")
	flag.Parse()

	// Reject conflicting flags (only one mode at a time).
	flagsSet := 0
	for _, b := range []*bool{gpusFlag, runtimesFlag, modelsFlag, v2Flag, v2StorageFlag, v2ModelsFlag} {
		if *b {
			flagsSet++
		}
	}
	for _, s := range []*string{runtimeInspect, runtimeStatus, modelInspect, modelHealth, benchTarget} {
		if *s != "" {
			flagsSet++
		}
	}
	if flagsSet > 1 {
		fmt.Fprintln(os.Stderr, "scarlix-inventory: at most one mode flag may be set")
		flag.Usage()
		os.Exit(2)
	}
	// The benchmark's bounds parameters are only meaningful together
	// with --storage-benchmark (flag.Usage explains each).
	if *benchDuration != 0 || *benchSize != 0 || *benchBlock != 0 {
		if *benchTarget == "" {
			fmt.Fprintln(os.Stderr, "scarlix-inventory: --duration/--size/--block require --storage-benchmark")
			flag.Usage()
			os.Exit(2)
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	var (
		err      error
		payload  interface{}
		notFound bool
	)

	switch {
	case *runtimeInspect != "":
		reg := registry.NewRuntimeRegistry()
		_ = reg.LoadFromInventory()
		rt, ok := reg.Inspect(*runtimeInspect)
		if !ok {
			fmt.Fprintf(os.Stderr, "scarlix-inventory: runtime %q not found\n", *runtimeInspect)
			os.Exit(1)
		}
		payload = rt
	case *runtimeStatus != "":
		reg := registry.NewRuntimeRegistry()
		_ = reg.LoadFromInventory()
		h, ok := reg.Status(*runtimeStatus)
		if !ok {
			fmt.Fprintf(os.Stderr, "scarlix-inventory: runtime %q not found\n", *runtimeStatus)
			os.Exit(1)
		}
		payload = h
	case *modelInspect != "":
		reg := registry.NewModelRegistry()
		_ = reg.LoadFromInventory()
		mdl, ok := reg.Inspect(*modelInspect)
		if !ok {
			fmt.Fprintf(os.Stderr, "scarlix-inventory: model %q not found\n", *modelInspect)
			os.Exit(1)
		}
		payload = mdl
	case *modelHealth != "":
		reg := registry.NewModelRegistry()
		_ = reg.LoadFromInventory()
		h, ok := reg.Health(*modelHealth)
		if !ok {
			fmt.Fprintf(os.Stderr, "scarlix-inventory: model %q not found\n", *modelHealth)
			os.Exit(1)
		}
		payload = h
	case *gpusFlag:
		gpus := inventory.CollectGPUs()
		health := inventory.CollectGPUHealth(gpus)
		payload = map[string]interface{}{
			"gpus":   gpus,
			"health": health,
		}
	case *runtimesFlag:
		runtimes := inventory.CollectRuntimes()
		health := inventory.CollectRuntimeHealth(runtimes)
		payload = map[string]interface{}{
			"runtimes": runtimes,
			"health":   health,
		}
	case *modelsFlag:
		payload = inventory.CollectModels()
	case *benchTarget != "":
		// The OPT-IN bounded benchmark — the ONLY caller of
		// storage.RunReadBenchmark in the repository. Bounds are
		// enforced fail-closed inside the library (named errors);
		// the exit code carries the failure.
		res, benchErr := storage.RunReadBenchmark(context.Background(), storage.BenchmarkOptions{
			TargetPath: *benchTarget,
			Duration:   *benchDuration,
			FileSize:   *benchSize,
			BlockSize:  *benchBlock,
		})
		if benchErr != nil {
			fmt.Fprintf(os.Stderr, "scarlix-inventory: storage benchmark failed: %v\n", benchErr)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "scarlix-inventory: benchmark detail: %s\n", res.Detail)
		payload = res
	case *v2Flag:
		snap, snapErr := inventory.BuildSnapshotV2()
		if snapErr != nil {
			fmt.Fprintf(os.Stderr, "scarlix-inventory: v2 snapshot unavailable: %v\n", snapErr)
			os.Exit(1)
		}
		payload = snap.Response(60 * time.Second)
	case *v2StorageFlag:
		snap, snapErr := inventory.BuildSnapshotV2()
		if snapErr != nil {
			fmt.Fprintf(os.Stderr, "scarlix-inventory: v2 snapshot unavailable: %v\n", snapErr)
			os.Exit(1)
		}
		resp := snap.Response(60 * time.Second)
		payload = map[string]interface{}{
			"freshness": resp.Freshness,
			"host":      snap.Host,
			"storage":   snap.Storage,
			"sources":   filterSourcesCLI(snap.Sources, "storage", "storage.host"),
		}
	case *v2ModelsFlag:
		snap, snapErr := inventory.BuildSnapshotV2()
		if snapErr != nil {
			fmt.Fprintf(os.Stderr, "scarlix-inventory: v2 snapshot unavailable: %v\n", snapErr)
			os.Exit(1)
		}
		resp := snap.Response(60 * time.Second)
		payload = map[string]interface{}{
			"freshness": resp.Freshness,
			"models":    snap.Models,
			"sources":   filterSourcesCLI(snap.Sources, "models", "models.manifests"),
		}
	default:
		payload = collectSystemStatus()
	}

	_ = notFound

	if err = enc.Encode(payload); err != nil {
		fmt.Fprintf(os.Stderr, "scarlix-inventory: encode error: %v\n", err)
		os.Exit(1)
	}
}

// collectSystemStatus composes the full SystemStatus root object from all
// collectors + system metadata (version, mode, uptime, timestamp).
//
// GPU + Runtime + Model collectors all gracefully degrade to empty slices
// when their data source is unavailable — so even on a host with no GPU,
// no Docker, and no models.yaml, this returns a valid SystemStatus with
// empty arrays (not null) per stability contract §5.
func collectSystemStatus() inventory.SystemStatus {
	gpus := inventory.CollectGPUs()
	gpuHealth := inventory.CollectGPUHealth(gpus)

	runtimes := inventory.CollectRuntimes()
	runtimeHealth := inventory.CollectRuntimeHealth(runtimes)

	models := inventory.CollectModels()

	// Combine all health entries into one slice (GPU health + runtime
	// health). The data contract §1 says Health is "the combined health
	// of all components" — we emit them in this order: GPUs first
	// (gpu.0, gpu.1, ...) then runtimes (sglang, vllm, ...).
	allHealth := make([]inventory.Health, 0, len(gpuHealth)+len(runtimeHealth))
	allHealth = append(allHealth, gpuHealth...)
	allHealth = append(allHealth, runtimeHealth...)

	return inventory.SystemStatus{
		Version:       readVersion(),
		Mode:          readMode(),
		UptimeSeconds: readUptime(),
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		GPUs:          gpus,
		Runtimes:      runtimes,
		Models:        models,
		Services:      []inventory.Service{}, // no collector in v19.0.9
		Health:        allHealth,
	}
}

// filterSourcesCLI keeps the source statuses whose Component matches one
// of the wanted components (never nil — the []-not-null contract). The
// CLI mirror of the API's filterSources.
func filterSourcesCLI(srcs []storage.SourceStatus, want ...string) []storage.SourceStatus {
	set := make(map[string]bool, len(want))
	for _, w := range want {
		set[w] = true
	}
	out := make([]storage.SourceStatus, 0, len(srcs))
	for _, s := range srcs {
		if set[s.Component] {
			out = append(out, s)
		}
	}
	return out
}

// readVersion reads the SCARLIX OS version from /etc/scarlix/VERSION (the
// canonical install path). Falls back to:
//   - $SCARLIX_REPO/VERSION (dev sandbox with repo checkout)
//   - "unknown" (last resort)
//
// Trims surrounding whitespace (the VERSION file is `19.0.7\n`).
func readVersion() string {
	candidates := []string{
		"/etc/scarlix/VERSION",
		os.Getenv("SCARLIX_REPO") + "/VERSION",
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		data, err := os.ReadFile(c)
		if err == nil {
			s := strings.TrimSpace(string(data))
			if s != "" {
				return s
			}
		}
	}
	return "unknown"
}

// readMode reads the current scarlix-mode from /var/lib/scarlix/current-mode
// (written by scarlix-mode on every mode switch). Falls back to:
//   - $SCARLIX_REPO/.current-mode (dev sandbox)
//   - "unknown" (last resort)
func readMode() string {
	candidates := []string{
		"/var/lib/scarlix/current-mode",
		os.Getenv("SCARLIX_REPO") + "/.current-mode",
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		data, err := os.ReadFile(c)
		if err == nil {
			s := strings.TrimSpace(string(data))
			if s != "" {
				return s
			}
		}
	}
	return "unknown"
}

// readUptime reads host uptime in seconds from /proc/uptime (Linux only —
// the only platform ScarLiX supports). The file format is "12345.67 67890.12\n"
// where the first float is uptime in seconds. Returns 0 on read/parse
// failure (graceful degradation — SystemStatus.UptimeSeconds is int64
// so 0 is the natural "unknown" sentinel).
func readUptime() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	// /proc/uptime: "<uptime_seconds> <idle_seconds>\n"
	// We want the first float — parse just enough to extract it.
	line := strings.TrimSpace(string(data))
	if line == "" {
		return 0
	}
	fields := strings.Fields(line)
	if len(fields) < 1 {
		return 0
	}
	var f float64
	if _, err := fmt.Sscanf(fields[0], "%f", &f); err != nil {
		return 0
	}
	return int64(f)
}
