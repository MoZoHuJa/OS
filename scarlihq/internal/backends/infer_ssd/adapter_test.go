package infer_ssd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
)

// validProfile builds a standalone-valid, ENABLED runtime profile (the
// golden fixture — every test mutates one fact and asserts the NAMED
// refusal).
func validProfile() *RuntimeProfile {
	p := &RuntimeProfile{
		SchemaVersion: RuntimeProfileSchemaVersion,
		ProfileID:     "infer-ssd-moe-experimental",
		BackendID:     "infer_ssd",
		Enabled:       true,
		Experimental:  true,
	}
	p.Image.Reference = "ghcr.io/scarlix/infer-ssd:v0.1@sha256:" + strings.Repeat("aa", 32)
	p.Compatibility.ModelManifest = "deepseek-moe-test"
	p.Compatibility.CheckpointSHA256 = strings.Repeat("cd", 32)
	p.Compatibility.CUDA = "12.8"
	p.Compatibility.DriverMinimum = "570.00"
	p.Compatibility.GPUComputeCapabilities = []string{"sm_120"}
	p.Execution.Strategy = StrategySSDBackedMoE
	p.Execution.ContextLimit = 8192
	p.Execution.MaxConcurrentRequests = 1
	p.Resources.GPU.Mode = GPUModeExclusive
	p.Resources.GPU.VRAMReserveBytes = 1 << 30
	p.Resources.HostRAMBytes = 4 << 30
	p.Resources.CPUThreads = 8
	p.Resources.Storage.DeviceID = "storage.nvme.0"
	p.Resources.Storage.ModelPath = "/var/lib/scarlix/models/deepseek-moe-test"
	p.Resources.Storage.ExpertPath = "/var/lib/scarlix/experts/deepseek-moe-test"
	p.Resources.Storage.MinimumFreeBytes = 64 << 30
	p.Resources.Storage.MinimumReadMBps = 2000
	p.Resources.Storage.MinimumRandomReadIOPS = 50000
	p.Resources.Storage.MaximumP95LatencyMs = 1.5
	p.Network.NetworkProfile = NetworkProfileInternal
	p.Health.Type = HealthTypeHTTP
	p.Health.Path = "/health"
	p.Health.StartupTimeoutSeconds = 600
	return p
}

// moeModel builds the model execution metadata the golden profile binds
// to (the projection of a schema-valid, placement-eligible manifest).
func moeModel() modelmeta.ModelExecution {
	return modelmeta.ModelExecution{
		ID:                   "deepseek-moe-test",
		Revision:             "rev-1",
		CheckpointSHA256:     strings.Repeat("cd", 32),
		Architecture:         modelmeta.ArchitectureMoE,
		Format:               "safetensors",
		Quantization:         "awq",
		ContextLimit:         8192,
		KVCacheBytesPerToken: 8192,
		ExpertCount:          8,
		ExpertsPerToken:      2,
		ExpertBytes:          512 << 20,
		SharedBytes:          2 << 30,
	}
}

// TestRuntimeProfileValidationTruthTable — every rule of the §8.4
// contract refuses BY NAME (one mutated fact per case).
func TestRuntimeProfileValidationTruthTable(t *testing.T) {
	type tc struct {
		name   string
		mutate func(p *RuntimeProfile)
		marker string
	}
	cases := []tc{
		{"wrong schema version", func(p *RuntimeProfile) { p.SchemaVersion = "scarlix.runtime-profile/v2" }, "is not \"scarlix.runtime-profile/v1\""},
		{"empty profile id", func(p *RuntimeProfile) { p.ProfileID = "" }, "profile_id is required"},
		{"unsafe profile id", func(p *RuntimeProfile) { p.ProfileID = "../escape" }, "not a safe closed identifier"},
		{"wrong backend id", func(p *RuntimeProfile) { p.BackendID = "sglang" }, "is a wiring error refused by name"},
		{"non-experimental (production claim)", func(p *RuntimeProfile) { p.Experimental = false }, "experimental must be true"},
		{"empty image", func(p *RuntimeProfile) { p.Image.Reference = "" }, "image.reference is required"},
		{"unpinned image", func(p *RuntimeProfile) { p.Image.Reference = "ghcr.io/scarlix/infer-ssd:v0.1" }, "carries no @sha256: digest"},
		{"empty model manifest", func(p *RuntimeProfile) { p.Compatibility.ModelManifest = "" }, "compatibility.model_manifest is required"},
		{"short checkpoint hash", func(p *RuntimeProfile) { p.Compatibility.CheckpointSHA256 = "abcd" }, "not a 64-hex-char SHA-256 fingerprint"},
		{"empty cuda", func(p *RuntimeProfile) { p.Compatibility.CUDA = "" }, "compatibility.cuda is required"},
		{"empty driver minimum", func(p *RuntimeProfile) { p.Compatibility.DriverMinimum = "" }, "compatibility.driver_minimum is required"},
		{"empty capabilities", func(p *RuntimeProfile) { p.Compatibility.GPUComputeCapabilities = nil }, "gpu_compute_capabilities is required non-empty"},
		{"empty capability entry", func(p *RuntimeProfile) { p.Compatibility.GPUComputeCapabilities = []string{"sm_120", ""} }, "a placeholder slot is not a capability"},
		{"wrong strategy", func(p *RuntimeProfile) { p.Execution.Strategy = "moe-gpu-resident" }, "is not \"ssd-backed-moe\""},
		{"zero context limit", func(p *RuntimeProfile) { p.Execution.ContextLimit = 0 }, "execution.context_limit must be > 0"},
		{"concurrency above one (contention claim)", func(p *RuntimeProfile) { p.Execution.MaxConcurrentRequests = 2 }, "must be exactly 1 in v19.8.0"},
		{"shared gpu mode", func(p *RuntimeProfile) { p.Resources.GPU.Mode = "shared" }, "one allocation per GPU"},
		{"empty storage device", func(p *RuntimeProfile) { p.Resources.Storage.DeviceID = "" }, "resources.storage.device_id is required"},
		{"empty model path", func(p *RuntimeProfile) { p.Resources.Storage.ModelPath = "" }, "resources.storage.model_path is required"},
		{"empty expert path", func(p *RuntimeProfile) { p.Resources.Storage.ExpertPath = "" }, "resources.storage.expert_path is required"},
		{"zero minimum free", func(p *RuntimeProfile) { p.Resources.Storage.MinimumFreeBytes = 0 }, "minimum_free_bytes must be > 0"},
		{"zero sequential floor", func(p *RuntimeProfile) { p.Resources.Storage.MinimumReadMBps = 0 }, "minimum_read_mbps must be > 0"},
		{"zero random floor", func(p *RuntimeProfile) { p.Resources.Storage.MinimumRandomReadIOPS = 0 }, "minimum_random_read_iops must be > 0"},
		{"zero latency ceiling", func(p *RuntimeProfile) { p.Resources.Storage.MaximumP95LatencyMs = 0 }, "maximum_p95_latency_ms must be > 0"},
		{"empty network profile", func(p *RuntimeProfile) { p.Network.NetworkProfile = "" }, "network.network_profile is required"},
		{"published ports (new network surface)", func(p *RuntimeProfile) { p.Network.PublishedPorts = []int{8080} }, "published_ports must be EMPTY"},
		{"wrong health type", func(p *RuntimeProfile) { p.Health.Type = "tcp" }, "the v19.8.0 health probe contract is http"},
		{"health path without slash", func(p *RuntimeProfile) { p.Health.Path = "health" }, "must start with \"/\""},
		{"zero startup timeout", func(p *RuntimeProfile) { p.Health.StartupTimeoutSeconds = 0 }, "startup_timeout_seconds must be > 0"},
		{"placeholder image digest", func(p *RuntimeProfile) { p.Image.Reference = "ghcr.io/x/y@sha256:REPLACE_WITH_VERIFIED_DIGEST" }, "carries a placeholder value"},
		{"placeholder checkpoint", func(p *RuntimeProfile) { p.Compatibility.CheckpointSHA256 = "REPLACE_WITH_VERIFIED_CHECKPOINT_HASH" }, "carries a placeholder value"},
		{"placeholder capability", func(p *RuntimeProfile) { p.Compatibility.GPUComputeCapabilities = []string{"VERIFIED_CAPABILITY"} }, "carries a placeholder value"},
		{"placeholder cuda", func(p *RuntimeProfile) { p.Compatibility.CUDA = "VERIFIED_VERSION" }, "carries a placeholder value"},
	}
	for _, c := range cases {
		p := validProfile()
		c.mutate(p)
		reasons := ValidateRuntimeProfile(p)
		if len(reasons) == 0 {
			t.Errorf("%s: expected a named refusal, got none", c.name)
			continue
		}
		found := false
		for _, r := range reasons {
			if strings.Contains(r, c.marker) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: refusal must carry the marker %q; got %v", c.name, c.marker, reasons)
		}
	}
	// The golden fixture itself validates cleanly.
	if reasons := ValidateRuntimeProfile(validProfile()); len(reasons) != 0 {
		t.Errorf("the golden profile must validate cleanly; got %v", reasons)
	}
	// A DISABLED profile is still VALID (documentation — §8.8's initial
	// status; the planner's seam turns it into not-configured).
	disabled := validProfile()
	disabled.Enabled = false
	if reasons := ValidateRuntimeProfile(disabled); len(reasons) != 0 {
		t.Errorf("a disabled profile is valid documentation; got %v", reasons)
	}
}

// TestLoadRuntimeProfile — the fail-closed loader: good YAML loads with
// provenance; malformed/invalid/missing files are NAMED errors.
func TestLoadRuntimeProfile(t *testing.T) {
	dir := t.TempDir()
	good := validProfile()
	goodYAML := "schema_version: \"scarlix.runtime-profile/v1\"\n" +
		"profile_id: \"infer-ssd-moe-experimental\"\n" +
		"backend_id: \"infer_ssd\"\n" +
		"enabled: true\n" +
		"experimental: true\n" +
		"image:\n  reference: \"ghcr.io/scarlix/infer-ssd:v0.1@sha256:" + strings.Repeat("aa", 32) + "\"\n" +
		"compatibility:\n  model_manifest: \"deepseek-moe-test\"\n  checkpoint_sha256: \"" + strings.Repeat("cd", 32) + "\"\n  cuda: \"12.8\"\n  driver_minimum: \"570.00\"\n  gpu_compute_capabilities:\n    - \"sm_120\"\n" +
		"execution:\n  strategy: \"ssd-backed-moe\"\n  context_limit: 8192\n  max_concurrent_requests: 1\n" +
		"resources:\n  gpu:\n    mode: \"exclusive\"\n    vram_reserve_bytes: 1073741824\n  host_ram_bytes: 4294967296\n  cpu_threads: 8\n  storage:\n    device_id: \"storage.nvme.0\"\n    model_path: \"/var/lib/scarlix/models/x\"\n    expert_path: \"/var/lib/scarlix/experts/x\"\n    minimum_free_bytes: 68719476736\n    minimum_read_mbps: 2000\n    minimum_random_read_iops: 50000\n    maximum_p95_latency_ms: 1.5\n" +
		"network:\n  network_profile: \"scarlix-compute-internal\"\n  published_ports: []\n" +
		"health:\n  type: \"http\"\n  path: \"/health\"\n  startup_timeout_seconds: 600\n"
	goodPath := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(goodPath, []byte(goodYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRuntimeProfile(goodPath)
	if err != nil {
		t.Fatalf("the good profile must load: %v", err)
	}
	if loaded.ProfileID != good.ProfileID || !loaded.Enabled || loaded.SourcePath != goodPath {
		t.Fatalf("loaded profile drift: %+v", loaded)
	}
	// Unparsable YAML: named error.
	badPath := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(badPath, []byte(":::not yaml["), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeProfile(badPath); err == nil || !strings.Contains(err.Error(), "YAML parse failed") {
		t.Errorf("unparsable YAML must be a named parse error: %v", err)
	}
	// Invalid content (a production claim): named error with the reasons.
	claimPath := filepath.Join(dir, "claim.yaml")
	claimYAML := strings.Replace(goodYAML, "experimental: true", "experimental: false", 1)
	if err := os.WriteFile(claimPath, []byte(claimYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeProfile(claimPath); err == nil || !strings.Contains(err.Error(), "experimental must be true") {
		t.Errorf("a non-experimental profile must be refused by name: %v", err)
	}
	// Missing file: named error.
	if _, err := LoadRuntimeProfile(filepath.Join(dir, "nope.yaml")); err == nil {
		t.Errorf("a missing file must be a named error")
	}
	// Empty path: named error.
	if _, err := LoadRuntimeProfile(""); err == nil {
		t.Errorf("an empty path must be a named error")
	}
}

// TestNewAdapterFailClosed — an adapter over a nil/disabled/invalid
// profile is a phantom, refused by name.
func TestNewAdapterFailClosed(t *testing.T) {
	if _, err := NewAdapter(nil, nil); err == nil {
		t.Fatalf("a nil profile must be refused")
	}
	disabled := validProfile()
	disabled.Enabled = false
	if _, err := NewAdapter(disabled, nil); err == nil || !strings.Contains(err.Error(), "DISABLED") {
		t.Fatalf("a disabled profile must be refused by name (the §8.8 initial status): %v", err)
	}
	invalid := validProfile()
	invalid.Experimental = false
	if _, err := NewAdapter(invalid, nil); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("an invalid profile must be refused by name: %v", err)
	}
	// The golden profile constructs (a nil prober is allowed — Probe
	// refuses by name, the seam discipline).
	adapter, err := NewAdapter(validProfile(), nil)
	if err != nil {
		t.Fatalf("the golden profile must construct: %v", err)
	}
	if adapter.ID() != InferSSDBackendID {
		t.Fatalf("adapter id: %s", adapter.ID())
	}
	row := adapter.Capabilities()
	if row.BackendID != InferSSDBackendID || !row.SupportsSSDExperts || !row.Experimental {
		t.Fatalf("adapter capabilities row: %+v", row)
	}
}

// TestAdapterValidateModel — the §8.2 model compatibility matrix (one
// mutated fact per case, every refusal named).
func TestAdapterValidateModel(t *testing.T) {
	adapter, err := NewAdapter(validProfile(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.ValidateModel(moeModel()); err != nil {
		t.Fatalf("the golden model must validate: %v", err)
	}
	type tc struct {
		name   string
		mutate func(m *modelmeta.ModelExecution)
		marker string
	}
	cases := []tc{
		{"wrong format", func(m *modelmeta.ModelExecution) { m.Format = "gguf" }, "model-format-unsupported"},
		{"dense architecture", func(m *modelmeta.ModelExecution) { m.Architecture = modelmeta.ArchitectureDense }, "model-architecture-unsupported"},
		{"unknown expert count", func(m *modelmeta.ModelExecution) { m.ExpertCount = 0 }, "model-geometry-unknown"},
		{"unknown expert bytes", func(m *modelmeta.ModelExecution) { m.ExpertBytes = 0 }, "model-geometry-unknown"},
		{"different model id", func(m *modelmeta.ModelExecution) { m.ID = "other-model" }, "model-not-bound"},
		{"different checkpoint", func(m *modelmeta.ModelExecution) { m.CheckpointSHA256 = strings.Repeat("ce", 32) }, "model-not-bound"},
	}
	for _, c := range cases {
		m := moeModel()
		c.mutate(&m)
		err := adapter.ValidateModel(m)
		if err == nil {
			t.Errorf("%s: expected a named refusal", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.marker) {
			t.Errorf("%s: refusal must carry %q; got %v", c.name, c.marker, err)
		}
	}
}

// TestAdapterEstimate — the §8.3 resource estimation with v19.8.1's
// single-count contract (KPK-19.8.0-P2-03): VRAM = shared + the profile's
// reserve ONLY, the KV cache rides KVCacheBytes (the admission gate adds
// it to the assigned GPU exactly once); the storage demand = experts +
// the minimum free floor with the measured floors; the profile's additive
// host demands ride the vector.
func TestAdapterEstimate(t *testing.T) {
	profile := validProfile()
	adapter, err := NewAdapter(profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := moeModel()
	req := EstimateRequest{ContextTokens: 4096, MaxConcurrent: 1}
	vec, err := adapter.Estimate(m, req)
	if err != nil {
		t.Fatal(err)
	}
	kv := uint64(req.ContextTokens) * m.KVCacheBytesPerToken
	wantVRAM := m.SharedBytes + profile.Resources.GPU.VRAMReserveBytes // KV EXCLUDED (P2-03)
	if len(vec.GPU) != 1 || vec.GPU[0].VRAMBytes != wantVRAM || !vec.GPU[0].Exclusive {
		t.Fatalf("GPU demand: %+v (want VRAM %d exclusive — KV rides KVCacheBytes, never VRAMBytes)", vec.GPU, wantVRAM)
	}
	if wantVRAM == m.SharedBytes+kv+profile.Resources.GPU.VRAMReserveBytes {
		t.Fatalf("the fixture's KV must be non-zero for the single-count assertion to mean anything")
	}
	if vec.KVCacheBytes != kv {
		t.Fatalf("KV bytes: %d, want %d", vec.KVCacheBytes, kv)
	}
	if vec.RAMBytes != profile.Resources.HostRAMBytes || vec.CPUThreads != float64(profile.Resources.CPUThreads) {
		t.Fatalf("host demands: RAM %d, threads %v", vec.RAMBytes, vec.CPUThreads)
	}
	experts := uint64(m.ExpertCount) * m.ExpertBytes
	if len(vec.Storage) != 1 {
		t.Fatalf("the ssd-backed-moe vector must carry ONE storage demand: %+v", vec.Storage)
	}
	s := vec.Storage[0]
	if s.DeviceID != profile.Resources.Storage.DeviceID ||
		s.MinimumFreeBytes != experts+profile.Resources.Storage.MinimumFreeBytes ||
		s.MinimumReadMBps != profile.Resources.Storage.MinimumReadMBps ||
		s.MinimumRandomReadIOPS != profile.Resources.Storage.MinimumRandomReadIOPS ||
		s.MaximumP95LatencyMs != profile.Resources.Storage.MaximumP95LatencyMs {
		t.Fatalf("storage demand drift: %+v", s)
	}
	// Estimation refusals: unbound model, non-positive context, context
	// above the profile's ceiling.
	if _, err := adapter.Estimate(modelmeta.ModelExecution{ID: "other"}, req); err == nil {
		t.Errorf("an unbound model must refuse the estimate")
	}
	if _, err := adapter.Estimate(m, EstimateRequest{ContextTokens: 0}); err == nil {
		t.Errorf("a non-positive context must refuse the estimate")
	}
	if _, err := adapter.Estimate(m, EstimateRequest{ContextTokens: profile.Execution.ContextLimit + 1}); err == nil {
		t.Errorf("a context above the profile ceiling must refuse the estimate")
	}
}

// TestAdapterBuildProfile — the validated launch shape for the Host
// Bridge path (never a launch): the binding checks + the returned
// profile's facts.
func TestAdapterBuildProfile(t *testing.T) {
	adapter, err := NewAdapter(validProfile(), nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := PlanBinding{
		PlanID:        "plan-abc",
		BackendID:     "infer_ssd",
		Strategy:      StrategySSDBackedMoE,
		ModelID:       "deepseek-moe-test",
		ContextTokens: 4096,
		MaxConcurrent: 1,
		GPUUUIDs:      []string{"gpu-uuid-a"},
		Principal:     "operator",
	}
	approved, err := adapter.BuildProfile(binding)
	if err != nil {
		t.Fatal(err)
	}
	if approved.ProfileID != "infer-ssd-moe-experimental" || approved.Strategy != StrategySSDBackedMoE {
		t.Fatalf("approved profile: %+v", approved)
	}
	if approved.HealthEndpointURL != "" {
		t.Fatalf("the adapter never assigns endpoints — the Host Bridge path owns endpoint assignment")
	}
	if approved.ImageReference == "" || approved.ExpertPath == "" || approved.StorageDeviceID == "" {
		t.Fatalf("the approved profile must carry the launch facts: %+v", approved)
	}
	// The binding refusals: empty plan id, wrong backend, wrong
	// strategy, wrong model, context over the ceiling, concurrency over
	// the §8.8 cap.
	type tc struct {
		name   string
		mutate func(b *PlanBinding)
		marker string
	}
	for _, c := range []tc{
		{"empty plan id", func(b *PlanBinding) { b.PlanID = "" }, "no plan id"},
		{"wrong backend", func(b *PlanBinding) { b.BackendID = "sglang" }, "never launches through this adapter"},
		{"wrong strategy", func(b *PlanBinding) { b.Strategy = "moe-gpu-resident" }, "the one strategy this engine executes"},
		{"wrong model", func(b *PlanBinding) { b.ModelID = "other-model" }, "§8.2 exact-combination rule"},
		{"context over ceiling", func(b *PlanBinding) { b.ContextTokens = 99999 }, "exceeds the profile's serving ceiling"},
		{"concurrency over cap", func(b *PlanBinding) { b.MaxConcurrent = 4 }, "§8.8's one-active-workload rule"},
	} {
		b := binding
		c.mutate(&b)
		if _, err := adapter.BuildProfile(b); err == nil || !strings.Contains(err.Error(), c.marker) {
			t.Errorf("%s: refusal must carry %q; got %v", c.name, c.marker, err)
		}
	}
}

// fakeProber — a deterministic HealthProber for the seam tests.
type fakeProber struct {
	healthy bool
	detail  string
}

func (f fakeProber) CheckHealth(ctx context.Context, target ApprovedRuntimeProfile) HealthResult {
	if err := ctx.Err(); err != nil {
		return HealthResult{Healthy: false, Detail: "context: " + err.Error(), CheckedAt: time.Now().UTC()}
	}
	return HealthResult{Healthy: f.healthy, Detail: f.detail, CheckedAt: time.Now().UTC()}
}

// TestAdapterProbe — the seam discipline: a nil prober is a NAMED
// refusal; an injected prober's verdict rides through unchanged.
func TestAdapterProbe(t *testing.T) {
	nilProber, err := NewAdapter(validProfile(), nil)
	if err != nil {
		t.Fatal(err)
	}
	res := nilProber.Probe(context.Background(), ApprovedRuntimeProfile{})
	if res.Healthy || !strings.Contains(res.Detail, "no health prober configured") {
		t.Fatalf("a nil prober must refuse by name: %+v", res)
	}
	healthy, err := NewAdapter(validProfile(), fakeProber{healthy: true, detail: "200"})
	if err != nil {
		t.Fatal(err)
	}
	target := ApprovedRuntimeProfile{HealthEndpointURL: "http://127.0.0.1:30100", HealthPath: "/health"}
	if res := healthy.Probe(context.Background(), target); !res.Healthy || res.CheckedAt.IsZero() {
		t.Fatalf("the injected verdict must ride through: %+v", res)
	}
	// A cancelled context is a probe failure carrying the context error.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := healthy.Probe(cancelledCtx, target); res.Healthy {
		t.Fatalf("a cancelled context must not probe healthy")
	}
}

// TestHTTPHealthProberNoEndpoint — the prober without a bound endpoint
// is the named refusal (the Host Bridge owns endpoint assignment).
func TestHTTPHealthProberNoEndpoint(t *testing.T) {
	prober := NewHTTPHealthProber()
	res := prober.CheckHealth(context.Background(), ApprovedRuntimeProfile{HealthPath: "/health"})
	if res.Healthy || !strings.Contains(res.Detail, "no health endpoint bound") {
		t.Fatalf("no endpoint must refuse by name: %+v", res)
	}
}

// TestHTTPHealthProberRedirectsNotFollowed — a 3xx on the health
// endpoint is the failure it is (the no-redirect discipline).
func TestHTTPHealthProberRedirectsNotFollowed(t *testing.T) {
	// A data URI is not dialable; instead assert the client construction
	// discipline at the source level: the prober's default client sets
	// http.ErrUseLastResponse (prose cannot mask code).
	raw, err := os.ReadFile("health.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "http.ErrUseLastResponse") {
		t.Fatalf("the default health client must carry the no-redirect policy (http.ErrUseLastResponse)")
	}
}

// TestHTTPHealthProberLocalOnly — v19.8.1 (the SK audit's P2-02): the
// prober's local-only transport discipline — a loopback endpoint is
// dialed; a public address, an arbitrary hostname, or a *.lan name is
// the NAMED refusal, never dialed.
func TestHTTPHealthProberLocalOnly(t *testing.T) {
	p := NewHTTPHealthProber()
	target := ApprovedRuntimeProfile{HealthPath: "/health"}

	// A loopback endpoint is dialed (and fails to connect — a probe
	// failure, NOT a transport refusal: the discipline accepted the host).
	target.HealthEndpointURL = "http://127.0.0.1:1"
	res := p.CheckHealth(context.Background(), target)
	if strings.Contains(res.Detail, "NOT local-only") {
		t.Fatalf("loopback must be dialed, not refused by name: %+v", res)
	}
	target.HealthEndpointURL = "http://localhost:1"
	res = p.CheckHealth(context.Background(), target)
	if strings.Contains(res.Detail, "NOT local-only") {
		t.Fatalf("localhost must be dialed, not refused by name: %+v", res)
	}
	// The compute-internal convention is dialed.
	target.HealthEndpointURL = "http://runtime-a.scarlix.internal:1"
	res = p.CheckHealth(context.Background(), target)
	if strings.Contains(res.Detail, "NOT local-only") {
		t.Fatalf("*.scarlix.internal must be dialed, not refused by name: %+v", res)
	}

	// A public address / arbitrary host / LAN name: the NAMED refusal.
	for _, host := range []string{"http://93.184.216.34:80", "http://example.com", "http://router.lan", "http://gpu-box.mynetwork.local:8080"} {
		target.HealthEndpointURL = host
		res := p.CheckHealth(context.Background(), target)
		if res.Healthy || !strings.Contains(res.Detail, "NOT local-only") {
			t.Fatalf("%s must be the named local-only refusal, never dialed: %+v", host, res)
		}
	}
}

// TestLoadRuntimeProfileStrictYAML — v19.8.1 (KPK-19.8.0-P3-06): the
// runtime profile loader refuses UNKNOWN fields and MULTIPLE documents
// by name — operator configuration the scarlix.runtime-profile/v1
// schema never agreed to carry is a named error, never a silent default.
func TestLoadRuntimeProfileStrictYAML(t *testing.T) {
	dir := t.TempDir()
	good := "schema_version: \"scarlix.runtime-profile/v1\"\n" +
		"profile_id: \"infer-ssd-moe-experimental\"\n" +
		"backend_id: \"infer_ssd\"\n" +
		"enabled: false\n" + // disabled = honest documentation, loads
		"experimental: true\n" +
		"image:\n  reference: \"ghcr.io/scarlix/infer-ssd:v0.1@sha256:" + strings.Repeat("aa", 32) + "\"\n" +
		"compatibility:\n  model_manifest: \"deepseek-moe-test\"\n  checkpoint_sha256: \"" + strings.Repeat("cd", 32) + "\"\n  cuda: \"12.8\"\n  driver_minimum: \"570.00\"\n  gpu_compute_capabilities:\n    - \"sm_120\"\n" +
		"execution:\n  strategy: \"ssd-backed-moe\"\n  context_limit: 8192\n  max_concurrent_requests: 1\n" +
		"resources:\n  gpu:\n    mode: \"exclusive\"\n    vram_reserve_bytes: 1073741824\n  host_ram_bytes: 4294967296\n  cpu_threads: 8\n  storage:\n    device_id: \"storage.nvme.0\"\n    model_path: \"/var/lib/scarlix/models/x\"\n    expert_path: \"/var/lib/scarlix/experts/x\"\n    minimum_free_bytes: 68719476736\n    minimum_read_mbps: 2000\n    minimum_random_read_iops: 50000\n    maximum_p95_latency_ms: 1.5\n" +
		"network:\n  network_profile: \"scarlix-compute-internal\"\n  published_ports: []\n" +
		"health:\n  type: \"http\"\n  path: \"/health\"\n  startup_timeout_seconds: 600\n"

	goodPath := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(goodPath, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeProfile(goodPath); err != nil {
		t.Fatalf("the valid profile must load unchanged under strict decoding: %v", err)
	}

	// An unknown TOP-LEVEL field.
	unknownPath := filepath.Join(dir, "unknown.yaml")
	if err := os.WriteFile(unknownPath, []byte(good+"favourite_colour: \"blue\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeProfile(unknownPath); err == nil || !strings.Contains(err.Error(), "YAML parse failed") {
		t.Errorf("an unknown top-level field must be a named parse error: %v", err)
	}

	// An unknown NESTED field.
	nestedPath := filepath.Join(dir, "nested.yaml")
	nested := strings.Replace(good, "    minimum_read_mbps: 2000", "    minimum_read_mbps: 2000\n    elevator_music: true", 1)
	if err := os.WriteFile(nestedPath, []byte(nested), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeProfile(nestedPath); err == nil || !strings.Contains(err.Error(), "YAML parse failed") {
		t.Errorf("an unknown nested field must be a named parse error: %v", err)
	}

	// MULTIPLE documents in one file.
	multiPath := filepath.Join(dir, "multi.yaml")
	if err := os.WriteFile(multiPath, []byte(good+"---\n"+good), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeProfile(multiPath); err == nil || !strings.Contains(err.Error(), "YAML parse failed") {
		t.Errorf("multiple YAML documents must be a named parse error: %v", err)
	}
}

// TestAdapterPackageNeverCallsDocker — the §8.3 charter asserted at
// the source level: no Docker API client, no container launch, no
// image pull exists anywhere in the package (prose cannot mask code).
func TestAdapterPackageNeverCallsDocker(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("package sources not found: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := stripGoComments(string(raw))
		for _, banned := range []string{"docker", "Docker", "containerd", "exec.Command", "os/exec"} {
			if strings.Contains(src, banned) {
				t.Errorf("%s references %q — the adapter must not call Docker directly (roadmap §8.3: it returns a validated profile for the workload/Host Bridge path)", f, banned)
			}
		}
	}
}

// stripGoComments removes //-line and /*-block comments (crude but
// sufficient for the source-level charter scan: prose cannot mask
// code).
func stripGoComments(src string) string {
	var out []byte
	inLine, inBlock, inStr := false, false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				out = append(out, c)
			}
		case inBlock:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i++
			}
		case inStr:
			out = append(out, c)
			if c == '\\' && i+1 < len(src) {
				out = append(out, src[i+1])
				i++
			} else if c == '"' {
				inStr = false
			}
		default:
			if c == '"' {
				inStr = true
				out = append(out, c)
			} else if c == '/' && i+1 < len(src) && src[i+1] == '/' {
				inLine = true
				i++
			} else if c == '/' && i+1 < len(src) && src[i+1] == '*' {
				inBlock = true
				i++
			} else {
				out = append(out, c)
			}
		}
	}
	return string(out)
}
