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
	// GPU identity uses the canonical "gpu.nvidia.<index>" format from inventory.
	ResourceTypeGPUDevice ResourceType = "gpu.device"

	// ResourceTypeRuntimeSlot is a logical slot for a runtime instance
	// (e.g. "sglang", "vllm"). Default capacity per runtime is 1 (exclusive).
	ResourceTypeRuntimeSlot ResourceType = "runtime.slot"

	// ResourceTypeModelSlot is a logical slot for a model+runtime combination.
	ResourceTypeModelSlot ResourceType = "model.slot"

	// ResourceTypePort is a specific host port. Only managed ports can be reserved.
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
	StatusReleasing ReservationStatus = "RELEASING"

	// StatusReleased means the reservation has been positively released.
	// Terminal state.
	StatusReleased ReservationStatus = "RELEASED"

	// StatusExpiredPendingReconciliation means the lease has expired but the
	// resource has NOT been confirmed free. Requires reconciliation.
	StatusExpiredPendingReconciliation ReservationStatus = "EXPIRED_PENDING_RECONCILIATION"

	// StatusConflict means there is an ambiguous ownership state that requires
	// explicit resolution. Fail-closed.
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

// ReservationSchemaVersion is the schema version. v1 frozen in v19.2.4.
const ReservationSchemaVersion = "v1"

// Reservation is the persistent record of a resource reservation.
// It is separate from the Allocation record (Phase 2) and linked via AllocationID.
//
// DESIGN PRINCIPLES (per Phase 3 guide §8.3):
//   - Never store bearer tokens, API keys, passwords, private keys, raw prompts.
//   - Never store raw shell commands or arbitrary environment variables.
//   - Resource identity uses stable canonical IDs (not GPU index alone).
//   - A planned resource is not yet an owned resource.
//
// JSON field names are FROZEN in v19.2.4 (stability contract).
type Reservation struct {
	// === Identity ===
	ReservationID string `json:"reservation_id"` // unique UUID
	AllocationID  string `json:"allocation_id"`  // link to Allocation record
	SchemaVersion string `json:"schema_version"` // always "v1"

	// === Resource ===
	ResourceID   string       `json:"resource_id"`   // e.g. "gpu.nvidia.0"
	ResourceType ResourceType `json:"resource_type"` // e.g. "gpu.device"

	// === Ownership ===
	Owner string `json:"owner"` // trusted principal from identity model

	// === Lease ===
	LeaseID       string    `json:"lease_id,omitempty"`
	LeaseExpiry   time.Time `json:"lease_expiry,omitempty"` // zero = no lease
	LastRenewedAt time.Time `json:"last_renewed_at,omitempty"`

	// === Inventory snapshot reference ===
	// InventoryGeneration is a timestamp or generation marker from the inventory
	// snapshot used to make the reservation decision. Used to detect stale
	// inventory at reservation time.
	InventoryGeneration string `json:"inventory_generation,omitempty"`

	// === Status ===
	Status    ReservationStatus `json:"status"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`

	// === Idempotency ===
	// IdempotencyKey is set when the caller provides one. Linked to owner.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// RequestFingerprint is a normalized hash of the request parameters.
	// Used to detect idempotency key reuse with different request content.
	RequestFingerprint string `json:"request_fingerprint,omitempty"`

	// === Audit ===
	LastReason string `json:"last_reason,omitempty"` // sanitized, bounded to 500 chars

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
// A reservation with no lease (LeaseExpiry.IsZero()) never expires.
func (r *Reservation) IsExpired(now time.Time) bool {
	if r.LeaseExpiry.IsZero() {
		return false
	}
	return now.After(r.LeaseExpiry)
}

// String returns a human-readable summary.
func (r *Reservation) String() string {
	return fmt.Sprintf("Reservation{%s alloc=%s resource=%s/%s owner=%s status=%s}",
		r.ReservationID[:8], r.AllocationID[:8], r.ResourceType, r.ResourceID, r.Owner, r.Status)
}
