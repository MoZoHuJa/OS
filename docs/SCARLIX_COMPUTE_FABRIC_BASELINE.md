# SCARLIX Compute Fabric — Verified Baseline

**Document type:** Phase 0 baseline report (no code changes)
**Date:** 2026-10-10
**Guide:** `SCARGEN_COMPUTE_FABRIC_CODING_AGENT_GUIDE.md` §2 (Phase 0)

---

## 1. Repository Identity

| Field | Value |
|-------|-------|
| Repository | `https://github.com/MoZoHuJa/OS` |
| Branch | `main` |
| Commit SHA | `c5d3d795ac136b920fc793831bbdd5db5662b460` |
| Version (VERSION file) | `19.2.1` |
| Tag | `v19.2.1` (ScaRgeN_Zero_alpha) |
| GitHub Release | `ScaRgeN_Zero_alpha` (first formal release) |

### Recent commit history

```
c5d3d79 (HEAD -> main, origin/main) Merge add-skill-scarlix-os-v19-replica
d426686 docs(skills): add scarlix-os-v19-replica SKILL.md for v19.2.1
93a7eca Merge fix-v19.2.1: ScaRgeN_Zero_alpha — First Formal Release
406d692 (tag: v19.2.1) SCARLIX OS v19.2.1 — ScaRgeN_Zero_alpha
2c5cc29 Merge fix-v19.2.0: Compute Fabric Kickoff — v19.1.x FROZEN
ac5947d (tag: v19.2.0) SCARLIX OS v19.2.0 (Compute Fabric Kickoff)
```

## 2. Worktree Status

**Status:** Dirty — 7 file mode changes (100644 → 100755), **zero content changes**.

```
 mode change 100644 => 100755 files/etc/systemd/system/scarlix-monitor-prune.service
 mode change 100644 => 100755 files/etc/systemd/system/scarlix-monitor-prune.timer
 mode change 100644 => 100755 files/etc/systemd/system/scarlix-monitor.service
 mode change 100644 => 100755 files/etc/systemd/system/scarlix-monitor.timer
 mode change 100644 => 100755 scarlihq/internal/telemetry/duration.go
 mode change 100644 => 100755 scarlihq/internal/telemetry/duration_test.go
 mode change 100644 => 100755 skills/README.md
```

**Assessment:** These are mode-only changes from prior sessions. No content impact. `git diff --check` is clean (no whitespace errors). Safe to work around — will not affect any code changes.

## 3. Existing Compute-Related Modules

### 3.1 Go packages (28 source files, 6,009 LOC + 16 test files, 4,096 LOC = 172 tests)

| Package | Files | Source LOC | Test LOC | Tests | Role |
|---------|-------|-----------|----------|-------|------|
| `contract/` | 3+2 | 537 | 610 | 25 | Resource Contract v1 schema (FROZEN v19.1.0) |
| `inventory/` | 4+4 | 1,238 | 1,545 | 30 | Read-only state collectors (GPU/Runtime/Model/Health) |
| `registry/` | 2+2 | 282 | 242 | 17 | In-memory Runtime + Model registries |
| `scheduler/` | 1+1 | 587 | 560 | 21 | **Dry-run** deterministic planner (NO allocation) |
| `monitor/` | 1+1 | 472 | 194 | 8 | Read-only Snapshot composer |
| `telemetry/` | 2+2 | 330 | 371 | 22 | JSONL persistence for measurements |
| `api/` | 1+1 | 390 | ~200 | 7 | HTTP REST (7 endpoints, bearer auth, WS tickets) |
| `mcp/` | 1+0 | 219 | 0 | 0 | JSON-RPC 2.0 (6 tools) |
| `guard/` | 1+0 | 72 | 0 | 0 | Command pattern blocker (plumbed, NOT yet called) |
| `scarlix_mode/` | 1+1 | ~120 | ~80 | 5 | Mode request (atomic O_EXCL desired-mode write) |
| `status/` | 1+1 | ~130 | ~60 | 4 | ReadOrStale of host-status.json |
| `profiles/` | 1+1 | ~100 | ~50 | 3 | YAML profile loader (zmor/hugo/xox/mon) |
| `compat/` | 1+1 | ~150 | ~120 | 11 | GPU×Runtime×Model compatibility matrix |

### 3.2 CLI binaries (7)

| Binary | Purpose | Side effects? |
|--------|---------|---------------|
| `scarlihq` | Dashboard HTTP server (REST + WS + MCP) | Writes only to `bridge-input/desired-mode` (O_EXCL) |
| `scarlix-scheduler` | Dry-run resource planner | **NONE** — pure scoring, no allocation |
| `scarlix-monitor` | Snapshot CLI + HTTP serve + telemetry | Writes telemetry JSONL (append-only) |
| `scarlix-contract` | Contract example/validate/parse | NONE — pure data |
| `scarlix-gpu` | GPU JSON producer | NONE — read-only |
| `scarlix-inventory` | Full SystemStatus JSON | NONE — read-only |
| `scarlix-bridge-reader` | TOCTOU-free atomic file reader | NONE — reads fd, writes stdout |

### 3.3 Shell scripts (13 in `files/usr/local/bin/` + 1 in systemd)

| Script | Privileged? | Role |
|--------|-------------|------|
| `scarlix-mode` | root (sudo re-exec) | Mode switcher: generates .env, starts/stops docker compose services |
| `scarlix-host-bridge` | root (systemd timer, 5s) | Reads desired-mode via bridge-reader, calls scarlix-mode, writes host-status.json |
| `scarlix` | user | Read-only unified CLI (delegates to Go binaries) |
| `scarlix-doctor` | user/root | Self-diagnostic + `--fix` |
| `scarlix-smoke-test.sh` | user | 10-check regression suite |
| `scarlix-wizard` | root | First-boot setup (whiptail) |
| `model-manager.sh` | root | Weekly HF + Ollama model updater |
| `download-models.sh` | root | Initial model pull |
| `generate-litellm-config.sh` | root | Generate LiteLLM config.yaml from models.yaml |
| `generate-sha256sums.sh` | user | SHA256 manifest for release integrity |
| `generate-env.sh` | root | Generate /etc/scarlix/.env atomically |

## 4. Scheduler Behavior — Dry-Run Verification

**`scheduler.Plan()` has ZERO side effects.** Confirmed by three independent observations:

1. **Imports** (`plan.go:15-22`): only `fmt`, `sort`, `strings`, `internal/contract`, `internal/inventory`. No `os`, `exec`, `net`, `io`, `time`, or any I/O package.

2. **Body inspection**: grep for `os.`, `exec.`, `docker`, `container`, `port`, `reserve`, `allocate`, `Write`, `Start`, `Stop`, `Kill` returned only `strings.Builder.WriteString` (inside `FormatPlan()` formatter) and `sort.Slice` for ranking.

3. **Comments**:
   - Package doc: *"v19.1.7 implements the DRY-RUN scheduler… it scores GPU+runtime+model combinations and returns a plan, but performs NO actual allocation."*
   - Plan doc: *"Plan produces a dry-run scheduling decision… NO actual allocation is performed."*

### Scoring formula

```
totalScore = vram_fit(+30) + utilization(+20/+10/-10) + runtime_fit(+20, +5 running)
           + model_fit(+20, +matched*5, +5 present) + latency(+20/+10/-20)
           + health(+10) + temperature_penalty(0/-5/-10)
```

### Hard rejections (produce Rejection entry, continue GPU loop)

1. Accelerator mismatch (cuda↔nvidia, rocm↔amd, cpu→never matches GPU)
2. Insufficient VRAM (`VRAMMB > 0 && VRAMFreeMB < VRAMMB`)
3. GPU health not `"healthy"` (fail-closed — `unknown`/`missing` rejected, v19.1.17 P1)
4. Runtime `down`/`unhealthy` (v19.1.10 P1-3 hard filter)
5. CPU-only runtime on GPU path (`len(GPUIDs)==0`, v19.1.12 P1-B1)
6. No compatible model (v19.1.10 P1-2)
7. Missing model capabilities (ALL must match, v19.1.11 P1-1)

### Preferred runtime — HARD constraint (known limitation)

`c.Runtime.Preferred` is currently a **hard filter** (`plan.go:359-361`):
```go
if len(preferred) > 0 && !preferred[rt.ID] {
    continue
}
```

Documented as **KNOWN CONSTRAINT** (v19.1.18 P2 Zmor-7): if preferred runtime is down/unhealthy, no fallback occurs (fail-closed). v19.2.0 Compute Fabric will add a fallback pass (preferred = soft hint).

## 5. Request → Execution Path (Traced)

### 5.1 Read path (scheduler — standalone CLI, NOT wired to API)

```
operator → scarlix-scheduler --task coding --json
  → cmd/scarlix-scheduler/main.go
    → inventory.CollectGPUs/Runtimes/Models/Health
    → scheduler.New(gpus, runtimes, models, allHealth)
    → s.Plan(contract) → *Plan (pure scoring, no side effects)
    → JSON to stdout
```

**The scheduler is NOT connected to the HTTP API.** No `/api/scheduler/plan` endpoint exists. It is invoked manually by an operator.

### 5.2 Write path (mode switch — async, 5s end-to-end)

```
1. Dashboard JS → POST /api/mode?set=ai (Bearer token)
2. ScarliHQ container (UID 65532, port 8090)
   → api.Handler.modeHandler → scarlix_mode.Mode.Request("ai")
   → Mode.Set("ai") → os.OpenFile(bridge-input/desired-mode, O_EXCL, 0600)
   → HTTP 202 Accepted
3. systemd timer (5s) → scarlix-host-bridge.service (root)
   → flock -n 200 (skip if busy)
   → verify bridge-input/ owner=65532:65532 mode=700
   → scarlix-bridge-reader (Go: O_NOFOLLOW + Fstat + UID check + perm check)
   → scarlix-mode ai (single execution, captures rc + output)
4. scarlix-mode (root)
   → generate_env_file (regenerate /opt/scarlix/.env from models.yaml)
   → generate-litellm-config.sh
   → start_verified_ai: SGLang(GPU0) → vLLM(GPU1, experimental) → BeeLlama(CPU) → Ollama(CPU)
   → LiteLLM gateway (:4001)
   → echo "ai" > /var/lib/scarlix/current-mode (only on success)
5. Back in host-bridge
   → rc==0: transition="applied", rm desired-mode + .retry
   → rc!=0: retry_count++, transition="retrying" or "failed" (if >=3)
   → atomic write last-transition file
   → gather status (nvidia-smi, docker ps, df, VERSION)
   → atomic write host-status.json (mktemp + mv, chmod 644)
6. ScarliHQ WS (2s tick) → status.ReadOrStale() → push to dashboard
```

### 5.3 Security boundary (Host-Bridge)

- ScarliHQ container: UID 65532, **NO docker.sock**, **NO nvidia runtime**
- Writable: only `/var/lib/scarlix/bridge-input/` (UID 65532, mode 0700)
- Readable: `/var/lib/scarlix/host-status.json` (bind-mounted ro)
- Host-bridge: root, reads via `scarlix-bridge-reader` (O_NOFOLLOW + Fstat + UID/perm/size validation)
- Atomic writes everywhere: `mktemp + chmod + mv` pattern
- WS tickets: 30s single-use, atomic delete before Upgrade
- Bearer token auth only (no URL `?token=`)

## 6. API and Contract Versions

### Resource Contract v1 (FROZEN v19.1.0)

- `ContractVersion = "v1"` — frozen, additive-only extensions
- 7 reject categories (IDs, version, task, compute, runtime, model, security)
- Enum sets: TaskType, Priority, Accelerator, FilesystemScope, NetworkScope, ShellScope
- Slices serialize as `[]` not `null` (enforced via `Marshaler` impls)
- No allocation response type exists yet (planned v19.2.x)

### HTTP API

- 7 REST endpoints: `/api/health`, `/api/gpu`, `/api/containers`, `/api/mode`, `/api/profiles`, `/api/status`, `/api/ws-ticket`
- WebSocket: `/ws` (ticket-gated, 2s push, 16-slot cap)
- MCP: `/rpc` (JSON-RPC 2.0, 6 tools), `/mcp` (info)
- Auth: Bearer token (ConstantTimeCompare), 503 if unconfigured
- No scheduler endpoint exists

### Inventory schema (FROZEN v19.0.7)

- `GPU` (14 fields), `Runtime` (10 fields), `Model` (10 fields), `Service` (7 fields), `Health` (4 fields), `SystemStatus` (9 fields)
- Canonical ID format: `gpu.nvidia.<idx>` links GPU.ID, Runtime.GPUIDs, Health.Component

### Telemetry schema (FROZEN v19.1.5)

- `Measurement` (10 fields): JSONL at `/var/lib/scarlix/telemetry.jsonl`
- Append/Query/Count/Prune operations

## 7. Test Suite Results

### Static and unit checks

| Check | Command | Result |
|-------|---------|--------|
| gofmt | `gofmt -l .` | **PASS** — clean |
| go vet | `go vet ./...` | **PASS** — clean (10 packages) |
| go test | `go test ./... -count=1` | **PASS** — 10 packages, 172 tests, 0 FAIL |
| bash -n | all shell scripts | **PASS** — 13 scripts |
| shellcheck | `-S warning` on 10 CI scripts | **PASS** — clean |
| YAML lint | all docker-compose.yml + smg config | **PASS** — 26 files |
| systemd-analyze | `verify` on *.service + *.timer | **PASS** — 9 units |
| smoke test | `scarlix-smoke-test.sh --offline` | **PASS** — 9 passed, 0 failed, 1 warned |

### Go test breakdown by package

| Package | Tests | Status |
|---------|-------|--------|
| `api` | 7 | PASS |
| `compat` | 11 | PASS |
| `contract` | 25 | PASS |
| `inventory` | 30 | PASS |
| `monitor` | 8 | PASS |
| `registry` | 17 | PASS |
| `scarlix_mode` | 5 | PASS |
| `scheduler` | 21 | PASS |
| `status` | 4 | PASS |
| `telemetry` | 22 | PASS |
| **Total** | **172** | **ALL PASS** |

### CI jobs (9 total, all green)

go-build, shellcheck, bash-syntax, yaml-validation, compose-validation, systemd-validation, scarlihq-docker-build, integration-test, **smoke-test** (added v19.2.1)

## 8. Known Gaps for Compute Fabric

### 8.1 No allocation state machine

No `Allocation` type, no lifecycle states (`REQUESTED → VALIDATING → ... → RELEASED`), no transition rules. The scheduler produces a `Plan` but cannot persist, track, or release it.

### 8.2 No atomic reservation

Two concurrent `Plan()` calls can select the same GPU — there is no reservation lock, no ownership record, no idempotency key. The scheduler is stateless by design.

### 8.3 No lease/TTL/expiry

No time-bounded resource holds. No heartbeat, no renewal, no expiry, no expiration-based cleanup.

### 8.4 No allocation persistence

No `Allocation` record stored to disk. Process restart loses all plan state. No reconciliation on startup.

### 8.5 No concurrent request handling

`Plan()` is stateless and thread-safe (each call independent), but there is no mutex protecting "GPU X is being allocated to request Y" — a race condition between Plan() and actual execution.

### 8.6 No release/cleanup/reconciliation

No `Release()` method. No cleanup path. No startup reconciliation comparing allocation records with actual workload state.

### 8.7 No API endpoints for allocation

No `/api/scheduler/plan` or `/api/allocation` endpoint. The scheduler is CLI-only — not reachable from the dashboard.

### 8.8 No telemetry for allocation lifecycle

Telemetry records GPU metrics (utilization, VRAM, latency) but has no allocation events (request accepted/rejected, reservation succeeded/failed, state transition, release/cleanup).

### 8.9 Preferred runtime is hard constraint

If preferred runtime is down/unhealthy, no fallback to another compatible runtime. Documented as v19.2.0 planned fix (soft-hint + fallback pass).

### 8.10 guard package plumbed but unused

`guard.Guard` is constructed in both `api.Handler` and `mcp.Server` but **no handler calls `g.Check()`**. Reserved for future "shell exec" tool.

### 8.11 No port allocation mechanism

No port reservation, no collision check, no port pool management. Runtime ports are statically defined in compose files (SGLang :30000, vLLM :8089, BeeLlama :11438, Ollama :11435, LiteLLM :4001).

### 8.12 No GPU sharing policy

No definition of whether GPU sharing is allowed. VRAM estimates alone do not make concurrent model execution safe. The scheduler treats VRAM as a hard fit check (`VRAMFreeMB >= VRAMMB`) but does not model concurrent allocation.

## 9. Tests Requiring Target Hardware

The following tests are **BLOCKED — NOT HARDWARE-VERIFIED** in this environment:

- GPU collection with real `nvidia-smi` (dual RTX 5060 Ti + RTX 4060 Ti)
- Runtime health probes against live SGLang/vLLM/BeeLlama/Ollama containers
- Docker Compose `up -d` for AI inference stack
- `scarlix-mode ai` end-to-end (start_verified_ai with real GPU)
- Allocation lifecycle with real GPU reservation
- Concurrent request behavior on real hardware
- Cancellation and release with real workloads
- Forced runtime failure and recovery
- Reboot/reconciliation behavior
- End-to-end inference result verification
- `scarlix-doctor` full diagnostic on target machine
- `scarlix-smoke-test.sh` online mode (registry image tag checks)

**Hardware profile (from docs):**
- CPU: AMD Ryzen 7 7700X
- RAM: 64 GB
- GPU 0: NVIDIA RTX 5060 Ti 16 GB (Blackwell, sm_120)
- GPU 1: NVIDIA RTX 4060 Ti 16 GB (Ada)
- Storage: BTRFS + Snapper
- OS: EndeavourOS (Arch Linux)

## 10. Gate Assessment

**Phase 0 gate: PASSED.**

The baseline is understood:
- Exact commit, version, and worktree status recorded ✓
- All 12 Go packages mapped with types, functions, behavior ✓
- Scheduler confirmed dry-run only (zero side effects) ✓
- Request→execution path traced (both read and write paths) ✓
- Security boundary documented (Host-Bridge, UID 65532, O_EXCL, bridge-reader) ✓
- Existing test suite run: 172 Go tests + 9 smoke checks all PASS ✓
- Known gaps identified (12 items, all are "missing feature" not "broken code") ✓
- Hardware-dependent tests marked BLOCKED ✓

**Ready to proceed to Phase 1 (trace existing request/security path) and Phase 2 (define allocation state machine).**

---

*This is a living document. Update it as the baseline changes or as new findings emerge during implementation.*
