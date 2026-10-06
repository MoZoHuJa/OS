package registry

import (
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

func TestRuntimeRegistry_EmptyByDefault(t *testing.T) {
	r := NewRuntimeRegistry()
	if r.Count() != 0 {
		t.Errorf("new registry should be empty, got count %d", r.Count())
	}
	if len(r.List()) != 0 {
		t.Errorf("empty registry List() should be empty slice, got %d items", len(r.List()))
	}
}

func TestRuntimeRegistry_Register(t *testing.T) {
	r := NewRuntimeRegistry()
	r.Register(inventory.Runtime{ID: "sglang", Version: "v0.4.9", Running: true})
	if r.Count() != 1 {
		t.Errorf("after register, count should be 1, got %d", r.Count())
	}
	rt, ok := r.Inspect("sglang")
	if !ok {
		t.Fatal("Inspect should find registered runtime")
	}
	if rt.Version != "v0.4.9" {
		t.Errorf("version mismatch: got %s", rt.Version)
	}
}

func TestRuntimeRegistry_Unregister(t *testing.T) {
	r := NewRuntimeRegistry()
	r.Register(inventory.Runtime{ID: "sglang"})
	r.Unregister("sglang")
	if r.Count() != 0 {
		t.Errorf("after unregister, count should be 0, got %d", r.Count())
	}
	if _, ok := r.Inspect("sglang"); ok {
		t.Error("Inspect should return false after unregister")
	}
}

func TestRuntimeRegistry_InspectNotFound(t *testing.T) {
	r := NewRuntimeRegistry()
	if _, ok := r.Inspect("nonexistent"); ok {
		t.Error("Inspect should return false for nonexistent ID")
	}
}

func TestRuntimeRegistry_StatusNotFound(t *testing.T) {
	r := NewRuntimeRegistry()
	if _, ok := r.Status("nonexistent"); ok {
		t.Error("Status should return false for nonexistent ID")
	}
}

func TestRuntimeRegistry_StatusForRegisteredWithoutHealth(t *testing.T) {
	r := NewRuntimeRegistry()
	r.Register(inventory.Runtime{ID: "sglang"})
	h, ok := r.Status("sglang")
	if !ok {
		t.Fatal("Status should return true for registered runtime without health probe")
	}
	if h.State != "unknown" {
		t.Errorf("state should be 'unknown' when no health probe, got %q", h.State)
	}
}

func TestRuntimeRegistry_ListSortedByID(t *testing.T) {
	r := NewRuntimeRegistry()
	r.Register(inventory.Runtime{ID: "vllm"})
	r.Register(inventory.Runtime{ID: "sglang"})
	r.Register(inventory.Runtime{ID: "beellama"})
	list := r.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(list))
	}
	// Should be sorted: beellama, sglang, vllm
	if list[0].ID != "beellama" {
		t.Errorf("expected beellama first, got %s", list[0].ID)
	}
	if list[1].ID != "sglang" {
		t.Errorf("expected sglang second, got %s", list[1].ID)
	}
	if list[2].ID != "vllm" {
		t.Errorf("expected vllm third, got %s", list[2].ID)
	}
}

func TestRuntimeRegistry_LoadFromInventory(t *testing.T) {
	r := NewRuntimeRegistry()
	if err := r.LoadFromInventory(); err != nil {
		t.Fatalf("LoadFromInventory failed: %v", err)
	}
	// Should have at least the 5 known runtimes (sglang, vllm, beellama, ollama, litellm)
	// Note: in sandbox without Docker, CollectRuntimes returns all 5 with Running=false
	if r.Count() < 5 {
		t.Errorf("expected at least 5 runtimes from inventory, got %d", r.Count())
	}
}

func TestRuntimeRegistry_IDs(t *testing.T) {
	r := NewRuntimeRegistry()
	r.Register(inventory.Runtime{ID: "vllm"})
	r.Register(inventory.Runtime{ID: "sglang"})
	ids := r.IDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d", len(ids))
	}
	if ids[0] != "sglang" || ids[1] != "vllm" {
		t.Errorf("IDs should be sorted: got %v", ids)
	}
}
