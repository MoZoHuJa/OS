# SCARLIX OS v17.1 — EndeavourOS Edition

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Base:** EndeavourOS (near-vanilla Arch)
> **5-Tier Inference:** SGLang → vLLM → Ollama → BeeLlama.cpp → FreeToken
> **Model-Agnostic:** Supports any HuggingFace model.

**Version:** v17.1.0 | **Base:** EndeavourOS (Arch) | **License:** MIT

## 🚀 What's New in v17.1 (vs v17.0)

Based on 4 independent code reviews of v17.0 + research of 13 GitHub repos.

### Track 1: Review Fixes (10 P0-P1 issues fixed)

| # | Fix | Detail |
|---|-----|--------|
| Q1a | AUR packages removed from ISO | `snap-pac-grub`, `downgrade`, `archiso` removed; `yay` added for first-boot AUR installs |
| Q2a | `archiso` is build-only | Removed from runtime packages (was orphaned on installed system) |
| Q3b | BTRFS subvols in Calamares | Moved from first-boot.sh → `calamares/modules/mount.conf` (was too late, conflicted with Calamares auto-layout) |
| Q4a | `nvidia-open-lts` added | LTS kernel fallback now works with NVIDIA (was broken — only `nvidia-open` installed) |
| Q5b | first-boot.sh 3-phase checkpointing | If crash, re-run resumes from last checkpoint (was one mega-script, half-installed on crash) |
| Q6a | NVIDIA GPU auto-detection | Turing+ → `nvidia-open`, pre-Turing → `nvidia` proprietary (was always nvidia-open, broke GTX 1080) |
| Q7a | Docker volume backup hook | `snap-pac` pre-update hook backs up Docker volumes before kernel/NVIDIA updates (rollback was losing volumes) |
| Q8b | model-manager split | HF models auto-pull (safe); Ollama tags require `--apply-ollama` (was auto-updating Ollama, risked CUDA regression) |
| Q9a | ZRAM formula fixed | `min(ram/2, 16384)` — ArchWiki recommendation (was `min(ram, 32768)`, caused thrashing on 16GB RAM) |
| Q10a | PC types renamed | "Main PC" → "AI Server", "HP Agent" → "Dev Workstation" (clearer naming) |

### Track 2: New Inference Engines (3 integrations)

| Tier | Engine | What | Port |
|------|--------|------|------|
| 2 | **vLLM** ⭐ | Multi-GPU tensor parallelism, Multi-LoRA (Hermes/OpenCode/Voice share base) | 8089 |
| 4 | **BeeLlama.cpp** ⭐ | KVarN KV-cache quantization (~50% VRAM reduction), replaces llama.cpp | 11438 |
| — | **oh-my-pi (omp)** ⭐ | Coding agent with 14 LSP ops + 28 DAP ops, replaces OpenCode | — |

### Track 3: New Integrations (from 13-repo research)

| # | Integration | Replaces | License |
|---|-------------|----------|---------|
| Q11a | **Laya** (System-1 router, 33ms decisions) | Needle2 | Apache-2.0 |
| Q12a | **FreeToken** (Tier-5 MoE engine, experimental `--profile moe`) | — (new) | Apache-2.0 |
| Q13a | **Playwright MCP** (browser automation) | — (new, replaces AGPL Sitegeist) | Apache-2.0 |
| Q14b | **ScarliHQ Kanban** (built-in Go board, replaces Multica) | — (new) | MIT |
| Q15b | **WebSocket activity log** (real-time agent activity in dashboard) | — (new, AG-UI inspired) | MIT |
| Q16a | **Unified image** (one ISO, auto-detect hardware) | Two profiles (Main/HP) | — |
| Q17a | **omp default + qwen-code opt-in** | OpenCode only | MIT/Apache |

### Track 4: AgentVerse Merge Plan

See `docs/AGENTVERSE_MERGE_PLAN.md` — analysis of sister project + phased roadmap (v17.2-v18.0).

## 🏗️ 5-Tier Inference Architecture (NEW in v17.1)

```
┌─────────────────────────────────────────────────────────┐
│  System-1: Laya (33ms router, replaces Needle2)        │
│  HITL Triage → safe? execute / risky? Telegram approve │
├─────────────────────────────────────────────────────────┤
│  Tier-1: SGLang (GPU 0, RadixAttention, agents)         │
│  Tier-2: vLLM (GPU 0+1 TP=2, Multi-LoRA, batch) [NEW]  │
│  Tier-3: Ollama ×2 (GGUF, concurrent, GPU 0+1)         │
│  Tier-4: BeeLlama.cpp (CPU, KVarN, 32k context) [NEW]  │
│  Tier-5: FreeToken (MoE, experimental) [NEW]           │
├─────────────────────────────────────────────────────────┤
│  Browser MCP: Playwright (Apache-2.0, replaces AGPL)   │
│  Coding Agent: oh-my-pi (LSP+DAP, replaces OpenCode)   │
└─────────────────────────────────────────────────────────┘
```

## 🚀 Quick Start

### Build ISO:
```bash
git clone https://github.com/MoZoHuJa/OS.git
cd OS && git checkout v17.1
sudo pacman -S archiso edk2-ovmf qemu-desktop
bash installer/scripts/build-iso.sh
```

### Install:
1. Write ISO to USB → boot → Calamares (auto BTRFS subvol layout via `mount.conf`)
2. Wizard: auto-detects GPU → suggests AI Server / Dev Workstation
3. Select backend: SGLang (default) or vLLM
4. Reboot → 3-phase checkpointed first-boot:
   - Phase 1: BTRFS verify + Snapper + CoW + ZRAM
   - Phase 2: NVIDIA auto-detect + nvidia-open-lts + GRUB modeset
   - Phase 3: Docker + 5-tier inference + omp + model-manager.timer
5. Dashboard: http://192.168.1.100:8090

## 🎮 GPU Modes

```bash
scarlix-mode ai --backend sglang   # Default (RadixAttention for agents)
scarlix-mode ai --backend vllm     # vLLM (Multi-LoRA, high throughput)
scarlix-mode turbo                  # SGLang + vLLM + Ollama (max)
scarlix-mode offline                # BeeLlama.cpp CPU (KVarN, 32k context)
scarlix-mode game                   # Native Steam/Sunshine
scarlix-mode creative               # ComfyUI + Video + Music
scarlix-mode tv                     # Docker Sunshine
scarlix-mode vram                   # VRAM health check
```

## 🔧 NVIDIA Rollback

```bash
# Option 1: rollback driver version (downgrade installed via yay in first-boot)
sudo downgrade nvidia-open

# Option 2: boot LTS kernel (nvidia-open-lts already installed)
sudo grub-set-default 1  # linux-lts entry
sudo reboot
```

## 📁 File Layout (v17.1)

```
scarlix/
├── profiledef.sh                          # v17.1.0, SCARLIX_V171
├── packages.x86_64                        # +yay, -snap-pac-grub, -downgrade, -archiso
├── pacman.conf                            # [core] [extra] [multilib] only
├── airootfs/
│   ├── etc/
│   │   ├── calamares/modules/
│   │   │   ├── mount.conf                # NEW Q3b: explicit BTRFS subvol layout
│   │   │   └── partition.conf            # Default FS = btrfs
│   │   ├── systemd/
│   │   │   ├── zram-generator.conf        # Q9a: min(ram/2, 16384)
│   │   │   └── system/
│   │   │       ├── first-boot.sh         # Q5b: 3-phase checkpointed
│   │   │       ├── model-manager.service
│   │   │       ├── model-manager.timer   # Q8b: HF auto, Ollama --apply-ollama
│   │   │       └── scarlix-first-boot.service
│   │   └── pacman.d/hooks/
│   │       ├── scarlix-docker-backup.hook # NEW Q7a
│   │       └── scarlix-docker-backup.sh  # NEW Q7a
│   └── usr/local/bin/
│       ├── scarlix-wizard                 # Q10a+Q16a: AI Server/Dev Workstation + auto-detect
│       ├── scarlix-mode                   # +--backend sglang|vllm + Laya status
│       └── model-manager.sh               # Q8b: --apply-ollama flag
├── efiboot/loader/entries/archiso-x86_64.conf
└── syslinux/syslinux.cfg

ai/
├── vllm/docker-compose.yml                # NEW I1: Tier-2 (TP=2, Multi-LoRA)
├── laya/docker-compose.yml               # NEW Q11a: System-1 router
├── freetoken/docker-compose.yml          # NEW Q12a: Tier-5 MoE (--profile moe)
├── browser-mcp/docker-compose.yml        # NEW Q13a: Playwright MCP
├── llamacpp/docker-compose.yml           # UPGRADED I2: BeeLlama.cpp (KVarN)
├── sglang/                                # Tier-1 (unchanged)
├── ollama/                                 # Tier-3 (unchanged)
└── smg/                                    # Gateway (unchanged)

agents/omp/                                # NEW I3: oh-my-pi profile
docs/AGENTVERSE_MERGE_PLAN.md             # NEW Track 4: sister project merge plan
AGENTS.md                                  # Updated: 5-tier + omp delegation
models.yaml                               # Updated: vllm + beellama + laya + freetoken sections
installer/scripts/build-iso.sh            # v17.1.0
tests/qemu-boot.sh                        # v17.1.0
```

## 📜 Version History

| Version | Date | Key Changes |
|---------|------|-------------|
| **v17.1** | 2026-09 | **10 review fixes + 5-tier inference (vLLM+BeeLlama+FreeToken) + Laya router + omp coding agent + Playwright MCP + ScarliHQ Kanban + 3-phase first-boot + Calamares BTRFS layout + AgentVerse merge plan** |
| v17.0 | 2026-09 | Garuda → EndeavourOS re-base. Profile renamed `scarlix/`. Pipewire. Explicit BTRFS+Snapper. NVIDIA open. |
| v16.5 | 2026-08 | 10 review fixes (PIPESTATUS, OVMF, console=ttyS0, model-manager, etc.) |
| v16.4 | 2026-08 | VERSION consistency, OVMF CODE/VARS split, real QEMU PASS/FAIL |
| v16.3 | 2026-08 | Wizard enables first-boot, NVIDIA post-install |
| v16.2 | 2026-08 | archiso-based |
| v16.1 | 2026-07 | Garuda Linux, BTRFS+Snapper, native gaming |
| v15 | 2026-06 | Model-Agnostic system |
| v12 | 2026-05 | Sovereign Agent Compute Edition |
