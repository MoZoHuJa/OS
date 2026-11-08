// Package modelmeta tests — artifact verification (v19.7.1,
// KPK-19.7.0-P2-04): a well-formed-but-wrong digest never becomes
// "verified"; the computed digest, path, and size are recorded; escapes
// are refused; duplicate IDs are group-rejected.
package modelmeta

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeArtifact creates a file with the given content and returns its
// real SHA-256 hex digest.
func writeArtifact(t *testing.T, dir, name, content string) (string, string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return p, hex.EncodeToString(sum[:])
}

// validManifestFor builds a minimal schema-valid manifest with the given
// model path + checkpoint digest.
func validManifestFor(id, modelPath, digest string) *Manifest {
	m := &Manifest{
		SchemaVersion: SchemaVersionV2,
		ID:            id,
		Revision:      "test-rev",
	}
	m.Checkpoint.Format = "safetensors"
	m.Checkpoint.SHA256 = digest
	m.Architecture.Kind = ArchitectureDense
	m.Architecture.SharedBytes = 1024
	m.Runtime.ContextLimit = 4096
	m.Runtime.SupportedBackends = []string{"sglang"}
	m.Storage.ModelPath = modelPath
	return m
}

func TestVerifyManifestArtifacts_FileMatch(t *testing.T) {
	dir := t.TempDir()
	p, digest := writeArtifact(t, dir, "model.safetensors", "checkpoint-bytes")
	m := validManifestFor("test-model", p, digest)

	rec := VerifyManifestArtifacts(m, []string{dir})
	if !rec.AllVerified {
		t.Fatalf("AllVerified=false, want true: %+v", rec)
	}
	if rec.Checkpoint == nil || !rec.Checkpoint.Match {
		t.Fatalf("checkpoint record=%+v, want match", rec.Checkpoint)
	}
	if rec.Checkpoint.ComputedSHA256 != digest {
		t.Errorf("computed=%q, want the real digest %q", rec.Checkpoint.ComputedSHA256, digest)
	}
	if rec.Checkpoint.Path != p || rec.Checkpoint.Kind != "file" || rec.Checkpoint.SizeBytes != int64(len("checkpoint-bytes")) {
		t.Errorf("record provenance incomplete: %+v", rec.Checkpoint)
	}
	if rec.VerifiedAt.IsZero() {
		t.Errorf("VerifiedAt zero — the verification time must be recorded")
	}
}

// TestVerifyManifestArtifacts_WrongDigest (KPK-19.7.0-P2-04 acceptance):
// a valid-SHAPED but wrong digest does NOT become verified.
func TestVerifyManifestArtifacts_WrongDigest(t *testing.T) {
	dir := t.TempDir()
	p, _ := writeArtifact(t, dir, "model.safetensors", "checkpoint-bytes")
	wrong := strings.Repeat("a", 64) // well-formed, wrong
	m := validManifestFor("test-model", p, wrong)

	rec := VerifyManifestArtifacts(m, []string{dir})
	if rec.AllVerified {
		t.Fatal("AllVerified=true for a wrong digest — never")
	}
	if rec.Checkpoint == nil || rec.Checkpoint.Match {
		t.Fatal("a well-formed-but-wrong digest must not match")
	}
	if rec.Checkpoint.ComputedSHA256 == "" {
		t.Errorf("the COMPUTED digest must still be recorded (evidence, not a boolean)")
	}
}

// TestVerifyManifestArtifacts_DirectoryDigest: a directory artifact is
// hashed with the documented ordered rule.
func TestVerifyManifestArtifacts_DirectoryDigest(t *testing.T) {
	dir := t.TempDir()
	modelDir := filepath.Join(dir, "qwen-checkpoint")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "a.safetensors"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "b.safetensors"), []byte("bbbb"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Compute the expected digest with the SAME rule: sorted relpaths,
	// "rel\0size\0" + bytes.
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00", "a.safetensors", 3)
	h.Write([]byte("aaa"))
	fmt.Fprintf(h, "%s\x00%d\x00", "b.safetensors", 4)
	h.Write([]byte("bbbb"))
	expected := hex.EncodeToString(h.Sum(nil))

	m := validManifestFor("test-model", modelDir, expected)
	rec := VerifyManifestArtifacts(m, []string{dir})
	if !rec.AllVerified || rec.Checkpoint == nil || !rec.Checkpoint.Match {
		t.Fatalf("directory digest mismatch: %+v", rec.Checkpoint)
	}
	if rec.Checkpoint.Kind != "directory" || rec.Checkpoint.SizeBytes != 7 {
		t.Errorf("directory record wrong: %+v", rec.Checkpoint)
	}
}

// TestVerifyManifestArtifacts_EscapeRefused (KPK-19.7.0-P2-04 acceptance):
// `../` traversal and absolute paths outside the roots are refused.
func TestVerifyManifestArtifacts_EscapeRefused(t *testing.T) {
	outside := t.TempDir()
	p, _ := writeArtifact(t, outside, "model.safetensors", "x")
	dir := t.TempDir() // the configured root — does NOT contain p

	// Absolute path outside the roots.
	m := validManifestFor("test-model", p, strings.Repeat("0", 64))
	rec := VerifyManifestArtifacts(m, []string{dir})
	if rec.Checkpoint == nil || rec.Checkpoint.Error == "" {
		t.Fatalf("absolute path outside the roots must be refused with a named error: %+v", rec.Checkpoint)
	}
	if rec.AllVerified {
		t.Error("AllVerified=true for an escaped path")
	}

	// Traversal.
	m2 := validManifestFor("test-model", "../../../etc/passwd", strings.Repeat("0", 64))
	rec2 := VerifyManifestArtifacts(m2, []string{dir})
	if rec2.Checkpoint == nil || rec2.Checkpoint.Error == "" {
		t.Fatalf("'..' traversal must be refused: %+v", rec2.Checkpoint)
	}
}

// TestVerifyManifestArtifacts_ExpertPackMismatch (KPK-19.7.0-P2-04
// acceptance): an enabled expert pack with wrong bytes fails the record.
func TestVerifyManifestArtifacts_ExpertPackMismatch(t *testing.T) {
	dir := t.TempDir()
	modelPath, modelDigest := writeArtifact(t, dir, "model.safetensors", "checkpoint")
	packPath, _ := writeArtifact(t, dir, "pack.bin", "pack-bytes")

	m := validManifestFor("test-model", modelPath, modelDigest)
	m.Artifacts.ExpertPack.Enabled = true
	m.Artifacts.ExpertPack.Format = "sglang-pack"
	m.Artifacts.ExpertPack.Version = "v1"
	m.Artifacts.ExpertPack.SHA256 = strings.Repeat("b", 64) // wrong
	m.Artifacts.ExpertPack.CompatibleBackend = "sglang"
	m.Storage.ExpertPath = packPath

	rec := VerifyManifestArtifacts(m, []string{dir})
	if rec.AllVerified {
		t.Fatal("AllVerified=true with a mismatched expert pack")
	}
	if rec.ExpertPack == nil || rec.ExpertPack.Match {
		t.Fatalf("expert pack record=%+v, want mismatch", rec.ExpertPack)
	}
}

// TestLoadManifestsDuplicateIDs (KPK-19.7.0-P2-04 acceptance): duplicate
// model IDs across files → BOTH invalid with a named collision reason;
// no winner is chosen.
func TestLoadManifestsDuplicateIDs(t *testing.T) {
	dir := t.TempDir()
	digest := hex.EncodeToString(make([]byte, 32))
	body := func() string {
		return fmt.Sprintf("schema_version: %s\nid: dup-model\nrevision: r1\ncheckpoint:\n  format: safetensors\n  sha256: %s\narchitecture:\n  kind: dense\n  shared_bytes: 1024\nruntime:\n  context_limit: 4096\n  supported_backends: [sglang]\nstorage:\n  model_path: /models/dup\n", SchemaVersionV2, digest)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.manifest.yaml"), []byte(body()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.manifest.yaml"), []byte(body()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := LoadManifests(dir)
	if len(res.Manifests) != 0 {
		t.Fatalf("manifests=%d, want 0 (duplicate IDs are group-rejected)", len(res.Manifests))
	}
	if len(res.Invalid) != 2 {
		t.Fatalf("invalid=%d, want 2 (BOTH files invalid)", len(res.Invalid))
	}
	for _, inv := range res.Invalid {
		if len(inv.Reasons) != 1 || !strings.Contains(inv.Reasons[0], "duplicate model id") {
			t.Errorf("reasons=%v, want the named collision reason", inv.Reasons)
		}
		if !strings.Contains(inv.Reasons[0], "a.manifest.yaml") || !strings.Contains(inv.Reasons[0], "b.manifest.yaml") {
			t.Errorf("the collision reason must name BOTH paths: %v", inv.Reasons)
		}
	}
}

// TestValidateManifestSafeModelID (KPK-19.7.0-P2-04): model IDs are a
// closed safe identifier set — path-shaped or arbitrary text is refused.
func TestValidateManifestSafeModelID(t *testing.T) {
	digest := hex.EncodeToString(make([]byte, 32))
	for _, id := range []string{"../../etc/passwd", "Model With Spaces", "UPPER", "-leading-dash", ""} {
		m := validManifestFor(id, "/models/x", digest)
		if reasons := ValidateManifest(m); len(reasons) == 0 {
			t.Errorf("id %q must be refused by the safe-identifier rule", id)
		}
	}
	for _, id := range []string{"qwen3-14b-awq", "llama-3.1-8b", "m0", "a.b_c-d"} {
		m := validManifestFor(id, "/models/x", digest)
		if reasons := ValidateManifest(m); len(reasons) != 0 {
			t.Errorf("id %q must be valid, got %v", id, reasons)
		}
	}
}

// TestVerificationStoreRoundTrip: the durable store survives a
// save/load cycle with the schema check.
func TestVerificationStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p, digest := writeArtifact(t, dir, "m.bin", "bytes")
	m := validManifestFor("round-trip", p, digest)
	rec := VerifyManifestArtifacts(m, []string{dir})

	store := filepath.Join(dir, "verification.json")
	if err := SaveVerificationStore(store, []ArtifactVerification{rec}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadVerificationStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := loaded["round-trip"]; !ok || !got.AllVerified || got.Checkpoint == nil || got.Checkpoint.Path != p {
		t.Fatalf("round trip lost the record: %+v ok=%v", got, ok)
	}
	// 0600 discipline.
	if info, err := os.Stat(store); err == nil && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("store perms=%v, want 0600", info.Mode().Perm())
	}
}
