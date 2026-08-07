# SCARLIX OS v12 — Architecture

## 5-Layer Architecture

### Layer 1: Inference + Base OS
- **Base OS:** Ubuntu 24.04 LTS + Docker 27.x
- **NVIDIA:** Driver 570-open + CUDA 12.4 + Container Toolkit
- **SGLang** (GPU 0): Primary inference, safetensors, Qwen3-14B
- **Ollama** (GPU 1): Concurrent/fallback, GGUF, qwen3.6:14b
- **llama.cpp** (CPU): Offline fallback, GGUF Q4_K_M

### Layer 2: Workspace + Infrastructure
- **Buzz** (Nostr relay): Signed audit trail, channels, git events
- **LiteLLM** (gateway): Virtual keys, rate limits, 3-tier failover
- **Headscale** (VPN): WireGuard mesh, MagicDNS
- **Caddy** (proxy): Auto-HTTPS, reverse proxy
- **Authelia** (SSO): TOTP + WebAuthn 2FA
- **CrowdSec** (WAF): Intrusion prevention

### Layer 3: Agents
- **Hermes** (CEO): Python, 223k stars, multi-platform gateway, self-improving
- **OpenCode** (Coding Manager): TypeScript, 191k stars, build/plan agents
- **gstack**: 23 senior manager tools (Designer, Eng Manager, CFO, Security, etc.)

### Layer 4: ScarliHQ
- **Go binary** with go:embed 2D dashboard
- **REST API** + **WebSocket** + **MCP Server**
- **Profile Manager** (4 profiles: Zmor/Hugo/XOX/Mon)
- **Guard** (destructive_command_guard)
- **Memory** (SQLite + cosine similarity)
- **Model Swap** (SGLang ↔ Ollama ↔ llama.cpp)

### Layer 5: Family
- **Zmor** (admin): Unlimited, Iron Man theme, all modes
- **Hugo** (son): 100k tokens/month, Gaming theme, game+ai
- **XOX** (daughter): 10k tokens, Creative theme, ai only, kids-safe
- **Mon** (wife): 50k tokens, Elegant theme, ai+game

## GPU Arbitration (scarlix-mode)

| Mode | GPU 0 (16GB) | GPU 1 (16GB) | VRAM Total |
|------|-------------|-------------|------------|
| ai | SGLang 12GB | Ollama 8GB + Whisper 2GB | 22GB |
| game | Sunshine 2GB | Ollama 8GB + Whisper 2GB | 12GB |
| turbo | SGLang 12GB | SGLang 12GB | 24GB |
| offline | — | — | 0 (CPU) |

## 3-Tier Inference Failover

```
Request → LiteLLM Gateway
              ↓
         SGLang (GPU 0)  ← primary
              ↓ (if fail)
         Ollama (GPU 1)  ← fallback
              ↓ (if fail)
         llama.cpp (CPU) ← offline
```
