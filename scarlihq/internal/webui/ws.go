package webui

import (
        "crypto/subtle"
        "encoding/json"
        "log"
        "net"
        "net/http"
        "strings"
        "time"

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
func checkOrigin(r *http.Request) bool {
        origin := r.Header.Get("Origin")
        if origin == "" {
                return true // non-browser clients (curl, scripts) — still need token
        }

        // Parse host:port from origin URL (http://host:port or https://host:port)
        origin = strings.TrimSpace(origin)
        host := ""
        for _, scheme := range []string{"https://", "http://"} {
                if strings.HasPrefix(origin, scheme) {
                        host = strings.TrimPrefix(origin, scheme)
                        break
                }
        }
        if host == "" {
                return false // not http/https origin → reject
        }
        // Strip path + port
        if idx := strings.Index(host, "/"); idx >= 0 {
                host = host[:idx]
        }
        if idx := strings.LastIndex(host, ":"); idx >= 0 {
                host = host[:idx] // strip port
        }
        // Strip brackets from IPv6 [::1]
        host = strings.Trim(host, "[]")

        ip := net.ParseIP(host)
        if ip == nil {
                return false // not an IP (could be hostname — reject for security; LAN uses IPs)
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

// RegisterWS registers WebSocket route (token-authed, pushes real status every 2s).
func RegisterWS(mux *http.ServeMux, authToken string, _ *scarlix_mode.Mode, _ *profiles.Manager) {
        mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
                // Token auth (query param — browsers can't set headers on WS upgrade)
                token := r.URL.Query().Get("token")
                if authToken == "" {
                        http.Error(w, "SCARLIHQ_TOKEN not configured", http.StatusServiceUnavailable)
                        return
                }
                // v18.0.0 P1: crypto/subtle.ConstantTimeCompare (was: custom secureCompare)
                if subtle.ConstantTimeCompare([]byte(token), []byte(authToken)) != 1 {
                        http.Error(w, "invalid token", http.StatusUnauthorized)
                        return
                }

                // v18.5 P1: Limit concurrent WebSocket connections (was: unlimited → DoS)
                select {
                case <-wsSlots:
                        defer func() { wsSlots <- struct{}{} }()
                default:
                        http.Error(w, "too many WebSocket connections", http.StatusServiceUnavailable)
                        return
                }

                conn, err := upgrader.Upgrade(w, r, nil)
                if err != nil {
                        log.Printf("WS upgrade error: %v", err)
                        return
                }
                defer conn.Close()

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
