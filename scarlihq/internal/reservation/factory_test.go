package reservation

import (
	"testing"
	"time"
)

func TestNewFileBackedManager_RequiresDistinctStorePaths(t *testing.T) {
	inventory := newStubInventory()
	clock := RealClock{}
	cleanup := &stubCleanupVerifier{verified: true, clock: clock}
	identity := newStubIdentityProvider()
	if _, err := NewFileBackedManager("", "alloc.jsonl", DefaultPolicy(), clock, inventory, cleanup, identity); err == nil {
		t.Fatal("expected empty reservation path to be rejected")
	}
	path := t.TempDir() + "/shared.jsonl"
	if _, err := NewFileBackedManager(path, path, DefaultPolicy(), clock, inventory, cleanup, identity); err == nil {
		t.Fatal("same file for reservation and allocation stores must be rejected")
	}
}

func TestNewFileBackedManager_WiresDurableStores(t *testing.T) {
	dir := t.TempDir()
	clock := &FakeClock{T: time.Now().UTC()}
	inventory := newStubInventory()
	inventory.snap.AsOf = clock.Now()
	manager, err := NewFileBackedManager(dir+"/reservations.jsonl", dir+"/allocations.jsonl", DefaultPolicy(), clock, inventory, &stubCleanupVerifier{verified: true, clock: clock}, newStubIdentityProvider())
	if err != nil {
		t.Fatalf("NewFileBackedManager: %v", err)
	}
	if manager == nil || manager.resourceLock == nil {
		t.Fatal("manager/resource lock not initialized")
	}
}
