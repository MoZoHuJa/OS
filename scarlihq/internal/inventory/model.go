// Package inventory — Model catalog collector (v19.0.9).
//
// This file implements the runtime population of the Model struct defined in
// types.go (v19.0.7). It is the Go-side counterpart of the bash `scarlix model
// list` subcommand — both read models.yaml and scan /models/ for present files,
// but the Go collector emits the normalized `inventory.Model` JSON shape that
// downstream consumers (ScarliHQ resource view v19.1.6, future compute fabric
// v19.2.x, and `scarlix --json model list`) require.
//
// Scope rules (v19.0.9 task 3-b):
//
//   - READ-ONLY. No model download, no model removal, no path rewrite.
//   - Graceful degradation: if models.yaml is missing/unparseable, the
//     collector returns an empty (non-nil) []Model so callers can always
//     JSON-marshal to `[]` safely (stability contract §5).
//   - models.yaml location priority: $SCARLIX_MODELS_YAML (explicit override)
//     → /etc/scarlix/models.yaml (installed system copy) → repo-root
//     models.yaml (dev sandbox fallback). The selected path is NOT exposed
//     in the Model struct itself (the Model struct doesn't have a "source"
//     field) — callers that want the path should call resolveModelsYAML().
//   - Format / Quantization / Capabilities / SupportedRuntimes are inferred
//     from the engine name + the model path / hf_file name. Hard-coded in
//     engineMeta below — these reflect the engine→format→capabilities
//     mapping documented in docs/SCARLIX_DATA_CONTRACTS.md §4.
//   - Present is true iff os.Stat(model_path) succeeds. For ollama (which
//     has no local path — models are pulled on demand into /var/lib/scarlix/
//     ollama) Present is always false (the task spec allows this: "or just
//     set false — ollama pulls on demand").
//
// Complements (does NOT replace) the existing internal/status package, which
// reads /var/lib/scarlix/host-status.json (the host-bridge → dashboard pipe).
// The status package is the lower-level read; inventory is the higher-level
// normalized view that v19.1.6+ will compose from multiple sources.
package inventory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// engineMeta captures per-engine static metadata used to normalize a model
// section. The engine name is derived from the YAML section key (sglang,
// vllm, beellama, ollama) and the SupportedRuntimes list uses the engine
// name (where "llamacpp" is the canonical name for the beellama section —
// matches the bash CLI's `engine="llamacpp"` mapping in scarlix L520).
//
// Fields:
//   - Format           — "safetensors" or "gguf"
//   - Quantization      — heuristic default ("awq" for sglang/vllm, "" for
//     ollama — beellama overrides with q4_k_m parsed
//     from hf_file name)
//   - Capabilities     — Model.Capabilities (chat/completion/tools)
//   - SupportedRuntimes — Model.SupportedRuntimes (the runtimes that can
//     serve this format)
type engineMeta struct {
	Format            string
	Quantization      string
	Capabilities      []string
	SupportedRuntimes []string
}

// engineMetadata is the canonical engine → format/caps mapping. Sourced
// from docs/SCARLIX_DATA_CONTRACTS.md §4 examples:
//   - AWQ safetensors models are served by sglang + vllm
//   - GGUF models are served by llamacpp
//   - ollama pulls GGUF blobs internally but exposes them via its own API
var engineMetadata = map[string]engineMeta{
	"sglang": {
		Format:            "safetensors",
		Quantization:      "awq",
		Capabilities:      []string{"chat", "completion", "tools"},
		SupportedRuntimes: []string{"sglang", "vllm"},
	},
	"vllm": {
		Format:            "safetensors",
		Quantization:      "awq",
		Capabilities:      []string{"chat", "completion", "tools"},
		SupportedRuntimes: []string{"sglang", "vllm"},
	},
	"beellama": {
		Format:            "gguf",
		Quantization:      "q4_k_m", // overridden from hf_file name in modelFromYAML
		Capabilities:      []string{"chat", "completion"},
		SupportedRuntimes: []string{"llamacpp"},
	},
	"ollama": {
		Format:            "gguf",
		Quantization:      "",
		Capabilities:      []string{"chat", "completion"},
		SupportedRuntimes: []string{"ollama"},
	},
}

// modelSectionKeys is the canonical list of model sections in models.yaml.
// Order is stable (sglang, vllm, beellama, ollama) — matches the bash CLI's
// iteration order (scarlix L518) so JSON output is reproducible across
// invocations and matches the order humans see in `scarlix model list`.
var modelSectionKeys = []string{"sglang", "vllm", "beellama", "ollama"}

// CollectModels reads models.yaml config + scans /models/ for present files.
// Returns normalized Model structs. Empty []Model{} (never nil) if models.yaml
// is unavailable or unparseable.
//
// Field population rules (see docs/SCARLIX_DATA_CONTRACTS.md §4):
//   - ID                — the YAML section key ("sglang", "vllm", "beellama",
//     "ollama"). Stable identifier; matches the runtime
//     IDs in runtimeMetadata (so cross-refs work).
//   - Path              — model_path from YAML; for beellama (which has no
//     model_path, only hf_repo + hf_file) derive as
//     "/models/" + basename(hf_file); for ollama
//     (which has neither) empty string.
//   - Format            — "safetensors" for sglang/vllm, "gguf" for beellama
//     and ollama.
//   - Quantization      — "awq" for sglang/vllm; "q4_k_m" for beellama
//     (parsed from hf_file name, case-insensitive);
//     "" for ollama.
//   - Parameters        — parsed from model path / hf_file / model name
//     (e.g. "14B" from "Qwen3-14B-AWQ"). "" if no match.
//   - ContextLength     — read from YAML; sglang uses context_length key,
//     vllm uses max_model_len, beellama uses context_size.
//     Ollama has no context-length key → 0.
//   - EstimatedVRAM     — 0 (not in models.yaml — left for future
//     calculation in v19.1.x resource foundation).
//   - Capabilities      — engine-derived (see engineMetadata).
//   - SupportedRuntimes — engine-derived (see engineMetadata).
//   - Present           — true iff os.Stat(Path) succeeds. For ollama
//     (Path=="") always false (task spec allows this —
//     ollama pulls on demand).
func CollectModels() []Model {
	path := resolveModelsYAML()
	if path == "" {
		// models.yaml not found anywhere we look — graceful degradation.
		return []Model{}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// Read error (permissions, race-condition file delete) — return empty.
		// Logged to stderr for diagnosis; caller sees []Model{}.
		fmt.Fprintf(os.Stderr, "inventory: cannot read %s: %v\n", path, err)
		return []Model{}
	}

	// Parse with gopkg.in/yaml.v3 (already a go.mod dependency, v3.0.1).
	// Top-level structure: map[string]map[string]interface{} where outer
	// keys are section names (sglang/vllm/...) and inner maps are the
	// per-section config (model_path, hf_repo, hf_file, context_length, ...).
	var raw map[string]map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		fmt.Fprintf(os.Stderr, "inventory: cannot parse %s as YAML: %v\n", path, err)
		return []Model{}
	}

	out := make([]Model, 0, len(modelSectionKeys))
	for _, section := range modelSectionKeys {
		sectionData, ok := raw[section]
		if !ok || sectionData == nil {
			// Section absent in YAML (e.g. someone deleted the ollama:
			// block). Skip — don't emit a zero-value Model that would
			// confuse downstream consumers with a Present=false ghost.
			continue
		}
		out = append(out, modelFromYAML(section, section, sectionData))
	}
	return out
}

// modelFromYAML converts a yq-parsed model section to a Model struct.
//
//   - name    = the YAML section key (used as Model.ID)
//   - engine  = the canonical engine name (sglang/vllm/llamacpp/ollama —
//     for the beellama section the engine is "llamacpp")
//   - raw     = the parsed section map (keys: model_path, hf_repo,
//     hf_file, context_length, context_size, max_model_len,
//     model, ...)
//
// Numeric fields (ContextLength) are parsed defensively — a missing or
// non-integer value yields 0 (the zero value), not an error. This matches
// the data contract's "0 means unset" convention.
func modelFromYAML(name, engine string, raw map[string]interface{}) Model {
	// Look up engine metadata; default to a minimal entry if the engine
	// name is unknown (defensive — shouldn't happen with our fixed
	// modelSectionKeys + engineMetadata).
	meta, ok := engineMetadata[name]
	if !ok {
		meta = engineMeta{
			Format:            "",
			Quantization:      "",
			Capabilities:      []string{"chat"},
			SupportedRuntimes: []string{},
		}
	}

	// Extract model_path (may be empty for beellama + ollama).
	modelPath := stringField(raw, "model_path")

	// For beellama: derive model_path from hf_file if model_path
	// is missing. Compose's default is "/models/" + basename(hf_file).
	// If hf_file is already an absolute path (rare — e.g. test fixtures
	// or non-standard installs), use it verbatim.
	if modelPath == "" && name == "beellama" {
		if hfFile := stringField(raw, "hf_file"); hfFile != "" {
			if filepath.IsAbs(hfFile) {
				modelPath = hfFile
			} else {
				modelPath = filepath.Join("/models", filepath.Base(hfFile))
			}
		}
	}

	// Quantization: override for beellama by parsing hf_file name
	// (Q4_K_M.gguf → "q4_k_m"). For sglang/vllm the engine default
	// ("awq") is correct; for ollama the engine default ("") is correct.
	quant := meta.Quantization
	if name == "beellama" {
		if hfFile := stringField(raw, "hf_file"); hfFile != "" {
			if q := parseGGUFQuant(hfFile); q != "" {
				quant = q
			}
		}
	}

	// ContextLength: per-engine YAML key differs.
	//   - sglang:   context_length
	//   - vllm:     max_model_len
	//   - beellama: context_size
	//   - ollama:   (none — 0)
	ctxLen := 0
	switch name {
	case "sglang":
		ctxLen = intField(raw, "context_length")
	case "vllm":
		ctxLen = intField(raw, "max_model_len")
	case "beellama":
		ctxLen = intField(raw, "context_size")
	}

	// Parameters: parse from model_path or hf_repo / hf_file / model name.
	// Looks for a substring like "14B", "3b", "7M", "70B" — case-insensitive.
	params := parseParameters(modelPath)
	if params == "" {
		params = parseParameters(stringField(raw, "hf_repo"))
	}
	if params == "" {
		params = parseParameters(stringField(raw, "hf_file"))
	}
	if params == "" {
		params = parseParameters(stringField(raw, "model"))
	}

	// Present: true iff Path resolves on disk. For ollama (Path="") → false.
	present := false
	if modelPath != "" {
		if _, err := os.Stat(modelPath); err == nil {
			present = true
		}
	}

	// Build non-nil slices so JSON marshals as [...] not null.
	caps := make([]string, len(meta.Capabilities))
	copy(caps, meta.Capabilities)
	sr := make([]string, len(meta.SupportedRuntimes))
	copy(sr, meta.SupportedRuntimes)

	return Model{
		ID:                name,
		Path:              modelPath,
		Format:            meta.Format,
		Quantization:      quant,
		Parameters:        params,
		ContextLength:     ctxLen,
		EstimatedVRAM:     0, // not in models.yaml — future v19.1.x calculation
		Capabilities:      caps,
		SupportedRuntimes: sr,
		Present:           present,
	}
}

// ----- internal helpers ----------------------------------------------------

// resolveModelsYAML finds the models.yaml path using the priority order:
//  1. $SCARLIX_MODELS_YAML env var (explicit override — used in tests +
//     non-standard installs)
//  2. /etc/scarlix/models.yaml (the canonical system install path)
//  3. $SCARLIX_REPO/models.yaml (dev sandbox convenience)
//  4. $PWD/models.yaml (last-resort fallback for `cd repo && scarlix ...`)
//
// Returns "" if no candidate exists. The returned path is the first
// existing file — does NOT validate it parses as YAML (CollectModels
// does that).
func resolveModelsYAML() string {
	if p := os.Getenv("SCARLIX_MODELS_YAML"); p != "" {
		if fileExists(p) {
			return p
		}
	}
	candidates := []string{
		"/etc/scarlix/models.yaml",
		filepath.Join(os.Getenv("SCARLIX_REPO"), "models.yaml"),
		filepath.Join(os.Getenv("PWD"), "models.yaml"),
	}
	for _, c := range candidates {
		if c != "" && fileExists(c) {
			return c
		}
	}
	return ""
}

// fileExists returns true iff path exists and is a regular file (not a
// directory, socket, etc.). Used by resolveModelsYAML for the candidate
// priority walk.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// stringField extracts a string value from a parsed YAML map. Handles the
// case where the YAML value is a non-string scalar (e.g. unquoted number
// or bool) by formatting it. Returns "" for missing keys or null values.
func stringField(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case int:
		return strconv.Itoa(s)
	case int64:
		return strconv.FormatInt(s, 10)
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(s)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// intField extracts an integer value from a parsed YAML map. yaml.v3 parses
// integer literals as `int` (not int64) so the int case is the common one;
// other numeric types are coerced. Returns 0 for missing / null / non-numeric.
func intField(m map[string]interface{}, key string) int {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	case string:
		// Defensive: yaml.v3 may quote numeric values if the user wrote
		// `context_length: "32768"`. Parse leniently.
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
		return 0
	default:
		return 0
	}
}

// parseParameters scans a model path / repo / filename for a parameter-size
// token like "14B", "7b", "70B", "3B", "8x7B". Returns the canonical form
// (uppercase: "14B") or "" if none found.
//
// Examples:
//   - "/models/Qwen3-14B-AWQ"              → "14B"
//   - "Qwen/Qwen3-14B-AWQ"                 → "14B"
//   - "Qwen3-14B-Q4_K_M.gguf"              → "14B"
//   - "qwen2.5:3b"                         → "3B"
//   - "meta-llama/Llama-3.1-8B-Instruct"   → "8B"
//   - "/models/Mixtral-8x7B-Instruct-v0.3" → "8x7B" (compound — returned as-is)
//   - "/models/embeddings"                 → "" (no match)
var paramRe = regexp.MustCompile(`(?i)\b(\d+(?:x\d+)?)([bmk])\b`)

func parseParameters(s string) string {
	if s == "" {
		return ""
	}
	m := paramRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	// Canonicalize: uppercase the size suffix (B/M/K). Preserve the
	// digit portion (including any "x" multiplier — "8x7" stays "8x7").
	num := m[1]                     // e.g. "14", "8x7", "3"
	suffix := strings.ToUpper(m[2]) // "B", "M", "K"
	return num + suffix
}

// parseGGUFQuant extracts the quantization label from a GGUF filename.
// Looks for known quant tokens (Q4_K_M, Q5_K_M, Q8_0, F16, etc.) and
// returns them in lowercase (matching the data contract examples:
// "q4_k_m", "q8_0", "fp16").
//
// Examples:
//   - "Qwen3-14B-Q4_K_M.gguf"  → "q4_k_m"
//   - "qwen-7b-q5_k_s.gguf"    → "q5_k_s"
//   - "model-q8_0.gguf"        → "q8_0"
//   - "model-f16.gguf"         → "f16"
//   - "model.fp16.gguf"        → "fp16"
//   - "model.gguf"             → ""
var ggufQuantRe = regexp.MustCompile(`(?i)\b(q\d(?:_\w+)*|f16|fp16|i8|i4)\b`)

func parseGGUFQuant(filename string) string {
	if filename == "" {
		return ""
	}
	// Strip extension so the regex sees "Qwen3-14B-Q4_K_M" not "...Q4_K_M.gguf"
	// (the regex uses \b boundaries which work on either form, but stripping
	// makes the F16 vs fp16 distinction cleaner when there's no separator).
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	m := ggufQuantRe.FindStringSubmatch(stem)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}
