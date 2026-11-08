// Package modelmeta — model execution metadata and the verified manifest
// contract (v19.7.0, Compute Fabric Phase 1, roadmap §5.4/§5.5 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md).
//
// CHARTER:
//
//   - Metadata must come from a TRUSTED MANIFEST or verified model
//     metadata, never from an LLM guess (roadmap §5.4). Every field in a
//     ModelExecution is either manifest-declared, derived from the
//     manifest by a documented rule, or explicitly UNKNOWN.
//   - Unknown required values make a manifest INELIGIBLE for automatic
//     placement — never a defaulted guess (roadmap §5.5: "A manifest with
//     required unknown values must not be used for automatic placement").
//     PlacementEligibility carries the named reasons.
//   - Total parameters and active parameters are NOT interchangeable
//     (§5.4): TotalParameters describes checkpoint storage, ActiveParameters
//     describes per-token compute; both are carried as strings exactly as
//     declared ("XXB" is a declared label, not a parsed number — the
//     planner parses under its own policy).
//   - A checkpoint fingerprint binds an expert pack to the EXACT model
//     revision: the pack's compatibility is evaluated against the
//     manifest's checkpoint SHA-256, and cross-engine pack compatibility
//     is never assumed (§0.1 principle 9 — compatibility is evidence-
//     based; v19.8.0 owns the pack-engine validation evidence).
//   - The manifest schema is versioned ("scarlix.model/v2"); an unknown
//     schema_version is rejected BY NAME, fail-closed, because a silent
//     best-effort parse of a future schema would invent metadata.
package modelmeta

// Architecture classifies a model's parameter architecture for EXECUTION
// planning (roadmap §2.2: the execution-strategy concept — dense resident
// vs MoE expert paging vs other).
type Architecture string

const (
	// ArchitectureDense — every parameter participates in every token.
	ArchitectureDense Architecture = "dense"
	// ArchitectureMoE — mixture-of-experts: experts_per_token experts
	// activate per token; expert storage may be paged/offloaded.
	ArchitectureMoE Architecture = "moe"
	// ArchitectureOther — a declared architecture outside the two
	// planning categories (honest label, not a guess).
	ArchitectureOther Architecture = "other"
)

// ModelExecution is the normalized execution metadata of one model
// artifact (roadmap §5.4 — the field set is the contract). Zero values in
// the omitempty numeric fields mean "unknown" (see Manifest docs); the
// eligibility gate (PlacementEligibility) is the single consumer-side
// answer to "may this be planned automatically".
type ModelExecution struct {
	// ID — the manifest-declared model identity (must match the v1
	// inventory Model.ID it annotates, when it does).
	ID string `json:"id"`
	// Revision — the checkpoint revision label (HF revision or local
	// build id).
	Revision string `json:"revision"`
	// CheckpointSHA256 — the DECLARED checkpoint fingerprint (64 hex).
	// v19.7.1 (KPK-19.7.0-P2-04): the JSON key is declared_checkpoint_sha256
	// — the emitted provenance distinguishes DECLARED from VERIFIED bytes;
	// verification (VerifyManifestArtifacts) is the separate, recorded
	// answer (see ArtifactVerification).
	CheckpointSHA256 string `json:"declared_checkpoint_sha256"`
	// Architecture — dense | moe | other.
	Architecture Architecture `json:"architecture"`
	// TotalParameters — declared total-parameter label ("14B", "8x7B").
	// Describes CHECKPOINT STORAGE, not per-token compute.
	TotalParameters string `json:"total_parameters,omitempty"`
	// ActiveParameters — declared active-per-token label ("XB").
	// Describes PER-TOKEN COMPUTE, not checkpoint storage.
	ActiveParameters string `json:"active_parameters,omitempty"`
	// Quantization — the checkpoint quantization ("awq", "q4_k_m", ...).
	Quantization string `json:"quantization"`
	// Format — the checkpoint format ("safetensors", "gguf", ...).
	Format string `json:"format"`
	// ContextLimit — the model's maximum context length in tokens.
	ContextLimit int `json:"context_limit"`
	// KVCacheBytesPerToken — the per-token KV-cache footprint. 0 =
	// UNKNOWN (KV cache depends on architecture, context, batch,
	// precision and runtime implementation — §5.4 — and is a
	// runtime-measured quantity, not a manifest guess).
	KVCacheBytesPerToken uint64 `json:"kv_cache_bytes_per_token,omitempty"`
	// DenseFootprintBytes — the dense (non-expert) parameter bytes. 0 =
	// unknown. For dense models: the whole weight footprint.
	DenseFootprintBytes uint64 `json:"dense_footprint_bytes,omitempty"`
	// ExpertCount — number of experts (MoE only). 0 = unknown (for moe
	// this makes the manifest placement-ineligible).
	ExpertCount int `json:"expert_count,omitempty"`
	// ExpertsPerToken — experts activated per token (MoE only). 0 =
	// unknown (for moe: ineligible).
	ExpertsPerToken int `json:"experts_per_token,omitempty"`
	// ExpertBytes — per-expert storage bytes (MoE only). 0 = unknown
	// (ineligible for expert-paging placement).
	ExpertBytes uint64 `json:"expert_bytes,omitempty"`
	// SharedBytes — shared/non-expert parameter bytes (MoE only).
	SharedBytes uint64 `json:"shared_bytes,omitempty"`
	// SupportedBackends — the declared backend/engine identities that
	// may serve this artifact (e.g. ["sglang", "infer_ssd"]). Declared,
	// not intersected with the present runtimes — the planner (v19.7.2)
	// performs the intersection against live inventory.
	SupportedBackends []string `json:"supported_backends"`
	// RequiredFeatures — backend features the artifact requires
	// (e.g. "cuda-sm120", "paged-experts").
	RequiredFeatures []string `json:"required_features,omitempty"`
	// ExpertPack — the SGLang Expert Pack artifact declaration (nil/
	// disabled when absent).
	ExpertPack *ExpertPackArtifact `json:"expert_pack,omitempty"`
	// ManifestPath — where this metadata was loaded from (provenance).
	ManifestPath string `json:"manifest_path,omitempty"`
}

// ExpertPackArtifact is the SGLang Expert Pack declaration (roadmap
// §2.2: an artifact/storage mode — the pack is NOT a fifth runtime
// provider). A pack binds to the EXACT checkpoint revision of its
// manifest; compatible_backend names the ONE engine it is validated for
// (never "any" — cross-engine compatibility is not claimed).
type ExpertPackArtifact struct {
	// Enabled — false in the example/default manifest; a disabled pack
	// is documentation, not a usable artifact.
	Enabled bool `json:"enabled"`
	// Format — the pack artifact format (versioned by the engine).
	Format string `json:"format,omitempty"`
	// Version — the pack build version.
	Version string `json:"version,omitempty"`
	// SHA256 — the DECLARED pack artifact fingerprint (64 hex, required
	// when enabled). Declared, not verified-by-this-struct — see
	// CheckpointSHA256's note (v19.7.1, KPK-19.7.0-P2-04).
	SHA256 string `json:"declared_sha256,omitempty"`
	// CompatibleBackend — the single backend this pack is validated for
	// (e.g. "sglang"). Required when enabled.
	CompatibleBackend string `json:"compatible_backend,omitempty"`
}
