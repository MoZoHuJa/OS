package nodeagent

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

// The agent's HTTP surface (design §3.1) — exactly six routes, every one
// typed and authenticated. The machine-readable error vocabulary rides
// the §11.5 discipline; raw executor diagnostics never leave the node.
const (
	CodeUnauthenticated   = "IDENTITY_REQUIRED"
	CodeInvalidCommand    = "COMMAND_INVALID"
	CodeReservationMirror = "RESERVATION_MIRROR_REFUSED"
	CodeRuntimeNotAllowed = "RUNTIME_NOT_ALLOWED"
	CodeStartConflict     = "START_CONFLICT"
	CodeNotOwned          = "ALLOCATION_NOT_OWNED"
	CodeUnknownAllocation = "ALLOCATION_UNKNOWN"
	CodeInternal          = "AGENT_INTERNAL"
)

// maxBodyBytes bounds request bodies (typed small payloads — megabytes
// of "inventory" travel the other direction; a start/stop/notice is a
// few hundred bytes).
const maxBodyBytes = 64 * 1024

// SnapshotProvider supplies the node's inventory snapshot for the
// control plane's poll (production: inventory.BuildSnapshotV2; the
// production seam lives in the agent binary).
type SnapshotProvider interface {
	Current() (*inventory.InventorySnapshotV2, error)
}

// HealthReport is the typed /agent/v1/health answer.
type HealthReport struct {
	NodeID     string    `json:"node_id"`
	Status     string    `json:"status"` // "ok"
	AgentTime  time.Time `json:"agent_time"`
	Executions int       `json:"executions"`
	Mirror     int       `json:"mirror"`
}

// Server is the node agent's HTTP boundary. Construction is fail-closed:
// a node without identity or service refuses to serve.
//
// Idempotency allocation: request IDs travel the commands for the
// CONTROL-PLANE's replay protection (nodeidentity.ReplayGuard guards
// the CP's inbound mutations); the agent's own idempotency is
// ALLOCATION-STATE based — a retried start/stop of the same allocation
// converges through the service's own state gates (design §3.3).
type Server struct {
	nodeID    string
	service   *AgentService
	snapshots SnapshotProvider
	cpSecret  string
	clock     Clock
}

// NewServer wires the agent's HTTP boundary. cpSecret is the control-
// plane credential this agent accepts (provisioned at agent setup);
// snapshots may be nil (the inventory route answers UNAVAILABLE — an
// honest degraded answer, never a fabricated snapshot).
func NewServer(nodeID, cpSecret string, service *AgentService, snapshots SnapshotProvider, clock Clock) (*Server, error) {
	if strings.TrimSpace(nodeID) == "" {
		return nil, fmt.Errorf("nodeagent: the server requires the node ID")
	}
	if service == nil {
		return nil, fmt.Errorf("nodeagent: the server requires the agent service")
	}
	if strings.TrimSpace(cpSecret) == "" {
		return nil, fmt.Errorf("nodeagent: the control-plane credential is required — Tailscale is transport, not authorization (roadmap §9.2)")
	}
	if clock == nil {
		clock = SystemClock{}
	}
	return &Server{
		nodeID:    nodeID,
		service:   service,
		snapshots: snapshots,
		cpSecret:  cpSecret,
		clock:     clock,
	}, nil
}

// Handler builds the routed, authenticated http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/v1/health", s.handleHealth)
	mux.HandleFunc("/agent/v1/inventory", s.requireCP(s.handleInventory))
	mux.HandleFunc("/agent/v1/reservations", s.requireCP(s.handleNotify))
	mux.HandleFunc("/agent/v1/executions/start", s.requireCP(s.handleStart))
	mux.HandleFunc("/agent/v1/executions/stop", s.requireCP(s.handleStop))
	mux.HandleFunc("/agent/v1/executions/reconcile", s.requireCP(s.handleReconcile))
	return mux
}

// requireCP is the authentication middleware: every data-bearing route
// demands the control-plane credential (constant-time compare — network
// reachability authorizes nothing, roadmap §9.2).
func (s *Server) requireCP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.cpSecret)) != 1 {
			writeError(w, http.StatusUnauthorized, CodeUnauthenticated, "the control-plane credential is required")
			return
		}
		next(w, r)
	}
}

func bearerToken(r *http.Request) string {
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

// handleHealth answers the unauthenticated liveness probe (identity +
// counts only — no resource data, no secrets).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, CodeInvalidCommand, "health is GET")
		return
	}
	writeJSON(w, http.StatusOK, HealthReport{
		NodeID:     s.nodeID,
		Status:     "ok",
		AgentTime:  s.clock.Now(),
		Executions: len(s.service.Records()),
		Mirror:     len(s.service.Mirror()),
	})
}

// handleInventory answers the control plane's inventory poll.
func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, CodeInvalidCommand, "inventory is GET")
		return
	}
	if s.snapshots == nil {
		writeError(w, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "this agent has no snapshot provider wired")
		return
	}
	snap, err := s.snapshots.Current()
	if err != nil || snap == nil {
		writeError(w, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", fmt.Sprintf("the node snapshot is unavailable: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleNotify records the control plane's reservation mirror notice.
func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, CodeInvalidCommand, "reservations is POST")
		return
	}
	var notice ReservationNotice
	if !decodeBody(w, r, &notice) {
		return
	}
	if err := s.service.NotifyReservation(notice); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "mirrored"})
}

// handleStart executes the typed start.
func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, CodeInvalidCommand, "start is POST")
		return
	}
	var cmd StartCommand
	if !decodeBody(w, r, &cmd) {
		return
	}
	rec, err := s.service.Start(r.Context(), cmd)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// handleStop executes the typed stop.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, CodeInvalidCommand, "stop is POST")
		return
	}
	var cmd StopCommand
	if !decodeBody(w, r, &cmd) {
		return
	}
	rec, err := s.service.Stop(r.Context(), cmd)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// handleReconcile answers the reconciliation report: the agent's
// execution records + its reservation mirror (design §4.2's inputs).
func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, CodeInvalidCommand, "reconcile is POST")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"node_id":            s.nodeID,
		"reported_at":        s.clock.Now(),
		"executions":         s.service.Records(),
		"reservation_mirror": s.service.Mirror(),
	})
}

// decodeBody parses one bounded typed JSON body.
func decodeBody(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidCommand, fmt.Sprintf("malformed request body: %v", err))
		return false
	}
	return true
}

// writeServiceError maps the typed sentinel errors onto the wire codes
// (§11.5's stable vocabulary — HOST_BRIDGE_UNAVAILABLE included).
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidCommand):
		writeError(w, http.StatusBadRequest, CodeInvalidCommand, err.Error())
	case errors.Is(err, ErrReservationUnknown):
		writeError(w, http.StatusForbidden, CodeReservationMirror, err.Error())
	case errors.Is(err, ErrReservationReleased):
		writeError(w, http.StatusConflict, CodeReservationMirror, err.Error())
	case errors.Is(err, ErrRuntimeNotAllowed):
		writeError(w, http.StatusBadRequest, CodeRuntimeNotAllowed, err.Error())
	case errors.Is(err, ErrAlreadyRunning), errors.Is(err, ErrStartConflict):
		writeError(w, http.StatusConflict, CodeStartConflict, err.Error())
	case errors.Is(err, ErrAllocationNotOwned):
		writeError(w, http.StatusForbidden, CodeNotOwned, err.Error())
	case errors.Is(err, ErrAllocationUnknown):
		writeError(w, http.StatusNotFound, CodeUnknownAllocation, err.Error())
	case errors.Is(err, ErrExecutionUnavailable):
		writeError(w, http.StatusServiceUnavailable, "HOST_BRIDGE_UNAVAILABLE", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError emits the stable §11.5-style error envelope.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
