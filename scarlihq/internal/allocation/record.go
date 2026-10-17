package allocation

import (
	"time"
)

// SchemaVersion is the Allocation record schema version. v1 is frozen in v19.2.2.
// Future versions may ADD fields (with omitempty) but MUST NOT rename or remove
// existing ones without a v2 migration.
const SchemaVersion = "v1"

// Allocation is the typed lifecycle record for a Compute Fabric resource
// allocation request. It tracks the request from intake through release,
// preserving transition history and cleanup status.
//
// DESIGN PRINCIPLES (per SCARGEN_COMPUTE_FABRIC_PHASE_2_GUIDE.md §5):
//   - Prefer typed fields over arbitrary maps.
//   - NEVER store credentials, API keys, bearer tokens, private keys,
//     authorization headers, or raw prompts.
//   - Do not persist raw shell commands or arbitrary environment variables.
//   - A planned resource is not yet an owned resource (SelectedGPU/Runtime/Model
//     are empty until the allocation reaches RESERVED or later).
//   - Preserve Resource Contract v1 and public API compatibility.
//
// JSON field names are FROZEN in v19.2.2 (stability contract).
type Allocation struct {
	// === Identity ===
	AllocationID   string `json:"allocation_id"`             // stable unique identifier (UUID)
	RequestID      string `json:"request_id,omitempty"`      // correlation ID if available
	IDempotencyKey string `json:"idempotency_key,omitempty"` // only if caller provided one
	SchemaVersion  string `json:"schema_version"`            // always "v1" for now

	// === Contract reference ===
	ContractVersion string `json:"contract_version"` // "v1" (from contract.ResourceContract.Version)
	TaskType        string `json:"task_type"`        // e.g. "coding", "chat"
	Priority        string `json:"priority"`         // e.g. "interactive", "normal"

	// === Lifecycle state ===
	State          State     `json:"state"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	StateChangedAt time.Time `json:"state_changed_at"`

	// === Plan reference (empty until PLANNED) ===
	// SelectedGPU/Runtime/Model are empty until the allocation reaches PLANNED.
	// They represent what the planner RECOMMENDS, not what is owned.
	SelectedGPU     string `json:"selected_gpu,omitempty"`     // e.g. "gpu.nvidia.0"
	SelectedRuntime string `json:"selected_runtime,omitempty"` // e.g. "sglang"
	SelectedModel   string `json:"selected_model,omitempty"`   // e.g. "qwen3-14b-awq"

	// === Resource assignment (empty until RESERVED) ===
	// These represent what is actually owned/allocated. Empty until RESERVED+.
	AssignedGPU     string `json:"assigned_gpu,omitempty"`
	AssignedRuntime string `json:"assigned_runtime,omitempty"`
	AssignedModel   string `json:"assigned_model,omitempty"`
	AssignedPort    int    `json:"assigned_port,omitempty"`

	// === Ownership ===
	// Owner is the trusted principal reference from the existing identity model.
	// NEVER trust a caller-supplied owner ID as proof — the caller must verify ownership.
	Owner string `json:"owner,omitempty"` // e.g. "agent.coder" or API caller identity

	// === Lease metadata (optional, not enforced in Phase 2) ===
	LeaseExpiry time.Time `json:"lease_expiry,omitempty"` // zero = no lease
	LeaseID     string    `json:"lease_id,omitempty"`

	// === Workload reference (empty until STARTING) ===
	// Only populated once a workload actually exists (Phase 5+).
	WorkloadRef string `json:"workload_ref,omitempty"` // e.g. container ID or process PID

	// === Error + cleanup status ===
	LastError       string `json:"last_error,omitempty"`       // sanitized, bounded to 500 chars
	CleanupStatus   string `json:"cleanup_status,omitempty"`   // "pending", "verified", "failed", ""
	CleanupVerified bool   `json:"cleanup_verified,omitempty"` // true only when positively confirmed

	// === Revision / concurrency ===
	// Revision is a simple incrementing counter for optimistic concurrency.
	// The Store increments it on every successful write.
	Revision int64 `json:"revision"`

	// === Transition history (audit trail) ===
	// No omitempty: History must always serialize as [] (even when empty),
	// never null or omitted — matching the project stability contract.
	History []TransitionEvent `json:"history"`
}

// TransitionEvent records a single state transition in the allocation's history.
// Used for audit/observability — never contains secrets.
type TransitionEvent struct {
	FromState  State     `json:"from_state"`
	ToState    State     `json:"to_state"`
	OccurredAt time.Time `json:"occurred_at"`
	Reason     string    `json:"reason,omitempty"` // sanitized, bounded
}

// NewAllocation creates a new Allocation in the REQUESTED state with
// the given identity and contract reference. It initializes timestamps
// and an empty history slice.
//
// The caller is responsible for generating a unique AllocationID (UUID)
// and providing the trusted Owner.
func NewAllocation(allocID, contractVersion, taskType, priority, owner string) *Allocation {
	now := time.Now().UTC()
	return &Allocation{
		AllocationID:    allocID,
		SchemaVersion:   SchemaVersion,
		ContractVersion: contractVersion,
		TaskType:        taskType,
		Priority:        priority,
		State:           StateRequested,
		CreatedAt:       now,
		UpdatedAt:       now,
		StateChangedAt:  now,
		Owner:           owner,
		History:         []TransitionEvent{},
		Revision:        0,
	}
}

// HasSecrets checks whether any user-influenced field contains something that
// looks like a secret. This is a defense-in-depth heuristic check — it should
// always return false for records created through NewAllocation + Transition.
//
// KPK-06 (v19.2.3): Previously this function only checked LastError and a
// subset of patterns in History[].Reason. Now it checks ALL user-influenced
// string fields consistently against the full secret marker set:
//   - LastError
//   - History[].Reason
//   - Owner (could contain echoed input in future)
//   - WorkloadRef (could contain echoed container name/env in future)
//
// Secret markers checked (case-insensitive):
//
//	"token=", "password=", "secret=", "api_key=", "bearer ", "authorization:"
//
// This is a heuristic, not a security boundary. Secrets must never be part
// of the allocation data contract. The check exists to catch accidental
// leaks during development/debugging.
func (a *Allocation) HasSecrets() bool {
	if a == nil {
		return false
	}
	// Check all user-influenced string fields.
	if hasSecretMarker(a.LastError) {
		return true
	}
	if hasSecretMarker(a.Owner) {
		return true
	}
	if hasSecretMarker(a.WorkloadRef) {
		return true
	}
	// Check all history event reasons.
	for _, ev := range a.History {
		if hasSecretMarker(ev.Reason) {
			return true
		}
	}
	return false
}

// hasSecretMarker checks whether s contains any of the secret markers
// (case-insensitive). Used by HasSecrets() to apply the full marker set
// consistently across all fields.
func hasSecretMarker(s string) bool {
	if s == "" {
		return false
	}
	markers := []string{
		"token=", "password=", "secret=", "api_key=",
		"bearer ", "authorization:",
	}
	for _, m := range markers {
		if containsCI(s, m) {
			return true
		}
	}
	return false
}

// containsCI is a case-insensitive substring check.
func containsCI(s, substr string) bool {
	if len(substr) == 0 {
		return false
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a, b := s[i+j], substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
