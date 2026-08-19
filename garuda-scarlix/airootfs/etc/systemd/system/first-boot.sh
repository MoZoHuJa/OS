#!/usr/bin/env bash
set -euo pipefail
echo "=== SCARLIX OS v16.3 — First Boot ==="

# Check if already installed
[[ -f /opt/scarlix/.installed ]] && { echo "Already installed."; exit 0; }

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

# FIX 2-b: Install NVIDIA + CUDA post-install (only on Main PC)
if [ "$PC_TYPE" == "main" ]; then
  echo "Installing NVIDIA driver + CUDA (post-install)..."
  sudo pacman -S --noconfirm --needed nvidia-dkms nvidia-utils nvidia-settings lib32-nvidia-utils
  sudo pacman -S --noconfirm --needed cuda cudnn
  sudo mkinitcpio -P
  echo "NVIDIA + CUDA installed."
  
  # NVIDIA Container Toolkit (from AUR)
  echo "Installing NVIDIA Container Toolkit..."
  yay -S --noconfirm nvidia-container-toolkit 2>/dev/null || true
  sudo nvidia-ctk runtime configure --runtime=docker 2>/dev/null || true
fi

# Start Docker
systemctl start docker
sleep 3

# FIX 5-c: Absolute paths (no cd pattern), UFW already in install-garuda.sh (no duplication)
if [ "$PC_TYPE" == "main" ]; then
  echo "Starting Main PC services..."
  set -a; source /etc/scarlix/.env; set +a
  
  # AI Stack — absolute paths
  docker compose -f /opt/scarlix-src/ai/smg/docker-compose.yml up -d 2>/dev/null || true
  docker compose -f /opt/scarlix-src/ai/sglang/docker-compose.yml up -d 2>/dev/null || true
  docker compose -f /opt/scarlix-src/ai/ollama/docker-compose-main.yml up -d 2>/dev/null || true
  docker compose -f /opt/scarlix-src/ai/ollama/docker-compose-agent.yml up -d 2>/dev/null || true
  docker compose -f /opt/scarlix-src/ai/needle/docker-compose.yml up -d 2>/dev/null || true
  docker compose -f /opt/scarlix-src/ai/llamacpp/docker-compose.yml up -d 2>/dev/null || true
  
  # Network & Security
  docker compose -f /opt/scarlix-src/network/docker-compose.yml up -d 2>/dev/null || true
  
  # Voice pipeline
  docker compose -f /opt/scarlix-src/voice/docker-compose.yml up -d 2>/dev/null || true
  
  # Workspace & Agents
  docker compose -f /opt/scarlix-src/workspace/buzz/docker-compose.yml up -d 2>/dev/null || true
  docker compose -f /opt/scarlix-src/agents/hermes/docker-compose.yml up -d 2>/dev/null || true
  
  # ScarliHQ
  docker compose -f /opt/scarlix-src/scarlihq/docker-compose.yml up -d 2>/dev/null || true
  
  # Monitoring
  docker compose -f /opt/scarlix-src/monitoring/docker-compose.yml up -d 2>/dev/null || true
  
  # Gaming (always-on Docker services only — Steam/Lutris are native)
  docker compose -f /opt/scarlix-src/gaming/docker-compose.yml up -d jellyfin minecraft 2>/dev/null || true
  
  # Set default mode
  echo "ai" | tee /var/lib/scarlix/current-mode >/dev/null
  
  # Download models in background
  echo "Starting model download in background..."
  nohup /etc/systemd/system/download-models.sh >/dev/null 2>&1 &
else
  echo "Starting HP Agent services..."
  set -a; source /etc/scarlix/.env; set +a
  
  # HP Agent — absolute paths
  docker compose -f /opt/scarlix-src/coding-pipeline/docker-compose.yml up -d 2>/dev/null || true
  docker compose -f /opt/scarlix-src/media-tools/docker-compose.yml up -d 2>/dev/null || true
fi

echo "=== First boot complete ==="
echo "Dashboard: http://$(hostname -I | awk '{print $1}'):8090"
