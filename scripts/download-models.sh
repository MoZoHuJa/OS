#!/usr/bin/env bash
set -euo pipefail

echo "=== SCARLIX OS v12 — Model Download ==="

MODELS_DIR="${MODELS_DIR:-/models}"
SGLANG_CACHE="${SGLANG_CACHE:-/var/lib/sglang/cache}"

mkdir -p "$MODELS_DIR" "$SGLANG_CACHE"

# 1. SGLang model (safetensors)
echo "[1/3] Downloading Qwen3-14B-Instruct (safetensors for SGLang)..."
if [ ! -d "$MODELS_DIR/Qwen3-14B-Instruct" ]; then
  pip install -q huggingface_hub 2>/dev/null || pip3 install -q huggingface_hub
  huggingface-cli download Qwen/Qwen3-14B-Instruct \
    --local-dir "$MODELS_DIR/Qwen3-14B-Instruct"
  echo "Done."
else
  echo "Already exists, skipping."
fi

# 2. Ollama models (GGUF)
echo "[2/3] Pulling Ollama models (GGUF)..."
if docker ps | grep -q ollama-agent; then
  docker exec ollama-agent ollama pull qwen3.6:14b
  docker exec ollama-agent ollama pull nomic-embed-text
  echo "Done."
else
  echo "Ollama not running. Start it first: docker compose -f /opt/scarlix/ai/ollama/docker-compose.yml up -d"
fi

# 3. llama.cpp model (GGUF Q4_K_M)
echo "[3/3] Downloading Qwen3 GGUF (for llama.cpp fallback)..."
GGUF_FILE="$MODELS_DIR/qwen3.6-14b-instruct-q4_k_m.gguf"
if [ ! -f "$GGUF_FILE" ]; then
  wget -O "$GGUF_FILE" \
    "https://huggingface.co/Qwen/Qwen3-14B-Instruct-GGUF/resolve/main/qwen3.6-14b-instruct-q4_k_m.gguf"
  echo "Done."
else
  echo "Already exists, skipping."
fi

echo ""
echo "=== All models downloaded! ==="
echo "Models in: $MODELS_DIR"
