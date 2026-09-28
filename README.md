# SCARLIX OS v17.9.7 — EndeavourOS Edition (Reliability + Real Dashboard)

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Working AI Path**: model-aware, fail-hard, healthcheck + fallback.
> **Verified**: SGLang (GPU0, --disable-flashinfer) + vLLM (GPU1, TP=1) + BeeLlama (CPU) + Ollama (CPU tertiary fallback).

**Version:** v17.9.7 | **Base:** EndeavourOS (Arch) | **License:** MIT

## 🚀 Install (NO ISO)

### Primary (safe — review first) ⭐
```bash
git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
cd ~/scarlix-os
git checkout v17.9.7   # ALWAYS checkout specific tag (main may be ahead)
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
```

---

## 🆕 What's New in v17.9.7 (vs v17.9.6)

**Reliability + real dashboard — 11 fixes from 2 uncompromising reviews.**

v17.9.6 had 5 blockers that prevented a clean EndeavourOS install. v17.9.7 fixes them.

| # | Fix | v17.9.6 Problem | v17.9.7 Solution |
|---|-----|-----------------|-------------------|
| P0 | **ScarliHQ image tag mismatch** | compose `image: localhost/scarlihq:v12` ≠ install build `scarlihq:latest` → compose used wrong/no image | Unified to `scarlihq:latest` everywhere |
| P0 | **ScarliHQ Dockerfile build fail** | `COPY . .` overwrote `go.sum` (populated by `go mod download`) → `go build` failed on empty go.sum | Reordered: `COPY . .` → `go mod download` → `go build` (`GOFLAGS=-mod=mod`) |
| P0 | **ScarliHQ APIs returned empty** | Runtime `distroless/static` has no `nvidia-smi`/`docker` → `/api/gpu` + `/api/containers` always empty | Runtime = `nvidia/cuda:12.8.0-base` + `docker.io` installed + `scarlix-mode` mounted |
| P0 | **ScarliHQ = dead placeholder** | 1-line HTML (`<h1>Dashboard</h1><p>v17.9.2</p>`) | Real dashboard: GPU cards, mode switcher, container table, live WS clock |
| P0 | **git tag v17.9.x never existed** | README `git checkout v17.9.6` → `pathspec did not match` | `git tag v17.9.7` created + pushed |
| P1 | **SGLang flashinfer not disabled** | `flashinfer: true` in yaml + no flag in compose → Blackwell (sm_120) runtime fail | `--disable-flashinfer` in SGLang compose + `flashinfer: false` in yaml |
| P1 | **docker network rm without disconnect** | `docker network rm scarlix_net` fails on active endpoints → upgrade breaks | Disconnect loop before rm (graceful migration) |
| P1 | **multilib not checked** | Steam/Wine/`lib32-*` need `[multilib]`; clean EOS may lack it → Phase 1/2 fail | install.sh enables `[multilib]` before package install |
| P1 | **download-models FAILED counter** | `FAILED=1` boolean (last-fail-wins); no disk check | `FAILED=$((FAILED+1))` counter + disk-space pre-check (fail-hard < 10GB) |
| P1 | **scarlix-doctor curl hard dep** | `curl -sf` with no fallback | `http_ok()` helper: curl → wget fallback |
| P1 | **/models chmod 775** | Group-writable (unnecessary) | `chmod 750` (owner-only; containers read as root via `:ro`) |

**Bonus:** yq functional verification in Phase 1 (fail-hard if broken), disk-space check before starter model download, ScarliHQ dashboard auto-built + started in Phase 5.

### Review false-positives (verified already-correct in v17.9.6)
These were flagged in reviews but were already fixed in v17.9.6:
- `yq` IS in `packages.x86_64` (python kislyuk — supports jq syntax used by scripts).
- `usermod -aG docker` IS in Phase 3 (after docker package creates group in Phase 1).
- Duplicate `scarlix-net` in compose: NONE (all 25 compose files have single network).
- llamacpp healthcheck: already `wget` (alpine-safe, not python).
- models.yaml AWQ/GGUF split: already correct (sglang→`Qwen/Qwen3-14B-AWQ` safetensors, beellama→`Qwen/Qwen3-14B-GGUF` + `Qwen3-14B-Q4_K_M.gguf`).
- README uses `scarlix-doctor` (hyphen, not "scarlix doctor" with space).

---

## 🆕 What's New in v17.9.6 (vs v17.9.1)

**Reliability Release — 15 P0+P1 fixes from 4 reviews + CI + ScarliHQ.**

| # | Fix | v17.9.1 Problem | v17.9.6 Solution |
|---|-----|-----------------|-------------------|
| P0 | **4 compose files duplicate networks** | `[scarlix-net, scarlix-net]` (comfyui, litellm, smg, video) → Compose parse error | Deduplicated to `[scarlix-net]` |
| P0 | **scarlix_net in 10+ source files** | install.sh sed didn't catch `scarlix_net` → 10+ containers fail | Fixed in SOURCE files + migration in install.sh |
| P0 | **VERSION=17.9.0** | install.sh had wrong version | `VERSION="17.9.6"` + VERSION file as single source |
| P0 | **Default paths old in generate_env_file** | `Qwen3-14B-Instruct-AWQ` (doesn't exist) | `Qwen3-14B-AWQ` + `Qwen3-14B-Q4_K_M.gguf` |
| P0 | **download-models.sh false "complete"** | Download fail → script continues → "complete" is lie | `FAILED=0`, exit 1 on fail |
| P0 | **scarlix-doctor PASS for unhealthy** | `running` + `unhealthy` = PASS | `unhealthy` = FAIL; API healthcheck |
| P0 | **Hash stored before recreate** | Failed recreate → no retry | Hash stored AFTER successful recreate |
| P0 | **docker start (not $DC up -d)** | Old config not picked up | `$DC up -d` for all container starts |
| P1 | **vLLM + llama.cpp healthcheck** | No Docker healthcheck | Added `CMD-SHELL wget` healthcheck (alpine-safe) |
| P1 | **model-manager.sh dvojitý Ollama** | Same `.ollama.model` read 2x → double pull | Single read + pull |
| P1 | **Version strings unified** | 11+ files with old versions | All v17.9.6 |
| P1 | **scarlix-wizard path fix** | `bash /etc/systemd/system/download-models.sh` | `download-models.sh` |
| P1 | **dump_vram hardcoded model** | `qwen2.5:3b` hardcoded | Read from yaml |
| P1 | **docs Ubuntu → Arch** | `apt`, `Ubuntu 24.04` in troubleshooting | `pacman`, `EndeavourOS (Arch)` |
| NEW | **ScarliHQ placeholder** | `frontend/dist/index.html` missing → docker build fail | Placeholder created (replaced by real dashboard in v17.9.7) |

**Bonus:** `scarlix-doctor --fix` mode, GitHub Actions CI (shellcheck + bash + YAML + compose validation), VERSION file.

---

## 🆕 What's New in v17.9.1 (vs v17.9)

**Hotfix — 10 fixes from 4 reviews. scarlix-doctor added.**

| # | Fix | v17.9 Problem | v17.9.1 Solution |
|---|-----|---------------|-------------------|
| P0 | **MODE pred flock** | `MODE` used before assignment → `set -u` crash on every call | `MODE="${1:-status}"` moved above flock check |
| P0 | **Hash-based healthcheck** | mtime `.env` = always "now" → always `--force-recreate` | `sha256sum` of model paths → recreate only on real change |
| P0 | **Correct HF repo/file names** | `Qwen/Qwen3-14B-Instruct-AWQ` doesn't exist → silent download fail | `Qwen/Qwen3-14B-AWQ` + `Qwen3-14B-Q4_K_M.gguf` |
| P0 | **Checkpoint nvidia_open len phase2** | phase 1/3/4 stored `none` → always invalid → full re-install | nvidia_open compared only for phase2 |
| P1 | **model-manager.sh schema** | `.llamacpp.*`, `.ollama_main.*` (old schema) | `.beellama.*`, `.ollama.*` (v17.5+ schema) |
| P1 | **Creative stack scarlix_net** | `scarlix_net` (underscore) → network not found | `scarlix-net` (hyphen) everywhere |
| P1 | **SGLang image pin** | `latest-cu128` (unpinned) | `v0.4.4-cu128` (verified) |
| P1 | **generate-env.sh guard** | Overwrote passwords on re-run | If .env exists, only add missing keys |
| P1 | **Version strings unified** | v17.5/v17.8/v17.9 mixed | All v17.9.1 |
| NEW | **scarlix-doctor** | No self-diagnostic | `scarlix-doctor` — checks OS, NVIDIA, Docker, GPU, models, .env, containers |

---

## 🆕 What's New in v17.8 (vs v17.7)

**Stable release — 6 fixes. .env file properly passed to compose.**

| # | Fix | v17.7 Problem | v17.8 Solution |
|---|-----|---------------|-----------------|
| P0 | **--env-file on all compose calls** | Compose didn't read /opt/scarlix/.env → `${SGLANG_MODEL_PATH}` not substituted | `DC="docker compose --env-file /opt/scarlix/.env"` on ALL calls |
| P1 | **Always regenerate .env** | `load_model_paths` cached .env → stale after models.yaml edit | `generate_env_file()` called every time |
| P1 | **BeeLlama only when SGLang+vLLM fail** | Started "anyway" even when SGLang running → wasted RAM | Only starts if `sglang_ok=0 && vllm_ok=0` |
| P1 | **Docker restart after nvidia-ctk** | nvidia-ctk before Docker fully loaded → GPU not visible | `systemctl restart docker` after configure + verify |
| P1 | **Ollama volume chmod 700** | Was 777 (anyone can write) | `chmod 700 root:root` |
| P1 | **REAL_USER without logname** | `logname` fails without TTY | `${SUDO_USER:-${USER:-}}` fallback |

---

## 🎮 GPU Modes

```bash
scarlix-mode ai        # Verified path + healthcheck (always re-checks)
scarlix-mode turbo     # Same as ai (max throughput)
scarlix-mode offline   # BeeLlama.cpp CPU (q4_0 KV, 32k context)
scarlix-mode game      # Native Steam/Sunshine
scarlix-mode creative  # ComfyUI + Video + Music
scarlix-mode tv        # Docker Sunshine
scarlix-mode vram      # VRAM health (shows compute_cap per GPU)
scarlix-mode status    # System summary (shows models + config)
scarlix-doctor         # Self-diagnostic (add --fix for auto-fix)
```

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
download-models.sh   # download new model
scarlix-mode ai      # restart with new model (hash change → force-recreate)
```
`scarlix-mode` parses `models.yaml` → exports `SGLANG_MODEL_PATH` → compose uses it. **One source of truth.**

---

## 🔧 NVIDIA Rollback

```bash
# Option 1: rollback driver (downgrade via yay)
sudo downgrade nvidia-open

# Option 2: boot LTS kernel (nvidia-open-lts + linux-lts installed atomically)
sudo grub-set-default 1
sudo reboot
```

---

## 📁 File Layout (v17.9.7)

```
OS/
├── install.sh                              # v17.9.7: Bootstrap (5 phases, fail-hard, multilib, disk-check)
├── README.md
├── AGENTS.md
├── VERSION                                 # Single source of truth (17.9.7)
├── models.yaml                             # Single source of truth (env vars → compose)
├── packages.x86_64                         # Package list (yq, curl, wget, steam, wine, …)
├── .github/workflows/ci.yml               # CI: shellcheck + bash -n + YAML + compose validation
├── files/
│   ├── usr/local/bin/
│   │   ├── scarlix-wizard                  # Creates .experimental for 2+ GPU, gateway prompt
│   │   ├── scarlix-mode                    # Healthcheck + env var parsing + wait_for_healthy
│   │   ├── scarlix-doctor                  # Self-diagnostic (--fix mode, http_ok fallback)
│   │   ├── download-models.sh              # HF download (FAILED counter + disk-space check)
│   │   └── model-manager.sh                # Weekly HF auto-pull + Telegram report
│   └── etc/
│       ├── systemd/
│       │   ├── zram-generator.conf
│       │   └── system/
│       │       ├── model-manager.{service,timer}
│       │       └── generate-env.sh         # /etc/scarlix/.env (secrets, guarded)
│       └── pacman.d/hooks/
│           └── scarlix-docker-backup.*
├── ai/                                     # Docker stacks (all scarlix-net)
│   ├── sglang/docker-compose.yml          # cu128, --disable-flashinfer, env vars
│   ├── vllm/docker-compose.yml            # TP=1, no LoRA, no profile gate
│   ├── llamacpp/docker-compose.yml        # Official llama.cpp, q4_0 KV, wget healthcheck
│   ├── ollama/docker-compose.yml          # Fallback (starter qwen2.5:3b), CPU, mem_limit 6g
│   ├── comfyui/docker-compose.yml         # Creative profile, absolute volumes
│   └── ...
├── scarlihq/                               # Dashboard (real frontend, debian+cuda runtime)
│   ├── Dockerfile                          # Multi-stage: go build → nvidia/cuda + docker-cli
│   ├── docker-compose.yml                  # image: scarlihq:latest, mounts scarlix-mode
│   ├── go.mod / go.sum
│   ├── cmd/scarlihq/
│   │   ├── main.go                          # //go:embed frontend/dist/index.html
│   │   └── frontend/dist/index.html        # Real dashboard (GPU/mode/containers/WS)
│   └── internal/                           # api, webui(ws), guard, mcp, profiles, scarlix_mode
└── agents/, gaming/, voice/, network/, security/, monitoring/, ...
```

---

## 🏗️ Verified AI Path (v17.9.7)

```
GPU 0: RTX 5060 Ti 16GB (Blackwell sm_120)
└── Tier-1: SGLang v0.4.4-cu128 (agents, RadixAttention, --disable-flashinfer)

GPU 1: RTX 4060 Ti 16GB (Ada sm_89)
└── Tier-2: vLLM v0.8.0 (TP=1, no LoRA, separate model — mixed arch safe)

CPU:
└── Tier-4: llama.cpp (official image, q4_0 KV cache, 32k context)

Fallback (always ready):
└── Ollama 0.5.4 + qwen2.5:3b (starter model auto-downloaded by install.sh)
```

### 🔄 Fallback Chain (real, not theoretical)

1. `scarlix-mode ai` → starts SGLang + vLLM (if .experimental) + BeeLlama + Ollama
2. If SGLang unhealthy after 300s → `docker stop sglang` + try vLLM
3. If vLLM unhealthy after 300s → `docker stop vllm` + try BeeLlama (CPU)
4. If BeeLlama fails → Ollama (qwen2.5:3b starter, always ready)
5. `scarlix-mode ai` re-run → healthchecks + restarts unhealthy containers

### ScarliHQ Dashboard (:8090)
Real web dashboard built + started in install.sh Phase 5:
- GPU status cards (temp, util, VRAM, power) via `/api/gpu`
- Mode switcher (ai/turbo/offline/game/stop) via `/api/mode`
- Container table via `/api/containers`
- Live clock via WebSocket `/ws`

---

## 📜 Version History

| Version | Date | Key Changes |
|---------|------|-------------|
| **v17.9.7** | 2026-10 | **Reliability + real dashboard. 11 fixes: ScarliHQ image tag unified (P0), Dockerfile build fixed (P0), runtime = nvidia/cuda+docker-cli (P0), real dashboard HTML (P0), git tag pushed (P0), SGLang --disable-flashinfer (P1), network disconnect loop, multilib pre-check, download-models FAILED counter + disk check, scarlix-doctor http_ok fallback, /models chmod 750.** |
| v17.9.6 | 2026-10 | Reliability. 15 fixes: 4 duplicate networks (P0), scarlix_net→scarlix-net in source (P0), VERSION fix (P0), default paths (P0), download fail-hard (P0), doctor unhealthy=FAIL (P0), hash after recreate (P0), $DC up -d (P0), vLLM/llama.cpp healthcheck, model-manager dvojitý fix, version unified, wizard path, dump_vram yaml, docs Ubuntu→Arch, ScarliHQ placeholder. Bonus: doctor --fix, CI, VERSION file. |
| v17.9.1 | 2026-10 | Hotfix. 10 fixes: MODE pred flock (P0), hash-based healthcheck (P0), correct HF repo IDs (P0), checkpoint nvidia_open len phase2 (P0), model-manager schema, creative scarlix_net, SGLang image pin, generate-env guard, version unified, scarlix-doctor added. |
| v17.9 | 2026-10 | Final Polish. 6 fixes: --env-file, always regenerate .env, BeeLlama only on fallback, Docker restart after nvidia-ctk, Ollama chmod 700, REAL_USER without logname. |
| v17.8 | 2026-10 | Stable. 6 fixes: --env-file on all compose calls, always regenerate .env, BeeLlama only on fallback, Docker restart after nvidia-ctk, Ollama chmod 700, REAL_USER without logname. |
| v17.5–17.6 | 2026-10 | EndeavourOS re-base, bootstrap installer, model-agnostic, fail-hard, TP=1. |
| v16.x | 2026-08 | Garuda Linux. |
| v15 | 2026-06 | Model-Agnostic. |

---

## ⚠️ Known Limitations

- **CI exists but no real-HW test**: `.github/workflows/ci.yml` runs shellcheck + bash -n + YAML + compose validation on every push. QEMU doesn't test NVIDIA/CUDA (no GPU in CI).
- **Single maintainer**: One person maintaining full stack.
- **vLLM TP=1**: Separate model per GPU (less efficient than TP=2 but mixed-arch safe).
- **ScarliHQ mode-switch**: `scarlix-mode` exec'd in container — `systemctl` commands (sunshine) won't apply (container has no systemd); AI/docker mode switches work via docker.sock.
- **First install is slow**: `pacman -Syu` + NVIDIA + CUDA + cuDNN + Steam/Wine + docker images + ScarliHQ Go build + model download (50-150GB) = hours. Reboots + re-login required for NVIDIA driver + docker group.

## 🗺️ Roadmap

- **v17.10**: ScarliHQ — native Docker API (drop docker-cli exec), real GPU telemetry via DCGM.
- **v18.0**: Incus dev workspaces, ScarliHQ Rust refactor, profile-based stack selection.
