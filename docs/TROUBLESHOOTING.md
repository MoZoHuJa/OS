# SCARLIX OS v12 — Troubleshooting

## Common Issues

### NVIDIA Driver Issues

**Problem:** `nvidia-smi` fails or shows no GPUs
```bash
# Reinstall driver
sudo apt purge 'nvidia-*'
sudo apt install nvidia-driver-570-open
sudo reboot

# Verify
nvidia-smi
cat /proc/driver/nvidia/version | head -1
# Should say "NVIDIA UNIX Open Kernel Module"
```

### SGLang OOM

**Problem:** SGLang container runs out of VRAM
```bash
# Reduce memory fraction
# Edit ai/sglang/docker-compose.yml:
#   --mem-fraction-static 0.80  # was 0.90
docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml up -d
```

### LiteLLM Connection Refused

**Problem:** LiteLLM can't reach SGLang/Ollama
```bash
# Check logs
docker logs litellm

# Verify config
cat /opt/scarlix/ai/litellm/config.yaml

# Test direct connection
curl http://localhost:30000/v1/models  # SGLang
curl http://localhost:11435/api/tags   # Ollama
```

### Buzz Relay Crash

**Problem:** Buzz relay won't start
```bash
# Check dependencies
docker logs buzz-postgres
docker logs buzz-redis
docker logs buzz-minio

# Restart all
cd /opt/scarlix/workspace/buzz
docker compose down
docker compose up -d
```

### Hermes Not Responding

**Problem:** Hermes agent doesn't respond on Telegram
```bash
# Check health
curl http://localhost:7999/health

# Check logs
docker logs hermes

# Verify LiteLLM is reachable from Hermes
docker exec hermes curl http://litellm:4000/health

# Check Telegram token
docker exec hermes env | grep TELEGRAM
```

### Caddy 502 Bad Gateway

**Problem:** Reverse proxy returns 502
```bash
# Backend not running
docker ps | grep <service>

# Restart backend
docker compose -f /opt/scarlix/<service>/docker-compose.yml up -d

# Check Caddy logs
docker logs caddy
```

### GPU Handoff Fails

**Problem:** scarlix-mode game/turbo doesn't switch properly
```bash
# Check current mode
scarlix-mode status

# Manual switch
docker stop sglang
docker compose -f /opt/scarlix/gaming/docker-compose.yml up -d sunshine

# Check GPU memory
nvidia-smi
```

### Whisper CUDA Error

**Problem:** Whisper can't use GPU 1
```bash
# Check GPU 1 is free
nvidia-smi

# Stop other GPU 1 processes
docker stop ollama-agent  # temporarily
docker restart whisper-sk
docker start ollama-agent
```

### Docker Network Issues

**Problem:** Containers can't reach each other
```bash
# Verify networks exist
docker network ls | grep scarlix

# Recreate if needed
docker network rm scarlix_net scarlix_ai
bash /opt/scarlix/docker/networks.sh

# Restart all containers
docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml up -d
# ... etc
```

## Emergency Recovery

### Full System Reset
```bash
# Stop all containers
docker stop $(docker ps -q)

# Restore from backup
restic restore latest --target /

# Reboot
sudo reboot
```

### Offline Mode (No Internet)
```bash
scarlix-mode offline
# All services continue to work locally
# Only model downloads require internet
```
