# ScarLiX OS v19.7.0 — Compute Fabric Phase 1: Unified Resource Inventory + Model Execution Metadata

**Release:** v19.7.0
**Commit:** `TBD` — recorded at packaging (the release-notes genre: the hash lands with the tag)
**Baseline commit (pre-change):** `b37ef93` (v19.6.8 release-notes hash record)
**Date:** 2026-11-08
**Scope:** Compute Fabric **Phase 1 — Unified Resource Inventory and Model
Execution Metadata** (roadmap §5 of
`docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`,
the in-repo implementation plan to v20.0.0)

The release sequence toward v20 continues: v19.6.8 (stabilization — DONE)
→ **v19.7.0 (this release: unified resource + storage inventory + model
execution metadata)** → v19.7.1 (resource vector + aggregate admission) →
v19.7.2 (backend capability contracts + deterministic planner) → v19.8.0
(InferSSD + SGLang Expert Pack, experimental) → v19.9.0 (distributed
fabric) → v20.0.0 (production control plane). Release labels ≥ v19.7.1
remain planning labels, not commitments.

## Scope

This release executes Phase 1 of the Unified Compute Fabric roadmap: the
**storage inventory package**, the **model execution metadata package +
verified manifest contract**, the **v2 unified snapshot envelope with
freshness and per-source provenance**, and the **read-only API + CLI
surface**. The release's must-not-do is honored in the code: nothing in
this release schedules from unverified estimates — the inventory observes,
the placement gate refuses unknowns, and the planner that will consume
both is v19.7.2's work.

## Changes

### 1. The storage inventory package (`scarlihq/internal/storage` — NEW)

Roadmap §5.2, field-for-field: `DeviceKind` (nvme/sata/usb/network/
unknown), `ReadProfile` (provenance-carrying benchmark record),
`Device`, `HostResources` (§5.3), `SourceStatus`.

- **Read-only discovery** from `/proc/self/mounts` + `/sys/block` +
  `/sys/class/nvme` + `statfs(2)`: no exec, no writes, no benchmarks —
  the charter is asserted at the SOURCE level
  (`TestDiscoveryCharterNoExec` strips comments before asserting — prose
  cannot mask code) and in the smoke check.
- **Honest transport classification:** USB vs SATA is discriminated by
  the `/sys` device path containing a USB segment; virtio/mmc and
  unresolvable transports are `unknown` — never a guess.
- **Identity without serials:** `StableID` is a SHA-256 DIGEST of the
  identity material (16 hex, `dev.` prefix) — the raw serial is never
  emitted (roadmap §5.2 rule 5). Network mounts digest `fstype:source`.
- **No phantom capacity:** mounted devices report FILESYSTEM capacity;
  unmounted devices report RAW sysfs capacity with `available_bytes: 0`
  (no filesystem → free space unobservable — an LVM PV can never be
  mistaken for admission capacity). Stacked layers (`dm-*`, `md*`,
  `loop*`, `zram`) and virtual filesystems (tmpfs/proc/overlay…) are
  excluded by policy — the exclusions are documented, not silent.
- **Unknown ≠ zero:** `temperature_c` is a pointer (`null` = not
  measured, omitted from JSON); a zero `ReadProfile` means "never
  benchmarked", never "zero performance".
- **Host resources:** CPU threads/model, RAM total/available (kernel
  MemAvailable), swap REPORTED-never-ADMITTED (roadmap §5.3 policy in
  the type docs); `ram_pinned_bytes` documented as Phase 2's
  ledger property (zero = no figure reported, not none pinned).

### 2. The opt-in, bounded storage benchmark

`storage.RunReadBenchmark` + the `scarlix-inventory
--storage-benchmark <dir> --duration .. --size .. [--block ..]` door —
the ONLY benchmark caller in the repository:

- **Bounds fail-closed with named errors** (duration 1s..30s, size
  32MiB..1GiB, block 4KiB..8MiB); required options have NO defaults.
- Writes ONE identifiable temp file (`.scarlix-bench-<id>`), warms it,
  measures, REMOVES it (cancellation included). The target choice is
  the operator's responsibility (roadmap rule 4 — never benchmark a
  production model store without approval).
- The result is a data point with full provenance; the Detail carries
  the honesty note ("not a guaranteed sustained rate"; page-cache
  residency is part of what an unprivileged measurement sees).
- **Discovery never calls it** — asserted at the source level
  (`TestBenchmarkSeamIsolation`) and in the smoke check.

### 3. The model execution metadata package (`scarlihq/internal/modelmeta` — NEW)

Roadmap §5.4/§5.5: `Architecture` (dense/moe/other), `ModelExecution`
(the §5.4 field set exactly — total vs active parameters carried as
DISTINCT declared labels, never interchangeable), `ExpertPackArtifact`
(an artifact mode, never a fifth provider).

- **The versioned manifest schema `scarlix.model/v2`** — the §5.5
  example shape, loaded from `/etc/scarlix/models.d` (override
  `SCARLIX_MODELS_D`; the repo example `models.d/example-moe-model
  .manifest.yaml` documents the shape with DELIBERATELY invalid
  placeholders so it can never be mistaken for usable metadata).
- **Fail-closed validation with named reasons** (per manifest, never
  silent drops): schema-version refusal BY NAME (a future schema is
  future code), 64-hex checkpoint fingerprints, MoE geometry
  requirements, lowercase-slug backends (an OPEN set — declaring the
  future `infer_ssd` backend is legal; the planner will intersect with
  live runtimes), enabled expert packs must be verifiable + bound to
  ONE compatible backend.
- **The placement gate** (`PlacementEligibility`): the roadmap's "a
  manifest with required unknown values must not be used for automatic
  placement" as a per-model boolean + NAMED reasons. MoE with unknown
  `expert_bytes`/`shared_bytes` → ineligible. KV-cache bytes/token is
  optional (runtimes size it — Phase 2's KV budget names its own
  unknown). Invalid manifests are trivially ineligible.

### 4. The v2 unified snapshot (`scarlihq/internal/inventory/v2.go`)

Roadmap §5.3/§5.6 — `InventorySnapshotV2`: schema version, per-build
random snapshot ID (crypto/rand, 32 hex), `observed_at`, `Host`,
`GPUs` (the FROZEN v1 collector), `Storage`, `Runtimes` (frozen v1),
`Models` (the merged `ModelExecutionView`: v1 catalog + manifest
execution + `manifest_status` + the placement gate), `Health`, and
`Sources` — the per-component provenance (ok/partial/empty/unavailable,
with detail; silence is never an answer).

- **Freshness contract:** `FreshWithin(maxAge)` (non-positive policy =
  always stale — fail-closed), `Response(maxAge)` evaluates the stale
  marker at SERVE time (`freshness: {observed_at, age_seconds,
  max_age_seconds, stale}`) — the marker answers for the READER's now,
  and every consumer must reject/downgrade stale data, never reuse an
  old free-VRAM/free-space value as current capacity.
- **Every component degrades independently** to its empty shape + an
  honest source status; the snapshot is ALWAYS built (a partial
  inventory with markers beats a 500 with none).
- **v1 is FROZEN and untouched** (roadmap §5.3: no v1 JSON key renamed
  or repurposed): the v1 tests pass unmodified; `TestV1FrozenContractIntact`
  re-pins the frozen GPU keys + the internal-UUID exclusion +
  the []-not-null contract.

### 5. The read-only API + CLI surface

- `GET /api/inventory/v2` (full snapshot + freshness),
  `GET /api/inventory/v2/storage` (host + devices + storage sources),
  `GET /api/inventory/v2/models` (execution views + model sources) —
  behind the standard Bearer read gate (the `/api/status` precedent:
  any authenticated principal may OBSERVE; observation grants no
  control). **Non-GET is 405 BY NAME** ("inventory is read-only") on
  all three routes — there is no write surface, no benchmark trigger,
  no reservation; the route set enforces the roadmap's "inventory must
  not execute workloads or reserve capacity".
- Freshness policy: `SCARLIHQ_INVENTORY_MAX_AGE` (default 60s;
  garbage/negative → the default, documented — informational marker
  semantics, not an auth boundary).
- CLI: `scarlix-inventory --v2 | --v2-storage | --v2-models |
  --storage-benchmark` (the v1 flags unchanged; the conflicting-flag
  guard extended).
- The collector seam (`ConfigureInventoryV2`) keeps the HTTP surface
  testable without touching the host.

### 6. Documentation

- `docs/INVENTORY_V2_CONTRACT.md` (NEW, canonical): the boundary, the
  envelope, the storage semantics, the manifest contract, the benchmark
  policy, freshness + provenance, the API table, the v1-compatibility
  statement.
- `docs/architecture/SCARLIXOS_CURRENT_ARCHITECTURE.md`: the v19.7.0
  header + the package map gains `storage` + `modelmeta` + the v2
  snapshot row; the roadmap pointer notes Phase 1 DONE.
- README: the changelog row + the What's New section.

## Tests

- `internal/storage`: parse truth tables (mounts with octal escapes,
  partition→parent mapping incl. the dm-0/nvme0n1 edge cases, NVMe
  controller names, meminfo, cpuinfo), seam-fixture discovery (mount-
  driven grouping, USB transport, unmounted raw capacity, unavailable
  mounts → named status, host partial degradation), the read-only
  charter source assertion, the JSON wire contract (temperature
  omitted when unknown), the benchmark bounds fail-closed table, a
  real bounded run + cleanup proof, cancellation cleanup, and the
  discovery↔benchmark seam isolation.
- `internal/modelmeta`: the validation truth table (17 named-refusal
  cases), the placement-eligibility matrix (dense eligible, full-geometry
  MoE eligible, unknown expert/shared bytes ineligible BY NAME, invalid
  → ineligible), the KV-unknown-is-allowed rule, the Execution
  conversion (SHA case normalization, MoE geometry, pack binding,
  total-vs-active distinctness), directory loading (valid + invalid +
  non-manifest skip + unreadable dir), the env override resolution.
- `internal/inventory`: the v2 envelope shape (all six source
  components present, non-nil everywhere), unique snapshot IDs, the
  freshness math (age vs policy, non-positive always-stale, serve-time
  evaluation), the v1 frozen-key re-pin, the honest-absence model view
  JSON.
- `internal/api`: auth 401 on all three routes, the 405 read-only
  negative control on all three routes (body names the refusal), the
  full envelope shape, stale marking (a 5-minute-old snapshot under the
  60s policy), the section views + source filtering, collector-error →
  500 (never a fabricated snapshot), the max-age env parsing table, and
  the REAL collector end-to-end (no seam — the live /proc read honoring
  the contracts).

## Verification

```
cd scarlihq
gofmt -l .            # clean
go vet ./...          # clean
go test ./...         # all packages OK (incl. the new storage/modelmeta suites)
go test -race ./internal/storage ./internal/modelmeta ./internal/inventory ./internal/api
bash ../tests/test_registry_classification.sh    # 68/68
bash ../files/usr/local/bin/scarlix-smoke-test.sh --offline   # 37 checks, 0 FAIL
```

Hardware acceptance stays OPEN (KPK-06, unchanged since v19.3.x) — no
"production ready on the full OS" claim is made by this tag. The Host
Bridge target-host acceptance matrix (P5.1) remains the gate for enabling
the workload bridge service, which stays disabled.

## Known limitations (named, not hidden)

- The storage inventory is mount-driven: a device-mapper/LVM mount's
  backing transport is `unknown` (a sysfs holder walk is future work);
  the underlying PV device appears as raw capacity with zero available.
- Device temperature is not collected in v19.7.0 (`null` — the hwmon
  association is future work); the field exists so the schema does not
  break when it arrives.
- The benchmark measures page-cache-resident reads (the warm pass); a
  cold-cache measurement requires the operator-side drop_caches
  diagnostic — stated in every result's Detail.
- `ram_pinned_bytes` stays 0 until Phase 2's admission ledger owns it.
- No manifests ship for the real models yet: every model reports
  `manifest_status: "none"` + placement-ineligible until an operator
  writes verified manifests — the honest default, exactly as the
  roadmap's example demands.
- The v19.7.1 resource vector / aggregate admission and the v19.7.2
  planner are NOT in this release (Phase 2/3 of the roadmap); the
  inventory is the foundation they will consume.

'LLM navrhuje, politika rozhoduje' — the inventory observes honestly,
the unknowns stay unknown, the gate refuses what it cannot verify, and
the pen stays with the operator.
