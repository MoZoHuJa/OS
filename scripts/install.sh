#!/usr/bin/env bash
set -euo pipefail

echo "=== SCARLIX OS v12 — Main Installation ==="

if [ "$EUID" -ne 0 ]; then
  echo "Run as root: sudo bash scripts/install.sh"
  exit 1
fi

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
echo "Repository: $REPO_DIR"

# 1. Create directories
echo "[1/6] Creating directories..."
mkdir -p /opt/scarlix/{ai,workspace,agents,voice,network,security,monitoring,scripts,gaming}
mkdir -p /var/lib/scarlix /etc/scarlix/profiles /models /var/lib/sglang/{cache,scratch}
mkdir -p /var/lib/buzz /var/lib/docker

# 2. Copy configurations
echo "[2/6] Copying configurations..."
cp -r "$REPO_DIR/ai/"* /opt/scarlix/ai/
cp -r "$REPO_DIR/workspace/"* /opt/scarlix/workspace/
cp -r "$REPO_DIR/agents/"* /opt/scarlix/agents/
cp -r "$REPO_DIR/voice/"* /opt/scarlix/voice/
cp -r "$REPO_DIR/network/"* /opt/scarlix/network/
cp -r "$REPO_DIR/security/"* /opt/scarlix/security/
cp -r "$REPO_DIR/monitoring/"* /opt/scarlix/monitoring/

# 3. Install scripts
echo "[3/6] Installing scripts..."
cp "$REPO_DIR/scripts/scarlix-mode.sh" /usr/local/bin/scarlix-mode
chmod +x /usr/local/bin/scarlix-mode
cp "$REPO_DIR/scripts/backup.sh" /opt/scarlix/scripts/backup.sh
chmod +x /opt/scarlix/scripts/backup.sh
cp "$REPO_DIR/scripts/download-models.sh" /opt/scarlix/scripts/download-models.sh
chmod +x /opt/scarlix/scripts/download-models.sh

# 4. Install profiles
echo "[4/6] Installing profiles..."
cp "$REPO_DIR/profiles/"*.yaml /etc/scarlix/profiles/

# 5. Install AGENTS.md
echo "[5/6] Installing AGENTS.md..."
cp "$REPO_DIR/AGENTS.md" /etc/scarlix/AGENTS.md

# 6. Docker networks
echo "[6/6] Creating Docker networks..."
bash "$REPO_DIR/docker/networks.sh"

# Set default mode
echo "ai" > /var/lib/scarlix/current-mode

echo ""
echo "=== Installation complete! ==="
echo ""
echo "Next steps:"
echo "1. Copy .env.template to /etc/scarlix/.env and fill in secrets"
echo "2. Download models: bash /opt/scarlix/scripts/download-models.sh"
echo "3. Start AI stack:"
echo "   cd /opt/scarlix/ai/sglang && docker compose up -d"
echo "   cd /opt/scarlix/ai/ollama && docker compose up -d"
echo "   cd /opt/scarlix/ai/litellm && docker compose up -d"
echo "4. Check: scarlix-mode status"
