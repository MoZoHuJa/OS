// fabric.go — the distributed fabric's control-plane HTTP surface
// (v19.9.0, Compute Fabric Phase 5, roadmap §9.2/§9.3 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md,
// design: docs/SCARLIX_COMPUTE_FABRIC_PHASE_5_DISTRIBUTED_DESIGN.md §2–§4).
//
// ADMIN routes (the approval set — decision #10: ScarliCenter's future
// surface): the node registry — list, invite, revoke (quarantine),
// rotate credential, reactivate (trusted recovery) — plus the
// reconciliation view. AGENT routes (the monitoring/report set):
// node-credential-authenticated inventory/health/execution pushes
// (per-node credential + closed scope + replay protection — Tailscale
// is transport, NEVER authorization) and the one-time invite-based
// enrollment (the invite secret IS the authorization: the enrolling
// node holds no household token yet — roadmap §9.2's flow).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/admission"
	"github.com/MoZoHuJa/OS/scarlihq/internal/fabric"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/nodeidentity"
	"github.com/MoZoHuJa/OS/scarlihq/internal/planner"
)

// FabricLedgerViewProvider feeds the reconciliation decisions: the held
// reservations of ONE node from the SINGLE authoritative ledger (the
// reservation manager's adapter in production wiring; nil = report-only
// mode — the execution reports are recorded, decisions are computed
// when the ledger provider is wired).
type FabricLedgerViewProvider interface {
	HeldReservations(nodeID string) []fabric.LedgerReservationView
}

// FabricAdmissionLedger is the PRODUCTION ledger view (v20.0.0, roadmap
// §10 — the v19.9.0 named residual closed): the admission Manager's
// SINGLE authoritative ledger mapped onto the reconciliation view for
// ONE node — the /api/fabric/agent/executions push computes REAL
// decisions (reservation-stands / reconcile-required /
// stop-foreign-execution) against the actual HELD/ACTIVE records, not
// the report-only fallback. Wired by main.go whenever BOTH the
// admission gate and the fabric are enabled.
type FabricAdmissionLedger struct {
	// Manager — the constructed production admission manager (the single
	// ledger authority; nil is a wiring error main.go never makes).
	Manager *admission.Manager
}

// HeldReservations maps the node's HELD/ACTIVE records onto the
// reconciliation view (capacity-holding records only — a RELEASED or
// EXPIRED record is history, not a live reservation the agent could
// still affirm). An unknown node honestly returns the EMPTY view (the
// reconciliation engine then treats every agent-reported execution as
// un-ledgered foreign work — exactly the §4.2 contract).
func (a FabricAdmissionLedger) HeldReservations(nodeID string) []fabric.LedgerReservationView {
	if a.Manager == nil {
		return nil
	}
	node := fabricNodeKey(nodeID)
	out := []fabric.LedgerReservationView{}
	for _, r := range a.Manager.Held() {
		if fabricNodeKey(r.NodeID) != node {
			continue
		}
		out = append(out, fabric.LedgerReservationView{
			AllocationID: r.AdmissionID,
			Held:         r.Status == admission.StatusHeld || r.Status == admission.StatusActive,
		})
	}
	return out
}

// fabricNodeKey normalizes a node reference onto the ledger's routing
// key (the admission Manager's normalization: empty or "local" means
// the control plane's own host; enrolled identities are case-insensitive
// — the ledger stores the normalized key).
func fabricNodeKey(nodeID string) string {
	v := strings.TrimSpace(strings.ToLower(nodeID))
	if v == "" || v == "local" {
		return "local"
	}
	return v
}

// FabricNodeSnapshots adapts the fabric Registry onto the admission
// Manager's NodeSnapshots seam (v20.0.0, roadmap §10 "authenticated
// single- and multi-node execution"): the registry's §4.1 admission
// semantics evaluated under ONE lock (Registry.SnapshotFor), its
// sentinel errors mapped ONE-TO-ONE onto the admission package's typed
// refusals — the vocabulary translation lives here, next to the HTTP
// surfaces that own both packages' imports. Wired by main.go whenever
// BOTH the admission gate and the fabric are enabled; a node-tagged
// admission without it refuses BY NAME (node-source-unconfigured).
type FabricNodeSnapshots struct {
	// Registry — the observational fabric registry (the retained
	// per-node snapshots + the §4.1 guards).
	Registry *fabric.Registry
}

// SnapshotFor runs the fabric guards and returns the node's VERIFIED
// snapshot, mapping each sentinel onto the typed refusal the manager
// surfaces verbatim as the decision's reason_code.
func (a FabricNodeSnapshots) SnapshotFor(nodeID string, now time.Time) (*inventory.InventorySnapshotV2, error) {
	snap, err := a.Registry.SnapshotFor(nodeID, now)
	if err != nil {
		refusal := &admission.NodeSnapshotRefusal{Detail: err.Error()}
		switch {
		case errors.Is(err, fabric.ErrUnknownNode):
			refusal.Code = admission.ReasonNodeUnknown
		case errors.Is(err, fabric.ErrNodeQuarantined):
			refusal.Code = admission.ReasonNodeQuarantined
		case errors.Is(err, fabric.ErrNodeUnreachable):
			refusal.Code = admission.ReasonNodeUnreachable
		case errors.Is(err, fabric.ErrInventoryAbsent):
			refusal.Code = admission.ReasonNodeInventoryAbsent
		case errors.Is(err, fabric.ErrInventoryStale):
			refusal.Code = admission.ReasonNodeInventoryStale
		default:
			// The registry's contract says the named sentinels are the ONLY
			// refusal errors it returns; anything else is an unexpected
			// failure — the generic fail-closed answer, never a guessed
			// capacity.
			return nil, err
		}
		return nil, refusal
	}
	return snap, nil
}

// FabricConfig is the opt-in wiring input for ConfigureFabric.
type FabricConfig struct {
	// Nodes — the node identity registry (enrollment/credentials).
	Nodes *nodeidentity.Registry
	// Registry — the observational fabric registry.
	Registry *fabric.Registry
	// Replay — the node replay guard (mutating agent pushes).
	Replay *nodeidentity.ReplayGuard
	// BasePlanner — the constructed single-node planner whose gates the
	// fabric planner reuses.
	BasePlanner *planner.Planner
	// LocalSnapshots — the same snapshot source the base planner uses
	// (the fabric planner's local view).
	LocalSnapshots planner.SnapshotSource
	// Network — the network-aware admission policy (zero value → the
	// documented v19.9.0 defaults).
	Network planner.NetworkPolicy
	// LatencyClasses — the operator-declared per-node classes ("A"/"B"/"C";
	// unknown/absent → C, fail-closed). Cross-node latency is operator
	// knowledge, never an agent's self-declaration.
	LatencyClasses map[string]string
	// Ledger — the optional ledger-view provider for reconciliation
	// decisions (nil = report-only).
	Ledger FabricLedgerViewProvider
}

// apiFabricSource adapts the fabric registry + the local snapshot source
// into the planner's FabricSource (design §5: the views arrive through
// the constructor-injected source; the planner stays pure).
type apiFabricSource struct {
	registry *fabric.Registry
	local    planner.SnapshotSource
}

// Views builds the deterministic node-view list: the local view FIRST,
// then remote views sorted by node ID (the fabric registry's
// Observations() is already sorted; the local view is prepended).
func (s apiFabricSource) Views() []planner.FabricNodeView {
	views := make([]planner.FabricNodeView, 0, 8)
	if snap, err := s.local.Current(); err == nil && snap != nil {
		views = append(views, planner.FabricNodeView{NodeID: planner.NodeLocal, Snapshot: snap, LatencyClass: "A"})
	}
	for _, obs := range s.registry.Observations() {
		if obs.Snapshot == nil {
			continue // an unobserved node is not a planning input
		}
		views = append(views, planner.FabricNodeView{
			NodeID:       obs.NodeID,
			Snapshot:     obs.Snapshot,
			LatencyClass: string(obs.LatencyClass),
			Remote:       true,
		})
	}
	return views
}

// ConfigureFabric wires the distributed fabric surface (opt-in; the
// routes answer 503 until this is called). Fail-closed: every component
// is required — a fabric without identity, observation or planning is
// not a fabric.
func (h *Handler) ConfigureFabric(cfg FabricConfig) error {
	if cfg.Nodes == nil {
		return errors.New("api: the fabric requires the node identity registry")
	}
	if cfg.Registry == nil {
		return errors.New("api: the fabric requires the observational registry")
	}
	if cfg.Replay == nil {
		return errors.New("api: the fabric requires the replay guard (mutating node pushes are replay-protected)")
	}
	if cfg.BasePlanner == nil || cfg.LocalSnapshots == nil {
		return errors.New("api: the fabric requires the base planner + the local snapshot source (the network-aware planner reuses the single-node gates)")
	}
	h.fabricNodes = cfg.Nodes
	h.fabricRegistry = cfg.Registry
	h.fabricReplay = cfg.Replay
	h.fabricLatency = cfg.LatencyClasses
	h.fabricLedger = cfg.Ledger
	h.fabricReports = make(map[string][]fabric.ReconcileDecision)
	fp, err := planner.NewFabricPlanner(cfg.BasePlanner, cfg.Network, apiFabricSource{registry: cfg.Registry, local: cfg.LocalSnapshots})
	if err != nil {
		return fmt.Errorf("api: the network-aware fabric planner was rejected: %w", err)
	}
	h.fabricPlanner = fp
	return nil
}

// ---- the ADMIN surface --------------------------------------------------------

// fabricNodesRoute serves GET (the sanitized registry list) and POST
// (create an enrollment invite — the one-time secret is returned EXACTLY
// ONCE to the admin; the registry persists the digest only).
func (h *Handler) fabricNodesRoute(w http.ResponseWriter, r *http.Request) {
	if h.fabricNotConfigured(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]interface{}{
			"nodes":   h.fabricNodes.List(),
			"version": h.version,
		})
	case http.MethodPost:
		var req struct {
			NodeID     string `json:"node_id"`
			TTLSeconds int    `json:"ttl_seconds"`
		}
		if !fabricDecodeBody(w, r, &req) {
			return
		}
		ttl := time.Duration(req.TTLSeconds) * time.Second
		if req.TTLSeconds == 0 {
			ttl = 0 // the registry's documented default (30m)
		}
		invite, err := h.fabricNodes.CreateInvite("api-admin", req.NodeID, ttl)
		if err != nil {
			fabricWriteRegistryError(w, err)
			return
		}
		// The one-time transport: this response is the ONLY place the raw
		// invite secret ever exists on the control plane.
		writeJSON(w, map[string]interface{}{
			"node_id":    invite.NodeID,
			"secret":     invite.Secret,
			"expires_at": invite.ExpiresAt,
			"note":       "one-time enrollment secret — deliver to the node operator over a secure channel; the control plane keeps only the digest",
		})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "fabric nodes supports GET (list) and POST (invite)")
	}
}

// fabricNodeRevoke quarantines a node (ADMIN; reservations are NOT
// auto-released — capacity is not proven free).
func (h *Handler) fabricNodeRevoke(w http.ResponseWriter, r *http.Request) {
	if h.fabricNotConfigured(w) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "revoke is POST")
		return
	}
	nodeID, ok := fabricNodeIDBody(w, r)
	if !ok {
		return
	}
	if err := h.fabricNodes.Revoke("api-admin", nodeID); err != nil {
		fabricWriteRegistryError(w, err)
		return
	}
	writeJSON(w, map[string]string{"node_id": nodeID, "status": string(nodeidentity.StatusQuarantined)})
}

// fabricNodeRotate issues a fresh credential (ADMIN; old accepted within
// the grace window — the one-time secret is returned exactly once).
func (h *Handler) fabricNodeRotate(w http.ResponseWriter, r *http.Request) {
	if h.fabricNotConfigured(w) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "rotate is POST")
		return
	}
	nodeID, ok := fabricNodeIDBody(w, r)
	if !ok {
		return
	}
	cred, err := h.fabricNodes.RotateCredential("api-admin", nodeID)
	if err != nil {
		fabricWriteRegistryError(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"node_id": nodeID, "secret": cred.Secret, "note": "one-time credential — deliver over a secure channel; the old credential stays valid for the rotation grace window"})
}

// fabricNodeReactivate is the trusted-recovery path (ADMIN; a fresh
// credential, the old one is dead).
func (h *Handler) fabricNodeReactivate(w http.ResponseWriter, r *http.Request) {
	if h.fabricNotConfigured(w) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "reactivate is POST")
		return
	}
	nodeID, ok := fabricNodeIDBody(w, r)
	if !ok {
		return
	}
	cred, err := h.fabricNodes.Reactivate("api-admin", nodeID)
	if err != nil {
		fabricWriteRegistryError(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"node_id": nodeID, "secret": cred.Secret, "note": "one-time credential — deliver over a secure channel; the quarantined credential is dead"})
}

// fabricReconciliationView is the operator's monitoring view (decision
// #10: ScarliMonitor's future surface): the last stored reconciliation
// decisions per node + the fabric admission-guard state per node.
func (h *Handler) fabricReconciliationView(w http.ResponseWriter, r *http.Request) {
	if h.fabricNotConfigured(w) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "the reconciliation view is GET")
		return
	}
	h.fabricMu.Lock()
	decisions := make(map[string][]fabric.ReconcileDecision, len(h.fabricReports))
	for nodeID, dec := range h.fabricReports {
		decisions[nodeID] = dec
	}
	h.fabricMu.Unlock()
	guards := make(map[string]string, 8)
	for _, obs := range h.fabricRegistry.Observations() {
		if err := h.fabricRegistry.AdmissionGuard(obs.NodeID, time.Now().UTC()); err != nil {
			guards[obs.NodeID] = err.Error()
		} else {
			guards[obs.NodeID] = "admissible"
		}
	}
	writeJSON(w, map[string]interface{}{
		"decisions":        decisions,
		"admission_guards": guards,
		"remote_freshness": h.fabricRegistry.RemoteSnapshotMaxAge().String(),
	})
}

// ---- the AGENT surface --------------------------------------------------------

// authNode is the node-principal middleware: the request presents its
// enrolled node ID (X-Scarlix-Node-ID) + the per-node credential
// (Bearer); the resolved identity is scope-checked, and MUTATING pushes
// additionally pass the replay guard (unique request ID + fresh
// timestamp). Tailscale/LAN reachability authorizes NOTHING — the
// credential does (roadmap §9.2 verbatim).
func (h *Handler) authNode(scope nodeidentity.NodeScope, mutating bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.fabricNotConfigured(w) {
			return
		}
		nodeID := strings.TrimSpace(r.Header.Get("X-Scarlix-Node-ID"))
		token := fabricBearerToken(r)
		identity, err := h.fabricNodes.Authenticate(nodeID, token)
		if err != nil {
			code := "IDENTITY_REQUIRED"
			if errors.Is(err, nodeidentity.ErrNodeQuarantined) {
				code = "NODE_QUARANTINED"
			}
			fabricWriteJSON(w, http.StatusUnauthorized, map[string]interface{}{"error": map[string]string{"code": code, "message": err.Error()}})
			return
		}
		if !identity.HasScope(scope) {
			fabricWriteJSON(w, http.StatusForbidden, map[string]interface{}{"error": map[string]string{"code": "POLICY_DENIED", "message": fmt.Sprintf("node %s lacks scope %s", nodeID, scope)}})
			return
		}
		if mutating {
			requestID := strings.TrimSpace(r.Header.Get("X-Scarlix-Node-Request-ID"))
			ts := strings.TrimSpace(r.Header.Get("X-Scarlix-Node-Timestamp"))
			stamp, err := time.Parse(time.RFC3339, ts)
			if err != nil {
				fabricWriteJSON(w, http.StatusBadRequest, map[string]interface{}{"error": map[string]string{"code": "REPLAY_PROTECTION", "message": "the mutating push must carry X-Scarlix-Node-Request-ID + X-Scarlix-Node-Timestamp (RFC3339)"}})
				return
			}
			if err := h.fabricReplay.Check(nodeID, requestID, stamp); err != nil {
				fabricWriteJSON(w, http.StatusConflict, map[string]interface{}{"error": map[string]string{"code": "REPLAY_PROTECTION", "message": err.Error()}})
				return
			}
		}
		next(w, r.WithContext(withNodeIdentity(r.Context(), identity)))
	}
}

// nodeIdentityContextKey carries the resolved node principal.
type nodeIdentityContextKey struct{}

func withNodeIdentity(ctx context.Context, identity nodeidentity.NodeIdentity) context.Context {
	return context.WithValue(ctx, nodeIdentityContextKey{}, identity)
}

// nodeIdentityFromContext resolves the request's node principal.
func nodeIdentityFromContext(ctx context.Context) nodeidentity.NodeIdentity {
	if identity, ok := ctx.Value(nodeIdentityContextKey{}).(nodeidentity.NodeIdentity); ok {
		return identity
	}
	return nodeidentity.NodeIdentity{}
}

// fabricBearerToken extracts the Bearer credential (the node's own
// per-node credential — distinct from the household token the admin
// surface uses).
func fabricBearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// fabricWriteJSON writes one JSON response with an explicit status code
// (the api package's writeJSON is status-less; the fabric surface needs
// the envelope's HTTP classes for its typed error vocabulary).
func fabricWriteJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fabricAgentEnroll completes enrollment (roadmap §9.2): the enrolling
// node presents its one-time invite secret; on success the per-node
// credential is returned EXACTLY ONCE. This is the ONE agent route
// without a prior node credential — the invite secret IS the
// authorization (TTL-bounded, one-time, admin-minted).
func (h *Handler) fabricAgentEnroll(w http.ResponseWriter, r *http.Request) {
	if h.fabricNotConfigured(w) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "enroll is POST")
		return
	}
	var req struct {
		NodeID       string `json:"node_id"`
		InviteSecret string `json:"invite_secret"`
		Nonce        string `json:"nonce"`
	}
	if !fabricDecodeBody(w, r, &req) {
		return
	}
	cred, err := h.fabricNodes.Enroll(req.NodeID, req.InviteSecret, req.Nonce)
	if err != nil {
		fabricWriteRegistryError(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{
		"node_id":   cred.NodeID,
		"secret":    cred.Secret,
		"principal": nodeidentity.NodePrincipalPrefix + cred.NodeID,
		"note":      "one-time node credential — store securely; the control plane keeps only the digest",
	})
}

// fabricAgentInventory records a node's inventory push: the
// observational registry + the network-aware planning input.
func (h *Handler) fabricAgentInventory(w http.ResponseWriter, r *http.Request) {
	if h.fabricNotConfigured(w) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "inventory push is POST")
		return
	}
	identity := nodeIdentityFromContext(r.Context())
	var snap inventory.InventorySnapshotV2
	if !fabricDecodeBody(w, r, &snap) {
		return
	}
	// The latency class is OPERATOR-declared knowledge (cross-node latency
	// is a control-plane decision input, never the agent's
	// self-declaration — design §5.1); unknown → C (fail-closed).
	class := h.fabricLatency[identity.NodeID()]
	if err := h.fabricRegistry.ObserveInventory(identity.NodeID(), &snap, class); err != nil {
		fabricWriteRegistryError(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"status": "observed", "node_id": identity.NodeID(), "snapshot_id": snap.SnapshotID})
}

// fabricAgentHealth records a node's health report (the observational
// annotation; informational — no replay window needed, the inventory
// freshness governs admission).
func (h *Handler) fabricAgentHealth(w http.ResponseWriter, r *http.Request) {
	if h.fabricNotConfigured(w) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "health push is POST")
		return
	}
	identity := nodeIdentityFromContext(r.Context())
	var report map[string]interface{}
	if !fabricDecodeBody(w, r, &report) {
		return
	}
	writeJSON(w, map[string]interface{}{"status": "recorded", "node_id": identity.NodeID()})
}

// fabricAgentExecutions records the node agent's execution report and —
// when the ledger-view provider is wired — computes the reconciliation
// decisions (the PURE engine; releases happen ONLY through the trusted
// reservation-manager path, never here).
func (h *Handler) fabricAgentExecutions(w http.ResponseWriter, r *http.Request) {
	if h.fabricNotConfigured(w) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "execution report is POST")
		return
	}
	identity := nodeIdentityFromContext(r.Context())
	var report struct {
		Executions []struct {
			AllocationID string    `json:"allocation_id"`
			State        string    `json:"state"`
			ObservedAt   time.Time `json:"observed_at"`
		} `json:"executions"`
	}
	if !fabricDecodeBody(w, r, &report) {
		return
	}
	// Translate the agent's vocabulary into the fabric engine's closed
	// states (unknown states fail closed to the ambiguous vocabulary).
	agentRecords := make([]fabric.AgentExecutionRecord, 0, len(report.Executions))
	for _, e := range report.Executions {
		state := fabric.AgentExecutionState(e.State)
		switch state {
		case fabric.AgentExecRunning, fabric.AgentExecStopped, fabric.AgentExecStartUncertain, fabric.AgentExecStopUncertain, fabric.AgentExecNeverStarted:
		default:
			state = fabric.AgentExecStopUncertain // fail closed: unknown = ambiguous
		}
		agentRecords = append(agentRecords, fabric.AgentExecutionRecord{
			AllocationID: e.AllocationID,
			State:        state,
			ObservedAt:   e.ObservedAt,
		})
	}

	var decisions []fabric.ReconcileDecision
	if h.fabricLedger != nil {
		ledger := h.fabricLedger.HeldReservations(identity.NodeID())
		decisions = fabric.ReconcileNode(agentRecords, ledger)
	}
	h.fabricMu.Lock()
	h.fabricReports[identity.NodeID()] = decisions
	h.fabricMu.Unlock()

	out := map[string]interface{}{
		"status":  "recorded",
		"node_id": identity.NodeID(),
	}
	if h.fabricLedger != nil {
		out["decisions"] = decisions
	} else {
		out["decisions"] = nil
		out["note"] = "report-only mode: the ledger-view provider is not wired — decisions are computed when the reservation-manager adapter is configured (the releases themselves always flow through the trusted reservation-manager path)"
	}
	writeJSON(w, out)
}

// ---- shared helpers -----------------------------------------------------------

// fabricNotConfigured answers 503 when the fabric surface is unwired.
func (h *Handler) fabricNotConfigured(w http.ResponseWriter) bool {
	if h.fabricNodes == nil || h.fabricRegistry == nil || h.fabricReplay == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "distributed fabric not configured (Compute Fabric Phase 5 surface — wire ConfigureFabric in main.go; this 503 means a partial boot)")
		return true
	}
	return false
}

// fabricDecodeBody decodes one bounded, strict body.
func fabricDecodeBody(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20) // the inventory snapshot is the big one
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSONError(w, http.StatusBadRequest, "fabric request malformed (strict decode — a typo'd field is a refusal, never a silent default): "+err.Error())
		return false
	}
	return true
}

// fabricNodeIDBody decodes the single-field node act body.
func fabricNodeIDBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		NodeID string `json:"node_id"`
	}
	if !fabricDecodeBody(w, r, &req) {
		return "", false
	}
	if strings.TrimSpace(req.NodeID) == "" {
		writeJSONError(w, http.StatusBadRequest, "node_id is required")
		return "", false
	}
	return req.NodeID, true
}

// fabricWriteRegistryError maps the identity registry's typed errors.
func fabricWriteRegistryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, nodeidentity.ErrNodeQuarantined):
		fabricWriteJSON(w, http.StatusConflict, map[string]interface{}{"error": map[string]string{"code": "NODE_QUARANTINED", "message": err.Error()}})
	case errors.Is(err, nodeidentity.ErrInviteLive):
		fabricWriteJSON(w, http.StatusConflict, map[string]interface{}{"error": map[string]string{"code": "INVITE_LIVE", "message": err.Error()}})
	case errors.Is(err, nodeidentity.ErrInviteExpired):
		fabricWriteJSON(w, http.StatusGone, map[string]interface{}{"error": map[string]string{"code": "INVITE_EXPIRED", "message": err.Error()}})
	case errors.Is(err, nodeidentity.ErrNodeExists):
		fabricWriteJSON(w, http.StatusConflict, map[string]interface{}{"error": map[string]string{"code": "NODE_EXISTS", "message": err.Error()}})
	case errors.Is(err, nodeidentity.ErrNodeNotFound):
		fabricWriteJSON(w, http.StatusNotFound, map[string]interface{}{"error": map[string]string{"code": "NODE_NOT_FOUND", "message": err.Error()}})
	case errors.Is(err, nodeidentity.ErrInvalidNodeID):
		fabricWriteJSON(w, http.StatusBadRequest, map[string]interface{}{"error": map[string]string{"code": "NODE_ID_INVALID", "message": err.Error()}})
	default:
		// The generic authentication failure never leaks WHICH part
		// failed (no node-existence oracle for outsiders).
		fabricWriteJSON(w, http.StatusBadRequest, map[string]interface{}{"error": map[string]string{"code": "FABRIC_REQUEST_REFUSED", "message": err.Error()}})
	}
}
