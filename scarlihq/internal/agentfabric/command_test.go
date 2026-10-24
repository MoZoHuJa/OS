package agentfabric

import (
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

func validCommandID() CommandID {
	// Deterministic well-formed ID for record tests (store tests generate
	// real crypto IDs; here we only validate the SHAPE).
	return CommandID("cmd-0123456789abcdef0123456789abcdef")
}

func TestCommandIDValidate(t *testing.T) {
	valid := []string{
		"cmd-0123456789abcdef0123456789abcdef",
		"cmd-ffffffffffffffffffffffffffffffff",
		"cmd-00000000000000000000000000000000",
	}
	for _, v := range valid {
		if err := CommandID(v).Validate(); err != nil {
			t.Errorf("CommandID(%q).Validate() = %v, want nil", v, err)
		}
	}
	invalid := map[string]string{
		"":                                      "empty",
		"cmd-":                                  "prefix only",
		"cmd-0123456789abcdef":                  "too short",
		"cmd-0123456789abcdef0123456789abcdeff": "too long",
		"aks-0123456789abcdef0123456789abcdef":  "wrong prefix",
		"cmd-0123456789ABCDEF0123456789ABCDEF":  "uppercase hex",
		"cmd-0123456789abcdeg0123456789abcdef":  "non-hex char g",
		"cmd 0123456789abcdef0123456789abcdef":  "space instead of dash",
		"0123456789abcdef0123456789abcdef":      "no prefix",
	}
	for v, why := range invalid {
		if err := CommandID(v).Validate(); err == nil {
			t.Errorf("CommandID(%q).Validate() = nil, want error (%s)", v, why)
		}
	}
}

func TestValidateIntent(t *testing.T) {
	valid := []string{
		"read the gaming inventory",
		"  trimmedintent  ",
		strings.Repeat("a", MaxIntentRunes),
		"ASCII + unicode ľščťžýáíéô äöü — intents may be multilingual",
	}
	for _, v := range valid {
		if err := ValidateIntent(v); err != nil {
			t.Errorf("ValidateIntent(%q) = %v, want nil", v, err)
		}
	}
	invalid := map[string]string{
		"":                                    "empty",
		"   ":                                 "whitespace only",
		"\t":                                  "tab only (control char)",
		"line\nbreak":                         "newline is a control character",
		"carriage\rreturn":                    "CR is a control character",
		"null\x00byte":                        "NUL",
		"del\x7fchar":                         "DEL",
		"escape\x1b[31msequence":              "ANSI escape",
		strings.Repeat("a", MaxIntentRunes+1): "too long by one rune",
		strings.Repeat("ľ", MaxIntentRunes+1): "too long (multibyte runes count as runes)",
	}
	for v, why := range invalid {
		if err := ValidateIntent(v); err == nil {
			t.Errorf("ValidateIntent(%q) = nil, want error (%s)", v, why)
		}
	}
}

func TestLegalCommandTransitions(t *testing.T) {
	cases := map[CommandState]map[CommandState]bool{
		CommandSubmitted: {CommandRouted: true, CommandCancelled: true},
		CommandRouted:    {CommandRouted: true, CommandDecided: true, CommandCancelled: true},
		CommandDecided:   {},
		CommandCancelled: {},
	}
	for from, allowed := range cases {
		got := LegalCommandTransitions(from)
		if len(got) != len(allowed) {
			t.Errorf("LegalCommandTransitions(%s) = %v, want %d targets", from, got, len(allowed))
		}
		for _, to := range got {
			if !allowed[to] {
				t.Errorf("LegalCommandTransitions(%s) reports illegal target %s", from, to)
			}
		}
	}
}

func TestCommandTransitionMatrix(t *testing.T) {
	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	type step struct {
		to   CommandState
		at   time.Time
		want error
	}
	tests := []struct {
		name  string
		from  CommandState
		steps []step
	}{
		{"submit to route to decide", CommandSubmitted, []step{
			{CommandRouted, base.Add(time.Minute), nil},
			{CommandDecided, base.Add(2 * time.Minute), nil},
		}},
		{"submit to cancel", CommandSubmitted, []step{
			{CommandCancelled, base.Add(time.Minute), nil},
		}},
		{"routed to cancel", CommandRouted, []step{
			{CommandCancelled, base.Add(time.Minute), nil},
		}},
		{"routed re-bind", CommandRouted, []step{
			{CommandRouted, base.Add(time.Minute), nil},
		}},
		{"submit cannot decide directly", CommandSubmitted, []step{
			{CommandDecided, base.Add(time.Minute), errWant},
		}},
		{"decided is terminal", CommandDecided, []step{
			{CommandCancelled, base.Add(time.Minute), errWant},
			{CommandRouted, base.Add(2 * time.Minute), errWant},
			{CommandDecided, base.Add(3 * time.Minute), errWant},
		}},
		{"cancelled is terminal", CommandCancelled, []step{
			{CommandRouted, base.Add(time.Minute), errWant},
			{CommandDecided, base.Add(2 * time.Minute), errWant},
			{CommandCancelled, base.Add(3 * time.Minute), errWant},
		}},
		{"backwards clock rejected", CommandRouted, []step{
			{CommandDecided, base.Add(-time.Second), errWant},
		}},
		{"unknown target rejected", CommandSubmitted, []step{
			{CommandState("HELD"), base.Add(time.Minute), errWant},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &CommandRecord{
				ID:          string(validCommandID()),
				Intent:      "test intent",
				AgentID:     "coder.agent-one",
				Capability:  agentkernel.CapabilityStatusRead,
				State:       tc.from,
				SubmittedAt: base,
				UpdatedAt:   base,
			}
			for _, st := range tc.steps {
				err := rec.transition(st.to, st.at)
				if st.want == nil && err != nil {
					t.Fatalf("transition(%s) = %v, want nil", st.to, err)
				}
				if st.want != nil && err == nil {
					t.Fatalf("transition(%s) = nil, want error", st.to)
				}
			}
		})
	}
}

// errWant is a sentinel meaning "an error is expected" (used as a non-nil
// placeholder in the matrix above).
var errWant = &staticError{}

type staticError struct{}

func (e *staticError) Error() string { return "expected error" }

func TestCommandRecordValidate(t *testing.T) {
	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	routed := base.Add(time.Minute)
	decided := base.Add(2 * time.Minute)
	allow := &agentkernel.Decision{Allow: true, Reason: "grant: agent coder.agent-one holds capability status.read"}
	deny := &agentkernel.Decision{Allow: false, Reason: "rule/capability-not-granted: ..."}

	valid := []CommandRecord{
		{ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandSubmitted, SubmittedAt: base, UpdatedAt: base},
		{ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityGamingRead, State: CommandRouted, SessionID: "aks-x", RoutedAt: &routed, SubmittedAt: base, UpdatedAt: routed},
		{ID: string(validCommandID()), Intent: "i", AgentID: "research.agent-two", Capability: agentkernel.CapabilityComputePlan, State: CommandDecided, SessionID: "aks-x", RoutedAt: &routed, DecidedAt: &decided, Decision: allow, SubmittedAt: base, UpdatedAt: decided},
		{ID: string(validCommandID()), Intent: "i", AgentID: "research.agent-two", Capability: agentkernel.CapabilityComputeReserve, State: CommandDecided, SessionID: "aks-x", RoutedAt: &routed, DecidedAt: &decided, Decision: deny, SubmittedAt: base, UpdatedAt: decided},
		{ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityModeRequest, State: CommandCancelled, CancelledAt: &decided, SubmittedAt: base, UpdatedAt: decided},
		{ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandCancelled, SessionID: "aks-x", RoutedAt: &routed, CancelledAt: &decided, SubmittedAt: base, UpdatedAt: decided},
	}
	for i, r := range valid {
		if err := r.validate(); err != nil {
			t.Errorf("valid record %d: validate() = %v, want nil", i, err)
		}
	}

	invalid := map[string]CommandRecord{
		"bad id":                       {ID: "nope", Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandSubmitted, SubmittedAt: base, UpdatedAt: base},
		"bad intent":                   {ID: string(validCommandID()), Intent: "", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandSubmitted, SubmittedAt: base, UpdatedAt: base},
		"bad agent":                    {ID: string(validCommandID()), Intent: "i", AgentID: "nope", Capability: agentkernel.CapabilityStatusRead, State: CommandSubmitted, SubmittedAt: base, UpdatedAt: base},
		"bad capability":               {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.Capability("shell.exec"), State: CommandSubmitted, SubmittedAt: base, UpdatedAt: base},
		"bad state":                    {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandState("HELD"), SubmittedAt: base, UpdatedAt: base},
		"zero submitted":               {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandSubmitted, UpdatedAt: base},
		"updated before submitted":     {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandSubmitted, SubmittedAt: base, UpdatedAt: base.Add(-time.Second)},
		"submitted with session":       {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandSubmitted, SessionID: "aks-x", SubmittedAt: base, UpdatedAt: base},
		"routed without session":       {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandRouted, RoutedAt: &routed, SubmittedAt: base, UpdatedAt: routed},
		"routed with decision":         {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandRouted, SessionID: "aks-x", RoutedAt: &routed, Decision: allow, SubmittedAt: base, UpdatedAt: routed},
		"decided without decision":     {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandDecided, SessionID: "aks-x", RoutedAt: &routed, DecidedAt: &decided, SubmittedAt: base, UpdatedAt: decided},
		"decided empty reason":         {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandDecided, SessionID: "aks-x", RoutedAt: &routed, DecidedAt: &decided, Decision: &agentkernel.Decision{Allow: true}, SubmittedAt: base, UpdatedAt: decided},
		"decided with cancelled":       {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandDecided, SessionID: "aks-x", RoutedAt: &routed, DecidedAt: &decided, Decision: allow, CancelledAt: &decided, SubmittedAt: base, UpdatedAt: decided},
		"cancelled without timestamp":  {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandCancelled, SubmittedAt: base, UpdatedAt: base},
		"cancelled with decision":      {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandCancelled, Decision: allow, CancelledAt: &decided, SubmittedAt: base, UpdatedAt: decided},
		"cancelled mismatched session": {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandCancelled, SessionID: "aks-x", CancelledAt: &decided, SubmittedAt: base, UpdatedAt: decided},
		"routed before submitted":      {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandRouted, SessionID: "aks-x", RoutedAt: func() *time.Time { t := base.Add(-time.Second); return &t }(), SubmittedAt: base, UpdatedAt: base},
		"decided before routed":        {ID: string(validCommandID()), Intent: "i", AgentID: "coder.agent-one", Capability: agentkernel.CapabilityStatusRead, State: CommandDecided, SessionID: "aks-x", RoutedAt: &routed, DecidedAt: func() *time.Time { t := base; return &t }(), Decision: allow, SubmittedAt: base, UpdatedAt: routed},
	}
	for why, r := range invalid {
		if err := r.validate(); err == nil {
			t.Errorf("invalid record (%s): validate() = nil, want error", why)
		}
	}
}
