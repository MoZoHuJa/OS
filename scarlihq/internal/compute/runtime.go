// Package compute wires the allocation lifecycle manager to the host bridge.
// It keeps host execution behind workload.Driver and never grants ScarliHQ direct
// access to Docker, systemd, or host commands.
package compute

import (
	"errors"

	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
	"github.com/MoZoHuJa/OS/scarlihq/internal/hostbridge"
	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

// Runtime is the composition root for the workload lifecycle and host bridge.
type Runtime struct {
	Manager *workload.Manager
	client  *hostbridge.Client
}

// New constructs a lifecycle manager with the Unix-socket host bridge driver.
// The socket must be configured by the host administrator; there is no TCP
// fallback. The host service must independently enforce ownership and idempotency.
func New(store workload.AllocationStore, socketPath string) (*Runtime, error) {
	if store == nil {
		return nil, errors.New("compute: allocation store is required")
	}
	client, err := hostbridge.NewClient(socketPath)
	if err != nil {
		return nil, err
	}
	manager, err := workload.NewManager(store, client)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	return &Runtime{Manager: manager, client: client}, nil
}

// Close releases idle transport resources. It does not stop active workloads.
func (r *Runtime) Close() {
	if r != nil && r.client != nil {
		r.client.CloseIdleConnections()
	}
}
