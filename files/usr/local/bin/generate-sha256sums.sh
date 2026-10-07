#!/usr/bin/env bash
# SCARLIX OS v19.0.3 — SHA256SUMS manifest generator for release integrity
#
# v18.7.4 P2: Generate SHA256SUMS of all critical SCARLIX files so users can
# verify release integrity after install/upgrade (was: no manifest → silent
# tampering / partial-upgrade risk).
# v18.8.3 P2 (P2-11): Expanded manifest — now includes docker-compose files,
#   litellm config, Dockerfiles, install.sh, generate-env.sh, etc. (was: only
#   host bin scripts + models.yaml + VERSION → couldn't detect tampering of
#   compose files or Dockerfiles.)
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
# v18.8.3 P2 (P2-11): Expanded to include compose files, configs, Dockerfiles
CRITICAL_FILES=(
  # Host bin scripts
  /usr/local/bin/scarlix-mode
  /usr/local/bin/scarlix-wizard
  /usr/local/bin/scarlix-doctor
  /usr/local/bin/scarlix-host-bridge
  /usr/local/bin/download-models.sh
  /usr/local/bin/model-manager.sh
  /usr/local/bin/generate-sha256sums.sh
  /usr/local/bin/generate-litellm-config.sh
  # v19.1.14: Added new Go binaries + scarlix CLI
  /usr/local/bin/scarlix
  /usr/local/bin/scarlix-smoke-test.sh
  /usr/local/bin/scarlix-bridge-reader
  /usr/local/bin/scarlix-gpu
  /usr/local/bin/scarlix-inventory
  /usr/local/bin/scarlix-contract
  /usr/local/bin/scarlix-monitor
  /usr/local/bin/scarlix-scheduler
  # Configs
  /etc/scarlix/models.yaml
  /etc/scarlix/VERSION
  /etc/systemd/system/generate-env.sh
  # Docker compose files (AI stack)
  /opt/scarlix/ai/sglang/docker-compose.yml
  /opt/scarlix/ai/vllm/docker-compose.yml
  /opt/scarlix/ai/llamacpp/docker-compose.yml
  /opt/scarlix/ai/ollama/docker-compose.yml
  /opt/scarlix/ai/litellm/docker-compose.yml
  /opt/scarlix/ai/litellm/config.yaml
  /opt/scarlix/ai/comfyui/docker-compose.yml
  /opt/scarlix/gaming/docker-compose.yml
  /opt/scarlix/scarlihq/docker-compose.yml
  # Dockerfiles
  /opt/scarlix/scarlihq/Dockerfile
  /opt/scarlix/ai/musicgen/Dockerfile
  /opt/scarlix/ai/video/Dockerfile
  # Installer
  /opt/scarlix/install.sh
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
