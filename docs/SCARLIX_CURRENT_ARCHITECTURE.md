# SCARLIX OS v19.0.6 — Current Architecture

> Purpose: One-page reference for the live v19.0.6 system architecture — the 5-layer model, the Secure Host-Bridge privilege pattern, the 4-tier inference failover, and the LiteLLM/SMG gateway tier. Derived from the actual repo on branch `fix-v19.0.6` (commit c731b48 + this task's doc additions).

## Install model (v19+, frozen since v19.0.0)

- **Base OS:** EndeavourOS (near-vanilla Arch). No ISO — `git clone` + `bash install.sh` (5 phases: packages → NVIDIA → Docker → copy files → wizard + ScarliHQ build). Source: `install.sh` line 9.
- **Kernel:** `linux` + `linux-lts` (fallback). BTRFS + Snapper. ZRAM `min(ram/2, 16GB)`. Pipewire.
- **GPU:** NVIDIA driver + CUDA 12.8 (`cu128` images) + `nvidia-container-toolkit`. Source: `docs/HARDWARE.md`, `install.sh` Phase 2.

## The 5-layer model

| Layer | Components | Purpose |
|---|---|---|
| 1 — Inference + Base OS | EndeavourOS + Docker + NVIDIA driver + 4 inference engines | Compute substrate |
| 2 — Workspace + Infrastructure | Buzz (Nostr relay), LiteLLM `:4001`, SMG `:4000`, Headscale VPN, Caddy, CrowdSec | Services that route/observe/persist |
| 3 — Agents | Hermes (CEO gateway), Pi-Bolt (coding agent), gstack tools | Long-running agents |
| 4 — ScarliHQ | Go binary `:8090` (REST + WS + MCP), go:embed 2D dashboard, Profile Manager, Guard | Host-Bridge control plane |
| 5 — Family (Profiles) | Zmor (admin, unlimited), Hugo (100k), XOX (10k, kids-safe), Mon (50k) | Per-user policy layer |

Source: `docs/ARCHITECTURE.md` "5-Layer Architecture"; profiles: `profiles/{zmor,hugo,xox,mon}.yaml`.

## Secure Host-Bridge pattern (the privilege boundary)

The ScarliHQ dashboard container is **untrusted + LAN-facing**. It has NO direct host capability. Privileged operations are funneled through a file-based bridge.

```
Browser (token) ──HTTP──> ScarliHQ container (:8090)
                            │  nonroot UID 65532, alpine:3.20
                            │  NO docker.sock, NO nvidia runtime, NO scarlix-mode mount
                            │  READ-ONLY mounts: /var/lib/scarlix, /etc/scarlix
                            │  WRITABLE mount:  /var/lib/scarlix/bridge-input/ (UID 65532, mode 700)
                            ▼
                  writes /var/lib/scarlix/bridge-input/desired-mode  (O_EXCL atomic create)
                            │
                  scarlix-host-bridge.timer (root, every 5s)
                            │  flock /var/lock/scarlix-host-bridge.lock
                            ▼
                  /usr/local/bin/scarlix-bridge-reader (Go, O_NOFOLLOW + fstat + read same fd)
                            │  validates UID=65532 + mode in {600,640,700} + size ≤100 + S_IFREG
                            ▼
                  /usr/local/bin/scarlix-mode <mode>   (root)
                            │  flock /run/scarlix/scarlix-mode.lock (FD 200)
                            │  flock /var/lib/scarlix/.models.lock (FD 9, shared with model-manager + download-models)
                            ▼
                  docker compose up/down on /opt/scarlix/ai/*/
                            │
                            ▼
                  /var/lib/scarlix/host-status.json (atomic mktemp + mv, 644)
                            │  read by ScarliHQ (ro mount) → /api/status + /ws (2s push)
```

Sources: `scarlihq/docker-compose.yml`, `scarlihq/Dockerfile`, `files/usr/local/bin/scarlix-host-bridge`, `scarlihq/cmd/scarlix-bridge-reader/main.go`, `scarlihq/internal/scarlix_mode/mode.go`.

## 4-tier inference failover (`scarlix-mode ai`, code-truth)

Verified against `files/usr/local/bin/scarlix-mode::start_verified_ai()` lines 757–855:

| Priority | Engine | Triggered by | On failure |
|---|---|---|---|
| 1 (primary) | SGLang (GPU 0, AWQ, `--disable-flashinfer`) | always, model exists | stop container, free VRAM, fall to tier 2 |
| 2 | vLLM (GPU 1, TP=1) | only if `/etc/scarlix/.experimental` exists | stop, fall to tier 3 |
| 3 | BeeLlama / llama.cpp (CPU, GGUF Q4_K_M) | both 1 + 2 failed | log + fall to tier 4 |
| 4 (tertiary fallback) | Ollama (CPU, `qwen2.5:3b`) | tiers 1+2+3 all failed | if this also fails → mode switch fails (`return 1`) |

**Note:** `docs/ARCHITECTURE.md`'s ASCII diagram has tiers 3 and 4 swapped (it shows Ollama before BeeLlama). The code is the source of truth — BeeLlama is tier-3, Ollama is the tertiary fallback. AGENTS.md table matches the code.

## LiteLLM 3-tier gateway (external clients, vLLM excluded)

`ai/litellm/config.yaml` defines a SIMPLIFIED 3-tier chain for external OpenAI-compatible clients:

```
POST :4001/v1/chat/completions  (model="scarlix-default", Bearer LITELLM_MASTER_KEY)
        ↓
   SGLang (scarlix-default → openai/Qwen3-14B-AWQ @ http://sglang:30000/v1)
        ↓ (failure)
   Ollama (scarlix-ollama → ollama/qwen2.5:3b @ http://ollama-agent:11434)
        ↓ (failure)
   BeeLlama (scarlix-beellama → openai/Qwen3-14B-Q4_K_M.gguf @ http://beellama:8080/v1)
```

vLLM is excluded from LiteLLM because it is `.experimental`-gated. The SMG (`ai/smg/config.yaml`) does its own latency-aware routing with `fallback_chain: [sglang-main, ollama-agent, beellama-cpu]`.

## Dashboard data flow

The 2D dashboard (`scarlihq/cmd/scarlihq/frontend/dist/index.html`, go:embed) drives everything from one JSON file written every 5s:

1. `scarlix-host-bridge.timer` fires (systemd, `OnUnitActiveSec=5s`).
2. `scarlix-host-bridge` (root, oneshot) reads `desired-mode` (via `scarlix-bridge-reader`), dispatches `scarlix-mode`, then queries `nvidia-smi` (timeout 10s) + `docker ps -a` (timeout 10s) + `df -m /models`.
3. Python3 serializes `host-status.json` to a mktemp file, atomically `mv` to `/var/lib/scarlix/host-status.json` (mode 644), fail-closed on error.
4. ScarliHQ container reads `host-status.json` (ro mount) → served by `/api/status`, `/api/gpu`, `/api/containers`, `/api/mode`.
5. Browser opens `/ws?ticket=<one-time>` → receives `host-status.json` snapshot every 2s (max 16 concurrent WS clients).

## Network surface (bound to 127.0.0.1 unless noted)

| Port | Service | Source |
|---|---|---|
| 127.0.0.1:8090 | ScarliHQ (REST + WS + MCP) | `scarlihq/docker-compose.yml` |
| 127.0.0.1:30000 | SGLang | `ai/sglang/docker-compose.yml` |
| 127.0.0.1:8089 | vLLM (host port → container 8000) | `ai/vllm/docker-compose.yml` |
| 127.0.0.1:11435 | Ollama (host port → container 11434) | `ai/ollama/docker-compose.yml` |
| 127.0.0.1:11438 | BeeLlama (host port → container 8080) | `ai/llamacpp/docker-compose.yml` |
| 127.0.0.1:4001 | LiteLLM (host port → container 4000) | `ai/litellm/docker-compose.yml` |
| 127.0.0.1:4000 | SMG | `ai/smg/docker-compose.yml` |
| 127.0.0.1:8002 | Whisper STT | `voice/whisper/docker-compose.yml` |
| 100.64.0.1:3001 | Grafana (Headscale-only) | `monitoring/docker-compose.yml` |

> **Note:** `docs/ARCHITECTURE.md` and `AGENTS.md` reference SMG on `:4002`. The actual compose file binds SMG to host port `4000`. This is a known doc drift, not a runtime bug — flag for the main agent.

## Profiles (Layer 5)

| Profile | File | Token budget | Allowed modes | Theme |
|---|---|---|---|---|
| zmor | `profiles/zmor.yaml` | unlimited | ai, game, turbo, offline | iron-man |
| hugo | `profiles/hugo.yaml` | 100k | ai, game | gaming |
| xox | `profiles/xox.yaml` | 10k | ai | creative (kids-safe) |
| mon | `profiles/mon.yaml` | 50k | ai, game | elegant |

> **Baseline:** This document is part of the v19.0.6 release baseline freeze. Do not modify content without a version bump.
