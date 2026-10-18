package reservation

import (
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

func TestNewInventorySnapshotFromInventory_UsesStableGPUUUID(t *testing.T) {
	now := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	snapshot, err := NewInventorySnapshotFromInventory("", now,
		[]inventory.GPU{
			{ID: "gpu.nvidia.0", UUID: "GPU-12345678-ABCD-1234-ABCD-123456789ABC", Vendor: "nvidia", Healthy: true, VRAMFreeMB: 8000},
			{ID: "gpu.nvidia.1", UUID: "", Vendor: "nvidia", Healthy: true, VRAMFreeMB: 16000},
			{ID: "gpu.nvidia.2", UUID: "GPU-87654321-DCBA-4321-DCBA-987654321CBA", Vendor: "nvidia", Healthy: false, VRAMFreeMB: 16000},
		},
		[]inventory.Runtime{{ID: "sglang", Enabled: true, Healthy: true}, {ID: "vllm", Enabled: true, Healthy: false}},
		[]inventory.Model{{ID: "qwen3-14b", Present: true}, {ID: "missing-model", Present: false}},
		[]int{30000},
	)
	if err != nil {
		t.Fatalf("snapshot adapter: %v", err)
	}
	for _, id := range []string{"gpu.nvidia.GPU-12345678-abcd-1234-abcd-123456789abc", "runtime.sglang", "model.qwen3-14b", "port.30000"} {
		if !snapshot.ResourceExists(id) {
			t.Errorf("expected resource %q in snapshot", id)
		}
	}
	for _, id := range []string{"gpu.nvidia.0", "runtime.vllm", "model.missing-model", "port.8080"} {
		if snapshot.ResourceExists(id) {
			t.Errorf("unexpected resource %q in snapshot", id)
		}
	}
	if snapshot.Generation == "" {
		t.Fatal("snapshot generation must be deterministic and non-empty")
	}
}

func TestNewInventorySnapshotFromInventory_GenerationChangesWithCapacity(t *testing.T) {
	now := time.Now().UTC()
	gpu := inventory.GPU{ID: "gpu.nvidia.0", UUID: "GPU-12345678-abcd-1234-abcd-123456789abc", Vendor: "nvidia", Healthy: true, VRAMFreeMB: 8000}
	first, err := NewInventorySnapshotFromInventory("source-generation-1", now, []inventory.GPU{gpu}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	gpu.VRAMFreeMB = 4000
	second, err := NewInventorySnapshotFromInventory("source-generation-1", now, []inventory.GPU{gpu}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation == second.Generation {
		t.Fatal("inventory generation must change when available GPU capacity changes")
	}
}

func TestNewInventorySnapshotFromInventory_RejectsInvalidManagedPort(t *testing.T) {
	_, err := NewInventorySnapshotFromInventory("gen", time.Now().UTC(), nil, nil, nil, []int{0})
	if err == nil {
		t.Fatal("expected invalid managed port error")
	}
}
