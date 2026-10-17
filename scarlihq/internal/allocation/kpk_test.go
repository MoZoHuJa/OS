package allocation

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStore_Append_RejectsDuplicateID verifies that Append rejects a duplicate
// AllocationID.
// KPK-05 (v19.2.3): one ID must represent exactly one allocation record.
func TestStore_Append_RejectsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "allocations.jsonl"))

	a1 := NewAllocation("alloc-dup", "v1", "coding", "interactive", "agent")
	if err := store.Append(a1); err != nil {
		t.Fatalf("First Append failed: %v", err)
	}

	// Second Append with same ID must fail.
	a2 := NewAllocation("alloc-dup", "v1", "chat", "normal", "agent")
	err := store.Append(a2)
	if err == nil {
		t.Fatal("Append with duplicate ID should return error")
	}
	if err != ErrDuplicateID {
		t.Errorf("Append with duplicate ID returned %v, want ErrDuplicateID", err)
	}

	// Verify only one record exists.
	count, _ := store.Count()
	if count != 1 {
		t.Errorf("Count = %d after duplicate append, want 1", count)
	}
}

// TestStore_Update_RejectsCorruptData verifies that Update rejects the
// operation when corrupt JSONL lines are detected.
// KPK-03 (v19.2.3): corrupt data must NOT be silently discarded.
func TestStore_Update_RejectsCorruptData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	// Write a valid record.
	a := NewAllocation("alloc-good", "v1", "coding", "interactive", "agent")
	_ = store.Append(a)

	// Append corrupt data directly.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	f.WriteString("{not valid json}\n")
	f.Close()

	// Update must fail with ErrCorruptData — not silently rewrite.
	err := store.Update("alloc-good", func(a *Allocation) error {
		_, err := a.Transition(StateValidating, "")
		return err
	})
	if err == nil {
		t.Fatal("Update with corrupt data should return error")
	}

	// Verify the file still contains the corrupt line (not silently discarded).
	data, _ := os.ReadFile(path)
	fileStr := string(data)
	if !containsCI(fileStr, "not valid json") {
		t.Error("Corrupt line was silently removed from store file")
	}
}

// TestStore_PruneReleased_RejectsCorruptData verifies that PruneReleased
// rejects the operation when corrupt JSONL lines are detected.
// KPK-03 (v19.2.3): corrupt data must NOT be silently discarded.
func TestStore_PruneReleased_RejectsCorruptData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	// Write a valid record.
	a := NewAllocation("alloc-good", "v1", "coding", "interactive", "agent")
	_ = store.Append(a)

	// Append corrupt data directly.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	f.WriteString("{corrupt}\n")
	f.Close()

	// Prune must fail with ErrCorruptData.
	_, err := store.PruneReleased(1)
	if err == nil {
		t.Fatal("PruneReleased with corrupt data should return error")
	}

	// Verify the file still contains the corrupt line.
	data, _ := os.ReadFile(path)
	fileStr := string(data)
	if !containsCI(fileStr, "corrupt") {
		t.Error("Corrupt line was silently removed during prune")
	}
}

// TestStore_FilePermissions verifies that the store file and directory have
// secure permissions (0600 file, 0700 dir).
// KPK-02 (v19.2.3): no file is world-readable or group-readable.
func TestStore_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "allocations.jsonl")
	store := NewStore(path)

	a := NewAllocation("alloc-perm", "v1", "coding", "interactive", "agent")
	if err := store.Append(a); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Check file permissions.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0600 {
		t.Errorf("File mode = %o, want 0600", mode)
	}

	// Check directory permissions.
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat dir failed: %v", err)
	}
	dirMode := dirInfo.Mode().Perm()
	// Directory should be 0700 (or tighter — some systems may add bits).
	if dirMode&0077 != 0 {
		t.Errorf("Directory mode = %o, want 0700 (no group/other access)", dirMode)
	}
}

// TestStore_RewriteAll_FilePermissions verifies that after a rewrite (Update),
// the file permissions remain 0600.
// KPK-02 (v19.2.3): rewrite must not loosen permissions.
func TestStore_RewriteAll_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	a := NewAllocation("alloc-rw", "v1", "coding", "interactive", "agent")
	_ = store.Append(a)

	// Perform an update (triggers rewriteAll).
	_ = store.Update("alloc-rw", func(a *Allocation) error {
		_, err := a.Transition(StateValidating, "")
		return err
	})

	// Check file permissions after rewrite.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0600 {
		t.Errorf("File mode after rewrite = %o, want 0600", mode)
	}
}

// TestHasSecrets_AllFields verifies that HasSecrets checks all user-influenced
// fields, not just LastError and History[].Reason.
// KPK-06 (v19.2.3): all fields checked consistently with full marker set.
func TestHasSecrets_AllFields(t *testing.T) {
	// Test Owner field.
	a1 := NewAllocation("alloc-1", "v1", "coding", "interactive", "agent")
	a1.Owner = "user?token=sk-secret123"
	if !a1.HasSecrets() {
		t.Error("HasSecrets() = false for Owner containing token=, want true")
	}

	// Test WorkloadRef field.
	a2 := NewAllocation("alloc-2", "v1", "coding", "interactive", "agent")
	a2.WorkloadRef = "container?authorization: bearer xyz"
	if !a2.HasSecrets() {
		t.Error("HasSecrets() = false for WorkloadRef containing authorization:, want true")
	}

	// Test all markers in LastError.
	markers := []string{"token=", "password=", "secret=", "api_key=", "bearer ", "authorization:"}
	for i, marker := range markers {
		a := NewAllocation("alloc-marker-"+string(rune('A'+i)), "v1", "coding", "interactive", "agent")
		a.LastError = "error: " + marker + "value123"
		if !a.HasSecrets() {
			t.Errorf("HasSecrets() = false for LastError containing %q, want true", marker)
		}
	}

	// Test all markers in History[].Reason.
	for i, marker := range markers {
		a := NewAllocation("alloc-reason-"+string(rune('A'+i)), "v1", "coding", "interactive", "agent")
		_, _ = a.Transition(StateValidating, "failed: "+marker+"value")
		if !a.HasSecrets() {
			t.Errorf("HasSecrets() = false for Reason containing %q, want true", marker)
		}
	}

	// Test case insensitivity.
	a3 := NewAllocation("alloc-ci", "v1", "coding", "interactive", "agent")
	a3.LastError = "error: TOKEN=secret"
	if !a3.HasSecrets() {
		t.Error("HasSecrets() = false for TOKEN= (uppercase), want true")
	}
}

// TestHasSecrets_NilReceiver verifies nil receiver is safe.
func TestHasSecrets_NilReceiver(t *testing.T) {
	var a *Allocation
	if a.HasSecrets() {
		t.Error("HasSecrets() on nil receiver should return false")
	}
}

// TestStore_LockFileCreated verifies that the flock lock file is created
// and has secure permissions.
// KPK-04 (v19.2.3): flock for multi-process safety.
func TestStore_LockFileCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allocations.jsonl")
	store := NewStore(path)

	a := NewAllocation("alloc-lock", "v1", "coding", "interactive", "agent")
	_ = store.Append(a)

	// Lock file should exist.
	lockPath := path + ".lock"
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("Lock file not created at %s: %v", lockPath, err)
	}

	// Lock file should have 0600 permissions.
	info, _ := os.Stat(lockPath)
	if info.Mode().Perm() != 0600 {
		t.Errorf("Lock file mode = %o, want 0600", info.Mode().Perm())
	}
}
