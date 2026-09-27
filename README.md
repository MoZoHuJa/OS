# SCARLIX OS v17.2 — EndeavourOS Edition (Minimal Working Baseline)

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Base:** EndeavourOS (near-vanilla Arch)
> **2-Tier Default:** SGLang + BeeLlama.cpp (vLLM/FreeToken/Laya = opt-in experimental)

**Version:** v17.2.0 | **Base:** EndeavourOS (Arch) | **License:** MIT

## 🎯 What's New in v17.2 (vs v17.1)

**v17.2 is a correction release.** v17.1 was over-engineered — 5-tier inference + 7 new integrations broke the basic ISO build path. v17.2 strips back to a **minimal working baseline** that actually builds and installs.

### 🔴 P0 Fixes (ISO build + install blockers)

| # | Fix | v17.1 Problem | v17.2 Solution |
|---|-----|---------------|-----------------|
| Q1a | **yay removed from ISO** | `yay` is AUR → `mkarchiso` failed "target not found" | Removed from `packages.x86_64`; first-boot installs via `git clone https://aur.archlinux.org/yay.git && makepkg -si` |
| Q2b | **Calamares mount.conf stripped** | 8 subvols in mount.conf crashed Calamares on existing subvols | Only `@` and `@home` in mount.conf; specialized subvols created by first-boot **empty** → `chattr +C` works |
| Q3a | **2-tier default** | 5-tier (9GB ISO, OOM on 16GB VRAM) | Default: SGLang + BeeLlama.cpp; vLLM/FreeToken/Laya behind `--profile experimental` |
| Q4b | **Checkpoint resume works** | `ConditionPathExists` blocked re-run after crash | Removed condition; added `Restart=on-failure` + `RestartSec=30s` |
| Q5a | **NVIDIA dGPU-only detect** | `lspci` returned 2 cards on hybrid laptops → installed both drivers → black screen | Filter: `lspci -nn \| grep -iE 'NVIDIA.*(VGA\|3D)'` (ignores Intel iGPU) |
| Q6a | **Atomic NVIDIA install** | linux-lts + nvidia-open-lts version mismatch → unbootable | Both installed in ONE `pacman -S` call (atomic dependency resolution) |
| Q7a | **CoW on empty subvols** | chattr +C on non-empty dirs (Calamares had written data) → Docker overlay2 broke | Subvols created empty in Phase 1 → chattr +C before any data |
| Q8a | **AUR optional + Docker repo** | AUR in first-boot fragile (network/DNS) | `nvidia-container-toolkit` from Docker official repo (added to pacman.conf); model download opt-in `--skip-models` |

### 📦 2-Tier Default Inference (NEW in v17.2)

```
┌─────────────────────────────────────────────────┐
│  DEFAULT (starts on first-boot):                │
│  Tier-1: SGLang (agents, RadixAttention)        │
│  Tier-3: Ollama (GGUF concurrent)              │
│  Tier-4: BeeLlama.cpp (offline, KVarN, 32k ctx) │
├─────────────────────────────────────────────────┤
│  EXPERIMENTAL (opt-in via wizard):             │
│  Tier-2: vLLM (Multi-LoRA, TP=2, needs 2 GPU)  │
│  Tier-5: FreeToken (MoE, --profile moe)         │
│  System-1: Laya (33ms router)                   │
└─────────────────────────────────────────────────┘
```

### 🔧 Wizard Options (NEW)

The setup wizard now asks:
1. **PC Type**: Auto-detected (NVIDIA dGPU → AI Server, else Dev Workstation)
2. **Model Download**: Skip (default, faster) or Download (50-150GB, hours)
3. **Experimental Mode**: No (default, 2-tier) or Yes (vLLM/FreeToken/Laya)

### ✅ Kept from v17.1 (all good engineering)

- archiso-based build, model-agnostic models.yaml
- 6 GPU modes (ai/game/creative/turbo/offline/tv) + vram
- Pipewire audio, BTRFS+Snapper, ZRAM min(ram/2, 16384)
- NVIDIA auto-detect (Turing+ → nvidia-open, older → proprietary)
- nvidia-open-lts for LTS kernel fallback
- 3-phase checkpointed first-boot
- Docker volume backup pacman hook (pre-rollback safety)
- model-manager.sh (HF auto + Ollama --apply-ollama manual)
- omp (oh-my-pi) coding agent (replaces OpenCode)
- Playwright MCP (browser automation)
- AgentVerse merge plan (docs/AGENTVERSE_MERGE_PLAN.md)

## 🚀 Quick Start

### Build ISO:
```bash
git clone https://github.com/MoZoHuJa/OS.git
cd OS && git checkout v17.2
sudo pacman -S archiso edk2-ovmf qemu-desktop
bash installer/scripts/build-iso.sh
```

### Install:
1. Write ISO to USB → boot → Calamares (BTRFS, only `@` + `@home`)
2. Wizard: auto-detects GPU → AI Server / Dev Workstation
3. Choose: Skip models (default) or Download
4. Choose: 2-tier default (recommended) or Experimental
5. Reboot → 3-phase first-boot (auto-resume on crash via Restart=on-failure):
   - Phase 1: BTRFS subvols (empty) + Snapper + CoW + ZRAM
   - Phase 2: NVIDIA dGPU detect + atomic nvidia-open + nvidia-open-lts + GRUB modeset
   - Phase 3: Docker + yay (git clone) + 2-tier services + omp
6. Dashboard: http://192.168.1.100:8090

## 🎮 GPU Modes

```bash
scarlix-mode ai        # 2-tier: SGLang + BeeLlama + Ollama (default)
scarlix-mode turbo     # SGLang + BeeLlama + Ollama (max throughput)
scarlix-mode offline   # BeeLlama.cpp CPU (KVarN, 32k context)
scarlix-mode game      # Native Steam/Sunshine
scarlix-mode creative  # ComfyUI + Video + Music
scarlix-mode tv        # Docker Sunshine
scarlix-mode vram      # VRAM health check
scarlix-mode status    # System summary
```

## 🔧 NVIDIA Rollback

```bash
# Option 1: rollback driver (downgrade installed via yay in first-boot)
sudo downgrade nvidia-open

# Option 2: boot LTS kernel (nvidia-open-lts already installed atomically)
sudo grub-set-default 1
sudo reboot
```

## 🧪 Enable Experimental Mode (vLLM/FreeToken/Laya)

If you have 2+ GPUs with 32GB+ VRAM and want the full 5-tier stack:

```bash
# During install: choose "Yes" for Experimental in wizard
# OR post-install:
sudo touch /etc/scarlix/.experimental
sudo systemctl restart scarlix-first-boot.service
```

## 📁 File Layout (v17.2)

```
scarlix/
├── profiledef.sh                          # v17.2.0, SCARLIX_V172
├── packages.x86_64                        # yay REMOVED (was breaking build)
├── pacman.conf                            # +[docker] repo for nvidia-container-toolkit
├── airootfs/
│   ├── etc/calamares/modules/
│   │   ├── mount.conf                     # Q2b: ONLY @ + @home (was 8 subvols)
│   │   └── partition.conf
│   ├── etc/systemd/
│   │   ├── zram-generator.conf            # min(ram/2, 16384)
│   │   └── system/
│   │       ├── first-boot.sh              # Q5a+Q6a+Q7a+Q8a rewrite
│   │       ├── scarlix-first-boot.service # Q4b: Restart=on-failure (no ConditionPathExists)
│   │       ├── model-manager.{service,timer}
│   ├── etc/pacman.d/hooks/
│   │   ├── scarlix-docker-backup.hook     # Pre-rollback Docker backup
│   │   └── scarlix-docker-backup.sh
│   └── usr/local/bin/
│       ├── scarlix-wizard                 # +--skip-models +--experimental
│       ├── scarlix-mode                   # 2-tier default
│       └── model-manager.sh
├── efiboot/loader/entries/archiso-x86_64.conf
└── syslinux/syslinux.cfg

ai/
├── vllm/docker-compose.yml                # profiles:[experimental]
├── laya/docker-compose.yml               # profiles:[experimental]
├── freetoken/docker-compose.yml          # profiles:[moe] (already)
├── llamacpp/docker-compose.yml           # BeeLlama.cpp (default Tier-4)
├── sglang/                                # SGLang (default Tier-1)
├── ollama/                                # Ollama (default Tier-3)
├── browser-mcp/                           # Playwright MCP
└── smg/                                   # Gateway

models.yaml                               # 2-tier default + experimental sections
installer/scripts/build-iso.sh            # v17.2.0
tests/qemu-boot.sh                        # v17.2.0
docs/AGENTVERSE_MERGE_PLAN.md             # From v17.1 (sister project analysis)
```

## 📜 Version History

| Version | Date | Key Changes |
|---------|------|-------------|
| **v17.2** | 2026-10 | **Correction release. 2-tier default (SGLang+BeeLlama). yay removed from ISO. Calamares mount.conf stripped to @ + @home. NVIDIA dGPU-only detect. Atomic nvidia-open-lts install. Checkpoint resume (Restart=on-failure). Model download opt-in.** |
| v17.1 | 2026-09 | 5-tier inference (vLLM+BeeLlama+FreeToken+Laya+omp). 10 review fixes. (Over-engineered — ISO build broken) |
| v17.0 | 2026-09 | Garuda → EndeavourOS re-base. Profile renamed `scarlix/`. |
| v16.5 | 2026-08 | 10 review fixes (PIPESTATUS, OVMF, console=ttyS0, etc.) |
| v16.4 | 2026-08 | VERSION consistency, OVMF CODE/VARS split, QEMU PASS/FAIL |
| v16.1 | 2026-07 | Garuda Linux, BTRFS+Snapper, native gaming |
| v15 | 2026-06 | Model-Agnostic system |
| v12 | 2026-05 | Sovereign Agent Compute Edition |

## ⚠️ Known Limitations (v17.2)

- **No CI/CD**: ISO build not tested automatically (manual QEMU only)
- **No real HW test**: QEMU doesn't test NVIDIA/CUDA/Sunshine/HDMI-CEC
- **Single maintainer**: One person maintaining the full stack
- **SGLang on Arch**: Officially Ubuntu-only, AUR package has packaging gaps
- **Experimental tier**: vLLM/FreeToken/Laya are NOT production-tested

## 🗺️ Roadmap

- **v17.3**: AgentVerse merge (944-app store, capabilities, cloudd side-car)
- **v18.0**: Incus dev workspaces, ScarliHQ Rust refactor consideration
- **v18.1**: Single-core consolidation (Go vs Rust decision)
