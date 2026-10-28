// BF-3 (v19.5.2): the fabric event bus — the change-signal backbone for
// the /ws/bar push surface. Contract:
//
//   - CHANGE SIGNALS, NOT STATE: the bus carries "command.submitted",
//     "command.routed", "command.decided", "command.cancelled",
//     "execution.recorded" — one-line notifications that something
//     changed. The payload is a TYPE + a REFERENCE (the id) + a
//     timestamp, never the object itself: the WS client refetches
//     through the auth-gated REST surface (the state authority). A
//     signal that carried state would be a second, drift-prone copy of
//     the state — deliberately not built.
//   - BOUNDED 16×16, DROP-NOT-BLOCK: at most 16 subscribers, each with a
//     16-slot buffered channel. Publish NEVER blocks the caller (the
//     REST handlers must not stall on a slow WS client): a full
//     subscriber queue silently DROPS the signal and counts the drop
//     (Dropped()). A dropped change signal costs a delayed refetch —
//     the 5s polling fallback already covers that failure mode.
//   - NO REPLAY: a new subscriber receives nothing that happened before
//     it subscribed (its first authoritative state comes from the REST
//     refetch on subscribe; the bus is a wake-up channel, not a log).
//     The control frame "fabric.subscribed" (sent by the WS layer, not
//     the bus) is the client's cue.
//
// The bus is constructed by main.go when the fabric is enabled and passed
// both to the API handler (publisher) and the WS surface (subscriber). A
// nil bus is legal: the WS layer answers 503 before upgrade (the surface
// exists, the fabric is off — no silent degradation).
package agentfabric

import (
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Signal model
// ---------------------------------------------------------------------------

// SignalType is a closed enumeration of change signals (the smoke check
// pins this set — extend deliberately).
type SignalType string

const (
	// SignalCommandSubmitted: a new intent was captured (SUBMITTED).
	SignalCommandSubmitted SignalType = "command.submitted"
	// SignalCommandRouted: a command was (re-)bound to a session.
	SignalCommandRouted SignalType = "command.routed"
	// SignalCommandDecided: a command reached DECIDED (allow or deny).
	SignalCommandDecided SignalType = "command.decided"
	// SignalCommandCancelled: a live command was withdrawn.
	SignalCommandCancelled SignalType = "command.cancelled"
	// SignalExecutionRecorded: a terminal execution record was persisted
	// (COMPLETED / FAILED / SKIPPED).
	SignalExecutionRecorded SignalType = "execution.recorded"
	// SignalApprovalRequested: a coding act's execution needs an
	// operator approval — a PENDING approval request was persisted
	// (BF-4; the human gate for mutating acts).
	SignalApprovalRequested SignalType = "approval.requested"
	// SignalApprovalDecided: an operator decision (APPROVED / REJECTED —
	// the closed two-way terminal) closed an approval request. There is
	// deliberately NO withdrawn state: a PENDING request's command is
	// DECIDED (terminal), so the request stays answerable until decided.
	SignalApprovalDecided SignalType = "approval.decided"
)

// Signal is one change notification: what changed (Type), which record
// (Ref — the command or execution id), when (At). Nothing else — the
// client refetches state through the REST surface.
type Signal struct {
	Type SignalType `json:"type"`
	Ref  string     `json:"ref"`
	At   time.Time  `json:"at"`
}

// ---------------------------------------------------------------------------
// Bus bounds
// ---------------------------------------------------------------------------

const (
	// MaxBusSubscribers bounds the subscriber count (each holds a channel
	// — real state, not free; 16 mirrors the WS slot budget).
	MaxBusSubscribers = 16
	// BusSubscriberQueue bounds one subscriber's signal queue (16 slots —
	// a burst beyond that drops, the polling fallback covers the loss).
	BusSubscriberQueue = 16
)

// ---------------------------------------------------------------------------
// FabricBus
// ---------------------------------------------------------------------------

// FabricBus is the bounded change-signal bus. Zero value is NOT usable —
// construct with NewFabricBus.
type FabricBus struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[uint64]chan Signal
	dropped     uint64
}

// NewFabricBus constructs an empty bus.
func NewFabricBus() *FabricBus {
	return &FabricBus{subscribers: make(map[uint64]chan Signal)}
}

// Subscribe registers a subscriber. ok=false when the subscriber bound is
// reached (the WS layer answers 503 — the client falls back to polling).
// The returned channel is buffered (BusSubscriberQueue) and receive-only.
func (b *FabricBus) Subscribe() (id uint64, ch <-chan Signal, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.subscribers) >= MaxBusSubscribers {
		return 0, nil, false
	}
	id = b.nextID
	b.nextID++
	chBuf := make(chan Signal, BusSubscriberQueue)
	b.subscribers[id] = chBuf
	return id, chBuf, true
}

// Unsubscribe removes a subscriber and drops its channel (the subscriber
// stops receiving; queued signals are discarded — no replay, ever).
// Unsubscribing an unknown id is a no-op (the WS disconnect path is
// idempotent by construction).
func (b *FabricBus) Unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, exists := b.subscribers[id]; exists {
		delete(b.subscribers, id)
		close(ch)
	}
}

// Publish delivers the signal to every subscriber WITHOUT BLOCKING: a
// full queue drops the signal for that subscriber and counts the drop.
// The publisher (a REST mutation handler) must never stall on a slow WS
// consumer — drop-not-block is the contract. Drops are observable via
// Dropped().
func (b *FabricBus) Publish(sig Signal) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subscribers {
		select {
		case ch <- sig:
		default:
			b.dropped++ // bounded by construction: 16 queues × 16 slots
		}
	}
}

// Dropped returns the total count of dropped signals (observability —
// surfaced by the WS layer's control frames on disconnect).
func (b *FabricBus) Dropped() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// Subscribers returns the current subscriber count (observability).
func (b *FabricBus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}
