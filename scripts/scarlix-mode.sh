#!/usr/bin/env bash
set -euo pipefail
MODE="${1:-status}"

case "$MODE" in
  ai)
    echo "AI mode: SGLang GPU 0, Ollama GPU 1"
    docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml up -d
    docker compose -f /opt/scarlix/ai/ollama/docker-compose.yml up -d
    docker stop sunshine 2>/dev/null || true
    docker stop llamacpp 2>/dev/null || true
    echo "ai" | sudo tee /var/lib/scarlix/current-mode
    ;;
  game)
    echo "Game mode: Sunshine GPU 0, Ollama GPU 1"
    ACTIVE=$(docker exec hermes curl -s http://localhost:7999/api/active-tasks 2>/dev/null | jq '.count // 0' || echo 0)
    if [ "$ACTIVE" -gt 0 ] 2>/dev/null; then
      echo "WARNING: Hermes has $ACTIVE active tasks. Waiting 30s..."
      sleep 30
    fi
    docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml stop
    docker compose -f /opt/scarlix/gaming/docker-compose.yml up -d sunshine 2>/dev/null || echo "Sunshine not configured"
    echo "game" | sudo tee /var/lib/scarlix/current-mode
    ;;
  turbo)
    echo "Turbo mode: dual inference (SGLang GPU 0 + GPU 1)"
    docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml up -d
    docker stop sunshine 2>/dev/null || true
    echo "turbo" | sudo tee /var/lib/scarlix/current-mode
    ;;
  offline)
    echo "Offline mode: llama.cpp CPU only"
    docker stop sglang ollama-agent sunshine 2>/dev/null || true
    docker compose -f /opt/scarlix/ai/llamacpp/docker-compose.yml up -d
    echo "offline" | sudo tee /var/lib/scarlix/current-mode
    ;;
  status)
    echo "=== SCARLIX OS v12 Status ==="
    echo "Mode: $(cat /var/lib/scarlix/current-mode 2>/dev/null || echo unknown)"
    echo ""
    nvidia-smi --query-gpu=index,name,memory.used,memory.free,utilization.gpu --format=csv,noheader 2>/dev/null || echo "nvidia-smi not available"
    echo ""
    docker ps --format "table {{.Names}}\t{{.Status}}" 2>/dev/null | head -25
    ;;
  *)
    echo "Usage: scarlix-mode {ai|game|turbo|offline|status}"
    exit 1
    ;;
esac
