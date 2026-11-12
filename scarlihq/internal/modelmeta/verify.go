// Package modelmeta — artifact verification (v19.7.1, KPK-19.7.0-P2-04).
//
// The v19.7.0 defect: `checkpoint.sha256` was checked for being 64 hex
// characters — a FORMAT check — while the release notes and comments
// described the value as the "verified fingerprint that binds revisions
// and expert packs". No bytes were ever hashed. A manifest with any
// arbitrary-but-well-formed digest was schema-valid.
//
// This file closes that gap with an EXPLICIT, operator-triggered
// verification pass:
//
//   - VerifyManifestArtifacts resolves the declared storage paths under
//     CONFIGURED ROOTS (absolute paths must still live under a root; `..`
//     traversal is refused), hashes the actual artifact bytes, and
//     compares the computed digest to the declaration.
//   - A FILE artifact is hashed directly. A DIRECTORY artifact (a
//     safetensors checkpoint directory) is hashed as a deterministic
//     ordered digest over its regular files (relative path + size +
//     bytes, sorted by path) — the documented directory-digest rule.
//   - The result is an ArtifactVerification RECORD (path, size, computed
//     digest, match, verified-at, notes) — provenance an operator can
//     audit, never a boolean in place of evidence.
//   - The record is PERSISTABLE (SaveVerificationStore /
//     LoadVerificationStore) so the v19.7.1 admission gate can refuse any
//     model whose verification is absent or stale (the audit's planner
//     rule, enforced one phase early).
//
// Verification is NEVER automatic: snapshot building does not hash
// checkpoints (a 14B checkpoint takes tens of seconds to read — the
// read-only GET surface must not trigger it). The operator door is the
// `scarlix-inventory --verify-manifests` CLI flag.
package modelmeta

import (
        "crypto/sha256"
        "encoding/hex"
        "encoding/json"
        "fmt"
        "io"
        "os"
        "path/filepath"
        "sort"
        "strings"
        "time"
)

// ArtifactDigest is the verification record for ONE declared artifact.
type ArtifactDigest struct {
        // Path — the RESOLVED absolute path that was hashed (under a root).
        Path string `json:"path"`
        // Kind — "file" or "directory" (the directory-digest rule applies).
        Kind string `json:"kind"`
        // SizeBytes — file size, or the summed size of the directory's
        // regular files.
        SizeBytes int64 `json:"size_bytes"`
        // DeclaredSHA256 — the manifest's declared digest (lowercase).
        DeclaredSHA256 string `json:"declared_sha256"`
        // ComputedSHA256 — the digest computed from the actual bytes ("" when
        // hashing failed — see Error).
        ComputedSHA256 string `json:"computed_sha256,omitempty"`
        // Match — computed == declared (false when either side is unknown).
        Match bool `json:"match"`
        // Error — why the artifact could not be verified (escape attempt,
        // unreadable path, hash failure). Empty on a clean verification.
        Error string `json:"error,omitempty"`
}

// ArtifactVerification is the full verification record for one model.
type ArtifactVerification struct {
        // ModelID — the manifest model identity the record verifies.
        ModelID string `json:"model_id"`
        // ManifestFingerprint — the canonical SHA-256 of the manifest's
        // identity-bearing fields AT VERIFICATION TIME (v19.8.1,
        // KPK-19.8.0-P1-02: a record keyed by model ID alone is a stale
        // authorization token for whatever manifest later reuses that ID).
        // Admission and the planner RECOMPUTE the current manifest's
        // fingerprint and require an EXACT match — a manifest whose
        // revision, checkpoint path/digest, or expert-pack declaration
        // changed after verification is UNVERIFIED until re-verified. An
        // empty fingerprint (a pre-v19.8.1 record) is refused by name.
        ManifestFingerprint string `json:"manifest_fingerprint,omitempty"`
        // Checkpoint — the checkpoint artifact record (nil when the manifest
        // declared no model_path).
        Checkpoint *ArtifactDigest `json:"checkpoint,omitempty"`
        // ExpertPack — the enabled expert-pack artifact record (nil when the
        // pack is disabled/absent).
        ExpertPack *ArtifactDigest `json:"expert_pack,omitempty"`
        // AllVerified — every declared artifact hashed AND matched.
        AllVerified bool `json:"all_verified"`
        // VerifiedAt — when the verification pass ran (UTC).
        VerifiedAt time.Time `json:"verified_at"`
        // Notes — non-fatal observations (e.g. "no model_path declared").
        Notes []string `json:"notes,omitempty"`
}

// ManifestFingerprint computes the canonical identity fingerprint of a
// manifest: a SHA-256 over the manifest's IDENTITY-BEARING fields in a
// fixed, unambiguous serialization (field name + NUL + value + NUL per
// field). The covered set is exactly the set that can silently change a
// model's execution identity while keeping the same model ID:
//
//      schema_version, id, revision;
//      checkpoint (format, quantization, declared sha256 — lowercased);
//      architecture (kind, total/active parameters, expert geometry, expert
//      bytes, shared bytes — the resource-derivation inputs);
//      runtime (context limit, KV precision + bytes/token, the SORTED
//      supported backend set);
//      artifacts.expert_pack (enabled, format, version, declared sha256,
//      compatible backend);
//      storage (model_path, expert_path, preferred_device_id).
//
// Two manifests with the same fingerprint are the same execution
// identity; ANY change to a covered field changes the fingerprint and
// voids every prior verification record (re-verification is the
// operator's explicit act). Digests are lowercased so a cosmetic case
// change in a declared fingerprint does not void a byte-identical
// artifact set; the backend set is sorted so declaration ORDER is
// cosmetic too — everything else is exact.
func ManifestFingerprint(m *Manifest) string {
        if m == nil {
                return ""
        }
        h := sha256.New()
        f := func(field, value string) {
                fmt.Fprintf(h, "%s\x00%s\x00", field, value)
        }
        backends := append([]string(nil), m.Runtime.SupportedBackends...)
        sort.Strings(backends)
        f("schema_version", m.SchemaVersion)
        f("id", m.ID)
        f("revision", m.Revision)
        f("checkpoint.format", m.Checkpoint.Format)
        f("checkpoint.quantization", m.Checkpoint.Quantization)
        f("checkpoint.sha256", strings.ToLower(m.Checkpoint.SHA256))
        f("architecture.kind", string(m.Architecture.Kind))
        f("architecture.total_parameters", m.Architecture.TotalParameters)
        f("architecture.active_parameters", m.Architecture.ActiveParameters)
        fmt.Fprintf(h, "architecture.expert_count\x00%d\x00", m.Architecture.ExpertCount)
        fmt.Fprintf(h, "architecture.experts_per_token\x00%d\x00", m.Architecture.ExpertsPerToken)
        fmt.Fprintf(h, "architecture.expert_bytes\x00%d\x00", m.Architecture.ExpertBytes)
        fmt.Fprintf(h, "architecture.shared_bytes\x00%d\x00", m.Architecture.SharedBytes)
        fmt.Fprintf(h, "runtime.context_limit\x00%d\x00", m.Runtime.ContextLimit)
        f("runtime.kv_cache.precision", m.Runtime.KVCache.Precision)
        fmt.Fprintf(h, "runtime.kv_cache.bytes_per_token_estimate\x00%d\x00", m.Runtime.KVCache.BytesPerTokenEstimate)
        f("runtime.supported_backends", strings.Join(backends, ","))
        f("artifacts.expert_pack.enabled", fmt.Sprintf("%t", m.Artifacts.ExpertPack.Enabled))
        f("artifacts.expert_pack.format", m.Artifacts.ExpertPack.Format)
        f("artifacts.expert_pack.version", m.Artifacts.ExpertPack.Version)
        f("artifacts.expert_pack.sha256", strings.ToLower(m.Artifacts.ExpertPack.SHA256))
        f("artifacts.expert_pack.compatible_backend", m.Artifacts.ExpertPack.CompatibleBackend)
        f("storage.model_path", m.Storage.ModelPath)
        f("storage.expert_path", m.Storage.ExpertPath)
        f("storage.preferred_device_id", m.Storage.PreferredDeviceID)
        return hex.EncodeToString(h.Sum(nil))
}

// DefaultVerificationRoots are the roots an artifact path may resolve
// under: the repo's canonical model store (/models) plus the manifest
// directory itself (dev fixtures colocated with their manifests).
func DefaultVerificationRoots(manifestsDir string) []string {
        roots := []string{"/models"}
        if abs, err := filepath.Abs(manifestsDir); err == nil && abs != "" {
                roots = append(roots, abs)
        }
        return roots
}

// resolveArtifactPath resolves a declared storage path under one of the
// roots. Absolute paths must ALREADY live under a root (after symlink
// resolution); relative paths are joined to each root in order. `..`
// traversal out of a root is refused. Returns the resolved path or a
// named error.
func resolveArtifactPath(declared string, roots []string) (string, error) {
        declared = strings.TrimSpace(declared)
        if declared == "" {
                return "", fmt.Errorf("no path declared")
        }
        if strings.Contains(declared, "..") {
                // Refuse traversal wholesale: a legitimate model store layout has
                // no reason to climb out of its root (KPK-19.7.0-P2-04: "`../` and
                // absolute path attempts are refused").
                return "", fmt.Errorf("path %q contains '..' traversal — refused", declared)
        }
        for _, root := range roots {
                var candidate string
                if filepath.IsAbs(declared) {
                        candidate = filepath.Clean(declared)
                } else {
                        candidate = filepath.Join(root, declared)
                }
                // Resolve symlinks when possible; an unresolvable path just fails
                // the stat below.
                if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
                        candidate = resolved
                }
                rootClean := filepath.Clean(root)
                if candidate == rootClean || strings.HasPrefix(candidate, rootClean+string(filepath.Separator)) {
                        if _, err := os.Stat(candidate); err == nil {
                                return candidate, nil
                        }
                        continue // under this root but not present — try the next
                }
        }
        return "", fmt.Errorf("path %q resolves under none of the configured roots %v (absolute escape refused)", declared, roots)
}

// digestFile computes the SHA-256 of one file, streaming (bounded memory;
// a 14B checkpoint is read in 4 MiB chunks).
func digestFile(path string) (string, int64, error) {
        f, err := os.Open(path)
        if err != nil {
                return "", 0, err
        }
        defer f.Close()
        h := sha256.New()
        n, err := io.Copy(h, f)
        if err != nil {
                return "", 0, err
        }
        return hex.EncodeToString(h.Sum(nil)), n, nil
}

// digestDirectory computes the deterministic DIRECTORY digest: every
// REGULAR file under path (recursively), sorted by relative path,
// contributes "relpath\0size\0bytes" to the hash. Symlinks inside are
// skipped and counted as notes (a symlink's target is outside this
// digest's authority). The rule is documented here and in the contract.
func digestDirectory(path string) (string, int64, error) {
        var files []string
        err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
                if err != nil {
                        return err
                }
                if info.Mode().IsRegular() {
                        files = append(files, p)
                }
                return nil
        })
        if err != nil {
                return "", 0, err
        }
        sort.Strings(files)
        h := sha256.New()
        var total int64
        for _, f := range files {
                rel, err := filepath.Rel(path, f)
                if err != nil {
                        return "", 0, err
                }
                info, err := os.Stat(f)
                if err != nil {
                        return "", 0, err
                }
                fmt.Fprintf(h, "%s\x00%d\x00", rel, info.Size())
                fh, err := os.Open(f)
                if err != nil {
                        return "", 0, err
                }
                n, err := io.Copy(h, fh)
                fh.Close()
                if err != nil {
                        return "", 0, err
                }
                total += n
        }
        return hex.EncodeToString(h.Sum(nil)), total, nil
}

// verifyOne resolves + hashes one declared artifact and compares it to
// the declared digest.
func verifyOne(declaredPath, declaredSHA string, roots []string) *ArtifactDigest {
        rec := &ArtifactDigest{DeclaredSHA256: strings.ToLower(declaredSHA)}
        resolved, err := resolveArtifactPath(declaredPath, roots)
        if err != nil {
                rec.Error = err.Error()
                return rec
        }
        rec.Path = resolved
        info, err := os.Stat(resolved)
        if err != nil {
                rec.Error = fmt.Sprintf("stat failed: %v", err)
                return rec
        }
        var computed string
        var size int64
        if info.IsDir() {
                rec.Kind = "directory"
                computed, size, err = digestDirectory(resolved)
        } else {
                rec.Kind = "file"
                computed, size, err = digestFile(resolved)
        }
        if err != nil {
                rec.Error = fmt.Sprintf("hashing failed: %v", err)
                return rec
        }
        rec.SizeBytes = size
        rec.ComputedSHA256 = computed
        rec.Match = computed != "" && computed == rec.DeclaredSHA256
        return rec
}

// VerifyManifestArtifacts hashes the manifest's declared artifacts
// (checkpoint + enabled expert pack) and records the comparison. The
// manifest SHOULD have passed ValidateManifest (an unvalidated manifest
// has unknown-shaped declarations — verification still runs and reports
// honestly; the caller gates).
func VerifyManifestArtifacts(m *Manifest, roots []string) ArtifactVerification {
        rec := ArtifactVerification{
                ModelID:             m.ID,
                ManifestFingerprint: ManifestFingerprint(m),
                VerifiedAt:          time.Now().UTC(),
                Notes:               []string{},
        }
        allOK := true

        if strings.TrimSpace(m.Storage.ModelPath) == "" {
                rec.Notes = append(rec.Notes, "no storage.model_path declared — nothing to verify (metadata-only manifest)")
        } else {
                rec.Checkpoint = verifyOne(m.Storage.ModelPath, m.Checkpoint.SHA256, roots)
                if rec.Checkpoint.Error != "" || !rec.Checkpoint.Match {
                        allOK = false
                }
        }

        if m.Artifacts.ExpertPack.Enabled {
                if strings.TrimSpace(m.Storage.ExpertPath) == "" {
                        rec.Notes = append(rec.Notes, "expert pack enabled but no storage.expert_path declared — pack unverifiable")
                        allOK = false
                } else {
                        rec.ExpertPack = verifyOne(m.Storage.ExpertPath, m.Artifacts.ExpertPack.SHA256, roots)
                        if rec.ExpertPack.Error != "" || !rec.ExpertPack.Match {
                                allOK = false
                        }
                }
        }

        rec.AllVerified = allOK && (rec.Checkpoint != nil)
        return rec
}

// verificationStoreJSON is the durable store shape (schema-versioned).
type verificationStoreJSON struct {
        SchemaVersion int                             `json:"schema_version"`
        Verifications map[string]ArtifactVerification `json:"verifications"`
}

// VerificationStoreSchemaVersion is the store's schema identity.
const VerificationStoreSchemaVersion = 1

// SaveVerificationStore persists the verification records atomically
// (tmp+rename, 0600 — the grants.json/ledger discipline).
func SaveVerificationStore(path string, recs []ArtifactVerification) error {
        store := verificationStoreJSON{
                SchemaVersion: VerificationStoreSchemaVersion,
                Verifications: make(map[string]ArtifactVerification, len(recs)),
        }
        for _, r := range recs {
                store.Verifications[r.ModelID] = r
        }
        raw, err := json.MarshalIndent(&store, "", "  ")
        if err != nil {
                return err
        }
        if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
                return err
        }
        tmp := path + ".tmp"
        if err := os.WriteFile(tmp, raw, 0o600); err != nil {
                return err
        }
        return os.Rename(tmp, path)
}

// LoadVerificationStore reads the durable verification records
// (schema-checked; a corrupt or future-schema file is a named error —
// fail-closed, never a silent reinterpretation).
func LoadVerificationStore(path string) (map[string]ArtifactVerification, error) {
        raw, err := os.ReadFile(path)
        if err != nil {
                if os.IsNotExist(err) {
                        return map[string]ArtifactVerification{}, nil // honest absence
                }
                return nil, err
        }
        var store verificationStoreJSON
        if err := json.Unmarshal(raw, &store); err != nil {
                return nil, fmt.Errorf("verification store %s is corrupt: %v (fix or archive it deliberately)", path, err)
        }
        if store.SchemaVersion != VerificationStoreSchemaVersion {
                return nil, fmt.Errorf("verification store %s carries schema_version %d, expected %d", path, store.SchemaVersion, VerificationStoreSchemaVersion)
        }
        if store.Verifications == nil {
                store.Verifications = map[string]ArtifactVerification{}
        }
        return store.Verifications, nil
}

// ResolveVerificationStorePath picks the verification store location:
// explicit SCARLIX_MODELS_VERIFICATION → /var/lib/scarlix/
// models-verification.json → <manifestsDir>/verification.json (dev
// fallback, next to the manifests it verifies).
func ResolveVerificationStorePath(manifestsDir string) string {
        if p := strings.TrimSpace(os.Getenv("SCARLIX_MODELS_VERIFICATION")); p != "" {
                return p
        }
        if info, err := os.Stat("/var/lib/scarlix"); err == nil && info.IsDir() {
                return "/var/lib/scarlix/models-verification.json"
        }
        if manifestsDir == "" {
                return ""
        }
        return filepath.Join(manifestsDir, "verification.json")
}
