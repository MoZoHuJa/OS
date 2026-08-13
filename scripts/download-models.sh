#!/usr/bin/env bash
set -euo pipefail
MODELS_CONFIG="${MODELS_CONFIG:-/etc/scarlix/models.yaml}"
echo "=== SCARLIX OS v15 — Model Download ==="

if ! command -v yq &>/dev/null; then
  echo "Installing yq..."
  sudo wget -qO /usr/local/bin/yq https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64
  sudo chmod +x /usr/local/bin/yq
fi

# SGLang model
SGLANG_PATH=$(yq '.sglang.model_path' "$MODELS_CONFIG")
SGLANG_NAME=$(basename "$SGLANG_PATH")
if [ ! -d "/models/$SGLANG_NAME" ] && [ "$SGLANG_PATH" != "null" ]; then
  echo "[1/6] Downloading SGLang model: $SGLANG_PATH..."
  huggingface-cli download "$SGLANG_PATH" --local-dir "/models/$SGLANG_NAME"
else
  echo "[1/6] SGLang model already exists: $SGLANG_NAME"
fi

# Ollama models
OLLAMA_MAIN_MODEL=$(yq '.ollama_main.model' "$MODELS_CONFIG")
echo "[2/6] Pulling Ollama main: $OLLAMA_MAIN_MODEL..."
docker exec ollama-main ollama pull "$OLLAMA_MAIN_MODEL" 2>/dev/null || echo "  (Pull later)"

OLLAMA_AGENT_MODEL=$(yq '.ollama_agent.model' "$MODELS_CONFIG")
echo "[3/6] Pulling Ollama agent: $OLLAMA_AGENT_MODEL..."
docker exec ollama-agent ollama pull "$OLLAMA_AGENT_MODEL" 2>/dev/null || echo "  (Pull later)"

# Embeddings
EMBED_MODEL=$(yq '.embeddings.model' "$MODELS_CONFIG")
echo "[3.5/6] Pulling embedding model: $EMBED_MODEL..."
docker exec ollama-main ollama pull "$EMBED_MODEL" 2>/dev/null || true

# llama.cpp GGUF
LLAMACPP_FILE=$(yq '.llamacpp.model_file' "$MODELS_CONFIG")
if [ ! -f "$LLAMACPP_FILE" ] && [ "$LLAMACPP_FILE" != "null" ]; then
  echo "[4/6] Downloading GGUF model..."
  GGUF_REPO=$(dirname "$LLAMACPP_FILE" | sed 's|/models/||')
  GGUF_NAME=$(basename "$LLAMACPP_FILE")
  huggingface-cli download "$GGUF_REPO" --include "$GGUF_NAME" --local-dir "/models/"
fi

# Needle2
if [ ! -d "/models/needle-router-v1" ]; then
  echo "[5/6] Downloading Needle2 router..."
  huggingface-cli download Cactus-Compute/needle-router-v1 --local-dir /models/needle-router-v1
fi

# ComfyUI checkpoint
COMFY_CKPT=$(yq '.comfyui.checkpoint' "$MODELS_CONFIG")
COMFY_DIR="/opt/scarlix/ai/comfyui/data/models/checkpoints"
if [ ! -f "$COMFY_DIR/$COMFY_CKPT" ] && [ "$COMFY_CKPT" != "null" ]; then
  echo "[6/6] Downloading ComfyUI checkpoint: $COMFY_CKPT..."
  huggingface-cli download Kijai/flux-fp8 --include "$COMFY_CKPT" --local-dir "$COMFY_DIR" 2>/dev/null || echo "  Download manually"
fi

echo "=== Model download complete! ==="
