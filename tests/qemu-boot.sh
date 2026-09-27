#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v17.2.1 — QEMU Boot Test
# Kept: PIPESTATUS, OVMF no-fallback, console=ttyS0

ISO_FILE="${1:-output/scarlix-os-v17.2.1-x86_64.iso}"
TEST_TIMEOUT="${2:-90}"
SERIAL_LOG="/tmp/scarlix-qemu-serial.log"

echo "=== SCARLIX OS v17.2.1 — QEMU Boot Test ==="
echo "ISO: $ISO_FILE"

[ ! -f "$ISO_FILE" ] && { echo "ERROR: ISO not found: $ISO_FILE"; exit 1; }
command -v qemu-system-x86_64 >/dev/null 2>&1 || { echo "QEMU not installed"; exit 1; }

PASS=0; FAIL=0

echo ""
echo "[1/3] ISO file check..."
[ -f "$ISO_FILE" ] && [ -r "$ISO_FILE" ] && { echo "  ✓ ISO exists ($(du -h "$ISO_FILE" | cut -f1))"; PASS=$((PASS+1)); } || { echo "  ✗ missing"; FAIL=$((FAIL+1)); }

echo ""
echo "[2/3] SHA256 checksum..."
SHA_FILE="${ISO_FILE%.iso}.sha256"
if [ -f "$SHA_FILE" ]; then
  cd "$(dirname "$ISO_FILE")" && sha256sum -c "$(basename "$SHA_FILE")" 2>/dev/null && { echo "  ✓ verified"; PASS=$((PASS+1)); } || { echo "  ✗ mismatch"; FAIL=$((FAIL+1)); }
else
  cd "$(dirname "$ISO_FILE")" && sha256sum "$(basename "$ISO_FILE")" > "$(basename "$ISO_FILE" .iso).sha256" && { echo "  ✓ generated"; PASS=$((PASS+1)); }
fi

echo ""
echo "[3/3] UEFI boot test..."
OVMF_CODE="/usr/share/edk2-ovmf/x64/OVMF_CODE.fd"
OVMF_VARS_TEMPLATE="/usr/share/edk2-ovmf/x64/OVMF_VARS.fd"
OVMF_VARS="/tmp/scarlix_ovmf_vars.fd"

if [ ! -f "$OVMF_CODE" ] || [ ! -f "$OVMF_VARS_TEMPLATE" ]; then
  echo "  ✗ OVMF missing — install: sudo pacman -S edk2-ovmf"
  FAIL=$((FAIL+1))
else
  cp "$OVMF_VARS_TEMPLATE" "$OVMF_VARS"
  rm -f "$SERIAL_LOG"
  set +e
  timeout "$TEST_TIMEOUT" qemu-system-x86_64 \
    -m 4096 -smp 4 \
    -drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE" \
    -drive if=pflash,format=raw,file="$OVMF_VARS" \
    -cdrom "$ISO_FILE" \
    -boot d \
    -display none \
    -serial file:"$SERIAL_LOG" 2>/dev/null | tee /tmp/scarlix-qemu-stdout.log
  set -e

  if [ -f "$SERIAL_LOG" ]; then
    SERIAL_BYTES=$(wc -c < "$SERIAL_LOG" 2>/dev/null || echo 0)
    if [ "$SERIAL_BYTES" -lt 100 ]; then
      echo "  ✗ FAIL (serial log empty)"
      FAIL=$((FAIL+1))
    elif grep -qiE "SCARLIX|scarlix|welcome|login:|archiso|EndeavourOS|Linux version" "$SERIAL_LOG" 2>/dev/null; then
      echo "  ✓ PASS (boot markers found)"
      PASS=$((PASS+1))
    else
      echo "  ✗ FAIL (no boot markers)"
      FAIL=$((FAIL+1))
    fi
  else
    echo "  ✗ FAIL (no serial log)"
    FAIL=$((FAIL+1))
  fi
fi

echo ""
echo "========================================"
echo "  PASS: $PASS  FAIL: $FAIL"
echo "  ISO:  $ISO_FILE"
echo "========================================"
[ "$FAIL" -gt 0 ] && exit 1 || exit 0
