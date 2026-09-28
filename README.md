# SCARLIX OS v17.9.9 — EndeavourOS Edition (Host-Bridge Stabilization)

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Working AI Path**: model-aware, fail-hard, healthcheck + fallback.
> **Verified**: SGLang (GPU0, --disable-flashinfer) + vLLM (GPU1, TP=1) + BeeLlama (CPU) + Ollama (CPU tertiary fallback).

**Version:** v17.9.9 | **Base:** EndeavourOS (Arch) | **License:** MIT

## 🚀 Install (NO ISO)

### Primary (safe — review first) ⭐
```bash
git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
cd ~/scarlix-os
git checkout v17.9.9   # ALWAYS checkout specific tag (main may be ahead)
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
#    http://<this-ip>:8090/?token=<SCARLIHQ_TOKEN>
```

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

**Dashboard** (`http://<ip>:8090/?token=<SCARLIHQ_TOKEN>`): all 7 modes clickable. `game`/`tv` request the switch but `systemctl sunshine` runs on host (bridge applies it) — see Known Limitations.

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
| **v17.9.9** | 2026-10 | **Host-Bridge stabilization + state machine. 13 fixes: Dockerfile nonroot user (P0), bridge/ chown 65532+775 (P0), df parsing fixed (P0), HTTP error codes, desired-mode retry, JSON via python3, state machine fields, MCP secureCompare, WS CIDR origin, scarlix-mode sudo removed, turbo dump_vram, systemd hardening, mem-fraction env var, version via ldflags. CI: runtime smoke test.** |
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
- **ScarliHQ auth = single shared token**: No per-user auth. Token in `/etc/scarlix/.env` (chmod 600). Token-in-URL (`?token=`) can leak via browser history/proxy logs — known limitation. For production, put a reverse proxy with session auth in front.
- **First install is slow**: `pacman -Syu` + NVIDIA + CUDA + cuDNN + Steam/Wine + docker images + ScarliHQ Go build + model download (50-150GB) = hours. Reboots + re-login required for NVIDIA driver + docker group.
- **host-status.json 5s latency**: Dashboard data is up to 5s stale (host-bridge timer interval). Mode transition feedback (applied/retrying/failed) appears within 5s. Not for real-time control.

## 🗺️ Roadmap

- **v17.10**: ScarliHQ — per-user auth (OIDC/LDAP), real GPU telemetry via DCGM, mode-switch history.
- **v18.0**: Incus dev workspaces, ScarliHQ Rust refactor, profile-based stack selection.
