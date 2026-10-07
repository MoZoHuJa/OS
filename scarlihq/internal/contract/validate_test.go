package contract

import (
	"testing"
)

// validContract returns a contract that passes all validation checks.
func validContract() *ResourceContract {
	return &ResourceContract{
		Version: VersionV1,
		ID:      "550e8400-e29b-41d4-a716-446655440000",
		AgentID: "agent.coder",
		Task:    TaskSpec{Type: TaskTypeCoding, Priority: PriorityInteractive},
		Compute: ComputeSpec{Accelerator: AcceleratorCUDA, VRAMMB: 12000, CPUCores: 4, RAMMB: 8192},
		Runtime: RuntimeSpec{Preferred: []string{"sglang", "vllm"}},
		Model:   ModelSpec{Capabilities: []string{"coding", "reasoning"}},
		Security: SecuritySpec{
			Filesystem: FilesystemScopeWorkspace, Network: NetworkScopeRestricted, Shell: ShellScopeSandbox,
		},
	}
}

func TestValidate_ValidContract(t *testing.T) {
	c := validContract()
	errs := c.Validate()
	if errs.HasErrors() {
		t.Fatalf("valid contract should have no errors, got: %v", errs)
	}
}

func TestValidate_MissingID(t *testing.T) {
	c := validContract()
	c.ID = ""
	errs := c.Validate()
	if !hasField(errs, "id") {
		t.Errorf("expected error for missing id, got: %v", errs)
	}
}

func TestValidate_MissingAgentID(t *testing.T) {
	c := validContract()
	c.AgentID = ""
	errs := c.Validate()
	if !hasField(errs, "agent_id") {
		t.Errorf("expected error for missing agent_id, got: %v", errs)
	}
}

func TestValidate_InvalidTaskType(t *testing.T) {
	c := validContract()
	c.Task.Type = "bogus"
	errs := c.Validate()
	if !hasField(errs, "task.type") {
		t.Errorf("expected error for invalid task.type, got: %v", errs)
	}
}

func TestValidate_InvalidPriority(t *testing.T) {
	c := validContract()
	c.Task.Priority = "urgent"
	errs := c.Validate()
	if !hasField(errs, "task.priority") {
		t.Errorf("expected error for invalid task.priority, got: %v", errs)
	}
}

func TestValidate_NegativeVRAM(t *testing.T) {
	c := validContract()
	c.Compute.VRAMMB = -100
	errs := c.Validate()
	if !hasField(errs, "compute.vram_mb") {
		t.Errorf("expected error for negative vram_mb, got: %v", errs)
	}
}

func TestValidate_NegativeCPUCores(t *testing.T) {
	c := validContract()
	c.Compute.CPUCores = -2
	errs := c.Validate()
	if !hasField(errs, "compute.cpu_cores") {
		t.Errorf("expected error for negative cpu_cores, got: %v", errs)
	}
}

func TestValidate_NegativeRAM(t *testing.T) {
	c := validContract()
	c.Compute.RAMMB = -512
	errs := c.Validate()
	if !hasField(errs, "compute.ram_mb") {
		t.Errorf("expected error for negative ram_mb, got: %v", errs)
	}
}

func TestValidate_InvalidAccelerator(t *testing.T) {
	c := validContract()
	c.Compute.Accelerator = "tpu"
	errs := c.Validate()
	if !hasField(errs, "compute.accelerator") {
		t.Errorf("expected error for invalid accelerator, got: %v", errs)
	}
}

func TestValidate_UnsupportedRuntime(t *testing.T) {
	c := validContract()
	c.Runtime.Preferred = []string{"sglang", "nonexistent-runtime"}
	errs := c.Validate()
	if !hasField(errs, "runtime.preferred") {
		t.Errorf("expected error for unsupported runtime, got: %v", errs)
	}
}

func TestValidate_EmptyModelCapability(t *testing.T) {
	c := validContract()
	c.Model.Capabilities = []string{"coding", ""}
	errs := c.Validate()
	if !hasFieldContains(errs, "model.capabilities") {
		t.Errorf("expected error for empty capability, got: %v", errs)
	}
}

func TestValidate_InvalidFilesystemScope(t *testing.T) {
	c := validContract()
	c.Security.Filesystem = "root"
	errs := c.Validate()
	if !hasField(errs, "security.filesystem") {
		t.Errorf("expected error for invalid filesystem scope, got: %v", errs)
	}
}

func TestValidate_InvalidNetworkScope(t *testing.T) {
	c := validContract()
	c.Security.Network = "public"
	errs := c.Validate()
	if !hasField(errs, "security.network") {
		t.Errorf("expected error for invalid network scope, got: %v", errs)
	}
}

func TestValidate_InvalidShellScope(t *testing.T) {
	c := validContract()
	c.Security.Shell = "root"
	errs := c.Validate()
	if !hasField(errs, "security.shell") {
		t.Errorf("expected error for invalid shell scope, got: %v", errs)
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	c := &ResourceContract{} // all fields empty
	errs := c.Validate()
	if len(errs) < 5 {
		t.Errorf("expected at least 5 errors for empty contract, got %d: %v", len(errs), errs)
	}
}

func TestValidateStrict_ReturnsNilOnValid(t *testing.T) {
	c := validContract()
	if err := c.ValidateStrict(); err != nil {
		t.Errorf("ValidateStrict should return nil for valid contract, got: %v", err)
	}
}

func TestValidateStrict_ReturnsErrorOnInvalid(t *testing.T) {
	c := validContract()
	c.ID = ""
	if err := c.ValidateStrict(); err == nil {
		t.Error("ValidateStrict should return error for invalid contract")
	}
}

func TestValidationErrors_ErrorString(t *testing.T) {
	errs := ValidationErrors{
		ValidationError{Field: "id", Message: "missing"},
		ValidationError{Field: "agent_id", Message: "missing"},
	}
	s := errs.Error()
	if s == "" {
		t.Error("Error() should not be empty")
	}
}

// hasField checks if any error in the collection matches the given field.
func hasField(errs ValidationErrors, field string) bool {
	for _, e := range errs {
		if e.Field == field {
			return true
		}
	}
	return false
}

// hasFieldContains checks if any error field CONTAINS the given substring.
func hasFieldContains(errs ValidationErrors, substr string) bool {
	for _, e := range errs {
		if contains(e.Field, substr) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
