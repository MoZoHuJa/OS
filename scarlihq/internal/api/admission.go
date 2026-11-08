// Package api — the aggregate admission surface (v19.7.1, Compute
// Fabric Phase 2, roadmap §6.4).
//
// Two ADMIN-gated routes (admission RESERVES capacity — a strictly
// stronger act than the read-only inventory, so it takes the admin
// gate, not the observation gate):
//
//	POST /api/compute/admission           — decide + atomically reserve
//	GET  /api/compute/admission           — the held state + the policy
//	POST /api/compute/admission/release   — close a hold (verified cleanup)
//
// The POST response is ALWAYS a decision object (allowed true/false +
// the machine-readable reason_code + the numbers behind it) — a refusal
// is the gate working, not an HTTP error. Malformed JSON is 400; a
// missing/unconfigured admission manager is 503 (the surface is opt-in
// like every mutating act). Idempotency keys replay the original
// decision.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/MoZoHuJa/OS/scarlihq/internal/admission"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
)

// ConfigureAdmission attaches the admission manager (opt-in; the routes
// answer 503 until this is called).
func (h *Handler) ConfigureAdmission(m *admission.Manager) {
	h.admission = m
}

// admissionRoute serves GET (state) and POST (decide+reserve).
func (h *Handler) admissionRoute(w http.ResponseWriter, r *http.Request) {
	if h.admission == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "admission gate not configured (Compute Fabric Phase 2 surface — set SCARLIHQ_ADMISSION_ENABLED=true and a writable ledger)")
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.admissionState(w)
	case http.MethodPost:
		h.admissionDecide(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "admission supports GET (state) and POST (decide+reserve)")
	}
}

// admissionState reports the held records + the active policy.
func (h *Handler) admissionState(w http.ResponseWriter) {
	writeJSON(w, map[string]interface{}{
		"policy":  h.admission.Policy(),
		"held":    h.admission.Held(),
		"records": h.admission.Records(),
		"boot_id": admission.BootIdentity(),
		"version": h.version,
	})
}

// admissionDecide parses the request and returns the DECISION (the
// refusal is data: allowed=false + reason_code + explanation).
func (h *Handler) admissionDecide(w http.ResponseWriter, r *http.Request) {
	var req admission.AdmissionRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // a typo'd dimension is a refusal, not a silent default
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "admission request malformed: "+err.Error())
		return
	}
	// The authenticated principal is stamped SERVER-SIDE from the resolved
	// token identity — a caller cannot attribute its hold to someone else.
	if p := profiles.PrincipalFromContext(r.Context()); p != nil {
		req.Principal = p.Name
	}
	decision := h.admission.Admit(req)
	writeJSON(w, decision)
}

// admissionRelease closes a HELD admission (verified cleanup: a double
// release or unknown ID is a named 409, never a silent no-op).
func (h *Handler) admissionRelease(w http.ResponseWriter, r *http.Request) {
	if h.admission == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "admission gate not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "admission release is POST")
		return
	}
	var body struct {
		AdmissionID string `json:"admission_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AdmissionID == "" {
		writeJSONError(w, http.StatusBadRequest, "release requires {\"admission_id\": \"...\"}")
		return
	}
	rec, err := h.admission.Release(body.AdmissionID)
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]interface{}{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}
	writeJSON(w, map[string]interface{}{
		"status": "released",
		"record": rec,
	})
}

// principalFrom extracts the authenticated principal name for the
// request (the auth middleware's resolved identity; empty when the
// single-user deployment carries no profile).
func (h *Handler) principalFrom(r *http.Request) string {
	if v := r.Header.Get("X-Scarlix-Principal"); v != "" {
		return v
	}
	return ""
}
