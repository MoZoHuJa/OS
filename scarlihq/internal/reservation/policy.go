package reservation

import (
	"time"
)

// ReservationPolicy defines the pure (no-I/O) rules for capacity, exclusivity,
// and lease limits. All decisions are based on the provided data, not on
// external state.
//
// Per Phase 3 guide §7: "Pure rules of capacity, exclusivity, compatibility,
// priority, and lease limits. Without I/O."
type ReservationPolicy struct {
	// MaxGPUPerReservation is the maximum GPUs a single reservation can hold.
	// Default: 1 (exclusive).
	MaxGPUPerReservation int

	// MaxRuntimeSlotsPerRuntime is the maximum concurrent slots per runtime.
	// Default: 1 (exclusive — one SGLang, one vLLM, etc.).
	MaxRuntimeSlotsPerRuntime int

	// MaxModelSlotsPerModel is the maximum concurrent slots per model.
	// Default: 1.
	MaxModelSlotsPerModel int

	// LeaseMinTTL is the minimum allowed lease TTL.
	LeaseMinTTL time.Duration

	// LeaseMaxTTL is the maximum allowed lease TTL.
	LeaseMaxTTL time.Duration

	// InventoryFreshnessLimit is the maximum age of an inventory snapshot
	// before it is considered stale.
	InventoryFreshnessLimit time.Duration

	// GPUExclusive controls whether GPU reservations are exclusive (no sharing).
	// Default: true (Phase 3 policy — GPU sharing/VRAM overcommit is a
	// future explicit feature, not an implicit exception).
	GPUExclusive bool
}

// DefaultPolicy returns the default Phase 3 policy.
func DefaultPolicy() ReservationPolicy {
	return ReservationPolicy{
		MaxGPUPerReservation:      1,
		MaxRuntimeSlotsPerRuntime: 1,
		MaxModelSlotsPerModel:     1,
		LeaseMinTTL:               30 * time.Second,
		LeaseMaxTTL:               1 * time.Hour,
		InventoryFreshnessLimit:   30 * time.Second,
		GPUExclusive:              true,
	}
}

// ValidateLeaseTTL reports whether the given TTL is within the allowed range.
func (p ReservationPolicy) ValidateLeaseTTL(ttl time.Duration) bool {
	if ttl < p.LeaseMinTTL {
		return false
	}
	if ttl > p.LeaseMaxTTL {
		return false
	}
	return true
}

// IsInventoryStale reports whether the given snapshot age exceeds the
// freshness limit.
func (p ReservationPolicy) IsInventoryStale(snapshotAge time.Duration) bool {
	return snapshotAge > p.InventoryFreshnessLimit
}

// CanReserveGPU checks whether a GPU can be reserved given the current
// active reservations. Under the default exclusive policy, a GPU can be
// reserved only if no other HELD reservation exists for it.
//
// Parameters:
//   - resourceID: the GPU resource ID (e.g. "gpu.nvidia.0")
//   - activeReservations: all currently HELD reservations
//   - excludeAllocationID: allocation to exclude from the check (for renewals)
//
// Returns true if the GPU is available for reservation.
func (p ReservationPolicy) CanReserveGPU(resourceID string, activeReservations []*Reservation, excludeAllocationID string) bool {
	if p.GPUExclusive {
		for _, r := range activeReservations {
			if r.Status != StatusHeld {
				continue
			}
			if r.ResourceID == resourceID && r.AllocationID != excludeAllocationID {
				// Another allocation holds this GPU exclusively.
				return false
			}
		}
	}
	return true
}

// CanReserveRuntimeSlot checks whether a runtime slot can be reserved.
func (p ReservationPolicy) CanReserveRuntimeSlot(runtimeID string, activeReservations []*Reservation, excludeAllocationID string) bool {
	count := 0
	for _, r := range activeReservations {
		if r.Status != StatusHeld {
			continue
		}
		if r.ResourceType == ResourceTypeRuntimeSlot && r.ResourceID == runtimeID {
			if r.AllocationID != excludeAllocationID {
				count++
			}
		}
	}
	return count < p.MaxRuntimeSlotsPerRuntime
}

// CanReserveModelSlot checks whether a model slot can be reserved.
func (p ReservationPolicy) CanReserveModelSlot(modelID string, activeReservations []*Reservation, excludeAllocationID string) bool {
	count := 0
	for _, r := range activeReservations {
		if r.Status != StatusHeld {
			continue
		}
		if r.ResourceType == ResourceTypeModelSlot && r.ResourceID == modelID {
			if r.AllocationID != excludeAllocationID {
				count++
			}
		}
	}
	return count < p.MaxModelSlotsPerModel
}
