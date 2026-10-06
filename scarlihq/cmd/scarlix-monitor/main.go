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
        "flag"
        "fmt"
        "os"
        "strings"

        "github.com/MoZoHuJa/OS/scarlihq/internal/monitor"
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
        // Define flags. --once is the default behavior (no-op flag, kept for
        // explicitness + future-script clarity). --serve <port> is a stub — it
        // prints a "coming in v19.1.5" message and exits 0 per the task spec
        // ("don't implement the server yet, just the flag").
        once := flag.Bool("once", false, "print a single Snapshot as JSON and exit (default behavior)")
        servePort := flag.Int("serve", 0, "start HTTP server on the given port (STUB — coming in v19.1.5)")
        flag.Usage = func() {
                fmt.Fprint(os.Stderr, usage)
        }
        flag.Parse()

        // --serve is mutually exclusive with --once (and with the default
        // no-args form). If both are set, prefer --serve (the explicit future
        // mode) — but since --serve is a stub in v19.1.4, we just print the
        // stub message and exit.
        if *servePort > 0 {
                fmt.Println("HTTP server mode coming in v19.1.5")
                // Exit 0 — the stub message is the intended v19.1.4 behavior,
                // not an error. This makes scripts that probe for --serve support
                // get a clean exit code they can branch on.
                os.Exit(0)
        }

        // Default + --once: same path. The *once bool is intentionally unused
        // beyond flag parsing — its presence in --help output documents that
        // the no-args form IS the "once" mode, which is otherwise non-obvious.
        _ = once

        // Read the SCARLIX OS version from /etc/scarlix/VERSION (canonical install
        // path). Falls back through repo-relative paths + "unknown" sentinel,
        // matching scarlix-inventory/main.go's readVersion() helper. We don't
        // import that helper because it's package main-scoped — re-implement
        // the small reader inline (5 lines, no behavior drift risk).
        version := readVersion()

        m := monitor.New(version)
        data, err := m.SnapshotJSON()
        if err != nil {
                fmt.Fprintf(os.Stderr, "scarlix-monitor: encode error: %v\n", err)
                os.Exit(1)
        }

        // json.MarshalIndent doesn't append a trailing newline; print one for
        // clean terminal output + POSIX-friendly piping (e.g. `| jq`).
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
