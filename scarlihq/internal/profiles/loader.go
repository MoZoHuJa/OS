package profiles

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Profile represents a user profile (parsed from /etc/scarlix/profiles/*.yaml).
// v17.9.8 P1: fields now actually populated via yaml.Unmarshal (was: name used for
// both Name and DisplayName, other fields empty).
type Profile struct {
	Name        string `json:"name" yaml:"name"`
	DisplayName string `json:"display_name" yaml:"display_name"`
	Role        string `json:"role" yaml:"role"`
	HudTheme    string `json:"hud_theme" yaml:"hud_theme"`
	TokenBudget string `json:"token_budget" yaml:"token_budget"`
}

// Manager manages user profiles.
type Manager struct {
	dir string
}

// New creates a new profile manager.
func New(dir string) *Manager {
	return &Manager{dir: dir}
}

// List returns all profiles (parsed from YAML, not just filenames).
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
		path := filepath.Join(m.dir, entry.Name())

		data, err := os.ReadFile(path)
		if err != nil {
			// Fallback: filename-only profile (readable but unparseable)
			profiles = append(profiles, Profile{
				Name:        name,
				DisplayName: name,
				HudTheme:    "default",
			})
			continue
		}

		var p Profile
		if err := yaml.Unmarshal(data, &p); err != nil {
			// Fallback: filename-only profile (invalid YAML)
			profiles = append(profiles, Profile{
				Name:        name,
				DisplayName: name,
				HudTheme:    "default",
			})
			continue
		}

		// Ensure Name is set (from filename if not in YAML)
		if p.Name == "" {
			p.Name = name
		}
		if p.DisplayName == "" {
			p.DisplayName = p.Name
		}
		if p.HudTheme == "" {
			p.HudTheme = "default"
		}
		profiles = append(profiles, p)
	}

	return profiles
}

// Get returns a specific profile (parsed from YAML).
func (m *Manager) Get(name string) (*Profile, error) {
	path := filepath.Join(m.dir, name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}

	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		// Fallback: return filename-based profile
		return &Profile{
			Name:        name,
			DisplayName: name,
			HudTheme:    "default",
		}, nil
	}

	if p.Name == "" {
		p.Name = name
	}
	if p.DisplayName == "" {
		p.DisplayName = p.Name
	}
	if p.HudTheme == "" {
		p.HudTheme = "default"
	}
	return &p, nil
}
