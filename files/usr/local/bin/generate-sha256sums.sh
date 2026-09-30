#!/usr/bin/env bash
# SCARLIX OS v18.8.1 — SHA256SUMS manifest generator for release integrity
#
# v18.7.4 P2: Generate SHA256SUMS of all critical SCARLIX files so users can
# verify release integrity after install/upgrade (was: no manifest → silent
# tampering / partial-upgrade risk).
#
# Usage: generate-sha256sums.sh [--output PATH]
#   Default output: /tmp/SCARLIX-v<version>-SHA256SUMS
set -euo pipefail

VERSION=$(cat /etc/scarlix/VERSION 2>/dev/null || echo "unknown")
OUTPUT="/tmp/SCARLIX-v${VERSION}-SHA256SUMS"

# Allow --output override
while [ $# -gt 0 ]; do
  case "$1" in
    --output)
      OUTPUT="$2"
      shift 2
      ;;
    -h|--help)
      echo "Usage: $0 [--output PATH]"
      echo "  Default output: ${OUTPUT}"
      exit 0
      ;;
    *)
      echo "ERROR: unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

# v18.7.4 P2: Critical files — if any are tampered, the SHA256SUMS will mismatch
CRITICAL_FILES=(
  /usr/local/bin/scarlix-mode
  /usr/local/bin/scarlix-wizard
  /usr/local/bin/scarlix-doctor
  /usr/local/bin/scarlix-host-bridge
  /usr/local/bin/download-models.sh
  /usr/local/bin/model-manager.sh
  /usr/local/bin/generate-sha256sums.sh
  /etc/scarlix/models.yaml
  /etc/scarlix/VERSION
)

{
  echo "# SCARLIX OS v${VERSION} — SHA256SUMS"
  echo "# Generated: $(date -Iseconds)"
  echo "# Host: $(hostname 2>/dev/null || echo unknown)"
  echo ""
  for f in "${CRITICAL_FILES[@]}"; do
    if [ -f "$f" ]; then
      sha256sum "$f"
    else
      echo "# MISSING: $f"
    fi
  done
} > "$OUTPUT"

echo "SHA256SUMS written to $OUTPUT"
cat "$OUTPUT"
