// Package infer_ssd — manifest.go: the §8.2 model compatibility
// declaration — the ten-dimension compatibility matrix's MODEL side
// (the exact checkpoint binding, the architecture, the quantization)
// evaluated as pure functions over (profile, model execution metadata).
package infer_ssd

import (
	"fmt"
	"strings"

	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
)

// ModelBindingError — the closed, named refusal vocabulary of the
// model × profile compatibility join (§8.2: "For each candidate
// combination, record: Model: Repository/revision and checkpoint hash;
// Architecture: Dense/MoE, expert count and routing metadata;
// Quantization: Format and quantization details" — every unrecorded or
// mismatched dimension refuses BY NAME).
type ModelBindingError string

const (
	// ErrModelFormatUnsupported — the checkpoint format is not
	// safetensors (the engine's only format).
	ErrModelFormatUnsupported ModelBindingError = "model-format-unsupported"
	// ErrModelArchitectureUnsupported — the architecture is not MoE
	// (SSD-backed execution IS expert paging; a dense checkpoint has no
	// experts to page).
	ErrModelArchitectureUnsupported ModelBindingError = "model-architecture-unsupported"
	// ErrModelGeometryUnknown — the MoE geometry is unknown (expert
	// count or expert bytes missing — the estimate derivation needs the
	// expert storage demand; a parameter label is a label, not bytes).
	ErrModelGeometryUnknown ModelBindingError = "model-geometry-unknown"
	// ErrModelNotBound — the profile binds a DIFFERENT model manifest
	// or checkpoint (the profile's compatibility block is the §8.2
	// evidence record for ONE exact combination).
	ErrModelNotBound ModelBindingError = "model-not-bound"
)

// ModelBinding is the §8.2 model-side verdict: the join of the profile's
// declared compatibility dimensions against one model's execution
// metadata. Qualified=false always carries the named code + detail.
type ModelBinding struct {
	Qualified bool
	Code      ModelBindingError
	Detail    string
}

// BindModel evaluates the model × profile compatibility (§8.2's model/
// architecture/quantization dimensions + §8.4's exact binding):
//
//   - the checkpoint format must be safetensors,
//   - the architecture must be MoE with KNOWN geometry (expert count
//     and expert bytes — the storage demand derivation),
//   - the profile's model_manifest + checkpoint_sha256 must match the
//     model EXACTLY (the profile is evidence for one combination).
//
// The profile's execution.context_limit ceiling is deliberately NOT
// checked here: it is a REQUEST-level bound (the planner refuses
// requests that exceed it, and the adapter's Estimate/BuildProfile
// refuse estimate/build calls that exceed it) — a model DECLARING a
// larger capability is legitimately servable at smaller contexts, and
// conflating the two would make the request-level refusal dead code.
//
// m is the modelmeta.ModelExecution projection of a schema-valid,
// placement-eligible manifest (the caller — planner or adapter — owns
// that gating; this join is the backend's own §8.2 answer).
func BindModel(profile *RuntimeProfile, m modelmeta.ModelExecution) ModelBinding {
	if profile == nil {
		return ModelBinding{Code: ErrModelNotBound, Detail: "no runtime profile configured — the model binding has nothing to bind to"}
	}
	if !strings.EqualFold(m.Format, SupportedModelFormat) {
		return ModelBinding{Code: ErrModelFormatUnsupported, Detail: fmt.Sprintf("checkpoint format %q is not %q (the engine's only format)", m.Format, SupportedModelFormat)}
	}
	if m.Architecture != modelmeta.ArchitectureMoE {
		return ModelBinding{Code: ErrModelArchitectureUnsupported, Detail: fmt.Sprintf("architecture %q is not %q — SSD-backed execution IS expert paging; a dense checkpoint has no experts to page (roadmap §8.1's conceptual split)", m.Architecture, SupportedArchitecture)}
	}
	if m.ExpertCount <= 0 || m.ExpertBytes == 0 {
		return ModelBinding{Code: ErrModelGeometryUnknown, Detail: fmt.Sprintf("moe geometry unknown (expert_count=%d, expert_bytes=%d) — the storage demand derivation needs the expert bytes; a parameter label is a label, not bytes", m.ExpertCount, m.ExpertBytes)}
	}
	if profile.Compatibility.ModelManifest != m.ID || !strings.EqualFold(profile.Compatibility.CheckpointSHA256, m.CheckpointSHA256) {
		return ModelBinding{Code: ErrModelNotBound, Detail: fmt.Sprintf("the profile binds model %q @ checkpoint %s; this is model %q @ %s — the profile's compatibility block is the §8.2 evidence record for ONE exact combination, and a different combination is a different profile", profile.Compatibility.ModelManifest, profile.Compatibility.CheckpointSHA256, m.ID, m.CheckpointSHA256)}
	}
	return ModelBinding{Qualified: true, Detail: fmt.Sprintf("model %q @ checkpoint %s: %s MoE, %d experts × %d bytes, declared context %d (served ≤ the profile ceiling %d) — bound to profile %q", m.ID, m.CheckpointSHA256, m.Format, m.ExpertCount, m.ExpertBytes, m.ContextLimit, profile.Execution.ContextLimit, profile.ProfileID)}
}

// ExpertStorageBytes derives the expert storage demand (the §8.4
// ssd-backed-moe derivation: the PAGED experts' bytes — shared weights
// and KV stay in VRAM). Callers pass a model that already passed
// BindModel (the geometry is known); a zero result on an unbound model
// is the caller's earlier named refusal, never a silent default here.
func ExpertStorageBytes(m modelmeta.ModelExecution) uint64 {
	if m.ExpertCount <= 0 {
		return 0
	}
	return uint64(m.ExpertCount) * m.ExpertBytes
}
