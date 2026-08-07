package profiles

import (
	"os"
	"path/filepath"
	"strings"
)

// Profile represents a user profile
type Profile struct {
	Name         string `json:"name" yaml:"name"`
	DisplayName  string `json:"display_name" yaml:"display_name"`
	Role         string `json:"role" yaml:"role"`
	HudTheme     string `json:"hud_theme" yaml:"hud_theme"`
	TokenBudget  string `json:"token_budget" yaml:"token_budget"`
}

// Manager manages user profiles
type Manager struct {
	dir string
}

// New creates a new profile manager
func New(dir string) *Manager {
	return &Manager{dir: dir}
}

// List returns all profiles
func (m *Manager) List() []Profile {
	var profiles []Profile

	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return profiles
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		profiles = append(profiles, Profile{
			Name:        name,
			DisplayName: name,
			HudTheme:    "default",
		})
	}

	return profiles
}

// Get returns a specific profile
func (m *Manager) Get(name string) (*Profile, error) {
	path := filepath.Join(m.dir, name+".yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, os.ErrNotExist
	}
	return &Profile{
		Name:        name,
		DisplayName: name,
	}, nil
}
