// Package modelmeta — the SGLang Expert Pack ARTIFACT manifest
// (v19.8.0, Compute Fabric Phase 4, roadmap §8.5 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md).
//
// CHARTER (the §8.5 contract, fail-closed at every rule):
//
//   - The pack manifest is a SGLang-SPECIFIC artifact record
//     (schema "scarlix.artifact/v1", artifact_type "sglang_expert_pack",
//     backend_id "sglang"). Roadmap §8.1: the Expert Pack is an artifact/
//     capability mode of SGLang, NOT a fifth runtime provider — and this
//     release's must-not-do is honored BY THE VALIDATION: a pack manifest
//     declaring ANY backend other than sglang is refused BY NAME
//     ("cross-engine pack compatibility is not claimed" — §8.5's own
//     closing rule), never warned about and loaded anyway.
//   - The manifest BINDS to the EXACT checkpoint (§8.5: "The manifest
//     must bind the pack to the exact checkpoint. Reject mismatches"):
//     model.id + model.revision + model.checkpoint_sha256 must match a
//     loaded scarlix.model/v2 manifest EXACTLY, and the model manifest's
//     own artifacts.expert_pack declaration must AGREE (enabled, the
//     same compatible_backend, the same pack sha256/format/version) —
//     two files disagreeing about one artifact is a collision, not a
//     merge.
//   - PLACEHOLDER VALUES ARE REFUSED (§8.2/§8.4's rule, applied to the
//     artifact record): "REPLACE_WITH..." and "VERIFIED_..." are the
//     roadmap's own placeholder markers — a manifest carrying one is
//     invalid with a named reason. An unverified compatibility fact is
//     an UNKNOWN, and an unknown fact never becomes a capability claim.
//   - The digests are DECLARATIONS, not verifications (the
//     KPK-19.7.0-P2-04 discipline carried into the artifact tier): the
//     JSON keys say declared_sha256; consuming a pack for EXECUTION
//     requires the pack-byte verification machinery + the §8.7
//     target-host acceptance matrix — named future work, honestly
//     labeled. The v19.8.0 planner never schedules from this manifest;
//     it exists to make the operator's pack declaration COMPLETE,
//     CHECKABLE, and collision-proof.
package modelmeta

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SchemaVersionArtifact identifies the v19.8.0 artifact manifest schema
// (roadmap §8.5's own schema_version value).
const SchemaVersionArtifact = "scarlix.artifact/v1"

// BackendIDSGLang — the ONE engine an SGLang Expert Pack is validated
// for, as the artifact tier's own closed constant (modelmeta is a leaf
// package and deliberately does not import the airuntime capability
// tier; the two enumerations are kept honest against each other by the
// v19.8.0 smoke check and the registry's SupportsExpertPack row).
const BackendIDSGLang = "sglang"

// ArtifactTypeSGLangExpertPack — the CLOSED v19.8.0 artifact type: the
// SGLang Expert Pack. The set is deliberately one member; a future
// artifact type is a future schema decision, not a silent extension.
const ArtifactTypeSGLangExpertPack = "sglang_expert_pack"

// ExpertPackManifest is the on-disk YAML shape of one SGLang Expert Pack
// artifact (roadmap §8.5, field-for-field). Required fields are enforced
// by ValidateExpertPackManifest; the JSON provenance distinguishes the
// DECLARED pack fingerprint (declared_sha256) from any future VERIFIED
// bytes — the same discipline the model manifest carries.
type ExpertPackManifest struct {
	SchemaVersion string `yaml:"schema_version" json:"schema_version"`
	// ArtifactID — the pack artifact's identity (a closed-charset slug;
	// join keys carry the same safety rules as model ids).
	ArtifactID string `yaml:"artifact_id" json:"artifact_id"`
	// ArtifactType — must be "sglang_expert_pack" (the closed set).
	ArtifactType string `yaml:"artifact_type" json:"artifact_type"`
	// BackendID — must be "sglang": the ONE engine an Expert Pack is
	// validated for. Cross-engine compatibility is never claimed — a
	// different backend here is a NAMED refusal (this release's
	// must-not-do, enforced at the artifact tier).
	BackendID string `yaml:"backend_id" json:"backend_id"`
	// Model — the EXACT checkpoint binding (id + revision + the
	// checkpoint fingerprint the pack was built against).
	Model struct {
		ID               string `yaml:"id" json:"id"`
		Revision         string `yaml:"revision" json:"revision"`
		CheckpointSHA256 string `yaml:"checkpoint_sha256" json:"checkpoint_sha256"`
	} `yaml:"model" json:"model"`
	// Pack — the artifact's own identity (format + version + the
	// DECLARED fingerprint + the storage size).
	Pack struct {
		Format     string `yaml:"format" json:"format"`
		Version    string `yaml:"version" json:"version"`
		SHA256     string `yaml:"sha256" json:"declared_sha256"`
		TotalBytes uint64 `yaml:"total_bytes" json:"total_bytes"`
	} `yaml:"pack" json:"pack"`
	// Compatibility — §8.2's evidence dimensions the pack must carry
	// COMPLETE (unknown facts refuse the manifest, they never mark it
	// quietly usable).
	Compatibility struct {
		SGLangVersion          string   `yaml:"sglang_version" json:"sglang_version"`
		ContainerDigest        string   `yaml:"container_digest" json:"container_digest"`
		CUDAVersion            string   `yaml:"cuda_version" json:"cuda_version"`
		GPUComputeCapabilities []string `yaml:"gpu_compute_capabilities" json:"gpu_compute_capabilities"`
	} `yaml:"compatibility" json:"compatibility"`
	// SourcePath — where this manifest was loaded from (provenance).
	SourcePath string `yaml:"-" json:"source_path,omitempty"`
}

// InvalidExpertPack is one refused pack manifest: the path + the named
// reasons (the LoadManifests genre — one broken file degrades only
// itself).
type InvalidExpertPack struct {
	Path    string
	Reasons []string
}

// LoadedExpertPacks is the full result of a pack directory load: the
// standalone-valid manifests (sorted by artifact id) + the invalid
// entries + the source provenance. Standalone validity does NOT include
// the checkpoint binding — that needs the model manifests and lives in
// BindExpertPacks (two honest stages: shape first, binding second).
type LoadedExpertPacks struct {
	Packs   []ExpertPackManifest
	Invalid []InvalidExpertPack
	// Dir — the directory that was read (provenance).
	Dir string
	// DirError — non-empty when the directory itself was unreadable
	// (then Packs and Invalid are both empty: NOTHING was loaded).
	DirError string
}

// ResolveExpertPacksDir picks the pack manifest directory: explicit
// SCARLIX_EXPERT_PACKS_D → the resolved MODELS directory (packs live
// beside their model manifests as *.pack.yaml). Empty string when no
// candidate exists (a valid absence: no packs declared).
func ResolveExpertPacksDir(manifestsDir string) string {
	if d := os.Getenv("SCARLIX_EXPERT_PACKS_D"); d != "" {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			return d
		}
		return "" // explicit override that does not exist: honest absence
	}
	if manifestsDir == "" {
		return ""
	}
	if info, err := os.Stat(manifestsDir); err == nil && info.IsDir() {
		return manifestsDir
	}
	return ""
}

// LoadExpertPacks reads every *.pack.yaml in dir (the LoadManifests
// genre: files are read, parsed, and validated independently — one
// broken pack degrades ONLY itself; DUPLICATE artifact ids are rejected
// as a GROUP — the KPK-19.7.0-P2-04 collision discipline, no file-order
// winner).
func LoadExpertPacks(dir string) LoadedExpertPacks {
	res := LoadedExpertPacks{Dir: dir}
	if dir == "" {
		res.DirError = "no expert pack directory resolved"
		return res
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		res.DirError = err.Error()
		return res
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".pack.yaml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // reproducible order

	type loaded struct {
		path string
		p    ExpertPackManifest
	}
	var valid []loaded
	for _, name := range names {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			res.Invalid = append(res.Invalid, InvalidExpertPack{Path: path, Reasons: []string{fmt.Sprintf("read failed: %v", err)}})
			continue
		}
		var p ExpertPackManifest
		// v19.8.1 (KPK-19.8.0-P3-06): strict single-document decode —
		// unknown fields and trailing documents are named errors, never
		// silently-accepted operator configuration.
		if err := DecodeSingleStrictYAML(raw, &p); err != nil {
			res.Invalid = append(res.Invalid, InvalidExpertPack{Path: path, Reasons: []string{fmt.Sprintf("YAML parse failed: %v", err)}})
			continue
		}
		p.SourcePath = path
		if reasons := ValidateExpertPackManifest(&p); len(reasons) > 0 {
			res.Invalid = append(res.Invalid, InvalidExpertPack{Path: path, Reasons: reasons})
			continue
		}
		valid = append(valid, loaded{path: path, p: p})
	}

	// Duplicate artifact-id group rejection (the KPK-19.7.0-P2-04
	// discipline, carried to the artifact tier): an id claimed by more
	// than one file invalidates ALL of them — no winner is chosen.
	byIDPaths := make(map[string][]string)
	for _, v := range valid {
		byIDPaths[v.p.ArtifactID] = append(byIDPaths[v.p.ArtifactID], v.path)
	}
	for _, v := range valid {
		if paths := byIDPaths[v.p.ArtifactID]; len(paths) > 1 {
			res.Invalid = append(res.Invalid, InvalidExpertPack{
				Path: v.path,
				Reasons: []string{fmt.Sprintf(
					"duplicate artifact_id %q — declared in %v; ALL conflicting pack manifests are invalid, no winner is chosen",
					v.p.ArtifactID, paths)},
			})
			continue
		}
		res.Packs = append(res.Packs, v.p)
	}
	return res
}

// isPlaceholderValue reports whether s carries the roadmap's own
// placeholder markers ("REPLACE_WITH..." and "VERIFIED_..." — §8.4's
// example values, which may appear EMBEDDED, e.g. after an
// "@sha256:" pin). A placeholder in a REQUIRED field is an UNKNOWN
// compatibility fact, and §8.2's rule is absolute: it never becomes a
// capability claim.
func isPlaceholderValue(s string) bool {
	return strings.Contains(s, "REPLACE_WITH") || strings.Contains(s, "VERIFIED_")
}

// placeholderScan returns the named reasons for every placeholder found
// in the manifest's required string fields (deterministic field order).
func placeholderScan(p *ExpertPackManifest) []string {
	var reasons []string
	check := func(field, value string) {
		if isPlaceholderValue(value) {
			reasons = append(reasons, fmt.Sprintf("%s %q carries a placeholder value — an unknown compatibility fact never becomes a capability claim (roadmap §8.2/§8.4: placeholders are refused, not warned about)", field, value))
		}
	}
	check("model.id", p.Model.ID)
	check("model.revision", p.Model.Revision)
	check("model.checkpoint_sha256", p.Model.CheckpointSHA256)
	check("pack.format", p.Pack.Format)
	check("pack.version", p.Pack.Version)
	check("pack.sha256", p.Pack.SHA256)
	check("compatibility.sglang_version", p.Compatibility.SGLangVersion)
	check("compatibility.container_digest", p.Compatibility.ContainerDigest)
	check("compatibility.cuda_version", p.Compatibility.CUDAVersion)
	for i, c := range p.Compatibility.GPUComputeCapabilities {
		check(fmt.Sprintf("compatibility.gpu_compute_capabilities[%d]", i), c)
	}
	return reasons
}

// ValidateExpertPackManifest returns the named reasons a pack manifest
// is STANDALONE-invalid (empty = standalone-valid; the checkpoint
// binding is BindExpertPacks' separate answer). The rules, in order:
//
//   - schema_version must be exactly "scarlix.artifact/v1".
//   - artifact_type must be "sglang_expert_pack" (the closed set).
//   - backend_id must be "sglang" — the cross-engine refusal (this
//     release's must-not-do, ENFORCED: a pack for vllm/beellama/ollama/
//     infer_ssd or any other slug is refused BY NAME).
//   - artifact_id: required, closed-charset slug (the model-id rules —
//     join keys are not free text).
//   - model.id / model.revision / model.checkpoint_sha256 required; the
//     checkpoint fingerprint must be 64 hex.
//   - pack.format / pack.version required; pack.sha256 must be 64 hex
//     (the DECLARED pack fingerprint — verification is the separate
//     future machinery); pack.total_bytes > 0.
//   - compatibility.sglang_version / cuda_version required non-empty;
//     container_digest must be @sha256:-pinned (digest-pinned and
//     fixed — §8.4's exit criterion, applied to the pack's engine
//     container); gpu_compute_capabilities non-empty with non-empty
//     entries.
//   - NO placeholder values anywhere (the §8.2/§8.4 rule).
func ValidateExpertPackManifest(p *ExpertPackManifest) []string {
	var reasons []string
	add := func(format string, args ...interface{}) {
		reasons = append(reasons, fmt.Sprintf(format, args...))
	}

	if p.SchemaVersion != SchemaVersionArtifact {
		add("schema_version %q is not %q — a future artifact schema must be read by future code, this loader refuses it by name", p.SchemaVersion, SchemaVersionArtifact)
	}
	if p.ArtifactType != ArtifactTypeSGLangExpertPack {
		add("artifact_type %q is not %q (the closed v19.8.0 artifact-type set)", p.ArtifactType, ArtifactTypeSGLangExpertPack)
	}
	if p.BackendID != BackendIDSGLang {
		add("backend_id %q is not %q — an SGLang Expert Pack is validated for SGLang ONLY; cross-engine pack compatibility is not claimed and is REFUSED here by name (roadmap §8.5, the v19.8.0 must-not-do)", p.BackendID, BackendIDSGLang)
	}
	if strings.TrimSpace(p.ArtifactID) == "" {
		add("artifact_id is required (the pack artifact's identity)")
	} else if !modelIDRe.MatchString(p.ArtifactID) {
		add("artifact_id %q is not a safe closed identifier (lowercase slug: alnum start, alnum/dot/dash/underscore, max 128) — artifact ids are join keys (the model-id rules)", p.ArtifactID)
	}
	if strings.TrimSpace(p.Model.ID) == "" {
		add("model.id is required (the checkpoint binding)")
	}
	if strings.TrimSpace(p.Model.Revision) == "" {
		add("model.revision is required (the checkpoint binding)")
	}
	if !hex64Re.MatchString(p.Model.CheckpointSHA256) {
		add("model.checkpoint_sha256 %q is not a 64-hex-char SHA-256 fingerprint — the pack binds to the EXACT checkpoint (roadmap §8.5: reject mismatches)", p.Model.CheckpointSHA256)
	}
	if strings.TrimSpace(p.Pack.Format) == "" {
		add("pack.format is required (the pack artifact format, versioned by the engine)")
	}
	if strings.TrimSpace(p.Pack.Version) == "" {
		add("pack.version is required (the pack build version)")
	}
	if !hex64Re.MatchString(p.Pack.SHA256) {
		add("pack.sha256 %q is not a 64-hex-char SHA-256 fingerprint — the DECLARED pack fingerprint is required (verification is the separate future machinery; a declaration still needs a fingerprint)", p.Pack.SHA256)
	}
	if p.Pack.TotalBytes == 0 {
		add("pack.total_bytes must be > 0 (the pack's storage demand is a number, not an unknown)")
	}
	if strings.TrimSpace(p.Compatibility.SGLangVersion) == "" {
		add("compatibility.sglang_version is required (§8.2's engine dimension: exact repository, commit and release)")
	}
	if !strings.Contains(p.Compatibility.ContainerDigest, "@sha256:") {
		add("compatibility.container_digest %q carries no @sha256: digest — the engine container is digest-pinned and fixed (roadmap §8.4's exit criterion applied to the pack)", p.Compatibility.ContainerDigest)
	}
	if strings.TrimSpace(p.Compatibility.CUDAVersion) == "" {
		add("compatibility.cuda_version is required (§8.2's CUDA dimension: exact runtime/toolkit version)")
	}
	if len(p.Compatibility.GPUComputeCapabilities) == 0 {
		add("compatibility.gpu_compute_capabilities is required non-empty (§8.2's GPU dimension: vendor, model, compute capability)")
	}
	for i, c := range p.Compatibility.GPUComputeCapabilities {
		if strings.TrimSpace(c) == "" {
			add("compatibility.gpu_compute_capabilities[%d] is empty — a placeholder slot is not a capability", i)
		}
	}
	reasons = append(reasons, placeholderScan(p)...)
	return reasons
}

// BoundExpertPack is one standalone-valid pack manifest PLUS its
// checkpoint-binding verdict against the loaded model manifests. An
// unbound pack is DATA (the operator's declared artifact), never a
// schedulable capability — the v19.8.0 planner does not consume packs
// (the §8.8 enablement policy: pack execution needs pack-byte
// verification + the §8.7 target-host acceptance matrix, both named
// future work).
type BoundExpertPack struct {
	Manifest ExpertPackManifest `json:"manifest"`
	// BoundModelID — the model manifest this pack bound to (empty when
	// unbound).
	BoundModelID string `json:"bound_model_id"`
	// Bound — the checkpoint binding succeeded (id + revision +
	// checkpoint fingerprint all matched, and the model's own expert
	// pack declaration AGREES).
	Bound bool `json:"bound"`
	// BindingReasons — the named reasons the binding failed (empty when
	// bound; populated when Bound=false).
	BindingReasons []string `json:"binding_reasons,omitempty"`
}

// BindExpertPacks evaluates every standalone-valid pack against the
// loaded model manifests (the SECOND validation stage — shape first,
// binding second). The binding rules:
//
//   - a model manifest with the pack's model.id must EXIST,
//   - its revision and checkpoint.sha256 must match EXACTLY (§8.5: "the
//     manifest must bind the pack to the exact checkpoint. Reject
//     mismatches."),
//   - the model's artifacts.expert_pack must be ENABLED with
//     compatible_backend == "sglang" (a pack for a model that does not
//     declare one is an orphan — refused as a binding, not merged),
//   - the model's declared pack sha256/format/version must AGREE with
//     the artifact manifest's (two files disagreeing about one artifact
//     is a collision, not a merge).
func BindExpertPacks(packs []ExpertPackManifest, manifests []Manifest) []BoundExpertPack {
	byID := make(map[string]*Manifest, len(manifests))
	for i := range manifests {
		byID[manifests[i].ID] = &manifests[i]
	}
	out := make([]BoundExpertPack, 0, len(packs))
	for _, p := range packs {
		b := BoundExpertPack{Manifest: p}
		m, ok := byID[p.Model.ID]
		if !ok {
			b.BindingReasons = append(b.BindingReasons, fmt.Sprintf("no model manifest declares model.id %q — an Expert Pack binds to an EXACT checkpoint, and an orphan pack is refused as a binding", p.Model.ID))
			out = append(out, b)
			continue
		}
		if m.Revision != p.Model.Revision {
			b.BindingReasons = append(b.BindingReasons, fmt.Sprintf("model.revision %q does not match the manifest revision %q — the pack binds to the EXACT checkpoint (reject mismatches)", p.Model.Revision, m.Revision))
		}
		if !strings.EqualFold(m.Checkpoint.SHA256, p.Model.CheckpointSHA256) {
			b.BindingReasons = append(b.BindingReasons, fmt.Sprintf("model.checkpoint_sha256 %s does not match the manifest checkpoint fingerprint %s — the pack binds to the EXACT checkpoint (reject mismatches)", p.Model.CheckpointSHA256, m.Checkpoint.SHA256))
		}
		if !m.Artifacts.ExpertPack.Enabled {
			b.BindingReasons = append(b.BindingReasons, fmt.Sprintf("model %q does not declare an ENABLED artifacts.expert_pack — a pack for a model that declares none is an orphan", m.ID))
		} else {
			if m.Artifacts.ExpertPack.CompatibleBackend != BackendIDSGLang {
				b.BindingReasons = append(b.BindingReasons, fmt.Sprintf("the model's expert_pack.compatible_backend %q is not %q — the pack and the model declaration must agree on the ONE validated engine", m.Artifacts.ExpertPack.CompatibleBackend, BackendIDSGLang))
			}
			if !strings.EqualFold(m.Artifacts.ExpertPack.SHA256, p.Pack.SHA256) {
				b.BindingReasons = append(b.BindingReasons, fmt.Sprintf("the model's expert_pack.sha256 %s does not match the artifact manifest's pack.sha256 %s — two files disagreeing about one artifact is a collision, not a merge", m.Artifacts.ExpertPack.SHA256, p.Pack.SHA256))
			}
			if m.Artifacts.ExpertPack.Format != p.Pack.Format {
				b.BindingReasons = append(b.BindingReasons, fmt.Sprintf("the model's expert_pack.format %q does not match the artifact manifest's pack.format %q", m.Artifacts.ExpertPack.Format, p.Pack.Format))
			}
			if m.Artifacts.ExpertPack.Version != p.Pack.Version {
				b.BindingReasons = append(b.BindingReasons, fmt.Sprintf("the model's expert_pack.version %q does not match the artifact manifest's pack.version %q", m.Artifacts.ExpertPack.Version, p.Pack.Version))
			}
		}
		if len(b.BindingReasons) == 0 {
			b.Bound = true
			b.BoundModelID = m.ID
		}
		out = append(out, b)
	}
	return out
}
