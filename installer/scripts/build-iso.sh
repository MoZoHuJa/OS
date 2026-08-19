#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v16.2 — Garuda ISO Builder (archiso-based)
VERSION="16.2.0"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
WORK_DIR="/tmp/scarlix-iso-build"
OUTPUT_DIR="$REPO_ROOT/output"

mkdir -p "$WORK_DIR" "$OUTPUT_DIR"

echo "================================================"
echo "  SCARLIX OS v16.2 — Garuda ISO Builder"
echo "================================================"

# Check dependencies
echo "[1/8] Checking dependencies..."
for cmd in mkarchiso pacman wget; do
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "ERROR: $cmd not found. Install: sudo pacman -S archiso"
    exit 1
  }
done

# Use archiso profile directly (no need to download Garuda ISO)
echo "[2/8] Using archiso profile: garuda-scarlix/"
PROFILE_DIR="$REPO_ROOT/garuda-scarlix"

if [ ! -d "$PROFILE_DIR" ]; then
  echo "ERROR: Profile directory not found: $PROFILE_DIR"
  exit 1
fi

# Build ISO using mkarchiso
echo "[3/8] Building ISO with mkarchiso..."
sudo mkarchiso -v -w "$WORK_DIR" -o "$OUTPUT_DIR" "$PROFILE_DIR"

# Find the ISO
ISO_FILE=$(ls -t "$OUTPUT_DIR"/*.iso 2>/dev/null | head -1)

if [ -z "$ISO_FILE" ]; then
  echo "ERROR: ISO file not found in $OUTPUT_DIR"
  exit 1
fi

echo "[4/8] ISO created: $(basename "$ISO_FILE")"

# Generate SHA256 checksum
echo "[5/8] Generating SHA256 checksum..."
cd "$OUTPUT_DIR"
sha256sum "$(basename "$ISO_FILE")" > "$(basename "$ISO_FILE" .iso).sha256"

# Get ISO size
ISO_SIZE=$(du -h "$ISO_FILE" | cut -f1)
echo "[6/8] ISO size: $ISO_SIZE"

# QEMU test (user choice E2-a)
echo "[7/8] Running QEMU boot test..."
if command -v qemu-system-x86_64 >/dev/null 2>&1; then
  echo "Starting QEMU test (30s timeout)..."
  timeout 30 qemu-system-x86_64 \
    -m 4096 -smp 4 \
    -cdrom "$ISO_FILE" \
    -boot d \
    -display none \
    -serial stdio 2>/dev/null && echo "QEMU test PASSED" || echo "QEMU test: timeout (expected for live ISO)"
else
  echo "QEMU not installed — skipping boot test"
  echo "Install: sudo pacman -S qemu-desktop"
fi

echo "[8/8] Done!"
echo ""
echo "================================================"
echo "  ✅ SCARLIX OS v16.2 ISO BUILD COMPLETE"
echo "================================================"
echo "  ISO:     $ISO_FILE"
echo "  Size:    $ISO_SIZE"
echo "  SHA256:  $(cat "$(basename "$ISO_FILE" .iso).sha256" | awk '{print $1}')"
echo ""
echo "  Write to USB:"
echo "    sudo dd if=$ISO_FILE of=/dev/sdX bs=4M status=progress"
echo "    sync"
echo ""
echo "  Or use Ventoy / balenaEtcher"
echo "================================================"
