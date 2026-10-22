# ScarLiX OS — Regression Matrix (v19.4.3, KPK-07)

**Purpose:** explicit finding → reproducer → fix → regression test → evidence
chains for the historical findings cited in the current baseline. Per the KpK
review (2026-10-22, KPK-07): closure is NOT assumed just because the latest
smoke suite passes — every row lists the exact test and command; rows without
a dedicated regression test are marked **OPEN**.

**How to read:** *Finding ID* is preserved as cited in the source (smoke check
headers, audit docs, design docs). *Guard* is the regression test that fails
if the old behavior returns. *Command* proves it on any clone.

## Shell-level regressions (guarded by scarlix-smoke-test.sh)

| Finding | Description (expected behavior) | Guard | Command |
|---|---|---|---|
| v19.2.12 F-01 | Pi-Bolt installer raw `curl\|sh` — must be SHA-256 pinned, temp download, verify, then execute | check_12 | `bash files/usr/local/bin/scarlix-smoke-test.sh --offline` |
| v19.2.12 F-02 | Single-GPU hosts assumed multi-GPU — GPU_COUNT detection + vLLM gate + sharing opt-in must exist | check_11 | same |
| v19.2.12 P2-01 / v19.2.13 KPK-P2-01 | Root-owned 0600 temp executed as user — verified installer must be handed to a user-owned copy before non-root execution | check_13 | same |
| v19.2.12 P2-02 / v19.2.13 KPK-P2-02 | GPU-sharing opt-in accepted fuzzy values — only exact `SCARLIX_ALLOW_SHARED_GPU=1` may enable; duplicates fail closed | check_14 | same |
| v19.2.12 F-04 | Reservation governor must be fail-closed, disabled by default, off the HTTP plane | check_17 | same |
| v19.2.1 F-02 | CI never executed the smoke suite — now the full suite runs in CI on every push | CI job `smoke-test` | check run URL on any commit |
| v17.9.8 | ScarliHQ compose must not mount docker.sock (privilege boundary) | CI job `compose-validation` (docker.sock rejection) | CI check run |
| v18.7.8 P1 | `grep -c \|\| echo 0` produced `$'0\n0'` — integer comparison crash in compose validation | CI job `compose-validation` (`\|\| true` + explicit empty check) | CI check run |
| v18.7.6 P2 | Floating PyYAML dep in CI — pinned `pyyaml==6.0.2` | CI job `yaml-validation` | CI check run |
| v18.8.2 B-a/c | Buzz services ran in the default stack (`:latest`) — must be profile-gated opt-in | check_19 (v19.4.3: whole profile disabled — see below) | same |
| v19.0.3 P1-3 | Wrong buzz image repo (`ghcr.io/block/buzz-relay` — does not exist) — corrected to `ghcr.io/block/buzz` | check_19 (profile disabled; historical image reference preserved in the quarantined file) | same |
| v19.4.2 audit §6.1 / KPK-03 (2026-10-22) | minio/minio pin dead upstream — buzz profile must stay formally disabled; dead pin quarantined in `.disabled` file | check_19 | same |
| KPK-02 (2026-10-22) | Registry checks must classify VERIFIED_EXISTS / MISSING / UNVERIFIED; UNVERIFIED never reported as existing | `tests/test_registry_classification.sh` (68 assertions incl. negative controls) | `bash tests/test_registry_classification.sh` |
| KPK-04 (2026-10-22) | Smoke check count documentation drift — schedule must equal defined functions; summary reports executed count | `verify_check_inventory` (runs on every smoke invocation) + test suite negative controls | same two commands |
| v19.4.2 audit §6.1 (P0 detector) / v19.4.3 live catch | Sunshine nightly tag pruned 2 days after pinning — digest-aware probes must catch dead digests (MISSING) and live digests (VERIFIED_EXISTS) | check_07 digest-aware probe list (14 pins) + test suite digest scenarios | smoke online run; test suite |

## Go-level regressions (guarded by go test)

| Finding | Description | Guard (test) | Command |
|---|---|---|---|
| AK-1 review finding (2026-10-20, pre-merge) | Policy.Evaluate did not verify that the agent's capability set contains the capability (fail-open); test discarded the capability set (`_ = caps`) | `TestPolicyEvaluate_Denials` (rule `capability-not-granted`, nil caps), `TestPolicyEvaluate_NotGrantedReason` | `cd scarlihq && go test ./internal/agentkernel/ -run TestPolicyEvaluate -v` |
| v18.7.4-P0 precedent (WS ticket RNG) | RNG failure must fail closed on ID generation — applied to agent session IDs | `TestStoreCreateRNGFailClosed` | `go test ./internal/agentkernel/ -run TestStoreCreateRNGFailClosed -v` |
| AK-2 store invariants | Grants must not be mutable through returned records (deep copies); bounded capacity counts CLOSED too; deterministic listing | `TestStoreGetListCopies`, `TestStoreCapacity`, `TestStoreListDeterministicOrder` | `go test ./internal/agentkernel/ -v` |
| AK-2 API surface | Sessions disabled by default (503); auth required (401); shell.exec / unknown fields / malformed bodies rejected | `TestAgentKernelRoutesDisabledByDefault`, `TestAgentKernelRoutesRequireAuth`, `TestAgentSessionCreateValidation`, `TestAgentSessionEvaluateValidation` | `go test ./internal/api/ -run TestAgent -v` |
| Reservation store integrity | Duplicate/corrupt/non-canonical reservation records must be rejected on load; file permissions enforced | `TestStore_Append_RejectsDuplicate`, `TestStore_Append_RejectsCorruption`, `TestStore_Load_RejectsNonCanonicalID`, `TestStore_FilePermissions` | `go test ./internal/reservation/ -v` |
| Resource ID canonicalization | GPU UUID case variants must canonicalize to the same ID (case-insensitive collisions) | `TestCanonicalResourceID_GPUUUIDCaseCanonicalized`, `TestCanonicalResourceID_CaseVariantsSameCanon` | `go test ./internal/reservation/ -run TestCanonicalResourceID -v` |
| Mode / GPU opt-in parsing | Strict parse of GPU opt-in env in Go surface (mirrors check_14) | `internal/scarlix_mode` tests | `go test ./internal/scarlix_mode/ -v` |
| Gaming API read-only | /api/gaming must be GET-only, auth-gated, pure data | `internal/gaming` tests + check_16 | `go test ./internal/gaming/ -v` |

## Open gaps (honest — no dedicated regression test yet)

| Finding | Why open | Planned guard |
|---|---|---|
| Historical parser/GPU-`unknown` findings from pre-v19.2.11 audits (v19.2.9 deep audit and earlier) | The v19.2.9 audit doc lists findings whose fixes predate the current check inventory; several are covered only indirectly (e.g. check_11 covers single-GPU adaptation, but not every parser edge case cited) | enumerate in a follow-up audit pass; add targeted checks where the source finding is reproducible |
| Backup/restore findings cited in v19.2.11–v19.2.13 KPK docs | docker-backup hook is shellcheck'd and bash-syntax'd (check_01/02 + CI), but no end-to-end restore regression test exists (needs a host with docker) | restore drill on the target host; record in HARDWARE_ACCEPTANCE_PROTOCOL record |
| H-1..H-6 hardware acceptance (KPK-06) | requires the target machine | docs/HARDWARE_ACCEPTANCE_PROTOCOL.md execution |

## Full-suite verification commands (run at the reviewed commit and at release)

```bash
cd scarlihq
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
cd ..
bash tests/test_registry_classification.sh
bash files/usr/local/bin/scarlix-smoke-test.sh          # online
bash files/usr/local/bin/scarlix-smoke-test.sh --offline
```

Results for v19.4.3 are archived in `docs/RELEASE_GATE_v19.4.3.md`.
