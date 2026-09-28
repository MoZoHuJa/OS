package webui

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/scarlix_mode"
	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/status"
	"github.com/gorilla/websocket"
)

// v17.9.8 P1: origin check (was `return true` — any origin accepted).
// Allow localhost + LAN private ranges (10.x, 172.16-31.x, 192.168.x).
var upgrader = websocket.Upgrader{
	CheckOrigin:     checkOrigin,
	HandshakeTimeout: 10 * time.Second,
}

func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients (curl, scripts) — still need token
	}
	allowed := []string{
		"http://localhost", "http://127.0.0.1",
		"http://10.", "http://192.168.",
	}
	for _, p := range allowed {
		if strings.HasPrefix(origin, p) {
			return true
		}
	}
	// 172.16.0.0/12 (172.16.x – 172.31.x)
	if strings.HasPrefix(origin, "http://172.") {
		for i := 16; i <= 31; i++ {
			if strings.HasPrefix(origin, "http://172."+itoa(i)+".") {
				return true
			}
		}
	}
	return false
}

// itoa is a tiny int→string helper (avoids strconv import for this simple use).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// statusJSON returns the current host-status.json as JSON bytes.
// v17.9.8: reads via status package (file-based bridge — no nvidia-smi/docker exec).
func statusJSON() []byte {
	s := status.Read()
	b, err := json.Marshal(s)
	if err != nil {
		return []byte(`{"error":"marshal failed","stale":true}`)
	}
	return b
}

// RegisterWS registers WebSocket route (token-authed, pushes real status every 2s).
// v17.9.8 P1: pushes GPU + containers + mode (was: only clock).
func RegisterWS(mux *http.ServeMux, authToken string, _ *scarlix_mode.Mode, _ *profiles.Manager) {
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// Token auth (query param — browsers can't set headers on WS upgrade)
		token := r.URL.Query().Get("token")
		if authToken == "" {
			http.Error(w, "SCARLIHQ_TOKEN not configured", http.StatusServiceUnavailable)
			return
		}
		if token != authToken {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS upgrade error: %v", err)
			return
		}
		defer conn.Close()

		log.Println("WS client connected")

		// Send full status every 2s (real GPU/mode/containers, not just clock)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		// Initial push immediately
		if err := conn.WriteMessage(websocket.TextMessage, statusJSON()); err != nil {
			log.Printf("WS write error: %v", err)
			return
		}

		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.TextMessage, statusJSON()); err != nil {
					log.Printf("WS write error: %v", err)
					return
				}
			}
		}
	})
}
