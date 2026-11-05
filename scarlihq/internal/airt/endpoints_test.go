package airt

import (
	"strings"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

func TestDefaultEndpointsMatchAPIMap(t *testing.T) {
	endpoints := DefaultEndpoints()
	if len(endpoints) != 4 {
		t.Fatalf("DefaultEndpoints must cover exactly the closed 4-tier stack, got %d entries", len(endpoints))
	}
	// The SCARLIX_API_MAP.md documented addresses — release-locked like
	// DefaultFailoverOrder.
	want := map[airuntime.Provider]string{
		airuntime.ProviderSGLang:   "http://127.0.0.1:30000",
		airuntime.ProviderVLLM:     "http://127.0.0.1:8089",
		airuntime.ProviderBeeLlama: "http://127.0.0.1:11438",
		airuntime.ProviderOllama:   "http://127.0.0.1:11435",
	}
	for p, w := range want {
		if got := string(endpoints[p]); got != w {
			t.Errorf("default endpoint for %s must be %s (the API-map documented address), got %s", p, w, got)
		}
	}
	// Every default must validate against the closed shape.
	for p, e := range endpoints {
		if err := e.Validate(); err != nil {
			t.Errorf("default endpoint for %s must validate: %v", p, err)
		}
	}
}

func TestEndpointValidateAccepts(t *testing.T) {
	ok := []string{
		"http://127.0.0.1:30000",
		"http://127.0.0.1:8089",
		"http://localhost:11435",
		"http://127.0.0.1:11438",
		"http://[::1]:8080",
		"http://sglang:30000",       // docker service name (scarlix-net)
		"http://ollama-agent:11434", // the real container name
		"http://beellama:8080",
		"http://vllm:8000",
		"http://a1-b2:1234", // service-name charset, digits + hyphen
	}
	for _, s := range ok {
		if err := Endpoint(s).Validate(); err != nil {
			t.Errorf("endpoint %q must validate (loopback or single-label service name, http, explicit port), got: %v", s, err)
		}
	}
}

func TestEndpointValidateRejects(t *testing.T) {
	bad := map[string]string{
		"":                              "empty",
		"http://127.0.0.1":              "implicit port",
		"https://127.0.0.1:30000":       "https (the local stack is plain HTTP)",
		"http://user:pass@127.0.0.1:1":  "userinfo",
		"http://127.0.0.1:30000/v1":     "path",
		"http://127.0.0.1:30000/?x=1":   "query",
		"http://127.0.0.1:30000#f":      "fragment",
		"http://api.openai.com/v1":      "dotted public host — the cloud-gateway shape",
		"http://sglang.internal:30000":  "dotted internal name",
		"http://SGlang:30000":           "uppercase service name",
		"http://-sglang:30000":          "leading hyphen",
		"http://sglang-:30000":          "trailing hyphen",
		"http://127.0.0.1:0":            "port 0",
		"http://127.0.0.1:99999":        "port out of range",
		"http://127.0.0.1:abc":          "non-numeric port",
		"sglang:30000":                  "no scheme",
		"http://:30000":                 "no host",
		"not a url at all":              "garbage",
		"http://api.anthropic.com:443":  "another cloud shape",
		"http://azure-east.openai.com/": "a longer cloud shape",
	}
	for s, why := range bad {
		if err := Endpoint(s).Validate(); err == nil {
			t.Errorf("endpoint %q must be rejected (%s)", s, why)
		}
	}
	// The cloud-gateway guard must NAME the rule (fail-closed with the
	// reason, not a silent reject).
	err := Endpoint("http://api.openai.com/v1").Validate()
	if err == nil || !strings.Contains(err.Error(), "not a cloud-LLM gateway") {
		t.Fatalf("the dotted-host rejection must name the cloud-gateway rule, got: %v", err)
	}
	// The https rejection must name the plain-HTTP rule.
	err = Endpoint("https://127.0.0.1:30000").Validate()
	if err == nil || !strings.Contains(err.Error(), "plain HTTP") {
		t.Fatalf("the https rejection must name the plain-HTTP rule, got: %v", err)
	}
}

func TestEndpointURLComposition(t *testing.T) {
	e := Endpoint("http://127.0.0.1:30000")
	if got := e.URL("/v1/chat/completions"); got != "http://127.0.0.1:30000/v1/chat/completions" {
		t.Fatalf("URL join failed: %s", got)
	}
	// A trailing slash on the base must not double the separator.
	e = Endpoint("http://sglang:30000/")
	if got := e.URL("/health"); got != "http://sglang:30000/health" {
		t.Fatalf("URL join with trailing slash failed: %s", got)
	}
}

func TestEndpointsFromEnv(t *testing.T) {
	// No overrides: the defaults stand.
	endpoints, err := EndpointsFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("no overrides must yield the defaults, got %v", err)
	}
	if got := string(endpoints[airuntime.ProviderSGLang]); got != "http://127.0.0.1:30000" {
		t.Fatalf("sglang default must stand, got %s", got)
	}
	// One override: applied, the rest default.
	endpoints, err = EndpointsFromEnv(func(k string) string {
		if k == "SCARLIHQ_AIRUNTIME_OLLAMA_URL" {
			return "http://ollama-agent:11434"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("valid override must apply, got %v", err)
	}
	if got := string(endpoints[airuntime.ProviderOllama]); got != "http://ollama-agent:11434" {
		t.Fatalf("ollama override must apply, got %s", got)
	}
	if got := string(endpoints[airuntime.ProviderVLLM]); got != "http://127.0.0.1:8089" {
		t.Fatalf("vllm default must stand, got %s", got)
	}
	// Whitespace around the value is trimmed.
	endpoints, err = EndpointsFromEnv(func(k string) string {
		if k == "SCARLIHQ_AIRUNTIME_BEELLAMA_URL" {
			return "  http://beellama:8080  "
		}
		return ""
	})
	if err != nil {
		t.Fatalf("trimmed override must apply, got %v", err)
	}
	if got := string(endpoints[airuntime.ProviderBeeLlama]); got != "http://beellama:8080" {
		t.Fatalf("beellama override must apply after trim, got %s", got)
	}
	// An INVALID override is an error naming the variable — never a
	// silent fall-back to the default (a typo must fail loudly).
	_, err = EndpointsFromEnv(func(k string) string {
		if k == "SCARLIHQ_AIRUNTIME_SGLANG_URL" {
			return "http://api.openai.com"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "SCARLIHQ_AIRUNTIME_SGLANG_URL") {
		t.Fatalf("invalid override must fail loudly naming the variable, got %v", err)
	}
}

func TestValidateEndpointSet(t *testing.T) {
	if err := validateEndpointSet(nil); err == nil {
		t.Fatal("nil endpoint set must be rejected")
	}
	// Missing a tier: named in the error.
	partial := DefaultEndpoints()
	delete(partial, airuntime.ProviderVLLM)
	err := validateEndpointSet(partial)
	if err == nil || !strings.Contains(err.Error(), "vllm") {
		t.Fatalf("missing tier must be named, got %v", err)
	}
	// A foreign provider entry: rejected (the closed set only).
	foreign := DefaultEndpoints()
	foreign["openai"] = "http://127.0.0.1:1"
	err = validateEndpointSet(foreign)
	if err == nil || !strings.Contains(err.Error(), "foreign provider") {
		t.Fatalf("foreign provider must be rejected by name, got %v", err)
	}
	// The full default set validates.
	if err := validateEndpointSet(DefaultEndpoints()); err != nil {
		t.Fatalf("the default set must validate: %v", err)
	}
}
