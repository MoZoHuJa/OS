package integrationcontract

import (
	"bytes"
	"encoding/json"
	"testing"

	resourcecontract "github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
	resourcecontract "github.com/MoZoHuJa/OS/scarlihq/internal/contract"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

func TestConfiguredRuntimeIDsMatchResourceContractAllowlist(t *testing.T) {
	configured := inventory.ConfiguredRuntimeIDs()
	accepted := resourcecontract.SupportedRuntimeIDs()

	if len(configured) == 0 {
		t.Fatal("inventory must declare at least one configured runtime")
	}
	configuredSet := make(map[string]bool, len(configured))
	for _, id := range configured {
		if id == "" {
			t.Fatal("inventory returned an empty runtime ID")
		}
		if configuredSet[id] {
			t.Fatalf("inventory returned duplicate runtime ID %q", id)
		}
		configuredSet[id] = true
		if !resourcecontract.IsSupportedRuntime(id) {
			t.Errorf("inventory runtime %q is rejected by Resource Contract v1", id)
		}
	}

	acceptedSet := make(map[string]bool, len(accepted))
	for _, id := range accepted {
		if id == "" {
			t.Fatal("Resource Contract returned an empty supported runtime ID")
		}
		if acceptedSet[id] {
			t.Fatalf("Resource Contract returned duplicate runtime ID %q", id)
		}
		acceptedSet[id] = true
		if !configuredSet[id] {
			t.Errorf("Resource Contract accepts runtime %q that inventory does not configure", id)
		}
	}
	if len(configuredSet) != len(acceptedSet) {
		t.Errorf("runtime set sizes differ: inventory=%v contract=%v", configured, accepted)
	}
}

func TestResourceContractV1EmptySlicesSerializeAsArrays(t *testing.T) {
	c := &resourcecontract.ResourceContract{
		Version: resourcecontract.VersionV1,
		ID:      "550e8400-e29b-41d4-a716-446655440000",
		AgentID: "agent.integration-test",
		Task:    resourcecontract.TaskSpec{Type: resourcecontract.TaskTypeCoding, Priority: resourcecontract.PriorityInteractive},
		Compute: resourcecontract.ComputeSpec{Accelerator: resourcecontract.AcceleratorCPU},
		Runtime: resourcecontract.RuntimeSpec{Preferred: nil},
		Model:   resourcecontract.ModelSpec{Capabilities: nil},
		Security: resourcecontract.SecuritySpec{
			Filesystem: resourcecontract.FilesystemScopeWorkspace,
			Network:    resourcecontract.NetworkScopeRestricted,
			Shell:      resourcecontract.ShellScopeSandbox,
		},
	}
	if err := c.ValidateStrict(); err != nil {
		t.Fatalf("valid Resource Contract v1 rejected: %v", err)
	}
	data, err := resourcecontract.MarshalJSON(c)
	if err != nil {
		t.Fatalf("marshal Resource Contract v1: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode marshaled contract: %v", err)
	}
	for _, path := range []string{"runtime", "model"} {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(payload[path], &nested); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		key := "preferred"
		if path == "model" {
			key = "capabilities"
		}
		got := bytes.TrimSpace(nested[key])
		if !bytes.Equal(got, []byte("[]")) {
			t.Errorf("%s.%s = %s, want []", path, key, got)
		}
	}
}

func TestInventorySnapshotKeepsStableArraysAndHidesGPUUUID(t *testing.T) {
	snapshot := inventory.SystemStatus{
		Version:  "19.2.10",
		Mode:     "ai",
		Timestamp: "2026-10-18T00:00:00Z",
		GPUs:     []inventory.GPU{{ID: "gpu.nvidia.0", UUID: "GPU-private-test-uuid", Vendor: "nvidia", Name: "test"}},
		Runtimes: []inventory.Runtime{},
		Models:   []inventory.Model{},
		Services: []inventory.Service{},
		Health:   []inventory.Health{},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal inventory snapshot: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode inventory snapshot: %v", err)
	}
	for _, key := range []string{"gpus", "runtimes", "models", "services", "health"} {
		if got := bytes.TrimSpace(payload[key]); !bytes.HasPrefix(got, []byte("[")) || bytes.Equal(got, []byte("null")) {
			t.Errorf("inventory.%s = %s, want JSON array", key, got)
		}
	}
	if bytes.Contains(data, []byte("GPU-private-test-uuid")) {
		t.Fatal("internal GPU UUID leaked into frozen public inventory JSON")
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["gpus"]; !ok {
		t.Fatalf("inventory snapshot missing gpus key: %s", data)
	}
}

func TestAllocationSchemaV1ConsumesResourceContractVersion(t *testing.T) {
	record := allocation.NewAllocation(
		"allocation-integration-test",
		string(resourcecontract.VersionV1),
		resourcecontract.TaskTypeCoding,
		resourcecontract.PriorityInteractive,
		"agent.integration-test",
	)
	if record.ContractVersion != string(resourcecontract.VersionV1) {
		t.Fatalf("allocation contract version = %q, want %q", record.ContractVersion, resourcecontract.VersionV1)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal Allocation v1: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode Allocation v1: %v", err)
	}
	if got := string(bytes.TrimSpace(payload["schema_version"])); got != `"v1"` {
		t.Errorf("allocation schema_version = %s, want \"v1\"", got)
	}
	if got := string(bytes.TrimSpace(payload["history"])); got != "[]" {
		t.Errorf("allocation history = %s, want []", got)
	}
}
