package agentfabric

// CF-3 (v19.6.2): the proposal model + parser test matrix — ID shapes,
// the closed state enumeration, state↔fields coherence, typed proposer
// attribution, and the fail-closed parser (fence discipline, closed
// three-field shape, the bar's own validators, ambiguity refused).

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

func validProposalFixture(mutate func(*ProposalRecord)) ProposalRecord {
	rec := ProposalRecord{
		ID:               "prp-" + strings.Repeat("a", 32),
		Intent:           "switch the host to ai mode",
		AgentID:          agentkernel.AgentID("coder.test"),
		Capability:       agentkernel.CapabilityModeRequest,
		ProposerProvider: airuntime.ProviderSGLang,
		ProposerModel:    airuntime.ModelID("qwen3-14b"),
		State:            ProposalPending,
		CapturedAt:       time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
	}
	if mutate != nil {
		mutate(&rec)
	}
	return rec
}

func TestProposalIDValidate(t *testing.T) {
	valid := []string{"prp-0123456789abcdef0123456789abcdef"}
	for _, id := range valid {
		if err := ProposalID(id).Validate(); err != nil {
			t.Errorf("valid id %q rejected: %v", id, err)
		}
	}
	invalid := []string{
		"",
		"prp-",
		"prp-0123",
		"apv-" + strings.Repeat("a", 32),
		"prp-" + strings.Repeat("A", 32),
		"prp-" + strings.Repeat("g", 32),
		"prp-" + strings.Repeat("a", 33),
		"prp-" + strings.Repeat("a", 31),
		strings.Repeat("a", 36),
	}
	for _, id := range invalid {
		if err := ProposalID(id).Validate(); err == nil {
			t.Errorf("invalid id %q accepted", id)
		}
	}
}

func TestProposalRecordValidateStateCoherence(t *testing.T) {
	decided := time.Date(2026, 10, 30, 12, 5, 0, 0, time.UTC)
	commandID := "cmd-" + strings.Repeat("b", 32)

	// PENDING with any decision field is corrupt.
	for _, m := range []func(*ProposalRecord){
		func(r *ProposalRecord) { r.Operator = "zuzka" },
		func(r *ProposalRecord) { r.Reason = "no" },
		func(r *ProposalRecord) { r.CommandID = commandID },
		func(r *ProposalRecord) { r.DecidedAt = &decided },
	} {
		if err := validProposalFixture(m).validate(); err == nil {
			t.Errorf("PENDING record with decision fields accepted (corrupt)")
		}
	}

	// ACCEPTED requires operator + a well-formed command reference + decided_at.
	if err := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalAccepted
	}).validate(); err == nil {
		t.Errorf("ACCEPTED without decision fields accepted (corrupt)")
	}
	if err := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalAccepted
		r.Operator = "zuzka"
		r.CommandID = "not-a-cmd-id"
		r.DecidedAt = &decided
	}).validate(); err == nil {
		t.Errorf("ACCEPTED with malformed command id accepted (corrupt)")
	}
	if err := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalAccepted
		r.Operator = "zuzka"
		r.CommandID = commandID
	}).validate(); err == nil {
		t.Errorf("ACCEPTED without decided_at accepted (corrupt)")
	}
	if err := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalAccepted
		r.Operator = "zuzka"
		r.CommandID = commandID
		r.Reason = "why"
		r.DecidedAt = &decided
	}).validate(); err == nil {
		t.Errorf("ACCEPTED with a reason accepted (reason is the dismissal's why — corrupt)")
	}
	// A full ACCEPTED record is valid.
	if err := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalAccepted
		r.Operator = "zuzka"
		r.CommandID = commandID
		r.DecidedAt = &decided
	}).validate(); err != nil {
		t.Errorf("full ACCEPTED record rejected: %v", err)
	}

	// DISMISSED requires operator + decided_at and NEVER a command reference.
	if err := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalDismissed
	}).validate(); err == nil {
		t.Errorf("DISMISSED without decision fields accepted (corrupt)")
	}
	if err := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalDismissed
		r.Operator = "zuzka"
		r.DecidedAt = &decided
		r.CommandID = commandID
	}).validate(); err == nil {
		t.Errorf("DISMISSED with a command reference accepted — a dismissal never became a command (corrupt)")
	}
	if err := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalDismissed
		r.Operator = "zuzka"
		r.Reason = "not now"
		r.DecidedAt = &decided
	}).validate(); err != nil {
		t.Errorf("DISMISSED with a bounded reason rejected: %v", err)
	}
}

func TestProposalRecordValidateAttributionAndFields(t *testing.T) {
	decided := time.Date(2026, 10, 30, 12, 5, 0, 0, time.UTC)
	// Typed proposer attribution: foreign providers and malformed model ids
	// are refused — the record says WHO proposed using the runtime's own
	// typed identity, never free text.
	for _, m := range []func(*ProposalRecord){
		func(r *ProposalRecord) { r.ProposerProvider = airuntime.Provider("openai") },
		func(r *ProposalRecord) { r.ProposerProvider = airuntime.Provider("") },
		func(r *ProposalRecord) { r.ProposerModel = airuntime.ModelID("") },
		func(r *ProposalRecord) { r.ProposerModel = airuntime.ModelID("Not A Model") },
		func(r *ProposalRecord) { r.Intent = "" },
		func(r *ProposalRecord) { r.AgentID = agentkernel.AgentID("not an agent id") },
		func(r *ProposalRecord) { r.Capability = agentkernel.Capability("shell.exec") },
		func(r *ProposalRecord) { r.State = ProposalState("WITHDRAWN") },
		func(r *ProposalRecord) { r.CapturedAt = time.Time{} },
		func(r *ProposalRecord) { r.UpdatedAt = validProposalFixture(nil).CapturedAt.Add(-time.Minute) },
	} {
		if err := validProposalFixture(m).validate(); err == nil {
			t.Errorf("corrupt proposal accepted (mutation should fail validation)")
		}
	}
	// decided_at before captured_at is corrupt.
	if err := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalDismissed
		r.Operator = "zuzka"
		r.DecidedAt = &decided
	}).validate(); err != nil {
		t.Fatalf("baseline DISMISSED fixture rejected: %v", err)
	}
	early := time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)
	if err := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalDismissed
		r.Operator = "zuzka"
		r.DecidedAt = &early
	}).validate(); err == nil {
		t.Errorf("decided_at before captured_at accepted (corrupt)")
	}
	// The operator label is a bounded principal label (attribution, never a credential).
	if err := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalDismissed
		r.Operator = strings.Repeat("x", 65)
		r.DecidedAt = &decided
	}).validate(); err == nil {
		t.Errorf("over-bound operator label accepted (corrupt)")
	}
}

func TestParseProposalHappyBlock(t *testing.T) {
	// Single-line JSON, prose around the fence, trailing whitespace on the
	// fence line — the honest shape.
	content := "I think we should switch modes.\n\n```scarlix-proposal   \n{\"intent\":\"switch the host to ai mode\",\"agent_id\":\"coder.test\",\"capability\":\"mode.request\"}\n```\n\nLet me know."
	draft, err := ParseProposal(content)
	if err != nil {
		t.Fatalf("happy block rejected: %v", err)
	}
	happy := draft // the FIRST parse — determinism compares against THIS
	if draft.Intent != "switch the host to ai mode" {
		t.Errorf("intent = %q", draft.Intent)
	}
	if draft.AgentID != agentkernel.AgentID("coder.test") {
		t.Errorf("agent = %q", draft.AgentID)
	}
	if draft.Capability != agentkernel.CapabilityModeRequest {
		t.Errorf("capability = %q", draft.Capability)
	}

	// Multi-line JSON inside the fence is fine (the content is everything
	// between the fences).
	multi := "```scarlix-proposal\n{\n  \"intent\": \"read gpu status\",\n  \"agent_id\": \"research.one\",\n  \"capability\": \"status.read\"\n}\n```"
	draft, err = ParseProposal(multi)
	if err != nil {
		t.Fatalf("multi-line block rejected: %v", err)
	}
	if draft.Capability != agentkernel.CapabilityStatusRead {
		t.Errorf("capability = %q", draft.Capability)
	}

	// Determinism: same input, same output, byte for byte.
	for i := 0; i < 20; i++ {
		d2, err := ParseProposal(content)
		if err != nil || d2 != happy {
			t.Fatalf("parser is not deterministic on iteration %d: %v", i, err)
		}
	}
}

func TestParseProposalNoBlock(t *testing.T) {
	for _, content := range []string{
		"",
		"plain answer, no structure",
		"```json\n{\"intent\":\"x\"}\n```", // wrong fence tag
		"  ```scarlix-proposal\n{}\n```",   // indented fence — NOT column 0
		"```scarlix-proposalx\n{}\n```",    // wrong tag suffix
	} {
		draft, err := ParseProposal(content)
		if !errors.Is(err, ErrNoProposal) {
			t.Errorf("content %q: expected ErrNoProposal, got %v (draft %+v)", content, err, draft)
		}
	}
}

func TestParseProposalMalformedMatrix(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"unterminated fence", "prose\n```scarlix-proposal\n{\"intent\":\"x\"}"},
		{"two blocks", "```scarlix-proposal\n{\"intent\":\"a\",\"agent_id\":\"coder.test\",\"capability\":\"mode.request\"}\n```\nprose\n```scarlix-proposal\n{\"intent\":\"b\",\"agent_id\":\"coder.test\",\"capability\":\"mode.request\"}\n```"},
		{"empty block", "```scarlix-proposal\n```"},
		{"bad json", "```scarlix-proposal\nnot json at all\n```"},
		{"two objects", "```scarlix-proposal\n{\"intent\":\"a\",\"agent_id\":\"coder.test\",\"capability\":\"mode.request\"} {\"intent\":\"b\"}\n```"},
		{"unknown field", "```scarlix-proposal\n{\"intent\":\"x\",\"agent_id\":\"coder.test\",\"capability\":\"mode.request\",\"mode\":\"ai\"}\n```"},
		{"missing intent", "```scarlix-proposal\n{\"agent_id\":\"coder.test\",\"capability\":\"mode.request\"}\n```"},
		{"missing agent", "```scarlix-proposal\n{\"intent\":\"x\",\"capability\":\"mode.request\"}\n```"},
		{"missing capability", "```scarlix-proposal\n{\"intent\":\"x\",\"agent_id\":\"coder.test\"}\n```"},
		{"empty intent", "```scarlix-proposal\n{\"intent\":\"   \",\"agent_id\":\"coder.test\",\"capability\":\"mode.request\"}\n```"},
		{"invalid agent", "```scarlix-proposal\n{\"intent\":\"x\",\"agent_id\":\"not an agent id\",\"capability\":\"mode.request\"}\n```"},
		{"unknown capability", "```scarlix-proposal\n{\"intent\":\"x\",\"agent_id\":\"coder.test\",\"capability\":\"shell.exec\"}\n```"},
		{"control rune intent", "```scarlix-proposal\n{\"intent\":\"line\\u0007break\",\"agent_id\":\"coder.test\",\"capability\":\"mode.request\"}\n```"},
		{"over-bound intent", "```scarlix-proposal\n{\"intent\":\"" + strings.Repeat("x", 513) + "\",\"agent_id\":\"coder.test\",\"capability\":\"mode.request\"}\n```"},
	}
	for _, tc := range cases {
		draft, err := ParseProposal(tc.content)
		if !errors.Is(err, ErrMalformedProposal) {
			t.Errorf("%s: expected ErrMalformedProposal, got %v (draft %+v)", tc.name, err, draft)
			continue
		}
		if draft != (ProposalDraft{}) {
			t.Errorf("%s: a partial draft returned — the parser never returns a guess", tc.name)
		}
	}
}

func TestParseProposalNeverGuesses(t *testing.T) {
	// The named-reason contract: every malformed error carries the reason.
	_, err := ParseProposal("```scarlix-proposal\n{\"intent\":\"x\"}\n```")
	if err == nil || !strings.Contains(err.Error(), "unknown capability") {
		// missing capability → the JSON decode fails first; pick the real case:
		_, err = ParseProposal("```scarlix-proposal\n{\"intent\":\"x\",\"agent_id\":\"coder.test\",\"capability\":\"shell.exec\"}\n```")
		if err == nil || !strings.Contains(err.Error(), "shell.exec") {
			t.Fatalf("the malformed error must name the offender: %v", err)
		}
	}
	// ErrNoProposal is NOT malformed — the honest null.
	if errors.Is(ErrNoProposal, ErrMalformedProposal) {
		t.Fatalf("ErrNoProposal must not wrap ErrMalformedProposal (the model proposing nothing is not a failure)")
	}
}

func TestNewProposalIDShape(t *testing.T) {
	id, err := NewProposalID()
	if err != nil {
		t.Fatalf("rng: %v", err)
	}
	if err := ProposalID(id).Validate(); err != nil {
		t.Fatalf("generated id invalid: %v", err)
	}
	// Fail-closed RNG injection (the cmd-/ws-/afx-/apv- precedent).
	old := rngProposalID
	rngProposalID = func() (string, error) { return "", ErrProposalRNG }
	defer func() { rngProposalID = old }()
	if _, err := NewProposalID(); !errors.Is(err, ErrProposalRNG) {
		t.Fatalf("rng failure must abort (fail-closed), got %v", err)
	}
}

func TestCopyProposalRecordIsolation(t *testing.T) {
	decided := time.Date(2026, 10, 30, 12, 5, 0, 0, time.UTC)
	rec := validProposalFixture(func(r *ProposalRecord) {
		r.State = ProposalDismissed
		r.Operator = "zuzka"
		r.DecidedAt = &decided
	})
	cp := copyProposalRecord(rec)
	if cp.DecidedAt == rec.DecidedAt {
		t.Fatalf("copy aliases the DecidedAt pointer — callers could mutate store state")
	}
	*cp.DecidedAt = decided.Add(time.Hour)
	if !rec.DecidedAt.Equal(decided) {
		t.Fatalf("mutating the copy changed the original")
	}
}
