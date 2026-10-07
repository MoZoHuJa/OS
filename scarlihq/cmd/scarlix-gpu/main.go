// Command scarlix-gpu prints normalized GPU telemetry as JSON.
//
// This is the Go-backed producer of the v19.0.7 GPU data contract. The bash
// `scarlix` CLI delegates `--json gpu status` (and `--json gpu list`) to
// this binary when present, so the JSON output matches the frozen schema in
// docs/SCARLIX_DATA_CONTRACTS.md exactly (no hand-rolled JSON string
// concatenation in bash). Human-readable GPU output (`scarlix gpu status`
// without `--json`) still uses the bash CLI's direct nvidia-smi formatting.
//
// Usage:
//
//	scarlix-gpu            # print []GPU as JSON (one entry per GPU)
//	scarlix-gpu --health   # print {"gpus":[...],"health":[...]}
//
// Exit codes:
//
//	0 — success (including the "no nvidia-smi → print []" case)
//	1 — JSON encode failure (should never happen with well-formed types)
//
// Build (from scarlihq module root):
//
//	CGO_ENABLED=0 go build -o /usr/local/bin/scarlix-gpu ./cmd/scarlix-gpu
//
// Deployment (v19.0.8 task 3-a): install.sh Phase 4 copies this binary to
// /usr/local/bin/scarlix-gpu (root:root, mode 755). It is invoked by the
// `scarlix` bash CLI's `--json gpu status` / `--json gpu list` paths; falls
// back to the bash-native JSON if absent.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

func main() {
	healthFlag := flag.Bool("health", false, "also print GPU health status")
	flag.Parse()

	gpus := inventory.CollectGPUs()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	var err error
	if *healthFlag {
		// CollectGPUHealth mutates gpus in place (mirrors state → Healthy
		// field on each GPU), so call it AFTER CollectGPUs but BEFORE we
		// encode the gpus slice. This way the "healthy" field in each GPU
		// entry and the "state" field in each Health entry stay in sync.
		healths := inventory.CollectGPUHealth(gpus)
		output := map[string]interface{}{
			"gpus":   gpus,
			"health": healths,
		}
		err = enc.Encode(output)
	} else {
		err = enc.Encode(gpus)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "scarlix-gpu: error: %v\n", err)
		os.Exit(1)
	}
}
