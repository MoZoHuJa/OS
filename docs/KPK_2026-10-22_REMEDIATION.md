# KpK Review 2026-10-22 — Findings Remediation (v19.4.3)

**Reviewed baseline:** v19.4.2, commit `3d79d53582c366810bc17fce655e1599b4ee6e58`
**Review document:** `ScarLiXOS_v19.4.2_KpK_Findings_and_Fixes_EN.md` (external, 2026-10-22)
**Remediation release:** v19.4.3
**Method:** every finding was re-verified against the repository and live registries
BEFORE fixing. Evidence classes used throughout (KPK-08):
`IMPLEMENTED` (code shipped), `TESTED_IN_CI` (CI job proves it),
`HARDWARE_VERIFIED` (target-host record — only where a dated record exists),
`PLANNED` (honest open item). A green CI run is never presented as hardware
acceptance.

---

## KPK-01 — CI skipped registry pin-existence checks (P1)

**Status:** `IMPLEMENTED` (already fixed in v19.4.2 commit 3d79d53) + `TESTED_IN_CI` (new in v19.4.3)

- Re-verified: `.github/workflows/ci.yml` smoke-test job invokes the script
  without `--offline`; the 2026-10-22 CI run on 3d79d53 executed the registry
  checks online (12/12 "exists" evidence in the v19.4.2 release notes).
- v19.4.3 addition (the review asked for tests of every response class):
  `tests/test_registry_classification.sh` — deterministic, network-free,
  wired into the CI smoke-test job as a second step. Covers HTTP 404,
  401/403/429/5xx/000, timeout (curl 28), DNS failure (curl 6), malformed
  auth JSON, empty token. 68 assertions, 0 failures (local run archived in
  `docs/RELEASE_GATE_v19.4.3.md`).
- Acceptance confirmed: a confirmed 404 FAILs (mock test proves FAIL event);
  transient failures WARN; auth/rate-limit failures are classified
  UNVERIFIED and can never increment the PASS counter (asserted).

## KPK-02 — Registry verification inconclusive for non-404 (P1)

**Status:** `IMPLEMENTED` + `TESTED_IN_CI` (v19.4.3)

Explicit classification table now lives in
`files/usr/local/bin/scarlix-smoke-test.sh` (check_07):

| Registry answer | Verdict | Event |
|---|---|---|
| manifest HTTP 200 (authoritative) | `VERIFIED_EXISTS` | PASS |
| manifest HTTP 404 | `MISSING` | FAIL |
| HTTP 401/403/429/5xx/3xx/unknown | `UNVERIFIED` | WARN |
| transport error (timeout/DNS/TLS/malformed JSON/empty token) | `UNVERIFIED` | WARN |
| auth endpoint non-200 / malformed / empty | `UNVERIFIED` | WARN |

- The transport exit code WINS over any captured status: a timeout can never
  confirm NOR deny (fail-closed against false MISSING as well as false
  VERIFIED_EXISTS).
- CI output literally contains `VERIFIED_EXISTS` / `MISSING` / `UNVERIFIED`.
- An UNVERIFIED result is asserted (in the test suite) to never contain
  "VERIFIED_EXISTS" and to never increment PASS_COUNT.
- Deterministic tests with mocked registry responses:
  `tests/test_registry_classification.sh` (68 assertions; runs in CI).

## KPK-03 — Buzz/MinIO image pin unavailable (P0)

**Status:** `IMPLEMENTED` + `TESTED_IN_CI` (v19.4.3) — resolved by formal disablement

Fresh independent registry probes (2026-10-22, this remediation — not just
the audit document):

| Channel | Probe | Result |
|---|---|---|
| Docker Hub repo API | `GET /v2/repositories/minio/minio/` | **404 object not found** |
| Docker Hub registry API | `GET /v2/minio/minio/manifests/latest` with anonymous pull token | **401 UNAUTHORIZED** ("authentication required", repository `minio/minio` action pull) — with a *valid* token this means the repository is gone (control group below) |
| quay.io | anonymous token + `GET /v2/minio/minio/tags/list` | **401 UNAUTHORIZED** — no public tags |
| dl.min.io | release dir + archive | **410 Gone** (both) |
| GitHub source | `api.github.com/repos/minio/minio` | **archived: true** (2026-04-24); last tag `RELEASE.2025-10-15T17-29-55Z` |

Methodology control (validates that 401/404 conclusions are meaningful):
`library/redis` and `library/nextcloud` repo APIs answered **200**, and
`registry-1.docker.io/v2/library/redis/manifests/7-alpine` answered **200**
with the same token method. A 404 from the repo API plus a 401 from the
registry API with a valid pull token = the repository is deleted.

Decision per the review's own decision tree ("identify and verify a supported
upstream image/source, **or** formally remove/disable the affected
integration"; "if no supported source is available, keep it opt-in and clearly
mark the profile unavailable"):

- No supported upstream source exists (all channels dead, source archived —
  building from an archived repo with no security updates was deliberately
  NOT chosen; no third-party mirror was used).
- **The [buzz] profile is formally disabled:**
  `workspace/buzz/docker-compose.yml` → `workspace/buzz/docker-compose.yml.disabled`
  (renamed — no compose tooling picks it up; CI compose-validation and smoke
  check_03 globs match `*.yml` only).
  The dead `minio/minio:RELEASE.2024-10-13T13-34-50Z` pin is quarantined
  inside the disabled file with the full probe evidence and a re-enable
  runbook in its header.
- New smoke **check_19** guards the quarantine: an active
  `workspace/buzz/docker-compose.yml` referencing minio/minio FAILS CI; the
  disabled file with the quarantined pin PASSES. The profile cannot be
  silently re-enabled.
- Not switched to a guessed version or unverified mirror (per review).
- "Add a clean-environment test that pulls and starts the selected image" —
  not applicable: no image was selected; the integration is disabled. Pull
  verification applies to the future re-enable runbook step 2
  (`docker manifest inspect`).

## KPK-04 — Smoke-test documentation vs actual check count (P2)

**Status:** `IMPLEMENTED` + `TESTED_IN_CI` (v19.4.3)

- The header no longer hardcodes a count; the summary now reports
  `N checks executed` dynamically (v19.4.3 local run: 19 checks executed).
- New `verify_check_inventory()` guard: the scheduled `SMOKE_CHECKS` array is
  compared against the defined `check_*` functions before execution — any
  drift (defined-but-not-scheduled OR scheduled-but-not-defined) emits a FAIL
  event and fails the suite. This IS the review's requested "test validating
  the summary against the executed check inventory", and it runs on every
  CI invocation.
- The test suite additionally injects a stray `check_99_stray` function and a
  bogus scheduled entry and asserts both are detected (negative controls).
- Checks vs events are now distinguished in the summary output
  ("19 checks executed — 32 passed, 0 failed, 0 warned (events)").

## KPK-05 — Full Go test suite not independently reproduced (P2)

**Status:** `IMPLEMENTED` (reproduced and archived; not a code defect)

Reproduced against the EXACT reviewed commit `3d79d53` (git rev-parse run in
the same shell, network-enabled environment, Go 1.23.4):

| Command | Result |
|---|---|
| `go vet ./...` | exit 0, no findings |
| `go test -count=1 ./...` | **19/19 packages ok, exit 0** |
| `go test -race -count=1 ./...` | **19/19 packages ok, exit 0** (full race suite — not only security-sensitive packages) |

Full command output and exit statuses archived in
`docs/RELEASE_GATE_v19.4.3.md` §2. No reproducible test failure was obtained —
per the review, this is not labeled a code defect.

## KPK-06 — Runtime and hardware acceptance unverified (P1)

**Status:** `PLANNED` — protocol delivered, execution requires the target host

This remediation was produced in a network-enabled sandbox WITHOUT the target
GPUs; nothing here is claimed as hardware acceptance. What v19.4.3 delivers:

- `docs/HARDWARE_ACCEPTANCE_PROTOCOL.md` — a dated acceptance record template
  that separates `TESTED_IN_CI` / `OWNER_ATTESTED` / `HARDWARE_VERIFIED`
  evidence, with the exact checklists the review demanded (GPU discovery and
  assignment incl. single-GPU and unknown-state, real inference request,
  gateway and fallback path, mode transitions, restart recovery,
  disabled-by-default services, environment capture).
- The protocol is the required artifact before any production-readiness
  claim; the README roadmap marks it OPEN until executed on the target host.

## KPK-07 — Historical regression findings traceability (P2)

**Status:** `IMPLEMENTED` (matrix delivered) — per-finding reproduction gaps marked OPEN

- `docs/REGRESSION_MATRIX.md` — every historical finding cited in the current
  baseline (smoke check headers, Go test files, past audits) mapped to its
  regression test and the exact command that proves it. Findings without a
  dedicated regression test are marked OPEN rather than assumed closed.
- The matrix is generated from the repository itself (check headers cite
  finding IDs; each entry lists the guarding check/test), so the chain
  finding → reproducer → fix → regression test → evidence is explicit.

## KPK-08 — Version and evidence claims alignment (P2)

**Status:** `IMPLEMENTED` (v19.4.3 and onward)

- `docs/RELEASE_GATE_v19.4.3.md` records the exact commit SHA, environment,
  every gate command with its exit status and output, and the
  IMPLEMENTED / TESTED_IN_CI / HARDWARE_VERIFIED / PLANNED classes for every
  v19.4.3 claim.
- No claim of online registry verification is made anywhere for checks that
  were skipped or inconclusive (the classification table makes this
  structurally impossible now: only manifest HTTP 200 prints
  VERIFIED_EXISTS).
- Release notes for v19.4.3 separate CI evidence from owner-attested results
  and hardware status (see the GitHub Release body).

---

## Live finding during remediation (not in the review): sunshine tag pruned

While running the hardened check_07 against the live registries on
2026-10-22, the P0 detector caught a REAL dead tag two days after v19.4.2:

- `lizardbyte/sunshine:v2026.1020.123939-ubuntu-24.04` (pinned in v19.4.2,
  "newest verified nightly 2026-10-20") → manifest HTTP **404** (tag pruned).
- The pinned **digest `sha256:5c790c79…` is still alive** — upstream now
  maintains rolling `master-*` / `latest-*` tags referencing exactly this
  digest (verified via the tags API: `master-ubuntu-24.04` and
  `latest-ubuntu-24.04` both point at it).
- Docker pulls `tag@digest` BY DIGEST, so a clean deploy was never broken by
  the pruned tag NAME — but a tag-only probe would have reported a false
  MISSING, and the digest itself would eventually be garbage-collected once
  untagged.

Remediation shipped in v19.4.3:

1. `gaming/docker-compose.yml`: the pin's tag-name portion re-pinned to the
   rolling tag `master-ubuntu-24.04` with the SAME digest — byte-identical
   image content, zero functional change, documented in the compose comment.
2. `check_07` is now **digest-aware**: pins written `tag@sha256` are probed at
   the digest endpoint (what a clean deploy actually pulls). A pruned tag
   with a live digest is VERIFIED_EXISTS; a dead digest is MISSING.
3. The guard list now covers **every digest-pinned image in active compose
   files** (coolify, sunshine, crowdsec, ollama, headscale, hermes-agent,
   llama.cpp, pumpkin) in addition to the tag-only audit pins — 14 probes,
   all VERIFIED_EXISTS on 2026-10-22 (see RELEASE_GATE_v19.4.3.md §3).

This is the P0 detector doing exactly what it was built for (audit §6.1),
now with correct digest semantics.
