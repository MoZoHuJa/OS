package mcp

import (
        "crypto/subtle"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "strings"

        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/guard"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/profiles"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/scarlix_mode"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/status"
)

// v17.9.8 P0: This is a REAL JSON-RPC 2.0 endpoint (was: fake MCP returning a static
// JSON blob claiming "mcp/v1" without implementing initialize/tools/list/tools/call).
//
// Note: MCP (Model Context Protocol) is typically stdio-based. This HTTP endpoint
// implements the JSON-RPC 2.0 message layer that MCP builds on, so MCP clients that
// speak HTTP transport can use it. A full stdio MCP server would be a separate binary.
// We expose this at /rpc (honest name) rather than /mcp (overclaim).

// Server is the JSON-RPC server exposing ScarliHQ tools.
type Server struct {
        guard     *guard.Guard
        mode      *scarlix_mode.Mode
        profiles  *profiles.Manager
        authToken string
        version   string
}

// NewServer creates a new JSON-RPC server.
func NewServer(g *guard.Guard, m *scarlix_mode.Mode, p *profiles.Manager, authToken, version string) *Server {
        return &Server{guard: g, mode: m, profiles: p, authToken: authToken, version: version}
}

// JSON-RPC 2.0 types
type rpcRequest struct {
        JSONRPC string          `json:"jsonrpc"`
        ID      json.RawMessage `json:"id"`
        Method  string          `json:"method"`
        Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
        JSONRPC string          `json:"jsonrpc"`
        ID      json.RawMessage `json:"id"`
        Result  interface{}     `json:"result,omitempty"`
        Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
        Code    int    `json:"code"`
        Message string `json:"message"`
}

type toolDef struct {
        Name        string `json:"name"`
        Description string `json:"description"`
}

type initializeResult struct {
        ProtocolVersion string            `json:"protocolVersion"`
        Capabilities    map[string]any    `json:"capabilities"`
        ServerInfo      map[string]string `json:"serverInfo"`
}

// RegisterRoutes registers JSON-RPC routes (all behind token auth).
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
        mux.HandleFunc("/rpc", s.auth(s.handleRPC))
        mux.HandleFunc("/mcp", s.auth(s.handleMCPInfo))
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
        return func(w http.ResponseWriter, r *http.Request) {
                if s.authToken == "" {
                        writeRPCError(w, http.StatusServiceUnavailable, nil, -32000, "SCARLIHQ_TOKEN not configured")
                        return
                }
                // v18.6 P2: Only accept Bearer header (was: also accepted ?token= query param → leaked in logs)
                token := r.Header.Get("Authorization")
                if strings.HasPrefix(token, "Bearer ") {
                        token = strings.TrimPrefix(token, "Bearer ")
                } else {
                        writeRPCError(w, http.StatusUnauthorized, nil, -32001, "invalid or missing token (use Authorization: Bearer)")
                        return
                }
                // v18.0.0 P1: crypto/subtle.ConstantTimeCompare (was: custom secureCompare with !=)
                if subtle.ConstantTimeCompare([]byte(token), []byte(s.authToken)) != 1 {
                        writeRPCError(w, http.StatusUnauthorized, nil, -32001, "invalid or missing token")
                        return
                }
                next(w, r)
        }
}

func (s *Server) handleMCPInfo(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{
                "protocol":    "jsonrpc/2.0",
                "server":       "scarlihq",
                "version":      s.version,
                "endpoint":     "/rpc",
                "note":         "HTTP JSON-RPC 2.0 endpoint (initialize, tools/list, tools/call)",
        })
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
                http.Error(w, "POST required", http.StatusMethodNotAllowed)
                return
        }
        // v18.7 P1: Limit RPC body size (was: no limit → DoS)
        r.Body = http.MaxBytesReader(w, r.Body, 64<<10) // 64KB
        // v18.7.2 P1: Correct trailing JSON check (was: decoder.More() — wrong for top-level values)
        decoder := json.NewDecoder(r.Body)
        var req rpcRequest
        if err := decoder.Decode(&req); err != nil {
                writeRPCError(w, http.StatusBadRequest, nil, -32700, "parse error")
                return
        }
        // Verify no trailing JSON (exactly one top-level value)
        var extra interface{}
        if err := decoder.Decode(&extra); err != io.EOF {
                writeRPCError(w, http.StatusBadRequest, req.ID, -32700, "trailing data after JSON request")
                return
        }
        if req.JSONRPC != "2.0" {
                writeRPCError(w, http.StatusOK, req.ID, -32600, "invalid request: jsonrpc must be 2.0")
                return
        }

        w.Header().Set("Content-Type", "application/json")
        var resp rpcResponse
        resp.JSONRPC = "2.0"
        resp.ID = req.ID

        switch req.Method {
        case "initialize":
                resp.Result = initializeResult{
                        ProtocolVersion: "2024-11-05",
                        Capabilities: map[string]any{
                                "tools": map[string]any{"listChanged": false},
                        },
                        ServerInfo: map[string]string{
                                "name":    "scarlihq",
                                "version": s.version,
                        },
                }
        case "tools/list":
                resp.Result = map[string]any{"tools": s.tools()}
        case "tools/call":
                resp = s.handleToolCall(req)
        case "ping":
                resp.Result = map[string]any{}
        default:
                resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
        }

        json.NewEncoder(w).Encode(resp)
}

func (s *Server) tools() []toolDef {
        return []toolDef{
                {Name: "scarlix_mode_get", Description: "Get current scarlix-mode (ai/stop/game/creative/turbo/offline/tv)"},
                {Name: "scarlix_mode_set", Description: "Request scarlix-mode switch (async via host bridge). Params: {mode: string}"},
                {Name: "scarlix_gpu_status", Description: "Get GPU status from host-status.json (nvidia-smi snapshot)"},
                {Name: "scarlix_container_list", Description: "List Docker containers from host-status.json"},
                {Name: "scarlix_profile_list", Description: "List user profiles from /etc/scarlix/profiles/*.yaml"},
                {Name: "scarlix_full_status", Description: "Full host status (mode, gpus, containers, disk, experimental)"},
        }
}

type toolCallParams struct {
        Name      string         `json:"name"`
        Arguments map[string]any `json:"arguments,omitempty"`
}

func (s *Server) handleToolCall(req rpcRequest) rpcResponse {
        var params toolCallParams
        if err := json.Unmarshal(req.Params, &params); err != nil {
                return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}}
        }

        switch params.Name {
        case "scarlix_mode_get":
                return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]string{"mode": s.mode.Current()}}
        case "scarlix_mode_set":
                mode, _ := params.Arguments["mode"].(string)
                // v18.7.3 P1: Centralized mode request (was: duplicate retrying check + Set()
                // here in MCP and again in REST modeHandler). scarlix_mode.Mode.Request() now
                // does both the transition-state check (from host-status.json) AND the atomic
                // O_EXCL reservation via Set(). The error string discriminates which check
                // failed so we can map it to the right JSON-RPC error code.
                if err := s.mode.Request(mode); err != nil {
                        code := -32603 // Internal error (default)
                        msg := err.Error()
                        if strings.Contains(msg, "already pending") || strings.Contains(msg, "in progress (retrying)") {
                                code = -32004 // Resource busy (matches REST 409 semantics)
                        } else if strings.Contains(msg, "invalid mode") {
                                code = -32602 // Invalid params
                        }
                        return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: msg}}
                }
                return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]string{"status": "accepted", "mode": mode}}
        case "scarlix_gpu_status":
                return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"gpus": status.ReadOrStale().GPUs}}
        case "scarlix_container_list":
                return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"containers": status.ReadOrStale().Containers}}
        case "scarlix_profile_list":
                return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"profiles": s.profiles.List()}}
        case "scarlix_full_status":
                return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: status.ReadOrStale()}
        default:
                return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", params.Name)}}
        }
}

func writeRPCError(w http.ResponseWriter, httpCode int, id json.RawMessage, code int, msg string) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(httpCode)
        json.NewEncoder(w).Encode(rpcResponse{
                JSONRPC: "2.0",
                ID:      id,
                Error:   &rpcError{Code: code, Message: msg},
        })
}
