// Package infer_ssd — the InferSSD backend adapter (v19.8.0, Compute
// Fabric Phase 4, roadmap §8 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md).
//
// CHARTER (the package's non-negotiable contract):
//
//   - InferSSD is a DISTINCT inference backend (roadmap §8.1: "Treat
//     InferSSD as a distinct inference backend/engine integration for
//     SSD-backed MoE expert execution"), NOT a generic storage mode and
//     NOT a fifth chat transport: the capability tier's registry row
//     (airuntime.BackendInferSSD) carries its identity, and this package
//     carries its adapter boundary — the model compatibility
//     declaration, the runtime profile, the health probe contract, the
//     storage requirements, and the resource estimation.
//   - THE ADAPTER NEVER CALLS DOCKER (roadmap §8.3: "The adapter must
//     not call Docker directly. It returns a validated profile for the
//     workload/Host Bridge path"): no Docker API client, no container
//     launch, no image pull exists anywhere in this package. The only
//     network egress is the health PROBE (health.go) behind its own
//     injectable seam — the same discipline the airuntime transport
//     uses for its probers.
//   - The runtime profile (config.go, scarlix.runtime-profile/v1) is
//     OPERATOR-OWNED and FAIL-CLOSED: placeholder values are REFUSED BY
//     NAME (roadmap §8.4: "Never enable a profile containing
//     placeholder values. The configuration loader must reject them"),
//     the image is digest-pinned, the enablement status follows §8.8's
//     initial status (enabled: false, experimental: true, one active
//     workload per profile, exclusive GPU), and a profile claiming
//     non-experimental production readiness without hardware acceptance
//     is refused.
//   - The qualification (requirements.go) consumes MEASURED
//     conservative capacity only (roadmap §8.6/§8.8: "SSD I/O
//     admission uses measured conservative capacity"): a
//     measured-performance floor without a measurement is a named
//     refusal — never schedule from unverified estimates.
//   - DEVIATION FROM §8.3'S SIGNATURES (documented, per the roadmap's
//     §0 "adapt names and interfaces to the actual package APIs"): the
//     interface's Estimate takes an adapter-local EstimateRequest and
//     BuildProfile an adapter-local PlanBinding — the §7.4
//     ExecutionRequest/ExecutionPlan contracts belong to the planner
//     package, and this package MUST NOT import the planner (the
//     planner imports THIS package for the profile type + the strategy
//     constant + the qualification checks; the acyclic split keeps both
//     reviewable). The projections carry exactly the §8-relevant facts.
package infer_ssd

import (
	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// StrategySSDBackedMoE — the ONE v19.8.0 execution strategy this backend
// executes (roadmap §8.4's execution.strategy, normalized to the
// planner's kebab-case strategy vocabulary; the profile schema requires
// exactly this value). MoE experts are paged from the approved SSD path
// at serving time; the shared/dense weights + the KV cache stay resident
// in GPU VRAM.
const StrategySSDBackedMoE = "ssd-backed-moe"

// InferSSDBackendID — the capability-tier identity (mirrors
// airuntime.BackendInferSSD; the constant exists so the package's
// validation never string-matches blindly).
const InferSSDBackendID = airuntime.BackendInferSSD

// The adapter's own model-compatibility claims (the §8.1 "model
// compatibility declaration"): the engine executes safetensors MoE
// checkpoints only — the profile's compatibility block binds ONE exact
// checkpoint, these constants are the FORMAT/ARCHITECTURE half of the
// join (the airuntime registry row carries the same claims at the
// capability tier; the two are kept honest against each other by the
// v19.8.0 smoke check).
const (
	// SupportedModelFormat — the checkpoint format the engine executes.
	SupportedModelFormat = "safetensors"
	// SupportedArchitecture — the parameter architecture the engine
	// executes (MoE only: SSD-backed execution IS expert paging; a dense
	// checkpoint has no experts to page and is refused BY NAME).
	SupportedArchitecture = "moe"
)

// AdapterCapabilities returns the capability-tier row for the backend
// (a copy from the closed airuntime registry — the adapter never
// authors its own capability row; the registry is administrator-owned,
// validated at construction, and this function is the honest bridge for
// the BackendAdapter interface's Capabilities()).
func AdapterCapabilities() (airuntime.BackendCapabilities, error) {
	return airuntime.BackendCapabilitiesFor(InferSSDBackendID)
}
