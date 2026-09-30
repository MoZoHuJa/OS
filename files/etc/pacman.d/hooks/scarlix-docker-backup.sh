#!/usr/bin/env bash
# SCARLIX OS v18.8.7 — Docker Volume Backup Script (pacman hook)
# FIX Q7a: Called by scarlix-docker-backup.hook before kernel/NVIDIA updates.
#
# Uses restic to snapshot /var/lib/docker/volumes to /mnt/backup/restic/.
# Non-fatal — if backup fails, the pacman transaction still proceeds.

set -euo pipefail

# v18.8.6 P1: Default trigger if no arg (was: $1 empty → tag "Trigger: " with empty value)
TRIGGER="${1:-pacman}"

BACKUP_REPO="/mnt/backup/restic/docker-volumes"
BACKUP_TARGET="/var/lib/docker/volumes"
TIMESTAMP=$(date '+%Y-%m-%d_%H%M%S')
LOG_FILE="/var/log/scarlix/docker-backup.log"

mkdir -p "$(dirname "$LOG_FILE")"

log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

log "=== SCARLIX Docker Volume Backup (pre-pacman) ==="
log "Trigger: $TRIGGER (triggered by pacman hook)"

# Check if Docker is running
if ! docker info >/dev/null 2>&1; then
  log "Docker not running — skipping backup."
  exit 0
fi

# v18.8.3 P2 (P2-12): Track backup status explicitly (was: restic init || true
#   → if init failed, backup also failed silently with "continuing anyway".
#   Now: BACKUP_STATUS tracks init + backup result, logged at end.)
BACKUP_STATUS="OK"

# v18.8.5 P1 (P1-2): Source /etc/scarlix/.env so RESTIC_PASSWORD is available
#   to restic init/backup. (was: no sourcing → restic prompted for password
#   interactively in pacman hook (no TTY) → init/backup silently failed.)
if [ -f /etc/scarlix/.env ]; then
  set -a
  # shellcheck source=/dev/null
  . /etc/scarlix/.env
  set +a
fi

# v18.8.5 P1 (P1-2): restic needs RESTIC_PASSWORD (was: interactive prompt
#   fails in pacman hook — no TTY. Without password, restic init prompts and
#   hangs/fails. Now: fail-closed early with explicit NO_PASSWORD status.)
if [ -z "${RESTIC_PASSWORD:-}" ]; then
  log "⚠ RESTIC_PASSWORD not set — restic init/backup will fail (set in /etc/scarlix/.env)"
  BACKUP_STATUS="NO_PASSWORD"
  log "Backup status: $BACKUP_STATUS"
  log "=== Backup complete (no password) ==="
  exit 0
fi
export RESTIC_PASSWORD

# v18.8.6 P1: Verify backup disk is mounted (was: wrote to root filesystem
#   if /mnt/backup not mounted — restic would silently create the repo dir on
#   the root filesystem, filling /. Now: check parent is a mountpoint, exit
#   early with NO_MOUNTPOINT status so root filesystem is never written to.)
if ! mountpoint -q "$(dirname "$BACKUP_REPO")" 2>/dev/null; then
  log "⚠ Backup directory parent not a mountpoint — backup may write to root filesystem"
  log "  (expected: /mnt/backup mounted. Got: $(df "$(dirname "$BACKUP_REPO")" 2>/dev/null | tail -1 | awk '{print $1}'))"
  BACKUP_STATUS="NO_MOUNTPOINT"
  log "Backup status: $BACKUP_STATUS"
  exit 0
fi

# Check if restic backup repo exists, init if not
# v18.8.5 P0 (P0-3): Check for restic config file, not directory (was: [ ! -d ]
#   → mkdir -p created the dir → next run [ ! -d ] was false → restic init was
#   NEVER retried → repo stayed uninitialized → every backup failed silently.
#   Also: after INIT_FAILED, script continued to restic backup which also
#   failed, and BACKUP_STATUS got overwritten from INIT_FAILED to BACKUP_FAILED
#   — the root cause (init) was lost. Now: check for config file (only present
#   after successful restic init), and exit early on init failure so the
#   INIT_FAILED status survives.)
if [ ! -f "$BACKUP_REPO/config" ]; then
  mkdir -p "$BACKUP_REPO"
  log "Initializing restic repo: $BACKUP_REPO"
  if ! restic init --repo "$BACKUP_REPO" >> "$LOG_FILE" 2>&1; then
    log "✗ restic init FAILED — skipping backup (repo not initialized)"
    BACKUP_STATUS="INIT_FAILED"
    log "Backup status: $BACKUP_STATUS"
    log "=== Backup complete (init failed) ==="
    exit 0
  fi
fi

# Snapshot Docker volumes
log "Backing up $BACKUP_TARGET → $BACKUP_REPO"
if restic backup --repo "$BACKUP_REPO" "$BACKUP_TARGET" \
  --tag "pre-pacman" \
  --tag "$TRIGGER" \
  --tag "timestamp-$TIMESTAMP" >> "$LOG_FILE" 2>&1; then
  log "✓ Docker volumes backed up successfully"

  # Prune old backups (keep last 10)
  restic forget --repo "$BACKUP_REPO" \
    --keep-last 10 \
    --prune >> "$LOG_FILE" 2>&1 || log "⚠ prune failed (non-fatal)"
else
  log "⚠ Backup FAILED — continuing anyway (non-fatal, but volumes NOT backed up)"
  BACKUP_STATUS="BACKUP_FAILED"
fi

# v18.8.3 P2: Final status line (was: silent — user couldn't tell if backup worked)
log "Backup status: $BACKUP_STATUS"

log "=== Backup complete ==="
exit 0
