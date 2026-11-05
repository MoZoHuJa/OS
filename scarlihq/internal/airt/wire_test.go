package airt

import (
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

func wireBody(t *testing.T, raw string) []byte {
	t.Helper()
	return []byte(raw)
}

func TestDecodeWireHappyPath(t *testing.T) {
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	body := wireBody(t, `{"choices":[{"message":{"role":"assistant","content":"ahoj"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	resp, err := decodeWireResponse(airuntime.ProviderSGLang, "qwen3-14b", body, at)
	if err != nil {
		t.Fatalf("happy path must decode: %v", err)
	}
	if resp.Provider != airuntime.ProviderSGLang {
		t.Fatalf("attribution must survive the wire: got %s", resp.Provider)
	}
	if resp.Model != "qwen3-14b" {
		t.Fatalf("model must round-trip: got %s", resp.Model)
	}
	if resp.Message.Role != airuntime.RoleAssistant || resp.Message.Content != "ahoj" {
		t.Fatalf("message must normalize to an assistant turn, got %+v", resp.Message)
	}
	if resp.Finish != airuntime.FinishStop {
		t.Fatalf("finish must map stop→stop, got %s", resp.Finish)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("usage must map prompt/completion → input/output, got %+v", resp.Usage)
	}
	if !resp.Timestamp.Equal(at) {
		t.Fatalf("timestamp must be the given UTC instant, got %s", resp.Timestamp)
	}
}

func TestDecodeWireFinishReasons(t *testing.T) {
	at := time.Now().UTC()
	// "length" is the other legitimate closed value.
	body := wireBody(t, `{"choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"length"}]}`)
	if _, err := decodeWireResponse(airuntime.ProviderVLLM, "qwen3-14b", body, at); err != nil {
		t.Fatalf("finish=length must decode (the closed enumeration), got %v", err)
	}
	// Unknown finish reasons are transport failures, never coerced.
	for _, finish := range []string{"content_filter", "tool_calls", "", "STOP", "null", "error"} {
		body := wireBody(t, `{"choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"`+finish+`"}]}`)
		_, err := decodeWireResponse(airuntime.ProviderSGLang, "qwen3-14b", body, at)
		if err == nil {
			t.Fatalf("finish %q must be an adapter ERROR (unknown finish reasons are transport failures, never coerced)", finish)
		}
		if !strings.Contains(err.Error(), "closed enumeration") {
			t.Fatalf("the rejection must name the closed enumeration, got %v", err)
		}
	}
}

func TestDecodeWireChoiceDiscipline(t *testing.T) {
	at := time.Now().UTC()
	// Zero choices: malformed transport answer.
	if _, err := decodeWireResponse(airuntime.ProviderOllama, "qwen3-14b", wireBody(t, `{"choices":[]}`), at); err == nil {
		t.Fatal("zero choices must be rejected (exactly one required)")
	}
	// Several choices: non-streamed transport carries exactly one.
	if _, err := decodeWireResponse(airuntime.ProviderOllama, "qwen3-14b", wireBody(t, `{"choices":[{"message":{"role":"assistant","content":"a"},"finish_reason":"stop"},{"message":{"role":"assistant","content":"b"},"finish_reason":"stop"}]}`), at); err == nil {
		t.Fatal("two choices must be rejected (non-streamed transport)")
	}
	// A non-assistant role in the response: a shape violation, never coerced.
	body := wireBody(t, `{"choices":[{"message":{"role":"user","content":"x"},"finish_reason":"stop"}]}`)
	if _, err := decodeWireResponse(airuntime.ProviderBeeLlama, "qwen3-14b", body, at); err == nil {
		t.Fatal("a response role other than assistant must be rejected")
	}
}

func TestDecodeWireContentBounds(t *testing.T) {
	at := time.Now().UTC()
	// Empty content: Message validation fails closed.
	if _, err := decodeWireResponse(airuntime.ProviderSGLang, "qwen3-14b", wireBody(t, `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`), at); err == nil {
		t.Fatal("empty content must be rejected")
	}
	// Over-bound content (4097 runes): the contract bound applies to real transport too.
	long := strings.Repeat("x", airuntime.MaxContentRunes+1)
	body := wireBody(t, `{"choices":[{"message":{"role":"assistant","content":"`+long+`"},"finish_reason":"stop"}]}`)
	if _, err := decodeWireResponse(airuntime.ProviderSGLang, "qwen3-14b", body, at); err == nil {
		t.Fatal("over-bound content must be rejected at the wire boundary")
	}
	// Control runes (except newline/tab) are rejected — the contract holds on transport.
	body = wireBody(t, "{\"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"a\\u0000b\"},\"finish_reason\":\"stop\"}]}")
	if _, err := decodeWireResponse(airuntime.ProviderSGLang, "qwen3-14b", body, at); err == nil {
		t.Fatal("control runes in content must be rejected at the wire boundary")
	}
}

func TestDecodeWireUsageDiscipline(t *testing.T) {
	at := time.Now().UTC()
	// Absent usage: legal — zero accounting, validated as such.
	if _, err := decodeWireResponse(airuntime.ProviderSGLang, "qwen3-14b", wireBody(t, `{"choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`), at); err != nil {
		t.Fatalf("absent usage must decode as zero accounting, got %v", err)
	}
	// Negative usage: a lie, fails closed.
	body := wireBody(t, `{"choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":-1,"completion_tokens":0}}`)
	if _, err := decodeWireResponse(airuntime.ProviderSGLang, "qwen3-14b", body, at); err == nil {
		t.Fatal("negative usage must be rejected (negative usage is a lie, not accounting)")
	}
}

func TestDecodeWireMalformedJSON(t *testing.T) {
	at := time.Now().UTC()
	for _, raw := range []string{``, `{`, `not json`, `[]`, `"a string"`} {
		if _, err := decodeWireResponse(airuntime.ProviderSGLang, "qwen3-14b", []byte(raw), at); err == nil {
			t.Fatalf("malformed body %q must be rejected", raw)
		}
	}
}

func TestEncodeWireRequest(t *testing.T) {
	req := airuntime.Request{
		Model: "qwen3-14b",
		Messages: []airuntime.Message{
			{Role: airuntime.RoleSystem, Content: "sys"},
			{Role: airuntime.RoleUser, Content: "hi"},
		},
	}
	w := encodeWireRequest(req)
	if w.Model != "qwen3-14b" {
		t.Fatalf("model must map 1:1, got %s", w.Model)
	}
	if len(w.Messages) != 2 || w.Messages[0].Role != "system" || w.Messages[1].Role != "user" {
		t.Fatalf("roles must map 1:1 onto the wire, got %+v", w.Messages)
	}
	if w.Stream {
		t.Fatal("stream must be false — CF-2 chat is request/response; a streaming surface would be a separate reviewed act")
	}
}
