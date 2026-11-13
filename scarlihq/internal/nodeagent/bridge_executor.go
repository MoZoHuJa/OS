package nodeagent

import (
	"context"
	"errors"

	"github.com/MoZoHuJa/OS/scarlihq/internal/hostbridge"
	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

// ErrExecutionUnavailable is the DETERMINISTIC executor refusal: the
// node's execution boundary (its local host workload bridge) is not
// configured/reachable — no side effect is possible, so the agent
// refuses the start WITHOUT recording an uncertain outcome (uncertainty
// is reserved for genuinely ambiguous side effects; a deterministic
// refusal must not poison reconciliation).
var ErrExecutionUnavailable = errors.New("nodeagent: this node's execution boundary is unavailable (the local host workload bridge is not configured/reachable)")

// BridgeExecutor adapts the node's OWN hardened host workload bridge
// (the cmd/scarlix-host-workload-bridge pattern: administrator-owned
// runtime allowlist, idempotent-by-allocation ledger, no Docker socket
// in the agent process) to the agent's AgentExecutor boundary.
type BridgeExecutor struct {
	client *hostbridge.Client
}

// NewBridgeExecutor wires the executor to the node-local bridge socket.
func NewBridgeExecutor(socketPath string) (*BridgeExecutor, error) {
	client, err := hostbridge.NewClient(socketPath)
	if err != nil {
		return nil, err
	}
	return &BridgeExecutor{client: client}, nil
}

// Start translates the typed command to the bridge's structured request
// (identifiers only — the bridge enforces its own allowlist; the agent
// never transports commands, environments or paths).
func (b *BridgeExecutor) Start(ctx context.Context, cmd StartCommand) (StartOutcome, error) {
	result, err := b.client.Start(ctx, workload.StartRequest{
		AllocationID: cmd.AllocationID,
		RuntimeID:    cmd.RuntimeID,
		ModelID:      cmd.ModelID,
		GPUID:        cmd.GPUID,
		Port:         cmd.Port,
	})
	if err != nil {
		// The bridge's typed refusals are deterministic (no side effect);
		// transport failures are ambiguous — the caller's error class
		// decides (the service checks ErrExecutionUnavailable first).
		return StartOutcome{}, err
	}
	return StartOutcome{Ref: result.Ref, Ready: result.Ready, Owned: result.Owned}, nil
}

// Stop translates the typed stop to the bridge's structured request.
func (b *BridgeExecutor) Stop(ctx context.Context, allocationID, ref string) (StopOutcome, error) {
	// The agent's records carry the allocation's runtime/model identity;
	// the bridge's ledger is the idempotent authority for what it started.
	result, err := b.client.Stop(ctx, workload.StopRequest{
		AllocationID: allocationID,
	})
	if err != nil {
		return StopOutcome{}, err
	}
	return StopOutcome{Verified: result.Verified}, nil
}
