#!/usr/bin/env bash
set -euo pipefail

echo "=== SCARLIX OS v12 — Base OS Installation ==="

# Check root
if [ "$EUID" -ne 0 ]; then
  echo "Run as root: sudo bash base-os/install-base.sh"
  exit 1
fi

# 1. System update
echo "[1/8] System update..."
apt update && apt upgrade -y

# 2. Install base packages
echo "[2/8] Installing base packages..."
apt install -y curl wget git jq bc net-tools htop tmux vim nano \
  build-essential dkms linux-headers-$(uname -r) \
  ufw fail2ban chrony unzip rsync btrfs-progs lvm2

# 3. Set hostname + timezone
echo "[3/8] Setting hostname + timezone..."
hostnamectl set-hostname scarlix-main
timedatectl set-timezone Europe/Bratislava

# 4. Static IP
echo "[4/8] Configuring static IP..."
cp netplan.yaml /etc/netplan/01-scarlix.yaml
netplan apply

# 5. Sudoers
echo "[5/8] Configuring sudoers..."
cp sudoers-scarlix /etc/sudoers.d/scarlix
chmod 440 /etc/sudoers.d/scarlix

# 6. UFW Firewall
echo "[6/8] Configuring UFW firewall..."
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp comment 'SSH'
ufw allow 80/tcp comment 'HTTP'
ufw allow 443/tcp comment 'HTTPS'
ufw allow 41641/udp comment 'Headscale WireGuard'
ufw --force enable

# 7. Fail2ban
echo "[7/8] Configuring Fail2ban..."
cp fail2ban.local /etc/fail2ban/jail.local
systemctl enable --now fail2ban

# 8. Create directories
echo "[8/8] Creating directories..."
mkdir -p /opt/scarlix/{ai,workspace,agents,voice,network,security,monitoring,scripts}
mkdir -p /var/lib/scarlix /etc/scarlix/profiles /models /var/lib/sglang/{cache,scratch}

echo ""
echo "=== Base OS installation complete! ==="
echo "Next steps:"
echo "1. Install NVIDIA driver: sudo apt install nvidia-driver-570-open && sudo reboot"
echo "2. After reboot: Install Docker (see docker/ directory)"
echo "3. Run: bash scripts/install.sh"
