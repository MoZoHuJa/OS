# SCARLIX OS v19 — Architecture

> **v19.0.2 note:** This document was previously stale (described the abandoned
> v12 Ubuntu-ISO workflow). It has been rewritten to reflect the actual v19
> EndeavourOS/Arch `git clone + bash install.sh` architecture. For the
> step-by-step install procedure see `README.md`; for the obsolete v12 Ubuntu
> guide see `docs/archive/`.

## Install Model (v19+)

- **Base:** EndeavourOS (near-vanilla Arch). NO ISO — `git clone` + `bash install.sh`.
- **Installer:** 5 phases — packages → NVIDIA → Docker → copy files → wizard + ScarliHQ build.
- **Kernel:** `linux` + `linux-lts` (fallback). BTRFS + Snapper. ZRAM min(ram/2, 16GB). Pipewire.
- **GPU:** NVIDIA driver + CUDA 12.8 (`cu128` container images) + nvidia-container-toolkit.

## 5-Layer Architecture

### Layer 1: Inference + Base OS
- **Base OS:** EndeavourOS (Arch rolling) + Docker
- **NVIDIA:** Driver (linux driver) + CUDA 12.8 (`cu128` images) + Container Toolkit
- **SGLang** (GPU 0, `--disable-flashinfer`): Primary inference, AWQ, Qwen3-14B-AWQ
- **vLLM** (GPU 1, TP=1, `.experimental` only): High-throughput, experimental
- **BeeLlama** (CPU, llama.cpp): Offline fallback, GGUF Q4_K_M
- **Ollama** (CPU): Tertiary fallback, starter model qwen2.5:3b

### Layer 2: Workspace + Infrastructure
- **Buzz** (Nostr relay): Signed audit trail, channels, git events
- **LiteLLM** (gateway `:4001`): Unified OpenAI-compatible API, 3-tier failover (SGLang → Ollama → BeeLlama; vLLM excluded — experimental only)
- **SMG** (Scarlix Model Gateway, `:4002`, profile-gated): Latency-aware routing with `fallback_chain: [sglang-main, ollama-agent, beellama-cpu]`
- **Headscale** (VPN): WireGuard mesh, MagicDNS
- **Caddy** (proxy): Auto-HTTPS, reverse proxy
- **CrowdSec** (WAF): Intrusion prevention

### Layer 3: Agents
- **Hermes** (CEO): Multi-platform agent gateway
- **OpenCode** (Coding Manager): Build/plan agents
- **gstack**: Senior manager tools (Designer, Eng Manager, CFO, Security, etc.)

### Layer 4: ScarliHQ (Host-Bridge)
- **Go binary** (`:8090`) with go:embed 2D dashboard, token auth
- **REST API** + **WebSocket** + **MCP Server**
- **Profile Manager** (4 profiles: Zmor/Hugo/XOX/Mon)
- **Guard** (destructive_command_guard)
- **Memory** (SQLite + cosine similarity)
- **Model Swap** (SGLang ↔ Ollama ↔ BeeLlama via scarlix-mode)
- **Secure Host-Bridge architecture:** ScarliHQ container has NO docker.sock, NO nvidia runtime, NO scarlix-mode mount. Desired mode written to `bridge-input/` (UID 65532, mode 700); root `scarlix-host-bridge` (5s timer) reads it via `scarlix-bridge-reader` (Go, `O_NOFOLLOW` + fstat — no TOCTOU).

### Layer 5: Family (Profiles)
- **Zmor** (admin): Unlimited, Iron Man theme, all modes
- **Hugo** (son): 100k tokens/month, Gaming theme, game+ai
- **XOX** (daughter): 10k tokens, Creative theme, ai only, kids-safe
- **Mon** (wife): 50k tokens, Elegant theme, ai+game

## GPU Arbitration (scarlix-mode)

| Mode | GPU 0 | GPU 1 | CPU | Notes |
|------|-------|-------|-----|-------|
| ai | SGLang (AWQ) | vLLM (TP=1, `.experimental` only) | BeeLlama + Ollama | Default working path |
| stop | — | — | — | All inference stopped |
| creative | SGLang | — | BeeLlama | + ComfyUI / Video (Wan2GP) |
| game | Sunshine | — | — | Gaming streaming |
| turbo | SGLang | SGLang | — | Max throughput (2 GPU) |
| tv | — | — | — | TV dashboard only |
| offline | — | — | BeeLlama | No GPU, CPU-only fallback |

## Inference Failover (scarlix-mode direct AI path — 4-tier)

```
Request → scarlix-mode direct path
              ↓
         SGLang (GPU 0, AWQ)        ← primary
              ↓ (if fail)
         vLLM (GPU 1, .experimental) ← high-throughput (skipped if no .experimental)
              ↓ (if fail)
         Ollama (CPU)               ← fallback
              ↓ (if fail)
         BeeLlama (CPU, GGUF)       ← offline last resort
```

## LiteLLM Gateway failover (external clients — 3-tier, vLLM excluded)

```
Request → LiteLLM :4001 (OpenAI-compatible)
              ↓
         SGLang (scarlix-default)   ← primary
              ↓ (if fail)
         Ollama (scarlix-ollama)    ← fallback
              ↓ (if fail)
         BeeLlama (scarlix-beellama) ← offline
```

vLLM is excluded from the LiteLLM 3-tier because it is experimental (`.experimental` flag only). scarlix-mode's direct AI path keeps the full 4-tier including vLLM.
