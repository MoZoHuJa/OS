package mcp

import (
	"encoding/json"
	"net/http"

	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/guard"
	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/scarlix_mode"
)

// Server is the MCP (Model Context Protocol) server
type Server struct {
	guard    *guard.Guard
	mode     *scarlix_mode.Mode
	profiles *profiles.Manager
}

// NewServer creates a new MCP server
func NewServer(g *guard.Guard, m *scarlix_mode.Mode, p *profiles.Manager) *Server {
	return &Server{guard: g, mode: m, profiles: p}
}

// Tool represents an MCP tool definition
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// RegisterRoutes registers MCP routes
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/mcp/tools", s.listTools)
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	// MCP protocol handler — tools listing + invocation
	response := map[string]interface{}{
		"protocol": "mcp/v1",
		"server":   "scarlihq",
		"version":  "v12.0",
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	tools := []Tool{
		{Name: "scarlix_exec", Description: "Execute shell command with guard"},
		{Name: "scarlix_read_file", Description: "Read file with permissions"},
		{Name: "scarlix_write_file", Description: "Write file with permissions"},
		{Name: "scarlix_git_commit", Description: "Git commit to worktree"},
		{Name: "scarlix_post_nostr", Description: "Post Nostr event to Buzz"},
		{Name: "scarlix_query_memory", Description: "Query agent memory"},
		{Name: "scarlix_store_memory", Description: "Store agent memory"},
		{Name: "scarlix_modelswap_status", Description: "Get current model status"},
		{Name: "scarlix_mode_get", Description: "Get current scarlix-mode"},
		{Name: "scarlix_mode_set", Description: "Set scarlix-mode (ai/game/turbo/offline)"},
		{Name: "scarlix_agents_md_read", Description: "Read AGENTS.md rules"},
		{Name: "scarlix_hitl_ask", Description: "Ask for HITL approval via Telegram"},
		{Name: "scarlix_gpu_status", Description: "Get nvidia-smi GPU status"},
		{Name: "scarlix_container_list", Description: "List Docker containers"},
		{Name: "scarlix_voice_speak", Description: "Speak text via Piper TTS"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"tools": tools})
}
