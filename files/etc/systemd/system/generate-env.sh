#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v18.8.7 — Generate .env (with guard for existing passwords)
# v17.9.5 FIX: If .env exists, only add missing keys (don't overwrite existing passwords)
# v17.9.7: This handles /etc/scarlix/.env (SECRETS only). Model paths live in /opt/scarlix/.env
#   which scarlix-mode ALWAYS regenerates (no guard) — model updates are picked up.

mkdir -p /etc/scarlix/secrets

if [ -f /etc/scarlix/.env ]; then
  echo "✓ /etc/scarlix/.env already exists — preserving existing passwords"
  # Only add missing keys (don't overwrite)
  grep -q "^SMG_MASTER_KEY=" /etc/scarlix/.env || echo "SMG_MASTER_KEY=sk-scarlix-$(openssl rand -hex 16)" >> /etc/scarlix/.env
  grep -q "^TELEGRAM_BOT_TOKEN=" /etc/scarlix/.env || echo "TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN:-}" >> /etc/scarlix/.env
  grep -q "^TELEGRAM_ZMOR_CHAT_ID=" /etc/scarlix/.env || echo "TELEGRAM_ZMOR_CHAT_ID=${TELEGRAM_ZMOR_CHAT_ID:-}" >> /etc/scarlix/.env
  grep -q "^POSTGRES_PASSWORD=" /etc/scarlix/.env || echo "POSTGRES_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^NEXTCLOUD_ROOT_PASSWORD=" /etc/scarlix/.env || echo "NEXTCLOUD_ROOT_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^NEXTCLOUD_DB_PASSWORD=" /etc/scarlix/.env || echo "NEXTCLOUD_DB_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^GITEA_DB_PASSWORD=" /etc/scarlix/.env || echo "GITEA_DB_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^COOLIFY_DB_PASSWORD=" /etc/scarlix/.env || echo "COOLIFY_DB_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^COOLIFY_APP_KEY=" /etc/scarlix/.env || echo "COOLIFY_APP_KEY=$(openssl rand -hex 32)" >> /etc/scarlix/.env
  grep -q "^N8N_DB_PASSWORD=" /etc/scarlix/.env || echo "N8N_DB_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^REDIS_PASSWORD=" /etc/scarlix/.env || echo "REDIS_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^GRAFANA_PASSWORD=" /etc/scarlix/.env || echo "GRAFANA_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^PHOTOPRISM_ADMIN_PASSWORD=" /etc/scarlix/.env || echo "PHOTOPRISM_ADMIN_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^N8N_PASSWORD=" /etc/scarlix/.env || echo "N8N_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^RESTIC_PASSWORD=" /etc/scarlix/.env || echo "RESTIC_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^BUZZ_POSTGRES_PASSWORD=" /etc/scarlix/.env || echo "BUZZ_POSTGRES_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^BUZZ_MINIO_PASSWORD=" /etc/scarlix/.env || echo "BUZZ_MINIO_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^JWT_SECRET=" /etc/scarlix/.env || echo "JWT_SECRET=$(openssl rand -hex 32)" >> /etc/scarlix/.env
  grep -q "^STORAGE_ENCRYPTION_KEY=" /etc/scarlix/.env || echo "STORAGE_ENCRYPTION_KEY=$(openssl rand -hex 32)" >> /etc/scarlix/.env
  grep -q "^STEAM_PASSWORD=" /etc/scarlix/.env || echo "STEAM_PASSWORD=$(openssl rand -base64 24)" >> /etc/scarlix/.env
  grep -q "^SCARLIHQ_TOKEN=" /etc/scarlix/.env || echo "SCARLIHQ_TOKEN=$(openssl rand -hex 32)" >> /etc/scarlix/.env
  # v18.8.2 P0: LITELLM_MASTER_KEY (was: missing → LiteLLM compose got empty value
  #   → gateway auth broken even though routing config was correct).
  grep -q "^LITELLM_MASTER_KEY=" /etc/scarlix/.env || echo "LITELLM_MASTER_KEY=sk-scarlix-$(openssl rand -hex 32)" >> /etc/scarlix/.env
  grep -q "^SCARLIX_DEFAULT_PROFILE=" /etc/scarlix/.env || echo "SCARLIX_DEFAULT_PROFILE=zmor" >> /etc/scarlix/.env
  grep -q "^SCARLIX_DEFAULT_MODE=" /etc/scarlix/.env || echo "SCARLIX_DEFAULT_MODE=ai" >> /etc/scarlix/.env
  # v18.8.2 P0: SCARLIX_VERSION for compose image tag interpolation (was: hardcoded
  #   in docker-compose.yml → sed regex created v18.8.1.1 mismatch. Now: single
  #   source of truth from /etc/scarlix/VERSION, compose uses ${SCARLIX_VERSION}.)
  SCARLIX_VER_FILE="$(cat /etc/scarlix/VERSION 2>/dev/null || echo unknown)"
  if grep -q "^SCARLIX_VERSION=" /etc/scarlix/.env; then
    sed -i "s|^SCARLIX_VERSION=.*|SCARLIX_VERSION=$SCARLIX_VER_FILE|" /etc/scarlix/.env
  else
    echo "SCARLIX_VERSION=$SCARLIX_VER_FILE" >> /etc/scarlix/.env
  fi
  # Update Telegram tokens if new ones provided
  [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && sed -i "s|^TELEGRAM_BOT_TOKEN=.*|TELEGRAM_BOT_TOKEN=$TELEGRAM_BOT_TOKEN|" /etc/scarlix/.env || true
  [ -n "${TELEGRAM_ZMOR_CHAT_ID:-}" ] && sed -i "s|^TELEGRAM_ZMOR_CHAT_ID=.*|TELEGRAM_ZMOR_CHAT_ID=$TELEGRAM_ZMOR_CHAT_ID|" /etc/scarlix/.env || true
else
  # First run — generate all
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
JWT_SECRET=$(openssl rand -hex 32)
STORAGE_ENCRYPTION_KEY=$(openssl rand -hex 32)
STEAM_PASSWORD=$(openssl rand -base64 24)
SCARLIHQ_TOKEN=$(openssl rand -hex 32)
LITELLM_MASTER_KEY=sk-scarlix-$(openssl rand -hex 32)
SCARLIX_DEFAULT_PROFILE=zmor
SCARLIX_DEFAULT_MODE=ai
SCARLIX_VERSION=$(cat /etc/scarlix/VERSION 2>/dev/null || echo unknown)
EOF
  # v18.5 P0: Don't print secrets to log (was: echo "SMG_MASTER_KEY: $SMG_KEY" → install.log)
  echo "SMG_MASTER_KEY: generated (in /etc/scarlix/.env, chmod 600)"
  echo "SCARLIHQ_TOKEN: (generated — for dashboard login at :8090)"
  echo "LITELLM_MASTER_KEY: (generated — for LiteLLM gateway auth at :4001)"
fi

chmod 600 /etc/scarlix/.env
echo "✓ /etc/scarlix/.env ready"
