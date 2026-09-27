#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v17.1 — EndeavourOS Edition — First Boot Setup (3-phase with checkpointing)
#
# FIX Q5b: Split into 3 idempotent phases with checkpointing.
#   If first-boot crashes, re-running it resumes from the last checkpoint.
#
# FIX Q6a: NVIDIA driver auto-detection (Turing+ → nvidia-open, older → nvidia proprietary)
# FIX Q4a: Install BOTH nvidia-open (for linux) + nvidia-open-lts (for linux-lts)
#
# Phase 1: BTRFS verify + Snapper configs + chattr +C + ZRAM verify
# Phase 2: NVIDIA auto-detect + install + GRUB modeset + initramfs rebuild
# Phase 3: Docker + services + model-manager.timer + default mode + model download

LOG_DIR="/var/log/scarlix"
LOG_FILE="$LOG_DIR/first-boot.log"
CHECKPOINT_DIR="/var/lib/scarlix"
SUCCESS_COUNT=0
FAIL_COUNT=0
SERVICES_STARTED=""

mkdir -p "$LOG_DIR" "$CHECKPOINT_DIR"

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

checkpoint() {
  local phase="$1"
  touch "$CHECKPOINT_DIR/.checkpoint-phase${phase}"
  log "  ⏸ Checkpoint: Phase $phase complete"
}

is_checkpoint() {
  local phase="$1"
  [ -f "$CHECKPOINT_DIR/.checkpoint-phase${phase}" ]
}

log "========================================"
log "  SCARLIX OS v17.1 — EndeavourOS Edition"
log "  First Boot Setup (3-phase checkpointed)"
log "========================================"
log "Base: $(grep '^PRETTY_NAME=' /etc/os-release 2>/dev/null | cut -d'"' -f2 || echo 'EndeavourOS')"
log "Kernel: $(uname -r)"

# Check if already fully installed
if [ -f /opt/scarlix/.installed ]; then
  log "Already installed. Skipping."
  exit 0
fi

# Detect PC type (Q16a: auto-detect, allow wizard override)
PC_TYPE=$(cat /etc/scarlix/pc_type 2>/dev/null || echo "")
if [ -z "$PC_TYPE" ]; then
  GPU_COUNT=$(lspci | grep -ic nvidia 2>/dev/null || echo 0)
  if [ "$GPU_COUNT" -gt 0 ]; then
    PC_TYPE="ai_server"
  else
    PC_TYPE="dev_workstation"
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

# Detect the real user (FIX Q10: scarlix user may have different name)
REAL_USER=$(grep -E '^[^:]+:x:1000:' /etc/passwd 2>/dev/null | cut -d: -f1 || echo "scarlix")
REAL_GROUP=$(id -gn "$REAL_USER" 2>/dev/null || echo "scarlix")
log "Detected user: $REAL_USER:$REAL_GROUP"

# ============================================================================
# PHASE 1: BTRFS + Snapper + CoW + ZRAM
# ============================================================================
log ""
log "========================================"
log "  PHASE 1: BTRFS + Snapper + CoW + ZRAM"
log "========================================"

if is_checkpoint 1; then
  log "Phase 1 already completed — skipping."
else
  # Create directories
  mkdir -p /opt/scarlix /models /var/lib/scarlix /etc/scarlix/{profiles,secrets}
  mkdir -p /mnt/{files,games,photos,backup/restic}
  mkdir -p /var/lib/docker
  chown -R "$REAL_USER:$REAL_GROUP" /opt/scarlix /models /var/lib/scarlix /etc/scarlix /mnt

  # BTRFS subvolumes were created by Calamares (Q3b mount.conf)
  # Here we just verify they exist
  log "Verifying BTRFS subvolumes (created by Calamares)..."
  ROOT_DEV=$(findmnt -no SOURCE / 2>/dev/null || echo "")
  if echo "$ROOT_DEV" | grep -q btrfs; then
    log "  Root is on BTRFS ($ROOT_DEV)"
    SUBVOLS_PRESENT=true
    for sv in @ @home @root @srv @var_log @var_lib_docker @models @snapshots; do
      if btrfs subvolume list / 2>/dev/null | grep -q "$sv"; then
        log "  ✓ Subvolume: $sv"
      else
        log "  ⚠ Subvolume missing: $sv (may have different name from Calamares)"
      fi
    done
  else
    log "  Root is NOT on BTRFS — skipping subvolume verification"
  fi

  # Snapper configs (root + home)
  if command -v snapper >/dev/null 2>&1; then
    if ! snapper -c root list >/dev/null 2>&1; then
      snapper -c root create-config / >> "$LOG_FILE" 2>&1 && log "  Created Snapper config: root" || log "  (root Snapper config may already exist)"
    else
      log "  Snapper config root already exists"
    fi

    if ! snapper -c home list >/dev/null 2>&1; then
      snapper -c home create-config /home >> "$LOG_FILE" 2>&1 && log "  Created Snapper config: home" || log "  (home Snapper config may already exist)"
    else
      log "  Snapper config home already exists"
    fi

    systemctl enable --now snapper-timeline.timer >> "$LOG_FILE" 2>&1 && log_success "Snapper timeline timer" || log_failed "Snapper timeline timer"
    systemctl enable --now snapper-cleanup.timer >> "$LOG_FILE" 2>&1 && log_success "Snapper cleanup timer" || log_failed "Snapper cleanup timer"
  else
    log_failed "Snapper not installed"
  fi

  # Disable CoW on heavy mutable stores (chattr +C on empty dirs)
  disable_cow() {
    local target="$1"
    if command -v chattr >/dev/null 2>&1; then
      if chattr +C "$target" 2>/dev/null; then
        log "  CoW disabled: $target"
      else
        log "  (CoW not applicable on $target — non-BTRFS or has existing files)"
      fi
    fi
  }
  disable_cow /models
  disable_cow /mnt/games
  disable_cow /var/lib/docker
  disable_cow /var/lib/scarlix
  log_success "BTRFS + Snapper + CoW setup"

  # ZRAM verify (zram-generator.conf shipped via airootfs)
  if [ -f /etc/systemd/zram-generator.conf ]; then
    log "ZRAM config present (min(ram/2, 16384) zstd)"
    systemctl daemon-reload 2>/dev/null || true
    systemctl start systemd-zram-setup@zram0 2>/dev/null || true
    log_success "ZRAM configuration"
  else
    log_failed "zram-generator.conf missing"
  fi

  checkpoint 1
fi

# ============================================================================
# PHASE 2: NVIDIA + CUDA (Main PC / AI Server only)
# ============================================================================
log ""
log "========================================"
log "  PHASE 2: NVIDIA + CUDA (auto-detect)"
log "========================================"

if [ "$PC_TYPE" != "ai_server" ]; then
  log "Not AI Server (PC_TYPE=$PC_TYPE) — skipping NVIDIA."
  checkpoint 2
else
  if is_checkpoint 2; then
    log "Phase 2 already completed — skipping."
  else
    # FIX Q6a: Detect GPU architecture
    # Turing+ (RTX 20xx+, GTX 16xx+) = compute capability 7.5+
    # nvidia-open works on Turing+; older GPUs need proprietary nvidia
    GPU_IS_TURING_PLUS=false
    GPU_INFO=""

    if lspci | grep -qi nvidia; then
      log "NVIDIA GPU detected — checking architecture..."

      # Try to get GPU name from lspci
      GPU_NAME=$(lspci | grep -i nvidia | grep -i vga | head -1 | sed 's/.*NVIDIA[^:]*: //' | cut -d'(' -f1 | xargs)
      log "  GPU: $GPU_NAME"

      # Check if it's Turing+ by GPU name pattern
      if echo "$GPU_NAME" | grep -qiE "RTX [2-9]|GTX 16[0-9]|Quadro RTX|A[2-9]|A100|H100"; then
        GPU_IS_TURING_PLUS=true
        log "  Architecture: Turing+ → will use nvidia-open"
      else
        log "  Architecture: pre-Turing → will use nvidia (proprietary)"
      fi
    fi

    # Install NVIDIA driver based on detection
    if [ "$GPU_IS_TURING_PLUS" = true ]; then
      # FIX Q4a: Install nvidia-open for linux + nvidia-open-lts for linux-lts
      log "Installing nvidia-open (Turing+) for linux + linux-lts..."
      if sudo pacman -S --noconfirm --needed nvidia-open nvidia-open-lts nvidia-utils lib32-nvidia-utils nvidia-settings >> "$LOG_FILE" 2>&1; then
        log_success "NVIDIA open driver (linux + linux-lts)"
      else
        log_failed "NVIDIA open driver — trying proprietary fallback"
        sudo pacman -S --noconfirm --needed nvidia nvidia-lts nvidia-utils lib32-nvidia-utils >> "$LOG_FILE" 2>&1 && log_success "NVIDIA proprietary (fallback)" || log_failed "NVIDIA proprietary"
      fi
    else
      # Pre-Turing: proprietary nvidia for both kernels
      log "Installing nvidia (proprietary) for linux + linux-lts..."
      if sudo pacman -S --noconfirm --needed nvidia nvidia-lts nvidia-utils lib32-nvidia-utils nvidia-settings >> "$LOG_FILE" 2>&1; then
        log_success "NVIDIA proprietary driver (linux + linux-lts)"
      else
        log_failed "NVIDIA proprietary driver"
      fi
    fi

    # CUDA + cuDNN
    log "Installing CUDA + cuDNN..."
    if sudo pacman -S --noconfirm --needed cuda cudnn >> "$LOG_FILE" 2>&1; then
      log_success "CUDA + cuDNN"
    else
      log_failed "CUDA + cuDNN"
    fi

    # Rebuild initramfs for both kernels
    log "Rebuilding initramfs (linux + linux-lts)..."
    if sudo mkinitcpio -P >> "$LOG_FILE" 2>&1; then
      log_success "initramfs rebuild (all kernels)"
    else
      log_failed "initramfs rebuild"
    fi

    # NVIDIA Container Toolkit (via AUR/yay)
    log "Installing NVIDIA Container Toolkit (via yay)..."
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

    # Install downgrade (AUR) for driver rollback (Q1a: moved from ISO to first-boot)
    log "Installing downgrade (AUR) for NVIDIA driver rollback..."
    if command -v yay >/dev/null 2>&1; then
      if yay -S --noconfirm downgrade >> "$LOG_FILE" 2>&1; then
        log_success "downgrade (AUR rollback tool)"
      else
        log_failed "downgrade AUR install"
      fi
    fi

    # GRUB: add nvidia_drm.modeset=1
    log "Configuring GRUB nvidia_drm.modeset=1..."
    if [ -f /etc/default/grub ]; then
      if ! grep -q "nvidia_drm.modeset=1" /etc/default/grub; then
        sed -i 's/GRUB_CMDLINE_LINUX_DEFAULT="\(.*\)"/GRUB_CMDLINE_LINUX_DEFAULT="\1 nvidia_drm.modeset=1"/' /etc/default/grub
        grub-mkconfig -o /boot/grub/grub.cfg >> "$LOG_FILE" 2>&1 && log_success "GRUB nvidia_drm.modeset=1" || log_failed "GRUB update"
      else
        log "  nvidia_drm.modeset=1 already set"
        log_success "GRUB config (already configured)"
      fi
    fi

    checkpoint 2
  fi
fi

# ============================================================================
# PHASE 3: Docker + Services + Model Manager
# ============================================================================
log ""
log "========================================"
log "  PHASE 3: Docker + Services"
log "========================================"

if is_checkpoint 3; then
  log "Phase 3 already completed — skipping."
else
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

  # Start services with logging
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

  if [ "$PC_TYPE" == "ai_server" ]; then
    log "--- Starting AI Server services ---"

    # Inference tiers (v17.1: 5-tier architecture)
    start_service "vLLM (Tier-2)"     "/opt/scarlix-src/ai/vllm/docker-compose.yml"
    start_service "SGLang (Tier-1)"   "/opt/scarlix-src/ai/sglang/docker-compose.yml"
    start_service "Ollama Main"       "/opt/scarlix-src/ai/ollama/docker-compose-main.yml"
    start_service "Ollama Agent"      "/opt/scarlix-src/ai/ollama/docker-compose-agent.yml"
    start_service "Laya (System-1)"   "/opt/scarlix-src/ai/laya/docker-compose.yml"
    start_service "BeeLlama (Tier-4)" "/opt/scarlix-src/ai/llamacpp/docker-compose.yml"
    start_service "Browser MCP"       "/opt/scarlix-src/ai/browser-mcp/docker-compose.yml"
    start_service "smg Gateway"       "/opt/scarlix-src/ai/smg/docker-compose.yml"

    # Infra
    start_service "Network/Sec"        "/opt/scarlix-src/network/docker-compose.yml"
    start_service "Voice"             "/opt/scarlix-src/voice/docker-compose.yml"
    start_service "Buzz"              "/opt/scarlix-src/workspace/buzz/docker-compose.yml"

    # Agents
    start_service "Hermes"            "/opt/scarlix-src/agents/hermes/docker-compose.yml"
    start_service "ScarliHQ"          "/opt/scarlix-src/scarlihq/docker-compose.yml"
    start_service "Monitoring"        "/opt/scarlix-src/monitoring/docker-compose.yml"

    # Gaming
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
    log "--- Starting Dev Workstation services ---"

    start_service "Coding Pipeline"  "/opt/scarlix-src/coding-pipeline/docker-compose.yml"
    start_service "Media Tools"       "/opt/scarlix-src/media-tools/docker-compose.yml"
  fi

  # Install oh-my-pi (replaces OpenCode)
  log "Installing oh-my-pi (omp) coding agent..."
  if curl -fsSL https://omp.sh/install 2>/dev/null | sh >> "$LOG_FILE" 2>&1; then
    log_success "oh-my-pi (omp) installed"
  else
    log_failed "oh-my-pi install (network may be needed)"
  fi

  # Enable model-manager weekly timer
  if [ -f /etc/systemd/system/model-manager.timer ]; then
    systemctl enable model-manager.timer >> "$LOG_FILE" 2>&1 && log_success "model-manager.timer (weekly Mon 04:00)" || log_failed "model-manager.timer enable"
  fi

  checkpoint 3
fi

# ============================================================================
# SUMMARY
# ============================================================================
log ""
log "========================================"
log "  SCARLIX OS v17.1 — First Boot Summary"
log "========================================"
log "  Base:        EndeavourOS (Arch)"
log "  Kernel:      $(uname -r)"
log "  LTS kernel:  $(pacman -Q linux-lts 2>/dev/null | head -1 || echo 'not installed')"
log "  PC Type:     $PC_TYPE"
log "  User:        $REAL_USER"
log "  SUCCESS:     $SUCCESS_COUNT"
log "  FAILED:      $FAIL_COUNT"
log ""
log "  Services:"
echo -e "$SERVICES_STARTED" | tee -a "$LOG_FILE"
log ""
log "  Inference tiers:"
log "    Tier-1: SGLang (agents, RadixAttention)"
log "    Tier-2: vLLM (high throughput, Multi-LoRA) [NEW v17.1]"
log "    Tier-3: Ollama (GGUF, concurrent)"
log "    Tier-4: BeeLlama.cpp (offline, KVarN) [NEW v17.1]"
log "    System-1: Laya (33ms router) [NEW v17.1]"
log ""
log "  Dashboard:   http://$(hostname -I | awk '{print $1}'):8090"
log "  Full log:    $LOG_FILE"
log "  Rollback:    sudo snapper -c root list"
log "  VRAM check:  scarlix-mode vram"
log "  Coding agent: omp (oh-my-pi)"
log "========================================"

# Mark as installed
touch /opt/scarlix/.installed

exit 0
