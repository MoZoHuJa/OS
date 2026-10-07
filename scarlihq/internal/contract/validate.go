package contract

import (
	"fmt"
	"strings"
)

// ValidationError describes a single contract validation failure.
type ValidationError struct {
	Field   string // dotted path, e.g. "compute.vram_mb"
	Message string // human-readable reason
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("contract validation: %s: %s", e.Field, e.Message)
}

// ValidationErrors is a collection of validation failures. It implements error.
type ValidationErrors []ValidationError

func (errs ValidationErrors) Error() string {
	if len(errs) == 0 {
		return "no validation errors"
	}
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}

// HasErrors returns true if the collection contains at least one error.
func (errs ValidationErrors) HasErrors() bool {
	return len(errs) > 0
}

// allowed enum sets for validation
var allowedTaskTypes = map[string]bool{
	TaskTypeCoding: true, TaskTypeChat: true, TaskTypeResearch: true,
	TaskTypeEmbedding: true, TaskTypeIndexing: true,
}

var allowedPriorities = map[string]bool{
	PriorityRealtime: true, PriorityInteractive: true, PriorityNormal: true,
	PriorityBackground: true, PriorityBatch: true,
}

var allowedAccelerators = map[string]bool{
	AcceleratorCUDA: true, AcceleratorCPU: true, AcceleratorROCM: true,
}

var allowedFilesystemScopes = map[string]bool{
	FilesystemScopeWorkspace: true, FilesystemScopeNone: true,
}

var allowedNetworkScopes = map[string]bool{
	NetworkScopeRestricted: true, NetworkScopeNone: true,
}

var allowedShellScopes = map[string]bool{
	ShellScopeSandbox: true, ShellScopeNone: true,
}

// Validate checks a ResourceContract for structural + semantic validity.
// Returns a ValidationErrors collection (empty if valid). No silent correction
// of invalid requests — every problem is reported.
//
// v19.1.1: Reject rules per master guide section 11:
//   - missing IDs
//   - invalid resource types
//   - negative resources
//   - unsupported runtime
//   - invalid model
//   - invalid security scope
func (c *ResourceContract) Validate() ValidationErrors {
	var errs ValidationErrors

	// 1. Missing IDs
	if c.ID == "" {
		errs = append(errs, ValidationError{Field: "id", Message: "missing required field"})
	}
	if c.AgentID == "" {
		errs = append(errs, ValidationError{Field: "agent_id", Message: "missing required field"})
	}

	// 2. Version
	if c.Version == "" {
		errs = append(errs, ValidationError{Field: "version", Message: "missing required field"})
	} else if c.Version != VersionV1 {
		errs = append(errs, ValidationError{Field: "version", Message: fmt.Sprintf("unsupported version %q (only %q supported)", c.Version, VersionV1)})
	}

	// 3. Task validation
	if c.Task.Type == "" {
		errs = append(errs, ValidationError{Field: "task.type", Message: "missing required field"})
	} else if !allowedTaskTypes[c.Task.Type] {
		errs = append(errs, ValidationError{Field: "task.type", Message: fmt.Sprintf("invalid task type %q (allowed: coding, chat, research, embedding, indexing)", c.Task.Type)})
	}
	if c.Task.Priority == "" {
		errs = append(errs, ValidationError{Field: "task.priority", Message: "missing required field"})
	} else if !allowedPriorities[c.Task.Priority] {
		errs = append(errs, ValidationError{Field: "task.priority", Message: fmt.Sprintf("invalid priority %q (allowed: realtime, interactive, normal, background, batch)", c.Task.Priority)})
	}

	// 4. Compute validation
	if c.Compute.Accelerator == "" {
		errs = append(errs, ValidationError{Field: "compute.accelerator", Message: "missing required field"})
	} else if !allowedAccelerators[c.Compute.Accelerator] {
		errs = append(errs, ValidationError{Field: "compute.accelerator", Message: fmt.Sprintf("invalid accelerator %q (allowed: cuda, cpu, rocm)", c.Compute.Accelerator)})
	}
	if c.Compute.VRAMMB < 0 {
		errs = append(errs, ValidationError{Field: "compute.vram_mb", Message: fmt.Sprintf("negative value %d (must be >= 0)", c.Compute.VRAMMB)})
	}
	if c.Compute.CPUCores < 0 {
		errs = append(errs, ValidationError{Field: "compute.cpu_cores", Message: fmt.Sprintf("negative value %d (must be >= 0)", c.Compute.CPUCores)})
	}
	if c.Compute.RAMMB < 0 {
		errs = append(errs, ValidationError{Field: "compute.ram_mb", Message: fmt.Sprintf("negative value %d (must be >= 0)", c.Compute.RAMMB)})
	}

	// 5. Runtime validation — check preferred runtimes against known set
	knownRuntimes := map[string]bool{"sglang": true, "vllm": true, "beellama": true, "ollama": true, "litellm": true}
	for _, r := range c.Runtime.Preferred {
		if !knownRuntimes[r] {
			errs = append(errs, ValidationError{Field: "runtime.preferred", Message: fmt.Sprintf("unsupported runtime %q (known: sglang, vllm, beellama, ollama, litellm)", r)})
		}
	}

	// 6. Model validation — capabilities must be non-empty strings
	for i, cap := range c.Model.Capabilities {
		if cap == "" {
			errs = append(errs, ValidationError{Field: fmt.Sprintf("model.capabilities[%d]", i), Message: "empty capability string"})
		}
	}

	// 7. Security validation
	if c.Security.Filesystem == "" {
		errs = append(errs, ValidationError{Field: "security.filesystem", Message: "missing required field"})
	} else if !allowedFilesystemScopes[c.Security.Filesystem] {
		errs = append(errs, ValidationError{Field: "security.filesystem", Message: fmt.Sprintf("invalid scope %q (allowed: workspace, none)", c.Security.Filesystem)})
	}
	if c.Security.Network == "" {
		errs = append(errs, ValidationError{Field: "security.network", Message: "missing required field"})
	} else if !allowedNetworkScopes[c.Security.Network] {
		errs = append(errs, ValidationError{Field: "security.network", Message: fmt.Sprintf("invalid scope %q (allowed: restricted, none)", c.Security.Network)})
	}
	if c.Security.Shell == "" {
		errs = append(errs, ValidationError{Field: "security.shell", Message: "missing required field"})
	} else if !allowedShellScopes[c.Security.Shell] {
		errs = append(errs, ValidationError{Field: "security.shell", Message: fmt.Sprintf("invalid scope %q (allowed: sandbox, none)", c.Security.Shell)})
	}

	return errs
}

// ValidateStrict is like Validate but returns an error (not a collection) for
// convenience in CLI tools that just want pass/fail.
func (c *ResourceContract) ValidateStrict() error {
	errs := c.Validate()
	if errs.HasErrors() {
		return errs
	}
	return nil
}
