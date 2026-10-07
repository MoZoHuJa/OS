package registry

import (
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

func TestModelRegistry_EmptyByDefault(t *testing.T) {
	m := NewModelRegistry()
	if m.Count() != 0 {
		t.Errorf("new registry should be empty, got count %d", m.Count())
	}
	if len(m.List()) != 0 {
		t.Errorf("empty registry List() should be empty slice, got %d items", len(m.List()))
	}
}

func TestModelRegistry_Register(t *testing.T) {
	m := NewModelRegistry()
	m.Register(inventory.Model{ID: "sglang", Path: "/models/Qwen3-14B-AWQ", Present: true})
	if m.Count() != 1 {
		t.Errorf("after register, count should be 1, got %d", m.Count())
	}
	mdl, ok := m.Inspect("sglang")
	if !ok {
		t.Fatal("Inspect should find registered model")
	}
	if mdl.Path != "/models/Qwen3-14B-AWQ" {
		t.Errorf("path mismatch: got %s", mdl.Path)
	}
}

func TestModelRegistry_Unregister(t *testing.T) {
	m := NewModelRegistry()
	m.Register(inventory.Model{ID: "sglang"})
	m.Unregister("sglang")
	if m.Count() != 0 {
		t.Errorf("after unregister, count should be 0, got %d", m.Count())
	}
}

func TestModelRegistry_InspectNotFound(t *testing.T) {
	m := NewModelRegistry()
	if _, ok := m.Inspect("nonexistent"); ok {
		t.Error("Inspect should return false for nonexistent ID")
	}
}

func TestModelRegistry_HealthForPresentModel(t *testing.T) {
	m := NewModelRegistry()
	m.Register(inventory.Model{ID: "sglang", Present: true})
	h, ok := m.Health("sglang")
	if !ok {
		t.Fatal("Health should return true for registered model")
	}
	if h.State != "healthy" {
		t.Errorf("state should be 'healthy' for present model, got %q", h.State)
	}
}

func TestModelRegistry_HealthForAbsentModel(t *testing.T) {
	m := NewModelRegistry()
	m.Register(inventory.Model{ID: "sglang", Present: false})
	h, ok := m.Health("sglang")
	if !ok {
		t.Fatal("Health should return true for registered model")
	}
	if h.State != "unhealthy" {
		t.Errorf("state should be 'unhealthy' for absent model, got %q", h.State)
	}
}

func TestModelRegistry_HealthNotFound(t *testing.T) {
	m := NewModelRegistry()
	if _, ok := m.Health("nonexistent"); ok {
		t.Error("Health should return false for nonexistent ID")
	}
}

func TestModelRegistry_PresentCount(t *testing.T) {
	m := NewModelRegistry()
	m.Register(inventory.Model{ID: "sglang", Present: true})
	m.Register(inventory.Model{ID: "vllm", Present: true})
	m.Register(inventory.Model{ID: "beellama", Present: false})
	if m.PresentCount() != 2 {
		t.Errorf("expected 2 present models, got %d", m.PresentCount())
	}
}

func TestModelRegistry_ListSortedByID(t *testing.T) {
	m := NewModelRegistry()
	m.Register(inventory.Model{ID: "vllm"})
	m.Register(inventory.Model{ID: "sglang"})
	m.Register(inventory.Model{ID: "beellama"})
	list := m.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(list))
	}
	if list[0].ID != "beellama" {
		t.Errorf("expected beellama first, got %s", list[0].ID)
	}
}

func TestModelRegistry_LoadFromInventory(t *testing.T) {
	m := NewModelRegistry()
	if err := m.LoadFromInventory(); err != nil {
		t.Fatalf("LoadFromInventory failed: %v", err)
	}
	// In sandbox without /models dir, CollectModels returns 0-4 entries depending
	// on whether models.yaml is found. Just verify it doesn't crash.
	_ = m.Count()
}

func TestModelRegistry_IDs(t *testing.T) {
	m := NewModelRegistry()
	m.Register(inventory.Model{ID: "vllm"})
	m.Register(inventory.Model{ID: "sglang"})
	ids := m.IDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d", len(ids))
	}
	if ids[0] != "sglang" || ids[1] != "vllm" {
		t.Errorf("IDs should be sorted: got %v", ids)
	}
}
