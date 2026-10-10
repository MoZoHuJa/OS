<p align="center">
  <img src="docs/scarlixos-banner.png" alt="ScarLiXoS v19.2.0 — Sovereign AI Cloud" width="100%" />
</p>

<h1 align="center">SCARLIX OS v19.2.0</h1>

<p align="center">
  <strong>Suverénny domáci OS pre AI cloud, coding, gaming a rodinnú zábavu.</strong><br/>
  Architektúra Secure Host-Bridge · Token autentifikácia · State machine · Privilege boundary
</p>

<p align="center">
  <a href="https://github.com/MoZoHuJa/OS/releases/tag/v19.2.0"><img alt="Version" src="https://img.shields.io/badge/version-v19.2.0-06b6d4?style=flat-square" /></a>
  <a href="https://github.com/MoZoHuJa/OS/blob/main/LICENSE"><img alt="License" src="https://img.shields.io/badge/license-MIT-14b8a6?style=flat-square" /></a>
  <a href="https://github.com/MoZoHuJa/OS"><img alt="Base" src="https://img.shields.io/badge/base-EndeavourOS%20%28Arch%29-10b981?style=flat-square" /></a>
  <a href="https://github.com/MoZoHuJa/OS/actions"><img alt="CI" src="https://img.shields.io/badge/CI-GitHub%20Actions-22d3ee?style=flat-square" /></a>
</p>

---

> **Working AI Path**: model-aware, fail-hard, healthcheck + fallback.
> **Verified**: SGLang (GPU0, --disable-flashinfer) + vLLM (GPU1, TP=1, experimental) + BeeLlama (CPU) + Ollama (CPU tertiary fallback).
> **LiteLLM Gateway** (v19.0.0+): unified OpenAI-compatible API on :4001. Uses a **simplified 3-tier fallback** (SGLang → Ollama → BeeLlama) for external clients — vLLM excluded because it's experimental (.experimental only). scarlix-mode's direct AI path keeps the full 4-tier including vLLM.

**Version:** v19.2.0 | **Base:** EndeavourOS (Arch) | **License:** MIT

## 🚀 Install (NO ISO)

### Primary (safe — review first) ⭐
```bash
git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
cd ~/scarlix-os
git checkout v19.2.0   # ALWAYS checkout specific tag (main may be ahead)
nano install.sh         # review
bash install.sh
```

### Post-install (3 steps):
```bash
# 1. Reboot (activate NVIDIA driver + docker group takes effect on re-login)
sudo reboot

# 2. Download models (50-150GB, takes hours — disk-space pre-checked)
download-models.sh

# 3. Start AI inference
scarlix-mode ai

# 4. (optional) Open dashboard — token printed by install.sh
#    http://127.0.0.1:8090/  (localhost only — use Tailscale/SSH tunnel for LAN)
```

---

## 🆕 What's New in v19.1.15 (vs v19.1.14)

**Data contract integrity + security gate fixes.** 5 P1 + 4 P2 from 3 independent reviews.

Three reviews of v19.1.14 found critical data contract mismatches (GPU health ID, runtime metadata version), security gate semantic bugs (vLLM trust-remote-code), and systemd anti-patterns. v19.1.15 fixes all.

### P1 fixes

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **vLLM trust-remote-code boolean gate** | `${VLLM_TRUST_REMOTE_CODE:+--trust-remote-code}` — `false`/`0`/`no` all enabled the flag (any non-empty = true) | scarlix-mode validates: `true`/`1`/`yes` → `VLLM_TRUST_FLAG="--trust-remote-code"`, else empty. Compose uses `${VLLM_TRUST_FLAG:-}` |
| **P1-2** | **agent.env permissions** | `umask 077` only on mkdir (first bash -c), cat ran in separate bash -c with default umask → file was 644, not 600. `ok()` was unconditional | Single `bash -c` with `umask 077` wrapping mkdir + cat + chmod 600. Conditional `ok/warn` based on success |
| **P1-3** | **GPU health ID mismatch** | `CollectGPUHealth` used `fmt.Sprintf("gpu.%d", index)` → `"gpu.0"`, but scheduler looked up `gpu.ID` = `"gpu.nvidia.0"` → health never matched → unhealthy GPUs could be selected | `CollectGPUHealth` now uses `gpu.ID` (`"gpu.nvidia.0"`) — matches scheduler lookup |
| **P1-4** | **runtime.go + smoke test LiteLLM version drift** | `runtime.go` had `main-v1.21.7`, smoke test had `main-v1.21.7`, while compose had `main-v1.23.9` | Both updated to `main-v1.23.9` (matches production) |
| **P1-5** | **model-manager.timer Requires= anti-pattern** | `Requires=model-manager.service` could trigger service at boot instead of waiting for OnCalendar=Mon 04:00 | Removed `Requires=` — timer targets service by basename convention |

### P2 fixes

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P2-1** | **scarlix CLI header** | v19.0.7 (stale by 8 releases) | v19.1.15 |
| **P2-2** | **/mnt subdirs ownership** | After removing `chown -R /mnt`, subdirs (/mnt/files, /mnt/games, /mnt/photos) were root:root — user couldn't write | Individual `chown` for each subdir (non-recursive) |
| **P2-3** | **models.yaml trust_remote_code** | Key missing — scarlix-mode read empty default, but single-source-of-truth was incomplete | Added `trust_remote_code: false` to vllm section + yq read in scarlix-mode |
| **P2-4** | **Version headers → v19.1.15** | models.yaml, scarlix-mode, scarlix CLI, Dockerfile, Pi-Bolt, model-manager.timer | All → v19.1.15 |

### Verification
```
gofmt -l .              → EMPTY (clean)
go vet ./...            → CLEAN
go test ./...           → 10 packages all OK (no regression)
bash -n                 → OK
YAML lint               → OK
scarlix-smoke-test.sh  → 13 passed, 0 failed, 0 warned
CI (all 8 jobs)        → expected PASS (compose-validation fix from 1772fe8 confirmed)
```

### v19.1.x Resource Foundation + Hardening — FINAL
```
v19.1.0–v19.1.5   Resource Foundation
v19.1.6–v19.1.9   Resource View + Scheduler + Compatibility + Release freeze
v19.1.10–v19.1.12  Scheduler Correctness + Semantics + Snapshot Integrity
v19.1.13          Release Integrity (gofmt + security + deployment)
v19.1.14          CI Green + Security Hardening
v19.1.15          Data Contract Integrity + Security Gate  ← FINAL
```

**v19.1.x is now FROZEN. v19.2.0 Compute Fabric can safely begin.**

---

## 🆕 What's New in v19.1.14 (vs v19.1.13)

**CI Green + security hardening.** 1 P1 + 4 P2 from 2 independent reviews.

Two reviews of v19.1.13 found that compose-validation CI was failing (fixed in `1772fe8`) + LiteLLM CI test drift + vLLM trust-remote-code should be env-gated + missing binaries in sha256sums. v19.1.14 fixes all remaining issues.

### P1 fix

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1** | **LiteLLM CI test drift** | CI integration test used `main-v1.16.19` while production uses `main-v1.23.9` → test-drift | CI now uses `main-v1.23.9` (matches production) + health URL updated to `/health/liveliness` |

### P2 fixes

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P2-1** | **vLLM --trust-remote-code env-gated** | Flag was always on (only documented) — supply-chain risk | Now `${VLLM_TRUST_REMOTE_CODE:+--trust-remote-code}` — disabled by default, enabled via `VLLM_TRUST_REMOTE_CODE=true` in .env |
| **P2-2** | **minio:latest pinned** | `minio/minio:latest` (unpinned, profile-gated) | `minio/minio:RELEASE.2024-10-13T13-34-50Z` |
| **P2-3** | **generate-sha256sums.sh updated** | Missing 7 new Go binaries + scarlix CLI | Added: scarlix, scarlix-smoke-test.sh, scarlix-bridge-reader, scarlix-gpu, scarlix-inventory, scarlix-contract, scarlix-monitor, scarlix-scheduler |
| **P2-4** | **Version headers → v19.1.14** | models.yaml, scarlix-mode, Pi-Bolt config, Dockerfile | All → v19.1.14 |

### Verification
```
gofmt -l .              → EMPTY (clean)
go vet ./...            → CLEAN
go test ./...           → 10 packages all OK
bash -n                 → OK
YAML lint               → OK
scarlix-smoke-test.sh  → 13 passed, 0 failed, 0 warned
CI (compose-validation) → PASS (fixed in 1772fe8, confirmed green)
```

### v19.1.x Resource Foundation + Hardening — FINAL (CI GREEN)
```
v19.1.0–v19.1.5   Resource Foundation
v19.1.6–v19.1.9   Resource View + Scheduler + Compatibility + Release freeze
v19.1.10          Scheduler Correctness
v19.1.11          Contract Semantics Hardening
v19.1.12          Resource Snapshot Integrity
v19.1.13          Release Integrity (gofmt + security + deployment)
v19.1.14          CI Green + Security Hardening  ← FINAL (CI GREEN ✅)
```

**v19.1.x is now FROZEN. CI passes all 8 jobs. v19.2.0 Compute Fabric can safely begin.**

---

## 🆕 What's New in v19.1.13 (vs v19.1.12)

**Release Integrity — P0 gofmt fix + P1 security/deployment fixes.** 1 P0 + 5 P1 + 5 P2 from 3 independent reviews.

Three reviews of v19.1.12 found a P0 CI blocker (gofmt gate FAIL on 29 files) + 5 P1 issues (install.sh regression, compat matrix, chown /mnt, LiteLLM security, vLLM trust-remote-code). v19.1.13 fixes all.

### P0 fix (CI blocker)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P0** | **gofmt -w . on 29 Go files** | CI gofmt check added in v19.1.12 but 29 files had spaces-instead-of-tabs → CI FAILED → go build + go test SKIPPED | `gofmt -w .` applied → `gofmt -l .` returns empty → CI will pass |

### P1 fixes (security + deployment)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **install.sh copy loop regression** | `cp && chmod && ok \|\| warn && continue` made ALL copy failures non-fatal (including core: scarlix-mode, scarlix-doctor) | Proper if/else: `crit` for core binaries, `warn` only for optional Go binaries |
| **P1-2** | **compat matrix CPU≠GPU** | `gpuAssignedToRuntime()` returned `true` for empty GPUIDs → beellama marked as GPU-compatible | Returns `false` for empty GPUIDs — CPU runtimes are NOT GPU-compatible |
| **P1-3** | **chown -R /mnt removed** | `chown -R "$REAL_USER" /mnt` — dangerous on systems with existing /mnt/photos, /mnt/games | `mkdir -p /mnt/scarlix` + `chown` only the scarlix subdirectory |
| **P1-4** | **LiteLLM security bump** | `main-v1.21.7` (multiple CVEs: CVE-2026-59822, CVE-2026-59823, CVE-2026-35029, CVE-2025-45809) | `main-v1.23.9` (latest stable, all known CVEs fixed) |
| **P1-5** | **vLLM --trust-remote-code security warning** | Flag present without any security comment | Added prominent security comment: supply-chain risk, trusted models only, explicit opt-in |

### P2 fixes (documentation + hygiene)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P2-1** | **agent.env symlink race** | Root wrote file then chown — user could pre-create symlink | Write as user (`sudo -u` + `umask 077`) — no root write, no race |
| **P2-2** | **Stale version headers** | models.yaml v19.1.11, scarlix-mode v19.1.11, Pi-Bolt config v19.1.12 | All → v19.1.13 |
| **P2-3** | **CI trigger v18.\* → v\*** | CI only triggered on `main` + `v18.*` branches | Now triggers on `main` + `v*` (covers v19, v20, etc.) |
| **P2-4** | **Pi-Bolt config text cleanup** | Duplicate sentence from merge: "(generated by install.sh...) (generated by generate-env.sh...)" | Cleaned to single sentence |
| **P2-5** | **Dockerfile header** | v19.1.12 | v19.1.13 |

### Verification
```
gofmt -l .              → EMPTY (all 29 files formatted)
go vet ./...            → CLEAN
go test ./...           → 10 packages all OK (no regression)
bash -n install.sh      → OK
YAML lint (26 files)   → ALL OK
scarlix-smoke-test.sh  → 13 passed, 0 failed, 0 warned
```

### v19.1.x Resource Foundation + Hardening — FINAL
```
v19.1.0–v19.1.5   Resource Foundation (contract, registries, monitor, telemetry)
v19.1.6–v19.1.9   Resource View + Scheduler + Compatibility + Release freeze
v19.1.10          Scheduler Correctness (3 P1)
v19.1.11          Contract Semantics Hardening (8 P1 + 5 P2)
v19.1.12          Resource Snapshot Integrity (4 P1 + 4 P2)
v19.1.13          Release Integrity (1 P0 + 5 P1 + 5 P2)  ← FINAL
```

**v19.1.x is now FROZEN. CI will pass. v19.2.0 Compute Fabric can safely begin.**

---

## 🆕 What's New in v19.1.12 (vs v19.1.11)

**Resource Snapshot Integrity — final P1 fixes from 2 independent reviews.** 4 P1 + 4 P2.

Two reviews of v19.1.11 found 4 remaining P1 issues in the scheduler + production CLI path. v19.1.12 fixes all — this is the **final integrity release** before v19.2.0 Compute Fabric.

### P1 fixes (scheduler + CLI)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **CPU runtime not on GPU path** | `scoreRuntime()` let CPU-only runtimes (empty GPUIDs) leak into GPU scoring → beellama could be selected for cuda task | Explicit `if len(rt.GPUIDs) == 0 { continue }` — CPU runtimes excluded from GPU path |
| **P1-2** | **scoreModel continue (not return)** | `return "", 0, ScoreBreakdown{}` on first incompatible model → aborted loop, never tried model B | `continue` — tries ALL models, returns best. Model A rejected → Model B selected |
| **P1-3** | **scarlix-scheduler CLI collects runtime health** | CLI sent only `CollectGPUHealth()` to scheduler → runtime health map was empty → down runtimes never hard-rejected in production path | CLI now calls `CollectRuntimeHealth()` + combines GPU+runtime health → scheduler sees runtime health in production |
| **P1-4** | **Plan() doesn't mutate caller's contract** | `c.Compute.Accelerator = "cuda"` mutated the caller's struct → side effect | Local `accelerator` variable — caller's contract unchanged |

### P2 fixes (documentation + CI)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P2-1** | **gofmt CI actually added** | README claimed gofmt in CI but `ci.yml` had no gofmt step (false claim) | `gofmt -l .` step added to CI after `go vet` — exits 1 on unformatted files |
| **P2-2** | **Dockerfile header** | v19.1.10 (stale by 2 releases) | v19.1.12 |
| **P2-3** | **Pi-Bolt config.template.json** | v19.0.10 + security text referenced old `/etc/scarlix/.env` pattern | v19.1.12 + references `~/.config/scarlix/agent.env` (v19.1.11 pattern) |
| **P2-4** | **Monitor file-header comments** | Said "currently a stub" / "coming in v19.1.5+" (server implemented since v19.1.6) | Updated to "HTTP server mode implemented in v19.1.6+" |

### New tests (4)
```
TestPlan_CPURuntimeNotOnGPUPath              — beellama (empty GPUIDs) NOT selected for cuda request
TestPlan_MultiModelSelectionSkipsIncompatible — Model A (no "coding") → skip → Model B (has "coding") → selected
TestPlan_EmptySupportedRuntimesNotRejected   — empty SupportedRuntimes = "supports all"
TestPlan_DoesNotMutateContract               — Plan() leaves caller's Accelerator unchanged
```

### Test results
```
go vet ./internal/scheduler/      → CLEAN
go test ./internal/scheduler/ -v   → 21 PASS / 0 FAIL (17 existing + 4 new)
go test ./...                      → 10 packages all OK (no regression)
scarlix-smoke-test.sh             → 13 passed, 0 failed, 0 warned
```

### v19.1.x Resource Foundation + Hardening — FINAL
```
v19.1.0–v19.1.5   Resource Foundation (contract, registries, monitor, telemetry)
v19.1.6–v19.1.9   Resource View + Scheduler + Compatibility + Release freeze
v19.1.10          Scheduler Correctness (3 P1: CPU, capabilities, down runtime)
v19.1.11          Contract Semantics Hardening (8 P1 + 5 P2)
v19.1.12          Resource Snapshot Integrity (4 P1 + 4 P2)  ← FINAL RELEASE
```

**v19.1.x is now FROZEN. v19.2.0 Compute Fabric can safely transition from dry-run → real allocation.**

---

## 🆕 What's New in v19.1.11 (vs v19.1.10)

**Scheduler Contract Semantics Hardening + Pi-Bolt deployment fixes.** 8 P1 + 5 P2 from 3 independent reviews.

Three independent reviews of v19.1.10 found scheduler semantics gaps (capability matching, best-of selection, model mapping) + Pi-Bolt deployment issues (unreadable secrets, missing MCP auth, install abort on optional binaries). v19.1.11 fixes all before v19.2.0 Compute Fabric.

### P1 fixes (scheduler + Pi-Bolt)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **Capability matching = ALL required** | `matched > 0` accepted (2/3 OK) → model without "vision" could be selected for "vision" task | `matched < len(requested)` → REJECT. ALL capabilities must match |
| **P1-2** | **FormatPlan CPU plan** | Valid CPU plan showed "No compatible GPU found" | Distinguishes CPU OK (`SelectedRuntime != ""`) from failure |
| **P1-3** | **scoreRuntime best-of** | First-match return (first compatible runtime selected regardless of score) | Best-of: iterates ALL compatible runtimes, returns highest score |
| **P1-4** | **scoreModel via SupportedRuntimes** | Hardcoded `modelMap` (runtime→modelID 1:1) | Iterates ALL models, checks `SupportedRuntimes` via `runtimeEngine()` mapping (beellama→llamacpp) |
| **P1-5** | **Empty accelerator default** | `accelerator=""` → no GPU path, no CPU path → empty plan | Defaults to `"cuda"` (sensible default for AI workloads) |
| **P1-6** | **Pi-Bolt secrets readable** | `~/.bashrc` sourced `/etc/scarlix/.env` (root:root 600 → user can't read → empty key) | Creates `~/.config/scarlix/agent.env` (600, user-owned) with LITELLM_MASTER_KEY + SCARLIHQ_TOKEN, sourced from .bashrc |
| **P1-7** | **MCP Bearer token** | `config.template.json` MCP had no auth header → 401 | Added `"headers": {"Authorization": "Bearer ${SCARLIHQ_TOKEN}"}` |
| **P1-8** | **Install doesn't abort on optional binaries** | Copy loop `crit()` on missing scarlix-gpu etc. → install aborts if Go build skipped | `case` statement: non-critical Go binaries → `warn + skip`, core binaries → `crit` |

### P2 fixes (documentation + CI)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P2-1** | scarlix-mode header | v19.0.3 | v19.1.11 |
| **P2-2** | models.yaml header | v19.0.0 | v19.1.11 |
| **P2-3** | Duplicate comment | `acceleratorCompatible` had duplicate doc comment | Removed |
| **P2-4** | gofmt CI enforcement | No gofmt check in CI | Added `gofmt -l .` check after `go vet` (exit 1 on unformatted files) |
| **P2-5** | Dockerfile header | v19.1.10 (was OK but comment chain) | v19.1.11 clean |

### New tests (4)
```
TestPlan_AllCapabilitiesMustMatch          — 2/3 capabilities → REJECT
TestFormatPlan_CPUPlanNotNoGPU             — CPU plan shows runtime, not "no GPU found"
TestPlan_BestRuntimeSelected               — running runtime preferred over non-running
TestPlan_EmptyAcceleratorDefaultsCuda      — empty accelerator → cuda default → GPU found
```

### Test results
```
go vet ./internal/scheduler/      → CLEAN
go test ./internal/scheduler/ -v   → 17 PASS / 0 FAIL (13 existing + 4 new)
go test ./... -count=1             → 153 PASS / 0 FAIL (10 packages, no regression)
scarlix-smoke-test.sh             → 13 passed, 0 failed, 0 warned
```

### v19.1.x Resource Foundation + Hardening COMPLETE
```
v19.1.0–v19.1.5   Resource Foundation (contract, registries, monitor, telemetry)
v19.1.6–v19.1.9   Resource View + Scheduler + Compatibility + Release freeze
v19.1.10          Scheduler Correctness (3 P1: CPU, capabilities, down runtime)
v19.1.11          Contract Semantics Hardening (8 P1 + 5 P2)  ← THIS RELEASE
```

**v19.2.0 Compute Fabric can now safely transition from dry-run → real allocation.**

---

## 🆕 What's New in v19.1.10 (vs v19.1.9)

**Scheduler Correctness Patch — 3 P1 + 3 P2 fixes from independent review.** 6 deliverables.

Independent review of v19.1.9 found 3 P1 logic bugs in the dry-run scheduler + 3 P2 documentation/CI gaps. v19.1.10 fixes all 6 before the transition to v19.2.x Compute Fabric.

### P1 fixes (scheduler correctness)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **CPU request never selects GPU** | `acceleratorCompatible("cpu", gpu)` returned `true` → CPU tasks could be assigned to GPUs via the GPU loop | `acceleratorCompatible("cpu", gpu)` returns `false`. CPU requests skip the GPU loop entirely, only matching CPU runtimes (beellama/ollama) via new `scoreRuntimeForCPU()` method |
| **P1-2** | **Model with missing capabilities is REJECTED** | `scoreModel()` gave `score=5` (lower but still a candidate) when capabilities didn't match → model without "vision" could be selected for a "vision" task | `scoreModel()` returns empty (rejection) when `matched==0`. Plan() hard-rejects candidates with no compatible model |
| **P1-3** | **Down/unhealthy runtime is HARD-rejected** | Health only affected latency score → a DOWN runtime (sglang) could be selected over a HEALTHY one (vllm) if sglang was first in the loop | `scoreRuntime()` + `scoreRuntimeForCPU()` hard-filter `down`/`unhealthy` runtimes BEFORE scoring. Only `healthy`/`unknown` runtimes are candidates |

### P2 fixes (documentation + CI)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P2-1** | scarlix-monitor usage text | Said "v19.1.4" + "--serve = STUB — coming in v19.1.5" | Updated to "v19.1.10" + "--serve = ScarliHQ Resource View, v19.1.6" (server is implemented since v19.1.6) |
| **P2-2** | Dockerfile header | `# ScarliHQ v19.0.5` (stale by 5+ minor releases) | `# ScarliHQ v19.1.10` |
| **P2-3** | CI builds only 2 of 7 Go binaries | CI verified only `scarlihq` + `scarlix-bridge-reader` (v18.7.8) — 5 new binaries (scarlix-gpu, scarlix-inventory, scarlix-contract, scarlix-monitor, scarlix-scheduler) were not CI-verified | CI now explicitly builds all 7 Go binaries with success verification |

### New scheduler tests (4)
```
TestPlan_CPURequestNeverSelectsGPU        — CPU accelerator → no GPU selected, CPU runtime used
TestPlan_RejectsModelWithMissingCapabilities — model without "vision" → rejected, not lowered score
TestPlan_RejectsWhenNoModelFound          — no models in registry → candidate rejected
TestPlan_RejectsDownRuntime               — sglang DOWN + vllm HEALTHY → vllm selected (hard filter)
```

### Test results
```
go vet ./internal/scheduler/      → CLEAN
go test ./internal/scheduler/ -v   → 13 PASS / 0 FAIL (9 existing + 4 new)
go test ./... -count=1            → 149 PASS / 0 FAIL (10 packages, no regression)
go build ./...                     → all packages OK
scarlix-smoke-test.sh             → 13 passed, 0 failed, 0 warned
```

### What this means for v19.2.x
The scheduler now correctly handles the 3 edge cases that would have caused incorrect allocations in the real Compute Fabric:
- CPU tasks will never be sent to GPUs
- Models without required capabilities will never be selected
- Down runtimes will never be selected over healthy ones

**v19.2.0 Compute Fabric can now safely transition from dry-run → real allocation.**

---

## 🆕 What's New in v19.1.9 (vs v19.1.8)

**Resource Foundation Release — freeze + full regression.** 2 deliverables.

v19.1.9 is the Resource Foundation release per master guide section 19. No new architecture — this version freezes the v19.1.x generation (contracts, registries, telemetry, monitor, dry-run scheduler, compatibility matrix) and runs full regression.

### v19.1.9 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **Release baseline document** (`docs/SCARLIX_RESOURCE_FOUNDATION_RELEASE.md`) | Frozen artifacts inventory: 10 Go packages (145 tests), 7 Go binaries, 8+ bash CLIs. Full regression results. What's frozen (must not change without version bump). Next generation roadmap (v19.2.x Compute Fabric). |
| 2 | **Full regression verification** | 145 Go tests PASS / 0 FAIL. 10 packages all OK. 7 binaries build. bash -n + shellcheck -S warning + YAML + systemd + smoke test all green. |

### Full regression results (v19.1.9)
```
Go packages (10):
  api, compat, contract, inventory, monitor, registry,
  scheduler, scarlix_mode, status, telemetry
  → 145 tests PASS / 0 FAIL / 0 SKIP

Go binaries (7):
  scarlihq, scarlix-bridge-reader, scarlix-gpu, scarlix-inventory,
  scarlix-contract, scarlix-monitor, scarlix-scheduler
  → all build OK

Bash validation:
  bash -n (all scripts)             → PASS
  shellcheck -S warning (CI scripts) → CLEAN
  YAML lint (26 compose + smg)      → 26/26 OK
  systemd-analyze verify (5 units)  → PASS
  VERSION consistency               → PASS (19.1.9)
  go.mod module path                → PASS
  image tags (5 via registry API)   → HTTP 200
  ollama-main active refs           → 0
  v12 doc headers                   → 0
  scarlix-smoke-test.sh             → 13 passed, 0 failed, 0 warned
```

### What is frozen (v19.1.x Resource Foundation)
- Resource Contract v1 schema (v19.1.0)
- Contract validation rules (v19.1.1)
- RuntimeRegistry + ModelRegistry APIs (v19.1.2/v19.1.3)
- ScarliMonitor Snapshot schema (v19.1.4)
- Telemetry Measurement fields (v19.1.5)
- HTTP API endpoints (v19.1.6)
- Scheduler scoring formula (v19.1.7)
- Compatibility rules (v19.1.8)

### v19.1.x Resource Foundation generation COMPLETE
```
v19.1.0  Resource Contract v1 (schema)
v19.1.1  Contract Validation (reject rules)
v19.1.2  Runtime Registry v1 (lifecycle + lookup)
v19.1.3  Model Registry v1 (CLI inspect/health)
v19.1.4  ScarliMonitor Foundation (read-only monitoring)
v19.1.5  Telemetry History (JSON Lines persistence)
v19.1.6  ScarliHQ Resource View (HTTP server)
v19.1.7  Scheduler Dry Run (deterministic scoring)
v19.1.8  GPU Compatibility Matrix (GPU×Runtime×Model)
v19.1.9  Resource Foundation Release (freeze + regression)  ← THIS RELEASE
```

**Next generation: v19.2.x Compute Fabric** (real scheduler, GPU selection, priority queues, leases, safe fallback, ScarliHQ compute control).

---

## 🆕 What's New in v19.1.8 (vs v19.1.7)

**GPU Compatibility Matrix — GPU × Runtime × Model checks.** 3 deliverables.

v19.1.8 implements the GPU Compatibility Matrix per master guide section 18. A new `compat` package evaluates every GPU+Runtime+Model combination and reports whether it's compatible — checking VRAM fit, format support, runtime support list, GPU vendor match, and GPU assignment.

### v19.1.8 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **compat package** (`scarlihq/internal/compat/matrix.go`, 190 lines) | `BuildMatrix()` evaluates every GPU×Runtime×Model triple. `Check()` returns compatible + reason. 5 compatibility rules: format support, runtime support list, VRAM fit, vendor match, GPU assignment. `Compatible()/Incompatible()/ForGPU()/ForRuntime()` query methods. |
| 2 | **11 compat tests** (`matrix_test.go`) | Compatible triple, wrong format, runtime not in support list, insufficient VRAM, GPU not assigned, CPU runtime with any GPU, empty matrix, full grid, compatible filter, ForGPU filter, count compatible. All PASS. |
| 3 | **Integration** | The scheduler (v19.1.7) can query the matrix to filter incompatible candidates before scoring. |

### Compatibility rules (5)
```
✓ Runtime supports model format      (sglang/vllm → safetensors/awq; beellama/ollama → gguf)
✓ Runtime in model's supported list   (model.SupportedRuntimes contains runtime.ID)
✓ GPU VRAM >= model estimated VRAM    (if estimated_vram > 0)
✓ GPU vendor matches runtime accel    (nvidia→cuda, amd→rocm, cpu→any)
✓ GPU assigned to runtime             (or runtime is CPU-only with empty GPUIDs)
```

### Test results
```
go vet ./internal/compat/     → CLEAN
go test ./internal/compat/ -v  → 11 PASS / 0 FAIL
go build ./...                 → 10 packages OK (no regression)
go test ./...                  → 10 packages all PASS
scarlix-smoke-test.sh          → 13 passed, 0 failed, 0 warned
```

### What's NOT in v19.1.8 (by design)
- No scheduler integration yet (v19.1.9 freeze, v19.2.0 real)
- No CLI command (the matrix is a Go package, queried by the scheduler)
- No automatic compatibility testing on GPU host

---

## 🆕 What's New in v19.1.7 (vs v19.1.6)

**Scheduler Dry Run — deterministic scoring, no allocation.** 4 deliverables.

v19.1.7 implements the dry-run scheduler per master guide section 17. The `scarlix-scheduler` binary (or future `scarlix compute plan`) takes a task type + computes a plan: which GPU, runtime, and model the scheduler WOULD select. No actual allocation — read-only planning.

### v19.1.7 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **Scheduler package** (`scarlihq/internal/scheduler/plan.go`, 330 lines) | `Scheduler` type + `Plan()` method. Deterministic scoring: `vram_fit + runtime_fit + model_fit + latency + health - utilization_penalty - temperature_penalty`. Returns `Plan` with selected GPU/runtime/model + score breakdown + rejections. |
| 2 | **9 scheduler tests** (`plan_test.go`) | No-GPUs, selects compatible GPU, rejects insufficient VRAM, rejects wrong accelerator, prefers low utilization, score breakdown non-empty, format human-readable, accelerator compatibility, rejects unhealthy GPU. All PASS. |
| 3 | **scarlix-scheduler binary** (`scarlihq/cmd/scarlix-scheduler/main.go`, 90 lines) | CLI: `plan --task <type> [--priority <p>] [--vram <mb>] [--runtime <r>] [--capabilities <c>] [--json]`. Human-readable output by default (master guide format), JSON with `--json`. |
| 4 | **install.sh integration** | Build block (native Go + Docker fallback) + binary copy list. |

### Scoring formula (deterministic, no AI)
```
score = vram_fit(+30) + runtime_fit(+20) + model_fit(+20) + latency(+20) + health(+10)
      - utilization_penalty(-10 if >80%) - temperature_penalty(-10 if >90°C)
```

### CLI usage
```bash
scarlix-scheduler plan --task coding                    # human-readable plan
scarlix-scheduler plan --task coding --json              # JSON output
scarlix-scheduler plan --task coding --vram 12000         # specify VRAM requirement
scarlix-scheduler plan --task chat --runtime sglang       # preferred runtime
scarlix-scheduler plan --task coding --capabilities coding,reasoning  # model caps
```

### Test results
```
go vet ./internal/scheduler/      → CLEAN
go test ./internal/scheduler/ -v   → 9 PASS / 0 FAIL
go build ./...                      → 9 packages OK (no regression)
go test ./...                       → 9 packages all PASS
scarlix-scheduler plan --task coding → "No compatible GPU found" (sandbox, no nvidia-smi)
scarlix-scheduler --json plan       → valid JSON
scarlix-smoke-test.sh               → 13 passed, 0 failed, 0 warned
```

### What's NOT in v19.1.7 (by design)
- No actual allocation (v19.2.0 real Compute Fabric)
- No resource leases (v19.2.7)
- No priority queue ordering (v19.2.5)
- GPU compatibility matrix not formalized yet (v19.1.8)

---

## 🆕 What's New in v19.1.6 (vs v19.1.5)

**ScarliHQ Resource View — HTTP server exposing system state.** 3 deliverables.

v19.1.6 implements the ScarliHQ Resource View per master guide section 16. The `scarlix-monitor --serve <port>` flag (which was a stub in v19.1.4) now starts a real HTTP server exposing GPUs, runtime status, models, health, and telemetry history. ScarliHQ remains the control plane — this is a read-only observation endpoint.

### v19.1.6 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **HTTP server in scarlix-monitor** | `--serve <port>` starts a localhost-only HTTP server. Endpoints: `GET /` (full Snapshot JSON), `GET /health` (liveness probe), `GET /telemetry?from=T&to=T` (telemetry history query). Read-only — no control operations. |
| 2 | **3 HTTP endpoints** | `GET /` returns full `monitor.Snapshot` JSON. `GET /health` returns `{"status":"ok","service":"scarlix-monitor","version":"..."}` (no auth — like LiteLLM `/health/liveliness`). `GET /telemetry` accepts `from`/`to` RFC3339 query params + returns `[]Measurement` JSON. |
| 3 | **Security: localhost-only** | Server binds to `127.0.0.1:<port>` — no LAN/WAN exposure per the security policy (inference + monitoring endpoints are localhost-only). |

### HTTP API
```
GET /                           # full system snapshot (GPUs, CPU, RAM, storage, runtimes, models, health)
GET /health                     # liveness probe (no auth)
GET /telemetry?from=T1&to=T2    # telemetry history (RFC3339 time range, optional)
```

### Test results
```
go vet ./cmd/scarlix-monitor/    → CLEAN
go build ./...                    → all packages OK (no regression)
scarlix-monitor --serve 18080    → HTTP server starts, 3 endpoints respond:
  GET /                          → valid Snapshot JSON
  GET /health                    → {"status":"ok","service":"scarlix-monitor","version":"..."}
  GET /telemetry                 → []Measurement JSON
scarlix-smoke-test.sh            → 13 passed, 0 failed, 0 warned
```

### What's NOT in v19.1.6 (by design)
- No scheduler (v19.1.7 dry-run, v19.2.x real)
- No resource allocation
- No auth on /health (liveness probe) — but / and /telemetry would need auth in production (not enforced yet — localhost-only binding is the current boundary)

---

## 🆕 What's New in v19.1.5 (vs v19.1.4)

**Telemetry History — lightweight measurement persistence.** 4 deliverables.

v19.1.5 implements telemetry persistence per master guide section 16. Measurements (timestamp, GPU util, VRAM, temp, runtime, model, latency, tokens/sec) are stored as JSON Lines — no SQLite dependency. The `scarlix-monitor` binary gains `--record` (persist a snapshot) and `--history` (query past measurements) modes.

### v19.1.5 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **Telemetry store** (`scarlihq/internal/telemetry/store.go`, 230 lines) | JSON Lines persistence. `Measurement` struct (10 fields, FROZEN in v19.1.5). `Append()`, `Query(from, to)`, `Count()`, `Prune(olderThan)`, `Path()`. Thread-safe append, sorted query results, malformed-line skip. |
| 2 | **10 telemetry tests** (`store_test.go`) | Append+count, auto-timestamp, empty query, time-range filter, sorted results, prune, parent-dir creation, malformed-line skip, env-var path, JSON round-trip. All PASS. |
| 3 | **scarlix-monitor `--record`/`--history`/`--prune`** | `--record`: take snapshot + persist measurements (one per GPU + runtime mapping). `--history [--from T --to T]`: query + print JSON. `--prune <duration>`: remove old entries (e.g. `24h`, `7d`). |
| 4 | **No SQLite dependency** — JSON Lines format (one JSON object per line). Append-only, simple time-range queries. Storage path: `/var/lib/scarlix/telemetry.jsonl` (override via `SCARLIX_TELEMETRY_FILE`). |

### Measurement fields (FROZEN in v19.1.5)
```
timestamp, gpu_index, gpu_util_pct, gpu_vram_used_mb, gpu_vram_total_mb,
gpu_temp_c, runtime, model, latency_ms, tokens_per_sec
```

### Test results
```
go vet ./internal/telemetry/         → CLEAN
go test ./internal/telemetry/ -v     → 10 PASS / 0 FAIL
go build ./...                        → all packages OK (no regression)
go test ./...                         → 8 packages all PASS
scarlix-monitor --record              → "recorded telemetry to /var/lib/scarlix/telemetry.jsonl"
scarlix-monitor --history             → []Measurement JSON array
scarlix-monitor --history --from T    → time-range filtered
scarlix-monitor --prune 24h           → "pruned N entries older than 24h"
scarlix-smoke-test.sh                → 13 passed, 0 failed, 0 warned
```

### v19.1.x Resource Foundation generation COMPLETE
v19.1.0–v19.1.5 delivers the full Resource Foundation per ScaRgeN master guide sections 10–16:
- v19.1.0: Resource Contract v1 (schema)
- v19.1.1: Contract Validation (reject rules)
- v19.1.2: Runtime Registry v1 (lifecycle + lookup)
- v19.1.3: Model Registry v1 (CLI inspect/health)
- v19.1.4: ScarliMonitor Foundation (read-only monitoring)
- v19.1.5: Telemetry History (persistence)

**Next generation: v19.2.x Compute Fabric** (real scheduler, deterministic scoring, GPU selection, leases).

---

## 🆕 What's New in v19.1.4 (vs v19.1.3)

**ScarliMonitor Foundation — read-only monitoring service.** 5 deliverables.

v19.1.4 implements ScarliMonitor per master guide section 14: a read-only monitoring service that exposes GPU, CPU, RAM, storage, runtime health, model health, and service health. No control operations — observation only.

### v19.1.4 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **Monitor package** (`scarlihq/internal/monitor/monitor.go`, 469 lines) | `Monitor` type + `Snapshot()` method composing inventory collectors + /proc + /sys reads (CPU, RAM, storage). Graceful degradation — never panics, never nil. |
| 2 | **8 monitor tests** (`monitor_test.go`, 197 lines) | Never-panics, valid timestamp, never-nil slices, valid JSON round-trip, CPU/RAM reads from /proc. All PASS. |
| 3 | **scarlix-monitor binary** (`scarlihq/cmd/scarlix-monitor/main.go`, 140 lines) | Standalone CLI: `--once` (default, print Snapshot JSON), `--serve <port>` (stub for v19.1.5), `--help`. |
| 4 | **Documentation** (`docs/SCARLIX_MONITOR.md`, 193 lines) | Purpose, architecture, schema, CLI usage, what's-NOT-in-v19.1.4, integration. |
| 5 | **install.sh integration** | Build block (native Go + Docker fallback) + binary copy list. |

### Snapshot schema
```json
{
  "timestamp": "2026-10-06T09:54:44Z",
  "version": "19.1.4",
  "mode": "ai",
  "gpus": [...],
  "cpu": { "cores": 8, "model_name": "...", "load_avg_1m": 0.5, "usage_pct": 12.3 },
  "ram": { "total_mb": 65536, "used_mb": 8192, "free_mb": 57344, "available_mb": 57344 },
  "storage": { "models_dir": "/models", "models_total_mb": 102400, "models_free_mb": 51200, "root_free_mb": 20480 },
  "runtimes": [...],
  "models": [...],
  "services": [],
  "health": [...]
}
```

### Test results
```
go vet ./internal/monitor/      → CLEAN
go test ./internal/monitor/ -v  → 8 PASS / 0 FAIL
go build ./...                   → 16 packages OK (no regression)
go test ./...                    → 7 test packages all PASS
scarlix-monitor                  → valid Snapshot JSON
scarlix-monitor --serve 8080    → "HTTP server mode coming in v19.1.5" (stub)
scarlix-smoke-test.sh           → 13 passed, 0 failed, 0 warned
```

### What's NOT in v19.1.4 (by design)
- No HTTP server (v19.1.5 — `--serve` is a stub)
- No telemetry persistence (v19.1.5)
- No alerting
- No control operations — read-only observation

---

## 🆕 What's New in v19.1.3 (vs v19.1.2)

**Model Registry v1 + CLI inspect/status/health.** 3 deliverables.

v19.1.3 formalizes the Model Registry (per master guide section 13) and adds CLI inspect/status/health subcommands for both runtimes and models. The `scarlix-inventory` binary now supports single-entry lookup by ID.

### v19.1.3 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **scarlix-inventory inspect/status/health** | New flags: `--runtime-inspect=<id>`, `--runtime-status=<id>`, `--model-inspect=<id>`, `--model-health=<id>`. Single-entry JSON lookup via the registry. Exit 1 + "not found" message on unknown ID. |
| 2 | **ModelRegistry formalized** | Already created in v19.1.2 (package cohesion), now fully wired into scarlix-inventory CLI: `LoadFromInventory()`, `Inspect(id)`, `Health(id)`, `PresentCount()`. |
| 3 | **No scheduler yet** — the registries + CLI are read-only. The scheduler (v19.2.x) will query these. |

### scarlix-inventory new operations
```bash
scarlix-inventory --runtime-inspect=sglang    # single Runtime JSON
scarlix-inventory --runtime-status=sglang     # single runtime Health JSON
scarlix-inventory --model-inspect=sglang      # single Model JSON
scarlix-inventory --model-health=sglang       # single model Health JSON
# exit 1 + "not found" on unknown ID
```

### Test results
```
go vet ./...                       → CLEAN
go test ./...                      → 6 packages all PASS (no regression)
go build ./...                     → all packages OK
scarlix-inventory --runtime-inspect=sglang   → valid Runtime JSON
scarlix-inventory --model-inspect=sglang      → valid Model JSON (with SCARLIX_REPO)
scarlix-inventory --runtime-inspect=bogus     → "not found", exit 1
scarlix-smoke-test.sh              → 13 passed, 0 failed, 0 warned
```

### What's NOT in v19.1.3 (by design)
- No scheduler (v19.2.x)
- No resource allocation
- No automatic model scanning beyond models.yaml parsing (that's the inventory collector's job)

---

## 🆕 What's New in v19.1.2 (vs v19.1.1)

**Runtime Registry v1 — formalized registry with lifecycle + lookup.** 4 deliverables.

v19.1.2 formalizes the Runtime Registry per master guide section 12. The registry wraps the v19.0.9 `inventory.CollectRuntimes()` collector with explicit lifecycle (Register/Unregister), lookup (Inspect/Status), and sorted listing. This is the data structure the future scheduler (v19.2.x) will query when matching Resource Contract requests against available runtimes.

### v19.1.2 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **RuntimeRegistry** (`scarlihq/internal/registry/runtime.go`, 145 lines) | Thread-safe registry with `LoadFromInventory()`, `Register()`, `Unregister()`, `List()`, `Inspect(id)`, `Status(id)`, `Count()`, `IDs()`. Sorted by ID. Never nil. |
| 2 | **ModelRegistry** (`scarlihq/internal/registry/model.go`, 165 lines) | Same pattern for models — `LoadFromInventory()`, `Register()`, `Unregister()`, `List()`, `Inspect(id)`, `Health(id)`, `PresentCount()`. (v19.1.3 deliverable included here for package cohesion.) |
| 3 | **20 registry tests** (`runtime_test.go` + `model_test.go`) | 10 RuntimeRegistry + 10 ModelRegistry tests — empty, register, unregister, inspect not-found, status/health, sorted list, load from inventory, IDs. All PASS. |
| 4 | **No scheduler yet** — the registries are read-only data structures. The scheduler (v19.2.x) will match Resource Contract requests against registry state. |

### RuntimeRegistry operations
```
LoadFromInventory()    — snapshot current system state (calls inventory.CollectRuntimes)
Register(runtime)      — manually add/update a runtime entry
Unregister(id)         — remove a runtime entry
List()                 — all runtimes sorted by ID (never nil)
Inspect(id)            — single runtime lookup (returns ok bool)
Status(id)             — health probe for a runtime (returns ok bool)
Count() / IDs()        — size + sorted ID list
```

### Test results
```
go vet ./internal/registry/      → CLEAN
go test ./internal/registry/ -v  → 20 PASS / 0 FAIL
go build ./...                    → all packages OK (no regression)
go test ./...                     → 6 packages all PASS
scarlix-smoke-test.sh             → 13 passed, 0 failed, 0 warned
```

### What's NOT in v19.1.2 (by design)
- No scheduler (v19.2.x)
- No resource allocation
- No CLI `runtime inspect/status` yet (the registries are Go-only in v19.1.2; CLI integration comes in v19.1.3)

---

## 🆕 What's New in v19.1.1 (vs v19.1.0)

**Contract Validation — schema enforcement + reject rules.** 3 deliverables.

v19.1.1 adds validation logic to the Resource Contract. Per master guide section 11: reject missing IDs, invalid resource types, negative resources, unsupported runtimes, invalid models, invalid security scopes. No silent correction of invalid requests.

### v19.1.1 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **Validator** (`scarlihq/internal/contract/validate.go`, 175 lines) | `Validate()` returns `ValidationErrors` collection (every problem reported). `ValidateStrict()` returns `error` for CLI pass/fail. Checks: missing ID/agent_id/version, invalid task type/priority/accelator, negative VRAM/CPU/RAM, unsupported runtime names, empty model capabilities, invalid security scopes. |
| 2 | **17 validation tests** (`scarlihq/internal/contract/validate_test.go`) | Valid contract passes, each reject rule tested individually (missing ID, missing agent_id, invalid task type, invalid priority, negative VRAM/CPU/RAM, invalid accelerator, unsupported runtime, empty capability, invalid filesystem/network/shell scope, multiple errors, strict mode). |
| 3 | **CLI integration** | `scarlix-contract validate <file>` now runs `ValidateStrict()` — reports specific field errors + exit 1 on invalid (was: parse-only in v19.1.0). |

### Validation rules (per master guide section 11)
```
✓ missing IDs            → reject (id, agent_id, version required)
✓ invalid resource types → reject (task.type, priority, accelerator enums)
✓ negative resources     → reject (vram_mb, cpu_cores, ram_mb >= 0)
✓ unsupported runtime   → reject (preferred must be known: sglang/vllm/beellama/ollama/litellm)
✓ invalid model          → reject (capabilities must be non-empty strings)
✓ invalid security scope → reject (filesystem/network/shell enums)
```

### Test results
```
go vet ./internal/contract/        → CLEAN
go test ./internal/contract/ -v   → 27 PASS / 0 FAIL (10 parse + 17 validate)
go build ./...                     → all packages OK (no regression)
scarlix-contract validate <valid>  → "valid", exit 0
scarlix-contract validate <invalid>→ "error: id: missing required field", exit 1
scarlix-smoke-test.sh              → 13 passed, 0 failed, 0 warned
```

### What's NOT in v19.1.1 (by design)
- No scheduler (v19.2.x)
- No resource allocation
- No runtime registry formalization (v19.1.2)

---

## 🆕 What's New in v19.1.0 (vs v19.0.10)

**Resource Contract v1 — canonical schema for compute resource requests.** 6 deliverables.

v19.1.0 begins the **Resource Foundation** generation (v19.1.x) per the ScaRgeN master guide. This release defines the Resource Contract — the canonical schema that agents submit to request compute resources (GPU, runtime, model, security scope). The contract is defined + parseable + serializable, but NOT yet enforced by a scheduler (that comes in v19.2.x Compute Fabric).

### v19.1.0 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **Contract package** (`scarlihq/internal/contract/types.go`, 171 lines) | `ResourceContract` struct + 5 nested specs (TaskSpec, ComputeSpec, RuntimeSpec, ModelSpec, SecuritySpec) + 6 const blocks for enum values (task types, priorities, accelerators, security scopes). JSON+YAML tags on every field. |
| 2 | **Parser** (`scarlihq/internal/contract/parse.go`, 202 lines) | `ParseYAML()`, `ParseJSON()`, `MarshalYAML()`, `MarshalJSON()`, `Example()`. Custom marshalers enforce `[]` not `null` for slices. |
| 3 | **scarlix-contract binary** (`scarlihq/cmd/scarlix-contract/main.go`, 164 lines) | Standalone CLI: `example` (print example contract), `validate <file>` (parse + validate), `parse <file>` (normalized JSON output). |
| 4 | **Example contract** (`scarlihq/internal/contract/example.yaml`) | Canonical example from master guide section 10 — used by tests + documentation. |
| 5 | **Documentation** (`docs/SCARLIX_RESOURCE_CONTRACT.md`, 236 lines) | Purpose, schema, field reference, enum values, stability contract, CLI usage, relationship to inventory package. |
| 6 | **Integration** | scarlix-contract added to install.sh build block + binary copy list. |

### Resource Contract schema (v1, FROZEN)
```yaml
version: v1
id: <uuid>
agent_id: agent.coder
task:
  type: coding          # coding|chat|research|embedding|indexing
  priority: interactive # realtime|interactive|normal|background|batch
compute:
  accelerator: cuda     # cuda|cpu|rocm
  vram_mb: 12000
  cpu_cores: 4
  ram_mb: 8192
runtime:
  preferred: [sglang, vllm]
model:
  capabilities: [coding, reasoning]
security:
  filesystem: workspace # workspace|none
  network: restricted   # restricted|none
  shell: sandbox         # sandbox|none
```

### Test results
```
go vet ./internal/contract/        → CLEAN
go test ./internal/contract/ -v   → 10 PASS / 0 FAIL
go build ./...                     → 13 packages OK (no regression)
scarlix-contract example          → valid YAML output
scarlix-contract validate <file>  → "valid", exit 0
scarlix-contract parse <file>     → valid normalized JSON
scarlix-smoke-test.sh             → 13 passed, 0 failed, 0 warned
```

### Relationship to inventory package
- **contract** = REQUEST (what the agent wants — GPU, runtime, model capabilities, security scope)
- **inventory** = STATE (what the system has — actual GPUs, running runtimes, available models)
- The **scheduler** (v19.2.x Compute Fabric) will match contract requests against inventory state.

### What's NOT in v19.1.0 (by design)
- No scheduler (v19.2.x)
- No resource allocation
- No contract enforcement — the contract is defined + parseable, but nothing rejects invalid contracts yet (that's v19.1.1 Contract Validation)

---

## 🆕 What's New in v19.0.10 (vs v19.0.9)

**OpenCode → Pi-Bolt migration (agent-layer replacement).** 6 deliverables.

v19.0.10 replaces the OpenCode coding-agent layer with [Pi-Bolt](https://github.com/opensec-git/Pi-Bolt) — a fork of Pi compiled AOT to native code. This is an **isolated agent-layer replacement**: no inference, security, dashboard, Docker, or host-bridge architecture was changed. OpenCode is quarantined (not deleted) for rollback.

### v19.0.10 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **Pi-Bolt config template** (`agents/pi-bolt/config.template.json`) | Preserves OpenCode functional intent: LiteLLM gateway, ScarliHQ MCP, plan/build agents. Security fixes: localhost-only endpoints (not 100.64.0.1), env-var API key (`${LITELLM_MASTER_KEY}`, not hardcoded) |
| 2 | **Pi-Bolt README** (`agents/pi-bolt/README.md`) | Install instructions, config, security model, workflow, rollback procedure |
| 3 | **OpenCode quarantined** (`agents/opencode/config.json` → `config.json.DEPRECATED`) | Renamed (not deleted) for rollback per migration guide section 39. DEPRECATED.md marker explains status + restore procedure |
| 4 | **install.sh integration** | Phase 5: Pi-Bolt installed as non-root user via `curl -fsSL https://pi-bolt.opensec.in/install.sh \| sh`. LITELLM_MASTER_KEY exported to user's `.bashrc` via safe grep+cut from `/etc/scarlix/.env` (never hardcoded) |
| 5 | **Migration documentation** (`docs/SCARLIX_AGENT_MIGRATION_OpenCode_to_PiBolt.md`) | Full migration record: changed files, security checks, regression matrix, test checklist (A–J per guide section 21), rollback procedure |
| 6 | **Architecture doc update** (`docs/SCARLIX_CURRENT_ARCHITECTURE.md`) | Layer 3: "OpenCode (coding manager)" → "Pi-Bolt (coding agent)" |

### Security model (unchanged boundaries)
| Check | Status |
|-------|--------|
| docker.sock exposure | ✅ NO — Pi-Bolt has no Docker access |
| root privilege | ✅ NO — installed + runs as non-root user |
| NVIDIA runtime | ✅ NO — Pi-Bolt is a coding agent, not inference |
| bridge-state write | ✅ NO — Pi-Bolt has no host-bridge access |
| .env secret leak | ✅ NO — API key is env var reference, not committed |
| inference endpoint exposed publicly | ✅ NO — localhost-only (127.0.0.1) |
| hard-coded production secrets | ✅ NO — `${LITELLM_MASTER_KEY}` env var pattern |

### Regression (completely unchanged)
Hermes, ScarliHQ, dashboard, LiteLLM, SGLang, vLLM, BeeLlama, Ollama, scarlix-mode, GPU config, Docker stack, models.yaml, host-bridge, bridge-reader, /etc/scarlix/.env generation — **all untouched**.

### Remaining gate (requires GPU host)
Tests A–J per migration guide section 21 (startup, model connectivity, repo inspection, file read, planning, controlled write, Git awareness, Git diff, MCP, subagent) — require real Pi-Bolt binary on target hardware. Not verifiable in repo-only sandbox. Once tests pass on hardware, OpenCode can be fully deleted (remove `agents/opencode/` directory).

### Rollback
```bash
git revert <migration-commit>
mv agents/opencode/config.json.DEPRECATED agents/opencode/config.json
rm -rf ~/.pi-bolt ~/.local/bin/pi-bolt
# Remove LITELLM_MASTER_KEY export line from .bashrc
```

---

## 🆕 What's New in v19.0.9 (vs v19.0.8)

**Runtime/Model Inventory — Runtime Registry + Model Registry.** 4 deliverables.

v19.0.9 implements the runtime and model registries per the ScaRgeN master guide section 9. New Go collectors query Docker for running runtimes + read models.yaml for model metadata, populating the `inventory.Runtime` and `inventory.Model` structs (10 fields each) defined in v19.0.7. A standalone `scarlix-inventory` binary provides the full system snapshot.

### v19.0.9 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **Runtime collector** (`scarlihq/internal/inventory/runtime.go`, 380 lines) | `CollectRuntimes()` queries Docker for 5 runtimes (sglang, vllm, beellama, ollama, litellm) — running status, image, port, GPU IDs. `CollectRuntimeHealth()` probes HTTP healthcheck endpoints (2s timeout per runtime). Never nil. |
| 2 | **Model collector** (`scarlihq/internal/inventory/model.go`, 438 lines) | `CollectModels()` reads models.yaml via gopkg.in/yaml.v3, parses 4 model sections (sglang AWQ, vllm AWQ, beellama GGUF, ollama), infers format/quantization/capabilities/supported_runtimes, checks file presence on disk. Never nil. |
| 3 | **scarlix-inventory binary** (`scarlihq/cmd/scarlix-inventory/main.go`, 216 lines) | Standalone CLI: `scarlix-inventory [--gpus\|--runtimes\|--models]` (default: full SystemStatus JSON). Pretty-printed output matching the frozen data contract. |
| 4 | **CLI integration** | `scarlix runtime list --json` + `scarlix model list --json` now delegate to `scarlix-inventory` when available (falls back to bash native). Human-readable output unchanged. |

### Runtime struct (10 fields, frozen v19.0.7)
```
id, version, enabled, running, healthy, protocol, port, gpu_ids, image, capabilities
```

### Model struct (10 fields, frozen v19.0.7)
```
id, path, format, quantization, parameters, context_length, estimated_vram_mb,
capabilities, supported_runtimes, present
```

### Test results
```
go vet ./internal/inventory/              → CLEAN
go test ./internal/inventory/ -v          → 39 PASS / 0 FAIL / 2 SKIP (no docker/nvidia-smi)
go build ./...                             → 12 packages OK (no regression)
scarlix-inventory (no docker)             → 5 runtimes (Running=false) + 4 models + 5 health (down)
scarlix-inventory --runtimes               → 5 runtime entries valid JSON
scarlix-inventory --models                 → 4 model entries valid JSON
scarlix --json runtime list                → delegates to scarlix-inventory ✓
scarlix --json model list                  → delegates to scarlix-inventory ✓
shellcheck -S warning scarlix CLI         → CLEAN
scarlix-smoke-test.sh                      → 13 passed, 0 failed, 0 warned
```

### Runtime registry entries (5)
| ID | Image | Port | GPU | Protocol |
|----|-------|------|-----|----------|
| sglang | lmsysorg/sglang:v0.4.9.post6-cu128-b200 | 30000 | gpu.nvidia.0 | openai-compatible |
| vllm | vllm/vllm-openai:v0.8.5 | 8089 | gpu.nvidia.1 | openai-compatible |
| beellama | ghcr.io/ggml-org/llama.cpp@sha256:… | 11438 | CPU | openai-compatible |
| ollama | ollama/ollama:0.5.4 | 11435 | CPU | ollama |
| litellm | ghcr.io/berriai/litellm:main-v1.21.7 | 4001 | — | openai-compatible |

### Model registry entries (4)
| ID | Format | Quantization | Path | Present |
|----|--------|---------------|------|---------|
| sglang | safetensors | awq | /models/Qwen3-14B-AWQ | checked |
| vllm | safetensors | awq | /models/Qwen3-14B-AWQ | checked |
| beellama | gguf | q4_k_m | /models/Qwen3-14B-Q4_K_M.gguf | checked |
| ollama | gguf | — | (ollama pull) | false |

### What's NOT in v19.0.9 (by design)
- No automatic scheduling (v19.2.x)
- No resource allocation
- No write/control operations
- Still no automatic scheduler — the registries are read-only

---

## 🆕 What's New in v19.0.8 (vs v19.0.7)

**GPU Telemetry — normalized GPU state collection.** 3 deliverables.

v19.0.8 implements GPU telemetry per the ScaRgeN master guide section 8. A new Go binary (`scarlix-gpu`) collects normalized GPU state from nvidia-smi and populates the `inventory.GPU` struct (14 fields) defined in v19.0.7. The `scarlix gpu status --json` command now delegates to this binary for data-contract-compliant output.

### v19.0.8 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **GPU collector** (`scarlihq/internal/inventory/gpu.go`, 286 lines) | 3 exported functions: `CollectGPUs()` (queries nvidia-smi, returns `[]GPU` — never nil, empty `[]GPU{}` if no nvidia-smi), `CollectGPUHealth()` (marks healthy/unhealthy based on VRAM/utilization), `nvidiaSmiQuery()` (reusable helper with 5s timeout). Handles `[N/A]` power draw, parse errors, missing nvidia-smi. |
| 2 | **scarlix-gpu binary** (`scarlihq/cmd/scarlix-gpu/main.go`, 68 lines) | Standalone CLI: `scarlix-gpu [--health]` → JSON output matching the frozen data contract. Exit 0 on success, 1 on encode failure. |
| 3 | **CLI integration** | `scarlix gpu status --json` + `scarlix gpu list --json` now delegate to `scarlix-gpu` binary when available (falls back to nvidia-smi direct if not). Human-readable output unchanged. |

### GPU struct fields (normalized, frozen in v19.0.7)
```
id, index, vendor, name, vram_total_mb, vram_used_mb, vram_free_mb,
utilization_percent, temperature_c, power_w, driver, cuda, compute_cap, healthy
```

### Test results
```
go vet ./internal/inventory/          → CLEAN
go test ./internal/inventory/ -v      → 8 PASS + 1 SKIP (no-nvidia-smi sandbox)
go build ./...                         → 11 packages OK (no regression)
scarlix-gpu (no nvidia-smi)            → [] (empty array, not null)
scarlix-gpu --health (no nvidia-smi)  → {"gpus":[],"health":[]}
scarlix --json gpu status              → delegates to scarlix-gpu ✓
shellcheck -S warning scarlix CLI     → CLEAN
scarlix-smoke-test.sh                  → 13 passed, 0 failed, 0 warned
```

### What's NOT in v19.0.8 (by design)
- No automatic GPU scheduling (v19.2.x)
- No resource allocation
- No write/control operations
- CUDA field left empty (host-global property — will be populated by a future `CollectSystemInfo()`)
- The existing AI path (scarlix-mode ai → SGLang/vLLM/BeeLlama/Ollama) is completely unchanged

---

## 🆕 What's New in v19.0.7 (vs v19.0.6)

**Observability Preparation — read-only discovery CLI + normalized data structures.** 4 deliverables.

v19.0.7 adds the first read-only observability layer per the ScaRgeN master guide section 7. No automatic resource allocation yet — only discovery commands and the data structures that future versions (v19.0.8 GPU telemetry, v19.1.x resource foundation) will build on.

### v19.0.7 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **`scarlix` unified CLI** (`files/usr/local/bin/scarlix`, 603 lines) | 5 read-only subcommands: `system status`, `gpu list`, `gpu status`, `runtime list`, `model list`. Global `--json` flag for normalized JSON output. Graceful degradation (WARNs if nvidia-smi/docker/yq unavailable, never crashes). Does NOT replace scarlix-mode/doctor/wizard — extends the CLI surface. |
| 2 | **Normalized Go data structures** (`scarlihq/internal/inventory/`) | 6 structs (GPU, Runtime, Model, Service, Health, SystemStatus) with 54 exported fields, all JSON-tagged. 7 Go tests (all PASS). `go vet` clean, `go build ./...` all 8 packages OK, no regression to existing api/scarlix_mode/status packages. |
| 3 | **Data contracts document** (`docs/SCARLIX_DATA_CONTRACTS.md`, 145 lines) | Canonical JSON schema — single source of truth for CLI `--json` output, ScarliHQ serialization, and future compute-fabric consumption. Field names FROZEN in v19.0.7 (additive changes only without v2 migration). |
| 4 | **Integration** | `scarlix` + `scarlix-smoke-test.sh` added to install.sh binary copy list + CI shellcheck checks. |

### scarlix CLI subcommands (all read-only)
```
scarlix system status    # version, mode, uptime, docker count, health probes
scarlix gpu list         # index, name, VRAM total, compute cap
scarlix gpu status       # VRAM used/free, util, temp, power, driver, CUDA
scarlix runtime list     # sglang/vllm/beellama/ollama/litellm status + port + image
scarlix model list       # configured models (models.yaml) + files present (/models/)
scarlix --json <cmd>     # normalized JSON output (matches data contracts)
```

### Normalized data structures (Go package inventory)
| Struct | Fields | Purpose |
|--------|--------|---------|
| `GPU` | 14 | Normalized GPU state (id, index, vendor, name, VRAM, util, temp, power, driver, CUDA, compute_cap, healthy) |
| `Runtime` | 10 | Inference runtime state (id, version, enabled, running, healthy, protocol, port, gpu_ids, image, capabilities) |
| `Model` | 10 | Model metadata (id, path, format, quantization, parameters, context_length, vram, capabilities, runtimes, present) |
| `Service` | 7 | Service state (id, name, type, status, port, container_id, uptime) |
| `Health` | 4 | Component health (component, state, message, checked_at) |
| `SystemStatus` | 9 | Composite snapshot (version, mode, uptime, timestamp, gpus, runtimes, models, services, health) |

### What's NOT in v19.0.7 (by design)
- No automatic resource allocation
- No GPU scheduling
- No agent kernel
- No write/control operations
- The existing AI path (scarlix-mode ai → SGLang/vLLM/BeeLlama/Ollama) is completely unchanged

---

## 🆕 What's New in v19.0.6 (vs v19.0.5)

**Baseline Freeze — ScaRgeN preparation.** Repository inventory + architecture maps + regression smoke test.

v19.0.6 freezes the v19.0.5 baseline before the ScaRgeN evolution (v19.0.7→v19.8.9 per the master coding guide). No runtime code changed — this release produces the documentation and tooling that all future generations will build on.

### v19.0.6 deliverables

| # | Deliverable | Description |
|---|-------------|-------------|
| 1 | **7 architecture map documents** (`docs/SCARLIX_*.md`) | Current architecture, component map, runtime map, GPU map, security map, API map, release baseline — all code-truth verified |
| 2 | **Regression smoke test** (`scarlix-smoke-test.sh`) | 10-check automated validation: bash -n, shellcheck -S warning, YAML lint, systemd verify, VERSION consistency, go.mod path, 5 image-tag registry checks (Docker Hub + ghcr API), ollama-main audit, v12 doc-drift audit |
| 3 | **Doc-drift fixes** | `docs/ARCHITECTURE.md` failover diagram corrected (BeeLlama tier-3, Ollama tier-4 — code-truth); SMG port `:4002` → `:4000` (matches `ai/smg/docker-compose.yml`) |
| 4 | **Version bump** | VERSION + install.sh + Dockerfile + AGENTS.md → v19.0.6 |

### Smoke test checks (10)
```
01  bash -n on all shell scripts                    → PASS
02  shellcheck -S warning on 10 CI scripts          → PASS
03  YAML safe_load on 25 compose + smg config       → PASS
04  systemd-analyze verify on 5 units               → PASS
05  VERSION consistency (VERSION↔install↔Dockerfile↔README) → PASS
06  go.mod module path (github.com/MoZoHuJa/OS/scarlihq)    → PASS
07  5 image tags via registry API (SGLang/Whisper/Buzz/LiteLLM/openlit) → PASS (HTTP 200)
08  No ollama-main in active smg config             → PASS
09  No v12 doc headers in active docs/              → PASS
10  Summary + exit code                             → exit 0
```

### What is locked in v19.0.6 (must NOT change in v19.0.x)
- All image tags (SGLang `lmsysorg/sglang:v0.4.9.post6-cu128-b200`, Whisper `sha-307e23f-cuda`, Buzz `ghcr.io/block/buzz:latest`, LiteLLM `main-v1.21.7`, openlit `1.5.0`)
- Go module path (`github.com/MoZoHuJa/OS/scarlihq`)
- 4-tier inference failover order (SGLang → vLLM → BeeLlama → Ollama)
- Secure Host-Bridge architecture (ScarliHQ nonroot UID 65532, no docker.sock)
- All existing CLI behavior (scarlix-mode, scarlix-doctor, scarlix-wizard, scarlix-host-bridge)
- Install path (git clone + checkout + bash install.sh)

Full baseline: `docs/SCARLIX_RELEASE_BASELINE.md`

### ScaRgeN roadmap (v19.0.7 → v19.8.9)
```
v19.0.6  Baseline Freeze (THIS RELEASE)
v19.0.7  Observability Preparation (read-only discovery CLI)
v19.0.8  GPU Telemetry (normalized GPU state)
v19.0.9  Runtime/Model Inventory (registries)
v19.1.x  Resource Foundation (contracts, registries, monitor, dry-run scheduler)
v19.2.x  Compute Fabric (real scheduler, scoring, GPU selection, leases)
v19.3.x  Agent Kernel (Rust, capabilities, permissions, sessions, sandbox)
v19.4.x  Agent Fabric (coding/research agents, workspaces, approvals)
v19.5.x  Desk/Hive (team workflows, delegation, shared context)
v19.6.x  Universal AI Runtime (normalized adapters, model router, failover)
v19.7.x  Memory + Cross Device + Agent IR (memory fabric, device pairing, IR)
v19.8.x  ScaRgeN Integration (orchestrator, ScarliCenter, ScarliMonitor, audit)
```

---

## 🆕 What's New in v19.0.5 (vs v19.0.4)

**Housekeeping — header version drift cleanup.** 3 fixes.

Final audit of v19.0.4 found 2 P2 hygiene items: stale version headers in `scarlihq/Dockerfile` (said v18.5.2) and `ai/sglang/docker-compose.yml` (said v18.8.3) — the runtime content was already correct, only the top comment had drifted. v19.0.5 bumps both headers to current and aligns all version refs.

### v19.0.5 fixes (3)

| # | Severity | Fix | Was | Now |
|---|----------|-----|-----|-----|
| 1 | **P2** | **`scarlihq/Dockerfile` header** | `# ScarliHQ v18.5.2 — Multi-stage Dockerfile` (stale by 10 minor releases — ARG SCARLIX_VERSION tracked correctly, but the file's top comment had drifted) | `# ScarliHQ v19.0.5 — Multi-stage Dockerfile` (historical architecture comments retained below) |
| 2 | **P2** | **`ai/sglang/docker-compose.yml` header** | `# SCARLIX OS v18.8.3 — SGLang (Tier-1, GPU 0)` (stale by 6 minor releases — image tag was already v0.4.9.post6-cu128-b200 from v19.0.3) | `# SCARLIX OS v19.0.5 — SGLang Tier-1 GPU inference (Blackwell sm_120, Qwen3)` (historical P-fix comments retained for audit trail) |
| 3 | **P2** | **`VERSION` + `install.sh` + `Dockerfile` ARG + `AGENTS.md` → v19.0.5** | v19.0.4 | v19.0.5 |

### No runtime change
No compose image tags, no script logic, no config values were modified. This release is purely header/comment version alignment. All v19.0.4 runtime fixes (SGLang, Whisper, Buzz, generate-env.sh, multilib, go.mod, dynamic banners) remain **untouched and verified**.

### Pre-release gate (unchanged from v19.0.4)
The remaining gate is NOT another code review — it is a real hardware lifecycle test on the target RTX 5060 Ti + RTX 4060 Ti box:
```
EndeavourOS → install.sh → NVIDIA → Docker GPU → docker pull SGLang
→ SGLang startup → Qwen3-14B-AWQ load → scarlix-mode ai → healthcheck
→ inference request → fallback chain → reboot → doctor
```

---

## 🆕 What's New in v19.0.4 (vs v19.0.3)

**Final docs cleanup — release-candidate for hardware lifecycle test.** 3 fixes.

Independent audit of v19.0.3 confirmed all P0/P1 runtime fixes are in place (SGLang, Whisper, Buzz, generate-env.sh, multilib, go.mod, Dockerfile, dynamic banners). Only 2 documentation legacy items remained: a stale `v12` header on `docs/NETWORK.md` and deprecated image refs in the body of `docs/COMPLETE_INSTALL_GUIDE.md`. v19.0.4 closes both.

### v19.0.4 fixes (3)

| # | Severity | Fix | Was | Now |
|---|----------|-----|-----|-----|
| 1 | **P2** | **`docs/NETWORK.md` header v12 → v19** | Header said "SCARLIX OS v12 — Network Topology" (content was still valid — ports, VPN, domains all match v19 runtime) | Header → v19 + note that topology/ports are unchanged (ScarliHQ :8090, SGLang :30000, Ollama :11435, LiteLLM :4001) |
| 2 | **P2** | **`docs/COMPLETE_INSTALL_GUIDE.md` moved to `docs/archive/`** | 2364-line deprecated v12 Ubuntu-ISO guide still in active `docs/` root — body contained stale image refs (`ghcr.io/sgl-project/sglang:v0.5.5-cu124`, `fedirz/faster-whisper-server:0.10.0`, `ghcr.io/block/buzz-relay:latest`) that could confuse a grep audit | Moved to `docs/archive/COMPLETE_INSTALL_GUIDE_v12_UBUNTU_DEPRECATED.md` — cleanly separated from active docs, `docs/archive/` already exists for historical manifests (V15, V16.1) |
| 3 | **P2** | **`VERSION` + `install.sh` + `Dockerfile` + `AGENTS.md` → v19.0.4** | v19.0.3 | v19.0.4 |

### Audit confirmation (v19.0.3 fixes verified by independent reviewer)

| Area | Status |
|------|--------|
| SGLang image exists (lmsysorg/sglang:v0.4.9.post6-cu128-b200) | 🟢 verified (manifest sha256, linux/amd64, CUDA 12.8.1, Blackwell build) |
| Whisper image exists (sha-307e23f-cuda) | 🟢 verified (amd64 + arm64) |
| Buzz image (ghcr.io/block/buzz:latest) | 🟢 verified |
| multilib ordering, Telegram env, cp -a dotfiles | 🟢 confirmed |
| Go module path (github.com/MoZoHuJa/OS/scarlihq) | 🟢 confirmed |
| Dockerfile SCARLIX_VERSION=19.0.3 | 🟢 confirmed (now 19.0.4) |
| HARDWARE.md + TROUBLESHOOTING.md v12 → v19 | 🟢 confirmed |
| Bash syntax (all scripts) | 🟢 PASS |
| Compose YAML (25 files) | 🟢 25/25 PASS |
| ollama-main runtime refs | 🟢 0 in active config (historical comment refs in archive only) |

### Remaining pre-release checks (require GPU host — NOT code fixes)
- `docker pull lmsysorg/sglang:v0.4.9.post6-cu128-b200` on RTX 5060 Ti + RTX 4060 Ti — confirm SGLang starts + `--disable-flashinfer` accepted
- Full lifecycle: EndeavourOS → install.sh → NVIDIA → Docker GPU → SGLang startup → Qwen3-14B-AWQ load → scarlix-mode ai → healthcheck → inference → fallback chain → reboot → doctor

**Verdict:** v19.0.4 is a release-candidate. No more code fixes needed — the remaining gate is a real hardware lifecycle test on the target RTX 5060 Ti + 4060 Ti box.

---

## 🆕 What's New in v19.0.3 (vs v19.0.2)

**Release-gate fixes: SGLang P0 blocker + image registry audit + version-drift cleanup.** 9 fixes.

A stricter independent audit of v19.0.2 found that the **SGLang Tier-1 image was a real P0 blocker** — the tag `ghcr.io/sgl-project/sglang:v0.4.6.post1-cu128` does NOT exist on any registry (ghcr returns 403/DENIED, Docker Hub `lmsysorg/sglang` returns 404). The v19.0.1 comment already admitted it "could NOT be verified" but it stayed in release. v19.0.3 fixes this + two more dead images + all version-drift.

### v19.0.3 fixes (9)

| # | Severity | Fix | Was | Now |
|---|----------|-----|-----|-----|
| 1 | **P0** | **SGLang Tier-1 image** (BLOCKER — scarlix-mode ai pulls this) | `ghcr.io/sgl-project/sglang:v0.4.6.post1-cu128` (ghcr 403/DENIED; tag 404 on Docker Hub `lmsysorg/sglang` — cu128 variant only exists from v0.4.8+ as `-b200`/`-gb200`) | `lmsysorg/sglang:v0.4.9.post6-cu128-b200` (verified HTTP 200 + manifest via registry API; latest stable 0.4.9 cu128-b200 = consumer Blackwell sm_120; supports Qwen3) |
| 2 | **P0** | **Whisper image tag** | `fedirz/faster-whisper-server:0.10.0` (tag does NOT exist — 0.10.x tags = [] on Docker Hub) | `fedirz/faster-whisper-server:sha-307e23f-cuda` (verified exists; note: upstream moved to "Speaches" — migrate in future) |
| 3 | **P1** | **Buzz image registry** | `ghcr.io/block/buzz-relay:latest` (ghcr DENIED — repo doesn't exist) | `ghcr.io/block/buzz:latest` (verified: latest, main, sha-* tags exist; profile-gated `[buzz]`) |
| 4 | **P2** | **All script banners now dynamic** (version-drift fix) | scarlix-doctor / scarlix-mode status+VRAM / download-models / model-manager banners hardcoded `v19.0.0` (drifted every release) | Read `/etc/scarlix/VERSION` dynamically → `v${SCARLIX_VER}` (never drifts again) |
| 5 | **P2** | **Dockerfile `SCARLIX_VERSION`** | `ARG SCARLIX_VERSION=19.0.0` (manual `docker build` without `--build-arg` produced binary reporting v19.0.0) | `ARG SCARLIX_VERSION=19.0.3` (matches VERSION file) |
| 6 | **P2** | **Go module path** | `module github.com/MoZoHuJa/scarlix-os-v12/scarlihq` (stale `scarlix-os-v12` path; actual repo is `MoZoHuJa/OS`) | `module github.com/MoZoHuJa/OS/scarlihq` (all `.go` imports updated) |
| 7 | **P2** | **`docs/HARDWARE.md` + `docs/TROUBLESHOOTING.md` v12 headers** | Both said "v12"; TROUBLESHOOTING had Ubuntu `apt purge`/`apt install` commands (wrong OS — EndeavourOS uses `pacman`) | Headers → v19; TROUBLESHOOTING NVIDIA driver reinstall rewritten with `pacman -S nvidia-open` + `mkinitcpio -P` |
| 8 | **P2** | **`smg/config.yaml` comment grep false-positive** | Historical comment contained literal `ollama-main` → `grep ollama-main` false-positived on the config (review P1-1/P2-5) | Comment reworded to not contain the dead hostname literally; plain grep now returns 0 matches |
| 9 | **P2** | **`VERSION` + `install.sh` + `AGENTS.md` → v19.0.3** | v19.0.2 | v19.0.3 (AGENTS.md SGLang version also updated to v0.4.9.post6-cu128-b200) |

### Image-registry verification (v19.0.3)
```
lmsysorg/sglang:v0.4.9.post6-cu128-b200     → HTTP 200 + manifest (Docker Hub)
fedirz/faster-whisper-server:sha-307e23f-cuda → exists (Docker Hub tags list)
ghcr.io/block/buzz:latest                   → tags: latest, main, sha-* (ghcr API)
```

### Remaining pre-release checks (NOT fixed in v19.0.3 — require GPU host)
- `docker pull lmsysorg/sglang:v0.4.9.post6-cu128-b200` on a Blackwell GPU host — confirm SGLang starts + `--disable-flashinfer` accepted
- `docker pull fedirz/faster-whisper-server:sha-307e23f-cuda` — confirm Whisper starts
- Full clean-install lifecycle test (EndeavourOS → install.sh → scarlix-mode ai → all fallbacks → reboot → doctor)

---

## 🆕 What's New in v19.0.2 (vs v19.0.1)

**Verified fixes from an independent code review + stale-docs cleanup.** 6 fixes.

An independent review of the v19.0.0 ZIP surfaced ~23 claimed bugs. On verification against the live repo (post-v19.0.1), **only 3 were real** (~74% false positives — the review was run on a stale snapshot and didn't check runtime facts like HuggingFace/Docker Hub APIs). v19.0.2 fixes the 3 real bugs + rewrites stale v12-era docs.

### v19.0.2 fixes (6)

| # | Severity | Fix | Was | Now |
|---|----------|-----|-----|-----|
| 1 | **P1** | **`scarlix-wizard` Telegram token silently lost via `sudo env_reset`** | `sudo /etc/systemd/system/generate-env.sh` — sudo's default `env_reset` strips `TELEGRAM_BOT_TOKEN` / `TELEGRAM_ZMOR_CHAT_ID` (not in `env_keep`) → generate-env.sh saw them EMPTY → token the user typed into the wizard was silently discarded | `sudo --preserve-env=TELEGRAM_BOT_TOKEN,TELEGRAM_ZMOR_CHAT_ID` (scoped, not bare `-E` which would leak the whole env) |
| 2 | **P0** | **`smg/config.yaml` dead upstream hostnames** | Referenced `ollama-main` (no such service — only `ollama-agent` exists) and `llamacpp` (renamed to `beellama` in v18.5) → 2 of 4 fallback targets never resolved on `scarlix-net` | Removed dead `ollama-main`; `llamacpp-cpu` → `beellama-cpu` (`http://beellama:8080`); `fallback_chain` now `[sglang-main, ollama-agent, beellama-cpu]` (matches the actual service names, consistent with the v18.8 LiteLLM config fix) |
| 3 | **P0** | **`docs/ARCHITECTURE.md` rewritten (was v12/Ubuntu)** | Said "v12 / Ubuntu 24.04 LTS / Ollama GPU 1 qwen3.6:14b / llama.cpp" — completely stale from the abandoned Ubuntu-ISO era | Rewritten for v19 EndeavourOS/Arch: real inference tiers (SGLang AWQ + vLLM TP=1 + BeeLlama + Ollama CPU), Secure Host-Bridge architecture, 4-tier + 3-tier failover diagrams, GPU arbitration table matching actual `scarlix-mode` modes |
| 4 | **P2** | **`docs/COMPLETE_INSTALL_GUIDE.md` deprecation banner** | 2364-line guide describing the obsolete v12 Ubuntu-ISO workflow (Rufus, Ubuntu 24.04 Server ISO, apt steps) — misleading for v19 users | Prominent DEPRECATED banner at top pointing to README.md "🚀 Install (NO ISO)"; header metadata marked zastarané. Full body retained as historical reference (rewriting 2364 lines of dead Ubuntu steps has no value) |
| 5 | **P2** | **`AGENTS.md` inference-stack table stale image versions** | Table said SGLang `v0.4.4-cu128`, vLLM `v0.8.0`, Ollama `0.5.4`, "llama.cpp official" | Updated to SGLang `v0.4.6.post1-cu128` (--disable-flashinfer), vLLM `v0.8.5`, BeeLlama (the llama.cpp tier), real service names (`sglang`/`vllm`/`beellama`/`ollama-agent`); added note that `ollama-main`/`llamacpp` were removed as dead references |
| 6 | **P2** | **`VERSION` + `install.sh` header → v19.0.2** | v19.0.1 | v19.0.2 |

### Review-claims verified FALSE (not bugs — left unchanged)

| Claim | Verdict | Evidence |
|-------|---------|----------|
| `/etc/scarlix/VERSION` never created | **FALSE** | `install.sh:679` creates it (v19.0.0 P1-7) |
| host-bridge timer `flock -n` silent skip | **FALSE** | Intentional oneshot+flock design (v19.0.0 P1-13 documented) |
| HF model IDs `Qwen/Qwen3-14B-AWQ` / `-GGUF` don't exist | **FALSE** | HuggingFace API confirms both repos + files exist |
| download-models.sh Ollama pull race | **FALSE** | `download-models.sh:298-312` waits for Ollama API (max 60s) |
| Video Dockerfile Wan2GP clone needs auth | **FALSE** | Public repo, commit pinned (`f3f204e50f6e`) |
| bridge-reader UID 65532 may not exist | **FALSE** | `scarlihq/Dockerfile:46,57` creates nonroot UID 65532 (intentional security design) |
| scarlix-doctor checks `ollama-main` | **FALSE** | `scarlix-doctor:336` checks `ollama-agent` (correct) |
| jellyfin 12.1 doesn't exist | **FALSE** | Docker Hub API confirms tag `12.1` exists |

### Verification (v19.0.2)
```
bash -n  install.sh scarlix-wizard scarlix-mode generate-env.sh ...  → PASS
python3 yaml.safe_load ai/smg/config.yaml                            → OK
git grep -n "ollama-main\|http://llamacpp:" ai/ docs/ AGENTS.md       → 0 matches (dead refs removed)
```

---

## 🆕 What's New in v19.0.1 (vs v19.0.0)

**Sandbox-verified bug fixes + image hygiene.** 11 fixes from two independent sandbox reviews of v19.0.0.

v19.0.0 passed `bash -n` and YAML lint, but live sandbox runs surfaced three real `generate-env.sh` data-corruption bugs, a multilib DB-sync ordering issue, stale image tags, and documentation drift. v19.0.1 closes all of them with the fixes verified in-sandbox.

### v19.0.1 fixes (11)

| # | Fix | Was (v19.0.0) | Now (v19.0.1) |
|---|-----|---------------|----------------|
| **P1** | **`generate-env.sh` duplicate detection ignores comments** | `cut -d= -f1` counted comments/blank lines → two identical `# note` lines → `ERROR` exit rc=1 → `scarlix-doctor` (calls this via `fix_regenerate_env`) infinite loop | `grep -E '^[A-Z_][A-Z0-9_]*='` before `cut`; duplicates auto-resolved keeping LAST occurrence (no hard-abort) |
| **P1** | **Telegram token update preserves `=` in value** | `awk FS=OFS="=" $2=v` left field-3+ in place → token `abc=def=` became `NEW123=def=` | `index($0,k"=")==1` line-match + `ENVIRON[]` for value (also safe vs backslashes) |
| **P1** | **Orphan-line / truncated-secret detection** | Old base64 multiline writes left orphan continuation lines; the orphan was dropped but the half `KEY=` stayed as a silently-valid (wrong) secret | WARN on orphan-after-assignment; `JWT_SECRET` auto-regenerated (rotate-safe); `STORAGE_ENCRYPTION_KEY` flagged for manual review (rotating breaks existing data) |
| **P1** | **`[multilib]` enabled BEFORE first `pacman -Syu`** | Multilib enabled in Phase 1 AFTER the first `-Syu` → multilib DB never synced → `lib32-*` installs could fail on clean EndeavourOS (comment falsely claimed "db synced by initial -Syu") | New `ensure_multilib()` called in pre-flight before any `-Syu`; single full `-Syu` (Phase 1 skips if pre-flight already synced) |
| **P1** | **`openlit` image: wrong registry** | `openlit/openlit:latest` (Docker Hub) — no official repo exists there → `docker pull` would fail on first start | `ghcr.io/openlit/openlit:1.5.0` (verified via ghcr API; latest stable) |
| **P1** | **LiteLLM image bumped + false comment fixed** | `main-v1.16.19` with a comment falsely claiming "latest stable" (1.17–1.23 already existed) | `main-v1.21.7` (conservative bump, config schema unchanged); pre-release healthcheck note added |
| **P1** | **SGLang image pre-release verification note** | `v0.4.6.post1-cu128` tag could not be verified (ghcr anonymous token returns 403); reviewer unsure about `cu128` variant | Tag left unchanged (changing CUDA suffix blindly risks GPU breakage); prominent pre-release `docker pull` + `--disable-flashinfer --help` check added |
| **P2** | **`scarlix-wizard` `cp -r` glob copy** | `cp -r /opt/scarlix-src/* /opt/scarlix/ \|\| true` — (1) `*` skips dotfiles, (2) `\|\| true` masks copy failures | `cp -a /opt/scarlix-src/. /opt/scarlix/` (copies dotfiles, preserves attrs) + fail-closed |
| **P2** | **`scarlix-mode` `.env` header version** | Hardcoded `v18.9.0` in the generated `.env` header — drifted from runtime version | Reads `/etc/scarlix/VERSION` (falls back to `unknown`) |
| **P2** | **CI shellcheck severity `-S error` → `-S warning`** | Only shellcheck *errors* blocked CI; warnings (unused vars, redirect issues) silently passed | `-S warning` + all 6 resulting warnings fixed (4 unused loop vars, 1 `test -x` SC2065 real bug in model-manager, 1 false-positive SC2024 documented) |
| **P2** | **`install.sh` header + VERSION** | Header comment said `v18.9.8`, `VERSION="19.0.0"` | Header `v19.0.1`, `VERSION="19.0.1"` (VERSION file also bumped) |

### Sandbox verification (v19.0.1)
```
bash -n  install.sh scarlix-wizard scarlix-mode scarlix-doctor generate-env.sh ...  → PASS
shellcheck -S warning <all CI scripts>                                              → CLEAN
python3 yaml.safe_load <all docker-compose.yml>                                    → 25/25 OK
generate-env.sh dup test (# note ×2 + dupe SCARLIHQ_TOKEN) → keeps last, no abort  → PASS
generate-env.sh token test (TELEGRAM_BOT_TOKEN=abc=def= → NEW123:XY=Z=)            → PASS
generate-env.sh orphan test (JWT_SECRET half + STORAGE_ENCRYPTION_KEY half)         → WARN + JWT regen
```

---

## 🆕 What's New in v19.0.0 (vs v18.9.8)

**Config validation, .env hardening, and README refresh.** 6 fixes from the v19.0.0 review (P1 + P2).

This release tightens the dynamic-config pipeline (`models.yaml` → `generate-litellm-config.sh` → LiteLLM `config.yaml`) and the secrets file (`/etc/scarlix/.env`) that the backup hook sources under `set -euo pipefail`. Several latent issues inherited from v18.8.x sed rewrites are closed with explicit validation, atomic writes, and a doctor-side diagnostic.

### v19.0.0 fixes (6)
| # | Fix | Was (v18.8.x) | Now (v19.0.0) |
|---|-----|---------------|----------------|
| P1 | **model-manager: restart after update + staging dir** | Updated model moved into place but the running engine kept serving the old one; no staging dir → in-place overwrite risk during download | Restart engine after a successful update; stage the new model in a temp dir, then atomic `mv` into final path |
| P1 | **LiteLLM config change detection → restart** | `generate-litellm-config.sh` wrote `config.yaml` but LiteLLM kept the old routing in memory → routing stayed stale until manual restart | Detect config change (checksum/mtime) and restart the LiteLLM container so the new routing takes effect |
| P1 | **generate-litellm-config charset validation** | Model IDs from `models.yaml` were written into YAML unvalidated — a `model_path` containing `:`, `#`, `[`, `]` could produce an invalid `config.yaml` | `validate_model_id()` enforces `^[A-Za-z0-9._/@:-]+$` for SGLang / BeeLlama / Ollama IDs; exit 1 on violation |
| P1 | **`.env` orphan line check in `scarlix-doctor`** | Old `.env` files from base64-64 multiline writes left orphan lines; the backup hook sources `.env` under `set -euo pipefail` → silent abort | `scarlix-doctor` scans for non-assignment lines and offers `fix_regenerate_env` (calls `generate-env.sh` or strips bad lines) |
| P2 | **atomic `.env` write (`mktemp` + `mv`)** | `cat > .env` left a truncated file if interrupted mid-write; next `source .env` failed under `set -e` | Write to `mktemp` then `mv -f` into place — readers always see a complete file |
| P2 | **model-manager YAML validation** | `yq` errors on individual keys gave confusing "model_path missing" errors when the real problem was invalid YAML syntax | Explicit `yq -e '.'` validation up front with a clear error message |

---

## 🆕 What's New in v18.4 (vs v18.3)

**Critical security + upgrade reliability.** 12 fixes from expert review.

v18.3 had a **P0 Local Privilege Escalation**: `chown -R REAL_USER /etc/scarlix /opt/scarlix` gave user ownership of `.env` files that root services later `source`d → user writes `$(touch /root/PWNED)` into `.env` → root executes it via `source` = **full root compromise**.

### P0 — local privilege escalation (5)
| # | Fix | v18.3 Problem | v18.4 Solution |
|---|-----|---------------|-------------------|
| P0 | **No chown /etc/scarlix or /opt/scarlix to user** | `chown -R REAL_USER /etc/scarlix /opt/scarlix` → user owns `.env` files that root `source`s | Only chown `/var/lib/scarlix` + `/mnt` (user data). `/etc/scarlix` + `/opt/scarlix` stay `root:root 755` |
| P0 | **`source` → `load_env_safe()` in model-manager** | `set -a; source /etc/scarlix/.env; set +a` — root executes user-controlled shell | `load_env_safe()`: parse `KEY=VALUE` via `while IFS='=' read` + `export` — NO shell evaluation |
| P0 | **`source` → `grep` in install.sh Phase 5** | `set -a; source /etc/scarlix/.env` for SCARLIHQ_TOKEN — LPE on re-run | `grep '^SCARLIHQ_TOKEN=' \| cut -d= -f2` — safe single-key extraction |
| P0 | **/opt/scarlix/.env root:root 600** | scarlix-mode `chown "$real_user" "$ENV_FILE"` → user-owned .env | `chown root:root "$ENV_FILE"` + `chmod 600` — root-only write |
| P0 | **SMG_MASTER_KEY not logged** | `echo "SMG_MASTER_KEY: $SMG_KEY"` → install.log (permanent secret leak) | `echo "SMG_MASTER_KEY: generated (in /etc/scarlix/.env)"` — no value in log |

### P1 — upgrade reliability (7)
| # | Fix | v18.3 Problem | v18.5 Solution |
|---|-----|---------------|-------------------|
| P1 | **Checkpoint tracks VERSION** | `is_checkpoint_valid()` didn't check version → upgrade skipped phases with old scripts | Added `scarlix_version=${VERSION}` to checkpoint + comparison |
| P1 | **Checkpoint tracks git commit hash** | Same version different commit → checkpoint valid → new files not installed | Added `repo_hash=$(git rev-parse --short HEAD)` to checkpoint + comparison |
| P1 | **rsync --delete for /opt/scarlix** | `cp -r` left stale files from old versions (deleted compose files persisted) | `rsync -a --delete` (with `rm -rf`+`cp` fallback if rsync unavailable) |
| P1 | **model-manager flock on /models** | No lock → race between model-manager download + scarlix-mode start | Shared flock (`flock -s`) on `/var/lock/scarlix-models.lock` |
| P1 | **download-models flock on /models** | Same race | Same shared flock |
| P1 | **scarlix-mode exclusive flock on /models** | No lock → start SGLang during model download → corrupt model | Exclusive flock (`flock -x`) for write ops, shared (`flock -s`) for read ops |
| P1 | **host-status.json mktemp** | Fixed `.tmp` name — predictable path (TOCTOU risk) | `mktemp "${STATUS_FILE}.XXXXXX"` — random suffix |

### Security regression test
```bash
# 1. As user, modify /opt/scarlix/.env (should be root:root 600 → permission denied)
echo '$(touch /root/PWNED)' >> /opt/scarlix/.env  # → Permission denied

# 2. Run model-manager as root
systemctl start model-manager.service

# 3. Verify /root/PWNED does NOT exist (LPE closed)
ls /root/PWNED  # → No such file
```

---

## 🆕 What's New in v18.3 (vs v18.2)

**Critical correctness fixes.** 11 fixes from 4 expert reviews.

v18.2 had a **P0 octal permission bug**: `stat -c %a` returns "600" (string), but `$((perms & 022))` interpreted 600 as **decimal 600** → `600 & 022 = 16` (not 0) → host bridge **rejected legit 0600 files** created by Go `os.CreateTemp()` → **mode switch from dashboard was completely broken**.

### P0 — critical (3)
| # | Fix | v18.2 Problem | v18.5 Solution |
|---|-----|---------------|-------------------|
| P0 | **Octal permission bug** | `$((perms & 022))` interpreted "600" as decimal → `600 & 022 = 16` → REJECTED all 0600 files from Go CreateTemp → mode switch broken | Explicit `case "$mode" in 600\|640\|700)` whitelist — no arithmetic, no octal/decimal confusion |
| P0 | **`local` outside function** | `local pending_mode` in install.sh migration block (not in a function) — bash error on `set -e` | Removed `local` — plain variable assignment |
| P0 | **TOCTOU mitigation** | validate → then later read (window for attacker to swap file) | Read content **immediately** after validate (minimizes window; full atomicity needs Go helper) |

### P1 — correctness (5)
| # | Fix | v18.2 Problem | v18.5 Solution |
|---|-----|---------------|-------------------|
| P1 | **scarlix-mode ai returns failure** | `start_verified_ai` always implicit `exit 0` → host-bridge saw "applied" even with no engine → dashboard lied | Returns `1` if no engine (sglang+vllm+beellama+ollama all 0). `ai`/`turbo` write "failed" to state + `exit 1` |
| P1 | **CI go test mask removed** | `go test ./... \|\| echo "(no tests yet — OK)"` — hid test failures | `if go list ./... \| grep -q .; then go test ./...; else echo "(no tests)"` — real failures fail CI |
| P1 | **host bridge fail-closed** | `chown root:root ... \|\| true` + `chmod 700 ... \|\| true` — continued on failure → security boundary broken | `if ! chown/chmod; then exit 1` — aborts if can't enforce root:root 700 on bridge-state/ |
| P1 | **scarlix-doctor token auth** | `http_ok /api/health` without token → 401 → "not responding" false negative | `http_ok_with_token` with Bearer from `/etc/scarlix/.env` → correct 200 check + version match |
| P1 | **status.Read() returns error** | `_ = json.Unmarshal(data, &s)` — silent ignore → API returned zero values on corrupt JSON | `Read() (HostStatus, error)` + `ReadOrStale()` wrapper + `Stale` field in JSON |

### P1 — locale + quality (3)
| # | Fix | v18.2 Problem | v18.5 Solution |
|---|-----|---------------|-------------------|
| P1 | **df locale-independent** | `df -m /models` without `--output` — breaks on `LANG=sk_SK` (localized headers) | `df -m --output=size,avail /models` — deterministic columns |
| P1 | **model-manager.service header** | `Description=SCARLIX OS v17.5 Model Manager` — stale version | Updated to v18.5 |
| P1 | **host-bridge header comment** | Said "v18.0.0" — stale | Updated to v18.5 |

### Verification
- `bash -n` on 8 scripts: OK
- YAML validation on 26 files: 0 errors
- Go: `var Version` (ldflags works), `ReadOrStale()` replaces old `Read()`
- Grep confirms: 0 `local` outside functions, 0 `|| echo` masking go test, 0 `|| true` on mkdir/chown/chmod

---

## 🆕 What's New in v18.2 (vs v18.1)

**Security + UX fixes.** 13 fixes from 2 expert reviews.

v18.1 had a **P0 real_user bug**: when systemd timer ran scarlix-mode as root, `$SUDO_USER` was empty + `$USER` was "root" → `chown root /opt/scarlix` → user CLI broke. Also `const Version` in Go couldn't be overridden by ldflags.

### P0 — security (1)
| # | Fix | v18.1 Problem | v18.5 Solution |
|---|-----|---------------|-------------------|
| P0 | **real_user detection under systemd** | `${SUDO_USER:-${USER:-scarlix}}` → when systemd runs as root, $USER=root → chown root /opt/scarlix → user `scarlix-mode ai` fails "Permission denied" | Detect from `/opt/scarlix` ownership (set by install.sh) or UID 1000 fallback. Works under systemd, sudo, and direct user CLI. |

### P1 — correctness + security (6)
| # | Fix | v18.1 Problem | v18.5 Solution |
|---|-----|---------------|-------------------|
| P1 | **const Version → var Version** | `-ldflags "-X main.Version"` doesn't work on `const` → version always "18.1" in binary even with `--build-arg SCARLIX_VERSION=18.2` | Changed to `var Version` in main.go + api/rest.go. main.go passes Version to NewHandler. ldflags now overrides correctly. |
| P1 | **scarlix-mode systemctl sudo fallback** | v18.1 removed sudo → user `scarlix-mode game` fails silently on `systemctl start sunshine` | `if [ "$(id -u)" -eq 0 ]; then systemctl...; else sudo systemctl...; fi` — works for both root (host-bridge) and user (CLI) |
| P1 | **token only on TTY** | `info "Dashboard login token: $TOKEN"` → tee to install.log (644) → token readable by all local users | `if [ -t 1 ]; then echo to /dev/tty; else info "token in /etc/scarlix/.env"` + `chmod 600 install.log` |
| P1 | **FIFO/pipe/socket rejection** | validate_input_file checked symlink + owner, but not FIFO → `mkfifo desired-mode` would block bridge timer (DoS) | Explicit `[ -p ]`, `[ -S ]`, `[ -b ]`, `[ -c ]` checks (in addition to existing `-f` which already rejects non-regular) |
| P1 | **models.yaml schema validation** | `yq '.sglang.hf_repo'` silently returned empty on typo → script continued with empty paths | `validate_models_yaml()` checks 5 required keys before download. Fail-fast with clear error. |
| P1 | **go.sum handling** | go.sum was 0 bytes → non-deterministic build, offline install impossible | Removed go.sum from repo (generated by Dockerfile `go mod download`). CI `go mod verify` checks integrity. |

### P2 — UX + consistency (6)
| # | Fix | v18.1 Problem | v18.5 Solution |
|---|-----|---------------|-------------------|
| P2 | **CI smoke test chown 65532** | CI created bridge-input/ as root → nonroot container couldn't write desired-mode → mode POST 202 test failed | `sudo chown -R 65532:65532 /tmp/scarlix-test/bridge-input` before test |
| P2 | **migration applies pending desired-mode** | `rm -rf /var/lib/scarlix/bridge` deleted pending desired-mode → lost mode switch request | Read + apply pending mode via `scarlix-mode "$pending"` BEFORE rm -rf |
| P2 | **whiptail ESC/Cancel handling** | `IP=$(whiptail ...)` without `|| true` → ESC aborts whole wizard via `set -e` | All whiptail calls have `|| fallback` + empty validation |
| P2 | **bridge restores retry_count + last_error** | last-transition file stored retry_count + error, but bridge didn't restore them for status | Read retry_count + error from last-transition when transition="none" |
| P2 | **creative/tv ensure .env exists** | `creative` mode called `$DC` without .env → compose fails if no prior `scarlix-mode ai` | `[ -f "$ENV_FILE" ] || load_model_paths` before $DC |
| P2 | **.env.template updated** | Header said "v15" + no SCARLIHQ_TOKEN | Updated to v18.5 + added SCARLIHQ_TOKEN + note that install.sh auto-generates |

### Review false-positives (verified already-correct in v18.1)
- Review 2 P0-1 (|| true masking) — **already fixed in v18.1** (reviewer checked v18.0.0)
- Review 2 P1-5 (host-status.json mount) — already mounted via `/var/lib/scarlix:ro`
- Review 2 P2-6 (guard dead code) — Guard IS used (in main.go, rest.go, mcp/server.go)
- Review 1 #5 (host-bridge input validation) — validate_input_file already exists since v18.0.0

### Decisions made (where I couldn't decide automatically)
- **go.sum**: Committed empty (0 bytes) vs generated at build time → chose **generated** (no local Go to create deterministic hashes; Dockerfile `go mod download` generates it; CI `go mod verify` validates)
- **scarlix-mode sudo**: Removed entirely vs conditional → chose **conditional** (`if root: systemctl; else: sudo systemctl`) — works for both host-bridge (root) and user CLI (sudo)

---

## 🆕 What's New in v18.1 (vs v18.0.0)

**Correctness fixes — retry actually works now.** 6 fixes from self-audit.

v18.0.0 had a **P0 logic bug**: `mode_output=$(... 2>&1) || true` masked the exit code to 0, so `mode_rc=$?` was always 0 → transition always "applied" even when scarlix-mode failed → retry mechanism never triggered.

### P0 — correctness (1)
| # | Fix | v18.0.0 Problem | v18.5 Solution |
|---|-----|-----------------|-------------------|
| P0 | **mode_rc exit code capture** | `|| true` after command substitution masked exit code to 0 → `mode_rc` always 0 → transition always "applied" even on failure → retry never triggered | Removed `|| true` (script uses `set -uo pipefail`, not `-e`, so assignment doesn't abort). `mode_rc=$?` now captures real exit code. Retry + failure states work correctly. |

### P1 — quality (5)
| # | Fix | v18.0.0 Problem | v18.5 Solution |
|---|-----|-----------------|-------------------|
| P1 | **install.sh migration rm -rf** | `rmdir /var/lib/scarlix/bridge` fails if dir has hidden files (`.retry`) → migration stuck | `rm -rf` (safe — only the bridge/ dir being migrated) |
| P1 | **Dockerfile header comments** | Header said "v17.9.9" + "bridge/desired-mode" (stale v18.0.0 didn't update) | Updated to v18.5 + "bridge-input/desired-mode" |
| P1 | **scarlix_mode.Set() dir check** | If `bridge-input/` dir doesn't exist, `os.CreateTemp` fails with cryptic error | Explicit `os.Stat(dir)` check → clear 503 error message |
| P1 | **Removed unused Transition() method** | `scarlix_mode.Transition()` defined but never called → `status` import only for it | Removed method + import (cleaner, avoids go vet warning) |
| P1 | **host-bridge last_error sanitization** | `last_error` from scarlix-mode output could contain newlines → broke env var passing to python3 | `tr '\n' ' ' + tr -d '\r'` before storing in last_error |

### Verification
- `bash -n` on all 8 scripts: OK
- YAML validation on 26 compose + models.yaml: 0 errors
- Go imports verified: no unused imports (removed `status` from scarlix_mode)
- Grep verified: 0 occurrences of `|| true` masking exit codes in host-bridge

---

## 🆕 What's New in v18.0.0 (vs v17.9.9)

**Secure Host-Bridge — privilege boundary hardened.** 10 fixes from 2 reviews.

v17.9.9 had a **P0 symlink vulnerability**: ScarliHQ owned `bridge/` dir, root wrote `.retry` to it. If ScarliHQ created a symlink `bridge/.retry -> /etc/shadow`, root would write to `/etc/shadow`. v18.0.0 separates writable input dir from root-owned state dir.

### P0 — privilege boundary (2)
| # | Fix | v17.9.9 Problem | v18.0.0 Solution |
|---|-----|-----------------|-------------------|
| P0 | **Separate bridge-input/ from bridge-state/** | Single `bridge/` dir owned by 65532 + root wrote `.retry` there = symlink attack | `bridge-input/` (65532:65532, 700) — ScarliHQ writes ONLY desired-mode. `bridge-state/` (root:root, 700) — root writes retry/last-transition. ScarliHQ cannot create symlinks in bridge-state/. |
| P0 | **Single scarlix-mode execution** | On failure, host bridge ran `scarlix-mode` AGAIN to capture error output → double mode transition (e.g. 2× compose up, 2× stop) | Capture output once: `output=$("$MODE_BIN" "$mode" 2>&1)` then check `$?` |

### P1 — security + state machine (8)
| # | Fix | v17.9.9 Problem | v18.0.0 Solution |
|---|-----|-----------------|-------------------|
| P1 | **Concurrent Mode.Set() race** | Two concurrent POST /api/mode used same `.tmp` file → race (A writes, B writes, A renames, B renames → "no such file") | `os.CreateTemp(dir, ".desired-mode-*")` — unique temp per request |
| P1 | **GET /api/mode returns transition state** | Only returned `{"mode":"ai"}` — dashboard couldn't show retry/failed | Returns full: `mode`, `requested_mode`, `transition_state`, `retry_count`, `last_error`, `last_timestamp` |
| P1 | **Persistent last-transition** | After max retries, state cleared on next timer tick → diagnostic info lost | `last-transition` file in bridge-state/ (root 700) — survives timer cycles |
| P1 | **scarlix-mode .env permission** | `/opt/scarlix/.env` owned root:root → user `scarlix-mode ai` fails "Permission denied" | If root: chown `/opt/scarlix` to REAL_USER before write. If user: warn with fix command. |
| P1 | **crypto/subtle.ConstantTimeCompare** | Custom `secureCompare()` with early `len(a) != len(b)` return — not truly constant-time | `subtle.ConstantTimeCompare()` from `crypto/subtle` (standard library, audited) |
| P1 | **Host bridge input validation** | Read `desired-mode` without checks → root could follow symlink created by nonroot | `validate_input_file()`: rejects symlinks, checks owner=65532, no world-writable, max 100 bytes |
| P1 | **No hardcoded version in host bridge** | `"version": "17.9.9"` hardcoded in JSON → duplicate source of truth | Reads `scarlix_version` from VERSION file (single source) |
| P1 | **JSON via env vars + single python3** | Nested string interpolation `python3 -c "...\"$last_error\"..."` — broke on quotes/backslashes in error messages | Pass all values as env vars, single `python3 -c` reads `os.environ` — no interpolation |

### P2 — CI (1)
| # | Fix | v17.9.9 Problem | v18.0.0 Solution |
|---|-----|-----------------|-------------------|
| P2 | **CI uses VERSION as build-arg + integration test** | CI used Dockerfile default (hardcoded 17.9.9); smoke test only checked health endpoint | CI reads `VERSION` file as `--build-arg`; smoke test now POST /api/mode → checks `desired-mode` file written → checks 202/400 error codes |

### Breaking change: bridge/ → bridge-input/ + bridge-state/
Existing v17.9.9 installs: install.sh auto-migrates (removes old `bridge/` dir, creates new layout). ScarliHQ compose mount updated from `bridge:rw` to `bridge-input:rw`.

### Review false-positives (verified already-correct in v17.9.9)
- `internal/api/` and `internal/profiles/` packages **DO exist** on remote main + tag v17.9.9 (verified via `git ls-tree -r origin/main`). Reviewer checked stale GitHub web UI cache.
- `yq -r '.key'` works with python-kislyuk (jq wrapper, `-r` is jq flag).

---

## 🆕 What's New in v17.9.9 (vs v17.9.8)

**Host-Bridge stabilization + state machine — 13 fixes from 3 reviews.**

v17.9.8 introduced the host-bridge architecture (correct direction) but had 3 P0 bugs that prevented ScarliHQ from actually working: nonroot user didn't exist in alpine, bridge/ directory was root-owned (nonroot couldn't write), and df parsing had swapped columns.

### P0 — ScarliHQ actually works now (3)
| # | Fix | v17.9.8 Problem | v17.9.9 Solution |
|---|-----|-----------------|-------------------|
| P0 | **Dockerfile nonroot user** | `USER nonroot:nonroot` — alpine has no such user → container crash on start | `RUN adduser -D -u 65532 nonroot` creates the user before `USER nonroot` |
| P0 | **bridge/ directory permissions** | `chmod 755 root:root` → nonroot container couldn't write `desired-mode` → mode switch always failed | `chown 65532:65532 + chmod 775` — nonroot can write, root bridge can read |
| P0 | **host-bridge df parsing** | `read -r _ _ total used` — columns swapped (total=Used, free=Used-Avail) | `read -r _ size used avail _` — correct columns per `df -m` output |

### P1 — reliability + security (8)
| # | Fix | v17.9.8 Problem | v17.9.9 Solution |
|---|-----|-----------------|-------------------|
| P1 | **HTTP error codes** | `/api/mode` returned `200 OK` + `{"status":"error"}` on failure | `writeJSONError()` with proper codes: 400 (invalid mode), 503 (fs error), 500 (internal) |
| P1 | **desired-mode retry on failure** | `rm -f desired-mode` always — even on failure → lost retry info | Keep on failure + retry counter (max 3); only delete on success or max-retries |
| P1 | **host-bridge JSON via python3** | Shell heredoc `cat <<EOF` — broke on container names with quotes/backslashes | `python3 -c json.dumps()` — safe escaping for all string values |
| P1 | **State machine fields** | `mode_applied: "ERROR: ..."` (unstructured string) | `mode_transition: {requested, state, retry_count, last_error}` — frontend renders status |
| P1 | **MCP secureCompare** | `if token != s.authToken` (timing attack risk) | `secureCompare()` constant-time comparison (same as REST API) |
| P1 | **WS origin CIDR check** | `strings.HasPrefix(origin, "http://10.")` — crude, rejected https, no real IP validation | `net.ParseIP + net.IPNet.Contains` with proper CIDR ranges (127.0.0.1/32, 10/8, 172.16/12, 192.168/16) |
| P1 | **scarlix-mode: removed sudo** | 5× `sudo systemctl` — bridge runs as root, sudo is unnecessary dependency | Direct `systemctl` (bridge is already root) |
| P1 | **scarlix-mode turbo dump_vram** | `turbo` called `start_verified_ai` without VRAM cleanup → CUDA OOM if creative/game held VRAM | `dump_vram` before `start_verified_ai` (VRAM safety gate) |

### P1 — host-bridge systemd hardening
```ini
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
RestrictRealtime=true
RestrictNamespaces=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=CAP_SYS_ADMIN CAP_NET_RAW CAP_DAC_OVERRIDE CAP_KILL
```
(ProtectSystem=strict NOT used — scarlix-mode writes to /var/lib/scarlix + needs docker socket)

### P2 — hardening (2)
| # | Fix | v17.9.8 Problem | v17.9.9 Solution |
|---|-----|-----------------|-------------------|
| P2 | **mem-fraction configurable** | SGLang/vLLM `--mem-fraction 0.85` hardcoded → OOM on 8GB GPU | `${SGLANG_MEM_FRACTION}` + `${VLLM_GPU_UTIL}` env vars from models.yaml |
| P2 | **Version via -ldflags** | `const Version = "17.9.8"` hardcoded in 3 Go files | `-ldflags "-X main.Version=$VERSION"` in Dockerfile (single source: VERSION file) |

### CI: runtime smoke test
Added `scarlihq-docker-build` job now runs the container + tests:
- Container starts without crash (catches nonroot/permission issues)
- `GET /api/health` without token → `401` (auth works)
- `GET /api/health` with token → `200` (full path works)

### Review false-positives (verified already-correct in v17.9.8)
- `internal/api/` and `internal/profiles/` packages **DO exist** (rest.go 6159B, loader.go 2619B) — reviewer 2 checked stale GitHub cache
- `yq -r '.key'` works with python-kislyuk (it's a jq wrapper, `-r` is a jq flag) — reviewer 3 was wrong about syntax mismatch

---

## 🆕 What's New in v17.9.8 (vs v17.9.7)

**ScarliHQ privilege boundary + reliability — 16 fixes from 3 reviews.**

v17.9.7 introduced a ScarliHQ dashboard that mounted docker.sock + scarlix-mode + nvidia runtime into the container — functional but a privilege-escalation nightmare (any LAN client with the token could run arbitrary Docker commands). v17.9.8 replaces this with a **host-bridge architecture**.

### 🏗️ Architecture change: ScarliHQ Host-Bridge

```
┌─────────────────┐         ┌──────────────────────┐
│  ScarliHQ       │  reads  │ /var/lib/scarlix/    │
│  (alpine, ~20MB,│ ◀────── │   host-status.json    │ ◀── scarlix-host-bridge.timer
│   non-root,     │         │ /var/lib/scarlix/     │     (host, root, every 5s:
│   NO docker.sock│ writes  │   bridge/desired-mode │     nvidia-smi + docker ps +
│   NO nvidia,    │ ──────▶ │                       │     scarlix-mode)
│   NO scarlix-mode│        └──────────────────────┘
└─────────────────┘
```

ScarliHQ has **zero** privileged host access. It reads a JSON status file and writes a desired-mode file. The host-side `scarlix-host-bridge` systemd timer (runs as root, every 5s) does all privileged work: `nvidia-smi`, `docker ps`, `scarlix-mode <mode>`.

### P0 — install/build blockers (6)
| # | Fix | v17.9.7 Problem | v17.9.8 Solution |
|---|-----|-----------------|-------------------|
| P0 | **Dockerfile build order** | `COPY . .` before `go mod download` → fragile go.sum | `COPY go.mod` → `go mod download` → `COPY . .` → `go build` (standard Go Docker pattern) |
| P0 | **go.mod cleaned** | 9 unused heavy deps (nostr, zerolog, cobra, viper, otel, crypto, sqlite, chi, uuid) → 400MB `go mod download`, slow builds | Only `gorilla/websocket` + `gopkg.in/yaml.v3` (2 deps, <10s download) |
| P0 | **ScarliHQ privilege boundary** | Mounted `docker.sock:ro` + `scarlix-mode:ro` + nvidia runtime → any token-holder = root on host | Host-bridge architecture: ScarliHQ has NO docker.sock, NO scarlix-mode, NO nvidia. Only file read/write |
| P0 | **API authentication** | `/api/mode?set=ai` accepted ANY request (no auth) → LAN anyone could switch modes | All `/api/*` + `/ws` require `Authorization: Bearer $SCARLIHQ_TOKEN` (generated by `generate-env.sh`) |
| P0 | **Mode API/CLI/UI unified** | Go `Set()` accepted only `ai/game/turbo/offline`; UI had `stop`; CLI had `stop/creative/tv` | All 7 modes valid everywhere: `ai/stop/game/creative/turbo/offline/tv` |
| P0 | **Go/MCP version 12.0 → 17.9.8** | `main.go` + `rest.go` + `mcp/server.go` hardcoded `"v12.0"` | `const Version = "17.9.8"` in main, passed to api + mcp |

### P0 — Go correctness (4)
| # | Fix | v17.9.7 Problem | v17.9.8 Solution |
|---|-----|-----------------|-------------------|
| P0 | **profiles YAML parsed** | `Get()`/`List()` used filename as Name+DisplayName, ignored YAML fields | `yaml.Unmarshal` — reads `display_name`, `role`, `hud_theme`, `token_budget` from `.yaml` files |
| P0 | **MCP = real JSON-RPC 2.0** | `/mcp` returned static JSON `{"protocol":"mcp/v1"}` — not a protocol implementation | `/rpc` endpoint: `initialize` + `tools/list` + `tools/call` with proper request IDs + error codes (`-32601` etc.) |
| P0 | **`Current()` TrimSpace** | Returned `"ai\n"` (state file has trailing newline) → `mode != "ai"` always true in Go | `strings.TrimSpace(string(data))` |
| P0 | **`main.go` dead code removed** | `getGPUStatus()` + `writeJSON()` duplicated (also in rest.go) — unused | Removed from main.go; rest.go is the single source |

### P1 — reliability (6)
| # | Fix | v17.9.7 Problem | v17.9.8 Solution |
|---|-----|-----------------|-------------------|
| P1 | **`crit()` aborts immediately** | Set `CRITICAL_FAIL=1` + continued → cascading secondary errors (toolkit fail → Docker start → network → Phase 4…) | `crit()` calls `exit 1` after logging — clean abort, re-runnable |
| P1 | **Phase 1 critical ops = crit** | `pacman -Syu` / package install were `fail()` (non-crit) → checkpoint written despite broken state → re-run skips broken phase | Both are now `crit()` → abort before checkpoint |
| P1 | **multilib scoped + synced** | Aggressive `sed` uncommented EVERY `Include=` line; no `pacman -Sy` after enable | Scoped `awk` (only `[multilib]` block) + explicit `pacman -Sy` (without it: "target not found") |
| P1 | **WebSocket real status + origin** | Pushed only `{"time":"..."}` every 2s; `CheckOrigin: return true` (any origin) | Pushes full host-status JSON (GPU/mode/containers/disk); origin check: localhost + LAN private ranges only |
| P1 | **download-models fail-hard** | Missing Ollama compose = silent skip → "complete"; missing SGLang `hf_repo` = warn → "complete" | Both now `FAILED=$((FAILED+1))` → exit 1 |
| P1 | **model-manager Ollama via docker** | Used `command -v ollama` (host binary — never installed; Ollama runs in container) | `docker exec ollama-agent ollama pull` + checks container running |

### P2 — hardening (3)
| # | Fix | v17.9.7 Problem | v17.9.8 Solution |
|---|-----|-----------------|-------------------|
| P2 | **ScarliHQ runtime slim** | `nvidia/cuda:12.8.0-base` + `docker.io` = ~2.5GB image | `alpine:3.20` + `ca-certificates` + `tzdata` = ~20MB (100× smaller) |
| P2 | **`.env` permissions 600** | `chmod 664` (group-readable; `.env` may hold `HF_TOKEN` in future) | `chmod 600` (owner-only) |
| P2 | **CI: go build + docker build** | CI only did shellcheck + YAML + bash -n — Go compile errors + Dockerfile issues slipped through | Added `go-build` job (`go vet` + `go build` + `go test`) + `scarlihq-docker-build` job (real `docker build`) |

### Review false-positives (verified already-correct in v17.9.7)
- `internal/api/` and `internal/profiles/` packages DO exist (not 404 — reviewer may have checked a stale GitHub cache).
- `yq -r '.key'` works with both python-kislyuk-yq (Arch package) AND go-yq — no syntax mismatch.
- `usermod -aG docker` is in Phase 3 (after docker group created in Phase 1).

---

## 🆕 What's New in v17.9.7 (vs v17.9.6)

**Reliability + real dashboard — 11 fixes.** (See git history for full table.)

Key: ScarliHQ image tag unified, `--disable-flashinfer`, network disconnect loop, multilib pre-check, download-models FAILED counter, `http_ok()` fallback, `/models` chmod 750, git tag pushed.

---

## 🎮 GPU Modes (CLI + Dashboard)

```bash
# CLI (host, direct):
scarlix-mode ai        # Verified AI path + healthcheck
scarlix-mode turbo      # Max throughput
scarlix-mode offline    # BeeLlama CPU (q4_0 KV, 32k context)
scarlix-mode game       # Native Steam/Sunshine (needs host systemd — NOT from dashboard)
scarlix-mode creative   # ComfyUI + Video + Music
scarlix-mode tv         # Docker Sunshine
scarlix-mode stop       # Stop AI stack (keeps dashboard/infra)
scarlix-mode vram       # VRAM health
scarlix-mode status     # System summary
scarlix-doctor          # Self-diagnostic (--fix for auto-fix)
```

**Dashboard** (`http://<ip>:8090/` — login with `SCARLIHQ_TOKEN` from `/etc/scarlix/.env`): all 7 modes clickable. `game`/`tv` request the switch but `systemctl sunshine` runs on host (bridge applies it) — see Known Limitations.

---

## 🧪 Model-Agnostic (Single Source of Truth)

Edit `/etc/scarlix/models.yaml`:
```yaml
sglang:
  model_path: "/models/Qwen3-14B-AWQ"
  hf_repo: "Qwen/Qwen3-14B-AWQ"
  # Change to ANY HuggingFace model:
  # model_path: "/models/Meta-Llama-3.1-8B-Instruct"
  # hf_repo: "meta-llama/Llama-3.1-8B-Instruct"
```
Then:
```bash
download-models.sh   # download new model (disk-space pre-checked)
scarlix-mode ai      # restart with new model (hash change → force-recreate)
```

---

## 🔧 NVIDIA Rollback

```bash
sudo downgrade nvidia-open      # rollback driver
sudo grub-set-default 1 && sudo reboot   # boot LTS kernel
```

---

## 📁 File Layout (v17.9.8)

```
OS/
├── install.sh                              # v17.9.8: Bootstrap (5 phases, host-bridge setup, multilib, disk-check)
├── README.md
├── AGENTS.md
├── VERSION                                 # 17.9.8 (single source of truth)
├── models.yaml                             # Single source of truth (env vars → compose)
├── packages.x86_64                         # Package list (yq, curl, wget, steam, wine, …)
├── .github/workflows/ci.yml               # CI: shellcheck + bash + YAML + compose + go-build + docker-build
├── files/
│   ├── usr/local/bin/
│   │   ├── scarlix-wizard                  # Creates .experimental for 2+ GPU, gateway prompt
│   │   ├── scarlix-mode                    # Healthcheck + env var parsing + wait_for_healthy
│   │   ├── scarlix-doctor                  # Self-diagnostic (--fix, http_ok -k, host-bridge check)
│   │   ├── scarlix-host-bridge             # v17.9.8 NEW: privileged ops for dashboard (systemd timer)
│   │   ├── download-models.sh              # HF download (FAILED counter + disk + missing-config fail)
│   │   └── model-manager.sh                # Weekly HF auto-pull + Ollama via docker exec + Telegram
│   └── etc/
│       ├── systemd/
│       │   ├── zram-generator.conf
│       │   └── system/
│       │       ├── model-manager.{service,timer}
│       │       ├── scarlix-host-bridge.{service,timer}   # v17.9.8 NEW: 5s status refresh
│       │       └── generate-env.sh         # /etc/scarlix/.env (secrets + SCARLIHQ_TOKEN, guarded)
│       └── pacman.d/hooks/
├── ai/                                     # Docker stacks (all scarlix-net)
│   ├── sglang/docker-compose.yml          # cu128, --disable-flashinfer, env vars
│   ├── vllm/docker-compose.yml            # TP=1, no LoRA
│   ├── llamacpp/docker-compose.yml        # Official llama.cpp, q4_0 KV, wget healthcheck
│   ├── ollama/docker-compose.yml          # Fallback (starter qwen2.5:3b), CPU, mem_limit 6g
│   └── comfyui/docker-compose.yml         # Creative profile, absolute volumes
├── scarlihq/                               # Dashboard (host-bridge architecture, alpine)
│   ├── Dockerfile                          # Multi-stage: go build → alpine (NO docker.sock, NO nvidia)
│   ├── docker-compose.yml                  # image: scarlihq:latest, ro mounts + bridge/ rw
│   ├── go.mod / go.sum                    # v17.9.8: only gorilla/websocket + yaml.v3 (was 11 heavy deps)
│   ├── cmd/scarlihq/
│   │   ├── main.go                          # Version const, serves HTML + registers routes
│   │   └── frontend/dist/index.html        # Real dashboard (token auth, all 7 modes, live WS)
│   └── internal/
│       ├── api/rest.go                      # /api/* (auth middleware, reads host-status.json)
│       ├── scarlix_mode/mode.go             # Current() TrimSpace, Set() writes desired-mode
│       ├── profiles/loader.go               # Real yaml.Unmarshal
│       ├── webui/ws.go                      # Real status push + origin check
│       ├── mcp/server.go                    # Real JSON-RPC 2.0 (initialize/tools/list/tools/call)
│       ├── guard/guard.go                   # Command safety patterns
│       └── status/status.go                 # Shared HostStatus type + reader
└── agents/, gaming/, voice/, network/, security/, monitoring/, ...
```

---

## 🏗️ Verified AI Path (v17.9.8)

```
GPU 0: RTX 5060 Ti 16GB (Blackwell sm_120)
└── Tier-1: SGLang v0.4.4-cu128 (--disable-flashinfer, mem-fraction 0.85)

GPU 1: RTX 4060 Ti 16GB (Ada sm_89)
└── Tier-2: vLLM v0.8.0 (TP=1, mixed-arch safe)

CPU:
└── Tier-4: llama.cpp (official image, q4_0 KV cache, 32k context)

Fallback (always ready):
└── Ollama 0.5.4 + qwen2.5:3b (starter model auto-downloaded)
```

### 🔄 Fallback Chain
1. `scarlix-mode ai` → SGLang (+ vLLM if `.experimental`)
2. SGLang unhealthy after 300s → `docker stop sglang` → try vLLM
3. vLLM unhealthy after 300s → `docker stop vllm` → try BeeLlama (CPU)
4. BeeLlama fails → Ollama (qwen2.5:3b, always ready)
5. Re-run `scarlix-mode ai` → healthchecks + restarts unhealthy

### ScarliHQ Dashboard (:8090)
Host-bridge architecture: dashboard reads JSON status, writes desired-mode. No privileged host access.
- GPU cards (temp, util, VRAM, power, compute_cap)
- Mode switcher (all 7 modes — async via host bridge, ~5s)
- Container table (name, status, ports)
- Live updates via WebSocket (every 2s)
- Token auth on all API + WS endpoints

---

## 📜 Version History

| Version | Date | Key Changes |
|---------|------|-------------|
| **v18.5** | 2026-10 | **Parent dir boundary + user CLI + WS hardening. P0: /var/lib/scarlix root:root 755 (was user-owned → symlink attack), scarlix-mode sudo re-exec (CLI was dead after LPE fix), download-models sudo re-exec, /opt/scarlix/.env chowned root on upgrade, flock -n (was blocking for hours). P1: models.yaml preserved on upgrade, exclusive locks for model writes, WS write deadline + 16 connection limit, status.Read() real stale check (time.Parse), mem_fraction in hash, fallback JSON Error field. 10 fixes from 2 reviews.** |
| v18.4 | 2026-10 | Security + upgrade reliability. P0: Local Privilege Escalation fixed (chown -R user /etc+opt → root:root; source .env → load_env_safe; SMG_KEY not logged). P1: checkpoint version+hash, rsync --delete stale files, flock on /models (model-manager + download-models + scarlix-mode), mktemp for host-status.json. 12 fixes. |
| v18.3 | 2026-10 | Critical correctness. P0: octal permission bug (600&022=16 → rejected all 0600 files → mode switch broken!), `local` outside function, TOCTOU mitigation. P1: scarlix-mode returns failure if no engine healthy, CI go test no mask, host bridge fail-closed, scarlix-doctor token auth, status.Read() error on corrupt JSON, df locale-independent, stale version headers. 11 fixes from 4 reviews. |
| v18.2 | 2026-10 | Security + UX. P0: real_user detection under systemd (was: $USER=root → chown root). P1: const→var Version (ldflags fix), systemctl sudo fallback, token TTY-only + log 600, FIFO/pipe rejection, models.yaml schema validation, go.sum generated at build. P2: CI chown 65532, migration applies pending mode, whiptail ESC, bridge retry_count restore, creative/tv .env check, .env.template v18.2. |
| v18.1 | 2026-10 | Correctness fixes. P0: host-bridge mode_rc exit code capture (was `|| true` masking to 0 → retry never triggered). P1: install.sh migration rm -rf, Dockerfile header updated, scarlix_mode.Set() dir check, removed unused Transition(), last_error sanitization. |
| v18.0.0 | 2026-10 | Secure Host-Bridge. 10 fixes: bridge-input/+bridge-state/ separation (P0 symlink attack fix), single scarlix-mode execution (P0 double-run fix), concurrent Mode.Set os.CreateTemp, GET /api/mode transition state, persistent last-transition, scarlix-mode .env permission, crypto/subtle, input validation (symlink/owner/size), no hardcoded version, CI VERSION build-arg + mode integration test. Breaking: bridge/ → bridge-input/ + bridge-state/. |
| v17.9.9 | 2026-10 | Host-Bridge stabilization + state machine. 13 fixes: Dockerfile nonroot user (P0), bridge/ chown 65532+775 (P0), df parsing fixed (P0), HTTP error codes, desired-mode retry, JSON via python3, state machine fields, MCP secureCompare, WS CIDR origin, scarlix-mode sudo removed, turbo dump_vram, systemd hardening, mem-fraction env var, version via ldflags. CI: runtime smoke test. |
| v17.9.8 | 2026-10 | Host-bridge architecture + reliability. 16 fixes: ScarliHQ privilege boundary (P0), Dockerfile build order, go.mod cleaned, API auth, mode API/CLI/UI unified, Go/MCP v12→17.9.8, profiles YAML parsed, real JSON-RPC MCP, Current() TrimSpace, crit() aborts, Phase 1 crit ops, multilib scoped+synced, WS real status+origin, download-models fail-hard, model-manager Ollama via docker, alpine runtime 20MB, .env 600, CI go+docker build. |
| v17.9.7 | 2026-10 | Reliability + real dashboard. 11 fixes: ScarliHQ image tag, Dockerfile reorder, nvidia/cuda runtime, real dashboard HTML, git tag, SGLang --disable-flashinfer, network disconnect loop, multilib, disk checks, /models 750, http_ok fallback. |
| v17.9.6 | 2026-10 | Reliability. 15 fixes: duplicate networks, scarlix_net→scarlix-net, VERSION, default paths, download fail-hard, doctor unhealthy=FAIL, hash after recreate, $DC up -d, healthchecks, version unified, docs. |
| v17.9.1 | 2026-10 | Hotfix. 10 fixes: MODE pred flock, hash healthcheck, HF repo IDs, checkpoint nvidia_open, schema, scarlix_net, SGLang pin, generate-env guard, version, scarlix-doctor. |
| v17.5–17.9 | 2026-10 | EndeavourOS re-base, bootstrap installer, model-agnostic, fail-hard, TP=1. |
| v16.x | 2026-08 | Garuda Linux. |

---

## ⚠️ Known Limitations

- **CI covers syntax + build, not runtime**: `.github/workflows/ci.yml` runs shellcheck + bash -n + YAML + compose + `go build` + `docker build`. QEMU doesn't test NVIDIA/CUDA (no GPU in CI).
- **Single maintainer**: One person maintaining full stack.
- **vLLM TP=1**: Separate model per GPU (less efficient than TP=2 but mixed-arch safe).
- **ScarliHQ `game`/`tv` modes**: `scarlix-mode game` calls `systemctl start sunshine` on the HOST (via host-bridge, which runs as root — `sudo` removed in v17.9.9). If `sunshine.service` isn't installed, mode switch fails — dashboard now shows `✗ game failed` in mode transition field (v17.9.9 state machine). Check `/var/log/scarlix-host-bridge.log`.
- **ScarliHQ auth = single shared token**: No per-user auth. Token in `/etc/scarlix/.env` (chmod 600). v18.7.5: token no longer accepted via `?token=` URL param — dashboard reads it only from `sessionStorage` (paste-once-per-session via the auth gate); previously the URL `?token=` could leak via browser history/proxy logs (known limitation, now resolved). For production, put a reverse proxy with session auth in front.
- **First install is slow**: `pacman -Syu` + NVIDIA + CUDA + cuDNN + Steam/Wine + docker images + ScarliHQ Go build + model download (50-150GB) = hours. Reboots + re-login required for NVIDIA driver + docker group.
- **host-status.json 5s latency**: Dashboard data is up to 5s stale (host-bridge timer interval). Mode transition feedback (applied/retrying/failed) appears within 5s. Not for real-time control.

## ⚠️ Known Arch/EndeavourOS Risks

These are platform-level risks, not SCARLIX bugs. EndeavourOS/Arch is a rolling release — always test after `pacman -Syu`.

| Risk | Impact | Mitigation |
|------|--------|------------|
| Calamares + existing BTRFS subvolumes | Install fails if disk has existing `@`/`@home` | Use clean disk or rename existing subvolumes before install |
| `nvidia-container-toolkit` via AUR | GPU Docker runtime may break | Install from Docker official repo (`install.sh` Phase 3 already does this) |
| `nvidia-open` on Turing (RTX 20xx) | Power management gaps, flicker | Use `nvidia` (proprietary) if flicker occurs; `nvidia-open` is for Ampere+ |
| SGLang AUR packaging gaps | Tier-1 may need manual fixes | We use Docker image (not AUR) — avoids this |
| Ollama CUDA regressions after update | Tier-3 breaks | `model-manager.sh` tests health after pull; pinned to 0.5.4 |
| `snap-pac` backup hook fragility | Docker volumes may not snapshot | `scarlix-docker-backup.sh` runs BEFORE pacman; restic used as fallback |
| `nvidia-open-lts` + `nvidia-dkms` conflict | LTS fallback fails | `install.sh` uses atomic single `pacman -S` call to avoid this |
| `model-manager.timer` auto-update | May pull incompatible models | Timer tests health post-pull; rollback via Snapper if needed |

## 🗺️ Roadmap

- **v18.9**: LiteLLM E2E inference CI test, Go unit tests (ReserveWSTicket, Mode.Set O_EXCL), CI artifact sharing between jobs (faster), scarlix-doctor LiteLLM + model identity checks.
- **v19.0**: Per-user auth (OIDC/LDAP), real GPU telemetry via DCGM, mode-switch history, Incus dev workspaces.

## 🆕 What's New in v19.1.16 (vs v19.1.15)

**Critical heredoc fix + security hardening.** 1 P0 + 4 P1 + 7 P2 from 4 independent reviews.

Four reviews of v19.1.15 found a critical P0 (VLLM_TRUST_FLAG logic inside heredoc → written as text, not executed), plus security issues (chown backup tree, agent.env empty keys, scheduler fail-open), stale headers, missing telemetry timers, and aggressive VRAM defaults for 16GB GPUs.

### P0 fix

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P0** | **VLLM_TRUST_FLAG heredoc bug** | `if`/`case`/`fi` block was inside `cat > .env << EOF` heredoc → written as literal text → VLLM_TRUST_FLAG always empty + 5 non-KEY=VALUE lines in .env breaking docker compose | Flag computed BEFORE heredoc: `case` validates `true`/`1`/`yes` → `--trust-remote-code`, else empty. Heredoc writes `VLLM_TRUST_FLAG=$vllm_trust_flag` (single line) |

### P1 fixes

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **chown -R /mnt/backup/restic** | Root backup hook writes to user-owned tree → symlink attack | `chown root:root /mnt/backup/restic` + `chmod 700` |
| **P1-2** | **agent.env empty key writes** | Always wrote both `export KEY=""` even when empty → overwrites user's existing var | Only writes non-empty keys via `printf` + conditional |
| **P1-3** | **Scheduler GPU health fail-closed** | Missing health entry = GPU passes (fail-open) → unhealthy GPU could be selected | Missing health = GPU rejected (fail-closed) |
| **P1-4** | **types.go comments** | Said `"gpu.0"` (stale from pre-v19.1.15) | Updated to `"gpu.nvidia.0"` |

### P2 fixes

| # | Fix |
|---|-----|
| **P2-1** | Stale headers: sglang compose, vllm compose, packages.x86_64 → v19.1.16 |
| **P2-2** | LiteLLM compose comments: removed "latest stable 1.21.7" references |
| **P2-3** | shellcheck CI: added scarlix-docker-backup.sh, removed scarlix-gpu (Go binary) |
| **P2-4** | Telemetry timers: scarlix-monitor.service + .timer (5 min record), scarlix-monitor-prune.service + .timer (daily prune 30d) |
| **P2-5** | VRAM defaults: mem_fraction 0.85→0.80, gpu_memory_utilization 0.85→0.80, shm_size 8gb→4gb (safe for 16GB) |
| **P2-6** | agent.env: added .zshrc + fish config.fish source (was: .bashrc only) |
| **P2-7** | Version headers → v19.1.16 (models.yaml, scarlix-mode, Pi-Bolt, Dockerfile, all timers) |

### Verification
```
gofmt -l .              → EMPTY (clean)
go vet ./...            → CLEAN
go test ./...           → 10 packages all OK (21 scheduler tests PASS)
bash -n                 → OK
YAML lint               → OK
scarlix-smoke-test.sh  → 13 passed, 0 failed, 0 warned
```

## 🆕 What's New in v19.1.17 (vs v19.1.16)

**Runtime correctness + security hardening.** 5 P1 + 6 P2 from 3 independent reviews.

Three reviews of v19.1.16 found runtime bugs (Go `time.ParseDuration` doesn't support `30d`, scheduler still allowed `unknown` GPU health), security issues (backup hook wrong mountpoint, `set -e` abort on empty agent.env key, `|| true` on security operations), and configuration drift (VRAM fallbacks, version headers, fish shell syntax).

### P1 fixes

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **`--prune 30d` never worked** | `time.ParseDuration("30d")` → error (Go doesn't support `d` unit) → prune timer always failed with exit 2 | New `ParseRetentionDuration()` supports `d` (days) + `w` (weeks) + standard Go units. 9 tests. |
| **P1-2** | **Scheduler allows `unknown` GPU health** | `gpuHealth.State != "healthy" && gpuHealth.State != "unknown"` → `unknown` passes (not proof of health) | Only `"healthy"` passes. `unknown`/`starting`/missing → rejected. |
| **P1-3** | **Backup hook checked wrong mountpoint** | Checked `dirname($BACKUP_REPO)` = `/mnt/backup/restic` — but docs say `/mnt/backup` is the mount | Checks `/mnt/backup` directly |
| **P1-4** | **agent.env `[ -n KEY ] && printf` aborts install** | Under `set -euo pipefail`, if last `[ -n ]` test fails → exit 1 → install aborts | Explicit `if/then` blocks — never triggers set -e |
| **P1-5** | **chown/chmod backup `|| true`** | Security operations silently ignored failures | `|| crit` — install aborts if backup perms can't be set |

### P2 fixes

| # | Fix |
|---|-----|
| **P2-1** | Telemetry timers: independent activation (was: nested inside host-bridge timer if-block) |
| **P2-2** | scarlix CLI header + model-manager.timer → v19.1.17 |
| **P2-3** | packages.x86_64 header → v19.1.17 |
| **P2-4** | VRAM fallback defaults aligned with models.yaml: 0.85→0.80, 8gb→4gb in scarlix-mode + compose |
| **P2-5** | Fish shell: separate `agent.env.fish` with `set -gx` (was: bash `export` → fish can't parse) |
| **P2-6** | Version headers → v19.1.17 (models.yaml, scarlix-mode, Dockerfile, Pi-Bolt, all timers) |

### Verification
```
gofmt -l .              → EMPTY (clean)
go vet ./...            → CLEAN
go test ./...           → 10 packages all OK (19 telemetry tests, 21 scheduler tests)
bash -n                 → OK
YAML lint               → OK
scarlix-smoke-test.sh  → 13 passed, 0 failed, 0 warned
```

## 🆕 What's New in v19.1.18 (vs v19.1.17)

**Audit closure + secret hardening.** 4 P1 + 5 P2 from 3 independent audits of v19.1.17.

Three audits of v19.1.17 found a shell injection vector in agent.env generation (secret values with quotes/metacharacters could break the generated file or inject shell code), a fish write path missing `mkdir -p`, edge cases in the retention duration parser (explicit signs, sub-nanosecond rounding), and version header drift (packages.x86_64, backup hook, compose files, scarlix-monitor usage text all still at older versions).

### P1 fixes

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **agent.env shell injection via secret values (A-07)** | `printf "export KEY='%s'\n" "$KEY"` — a key containing `'`, `$`, backtick, or `\` could break the generated file or inject shell code at `source` time | Keys encoded as base64, decoded at source time: `export KEY="$(printf '%s' '<b64>' | base64 -d)"`. Base64 is `[A-Za-z0-9+/=]` only → safe to inline. Applied to both bash `agent.env` and fish `agent.env.fish`. |
| **P1-2** | **Fish agent.env.fish missing mkdir -p (Zmor-3)** | `cat > ~/.config/scarlix/agent.env.fish` without `mkdir -p` — if dir didn't exist, cat failed silently but the `source` line was still added to `config.fish` | `mkdir -p "$d"` before cat + `PIPESTATUS` check — `source` line only added on successful write |
| **P1-3** | **ParseRetentionDuration accepts explicit signs (A-08)** | `+1d` / `-1d` — `time.ParseDuration` fails on `d`, then `numPart="+1"` → `ParseFloat` ok → accepted. `-1d` was caught by `num <= 0` but `+1d` passed through | Explicit `+`/`-` sign prefix rejected in the d/w branch |
| **P1-4** | **ParseRetentionDuration sub-nanosecond rounds to zero (A-09)** | `0.000000000000001d` (1 femtoday) → `num > 0` passes but `time.Duration()` truncates to 0ns → returned as valid 0-duration | Result checked after conversion: `if result <= 0 → error` |

### P2 fixes

| # | Fix |
|---|-----|
| **P2-1** | packages.x86_64 header: v19.0.0 → v19.1.18 (Zmor-1) |
| **P2-2** | scarlix-docker-backup.sh header: v19.0.0 → v19.1.18 (Zmor-2) |
| **P2-3** | compose headers (sglang + vllm): v19.1.16 → v19.1.18 |
| **P2-4** | scarlix-monitor usage text: v19.1.10 → v19.1.18 (Audit-3-6) |
| **P2-5** | .bashrc/.zshrc source comment: v19.1.16 → v19.1.18 (Zmor-4) |
| **P2-6** | .env.template: added VLLM_TRUST_FLAG documentation (Zmor-5) |
| **P2-7** | scarlix-smoke-test.sh: new check [10] — P0 regression guard for VLLM_TRUST_FLAG heredoc fix (Zmor-6). Verifies `local vllm_trust_flag=` declaration, case→assign ordering, and that the assignment is not inside a heredoc. |
| **P2-8** | Scheduler preferred runtime constraint documented (Zmor-7) — v19.1.x is fail-closed (no fallback if preferred is down). v19.2.0 Compute Fabric will add soft-hint fallback. |
| **P2-9** | Version headers → v19.1.18 across all files: install.sh, VERSION, Dockerfile, models.yaml, scarlix-mode, scarlix, AGENTS.md, pi-bolt config, all systemd units |

### New tests (telemetry duration parser)

| Test | Input | Expected |
|------|-------|----------|
| `TestParseRetentionDuration_NegativeDay` | `-1d` | error (explicit sign) |
| `TestParseRetentionDuration_NegativeWeek` | `-1w` | error (explicit sign) |
| `TestParseRetentionDuration_PlusDay` | `+1d` | error (explicit sign) |
| `TestParseRetentionDuration_PlusWeek` | `+1w` | error (explicit sign) |
| `TestParseRetentionDuration_SubNanoSecondResult` | `0.000000000000001d` | error (rounds to zero) |
| `TestParseRetentionDuration_VerySmallButValid` | `0.0000000001d` | OK (86ns > 0) |

### Verification
```
gofmt -l .              → EMPTY (clean)
go vet ./...            → CLEAN
go test ./...           → 10 packages all OK (25 telemetry tests, 21 scheduler tests)
bash -n                 → OK (13 scripts)
shellcheck -S warning  → OK (10 CI scripts)
YAML lint               → OK (26 files)
scarlix-smoke-test.sh  → 9 passed, 0 failed, 1 warned (offline: registry checks skipped)
base64 round-trip       → PASS (quotes, backticks, $, \, ; all safe)
```

## 🆕 What's New in v19.1.19 (vs v19.1.18)

**Audit closure + portability.** 2 P1 + 5 P2 from 2 independent reviews (external audit + Zmor).

Two reviews of v19.1.18 found: the smoke test check_10 regression guard was ineffective (grep-only, couldn't distinguish shell commands from heredoc text — the original P0 could return undetected), `base64 -w0` is GNU-only (fails on BusyBox/macOS), `scarlix-host-bridge.timer` had a `Requires=` anti-pattern, and 8+2 stale version headers remained at v19.0.0/v19.0.3.

### P1 fixes

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **Smoke test check_10 ineffective (Audit A-01 / Zmor-5)** | `grep` for `VLLM_TRUST_FLAG=$vllm_trust_flag` — couldn't distinguish a shell assignment from a line inside a heredoc. If the original P0 (if/case inside heredoc) returned, the test could still PASS. | **Heredoc-aware check**: finds the exact `cat > "$env_tmp" << EOF` ... `EOF` boundary, verifies `local vllm_trust_flag=` + `case` are BEFORE the heredoc, `VLLM_TRUST_FLAG=$vllm_trust_flag` is INSIDE the heredoc, and NO shell control-flow lines (`if`/`case`/`fi`/`esac`) leaked inside. Verified with positive + negative (injected if/fi) tests. |
| **P1-2** | **`base64 -w0` GNU-only (Zmor-3)** | `base64 -w0` (GNU coreutils flag) — fails on BusyBox (Alpine rescue), macOS BSD, some Arch minimal installs → install aborts at agent.env generation | `base64 | tr -d '\n'` — portable across GNU/BusyBox/BSD. Applied to all 4 base64 calls (bash LITELLM + SCARLIHQ, fish LITELLM + SCARLIHQ). |

### P2 fixes

| # | Fix |
|---|-----|
| **P2-1** | 8 stale version headers → v19.1.19 (Zmor-1): scarlix-doctor (v19.0.0), scarlix-host-bridge, scarlix-wizard, download-models.sh, model-manager.sh, generate-litellm-config.sh, generate-sha256sums.sh (all v19.0.3), model-manager.service Description (v19.0.3) |
| **P2-2** | `scarlix-host-bridge.timer`: removed `Requires=scarlix-host-bridge.service` (Zmor-2) — same anti-pattern as model-manager.timer (fixed in v19.1.15). Timer targets service by basename; Requires= pulls service at enable time, outside OnUnitActiveSec cycle. |
| **P2-3** | `scarlix-host-bridge.timer` Description + `scarlix-tv-mode.service` Description → v19.1.19 (Zmor-7) |
| **P2-4** | All current version headers → v19.1.19 (install.sh, VERSION, Dockerfile, README, all systemd units, all scripts, compose, models.yaml, AGENTS.md, pi-bolt, smoke test, scarlix-monitor usage) |
| **P2-5** | Scheduler preferred runtime constraint remains documented (Zmor-7 from v19.1.18) — v19.1.x fail-closed, v19.2.0 will add soft-hint fallback |

### Deferred (non-blocking, for v19.2+)

- **Zmor-4**: Pi-Bolt `curl|sh` without pin/hash — non-fatal warn path, supply-chain hardening for future
- **Zmor-6**: Dual-GPU hardcode (SGLang `device_ids: ['0']`, vLLM `["1"]`) — single-GPU host → vLLM experimental path fails. scarlix-mode should detect GPU count via `nvidia-smi -L` and adapt. Feature for v19.2.

### Verification
```
gofmt -l .              → EMPTY (clean)
go vet ./...            → CLEAN (10 packages)
go test ./...           → 10 packages all OK (25 telemetry + 21 scheduler tests)
bash -n                 → OK (13 scripts)
shellcheck -S warning  → OK (10 CI scripts)
YAML lint               → OK (26 files)
systemd-analyze verify  → OK (9 units)
scarlix-smoke-test.sh  → 9 passed, 0 failed, 1 warned (offline)
base64 round-trip       → PASS (portable base64 | tr -d '\n')
check_10 negative test  → PASS (injected if/fi in heredoc → FAIL detected)
```

## 🆕 What's New in v19.1.20 (Jubilee) (vs v19.1.19)

**Mutation-hardened smoke test guard.** 1 P1 + 3 P2 from 3 independent audits.

Three audits of v19.1.19 confirmed all previous fixes were correctly applied. One audit performed mutation testing on the new `check_10` heredoc guard and found it only caught **1 of 4** injection types: the deny-list (`^(if|case|fi|esac)` at col-0) missed indented `case`, `while` loops, and plain commands like `rm -rf /`. Two stale version headers were also found (`generate-env.sh` v19.0.1, `scarlix-docker-backup.hook` v19.0.0).

### P1 fix

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **check_10 mutation-hardened (audit #3 mutation test)** | Deny-list: only col-0 `if/case/fi/esac` rejected. Mutation testing showed 3/4 injections PASSED: (1) indented `case ... in` → PASS, (2) `while true; do` → PASS, (3) `rm -rf /tmp/x` → PASS. Only col-0 `if` was caught. | **Allow-list**: every non-blank, non-comment line inside the `.env` heredoc must match `^[A-Z][A-Z0-9_]*=`. Catches all 4/4 injection types. Baseline (correct scarlix-mode) still PASSes. |

### P2 fixes

| # | Fix |
|---|-----|
| **P2-1** | `files/etc/systemd/system/generate-env.sh` header: v19.0.1 → v19.1.20 |
| **P2-2** | `files/etc/pacman.d/hooks/scarlix-docker-backup.hook` header: v19.0.0 → v19.1.20 |
| **P2-3** | All version headers → v19.1.20 (jubilee) |

### Mutation test results (check_10)

| Injection | v19.1.19 (deny-list) | v19.1.20 (allow-list) |
|-----------|----------------------|----------------------|
| `if [ ... ]; then ... fi` (col-0) | FAIL ✓ | FAIL ✓ |
| `  case ... in ... esac` (indented) | PASS ✗ (missed) | FAIL ✓ |
| `  while true; do ... done` | PASS ✗ (missed) | FAIL ✓ |
| `  rm -rf /tmp/x` (plain command) | PASS ✗ (missed) | FAIL ✓ |
| Baseline (correct code) | PASS ✓ | PASS ✓ |

### Confirmed OK (no action needed)

- base64 portable `| tr -d '\n'` — round-trip tested with full special character set in bash + fish 3.7
- `scarlix-host-bridge.timer` — `Requires=` removed, `Unit=` targets service by basename
- agent.env base64 encoding — injection vector closed
- All Go tests (171 total) pass

### Verification
```
gofmt -l .              → EMPTY (clean)
go vet ./...            → CLEAN (10 packages)
go test ./...           → 10 packages all OK (25 telemetry + 21 scheduler tests)
bash -n                 → OK (13 scripts)
shellcheck -S warning  → OK (10 CI scripts)
YAML lint               → OK (26 files)
systemd-analyze verify  → OK (9 units)
scarlix-smoke-test.sh  → 9 passed, 0 failed, 1 warned (offline)
check_10 mutation test  → 4/4 injections FAIL, baseline PASS
```

---

## 🆕 What's New in v19.2.0 (Compute Fabric Kickoff) (vs v19.1.20)

**v19.1.x generation declared FROZEN.** This release closes the final audit findings from v19.1.20 and marks the transition to the **Compute Fabric** generation — real scheduler with allocation, lease, and reservation semantics.

### v19.1.x FROZEN declaration

The entire v19.1.x generation (v19.1.0 → v19.1.20) is now declared **FROZEN**:
- **Resource Contract v1** (schema, validation, registries) — stable, no breaking changes
- **Scheduler dry-run** (deterministic scoring, no allocation) — stable baseline
- **ScarliMonitor + Telemetry** — stable, read-only
- **GPU Compatibility Matrix** — stable
- **All 10 Go packages** — API surface frozen for additive extensions only

### P1 fix — Command substitution bypass closed (3 audits of v19.1.20)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **check_10 allowed `$(...)` and backticks in heredoc values** | The v19.1.20 allow-list (`^[A-Z][A-Z0-9_]*=`) only checked the line *prefix*. A line like `PWN=$(touch /tmp/pwned)` matched the pattern, but because the `.env` heredoc uses an unquoted delimiter (`<< EOF`), bash would **execute** the `$(...)` at generation time. Mutation testing confirmed **5/5 bypasses PASSED**: `$(cmd)`, `` `cmd` ``, `a; cmd`, `${x:-$(cmd)}`, and `# $(cmd)` in comments. | **Double-layer guard** (from auditor's patch, commit `724be91`): (1) No line anywhere in the heredoc may contain `$(` or a backtick. (2) Non-blank, non-comment lines must match a strict VALUE regex allowing only `[A-Za-z0-9_./:-]` plus `$var` / `${var}` expansions. Mutation test: **9/9 injections FAIL, baseline PASS, 0 false positives**. |

### P2 fix — Dynamic version in generated LiteLLM config (2A)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P2-1** | **generate-litellm-config.sh hardcoded v19.0.3 in generated config header** | Line 82 of the script wrote `# SCARLIX OS v19.0.3 — LiteLLM routing config` into every generated `config.yaml`. The script header was bumped each release, but the *generated output* stayed at v19.0.3 — a diagnostic drift across 17 releases. | **Dynamic version** read from `VERSION` file (search order: repo root → `/etc/scarlix/VERSION` → `/usr/local/share/scarlix/VERSION` → fallback `"unknown"`). No future drift — generated config always reflects the installed version. |

### P3 — Systemd unit executable bits left as-is (3B)

5 systemd unit files (`model-manager.service`, `model-manager.timer`, `scarlix-host-bridge.service`, `scarlix-host-bridge.timer`, `scarlix-tv-mode.service`) retain mode `0755` in the repository. **Runtime is unaffected** — `install.sh:857` explicitly sets `chmod 644` on `.service`/`.timer` files at install time. Decision: leave as-is (3B) to avoid a large mode-only diff; the install-time correction is sufficient.

### Info — scarlix-contract v19.1.0 (no action)

The `v19.1.0` references in `scarlihq/cmd/scarlix-contract/main.go` and `scarlihq/internal/contract/types.go` refer to the **Resource Contract v1 schema version**, which is intentionally FROZEN. These are not version-drift — they document when the contract schema was frozen. **Not changed.**

### Mutation test results (check_10, v19.2.0)

| Injection | v19.1.20 (allow-list) | v19.2.0 (double-layer) |
|-----------|----------------------|----------------------|
| `KEY=$(cmd)` | PASS ✗ (bypassed) | FAIL ✓ (detected) |
| `` KEY=`cmd` `` | PASS ✗ (bypassed) | FAIL ✓ (detected) |
| `KEY=a; cmd` | PASS ✗ (bypassed) | FAIL ✓ (detected) |
| `KEY=${x:-$(cmd)}` | PASS ✗ (bypassed) | FAIL ✓ (detected) |
| `# $(cmd) comment` | PASS ✗ (bypassed) | FAIL ✓ (detected) |
| `if ... fi` (col-0) | FAIL ✓ | FAIL ✓ |
| `  case ... esac` (indented) | FAIL ✓ | FAIL ✓ |
| `  while ... done` | FAIL ✓ | FAIL ✓ |
| `  rm -rf /tmp/x` | FAIL ✓ | FAIL ✓ |
| Baseline (correct code) | PASS ✓ | PASS ✓ |

### Roadmap — Compute Fabric (v19.2.x)

With v19.1.x frozen, development now focuses on:
- **v19.2.1**: Real scheduler allocation (GPU IDs + ports granted in response)
- **v19.2.2**: Lease + reservation semantics (time-bounded GPU holds)
- **v19.2.3**: Preferred runtime soft-hint fallback (Zmor-7 from v19.1.18)
- **v19.2.4**: Single-GPU detection (Zmor-6 — `nvidia-smi -L` count, adapt vLLM path)
- **v19.2.5**: Pi-Bolt supply-chain pin (Zmor-4 — SHA256 verify before `sh`)
- **v19.2.6**: Model integrity (SHA256/revision), image digest pinning

### Verification
```
gofmt -l .              → EMPTY (clean)
go vet ./...            → CLEAN (10 packages)
go test ./...           → 10 packages all OK (25 telemetry + 21 scheduler tests)
bash -n                 → OK (13 scripts)
shellcheck -S warning  → OK (12 scripts)
YAML lint               → OK (26 files)
systemd-analyze verify  → OK (9 units)
scarlix-smoke-test.sh  → 9 passed, 0 failed, 1 warned (offline)
check_10 mutation test  → 9/9 injections FAIL, baseline PASS, 0 false positives
litellm dynamic version → PASS (reads VERSION file, falls back to "unknown")
```

---

## 🆕 What's New in v19.2.0 (Compute Fabric Kickoff) (vs v19.1.20)

**v19.1.x generation declared FROZEN.** This release closes the final audit findings from v19.1.20 and marks the transition to the **Compute Fabric** generation — real scheduler with allocation, lease, and reservation semantics.

### v19.1.x FROZEN declaration

The entire v19.1.x generation (v19.1.0 → v19.1.20) is now declared **FROZEN**:
- **Resource Contract v1** (schema, validation, registries) — stable, no breaking changes
- **Scheduler dry-run** (deterministic scoring, no allocation) — stable baseline
- **ScarliMonitor + Telemetry** — stable, read-only
- **GPU Compatibility Matrix** — stable
- **All 10 Go packages** — API surface frozen for additive extensions only

### P1 fix — Command substitution bypass closed (3 audits of v19.1.20)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P1-1** | **check_10 allowed `$(...)` and backticks in heredoc values** | The v19.1.20 allow-list (`^[A-Z][A-Z0-9_]*=`) only checked the line *prefix*. A line like `PWN=$(touch /tmp/pwned)` matched the pattern, but because the `.env` heredoc uses an unquoted delimiter (`<< EOF`), bash would **execute** the `$(...)` at generation time. Mutation testing confirmed **5/5 bypasses PASSED**: `$(cmd)`, `` `cmd` ``, `a; cmd`, `${x:-$(cmd)}`, and `# $(cmd)` in comments. | **Double-layer guard** (from auditor's patch, commit `724be91`): (1) No line anywhere in the heredoc may contain `$(` or a backtick. (2) Non-blank, non-comment lines must match a strict VALUE regex allowing only `[A-Za-z0-9_./:-]` plus `$var` / `${var}` expansions. Mutation test: **9/9 injections FAIL, baseline PASS, 0 false positives**. |

### P2 fix — Dynamic version in generated LiteLLM config (2A)

| # | Fix | Was | Now |
|---|-----|-----|-----|
| **P2-1** | **generate-litellm-config.sh hardcoded v19.0.3 in generated config header** | Line 82 of the script wrote `# SCARLIX OS v19.0.3 — LiteLLM routing config` into every generated `config.yaml`. The script header was bumped each release, but the *generated output* stayed at v19.0.3 — a diagnostic drift across 17 releases. | **Dynamic version** read from `VERSION` file (search order: repo root → `/etc/scarlix/VERSION` → `/usr/local/share/scarlix/VERSION` → fallback `"unknown"`). No future drift — generated config always reflects the installed version. |

### P3 — Systemd unit executable bits left as-is (3B)

5 systemd unit files retain mode `0755` in the repository. **Runtime is unaffected** — `install.sh:857` explicitly sets `chmod 644` on `.service`/`.timer` files at install time. Decision: leave as-is (3B) to avoid a large mode-only diff.

### Info — scarlix-contract v19.1.0 (no action)

The `v19.1.0` references in `scarlihq/cmd/scarlix-contract/main.go` and `scarlihq/internal/contract/types.go` refer to the **Resource Contract v1 schema version**, intentionally FROZEN. Not version-drift. **Not changed.**

### Mutation test results (check_10, v19.2.0)

| Injection | v19.1.20 (allow-list) | v19.2.0 (double-layer) |
|-----------|----------------------|----------------------|
| `KEY=$(cmd)` | PASS ✗ (bypassed) | FAIL ✓ (detected) |
| `` KEY=`cmd` `` | PASS ✗ (bypassed) | FAIL ✓ (detected) |
| `KEY=a; cmd` | PASS ✗ (bypassed) | FAIL ✓ (detected) |
| `KEY=${x:-$(cmd)}` | PASS ✗ (bypassed) | FAIL ✓ (detected) |
| `# $(cmd) comment` | PASS ✗ (bypassed) | FAIL ✓ (detected) |
| `if ... fi` (col-0) | FAIL ✓ | FAIL ✓ |
| `  case ... esac` (indented) | FAIL ✓ | FAIL ✓ |
| `  while ... done` | FAIL ✓ | FAIL ✓ |
| `  rm -rf /tmp/x` | FAIL ✓ | FAIL ✓ |
| Baseline (correct code) | PASS ✓ | PASS ✓ |

### Roadmap — Compute Fabric (v19.2.x)

With v19.1.x frozen, development now focuses on:
- **v19.2.1**: Real scheduler allocation (GPU IDs + ports granted in response)
- **v19.2.2**: Lease + reservation semantics (time-bounded GPU holds)
- **v19.2.3**: Preferred runtime soft-hint fallback (Zmor-7 from v19.1.18)
- **v19.2.4**: Single-GPU detection (Zmor-6 — `nvidia-smi -L` count, adapt vLLM path)
- **v19.2.5**: Pi-Bolt supply-chain pin (Zmor-4 — SHA256 verify before `sh`)
- **v19.2.6**: Model integrity (SHA256/revision), image digest pinning

### Verification
```
gofmt -l .              → EMPTY (clean)
go vet ./...            → CLEAN (10 packages)
go test ./...           → 10 packages all OK (25 telemetry + 21 scheduler tests)
bash -n                 → OK (13 scripts)
shellcheck -S warning  → OK (12 scripts)
YAML lint               → OK (26 files)
systemd-analyze verify  → OK (9 units)
scarlix-smoke-test.sh  → 9 passed, 0 failed, 1 warned (offline)
check_10 mutation test  → 9/9 injections FAIL, baseline PASS, 0 false positives
litellm dynamic version → PASS (reads VERSION file, falls back to "unknown")
```
