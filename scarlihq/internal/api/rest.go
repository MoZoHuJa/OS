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

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentfabric"
	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airt"
	"github.com/MoZoHuJa/OS/scarlihq/internal/gaming"
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
	"github.com/MoZoHuJa/OS/scarlihq/internal/status"
	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

// Version is the fallback default for /api/health when Handler has no version passed.
// v18.2 P1: main.go now passes Version to NewHandler — this is only used if not set.
var Version = "19.6.8"

// v18.7.3 P1: Short-lived WS ticket store (replaces permanent token in URL).
// Tickets are 32-byte random hex strings, valid for 30s, single-use.
// The dashboard POSTs /api/ws-ticket (Bearer-authed) to obtain a ticket,
// then connects to /ws?ticket=<one-time> — the URL no longer carries the
// permanent SCARLIHQ_TOKEN (was: leaked into logs/history/referrer headers).
//
// v18.7.7 P1: Rate limit max outstanding tickets (was: a single holder of
//
//	SCARLIHQ_TOKEN could POST /api/ws-ticket in a tight loop and create
//	unlimited map entries during the 30s TTL — the sweep only runs on the
//	NEXT issueWSTicket call, so 100k requests could allocate 100k entries
//	before any expired. Now: maxWSTickets=1024 caps the map size — if the
//	store is full, issueWSTicket returns ErrTicketLimit and the HTTP handler
//	returns 503 so the dashboard backs off instead of exhausting memory.)
//
// v18.7.8 P1: Use error type instead of returning "" + guessing (was:
//
//	issueWSTicket returned "" for BOTH RNG failure and rate-limit → handler
//	re-checked len(wsTickets) to distinguish → RACE: between issueWSTicket
//	returning "" and the handler checking len(), another goroutine could
//	consume tickets → len < 1024 → handler returns 500 (RNG failure) even
//	though the real reason was 503 (rate-limit). Now: issueWSTicket returns
//	(ticket, err) with sentinel errors ErrTicketLimit / ErrTicketRNG so
//	the handler can return the correct status without re-checking state.)
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
	guard            *guard.Guard
	mode             *scarlix_mode.Mode
	profiles         *profiles.Manager
	authToken        string
	version          string // v18.2: passed from main (ldflags-injected)
	computeManager   *workload.Manager
	computePrincipal string
	// AK-2 (v19.4.1): Agent Kernel session surface — nil until
	// ConfigureAgentKernel is called (opt-in, like the compute lifecycle).
	agentStore *agentkernel.Store
	// AK-3 (v19.4.4): persisted admin-owned grant registry — nil until
	// ConfigureAgentGrants is called (separate opt-in; 503 without it).
	agentPolicy agentkernel.Policy
	agentGrants *agentkernel.GrantRegistry
	// AK-4 (v19.4.5): append-only audit sink — nil until
	// ConfigureAgentAudit is called (separate opt-in; 503 without it).
	agentAudit *agentkernel.AuditSink
	// BF-1 (v19.5.0): Agent Fabric command store (ScarliBar backend) —
	// nil until ConfigureAgentFabric is called (opt-in; 503 without it).
	agentFabric *agentfabric.CommandStore
	// BF-2 (v19.5.1): Agent Fabric workspace store — nil until
	// ConfigureAgentWorkspaces is called (wired whenever the fabric is
	// enabled; 503 without it).
	agentWorkspaces *agentfabric.WorkspaceStore
	// BF-3 (v19.5.2): Agent Fabric execution store (executions.json) —
	// nil until ConfigureAgentFabricExecutions is called (rides the
	// fabric opt-in; 503 without it).
	agentExecutions *agentfabric.ExecutionStore
	// BF-4 (v19.5.3): Agent Fabric approval store (approvals.json) —
	// nil until ConfigureAgentFabricApprovals is called (rides the
	// fabric opt-in; 503 without it — mutating acts never bypass the
	// human gate).
	agentApprovals *agentfabric.ApprovalStore
	// CF-3 (v19.6.2): Agent Fabric proposal store (proposals.json) —
	// the LLM propose surface's persisted entity (an LLM response
	// becomes a fabric PROPOSAL, never an auto-submitted command).
	// Nil until ConfigureAgentFabricProposals is called (rides the
	// fabric opt-in; 503 without it).
	agentProposals *agentfabric.ProposalStore
	// BF-3 (v19.5.2): Agent Fabric change-signal bus — the /ws/bar push
	// backbone (bounded 16×16, drop-not-block, no replay). Nil until
	// ConfigureAgentFabricExecutions is called.
	agentBus *agentfabric.FabricBus
	// CF-2 (v19.6.1): Universal AI Runtime — the normalized LLM layer
	// (adapters + probing + failover chat behind the CF-1 contract).
	// Nil until ConfigureAIRuntime is called (opt-in; 503 without it).
	aiRuntime *airt.Runtime
	// v19.6.7 (KPK-19.6.6-P1-01): the shared authenticator — Bearer
	// tokens resolve to identity-bound principals (per-profile token
	// index + the household admin token); every policy-aware boundary
	// decides through the ONE profiles.Authenticator.Authorize.
	authz *profiles.Authenticator
	// v19.6.7 (KPK-19.6.6-P2-02): the enforced monthly token budget
	// ledger — nil (informational budgets) until ConfigureBudgetLedger
	// is called; when set, /api/ai/chat reserves before forwarding and
	// settles the provider-reported usage after.
	budget         *profiles.BudgetLedger
	budgetEnforced bool
}

// NewHandler creates a new API handler.
// v18.2 P1: version param added (was: separate const — ldflags couldn't override).
// v19.6.7 (KPK-19.6.6-P1-01): the shared authenticator is constructed here
// (single-user: the household token is the admin identity); the per-profile
// token index is attached later via ConfigureProfileAuthz (multi-user).
func NewHandler(g *guard.Guard, m *scarlix_mode.Mode, p *profiles.Manager, authToken, version string) *Handler {
	if version == "" {
		version = Version // fallback to package var
	}
	h := &Handler{guard: g, mode: m, profiles: p, authToken: authToken, version: version}
	if authToken != "" && p != nil {
		authz, err := profiles.NewAuthenticator(nil, authToken, p)
		if err == nil {
			h.authz = authz
		}
		// A nil authz (no profiles manager) leaves the legacy 503/401
		// behavior — never an unauthenticated open surface.
	}
	return h
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
	// v19.3.1: read-only gaming-stack inventory (Gaming Stack Integration Plan,
	// Phase 4). Serves the host-bridge-detected entries only — no launch, no
	// install, no arbitrary command surface is added here.
	mux.HandleFunc("/api/gaming", h.auth(h.gamingInventory))
	mux.HandleFunc("/api/ws-ticket", h.auth(h.issueWSTicket))
	// v19.6.7 (KPK-19.6.6-P1-01): operator surfaces require the ADMIN
	// principal — a per-profile family token is refused here (403), identity
	// is bound at the auth boundary, not self-declared at the action.
	mux.HandleFunc("/api/compute/start", h.authAdmin(h.computeStart))
	mux.HandleFunc("/api/compute/stop", h.authAdmin(h.computeStop))
	// AK-2 (v19.4.1): Agent Kernel session surface — auth-gated like every
	// other route; responds 503 until ConfigureAgentKernel has been called
	// (opt-in wiring from main.go, same contract as compute lifecycle).
	mux.HandleFunc("/api/agent/sessions", h.authAdmin(h.agentSessions))
	mux.HandleFunc("/api/agent/sessions/transition", h.authAdmin(h.agentSessionTransition))
	mux.HandleFunc("/api/agent/sessions/evaluate", h.authAdmin(h.agentSessionEvaluate))
	// AK-5 (v19.4.6): compute intake — agent sessions reserve/release
	// compute allocations through the kernel decision + the workload
	// bridge (per-agent-owner principal; registry required).
	mux.HandleFunc("/api/agent/sessions/compute", h.authAdmin(h.agentCompute))
	// AK-3 (v19.4.4): persisted, admin-owned grant registry — grant /
	// revoke / disable / enable are separate reviewed admin acts, gated
	// behind the same Bearer auth as everything else and 503 until
	// ConfigureAgentGrants has been called (own opt-in).
	mux.HandleFunc("/api/agent/grants", h.authAdmin(h.agentGrantsRoute))
	mux.HandleFunc("/api/agent/grants/revoke", h.authAdmin(h.agentGrantRevoke))
	mux.HandleFunc("/api/agent/capabilities/disable", h.authAdmin(h.agentCapabilityDisable))
	mux.HandleFunc("/api/agent/capabilities/enable", h.authAdmin(h.agentCapabilityEnable))
	// AK-4 (v19.4.5): append-only audit sink read surface — bounded
	// (last N lines, newest first), read-only, 503 until the sink is
	// configured (own opt-in: SCARLIHQ_AGENT_AUDIT_FILE).
	mux.HandleFunc("/api/agent/audit", h.authAdmin(h.agentAuditRoute))

	// BF-1 (v19.5.0): Agent Fabric / ScarliBar command surface — intent
	// capture + user-visible command status (P4 ownership). All behind
	// the standard Bearer auth; 503 until the fabric is configured.
	mux.HandleFunc("/api/bar/commands", h.authAdmin(h.barCommands))
	mux.HandleFunc("/api/bar/commands/route", h.authAdmin(h.barCommandRoute))
	mux.HandleFunc("/api/bar/commands/cancel", h.authAdmin(h.barCommandCancel))
	// BF-2 (v19.5.1): Agent Fabric workspace surface — named isolation
	// contexts for agent sessions and commands. Same auth gate; 503 until
	// the workspace store is configured (it is wired whenever the fabric
	// is enabled — no extra opt-in beyond the fabric itself).
	mux.HandleFunc("/api/bar/workspaces", h.authAdmin(h.barWorkspaces))
	mux.HandleFunc("/api/bar/workspaces/archive", h.authAdmin(h.barWorkspaceArchive))
	mux.HandleFunc("/api/bar/workspaces/sessions", h.authAdmin(h.barWorkspaceJoin))
	mux.HandleFunc("/api/bar/workspaces/sessions/leave", h.authAdmin(h.barWorkspaceLeave))
	// BF-3 (v19.5.2): Agent Fabric execution surface — coding/research
	// agent runtimes. Same auth gate; 503 until the execution store is
	// configured (rides the fabric opt-in with its own persisted
	// executions file). Execute runs a FRESH kernel evaluation (the
	// stale-allow guard) before the act; executions are terminal records.
	mux.HandleFunc("/api/bar/commands/execute", h.authAdmin(h.barCommandExecute))
	mux.HandleFunc("/api/bar/executions", h.authAdmin(h.barExecutions))
	// BF-4 (v19.5.3): Agent Fabric operator-approval surface — the human
	// gate for mutating (coding) acts. Same auth gate; 503 until the
	// approval store is configured (rides the fabric opt-in). Execute on
	// a coding capability creates the PENDING request (202); approve runs
	// the act (the full BF-3 flow + the operator's yes); reject is
	// terminal. One request per command, ever.
	mux.HandleFunc("/api/bar/approvals", h.authAdmin(h.barApprovals))
	mux.HandleFunc("/api/bar/approvals/approve", h.authAdmin(h.barApprovalApprove))
	mux.HandleFunc("/api/bar/approvals/reject", h.authAdmin(h.barApprovalReject))
	// CF-2 (v19.6.1): Universal AI Runtime surface — the normalized LLM
	// layer (adapters for the documented local stack, health probing,
	// failover chat). Same Bearer auth gate; 503 until the runtime is
	// configured (SCARLIHQ_AIRUNTIME_ENABLED=true, own opt-in). Chat is
	// transport-only: it mutates nothing on the host — an LLM response
	// becoming a fabric PROPOSAL is CF-3, behind the fabric's approvals.
	mux.HandleFunc("/api/ai/health", h.auth(h.aiHealth))
	mux.HandleFunc("/api/ai/models", h.auth(h.aiModels))
	// v19.6.7 (KPK-19.6.6-P1-01): the probe round is an operator act —
	// the ADMIN principal is required (family tokens are read-only on the
	// AI surface; /api/ai/chat is the family boundary, policy-decided).
	mux.HandleFunc("/api/ai/probe", h.authAdmin(h.aiProbe))
	mux.HandleFunc("/api/ai/chat", h.auth(h.aiChat))
	// CF-3 (v19.6.2): the propose surface — the bridge between the AI
	// runtime and the fabric. POST /api/ai/propose runs a chat through
	// the CF-2 transport and captures a well-formed ```scarlix-proposal
	// block as a PENDING proposal (pure parser, attribution: provider +
	// model); the review surface lists them; accept is the ONLY door
	// from a proposal to a command (the BF-1 capture path + the bar's
	// routing + the reconciliation for a crashed accept); dismiss is
	// the operator's unconditional no. 503 until BOTH opt-ins are on
	// (the runtime AND the fabric's proposal store).
	mux.HandleFunc("/api/ai/propose", h.authAdmin(h.aiPropose))
	mux.HandleFunc("/api/ai/proposals", h.authAdmin(h.aiProposals))
	mux.HandleFunc("/api/ai/proposals/accept", h.authAdmin(h.aiProposalAccept))
	mux.HandleFunc("/api/ai/proposals/dismiss", h.authAdmin(h.aiProposalDismiss))
}

// auth middleware: validates the Bearer token header only.
// v18.0.0 P1: uses crypto/subtle.ConstantTimeCompare (was: custom secureCompare).
// v18.6 P2: removed ?token= query fallback (was: token leaked in URLs/logs/history).
// v19.6.7 (KPK-19.6.6-P1-01): the token now RESOLVES to an identity-bound
// principal (per-profile token index first — constant-time per entry; the
// household token = the ADMIN identity) which rides the request context to
// the policy-aware boundaries. The 401/503 semantics for the household
// token are byte-for-byte the legacy contract.
func (h *Handler) auth(next http.HandlerFunc) http.HandlerFunc {
	return h.authGate(next, false)
}

// authAdmin is the auth middleware for OPERATOR surfaces: the request
// additionally requires the ADMIN principal (the household token or a
// role:admin profile's own token). A family member's per-profile token is
// refused 403 there — identity is bound, not self-declared.
func (h *Handler) authAdmin(next http.HandlerFunc) http.HandlerFunc {
	return h.authGate(next, true)
}

func (h *Handler) authGate(next http.HandlerFunc, requireAdmin bool) http.HandlerFunc {
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
		if h.authz == nil {
			// No authenticator (no profile manager): the legacy constant-time
			// household-token check stands on its own.
			if subtle.ConstantTimeCompare([]byte(token), []byte(h.authToken)) != 1 {
				writeJSONError(w, http.StatusUnauthorized, "invalid or missing token")
				return
			}
			next(w, r)
			return
		}
		principal := h.authz.ResolveToken(token)
		if principal == nil {
			writeJSONError(w, http.StatusUnauthorized, "invalid or missing token")
			return
		}
		if requireAdmin && !principal.IsAdmin {
			writeJSONError(w, http.StatusForbidden, "this surface requires the household (admin) token — a per-profile family token is refused here (KPK-19.6.6-P1-01)")
			return
		}
		next(w, r.WithContext(profiles.WithPrincipal(r.Context(), principal)))
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

// gamingInventory serves the read-only gaming-stack inventory detected by the
// host bridge (Gaming Stack Integration Plan, Phase 4). It is strictly
// read-only: ScarliHQ detects nothing itself and cannot launch or install
// anything — the manifest-driven detection runs host-side in
// scarlix-host-bridge and is published through host-status.json.
// Entries are always a JSON array (never null) so clients need no nil branch.
func (h *Handler) gamingInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "gaming inventory is read-only (GET)")
		return
	}
	s := status.ReadOrStale()
	entries := s.Gaming
	if entries == nil {
		entries = []gaming.Entry{}
	}
	writeJSON(w, map[string]interface{}{
		"gaming":    entries,
		"timestamp": s.Timestamp,
		"stale":     s.Stale,
	})
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
	profile := r.URL.Query().Get("profile")
	if r.Header.Get("X-Scarlix-Profile") != "" && profile == "" {
		profile = r.Header.Get("X-Scarlix-Profile")
	}
	if mode == "" || profile == "" {
		// v18.6 P1: Limit request body size (was: no limit → DoS with large body)
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if mode == "" {
				mode = body["mode"]
			}
			if profile == "" {
				profile = body["profile"]
			}
		}
	}
	if mode == "" {
		writeJSONError(w, http.StatusBadRequest, "no mode specified")
		return
	}
	// v19.6.7 (KPK-19.6.6-P1-01): SERVER-SIDE profile policy enforcement
	// at the action boundary, now IDENTITY-BOUND — the profile reference
	// (query/body field or X-Scarlix-Profile header) is checked against
	// the AUTHENTICATED principal through the ONE shared
	// profiles.Authenticator.Authorize: a family token cannot select a
	// foreign profile (an admin act-as can only REDUCE privileges); an
	// omitted reference is DERIVED from the principal (their own policy;
	// the household token derives the role:admin profile) — there is no
	// policy-free path in multi-user mode. An invalid/unloadable profile
	// reference FAILS CLOSED (400): a broken policy file can never become
	// an unenforced bypass (the v19.6.6 contract, preserved).
	p, ok := h.authorizeProfile(w, r, profile, "mode.switch")
	if !ok {
		return
	}
	if p != nil {
		if err := p.AllowsMode(mode); err != nil {
			writeJSONError(w, http.StatusForbidden, err.Error())
			return
		}
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

// listProfiles returns the FULL policy-bearing profile set plus the
// EXPLICIT issues of invalid profile files (v19.6.6, KPK-19.6.5-P1-03):
// every profile now serializes its declared policy (permissions,
// scarlix_mode, token_budget, content_filter), and a file that fails to
// parse or validate is EXCLUDED from the list and surfaced in "invalid" —
// never silently converted into a filename-only fallback profile that
// looked permissively fine. An empty directory is an empty list and an
// empty issues array (a stable JSON shape, not nulls).
func (h *Handler) listProfiles(w http.ResponseWriter, r *http.Request) {
	profs, issues := h.profiles.List()
	if profs == nil {
		profs = []profiles.Profile{}
	}
	if issues == nil {
		issues = []profiles.Issue{}
	}
	writeJSON(w, map[string]interface{}{
		"profiles": profs,
		"invalid":  issues,
	})
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
//
//	issueWSTicket returned "" for both failures → handler re-checked len() →
//	RACE: between issueWSTicket returning "" and the handler checking len(),
//	another goroutine could consume tickets → handler returns wrong status.
//	Now: errors.Is() on the returned error is atomic — no TOCTOU window.)
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
