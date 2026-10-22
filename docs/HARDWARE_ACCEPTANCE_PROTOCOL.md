# ScarLiX OS — Hardware Acceptance Protocol (v19.4.3, KPK-06)

**Purpose:** target-host acceptance record template. Passing CI and static
smoke checks do NOT prove that ScarLiX OS operates correctly on the target
host. This protocol separates evidence classes so a green CI run can never
be presented as hardware acceptance (KpK review 2026-10-22, finding KPK-06).

**Status: OPEN — NOT EXECUTED.** No row in any record derived from this
protocol may be marked `HARDWARE_VERIFIED` until it is filled on the target
machine with dated command output.

## Evidence classes

| Class | Meaning | Who can produce it |
|---|---|---|
| `TESTED_IN_CI` | GitHub Actions run on a specific commit (check run URL) | CI |
| `OWNER_ATTESTED` | Owner-executed command output on the target host, dated | Host owner |
| `HARDWARE_VERIFIED` | Reproducible target-host record: environment captured + command + output + pass criteria met | Host owner (this protocol) |
| `IMPLEMENTED` | Code exists on a specific commit | Repository |
| `PLANNED` | Honest open item | — |

A claim is `HARDWARE_VERIFIED` only when ALL of: the environment block below
is filled, the command and its exit status are recorded, and the pass
criterion is met. Partial records stay `OWNER_ATTESTED` at best.

## Environment capture (fill once per record)

| Field | Value |
|---|---|
| Date (YYYY-MM-DD, timezone) | ______ |
| ScarLiX OS version + commit SHA | ______ |
| CPU / RAM | ______ |
| GPU model(s) + count + driver version | ______ |
| CUDA / runtime version | ______ |
| OS / kernel | ______ |
| Docker version | ______ |
| Models present (names + sizes) | ______ |
| Relevant env (SCARLIX_* flags actually set) | ______ |

## Required checks (per KPK-06)

### H-1 — GPU discovery and assignment
- [ ] `scarlihq` logs GPU discovery for every installed GPU; assignment list
      matches `nvidia-smi` output.
- [ ] Single-GPU host: correct single-GPU path (no multi-GPU assumptions).
- [ ] Unknown-state GPU: discovery reports `unknown` explicitly and fails
      closed (no silent skip).
- Command + exit status: ______ Result: ______

### H-2 — AI runtime start + real inference request
- [ ] Configured AI runtime starts (compose profile up).
- [ ] A REAL inference request is issued through the gateway and answered
      (record model, latency, tokens).
- Command + exit status: ______ Result: ______

### H-3 — Gateway and fallback path
- [ ] Primary gateway path answers.
- [ ] Fallback path exercised (primary deliberately stopped); requests still
      served or failed closed per design — record which.
- Command + exit status: ______ Result: ______

### H-4 — Mode transitions (AI / game / AI+game)
- [ ] `scarlix-mode` transitions between AI, game and combined modes (if
      enabled on this host).
- [ ] GPU allocation follows the mode contract (dedicated vs shared with
      explicit SCARLIX_ALLOW_SHARED_GPU=1).
- Command + exit status: ______ Result: ______

### H-5 — Restart recovery
- [ ] Host restarted; reservation/workload state recovers per design
      (reserved stays reserved, released stays released; no phantom state).
- Command + exit status: ______ Result: ______

### H-6 — Disabled-by-default services stay disabled
- [ ] After full stack start WITHOUT explicit opt-in: agent kernel session
      API answers 503 (SCARLIHQ_AGENTKERNEL_ENABLED unset); reservation
      governor does not run; every `[buzz]`-style opt-in profile is not
      started (buzz is disabled entirely as of v19.4.3 — see
      workspace/buzz/docker-compose.yml.disabled).
- Command + exit status: ______ Result: ______

## Record custody

- Each completed record is committed to `docs/` as
  `HARDWARE_ACCEPTANCE_<date>_<version>.md` (see
  `docs/RELEASE_19.3.0_HARDWARE_ACCEPTANCE.md` for the v19.3.0 precedent).
- CI evidence (check run URLs) is recorded in the release gate document, not
  here — classes never mix.

## Current status (2026-10-22, v19.4.3)

| Item | Class |
|---|---|
| v19.4.3 code changes | `IMPLEMENTED` |
| CI gate (all jobs incl. online registry checks + deterministic tests) | `TESTED_IN_CI` (see RELEASE_GATE_v19.4.3.md) |
| H-1..H-6 on the target host | `PLANNED` — protocol above, not yet executed |
