package webui

import (
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins in LAN
	},
}

// RegisterWS registers WebSocket routes for real-time updates
func RegisterWS(mux *http.ServeMux) {
	mux.HandleFunc("/ws", handleWS)
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WS upgrade error: %v", err)
		return
	}
	defer conn.Close()

	log.Println("WS client connected")

	// Send periodic updates
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Send system status update
			msg := map[string]interface{}{
				"type": "status",
				"time": time.Now().Format("15:04:05"),
			}
			if err := conn.WriteJSON(msg); err != nil {
				log.Printf("WS write error: %v", err)
				return
			}
		}
	}
}
