#!/usr/bin/env bash
set -euo pipefail
echo "=== SCARLIX OS v16.2 — First Boot ==="

# Detect PC type if not set
PC_TYPE=$(cat /etc/scarlix/pc_type 2>/dev/null || echo "")
if [ -z "$PC_TYPE" ]; then
  GPU_COUNT=$(lspci | grep -ic nvidia 2>/dev/null || echo 0)
  if [ "$GPU_COUNT" -gt 0 ]; then
    PC_TYPE="main"
  else
    PC_TYPE="hp_agent"
  fi
  echo "$PC_TYPE" | tee /etc/scarlix/pc_type >/dev/null
fi

echo "PC Type: $PC_TYPE"

# Generate .env if missing
if [ ! -f /etc/scarlix/.env ]; then
  echo "Generating .env..."
  /etc/systemd/system/generate-env.sh
fi

# Create directories
mkdir -p /opt/scarlix /models /var/lib/scarlix /etc/scarlix/{profiles,secrets}
mkdir -p /mnt/{files,games,photos,backup/restic}
chown -R scarlix:scarlix /opt/scarlix /models /var/lib/scarlix /etc/scarlix /mnt

# Start Docker
systemctl start docker
sleep 3

if [ "$PC_TYPE" == "main" ]; then
  echo "Starting Main PC services..."
  set -a; source /etc/scarlix/.env; set +a
  
  cd /opt/scarlix-src/ai/smg && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix-src/ai/sglang && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix-src/ai/ollama && docker compose -f docker-compose-main.yml up -d 2>/dev/null || true
  cd /opt/scarlix-src/ai/ollama && docker compose -f docker-compose-agent.yml up -d 2>/dev/null || true
  cd /opt/scarlix-src/ai/needle && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix-src/ai/llamacpp && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix-src/network && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix-src/voice && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix-src/workspace/buzz && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix-src/agents/hermes && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix-src/scarlihq && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix-src/monitoring && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix-src/gaming && docker compose up -d jellyfin minecraft 2>/dev/null || true
  
  echo "ai" | tee /var/lib/scarlix/current-mode >/dev/null
  
  # Download models in background
  echo "Starting model download in background..."
  nohup /etc/systemd/system/download-models.sh >/dev/null 2>&1 &
else
  echo "Starting HP Agent services..."
  set -a; source /etc/scarlix/.env; set +a
  
  cd /opt/scarlix-src/coding-pipeline && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix-src/media-tools && docker compose up -d 2>/dev/null || true
fi

echo "=== First boot complete ==="
echo "Dashboard: http://$(hostname -I | awk '{print $1}'):8090"
