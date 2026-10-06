package registry

import (
	"fmt"
	"sort"
	"sync"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

// ModelRegistry is the normalized Model Registry v1 (v19.1.3).
// It wraps the inventory.CollectModels() collector with explicit lifecycle
// (Register/Unregister) and lookup (Inspect/Health) operations.
//
// Per master guide section 13: scan, register, inspect, health. Does NOT
// move model files — the registry points to existing storage.
//
// Thread-safe: all methods are safe for concurrent use.
type ModelRegistry struct {
	mu      sync.RWMutex
	entries map[string]inventory.Model // keyed by Model.ID
}

// NewModelRegistry creates an empty registry.
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		entries: make(map[string]inventory.Model),
	}
}

// LoadFromInventory populates the registry by calling inventory.CollectModels().
func (m *ModelRegistry) LoadFromInventory() error {
	models := inventory.CollectModels()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make(map[string]inventory.Model, len(models))
	for _, mdl := range models {
		m.entries[mdl.ID] = mdl
	}
	return nil
}

// Register adds or updates a model entry in the registry.
func (m *ModelRegistry) Register(mdl inventory.Model) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[mdl.ID] = mdl
}

// Unregister removes a model entry by ID.
func (m *ModelRegistry) Unregister(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, id)
}

// List returns all registered models, sorted by ID.
// Returns an empty slice (never nil) if the registry is empty.
func (m *ModelRegistry) List() []inventory.Model {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]inventory.Model, 0, len(m.entries))
	for _, mdl := range m.entries {
		result = append(result, mdl)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Inspect returns a single model by ID. Returns false if not found.
func (m *ModelRegistry) Inspect(id string) (inventory.Model, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mdl, ok := m.entries[id]
	return mdl, ok
}

// Health returns a health entry for a model. A model is "healthy" if it is
// present on disk (Present=true). Returns false if the model is not registered.
func (m *ModelRegistry) Health(id string) (inventory.Health, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mdl, ok := m.entries[id]
	if !ok {
		return inventory.Health{}, false
	}
	state := "unhealthy"
	message := "model file not present on disk"
	if mdl.Present {
		state = "healthy"
		message = ""
	}
	return inventory.Health{
		Component: id,
		State:     state,
		Message:   message,
		CheckedAt: "", // could be populated with a timestamp if needed
	}, true
}

// Count returns the number of registered models.
func (m *ModelRegistry) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}

// IDs returns all registered model IDs, sorted.
func (m *ModelRegistry) IDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, 0, len(m.entries))
	for id := range m.entries {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// PresentCount returns the number of models that are actually present on disk.
func (m *ModelRegistry) PresentCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, mdl := range m.entries {
		if mdl.Present {
			count++
		}
	}
	return count
}

// String returns a human-readable summary for debugging.
func (m *ModelRegistry) String() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return fmt.Sprintf("ModelRegistry(%d entries, %d present: %v)", len(m.entries), m.PresentCount(), m.IDs())
}
