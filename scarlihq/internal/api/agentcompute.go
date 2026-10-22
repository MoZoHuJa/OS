package api

// AK-5 (v19.4.6): compute intake meets agent sessions — the closing step of
// the Agent Kernel v19.4.x line (Phase A). POST /api/agent/sessions/compute
// lets an ACTIVE agent session reserve or release a compute allocation,
// with the kernel deciding and the workload bridge enforcing:
//
//   1. Kernel evaluation (deterministic, no LLM): the session must be
//      ACTIVE and hold compute.reserve / compute.release (registry grants
//      when a registry is configured — admin-owned acts; legacy-mode
//      request-supplied grants otherwise). A denial is a 200 response with
//      allow=false (the denial-is-200 contract) — auditable, not an error.
//   2. Principal mapping: the agent acts as its OWNER principal — but only
//      the registry-declared owner is trusted (admin-verified). In legacy
//      mode the owner is caller-supplied, which would make the mapping
//      spoofable — so compute intake REQUIRES the grant registry (400 with
//      a clear message otherwise, not a silent fallback).
//   3. Ownership enforcement: the workload bridge rejects operations on
//      allocations the principal does not own (ErrOwnerMismatch → 403) —
//      the same single-principal discipline as /api/compute/start|stop,
//      now per-agent-owner instead of per-server-config.
//
// Every attempt (allow AND deny, success AND bridge rejection) is audited
// to the AK-4 sink with the allocation id — compute intake is the most
// consequential capability, so its trail is the most detailed.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

// agentComputeRequest: one JSON object, unknown fields rejected.
type agentComputeRequest struct {
	SessionID    string `json:"session_id"`
	Action       string `json:"action"`
	AllocationID string `json:"allocation_id"`
}

// agentComputeDisabled reports (and writes) why the intake surface is not
// available: it needs BOTH the compute lifecycle (manager wired) and the
// agent kernel session store.
func (h *Handler) agentComputeDisabled(w http.ResponseWriter) bool {
	if h.agentStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent kernel sessions are disabled (compute intake needs SCARLIHQ_AGENTKERNEL_ENABLED=true)")
		return true
	}
	if h.computeManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "compute lifecycle is disabled (compute intake needs SCARLIHQ_COMPUTE_ENABLED=true)")
		return true
	}
	return false
}

// agentCompute handles POST /api/agent/sessions/compute.
func (h *Handler) agentCompute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.agentComputeDisabled(w) {
		return
	}
	request, ok := decodeOneJSON(w, r, func() any { return &agentComputeRequest{} })
	if !ok {
		return
	}
	req := request.(*agentComputeRequest)

	sessionID := strings.TrimSpace(req.SessionID)
	action := strings.TrimSpace(req.Action)
	allocationID := strings.TrimSpace(req.AllocationID)

	var capability agentkernel.Capability
	switch action {
	case "reserve":
		capability = agentkernel.CapabilityComputeReserve
	case "release":
		capability = agentkernel.CapabilityComputeRelease
	default:
		writeJSONError(w, http.StatusBadRequest, "action must be reserve or release")
		return
	}
	if allocationID == "" || len(allocationID) > 128 {
		writeJSONError(w, http.StatusBadRequest, "allocation_id is required (1..128 chars)")
		return
	}

	// The registry is REQUIRED for compute intake: the agent's owner
	// principal must come from admin-verified grants, not from a
	// caller-supplied field (legacy mode). A spoofable principal mapping
	// into the compute lifecycle is exactly what AK-5 exists to prevent.
	if h.agentGrants == nil {
		writeJSONError(w, http.StatusBadRequest, "compute intake requires the grant registry (SCARLIHQ_AGENT_GRANTS_FILE + SCARLIHQ_AGENT_PRINCIPALS) — the acting principal must be admin-verified")
		return
	}

	// 1. Kernel decision (registry policy — runtime disable switches apply).
	policy := h.agentGrants.Policy()
	event, decision, err := h.agentStore.Evaluate(sessionID, policy, capability, time.Now().UTC())
	if err != nil {
		if errors.Is(err, agentkernel.ErrUnknownSession) {
			writeJSONError(w, http.StatusNotFound, "unknown session")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "evaluation failed")
		return
	}
	if !decision.Allow {
		// Denial-is-200: an auditable kernel decision, not an HTTP error.
		if h.agentAudit != nil {
			if aerr := h.agentAudit.Append(computeAuditLine(event, decision, action, allocationID, "")); aerr != nil {
				writeJSONError(w, http.StatusInternalServerError, "audit write failed — decision not served (fail-closed)")
				return
			}
		}
		writeJSON(w, map[string]interface{}{
			"session_id":    event.SessionID,
			"agent_id":      string(event.AgentID),
			"action":        action,
			"allocation_id": allocationID,
			"allow":         false,
			"reason":        decision.Reason,
			"timestamp":     event.Timestamp.Format(time.RFC3339),
		})
		return
	}

	// 2. Principal mapping: the agent acts as its registry-declared owner.
	owner, ok := h.agentGrants.AgentOwner(event.AgentID)
	if !ok || strings.TrimSpace(owner) == "" {
		// Unreachable in registry mode (Evaluate passed ⇒ grants exist), but
		// fail-closed beats a nil principal any day.
		writeJSONError(w, http.StatusInternalServerError, "agent owner resolution failed")
		return
	}

	// 3. Workload bridge call (ownership enforced there; 45s like the
	// direct lifecycle surface).
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var result workload.Result
	if action == "reserve" {
		result, err = h.computeManager.Start(ctx, allocationID, owner)
	} else {
		result, err = h.computeManager.Stop(ctx, allocationID, owner)
	}

	outcome := "executed"
	if err != nil {
		outcome = "rejected:" + bridgeErrorKind(err)
	}
	if h.agentAudit != nil {
		if aerr := h.agentAudit.Append(computeAuditLine(event, decision, action, allocationID, outcome)); aerr != nil {
			writeJSONError(w, http.StatusInternalServerError, "intake executed but audit write failed: "+aerr.Error())
			return
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, workload.ErrOwnerMismatch):
			writeJSONError(w, http.StatusForbidden, "allocation is not owned by the agent's principal")
		case errors.Is(err, workload.ErrInvalidState), errors.Is(err, workload.ErrInvalidAllocation):
			writeJSONError(w, http.StatusConflict, "allocation state or assignment does not permit this operation")
		case errors.Is(err, workload.ErrReconciliationRequired):
			writeJSONError(w, http.StatusServiceUnavailable, "lifecycle outcome requires reconciliation; do not retry blindly")
		default:
			writeJSONError(w, http.StatusConflict, "lifecycle operation rejected")
		}
		return
	}
	status := "running"
	if action == "release" {
		// Same honest contract as the direct stop surface: cleanup verified,
		// the reservation bundle stays RELEASING until reconciliation.
		status = "cleanup_verified_reservation_pending"
	}
	writeJSON(w, map[string]interface{}{
		"session_id":    event.SessionID,
		"agent_id":      string(event.AgentID),
		"principal":     owner,
		"action":        action,
		"allow":         true,
		"allocation_id": result.AllocationID,
		"state":         string(result.State),
		"workload_ref":  result.WorkloadRef,
		"owned":         result.WorkloadOwned,
		"status":        status,
		"reason":        decision.Reason,
		"timestamp":     event.Timestamp.Format(time.RFC3339),
	})
}

// computeAuditLine builds the AK-4 audit line for a compute-intake attempt.
func computeAuditLine(event agentkernel.AuditEvent, decision agentkernel.Decision, action, allocationID, outcome string) agentkernel.AuditLine {
	t := agentkernel.AuditTypeComputeReserve
	if action == "release" {
		t = agentkernel.AuditTypeComputeRelease
	}
	line := agentkernel.NewAuditLineFromEvent(event)
	line.Type = t
	line.AllocationID = allocationID
	line.Detail = outcome
	return line
}

// bridgeErrorKind maps a workload error to a short stable tag for the audit
// trail (the full message stays in the HTTP response; the audit line keeps
// a searchable kind).
func bridgeErrorKind(err error) string {
	switch {
	case errors.Is(err, workload.ErrOwnerMismatch):
		return "owner-mismatch"
	case errors.Is(err, workload.ErrInvalidState):
		return "invalid-state"
	case errors.Is(err, workload.ErrInvalidAllocation):
		return "invalid-allocation"
	case errors.Is(err, workload.ErrReconciliationRequired):
		return "reconciliation-required"
	default:
		return "rejected"
	}
}
