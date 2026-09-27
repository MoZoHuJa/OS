#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# SCARLIX OS v17.3 — Bootstrap Installer (No ISO needed)
# ============================================================================
#
# WHAT THIS DOES:
#   Installs SCARLIX OS on top of a clean EndeavourOS/Arch system.
#   No ISO build required — just run this script.
#
# USAGE:
#   # Option 1: One-liner (curl + pipe)
#   curl -fsSL https://raw.githubusercontent.com/MoZoHuJa/OS/main/install.sh | bash
#
#   # Option 2: Safer (clone + review + run)
#   git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
#   cd ~/scarlix-os
#   nano install.sh   # review what it does
#   bash install.sh
#
# REQUIREMENTS:
#   - Clean EndeavourOS or Arch Linux installation
#   - Root privileges (script will sudo internally if needed)
#   - Internet connection
#   - (Optional) 1+ NVIDIA GPU for AI inference
#
# IDEMPOTENT: Can be re-run safely. Uses checkpoints in /var/lib/scarlix/.
# ============================================================================

VERSION="17.3.0"
LOG_DIR="/var/log/scarlix"
LOG_FILE="$LOG_DIR/install.log"
CHECKPOINT_DIR="/var/lib/scarlix"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$SCRIPT_DIR"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

SUCCESS_COUNT=0
FAIL_COUNT=0

mkdir -p "$LOG_DIR" "$CHECKPOINT_DIR"

log() { echo -e "[$(date '+%H:%M:%S')] $1" | tee -a "$LOG_FILE"; }
ok() { echo -e "${GREEN}  ✓${NC} $1" | tee -a "$LOG_FILE"; SUCCESS_COUNT=$((SUCCESS_COUNT+1)); }
fail() { echo -e "${RED}  ✗${NC} $1" | tee -a "$LOG_FILE"; FAIL_COUNT=$((FAIL_COUNT+1)); }
info() { echo -e "${CYAN}  ℹ${NC} $1" | tee -a "$LOG_FILE"; }
warn() { echo -e "${YELLOW}  ⚠${NC} $1" | tee -a "$LOG_FILE"; }

checkpoint() { touch "$CHECKPOINT_DIR/.checkpoint-$1"; info "Checkpoint: $1 complete"; }
is_checkpoint() { [ -f "$CHECKPOINT_DIR/.checkpoint-$1" ]; }

# ============================================================================
# BANNER
# ============================================================================
echo ""
echo -e "${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${CYAN}║  SCARLIX OS v${VERSION} — Bootstrap Installer               ║${NC}"
echo -e "${CYAN}║  EndeavourOS Edition · No ISO required                      ║${NC}"
echo -e "${CYAN}║  Auto 5-Tier for 2+ NVIDIA GPU                             ║${NC}"
echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}"
echo ""
log "Starting SCARLIX OS v${VERSION} bootstrap installer"
log "Log: $LOG_FILE"
log "Repo: $REPO_DIR"
echo ""

# ============================================================================
# PRE-CHECKS
# ============================================================================
log "=== Pre-checks ==="

# Check if running on Arch/EndeavourOS
if [ ! -f /etc/arch-release ]; then
  echo -e "${RED}ERROR: This script requires Arch Linux or EndeavourOS.${NC}"
  echo "Detected: $(grep '^PRETTY_NAME=' /etc/os-release 2>/dev/null | cut -d'"' -f2 || echo 'unknown')"
  echo ""
  echo "Please install EndeavourOS first: https://endeavouros.com/"
  exit 1
fi
ok "Arch/EndeavourOS detected: $(grep '^PRETTY_NAME=' /etc/os-release | cut -d'"' -f2)"

# Check root
if [ "$(id -u)" -ne 0 ]; then
  echo -e "${YELLOW}This script needs root privileges. Re-running with sudo...${NC}"
  exec sudo bash "$0" "$@"
fi
ok "Running as root"

# Check internet
if ! ping -c1 -W3 archlinux.org >/dev/null 2>&1; then
  fail "No internet connection (cannot reach archlinux.org)"
  exit 1
fi
ok "Internet connection"

# Check repo files exist (install.sh is in repo root)
if [ ! -f "$REPO_DIR/scarlix/profiledef.sh" ]; then
  echo -e "${RED}ERROR: SCARLIX repo files not found.${NC}"
  echo "Expected: $REPO_DIR/scarlix/profiledef.sh"
  echo ""
  echo "If you ran via curl|bash, the repo wasn't cloned."
  echo "Please run:"
  echo "  git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os"
  echo "  cd ~/scarlix-os && bash install.sh"
  exit 1
fi
ok "SCARLIX repo found at $REPO_DIR"

# Already installed?
if [ -f /opt/scarlix/.installed ]; then
  echo -e "${YELLOW}SCARLIX OS already installed. Re-running installer (idempotent).${NC}"
  echo "Existing checkpoints will be skipped. To force full reinstall:"
  echo "  sudo rm -rf /var/lib/scarlix/.checkpoint-* /opt/scarlix/.installed"
  echo ""
  read -rp "Continue? [y/N] " REINSTALL
  [ "$REINSTALL" != "y" ] && [ "$REINSTALL" != "Y" ] && exit 0
fi

echo ""

# ============================================================================
# PHASE 1: System packages + BTRFS + Snapper + ZRAM
# ============================================================================
log "=== Phase 1: System packages + BTRFS + Snapper + ZRAM ==="

if is_checkpoint phase1; then
  info "Phase 1 already complete — skipping."
else
  # Update system first
  log "Updating system packages..."
  if pacman -Syu --noconfirm >> "$LOG_FILE" 2>&1; then
    ok "System updated"
  else
    fail "System update (may be partial)"
  fi

  # Install all packages from packages.x86_64 (except yay — installed via git clone later)
  log "Installing SCARLIX packages..."
  PKG_FILE="$REPO_DIR/scarlix/packages.x86_64"
  if [ -f "$PKG_FILE" ]; then
    # Filter out comments and empty lines, exclude yay (AUR)
    PKGS=$(grep -vE '^\s*#|^\s*$' "$PKG_FILE" | grep -v '^yay$' || true)
    if [ -n "$PKGS" ]; then
      if pacman -S --noconfirm --needed $PKGS >> "$LOG_FILE" 2>&1; then
        ok "Packages installed ($(echo "$PKGS" | wc -l) packages)"
      else
        fail "Package install (some may have failed)"
      fi
    fi
  else
    fail "packages.x86_64 not found at $PKG_FILE"
  fi

  # BTRFS subvols (only @ + @home exist from EndeavourOS installer)
  ROOT_DEV=$(findmnt -no SOURCE / 2>/dev/null || echo "")
  if echo "$ROOT_DEV" | grep -q btrfs; then
    log "BTRFS detected ($ROOT_DEV) — creating specialized subvolumes (empty)..."
    BTRFS_TMP="/mnt/btrfs-top"
    mkdir -p "$BTRFS_TMP"
    if mount "$ROOT_DEV" "$BTRFS_TMP" 2>/dev/null; then
      for sv in @root @srv @var_log @var_lib_docker @models @snapshots; do
        if [ ! -e "$BTRFS_TMP/$sv" ]; then
          btrfs subvolume create "$BTRFS_TMP/$sv" >> "$LOG_FILE" 2>&1 && info "Created: $sv" || warn "Could not create $sv"
        else
          info "Exists: $sv"
        fi
      done

      # chattr +C on empty subvols (before any data written)
      for cowdir in models var_lib_docker; do
        MOUNT_TMP="/tmp/cow-check-$cowdir"
        mkdir -p "$MOUNT_TMP"
        if mount -o subvol="@$cowdir" "$ROOT_DEV" "$MOUNT_TMP" 2>/dev/null; then
          chattr +C "$MOUNT_TMP" 2>/dev/null && info "CoW disabled: @$cowdir" || info "CoW not applicable: @$cowdir"
          umount "$MOUNT_TMP" 2>/dev/null || true
        fi
        rmdir "$MOUNT_TMP" 2>/dev/null || true
      done
      umount "$BTRFS_TMP" 2>/dev/null || true
    else
      warn "Could not mount BTRFS top-level — skipping subvol creation"
    fi
  else
    info "Root is NOT on BTRFS ($ROOT_DEV) — skipping subvolumes"
  fi

  # Create mount points
  mkdir -p /models /var/lib/docker /srv /var/log /.snapshots /opt/scarlix /var/lib/scarlix /etc/scarlix/{profiles,secrets}
  mkdir -p /mnt/{files,games,photos,backup/restic}

  # Snapper configs
  if command -v snapper >/dev/null 2>&1; then
    log "Configuring Snapper..."
    snapper -c root list >/dev/null 2>&1 || snapper -c root create-config / >> "$LOG_FILE" 2>&1 || true
    snapper -c home list >/dev/null 2>&1 || snapper -c home create-config /home >> "$LOG_FILE" 2>&1 || true
    systemctl enable --now snapper-timeline.timer >> "$LOG_FILE" 2>&1 && ok "Snapper timeline timer" || fail "Snapper timeline timer"
    systemctl enable --now snapper-cleanup.timer >> "$LOG_FILE" 2>&1 && ok "Snapper cleanup timer" || fail "Snapper cleanup timer"
  else
    fail "Snapper not installed"
  fi

  # CoW on /var/lib/scarlix
  chattr +C /var/lib/scarlix 2>/dev/null && info "CoW disabled: /var/lib/scarlix" || true

  # ZRAM
  log "Configuring ZRAM..."
  if [ -f "$REPO_DIR/scarlix/airootfs/etc/systemd/zram-generator.conf" ]; then
    cp "$REPO_DIR/scarlix/airootfs/etc/systemd/zram-generator.conf" /etc/systemd/zram-generator.conf
    systemctl daemon-reload 2>/dev/null || true
    systemctl start systemd-zram-setup@zram0 2>/dev/null || true
    ok "ZRAM configured (min(ram/2, 16384) zstd)"
  else
    fail "zram-generator.conf not found in repo"
  fi

  checkpoint phase1
fi

echo ""

# ============================================================================
# PHASE 2: NVIDIA + CUDA (auto-detect dGPU, atomic install)
# ============================================================================
log "=== Phase 2: NVIDIA + CUDA (auto-detect) ==="

if is_checkpoint phase2; then
  info "Phase 2 already complete — skipping."
else
  # Detect only NVIDIA dGPU (ignore Intel iGPU on hybrid laptops)
  NVIDIA_GPUS=$(lspci -nn 2>/dev/null | grep -iE 'NVIDIA.*(VGA|3D)' || true)
  NVIDIA_COUNT=$(echo "$NVIDIA_GPUS" | grep -c . 2>/dev/null || echo 0)

  if [ "$NVIDIA_COUNT" -eq 0 ]; then
    info "No NVIDIA dGPU detected — skipping NVIDIA install."
  else
    log "NVIDIA dGPU(s) detected: $NVIDIA_COUNT"
    echo "$NVIDIA_GPUS" | while read -r line; do info "  $line"; done

    # Determine Turing+ for nvidia-open
    GPU_IS_TURING_PLUS=false
    GPU_NAME=$(echo "$NVIDIA_GPUS" | head -1 | sed 's/.*NVIDIA[^:]*: //' | cut -d'(' -f1 | xargs)
    info "Primary GPU: $GPU_NAME"
    if echo "$GPU_NAME" | grep -qiE "RTX [2-9]|GTX 16[0-9]|Quadro RTX|A[2-9]|A100|H100"; then
      GPU_IS_TURING_PLUS=true
      info "Architecture: Turing+ → will use nvidia-open"
    else
      info "Architecture: pre-Turing → will use nvidia (proprietary)"
    fi

    # Atomic install: nvidia-open + nvidia-open-lts in ONE pacman call
    log "Installing NVIDIA driver (atomic: driver + both kernels)..."
    if [ "$GPU_IS_TURING_PLUS" = true ]; then
      if pacman -S --noconfirm --needed nvidia-open nvidia-open-lts nvidia-utils lib32-nvidia-utils nvidia-settings >> "$LOG_FILE" 2>&1; then
        ok "NVIDIA open driver (linux + linux-lts atomic)"
      else
        fail "NVIDIA open — trying proprietary fallback"
        pacman -S --noconfirm --needed nvidia nvidia-lts nvidia-utils lib32-nvidia-utils >> "$LOG_FILE" 2>&1 && ok "NVIDIA proprietary (fallback)" || fail "NVIDIA proprietary"
      fi
    else
      pacman -S --noconfirm --needed nvidia nvidia-lts nvidia-utils lib32-nvidia-utils nvidia-settings >> "$LOG_FILE" 2>&1 && ok "NVIDIA proprietary (linux + linux-lts)" || fail "NVIDIA proprietary"
    fi

    # CUDA + cuDNN
    log "Installing CUDA + cuDNN..."
    pacman -S --noconfirm --needed cuda cudnn >> "$LOG_FILE" 2>&1 && ok "CUDA + cuDNN" || fail "CUDA + cuDNN"

    # Verify linux-lts
    if pacman -Q linux-lts >/dev/null 2>&1; then
      info "linux-lts verified: $(pacman -Q linux-lts)"
    else
      fail "linux-lts not installed — LTS fallback broken"
    fi

    # Rebuild initramfs
    log "Rebuilding initramfs..."
    mkinitcpio -P >> "$LOG_FILE" 2>&1 && ok "initramfs rebuild" || fail "initramfs rebuild"

    # GRUB nvidia_drm.modeset=1
    log "Configuring GRUB nvidia_drm.modeset=1..."
    if [ -f /etc/default/grub ]; then
      if ! grep -q "nvidia_drm.modeset=1" /etc/default/grub; then
        sed -i 's/GRUB_CMDLINE_LINUX_DEFAULT="\(.*\)"/GRUB_CMDLINE_LINUX_DEFAULT="\1 nvidia_drm.modeset=1"/' /etc/default/grub
        grub-mkconfig -o /boot/grub/grub.cfg >> "$LOG_FILE" 2>&1 && ok "GRUB nvidia_drm.modeset=1" || fail "GRUB update"
      else
        ok "GRUB config (already set)"
      fi
    fi
  fi

  checkpoint phase2
fi

echo ""

# ============================================================================
# PHASE 3: Docker + yay + AUR packages
# ============================================================================
log "=== Phase 3: Docker + yay + AUR packages ==="

if is_checkpoint phase3; then
  info "Phase 3 already complete — skipping."
else
  # Docker repo for nvidia-container-toolkit (add to pacman.conf if not present)
  if ! grep -q '\[docker\]' /etc/pacman.conf 2>/dev/null; then
    log "Adding Docker official repo to pacman.conf..."
    cat >> /etc/pacman.conf << 'EOF'

[docker]
SigLevel = Optional TrustAll
Server = https://download.docker.com/linux/arch/$arch
EOF
    pacman -Sy >> "$LOG_FILE" 2>&1
    ok "Docker repo added to pacman.conf"
  else
    info "Docker repo already in pacman.conf"
  fi

  # Start Docker
  log "Starting Docker..."
  systemctl start docker >> "$LOG_FILE" 2>&1 && ok "Docker started" || fail "Docker start"
  systemctl enable docker >> "$LOG_FILE" 2>&1 && ok "Docker enabled on boot" || fail "Docker enable"
  sleep 2

  # nvidia-container-toolkit (from Docker repo, not AUR)
  if [ "$NVIDIA_COUNT" -gt 0 ]; then
    log "Installing nvidia-container-toolkit (Docker repo)..."
    if pacman -S --noconfirm --needed nvidia-container-toolkit >> "$LOG_FILE" 2>&1; then
      ok "nvidia-container-toolkit"
      nvidia-ctk runtime configure --runtime=docker >> "$LOG_FILE" 2>&1 && ok "Docker NVIDIA runtime" || fail "Docker NVIDIA runtime"
      systemctl restart docker >> "$LOG_FILE" 2>&1 || true
    else
      fail "nvidia-container-toolkit"
    fi
  fi

  # Install yay via git clone (NOT from AUR packages)
  log "Installing yay (AUR helper) via git clone..."
  if command -v yay >/dev/null 2>&1; then
    info "yay already installed: $(yay --version 2>/dev/null | head -1)"
    ok "yay available"
  else
    # Detect real user (not root)
    REAL_USER=$(grep -E '^[^:]+:x:1000:' /etc/passwd 2>/dev/null | cut -d: -f1 || echo "")
    if [ -z "$REAL_USER" ]; then
      warn "No UID 1000 user found — installing yay as root (not recommended)"
      REAL_USER="root"
    fi

    YAY_BUILD_DIR="/tmp/yay-build"
    rm -rf "$YAY_BUILD_DIR"
    if git clone https://aur.archlinux.org/yay.git "$YAY_BUILD_DIR" >> "$LOG_FILE" 2>&1; then
      cd "$YAY_BUILD_DIR"
      if sudo -u "$REAL_USER" makepkg -si --noconfirm >> "$LOG_FILE" 2>&1; then
        ok "yay installed (git clone + makepkg)"
      else
        fail "yay makepkg build"
      fi
      cd - >/dev/null
    else
      fail "yay git clone (network issue?)"
    fi
    rm -rf "$YAY_BUILD_DIR"
  fi

  # Install downgrade (optional, via yay — for NVIDIA driver rollback)
  if [ "$NVIDIA_COUNT" -gt 0 ]; then
    log "Installing downgrade (AUR) for NVIDIA driver rollback..."
    if command -v yay >/dev/null 2>&1; then
      if sudo -u "${REAL_USER:-$(logname 2>/dev/null || echo root)}" yay -S --noconfirm downgrade >> "$LOG_FILE" 2>&1; then
        ok "downgrade (AUR rollback tool)"
      else
        fail "downgrade AUR install"
      fi
    fi
  fi

  checkpoint phase3
fi

echo ""

# ============================================================================
# PHASE 4: Copy SCARLIX files to system
# ============================================================================
log "=== Phase 4: Copy SCARLIX files to system ==="

if is_checkpoint phase4; then
  info "Phase 4 already complete — skipping."
else
  # Copy binaries to /usr/local/bin/
  log "Installing SCARLIX scripts to /usr/local/bin/..."
  for binfile in scarlix-wizard scarlix-mode model-manager.sh; do
    src="$REPO_DIR/scarlix/airootfs/usr/local/bin/$binfile"
    if [ -f "$src" ]; then
      cp "$src" "/usr/local/bin/$binfile"
      chmod 755 "/usr/local/bin/$binfile"
      ok "/usr/local/bin/$binfile"
    else
      fail "$binfile not found in repo"
    fi
  done

  # Copy systemd services
  log "Installing systemd services..."
  for svcfile in scarlix-first-boot.service model-manager.service model-manager.timer; do
    src="$REPO_DIR/scarlix/airootfs/etc/systemd/system/$svcfile"
    if [ -f "$src" ]; then
      cp "$src" "/etc/systemd/system/$svcfile"
      chmod 644 "/etc/systemd/system/$svcfile"
      ok "/etc/systemd/system/$svcfile"
    else
      fail "$svcfile not found"
    fi
  done

  # Copy first-boot.sh, download-models.sh, generate-env.sh
  for shfile in first-boot.sh download-models.sh generate-env.sh; do
    src="$REPO_DIR/scarlix/airootfs/etc/systemd/system/$shfile"
    if [ -f "$src" ]; then
      cp "$src" "/etc/systemd/system/$shfile"
      chmod 755 "/etc/systemd/system/$shfile"
      ok "/etc/systemd/system/$shfile"
    fi
  done

  # Copy pacman hooks (Docker backup)
  log "Installing pacman hooks..."
  mkdir -p /etc/pacman.d/hooks
  for hookfile in scarlix-docker-backup.hook scarlix-docker-backup.sh; do
    src="$REPO_DIR/scarlix/airootfs/etc/pacman.d/hooks/$hookfile"
    if [ -f "$src" ]; then
      cp "$src" "/etc/pacman.d/hooks/$hookfile"
      case "$hookfile" in
        *.hook) chmod 644 "/etc/pacman.d/hooks/$hookfile" ;;
        *.sh) chmod 755 "/etc/pacman.d/hooks/$hookfile" ;;
      esac
      ok "/etc/pacman.d/hooks/$hookfile"
    fi
  done

  # Copy models.yaml
  log "Installing models.yaml..."
  if [ -f "$REPO_DIR/models.yaml" ]; then
    cp "$REPO_DIR/models.yaml" /etc/scarlix/models.yaml
    ok "/etc/scarlix/models.yaml"
  else
    fail "models.yaml not found in repo"
  fi

  # Copy AI stacks, agents, docker-compose files to /opt/scarlix/
  log "Copying SCARLIX stacks to /opt/scarlix/..."
  for dir in ai agents gaming voice network security monitoring workspace scarlihp hp-agent media-tools; do
    if [ -d "$REPO_DIR/$dir" ]; then
      mkdir -p "/opt/scarlix/$dir"
      cp -r "$REPO_DIR/$dir/"* "/opt/scarlix/$dir/" 2>/dev/null || true
      ok "/opt/scarlix/$dir/"
    fi
  done

  # Copy AGENTS.md, models.yaml reference
  [ -f "$REPO_DIR/AGENTS.md" ] && cp "$REPO_DIR/AGENTS.md" /etc/scarlix/AGENTS.md && ok "/etc/scarlix/AGENTS.md"

  # Create scarlix network for Docker
  log "Creating Docker network..."
  docker network create scarlix-net 2>/dev/null && ok "Docker network: scarlix-net" || info "scarlix-net already exists"

  # Reload systemd
  systemctl daemon-reload
  ok "systemd reloaded"

  checkpoint phase4
fi

echo ""

# ============================================================================
# PHASE 5: Run wizard + start services
# ============================================================================
log "=== Phase 5: Wizard + Service start ==="

if is_checkpoint phase5; then
  info "Phase 5 already complete — skipping."
else
  # Run scarlix-wizard (TUI: PC type, models, experimental)
  log "Launching SCARLIX wizard..."
  echo ""
  echo -e "${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
  echo -e "${CYAN}║  SCARLIX Setup Wizard                                         ║${NC}"
  echo -e "${CYAN}║  Answer the questions to configure your system                ║${NC}"
  echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}"
  echo ""

  if command -v scarlix-wizard >/dev/null 2>&1; then
    # Run wizard as the real user (not root) for whiptail TUI
    REAL_USER=$(grep -E '^[^:]+:x:1000:' /etc/passwd 2>/dev/null | cut -d: -f1 || echo "root")
    if [ "$REAL_USER" != "root" ]; then
      sudo -u "$REAL_USER" scarlix-wizard || warn "Wizard exited (user may have cancelled)"
    else
      scarlix-wizard || warn "Wizard exited (user may have cancelled)"
    fi
  else
    fail "scarlix-wizard not installed"
  fi

  # Enable + start first-boot service (runs 3-phase setup)
  log "Enabling scarlix-first-boot service..."
  systemctl enable scarlix-first-boot.service >> "$LOG_FILE" 2>&1 && ok "first-boot service enabled" || fail "first-boot enable"

  # Enable model-manager timer
  if [ -f /etc/systemd/system/model-manager.timer ]; then
    systemctl enable model-manager.timer >> "$LOG_FILE" 2>&1 && ok "model-manager.timer enabled (weekly Mon 04:00)" || fail "model-manager.timer"
  fi

  # Run first-boot.sh now (instead of waiting for reboot)
  log "Running 3-phase setup now (first-boot.sh)..."
  echo ""
  if [ -f /etc/systemd/system/first-boot.sh ]; then
    if /etc/systemd/system/first-boot.sh >> "$LOG_FILE" 2>&1; then
      ok "3-phase setup complete"
    else
      fail "3-phase setup had errors (check $LOG_FILE)"
    fi
  else
    fail "first-boot.sh not found"
  fi

  checkpoint phase5
fi

echo ""

# ============================================================================
# SUMMARY
# ============================================================================
echo ""
echo -e "${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${CYAN}║  SCARLIX OS v${VERSION} — Installation Summary                ║${NC}"
echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║  ✓ SUCCESS: $SUCCESS_COUNT${NC}"
[ "$FAIL_COUNT" -gt 0 ] && echo -e "${RED}║  ✗ FAILED:  $FAIL_COUNT${NC}"
echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${CYAN}║  System: $(grep '^PRETTY_NAME=' /etc/os-release | cut -d'"' -f2)${NC}"
echo -e "${CYAN}║  Kernel: $(uname -r)${NC}"
echo -e "${CYAN}║  NVIDIA: ${NVIDIA_COUNT:-0} dGPU(s)${NC}"
[ "$NVIDIA_COUNT" -gt 0 ] && echo -e "${CYAN}║  Driver: $(pacman -Q nvidia-open 2>/dev/null || pacman -Q nvidia 2>/dev/null || echo 'unknown')${NC}"
echo -e "${CYAN}║  LTS:    $(pacman -Q linux-lts 2>/dev/null || echo 'not installed')${NC}"
echo -e "${CYAN}║  ZRAM:   $(zramctl 2>/dev/null | tail -1 || echo 'pending reboot')${NC}"
echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${CYAN}║  Commands:${NC}"
echo -e "${CYAN}║    scarlix-mode status     # System summary${NC}"
echo -e "${CYAN}║    scarlix-mode vram        # VRAM health check${NC}"
echo -e "${CYAN}║    scarlix-mode ai          # Start AI inference${NC}"
echo -e "${CYAN}║    docker ps                # Running containers${NC}"
echo -e "${CYAN}║    snapper -c root list     # BTRFS snapshots${NC}"
echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${CYAN}║  Dashboard: http://$(hostname -I 2>/dev/null | awk '{print $1}' || echo 'localhost'):8090${NC}"
echo -e "${CYAN}║  Full log:  $LOG_FILE${NC}"
echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Mark as installed
touch /opt/scarlix/.installed

[ "$FAIL_COUNT" -gt 0 ] && warn "$FAIL_COUNT failures — check $LOG_FILE"

echo -e "${GREEN}SCARLIX OS v${VERSION} installed!${NC}"
echo ""
echo "Next steps:"
echo "  1. Reboot to activate NVIDIA driver + ZRAM fully"
echo "  2. Visit http://$(hostname -I 2>/dev/null | awk '{print $1}' || echo 'localhost'):8090 for dashboard"
echo "  3. Run 'scarlix-mode status' to verify system"
echo ""

exit 0
