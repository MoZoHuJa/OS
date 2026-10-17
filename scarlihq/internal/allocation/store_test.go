package allocation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStore_AppendAndLoad verifies the basic append + load cycle.
func TestStore_AppendAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	a := NewAllocation("alloc-1", "v1", "coding", "interactive", "agent.coder")
	if err := store.Append(a); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	records, corrupt, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(corrupt) != 0 {
		t.Errorf("Load returned %d corrupt records, want 0", len(corrupt))
	}
	if len(records) != 1 {
		t.Fatalf("Load returned %d records, want 1", len(records))
	}
	loaded := records["alloc-1"]
	if loaded == nil {
		t.Fatal("Loaded record is nil")
	}
	if loaded.AllocationID != "alloc-1" {
		t.Errorf("AllocationID = %q, want alloc-1", loaded.AllocationID)
	}
	if loaded.State != StateRequested {
		t.Errorf("State = %q, want REQUESTED", loaded.State)
	}
}

// TestStore_LoadEmpty verifies loading a nonexistent file returns empty map.
func TestStore_LoadEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.jsonl")
	store := NewStore(path)

	records, corrupt, err := store.Load()
	if err != nil {
		t.Fatalf("Load on nonexistent file failed: %v", err)
	}
	if records == nil {
		t.Error("Load returned nil map, want empty")
	}
	if len(records) != 0 {
		t.Errorf("Load returned %d records, want 0", len(records))
	}
	if len(corrupt) != 0 {
		t.Errorf("Load returned %d corrupt records, want 0", len(corrupt))
	}
}

// TestStore_Get verifies Get returns the correct record and ErrNotFound for missing IDs.
func TestStore_Get(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	a := NewAllocation("alloc-get", "v1", "coding", "interactive", "agent.coder")
	_ = store.Append(a)

	loaded, err := store.Get("alloc-get")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if loaded.AllocationID != "alloc-get" {
		t.Errorf("AllocationID = %q, want alloc-get", loaded.AllocationID)
	}

	_, err = store.Get("nonexistent")
	if err == nil {
		t.Error("Get on nonexistent ID returned nil error, want ErrNotFound")
	}
	if err != ErrNotFound {
		t.Errorf("Get on nonexistent ID returned %v, want ErrNotFound", err)
	}
}

// TestStore_Update_TransitionPersists verifies that a transition via Update
// is persisted to disk.
func TestStore_Update_TransitionPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	a := NewAllocation("alloc-update", "v1", "coding", "interactive", "agent.coder")
	_ = store.Append(a)

	// Perform a transition via Update.
	err := store.Update("alloc-update", func(a *Allocation) error {
		_, err := a.Transition(StateValidating, "validated contract")
		return err
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Reload and verify the transition persisted.
	loaded, err := store.Get("alloc-update")
	if err != nil {
		t.Fatalf("Get after Update failed: %v", err)
	}
	if loaded.State != StateValidating {
		t.Errorf("State after reload = %q, want VALIDATING", loaded.State)
	}
	if len(loaded.History) != 1 {
		t.Errorf("History length after reload = %d, want 1", len(loaded.History))
	}
	// Revision should have incremented.
	if loaded.Revision != 1 {
		t.Errorf("Revision after Update = %d, want 1", loaded.Revision)
	}
}

// TestStore_Update_InvalidTransitionDoesNotPersist verifies that a failed
// transition does NOT overwrite the persisted state.
func TestStore_Update_InvalidTransitionDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	a := NewAllocation("alloc-fail", "v1", "coding", "interactive", "agent.coder")
	_ = store.Append(a)

	// Attempt an illegal transition (REQUESTED → RUNNING is a shortcut).
	err := store.Update("alloc-fail", func(a *Allocation) error {
		_, err := a.Transition(StateRunning, "illegal shortcut")
		return err
	})
	if err == nil {
		t.Fatal("Update with illegal transition should return error")
	}

	// Reload and verify the state is unchanged.
	loaded, err := store.Get("alloc-fail")
	if err != nil {
		t.Fatalf("Get after failed Update failed: %v", err)
	}
	if loaded.State != StateRequested {
		t.Errorf("State after failed transition = %q, want REQUESTED (unchanged)", loaded.State)
	}
	if len(loaded.History) != 0 {
		t.Errorf("History length after failed transition = %d, want 0", len(loaded.History))
	}
	// Revision should NOT have incremented (no write happened).
	if loaded.Revision != 0 {
		t.Errorf("Revision after failed transition = %d, want 0", loaded.Revision)
	}
}

// TestStore_Update_NotFound verifies Update returns ErrNotFound for missing IDs.
func TestStore_Update_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	err := store.Update("nonexistent", func(a *Allocation) error {
		return nil
	})
	if err != ErrNotFound {
		t.Errorf("Update on nonexistent ID returned %v, want ErrNotFound", err)
	}
}

// TestStore_CreatesParentDir verifies Append creates parent directories.
func TestStore_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "allocations.jsonl")
	store := NewStore(path)

	a := NewAllocation("alloc-nested", "v1", "coding", "interactive", "agent.coder")
	if err := store.Append(a); err != nil {
		t.Fatalf("Append with nested path failed: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("Store file was not created at %s: %v", path, err)
	}
}

// TestStore_CorruptLineSkipped verifies that corrupt JSON lines are skipped
// during Load (not fatal).
func TestStore_CorruptLineSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	// Write a valid record, then a corrupt line, then another valid record.
	a1 := NewAllocation("alloc-good-1", "v1", "coding", "interactive", "agent.coder")
	_ = store.Append(a1)

	// Append corrupt data directly.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	f.WriteString("{not valid json}\n")
	f.Close()

	a2 := NewAllocation("alloc-good-2", "v1", "chat", "normal", "agent.chat")
	_ = store.Append(a2)

	records, corrupt, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(corrupt) != 1 {
		t.Errorf("Load returned %d corrupt records, want 1", len(corrupt))
	}
	if len(records) != 2 {
		t.Errorf("Load returned %d records, want 2 (corrupt skipped)", len(records))
	}
	if records["alloc-good-1"] == nil {
		t.Error("alloc-good-1 not found")
	}
	if records["alloc-good-2"] == nil {
		t.Error("alloc-good-2 not found")
	}
}

// TestStore_Count verifies the count of records.
func TestStore_Count(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	for i := 0; i < 5; i++ {
		a := NewAllocation("alloc-"+string(rune('A'+i)), "v1", "coding", "interactive", "agent")
		_ = store.Append(a)
	}

	count, err := store.Count()
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}
	if count != 5 {
		t.Errorf("Count = %d, want 5", count)
	}
}

// TestStore_ListByState verifies filtering by state.
func TestStore_ListByState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	// Create 3 allocations in different states.
	a1 := NewAllocation("alloc-1", "v1", "coding", "interactive", "agent")
	_ = store.Append(a1)

	a2 := NewAllocation("alloc-2", "v1", "chat", "normal", "agent")
	_ = store.Append(a2)
	// Transition a2 to VALIDATING via Update.
	_ = store.Update("alloc-2", func(a *Allocation) error {
		_, err := a.Transition(StateValidating, "validated")
		return err
	})

	a3 := NewAllocation("alloc-3", "v1", "research", "background", "agent")
	_ = store.Append(a3)
	// Transition a3 to RELEASED.
	// KPK-01 (v19.2.3): CleanupVerified must be set before RELEASED.
	_ = store.Update("alloc-3", func(a *Allocation) error {
		_, err := a.Transition(StateValidating, "")
		if err != nil {
			return err
		}
		_, err = a.Transition(StateFailed, "")
		if err != nil {
			return err
		}
		_, err = a.Transition(StateReleasing, "")
		if err != nil {
			return err
		}
		a.CleanupVerified = true // KPK-01: required before RELEASED
		_, err = a.Transition(StateReleased, "")
		return err
	})

	// List REQUESTED.
	requested, err := store.ListByState(StateRequested)
	if err != nil {
		t.Fatalf("ListByState(REQUESTED) failed: %v", err)
	}
	if len(requested) != 1 {
		t.Errorf("ListByState(REQUESTED) returned %d, want 1", len(requested))
	}

	// List VALIDATING.
	validating, _ := store.ListByState(StateValidating)
	if len(validating) != 1 {
		t.Errorf("ListByState(VALIDATING) returned %d, want 1", len(validating))
	}

	// List RELEASED.
	released, _ := store.ListByState(StateReleased)
	if len(released) != 1 {
		t.Errorf("ListByState(RELEASED) returned %d, want 1", len(released))
	}
}

// TestStore_PruneReleased verifies that released records older than the
// cutoff are removed, and others are kept.
func TestStore_PruneReleased(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	// Create an old released allocation.
	old := NewAllocation("alloc-old", "v1", "coding", "interactive", "agent")
	_ = store.Append(old)
	// Transition to RELEASED.
	// KPK-01 (v19.2.3): CleanupVerified must be set before RELEASED.
	_ = store.Update("alloc-old", func(a *Allocation) error {
		_, err := a.Transition(StateValidating, "")
		if err != nil {
			return err
		}
		_, err = a.Transition(StateFailed, "")
		if err != nil {
			return err
		}
		_, err = a.Transition(StateReleasing, "")
		if err != nil {
			return err
		}
		a.CleanupVerified = true // KPK-01: required before RELEASED
		_, err = a.Transition(StateReleased, "")
		return err
	})
	// Manually set UpdatedAt to the past by rewriting the file.
	records, _, _ := store.Load()
	oldRec := records["alloc-old"]
	oldRec.UpdatedAt = time.Now().UTC().Add(-2 * time.Hour)
	// Rewrite manually.
	_ = store.rewriteAll(records)

	// Create a recent REQUESTED allocation (should NOT be pruned).
	recent := NewAllocation("alloc-recent", "v1", "coding", "interactive", "agent")
	_ = store.Append(recent)

	// Prune records older than 1 hour.
	removed, err := store.PruneReleased(1 * time.Hour)
	if err != nil {
		t.Fatalf("PruneReleased failed: %v", err)
	}
	if removed != 1 {
		t.Errorf("PruneReleased removed %d, want 1", removed)
	}

	// Verify the recent record is still there.
	_, err = store.Get("alloc-recent")
	if err != nil {
		t.Errorf("Recent record was pruned: %v", err)
	}

	// Verify the old record is gone.
	_, err = store.Get("alloc-old")
	if err != ErrNotFound {
		t.Errorf("Old released record should be pruned, got err=%v", err)
	}
}

// TestStore_Append_NilRecord verifies Append rejects nil records.
func TestStore_Append_NilRecord(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "allocations.jsonl"))
	err := store.Append(nil)
	if err == nil {
		t.Error("Append(nil) returned nil error, want error")
	}
}

// TestStore_Append_EmptyID verifies Append rejects records with empty AllocationID.
func TestStore_Append_EmptyID(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "allocations.jsonl"))
	a := NewAllocation("", "v1", "coding", "interactive", "agent")
	err := store.Append(a)
	if err == nil {
		t.Error("Append with empty ID returned nil error, want error")
	}
}

// TestStore_JSONLFormat verifies the file is valid JSON Lines (one JSON per line).
func TestStore_JSONLFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	_ = store.Append(NewAllocation("alloc-1", "v1", "coding", "interactive", "agent"))
	_ = store.Append(NewAllocation("alloc-2", "v1", "chat", "normal", "agent"))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("Expected 2 lines, got %d", len(lines))
	}

	// Each line should be valid JSON.
	for i, line := range lines {
		var a Allocation
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			t.Errorf("Line %d is not valid JSON: %v (line: %s)", i+1, err, line)
		}
	}
}

// TestStore_Path verifies the Path() method returns the configured path.
func TestStore_Path(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom-allocations.jsonl")
	store := NewStore(path)
	if store.Path() != path {
		t.Errorf("Path() = %q, want %q", store.Path(), path)
	}
}

// TestStore_Update_RevisionIncrements verifies that each Update increments the revision.
func TestStore_Update_RevisionIncrements(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "allocations.jsonl"))

	a := NewAllocation("alloc-rev", "v1", "coding", "interactive", "agent")
	_ = store.Append(a)

	// First update: revision 0 → 1.
	_ = store.Update("alloc-rev", func(a *Allocation) error {
		_, err := a.Transition(StateValidating, "")
		return err
	})
	loaded, _ := store.Get("alloc-rev")
	if loaded.Revision != 1 {
		t.Errorf("After 1st update: Revision = %d, want 1", loaded.Revision)
	}

	// Second update: revision 1 → 2.
	_ = store.Update("alloc-rev", func(a *Allocation) error {
		_, err := a.Transition(StatePlanned, "")
		return err
	})
	loaded, _ = store.Get("alloc-rev")
	if loaded.Revision != 2 {
		t.Errorf("After 2nd update: Revision = %d, want 2", loaded.Revision)
	}
}
