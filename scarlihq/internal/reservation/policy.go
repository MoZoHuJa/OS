package reservation

import (
	"fmt"
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
// active reservations.
// KPK-01 (v19.2.5): Uses BlocksResource() — RELEASING, EXPIRED, CONFLICT
// also block (was: only HELD blocked).
func (p ReservationPolicy) CanReserveGPU(resourceID string, allReservations []*Reservation, excludeAllocationID string) bool {
	if p.GPUExclusive {
		for _, r := range allReservations {
			// KPK-01: Use BlocksResource() instead of just checking HELD.
			if !r.Status.BlocksResource() {
				continue
			}
			if r.ResourceID == resourceID && r.AllocationID != excludeAllocationID {
				return false
			}
		}
	}
	return true
}

// CanReserveRuntimeSlot checks whether a runtime slot can be reserved.
// KPK-01 (v19.2.5): Uses BlocksResource() for all status checks.
func (p ReservationPolicy) CanReserveRuntimeSlot(runtimeID string, allReservations []*Reservation, excludeAllocationID string) bool {
	count := 0
	for _, r := range allReservations {
		if !r.Status.BlocksResource() {
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
// KPK-01 (v19.2.5): Uses BlocksResource() for all status checks.
func (p ReservationPolicy) CanReserveModelSlot(modelID string, allReservations []*Reservation, excludeAllocationID string) bool {
	count := 0
	for _, r := range allReservations {
		if !r.Status.BlocksResource() {
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

// CanReservePort checks whether a port can be reserved.
// KPK-02 (v19.2.5): New method — port reservations are exclusive by default.
// Ports are identified by a canonical string ID (e.g. "port.30000" or "tcp.127.0.0.1:30000").
func (p ReservationPolicy) CanReservePort(portID string, allReservations []*Reservation, excludeAllocationID string) bool {
	for _, r := range allReservations {
		if !r.Status.BlocksResource() {
			continue
		}
		if r.ResourceType == ResourceTypePort && r.ResourceID == portID {
			if r.AllocationID != excludeAllocationID {
				return false // port already reserved
			}
		}
	}
	return true
}

// Validate checks that the policy configuration is valid.
// KPK-15 (v19.2.5): Reject zero/negative capacities at construction.
func (p ReservationPolicy) Validate() error {
	if p.MaxGPUPerReservation <= 0 {
		return fmt.Errorf("policy: MaxGPUPerReservation must be > 0 (got %d)", p.MaxGPUPerReservation)
	}
	if p.MaxRuntimeSlotsPerRuntime <= 0 {
		return fmt.Errorf("policy: MaxRuntimeSlotsPerRuntime must be > 0 (got %d)", p.MaxRuntimeSlotsPerRuntime)
	}
	if p.MaxModelSlotsPerModel <= 0 {
		return fmt.Errorf("policy: MaxModelSlotsPerModel must be > 0 (got %d)", p.MaxModelSlotsPerModel)
	}
	if p.LeaseMinTTL <= 0 {
		return fmt.Errorf("policy: LeaseMinTTL must be > 0 (got %v)", p.LeaseMinTTL)
	}
	if p.LeaseMaxTTL <= p.LeaseMinTTL {
		return fmt.Errorf("policy: LeaseMaxTTL (%v) must be > LeaseMinTTL (%v)", p.LeaseMaxTTL, p.LeaseMinTTL)
	}
	if p.InventoryFreshnessLimit <= 0 {
		return fmt.Errorf("policy: InventoryFreshnessLimit must be > 0 (got %v)", p.InventoryFreshnessLimit)
	}
	return nil
}
