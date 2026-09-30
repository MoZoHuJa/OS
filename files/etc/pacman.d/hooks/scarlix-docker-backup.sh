#!/usr/bin/env bash
# SCARLIX OS v18.8.3 — Docker Volume Backup Script (pacman hook)
# FIX Q7a: Called by scarlix-docker-backup.hook before kernel/NVIDIA updates.
#
# Uses restic to snapshot /var/lib/docker/volumes to /mnt/backup/restic/.
# Non-fatal — if backup fails, the pacman transaction still proceeds.

set -euo pipefail

BACKUP_REPO="/mnt/backup/restic/docker-volumes"
BACKUP_TARGET="/var/lib/docker/volumes"
TIMESTAMP=$(date '+%Y-%m-%d_%H%M%S')
LOG_FILE="/var/log/scarlix/docker-backup.log"

mkdir -p "$(dirname "$LOG_FILE")"

log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

log "=== SCARLIX Docker Volume Backup (pre-pacman) ==="
log "Trigger: $1 (triggered by pacman hook)"

# Check if Docker is running
if ! docker info >/dev/null 2>&1; then
  log "Docker not running — skipping backup."
  exit 0
fi

# v18.8.3 P2 (P2-12): Track backup status explicitly (was: restic init || true
#   → if init failed, backup also failed silently with "continuing anyway".
#   Now: BACKUP_STATUS tracks init + backup result, logged at end.)
BACKUP_STATUS="OK"

# Check if restic backup repo exists, init if not
if [ ! -d "$BACKUP_REPO" ]; then
  mkdir -p "$BACKUP_REPO"
  log "Initializing restic repo: $BACKUP_REPO"
  # v18.8.3 P2: Fail-closed init (was: || true → backup ran on uninitialized repo)
  if ! restic init --repo "$BACKUP_REPO" >> "$LOG_FILE" 2>&1; then
    log "✗ restic init FAILED — backup will likely fail"
    BACKUP_STATUS="INIT_FAILED"
  fi
fi

# Snapshot Docker volumes
log "Backing up $BACKUP_TARGET → $BACKUP_REPO"
if restic backup --repo "$BACKUP_REPO" "$BACKUP_TARGET" \
  --tag "pre-pacman" \
  --tag "$1" \
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
