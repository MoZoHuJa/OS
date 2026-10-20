package reservation

import (
	"fmt"
)

// bundleValidationStatusSet describes which reservation statuses a bundle
// operation considers compatible. KPK-19.2.12-P2-02: one central validator is
// used by every bundle operation (replay, finalize, renew, reconcile, lease
// repair) so individually valid records can never be treated as one atomic
// bundle unless ALL shared invariants hold.
type bundleValidationStatusSet map[ReservationStatus]bool

// activeBundleStatuses is the set of statuses a bundle member may have for
// finalize/renew/lease-repair operations.
var activeBundleStatuses = bundleValidationStatusSet{
	StatusHeld: true,
}

// validateBundleConsistency enforces the shared invariants of a reservation
// bundle. Every member must agree on:
//
//   - allocation ID (non-empty, matching the expected allocation),
//   - owner (non-empty, identical),
//   - lease ID and lease expiry (identical),
//   - idempotency key (identical, possibly empty),
//   - request fingerprint (identical when set),
//   - bundle size (identical; positive and equal to the member count for
//     multi-record bundles; a zero value is legal only for legacy
//     single-record bundles),
//   - resource identity (no duplicate resource type + ID),
//   - status (each member must be in the allowed set for the operation).
//
// The bundle must be sorted deterministically before validation so error
// messages are stable. Failures are fail-closed: the caller must preserve the
// records for operator repair and must not write either store.
func validateBundleConsistency(bundle []*Reservation, expectedAllocationID string, allowedStatuses bundleValidationStatusSet) error {
	if len(bundle) == 0 {
		return fmt.Errorf("reservation: bundle validation requires at least one record")
	}
	if expectedAllocationID == "" {
		return fmt.Errorf("reservation: bundle validation requires a non-empty allocation id")
	}

	// KPK-19.2.13-P2-04: every member is checked for nil BEFORE the first
	// record is dereferenced. The v19.2.12 version read bundle[0].AllocationID
	// before the in-loop nil check, so a nil FIRST member panicked instead of
	// failing closed. This is a callable validation boundary: malformed input
	// must return a typed error, never a panic.
	for _, member := range bundle {
		if member == nil {
			return fmt.Errorf("reservation: bundle contains a nil record")
		}
	}

	first := bundle[0]
	if first.AllocationID != expectedAllocationID {
		return fmt.Errorf("reservation: bundle member %s belongs to allocation %s, expected %s",
			first.ReservationID, first.AllocationID, expectedAllocationID)
	}

	seenResources := make(map[string]bool, len(bundle))
	for _, member := range bundle {
		if member.AllocationID != expectedAllocationID {
			return fmt.Errorf("reservation: bundle member %s belongs to allocation %s, expected %s",
				member.ReservationID, member.AllocationID, expectedAllocationID)
		}
		if member.Owner == "" {
			return fmt.Errorf("reservation: bundle member %s has an empty owner", member.ReservationID)
		}
		if member.Owner != first.Owner {
			return fmt.Errorf("reservation: bundle member %s owner %q differs from %q",
				member.ReservationID, member.Owner, first.Owner)
		}
		if !member.Status.IsValid() {
			return fmt.Errorf("reservation: bundle member %s has invalid status %q",
				member.ReservationID, member.Status)
		}
		if !allowedStatuses[member.Status] {
			return fmt.Errorf("reservation: bundle member %s has status %s which is not allowed for this operation",
				member.ReservationID, member.Status)
		}
		if member.LeaseID == "" {
			return fmt.Errorf("reservation: bundle member %s has an empty lease id", member.ReservationID)
		}
		if member.LeaseID != first.LeaseID {
			return fmt.Errorf("reservation: bundle member %s lease %q differs from %q",
				member.ReservationID, member.LeaseID, first.LeaseID)
		}
		if !member.LeaseExpiry.Equal(first.LeaseExpiry) {
			return fmt.Errorf("reservation: bundle member %s lease expiry %s differs from %s",
				member.ReservationID, member.LeaseExpiry.UTC(), first.LeaseExpiry.UTC())
		}
		if member.IdempotencyKey != first.IdempotencyKey {
			return fmt.Errorf("reservation: bundle member %s idempotency key differs within the bundle",
				member.ReservationID)
		}
		if member.RequestFingerprint != first.RequestFingerprint {
			return fmt.Errorf("reservation: bundle member %s request fingerprint differs within the bundle",
				member.ReservationID)
		}
		if member.BundleSize != first.BundleSize {
			return fmt.Errorf("reservation: bundle member %s bundle size %d differs from %d",
				member.ReservationID, member.BundleSize, first.BundleSize)
		}
		resourceKey := string(member.ResourceType) + "\x00" + member.ResourceID
		if seenResources[resourceKey] {
			return fmt.Errorf("reservation: bundle contains duplicate resource %s (%s)",
				member.ResourceID, member.ResourceType)
		}
		seenResources[resourceKey] = true
	}

	// Bundle size consistency. A multi-record bundle must carry one positive
	// BundleSize equal to the member count. Legacy single-record bundles may
	// carry zero. Mixed zero/non-zero inside one bundle is always rejected.
	if len(bundle) > 1 {
		if first.BundleSize == 0 {
			return fmt.Errorf("reservation: multi-record bundle mixes legacy zero bundle sizes")
		}
		if first.BundleSize != len(bundle) {
			return fmt.Errorf("reservation: bundle declares size %d but has %d members",
				first.BundleSize, len(bundle))
		}
	} else if first.BundleSize != 0 && first.BundleSize != 1 {
		return fmt.Errorf("reservation: single-record bundle declares size %d",
			first.BundleSize)
	}
	return nil
}

// validateActiveBundle is the convenience wrapper for operations that only
// accept active (HELD) bundles: finalize, renew, lease repair.
func validateActiveBundle(bundle []*Reservation, expectedAllocationID string) error {
	sorted := append([]*Reservation(nil), bundle...)
	sortReservations(sorted)
	return validateBundleConsistency(sorted, expectedAllocationID, activeBundleStatuses)
}
