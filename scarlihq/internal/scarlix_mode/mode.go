package scarlix_mode

import (
        "fmt"
        "os"
        "strings"
)

// Mode manages scarlix-mode via a file-based bridge (v17.9.8).
//
// ARCHITECTURE CHANGE (v17.9.8):
// Previously Set() exec'd /usr/local/bin/scarlix-mode directly inside the ScarliHQ
// container. That required mounting the host script + docker.sock + nvidia runtime
// into the container = privilege escalation nightmare, and it failed anyway because
// scarlix-mode needs yq/sudo/systemctl which aren't in the container image.
//
// Now Set() writes the desired mode to /var/lib/scarlix/desired-mode (a file the
// container CAN write to). The host-side scarlix-host-bridge systemd timer (runs as
// root, every 5s) reads that file and runs `scarlix-mode <mode>` on the host.
//
// Current() reads /var/lib/scarlix/current-mode (written by scarlix-mode itself).
type Mode struct {
        stateFile       string
        desiredModeFile string
}

// New creates a new Mode manager with default paths.
func New() *Mode {
        return &Mode{
                stateFile:       "/var/lib/scarlix/current-mode",
                desiredModeFile: "/var/lib/scarlix/bridge/desired-mode",
        }
}

// Current returns the current mode.
// v17.9.8 P1: TrimSpace — state file contains trailing newline from `echo "ai" | tee`.
func (m *Mode) Current() string {
        data, err := os.ReadFile(m.stateFile)
        if err != nil {
                return "unknown"
        }
        return strings.TrimSpace(string(data))
}

// Set requests a mode switch by writing to desired-mode file.
// The host bridge (scarlix-host-bridge.timer) applies it asynchronously within 5s.
// Returns nil if the mode is valid and the file was written; the actual switch is async.
func (m *Mode) Set(mode string) error {
        // v17.9.8 P1: accept ALL scarlix-mode modes (was only ai/game/turbo/offline —
        // missing stop/creative/tv → UI buttons broken).
        switch mode {
        case "ai", "stop", "game", "creative", "turbo", "offline", "tv":
                // valid
        default:
                return fmt.Errorf("invalid mode: %s (valid: ai, stop, game, creative, turbo, offline, tv)", mode)
        }

        // Write atomically: tmp file + rename, so host bridge never reads a half-written file.
        tmp := m.desiredModeFile + ".tmp"
        if err := os.WriteFile(tmp, []byte(mode+"\n"), 0644); err != nil {
                return fmt.Errorf("write desired-mode failed: %w", err)
        }
        if err := os.Rename(tmp, m.desiredModeFile); err != nil {
                return fmt.Errorf("rename desired-mode failed: %w", err)
        }
        return nil
}

// AvailableModes returns all valid modes (must match scarlix-mode case statement).
func (m *Mode) AvailableModes() []string {
        return []string{"ai", "stop", "game", "creative", "turbo", "offline", "tv"}
}
