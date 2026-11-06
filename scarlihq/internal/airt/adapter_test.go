package airt

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

func chatRequest() airuntime.Request {
	return airuntime.Request{
		Model:    "qwen3-14b",
		Messages: []airuntime.Message{{Role: airuntime.RoleUser, Content: "hello"}},
	}
}

func TestLocalAdapterChatHappyPath(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("chat must POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("chat must target /v1/chat/completions, got %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type must be application/json, got %s", ct)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("request body must be JSON: %v", err)
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	defer server.Close()

	adapter, err := NewLocalAdapter(airuntime.ProviderSGLang, Endpoint(server.URL), DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	resp, err := adapter.Chat(context.Background(), chatRequest())
	if err != nil {
		t.Fatalf("chat must succeed against a healthy tier: %v", err)
	}
	if resp.Provider != airuntime.ProviderSGLang {
		t.Fatalf("attribution must name the tier: got %s", resp.Provider)
	}
	if resp.Message.Content != "answer" {
		t.Fatalf("content must normalize, got %q", resp.Message.Content)
	}
	if resp.Usage.InputTokens != 5 || resp.Usage.OutputTokens != 2 {
		t.Fatalf("usage must normalize, got %+v", resp.Usage)
	}
	if resp.Timestamp.IsZero() {
		t.Fatal("the response timestamp is required — attribution + time survive transport")
	}
	// The wire request shape: model, messages, stream=false.
	if gotBody["model"] != "qwen3-14b" {
		t.Errorf("wire model must map 1:1, got %v", gotBody["model"])
	}
	if gotBody["stream"] != false {
		t.Errorf("wire stream must be false, got %v", gotBody["stream"])
	}
	messages, ok := gotBody["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("wire messages must map 1:1, got %v", gotBody["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "hello" {
		t.Errorf("wire message must map 1:1, got %v", first)
	}
}

func TestLocalAdapterChatRejectsInvalidRequestBeforeWire(t *testing.T) {
	reached := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	adapter, err := NewLocalAdapter(airuntime.ProviderOllama, Endpoint(server.URL), DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	// Empty message content: shape validation fails BEFORE any HTTP call.
	bad := airuntime.Request{Model: "qwen3-14b", Messages: []airuntime.Message{{Role: airuntime.RoleUser, Content: ""}}}
	if _, err := adapter.Chat(context.Background(), bad); err == nil {
		t.Fatal("invalid request must be rejected (defense in depth)")
	}
	if reached {
		t.Fatal("an invalid request must never reach the wire")
	}
}

func TestLocalAdapterChatNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model loading", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	adapter, err := NewLocalAdapter(airuntime.ProviderVLLM, Endpoint(server.URL), DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	_, err = adapter.Chat(context.Background(), chatRequest())
	if err == nil {
		t.Fatal("a non-2xx tier must be an adapter error the plan skips past")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("the error must carry the status, got %v", err)
	}
}

func TestLocalAdapterChatMalformedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `not json at all`)
	}))
	defer server.Close()
	adapter, err := NewLocalAdapter(airuntime.ProviderBeeLlama, Endpoint(server.URL), DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if _, err := adapter.Chat(context.Background(), chatRequest()); err == nil {
		t.Fatal("a malformed body must be an adapter error")
	}
}

func TestLocalAdapterChatOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"padding":"` + strings.Repeat("x", MaxResponseBytes+1024) + `"}`))
	}))
	defer server.Close()
	adapter, err := NewLocalAdapter(airuntime.ProviderSGLang, Endpoint(server.URL), DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	_, err = adapter.Chat(context.Background(), chatRequest())
	if err == nil {
		t.Fatal("an over-bound response body must be an adapter error (a runaway response must not eat memory)")
	}
	if !strings.Contains(err.Error(), "transport bound") {
		t.Fatalf("the error must name the transport bound, got %v", err)
	}
}

func TestLocalAdapterChatTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	adapter, err := NewLocalAdapter(airuntime.ProviderOllama, Endpoint(server.URL), 50*time.Millisecond)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	_, err = adapter.Chat(context.Background(), chatRequest())
	if err == nil {
		t.Fatal("a tier slower than the adapter timeout must be an adapter error")
	}
}

func TestLocalAdapterContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	adapter, err := NewLocalAdapter(airuntime.ProviderSGLang, Endpoint(server.URL), DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := adapter.Chat(ctx, chatRequest()); err == nil {
		t.Fatal("a cancelled context must abort the attempt")
	}
}

func TestLocalAdapterConstruction(t *testing.T) {
	// Invalid provider: closed enumeration holds at construction.
	if _, err := NewLocalAdapter("openai", "http://127.0.0.1:1", time.Second); err == nil {
		t.Fatal("a cloud provider name must be rejected at construction (the runtime is not a cloud-LLM gateway)")
	}
	// Invalid endpoint: the closed endpoint shape holds at construction.
	if _, err := NewLocalAdapter(airuntime.ProviderSGLang, "https://api.openai.com", time.Second); err == nil {
		t.Fatal("a cloud endpoint must be rejected at construction")
	}
	// Non-positive timeout: bounded transport only.
	if _, err := NewLocalAdapter(airuntime.ProviderSGLang, "http://127.0.0.1:30000", 0); err == nil {
		t.Fatal("a non-positive timeout must be rejected (bounded transport only)")
	}
}

func TestNewLocalStack(t *testing.T) {
	// The full stack: all four tiers, one adapter each, plus the prober.
	adapters, prober, err := NewLocalStack(DefaultEndpoints(), DefaultAdapterTimeout)
	if err != nil {
		t.Fatalf("local stack: %v", err)
	}
	if len(adapters) != 4 {
		t.Fatalf("the stack must carry exactly four adapters (the closed set), got %d", len(adapters))
	}
	for _, p := range airuntime.AllProviders() {
		if adapters[p] == nil {
			t.Fatalf("tier %s must have an adapter — health gates routing, construction must not", p)
		}
		if adapters[p].Provider() != p {
			t.Fatalf("adapter in slot %s must claim %s", p, p)
		}
	}
	if prober == nil {
		t.Fatal("the stack must carry the prober (freshness is this package's job)")
	}
	// An incomplete endpoint set: fail-closed construction, named tier.
	partial := DefaultEndpoints()
	delete(partial, airuntime.ProviderBeeLlama)
	if _, _, err := NewLocalStack(partial, DefaultAdapterTimeout); err == nil || !strings.Contains(err.Error(), "beellama") {
		t.Fatalf("an incomplete stack must fail closed naming the missing tier, got %v", err)
	}
}
