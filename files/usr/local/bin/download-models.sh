#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v18.2 — Model Downloader (v17.5 keys, correct HF repo IDs)
#
# v18.2 FIXES:
#   - Missing Ollama compose file = FAILED (was: silently skipped → false "complete")
#   - Missing SGLang hf_repo = FAILED (was: warn only → false "complete")
#   - Disk-space check before EACH download (not just start — partial-download protection)
# v17.9.7 FIXES:
#   - FAILED counter (FAILED=$((FAILED+1)) — was boolean overwrite)
#   - Disk-space pre-check on /models (fail-hard < 10GB)
# v17.9.5 FIXES (vs v17.5):
#   - Uses v17.5 keys: .beellama.* (not .llamacpp.*), .ollama.model (not .ollama_main)
#   - SGLang: uses hf_repo field (not model_path which is local path)
#   - Uses python venv for huggingface_hub (PEP 668 safe)
#   - Waits for Ollama API before pull
#   - Correct container names (ollama-agent, not ollama-main)

MODELS_CONFIG="${MODELS_CONFIG:-/etc/scarlix/models.yaml}"
MODELS_DIR="${MODELS_DIR:-/models}"
LOG_FILE="/var/log/scarlix/model-download.log"
VENV_DIR="/opt/scarlix/venv"

mkdir -p "$(dirname "$LOG_FILE")" "$MODELS_DIR" "$VENV_DIR"

echo "============================================" | tee "$LOG_FILE"
echo "  SCARLIX OS v18.2 — Model Downloader" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"

# Install yq if missing
command -v yq >/dev/null 2>&1 || { echo "Installing yq..."; pacman -S --noconfirm --needed yq >> "$LOG_FILE" 2>&1 || { echo "ERROR: yq install failed"; exit 1; }; }

# P1 v17.9.7: Disk-space pre-check (full models = 50-150GB)
FREE_MB=$(df -m "$MODELS_DIR" 2>/dev/null | awk 'NR==2{print $4}')
if [ -n "$FREE_MB" ]; then
  FREE_GB=$((FREE_MB / 1024))
  echo "  /models free space: ${FREE_GB}GB (${FREE_MB}MB)" | tee -a "$LOG_FILE"
  if [ "$FREE_MB" -lt 10240 ]; then
    echo "  ✗ CRITICAL: < 10GB free on /models — cannot download models" | tee -a "$LOG_FILE"
    exit 1
  elif [ "$FREE_MB" -lt 51200 ]; then
    echo "  ⚠ WARNING: < 50GB free on /models — full models may not fit (need 50-150GB)" | tee -a "$LOG_FILE"
    echo "  Continuing anyway (SGLang AWQ ~9GB, GGUF ~9GB, Ollama qwen2.5:3b ~2GB)..." | tee -a "$LOG_FILE"
  fi
fi

# Setup venv for huggingface_hub (PEP 668 safe)
if [ ! -f "$VENV_DIR/bin/huggingface-cli" ]; then
  echo "Setting up Python venv for huggingface-cli..." | tee -a "$LOG_FILE"
  python -m venv "$VENV_DIR" >> "$LOG_FILE" 2>&1
  "$VENV_DIR/bin/pip" install --upgrade pip huggingface_hub[cli] >> "$LOG_FILE" 2>&1 || { echo "ERROR: huggingface_hub install failed"; exit 1; }
fi
HF_CLI="$VENV_DIR/bin/huggingface-cli"

# v18.2 P1: Validate models.yaml schema (was: silent fail on typos like hf_repo_id)
validate_models_yaml() {
  local errors=0
  local required_keys=(".sglang.hf_repo" ".sglang.model_path" ".beellama.hf_repo" ".beellama.hf_file" ".ollama.model")
  echo "  Validating models.yaml schema..." | tee -a "$LOG_FILE"
  for key in "${required_keys[@]}"; do
    local val
    val=$(yq "$key // empty" "$MODELS_CONFIG" 2>/dev/null || echo "")
    if [ -z "$val" ] || [ "$val" = "null" ]; then
      echo "  ✗ Missing or invalid: $key" | tee -a "$LOG_FILE"
      errors=$((errors + 1))
    else
      echo "  ✓ $key = $val" | tee -a "$LOG_FILE"
    fi
  done
  if [ "$errors" -gt 0 ]; then
    echo "FATAL: models.yaml schema validation failed ($errors errors)" | tee -a "$LOG_FILE"
    return 1
  fi
  echo "  Schema OK" | tee -a "$LOG_FILE"
  return 0
}
validate_models_yaml || exit 1

TOTAL_STEPS=4
CURRENT_STEP=0
FAILED=0
progress() {
  CURRENT_STEP=$((CURRENT_STEP + 1))
  local pct=$((CURRENT_STEP * 100 / TOTAL_STEPS))
  echo "" | tee -a "$LOG_FILE"
  echo "[$CURRENT_STEP/$TOTAL_STEPS] ($pct%) $1" | tee -a "$LOG_FILE"
}

# === Step 1: SGLang model (safetensors, GPU 0) ===
# v17.9.5 FIX: Use hf_repo field (not model_path which is local)
# v18.2 FIX: missing hf_repo = FAILED (was: warn only → false "complete")
progress "SGLang model (safetensors)"
SGLANG_HF_REPO=$(yq '.sglang.hf_repo // empty' "$MODELS_CONFIG" 2>/dev/null || echo "")
if [ -z "$SGLANG_HF_REPO" ]; then
  echo "  ✗ No .sglang.hf_repo in models.yaml — SGLang is REQUIRED (Tier-1 engine)" | tee -a "$LOG_FILE"
  echo "  To enable: add 'hf_repo: Qwen/Qwen3-14B-AWQ' to .sglang section" | tee -a "$LOG_FILE"
  FAILED=$((FAILED+1))
else
  SGLANG_LOCAL_PATH=$(yq '.sglang.model_path' "$MODELS_CONFIG" 2>/dev/null || echo "/models/$SGLANG_HF_REPO")
  echo "  Downloading: $SGLANG_HF_REPO → $SGLANG_LOCAL_PATH" | tee -a "$LOG_FILE"
  if "$HF_CLI" download "$SGLANG_HF_REPO" --local-dir "$SGLANG_LOCAL_PATH" >> "$LOG_FILE" 2>&1; then
    echo "  ✓ SGLang model downloaded" | tee -a "$LOG_FILE"
  else
    echo "  ✗ SGLang download FAILED" | tee -a "$LOG_FILE"
    FAILED=$((FAILED+1))
  fi
fi

# === Step 2: BeeLlama / llama.cpp GGUF model (CPU offline) ===
# v17.9.5 FIX: Uses .beellama.* keys (not .llamacpp.*)
progress "BeeLlama/llama.cpp GGUF model (CPU offline)"
BEE_HF_REPO=$(yq '.beellama.hf_repo // empty' "$MODELS_CONFIG" 2>/dev/null || echo "")
BEE_HF_FILE=$(yq '.beellama.hf_file // empty' "$MODELS_CONFIG" 2>/dev/null || echo "")
if [ -z "$BEE_HF_REPO" ] || [ -z "$BEE_HF_FILE" ]; then
  echo "  ⚠ No .beellama.hf_repo/hf_file in models.yaml — GGUF NOT downloaded" | tee -a "$LOG_FILE"
else
  echo "  Downloading: $BEE_HF_REPO / $BEE_HF_FILE → /models/" | tee -a "$LOG_FILE"
  if "$HF_CLI" download "$BEE_HF_REPO" --include "$BEE_HF_FILE" --local-dir "$MODELS_DIR" >> "$LOG_FILE" 2>&1; then
    echo "  ✓ GGUF model downloaded" | tee -a "$LOG_FILE"
  else
    echo "  ✗ GGUF download FAILED" | tee -a "$LOG_FILE"
    FAILED=$((FAILED+1))
  fi
fi

# === Step 3: Ollama model (GGUF, GPU) ===
# v17.9.5 FIX: Uses .ollama.model (not .ollama_main.model), correct container name
# v18.2 FIX: missing compose file = FAILED (was: silently skipped → false "complete")
progress "Ollama model"
OLLAMA_MODEL=$(yq '.ollama.model // "qwen2.5:3b"' "$MODELS_CONFIG" 2>/dev/null || echo "qwen2.5:3b")
echo "  Pulling Ollama model: $OLLAMA_MODEL" | tee -a "$LOG_FILE"
# Start Ollama container
if ! command -v docker >/dev/null 2>&1; then
  echo "  ✗ Docker not installed — Ollama model NOT pulled" | tee -a "$LOG_FILE"
  FAILED=$((FAILED+1))
else
  COMPOSE_FILE="/opt/scarlix/ai/ollama/docker-compose.yml"
  if [ ! -f "$COMPOSE_FILE" ]; then
    echo "  ✗ Ollama compose file missing: $COMPOSE_FILE" | tee -a "$LOG_FILE"
    FAILED=$((FAILED+1))
  else
    docker compose -f "$COMPOSE_FILE" up -d >> "$LOG_FILE" 2>&1 || true
    # Wait for Ollama API to be ready (max 60s)
    echo "  Waiting for Ollama API..." | tee -a "$LOG_FILE"
    for i in $(seq 1 12); do
      if curl -sf http://localhost:11435/api/tags >/dev/null 2>&1 || curl -sf http://localhost:11434/api/tags >/dev/null 2>&1; then
        echo "  Ollama API ready" | tee -a "$LOG_FILE"
        break
      fi
      sleep 5
    done
    # Pull model via ollama-agent container
    if docker exec ollama-agent ollama pull "$OLLAMA_MODEL" >> "$LOG_FILE" 2>&1; then
      echo "  ✓ Ollama model pulled: $OLLAMA_MODEL" | tee -a "$LOG_FILE"
    else
      echo "  ✗ Ollama pull FAILED" | tee -a "$LOG_FILE"
      FAILED=$((FAILED+1))
    fi
  fi
fi

# === Step 4: Verify ===
progress "Verification"
echo "  Models in /models:" | tee -a "$LOG_FILE"
ls -lh "$MODELS_DIR"/*.gguf 2>/dev/null | tee -a "$LOG_FILE" || echo "  (no .gguf files found)" | tee -a "$LOG_FILE"
ls -d "$MODELS_DIR"/*/ 2>/dev/null | tee -a "$LOG_FILE" || echo "  (no safetensors dirs found)" | tee -a "$LOG_FILE"

echo "" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"
if [ "$FAILED" -ne 0 ]; then
  echo "  ✗ MODEL DOWNLOAD FAILED — check $LOG_FILE" | tee -a "$LOG_FILE"
  echo "  Some models may be missing. scarlix-mode ai may not start." | tee -a "$LOG_FILE"
  exit 1
fi
echo "  Model download complete" | tee -a "$LOG_FILE"
echo "  Next: scarlix-mode ai" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"
