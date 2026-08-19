# SCARLIX OS v16.1 — Garuda Edition

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Base:** Garuda Linux (Arch-based, Zen kernel, BTRFS+Snapper)
> **Model-Agnostic:** Supports any HuggingFace model.
> 2 servers, 30+ containers, 6 GPU modes, 100% offline-capable.

**Version:** v16.1.0 | **Base:** Garuda Linux | **License:** MIT

## What's New in v16.1 (vs v15)

| Change | v15 (Ubuntu) | v16.1 (Garuda) |
|--------|-------------|----------------|
| Base OS | Ubuntu 24.04 LTS | **Garuda Linux (Arch-based)** |
| Kernel | generic 5.15 | **Zen kernel** (performance) |
| Filesystem | ext4 + LVM | **BTRFS + Snapper** (atomic snapshots) |
| Package manager | apt | **pacman + yay (AUR)** |
| NVIDIA driver | nvidia-driver-570-open | **nvidia-dkms** |
| Network | netplan | **NetworkManager** |
| Gaming | Docker stack | **Garuda Gamer (native Steam/Wine/Proton)** |
| Sunshine | Docker | **Native (systemd)** |
| Snapshots | LVM (manual) | **Snapper (automatic, BTRFS)** |
| ZRAM | No | **Yes (built-in)** |
| Rollback | LVM restore | **`snapper rollback` (<5s)** |

## Documentation

- [V16.1 Garuda Master Manifest](docs/V16.1_GARUDA_MASTER_MANIFEST.md) — Complete build guide
- [V15 Master Manifest](docs/V15_FINAL_MASTER_MANIFEST.md) — Previous version (Ubuntu-based)
- [Architecture](docs/ARCHITECTURE.md)
- [Hardware](docs/HARDWARE.md)
- [Network](docs/NETWORK.md)
- [Troubleshooting](docs/TROUBLESHOOTING.md)

## Quick Start

```bash
git clone https://github.com/MoZoHuJa/OS.git
cd OS
# For Garuda-based install:
bash base-os/install-garuda.sh
# For Ubuntu-based install (v15):
# bash base-os/install-base.sh
```

## GPU Modes

`scarlix-mode {ai|game|creative|turbo|offline|tv|status}`

## Model-Agnostic

Edit `/etc/scarlix/models.yaml` → run `download-models.sh` → restart.
Supports: Llama, Qwen, Mistral, Gemma, Phi, DeepSeek, Nemotron.
