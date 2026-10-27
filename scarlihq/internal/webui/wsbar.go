package webui

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentfabric"
	"github.com/MoZoHuJa/OS/scarlihq/internal/api"
	"github.com/gorilla/websocket"
)

// BF-3 (v19.5.2): the /ws/bar push surface — change signals from the
// Agent Fabric to the dashboard. Contract (the /ws precedent, verbatim):
//
//   - TICKET BEFORE UPGRADE: the client POSTs /api/ws-ticket (Bearer-
//     authed) and connects with ?ticket=<one-time>. The ticket is
//     RESERVED (atomically deleted) BEFORE upgrader.Upgrade() — the
//     v18.7.5/v18.7.6 single-use discipline, shared with the /ws surface
//     (a consumed ticket is never re-added on failure; the client
//     re-POSTs).
//   - SHARED SLOTS: /ws/bar and /ws share ONE wsSlots channel (16
//     concurrent WS clients across BOTH surfaces — the bound is real
//     state, not per-route decoration; a flood on one surface exhausts
//     the shared budget, exactly like a flood on the other).
//   - NIL BUS = 503 BEFORE UPGRADE: the route is always registered; when
//     the fabric is disabled (bus nil) it answers 503 like the REST
//     fabric surfaces — no silent degradation, no half-open upgrades.
//   - CHANGE SIGNALS NOT STATE: every bus signal (command.* /
//     execution.*) is forwarded verbatim; the CLIENT refetches through
//     the auth-gated REST surface. Control frames (fabric.*) are sent by
//     this layer, never by the bus: "fabric.subscribed" on connect (the
//     client's cue to do its first refetch — no replay, remember), and
//     "fabric.dropped" on disconnect when signals were dropped (the
//     bounded-queue contract made visible).
//   - BOUNDED LIFETIME: write deadlines on every frame; the read side
//     drains (the protocol is server-push only — client frames are
//     ignored but must be read to detect close).
//
// Fallback contract: the dashboard keeps its 5s polling — the bus makes
// the status FRESHER, the polling makes it RELIABLE. Neither depends on
// the other.
func RegisterBarWS(mux *http.ServeMux, bus *agentfabric.FabricBus) {
	mux.HandleFunc("/ws/bar", func(w http.ResponseWriter, r *http.Request) {
		// Nil bus = fabric disabled: 503 BEFORE the upgrade (never a
		// half-configured surface, never an upgraded connection that
		// would receive nothing forever).
		if bus == nil {
			http.Error(w, "agent fabric is disabled (SCARLIHQ_AGENTFABRIC_ENABLED=true + SCARLIHQ_AGENTFABRIC_COMMANDS_FILE) — no fabric events to push", http.StatusServiceUnavailable)
			return
		}
		ticket := r.URL.Query().Get("ticket")
		if ticket == "" {
			http.Error(w, "missing ticket", http.StatusUnauthorized)
			return
		}
		// Atomic reservation — delete BEFORE Upgrade (the /ws discipline;
		// single-use, consumed on failure, never re-added).
		if !api.ReserveWSTicket(ticket) {
			http.Error(w, "invalid or expired ticket", http.StatusUnauthorized)
			return
		}

		// SHARED slot budget with /ws (one channel, both surfaces).
		select {
		case <-wsSlots:
			defer func() { wsSlots <- struct{}{} }()
		default:
			// Ticket consumed (never re-added); client re-POSTs /api/ws-ticket to retry.
			http.Error(w, "too many WebSocket connections", http.StatusServiceUnavailable)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS /ws/bar upgrade error: %v", err)
			return // ticket stays consumed — single-use is unconditional
		}
		defer conn.Close()

		id, signals, ok := bus.Subscribe()
		if !ok {
			// Subscriber bound reached: 503 AFTER the upgrade is not
			// honest — close the just-upgraded connection with a close
			// frame carrying 1013 (try again later). The slot is returned
			// by the deferred release above.
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(1013, "fabric bus at capacity — retry later"),
				time.Now().Add(5*time.Second))
			return
		}
		defer bus.Unsubscribe(id)

		log.Println("WS /ws/bar client connected (fabric push)")

		// Control frame: the subscription cue. The client refetches NOW —
		// its first authoritative state (no replay: nothing before this
		// moment is ever delivered).
		frame := map[string]string{"type": "fabric.subscribed", "at": time.Now().UTC().Format(time.RFC3339)}
		if raw, err := json.Marshal(frame); err == nil {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				log.Printf("WS /ws/bar write error: %v", err)
				return
			}
		}

		// Drain the read side: the protocol is server-push only, but close
		// detection needs reads. Anything the client sends is discarded.
		go func() {
			conn.SetReadLimit(512)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return // closed, corrupted or timed out — the write loop notices
				}
			}
		}()

		for sig := range signals {
			raw, err := json.Marshal(sig)
			if err != nil {
				continue // a signal that cannot encode is dropped (drop-not-block)
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				log.Printf("WS /ws/bar write error: %v", err)
				return
			}
		}
		// signals closed (Unsubscribe) — normal disconnect path. Report
		// drop counters so the bounded-queue contract stays observable.
		dropped := bus.Dropped()
		if dropped > 0 {
			dropFrame := map[string]interface{}{"type": "fabric.dropped", "dropped_total": dropped}
			if raw, err := json.Marshal(dropFrame); err == nil {
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = conn.WriteMessage(websocket.TextMessage, raw)
			}
		}
	})
}
