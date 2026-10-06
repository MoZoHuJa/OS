// Package scheduler implements the deterministic resource scheduler for the
// ScaRgeN Compute Fabric. v19.1.7 implements the DRY-RUN scheduler per master
// guide section 17: it scores GPU+runtime+model combinations and returns a
// plan, but performs NO actual allocation.
//
// The scoring is deterministic (no AI scheduler per master guide section 95):
//
//	score = vram_fit + runtime_fit + model_fit + latency_fit + health
//	      - utilization_penalty - temperature_penalty
//
// This is the foundation that v19.2.0 (real Compute Fabric) will build on
// by adding actual allocation + lease management.
package scheduler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MoZoHuJa/OS/scarlihq/internal/contract"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

// Plan is the output of a dry-run scheduling decision. It describes what the
// scheduler WOULD allocate, without actually allocating anything.
type Plan struct {
	Task         string         `json:"task"`          // e.g. "coding"
	Priority     string         `json:"priority"`       // e.g. "interactive"
	SelectedGPU  *inventory.GPU `json:"selected_gpu"`  // nil if none compatible
	SelectedRuntime string      `json:"selected_runtime"` // e.g. "sglang"
	SelectedModel   string      `json:"selected_model"`   // e.g. "qwen3-14b-awq"
	Score         int            `json:"score"`          // total score
	Reason       []ScoreBreakdown `json:"reason"`        // scoring breakdown
	Rejected     []Rejection     `json:"rejected"`      // why other options were rejected
}

// ScoreBreakdown shows how each scoring factor contributed.
type ScoreBreakdown struct {
	Factor string `json:"factor"`  // "vram_fit", "runtime_fit", etc.
	Score  int    `json:"score"`    // points awarded (can be negative)
	Detail string `json:"detail"`  // human-readable explanation
}

// Rejection describes why a GPU+runtime+model combo was rejected.
type Rejection struct {
	GPU     string `json:"gpu"`
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
	Reason  string `json:"reason"`
}

// Scheduler is the dry-run scheduler. It reads from inventory collectors +
// produces a Plan. Thread-safe (stateless — each Plan() call is independent).
type Scheduler struct {
	gpus     []inventory.GPU
	runtimes []inventory.Runtime
	models   []inventory.Model
	health   map[string]inventory.Health
}

// New creates a Scheduler from the current system inventory snapshot.
// Call inventory.CollectGPUs/Runtimes/Models/CollectGPUHealth to populate.
func New(gpus []inventory.GPU, runtimes []inventory.Runtime, models []inventory.Model, health []inventory.Health) *Scheduler {
	hMap := make(map[string]inventory.Health, len(health))
	for _, h := range health {
		hMap[h.Component] = h
	}
	return &Scheduler{
		gpus:     gpus,
		runtimes: runtimes,
		models:   models,
		health:   hMap,
	}
}

// Plan produces a dry-run scheduling decision for the given contract.
// NO actual allocation is performed. The Plan describes what the scheduler
// WOULD do if asked to allocate.
//
// Per master guide section 17, the output format is:
//
//	Task: coding
//	Selected GPU: gpu.nvidia.1
//	Runtime: sglang
//	Reason:
//	  VRAM fit       +30
//	  utilization    +20
//	  runtime fit    +20
//	  latency        +20
//	  health         +10
func (s *Scheduler) Plan(c *contract.ResourceContract) *Plan {
	plan := &Plan{
		Task:     c.Task.Type,
		Priority: c.Task.Priority,
		Reason:  []ScoreBreakdown{},
		Rejected: []Rejection{},
	}

	// Score each GPU that is compatible with the contract's accelerator
	type scored struct {
		gpu       inventory.GPU
		runtime   string
		model     string
		score     int
		breakdown []ScoreBreakdown
	}
	var candidates []scored

	for _, gpu := range s.gpus {
		// Check accelerator compatibility
		if !acceleratorCompatible(c.Compute.Accelerator, gpu) {
			plan.Rejected = append(plan.Rejected, Rejection{
				GPU: gpu.ID, Reason: fmt.Sprintf("accelerator mismatch: contract wants %s, GPU is %s", c.Compute.Accelerator, gpu.Vendor),
			})
			continue
		}

		// Check VRAM fit
		if c.Compute.VRAMMB > 0 && gpu.VRAMFreeMB < c.Compute.VRAMMB {
			plan.Rejected = append(plan.Rejected, Rejection{
				GPU: gpu.ID, Reason: fmt.Sprintf("insufficient VRAM: contract wants %d MB, GPU has %d MB free", c.Compute.VRAMMB, gpu.VRAMFreeMB),
			})
			continue
		}

		// Check health
		gpuHealth, healthy := s.health[gpu.ID]
		if healthy && gpuHealth.State != "healthy" && gpuHealth.State != "unknown" {
			plan.Rejected = append(plan.Rejected, Rejection{
				GPU: gpu.ID, Reason: fmt.Sprintf("GPU unhealthy: %s", gpuHealth.State),
			})
			continue
		}

		// Find best runtime for this GPU
		runtime, runtimeScore, runtimeBreakdown := s.scoreRuntime(gpu, c)
		if runtime == "" {
			plan.Rejected = append(plan.Rejected, Rejection{
				GPU: gpu.ID, Reason: "no compatible runtime found",
			})
			continue
		}

		// Find best model for this runtime
		model, modelScore, modelBreakdown := s.scoreModel(runtime, c)

		// Calculate total score
		breakdown := []ScoreBreakdown{}

		// VRAM fit: +30 if fits, -30 if not (already filtered, so +30)
		vramScore := 30
		breakdown = append(breakdown, ScoreBreakdown{
			Factor: "vram_fit", Score: vramScore,
			Detail: fmt.Sprintf("GPU has %d MB free, contract needs %d MB", gpu.VRAMFreeMB, c.Compute.VRAMMB),
		})

		// Utilization: +20 if low (<50%), +10 if medium (<80%), -10 if high
		utilScore := -10
		if gpu.UtilizationPct < 50 {
			utilScore = 20
		} else if gpu.UtilizationPct < 80 {
			utilScore = 10
		}
		breakdown = append(breakdown, ScoreBreakdown{
			Factor: "utilization", Score: utilScore,
			Detail: fmt.Sprintf("GPU utilization: %d%%", gpu.UtilizationPct),
		})

		// Runtime fit
		breakdown = append(breakdown, runtimeBreakdown)

		// Model fit
		breakdown = append(breakdown, modelBreakdown)

		// Latency: +20 if healthy runtime, +10 if unknown, -20 if down
		latencyScore := 10
		rtHealth, rtHasHealth := s.health[runtime]
		if rtHasHealth {
			switch rtHealth.State {
			case "healthy":
				latencyScore = 20
			case "down", "unhealthy":
				latencyScore = -20
			}
		}
		breakdown = append(breakdown, ScoreBreakdown{
			Factor: "latency", Score: latencyScore,
			Detail: fmt.Sprintf("runtime %s health: %s", runtime, healthStateString(s.health, runtime)),
		})

		// Health: +10 if GPU healthy
		healthScore := 10
		breakdown = append(breakdown, ScoreBreakdown{
			Factor: "health", Score: healthScore,
			Detail: fmt.Sprintf("GPU %s healthy: %v", gpu.ID, gpu.Healthy),
		})

		// Temperature penalty: -5 if >80°C, -10 if >90°C
		tempPenalty := 0
		if gpu.TemperatureC > 90 {
			tempPenalty = -10
		} else if gpu.TemperatureC > 80 {
			tempPenalty = -5
		}
		if tempPenalty != 0 {
			breakdown = append(breakdown, ScoreBreakdown{
				Factor: "temperature_penalty", Score: tempPenalty,
				Detail: fmt.Sprintf("GPU temp: %d°C", gpu.TemperatureC),
			})
		}

		totalScore := vramScore + utilScore + runtimeScore + modelScore + latencyScore + healthScore + tempPenalty

		candidates = append(candidates, scored{
			gpu:       gpu,
			runtime:   runtime,
			model:     model,
			score:     totalScore,
			breakdown: breakdown,
		})
	}

	if len(candidates) == 0 {
		return plan
	}

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })

	best := candidates[0]
	plan.SelectedGPU = &best.gpu
	plan.SelectedRuntime = best.runtime
	plan.SelectedModel = best.model
	plan.Score = best.score
	plan.Reason = best.breakdown

	return plan
}

// acceleratorCompatible checks if a GPU matches the contract's accelerator type.
func acceleratorCompatible(want string, gpu inventory.GPU) bool {
	if want == "" || want == "cpu" {
		return true // CPU is always "compatible" (fallback)
	}
	if want == "cuda" {
		return strings.EqualFold(gpu.Vendor, "nvidia")
	}
	if want == "rocm" {
		return strings.EqualFold(gpu.Vendor, "amd")
	}
	return false
}

// scoreRuntime finds the best runtime for a GPU + contract.
// Returns (runtimeID, score, breakdown).
func (s *Scheduler) scoreRuntime(gpu inventory.GPU, c *contract.ResourceContract) (string, int, ScoreBreakdown) {
	// Check contract's preferred runtimes first
	preferred := make(map[string]bool)
	for _, p := range c.Runtime.Preferred {
		preferred[p] = true
	}

	// Find runtimes that are assigned to this GPU
	for _, rt := range s.runtimes {
		// Check if this runtime is assigned to this GPU
		gpuMatch := false
		for _, gid := range rt.GPUIDs {
			if gid == gpu.ID || gid == fmt.Sprintf("gpu.nvidia.%d", gpu.Index) {
				gpuMatch = true
				break
			}
		}
		if !gpuMatch && len(rt.GPUIDs) > 0 {
			continue // runtime is on a different GPU
		}

		// If contract has preferences, check them
		if len(preferred) > 0 && !preferred[rt.ID] {
			continue
		}

		score := 20 // runtime_fit base score
		detail := fmt.Sprintf("runtime %s compatible", rt.ID)

		// Bonus if runtime is running
		if rt.Running {
			score += 5
			detail += " (running)"
		}

		return rt.ID, score, ScoreBreakdown{
			Factor: "runtime_fit", Score: score, Detail: detail,
		}
	}

	// No runtime found — return empty
	return "", 0, ScoreBreakdown{}
}

// scoreModel finds the best model for a runtime + contract.
// Returns (modelID, score, breakdown).
func (s *Scheduler) scoreModel(runtimeID string, c *contract.ResourceContract) (string, int, ScoreBreakdown) {
	// Map runtime to model section in models.yaml
	modelMap := map[string]string{
		"sglang":  "sglang",
		"vllm":    "vllm",
		"beellama": "beellama",
		"ollama":  "ollama",
	}
	modelID, ok := modelMap[runtimeID]
	if !ok {
		return "", 0, ScoreBreakdown{}
	}

	for _, m := range s.models {
		if m.ID != modelID {
			continue
		}

		score := 20 // model_fit base score
		detail := fmt.Sprintf("model %s available", m.ID)

		// Check capability match
		if len(c.Model.Capabilities) > 0 {
			matched := 0
			for _, want := range c.Model.Capabilities {
				for _, have := range m.Capabilities {
					if want == have {
						matched++
						break
					}
				}
			}
			if matched == 0 {
				// Model doesn't have requested capabilities — still OK but lower score
				score = 5
				detail = fmt.Sprintf("model %s available but missing capabilities %v", m.ID, c.Model.Capabilities)
			} else {
				score += matched * 5
				detail = fmt.Sprintf("model %s matches %d/%d capabilities", m.ID, matched, len(c.Model.Capabilities))
			}
		}

		// Bonus if model is present on disk
		if m.Present {
			score += 5
			detail += " (present on disk)"
		}

		return m.ID, score, ScoreBreakdown{
			Factor: "model_fit", Score: score, Detail: detail,
		}
	}

	return "", 0, ScoreBreakdown{}
}

// healthStateString returns the health state for a component, or "unknown".
func healthStateString(health map[string]inventory.Health, component string) string {
	if h, ok := health[component]; ok {
		return h.State
	}
	return "unknown"
}

// FormatPlan returns a human-readable plan (per master guide section 17 format).
func (p *Plan) FormatPlan() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Task: %s\n\n", p.Task))

	if p.SelectedGPU == nil {
		sb.WriteString("No compatible GPU found.\n")
		if len(p.Rejected) > 0 {
			sb.WriteString("\nRejected:\n")
			for _, r := range p.Rejected {
				sb.WriteString(fmt.Sprintf("  %s: %s\n", r.GPU, r.Reason))
			}
		}
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("Selected GPU: %s\n", p.SelectedGPU.ID))
	sb.WriteString(fmt.Sprintf("Runtime: %s\n", p.SelectedRuntime))
	if p.SelectedModel != "" {
		sb.WriteString(fmt.Sprintf("Model: %s\n", p.SelectedModel))
	}
	sb.WriteString(fmt.Sprintf("Score: %d\n\n", p.Score))
	sb.WriteString("Reason:\n")
	for _, r := range p.Reason {
		sb.WriteString(fmt.Sprintf("  %-20s %+d  %s\n", r.Factor, r.Score, r.Detail))
	}

	if len(p.Rejected) > 0 {
		sb.WriteString("\nRejected:\n")
		for _, r := range p.Rejected {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", r.GPU, r.Reason))
		}
	}

	return sb.String()
}
