# ScarLiX OS v19.8.0 — Compute Fabric Phase 4: InferSSD Adapter + SGLang Expert Pack (experimental)

**Release:** v19.8.0
**Commit:** [`095a690edfe267b3f7a99cc7d2fbb3645c1fe047`](https://github.com/MoZoHuJa/OS/commit/095a690edfe267b3f7a99cc7d2fbb3645c1fe047) — recorded at packaging (the release-notes genre: the hash lands with the tag)
**Baseline commit (pre-change):** `9a95f3f` (v19.7.2 release-notes hash record)
**Date:** 2026-11-08
**Scope:** Compute Fabric **Phase 4 — InferSSD and SGLang Expert Pack**
(roadmap §8 of
`docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`,
the in-repo implementation plan to v20.0.0)

The release sequence toward v20 continues: v19.6.8 (stabilization —
DONE) → v19.7.0 (unified resource inventory — DONE) → v19.7.1
(resource vector + aggregate admission — DONE) → v19.7.2 (backend
capability contracts + the deterministic execution planner — DONE) →
**v19.8.0 (this release: the InferSSD adapter + the SGLang Expert
Pack, experimental, default OFF)** → v19.9.0 (distributed fabric) →
v20.0.0 (production control plane). Release labels ≥ v19.9.0 remain
planning labels, not commitments.

## Scope

This release executes Phase 4 of the Unified Compute Fabric roadmap:
the **InferSSD backend adapter** (roadmap §8.3's structure — a DISTINCT
inference backend for SSD-backed MoE expert execution, with its own
runtime profile, health probe contract, model compatibility
declaration, storage requirements, and resource estimation) and the
**SGLang Expert Pack modeled at the artifact tier** (§8.1's conceptual
split — an artifact/capability mode of SGLang, never a fifth runtime
provider). The release's must-not-do is honored **at the pack
manifest's validation**: *do not claim cross-engine pack compatibility*
— a pack declaring any backend other than sglang is refused BY NAME.
Everything §8 ships **experimental and default OFF** (§8.8's initial
status): no enabled runtime profile ships, no infer_ssd runtime is
observed anywhere, and the planner refuses every infer_ssd plan in
every default deployment until the operator's own enablement act
follows the compatibility matrix and the target-host acceptance.

## Changes

### 1. The InferSSD adapter (`scarlihq/internal/backends/infer_ssd` — NEW)

Roadmap §8.3's suggested structure, realized:

- **`adapter.go`** — the `BackendAdapter` boundary (the roadmap's
  interface, names adapted per §0's adaptation rule: `EstimateRequest`
  and `PlanBinding` are adapter-local projections of the planner's
  §7.4 contracts, because this package must not import the planner —
  the planner imports THIS package for the profile type + the strategy
  constant + the qualification checks; the acyclic split keeps both
  reviewable). The adapter **never calls Docker** (asserted
  comment-stripped at the source level): `BuildProfile` returns a
  validated `ApprovedRuntimeProfile` — the launch shape for the
  workload/Host Bridge path. Construction is fail-closed on a
  nil/disabled/invalid profile (an adapter over a disabled profile is
  a phantom capability).
- **`config.go`** — the runtime profile contract
  (`scarlix.runtime-profile/v1`, §8.4 field-for-field) + the
  fail-closed loader. **Placeholder values are REFUSED BY NAME**
  (§8.4's own rule: "Never enable a profile containing placeholder
  values. The configuration loader must reject them" — REPLACE_WITH /
  VERIFIED_ markers scanned on every string field, embedded ones
  included). The image is digest-pinned; `experimental: true` is
  REQUIRED (a non-experimental profile is a production claim without
  hardware acceptance); `max_concurrent_requests` is EXACTLY 1
  (§8.8's one-active-workload rule — relaxing it is a §8.6
  contention-evidence act, never a config edit); the GPU mode is
  exclusive; published ports are refused (a port is a new network
  surface); a DISABLED profile is honest documentation.
- **`manifest.go`** — the §8.2 model compatibility declaration:
  `BindModel` (the profile binds ONE exact model × checkpoint —
  safetensors, MoE with known geometry, the exact id + fingerprint
  match) + `ExpertStorageBytes` (the paged experts' bytes). The
  profile's context ceiling is deliberately NOT checked here — it is
  a REQUEST-level bound (a model DECLARING a larger capability is
  legitimately servable at smaller contexts; conflating the two would
  make the request-level refusal dead code).
- **`requirements.go`** — the §8.6 measured SSD qualification
  (`QualifySSD`: sequential MB/s + random IOPS + p95 latency floors +
  free bytes against the device's RECORDED benchmark — a
  measured-performance floor without a measurement is a named refusal)
  + the §8.2 GPU compatibility join (`GPUCompatibility`: the OBSERVED
  compute capability ∈ the declared capabilities with the `12.0` →
  `sm_120` normalization (the `9.0a` architectural suffix handled),
  the driver CUDA matches the declared CUDA EXACTLY — the tested
  combination is the approved combination — and the driver meets the
  declared minimum with numeric-aware comparison; an unparseable
  observation is a named refusal, never a guess).
- **`health.go`** — the §8.4 health probe contract behind an
  injectable seam: `HTTPHealthProber` (no redirects — the airt
  discipline; 200 = healthy, everything else is the failure it is),
  and the endpoint URL is bound by the HOST BRIDGE path (a probe
  without a bound endpoint is a named refusal — the adapter never
  assigns endpoints).
- **`capabilities.go`** — the strategy constant
  (`ssd-backed-moe`), the backend identity, and the adapter's own
  model-compatibility claims mirrored from the closed registry.

### 2. The storage benchmark's random-read phase (roadmap §8.6)

`RunReadBenchmark` gains the OPT-IN `RandomReads` phase (a v19.7.0-shaped
request measures sequential throughput only, byte-for-byte unchanged):
random reads at a bounded block (default 4 KiB — the
expert-paging-relevant size; out-of-bound values are NAMED errors,
never clamps) over the SAME warmed temp file, with a FIXED-SEED
reproducible offset sequence, synchronous queue depth 1 (recorded,
never implied), measuring **IOPS + the p50/p95/p99 latency percentiles**
(nearest-rank). `ReadProfile` gains `RandomReadP50Ms`,
`RandomReadP99Ms`, and `RandomBlockBytes` (additive, omitempty — the
v2 snapshot JSON contract is unchanged for sequential-only profiles).

### 3. The planner's ssd-backed-moe strategy (`internal/planner`)

The closed strategy vocabulary gains exactly ONE paging member —
`ssd-backed-moe` (infer_ssd only, behind an enabled runtime profile
with a measured qualification). The SGLang Expert Pack's
`moe_expert_pack` execution mode stays **UNPLANNED**: the §8.8
enablement policy names its prerequisites (pack-byte verification +
the §8.7 target-host acceptance matrix), and an ENABLED pack
declaration on a model rides the plan's explanation as the generated
`expert-pack-declared-not-planned` entry — the resident strategy IS
the plan.

The InferSSD execution class has its own hard-constraint chain
(`evaluateInferSSD`, reached only AFTER the placement × runtime join
observes an infer_ssd runtime running + healthy):

| # | Constraint | Named refusal |
|---|---|---|
| 1 | the enabled runtime profile (§8.8) | `plan-ssd-profile-unconfigured` |
| 2 | the §8.2 model binding | `plan-ssd-profile-model-mismatch` |
| 3 | the profile's serving ceiling (request-level) | `plan-context-exceeds-profile-limit` |
| 4 | the profile's digest-pinned image | `plan-image-not-digest-pinned` |
| 5 | the §8.2 GPU join + admission-shaped VRAM | `plan-ssd-gpu-incompatible` / `plan-gpu-insufficient` |
| 6 | the §8.6 measured qualification | `plan-ssd-device-unknown` / `plan-ssd-unqualified` / `plan-ssd-storage-insufficient` |

The ssd plan carries the profile as its **config profile** (the
field's first real value — v19.7.2's honest empty stays for the
resident strategies), the §8.8-capped concurrency (a generated
explanation entry, never a silent edit of the operator-visible ask),
the storage demand WITH the profile's measured floors (admission
revalidates the same facts), and the generated §8 explanation entries
(`ssd-profile-bound`, `ssd-gpu-compatibility-joined`,
`ssd-qualified-measured`, `ssd-concurrency-capped`). The refusal
vocabulary grows to 22 machine-readable codes (the 7 new §8 codes are
additive; no rename). The policy version moves to `planner/v19.8.0`,
and the policy gains the `SSDProfile` field (constructor-injected,
validated + ENABLED-checked fail-closed — a disabled profile in the
policy is a named wiring bug).

### 4. The SGLang Expert Pack at the artifact tier (`internal/modelmeta` — NEW)

The `scarlix.artifact/v1` manifest (`ExpertPackManifest` +
`LoadExpertPacks` + `ValidateExpertPackManifest` + `BindExpertPacks`):

- **Binds to the EXACT checkpoint** (§8.5: "The manifest must bind the
  pack to the exact checkpoint. Reject mismatches"): model.id +
  revision + checkpoint_sha256 must match a loaded
  `scarlix.model/v2` manifest, and the model's own
  `artifacts.expert_pack` declaration must AGREE (enabled, the same
  compatible_backend, the same sha256/format/version — two files
  disagreeing about one artifact is a collision, not a merge).
- **backend_id is `sglang` ONLY** — a pack for any other backend is
  REFUSED BY NAME: *cross-engine pack compatibility is not claimed*
  (this release's must-not-do, ENFORCED at the validation, never
  warned about and loaded anyway).
- Placeholders refused (the §8.2/§8.4 rule applied to the artifact
  tier — an unknown compatibility fact never becomes a capability
  claim); the pack sha256 is the DECLARED fingerprint (the
  KPK-19.7.0-P2-04 discipline: JSON says `declared_sha256`); the
  container digest is `@sha256:`-pinned; `total_bytes > 0`; duplicate
  artifact ids are rejected as a GROUP (the KPK-19.7.0-P2-04
  collision discipline — no file-order winner).
- `models.d/example-moe-model.pack.yaml` — the DELIBERATELY INVALID
  example that documents the schema (the example model manifest's
  genre: placeholders fail validation BY DESIGN; the loader reports
  the named reasons).

### 5. The registry flip (`internal/airuntime/backends.go`)

sglang's `SupportsExpertPack` is TRUE **with the evidence note** (the
§8.5 manifest binding is the capability claim's evidence; pack
EXECUTION stays unplanned — the §8.8 enablement policy, never a
silent capability upgrade). Every other row keeps
`SupportsExpertPack=false` (the pack is a SGLang artifact — §8.1's
split). infer_ssd's `AdapterNote` evolves from the v19.7.2
declared-future marker to the honest v19.8.0 status (experimental
§8.3 planning-side adapter, execution through the Host Bridge path,
still refused by the presence join in every default deployment), and
`ProviderForBackend(infer_ssd)` stays the honest named error — no
TRANSPORT provider (it is not an HTTP chat runtime).

### 6. The operator surfaces

- **`SCARLIHQ_INFERSSD_PROFILE`** — one runtime profile file, loaded +
  validated + placeholder-refused fail-closed at startup (a malformed
  profile is a startup error, never a per-request surprise); a
  DISABLED profile is the honest not-configured.
- **The startup log's honest three-state line**: not configured /
  disabled documentation / enabled-and-experimental.
- **`GET /api/compute/plan`** gains the `ssd_profile` compact audit
  view (JSON null when not configured — the honest absence) and the
  `expert_packs` view (bound + unbound verdicts; omitted entirely
  without a loader — never an empty masquerade). No new routes: the
  planner surface stays the READ-gated two-method route.
- **The pack startup summary** — loaded / checkpoint-bound / invalid
  counts, with the named reasons for every invalid entry (the
  LoadManifests genre).

## Verification

- `gofmt` clean; `go vet ./...` clean.
- `go test ./...` — **all 30 packages OK** (29 + the new infer_ssd).
- `go test -race` (planner + infer_ssd + storage + airuntime + api +
  modelmeta + admission) — clean.
- Registry classification 68/68 (unchanged).
- `bash -n` clean on install.sh + the smoke script.
- `smoke --offline` — **40 checks, 0 FAIL** — the new check_40 with
  THREE verified negative controls:
  1. removing the cross-engine refusal from the pack validation FAILs
     check_40 (the enforced must-not-do vanishes with the code);
  2. removing the placeholder rejection from the runtime profile
     loader FAILs check_40 (§8.4's loader rule);
  3. removing the `plan-ssd-profile-unconfigured` branch (the §8.8
     default) from the planner FAILs check_40.
- check_39 evolved (the `ProviderForBackend(infer_ssd)` marker moved
  with the honest v19.8.0 status — no transport provider,
  planning-side adapter) + check_28 evolved (current-release README
  surface → v19.8.0; the v19.7.2 surface stays as the permanent
  record) + check_37 evolved (the VERSION guard → 19.8.0 + the
  five-place fallback discipline).

## Known limitations (named, not hidden)

- **Hardware acceptance stays OPEN** (KPK-06): CI green ≠ hardware
  verified; §8.7's target-host acceptance matrix (the comparative
  model serving acceptance — baseline SGLang vs SGLang+Pack vs
  InferSSD) remains the operator's recorded act, and the §8.8
  enablement policy requires it before any production scheduling.
- **No enabled runtime profile ships** — every default deployment
  refuses every infer_ssd plan (`plan-ssd-profile-unconfigured` even
  if a runtime were observed; the presence join refuses first in
  practice because no infer_ssd runtime exists anywhere yet). The
  operator's enablement act is: write the validated, digest-pinned,
  placeholder-free profile; run the §8.6 qualification benchmark
  (with the random-read phase); record the §8.2 compatibility matrix.
- **Pack-byte verification is named future work**: the
  `scarlix.artifact/v1` digests are DECLARATIONS (the JSON says
  `declared_sha256`); the pack-byte verification machinery + the §8.7
  comparative acceptance are the named prerequisites before pack
  EXECUTION is ever planned (the §8.8 policy). The planner keeps the
  `moe_expert_pack` strategy unplanned and answers an enabled
  declaration with the generated `expert-pack-declared-not-planned`
  entry.
- **The one-active-workload-per-SSD-profile ledger is the Host Bridge
  integration's job**: the planner carries the §8.8 cap (exactly 1)
  and admission enforces the GPU/storage sides; a per-profile
  workload-count ledger arrives with the Host Bridge execution path.
- **The benchmark's queue depth is 1** (synchronous single reader —
  recorded, never implied): a queued-depth sweep is a future
  diagnostic; §8.6's "queue depth sensitivity" dimension stays an
  operator-side procedure until then.
- **The InferSSD transport does not exist**: `infer_ssd` is not an
  HTTP chat runtime and never enters the provider failover chain;
  execution flows through the Host Bridge path when that integration
  lands.
- **Hardware-specific §8.6 measurements need the real target device**:
  the benchmark measures what it measures (warm-cache, single-queue
  random reads at the requested block size); the operator derives the
  conservative allocatable I/O budget from THEIR runs and declares it
  as the profile's floors — the qualification compares
  measured-vs-declared, never advertised-vs-hoped.

## Post-release note: the Pi-Bolt installer pin drift (external, remediated on main)

The `Pin Freshness` CI job on the v19.8.0 tag failed with a REAL
external finding, caught exactly as designed (KPK-19.2.13-P2-01): the
upstream Pi-Bolt installer at `https://pi-bolt.opensec.in/install.sh`
changed — the live SHA-256
(`91918786f9341ef7d5ef1bf1db6fabeaffdf0434835c9412055bbc5c5fedac39`)
no longer matched the shipped pin
(`e5ac52303cf213aeac1e4cf842edfeac63b1472c3061fe090e41d828a0a1cef0`,
last verified 2026-10-27). This is upstream's release act, not a
repository regression — no ScarLiX code or configuration changed the
pin.

The remediation followed the check's own instruction — refresh
DELIBERATELY: the new installer was downloaded and inspected
(the documented installer structure unchanged: release download +
SHA-256 checksum + signature verification against the release public
key, install to `~/.pi-bolt`, npm-registry/GitHub sources only, no
`curl | sh` execution patterns, no obfuscation, no unexpected
destinations), the pin file
`files/usr/local/share/scarlix/pi-bolt-install.sha256` was recomputed
and refreshed, and the "last verified" dates in README.md and
`docs/SCARLIX_SECURITY_MAP.md` moved to 2026-11-08 — a follow-up
commit on main immediately after the release, recorded here by the
v19.3.2 post-release-note precedent. The main `CI` workflow on the
tag is unaffected (it does not run the pin-freshness job); the weekly
scheduled job goes green again from the remediation commit.
