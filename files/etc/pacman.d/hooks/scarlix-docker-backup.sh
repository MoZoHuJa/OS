#!/usr/bin/env bash
# SCARLIX OS v18.8.2 — Docker Volume Backup Script (pacman hook)
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

# Check if restic backup repo exists, init if not
if [ ! -d "$BACKUP_REPO" ]; then
  mkdir -p "$BACKUP_REPO"
  log "Initializing restic repo: $BACKUP_REPO"
  restic init --repo "$BACKUP_REPO" >> "$LOG_FILE" 2>&1 || true
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
    --prune >> "$LOG_FILE" 2>&1 || true
else
  log "⚠ Backup failed — continuing anyway (non-fatal)"
fi

log "=== Backup complete ==="
exit 0
