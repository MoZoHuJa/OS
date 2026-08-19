#!/usr/bin/env bash
set -euo pipefail
VERSION="16.1.0"
WORK_DIR="/tmp/scarlix-iso-builder"
OUTPUT_DIR="$HOME/scarlix-iso"
mkdir -p "$WORK_DIR" "$OUTPUT_DIR"

echo "=== SCARLIX OS v16.1 — Garuda ISO Builder ==="

# Download Garuda Dr460nized ISO
GARUDA_URL="https://downloads.garudalinux.org/iso/garuda-dr460nized/2401/garuda-dr460nized-linux-kde-2401-linux-zen.iso"
wget -O "$WORK_DIR/garuda-base.iso" "$GARUDA_URL"

# Extract
mkdir -p "$WORK_DIR/iso-content" "$WORK_DIR/iso-mount"
sudo mount -o loop "$WORK_DIR/garuda-base.iso" "$WORK_DIR/iso-mount"
cp -r "$WORK_DIR/iso-mount/"* "$WORK_DIR/iso-content/"
sudo umount "$WORK_DIR/iso-mount"

# Add SCARLIX overlay
cp -r ../{ai,workspace,agents,voice,network,security,monitoring,scarlihq,profiles,scripts,models.yaml,AGENTS.md,.env.template} \
  "$WORK_DIR/iso-content/opt/scarlix-src/"
cp -r . "$WORK_DIR/iso-content/opt/scarlix-installer/"

# GRUB config
cat > "$WORK_DIR/iso-content/boot/grub/grub.cfg" << 'GRUB'
set default=0
set timeout=15
insmod all_video
insmod gfxterm
set menu_color_normal=white/black
set menu_color_highlight=cyan/black

menuentry "  SCARLIX OS v16.1 — Main PC (Garuda Dr460nized + AI + Gaming)" {
  linux /boot/vmlinuz-linux-zen archisobasedir=garuda archisolabel=SCARLIX_V16 scarlix_pc=main
  initrd /boot/initramfs-linux-zen.img
}

menuentry "  SCARLIX OS v16.1 — HP Agent PC (Headless Worker)" {
  linux /boot/vmlinuz-linux-zen archisobasedir=garuda archisolabel=SCARLIX_V16 scarlix_pc=hp_agent
  initrd /boot/initramfs-linux-zen.img
}

menuentry "  Garuda Linux (Original — no SCARLIX)" {
  linux /boot/vmlinuz-linux-zen archisobasedir=garuda archisolabel=GARUDA
  initrd /boot/initramfs-linux-zen.img
}
GRUB

# First-boot service
cat > "$WORK_DIR/iso-content/etc/systemd/system/scarlix-first-boot.service" << 'SVC'
[Unit]
Description=SCARLIX OS v16.1 First Boot
After=network-online.target
Wants=network-online.target
ConditionPathExists=!/opt/scarlix/.installed
[Service]
Type=oneshot
ExecStart=/opt/scarlix-installer/scripts/first-boot.sh
ExecStartPost=/bin/touch /opt/scarlix/.installed
RemainAfterExit=yes
TimeoutStartSec=3600
[Install]
WantedBy=multi-user.target
SVC

# Build ISO
sudo xorriso -as mkisofs \
  -iso-level 3 -full-iso9660-filenames \
  -volid "SCARLIX_V16" \
  -eltorito-boot boot/grub/bios.img \
  -eltorito-alt-boot -e EFI/garuda/efiboot.img -no-emul-boot \
  -isohybrid-gpt-basdat \
  -o "$OUTPUT_DIR/scarlix-os-v16.1-garuda.iso" \
  "$WORK_DIR/iso-content"

echo "ISO: $OUTPUT_DIR/scarlix-os-v16.1-garuda.iso"
ls -lh "$OUTPUT_DIR/scarlix-os-v16.1-garuda.iso"
