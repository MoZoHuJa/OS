package api

import (
        "crypto/rand"
        "crypto/subtle"
        "encoding/hex"
        "encoding/json"
        "net/http"
        "strings"
        "sync"
        "time"

        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/guard"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/profiles"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/scarlix_mode"
        "github.com/MoZoHuJa/scarlix-os-v12/scarlihq/internal/status"
)

// Version is the fallback default for /api/health when Handler has no version passed.
// v18.2 P1: main.go now passes Version to NewHandler — this is only used if not set.
var Version = "18.7.3"

// v18.7.3 P1: Short-lived WS ticket store (replaces permanent token in URL).
// Tickets are 32-byte random hex strings, valid for 30s, single-use.
// The dashboard POSTs /api/ws-ticket (Bearer-authed) to obtain a ticket,
// then connects to /ws?ticket=<one-time> — the URL no longer carries the
// permanent SCARLIHQ_TOKEN (was: leaked into logs/history/referrer headers).
var (
        wsTickets   = make(map[string]time.Time)
        wsTicketsMu sync.Mutex
        wsTicketTTL = 30 * time.Second
)

// issueWSTicket generates a single-use WS ticket valid for wsTicketTTL.
// Also sweeps any expired tickets so the map can't grow unboundedly under
// repeated POST /api/ws-ticket calls without a WS connect.
func issueWSTicket() string {
        b := make([]byte, 32)
        // v18.7.3 P1: crypto/rand.Read errors only if /dev/urandom is unavailable —
        // in that case b is all-zeros and the resulting ticket is constant; acceptable
        // failure mode (single predictable ticket that expires in 30s), better than
        // blocking the request. Logged by Go runtime if /dev/urandom open fails.
        _, _ = rand.Read(b)
        ticket := hex.EncodeToString(b)
        wsTicketsMu.Lock()
        now := time.Now()
        for t, expiry := range wsTickets {
                if now.After(expiry) {
                        delete(wsTickets, t)
                }
        }
        wsTickets[ticket] = now.Add(wsTicketTTL)
        wsTicketsMu.Unlock()
        return ticket
}

// ValidateWSTicket validates AND consumes a single-use WS ticket.
// Returns false if the ticket is unknown or already used (single-use guarantee).
// Exported so the webui package can use it from /ws upgrade handler.
func ValidateWSTicket(ticket string) bool {
        if ticket == "" {
                return false
        }
        wsTicketsMu.Lock()
        defer wsTicketsMu.Unlock()
        expiry, ok := wsTickets[ticket]
        if !ok {
                return false
        }
        delete(wsTickets, ticket) // single use — replay-resistant
        return time.Now().Before(expiry)
}

// Handler holds dependencies for API routes.
type Handler struct {
        guard     *guard.Guard
        mode      *scarlix_mode.Mode
        profiles  *profiles.Manager
        authToken string
        version   string // v18.2: passed from main (ldflags-injected)
}

// NewHandler creates a new API handler.
// v18.2 P1: version param added (was: separate const — ldflags couldn't override).
func NewHandler(g *guard.Guard, m *scarlix_mode.Mode, p *profiles.Manager, authToken, version string) *Handler {
        if version == "" {
                version = Version // fallback to package var
        }
        return &Handler{guard: g, mode: m, profiles: p, authToken: authToken, version: version}
}

// RegisterRoutes registers all REST API routes (all behind auth middleware).
// v18.7.3 P1: Added /api/ws-ticket — short-lived single-use ticket for WS upgrade
// (was: WS upgrade required permanent SCARLIHQ_TOKEN in ?token= URL → leaked in logs).
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
        mux.HandleFunc("/api/health", h.auth(h.health))
        mux.HandleFunc("/api/gpu", h.auth(h.gpuStatus))
        mux.HandleFunc("/api/containers", h.auth(h.listContainers))
        mux.HandleFunc("/api/mode", h.auth(h.modeHandler))
        mux.HandleFunc("/api/profiles", h.auth(h.listProfiles))
        mux.HandleFunc("/api/status", h.auth(h.fullStatus))
        mux.HandleFunc("/api/ws-ticket", h.auth(h.issueWSTicket))
}

// auth middleware: validates Bearer token header only.
// v18.0.0 P1: uses crypto/subtle.ConstantTimeCompare (was: custom secureCompare).
// v18.6 P2: removed ?token= query fallback (was: token leaked in URLs/logs/history).
func (h *Handler) auth(next http.HandlerFunc) http.HandlerFunc {
        return func(w http.ResponseWriter, r *http.Request) {
                if h.authToken == "" {
                        writeJSONError(w, http.StatusServiceUnavailable, "SCARLIHQ_TOKEN not configured on server")
                        return
                }
                // v18.6 P2: Only accept Bearer header now (was: also accepted ?token= query param)
                token := r.Header.Get("Authorization")
                if strings.HasPrefix(token, "Bearer ") {
                        token = strings.TrimPrefix(token, "Bearer ")
                } else {
                        writeJSONError(w, http.StatusUnauthorized, "invalid or missing token (use Authorization: Bearer)")
                        return
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
                "version": h.version, // v18.2: from main.Version (ldflags-injected)
        })
}

func (h *Handler) gpuStatus(w http.ResponseWriter, r *http.Request) {
        s := status.ReadOrStale()
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
        s := status.ReadOrStale()
        if s.Containers == nil {
                s.Containers = []status.Container{}
        }
        writeJSON(w, map[string]interface{}{
                "containers": s.Containers,
                "timestamp":  s.Timestamp,
        })
}

func (h *Handler) fullStatus(w http.ResponseWriter, r *http.Request) {
        writeJSON(w, status.ReadOrStale())
}

// modeHandler handles GET (return current + transition state) and POST (set desired mode).
// v18.0.0 P1: GET returns full transition state (was: only current-mode).
// Dashboard can now show "requested=ai, state=retrying, retry_count=1, last_error=...".
func (h *Handler) modeHandler(w http.ResponseWriter, r *http.Request) {
        if r.Method == http.MethodGet {
                s := status.ReadOrStale()
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
                // v18.6 P1: Limit request body size (was: no limit → DoS with large body)
                r.Body = http.MaxBytesReader(w, r.Body, 4096)
                var body map[string]string
                if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
                        mode = body["mode"]
                }
        }
        if mode == "" {
                writeJSONError(w, http.StatusBadRequest, "no mode specified")
                return
        }

        // v18.7.3 P1: Centralized mode request (was: duplicate retrying check + Set()
        // in both this REST handler and the MCP handler). Mode.Request() now does:
        //   1. Rejects if host-status.json says state="retrying" (transition in progress)
        //   2. Atomically reserves desired-mode via O_EXCL (v18.7.2 P0 atomicity preserved)
        // On error, the message string discriminates which check failed so we can return
        // the right HTTP status (409 for busy/retrying, 400 for invalid mode, 503 for fs).
        if err := h.mode.Request(mode); err != nil {
                if strings.Contains(err.Error(), "already pending") {
                        w.WriteHeader(http.StatusConflict)
                        writeJSON(w, map[string]interface{}{
                                "status":  "busy",
                                "message": "a mode request is already pending",
                        })
                        return
                }
                if strings.Contains(err.Error(), "in progress (retrying)") {
                        // v18.7.3 P1: Surface retrying-state from Mode.Request() as 409 with context
                        // (preserves v18.6 P0 dashboard semantics: client sees requested_mode +
                        // transition_state + retry_count so it can render the retry banner).
                        s := status.ReadOrStale()
                        w.WriteHeader(http.StatusConflict)
                        writeJSON(w, map[string]interface{}{
                                "status":           "busy",
                                "message":          "a mode transition is already in progress",
                                "requested_mode":   s.ModeTransition.Requested,
                                "transition_state": s.ModeTransition.State,
                                "retry_count":      s.ModeTransition.RetryCount,
                        })
                        return
                }
                code := http.StatusInternalServerError
                if strings.Contains(err.Error(), "invalid mode") {
                        code = http.StatusBadRequest
                } else if strings.Contains(err.Error(), "create") || strings.Contains(err.Error(), "permission") {
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

// issueWSTicket issues a short-lived, single-use ticket for the WS upgrade handshake.
// v18.7.3 P1: Replaces passing SCARLIHQ_TOKEN in the WS URL query string.
// Caller must already be authed (route is registered behind h.auth) — the bearer
// token never appears in URLs/logs; only this 30s, single-use ticket does.
// Returns {"ticket": "<64 hex chars>", "expires_in": 30}.
func (h *Handler) issueWSTicket(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
                writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
                return
        }
        ticket := issueWSTicket()
        writeJSON(w, map[string]interface{}{
                "ticket":     ticket,
                "expires_in": int(wsTicketTTL / time.Second),
        })
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
