// Tests for the v19.0.9 model catalog collector (model.go).
//
// Strategy:
//   - Most tests write a synthetic models.yaml to a temp file + set
//     SCARLIX_MODELS_YAML env var to point at it. This deterministically
//     exercises the parsing path without depending on whether the repo
//     has a models.yaml at any of the default search paths.
//   - A "real models.yaml" test uses the actual repo-root models.yaml
//     (when SCARLIX_REPO points at it) to verify the parser handles
//     the real file's schema correctly.
//   - Helpers (parseParameters, parseGGUFQuant) are exercised with
//     table-driven tests for thorough edge-case coverage.
//
// All tests assert the v19.0.7 stability contract: empty slices
// serialize as `[]`, never `null`, and the JSON field tags match the
// data contract.
package inventory

import (
        "encoding/json"
        "os"
        "path/filepath"
        "strings"
        "testing"
)

// writeTempYAML writes content to a temp file in t.TempDir() and returns
// its path. The temp dir is auto-cleaned by t.TempDir().
func writeTempYAML(t *testing.T, content string) string {
        t.Helper()
        dir := t.TempDir()
        path := filepath.Join(dir, "models.yaml")
        if err := os.WriteFile(path, []byte(content), 0644); err != nil {
                t.Fatalf("cannot write temp models.yaml: %v", err)
        }
        return path
}

// withEnv sets env vars for the duration of the test, restoring the
// originals on cleanup. Used to point SCARLIX_MODELS_YAML at a temp file
// (which is the canonical way to override the search path).
func withEnv(t *testing.T, key, value string) {
        t.Helper()
        old, had := os.LookupEnv(key)
        if err := os.Setenv(key, value); err != nil {
                t.Fatalf("cannot set %s: %v", key, err)
        }
        t.Cleanup(func() {
                if had {
                        _ = os.Setenv(key, old)
                } else {
                        _ = os.Unsetenv(key)
                }
        })
}

// --- CollectModels -------------------------------------------------------

func TestCollectModels_ReturnsEmptyWhenNoYAML(t *testing.T) {
        // When SCARLIX_MODELS_YAML is unset AND no default-path models.yaml
        // exists, CollectModels must return []Model{} (non-nil, len 0).
        // To make this deterministic we point SCARLIX_MODELS_YAML at a
        // non-existent path AND ensure no default-path file resolves —
        // easiest: point it at a temp file we never create.
        withEnv(t, "SCARLIX_MODELS_YAML", "/tmp/nonexistent-scarlix-test-models.yaml")
        // Also override SCARLIX_REPO + PWD so the fallback paths don't find
        // a real models.yaml. PWD is hard to override (we can't change the
        // process's PWD without a fork), but pointing SCARLIX_REPO at a
        // non-existent dir removes one candidate.
        withEnv(t, "SCARLIX_REPO", "/tmp/nonexistent-scarlix-test-repo")

        models := CollectModels()
        if models == nil {
                t.Fatalf("CollectModels() returned nil — must return empty slice per stability contract §5")
        }
        if len(models) != 0 {
                t.Fatalf("CollectModels() with no models.yaml should return 0 models; got %d (%+v)", len(models), models)
        }
        // Stability contract §5: marshal as `[]`, not `null`.
        data, err := json.Marshal(models)
        if err != nil {
                t.Fatalf("marshal empty Model slice failed: %v", err)
        }
        if string(data) != "[]" {
                t.Errorf("empty Model slice should marshal as `[]`; got %s", string(data))
        }
}

func TestCollectModels_NeverReturnsNil(t *testing.T) {
        // Even in error conditions, the result MUST be a non-nil slice.
        // (We can't easily test the "yaml exists but is unparseable" path
        // deterministically because the YAML parser is permissive — most
        // garbage parses to nil. But we can test the no-file path.)
        withEnv(t, "SCARLIX_MODELS_YAML", "/tmp/nonexistent-scarlix-test-models-2.yaml")
        withEnv(t, "SCARLIX_REPO", "/tmp/nonexistent-scarlix-test-repo-2")

        models := CollectModels()
        if models == nil {
                t.Fatal("CollectModels() returned nil — must always return initialized slice")
        }
        data, err := json.Marshal(models)
        if err != nil {
                t.Fatalf("marshal failed: %v", err)
        }
        if string(data) == "null" {
                t.Fatal("CollectModels() result marshaled as null — must be [] or [{...}]")
        }
}

func TestCollectModels_ParsesRealSchema(t *testing.T) {
        // Use a synthetic models.yaml that mirrors the schema of the real
        // repo-root models.yaml (all 4 sections with realistic field values).
        yaml := `# Test models.yaml — synthetic copy of the repo's schema.
sglang:
  model_path: "/models/Qwen3-14B-AWQ"
  hf_repo: "Qwen/Qwen3-14B-AWQ"
  context_length: 32768

vllm:
  model_path: "/models/Qwen3-14B-AWQ"
  tensor_parallel_size: 1
  max_model_len: 32768

beellama:
  hf_repo: "Qwen/Qwen3-14B-GGUF"
  hf_file: "Qwen3-14B-Q4_K_M.gguf"
  context_size: 32768

ollama:
  model: "qwen2.5:3b"
  keep_alive: "15m"
`
        path := writeTempYAML(t, yaml)
        withEnv(t, "SCARLIX_MODELS_YAML", path)

        models := CollectModels()
        if len(models) != 4 {
                t.Fatalf("expected 4 models; got %d (%+v)", len(models), models)
        }

        // Spot-check each section.
        byID := make(map[string]Model, len(models))
        for _, m := range models {
                byID[m.ID] = m
        }

        sglang, ok := byID["sglang"]
        if !ok {
                t.Fatal("no sglang model in output")
        }
        if sglang.Path != "/models/Qwen3-14B-AWQ" {
                t.Errorf("sglang.Path: got %q want /models/Qwen3-14B-AWQ", sglang.Path)
        }
        if sglang.Format != "safetensors" {
                t.Errorf("sglang.Format: got %q want safetensors", sglang.Format)
        }
        if sglang.Quantization != "awq" {
                t.Errorf("sglang.Quantization: got %q want awq", sglang.Quantization)
        }
        if sglang.Parameters != "14B" {
                t.Errorf("sglang.Parameters: got %q want 14B", sglang.Parameters)
        }
        if sglang.ContextLength != 32768 {
                t.Errorf("sglang.ContextLength: got %d want 32768", sglang.ContextLength)
        }
        if len(sglang.SupportedRuntimes) != 2 ||
                sglang.SupportedRuntimes[0] != "sglang" ||
                sglang.SupportedRuntimes[1] != "vllm" {
                t.Errorf("sglang.SupportedRuntimes: got %v want [sglang vllm]", sglang.SupportedRuntimes)
        }
        if !contains(sglang.Capabilities, "tools") {
                t.Errorf("sglang.Capabilities should contain 'tools'; got %v", sglang.Capabilities)
        }

        // vllm shares the same model_path as sglang (both load Qwen3-14B-AWQ).
        vllm, ok := byID["vllm"]
        if !ok {
                t.Fatal("no vllm model in output")
        }
        if vllm.Path != "/models/Qwen3-14B-AWQ" {
                t.Errorf("vllm.Path: got %q want /models/Qwen3-14B-AWQ", vllm.Path)
        }
        if vllm.ContextLength != 32768 {
                t.Errorf("vllm.ContextLength: got %d want 32768 (from max_model_len)", vllm.ContextLength)
        }

        // beellama: Path derived from hf_file basename; Quantization from hf_file name.
        beellama, ok := byID["beellama"]
        if !ok {
                t.Fatal("no beellama model in output")
        }
        if beellama.Path != "/models/Qwen3-14B-Q4_K_M.gguf" {
                t.Errorf("beellama.Path: got %q want /models/Qwen3-14B-Q4_K_M.gguf (derived from hf_file)", beellama.Path)
        }
        if beellama.Format != "gguf" {
                t.Errorf("beellama.Format: got %q want gguf", beellama.Format)
        }
        if beellama.Quantization != "q4_k_m" {
                t.Errorf("beellama.Quantization: got %q want q4_k_m (parsed from hf_file)", beellama.Quantization)
        }
        if beellama.Parameters != "14B" {
                t.Errorf("beellama.Parameters: got %q want 14B", beellama.Parameters)
        }
        if beellama.ContextLength != 32768 {
                t.Errorf("beellama.ContextLength: got %d want 32768 (from context_size)", beellama.ContextLength)
        }
        if len(beellama.SupportedRuntimes) != 1 || beellama.SupportedRuntimes[0] != "llamacpp" {
                t.Errorf("beellama.SupportedRuntimes: got %v want [llamacpp]", beellama.SupportedRuntimes)
        }

        // ollama: no model_path; Present=false; Parameters parsed from model name.
        ollama, ok := byID["ollama"]
        if !ok {
                t.Fatal("no ollama model in output")
        }
        if ollama.Path != "" {
                t.Errorf("ollama.Path: got %q want empty (no model_path; pulled on demand)", ollama.Path)
        }
        if ollama.Parameters != "3B" {
                t.Errorf("ollama.Parameters: got %q want 3B (parsed from qwen2.5:3b)", ollama.Parameters)
        }
        if ollama.ContextLength != 0 {
                t.Errorf("ollama.ContextLength: got %d want 0 (no context_length key)", ollama.ContextLength)
        }
        if ollama.Quantization != "" {
                t.Errorf("ollama.Quantization: got %q want empty (ollama quant is internal)", ollama.Quantization)
        }
        if ollama.Present {
                t.Errorf("ollama.Present: got true want false (Path empty → no file to stat)")
        }
        if len(ollama.SupportedRuntimes) != 1 || ollama.SupportedRuntimes[0] != "ollama" {
                t.Errorf("ollama.SupportedRuntimes: got %v want [ollama]", ollama.SupportedRuntimes)
        }
}

func TestCollectModels_PresentTrueWhenFileExists(t *testing.T) {
        // Create a temp dir that mimics /models/ + a fake model file, point
        // models.yaml at it, and verify Present=true for sglang.
        dir := t.TempDir()
        modelPath := filepath.Join(dir, "Qwen3-14B-AWQ")
        if err := os.Mkdir(modelPath, 0755); err != nil {
                t.Fatalf("mkdir %s: %v", modelPath, err)
        }
        // Also drop a fake gguf file for beellama.
        ggufPath := filepath.Join(dir, "Qwen3-14B-Q4_K_M.gguf")
        if err := os.WriteFile(ggufPath, []byte("fake gguf"), 0644); err != nil {
                t.Fatalf("write %s: %v", ggufPath, err)
        }

        yaml := `sglang:
  model_path: "` + modelPath + `"
  context_length: 32768

vllm:
  model_path: "` + modelPath + `"
  max_model_len: 32768

beellama:
  hf_repo: "Qwen/Qwen3-14B-GGUF"
  hf_file: "Qwen3-14B-Q4_K_M.gguf"
  context_size: 32768

ollama:
  model: "qwen2.5:3b"
`
        // Override the "/models/" prefix for beellama's derived path by using
        // an absolute model_path that points at our temp gguf.
        yaml = strings.Replace(yaml, "  hf_file: \"Qwen3-14B-Q4_K_M.gguf\"",
                "  hf_file: \""+ggufPath+"\"", 1)

        path := writeTempYAML(t, yaml)
        withEnv(t, "SCARLIX_MODELS_YAML", path)

        models := CollectModels()
        byID := make(map[string]Model, len(models))
        for _, m := range models {
                byID[m.ID] = m
        }
        if !byID["sglang"].Present {
                t.Errorf("sglang.Present should be true (model_path dir exists)")
        }
        if !byID["vllm"].Present {
                t.Errorf("vllm.Present should be true (model_path dir exists)")
        }
        if !byID["beellama"].Present {
                t.Errorf("beellama.Present should be true (derived /models/<basename> file exists)")
        }
        // ollama has no path → always false.
        if byID["ollama"].Present {
                t.Errorf("ollama.Present should be false (no path)")
        }
}

func TestCollectModels_SkipsAbsentSections(t *testing.T) {
        // If a section is missing from models.yaml, it should be skipped —
        // not emit a zero-value Model with Present=false.
        yaml := `sglang:
  model_path: "/models/Qwen3-14B-AWQ"
  context_length: 32768
# (no vllm, beellama, or ollama sections)
`
        path := writeTempYAML(t, yaml)
        withEnv(t, "SCARLIX_MODELS_YAML", path)

        models := CollectModels()
        if len(models) != 1 {
                t.Fatalf("expected 1 model (sglang only); got %d (%+v)", len(models), models)
        }
        if models[0].ID != "sglang" {
                t.Errorf("expected sglang; got %s", models[0].ID)
        }
}

func TestCollectModels_RealRepoYAML(t *testing.T) {
        // When SCARLIX_REPO points at the actual repo, CollectModels should
        // find the repo-root models.yaml and parse all 4 sections. This is
        // the integration test for the default-path fallback.
        repo := os.Getenv("SCARLIX_REPO")
        if repo == "" {
                // Fall back to a few candidates that should exist in this repo.
                for _, c := range []string{"/home/z/my-project/scarlix-os-v12-repo"} {
                        if _, err := os.Stat(filepath.Join(c, "models.yaml")); err == nil {
                                repo = c
                                break
                        }
                }
        }
        if repo == "" {
                t.Skip("SCARLIX_REPO not set and repo models.yaml not at expected path")
        }

        // Clear SCARLIX_MODELS_YAML so the default-path search kicks in.
        withEnv(t, "SCARLIX_MODELS_YAML", "")
        withEnv(t, "SCARLIX_REPO", repo)

        models := CollectModels()
        if len(models) != 4 {
                t.Fatalf("expected 4 models from repo models.yaml; got %d", len(models))
        }
        // Spot-check: every model has a non-empty ID.
        for _, m := range models {
                if m.ID == "" {
                        t.Errorf("model has empty ID: %+v", m)
                }
                // Capabilities + SupportedRuntimes must be non-nil slices (so
                // they marshal as [] not null).
                if m.Capabilities == nil {
                        t.Errorf("model %s: Capabilities is nil — should be non-nil slice", m.ID)
                }
                if m.SupportedRuntimes == nil {
                        t.Errorf("model %s: SupportedRuntimes is nil — should be non-nil slice", m.ID)
                }
        }
}

// --- parseParameters helper ----------------------------------------------

func TestParseParameters(t *testing.T) {
        cases := []struct {
                input string
                want  string
        }{
                {"/models/Qwen3-14B-AWQ", "14B"},
                {"Qwen/Qwen3-14B-AWQ", "14B"},
                {"Qwen3-14B-Q4_K_M.gguf", "14B"},
                {"qwen2.5:3b", "3B"},
                {"meta-llama/Llama-3.1-8B-Instruct", "8B"},
                {"mixtral-8x7B", "8x7B"},
                {"/models/Mistral-7B-Instruct-v0.3", "7B"},
                {"/models/embeddings", ""},
                {"", ""},
                // Edge case: lowercase suffix normalizes to uppercase.
                {"model-1b", "1B"},
                // Edge case: scientific-notation-like input ("1e5b") — "e" is not
                // a digit, and \b before "5" doesn't match (no word boundary
                // between "e" and "5"), so the whole regex finds no match.
                {"1e5b", ""},
        }
        for _, tc := range cases {
                t.Run(tc.input, func(t *testing.T) {
                        got := parseParameters(tc.input)
                        if got != tc.want {
                                t.Errorf("parseParameters(%q): got %q want %q", tc.input, got, tc.want)
                        }
                })
        }
}

// --- parseGGUFQuant helper -----------------------------------------------

func TestParseGGUFQuant(t *testing.T) {
        cases := []struct {
                input string
                want  string
        }{
                {"Qwen3-14B-Q4_K_M.gguf", "q4_k_m"},
                {"qwen-7b-q5_k_s.gguf", "q5_k_s"},
                {"model-q8_0.gguf", "q8_0"},
                {"model-f16.gguf", "f16"},
                {"model.fp16.gguf", "fp16"},
                {"model-i8.gguf", "i8"},
                {"model-i4.gguf", "i4"},
                {"model.gguf", ""},       // no quant token
                {"", ""},                 // empty input
                {"MODEL-Q3_K_L.GGUF", "q3_k_l"}, // case-insensitive
        }
        for _, tc := range cases {
                t.Run(tc.input, func(t *testing.T) {
                        got := parseGGUFQuant(tc.input)
                        if got != tc.want {
                                t.Errorf("parseGGUFQuant(%q): got %q want %q", tc.input, got, tc.want)
                        }
                })
        }
}

// --- JSON round-trip -----------------------------------------------------

func TestModel_JSONRoundTrip(t *testing.T) {
        // Populate all 10 Model fields with realistic v19 values (matching
        // the data-contract example in docs/SCARLIX_DATA_CONTRACTS.md §4).
        src := Model{
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
        }
        data, err := json.Marshal(src)
        if err != nil {
                t.Fatalf("marshal failed: %v", err)
        }
        j := string(data)

        // Spot-check frozen JSON field names + values from the data contract.
        for _, want := range []string{
                `"id":"qwen3-14b-awq"`,
                `"path":"/models/Qwen3-14B-AWQ"`,
                `"format":"safetensors"`,
                `"quantization":"awq"`,
                `"parameters":"14B"`,
                `"context_length":32768`,
                `"estimated_vram_mb":13600`,
                `"capabilities":["coding","reasoning","chat"]`,
                `"supported_runtimes":["sglang","vllm"]`,
                `"present":true`,
        } {
                if !strings.Contains(j, want) {
                        t.Errorf("Model JSON missing expected substring %s; got: %s", want, j)
                }
        }

        // Round-trip: unmarshal back into a fresh Model, verify field equality.
        var dst Model
        if err := json.Unmarshal(data, &dst); err != nil {
                t.Fatalf("unmarshal failed: %v", err)
        }
        if dst.ID != src.ID || dst.Path != src.Path || dst.Format != src.Format {
                t.Errorf("round-trip mismatch:\n src=%+v\n dst=%+v", src, dst)
        }
        if dst.ContextLength != src.ContextLength || dst.EstimatedVRAM != src.EstimatedVRAM {
                t.Errorf("numeric round-trip mismatch:\n src=%+v\n dst=%+v", src, dst)
        }
}

func TestModel_EmptySlicesMarshalAsNotNull(t *testing.T) {
        // Stability contract §5: empty slices must marshal as [], never null.
        m := Model{ID: "test", Capabilities: []string{}, SupportedRuntimes: []string{}}
        data, err := json.Marshal(m)
        if err != nil {
                t.Fatalf("marshal failed: %v", err)
        }
        if !strings.Contains(string(data), `"capabilities":[]`) {
                t.Errorf("empty Capabilities should marshal as []; got: %s", string(data))
        }
        if !strings.Contains(string(data), `"supported_runtimes":[]`) {
                t.Errorf("empty SupportedRuntimes should marshal as []; got: %s", string(data))
        }
}

// --- helpers --------------------------------------------------------------

// contains is a tiny helper for slice membership (avoids pulling in
// slices.Contains for Go <1.21 compatibility — even though our go.mod
// says 1.23, keeping the test file dependency-free is cleaner).
func contains(s []string, v string) bool {
        for _, x := range s {
                if x == v {
                        return true
                }
        }
        return false
}
