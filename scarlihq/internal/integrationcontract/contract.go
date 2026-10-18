// Package integrationcontract validates the machine-readable P4 component boundary map.
// It deliberately validates architecture metadata only; it does not claim that every
// listed component or contract is fully implemented.
package integrationcontract

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

//go:embed integration-contract.v1.json
var manifestJSON []byte

type Manifest struct {
	SchemaVersion string      `json:"schema_version"`
	Phase         string      `json:"phase"`
	Baseline      Baseline    `json:"baseline"`
	Components    []Component `json:"components"`
	ContractRules []Rule      `json:"contract_rules"`
}

type Baseline struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Version    string `json:"version"`
	Scope      string `json:"scope"`
}

type Component struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	Responsibility string   `json:"responsibility"`
	Owns           []string `json:"owns"`
	MustNotOwn     []string `json:"must_not_own"`
	DependsOn      []string `json:"depends_on"`
	Contracts      []string `json:"contracts"`
}

type Rule struct {
	ID   string `json:"id"`
	Rule string `json:"rule"`
}

// Load returns a fresh decoded copy of the embedded manifest.
func Load() (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(manifestJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("decode integration contract manifest: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("decode integration contract manifest: trailing JSON value")
		}
		return Manifest{}, fmt.Errorf("decode integration contract manifest trailing data: %w", err)
	}
	if err := Validate(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Validate checks structural invariants without asserting implementation maturity.
// A valid manifest is a consistent contract map, not proof of runtime wiring.
func Validate(m Manifest) error {
	if m.SchemaVersion != "1.0" {
		return fmt.Errorf("schema_version: unsupported value %q", m.SchemaVersion)
	}
	if m.Phase != "P4" {
		return fmt.Errorf("phase: expected P4, got %q", m.Phase)
	}
	if strings.TrimSpace(m.Baseline.Repository) == "" || strings.TrimSpace(m.Baseline.Branch) == "" ||
		strings.TrimSpace(m.Baseline.Version) == "" || strings.TrimSpace(m.Baseline.Scope) == "" {
		return fmt.Errorf("baseline: repository, branch, version, and scope are required")
	}
	if len(m.Components) == 0 || len(m.ContractRules) == 0 {
		return fmt.Errorf("components and contract_rules must not be empty")
	}

	components := make(map[string]Component, len(m.Components))
	for i, c := range m.Components {
		if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Name) == "" ||
			strings.TrimSpace(c.Responsibility) == "" {
			return fmt.Errorf("components[%d]: id, name, and responsibility are required", i)
		}
		switch c.Status {
		case "implemented", "partial", "planned":
		default:
			return fmt.Errorf("component %q: invalid status %q", c.ID, c.Status)
		}
		if _, exists := components[c.ID]; exists {
			return fmt.Errorf("component %q: duplicate id", c.ID)
		}
		if len(c.Owns) == 0 || len(c.MustNotOwn) == 0 {
			return fmt.Errorf("component %q: owns and must_not_own must be explicit", c.ID)
		}
		components[c.ID] = c
	}

	rules := make(map[string]bool, len(m.ContractRules))
	for i, r := range m.ContractRules {
		if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.Rule) == "" {
			return fmt.Errorf("contract_rules[%d]: id and rule are required", i)
		}
		if rules[r.ID] {
			return fmt.Errorf("contract rule %q: duplicate id", r.ID)
		}
		rules[r.ID] = true
	}
	for _, c := range m.Components {
		for _, dep := range c.DependsOn {
			if _, ok := components[dep]; !ok {
				return fmt.Errorf("component %q: unknown dependency %q", c.ID, dep)
			}
			if dep == c.ID {
				return fmt.Errorf("component %q: self dependency", c.ID)
			}
		}
		for _, id := range c.Contracts {
			if !rules[id] {
				return fmt.Errorf("component %q: unknown contract %q", c.ID, id)
			}
		}
	}
	if cycle := dependencyCycle(components); len(cycle) > 0 {
		return fmt.Errorf("component dependency cycle: %s", strings.Join(cycle, " -> "))
	}
	return nil
}

func dependencyCycle(components map[string]Component) []string {
	const (
		unseen = iota
		visiting
		visited
	)
	state := make(map[string]int, len(components))
	stack := make([]string, 0, len(components))
	var visit func(string) []string
	visit = func(id string) []string {
		switch state[id] {
		case visiting:
			start := 0
			for i, v := range stack {
				if v == id {
					start = i
					break
				}
			}
			cycle := append([]string(nil), stack[start:]...)
			return append(cycle, id)
		case visited:
			return nil
		}
		state[id] = visiting
		stack = append(stack, id)
		deps := append([]string(nil), components[id].DependsOn...)
		sort.Strings(deps)
		for _, dep := range deps {
			if cycle := visit(dep); len(cycle) > 0 {
				return cycle
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = visited
		return nil
	}
	ids := make([]string, 0, len(components))
	for id := range components {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if cycle := visit(id); len(cycle) > 0 {
			return cycle
		}
	}
	return nil
}
