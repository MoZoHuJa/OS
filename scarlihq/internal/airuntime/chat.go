package airuntime

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Normalized chat contract
// ---------------------------------------------------------------------------

// Role is a closed enumeration of chat roles: system, user, assistant.
// There is deliberately no tool role yet — a role appears when a real
// consumer needs it (the same closed-enumeration discipline as the
// kernel capabilities).
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// IsValid reports whether r is part of the closed enumeration.
func (r Role) IsValid() bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant:
		return true
	}
	return false
}

// FinishReason is a closed enumeration: stop (natural end) or length
// (the token bound was reached). There is deliberately NO error value —
// a failed generation is an ADAPTER ERROR the router skips past, never
// a response shape. Failure is transport, not content.
type FinishReason string

const (
	FinishStop   FinishReason = "stop"
	FinishLength FinishReason = "length"
)

// IsValid reports whether f is part of the closed enumeration.
func (f FinishReason) IsValid() bool {
	switch f {
	case FinishStop, FinishLength:
		return true
	}
	return false
}

// Normalized bounds. Content is bounded in RUNES (a rune bound is what a
// token-ish budget means — a byte bound would charge multibyte text
// double); requests are bounded in message count.
const (
	MaxContentRunes = 4096
	MaxMessages     = 32
)

// Message is one chat turn.
type Message struct {
	Role    Role
	Content string
}

// Validate checks the message shape: closed role, content 1..MaxContentRunes
// runes, no control characters except newline and tab (both legitimate
// prose), never other control runes.
func (m Message) Validate() error {
	if !m.Role.IsValid() {
		return fmt.Errorf("airuntime: role %q is not in the closed enumeration (system/user/assistant)", m.Role)
	}
	runes := utf8.RuneCountInString(m.Content)
	if runes < 1 {
		return fmt.Errorf("airuntime: message content is empty")
	}
	if runes > MaxContentRunes {
		return fmt.Errorf("airuntime: message content longer than %d runes (got %d)", MaxContentRunes, runes)
	}
	for _, r := range m.Content {
		if (r < 0x20 || r == 0x7f) && r != '\n' && r != '\t' {
			return fmt.Errorf("airuntime: message content contains control character %q (newline and tab are legitimate, other control runes are not)", r)
		}
	}
	return nil
}

// Usage is the non-negative token accounting of one response.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Validate checks that usage is non-negative — negative usage is a lie,
// not accounting.
func (u Usage) Validate() error {
	if u.InputTokens < 0 {
		return fmt.Errorf("airuntime: input tokens negative (%d)", u.InputTokens)
	}
	if u.OutputTokens < 0 {
		return fmt.Errorf("airuntime: output tokens negative (%d)", u.OutputTokens)
	}
	return nil
}

// Request is the normalized chat request: a model and 1..MaxMessages
// validated messages. Unknown models are the ROUTER's error (this layer
// only checks the shape); the model id itself must be well-formed.
type Request struct {
	Model    ModelID
	Messages []Message
}

// Validate checks the request shape: well-formed model id, 1..32
// messages, every message validated.
func (r Request) Validate() error {
	if err := r.Model.Validate(); err != nil {
		return err
	}
	if len(r.Messages) < 1 {
		return fmt.Errorf("airuntime: request carries no messages (1..%d required)", MaxMessages)
	}
	if len(r.Messages) > MaxMessages {
		return fmt.Errorf("airuntime: request carries %d messages (bound %d)", len(r.Messages), MaxMessages)
	}
	for i, m := range r.Messages {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("airuntime: message %d: %w", i, err)
		}
	}
	return nil
}

// Clone returns a deep copy — a handed-over request cannot be mutated
// through the original (copy isolation; the messages slice is copied).
func (r Request) Clone() Request {
	messages := make([]Message, len(r.Messages))
	copy(messages, r.Messages)
	return Request{Model: r.Model, Messages: messages}
}

// Response is the normalized chat response. EVERY response carries
// provider attribution + a required timestamp — attribution survives
// failover: the caller sees WHICH tier answered.
type Response struct {
	Provider  Provider
	Model     ModelID
	Message   Message
	Finish    FinishReason
	Usage     Usage
	Timestamp time.Time
}

// NewResponse validates and constructs a response, stamping the
// timestamp in UTC (required — a response without a time cannot be
// ordered into any audit trail). Provider attribution is mandatory and
// must be part of the closed local stack.
func NewResponse(provider Provider, model ModelID, msg Message, finish FinishReason, usage Usage, at time.Time) (Response, error) {
	if err := provider.Validate(); err != nil {
		return Response{}, err
	}
	if err := model.Validate(); err != nil {
		return Response{}, err
	}
	if err := msg.Validate(); err != nil {
		return Response{}, err
	}
	if !finish.IsValid() {
		return Response{}, fmt.Errorf("airuntime: finish reason %q is not in the closed enumeration (stop/length) — a failed generation is an adapter error, never a response shape", finish)
	}
	if err := usage.Validate(); err != nil {
		return Response{}, err
	}
	if at.IsZero() {
		return Response{}, fmt.Errorf("airuntime: response timestamp is required (attribution + time survive failover)")
	}
	return Response{
		Provider:  provider,
		Model:     model,
		Message:   msg,
		Finish:    finish,
		Usage:     usage,
		Timestamp: at.UTC(),
	}, nil
}
