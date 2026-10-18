// Package workload coordinates allocation execution without granting ScarliHQ
// direct access to Docker, systemd, or privileged host commands.
package workload

import (
	"context"
	"errors"
)

// ErrInvalidState means the allocation is not in a state that permits the requested operation.
var ErrInvalidState = errors.New("workload: allocation state does not permit operation")

// ErrOwnerMismatch means the authenticated principal does not own the allocation.
var ErrOwnerMismatch = errors.New("workload: allocation owner mismatch")

// ErrInvalidAllocation means the reserved allocation is missing required execution assignments.
var ErrInvalidAllocation = errors.New("workload: allocation has incomplete execution assignments")

// ErrReconciliationRequired means an external side effect may have happened but durable state
// could not safely record or verify its outcome. Callers must not retry blindly.
var ErrReconciliationRequired = errors.New("workload: reconciliation required")

// StartRequest is a structured, allowlisted request. It deliberately contains no shell command,
// arbitrary environment, image name, or filesystem path.
type StartRequest struct {
	AllocationID string
	RuntimeID    string
	ModelID      string
	GPUID        string
	Port         int
}

// StartResult describes positively observed workload state.
type StartResult struct {
	// Ref is an opaque, bounded reference from the trusted driver (for example, a container ID).
	Ref string
	// Ready must be true only after the driver positively observes a usable workload.
	Ready bool
	// Owned is true only if this allocation caused the workload to be started.
	// Pre-existing/shared runtimes must report false and must never be stopped by this allocation.
	Owned bool
}

// StopRequest identifies only the workload previously returned for this allocation.
type StopRequest struct {
	AllocationID string
	RuntimeID    string
	ModelID      string
	Ref          string
	Owned        bool
}

// StopResult reports whether cleanup was positively verified.
type StopResult struct {
	Verified bool
}

// Driver is the only execution boundary used by the lifecycle manager.
// Implementations must be idempotent by AllocationID, enforce runtime allowlists, and fail closed.
// They must not accept or execute caller-provided commands.
type Driver interface {
	Start(ctx context.Context, request StartRequest) (StartResult, error)
	Stop(ctx context.Context, request StopRequest) (StopResult, error)
}
