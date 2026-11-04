package airuntime

import (
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Model registry (explicit, bounded — registration is an operator act)
// ---------------------------------------------------------------------------

// ModelID is a typed, validated model identifier: lowercase [a-z0-9._:-],
// at most 64 runes — the real model naming shapes (e.g. "qwen3-14b",
// "qwen2.5:3b", "llama3.1-8b-q4_k_m"). Empty, over-bound or malformed IDs
// are rejected.
type ModelID string

const maxModelIDLen = 64

// Validate checks the canonical model id shape.
func (m ModelID) Validate() error {
	s := string(m)
	if s == "" {
		return fmt.Errorf("airuntime: model id is empty")
	}
	if utf8.RuneCountInString(s) > maxModelIDLen {
		return fmt.Errorf("airuntime: model id longer than %d runes", maxModelIDLen)
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == ':' || r == '-' {
			continue
		}
		return fmt.Errorf("airuntime: model id %q must be [a-z0-9._:-]", s)
	}
	return nil
}

// DefaultMaxModels bounds the registry: registration is an operator act,
// not a bulk import.
const DefaultMaxModels = 64

var (
	// ErrDuplicateRegistration: the (provider, model) pair is already
	// registered — one registration is one declaration.
	ErrDuplicateRegistration = errors.New("airuntime: provider already serves this model (duplicate registration)")

	// ErrModelLimit: the distinct-model bound is reached.
	ErrModelLimit = errors.New("airuntime: model registry is full (distinct-model bound)")
)

// ModelRegistry is the explicit routing surface: which provider serves
// which model. There is NO auto-discovery — the registry IS the routing
// surface; "provider P serves model M" is an operator declaration. The
// bound counts DISTINCT models: provider fan-out on an already-registered
// model does not consume a new slot (the fan-out is routing information,
// not capacity).
type ModelRegistry struct {
	mu      sync.Mutex
	max     int
	serving map[ModelID][]Provider // model -> providers, kept tier-ordered
	count   int                    // distinct models
}

// NewModelRegistry constructs a bounded registry. maxModels <= 0 is
// rejected (fail-closed — an unbounded registry is never constructed).
func NewModelRegistry(maxModels int) (*ModelRegistry, error) {
	if maxModels <= 0 {
		return nil, fmt.Errorf("airuntime: maxModels must be > 0 (got %d)", maxModels)
	}
	return &ModelRegistry{
		max:     maxModels,
		serving: make(map[ModelID][]Provider),
	}, nil
}

// Register declares "provider p serves model m". The provider and the
// model are validated against their closed shapes; a duplicate pair is
// rejected; the distinct-model bound is enforced (fan-out on an existing
// model does not consume a slot).
func (r *ModelRegistry) Register(p Provider, m ModelID) error {
	if r == nil {
		return fmt.Errorf("airuntime: nil model registry")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	providers := r.serving[m]
	for _, existing := range providers {
		if existing == p {
			return fmt.Errorf("%w: %s × %q", ErrDuplicateRegistration, p, m)
		}
	}
	if len(providers) == 0 && r.count >= r.max {
		return fmt.Errorf("%w: %d models registered (bound %d)", ErrModelLimit, r.count, r.max)
	}
	tier, err := p.Tier()
	if err != nil {
		return err // cannot happen after Validate — honest guard
	}
	// Tier-ordered insert keeps ProvidersFor deterministic without
	// re-sorting on every read.
	inserted := false
	for i, existing := range providers {
		existingTier, terr := existing.Tier()
		if terr != nil {
			return terr // cannot happen after Validate — honest guard
		}
		if tier < existingTier {
			providers = append(providers[:i], append([]Provider{p}, providers[i:]...)...)
			inserted = true
			break
		}
	}
	if !inserted {
		providers = append(providers, p)
	}
	r.serving[m] = providers
	if len(providers) == 1 {
		r.count++
	}
	return nil
}

// ProvidersFor returns the providers serving m, in tier order. An unknown
// model returns an empty slice (the router turns that into
// ErrUnknownModel — never a guess).
func (r *ModelRegistry) ProvidersFor(m ModelID) []Provider {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	providers := r.serving[m]
	out := make([]Provider, len(providers))
	copy(out, providers)
	return out
}

// ServesModel reports whether provider p is registered for model m.
func (r *ModelRegistry) ServesModel(p Provider, m ModelID) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.serving[m] {
		if existing == p {
			return true
		}
	}
	return false
}

// Len returns the number of DISTINCT registered models.
func (r *ModelRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// RegistryEntry is one provider's slice of the registry listing.
type RegistryEntry struct {
	Provider Provider
	Models   []ModelID
}

// List returns the full registry listing, deterministic: providers in
// tier order (DefaultFailoverOrder), each with its models in ascending
// lexical order. Providers serving no models are omitted. Reading is a
// copy — the listing cannot mutate the registry.
func (r *ModelRegistry) List() []RegistryEntry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	byProvider := make(map[Provider][]ModelID)
	for m, providers := range r.serving {
		for _, p := range providers {
			byProvider[p] = append(byProvider[p], m)
		}
	}
	out := make([]RegistryEntry, 0, len(byProvider))
	for _, p := range DefaultFailoverOrder {
		models := byProvider[p]
		if len(models) == 0 {
			continue
		}
		sorted := make([]ModelID, len(models))
		copy(sorted, models)
		// insertion sort: small slices, deterministic ascending order
		for i := 1; i < len(sorted); i++ {
			for j := i; j > 0 && string(sorted[j-1]) > string(sorted[j]); j-- {
				sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
			}
		}
		out = append(out, RegistryEntry{Provider: p, Models: sorted})
	}
	return out
}
