// Package registry formalizes the Runtime + Model registries built on top of
// the inventory package (read-only collectors). The registry layer adds:
//   - explicit registration lifecycle (Register/Unregister)
//   - inspect by ID (single-entry lookup)
//   - status by ID (health probe)
//   - a YAML config source for runtime/model definitions
//
// v19.1.2 (Runtime Registry v1) + v19.1.3 (Model Registry v1) per ScaRgeN
// master guide sections 12-13. The registries are read-only data structures —
// no scheduling, no allocation. They normalize what the system HAS so the
// future scheduler (v19.2.x) can match contracts against state.
package registry

import (
	"fmt"
	"sort"
	"sync"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

// RuntimeRegistry is the normalized Runtime Registry v1.
// It wraps the inventory.CollectRuntimes() collector with explicit lifecycle
// (Register/Unregister) and lookup (Inspect/Status) operations.
//
// Thread-safe: all methods are safe for concurrent use.
type RuntimeRegistry struct {
	mu      sync.RWMutex
	entries map[string]inventory.Runtime // keyed by Runtime.ID
	health  map[string]inventory.Health  // keyed by Runtime.ID
}

// NewRuntimeRegistry creates an empty registry.
func NewRuntimeRegistry() *RuntimeRegistry {
	return &RuntimeRegistry{
		entries: make(map[string]inventory.Runtime),
		health:  make(map[string]inventory.Health),
	}
}

// LoadFromInventory populates the registry by calling inventory.CollectRuntimes()
// + inventory.CollectRuntimeHealth(). This is the standard way to snapshot the
// current system state into the registry.
func (r *RuntimeRegistry) LoadFromInventory() error {
	runtimes := inventory.CollectRuntimes()
	healths := inventory.CollectRuntimeHealth(runtimes)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = make(map[string]inventory.Runtime, len(runtimes))
	r.health = make(map[string]inventory.Health, len(healths))
	for _, rt := range runtimes {
		r.entries[rt.ID] = rt
	}
	for _, h := range healths {
		r.health[h.Component] = h
	}
	return nil
}

// Register adds or updates a runtime entry in the registry.
// Used for manual registration (e.g. a new runtime not yet in compose files).
func (r *RuntimeRegistry) Register(rt inventory.Runtime) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[rt.ID] = rt
}

// Unregister removes a runtime entry by ID.
func (r *RuntimeRegistry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, id)
	delete(r.health, id)
}

// List returns all registered runtimes, sorted by ID.
// Returns an empty slice (never nil) if the registry is empty.
func (r *RuntimeRegistry) List() []inventory.Runtime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]inventory.Runtime, 0, len(r.entries))
	for _, rt := range r.entries {
		result = append(result, rt)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Inspect returns a single runtime by ID. Returns false if not found.
func (r *RuntimeRegistry) Inspect(id string) (inventory.Runtime, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.entries[id]
	return rt, ok
}

// Status returns the health of a single runtime by ID. Returns false if not found.
func (r *RuntimeRegistry) Status(id string) (inventory.Health, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Try direct match first (health.Component may be the runtime ID)
	h, ok := r.health[id]
	if ok {
		return h, true
	}
	// Fallback: check if the runtime exists, return a synthetic "unknown" health
	if _, exists := r.entries[id]; exists {
		return inventory.Health{
			Component: id,
			State:     "unknown",
			Message:   "no health probe available",
			CheckedAt: "",
		}, true
	}
	return inventory.Health{}, false
}

// Count returns the number of registered runtimes.
func (r *RuntimeRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// IDs returns all registered runtime IDs, sorted.
func (r *RuntimeRegistry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]string, 0, len(r.entries))
	for id := range r.entries {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// String returns a human-readable summary for debugging.
func (r *RuntimeRegistry) String() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return fmt.Sprintf("RuntimeRegistry(%d entries: %v)", len(r.entries), r.IDs())
}
