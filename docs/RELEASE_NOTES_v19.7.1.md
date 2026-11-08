# ScarLiX OS v19.7.1 — Compute Fabric Phase 2: Resource Vector + Aggregate Admission + Reservation

**Release:** v19.7.1
**Commit:** [`cb725e8cc653b4d9ff18b2bb7fd54001dfb86df4`](https://github.com/MoZoHuJa/OS/commit/cb725e8cc653b4d9ff18b2bb7fd54001dfb86df4) — recorded at packaging (the release-notes genre: the hash lands with the tag)
**Baseline commit (pre-change):** `f8638ef` (v19.7.0 release-notes hash record)
**Date:** 2026-11-08
**Scope:** Compute Fabric **Phase 2 — Resource Vector, Aggregate
Admission and Reservation** (roadmap §6 of
`docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`,
the in-repo implementation plan to v20.0.0) + **the KPK-19.7.0 audit
remediations** (P2-01..P2-05, P3-05) + **the crash-safe budget
reservations** (KPK-19.6.7-carry-forward-P2-06)

The release sequence toward v20 continues: v19.6.8 (stabilization —
DONE) → v19.7.0 (unified resource inventory — DONE) → **v19.7.1 (this
release: resource vector + aggregate admission)** → v19.7.2 (backend
capability contracts + deterministic planner) → v19.8.0 (InferSSD +
SGLang Expert Pack, experimental) → v19.9.0 (distributed fabric) →
v20.0.0 (production control plane). Release labels ≥ v19.7.2 remain
planning labels, not commitments.

## Scope

This release executes Phase 2 of the Unified Compute Fabric roadmap:
the **resource vector package**, the **deterministic aggregate
admission gate with the atomic reserve pattern**, and the **durable,
crash-safe admission ledger** — plus the remediation of the six
KPK-19.7.0 audit findings in the Phase 1 surface they audited, and the
crash-safe rework of the profile token-budget reservations carried
forward from the v19.6.7 audit. The release's must-not-do is honored
in code at the gate itself: **nothing schedules from unverified
estimates** — a measured-performance floor without a measurement is a
named refusal, and a model without a RECORDED, FRESH verification is a
named refusal.

## Changes

### 1. The admission package (`scarlihq/internal/admission` — NEW)

Roadmap §6, field-for-field:

- **The resource vector (§6.1/§6.2):** `ResourceVector` — GPU VRAM
  **per UUID** (`GPURequest`: VRAM bytes + the exclusive-device
  requirement), host RAM, pinned RAM, CPU threads, KV-cache bytes,
  storage free bytes with the measured read-profile floor
  (`StorageRequest`), and host ports. Integer bytes and explicit
  units; never one scalar score.
- **The admission interface (§6.4):** `AdmissionRequest` /
  `AdmissionDecision` with the closed, stable refusal vocabulary (25
  `Reason*` codes — machine-readable; additions additive, renames
  breaking). Every refusal names the exact numbers that failed.
- **The atomic reserve pattern (§6.4 steps 1–9):** validate identity →
  snapshot freshness (HARD refusal on stale — KPK-19.7.0-P2-01) →
  model/backend compatibility (the VERIFIED-artifact gate +
  placement × runtime join — KPK-19.7.0-P2-04) → compute the candidate
  assignment → check ALL dimensions under ONE ledger transaction →
  persist → return the assignment + lease. **Any dimension failure
  reserves NOTHING.**
- **The capacity model (§6.3):** safety headroom subtracted BEFORE
  allocation (VRAM 5%, RAM 10%, storage 10% fractions; 2 CPU threads
  for the OS); allocatable = max(0, observed − reserved − headroom);
  GPU capacity per physical UUID, never aggregated across GPUs.
- **The GPU sharing policy (§6.5):** EXCLUSIVE assignment only in
  v19.7.1 — a non-exclusive ask is refused BY NAME (sharing without
  compute-share enforcement is a phantom capability).
- **Lease and release (§6.6):** TTL leases (default 10 min); expiry
  triggers reconciliation (the named `EXPIRED` status), never blind
  release; explicit release requires the held record (unknown/double
  release is a named 409); the idempotency fingerprint includes model,
  backend, GPU UUIDs, storage device, and ports.
- **The durable ledger:** fail-closed construction (a
  group/world-accessible ledger file is refused — capacity accounting
  is not ambient; a foreign `schema_version` is refused BY NAME — a
  future migration, never a silent reinterpretation); **boot
  reconciliation** (holds whose boot identity is not this process's
  are crash-released ON THE RECORD with the named
  `CRASH_RECOVERED` status); expired-lease sweeps; atomic
  tmp+rename persistence before the decision is returned (an
  unpersisted reservation is not a reservation).
- **Pinned RAM is refused by default** (`pinned-ram-unavailable`):
  `MaxPinnableRAMBytes: 0` is the honest v19.7.1 policy — no
  pinned-RAM observation or ledger exists yet, so pinning is refused,
  not guessed.

### 2. The admission API surface (opt-in, ADMIN-gated)

Two routes: `POST/GET /api/compute/admission` (decide + atomically
reserve; GET reports the held state + the active policy + the boot
identity) and `POST /api/compute/admission/release`. Behind the ADMIN
gate (admission RESERVES capacity — a strictly stronger act than the
read-only inventory). The principal is stamped **SERVER-SIDE** from
the resolved token identity (`PrincipalFromContext`) — a caller cannot
attribute its hold to someone else. Strict `DisallowUnknownFields`
decoding — a typo'd dimension is a refusal, not a silent default.
**The POST response is ALWAYS a decision object** — a refusal is 200 +
`allowed:false` + the machine-readable `reason_code` (the gate
working, never an HTTP error); malformed JSON is 400; the surface
answers 503 until `ConfigureAdmission` is called (opt-in like every
mutating act). Enablement: `SCARLIHQ_ADMISSION_ENABLED=true` +
`SCARLIHQ_ADMISSION_LEDGER=<path>` — construction fail-closes on a
corrupt/tampered ledger.

### 3. The production wiring

`cmd/scarlihq/main.go` constructs the production manager
(`admission.NewProductionManager`): the live snapshot seam
(`inventory.BuildSnapshotV2` — same freshness envelope, same
per-source provenance the decision cites) and the resolving model seam
(models.d manifests + the recorded verification store, re-loaded at
CALL time — no caching; a manifest edited between two admissions is
seen by the second one).

### 4. The KPK-19.7.0 audit remediations (the Phase 1 surface)

- **KPK-19.7.0-P2-01 — per-device filesystem collection:** the first
  mount no longer stands in for the whole disk
  (`collectDeviceFilesystems`); a physical disk with several
  partitions reports every filesystem; the v2 snapshot's per-source
  provenance carries the multi-mount truth. A stale snapshot is
  HARD-refused at the admission gate (the planner-side rule enforced
  where it matters).
- **KPK-19.7.0-P2-02 — empty ≠ unavailable:** `CollectGPUsWithStatus`
  / `CollectRuntimesWithStatus` return the `SourceStatus` alongside
  the data ("queried successfully, nothing there" vs "could not query
  — state unknown"); `BackendPresence` joins DECLARED backends with
  RUNNING ones (the placement × runtime view admission consumes).
- **KPK-19.7.0-P2-03 — benchmark hardening:** the temp file is created
  EXCLUSIVELY (`O_CREATE|O_EXCL` — a same-name stale file is a named
  error, never an accidental overwrite); a failed cleanup is REPORTED
  to the caller ("benchmark completed but cleanup failed") — never
  swallowed; cancellation before the fill starts leaves NO temp file.
- **KPK-19.7.0-P2-04 — declared ≠ verified, end to end:** duplicate
  model IDs are rejected as a GROUP ("ALL conflicting manifests are
  invalid" — no deterministic file-order winner; the operator sees
  both paths); every emitted digest is renamed `declared_sha256` in
  the JSON (declared ≠ verified in every consumer's view); the
  admission gate consumes RECORDED verification records only, and a
  STALE verification is absent verification (the age policy enforced).
- **KPK-19.7.0-P2-05 — production stores protected:** benchmarking a
  well-known production model store root is DENIED by default
  (`ErrBenchmarkTargetDenied`); the deliberate explicit override is
  `SCARLIHQ_BENCHMARK_DENY_ROOTS=` (empty value — a conscious operator
  act, never a silent default).
- **KPK-19.7.0-P3-05 — exact-token mount options:** `errors=remount-ro`
  is the exact-token comparator (never a substring match on `ro`).

### 5. The crash-safe budget reservations (KPK-19.6.7-carry-forward-P2-06)

`internal/profiles/budget.go` reworked: every reservation is a durable
RECORD (unique ID, profile, month, amount, created-at, BOOT IDENTITY)
— not an anonymous aggregate counter. At startup the ledger
reconciles: reservations whose boot identity is not this process's are
RELEASED (a provider request from a dead process cannot still hold
quota — the chat HTTP path is synchronous; if the process died, the
request died with it) and the release is RECORDED in a bounded audit
trail (`Recovered`, 64 entries). Accounting is MONTH-SPECIFIC (an
abandoned hold from a previous month cannot consume the current
month's budget). A v19.6.x ledger (schema 1, aggregate map with no
identity) is MIGRATED explicitly: each aggregate becomes a legacy
record which the first reconciliation then releases — the old
orphaned aggregates are recovered on the record at upgrade time.

## Tests

- `internal/admission/manager_test.go` — 25 tests: the §6.7 checklist
  (two concurrent requests competing for the same GPU; RAM
  competition; SSD capacity exhaustion; SSD I/O requirement
  unmeetable; stale inventory during admission; idempotency replay
  same-key-same-fingerprint; crash after ledger write; lease expiry
  reconciliation; verified release) plus every named refusal reason,
  the KV-counts-against-GPU accounting, the deterministic any-GPU
  pick, concurrent no-overspend, and failure-reserves-nothing.
- `internal/admission/production.go` — the thin production seams (no
  cache by design; unreadable verification store = unverified).
- `internal/api/admission_test.go` — 9 tests: 503-before-config, 401
  auth, 400 strict decode, 405 method guard, the allowed golden path
  (assignment + lease + policy provenance + the held state),
  refusal-is-data (200 + `allowed:false` + `reason_code` + held=0),
  idempotent replay (one hold), the release flow (released / double
  release 409 / unknown 409), release validation.
- `internal/profiles/budget_recovery_test.go` — 5 tests: the orphaned
  hold released at startup, same-boot survival, the month boundary,
  idempotent recovery, the v1 aggregate migration.
- The Phase 1 suites evolved in place (storage discovery/benchmark,
  inventory v2 + provenance, modelmeta manifest/verify, inventoryv2
  API) covering the six KPK-19.7.0 remediations.

## Verification

gofmt clean; `go vet` clean; `go build ./...` clean; `go test ./...`
all 28 packages OK; `go test -race` clean on
admission/api/profiles/storage/inventory/modelmeta; smoke
`--offline` 38 checks 0 FAIL (check_38 NEW with negative controls —
the admin-gate assertion, the server-side principal stamp, the opt-in
env contract, the KPK remediation markers; check_28/check_37 evolved
current-release guards).

## Known limitations (named, not hidden)

- **Hardware acceptance stays OPEN (KPK-06)** — CI cannot establish
  correctness on the target machine; the P5.1 matrix is the gate.
- **No verified manifests ship for the real models yet** — every
  admission for a real model refuses `model-unverified` until an
  operator records verifications (`scarlix-inventory` verification
  pass). The honest default the roadmap's own example demands.
- **Pinned RAM is refused** until a pinned-RAM observation/ledger
  exists (the honest no-ledger-yet answer, not a guess).
- **GPU sharing arrives only with a measured per-backend profile**
  (v19.7.2+); v19.7.1 admits EXCLUSIVE GPU assignment only.
- **The admission ledger holds capacity, not workloads** — the
  v19.7.2 planner joins admission with the backend capability
  contracts and the deterministic execution plan.
- **GPU reset/driver-error invalidation** triggers reconciliation
  through the ledger's boot/expiry machinery; a live GPU-health ×
  reservation reconciler is v19.7.2+ work.

'LLM navrhuje, politika rozhoduje' — the vector is honest, the
reserve is atomic, the ledger survives the crash, and the pen stays
with the operator.
