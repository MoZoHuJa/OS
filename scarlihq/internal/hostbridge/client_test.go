package hostbridge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

func TestClientUsesUnixSocketForStartAndStop(t *testing.T) {
	executor := &fakeExecutor{
		startResult: workload.StartResult{Ref: "container:owned-123", Ready: true, Owned: true},
		stopResult:  workload.StopResult{Verified: true},
	}
	server, err := NewServer(executor)
	if err != nil {
		t.Fatal(err)
	}
	// Umask-independent parent dir: ListenUnix rejects group-writable parents
	// (secureSocketDir tightens t.TempDir() from ambient-umask 0775 to 0700).
	socketPath := filepath.Join(secureSocketDir(t), "bridge.sock")
	listener, err := ListenUnix(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := NewHTTPServer(server.Handler())
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	t.Cleanup(func() {
		_ = httpServer.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("host bridge HTTP server did not stop")
		}
	})

	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)

	started, err := client.Start(context.Background(), workload.StartRequest{
		AllocationID: "alloc-1",
		RuntimeID:    "sglang",
		ModelID:      "qwen3-14b-awq",
		GPUID:        "GPU-abc",
		Port:         30000,
	})
	if err != nil {
		t.Fatalf("client Start: %v", err)
	}
	if !started.Ready || !started.Owned || started.Ref != "container:owned-123" {
		t.Fatalf("unexpected start result: %+v", started)
	}

	stopped, err := client.Stop(context.Background(), workload.StopRequest{
		AllocationID: "alloc-1",
		RuntimeID:    "sglang",
		ModelID:      "qwen3-14b-awq",
		Ref:          started.Ref,
		Owned:        true,
	})
	if err != nil {
		t.Fatalf("client Stop: %v", err)
	}
	if !stopped.Verified || executor.startCalls != 1 || executor.stopCalls != 1 {
		t.Fatalf("unexpected lifecycle: stopped=%+v starts=%d stops=%d", stopped, executor.startCalls, executor.stopCalls)
	}
}

func TestClientRefusesSharedWorkloadStopWithoutSocketCall(t *testing.T) {
	client, err := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	_, err = client.Stop(context.Background(), workload.StopRequest{
		AllocationID: "alloc-1",
		RuntimeID:    "sglang",
		ModelID:      "model",
		Ref:          "container:shared",
		Owned:        false,
	})
	if err == nil {
		t.Fatal("expected shared workload stop to be refused")
	}
}
