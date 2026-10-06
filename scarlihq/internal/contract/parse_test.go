package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exampleYAML is the canonical Resource Contract v1 example from the
// ScaRgeN master guide section 10. It must round-trip through ParseYAML
// and match Example() field-for-field.
const exampleYAML = `version: v1
id: 550e8400-e29b-41d4-a716-446655440000
agent_id: agent.coder

task:
  type: coding
  priority: interactive

compute:
  accelerator: cuda
  vram_mb: 12000
  cpu_cores: 4
  ram_mb: 8192

runtime:
  preferred:
    - sglang
    - vllm

model:
  capabilities:
    - coding
    - reasoning

security:
  filesystem: workspace
  network: restricted
  shell: sandbox
`

// exampleJSON is the same contract expressed as JSON (used for JSON tests).
const exampleJSON = `{
  "version": "v1",
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "agent_id": "agent.coder",
  "task": {
    "type": "coding",
    "priority": "interactive"
  },
  "compute": {
    "accelerator": "cuda",
    "vram_mb": 12000,
    "cpu_cores": 4,
    "ram_mb": 8192
  },
  "runtime": {
    "preferred": ["sglang", "vllm"]
  },
  "model": {
    "capabilities": ["coding", "reasoning"]
  },
  "security": {
    "filesystem": "workspace",
    "network": "restricted",
    "shell": "sandbox"
  }
}`

// TestParseYAML_Valid verifies the master guide section 10 example YAML
// parses correctly and matches the expected field values.
func TestParseYAML_Valid(t *testing.T) {
	c, err := ParseYAML([]byte(exampleYAML))
	if err != nil {
		t.Fatalf("ParseYAML returned error: %v", err)
	}
	if c.Version != VersionV1 {
		t.Errorf("version: got %q want %q", c.Version, VersionV1)
	}
	if c.ID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("id: got %q", c.ID)
	}
	if c.AgentID != "agent.coder" {
		t.Errorf("agent_id: got %q", c.AgentID)
	}
	if c.Task.Type != TaskTypeCoding {
		t.Errorf("task.type: got %q want %q", c.Task.Type, TaskTypeCoding)
	}
	if c.Task.Priority != PriorityInteractive {
		t.Errorf("task.priority: got %q want %q", c.Task.Priority, PriorityInteractive)
	}
	if c.Compute.Accelerator != AcceleratorCUDA {
		t.Errorf("compute.accelerator: got %q want %q", c.Compute.Accelerator, AcceleratorCUDA)
	}
	if c.Compute.VRAMMB != 12000 {
		t.Errorf("compute.vram_mb: got %d want 12000", c.Compute.VRAMMB)
	}
	if c.Compute.CPUCores != 4 {
		t.Errorf("compute.cpu_cores: got %d want 4", c.Compute.CPUCores)
	}
	if c.Compute.RAMMB != 8192 {
		t.Errorf("compute.ram_mb: got %d want 8192", c.Compute.RAMMB)
	}
	if len(c.Runtime.Preferred) != 2 || c.Runtime.Preferred[0] != "sglang" || c.Runtime.Preferred[1] != "vllm" {
		t.Errorf("runtime.preferred: got %v want [sglang vllm]", c.Runtime.Preferred)
	}
	if len(c.Model.Capabilities) != 2 {
		t.Errorf("model.capabilities: got %v want [coding reasoning]", c.Model.Capabilities)
	}
	if c.Security.Filesystem != FilesystemScopeWorkspace {
		t.Errorf("security.filesystem: got %q want %q", c.Security.Filesystem, FilesystemScopeWorkspace)
	}
	if c.Security.Network != NetworkScopeRestricted {
		t.Errorf("security.network: got %q want %q", c.Security.Network, NetworkScopeRestricted)
	}
	if c.Security.Shell != ShellScopeSandbox {
		t.Errorf("security.shell: got %q want %q", c.Security.Shell, ShellScopeSandbox)
	}
}

// TestParseYAML_InvalidJSON verifies malformed YAML returns an error
// rather than silently producing a zero-value contract.
func TestParseYAML_InvalidJSON(t *testing.T) {
	malformed := []byte("version: v1\n  id: : broken: :\n\tbad indent: oops\n   - [")
	c, err := ParseYAML(malformed)
	if err == nil {
		t.Fatalf("ParseYAML expected error, got nil (contract=%+v)", c)
	}
	if c != nil {
		t.Fatalf("ParseYAML expected nil contract on error, got %+v", c)
	}
}

// TestParseYAML_EmptyInput verifies empty input is rejected.
func TestParseYAML_EmptyInput(t *testing.T) {
	if _, err := ParseYAML(nil); err == nil {
		t.Errorf("ParseYAML(nil) expected error")
	}
	if _, err := ParseYAML([]byte{}); err == nil {
		t.Errorf("ParseYAML([]byte{}) expected error")
	}
}

// TestParseJSON_Valid verifies the same example (as JSON) parses correctly.
func TestParseJSON_Valid(t *testing.T) {
	c, err := ParseJSON([]byte(exampleJSON))
	if err != nil {
		t.Fatalf("ParseJSON returned error: %v", err)
	}
	if c.Version != VersionV1 {
		t.Errorf("version: got %q want %q", c.Version, VersionV1)
	}
	if c.AgentID != "agent.coder" {
		t.Errorf("agent_id: got %q", c.AgentID)
	}
	if c.Compute.VRAMMB != 12000 {
		t.Errorf("compute.vram_mb: got %d want 12000", c.Compute.VRAMMB)
	}
	if len(c.Runtime.Preferred) != 2 {
		t.Errorf("runtime.preferred: got %v want [sglang vllm]", c.Runtime.Preferred)
	}
	if len(c.Model.Capabilities) != 2 {
		t.Errorf("model.capabilities: got %v want [coding reasoning]", c.Model.Capabilities)
	}
}

// TestMarshalYAML_RoundTrip verifies parse → marshal → parse produces an
// equal contract (by-value comparison after normalize).
func TestMarshalYAML_RoundTrip(t *testing.T) {
	original, err := ParseYAML([]byte(exampleYAML))
	if err != nil {
		t.Fatalf("initial ParseYAML: %v", err)
	}
	out, err := MarshalYAML(original)
	if err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	roundTripped, err := ParseYAML(out)
	if err != nil {
		t.Fatalf("second ParseYAML: %v", err)
	}
	if !contractsEqual(original, roundTripped) {
		t.Errorf("round-trip mismatch:\noriginal:  %+v\nroundtrip: %+v", original, roundTripped)
	}
}

// TestMarshalJSON_RoundTrip verifies parse → marshal → parse produces an
// equal contract for the JSON path.
func TestMarshalJSON_RoundTrip(t *testing.T) {
	original, err := ParseJSON([]byte(exampleJSON))
	if err != nil {
		t.Fatalf("initial ParseJSON: %v", err)
	}
	out, err := MarshalJSON(original)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	roundTripped, err := ParseJSON(out)
	if err != nil {
		t.Fatalf("second ParseJSON: %v", err)
	}
	if !contractsEqual(original, roundTripped) {
		t.Errorf("round-trip mismatch:\noriginal:  %+v\nroundtrip: %+v", original, roundTripped)
	}
}

// TestExample_ReturnsValid verifies Example() returns a contract that
// passes basic field checks and is serializable to both YAML and JSON.
func TestExample_ReturnsValid(t *testing.T) {
	c := Example()
	if c == nil {
		t.Fatal("Example() returned nil")
	}
	if c.Version != VersionV1 {
		t.Errorf("version: got %q want %q", c.Version, VersionV1)
	}
	if c.ID == "" {
		t.Error("id is empty")
	}
	if c.AgentID != "agent.coder" {
		t.Errorf("agent_id: got %q want %q", c.AgentID, "agent.coder")
	}
	if c.Runtime.Preferred == nil {
		t.Error("runtime.preferred is nil (must be non-nil per stability contract)")
	}
	if c.Model.Capabilities == nil {
		t.Error("model.capabilities is nil (must be non-nil per stability contract)")
	}
	// Must be serializable to both formats without error.
	if _, err := MarshalYAML(c); err != nil {
		t.Errorf("MarshalYAML(Example()): %v", err)
	}
	if _, err := MarshalJSON(c); err != nil {
		t.Errorf("MarshalJSON(Example()): %v", err)
	}
}

// TestFieldNames_MatchDataContract spot-checks the JSON tags against the
// canonical field names from the ScaRgeN master guide section 10 and the
// SCARLIX_DATA_CONTRACTS.md conventions.
func TestFieldNames_MatchDataContract(t *testing.T) {
	// Marshal Example() to JSON, then re-parse into a generic map so we
	// can check the EXACT key names at the wire level (not just struct
	// tag presence).
	c := Example()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal into map: %v", err)
	}

	// Top-level keys.
	for _, key := range []string{"version", "id", "agent_id", "task", "compute", "runtime", "model", "security"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing top-level JSON key %q in %s", key, string(raw))
		}
	}

	// Nested: task.type
	if task, ok := m["task"].(map[string]any); ok {
		if _, ok := task["type"]; !ok {
			t.Errorf("missing task.type in %v", task)
		}
		if _, ok := task["priority"]; !ok {
			t.Errorf("missing task.priority in %v", task)
		}
	} else {
		t.Errorf("task is not an object: %T", m["task"])
	}

	// Nested: compute.vram_mb (snake_case, MiB suffix).
	if compute, ok := m["compute"].(map[string]any); ok {
		for _, key := range []string{"accelerator", "vram_mb", "cpu_cores", "ram_mb"} {
			if _, ok := compute[key]; !ok {
				t.Errorf("missing compute.%s in %v", key, compute)
			}
		}
	} else {
		t.Errorf("compute is not an object: %T", m["compute"])
	}

	// Nested: runtime.preferred.
	if runtime, ok := m["runtime"].(map[string]any); ok {
		if _, ok := runtime["preferred"]; !ok {
			t.Errorf("missing runtime.preferred in %v", runtime)
		}
	} else {
		t.Errorf("runtime is not an object: %T", m["runtime"])
	}

	// Nested: model.capabilities.
	if model, ok := m["model"].(map[string]any); ok {
		if _, ok := model["capabilities"]; !ok {
			t.Errorf("missing model.capabilities in %v", model)
		}
	} else {
		t.Errorf("model is not an object: %T", m["model"])
	}

	// Nested: security.filesystem.
	if security, ok := m["security"].(map[string]any); ok {
		for _, key := range []string{"filesystem", "network", "shell"} {
			if _, ok := security[key]; !ok {
				t.Errorf("missing security.%s in %v", key, security)
			}
		}
	} else {
		t.Errorf("security is not an object: %T", m["security"])
	}
}

// TestExampleYAMLFileOnDisk verifies the example.yaml file shipped in
// this package parses correctly. This protects against drift between
// the in-code Example() and the example.yaml documentation file.
func TestExampleYAMLFileOnDisk(t *testing.T) {
	// Resolve the path relative to this test file.
	path := filepath.Join("example.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("cannot read %s: %v (skipping — run from package dir)", path, err)
	}
	c, err := ParseYAML(data)
	if err != nil {
		t.Fatalf("ParseYAML(example.yaml): %v", err)
	}
	if c.Version != VersionV1 {
		t.Errorf("version: got %q want %q", c.Version, VersionV1)
	}
	if c.AgentID != "agent.coder" {
		t.Errorf("agent_id: got %q want %q", c.AgentID, "agent.coder")
	}
}

// TestSlicesSerializeAsEmptyNotNull verifies the stability contract rule
// that slices must serialize as [] not null when empty.
func TestSlicesSerializeAsEmptyNotNull(t *testing.T) {
	c := &ResourceContract{
		Version: VersionV1,
		ID:      "test",
		AgentID: "agent.test",
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"preferred":[]`) {
		t.Errorf("preferred should serialize as [] not null: %s", s)
	}
	if !strings.Contains(s, `"capabilities":[]`) {
		t.Errorf("capabilities should serialize as [] not null: %s", s)
	}
}

// contractsEqual compares two contracts by value (used by round-trip
// tests). Returns true if they're field-for-field equal.
func contractsEqual(a, b *ResourceContract) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Version != b.Version || a.ID != b.ID || a.AgentID != b.AgentID {
		return false
	}
	if a.Task != b.Task {
		return false
	}
	if a.Compute != b.Compute {
		return false
	}
	if a.Security != b.Security {
		return false
	}
	if !slicesEqual(a.Runtime.Preferred, b.Runtime.Preferred) {
		return false
	}
	if !slicesEqual(a.Model.Capabilities, b.Model.Capabilities) {
		return false
	}
	return true
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
