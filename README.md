# SCARLIX OS v17.2.1 — EndeavourOS Edition (Auto 5-Tier for Dual GPU)

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Base:** EndeavourOS (near-vanilla Arch)
> **Auto-detect:** 1 GPU → 2-tier default · 2+ GPU → 5-tier (vLLM TP=2)

**Version:** v17.2.1 | **Base:** EndeavourOS (Arch) | **License:** MIT

## 🆕 What's New in v17.2.1 (vs v17.2)

**Auto 5-tier detection for dual-GPU systems.**

v17.2 stripped 5-tier to opt-in "experimental" for stability (reviewers said "95% have 1 GPU"). But for **dual-GPU systems** (like RTX 5060 Ti + RTX 4060 Ti = 32GB VRAM), 5-tier is the optimal configuration — vLLM TP=2 enables Qwen3-72B that won't fit on a single 16GB card.

**v17.2.1 fix:** The wizard now auto-detects GPU count and **auto-suggests** the right tier:

| GPU Count | Wizard Default | Inference Stack | Why |
|-----------|---------------|-----------------|-----|
| 0 NVIDIA | Dev Workstation | (coding only) | No GPU = no AI inference |
| 1 NVIDIA | AI Server, 2-tier | SGLang + BeeLlama | 16GB can't run TP=2 efficiently |
| **2+ NVIDIA** | **AI Server, 5-tier** | **SGLang + vLLM TP=2 + Ollama + BeeLlama + FreeToken/Laya** | **32GB+ enables Qwen3-72B, Multi-LoRA** |

User can still override (the menu shows both options, auto-detected one is pre-selected).

## 🎯 Target Hardware (Dual NVIDIA GPU)

Optimized for **2× NVIDIA 16GB GPU** configurations:

```
GPU 0 (e.g. RTX 5060 Ti 16GB — newer, faster):
├── Tier-1: SGLang AWQ (~9GB) — primary agent inference
├── Tier-2: vLLM TP=2 shard 0 — Multi-LoRA, shared with GPU 1
└── Tier-5: FreeToken MoE (if --profile moe)

GPU 1 (e.g. RTX 4060 Ti 16GB):
├── Tier-2: vLLM TP=2 shard 1 — second half of model
├── Tier-3: Ollama Agent (~9GB) — persistent agent inference
├── Whisper STT (~3GB)
└── System-1: Laya router (26M params, ~1GB)

CPU:
└── Tier-4: BeeLlama.cpp (offline, KVarN, 32k context)
```

**Result:** Qwen3-72B-AWQ (36GB) splits across both GPUs via vLLM TP=2 → 18GB per card, fits in 16GB with minor offload. Multi-LoRA lets Hermes/omp/Voice share the base model with separate adapter heads.

## 🚀 Quick Start

### Build ISO:
```bash
git clone https://github.com/MoZoHuJa/OS.git
cd OS && git checkout v17.2.1
sudo pacman -S archiso edk2-ovmf qemu-desktop
bash installer/scripts/build-iso.sh
```

### Install (dual-GPU system):
1. Write ISO to USB → boot → Calamares (BTRFS, only `@` + `@home`)
2. Wizard auto-detects: **"2 NVIDIA dGPU(s) detected → 5-tier recommended"**
3. Press Enter to accept (or override to 2-tier)
4. Choose: Skip models (default) or Download
5. Reboot → 3-phase first-boot (auto-resume on crash):
   - Phase 1: BTRFS subvols (empty) + Snapper + CoW + ZRAM
   - Phase 2: NVIDIA dGPU detect + atomic nvidia-open + nvidia-open-lts + GRUB modeset
   - Phase 3: Docker + yay (git clone) + 5-tier services + omp
6. Dashboard: http://192.168.1.100:8090

## 🏗️ Inference Architecture

### 2-Tier Default (1 GPU — stable)
```
Tier-1: SGLang (agents, RadixAttention)
Tier-3: Ollama (GGUF concurrent)
Tier-4: BeeLlama.cpp (offline, KVarN, 32k context)
```

### 5-Tier (2+ GPU — auto-suggested)
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

## 🔧 NVIDIA Rollback

```bash
# Option 1: rollback driver (downgrade installed via yay in first-boot)
sudo downgrade nvidia-open

# Option 2: boot LTS kernel (nvidia-open-lts installed atomically)
sudo grub-set-default 1
sudo reboot
```

## 🧪 Manual Experimental Toggle

If auto-detect didn't suggest 5-tier (or you want to override):

```bash
# Enable 5-tier (vLLM/FreeToken/Laya)
sudo touch /etc/scarlix/.experimental
sudo systemctl restart scarlix-first-boot.service

# Disable (back to 2-tier)
sudo rm /etc/scarlix/.experimental
# Restart relevant services or reboot
```

## 📁 File Layout (v17.2.1)

```
scarlix/
├── profiledef.sh                          # v17.2.1, SCARLIX_V1721
├── packages.x86_64                        # yay NOT in ISO (git clone in first-boot)
├── pacman.conf                            # +[docker] repo for nvidia-container-toolkit
├── airootfs/
│   ├── etc/calamares/modules/
│   │   ├── mount.conf                     # ONLY @ + @home (safe defaults)
│   │   └── partition.conf
│   ├── etc/systemd/
│   │   ├── zram-generator.conf            # min(ram/2, 16384)
│   │   └── system/
│   │       ├── first-boot.sh              # 3-phase checkpointed + auto 5-tier
│   │       ├── scarlix-first-boot.service # Restart=on-failure (no ConditionPathExists)
│   │       ├── model-manager.{service,timer}
│   ├── etc/pacman.d/hooks/
│   │   └── scarlix-docker-backup.*       # Pre-rollback Docker backup
│   └── usr/local/bin/
│       ├── scarlix-wizard                 # v17.2.1: auto-detect 2+ GPU → suggest 5-tier
│       ├── scarlix-mode                   # 2-tier or 5-tier based on .experimental
│       └── model-manager.sh               # HF auto + Ollama --apply-ollama
├── efiboot/loader/entries/archiso-x86_64.conf
└── syslinux/syslinux.cfg

ai/
├── vllm/docker-compose.yml                # profiles:[experimental] — TP=2 for dual GPU
├── laya/docker-compose.yml               # profiles:[experimental]
├── freetoken/docker-compose.yml          # profiles:[moe]
├── llamacpp/docker-compose.yml           # BeeLlama.cpp (default Tier-4)
├── sglang/                                # SGLang (default Tier-1)
├── ollama/                                # Ollama (default Tier-3)
├── browser-mcp/                           # Playwright MCP
└── smg/                                   # Gateway

models.yaml                               # 2-tier default + experimental (5-tier) sections
installer/scripts/build-iso.sh            # v17.2.1
tests/qemu-boot.sh                        # v17.2.1
docs/AGENTVERSE_MERGE_PLAN.md             # Sister project merge plan
```

## 📜 Version History

| Version | Date | Key Changes |
|---------|------|-------------|
| **v17.2.1** | 2026-10 | **Auto 5-tier for 2+ NVIDIA GPU. Wizard auto-detects GPU count → suggests 5-tier (vLLM TP=2) for dual-GPU systems. Optimized for RTX 5060 Ti + RTX 4060 Ti (32GB total VRAM).** |
| v17.2 | 2026-10 | Minimal Working Baseline. 8 P0 fixes (yay removed, Calamares stripped, NVIDIA dGPU detect, atomic nvidia-open-lts, checkpoint resume, Docker repo, model opt-in). 2-tier default. |
| v17.1 | 2026-09 | 5-tier inference (vLLM+BeeLlama+FreeToken+Laya+omp). 10 review fixes. (Over-engineered — ISO build broken) |
| v17.0 | 2026-09 | Garuda → EndeavourOS re-base. Profile renamed `scarlix/`. |
| v16.5 | 2026-08 | 10 review fixes (PIPESTATUS, OVMF, console=ttyS0, etc.) |
| v16.1 | 2026-07 | Garuda Linux, BTRFS+Snapper, native gaming |
| v15 | 2026-06 | Model-Agnostic system |

## ⚠️ Known Limitations

- **No CI/CD**: ISO build not tested automatically (manual QEMU only)
- **No real HW test**: QEMU doesn't test NVIDIA/CUDA/Sunshine/HDMI-CEC
- **Single maintainer**: One person maintaining the full stack
- **SGLang on Arch**: Officially Ubuntu-only, AUR package has packaging gaps
- **vLLM TP=2**: Requires both GPUs to be same architecture family (RTX 40+50 series OK)

## 🗺️ Roadmap

- **v17.3**: AgentVerse merge (944-app store, capabilities, cloudd side-car)
- **v18.0**: Incus dev workspaces, ScarliHQ Rust refactor consideration
