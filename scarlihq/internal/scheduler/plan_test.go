package scheduler

import (
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/contract"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

func TestPlan_NoGPUsReturnsEmptyPlan(t *testing.T) {
	s := New(nil, nil, nil, nil)
	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
	}
	plan := s.Plan(c)
	if plan.SelectedGPU != nil {
		t.Errorf("expected nil SelectedGPU when no GPUs, got %+v", plan.SelectedGPU)
	}
}

func TestPlan_SelectsCompatibleGPU(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, UtilizationPct: 10, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true, Healthy: true},
	}
	models := []inventory.Model{
		{ID: "sglang", Present: true, Capabilities: []string{"chat", "coding"}},
	}
	s := New(gpus, runtimes, models, nil)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang"}},
		Model:   contract.ModelSpec{Capabilities: []string{"coding"}},
	}

	plan := s.Plan(c)
	if plan.SelectedGPU == nil {
		t.Fatal("expected a selected GPU, got nil")
	}
	if plan.SelectedGPU.ID != "gpu.nvidia.0" {
		t.Errorf("expected gpu.nvidia.0, got %s", plan.SelectedGPU.ID)
	}
	if plan.SelectedRuntime != "sglang" {
		t.Errorf("expected sglang runtime, got %s", plan.SelectedRuntime)
	}
}

func TestPlan_RejectsInsufficientVRAM(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 4000, UtilizationPct: 10, Healthy: true},
	}
	s := New(gpus, nil, nil, nil)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
	}

	plan := s.Plan(c)
	if plan.SelectedGPU != nil {
		t.Errorf("expected nil (VRAM insufficient), got %+v", plan.SelectedGPU)
	}
	if len(plan.Rejected) == 0 {
		t.Error("expected rejection for insufficient VRAM")
	}
}

func TestPlan_RejectsWrongAccelerator(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	s := New(gpus, nil, nil, nil)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "rocm"}, // want AMD, have NVIDIA
	}

	plan := s.Plan(c)
	if plan.SelectedGPU != nil {
		t.Errorf("expected nil (wrong accelerator), got %+v", plan.SelectedGPU)
	}
}

func TestPlan_PrefersLowUtilizationGPU(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, UtilizationPct: 90, Healthy: true},
		{ID: "gpu.nvidia.1", Index: 1, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, UtilizationPct: 10, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true},
		{ID: "vllm", GPUIDs: []string{"gpu.nvidia.1"}, Running: true},
	}
	s := New(gpus, runtimes, nil, nil)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang", "vllm"}},
	}

	plan := s.Plan(c)
	if plan.SelectedGPU == nil {
		t.Fatal("expected a selected GPU, got nil")
	}
	// GPU 1 has lower utilization → should be selected
	if plan.SelectedGPU.ID != "gpu.nvidia.1" {
		t.Errorf("expected gpu.nvidia.1 (low util), got %s", plan.SelectedGPU.ID)
	}
}

func TestPlan_ScoreBreakdownNotEmpty(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, UtilizationPct: 10, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true},
	}
	models := []inventory.Model{
		{ID: "sglang", Present: true, Capabilities: []string{"coding"}},
	}
	s := New(gpus, runtimes, models, nil)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang"}},
		Model:   contract.ModelSpec{Capabilities: []string{"coding"}},
	}

	plan := s.Plan(c)
	if len(plan.Reason) == 0 {
		t.Error("expected non-empty reason breakdown")
	}
}

func TestFormatPlan_HumanReadable(t *testing.T) {
	plan := &Plan{
		Task:     "coding",
		Priority: "interactive",
		SelectedRuntime: "sglang",
		Score:    100,
	}
	plan.SelectedGPU = &inventory.GPU{ID: "gpu.nvidia.0"}
	plan.Reason = []ScoreBreakdown{
		{Factor: "vram_fit", Score: 30, Detail: "fits"},
	}
	formatted := plan.FormatPlan()
	if formatted == "" {
		t.Error("expected non-empty formatted plan")
	}
}

func TestAcceleratorCompatible(t *testing.T) {
	gpu := inventory.GPU{Vendor: "nvidia"}
	if !acceleratorCompatible("cuda", gpu) {
		t.Error("cuda should be compatible with nvidia GPU")
	}
	if acceleratorCompatible("rocm", gpu) {
		t.Error("rocm should not be compatible with nvidia GPU")
	}
	if !acceleratorCompatible("cpu", gpu) {
		t.Error("cpu should always be compatible")
	}
}

func TestPlan_RejectsUnhealthyGPU(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: false},
	}
	health := []inventory.Health{
		{Component: "gpu.nvidia.0", State: "unhealthy"},
	}
	s := New(gpus, nil, nil, health)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
	}

	plan := s.Plan(c)
	// GPU should be rejected due to unhealthy state
	found := false
	for _, r := range plan.Rejected {
		if r.GPU == "gpu.nvidia.0" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected rejection for unhealthy GPU")
	}
}
