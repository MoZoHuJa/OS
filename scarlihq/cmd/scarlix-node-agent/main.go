// scarlix-node-agent — the distributed fabric's remote node agent
// (v19.9.0, Compute Fabric Phase 5, roadmap §9.3; design:
// docs/SCARLIX_COMPUTE_FABRIC_PHASE_5_DISTRIBUTED_DESIGN.md §3).
//
// The binary is deliberately THIN: flags → wire the typed domain core
// (internal/nodeagent) → serve. The narrow typed API is the whole
// surface; this process holds no Docker socket, no shell, no arbitrary
// file access — the execution boundary is the node's OWN hardened host
// workload bridge (cmd/scarlix-host-workload-bridge), which this agent
// calls through its Unix socket with structured identifiers only.
//
// Fail-closed startup: a node without identity, credential, store or a
// reachable local bridge refuses to serve (an agent that cannot prove
// its boundary is not an agent).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/nodeagent"
)

// inventorySnapshots adapts inventory.BuildSnapshotV2 to the agent's
// SnapshotProvider (the production seam — the ONLY place this binary
// touches the node's real collectors).
type inventorySnapshots struct{}

func (inventorySnapshots) Current() (*inventory.InventorySnapshotV2, error) {
	return inventory.BuildSnapshotV2()
}

func main() {
	nodeID := flag.String("node-id", "", "this node's enrolled identity (must match the control-plane enrollment)")
	cpCredential := flag.String("cp-credential", "", "path to the file holding the control-plane credential this agent accepts (0600)")
	listen := flag.String("listen", ":8420", "listen address (serve on the tailnet interface; transport is NOT authorization)")
	dataDir := flag.String("data-dir", "/var/lib/scarlix-node-agent", "durable execution records + reservation mirror directory")
	bridgeSocket := flag.String("bridge-socket", "/run/scarlix-workload-bridge/bridge.sock", "the node's local host workload bridge socket (the ONLY execution boundary)")
	flag.Parse()

	if *nodeID == "" {
		log.Fatalf("scarlix-node-agent: --node-id is required (the enrolled identity; roadmap §9.2: reachability is not identity)")
	}
	secret, err := loadCredential(*cpCredential)
	if err != nil {
		log.Fatalf("scarlix-node-agent: control-plane credential rejected: %v", err)
	}

	store := nodeagent.NewJSONLRecordStore(*dataDir)
	executor, err := nodeagent.NewBridgeExecutor(*bridgeSocket)
	if err != nil {
		log.Fatalf("scarlix-node-agent: the local host workload bridge is unreachable at %s — an agent without its execution boundary refuses to serve: %v", *bridgeSocket, err)
	}
	service, err := nodeagent.NewAgentService(nodeagent.SystemClock{}, executor, store)
	if err != nil {
		log.Fatalf("scarlix-node-agent: agent core rejected: %v", err)
	}
	server, err := nodeagent.NewServer(*nodeID, secret, service, inventorySnapshots{}, nodeagent.SystemClock{})
	if err != nil {
		log.Fatalf("scarlix-node-agent: server rejected: %v", err)
	}

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	go func() {
		log.Printf("scarlix-node-agent: serving the narrow typed API on %s (node %s; %d mirrored reservations, %d execution records)", *listen, *nodeID, len(service.Mirror()), len(service.Records()))
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("scarlix-node-agent: serve failed: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("scarlix-node-agent: shutting down (draining in-flight typed requests)")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("scarlix-node-agent: shutdown: %v", err)
	}
}

// loadCredential reads the control-plane credential from a 0600 file.
func loadCredential(path string) (string, error) {
	if path == "" {
		return "", errConfig("--cp-credential path is required (Tailscale is transport, not authorization; roadmap §9.2)")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", errConfig(fmt.Sprintf("cannot stat %s: %v", path, err))
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errConfig(fmt.Sprintf("%s must be 0600 (got %o) — a credential readable by others is not a credential", path, info.Mode().Perm()))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errConfig(fmt.Sprintf("cannot read %s: %v", path, err))
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", errConfig("the credential file is empty")
	}
	return secret, nil
}

type configError struct{ msg string }

func (e *configError) Error() string { return e.msg }

func errConfig(msg string) error { return &configError{msg: msg} }
