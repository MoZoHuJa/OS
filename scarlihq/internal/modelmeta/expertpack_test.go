package modelmeta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validPackManifest builds a standalone-valid Expert Pack manifest bound
// to a MoE model manifest that DECLARES the same pack (the golden
// fixture — every test mutates one fact and asserts the NAMED refusal).
func validPackManifest() *ExpertPackManifest {
	p := &ExpertPackManifest{
		SchemaVersion: SchemaVersionArtifact,
		ArtifactID:    "deepseek-moe-sglang-expert-pack",
		ArtifactType:  ArtifactTypeSGLangExpertPack,
		BackendID:     BackendIDSGLang,
	}
	p.Model.ID = "deepseek-moe-test"
	p.Model.Revision = "rev-1"
	p.Model.CheckpointSHA256 = strings.Repeat("cd", 32)
	p.Pack.Format = "sglang-expert-pack"
	p.Pack.Version = "2026.11"
	p.Pack.SHA256 = strings.Repeat("ef", 32)
	p.Pack.TotalBytes = 40 << 30
	p.Compatibility.SGLangVersion = "v0.4.6"
	p.Compatibility.ContainerDigest = "ghcr.io/scarlix/sglang:v0.4.6@sha256:" + strings.Repeat("11", 32)
	p.Compatibility.CUDAVersion = "12.8"
	p.Compatibility.GPUComputeCapabilities = []string{"sm_120"}
	return p
}

// packDeclaringModel builds the model manifest that declares the pack
// (mirrors the artifact manifest's own values — the agreement the
// binding requires).
func packDeclaringModel() *Manifest {
	m := &Manifest{
		SchemaVersion: SchemaVersionV2,
		ID:            "deepseek-moe-test",
		Revision:      "rev-1",
	}
	m.Checkpoint.Format = "safetensors"
	m.Checkpoint.SHA256 = strings.Repeat("cd", 32)
	m.Architecture.Kind = ArchitectureMoE
	m.Architecture.ExpertCount = 8
	m.Architecture.ExpertsPerToken = 2
	m.Architecture.ExpertBytes = 512 << 20
	m.Architecture.SharedBytes = 2 << 30
	m.Runtime.ContextLimit = 32768
	m.Runtime.SupportedBackends = []string{"sglang", "infer_ssd"}
	m.Artifacts.ExpertPack.Enabled = true
	m.Artifacts.ExpertPack.Format = "sglang-expert-pack"
	m.Artifacts.ExpertPack.Version = "2026.11"
	m.Artifacts.ExpertPack.SHA256 = strings.Repeat("ef", 32)
	m.Artifacts.ExpertPack.CompatibleBackend = BackendIDSGLang
	return m
}

// TestExpertPackValidationTruthTable — every rule of the §8.5 contract
// refuses BY NAME (one mutated fact per case; the named marker must
// appear in the reasons).
func TestExpertPackValidationTruthTable(t *testing.T) {
	type tc struct {
		name   string
		mutate func(p *ExpertPackManifest)
		marker string
	}
	cases := []tc{
		{"wrong schema version", func(p *ExpertPackManifest) { p.SchemaVersion = "scarlix.artifact/v2" }, "is not \"scarlix.artifact/v1\""},
		{"wrong artifact type", func(p *ExpertPackManifest) { p.ArtifactType = "vllm_pack" }, "the closed v19.8.0 artifact-type set"},
		{"cross-engine backend (the must-not-do)", func(p *ExpertPackManifest) { p.BackendID = "vllm" }, "cross-engine pack compatibility is not claimed"},
		{"cross-engine backend (infer_ssd)", func(p *ExpertPackManifest) { p.BackendID = "infer_ssd" }, "cross-engine pack compatibility is not claimed"},
		{"empty artifact id", func(p *ExpertPackManifest) { p.ArtifactID = "" }, "artifact_id is required"},
		{"unsafe artifact id", func(p *ExpertPackManifest) { p.ArtifactID = "../escape" }, "not a safe closed identifier"},
		{"empty model id", func(p *ExpertPackManifest) { p.Model.ID = "" }, "model.id is required"},
		{"empty model revision", func(p *ExpertPackManifest) { p.Model.Revision = "" }, "model.revision is required"},
		{"short checkpoint hash", func(p *ExpertPackManifest) { p.Model.CheckpointSHA256 = "abcd" }, "not a 64-hex-char SHA-256 fingerprint"},
		{"empty pack format", func(p *ExpertPackManifest) { p.Pack.Format = "" }, "pack.format is required"},
		{"empty pack version", func(p *ExpertPackManifest) { p.Pack.Version = "" }, "pack.version is required"},
		{"short pack hash", func(p *ExpertPackManifest) { p.Pack.SHA256 = "ff" }, "not a 64-hex-char SHA-256 fingerprint"},
		{"zero total bytes", func(p *ExpertPackManifest) { p.Pack.TotalBytes = 0 }, "pack.total_bytes must be > 0"},
		{"empty sglang version", func(p *ExpertPackManifest) { p.Compatibility.SGLangVersion = "" }, "compatibility.sglang_version is required"},
		{"unpinned container digest", func(p *ExpertPackManifest) { p.Compatibility.ContainerDigest = "ghcr.io/scarlix/sglang:v0.4.6" }, "carries no @sha256: digest"},
		{"empty cuda version", func(p *ExpertPackManifest) { p.Compatibility.CUDAVersion = "" }, "compatibility.cuda_version is required"},
		{"empty capability list", func(p *ExpertPackManifest) { p.Compatibility.GPUComputeCapabilities = nil }, "gpu_compute_capabilities is required non-empty"},
		{"empty capability entry", func(p *ExpertPackManifest) { p.Compatibility.GPUComputeCapabilities = []string{"sm_120", ""} }, "a placeholder slot is not a capability"},
		{"placeholder checkpoint hash", func(p *ExpertPackManifest) { p.Model.CheckpointSHA256 = "REPLACE_WITH_VERIFIED_CHECKPOINT_HASH" }, "carries a placeholder value"},
		{"placeholder digest", func(p *ExpertPackManifest) { p.Compatibility.ContainerDigest = "ghcr.io/x/y@sha256:VERIFIED_DIGEST" }, "carries a placeholder value"},
		{"placeholder capability", func(p *ExpertPackManifest) { p.Compatibility.GPUComputeCapabilities = []string{"VERIFIED_CAPABILITY"} }, "carries a placeholder value"},
		{"placeholder sglang version", func(p *ExpertPackManifest) { p.Compatibility.SGLangVersion = "VERIFIED_VERSION" }, "carries a placeholder value"},
	}
	for _, c := range cases {
		p := validPackManifest()
		c.mutate(p)
		reasons := ValidateExpertPackManifest(p)
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
	// The golden fixture itself is standalone-valid.
	if reasons := ValidateExpertPackManifest(validPackManifest()); len(reasons) != 0 {
		t.Errorf("the golden pack manifest must validate cleanly; got %v", reasons)
	}
}

// TestExpertPackBindTruthTable — the checkpoint binding (§8.5: "the
// manifest must bind the pack to the exact checkpoint. Reject
// mismatches."), one mutated fact per case.
func TestExpertPackBindTruthTable(t *testing.T) {
	packs := func() []ExpertPackManifest { return []ExpertPackManifest{*validPackManifest()} }
	models := func() []Manifest { return []Manifest{*packDeclaringModel()} }

	// Golden: the binding succeeds.
	bound := BindExpertPacks(packs(), models())
	if len(bound) != 1 || !bound[0].Bound || bound[0].BoundModelID != "deepseek-moe-test" {
		t.Fatalf("golden binding failed: %+v", bound)
	}
	if len(bound[0].BindingReasons) != 0 {
		t.Fatalf("golden binding must carry no reasons: %v", bound[0].BindingReasons)
	}

	type tc struct {
		name   string
		mutate func(p *ExpertPackManifest, m *Manifest)
		marker string
	}
	cases := []tc{
		{"orphan pack (no model manifest)", func(p *ExpertPackManifest, m *Manifest) { p.Model.ID = "unknown-model" }, "an orphan pack is refused as a binding"},
		{"revision mismatch", func(p *ExpertPackManifest, m *Manifest) { p.Model.Revision = "rev-2" }, "does not match the manifest revision"},
		{"checkpoint mismatch", func(p *ExpertPackManifest, m *Manifest) { p.Model.CheckpointSHA256 = strings.Repeat("ce", 32) }, "does not match the manifest checkpoint fingerprint"},
		{"model declares no pack", func(p *ExpertPackManifest, m *Manifest) { m.Artifacts.ExpertPack.Enabled = false }, "does not declare an ENABLED artifacts.expert_pack"},
		{"model declares another engine", func(p *ExpertPackManifest, m *Manifest) { m.Artifacts.ExpertPack.CompatibleBackend = "vllm" }, "must agree on the ONE validated engine"},
		{"pack sha disagreement", func(p *ExpertPackManifest, m *Manifest) { m.Artifacts.ExpertPack.SHA256 = strings.Repeat("ee", 32) }, "is a collision, not a merge"},
		{"pack format disagreement", func(p *ExpertPackManifest, m *Manifest) { m.Artifacts.ExpertPack.Format = "other-format" }, "does not match the artifact manifest's pack.format"},
		{"pack version disagreement", func(p *ExpertPackManifest, m *Manifest) { m.Artifacts.ExpertPack.Version = "1999.01" }, "does not match the artifact manifest's pack.version"},
	}
	for _, c := range cases {
		ps, ms := packs(), models()
		c.mutate(&ps[0], &ms[0])
		bound := BindExpertPacks(ps, ms)
		if len(bound) != 1 {
			t.Fatalf("%s: expected one bound verdict, got %d", c.name, len(bound))
		}
		if bound[0].Bound {
			t.Errorf("%s: the binding must be refused", c.name)
			continue
		}
		found := false
		for _, r := range bound[0].BindingReasons {
			if strings.Contains(r, c.marker) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: the binding refusal must carry the marker %q; got %v", c.name, c.marker, bound[0].BindingReasons)
		}
	}
}

// TestLoadExpertPacks — the loader genre: *.pack.yaml files only, one
// broken file degrades only itself, duplicate artifact ids are rejected
// as a GROUP (no file-order winner).
func TestLoadExpertPacks(t *testing.T) {
	dir := t.TempDir()
	goldenYAML := "schema_version: \"scarlix.artifact/v1\"\n" +
		"artifact_id: \"pack-a\"\n" +
		"artifact_type: \"sglang_expert_pack\"\n" +
		"backend_id: \"sglang\"\n" +
		"model:\n  id: \"deepseek-moe-test\"\n  revision: \"rev-1\"\n  checkpoint_sha256: \"" + strings.Repeat("cd", 32) + "\"\n" +
		"pack:\n  format: \"sglang-expert-pack\"\n  version: \"2026.11\"\n  sha256: \"" + strings.Repeat("ef", 32) + "\"\n  total_bytes: 42949672960\n" +
		"compatibility:\n  sglang_version: \"v0.4.6\"\n  container_digest: \"ghcr.io/x/y@sha256:" + strings.Repeat("11", 32) + "\"\n  cuda_version: \"12.8\"\n  gpu_compute_capabilities:\n    - \"sm_120\"\n"
	if err := os.WriteFile(filepath.Join(dir, "a.pack.yaml"), []byte(goldenYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	// A duplicate artifact id in a second file: BOTH are invalid.
	if err := os.WriteFile(filepath.Join(dir, "b.pack.yaml"), []byte(goldenYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	// A broken YAML file: only itself degrades.
	if err := os.WriteFile(filepath.Join(dir, "c.pack.yaml"), []byte(":::not yaml["), 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-pack file: ignored.
	if err := os.WriteFile(filepath.Join(dir, "d.manifest.yaml"), []byte("id: x"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := LoadExpertPacks(dir)
	if len(res.Packs) != 0 {
		t.Errorf("duplicate ids must invalidate the whole group; got %d valid packs", len(res.Packs))
	}
	if len(res.Invalid) != 3 {
		t.Errorf("expected 3 invalid entries (two duplicates + one parse failure), got %d", len(res.Invalid))
	}
	dupFound := false
	parseFound := false
	for _, inv := range res.Invalid {
		if strings.Contains(strings.Join(inv.Reasons, ";"), "duplicate artifact_id") {
			dupFound = true
		}
		if strings.Contains(strings.Join(inv.Reasons, ";"), "YAML parse failed") {
			parseFound = true
		}
	}
	if !dupFound {
		t.Errorf("the duplicate group rejection must be named")
	}
	if !parseFound {
		t.Errorf("the parse failure must be named")
	}

	// Without the duplicate, the golden pack loads.
	if err := os.Remove(filepath.Join(dir, "b.pack.yaml")); err != nil {
		t.Fatal(err)
	}
	res2 := LoadExpertPacks(dir)
	if len(res2.Packs) != 1 || res2.Packs[0].ArtifactID != "pack-a" {
		t.Fatalf("the golden pack must load alone; got %+v", res2.Packs)
	}
	if res2.Packs[0].SourcePath == "" {
		t.Errorf("SourcePath provenance must be recorded")
	}

	// An empty dir is a valid absence, not an error.
	empty := t.TempDir()
	res3 := LoadExpertPacks(empty)
	if len(res3.Packs) != 0 || res3.DirError != "" {
		t.Errorf("an empty pack dir is a valid absence; got DirError=%q", res3.DirError)
	}
	// A missing dir is DirError (nothing was loaded).
	res4 := LoadExpertPacks(filepath.Join(dir, "nope"))
	if res4.DirError == "" || len(res4.Packs) != 0 {
		t.Errorf("a missing dir must be a named DirError with zero packs")
	}
}

// TestResolveExpertPacksDir — the explicit env override wins; the
// default rides the resolved manifests dir; nonexistent candidates are
// honest absences.
func TestResolveExpertPacksDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCARLIX_EXPERT_PACKS_D", dir)
	if got := ResolveExpertPacksDir("/elsewhere"); got != dir {
		t.Errorf("explicit override: got %q, want %q", got, dir)
	}
	t.Setenv("SCARLIX_EXPERT_PACKS_D", filepath.Join(dir, "missing"))
	if got := ResolveExpertPacksDir(dir); got != "" {
		t.Errorf("a nonexistent override is an honest absence: got %q", got)
	}
	t.Setenv("SCARLIX_EXPERT_PACKS_D", "")
	if got := ResolveExpertPacksDir(dir); got != dir {
		t.Errorf("default: the manifests dir serves packs; got %q", got)
	}
	if got := ResolveExpertPacksDir(""); got != "" {
		t.Errorf("no manifests dir → no packs dir; got %q", got)
	}
}
