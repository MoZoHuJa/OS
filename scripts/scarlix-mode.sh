#!/usr/bin/env bash
set -euo pipefail
LOG_FILE="/var/log/scarlix-mode.log"
STATE_FILE="/var/lib/scarlix/current-mode"
LOCK_FILE="/tmp/scarlix-mode.lock"

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"; }

exec 200>"$LOCK_FILE"
flock -n 200 || { log "ERROR: iny proces prepina rezim"; exit 1; }

MODE="${1:-status}"
CURRENT_MODE=$(cat "$STATE_FILE" 2>/dev/null || echo "unknown")

dump_vram() {
  log "Uvolnujem VRAM..."
  curl -s -X POST "http://localhost:11434/api/generate" -d '{"model":"qwen3.6:14b","keep_alive":0}' >/dev/null 2>&1 || true
  curl -s -X POST "http://localhost:11435/api/generate" -d '{"model":"qwen3.6:14b","keep_alive":0}' >/dev/null 2>&1 || true
  docker stop sglang 2>/dev/null || true
  sleep 3
  log "VRAM uvolnena."
}

case "$MODE" in
  ai)
    [[ "$CURRENT_MODE" == "ai" ]] && { log "Uz v AI mode."; exit 0; }
    log "=== AI mode ==="
    docker compose -f /opt/scarlix/gaming/docker-compose.yml --profile gaming stop 2>/dev/null || true
    docker compose -f /opt/scarlix/ai/comfyui/docker-compose.yml --profile creative stop 2>/dev/null || true
    docker compose -f /opt/scarlix/ai/video/docker-compose.yml --profile creative stop 2>/dev/null || true
    docker compose -f /opt/scarlix/ai/musicgen/docker-compose.yml --profile creative stop 2>/dev/null || true
    docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml up -d
    echo "ai" | tee "$STATE_FILE" >/dev/null
    ;;
  game)
    [[ "$CURRENT_MODE" == "game" ]] && { log "Uz v Game mode."; exit 0; }
    log "=== GAME mode ==="
    dump_vram
    docker compose -f /opt/scarlix/ai/comfyui/docker-compose.yml --profile creative stop 2>/dev/null || true
    docker compose -f /opt/scarlix/gaming/docker-compose.yml --profile gaming up -d sunshine steam-headless
    echo "game" | tee "$STATE_FILE" >/dev/null
    ;;
  creative)
    [[ "$CURRENT_MODE" == "creative" ]] && { log "Uz v Creative mode."; exit 0; }
    log "=== CREATIVE mode ==="
    dump_vram
    docker compose -f /opt/scarlix/ai/comfyui/docker-compose.yml --profile creative up -d
    docker compose -f /opt/scarlix/ai/video/docker-compose.yml --profile creative up -d
    docker compose -f /opt/scarlix/ai/musicgen/docker-compose.yml --profile creative up -d
    echo "creative" | tee "$STATE_FILE" >/dev/null
    ;;
  turbo)
    log "=== TURBO mode ==="
    docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml up -d
    docker compose -f /opt/scarlix/ai/ollama/docker-compose-main.yml up -d
    echo "turbo" | tee "$STATE_FILE" >/dev/null
    ;;
  offline)
    log "=== OFFLINE mode ==="
    dump_vram
    docker compose -f /opt/scarlix/ai/llamacpp/docker-compose.yml up -d
    echo "offline" | tee "$STATE_FILE" >/dev/null
    ;;
  tv)
    log "=== TV mode ==="
    docker compose -f /opt/scarlix/gaming/docker-compose.yml --profile gaming up -d sunshine
    sudo systemctl start scarlix-tv-mode.service
    echo "tv" | tee "$STATE_FILE" >/dev/null
    ;;
  status)
    echo "=== SCARLIX OS v15 Status ==="
    echo "Mode: $CURRENT_MODE"
    nvidia-smi --query-gpu=index,name,memory.used,memory.free,utilization.gpu --format=csv,noheader 2>/dev/null || echo "nvidia-smi not available"
    docker ps --format "table {{.Names}}\t{{.Status}}" | head -30
    ;;
  *)
    echo "Usage: scarlix-mode {ai|game|creative|turbo|offline|tv|status}"
    exit 1
    ;;
esac
flock -u 200
