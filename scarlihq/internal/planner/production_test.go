package planner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ssdProfileYAML renders a standalone-valid runtime profile bound to
// deepseek-moe-test (the ssdProfileFixture shape, on-disk form).
func ssdProfileYAML(enabled string) string {
	return "schema_version: \"scarlix.runtime-profile/v1\"\n" +
		"profile_id: \"infer-ssd-moe-experimental\"\n" +
		"backend_id: \"infer_ssd\"\n" +
		"enabled: " + enabled + "\n" +
		"experimental: true\n" +
		"image:\n  reference: \"ghcr.io/scarlix/infer-ssd:v0.1@sha256:" + strings.Repeat("aa", 32) + "\"\n" +
		"compatibility:\n  model_manifest: \"deepseek-moe-test\"\n  checkpoint_sha256: \"" + strings.Repeat("cd", 32) + "\"\n  cuda: \"12.8\"\n  driver_minimum: \"570.00\"\n  gpu_compute_capabilities:\n    - \"sm_120\"\n" +
		"execution:\n  strategy: \"ssd-backed-moe\"\n  context_limit: 8192\n  max_concurrent_requests: 1\n" +
		"resources:\n  gpu:\n    mode: \"exclusive\"\n    vram_reserve_bytes: 1073741824\n  host_ram_bytes: 4294967296\n  cpu_threads: 8\n  storage:\n    device_id: \"storage.nvme.0\"\n    model_path: \"/var/lib/scarlix/models/x\"\n    expert_path: \"/var/lib/scarlix/experts/x\"\n    minimum_free_bytes: 68719476736\n    minimum_read_mbps: 2000\n    minimum_random_read_iops: 50000\n    maximum_p95_latency_ms: 1.5\n" +
		"network:\n  network_profile: \"scarlix-compute-internal\"\n  published_ports: []\n" +
		"health:\n  type: \"http\"\n  path: \"/health\"\n  startup_timeout_seconds: 600\n"
}

// TestLoadInferSSDProfile — the v19.8.0 seam: unset → not configured;
// a valid ENABLED profile rides the policy; a DISABLED profile is the
// honest not-configured; a malformed/placeholder profile is a NAMED
// error (fail-closed startup).
func TestLoadInferSSDProfile(t *testing.T) {
	dir := t.TempDir()

	// Unset: (nil, nil).
	t.Setenv(InferSSDProfileEnv, "")
	if p, err := LoadInferSSDProfile(); p != nil || err != nil {
		t.Fatalf("unset: got (%v, %v), want (nil, nil)", p, err)
	}

	// Enabled: the profile rides through validated.
	enabledPath := filepath.Join(dir, "enabled.yaml")
	if err := os.WriteFile(enabledPath, []byte(ssdProfileYAML("true")), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(InferSSDProfileEnv, enabledPath)
	p, err := LoadInferSSDProfile()
	if err != nil || p == nil {
		t.Fatalf("enabled: got (%v, %v), want a loaded profile", p, err)
	}
	if !p.Enabled || p.ProfileID != "infer-ssd-moe-experimental" {
		t.Fatalf("enabled profile drift: %+v", p)
	}

	// Disabled: the honest not-configured (§8.8's initial status).
	disabledPath := filepath.Join(dir, "disabled.yaml")
	if err := os.WriteFile(disabledPath, []byte(ssdProfileYAML("false")), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(InferSSDProfileEnv, disabledPath)
	if p, err := LoadInferSSDProfile(); p != nil || err != nil {
		t.Fatalf("disabled: got (%v, %v), want (nil, nil) — documentation never plans", p, err)
	}

	// Malformed (a production claim): a NAMED error.
	claimPath := filepath.Join(dir, "claim.yaml")
	if err := os.WriteFile(claimPath, []byte(strings.Replace(ssdProfileYAML("true"), "experimental: true", "experimental: false", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(InferSSDProfileEnv, claimPath)
	if _, err := LoadInferSSDProfile(); err == nil || !strings.Contains(err.Error(), "SCARLIHQ_INFERSSD_PROFILE") {
		t.Fatalf("a malformed profile must be a named fail-closed error: %v", err)
	}

	// Placeholder values: refused by name (§8.4's loader rule).
	placeholderPath := filepath.Join(dir, "placeholder.yaml")
	placeholderYAML := strings.Replace(ssdProfileYAML("true"),
		"ghcr.io/scarlix/infer-ssd:v0.1@sha256:"+strings.Repeat("aa", 32),
		"registry.example/infer-ssd@sha256:REPLACE_WITH_VERIFIED_DIGEST", 1)
	if err := os.WriteFile(placeholderPath, []byte(placeholderYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(InferSSDProfileEnv, placeholderPath)
	if _, err := LoadInferSSDProfile(); err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("a placeholder-carrying profile must be refused by name: %v", err)
	}

	// Missing file: a named error.
	t.Setenv(InferSSDProfileEnv, filepath.Join(dir, "nope.yaml"))
	if _, err := LoadInferSSDProfile(); err == nil {
		t.Fatalf("a missing profile file must be a named error")
	}
}

// TestProductionPolicyCarriesProfile — ProductionPolicy folds the
// enabled profile into the policy (and stays nil-profiled without one).
func TestProductionPolicyCarriesProfile(t *testing.T) {
	dir := t.TempDir()
	enabledPath := filepath.Join(dir, "enabled.yaml")
	if err := os.WriteFile(enabledPath, []byte(ssdProfileYAML("true")), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(PlannerImagesEnv, "")
	t.Setenv(InferSSDProfileEnv, enabledPath)
	policy, err := ProductionPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy.SSDProfile == nil || policy.SSDProfile.ProfileID != "infer-ssd-moe-experimental" {
		t.Fatalf("the policy must carry the enabled profile: %+v", policy.SSDProfile)
	}
	if policy.PolicyVersion != "planner/v19.8.1" {
		t.Fatalf("policy version: %s", policy.PolicyVersion)
	}
	t.Setenv(InferSSDProfileEnv, "")
	policy2, err := ProductionPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy2.SSDProfile != nil {
		t.Fatalf("without the env the policy stays nil-profiled (§8.8's default)")
	}
}

// TestNewPlannerRefusesDisabledProfileInPolicy — a DISABLED profile
// carried in the policy is a named wiring bug (the seam turns disabled
// into not-configured; documentation never reaches the planning path).
func TestNewPlannerRefusesDisabledProfileInPolicy(t *testing.T) {
	policy := DefaultPlannerPolicy()
	// A STRUCTURALLY VALID but disabled profile (the §8.8 documentation
	// state) — validatePolicy must refuse it BY NAME (the seam turns
	// disabled into not-configured; documentation never plans).
	profile := ssdProfileFixture()
	profile.Enabled = false
	policy.SSDProfile = profile
	_, err := NewPlanner(policy, fixedSnapshots{snap: newPlannerFixture().snap}, newPlannerFixture().models)
	if err == nil || !strings.Contains(err.Error(), "DISABLED") {
		t.Fatalf("a disabled profile in the policy must be refused by name: %v", err)
	}
}
