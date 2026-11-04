package airuntime

import (
	"errors"
	"strings"
	"testing"
)

func TestModelIDValidation(t *testing.T) {
	// The real naming shapes: qwen3-14b, qwen2.5:3b, llama.cpp-style tags.
	for _, m := range []ModelID{
		"qwen3-14b",
		"qwen2.5:3b",
		"llama3.1-8b-q4_k_m",
		"m",
		"0.1:2_3-4",
		ModelID(strings.Repeat("a", 64)), // at the bound
	} {
		if err := m.Validate(); err != nil {
			t.Fatalf("model id %q must validate: %v", m, err)
		}
	}
	for _, m := range []ModelID{
		"",                               // empty
		ModelID(strings.Repeat("a", 65)), // over the bound
		"Qwen3",                          // uppercase
		"model/1",                        // slash
		"model x",                        // space
		"mo\x00del",                      // control rune
		"model+1",                        // forbidden symbol
	} {
		if err := m.Validate(); err == nil {
			t.Fatalf("model id %q must be rejected", m)
		}
	}
}

func TestModelRegistryConstruction(t *testing.T) {
	if _, err := NewModelRegistry(0); err == nil {
		t.Fatal("maxModels 0 must be rejected (fail-closed — an unbounded registry is never constructed)")
	}
	if _, err := NewModelRegistry(-1); err == nil {
		t.Fatal("negative maxModels must be rejected")
	}
	r, err := NewModelRegistry(DefaultMaxModels)
	if err != nil {
		t.Fatalf("DefaultMaxModels must construct: %v", err)
	}
	if r.Len() != 0 {
		t.Fatalf("fresh registry has %d models, want 0", r.Len())
	}
}

func TestRegisterAndProvidersFor(t *testing.T) {
	r, _ := NewModelRegistry(16)
	if err := r.Register(ProviderSGLang, "qwen3-14b"); err != nil {
		t.Fatalf("register sglang/qwen3-14b: %v", err)
	}
	if err := r.Register(ProviderVLLM, "qwen3-14b"); err != nil {
		t.Fatalf("register vllm/qwen3-14b (fan-out): %v", err)
	}
	if err := r.Register(ProviderOllama, "qwen2.5:3b"); err != nil {
		t.Fatalf("register ollama/qwen2.5:3b: %v", err)
	}
	// Tier-ordered, deterministic listing.
	got := r.ProvidersFor("qwen3-14b")
	if len(got) != 2 || got[0] != ProviderSGLang || got[1] != ProviderVLLM {
		t.Fatalf("ProvidersFor(qwen3-14b) = %v, want [sglang vllm] (tier order)", got)
	}
	if got := r.ProvidersFor("qwen2.5:3b"); len(got) != 1 || got[0] != ProviderOllama {
		t.Fatalf("ProvidersFor(qwen2.5:3b) = %v, want [ollama]", got)
	}
	if got := r.ProvidersFor("never-registered"); len(got) != 0 {
		t.Fatalf("unknown model must return no providers (the router turns that into ErrUnknownModel — never a guess), got %v", got)
	}
	if r.Len() != 2 {
		t.Fatalf("registry counts %d distinct models, want 2 (fan-out does not count)", r.Len())
	}
}

func TestRegisterValidation(t *testing.T) {
	r, _ := NewModelRegistry(16)
	if err := r.Register("openai", "gpt-4"); err == nil {
		t.Fatal("registering a cloud provider must be rejected (closed local-stack enumeration)")
	}
	if err := r.Register(ProviderSGLang, "Bad/Model"); err == nil {
		t.Fatal("registering a malformed model id must be rejected")
	}
	var nilRegistry *ModelRegistry
	if err := nilRegistry.Register(ProviderSGLang, "m"); err == nil {
		t.Fatal("nil registry must fail closed")
	}
}

func TestDuplicatePairRejected(t *testing.T) {
	r, _ := NewModelRegistry(16)
	if err := r.Register(ProviderSGLang, "qwen3-14b"); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	err := r.Register(ProviderSGLang, "qwen3-14b")
	if err == nil {
		t.Fatal("duplicate pair must be rejected — one registration is one declaration")
	}
	if !errors.Is(err, ErrDuplicateRegistration) {
		t.Fatalf("duplicate must wrap ErrDuplicateRegistration, got %v", err)
	}
}

func TestRegistryBoundDistinctModelsVsFanOut(t *testing.T) {
	// The bound counts DISTINCT models; provider fan-out on an existing
	// model does not consume a slot.
	r, _ := NewModelRegistry(2)
	for _, p := range []Provider{ProviderSGLang, ProviderVLLM, ProviderBeeLlama, ProviderOllama} {
		if err := r.Register(p, "shared-model"); err != nil {
			t.Fatalf("fan-out of shared-model on %s must be allowed (routing info, not capacity): %v", p, err)
		}
	}
	if r.Len() != 1 {
		t.Fatalf("fan-out consumed distinct-model slots: Len=%d, want 1", r.Len())
	}
	if err := r.Register(ProviderOllama, "second-model"); err != nil {
		t.Fatalf("second distinct model must fit the bound 2: %v", err)
	}
	err := r.Register(ProviderOllama, "third-model")
	if err == nil {
		t.Fatal("third distinct model must be rejected (bound 2)")
	}
	if !errors.Is(err, ErrModelLimit) {
		t.Fatalf("bound rejection must wrap ErrModelLimit, got %v", err)
	}
}

func TestListDeterministic(t *testing.T) {
	r, _ := NewModelRegistry(16)
	for _, entry := range []struct {
		provider Provider
		model    ModelID
	}{
		{ProviderOllama, "qwen2.5:3b"},
		{ProviderSGLang, "zeta-9"},
		{ProviderVLLM, "qwen3-14b"},
		{ProviderSGLang, "alpha-1"},
		{ProviderBeeLlama, "gguf-q4"},
		{ProviderSGLang, "qwen3-14b"},
	} {
		if err := r.Register(entry.provider, entry.model); err != nil {
			t.Fatalf("register %s/%s: %v", entry.provider, entry.model, err)
		}
	}
	first := r.List()
	for i := 0; i < 20; i++ {
		again := r.List()
		if len(again) != len(first) {
			t.Fatalf("List() length drifted at repetition %d: %d vs %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j].Provider != first[j].Provider {
				t.Fatalf("List() provider order drifted at repetition %d", i)
			}
			if len(again[j].Models) != len(first[j].Models) {
				t.Fatalf("List() models drifted for %s at repetition %d", first[j].Provider, i)
			}
			for k := range first[j].Models {
				if again[j].Models[k] != first[j].Models[k] {
					t.Fatalf("List() model order drifted for %s at repetition %d", first[j].Provider, i)
				}
			}
		}
	}
	// Tier order in the listing + ascending models within a provider.
	if len(first) != 4 {
		t.Fatalf("listing has %d providers, want all 4 serving", len(first))
	}
	wantOrder := []Provider{ProviderSGLang, ProviderVLLM, ProviderBeeLlama, ProviderOllama}
	for i := range wantOrder {
		if first[i].Provider != wantOrder[i] {
			t.Fatalf("listing provider %d = %s, want %s (tier order)", i, first[i].Provider, wantOrder[i])
		}
	}
	sg := first[0].Models
	if len(sg) != 3 || sg[0] != "alpha-1" || sg[1] != "qwen3-14b" || sg[2] != "zeta-9" {
		t.Fatalf("sglang models must be ascending: %v", sg)
	}
	// Reading is a copy: mutating the listing must not touch the registry.
	first[0].Models[0] = "mutated"
	if r.ProvidersFor("alpha-1")[0] != ProviderSGLang || r.ServesModel(ProviderSGLang, "alpha-1") != true {
		t.Fatal("mutating the listing leaked into the registry")
	}
	_ = first[0].Models[0] == "mutated" // silence the unused-mutation linters
}

func TestServesModel(t *testing.T) {
	r, _ := NewModelRegistry(16)
	if err := r.Register(ProviderSGLang, "m1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if !r.ServesModel(ProviderSGLang, "m1") {
		t.Fatal("sglang serves m1")
	}
	if r.ServesModel(ProviderVLLM, "m1") {
		t.Fatal("vllm does not serve m1")
	}
	if r.ServesModel(ProviderSGLang, "m2") {
		t.Fatal("sglang does not serve m2")
	}
}
