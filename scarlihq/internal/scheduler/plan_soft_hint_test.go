package scheduler

import (
	"strings"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/contract"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

// v19.3.2 (Zmor-7 closure): preferred runtime becomes a SOFT hint.
// When no preferred runtime is eligible, the planner falls back to any
// eligible runtime and says so in the plan breakdown; when nothing at all
// is eligible, it still fails closed (no plan).

func TestPlan_PreferredRuntimeDown_FallsBackToEligibleRuntime(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, UtilizationPct: 10, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: false, Healthy: true},
		{ID: "vllm", GPUIDs: []string{"gpu.nvidia.0"}, Running: true, Healthy: true},
	}
	models := []inventory.Model{
		{ID: "qwen", Present: true, Capabilities: []string{"coding"}, SupportedRuntimes: []string{"sglang", "vllm"}},
	}
	health := []inventory.Health{
		{Component: "gpu.nvidia.0", State: "healthy"},
		{Component: "sglang", State: "down"},
		{Component: "vllm", State: "healthy"},
	}
	s := New(gpus, runtimes, models, health)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang"}},
		Model:   contract.ModelSpec{Capabilities: []string{"coding"}},
	}

	plan := s.Plan(c)
	if plan.SelectedGPU == nil {
		t.Fatal("expected a selected GPU via fallback, got nil — the down preferred runtime must not strand the request")
	}
	if plan.SelectedRuntime != "vllm" {
		t.Fatalf("expected vllm via soft-hint fallback, got %q", plan.SelectedRuntime)
	}
	explains := false
	for _, r := range plan.Reason {
		if strings.Contains(r.Detail, "soft-hint fallback") {
			explains = true
		}
	}
	if !explains {
		t.Fatalf("plan must explain the fallback in the breakdown, got %+v", plan.Reason)
	}
}

func TestPlan_PreferredRuntimeDown_NoAlternative_FailsClosed(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: false, Healthy: true},
	}
	health := []inventory.Health{
		{Component: "gpu.nvidia.0", State: "healthy"},
		{Component: "sglang", State: "down"},
	}
	s := New(gpus, runtimes, nil, health)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang"}},
	}

	plan := s.Plan(c)
	if plan.SelectedGPU != nil {
		t.Fatalf("expected fail-closed (no runtime at all), got GPU %s runtime %s", plan.SelectedGPU.ID, plan.SelectedRuntime)
	}
}

func TestPlan_PreferredRuntimeHealthy_IsHonored(t *testing.T) {
	// Regression guard: the fallback must never override a WORKING preference.
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true, Healthy: true},
		{ID: "vllm", GPUIDs: []string{"gpu.nvidia.0"}, Running: true, Healthy: true},
	}
	models := []inventory.Model{
		{ID: "qwen", Present: true, Capabilities: []string{"coding"}, SupportedRuntimes: []string{"sglang", "vllm"}},
	}
	health := []inventory.Health{
		{Component: "gpu.nvidia.0", State: "healthy"},
		{Component: "sglang", State: "healthy"},
		{Component: "vllm", State: "healthy"},
	}
	s := New(gpus, runtimes, models, health)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang"}},
	}

	plan := s.Plan(c)
	if plan.SelectedRuntime != "sglang" {
		t.Fatalf("healthy preferred runtime must be selected, got %q", plan.SelectedRuntime)
	}
	for _, r := range plan.Reason {
		if strings.Contains(r.Detail, "fallback") {
			t.Fatalf("no fallback must be reported when the preference is honored, got %+v", r)
		}
	}
}

func TestPlan_PreferredCPURuntimeDown_FallsBack(t *testing.T) {
	runtimes := []inventory.Runtime{
		{ID: "beellama", GPUIDs: nil, Running: false, Healthy: true},
		{ID: "ollama", GPUIDs: nil, Running: true, Healthy: true},
	}
	models := []inventory.Model{
		{ID: "llama3", Format: "gguf", Present: true, Capabilities: []string{"coding"}, SupportedRuntimes: []string{"llamacpp", "ollama"}},
	}
	health := []inventory.Health{
		{Component: "beellama", State: "down"},
		{Component: "ollama", State: "healthy"},
	}
	s := New(nil, runtimes, models, health)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "background"},
		Compute: contract.ComputeSpec{Accelerator: "cpu"},
		Runtime: contract.RuntimeSpec{Preferred: []string{"beellama"}},
	}

	plan := s.Plan(c)
	if plan.SelectedRuntime != "ollama" {
		t.Fatalf("expected ollama via CPU soft-hint fallback, got %q", plan.SelectedRuntime)
	}
	explains := false
	for _, r := range plan.Reason {
		if strings.Contains(r.Detail, "soft-hint fallback") {
			explains = true
		}
	}
	if !explains {
		t.Fatalf("plan must explain the CPU fallback, got %+v", plan.Reason)
	}
}

func TestPlan_PreferredCPURuntimeDown_NoAlternative_FailsClosed(t *testing.T) {
	runtimes := []inventory.Runtime{
		{ID: "beellama", GPUIDs: nil, Running: false, Healthy: true},
	}
	health := []inventory.Health{
		{Component: "beellama", State: "down"},
	}
	s := New(nil, runtimes, nil, health)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "background"},
		Compute: contract.ComputeSpec{Accelerator: "cpu"},
		Runtime: contract.RuntimeSpec{Preferred: []string{"beellama"}},
	}

	plan := s.Plan(c)
	if plan.SelectedRuntime != "" || plan.SelectedModel != "" {
		t.Fatalf("expected fail-closed CPU plan, got runtime=%q model=%q", plan.SelectedRuntime, plan.SelectedModel)
	}
}
