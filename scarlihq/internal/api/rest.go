package api

import (
        "crypto/rand"
        "crypto/subtle"
        "encoding/hex"
        "encoding/json"
        "errors"
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
var Version = "18.9.6"

// v18.7.3 P1: Short-lived WS ticket store (replaces permanent token in URL).
// Tickets are 32-byte random hex strings, valid for 30s, single-use.
// The dashboard POSTs /api/ws-ticket (Bearer-authed) to obtain a ticket,
// then connects to /ws?ticket=<one-time> — the URL no longer carries the
// permanent SCARLIHQ_TOKEN (was: leaked into logs/history/referrer headers).
//
// v18.7.7 P1: Rate limit max outstanding tickets (was: a single holder of
//   SCARLIHQ_TOKEN could POST /api/ws-ticket in a tight loop and create
//   unlimited map entries during the 30s TTL — the sweep only runs on the
//   NEXT issueWSTicket call, so 100k requests could allocate 100k entries
//   before any expired. Now: maxWSTickets=1024 caps the map size — if the
//   store is full, issueWSTicket returns ErrTicketLimit and the HTTP handler
//   returns 503 so the dashboard backs off instead of exhausting memory.)
//
// v18.7.8 P1: Use error type instead of returning "" + guessing (was:
//   issueWSTicket returned "" for BOTH RNG failure and rate-limit → handler
//   re-checked len(wsTickets) to distinguish → RACE: between issueWSTicket
//   returning "" and the handler checking len(), another goroutine could
//   consume tickets → len < 1024 → handler returns 500 (RNG failure) even
//   though the real reason was 503 (rate-limit). Now: issueWSTicket returns
//   (ticket, err) with sentinel errors ErrTicketLimit / ErrTicketRNG so
//   the handler can return the correct status without re-checking state.)
var (
        wsTickets   = make(map[string]time.Time)
        wsTicketsMu sync.Mutex
        wsTicketTTL = 30 * time.Second
        // v18.7.7 P1: max outstanding (unconsumed) tickets. 1024 is far above the
        // dashboard's normal usage (1 ticket per WS connect, consumed atomically on
        // Reserve). A legitimate user never hits this; only a flooding client does.
        maxWSTickets = 1024
)

// v18.7.8 P1: Sentinel errors for ticket issuance (was: issueWSTicket returned
// "" for both failure modes → handler guessed via len() → race condition).
var (
        ErrTicketLimit = errors.New("ticket store at capacity")
        ErrTicketRNG   = errors.New("RNG unavailable")
)

// issueWSTicket generates a single-use WS ticket valid for wsTicketTTL.
// Also sweeps any expired tickets so the map can't grow unboundedly under
// repeated POST /api/ws-ticket calls without a WS connect.
//
// v18.7.4 P0: Returns error on RNG failure (was: ignored → all-zeros ticket
// "0000...0000" valid for 30s → single predictable ticket for any unauthed
// caller who could guess the failure mode). Caller MUST treat error as failure
// and fail-closed (HTTP 500 — do NOT issue a predictable ticket).
//
// v18.7.5 P0: Removed /dev/urandom fallback (was: os.ReadFile("/dev/urandom")
// → /dev/urandom never sends EOF → infinite read / OOM if rand.Read ever fails).
// crypto/rand.Read on Linux already uses getrandom(2) (or /dev/urandom internally
// via the runtime) — there is no scenario where crypto/rand fails but a manual
// /dev/urandom read would succeed AND be safe. Fail-closed: return ErrTicketRNG.
//
// v18.7.7 P1: Returns ErrTicketLimit if the ticket store is at capacity
// (maxWSTickets). This caps memory usage under a flooding client (was: unlimited
// map growth until the next sweep — but the sweep only removes EXPIRED entries,
// so a fast loop creates entries faster than they expire). Caller returns HTTP 503.
//
// v18.7.8 P1: Returns (ticket, error) instead of just string (was: returned ""
// for both failure modes → handler re-checked len(wsTickets) to distinguish →
// RACE between issueWSTicket and the handler's len() check). Now: sentinel
// errors ErrTicketLimit / ErrTicketRNG are returned atomically under the lock,
// so the handler returns the correct HTTP status without any TOCTOU window.
func issueWSTicket() (string, error) {
        b := make([]byte, 32)
        // v18.7.5 P0: Remove urandom fallback (was: os.ReadFile → infinite read/OOM)
        // crypto/rand on Linux uses /dev/urandom internally. If it fails, fail-closed.
        if _, err := rand.Read(b); err != nil {
                return "", ErrTicketRNG // Signal RNG failure
        }
        ticket := hex.EncodeToString(b)
        wsTicketsMu.Lock()
        now := time.Now()
        for t, expiry := range wsTickets {
                if now.After(expiry) {
                        delete(wsTickets, t)
                }
        }
        // v18.7.7 P1: Rate limit — reject if too many unconsumed tickets outstanding.
        // (was: no cap → flooding client could exhaust memory. 1024 is far above the
        // dashboard's normal usage of 1 ticket per WS connect.)
        if len(wsTickets) >= maxWSTickets {
                wsTicketsMu.Unlock()
                return "", ErrTicketLimit // Signal rate-limited
        }
        wsTickets[ticket] = now.Add(wsTicketTTL)
        wsTicketsMu.Unlock()
        return ticket, nil
}

// ReserveWSTicket atomically removes the ticket from the store (single-use).
// v18.7.5 P0: Atomic reservation (was: PeekWSTicket didn't delete → two
// concurrent WS connects with the same ticket could both Peek=true and both
// Upgrade, defeating the single-use replay-resistance guarantee).
// Returns true iff the ticket was known and unexpired — in which case it has
// been removed atomically under the lock, so concurrent callers see false.
//
// v18.7.6 P0: Ticket is consumed once reserved — there is no longer a
// Release-on-Upgrade-failure path (was: ReleaseWSTicket re-added the ticket
// with a FRESH 30s TTL → attacker could repeatedly trigger Upgrade failures
// to extend ticket lifetime indefinitely). Now, if upgrader.Upgrade() fails,
// the ticket stays consumed and the legitimate client must re-POST
// /api/ws-ticket to obtain a new one. This is the simplest safe behavior:
// single-use is enforced unconditionally, and no client-controlled failure
// can refresh the TTL.
func ReserveWSTicket(ticket string) bool {
        if ticket == "" {
                return false
        }
        wsTicketsMu.Lock()
        defer wsTicketsMu.Unlock()
        expiry, ok := wsTickets[ticket]
        if !ok {
                return false
        }
        if time.Now().After(expiry) {
                delete(wsTickets, ticket)
                return false
        }
        // Atomically delete — ticket is now "reserved" (and consumed for good).
        delete(wsTickets, ticket)
        return true
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
//
// v18.7.4 P0: Fail-closed on RNG failure (was: ignored → all-zeros ticket).
// If issueWSTicket returns ErrTicketRNG, the runtime's RNG is unavailable and we
// must NOT issue a predictable ticket — return HTTP 500 so the dashboard surfaces
// the error rather than silently accepting a constant-ticket DoS vector.
//
// v18.7.7 P1: Also returns 503 if the ticket store is at capacity (maxWSTickets).
// This distinguishes RNG failure (500) from rate-limiting (503) so the dashboard
// can back off appropriately.
//
// v18.7.8 P1: Use sentinel errors instead of re-checking len(wsTickets) (was:
//   issueWSTicket returned "" for both failures → handler re-checked len() →
//   RACE: between issueWSTicket returning "" and the handler checking len(),
//   another goroutine could consume tickets → handler returns wrong status.
//   Now: errors.Is() on the returned error is atomic — no TOCTOU window.)
func (h *Handler) issueWSTicket(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
                writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
                return
        }
        ticket, err := issueWSTicket()
        if err != nil {
                if errors.Is(err, ErrTicketLimit) {
                        writeJSONError(w, http.StatusServiceUnavailable, "too many outstanding tickets — retry shortly")
                        return
                }
                // ErrTicketRNG (or any other unexpected error) — fail-closed.
                writeJSONError(w, http.StatusInternalServerError, "unable to generate secure ticket (RNG unavailable)")
                return
        }
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
