// Backend capability registry tests (v19.7.2, Compute Fabric Phase 3,
// roadmap §7.2/§7.3): the closed enumeration, the validated registry
// (1:1 with the enumeration, no empty capability lists), the
// Provider↔BackendID alignment (infer_ssd's honest named error), the
// tiers, and the immutability of the returned rows.
package airuntime

import (
	"strings"
	"testing"
)

// TestBackendRegistryClosedAndValidated: every enumeration member has
// exactly one registry row; the registry validates (roadmap §7.2's
// "validated at startup").
func TestBackendRegistryClosedAndValidated(t *testing.T) {
	if err := ValidateBackendRegistry(); err != nil {
		t.Fatalf("the closed registry must validate: %v", err)
	}
	all := AllBackends()
	if len(all) != 5 {
		t.Fatalf("the closed capability enumeration must carry 5 members (sglang/vllm/beellama/ollama/infer_ssd — roadmap §7.2's own const block), got %d", len(all))
	}
	want := map[BackendID]bool{
		BackendSGLang: true, BackendVLLM: true, BackendBeeLlama: true,
		BackendOllama: true, BackendInferSSD: true,
	}
	for _, b := range all {
		if !want[b] {
			t.Fatalf("unexpected backend %q in the closed enumeration", b)
		}
		if !b.IsValid() {
			t.Fatalf("%q must be valid", b)
		}
		if err := b.Validate(); err != nil {
			t.Fatalf("%q must validate: %v", b, err)
		}
	}
}

// TestBackendCapabilitiesForEveryMember: the registry rows carry the
// §7.2 field set honestly — sglang/vllm GPU-class with formats and
// architectures, the CPU tiers, and the declared-future infer_ssd row.
func TestBackendCapabilitiesForEveryMember(t *testing.T) {
	for _, b := range AllBackends() {
		row, err := BackendCapabilitiesFor(b)
		if err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if row.BackendID != b {
			t.Fatalf("row id mismatch: %s vs %s", row.BackendID, b)
		}
		if len(row.ModelFormats) == 0 || len(row.Architectures) == 0 {
			t.Fatalf("%s: capability rows must declare formats + architectures", b)
		}
		if row.AdapterNote == "" {
			t.Fatalf("%s: the adapter note must state the honest v19.7.2 adapter status", b)
		}
	}
	sglang, _ := BackendCapabilitiesFor(BackendSGLang)
	if !sglang.RequiresGPU || !sglang.SupportsMoE || sglang.Experimental {
		t.Fatalf("sglang row wrong: %+v", sglang)
	}
	if !containsStr(sglang.ModelFormats, "safetensors") || !containsStr(sglang.Architectures, "dense") {
		t.Fatalf("sglang must execute safetensors dense models: %+v", sglang)
	}
	vllm, _ := BackendCapabilitiesFor(BackendVLLM)
	if !vllm.Experimental {
		t.Fatalf("vllm is the deployed stack's experimental tier — the row must say so")
	}
	beellama, _ := BackendCapabilitiesFor(BackendBeeLlama)
	if beellama.RequiresGPU || !containsStr(beellama.ModelFormats, "gguf") {
		t.Fatalf("beellama is the CPU gguf tier: %+v", beellama)
	}
	ollama, _ := BackendCapabilitiesFor(BackendOllama)
	if ollama.RequiresGPU {
		t.Fatalf("ollama is a CPU tier: %+v", ollama)
	}
	infer, _ := BackendCapabilitiesFor(BackendInferSSD)
	if !infer.Experimental || !infer.SupportsSSDExperts || !infer.RequiresGPU {
		t.Fatalf("infer_ssd is the declared-future SSD-expert backend: %+v", infer)
	}
	if !strings.Contains(infer.AdapterNote, "v19.8.0") {
		t.Fatalf("infer_ssd's adapter note must name v19.8.0 (the declared-future marker): %q", infer.AdapterNote)
	}
}

// TestBackendCapabilitiesUnknownRefused: an unregistered slug is a
// named error — capabilities are never guessed or synthesized.
func TestBackendCapabilitiesUnknownRefused(t *testing.T) {
	for _, bad := range []BackendID{"", "cloudgpt", "sglang:30000", "SG lang", "sg\x00lang"} {
		if _, err := BackendCapabilitiesFor(bad); err == nil {
			t.Fatalf("backend %q must be refused", bad)
		}
	}
}

// TestBackendRowsAreCopies: the returned row is a COPY — the caller
// cannot mutate the closed registry through it.
func TestBackendRowsAreCopies(t *testing.T) {
	row, err := BackendCapabilitiesFor(BackendSGLang)
	if err != nil {
		t.Fatal(err)
	}
	row.ModelFormats[0] = "mutated"
	row.Architectures[0] = "mutated"
	row.Features[0] = "mutated"
	fresh, _ := BackendCapabilitiesFor(BackendSGLang)
	if fresh.ModelFormats[0] == "mutated" || fresh.Architectures[0] == "mutated" || fresh.Features[0] == "mutated" {
		t.Fatalf("the registry must be immutable through the returned rows")
	}
}

// TestProviderBackendAlignment: the four real runtimes map 1:1;
// infer_ssd is the honest named error (no v19.7.2 adapter — v19.8.0).
func TestProviderBackendAlignment(t *testing.T) {
	pairs := []struct {
		p Provider
		b BackendID
	}{
		{ProviderSGLang, BackendSGLang},
		{ProviderVLLM, BackendVLLM},
		{ProviderBeeLlama, BackendBeeLlama},
		{ProviderOllama, BackendOllama},
	}
	for _, pair := range pairs {
		b, err := BackendForProvider(pair.p)
		if err != nil || b != pair.b {
			t.Fatalf("provider %s must map to %s (got %s, %v)", pair.p, pair.b, b, err)
		}
		p, err := ProviderForBackend(pair.b)
		if err != nil || p != pair.p {
			t.Fatalf("backend %s must map to %s (got %s, %v)", pair.b, pair.p, p, err)
		}
	}
	_, err := ProviderForBackend(BackendInferSSD)
	if err == nil || !strings.Contains(err.Error(), "v19.8.0") {
		t.Fatalf("infer_ssd must be the honest named error naming v19.8.0, got %v", err)
	}
}

// TestBackendTiers: the capability-tier priorities mirror the failover
// order; infer_ssd ranks last (never preferred while adapter-less).
func TestBackendTiers(t *testing.T) {
	want := map[BackendID]int{
		BackendSGLang: 1, BackendVLLM: 2, BackendBeeLlama: 3, BackendOllama: 4, BackendInferSSD: 99,
	}
	for b, tier := range want {
		got, err := BackendTier(b)
		if err != nil || got != tier {
			t.Fatalf("backend %s tier=%d (want %d, err=%v)", b, got, tier, err)
		}
	}
	if _, err := BackendTier("nope"); err == nil {
		t.Fatalf("an unknown backend must have no tier (fail-closed)")
	}
}

// TestRegistryViewDeterministic: the read-only registry view is sorted
// and complete.
func TestRegistryViewDeterministic(t *testing.T) {
	v1 := RegistryView()
	v2 := RegistryView()
	if len(v1) != 5 {
		t.Fatalf("the view must carry all 5 rows, got %d", len(v1))
	}
	for i := 1; i < len(v1); i++ {
		if v1[i-1].BackendID >= v1[i].BackendID {
			t.Fatalf("the view must be sorted by backend ID")
		}
	}
	for i := range v1 {
		if v1[i].BackendID != v2[i].BackendID {
			t.Fatalf("the view must be deterministic")
		}
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
