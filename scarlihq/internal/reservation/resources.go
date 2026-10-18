package reservation

import (
	"fmt"
	"strings"
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
// P2-03 (v19.2.8): Called on load AND before persistence (append/update).
// P2-03 (v19.2.8): Added canonical resource ID check, UpdatedAt >= CreatedAt,
// and idempotency fingerprint required when key is present.
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
	// P2-03: Validate canonical resource ID.
	// P1-03 (v19.2.9): Enforce that stored ID IS canonical (not just parseable).
	// Reject records where canonical form differs from stored value.
	canonID, err := CanonicalResourceID(r.ResourceID, r.ResourceType)
	if err != nil {
		return fmt.Errorf("reservation: resource_id not canonical: %w", err)
	}
	if canonID != r.ResourceID {
		return fmt.Errorf("reservation: resource_id %q is not canonical (should be %q)", r.ResourceID, canonID)
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
	// P2-03: UpdatedAt must be >= CreatedAt.
	if r.UpdatedAt.Before(r.CreatedAt) {
		return fmt.Errorf("reservation: updated_at before created_at")
	}
	// Lease consistency.
	if !r.LeaseExpiry.IsZero() && r.LeaseID == "" {
		return fmt.Errorf("reservation: lease_expiry set but lease_id is empty")
	}
	// P2-03: If idempotency key is present, fingerprint must be non-empty.
	if r.IdempotencyKey != "" && r.RequestFingerprint == "" {
		return fmt.Errorf("reservation: idempotency_key present but request_fingerprint is empty")
	}
	// NOTE: Lease validation (non-zero expiry for HELD) is enforced by Manager.Reserve(),
	// not by ValidateRecord — store tests create HELD records without leases.
	return nil
}

// --- P1-01 (v19.2.8): Unforgeable identity and evidence ---

// Identity represents a verified principal identity.
// P1-01 (v19.2.8): Exported constructors for privileged identities REMOVED.
// Use NewUserIdentity() for non-privileged identity (safe for export).
// Reconciler identity is ONLY available via TrustedIdentityProvider.ResolveIdentity()
// which is injected into Manager at construction. No public function can create
// a reconciler identity.
type Identity struct {
	principal    string
	isReconciler bool
}

func (i Identity) Principal() string  { return i.principal }
func (i Identity) IsReconciler() bool { return i.isReconciler }
func (i Identity) String() string     { return i.principal }

// NewUserIdentity creates a non-reconciler identity.
// This is safe to export — it CANNOT create a reconciler identity.
func NewUserIdentity(principal string) Identity {
	return Identity{principal: principal, isReconciler: false}
}

// newReconcilerIdentity creates a reconciler identity. Package-private.
// P1-01 (v19.2.8): NOT exported — only TrustedIdentityProvider or test helpers
// (in _test.go) can create this.
func newReconcilerIdentity(principal string) Identity {
	return Identity{principal: principal, isReconciler: true}
}

// TrustedIdentityProvider resolves authenticated identities.
// P1-01 (v19.2.8): Production code MUST inject this into Manager.
// The implementation authenticates the caller and returns an unforgeable Identity.
type TrustedIdentityProvider interface {
	ResolveIdentity(token string) (Identity, error)
}

// CleanupEvidence is typed proof that a resource has been cleaned up.
// P1-01 (v19.2.8): Exported constructor for verified evidence REMOVED.
// Evidence is ONLY created by the trusted cleanup service (injected into Manager).
type CleanupEvidence struct {
	resourceID   string
	operationID  string
	observedAt   time.Time
	verified     bool
	reconcilerID string
}

func (e CleanupEvidence) ResourceID() string    { return e.resourceID }
func (e CleanupEvidence) OperationID() string   { return e.operationID }
func (e CleanupEvidence) ObservedAt() time.Time { return e.observedAt }
func (e CleanupEvidence) Verified() bool        { return e.verified }
func (e CleanupEvidence) ReconcilerID() string  { return e.reconcilerID }

// NewCleanupEvidence creates CleanupEvidence for use by trusted CleanupVerifier implementations.
// P1-01 (v19.2.9): EXPORTED — external CleanupVerifier implementations need this
// to construct the evidence return value. The trust boundary is at the
// CleanupVerifier interface injection point (NewManager rejects nil CleanupVerifier).
// Only code that implements CleanupVerifier and is injected into Manager can
// produce evidence that Manager will accept.
func NewCleanupEvidence(resourceID, operationID string, observedAt time.Time, verified bool, reconcilerID string) CleanupEvidence {
	return CleanupEvidence{
		resourceID:   resourceID,
		operationID:  operationID,
		observedAt:   observedAt,
		verified:     verified,
		reconcilerID: reconcilerID,
	}
}

// CleanupVerifier is the interface for the trusted cleanup service.
// P1-01 (v19.2.8): Production code injects this into Manager. The implementation
// independently verifies that a resource is actually free before creating evidence.
type CleanupVerifier interface {
	// VerifyCleanup checks if the resource identified by resourceID is actually free.
	// Returns CleanupEvidence with Verified=true if cleanup is confirmed.
	VerifyCleanup(resourceID string, reconciler Identity) (CleanupEvidence, error)
}

// --- P1-03 (v19.2.8): Coherent inventory snapshot with exported constructor ---

// InventorySnapshot is a single coherent point-in-time view of inventory.
// P1-03 (v19.2.8): Exported constructor added — providers in other packages
// can now create snapshots with resource data.
type InventorySnapshot struct {
	Generation string
	AsOf       time.Time
	resources  map[string]bool
}

// NewInventorySnapshot creates an InventorySnapshot with a defensive copy
// of the resource map. P1-03 (v19.2.8): Exported so external providers
// can construct snapshots.
// P2-04 (v19.2.9): Resource IDs are canonicalized before storage.
func NewInventorySnapshot(generation string, asOf time.Time, resourceIDs []string) InventorySnapshot {
	m := make(map[string]bool, len(resourceIDs))
	for _, id := range resourceIDs {
		canon := canonicalizeAnyResourceID(id)
		if canon != "" {
			m[canon] = true
		} else {
			m[id] = true
		}
	}
	return InventorySnapshot{
		Generation: generation,
		AsOf:       asOf,
		resources:  m,
	}
}

// canonicalizeAnyResourceID tries to canonicalize a resource ID by detecting
// its type from the prefix. P2-04 (v19.2.9).
func canonicalizeAnyResourceID(id string) string {
	if strings.HasPrefix(id, "gpu.nvidia.") {
		canon, err := CanonicalResourceID(id, ResourceTypeGPUDevice)
		if err == nil {
			return canon
		}
	} else if strings.HasPrefix(id, "runtime.") {
		canon, err := CanonicalResourceID(id, ResourceTypeRuntimeSlot)
		if err == nil {
			return canon
		}
	} else if strings.HasPrefix(id, "model.") {
		canon, err := CanonicalResourceID(id, ResourceTypeModelSlot)
		if err == nil {
			return canon
		}
	} else if strings.HasPrefix(id, "port.") {
		canon, err := CanonicalResourceID(id, ResourceTypePort)
		if err == nil {
			return canon
		}
	}
	return ""
}

// ResourceExists checks if a resource exists in this snapshot.
func (s InventorySnapshot) ResourceExists(resourceID string) bool {
	return s.resources[resourceID]
}

// InventoryProvider supplies trusted inventory snapshots.
type InventoryProvider interface {
	GetSnapshot() InventorySnapshot
}

// AllocationValidator validates that an allocation is eligible for reservation.
// P1-02 (v19.2.8): Called INSIDE the reservation flock critical section.
// P1-02 (v19.2.9): Extended with BeginReservation/EndReservation for atomicity.
// BeginReservation atomically transitions the allocation to RESERVING state,
// preventing terminal transitions during the reservation window.
// EndReservation transitions to RESERVED (success) or rolls back (failure).
type AllocationValidator interface {
	ValidateForReservation(allocID string) error
	// P1-02 (v19.2.9): Atomically claim the allocation for reservation.
	// Must prevent the allocation from transitioning to terminal state
	// until EndReservation is called.
	BeginReservation(allocID string) error
	// P1-02 (v19.2.9): Release the claim. success=true → RESERVED, false → rollback.
	EndReservation(allocID string, success bool) error
}
