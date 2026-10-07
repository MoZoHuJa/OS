package inventory

import (
	"encoding/json"
	"strings"
	"testing"
)

// v19.0.7 — type-contract tests for the inventory package.
//
// These tests do NOT exercise any runtime discovery logic (that comes in
// v19.0.8+). They only assert that:
//   - each struct can be instantiated with zero values,
//   - each struct can be JSON-marshaled (to []byte) and unmarshaled back,
//   - the JSON field names on exported fields match the data contract
//     (docs/SCARLIX_DATA_CONTRACTS.md),
//   - the SystemStatus composite marshals correctly with nested arrays.
//
// If a future change renames a JSON tag, the spot-check sub-tests below
// will fail loudly — that is intentional (stability contract).

// roundTrip marshals v to JSON, unmarshals into a fresh zero-value of the
// same type, and returns the JSON string (for tag spot-checks) + the
// unmarshal error if any.
func roundTrip[T any](t *testing.T, v T) (string, error) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return string(data), err
	}
	return string(data), nil
}

func TestGPU_ZeroValueRoundTrip(t *testing.T) {
	var g GPU
	j, err := roundTrip(t, g)
	if err != nil {
		t.Fatalf("GPU zero-value round-trip failed: %v (json=%s)", err, j)
	}
	// Spot-check JSON field names from the data contract.
	for _, want := range []string{
		`"id"`, `"index"`, `"vendor"`, `"name"`,
		`"vram_total_mb"`, `"vram_used_mb"`, `"vram_free_mb"`,
		`"utilization_percent"`, `"temperature_c"`, `"power_w"`,
		`"driver"`, `"cuda"`, `"compute_cap"`, `"healthy"`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("GPU JSON missing expected field tag %s; got: %s", want, j)
		}
	}
}

func TestRuntime_ZeroValueRoundTrip(t *testing.T) {
	var r Runtime
	j, err := roundTrip(t, r)
	if err != nil {
		t.Fatalf("Runtime zero-value round-trip failed: %v (json=%s)", err, j)
	}
	for _, want := range []string{
		`"id"`, `"version"`, `"enabled"`, `"running"`, `"healthy"`,
		`"protocol"`, `"port"`, `"gpu_ids"`, `"image"`, `"capabilities"`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("Runtime JSON missing expected field tag %s; got: %s", want, j)
		}
	}
}

func TestModel_ZeroValueRoundTrip(t *testing.T) {
	var m Model
	j, err := roundTrip(t, m)
	if err != nil {
		t.Fatalf("Model zero-value round-trip failed: %v (json=%s)", err, j)
	}
	for _, want := range []string{
		`"id"`, `"path"`, `"format"`, `"quantization"`, `"parameters"`,
		`"context_length"`, `"estimated_vram_mb"`, `"capabilities"`,
		`"supported_runtimes"`, `"present"`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("Model JSON missing expected field tag %s; got: %s", want, j)
		}
	}
}

func TestService_ZeroValueRoundTrip(t *testing.T) {
	var s Service
	j, err := roundTrip(t, s)
	if err != nil {
		t.Fatalf("Service zero-value round-trip failed: %v (json=%s)", err, j)
	}
	for _, want := range []string{
		`"id"`, `"name"`, `"type"`, `"status"`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("Service JSON missing expected field tag %s; got: %s", want, j)
		}
	}
	// omitempty fields should NOT appear on zero value.
	if strings.Contains(j, `"port"`) {
		t.Errorf("Service zero-value JSON should omit `port` (omitempty); got: %s", j)
	}
	if strings.Contains(j, `"container_id"`) {
		t.Errorf("Service zero-value JSON should omit `container_id` (omitempty); got: %s", j)
	}
	if strings.Contains(j, `"uptime"`) {
		t.Errorf("Service zero-value JSON should omit `uptime` (omitempty); got: %s", j)
	}
}

func TestHealth_ZeroValueRoundTrip(t *testing.T) {
	var h Health
	j, err := roundTrip(t, h)
	if err != nil {
		t.Fatalf("Health zero-value round-trip failed: %v (json=%s)", err, j)
	}
	for _, want := range []string{
		`"component"`, `"state"`, `"checked_at"`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("Health JSON missing expected field tag %s; got: %s", want, j)
		}
	}
	// Message is omitempty.
	if strings.Contains(j, `"message"`) {
		t.Errorf("Health zero-value JSON should omit `message` (omitempty); got: %s", j)
	}
}

func TestSystemStatus_NestedArrays(t *testing.T) {
	// Populate with realistic-looking sample data (1 GPU, 1 runtime,
	// 1 model, 1 service, 1 health entry) to verify nested arrays
	// marshal correctly under SystemStatus.
	s := SystemStatus{
		Version:       "19.0.7",
		Mode:          "ai",
		UptimeSeconds: 3600,
		Timestamp:     "2025-01-01T00:00:00Z",
		GPUs: []GPU{{
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
		}},
		Runtimes: []Runtime{{
			ID:           "sglang",
			Version:      "v0.4.9.post6-cu128-b200",
			Enabled:      true,
			Running:      true,
			Healthy:      true,
			Protocol:     "openai-compatible",
			Port:         30000,
			GPUIDs:       []string{"gpu.nvidia.0"},
			Image:        "lmsysorg/sglang:v0.4.9.post6-cu128-b200",
			Capabilities: []string{"chat", "tools"},
		}},
		Models: []Model{{
			ID:                "qwen3-14b-awq",
			Path:              "/models/Qwen3-14B-AWQ",
			Format:            "safetensors",
			Quantization:      "awq",
			Parameters:        "14B",
			ContextLength:     32768,
			EstimatedVRAM:     13600,
			Capabilities:      []string{"coding", "reasoning", "chat"},
			SupportedRuntimes: []string{"sglang", "vllm"},
			Present:           true,
		}},
		Services: []Service{{
			ID:     "scarlihq",
			Name:   "ScarliHQ dashboard",
			Type:   "docker",
			Status: "running",
			Port:   8090,
			Uptime: "1h0m0s",
		}},
		Health: []Health{{
			Component: "sglang",
			State:     "healthy",
			CheckedAt: "2025-01-01T00:00:00Z",
		}},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("SystemStatus marshal failed: %v", err)
	}
	j := string(data)
	// Top-level fields.
	for _, want := range []string{
		`"version"`, `"mode"`, `"uptime_seconds"`, `"timestamp"`,
		`"gpus"`, `"runtimes"`, `"models"`, `"services"`, `"health"`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("SystemStatus JSON missing top-level field tag %s; got: %s", want, j)
		}
	}
	// Nested values that prove arrays actually contain entries.
	for _, want := range []string{
		`"gpu.nvidia.0"`, // from GPUs[].ID
		`"lmsysorg/sglang:v0.4.9.post6-cu128-b200"`, // from Runtimes[].Image
		`"qwen3-14b-awq"`,           // from Models[].ID
		`"scarlihq"`,                // from Services[].ID
		`"openai-compatible"`,       // from Runtimes[].Protocol
		`"v0.4.9.post6-cu128-b200"`, // from Runtimes[].Version
		`"RTX 5060 Ti"`,             // from GPUs[].Name
	} {
		if !strings.Contains(j, want) {
			t.Errorf("SystemStatus JSON missing expected nested value %s; got: %s", want, j)
		}
	}
	// Round-trip back to SystemStatus — must not error.
	var out SystemStatus
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("SystemStatus unmarshal failed: %v", err)
	}
	if out.Version != "19.0.7" {
		t.Errorf("post-unmarshal Version mismatch: got %q want %q", out.Version, "19.0.7")
	}
	if len(out.GPUs) != 1 || out.GPUs[0].Name != "RTX 5060 Ti" {
		t.Errorf("post-unmarshal GPUs mismatch: %+v", out.GPUs)
	}
	if len(out.Runtimes) != 1 || out.Runtimes[0].Port != 30000 {
		t.Errorf("post-unmarshal Runtimes mismatch: %+v", out.Runtimes)
	}
	if len(out.Models) != 1 || out.Models[0].ID != "qwen3-14b-awq" {
		t.Errorf("post-unmarshal Models mismatch: %+v", out.Models)
	}
	if len(out.Services) != 1 || out.Services[0].Port != 8090 {
		t.Errorf("post-unmarshal Services mismatch: %+v", out.Services)
	}
	if len(out.Health) != 1 || out.Health[0].State != "healthy" {
		t.Errorf("post-unmarshal Health mismatch: %+v", out.Health)
	}
}

func TestSystemStatus_EmptyArraysMarshalAsNotNull(t *testing.T) {
	// Stability contract: SystemStatus slices should marshal as `[]`
	// (empty array) when explicitly empty, not `null` (which is what
	// a nil slice would produce). The scarlix CLI is required to emit
	// empty arrays; ScarliHQ consumer code assumes non-nil.
	s := SystemStatus{
		Version:  "19.0.7",
		Mode:     "stop",
		GPUs:     []GPU{},
		Runtimes: []Runtime{},
		Models:   []Model{},
		Services: []Service{},
		Health:   []Health{},
	}
	j, err := roundTrip(t, s)
	if err != nil {
		t.Fatalf("empty-array SystemStatus round-trip failed: %v (json=%s)", err, j)
	}
	for _, want := range []string{
		`"gpus":[]`, `"runtimes":[]`, `"models":[]`, `"services":[]`, `"health":[]`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("empty array not marshaled as []: missing %s; got: %s", want, j)
		}
	}
}
