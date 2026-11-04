package airuntime

import (
	"strings"
	"testing"
)

func TestProviderValidationValid(t *testing.T) {
	for _, p := range []Provider{ProviderSGLang, ProviderVLLM, ProviderBeeLlama, ProviderOllama} {
		if err := p.Validate(); err != nil {
			t.Fatalf("provider %s must validate: %v", p, err)
		}
		if !p.IsValid() {
			t.Fatalf("provider %s must be part of the closed enumeration", p)
		}
	}
}

func TestProviderValidationRejectsCloudNames(t *testing.T) {
	// The runtime normalizes the LOCAL sovereign stack: cloud-LLM names are
	// not providers here. This is the deliberate rejection, tested — it is
	// the proof, not an accident.
	for _, p := range []Provider{
		"openai",
		"anthropic",
		"azure",
		"openai.com",
	} {
		if err := p.Validate(); err == nil {
			t.Fatalf("provider %q must be rejected (closed local-stack enumeration)", p)
		}
	}
}

func TestProviderValidationRejectsMalformedShapes(t *testing.T) {
	for _, p := range []Provider{
		"",               // empty
		" sglang",        // leading whitespace
		"sglang\n",       // trailing newline
		"sg\x01lang",     // control character
		"sglang:30000",   // port — providers are tiers, not endpoints
		"sglang/v1",      // path
		"sglang\\api",    // backslash path
		"SGLang",         // not in the lowercase enumeration
		"openai-gateway", // cloud-prefixed compound
	} {
		if err := p.Validate(); err == nil {
			t.Fatalf("provider %q must be rejected by shape validation", p)
		}
	}
}

func TestProviderTier(t *testing.T) {
	cases := []struct {
		provider Provider
		tier     Tier
	}{
		{ProviderSGLang, 1},
		{ProviderVLLM, 2},
		{ProviderBeeLlama, 3},
		{ProviderOllama, 4},
	}
	for _, c := range cases {
		tier, err := c.provider.Tier()
		if err != nil {
			t.Fatalf("provider %s: %v", c.provider, err)
		}
		if tier != c.tier {
			t.Fatalf("provider %s: got tier %d, want %d", c.provider, tier, c.tier)
		}
	}
	if _, err := Provider("openai").Tier(); err == nil {
		t.Fatal("tier of an unenumerated provider must be an error (fail-closed — never guessed)")
	}
}

func TestDefaultFailoverOrder(t *testing.T) {
	// The v19.0.6 release-locked order: SGLang → vLLM → BeeLlama → Ollama.
	want := []Provider{ProviderSGLang, ProviderVLLM, ProviderBeeLlama, ProviderOllama}
	if len(DefaultFailoverOrder) != len(want) {
		t.Fatalf("DefaultFailoverOrder has %d entries, want %d", len(DefaultFailoverOrder), len(want))
	}
	for i := range want {
		if DefaultFailoverOrder[i] != want[i] {
			t.Fatalf("DefaultFailoverOrder[%d] = %s, want %s (the v19.0.6-locked order)", i, DefaultFailoverOrder[i], want[i])
		}
	}
}

func TestAllProvidersCopyIsolation(t *testing.T) {
	all := AllProviders()
	if len(all) != 4 {
		t.Fatalf("AllProviders returns %d providers, want the closed 4", len(all))
	}
	all[0] = Provider("mutated")
	again := AllProviders()
	if again[0] != ProviderSGLang {
		t.Fatal("AllProviders must return a copy — the enumeration cannot be extended through the returned slice")
	}
	for _, p := range again {
		if err := p.Validate(); err != nil {
			t.Fatalf("enumerated provider %s must validate: %v", p, err)
		}
	}
	if strings.Contains(string(again[3]), "mutated") {
		t.Fatal("mutation leaked into the enumeration")
	}
}
