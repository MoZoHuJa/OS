// Package compat implements the GPU × Runtime × Model compatibility matrix
// (v19.1.8 per ScaRgeN master guide section 18).
package compat

import (
	"fmt"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

// Compatibility represents the compatibility of a GPU+Runtime+Model triple.
type Compatibility struct {
	GPU        inventory.GPU     `json:"gpu"`
	Runtime    inventory.Runtime `json:"runtime"`
	Model      inventory.Model   `json:"model"`
	Compatible bool              `json:"compatible"`
	Reason     string            `json:"reason"`
}

// Matrix is the full GPU × Runtime × Model compatibility matrix.
type Matrix struct {
	Entries []Compatibility `json:"entries"`
}

// BuildMatrix constructs the compatibility matrix from the current inventory.
func BuildMatrix(gpus []inventory.GPU, runtimes []inventory.Runtime, models []inventory.Model) *Matrix {
	m := &Matrix{Entries: []Compatibility{}}
	for _, gpu := range gpus {
		for _, rt := range runtimes {
			for _, mdl := range models {
				m.Entries = append(m.Entries, Check(gpu, rt, mdl))
			}
		}
	}
	return m
}

// Check evaluates the compatibility of a single GPU+Runtime+Model triple.
func Check(gpu inventory.GPU, rt inventory.Runtime, mdl inventory.Model) Compatibility {
	c := Compatibility{GPU: gpu, Runtime: rt, Model: mdl}

	if !runtimeSupportsModelFormat(rt, mdl) {
		c.Reason = fmt.Sprintf("runtime %s does not support model format %s", rt.ID, mdl.Format)
		return c
	}
	if len(mdl.SupportedRuntimes) > 0 && !contains(mdl.SupportedRuntimes, rt.ID) {
		c.Reason = fmt.Sprintf("model %s does not list runtime %s as supported", mdl.ID, rt.ID)
		return c
	}
	if mdl.EstimatedVRAM > 0 && gpu.VRAMTotalMB < mdl.EstimatedVRAM {
		c.Reason = fmt.Sprintf("GPU %s has %d MB VRAM, model %s needs %d MB", gpu.ID, gpu.VRAMTotalMB, mdl.ID, mdl.EstimatedVRAM)
		return c
	}
	if !gpuVendorMatchesRuntime(gpu, rt) {
		c.Reason = fmt.Sprintf("GPU vendor %s does not match runtime %s", gpu.Vendor, rt.ID)
		return c
	}
	if !gpuAssignedToRuntime(gpu, rt) {
		c.Reason = fmt.Sprintf("GPU %s is not assigned to runtime %s", gpu.ID, rt.ID)
		return c
	}
	c.Compatible = true
	return c
}

func (m *Matrix) Compatible() []Compatibility {
	result := []Compatibility{}
	for _, e := range m.Entries {
		if e.Compatible {
			result = append(result, e)
		}
	}
	return result
}

func (m *Matrix) Incompatible() []Compatibility {
	result := []Compatibility{}
	for _, e := range m.Entries {
		if !e.Compatible {
			result = append(result, e)
		}
	}
	return result
}

func (m *Matrix) ForGPU(gpuID string) []Compatibility {
	result := []Compatibility{}
	for _, e := range m.Entries {
		if e.GPU.ID == gpuID {
			result = append(result, e)
		}
	}
	return result
}

func (m *Matrix) ForRuntime(rtID string) []Compatibility {
	result := []Compatibility{}
	for _, e := range m.Entries {
		if e.Runtime.ID == rtID {
			result = append(result, e)
		}
	}
	return result
}

func (m *Matrix) Count() int           { return len(m.Entries) }
func (m *Matrix) CountCompatible() int { return len(m.Compatible()) }

func runtimeSupportsModelFormat(rt inventory.Runtime, mdl inventory.Model) bool {
	if mdl.Format == "" {
		return true
	}
	switch rt.ID {
	case "sglang", "vllm":
		return mdl.Format == "safetensors" || mdl.Format == "awq"
	case "beellama", "ollama":
		return mdl.Format == "gguf"
	case "litellm":
		return true
	default:
		return true
	}
}

func gpuVendorMatchesRuntime(gpu inventory.GPU, rt inventory.Runtime) bool {
	acc := acceleratorForRuntime(rt.ID)
	if acc == "cpu" {
		return true
	}
	if acc == "cuda" {
		return gpu.Vendor == "nvidia"
	}
	return true
}

func acceleratorForRuntime(rtID string) string {
	switch rtID {
	case "sglang", "vllm":
		return "cuda"
	default:
		return "cpu"
	}
}

func gpuAssignedToRuntime(gpu inventory.GPU, rt inventory.Runtime) bool {
	// v19.1.13 P1: CPU-only runtimes (empty GPUIDs) are NOT GPU-compatible.
	//   Was: returned true → beellama marked as compatible with RTX 5060 Ti.
	if len(rt.GPUIDs) == 0 {
		return false
	}
	for _, gid := range rt.GPUIDs {
		if gid == gpu.ID || gid == fmt.Sprintf("gpu.nvidia.%d", gpu.Index) {
			return true
		}
	}
	return false
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
