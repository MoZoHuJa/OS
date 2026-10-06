// Command scarlix-monitor prints a read-only system monitoring snapshot as
// JSON. It is the standalone CLI entry point for the ScarliMonitor service
// (v19.1.4 ScarliMonitor Foundation — ScaRgeN master guide section 14).
//
// The monitor package composes inventory collectors (GPUs, runtimes, models,
// health) with /proc + /sys reads (CPU, RAM, storage) into a single
// point-in-time Snapshot. This binary is the simplest possible consumer of
// that snapshot — print-as-JSON-and-exit. Future versions (v19.1.5+) will add
// an HTTP server mode (--serve) for long-running monitoring.
//
// Usage:
//
//      scarlix-monitor                    # print a single Snapshot JSON, exit 0
//      scarlix-monitor --once             # same as above (explicit)
//      scarlix-monitor --serve <port>     # future HTTP server mode (v19.1.5+)
//                                         # currently a stub
//      scarlix-monitor --help, -h         # print usage + exit 0
//
// Exit codes:
//
//      0 — success (including the "no GPU/Docker/models → print empty
//          arrays" case — graceful degradation is success, not failure)
//      1 — JSON encode failure (should never happen with well-formed types)
//      2 — flag parse error
//
// Build (from scarlihq module root):
//
//      CGO_ENABLED=0 go build -o /usr/local/bin/scarlix-monitor ./cmd/scarlix-monitor
//
// Deployment (v19.1.4 task 4-b): install.sh Phase 4 copies this binary to
// /usr/local/bin/scarlix-monitor (root:root, mode 755). It is a standalone
// tool — the bash `scarlix` CLI does not depend on it. Future integrations:
//   - v19.1.5: telemetry history (persist snapshots every N seconds)
//   - v19.1.6: ScarliHQ resource view (HTTP endpoint serving snapshots)
//   - v19.2.x: compute fabric (scheduler reads snapshot for resource state)
package main

import (
        "encoding/json"
        "flag"
        "fmt"
        "os"
        "strings"
        "time"

        "github.com/MoZoHuJa/OS/scarlihq/internal/monitor"
        "github.com/MoZoHuJa/OS/scarlihq/internal/telemetry"
)

const usage = `scarlix-monitor — ScarliMonitor read-only snapshot CLI (SCARLIX OS v19.1.4)

Usage:
  scarlix-monitor                  Print a single Snapshot as pretty JSON, exit 0
  scarlix-monitor --once           Same as default (explicit)
  scarlix-monitor --serve <port>   Start HTTP server (STUB — coming in v19.1.5)
  scarlix-monitor -h, --help       Print this help and exit 0

The snapshot includes GPU, CPU, RAM, storage, runtime health, model health,
and service health. All reads are best-effort — missing sources (no
nvidia-smi, no Docker, no models.yaml) yield zero values, never errors.

Exit codes:
  0  success
  1  JSON encode failure (should never happen)
  2  flag parse / usage error

See docs/SCARLIX_MONITOR.md for the snapshot schema and design notes.
`

func main() {
        once := flag.Bool("once", false, "print a single Snapshot as JSON and exit (default behavior)")
        servePort := flag.Int("serve", 0, "start HTTP server on the given port (STUB — coming in v19.1.5)")
        record := flag.Bool("record", false, "take a snapshot + persist telemetry measurements to store (v19.1.5)")
        history := flag.Bool("history", false, "print telemetry history as JSON array (v19.1.5)")
        historyFrom := flag.String("from", "", "history query start time (RFC3339, e.g. 2026-10-06T00:00:00Z)")
        historyTo := flag.String("to", "", "history query end time (RFC3339)")
        pruneOlder := flag.String("prune", "", "prune entries older than duration (e.g. 24h, 7d)")
        flag.Usage = func() {
                fmt.Fprint(os.Stderr, usage)
        }
        flag.Parse()

        // --serve stub
        if *servePort > 0 {
                fmt.Println("HTTP server mode coming in v19.1.6")
                os.Exit(0)
        }

        _ = once

        version := readVersion()

        // --record: take snapshot + persist telemetry
        if *record {
                m := monitor.New(version)
                snap := m.Snapshot()
                store := telemetry.New("")

                // Convert snapshot to measurements (one per GPU if GPUs present, else one general)
                if len(snap.GPUs) > 0 {
                        for _, gpu := range snap.GPUs {
                                meas := telemetry.Measurement{
                                        Timestamp:      snap.Timestamp,
                                        GPUIndex:       gpu.Index,
                                        GPUUtilPct:     float64(gpu.UtilizationPct),
                                        GPUVRAMUsedMB:  gpu.VRAMUsedMB,
                                        GPUVRAMTotalMB: gpu.VRAMTotalMB,
                                        GPUTempC:       gpu.TemperatureC,
                                }
                                // Find the runtime running on this GPU
                                for _, rt := range snap.Runtimes {
                                        if rt.Running {
                                                for _, gid := range rt.GPUIDs {
                                                        if gid == fmt.Sprintf("gpu.nvidia.%d", gpu.Index) {
                                                                meas.Runtime = rt.ID
                                                                break
                                                        }
                                                }
                                        }
                                }
                                if err := store.Append(meas); err != nil {
                                        fmt.Fprintf(os.Stderr, "scarlix-monitor: record failed: %v\n", err)
                                        os.Exit(1)
                                }
                        }
                } else {
                        // No GPUs — record a single measurement with just timestamp + CPU
                        meas := telemetry.Measurement{
                                Timestamp: snap.Timestamp,
                        }
                        if err := store.Append(meas); err != nil {
                                fmt.Fprintf(os.Stderr, "scarlix-monitor: record failed: %v\n", err)
                                os.Exit(1)
                        }
                }
                fmt.Fprintf(os.Stderr, "scarlix-monitor: recorded telemetry to %s\n", store.Path())
                os.Exit(0)
        }

        // --history: query + print telemetry
        if *history {
                store := telemetry.New("")

                var fromTime, toTime time.Time
                var err error
                if *historyFrom != "" {
                        fromTime, err = time.Parse(time.RFC3339, *historyFrom)
                        if err != nil {
                                fmt.Fprintf(os.Stderr, "scarlix-monitor: invalid --from time: %v\n", err)
                                os.Exit(2)
                        }
                }
                if *historyTo != "" {
                        toTime, err = time.Parse(time.RFC3339, *historyTo)
                        if err != nil {
                                fmt.Fprintf(os.Stderr, "scarlix-monitor: invalid --to time: %v\n", err)
                                os.Exit(2)
                        }
                }

                results, err := store.Query(fromTime, toTime)
                if err != nil {
                        fmt.Fprintf(os.Stderr, "scarlix-monitor: history query failed: %v\n", err)
                        os.Exit(1)
                }

                enc := json.NewEncoder(os.Stdout)
                enc.SetIndent("", "  ")
                if err := enc.Encode(results); err != nil {
                        fmt.Fprintf(os.Stderr, "scarlix-monitor: encode error: %v\n", err)
                        os.Exit(1)
                }
                os.Exit(0)
        }

        // --prune: remove old entries
        if *pruneOlder != "" {
                dur, err := time.ParseDuration(*pruneOlder)
                if err != nil {
                        fmt.Fprintf(os.Stderr, "scarlix-monitor: invalid duration %q: %v\n", *pruneOlder, err)
                        os.Exit(2)
                }
                store := telemetry.New("")
                removed, err := store.Prune(dur)
                if err != nil {
                        fmt.Fprintf(os.Stderr, "scarlix-monitor: prune failed: %v\n", err)
                        os.Exit(1)
                }
                fmt.Fprintf(os.Stderr, "scarlix-monitor: pruned %d entries older than %s\n", removed, *pruneOlder)
                os.Exit(0)
        }

        // Default: --once (print snapshot JSON)
        m := monitor.New(version)
        data, err := m.SnapshotJSON()
        if err != nil {
                fmt.Fprintf(os.Stderr, "scarlix-monitor: encode error: %v\n", err)
                os.Exit(1)
        }

        fmt.Println(string(data))
}

// readVersion reads the SCARLIX OS version from /etc/scarlix/VERSION (the
// canonical install path). Falls back to:
//   - $SCARLIX_REPO/VERSION (dev sandbox with repo checkout)
//   - "unknown" (last resort)
//
// Mirrors scarlix-inventory/main.go's readVersion() — kept inline here to
// avoid pulling package main-scoped helpers across binaries.
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
