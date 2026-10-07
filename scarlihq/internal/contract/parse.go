package contract

import (
	"encoding/json"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ParseYAML parses a Resource Contract from YAML bytes.
//
// On success the returned contract has non-nil slices (Preferred and
// Capabilities are guaranteed to be either populated from the source or
// empty []string{}, never nil — to satisfy the stability contract that
// slices must serialize as [] not null).
//
// On error returns (nil, err) with a descriptive message.
func ParseYAML(data []byte) (*ResourceContract, error) {
	if len(data) == 0 {
		return nil, errors.New("contract: ParseYAML: empty input")
	}
	var c ResourceContract
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("contract: ParseYAML: %w", err)
	}
	normalize(&c)
	return &c, nil
}

// ParseJSON parses a Resource Contract from JSON bytes.
//
// Mirrors ParseYAML semantics: non-nil slices guaranteed on success,
// descriptive error on failure.
func ParseJSON(data []byte) (*ResourceContract, error) {
	if len(data) == 0 {
		return nil, errors.New("contract: ParseJSON: empty input")
	}
	var c ResourceContract
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("contract: ParseJSON: %w", err)
	}
	normalize(&c)
	return &c, nil
}

// MarshalYAML serializes a ResourceContract to YAML bytes.
//
// Nil slices are coerced to empty slices to satisfy the stability contract
// ([] not null). A nil contract returns an error.
//
// Equivalent to calling yaml.Marshal on a non-nil *ResourceContract — the
// RuntimeSpec and ModelSpec types implement yaml.Marshaler to enforce the
// [] not null rule regardless of call site.
func MarshalYAML(c *ResourceContract) ([]byte, error) {
	if c == nil {
		return nil, errors.New("contract: MarshalYAML: nil contract")
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("contract: MarshalYAML: %w", err)
	}
	return out, nil
}

// MarshalJSON serializes a ResourceContract to JSON bytes.
//
// Nil slices are coerced to empty slices to satisfy the stability contract
// ([] not null). A nil contract returns an error. The output is indented
// with 2 spaces (canonical pretty form for human consumption and CLI
// output).
//
// Equivalent to calling json.MarshalIndent on a non-nil *ResourceContract
// (modulo indentation) — the RuntimeSpec and ModelSpec types implement
// json.Marshaler to enforce the [] not null rule regardless of call site.
func MarshalJSON(c *ResourceContract) ([]byte, error) {
	if c == nil {
		return nil, errors.New("contract: MarshalJSON: nil contract")
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("contract: MarshalJSON: %w", err)
	}
	return out, nil
}

// Example returns a valid example ResourceContract (useful for docs +
// tests). It mirrors the canonical example from the ScaRgeN master guide
// section 10 (v19.1.0 Resource Contract v1).
//
// The returned contract is fully initialized — all slices are non-nil.
func Example() *ResourceContract {
	c := &ResourceContract{
		Version: VersionV1,
		ID:      "550e8400-e29b-41d4-a716-446655440000",
		AgentID: "agent.coder",
		Task: TaskSpec{
			Type:     TaskTypeCoding,
			Priority: PriorityInteractive,
		},
		Compute: ComputeSpec{
			Accelerator: AcceleratorCUDA,
			VRAMMB:      12000,
			CPUCores:    4,
			RAMMB:       8192,
		},
		Runtime: RuntimeSpec{
			Preferred: []string{"sglang", "vllm"},
		},
		Model: ModelSpec{
			Capabilities: []string{"coding", "reasoning"},
		},
		Security: SecuritySpec{
			Filesystem: FilesystemScopeWorkspace,
			Network:    NetworkScopeRestricted,
			Shell:      ShellScopeSandbox,
		},
	}
	normalize(c)
	return c
}

// ---------------------------------------------------------------------------
// Marshaler implementations — enforce the stability contract ([] not null)
// at the type level so any marshalling path (package functions, direct
// json.Marshal/yaml.Marshal, encoding/json encoder, etc.) honors it.
// ---------------------------------------------------------------------------

// MarshalJSON implements json.Marshaler for RuntimeSpec.
//
// Ensures Preferred serializes as `[]` not `null` when nil. Uses a type
// alias to delegate to the default encoding/json machinery without
// infinite recursion.
func (r RuntimeSpec) MarshalJSON() ([]byte, error) {
	type alias RuntimeSpec
	tmp := alias(r)
	if tmp.Preferred == nil {
		tmp.Preferred = []string{}
	}
	return json.Marshal(tmp)
}

// MarshalYAML implements yaml.Marshaler for RuntimeSpec.
//
// Ensures Preferred serializes as `[]` not `null` when nil. Uses a type
// alias to delegate to the default yaml.v3 reflection-based encoder
// without infinite recursion (alias has no MarshalYAML method).
func (r RuntimeSpec) MarshalYAML() (interface{}, error) {
	type alias RuntimeSpec
	tmp := alias(r)
	if tmp.Preferred == nil {
		tmp.Preferred = []string{}
	}
	return tmp, nil
}

// MarshalJSON implements json.Marshaler for ModelSpec.
//
// Ensures Capabilities serializes as `[]` not `null` when nil.
func (m ModelSpec) MarshalJSON() ([]byte, error) {
	type alias ModelSpec
	tmp := alias(m)
	if tmp.Capabilities == nil {
		tmp.Capabilities = []string{}
	}
	return json.Marshal(tmp)
}

// MarshalYAML implements yaml.Marshaler for ModelSpec.
//
// Ensures Capabilities serializes as `[]` not `null` when nil. Uses a type
// alias to avoid infinite recursion (alias has no MarshalYAML method).
func (m ModelSpec) MarshalYAML() (interface{}, error) {
	type alias ModelSpec
	tmp := alias(m)
	if tmp.Capabilities == nil {
		tmp.Capabilities = []string{}
	}
	return tmp, nil
}

// normalize ensures all slices are non-nil (so they serialize as [] not
// null) and the version is set to v1 if empty (defaulting rule for v1).
//
// This is idempotent and safe to call on partially-populated contracts.
// It is invoked by ParseYAML/ParseJSON and Example() to guarantee that
// the in-memory representation also reflects the stability contract
// (consumers iterating over Preferred/Capabilities won't need nil checks).
func normalize(c *ResourceContract) {
	if c == nil {
		return
	}
	if c.Version == "" {
		c.Version = VersionV1
	}
	if c.Runtime.Preferred == nil {
		c.Runtime.Preferred = []string{}
	}
	if c.Model.Capabilities == nil {
		c.Model.Capabilities = []string{}
	}
}
