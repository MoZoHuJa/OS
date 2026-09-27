#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v17.0 — EndeavourOS Edition — Model Manager
# Weekly model updater with VRAM check + Telegram notifications
#
# v17.0: functionally identical to v16.5 (HF + Ollama + VRAM + Telegram)
# Only version string updated for branding consistency.
#
# Invoked weekly by model-manager.timer (Mondays 04:00 — low-traffic window).
# Manual: model-manager.sh [--dry-run]

LOG_FILE="/var/log/scarlix/model-manager.log"
MODELS_YAML="/etc/scarlix/models.yaml"
ENV_FILE="/etc/scarlix/.env"
DRY_RUN=0
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=1

mkdir -p "$(dirname "$LOG_FILE")"

log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

send_telegram() {
  local message="$1"
  if [ -f "$ENV_FILE" ]; then
    # shellcheck disable=SC1090
    set -a; source "$ENV_FILE"; set +a
  fi
  if [ -z "${TELEGRAM_BOT_TOKEN:-}" ] || [ -z "${TELEGRAM_ZMOR_CHAT_ID:-}" ]; then
    log "  (Telegram skipped — no token/chat_id configured)"
    return 0
  fi
  curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
    -d "chat_id=${TELEGRAM_ZMOR_CHAT_ID}" \
    -d "text=${message}" \
    -d "parse_mode=Markdown" >/dev/null 2>&1 || true
}

vram_snapshot() {
  if ! command -v nvidia-smi >/dev/null 2>&1; then
    echo "N/A (nvidia-smi missing)"
    return
  fi
  local used free total
  used=$(nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits 2>/dev/null | awk '{s+=$1} END {print s+0}')
  total=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>/dev/null | awk '{s+=$1} END {print s+0}')
  echo "${used} / ${total} MiB"
}

log "========================================"
log "  SCARLIX OS v17.0 — Model Manager"
[ "$DRY_RUN" -eq 1 ] && log "  (DRY RUN — no changes will be made)"
log "========================================"

if [ ! -f "$MODELS_YAML" ]; then
  log "ERROR: models.yaml not found at $MODELS_YAML"
  send_telegram "🚨 *SCARLIX Model Manager v17* — FAILED
models.yaml not found: \`$MODELS_YAML\`"
  exit 1
fi

VRAM_BEFORE=$(vram_snapshot)
log "VRAM before update: $VRAM_BEFORE"

UPDATED_COUNT=0
FAILED_COUNT=0
UPDATED_LIST=""

update_hf_model() {
  local repo="$1"
  local file="$2"
  local target_dir="$3"
  log "Pulling: $repo / $file"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "  (dry-run) would download $file from $repo"
    return 0
  fi
  if command -v huggingface-cli >/dev/null 2>&1; then
    if huggingface-cli download "$repo" "$file" --local-dir "$target_dir" >> "$LOG_FILE" 2>&1; then
      log "  ✓ $file"
      UPDATED_COUNT=$((UPDATED_COUNT + 1))
      UPDATED_LIST="${UPDATED_LIST}\n  ✓ ${file}"
    else
      log "  ✗ $file (huggingface-cli failed)"
      FAILED_COUNT=$((FAILED_COUNT + 1))
    fi
  else
    log "  ⚠ huggingface-cli not installed — skipping"
    FAILED_COUNT=$((FAILED_COUNT + 1))
  fi
}

# Parse models.yaml for hf_repo/hf_file pairs (llama.cpp section)
if command -v yq >/dev/null 2>&1; then
  LLAMACPP_REPO=$(yq '.llamacpp.hf_repo' "$MODELS_YAML" 2>/dev/null | grep -v '^$' || true)
  LLAMACPP_FILE=$(yq '.llamacpp.hf_file' "$MODELS_YAML" 2>/dev/null | grep -v '^$' || true)
  if [ -n "$LLAMACPP_REPO" ] && [ -n "$LLAMACPP_FILE" ]; then
    update_hf_model "$LLAMACPP_REPO" "$LLAMACPP_FILE" "/models"
  fi
else
  log "⚠ yq not installed — skipping HF model updates (install: sudo pacman -S yq)"
fi

# Update Ollama models (pull latest tag)
if command -v ollama >/dev/null 2>&1; then
  OLLAMA_MAIN_MODEL=$(yq '.ollama_main.model' "$MODELS_YAML" 2>/dev/null | grep -v '^$' || echo "")
  OLLAMA_AGENT_MODEL=$(yq '.ollama_agent.model' "$MODELS_YAML" 2>/dev/null | grep -v '^$' || echo "")
  for model in "$OLLAMA_MAIN_MODEL" "$OLLAMA_AGENT_MODEL"; do
    if [ -n "$model" ]; then
      log "Pulling Ollama model: $model"
      if [ "$DRY_RUN" -eq 1 ]; then
        log "  (dry-run) would run: ollama pull $model"
      else
        if ollama pull "$model" >> "$LOG_FILE" 2>&1; then
          log "  ✓ $model"
          UPDATED_COUNT=$((UPDATED_COUNT + 1))
          UPDATED_LIST="${UPDATED_LIST}\n  ✓ ${model} (ollama)"
        else
          log "  ✗ $model"
          FAILED_COUNT=$((FAILED_COUNT + 1))
        fi
      fi
    fi
  done
else
  log "⚠ ollama CLI not installed — skipping Ollama model updates"
fi

VRAM_AFTER=$(vram_snapshot)
log "VRAM after update:  $VRAM_AFTER"
log ""
log "Updated: $UPDATED_COUNT  Failed: $FAILED_COUNT"

SUMMARY="🤖 *SCARLIX Model Manager v17* — EndeavourOS
📊 Updated: \`${UPDATED_COUNT}\`  |  Failed: \`${FAILED_COUNT}\`
💾 VRAM before: \`$VRAM_BEFORE\`
💾 VRAM after:  \`$VRAM_AFTER\`
$( [ "$DRY_RUN" -eq 1 ] && echo "ℹ️ DRY RUN — no changes made" )
$([ -n "$UPDATED_LIST" ] && echo -e "✅ Models:" && echo -e "$UPDATED_LIST")"

if [ "$FAILED_COUNT" -gt 0 ]; then
  send_telegram "🚨 $SUMMARY

⚠️ $FAILED_COUNT model(s) failed — check /var/log/scarlix/model-manager.log"
  log "Telegram: sent (with failures)"
elif [ "$UPDATED_COUNT" -gt 0 ]; then
  send_telegram "$SUMMARY"
  log "Telegram: sent"
else
  log "Telegram: skipped (nothing updated)"
fi

log "========================================"
log "  Model Manager complete."
log "========================================"
exit 0
