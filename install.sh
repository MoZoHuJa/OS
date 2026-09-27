#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# SCARLIX OS v17.4 — Bootstrap Installer (Unified, Fail-Hard, HW-Aware)
# ============================================================================
#
# v17.4 CORRECTION (vs v17.3):
#   Q1a: curl|bash REMOVED as primary. Primary = git clone + review + bash.
#   Q2a: vLLM TP=1 (single GPU) — SGLang GPU0 + vLLM GPU1 (no TP=2, different arch OK)
#   Q3b: BTRFS subvol creation REMOVED — chattr +C on dirs only (Calamares made @ + @home)
#   Q4a: Docker network unified to scarlix-net (was scarlix_ai in compose files)
#   Q5a: Calamares + [docker] repo REMOVED. nvidia-container-toolkit via yay (AUR)
#   Q6a: Model paths synced (SGLang uses /Qwen3-14B-Instruct-AWQ matching models.yaml)
#   Q7a: Unified 5 phases (merged first-boot.sh into install.sh). Checkpoint has GPU info.
#   Q8a: scarlix-mode ai = verified path. Fallback SGLang→vLLM→BeeLlama. ISO profile removed.
#
# USAGE (primary — safe):
#   git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
#   cd ~/scarlix-os
#   nano install.sh   # review what it does
#   bash install.sh
#
# USAGE (secondary — convenience, downloads repo first):
#   curl -fsSL https://raw.githubusercontent.com/MoZoHuJa/OS/main/install.sh | bash -s -- --clone
#
# IDEMPOTENT: Checkpoints in /var/lib/scarlix/.checkpoint-* (include GPU count + compute_cap).
#   If HW changes (add/remove GPU), Phase 2 re-runs automatically.
# FAIL-HARD: If critical failure (NVIDIA, Docker), exits non-zero, does NOT mark .installed.
# ============================================================================

VERSION="17.4.0"
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

# Checkpoint stores GPU info — if HW changes, phase re-runs
write_checkpoint() {
  local name="$1"
  local gpu_info="${2:-}"
  cat > "$CHECKPOINT_DIR/.checkpoint-$name" << EOF
phase=$name
timestamp=$(date -Iseconds)
gpu_count=${NVIDIA_COUNT:-0}
gpu_compute_cap=${GPU_COMPUTE_CAPS:-none}
gpu_names=${GPU_NAMES:-none}
$gpu_info
EOF
  info "Checkpoint: $name (GPU: ${NVIDIA_COUNT:-0}, compute_cap: ${GPU_COMPUTE_CAPS:-none})"
}

is_checkpoint_valid() {
  local name="$1"
  local cp_file="$CHECKPOINT_DIR/.checkpoint-$name"
  [ -f "$cp_file" ] || return 1
  # Check GPU count matches
  local cp_gpu_count
  cp_gpu_count=$(grep '^gpu_count=' "$cp_file" 2>/dev/null | cut -d= -f2 || echo 0)
  [ "${cp_gpu_count:-0}" = "${NVIDIA_COUNT:-0}" ] || return 1
  # Check compute_cap matches
  local cp_cap
  cp_cap=$(grep '^gpu_compute_cap=' "$cp_file" 2>/dev/null | cut -d= -f2 || echo "none")
  [ "${cp_cap:-none}" = "${GPU_COMPUTE_CAPS:-none}" ] || return 1
  return 0
}

# ============================================================================
# BANNER
# ============================================================================
echo ""
echo -e "${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${CYAN}║  SCARLIX OS v${VERSION} — Bootstrap Installer                ║${NC}"
echo -e "${CYAN}║  EndeavourOS Edition · Unified · Fail-Hard · HW-Aware       ║${NC}"
echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}"
echo ""
log "Starting SCARLIX OS v${VERSION} bootstrap installer"
log "Log: $LOG_FILE"
echo ""

# ============================================================================
# PRE-CHECKS
# ============================================================================
log "=== Pre-checks ==="

# Handle --clone flag (curl|bash convenience)
if [ "${1:-}" = "--clone" ]; then
  if [ ! -f "$REPO_DIR/scarlix/profiledef.sh" ]; then
    log "Repo not found — cloning from GitHub..."
    TMP_CLONE="/tmp/scarlix-install-$$"
    git clone https://github.com/MoZoHuJa/OS.git "$TMP_CLONE" || { crit "git clone failed"; exit 1; }
    cd "$TMP_CLONE"
    REPO_DIR="$TMP_CLONE"
    SCRIPT_DIR="$TMP_CLONE"
    ok "Repo cloned to $REPO_DIR"
  fi
fi

# Check Arch/EndeavourOS
if [ ! -f /etc/arch-release ]; then
  echo -e "${RED}ERROR: This script requires Arch Linux or EndeavourOS.${NC}"
  echo "Detected: $(grep '^PRETTY_NAME=' /etc/os-release 2>/dev/null | cut -d'"' -f2 || echo 'unknown')"
  exit 1
fi
ok "Arch/EndeavourOS: $(grep '^PRETTY_NAME=' /etc/os-release | cut -d'"' -f2)"

# Root check
if [ "$(id -u)" -ne 0 ]; then
  echo -e "${YELLOW}Re-running with sudo...${NC}"
  exec sudo -E bash "$0" "$@"
fi
ok "Running as root"

# Detect real user (for makepkg/yay — must NOT be root)
REAL_USER="${SUDO_USER:-$(logname 2>/dev/null || echo "")}"
if [ -z "$REAL_USER" ] || [ "$REAL_USER" = "root" ]; then
  REAL_USER=$(grep -E '^[^:]+:x:1000:' /etc/passwd 2>/dev/null | cut -d: -f1 || echo "")
fi
if [ -z "$REAL_USER" ]; then
  crit "No non-root user found (UID 1000). Create a user first."
  exit 1
fi
ok "Real user: $REAL_USER (for makepkg/yay)"

# Internet check
ping -c1 -W3 archlinux.org >/dev/null 2>&1 || { crit "No internet"; exit 1; }
ok "Internet connection"

# Repo files check
if [ ! -f "$REPO_DIR/scarlix/packages.x86_64" ]; then
  crit "SCARLIX repo not found. Run: git clone https://github.com/MoZoHuJa/OS.git"
  exit 1
fi
ok "Repo at $REPO_DIR"

# Detect NVIDIA GPUs + compute capability (for TP=2 decision)
NVIDIA_GPUS=$(lspci -nn 2>/dev/null | grep -iE 'NVIDIA.*(VGA|3D)' || true)
NVIDIA_COUNT=$(echo "$NVIDIA_GPUS" | grep -c . 2>/dev/null || echo 0)
GPU_NAMES=""
GPU_COMPUTE_CAPS=""

if [ "$NVIDIA_COUNT" -gt 0 ]; then
  log "NVIDIA dGPU(s) detected: $NVIDIA_COUNT"
  echo "$NVIDIA_GPUS" | while read -r line; do info "  $line"; done
  GPU_NAMES=$(echo "$NVIDIA_GPUS" | sed 's/.*NVIDIA[^:]*: //' | cut -d'(' -f1 | xargs | tr '\n' ';')
  # compute_cap will be checked after nvidia-smi is available (Phase 2)
fi

# Already installed?
if [ -f /opt/scarlix/.installed ] && [ "$CRITICAL_FAIL" -eq 0 ]; then
  echo -e "${YELLOW}SCARLIX already installed. Re-run is idempotent (HW-aware).${NC}"
  read -rp "Continue? [y/N] " REINSTALL
  [ "$REINSTALL" != "y" ] && [ "$REINSTALL" != "Y" ] && exit 0
fi

echo ""

# ============================================================================
# PHASE 1: System packages + BTRFS CoW + Snapper + ZRAM
# ============================================================================
log "=== Phase 1: System packages + BTRFS CoW + Snapper + ZRAM ==="

if is_checkpoint_valid phase1; then
  info "Phase 1 checkpoint valid (same HW) — skipping."
else
  # Update system
  log "Updating system..."
  pacman -Syu --noconfirm >> "$LOG_FILE" 2>&1 && ok "System updated" || fail "System update"

  # Install packages — EXCLUDE calamares (ISO-only) and yay (installed via git clone)
  log "Installing SCARLIX packages..."
  PKG_FILE="$REPO_DIR/scarlix/packages.x86_64"
  PKGS=$(grep -vE '^\s*#|^\s*$' "$PKG_FILE" | grep -v '^yay$' | grep -v '^calamares$' || true)
  if [ -n "$PKGS" ]; then
    if pacman -S --noconfirm --needed $PKGS >> "$LOG_FILE" 2>&1; then
      ok "Packages installed"
    else
      fail "Package install (some may have failed)"
    fi
  fi

  # Q3b: NO BTRFS subvol creation — Calamares made @ + @home. We just chattr +C on dirs.
  mkdir -p /opt/scarlix /var/lib/scarlix /etc/scarlix/{profiles,secrets}
  mkdir -p /models /var/lib/docker /mnt/{files,games,photos,backup/restic} /srv /var/log /.snapshots
  chown -R "$REAL_USER:$REAL_USER" /opt/scarlix /var/lib/scarlix /etc/scarlix /mnt 2>/dev/null || true

  # chattr +C on directories (works on BTRFS — disables CoW for new files)
  log "Disabling CoW on heavy stores..."
  for d in /models /var/lib/docker /var/lib/scarlix /mnt/games; do
    chattr +C "$d" 2>/dev/null && info "CoW disabled: $d" || info "CoW not applicable: $d"
  done

  # Snapper configs (if BTRFS)
  if command -v snapper >/dev/null 2>&1; then
    log "Configuring Snapper..."
    snapper -c root list >/dev/null 2>&1 || snapper -c root create-config / >> "$LOG_FILE" 2>&1 || warn "root Snapper config"
    snapper -c home list >/dev/null 2>&1 || snapper -c home create-config /home >> "$LOG_FILE" 2>&1 || warn "home Snapper config"
    systemctl enable --now snapper-timeline.timer >> "$LOG_FILE" 2>&1 && ok "Snapper timeline" || fail "Snapper timeline"
    systemctl enable --now snapper-cleanup.timer >> "$LOG_FILE" 2>&1 && ok "Snapper cleanup" || fail "Snapper cleanup"
  fi

  # ZRAM
  if [ -f "$REPO_DIR/scarlix/airootfs/etc/systemd/zram-generator.conf" ]; then
    cp "$REPO_DIR/scarlix/airootfs/etc/systemd/zram-generator.conf" /etc/systemd/zram-generator.conf
    systemctl daemon-reload 2>/dev/null || true
    systemctl start systemd-zram-setup@zram0 2>/dev/null || true
    ok "ZRAM configured"
  fi

  write_checkpoint phase1
fi

echo ""

# ============================================================================
# PHASE 2: NVIDIA + CUDA (dGPU detect, atomic nvidia-open-lts, compute_cap)
# ============================================================================
log "=== Phase 2: NVIDIA + CUDA (auto-detect, atomic install) ==="

if [ "$NVIDIA_COUNT" -eq 0 ]; then
  info "No NVIDIA dGPU — skipping Phase 2."
  write_checkpoint phase2 "skipped=no_nvidia"
else
  # Get compute capability AFTER driver install (if nvidia-smi available)
  # First check if driver already installed (re-run scenario)
  if command -v nvidia-smi >/dev/null 2>&1; then
    GPU_COMPUTE_CAPS=$(nvidia-smi --query-gpu=compute_cap --format=csv,noheader 2>/dev/null | tr '\n' ';' | sed 's/;$//')
    log "Compute capabilities: $GPU_COMPUTE_CAPS"
    GPU_NAMES=$(nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null | tr '\n' ';' | sed 's/;$//')
  fi

  if is_checkpoint_valid phase2; then
    info "Phase 2 checkpoint valid (same GPU count + compute_cap) — skipping."
  else
    # Determine Turing+ for nvidia-open
    GPU_IS_TURING_PLUS=false
    GPU_NAME=$(echo "$NVIDIA_GPUS" | head -1 | sed 's/.*NVIDIA[^:]*: //' | cut -d'(' -f1 | xargs)
    info "Primary GPU: $GPU_NAME"
    if echo "$GPU_NAME" | grep -qiE "RTX [2-9]|GTX 16[0-9]|Quadro RTX|A[2-9]|A100|H100"; then
      GPU_IS_TURING_PLUS=true
      info "Architecture: Turing+ → nvidia-open"
    else
      info "Architecture: pre-Turing → nvidia (proprietary)"
    fi

    # Q5a+Q11: Atomic install — linux-lts + linux-lts-headers + nvidia-open-lts TOGETHER
    # Without headers, DKMS module won't build → black screen
    log "Installing NVIDIA driver (atomic: driver + both kernels + headers)..."
    if [ "$GPU_IS_TURING_PLUS" = true ]; then
      if pacman -S --noconfirm --needed \
        nvidia-open nvidia-open-lts nvidia-utils lib32-nvidia-utils nvidia-settings \
        linux-lts linux-lts-headers \
        >> "$LOG_FILE" 2>&1; then
        ok "NVIDIA open driver + linux-lts + headers (atomic)"
      else
        crit "NVIDIA open driver install failed"
        fail "Trying proprietary fallback..."
        pacman -S --noconfirm --needed nvidia nvidia-lts nvidia-utils lib32-nvidia-utils linux-lts linux-lts-headers >> "$LOG_FILE" 2>&1 && ok "NVIDIA proprietary (fallback)" || crit "NVIDIA proprietary"
      fi
    else
      pacman -S --noconfirm --needed nvidia nvidia-lts nvidia-utils lib32-nvidia-utils nvidia-settings linux-lts linux-lts-headers >> "$LOG_FILE" 2>&1 && ok "NVIDIA proprietary + linux-lts + headers" || crit "NVIDIA install"
    fi

    # CUDA + cuDNN
    log "Installing CUDA + cuDNN..."
    pacman -S --noconfirm --needed cuda cudnn >> "$LOG_FILE" 2>&1 && ok "CUDA + cuDNN" || fail "CUDA + cuDNN"

    # Verify linux-lts
    pacman -Q linux-lts >/dev/null 2>&1 && info "linux-lts: $(pacman -Q linux-lts)" || crit "linux-lts not installed"

    # Rebuild initramfs
    log "Rebuilding initramfs..."
    mkinitcpio -P >> "$LOG_FILE" 2>&1 && ok "initramfs rebuild" || fail "initramfs rebuild"

    # GRUB nvidia_drm.modeset=1
    if [ -f /etc/default/grub ] && ! grep -q "nvidia_drm.modeset=1" /etc/default/grub; then
      sed -i 's/GRUB_CMDLINE_LINUX_DEFAULT="\(.*\)"/GRUB_CMDLINE_LINUX_DEFAULT="\1 nvidia_drm.modeset=1"/' /etc/default/grub
      grub-mkconfig -o /boot/grub/grub.cfg >> "$LOG_FILE" 2>&1 && ok "GRUB nvidia_drm.modeset=1" || fail "GRUB update"
    else
      ok "GRUB config (already set)"
    fi

    # Now get compute_cap (driver just installed)
    if command -v nvidia-smi >/dev/null 2>&1; then
      GPU_COMPUTE_CAPS=$(nvidia-smi --query-gpu=compute_cap --format=csv,noheader 2>/dev/null | tr '\n' ';' | sed 's/;$//')
      GPU_NAMES=$(nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null | tr '\n' ';' | sed 's/;$//')
      log "Compute capabilities: $GPU_COMPUTE_CAPS"
      log "GPU names: $GPU_NAMES"
    fi

    write_checkpoint phase2
  fi
fi

echo ""

# ============================================================================
# PHASE 3: Docker + nvidia-container-toolkit (AUR via yay) + network
# ============================================================================
log "=== Phase 3: Docker + nvidia-container-toolkit + network ==="

if is_checkpoint_valid phase3; then
  info "Phase 3 checkpoint valid — skipping."
else
  # Start Docker
  log "Starting Docker..."
  systemctl start docker >> "$LOG_FILE" 2>&1 && ok "Docker started" || { crit "Docker start"; }
  systemctl enable docker >> "$LOG_FILE" 2>&1 && ok "Docker enabled on boot" || fail "Docker enable"
  sleep 2

  # Q5a: Install yay via git clone (as REAL_USER, NOT root)
  log "Installing yay (AUR helper)..."
  if command -v yay >/dev/null 2>&1; then
    info "yay already installed"
    ok "yay available"
  else
    YAY_BUILD="/tmp/yay-build"
    rm -rf "$YAY_BUILD"
    if sudo -u "$REAL_USER" git clone https://aur.archlinux.org/yay.git "$YAY_BUILD" >> "$LOG_FILE" 2>&1; then
      if sudo -u "$REAL_USER" bash -c "cd $YAY_BUILD && makepkg -si --noconfirm" >> "$LOG_FILE" 2>&1; then
        ok "yay installed (as $REAL_USER)"
      else
        fail "yay makepkg build (as $REAL_USER)"
      fi
    else
      fail "yay git clone"
    fi
    rm -rf "$YAY_BUILD"
  fi

  # nvidia-container-toolkit via yay (AUR — no [docker] repo, it doesn't exist on Arch)
  if [ "$NVIDIA_COUNT" -gt 0 ]; then
    log "Installing nvidia-container-toolkit (AUR via yay)..."
    if sudo -u "$REAL_USER" yay -S --noconfirm nvidia-container-toolkit >> "$LOG_FILE" 2>&1; then
      ok "nvidia-container-toolkit"
      nvidia-ctk runtime configure --runtime=docker >> "$LOG_FILE" 2>&1 && ok "Docker NVIDIA runtime" || fail "Docker NVIDIA runtime"
      systemctl restart docker >> "$LOG_FILE" 2>&1 || true
    else
      fail "nvidia-container-toolkit (AUR)"
    fi

    # downgrade (AUR) — for NVIDIA driver rollback
    log "Installing downgrade (AUR) for NVIDIA rollback..."
    sudo -u "$REAL_USER" yay -S --noconfirm downgrade >> "$LOG_FILE" 2>&1 && ok "downgrade" || fail "downgrade (AUR)"
  fi

  # Q4a: Create unified Docker network scarlix-net (was scarlix_ai in compose files)
  log "Creating Docker network scarlix-net..."
  docker network create scarlix-net 2>/dev/null && ok "Docker network: scarlix-net" || info "scarlix-net exists"

  write_checkpoint phase3
fi

echo ""

# ============================================================================
# PHASE 4: Copy SCARLIX files + fix compose networks + model paths
# ============================================================================
log "=== Phase 4: Copy SCARLIX files + unify compose ==="

if is_checkpoint_valid phase4; then
  info "Phase 4 checkpoint valid — skipping."
else
  # Copy binaries
  log "Installing SCARLIX scripts..."
  for binfile in scarlix-wizard scarlix-mode model-manager.sh; do
    src="$REPO_DIR/scarlix/airootfs/usr/local/bin/$binfile"
    [ -f "$src" ] && { cp "$src" "/usr/local/bin/$binfile"; chmod 755 "/usr/local/bin/$binfile"; ok "/usr/local/bin/$binfile"; } || fail "$binfile not found"
  done

  # Copy systemd services + scripts
  for f in scarlix-first-boot.service model-manager.service model-manager.timer first-boot.sh download-models.sh generate-env.sh; do
    src="$REPO_DIR/scarlix/airootfs/etc/systemd/system/$f"
    [ -f "$src" ] && {
      cp "$src" "/etc/systemd/system/$f"
      case "$f" in *.service|*.timer) chmod 644 "/etc/systemd/system/$f" ;; *) chmod 755 "/etc/systemd/system/$f" ;; esac
      ok "/etc/systemd/system/$f"
    }
  done

  # Copy pacman hooks
  mkdir -p /etc/pacman.d/hooks
  for h in scarlix-docker-backup.hook scarlix-docker-backup.sh; do
    src="$REPO_DIR/scarlix/airootfs/etc/pacman.d/hooks/$h"
    if [ -f "$src" ]; then
      cp "$src" "/etc/pacman.d/hooks/$h"
      case "$h" in
        *.hook) chmod 644 "/etc/pacman.d/hooks/$h" ;;
        *) chmod 755 "/etc/pacman.d/hooks/$h" ;;
      esac
      ok "/etc/pacman.d/hooks/$h"
    fi
  done

  # Copy models.yaml (model-agnostic — user can edit)
  [ -f "$REPO_DIR/models.yaml" ] && { cp "$REPO_DIR/models.yaml" /etc/scarlix/models.yaml; ok "/etc/scarlix/models.yaml (model-agnostic)"; }
  [ -f "$REPO_DIR/AGENTS.md" ] && { cp "$REPO_DIR/AGENTS.md" /etc/scarlix/AGENTS.md; ok "/etc/scarlix/AGENTS.md"; }

  # Q4a+Q4-fix: Copy stacks, UNIFY network name scarlix_ai → scarlix-net
  log "Copying SCARLIX stacks to /opt/scarlix/..."
  # FIX: scarlihp → scarlihq (typo from v17.3)
  for dir in ai agents gaming voice network security monitoring workspace scarlihq hp-agent media-tools; do
    if [ -d "$REPO_DIR/$dir" ]; then
      mkdir -p "/opt/scarlix/$dir"
      cp -r "$REPO_DIR/$dir/"* "/opt/scarlix/$dir/" 2>/dev/null || true
      ok "/opt/scarlix/$dir/"
    fi
  done

  # Q4a: Unify Docker network name in ALL compose files (scarlix_ai → scarlix-net)
  log "Unifying Docker network name (scarlix_ai → scarlix-net)..."
  find /opt/scarlix -name 'docker-compose*.yml' -exec sed -i 's/scarlix_ai/scarlix-net/g' {} \; 2>/dev/null
  ok "Network unified to scarlix-net"

  # Q6a: Fix SGLang model path to match models.yaml (/Qwen3-14B-Instruct-AWQ)
  log "Syncing model paths with models.yaml..."
  SGLANG_COMPOSE="/opt/scarlix/ai/sglang/docker-compose.yml"
  if [ -f "$SGLANG_COMPOSE" ]; then
    sed -i 's|--model-path /models/Qwen3-14B-Instruct|--model-path /models/Qwen3-14B-Instruct-AWQ|' "$SGLANG_COMPOSE" 2>/dev/null
    ok "SGLang model path synced"
  fi

  # Q7-fix: Fix docker-compose-main.yml → docker-compose.yml references in scarlix-mode
  log "Fixing scarlix-mode compose references..."
  sed -i 's|docker-compose-main.yml|docker-compose.yml|g' /usr/local/bin/scarlix-mode 2>/dev/null
  sed -i 's|docker-compose-agent.yml|docker-compose.yml|g' /usr/local/bin/scarlix-mode 2>/dev/null
  ok "scarlix-mode compose refs fixed"

  # Q2a: Configure vLLM for TP=1 (single GPU, GPU1) — NOT TP=2 (different architectures)
  VLLM_COMPOSE="/opt/scarlix/ai/vllm/docker-compose.yml"
  if [ -f "$VLLM_COMPOSE" ]; then
    # Check if GPUs have same compute capability
    if [ -n "$GPU_COMPUTE_CAPS" ] && [ "$NVIDIA_COUNT" -ge 2 ]; then
      CAP1=$(echo "$GPU_COMPUTE_CAPS" | cut -d';' -f1)
      CAP2=$(echo "$GPU_COMPUTE_CAPS" | cut -d';' -f2)
      if [ "$CAP1" = "$CAP2" ]; then
        info "GPUs have same compute_cap ($CAP1) — TP=2 would work (but using TP=1 for stability)"
      else
        warn "GPUs have DIFFERENT compute_cap ($CAP1 vs $CAP2) — TP=2 DISABLED, using TP=1"
      fi
      # Force TP=1 (single GPU) — safer for mixed architectures
      sed -i 's/--tensor-parallel-size 2/--tensor-parallel-size 1/' "$VLLM_COMPOSE" 2>/dev/null
      ok "vLLM set to TP=1 (single GPU, safe for mixed architectures)"
    fi
  fi

  systemctl daemon-reload
  ok "systemd reloaded"

  write_checkpoint phase4
fi

echo ""

# ============================================================================
# PHASE 5: Wizard + start services (verified minimal path)
# ============================================================================
log "=== Phase 5: Wizard + start verified AI path ==="

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
    fail "scarlix-wizard not installed"
  fi

  # Enable services
  log "Enabling SCARLIX services..."
  systemctl enable scarlix-first-boot.service >> "$LOG_FILE" 2>&1 && ok "first-boot service enabled" || fail "first-boot enable"
  [ -f /etc/systemd/system/model-manager.timer ] && systemctl enable model-manager.timer >> "$LOG_FILE" 2>&1 && ok "model-manager.timer" || true

  # Start verified AI path: SGLang (GPU0) + vLLM (GPU1, TP=1) + BeeLlama (CPU)
  log "Starting verified AI path (SGLang GPU0 + vLLM GPU1 + BeeLlama CPU)..."
  if [ "$NVIDIA_COUNT" -ge 1 ]; then
    docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml up -d >> "$LOG_FILE" 2>&1 && ok "SGLang (GPU0, Tier-1)" || fail "SGLang start"
  fi
  if [ "$NVIDIA_COUNT" -ge 2 ]; then
    docker compose -f /opt/scarlix/ai/vllm/docker-compose.yml --profile experimental up -d >> "$LOG_FILE" 2>&1 && ok "vLLM (GPU1, TP=1, Tier-2)" || fail "vLLM start"
  fi
  docker compose -f /opt/scarlix/ai/llamacpp/docker-compose.yml up -d >> "$LOG_FILE" 2>&1 && ok "BeeLlama (CPU, Tier-4)" || fail "BeeLlama start"

  # Browser MCP
  docker compose -f /opt/scarlix/ai/browser-mcp/docker-compose.yml up -d >> "$LOG_FILE" 2>&1 && ok "Browser MCP" || fail "Browser MCP"

  # Default mode
  echo "ai" | tee /var/lib/scarlix/current-mode >/dev/null
  ok "Default mode: ai"

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
[ "$NVIDIA_COUNT" -ge 2 ] && echo -e "${CYAN}║  vLLM:    TP=1 (separate models, mixed arch safe)${NC}"
echo -e "${CYAN}║  ZRAM:    $(zramctl 2>/dev/null | tail -1 || echo 'pending reboot')${NC}"
echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${CYAN}║  Commands:${NC}"
echo -e "${CYAN}║    scarlix-mode status     # System summary${NC}"
echo -e "${CYAN}║    scarlix-mode vram        # VRAM health check${NC}"
echo -e "${CYAN}║    scarlix-mode ai          # Start/verify AI inference${NC}"
echo -e "${CYAN}║    docker ps                # Running containers${NC}"
echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${CYAN}║  Dashboard: http://$(hostname -I 2>/dev/null | awk '{print $1}' || echo 'localhost'):8090${NC}"
echo -e "${CYAN}║  Log:       $LOG_FILE${NC}"
echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Q8: FAIL-HARD — don't mark as installed if critical failure
if [ "$CRITICAL_FAIL" -eq 1 ]; then
  echo -e "${RED}╔══════════════════════════════════════════════════════════════╗${NC}"
  echo -e "${RED}║  CRITICAL FAILURE — system NOT marked as installed.            ║${NC}"
  echo -e "${RED}║  Fix the errors above and re-run: bash install.sh             ║${NC}"
  echo -e "${RED}╚══════════════════════════════════════════════════════════════╝${NC}"
  exit 1
fi

# Mark as installed
touch /opt/scarlix/.installed
echo -e "${GREEN}SCARLIX OS v${VERSION} installed successfully!${NC}"
echo ""
echo "Next steps:"
echo "  1. Reboot to activate NVIDIA driver + ZRAM fully"
echo "  2. Visit http://$(hostname -I 2>/dev/null | awk '{print $1}' || echo 'localhost'):8090"
echo "  3. Run 'scarlix-mode status' to verify"
echo ""

exit 0
