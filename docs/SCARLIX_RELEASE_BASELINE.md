# SCARLIX OS v19.2.1 — Release Baseline

> Purpose: The FROZEN baseline for v19.0.6. Documents what is locked (image tags + version contracts), the 14-item regression matrix, and the explicit "do NOT change in v19.0.x" preserve list. Any change to these requires a version bump and explicit decision.

## 1. What is locked — image tags that MUST NOT change in v19.0.x

| Component | Frozen image tag | Source file | Locked since | Verification |
|---|---|---|---|---|
| SGLang (Tier-1) | **`lmsysorg/sglang:v0.4.9.post6-cu128-b200`** | `ai/sglang/docker-compose.yml:22` | v19.0.3 (P0-1) | HTTP 200 + manifest via Docker registry API; supports Qwen3 arch (requires SGLang ≥ 0.4.6.post1); Blackwell sm_120 build (`-b200` variant, cu128 = CUDA 12.8). |
| vLLM (Tier-2) | `vllm/vllm-openai:v0.8.5` | `ai/vllm/docker-compose.yml:10` | v18.9.6 (P0-02) | Qwen3 requires vLLM ≥ 0.8.5 (per official Qwen docs). |
| BeeLlama (Tier-3) | `ghcr.io/ggml-org/llama.cpp@sha256:6d607629e3dd5e85f45c43d1494648126cb3f93f2122c9cd53f43242c94cde14` | `ai/llamacpp/docker-compose.yml` | v18.8.2 (P1) | Pinned by digest — `server` (CPU image) is a floating tag, but BeeLlama is the CPU offline fallback so reproducibility matters. Update deliberately when upgrading. |
| Ollama (Tier-4) | `ollama/ollama:0.5.4` | `ai/ollama/docker-compose.yml` | v17.7 | CPU fallback tier, mem_limit 6g, `OLLAMA_FLASH_ATTENTION=0`. |
| LiteLLM gateway | **`ghcr.io/berriai/litellm:main-v1.21.7`** | `ai/litellm/docker-compose.yml:14` | v19.0.1 (P1) | Verified via `ghcr.io/v2/berriai/litellm/tags/list`. Schema unchanged since 1.16. |
| SMG | `ghcr.io/lightseekorg/smg:v1.4.1.post1-sglang-v0.5.10` | `ai/smg/docker-compose.yml:11` | v18.8 (P1) | Verified via `ghcr.io/v2/lightseekorg/smg/tags/list`. sglang variant matches Tier-1 engine. |
| Whisper (STT) | **`fedirz/faster-whisper-server:sha-307e23f-cuda`** | `voice/whisper/docker-compose.yml` | v19.0.3 (P0-2) | Was `:0.10.0` (tag does NOT exist on Docker Hub — 0.10.x tags = []). Upstream moved to "Speaches" — long-term debt. |
| Buzz (Nostr relay, profile-gated) | **`ghcr.io/block/buzz:latest`** | `workspace/buzz/docker-compose.yml` | v19.0.3 (P1-3) | Was `ghcr.io/block/buzz-relay:latest` (DENIED — repo doesn't exist). `:latest` could not be pinned (GHCR API NO_ACCESS) — experimental profile (`profiles: ["buzz"]`), not in default stack. |
| OpenLit (monitoring) | **`ghcr.io/openlit/openlit:1.5.0`** | `monitoring/docker-compose.yml:39` | v19.0.1 (P1) | Was `openlit/openlit:latest` (Docker Hub has NO official openlit/openlit repo). Verified via `ghcr.io/v2/openlit/openlit/tags/list`. |
| VictoriaMetrics | `victoriametrics/victoria-metrics:v1.108.0` | `monitoring/docker-compose.yml` | v19.0.0 | — |
| Grafana | `grafana/grafana:11.3.0` | `monitoring/docker-compose.yml` | v19.0.0 | — |
| OpenTelemetry Collector | `otel/opentelemetry-collector-contrib:0.161.0` | `monitoring/docker-compose.yml` | v18.8.1 (P1) | Was `:latest` (supply-chain risk). |
| ScarliHQ container | `scarlihq:v${SCARLIX_VERSION}` (locally built, not registry-pulled) | `scarlihq/docker-compose.yml` | v18.7.6 (P2) / v18.8.2 (P0, single source of truth from VERSION file) | Reproducibility via explicit tag; same source as `:latest` was. |
| ScarliHQ build base | `golang:1.23-alpine` | `scarlihq/Dockerfile` | v19.0.0 | go.mod `go 1.23`. |
| ScarliHQ runtime | `alpine:3.20` | `scarlihq/Dockerfile` | v19.0.0 | ca-certificates + tzdata only. |
| CUDA smoke test image | `nvidia/cuda:12.8.1-base-ubuntu24.04` | `install.sh:513` | v19.0.0 | Used by Phase 3 Docker GPU smoke test. |

## 2. Version contracts

| Artifact | Value | Source | Constraint |
|---|---|---|---|
| `VERSION` file | **`19.0.6`** | repo root `VERSION` | Single source of truth, read by every banner (`scarlix-mode`, `scarlix-doctor`, `scarlix-wizard`, `generate-env.sh`, `scarlix-host-bridge` status JSON). |
| `install.sh` VERSION var | `19.0.6` | `install.sh` line 32 | Tracks VERSION file. Header comment + checkout cmd in usage doc also reference `v19.0.5` (will be bumped to `v19.0.6` by main agent — this is a v19.0.6 fix task). |
| `Dockerfile` ARG | `ARG SCARLIX_VERSION=19.0.5` (default; `install.sh` passes `--build-arg SCARLIX_VERSION=$(cat VERSION)` so runtime is correct at 19.0.6 once main agent bumps) | `scarlihq/Dockerfile:26` | Header `# ScarliHQ v19.0.5` (v19.0.5 P2 — bumped from v18.5.2; will be bumped to v19.0.6). |
| Go module path | **`github.com/MoZoHuJa/OS/scarlihq`** | `scarlihq/go.mod` line 1 | Was `github.com/MoZoHuJa/scarlix-os-v12/scarlihq` (stale, v19.0.3 P2-6 fix). All `.go` imports updated to use `github.com/MoZoHuJa/OS/scarlihq/internal/...`. |
| Go version | `go 1.23` | `scarlihq/go.mod` line 3 | Matches `golang:1.23-alpine` build base. |
| `scarlihq` Version var default | `var Version = "19.0.0"` in `main.go` and `rest.go` | `cmd/scarlihq/main.go:23`, `internal/api/rest.go:23` | Fallback only — `install.sh` passes `-ldflags "-X main.Version=$VERSION"` so runtime reports `19.0.6`. |

## 3. Regression matrix (the 14 items from master guide §102)

Each item must pass on a clean EndeavourOS install + NVIDIA RTX 5060 Ti + RTX 4060 Ti before v19.0.6 ships.

| # | Item | How to verify | Owner script |
|---|---|---|---|
| 1 | OS boots | EndeavourOS → `linux` kernel boots to TTY/login | `install.sh` Phase 1 |
| 2 | NVIDIA driver loaded | `nvidia-smi` returns both GPUs; `compute_cap` = `12.0` (Blackwell sm_120) + `8.9` (Ada) | `install.sh` Phase 2 + line 513 Docker GPU smoke test |
| 3 | Docker daemon runs | `systemctl is-active docker` → active; `docker info` exits 0 | `install.sh` Phase 3 |
| 4 | Docker Compose v2 plugin | `docker compose version` → ≥ 2.x | `install.sh` Phase 3 |
| 5 | ScarliHQ dashboard reachable | `curl -sf http://127.0.0.1:8090/` returns embedded HTML (no auth on `/`) | `scarlihq/docker-compose.yml` |
| 6 | ScarliHQ REST API authed | `curl -sf -H "Authorization: Bearer $SCARLIHQ_TOKEN" http://127.0.0.1:8090/api/health` → `{"status":"ok","version":"19.0.6"}` | `scarlihq/internal/api/rest.go` |
| 7 | SGLang starts + healthy | `curl -sf http://127.0.0.1:30000/health` → 200 within 180s start_period | `scarlix-mode ai` → `start_verified_ai` |
| 8 | vLLM starts (if `.experimental`) | `curl -sf http://127.0.0.1:8089/v1/models` → 200 within 180s (only if `/etc/scarlix/.experimental` exists) | `scarlix-mode ai` EXPERIMENTAL branch |
| 9 | Ollama fallback works | Manually `docker stop sglang` then `scarlix-mode ai` → logs `Ollama (CPU, tertiary fallback)`; `curl http://127.0.0.1:11435/api/tags` lists `qwen2.5:3b` | `scarlix-mode::start_verified_ai` |
| 10 | Model storage on `/models` | `ls /models/Qwen3-14B-AWQ/config.json` + `ls /models/Qwen3-14B-Q4_K_M.gguf` exist; `df -m /models` returns ≥10GB free | `download-models.sh` |
| 11 | `install.sh` end-to-end | Fresh EndeavourOS → `bash install.sh` → all 5 phases pass with `SUCCESS_COUNT` > `FAIL_COUNT` and `CRITICAL_FAIL=0` | `install.sh` |
| 12 | Upgrade path | Existing v19.0.5 install → `git pull && bash install.sh` → no data loss, `host-status.json` regenerates, `current-mode` preserved | `install.sh` (idempotent) |
| 13 | Security boundaries intact | `stat -c '%u:%g %a' /var/lib/scarlix/bridge-input` → `65532:65532 700`; `stat -c '%u:%g %a' /var/lib/scarlix/bridge-state` → `0:0 700`; `stat -c '%u:%g %a' /etc/scarlix/.env` → `0:0 600`; `docker inspect scarlihq` shows no docker.sock mount | `scarlihq/docker-compose.yml` + `scarlihq/Dockerfile` |
| 14 | CLI behavior | `scarlix-mode status` exits 0 with no exclusive lock; `scarlix-mode vram` shows bar chart; `scarlix-doctor` exits 0 (PASS > FAIL); `scarlix-wizard` runs whiptail dialog | respective scripts |

## 4. What must NOT change in v19.0.x (the preserve list)

Any PR touching these requires an explicit decision + version bump:

| Area | Preserved property | Source |
|---|---|---|
| Base OS | EndeavourOS (near-vanilla Arch), `linux` + `linux-lts` kernels, BTRFS + Snapper, ZRAM `min(ram/2, 16GB)`, Pipewire | `install.sh` Phase 1, `docs/HARDWARE.md` |
| NVIDIA | Driver + CUDA 12.8 (`cu128` images) + `nvidia-container-toolkit`, `--disable-flashinfer` for Blackwell sm_120 | `install.sh` Phase 2, `ai/sglang/docker-compose.yml` |
| Docker / Compose | Docker engine + Compose v2 plugin (NOT swarm); `scarlix-net` external bridge network created by `scarlix-mode::ensure_scarlix_net` | `install.sh` Phase 3, all compose files |
| ScarliHQ | Go binary, nonroot UID 65532, alpine:3.20 runtime, NO docker.sock, NO nvidia runtime, NO scarlix-mode mount, go:embed 2D dashboard, REST + WS + MCP behind Bearer token | `scarlihq/Dockerfile`, `scarlihq/docker-compose.yml`, `scarlihq/internal/api/rest.go` |
| SGLang | `lmsysorg/sglang:v0.4.9.post6-cu128-b200`, GPU 0, `:30000`, `--disable-flashinfer`, AWQ quant, Qwen3-14B-AWQ default | `ai/sglang/docker-compose.yml` |
| vLLM | `vllm/vllm-openai:v0.8.5`, GPU 1, TP=1 (mixed-GPU safe), `:8089`, `.experimental`-gated ONLY | `ai/vllm/docker-compose.yml` |
| Ollama | `ollama/ollama:0.5.4`, CPU only, `:11435`, `mem_limit: 6g`, `OLLAMA_FLASH_ATTENTION=0`, starter model `qwen2.5:3b` | `ai/ollama/docker-compose.yml` |
| BeeLlama | `ghcr.io/ggml-org/llama.cpp@sha256:6d607...` (CPU `server` image), `:11438`, q4_0 KV cache, GGUF Q4_K_M | `ai/llamacpp/docker-compose.yml` |
| LiteLLM | `ghcr.io/berriai/litellm:main-v1.21.7`, `:4001`, 3-tier fallback (SGLang→Ollama→BeeLlama, vLLM excluded), `/health/liveliness` no-auth healthcheck | `ai/litellm/docker-compose.yml`, `ai/litellm/config.yaml` |
| Model storage | `/models/` (root:root 755), `/var/lib/sglang/cache` for HF cache, `/var/lib/scarlix/ollama` for Ollama store, staging in `/models/.staging/` | `install.sh` Phase 4, `model-manager.sh`, `download-models.sh` |
| Profiles | 4 profiles: Zmor (admin, unlimited), Hugo (100k, gaming), XOX (10k, kids-safe), Mon (50k, elegant); `profiles/*.yaml` | `profiles/` directory |
| Security — host-bridge | `bridge-input/` (UID 65532, 700) + `bridge-state/` (root:root, 700) split; `scarlix-bridge-reader` Go binary with `O_NOFOLLOW + fstat + read same fd`; `desired-mode` written via `O_EXCL` atomic create; no shell fallback if bridge-reader missing | `scarlihq/cmd/scarlix-bridge-reader/main.go`, `files/usr/local/bin/scarlix-host-bridge`, `scarlihq/internal/scarlix_mode/mode.go::Set` |
| Security — atomic readers | `load_env_safe()` grep-based `KEY=VALUE` parser (never `source`); atomic `mktemp + chmod 600 + chown root:root + mv -f` for `/etc/scarlix/.env` and `/opt/scarlix/.env`; fail-closed on every step | `files/etc/systemd/system/generate-env.sh`, `files/usr/local/bin/scarlix-mode`, `files/usr/local/bin/model-manager.sh` |
| Security — WS tickets | 30s single-use, 32-byte crypto/rand hex, `ReserveWSTicket` atomic delete before `Upgrade`, max 1024 outstanding, fail-closed on RNG failure, no `Release` on failure | `scarlihq/internal/api/rest.go`, `scarlihq/internal/webui/ws.go` |
| systemd hardening | `NoNewPrivileges`, `ProtectHome`, `ProtectKernelTunables/Modules/ControlGroups`, `RestrictSUIDSGID`, `LockPersonality`, `RestrictRealtime`, `RestrictNamespaces`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`, `CapabilityBoundingSet=CAP_SYS_ADMIN CAP_NET_RAW CAP_DAC_OVERRIDE CAP_KILL CAP_CHOWN CAP_FOWNER` | `files/etc/systemd/system/scarlix-host-bridge.service` |
| Healthchecks | Every AI container has a healthcheck with `start_period` ≥ 30s; `wait_for_healthy()` deadline-based loop with early-exit on `exited`/`dead`/`not_found`; `none`+running + API-probe fallback | all `ai/*/docker-compose.yml`, `scarlix-mode::wait_for_healthy` |
| Install path | `/opt/scarlix/` (root:root 755 — was chown-to-user LPE fixed v18.4), `/etc/scarlix/` (config + secrets), `/var/lib/scarlix/` (state + bridge dirs), `/models/` (model files) | `install.sh` Phase 4 |
| CLI behavior | `scarlix-mode <mode>` re-execs via sudo if non-root (no `-E`); `scarlix-mode status`/`vram`/`vram-check` skip the exclusive flock (read-only); `scarlix-doctor --fix` uses named `fix_*` functions (no `bash -c "$string"`); `scarlix-wizard` detects NVIDIA dGPU count via `lspci` and creates `/etc/scarlix/.experimental` if ≥2 | `files/usr/local/bin/scarlix-mode`, `files/usr/local/bin/scarlix-doctor`, `files/usr/local/bin/scarlix-wizard` |

> **Baseline:** This document is part of the v19.0.6 release baseline freeze. Do not modify content without a version bump.
