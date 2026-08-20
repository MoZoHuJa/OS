#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v16.5 — Model Manager
# FIX 6-b: Weekly model updater with VRAM check + Telegram notifications
#
# Scope (chosen: 6-b = full):
#   1. Pull latest model versions from HuggingFace (based on models.yaml)
#   2. Check VRAM usage before/after update
#   3. Send Telegram summary (success/fail + VRAM report)
#
# Invoked weekly by model-manager.timer (Mondays 04:00 — low-traffic window).
# Can also be run manually: model-manager.sh [--dry-run]

LOG_FILE="/var/log/scarlix/model-manager.log"
MODELS_YAML="/etc/scarlix/models.yaml"
ENV_FILE="/etc/scarlix/.env"
DRY_RUN=0
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=1

mkdir -p "$(dirname "$LOG_FILE")"

log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

# --- Telegram helper -------------------------------------------------------
send_telegram() {
  local message="$1"
  # Load .env for token + chat ID
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

# --- VRAM snapshot ---------------------------------------------------------
vram_snapshot() {
  # Returns: "USED_MIB / TOTAL_MIB"
  if ! command -v nvidia-smi >/dev/null 2>&1; then
    echo "N/A (nvidia-smi missing)"
    return
  fi
  local used free total
  used=$(nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits 2>/dev/null | awk '{s+=$1} END {print s+0}')
  total=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>/dev/null | awk '{s+=$1} END {print s+0}')
  echo "${used} / ${total} MiB"
}

# --- Main ------------------------------------------------------------------
log "========================================"
log "  SCARLIX OS v16.5 — Model Manager"
[ "$DRY_RUN" -eq 1 ] && log "  (DRY RUN — no changes will be made)"
log "========================================"

if [ ! -f "$MODELS_YAML" ]; then
  log "ERROR: models.yaml not found at $MODELS_YAML"
  send_telegram "🚨 *SCARLIX Model Manager* — FAILED
models.yaml not found: \`$MODELS_YAML\`"
  exit 1
fi

VRAM_BEFORE=$(vram_snapshot)
log "VRAM before update: $VRAM_BEFORE"

# --- Update HuggingFace models --------------------------------------------
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

# --- Update Ollama models (pull latest tag) -------------------------------
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

# --- Telegram summary -----------------------------------------------------
SUMMARY="🤖 *SCARLIX Model Manager* — v16.5
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
