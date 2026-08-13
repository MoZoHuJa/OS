package scarlix_mode

import (
	"fmt"
	"os"
	"os/exec"
)

// Mode manages scarlix-mode (ai/game/turbo/offline)
type Mode struct {
	stateFile string
}

// New creates a new Mode manager
func New() *Mode {
	return &Mode{stateFile: "/var/lib/scarlix/current-mode"}
}

// Current returns the current mode
func (m *Mode) Current() string {
	data, err := os.ReadFile(m.stateFile)
	if err != nil {
		return "unknown"
	}
	return string(data)
}

// Set changes the mode by running scarlix-mode script
func (m *Mode) Set(mode string) error {
	switch mode {
	case "ai", "game", "turbo", "offline":
		// valid
	default:
		return fmt.Errorf("invalid mode: %s", mode)
	}

	cmd := exec.Command("/usr/local/bin/scarlix-mode", mode)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scarlix-mode failed: %w", err)
	}

	return nil
}

// AvailableModes returns all valid modes
func (m *Mode) AvailableModes() []string {
	return []string{"ai", "game", "turbo", "offline"}
}
