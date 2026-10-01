#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v18.9.3 — Generate .env (fully atomic for both first-run and existing)
# v17.9.5 FIX: If .env exists, only add missing keys (don't overwrite existing passwords)
# v18.9.3 P1: Fully atomic for existing .env (was: echo >> + sed -i → partial on crash)
#   Now: read existing → add missing → write to mktemp → chmod → chown → mv

mkdir -p /etc/scarlix/secrets

# v18.9.3 P1: Helper to generate a missing key with default value
gen_if_missing() {
  local key="$1" val="$2"
  if ! grep -q "^${key}=" "$ENV_FILE" 2>/dev/null; then
    echo "${key}=${val}"
  fi
}

ENV_FILE="/etc/scarlix/.env"
SCARLIX_VER_FILE="$(cat /etc/scarlix/VERSION 2>/dev/null || echo unknown)"

# Build complete new content (existing + missing keys)
SECRETS_TMP=$(mktemp "${ENV_FILE}.XXXXXX") || exit 1

if [ -f "$ENV_FILE" ]; then
  echo "✓ /etc/scarlix/.env exists — preserving existing passwords, adding missing keys"
  # Start with existing valid lines (filter out orphan lines from old base64)
  grep -E '^([A-Z_][A-Z0-9_]*=|#|$)' "$ENV_FILE" > "$SECRETS_TMP" 2>/dev/null || true
else
  echo "✓ /etc/scarlix/.env does not exist — generating all secrets"
fi

# Add missing keys (only if not already in file)
gen_if_missing "SMG_MASTER_KEY" "sk-scarlix-$(openssl rand -hex 16)" >> "$SECRETS_TMP"
gen_if_missing "TELEGRAM_BOT_TOKEN" "${TELEGRAM_BOT_TOKEN:-}" >> "$SECRETS_TMP"
gen_if_missing "TELEGRAM_ZMOR_CHAT_ID" "${TELEGRAM_ZMOR_CHAT_ID:-}" >> "$SECRETS_TMP"
gen_if_missing "POSTGRES_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "NEXTCLOUD_ROOT_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "NEXTCLOUD_DB_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "GITEA_DB_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "COOLIFY_DB_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "COOLIFY_APP_KEY" "$(openssl rand -hex 32)" >> "$SECRETS_TMP"
gen_if_missing "N8N_DB_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "REDIS_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "GRAFANA_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "PHOTOPRISM_ADMIN_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "N8N_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "RESTIC_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "BUZZ_POSTGRES_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "BUZZ_MINIO_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "JWT_SECRET" "$(openssl rand -hex 32)" >> "$SECRETS_TMP"
gen_if_missing "STORAGE_ENCRYPTION_KEY" "$(openssl rand -hex 32)" >> "$SECRETS_TMP"
gen_if_missing "STEAM_PASSWORD" "$(openssl rand -base64 24)" >> "$SECRETS_TMP"
gen_if_missing "SCARLIHQ_TOKEN" "$(openssl rand -hex 32)" >> "$SECRETS_TMP"
gen_if_missing "LITELLM_MASTER_KEY" "sk-scarlix-$(openssl rand -hex 32)" >> "$SECRETS_TMP"
gen_if_missing "SCARLIX_DEFAULT_PROFILE" "zmor" >> "$SECRETS_TMP"
gen_if_missing "SCARLIX_DEFAULT_MODE" "ai" >> "$SECRETS_TMP"
gen_if_missing "SCARLIX_VERSION" "$SCARLIX_VER_FILE" >> "$SECRETS_TMP"

# Update SCARLIX_VERSION to current (always, not just if missing)
sed -i "s|^SCARLIX_VERSION=.*|SCARLIX_VERSION=$SCARLIX_VER_FILE|" "$SECRETS_TMP"

# Update Telegram tokens if new ones provided
[ -n "${TELEGRAM_BOT_TOKEN:-}" ] && sed -i "s|^TELEGRAM_BOT_TOKEN=.*|TELEGRAM_BOT_TOKEN=$TELEGRAM_BOT_TOKEN|" "$SECRETS_TMP" || true
[ -n "${TELEGRAM_ZMOR_CHAT_ID:-}" ] && sed -i "s|^TELEGRAM_ZMOR_CHAT_ID=.*|TELEGRAM_ZMOR_CHAT_ID=$TELEGRAM_ZMOR_CHAT_ID|" "$SECRETS_TMP" || true

# v18.9.3 P1: Atomic write — chmod + chown + mv (no direct writes to .env)
chmod 600 "$SECRETS_TMP" || { rm -f "$SECRETS_TMP"; exit 1; }
chown root:root "$SECRETS_TMP" || { rm -f "$SECRETS_TMP"; exit 1; }
mv -f "$SECRETS_TMP" "$ENV_FILE" || { rm -f "$SECRETS_TMP"; exit 1; }

echo "SMG_MASTER_KEY: generated (in /etc/scarlix/.env, chmod 600)"
echo "SCARLIHQ_TOKEN: (generated — for dashboard login at :8090)"
echo "LITELLM_MASTER_KEY: (generated — for LiteLLM gateway auth at :4001)"
echo "✓ /etc/scarlix/.env ready"
