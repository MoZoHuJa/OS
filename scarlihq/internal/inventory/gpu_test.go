// Tests for the v19.0.8 GPU telemetry collector (gpu.go).
//
// Strategy:
//   - nvidia-smi is almost certainly NOT installed in CI / dev sandboxes
//     (the test environment for this code). So most tests exercise the
//     graceful-degradation path: missing binary → empty []GPU{}, never nil.
//   - Tests that REQUIRE real nvidia-smi output use `t.Skip` if the binary
//     is missing — they document the intent and can run on a real GPU host.
//   - Health derivation tests use synthesized GPU slices (no nvidia-smi
//     dependency) — they are pure unit tests of CollectGPUHealth's logic.
//
// All tests assert the v19.0.7 stability contract: empty slices serialize
// as `[]`, never `null`, and the JSON field tags match the data contract.
package inventory

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// nvidiaSmiAvailable reports whether nvidia-smi is on PATH in the test
// environment. Used to gate tests that need real GPU data. Kept in sync
// with nvidiaSmiQuery's LookPath-based detection in gpu.go.
func nvidiaSmiAvailable() bool {
	_, err := exec.LookPath("nvidia-smi")
	return err == nil
}

// --- CollectGPUs ----------------------------------------------------------

func TestCollectGPUs_ReturnsEmptySliceWhenNoNvidiaSmi(t *testing.T) {
	// Most CI/dev sandboxes have no nvidia-smi. If we DO have it, skip —
	// the empty-result path is only testable when nvidia-smi is absent.
	if nvidiaSmiAvailable() {
		t.Skip("nvidia-smi is installed — skipping the no-binary path test")
	}

	gpus := CollectGPUs()
	if gpus == nil {
		t.Fatalf("CollectGPUs() returned nil — must return empty slice per stability contract §5")
	}
	if len(gpus) != 0 {
		t.Fatalf("CollectGPUs() with no nvidia-smi should return 0 GPUs; got %d (%+v)", len(gpus), gpus)
	}

	// Stability contract §5: marshal as `[]`, not `null`.
	data, err := json.Marshal(gpus)
	if err != nil {
		t.Fatalf("marshal empty GPU slice failed: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("empty GPU slice should marshal as `[]`; got %s", string(data))
	}
}

func TestCollectGPUs_NeverReturnsNil(t *testing.T) {
	// Even in error conditions (no nvidia-smi, nvidia-smi crash, empty output),
	// the result MUST be a non-nil slice. This is the stability contract.
	// We test by calling CollectGPUs in the default environment (whatever
	// nvidia-smi state happens to be) and asserting non-nil.
	gpus := CollectGPUs()
	if gpus == nil {
		t.Fatalf("CollectGPUs() returned nil — must always return initialized slice")
	}
	// And marshalling must never produce "null".
	data, err := json.Marshal(gpus)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if string(data) == "null" {
		t.Fatalf("CollectGPUs() result marshaled as null — must be [] or [{...}]")
	}
}

// --- CollectGPUHealth ------------------------------------------------------

func TestCollectGPUHealth_MarksHealthyGPU(t *testing.T) {
	// A GPU with sane VRAM + util should be marked healthy.
	gpus := []GPU{
		{
			ID:             "gpu.nvidia.0",
			Index:          0,
			Vendor:         "nvidia",
			Name:           "RTX 5060 Ti",
			VRAMTotalMB:    16384,
			VRAMUsedMB:     13600,
			VRAMFreeMB:     2784,
			UtilizationPct: 10,
			TemperatureC:   68,
			PowerW:         165.5,
			Driver:         "570.86.15",
			ComputeCap:     "12.0",
			Healthy:        false, // pre-health: should be overwritten to true
		},
	}

	healths := CollectGPUHealth(gpus)
	if len(healths) != 1 {
		t.Fatalf("expected 1 health entry; got %d", len(healths))
	}
	if healths[0].State != "healthy" {
		t.Errorf("expected state=healthy; got %q (msg=%q)", healths[0].State, healths[0].Message)
	}
	if healths[0].Component != "gpu.0" {
		t.Errorf("expected component=gpu.0; got %q", healths[0].Component)
	}
	// Mutation contract: the input GPU's Healthy field should have been
	// flipped to true.
	if !gpus[0].Healthy {
		t.Errorf("CollectGPUHealth did not mutate gpus[0].Healthy to true (mutation contract)")
	}
	// Healthy entries should have an empty Message (omitempty in JSON).
	if healths[0].Message != "" {
		t.Errorf("healthy GPU should have empty message; got %q", healths[0].Message)
	}
	// CheckedAt must be RFC 3339 UTC (ends with "Z").
	if !strings.HasSuffix(healths[0].CheckedAt, "Z") {
		t.Errorf("CheckedAt should be RFC 3339 UTC (end with Z); got %q", healths[0].CheckedAt)
	}
}

func TestCollectGPUHealth_MarksUnhealthyGPU(t *testing.T) {
	// A GPU with VRAMTotalMB == 0 should be marked unhealthy (nvidia-smi
	// couldn't read FB memory — driver issue or GPU reset state).
	gpus := []GPU{
		{
			ID:             "gpu.nvidia.0",
			Index:          0,
			Vendor:         "nvidia",
			Name:           "RTX 5060 Ti",
			VRAMTotalMB:    0, // ← driver error: no VRAM reported
			UtilizationPct: 0,
			Healthy:        true, // pre-health: should be overwritten to false
		},
	}

	healths := CollectGPUHealth(gpus)
	if len(healths) != 1 {
		t.Fatalf("expected 1 health entry; got %d", len(healths))
	}
	if healths[0].State != "unhealthy" {
		t.Errorf("expected state=unhealthy; got %q", healths[0].State)
	}
	if healths[0].Message == "" {
		t.Errorf("unhealthy GPU should have a non-empty message")
	}
	// Mutation: Healthy flipped to false.
	if gpus[0].Healthy {
		t.Errorf("CollectGPUHealth did not mutate gpus[0].Healthy to false (mutation contract)")
	}
}

func TestCollectGPUHealth_NeverReturnsNil(t *testing.T) {
	// Even with an empty input slice, the result must be non-nil.
	healths := CollectGPUHealth([]GPU{})
	if healths == nil {
		t.Fatalf("CollectGPUHealth([]) returned nil — must return empty slice")
	}
	if len(healths) != 0 {
		t.Errorf("CollectGPUHealth([]) should return 0 entries; got %d", len(healths))
	}
	// Marshal stability: empty slice → `[]`, never `null`.
	data, err := json.Marshal(healths)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("empty health slice should marshal as `[]`; got %s", string(data))
	}

	// And nil input must not panic (defensive — callers should pass []GPU{}
	// but we tolerate nil per the "graceful degradation" design principle).
	healthsNil := CollectGPUHealth(nil)
	if healthsNil == nil {
		t.Fatalf("CollectGPUHealth(nil) returned nil — must return empty slice")
	}
	if len(healthsNil) != 0 {
		t.Errorf("CollectGPUHealth(nil) should return 0 entries; got %d", len(healthsNil))
	}
}

// --- nvidiaSmiQuery -------------------------------------------------------

func TestNvidiaSmiQuery_HandlesMissingBinary(t *testing.T) {
	// When nvidia-smi is not on PATH, the function must return nil (not
	// an empty slice — nil is the "no data available" sentinel for the
	// internal API; CollectGPUs converts that to []GPU{}).
	if nvidiaSmiAvailable() {
		t.Skip("nvidia-smi is installed — skipping the no-binary path test")
	}

	rows := nvidiaSmiQuery("index,name")
	if rows != nil {
		t.Errorf("nvidiaSmiQuery with no nvidia-smi should return nil; got %+v", rows)
	}
}

// --- JSON round-trip ------------------------------------------------------

func TestGPU_JSONRoundTrip(t *testing.T) {
	// Populate ALL 14 fields of the GPU struct with realistic v19 values
	// (matching the data-contract example in docs/SCARLIX_DATA_CONTRACTS.md §2).
	// Then marshal + unmarshal + spot-check JSON field names match the
	// frozen contract exactly. If a future change renames a tag, this test
	// fails loudly (stability contract enforcement).
	src := GPU{
		ID:             "gpu.nvidia.0",
		Index:          0,
		Vendor:         "nvidia",
		Name:           "RTX 5060 Ti",
		VRAMTotalMB:    16384,
		VRAMUsedMB:     13600,
		VRAMFreeMB:     2784,
		UtilizationPct: 42,
		TemperatureC:   68,
		PowerW:         165.5,
		Driver:         "570.86.15",
		CUDA:           "12.8",
		ComputeCap:     "12.0",
		Healthy:        true,
	}

	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	j := string(data)

	// Spot-check frozen JSON field names from the data contract.
	for _, want := range []string{
		`"id":"gpu.nvidia.0"`,
		`"index":0`,
		`"vram_total_mb":16384`,
		`"compute_cap":"12.0"`,
		`"healthy":true`,
		`"vendor":"nvidia"`,
		`"name":"RTX 5060 Ti"`,
		`"vram_used_mb":13600`,
		`"vram_free_mb":2784`,
		`"utilization_percent":42`,
		`"temperature_c":68`,
		`"power_w":165.5`,
		`"driver":"570.86.15"`,
		`"cuda":"12.8"`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("GPU JSON missing expected substring %s; got: %s", want, j)
		}
	}

	// Round-trip: unmarshal back into a fresh GPU, verify field equality.
	var dst GPU
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if dst != src {
		t.Errorf("round-trip mismatch:\n src=%+v\n dst=%+v", src, dst)
	}
}

// --- table-driven: CollectGPUHealth edge cases ----------------------------

func TestCollectGPUHealth_TableDriven(t *testing.T) {
	// Cover the health-derivation logic across a range of edge cases.
	// Each case specifies a GPU + the expected State + expected Healthy
	// value mirrored back into the input slice.
	cases := []struct {
		name         string
		gpu          GPU
		wantState    string
		wantMessage  string // "" if no message expected
		wantHealthy  bool
	}{
		{
			name: "healthy_normal",
			gpu: GPU{
				Index: 0, VRAMTotalMB: 16384, UtilizationPct: 10,
			},
			wantState:   "healthy",
			wantHealthy: true,
		},
		{
			name: "healthy_zero_util",
			gpu: GPU{
				Index: 1, VRAMTotalMB: 24576, UtilizationPct: 0,
			},
			wantState:   "healthy",
			wantHealthy: true,
		},
		{
			name: "healthy_max_util",
			gpu: GPU{
				Index: 2, VRAMTotalMB: 16384, UtilizationPct: 100,
			},
			wantState:   "healthy",
			wantHealthy: true,
		},
		{
			name:        "unhealthy_zero_vram",
			gpu:         GPU{Index: 0, VRAMTotalMB: 0, UtilizationPct: 10},
			wantState:   "unhealthy",
			wantMessage: "VRAM or utilization out of range",
			wantHealthy: false,
		},
		{
			name:        "unhealthy_negative_util",
			gpu:         GPU{Index: 0, VRAMTotalMB: 16384, UtilizationPct: -1},
			wantState:   "unhealthy",
			wantMessage: "VRAM or utilization out of range",
			wantHealthy: false,
		},
		{
			name:        "unhealthy_both_bad",
			gpu:         GPU{Index: 3, VRAMTotalMB: 0, UtilizationPct: -1},
			wantState:   "unhealthy",
			wantMessage: "VRAM or utilization out of range",
			wantHealthy: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gpus := []GPU{tc.gpu}
			healths := CollectGPUHealth(gpus)

			if len(healths) != 1 {
				t.Fatalf("expected 1 health; got %d", len(healths))
			}
			h := healths[0]
			if h.State != tc.wantState {
				t.Errorf("state: got %q want %q", h.State, tc.wantState)
			}
			if tc.wantMessage == "" && h.Message != "" {
				t.Errorf("expected empty message; got %q", h.Message)
			}
			if tc.wantMessage != "" && h.Message != tc.wantMessage {
				t.Errorf("message: got %q want %q", h.Message, tc.wantMessage)
			}
			if gpus[0].Healthy != tc.wantHealthy {
				t.Errorf("mutation: gpus[0].Healthy got %v want %v", gpus[0].Healthy, tc.wantHealthy)
			}
		})
	}
}

// --- integration: real nvidia-smi path (gated) ----------------------------

func TestCollectGPUs_RealNvidiaSmi_Smoke(t *testing.T) {
	// Smoke test the real nvidia-smi path on GPU hosts. Skipped on
	// environments without nvidia-smi (CI/dev sandboxes). On a real GPU
	// host this verifies the field mapping actually works end-to-end.
	if !nvidiaSmiAvailable() {
		t.Skip("nvidia-smi not installed — skipping real-path smoke test")
	}

	gpus := CollectGPUs()
	if gpus == nil {
		t.Fatal("CollectGPUs returned nil — must always return non-nil slice")
	}
	if len(gpus) == 0 {
		t.Skip("nvidia-smi installed but reported 0 GPUs — nothing to verify")
	}

	// Spot-check the first GPU: must have a stable ID + the nvidia vendor
	// (hardcoded) + a non-empty name (nvidia-smi never emits empty names
	// on a healthy driver).
	g0 := gpus[0]
	if !strings.HasPrefix(g0.ID, "gpu.nvidia.") {
		t.Errorf("GPU[0].ID should start with 'gpu.nvidia.'; got %q", g0.ID)
	}
	if g0.Vendor != "nvidia" {
		t.Errorf("GPU[0].Vendor should be 'nvidia'; got %q", g0.Vendor)
	}
	if g0.Name == "" {
		t.Errorf("GPU[0].Name should be non-empty on a real GPU")
	}
	// And the whole slice must marshal cleanly.
	if _, err := json.Marshal(gpus); err != nil {
		t.Errorf("marshal real GPU slice failed: %v", err)
	}
}
