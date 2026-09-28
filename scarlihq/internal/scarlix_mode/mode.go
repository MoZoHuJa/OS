package scarlix_mode

import (
        "fmt"
        "os"
        "path/filepath"
        "strings"
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

// Current returns the current mode from current-mode file (written by scarlix-mode on host).
// v17.9.8 P1: TrimSpace — state file contains trailing newline from `echo "ai" | tee`.
func (m *Mode) Current() string {
        data, err := os.ReadFile("/var/lib/scarlix/current-mode")
        if err != nil {
                return "unknown"
        }
        return strings.TrimSpace(string(data))
}

// Set requests a mode switch by writing to desired-mode file (atomic rename).
// v18.0.0 P1: uses os.CreateTemp for unique temp file (was: same .tmp → concurrent race).
// v18.1: returns clear error if bridge-input/ dir doesn't exist (503 in API handler).
// The host bridge (scarlix-host-bridge.timer) applies it asynchronously within 5s.
// Returns nil if the mode is valid and the file was written; the actual switch is async.
func (m *Mode) Set(mode string) error {
        switch mode {
        case "ai", "stop", "game", "creative", "turbo", "offline", "tv":
                // valid
        default:
                return fmt.Errorf("invalid mode: %s (valid: ai, stop, game, creative, turbo, offline, tv)", mode)
        }

        dir := filepath.Dir(m.desiredModeFile)
        // v18.1: ensure bridge-input/ dir exists (install.sh creates it, but verify)
        if info, err := os.Stat(dir); err != nil || !info.IsDir() {
                return fmt.Errorf("bridge-input dir not accessible: %s (check install.sh Phase 5)", dir)
        }

        // v18.0.0 P1: unique temp file per request (prevents concurrent race)
        tmp, err := os.CreateTemp(dir, ".desired-mode-*")
        if err != nil {
                return fmt.Errorf("create temp file failed (permission?): %w", err)
        }
        tmpName := tmp.Name()
        defer os.Remove(tmpName) // cleanup if rename fails

        if _, err := tmp.WriteString(mode + "\n"); err != nil {
                tmp.Close()
                return fmt.Errorf("write desired-mode failed: %w", err)
        }
        tmp.Close()

        // Atomic rename (host bridge reads either old or new file — never half-written)
        if err := os.Rename(tmpName, m.desiredModeFile); err != nil {
                return fmt.Errorf("rename desired-mode failed: %w", err)
        }
        return nil
}

// AvailableModes returns all valid modes (must match scarlix-mode case statement).
func (m *Mode) AvailableModes() []string {
        return []string{"ai", "stop", "game", "creative", "turbo", "offline", "tv"}
}
