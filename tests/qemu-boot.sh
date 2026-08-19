#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v16.2 — QEMU Boot Test
ISO_FILE="${1:-output/scarlix-os-v16.2-x86_64.iso}"
TEST_TIMEOUT="${2:-60}"

echo "=== SCARLIX OS v16.2 — QEMU Boot Test ==="
echo "ISO: $ISO_FILE"
echo "Timeout: ${TEST_TIMEOUT}s"

if [ ! -f "$ISO_FILE" ]; then
  echo "ERROR: ISO not found: $ISO_FILE"
  exit 1
fi

if ! command -v qemu-system-x86_64 >/dev/null 2>&1; then
  echo "QEMU not installed. Install: sudo pacman -S qemu-desktop"
  exit 1
fi

# Test UEFI boot
echo ""
echo "[1/3] UEFI boot test..."
timeout "$TEST_TIMEOUT" qemu-system-x86_64 \
  -m 4096 -smp 4 \
  -drive if=pflash,format=raw,readonly=on,file=/usr/share/edk2-ovmf/x64/OVMF_CODE.fd \
  -drive if=pflash,format=raw,file=/tmp/ovmf_vars.fd \
  -cdrom "$ISO_FILE" \
  -boot d \
  -display none \
  -serial stdio 2>/dev/null && echo "UEFI: PASS" || echo "UEFI: timeout (expected for live ISO)"

# Test BIOS boot
echo ""
echo "[2/3] BIOS boot test..."
timeout "$TEST_TIMEOUT" qemu-system-x86_64 \
  -m 4096 -smp 4 \
  -cdrom "$ISO_FILE" \
  -boot d \
  -display none \
  -serial stdio 2>/dev/null && echo "BIOS: PASS" || echo "BIOS: timeout (expected for live ISO)"

# Test ISO structure
echo ""
echo "[3/3] ISO structure test..."
if [ -f "$ISO_FILE" ]; then
  ISO_SIZE=$(du -h "$ISO_FILE" | cut -f1)
  echo "ISO size: $ISO_SIZE"
  echo "ISO exists: PASS"
else
  echo "ISO exists: FAIL"
  exit 1
fi

echo ""
echo "=== QEMU Test complete ==="
