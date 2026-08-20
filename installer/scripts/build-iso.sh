#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v16.5 — Garuda ISO Builder (archiso-based)
#
# v16.5 P0/P1 fixes:
#   1-a: QEMU test call uses PIPESTATUS[0] (set +e around tee pipeline)
#   4-a: README_VERSION validated against VERSION (fail build if mismatch)

VERSION="16.5.0"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
WORK_DIR="/tmp/scarlix-iso-build"
OUTPUT_DIR="$REPO_ROOT/output"
LOG_FILE="/var/log/scarlix-iso-build.log"

mkdir -p "$WORK_DIR" "$OUTPUT_DIR"

echo "================================================" | tee "$LOG_FILE"
echo "  SCARLIX OS v${VERSION} — Garuda ISO Builder" | tee -a "$LOG_FILE"
echo "================================================" | tee -a "$LOG_FILE"

# FIX 1-a + FIX 4-a: Version consistency check (build script + profiledef + README)
echo "[0/8] Version consistency check..." | tee -a "$LOG_FILE"
PROFILE_VERSION=$(grep 'iso_version=' "$REPO_ROOT/garuda-scarlix/profiledef.sh" | cut -d'"' -f2)
README_VERSION=$(grep 'Version:\*\* v' "$REPO_ROOT/README.md" | head -1 | grep -oP 'v[\d.]+' | sed 's/^v//')

VERSION_MISMATCH=0

if [ "$VERSION" != "$PROFILE_VERSION" ]; then
  echo "ERROR: Version mismatch (build-iso.sh vs profiledef.sh)!" | tee -a "$LOG_FILE"
  echo "  build-iso.sh:     $VERSION" | tee -a "$LOG_FILE"
  echo "  profiledef.sh:    $PROFILE_VERSION" | tee -a "$LOG_FILE"
  VERSION_MISMATCH=1
fi

# FIX 4-a: README_VERSION validation — fail build if README out of sync
if [ -z "$README_VERSION" ]; then
  echo "ERROR: Could not extract version from README.md" | tee -a "$LOG_FILE"
  echo "  Expected pattern: '**Version:** v16.5.0'" | tee -a "$LOG_FILE"
  VERSION_MISMATCH=1
elif [ "$VERSION" != "$README_VERSION" ]; then
  echo "ERROR: Version mismatch (build-iso.sh vs README.md)!" | tee -a "$LOG_FILE"
  echo "  build-iso.sh:     $VERSION" | tee -a "$LOG_FILE"
  echo "  README.md:        $README_VERSION" | tee -a "$LOG_FILE"
  VERSION_MISMATCH=1
fi

if [ "$VERSION_MISMATCH" -ne 0 ]; then
  echo "" | tee -a "$LOG_FILE"
  echo "FATAL: Version mismatch detected. Aborting build." | tee -a "$LOG_FILE"
  echo "Fix: bump VERSION in build-iso.sh, profiledef.sh, AND README.md to match." | tee -a "$LOG_FILE"
  exit 1
fi
echo "  ✓ Versions match (build/profile/README): $VERSION" | tee -a "$LOG_FILE"

# Check dependencies
echo "[1/8] Checking dependencies..." | tee -a "$LOG_FILE"
for cmd in mkarchiso pacman; do
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "ERROR: $cmd not found. Install: sudo pacman -S archiso" | tee -a "$LOG_FILE"
    exit 1
  }
done
echo "  ✓ Dependencies OK" | tee -a "$LOG_FILE"

# Use archiso profile
echo "[2/8] Using archiso profile: garuda-scarlix/" | tee -a "$LOG_FILE"
PROFILE_DIR="$REPO_ROOT/garuda-scarlix"

if [ ! -d "$PROFILE_DIR" ]; then
  echo "ERROR: Profile directory not found: $PROFILE_DIR" | tee -a "$LOG_FILE"
  exit 1
fi
echo "  ✓ Profile found" | tee -a "$LOG_FILE"

# Build ISO
echo "[3/8] Building ISO with mkarchiso..." | tee -a "$LOG_FILE"
# FIX 1-a: mkarchiso | tee pipeline — capture mkarchiso exit via PIPESTATUS[0]
set +e
sudo mkarchiso -v -w "$WORK_DIR" -o "$OUTPUT_DIR" "$PROFILE_DIR" 2>&1 | tee -a "$LOG_FILE"
MKARCHISO_EXIT=${PIPESTATUS[0]}
set -e

if [ "$MKARCHISO_EXIT" -ne 0 ]; then
  echo "ERROR: mkarchiso failed (exit $MKARCHISO_EXIT)" | tee -a "$LOG_FILE"
  exit 1
fi

# Find the ISO
ISO_FILE=$(ls -t "$OUTPUT_DIR"/*.iso 2>/dev/null | head -1)

if [ -z "$ISO_FILE" ]; then
  echo "ERROR: ISO file not found in $OUTPUT_DIR" | tee -a "$LOG_FILE"
  exit 1
fi

echo "[4/8] ISO created: $(basename "$ISO_FILE")" | tee -a "$LOG_FILE"

# Generate SHA256 checksum
echo "[5/8] Generating SHA256 checksum..." | tee -a "$LOG_FILE"
cd "$OUTPUT_DIR"
sha256sum "$(basename "$ISO_FILE")" > "$(basename "$ISO_FILE" .iso).sha256"

ISO_SIZE=$(du -h "$ISO_FILE" | cut -f1)
echo "[6/8] ISO size: $ISO_SIZE" | tee -a "$LOG_FILE"
echo "  SHA256: $(awk '{print $1}' "$(basename "$ISO_FILE" .iso).sha256")" | tee -a "$LOG_FILE"

# FIX 1-a: Unified QEMU test — capture real exit via PIPESTATUS[0] (not tee's exit)
echo "[7/8] Running QEMU boot test..." | tee -a "$LOG_FILE"
if [ -f "$REPO_ROOT/tests/qemu-boot.sh" ]; then
  set +e
  bash "$REPO_ROOT/tests/qemu-boot.sh" "$ISO_FILE" 2>&1 | tee -a "$LOG_FILE"
  QEMU_EXIT=${PIPESTATUS[0]}
  set -e
  if [ "$QEMU_EXIT" -ne 0 ]; then
    echo "⚠ QEMU test had failures (exit $QEMU_EXIT) — review above." | tee -a "$LOG_FILE"
    echo "⚠ ISO is still built, but boot verification failed." | tee -a "$LOG_FILE"
  else
    echo "  ✓ QEMU boot test passed." | tee -a "$LOG_FILE"
  fi
else
  echo "  tests/qemu-boot.sh not found — skipping QEMU test" | tee -a "$LOG_FILE"
fi

echo "[8/8] Done!" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"
echo "================================================" | tee -a "$LOG_FILE"
echo "  ✅ SCARLIX OS v${VERSION} ISO BUILD COMPLETE" | tee -a "$LOG_FILE"
echo "================================================" | tee -a "$LOG_FILE"
echo "  ISO:     $ISO_FILE" | tee -a "$LOG_FILE"
echo "  Size:    $ISO_SIZE" | tee -a "$LOG_FILE"
echo "  SHA256:  $(awk '{print $1}' "$(basename "$ISO_FILE" .iso).sha256")" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"
echo "  Write to USB:" | tee -a "$LOG_FILE"
echo "    sudo dd if=$ISO_FILE of=/dev/sdX bs=4M status=progress" | tee -a "$LOG_FILE"
echo "    sync" | tee -a "$LOG_FILE"
echo "================================================" | tee -a "$LOG_FILE"
