# SCARLIX OS v16.5 — Garuda Edition (archiso-based)

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Base:** Garuda Linux (Arch-based, Zen kernel, BTRFS+Snapper)
> **Model-Agnostic:** Supports any HuggingFace model.

**Version:** v16.5.0 | **Base:** Garuda Linux | **License:** MIT

## 🐉 What's New in v16.5 (vs v16.4)

Based on 4 independent code reviews of v16.4, v16.5 fixes 10 issues (3 P0 + 7 P1):

### Fixed (P0 — Critical):
1. ✅ **`set -euo pipefail` + `tee` pipeline bug** (1-a) — QEMU/mkarchiso exit codes are now captured via `PIPESTATUS[0]` with `set +e` around tee pipelines (was being lost to tee's exit code)
2. ✅ **OVMF fallback removed** (2-a) — qemu-boot.sh now FAILS if `OVMF_VARS.fd` template is missing instead of silently copying from `OVMF_CODE.fd` (which produces broken UEFI NVRAM)
3. ✅ **Deterministic QEMU serial capture** (3-a) — `console=tty0 console=ttyS0,115200n8` added to both `archiso-x86_64.conf` (UEFI) and `syslinux.cfg` (BIOS) — kernel now reliably outputs to serial port

### Fixed (P1 — Recommended):
4. ✅ **README_VERSION validation** (4-a) — `build-iso.sh` now validates VERSION against README.md in addition to profiledef.sh (build fails on any mismatch)
5. ✅ **`linux-lts` kernel** (5-a) — Added `linux-lts` + `linux-lts-headers` to `packages.x86_64` as fallback when NVIDIA driver breaks on Zen kernel
6. ✅ **`model-manager.sh`** (6-b) — New weekly model updater script (`/usr/local/bin/model-manager.sh`) + systemd timer (Mondays 04:00). Full scope: pulls HuggingFace GGUF + Ollama tags, captures VRAM before/after, sends Telegram summary
7. ✅ **`downgrade` AUR tool** (7-a) — Added to `packages.x86_64` for NVIDIA driver rollback (`sudo downgrade nvidia-dkms`)
8. ✅ **`vram` subcommand** (8-b) — Integrated into `scarlix-mode` (no separate script). Run `scarlix-mode vram` for per-GPU VRAM bars + health check (returns exit 2 if ≥90% utilization)
9. ✅ **ZRAM tuned** (9-a) — New `/etc/systemd/zram-generator.conf`: zstd compression + 32GB allocation (caps at RAM size on smaller systems)
10. ✅ **BTRFS CoW disabled** (10-a) — `first-boot.sh` now runs `chattr +C` on `/models`, `/mnt/games`, `/var/lib/docker`, `/var/lib/scarlix` immediately after mkdir (prevents fragmentation on large mutable stores)

### Unchanged from v16.4:
- archiso profile structure (`garuda-scarlix/`)
- NVIDIA + CUDA installed post-install (keeps ISO small)
- Model-agnostic `models.yaml` with `hf_repo`/`hf_file` fields
- 6 GPU modes (ai/game/creative/turbo/offline/tv)
- 3-tier inference (SGLang → Ollama → llama.cpp)
- First-boot logging to `/var/log/scarlix/first-boot.log`

## 🚀 Quick Start

### Build ISO:
```bash
git clone https://github.com/MoZoHuJa/OS.git
cd OS
sudo pacman -S archiso edk2-ovmf qemu-desktop
bash installer/scripts/build-iso.sh
```

### Test ISO (UEFI + SHA256 + serial boot markers):
```bash
bash tests/qemu-boot.sh output/scarlix-os-v16.5-x86_64.iso
```

### Install:
1. Write ISO to USB (Ventoy / dd)
2. Boot from USB → Calamares → auto BTRFS → auto user
3. TUI wizard: select "Main PC" or "HP Agent"
4. Wizard enables first-boot service + model-manager timer
5. Reboot → NVIDIA install + Docker services + model download (check `/var/log/scarlix/first-boot.log`)
6. Dashboard: http://192.168.1.100:8090

## 🎮 GPU Modes

`scarlix-mode {ai|game|creative|turbo|offline|tv|status|vram}`

| Mode | GPU usage | Use case |
|------|-----------|----------|
| `ai` | SGLang + Ollama | Default AI inference |
| `game` | Native Steam/Sunshine | Full GPU for gaming |
| `creative` | ComfyUI + Video + Musicgen | Image/video/audio gen |
| `turbo` | SGLang + Ollama together | Max throughput |
| `offline` | llama.cpp CPU only | Low power / no GPU |
| `tv` | Docker Sunshine | Family streaming |
| `status` | — | System summary |
| `vram` | — | VRAM health check (NEW) |

## 🧠 Model-Agnostic

Edit `/etc/scarlix/models.yaml` → run `download-models.sh` → restart.
Supports: Llama, Qwen, Mistral, Gemma, Phi, DeepSeek, Nemotron.
Uses `hf_repo` field for direct GGUF downloads from HuggingFace.
Weekly auto-update via `model-manager.sh` (Monday 04:00 + Telegram summary).

## 🔧 NVIDIA Rollback (NEW in v16.5)

If NVIDIA driver breaks on Zen kernel:
```bash
# Option 1: rollback to previous driver version
sudo downgrade nvidia-dkms

# Option 2: boot LTS kernel
sudo grub-set-default 1   # select linux-lts entry
sudo reboot
```

## 📊 ZRAM (NEW in v16.5)

- Algorithm: zstd (3.5x typical compression)
- Size: 32GB virtual (caps at RAM size on smaller systems)
- Config: `/etc/systemd/zram-generator.conf`
- Result: ~3x effective RAM for mixed AI + Docker + gaming workload

## 📁 File Layout (v16.5)

```
garuda-scarlix/
├── profiledef.sh                          # archiso profile (v16.5.0)
├── packages.x86_64                        # + linux-lts, downgrade, zram-generator
├── airootfs/
│   ├── etc/
│   │   ├── systemd/
│   │   │   ├── zram-generator.conf        # NEW: zstd + 32GB ZRAM (9-a)
│   │   │   └── system/
│   │   │       ├── first-boot.sh          # + chattr +C BTRFS CoW disable (10-a)
│   │   │       ├── model-manager.service  # NEW: weekly model updater (6-b)
│   │   │       ├── model-manager.timer    # NEW: Monday 04:00 trigger (6-b)
│   │   │       └── scarlix-first-boot.service
│   │   └── ...
│   └── usr/local/bin/
│       ├── scarlix-wizard                 # + enable model-manager.timer
│       ├── scarlix-mode                   # + `vram` subcommand (8-b)
│       └── model-manager.sh               # NEW: HF + Ollama + VRAM + Telegram (6-b)
├── efiboot/loader/entries/archiso-x86_64.conf   # + console=ttyS0 (3-a)
└── syslinux/syslinux.cfg                        # + console=ttyS0 (3-a)

installer/scripts/build-iso.sh             # + PIPESTATUS + README_VERSION check (1-a, 4-a)
tests/qemu-boot.sh                        # + PIPESTATUS + OVMF no-fallback (1-a, 2-a)
models.yaml                               # v16.5 comment update
```

## 📜 Version History

| Version | Date | Base | Key Change |
|---------|------|------|------------|
| v16.5 | 2025-01 | Garuda | 10 review fixes (PIPESTATUS, OVMF no-fallback, console=ttyS0, README validation, linux-lts, model-manager.sh, downgrade, vram subcommand, ZRAM, BTRFS CoW) |
| v16.4 | 2025-01 | Garuda | 8 review fixes (VERSION consistency, OVMF CODE/VARS split, real QEMU PASS/FAIL, unified QEMU test, first-boot logging, models.yaml hf_repo) |
| v16.3 | 2025-01 | Garuda | 5 review fixes (wizard enables first-boot, NVIDIA post-install, garuda-common-settings only) |
| v16.2 | 2025-01 | Garuda | archiso-based, all v16.1 review fixes |
| v16.1 | 2025-01 | Garuda | Arch-based, BTRFS+Snapper, native gaming |
| v15 | 2024-12 | Ubuntu | Model-Agnostic system |
| v12 | 2024-11 | Ubuntu | Sovereign Agent Compute Edition |
