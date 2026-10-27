package agentfabric

import (
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

func validCommandIDForExec() CommandID {
	return CommandID("cmd-" + strings.Repeat("b", 32))
}

func validSessionIDForExec() string {
	return "aks-" + strings.Repeat("c", 32)
}

// baseExecution builds a valid COMPLETED execution record fixture.
func baseExecution() ExecutionRecord {
	return ExecutionRecord{
		ID:         validExecutionID(),
		CommandID:  string(validCommandIDForExec()),
		AgentID:    agentkernel.AgentID("research.agent-one"),
		Capability: agentkernel.CapabilityStatusRead,
		SessionID:  validSessionIDForExec(),
		Runtime:    RuntimeResearch,
		State:      ExecutionCompleted,
		Allow:      true,
		Outcome:    "status snapshot: mode=ai timestamp=2026-10-27T12:00:00Z stale=false (read-only)",
		Reason:     "grant: agent research.agent-one holds capability status.read",
		Intent:     "read the host status",
		ExecutedAt: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
	}
}

func TestExecutionIDValidate(t *testing.T) {
	if err := ExecutionID(validExecutionID()).Validate(); err != nil {
		t.Errorf("valid execution id rejected: %v", err)
	}
	for _, bad := range []string{"", "afx-short", "cmd-" + strings.Repeat("a", 32), "afx-" + strings.Repeat("g", 32), "AFX-" + strings.Repeat("a", 32)} {
		if err := ExecutionID(bad).Validate(); err == nil {
			t.Errorf("malformed execution id %q accepted", bad)
		}
	}
}

func TestExecutionStatesClosed(t *testing.T) {
	if !validExecutionState(ExecutionCompleted) || !validExecutionState(ExecutionFailed) || !validExecutionState(ExecutionSkipped) {
		t.Error("validExecutionState rejects a known state")
	}
	if validExecutionState("RUNNING") {
		t.Error("RUNNING accepted — there is NO in-flight state (terminal by construction)")
	}
	if validExecutionState("PENDING") {
		t.Error("PENDING accepted — there is NO in-flight state (terminal by construction)")
	}
	if len(ExecutionStates()) != 3 {
		t.Errorf("ExecutionStates = %v — COMPLETED/FAILED/SKIPPED only", ExecutionStates())
	}
}

func TestExecutionValidateStateMatrix(t *testing.T) {
	// COMPLETED: allow=true + non-empty outcome.
	rec := baseExecution()
	if err := rec.validate(); err != nil {
		t.Fatalf("valid COMPLETED record rejected: %v", err)
	}
	bad := rec
	bad.Allow = false
	if err := bad.validate(); err == nil {
		t.Error("COMPLETED with allow=false accepted — a completed act required a fresh allow")
	}
	bad = rec
	bad.Outcome = ""
	if err := bad.validate(); err == nil {
		t.Error("COMPLETED without outcome accepted — the act produced something")
	}
	bad = rec
	bad.Outcome = "bad\noutcome"
	if err := bad.validate(); err == nil {
		t.Error("COMPLETED with control-character outcome accepted")
	}

	// FAILED: allow=true + empty outcome + non-empty reason.
	failed := baseExecution()
	failed.State = ExecutionFailed
	failed.Outcome = ""
	failed.Reason = "mode bridge rejected the request: invalid mode"
	if err := failed.validate(); err != nil {
		t.Fatalf("valid FAILED record rejected: %v", err)
	}
	bad = failed
	bad.Allow = false
	if err := bad.validate(); err == nil {
		t.Error("FAILED with allow=false accepted — the act RAN (fresh allow), a fresh denial is SKIPPED")
	}
	bad = failed
	bad.Outcome = "something"
	if err := bad.validate(); err == nil {
		t.Error("FAILED with an outcome accepted — the act failed, there is no act result")
	}
	bad = failed
	bad.Reason = ""
	if err := bad.validate(); err == nil {
		t.Error("FAILED without a reason accepted — the act error must be named")
	}

	// SKIPPED: allow=false + empty outcome + non-empty reason (the fresh denial).
	skipped := baseExecution()
	skipped.State = ExecutionSkipped
	skipped.Allow = false
	skipped.Outcome = ""
	skipped.Reason = "rule/session-state: session is SUSPENDED, must be ACTIVE"
	if err := skipped.validate(); err != nil {
		t.Fatalf("valid SKIPPED record rejected: %v", err)
	}
	bad = skipped
	bad.Allow = true
	if err := bad.validate(); err == nil {
		t.Error("SKIPPED with allow=true accepted — the stale-allow guard skips only on a fresh DENIAL")
	}
	bad = skipped
	bad.Outcome = "something"
	if err := bad.validate(); err == nil {
		t.Error("SKIPPED with an outcome accepted — the act never ran")
	}
	bad = skipped
	bad.Reason = ""
	if err := bad.validate(); err == nil {
		t.Error("SKIPPED without a reason accepted — the fresh denial rule must be named")
	}
}

func TestExecutionValidateRuntimeCoherence(t *testing.T) {
	// A coding capability labeled research is a corrupt record.
	rec := baseExecution()
	rec.Capability = agentkernel.CapabilityModeRequest
	rec.Runtime = RuntimeResearch
	if err := rec.validate(); err == nil {
		t.Error("coding capability with research runtime accepted — the closed mapping is part of the record's integrity")
	}
	// The coherent pair validates.
	rec.Runtime = RuntimeCoding
	rec.Outcome = "mode request accepted: desired-mode=ai (the host bridge applies it on its next tick)"
	if err := rec.validate(); err != nil {
		t.Fatalf("coherent coding record rejected: %v", err)
	}
	// Unknown runtime label.
	rec.Runtime = "shell"
	if err := rec.validate(); err == nil {
		t.Error("unknown runtime label accepted")
	}
}

func TestExecutionValidateShapes(t *testing.T) {
	rec := baseExecution()
	bad := rec
	bad.ID = "nope"
	if err := bad.validate(); err == nil {
		t.Error("bad execution id accepted")
	}
	bad = rec
	bad.CommandID = "nope"
	if err := bad.validate(); err == nil {
		t.Error("bad command id accepted")
	}
	bad = rec
	bad.SessionID = "not-aks"
	if err := bad.validate(); err == nil {
		t.Error("bad session shape accepted")
	}
	bad = rec
	bad.WorkspaceID = "not-ws"
	if err := bad.validate(); err == nil {
		t.Error("bad workspace shape accepted")
	}
	bad = rec
	bad.Intent = ""
	if err := bad.validate(); err == nil {
		t.Error("empty intent accepted")
	}
	bad = rec
	bad.ExecutedAt = time.Time{}
	if err := bad.validate(); err == nil {
		t.Error("zero executed_at accepted")
	}
	bad = rec
	bad.State = "HELD"
	if err := bad.validate(); err == nil {
		t.Error("unknown state accepted")
	}
	// A scoped record with a well-formed workspace validates.
	scoped := rec
	scoped.WorkspaceID = string(validWorkspaceID())
	if err := scoped.validate(); err != nil {
		t.Fatalf("scoped record rejected: %v", err)
	}
}
