# SCARLIX OS v17.3 — EndeavourOS Edition (Bootstrap Installer)

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **NO ISO NEEDED** — install on clean EndeavourOS with one command.
> **Auto-detect:** 1 GPU → 2-tier · 2+ GPU → 5-tier (vLLM TP=2)

**Version:** v17.3.0 | **Base:** EndeavourOS (Arch) | **License:** MIT

## 🚀 Quick Install (NO ISO needed!)

### Option 1: One-liner (curl + pipe)
```bash
curl -fsSL https://raw.githubusercontent.com/MoZoHuJa/OS/main/install.sh | bash
```

### Option 2: Safer (clone + review + run) ⭐ recommended
```bash
git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
cd ~/scarlix-os
nano install.sh   # review what it does
bash install.sh
```

### What you need first:
1. **Clean EndeavourOS installation** — download from https://endeavouros.com/
2. **Boot it, open terminal**
3. **Run the install command above**

The installer does everything: packages, BTRFS subvols, NVIDIA, Docker, AI stacks, wizard.

---

## 🆕 What's New in v17.3 (vs v17.2.1)

**Bootstrap installer — no ISO build required!**

| Feature | v17.2.1 (ISO) | v17.3 (Bootstrap) |
|---------|---------------|-------------------|
| Install method | Boot from USB ISO | `curl \| bash` or `git clone + bash` |
| ISO build needed | ✅ Yes (needs Arch host + mkarchiso) | ❌ No |
| Download size | 2-4GB ISO | ~50MB repo |
| Updates | Rebuild ISO | `git pull && bash install.sh` |
| Debug | Hard (rebuild ISO) | Easy (edit + re-run) |
| Idempotent | Via first-boot.sh | ✅ Via checkpoints in install.sh |

### What `install.sh` does (5 phases):
1. **Phase 1**: System packages (pacman), BTRFS subvols, Snapper, ZRAM
2. **Phase 2**: NVIDIA auto-detect (dGPU-only, Turing+) + atomic nvidia-open-lts + GRUB modeset
3. **Phase 3**: Docker + Docker repo (nvidia-container-toolkit) + yay (git clone)
4. **Phase 4**: Copy SCARLIX scripts, services, stacks to `/usr/local/bin/`, `/etc/scarlix/`, `/opt/scarlix/`
5. **Phase 5**: Run wizard (TUI: PC type, models, experimental) + 3-phase setup

**Idempotent**: Re-running `install.sh` skips completed phases (checkpoints in `/var/lib/scarlix/`).

---

## 🎯 Target Hardware

### Dual NVIDIA GPU (recommended — 5-tier auto-enabled)
```
GPU 0: RTX 5060 Ti 16GB → SGLang + vLLM TP shard 0
GPU 1: RTX 4060 Ti 16GB → vLLM TP shard 1 + Ollama + Laya
CPU:   BeeLlama.cpp (offline, KVarN, 32k context)
```
**Result**: 32GB total VRAM. vLLM TP=2 enables Qwen3-72B (36GB → 18GB per GPU). Multi-LoRA lets Hermes/omp/Voice share base model.

### Single NVIDIA GPU (2-tier default)
```
GPU 0: 16GB → SGLang + Ollama
CPU:   BeeLlama.cpp (offline)
```

### No GPU (Dev Workstation)
Coding + files only. No AI inference.

---

## 🏗️ Inference Architecture

### 2-Tier Default (1 GPU)
```
Tier-1: SGLang (agents, RadixAttention)
Tier-3: Ollama (GGUF concurrent)
Tier-4: BeeLlama.cpp (offline, KVarN, 32k context)
```

### 5-Tier (2+ GPU — auto-suggested by wizard)
```
Tier-1: SGLang (GPU 0 — agents, RadixAttention)
Tier-2: vLLM (GPU 0+1 TP=2 — Multi-LoRA, batch, Qwen3-72B)
Tier-3: Ollama ×2 (GPU 0+1 — GGUF concurrent)
Tier-4: BeeLlama.cpp (CPU — offline, KVarN, 32k context)
Tier-5: FreeToken (GPU 0+1 — MoE, --profile moe, experimental)
System-1: Laya (GPU 1 — 33ms router, replaces Needle2)
Browser: Playwright MCP (Apache-2.0, browser automation)
Coding: omp / oh-my-pi (LSP+DAP, replaces OpenCode)
```

---

## 🎮 GPU Modes

```bash
scarlix-mode ai        # All tiers (5-tier if experimental, else 2-tier)
scarlix-mode turbo     # SGLang + BeeLlama + Ollama (max throughput)
scarlix-mode offline   # BeeLlama.cpp CPU (KVarN, 32k context)
scarlix-mode game      # Native Steam/Sunshine
scarlix-mode creative  # ComfyUI + Video + Music
scarlix-mode tv        # Docker Sunshine
scarlix-mode vram      # VRAM health check
scarlix-mode status    # System summary (shows tier + GPU count)
```

---

## 🔧 Manual Experimental Toggle

```bash
# Enable 5-tier (vLLM/FreeToken/Laya) — for 2+ GPU
sudo touch /etc/scarlix/.experimental
sudo systemctl restart scarlix-first-boot.service

# Disable (back to 2-tier)
sudo rm /etc/scarlix/.experimental
```

---

## 🔄 Re-run / Update

```bash
cd ~/scarlix-os
git pull
bash install.sh   # idempotent — skips completed phases
```

To force full reinstall:
```bash
sudo rm -rf /var/lib/scarlix/.checkpoint-* /opt/scarlix/.installed
bash install.sh
```

---

## 🔧 NVIDIA Rollback

```bash
# Option 1: rollback driver (downgrade installed via yay in install.sh)
sudo downgrade nvidia-open

# Option 2: boot LTS kernel (nvidia-open-lts installed atomically)
sudo grub-set-default 1
sudo reboot
```

---

## 📁 File Layout (v17.3)

```
OS/  (repo root)
├── install.sh                              # NEW v17.3: Bootstrap installer (no ISO)
├── README.md                               # This file
├── AGENTS.md                               # 5-tier + omp delegation protocol
├── models.yaml                             # Model-agnostic config
├── scarlix/                                # archiso profile (for ISO build, optional)
│   ├── profiledef.sh                       # v17.3.0
│   ├── packages.x86_64                     # Package list (install.sh uses this)
│   ├── pacman.conf                         # +[docker] repo
│   └── airootfs/
│       ├── etc/calamares/modules/mount.conf  # @ + @home only
│       ├── etc/systemd/
│       │   ├── zram-generator.conf         # min(ram/2, 16384)
│       │   └── system/
│       │       ├── first-boot.sh            # 3-phase checkpointed
│       │       ├── scarlix-first-boot.service
│       │       ├── model-manager.{service,timer}
│       ├── etc/pacman.d/hooks/
│       │   └── scarlix-docker-backup.*     # Pre-rollback Docker backup
│       └── usr/local/bin/
│           ├── scarlix-wizard              # Auto-detect GPU → suggest tier
│           ├── scarlix-mode               # 2-tier or 5-tier
│           └── model-manager.sh            # HF auto + Ollama --apply-ollama
├── ai/                                     # Docker stacks
│   ├── vllm/docker-compose.yml             # profiles:[experimental] — TP=2
│   ├── laya/docker-compose.yml             # profiles:[experimental]
│   ├── freetoken/docker-compose.yml        # profiles:[moe]
│   ├── llamacpp/docker-compose.yml         # BeeLlama.cpp (default Tier-4)
│   ├── sglang/                             # SGLang (default Tier-1)
│   ├── ollama/                             # Ollama (default Tier-3)
│   ├── browser-mcp/                         # Playwright MCP
│   └── smg/                                 # Gateway
├── installer/scripts/build-iso.sh          # v17.3.0 (optional ISO build)
├── tests/qemu-boot.sh                      # v17.3.0
└── docs/AGENTVERSE_MERGE_PLAN.md           # Sister project merge plan
```

---

## 📜 Version History

| Version | Date | Key Changes |
|---------|------|-------------|
| **v17.3** | 2026-10 | **Bootstrap installer (install.sh) — NO ISO NEEDED. One-liner: `curl \| bash`. Idempotent 5-phase setup. Auto-detect GPU.** |
| v17.2.1 | 2026-10 | Auto 5-tier for 2+ NVIDIA GPU. Wizard auto-suggests. RTX 5060 Ti + RTX 4060 Ti optimized. |
| v17.2 | 2026-10 | Minimal Working Baseline. 8 P0 fixes (yay removed, Calamares stripped, NVIDIA dGPU detect, atomic nvidia-open-lts, checkpoint resume, Docker repo, model opt-in). 2-tier default. |
| v17.1 | 2026-09 | 5-tier inference (vLLM+BeeLlama+FreeToken+Laya+omp). 10 review fixes. (Over-engineered) |
| v17.0 | 2026-09 | Garuda → EndeavourOS re-base. Profile renamed `scarlix/`. |
| v16.5 | 2026-08 | 10 review fixes (PIPESTATUS, OVMF, console=ttyS0, etc.) |
| v16.1 | 2026-07 | Garuda Linux, BTRFS+Snapper, native gaming |
| v15 | 2026-06 | Model-Agnostic system |

---

## ⚠️ Known Limitations

- **No CI/CD**: install.sh not tested automatically (manual testing only)
- **No real HW test**: QEMU doesn't test NVIDIA/CUDA/Sunshine/HDMI-CEC
- **Single maintainer**: One person maintaining the full stack
- **SGLang on Arch**: Officially Ubuntu-only, AUR package has packaging gaps
- **vLLM TP=2**: Requires both GPUs to be same architecture family (RTX 40+50 series OK)

## 🗺️ Roadmap

- **v17.4**: AgentVerse merge (944-app store, capabilities, cloudd side-car)
- **v18.0**: Incus dev workspaces, ScarliHQ Rust refactor consideration
