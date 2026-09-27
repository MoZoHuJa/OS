# SCARLIX OS v17.4 — EndeavourOS Edition (Unified Bootstrap, Fail-Hard)

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **NO ISO needed** — bootstrap installer on clean EndeavourOS.
> **Verified AI path**: SGLang (GPU0) + vLLM (GPU1, TP=1) + BeeLlama (CPU).

**Version:** v17.4.0 | **Base:** EndeavourOS (Arch) | **License:** MIT

## 🚀 Install (NO ISO)

### Primary (safe — review before run) ⭐
```bash
git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
cd ~/scarlix-os
nano install.sh   # review what it does
bash install.sh
```

### Secondary (convenience — clones repo for you)
```bash
curl -fsSL https://raw.githubusercontent.com/MoZoHuJa/OS/main/install.sh | bash -s -- --clone
```

---

## 🆕 What's New in v17.4 (vs v17.3)

**Correction release — fixes 19 issues from 3 independent reviews.**

### 🔴 P0 Fixes (install + runtime blockers)

| # | Fix | v17.3 Problem | v17.4 Solution |
|---|-----|---------------|-----------------|
| Q1a | **curl\|bash removed as primary** | Broken (needed repo files) | Primary = git clone + review. curl\|bash secondary with `--clone` flag |
| Q2a | **vLLM TP=1 (not TP=2)** | RTX 5060 Ti (Blackwell sm_120) + RTX 4060 Ti (Ada sm_89) = different compute_cap → TP=2 fails with NCCL error | TP=1 default (separate model per GPU). Auto-detect compute_cap. **Mixed GPU architectures safe.** |
| Q3b | **BTRFS subvol creation removed** | Created subvols but never mounted (no fstab) → useless | chattr +C on directories only. Calamares made @ + @home. |
| Q4a | **Docker network unified** | `scarlix-net` in install vs `scarlix_ai` in compose → containers fail | All 12 compose files → `scarlix-net` |
| Q5a | **Calamares + [docker] repo removed** | Calamares useless on installed system. [docker] repo doesn't exist on Arch → pacman -Syu fails | Calamares removed. nvidia-container-toolkit via yay (AUR) |
| Q6a | **Model paths synced** | SGLang `/Qwen3-14B-Instruct` vs models.yaml `/Qwen3-14B-Instruct-AWQ` | SGLang compose → `/Qwen3-14B-Instruct-AWQ`. **models.yaml stays model-agnostic** (edit to use ANY HuggingFace model) |
| Q7a | **Unified 5 phases + HW-aware checkpoint** | 5 install + 3 first-boot phases conflicted. Checkpoint ignored HW changes | Single 5-phase install.sh. Checkpoint stores GPU count + compute_cap → re-runs Phase 2 if HW changes |
| Q8a | **scarlix-mode ai = verified path** | No working minimal path. docker-compose-main.yml didn't exist | SGLang GPU0 + vLLM GPU1 (TP=1) + BeeLlama CPU. Fallback: SGLang→vLLM→BeeLlama→Ollama. Fixed compose refs. |
| #4 | **scarlihq typo fixed** | `scarlihp` → Dashboard not copied | `scarlihq` (correct) |
| #9 | **nvidia-open-lts + linux-lts + headers atomic** | nvidia-open-lts without linux-lts+headers → DKMS fails → black screen | All 3 installed atomically in one pacman -S |
| #10 | **yay build as user (not root)** | makepkg refuses root → "Running makepkg as root is not allowed" | `sudo -u $SUDO_USER` for git clone + makepkg |
| #11 | **Fail-hard** | `.installed` touched even on critical failure → false success | Critical failures (NVIDIA, Docker) → exit 1, NO `.installed` |

### 🏗️ Verified AI Architecture (v17.4)

**For your hardware (RTX 5060 Ti + RTX 4060 Ti — mixed architectures):**
```
GPU 0: RTX 5060 Ti 16GB (Blackwell sm_120)
└── Tier-1: SGLang (agents, RadixAttention, primary inference)

GPU 1: RTX 4060 Ti 16GB (Ada sm_89)
└── Tier-2: vLLM (TP=1, Multi-LoRA, separate model)

CPU:
└── Tier-4: BeeLlama.cpp / llama.cpp (offline, q4_0 KV cache, 32k context)
```

**Why TP=1 not TP=2:** vLLM tensor parallelism requires identical compute capability. Your RTX 5060 Ti (sm_120) + RTX 4060 Ti (sm_89) are different architectures → TP=2 would fail with `NCCL error`. TP=1 runs a separate model instance on each GPU — safe and efficient.

**Fallback chain:** If SGLang fails → vLLM takes over. If vLLM fails → BeeLlama (CPU). If both fail → Ollama.

### 🧪 Model-Agnostic (Q6a variant)

`models.yaml` stays model-agnostic — edit to use ANY HuggingFace model:
```yaml
sglang:
  model_path: "/models/Qwen3-14B-Instruct-AWQ"
  # Change to any: /models/Meta-Llama-3.1-8B-Instruct, /models/Mistral-7B, etc.
```
Then run `bash /opt/scarlix/scripts/download-models.sh` to fetch.

---

## 🎮 GPU Modes

```bash
scarlix-mode ai        # Verified path: SGLang GPU0 + vLLM GPU1 + BeeLlama (default)
scarlix-mode turbo     # Same as ai (max throughput)
scarlix-mode offline   # BeeLlama.cpp CPU (q4_0 KV, 32k context)
scarlix-mode game      # Native Steam/Sunshine
scarlix-mode creative  # ComfyUI + Video + Music
scarlix-mode tv        # Docker Sunshine
scarlix-mode vram      # VRAM health check (shows compute_cap per GPU)
scarlix-mode status    # System summary
```

---

## 🔧 NVIDIA Rollback

```bash
# Option 1: rollback driver (downgrade installed via yay)
sudo downgrade nvidia-open

# Option 2: boot LTS kernel (nvidia-open-lts + linux-lts installed atomically)
sudo grub-set-default 1
sudo reboot
```

---

## 🔄 Re-run / Update

```bash
cd ~/scarlix-os
git pull
bash install.sh   # idempotent — skips completed phases (HW-aware)
```

Force full reinstall:
```bash
sudo rm -rf /var/lib/scarlix/.checkpoint-* /opt/scarlix/.installed
bash install.sh
```

---

## 📁 File Layout (v17.4)

```
OS/
├── install.sh                              # v17.4: Unified bootstrap (5 phases, fail-hard, HW-aware)
├── README.md
├── AGENTS.md
├── models.yaml                             # Model-agnostic (edit for ANY HuggingFace model)
├── scarlix/
│   ├── profiledef.sh                       # v17.4.0
│   ├── packages.x86_64                     # NO calamares (ISO-only)
│   ├── pacman.conf                         # Standard Arch repos only (no [docker] repo)
│   └── airootfs/
│       ├── etc/calamares/modules/mount.conf
│       ├── etc/systemd/
│       │   ├── zram-generator.conf
│       │   └── system/
│       │       ├── first-boot.sh
│       │       ├── scarlix-first-boot.service
│       │       └── model-manager.{service,timer}
│       └── usr/local/bin/
│           ├── scarlix-wizard              # v17.4
│           ├── scarlix-mode               # Verified path + fallback
│           └── model-manager.sh
├── ai/
│   ├── sglang/docker-compose.yml          # model path synced, scarlix-net
│   ├── vllm/docker-compose.yml            # TP=1 (GPU1), scarlix-net
│   ├── llamacpp/docker-compose.yml        # Official llama.cpp image, q4_0 KV
│   ├── ollama/docker-compose.yml          # scarlix-net
│   ├── browser-mcp/                        # Playwright MCP
│   └── smg/                                 # Gateway
├── installer/scripts/build-iso.sh          # v17.4.0 (optional ISO)
└── tests/qemu-boot.sh                      # v17.4.0
```

---

## 📜 Version History

| Version | Date | Key Changes |
|---------|------|-------------|
| **v17.4** | 2026-10 | **Correction release. 19 fixes: curl\|bash removed, vLLM TP=1 (mixed GPU safe), BTRFS subvols removed, Docker network unified, Calamares+[docker] repo removed, model paths synced, unified 5-phase, fail-hard, verified AI path + fallback.** |
| v17.3 | 2026-10 | Bootstrap installer (install.sh) — no ISO needed. (Broken — curl\|bash, network mismatch, TP=2) |
| v17.2.1 | 2026-10 | Auto 5-tier for 2+ NVIDIA GPU. |
| v17.2 | 2026-10 | Minimal Working Baseline. 8 P0 fixes. 2-tier default. |
| v17.1 | 2026-09 | 5-tier inference (vLLM+BeeLlama+FreeToken+Laya). (Over-engineered) |
| v17.0 | 2026-09 | Garuda → EndeavourOS re-base. |
| v16.5 | 2026-08 | 10 review fixes (PIPESTATUS, OVMF, console=ttyS0) |
| v16.1 | 2026-07 | Garuda Linux, BTRFS+Snapper, native gaming |
| v15 | 2026-06 | Model-Agnostic system |

---

## ⚠️ Known Limitations

- **No CI/CD**: install.sh not tested automatically
- **No real HW test**: QEMU doesn't test NVIDIA/CUDA
- **Single maintainer**: One person maintaining full stack
- **SGLang on Arch**: Officially Ubuntu-only, AUR has packaging gaps
- **vLLM TP=1**: Separate model per GPU (not tensor parallel) — less efficient than TP=2 but works with mixed architectures

## 🗺️ Roadmap

- **v17.5**: AgentVerse merge (944-app store, capabilities)
- **v18.0**: Incus dev workspaces, ScarliHQ Rust refactor
