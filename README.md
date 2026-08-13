# SCARLIX OS v15 — Sovereign Agent Compute Edition

> Self-contained home OS for AI cloud, coding, gaming, creative, and family entertainment.
> Runs on 2 physical servers, 100% offline-capable, fully sovereign and self-hosted.
> **Model-Agnostic:** Supports any HuggingFace model (Llama, Qwen, Mistral, Gemma, Phi, DeepSeek, etc.)

**Version:** v15.0.0-FINAL | **Date:** August 2026 | **Author:** MoZoHuJa
**License:** MIT

---

## 🏗️ Architecture — 5 Layers

```
┌─────────────────────────────────────────────────────────────┐
│  LAYER 5: FAMILY (4 profiles)                              │
│  Zmor (admin) · Hugo (son) · XOX (daughter) · Mon (wife)  │
│  Access: 2D Dashboard · Telegram · Voice · TV (Moonlight)  │
├─────────────────────────────────────────────────────────────┤
│  LAYER 4: SCARLIHQ (Go binary — OS brain)                  │
│  2D Dashboard · REST/WS API · MCP Server (15 tools)        │
│  Profile Manager · Guard · Memory · scarlix-mode (6 módy)  │
├─────────────────────────────────────────────────────────────┤
│  LAYER 3: AGENTS                                           │
│  CEO: Hermes (Python) + SOUL.md persona                    │
│  Coding Mgr: OpenCode (TypeScript)                         │
│  Needle2 Router: 26M params, <50ms intent routing          │
├─────────────────────────────────────────────────────────────┤
│  LAYER 2: WORKSPACE + INFRA                                │
│  Buzz (Nostr relay) · smg (Rust gateway)                   │
│  Headscale (VPN) · Caddy · Authelia · CrowdSec · Wazuh    │
├─────────────────────────────────────────────────────────────┤
│  LAYER 1: INFERENCE + BASE OS                              │
│  SGLang (GPU 0, AWQ) · Ollama ×2 (GPU 0+1)                │
│  llama.cpp (CPU) · Needle2 (GPU 1)                         │
│  ComfyUI + Video API + MusicGen (creative mode)            │
│  Ubuntu 24.04 LTS · Docker 27 · NVIDIA 570-open            │
└─────────────────────────────────────────────────────────────┘
```

## 🎮 6 GPU Modes (scarlix-mode)

| Mode | GPU 0 (16GB) | GPU 1 (16GB) | Use case |
|------|-------------|-------------|----------|
| `ai` | SGLang AWQ + Ollama | Ollama + Whisper + Needle | Daily operation |
| `game` | Sunshine (stream) | Ollama + Whisper | Gaming |
| `creative` | ComfyUI + Video + Music | Ollama + Whisper | Creative |
| `turbo` | SGLang + MTP | Ollama dual | Dual inference |
| `offline` | — (CPU only) | — | WW3 scenario |
| `tv` | Sunshine + Kiosk | Ollama | TV dashboard |

## 🧠 Model-Agnostic System

Edit `/etc/scarlix/models.yaml` to use ANY HuggingFace model:

```yaml
sglang:
  model_path: "/models/Qwen3-14B-Instruct-AWQ"  # or Llama, Mistral, Gemma, Phi, DeepSeek
  quantization: "awq"
  context_length: 32768

ollama_main:
  model: "qwen3.6:14b"  # or llama3.1:8b, mistral:7b, gemma2:9b
```

Then run: `bash /opt/scarlix/scripts/download-models.sh`

## 📦 Components (30+ containers)

### Main Server (14+ containers)
scarlihq, sglang, ollama-main, ollama-agent, llamacpp, needle-router, smg, comfyui, video-api, musicgen, hermes, buzz-relay + postgres/redis/minio, whisper, piper, wakeword, jarvis, caddy, authelia, headscale, crowdsec, wazuh, victoria-metrics, grafana, otel, openlit, alertmanager, sunshine, steam-headless, retroarch, minecraft, jellyfin

### HP Agent (7 containers)
gitea, coolify, nextcloud + mariadb, photoprism, n8n, postgres, redis, node-exporter, media-tools

## 🚀 Quick Start

### Option A: ISO Installer (recommended)
1. Download V15 Master Manifest PDF
2. Give it to Claude AI to build ISO
3. Write ISO to USB → boot → select PC type → 30 min → done

### Option B: Manual installation
```bash
git clone https://github.com/MoZoHuJa/OS.git
cd OS
cp .env.template .env  # Fill in secrets
sudo bash base-os/install-base.sh
# Then follow docs/COMPLETE_INSTALL_GUIDE.md
```

## 📁 Repository Structure

```
OS/
├── base-os/           # OS configuration (netplan, sudoers, fail2ban)
├── docker/            # Docker daemon config, networks, socket proxy
├── ai/                # SGLang, Ollama, llama.cpp, smg, Needle, ComfyUI, Video, MusicGen
├── workspace/         # Buzz Nostr relay
├── agents/            # Hermes + OpenCode configs
├── voice/             # Whisper + Piper + WakeWord + Jarvis
├── network/           # Headscale VPN + Caddy + Authelia + CrowdSec + Wazuh
├── monitoring/        # VictoriaMetrics + Grafana + OTel + OpenLIT + Alertmanager
├── hp-agent/          # HP Agent dev stack (7 containers)
├── scarlihq/          # Go binary source (8 packages)
├── profiles/          # 4 user profiles (Zmor/Hugo/XOX/Mon)
├── scripts/           # scarlix-mode, backup, install, download-models
├── docs/              # Architecture, hardware, network, troubleshooting, V15 manifest
└── .github/           # CI (add manually)
```

## 📚 Documentation

- [V15 Final Master Manifest](docs/V15_FINAL_MASTER_MANIFEST.md) — **COMPLETE** build guide (105KB, 3097 lines, 144 code blocks)
- [Complete Install Guide](docs/COMPLETE_INSTALL_GUIDE.md) — Step-by-step BIOS to TV dashboard
- [Architecture](docs/ARCHITECTURE.md)
- [Hardware Requirements](docs/HARDWARE.md)
- [Network Topology](docs/NETWORK.md)
- [Troubleshooting](docs/TROUBLESHOOTING.md)

## 🔒 Security

- UFW with `iptables:false` (Docker bypass fix)
- CrowdSec WAF + Wazuh SIEM (intrusion prevention)
- Authelia SSO + 2FA (TOTP + WebAuthn)
- Docker Socket Proxy (anti-root-escape)
- AGENTS.md guard (blocks `rm -rf /`, requires HITL)
- Buzz Nostr signed events (complete audit trail)
- Log rotation (journald + logrotate)

## 🌐 Offline-First

All services work without internet. Only model downloads require HuggingFace access.
