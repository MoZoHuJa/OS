#!/usr/bin/env bash
# shellcheck disable=SC2034,SC2154
# SCARLIX OS v16.2 — archiso profile definition

iso_name="scarlix-os"
iso_label="SCARLIX_V162"
iso_publisher="MoZoHuJa"
iso_application="SCARLIX OS v16.2 — Garuda Edition"
iso_version="16.2.0"
install_dir="scarlix"
buildmodes=('bios' 'uefi')
bootmodes=('bios.syslinux.mbr' 'bios.syslinux.eltorito' 'uefi-x64.systemd-boot.esp' 'uefi-x64.systemd-boot.eltorito')
arch="x86_64"
pacman_conf="pacman.conf"
airootfs_image_type="squashfs"
airootfs_image_tool_options=('-comp' 'xz' '-Xbcj' 'x86' '-b' '1M' '-Xdict-size' '1M')
file_permissions=(
  ["/etc/shadow"]="0:0:400"
  ["/etc/gshadow"]="0:0:400"
  ["/usr/local/bin/scarlix-wizard"]="0:0:755"
  ["/usr/local/bin/scarlix-mode"]="0:0:755"
  ["/opt/scarlix-installer/scripts/first-boot.sh"]="0:0:755"
  ["/opt/scarlix-installer/scripts/download-models.sh"]="0:0:755"
  ["/opt/scarlix-installer/scripts/install-scarlix.sh"]="0:0:755"
)
