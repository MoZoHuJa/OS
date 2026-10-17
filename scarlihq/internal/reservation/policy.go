package reservation

import (
	"fmt"
	"strings"
	"time"
)

// ReservationPolicy defines the pure (no-I/O) rules for capacity, exclusivity,
// and lease limits. All decisions are based on the provided data, not on
// external state.
//
// Per Phase 3 guide §7: "Pure rules of capacity, exclusivity, compatibility,
// priority, and lease limits. Without I/O."
type ReservationPolicy struct {
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
	// Default: true (Phase 3 policy).
	GPUExclusive bool

	// P2-04 (v19.2.6): MaxGPUPerReservation removed — was validated but never
	// enforced. Will be re-added when multi-resource reservations are implemented.
}

// DefaultPolicy returns the default Phase 3 policy.
func DefaultPolicy() ReservationPolicy {
	return ReservationPolicy{
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

// CanReserveGPU checks whether a GPU can be reserved given all reservations.
// KPK-01: Uses BlocksResource() — RELEASING, EXPIRED, CONFLICT, unknown all block.
// P1-05 (v19.2.6): Removed excludeAllocationID — ALL blocking reservations
// count toward exclusivity, regardless of allocation. A single allocation
// cannot bypass exclusivity by reserving the same GPU twice.
func (p ReservationPolicy) CanReserveGPU(resourceID string, allReservations []*Reservation) bool {
	if p.GPUExclusive {
		for _, r := range allReservations {
			if !r.Status.BlocksResource() {
				continue
			}
			if r.ResourceID == resourceID {
				return false
			}
		}
	}
	return true
}

// CanReserveRuntimeSlot checks whether a runtime slot can be reserved.
// P1-05 (v19.2.6): Removed excludeAllocationID — ALL blocking reservations
// count toward capacity. Repeated requests by the same allocation are counted.
func (p ReservationPolicy) CanReserveRuntimeSlot(runtimeID string, allReservations []*Reservation) bool {
	count := 0
	for _, r := range allReservations {
		if !r.Status.BlocksResource() {
			continue
		}
		if r.ResourceType == ResourceTypeRuntimeSlot && r.ResourceID == runtimeID {
			count++
		}
	}
	return count < p.MaxRuntimeSlotsPerRuntime
}

// CanReserveModelSlot checks whether a model slot can be reserved.
// P1-05 (v19.2.6): Removed excludeAllocationID — ALL blocking reservations
// count toward capacity.
func (p ReservationPolicy) CanReserveModelSlot(modelID string, allReservations []*Reservation) bool {
	count := 0
	for _, r := range allReservations {
		if !r.Status.BlocksResource() {
			continue
		}
		if r.ResourceType == ResourceTypeModelSlot && r.ResourceID == modelID {
			count++
		}
	}
	return count < p.MaxModelSlotsPerModel
}

// CanReservePort checks whether a port can be reserved.
// P1-05 (v19.2.6): Removed excludeAllocationID — ALL blocking reservations count.
func (p ReservationPolicy) CanReservePort(portID string, allReservations []*Reservation) bool {
	for _, r := range allReservations {
		if !r.Status.BlocksResource() {
			continue
		}
		if r.ResourceType == ResourceTypePort && r.ResourceID == portID {
			return false
		}
	}
	return true
}

// Validate checks that the policy configuration is valid.
// KPK-15 (v19.2.5): Reject zero/negative capacities at construction.
// P2-04 (v19.2.6): MaxGPUPerReservation removed from validation.
func (p ReservationPolicy) Validate() error {
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

// CanonicalResourceID normalizes a resource ID for a given type.
// P2-03 (v19.2.6): Rejects non-canonical input to prevent alias bypass.
//
// Canonical formats:
//   - GPUDevice: "gpu.nvidia.<uuid>" (must start with "gpu.nvidia.")
//   - RuntimeSlot: "runtime.<name>" (must start with "runtime.")
//   - ModelSlot: "model.<id>" (must start with "model.")
//   - Port: "port.<port>" (must start with "port.")
//
// Returns the canonical ID or an error if the input is not canonical.
func CanonicalResourceID(resourceID string, resourceType ResourceType) (string, error) {
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		return "", fmt.Errorf("resource_id is empty")
	}
	switch resourceType {
	case ResourceTypeGPUDevice:
		if !strings.HasPrefix(resourceID, "gpu.nvidia.") {
			return "", fmt.Errorf("gpu resource_id must start with 'gpu.nvidia.' (got %q)", resourceID)
		}
		uuid := strings.TrimPrefix(resourceID, "gpu.nvidia.")
		if uuid == "" {
			return "", fmt.Errorf("gpu resource_id missing UUID after 'gpu.nvidia.'")
		}
		return resourceID, nil
	case ResourceTypeRuntimeSlot:
		if !strings.HasPrefix(resourceID, "runtime.") {
			return "", fmt.Errorf("runtime resource_id must start with 'runtime.' (got %q)", resourceID)
		}
		return resourceID, nil
	case ResourceTypeModelSlot:
		if !strings.HasPrefix(resourceID, "model.") {
			return "", fmt.Errorf("model resource_id must start with 'model.' (got %q)", resourceID)
		}
		return resourceID, nil
	case ResourceTypePort:
		if !strings.HasPrefix(resourceID, "port.") {
			return "", fmt.Errorf("port resource_id must start with 'port.' (got %q)", resourceID)
		}
		return resourceID, nil
	default:
		return "", fmt.Errorf("unknown resource type %q", resourceType)
	}
}
