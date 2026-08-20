#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v16.5 — First Boot Setup
# Logs every step to /var/log/scarlix/first-boot.log with SUCCESS/FAILED
# Continues on error (does not stop)
#
# v16.5 P1 fixes:
#   10-a: BTRFS CoW disabled on /models, /mnt/games, /var/lib/docker via chattr +C

LOG_DIR="/var/log/scarlix"
LOG_FILE="$LOG_DIR/first-boot.log"
SUCCESS_COUNT=0
FAIL_COUNT=0
SERVICES_STARTED=""

mkdir -p "$LOG_DIR"

log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

log_success() {
  log "  ✓ SUCCESS: $1"
  SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
  SERVICES_STARTED="$SERVICES_STARTED\n  ✓ $1"
}

log_failed() {
  log "  ✗ FAILED: $1"
  FAIL_COUNT=$((FAIL_COUNT + 1))
  SERVICES_STARTED="$SERVICES_STARTED\n  ✗ $1"
}

log "=== SCARLIX OS v16.5 — First Boot ==="

# Check if already installed
if [ -f /opt/scarlix/.installed ]; then
  log "Already installed. Skipping."
  exit 0
fi

# Detect PC type
PC_TYPE=$(cat /etc/scarlix/pc_type 2>/dev/null || echo "")
if [ -z "$PC_TYPE" ]; then
  GPU_COUNT=$(lspci | grep -ic nvidia 2>/dev/null || echo 0)
  if [ "$GPU_COUNT" -gt 0 ]; then
    PC_TYPE="main"
  else
    PC_TYPE="hp_agent"
  fi
  echo "$PC_TYPE" | tee /etc/scarlix/pc_type >/dev/null
fi
log "PC Type: $PC_TYPE"

# Generate .env if missing
if [ ! -f /etc/scarlix/.env ]; then
  log "Generating .env..."
  /etc/systemd/system/generate-env.sh >> "$LOG_FILE" 2>&1 && log_success ".env generation" || log_failed ".env generation"
else
  log ".env already exists"
fi

# Create directories
mkdir -p /opt/scarlix /models /var/lib/scarlix /etc/scarlix/{profiles,secrets}
mkdir -p /mnt/{files,games,photos,backup/restic}
mkdir -p /var/lib/docker
chown -R scarlix:scarlix /opt/scarlix /models /var/lib/scarlix /etc/scarlix /mnt
log "Directories created"

# FIX 10-a: Disable Copy-on-Write on BTRFS for large mutable stores.
# These directories hold Docker images, model weights, and game files which
# are rewritten in place. CoW on BTRFS fragments these badly and wastes space.
# chattr +C must be applied on EMPTY directories (before any data is written),
# so we do it immediately after mkdir above.
disable_cow() {
  local target="$1"
  if command -v chattr >/dev/null 2>&1; then
    if chattr +C "$target" 2>/dev/null; then
      log "  CoW disabled: $target"
    else
      # Non-fatal: ext4 / non-btrfs filesystems don't support +C
      log "  (CoW not applicable on $target — non-BTRFS or already has files)"
    fi
  fi
}

log "Disabling BTRFS CoW on large stores..."
disable_cow /models
disable_cow /mnt/games
disable_cow /var/lib/docker
disable_cow /var/lib/scarlix
log_success "BTRFS CoW configuration"

# NVIDIA + CUDA install with logging (only Main PC)
if [ "$PC_TYPE" == "main" ]; then
  log "Installing NVIDIA driver..."
  if sudo pacman -S --noconfirm --needed nvidia-dkms nvidia-utils nvidia-settings lib32-nvidia-utils >> "$LOG_FILE" 2>&1; then
    log_success "NVIDIA driver install"
  else
    log_failed "NVIDIA driver install"
  fi

  log "Installing CUDA..."
  if sudo pacman -S --noconfirm --needed cuda cudnn >> "$LOG_FILE" 2>&1; then
    log_success "CUDA install"
  else
    log_failed "CUDA install"
  fi

  log "Rebuilding initramfs..."
  if sudo mkinitcpio -P >> "$LOG_FILE" 2>&1; then
    log_success "initramfs rebuild"
  else
    log_failed "initramfs rebuild"
  fi

  log "Installing NVIDIA Container Toolkit..."
  if yay -S --noconfirm nvidia-container-toolkit >> "$LOG_FILE" 2>&1; then
    log_success "NVIDIA Container Toolkit"
    sudo nvidia-ctk runtime configure --runtime=docker >> "$LOG_FILE" 2>&1 && log_success "Docker NVIDIA runtime" || log_failed "Docker NVIDIA runtime"
  else
    log_failed "NVIDIA Container Toolkit"
  fi
fi

# Start Docker
log "Starting Docker..."
if systemctl start docker >> "$LOG_FILE" 2>&1; then
  log_success "Docker start"
  sleep 3
else
  log_failed "Docker start"
fi

# Load environment
set -a; source /etc/scarlix/.env; set +a

# Start services with logging — absolute paths, continue on error
start_service() {
  local name="$1"
  local compose_file="$2"
  log "Starting: $name"
  if docker compose -f "$compose_file" up -d >> "$LOG_FILE" 2>&1; then
    log_success "$name"
  else
    log_failed "$name"
  fi
}

if [ "$PC_TYPE" == "main" ]; then
  log "--- Starting Main PC services ---"

  start_service "smg"           "/opt/scarlix-src/ai/smg/docker-compose.yml"
  start_service "SGLang"        "/opt/scarlix-src/ai/sglang/docker-compose.yml"
  start_service "Ollama Main"   "/opt/scarlix-src/ai/ollama/docker-compose-main.yml"
  start_service "Ollama Agent"  "/opt/scarlix-src/ai/ollama/docker-compose-agent.yml"
  start_service "Needle2"       "/opt/scarlix-src/ai/needle/docker-compose.yml"
  start_service "llama.cpp"     "/opt/scarlix-src/ai/llamacpp/docker-compose.yml"
  start_service "Network/Sec"   "/opt/scarlix-src/network/docker-compose.yml"
  start_service "Voice"         "/opt/scarlix-src/voice/docker-compose.yml"
  start_service "Buzz"          "/opt/scarlix-src/workspace/buzz/docker-compose.yml"
  start_service "Hermes"        "/opt/scarlix-src/agents/hermes/docker-compose.yml"
  start_service "ScarliHQ"      "/opt/scarlix-src/scarlihq/docker-compose.yml"
  start_service "Monitoring"    "/opt/scarlix-src/monitoring/docker-compose.yml"

  # Gaming (Docker only — Steam/Lutris are native)
  log "Starting Jellyfin + Minecraft..."
  if docker compose -f /opt/scarlix-src/gaming/docker-compose.yml up -d jellyfin minecraft >> "$LOG_FILE" 2>&1; then
    log_success "Jellyfin + Minecraft"
  else
    log_failed "Jellyfin + Minecraft"
  fi

  # Set default mode
  echo "ai" | tee /var/lib/scarlix/current-mode >/dev/null
  log "Default mode: ai"

  # Download models in background
  log "Starting model download in background..."
  nohup /etc/systemd/system/download-models.sh >> "$LOG_FILE" 2>&1 &
  log_success "Model download (background)"
else
  log "--- Starting HP Agent services ---"

  start_service "Coding Pipeline" "/opt/scarlix-src/coding-pipeline/docker-compose.yml"
  start_service "Media Tools"     "/opt/scarlix-src/media-tools/docker-compose.yml"
fi

# Summary
log ""
log "========================================"
log "  SCARLIX OS v16.5 — First Boot Summary"
log "========================================"
log "  SUCCESS: $SUCCESS_COUNT"
log "  FAILED:  $FAIL_COUNT"
log ""
log "  Services:"
echo -e "$SERVICES_STARTED" | tee -a "$LOG_FILE"
log ""
log "  Dashboard: http://$(hostname -I | awk '{print $1}'):8090"
log "  Full log:  $LOG_FILE"
log "========================================"

# Mark as installed
touch /opt/scarlix/.installed

exit 0
