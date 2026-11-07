# ScarLiX OS — Unified Resource Inventory v2 Contract

**Status:** CANONICAL (v19.7.0) — the read-only resource inventory the
Compute Fabric roadmap §5 defines (Phase 1).
**Code truth:** `scarlihq/internal/storage`, `scarlihq/internal/modelmeta`,
`scarlihq/internal/inventory/v2.go`, `scarlihq/internal/api/inventoryv2.go`,
`scarlihq/cmd/scarlix-inventory` (`--v2*`, `--storage-benchmark`).
**Roadmap:** §5 of
[`docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`](roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md).

------------------------------------------------------------------------

## 1. The boundary (what this surface IS and IS NOT)

The v2 inventory is **ONE normalized, READ-ONLY view** of everything
resource planning needs: host resources, GPUs, storage devices, network
mounts, runtimes, and model execution metadata.

- It **executes no workloads** and **reserves no capacity** (roadmap §5.1).
- It **benchmarks nothing automatically** — the storage benchmark is a
  separate operator diagnostic (§5 below).
- It **schedules from nothing** — a planner (v19.7.2) will consume it
  under its own policy; this contract only OBSERVES.

## 2. The snapshot envelope (`scarlix.inventory/v2`)

`GET /api/inventory/v2` (and `scarlix-inventory --v2`) emits:

```json
{
  "schema_version": "scarlix.inventory/v2",
  "snapshot_id": "32-hex random identity, unique per build",
  "observed_at": "RFC 3339 UTC — the freshness anchor",
  "host": { "cpu_threads": 24, "cpu_model": "...",
            "ram_total_bytes": 0, "ram_available_bytes": 0 },
  "gpus":     [ /* the FROZEN v1 GPU records (unchanged keys) */ ],
  "storage":  [ /* storage.Device records (§3) */ ],
  "runtimes": [ /* the FROZEN v1 Runtime records */ ],
  "models":   [ /* ModelExecutionView records (§4) */ ],
  "health":   [ /* v1 Health records for GPUs + runtimes */ ],
  "sources":  [ /* per-component provenance (§6) */ ],
  "freshness": { "observed_at": "...", "age_seconds": 0.1,
                 "max_age_seconds": 60, "stale": false }
}
```

**Non-nil slices everywhere** (`[]`, never `null`) — the v1 stability
contract carries into v2.

## 3. Storage devices (`internal/storage`)

One `Device` per block device (mounts attached) or network mount:

- **Read-only sources only:** `/proc/self/mounts`, `/sys/block`,
  `/sys/class/nvme`, `statfs(2)`, `/proc/cpuinfo`, `/proc/meminfo`.
  No exec, no writes — asserted by a source-level test AND the smoke
  check (the check_28 "prose cannot mask code" discipline).
- **Identity:** `ID` (`storage.nvme.0`) is stable within a snapshot;
  `StableID` (`dev.` + 16 hex) is a SHA-256 DIGEST of identity material
  (kind + kernel name + model + serial) — **the raw serial is never
  emitted** (roadmap §5.2 rule 5). Identity strength varies by
  transport; network mounts digest `fstype:source`.
- **Kinds:** `nvme | sata | usb | network | unknown` — unknown is a
  VALID, honest value (virtio/mmc transports are not guessable).
  USB discrimination: the `/sys/block/<dev>/device` path containing a
  USB segment; unresolvable → unknown, never a guess.
- **Capacity semantics:** a mounted device reports FILESYSTEM capacity
  (`statfs`); an UNMOUNTED device reports RAW sysfs capacity with
  `available_bytes: 0` — no filesystem means free space is UNOBSERVABLE,
  which prevents phantom "free" devices (an LVM PV is raw capacity,
  never admission capacity).
- **Stacked layers excluded:** `dm-*`, `md*`, `loop*`, `zram`, `ram`
  are not separate devices (double-counting); tmpfs/proc/sysfs/overlay
  are not storage at all (RAM-as-disk substitution is exactly what the
  honest-accounting rules forbid).
- **Unknown ≠ zero:** `temperature_c` is a POINTER — `null` = not
  measured; `read_profile` zero value = never benchmarked.

### Host resources (`storage.HostResources`)

`cpu_threads`, `cpu_model`, `ram_total_bytes`, `ram_available_bytes`
(kernel MemAvailable), `swap_total_bytes`, `swap_free_bytes` —
**swap is REPORTED, never ADMITTED as model memory** (roadmap §5.3; no
backend has a tested swap strategy). `ram_pinned_bytes` is a
reservation-ledger property Phase 2 will populate; zero means "no figure
reported", not "none pinned".

## 4. Model execution metadata (`internal/modelmeta`)

Manifests are YAML files (`*.manifest.yaml`) in `/etc/scarlix/models.d`
(override: `SCARLIX_MODELS_D`; repo example: `models.d/`) under schema
**`scarlix.model/v2`** — the shape of the roadmap §5.5 example,
field-for-field.

**Fail-closed validation (named reasons, per manifest):**

- `schema_version` must be exactly `scarlix.model/v2` — a future schema
  is refused BY NAME (never half-parsed).
- `checkpoint.sha256` must be 64 hex chars — the verified fingerprint
  that binds revisions and expert packs.
- `architecture.kind` ∈ `dense|moe|other`; **MoE requires**
  `expert_count > 0` AND `experts_per_token > 0`.
- `runtime.context_limit > 0`; `supported_backends` non-empty, lowercase
  slugs, no duplicates. The set is OPEN: a manifest may DECLARE a future
  backend (`infer_ssd` before v19.8.0) — the planner intersects with
  live runtimes; the manifest never asserts presence.
- An ENABLED expert pack requires its own `sha256` + ONE
  `compatible_backend` + `version` (cross-engine compatibility is never
  claimed — roadmap principle 9).

**The placement gate (roadmap §5.5):** `PlacementEligibility` answers
per-model whether the metadata is COMPLETE ENOUGH for automatic
placement, with NAMED reasons. Valid-but-incomplete manifests (e.g. MoE
with unknown `expert_bytes`) are **placement-INELIGIBLE** — unknown
required values are never defaulted. KV-cache bytes/token is OPTIONAL
for placement (runtimes size it; Phase 2's KV budget names its own
unknown).

**The join:** every v1 catalog model gets a `ModelExecutionView`
(`catalog` + `execution` when a verified manifest matched +
`manifest_status`: `verified|none|invalid:<path>` + `placement`). A
model with NO manifest is honestly `none` + ineligible — the v1 fields
are all that is known.

## 5. The opt-in storage benchmark

`scarlix-inventory --storage-benchmark <dir> --duration 5s --size 256MiB`
(± `--block`) is the ONLY benchmark door in the repository:

- Bounds enforced **fail-closed with named errors** — duration 1s..30s,
  file size 32MiB..1GiB, block 4KiB..8MiB. Required options have NO
  defaults (an unset duration/size is an error — the operator chose the
  profile or did not benchmark).
- Writes ONE identifiable temp file (`.scarlix-bench-<id>`) into the
  target, warms it, measures a sequential-read window, removes it.
  Cancellation cleans up.
- **The target choice is the operator's responsibility** (roadmap
  rule 4: do not benchmark a mounted production model store without
  approval).
- The result is a **data point with provenance** (`ReadProfile`:
  timestamp, block size, profile ID) — never a guaranteed sustained
  rate. Cache residency is part of what is measured (unprivileged
  drop_caches is out of scope; the Detail says so).
- Discovery NEVER calls it — asserted at the source level
  (`TestBenchmarkSeamIsolation`) and in the smoke check.

## 6. Freshness and provenance (roadmap §5.6)

Every snapshot carries `sources[]` — per component
(`storage`, `storage.host`, `gpus`, `runtimes`, `models`,
`models.manifests`) the source read and its status:
`ok | partial | empty | unavailable` (+ detail). Silence is never an
answer: a failed source is named, a partial source says what degraded.

**Freshness is evaluated at SERVE time**: `freshness.stale` =
`age > max_age` against `SCARLIHQ_INVENTORY_MAX_AGE` (default 60s;
garbage/negative falls back to the default — informational marker
semantics). A non-positive policy is always-stale (fail-closed: an
unset policy must not bless data). **A stale snapshot must be rejected
or downgraded by every consumer — never reused as current capacity**
(the release's must-not-do: "do not schedule from unverified
estimates").

## 7. API surface (read-only)

| Route | Gate | Contract |
|---|---|---|
| `GET /api/inventory/v2` | Bearer (any authenticated principal — the `/api/status` read gate) | full snapshot + freshness |
| `GET /api/inventory/v2/storage` | same | `freshness` + `host` + `storage` + storage sources |
| `GET /api/inventory/v2/models` | same | `freshness` + `models` + model sources |

Non-GET → **405 BY NAME** ("inventory is read-only"). There is no write
surface, no benchmark trigger, no reservation — enforced by the route
set itself. CLI mirrors: `scarlix-inventory --v2 | --v2-storage |
--v2-models`.

## 8. v1 compatibility (the exit criterion)

The v1 contracts are FROZEN and unchanged: `SystemStatus`, `GPU`,
`Runtime`, `Model`, `Service`, `Health` marshal exactly as v19.0.7
froze them (the internal `GPU.UUID` stays excluded from public JSON).
The v1 tests keep passing unmodified; v2 is purely additive. The
`scarlix-inventory` CLI's v1 flags behave identically.
