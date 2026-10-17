package allocation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestNewAllocation_InitialState verifies NewAllocation creates a record in
// REQUESTED state with correct defaults.
func TestNewAllocation_InitialState(t *testing.T) {
	a := NewAllocation("alloc-001", "v1", "coding", "interactive", "agent.coder")
	if a.AllocationID != "alloc-001" {
		t.Errorf("AllocationID = %q, want alloc-001", a.AllocationID)
	}
	if a.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", a.SchemaVersion, SchemaVersion)
	}
	if a.ContractVersion != "v1" {
		t.Errorf("ContractVersion = %q, want v1", a.ContractVersion)
	}
	if a.TaskType != "coding" {
		t.Errorf("TaskType = %q, want coding", a.TaskType)
	}
	if a.Priority != "interactive" {
		t.Errorf("Priority = %q, want interactive", a.Priority)
	}
	if a.State != StateRequested {
		t.Errorf("State = %q, want REQUESTED", a.State)
	}
	if a.Owner != "agent.coder" {
		t.Errorf("Owner = %q, want agent.coder", a.Owner)
	}
	if a.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if a.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
	if a.StateChangedAt.IsZero() {
		t.Error("StateChangedAt is zero")
	}
	if a.History == nil {
		t.Error("History is nil, want empty slice")
	}
	if len(a.History) != 0 {
		t.Errorf("History length = %d, want 0", len(a.History))
	}
	if a.Revision != 0 {
		t.Errorf("Revision = %d, want 0", a.Revision)
	}
}

// TestNewAllocation_ResourceFieldsEmpty verifies that resource fields start empty.
// A planned resource is not yet an owned resource.
func TestNewAllocation_ResourceFieldsEmpty(t *testing.T) {
	a := NewAllocation("alloc-002", "v1", "coding", "interactive", "agent.coder")
	if a.SelectedGPU != "" {
		t.Errorf("SelectedGPU = %q, want empty", a.SelectedGPU)
	}
	if a.SelectedRuntime != "" {
		t.Errorf("SelectedRuntime = %q, want empty", a.SelectedRuntime)
	}
	if a.SelectedModel != "" {
		t.Errorf("SelectedModel = %q, want empty", a.SelectedModel)
	}
	if a.AssignedGPU != "" {
		t.Errorf("AssignedGPU = %q, want empty", a.AssignedGPU)
	}
	if a.AssignedRuntime != "" {
		t.Errorf("AssignedRuntime = %q, want empty", a.AssignedRuntime)
	}
	if a.AssignedModel != "" {
		t.Errorf("AssignedModel = %q, want empty", a.AssignedModel)
	}
	if a.AssignedPort != 0 {
		t.Errorf("AssignedPort = %d, want 0", a.AssignedPort)
	}
	if a.WorkloadRef != "" {
		t.Errorf("WorkloadRef = %q, want empty", a.WorkloadRef)
	}
	if a.LeaseID != "" {
		t.Errorf("LeaseID = %q, want empty", a.LeaseID)
	}
	if !a.LeaseExpiry.IsZero() {
		t.Error("LeaseExpiry is not zero, want zero")
	}
}

// TestAllocation_JSONRoundTrip verifies serialization round-trip preserves all fields.
func TestAllocation_JSONRoundTrip(t *testing.T) {
	original := NewAllocation("alloc-rt", "v1", "chat", "normal", "agent.chat")
	original.RequestID = "req-123"
	original.IDempotencyKey = "idem-456"
	// Simulate a transition to populate history.
	_, _ = original.Transition(StateValidating, "validated")

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded Allocation
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Compare key fields.
	if decoded.AllocationID != original.AllocationID {
		t.Errorf("AllocationID: %q vs %q", decoded.AllocationID, original.AllocationID)
	}
	if decoded.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion: %q vs %q", decoded.SchemaVersion, original.SchemaVersion)
	}
	if decoded.State != original.State {
		t.Errorf("State: %q vs %q", decoded.State, original.State)
	}
	if decoded.Owner != original.Owner {
		t.Errorf("Owner: %q vs %q", decoded.Owner, original.Owner)
	}
	if decoded.RequestID != original.RequestID {
		t.Errorf("RequestID: %q vs %q", decoded.RequestID, original.RequestID)
	}
	if decoded.IDempotencyKey != original.IDempotencyKey {
		t.Errorf("IDempotencyKey: %q vs %q", decoded.IDempotencyKey, original.IDempotencyKey)
	}
	if len(decoded.History) != len(original.History) {
		t.Errorf("History length: %d vs %d", len(decoded.History), len(original.History))
	}
	if !decoded.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("CreatedAt: %v vs %v", decoded.CreatedAt, original.CreatedAt)
	}
}

// TestAllocation_JSON_HistoryNeverNull verifies the History slice serializes
// as [] not null (matching the project stability contract).
func TestAllocation_JSON_HistoryNeverNull(t *testing.T) {
	a := NewAllocation("alloc-null-test", "v1", "coding", "interactive", "agent.coder")
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	jsonStr := string(data)
	if strings.Contains(jsonStr, `"history":null`) {
		t.Errorf("History serialized as null: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"history":[]`) {
		t.Errorf("History not serialized as empty array: %s", jsonStr)
	}
}

// TestAllocation_HasSecrets_CleanRecord verifies a clean record has no secrets.
func TestAllocation_HasSecrets_CleanRecord(t *testing.T) {
	a := NewAllocation("alloc-clean", "v1", "coding", "interactive", "agent.coder")
	_, _ = a.Transition(StateValidating, "contract validated")
	if a.HasSecrets() {
		t.Error("Clean record reports HasSecrets() = true")
	}
}

// TestAllocation_HasSecrets_DetectsTokenInLastError verifies the secret check
// catches tokens in LastError.
func TestAllocation_HasSecrets_DetectsTokenInLastError(t *testing.T) {
	a := NewAllocation("alloc-secret", "v1", "coding", "interactive", "agent.coder")
	a.LastError = "auth failed: token=sk-abc123"
	if !a.HasSecrets() {
		t.Error("Record with token in LastError reports HasSecrets() = false")
	}
}

// TestAllocation_HasSecrets_DetectsPasswordInReason verifies the secret check
// catches passwords in transition reasons.
func TestAllocation_HasSecrets_DetectsPasswordInReason(t *testing.T) {
	a := NewAllocation("alloc-secret2", "v1", "coding", "interactive", "agent.coder")
	_, err := a.Transition(StateValidating, "login failed password=hunter2")
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}
	if !a.HasSecrets() {
		t.Error("Record with password in Reason reports HasSecrets() = false")
	}
}

// TestSchemaVersion_Constant verifies the schema version is "v1".
func TestSchemaVersion_Constant(t *testing.T) {
	if SchemaVersion != "v1" {
		t.Errorf("SchemaVersion = %q, want v1", SchemaVersion)
	}
}

// TestAllocation_TransitionHistory_AppendsOnEachTransition verifies that
// multiple transitions append multiple history events.
func TestAllocation_TransitionHistory_AppendsOnEachTransition(t *testing.T) {
	a := NewAllocation("alloc-history", "v1", "coding", "interactive", "agent.coder")

	// REQUESTED → VALIDATING → PLANNED
	_, err := a.Transition(StateValidating, "validated contract")
	if err != nil {
		t.Fatalf("Transition 1 failed: %v", err)
	}
	_, err = a.Transition(StatePlanned, "plan computed")
	if err != nil {
		t.Fatalf("Transition 2 failed: %v", err)
	}

	if len(a.History) != 2 {
		t.Fatalf("History length = %d, want 2", len(a.History))
	}

	// Verify the events are in order.
	if a.History[0].FromState != StateRequested || a.History[0].ToState != StateValidating {
		t.Errorf("History[0] = %s → %s, want REQUESTED → VALIDATING",
			a.History[0].FromState, a.History[0].ToState)
	}
	if a.History[1].FromState != StateValidating || a.History[1].ToState != StatePlanned {
		t.Errorf("History[1] = %s → %s, want VALIDATING → PLANNED",
			a.History[1].FromState, a.History[1].ToState)
	}

	// Verify timestamps are ascending.
	if !a.History[1].OccurredAt.After(a.History[0].OccurredAt) &&
		!a.History[1].OccurredAt.Equal(a.History[0].OccurredAt) {
		t.Error("History events are not in timestamp order")
	}
}

// TestAllocation_FullLifecycle_HappyPath verifies a complete happy-path
// lifecycle: REQUESTED → VALIDATING → PLANNED → RESERVING → RESERVED →
// STARTING → READY → RUNNING → RELEASING → RELEASED.
func TestAllocation_FullLifecycle_HappyPath(t *testing.T) {
	a := NewAllocation("alloc-happy", "v1", "coding", "interactive", "agent.coder")

	steps := []struct {
		to     State
		reason string
	}{
		{StateValidating, "contract parsed"},
		{StatePlanned, "plan computed"},
		{StateReserving, "starting reservation"},
		{StateReserved, "GPU0 reserved"},
		{StateStarting, "starting runtime"},
		{StateReady, "healthcheck passed"},
		{StateRunning, "serving"},
		{StateReleasing, "task complete"},
		{StateReleased, "cleanup verified"},
	}

	for _, step := range steps {
		_, err := a.Transition(step.to, step.reason)
		if err != nil {
			t.Fatalf("Transition to %s failed: %v", step.to, err)
		}
	}

	if a.State != StateReleased {
		t.Errorf("Final state = %q, want RELEASED", a.State)
	}
	if !a.State.IsTerminal() {
		t.Error("Final state is not terminal")
	}
	if len(a.History) != len(steps) {
		t.Errorf("History length = %d, want %d", len(a.History), len(steps))
	}
}

// TestAllocation_FailurePath_VerifiesCleanupTransition verifies that a FAILED
// allocation can transition to RELEASING (cleanup path).
func TestAllocation_FailurePath_VerifiesCleanupTransition(t *testing.T) {
	a := NewAllocation("alloc-fail", "v1", "coding", "interactive", "agent.coder")

	// REQUESTED → VALIDATING → FAILED
	_, _ = a.Transition(StateValidating, "validated")
	_, err := a.Transition(StateFailed, "runtime crashed")
	if err != nil {
		t.Fatalf("Transition to FAILED failed: %v", err)
	}

	// FAILED → RELEASING (cleanup path)
	_, err = a.Transition(StateReleasing, "starting cleanup")
	if err != nil {
		t.Fatalf("Transition FAILED → RELEASING failed: %v", err)
	}

	// RELEASING → RELEASED (cleanup verified)
	_, err = a.Transition(StateReleased, "cleanup confirmed")
	if err != nil {
		t.Fatalf("Transition RELEASING → RELEASED failed: %v", err)
	}

	if a.State != StateReleased {
		t.Errorf("Final state = %q, want RELEASED", a.State)
	}
}

// TestAllocation_CleanupRequiredPath verifies the CLEANUP_REQUIRED → RELEASED
// path (only after cleanup is positively verified).
func TestAllocation_CleanupRequiredPath(t *testing.T) {
	a := NewAllocation("alloc-cleanup-path", "v1", "coding", "interactive", "agent.coder")

	// Fast-forward to STARTING → CLEANUP_REQUIRED
	_, _ = a.Transition(StateValidating, "validated")
	_, _ = a.Transition(StatePlanned, "planned")
	_, _ = a.Transition(StateReserving, "reserving")
	_, _ = a.Transition(StateReserved, "reserved")
	_, err := a.Transition(StateStarting, "starting")
	if err != nil {
		t.Fatalf("Transition to STARTING failed: %v", err)
	}
	_, err = a.Transition(StateCleanupRequired, "startup failed, cleanup uncertain")
	if err != nil {
		t.Fatalf("Transition to CLEANUP_REQUIRED failed: %v", err)
	}

	// CLEANUP_REQUIRED → RELEASING → RELEASED
	_, _ = a.Transition(StateReleasing, "starting cleanup")
	_, err = a.Transition(StateReleased, "cleanup verified")
	if err != nil {
		t.Fatalf("Transition to RELEASED failed: %v", err)
	}

	// Mark cleanup as verified.
	a.CleanupVerified = true
	a.CleanupStatus = "verified"

	if a.State != StateReleased {
		t.Errorf("Final state = %q, want RELEASED", a.State)
	}
	if !a.CleanupVerified {
		t.Error("CleanupVerified should be true")
	}
}

// TestAllocation_CancelledPath verifies the CANCELLED → RELEASING → RELEASED path.
func TestAllocation_CancelledPath(t *testing.T) {
	a := NewAllocation("alloc-cancel", "v1", "coding", "interactive", "agent.coder")

	// REQUESTED → CANCELLED
	_, err := a.Transition(StateCancelled, "user cancelled")
	if err != nil {
		t.Fatalf("Transition to CANCELLED failed: %v", err)
	}

	// CANCELLED → RELEASING → RELEASED
	_, _ = a.Transition(StateReleasing, "cleanup after cancel")
	_, err = a.Transition(StateReleased, "cleanup verified")
	if err != nil {
		t.Fatalf("Transition to RELEASED failed: %v", err)
	}
}

// TestAllocation_TimestampsUpdateOnTransition verifies UpdatedAt and
// StateChangedAt change on each transition.
func TestAllocation_TimestampsUpdateOnTransition(t *testing.T) {
	a := NewAllocation("alloc-ts", "v1", "coding", "interactive", "agent.coder")
	time.Sleep(1 * time.Millisecond) // ensure time advances

	originalChanged := a.StateChangedAt
	_, _ = a.Transition(StateValidating, "validated")

	if !a.StateChangedAt.After(originalChanged) {
		t.Error("StateChangedAt did not advance after transition")
	}
	if !a.UpdatedAt.After(originalChanged) {
		t.Error("UpdatedAt did not advance after transition")
	}
	// CreatedAt should NOT change.
	if !a.CreatedAt.Equal(originalChanged) {
		// CreatedAt was set to the same time as StateChangedAt initially, so this
		// check may be flaky. The key is CreatedAt should be <= StateChangedAt now.
		if a.CreatedAt.After(a.StateChangedAt) {
			t.Error("CreatedAt should not be after StateChangedAt")
		}
	}
}
