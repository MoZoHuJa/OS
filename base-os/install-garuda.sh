#!/usr/bin/env bash
set -euo pipefail
echo "=== SCARLIX OS v16.1 — Garuda Base Setup ==="

# Garuda update (creates BTRFS snapshot automatically)
sudo garuda-update

# Install base packages (pacman)
sudo pacman -S --needed \
  jq bc net-tools tmux nano base-devel dkms \
  ufw fail2ban chrony unzip rsync python-pip python-virtualenv \
  bluez blueman alsa-utils portaudio \
  docker docker-compose restic

# AUR packages
yay -S --needed --noconfirm nvidia-container-toolkit whiptail

# Enable services
sudo systemctl enable --now docker chrony fail2ban bluetooth
sudo timedatectl set-timezone Europe/Bratislava

# Static IP (NetworkManager)
sudo nmcli connection modify "Wired connection 1" \
  ipv4.addresses 192.168.1.100/24 \
  ipv4.gateway 192.168.1.1 \
  ipv4.dns "1.1.1.1 8.8.8.8" \
  ipv4.method manual
sudo nmcli connection up "Wired connection 1"

# UFW
sudo ufw default deny incoming && sudo ufw default allow outgoing
sudo ufw allow 22/tcp && sudo ufw allow 80/tcp && sudo ufw allow 443/tcp && sudo ufw allow 41641/udp
sudo ufw --force enable

# Snapper for SCARLIX subvolumes
sudo snapper -c scarlix-opt create-config /opt 2>/dev/null || true
sudo snapper -c scarlix-models create-config /models 2>/dev/null || true
sudo snapper -c scarlix-var create-config /var 2>/dev/null || true

# Directories
sudo mkdir -p /opt/scarlix/{ai,workspace,agents,voice,network,security,monitoring,scarlihq,scripts}
sudo mkdir -p /models /var/lib/scarlix /etc/scarlix/{profiles,secrets}
sudo mkdir -p /mnt/{files,games,photos,backup/restic}
sudo chown -R scarlix:scarlix /opt/scarlix /models /var/lib/scarlix /etc/scarlix /mnt

echo "=== Garuda base setup complete! ==="
