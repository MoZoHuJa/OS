<p align="center">
  <img src="docs/scarlixos-banner.png" alt="ScarLiXoS v18.8.2 — Sovereign AI Cloud" width="100%" />
</p>

<h1 align="center">SCARLIX OS v18.8.2</h1>

<p align="center">
  <strong>Suverénny domáci OS pre AI cloud, coding, gaming a rodinnú zábavu.</strong><br/>
  Architektúra Secure Host-Bridge · Token autentifikácia · State machine · Privilege boundary
</p>

<p align="center">
  <a href="https://github.com/MoZoHuJa/OS/releases/tag/v18.8.2"><img alt="Version" src="https://img.shields.io/badge/version-v18.8.2-06b6d4?style=flat-square" /></a>
  <a href="https://github.com/MoZoHuJa/OS/blob/main/LICENSE"><img alt="License" src="https://img.shields.io/badge/license-MIT-14b8a6?style=flat-square" /></a>
  <a href="https://github.com/MoZoHuJa/OS"><img alt="Base" src="https://img.shields.io/badge/base-EndeavourOS%20%28Arch%29-10b981?style=flat-square" /></a>
  <a href="https://github.com/MoZoHuJa/OS/actions"><img alt="CI" src="https://img.shields.io/badge/CI-GitHub%20Actions-22d3ee?style=flat-square" /></a>
</p>

---

> **Working AI Path**: model-aware, fail-hard, healthcheck + fallback.
> **Verified**: SGLang (GPU0, --disable-flashinfer) + vLLM (GPU1, TP=1, experimental) + BeeLlama (CPU) + Ollama (CPU tertiary fallback).
> **LiteLLM Gateway** (v18.8.2+): unified OpenAI-compatible API on :4001. Uses a **simplified 3-tier fallback** (SGLang → Ollama → BeeLlama) for external clients — vLLM excluded because it's experimental (.experimental only). scarlix-mode's direct AI path keeps the full 4-tier including vLLM.

**Version:** v18.8.2 | **Base:** EndeavourOS (Arch) | **License:** MIT

## 🚀 Install (NO ISO)

### Primary (safe — review first) ⭐
```bash
git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
cd ~/scarlix-os
git checkout v18.8.2   # ALWAYS checkout specific tag (main may be ahead)
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

## 🆕 What's New in v18.8.2 (vs v18.7.8)

**CI fail fix + LiteLLM routing + image pinning.** 12 fixes from 3 expert reviews.

v18.7.8 had a **CI that always failed**: shellcheck SC2168 (`local` outside function), WS test used wrong exception class (websockets 13.1 API), `curl || echo 000` → 000000 bug, and missing `setup-python` (PEP 668). Also LiteLLM fallback pointed to non-existent hostname `llamacpp` (should be `beellama`).

### P0 — CI fail + routing (4)
| # | Fix | v18.7.8 Problem | v18.8.2 Solution |
|---|-----|-----------------|-------------------|
| P0 | **shellcheck SC2168** | `local comfyui_status` in top-level case branch (not function) → CI failed | Plain assignment without `local` |
| P0 | **WS test exception class** | Expected `InvalidStatus` but websockets==13.1 raises `InvalidStatusCode` (different attribute) → server returned 401 correctly but test failed | Version-compatible `getattr()` extracts status_code from either API |
| P0 | **curl `|| echo 000` → 000000** | curl fail prints 000 AND exits 1 → `|| echo 000` adds another → code=000000 → false healthy | Separate assignment + fallback `|| code="000"` (6 sites) |
| P0 | **LiteLLM routing** | `api_base: http://llamacpp:8080` — service doesn't exist (container is `beellama`) → CPU fallback always failed with DNS error. Also 3 different model names | `beellama:8080` + unified `scarlix-default` logical model |

### P1 — reliability (8)
| # | Fix | v18.7.8 Problem | v18.8.2 Solution |
|---|-----|-----------------|-------------------|
| P1 | **setup-python before pip** | bare `pip install` on ubuntu 24.04 → PEP 668 "externally-managed" → job failed | `actions/setup-python@v5` with python 3.12 |
| P1 | **Creative start health wait** | `up -d` exit 0 → immediately wrote "creative" to state — container could still be starting | `wait_for_healthy comfyui 120` before writing state |
| P1 | **Fail-closed mkdir parity** | model-manager + download-models still had `mkdir \|\| true` | Explicit FATAL + exit 1 (same as scarlix-mode) |
| P1 | **Pin SMG + LiteLLM** | `:latest` and `:main-latest` (dev branch) | SMG `:v1.4.1.post1-sglang-v0.5.10`, LiteLLM `:main-v1.16.19` |
| P1 | **MusicGen pip pinned** | `pip install audiocraft fastapi...` no versions | audiocraft==0.0.2, fastapi==0.115.0, etc. |
| P1 | **Video Wan2GP pinned** | `git clone ... .` no commit | Shallow clone + checkout f3f204e50f6e |
| P1 | **download-models sudo -E → sudo** | Inconsistent with scarlix-mode v18.7.7 fix | `exec sudo` without -E |
| P1 | **BeeLlama missing warning** | Silent "NOT downloaded" → SUCCESS exit even though offline fallback missing | Explicit "OFFLINE MODE WILL NOT WORK" warning |
| P1 | **Fallback JSON has Error field** | When python3 failed, fallback JSON `{"error":"python3 unavailable"}` was valid but Go struct had no Error field → looked like valid status | Added `Error string` to HostStatus struct + `stale:true` in fallback JSON |

### Lock path unification
All 3 scripts now use `/var/lib/scarlix/.models.lock` (was: scarlix-mode + model-manager used `/var/lock/`, download-models used `/var/lib/scarlix/` — inconsistent → no mutual exclusion).

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

## 🗺️ Roadmap

- **v18.9**: LiteLLM E2E inference CI test, Go unit tests (ReserveWSTicket, Mode.Set O_EXCL), CI artifact sharing between jobs (faster), scarlix-doctor LiteLLM + model identity checks.
- **v19.0**: Per-user auth (OIDC/LDAP), real GPU telemetry via DCGM, mode-switch history, Incus dev workspaces.
