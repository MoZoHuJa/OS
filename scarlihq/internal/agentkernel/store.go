// AK-2: bounded in-memory session store — the runtime half of the Agent
// Kernel. The store owns session lifecycle (via the kernel state machine),
// keeps each session's agent identity + granted capability set together
// (in-memory grants; AK-3 persists them), and stamps every permission
// evaluation with an audit event (persistence lands in AK-4).
//
// Discipline copied from the reservation/allocation stores and the WS-ticket
// store: fail-closed everywhere — RNG failure aborts creation (no
// predictable IDs), the session cap is enforced (no unbounded growth),
// transitions are validated by the kernel state machine, and evaluation
// always runs under the store lock so a decision can never be attributed to
// a state that no longer exists.

package agentkernel

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Sentinel errors for the session store.
var (
	// ErrSessionLimit: the store is at capacity (bounded; callers back off).
	ErrSessionLimit = errors.New("agentkernel: session store at capacity")
	// ErrUnknownSession: no session with the requested ID.
	ErrUnknownSession = errors.New("agentkernel: unknown session")
	// ErrRNG: session-ID generation failed — fail-closed, never predictable.
	ErrRNG = errors.New("agentkernel: RNG unavailable")
)

// SessionRecord is one stored session: the kernel state machine plus the
// agent identity and capability grants that Evaluation runs against.
type SessionRecord struct {
	Session Session
	Agent   AgentIdentity
	Caps    CapabilitySet
}

// Store is a bounded, mutex-guarded session store.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*SessionRecord
	max      int
}

// DefaultMaxSessions caps concurrent agent sessions. Deliberately small:
// sessions are operator-visible bounded contexts, not a general queue
// (same reasoning as maxWSTickets in the API layer).
const DefaultMaxSessions = 128

// NewStore creates a bounded session store. maxSessions must be > 0 — an
// unbounded store is a construction error (fail-closed).
func NewStore(maxSessions int) (*Store, error) {
	if maxSessions <= 0 {
		return nil, fmt.Errorf("agentkernel: maxSessions must be > 0 (got %d)", maxSessions)
	}
	return &Store{
		sessions: make(map[string]*SessionRecord, maxSessions),
		max:      maxSessions,
	}, nil
}

// rngSessionID generates "aks-" + 16 crypto-random bytes hex (35 chars).
// v18.7.4-P0 precedent (WS tickets): RNG failure is returned as an error and
// the caller MUST fail-closed — never issue a predictable ID. It is a
// package-level variable (not a plain function) so tests can inject RNG
// failure and assert the fail-closed path.
var rngSessionID = func() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", ErrRNG
	}
	return "aks-" + hex.EncodeToString(b), nil
}

// Create registers a new session in the CREATED state for the given agent
// and capability set. The agent identity is re-validated (defense in depth —
// callers normally construct it via NewAgentIdentity), and a nil or empty
// capability set is rejected: a session that can evaluate nothing is a
// construction mistake, not a valid state — force the caller to be explicit
// about grants (nothing is granted implicitly).
func (s *Store) Create(agent AgentIdentity, caps CapabilitySet, now time.Time) (SessionRecord, error) {
	if err := agent.ID.Validate(); err != nil {
		return SessionRecord{}, err
	}
	if now.IsZero() {
		return SessionRecord{}, fmt.Errorf("agentkernel: session creation time is required")
	}
	if len(caps) == 0 {
		return SessionRecord{}, fmt.Errorf("agentkernel: capability set is required (nothing is granted implicitly — grant at least one capability explicitly)")
	}
	id, err := rngSessionID()
	if err != nil {
		return SessionRecord{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) >= s.max {
		return SessionRecord{}, ErrSessionLimit
	}
	rec := &SessionRecord{
		Session: Session{
			ID:        id,
			AgentID:   agent.ID,
			State:     SessionCreated,
			CreatedAt: now.UTC(),
			UpdatedAt: now.UTC(),
		},
		Agent: agent,
		Caps:  caps,
	}
	s.sessions[id] = rec
	return rec.copy(), nil
}

// Get returns a copy of the record (callers never see live pointers).
func (s *Store) Get(sessionID string) (SessionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[sessionID]
	if !ok {
		return SessionRecord{}, false
	}
	return rec.copy(), true
}

// List returns all records sorted by CreatedAt then ID — deterministic
// output for the API surface (no map-iteration order leaks).
func (s *Store) List() []SessionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SessionRecord, 0, len(s.sessions))
	for _, rec := range s.sessions {
		out = append(out, rec.copy())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Session.CreatedAt.Equal(out[j].Session.CreatedAt) {
			return out[i].Session.ID < out[j].Session.ID
		}
		return out[i].Session.CreatedAt.Before(out[j].Session.CreatedAt)
	})
	return out
}

// Transition applies a kernel-validated state transition to a stored
// session. Illegal moves, terminal-state violations and backwards clocks are
// rejected by Session.Transition; unknown IDs are rejected here.
func (s *Store) Transition(sessionID string, to SessionState, now time.Time) (SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[sessionID]
	if !ok {
		return SessionRecord{}, ErrUnknownSession
	}
	if err := rec.Session.Transition(to, now); err != nil {
		return SessionRecord{}, err
	}
	return rec.copy(), nil
}

// Evaluate runs the deterministic policy for one capability of one stored
// session and returns the decision together with its audit event. Evaluation
// happens under the store lock: the decision is always attributable to the
// exact session state it was made against (no TOCTOU between read and
// evaluate). Unknown session IDs return ErrUnknownSession — an evaluation
// attempt against a nonexistent session is a client error (the HTTP layer
// reports it), not an attributable kernel decision.
func (s *Store) Evaluate(sessionID string, policy Policy, capability Capability, now time.Time) (AuditEvent, Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[sessionID]
	if !ok {
		return AuditEvent{}, Decision{}, ErrUnknownSession
	}
	d := policy.Evaluate(rec.Agent, &rec.Session, rec.Caps, capability)
	ev, err := NewAuditEvent(now, rec.Agent, &rec.Session, capability, d)
	if err != nil {
		return AuditEvent{}, d, err
	}
	return ev, d, nil
}

// Count returns the number of live sessions (bounded by construction).
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// copy returns a value copy of the record. CapabilitySet is a map — copy it
// so callers cannot mutate grants through a returned record.
func (r *SessionRecord) copy() SessionRecord {
	caps := make(CapabilitySet, len(r.Caps))
	for c := range r.Caps {
		caps[c] = true
	}
	return SessionRecord{
		Session: r.Session,
		Agent:   r.Agent,
		Caps:    caps,
	}
}
