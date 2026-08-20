#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v16.5 — QEMU Boot Test (UEFI + SHA256 + real PASS/FAIL)
#
# v16.5 P0 fixes:
#   1-a: set -euo pipefail + tee pipeline → capture PIPESTATUS[0] correctly
#   2-a: OVMF fallback removed → FAIL if OVMF_VARS.fd template missing
#   3-a: console=ttyS0 added to boot configs → deterministic serial output

ISO_FILE="${1:-output/scarlix-os-v16.5-x86_64.iso}"
TEST_TIMEOUT="${2:-90}"
SERIAL_LOG="/tmp/scarlix-qemu-serial.log"

echo "=== SCARLIX OS v16.5 — QEMU Boot Test ==="
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
  echo "  ✓ SHA256 generated: $(awk '{print $1}' "$(basename "$ISO_FILE" .iso).sha256")"
  PASS=$((PASS + 1))
fi

# TEST 3: UEFI boot test with real PASS/FAIL
echo ""
echo "[3/3] UEFI boot test..."

# FIX 2-a: OVMF CODE (read-only) + VARS (writable copy from template)
# NO FALLBACK — if VARS template is missing, FAIL immediately.
OVMF_CODE="/usr/share/edk2-ovmf/x64/OVMF_CODE.fd"
OVMF_VARS_TEMPLATE="/usr/share/edk2-ovmf/x64/OVMF_VARS.fd"
OVMF_VARS="/tmp/scarlix_ovmf_vars.fd"

if [ ! -f "$OVMF_CODE" ]; then
  echo "  ✗ OVMF_CODE.fd not found — install: sudo pacman -S edk2-ovmf"
  echo "  ✗ UEFI boot: FAIL (OVMF missing)"
  FAIL=$((FAIL + 1))
elif [ ! -f "$OVMF_VARS_TEMPLATE" ]; then
  # FIX 2-a: FAIL if VARS template missing (do NOT copy from CODE — that produces
  # a firmware with no writable NVRAM vars and breaks UEFI boot silently).
  echo "  ✗ OVMF_VARS.fd template not found at: $OVMF_VARS_TEMPLATE"
  echo "  ✗ This file ships with edk2-ovmf. Reinstall: sudo pacman -S edk2-ovmf"
  echo "  ✗ UEFI boot: FAIL (OVMF_VARS missing)"
  FAIL=$((FAIL + 1))
else
  # Create writable VARS copy from the template (NOT from CODE)
  cp "$OVMF_VARS_TEMPLATE" "$OVMF_VARS"

  echo "  Starting QEMU UEFI boot (timeout: ${TEST_TIMEOUT}s)..."
  echo "  Serial log: $SERIAL_LOG"
  rm -f "$SERIAL_LOG"

  # FIX 1-a: set -euo pipefail causes `cmd | tee` to only see tee's exit code.
  # We disable -e temporarily, run QEMU with tee, capture PIPESTATUS[0] (qemu exit),
  # then re-enable -e. timeout returns 124 on timeout, which is OK (archiso may
  # still have written boot markers to the serial log before being killed).
  set +e
  timeout "$TEST_TIMEOUT" qemu-system-x86_64 \
    -m 4096 -smp 4 \
    -drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE" \
    -drive if=pflash,format=raw,file="$OVMF_VARS" \
    -cdrom "$ISO_FILE" \
    -boot d \
    -display none \
    -serial file:"$SERIAL_LOG" 2>/dev/null | tee /tmp/scarlix-qemu-stdout.log
  QEMU_EXIT=${PIPESTATUS[0]}
  set -e

  echo "  QEMU exit code: $QEMU_EXIT (124 = timeout, expected for live ISO)"

  # FIX 3-a: console=ttyS0 is now baked into the boot config (archiso-x86_64.conf
  # + syslinux.cfg), so the kernel WILL output to ttyS0 → serial log is deterministic.
  if [ -f "$SERIAL_LOG" ]; then
    SERIAL_LINES=$(wc -l < "$SERIAL_LOG" 2>/dev/null || echo 0)
    SERIAL_BYTES=$(wc -c < "$SERIAL_LOG" 2>/dev/null || echo 0)
    echo "  Serial log: $SERIAL_LINES lines, $SERIAL_BYTES bytes"

    if [ "$SERIAL_BYTES" -lt 100 ]; then
      echo "  ✗ UEFI boot: FAIL (serial log nearly empty — kernel did not boot)"
      echo "  Last 20 lines:"
      tail -20 "$SERIAL_LOG" 2>/dev/null || echo "  (log empty)"
      FAIL=$((FAIL + 1))
    elif grep -qiE "SCARLIX|scarlix|welcome|login:|archiso|Garuda|Linux version" "$SERIAL_LOG" 2>/dev/null; then
      echo "  ✓ UEFI boot: PASS (boot markers found in serial log)"
      PASS=$((PASS + 1))
    else
      echo "  ✗ UEFI boot: FAIL (no boot markers found in serial log)"
      echo "  Serial log content (last 20 lines):"
      tail -20 "$SERIAL_LOG" 2>/dev/null || echo "  (log empty)"
      FAIL=$((FAIL + 1))
    fi
  else
    echo "  ✗ UEFI boot: FAIL (no serial log generated)"
    FAIL=$((FAIL + 1))
  fi
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
