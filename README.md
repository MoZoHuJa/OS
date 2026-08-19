# SCARLIX OS v16.3 — Garuda Edition (archiso-based, validated)

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Base:** Garuda Linux (Arch-based, Zen kernel, BTRFS+Snapper)
> **Model-Agnostic:** Supports any HuggingFace model.

**Version:** v16.3.0 | **Base:** Garuda Linux | **License:** MIT

## 🐉 What's New in v16.3 (vs v16.2)

Based on 4 independent code reviews of v16.2, v16.3 fixes 5 remaining issues:

### Fixed (from v16.2 reviews):
1. ✅ **first-boot.service enabled** — `systemctl enable` now in TUI wizard (not just in profile)
2. ✅ **NVIDIA+CUDA removed from ISO** — installed post-install via `first-boot.sh` (smaller ISO, safer mkarchiso build)
3. ✅ **Garuda branding removed** — only `garuda-common-settings` kept (ZRAM+tweaks), removed `garuda-dr460nized` and `garuda-welcome` (avoids dependency conflicts)
4. ✅ **QEMU test improved** — UEFI boot test + SHA256 checksum verification (not just smoke test)
5. ✅ **Absolute paths in first-boot.sh** — all `docker compose` commands use `-f /full/path/compose.yml` (no `cd` pattern)

### Build flow:
```
git clone → pacman -S archiso → bash build-iso.sh → mkarchiso → ISO → SHA256 → QEMU UEFI test → USB → install
```

## 🚀 Quick Start

### Build ISO:
```bash
git clone https://github.com/MoZoHuJa/OS.git
cd OS
sudo pacman -S archiso
bash installer/scripts/build-iso.sh
```

### Test ISO:
```bash
bash tests/qemu-boot.sh
```

### Install:
1. Write ISO to USB (Ventoy / dd)
2. Boot from USB → Calamares → auto BTRFS → auto user
3. TUI wizard: select "Main PC" or "HP Agent"
4. Wizard enables first-boot service
5. Reboot → NVIDIA install + Docker services auto-start + model download
6. Dashboard: http://192.168.1.100:8090

## 🎮 GPU Modes

`scarlix-mode {ai|game|creative|turbo|offline|tv|status}`

## 🧠 Model-Agnostic

Edit `/etc/scarlix/models.yaml` → run `download-models.sh` → restart.
