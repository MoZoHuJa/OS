package integrationcontract

import (
	"strings"
	"testing"
)

func TestEmbeddedManifestIsValid(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(m.Components) != 9 {
		t.Fatalf("component count = %d, want 9", len(m.Components))
	}
	if m.Baseline.Branch != "19.2.10.G1" {
		t.Fatalf("baseline branch = %q, want 19.2.10.G1", m.Baseline.Branch)
	}
}

func TestValidateRejectsUnknownDependency(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	m.Components[0].DependsOn = []string{"missing-component"}
	err = Validate(m)
	if err == nil || !strings.Contains(err.Error(), "unknown dependency") {
		t.Fatalf("Validate() error = %v, want unknown dependency", err)
	}
}

func TestValidateRejectsUnknownContract(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	m.Components[0].Contracts = []string{"NO-SUCH-CONTRACT"}
	err = Validate(m)
	if err == nil || !strings.Contains(err.Error(), "unknown contract") {
		t.Fatalf("Validate() error = %v, want unknown contract", err)
	}
}

func TestValidateRejectsDependencyCycle(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	m.Components[0].DependsOn = []string{"host-agent"}
	for i := range m.Components {
		if m.Components[i].ID == "host-agent" {
			m.Components[i].DependsOn = []string{"scarlixos-os"}
		}
	}
	err = Validate(m)
	if err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("Validate() error = %v, want dependency cycle", err)
	}
}

func TestValidateRejectsDuplicateComponent(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	m.Components = append(m.Components, m.Components[0])
	err = Validate(m)
	if err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Fatalf("Validate() error = %v, want duplicate component id", err)
	}
}
