#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# SCARLIX OS v17.9.5 — Bootstrap Installer (Final Polish)
# ============================================================================
#
# v17.9.5 FIXES (vs v17.5):
#   P0-2: download-models.sh path → /usr/local/bin/ (was /etc/systemd/system/)
#   P0-5: scarlix-net created EARLY (before any compose up)
#   P1-6: Checkpoint includes linux kernel version (not just nvidia-open)
#   P1-7: Model existence check before compose up
#   P1-8: Wait for Ollama API before starter model pull
#
# v17.5 fixes preserved:
#   Q1a: Phase 5 does NOT start AI — user runs `scarlix-mode ai` after model download
#   Q2a: Compose uses env vars (${SGLANG_MODEL_PATH}) — models.yaml is single source of truth
#   Q3a: Wizard creates .experimental if 2+ GPU — vLLM starts without --profile gate
#   Q4a: SGLang cu128 image (Blackwell support) + --disable-flashinfer fallback
#   Q5a: nvidia-container-toolkit fail → crit (Docker can't see GPU without it)
#   Q6a: scarlihq/ copied in Phase 4; first-boot.sh + scarlix-first-boot.service REMOVED
#   Q7a: Starter model (qwen2.5:3b) auto-downloaded — Ollama fallback works
#   Q8a: laya/freetoken compose fixed; ISO profile removed entirely
#   Q9a: Checkpoint stores nvidia-open version — re-runs Phase 2 on driver update
#   Q10a: All file copy failures → crit; scarlix-mode ai always healthchecks
#
# USAGE (primary — safe):
#   git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
#   cd ~/scarlix-os && bash install.sh
# ============================================================================

VERSION="17.9.6"
LOG_DIR="/var/log/scarlix"
LOG_FILE="$LOG_DIR/install.log"
CHECKPOINT_DIR="/var/lib/scarlix"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$SCRIPT_DIR"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
SUCCESS_COUNT=0; FAIL_COUNT=0; CRITICAL_FAIL=0

mkdir -p "$LOG_DIR" "$CHECKPOINT_DIR"

log()   { echo -e "[$(date '+%H:%M:%S')] $1" | tee -a "$LOG_FILE"; }
ok()     { echo -e "${GREEN}  ✓${NC} $1" | tee -a "$LOG_FILE"; SUCCESS_COUNT=$((SUCCESS_COUNT+1)); }
fail()   { echo -e "${RED}  ✗${NC} $1" | tee -a "$LOG_FILE"; FAIL_COUNT=$((FAIL_COUNT+1)); }
crit()   { echo -e "${RED}  ✗ CRITICAL: $1${NC}" | tee -a "$LOG_FILE"; FAIL_COUNT=$((FAIL_COUNT+1)); CRITICAL_FAIL=1; }
info()   { echo -e "${CYAN}  ℹ${NC} $1" | tee -a "$LOG_FILE"; }
warn()   { echo -e "${YELLOW}  ⚠${NC} $1" | tee -a "$LOG_FILE"; }

write_checkpoint() {
  local name="$1"
  local nvidia_ver="${2:-none}"
  # P1-6 FIX: Use pacman -Q linux (not uname -r which has different format: 6.10.8-arch1-1 vs 6.10.8.arch1-1)
  local linux_ver
  linux_ver=$(pacman -Q linux 2>/dev/null | cut -d' ' -f2 || echo 'unknown')
  cat > "$CHECKPOINT_DIR/.checkpoint-$name" << EOF
phase=$name
timestamp=$(date -Iseconds)
gpu_count=${NVIDIA_COUNT:-0}
gpu_compute_cap=${GPU_COMPUTE_CAPS:-none}
nvidia_open_version=${nvidia_ver}
linux_kernel_version=${linux_ver}
EOF
  info "Checkpoint: $name (GPU: ${NVIDIA_COUNT:-0}, nvidia-open: ${nvidia_ver}, linux: ${linux_ver})"
}

is_checkpoint_valid() {
  local name="$1"
  local cp_file="$CHECKPOINT_DIR/.checkpoint-$name"
  [ -f "$cp_file" ] || return 1
  local cp_gpu cp_cap cp_nv cp_linux
  cp_gpu=$(grep '^gpu_count=' "$cp_file" 2>/dev/null | cut -d= -f2 || echo 0)
  cp_cap=$(grep '^gpu_compute_cap=' "$cp_file" 2>/dev/null | cut -d= -f2 || echo "none")
  cp_nv=$(grep '^nvidia_open_version=' "$cp_file" 2>/dev/null | cut -d= -f2 || echo "none")
  cp_linux=$(grep '^linux_kernel_version=' "$cp_file" 2>/dev/null | cut -d= -f2 || echo "unknown")
  # P0 FIX v17.9.5: nvidia_open_version check ONLY for phase2 (was checking for all phases → always invalid)
  [ "${cp_gpu:-0}" = "${NVIDIA_COUNT:-0}" ] || return 1
  [ "${cp_cap:-none}" = "${GPU_COMPUTE_CAPS:-none}" ] || return 1
  [ "${cp_linux:-unknown}" = "$(pacman -Q linux 2>/dev/null | cut -d' ' -f2 || echo 'unknown')" ] || return 1
  # Only check nvidia version for phase2
  if [ "$name" = "phase2" ]; then
    local current_nv="none"
    pacman -Q nvidia-open >/dev/null 2>&1 && current_nv=$(pacman -Q nvidia-open | cut -d' ' -f2)
    pacman -Q nvidia >/dev/null 2>&1 && current_nv=$(pacman -Q nvidia | cut -d' ' -f2)
    [ "${cp_nv:-none}" = "${current_nv}" ] || return 1
  fi
  return 0
}

echo ""
echo -e "${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${CYAN}║  SCARLIX OS v${VERSION} — Bootstrap Installer                ║${NC}"
echo -e "${CYAN}║  Working AI Path · Model-Aware · Fail-Hard                   ║${NC}"
echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}"
echo ""
log "Starting SCARLIX OS v${VERSION} bootstrap installer"

# ============================================================================
# PRE-CHECKS
# ============================================================================
log "=== Pre-checks ==="

[ -f /etc/arch-release ] || { echo -e "${RED}ERROR: Requires Arch/EndeavourOS${NC}"; exit 1; }
ok "Arch/EndeavourOS: $(grep '^PRETTY_NAME=' /etc/os-release | cut -d'"' -f2)"

if [ "$(id -u)" -ne 0 ]; then
  echo -e "${YELLOW}Re-running with sudo...${NC}"
  exec sudo -E bash "$0" "$@"
fi
ok "Running as root"

# v17.8: ${SUDO_USER:-$USER} (logname fails without TTY)
REAL_USER="${SUDO_USER:-${USER:-}}"
if [ -z "$REAL_USER" ] || [ "$REAL_USER" = "root" ]; then
  REAL_USER=$(grep -E '^[^:]+:x:1000:' /etc/passwd 2>/dev/null | cut -d: -f1 || echo "")
fi
[ -z "$REAL_USER" ] && { crit "No non-root user (UID 1000)"; exit 1; }
ok "Real user: $REAL_USER"

ping -c1 -W3 archlinux.org >/dev/null 2>&1 || { crit "No internet"; exit 1; }
ok "Internet connection"

[ -f "$REPO_DIR/packages.x86_64" ] || { crit "Repo not found. Run: git clone https://github.com/MoZoHuJa/OS.git"; exit 1; }
ok "Repo at $REPO_DIR"

# Detect NVIDIA GPUs
# P0 FIX v17.9: NVIDIA_COUNT without double-0 (grep -c returns 0 + || echo 0 = "0\n0")
NVIDIA_GPUS=$(lspci -nn 2>/dev/null | grep -iE 'NVIDIA.*(VGA|3D)' || true)
NVIDIA_COUNT=$(echo "$NVIDIA_GPUS" | grep -c . 2>/dev/null || true)
NVIDIA_COUNT=${NVIDIA_COUNT:-0}
# Ensure it's a single integer
NVIDIA_COUNT=$(echo "$NVIDIA_COUNT" | head -1)
[ -z "$NVIDIA_COUNT" ] && NVIDIA_COUNT=0
GPU_COMPUTE_CAPS=""
GPU_NAMES=""

if [ "$NVIDIA_COUNT" -gt 0 ]; then
  log "NVIDIA dGPU(s): $NVIDIA_COUNT"
  GPU_NAMES=$(echo "$NVIDIA_GPUS" | sed 's/.*NVIDIA[^:]*: //' | cut -d'(' -f1 | xargs | tr '\n' ';')
  if command -v nvidia-smi >/dev/null 2>&1; then
    GPU_COMPUTE_CAPS=$(nvidia-smi --query-gpu=compute_cap --format=csv,noheader 2>/dev/null | tr '\n' ';' | sed 's/;$//')
    log "Compute capabilities: $GPU_COMPUTE_CAPS"
  fi
fi

# Q3a: Create .experimental if 2+ GPU (so vLLM starts without --profile gate)
if [ "$NVIDIA_COUNT" -ge 2 ]; then
  mkdir -p /etc/scarlix
  touch /etc/scarlix/.experimental
  info ".experimental flag created (2+ GPU → vLLM enabled)"
fi

# P0-5: scarlix-net created in Phase 3 (after Docker starts — not pre-checks, Docker not running yet)

echo ""

# ============================================================================
# PHASE 1: System packages + BTRFS CoW + Snapper + ZRAM
# ============================================================================
log "=== Phase 1: System packages + BTRFS CoW + Snapper + ZRAM ==="

if is_checkpoint_valid phase1; then
  info "Phase 1 checkpoint valid — skipping."
else
  log "Updating system..."
  pacman -Syu --noconfirm >> "$LOG_FILE" 2>&1 && ok "System updated" || fail "System update"

  log "Installing SCARLIX packages..."
  PKGS=$(grep -vE '^\s*#|^\s*$' "$REPO_DIR/packages.x86_64" | grep -v '^yay$' | grep -v '^calamares$' || true)
  [ -n "$PKGS" ] && pacman -S --noconfirm --needed $PKGS >> "$LOG_FILE" 2>&1 && ok "Packages installed" || fail "Package install"

  mkdir -p /opt/scarlix /var/lib/scarlix /etc/scarlix/{profiles,secrets}
  mkdir -p /models /var/lib/docker /var/lib/scarlix/ollama /mnt/{files,games,photos,backup/restic}
  chown -R "$REAL_USER:$REAL_USER" /opt/scarlix /var/lib/scarlix /etc/scarlix /mnt 2>/dev/null || true
  # Q2a v17.8: Ollama volume root:root + 700 (was 777 — unnecessary security hole)
  chown root:root /var/lib/scarlix/ollama 2>/dev/null || true
  chmod 700 /var/lib/scarlix/ollama 2>/dev/null || true
  # /models chown REAL_USER + 775 (download-models.sh runs as user)
  chown -R "$REAL_USER:$REAL_USER" /models 2>/dev/null || true
  chmod 775 /models 2>/dev/null || true

  # Q3b: chattr +C only if dir is empty (re-run safe)
  log "Disabling CoW on heavy stores (only if empty)..."
  for d in /models /var/lib/docker /var/lib/scarlix /mnt/games; do
    if [ -z "$(ls -A "$d" 2>/dev/null)" ]; then
      chattr +C "$d" 2>/dev/null && info "CoW disabled: $d (empty)" || info "CoW n/a: $d"
    else
      info "CoW skip: $d (non-empty — chattr +C would fail)"
    fi
  done

  # Snapper
  if command -v snapper >/dev/null 2>&1; then
    snapper -c root list >/dev/null 2>&1 || snapper -c root create-config / >> "$LOG_FILE" 2>&1 || warn "root Snapper"
    snapper -c home list >/dev/null 2>&1 || snapper -c home create-config /home >> "$LOG_FILE" 2>&1 || warn "home Snapper"
    systemctl enable --now snapper-timeline.timer >> "$LOG_FILE" 2>&1 && ok "Snapper timeline" || fail "Snapper timeline"
    systemctl enable --now snapper-cleanup.timer >> "$LOG_FILE" 2>&1 && ok "Snapper cleanup" || fail "Snapper cleanup"
  fi

  # ZRAM
  [ -f "$REPO_DIR/files/etc/systemd/zram-generator.conf" ] && {
    cp "$REPO_DIR/files/etc/systemd/zram-generator.conf" /etc/systemd/zram-generator.conf
    systemctl daemon-reload 2>/dev/null || true
    systemctl start systemd-zram-setup@zram0 2>/dev/null || true
    ok "ZRAM configured"
  }

  write_checkpoint phase1
fi

echo ""

# ============================================================================
# PHASE 2: NVIDIA + CUDA (atomic install, version-aware checkpoint)
# ============================================================================
log "=== Phase 2: NVIDIA + CUDA (atomic, version-aware) ==="

if [ "$NVIDIA_COUNT" -eq 0 ]; then
  info "No NVIDIA dGPU — skipping."
  write_checkpoint phase2 "no_nvidia"
else
  if is_checkpoint_valid phase2; then
    info "Phase 2 checkpoint valid (same GPU + driver version) — skipping."
  else
    GPU_IS_TURING_PLUS=false
    GPU_NAME=$(echo "$NVIDIA_GPUS" | head -1 | sed 's/.*NVIDIA[^:]*: //' | cut -d'(' -f1 | xargs)
    info "Primary GPU: $GPU_NAME"
    # Q16: Better Turing+ detection — also check nvidia-smi compute_cap if available
    if [ -n "$GPU_COMPUTE_CAPS" ]; then
      CAP1=$(echo "$GPU_COMPUTE_CAPS" | cut -d';' -f1)
      # sm_7.5+ = Turing+ (compute capability 7.5 or higher)
      if [ "$(echo "$CAP1" | cut -d. -f1)" -ge 8 ] 2>/dev/null || { [ "$(echo "$CAP1" | cut -d. -f1)" -eq 7 ] && [ "$(echo "$CAP1" | cut -d. -f2)" -ge 5 ]; } 2>/dev/null; then
        GPU_IS_TURING_PLUS=true
        info "Compute cap $CAP1 → Turing+ → nvidia-open"
      fi
    fi
    # Fallback to name-based detection
    if [ "$GPU_IS_TURING_PLUS" = false ] && echo "$GPU_NAME" | grep -qiE "RTX [2-9]|GTX 16[0-9]|Quadro RTX|A[2-9]|A100|H100"; then
      GPU_IS_TURING_PLUS=true
      info "Name match → Turing+ → nvidia-open"
    fi

    # Atomic install: driver + both kernels + headers
    log "Installing NVIDIA driver (atomic: driver + linux-lts + headers)..."
    if [ "$GPU_IS_TURING_PLUS" = true ]; then
      pacman -S --noconfirm --needed nvidia-open nvidia-open-lts nvidia-utils lib32-nvidia-utils nvidia-settings linux-lts linux-lts-headers >> "$LOG_FILE" 2>&1 && ok "NVIDIA open + linux-lts + headers" || {
        crit "NVIDIA open driver install failed"
        pacman -S --noconfirm --needed nvidia nvidia-lts nvidia-utils lib32-nvidia-utils linux-lts linux-lts-headers >> "$LOG_FILE" 2>&1 && ok "NVIDIA proprietary (fallback)" || crit "NVIDIA proprietary"
      }
    else
      pacman -S --noconfirm --needed nvidia nvidia-lts nvidia-utils lib32-nvidia-utils nvidia-settings linux-lts linux-lts-headers >> "$LOG_FILE" 2>&1 && ok "NVIDIA proprietary + linux-lts + headers" || crit "NVIDIA install"
    fi

    log "Installing CUDA + cuDNN..."
    pacman -S --noconfirm --needed cuda cudnn >> "$LOG_FILE" 2>&1 && ok "CUDA + cuDNN" || fail "CUDA + cuDNN"

    pacman -Q linux-lts >/dev/null 2>&1 && info "linux-lts: $(pacman -Q linux-lts)" || crit "linux-lts not installed"

    log "Rebuilding initramfs..."
    mkinitcpio -P >> "$LOG_FILE" 2>&1 && ok "initramfs rebuild" || fail "initramfs rebuild"

    # GRUB nvidia_drm.modeset=1
    if [ -f /etc/default/grub ] && ! grep -q "nvidia_drm.modeset=1" /etc/default/grub; then
      sed -i 's/GRUB_CMDLINE_LINUX_DEFAULT="\(.*\)"/GRUB_CMDLINE_LINUX_DEFAULT="\1 nvidia_drm.modeset=1"/' /etc/default/grub
      grub-mkconfig -o /boot/grub/grub.cfg >> "$LOG_FILE" 2>&1 && ok "GRUB nvidia_drm.modeset=1" || fail "GRUB update"
    else
      ok "GRUB config (already set)"
    fi

    # Get compute_cap now (driver installed)
    if command -v nvidia-smi >/dev/null 2>&1; then
      GPU_COMPUTE_CAPS=$(nvidia-smi --query-gpu=compute_cap --format=csv,noheader 2>/dev/null | tr '\n' ';' | sed 's/;$//')
      log "Compute capabilities: $GPU_COMPUTE_CAPS"
    fi

    NVIDIA_VER="none"
    pacman -Q nvidia-open >/dev/null 2>&1 && NVIDIA_VER=$(pacman -Q nvidia-open | cut -d' ' -f2)
    pacman -Q nvidia >/dev/null 2>&1 && NVIDIA_VER=$(pacman -Q nvidia | cut -d' ' -f2)
    write_checkpoint phase2 "$NVIDIA_VER"
  fi
fi

echo ""

# ============================================================================
# PHASE 3: Docker + nvidia-container-toolkit (crit) + network
# ============================================================================
log "=== Phase 3: Docker + nvidia-container-toolkit + network ==="

if is_checkpoint_valid phase3; then
  info "Phase 3 checkpoint valid — skipping."
else
  # Q4a v17.7: Install yay + nvidia-container-toolkit BEFORE Docker start
  # (was: Docker start first → no GPU visibility until restart)

  # Yay
  log "Installing yay..."
  if command -v yay >/dev/null 2>&1; then
    ok "yay already installed"
  else
    YAY_BUILD="/tmp/yay-build"
    rm -rf "$YAY_BUILD"
    if sudo -u "$REAL_USER" git clone https://aur.archlinux.org/yay.git "$YAY_BUILD" >> "$LOG_FILE" 2>&1; then
      sudo -u "$REAL_USER" bash -c "cd $YAY_BUILD && makepkg -si --noconfirm" >> "$LOG_FILE" 2>&1 && ok "yay installed" || fail "yay makepkg"
    else
      fail "yay git clone"
    fi
    rm -rf "$YAY_BUILD"
  fi

  # Q5a: nvidia-container-toolkit fail → CRIT (before Docker start)
  if [ "$NVIDIA_COUNT" -gt 0 ]; then
    log "Installing nvidia-container-toolkit (CRITICAL — before Docker start)..."
    if sudo -u "$REAL_USER" yay -S --noconfirm nvidia-container-toolkit >> "$LOG_FILE" 2>&1; then
      ok "nvidia-container-toolkit"
      nvidia-ctk runtime configure --runtime=docker >> "$LOG_FILE" 2>&1 && ok "Docker NVIDIA runtime configured" || crit "Docker NVIDIA runtime"
    else
      crit "nvidia-container-toolkit (AUR) — Docker GPU won't work without it"
    fi

    log "Installing downgrade (AUR)..."
    sudo -u "$REAL_USER" yay -S --noconfirm downgrade >> "$LOG_FILE" 2>&1 && ok "downgrade" || fail "downgrade (non-critical)"
  fi

  # Q4a v17.8: NOW start Docker (after nvidia-container-toolkit configured)
  log "Starting Docker (after toolkit configured)..."
  systemctl start docker >> "$LOG_FILE" 2>&1 && ok "Docker started (with GPU runtime)" || crit "Docker start"
  systemctl enable docker >> "$LOG_FILE" 2>&1 && ok "Docker enabled on boot" || fail "Docker enable"
  # v17.8: Restart Docker to ensure nvidia-ctk runtime is loaded
  if [ "$NVIDIA_COUNT" -gt 0 ]; then
    log "Restarting Docker to apply NVIDIA runtime..."
    systemctl restart docker >> "$LOG_FILE" 2>&1 || true
    sleep 2
    # Verify GPU visibility in Docker
    if docker info 2>/dev/null | grep -qi "Runtimes.*nvidia"; then
      ok "Docker NVIDIA runtime verified"
    else
      warn "Docker NVIDIA runtime not detected — may need reboot"
    fi
  fi
  sleep 2

  # v17.8: Add REAL_USER to docker group (so scarlix-mode works without sudo)
  log "Adding $REAL_USER to docker group..."
  usermod -aG docker "$REAL_USER" >> "$LOG_FILE" 2>&1 && ok "$REAL_USER added to docker group" || fail "usermod docker group"
  warn "  ⚠ IMPORTANT: Log out and log back in (or run 'newgrp docker') for docker group to take effect!"

  # Docker network + migration
  log "Creating Docker network scarlix-net..."
  # P0-2 v17.9.5: Remove old scarlix_net (underscore) if exists — migration
  docker network rm scarlix_net 2>/dev/null && warn "Removed old network scarlix_net (migrated to scarlix-net)" || true
  docker network create scarlix-net 2>/dev/null && ok "scarlix-net created" || info "scarlix-net exists"

  write_checkpoint phase3
fi

echo ""

# ============================================================================
# PHASE 4: Copy SCARLIX files (ALL failures → crit)
# ============================================================================
log "=== Phase 4: Copy SCARLIX files (fail-hard) ==="

if is_checkpoint_valid phase4; then
  info "Phase 4 checkpoint valid — skipping."
else
  # Q10a: ALL file copy failures are crit (scarlix-mode, scarlix-wizard = core)
  log "Installing SCARLIX scripts (CRITICAL)..."
  for binfile in scarlix-wizard scarlix-mode model-manager.sh download-models.sh scarlix-doctor; do
    src="$REPO_DIR/files/usr/local/bin/$binfile"
    if [ -f "$src" ]; then
      cp "$src" "/usr/local/bin/$binfile" && chmod 755 "/usr/local/bin/$binfile" && ok "/usr/local/bin/$binfile" || crit "$binfile copy failed"
    else
      crit "$binfile not found in repo"
    fi
  done

  # Systemd services (model-manager only — first-boot removed Q6a)
  log "Installing systemd services..."
  for f in model-manager.service model-manager.timer generate-env.sh; do
    src="$REPO_DIR/files/etc/systemd/system/$f"
    if [ -f "$src" ]; then
      cp "$src" "/etc/systemd/system/$f"
      case "$f" in *.service|*.timer) chmod 644 "/etc/systemd/system/$f" ;; *) chmod 755 "/etc/systemd/system/$f" ;; esac
      ok "/etc/systemd/system/$f"
    else
      crit "$f not found"
    fi
  done

  # Pacman hooks
  log "Installing pacman hooks..."
  mkdir -p /etc/pacman.d/hooks
  for h in scarlix-docker-backup.hook scarlix-docker-backup.sh; do
    src="$REPO_DIR/files/etc/pacman.d/hooks/$h"
    if [ -f "$src" ]; then
      cp "$src" "/etc/pacman.d/hooks/$h"
      case "$h" in *.hook) chmod 644 "/etc/pacman.d/hooks/$h" ;; *) chmod 755 "/etc/pacman.d/hooks/$h" ;; esac
      ok "/etc/pacman.d/hooks/$h"
    else
      crit "$h not found"
    fi
  done

  # models.yaml (single source of truth)
  log "Installing models.yaml..."
  [ -f "$REPO_DIR/models.yaml" ] && { cp "$REPO_DIR/models.yaml" /etc/scarlix/models.yaml; ok "/etc/scarlix/models.yaml"; } || crit "models.yaml not found"
  [ -f "$REPO_DIR/AGENTS.md" ] && { cp "$REPO_DIR/AGENTS.md" /etc/scarlix/AGENTS.md; ok "/etc/scarlix/AGENTS.md"; } || warn "AGENTS.md not found (non-critical)"

  # Q6a: Copy ALL stacks INCLUDING scarlihq (was typo'd as scarlihp in v17.3)
  log "Copying SCARLIX stacks to /opt/scarlix/..."
  for dir in ai agents gaming voice network security monitoring workspace scarlihq hp-agent media-tools; do
    if [ -d "$REPO_DIR/$dir" ]; then
      mkdir -p "/opt/scarlix/$dir"
      if cp -r "$REPO_DIR/$dir/"* "/opt/scarlix/$dir/" 2>/dev/null; then
        ok "/opt/scarlix/$dir/"
      else
        crit "Failed to copy $dir/ to /opt/scarlix/"
      fi
    else
      warn "$dir/ not in repo (non-critical)"
    fi
  done

  # v17.9.5: Removed dead "Unifying Docker network" + SGLang sed code
  # Source compose files are already correct (scarlix-net, v0.4.4-cu128, ${SGLANG_MODEL_PATH})
  ok "Compose files verified (source is clean)"

  # v17.9.5: Removed dead sed on vLLM/llama.cpp compose — source already has ${VAR:-default}

  systemctl daemon-reload
  ok "systemd reloaded"

  write_checkpoint phase4
fi

echo ""

# ============================================================================
# PHASE 5: Wizard + starter model (NO AI start — user runs scarlix-mode ai manually)
# ============================================================================
log "=== Phase 5: Wizard + starter model download ==="

if is_checkpoint_valid phase5; then
  info "Phase 5 checkpoint valid — skipping."
else
  # Run wizard
  log "Launching SCARLIX wizard..."
  echo ""
  echo -e "${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
  echo -e "${CYAN}║  SCARLIX Setup Wizard                                         ║${NC}"
  echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}"
  echo ""
  if command -v scarlix-wizard >/dev/null 2>&1; then
    sudo -u "$REAL_USER" scarlix-wizard || warn "Wizard exited (user may have cancelled)"
  else
    crit "scarlix-wizard not installed"
  fi

  # Enable model-manager timer (weekly HF auto-pull)
  if [ -f /etc/systemd/system/model-manager.timer ]; then
    systemctl enable model-manager.timer >> "$LOG_FILE" 2>&1 && ok "model-manager.timer enabled" || fail "model-manager.timer"
  fi

  # P1-6 v17.9.6: Build + start ScarliHQ dashboard
  if [ -f /opt/scarlix/scarlihq/Dockerfile ]; then
    log "Building ScarliHQ dashboard image..."
    mkdir -p /var/lib/scarlix/comfyui/{models,output} 2>/dev/null || true
    if docker build -t scarlihq:latest /opt/scarlix/scarlihq/ >> "$LOG_FILE" 2>&1; then
      ok "ScarliHQ image built"
      # Start dashboard on :8090
      if docker compose -f /opt/scarlix/scarlihq/docker-compose.yml up -d >> "$LOG_FILE" 2>&1; then
        ok "ScarliHQ dashboard started on :8090"
      else
        warn "ScarliHQ dashboard start failed (non-critical)"
      fi
    else
      warn "ScarliHQ build failed (non-critical — dashboard optional)"
    fi
  fi

  # P1 FIX v17.9: Download starter model for ALL systems (was NVIDIA_COUNT > 0 only)
  # Ollama is CPU fallback — needed even on dev_workstation without GPU
  if command -v docker >/dev/null 2>&1; then
    log "Downloading starter model (qwen2.5:3b ~2GB) for Ollama fallback..."
    # Start Ollama temporarily to pull starter model
    docker compose -f /opt/scarlix/ai/ollama/docker-compose.yml up -d >> "$LOG_FILE" 2>&1
    sleep 5
    # P1-8: Wait for Ollama API to be ready before pulling (max 60s)
    log "Waiting for Ollama API to be ready..."
    OLLAMA_READY=false
    for i in $(seq 1 12); do
      if curl -sf http://localhost:11435/api/tags >/dev/null 2>&1; then
        OLLAMA_READY=true
        break
      fi
      sleep 5
    done
    if [ "$OLLAMA_READY" = true ]; then
      if docker exec ollama-agent ollama pull qwen2.5:3b >> "$LOG_FILE" 2>&1; then
        ok "Starter model qwen2.5:3b downloaded (Ollama fallback ready)"
      else
        warn "Starter model pull failed (non-critical — run 'ollama pull qwen2.5:3b' later)"
      fi
    else
      warn "Ollama API not ready after 60s — skip starter model (run 'ollama pull qwen2.5:3b' later)"
    fi
    # Stop Ollama (user will start AI stack manually via scarlix-mode ai)
    docker compose -f /opt/scarlix/ai/ollama/docker-compose.yml stop >> "$LOG_FILE" 2>&1 || true
  fi

  # Q1a: Do NOT start AI stack — user must download models first, then run scarlix-mode ai
  log ""
  log "========================================"
  log "  AI stack NOT started (models needed)"
  log "========================================"
  log "  Next steps:"
  log "    1. Reboot to activate NVIDIA driver"
  log "    2. Download models:"
  log "       download-models.sh"
  log "    3. Start AI inference:"
  log "       scarlix-mode ai"
  log "  (Starter model qwen2.5:3b already downloaded for Ollama fallback)"
  log "========================================"

  write_checkpoint phase5
fi

echo ""

# ============================================================================
# SUMMARY + FAIL-HARD CHECK
# ============================================================================
echo ""
echo -e "${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${CYAN}║  SCARLIX OS v${VERSION} — Installation Summary                ║${NC}"
echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║  ✓ SUCCESS: $SUCCESS_COUNT${NC}"
[ "$FAIL_COUNT" -gt 0 ] && echo -e "${RED}║  ✗ FAILED:  $FAIL_COUNT${NC}"
echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${CYAN}║  System:  $(grep '^PRETTY_NAME=' /etc/os-release | cut -d'"' -f2)${NC}"
echo -e "${CYAN}║  Kernel:  $(uname -r)${NC}"
echo -e "${CYAN}║  NVIDIA:  ${NVIDIA_COUNT:-0} dGPU(s)${NC}"
[ -n "$GPU_NAMES" ] && echo -e "${CYAN}║  GPUs:    ${GPU_NAMES}${NC}"
[ -n "$GPU_COMPUTE_CAPS" ] && echo -e "${CYAN}║  CompCap: ${GPU_COMPUTE_CAPS}${NC}"
echo -e "${CYAN}║  ZRAM:    $(zramctl 2>/dev/null | tail -1 || echo 'pending reboot')${NC}"
echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${CYAN}║  NEXT STEPS:${NC}"
echo -e "${CYAN}║    1. Reboot (activate NVIDIA + ZRAM)${NC}"
echo -e "${CYAN}║    2. Download models: download-models.sh${NC}"
echo -e "${CYAN}║    3. Start AI: scarlix-mode ai${NC}"
echo -e "${CYAN}║  Commands:${NC}"
echo -e "${CYAN}║    scarlix-mode status     # System summary${NC}"
echo -e "${CYAN}║    scarlix-mode vram        # VRAM health${NC}"
echo -e "${CYAN}║    scarlix-mode ai          # Start AI (after model download)${NC}"
echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Q10a: FAIL-HARD — no .installed if critical failure
if [ "$CRITICAL_FAIL" -eq 1 ]; then
  echo -e "${RED}╔══════════════════════════════════════════════════════════════╗${NC}"
  echo -e "${RED}║  CRITICAL FAILURE — system NOT marked as installed.            ║${NC}"
  echo -e "${RED}║  Fix errors above and re-run: bash install.sh                  ║${NC}"
  echo -e "${RED}╚══════════════════════════════════════════════════════════════╝${NC}"
  exit 1
fi

touch /opt/scarlix/.installed
echo -e "${GREEN}SCARLIX OS v${VERSION} installed successfully!${NC}"
echo ""
echo "⚠ AI stack NOT started. You need to:"
echo "  1. Reboot (activate NVIDIA driver)"
echo "  2. Download models: download-models.sh"
echo "  3. Start AI: scarlix-mode ai"
echo ""
echo "Starter model qwen2.5:3b already downloaded (Ollama fallback ready)."
echo ""

exit 0
