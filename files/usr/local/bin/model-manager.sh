#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v18.3 — Model Manager
# v18.3 FIX: Ollama pulls via `docker exec ollama-agent` (was: host `ollama` binary — never installed)
# FIX Q8b: Split — HF model pulls = auto (safe), Ollama tag pulls = manual (--apply only)
#
# Weekly timer (Mon 04:00) runs with NO --apply → only HF model pulls + Telegram report.
# Ollama tag updates require manual: model-manager.sh --apply-ollama
# (Because Ollama updates can break CUDA — e.g. 0.12.4 regression.)

LOG_FILE="/var/log/scarlix/model-manager.log"
MODELS_YAML="/etc/scarlix/models.yaml"
ENV_FILE="/opt/scarlix/.env"
APPLY_OLLAMA=0
[[ "${1:-}" == "--apply-ollama" ]] && APPLY_OLLAMA=1

mkdir -p "$(dirname "$LOG_FILE")"

log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

send_telegram() {
  local message="$1"
  if [ -f "$ENV_FILE" ]; then
    # P1 v17.9.5: Source both .env files — /opt for model paths, /etc for secrets/Telegram
    set -a; source "$ENV_FILE" 2>/dev/null || true; source "/etc/scarlix/.env" 2>/dev/null || true; set +a
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
log "  SCARLIX OS v18.3 — Model Manager"
[ "$APPLY_OLLAMA" -eq 1 ] && log "  (--apply-ollama: will update Ollama tags)" || log "  (HF auto-pull + Ollama dry-run report only)"
log "========================================"

if [ ! -f "$MODELS_YAML" ]; then
  log "ERROR: models.yaml not found"
  send_telegram "🚨 *SCARLIX Model Manager v18.3* — FAILED
models.yaml not found"
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
  log "Pulling HF: $repo / $file"
  if test -x /opt/scarlix/venv/bin/huggingface-cli >/dev/null 2>&1; then
    if /opt/scarlix/venv/bin/huggingface-cli download "$repo" "$file" --local-dir "$target_dir" >> "$LOG_FILE" 2>&1; then
      log "  ✓ $file"
      HF_UPDATED=$((HF_UPDATED + 1))
      UPDATED_LIST="${UPDATED_LIST}\n  ✓ ${file} (HF)"
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
  LLAMACPP_REPO=$(yq '.beellama.hf_repo' "$MODELS_YAML" 2>/dev/null | grep -v '^$' || true)
  LLAMACPP_FILE=$(yq '.beellama.hf_file' "$MODELS_YAML" 2>/dev/null | grep -v '^$' || true)
  if [ -n "$LLAMACPP_REPO" ] && [ -n "$LLAMACPP_FILE" ]; then
    update_hf_model "$LLAMACPP_REPO" "$LLAMACPP_FILE" "/models"
  fi
else
  log "⚠ yq not installed — skipping HF updates"
fi

# === Ollama model pulls (MANUAL unless --apply-ollama) ===
# v18.3 P1: Ollama runs in Docker container `ollama-agent` (not as host binary).
# Use `docker exec ollama-agent ollama ...` instead of `command -v ollama`.
if ! command -v docker >/dev/null 2>&1; then
  log "⚠ Docker not installed — skipping Ollama"
  OLLAMA_FAILED=$((OLLAMA_FAILED + 1))
else
  # Check if ollama-agent container exists + is running
  OLLAMA_STATUS=$(docker inspect -f '{{.State.Status}}' ollama-agent 2>/dev/null || echo "not_found")
  if [ "$OLLAMA_STATUS" != "running" ]; then
    log "⚠ ollama-agent container not running (status: $OLLAMA_STATUS) — skipping Ollama"
    OLLAMA_FAILED=$((OLLAMA_FAILED + 1))
  else
    OLLAMA_MODEL=$(yq '.ollama.model' "$MODELS_YAML" 2>/dev/null | grep -v '^$' || echo "")
    for model in "$OLLAMA_MODEL"; do
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
SUMMARY="🤖 *SCARLIX Model Manager v18.3*
📊 HF: \`${HF_UPDATED}\` updated, \`${HF_FAILED}\` failed
📊 Ollama: \`${OLLAMA_UPDATED}\` updated, \`${OLLAMA_FAILED}\` failed
$([ "$APPLY_OLLAMA" -eq 0 ] && echo "ℹ️ Ollama tags NOT updated (dry-run). Use \`model-manager.sh --apply-ollama\` to update.")
💾 VRAM: \`$VRAM_BEFORE\` → \`$VRAM_AFTER\`
$([ -n "$UPDATED_LIST" ] && echo -e "✅ Models:" && echo -e "$UPDATED_LIST")"

TOTAL_FAILED=$((HF_FAILED + OLLAMA_FAILED))
if [ "$TOTAL_FAILED" -gt 0 ]; then
  send_telegram "🚨 $SUMMARY

⚠️ $TOTAL_FAILED failed — check /var/log/scarlix/model-manager.log"
elif [ "$HF_UPDATED" -gt 0 ] || [ "$OLLAMA_UPDATED" -gt 0 ]; then
  send_telegram "$SUMMARY"
else
  log "Telegram: skipped (nothing updated)"
fi

log "========================================"
log "  Model Manager complete."
log "========================================"
exit 0
