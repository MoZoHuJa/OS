# SCARLIX OS v12 — Sovereign Agent Compute Edition

> Self-contained home OS for AI cloud, coding, gaming, development, webhosting and family entertainment.
> Runs on 2 physical servers, 100% offline-capable, fully sovereign and self-hosted.

**Version:** v12.0 | **Date:** July 2026 | **Author:** MoZoHuJa
**License:** MIT

---

## 🏗️ Architecture — 5 Layers

```
┌─────────────────────────────────────────────────────────────┐
│  LAYER 5: FAMILY (4 profiles)                              │
│  Zmor (admin) · Hugo (son) · XOX (daughter) · Mon (wife)  │
│  Access: 2D Dashboard · Telegram · Voice · Mobile          │
├─────────────────────────────────────────────────────────────┤
│  LAYER 4: SCARLIHQ (Go binary — OS brain)                  │
│  2D Dashboard · REST/WS API · MCP Server · Profile Manager │
│  scarlix-mode · Guard · Memory · Model Swap · Buzz Bridge  │
├─────────────────────────────────────────────────────────────┤
│  LAYER 3: AGENTS                                           │
│  CEO: hermes-agent (Python) + gstack (23 tools)            │
│  Coding Mgr: opencode (TypeScript) + build/plan agents     │
├─────────────────────────────────────────────────────────────┤
│  LAYER 2: WORKSPACE + INFRA                                │
│  Buzz (Nostr relay) · LiteLLM (gateway) · Qdrant           │
│  Headscale (VPN) · Caddy · Authelia · CrowdSec            │
├─────────────────────────────────────────────────────────────┤
│  LAYER 1: INFERENCE + BASE OS                              │
│  SGLang (GPU 0) · Ollama (GPU 1) · llama.cpp (CPU)         │
│  Ubuntu 24.04 LTS · Docker 27.x · NVIDIA 570-open          │
└─────────────────────────────────────────────────────────────┘
```

## 📦 Components (21 containers)

### Main Server (14 containers)
| # | Container | Purpose | Port |
|---|-----------|---------|------|
| 1 | scarlihq | Orchestrator + 2D dashboard | 8090, 8091 |
| 2 | buzz-relay | Nostr workspace (audit trail) | 3000 |
| 3 | buzz-postgres | Buzz database | 5432 |
| 4 | buzz-redis | Buzz cache | 6379 |
| 5 | buzz-minio | Buzz media (S3) | 9000 |
| 6 | hermes-agent | CEO agent | 7999 |
| 7 | sglang | Inference primary (GPU 0) | 30000 |
| 8 | ollama-agent | Inference concurrent (GPU 1) | 11435 |
| 9 | litellm | LLM gateway | 4000 |
| 10 | whisper-sk | Slovak STT (GPU 1) | 8002 |
| 11 | piper-sk | Slovak TTS | 8003 |
| 12 | caddy | Reverse proxy | 80, 443 |
| 13 | authelia | SSO + 2FA | 9091 |
| 14 | headscale | VPN | 8080 |

### HP Agent (7 containers)
| # | Container | Purpose | Port |
|---|-----------|---------|------|
| 15 | opencode | Coding manager | 8001 |
| 16 | gitea | Git server | 3000 |
| 17 | coolify | PaaS | 8000 |
| 18 | nextcloud | Files | 8080 |
| 19 | photoprism | Photos | 2342 |
| 20 | postgres | Shared DB | 5432 |
| 21 | n8n | Workflows | 5678 |

## 🚀 Quick Start

### 1. Clone and configure
```bash
git clone https://github.com/MoZoHuJa/scarlix-os-v12.git
cd scarlix-os-v12
cp .env.template .env
# Edit .env with your secrets
```

### 2. Install base OS
```bash
# On Ubuntu 24.04 LTS server:
sudo bash base-os/install-base.sh
```

### 3. Setup Docker + networks
```bash
sudo cp docker/daemon.json /etc/docker/daemon.json
sudo systemctl restart docker
bash docker/networks.sh
```

### 4. Start AI stack
```bash
cd ai/sglang && docker compose up -d
cd ../ollama && docker compose up -d
cd ../litellm && docker compose up -d
```

### 5. Download models
```bash
bash scripts/download-models.sh
```

### 6. Verify
```bash
scarlix-mode status
```

## 🎮 scarlix-mode

| Mode | GPU 0 | GPU 1 | Use case |
|------|-------|-------|----------|
| `ai` | SGLang (12GB) | Ollama (8GB) + Whisper (2GB) | Daily operation |
| `game` | Sunshine (2GB) | Ollama (8GB) + Whisper (2GB) | Gaming |
| `turbo` | SGLang (12GB) | SGLang (12GB) | Dual inference |
| `offline` | — | — | llama.cpp CPU only (WW3) |

## 📁 Repository Structure

```
scarlix-os-v12/
├── base-os/           # OS configuration (netplan, sudoers, fail2ban)
├── docker/            # Docker daemon config, networks, socket proxy
├── ai/                # SGLang, Ollama, llama.cpp, LiteLLM
├── workspace/         # Buzz Nostr relay
├── agents/            # Hermes + OpenCode configs
├── voice/             # Whisper + Piper + WakeWord
├── network/           # Headscale VPN + Caddy proxy
├── security/          # CrowdSec WAF
├── monitoring/        # VictoriaMetrics + Grafana + OTel
├── hp-agent/          # HP Agent dev stack (7 containers)
├── scarlihq/          # Go binary source (orchestrator)
├── profiles/          # 4 user profiles (Zmor/Hugo/XOX/Mon)
├── scripts/           # scarlix-mode, backup, install, models
├── docs/              # Architecture, hardware, network, troubleshooting
└── .github/workflows/ # CI/CD
```

## 📚 Documentation

- [Architecture](docs/ARCHITECTURE.md)
- [Hardware Requirements](docs/HARDWARE.md)
- [Network Topology](docs/NETWORK.md)
- [Troubleshooting](docs/TROUBLESHOOTING.md)

## 🔒 Security

- UFW with `iptables:false` (Docker bypass fix)
- CrowdSec WAF (intrusion prevention)
- Authelia SSO + 2FA (TOTP + WebAuthn)
- Docker Socket Proxy (anti-root-escape)
- AGENTS.md guard (blocks `rm -rf /`, requires HITL for critical ops)
- Buzz Nostr signed events (complete audit trail)

## 🌐 Offline-First

All services work without internet. The only exception is model download from HuggingFace.
