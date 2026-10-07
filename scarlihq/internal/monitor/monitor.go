// Package monitor implements ScarliMonitor — a read-only monitoring service
// that exposes system state (GPU, CPU, RAM, storage, runtime/model/service
// health). Per ScaRgeN master guide section 14. No control operations —
// observation only.
//
// v19.1.4 — ScarliMonitor Foundation.
//
// ScarliMonitor composes the inventory package's collectors (GPUs, runtimes,
// models, health) with direct /proc + /sys reads (CPU, RAM, storage) into a
// single point-in-time Snapshot. It is the natural evolution of
// scarlix-inventory's collectSystemStatus() into a reusable library that the
// upcoming HTTP API (v19.1.5+) and ScarliHQ resource view (v19.1.6) will
// consume.
//
// Scope rules (v19.1.4 task 4-b):
//
//   - READ-ONLY. No control operations: no container start/stop, no GPU
//     reset, no model download, no mode switch. The Monitor observes —
//     callers act.
//   - Graceful degradation: every system read (nvidia-smi, docker ps,
//     /proc/cpuinfo, /proc/meminfo, /proc/loadavg, df, models.yaml) is
//     optional. If a source is unavailable the corresponding Snapshot
//     field gets a zero value; Snapshot() NEVER returns an error and NEVER
//     panics. This mirrors the inventory package's stability contract §5
//     ("all arrays must serialize as [], never null") and extends it to
//     scalar fields.
//   - Linux-primary. /proc and /sys are Linux-only; on non-Linux dev
//     machines the corresponding fields return zero values (the package
//     still compiles + tests still run, with the Linux-only tests skipped
//     via os.Stat checks).
//   - Composition, not replacement. The Monitor does NOT re-implement GPU
//     or runtime collection — it delegates to inventory.CollectGPUs(),
//     inventory.CollectRuntimes(), inventory.CollectModels(),
//     inventory.CollectGPUHealth(), inventory.CollectRuntimeHealth().
//     Same collectors, same data contract, unified shape.
//
// STABILITY: The Snapshot struct's JSON field names are FROZEN in v19.1.4.
// Future versions may ADD fields (with omitempty where it makes sense) but
// MUST NOT rename or remove existing ones. This is the same stability
// contract used by the inventory package — see docs/SCARLIX_DATA_CONTRACTS.md §7.
package monitor

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

// Monitor is the ScarliMonitor service. It composes inventory collectors +
// registries into a unified monitoring snapshot.
//
// The Version field is the SCARLIX OS version string (e.g. "19.1.4") and is
// surfaced verbatim in every Snapshot. Callers should populate it from
// /etc/scarlix/VERSION (the canonical install path); passing an empty string
// is allowed and yields a Snapshot with Version="" — useful in tests and in
// environments where /etc/scarlix/VERSION hasn't been installed yet.
type Monitor struct {
	// Version is the SCARLIX OS version surfaced in Snapshot.Version.
	// Read by the caller from /etc/scarlix/VERSION (or set to "" / "unknown").
	Version string
}

// Snapshot is a point-in-time read-only view of the entire system.
// This is what the monitoring API returns.
//
// All slice fields are guaranteed non-nil: when no data is available they
// hold an empty (zero-length) slice rather than nil, so JSON marshalling
// always emits [] (stability contract §5 — "all arrays must serialize as
// [], never null"). The inventory package's Collect* functions enforce
// this for GPUs/Runtimes/Models/Services/Health; Snapshot() enforces it
// defensively on top.
type Snapshot struct {
	Timestamp string              `json:"timestamp"` // ISO 8601 (RFC 3339 UTC)
	Version   string              `json:"version"`   // SCARLIX OS version
	Mode      string              `json:"mode"`      // current scarlix-mode
	GPUs      []inventory.GPU     `json:"gpus"`
	CPU       CPUInfo             `json:"cpu"`
	RAM       RAMInfo             `json:"ram"`
	Storage   StorageInfo         `json:"storage"`
	Runtimes  []inventory.Runtime `json:"runtimes"`
	Models    []inventory.Model   `json:"models"`
	Services  []inventory.Service `json:"services"`
	Health    []inventory.Health  `json:"health"`
}

// CPUInfo captures CPU topology + load. Populated from /proc/cpuinfo (cores +
// model name), /proc/loadavg (1-minute load average), and /proc/stat
// (cumulative usage since boot).
//
// On non-Linux hosts (or if /proc/cpuinfo is unreadable) all fields are
// zero — graceful degradation, never an error.
type CPUInfo struct {
	Cores     int     `json:"cores"`       // logical cores (count of /proc/cpuinfo "processor" lines)
	ModelName string  `json:"model_name"`  // e.g. "AMD Ryzen 7 7700X" (first /proc/cpuinfo "model name")
	LoadAvg1m float64 `json:"load_avg_1m"` // 1-minute load average (first field of /proc/loadavg)
	UsagePct  float64 `json:"usage_pct"`   // CPU usage % (0-100) — cumulative since boot from /proc/stat
}

// RAMInfo captures physical memory. Populated from /proc/meminfo.
// All sizes are in megabytes (1 MB = 1024 KB = 2^20 bytes, matching
// /proc/meminfo's "kB" suffix which is actually KiB).
type RAMInfo struct {
	TotalMB     int `json:"total_mb"`
	UsedMB      int `json:"used_mb"`
	FreeMB      int `json:"free_mb"`
	AvailableMB int `json:"available_mb"`
}

// StorageInfo captures filesystem free space + the /models directory total
// size (sum of file sizes, in MB). Populated via os/exec call to `df -P -m`
// (portable across Linux + macOS, gracefully returns 0 if df missing) plus
// a filepath.WalkDir sum for ModelsTotalMB.
//
// ModelsDir is hardcoded "/models" (the canonical SCARLIX mount point).
// ModelsTotalMB is 0 if /models doesn't exist. ModelsFreeMB is the free
// space on the filesystem containing /models (e.g. if /models is a bind
// mount of /mnt/storage, that's the FS reported). RootFreeMB is the free
// space on /.
type StorageInfo struct {
	ModelsDir     string `json:"models_dir"`      // "/models" (canonical)
	ModelsTotalMB int    `json:"models_total_mb"` // sum of file sizes under /models (MB)
	ModelsFreeMB  int    `json:"models_free_mb"`  // free space on /models filesystem (MB)
	RootFreeMB    int    `json:"root_free_mb"`    // free space on / (MB)
}

// New creates a new Monitor instance.
//
// The version string is surfaced verbatim in every Snapshot's Version field.
// Callers should read it from /etc/scarlix/VERSION (see cmd/scarlix-monitor
// for the canonical reader). Passing "" is allowed — useful in tests and
// pre-install environments.
func New(version string) *Monitor {
	return &Monitor{Version: version}
}

// Snapshot collects a full read-only system snapshot. Calls inventory
// collectors (CollectGPUs, CollectRuntimes, CollectModels, CollectGPUHealth,
// CollectRuntimeHealth) + reads /proc/cpuinfo, /proc/loadavg, /proc/meminfo,
// /proc/stat (CPU/RAM/load), and `df` + filepath.WalkDir (storage).
//
// Never panics. Never returns an error. Every system read is best-effort —
// if a source is unavailable (non-Linux, no nvidia-smi, no Docker, no
// models.yaml, no /models dir) the corresponding field gets its zero value
// and the snapshot is still valid.
func (m *Monitor) Snapshot() Snapshot {
	// 1. Compose inventory collectors (these never panic / never error — they
	//    return empty slices on any failure, per inventory package contract).
	gpus := inventory.CollectGPUs()
	gpuHealth := inventory.CollectGPUHealth(gpus) // mutates gpus[].Healthy in place

	runtimes := inventory.CollectRuntimes()
	runtimeHealth := inventory.CollectRuntimeHealth(runtimes) // mutates runtimes[].Healthy in place

	models := inventory.CollectModels()

	// No service collector exists in v19.1.4 — scarlix-inventory also leaves
	// this empty. Future v19.1.x will add a CollectServices() that probes
	// systemd + docker compose services. Until then, emit [] not null.
	services := []inventory.Service{}

	// Combined health slice (GPU entries first, then runtime entries) —
	// matches scarlix-inventory/main.go collectSystemStatus() ordering.
	allHealth := make([]inventory.Health, 0, len(gpuHealth)+len(runtimeHealth))
	allHealth = append(allHealth, gpuHealth...)
	allHealth = append(allHealth, runtimeHealth...)

	// 2. Defensive nil-coercion (inventory collectors already guarantee
	//    non-nil, but a future refactor or an alternate collector could
	//    break that — enforce at the boundary).
	if gpus == nil {
		gpus = []inventory.GPU{}
	}
	if runtimes == nil {
		runtimes = []inventory.Runtime{}
	}
	if models == nil {
		models = []inventory.Model{}
	}
	if allHealth == nil {
		allHealth = []inventory.Health{}
	}

	// 3. System reads (CPU/RAM/storage). Each helper returns zero values on
	//    any failure — no error propagation.
	cpu := readCPU()
	ram := readRAM()
	storage := readStorage()

	return Snapshot{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Version:   m.Version,
		Mode:      readMode(),
		GPUs:      gpus,
		CPU:       cpu,
		RAM:       ram,
		Storage:   storage,
		Runtimes:  runtimes,
		Models:    models,
		Services:  services,
		Health:    allHealth,
	}
}

// SnapshotJSON returns the snapshot as pretty-printed JSON (2-space indent).
//
// Convenience method for callers that want JSON directly without invoking
// encoding/json themselves. Returns an error only if JSON encoding fails,
// which should be impossible for the well-formed Snapshot struct (all fields
// are basic types or slices of basic structs from the inventory package,
// which already have stable JSON tags).
func (m *Monitor) SnapshotJSON() ([]byte, error) {
	snap := m.Snapshot()
	return json.MarshalIndent(snap, "", "  ")
}

// ----- system readers ------------------------------------------------------

// readMode reads the current scarlix-mode from /var/lib/scarlix/current-mode
// (written by scarlix-mode on every mode switch). Falls back to "" if the
// file is missing or unreadable. This intentionally diverges from
// scarlix-inventory's readMode() (which returns "unknown") per the v19.1.4
// task spec — empty string signals "no mode set yet" without conflating with
// a real mode named "unknown".
func readMode() string {
	data, err := os.ReadFile("/var/lib/scarlix/current-mode")
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	return s
}

// readCPU reads CPU info from /proc/cpuinfo (cores + model name),
// /proc/loadavg (1-minute load average), and /proc/stat (cumulative usage %
// since boot). Returns zero values on any failure.
//
// UsagePct computation: parse the first cpu line of /proc/stat
//
//	cpu user nice system idle iowait irq softirq steal guest guest_nice
//
// and compute (1 - idle/total) * 100. This is the CUMULATIVE average since
// boot — not instantaneous. For instantaneous usage one would need to
// sample twice with a sleep in between; that's future work (v19.1.5+
// telemetry history). The cumulative value is still useful as a "since
// boot" rough indicator and matches what `top` shows on first invocation.
func readCPU() CPUInfo {
	var info CPUInfo

	// /proc/cpuinfo — count "processor" lines + extract first "model name".
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		cores := 0
		modelName := ""
		for _, line := range strings.Split(string(data), "\n") {
			// /proc/cpuinfo uses "<key>\t: <value>" format. Split on first ":".
			idx := strings.Index(line, ":")
			if idx < 0 {
				continue
			}
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			if key == "processor" {
				cores++
			} else if key == "model name" && modelName == "" {
				modelName = val
			}
		}
		info.Cores = cores
		info.ModelName = modelName
	}

	// /proc/loadavg — "0.02 0.06 0.02 1/256 22616" — first field is 1m load.
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 1 {
			if f, err := strconv.ParseFloat(fields[0], 64); err == nil {
				info.LoadAvg1m = f
			}
		}
	}

	// /proc/stat — cumulative usage since boot. Single-sample formula:
	//   idleRatio = idle / total
	//   usage = (1 - idleRatio) * 100
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(line, "cpu ") {
				continue
			}
			// "cpu  user nice system idle iowait irq softirq steal ..."
			fields := strings.Fields(line)
			if len(fields) < 5 {
				break
			}
			// Fields[0] == "cpu"; fields[1..] are the jiffy counters.
			var total, idle int64
			for i, f := range fields[1:] {
				n, err := strconv.ParseInt(f, 10, 64)
				if err != nil {
					continue
				}
				total += n
				// idle = fields[3] (i==3), iowait = fields[4] (i==4) — both
				// count as "idle" time in the standard top-style formula.
				if i == 3 || i == 4 {
					idle += n
				}
			}
			if total > 0 {
				info.UsagePct = (1.0 - float64(idle)/float64(total)) * 100.0
				// Clamp to [0, 100] — negative can happen on weird counters,
				// >100 can happen on multi-CPU aggregation edge cases.
				if info.UsagePct < 0 {
					info.UsagePct = 0
				}
				if info.UsagePct > 100 {
					info.UsagePct = 100
				}
			}
			break
		}
	}

	return info
}

// readRAM reads memory info from /proc/meminfo. Returns zero values on
// failure. Field mapping:
//
//	MemTotal      → TotalMB     (KiB → MiB: divide by 1024)
//	MemAvailable  → AvailableMB
//	MemFree       → FreeMB
//	UsedMB        = TotalMB - AvailableMB
//
// Used = Total - Available (not Total - Free) because Available accounts for
// reclaimable buffers/cached memory, which is the more meaningful "used" for
// capacity-planning purposes. This matches `free -m`'s "used" column.
func readRAM() RAMInfo {
	var info RAMInfo

	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return info
	}

	var memTotal, memFree, memAvail int64
	for _, line := range strings.Split(string(data), "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		// val is like "4138564 kB" — strip the " kB" suffix and parse.
		val = strings.TrimSuffix(val, " kB")
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			memTotal = n
		case "MemFree":
			memFree = n
		case "MemAvailable":
			memAvail = n
		}
	}

	info.TotalMB = int(memTotal / 1024)
	info.FreeMB = int(memFree / 1024)
	info.AvailableMB = int(memAvail / 1024)
	if memTotal >= memAvail {
		info.UsedMB = int((memTotal - memAvail) / 1024)
	} else {
		// Defensive — shouldn't happen but clamp to avoid negative.
		info.UsedMB = 0
	}
	return info
}

// readStorage reads filesystem free space for / and /models (via `df -P -m`,
// portable across Linux + macOS, gracefully returns 0 if df is unavailable)
// and sums the size of all files under /models (via filepath.WalkDir).
//
// ModelsDir is hardcoded "/models" (the canonical SCARLIX mount point).
func readStorage() StorageInfo {
	info := StorageInfo{
		ModelsDir: "/models",
	}

	// If /models exists, compute its total size (MB) + filesystem free (MB).
	if _, err := os.Stat("/models"); err == nil {
		info.ModelsTotalMB = dirSizeMB("/models")
		info.ModelsFreeMB = dfFreeMB("/models")
	}

	// Root filesystem free space.
	info.RootFreeMB = dfFreeMB("/")

	return info
}

// dfFreeMB runs `df -P -m <path>` and returns the "Available" column (in MB).
// Returns 0 if df is unavailable, fails, or returns an unparseable line.
//
// `df -P` uses POSIX output format (always one line per filesystem, columns
// in fixed order: Filesystem 1M-blocks Used Available Use% Mounted-on). The
// `-m` flag forces 1M-block units. Available is column 4 (0-indexed: fields[3]).
func dfFreeMB(path string) int {
	binary, err := exec.LookPath("df")
	if err != nil {
		return 0
	}
	out, err := exec.Command(binary, "-P", "-m", path).Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0
	}
	// Header is line[0]; first data row is line[1]. df -P guarantees
	// single-line output (no wrapping) but the filesystem column might
	// contain a space (rare — only for some NFS mounts); use Fields which
	// is whitespace-tolerant.
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return 0
	}
	n, err := strconv.Atoi(fields[3])
	if err != nil {
		return 0
	}
	return n
}

// dirSizeMB walks a directory tree and returns the sum of all regular-file
// sizes in megabytes. Returns 0 if the path doesn't exist or the walk
// encounters errors (errors are silently ignored — graceful degradation).
//
// Used for /models total size. Symlinks are followed only if they point at
// a regular file (filepath.WalkDir calls Lstat, so symlinks to directories
// are NOT descended — avoids cycles in bind-mounted model stores).
func dirSizeMB(path string) int {
	var total int64
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission denied, broken symlink, etc. — skip, don't abort.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// Mode().IsRegular() would skip symlinks and device files; size is
		// meaningful only for regular files. WalkDir already gives us a
		// DirEntry so we use Info() to get the size.
		total += info.Size()
		return nil
	})
	return int(total / (1024 * 1024))
}
