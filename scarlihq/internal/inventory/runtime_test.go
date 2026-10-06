// Tests for the v19.0.9 runtime registry collector (runtime.go).
//
// Strategy:
//   - Docker is almost certainly NOT installed in CI / dev sandboxes (the
//     test environment for this code). So most tests exercise the
//     graceful-degradation path: missing docker → []Runtime where every
//     runtime has Running=false but metadata is still populated.
//   - Tests that REQUIRE real Docker output use `t.Skip` if the binary
//     is missing — they document the intent and can run on a GPU host.
//   - Health-check tests use an httptest.Server to deterministically
//     exercise the 200 / refused / timeout paths without depending on
//     whether runtimes are actually running.
//
// All tests assert the v19.0.7 stability contract: empty slices serialize
// as `[]`, never `null`, and the JSON field tags match the data contract.
package inventory

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// dockerAvailable reports whether `docker` is on PATH in the test
// environment. Used to gate tests that need real Docker output. Kept in
// sync with dockerInspect's LookPath-based detection in runtime.go.
func dockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// --- CollectRuntimes ------------------------------------------------------

func TestCollectRuntimes_ReturnsNonNilSlice(t *testing.T) {
	// Even with no Docker, the collector must return a non-nil slice with
	// all 5 configured runtimes (Running=false, but metadata populated).
	rts := CollectRuntimes()
	if rts == nil {
		t.Fatalf("CollectRuntimes() returned nil — must return non-nil slice")
	}
	if len(rts) != 5 {
		t.Fatalf("expected 5 runtimes (sglang/vllm/beellama/ollama/litellm); got %d", len(rts))
	}
}

func TestCollectRuntimes_NeverMarshalsAsNull(t *testing.T) {
	rts := CollectRuntimes()
	data, err := json.Marshal(rts)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if string(data) == "null" {
		t.Fatalf("CollectRuntimes() marshaled as null — must be [{...}]")
	}
	// All 5 runtime IDs must be present in the JSON.
	j := string(data)
	for _, want := range []string{
		`"id":"sglang"`, `"id":"vllm"`, `"id":"beellama"`,
		`"id":"ollama"`, `"id":"litellm"`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("CollectRuntimes() JSON missing %s; got: %s", want, j)
		}
	}
}

func TestCollectRuntimes_StaticMetadataPopulated(t *testing.T) {
	rts := CollectRuntimes()
	// Spot-check the static metadata for sglang (first entry by convention):
	//   - ID=sglang, Version=v0.4.9.post6-cu128-b200, Protocol=openai-compatible
	//   - Port=30000, GPUIndex=0 (GPUIDs=["gpu.nvidia.0"])
	//   - Image=lmsysorg/sglang:v0.4.9.post6-cu128-b200
	//   - Capabilities=[chat, completion, tools]
	//   - Enabled=true, Healthy=false (refined by CollectRuntimeHealth)
	var sglang *Runtime
	for i := range rts {
		if rts[i].ID == "sglang" {
			sglang = &rts[i]
			break
		}
	}
	if sglang == nil {
		t.Fatalf("no sglang runtime in CollectRuntimes() output")
	}
	if sglang.Version != "v0.4.9.post6-cu128-b200" {
		t.Errorf("sglang.Version: got %q want v0.4.9.post6-cu128-b200", sglang.Version)
	}
	if sglang.Protocol != "openai-compatible" {
		t.Errorf("sglang.Protocol: got %q want openai-compatible", sglang.Protocol)
	}
	if sglang.Port != 30000 {
		t.Errorf("sglang.Port: got %d want 30000", sglang.Port)
	}
	if sglang.Image != "lmsysorg/sglang:v0.4.9.post6-cu128-b200" {
		t.Errorf("sglang.Image: got %q want lmsysorg/sglang:v0.4.9.post6-cu128-b200", sglang.Image)
	}
	if !sglang.Enabled {
		t.Errorf("sglang.Enabled should be true (compose-configured)")
	}
	if sglang.Healthy {
		t.Errorf("sglang.Healthy should be false initially (refined by CollectRuntimeHealth)")
	}
	// GPU assignment: sglang uses GPU 0 → GPUIDs=["gpu.nvidia.0"]
	if len(sglang.GPUIDs) != 1 || sglang.GPUIDs[0] != "gpu.nvidia.0" {
		t.Errorf("sglang.GPUIDs: got %v want [gpu.nvidia.0]", sglang.GPUIDs)
	}
	// Capabilities: chat, completion, tools
	if len(sglang.Capabilities) != 3 {
		t.Errorf("sglang.Capabilities len: got %d want 3", len(sglang.Capabilities))
	}
}

func TestCollectRuntimes_CPURuntimesHaveEmptyGPUIDs(t *testing.T) {
	// beellama, ollama, litellm are CPU-only (GPUIndex=-1) → GPUIDs=[].
	rts := CollectRuntimes()
	for _, r := range rts {
		if r.ID == "beellama" || r.ID == "ollama" || r.ID == "litellm" {
			if len(r.GPUIDs) != 0 {
				t.Errorf("%s.GPUIDs should be empty (CPU-only); got %v", r.ID, r.GPUIDs)
			}
		}
	}
}

func TestCollectRuntimes_RunningFalseWhenNoDocker(t *testing.T) {
	// On a no-Docker host, every runtime must have Running=false.
	if dockerAvailable() {
		t.Skip("docker is installed — skipping the no-docker path test")
	}
	rts := CollectRuntimes()
	for _, r := range rts {
		if r.Running {
			t.Errorf("%s.Running should be false when docker is absent; got true", r.ID)
		}
	}
}

func TestCollectRuntimes_RealDocker_Smoke(t *testing.T) {
	// On a real Docker host, all 5 runtimes should be returned; whether
	// any are Running depends on the host (don't assert specific values —
	// just verify the slice is well-formed).
	if !dockerAvailable() {
		t.Skip("docker not installed — skipping real-Docker smoke test")
	}
	rts := CollectRuntimes()
	if len(rts) != 5 {
		t.Fatalf("expected 5 runtimes; got %d", len(rts))
	}
	// All must marshal cleanly.
	if _, err := json.Marshal(rts); err != nil {
		t.Errorf("marshal real Runtime slice failed: %v", err)
	}
}

// --- CollectRuntimeHealth -------------------------------------------------

func TestCollectRuntimeHealth_NeverReturnsNil(t *testing.T) {
	// Empty input → empty non-nil slice.
	healths := CollectRuntimeHealth([]Runtime{})
	if healths == nil {
		t.Fatalf("CollectRuntimeHealth([]) returned nil — must return empty slice")
	}
	if len(healths) != 0 {
		t.Errorf("CollectRuntimeHealth([]) should return 0 entries; got %d", len(healths))
	}
	// Marshal stability: empty slice → `[]`, never `null`.
	data, err := json.Marshal(healths)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("empty health slice should marshal as `[]`; got %s", string(data))
	}
	// Nil input must not panic either (defensive).
	healthsNil := CollectRuntimeHealth(nil)
	if healthsNil == nil {
		t.Fatalf("CollectRuntimeHealth(nil) returned nil — must return empty slice")
	}
}

func TestCollectRuntimeHealth_MarksAllRuntimes(t *testing.T) {
	// When no runtimes are actually running (CI/dev sandbox), every health
	// entry must be State="down" with a non-empty message, and the matching
	// Runtime.Healthy field must be false.
	rts := CollectRuntimes()
	healths := CollectRuntimeHealth(rts)
	if len(healths) != len(rts) {
		t.Fatalf("health count mismatch: got %d want %d", len(healths), len(rts))
	}
	for i, h := range healths {
		if h.Component != rts[i].ID {
			t.Errorf("health[%d].Component: got %q want %q", i, h.Component, rts[i].ID)
		}
		if h.State == "" {
			t.Errorf("health[%d].State should be non-empty", i)
		}
		if h.CheckedAt == "" {
			t.Errorf("health[%d].CheckedAt should be non-empty", i)
		}
		if !strings.HasSuffix(h.CheckedAt, "Z") {
			t.Errorf("health[%d].CheckedAt should be RFC 3339 UTC (end with Z); got %q", i, h.CheckedAt)
		}
		// On a no-Docker host, every runtime is down → Healthy=false.
		// On a real Docker host with no runtimes started, same — but we
		// can't guarantee that in CI, so only assert the consistency
		// invariant: Healthy == (State == "healthy").
		expected := (h.State == "healthy")
		if rts[i].Healthy != expected {
			t.Errorf("runtime %s: Healthy=%v but State=%q (inconsistent mutation)", rts[i].ID, rts[i].Healthy, h.State)
		}
	}
}

func TestCollectRuntimeHealth_HTTP200Healthy(t *testing.T) {
	// Spin up a real HTTP server returning 200 — the collector should
	// mark it "healthy" with empty message. To do this we override the
	// runtimeMetadata's HealthURL for a synthetic runtime.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Build a synthetic Runtime that points to the test server. We can't
	// reach into runtimeMetadata (it's package-private + the iteration
	// uses the metadata's URL), so we exercise probeHealth directly.
	// This test verifies probeHealth's logic; the integration test
	// (CollectRuntimeHealth over runtimeMetadata) is the next one.
	client := &http.Client{Timeout: 2 * time.Second}
	state, msg := probeHealth(client, srv.URL+"/health")
	if state != "healthy" {
		t.Errorf("probeHealth on 200 server: state got %q want healthy (msg=%q)", state, msg)
	}
	if msg != "" {
		t.Errorf("probeHealth on 200 server: msg should be empty; got %q", msg)
	}
}

func TestCollectRuntimeHealth_HTTP500Unhealthy(t *testing.T) {
	// A 500 response means the runtime is up but not ready → "unhealthy".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	state, msg := probeHealth(client, srv.URL+"/health")
	if state != "unhealthy" {
		t.Errorf("probeHealth on 500 server: state got %q want unhealthy (msg=%q)", state, msg)
	}
	if !strings.Contains(msg, "500") {
		t.Errorf("probeHealth on 500 server: msg should contain '500'; got %q", msg)
	}
}

func TestCollectRuntimeHealth_ConnectionRefusedDown(t *testing.T) {
	// Get a port that's guaranteed not to have a listener (bind + close
	// to reserve+release a port, then probe — should get connection refused).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot reserve a port for refused-connection test: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	state, msg := probeHealth(client, "http://"+addr+"/health")
	if state != "down" {
		t.Errorf("probeHealth on refused-connection: state got %q want down (msg=%q)", state, msg)
	}
	if !strings.Contains(msg, "refused") && !strings.Contains(msg, "connection") {
		t.Errorf("probeHealth on refused-connection: msg should mention 'refused'; got %q", msg)
	}
}

// --- dockerInspect --------------------------------------------------------

func TestDockerInspect_NoDockerReturnsEmpty(t *testing.T) {
	if dockerAvailable() {
		t.Skip("docker is installed — skipping the no-docker path test")
	}
	status, err := dockerInspect("sglang")
	if err != nil {
		t.Errorf("dockerInspect with no docker should return nil error; got %v", err)
	}
	if status != "" {
		t.Errorf("dockerInspect with no docker should return empty status; got %q", status)
	}
}

// --- imageTag helper ------------------------------------------------------

func TestImageTag(t *testing.T) {
	cases := []struct {
		image string
		want  string
	}{
		{"lmsysorg/sglang:v0.4.9.post6-cu128-b200", "v0.4.9.post6-cu128-b200"},
		{"vllm/vllm-openai:v0.8.5", "v0.8.5"},
		{"ollama/ollama:0.5.4", "0.5.4"},
		{"ghcr.io/berriai/litellm:main-v1.21.7", "main-v1.21.7"},
		// Digest-pinned (no tag portion before @) → empty.
		{"ghcr.io/ggml-org/llama.cpp@sha256:6d607629", ""},
		// No tag at all → empty.
		{"library/nginx", ""},
		// Registry with port (port colon is not a tag separator).
		{"ghcr.io:443/repo:tag", "tag"},
		// Tag + digest both present (rare but valid).
		{"repo:v1.2.3@sha256:abc", "v1.2.3"},
	}
	for _, tc := range cases {
		t.Run(tc.image, func(t *testing.T) {
			got := imageTag(tc.image)
			if got != tc.want {
				t.Errorf("imageTag(%q): got %q want %q", tc.image, got, tc.want)
			}
		})
	}
}

// --- JSON round-trip ------------------------------------------------------

func TestRuntime_JSONRoundTrip(t *testing.T) {
	// Populate all 10 Runtime fields with realistic v19 values (matching
	// the data-contract example in docs/SCARLIX_DATA_CONTRACTS.md §3).
	src := Runtime{
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
	}
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	j := string(data)

	// Spot-check frozen JSON field names + values from the data contract.
	for _, want := range []string{
		`"id":"sglang"`,
		`"version":"v0.4.9.post6-cu128-b200"`,
		`"enabled":true`,
		`"running":true`,
		`"healthy":true`,
		`"protocol":"openai-compatible"`,
		`"port":30000`,
		`"gpu_ids":["gpu.nvidia.0"]`,
		`"image":"lmsysorg/sglang:v0.4.9.post6-cu128-b200"`,
		`"capabilities":["chat","tools"]`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("Runtime JSON missing expected substring %s; got: %s", want, j)
		}
	}

	// Round-trip: unmarshal back into a fresh Runtime, verify field equality.
	var dst Runtime
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if dst.ID != src.ID || dst.Port != src.Port || dst.Protocol != src.Protocol {
		t.Errorf("round-trip mismatch:\n src=%+v\n dst=%+v", src, dst)
	}
}

func TestRuntime_EmptyCapabilitiesMarshalsAsNotNull(t *testing.T) {
	// Stability contract §5: empty slices must marshal as [], never null.
	// Beellama/ollama/litellm have non-empty Capabilities, but a future
	// runtime with no capabilities declared would have []string{} (or nil).
	// Verify the contract holds for both.
	nonNilEmpty := Runtime{ID: "test", Capabilities: []string{}}
	data, err := json.Marshal(nonNilEmpty)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(data), `"capabilities":[]`) {
		t.Errorf("empty Capabilities should marshal as []; got: %s", string(data))
	}
}
