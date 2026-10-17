package reservation

import (
	"fmt"
	"time"
)

// ResourceType identifies the kind of resource being reserved.
// FROZEN in v19.2.4 — additive only.
type ResourceType string

const (
	// ResourceTypeGPUDevice is an exclusive physical GPU reservation.
	// KPK-11 (v19.2.5): GPU identity should use the NVIDIA GPU UUID,
	// not the index. The canonical ID format is "gpu.nvidia.<uuid>"
	// where <uuid> is the nvidia-smi --query-gpu=gpu_uuid output.
	// Legacy "gpu.nvidia.<index>" IDs are accepted for backward compat
	// but new code should use UUID-based IDs.
	ResourceTypeGPUDevice ResourceType = "gpu.device"

	// ResourceTypeRuntimeSlot is a logical slot for a runtime instance.
	ResourceTypeRuntimeSlot ResourceType = "runtime.slot"

	// ResourceTypeModelSlot is a logical slot for a model+runtime combination.
	ResourceTypeModelSlot ResourceType = "model.slot"

	// ResourceTypePort is a specific host port.
	// KPK-02 (v19.2.5): Now has an explicit policy branch in Reserve().
	ResourceTypePort ResourceType = "port"
)

// IsValid reports whether rt is a known resource type.
func (rt ResourceType) IsValid() bool {
	switch rt {
	case ResourceTypeGPUDevice, ResourceTypeRuntimeSlot, ResourceTypeModelSlot, ResourceTypePort:
		return true
	}
	return false
}

// ReservationStatus represents the lifecycle state of a reservation.
// FROZEN in v19.2.4 — additive only.
type ReservationStatus string

const (
	// StatusHeld means the reservation is active and the resource is owned.
	StatusHeld ReservationStatus = "HELD"

	// StatusReleasing means the reservation is being released (cleanup in progress).
	// KPK-01 (v19.2.5): Still BLOCKS the resource — only RELEASED stops blocking.
	StatusReleasing ReservationStatus = "RELEASING"

	// StatusReleased means the reservation has been positively released.
	// Terminal state. Only this state stops blocking the resource.
	StatusReleased ReservationStatus = "RELEASED"

	// StatusExpiredPendingReconciliation means the lease has expired but the
	// resource has NOT been confirmed free. Requires reconciliation.
	// KPK-01 (v19.2.5): Still BLOCKS the resource.
	StatusExpiredPendingReconciliation ReservationStatus = "EXPIRED_PENDING_RECONCILIATION"

	// StatusConflict means there is an ambiguous ownership state that requires
	// explicit resolution. Fail-closed.
	// KPK-01 (v19.2.5): Still BLOCKS the resource.
	StatusConflict ReservationStatus = "CONFLICT"
)

// IsValid reports whether s is a known reservation status.
func (s ReservationStatus) IsValid() bool {
	switch s {
	case StatusHeld, StatusReleasing, StatusReleased,
		StatusExpiredPendingReconciliation, StatusConflict:
		return true
	}
	return false
}

// IsTerminal reports whether s is a terminal status (no outgoing transitions).
func (s ReservationStatus) IsTerminal() bool {
	return s == StatusReleased
}

// BlocksResource reports whether a reservation in this status should prevent
// other reservations from acquiring the same resource.
//
// KPK-01 (v19.2.5): HELD, RELEASING, EXPIRED_PENDING_RECONCILIATION, and
// CONFLICT all block the resource. Only RELEASED stops blocking.
// P1-02 (v19.2.6): Unknown/empty statuses ALSO block (fail-closed).
// This prevents a malformed/future/persisted-unknown status from silently
// making a resource available.
func (s ReservationStatus) BlocksResource() bool {
	switch s {
	case StatusReleased:
		return false
	case StatusHeld, StatusReleasing, StatusExpiredPendingReconciliation, StatusConflict:
		return true
	default:
		// P1-02: Unknown statuses block (fail-closed).
		// A persisted record with an unknown status might be a future version
		// or a manually altered record — we must NOT make the resource available.
		return true
	}
}

// ReservationSchemaVersion is the schema version. v1 frozen in v19.2.4.
const ReservationSchemaVersion = "v1"

// Reservation is the persistent record of a resource reservation.
// See record.go for field documentation — this is a type alias for clarity.
type Reservation struct {
	// === Identity ===
	ReservationID string `json:"reservation_id"`
	AllocationID  string `json:"allocation_id"`
	SchemaVersion string `json:"schema_version"`

	// === Resource ===
	ResourceID   string       `json:"resource_id"`
	ResourceType ResourceType `json:"resource_type"`

	// === Ownership ===
	// KPK-05 (v19.2.5): Owner is derived from verified identity, not raw string.
	// The Manager accepts an Identity type and derives Owner from it.
	Owner string `json:"owner"`

	// === Lease ===
	LeaseID       string    `json:"lease_id,omitempty"`
	LeaseExpiry   time.Time `json:"lease_expiry,omitempty"`
	LastRenewedAt time.Time `json:"last_renewed_at,omitempty"`

	// === Inventory snapshot reference ===
	InventoryGeneration string `json:"inventory_generation,omitempty"`

	// === Status ===
	Status    ReservationStatus `json:"status"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`

	// === Idempotency ===
	// KPK-06 (v19.2.5): When IdempotencyKey is set, RequestFingerprint MUST be non-empty.
	IdempotencyKey     string `json:"idempotency_key,omitempty"`
	RequestFingerprint string `json:"request_fingerprint,omitempty"`

	// === Audit ===
	LastReason string `json:"last_reason,omitempty"`

	// === Concurrency ===
	Revision int64 `json:"revision"`
}

// NewReservation creates a new reservation in HELD status.
func NewReservation(resID, allocID string, resType ResourceType, resID2, owner string) *Reservation {
	now := time.Now().UTC()
	return &Reservation{
		ReservationID: resID,
		AllocationID:  allocID,
		SchemaVersion: ReservationSchemaVersion,
		ResourceID:    resID2,
		ResourceType:  resType,
		Owner:         owner,
		Status:        StatusHeld,
		CreatedAt:     now,
		UpdatedAt:     now,
		Revision:      0,
	}
}

// IsExpired reports whether the lease has expired.
// KPK-07 (v19.2.5): A reservation with no lease (LeaseExpiry.IsZero()) never
// expires — but this should not happen for ordinary reservations because
// Reserve() now requires a bounded TTL.
func (r *Reservation) IsExpired(now time.Time) bool {
	if r.LeaseExpiry.IsZero() {
		return false
	}
	return now.After(r.LeaseExpiry)
}

// String returns a human-readable summary.
// KPK-13 (v19.2.5): Panic-safe — uses truncateID() helper for short/empty IDs.
func (r *Reservation) String() string {
	if r == nil {
		return "Reservation(nil)"
	}
	return fmt.Sprintf("Reservation{%s alloc=%s resource=%s/%s owner=%s status=%s}",
		truncateID(r.ReservationID), truncateID(r.AllocationID),
		r.ResourceType, r.ResourceID, r.Owner, r.Status)
}

// truncateID returns the first 8 chars of s, or the whole string if shorter.
// KPK-13 (v19.2.5): prevents panic on short/empty IDs.
func truncateID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// CleanupEvidence is typed proof that a resource has been cleaned up.
// KPK-10 (v19.2.5): Replaces bare boolean in ReconcileExpired.
// Must come from a trusted reconciler, not from an untrusted API caller.
type CleanupEvidence struct {
	// ResourceID is the resource that was cleaned up.
	ResourceID string `json:"resource_id"`
	// OperationID is a unique identifier for the cleanup operation.
	OperationID string `json:"operation_id"`
	// ObservedAt is when the cleanup was verified.
	ObservedAt time.Time `json:"observed_at"`
	// Verified is the outcome: true = cleanup confirmed, false = cleanup failed.
	Verified bool `json:"verified"`
	// ReconcilerID is the identity of the trusted reconciler that produced
	// this evidence.
	ReconcilerID string `json:"reconciler_id"`
}

// Identity represents a verified principal identity.
// KPK-05 (v19.2.5): Replaces raw owner string at the domain boundary.
// The Manager accepts this type and derives the Owner string from it.
// Authentication is enforced by the caller (API layer / service layer),
// not by the reservation package itself.
type Identity struct {
	// Principal is the authenticated identity (e.g. "agent.coder", "system.reconciler").
	Principal string `json:"principal"`
	// IsReconciler indicates whether this identity is a trusted system reconciler.
	// KPK-10 (v19.2.5): Only reconcilers can produce CleanupEvidence.
	IsReconciler bool `json:"is_reconciler"`
}

// String returns the principal name.
func (i Identity) String() string {
	return i.Principal
}
