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

// CanonicalResourceID validates and normalizes a resource ID for a given type.
// P1-05 (v19.2.7): Real parsing — not just prefix check. Validates format
// and returns a canonical form.
//
// Canonical formats:
//   - GPUDevice: "gpu.nvidia.<UUID>" where UUID matches NVIDIA GPU UUID format
//     (GPU-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx). Legacy index IDs rejected.
//   - RuntimeSlot: "runtime.<name>" where name is [a-z0-9_-]+
//   - ModelSlot: "model.<id>" where id is [a-z0-9._/-]+
//   - Port: "port.<num>" where num is 1-65535 (no leading zeros)
func CanonicalResourceID(resourceID string, resourceType ResourceType) (string, error) {
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		return "", fmt.Errorf("resource_id is empty")
	}
	switch resourceType {
	case ResourceTypeGPUDevice:
		// Must start with "gpu.nvidia."
		if !strings.HasPrefix(resourceID, "gpu.nvidia.") {
			return "", fmt.Errorf("gpu resource_id must start with 'gpu.nvidia.' (got %q)", resourceID)
		}
		uuid := strings.TrimPrefix(resourceID, "gpu.nvidia.")
		if uuid == "" {
			return "", fmt.Errorf("gpu resource_id missing UUID after 'gpu.nvidia.'")
		}
		// P1-05: Validate NVIDIA GPU UUID format.
		if !isValidGPUUUID(uuid) {
			return "", fmt.Errorf("gpu resource_id has invalid UUID format %q (expected GPU-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx)", uuid)
		}
		// P1-04 (v19.2.8): Canonicalize to lowercase to prevent case aliases.
		// "GPU-" prefix stays uppercase (NVIDIA convention), hex digits lowercased.
		canonUUID := "GPU-" + strings.ToLower(uuid[4:])
		return "gpu.nvidia." + canonUUID, nil
	case ResourceTypeRuntimeSlot:
		if !strings.HasPrefix(resourceID, "runtime.") {
			return "", fmt.Errorf("runtime resource_id must start with 'runtime.' (got %q)", resourceID)
		}
		name := strings.TrimPrefix(resourceID, "runtime.")
		if name == "" {
			return "", fmt.Errorf("runtime resource_id missing name")
		}
		if !isValidRuntimeName(name) {
			return "", fmt.Errorf("runtime name %q contains invalid characters (allowed: a-z 0-9 _ -)", name)
		}
		return resourceID, nil
	case ResourceTypeModelSlot:
		if !strings.HasPrefix(resourceID, "model.") {
			return "", fmt.Errorf("model resource_id must start with 'model.' (got %q)", resourceID)
		}
		name := strings.TrimPrefix(resourceID, "model.")
		if name == "" {
			return "", fmt.Errorf("model resource_id missing name")
		}
		if !isValidModelName(name) {
			return "", fmt.Errorf("model name %q contains invalid characters (allowed: a-z 0-9 . _ / -)", name)
		}
		return resourceID, nil
	case ResourceTypePort:
		if !strings.HasPrefix(resourceID, "port.") {
			return "", fmt.Errorf("port resource_id must start with 'port.' (got %q)", resourceID)
		}
		portStr := strings.TrimPrefix(resourceID, "port.")
		if portStr == "" {
			return "", fmt.Errorf("port resource_id missing port number")
		}
		// P1-05: Validate port number (1-65535, no leading zeros except "0" which is invalid).
		port, err := parsePortNum(portStr)
		if err != nil {
			return "", fmt.Errorf("port resource_id has invalid port: %w", err)
		}
		// Canonical form: "port.<num>" with no leading zeros.
		return fmt.Sprintf("port.%d", port), nil
	default:
		return "", fmt.Errorf("unknown resource type %q", resourceType)
	}
}

// isValidGPUUUID validates NVIDIA GPU UUID format.
// Format: GPU-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx (hex, case-insensitive)
func isValidGPUUUID(uuid string) bool {
	if len(uuid) != 40 {
		return false
	}
	if !strings.HasPrefix(uuid, "GPU-") {
		return false
	}
	// Check hex segments: 8-4-4-4-12 after "GPU-"
	hexPart := uuid[4:]
	segments := strings.Split(hexPart, "-")
	if len(segments) != 5 {
		return false
	}
	expectedLens := []int{8, 4, 4, 4, 12}
	for i, seg := range segments {
		if len(seg) != expectedLens[i] {
			return false
		}
		for _, c := range seg {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// isValidRuntimeName validates runtime names: [a-z0-9_-]+
func isValidRuntimeName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// isValidModelName validates model names: [a-z0-9._/-]+
func isValidModelName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '/' || c == '-') {
			return false
		}
	}
	return true
}

// parsePortNum parses a port number string, rejecting leading zeros and
// values outside 1-65535.
func parsePortNum(s string) (int, error) {
	if len(s) == 0 {
		return 0, fmt.Errorf("empty port")
	}
	// Reject leading zeros (e.g. "030000").
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("leading zeros not allowed in port number %q", s)
	}
	// Must be all digits.
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-numeric port %q", s)
		}
	}
	port := 0
	for _, c := range s {
		port = port*10 + int(c-'0')
		if port > 65535 {
			return 0, fmt.Errorf("port %d exceeds 65535", port)
		}
	}
	if port < 1 {
		return 0, fmt.Errorf("port must be >= 1 (got %d)", port)
	}
	return port, nil
}
