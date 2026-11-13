// Package fabric — the control-plane side of the distributed compute
// fabric (v19.9.0, Compute Fabric Phase 5, roadmap §9.3–§9.5 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md,
// design: docs/SCARLIX_COMPUTE_FABRIC_PHASE_5_DISTRIBUTED_DESIGN.md §3–§4).
//
// CHARTER (the non-negotiable contract):
//
//   - This package holds OBSERVATIONAL state only: what the control plane
//     last heard from each enrolled node (inventory snapshot, reachability,
//     latency class). The AUTHORITY (the reservation ledger, admission,
//     the planner) stays in the control plane's existing packages — hub-
//     and-spoke with exactly one ledger (design §1; roadmap §9.5: "Do not
//     run independent schedulers with unsynchronized local ledgers").
//   - Failure semantics (design §4.1, roadmap §9.5): an unreachable node's
//     capacity is UNKNOWN — its reservations are NOT recycled, its last
//     snapshot is RETAINED (never forgotten), and admission on it is
//     refused. A revoked node is quarantined: no new observations accepted,
//     no new work; cleanup only through trusted recovery.
//   - Freshness is evaluated at CONSUMPTION time (the same rule the
//     inventory package applies): the remote window is STRICTER than the
//     local one (decision D5-3/#6: local 60s, remote 30s default).
//   - Reconciliation is a PURE, deterministic comparison (reconcile.go):
//     the agent's reported execution records × the ledger's held
//     reservations → typed decisions. It never releases anything itself —
//     releases go through the single ledger's trusted transition path.
package fabric

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/nodeidentity"
)

// Clock is the injected time source.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// LatencyClass is the operator-declared network class of a node pair path
// (§9.4): A (best, same-tailnet segment), B (acceptable), C (worst /
// unknown). Unknown declarations are treated as C — fail-closed.
type LatencyClass string

const (
	LatencyClassA LatencyClass = "A"
	LatencyClassB LatencyClass = "B"
	LatencyClassC LatencyClass = "C"
)

// NormalizeLatencyClass maps any declaration to the closed set; anything
// unknown (including the empty string) is C.
func NormalizeLatencyClass(s string) LatencyClass {
	switch LatencyClass(strings.ToUpper(strings.TrimSpace(s))) {
	case LatencyClassA:
		return LatencyClassA
	case LatencyClassB:
		return LatencyClassB
	default:
		return LatencyClassC
	}
}

// Rank returns the ordinal used for deterministic ordering (A < B < C).
func (c LatencyClass) Rank() int {
	switch c {
	case LatencyClassA:
		return 0
	case LatencyClassB:
		return 1
	default:
		return 2
	}
}

// Admission-guard error codes (roadmap §11.5's stable machine-readable
// vocabulary; these are the FABRIC layer's codes — the planner carries its
// own copies in its trail).
const (
	CodeNodeQuarantined      = "NODE_QUARANTINED"
	CodeNodeUnreachable      = "NODE_UNREACHABLE"
	CodeInventoryUnavailable = "INVENTORY_UNAVAILABLE"
	CodeInventoryStale       = "INVENTORY_STALE"
)

// Errors — sentinel form for the wiring layer; the Code* constants are the
// wire vocabulary.
var (
	ErrUnknownNode     = errors.New("fabric: unknown node")
	ErrNodeQuarantined = errors.New(CodeNodeQuarantined + ": node is quarantined — no new work; cleanup only via trusted recovery")
	ErrNodeUnreachable = errors.New(CodeNodeUnreachable + ": node capacity is UNKNOWN — admission refused, reservations retained")
	ErrInventoryAbsent = errors.New(CodeInventoryUnavailable + ": no inventory snapshot observed for this node yet")
	ErrInventoryStale  = errors.New(CodeInventoryStale + ": the node's inventory snapshot is stale for admission")
	ErrInvalidSnapshot = errors.New("fabric: invalid inventory snapshot")
	ErrNodeNotActive   = errors.New("fabric: node is not ACTIVE — observations refused")
)

// DefaultRemoteSnapshotMaxAge is the default REMOTE admission freshness
// window (design decision #6: 30s — STRICTER than the local 60s default;
// remote capacity ages in transit before the control plane observes it).
const DefaultRemoteSnapshotMaxAge = 30 * time.Second

// NodeStatusSource answers "is this node ACTIVE in the identity registry?"
// — satisfied by an adapter over internal/nodeidentity. Revocation
// (QUARANTINE) must stop observations HERE too, defense-in-depth under
// the API layer's own authentication gate.
type NodeStatusSource interface {
	NodeActive(nodeID string) bool
}

// identityStatusSource adapts the nodeidentity registry (the production
// seam — main.go wires this; tests provide their own fakes).
type identityStatusSource struct{ reg *nodeidentity.Registry }

// NewIdentityStatusSource builds the production NodeStatusSource over the
// node identity registry: a node is fabric-admissible only while its
// identity record is ACTIVE (enrolled and not quarantined).
func NewIdentityStatusSource(reg *nodeidentity.Registry) NodeStatusSource {
	return identityStatusSource{reg: reg}
}

func (s identityStatusSource) NodeActive(nodeID string) bool {
	view, err := s.reg.Record(nodeID)
	if err != nil {
		return false
	}
	return view.Status == nodeidentity.StatusActive
}

// NodeObservation is one node's OBSERVATIONAL state (a copy — safe to hand
// out). The snapshot is RETAINED when a node goes unreachable: capacity
// unknown ≠ capacity forgotten (design §4.1).
type NodeObservation struct {
	NodeID       string
	Reachable    bool
	LastSeenAt   time.Time
	LatencyClass LatencyClass
	Snapshot     *inventory.InventorySnapshotV2
}

// Registry is the control-plane's observational node registry. Safe for
// concurrent use. It deliberately persists NOTHING: durability of identity
// lives in nodeidentity; observations are re-pollable by definition.
type Registry struct {
	mu        sync.RWMutex
	clock     Clock
	status    NodeStatusSource
	remoteAge time.Duration
	nodes     map[string]*nodeState
}

type nodeState struct {
	observations NodeObservation
}

// NewRegistry constructs the fabric registry. Nil status source is refused
// (fail-closed: a registry that cannot check enrollment cannot gate).
func NewRegistry(clock Clock, status NodeStatusSource, remoteSnapshotMaxAge time.Duration) (*Registry, error) {
	if clock == nil {
		return nil, fmt.Errorf("fabric: clock is required")
	}
	if status == nil {
		return nil, fmt.Errorf("fabric: a node status source is required (fail-closed)")
	}
	if remoteSnapshotMaxAge <= 0 {
		remoteSnapshotMaxAge = DefaultRemoteSnapshotMaxAge
	}
	return &Registry{
		clock:     clock,
		status:    status,
		remoteAge: remoteSnapshotMaxAge,
		nodes:     make(map[string]*nodeState),
	}, nil
}

// ObserveInventory records a node's pushed/polled inventory snapshot:
// reachability flips to true, the snapshot + latency class ride along.
// Refused for unknown/non-ACTIVE nodes (quarantine stops observations).
func (r *Registry) ObserveInventory(nodeID string, snap *inventory.InventorySnapshotV2, latencyClass string) error {
	if snap == nil {
		return ErrInvalidSnapshot
	}
	if snap.ObservedAt.IsZero() {
		return fmt.Errorf("%w: ObservedAt is zero", ErrInvalidSnapshot)
	}
	if !r.status.NodeActive(nodeID) {
		return fmt.Errorf("%w (node %s)", ErrNodeNotActive, nodeID)
	}
	now := r.clock.Now()

	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.nodes[nodeID]
	if st == nil {
		st = &nodeState{}
		r.nodes[nodeID] = st
	}
	st.observations = NodeObservation{
		NodeID:       nodeID,
		Reachable:    true,
		LastSeenAt:   now,
		LatencyClass: NormalizeLatencyClass(latencyClass),
		Snapshot:     snap,
	}
	return nil
}

// MarkUnreachable annotates reachability loss (design §4.1): capacity
// becomes UNKNOWN, the last snapshot is RETAINED, reservations stay HELD.
func (r *Registry) MarkUnreachable(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st := r.nodes[nodeID]; st != nil {
		st.observations.Reachable = false
	}
}

// Observation returns a COPY of one node's observational state.
func (r *Registry) Observation(nodeID string) (NodeObservation, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.nodes[nodeID]
	if !ok {
		return NodeObservation{}, false
	}
	return st.observations, true
}

// Observations returns every node's observational state, sorted by NodeID
// (deterministic — the planner's fabric source consumes this order).
func (r *Registry) Observations() []NodeObservation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]NodeObservation, 0, len(r.nodes))
	ids := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, r.nodes[id].observations)
	}
	return out
}

// AdmissionGuard is the fabric layer's admission gate for ONE node at
// time now: quarantined → unreachable → no-snapshot-yet → stale. nil
// means the node is admissible for capacity evaluation. The planner runs
// its OWN freshness evaluation at plan time (every stage verifies its own
// conditions — the repo's standing rule); this guard serves the reserve/
// admission path.
func (r *Registry) AdmissionGuard(nodeID string, now time.Time) error {
	r.mu.RLock()
	st, ok := r.nodes[nodeID]
	var obs NodeObservation
	if ok {
		obs = st.observations
	}
	r.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w (node %s)", ErrUnknownNode, nodeID)
	}
	if !r.status.NodeActive(nodeID) {
		// Quarantine is observed through the identity registry — the
		// observational record alone would happily keep admitting a node
		// whose credential was revoked seconds ago.
		return fmt.Errorf("%w (node %s)", ErrNodeQuarantined, nodeID)
	}
	if !obs.Reachable {
		return fmt.Errorf("%w (node %s)", ErrNodeUnreachable, nodeID)
	}
	if obs.Snapshot == nil {
		return fmt.Errorf("%w (node %s)", ErrInventoryAbsent, nodeID)
	}
	if age := now.Sub(obs.Snapshot.ObservedAt); age > r.remoteAge {
		return fmt.Errorf("%w (node %s: snapshot %s is %s old, bound %s)", ErrInventoryStale, nodeID, obs.Snapshot.SnapshotID, age.Truncate(time.Second), r.remoteAge)
	}
	return nil
}

// RemoteSnapshotMaxAge exposes the configured remote freshness window.
func (r *Registry) RemoteSnapshotMaxAge() time.Duration { return r.remoteAge }
