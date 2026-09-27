#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v17.0 — EndeavourOS Edition — First Boot Setup
# Logs every step to /var/log/scarlix/first-boot.log with SUCCESS/FAILED.
# Continues on error (does not stop).
#
# v17.0 changes vs v16.5:
#   - Removed all Garuda assumptions (/etc/garuda-release etc.)
#   - Added explicit BTRFS subvolume layout (was automatic on Garuda)
#   - Added Snapper configs for root + home + timeline/cleanup timers
#   - Kept: ZRAM (zram-generator.conf), chattr +C CoW disable, NVIDIA post-install,
#           model-manager.timer enable, all Docker service starts

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

log "=== SCARLIX OS v17.0 — EndeavourOS Edition — First Boot ==="
log "Base: $(cat /etc/os-release 2>/dev/null | grep '^PRETTY_NAME=' | cut -d'"' -f2 || echo 'EndeavourOS')"
log "Kernel: $(uname -r)"

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

# ============================================================================
# STEP 1: BTRFS subvolumes (was automatic on Garuda, now explicit in v17)
# ============================================================================
log ""
log "--- BTRFS subvolume layout ---"

# Determine root mount point (Calamares mounts / at /mnt during install, but
# at first-boot we're on the real /). Only create missing subvolumes.
ROOT_DEV=$(findmnt -no SOURCE / 2>/dev/null || echo "")
if echo "$ROOT_DEV" | grep -q btrfs; then
  log "Root is on BTRFS ($ROOT_DEV) — verifying subvolume layout"

  # Mount the BTRFS top-level temporarily to inspect/create subvols
  BTRFS_TMP="/mnt/btrfs-top"
  mkdir -p "$BTRFS_TMP"
  if mount "$ROOT_DEV" "$BTRFS_TMP" 2>/dev/null; then
    # Create missing subvolumes (idempotent)
    for subvol in @ @home @root @srv @var_log @var_lib_docker @models @snapshots; do
      if [ ! -d "$BTRFS_TMP/$subvol" ] && [ ! -e "$BTRFS_TMP/$subvol" ]; then
        if btrfs subvolume create "$BTRFS_TMP/$subvol" >> "$LOG_FILE" 2>&1; then
          log "  Created subvolume: $subvol"
        else
          log "  (could not create $subvol — may already exist or non-BTRFS)"
        fi
      else
        log "  Subvolume exists: $subvol"
      fi
    done
    umount "$BTRFS_TMP" 2>/dev/null || true
  else
    log "  (could not mount BTRFS top-level for subvol check — skipping)"
  fi
else
  log "Root is NOT on BTRFS — skipping subvolume layout (non-BTRFS filesystem)"
fi

# ============================================================================
# STEP 2: Snapper configs + timers (was auto from Garuda, now explicit)
# ============================================================================
log ""
log "--- Snapper configuration ---"

setup_snapper() {
  local name="$1"
  local path="$2"
  if [ -d "$path" ]; then
    if snapper -c "$name" list >/dev/null 2>&1; then
      log "  Snapper config '$name' already exists for $path"
    else
      if snapper -c "$name" create-config "$path" >> "$LOG_FILE" 2>&1; then
        log "  Created Snapper config: $name → $path"
      else
        log "  (could not create Snapper config '$name' — non-BTRFS or already exists)"
      fi
    fi
  else
    log "  (path $path does not exist — skipping Snapper config $name)"
  fi
}

if command -v snapper >/dev/null 2>&1; then
  setup_snapper "root" "/"
  setup_snapper "home" "/home"

  # Enable timeline + cleanup timers (auto snapshots hourly + cleanup)
  if systemctl enable --now snapper-timeline.timer >> "$LOG_FILE" 2>&1; then
    log_success "Snapper timeline timer"
  else
    log_failed "Snapper timeline timer"
  fi
  if systemctl enable --now snapper-cleanup.timer >> "$LOG_FILE" 2>&1; then
    log_success "Snapper cleanup timer"
  else
    log_failed "Snapper cleanup timer"
  fi
else
  log_failed "Snapper not installed (should be in packages.x86_64)"
fi

# ============================================================================
# STEP 3: Create directories + disable BTRFS CoW on heavy stores (kept v16.5)
# ============================================================================
log ""
log "--- Directory setup + BTRFS CoW disable ---"

mkdir -p /opt/scarlix /models /var/lib/scarlix /etc/scarlix/{profiles,secrets}
mkdir -p /mnt/{files,games,photos,backup/restic}
mkdir -p /var/lib/docker
chown -R scarlix:scarlix /opt/scarlix /models /var/lib/scarlix /etc/scarlix /mnt

# Disable CoW on heavy mutable stores (chattr +C must run on EMPTY dirs)
disable_cow() {
  local target="$1"
  if command -v chattr >/dev/null 2>&1; then
    if chattr +C "$target" 2>/dev/null; then
      log "  CoW disabled: $target"
    else
      log "  (CoW not applicable on $target — non-BTRFS or already has files)"
    fi
  fi
}

disable_cow /models
disable_cow /mnt/games
disable_cow /var/lib/docker
disable_cow /var/lib/scarlix
log_success "BTRFS CoW configuration"

# ============================================================================
# STEP 4: ZRAM verify (config in zram-generator.conf, shipped via airootfs)
# ============================================================================
log ""
log "--- ZRAM ---"
if [ -f /etc/systemd/zram-generator.conf ]; then
  if systemctl start systemd-zram-setup@zram0 2>/dev/null || zramctl zram0 >/dev/null 2>&1; then
    log_success "ZRAM active: $(zramctl zram0 2>/dev/null | tail -1 | awk '{print $1, $3, $4}')"
  else
    log "  ZRAM will activate on next boot (zram-generator)"
  fi
else
  log_failed "zram-generator.conf missing"
fi

# ============================================================================
# STEP 5: NVIDIA + CUDA install (Main PC only — post-install keeps ISO small)
# ============================================================================
if [ "$PC_TYPE" == "main" ]; then
  log ""
  log "--- NVIDIA + CUDA install (Main PC) ---"

  log "Installing NVIDIA open driver (Turing+)..."
  if sudo pacman -S --noconfirm --needed nvidia-open nvidia-utils lib32-nvidia-utils nvidia-settings >> "$LOG_FILE" 2>&1; then
    log_success "NVIDIA open driver install"
  else
    log_failed "NVIDIA open driver install (trying proprietary fallback)"
    if sudo pacman -S --noconfirm --needed nvidia nvidia-utils lib32-nvidia-utils >> "$LOG_FILE" 2>&1; then
      log_success "NVIDIA proprietary driver install (fallback)"
    else
      log_failed "NVIDIA proprietary driver install"
    fi
  fi

  log "Installing CUDA + cuDNN..."
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

  log "Installing NVIDIA Container Toolkit (via AUR)..."
  if command -v yay >/dev/null 2>&1; then
    if yay -S --noconfirm nvidia-container-toolkit >> "$LOG_FILE" 2>&1; then
      log_success "NVIDIA Container Toolkit"
      sudo nvidia-ctk runtime configure --runtime=docker >> "$LOG_FILE" 2>&1 && log_success "Docker NVIDIA runtime" || log_failed "Docker NVIDIA runtime"
    else
      log_failed "NVIDIA Container Toolkit"
    fi
  else
    log_failed "yay not installed — cannot install nvidia-container-toolkit"
  fi

  log "Setting nvidia_drm.modeset=1 in GRUB..."
  if [ -f /etc/default/grub ]; then
    if ! grep -q "nvidia_drm.modeset=1" /etc/default/grub; then
      sed -i 's/GRUB_CMDLINE_LINUX_DEFAULT="\(.*\)"/GRUB_CMDLINE_LINUX_DEFAULT="\1 nvidia_drm.modeset=1"/' /etc/default/grub
      grub-mkconfig -o /boot/grub/grub.cfg >> "$LOG_FILE" 2>&1 && log_success "GRUB nvidia_drm.modeset=1" || log_failed "GRUB update"
    else
      log "  nvidia_drm.modeset=1 already set"
    fi
  fi
fi

# ============================================================================
# STEP 6: Start Docker + pull stacks
# ============================================================================
log ""
log "--- Docker + services ---"

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

# ============================================================================
# STEP 7: Enable model-manager weekly timer
# ============================================================================
log ""
log "--- Model manager timer ---"
if [ -f /etc/systemd/system/model-manager.timer ]; then
  if systemctl enable model-manager.timer >> "$LOG_FILE" 2>&1; then
    log_success "model-manager.timer enabled (weekly Mon 04:00)"
  else
    log_failed "model-manager.timer enable"
  fi
else
  log_failed "model-manager.timer not installed"
fi

# ============================================================================
# STEP 8: Summary
# ============================================================================
log ""
log "========================================"
log "  SCARLIX OS v17.0 — First Boot Summary"
log "========================================"
log "  Base:        EndeavourOS (Arch)"
log "  Kernel:      $(uname -r)"
log "  LTS kernel:  $(pacman -Q linux-lts 2>/dev/null | head -1 || echo 'not installed')"
log "  PC Type:     $PC_TYPE"
log "  SUCCESS:     $SUCCESS_COUNT"
log "  FAILED:      $FAIL_COUNT"
log ""
log "  Services:"
echo -e "$SERVICES_STARTED" | tee -a "$LOG_FILE"
log ""
log "  Dashboard:   http://$(hostname -I | awk '{print $1}'):8090"
log "  Full log:    $LOG_FILE"
log "  Rollback:    sudo snapper -c root list"
log "  VRAM check:  scarlix-mode vram"
log "========================================"

# Mark as installed
touch /opt/scarlix/.installed

exit 0
