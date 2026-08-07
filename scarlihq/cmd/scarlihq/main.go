package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/api"
	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/guard"
	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/mcp"
	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/scarlix_mode"
	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/webui"
)

//go:embed frontend/dist/index.html
var indexHTML []byte

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("SCARLIX OS v12 — ScarliHQ starting...")

	// Initialize subsystems
	g := guard.New()
	mode := scarlix_mode.New()
	pf := profiles.New("/etc/scarlix/profiles")

	// HTTP mux
	mux := http.NewServeMux()

	// 2D Dashboard
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	// REST API
	apiHandler := api.NewHandler(g, mode, pf)
	apiHandler.RegisterRoutes(mux)

	// MCP server
	mcpServer := mcp.NewServer(g, mode, pf)
	mcpServer.RegisterRoutes(mux)

	// WebSocket
	webui.RegisterWS(mux)

	// Start server
	port := os.Getenv("SCARLIHQ_PORT")
	if port == "" {
		port = "8090"
	}

	log.Printf("ScarliHQ listening on :%s", port)
	log.Printf("2D Dashboard: http://localhost:%s", port)
	log.Printf("MCP Server:    http://localhost:%s/mcp", port)

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// GPUStatus represents nvidia-smi GPU metrics
type GPUStatus struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Temp     float64 `json:"temp"`
	Util     float64 `json:"util"`
	MemUsed  float64 `json:"mem_used"`
	MemTotal float64 `json:"mem_total"`
	Power    float64 `json:"power"`
}

func getGPUStatus() []GPUStatus {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=index,name,temperature.gpu,utilization.gpu,memory.used,memory.total,power.draw",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return []GPUStatus{}
	}

	var gpus []GPUStatus
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 7 {
			continue
		}
		var gpu GPUStatus
		fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &gpu.Index)
		gpu.Name = strings.TrimSpace(parts[1])
		fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &gpu.Temp)
		fmt.Sscanf(strings.TrimSpace(parts[3]), "%f", &gpu.Util)
		fmt.Sscanf(strings.TrimSpace(parts[4]), "%f", &gpu.MemUsed)
		fmt.Sscanf(strings.TrimSpace(parts[5]), "%f", &gpu.MemTotal)
		fmt.Sscanf(strings.TrimSpace(parts[6]), "%f", &gpu.Power)
		gpus = append(gpus, gpu)
	}
	return gpus
}

// helper to write JSON
func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
