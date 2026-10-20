package reservationops

import (
	"fmt"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/reservation"
)

// HostInventoryProvider implements reservation.InventoryProvider by composing
// the read-only inventory collectors exactly the way ScarliMonitor does
// (monitor.Monitor.Snapshot): GPU collection + in-place health refinement,
// runtime collection + health refinement, model presence scan. It never
// panics and never returns an error: every collector degrades to an empty
// slice when its source is unavailable (no nvidia-smi, no docker, no
// models.yaml), which yields a snapshot with no reservable resources of that
// kind — requests then fail closed inside the reservation manager instead of
// the provider guessing.
type HostInventoryProvider struct {
	managedPorts []int
	// source tags the snapshot origin. It is folded into the derived
	// inventory generation by the reservation adapter together with the
	// observed resource state, so reusing a constant here can never hide a
	// capacity/health change.
	source string
}

// NewHostInventoryProvider builds the provider. managedPorts must already be
// validated (LoadGovernorConfig does this); ports outside [1,65535] make
// every snapshot invalid, so the constructor re-checks defensively.
func NewHostInventoryProvider(managedPorts []int) (*HostInventoryProvider, error) {
	seen := make(map[int]bool, len(managedPorts))
	for _, port := range managedPorts {
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("reservationops: managed port %d outside [1,65535]", port)
		}
		if seen[port] {
			return nil, fmt.Errorf("reservationops: duplicate managed port %d", port)
		}
		seen[port] = true
	}
	ports := make([]int, len(managedPorts))
	copy(ports, managedPorts)
	return &HostInventoryProvider{managedPorts: ports, source: "reservation-governor"}, nil
}

// GetSnapshot collects a coherent point-in-time inventory view. On an
// internal inconsistency (duplicate GPU UUID display mapping, invalid port)
// it returns an EMPTY snapshot: an unreservable world is the fail-closed
// answer, never a partially-trusted one.
func (p *HostInventoryProvider) GetSnapshot() reservation.InventorySnapshot {
	gpus := inventory.CollectGPUs()
	inventory.CollectGPUHealth(gpus) // mutates gpus[].Healthy in place (monitor pattern)

	runtimes := inventory.CollectRuntimes()
	inventory.CollectRuntimeHealth(runtimes) // mutates runtimes[].Healthy in place

	models := inventory.CollectModels()

	snapshot, err := reservation.NewInventorySnapshotFromInventory(p.source, time.Now(), gpus, runtimes, models, p.managedPorts)
	if err != nil {
		return reservation.NewInventorySnapshot("invalid-empty-fail-closed", time.Now().UTC(), nil)
	}
	return snapshot
}
