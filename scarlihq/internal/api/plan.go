// Package api — the deterministic execution planner surface (v19.7.2,
// Compute Fabric Phase 3, roadmap §7.4/§7.6).
//
// One route, two methods, behind the standard Bearer READ gate (the
// planner RESERVES NOTHING — it computes a proposal over the same
// observed data the inventory serves; a strictly weaker act than the
// ADMIN-gated admission):
//
//	GET  /api/compute/plan  — the operator view: the planner policy +
//	                          the closed backend capability registry
//	POST /api/compute/plan  — plan: compute the deterministic
//	                          execution plan (or the named refusal)
//
// The POST response is ALWAYS a result object — a refusal is
// 200 + selected=false + the machine-readable reason_code (the planner
// working, never an HTTP error); malformed JSON and unknown fields are
// 400 (strict decode — a typo'd field is a refusal, not a silent
// default); unsupported methods are 405 BY NAME. The principal is
// stamped SERVER-SIDE from the resolved token identity (a caller
// cannot attribute its plan to someone else). The request carries NO
// selection-steering field (roadmap §7.6 — LLM output never becomes
// directly executable); the deterministic selection order belongs to
// the operator-owned policy alone.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
	"github.com/MoZoHuJa/OS/scarlihq/internal/planner"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
)

// ConfigurePlanner attaches the deterministic execution planner (the
// surface answers 503 until this is called — the planner is wired in
// main.go unconditionally, but tests and partial boots may skip it).
func (h *Handler) ConfigurePlanner(p *planner.Planner) {
	h.executionPlanner = p
}

// planRoute serves GET (the operator view: policy + registry) and POST
// (compute the plan).
func (h *Handler) planRoute(w http.ResponseWriter, r *http.Request) {
	if h.executionPlanner == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "execution planner not configured (Compute Fabric Phase 3 surface — the planner wires automatically in production; this 503 means a partial boot)")
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.planState(w)
	case http.MethodPost:
		h.planCompute(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "plan supports GET (policy + registry view) and POST (compute a plan)")
	}
}

// planState reports the planner policy + the CLOSED capability
// registry (the operator's read-only view of §7.2's administrator-
// owned rows — there is no registration surface: no HTTP route, no
// env var, no file can add a backend row).
func (h *Handler) planState(w http.ResponseWriter) {
	writeJSON(w, map[string]interface{}{
		"policy":   h.executionPlanner.Policy(),
		"backends": airuntime.RegistryView(),
		"version":  h.version,
	})
}

// planCompute computes the deterministic plan (or the named refusal).
func (h *Handler) planCompute(w http.ResponseWriter, r *http.Request) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	var req planner.ExecutionRequest
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "plan request malformed (strict decode — a typo'd field is a refusal, never a silent default): "+err.Error())
		return
	}
	// The authenticated principal is stamped SERVER-SIDE from the
	// resolved token identity — a caller cannot attribute its plan to
	// someone else, and an LLM proposal can only ride in as this typed
	// operator-approved request (roadmap §7.6's boundary).
	if p := profiles.PrincipalFromContext(r.Context()); p != nil {
		req.Principal = p.Name
	}
	writeJSON(w, h.executionPlanner.Plan(req))
}
