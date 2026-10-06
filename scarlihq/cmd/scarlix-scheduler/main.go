// Command scarlix-scheduler implements the `scarlix compute plan` dry-run
// scheduler (v19.1.7 per ScaRgeN master guide section 17).
//
// Usage:
//
//	scarlix-scheduler plan --task <type> [--priority <p>] [--vram <mb>] [--runtime <r>]
//	scarlix-scheduler plan --json   # output as JSON instead of human-readable
//
// The scheduler reads the current system inventory (GPUs, runtimes, models,
// health) and produces a dry-run plan. NO actual allocation is performed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/MoZoHuJa/OS/scarlihq/internal/contract"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scheduler"
)

func main() {
	task := flag.String("task", "coding", "task type: coding, chat, research, embedding, indexing")
	priority := flag.String("priority", "interactive", "priority: realtime, interactive, normal, background, batch")
	vram := flag.Int("vram", 0, "requested VRAM in MB (0 = any)")
	accelerator := flag.String("accelerator", "cuda", "accelerator: cuda, cpu, rocm")
	runtimePref := flag.String("runtime", "", "preferred runtime (comma-separated: sglang,vllm)")
	modelCaps := flag.String("capabilities", "", "model capabilities (comma-separated: coding,reasoning)")
	asJSON := flag.Bool("json", false, "output as JSON instead of human-readable")
	flag.Parse()

	if *task == "" {
		fmt.Fprintln(os.Stderr, "scarlix-scheduler: --task is required")
		os.Exit(2)
	}

	// Build contract from CLI args
	c := &contract.ResourceContract{
		Version: contract.VersionV1,
		ID:      "cli-dry-run",
		AgentID: "cli",
		Task:    contract.TaskSpec{Type: *task, Priority: *priority},
		Compute: contract.ComputeSpec{
			Accelerator: *accelerator,
			VRAMMB:      *vram,
		},
	}

	if *runtimePref != "" {
		c.Runtime.Preferred = strings.Split(*runtimePref, ",")
	}
	if *modelCaps != "" {
		c.Model.Capabilities = strings.Split(*modelCaps, ",")
	}
	// Default security scope
	c.Security.Filesystem = "workspace"
	c.Security.Network = "restricted"
	c.Security.Shell = "sandbox"

	// Collect inventory
	gpus := inventory.CollectGPUs()
	runtimes := inventory.CollectRuntimes()
	models := inventory.CollectModels()
	gpuHealth := inventory.CollectGPUHealth(gpus)

	// Create scheduler + plan
	s := scheduler.New(gpus, runtimes, models, gpuHealth)
	plan := s.Plan(c)

	// Output
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(plan); err != nil {
			fmt.Fprintf(os.Stderr, "scarlix-scheduler: encode error: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Print(plan.FormatPlan())
	}
}
