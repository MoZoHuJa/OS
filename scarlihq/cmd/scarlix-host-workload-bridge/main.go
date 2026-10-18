package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/hostbridge"
)

func main() {
	configPath := flag.String("config", "/etc/scarlix/workload-bridge.json", "administrator-owned runtime allowlist configuration")
	socketPath := flag.String("socket", "/run/scarlix-workload-bridge/bridge.sock", "Unix socket path")
	ledgerPath := flag.String("ledger", "/var/lib/scarlix-workload-bridge/ledger.json", "durable workload ownership ledger")
	flag.Parse()

	cfg, err := hostbridge.LoadDockerExecutorConfig(*configPath)
	if err != nil {
		log.Fatalf("host workload bridge configuration rejected: %v", err)
	}
	dockerExecutor, err := hostbridge.NewDockerExecutor(cfg)
	if err != nil {
		log.Fatalf("host workload bridge runtime unavailable: %v", err)
	}
	ledger, err := hostbridge.NewLedger(*ledgerPath)
	if err != nil {
		log.Fatalf("host workload bridge ledger rejected: %v", err)
	}
	executor, err := hostbridge.NewIdempotentExecutor(dockerExecutor, ledger)
	if err != nil {
		log.Fatalf("host workload bridge executor unavailable: %v", err)
	}
	// Reconcile durable records against Docker before accepting new requests.
	// Ambiguous or unavailable runtime state stays fail-closed and is logged.
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	entries, err := ledger.List()
	if err != nil {
		reconcileCancel()
		log.Fatalf("host workload bridge ledger cannot be read: %v", err)
	}
	for _, entry := range entries {
		if entry.State == "STOPPED" {
			continue
		}
		if err := executor.ReconcileOne(reconcileCtx, entry.AllocationID); err != nil {
			log.Printf("allocation %q remains fail-closed pending reconciliation: %v", entry.AllocationID, err)
		}
	}
	reconcileCancel()
	api, err := hostbridge.NewServer(executor)
	if err != nil {
		log.Fatalf("host workload bridge API unavailable: %v", err)
	}
	listener, err := hostbridge.ListenUnix(*socketPath)
	if err != nil {
		log.Fatalf("host workload bridge socket unavailable: %v", err)
	}
	defer func() {
		_ = listener.Close()
		// The socket directory is service-owned and mode 0700. Remove only a
		// socket path after the listener has closed; never remove regular files.
		if info, statErr := os.Lstat(*socketPath); statErr == nil && info.Mode()&os.ModeSocket != 0 {
			_ = os.Remove(*socketPath)
		}
	}()

	httpServer := hostbridge.NewHTTPServer(api.Handler())
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.Serve(listener) }()

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalCtx.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			_ = httpServer.Close()
			log.Printf("host workload bridge forced shutdown: %v", err)
		}
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Fatalf("host workload bridge failed: %v", err)
		}
	}
}
