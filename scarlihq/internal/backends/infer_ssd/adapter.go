// Package infer_ssd — adapter.go: the BackendAdapter boundary (roadmap
// §8.3, the interface adapted to the repository's package APIs per the
// roadmap §0's adaptation rule — see the package charter's documented
// deviation: EstimateRequest/PlanBinding are adapter-local projections
// of the planner's §7.4 contracts, because this package MUST NOT import
// the planner; the planner imports THIS package).
package infer_ssd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/admission"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
)

// EstimateRequest is the §7.4 request projection the estimate needs
// (ContextTokens drives the KV derivation; MaxConcurrent informs the
// §8.8 capping). Deliberately NOT the planner's ExecutionRequest: the
// projection keeps the packages acyclic and carries exactly the
// §8-relevant facts.
type EstimateRequest struct {
	ContextTokens int
	MaxConcurrent int
}

// PlanBinding is the §7.4 plan projection BuildProfile needs (the plan
// identity, the model binding, the placement, the ask's provenance).
// The Host Bridge path constructs it from a planner-produced
// ExecutionPlan; the adapter never sees (and cannot be steered by)
// anything outside the projection.
type PlanBinding struct {
	PlanID          string
	BackendID       string
	Strategy        string
	ModelArtifactID string
	ModelID         string
	ModelRevision   string
	ContextTokens   int
	MaxConcurrent   int
	GPUUUIDs        []string
	Principal       string
}

// ApprovedRuntimeProfile is what BuildProfile returns: the VALIDATED
// launch shape for the workload/Host Bridge path (roadmap §8.3: "The
// adapter must not call Docker directly. It returns a validated profile
// for the workload/Host Bridge path"). HealthEndpointURL is empty
// until the Host Bridge binds a launched runtime's endpoint — the
// adapter never assigns endpoints.
type ApprovedRuntimeProfile struct {
	ProfileID         string                   `json:"profile_id"`
	Strategy          string                   `json:"strategy"`
	ImageReference    string                   `json:"image_reference"`
	NetworkProfile    string                   `json:"network_profile"`
	ModelPath         string                   `json:"model_path"`
	ExpertPath        string                   `json:"expert_path"`
	StorageDeviceID   string                   `json:"storage_device_id"`
	HealthPath        string                   `json:"health_path"`
	HealthType        string                   `json:"health_type"`
	StartupTimeoutSec int                      `json:"startup_timeout_seconds"`
	HealthEndpointURL string                   `json:"health_endpoint_url,omitempty"` // bound by the Host Bridge path
	Plan              PlanBinding              `json:"plan"`
	ResourceVector    admission.ResourceVector `json:"resource_vector"`
}

// BackendAdapter is the §8.3 adapter boundary (the roadmap's interface,
// names adapted to the repository's packages). One implementation
// exists in v19.8.0: InferSSDAdapter. The interface is the CONTRACT the
// Host Bridge path will consume; it is deliberately minimal and pure
// except Probe, which is seam-bounded.
type BackendAdapter interface {
	// ID — the capability-tier identity of this backend.
	ID() airuntime.BackendID
	// Capabilities — the closed registry row (a copy; the registry is
	// administrator-owned and this method never authors rows).
	Capabilities() airuntime.BackendCapabilities
	// ValidateModel — the §8.2 model compatibility declaration verdict.
	ValidateModel(model modelmeta.ModelExecution) error
	// Estimate — the §8.3 resource estimation (the admission-shaped
	// resource vector for one model × one request).
	Estimate(model modelmeta.ModelExecution, req EstimateRequest) (admission.ResourceVector, error)
	// BuildProfile — the validated launch shape for the Host Bridge
	// path. NEVER launches anything.
	BuildProfile(plan PlanBinding) (ApprovedRuntimeProfile, error)
	// Probe — the health probe (seam-bounded; see HealthProber).
	Probe(ctx context.Context, target ApprovedRuntimeProfile) HealthResult
}

// InferSSDAdapter is the v19.8.0 InferSSD BackendAdapter: a VALIDATED,
// ENABLED runtime profile + an optional HealthProber seam. Construction
// is fail-closed (a disabled or invalid profile is a phantom — the
// named error names it); the adapter's methods are pure except Probe.
type InferSSDAdapter struct {
	profile *RuntimeProfile
	prober  HealthProber
}

// NewAdapter constructs the adapter. Fail-closed contract:
//   - profile must be non-nil, VALID (ValidateRuntimeProfile passes),
//   - profile must be ENABLED (an adapter over a disabled profile is a
//     phantom capability — §8.8's initial status ships disabled, and
//     "enabled" is the operator's deliberate act),
//   - prober may be nil (Probe then refuses BY NAME — the seam
//     discipline; production wires HTTPHealthProber, tests wire fakes).
func NewAdapter(profile *RuntimeProfile, prober HealthProber) (*InferSSDAdapter, error) {
	if profile == nil {
		return nil, fmt.Errorf("infer_ssd: adapter construction refused — no runtime profile (a nil profile is not a disabled backend, it is no backend)")
	}
	if reasons := ValidateRuntimeProfile(profile); len(reasons) > 0 {
		return nil, fmt.Errorf("infer_ssd: adapter construction refused — the runtime profile is invalid: %s", strings.Join(reasons, "; "))
	}
	if !profile.Enabled {
		return nil, fmt.Errorf("infer_ssd: adapter construction refused — the runtime profile %q is DISABLED (roadmap §8.8's initial status: enabled=false is documentation; enabling it is the operator's deliberate act AFTER the compatibility matrix + target-host acceptance pass)", profile.ProfileID)
	}
	return &InferSSDAdapter{profile: profile, prober: prober}, nil
}

// ID implements BackendAdapter.
func (a *InferSSDAdapter) ID() airuntime.BackendID { return InferSSDBackendID }

// Capabilities implements BackendAdapter (the registry row copy — the
// adapter never authors capability rows).
func (a *InferSSDAdapter) Capabilities() airuntime.BackendCapabilities {
	row, err := AdapterCapabilities()
	if err != nil {
		// Unreachable for InferSSDBackendID (the closed registry carries
		// the row; ValidateBackendRegistry asserts the 1:1 shape) — the
		// zero row is never silently served, the error is named here.
		return airuntime.BackendCapabilities{BackendID: InferSSDBackendID, AdapterNote: "registry row unavailable: " + err.Error()}
	}
	return row
}

// ValidateModel implements BackendAdapter — the §8.2 verdict as an
// error (nil = compatible; a named ModelBindingError otherwise).
func (a *InferSSDAdapter) ValidateModel(model modelmeta.ModelExecution) error {
	verdict := BindModel(a.profile, model)
	if verdict.Qualified {
		return nil
	}
	return fmt.Errorf("infer_ssd: model %q refused: %s (%s)", model.ID, verdict.Code, verdict.Detail)
}

// Estimate implements BackendAdapter — the §8.3 resource estimation:
// the ssd-backed-moe vector (VRAM = shared + the profile's reserve, the
// KV cache declared SEPARATELY in KVCacheBytes — v19.8.1's
// single-count contract, KPK-19.8.0-P2-03: the admission gate adds the KV
// bytes to the assigned GPU's demand exactly ONCE, so a producer that
// bakes KV into VRAMBytes made the gate count it twice and refuse
// workloads that fit; the experts live on the SSD; host RAM + pinned +
// CPU threads from the profile's declared additive demands; the storage
// demand = the paged experts' bytes + the minimum free floor, with the
// profile's measured floors carried for the admission gate).
func (a *InferSSDAdapter) Estimate(model modelmeta.ModelExecution, req EstimateRequest) (admission.ResourceVector, error) {
	if err := a.ValidateModel(model); err != nil {
		return admission.ResourceVector{}, err
	}
	if req.ContextTokens <= 0 {
		return admission.ResourceVector{}, fmt.Errorf("infer_ssd: estimate refused — context_tokens must be > 0 (the KV derivation needs it)")
	}
	if req.ContextTokens > a.profile.Execution.ContextLimit {
		return admission.ResourceVector{}, fmt.Errorf("infer_ssd: estimate refused — context_tokens %d exceeds the profile's serving ceiling %d", req.ContextTokens, a.profile.Execution.ContextLimit)
	}
	kvBytes := uint64(req.ContextTokens) * model.KVCacheBytesPerToken
	// KPK-19.8.0-P2-03: VRAMBytes carries shared + the profile reserve ONLY
	// — the KV cache rides ResourceVector.KVCacheBytes and the admission
	// gate adds it to the assigned GPU exactly once.
	vramDemand := model.SharedBytes + a.profile.Resources.GPU.VRAMReserveBytes
	experts := ExpertStorageBytes(model)
	storage := admission.StorageRequest{
		DeviceID:              a.profile.Resources.Storage.DeviceID,
		MinimumFreeBytes:      experts + a.profile.Resources.Storage.MinimumFreeBytes,
		MinimumReadMBps:       a.profile.Resources.Storage.MinimumReadMBps,
		MinimumRandomReadIOPS: a.profile.Resources.Storage.MinimumRandomReadIOPS,
		MaximumP95LatencyMs:   a.profile.Resources.Storage.MaximumP95LatencyMs,
		PathClass:             "model",
	}
	return admission.ResourceVector{
		GPU: []admission.GPURequest{{
			// The GPUUUID is the PLACEMENT's answer (the planner picked
			// it; the adapter's estimate is placement-independent — an
			// empty UUID means "the deterministic pick happens above").
			VRAMBytes: vramDemand,
			Exclusive: true, // §8.8: one allocation per GPU
		}},
		RAMBytes:       a.profile.Resources.HostRAMBytes,
		PinnedRAMBytes: a.profile.Resources.PinnedRAMBytes,
		CPUThreads:     float64(a.profile.Resources.CPUThreads),
		KVCacheBytes:   kvBytes,
		Storage:        []admission.StorageRequest{storage},
	}, nil
}

// BuildProfile implements BackendAdapter — the validated launch shape.
// NEVER launches anything (no Docker, no exec, no network): the
// returned profile is the workload/Host Bridge path's input. The plan
// binding is validated against the adapter's own profile (the §8.2
// join again — the Host Bridge never trusts a plan whose model binding
// disagrees with the profile it launches from).
func (a *InferSSDAdapter) BuildProfile(plan PlanBinding) (ApprovedRuntimeProfile, error) {
	if strings.TrimSpace(plan.PlanID) == "" {
		return ApprovedRuntimeProfile{}, fmt.Errorf("infer_ssd: BuildProfile refused — the plan binding carries no plan id (a phantom plan)")
	}
	if plan.BackendID != string(InferSSDBackendID) {
		return ApprovedRuntimeProfile{}, fmt.Errorf("infer_ssd: BuildProfile refused — the plan binding names backend %q, not %q (a plan for another backend never launches through this adapter)", plan.BackendID, InferSSDBackendID)
	}
	if plan.Strategy != StrategySSDBackedMoE {
		return ApprovedRuntimeProfile{}, fmt.Errorf("infer_ssd: BuildProfile refused — the plan binding carries strategy %q, not %q (the one strategy this engine executes)", plan.Strategy, StrategySSDBackedMoE)
	}
	if plan.ModelID != a.profile.Compatibility.ModelManifest {
		return ApprovedRuntimeProfile{}, fmt.Errorf("infer_ssd: BuildProfile refused — the plan binding names model %q; the profile binds %q (the §8.2 exact-combination rule)", plan.ModelID, a.profile.Compatibility.ModelManifest)
	}
	if plan.ContextTokens > a.profile.Execution.ContextLimit {
		return ApprovedRuntimeProfile{}, fmt.Errorf("infer_ssd: BuildProfile refused — the plan binding's context %d exceeds the profile's serving ceiling %d", plan.ContextTokens, a.profile.Execution.ContextLimit)
	}
	if plan.MaxConcurrent > a.profile.Execution.MaxConcurrentRequests {
		return ApprovedRuntimeProfile{}, fmt.Errorf("infer_ssd: BuildProfile refused — the plan binding's max_concurrent %d exceeds the profile's cap %d (§8.8's one-active-workload rule)", plan.MaxConcurrent, a.profile.Execution.MaxConcurrentRequests)
	}
	return ApprovedRuntimeProfile{
		ProfileID:         a.profile.ProfileID,
		Strategy:          StrategySSDBackedMoE,
		ImageReference:    a.profile.Image.Reference,
		NetworkProfile:    a.profile.Network.NetworkProfile,
		ModelPath:         a.profile.Resources.Storage.ModelPath,
		ExpertPath:        a.profile.Resources.Storage.ExpertPath,
		StorageDeviceID:   a.profile.Resources.Storage.DeviceID,
		HealthPath:        a.profile.Health.Path,
		HealthType:        a.profile.Health.Type,
		StartupTimeoutSec: a.profile.Health.StartupTimeoutSeconds,
		Plan:              plan,
	}, nil
}

// Probe implements BackendAdapter — the seam-bounded health probe. A
// nil prober is a NAMED refusal (the seam discipline: production wires
// HTTPHealthProber; the adapter never dials on its own).
func (a *InferSSDAdapter) Probe(ctx context.Context, target ApprovedRuntimeProfile) HealthResult {
	if a.prober == nil {
		return HealthResult{Healthy: false, Detail: "no health prober configured — the adapter never dials on its own (wire the HTTPHealthProber or inject a test double)", CheckedAt: timeNowUTC()}
	}
	return a.prober.CheckHealth(ctx, target)
}

// timeNowUTC is the clock seam (tests override; production reads the
// wall clock once per verdict).
var timeNowUTC = func() time.Time { return time.Now().UTC() }
