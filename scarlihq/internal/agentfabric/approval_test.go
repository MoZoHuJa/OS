package agentfabric

import (
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

// BF-4 (v19.5.3): the approval model test matrix — ID shapes, the closed
// state enumeration, state↔fields coherence, the runtime-boundary
// derivation, params↔capability coherence and principal-label bounds.

func validApprovalFixture(mutate func(*ApprovalRecord)) ApprovalRecord {
	rec := ApprovalRecord{
		ID:          "apv-" + strings.Repeat("a", 32),
		CommandID:   "cmd-" + strings.Repeat("b", 32),
		AgentID:     agentkernel.AgentID("coder.test"),
		Capability:  agentkernel.CapabilityModeRequest,
		WorkspaceID: "",
		SessionID:   "aks-" + strings.Repeat("c", 32),
		Runtime:     RuntimeCoding,
		Submitter:   "zuzka",
		Params:      ApprovalParams{Mode: "ai"},
		State:       ApprovalPending,
		Intent:      "switch the host to ai mode",
		RequestedAt: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
	}
	if mutate != nil {
		mutate(&rec)
	}
	return rec
}

func TestApprovalIDValidate(t *testing.T) {
	valid := []string{"apv-0123456789abcdef0123456789abcdef"}
	for _, id := range valid {
		if err := ApprovalID(id).Validate(); err != nil {
			t.Errorf("valid id %q rejected: %v", id, err)
		}
	}
	invalid := []string{
		"",
		"apv-",
		"apv-0123",
		"afx-" + strings.Repeat("a", 32),
		"apv-" + strings.Repeat("A", 32),
		"apv-" + strings.Repeat("g", 32),
		"apv-" + strings.Repeat("a", 33),
		"apv-" + strings.Repeat("a", 31),
		strings.Repeat("a", 36),
	}
	for _, id := range invalid {
		if err := ApprovalID(id).Validate(); err == nil {
			t.Errorf("invalid id %q accepted", id)
		}
	}
}

func TestApprovalRequiredForCapabilityBoundary(t *testing.T) {
	// The approval boundary IS the runtime boundary — derived, total over
	// the kernel's closed enumeration.
	for _, cap := range agentkernel.AllCapabilities() {
		rt, ok := RuntimeForCapability(cap)
		if !ok {
			t.Fatalf("capability %q has no runtime (mapping must be total)", cap)
		}
		want := rt == RuntimeCoding
		if got := ApprovalRequiredForCapability(cap); got != want {
			t.Errorf("ApprovalRequiredForCapability(%q) = %v, want %v (the runtime boundary)", cap, got, want)
		}
	}
	// Unknown capabilities fail CLOSED (require approval — never a silent
	// auto-execute of an unmapped capability).
	if !ApprovalRequiredForCapability(agentkernel.Capability("shell.exec")) {
		t.Errorf("unknown capability must require approval (fail-closed)")
	}
}

func TestApprovalRecordValidateStateCoherence(t *testing.T) {
	decided := time.Date(2026, 10, 28, 12, 5, 0, 0, time.UTC)

	// PENDING with any decision field is corrupt.
	for _, m := range []func(*ApprovalRecord){
		func(r *ApprovalRecord) { r.Approver = "op" },
		func(r *ApprovalRecord) { r.Reason = "no" },
		func(r *ApprovalRecord) { r.ExecutionID = "afx-" + strings.Repeat("a", 32) },
		func(r *ApprovalRecord) { r.DecidedAt = &decided },
	} {
		if err := validApprovalFixture(m).validate(); err == nil {
			t.Errorf("PENDING record with decision fields accepted (corrupt)")
		}
	}

	// APPROVED requires approver + execution + decided_at.
	if err := validApprovalFixture(func(r *ApprovalRecord) {
		r.State = ApprovalApproved
	}).validate(); err == nil {
		t.Errorf("APPROVED without decision fields accepted (corrupt)")
	}
	if err := validApprovalFixture(func(r *ApprovalRecord) {
		r.State = ApprovalApproved
		r.Approver = "op"
		r.ExecutionID = "not-an-afx-id"
		r.DecidedAt = &decided
	}).validate(); err == nil {
		t.Errorf("APPROVED with malformed execution id accepted (corrupt)")
	}
	if err := validApprovalFixture(func(r *ApprovalRecord) {
		r.State = ApprovalApproved
		r.Approver = "op"
		r.ExecutionID = "afx-" + strings.Repeat("a", 32)
	}).validate(); err == nil {
		t.Errorf("APPROVED without decided_at accepted (corrupt)")
	}
	// A full APPROVED record is valid.
	if err := validApprovalFixture(func(r *ApprovalRecord) {
		r.State = ApprovalApproved
		r.Approver = "op"
		r.ExecutionID = "afx-" + strings.Repeat("a", 32)
		r.DecidedAt = &decided
	}).validate(); err != nil {
		t.Errorf("full APPROVED record rejected: %v", err)
	}

	// REJECTED never carries an execution; requires approver + decided_at.
	if err := validApprovalFixture(func(r *ApprovalRecord) {
		r.State = ApprovalRejected
		r.Approver = "op"
		r.ExecutionID = "afx-" + strings.Repeat("a", 32)
		r.DecidedAt = &decided
	}).validate(); err == nil {
		t.Errorf("REJECTED with execution reference accepted (no act ever ran — corrupt)")
	}
	if err := validApprovalFixture(func(r *ApprovalRecord) {
		r.State = ApprovalRejected
		r.Approver = "op"
		r.DecidedAt = &decided
	}).validate(); err != nil {
		t.Errorf("full REJECTED record rejected: %v", err)
	}

	// Unknown state — WITHDRAWN-shaped records land here too: there is
	// deliberately NO withdrawn state (the request's command is
	// DECIDED-terminal, it can never be cancelled out from under the
	// operator — a state no surface can reach would be a lie in the
	// enumeration).
	if err := validApprovalFixture(func(r *ApprovalRecord) {
		r.State = ApprovalState("RUNNING")
	}).validate(); err == nil {
		t.Errorf("unknown state accepted")
	}
	// decided_at before requested_at.
	if err := validApprovalFixture(func(r *ApprovalRecord) {
		r.State = ApprovalRejected
		r.Approver = "op"
		early := time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)
		r.DecidedAt = &early
	}).validate(); err == nil {
		t.Errorf("decided_at before requested_at accepted")
	}
}

func TestApprovalRecordValidateResearchNeverRequests(t *testing.T) {
	// A research capability (read-only by construction) never requires
	// approval — an approval record for one is corruption, not policy.
	for _, cap := range []agentkernel.Capability{
		agentkernel.CapabilityStatusRead,
		agentkernel.CapabilityGamingRead,
		agentkernel.CapabilityComputePlan,
	} {
		if err := validApprovalFixture(func(r *ApprovalRecord) {
			r.Capability = cap
			r.Runtime = RuntimeResearch
			r.Params = ApprovalParams{}
		}).validate(); err == nil {
			t.Errorf("research capability %q accepted on an approval record (corrupt)", cap)
		}
	}
}

func TestApprovalRecordValidateParamsCoherence(t *testing.T) {
	// mode.request: allocation_id is an unknown field.
	if err := validApprovalFixture(func(r *ApprovalRecord) {
		r.Params = ApprovalParams{Mode: "ai", AllocationID: "alloc-1"}
	}).validate(); err == nil {
		t.Errorf("mode.request params with allocation_id accepted")
	}
	// mode.request without mode: the closed shape was not captured.
	if err := validApprovalFixture(func(r *ApprovalRecord) {
		r.Params = ApprovalParams{}
	}).validate(); err == nil {
		t.Errorf("mode.request without the captured mode accepted")
	}
	// compute.reserve/release: mode is an unknown field; allocation required.
	for _, cap := range []agentkernel.Capability{agentkernel.CapabilityComputeReserve, agentkernel.CapabilityComputeRelease} {
		if err := validApprovalFixture(func(r *ApprovalRecord) {
			r.Capability = cap
			r.Params = ApprovalParams{Mode: "ai"}
		}).validate(); err == nil {
			t.Errorf("%s params with mode accepted (unknown field)", cap)
		}
		if err := validApprovalFixture(func(r *ApprovalRecord) {
			r.Capability = cap
			r.Params = ApprovalParams{}
		}).validate(); err == nil {
			t.Errorf("%s without the captured allocation_id accepted", cap)
		}
		if err := validApprovalFixture(func(r *ApprovalRecord) {
			r.Capability = cap
			r.Params = ApprovalParams{AllocationID: "alloc-1"}
		}).validate(); err != nil {
			t.Errorf("full %s record rejected: %v", cap, err)
		}
	}
	// Control characters in the captured params.
	if err := validApprovalFixture(func(r *ApprovalRecord) {
		r.Params = ApprovalParams{Mode: "a\x02i"}
	}).validate(); err == nil {
		t.Errorf("control character in captured mode accepted")
	}
}

func TestApprovalRecordValidateLabelsAndShapes(t *testing.T) {
	// Submitter label bounds (empty = unlabeled, the BF-1 behavior —
	// valid; whitespace-only and over-bound labels are corrupt).
	for _, bad := range []string{" ", strings.Repeat("x", 65), "line\nbreak", "tab\there"} {
		if err := validApprovalFixture(func(r *ApprovalRecord) { r.Submitter = bad }).validate(); err == nil {
			t.Errorf("submitter label %q accepted", bad)
		}
	}
	// A full-width label is fine.
	if err := validApprovalFixture(func(r *ApprovalRecord) { r.Submitter = strings.Repeat("x", 64) }).validate(); err != nil {
		t.Errorf("64-rune submitter label rejected: %v", err)
	}
	// Intent bounds ride the command rule (echoed verbatim).
	if err := validApprovalFixture(func(r *ApprovalRecord) { r.Intent = "" }).validate(); err == nil {
		t.Errorf("empty intent accepted")
	}
	// Session shape.
	if err := validApprovalFixture(func(r *ApprovalRecord) { r.SessionID = "nope" }).validate(); err == nil {
		t.Errorf("malformed session id accepted")
	}
	// Workspace shape when present.
	if err := validApprovalFixture(func(r *ApprovalRecord) { r.WorkspaceID = "ws-bad" }).validate(); err == nil {
		t.Errorf("malformed workspace id accepted")
	}
	// Runtime↔capability coherence (the closed mapping).
	if err := validApprovalFixture(func(r *ApprovalRecord) { r.Runtime = RuntimeResearch }).validate(); err == nil {
		t.Errorf("research runtime on a coding capability accepted (closed mapping coherence)")
	}
}

func TestValidatePrincipalLabel(t *testing.T) {
	valid := []string{"zuzka", "op-1", "operator@scarlihq", strings.Repeat("名", 64)}
	for _, label := range valid {
		if err := ValidatePrincipalLabel(label); err != nil {
			t.Errorf("valid label %q rejected: %v", label, err)
		}
	}
	invalid := []string{"", "  ", strings.Repeat("x", 65), "a\nb", "a\x7fb", "a\x02b"}
	for _, label := range invalid {
		if err := ValidatePrincipalLabel(label); err == nil {
			t.Errorf("invalid label %q accepted", label)
		}
	}
}

func TestApprovalStatesClosedEnumeration(t *testing.T) {
	states := ApprovalStates()
	want := []ApprovalState{ApprovalPending, ApprovalApproved, ApprovalRejected}
	if len(states) != len(want) {
		t.Fatalf("ApprovalStates() = %v, want %v", states, want)
	}
	for i, s := range want {
		if states[i] != s {
			t.Errorf("ApprovalStates()[%d] = %q, want %q (deterministic order)", i, states[i], s)
		}
	}
}
