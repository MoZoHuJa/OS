// Package admission — the request/decision contract (roadmap §6.4).
package admission

import "time"

// AdmissionRequest is the explicit ask (roadmap §6.4's shape).
type AdmissionRequest struct {
	// RequestID — the caller's request identity (required non-empty;
	// carried into the record for traceability).
	RequestID string `json:"request_id"`
	// Principal — the authenticated identity asking (operator/AP/principal
	// string; admission records WHO holds what).
	Principal string `json:"principal"`
	// ModelID — the model the workload serves (must have a schema-valid,
	// placement-eligible, ARTIFACT-VERIFIED manifest).
	ModelID string `json:"model_id"`
	// BackendID — the runtime backend that will serve it (must be
	// declared by the manifest AND observed running + healthy in the
	// snapshot — the placement × runtime join).
	BackendID string `json:"backend_id"`
	// Requirements — the resource vector.
	Requirements ResourceVector `json:"requirements"`
	// IdempotencyKey — optional; an identical key within the idempotency
	// window returns the ORIGINAL decision instead of double-admitting.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// AdmissionDecision is the deterministic answer. Every refusal carries a
// machine-readable ReasonCode + a human Explanation naming the numbers
// that failed — never a bare boolean.
type AdmissionDecision struct {
	// Allowed — true iff capacity was reserved.
	Allowed bool `json:"allowed"`
	// ReasonCode — one of the Reason* constants (machine-readable; the
	// CLI/API contract).
	ReasonCode string `json:"reason_code"`
	// Explanation — the numbers and named causes behind the decision.
	Explanation string `json:"explanation"`
	// AdmissionID — the durable record identity (set when allowed).
	AdmissionID string `json:"admission_id,omitempty"`
	// Assignment — the granted resources (set when allowed).
	Assignment *ResourceAssignment `json:"assignment,omitempty"`
	// SnapshotID — the inventory snapshot the decision was made against
	// (provenance — the decision is only as trustworthy as this snapshot).
	SnapshotID string `json:"snapshot_id"`
	// PolicyVersion — the policy that produced the decision.
	PolicyVersion string `json:"policy_version"`
	// LeaseExpiresAt — when the hold self-expires unless renewed/released
	// (set when allowed).
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitempty"`
}

// Reason codes — the closed, stable refusal vocabulary (the decision
// contract; additions are additive, renames are breaking).
const (
	// ReasonOK — capacity reserved.
	ReasonOK = "ok"
	// ReasonRequestInvalid — malformed request (bad identity, invalid
	// vector, non-exclusive GPU ask).
	ReasonRequestInvalid = "request-invalid"
	// ReasonSnapshotUnavailable — the inventory snapshot could not be built.
	ReasonSnapshotUnavailable = "snapshot-unavailable"
	// ReasonSnapshotStale — the snapshot exceeds the freshness policy —
	// HARD REFUSAL (KPK-19.7.0-P2-01: a planner/admission must never
	// consume stale free-VRAM/free-space as current capacity).
	ReasonSnapshotStale = "snapshot-stale"
	// ReasonModelUnknown — no schema-valid manifest declares the model.
	ReasonModelUnknown = "model-unknown"
	// ReasonModelIneligible — the manifest fails validation or the
	// placement gate (metadata incomplete).
	ReasonModelIneligible = "model-ineligible"
	// ReasonModelUnverified — no artifact verification record, the record
	// is not fully verified, or it is stale (KPK-19.7.0-P2-04: the
	// admission gate consumes VERIFIED records, not declared digests).
	ReasonModelUnverified = "model-unverified"
	// ReasonBackendNotDeclared — the manifest does not declare this backend.
	ReasonBackendNotDeclared = "backend-not-declared"
	// ReasonBackendUnavailable — the backend is declared but NOT observed
	// running + healthy in the snapshot (the placement × runtime join).
	ReasonBackendUnavailable = "backend-unavailable"
	// ReasonGPUUnknownUUID — the requested GPU UUID is not in the snapshot.
	ReasonGPUUnknownUUID = "gpu-unknown-uuid"
	// ReasonGPUExclusiveConflict — the GPU is already exclusively held.
	ReasonGPUExclusiveConflict = "gpu-exclusive-conflict"
	// ReasonGPUVRAMExhausted — VRAM (incl. KV) exceeds the allocatable.
	ReasonGPUVRAMExhausted = "gpu-vram-exhausted"
	// ReasonGPUNoCandidates — an any-GPU request found no GPU with capacity.
	ReasonGPUNoCandidates = "gpu-no-candidates"
	// ReasonRAMExhausted — host RAM exceeds the allocatable.
	ReasonRAMExhausted = "ram-exhausted"
	// ReasonPinnedRAMUnavailable — pinned RAM is demanded but the policy
	// admits none (the honest v19.7.1 default — no pinned-RAM ledger yet).
	ReasonPinnedRAMUnavailable = "pinned-ram-unavailable"
	// ReasonCPUExhausted — CPU threads exceed the allocatable.
	ReasonCPUExhausted = "cpu-exhausted"
	// ReasonStorageUnknownDevice — the device ID is not in the snapshot.
	ReasonStorageUnknownDevice = "storage-unknown-device"
	// ReasonStorageExhausted — free bytes exceed the allocatable.
	ReasonStorageExhausted = "storage-exhausted"
	// ReasonStorageNoReadProfile — a measured-performance floor was
	// demanded but the device was never benchmarked (never schedule from
	// unverified estimates — the release's must-not-do, ENFORCED).
	ReasonStorageNoReadProfile = "storage-no-read-profile"
	// ReasonStorageReadProfileInsufficient — the device HAS a measurement
	// and it is below the demanded floor (verified, but not enough).
	ReasonStorageReadProfileInsufficient = "storage-read-profile-insufficient"
	// ReasonPortConflict — the port is already held by another admission.
	ReasonPortConflict = "port-conflict"
	// ReasonLedgerUnavailable — the durable ledger could not persist the
	// hold — the admission is refused and NOTHING was reserved (fail
	// closed: an unpersisted reservation is not a reservation).
	ReasonLedgerUnavailable = "ledger-unavailable"
)

// RecordStatus — the admission lifecycle (a small, honest state machine).
const (
	// StatusHeld — capacity is reserved (the lease is live).
	StatusHeld = "HELD"
	// StatusReleased — explicitly released (verified cleanup).
	StatusReleased = "RELEASED"
	// StatusExpired — the lease expired; reconciliation freed the hold.
	StatusExpired = "EXPIRED"
	// StatusCrashRecovered — the holding process's boot identity is gone;
	// reconstruction released the hold (the recorded decision).
	StatusCrashRecovered = "CRASH_RECOVERED"
)

// refuse builds a refusal decision (allowed=false, the named reason).
func refuse(base AdmissionDecision, code, explanation string) AdmissionDecision {
	base.Allowed = false
	base.ReasonCode = code
	base.Explanation = explanation
	base.AdmissionID = ""
	base.Assignment = nil
	base.LeaseExpiresAt = time.Time{}
	return base
}
