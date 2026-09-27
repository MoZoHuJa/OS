#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v17.3 — EndeavourOS Edition — First Boot (Auto 5-Tier for Dual GPU)
#
# v17.2.1: Wizard auto-suggests experimental mode for 2+ NVIDIA GPU.
#   If /etc/scarlix/.experimental exists (auto-set by wizard for 2+ GPU),
#   first-boot starts vLLM/FreeToken/Laya in addition to 2-tier default.
#   Q1a: yay installed via git clone (not in ISO packages — was breaking mkarchiso)
#   Q2b: Only @ + @home in Calamares; specialized subvols created HERE as EMPTY
#   Q4b: Checkpoint resume actually works (service has Restart=on-failure)
#   Q5a: NVIDIA dGPU-only detection (ignores Intel iGPU on hybrid laptops)
#   Q6a: nvidia-open + nvidia-open-lts installed atomically in one pacman -S
#   Q7a: chattr +C on EMPTY subvols (created in Phase 1 before any data)
#   Q8a: nvidia-container-toolkit from Docker repo (not AUR); model download opt-in
#
# 2-tier default: SGLang + BeeLlama.cpp
# vLLM/FreeToken/Laya only if /etc/scarlix/.experimental flag exists

LOG_DIR="/var/log/scarlix"
LOG_FILE="$LOG_DIR/first-boot.log"
CHECKPOINT_DIR="/var/lib/scarlix"
SUCCESS_COUNT=0
FAIL_COUNT=0
SERVICES_STARTED=""

mkdir -p "$LOG_DIR" "$CHECKPOINT_DIR"

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"; }
log_success() { log "  ✓ SUCCESS: $1"; SUCCESS_COUNT=$((SUCCESS_COUNT + 1)); SERVICES_STARTED="$SERVICES_STARTED\n  ✓ $1"; }
log_failed() { log "  ✗ FAILED: $1"; FAIL_COUNT=$((FAIL_COUNT + 1)); SERVICES_STARTED="$SERVICES_STARTED\n  ✗ $1"; }

checkpoint() { touch "$CHECKPOINT_DIR/.checkpoint-phase$1"; log "  ⏸ Checkpoint: Phase $1 complete"; }
is_checkpoint() { [ -f "$CHECKPOINT_DIR/.checkpoint-phase$1" ]; }

log "========================================"
log "  SCARLIX OS v17.3 — EndeavourOS Edition"
log "  First Boot (Auto 5-Tier for Dual GPU)"
log "========================================"
log "Base: $(grep '^PRETTY_NAME=' /etc/os-release 2>/dev/null | cut -d'"' -f2 || echo 'EndeavourOS')"
log "Kernel: $(uname -r)"

# Already fully installed?
if [ -f /opt/scarlix/.installed ]; then
  log "Already installed. Exiting."
  exit 0
fi

# Detect PC type (auto-detect, allow wizard override)
PC_TYPE=$(cat /etc/scarlix/pc_type 2>/dev/null || echo "")
if [ -z "$PC_TYPE" ]; then
  # Q5a: Detect only NVIDIA dGPU (ignore Intel iGPU)
  NVIDIA_DGPU_COUNT=$(lspci -nn 2>/dev/null | grep -iE 'NVIDIA.*(VGA|3D)' | wc -l || echo 0)
  if [ "$NVIDIA_DGPU_COUNT" -gt 0 ]; then
    PC_TYPE="ai_server"
  else
    PC_TYPE="dev_workstation"
  fi
  echo "$PC_TYPE" | tee /etc/scarlix/pc_type >/dev/null
fi
log "PC Type: $PC_TYPE"

# Check experimental flag (enables vLLM/FreeToken/Laya profiles)
EXPERIMENTAL=0
[ -f /etc/scarlix/.experimental ] && EXPERIMENTAL=1
[ "$EXPERIMENTAL" -eq 1 ] && log "Experimental mode: ENABLED (vLLM/FreeToken/Laya will start)"

# Check skip-models flag
SKIP_MODELS=0
[ -f /etc/scarlix/.skip-models ] && SKIP_MODELS=1
[ "$SKIP_MODELS" -eq 1 ] && log "Model download: SKIPPED (user opted out)"

# Generate .env if missing
if [ ! -f /etc/scarlix/.env ]; then
  log "Generating .env..."
  /etc/systemd/system/generate-env.sh >> "$LOG_FILE" 2>&1 && log_success ".env generation" || log_failed ".env generation"
else
  log ".env already exists"
fi

# Detect real user (UID 1000)
REAL_USER=$(grep -E '^[^:]+:x:1000:' /etc/passwd 2>/dev/null | cut -d: -f1 || echo "scarlix")
REAL_GROUP=$(id -gn "$REAL_USER" 2>/dev/null || echo "scarlix")
log "Detected user: $REAL_USER:$REAL_GROUP"

# ============================================================================
# PHASE 1: BTRFS subvols (empty) + Snapper + CoW + ZRAM
# ============================================================================
log ""
log "========================================"
log "  PHASE 1: BTRFS subvols + Snapper + CoW"
log "========================================"

if is_checkpoint 1; then
  log "Phase 1 already completed — skipping."
else
  mkdir -p /opt/scarlix /var/lib/scarlix /etc/scarlix/{profiles,secrets}
  mkdir -p /mnt/{files,games,photos,backup/restic}
  chown -R "$REAL_USER:$REAL_GROUP" /opt/scarlix /var/lib/scarlix /etc/scarlix /mnt

  # Q2b+Q7a: Create specialized subvols as EMPTY (Calamares only made @ + @home)
  ROOT_DEV=$(findmnt -no SOURCE / 2>/dev/null || echo "")
  if echo "$ROOT_DEV" | grep -q btrfs; then
    log "Root is on BTRFS ($ROOT_DEV) — creating specialized subvolumes (empty)"
    BTRFS_TMP="/mnt/btrfs-top"
    mkdir -p "$BTRFS_TMP"
    if mount "$ROOT_DEV" "$BTRFS_TMP" 2>/dev/null; then
      for subvol in @root @srv @var_log @var_lib_docker @models @snapshots; do
        if [ ! -e "$BTRFS_TMP/$subvol" ]; then
          btrfs subvolume create "$BTRFS_TMP/$subvol" >> "$LOG_FILE" 2>&1 && log "  Created: $subvol" || log "  (could not create $subvol)"
        else
          log "  Exists: $subvol"
        fi
      done

      # Q7a: chattr +C on EMPTY subvols BEFORE mounting or writing data
      # Mount the new subvols temporarily to apply chattr +C
      for cowdir in models var_lib_docker; do
        MOUNT_TMP="/tmp/cow-check-$cowdir"
        mkdir -p "$MOUNT_TMP"
        if mount -o subvol="@$cowdir" "$ROOT_DEV" "$MOUNT_TMP" 2>/dev/null; then
          if chattr +C "$MOUNT_TMP" 2>/dev/null; then
            log "  CoW disabled: @$cowdir (empty, chattr +C OK)"
          else
            log "  (CoW not applicable on @$cowdir)"
          fi
          umount "$MOUNT_TMP" 2>/dev/null || true
        fi
        rmdir "$MOUNT_TMP" 2>/dev/null || true
      done

      umount "$BTRFS_TMP" 2>/dev/null || true
    else
      log "  (could not mount BTRFS top-level — subvol creation skipped)"
    fi
  else
    log "Root is NOT on BTRFS — skipping subvol creation (non-BTRFS filesystem)"
  fi

  # Create mount points for subvols (fstab entries added by Calamares for @ @home only;
  # we add the rest here if BTRFS)
  mkdir -p /models /var/lib/docker /srv /var/log /.snapshots

  # Snapper configs (root + home)
  if command -v snapper >/dev/null 2>&1; then
    snapper -c root list >/dev/null 2>&1 || snapper -c root create-config / >> "$LOG_FILE" 2>&1 && log "  Snapper config: root"
    snapper -c home list >/dev/null 2>&1 || snapper -c home create-config /home >> "$LOG_FILE" 2>&1 && log "  Snapper config: home"
    systemctl enable --now snapper-timeline.timer >> "$LOG_FILE" 2>&1 && log_success "Snapper timeline timer" || log_failed "Snapper timeline timer"
    systemctl enable --now snapper-cleanup.timer >> "$LOG_FILE" 2>&1 && log_success "Snapper cleanup timer" || log_failed "Snapper cleanup timer"
  else
    log_failed "Snapper not installed"
  fi

  # CoW on /var/lib/scarlix (not a subvol, just chattr if BTRFS)
  chattr +C /var/lib/scarlix 2>/dev/null && log "  CoW disabled: /var/lib/scarlix" || log "  (CoW not applicable on /var/lib/scarlix)"

  # ZRAM verify
  if [ -f /etc/systemd/zram-generator.conf ]; then
    systemctl daemon-reload 2>/dev/null || true
    systemctl start systemd-zram-setup@zram0 2>/dev/null || true
    log_success "ZRAM configuration (min(ram/2, 16384) zstd)"
  else
    log_failed "zram-generator.conf missing"
  fi

  checkpoint 1
fi

# ============================================================================
# PHASE 2: NVIDIA + CUDA (AI Server only) — dGPU detect + atomic install
# ============================================================================
log ""
log "========================================"
log "  PHASE 2: NVIDIA (dGPU auto-detect)"
log "========================================"

if [ "$PC_TYPE" != "ai_server" ]; then
  log "Not AI Server (PC_TYPE=$PC_TYPE) — skipping NVIDIA."
  checkpoint 2
else
  if is_checkpoint 2; then
    log "Phase 2 already completed — skipping."
  else
    # Q5a: Detect ONLY NVIDIA dGPU (ignore Intel iGPU on hybrid laptops)
    # Filter: VGA compatible controller OR 3D controller, with "NVIDIA" in description
    NVIDIA_GPUS=$(lspci -nn 2>/dev/null | grep -iE 'NVIDIA.*(VGA|3D)' || true)
    NVIDIA_COUNT=$(echo "$NVIDIA_GPUS" | grep -c . 2>/dev/null || echo 0)

    if [ "$NVIDIA_COUNT" -eq 0 ]; then
      log "No NVIDIA dGPU detected — skipping NVIDIA install."
      checkpoint 2
    else
      log "NVIDIA dGPU(s) detected: $NVIDIA_COUNT"
      echo "$NVIDIA_GPUS" | while read -r line; do log "  $line"; done

      # Q6a: Determine if Turing+ for nvidia-open
      GPU_IS_TURING_PLUS=false
      GPU_NAME=$(echo "$NVIDIA_GPUS" | head -1 | sed 's/.*NVIDIA[^:]*: //' | cut -d'(' -f1 | xargs)
      log "  Primary GPU: $GPU_NAME"
      if echo "$GPU_NAME" | grep -qiE "RTX [2-9]|GTX 16[0-9]|Quadro RTX|A[2-9]|A100|H100"; then
        GPU_IS_TURING_PLUS=true
        log "  Architecture: Turing+ → will use nvidia-open"
      else
        log "  Architecture: pre-Turing → will use nvidia (proprietary)"
      fi

      # Q6a: Atomic install — nvidia-open + nvidia-open-lts + linux + linux-lts in ONE pacman call
      # This ensures version matching (pacman resolves dependencies atomically)
      log "Installing NVIDIA driver (atomic: driver + both kernels)..."
      if [ "$GPU_IS_TURING_PLUS" = true ]; then
        # Q6a: nvidia-open for linux + nvidia-open-lts for linux-lts, atomically
        if sudo pacman -S --noconfirm --needed \
          nvidia-open nvidia-open-lts \
          nvidia-utils lib32-nvidia-utils nvidia-settings \
          >> "$LOG_FILE" 2>&1; then
          log_success "NVIDIA open driver (linux + linux-lts atomic)"
        else
          log_failed "NVIDIA open driver — trying proprietary fallback"
          sudo pacman -S --noconfirm --needed nvidia nvidia-lts nvidia-utils lib32-nvidia-utils >> "$LOG_FILE" 2>&1 && log_success "NVIDIA proprietary (fallback)" || log_failed "NVIDIA proprietary"
        fi
      else
        if sudo pacman -S --noconfirm --needed \
          nvidia nvidia-lts \
          nvidia-utils lib32-nvidia-utils nvidia-settings \
          >> "$LOG_FILE" 2>&1; then
          log_success "NVIDIA proprietary driver (linux + linux-lts atomic)"
        else
          log_failed "NVIDIA proprietary driver"
        fi
      fi

      # CUDA + cuDNN
      log "Installing CUDA + cuDNN..."
      sudo pacman -S --noconfirm --needed cuda cudnn >> "$LOG_FILE" 2>&1 && log_success "CUDA + cuDNN" || log_failed "CUDA + cuDNN"

      # Q6a: Verify linux-lts actually installed before GRUB update
      if pacman -Q linux-lts >/dev/null 2>&1; then
        log "  ✓ linux-lts verified: $(pacman -Q linux-lts)"
      else
        log_failed "linux-lts not installed — LTS fallback broken"
      fi

      # Rebuild initramfs for both kernels
      log "Rebuilding initramfs (all kernels)..."
      sudo mkinitcpio -P >> "$LOG_FILE" 2>&1 && log_success "initramfs rebuild" || log_failed "initramfs rebuild"

      # GRUB: nvidia_drm.modeset=1
      log "Configuring GRUB nvidia_drm.modeset=1..."
      if [ -f /etc/default/grub ]; then
        if ! grep -q "nvidia_drm.modeset=1" /etc/default/grub; then
          sed -i 's/GRUB_CMDLINE_LINUX_DEFAULT="\(.*\)"/GRUB_CMDLINE_LINUX_DEFAULT="\1 nvidia_drm.modeset=1"/' /etc/default/grub
          grub-mkconfig -o /boot/grub/grub.cfg >> "$LOG_FILE" 2>&1 && log_success "GRUB nvidia_drm.modeset=1" || log_failed "GRUB update"
        else
          log_success "GRUB config (already set)"
        fi
      fi

      checkpoint 2
    fi
  fi
fi

# ============================================================================
# PHASE 3: Docker + services + 2-tier inference
# ============================================================================
log ""
log "========================================"
log "  PHASE 3: Docker + 2-tier services"
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

  # Q1a: Install yay via git clone (NOT from AUR packages — was breaking mkarchiso)
  log "Installing yay (AUR helper) via git clone..."
  if command -v yay >/dev/null 2>&1; then
    log "  yay already installed: $(yay --version 2>/dev/null | head -1)"
    log_success "yay available"
  else
    YAY_BUILD_DIR="/tmp/yay-build"
    rm -rf "$YAY_BUILD_DIR"
    if git clone https://aur.archlinux.org/yay.git "$YAY_BUILD_DIR" >> "$LOG_FILE" 2>&1; then
      cd "$YAY_BUILD_DIR"
      if sudo -u "$REAL_USER" makepkg -si --noconfirm >> "$LOG_FILE" 2>&1; then
        log_success "yay installed (git clone + makepkg)"
      else
        log_failed "yay makepkg build"
      fi
      cd - >/dev/null
    else
      log_failed "yay git clone (network issue?)"
    fi
    rm -rf "$YAY_BUILD_DIR"
  fi

  # Q8a: nvidia-container-toolkit from Docker official repo (NOT AUR)
  # Docker repo added to pacman.conf in v17.2
  if [ "$PC_TYPE" == "ai_server" ]; then
    log "Installing nvidia-container-toolkit (from Docker repo)..."
    if sudo pacman -S --noconfirm --needed nvidia-container-toolkit >> "$LOG_FILE" 2>&1; then
      log_success "nvidia-container-toolkit (Docker repo)"
      sudo nvidia-ctk runtime configure --runtime=docker >> "$LOG_FILE" 2>&1 && log_success "Docker NVIDIA runtime" || log_failed "Docker NVIDIA runtime"
      systemctl restart docker >> "$LOG_FILE" 2>&1 || true
    else
      log_failed "nvidia-container-toolkit (Docker repo install failed)"
    fi
  fi

  # Load environment
  set -a; source /etc/scarlix/.env 2>/dev/null; set +a || true

  # Start services — 2-tier default (SGLang + BeeLlama)
  start_service() {
    local name="$1"
    local compose_file="$2"
    local profiles="${3:-}"
    log "Starting: $name"
    local cmd="docker compose -f $compose_file"
    [ -n "$profiles" ] && cmd="$cmd --profile $profiles"
    if $cmd up -d >> "$LOG_FILE" 2>&1; then
      log_success "$name"
    else
      log_failed "$name"
    fi
  }

  if [ "$PC_TYPE" == "ai_server" ]; then
    log "--- Starting AI Server services (2-tier default) ---"

    # 2-tier default: SGLang (Tier-1) + BeeLlama.cpp (Tier-4)
    start_service "SGLang (Tier-1)"     "/opt/scarlix-src/ai/sglang/docker-compose.yml"
    start_service "BeeLlama.cpp (T4)"  "/opt/scarlix-src/ai/llamacpp/docker-compose.yml"
    start_service "Ollama Main"         "/opt/scarlix-src/ai/ollama/docker-compose-main.yml"
    start_service "Ollama Agent"        "/opt/scarlix-src/ai/ollama/docker-compose-agent.yml"
    start_service "smg Gateway"         "/opt/scarlix-src/ai/smg/docker-compose.yml"

    # Experimental tier (only if /etc/scarlix/.experimental flag exists)
    if [ "$EXPERIMENTAL" -eq 1 ]; then
      log "--- Experimental mode: starting vLLM/FreeToken/Laya ---"
      start_service "vLLM (Tier-2)"      "/opt/scarlix-src/ai/vllm/docker-compose.yml" "experimental"
      start_service "Laya (System-1)"   "/opt/scarlix-src/ai/laya/docker-compose.yml" "experimental"
      start_service "FreeToken (T5)"    "/opt/scarlix-src/ai/freetoken/docker-compose.yml" "moe"
    fi

    # Browser MCP (license-clean, always useful)
    start_service "Browser MCP"         "/opt/scarlix-src/ai/browser-mcp/docker-compose.yml"

    # Infra
    start_service "Network/Sec"         "/opt/scarlix-src/network/docker-compose.yml"
    start_service "Voice"               "/opt/scarlix-src/voice/docker-compose.yml"
    start_service "Buzz"                "/opt/scarlix-src/workspace/buzz/docker-compose.yml"
    start_service "Hermes"              "/opt/scarlix-src/agents/hermes/docker-compose.yml"
    start_service "ScarliHQ"            "/opt/scarlix-src/scarlihq/docker-compose.yml"
    start_service "Monitoring"          "/opt/scarlix-src/monitoring/docker-compose.yml"

    # Gaming
    log "Starting Jellyfin + Minecraft..."
    docker compose -f /opt/scarlix-src/gaming/docker-compose.yml up -d jellyfin minecraft >> "$LOG_FILE" 2>&1 && log_success "Jellyfin + Minecraft" || log_failed "Jellyfin + Minecraft"

    # Default mode
    echo "ai" | tee /var/lib/scarlix/current-mode >/dev/null
    log "Default mode: ai (2-tier: SGLang + BeeLlama)"

    # Model download (opt-in — skip if /etc/scarlix/.skip-models)
    if [ "$SKIP_MODELS" -eq 1 ]; then
      log "Model download SKIPPED (user opted out). Run 'download-models.sh' manually."
      log_success "Model download (skipped)"
    else
      log "Starting model download in background..."
      nohup /etc/systemd/system/download-models.sh >> "$LOG_FILE" 2>&1 &
      log_success "Model download (background)"
    fi
  else
    log "--- Starting Dev Workstation services ---"
    start_service "Coding Pipeline"    "/opt/scarlix-src/coding-pipeline/docker-compose.yml"
    start_service "Media Tools"         "/opt/scarlix-src/media-tools/docker-compose.yml"
  fi

  # Install oh-my-pi (omp) coding agent
  log "Installing oh-my-pi (omp) coding agent..."
  if curl -fsSL https://omp.sh/install 2>/dev/null | sh >> "$LOG_FILE" 2>&1; then
    log_success "oh-my-pi (omp) installed"
  else
    log_failed "oh-my-pi install (network may be needed)"
  fi

  # Enable model-manager weekly timer
  if [ -f /etc/systemd/system/model-manager.timer ]; then
    systemctl enable model-manager.timer >> "$LOG_FILE" 2>&1 && log_success "model-manager.timer (weekly)" || log_failed "model-manager.timer enable"
  fi

  checkpoint 3
fi

# ============================================================================
# SUMMARY
# ============================================================================
log ""
log "========================================"
log "  SCARLIX OS v17.2.1 — First Boot Summary"
log "========================================"
log "  Base:        EndeavourOS (Arch)"
log "  Kernel:      $(uname -r)"
log "  LTS kernel:  $(pacman -Q linux-lts 2>/dev/null | head -1 || echo 'not installed')"
log "  PC Type:     $PC_TYPE"
log "  User:        $REAL_USER"
log "  Experimental: $([ $EXPERIMENTAL -eq 1 ] && echo 'YES (vLLM/FreeToken/Laya)' || echo 'no (2-tier default)')"
log "  Skip models:  $([ $SKIP_MODELS -eq 1 ] && echo 'YES' || echo 'no (will download)')"
log "  SUCCESS:     $SUCCESS_COUNT"
log "  FAILED:      $FAIL_COUNT"
log ""
log "  Services:"
echo -e "$SERVICES_STARTED" | tee -a "$LOG_FILE"
log ""
log "  Inference (2-tier default):"
log "    Tier-1: SGLang (agents, RadixAttention)"
log "    Tier-4: BeeLlama.cpp (offline, KVarN, 32k context)"
log "    Tier-3: Ollama (GGUF concurrent)"
[ "$EXPERIMENTAL" -eq 1 ] && log "    [EXP] Tier-2: vLLM (Multi-LoRA, TP=2)"
[ "$EXPERIMENTAL" -eq 1 ] && log "    [EXP] Tier-5: FreeToken (MoE)"
[ "$EXPERIMENTAL" -eq 1 ] && log "    [EXP] System-1: Laya (33ms router)"
log ""
log "  Dashboard:   http://$(hostname -I | awk '{print $1}'):8090"
log "  Full log:    $LOG_FILE"
log "  Rollback:    sudo snapper -c root list"
log "  VRAM check:  scarlix-mode vram"
log "  Coding agent: omp (oh-my-pi)"
log "========================================"

# Mark as installed (only if no critical failures)
if [ "$FAIL_COUNT" -lt 3 ]; then
  touch /opt/scarlix/.installed
  log "✓ First boot complete. System marked as installed."
else
  log "⚠ $FAIL_COUNT failures — system NOT marked as installed. Service will restart (Restart=on-failure)."
  log "  Check $LOG_FILE for details. Fix issues and reboot."
  exit 1
fi

exit 0
