package reservation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

// NewInventorySnapshotFromInventory adapts the read-only inventory to the
// reservation domain. GPU reservation IDs use stable hardware UUIDs; display
// indices (gpu.nvidia.0) are not stable enough to be ownership identifiers.
// Only healthy/enabled runtimes and present models are exposed as reservable.
// Port resources are included only when explicitly supplied by trusted config.
func NewInventorySnapshotFromInventory(
	generation string,
	asOf time.Time,
	gpus []inventory.GPU,
	runtimes []inventory.Runtime,
	models []inventory.Model,
	managedPorts []int,
) (InventorySnapshot, error) {
	if asOf.IsZero() {
		return InventorySnapshot{}, fmt.Errorf("reservation: inventory snapshot timestamp is required")
	}
	ids := make([]string, 0, len(gpus)+len(runtimes)+len(models)+len(managedPorts))
	for _, gpu := range gpus {
		if !strings.EqualFold(gpu.Vendor, "nvidia") || gpu.UUID == "" || !gpu.Healthy {
			continue
		}
		canonical, err := CanonicalResourceID("gpu.nvidia."+gpu.UUID, ResourceTypeGPUDevice)
		if err != nil {
			// Fail closed for this GPU. A malformed or missing UUID must never
			// fall back to the mutable display index.
			continue
		}
		ids = append(ids, canonical)
	}
	for _, runtime := range runtimes {
		if !runtime.Enabled || !runtime.Healthy {
			continue
		}
		canonical, err := CanonicalResourceID("runtime."+strings.ToLower(runtime.ID), ResourceTypeRuntimeSlot)
		if err == nil {
			ids = append(ids, canonical)
		}
	}
	for _, model := range models {
		if !model.Present {
			continue
		}
		canonical, err := CanonicalResourceID("model."+strings.ToLower(model.ID), ResourceTypeModelSlot)
		if err == nil {
			ids = append(ids, canonical)
		}
	}
	for _, port := range managedPorts {
		if port < 1 || port > 65535 {
			return InventorySnapshot{}, fmt.Errorf("reservation: managed port %d outside [1,65535]", port)
		}
		canonical, err := CanonicalResourceID("port."+strconv.Itoa(port), ResourceTypePort)
		if err != nil {
			return InventorySnapshot{}, err
		}
		ids = append(ids, canonical)
	}
	sort.Strings(ids)
	ids = uniqueStrings(ids)
	if generation == "" {
		h := sha256.New()
		for _, id := range ids {
			fmt.Fprintf(h, "%d:%s", len(id), id)
		}
		generation = hex.EncodeToString(h.Sum(nil))
	}
	return NewInventorySnapshot(generation, asOf.UTC(), ids), nil
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}
