// Package modelmeta — manifest loading + validation (v19.7.0, roadmap §5.5).
//
// The manifest is a YAML file declaring a model's execution metadata
// under the versioned schema "scarlix.model/v2". Files are loaded from a
// manifest directory (SCARLIX_MODELS_D env override → /etc/scarlix/
// models.d → <repo>/models.d), file name suffix ".manifest.yaml".
//
// Validation is FAIL-CLOSED with named errors: a manifest that fails
// validation is reported as an INVALID ENTRY (with reasons), never
// silently dropped and never best-effort parsed — the operator sees
// exactly which manifest is broken and why. An invalid manifest never
// contributes placement metadata; the v2 models view carries it as a
// declared-but-untrusted record with its reasons.
package modelmeta

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SchemaVersionV2 is the manifest schema identity this package reads.
// An unknown schema_version is a named validation error (fail-closed:
// a future schema must be read by future code, not half-parsed here).
const SchemaVersionV2 = "scarlix.model/v2"

// Manifest is the on-disk YAML shape (roadmap §5.5 example, field-for-
// field). Zero values inside `architecture`/`runtime`/`artifacts` are the
// example's PLACEHOLDERS — the loader treats required placeholders as
// UNKNOWN, and unknown required values fail placement eligibility.
type Manifest struct {
	SchemaVersion string `yaml:"schema_version" json:"schema_version"`
	ID            string `yaml:"id" json:"id"`
	Revision      string `yaml:"revision" json:"revision"`
	Checkpoint    struct {
		Format       string `yaml:"format" json:"format"`
		Quantization string `yaml:"quantization" json:"quantization"`
		// SHA256 — the DECLARED fingerprint. The yaml key stays "sha256"
		// (the on-disk scarlix.model/v2 schema is unchanged); the JSON key
		// is renamed "declared_sha256" (v19.7.1, KPK-19.7.0-P2-04) so every
		// EMITTED provenance distinguishes declared from VERIFIED bytes —
		// verification is VerifyManifestArtifacts' separate answer.
		SHA256 string `yaml:"sha256" json:"declared_sha256"`
	} `yaml:"checkpoint" json:"checkpoint"`
	Architecture struct {
		Kind             Architecture `yaml:"kind" json:"kind"`
		TotalParameters  string       `yaml:"total_parameters" json:"total_parameters"`
		ActiveParameters string       `yaml:"active_parameters" json:"active_parameters"`
		ExpertCount      int          `yaml:"expert_count" json:"expert_count"`
		ExpertsPerToken  int          `yaml:"experts_per_token" json:"experts_per_token"`
		ExpertBytes      uint64       `yaml:"expert_bytes" json:"expert_bytes"`
		SharedBytes      uint64       `yaml:"shared_bytes" json:"shared_bytes"`
	} `yaml:"architecture" json:"architecture"`
	Runtime struct {
		ContextLimit int `yaml:"context_limit" json:"context_limit"`
		KVCache      struct {
			Precision             string `yaml:"precision" json:"precision"`
			BytesPerTokenEstimate uint64 `yaml:"bytes_per_token_estimate" json:"bytes_per_token_estimate"`
		} `yaml:"kv_cache" json:"kv_cache"`
		SupportedBackends []string `yaml:"supported_backends" json:"supported_backends"`
	} `yaml:"runtime" json:"runtime"`
	Artifacts struct {
		ExpertPack struct {
			Enabled bool   `yaml:"enabled" json:"enabled"`
			Format  string `yaml:"format" json:"format"`
			Version string `yaml:"version" json:"version"`
			// SHA256 — DECLARED fingerprint (see Checkpoint.SHA256 note).
			SHA256            string `yaml:"sha256" json:"declared_sha256"`
			CompatibleBackend string `yaml:"compatible_backend" json:"compatible_backend"`
		} `yaml:"expert_pack" json:"expert_pack"`
	} `yaml:"artifacts" json:"artifacts"`
	Storage struct {
		ModelPath         string `yaml:"model_path" json:"model_path"`
		ExpertPath        string `yaml:"expert_path" json:"expert_path"`
		PreferredDeviceID string `yaml:"preferred_device_id" json:"preferred_device_id"`
	} `yaml:"storage" json:"storage"`
}

// hex64Re matches exactly 64 lowercase-or-uppercase hex chars (a SHA-256
// fingerprint). Uppercase is normalized to lowercase at validation.
var hex64Re = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// modelIDRe is the CLOSED safe identifier charset for manifest model IDs
// (v19.7.1, KPK-19.7.0-P2-04: an ID is a join key consumed by maps and
// paths — arbitrary free text invites collisions and path-shaped IDs).
// Lowercase slug: starts alnum, then alnum/dot/dash/underscore, ≤128 chars.
var modelIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// backendIDRe — backend identities are lowercase slugs ("sglang",
// "infer_ssd"). The set is deliberately OPEN (not the current runtime
// enumeration): a manifest may declare a future backend (the roadmap's
// own example declares "infer_ssd" before v19.8.0 exists); the PLANNER
// intersects declared backends with live runtimes — the manifest only
// declares, never asserts presence.
var backendIDRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// InvalidManifest is one loaded-but-invalid manifest with its named
// reasons (fail-closed with visibility — see package doc).
type InvalidManifest struct {
	Path    string
	Reasons []string
}

// LoadedManifests is the full result of a directory load: valid manifests
// (sorted by ID for reproducible output) + invalid entries + the source
// provenance.
type LoadedManifests struct {
	Manifests []Manifest
	Invalid   []InvalidManifest
	// Dir — the directory that was read (provenance).
	Dir string
	// DirError — non-empty when the directory itself was unreadable
	// (then Manifests is empty and Invalid is empty: NOTHING was loaded).
	DirError string
}

// ResolveManifestsDir picks the manifest directory: explicit
// SCARLIX_MODELS_D → /etc/scarlix/models.d → <cwd>/models.d (dev
// fallback). Empty string when no candidate exists (a valid, non-error
// absence: no manifests declared).
func ResolveManifestsDir() string {
	if d := os.Getenv("SCARLIX_MODELS_D"); d != "" {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			return d
		}
		return "" // explicit override that does not exist: honest absence
	}
	for _, d := range []string{"/etc/scarlix/models.d", "models.d"} {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			return d
		}
	}
	return ""
}

// LoadManifests reads every *.manifest.yaml in dir. Files are read,
// parsed, and validated independently — one broken manifest degrades
// ONLY itself (its InvalidManifest entry), never the set.
//
// DUPLICATE MODEL IDs are rejected as a GROUP (v19.7.1,
// KPK-19.7.0-P2-04): when two files declare the same id, EVERY
// conflicting manifest is invalid with a named collision reason — a
// deterministic file-order winner was silently overwriting the loser
// in the v2 snapshot's by-ID join; now nobody wins and the operator
// sees both paths.
func LoadManifests(dir string) LoadedManifests {
	res := LoadedManifests{Dir: dir}
	if dir == "" {
		res.DirError = "no manifest directory resolved"
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
		if strings.HasSuffix(e.Name(), ".manifest.yaml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // reproducible order

	type loaded struct {
		path string
		m    Manifest
	}
	var valid []loaded
	for _, name := range names {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			res.Invalid = append(res.Invalid, InvalidManifest{Path: path, Reasons: []string{fmt.Sprintf("read failed: %v", err)}})
			continue
		}
		var m Manifest
		// v19.8.1 (KPK-19.8.0-P3-06): strict single-document decode —
		// unknown fields and trailing documents are named errors, never
		// silently-accepted operator configuration.
		if err := DecodeSingleStrictYAML(raw, &m); err != nil {
			res.Invalid = append(res.Invalid, InvalidManifest{Path: path, Reasons: []string{fmt.Sprintf("YAML parse failed: %v", err)}})
			continue
		}
		if reasons := ValidateManifest(&m); len(reasons) > 0 {
			res.Invalid = append(res.Invalid, InvalidManifest{Path: path, Reasons: reasons})
			continue
		}
		valid = append(valid, loaded{path: path, m: m})
	}

	// Duplicate-ID group rejection (KPK-19.7.0-P2-04): collect every path
	// per id; an id claimed by more than one file invalidates ALL of them
	// with a reason naming every conflicting path. No winner is chosen.
	byIDPaths := make(map[string][]string)
	for _, v := range valid {
		byIDPaths[v.m.ID] = append(byIDPaths[v.m.ID], v.path)
	}
	for _, v := range valid {
		if paths := byIDPaths[v.m.ID]; len(paths) > 1 {
			res.Invalid = append(res.Invalid, InvalidManifest{
				Path: v.path,
				Reasons: []string{fmt.Sprintf(
					"duplicate model id %q — declared in %v; ALL conflicting manifests are invalid, no winner is chosen (KPK-19.7.0-P2-04)",
					v.m.ID, paths)},
			})
			continue
		}
		res.Manifests = append(res.Manifests, v.m)
	}
	return res
}

// ValidateManifest returns the named reasons a manifest is invalid
// (empty = valid). The rules are the roadmap's own fail-closed contract:
//
//   - schema_version must be exactly "scarlix.model/v2" (named refusal
//     for anything else — a future schema is future code).
//   - id and revision are required non-empty.
//   - checkpoint.format is required; checkpoint.sha256 must be 64 hex
//     chars (the verified fingerprint that binds packs and revisions).
//   - architecture.kind must be dense | moe | other.
//   - moe requires expert_count > 0 AND experts_per_token > 0 (unknown
//     MoE geometry is a placement blocker, not a default).
//   - runtime.context_limit must be > 0.
//   - runtime.supported_backends must be non-empty, every entry a
//     lowercase slug, no duplicates.
//   - an ENABLED expert pack requires its own sha256 (64 hex), a
//     compatible_backend (single slug), and a version; a DISABLED pack
//     is documentation and unconstrained.
func ValidateManifest(m *Manifest) []string {
	var reasons []string
	add := func(format string, args ...interface{}) {
		reasons = append(reasons, fmt.Sprintf(format, args...))
	}

	if m.SchemaVersion != SchemaVersionV2 {
		add("schema_version %q is not %q — a future schema must be read by future code, this loader refuses it by name", m.SchemaVersion, SchemaVersionV2)
	}
	if strings.TrimSpace(m.ID) == "" {
		add("id is required (the model identity the planner and the v1 catalog join on)")
	} else if !modelIDRe.MatchString(m.ID) {
		add("id %q is not a safe closed identifier (lowercase slug: alnum start, alnum/dot/dash/underscore, max 128) — ids are join keys and path-shaped free text is a collision and escape risk (KPK-19.7.0-P2-04)", m.ID)
	}
	if strings.TrimSpace(m.Revision) == "" {
		add("revision is required (HF revision or local build id — the pack/checkpoint binding anchor)")
	}
	if strings.TrimSpace(m.Checkpoint.Format) == "" {
		add("checkpoint.format is required (safetensors, gguf, ...)")
	}
	if !hex64Re.MatchString(m.Checkpoint.SHA256) {
		add("checkpoint.sha256 %q is not a 64-hex-char SHA-256 fingerprint — the verified checkpoint fingerprint is required (placement binds to it)", m.Checkpoint.SHA256)
	}
	switch m.Architecture.Kind {
	case ArchitectureDense, ArchitectureMoE, ArchitectureOther:
	default:
		add("architecture.kind %q is not one of dense|moe|other", m.Architecture.Kind)
	}
	if m.Architecture.Kind == ArchitectureMoE {
		if m.Architecture.ExpertCount <= 0 {
			add("moe requires architecture.expert_count > 0 (unknown MoE geometry blocks automatic placement)")
		}
		if m.Architecture.ExpertsPerToken <= 0 {
			add("moe requires architecture.experts_per_token > 0 (unknown MoE geometry blocks automatic placement)")
		}
	}
	if m.Runtime.ContextLimit <= 0 {
		add("runtime.context_limit must be > 0 (the context budget every admission decision divides)")
	}
	if len(m.Runtime.SupportedBackends) == 0 {
		add("runtime.supported_backends is required non-empty (which engines may serve this artifact)")
	}
	seen := map[string]bool{}
	for _, b := range m.Runtime.SupportedBackends {
		if !backendIDRe.MatchString(b) {
			add("runtime.supported_backends entry %q is not a lowercase backend slug", b)
			continue
		}
		if seen[b] {
			add("runtime.supported_backends entry %q is duplicated", b)
		}
		seen[b] = true
	}
	if m.Artifacts.ExpertPack.Enabled {
		ep := m.Artifacts.ExpertPack
		if !hex64Re.MatchString(ep.SHA256) {
			add("artifacts.expert_pack.sha256 %q is not a 64-hex-char fingerprint — an ENABLED pack must be verifiable", ep.SHA256)
		}
		if !backendIDRe.MatchString(ep.CompatibleBackend) {
			add("artifacts.expert_pack.compatible_backend %q is required as a single lowercase backend slug when the pack is enabled (cross-engine compatibility is never claimed)", ep.CompatibleBackend)
		}
		if strings.TrimSpace(ep.Version) == "" {
			add("artifacts.expert_pack.version is required when the pack is enabled")
		}
	}
	return reasons
}

// PlacementEligibility answers the roadmap's placement gate for one
// manifest: whether the metadata is COMPLETE ENOUGH for automatic
// placement, and the named reasons when it is not. A valid manifest can
// still be placement-INELIGIBLE when optional-but-required-for-planning
// values are unknown (the distinction the roadmap draws between schema
// validity and placement readiness).
type PlacementEligibility struct {
	// Eligible — true iff every placement-required value is known.
	Eligible bool `json:"eligible"`
	// Reasons — the named unknowns (empty when eligible).
	Reasons []string `json:"reasons"`
}

// PlacementEligible evaluates the placement gate:
//
//   - checkpoint fingerprint present (validated: 64 hex),
//   - context limit present,
//   - dense: dense footprint known OR parameters declared (the label the
//     planner parses under its own policy),
//   - moe: expert geometry known (expert_count, experts_per_token,
//     expert_bytes — expert paging needs all three),
//   - KV-cache bytes/token is OPTIONAL for placement (runtimes size the
//     KV cache themselves — runtime-defined); its absence is reported in
//     the snapshot as unknown, and Phase 2's KV-budget admission will
//     treat it as its own named unknown.
//
// Invalid manifests are trivially ineligible (validate first).
func PlacementEligible(m *Manifest) PlacementEligibility {
	var reasons []string
	add := func(format string, args ...interface{}) {
		reasons = append(reasons, fmt.Sprintf(format, args...))
	}

	if invalid := ValidateManifest(m); len(invalid) > 0 {
		return PlacementEligibility{Eligible: false, Reasons: append([]string{"manifest fails schema validation:"}, invalid...)}
	}
	switch m.Architecture.Kind {
	case ArchitectureMoE:
		if m.Architecture.ExpertBytes == 0 {
			add("moe placement requires architecture.expert_bytes > 0 (expert paging sizes the expert store from it)")
		}
		if m.Architecture.SharedBytes == 0 {
			add("moe placement requires architecture.shared_bytes > 0 (the resident dense footprint of the MoE)")
		}
	}
	return PlacementEligibility{Eligible: len(reasons) == 0, Reasons: reasons}
}

// Execution converts a VALID manifest into the normalized ModelExecution
// (the v2 snapshot's model record). The manifest MUST have passed
// ValidateManifest — callers gate that; this function assumes it and
// carries every remaining unknown as the honest zero (the eligibility
// reasons live in PlacementEligibility, not re-derived here).
func (m *Manifest) Execution(manifestPath string) ModelExecution {
	me := ModelExecution{
		ID:                   m.ID,
		Revision:             m.Revision,
		CheckpointSHA256:     strings.ToLower(m.Checkpoint.SHA256),
		Architecture:         m.Architecture.Kind,
		TotalParameters:      m.Architecture.TotalParameters,
		ActiveParameters:     m.Architecture.ActiveParameters,
		Quantization:         m.Checkpoint.Quantization,
		Format:               m.Checkpoint.Format,
		ContextLimit:         m.Runtime.ContextLimit,
		KVCacheBytesPerToken: m.Runtime.KVCache.BytesPerTokenEstimate,
		DenseFootprintBytes:  m.Architecture.SharedBytes, // dense footprint = shared bytes in MoE; for dense models the declared storage is the checkpoint itself
		ExpertCount:          m.Architecture.ExpertCount,
		ExpertsPerToken:      m.Architecture.ExpertsPerToken,
		ExpertBytes:          m.Architecture.ExpertBytes,
		SharedBytes:          m.Architecture.SharedBytes,
		SupportedBackends:    append([]string(nil), m.Runtime.SupportedBackends...),
		RequiredFeatures:     nil,
		ManifestPath:         manifestPath,
	}
	if m.Architecture.Kind == ArchitectureDense {
		// For a dense model the whole checkpoint is the dense footprint —
		// shared_bytes is the MoE field; dense models declare total size
		// through the same channel when they have it, else it stays
		// unknown (0) and the eligibility gate reports it.
		me.DenseFootprintBytes = m.Architecture.SharedBytes
	}
	if m.Artifacts.ExpertPack.Enabled || m.Artifacts.ExpertPack.Format != "" {
		me.ExpertPack = &ExpertPackArtifact{
			Enabled:           m.Artifacts.ExpertPack.Enabled,
			Format:            m.Artifacts.ExpertPack.Format,
			Version:           m.Artifacts.ExpertPack.Version,
			SHA256:            strings.ToLower(m.Artifacts.ExpertPack.SHA256),
			CompatibleBackend: m.Artifacts.ExpertPack.CompatibleBackend,
		}
	}
	if me.SupportedBackends == nil {
		me.SupportedBackends = []string{}
	}
	return me
}
