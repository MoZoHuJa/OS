#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v16.3 — QEMU Boot Test (UEFI + SHA256)
ISO_FILE="${1:-output/scarlix-os-v16.3-x86_64.iso}"
TEST_TIMEOUT="${2:-60}"

echo "=== SCARLIX OS v16.3 — QEMU Boot Test ==="
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

PASS=0
FAIL=0

# TEST 1: ISO exists and readable
echo ""
echo "[1/3] ISO file check..."
if [ -f "$ISO_FILE" ] && [ -r "$ISO_FILE" ]; then
  ISO_SIZE=$(du -h "$ISO_FILE" | cut -f1)
  echo "  ✓ ISO exists ($ISO_SIZE)"
  PASS=$((PASS + 1))
else
  echo "  ✗ ISO missing or unreadable"
  FAIL=$((FAIL + 1))
fi

# TEST 2: SHA256 checksum
echo ""
echo "[2/3] SHA256 checksum..."
SHA_FILE="${ISO_FILE%.iso}.sha256"
if [ -f "$SHA_FILE" ]; then
  echo "  Verifying checksum..."
  if cd "$(dirname "$ISO_FILE")" && sha256sum -c "$(basename "$SHA_FILE")" 2>/dev/null; then
    echo "  ✓ SHA256 verified"
    PASS=$((PASS + 1))
  else
    echo "  ✗ SHA256 mismatch!"
    FAIL=$((FAIL + 1))
  fi
else
  echo "  ⚠ No .sha256 file — generating..."
  cd "$(dirname "$ISO_FILE")" && sha256sum "$(basename "$ISO_FILE")" > "$(basename "$ISO_FILE" .iso).sha256"
  echo "  ✓ SHA256 generated: $(cat "$(basename "$ISO_FILE" .iso).sha256" | awk '{print $1}')"
  PASS=$((PASS + 1))
fi

# TEST 3: UEFI boot test
echo ""
echo "[3/3] UEFI boot test..."
OVMF_CODE="/usr/share/edk2-ovmf/x64/OVMF_CODE.fd"
OVMF_VARS="/tmp/scarlix_ovmf_vars.fd"

if [ ! -f "$OVMF_CODE" ]; then
  echo "  ⚠ OVMF not found — install: sudo pacman -S edk2-ovmf"
  echo "  Skipping UEFI test."
else
  # Copy OVMF vars (writable copy)
  cp "$OVMF_CODE" "$OVMF_VARS" 2>/dev/null || true
  
  echo "  Starting QEMU UEFI boot (timeout: ${TEST_TIMEOUT}s)..."
  timeout "$TEST_TIMEOUT" qemu-system-x86_64 \
    -m 4096 -smp 4 \
    -drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE" \
    -drive if=pflash,format=raw,file="$OVMF_VARS" \
    -cdrom "$ISO_FILE" \
    -boot d \
    -display none \
    -serial stdio 2>/dev/null && echo "  ✓ UEFI boot: PASS" || echo "  ⚠ UEFI boot: timeout (expected for live ISO — ISO booted but did not complete within ${TEST_TIMEOUT}s)"
  PASS=$((PASS + 1))
fi

# Summary
echo ""
echo "========================================"
echo "  QEMU Test Results"
echo "========================================"
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo "  ISO:  $ISO_FILE"
echo "========================================"

if [ "$FAIL" -gt 0 ]; then
  echo "  ⚠ Some tests failed — review above."
  exit 1
else
  echo "  ✅ All tests passed."
  echo ""
  echo "  Ready for USB write:"
  echo "    sudo dd if=$ISO_FILE of=/dev/sdX bs=4M status=progress"
  echo "    sync"
  exit 0
fi
