// Tests for the monitor package (v19.1.4 ScarliMonitor Foundation).
//
// Test philosophy: every test asserts the stability contract — Snapshot()
// never panics, never errors, slices are never nil, JSON is parseable,
// timestamps are RFC 3339. Linux-only assertions (CPU model from
// /proc/cpuinfo, RAM total from /proc/meminfo) skip gracefully on non-Linux
// hosts so the test suite still passes in dev sandboxes.
package monitor

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestSnapshot_NeverPanics — calling Snapshot() on a Monitor with an empty
// version must not crash (no panic, no fatal). The Monitor's contract is
// graceful degradation: every read is best-effort and the resulting Snapshot
// is always valid, even on a host with no GPU, no Docker, no models.yaml,
// and an uninstalled SCARLIX OS. This is the foundational safety property.
func TestSnapshot_NeverPanics(t *testing.T) {
	m := New("") // empty version — like a fresh dev environment

	// If Snapshot() panics, the test goroutine dies and the test fails.
	// recover() here is belt-and-suspenders — a panic in the test goroutine
	// would be reported by the test runner anyway, but the explicit recover
	// turns the implicit "test crashed" into an explicit "test failed".
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Snapshot() panicked: %v", r)
		}
	}()

	snap := m.Snapshot()
	if snap.Timestamp == "" {
		// Sanity — Snapshot() always sets a timestamp. If it doesn't, the
		// Snapshot() impl is broken regardless of panic safety.
		t.Fatalf("Snapshot.Timestamp is empty")
	}
}

// TestSnapshot_ReturnsValidTimestamp — Snapshot.Timestamp is non-empty and
// parseable as RFC 3339. This is the wire-format stability contract: any
// consumer parsing the JSON timestamp with time.RFC3339 must succeed.
func TestSnapshot_ReturnsValidTimestamp(t *testing.T) {
	m := New("19.1.4")
	snap := m.Snapshot()

	if snap.Timestamp == "" {
		t.Fatalf("Snapshot.Timestamp is empty — expected RFC 3339 string")
	}
	if _, err := time.Parse(time.RFC3339, snap.Timestamp); err != nil {
		t.Fatalf("Snapshot.Timestamp %q is not RFC 3339: %v", snap.Timestamp, err)
	}
}

// TestSnapshot_GpusNeverNil — Snapshot.GPUs is never nil (empty slice at
// worst). Stability contract §5: "all arrays must serialize as [], never
// null". JSON consumers must be able to len() / range over GPUs without
// nil-checking.
func TestSnapshot_GpusNeverNil(t *testing.T) {
	m := New("19.1.4")
	snap := m.Snapshot()
	if snap.GPUs == nil {
		t.Fatalf("Snapshot.GPUs is nil — must be [] not null per stability contract §5")
	}
	// Empty slice is OK (host without nvidia-smi) — just not nil.
	if len(snap.GPUs) < 0 {
		t.Fatalf("len(Snapshot.GPUs) is negative — impossible but defensive")
	}
}

// TestSnapshot_RuntimesNeverNil — Snapshot.Runtimes is never nil.
// inventory.CollectRuntimes() always returns 5 entries (sglang/vllm/beellama/
// ollama/litellm) on any host, but Snapshot() defensively re-checks.
func TestSnapshot_RuntimesNeverNil(t *testing.T) {
	m := New("19.1.4")
	snap := m.Snapshot()
	if snap.Runtimes == nil {
		t.Fatalf("Snapshot.Runtimes is nil — must be [] not null per stability contract §5")
	}
}

// TestSnapshot_ModelsNeverNil — Snapshot.Models is never nil. Even on a host
// without models.yaml, the slice is empty (not nil) so JSON consumers can
// safely range over it.
func TestSnapshot_ModelsNeverNil(t *testing.T) {
	m := New("19.1.4")
	snap := m.Snapshot()
	if snap.Models == nil {
		t.Fatalf("Snapshot.Models is nil — must be [] not null per stability contract §5")
	}
}

// TestSnapshotJSON_ValidJSON — SnapshotJSON() returns valid parseable JSON.
// Round-trips through json.Unmarshal into a map[string]interface{} — if any
// field has a non-serializable type or the JSON is malformed, this fails.
func TestSnapshotJSON_ValidJSON(t *testing.T) {
	m := New("19.1.4")
	data, err := m.SnapshotJSON()
	if err != nil {
		t.Fatalf("SnapshotJSON() error: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("SnapshotJSON() returned empty byte slice")
	}

	// Parse into a generic map — verifies the JSON is well-formed + has the
	// expected top-level keys.
	var generic map[string]interface{}
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("SnapshotJSON() output is not valid JSON: %v\noutput was:\n%s", err, string(data))
	}

	// Spot-check required top-level keys from the Snapshot struct definition.
	requiredKeys := []string{
		"timestamp", "version", "mode",
		"gpus", "cpu", "ram", "storage",
		"runtimes", "models", "services", "health",
	}
	for _, k := range requiredKeys {
		if _, ok := generic[k]; !ok {
			t.Fatalf("SnapshotJSON() missing top-level key %q in JSON output:\n%s", k, string(data))
		}
	}

	// Spot-check that arrays are [] not null — the stability contract §5.
	// json.Unmarshal of a JSON [] into interface{} yields []interface{} (len 0);
	// a JSON null yields nil interface{}. This catches accidental nil slices.
	arrays := []string{"gpus", "runtimes", "models", "services", "health"}
	for _, k := range arrays {
		v, ok := generic[k]
		if !ok {
			t.Fatalf("key %q missing (already checked above)", k)
		}
		if v == nil {
			t.Fatalf("array %q is null in JSON — must be [] per stability contract §5", k)
		}
		// Type must be []interface{}, not something else.
		if _, isSlice := v.([]interface{}); !isSlice {
			t.Fatalf("array %q is not a JSON array (got %T): %v", k, v, v)
		}
	}
}

// TestCPUInfo_ReadsCpuinfo — if /proc/cpuinfo exists (Linux), CPUInfo.Cores
// must be > 0 and CPUInfo.ModelName must be non-empty. Skipped on non-Linux
// (or if /proc/cpuinfo is unreadable — should never happen on a real Linux
// host but is theoretically possible in a sandboxed container that masks
// /proc).
func TestCPUInfo_ReadsCpuinfo(t *testing.T) {
	if _, err := os.Stat("/proc/cpuinfo"); err != nil {
		t.Skip("/proc/cpuinfo not available — skipping (non-Linux host)")
	}

	m := New("19.1.4")
	snap := m.Snapshot()

	if snap.CPU.Cores <= 0 {
		t.Fatalf("CPU.Cores = %d, want > 0 (host has at least 1 logical CPU)", snap.CPU.Cores)
	}
	if snap.CPU.ModelName == "" {
		t.Fatalf("CPU.ModelName is empty — /proc/cpuinfo should have a 'model name' line")
	}
	t.Logf("CPU: %d cores, %q, load=%.2f, usage=%.1f%%",
		snap.CPU.Cores, snap.CPU.ModelName, snap.CPU.LoadAvg1m, snap.CPU.UsagePct)
}

// TestRAMInfo_ReadsMeminfo — if /proc/meminfo exists (Linux), RAMInfo.TotalMB
// must be > 0. Skipped on non-Linux. Also verifies UsedMB <= TotalMB as a
// sanity check on the (Total - Available) formula in readRAM().
func TestRAMInfo_ReadsMeminfo(t *testing.T) {
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		t.Skip("/proc/meminfo not available — skipping (non-Linux host)")
	}

	m := New("19.1.4")
	snap := m.Snapshot()

	if snap.RAM.TotalMB <= 0 {
		t.Fatalf("RAM.TotalMB = %d, want > 0 (host has physical memory)", snap.RAM.TotalMB)
	}
	if snap.RAM.UsedMB > snap.RAM.TotalMB {
		t.Fatalf("RAM.UsedMB=%d > RAM.TotalMB=%d — impossible; check (Total-Available) formula",
			snap.RAM.UsedMB, snap.RAM.TotalMB)
	}
	if snap.RAM.AvailableMB > snap.RAM.TotalMB {
		t.Fatalf("RAM.AvailableMB=%d > RAM.TotalMB=%d — impossible; check /proc/meminfo parser",
			snap.RAM.AvailableMB, snap.RAM.TotalMB)
	}
	t.Logf("RAM: total=%d MB, used=%d MB, free=%d MB, available=%d MB",
		snap.RAM.TotalMB, snap.RAM.UsedMB, snap.RAM.FreeMB, snap.RAM.AvailableMB)
}
