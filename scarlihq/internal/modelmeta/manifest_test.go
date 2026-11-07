package modelmeta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validManifest — a dense model with every required value known.
func validManifest() Manifest {
	var m Manifest
	m.SchemaVersion = SchemaVersionV2
	m.ID = "qwen3-14b-awq"
	m.Revision = "main-20261101"
	m.Checkpoint.Format = "safetensors"
	m.Checkpoint.Quantization = "awq"
	m.Checkpoint.SHA256 = strings.Repeat("a", 64)
	m.Architecture.Kind = ArchitectureDense
	m.Architecture.TotalParameters = "14B"
	m.Architecture.ActiveParameters = "14B"
	m.Architecture.SharedBytes = 8_500_000_000
	m.Runtime.ContextLimit = 32768
	m.Runtime.SupportedBackends = []string{"sglang", "vllm"}
	return m
}

// validMoEManifest — MoE with full geometry.
func validMoEManifest() Manifest {
	m := validManifest()
	m.ID = "example-moe-model"
	m.Architecture.Kind = ArchitectureMoE
	m.Architecture.TotalParameters = "235B"
	m.Architecture.ActiveParameters = "22B"
	m.Architecture.ExpertCount = 128
	m.Architecture.ExpertsPerToken = 8
	m.Architecture.ExpertBytes = 1_500_000_000
	m.Architecture.SharedBytes = 2_000_000_000
	m.Runtime.SupportedBackends = []string{"sglang", "infer_ssd"}
	return m
}

func TestValidateManifestValid(t *testing.T) {
	for name, m := range map[string]Manifest{
		"dense": validManifest(),
		"moe":   validMoEManifest(),
	} {
		if reasons := ValidateManifest(&m); len(reasons) > 0 {
			t.Errorf("%s: unexpected reasons: %v", name, reasons)
		}
	}
}

func TestValidateManifestFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Manifest)
		want   string // a substring the named reason must carry
	}{
		{"future schema", func(m *Manifest) { m.SchemaVersion = "scarlix.model/v3" }, "scarlix.model/v2"},
		{"empty id", func(m *Manifest) { m.ID = "" }, "id is required"},
		{"empty revision", func(m *Manifest) { m.Revision = " " }, "revision is required"},
		{"no checkpoint format", func(m *Manifest) { m.Checkpoint.Format = "" }, "checkpoint.format is required"},
		{"short sha", func(m *Manifest) { m.Checkpoint.SHA256 = "abc" }, "64-hex-char"},
		{"empty sha", func(m *Manifest) { m.Checkpoint.SHA256 = "" }, "64-hex-char"},
		{"bad kind", func(m *Manifest) { m.Architecture.Kind = "hybrid" }, "dense|moe|other"},
		{"moe no experts", func(m *Manifest) {
			m.Architecture.Kind = ArchitectureMoE
			m.Architecture.ExpertCount = 0
		}, "expert_count > 0"},
		{"moe no topk", func(m *Manifest) {
			m.Architecture.Kind = ArchitectureMoE
			m.Architecture.ExpertsPerToken = 0
		}, "experts_per_token > 0"},
		{"ctx limit zero", func(m *Manifest) { m.Runtime.ContextLimit = 0 }, "context_limit"},
		{"no backends", func(m *Manifest) { m.Runtime.SupportedBackends = nil }, "supported_backends"},
		{"empty backends", func(m *Manifest) { m.Runtime.SupportedBackends = []string{} }, "supported_backends"},
		{"bad backend slug", func(m *Manifest) { m.Runtime.SupportedBackends = []string{"SGLang"} }, "lowercase backend slug"},
		{"dup backends", func(m *Manifest) { m.Runtime.SupportedBackends = []string{"sglang", "sglang"} }, "duplicated"},
		{"enabled pack no sha", func(m *Manifest) {
			m.Artifacts.ExpertPack.Enabled = true
			m.Artifacts.ExpertPack.Version = "1"
			m.Artifacts.ExpertPack.CompatibleBackend = "sglang"
		}, "64-hex-char fingerprint"},
		{"enabled pack no backend", func(m *Manifest) {
			m.Artifacts.ExpertPack.Enabled = true
			m.Artifacts.ExpertPack.SHA256 = strings.Repeat("b", 64)
			m.Artifacts.ExpertPack.Version = "1"
		}, "compatible_backend"},
		{"enabled pack no version", func(m *Manifest) {
			m.Artifacts.ExpertPack.Enabled = true
			m.Artifacts.ExpertPack.SHA256 = strings.Repeat("b", 64)
			m.Artifacts.ExpertPack.CompatibleBackend = "sglang"
		}, "version"},
	}
	for _, tc := range cases {
		m := validManifest()
		tc.mutate(&m)
		reasons := ValidateManifest(&m)
		if len(reasons) == 0 {
			t.Errorf("%s: expected a named reason, got none", tc.name)
			continue
		}
		if !strings.Contains(strings.Join(reasons, "; "), tc.want) {
			t.Errorf("%s: reasons %v do not carry %q", tc.name, reasons, tc.want)
		}
	}
}

func TestPlacementEligible(t *testing.T) {
	// Valid dense with shared bytes → eligible.
	d := validManifest()
	if pe := PlacementEligible(&d); !pe.Eligible || len(pe.Reasons) != 0 {
		t.Errorf("dense eligible=%v reasons=%v", pe.Eligible, pe.Reasons)
	}

	// Valid MoE with full geometry → eligible.
	moe := validMoEManifest()
	if pe := PlacementEligible(&moe); !pe.Eligible {
		t.Errorf("moe eligible=%v reasons=%v (full geometry must be eligible)", pe.Eligible, pe.Reasons)
	}

	// MoE with unknown expert_bytes → INELIGIBLE with the named unknown
	// (roadmap §5.5: required unknown values block automatic placement).
	moeNoBytes := validMoEManifest()
	moeNoBytes.Architecture.ExpertBytes = 0
	pe := PlacementEligible(&moeNoBytes)
	if pe.Eligible {
		t.Error("moe with unknown expert_bytes must be placement-ineligible")
	}
	if !strings.Contains(strings.Join(pe.Reasons, "; "), "expert_bytes") {
		t.Errorf("reasons %v do not name expert_bytes", pe.Reasons)
	}

	// MoE with unknown shared_bytes → ineligible.
	moeNoShared := validMoEManifest()
	moeNoShared.Architecture.SharedBytes = 0
	if pe := PlacementEligible(&moeNoShared); pe.Eligible {
		t.Error("moe with unknown shared_bytes must be placement-ineligible")
	}

	// Invalid manifest → ineligible with the schema reason first.
	bad := validManifest()
	bad.Checkpoint.SHA256 = "nope"
	pe = PlacementEligible(&bad)
	if pe.Eligible {
		t.Error("invalid manifest must be placement-ineligible")
	}
	if !strings.Contains(strings.Join(pe.Reasons, "; "), "schema validation") {
		t.Errorf("reasons %v do not carry the schema-validation marker", pe.Reasons)
	}
}

func TestKVCacheUnknownIsAllowed(t *testing.T) {
	// bytes_per_token_estimate = 0 (runtime-defined) is VALID and does
	// not block placement (runtimes size the KV cache; Phase 2's KV
	// budget treats it as its own named unknown).
	m := validManifest()
	if m.Runtime.KVCache.BytesPerTokenEstimate != 0 {
		t.Fatal("fixture must start with unknown KV bytes")
	}
	if pe := PlacementEligible(&m); !pe.Eligible {
		t.Errorf("unknown KV bytes/token must not block placement: %v", pe.Reasons)
	}
}

func TestExecutionConversion(t *testing.T) {
	m := validMoEManifest()
	m.Artifacts.ExpertPack.Enabled = true
	m.Artifacts.ExpertPack.SHA256 = strings.Repeat("c", 64)
	m.Artifacts.ExpertPack.CompatibleBackend = "sglang"
	m.Artifacts.ExpertPack.Version = "v1.0"
	m.Artifacts.ExpertPack.Format = "sglang-expert-pack"
	// Uppercase SHA must normalize to lowercase.
	m.Checkpoint.SHA256 = strings.ToUpper(strings.Repeat("a", 64))

	e := m.Execution("/etc/scarlix/models.d/example-moe-model.manifest.yaml")
	if e.ID != m.ID || e.Revision != m.Revision {
		t.Errorf("id/revision %q/%q", e.ID, e.Revision)
	}
	if e.CheckpointSHA256 != strings.Repeat("a", 64) {
		t.Errorf("checkpoint sha not lowercased: %q", e.CheckpointSHA256)
	}
	if e.Architecture != ArchitectureMoE || e.ExpertCount != 128 || e.ExpertsPerToken != 8 {
		t.Errorf("moe geometry lost: %+v", e)
	}
	if e.SupportedBackends == nil || len(e.SupportedBackends) != 2 {
		t.Errorf("supported backends %v", e.SupportedBackends)
	}
	if e.ExpertPack == nil || !e.ExpertPack.Enabled || e.ExpertPack.CompatibleBackend != "sglang" {
		t.Errorf("expert pack %+v", e.ExpertPack)
	}
	if e.ManifestPath == "" {
		t.Errorf("manifest path (provenance) empty")
	}
	// Total vs active are NOT interchangeable — both carried as declared.
	if e.TotalParameters == e.ActiveParameters {
		t.Errorf("total/active must stay distinct labels (got both %q)", e.TotalParameters)
	}
}

// ---- directory loading -------------------------------------------------------

func writeManifest(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const denseYAML = `schema_version: "scarlix.model/v2"
id: "qwen3-14b-awq"
revision: "main-20261101"
checkpoint:
  format: "safetensors"
  quantization: "awq"
  sha256: "` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `"
architecture:
  kind: "dense"
  total_parameters: "14B"
  active_parameters: "14B"
  shared_bytes: 8500000000
runtime:
  context_limit: 32768
  supported_backends: ["sglang", "vllm"]
artifacts:
  expert_pack:
    enabled: false
storage:
  model_path: "/models/Qwen3-14B-AWQ"
`

const badSchemaYAML = `schema_version: "scarlix.model/v9"
id: "future-thing"
revision: "r"
checkpoint:
  format: "safetensors"
  sha256: "` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `"
architecture:
  kind: "dense"
runtime:
  context_limit: 8192
  supported_backends: ["sglang"]
`

func TestLoadManifests(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "qwen3-14b-awq.manifest.yaml", denseYAML)
	writeManifest(t, dir, "future-thing.manifest.yaml", badSchemaYAML)
	writeManifest(t, dir, "notes.txt", "not a manifest")

	res := LoadManifests(dir)
	if res.DirError != "" {
		t.Fatalf("dir error: %s", res.DirError)
	}
	if len(res.Manifests) != 1 {
		t.Fatalf("manifests=%d, want 1 (notes.txt skipped, bad schema invalid)", len(res.Manifests))
	}
	if res.Manifests[0].ID != "qwen3-14b-awq" {
		t.Errorf("loaded id=%q", res.Manifests[0].ID)
	}
	if len(res.Invalid) != 1 {
		t.Fatalf("invalid=%d, want 1", len(res.Invalid))
	}
	if !strings.Contains(strings.Join(res.Invalid[0].Reasons, "; "), "scarlix.model/v2") {
		t.Errorf("invalid reasons %v must name the schema refusal", res.Invalid[0].Reasons)
	}
}

func TestLoadManifestsUnreadableDir(t *testing.T) {
	res := LoadManifests(filepath.Join(t.TempDir(), "does-not-exist"))
	if res.DirError == "" {
		t.Error("DirError must name the unreadable directory")
	}
	if len(res.Manifests) != 0 || len(res.Invalid) != 0 {
		t.Error("nothing may be loaded from an unreadable directory")
	}
}

func TestResolveManifestsDirEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCARLIX_MODELS_D", dir)
	if got := ResolveManifestsDir(); got != dir {
		t.Errorf("resolve=%q, want env override %q", got, dir)
	}
	// An override that does not exist: honest absence (empty).
	t.Setenv("SCARLIX_MODELS_D", filepath.Join(dir, "nope"))
	if got := ResolveManifestsDir(); got != "" {
		t.Errorf("resolve=%q, want empty for a missing override", got)
	}
}
