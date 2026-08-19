#!/usr/bin/env bash
set -euo pipefail
mkdir -p /etc/scarlix/secrets
SMG_KEY="sk-scarlix-$(openssl rand -hex 16)"
cat > /etc/scarlix/.env << EOF
SMG_MASTER_KEY=$SMG_KEY
TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN:-}
TELEGRAM_ZMOR_CHAT_ID=${TELEGRAM_ZMOR_CHAT_ID:-}
POSTGRES_PASSWORD=$(openssl rand -base64 24)
NEXTCLOUD_ROOT_PASSWORD=$(openssl rand -base64 24)
NEXTCLOUD_DB_PASSWORD=$(openssl rand -base64 24)
GITEA_DB_PASSWORD=$(openssl rand -base64 24)
COOLIFY_DB_PASSWORD=$(openssl rand -base64 24)
COOLIFY_APP_KEY=$(openssl rand -hex 32)
N8N_DB_PASSWORD=$(openssl rand -base64 24)
REDIS_PASSWORD=$(openssl rand -base64 24)
GRAFANA_PASSWORD=$(openssl rand -base64 24)
PHOTOPRISM_ADMIN_PASSWORD=$(openssl rand -base64 24)
N8N_PASSWORD=$(openssl rand -base64 24)
RESTIC_PASSWORD=$(openssl rand -base64 24)
BUZZ_POSTGRES_PASSWORD=$(openssl rand -base64 24)
BUZZ_MINIO_PASSWORD=$(openssl rand -base64 24)
JWT_SECRET=$(openssl rand -base64 64)
STORAGE_ENCRYPTION_KEY=$(openssl rand -base64 64)
STEAM_PASSWORD=$(openssl rand -base64 24)
SCARLIX_DEFAULT_PROFILE=zmor
SCARLIX_DEFAULT_MODE=ai
EOF
chmod 600 /etc/scarlix/.env
echo "SMG_MASTER_KEY: $SMG_KEY"
echo "Save this key!"
