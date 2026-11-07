# ScarLiX OS v19.6.8 — Compute Fabric Phase 0: Lifecycle Vocabulary + Additive State Machine + Canonical Architecture

**Release:** v19.6.8
**Commit:** [`b09011ad4ad1c56927681cbd5fc86fc2545d873c`](https://github.com/MoZoHuJa/OS/commit/b09011ad4ad1c56927681cbd5fc86fc2545d873c) — recorded at packaging (the release-notes genre: the hash lands with the tag)
**Baseline commit (pre-change):** `292b3d877b4f1f0b727f6286b7972096b07af512`
**Date:** 2026-11-07
**Scope:** Compute Fabric **Phase 0 — Stabilize and establish a trusted
baseline** (roadmap §4 of
`docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`,
the in-repo implementation plan to v20.0.0)

## Scope

This release executes Phase 0 of the Unified Compute Fabric roadmap: the
lifecycle-vocabulary correction (the roadmap's own "specific remediation"
§4.3), the additive state-machine amendment (§4.4), the documentation
reconciliation (§4.2 — the canonical architecture trio + historical
banners), and the baseline freeze (§4.1 — the frozen record at
`docs/releases/v19.6.8/BASELINE.md`).

The release sequence toward v20 is now an in-repo plan: v19.6.8 (this
release, stabilization) → v19.7.0 (unified resource + storage inventory)
→ v19.7.1 (resource vector + aggregate admission) → v19.7.2 (backend
capability contracts + deterministic planner) → v19.8.0 (InferSSD +
SGLang Expert Pack, experimental) → v19.9.0 (distributed fabric) →
v20.0.0 (production control plane). Release labels ≥ v19.7.0 are planning
labels, not commitments.

## Changes

### 1. The lifecycle-vocabulary correction (roadmap §4.3, the §1.3-8 semantic gap)

The agent compute intake (`POST /api/agent/sessions/compute`) formerly
labeled its workload-start operation `"reserve"` — it called
`computeManager.Start` on an allocation the reservation service had
already reserved. As of v19.6.8:

- The actions are **`start`** and **`release`** — the honest names for
  what the surface does.
- The legacy `"reserve"` spelling is **rejected BY NAME** (400) with a
  message naming the boundary, the honest action, and where reservations
  are actually created (the reservation service,
  `cmd/scarlix-reservation`) — per the roadmap: "do not retain a
  misleading API action name". The rejection happens BEFORE the kernel
  evaluation (a vocabulary error, not a capability decision — no session,
  no grant, no audit line).
- The kernel capability `compute.reserve` keeps its name — renaming a
  capability would break every persisted grants.json record; the
  capability NAME is historical, the action is what it always was: a
  workload start. Documented at the action site and in the contract.
- The audit type for start actions is the new honest
  **`compute.start`** (`AuditTypeComputeStart`); the validation gates it
  identically to the other compute lifecycle types; the legacy
  `compute.reserve` type stays valid in the append-only audit history.
- Regression: `TestAgentComputeReserveActionRejectedByName` — the named
  refusal (the message must name the boundary AND the reservation
  service) + the same session CAN start through the honest action (a
  vocabulary correction, not a capability denial). All existing intake
  tests migrated to `"start"`.

### 2. The additive state-machine amendment (roadmap §4.4)

Two states join the FROZEN v19.2.2 table — an ADDITION, which the frozen
contract explicitly permits ("Additions require a new state or an
explicit documented amendment"; this is both). 15 states total; no state
renamed, removed, or redirected; old persisted records remain valid:

- **`STOP_REQUESTED`** — a stop has been requested for a live workload
  (READY/RUNNING/STARTING) and the driver stop has not yet been verified.
  In-edges: RUNNING/READY/STARTING → STOP_REQUESTED. Out-edges: RELEASING
  (the driver verified the stop), CLEANUP_REQUIRED (explicit failure),
  RECONCILE_REQUIRED (ambiguous), FAILED/CANCELLED (side exits). Never
  RELEASED directly (no terminal shortcut — the KPK-01 verified-cleanup
  gate extends to the new vocabulary).
- **`RECONCILE_REQUIRED`** — an operation's outcome is AMBIGUOUS; a
  reconciler must decide, never a blind retry. In-edges: STOP_REQUESTED,
  STARTING, RELEASING. Out-edges: RELEASING, CLEANUP_REQUIRED,
  FAILED/CANCELLED. Distinct from CLEANUP_REQUIRED (cleanup affirmatively
  needed) — the distinction the roadmap's vocabulary exists to make.

**`Manager.Stop` wiring:** the stop REQUEST is recorded durably as
STOP_REQUESTED BEFORE the driver call (crash-safety: a restart sees the
honest marker; the retry path accepts STOP_REQUESTED and RELEASING
idempotently). Outcome mapping: verified → RELEASING with
`CleanupVerified=true` (STOP_REQUESTED → RELEASING; the reservation
bundle is NOT released — Phase 6 owns that); explicit stop failure →
CLEANUP_REQUIRED; **ambiguous outcome (stopErr == nil, unverified) →
RECONCILE_REQUIRED** — new. Already-down allocations
(FAILED/CANCELLED/CLEANUP_REQUIRED) keep the direct RELEASING entry (no
stop request for a workload that was never running).

**Reservation release scanner:** `ListReleasingAllocations` now covers
RECONCILE_REQUIRED (an ambiguous allocation is release-lifecycle work —
leaving it out of the scan would strand it forever) but NOT the transient
STOP_REQUESTED (the stop is still in flight — release comes after the
verified stop).

**Tests:** the state-machine matrix gains the additive legality/illegality
entries (no resume from STOP_REQUESTED/RECONCILE_REQUIRED, no
STOP_REQUESTED → RELEASED shortcut, no ping-pong ambiguity) and the
15-state count guard; the workload manager gains
`TestStopRecordsStopRequestedBeforeDriverCall` (the driver observes the
durable STOP_REQUESTED state at call time — the ordering proof),
`TestStopAmbiguousOutcomeBecomesReconcileRequired`,
`TestStopRetryFromStopRequestedCompletes` and
`TestStopFromFailedGoesDirectlyToReleasing`; the release-scan test covers
the RECONCILE_REQUIRED source state and the STOP_REQUESTED exclusion.

### 3. The documentation reconciliation (roadmap §4.2)

- **Canonical trio** under `docs/architecture/` (one source of truth,
  code-verified): `SCARLIXOS_CURRENT_ARCHITECTURE.md` (the 5-layer model,
  package map, lifecycle summary, known limitations, document status map),
  `COMPUTE_FABRIC_CONTRACT.md` (the nine-operation lifecycle vocabulary,
  the state machine, the API contract — what callers may never supply —
  the conservative admission guard, the Host Bridge acceptance status),
  `AI_RUNTIME_CONTRACT.md` (the closed provider set, the transport
  boundary, failover + budgets, the proposal path, the identity model).
- **Historical banners** on the stale architecture documents:
  `docs/SCARLIX_CURRENT_ARCHITECTURE.md` (v19.2.9), `docs/ARCHITECTURE.md`
  (v19.0.2), `docs/SCARLIX_GPU_MAP.md` and `docs/SCARLIX_RUNTIME_MAP.md`
  (v19.2.9) — each banner names the last-valid version and points at the
  canonical document.
- **The roadmap lives in-repo**:
  `docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`
  (the user's implementation plan — baseline v19.6.6, target direction
  v19.6.7→v20.0.0; release labels ≥ v19.7.0 are planning labels).

### 4. The baseline freeze (roadmap §4.1)

`docs/releases/v19.6.8/BASELINE.md`: the exact pre-change repository SHA
(`292b3d8…`), the toolchain (go1.23.4), the active compose/model/profile
set (pointers into the drift-guarded dependency inventory), the
verification evidence, and honest `TARGET-HOST — record at acceptance`
markers for the facts only the operator can record on the real machine
(kernel, Docker Engine, NVIDIA driver/CUDA, GPU UUIDs, SSD devices).

### Housekeeping

- Version bump v19.6.8: VERSION, install.sh, the Dockerfile ARG + header,
  main.go/rest.go vars.
- **gofmt normalized** — the whole `scarlihq/` tree is now gofmt-clean
  (the v19.6.7 release left 10 space-indented files; the release-notes
  promise "the Go lane normalizes at packaging" is kept here).
- Smoke check inventory 35 → **36**: `check_36_compute_fabric_phase0_lifecycle`
  (comment-stripped code markers + THREE negative controls: (a) reverting
  the action vocabulary — `action == "reserve"` discriminating the start
  call — FAILs the absence assertion; (b) removing the RECONCILE wiring
  from the stop path or the release scanner FAILs the wiring/scanner
  markers; (c) removing a canonical doc or a historical banner FAILs the
  documentation markers). check_28/check_35 evolved to the v19.6.8
  current-release surface (the v19.6.7 surface stays asserted as the
  permanent record).
- Docs: this release note; README (banner/badge/version line, changelog
  row, What's New v19.6.8, install header + mainline paragraph).

## Verification status

Executed in the release environment (2026-11-07):

```
cd scarlihq
gofmt -l .                     # clean — the whole tree normalized
go vet ./...                   # PASS
go build ./...                 # PASS
go test ./...                  # PASS — all packages
go test -race ./internal/allocation ./internal/reservation \
  ./internal/workload ./internal/hostbridge ./internal/compute \
  ./internal/api ./internal/agentkernel -count=1    # PASS
bash tests/test_registry_classification.sh          # 68/68
bash tests/test_dependency_inventory.sh             # 44/44
bash tests/test_media_tools.sh                      # 42/42
bash files/usr/local/bin/scarlix-smoke-test.sh --offline   # 36 checks, 0 FAIL
python3 -m py_compile media-tools/server.py         # PASS
```

Hardware acceptance stays OPEN (KPK-06, unchanged since 19.3.x) — no
"production ready on the full OS" claim is made by this tag. The Host
Bridge target-host acceptance matrix (P5.1) remains the gate for enabling
the workload bridge service, which stays disabled.

## Known limitation

- The agent compute intake action vocabulary is a **breaking API change**
  for any caller still sending `action:"reserve"` — deliberate, per the
  roadmap's "do not retain a misleading API action name"; the refusal
  message names the correct action. The kernel capability name
  `compute.reserve` is unchanged (grants.json compatibility).
- `RECONCILE_REQUIRED` allocations converge through the reservation
  reconciler's existing release-recovery scan; a dedicated operator
  surface for inspecting/driving them individually is v19.7.x UI work.
- STOP_REQUESTED is transient by construction (the manager completes the
  driver call synchronously within the Stop operation); a crash DURING
  the driver call leaves it as the honest retry marker — accepted
  idempotently, not auto-driven.
- Aggregate admission remains conservative (one allocation per GPU/port)
  until the v19.7.1 Resource Vector; the storage inventory (v19.7.0) and
  the deterministic planner (v19.7.2) are the next roadmap milestones.
- The residuals of the v19.6.6/v19.6.7 remediations stand as documented
  there: hardware OPEN, keyword-level content filter, the budget covers
  the chat path only, IPv6 dual-stack, the four time-bounded `:latest`
  pins (review-by 2027-01-31).
