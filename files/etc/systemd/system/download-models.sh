#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v16.4 — Model-Agnostic Downloader (with hf_repo support)
MODELS_CONFIG="${MODELS_CONFIG:-/etc/scarlix/models.yaml}"
MODELS_DIR="${MODELS_DIR:-/models}"
LOG_FILE="/var/log/scarlix/model-download.log"

mkdir -p "$(dirname "$LOG_FILE")"

echo "============================================" | tee "$LOG_FILE"
echo "  SCARLIX OS v16.4 — Model Downloader" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"

# Install deps
command -v yq >/dev/null 2>&1 || sudo pacman -S --noconfirm yq
command -v huggingface-cli >/dev/null 2>&1 || pip install huggingface_hub

TOTAL_STEPS=7
CURRENT_STEP=0
progress() {
  CURRENT_STEP=$((CURRENT_STEP + 1))
  local pct=$((CURRENT_STEP * 100 / TOTAL_STEPS))
  echo "" | tee -a "$LOG_FILE"
  echo "[$CURRENT_STEP/$TOTAL_STEPS] ($pct%) $1" | tee -a "$LOG_FILE"
}

# === Step 1: SGLang model (safetensors) ===
progress "SGLang model (safetensors)"
SGLANG_PATH=$(yq '.sglang.model_path' "$MODELS_CONFIG")
SGLANG_NAME=$(basename "$SGLANG_PATH")
if [ -d "$MODELS_DIR/$SGLANG_NAME" ]; then
  echo "  ✓ Already exists: $SGLANG_NAME" | tee -a "$LOG_FILE"
else
  echo "  Downloading: $SGLANG_PATH" | tee -a "$LOG_FILE"
  huggingface-cli download "$SGLANG_PATH" --local-dir "$MODELS_DIR/$SGLANG_NAME"
  echo "  ✓ Downloaded" | tee -a "$LOG_FILE"
fi

# === Step 2: Ollama main model ===
progress "Ollama main model (GGUF)"
OLLAMA_MAIN=$(yq '.ollama_main.model' "$MODELS_CONFIG")
if docker exec ollama-main ollama list 2>/dev/null | grep -q "$OLLAMA_MAIN"; then
  echo "  ✓ Already pulled: $OLLAMA_MAIN" | tee -a "$LOG_FILE"
else
  echo "  Pulling: $OLLAMA_MAIN" | tee -a "$LOG_FILE"
  docker exec ollama-main ollama pull "$OLLAMA_MAIN" 2>/dev/null && echo "  ✓ Pulled" | tee -a "$LOG_FILE" || echo "  ⚠ Pull later" | tee -a "$LOG_FILE"
fi

# === Step 3: Ollama agent model ===
progress "Ollama agent model (GGUF)"
OLLAMA_AGENT=$(yq '.ollama_agent.model' "$MODELS_CONFIG")
if docker exec ollama-agent ollama list 2>/dev/null | grep -q "$OLLAMA_AGENT"; then
  echo "  ✓ Already pulled: $OLLAMA_AGENT" | tee -a "$LOG_FILE"
else
  echo "  Pulling: $OLLAMA_AGENT" | tee -a "$LOG_FILE"
  docker exec ollama-agent ollama pull "$OLLAMA_AGENT" 2>/dev/null && echo "  ✓ Pulled" | tee -a "$LOG_FILE" || echo "  ⚠ Pull later" | tee -a "$LOG_FILE"
fi

# === Step 4: Embeddings ===
progress "Embeddings model"
EMBED_MODEL=$(yq '.embeddings.model' "$MODELS_CONFIG")
docker exec ollama-main ollama pull "$EMBED_MODEL" 2>/dev/null && echo "  ✓ Pulled: $EMBED_MODEL" | tee -a "$LOG_FILE" || echo "  ⚠ Pull later" | tee -a "$LOG_FILE"

# === Step 5: llama.cpp GGUF (FIX 8-b: use hf_repo + hf_file) ===
progress "llama.cpp GGUF model (CPU fallback)"
LLAMACPP_FILE=$(yq '.llamacpp.model_file' "$MODELS_CONFIG")
if [ -f "$LLAMACPP_FILE" ]; then
  echo "  ✓ Already exists: $(basename "$LLAMACPP_FILE")" | tee -a "$LOG_FILE"
else
  # FIX 8-b: Use hf_repo + hf_file if available
  HF_REPO=$(yq '.llamacpp.hf_repo' "$MODELS_CONFIG")
  HF_FILE=$(yq '.llamacpp.hf_file' "$MODELS_CONFIG")
  if [ "$HF_REPO" != "null" ] && [ "$HF_FILE" != "null" ]; then
    echo "  Downloading via hf_repo: $HF_REPO / $HF_FILE" | tee -a "$LOG_FILE"
    huggingface-cli download "$HF_REPO" --include "$HF_FILE" --local-dir "$MODELS_DIR/"
    echo "  ✓ Downloaded: $HF_FILE" | tee -a "$LOG_FILE"
  else
    # Fallback: parse from model_file path
    GGUF_REPO=$(dirname "$LLAMACPP_FILE" | sed 's|/models/||')
    GGUF_NAME=$(basename "$LLAMACPP_FILE")
    echo "  Downloading: $GGUF_REPO / $GGUF_NAME" | tee -a "$LOG_FILE"
    huggingface-cli download "$GGUF_REPO" --include "$GGUF_NAME" --local-dir "$MODELS_DIR/"
    echo "  ✓ Downloaded" | tee -a "$LOG_FILE"
  fi
fi

# === Step 6: Needle2 ===
progress "Needle2 router (26M params)"
if [ -d "$MODELS_DIR/needle-router-v1" ]; then
  echo "  ✓ Already exists" | tee -a "$LOG_FILE"
else
  echo "  Downloading..." | tee -a "$LOG_FILE"
  huggingface-cli download Cactus-Compute/needle-router-v1 --local-dir "$MODELS_DIR/needle-router-v1"
  echo "  ✓ Downloaded" | tee -a "$LOG_FILE"
fi

# === Step 7: ComfyUI checkpoint ===
progress "ComfyUI checkpoint (creative mode)"
COMFY_CKPT=$(yq '.comfyui.checkpoint' "$MODELS_CONFIG")
COMFY_DIR="/opt/scarlix/ai/comfyui/data/models/checkpoints"
if [ -f "$COMFY_DIR/$COMFY_CKPT" ]; then
  echo "  ✓ Already exists: $COMFY_CKPT" | tee -a "$LOG_FILE"
else
  echo "  Downloading: $COMFY_CKPT" | tee -a "$LOG_FILE"
  huggingface-cli download Kijai/flux-fp8 --include "$COMFY_CKPT" --local-dir "$COMFY_DIR" 2>/dev/null && echo "  ✓ Downloaded" | tee -a "$LOG_FILE" || echo "  ⚠ Download manually" | tee -a "$LOG_FILE"
fi

echo "" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"
echo "  ✅ Model download complete!" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"
echo "Models in $MODELS_DIR:" | tee -a "$LOG_FILE"
ls -lh "$MODELS_DIR" 2>/dev/null | head -20 | tee -a "$LOG_FILE"
