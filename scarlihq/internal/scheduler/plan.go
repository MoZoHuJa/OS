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
	Task            string           `json:"task"`             // e.g. "coding"
	Priority        string           `json:"priority"`         // e.g. "interactive"
	SelectedGPU     *inventory.GPU   `json:"selected_gpu"`     // nil if none compatible
	SelectedRuntime string           `json:"selected_runtime"` // e.g. "sglang"
	SelectedModel   string           `json:"selected_model"`   // e.g. "qwen3-14b-awq"
	Score           int              `json:"score"`            // total score
	Reason          []ScoreBreakdown `json:"reason"`           // scoring breakdown
	Rejected        []Rejection      `json:"rejected"`         // why other options were rejected
}

// ScoreBreakdown shows how each scoring factor contributed.
type ScoreBreakdown struct {
	Factor string `json:"factor"` // "vram_fit", "runtime_fit", etc.
	Score  int    `json:"score"`  // points awarded (can be negative)
	Detail string `json:"detail"` // human-readable explanation
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
		Reason:   []ScoreBreakdown{},
		Rejected: []Rejection{},
	}

	// v19.1.12 P1-B3: Don't mutate caller's contract. Use local copy.
	accelerator := c.Compute.Accelerator
	if accelerator == "" {
		accelerator = "cuda" // v19.1.11: sensible default for AI workloads
	}

	// v19.1.10 P1-1 FIX: CPU requests must NOT enter the GPU loop.
	if accelerator == "cpu" {
		runtime, runtimeScore, runtimeBreakdown := s.scoreRuntimeForCPU(c)
		if runtime == "" {
			plan.Rejected = append(plan.Rejected, Rejection{
				GPU: "none", Reason: "no CPU-compatible runtime found (beellama/ollama not available)",
			})
			return plan
		}
		model, modelScore, modelBreakdown := s.scoreModel(runtime, c)
		if model == "" {
			plan.Rejected = append(plan.Rejected, Rejection{
				GPU: "none", Reason: "no compatible model for CPU runtime",
			})
			return plan
		}
		breakdown := []ScoreBreakdown{
			{Factor: "accelerator", Score: 10, Detail: "CPU request — no GPU needed"},
			runtimeBreakdown,
			modelBreakdown,
		}
		totalScore := 10 + runtimeScore + modelScore
		plan.SelectedRuntime = runtime
		plan.SelectedModel = model
		plan.Score = totalScore
		plan.Reason = breakdown
		return plan
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
		// Check accelerator compatibility (v19.1.12: use local accelerator, not mutated contract)
		if !acceleratorCompatible(accelerator, gpu) {
			plan.Rejected = append(plan.Rejected, Rejection{
				GPU: gpu.ID, Reason: fmt.Sprintf("accelerator mismatch: contract wants %s, GPU is %s", accelerator, gpu.Vendor),
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
		// v19.1.16 P1-3: Fail-closed for missing GPU health (was: fail-open).
		// v19.1.17 P1 (A-02): Was: allowed "unknown" state. Now: only "healthy" passes.
		//   "unknown" is NOT proof that GPU is healthy — it means we haven't checked.
		//   For dry-run scheduler this is acceptable, but for real allocation in v19.2
		//   it would be dangerous to allocate on an unchecked GPU.
		gpuHealth, hasHealth := s.health[gpu.ID]
		if !hasHealth || gpuHealth.State != "healthy" {
			plan.Rejected = append(plan.Rejected, Rejection{
				GPU: gpu.ID, Reason: fmt.Sprintf("GPU health missing or unhealthy: %s", func() string {
					if hasHealth {
						return gpuHealth.State
					}
					return "no health data"
				}()),
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

		// v19.1.10 P1-2 FIX: If no compatible model found, REJECT this candidate.
		if model == "" {
			plan.Rejected = append(plan.Rejected, Rejection{
				GPU: gpu.ID, Reason: "no compatible model found (missing capabilities or no model for runtime)",
			})
			continue
		}

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
// v19.1.10 P1-1 FIX: "cpu" requests must NOT match any GPU. CPU tasks
// should only use CPU runtimes (beellama, ollama), never GPUs.
// The Plan() method handles cpu accelerator by skipping the GPU loop entirely.
func acceleratorCompatible(want string, gpu inventory.GPU) bool {
	if want == "" || want == "cpu" {
		return false // v19.1.10: CPU requests must NOT match GPUs
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
	// v19.1.11 P1-3: Best-of selection (was: first-match).
	preferred := make(map[string]bool)
	for _, p := range c.Runtime.Preferred {
		preferred[p] = true
	}

	bestID := ""
	bestScore := -1
	var bestBreakdown ScoreBreakdown

	for _, rt := range s.runtimes {
		// v19.1.12 P1-B1: CPU-only runtimes (empty GPUIDs) must NOT
		// be selected for GPU tasks. Was: only skipped if GPUIDs didn't
		// match AND were non-empty → CPU runtimes leaked into GPU path.
		if len(rt.GPUIDs) == 0 {
			continue // CPU-only runtime — skip in GPU scoring
		}

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

		// v19.1.10 P1-3 FIX: Hard-filter unhealthy/down runtimes BEFORE scoring.
		// "down" and "unhealthy" are HARD rejections — not lower-score candidates.
		// "unknown" is allowed (may be a runtime we haven't probed yet).
		if rtHealth, hasHealth := s.health[rt.ID]; hasHealth {
			if rtHealth.State == "down" || rtHealth.State == "unhealthy" {
				continue // HARD reject: skip this runtime, try next
			}
		}

		// If contract has preferences, check them
		// v19.1.18 P2 (Zmor-7): KNOWN CONSTRAINT — if the preferred runtime is
		// down/unhealthy, it is hard-rejected above AND non-preferred runtimes
		// are filtered here. Result: no runtime is selected (fail-closed).
		// This is INTENTIONAL for v19.1.x (dry-run scheduler). v19.2.0 Compute
		// Fabric will add a fallback pass: if bestID == "" after the preferred
		// loop, retry without the preferred filter (preferred = soft hint).
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

		if score > bestScore {
			bestScore = score
			bestID = rt.ID
			bestBreakdown = ScoreBreakdown{
				Factor: "runtime_fit", Score: score, Detail: detail,
			}
		}
	}

	if bestID == "" {
		return "", 0, ScoreBreakdown{}
	}
	return bestID, bestScore, bestBreakdown
}

// scoreRuntimeForCPU finds the best CPU-only runtime for a CPU request.
// v19.1.10 P1-1: CPU requests must only match CPU runtimes (beellama, ollama),
// never GPU runtimes (sglang, vllm). This method is called when accelerator="cpu".
// Returns (runtimeID, score, breakdown).
func (s *Scheduler) scoreRuntimeForCPU(c *contract.ResourceContract) (string, int, ScoreBreakdown) {
	preferred := make(map[string]bool)
	for _, p := range c.Runtime.Preferred {
		preferred[p] = true
	}

	bestID := ""
	bestScore := -1
	var bestBreakdown ScoreBreakdown

	// CPU runtimes: those with no GPU assignment (empty GPUIDs)
	for _, rt := range s.runtimes {
		// Skip GPU-assigned runtimes (sglang, vllm have GPUIDs)
		if len(rt.GPUIDs) > 0 {
			continue
		}

		// v19.1.10 P1-3: Same health hard-filter as scoreRuntime
		if rtHealth, hasHealth := s.health[rt.ID]; hasHealth {
			if rtHealth.State == "down" || rtHealth.State == "unhealthy" {
				continue
			}
		}

		// If contract has preferences, check them
		// v19.1.18 P2 (Zmor-7): KNOWN CONSTRAINT — if the preferred runtime is
		// down/unhealthy, it is hard-rejected above AND non-preferred runtimes
		// are filtered here. Result: no runtime is selected (fail-closed).
		// This is INTENTIONAL for v19.1.x (dry-run scheduler). v19.2.0 Compute
		// Fabric will add a fallback pass: if bestID == "" after the preferred
		// loop, retry without the preferred filter (preferred = soft hint).
		if len(preferred) > 0 && !preferred[rt.ID] {
			continue
		}

		score := 20
		detail := fmt.Sprintf("CPU runtime %s compatible", rt.ID)
		if rt.Running {
			score += 5
			detail += " (running)"
		}

		if score > bestScore {
			bestScore = score
			bestID = rt.ID
			bestBreakdown = ScoreBreakdown{
				Factor: "runtime_fit", Score: score, Detail: detail,
			}
		}
	}

	if bestID == "" {
		return "", 0, ScoreBreakdown{}
	}
	return bestID, bestScore, bestBreakdown
}

// scoreModel finds the best model for a runtime + contract.
// Returns (modelID, score, breakdown).
func (s *Scheduler) scoreModel(runtimeID string, c *contract.ResourceContract) (string, int, ScoreBreakdown) {
	// v19.1.11 P1-4: Use SupportedRuntimes (was: hardcoded modelMap).
	bestID := ""
	bestScore := -1
	var bestBreakdown ScoreBreakdown

	for _, m := range s.models {
		// v19.1.11 P1-4: Check if this model supports the requested runtime's engine.
		// v19.1.12 P1-B2: Empty SupportedRuntimes = "supports all" (was: always rejected).
		if len(m.SupportedRuntimes) > 0 && !containsString(m.SupportedRuntimes, runtimeEngine(runtimeID)) {
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
			// v19.1.11 P1-1: ALL required capabilities must match.
			// v19.1.12 P1-2 (Review2): Was `return` (aborts entire loop on first
			// incompatible model). Now: `continue` (try next model).
			if matched < len(c.Model.Capabilities) {
				continue // This model doesn't match — try next
			}
			score += matched * 5
			detail = fmt.Sprintf("model %s matches %d/%d capabilities", m.ID, matched, len(c.Model.Capabilities))
		}

		// Bonus if model is present on disk
		if m.Present {
			score += 5
			detail += " (present on disk)"
		}

		// v19.1.11 P1-4: Track best model (was: first-match return)
		if score > bestScore {
			bestScore = score
			bestID = m.ID
			bestBreakdown = ScoreBreakdown{
				Factor: "model_fit", Score: score, Detail: detail,
			}
		}
	}

	if bestID == "" {
		return "", 0, ScoreBreakdown{}
	}
	return bestID, bestScore, bestBreakdown
}

// containsString checks if a string is in a slice.
func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// runtimeEngine maps a runtime ID to its engine name for SupportedRuntimes matching.
// beellama runtime uses llama.cpp engine, so its model's SupportedRuntimes contains
// "llamacpp" not "beellama". Other runtimes (sglang, vllm, ollama) have ID == engine.
func runtimeEngine(runtimeID string) string {
	switch runtimeID {
	case "beellama":
		return "llamacpp"
	default:
		return runtimeID
	}
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

	// v19.1.11 P1-2: Distinguish valid CPU plan from failure.
	if p.SelectedGPU == nil && p.SelectedRuntime != "" {
		sb.WriteString(fmt.Sprintf("Selected Runtime: %s (CPU)\n", p.SelectedRuntime))
		if p.SelectedModel != "" {
			sb.WriteString(fmt.Sprintf("Model: %s\n", p.SelectedModel))
		}
		sb.WriteString(fmt.Sprintf("Score: %d\n\n", p.Score))
		sb.WriteString("Reason:\n")
		for _, r := range p.Reason {
			sb.WriteString(fmt.Sprintf("  %-20s %+d  %s\n", r.Factor, r.Score, r.Detail))
		}
		return sb.String()
	}
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
