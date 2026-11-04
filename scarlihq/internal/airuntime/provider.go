// Package airuntime is the Universal AI Runtime foundation (CF-1, the
// v19.6.x line — Phase C opens).
//
// Phase B closed the deterministic Agent Fabric (v19.5.4): nothing in it
// contains an LLM, deliberately. Phase C builds the layer that will PROPOSE
// to that fabric — the fabric and the kernel remain the deciding, auditable
// path. The charter, encoded on one line and enforced by smoke check 28:
// "LLM navrhuje, politika rozhoduje" — the LLM proposes, policy decides.
//
// CF-1 scope (this file set): the typed foundations of the runtime — the
// closed provider enumeration of the REAL local stack, the explicit bounded
// model registry, observed fail-closed health, the normalized chat contract
// and the pure deterministic router. It is deliberately NOT yet wired into
// any HTTP/MCP surface (the AK-1 precedent): adapters, probing and the REST
// surface are CF-2+, after review. No user-facing surface changes in CF-1.
//
// Hard rules encoded here:
//   - the runtime normalizes the LOCAL sovereign stack only — it is not a
//     cloud-LLM gateway
//   - health is fail-closed: UNKNOWN never serves (trust is earned by
//     observation, not assumed by default)
//   - the router is pure: same inputs, same plan, byte for byte — and it
//     never executes anything
//   - network/IO-free by construction: no net/http, no sockets, no file
//     access, no exec anywhere in this package (runtime and tests)
package airuntime

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Provider enumeration (the REAL local stack, v19.0.6-locked failover order)
// ---------------------------------------------------------------------------

// Provider is a closed enumeration of the real local inference stack:
// sglang (Tier 1, GPU 0) · vllm (Tier 2, GPU 1, experimental) ·
// beellama (Tier 3, CPU llama.cpp) · ollama (Tier 4, CPU fallback).
// There are deliberately no cloud-LLM providers here — the runtime
// normalizes the LOCAL sovereign stack; it is not a cloud-LLM gateway.
// Extending the enumeration is a deliberate, reviewed act: AllProviders,
// Tier, DefaultFailoverOrder and the tests change together.
type Provider string

const (
	// ProviderSGLang — Tier 1 (GPU 0): agents, RadixAttention, default.
	ProviderSGLang Provider = "sglang"
	// ProviderVLLM — Tier 2 (GPU 1): high throughput, experimental flag only.
	ProviderVLLM Provider = "vllm"
	// ProviderBeeLlama — Tier 3 (CPU llama.cpp): offline fallback.
	ProviderBeeLlama Provider = "beellama"
	// ProviderOllama — Tier 4 (CPU): starter fallback.
	ProviderOllama Provider = "ollama"
)

// AllProviders is the closed enumeration (a copy — the caller cannot
// extend the enumeration through the returned slice).
func AllProviders() []Provider {
	return []Provider{
		ProviderSGLang,
		ProviderVLLM,
		ProviderBeeLlama,
		ProviderOllama,
	}
}

// IsValid reports whether p is part of the closed enumeration.
func (p Provider) IsValid() bool {
	switch p {
	case ProviderSGLang, ProviderVLLM, ProviderBeeLlama, ProviderOllama:
		return true
	}
	return false
}

// Validate checks the provider shape AND membership in the closed
// local-stack enumeration. Cloud-LLM names, ports, paths, whitespace and
// control characters are rejected — a provider here is a tier of the local
// stack, never an address and never a cloud endpoint.
func (p Provider) Validate() error {
	s := string(p)
	if s == "" {
		return fmt.Errorf("airuntime: provider is empty")
	}
	if strings.ContainsAny(s, " \t\n\r") {
		return fmt.Errorf("airuntime: provider %q contains whitespace", s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("airuntime: provider %q contains control characters", s)
		}
	}
	if strings.ContainsAny(s, ":/\\") {
		return fmt.Errorf("airuntime: provider %q looks like an address (port/path) — providers are local-stack tiers, not endpoints", s)
	}
	if !p.IsValid() {
		return fmt.Errorf("airuntime: provider %q is not in the closed local-stack enumeration (sglang/vllm/beellama/ollama) — the runtime is not a cloud-LLM gateway", s)
	}
	return nil
}

// Tier is the failover tier of a provider (1 = primary GPU .. 4 = CPU
// fallback). It encodes WHERE in DefaultFailoverOrder the provider sits.
type Tier int

const (
	TierSGLang   Tier = 1
	TierVLLM     Tier = 2
	TierBeeLlama Tier = 3
	TierOllama   Tier = 4
)

// Tier returns the failover tier of p. Unknown providers are an error
// (fail-closed — the tier of a provider outside the enumeration is never
// guessed).
func (p Provider) Tier() (Tier, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	switch p {
	case ProviderSGLang:
		return TierSGLang, nil
	case ProviderVLLM:
		return TierVLLM, nil
	case ProviderBeeLlama:
		return TierBeeLlama, nil
	case ProviderOllama:
		return TierOllama, nil
	}
	return 0, fmt.Errorf("airuntime: provider %q has no tier", p)
}

// DefaultFailoverOrder is the v19.0.6 release-locked 4-tier failover order
// (SGLang → vLLM → BeeLlama → Ollama). It is encoded ONCE here: not a
// runtime option, not a request parameter. Changing the order is a
// reviewed release act.
var DefaultFailoverOrder = []Provider{
	ProviderSGLang,
	ProviderVLLM,
	ProviderBeeLlama,
	ProviderOllama,
}
