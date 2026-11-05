package api

// CF-2 (v19.6.1): Universal AI Runtime REST surface tests — auth-gating,
// the 503-until-configured opt-in, the read-only views, the operator
// registration act, the probe round, and the failover chat contract
// (attribution + attempts) over REAL transports bound to httptest servers.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airt"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
)

// newAIRuntimeTestMux builds the handler + mux. configure attaches the
// runtime wired to the given live test servers (a real airt.Runtime over
// real transports — the surface is tested exactly as main.go wires it).
func newAIRuntimeTestMux(t *testing.T, configure func(h *handlerAI) *airt.Runtime) *http.ServeMux {
	t.Helper()
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.6.1")
	if configure != nil {
		rt := configure(&handlerAI{h: h})
		if rt != nil {
			if err := h.ConfigureAIRuntime(rt); err != nil {
				t.Fatal(err)
			}
		}
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

// handlerAI is a tiny pass-through so the configure closure can build the
// runtime before ConfigureAIRuntime is called.
type handlerAI struct {
	h *Handler
}

// aiStack wires a real Runtime over httptest servers: sglang + ollama
// live chat servers, vllm + beellama dead ports. Register qwen3-14b on
// all four tiers and observe sglang + ollama HEALTHY.
func aiStack(t *testing.T, sglangStatus int, ollamaStatus int) *airt.Runtime {
	t.Helper()
	sglang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(sglangStatus)
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			if sglangStatus != http.StatusOK {
				http.Error(w, "tier down", sglangStatus)
				return
			}
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"answer from sglang"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(sglang.Close)
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(ollamaStatus)
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			if ollamaStatus != http.StatusOK {
				http.Error(w, "tier down", ollamaStatus)
				return
			}
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"answer from ollama"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ollama.Close)
	dead := "http://127.0.0.1:1" // port 1 on loopback: refused in practice
	endpoints := map[airuntime.Provider]airt.Endpoint{
		airuntime.ProviderSGLang:   airt.Endpoint(sglang.URL),
		airuntime.ProviderVLLM:     airt.Endpoint(dead),
		airuntime.ProviderBeeLlama: airt.Endpoint(dead),
		airuntime.ProviderOllama:   airt.Endpoint(ollama.URL),
	}
	adapters, prober, err := airt.NewLocalStack(endpoints)
	if err != nil {
		t.Fatalf("local stack: %v", err)
	}
	registry, err := airuntime.NewModelRegistry(airuntime.DefaultMaxModels)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, p := range airuntime.AllProviders() {
		if err := registry.Register(p, "qwen3-14b"); err != nil {
			t.Fatalf("register %s: %v", p, err)
		}
	}
	table := airuntime.NewHealthTable()
	base := time.Now().UTC()
	for i, p := range airuntime.AllProviders() {
		state := airuntime.HealthUnavailable
		if p == airuntime.ProviderSGLang || p == airuntime.ProviderOllama {
			state = airuntime.HealthHealthy
		}
		// Firmly in the PAST: a probe round stamps time.Now(), and the
		// table's monotone discipline rejects an older probe by name —
		// fake observations must never sit in the future.
		if err := table.Observe(airuntime.Observation{Provider: p, State: state, At: base.Add(-time.Duration(i+1) * time.Minute)}); err != nil {
			t.Fatalf("observe %s: %v", p, err)
		}
	}
	rt, err := airt.NewRuntime(registry, table, prober, adapters)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	return rt
}

func aiRequest(t *testing.T, mux *http.ServeMux, method, path, body string) (int, map[string]interface{}) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]interface{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("non-JSON response (%d): %s", rec.Code, rec.Body.String())
		}
	}
	return rec.Code, out
}

func TestAIRuntimeRoutesDisabledByDefault(t *testing.T) {
	mux := newAIRuntimeTestMux(t, nil)
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/ai/health", ""},
		{http.MethodGet, "/api/ai/models", ""},
		{http.MethodPost, "/api/ai/models", `{"provider":"sglang","model":"qwen3-14b"}`},
		{http.MethodPost, "/api/ai/probe", ""},
		{http.MethodPost, "/api/ai/chat", `{"model":"qwen3-14b","messages":[{"role":"user","content":"hi"}]}`},
	} {
		code, body := aiRequest(t, mux, tc.method, tc.path, tc.body)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: expected 503 when unconfigured, got %d (%v)", tc.method, tc.path, code, body)
		}
	}
}

func TestAIRuntimeRoutesRequireAuth(t *testing.T) {
	mux := newAIRuntimeTestMux(t, func(h *handlerAI) *airt.Runtime {
		return aiStack(t, http.StatusOK, http.StatusOK)
	})
	for _, path := range []string{"/api/ai/health", "/api/ai/models", "/api/ai/probe", "/api/ai/chat"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401 without token, got %d", path, rec.Code)
		}
	}
}

func TestAIHealthView(t *testing.T) {
	mux := newAIRuntimeTestMux(t, func(h *handlerAI) *airt.Runtime {
		return aiStack(t, http.StatusOK, http.StatusOK)
	})
	code, body := aiRequest(t, mux, http.MethodGet, "/api/ai/health", "")
	if code != http.StatusOK {
		t.Fatalf("health view: expected 200, got %d (%v)", code, body)
	}
	providers, ok := body["providers"].([]interface{})
	if !ok || len(providers) != 4 {
		t.Fatalf("the health view covers the whole closed stack, got %v", body)
	}
	// Tier order + the observed states (sglang/ollama HEALTHY serve;
	// vllm/beellama UNAVAILABLE do not).
	wantState := map[string]string{"sglang": "HEALTHY", "vllm": "UNAVAILABLE", "beellama": "UNAVAILABLE", "ollama": "HEALTHY"}
	wantServes := map[string]bool{"sglang": true, "vllm": false, "beellama": false, "ollama": true}
	wantTier := map[string]float64{"sglang": 1, "vllm": 2, "beellama": 3, "ollama": 4}
	for i, entry := range providers {
		e := entry.(map[string]interface{})
		want := []string{"sglang", "vllm", "beellama", "ollama"}[i]
		if e["provider"] != want {
			t.Fatalf("health view must be tier-ordered at %d: want %s, got %v", i, want, e["provider"])
		}
		if e["state"] != wantState[want] {
			t.Fatalf("%s must show %s, got %v", want, wantState[want], e["state"])
		}
		if e["serves"] != wantServes[want] {
			t.Fatalf("%s serves=%v, want %v", want, e["serves"], wantServes[want])
		}
		if e["tier"] != wantTier[want] {
			t.Fatalf("%s tier=%v, want %v", want, e["tier"], wantTier[want])
		}
	}
}

func TestAIModelsListAndRegister(t *testing.T) {
	mux := newAIRuntimeTestMux(t, func(h *handlerAI) *airt.Runtime {
		return aiStack(t, http.StatusOK, http.StatusOK)
	})
	// Listing: the bootstrapped registry (qwen3-14b on all four tiers).
	code, body := aiRequest(t, mux, http.MethodGet, "/api/ai/models", "")
	if code != http.StatusOK {
		t.Fatalf("models list: expected 200, got %d (%v)", code, body)
	}
	if count := body["count"]; count != float64(4) {
		t.Fatalf("four providers serve the bootstrapped model, got %v", count)
	}
	// The operator act: register a second model on the CPU fallback.
	code, body = aiRequest(t, mux, http.MethodPost, "/api/ai/models", `{"provider":"ollama","model":"qwen3:4b"}`)
	if code != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d (%v)", code, body)
	}
	if body["provider"] != "ollama" || body["model"] != "qwen3:4b" || body["tier"] != float64(4) {
		t.Fatalf("the registration must be acknowledged w/ attribution, got %v", body)
	}
	// Duplicate: 409 (one registration is one declaration).
	code, _ = aiRequest(t, mux, http.MethodPost, "/api/ai/models", `{"provider":"ollama","model":"qwen3:4b"}`)
	if code != http.StatusConflict {
		t.Fatalf("duplicate registration: expected 409, got %d", code)
	}
	// Foreign provider: 400 — the closed enumeration holds through the API.
	code, _ = aiRequest(t, mux, http.MethodPost, "/api/ai/models", `{"provider":"openai","model":"gpt-4"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("a cloud provider name: expected 400, got %d", code)
	}
	// Malformed model id: 400.
	code, _ = aiRequest(t, mux, http.MethodPost, "/api/ai/models", `{"provider":"sglang","model":"Bad Model!"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("malformed model id: expected 400, got %d", code)
	}
	// Unknown fields: rejected (closed decode).
	code, _ = aiRequest(t, mux, http.MethodPost, "/api/ai/models", `{"provider":"sglang","model":"m","extra":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown fields: expected 400, got %d", code)
	}
}

func TestAIProbeRound(t *testing.T) {
	mux := newAIRuntimeTestMux(t, func(h *handlerAI) *airt.Runtime {
		// sglang stands but is unready (503 → DEGRADED); ollama healthy;
		// vllm + beellama dead (refused → UNAVAILABLE).
		return aiStack(t, http.StatusServiceUnavailable, http.StatusOK)
	})
	code, body := aiRequest(t, mux, http.MethodPost, "/api/ai/probe", "")
	if code != http.StatusOK {
		t.Fatalf("probe: expected 200, got %d (%v)", code, body)
	}
	observations, ok := body["observations"].([]interface{})
	if !ok || len(observations) != 4 {
		t.Fatalf("a probe round observes all four tiers, got %v", body)
	}
	want := map[string]string{"sglang": "DEGRADED", "vllm": "UNAVAILABLE", "beellama": "UNAVAILABLE", "ollama": "HEALTHY"}
	for _, o := range observations {
		obs := o.(map[string]interface{})
		p := obs["provider"].(string)
		if obs["state"] != want[p] {
			t.Fatalf("%s must observe %s, got %v", p, want[p], obs["state"])
		}
		if obs["observed"] == "" {
			t.Fatalf("%s observation must carry its time", p)
		}
	}
	// The round is RECORDED: the health view now shows DEGRADED for sglang.
	code, body = aiRequest(t, mux, http.MethodGet, "/api/ai/health", "")
	if code != http.StatusOK {
		t.Fatalf("health view after probe: %d", code)
	}
	providers := body["providers"].([]interface{})
	sglang := providers[0].(map[string]interface{})
	if sglang["state"] != "DEGRADED" || sglang["serves"] != true {
		t.Fatalf("the recorded observation must lead the view (DEGRADED serves honestly), got %v", sglang)
	}
}

func TestAIChatPrimaryAnswers(t *testing.T) {
	mux := newAIRuntimeTestMux(t, func(h *handlerAI) *airt.Runtime {
		return aiStack(t, http.StatusOK, http.StatusOK)
	})
	code, body := aiChatDo(t, mux, `{"model":"qwen3-14b","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello"}]}`)
	if code != http.StatusOK {
		t.Fatalf("chat: expected 200, got %d (%v)", code, body)
	}
	if body["provider"] != "sglang" {
		t.Fatalf("the healthy primary must answer w/ attribution, got %v", body["provider"])
	}
	if body["model"] != "qwen3-14b" {
		t.Fatalf("model must round-trip, got %v", body["model"])
	}
	msg := body["message"].(map[string]interface{})
	if msg["role"] != "assistant" || msg["content"] != "answer from sglang" {
		t.Fatalf("message must normalize, got %v", msg)
	}
	if body["finish_reason"] != "stop" {
		t.Fatalf("finish must round-trip, got %v", body["finish_reason"])
	}
	usage := body["usage"].(map[string]interface{})
	if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(2) {
		t.Fatalf("usage must round-trip, got %v", usage)
	}
	if body["timestamp"] == "" {
		t.Fatal("the response timestamp is required (attribution + time survive failover)")
	}
	attempts := body["attempts"].([]interface{})
	if len(attempts) != 0 {
		t.Fatalf("a successful primary leaves no attempts, got %v", attempts)
	}
}

func TestAIChatFailoverAttribution(t *testing.T) {
	// The primary is down (500 on chat): the plan walks to the tier-4 CPU
	// fallback — the caller SEES the failover.
	mux := newAIRuntimeTestMux(t, func(h *handlerAI) *airt.Runtime {
		return aiStack(t, http.StatusInternalServerError, http.StatusOK)
	})
	code, body := aiChatDo(t, mux, `{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}]}`)
	if code != http.StatusOK {
		t.Fatalf("chat must succeed through the fallback, got %d (%v)", code, body)
	}
	if body["provider"] != "ollama" {
		t.Fatalf("the fallback must answer w/ attribution, got %v", body["provider"])
	}
	attempts := body["attempts"].([]interface{})
	if len(attempts) != 1 {
		t.Fatalf("the failed primary must be recorded, got %v", attempts)
	}
	attempt := attempts[0].(map[string]interface{})
	if attempt["provider"] != "sglang" || attempt["tier"] != float64(1) || attempt["role"] != "primary" {
		t.Fatalf("the attempt must name the failed primary, got %v", attempt)
	}
	if attempt["error"] == "" {
		t.Fatal("fail-closed, not fail-blind: the attempt error is required")
	}
	msg := body["message"].(map[string]interface{})
	if msg["content"] != "answer from ollama" {
		t.Fatalf("the fallback's answer must ride the response, got %v", msg)
	}
}

func TestAIChatAllTiersFailed(t *testing.T) {
	mux := newAIRuntimeTestMux(t, func(h *handlerAI) *airt.Runtime {
		return aiStack(t, http.StatusInternalServerError, http.StatusInternalServerError)
	})
	code, body := aiChatDo(t, mux, `{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}]}`)
	if code != http.StatusBadGateway {
		t.Fatalf("all tiers failed: expected 502, got %d", code)
	}
	attempts, ok := body["attempts"].([]interface{})
	if !ok || len(attempts) != 2 {
		t.Fatalf("the attempts ride the error (sglang + ollama were serving), got %v", body)
	}
}

func TestAIChatUnknownModel(t *testing.T) {
	mux := newAIRuntimeTestMux(t, func(h *handlerAI) *airt.Runtime {
		return aiStack(t, http.StatusOK, http.StatusOK)
	})
	code, _ := aiChatDo(t, mux, `{"model":"never-registered","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusNotFound {
		t.Fatalf("unknown model: expected 404 (the router never guesses a model), got %d", code)
	}
}

func TestAIChatNoRoute(t *testing.T) {
	// Nothing observed: every tier UNKNOWN — no route exists, and the
	// skips ride the 503.
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.6.1")
	registry, err := airuntime.NewModelRegistry(8)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, p := range airuntime.AllProviders() {
		if err := registry.Register(p, "qwen3-14b"); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	adapters, prober, err := airt.NewLocalStack(airt.DefaultEndpoints())
	if err != nil {
		t.Fatalf("stack: %v", err)
	}
	rt, err := airt.NewRuntime(registry, airuntime.NewHealthTable(), prober, adapters)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	if err := h.ConfigureAIRuntime(rt); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	code, body := aiChatDo(t, mux, `{"model":"qwen3-14b","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no route: expected 503, got %d (%v)", code, body)
	}
	skips, ok := body["skips"].([]interface{})
	if !ok || len(skips) != 4 {
		t.Fatalf("the skips ride the no-route error (fail-closed, not fail-blind), got %v", body)
	}
	for _, s := range skips {
		skip := s.(map[string]interface{})
		if !strings.Contains(skip["rule"].(string), "rule/health-unknown") {
			t.Fatalf("each skip must cite its named rule, got %v", skip)
		}
	}
}

func TestAIChatRequestValidation(t *testing.T) {
	mux := newAIRuntimeTestMux(t, func(h *handlerAI) *airt.Runtime {
		return aiStack(t, http.StatusOK, http.StatusOK)
	})
	cases := []struct {
		name, body string
	}{
		{"no messages", `{"model":"qwen3-14b","messages":[]}`},
		{"bad role", `{"model":"qwen3-14b","messages":[{"role":"tool","content":"x"}]}`},
		{"empty content", `{"model":"qwen3-14b","messages":[{"role":"user","content":""}]}`},
		{"bad model id", `{"model":"Not A Model","messages":[{"role":"user","content":"x"}]}`},
		{"unknown field", `{"model":"qwen3-14b","messages":[{"role":"user","content":"x"}],"temperature":0.5}`},
		{"two objects", `{"model":"qwen3-14b","messages":[{"role":"user","content":"x"}]}{"more":1}`},
	}
	for _, tc := range cases {
		code, _ := aiChatDo(t, mux, tc.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", tc.name, code)
		}
	}
	// The contract bounds hold through the API: 33 messages rejected.
	var msgs strings.Builder
	msgs.WriteString("[")
	for i := 0; i < 33; i++ {
		if i > 0 {
			msgs.WriteString(",")
		}
		msgs.WriteString(fmt.Sprintf(`{"role":"user","content":"m%d"}`, i))
	}
	msgs.WriteString("]")
	code, _ := aiChatDo(t, mux, `{"model":"qwen3-14b","messages":`+msgs.String()+`}`)
	if code != http.StatusBadRequest {
		t.Fatalf("33 messages (bound 32): expected 400, got %d", code)
	}
}

func TestAIChatOversizedBody(t *testing.T) {
	mux := newAIRuntimeTestMux(t, func(h *handlerAI) *airt.Runtime {
		return aiStack(t, http.StatusOK, http.StatusOK)
	})
	// A body beyond MaxChatBodyBytes: rejected by the closed decode.
	big := strings.Repeat("x", MaxChatBodyBytes+1024)
	req := httptest.NewRequest(http.MethodPost, "/api/ai/chat", strings.NewReader(`{"model":"qwen3-14b","messages":[{"role":"user","content":"`+big+`"}]}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an over-bound body must be rejected, got %d", rec.Code)
	}
}

func TestAIChatMethodDiscipline(t *testing.T) {
	mux := newAIRuntimeTestMux(t, func(h *handlerAI) *airt.Runtime {
		return aiStack(t, http.StatusOK, http.StatusOK)
	})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/ai/chat"},
		{http.MethodGet, "/api/ai/probe"},
		{http.MethodPost, "/api/ai/health"},
		{http.MethodPut, "/api/ai/models"},
	} {
		code, _ := aiRequest(t, mux, tc.method, tc.path, "")
		if code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: expected 405, got %d", tc.method, tc.path, code)
		}
	}
}

func aiChatDo(t *testing.T, mux *http.ServeMux, body string) (int, map[string]interface{}) {
	t.Helper()
	return aiRequest(t, mux, http.MethodPost, "/api/ai/chat", body)
}
