#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v19.0.0 — Model Manager
# v18.7 FIX: Ollama pulls via `docker exec ollama-agent` (was: host `ollama` binary — never installed)
# FIX Q8b: Split — HF model pulls = auto (safe), Ollama tag pulls = manual (--apply only)
#
# Weekly timer (Mon 04:00) runs with NO --apply → only HF model pulls + Telegram report.
# Ollama tag updates require manual: model-manager.sh --apply-ollama
# (Because Ollama updates can break CUDA — e.g. 0.12.4 regression.)

LOG_FILE="/var/log/scarlix/model-manager.log"
MODELS_YAML="/etc/scarlix/models.yaml"
ENV_FILE="/opt/scarlix/.env"
# v18.9.0 P2-04: Read version from VERSION file (was: hardcoded in Telegram messages)
SCARLIX_VER="$(cat /etc/scarlix/VERSION 2>/dev/null || echo unknown)"
APPLY_OLLAMA=0
[[ "${1:-}" == "--apply-ollama" ]] && APPLY_OLLAMA=1

# v18.9.0 P1-03: Download to staging, then atomic move
# (was: direct to /models → partial files on interruption)
STAGING_DIR="/models/.staging"
mkdir -p "$STAGING_DIR" 2>/dev/null || true

mkdir -p "$(dirname "$LOG_FILE")"

# v18.7 P1: Shared lock with scarlix-mode + download-models (was: race condition on /models)
MODELS_LOCK="/var/lib/scarlix/.models.lock"
if ! mkdir -p "$(dirname "$MODELS_LOCK")" 2>/dev/null; then
  echo "FATAL: cannot create models lock directory $(dirname "$MODELS_LOCK")" >&2
  exit 1
fi
# v18.8 P1: Fail-closed mkdir (was: || true → cryptic set -e exit if dir RO)
exec 9>"$MODELS_LOCK"
# v18.7 P0: Non-blocking + exclusive (was: shared -s, blocking without -n)
# v18.7 P1: Exclusive because model-manager WRITES to /models (was: -s shared → race with download-models)
flock -n -x 9 || { echo "ERROR: cannot acquire models lock (scarlix-mode or download-models running?)" >&2; exit 1; }

log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

# v18.7 P0: SECURITY — NEVER `source` .env files (shell execution of potentially user-modified file).
# Instead, parse KEY=VALUE pairs safely with grep + export.
# This prevents local privilege escalation via injected shell commands in .env.
load_env_safe() {
  local envfile="$1"
  [ -f "$envfile" ] || return 0
  # Only parse lines matching KEY=VALUE pattern (no shell evaluation)
  while IFS='=' read -r key value; do
    # Skip comments, empty lines, and lines without =
    [[ "$key" =~ ^[[:space:]]*# ]] && continue
    [[ -z "$key" ]] && continue
    # v18.6 P1: Validate key is a valid shell identifier (was: any key accepted → injection risk)
    [[ "$key" =~ ^[A-Z_][A-Z0-9_]*$ ]] || continue
    # Export the value (no shell evaluation — just string assignment)
    export "$key=$value"
  done < "$envfile" 2>/dev/null || true
}

send_telegram() {
  local message="$1"
  if [ -f "$ENV_FILE" ] || [ -f "/etc/scarlix/.env" ]; then
    # P1 v17.9.5: Source both .env files — /opt for model paths, /etc for secrets/Telegram
    # v18.7 P0: SAFE parse (was: `source` → LPE if user modifies .env)
    load_env_safe "$ENV_FILE"
    load_env_safe "/etc/scarlix/.env"
  fi
  if [ -z "${TELEGRAM_BOT_TOKEN:-}" ] || [ -z "${TELEGRAM_ZMOR_CHAT_ID:-}" ]; then
    log "  (Telegram skipped — no token/chat_id)"
    return 0
  fi
  curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
    -d "chat_id=${TELEGRAM_ZMOR_CHAT_ID}" \
    -d "text=${message}" \
    -d "parse_mode=Markdown" >/dev/null 2>&1 || true
}

vram_snapshot() {
  if ! command -v nvidia-smi >/dev/null 2>&1; then
    echo "N/A"
    return
  fi
  local used total
  used=$(nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits 2>/dev/null | awk '{s+=$1} END {print s+0}')
  total=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>/dev/null | awk '{s+=$1} END {print s+0}')
  echo "${used} / ${total} MiB"
}

log "========================================"
log "  SCARLIX OS v19.0.0 — Model Manager"
[ "$APPLY_OLLAMA" -eq 1 ] && log "  (--apply-ollama: will update Ollama tags)" || log "  (HF auto-pull + Ollama dry-run report only)"
log "========================================"

if [ ! -f "$MODELS_YAML" ]; then
  log "ERROR: models.yaml not found"
  send_telegram "🚨 *SCARLIX Model Manager v${SCARLIX_VER}* — FAILED
models.yaml not found"
  exit 1
fi

# v18.9.0 P2-04: Validate YAML before reading keys
if ! yq -e '.' "$MODELS_YAML" >/dev/null 2>&1; then
    log "ERROR: models.yaml is invalid YAML"
    exit 1
fi

VRAM_BEFORE=$(vram_snapshot)
log "VRAM before: $VRAM_BEFORE"

HF_UPDATED=0
HF_FAILED=0
OLLAMA_UPDATED=0
OLLAMA_FAILED=0
UPDATED_LIST=""

# === HuggingFace model pulls (AUTO — safe, just GGUF files) ===
update_hf_model() {
  local repo="$1"
  local file="$2"
  local target_dir="$3"
  local engine="${4:-}"
  log "Pulling HF: $repo / $file"
  # v19.0.1: was `test -x file >/dev/null 2>&1` — the redirect is meaningless (test
  #   emits no output) and shellcheck SC2065 flags it as a redirection-not-comparison.
  #   Switched to `[ -x ]` for clarity; behavior unchanged.
  if [ -x /opt/scarlix/venv/bin/huggingface-cli ]; then
    # v18.9.0 P1-03: Download to staging, then atomic move to final location
    # (was: --local-dir "$target_dir" → partial/corrupt file in /models on interruption)
    if /opt/scarlix/venv/bin/huggingface-cli download "$repo" "$file" --local-dir "$STAGING_DIR" >> "$LOG_FILE" 2>&1; then
      # v18.9.4 P1-02: Capture mv exit code (was: || true → false success reported)
      # v18.9.4 P1-03: Use .previous rollback pattern (was: direct mv, no rollback)
      local target_file="$target_dir/$file"
      local mv_rc=0
      # If target exists, move to .previous for rollback
      if [ -e "$target_file" ]; then
        mv -f "$target_file" "${target_file}.previous" 2>/dev/null || mv_rc=1
      fi
      if [ "$mv_rc" -eq 0 ]; then
        mv -f "$STAGING_DIR/$file" "$target_file" 2>/dev/null || {
          # Fallback: try moving all staging contents
          mv -f "$STAGING_DIR"/* "$target_dir/" 2>/dev/null || mv_rc=1
        }
      fi
      if [ "$mv_rc" -eq 0 ]; then
        # v18.9.5 P1-06: Keep .previous until runtime health passes (was: rm .previous
        #   before restart → if restart fails, no rollback available. Now: only delete
        #   .previous after successful restart + healthcheck.)
        find "$STAGING_DIR" -mindepth 1 -delete 2>/dev/null || true
        log "  ✓ $file"
        HF_UPDATED=$((HF_UPDATED + 1))
        UPDATED_LIST="${UPDATED_LIST}\n  ✓ ${file} (HF)"
        # v18.9.0 P1-02: Restart runtime after model update
        if [ "$engine" = "beellama" ]; then
          log "Restarting BeeLlama to load updated model..."
          if docker compose --env-file /opt/scarlix/.env -f /opt/scarlix/ai/llamacpp/docker-compose.yml up -d --force-recreate >> "$LOG_FILE" 2>&1; then
            # v18.9.5 P1-06: Runtime restart OK — now safe to delete .previous
            rm -f "${target_file}.previous" 2>/dev/null || true
            log "  ✓ BeeLlama restarted with new model, .previous cleaned up"
          else
            # v18.9.5 P1-06: Restart failed — restore .previous
            log "  ✗ BeeLlama restart failed — restoring previous model"
            [ -e "${target_file}.previous" ] && mv -f "${target_file}.previous" "$target_file" 2>/dev/null || true
            docker compose --env-file /opt/scarlix/.env -f /opt/scarlix/ai/llamacpp/docker-compose.yml up -d --force-recreate >> "$LOG_FILE" 2>&1 || true
            HF_FAILED=$((HF_FAILED + 1))
            HF_UPDATED=$((HF_UPDATED - 1))
          fi
        else
          # v18.9.5 P1-06: Non-beellama — no runtime restart needed, safe to clean up
          rm -f "${target_file}.previous" 2>/dev/null || true
        fi
      else
        # v18.9.4 P1-02: mv failed — restore .previous if available
        log "  ✗ $file — model promotion FAILED, restoring previous"
        [ -e "${target_file}.previous" ] && mv -f "${target_file}.previous" "$target_file" 2>/dev/null || true
        find "$STAGING_DIR" -mindepth 1 -delete 2>/dev/null || true
        HF_FAILED=$((HF_FAILED + 1))
      fi
    else
      log "  ✗ $file"
      HF_FAILED=$((HF_FAILED + 1))
    fi
  else
    log "  ⚠ huggingface-cli not installed — skipping HF"
    HF_FAILED=$((HF_FAILED + 1))
  fi
}

if command -v yq >/dev/null 2>&1; then
  LLAMACPP_REPO=$(yq -r '.beellama.hf_repo // empty' "$MODELS_YAML" 2>/dev/null | grep -v '^$' || true)
  LLAMACPP_FILE=$(yq -r '.beellama.hf_file // empty' "$MODELS_YAML" 2>/dev/null | grep -v '^$' || true)
  if [ -n "$LLAMACPP_REPO" ] && [ -n "$LLAMACPP_FILE" ]; then
    update_hf_model "$LLAMACPP_REPO" "$LLAMACPP_FILE" "/models" "beellama"
  fi
else
  log "⚠ yq not installed — skipping HF updates"
fi

# === Ollama model pulls (MANUAL unless --apply-ollama) ===
# v18.7 P1: Ollama runs in Docker container `ollama-agent` (not as host binary).
# Use `docker exec ollama-agent ollama ...` instead of `command -v ollama`.
if ! command -v docker >/dev/null 2>&1; then
  log "⚠ Docker not installed — skipping Ollama"
  OLLAMA_FAILED=$((OLLAMA_FAILED + 1))
else
  # Check if ollama-agent container exists + is running
  OLLAMA_STATUS=$(docker inspect -f '{{.State.Status}}' ollama-agent 2>/dev/null || echo "not_found")
  if [ "$OLLAMA_STATUS" = "not_found" ]; then
    log "⚠ ollama-agent container not found — skipping Ollama"
    OLLAMA_FAILED=$((OLLAMA_FAILED + 1))
  elif [ "$OLLAMA_STATUS" != "running" ]; then
    # v18.7.2 P1: stopped Ollama is OK (SGLang may be primary) — was: counted as FAILED
    log "  ℹ ollama-agent stopped (status: $OLLAMA_STATUS) — skipping (SGLang primary?)"
    # Don't increment OLLAMA_FAILED — stopped ≠ failed
  else
    OLLAMA_MODEL=$(yq -r '.ollama.model // empty' "$MODELS_YAML" 2>/dev/null | grep -v '^$' || echo "")
    # v18.7.1: fix SC2066 (was: `for model in "$OLLAMA_MODEL"` — double-quoted = no word-split = loop runs once)
    for model in $OLLAMA_MODEL; do
      [ -n "$model" ] || continue
      if [ "$APPLY_OLLAMA" -eq 1 ]; then
        log "Pulling Ollama (via docker exec): $model (--apply-ollama)"
        if docker exec ollama-agent ollama pull "$model" >> "$LOG_FILE" 2>&1; then
          log "  ✓ $model"
          OLLAMA_UPDATED=$((OLLAMA_UPDATED + 1))
          UPDATED_LIST="${UPDATED_LIST}\n  ✓ ${model} (ollama)"
        else
          log "  ✗ $model"
          OLLAMA_FAILED=$((OLLAMA_FAILED + 1))
        fi
      else
        # Dry-run: just report current vs latest (via docker exec)
        CURRENT=$(docker exec ollama-agent ollama list 2>/dev/null | grep "$model" | awk '{print $2}' | head -1 || echo "not installed")
        log "  (dry-run) Ollama $model: current=$CURRENT — use --apply-ollama to update"
      fi
    done
  fi
fi

VRAM_AFTER=$(vram_snapshot)
log "VRAM after: $VRAM_AFTER"
log ""
log "HF: updated=$HF_UPDATED failed=$HF_FAILED"
log "Ollama: updated=$OLLAMA_UPDATED failed=$OLLAMA_FAILED"

# === Telegram summary ===
SUMMARY="🤖 *SCARLIX Model Manager v${SCARLIX_VER}*
📊 HF: \`${HF_UPDATED}\` updated, \`${HF_FAILED}\` failed
📊 Ollama: \`${OLLAMA_UPDATED}\` updated, \`${OLLAMA_FAILED}\` failed
$([ "$APPLY_OLLAMA" -eq 0 ] && echo "ℹ️ Ollama tags NOT updated (dry-run). Use \`model-manager.sh --apply-ollama\` to update.")
💾 VRAM: \`$VRAM_BEFORE\` → \`$VRAM_AFTER\`
$([ -n "$UPDATED_LIST" ] && echo -e "✅ Models:" && echo -e "$UPDATED_LIST")"

TOTAL_FAILED=$((HF_FAILED + OLLAMA_FAILED))
if [ "$TOTAL_FAILED" -gt 0 ]; then
  send_telegram "🚨 $SUMMARY

⚠️ $TOTAL_FAILED failed — check /var/log/scarlix/model-manager.log"
  log "Model Manager FAILED: $TOTAL_FAILED operation(s) failed"
  exit 1
elif [ "$HF_UPDATED" -gt 0 ] || [ "$OLLAMA_UPDATED" -gt 0 ]; then
  send_telegram "$SUMMARY"
else
  log "Telegram: skipped (nothing updated)"
fi

log "========================================"
log "  Model Manager complete."
log "========================================"
exit 0
