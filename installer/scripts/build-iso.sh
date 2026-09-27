#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v17.2.1 — archiso ISO Builder (Minimal Baseline + auto 5-tier)

VERSION="17.2.1"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
WORK_DIR="/tmp/scarlix-iso-build"
OUTPUT_DIR="$REPO_ROOT/output"
LOG_FILE="/var/log/scarlix-iso-build.log"

mkdir -p "$WORK_DIR" "$OUTPUT_DIR"

echo "================================================" | tee "$LOG_FILE"
echo "  SCARLIX OS v${VERSION} — EndeavourOS ISO Builder" | tee -a "$LOG_FILE"
echo "  (Minimal Baseline + auto 5-tier for 2+ GPU)" | tee -a "$LOG_FILE"
echo "================================================" | tee -a "$LOG_FILE"

echo "[0/8] Version consistency check..." | tee -a "$LOG_FILE"
PROFILE_VERSION=$(grep 'iso_version=' "$REPO_ROOT/scarlix/profiledef.sh" | cut -d'"' -f2)
README_VERSION=$(grep 'Version:\*\* v' "$REPO_ROOT/README.md" | head -1 | grep -oP 'v[\d.]+' | sed 's/^v//')
VERSION_MISMATCH=0
[ "$VERSION" != "$PROFILE_VERSION" ] && { echo "ERROR: build vs profile ($VERSION vs $PROFILE_VERSION)"; VERSION_MISMATCH=1; }
[ -z "$README_VERSION" ] && { echo "ERROR: README version not found"; VERSION_MISMATCH=1; }
[ "$VERSION" != "$README_VERSION" ] && { echo "ERROR: build vs README ($VERSION vs $README_VERSION)"; VERSION_MISMATCH=1; }
[ "$VERSION_MISMATCH" -ne 0 ] && { echo "FATAL: Aborting."; exit 1; }
echo "  ✓ Versions match: $VERSION" | tee -a "$LOG_FILE"

echo "[1/8] Checking dependencies..." | tee -a "$LOG_FILE"
for cmd in mkarchiso pacman; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "ERROR: $cmd not found. Install: sudo pacman -S archiso"; exit 1; }
done
echo "  ✓ Dependencies OK" | tee -a "$LOG_FILE"

echo "[2/8] Using archiso profile: scarlix/" | tee -a "$LOG_FILE"
PROFILE_DIR="$REPO_ROOT/scarlix"
[ ! -d "$PROFILE_DIR" ] && { echo "ERROR: Profile not found"; exit 1; }
echo "  ✓ Profile found" | tee -a "$LOG_FILE"

echo "[3/8] Building ISO with mkarchiso..." | tee -a "$LOG_FILE"
set +e
sudo mkarchiso -v -w "$WORK_DIR" -o "$OUTPUT_DIR" "$PROFILE_DIR" 2>&1 | tee -a "$LOG_FILE"
MKARCHISO_EXIT=${PIPESTATUS[0]}
set -e
[ "$MKARCHISO_EXIT" -ne 0 ] && { echo "ERROR: mkarchiso failed ($MKARCHISO_EXIT)"; exit 1; }

ISO_FILE=$(ls -t "$OUTPUT_DIR"/*.iso 2>/dev/null | head -1)
[ -z "$ISO_FILE" ] && { echo "ERROR: ISO not found"; exit 1; }
echo "[4/8] ISO created: $(basename "$ISO_FILE")" | tee -a "$LOG_FILE"

echo "[5/8] Generating SHA256..." | tee -a "$LOG_FILE"
cd "$OUTPUT_DIR"
sha256sum "$(basename "$ISO_FILE")" > "$(basename "$ISO_FILE" .iso).sha256"
ISO_SIZE=$(du -h "$ISO_FILE" | cut -f1)
echo "[6/8] ISO size: $ISO_SIZE" | tee -a "$LOG_FILE"

echo "[7/8] Running QEMU boot test..." | tee -a "$LOG_FILE"
if [ -f "$REPO_ROOT/tests/qemu-boot.sh" ]; then
  set +e
  bash "$REPO_ROOT/tests/qemu-boot.sh" "$ISO_FILE" 2>&1 | tee -a "$LOG_FILE"
  QEMU_EXIT=${PIPESTATUS[0]}
  set -e
  [ "$QEMU_EXIT" -ne 0 ] && echo "⚠ QEMU test had failures" | tee -a "$LOG_FILE" || echo "  ✓ QEMU passed" | tee -a "$LOG_FILE"
fi

echo "[8/8] Done!" | tee -a "$LOG_FILE"
echo "================================================" | tee -a "$LOG_FILE"
echo "  ✅ SCARLIX OS v${VERSION} — BUILD COMPLETE" | tee -a "$LOG_FILE"
echo "  Auto 5-tier for 2+ NVIDIA GPU (vLLM TP=2)" | tee -a "$LOG_FILE"
echo "  ISO: $ISO_FILE" | tee -a "$LOG_FILE"
echo "  Size: $ISO_SIZE" | tee -a "$LOG_FILE"
echo "================================================" | tee -a "$LOG_FILE"
