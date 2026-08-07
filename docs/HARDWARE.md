# SCARLIX OS v12 — Hardware Requirements

## Main Server (AI Brain + Gaming)

| Component | Specification | Purpose |
|-----------|--------------|---------|
| CPU | AMD Ryzen 7 7700X (8C/16T) | Docker, scarlix-mode, transcoding |
| RAM | 64 GB DDR5-5600 (2×32) | SGLang + Ollama + Buzz + cache |
| GPU 0 | NVIDIA RTX 5060 Ti 16 GB (Blackwell) | SGLang inference (primary) |
| GPU 1 | NVIDIA RTX 4060 Ti 16 GB (Ada) | Ollama concurrent + Whisper |
| Storage | 2TB NVMe Gen4 (sys) + 4TB NVMe (data) + 8TB HDD (backup) | LVM snapshots |
| Network | 2.5 GbE + WiFi 6E | Headscale LAN |
| PSU | 850W 80+ Gold (ATX 3.0) | 2× GPU |
| Motherboard | ASUS TUF B650-PLUS | 2× PCIe x16 |

## HP Agent PC (Worker — Coding + Files)

| Component | Specification | Purpose |
|-----------|--------------|---------|
| CPU | Intel i5-12400 (6C/12T) | Coding agent + dev tools |
| RAM | 32 GB DDR4-3200 (2×16) | Coolify + Gitea + n8n |
| GPU | — (optional T4) | Local embeddings |
| Storage | 1TB NVMe (sys) + 2TB SSD (data) | Nextcloud + Photoprism |
| Network | 2.5 GbE | Headscale client |
| PSU | 500W 80+ Bronze | CPU-only |

## Power Consumption

| Server | Idle | Full Load | Annual Electricity (0.20 €/kWh) |
|--------|------|-----------|-------------------------------|
| Main | ~80W | 250-400W | ~250-600 € |
| HP Agent | ~30W | 80-150W | ~150-300 € |
| **Total** | ~110W | 330-550W | **~400-900 €/year** |

## BIOS Settings

- UEFI mode (not Legacy)
- Above 4G Decoding: **Enabled**
- Resizable BAR: **Enabled**
- Secure Boot: **Disabled** (for NVIDIA open driver)
- GRUB: `pci=realloc nvidia-drm.modeset=1 nvidia-drm.fbdev=1`
