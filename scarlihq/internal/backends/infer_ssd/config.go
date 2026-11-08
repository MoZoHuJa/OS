// Package infer_ssd — config.go: the runtime profile contract
// (scarlix.runtime-profile/v1, roadmap §8.4, field-for-field) + the
// fail-closed loader.
package infer_ssd

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// RuntimeProfileSchemaVersion — the §8.4 schema identity.
const RuntimeProfileSchemaVersion = "scarlix.runtime-profile/v1"

// profileIDRe — the closed safe identifier charset for profile ids (the
// modelmeta model-id rules: profile ids are join keys carried into
// plans, not free text).
var profileIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// GPUModeExclusive — the ONLY v19.8.0 GPU mode (roadmap §8.8: "one
// allocation per GPU"; the §8.4 example's exclusive mode).
const GPUModeExclusive = "exclusive"

// HealthTypeHTTP — the ONLY v19.8.0 health probe type (the §8.4
// example's http /health contract).
const HealthTypeHTTP = "http"

// NetworkProfileInternal — the §8.4 example's network profile (the
// compute-internal network; no published ports in v19.8.0).
const NetworkProfileInternal = "scarlix-compute-internal"

// RuntimeProfile is the on-disk YAML shape of one InferSSD runtime
// profile (roadmap §8.4, field-for-field; the example's placeholder
// values are REFUSED by ValidateRuntimeProfile — an unknown
// compatibility fact never becomes a schedulable claim).
type RuntimeProfile struct {
	SchemaVersion string `yaml:"schema_version" json:"schema_version"`
	// ProfileID — the profile's identity (closed-charset slug).
	ProfileID string `yaml:"profile_id" json:"profile_id"`
	// BackendID — must be "infer_ssd" (a profile for any other backend
	// is a wiring error refused BY NAME).
	BackendID string `yaml:"backend_id" json:"backend_id"`
	// Enabled — §8.8's initial status: false. A disabled profile is
	// DOCUMENTATION: the loader accepts it, the planner's seam turns it
	// into "no profile configured" (every infer_ssd plan refuses
	// plan-ssd-profile-unconfigured), and nothing ever launches from it.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Experimental — MUST be true (§8.8: "experimental: true"). A
	// non-experimental profile is a PRODUCTION claim, and production
	// scheduling is disabled until the compatibility matrix + the
	// target-host acceptance pass (§8.2's own rule) — refused BY NAME.
	Experimental bool `yaml:"experimental" json:"experimental"`

	// Image — the digest-pinned engine container (§8.4's
	// "Runtime profiles are digest-pinned and fixed").
	Image struct {
		Reference string `yaml:"reference" json:"reference"`
	} `yaml:"image" json:"image"`

	// Compatibility — §8.2's evidence dimensions the profile binds:
	// the exact model manifest + checkpoint fingerprint, the CUDA
	// runtime/toolkit version, the minimum driver, and the GPU compute
	// capabilities the build was verified against.
	Compatibility struct {
		ModelManifest          string   `yaml:"model_manifest" json:"model_manifest"`
		CheckpointSHA256       string   `yaml:"checkpoint_sha256" json:"checkpoint_sha256"`
		CUDA                   string   `yaml:"cuda" json:"cuda"`
		DriverMinimum          string   `yaml:"driver_minimum" json:"driver_minimum"`
		GPUComputeCapabilities []string `yaml:"gpu_compute_capabilities" json:"gpu_compute_capabilities"`
	} `yaml:"compatibility" json:"compatibility"`

	// Execution — the serving contract (the strategy, the context
	// ceiling, the concurrency cap).
	Execution struct {
		Strategy              string `yaml:"strategy" json:"strategy"`
		ContextLimit          int    `yaml:"context_limit" json:"context_limit"`
		MaxConcurrentRequests int    `yaml:"max_concurrent_requests" json:"max_concurrent_requests"`
	} `yaml:"execution" json:"execution"`

	// Resources — the resource demands (the additive ones may be 0 —
	// the §8.4 example's honesty; the DERIVED ones — shared weights,
	// KV cache, expert storage — come from the model manifest, never
	// from here).
	Resources struct {
		GPU struct {
			Mode             string `yaml:"mode" json:"mode"`
			VRAMReserveBytes uint64 `yaml:"vram_reserve_bytes" json:"vram_reserve_bytes"`
		} `yaml:"gpu" json:"gpu"`
		HostRAMBytes   uint64 `yaml:"host_ram_bytes" json:"host_ram_bytes"`
		PinnedRAMBytes uint64 `yaml:"pinned_ram_bytes" json:"pinned_ram_bytes"`
		CPUThreads     uint64 `yaml:"cpu_threads" json:"cpu_threads"`
		KVCacheBytes   uint64 `yaml:"kv_cache_bytes" json:"kv_cache_bytes"`
		Storage        struct {
			DeviceID              string  `yaml:"device_id" json:"device_id"`
			ModelPath             string  `yaml:"model_path" json:"model_path"`
			ExpertPath            string  `yaml:"expert_path" json:"expert_path"`
			MinimumFreeBytes      uint64  `yaml:"minimum_free_bytes" json:"minimum_free_bytes"`
			MinimumReadMBps       float64 `yaml:"minimum_read_mbps" json:"minimum_read_mbps"`
			MinimumRandomReadIOPS float64 `yaml:"minimum_random_read_iops" json:"minimum_random_read_iops"`
			MaximumP95LatencyMs   float64 `yaml:"maximum_p95_latency_ms" json:"maximum_p95_latency_ms"`
		} `yaml:"storage" json:"storage"`
	} `yaml:"resources" json:"resources"`

	// Network — the §8.4 network contract: the compute-internal
	// profile, NO published ports (a published port is a new network
	// surface — v19.8.0 refuses it BY NAME).
	Network struct {
		NetworkProfile string `yaml:"network_profile" json:"network_profile"`
		PublishedPorts []int  `yaml:"published_ports" json:"published_ports"`
	} `yaml:"network" json:"network"`

	// Health — the §8.4 health probe contract (http /health with a
	// startup timeout).
	Health struct {
		Type                  string `yaml:"type" json:"type"`
		Path                  string `yaml:"path" json:"path"`
		StartupTimeoutSeconds int    `yaml:"startup_timeout_seconds" json:"startup_timeout_seconds"`
	} `yaml:"health" json:"health"`

	// SourcePath — where this profile was loaded from (provenance).
	SourcePath string `yaml:"-" json:"source_path,omitempty"`
}

// isPlaceholder reports whether s carries the roadmap's own placeholder
// markers (§8.4's example values: "REPLACE_WITH_VERIFIED_DIGEST",
// "VERIFIED_VERSION", ... — possibly embedded after a pin). A
// placeholder in a REQUIRED field is an UNKNOWN compatibility fact, and
// §8.2's rule is absolute: "If any required compatibility fact is
// unknown, mark the profile experimental and disable production
// scheduling" — this loader goes further and REFUSES the profile BY
// NAME (a placeholder that loads is a future phantom capability).
func isPlaceholder(s string) bool {
	return strings.Contains(s, "REPLACE_WITH") || strings.Contains(s, "VERIFIED_")
}

// placeholderReasons scans every required string field for placeholder
// markers (deterministic field order; one named reason per finding).
func placeholderReasons(p *RuntimeProfile) []string {
	var reasons []string
	check := func(field, value string) {
		if isPlaceholder(value) {
			reasons = append(reasons, fmt.Sprintf("%s %q carries a placeholder value — roadmap §8.4: never enable a profile containing placeholder values; the configuration loader must reject them (and it does)", field, value))
		}
	}
	check("profile_id", p.ProfileID)
	check("backend_id", p.BackendID)
	check("image.reference", p.Image.Reference)
	check("compatibility.model_manifest", p.Compatibility.ModelManifest)
	check("compatibility.checkpoint_sha256", p.Compatibility.CheckpointSHA256)
	check("compatibility.cuda", p.Compatibility.CUDA)
	check("compatibility.driver_minimum", p.Compatibility.DriverMinimum)
	for i, c := range p.Compatibility.GPUComputeCapabilities {
		check(fmt.Sprintf("compatibility.gpu_compute_capabilities[%d]", i), c)
	}
	check("execution.strategy", p.Execution.Strategy)
	check("resources.storage.device_id", p.Resources.Storage.DeviceID)
	check("resources.storage.model_path", p.Resources.Storage.ModelPath)
	check("resources.storage.expert_path", p.Resources.Storage.ExpertPath)
	check("network.network_profile", p.Network.NetworkProfile)
	check("health.type", p.Health.Type)
	check("health.path", p.Health.Path)
	return reasons
}

// hex64Re — the 64-hex fingerprint shape (shared discipline with
// modelmeta; duplicated here so the leaf package stays dependency-free
// beyond its own charter).
var hex64Re = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// ValidateRuntimeProfile returns the named reasons a profile is invalid
// (empty = valid — the profile may still be DISABLED, which is honest
// documentation, never a schedulable claim). The rules, in order:
//
//   - schema_version must be exactly "scarlix.runtime-profile/v1".
//   - profile_id: required, closed-charset slug.
//   - backend_id must be "infer_ssd" (a profile for another backend is
//     a wiring error).
//   - experimental MUST be true (§8.8's initial status — a
//     non-experimental profile is a production claim without hardware
//     acceptance, refused BY NAME).
//   - image.reference: required, @sha256:-pinned ("digest-pinned and
//     fixed" — §8.4's exit criterion).
//   - compatibility: model_manifest + checkpoint fingerprint (64 hex)
//   - cuda + driver_minimum + non-empty compute capabilities (the
//     §8.2 evidence dimensions; unknown facts refuse the profile).
//   - execution.strategy must be "ssd-backed-moe" (the one strategy the
//     engine executes); context_limit > 0; max_concurrent_requests must
//     be EXACTLY 1 (§8.8: "one active workload per SSD profile unless
//     contention tests justify more" — no contention evidence exists in
//     v19.8.0, so more than one is an evidence-less contention claim).
//   - resources.gpu.mode must be "exclusive" (§8.8: one allocation per
//     GPU); the additive demands may be 0 (the example's honesty).
//   - resources.storage: device_id + model_path + expert_path required;
//     minimum_free_bytes > 0; minimum_read_mbps > 0;
//     minimum_random_read_iops > 0; maximum_p95_latency_ms > 0 (the
//     §8.6 measured floors — a floor of 0 would be "any device
//     qualifies", which is the phantom the qualification exists to
//     refuse).
//   - network.network_profile required; published_ports must be EMPTY
//     (v19.8.0 publishes nothing — a port is a new network surface).
//   - health: type "http", a path starting with "/", startup timeout
//     > 0.
//   - NO placeholder values anywhere.
func ValidateRuntimeProfile(p *RuntimeProfile) []string {
	var reasons []string
	add := func(format string, args ...interface{}) {
		reasons = append(reasons, fmt.Sprintf(format, args...))
	}

	if p.SchemaVersion != RuntimeProfileSchemaVersion {
		add("schema_version %q is not %q — a future profile schema must be read by future code, this loader refuses it by name", p.SchemaVersion, RuntimeProfileSchemaVersion)
	}
	if strings.TrimSpace(p.ProfileID) == "" {
		add("profile_id is required (the profile's identity — carried into plans as the config profile)")
	} else if !profileIDRe.MatchString(p.ProfileID) {
		add("profile_id %q is not a safe closed identifier (lowercase slug: alnum start, alnum/dot/dash/underscore, max 128)", p.ProfileID)
	}
	if p.BackendID != string(InferSSDBackendID) {
		add("backend_id %q is not %q — a runtime profile in internal/backends/infer_ssd is for the InferSSD backend, and a profile for any other backend is a wiring error refused by name", p.BackendID, InferSSDBackendID)
	}
	if !p.Experimental {
		add("experimental must be true (roadmap §8.8's initial status — a non-experimental profile is a PRODUCTION claim, and production scheduling is disabled until the compatibility matrix + target-host acceptance pass; §8.2: an unknown compatibility fact disables production scheduling, it never upgrades itself)")
	}
	if strings.TrimSpace(p.Image.Reference) == "" {
		add("image.reference is required (the engine container — digest-pinned and fixed, roadmap §8.4's exit criterion)")
	} else if !strings.Contains(p.Image.Reference, "@sha256:") {
		add("image.reference %q carries no @sha256: digest — runtime profiles are digest-pinned and fixed (roadmap §8.4's exit criterion)", p.Image.Reference)
	}
	if strings.TrimSpace(p.Compatibility.ModelManifest) == "" {
		add("compatibility.model_manifest is required (§8.2's model dimension: the exact manifest this profile binds to)")
	}
	if !hex64Re.MatchString(p.Compatibility.CheckpointSHA256) {
		add("compatibility.checkpoint_sha256 %q is not a 64-hex-char SHA-256 fingerprint — the profile binds to the EXACT checkpoint (§8.2's model dimension)", p.Compatibility.CheckpointSHA256)
	}
	if strings.TrimSpace(p.Compatibility.CUDA) == "" {
		add("compatibility.cuda is required (§8.2's CUDA dimension: exact runtime/toolkit version)")
	}
	if strings.TrimSpace(p.Compatibility.DriverMinimum) == "" {
		add("compatibility.driver_minimum is required (§8.2's GPU dimension: the minimum driver)")
	}
	if len(p.Compatibility.GPUComputeCapabilities) == 0 {
		add("compatibility.gpu_compute_capabilities is required non-empty (§8.2's GPU dimension: the compute capabilities the build was verified against)")
	}
	for i, c := range p.Compatibility.GPUComputeCapabilities {
		if strings.TrimSpace(c) == "" {
			add("compatibility.gpu_compute_capabilities[%d] is empty — a placeholder slot is not a capability", i)
		}
	}
	if p.Execution.Strategy != StrategySSDBackedMoE {
		add("execution.strategy %q is not %q — the one v19.8.0 strategy the InferSSD engine executes (the planner's closed vocabulary gains exactly this member)", p.Execution.Strategy, StrategySSDBackedMoE)
	}
	if p.Execution.ContextLimit <= 0 {
		add("execution.context_limit must be > 0 (the profile's serving ceiling)")
	}
	if p.Execution.MaxConcurrentRequests != 1 {
		add("execution.max_concurrent_requests must be exactly 1 in v19.8.0 — roadmap §8.8: one active workload per SSD profile unless contention tests justify more, and no contention evidence exists yet (relaxing this is a §8.6 evidence act, never a config edit)")
	}
	if p.Resources.GPU.Mode != GPUModeExclusive {
		add("resources.gpu.mode %q is not %q — one allocation per GPU (roadmap §8.8's enablement policy; sharing without compute-share enforcement is a phantom capability)", p.Resources.GPU.Mode, GPUModeExclusive)
	}
	if strings.TrimSpace(p.Resources.Storage.DeviceID) == "" {
		add("resources.storage.device_id is required (the approved SSD path's device — the qualification joins it against the measured read profile)")
	}
	if strings.TrimSpace(p.Resources.Storage.ModelPath) == "" {
		add("resources.storage.model_path is required (the approved model path)")
	}
	if strings.TrimSpace(p.Resources.Storage.ExpertPath) == "" {
		add("resources.storage.expert_path is required (the approved expert path — SSD-backed expert paging IS the strategy)")
	}
	if p.Resources.Storage.MinimumFreeBytes == 0 {
		add("resources.storage.minimum_free_bytes must be > 0 (a zero floor admits a full device — the phantom the qualification refuses)")
	}
	if p.Resources.Storage.MinimumReadMBps <= 0 {
		add("resources.storage.minimum_read_mbps must be > 0 (§8.6's measured sequential floor)")
	}
	if p.Resources.Storage.MinimumRandomReadIOPS <= 0 {
		add("resources.storage.minimum_random_read_iops must be > 0 (§8.6's measured random floor — SSD-backed expert paging is sensitive to random reads, not only sequential MB/s)")
	}
	if p.Resources.Storage.MaximumP95LatencyMs <= 0 {
		add("resources.storage.maximum_p95_latency_ms must be > 0 (§8.6's measured latency ceiling)")
	}
	if strings.TrimSpace(p.Network.NetworkProfile) == "" {
		add("network.network_profile is required (the compute-internal network)")
	}
	if len(p.Network.PublishedPorts) != 0 {
		add("network.published_ports must be EMPTY in v19.8.0 — a published port is a new network surface (roadmap §8.4's example publishes nothing; the Host Bridge path owns every future surface decision)")
	}
	if p.Health.Type != HealthTypeHTTP {
		add("health.type %q is not %q — the v19.8.0 health probe contract is http", p.Health.Type, HealthTypeHTTP)
	}
	if !strings.HasPrefix(p.Health.Path, "/") {
		add("health.path %q must start with \"/\" (an http health path)", p.Health.Path)
	}
	if p.Health.StartupTimeoutSeconds <= 0 {
		add("health.startup_timeout_seconds must be > 0 (the startup window)")
	}
	reasons = append(reasons, placeholderReasons(p)...)
	return reasons
}

// LoadRuntimeProfile reads + parses + validates ONE runtime profile
// file (fail-closed: a malformed or invalid profile is a NAMED error,
// never a silent default; the enabled flag is HONEST DATA — the caller
// decides what a disabled profile means, and the planner's seam means
// "not configured").
func LoadRuntimeProfile(path string) (*RuntimeProfile, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("infer_ssd: runtime profile path is empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("infer_ssd: runtime profile read failed: %w", err)
	}
	var p RuntimeProfile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("infer_ssd: runtime profile %s YAML parse failed: %w", path, err)
	}
	p.SourcePath = path
	if reasons := ValidateRuntimeProfile(&p); len(reasons) > 0 {
		return nil, fmt.Errorf("infer_ssd: runtime profile %s is invalid: %s", path, strings.Join(reasons, "; "))
	}
	return &p, nil
}

// EnabledProfileView is the operator-facing JSON summary of an enabled
// profile (served by the planner state view — the full profile stays
// in the operator's file; the view carries the facts an operator
// audits: the pin, the binding, the floors, the enablement status).
type EnabledProfileView struct {
	ProfileID         string  `json:"profile_id"`
	Enabled           bool    `json:"enabled"`
	Experimental      bool    `json:"experimental"`
	ImageReference    string  `json:"image_reference"`
	ModelManifest     string  `json:"model_manifest"`
	CheckpointSHA256  string  `json:"checkpoint_sha256"`
	Strategy          string  `json:"strategy"`
	ContextLimit      int     `json:"context_limit"`
	MaxConcurrent     int     `json:"max_concurrent_requests"`
	StorageDeviceID   string  `json:"storage_device_id"`
	MinimumReadMBps   float64 `json:"minimum_read_mbps"`
	MinimumRandomIOPS float64 `json:"minimum_random_read_iops"`
	MaximumP95Ms      float64 `json:"maximum_p95_latency_ms"`
	SourcePath        string  `json:"source_path,omitempty"`
}

// View renders the operator-facing summary (called on a VALIDATED
// profile; a nil profile renders nil — the honest "not configured").
func (p *RuntimeProfile) View() *EnabledProfileView {
	if p == nil {
		return nil
	}
	return &EnabledProfileView{
		ProfileID:         p.ProfileID,
		Enabled:           p.Enabled,
		Experimental:      p.Experimental,
		ImageReference:    p.Image.Reference,
		ModelManifest:     p.Compatibility.ModelManifest,
		CheckpointSHA256:  p.Compatibility.CheckpointSHA256,
		Strategy:          p.Execution.Strategy,
		ContextLimit:      p.Execution.ContextLimit,
		MaxConcurrent:     p.Execution.MaxConcurrentRequests,
		StorageDeviceID:   p.Resources.Storage.DeviceID,
		MinimumReadMBps:   p.Resources.Storage.MinimumReadMBps,
		MinimumRandomIOPS: p.Resources.Storage.MinimumRandomReadIOPS,
		MaximumP95Ms:      p.Resources.Storage.MaximumP95LatencyMs,
		SourcePath:        p.SourcePath,
	}
}
