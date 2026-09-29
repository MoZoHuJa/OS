package webui

import (
        "encoding/json"
        "log"
        "net"
        "net/http"
        "net/url"
        "time"

        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/api"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/profiles"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/scarlix_mode"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/status"
        "github.com/gorilla/websocket"
)

// v17.9.9 P1: proper CIDR origin check (was: strings.HasPrefix — missed https, no real IP validation).
// Allowed networks: 127.0.0.1/32, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16.
// Accepts both http:// and https:// origins.
var allowedNetworks = func() []*net.IPNet {
        cidrs := []string{"127.0.0.1/32", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
        nets := make([]*net.IPNet, 0, len(cidrs))
        for _, c := range cidrs {
                if _, n, err := net.ParseCIDR(c); err == nil {
                        nets = append(nets, n)
                }
        }
        return nets
}()

var upgrader = websocket.Upgrader{
        CheckOrigin:      checkOrigin,
        HandshakeTimeout: 10 * time.Second,
}

// v18.5 P1: Limit concurrent WebSocket connections (was: unlimited → DoS)
var wsSlots = make(chan struct{}, 16) // max 16 concurrent WS clients

func init() {
        for i := 0; i < 16; i++ {
                wsSlots <- struct{}{}
        }
}

// checkOrigin validates the Origin header against allowed LAN networks.
// Returns true for: empty origin (non-browser clients like curl), localhost, LAN private ranges.
// v17.9.9: uses net.ParseIP + net.IPNet.Contains (was: strings.HasPrefix — crude, https rejected).
//
// v18.7.5 P1: Use url.Parse for robust origin validation (was: manual string
// ops to strip scheme/port/brackets → broke on IPv6 origins like
// http://[::1]:8090, and on origins with paths/queries; also mis-stripped the
// port for IPv6 because LastIndex(":") matched inside the [::1] brackets).
// url.Parse + u.Hostname() handles all of these correctly per RFC 3986.
// We still require an IP literal (not a hostname) and still require the IP to
// be in one of the allowed LAN CIDRs — so this is strictly a parser robustness
// fix, not a policy change.
func checkOrigin(r *http.Request) bool {
        origin := r.Header.Get("Origin")
        if origin == "" {
                return true // non-browser clients (curl, scripts) — still need token
        }
        // v18.7.5 P1: Use url.Parse for robust origin validation (was: manual string ops → IPv6 broken)
        u, err := url.Parse(origin)
        if err != nil {
                return false
        }
        if u.Scheme != "http" && u.Scheme != "https" {
                return false
        }
        host := u.Hostname()
        if host == "" {
                return false
        }
        ip := net.ParseIP(host)
        if ip == nil {
                return false // not an IP (hostname — reject for security; LAN uses IPs)
        }
        for _, n := range allowedNetworks {
                if n.Contains(ip) {
                        return true
                }
        }
        return false
}

// statusJSON returns the current host-status.json as JSON bytes.
func statusJSON() []byte {
        s := status.ReadOrStale()
        if s.Timestamp == "" {
                return []byte(`{"error":"host-status.json not found","stale":true}`)
        }
        b, err := json.Marshal(s)
        if err != nil {
                return []byte(`{"error":"marshal failed","stale":true}`)
        }
        return b
}

// RegisterWS registers WebSocket route (ticket-authed, pushes real status every 2s).
// v18.7.3 P1: Switched from permanent ?token=<SCARLIHQ_TOKEN> to short-lived,
// single-use ?ticket=<one-time>. The dashboard first POSTs /api/ws-ticket (Bearer-authed)
// to obtain a 30s single-use ticket, then connects to /ws?ticket=<...>.
// Net effect: SCARLIHQ_TOKEN no longer appears in URLs, browser history, referrer
// headers, or reverse-proxy access logs.
//
// v18.7.5 P0: Ticket is now RESERVED (atomically deleted) BEFORE upgrader.Upgrade(),
// replacing the v18.7.4 Peek+Consume split (was: Peek didn't delete → two concurrent
// WS connects with the same ticket could both Peek=true and both Upgrade, defeating
// the single-use replay-resistance guarantee). On Upgrade failure, Release puts the
// ticket back so the legitimate client can retry without re-POSTing /api/ws-ticket.
//
// authToken + mode + pf parameters are kept for signature stability (callers in main.go
// pass them); they are unused here now since auth is delegated to api.ReserveWSTicket.
func RegisterWS(mux *http.ServeMux, _ string, _ *scarlix_mode.Mode, _ *profiles.Manager) {
        mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
                ticket := r.URL.Query().Get("ticket")
                if ticket == "" {
                        http.Error(w, "missing ticket", http.StatusUnauthorized)
                        return
                }
                // v18.7.5 P0: Atomic reservation — delete BEFORE Upgrade (was: Peek didn't delete).
                // Single-use replay-resistance guarantee: only the first concurrent caller
                // can Reserve=true; all others see false because the entry is gone.
                if !api.ReserveWSTicket(ticket) {
                        http.Error(w, "invalid or expired ticket", http.StatusUnauthorized)
                        return
                }

                // v18.5 P1: Limit concurrent WebSocket connections (was: unlimited → DoS)
                select {
                case <-wsSlots:
                        defer func() { wsSlots <- struct{}{} }()
                default:
                        // v18.7.5 P0: Release ticket so client can retry without re-POSTing.
                        api.ReleaseWSTicket(ticket)
                        http.Error(w, "too many WebSocket connections", http.StatusServiceUnavailable)
                        return
                }

                conn, err := upgrader.Upgrade(w, r, nil)
                if err != nil {
                        log.Printf("WS upgrade error: %v", err)
                        // v18.7.5 P0: Release ticket on upgrade failure (was: consumed before → wasted)
                        api.ReleaseWSTicket(ticket)
                        return
                }
                defer conn.Close()
                // Ticket is already reserved (deleted from store) — no ConsumeWSTicket needed.

                log.Println("WS client connected")

                // v18.5 P1: Write deadline prevents blocked clients (was: no deadline → goroutine leak DoS)
                conn.SetWriteDeadline(time.Now().Add(10 * time.Second))

                ticker := time.NewTicker(2 * time.Second)
                defer ticker.Stop()

                // v18.5 P1: refresh write deadline before each WriteMessage
                conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
                if err := conn.WriteMessage(websocket.TextMessage, statusJSON()); err != nil {
                        log.Printf("WS write error: %v", err)
                        return
                }

                for {
                        select {
                        case <-ticker.C:
                                // v18.5 P1: refresh write deadline before each WriteMessage
                                conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
                                if err := conn.WriteMessage(websocket.TextMessage, statusJSON()); err != nil {
                                        log.Printf("WS write error: %v", err)
                                        return
                                }
                        }
                }
        })
}
