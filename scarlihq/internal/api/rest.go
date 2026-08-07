package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/guard"
	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/scarlix_mode"
)

// Handler holds dependencies for API routes
type Handler struct {
	guard    *guard.Guard
	mode     *scarlix_mode.Mode
	profiles *profiles.Manager
}

// NewHandler creates a new API handler
func NewHandler(g *guard.Guard, m *scarlix_mode.Mode, p *profiles.Manager) *Handler {
	return &Handler{guard: g, mode: m, profiles: p}
}

// RegisterRoutes registers all REST API routes
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/health", h.health)
	mux.HandleFunc("/api/gpu", h.gpuStatus)
	mux.HandleFunc("/api/mode", h.modeHandler)
	mux.HandleFunc("/api/profiles", h.listProfiles)
	mux.HandleFunc("/api/containers", h.listContainers)
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{
		"status":  "ok",
		"version": "v12.0",
	})
}

func (h *Handler) gpuStatus(w http.ResponseWriter, r *http.Request) {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=index,name,temperature.gpu,utilization.gpu,memory.used,memory.total,power.draw",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		writeJSON(w, []interface{}{})
		return
	}

	type GPU struct {
		Index    int     `json:"index"`
		Name     string  `json:"name"`
		Temp     float64 `json:"temp"`
		Util     float64 `json:"util"`
		MemUsed  float64 `json:"mem_used"`
		MemTotal float64 `json:"mem_total"`
		Power    float64 `json:"power"`
	}

	var gpus []GPU
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 7 {
			continue
		}
		var gpu GPU
		fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &gpu.Index)
		gpu.Name = strings.TrimSpace(parts[1])
		fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &gpu.Temp)
		fmt.Sscanf(strings.TrimSpace(parts[3]), "%f", &gpu.Util)
		fmt.Sscanf(strings.TrimSpace(parts[4]), "%f", &gpu.MemUsed)
		fmt.Sscanf(strings.TrimSpace(parts[5]), "%f", &gpu.MemTotal)
		fmt.Sscanf(strings.TrimSpace(parts[6]), "%f", &gpu.Power)
		gpus = append(gpus, gpu)
	}
	writeJSON(w, gpus)
}

func (h *Handler) modeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		mode := h.mode.Current()
		writeJSON(w, map[string]string{"mode": mode, "status": "ok"})
		return
	}

	mode := r.URL.Query().Get("set")
	if mode == "" {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		mode = body["mode"]
	}

	if mode == "" {
		writeJSON(w, map[string]string{"status": "error", "message": "no mode specified"})
		return
	}

	// Validate mode
	switch mode {
	case "ai", "game", "turbo", "offline":
		// ok
	default:
		writeJSON(w, map[string]string{"status": "error", "message": "invalid mode"})
		return
	}

	if err := h.mode.Set(mode); err != nil {
		writeJSON(w, map[string]string{"status": "error", "message": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"mode": mode, "status": "ok", "message": "Mode switched"})
}

func (h *Handler) listProfiles(w http.ResponseWriter, r *http.Request) {
	profs := h.profiles.List()
	writeJSON(w, profs)
}

func (h *Handler) listContainers(w http.ResponseWriter, r *http.Request) {
	out, err := exec.Command("docker", "ps", "--format",
		"{{.Names}}\t{{.Status}}\t{{.Ports}}").Output()
	if err != nil {
		writeJSON(w, []interface{}{})
		return
	}

	type Container struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Ports  string `json:"ports"`
	}

	var containers []Container
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 3 {
			containers = append(containers, Container{
				Name:   parts[0],
				Status: parts[1],
				Ports:  parts[2],
			})
		}
	}
	writeJSON(w, containers)
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
