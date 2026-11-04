package airuntime

import (
	"strings"
	"testing"
	"time"
)

func TestRoleMatrix(t *testing.T) {
	for _, r := range []Role{RoleSystem, RoleUser, RoleAssistant} {
		if !r.IsValid() {
			t.Fatalf("role %s must be part of the closed enumeration", r)
		}
	}
	// No tool role until a real consumer exists.
	for _, r := range []Role{"tool", "function", "developer", ""} {
		if r.IsValid() {
			t.Fatalf("role %q must NOT be valid (closed enumeration)", r)
		}
	}
}

func TestFinishReasonMatrix(t *testing.T) {
	for _, f := range []FinishReason{FinishStop, FinishLength} {
		if !f.IsValid() {
			t.Fatalf("finish reason %s must be part of the closed enumeration", f)
		}
	}
	// Deliberately NO error value: a failed generation is an adapter error
	// the router skips past, never a response shape.
	for _, f := range []FinishReason{"error", "ERROR", "timeout", ""} {
		if f.IsValid() {
			t.Fatalf("finish reason %q must NOT be valid (no error value — failure is transport, not content)", f)
		}
	}
}

func TestMessageValidation(t *testing.T) {
	valid := []Message{
		{Role: RoleSystem, Content: "You are ScarLiX."},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "line one\nline two"}, // newline is legitimate prose
		{Role: RoleUser, Content: "a\tb"},                    // tab is legitimate prose
		{Role: RoleUser, Content: strings.Repeat("č", 4096)}, // at the rune bound (multibyte)
	}
	for _, m := range valid {
		if err := m.Validate(); err != nil {
			t.Fatalf("message must validate: %v (role=%s)", err, m.Role)
		}
	}
	invalid := []Message{
		{Role: RoleUser, Content: ""},                        // empty
		{Role: RoleUser, Content: strings.Repeat("a", 4097)}, // over the bound
		{Role: "tool", Content: "hi"},                        // unenumerated role
		{Role: RoleUser, Content: "bad\x00char"},             // control rune (not \n/\t)
		{Role: RoleUser, Content: "bad\rchar"},               // carriage return is a control rune
		{Role: RoleUser, Content: "bad\x7fchar"},             // DEL
	}
	for _, m := range invalid {
		if err := m.Validate(); err == nil {
			t.Fatalf("message must be rejected (role=%s)", m.Role)
		}
	}
}

func TestUsageValidation(t *testing.T) {
	if err := (Usage{}).Validate(); err != nil {
		t.Fatalf("zero usage is valid: %v", err)
	}
	if err := (Usage{InputTokens: 120, OutputTokens: 34}).Validate(); err != nil {
		t.Fatalf("positive usage is valid: %v", err)
	}
	if err := (Usage{InputTokens: -1}).Validate(); err == nil {
		t.Fatal("negative input tokens must be rejected (negative usage is a lie)")
	}
	if err := (Usage{OutputTokens: -5}).Validate(); err == nil {
		t.Fatal("negative output tokens must be rejected")
	}
}

func TestRequestValidationBounds(t *testing.T) {
	msg := Message{Role: RoleUser, Content: "hello"}
	// 0 messages rejected.
	if err := (Request{Model: "qwen3-14b"}).Validate(); err == nil {
		t.Fatal("a request without messages must be rejected (1..32)")
	}
	// 1 (minimum) and 32 (the bound) valid.
	one := Request{Model: "qwen3-14b", Messages: []Message{msg}}
	if err := one.Validate(); err != nil {
		t.Fatalf("1 message must validate: %v", err)
	}
	exactly := Request{Model: "qwen3-14b", Messages: make([]Message, 32)}
	for i := range exactly.Messages {
		exactly.Messages[i] = msg
	}
	if err := exactly.Validate(); err != nil {
		t.Fatalf("32 messages must validate (at the bound): %v", err)
	}
	// 33 rejected.
	over := Request{Model: "qwen3-14b", Messages: make([]Message, 33)}
	for i := range over.Messages {
		over.Messages[i] = msg
	}
	if err := over.Validate(); err == nil {
		t.Fatal("33 messages must be rejected (bound 32)")
	}
	// Model must be well-formed; every message validated.
	if err := (Request{Model: "", Messages: []Message{msg}}).Validate(); err == nil {
		t.Fatal("a request without a model must be rejected")
	}
	if err := (Request{Model: "Qwen", Messages: []Message{msg}}).Validate(); err == nil {
		t.Fatal("a malformed model id must be rejected")
	}
	badMsg := Message{Role: RoleUser, Content: strings.Repeat("a", 4097)}
	if err := (Request{Model: "qwen3-14b", Messages: []Message{badMsg}}).Validate(); err == nil {
		t.Fatal("an invalid message inside the request must be rejected")
	}
}

func TestRequestCloneIsolation(t *testing.T) {
	orig := Request{Model: "qwen3-14b", Messages: []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hello"},
	}}
	clone := orig.Clone()
	clone.Messages[0].Content = "mutated"
	clone.Messages[1].Role = "tool"
	if orig.Messages[0].Content != "sys" || orig.Messages[1].Role != RoleUser {
		t.Fatal("Clone must deep-copy — mutating the clone must not reach the original")
	}
	clone.Messages = append(clone.Messages, Message{Role: RoleUser, Content: "extra"})
	if len(orig.Messages) != 2 {
		t.Fatal("appending to the clone's slice must not reach the original")
	}
}

func TestNewResponseValidationAndStamping(t *testing.T) {
	at := time.Date(2026, 10, 24, 10, 30, 0, 0, time.FixedZone("CET", 3600))
	resp, err := NewResponse(
		ProviderSGLang,
		"qwen3-14b",
		Message{Role: RoleAssistant, Content: "answer"},
		FinishStop,
		Usage{InputTokens: 10, OutputTokens: 5},
		at,
	)
	if err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	if resp.Provider != ProviderSGLang {
		t.Fatalf("attribution lost: %s", resp.Provider)
	}
	if resp.Timestamp.Location() != time.UTC {
		t.Fatalf("response timestamp must be stamped in UTC, got %v", resp.Timestamp.Location())
	}
	if got := at.UTC(); !resp.Timestamp.Equal(got) {
		t.Fatalf("UTC stamping: got %v, want %v", resp.Timestamp, got)
	}
	// Zero timestamp rejected.
	if _, err := NewResponse(ProviderSGLang, "qwen3-14b", Message{Role: RoleAssistant, Content: "a"}, FinishStop, Usage{}, time.Time{}); err == nil {
		t.Fatal("response without timestamp must be rejected (attribution + time survive failover)")
	}
	// Cloud provider rejected — attribution is a local tier.
	if _, err := NewResponse("openai", "gpt-4", Message{Role: RoleAssistant, Content: "a"}, FinishStop, Usage{}, at); err == nil {
		t.Fatal("response attributed to a cloud provider must be rejected")
	}
	// The deliberately-absent error value.
	if _, err := NewResponse(ProviderSGLang, "qwen3-14b", Message{Role: RoleAssistant, Content: "a"}, "error", Usage{}, at); err == nil {
		t.Fatal("finish reason \"error\" must be rejected — a failed generation is an adapter error, never a response shape")
	}
	if _, err := NewResponse(ProviderSGLang, "qwen3-14b", Message{Role: RoleAssistant, Content: "a"}, FinishStop, Usage{InputTokens: -1}, at); err == nil {
		t.Fatal("negative usage in a response must be rejected")
	}
}
