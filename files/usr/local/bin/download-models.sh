#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v17.5.1 — Model Downloader (v17.5 keys, correct HF repo IDs)
#
# v17.5.1 FIXES (vs v17.5):
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
echo "  SCARLIX OS v17.5.1 — Model Downloader" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"

# Install yq if missing
command -v yq >/dev/null 2>&1 || { echo "Installing yq..."; pacman -S --noconfirm --needed yq >> "$LOG_FILE" 2>&1 || { echo "ERROR: yq install failed"; exit 1; }; }

# Setup venv for huggingface_hub (PEP 668 safe)
if [ ! -f "$VENV_DIR/bin/huggingface-cli" ]; then
  echo "Setting up Python venv for huggingface-cli..." | tee -a "$LOG_FILE"
  python -m venv "$VENV_DIR" >> "$LOG_FILE" 2>&1
  "$VENV_DIR/bin/pip" install --upgrade pip huggingface_hub[cli] >> "$LOG_FILE" 2>&1 || { echo "ERROR: huggingface_hub install failed"; exit 1; }
fi
HF_CLI="$VENV_DIR/bin/huggingface-cli"

TOTAL_STEPS=4
CURRENT_STEP=0
progress() {
  CURRENT_STEP=$((CURRENT_STEP + 1))
  local pct=$((CURRENT_STEP * 100 / TOTAL_STEPS))
  echo "" | tee -a "$LOG_FILE"
  echo "[$CURRENT_STEP/$TOTAL_STEPS] ($pct%) $1" | tee -a "$LOG_FILE"
}

# === Step 1: SGLang model (safetensors, GPU 0) ===
# v17.5.1 FIX: Use hf_repo field (not model_path which is local)
progress "SGLang model (safetensors)"
SGLANG_HF_REPO=$(yq '.sglang.hf_repo // empty' "$MODELS_CONFIG" 2>/dev/null || echo "")
if [ -z "$SGLANG_HF_REPO" ]; then
  echo "  ⚠ No .sglang.hf_repo in models.yaml — SGLang model NOT downloaded" | tee -a "$LOG_FILE"
  echo "  To enable: add 'hf_repo: Qwen/Qwen3-14B-Instruct-AWQ' to .sglang section" | tee -a "$LOG_FILE"
else
  SGLANG_LOCAL_PATH=$(yq '.sglang.model_path' "$MODELS_CONFIG" 2>/dev/null || echo "/models/$SGLANG_HF_REPO")
  echo "  Downloading: $SGLANG_HF_REPO → $SGLANG_LOCAL_PATH" | tee -a "$LOG_FILE"
  "$HF_CLI" download "$SGLANG_HF_REPO" --local-dir "$SGLANG_LOCAL_PATH" >> "$LOG_FILE" 2>&1 && echo "  ✓ SGLang model downloaded" | tee -a "$LOG_FILE" || echo "  ✗ SGLang download failed" | tee -a "$LOG_FILE"
fi

# === Step 2: BeeLlama / llama.cpp GGUF model (CPU offline) ===
# v17.5.1 FIX: Uses .beellama.* keys (not .llamacpp.*)
progress "BeeLlama/llama.cpp GGUF model (CPU offline)"
BEE_HF_REPO=$(yq '.beellama.hf_repo // empty' "$MODELS_CONFIG" 2>/dev/null || echo "")
BEE_HF_FILE=$(yq '.beellama.hf_file // empty' "$MODELS_CONFIG" 2>/dev/null || echo "")
if [ -z "$BEE_HF_REPO" ] || [ -z "$BEE_HF_FILE" ]; then
  echo "  ⚠ No .beellama.hf_repo/hf_file in models.yaml — GGUF NOT downloaded" | tee -a "$LOG_FILE"
else
  echo "  Downloading: $BEE_HF_REPO / $BEE_HF_FILE → /models/" | tee -a "$LOG_FILE"
  "$HF_CLI" download "$BEE_HF_REPO" "$BEE_HF_FILE" --local-dir "$MODELS_DIR" >> "$LOG_FILE" 2>&1 && echo "  ✓ GGUF model downloaded" | tee -a "$LOG_FILE" || echo "  ✗ GGUF download failed" | tee -a "$LOG_FILE"
fi

# === Step 3: Ollama model (GGUF, GPU) ===
# v17.5.1 FIX: Uses .ollama.model (not .ollama_main.model), correct container name
progress "Ollama model"
OLLAMA_MODEL=$(yq '.ollama.model // "qwen2.5:3b"' "$MODELS_CONFIG" 2>/dev/null || echo "qwen2.5:3b")
echo "  Pulling Ollama model: $OLLAMA_MODEL" | tee -a "$LOG_FILE"
# Start Ollama container
if command -v docker >/dev/null 2>&1; then
  COMPOSE_FILE="/opt/scarlix/ai/ollama/docker-compose.yml"
  if [ -f "$COMPOSE_FILE" ]; then
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
      echo "  ✗ Ollama pull failed (try: docker exec ollama-agent ollama pull $OLLAMA_MODEL)" | tee -a "$LOG_FILE"
    fi
  fi
else
  echo "  ⚠ Docker not running — Ollama model NOT pulled" | tee -a "$LOG_FILE"
fi

# === Step 4: Verify ===
progress "Verification"
echo "  Models in /models:" | tee -a "$LOG_FILE"
ls -lh "$MODELS_DIR"/*.gguf 2>/dev/null | tee -a "$LOG_FILE" || echo "  (no .gguf files found)" | tee -a "$LOG_FILE"
ls -d "$MODELS_DIR"/*/ 2>/dev/null | tee -a "$LOG_FILE" || echo "  (no safetensors dirs found)" | tee -a "$LOG_FILE"

echo "" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"
echo "  Model download complete" | tee -a "$LOG_FILE"
echo "  Next: scarlix-mode ai" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"
