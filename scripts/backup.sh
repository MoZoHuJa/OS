#!/bin/bash
set -euo pipefail

export RESTIC_REPOSITORY="${RESTIC_REPOSITORY:-/mnt/backup/restic}"
export RESTIC_PASSWORD_FILE="${RESTIC_PASSWORD_FILE:-/opt/scarlix/secrets/restic-pass}"

TS=$(date +%Y%m%d-%H%M%S)

echo "=== SCARLIX OS v12 Backup ==="

# LVM snapshot (if available)
if sudo lvdisplay /dev/vg0/lv-root &>/dev/null; then
  echo "[1/3] Creating LVM snapshot..."
  sudo lvcreate -L 20G -s -n "root-snap-${TS}" /dev/vg0/lv-root
fi

# Restic backup
echo "[2/3] Running Restic backup..."
restic backup \
  /etc/scarlix \
  /opt/scarlix \
  /var/lib/scarlix \
  /var/lib/buzz \
  /var/lib/sglang \
  --tag daily --tag scarlix-v12

# Cleanup
echo "[3/3] Cleaning old backups..."
restic forget --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune

# Remove old LVM snapshots
if command -v lvs &>/dev/null; then
  sudo lvs --noheadings -o lv_name,lv_time 2>/dev/null | grep root-snap | while read name time; do
    if [[ $(date -d "$time" +%s 2>/dev/null || echo 0) -lt $(date -d "7 days ago" +%s) ]]; then
      sudo lvremove -f "/dev/vg0/$name" 2>/dev/null || true
    fi
  done
fi

echo "=== Backup complete: $TS ==="
