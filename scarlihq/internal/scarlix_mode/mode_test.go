package scarlix_mode

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// v18.8.2 P2: Unit tests for Mode.Set() O_EXCL atomicity + Mode.Request()
// (was: no Go tests — CI couldn't catch regressions in the core atomicity guarantee)

func TestSetInvalidMode(t *testing.T) {
	dir := t.TempDir()
	m := NewForTest(filepath.Join(dir, "desired-mode"))
	err := m.Set("invalid-mode")
	if err == nil {
		t.Fatal("expected error for invalid mode, got nil")
	}
}

func TestSetValidModeCreatesFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "desired-mode")
	m := NewForTest(f)
	if err := m.Set("ai"); err != nil {
		t.Fatalf("Set(ai) failed: %v", err)
	}
	data, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "ai\n" {
		t.Errorf("expected 'ai\\n', got %q", string(data))
	}
}

func TestSetAlreadyPendingReturnsError(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "desired-mode")
	m := NewForTest(f)
	// First Set succeeds (creates file)
	if err := m.Set("ai"); err != nil {
		t.Fatalf("first Set failed: %v", err)
	}
	// Second Set must fail — O_EXCL prevents overwrite
	err := m.Set("game")
	if err == nil {
		t.Fatal("expected 'already pending' error on second Set, got nil")
	}
	// Verify the original content wasn't overwritten
	data, _ := os.ReadFile(f)
	if string(data) != "ai\n" {
		t.Errorf("file was overwritten! expected 'ai\\n', got %q", string(data))
	}
}

// TestSetConcurrentAtomicity verifies O_EXCL prevents lost updates under concurrency.
// Multiple goroutines call Set() simultaneously — exactly ONE must succeed.
func TestSetConcurrentAtomicity(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "desired-mode")
	m := NewForTest(f)

	const N = 50
	var wg sync.WaitGroup
	var successes, failures int64
	var mu sync.Mutex

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			err := m.Set("ai")
			mu.Lock()
			if err == nil {
				successes++
			} else {
				failures++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Errorf("expected exactly 1 success, got %d (O_EXCL atomicity broken — lost update race)", successes)
	}
	if failures != N-1 {
		t.Errorf("expected %d failures, got %d", N-1, failures)
	}
}

func TestRequestInvalidMode(t *testing.T) {
	dir := t.TempDir()
	m := NewForTest(filepath.Join(dir, "desired-mode"))
	err := m.Request("invalid")
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestRequestValidMode(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "desired-mode")
	m := NewForTest(f)
	if err := m.Request("ai"); err != nil {
		t.Fatalf("Request(ai) failed: %v", err)
	}
	if _, err := os.Stat(f); os.IsNotExist(err) {
		t.Error("expected desired-mode file to exist after Request")
	}
}
