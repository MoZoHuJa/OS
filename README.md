# SCARLIX OS v17.0 — EndeavourOS Edition

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Base:** EndeavourOS (near-vanilla Arch) — cleaner than Garuda, easier to maintain.
> **Model-Agnostic:** Supports any HuggingFace model.

**Version:** v17.0.0 | **Base:** EndeavourOS (Arch) | **License:** MIT

## 🚀 What's New in v17.0 (vs v16.5)

This is a **complete re-base** from Garuda Linux → EndeavourOS, following the
`SCARLIX_ENDEAVOUROS_MIGRATION_GUIDE.md`.

### Structural changes
1. ✅ **Profile renamed** — `garuda-scarlix/` → `scarlix/` (cleaner, no distro coupling)
2. ✅ **All Garuda packages removed** — `garuda-common-settings` dropped; no more Chaotic-AUR dependency
3. ✅ **Vanilla Arch kernel** — `linux` (default) + `linux-lts` (fallback); `linux-zen` removed from ISO (optional post-install via AUR)
4. ✅ **Pipewire audio stack** — replaced Pulseaudio (modern, Wayland-ready)
5. ✅ **GRUB + efibootmgr + os-prober** — explicit bootloader toolchain (was implicit via Garuda)
6. ✅ **Snap-pac-grub** — auto-update GRUB after pacman kernel upgrades

### New in first-boot.sh (was automatic on Garuda, now explicit)
7. ✅ **BTRFS subvolume layout** — explicit creation of `@ @home @root @srv @var_log @var_lib_docker @models @snapshots`
8. ✅ **Snapper configs** — `snapper -c root create-config /` + `snapper -c home create-config /home`
9. ✅ **Snapper timers** — `snapper-timeline.timer` + `snapper-cleanup.timer` enabled at first-boot
10. ✅ **NVIDIA open driver** — `nvidia-open` (Turing+) preferred over proprietary `nvidia` (with fallback)
11. ✅ **nvidia_drm.modeset=1** — auto-added to `GRUB_CMDLINE_LINUX_DEFAULT` for clean console + Wayland

### Kept from v16.5 (all good engineering decisions preserved)
- ✅ archiso-based ISO build (`mkarchiso`)
- ✅ Model-agnostic `models.yaml` with `hf_repo`/`hf_file` fields
- ✅ 6 GPU modes (ai/game/creative/turbo/offline/tv) + `vram` health check
- ✅ 3-tier inference (SGLang → Ollama → llama.cpp)
- ✅ `model-manager.sh` weekly updater (Mon 04:00 + VRAM + Telegram)
- ✅ `downgrade` AUR tool for NVIDIA driver rollback
- ✅ ZRAM 32GB zstd (`zram-generator.conf` — now uses `min(ram, 32768)`)
- ✅ BTRFS CoW disabled via `chattr +C` on `/models`, `/mnt/games`, `/var/lib/docker`, `/var/lib/scarlix`
- ✅ NVIDIA + CUDA post-install (keeps ISO small)
- ✅ `set -euo pipefail` + `PIPESTATUS[0]` around tee pipelines
- ✅ OVMF no-fallback (FAIL if `OVMF_VARS.fd` missing)
- ✅ `console=ttyS0` in UEFI + BIOS boot configs (deterministic serial capture)
- ✅ README_VERSION validation in build-iso.sh (build fails on mismatch)
- ✅ First-boot logging to `/var/log/scarlix/first-boot.log`

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
bash tests/qemu-boot.sh output/scarlix-os-v17.0-x86_64.iso
```

### Install:
1. Write ISO to USB (Ventoy / `dd`)
2. Boot from USB → Calamares → auto BTRFS → auto user
3. TUI wizard: select "Main PC" or "HP Agent"
4. Wizard enables `scarlix-first-boot.service` + `model-manager.timer`
5. Reboot → BTRFS subvolumes + Snapper + NVIDIA + Docker + models (check `/var/log/scarlix/first-boot.log`)
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
| `vram` | — | VRAM health check |

## 🧠 Model-Agnostic

Edit `/etc/scarlix/models.yaml` → run `download-models.sh` → restart.
Supports: Llama, Qwen, Mistral, Gemma, Phi, DeepSeek, Nemotron.
Uses `hf_repo` field for direct GGUF downloads from HuggingFace.
Weekly auto-update via `model-manager.sh` (Monday 04:00 + Telegram summary).

## 🔧 NVIDIA Rollback

If NVIDIA driver breaks on default kernel:
```bash
# Option 1: rollback to previous driver version
sudo downgrade nvidia-open

# Option 2: boot LTS kernel (installed by default in v17)
sudo grub-set-default 1   # select linux-lts entry
sudo grub-mkconfig -o /boot/grub/grub.cfg
sudo reboot
```

## 📊 ZRAM

- Algorithm: zstd (3.5x typical compression)
- Size: `min(ram, 32768)` — 32GB virtual OR system RAM, whichever is smaller
- Config: `/etc/systemd/zram-generator.conf`
- Result: ~3x effective RAM for mixed AI + Docker + gaming workload

## 📁 BTRFS + Snapper (explicit in v17)

Subvolumes created at first-boot:
```
@                    # root
@home                # /home
@root                # /root
@srv                 # /srv
@var_log             # /var/log
@var_lib_docker      # /var/lib/docker (CoW disabled)
@models              # /models (CoW disabled)
@snapshots           # /.snapshots
```

Snapper configs:
- `root` → `/` (timeline + cleanup timers enabled)
- `home` → `/home` (timeline + cleanup timers enabled)

Rollback: `sudo snapper -c root list` → `sudo snapper -c root rollback <id>`

## 📁 File Layout (v17.0)

```
scarlix/                                    # ← renamed from garuda-scarlix/
├── profiledef.sh                          # archiso profile (v17.0.0, SCARLIX_V170)
├── packages.x86_64                        # +linux, +linux-lts, +pipewire, +grub, +openssh
├── airootfs/
│   ├── etc/
│   │   ├── systemd/
│   │   │   ├── zram-generator.conf        # zstd + min(ram, 32768)
│   │   │   └── system/
│   │   │       ├── first-boot.sh          # +BTRFS subvols +Snapper configs +timers +nvidia_drm.modeset
│   │   │       ├── model-manager.service
│   │   │       ├── model-manager.timer    # Mon 04:00 weekly
│   │   │       └── scarlix-first-boot.service
│   │   └── ...
│   └── usr/local/bin/
│       ├── scarlix-wizard                 # EndeavourOS branding
│       ├── scarlix-mode                   # /etc/os-release (was /etc/garuda-release)
│       └── model-manager.sh               # HF + Ollama + VRAM + Telegram
├── efiboot/loader/entries/archiso-x86_64.conf   # console=ttyS0, vmlinuz-linux (was vmlinuz-linux-zen)
└── syslinux/syslinux.cfg                        # console=ttyS0, vmlinuz-linux

installer/scripts/build-iso.sh             # VERSION=17.0.0, PROFILE_DIR=scarlix/
tests/qemu-boot.sh                        # v17.0 ISO filename, EndeavourOS boot markers
models.yaml                               # v17.0 comment update
```

## 📜 Version History

| Version | Date | Base | Key Change |
|---------|------|------|------------|
| **v17.0** | 2026-09 | **EndeavourOS** | **Garuda → EndeavourOS re-base. Profile renamed `scarlix/`. Pipewire audio. Explicit BTRFS subvols + Snapper. NVIDIA open driver. nvidia_drm.modeset=1.** |
| v16.5 | 2026-08 | Garuda | 10 review fixes (PIPESTATUS, OVMF no-fallback, console=ttyS0, README validation, linux-lts, model-manager.sh, downgrade, vram subcommand, ZRAM, BTRFS CoW) |
| v16.4 | 2026-08 | Garuda | 8 review fixes (VERSION consistency, OVMF CODE/VARS split, real QEMU PASS/FAIL, unified QEMU test, first-boot logging, models.yaml hf_repo) |
| v16.3 | 2026-08 | Garuda | 5 review fixes (wizard enables first-boot, NVIDIA post-install, garuda-common-settings only) |
| v16.2 | 2026-08 | Garuda | archiso-based, all v16.1 review fixes |
| v16.1 | 2026-07 | Garuda | Arch-based, BTRFS+Snapper, native gaming |
| v15 | 2026-06 | Ubuntu | Model-Agnostic system |
| v12 | 2026-05 | Ubuntu | Sovereign Agent Compute Edition |

## 🔄 Migration from v16.5 → v17.0

This is a **clean rebuild**, not an in-place upgrade. To migrate an existing v16.5 install:

1. Back up data (Docker volumes, `/models`, `/home`, `/etc/scarlix/.env`)
2. Write v17.0 ISO to USB
3. Install fresh (Calamares auto-BTRFS)
4. Restore `/etc/scarlix/.env` + `/models/` + Docker volumes
5. Run `scarlix-wizard` → reboot → `first-boot.sh` re-pulls everything

For development reference, see `SCARLIX_ENDEAVOUROS_MIGRATION_GUIDE.md` (source of this re-base).
