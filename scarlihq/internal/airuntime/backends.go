// Backend capability contract (v19.7.2, evolved v19.8.0 — Compute Fabric
// Phases 3-4, roadmap §7.2/§7.3/§8 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md).
//
// This file separates the CAPABILITY tier from the ADAPTER tier:
//
//   - Provider (provider.go) is the closed 4-member enumeration the
//     transport/adapters consume — sglang/vllm/beellama/ollama, the
//     runtimes that exist TODAY with adapters, probing, failover order,
//     chat budgets and tests.
//   - BackendID (here) is the CAPABILITY identity — the administrator-
//     owned, validated-at-construction registry rows the deterministic
//     planner consumes. It deliberately includes infer_ssd (roadmap
//     §7.2's own const block): the v19.8.0 §8.3 ADAPTER
//     (internal/backends/infer_ssd — planning-side: model binding,
//     estimation, runtime profile, health contract, SSD qualification)
//     exists behind an operator-owned EXPERIMENTAL runtime profile,
//     while the backend still has NO transport-tier chat provider
//     (ProviderForBackend names that honestly) — so the planner's
//     placement × runtime join refuses every infer_ssd plan in every
//     default deployment until an enabled profile AND an observed
//     running runtime both exist.
//
// The registry is ADMINISTRATOR-OWNED and VALIDATED at construction
// (roadmap §7.2: "Do not allow an HTTP caller or LLM to register
// arbitrary backends"): there is NO registration surface — no HTTP
// route, no env var, no file can add a backend row. Extending the
// registry is a reviewed release act following §7.3's eleven steps:
// add the backend identity; update the closed list and validation;
// update capability metadata; update health probing and health
// semantics; update adapter construction; update failover/selection
// policy; update chat/request budgets and limits; update API schemas
// and MCP contracts where exposed; update configuration validation;
// update unit, integration, race and compatibility tests; update
// documentation and threat model. Do not merely add a string constant.
package airuntime

import (
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Backend identity (roadmap §7.2)
// ---------------------------------------------------------------------------

// BackendID is the capability-tier identity of an inference backend.
// The set is CLOSED in code: sglang, vllm, beellama, ollama (the four
// real local-stack runtimes, each aligned 1:1 with a Provider) plus
// infer_ssd (roadmap §7.2's declared-future backend — capability row
// only, no adapter, refused by the runtime-presence join in every
// v19.7.2 plan; v19.8.0 completes §7.3's eleven steps for it).
type BackendID string

const (
	// BackendSGLang — Tier 1 GPU backend (agents, RadixAttention).
	BackendSGLang BackendID = "sglang"
	// BackendVLLM — Tier 2 GPU backend (experimental in the v19.0.6
	// stack; the experimental flag rides the capability row).
	BackendVLLM BackendID = "vllm"
	// BackendBeeLlama — Tier 3 CPU backend (llama.cpp).
	BackendBeeLlama BackendID = "beellama"
	// BackendOllama — Tier 4 CPU backend (starter fallback).
	BackendOllama BackendID = "ollama"
	// BackendInferSSD — the SSD-expert backend (roadmap §8, v19.8.0:
	// Compute Fabric Phase 4). The capability row is closed and
	// administrator-owned as ever; the v19.8.0 ADAPTER (planning-side:
	// internal/backends/infer_ssd — model binding, estimation, runtime
	// profile, health contract, SSD qualification) exists and is
	// EXPERIMENTAL behind an operator-owned, enabled:false,
	// digest-pinned runtime profile; the backend is STILL refused by
	// the placement × runtime join in every default deployment (nothing
	// runs it until the Host Bridge path + the target-host acceptance
	// land), and it has NO transport-tier chat provider — ProviderForBackend
	// names that honestly.
	BackendInferSSD BackendID = "infer_ssd"
)

// AllBackends is the closed capability enumeration (a copy — the caller
// cannot extend the enumeration through the returned slice).
func AllBackends() []BackendID {
	return []BackendID{
		BackendSGLang,
		BackendVLLM,
		BackendBeeLlama,
		BackendOllama,
		BackendInferSSD,
	}
}

// IsValid reports whether b is part of the closed capability enumeration.
func (b BackendID) IsValid() bool {
	switch b {
	case BackendSGLang, BackendVLLM, BackendBeeLlama, BackendOllama, BackendInferSSD:
		return true
	}
	return false
}

// Validate checks the backend shape AND membership in the closed
// capability enumeration (mirrors Provider.Validate's discipline: an
// address-shaped or control-character slug is rejected BY NAME — a
// backend here is a capability identity, never an endpoint).
func (b BackendID) Validate() error {
	s := string(b)
	if s == "" {
		return fmt.Errorf("airuntime: backend id is empty")
	}
	if strings.ContainsAny(s, " \t\n\r") {
		return fmt.Errorf("airuntime: backend id %q contains whitespace", s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("airuntime: backend id %q contains control characters", s)
		}
	}
	if strings.ContainsAny(s, ":/\\") {
		return fmt.Errorf("airuntime: backend id %q looks like an address (port/path) — backend ids are capability identities, not endpoints", s)
	}
	if !b.IsValid() {
		return fmt.Errorf("airuntime: backend id %q is not in the closed capability enumeration (sglang/vllm/beellama/ollama/infer_ssd)", s)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Backend capabilities (roadmap §7.2 — the field set is the contract)
// ---------------------------------------------------------------------------

// BackendCapabilities is one registry row: what a backend CAN DO, as
// declared by the administrator and consumed by the deterministic
// planner's hard constraints (roadmap §7.5: model/backend compatibility,
// GPU architecture compatibility, required runtime features). The
// fields are capability CLAIMS about the deployed stack — conservative,
// honest, and updated only through §7.3's eleven-step procedure.
type BackendCapabilities struct {
	// BackendID — the capability identity (closed enumeration).
	BackendID BackendID `json:"backend_id"`
	// ModelFormats — the checkpoint formats this backend executes
	// (matched against the manifest's checkpoint.format).
	ModelFormats []string `json:"model_formats"`
	// Architectures — the parameter architectures this backend executes
	// (matched against the manifest's architecture.kind).
	Architectures []string `json:"architectures"`
	// Features — the runtime feature tokens this backend provides (the
	// manifest's required_features must be a SUBSET — matched exactly,
	// never fuzzily).
	Features []string `json:"features"`
	// SupportsMoE — the backend can execute mixture-of-experts
	// checkpoints (llama.cpp-family engines run MoE ggufs fully
	// resident; the GPU tiers page experts).
	SupportsMoE bool `json:"supports_moe"`
	// SupportsSSDExperts — the backend's OWN SSD-backed expert mode
	// (infer_ssd's execution). v19.8.0: the adapter exists
	// (internal/backends/infer_ssd) and the planner derives the
	// ssd-backed-moe strategy for it — but ONLY behind an enabled,
	// validated runtime profile with a MEASURED SSD qualification
	// (roadmap §8.6); every default deployment refuses infer_ssd plans
	// at the placement × runtime join until then.
	SupportsSSDExperts bool `json:"supports_ssd_experts"`
	// SupportsExpertPack — the backend executes the SGLang Expert Pack
	// artifact. v19.8.0 flips this TRUE for sglang WITH the pack-engine
	// validation evidence: the scarlix.artifact/v1 pack manifest
	// (modelmeta.ExpertPackManifest) binds a pack to the EXACT checkpoint
	// and to the ONE engine it is validated for (cross-engine pack
	// compatibility is refused BY NAME — this release's must-not-do).
	// The flag is the capability CLAIM; USING it for execution still
	// requires the operator's verified pack manifest + the §8.7
	// target-host acceptance matrix (the v19.8.0 planner keeps pack
	// execution unplanned — the §8.8 enablement policy).
	SupportsExpertPack bool `json:"supports_expert_pack"`
	// SupportsMultiGPU — tensor-parallel execution across GPUs. v19.7.2
	// plans remain SINGLE-GPU regardless (multi-GPU splitting needs a
	// measured per-GPU overhead profile that does not exist yet — the
	// honest named refusal, not a guessed split).
	SupportsMultiGPU bool `json:"supports_multi_gpu"`
	// SupportsCPUOffload — the backend can offload layers/activations
	// to host RAM while retaining GPU execution.
	SupportsCPUOffload bool `json:"supports_cpu_offload"`
	// HealthRequired — a live HTTP healthcheck gates this backend's
	// usability (every tier of the local stack is probed; the planner
	// requires RuntimeUp AND RuntimeHealthy).
	HealthRequired bool `json:"health_required"`
	// Experimental — the deployed stack marks this backend experimental
	// (vllm Tier 2, infer_ssd): the planner carries the flag into the
	// plan's explanation — an operator sees the experimental selection.
	Experimental bool `json:"experimental"`
	// RequiresGPU — the execution class the planner's vector derivation
	// needs: GPU-resident weights (VRAM demand) vs. host-RAM-resident
	// weights. v19.7.2 addition (documented deviation from §7.2's field
	// list): the placement derivation requires the typed execution-class
	// fact — a magic feature token or a tier-number heuristic would be
	// weaker typing for the same claim.
	RequiresGPU bool `json:"requires_gpu"`
	// AdapterNote — the honest v19.7.2 adapter status ("adapter:
	// provider-tier" for the four runtimes; the v19.8.0 marker for
	// infer_ssd). Carried in the registry view so an operator reading
	// GET /api/compute/plan sees the declared-future distinction.
	AdapterNote string `json:"adapter_note"`
}

// backendRegistry is the administrator-owned CLOSED registry: one row
// per BackendID, encoded ONCE here. Not a runtime option, not a request
// parameter, not env-configurable — a silent registry edit is
// structurally impossible (no mutation surface exists; ValidateRegistry
// asserts the closed shape at construction).
var backendRegistry = map[BackendID]BackendCapabilities{
	BackendSGLang: {
		BackendID:          BackendSGLang,
		ModelFormats:       []string{"safetensors"},
		Architectures:      []string{"dense", "moe"},
		Features:           []string{"chat", "embed", "radix-attention"},
		SupportsMoE:        true,
		SupportsSSDExperts: false,
		// v19.8.0 (roadmap §8.1/§8.5): the Expert Pack is a SGLang
		// artifact/capability mode — the claim rides the closed registry
		// WITH the validation evidence (the scarlix.artifact/v1 manifest
		// binding: one pack, one exact checkpoint, one engine). Pack
		// EXECUTION stays unplanned until pack-byte verification + the
		// §8.7 target-host acceptance matrix pass (the §8.8 enablement
		// policy — never a silent capability upgrade).
		SupportsExpertPack: true,
		SupportsMultiGPU:   true,
		SupportsCPUOffload: true,
		HealthRequired:     true,
		Experimental:       false,
		RequiresGPU:        true,
		AdapterNote:        "adapter: provider-tier (sglang, Tier 1, GPU 0; v19.8.0: sglang_expert_pack capability with §8.5 manifest evidence)",
	},
	BackendVLLM: {
		BackendID:          BackendVLLM,
		ModelFormats:       []string{"safetensors"},
		Architectures:      []string{"dense", "moe"},
		Features:           []string{"chat", "embed"},
		SupportsMoE:        true,
		SupportsSSDExperts: false,
		SupportsExpertPack: false,
		SupportsMultiGPU:   true,
		SupportsCPUOffload: true,
		HealthRequired:     true,
		Experimental:       true, // the v19.0.6 stack marks vllm experimental
		RequiresGPU:        true,
		AdapterNote:        "adapter: provider-tier (vllm, Tier 2, GPU 1, experimental)",
	},
	BackendBeeLlama: {
		BackendID:          BackendBeeLlama,
		ModelFormats:       []string{"gguf"},
		Architectures:      []string{"dense", "moe"},
		Features:           []string{"chat"},
		SupportsMoE:        true, // llama.cpp executes MoE ggufs fully resident
		SupportsSSDExperts: false,
		SupportsExpertPack: false,
		SupportsMultiGPU:   false,
		SupportsCPUOffload: true,
		HealthRequired:     true,
		Experimental:       false,
		RequiresGPU:        false,
		AdapterNote:        "adapter: provider-tier (beellama, Tier 3, CPU llama.cpp)",
	},
	BackendOllama: {
		BackendID:          BackendOllama,
		ModelFormats:       []string{"gguf"},
		Architectures:      []string{"dense", "moe"},
		Features:           []string{"chat", "embed"},
		SupportsMoE:        true,
		SupportsSSDExperts: false,
		SupportsExpertPack: false,
		SupportsMultiGPU:   false,
		SupportsCPUOffload: true,
		HealthRequired:     true,
		Experimental:       false,
		RequiresGPU:        false,
		AdapterNote:        "adapter: provider-tier (ollama, Tier 4, CPU fallback)",
	},
	BackendInferSSD: {
		BackendID:          BackendInferSSD,
		ModelFormats:       []string{"safetensors"},
		Architectures:      []string{"moe"},
		Features:           []string{"chat"},
		SupportsMoE:        true,
		SupportsSSDExperts: true, // the engine's own SSD-expert mode
		SupportsExpertPack: false,
		SupportsMultiGPU:   false,
		SupportsCPUOffload: false,
		HealthRequired:     true,
		Experimental:       true,
		RequiresGPU:        true,
		AdapterNote:        "adapter: experimental (v19.8.0 roadmap §8.3 — internal/backends/infer_ssd, planning-side: model binding + estimation + runtime profile + health contract + SSD qualification; execution flows through the Host Bridge path, not the transport tier; every default deployment still refuses infer_ssd plans — the runtime-presence join + the §8.8 enabled:false profile)",
	},
}

// BackendCapabilitiesFor returns the registry row for b (a COPY — the
// caller cannot mutate the closed registry through the returned row).
// An unknown backend is an error (fail-closed — capabilities are never
// guessed, defaulted, or synthesized for an unregistered slug).
func BackendCapabilitiesFor(b BackendID) (BackendCapabilities, error) {
	if err := b.Validate(); err != nil {
		return BackendCapabilities{}, err
	}
	row, ok := backendRegistry[b]
	if !ok {
		// Unreachable while the closed map and the enumeration stay in
		// sync — ValidateRegistry makes the invariant a construction
		// error, so this branch is the belt to that suspenders.
		return BackendCapabilities{}, fmt.Errorf("airuntime: backend %q passed validation but has no registry row — the enumeration and the registry drifted (a release act must fix this, never a runtime guess)", b)
	}
	out := row
	out.ModelFormats = append([]string(nil), row.ModelFormats...)
	out.Architectures = append([]string(nil), row.Architectures...)
	out.Features = append([]string(nil), row.Features...)
	return out, nil
}

// ValidateBackendRegistry asserts the CLOSED registry's shape — every
// enumeration member has exactly one row, every row's ID is a valid
// member, and no row carries empty format/architecture lists (a backend
// that executes nothing is a registry bug, not a capability). The
// v19.7.2 planner construction calls this: an inconsistent registry is
// a STARTUP failure, never a per-request surprise (roadmap §7.2:
// "validated at startup").
func ValidateBackendRegistry() error {
	seen := map[BackendID]bool{}
	for _, b := range AllBackends() {
		row, ok := backendRegistry[b]
		if !ok {
			return fmt.Errorf("airuntime: backend registry is missing the row for %q (closed enumeration drift — a release act must restore the 1:1 shape)", b)
		}
		if row.BackendID != b {
			return fmt.Errorf("airuntime: backend registry row for %q carries id %q (row/id mismatch)", b, row.BackendID)
		}
		if len(row.ModelFormats) == 0 {
			return fmt.Errorf("airuntime: backend %q declares no model formats (a backend that executes nothing is a registry bug)", b)
		}
		if len(row.Architectures) == 0 {
			return fmt.Errorf("airuntime: backend %q declares no architectures", b)
		}
		if seen[b] {
			return fmt.Errorf("airuntime: backend %q has duplicate registry rows", b)
		}
		seen[b] = true
	}
	if len(seen) != len(backendRegistry) {
		return fmt.Errorf("airuntime: backend registry carries %d rows for %d enumeration members (extra rows exist — the registry is not closed)", len(backendRegistry), len(seen))
	}
	return nil
}

// RegistryView is the read-only registry surface (GET /api/compute/plan
// serves it): every row, deterministically ordered by backend ID.
func RegistryView() []BackendCapabilities {
	ids := make([]BackendID, 0, len(backendRegistry))
	for b := range backendRegistry {
		ids = append(ids, b)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]BackendCapabilities, 0, len(ids))
	for _, b := range ids {
		row, err := BackendCapabilitiesFor(b)
		if err != nil {
			// Unreachable after ValidateBackendRegistry — the view never
			// synthesizes around an inconsistent registry.
			continue
		}
		out = append(out, row)
	}
	return out
}

// ---------------------------------------------------------------------------
// Provider ↔ BackendID alignment (the adapter tier vs. the capability tier)
// ---------------------------------------------------------------------------

// BackendForProvider maps the adapter-tier Provider to the capability-tier
// BackendID. The four real runtimes map 1:1.
func BackendForProvider(p Provider) (BackendID, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	switch p {
	case ProviderSGLang:
		return BackendSGLang, nil
	case ProviderVLLM:
		return BackendVLLM, nil
	case ProviderBeeLlama:
		return BackendBeeLlama, nil
	case ProviderOllama:
		return BackendOllama, nil
	}
	return "", fmt.Errorf("airuntime: provider %q has no backend id", p)
}

// ProviderForBackend maps the capability-tier BackendID to the
// adapter-tier Provider. infer_ssd is the honest named error: it has
// NO transport-tier chat provider in v19.8.0 — the §8.3 adapter is
// planning-side (internal/backends/infer_ssd) and its execution flows
// through the Host Bridge path, never the provider failover chain.
// Callers must never guess a transport provider for it.
func ProviderForBackend(b BackendID) (Provider, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	switch b {
	case BackendSGLang:
		return ProviderSGLang, nil
	case BackendVLLM:
		return ProviderVLLM, nil
	case BackendBeeLlama:
		return ProviderBeeLlama, nil
	case BackendOllama:
		return ProviderOllama, nil
	case BackendInferSSD:
		return "", fmt.Errorf("airuntime: backend %q has no transport provider — the v19.8.0 §8.3 adapter is planning-side (internal/backends/infer_ssd); chat execution flows through the Host Bridge path, not the provider tier", b)
	}
	return "", fmt.Errorf("airuntime: backend %q has no provider mapping", b)
}

// BackendTier is the capability-tier policy priority (roadmap §7.5's
// tie-break 1: "highest policy priority"). It mirrors the v19.0.6
// release-locked failover order for the four runtimes; infer_ssd gets
// the declared-future priority 99 — it is NEVER preferred while its
// adapter does not exist.
func BackendTier(b BackendID) (int, error) {
	if err := b.Validate(); err != nil {
		return 0, err
	}
	switch b {
	case BackendSGLang:
		return 1, nil
	case BackendVLLM:
		return 2, nil
	case BackendBeeLlama:
		return 3, nil
	case BackendOllama:
		return 4, nil
	case BackendInferSSD:
		return 99, nil
	}
	return 0, fmt.Errorf("airuntime: backend %q has no tier", b)
}
