#!/usr/bin/env bash
# shellcheck disable=SC2024  # v19.0.1: script re-execs as root (line ~33); `sudo -u USER cmd >> LOG`
                              #   intentionally redirects as root (root owns install.log). SC2024's
                              #   `| sudo tee` suggestion would write the log as REAL_USER — wrong.
set -euo pipefail

# ============================================================================
# SCARLIX OS v19.1.15 — Bootstrap Installer (Secure Host-Bridge)
# ============================================================================
#
# EndeavourOS/Arch bootstrap installer — NO ISO, runs on clean EndeavourOS.
# 5 phases: packages → NVIDIA → Docker → copy files → wizard + ScarliHQ build.
# Full changelog: see CHANGELOG.md or git log.
#
# USAGE:
#   git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
#   cd ~/scarlix-os
#   git checkout v19.1.15   # ALWAYS checkout specific tag (main may be ahead)
#   bash install.sh
# ============================================================================

VERSION="19.1.15"
LOG_DIR="/var/log/scarlix"
LOG_FILE="$LOG_DIR/install.log"
CHECKPOINT_DIR="/var/lib/scarlix"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$SCRIPT_DIR"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
SUCCESS_COUNT=0; FAIL_COUNT=0; CRITICAL_FAIL=0

# v18.9.6 P0-04: Root check MUST be first operation (was: mkdir/touch/chmod on
#   /var/log before sudo re-exec → Permission denied → set -e → EXIT before sudo)
if [ "$(id -u)" -ne 0 ]; then
  exec sudo -E bash "$0" "$@"
fi

mkdir -p "$LOG_DIR" "$CHECKPOINT_DIR"
# v18.5.2 P1: Create log file with correct perms (was: chmod on non-existent file → no effect)
touch "$LOG_FILE"
chmod 600 "$LOG_FILE"
# v18.2 P1: install.log may contain tokens (SCARLIHQ_TOKEN via info) → chmod 600

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

# v19.0.1 P1-1: ensure_multilib() — enable [multilib] BEFORE any `pacman -Syu`.
#   Was: multilib enabled in Phase 1 AFTER the first `pacman -Syu` → the multilib
#   repo DB was never refreshed by that -Syu → subsequent `pacman -S lib32-*`
#   could fail on a truly clean EndeavourOS where multilib was commented out.
#   Comment in v19.0.0 falsely claimed "db synced by initial -Syu". Now multilib
#   is enabled up-front so the single full -Syu syncs it too. Idempotent.
SYSTEM_SYNCED=false
ensure_multilib() {
  if grep -q '^\[multilib\]' /etc/pacman.conf 2>/dev/null; then
    ok "[multilib] already enabled"
    return 0
  fi
  if grep -Eq '^#\s*\[multilib\]' /etc/pacman.conf 2>/dev/null; then
    # Scoped: uncomment ONLY the [multilib] header + its Include line
    awk '
      /^#[[:space:]]*\[multilib\][[:space:]]*$/ { sub(/^#[[:space:]]*/,""); print; in_ml=1; next }
      in_ml && /^#[[:space:]]*Include[[:space:]]*=/ { sub(/^#[[:space:]]*/,""); print; in_ml=0; next }
      in_ml && /^\[/ { in_ml=0; print; next }
      { print }
    ' /etc/pacman.conf > /tmp/pacman.conf.ml && mv /tmp/pacman.conf.ml /etc/pacman.conf
    if grep -q '^\[multilib\]' /etc/pacman.conf 2>/dev/null; then
      ok "[multilib] enabled (uncommented)"
    else
      crit "[multilib] enable failed (awk) — Steam/Wine/lib32-* will not install"
    fi
  else
    # Append multilib section if not present at all
    printf '\n[multilib]\nInclude = /etc/pacman.d/mirrorlist\n' >> /etc/pacman.conf
    ok "[multilib] added (appended)"
  fi
}

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

# v18.9.6 P0-04: Root check already done at top of script (line 44)
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
# v18.8.6 P0-1: lspci must be present (was: silent failure → NVIDIA_COUNT=0 → driver skipped)
# v18.9.7 P0: Bootstrap pciutils BEFORE lspci check (was: crit if lspci missing → but
#   pciutils was in Phase 1 packages → chicken-and-egg: lspci checked before it could
#   be installed. Now: install pciutils first, then check.)
# v19.0.1 P1-1: enable [multilib] BEFORE the first `pacman -Syu` so the multilib
#   repo DB is synced by that single full upgrade (was: enabled later in Phase 1 →
#   multilib DB never refreshed → lib32-* installs could fail on clean EndeavourOS).
ensure_multilib
if ! command -v lspci >/dev/null 2>&1; then
  log "Bootstrap: full system sync + pciutils install..."
  # v19.0.0 P1-1: Full pacman -Syu BEFORE pciutils install (was: pacman -Sy
  #   pciutils only = partial upgrade → Arch breakage risk on lib mismatch).
  # v19.0.1 P1-1: This is now THE single full -Syu (multilib already enabled
  #   above). SYSTEM_SYNCED is set so Phase 1 skips its redundant second -Syu.
  if ! pacman -Syu --noconfirm >> "$LOG_FILE" 2>&1; then
    log "pacman -Syu failed — attempting DB repair..."
    pacman-key --init >> "$LOG_FILE" 2>&1 || true
    pacman -Syu --noconfirm >> "$LOG_FILE" 2>&1 || crit "pacman DB corrupted — run 'pacman-key --init && pacman -Syu' manually, then re-run install.sh"
  fi
  SYSTEM_SYNCED=true
  pacman -S --noconfirm --needed pciutils >> "$LOG_FILE" 2>&1 || crit "Cannot install pciutils"
fi
command -v lspci >/dev/null 2>&1 || crit "lspci still not found after pciutils install"
# v18.8.6 P0-1: lspci emits "VGA compatible controller: NVIDIA Corporation ..."
#   (NVIDIA comes AFTER controller type). Old regex required NVIDIA before VGA/3D
#   → matched nothing → NVIDIA_COUNT=0 → driver skipped → no GPU.
NVIDIA_GPUS=$(lspci -nn 2>/dev/null | grep -iE '(VGA compatible controller|3D controller|Display controller).*NVIDIA' || true)
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
  # v19.0.1 P1-1: single full -Syu. If the pre-flight pciutils bootstrap already
  #   ran a full -Syu (lspci was missing), skip the redundant second sync here.
  #   If lspci was present (bootstrap skipped), this is THE single full -Syu.
  #   [multilib] was already enabled by ensure_multilib before the bootstrap,
  #   so either path now syncs the multilib DB too.
  if [ "$SYSTEM_SYNCED" = true ]; then
    ok "System already updated (pre-flight sync)"
  else
    pacman -Syu --noconfirm >> "$LOG_FILE" 2>&1 && ok "System updated" || crit "System update failed (fix pacman conflicts, re-run)"
    SYSTEM_SYNCED=true
  fi

  log "Installing SCARLIX packages..."
  # v19.0.1 P1-1: [multilib] was enabled before the first -Syu (see ensure_multilib
  #   at the top of the pre-flight section). Verify it is still active — do NOT
  #   re-enable or re-sync here (would be a redundant partial -Sy risk).
  log "Verifying [multilib] repository is enabled..."
  if grep -q '^\[multilib\]' /etc/pacman.conf 2>/dev/null; then
    ok "[multilib] enabled (db synced by full -Syu)"
  else
    crit "[multilib] not enabled — ensure_multilib should have set it. Steam/Wine/lib32-* will fail"
  fi

  PKGS=$(grep -vE '^\s*#|^\s*$' "$REPO_DIR/packages.x86_64" | grep -v '^yay$' | grep -v '^calamares$' || true)
  # v17.9.8 P1: package install = crit (was fail → checkpoint written despite missing yq/docker/etc.)
  [ -n "$PKGS" ] && pacman -S --noconfirm --needed $PKGS >> "$LOG_FILE" 2>&1 && ok "Packages installed" || crit "Package install failed (check /var/log/scarlix/install.log)"

  mkdir -p /opt/scarlix /var/lib/scarlix /etc/scarlix/{profiles,secrets}
  mkdir -p /models /var/lib/docker /var/lib/scarlix/ollama /mnt/{files,games,photos,backup/restic}
  # v18.5 P0: /var/lib/scarlix must be root:root 755 (was: chown -R REAL_USER → symlink attack on bridge-state/input)
  # v19.1.13 P1-4: Do NOT chown -R /mnt (was: recursive ownership change on existing
  #   mount points — dangerous if /mnt/photos, /mnt/games, /mnt/backup exist).
  # v19.1.15 P2: Chown the specific subdirs created above (was: only /mnt/scarlix,
  #   leaving /mnt/{files,games,photos} as root:root → user can't write).
  chown "$REAL_USER:$REAL_USER" /mnt/files /mnt/games /mnt/photos /mnt/scarlix 2>/dev/null || true
  chown -R "$REAL_USER:$REAL_USER" /mnt/backup/restic 2>/dev/null || true
  # /var/lib/scarlix stays root:root (install.sh creates it with mkdir, which defaults to root)
  # v18.5.2 P1: Security operations must fail-closed (was: || true → continued on failure)
  chown root:root /var/lib/scarlix 2>/dev/null || crit "Cannot chown root:root /var/lib/scarlix"
  chmod 755 /var/lib/scarlix 2>/dev/null || crit "Cannot chmod 755 /var/lib/scarlix"
  # /etc/scarlix stays root:root (user reads models.yaml but can't modify .env)
  # /opt/scarlix stays root:root (user reads scripts but can't modify .env)
  # v18.4 P0: Ensure config dirs are root-owned (security boundary)
  # v18.5.2 P1: Security operations must fail-closed (was: || true → continued on failure)
  chown root:root /etc/scarlix /opt/scarlix 2>/dev/null || crit "Cannot chown root:root /etc/scarlix /opt/scarlix"
  chmod 755 /etc/scarlix /opt/scarlix 2>/dev/null || crit "Cannot chmod 755 /etc/scarlix /opt/scarlix"
  # v18.8.9 P1: fail-closed (was: || true → security inconsistency with v18.5.2 policy)
  chown root:root /var/lib/scarlix/ollama 2>/dev/null || crit "Cannot chown root:root /var/lib/scarlix/ollama"
  chmod 700 /var/lib/scarlix/ollama 2>/dev/null || crit "Cannot chmod 700 /var/lib/scarlix/ollama"
  # P1 v17.9.7: /models chmod 750 (was 775 — tighter; containers read as root via :ro)
  chown -R "$REAL_USER:$REAL_USER" /models 2>/dev/null || crit "Cannot chown /models"
  chmod 750 /models 2>/dev/null || crit "Cannot chmod 750 /models"

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
      # v18.8.6 P0-2: was `pacman ... && ok || { crit ...; pacman ... && ok || crit ... }`
      #   crit() exits 1 → fallback pacman never ran. Now: if/else — crit ONLY if BOTH fail.
      if pacman -S --noconfirm --needed nvidia-open nvidia-open-lts nvidia-utils lib32-nvidia-utils linux-lts linux-lts-headers >> "$LOG_FILE" 2>&1; then
        ok "NVIDIA open (Turing+) + nvidia-open-lts + linux-lts + headers (atomic)"
      else
        warn "NVIDIA open failed — trying proprietary NVIDIA driver (fallback)"
        if pacman -S --noconfirm --needed nvidia nvidia-lts nvidia-utils lib32-nvidia-utils linux-lts linux-lts-headers >> "$LOG_FILE" 2>&1; then
          ok "NVIDIA proprietary (fallback) + linux-lts + headers"
        else
          crit "Both NVIDIA open and proprietary driver installation failed"
        fi
      fi
    else
      pacman -S --noconfirm --needed nvidia nvidia-lts nvidia-utils lib32-nvidia-utils nvidia-settings linux-lts linux-lts-headers >> "$LOG_FILE" 2>&1 && ok "NVIDIA proprietary + linux-lts + headers" || crit "NVIDIA install"
    fi

    log "Installing CUDA + cuDNN..."
    # v18.8.6 P1-1: CUDA install is crit (was: fail → checkpoint written despite CUDA missing
    #   → AI inference broken). If NVIDIA is present, CUDA is required for AI.
    pacman -S --noconfirm --needed cuda cudnn >> "$LOG_FILE" 2>&1 && ok "CUDA + cuDNN" || crit "CUDA + cuDNN installation failed (required for AI inference)"

    pacman -Q linux-lts >/dev/null 2>&1 && info "linux-lts: $(pacman -Q linux-lts)" || crit "linux-lts not installed"

    log "Rebuilding initramfs..."
    mkinitcpio -P >> "$LOG_FILE" 2>&1 && ok "initramfs rebuild" || crit "initramfs rebuild FAILED — system must not be marked installed"

    # GRUB nvidia_drm.modeset=1
    if [ -f /etc/default/grub ] && ! grep -q "nvidia_drm.modeset=1" /etc/default/grub; then
      sed -i 's/GRUB_CMDLINE_LINUX_DEFAULT="\(.*\)"/GRUB_CMDLINE_LINUX_DEFAULT="\1 nvidia_drm.modeset=1"/' /etc/default/grub
      grub-mkconfig -o /boot/grub/grub.cfg >> "$LOG_FILE" 2>&1 && ok "GRUB nvidia_drm.modeset=1" || crit "GRUB configuration FAILED — system may not boot correctly"
    else
      ok "GRUB config (already set)"
    fi

    # v19.0.0 P1-12: Detect if reboot is required (new kernel/driver installed)
    #   (was: no detection → user might run AI workloads on old kernel without
    #   the just-installed NVIDIA driver loaded → confusing failures. Now: touch
    #   /var/lib/scarlix/.reboot-required so scarlix-doctor + user can be warned.)
    RUNNING_KERNEL=$(uname -r)
    INSTALLED_KERNEL=$(pacman -Q linux 2>/dev/null | awk '{print $2}' | cut -d- -f1)
    if [ -n "$INSTALLED_KERNEL" ] && ! echo "$RUNNING_KERNEL" | grep -q "$INSTALLED_KERNEL"; then
      warn "⚠ REBOOT REQUIRED: running kernel $RUNNING_KERNEL, installed kernel $INSTALLED_KERNEL"
      touch /var/lib/scarlix/.reboot-required
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
    # v18.8.6 P1-3: Use mktemp -d for AUR build dir (was: /tmp/yay-build predictable path
    #   → symlink-race / pre-populated dir could inject modified PKGBUILD).
    YAY_BUILD_DIR=$(mktemp -d) || crit "Cannot create temp dir for AUR build"
    if sudo -u "$REAL_USER" git clone https://aur.archlinux.org/yay.git "$YAY_BUILD_DIR" >> "$LOG_FILE" 2>&1; then
      # v19.0.0 P1-4: After makepkg failure, check if yay is actually available
      #   (was: plain `fail` → installer continued but later nvidia-container-toolkit
      #   AUR fallback would silently fail. Now: distinguish yay-exists vs yay-missing.)
      sudo -u "$REAL_USER" bash -c "cd $YAY_BUILD_DIR && makepkg -si --noconfirm" >> "$LOG_FILE" 2>&1 && ok "yay installed" || {
        if command -v yay >/dev/null 2>&1; then
          ok "yay already available (makepkg may have failed but yay exists)"
        else
          warn "yay build failed — AUR packages (downgrade) will not be available"
        fi
      }
    else
      fail "yay git clone"
    fi
    rm -rf "$YAY_BUILD_DIR"
  fi

  # Q5a: nvidia-container-toolkit fail → CRIT (before Docker start)
  if [ "$NVIDIA_COUNT" -gt 0 ]; then
    log "Installing nvidia-container-toolkit (CRITICAL — before Docker start)..."
    # v18.9.6 P1-01: Use pacman first (was: yay only — AUR dependency for a package
    #   that is now in Arch Extra. Try pacman, fall back to yay only if needed.)
    if pacman -S --noconfirm --needed nvidia-container-toolkit >> "$LOG_FILE" 2>&1; then
      ok "nvidia-container-toolkit (pacman)"
    elif sudo -u "$REAL_USER" yay -S --noconfirm nvidia-container-toolkit >> "$LOG_FILE" 2>&1; then
      ok "nvidia-container-toolkit (AUR fallback)"
    else
      crit "nvidia-container-toolkit — Docker GPU won't work without it"
    fi
    nvidia-ctk runtime configure --runtime=docker >> "$LOG_FILE" 2>&1 && ok "Docker NVIDIA runtime configured" || crit "Docker NVIDIA runtime"

    log "Installing downgrade (AUR)..."
    sudo -u "$REAL_USER" yay -S --noconfirm downgrade >> "$LOG_FILE" 2>&1 && ok "downgrade" || fail "downgrade (non-critical)"
  fi

  # Q4a v17.8: NOW start Docker (after nvidia-container-toolkit configured)
  log "Starting Docker (after toolkit configured)..."
  systemctl start docker >> "$LOG_FILE" 2>&1 && ok "Docker started (with GPU runtime)" || crit "Docker start"
  systemctl enable docker >> "$LOG_FILE" 2>&1 && ok "Docker enabled on boot" || crit "Docker could not be enabled — AI stack will not survive reboot"
  # v17.8: Restart Docker to ensure nvidia-ctk runtime is loaded
  if [ "$NVIDIA_COUNT" -gt 0 ]; then
    log "Restarting Docker to apply NVIDIA runtime..."
    # v18.8.6 P1-2: Docker restart fail-closed (was: || true → GPU runtime not active → SGLang/vLLM fail later)
    if ! systemctl restart docker >> "$LOG_FILE" 2>&1; then
      log "Docker restart failed — retrying in 3s..."
      sleep 3
      systemctl restart docker >> "$LOG_FILE" 2>&1 || crit "Docker restart failed after NVIDIA runtime configuration"
    fi
    sleep 2  # Wait for Docker socket to be ready
    # Verify GPU visibility in Docker
    if docker info 2>/dev/null | grep -qi "Runtimes.*nvidia"; then
      ok "Docker NVIDIA runtime verified"
    else
      # v18.9.5 P1-04: crit not warn (was: warn → install continues → GPU workloads fail later)
      crit "Docker NVIDIA runtime unavailable — GPU workloads cannot start (try reboot after install)"
    fi

    # v18.9.7 P0-02 + v19.0.0 P1-2: Hard Docker GPU smoke test (was: only checked
    #   `docker info | grep nvidia` → runtime registered but GPU not necessarily
    #   accessible. Now: actual `nvidia-smi` inside container proves end-to-end
    #   GPU passthrough works. v19.0.0 P1-2: Add retry + fallback to host
    #   nvidia-smi so install does not fail purely on Docker Hub being unreachable.)
    log "Running Docker GPU smoke test (nvidia-smi in container)..."
    gpu_test_rc=0
    docker run --rm --gpus all nvidia/cuda:12.8.1-base-ubuntu24.04 nvidia-smi >> "$LOG_FILE" 2>&1 || gpu_test_rc=$?
    if [ "$gpu_test_rc" -eq 0 ]; then
      ok "Docker GPU smoke test PASSED"
    elif docker run --rm --gpus all nvidia/cuda:12.8.1-base-ubuntu24.04 nvidia-smi >> "$LOG_FILE" 2>&1; then
      ok "Docker GPU smoke test PASSED (retry 2)"
    else
      # v19.0.0 P1-2: Distinguish GPU failure from network/image failure
      if docker info 2>/dev/null | grep -qi "Runtimes.*nvidia" && nvidia-smi >/dev/null 2>&1; then
        warn "Docker GPU smoke test could not pull CUDA image (network issue?), but NVIDIA runtime + nvidia-smi work — proceeding (test after install with: docker run --gpus all nvidia/cuda:12.8.1-base-ubuntu24.04 nvidia-smi)"
      else
        crit "Docker GPU smoke test FAILED — nvidia-smi does not work in container (GPU passthrough broken)"
      fi
    fi

    # v18.9.7 P1-03: Save GPU topology for diagnostics (was: no record of GPU layout →
    #   debugging SGLang/vLLM device assignment was guesswork. Now: saved to
    #   /var/lib/scarlix/gpu-layout for scarlix-doctor + future use.)
    nvidia-smi --query-gpu=index,name,pci.bus_id,compute_cap,memory.total --format=csv,noheader > /var/lib/scarlix/gpu-layout 2>/dev/null
    if [ -s /var/lib/scarlix/gpu-layout ]; then
      log "GPU topology saved to /var/lib/scarlix/gpu-layout"
    else
      warn "Failed to save GPU topology (non-critical, but scarlix-doctor diagnostics will be limited)"
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
  # v18.9.5 P0-03: Proper Docker network check (was: || info "exists" → any failure = "exists")
  if docker network inspect scarlix-net >/dev/null 2>&1; then
    ok "scarlix-net already exists"
  elif docker network create scarlix-net >/dev/null 2>&1; then
    ok "scarlix-net created"
  else
    crit "Cannot create required Docker network scarlix-net"
  fi

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
  # v18.7.4 P1: Build scarlix-bridge-reader (host-side atomic Go file reader).
  # Used by /usr/local/bin/scarlix-host-bridge to atomically read desired-mode
  # (was: shell validate+cat+re-validate pattern with TOCTOU window).
  # v18.7.5 P0: build is now CRITICAL — host-bridge no longer has a shell fallback
  # (the shell fallback had the TOCTOU window, so it was removed for security).
  # If the build fails here, crit() aborts install — the host-bridge script will
  # reject every desired-mode until scarlix-bridge-reader is installed.
  log "Building scarlix-bridge-reader (atomic Go file reader)..."
  # v18.9.8 P0-3: Pre-flight toolchain check (was: build failed silently if
  #   Go missing AND Docker missing → crit with unclear error. Now: check
  #   upfront and install Go via Docker if needed.)
  if ! command -v go >/dev/null 2>&1 && ! command -v docker >/dev/null 2>&1; then
    crit "Neither Go nor Docker available — cannot build scarlix-bridge-reader (install Docker first or install go)"
  fi
  mkdir -p "$REPO_DIR/files/usr/local/bin"
  SBR_SRC="$REPO_DIR/scarlihq/cmd/scarlix-bridge-reader/main.go"
  SBR_OUT="$REPO_DIR/files/usr/local/bin/scarlix-bridge-reader"
  if [ -f "$SBR_SRC" ]; then
    if command -v go >/dev/null 2>&1; then
      # Native Go build (host has Go installed — e.g. dev workstation).
      if (cd "$REPO_DIR/scarlihq" && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -o "$SBR_OUT" ./cmd/scarlix-bridge-reader) >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SBR_OUT"
        ok "scarlix-bridge-reader built (native Go)"
      else
        # v18.7.5 P0: was warn (shell fallback existed) — now crit (no fallback).
        crit "scarlix-bridge-reader native build failed — required for secure host-bridge operation"
      fi
    elif command -v docker >/dev/null 2>&1; then
      # Build via golang:1.23-alpine container (host has no Go but has Docker).
      # v18.7.5 P1: added second volume mount for the output path (was:
      #   -o /build/files/usr/local/bin/scarlix-bridge-reader — but /build is
      #   mounted to $REPO_DIR/scarlihq, so the output landed at
      #   $REPO_DIR/scarlihq/files/usr/local/bin/scarlix-bridge-reader, not
      #   $REPO_DIR/files/usr/local/bin/scarlix-bridge-reader → SBR_OUT missing
      #   → later crit 'scarlix-bridge-reader not built' even though go build
      #   exited 0). Now mount $REPO_DIR/files/usr/local/bin → /out and write
      #   to /out/scarlix-bridge-reader.
      if docker run --rm \
          -v "$REPO_DIR/scarlihq:/build" \
          -v "$REPO_DIR/files/usr/local/bin:/out" \
          -w /build \
          -e GOFLAGS=-mod=mod \
          golang:1.23-alpine \
          go build -o /out/scarlix-bridge-reader ./cmd/scarlix-bridge-reader \
          >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SBR_OUT"
        ok "scarlix-bridge-reader built (via golang:1.23-alpine container)"
      else
        # v18.7.5 P0: was warn (shell fallback existed) — now crit (no fallback).
        crit "scarlix-bridge-reader container build failed — required for secure host-bridge operation"
      fi
    else
      # v18.7.5 P0: was warn (shell fallback existed) — now crit (no fallback).
      crit "Neither Go nor Docker available — scarlix-bridge-reader not built (required for secure host-bridge operation)"
    fi
  else
    # v18.7.5 P0: was warn (shell fallback existed) — now crit (no fallback).
    crit "scarlix-bridge-reader source not found ($SBR_SRC) — required for secure host-bridge operation"
  fi
  # v18.7.5 P0: bridge-reader is CRITICAL (was: warn → shell TOCTOU fallback).
  # Defense-in-depth: even if a future edit re-introduces a warn() above, this
  # final check catches a missing binary before the copy loop below.
  if [ ! -f "$SBR_OUT" ]; then
    crit "scarlix-bridge-reader not built — required for secure host-bridge operation"
  fi

  # v19.0.8: Build scarlix-gpu (normalized GPU telemetry collector, Go binary).
  #   Used by `scarlix gpu status --json` for data-contract-compliant JSON output.
  #   Non-critical (warn on failure — the bash CLI falls back to nvidia-smi directly).
  SGPU_SRC="$REPO_DIR/scarlihq/cmd/scarlix-gpu/main.go"
  SGPU_OUT="$REPO_DIR/files/usr/local/bin/scarlix-gpu"
  if [ -f "$SGPU_SRC" ]; then
    if command -v go >/dev/null 2>&1; then
      if (cd "$REPO_DIR/scarlihq" && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -o "$SGPU_OUT" ./cmd/scarlix-gpu) >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SGPU_OUT"
        ok "scarlix-gpu built (native Go)"
      else
        warn "scarlix-gpu build failed — scarlix gpu --json will fall back to nvidia-smi"
      fi
    elif command -v docker >/dev/null 2>&1; then
      if docker run --rm \
          -v "$REPO_DIR/scarlihq:/build" \
          -v "$REPO_DIR/files/usr/local/bin:/out" \
          -w /build \
          -e GOFLAGS=-mod=mod \
          golang:1.23-alpine \
          go build -o /out/scarlix-gpu ./cmd/scarlix-gpu \
          >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SGPU_OUT"
        ok "scarlix-gpu built (via golang:1.23-alpine container)"
      else
        warn "scarlix-gpu container build failed — scarlix gpu --json will fall back to nvidia-smi"
      fi
    else
      warn "Neither Go nor Docker available — scarlix-gpu not built (non-critical, scarlix gpu --json falls back)"
    fi
  else
    warn "scarlix-gpu source not found ($SGPU_SRC) — non-critical"
  fi

  # v19.0.9: Build scarlix-inventory (full system inventory: GPUs + runtimes + models + health).
  #   Used by `scarlix runtime/model list --json` + `scarlix-inventory` standalone binary.
  #   Non-critical (warn on failure — bash CLI falls back to docker/yq direct).
  SINV_SRC="$REPO_DIR/scarlihq/cmd/scarlix-inventory/main.go"
  SINV_OUT="$REPO_DIR/files/usr/local/bin/scarlix-inventory"
  if [ -f "$SINV_SRC" ]; then
    if command -v go >/dev/null 2>&1; then
      if (cd "$REPO_DIR/scarlihq" && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -o "$SINV_OUT" ./cmd/scarlix-inventory) >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SINV_OUT"
        ok "scarlix-inventory built (native Go)"
      else
        warn "scarlix-inventory build failed — scarlix runtime/model --json will fall back"
      fi
    elif command -v docker >/dev/null 2>&1; then
      if docker run --rm \
          -v "$REPO_DIR/scarlihq:/build" \
          -v "$REPO_DIR/files/usr/local/bin:/out" \
          -w /build \
          -e GOFLAGS=-mod=mod \
          golang:1.23-alpine \
          go build -o /out/scarlix-inventory ./cmd/scarlix-inventory \
          >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SINV_OUT"
        ok "scarlix-inventory built (via golang:1.23-alpine container)"
      else
        warn "scarlix-inventory container build failed — scarlix runtime/model --json will fall back"
      fi
    else
      warn "Neither Go nor Docker available — scarlix-inventory not built (non-critical)"
    fi
  else
    warn "scarlix-inventory source not found ($SINV_SRC) — non-critical"
  fi

  # v19.1.0: Build scarlix-contract (Resource Contract v1 parser/validator CLI).
  #   Used by agents to validate compute resource requests before submission.
  #   Non-critical (warn on failure — agents can validate via other means).
  SCON_SRC="$REPO_DIR/scarlihq/cmd/scarlix-contract/main.go"
  SCON_OUT="$REPO_DIR/files/usr/local/bin/scarlix-contract"
  if [ -f "$SCON_SRC" ]; then
    if command -v go >/dev/null 2>&1; then
      if (cd "$REPO_DIR/scarlihq" && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -o "$SCON_OUT" ./cmd/scarlix-contract) >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SCON_OUT"
        ok "scarlix-contract built (native Go)"
      else
        warn "scarlix-contract build failed — non-critical"
      fi
    elif command -v docker >/dev/null 2>&1; then
      if docker run --rm -v "$REPO_DIR/scarlihq:/build" -v "$REPO_DIR/files/usr/local/bin:/out" -w /build -e GOFLAGS=-mod=mod golang:1.23-alpine go build -o /out/scarlix-contract ./cmd/scarlix-contract >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SCON_OUT"
        ok "scarlix-contract built (via golang:1.23-alpine container)"
      else
        warn "scarlix-contract container build failed — non-critical"
      fi
    else
      warn "Neither Go nor Docker available — scarlix-contract not built (non-critical)"
    fi
  else
    warn "scarlix-contract source not found ($SCON_SRC) — non-critical"
  fi

  # v19.1.4: Build scarlix-monitor (read-only system snapshot CLI — ScarliMonitor Foundation).
  #   Composes inventory collectors + /proc reads (CPU/RAM/storage) into a single
  #   point-in-time JSON snapshot. Non-critical (warn on failure — monitoring is
  #   observational, not required for system operation).
  SMON_SRC="$REPO_DIR/scarlihq/cmd/scarlix-monitor/main.go"
  SMON_OUT="$REPO_DIR/files/usr/local/bin/scarlix-monitor"
  if [ -f "$SMON_SRC" ]; then
    if command -v go >/dev/null 2>&1; then
      if (cd "$REPO_DIR/scarlihq" && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -o "$SMON_OUT" ./cmd/scarlix-monitor) >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SMON_OUT"
        ok "scarlix-monitor built (native Go)"
      else
        warn "scarlix-monitor build failed — non-critical"
      fi
    elif command -v docker >/dev/null 2>&1; then
      if docker run --rm \
          -v "$REPO_DIR/scarlihq:/build" \
          -v "$REPO_DIR/files/usr/local/bin:/out" \
          -w /build \
          -e GOFLAGS=-mod=mod \
          golang:1.23-alpine \
          go build -o /out/scarlix-monitor ./cmd/scarlix-monitor \
          >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SMON_OUT"
        ok "scarlix-monitor built (via golang:1.23-alpine container)"
      else
        warn "scarlix-monitor container build failed — non-critical"
      fi
    else
      warn "Neither Go nor Docker available — scarlix-monitor not built (non-critical)"
    fi
  else
    warn "scarlix-monitor source not found ($SMON_SRC) — non-critical"
  fi

  # v19.1.7: Build scarlix-scheduler (dry-run scheduler CLI: scarlix compute plan).
  #   Deterministic scoring — no actual allocation. Non-critical.
  SSCH_SRC="$REPO_DIR/scarlihq/cmd/scarlix-scheduler/main.go"
  SSCH_OUT="$REPO_DIR/files/usr/local/bin/scarlix-scheduler"
  if [ -f "$SSCH_SRC" ]; then
    if command -v go >/dev/null 2>&1; then
      if (cd "$REPO_DIR/scarlihq" && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -o "$SSCH_OUT" ./cmd/scarlix-scheduler) >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SSCH_OUT"
        ok "scarlix-scheduler built (native Go)"
      else
        warn "scarlix-scheduler build failed — non-critical"
      fi
    elif command -v docker >/dev/null 2>&1; then
      if docker run --rm -v "$REPO_DIR/scarlihq:/build" -v "$REPO_DIR/files/usr/local/bin:/out" -w /build -e GOFLAGS=-mod=mod golang:1.23-alpine go build -o /out/scarlix-scheduler ./cmd/scarlix-scheduler >> "$LOG_FILE" 2>&1; then
        chmod 755 "$SSCH_OUT"
        ok "scarlix-scheduler built (via golang:1.23-alpine container)"
      else
        warn "scarlix-scheduler container build failed — non-critical"
      fi
    else
      warn "Neither Go nor Docker available — scarlix-scheduler not built (non-critical)"
    fi
  else
    warn "scarlix-scheduler source not found ($SSCH_SRC) — non-critical"
  fi

  # Q10a: ALL file copy failures are crit (scarlix-mode, scarlix-wizard = core)
  # v17.9.8: added scarlix-host-bridge (privileged ops for ScarliHQ dashboard)
  # v18.7.4 P2: added generate-sha256sums.sh (release integrity manifest generator)
  # v18.7.4 P1: added scarlix-bridge-reader (atomic Go file reader — built above).
  # v18.7.5 P0: scarlix-bridge-reader is now CRITICAL (was: warn — shell fallback
  #   existed). The build block above crit()s on any failure, so reaching this
  #   elif branch means the build was skipped/aborted — crit here too for safety.
  log "Installing SCARLIX scripts (CRITICAL)..."
  # v18.8.3 P1 (A-a): Added generate-litellm-config.sh (dynamic LiteLLM config from models.yaml)
  for binfile in scarlix-wizard scarlix-mode model-manager.sh download-models.sh scarlix-doctor scarlix-host-bridge generate-sha256sums.sh generate-litellm-config.sh scarlix-bridge-reader scarlix-gpu scarlix-inventory scarlix-contract scarlix-monitor scarlix-scheduler scarlix scarlix-smoke-test.sh; do
    src="$REPO_DIR/files/usr/local/bin/$binfile"
    if [ -f "$src" ]; then
      # v19.1.13 P1: Fix copy loop regression. Was: `cp && chmod && ok || warn && continue`
      #   which made ALL copy failures (including core: scarlix-mode, scarlix-doctor)
      #   non-fatal. Now: proper if/else with crit for core, warn for optional.
      if cp "$src" "/usr/local/bin/$binfile" && chmod 755 "/usr/local/bin/$binfile"; then
        ok "/usr/local/bin/$binfile"
      else
        case "$binfile" in
          scarlix-gpu|scarlix-inventory|scarlix-contract|scarlix-monitor|scarlix-scheduler)
            warn "$binfile copy failed (non-critical)"
            ;;
          *)
            crit "$binfile copy failed"
            ;;
        esac
      fi
    elif [ "$binfile" = "scarlix-bridge-reader" ]; then
      # v18.7.5 P0: was warn (shell fallback existed) — now crit (no fallback).
      crit "scarlix-bridge-reader not built — required for secure host-bridge operation"
    else
      # v19.1.11 P1-8: Non-critical Go binaries (scarlix-gpu, -inventory, -contract,
      # -monitor, -scheduler) may not exist if Go/Docker build failed. Was: crit (abort
      # install). Now: warn + skip — these are optional enhancements, not core.
      case "$binfile" in
        scarlix-gpu|scarlix-inventory|scarlix-contract|scarlix-monitor|scarlix-scheduler)
          warn "$binfile not built — non-critical (Go/Docker build skipped)"
          ;;
        *)
          crit "$binfile not found in repo"
          ;;
      esac
    fi
  done

  # Systemd services (model-manager + host-bridge — first-boot removed Q6a)
  log "Installing systemd services..."
  for f in model-manager.service model-manager.timer generate-env.sh scarlix-host-bridge.service scarlix-host-bridge.timer scarlix-tv-mode.service; do
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
    cp "$REPO_DIR/VERSION" /etc/scarlix/VERSION 2>/dev/null || crit "Cannot copy VERSION file to /etc/scarlix/"
    mkdir -p /usr/local/share/scarlix
    cp "$REPO_DIR/VERSION" /usr/local/share/scarlix/VERSION 2>/dev/null || crit "Cannot copy VERSION file to /usr/local/share/scarlix/"
    [ -s /etc/scarlix/VERSION ] || crit "VERSION file is empty or missing"
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
        if cp -a "$REPO_DIR/$dir/." "/opt/scarlix/$dir/" 2>/dev/null; then
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
    # v18.8.9 P1: fail-closed (was: || true → LPE if .env stays user-owned)
    chown root:root /opt/scarlix/.env 2>/dev/null || crit "Cannot chown root:root /opt/scarlix/.env"
    chmod 600 /opt/scarlix/.env 2>/dev/null || crit "Cannot chmod 600 /opt/scarlix/.env"
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
    # v19.0.0 P1-6: Check TTY before running wizard (was: ran unconditionally →
    #   in non-interactive SSH without TTY, whiptail could hang or behave
    #   unpredictably. Now: skip + leave .wizard-pending marker so user is reminded.)
    if [ -t 0 ] && [ -t 1 ]; then
      sudo -u "$REAL_USER" scarlix-wizard || warn "Wizard exited (user may have cancelled)"
    else
      warn "No TTY detected — skipping wizard. Run 'sudo scarlix-wizard' after install to configure."
      touch /var/lib/scarlix/.wizard-pending
    fi
  else
    crit "scarlix-wizard not installed"
  fi

  # v19.0.10: Install Pi-Bolt (coding agent — replaces OpenCode).
  #   Per migration guide: install as NON-ROOT user via official installer.
  #   Non-critical (warn on failure — Pi-Bolt can be installed manually later).
  #   The installer places Pi-Bolt under ~/.pi-bolt and links ~/.local/bin/pi-bolt.
  log "Installing Pi-Bolt (coding agent)..."
  if sudo -u "$REAL_USER" bash -c 'curl -fsSL https://pi-bolt.opensec.in/install.sh | sh' >> "$LOG_FILE" 2>&1; then
    ok "Pi-Bolt installed (as $REAL_USER)"
    # Verify the binary is available
    if sudo -u "$REAL_USER" bash -c 'command -v pi-bolt >/dev/null 2>&1' || [ -f "/home/$REAL_USER/.local/bin/pi-bolt" ]; then
      ok "pi-bolt binary available"
    else
      warn "Pi-Bolt installed but binary not on PATH — user may need to restart shell or add ~/.local/bin to PATH"
    fi
    # v19.1.11 P1-6 FIX: Pi-Bolt needs LITELLM_MASTER_KEY + SCARLIHQ_TOKEN env vars.
    # /etc/scarlix/.env is root:root 600 — user CANNOT read it.
    # Fix: create a user-readable secrets file (~/.config/scarlix/agent.env, 600, user-owned)
    # and source it from .bashrc. Never write the key directly into .bashrc.
    USER_SECRETS_DIR="/home/$REAL_USER/.config/scarlix"
    USER_SECRETS_FILE="$USER_SECRETS_DIR/agent.env"
    USER_PROFILE="/home/$REAL_USER/.bashrc"
    if [ -f /etc/scarlix/.env ]; then
      # Extract keys from root-owned .env (install.sh runs as root)
      LITELLM_KEY=$(grep "^LITELLM_MASTER_KEY=" /etc/scarlix/.env 2>/dev/null | cut -d= -f2- || echo "")
      SCARLIHQ_KEY=$(grep "^SCARLIHQ_TOKEN=" /etc/scarlix/.env 2>/dev/null | cut -d= -f2- || echo "")
      if [ -n "$LITELLM_KEY" ] || [ -n "$SCARLIHQ_KEY" ]; then
        # v19.1.15 P1-2: Fix agent.env permissions. Was: umask 077 only on mkdir
        #   (first bash -c), then cat in separate bash -c with default umask → file
        #   ended up as 644, not 600. Also ok() was unconditional.
        #   Now: single bash -c with umask 077 wrapping BOTH mkdir + cat + chmod.
        #   Write keys via heredoc to avoid nested quote escaping.
        if sudo -u "$REAL_USER" bash -c 'umask 077; mkdir -p "$HOME/.config/scarlix" && cat > "$HOME/.config/scarlix/agent.env" && chmod 600 "$HOME/.config/scarlix/agent.env"' << AGENTEOF
# SCARLIX OS agent secrets (v19.1.15) — user-readable, NOT committed to git.
# Generated by install.sh from /etc/scarlix/.env (root:root 600).
# Pi-Bolt coding agent reads LITELLM_MASTER_KEY + SCARLIHQ_TOKEN from here.
export LITELLM_MASTER_KEY="$LITELLM_KEY"
export SCARLIHQ_TOKEN="$SCARLIHQ_KEY"
AGENTEOF
        then
          ok "Agent secrets written to ~/.config/scarlix/agent.env (600, user-owned)"
        else
          warn "Failed to write agent.env — Pi-Bolt may not have API keys"
        fi
        # Add source line to .bashrc (idempotent)
        if [ -f "$USER_PROFILE" ] && ! grep -q "scarlix/agent.env" "$USER_PROFILE" 2>/dev/null; then
          echo "" >> "$USER_PROFILE"
          echo "# SCARLIX OS agent secrets for Pi-Bolt (v19.1.15)" >> "$USER_PROFILE"
          echo '[ -f "$HOME/.config/scarlix/agent.env" ] && source "$HOME/.config/scarlix/agent.env"' >> "$USER_PROFILE"
          chown "$REAL_USER:$REAL_USER" "$USER_PROFILE"
        fi
      else
        warn "Could not extract LITELLM_MASTER_KEY/SCARLIHQ_TOKEN from /etc/scarlix/.env"
      fi
    fi
  else
    warn "Pi-Bolt installer failed — user can install manually: curl -fsSL https://pi-bolt.opensec.in/install.sh | sh"
  fi

  # Enable model-manager timer (weekly HF auto-pull)
  if [ -f /etc/systemd/system/model-manager.timer ]; then
    systemctl enable model-manager.timer >> "$LOG_FILE" 2>&1 && ok "model-manager.timer enabled" || fail "model-manager.timer"
  fi

  # v17.9.8: Enable host-bridge timer (writes host-status.json every 5s for ScarliHQ)
  if [ -f /etc/systemd/system/scarlix-host-bridge.timer ]; then
    systemctl enable --now scarlix-host-bridge.timer >> "$LOG_FILE" 2>&1 && ok "scarlix-host-bridge.timer enabled + started" || crit "scarlix-host-bridge.timer failed — dashboard will not receive updates"
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
    # v18.5.2 P1: Security operations must fail-closed (was: || true → continued on failure)
    chown root:root /var/lib/scarlix 2>/dev/null || crit "Cannot chown root:root /var/lib/scarlix"
    chmod 755 /var/lib/scarlix 2>/dev/null || crit "Cannot chmod 755 /var/lib/scarlix"
    # bridge-input: ScarliHQ nonroot (65532) writes desired-mode here (mode 700 — owner only)
    mkdir -p /var/lib/scarlix/bridge-input
    # v18.5.2 P2: fail-closed on chown (was: `|| chown nobody:nobody` fallback → wrong UID,
    # ScarliHQ nonroot 65532 could not write desired-mode → silent mode-switch failure)
    chown 65532:65532 /var/lib/scarlix/bridge-input 2>/dev/null || crit "Cannot chown bridge-input to UID 65532"
    chmod 700 /var/lib/scarlix/bridge-input
    # bridge-state: host bridge (root) writes retry + last-transition here (NOT writable by ScarliHQ)
    mkdir -p /var/lib/scarlix/bridge-state
    # v18.5.2 P1: Security operations must fail-closed (was: no error message on failure)
    chown root:root /var/lib/scarlix/bridge-state 2>/dev/null || crit "Cannot chown root:root /var/lib/scarlix/bridge-state"
    chmod 700 /var/lib/scarlix/bridge-state 2>/dev/null || crit "Cannot chmod 700 /var/lib/scarlix/bridge-state"
    ok "bridge-input/ (uid 65532, 700) + bridge-state/ (root, 700) — symlink attack prevented"
  fi

  # v17.9.8: Generate /etc/scarlix/.env (secrets + SCARLIHQ_TOKEN) if not already
  # v18.8.2 P0: Run generate-env.sh ALWAYS (was: only if .env didn't exist → upgrade
  #   runs skipped it → new keys like LITELLM_MASTER_KEY + SCARLIX_VERSION never got
  #   added to existing .env. generate-env.sh is idempotent: `grep -q || echo` only
  #   adds missing keys, never overwrites existing secrets.)
  if [ -f /etc/systemd/system/generate-env.sh ]; then
    log "Ensuring /etc/scarlix/.env has all keys (idempotent)..."
    /etc/systemd/system/generate-env.sh >> "$LOG_FILE" 2>&1 && ok ".env keys ensured" || crit ".env generation failed — required secrets were not created"
  fi
  # v18.8.3 P1 (A-a): Generate LiteLLM config from models.yaml (was: hardcoded
  #   config.yaml → model ID mismatch when user changed model. Now: dynamic.)
  if [ -f /usr/local/bin/generate-litellm-config.sh ] && [ -f /etc/scarlix/models.yaml ]; then
    log "Generating LiteLLM config from models.yaml..."
    /usr/local/bin/generate-litellm-config.sh /etc/scarlix/models.yaml /opt/scarlix/ai/litellm/config.yaml >> "$LOG_FILE" 2>&1 && ok "LiteLLM config generated (matches models.yaml)" || warn "LiteLLM config generation (non-critical)"
  fi
  # v18.4 P0: /etc/scarlix/.env MUST be root:root 600 (contains secrets, root services source it)
  # v18.5.3 P0: fail-closed (was: || true → security inconsistency with commit claim)
  if [ -f /etc/scarlix/.env ]; then
    chown root:root /etc/scarlix/.env 2>/dev/null || crit "Cannot chown root:root /etc/scarlix/.env"
    chmod 600 /etc/scarlix/.env 2>/dev/null || crit "Cannot chmod 600 /etc/scarlix/.env"
    # v18.5.3 P0: verify ownership + perms after setting
    env_owner=$(stat -c '%u:%g' /etc/scarlix/.env 2>/dev/null || echo "?")
    env_mode=$(stat -c '%a' /etc/scarlix/.env 2>/dev/null || echo "?")
    [ "$env_owner" = "0:0" ] || crit "/etc/scarlix/.env owner is $env_owner, expected 0:0"
    [ "$env_mode" = "600" ] || crit "/etc/scarlix/.env mode is $env_mode, expected 600"
    ok "/etc/scarlix/.env verified (root:root 600)"
  fi

  # P1 v17.9.8: Build + start ScarliHQ dashboard (alpine image — NO nvidia needed, builds pre-reboot)
  # Architecture: ScarliHQ reads host-status.json (written by host-bridge timer), writes desired-mode.
  # No docker.sock, no scarlix-mode mount, no nvidia runtime → minimal privilege.
  if [ -f /opt/scarlix/scarlihq/Dockerfile ]; then
    # v18.7.6 P2: Versioned image tag (was: scarlihq:latest). Compose now references
    # scarlihq:v$VERSION — the literal tag here MUST match the image: line in
    # scarlihq/docker-compose.yml, otherwise `docker compose up` re-builds as latest.
    # v18.7.7 P2: Use $VERSION variable (was: hardcoded v18.7.6 → drifted on every release).
    log "Building ScarliHQ dashboard image (scarlihq:v$VERSION, alpine)..."
    # v18.4 P0: SAFE parse SCARLIHQ_TOKEN from /etc/scarlix/.env (was: `source` → LPE on re-run)
    if [ -f /etc/scarlix/.env ]; then
      SCARLIHQ_TOKEN=$(grep '^SCARLIHQ_TOKEN=' /etc/scarlix/.env 2>/dev/null | cut -d= -f2 || echo "")
    fi
    # Export for compose (compose reads ${SCARLIHQ_TOKEN} from environment)
    export SCARLIHQ_TOKEN="${SCARLIHQ_TOKEN:-}"
    # v17.9.9 P2: pass VERSION as build-arg (Dockerfile injects via -ldflags -X main.Version)
    # v18.7.7 P2: image tag matches VERSION variable (was: hardcoded v18.7.6 —
    #   drifted from VERSION on every release). Now uses $VERSION so the build
    #   tag, compose image tag, and VERSION file all agree.
    if docker build --build-arg SCARLIX_VERSION="$VERSION" -t scarlihq:v$VERSION /opt/scarlix/scarlihq/ >> "$LOG_FILE" 2>&1; then
      ok "ScarliHQ image built (scarlihq:v$VERSION, alpine ~20MB, version $VERSION)"
      # v18.9.8 P0-2 + P1-7: Validate compose config BEFORE up (was: config not checked
      #   → missing env vars could cause silent container failure. Now: docker compose
      #   config --quiet with same --env-file as up → catches interpolation errors.)
      if ! docker compose --env-file /etc/scarlix/.env -f /opt/scarlix/scarlihq/docker-compose.yml config --quiet >> "$LOG_FILE" 2>&1; then
        crit "ScarliHQ compose config validation failed — check /etc/scarlix/.env for required vars"
      fi
      if docker compose --env-file /etc/scarlix/.env -f /opt/scarlix/scarlihq/docker-compose.yml up -d >> "$LOG_FILE" 2>&1; then
        # v18.7.8 P0: Post-start health check (was: `compose up -d` exit 0 →
        #   "dashboard started" — but container could start then immediately
        #   panic/exit. Compose returns 0 even if the container exits 1 second
        #   later. Now: poll /api/health for up to 30s; if no HTTP response,
        #   the dashboard is NOT actually running → set FAILED flag + dump logs).
        DASHBOARD_OK=0
        for _ in $(seq 1 30); do
          # Any HTTP response (even 401) = server is up. 000 = connection refused.
          code=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 2 --max-time 5 http://127.0.0.1:8090/api/health 2>/dev/null) || code="000"
          if [ "$code" != "000" ]; then
            DASHBOARD_OK=1
            break
          fi
          sleep 1
        done
        if [ "$DASHBOARD_OK" -eq 1 ]; then
          ok "ScarliHQ dashboard started on :8090 (HTTP $code — healthy)"
          if [ -n "$SCARLIHQ_TOKEN" ]; then
            # v18.2 P1: token only on TTY (was: info() → tee to log 644 → token leaked)
            # v18.7.7 P1: Dashboard URL says 127.0.0.1:8090 (was: <this-ip>:8090 —
            #   misleading because the compose binds 127.0.0.1:8090:8090, so LAN
            #   clients get connection refused. The bind is correct for security
            #   (dashboard not exposed to LAN without a tunnel). The text now reflects
            #   reality: localhost only, with a note about Tailscale/SSH tunnel for LAN.)
            if [ -t 1 ]; then
              echo -e "${CYAN}  Dashboard login token: ${GREEN}${SCARLIHQ_TOKEN}${NC}" | tee /dev/tty 2>/dev/null || true
              echo -e "${CYAN}  Dashboard (this PC only): http://127.0.0.1:8090/${NC}" | tee /dev/tty 2>/dev/null || true
              echo -e "${CYAN}  (login with the token above, or from /etc/scarlix/.env — chmod 600)${NC}" | tee /dev/tty 2>/dev/null || true
              echo -e "${CYAN}  LAN access: use Tailscale, an SSH tunnel, or a reverse proxy → 127.0.0.1:8090${NC}" | tee /dev/tty 2>/dev/null || true
            else
              info "  Dashboard token generated (in /etc/scarlix/.env — run 'cat /etc/scarlix/.env | grep SCARLIHQ_TOKEN')"
              info "  Dashboard URL: http://127.0.0.1:8090/ (localhost only — use Tailscale/SSH tunnel for LAN)"
            fi
          fi
        else
          # v18.7.8 P0: Container started but /api/health never responded — likely
          # panicked/exited immediately. Dump logs so the user sees WHY.
          warn "ScarliHQ container started but /api/health never responded (30s) — likely crashed"
          warn "  Container status:"
          docker ps -a --filter name=scarlihq --format '{{.Status}}' 2>/dev/null | head -3 | sed 's/^/    /' || true
          warn "  Last 20 log lines:"
          docker logs scarlihq 2>&1 | tail -20 | sed 's/^/    /' || true
          SCARLIHQ_DASHBOARD_FAILED=1
        fi
      else
        # v18.7.7 P1: ScarliHQ dashboard start fail is non-critical (CLI scarlix-mode
        # still works), but make it VERY visible in the summary so the user knows
        # the dashboard won't be available (was: just a warn line easily missed).
        warn "ScarliHQ dashboard start failed (non-critical — CLI still works)"
        warn "  Run: docker compose --env-file /etc/scarlix/.env -f /opt/scarlix/scarlihq/docker-compose.yml up -d"
        SCARLIHQ_DASHBOARD_FAILED=1
      fi
    else
      # v18.7.7 P1: ScarliHQ build fail is non-critical (CLI scarlix-mode still
      # works), but flag it prominently so the user knows the dashboard won't be
      # available. The final summary checks SCARLIHQ_DASHBOARD_FAILED.
      warn "ScarliHQ build failed (non-critical — CLI scarlix-mode still works, but dashboard will NOT be available)"
      warn "  Common cause: no internet for Go module download, or Go syntax error"
      warn "  Check /var/log/scarlix/install.log and re-run install.sh after fixing"
      SCARLIHQ_DASHBOARD_FAILED=1
    fi
  else
    # v18.7.8 P1: Dockerfile missing = dashboard won't exist → set FAILED flag
    # (was: just a warn, SCARLIHQ_DASHBOARD_FAILED never set → summary showed SUCCESS
    # even though dashboard was never built).
    warn "ScarliHQ Dockerfile not found — dashboard not built"
    SCARLIHQ_DASHBOARD_FAILED=1
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
    # v18.5.3 P1: Check compose startup failure (was: docker compose up unconditionally → sleep → API wait)
    if ! docker compose -f /opt/scarlix/ai/ollama/docker-compose.yml up -d >> "$LOG_FILE" 2>&1; then
      warn "Ollama container failed to start — starter model download skipped"
    else
    sleep 5
    # P1-8: Wait for Ollama API to be ready before pulling (max 60s)
    log "Waiting for Ollama API to be ready..."
    OLLAMA_READY=false
    for _ in $(seq 1 12); do
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
    fi  # v18.5.3: end compose startup else (was: unconditional docker compose up)
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
# v18.7.7 P1: Prominent dashboard failure warning (was: just a warn line that
# was easy to miss in the scrollback. If the ScarliHQ build or start failed,
# the user would expect the dashboard at :8090 and get connection refused
# with no explanation. Now: a big red banner in the summary.)
if [ -n "${SCARLIHQ_DASHBOARD_FAILED:-}" ]; then
  echo -e "${RED}╠══════════════════════════════════════════════════════════════╣${NC}"
  echo -e "${RED}║  ⚠ DASHBOARD NOT INSTALLED — scarlihq build/start failed      ║${NC}"
  echo -e "${RED}║    CLI scarlix-mode still works (ai/game/tv/creative/offline) ║${NC}"
  echo -e "${RED}║    Dashboard at http://127.0.0.1:8090 will NOT be available   ║${NC}"
  echo -e "${RED}║    Fix: check /var/log/scarlix/install.log, re-run install.sh ║${NC}"
fi
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
