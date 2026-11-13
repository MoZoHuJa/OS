package nodeidentity

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Replay protection (design §2.3, roadmap §9.2's "replay protection and
// request IDs"): every authenticated mutating node request carries a unique
// request ID + a timestamp; the guard rejects stale timestamps and
// (node, request-ID) replays within a bounded window. Fail-closed.
//
// The guard is deliberately in-memory and per-process: a control-plane
// restart resets it, and that is acceptable because the timestamp freshness
// window (±RequestFreshness) still rejects anything replayed across a
// restart that is older than the window — the design's recorded trade.

var (
	// ErrStaleRequest — the request timestamp is outside the freshness
	// window (±RequestFreshness around now).
	ErrStaleRequest = errors.New("nodeidentity: request timestamp outside the freshness window")
	// ErrReplay — the (node, request-ID) pair was already seen inside the
	// replay window.
	ErrReplay = errors.New("nodeidentity: replayed request ID")
	// ErrReplayGuardOverflow — per-node ID cap exceeded after sweeping
	// expired entries (fail closed rather than forget).
	ErrReplayGuardOverflow = errors.New("nodeidentity: replay guard cap exceeded")
	// ErrInvalidRequestID — empty request ID (mutating requests must carry
	// one).
	ErrInvalidRequestID = errors.New("nodeidentity: request ID is required")
)

// RequestFreshness is the ± timestamp window around "now".
const RequestFreshness = 90 * time.Second

// DefaultReplayWindow is how long a (node, request-ID) pair is remembered.
const DefaultReplayWindow = 10 * time.Minute

// DefaultMaxIDsPerNode caps the per-node remembered IDs (bounded memory).
const DefaultMaxIDsPerNode = 1024

// ReplayGuard rejects request replays. Safe for concurrent use.
type ReplayGuard struct {
	mu     sync.Mutex
	clock  Clock
	window time.Duration
	cap    int
	seen   map[string]map[string]time.Time // nodeID → requestID → expiresAt
}

// NewReplayGuard builds the guard (nil clock → SystemClock; window/cap
// defaults when <= 0).
func NewReplayGuard(clock Clock, window time.Duration, maxPerNode int) *ReplayGuard {
	if clock == nil {
		clock = SystemClock{}
	}
	if window <= 0 {
		window = DefaultReplayWindow
	}
	if maxPerNode <= 0 {
		maxPerNode = DefaultMaxIDsPerNode
	}
	return &ReplayGuard{clock: clock, window: window, cap: maxPerNode, seen: make(map[string]map[string]time.Time)}
}

// Check validates one mutating request from node nodeID: ts must be inside
// ±RequestFreshness of now; requestID must be non-empty and unseen (per
// node) within the window. A first sighting is REMEMBERED (a check that
// forgets is not a guard).
func (g *ReplayGuard) Check(nodeID, requestID string, ts time.Time) error {
	if requestID == "" {
		return ErrInvalidRequestID
	}
	now := g.clock.Now()
	delta := now.Sub(ts)
	if delta < 0 {
		delta = -delta
	}
	if delta > RequestFreshness {
		return fmt.Errorf("%w (|now-ts|=%s > %s)", ErrStaleRequest, delta.Truncate(time.Second), RequestFreshness)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	nodeSeen := g.seen[nodeID]
	if nodeSeen == nil {
		nodeSeen = make(map[string]time.Time)
		g.seen[nodeID] = nodeSeen
	}
	// Sweep expired entries for this node (bounded work; the cap makes the
	// memory bound the real guarantee).
	for id, exp := range nodeSeen {
		if !now.Before(exp) {
			delete(nodeSeen, id)
		}
	}
	if exp, dup := nodeSeen[requestID]; dup && now.Before(exp) {
		return fmt.Errorf("%w (node=%s id seen until %s)", ErrReplay, nodeID, exp.UTC().Format(time.RFC3339))
	}
	if len(nodeSeen) >= g.cap {
		return ErrReplayGuardOverflow
	}
	nodeSeen[requestID] = now.Add(g.window)
	return nil
}
