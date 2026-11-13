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
	// is not fully verified, it is stale (KPK-19.7.0-P2-04: the
	// admission gate consumes VERIFIED records, not declared digests),
	// or it carries no manifest fingerprint (a pre-v19.8.1 record).
	ReasonModelUnverified = "model-unverified"
	// ReasonModelVerificationIdentityMismatch — the verification record
	// is NOT bound to the current manifest identity (v19.8.1,
	// KPK-19.8.0-P1-02: the record's manifest fingerprint differs from
	// the loaded manifest's — a revision/checkpoint/pack change after
	// verification voids the record until re-verification).
	ReasonModelVerificationIdentityMismatch = "model-verification-identity-mismatch"
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
	// ReasonStorageReadProfileStale — the device HAS a benchmark record
	// but it is older than the policy's benchmark max-age (v19.8.1,
	// KPK-19.8.0-SK-P2-04: an arbitrarily old benchmark is a different
	// device's story — re-run the operator benchmark).
	ReasonStorageReadProfileStale = "storage-read-profile-stale"
	// ReasonStorageReadProfileInsufficient — the device HAS a measurement
	// and it is below the demanded floor, its random-read phase is missing
	// while a random floor is demanded, or its p95 exceeds the demanded
	// ceiling (v19.8.1, KPK-19.8.0-P1-01: the FULL measured contract —
	// sequential + random IOPS + p95 — enforced at the final gate).
	ReasonStorageReadProfileInsufficient = "storage-read-profile-insufficient"
	// ReasonPortConflict — the port is already held by another admission.
	ReasonPortConflict = "port-conflict"
	// ReasonLedgerUnavailable — the durable ledger could not persist the
	// hold — the admission is refused and NOTHING was reserved (fail
	// closed: an unpersisted reservation is not a reservation).
	ReasonLedgerUnavailable = "ledger-unavailable"
	// ReasonIdempotencyConflict — the idempotency key was REUSED with a
	// DIFFERENT request (principal, model, backend, or resource vector
	// differ from the original admission — v19.8.1, KPK-19.8.0-P2-04:
	// a replayed key returns the ORIGINAL decision or a NAMED conflict,
	// never another request's allocation wearing this key's name).
	ReasonIdempotencyConflict = "idempotency-conflict"
)

// RecordStatus — the admission lifecycle (a small, honest state machine).
// v19.8.1 (KPK-19.8.0-P2-05) closes the gap the v19.7.1 machine left: a
// HELD admission can be CONFIRMED STARTED (the workload owner's explicit,
// principal-bound act) and becomes ACTIVE — an ACTIVE record does NOT
// expire by lease (its capacity hold ends only by explicit release or
// crash reconciliation, never by a timer alone freeing capacity a
// running workload still consumes).
const (
	// StatusHeld — capacity is reserved (the lease is live; expiry
	// sweeps records that were never confirmed started).
	StatusHeld = "HELD"
	// StatusActive — the workload start was CONFIRMED (principal-bound,
	// idempotent); the hold persists until explicit release or crash
	// recovery. The lease TTL no longer applies.
	StatusActive = "ACTIVE"
	// StatusReleased — explicitly released (verified cleanup).
	StatusReleased = "RELEASED"
	// StatusExpired — the lease expired before a start was confirmed;
	// reconciliation freed the hold (a never-started hold).
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
