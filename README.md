# SCARLIX OS v16.4 — Garuda Edition (archiso-based)

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Base:** Garuda Linux (Arch-based, Zen kernel, BTRFS+Snapper)
> **Model-Agnostic:** Supports any HuggingFace model.

**Version:** v16.4.0 | **Base:** Garuda Linux | **License:** MIT

## 🐉 What's New in v16.4 (vs v16.3)

Based on 4 independent code reviews of v16.3, v16.4 fixes 8 issues:

### Fixed (P0 — Critical):
1. ✅ **VERSION consistency check** — build-iso.sh validates VERSION against profiledef.sh before building
2. ✅ **OVMF QEMU fix** — proper CODE (read-only) + VARS (writable template copy) separation
3. ✅ **Real QEMU PASS/FAIL** — serial output grepped for boot markers (SCARLIX/welcome/login), not just timeout

### Fixed (P1 — Recommended):
4. ✅ **Unified QEMU test** — build-iso.sh calls `tests/qemu-boot.sh` (single source of truth, no duplication)
5. ✅ **first-boot error handling** — each service logs SUCCESS/FAILED, continues on error
6. ✅ **first-boot log file** — `/var/log/scarlix/first-boot.log` with detailed log + summary

### Fixed (P2 — Optional):
7. ✅ **README wording** — changed "validated" to "ready for first build" (conservative)
8. ✅ **models.yaml hf_repo** — added `hf_repo` field for direct GGUF download from HuggingFace

## 🚀 Quick Start

### Build ISO:
```bash
git clone https://github.com/MoZoHuJa/OS.git
cd OS
sudo pacman -S archiso edk2-ovmf qemu-desktop
bash installer/scripts/build-iso.sh
```

### Test ISO:
```bash
bash tests/qemu-boot.sh output/scarlix-os-v16.4-x86_64.iso
```

### Install:
1. Write ISO to USB (Ventoy / dd)
2. Boot from USB → Calamares → auto BTRFS → auto user
3. TUI wizard: select "Main PC" or "HP Agent"
4. Wizard enables first-boot service
5. Reboot → NVIDIA install + Docker services + model download (check `/var/log/scarlix/first-boot.log`)
6. Dashboard: http://192.168.1.100:8090

## 🎮 GPU Modes

`scarlix-mode {ai|game|creative|turbo|offline|tv|status}`

## 🧠 Model-Agnostic

Edit `/etc/scarlix/models.yaml` → run `download-models.sh` → restart.
Supports: Llama, Qwen, Mistral, Gemma, Phi, DeepSeek, Nemotron.
Uses `hf_repo` field for direct GGUF downloads from HuggingFace.
