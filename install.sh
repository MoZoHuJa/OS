#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# SCARLIX OS v18.5 — Bootstrap Installer (Secure Host-Bridge)
# ============================================================================
#
# v18.2 FIXES (vs v18.1) — Security + UX:
#   P0: real_user detection under systemd (was: $USER=root → chown root /opt/scarlix)
#   P1: scarlix-mode systemctl sudo fallback for user CLI (was: removed sudo → game/tv fails)
#   P1: token only on TTY + install.log chmod 600 (was: token in 644 log)
#   P1: FIFO/pipe/socket explicit rejection in validate_input_file
#   P1: models.yaml schema validation in download-models.sh (fail-fast on typos)
#   P1: const Version → var Version (ldflags -X main.Version only works on vars)
#   P1: go.sum removed (generated at build time by Dockerfile go mod download)
#   P2: CI smoke test chown 65532 (was: mode POST 202 failed in CI)
#   P2: migration applies pending desired-mode before rm -rf
#   P2: whiptail ESC/Cancel handling (was: set -e aborted whole script)
#   P2: bridge restores retry_count + last_error from last-transition
#   P2: creative/tv ensure .env exists before $DC
#   P2: .env.template updated (was: v15 header)
#
# v18.0.0–18.1 fixes preserved (see git history for details).
#
# USAGE (primary — safe):
#   git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
#   cd ~/scarlix-os
#   git checkout v18.5   # ALWAYS checkout specific tag (main may be ahead)
#   bash install.sh
# ============================================================================

VERSION="18.5.1"
LOG_DIR="/var/log/scarlix"
LOG_FILE="$LOG_DIR/install.log"
CHECKPOINT_DIR="/var/lib/scarlix"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$SCRIPT_DIR"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
SUCCESS_COUNT=0; FAIL_COUNT=0; CRITICAL_FAIL=0

mkdir -p "$LOG_DIR" "$CHECKPOINT_DIR"
# v18.2 P1: install.log may contain tokens (SCARLIHQ_TOKEN via info) → chmod 600
chmod 600 "$LOG_FILE" 2>/dev/null || true

log()   { echo -e "[$(date '+%H:%M:%S')] $1" | tee -a "$LOG_FILE"; }
ok()     { echo -e "${GREEN}  ✓${NC} $1" | tee -a "$LOG_FILE"; SUCCESS_COUNT=$((SUCCESS_COUNT+1)); }
fail()   { echo -e "${RED}  ✗${NC} $1" | tee -a "$LOG_FILE"; FAIL_COUNT=$((FAIL_COUNT+1)); }
# v17.9.8 P1: crit() aborts immediately (was: set flag + continue → cascading secondary errors)
crit()   {
  echo -e "${RED}  ✗ CRITICAL: $1${NC}" | tee -a "$LOG_FILE"
  FAIL_COUNT=$((FAIL_COUNT+1))
  CRITICAL_FAIL=1
  echo -e "${RED}╔══════════════════════════════════════════════════════════════╗${NC}" | tee -a "$LOG_FILE"
  echo -e "${RED}║  CRITICAL FAILURE — install aborted. Fix the error above       ║${NC}" | tee -a "$LOG_FILE"
  echo -e "${RED}║  and re-run: bash install.sh                                    ║${NC}" | tee -a "$LOG_FILE"
  echo -e "${RED}╚══════════════════════════════════════════════════════════════╝${NC}" | tee -a "$LOG_FILE"
  exit 1
}
info()   { echo -e "${CYAN}  ℹ${NC} $1" | tee -a "$LOG_FILE"; }
warn()   { echo -e "${YELLOW}  ⚠${NC} $1" | tee -a "$LOG_FILE"; }

write_checkpoint() {
  local name="$1"
  local nvidia_ver="${2:-none}"
  # P1-6 FIX: Use pacman -Q linux (not uname -r which has different format: 6.10.8-arch1-1 vs 6.10.8.arch1-1)
  local linux_ver
  linux_ver=$(pacman -Q linux 2>/dev/null | cut -d' ' -f2 || echo 'unknown')
  # v18.4 P1: Add VERSION + git commit hash to checkpoint (was: missing → upgrade skipped phases with old scripts)
  local repo_hash
  repo_hash=$(git -C "$REPO_DIR" rev-parse --short HEAD 2>/dev/null || echo "unknown")
  cat > "$CHECKPOINT_DIR/.checkpoint-$name" << EOF
phase=$name
timestamp=$(date -Iseconds)
gpu_count=${NVIDIA_COUNT:-0}
gpu_compute_cap=${GPU_COMPUTE_CAPS:-none}
nvidia_open_version=${nvidia_ver}
linux_kernel_version=${linux_ver}
scarlix_version=${VERSION}
repo_hash=${repo_hash}
EOF
  info "Checkpoint: $name (v$VERSION, hash: $repo_hash, GPU: ${NVIDIA_COUNT:-0})"
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
  # v18.4 P1: Check SCARLIX_VERSION (was: missing → upgrade skipped phases with old scripts)
  local cp_version
  cp_version=$(grep '^scarlix_version=' "$cp_file" 2>/dev/null | cut -d= -f2 || echo "")
  [ "${cp_version:-unknown}" = "$VERSION" ] || return 1
  # v18.4 P1: Check git commit hash (was: missing → same version different commit skipped)
  local cp_hash current_hash
  cp_hash=$(grep '^repo_hash=' "$cp_file" 2>/dev/null | cut -d= -f2 || echo "")
  current_hash=$(git -C "$REPO_DIR" rev-parse --short HEAD 2>/dev/null || echo "unknown")
  [ "${cp_hash:-unknown}" = "$current_hash" ] || return 1
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
  # v17.9.8 P1: system update + package install = crit (was fail → checkpoint written despite broken state)
  pacman -Syu --noconfirm >> "$LOG_FILE" 2>&1 && ok "System updated" || crit "System update failed (fix pacman conflicts, re-run)"

  log "Installing SCARLIX packages..."
  # P1 v17.9.8: Ensure [multilib] is enabled BEFORE installing steam/wine/lib32-*
  # (clean EndeavourOS may have multilib commented out → lib32-nvidia-utils install fails)
  # v17.9.8 FIX: scoped awk (was aggressive sed that uncommented EVERY Include line in pacman.conf)
  #           + explicit pacman -Sy after enable (without it: "target not found" for lib32-*)
  log "Ensuring [multilib] repository is enabled..."
  MULTILIB_OK=false
  if grep -q '^\[multilib\]' /etc/pacman.conf 2>/dev/null; then
    ok "[multilib] already enabled"
    MULTILIB_OK=true
  elif grep -Eq '^#\s*\[multilib\]' /etc/pacman.conf 2>/dev/null; then
    # Scoped: uncomment ONLY the [multilib] header + its Include line (the 2 lines in that section)
    awk '
      /^#[[:space:]]*\[multilib\][[:space:]]*$/ { sub(/^#[[:space:]]*/,""); print; in_ml=1; next }
      in_ml && /^#[[:space:]]*Include[[:space:]]*=/ { sub(/^#[[:space:]]*/,""); print; in_ml=0; next }
      in_ml && /^\[/ { in_ml=0; print; next }
      { print }
    ' /etc/pacman.conf > /tmp/pacman.conf.ml && mv /tmp/pacman.conf.ml /etc/pacman.conf
    if grep -q '^\[multilib\]' /etc/pacman.conf 2>/dev/null; then
      pacman -Sy >> "$LOG_FILE" 2>&1 && ok "[multilib] enabled + db synced" || fail "Enable [multilib]"
      MULTILIB_OK=true
    else
      fail "[multilib] enable (awk)"
    fi
  else
    # Append multilib section if not present at all
    printf '\n[multilib]\nInclude = /etc/pacman.d/mirrorlist\n' >> /etc/pacman.conf
    pacman -Sy >> "$LOG_FILE" 2>&1 && ok "[multilib] added + db synced" || fail "Add [multilib]"
    MULTILIB_OK=true
  fi
  # Hard-fail if multilib still not active (Steam/Wine/lib32-* will break otherwise)
  if [ "$MULTILIB_OK" = false ]; then
    crit "[multilib] could not be enabled — Steam/Wine/lib32-* will fail to install"
  fi

  PKGS=$(grep -vE '^\s*#|^\s*$' "$REPO_DIR/packages.x86_64" | grep -v '^yay$' | grep -v '^calamares$' || true)
  # v17.9.8 P1: package install = crit (was fail → checkpoint written despite missing yq/docker/etc.)
  [ -n "$PKGS" ] && pacman -S --noconfirm --needed $PKGS >> "$LOG_FILE" 2>&1 && ok "Packages installed" || crit "Package install failed (check /var/log/scarlix/install.log)"

  mkdir -p /opt/scarlix /var/lib/scarlix /etc/scarlix/{profiles,secrets}
  mkdir -p /models /var/lib/docker /var/lib/scarlix/ollama /mnt/{files,games,photos,backup/restic}
  # v18.5 P0: /var/lib/scarlix must be root:root 755 (was: chown -R REAL_USER → symlink attack on bridge-state/input)
  # Only /mnt is user-owned (user files). /var/lib/scarlix contains security-critical bridge dirs.
  chown -R "$REAL_USER:$REAL_USER" /mnt 2>/dev/null || true
  # /var/lib/scarlix stays root:root (install.sh creates it with mkdir, which defaults to root)
  chown root:root /var/lib/scarlix 2>/dev/null || true
  chmod 755 /var/lib/scarlix 2>/dev/null || true
  # /etc/scarlix stays root:root (user reads models.yaml but can't modify .env)
  # /opt/scarlix stays root:root (user reads scripts but can't modify .env)
  # v18.4 P0: Ensure config dirs are root-owned (security boundary)
  chown root:root /etc/scarlix /opt/scarlix 2>/dev/null || true
  chmod 755 /etc/scarlix /opt/scarlix 2>/dev/null || true
  # Q2a v17.8: Ollama volume root:root + 700 (was 777 — unnecessary security hole)
  chown root:root /var/lib/scarlix/ollama 2>/dev/null || true
  chmod 700 /var/lib/scarlix/ollama 2>/dev/null || true
  # P1 v17.9.7: /models chmod 750 (was 775 — tighter; containers read as root via :ro)
  chown -R "$REAL_USER:$REAL_USER" /models 2>/dev/null || true
  chmod 750 /models 2>/dev/null || true

  # P1 v17.9.7: Verify yq is functional (scripts depend on it — fail hard if broken)
  if command -v yq >/dev/null 2>&1; then
    if yq -r '.sglang' /dev/stdin <<<"sglang: ok" >/dev/null 2>&1; then
      ok "yq functional ($(yq --version 2>&1 | head -1))"
    else
      crit "yq installed but NOT functional — scarlix-mode will crash. Install go-yq: 'yay -S go-yq'"
    fi
  else
    crit "yq not installed — scarlix-mode/doctor/download-models.sh will fail. Check packages.x86_64"
  fi

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
  # P1 v17.9.7: disconnect loop before rm (was: rm fails on active endpoints → upgrade breaks)
  if docker network inspect scarlix_net >/dev/null 2>&1; then
    log "  Old network scarlix_net found — disconnecting containers..."
    for c in $(docker network inspect scarlix_net -f '{{range .Containers}}{{.Name}} {{end}}' 2>/dev/null); do
      docker network disconnect scarlix_net "$c" >> "$LOG_FILE" 2>&1 && info "  disconnected $c from scarlix_net" || true
    done
    docker network rm scarlix_net >> "$LOG_FILE" 2>&1 && warn "Removed old network scarlix_net (migrated to scarlix-net)" || info "scarlix_net removal deferred (will retry)"
  fi
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
  # v17.9.8: added scarlix-host-bridge (privileged ops for ScarliHQ dashboard)
  log "Installing SCARLIX scripts (CRITICAL)..."
  for binfile in scarlix-wizard scarlix-mode model-manager.sh download-models.sh scarlix-doctor scarlix-host-bridge; do
    src="$REPO_DIR/files/usr/local/bin/$binfile"
    if [ -f "$src" ]; then
      cp "$src" "/usr/local/bin/$binfile" && chmod 755 "/usr/local/bin/$binfile" && ok "/usr/local/bin/$binfile" || crit "$binfile copy failed"
    else
      crit "$binfile not found in repo"
    fi
  done

  # Systemd services (model-manager + host-bridge — first-boot removed Q6a)
  log "Installing systemd services..."
  for f in model-manager.service model-manager.timer generate-env.sh scarlix-host-bridge.service scarlix-host-bridge.timer; do
    src="$REPO_DIR/files/etc/systemd/system/$f"
    if [ -f "$src" ]; then
      cp "$src" "/etc/systemd/system/$f"
      case "$f" in *.service|*.timer) chmod 644 "/etc/systemd/system/$f" ;; *) chmod 755 "/etc/systemd/system/$f" ;; esac
      ok "/etc/systemd/system/$f"
    else
      crit "$f not found"
    fi
  done

  # v17.9.8: Copy VERSION file to /etc/scarlix/ + /usr/local/share/scarlix/
  # (host-bridge reads it for host-status.json "scarlix_version" field)
  if [ -f "$REPO_DIR/VERSION" ]; then
    cp "$REPO_DIR/VERSION" /etc/scarlix/VERSION 2>/dev/null || true
    mkdir -p /usr/local/share/scarlix
    cp "$REPO_DIR/VERSION" /usr/local/share/scarlix/VERSION 2>/dev/null || true
    ok "VERSION file installed ($(cat "$REPO_DIR/VERSION"))"
  fi

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
  # v18.5 P1: Don't overwrite user's models.yaml on upgrade (was: unconditional cp → lost custom model config)
  [ -f "$REPO_DIR/models.yaml" ] || crit "models.yaml not found"
  if [ ! -f /etc/scarlix/models.yaml ]; then
    cp "$REPO_DIR/models.yaml" /etc/scarlix/models.yaml
    ok "/etc/scarlix/models.yaml (installed from repo)"
  else
    # User already has a models.yaml — preserve it, but show diff for reference
    if ! diff -q "$REPO_DIR/models.yaml" /etc/scarlix/models.yaml >/dev/null 2>&1; then
      info "/etc/scarlix/models.yaml: preserved (user-customized, repo version available at $REPO_DIR/models.yaml)"
    else
      ok "/etc/scarlix/models.yaml (unchanged)"
    fi
  fi
  [ -f "$REPO_DIR/AGENTS.md" ] && { cp "$REPO_DIR/AGENTS.md" /etc/scarlix/AGENTS.md; ok "/etc/scarlix/AGENTS.md"; } || warn "AGENTS.md not found (non-critical)"

  # Q6a: Copy ALL stacks INCLUDING scarlihq (was typo'd as scarlihp in v17.3)
  log "Copying SCARLIX stacks to /opt/scarlix/..."
  for dir in ai agents gaming voice network security monitoring workspace scarlihq hp-agent media-tools; do
    if [ -d "$REPO_DIR/$dir" ]; then
      mkdir -p "/opt/scarlix/$dir"
      # v18.4 P1: rsync --delete (was: cp -r — left stale files from old versions)
      if command -v rsync >/dev/null 2>&1; then
        if rsync -a --delete "$REPO_DIR/$dir/" "/opt/scarlix/$dir/" 2>/dev/null; then
          ok "/opt/scarlix/$dir/ (rsync --delete)"
        else
          crit "Failed to rsync $dir/ to /opt/scarlix/"
        fi
      else
        # Fallback: rm + cp if rsync not available
        rm -rf "/opt/scarlix/$dir"
        mkdir -p "/opt/scarlix/$dir"
        if cp -r "$REPO_DIR/$dir/"* "/opt/scarlix/$dir/" 2>/dev/null; then
          ok "/opt/scarlix/$dir/ (rm+cp fallback)"
        else
          crit "Failed to copy $dir/ to /opt/scarlix/"
        fi
      fi
    else
      warn "$dir/ not in repo (non-critical)"
    fi
  done

  # v18.5 P0: Fix /opt/scarlix/.env ownership on upgrade (was: only /etc/scarlix/.env fixed in v18.4)
  # Systems upgraded from v18.0-18.3 have user-owned .env → LPE via source
  if [ -f /opt/scarlix/.env ]; then
    chown root:root /opt/scarlix/.env 2>/dev/null || true
    chmod 600 /opt/scarlix/.env 2>/dev/null || true
    ok "/opt/scarlix/.env ownership fixed (root:root 600)"
  fi

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

  # v17.9.8: Enable host-bridge timer (writes host-status.json every 5s for ScarliHQ)
  if [ -f /etc/systemd/system/scarlix-host-bridge.timer ]; then
    systemctl enable --now scarlix-host-bridge.timer >> "$LOG_FILE" 2>&1 && ok "scarlix-host-bridge.timer enabled + started" || fail "host-bridge.timer"
    # v18.0.0 P0: SEPARATE bridge-input/ (65532 writable) from bridge-state/ (root 700)
    # Was (v17.9.9): single bridge/ dir owned by 65532 + root wrote .retry there = symlink attack.
    # Now: ScarliHQ can ONLY write to bridge-input/desired-mode. Root writes ALL state to
    # bridge-state/ (root:root 700) — ScarliHQ cannot create symlinks there.
    #
    # Migration: remove old bridge/ dir if exists (v17.9.9 layout)
    if [ -d /var/lib/scarlix/bridge ]; then
      log "Migrating from v17.9.9 bridge/ layout → v18.0.0 bridge-input/ + bridge-state/..."
      # v18.2 P2: apply pending desired-mode BEFORE rm -rf (was: lost pending mode switch)
      # v18.3 P0: removed `local` (was outside function — bash error)
      if [ -f /var/lib/scarlix/bridge/desired-mode ]; then
        pending_mode=$(tr -d '[:space:]' < /var/lib/scarlix/bridge/desired-mode 2>/dev/null || echo "")
        if [ -n "$pending_mode" ]; then
          log "  Found pending desired-mode '$pending_mode' — applying before migration..."
          [ -x /usr/local/bin/scarlix-mode ] && /usr/local/bin/scarlix-mode "$pending_mode" >> "$LOG_FILE" 2>&1 || true
        fi
      fi
      # v18.1 P1: rm -rf (was: rmdir — fails if dir has hidden files like .retry)
      rm -rf /var/lib/scarlix/bridge 2>/dev/null || true
    fi
    # v18.5 P0: Ensure parent /var/lib/scarlix is root-owned BEFORE creating bridge dirs
    chown root:root /var/lib/scarlix 2>/dev/null || true
    chmod 755 /var/lib/scarlix 2>/dev/null || true
    # bridge-input: ScarliHQ nonroot (65532) writes desired-mode here (mode 700 — owner only)
    mkdir -p /var/lib/scarlix/bridge-input
    chown 65532:65532 /var/lib/scarlix/bridge-input 2>/dev/null || chown nobody:nobody /var/lib/scarlix/bridge-input
    chmod 700 /var/lib/scarlix/bridge-input
    # bridge-state: host bridge (root) writes retry + last-transition here (NOT writable by ScarliHQ)
    mkdir -p /var/lib/scarlix/bridge-state
    chown root:root /var/lib/scarlix/bridge-state
    chmod 700 /var/lib/scarlix/bridge-state
    ok "bridge-input/ (uid 65532, 700) + bridge-state/ (root, 700) — symlink attack prevented"
  fi

  # v17.9.8: Generate /etc/scarlix/.env (secrets + SCARLIHQ_TOKEN) if not already
  if [ ! -f /etc/scarlix/.env ] && [ -f /etc/systemd/system/generate-env.sh ]; then
    log "Generating /etc/scarlix/.env (secrets + SCARLIHQ_TOKEN)..."
    /etc/systemd/system/generate-env.sh >> "$LOG_FILE" 2>&1 && ok ".env generated" || warn ".env generation (non-critical)"
  fi
  # v18.4 P0: /etc/scarlix/.env MUST be root:root 600 (contains secrets, root services source it)
  chown root:root /etc/scarlix/.env 2>/dev/null || true
  chmod 600 /etc/scarlix/.env 2>/dev/null || true

  # P1 v17.9.8: Build + start ScarliHQ dashboard (alpine image — NO nvidia needed, builds pre-reboot)
  # Architecture: ScarliHQ reads host-status.json (written by host-bridge timer), writes desired-mode.
  # No docker.sock, no scarlix-mode mount, no nvidia runtime → minimal privilege.
  if [ -f /opt/scarlix/scarlihq/Dockerfile ]; then
    log "Building ScarliHQ dashboard image (scarlihq:latest, alpine)..."
    # v18.4 P0: SAFE parse SCARLIHQ_TOKEN from /etc/scarlix/.env (was: `source` → LPE on re-run)
    if [ -f /etc/scarlix/.env ]; then
      SCARLIHQ_TOKEN=$(grep '^SCARLIHQ_TOKEN=' /etc/scarlix/.env 2>/dev/null | cut -d= -f2 || echo "")
    fi
    # Export for compose (compose reads ${SCARLIHQ_TOKEN} from environment)
    export SCARLIHQ_TOKEN="${SCARLIHQ_TOKEN:-}"
    # v17.9.9 P2: pass VERSION as build-arg (Dockerfile injects via -ldflags -X main.Version)
    if docker build --build-arg SCARLIX_VERSION="$VERSION" -t scarlihq:latest /opt/scarlix/scarlihq/ >> "$LOG_FILE" 2>&1; then
      ok "ScarliHQ image built (scarlihq:latest, alpine ~20MB, version $VERSION)"
      # Start dashboard on :8090 (compose references scarlihq:latest — matches build tag)
      if docker compose --env-file /etc/scarlix/.env -f /opt/scarlix/scarlihq/docker-compose.yml up -d >> "$LOG_FILE" 2>&1; then
        ok "ScarliHQ dashboard started on :8090"
        if [ -n "$SCARLIHQ_TOKEN" ]; then
          # v18.2 P1: token only on TTY (was: info() → tee to log 644 → token leaked)
          if [ -t 1 ]; then
            echo -e "${CYAN}  Dashboard login token: ${GREEN}${SCARLIHQ_TOKEN}${NC}" | tee /dev/tty 2>/dev/null || true
            echo -e "${CYAN}  Open: http://<this-ip>:8090/?token=${SCARLIHQ_TOKEN}${NC}" | tee /dev/tty 2>/dev/null || true
            echo -e "${CYAN}  (token also in /etc/scarlix/.env — chmod 600)${NC}" | tee /dev/tty 2>/dev/null || true
          else
            info "  Dashboard token generated (in /etc/scarlix/.env — run 'cat /etc/scarlix/.env | grep SCARLIHQ_TOKEN')"
          fi
        fi
      else
        warn "ScarliHQ dashboard start failed (non-critical — run 'docker compose --env-file /etc/scarlix/.env -f /opt/scarlix/scarlihq/docker-compose.yml up -d' later)"
      fi
    else
      warn "ScarliHQ build failed (non-critical — dashboard optional. Check /var/log/scarlix/install.log)"
      warn "  Common cause: no internet for Go module download, or Go syntax error"
    fi
  else
    warn "ScarliHQ Dockerfile not found — dashboard not built"
  fi

  # P1 FIX v17.9: Download starter model for ALL systems (was NVIDIA_COUNT > 0 only)
  # Ollama is CPU fallback — needed even on dev_workstation without GPU
  if command -v docker >/dev/null 2>&1; then
    # P1 v17.9.7: Disk-space pre-check (starter model ~2GB, full models 50-150GB)
    MODELS_FREE_MB=$(df -m /models 2>/dev/null | awk 'NR==2{print $4}')
    if [ -n "$MODELS_FREE_MB" ] && [ "$MODELS_FREE_MB" -lt 2048 ] 2>/dev/null; then
      warn "/models has only ${MODELS_FREE_MB}MB free (< 2GB) — skipping starter model download"
      warn "  Free space then run: download-models.sh"
    else
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
    fi  # end disk-space else
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
