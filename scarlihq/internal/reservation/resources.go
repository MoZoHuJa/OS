package reservation

import (
	"fmt"
	"time"
)

// ResourceType identifies the kind of resource being reserved.
// FROZEN in v19.2.4 — additive only.
type ResourceType string

const (
	ResourceTypeGPUDevice   ResourceType = "gpu.device"
	ResourceTypeRuntimeSlot ResourceType = "runtime.slot"
	ResourceTypeModelSlot   ResourceType = "model.slot"
	ResourceTypePort        ResourceType = "port"
)

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
	StatusHeld                         ReservationStatus = "HELD"
	StatusReleasing                    ReservationStatus = "RELEASING"
	StatusReleased                     ReservationStatus = "RELEASED"
	StatusExpiredPendingReconciliation ReservationStatus = "EXPIRED_PENDING_RECONCILIATION"
	StatusConflict                     ReservationStatus = "CONFLICT"
)

func (s ReservationStatus) IsValid() bool {
	switch s {
	case StatusHeld, StatusReleasing, StatusReleased, StatusExpiredPendingReconciliation, StatusConflict:
		return true
	}
	return false
}

func (s ReservationStatus) IsTerminal() bool {
	return s == StatusReleased
}

// BlocksResource reports whether a reservation in this status should prevent
// other reservations from acquiring the same resource.
// P1-02 (v19.2.6): Unknown/empty statuses ALSO block (fail-closed).
func (s ReservationStatus) BlocksResource() bool {
	switch s {
	case StatusReleased:
		return false
	case StatusHeld, StatusReleasing, StatusExpiredPendingReconciliation, StatusConflict:
		return true
	default:
		return true // P1-02: unknown blocks (fail-closed)
	}
}

const ReservationSchemaVersion = "v1"

// Reservation is the persistent record of a resource reservation.
type Reservation struct {
	ReservationID       string            `json:"reservation_id"`
	AllocationID        string            `json:"allocation_id"`
	SchemaVersion       string            `json:"schema_version"`
	ResourceID          string            `json:"resource_id"`
	ResourceType        ResourceType      `json:"resource_type"`
	Owner               string            `json:"owner"`
	LeaseID             string            `json:"lease_id,omitempty"`
	LeaseExpiry         time.Time         `json:"lease_expiry,omitempty"`
	LastRenewedAt       time.Time         `json:"last_renewed_at,omitempty"`
	InventoryGeneration string            `json:"inventory_generation,omitempty"`
	Status              ReservationStatus `json:"status"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	IdempotencyKey      string            `json:"idempotency_key,omitempty"`
	RequestFingerprint  string            `json:"request_fingerprint,omitempty"`
	LastReason          string            `json:"last_reason,omitempty"`
	Revision            int64             `json:"revision"`
}

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

func (r *Reservation) IsExpired(now time.Time) bool {
	if r.LeaseExpiry.IsZero() {
		return false
	}
	return now.After(r.LeaseExpiry)
}

// String returns a human-readable summary.
// KPK-13: panic-safe with truncateID.
func (r *Reservation) String() string {
	if r == nil {
		return "Reservation(nil)"
	}
	return fmt.Sprintf("Reservation{%s alloc=%s resource=%s/%s owner=%s status=%s}",
		truncateID(r.ReservationID), truncateID(r.AllocationID),
		r.ResourceType, r.ResourceID, r.Owner, r.Status)
}

func truncateID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// ValidateRecord validates a Reservation's invariants.
// P2-03 (v19.2.7): Called on load and before persistence to reject invalid records.
func (r *Reservation) ValidateRecord() error {
	if r == nil {
		return fmt.Errorf("reservation: nil record")
	}
	if r.ReservationID == "" {
		return fmt.Errorf("reservation: reservation_id is empty")
	}
	if r.AllocationID == "" {
		return fmt.Errorf("reservation: allocation_id is empty")
	}
	if r.SchemaVersion != ReservationSchemaVersion {
		return fmt.Errorf("reservation: schema_version %q != %q", r.SchemaVersion, ReservationSchemaVersion)
	}
	if !r.ResourceType.IsValid() {
		return fmt.Errorf("reservation: invalid resource_type %q", r.ResourceType)
	}
	if r.ResourceID == "" {
		return fmt.Errorf("reservation: resource_id is empty")
	}
	if !r.Status.IsValid() {
		return fmt.Errorf("reservation: invalid status %q", r.Status)
	}
	if r.Owner == "" {
		return fmt.Errorf("reservation: owner is empty")
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("reservation: created_at is zero")
	}
	if r.UpdatedAt.IsZero() {
		return fmt.Errorf("reservation: updated_at is zero")
	}
	// Lease consistency: if LeaseExpiry is set, LeaseID must be non-empty.
	if !r.LeaseExpiry.IsZero() && r.LeaseID == "" {
		return fmt.Errorf("reservation: lease_expiry set but lease_id is empty")
	}
	return nil
}

// --- P1-01 (v19.2.7): Unforgeable identity and evidence ---

// Identity represents a verified principal identity.
// P1-01 (v19.2.7): This struct is NOT directly constructible by untrusted callers.
// Use NewIdentity() (package-private) or TrustedIdentityProvider to create
// verified identities. The fields are read-only after construction.
type Identity struct {
	principal    string
	isReconciler bool
}

// Principal returns the authenticated principal name.
func (i Identity) Principal() string { return i.principal }

// IsReconciler reports whether this identity is a trusted system reconciler.
func (i Identity) IsReconciler() bool { return i.isReconciler }

// String returns the principal name.
func (i Identity) String() string { return i.principal }

// newIdentity creates an Identity. Package-private — only Manager and
// TrustedIdentityProvider can create identities.
func newIdentity(principal string, isReconciler bool) Identity {
	return Identity{principal: principal, isReconciler: isReconciler}
}

// NewUserIdentity creates a non-reconciler identity for testing.
// Production code should use TrustedIdentityProvider.
func NewUserIdentity(principal string) Identity {
	return newIdentity(principal, false)
}

// NewReconcilerIdentity creates a reconciler identity for testing.
// Production code should use TrustedIdentityProvider.
func NewReconcilerIdentity(principal string) Identity {
	return newIdentity(principal, true)
}

// TrustedIdentityProvider is the interface for resolving authenticated identities.
// P1-01 (v19.2.7): Production code MUST use this instead of constructing
// Identity directly from request data.
type TrustedIdentityProvider interface {
	// ResolveIdentity resolves an authenticated token/credential to a verified Identity.
	// Returns an error if the token is invalid or the principal is unknown.
	ResolveIdentity(token string) (Identity, error)
}

// CleanupEvidence is typed proof that a resource has been cleaned up.
// P1-01 (v19.2.7): This struct is NOT directly constructible by untrusted callers.
// Use NewCleanupEvidence() (package-private) to create verified evidence.
// The cleanup/reconciliation service creates this ONLY after independently
// verifying that the resource is actually free.
type CleanupEvidence struct {
	resourceID   string
	operationID  string
	observedAt   time.Time
	verified     bool
	reconcilerID string
}

// ResourceID returns the resource that was cleaned up.
func (e CleanupEvidence) ResourceID() string { return e.resourceID }

// OperationID returns the unique cleanup operation identifier.
func (e CleanupEvidence) OperationID() string { return e.operationID }

// ObservedAt returns when the cleanup was verified.
func (e CleanupEvidence) ObservedAt() time.Time { return e.observedAt }

// Verified returns the cleanup outcome.
func (e CleanupEvidence) Verified() bool { return e.verified }

// ReconcilerID returns the identity of the trusted reconciler.
func (e CleanupEvidence) ReconcilerID() string { return e.reconcilerID }

// newCleanupEvidence creates CleanupEvidence. Package-private — only the
// trusted cleanup service should create evidence.
func newCleanupEvidence(resourceID, operationID string, observedAt time.Time, verified bool, reconcilerID string) CleanupEvidence {
	return CleanupEvidence{
		resourceID:   resourceID,
		operationID:  operationID,
		observedAt:   observedAt,
		verified:     verified,
		reconcilerID: reconcilerID,
	}
}

// NewCleanupEvidence creates CleanupEvidence for testing.
// Production code should use the trusted cleanup service.
func NewCleanupEvidence(resourceID, operationID string, observedAt time.Time, verified bool, reconcilerID string) CleanupEvidence {
	return newCleanupEvidence(resourceID, operationID, observedAt, verified, reconcilerID)
}

// --- P1-03 (v19.2.7): Coherent inventory snapshot ---

// InventorySnapshot is a single coherent point-in-time view of inventory.
// P1-03 (v19.2.7): Replaces separate SnapshotGeneration/SnapshotAge/ResourceExists
// calls with one immutable snapshot that ties generation, age, and resource
// health together.
type InventorySnapshot struct {
	Generation string
	AsOf       time.Time
	resources  map[string]bool // resourceID → exists/healthy
}

// ResourceExists checks if a resource exists in this snapshot.
func (s InventorySnapshot) ResourceExists(resourceID string) bool {
	return s.resources[resourceID]
}

// InventoryProvider supplies trusted inventory snapshots.
// P1-03 (v19.2.7): GetSnapshot returns one coherent immutable snapshot.
type InventoryProvider interface {
	// GetSnapshot returns a coherent point-in-time inventory snapshot.
	GetSnapshot() InventorySnapshot
}

// AllocationValidator validates that an allocation is eligible for reservation.
type AllocationValidator interface {
	// ValidateForReservation checks that allocID exists and is eligible.
	// P1-02 (v19.2.7): Called INSIDE the reservation flock critical section.
	ValidateForReservation(allocID string) error
}
