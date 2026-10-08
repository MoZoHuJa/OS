package scheduler

import (
	"strings"
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
		{ID: "sglang", Present: true, Capabilities: []string{"chat", "coding"}, SupportedRuntimes: []string{"sglang", "vllm"}},
	}
	s := New(gpus, runtimes, models, []inventory.Health{{Component: "gpu.nvidia.0", State: "healthy"}})

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
	models := []inventory.Model{
		{ID: "sglang", Format: "safetensors", SupportedRuntimes: []string{"sglang", "vllm"}, Present: true},
		{ID: "vllm", Format: "safetensors", SupportedRuntimes: []string{"sglang", "vllm"}, Present: true},
	}
	s := New(gpus, runtimes, models, []inventory.Health{
		{Component: "gpu.nvidia.0", State: "healthy"},
		{Component: "gpu.nvidia.1", State: "healthy"},
	})

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
		{ID: "sglang", Present: true, Capabilities: []string{"coding"}, SupportedRuntimes: []string{"sglang", "vllm"}},
	}
	s := New(gpus, runtimes, models, []inventory.Health{{Component: "gpu.nvidia.0", State: "healthy"}})

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
		Task:            "coding",
		Priority:        "interactive",
		SelectedRuntime: "sglang",
		Score:           100,
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
	// v19.1.10 P1-1: cpu must NOT match GPUs (only CPU runtimes)
	if acceleratorCompatible("cpu", gpu) {
		t.Error("cpu should NOT be compatible with any GPU (v19.1.10 P1-1 fix)")
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

// v19.1.10 P1-1: CPU request must NEVER select a GPU
func TestPlan_CPURequestNeverSelectsGPU(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true},
		{ID: "beellama", GPUIDs: []string{}, Running: true}, // CPU runtime
	}
	models := []inventory.Model{
		{ID: "sglang", Format: "safetensors", SupportedRuntimes: []string{"sglang"}, Present: true},
		{ID: "beellama", Format: "gguf", SupportedRuntimes: []string{"llamacpp"}, Present: true},
	}
	s := New(gpus, runtimes, models, []inventory.Health{{Component: "gpu.nvidia.0", State: "healthy"}})

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "background"},
		Compute: contract.ComputeSpec{Accelerator: "cpu"}, // CPU request!
		Runtime: contract.RuntimeSpec{Preferred: []string{"beellama"}},
	}

	plan := s.Plan(c)
	if plan.SelectedGPU != nil {
		t.Errorf("CPU request must NOT select a GPU, got %s", plan.SelectedGPU.ID)
	}
	if plan.SelectedRuntime != "beellama" {
		t.Errorf("expected beellama (CPU runtime), got %s", plan.SelectedRuntime)
	}
}

// v19.1.10 P1-2: Model with missing capabilities must be REJECTED (not lower-scored)
func TestPlan_RejectsModelWithMissingCapabilities(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true},
	}
	models := []inventory.Model{
		{ID: "sglang", Format: "safetensors", SupportedRuntimes: []string{"sglang"}, Present: true, Capabilities: []string{"chat"}}, // no "vision"
	}
	s := New(gpus, runtimes, models, []inventory.Health{{Component: "gpu.nvidia.0", State: "healthy"}})

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang"}},
		Model:   contract.ModelSpec{Capabilities: []string{"vision"}}, // model doesn't have "vision"
	}

	plan := s.Plan(c)
	if plan.SelectedGPU != nil {
		t.Errorf("model without 'vision' capability should be rejected, got GPU %s", plan.SelectedGPU.ID)
	}
}

// v19.1.10 P1-2: No model at all must reject the candidate
func TestPlan_RejectsWhenNoModelFound(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true},
	}
	// No models in registry
	s := New(gpus, runtimes, nil, nil)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang"}},
	}

	plan := s.Plan(c)
	if plan.SelectedGPU != nil {
		t.Errorf("no model should cause rejection, got GPU %s", plan.SelectedGPU.ID)
	}
	foundRejection := false
	for _, r := range plan.Rejected {
		if r.GPU == "gpu.nvidia.0" {
			foundRejection = true
			break
		}
	}
	if !foundRejection {
		t.Error("expected rejection reason for no model found")
	}
}

// v19.1.10 P1-3: Down runtime must be REJECTED (hard filter, not lower score)
func TestPlan_RejectsDownRuntime(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
		{ID: "gpu.nvidia.1", Index: 1, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true}, // DOWN
		{ID: "vllm", GPUIDs: []string{"gpu.nvidia.1"}, Running: true},   // HEALTHY
	}
	models := []inventory.Model{
		{ID: "sglang", Format: "safetensors", SupportedRuntimes: []string{"sglang"}, Present: true},
		{ID: "vllm", Format: "safetensors", SupportedRuntimes: []string{"vllm"}, Present: true},
	}
	health := []inventory.Health{
		{Component: "gpu.nvidia.0", State: "healthy"},
		{Component: "gpu.nvidia.1", State: "healthy"},
		{Component: "sglang", State: "down"},  // sglang is DOWN
		{Component: "vllm", State: "healthy"}, // vllm is HEALTHY
	}
	s := New(gpus, runtimes, models, health)

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang", "vllm"}},
	}

	plan := s.Plan(c)
	if plan.SelectedGPU == nil {
		t.Fatal("expected a selected GPU, got nil")
	}
	// sglang is DOWN → must NOT be selected. vllm is HEALTHY → should be selected.
	if plan.SelectedRuntime == "sglang" {
		t.Error("sglang is DOWN — must NOT be selected (P1-3 hard filter)")
	}
	if plan.SelectedRuntime != "vllm" {
		t.Errorf("expected vllm (healthy runtime), got %s", plan.SelectedRuntime)
	}
}

// v19.1.11 P1-1: ALL required capabilities must match (not just 1)
func TestPlan_AllCapabilitiesMustMatch(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true},
	}
	models := []inventory.Model{
		{ID: "sglang", Format: "safetensors", SupportedRuntimes: []string{"sglang"}, Present: true, Capabilities: []string{"coding", "reasoning"}}, // no "vision"
	}
	s := New(gpus, runtimes, models, []inventory.Health{{Component: "gpu.nvidia.0", State: "healthy"}})

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang"}},
		Model:   contract.ModelSpec{Capabilities: []string{"coding", "reasoning", "vision"}}, // wants 3, model has 2
	}

	plan := s.Plan(c)
	if plan.SelectedGPU != nil {
		t.Errorf("model with 2/3 capabilities should be REJECTED, got GPU %s", plan.SelectedGPU.ID)
	}
}

// v19.1.11 P1-2: FormatPlan for CPU shows runtime, not "no GPU found"
func TestFormatPlan_CPUPlanNotNoGPU(t *testing.T) {
	plan := &Plan{
		Task:            "coding",
		Priority:        "background",
		SelectedRuntime: "beellama",
		SelectedModel:   "beellama-model",
		Score:           50,
		Reason:          []ScoreBreakdown{{Factor: "accelerator", Score: 10, Detail: "CPU"}},
	}
	// SelectedGPU is nil, but SelectedRuntime is set → valid CPU plan
	formatted := plan.FormatPlan()
	if formatted == "" {
		t.Error("expected non-empty formatted plan")
	}
	// Should NOT say "No compatible GPU found"
	if strings.Contains(formatted, "No compatible GPU found") {
		t.Error("CPU plan should not say 'No compatible GPU found'")
	}
	// Should mention the runtime
	if !strings.Contains(formatted, "beellama") {
		t.Error("CPU plan should mention the runtime name")
	}
}

// v19.1.11 P1-3: scoreRuntime returns best-of, not first-match
func TestPlan_BestRuntimeSelected(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: false}, // not running → score 20
		{ID: "vllm", GPUIDs: []string{"gpu.nvidia.0"}, Running: true},    // running → score 25
	}
	models := []inventory.Model{
		{ID: "vllm-model", Format: "safetensors", SupportedRuntimes: []string{"vllm"}, Present: true, Capabilities: []string{"coding"}},
		{ID: "sglang-model", Format: "safetensors", SupportedRuntimes: []string{"sglang"}, Present: true, Capabilities: []string{"coding"}},
	}
	s := New(gpus, runtimes, models, []inventory.Health{
		{Component: "gpu.nvidia.0", State: "healthy"},
		{Component: "gpu.nvidia.1", State: "healthy"},
	})

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang", "vllm"}},
		Model:   contract.ModelSpec{Capabilities: []string{"coding"}},
	}

	plan := s.Plan(c)
	if plan.SelectedGPU == nil {
		t.Fatal("expected a selected GPU")
	}
	// vllm is running (score 25) > sglang not running (score 20) → vllm should win
	if plan.SelectedRuntime != "vllm" {
		t.Errorf("expected vllm (best score, running), got %s", plan.SelectedRuntime)
	}
}

// v19.1.11 P1-5: Empty accelerator defaults to cuda
func TestPlan_EmptyAcceleratorDefaultsCuda(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true},
	}
	models := []inventory.Model{
		{ID: "test-model", Format: "safetensors", SupportedRuntimes: []string{"sglang"}, Present: true, Capabilities: []string{"coding"}},
	}
	s := New(gpus, runtimes, models, []inventory.Health{{Component: "gpu.nvidia.0", State: "healthy"}})

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: ""}, // empty → should default to cuda
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang"}},
		Model:   contract.ModelSpec{Capabilities: []string{"coding"}},
	}

	plan := s.Plan(c)
	if plan.SelectedGPU == nil {
		t.Error("empty accelerator should default to cuda and find GPU")
	}
}

// v19.1.12 P1-B1: CPU-only runtime (empty GPUIDs) must NOT be selected on GPU path
func TestPlan_CPURuntimeNotOnGPUPath(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "beellama", GPUIDs: []string{}, Running: true},              // CPU-only, running
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: false}, // GPU, not running
	}
	models := []inventory.Model{
		{ID: "beellama-model", Format: "gguf", SupportedRuntimes: []string{"llamacpp"}, Present: true, Capabilities: []string{"coding"}},
		{ID: "sglang-model", Format: "safetensors", SupportedRuntimes: []string{"sglang"}, Present: true, Capabilities: []string{"coding"}},
	}
	s := New(gpus, runtimes, models, []inventory.Health{{Component: "gpu.nvidia.0", State: "healthy"}})

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang", "beellama"}},
		Model:   contract.ModelSpec{Capabilities: []string{"coding"}},
	}

	plan := s.Plan(c)
	if plan.SelectedRuntime == "beellama" {
		t.Error("beellama (CPU-only) must NOT be selected for a cuda request (P1-B1)")
	}
}

// v19.1.12 P1-2 (Review2): Model A incompatible → continue → Model B compatible → B selected
func TestPlan_MultiModelSelectionSkipsIncompatible(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true},
	}
	models := []inventory.Model{
		{ID: "model-a", Format: "safetensors", SupportedRuntimes: []string{"sglang"}, Present: true, Capabilities: []string{"chat"}},                // no "coding"
		{ID: "model-b", Format: "safetensors", SupportedRuntimes: []string{"sglang"}, Present: true, Capabilities: []string{"coding", "reasoning"}}, // has "coding"
	}
	s := New(gpus, runtimes, models, []inventory.Health{{Component: "gpu.nvidia.0", State: "healthy"}})

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
	if plan.SelectedModel != "model-b" {
		t.Errorf("expected model-b (has coding capability), got %s (model-a should have been skipped, not abort the loop)", plan.SelectedModel)
	}
}

// v19.1.12 P1-B2: Empty SupportedRuntimes = "supports all"
func TestPlan_EmptySupportedRuntimesNotRejected(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true},
	}
	models := []inventory.Model{
		{ID: "custom-model", Format: "safetensors", SupportedRuntimes: []string{}, Present: true, Capabilities: []string{"coding"}},
	}
	s := New(gpus, runtimes, models, []inventory.Health{{Component: "gpu.nvidia.0", State: "healthy"}})

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: "cuda", VRAMMB: 8000},
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang"}},
		Model:   contract.ModelSpec{Capabilities: []string{"coding"}},
	}

	plan := s.Plan(c)
	if plan.SelectedGPU == nil {
		t.Error("model with empty SupportedRuntimes should be treated as 'supports all' — not rejected")
	}
}

// v19.1.12 P1-B3: Plan() must NOT mutate caller's contract
func TestPlan_DoesNotMutateContract(t *testing.T) {
	gpus := []inventory.GPU{
		{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", VRAMTotalMB: 16384, VRAMFreeMB: 12000, Healthy: true},
	}
	runtimes := []inventory.Runtime{
		{ID: "sglang", GPUIDs: []string{"gpu.nvidia.0"}, Running: true},
	}
	models := []inventory.Model{
		{ID: "test-model", Format: "safetensors", SupportedRuntimes: []string{"sglang"}, Present: true, Capabilities: []string{"coding"}},
	}
	s := New(gpus, runtimes, models, []inventory.Health{{Component: "gpu.nvidia.0", State: "healthy"}})

	c := &contract.ResourceContract{
		Task:    contract.TaskSpec{Type: "coding", Priority: "interactive"},
		Compute: contract.ComputeSpec{Accelerator: ""}, // empty — should default to cuda internally
		Runtime: contract.RuntimeSpec{Preferred: []string{"sglang"}},
		Model:   contract.ModelSpec{Capabilities: []string{"coding"}},
	}

	_ = s.Plan(c)
	// After Plan(), the caller's contract should still have empty accelerator
	if c.Compute.Accelerator != "" {
		t.Errorf("Plan() must NOT mutate caller's contract — accelerator was changed to %q (was empty)", c.Compute.Accelerator)
	}
}
