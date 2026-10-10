#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v19.2.1 — Generate .env (fully atomic for both first-run and existing)
# v17.9.5 FIX: If .env exists, only add missing keys (don't overwrite existing passwords)
# v18.9.3 P1: Fully atomic for existing .env (was: echo >> + sed -i → partial on crash)
#   Now: read existing → add missing → write to mktemp → chmod → chown → mv
# v19.0.1 P1 (3 fixes from sandbox review):
#   (a) Duplicate detection now matches ONLY KEY= lines (was: cut -d= -f1 also
#       counted comments/blank lines → two identical "# note" lines → ERROR exit
#       → doctor loop. Now: grep -E '^[A-Z_][A-Z0-9_]*=' before cut.)
#       Duplicates are auto-resolved (keep last) instead of hard-aborting, so a
#       pre-existing dupe in .env no longer deadlocks scarlix-doctor.
#   (b) Telegram token update no longer corrupts values containing '='. Was:
#       awk FS=OFS="=" with $2=v — a token "abc=def=" became "NEW123=def=".
#       Now: index($0,k"=")==1 line-match + ENVIRON[] for the value (also safe
#       against backslashes, which awk -v would have re-interpreted).
#   (c) Orphan-line (continuation) detection: a KEY= line whose value came from
#       a multi-line base64 write may have its 2nd half on an orphan line. The
#       orphan is dropped, leaving a TRUNCATED secret that silently validates.
#       Now: WARN on orphan-after-assignment; regenerate JWT_SECRET automatically
#       (rotate-able); STORAGE_ENCRYPTION_KEY left for manual review (rotating
#       it would make existing encrypted data unreadable).

mkdir -p /etc/scarlix/secrets

# v18.9.3 P1: Helper to generate a missing key with default value
# v18.9.4 P1-01: Check SECRETS_TMP not ENV_FILE (was: checked ENV_FILE →
#   keys already in SECRETS_TMP from previous gen_if_missing were invisible
#   → duplicate entries possible)
gen_if_missing() {
  local key="$1" val="$2"
  if ! grep -q "^${key}=" "$SECRETS_TMP" 2>/dev/null; then
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
  # v19.0.1 P1-c: Detect orphan lines BEFORE filtering so we can warn + act on them.
  #   An "orphan" is a non-empty line that is NOT a KEY= assignment, NOT a comment,
  #   and NOT blank — i.e. a continuation fragment left by an old multiline write.
  #   We scan the ORIGINAL file (before filtering) so the signal isn't lost.
  ORPHAN_FOUND=false
  JWT_REGEN=false
  prev_key=""
  while IFS= read -r line || [ -n "$line" ]; do
    if [[ "$line" =~ ^[A-Z_][A-Z0-9_]*= ]]; then
      prev_key="${line%%=*}"
    elif [[ "$line" =~ ^[[:space:]]*($|#) ]]; then
      : # comment / blank — not an orphan
    elif [ -n "$line" ]; then
      # Non-empty, non-assignment, non-comment → orphan continuation of prev_key
      ORPHAN_FOUND=true
      echo "⚠ WARN: orphan continuation line after '${prev_key}=' — value may be truncated." >&2
      if [ "$prev_key" = "JWT_SECRET" ]; then
        JWT_REGEN=true
        echo "⚠ WARN: JWT_SECRET appears truncated — will regenerate (safe to rotate)." >&2
      elif [ "$prev_key" = "STORAGE_ENCRYPTION_KEY" ]; then
        echo "⚠ ERROR: STORAGE_ENCRYPTION_KEY appears truncated but will NOT be auto-regenerated" >&2
        echo "         (rotating it makes existing encrypted data unreadable)." >&2
        echo "         Manually verify /etc/scarlix/.env and restore from backup if needed." >&2
      fi
    fi
  done < "$ENV_FILE"
  if [ "$ORPHAN_FOUND" = true ]; then
    echo "⚠ WARN: orphan continuation lines found in $ENV_FILE (see warnings above)" >&2
  fi
  grep -E '^([A-Z_][A-Z0-9_]*=|#|$)' "$ENV_FILE" > "$SECRETS_TMP" 2>/dev/null || true
  # v19.0.1 P1-c: If JWT_SECRET was flagged as truncated, drop it so gen_if_missing
  #   regenerates a fresh one below (rotate-safe; other services re-read .env on restart).
  if [ "$JWT_REGEN" = true ]; then
    sed -i '/^JWT_SECRET=/d' "$SECRETS_TMP"
    echo "✓ JWT_SECRET regenerated (truncated value replaced with fresh secret)" >&2
  fi
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

# Update Telegram tokens if new ones provided (v18.9.4 P2-05: safe interpolation)
# v18.9.5 P2-01: Fail-closed (was: || true → Telegram update failure masked)
# v19.0.1 P1-b: Do NOT use awk FS=OFS="=" with $2=v — a token containing '='
#   (e.g. "123:ABC=DEF=") was corrupted to "NEW=DEF=" because only field 2 was
#   replaced and fields 3..N survived. Now: match the line by prefix "KEY=" and
#   print "KEY=<value>" from ENVIRON, which (1) preserves embedded '=' and
#   (2) avoids awk -v re-interpreting backslashes in the value.
update_kv() {
  local key="$1" envvar="$2" src="$3"
  local tmp="${src}.awk"
  # Pass the value through the environment (no -v → no escape re-processing)
  TV="${!envvar}" awk -v k="$key" '
    index($0, k "=") == 1 { print k "=" ENVIRON["TV"]; next }
    { print }
  ' "$src" > "$tmp" || return 1
  mv -f "$tmp" "$src"
}
if [ -n "${TELEGRAM_BOT_TOKEN:-}" ]; then
  update_kv "TELEGRAM_BOT_TOKEN" "TELEGRAM_BOT_TOKEN" "$SECRETS_TMP" \
    || { echo "ERROR: failed to update TELEGRAM_BOT_TOKEN" >&2; rm -f "$SECRETS_TMP"; exit 1; }
fi
if [ -n "${TELEGRAM_ZMOR_CHAT_ID:-}" ]; then
  update_kv "TELEGRAM_ZMOR_CHAT_ID" "TELEGRAM_ZMOR_CHAT_ID" "$SECRETS_TMP" \
    || { echo "ERROR: failed to update TELEGRAM_ZMOR_CHAT_ID" >&2; rm -f "$SECRETS_TMP"; exit 1; }
fi

# v18.9.5 P2-02: Validate required secrets have non-empty values (was: empty key accepted)
REQUIRED_KEYS="SCARLIHQ_TOKEN LITELLM_MASTER_KEY SMG_MASTER_KEY RESTIC_PASSWORD"
for rkey in $REQUIRED_KEYS; do
  rval=$(grep "^${rkey}=" "$SECRETS_TMP" 2>/dev/null | cut -d= -f2- || echo "")
  if [ -z "$rval" ]; then
    echo "ERROR: Required secret $rkey is empty or missing" >&2
    rm -f "$SECRETS_TMP"
    exit 1
  fi
done

# v18.9.4 P1-01: Validate no duplicate keys (was: gen_if_missing checked wrong file → dupes)
# v19.0.1 P1-a: The old check `cut -d= -f1 "$SECRETS_TMP" | sort | uniq -d` matched
#   EVERY line including comments/blank lines → two identical "# note" comments
#   were reported as a duplicate → ERROR exit rc=1 → scarlix-doctor (which calls
#   this script via fix_regenerate_env) entered an infinite loop.
#   Now: (1) only KEY= lines are inspected via `grep -E '^[A-Z_][A-Z0-9_]*='`;
#   (2) duplicates are auto-resolved keeping the LAST occurrence (so a pre-existing
#   dupe in .env no longer hard-aborts — it is fixed in place) and a WARN is
#   printed listing the collapsed keys.
DUPES=$(grep -E '^[A-Z_][A-Z0-9_]*=' "$SECRETS_TMP" | cut -d= -f1 | sort | uniq -d)
if [ -n "$DUPES" ]; then
  echo "⚠ WARN: Duplicate env keys detected and auto-resolved (kept last): $DUPES" >&2
  # Collapse duplicates keeping the LAST occurrence:
  #   tac | awk (skip if seen before, keyed on field 1) | tac
  #   Only KEY= lines participate in dedup; comments/blank pass through untouched.
  tac "$SECRETS_TMP" | awk -F= '
    /^[A-Z_][A-Z0-9_]*=/ { if (s[$1]++) { next } }
    { print }
  ' | tac > "${SECRETS_TMP}.dedup" || { rm -f "$SECRETS_TMP"; exit 1; }
  mv -f "${SECRETS_TMP}.dedup" "$SECRETS_TMP"
  # Re-verify (defensive — should always be empty now)
  REMAIN=$(grep -E '^[A-Z_][A-Z0-9_]*=' "$SECRETS_TMP" | cut -d= -f1 | sort | uniq -d)
  if [ -n "$REMAIN" ]; then
    echo "ERROR: Duplicate env keys persist after dedup: $REMAIN" >&2
    rm -f "$SECRETS_TMP"
    exit 1
  fi
fi

# v18.9.3 P1: Atomic write — chmod + chown + mv (no direct writes to .env)
chmod 600 "$SECRETS_TMP" || { rm -f "$SECRETS_TMP"; exit 1; }
chown root:root "$SECRETS_TMP" || { rm -f "$SECRETS_TMP"; exit 1; }
mv -f "$SECRETS_TMP" "$ENV_FILE" || { rm -f "$SECRETS_TMP"; exit 1; }

echo "SMG_MASTER_KEY: generated (in /etc/scarlix/.env, chmod 600)"
echo "SCARLIHQ_TOKEN: (generated — for dashboard login at :8090)"
echo "LITELLM_MASTER_KEY: (generated — for LiteLLM gateway auth at :4001)"
echo "✓ /etc/scarlix/.env ready"
