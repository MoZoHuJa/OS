package airt

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// ---------------------------------------------------------------------------
// The OpenAI-compatible wire (what the whole real local stack speaks)
// ---------------------------------------------------------------------------

// wireRequest is the chat-completions body every tier of the real local
// stack speaks: sglang, vllm, beellama (llama.cpp server) and ollama all
// expose POST /v1/chat/completions with this shape. Streaming is
// deliberately false — CF-2 chat is request/response; a streaming
// surface would be a separate reviewed act.
type wireRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// wireMessage is one wire turn. The roles map 1:1 onto the CF-1 closed
// enumeration (system/user/assistant) — nothing else crosses the wire.
type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// wireResponse is the subset of the chat-completions response the
// runtime normalizes. Unknown JSON fields are ignored ON PURPOSE here
// (servers add their own metadata: id, created, model echo, system
// fingerprints) — the closed contract is enforced on the NORMALIZED
// types, not on the servers' freedom to decorate their responses.
type wireResponse struct {
	Choices []wireChoice `json:"choices"`
	Usage   *wireUsage   `json:"usage"`
}

// wireChoice is one completion choice. Non-streamed responses carry
// exactly one — zero or several is a malformed transport answer.
type wireChoice struct {
	Message      wireMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// wireUsage is the token accounting. A pointer in wireResponse: an
// absent usage object is legal (zero accounting — validated as such), a
// present-but-negative one is a lie and fails closed.
type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// encodeWireRequest renders the normalized request onto the wire. The
// request is expected to be validated already (Adapter.Chat does); this
// function only maps shapes, it makes no decisions.
func encodeWireRequest(req airuntime.Request) wireRequest {
	messages := make([]wireMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, wireMessage{Role: string(m.Role), Content: m.Content})
	}
	return wireRequest{Model: string(req.Model), Messages: messages, Stream: false}
}

// decodeWireResponse normalizes one chat-completions body into the CF-1
// contract. Fail-closed, by rule:
//   - exactly one choice (zero or several = malformed transport answer);
//   - the choice's role must be "assistant" (a response IS an assistant
//     turn — anything else is a shape violation, never coerced);
//   - finish_reason must be "stop" or "length" — any other value
//     ("content_filter", "tool_calls", "", ...) is an adapter ERROR, not
//     a coerced "stop": unknown finish reasons are transport failures;
//   - usage may be absent (zero accounting); present values are
//     validated non-negative by NewResponse;
//   - the content must survive Message validation (non-empty, bounded,
//     no control runes) — NewResponse enforces all of it;
//   - the timestamp is stamped HERE, in UTC — attribution + time survive
//     failover (the CF-1 contract, now behind real transport).
func decodeWireResponse(provider airuntime.Provider, model airuntime.ModelID, body []byte, at time.Time) (airuntime.Response, error) {
	var wr wireResponse
	if err := json.Unmarshal(body, &wr); err != nil {
		return airuntime.Response{}, fmt.Errorf("airt: %s: response is not valid JSON: %w", provider, err)
	}
	if len(wr.Choices) != 1 {
		return airuntime.Response{}, fmt.Errorf("airt: %s: response carries %d choices (exactly one required — non-streamed transport)", provider, len(wr.Choices))
	}
	choice := wr.Choices[0]
	if choice.Message.Role != string(airuntime.RoleAssistant) {
		return airuntime.Response{}, fmt.Errorf("airt: %s: response role %q is not %q (a response IS an assistant turn)", provider, choice.Message.Role, airuntime.RoleAssistant)
	}
	finish := airuntime.FinishReason(choice.FinishReason)
	if !finish.IsValid() {
		return airuntime.Response{}, fmt.Errorf("airt: %s: finish reason %q is not in the closed enumeration (stop/length) — unknown finish reasons are transport failures, never coerced", provider, choice.FinishReason)
	}
	usage := airuntime.Usage{}
	if wr.Usage != nil {
		usage = airuntime.Usage{InputTokens: wr.Usage.PromptTokens, OutputTokens: wr.Usage.CompletionTokens}
	}
	msg := airuntime.Message{Role: airuntime.RoleAssistant, Content: choice.Message.Content}
	return airuntime.NewResponse(provider, model, msg, finish, usage, at)
}
