package api

import (
        "crypto/subtle"
        "encoding/json"
        "net/http"
        "strings"

        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/guard"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/profiles"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/scarlix_mode"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/status"
)

// Version mirrors main.Version (passed to avoid import cycle).
// v18.0.0: injected via -ldflags "-X main.Version" in Dockerfile.
const Version = "18.1"

// Handler holds dependencies for API routes.
type Handler struct {
        guard     *guard.Guard
        mode      *scarlix_mode.Mode
        profiles  *profiles.Manager
        authToken string
}

func NewHandler(g *guard.Guard, m *scarlix_mode.Mode, p *profiles.Manager, authToken string) *Handler {
        return &Handler{guard: g, mode: m, profiles: p, authToken: authToken}
}

// RegisterRoutes registers all REST API routes (all behind auth middleware).
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
        mux.HandleFunc("/api/health", h.auth(h.health))
        mux.HandleFunc("/api/gpu", h.auth(h.gpuStatus))
        mux.HandleFunc("/api/containers", h.auth(h.listContainers))
        mux.HandleFunc("/api/mode", h.auth(h.modeHandler))
        mux.HandleFunc("/api/profiles", h.auth(h.listProfiles))
        mux.HandleFunc("/api/status", h.auth(h.fullStatus))
}

// auth middleware: validates Bearer token or ?token= query param.
// v18.0.0 P1: uses crypto/subtle.ConstantTimeCompare (was: custom secureCompare).
func (h *Handler) auth(next http.HandlerFunc) http.HandlerFunc {
        return func(w http.ResponseWriter, r *http.Request) {
                if h.authToken == "" {
                        writeJSONError(w, http.StatusServiceUnavailable, "SCARLIHQ_TOKEN not configured on server")
                        return
                }
                token := r.Header.Get("Authorization")
                if strings.HasPrefix(token, "Bearer ") {
                        token = strings.TrimPrefix(token, "Bearer ")
                } else {
                        token = r.URL.Query().Get("token")
                }
                // v18.0.0 P1: crypto/subtle.ConstantTimeCompare (standard library, constant-time)
                if subtle.ConstantTimeCompare([]byte(token), []byte(h.authToken)) != 1 {
                        writeJSONError(w, http.StatusUnauthorized, "invalid or missing token")
                        return
                }
                next(w, r)
        }
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
        writeJSON(w, map[string]string{
                "status":  "ok",
                "version": Version,
        })
}

func (h *Handler) gpuStatus(w http.ResponseWriter, r *http.Request) {
        s := status.Read()
        if len(s.GPUs) == 0 && s.Timestamp == "" {
                writeJSON(w, map[string]interface{}{
                        "gpus":    []status.GPU{},
                        "stale":   true,
                        "message": "host-status.json not found — scarlix-host-bridge.timer not running?",
                })
                return
        }
        writeJSON(w, map[string]interface{}{
                "gpus":      s.GPUs,
                "timestamp": s.Timestamp,
        })
}

func (h *Handler) listContainers(w http.ResponseWriter, r *http.Request) {
        s := status.Read()
        if s.Containers == nil {
                s.Containers = []status.Container{}
        }
        writeJSON(w, map[string]interface{}{
                "containers": s.Containers,
                "timestamp":  s.Timestamp,
        })
}

func (h *Handler) fullStatus(w http.ResponseWriter, r *http.Request) {
        writeJSON(w, status.Read())
}

// modeHandler handles GET (return current + transition state) and POST (set desired mode).
// v18.0.0 P1: GET returns full transition state (was: only current-mode).
// Dashboard can now show "requested=ai, state=retrying, retry_count=1, last_error=...".
func (h *Handler) modeHandler(w http.ResponseWriter, r *http.Request) {
        if r.Method == http.MethodGet {
                s := status.Read()
                writeJSON(w, map[string]interface{}{
                        "mode":             s.Mode,
                        "status":           "ok",
                        "requested_mode":   s.ModeTransition.Requested,
                        "transition_state": s.ModeTransition.State,
                        "retry_count":      s.ModeTransition.RetryCount,
                        "last_error":       s.ModeTransition.LastError,
                        "last_timestamp":   s.ModeTransition.LastTimestamp,
                })
                return
        }

        if r.Method != http.MethodPost {
                writeJSONError(w, http.StatusMethodNotAllowed, "use GET or POST")
                return
        }

        mode := r.URL.Query().Get("set")
        if mode == "" {
                var body map[string]string
                if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
                        mode = body["mode"]
                }
        }
        if mode == "" {
                writeJSONError(w, http.StatusBadRequest, "no mode specified")
                return
        }

        if err := h.mode.Set(mode); err != nil {
                code := http.StatusInternalServerError
                if strings.Contains(err.Error(), "invalid mode") {
                        code = http.StatusBadRequest
                } else if strings.Contains(err.Error(), "write") || strings.Contains(err.Error(), "permission") || strings.Contains(err.Error(), "create temp") {
                        code = http.StatusServiceUnavailable
                }
                writeJSONError(w, code, err.Error())
                return
        }
        w.WriteHeader(http.StatusAccepted)
        writeJSON(w, map[string]string{
                "mode":    mode,
                "status":  "accepted",
                "message": "mode switch requested — host bridge will apply within 5s",
        })
}

func (h *Handler) listProfiles(w http.ResponseWriter, r *http.Request) {
        profs := h.profiles.List()
        if profs == nil {
                profs = []profiles.Profile{}
        }
        writeJSON(w, profs)
}

func writeJSON(w http.ResponseWriter, data interface{}) {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(data)
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(code)
        json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": msg})
}
