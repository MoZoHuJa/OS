package compute

import (
	"path/filepath"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
)

func TestNewRequiresStoreAndAbsoluteSocketPath(t *testing.T) {
	if _, err := New(nil, "/run/scarlix/workload.sock"); err == nil {
		t.Fatal("expected nil allocation store to be rejected")
	}
	store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
	if _, err := New(store, "relative.sock"); err == nil {
		t.Fatal("expected relative socket path to be rejected")
	}
}

func TestNewBuildsRuntimeWithUnixSocketClient(t *testing.T) {
	store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
	runtime, err := New(store, filepath.Join(t.TempDir(), "workload.sock"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if runtime.Manager == nil {
		t.Fatal("runtime manager is nil")
	}
	runtime.Close()
}
