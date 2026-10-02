package scarlix_mode

import (
        "fmt"
        "os"
        "path/filepath"
        "strings"

        "github.com/MoZoHuJa/OS/scarlihq/internal/status"
)

// Mode manages scarlix-mode via a file-based bridge (v17.9.8+).
//
// ARCHITECTURE (v18.0.0):
//   ScarliHQ writes desired-mode to /var/lib/scarlix/bridge-input/desired-mode
//   (writable by nonroot UID 65532 — the ONLY file ScarliHQ can write).
//   Host bridge reads from bridge-input/ but writes ALL state (retry, last-transition)
//   to /var/lib/scarlix/bridge-state/ (root:root 700 — ScarliHQ cannot create symlinks there).
//
// v18.0.0 FIX: concurrent mode requests use unique temp files (was: same .tmp → race)
// v17.9.8: Current() TrimSpace (was: returned "ai\n" → comparison failed)
type Mode struct {
        desiredModeFile string // bridge-input/desired-mode (writable by ScarliHQ nonroot)
}

// New creates a new Mode manager with default paths.
func New() *Mode {
        return &Mode{
                desiredModeFile: "/var/lib/scarlix/bridge-input/desired-mode",
        }
}

// NewForTest creates a Mode manager with a custom desired-mode file path.
// v18.8.2 P2: Used by unit tests to avoid touching real /var/lib/scarlix paths.
func NewForTest(desiredModeFile string) *Mode {
        return &Mode{
                desiredModeFile: desiredModeFile,
        }
}

// Current returns the current mode from current-mode file (written by scarlix-mode on host).
// v17.9.8 P1: TrimSpace — state file contains trailing newline from `echo "ai" | tee`.
func (m *Mode) Current() string {
        data, err := os.ReadFile("/var/lib/scarlix/current-mode")
        if err != nil {
                return "unknown"
        }
        return strings.TrimSpace(string(data))
}

// Request is the centralized mode-request entry point for both REST and MCP handlers.
// v18.7.3 P1: Centralizes the transition-state check + atomic Set() (was: duplicated in
// scarlihq/internal/api/rest.go modeHandler and scarlihq/internal/mcp/server.go scarlix_mode_set).
//
// Order of operations:
//   1. Read host-status.json. If ModeTransition.State == "retrying", reject with a
//      "... in progress (retrying) ..." message — the dashboard surfaces this as 409.
//      (Other states — none/applied/failed/rejected — allow new requests.)
//   2. Delegate to Set(), which atomically reserves desired-mode via O_EXCL.
//
// Note: There is a small TOCTOU window between step 1 (read status) and step 2 (write
// desired-mode). If the host bridge flips state to "retrying" between the two reads,
// we still have the O_EXCL atomic reservation in step 2 — so the worst case is that a
// request lands DURING an in-progress transition and is queued by the host bridge on
// its next tick (host bridge serializes via flock 200). This is acceptable; the goal
// of step 1 is just to short-circuit the common "user double-clicks" case.
func (m *Mode) Request(mode string) error {
        // Check if a transition is actively in progress.
        // v18.6 P0: Only "retrying" blocks new requests — "applied" was a transition that
        // succeeded, so new requests are fine (was: blocked all future switches).
        s := status.ReadOrStale()
        if s.ModeTransition.State == "retrying" {
                return fmt.Errorf("mode transition in progress (retrying), try again later")
        }
        // v18.7.2 P0: Atomic reservation via O_EXCL — Set() is the actual lock, not a
        // separate Stat() pre-check followed by Write (which was the v18.7.1 race).
        return m.Set(mode)
}

// Set requests a mode switch by writing to desired-mode file.
// v18.7.2 P0: Atomic request reservation via O_EXCL (was: Stat+CreateTemp+Rename → race).
// O_EXCL fails if file exists → prevents lost update between Stat and Write.
// The host bridge (scarlix-host-bridge.timer) applies it asynchronously within 5s.
// Returns nil if the mode is valid and the file was written; the actual switch is async.
//
// v18.7.3 P1: Most callers should prefer Request() — it adds the transition-state
// pre-check. Set() is the lower-level atomic write and is kept exported for any
// future caller that intentionally wants to bypass the retrying check (e.g. a forced
// recovery tool).
func (m *Mode) Set(mode string) error {
        switch mode {
        case "ai", "stop", "game", "creative", "turbo", "offline", "tv":
        default:
                return fmt.Errorf("invalid mode: %s (valid: ai, stop, game, creative, turbo, offline, tv)", mode)
        }

        dir := filepath.Dir(m.desiredModeFile)
        if info, err := os.Stat(dir); err != nil || !info.IsDir() {
                return fmt.Errorf("bridge-input dir not accessible: %s (check install.sh Phase 5)", dir)
        }

        // v18.7.2 P0: O_EXCL = atomic "create if not exists" — if file exists, returns EEXIST.
        // This is the ACTUAL lock, not a Stat() check followed by a separate Write.
        fd, err := os.OpenFile(m.desiredModeFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
        if err != nil {
                if os.IsExist(err) {
                        return fmt.Errorf("mode request already pending (desired-mode exists)")
                }
                return fmt.Errorf("create desired-mode failed (permission?): %w", err)
        }
        defer fd.Close()

        if _, err := fd.WriteString(mode + "\n"); err != nil {
                os.Remove(m.desiredModeFile) // cleanup on write failure
                return fmt.Errorf("write desired-mode failed: %w", err)
        }

        return nil
}

// AvailableModes returns all valid modes (must match scarlix-mode case statement).
func (m *Mode) AvailableModes() []string {
        return []string{"ai", "stop", "game", "creative", "turbo", "offline", "tv"}
}
