#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v16.2 — Model-Agnostic Downloader (rewritten from scratch)
# Reads /etc/scarlix/models.yaml and downloads any HuggingFace model
# Shows progress bars

MODELS_CONFIG="${MODELS_CONFIG:-/etc/scarlix/models.yaml}"
MODELS_DIR="${MODELS_DIR:-/models}"

echo "============================================"
echo "  SCARLIX OS v16.2 — Model Downloader"
echo "============================================"
echo ""

# Install yq if missing
if ! command -v yq >/dev/null 2>&1; then
  echo "Installing yq..."
  if command -v pacman >/dev/null 2>&1; then
    sudo pacman -S --noconfirm yq
  else
    sudo wget -qO /usr/local/bin/yq https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64
    sudo chmod +x /usr/local/bin/yq
  fi
fi

# Install huggingface-cli if missing
if ! command -v huggingface-cli >/dev/null 2>&1; then
  echo "Installing huggingface-cli..."
  pip install huggingface_hub
fi

TOTAL_STEPS=7
CURRENT_STEP=0
progress() {
  CURRENT_STEP=$((CURRENT_STEP + 1))
  local pct=$((CURRENT_STEP * 100 / TOTAL_STEPS))
  echo ""
  echo "[$CURRENT_STEP/$TOTAL_STEPS] ($pct%) $1"
}

# === Step 1: SGLang model (safetensors) ===
progress "SGLang model (safetensors)"
SGLANG_PATH=$(yq '.sglang.model_path' "$MODELS_CONFIG")
SGLANG_NAME=$(basename "$SGLANG_PATH")
if [ -d "$MODELS_DIR/$SGLANG_NAME" ]; then
  echo "  ✓ Already exists: $SGLANG_NAME"
else
  echo "  Downloading: $SGLANG_PATH"
  echo "  → $MODELS_DIR/$SGLANG_NAME"
  huggingface-cli download "$SGLANG_PATH" --local-dir "$MODELS_DIR/$SGLANG_NAME"
  echo "  ✓ Downloaded: $SGLANG_NAME"
fi

# === Step 2: Ollama main model (GGUF) ===
progress "Ollama main model (GGUF)"
OLLAMA_MAIN=$(yq '.ollama_main.model' "$MODELS_CONFIG")
if docker exec ollama-main ollama list 2>/dev/null | grep -q "$OLLAMA_MAIN"; then
  echo "  ✓ Already pulled: $OLLAMA_MAIN"
else
  echo "  Pulling: $OLLAMA_MAIN"
  docker exec ollama-main ollama pull "$OLLAMA_MAIN" 2>/dev/null && echo "  ✓ Pulled: $OLLAMA_MAIN" || echo "  ⚠ Ollama not running — pull later: docker exec ollama-main ollama pull $OLLAMA_MAIN"
fi

# === Step 3: Ollama agent model (GGUF) ===
progress "Ollama agent model (GGUF)"
OLLAMA_AGENT=$(yq '.ollama_agent.model' "$MODELS_CONFIG")
if docker exec ollama-agent ollama list 2>/dev/null | grep -q "$OLLAMA_AGENT"; then
  echo "  ✓ Already pulled: $OLLAMA_AGENT"
else
  echo "  Pulling: $OLLAMA_AGENT"
  docker exec ollama-agent ollama pull "$OLLAMA_AGENT" 2>/dev/null && echo "  ✓ Pulled: $OLLAMA_AGENT" || echo "  ⚠ Ollama not running — pull later"
fi

# === Step 4: Embeddings model ===
progress "Embeddings model"
EMBED_MODEL=$(yq '.embeddings.model' "$MODELS_CONFIG")
if docker exec ollama-main ollama list 2>/dev/null | grep -q "$EMBED_MODEL"; then
  echo "  ✓ Already pulled: $EMBED_MODEL"
else
  echo "  Pulling: $EMBED_MODEL"
  docker exec ollama-main ollama pull "$EMBED_MODEL" 2>/dev/null && echo "  ✓ Pulled: $EMBED_MODEL" || echo "  ⚠ Pull later"
fi

# === Step 5: llama.cpp GGUF model (CPU fallback) ===
progress "llama.cpp GGUF model (CPU fallback)"
LLAMACPP_FILE=$(yq '.llamacpp.model_file' "$MODELS_CONFIG")
if [ -f "$LLAMACPP_FILE" ]; then
  echo "  ✓ Already exists: $(basename "$LLAMACPP_FILE")"
else
  echo "  Downloading GGUF..."
  GGUF_REPO=$(dirname "$LLAMACPP_FILE" | sed 's|/models/||')
  GGUF_NAME=$(basename "$LLAMACPP_FILE")
  huggingface-cli download "$GGUF_REPO" --include "$GGUF_NAME" --local-dir "$MODELS_DIR/"
  echo "  ✓ Downloaded: $GGUF_NAME"
fi

# === Step 6: Needle2 router (fixed) ===
progress "Needle2 router (26M params)"
if [ -d "$MODELS_DIR/needle-router-v1" ]; then
  echo "  ✓ Already exists"
else
  echo "  Downloading..."
  huggingface-cli download Cactus-Compute/needle-router-v1 --local-dir "$MODELS_DIR/needle-router-v1"
  echo "  ✓ Downloaded"
fi

# === Step 7: ComfyUI checkpoint (creative mode) ===
progress "ComfyUI checkpoint (creative mode)"
COMFY_CKPT=$(yq '.comfyui.checkpoint' "$MODELS_CONFIG")
COMFY_DIR="/opt/scarlix/ai/comfyui/data/models/checkpoints"
if [ -f "$COMFY_DIR/$COMFY_CKPT" ]; then
  echo "  ✓ Already exists: $COMFY_CKPT"
else
  echo "  Downloading: $COMFY_CKPT"
  case "$COMFY_CKPT" in
    flux*) huggingface-cli download Kijai/flux-fp8 --include "$COMFY_CKPT" --local-dir "$COMFY_DIR" 2>/dev/null && echo "  ✓ Downloaded" || echo "  ⚠ Download manually from HuggingFace" ;;
    sd3*) huggingface-cli download stabilityai/stable-diffusion-3.5-large --include "$COMFY_CKPT" --local-dir "$COMFY_DIR" 2>/dev/null && echo "  ✓ Downloaded" || echo "  ⚠ Download manually" ;;
    *) echo "  ⚠ Unknown checkpoint — download manually from HuggingFace" ;;
  esac
fi

echo ""
echo "============================================"
echo "  ✅ Model download complete!"
echo "============================================"
echo ""
echo "Models in $MODELS_DIR:"
ls -lh "$MODELS_DIR" 2>/dev/null | head -20
echo ""
echo "Ollama main models:"
docker exec ollama-main ollama list 2>/dev/null || echo "  (Ollama not running)"
echo ""
echo "Ollama agent models:"
docker exec ollama-agent ollama list 2>/dev/null || echo "  (Ollama not running)"
