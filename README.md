# SCARLIX OS v16.2 — Garuda Edition (Archiso-based)

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Base:** Garuda Linux (Arch-based, Zen kernel, BTRFS+Snapper)
> **Model-Agnostic:** Supports any HuggingFace model.
> 2 servers, 30+ containers, 6 GPU modes, 100% offline-capable.

**Version:** v16.2.0 | **Base:** Garuda Linux | **License:** MIT

## 🐉 What's New in v16.2 (vs v16.1)

Based on 4 independent code reviews, v16.2 fixes ALL critical issues:

### Fixed (from reviews):
1. ✅ **archiso profile** — proper `garuda-scarlix/` directory with `pacman.conf`, `packages.x86_64`, `profiledef.sh`, `airootfs/`, `efiboot/`
2. ✅ **Dynamic ISO build** — `mkarchiso` instead of mount→cp→xorriso
3. ✅ **Single ISO + TUI wizard** — one ISO, wizard asks Main/HP Agent
4. ✅ **Native gaming** — Steam/Wine/Proton/Lutris/RetroArch via pacman (no Docker)
5. ✅ **Sunshine hybrid** — native (game mode) + Docker (TV mode)
6. ✅ **snap-pac hook** — auto BTRFS snapshot before every pacman operation
7. ✅ **Single root subvolume** — simple, reliable, Snapper snapshots entire root
8. ✅ **Port fixes** — Grafana→3001, OpenLIT→3002 (no conflicts)
9. ✅ **first-boot in /etc/systemd/system/** — proper systemd location
10. ✅ **QEMU test** — `tests/qemu-boot.sh` for ISO validation
11. ✅ **Calamares modules** — auto BTRFS partition, auto user, auto wizard
12. ✅ **download-models.sh rewritten** — from scratch, with progress bars
13. ✅ **Absolute paths** — no relative path bugs in build scripts
14. ✅ **NetworkManager** — not netplan (Garuda way)

### Architecture:
```
garuda-scarlix/           ← archiso profile
├── profiledef.sh          ← ISO metadata
├── pacman.conf            ← package sources
├── packages.x86_64        ← all packages (pacman)
├── airootfs/              ← SCARLIX overlay
│   ├── etc/
│   │   ├── systemd/system/  ← first-boot service + scripts
│   │   ├── calamares/modules/ ← auto-partition BTRFS, auto-user
│   │   ├── pacman.d/hooks/   ← snap-pac (auto snapshot)
│   │   └── snapper/configs/  ← Snapper config (no timeline)
│   ├── usr/local/bin/        ← scarlix-wizard, scarlix-mode
│   └── opt/scarlix/         ← SCARLIX source
├── efiboot/                 ← UEFI boot entry
└── syslinux/                ← BIOS boot entry

installer/scripts/
├── build-iso.sh            ← mkarchiso-based ISO builder
└── scarlix-wizard          ← TUI wizard (Main/HP selection)

tests/
└── qemu-boot.sh            ← QEMU boot validation
```

## 🚀 Quick Start

### Build ISO:
```bash
git clone https://github.com/MoZoHuJa/OS.git
cd OS
sudo pacman -S archiso
bash installer/scripts/build-iso.sh
```

### Install:
1. Write ISO to USB (Ventoy / dd)
2. Boot from USB
3. Calamares installer → auto BTRFS → auto user
4. TUI wizard: select "Main PC" or "HP Agent"
5. Reboot → first-boot auto-starts all services
6. Dashboard: http://192.168.1.100:8090

## 🎮 GPU Modes

`scarlix-mode {ai|game|creative|turbo|offline|tv|status}`

- `ai` — SGLang + Ollama (default)
- `game` — Native Sunshine (Steam/Wine/Proton)
- `creative` — ComfyUI + Video + Music
- `turbo` — Dual SGLang
- `offline` — llama.cpp CPU only
- `tv` — Docker Sunshine + kiosk dashboard

## 🧠 Model-Agnostic

Edit `/etc/scarlix/models.yaml` → run `download-models.sh` → restart.
Supports: Llama, Qwen, Mistral, Gemma, Phi, DeepSeek, Nemotron.

## 📁 Documentation

- [V16.2 in repo] — `garuda-scarlix/` archiso profile
- [V15 Master Manifest](docs/V15_FINAL_MASTER_MANIFEST.md) — Complete code reference
- [V16.1 Manifest](docs/V16.1_GARUDA_MASTER_MANIFEST.md) — Garuda architecture
- [Architecture](docs/ARCHITECTURE.md)
- [Hardware](docs/HARDWARE.md)
- [Network](docs/NETWORK.md)
