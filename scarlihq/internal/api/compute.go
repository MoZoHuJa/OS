package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

// ConfigureCompute enables authenticated lifecycle endpoints. The principal is
// server configuration, never a caller-supplied field. Since the current dashboard
// uses one bearer token, this mode is suitable only for a single-principal deployment.
func (h *Handler) ConfigureCompute(manager *workload.Manager, principal string) error {
	if manager == nil || strings.TrimSpace(principal) == "" {
		return errors.New("api: compute manager and trusted principal are required")
	}
	h.computeManager = manager
	h.computePrincipal = strings.TrimSpace(principal)
	return nil
}

type computeAllocationRequest struct {
	AllocationID string `json:"allocation_id"`
}

type computeAllocationResponse struct {
	Status       string `json:"status"`
	AllocationID string `json:"allocation_id,omitempty"`
	State        string `json:"state,omitempty"`
	WorkloadRef  string `json:"workload_ref,omitempty"`
	Owned        bool   `json:"owned,omitempty"`
}

func (h *Handler) computeStart(w http.ResponseWriter, r *http.Request) {
	h.computeLifecycle(w, r, true)
}

func (h *Handler) computeStop(w http.ResponseWriter, r *http.Request) {
	h.computeLifecycle(w, r, false)
}

func (h *Handler) computeLifecycle(w http.ResponseWriter, r *http.Request, start bool) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if h.computeManager == nil || h.computePrincipal == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "compute lifecycle is disabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request computeAllocationRequest
	if err := decoder.Decode(&request); err != nil || strings.TrimSpace(request.AllocationID) == "" || len(request.AllocationID) > 128 {
		writeJSONError(w, http.StatusBadRequest, "invalid allocation request")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "request must contain exactly one JSON object")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	var result workload.Result
	var err error
	if start {
		result, err = h.computeManager.Start(ctx, request.AllocationID, h.computePrincipal)
	} else {
		result, err = h.computeManager.Stop(ctx, request.AllocationID, h.computePrincipal)
	}
	if err != nil {
		switch {
		case errors.Is(err, workload.ErrOwnerMismatch):
			writeJSONError(w, http.StatusForbidden, "allocation is not owned by the configured principal")
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
	if !start {
		// Cleanup is verified here, but the reservation bundle is not released.
		// Keep the allocation RELEASING until trusted reservation reconciliation completes.
		status = "cleanup_verified_reservation_pending"
	}
	writeJSON(w, computeAllocationResponse{
		Status: status, AllocationID: result.AllocationID,
		State: string(result.State), WorkloadRef: result.WorkloadRef, Owned: result.WorkloadOwned,
	})
}
