# SCARLIX OS v19.0.6 — Component Map

> Purpose: Catalog every component shipped by the v19.0.6 tree — what it is, where it lives, what it reads, what it writes. Sourced by direct file read on branch `fix-v19.0.6`.

## ScarliHQ (Go) — the dashboard control plane

| Sub-component | Language | File path | Runtime | Reads | Writes |
|---|---|---|---|---|---|
| ScarliHQ binary | Go | `scarlihq/cmd/scarlihq/main.go` | service (container, nonroot UID 65532) | `/etc/scarlix/.env` (SCARLIHQ_TOKEN), `/etc/scarlix/profiles/`, `/var/lib/scarlix/*` | `/var/lib/scarlix/bridge-input/desired-mode` (via Mode.Set) |
| Dashboard HTML | HTML/CSS/JS | `scarlihq/cmd/scarlihq/frontend/dist/index.html` | go:embed into binary | — | — |
| REST API | Go | `scarlihq/internal/api/rest.go` | inside ScarliHQ | `host-status.json` | wsTickets (in-mem map), desired-mode |
| WebSocket server | Go | `scarlihq/internal/webui/ws.go` | inside ScarliHQ | `host-status.json` | wsSlots channel (max 16) |
| MCP / JSON-RPC server | Go | `scarlihq/internal/mcp/server.go` | inside ScarliHQ | host-status | — |
| Mode manager | Go | `scarlihq/internal/scarlix_mode/mode.go` | inside ScarliHQ | `/var/lib/scarlix/host-status.json` (transition check) | `/var/lib/scarlix/bridge-input/desired-mode` (O_EXCL, 0600) |
| Status reader | Go | `scarlihq/internal/status/status.go` | inside ScarliHQ | `/var/lib/scarlix/host-status.json` | — |
| Guard | Go | `scarlihq/internal/guard/guard.go` | inside ScarliHQ | — | — |
| Profile loader | Go | `scarlihq/internal/profiles/loader.go` | inside ScarliHQ | `/etc/scarlix/profiles/*.yaml` | — |
| **scarlix-bridge-reader** | Go | `scarlihq/cmd/scarlix-bridge-reader/main.go` | host CLI (root, `/usr/local/bin/`) | one arg: file path (opened `O_RDONLY\|O_NOFOLLOW\|O_NONBLOCK`) | stdout (file contents), stderr (errors → `/var/log/scarlix/bridge-reader.log`) |
| ScarliHQ build | Dockerfile | `scarlihq/Dockerfile` | multi-stage: `golang:1.23-alpine` → `alpine:3.20` | `scarlihq/` source | `scarlihq:v${SCARLIX_VERSION}` image (also embeds `/scarlix-bridge-reader` for `docker create`+`docker cp` extraction in install.sh Phase 4) |
| Compose | YAML | `scarlihq/docker-compose.yml` | systemd-independent (docker compose up) | `/etc/scarlix/.env` (SCARLIX_VERSION, SCARLIHQ_TOKEN) | `scarlihq` container |
| Go module | Go | `scarlihq/go.mod` | module `github.com/MoZoHuJa/OS/scarlihq`, go 1.23 | deps: gorilla/websocket v1.5.3, yaml.v3 v3.0.1 | — |

## Host-side bash scripts (root, `/usr/local/bin/`)

| Script | File path | Runtime | Reads | Writes |
|---|---|---|---|---|
| **scarlix-mode** | `files/usr/local/bin/scarlix-mode` | CLI (root; re-execs via sudo if non-root) | `/etc/scarlix/models.yaml`, `/etc/scarlix/.env`, `/etc/scarlix/.experimental`, `/etc/scarlix/VERSION` | `/opt/scarlix/.env` (atomic), `/var/lib/scarlix/current-mode`, `/var/log/scarlix-mode.log`, `/var/lib/scarlix/models.sha256` |
| **scarlix-host-bridge** | `files/usr/local/bin/scarlix-host-bridge` | systemd oneshot timer (5s, root) | `/var/lib/scarlix/bridge-input/desired-mode` (via bridge-reader), `/var/lib/scarlix/bridge-state/.retry`, `/var/lib/scarlix/current-mode` | `/var/lib/scarlix/host-status.json` (atomic), `/var/lib/scarlix/bridge-state/{.retry,last-transition}`, `/var/log/scarlix/host-bridge.log` |
| **scarlix-doctor** | `files/usr/local/bin/scarlix-doctor` | CLI (root recommended; `--fix`, `--strict`) | `/etc/scarlix/.env`, `/var/lib/scarlix/host-status.json`, docker, nvidia-smi, curl | fixes via named `fix_*` functions (no destructive writes) |
| **scarlix-wizard** | `files/usr/local/bin/scarlix-wizard` | CLI (interactive, whiptail) | `lspci`, `ip route`, `/etc/scarlix/VERSION` | `/etc/scarlix/.experimental` (if 2+ NVIDIA dGPU), network/system config |
| **model-manager.sh** | `files/usr/local/bin/model-manager.sh` | systemd oneshot timer (Mon 04:00 weekly, root) | `/etc/scarlix/models.yaml`, `/opt/scarlix/.env` (via `load_env_safe`) | `/models/.staging/` → `/models/`, `/var/log/scarlix/model-manager.log`, Telegram report |
| **download-models.sh** | `files/usr/local/bin/download-models.sh` | CLI (root) | `/etc/scarlix/models.yaml` | `/models/Qwen3-14B-AWQ/`, `/models/Qwen3-14B-Q4_K_M.gguf`, Ollama pull `qwen2.5:3b`, `/var/log/scarlix/model-download.log` |
| **generate-env.sh** | `files/etc/systemd/system/generate-env.sh` (installed as `/usr/local/bin/`-equivalent; called by scarlix-doctor `fix_regenerate_env`) | CLI/oneshot (root) | `/etc/scarlix/.env` (existing), `/etc/scarlix/VERSION`, env: `TELEGRAM_BOT_TOKEN`, `TELEGRAM_ZMOR_CHAT_ID` | `/etc/scarlix/.env` (atomic mktemp + chmod 600 + chown root:root + mv), `/etc/scarlix/secrets/` dir |
| **generate-litellm-config.sh** | `files/usr/local/bin/generate-litellm-config.sh` | CLI (root; called by `scarlix-mode` before AI start) | `$1` = models.yaml (default `/etc/scarlix/models.yaml`) | `$2` = output (default `/opt/scarlix/ai/litellm/config.yaml`); atomic? (called inline) |
| **generate-sha256sums.sh** | `files/usr/local/bin/generate-sha256sums.sh` | CLI (root) | `CRITICAL_FILES[]` array of install/compose/Dockerfile paths | `/tmp/SCARLIX-v${VERSION}-SHA256SUMS` (or `--output PATH`) |

## Inference runtimes (one compose file each, `ai/*/`)

| Engine | Compose | Container name | Tier |
|---|---|---|---|
| SGLang | `ai/sglang/docker-compose.yml` | `sglang` | 1 (GPU 0, primary) |
| vLLM | `ai/vllm/docker-compose.yml` | `vllm` | 2 (GPU 1, `.experimental` only) |
| BeeLlama (llama.cpp) | `ai/llamacpp/docker-compose.yml` | `beellama` | 3 (CPU offline fallback) |
| Ollama | `ai/ollama/docker-compose.yml` | `ollama-agent` | 4 (CPU tertiary fallback) |
| LiteLLM gateway | `ai/litellm/docker-compose.yml` + `ai/litellm/config.yaml` | `litellm` | external client gateway |
| SMG | `ai/smg/docker-compose.yml` + `ai/smg/config.yaml` | `smg` | profile-gated latency-aware gateway |

See `docs/SCARLIX_RUNTIME_MAP.md` for full per-engine details (image tags, ports, GPU bindings, healthchecks, start commands).

## systemd units (`files/etc/systemd/system/`)

| Unit | File | Type | Fires |
|---|---|---|---|
| `scarlix-host-bridge.service` | `scarlix-host-bridge.service` | oneshot, root | `ExecStart=/usr/local/bin/scarlix-host-bridge` |
| `scarlix-host-bridge.timer` | `scarlix-host-bridge.timer` | timer | `OnBootSec=10s`, `OnUnitActiveSec=5s`, `AccuracySec=1s` |
| `model-manager.service` | `model-manager.service` | oneshot, root, `TimeoutStartSec=7200` | `ExecStart=/usr/local/bin/model-manager.sh` |
| `model-manager.timer` | `model-manager.timer` | timer | `OnCalendar=Mon *-*-* 04:00:00`, `RandomizedDelaySec=900` |
| `scarlix-tv-mode.service` | `scarlix-tv-mode.service` | oneshot, `RemainAfterExit=yes`, `TimeoutStartSec=300` | `ExecStart=/bin/bash -c 'for i in $(seq 1 60); do docker inspect sunshine-tv ...; done'` (60×2s retries for cold pull) |
| `generate-env.sh` (script, not unit) | `generate-env.sh` | — | invoked by `scarlix-doctor fix_regenerate_env` and at install time |

## Configuration sources (single source of truth chain)

| Config | Source of truth | Read by |
|---|---|---|
| Model paths + per-engine tuning | `/etc/scarlix/models.yaml` (repo: `models.yaml`) | `scarlix-mode`, `model-manager.sh`, `download-models.sh`, `generate-litellm-config.sh` |
| Per-engine env vars (model paths, mem_fraction, ctx_len, etc.) | `/opt/scarlix/.env` (generated by `scarlix-mode` from models.yaml) | `docker compose --env-file /opt/scarlix/.env` |
| Secrets + tokens | `/etc/scarlix/.env` (generated by `generate-env.sh`, root:root 600) | ScarliHQ compose, LiteLLM compose, SMG compose, Buzz compose, monitoring compose |
| Version | `/etc/scarlix/VERSION` (repo: `VERSION`) | All banners (scarlix-mode, scarlix-doctor, scarlix-wizard, generate-env.sh, scarlix-host-bridge status JSON) |
| Experimental flag | `/etc/scarlix/.experimental` (created by wizard if 2+ NVIDIA dGPU) | `scarlix-mode` (gates vLLM tier-2), `scarlix-host-bridge` (sets `experimental:true` in host-status.json) |
| GPU topology | `/var/lib/scarlix/gpu-layout` (written once by `install.sh` line 530) | `scarlix-doctor` |

> **Baseline:** This document is part of the v19.0.6 release baseline freeze. Do not modify content without a version bump.
