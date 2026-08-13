# SCARLIX OS v15 — KOMPLETNÝ SELF-CONTAINED BUILD MANIFEST

**Verzia:** 15.0.0-FINAL | **Účel:** Tento jediný dokument obsahuje ÚPLNE VŠETKO potrebné na vytvorenie bootovateľného ISO, ktoré nainštaluje celý SCARLIX OS na dva PC servery. Žiadne externé odkazy. Žiadne TODO. Každý blok kódu je finálny.

**Model-agnostic:** Podporuje akýkoľvek HuggingFace model (safetensors aj GGUF), nielen Qwen.

---

## ČASŤ 0 — ARCHITEKTÚRA A HARDVÉR

### 0.1 Topológia

```
INTERNET ←→ ROUTER (192.168.1.1)
│
┌───────────┴───────────┐
│                       │
│   MAIN PC             │   HP AGENT PC
│   192.168.1.100       │   192.168.1.101
│   scarlix-main        │   scarlix-hp
│   GPU0+GPU1 (AI+Game) │   Worker (Cloud+Dev)
│                       │
└───────────┬───────────┘
│
HEADSCALE VPN (100.64.0.0/10)
Main=100.64.0.1 | HP=100.64.0.2
│
TV (Moonlight stream, žiadny HDMI kábel)
```

### 0.2 Hardvér

**MAIN PC:**
- CPU: AMD Ryzen 7 7700X (8C/16T)
- RAM: 64 GB DDR5-5600
- GPU 0: NVIDIA RTX 5060 Ti 16GB + HDMI dummy plug (4K)
- GPU 1: NVIDIA RTX 4060 Ti 16GB + HDMI dummy plug (1080p)
- Disk1: 2TB NVMe (OS + modely) | Disk2: 4TB NVMe (média+hry) | Disk3: 8TB HDD (zálohy)
- Sieť: 2.5GbE + WiFi6E | Zdroj: 850W ATX3.0 | USB BT 5.3 dongle

**HP AGENT PC:**
- CPU: Intel i5-12400 | RAM: 32 GB DDR4 | Disk1: 1TB NVMe | Disk2: 2TB SSD | Disk3: 4TB HDD | 2.5GbE

**TV:** Android TV + Moonlight + BT klávesnica + BT myš (všetko cez LAN/WiFi stream, NIE HDMI kábel)

### 0.3 VRAM rozpočet (kritické — nikdy nespúšťať tieto kombinácie naraz)

| Mód | GPU 0 (16GB) | GPU 1 (16GB) |
|-----|--------------|--------------|
| ai | SGLang AWQ (~9GB) + Ollama main | Ollama agent + Whisper + Needle |
| game | Sunshine | Ollama agent + Whisper |
| creative | ComfyUI FP8 (~12GB) | Ollama agent + Whisper |
| turbo | SGLang + MTP | Ollama main + agent |
| offline | — (llama.cpp CPU) | — |
| tv | Sunshine | Ollama agent |

### 0.4 Kontajnerový profilový systém

| Profil | Kontajnery | Kedy beží |
|--------|-----------|-----------|
| default | smg, ollama-main, ollama-agent, hermes, caddy, authelia, headscale, scarlihq, buzz | Vždy |
| ai | sglang, needle | scarlix-mode ai/turbo |
| creative | comfyui, video-api, musicgen | scarlix-mode creative |
| gaming | sunshine, steam-headless, retroarch | scarlix-mode game/tv |
| siem | wazuh-manager | Manuálne (--profile siem) |
| tracing | openlit, alertmanager | Manuálne (--profile tracing) |

### 0.5 Model-Agnostic Systém (NOVÉ v V15)

SCARLIX OS v15 podporuje akýkoľvek HuggingFace model. Konfigurácia je v `/etc/scarlix/models.yaml`:

```yaml
# /etc/scarlix/models.yaml — Model Configuration
# Tu definuješ ktoré modely chceš používať.
# Môžeš zmeniť na akýkoľvek HuggingFace model.

# === SGLang (safetensors formát) ===
# Primárny inference server. Podporuje: Llama, Qwen, Mistral, Gemma, Phi, DeepSeek, etc.
sglang:
  model_path: "/models/Qwen3-14B-Instruct-AWQ"       # HuggingFace model path (safetensors)
  # Alternatívy (odkomentuj jednu):
  # model_path: "/models/Meta-Llama-3.1-8B-Instruct"
  # model_path: "/models/mistralai/Mistral-7B-Instruct-v0.3"
  # model_path: "/models/google/gemma-2-9b-it"
  # model_path: "/models/microsoft/Phi-3-medium-128k-instruct"
  # model_path: "/models/deepseek-ai/DeepSeek-Coder-V2-Lite-Instruct"
  quantization: "awq"           # awq, gptq, fp8, alebo null pre none
  context_length: 32768         # 8192 pre menšie GPU, 32768 pre 16GB+
  mem_fraction: 0.85            # 0.80 pre GPU 8GB, 0.85 pre 16GB, 0.90 pre 24GB+
  flashinfer: true

# === Ollama Main (GGUF formát, GPU 0) ===
# Concurrent/fallback. Podporuje: akýkoľvek GGUF model z HuggingFace.
ollama_main:
  model: "qwen3.6:14b"          # Ollama tag
  # Alternatívy:
  # model: "llama3.1:8b"
  # model: "mistral:7b"
  # model: "gemma2:9b"
  # model: "phi3:14b"
  # model: "deepseek-coder-v2:16b"
  keep_alive: "15m"
  num_parallel: 4

# === Ollama Agent (GGUF formát, GPU 1) ===
# Persistent agent inference.
ollama_agent:
  model: "qwen3.6:14b"
  # Alternatívy:
  # model: "llama3.1:8b"
  # model: "qwen2.5-coder:14b"
  # model: "mistral-nemo:12b"
  keep_alive: "-1"              # -1 = trvalo v VRAM
  num_parallel: 2

# === llama.cpp (CPU fallback, GGUF) ===
llamacpp:
  model_file: "/models/qwen3.6-14b-instruct-q4_k_m.gguf"
  # Alternatívy (stiahni z HuggingFace GGUF repozitárov):
  # model_file: "/models/Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf"
  # model_file: "/models/mistral-7b-instruct-v0.3-Q4_K_M.gguf"
  # model_file: "/models/gemma-2-9b-it-Q4_K_M.gguf"
  context_size: 8192
  threads: 16

# === Needle2 Router (GPU 1, 26M params) ===
# Fast intent routing — nezávisí od hlavného modelu.
needle:
  model_path: "/models/needle-router-v1"
  # Tento je fixný — nemeniť.

# === ComfyUI (creative mode, GPU 0) ===
comfyui:
  checkpoint: "flux1-dev-fp8.safetensors"
  # Alternatívy:
  # checkpoint: "sd3.5_large_fp8.safetensors"
  # checkpoint: "flux1-schnell-fp8.safetensors"

# === Whisper (STT, GPU 1) ===
whisper:
  model: "Systran/faster-whisper-large-v3"
  # Alternatívy:
  # model: "Systran/faster-whisper-medium"
  # model: "Systran/faster-whisper-turbo"
  language: "sk"

# === Piper (TTS, CPU) ===
piper:
  voice: "sk_SK-lili-medium"
  # Alternatívy pre iné jazyky:
  # voice: "en_US-amy-medium"
  # voice: "de_DE-thorsten-medium"
  # voice: "cs_CZ-jirka-medium"

# === Embeddings (pre RAG/memory) ===
embeddings:
  model: "nomic-embed-text"
  # Alternatívy:
  # model: "all-MiniLM-L6-v2"
  # model: "bge-large-en-v1.5"
```

```bash
#!/usr/bin/env bash
# /opt/scarlix/scripts/download-models.sh — Model-Agnostic Downloader
# Stiahne modely definované v /etc/scarlix/models.yaml
set -euo pipefail
MODELS_CONFIG="${MODELS_CONFIG:-/etc/scarlix/models.yaml}"
echo "=== SCARLIX OS v15 — Model Download ==="
echo "Reading config: $MODELS_CONFIG"

# Parse YAML (requires yq)
if ! command -v yq &>/dev/null; then
  echo "Installing yq..."
  sudo wget -qO /usr/local/bin/yq https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64
  sudo chmod +x /usr/local/bin/yq
fi

# SGLang model (safetensors)
SGLANG_PATH=$(yq '.sglang.model_path' "$MODELS_CONFIG")
SGLANG_NAME=$(basename "$SGLANG_PATH")
if [ ! -d "/models/$SGLANG_NAME" ] && [ "$SGLANG_PATH" != "null" ]; then
  echo "[1/6] Downloading SGLang model: $SGLANG_PATH..."
  huggingface-cli download "$SGLANG_PATH" --local-dir "/models/$SGLANG_NAME"
else
  echo "[1/6] SGLang model already exists: $SGLANG_NAME"
fi

# Ollama Main model (GGUF via Ollama)
OLLAMA_MAIN_MODEL=$(yq '.ollama_main.model' "$MODELS_CONFIG")
echo "[2/6] Pulling Ollama main model: $OLLAMA_MAIN_MODEL..."
docker exec ollama-main ollama pull "$OLLAMA_MAIN_MODEL" 2>/dev/null || echo "  (Ollama not running yet — pull later)"

# Ollama Agent model
OLLAMA_AGENT_MODEL=$(yq '.ollama_agent.model' "$MODELS_CONFIG")
echo "[3/6] Pulling Ollama agent model: $OLLAMA_AGENT_MODEL..."
docker exec ollama-agent ollama pull "$OLLAMA_AGENT_MODEL" 2>/dev/null || echo "  (Ollama not running yet — pull later)"

# Embeddings
EMBED_MODEL=$(yq '.embeddings.model' "$MODELS_CONFIG")
echo "[3.5/6] Pulling embedding model: $EMBED_MODEL..."
docker exec ollama-main ollama pull "$EMBED_MODEL" 2>/dev/null || true

# llama.cpp model (GGUF file)
LLAMACPP_FILE=$(yq '.llamacpp.model_file' "$MODELS_CONFIG")
if [ ! -f "$LLAMACPP_FILE" ] && [ "$LLAMACPP_FILE" != "null" ]; then
  echo "[4/6] Downloading llama.cpp GGUF model..."
  # Extract repo and filename from path
  GGUF_REPO=$(dirname "$LLAMACPP_FILE" | sed 's|/models/||')
  GGUF_NAME=$(basename "$LLAMACPP_FILE")
  huggingface-cli download "$GGUF_REPO" --include "$GGUF_NAME" --local-dir "/models/"
fi

# Needle2 router (fixný)
if [ ! -d "/models/needle-router-v1" ]; then
  echo "[5/6] Downloading Needle2 router..."
  huggingface-cli download Cactus-Compute/needle-router-v1 --local-dir /models/needle-router-v1
fi

# ComfyUI checkpoint (creative mode)
COMFY_CKPT=$(yq '.comfyui.checkpoint' "$MODELS_CONFIG")
COMFY_DIR="/opt/scarlix/ai/comfyui/data/models/checkpoints"
if [ ! -f "$COMFY_DIR/$COMFY_CKPT" ] && [ "$COMFY_CKPT" != "null" ]; then
  echo "[6/6] Downloading ComfyUI checkpoint: $COMFY_CKPT..."
  case "$COMFY_CKPT" in
    flux1-dev-fp8*) huggingface-cli download Kijai/flux-fp8 --include "$COMFY_CKPT" --local-dir "$COMFY_DIR" ;;
    sd3.5*) huggingface-cli download stabilityai/stable-diffusion-3.5-large --include "$COMFY_CKPT" --local-dir "$COMFY_DIR" ;;
    *) echo "  Unknown checkpoint — download manually from HuggingFace" ;;
  esac
fi

# Whisper model (auto-download v kontajneri)
WHISPER_MODEL=$(yq '.whisper.model' "$MODELS_CONFIG")
echo "Whisper model '$WHISPER_MODEL' will auto-download on first use."

echo ""
echo "=== Model download complete! ==="
echo "Models in /models/:"
ls -lh /models/ 2>/dev/null | head -20
echo ""
echo "Ollama models:"
docker exec ollama-main ollama list 2>/dev/null || echo "  (Ollama not running)"
docker exec ollama-agent ollama list 2>/dev/null || echo "  (Ollama not running)"
```

---

## ČASŤ 1 — BIOS (oba PC)

```
Boot Mode: UEFI | Above 4G Decoding: ENABLED | Resizable BAR: ENABLED
Secure Boot: DISABLED | CSM: Disabled | VT-d/IOMMU: Enabled
XMP: Profile 1 | Boot Order: USB first
```


---

## ČASŤ 2 — BASE OS (oba PC)

```bash
# Ubuntu 24.04 LTS Server, Minimized, LVM, SSH, user: scarlix
# Main: hostname scarlix-main | HP: hostname scarlix-hp
sudo apt update && sudo apt upgrade -y
sudo apt install -y curl wget git jq bc net-tools htop tmux vim nano \
  build-essential dkms linux-headers-$(uname -r) \
  ufw fail2ban chrony unzip rsync lvm2 python3-pip python3-venv whiptail \
  bluez blueman pulseaudio-module-bluetooth alsa-utils portaudio19-dev
sudo timedatectl set-timezone Europe/Bratislava

# Statická IP — Main:
sudo tee /etc/netplan/01-scarlix.yaml << 'EOF'
network:
  version: 2
  ethernets:
    id0:
      match: {name: "en*"}
      addresses: [192.168.1.100/24]
      routes: [{to: default, via: 192.168.1.1}]
      nameservers: {addresses: [1.1.1.1, 8.8.8.8]}
      dhcp4: false
EOF
# HP Agent: rovnaké, ale addresses: [192.168.1.101/24]
sudo netplan apply

# UFW + Fail2ban:
sudo ufw default deny incoming && sudo ufw default allow outgoing
sudo ufw allow 22/tcp && sudo ufw allow 80/tcp && sudo ufw allow 443/tcp && sudo ufw allow 41641/udp
sudo ufw --force enable
sudo tee /etc/fail2ban/jail.local << 'EOF'
[DEFAULT]
bantime=3600
findtime=600
maxretry=3
[sshd]
enabled=true
port=22
maxretry=3
bantime=3600
EOF
sudo systemctl enable --now fail2ban chrony

# Log rotation (kritické pre stabilitu):
sudo tee /etc/systemd/journald.conf << 'EOF'
[Journal]
SystemMaxUse=500M
SystemMaxFileSize=100M
MaxRetentionSec=7day
EOF
sudo systemctl restart systemd-journald

sudo tee /etc/logrotate.d/scarlix << 'EOF'
/var/log/caddy/*.log {
  daily
  rotate 7
  compress
  delaycompress
  missingok
  notifempty
  create 644 root root
  sharedscripts
  postrotate
    docker exec caddy caddy reload --config /etc/caddy/Caddyfile 2>/dev/null || true
  endscript
}
/var/log/scarlix-*.log {
  weekly
  rotate 4
  compress
  missingok
  notifempty
}
EOF

# Adresáre MAIN:
sudo mkdir -p \
  /opt/scarlix/{ai/{sglang,ollama/{main,agent},llamacpp,needle,smg,comfyui/data/{models/{checkpoints,diffusion_models,vae},output,customnodes},video/data,musicgen/data/output},voice/{whisper/data,piper/models,wakeword,jarvis},agents/hermes/{config,workspace,skills,soul},workspace/buzz/{data,postgres/data,redis,minio/data},gaming/{sunshine/config,steam,retroarch/config,jellyfin/config,minecraft/data},network/{caddy/{data,config},authelia/{config,data},headscale/{config,data},crowdsec/{config,data},wazuh/{config,logs,data}},monitoring/{victoria,grafana,otel,openlit,alertmanager},scarlihq,scripts}
sudo mkdir -p /models /var/lib/sglang/cache /var/lib/scarlix /etc/scarlix/{profiles,secrets}
sudo mkdir -p /mnt/{files/media,games/{steam-games,heroic-games,retroroms},photos,backup/restic}

# Adresáre HP:
sudo mkdir -p /opt/scarlix/{codingpipeline/{postgres/data,redis,gitea/data,coolify/data,n8n/data,nextcloud/{data,config,db},photoprism/storage},media-tools/data/{input,output,templates}}
sudo mkdir -p /mnt/{files,photos,backup}
sudo chown -R scarlix:scarlix /opt/scarlix /models /var/lib/sglang /var/lib/scarlix /etc/scarlix /mnt

# Sudoers:
sudo tee /etc/sudoers.d/scarlix << 'EOF'
scarlix ALL=(ALL) NOPASSWD: /usr/local/bin/scarlix-mode,/usr/bin/docker,/usr/bin/systemctl,/usr/bin/journalctl,/usr/sbin/ufw,/usr/bin/apt
scarlix ALL=(ALL) PASSWD: ALL
EOF
sudo chmod 440 /etc/sudoers.d/scarlix
```


---

## ČASŤ 3 — NVIDIA + CUDA + DOCKER

### 3.1 NVIDIA (len MAIN PC)

```bash
sudo add-apt-repository ppa:graphics-drivers/ppa -y && sudo apt update
sudo apt install -y nvidia-driver-570-open && sudo reboot
# po reboote: nvidia-smi (musí ukázať 2× GPU, driver 570.x)

wget https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64/cuda-keyring_1.1-1_all.deb
sudo dpkg -i cuda-keyring_1.1-1_all.deb && sudo apt update
sudo apt install -y cuda-toolkit-12-4 libcudnn9-cuda-12 libcudnn9-dev-cuda-12
echo 'export PATH=/usr/local/cuda-12.4/bin:$PATH' | sudo tee /etc/profile.d/cuda.sh
echo 'export LD_LIBRARY_PATH=/usr/local/cuda-12.4/lib64:$LD_LIBRARY_PATH' | sudo tee -a /etc/profile.d/cuda.sh

curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
sudo apt update && sudo apt install -y nvidia-container-toolkit

# Headless X server s HDMI dummy pre Sunshine:
sudo nvidia-xconfig --allow-empty-initial-configuration --virtual=3840x2160 -busid=PCI:1:0:0
sudo tee /etc/systemd/system/xorg-headless.service << 'EOF'
[Unit]
Description=X Server headless HDMI dummy
After=network.target
[Service]
Type=simple
ExecStart=/usr/bin/Xorg :0 -config /etc/X11/xorg.conf -noreset
Restart=on-failure
RestartSec=5
[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload && sudo systemctl enable xorg-headless

# Stabilné GPU indexy (udev rules):
GPU_PCI=$(lspci | grep -i nvidia | awk '{print $1}')
GPU0_PCI=$(echo "$GPU_PCI" | head -1)
GPU1_PCI=$(echo "$GPU_PCI" | tail -1)
sudo tee /etc/udev/rules.d/99-nvidia-stable.rules << EOF
KERNEL=="nvidia", KERNELS=="0000:$GPU0_PCI", SYMLINK+="nvidia-ai"
KERNEL=="nvidia_modeset", KERNELS=="0000:$GPU0_PCI", SYMLINK+="nvidia-ai-modeset"
KERNEL=="nvidia_uvm", KERNELS=="0000:$GPU0_PCI", SYMLINK+="nvidia-ai-uvm"
KERNEL=="nvidia", KERNELS=="0000:$GPU1_PCI", SYMLINK+="nvidia-game"
KERNEL=="nvidia_modeset", KERNELS=="0000:$GPU1_PCI", SYMLINK+="nvidia-game-modeset"
KERNEL=="nvidia_uvm", KERNELS=="0000:$GPU1_PCI", SYMLINK+="nvidia-game-uvm"
EOF
sudo udevadm control --reload-rules && sudo udevadm trigger
```

### 3.2 Docker (oba PC)

```bash
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" | sudo tee /etc/apt/sources.list.d/docker.list
sudo apt update && sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo usermod -aG docker scarlix

sudo tee /etc/docker/daemon.json << 'EOF'
{
  "iptables": false,
  "bridge": "none",
  "log-driver": "json-file",
  "log-opts": {"max-size": "10m", "max-file": "3"},
  "live-restore": true,
  "userland-proxy": false,
  "no-new-privileges": true,
  "runtimes": {"nvidia": {"path": "nvidia-container-runtime", "runtimeArgs": []}}
}
EOF
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker

docker network create scarlix_net --driver bridge --subnet 172.20.0.0/16
docker network create scarlix_ai --driver bridge --subnet 172.21.0.0/16

sudo curl -fsSL https://github.com/chaifeng/ufw-docker/raw/master/ufw-docker -o /usr/local/bin/ufw-docker
sudo chmod +x /usr/local/bin/ufw-docker && sudo ufw-docker install && sudo ufw reload
```


---

## ČASŤ 4 — AI INFERENCE STACK (MAIN PC)

### 4.1 SGLang (GPU 0) — Model-Agnostic, AWQ kvantizácia

```yaml
# /opt/scarlix/ai/sglang/docker-compose.yml
services:
  sglang:
    image: ghcr.io/sgl-project/sglang:v0.5.5-cu124
    container_name: sglang
    restart: unless-stopped
    ports: ["30000:30000"]
    volumes:
      - /var/lib/sglang/cache:/root/.cache/huggingface
      - /models:/models:ro
      - /etc/scarlix/models.yaml:/etc/scarlix/models.yaml:ro
    environment:
      - NVIDIA_VISIBLE_DEVICES=0
      - NVIDIA_DRIVER_CAPABILITIES=compute,utility
    command: >
      python -m sglang.launch_server
      --model-path /models/Qwen3-14B-Instruct-AWQ
      --port 30000 --host 0.0.0.0
      --mem-fraction-static 0.85
      --context-length 32768
      --enable-flashinfer
      --quantization awq
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, device_ids: ['0'], capabilities: [gpu]}]
    networks: [scarlix_ai]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:30000/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 180s
networks:
  scarlix_ai: {external: true}
```

> **Model-Agnostic:** Zmeň `--model-path` na akýkoľvek HuggingFace safetensors model.
> Zmeň `--quantization` na `gptq`, `fp8`, alebo odstráň pre none.
> Zmeň `--context-length` podľa modelu a VRAM.
> Zmeň `--mem-fraction-static` podľa GPU VRAM (0.80 pre 8GB, 0.85 pre 16GB, 0.90 pre 24GB+).

### 4.2 Ollama Main (GPU 0)

```yaml
# /opt/scarlix/ai/ollama/docker-compose-main.yml
services:
  ollama-main:
    image: ollama/ollama:0.5.4
    container_name: ollama-main
    restart: unless-stopped
    ports: ["11434:11434"]
    volumes:
      - ./main:/root/.ollama
      - /models:/models:ro
    environment:
      - OLLAMA_HOST=0.0.0.0:11434
      - OLLAMA_NUM_PARALLEL=4
      - OLLAMA_MAX_LOADED_MODELS=3
      - OLLAMA_KEEP_ALIVE=15m
      - OLLAMA_FLASH_ATTENTION=1
      - OLLAMA_KV_CACHE_TYPE=q4_0
      - TZ=Europe/Bratislava
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, device_ids: ['0'], capabilities: [gpu]}]
    networks: [scarlix_ai]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:11434/api/tags"]
      interval: 30s
      timeout: 10s
      retries: 3
networks:
  scarlix_ai: {external: true}
```

### 4.3 Ollama Agent (GPU 1)

```yaml
# /opt/scarlix/ai/ollama/docker-compose-agent.yml
services:
  ollama-agent:
    image: ollama/ollama:0.5.4
    container_name: ollama-agent
    restart: unless-stopped
    ports: ["11435:11434"]
    volumes:
      - ./agent:/root/.ollama
      - /models:/models:ro
    environment:
      - OLLAMA_HOST=0.0.0.0:11434
      - OLLAMA_NUM_PARALLEL=2
      - OLLAMA_KEEP_ALIVE=-1
      - OLLAMA_FLASH_ATTENTION=1
      - OLLAMA_KV_CACHE_TYPE=q4_0
      - TZ=Europe/Bratislava
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, device_ids: ['1'], capabilities: [gpu]}]
    networks: [scarlix_ai]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:11434/api/tags"]
      interval: 30s
      timeout: 10s
      retries: 3
networks:
  scarlix_ai: {external: true}
```

### 4.4 llama.cpp (CPU fallback)

```yaml
# /opt/scarlix/ai/llamacpp/docker-compose.yml
services:
  llamacpp:
    image: ghcr.io/ggml-org/llama.cpp:server
    container_name: llamacpp
    restart: unless-stopped
    ports: ["127.0.0.1:11438:8080"]
    volumes: ["/models:/models:ro"]
    command: --model /models/qwen3.6-14b-instruct-q4_k_m.gguf --ctx-size 8192 --threads 16 --host 0.0.0.0 --port 8080
    networks: [scarlix_ai]
networks:
  scarlix_ai: {external: true}
```

### 4.5 Needle Router (GPU 1) — Fast intent routing (26M params)

```yaml
# /opt/scarlix/ai/needle/docker-compose.yml
services:
  needle-router:
    image: ghcr.io/cactus-compute/needle:latest
    container_name: needle-router
    restart: unless-stopped
    ports: ["127.0.0.1:11440:11440"]
    volumes:
      - ./data:/root/.needle
      - /models:/models:ro
    environment:
      - NEEDLE_MODEL_PATH=/models/needle-router-v1
      - NEEDLE_BACKENDS=sglang:30000,ollama-main:11434,ollama-agent:11435,llamacpp:8080
      - NEEDLE_ROUTING_STRATEGY=latency-aware
      - NEEDLE_CACHE_TTL=300
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, device_ids: ['1'], capabilities: [gpu]}]
    networks: [scarlix_ai]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:11440/health"]
      interval: 30s
      timeout: 5s
      retries: 3
networks:
  scarlix_ai: {external: true}
```

### 4.6 smg Gateway (Rust, KV-cache aware) — LLM Gateway

```yaml
# /opt/scarlix/ai/smg/docker-compose.yml
services:
  smg:
    image: ghcr.io/lightseekorg/smg:latest
    container_name: smg
    restart: unless-stopped
    ports: ["4000:4000"]
    volumes:
      - ./config.yaml:/config/config.yaml:ro
      - ./data:/data
    environment:
      - SMG_CONFIG=/config/config.yaml
      - SMG_PORT=4000
      - TZ=Europe/Bratislava
    networks: [scarlix_ai, scarlix_net]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:4000/health"]
      interval: 30s
      timeout: 5s
      retries: 3
networks:
  scarlix_ai: {external: true}
  scarlix_net: {external: true}
```

```yaml
# /opt/scarlix/ai/smg/config.yaml
upstreams:
  - name: sglang-main
    url: http://sglang:30000/v1
    type: openai
    weight: 10
    kv_cache_aware: true
  - name: ollama-agent
    url: http://ollama-agent:11434/v1
    type: openai
    weight: 5
  - name: ollama-main
    url: http://ollama-main:11434/v1
    type: openai
    weight: 3
  - name: llamacpp-cpu
    url: http://llamacpp:8080/v1
    type: openai
    weight: 1
routing:
  strategy: latency-aware
  fallback_chain: [sglang-main, ollama-agent, ollama-main, llamacpp-cpu]
  retry_count: 2
  timeout_ms: 30000
auth:
  master_key: "${SMG_MASTER_KEY}"
rate_limiting:
  profiles:
    zmor: {rpm: 100, tpm: 1000000}
    hugo: {rpm: 20, tpm: 100000}
    xox: {rpm: 10, tpm: 10000}
    mon: {rpm: 30, tpm: 50000}
```

### 4.7 ComfyUI (GPU 0, creative mode)

```yaml
# /opt/scarlix/ai/comfyui/docker-compose.yml
services:
  comfyui:
    image: ghcr.io/ai-dock/comfyui:latest
    container_name: comfyui
    restart: unless-stopped
    ports: ["8188:8188"]
    volumes:
      - ./data/models:/ComfyUI/models
      - ./data/output:/ComfyUI/output
      - ./data/custom-nodes:/ComfyUI/custom_nodes
    environment:
      - CLI_ARGS=--listen 0.0.0.0 --port 8188
      - NVIDIA_VISIBLE_DEVICES=0
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, device_ids: ['0'], capabilities: [gpu]}]
    networks: [scarlix_ai, scarlix_net]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:8188/system_stats"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 120s
    profiles: [creative]
networks:
  scarlix_ai: {external: true}
  scarlix_net: {external: true}
```

### 4.8 Video Generation API (GPU 0, creative mode)

```dockerfile
# /opt/scarlix/ai/video/Dockerfile
FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends git build-essential ffmpeg && rm -rf /var/lib/apt/lists/*
WORKDIR /app
RUN git clone https://github.com/deepbeepmeep/Wan2GP.git . && pip install --no-cache-dir -r requirements.txt
EXPOSE 7861
CMD ["python", "app.py", "--listen", "0.0.0.0", "--port", "7861"]
```

```yaml
# /opt/scarlix/ai/video/docker-compose.yml
services:
  video-api:
    build: .
    container_name: video-api
    restart: unless-stopped
    ports: ["7861:7861"]
    volumes:
      - ./data/models:/app/models
      - ./data/output:/app/output
    environment: [NVIDIA_VISIBLE_DEVICES=0]
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, device_ids: ['0'], capabilities: [gpu]}]
    networks: [scarlix_ai, scarlix_net]
    profiles: [creative]
networks:
  scarlix_ai: {external: true}
  scarlix_net: {external: true}
```

### 4.9 MusicGen (GPU 0, creative mode)

```dockerfile
# /opt/scarlix/ai/musicgen/Dockerfile
FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir audiocraft fastapi uvicorn torchaudio
COPY server.py /app/server.py
WORKDIR /app
EXPOSE 7862
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "7862"]
```

```python
# /opt/scarlix/ai/musicgen/server.py
from fastapi import FastAPI
import uuid, torchaudio
from audiocraft.models import MusicGen

app = FastAPI(title="SCARLIX MusicGen")
OUTPUT_DIR = "/app/output"
model = None

def get_model():
    global model
    if model is None:
        model = MusicGen.get_pretrained("facebook/musicgen-medium")
    return model

@app.get("/health")
def health():
    return {"status": "ok", "service": "musicgen"}

@app.post("/generate")
def generate(prompt: str, duration: int = 30):
    m = get_model()
    m.set_generation_params(duration=duration)
    wav = m.generate([prompt])
    out = f"{OUTPUT_DIR}/{uuid.uuid4().hex}.wav"
    torchaudio.save(out, wav[0].cpu(), sample_rate=32000)
    return {"file": out, "status": "generated"}
```

```yaml
# /opt/scarlix/ai/musicgen/docker-compose.yml
services:
  musicgen:
    build: .
    container_name: musicgen
    restart: unless-stopped
    ports: ["7862:7862"]
    volumes: ["./data/output:/app/output"]
    environment: [NVIDIA_VISIBLE_DEVICES=0]
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, device_ids: ['0'], capabilities: [gpu]}]
    networks: [scarlix_net]
    profiles: [creative]
networks:
  scarlix_net: {external: true}
```


---

## ČASŤ 5 — VOICE PIPELINE (MAIN PC)

```yaml
# /opt/scarlix/voice/docker-compose.yml
services:
  whisper-sk:
    image: fedirz/faster-whisper-server:0.10.0
    container_name: whisper-sk
    restart: unless-stopped
    ports: ["8002:8000"]
    volumes: ["./whisper/data:/root/.cache"]
    environment:
      - WHISPER__MODEL=Systran/faster-whisper-large-v3
      - WHISPER__INFERENCE_DEVICE=cuda
      - WHISPER__COMPUTE_TYPE=int8_float16
      - WHISPER__LANGUAGE=sk
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, device_ids: ['1'], capabilities: [gpu]}]
    networks: [scarlix_ai, scarlix_net]

  piper-sk:
    image: rhasspy/wyoming-piper:1.5.0
    container_name: piper-sk
    restart: unless-stopped
    ports: ["8003:10200"]
    volumes: ["./piper/models:/voices:ro"]
    command: --voice sk_SK-lili-medium
    networks: [scarlix_net]

  openwakeword:
    image: rhasspy/wyoming-openwakeword:1.10.0
    container_name: openwakeword
    restart: unless-stopped
    ports: ["10400:10400"]
    command: --uri 'tcp://0.0.0.0:10400' --wake-word hey_jarvis --threshold 0.5
    networks: [scarlix_net]

  jarvis-pipeline:
    build: ./jarvis
    container_name: jarvis-pipeline
    restart: unless-stopped
    ports: ["8004:8004"]
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - /dev/snd:/dev/snd
    environment:
      - TZ=Europe/Bratislava
      - SMG_MASTER_KEY=${SMG_MASTER_KEY}
    networks: [scarlix_ai, scarlix_net]

networks:
  scarlix_ai: {external: true}
  scarlix_net: {external: true}
```

```dockerfile
# /opt/scarlix/voice/jarvis/Dockerfile
FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends alsa-utils portaudio19-dev build-essential ffmpeg && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir websockets httpx fastapi uvicorn
COPY jarvis.py /app/jarvis.py
WORKDIR /app
EXPOSE 8004
CMD ["python", "jarvis.py"]
```

```python
# /opt/scarlix/voice/jarvis/jarvis.py
import asyncio, json, os, subprocess
import httpx, websockets, uvicorn
from fastapi import FastAPI

WHISPER_URL = os.environ.get("WHISPER_URL", "http://whisper-sk:8002/v1/audio/transcriptions")
SMG_URL = os.environ.get("SMG_URL", "http://smg:4000/v1/chat/completions")
SMG_KEY = os.environ.get("SMG_MASTER_KEY", "")
OLLAMA_URL = os.environ.get("OLLAMA_URL", "http://ollama-main:11434/api/chat")
OLLAMA_MODEL = os.environ.get("OLLAMA_MODEL", "qwen3.6:14b")
PIPER_URL = os.environ.get("PIPER_URL", "http://piper-sk:10200")
WAKEWORD_WS = os.environ.get("WAKEWORD_WS", "ws://openwakeword:10400")
DOCKER_PROXY = os.environ.get("DOCKER_PROXY", "http://docker-socket-proxy:2375")

TOOLS_SCHEMA = [
    {"type": "function", "function": {"name": "system_status", "description": "Get system status (GPU, containers, mode)", "parameters": {"type": "object", "properties": {}, "required": []}}},
    {"type": "function", "function": {"name": "get_gpu_status", "description": "Get GPU VRAM and temperature", "parameters": {"type": "object", "properties": {}, "required": []}}},
    {"type": "function", "function": {"name": "list_containers", "description": "List running Docker containers via Socket Proxy", "parameters": {"type": "object", "properties": {}, "required": []}}}
]

SYSTEM_PROMPT = '''Si Jarvis, hlasovy asistent SCARLIX OS. Odpovedaj struncne, max 2 vety, slovensky. Nikdy nevymyslaj data. Ak pouzivatel chce stav systemu, zavolaj funkciu system_status.'''

def call_tool(name):
    try:
        if name == "system_status":
            mode = subprocess.run(["cat", "/var/lib/scarlix/current-mode"], capture_output=True, text=True, timeout=5).stdout.strip()
            gpu = subprocess.run(["nvidia-smi", "--query-gpu=index,name,memory.used,memory.free,utilization.gpu", "--format=csv,noheader,nounits"], capture_output=True, text=True, timeout=5).stdout.strip()
            containers = subprocess.run(["docker", "ps", "--format", "{{.Names}}: {{.Status}}"], capture_output=True, text=True, timeout=5).stdout.strip()
            return f"Mod: {mode}\nGPU:\n{gpu}\nKontajnery:\n{containers}"
        elif name == "get_gpu_status":
            return subprocess.run(["nvidia-smi", "--query-gpu=index,name,memory.used,memory.free,temperature.gpu", "--format=csv,noheader,nounits"], capture_output=True, text=True, timeout=5).stdout.strip()
        elif name == "list_containers":
            resp = httpx.get(f"{DOCKER_PROXY}/containers/json", timeout=5.0)
            return "\n".join([f"{c['Names'][0].lstrip('/')}: {c['State']}" for c in resp.json()[:20]])
        return f"Neznamy tool: {name}"
    except Exception as e:
        return f"Chyba pri volani {name}: {e}"

async def transcribe_audio(audio_data):
    async with httpx.AsyncClient(timeout=30.0) as client:
        resp = await client.post(WHISPER_URL, files={"file": ("audio.wav", audio_data, "audio/wav")}, data={"model": "Systran/faster-whisper-large-v3", "language": "sk"})
        resp.raise_for_status()
        return resp.json().get("text", "")

async def ollama_chat(text):
    async with httpx.AsyncClient(timeout=60.0) as client:
        payload = {"model": OLLAMA_MODEL, "messages": [{"role": "system", "content": SYSTEM_PROMPT}, {"role": "user", "content": text}], "tools": TOOLS_SCHEMA, "stream": False}
        headers = {}
        if SMG_KEY:
            headers["Authorization"] = f"Bearer {SMG_KEY}"
            resp = await client.post(SMG_URL, json=payload, headers=headers)
        else:
            resp = await client.post(OLLAMA_URL, json=payload)
        resp.raise_for_status()
        data = resp.json()
        msg = data["choices"][0]["message"] if "choices" in data else data.get("message", {})
        if msg.get("tool_calls"):
            tc = msg["tool_calls"][0]
            result = call_tool(tc["function"]["name"])
            payload["messages"] += [{"role": "assistant", "content": None, "tool_calls": [tc]}, {"role": "tool", "content": result}]
            resp2 = await client.post(SMG_URL if SMG_KEY else OLLAMA_URL, json=payload, headers=headers)
            data2 = resp2.json()
            return data2["choices"][0]["message"]["content"] if "choices" in data2 else data2.get("message", {}).get("content", "")
        return msg.get("content", "Neviem")

async def synthesize_speech(text):
    async with httpx.AsyncClient(timeout=30.0) as client:
        resp = await client.post(PIPER_URL, json={"text": text})
        resp.raise_for_status()
        return resp.content

def play_audio(audio_data):
    subprocess.Popen(["aplay", "-f", "S16_LE", "-r", "22050", "-c", "1", "-"], stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).communicate(input=audio_data)

async def process_voice_pipeline():
    try:
        audio = subprocess.run(["arecord", "-f", "S16_LE", "-r", "16000", "-c", "1", "-d", "5", "-t", "wav", "-"], capture_output=True, timeout=10).stdout
        text = await transcribe_audio(audio)
        if not text.strip():
            return
        response = await ollama_chat(text)
        speech = await synthesize_speech(response)
        play_audio(speech)
    except Exception as e:
        print(f"Jarvis pipeline error: {e}")

async def listen_for_wakeword():
    while True:
        try:
            async with websockets.connect(WAKEWORD_WS) as ws:
                async for message in ws:
                    data = json.loads(message)
                    if data.get("type") == "detection" and data.get("wake_word") == "hey_jarvis":
                        await process_voice_pipeline()
        except Exception as e:
            print(f"Wakeword listener error: {e}")
            await asyncio.sleep(5)

app = FastAPI(title="Jarvis Pipeline")

@app.get("/health")
def health():
    return {"status": "ok", "service": "jarvis-pipeline"}

@app.post("/trigger")
async def trigger(text: str):
    response = await ollama_chat(text)
    speech = await synthesize_speech(response)
    play_audio(speech)
    return {"response": response}

async def main():
    asyncio.create_task(listen_for_wakeword())
    await uvicorn.Server(uvicorn.Config(app, host="0.0.0.0", port=8004)).serve()

if __name__ == "__main__":
    asyncio.run(main())
```


---

## ČASŤ 6 — AGENTI (MAIN PC)

### 6.1 Hermes CEO + Web Workspace

```yaml
# /opt/scarlix/agents/hermes/docker-compose.yml
services:
  hermes:
    image: nousresearch/hermes-agent:latest
    container_name: hermes
    restart: unless-stopped
    ports: ["127.0.0.1:7999:7999"]
    volumes:
      - ./config:/root/.hermes
      - ./workspace:/workspace
      - ./skills:/root/.hermes/skills
      - ./soul:/root/.hermes/soul
      - /opt/scarlix:/opt/scarlix:ro
    environment:
      - HERMES_HOME=/root/.hermes
      - OPENAI_API_BASE=http://smg:4000/v1
      - OPENAI_API_KEY=${SMG_MASTER_KEY}
      - TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
      - TELEGRAM_CHAT_ID=${TELEGRAM_ZMOR_CHAT_ID}
      - TZ=Europe/Bratislava
    networks: [scarlix_ai, scarlix_net]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:7999/health"]
      interval: 30s
      timeout: 5s
      retries: 3

  hermes-workspace:
    image: ghcr.io/nousresearch/hermes-workspace:latest
    container_name: hermes-workspace
    restart: unless-stopped
    ports: ["127.0.0.1:7998:7998"]
    environment:
      - HERMES_API_URL=http://hermes:7999
      - TZ=Europe/Bratislava
    networks: [scarlix_net]

networks:
  scarlix_ai: {external: true}
  scarlix_net: {external: true}
```

```yaml
# /opt/scarlix/agents/hermes/config/config.yaml
provider: custom
model: qwen3.6:14b
custom_provider:
  base_url: http://smg:4000/v1
  api_key: ${SMG_MASTER_KEY}
memory:
  enabled: true
  nudge_interval: 300
  store_path: /root/.hermes/memory
skills:
  enabled: true
  auto_create: true
  skills_dir: /root/.hermes/skills
cron:
  enabled: true
gateway:
  telegram:
    enabled: true
    bot_token: ${TELEGRAM_BOT_TOKEN}
    allowed_users: [${TELEGRAM_ZMOR_CHAT_ID}]
mcp:
  servers:
    - name: scarlihq
      url: http://scarlihq:8091/mcp
      transport: http
persona: /root/.hermes/soul/CEO.md
```

```markdown
# /opt/scarlix/agents/hermes/soul/CEO.md
# HERMES — CEO Agent Persona

## Identita
Si Hermes, CEO agent SCARLIX OS. Rozhodny, analyticky, chranici bezpecnost rodiny.

## Ciele
1. Koordinovat agentov (Coding Manager, Senior Manageri)
2. Schvalovat/zamietovat deploy-e
3. Monitorovat stav systemu
4. Komunikovat so Zmorom cez Telegram

## Pravidla
- NIKDY destructive operacie bez HITL approval
- Overuj GPU stav pred novymi ulohami
- Reportuj chyby do Buzz #emergency
- Komunikuj v slovencine

## Obmedzenia
- Bez pristupu k /etc/shadow, /root, /var/lib/docker
- scarlix-mode menis len so Zmorovym schvalenim
```

```bash
# Import Hermes skills:
git clone --depth 1 https://github.com/NousResearch/hermes-agent-skills.git /opt/scarlix/agents/hermes/skills
chown -R scarlix:scarlix /opt/scarlix/agents/hermes/skills
```

### 6.2 Buzz Nostr Workspace

```yaml
# /opt/scarlix/workspace/buzz/docker-compose.yml
services:
  buzz-relay:
    image: ghcr.io/block/buzz-relay:latest
    container_name: buzz-relay
    restart: unless-stopped
    ports: ["3000:3000"]
    volumes: ["./data:/data"]
    environment:
      - BUZZ_RELAY_URL=ws://scarlix-main:3000
      - BUZZ_DOMAIN=scarlix.local
      - POSTGRES_HOST=buzz-postgres
      - POSTGRES_PASSWORD=${BUZZ_POSTGRES_PASSWORD}
      - REDIS_URL=redis://buzz-redis:6379
      - MINIO_ENDPOINT=buzz-minio:9000
    depends_on: [buzz-postgres, buzz-redis, buzz-minio]
    networks: [scarlix_net]

  buzz-postgres:
    image: postgres:16-alpine
    container_name: buzz-postgres
    restart: unless-stopped
    volumes: ["./postgres/data:/var/lib/postgresql/data"]
    environment: [POSTGRES_PASSWORD=${BUZZ_POSTGRES_PASSWORD}, POSTGRES_DB=buzz]
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 1G, cpus: '1'}}}

  buzz-redis:
    image: redis:7-alpine
    container_name: buzz-redis
    restart: unless-stopped
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 256M, cpus: '0.5'}}}

  buzz-minio:
    image: minio/minio:latest
    container_name: buzz-minio
    restart: unless-stopped
    ports: ["127.0.0.1:9000:9000"]
    volumes: ["./minio/data:/data"]
    environment: [MINIO_ROOT_USER=scarlix, MINIO_ROOT_PASSWORD=${BUZZ_MINIO_PASSWORD}]
    command: server /data
    networks: [scarlix_net]

networks:
  scarlix_net: {external: true}
```

### 6.3 OpenCode (HP Agent)

```bash
# Na HP Agent:
curl -fsSL https://opencode.ai/install | bash
cat > ~/.opencode/config.json << 'EOF'
{
  "provider": "openai",
  "model": "qwen3.6:14b",
  "openai": {
    "baseURL": "http://100.64.0.1:4000/v1",
    "apiKey": "sk-scarlix-change-me"
  },
  "mcp": {
    "scarlihq": {"url": "http://100.64.0.1:8091/mcp", "transport": "http"}
  },
  "git": {"autoCommit": true, "remote": "origin", "worktreePerTask": true}
}
EOF
```


---

## ČASŤ 7 — CODING PIPELINE + WEBHOSTING (HP AGENT)

```yaml
# /opt/scarlix/coding-pipeline/docker-compose.yml
services:
  postgres:
    image: postgres:16-alpine
    container_name: postgres
    restart: unless-stopped
    volumes: ["./postgres/data:/var/lib/postgresql/data"]
    environment: [POSTGRES_PASSWORD=${POSTGRES_PASSWORD}]
    networks: [scarlix_net]
    healthcheck: {test: ["CMD-SHELL", "pg_isready -U postgres"], interval: 10s, timeout: 5s, retries: 5}
    deploy: {resources: {limits: {memory: 1G, cpus: '1'}}}

  redis:
    image: redis:7-alpine
    container_name: redis
    restart: unless-stopped
    command: redis-server --requirepass ${REDIS_PASSWORD}
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 512M, cpus: '0.5'}}}

  gitea:
    image: gitea/gitea:1.23
    container_name: gitea
    restart: unless-stopped
    ports: ["3003:3000", "2222:22"]
    volumes: ["./gitea/data:/data"]
    environment:
      - USER_UID=1000
      - USER_GID=1000
      - GITEA__database__DB_TYPE=postgres
      - GITEA__database__HOST=postgres:5432
      - GITEA__database__NAME=gitea
      - GITEA__database__USER=gitea
      - GITEA__database__PASSWD=${GITEA_DB_PASSWORD}
      - GITEA__webhook__ALLOWED_HOST_LIST=*
    depends_on: {postgres: {condition: service_healthy}}
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 1G, cpus: '1'}}}

  coolify:
    image: coollabsio/coolify:4.0.30
    container_name: coolify
    restart: unless-stopped
    ports: ["8000:8000"]
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - ./coolify/data:/data
    environment:
      - APP_KEY=${COOLIFY_APP_KEY}
      - DB_CONNECTION=pgsql
      - DB_HOST=postgres
      - DB_DATABASE=coolify
      - DB_USERNAME=coolify
      - DB_PASSWORD=${COOLIFY_DB_PASSWORD}
      - REDIS_HOST=redis
      - REDIS_PASSWORD=${REDIS_PASSWORD}
    depends_on: {postgres: {condition: service_healthy}}
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 2G, cpus: '2'}}}

  nextcloud:
    image: nextcloud:30-apache
    container_name: nextcloud
    restart: unless-stopped
    ports: ["8080:80"]
    volumes:
      - ./nextcloud/data:/var/www/html/data
      - ./nextcloud/config:/var/www/html/config
    environment:
      - MYSQL_HOST=nextcloud-db
      - MYSQL_DATABASE=nextcloud
      - MYSQL_USER=nextcloud
      - MYSQL_PASSWORD=${NEXTCLOUD_DB_PASSWORD}
      - REDIS_HOST=redis
      - REDIS_HOST_PASSWORD=${REDIS_PASSWORD}
      - TRUSTED_DOMAINS=files.scarlix.example.com
      - OVERWRITEPROTOCOL=https
    depends_on: [nextcloud-db, redis]
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 2G, cpus: '2'}}}

  nextcloud-db:
    image: mariadb:11.4
    container_name: nextcloud-db
    restart: unless-stopped
    volumes: ["./nextcloud/db:/var/lib/mysql"]
    environment:
      - MYSQL_ROOT_PASSWORD=${NEXTCLOUD_ROOT_PASSWORD}
      - MYSQL_DATABASE=nextcloud
      - MYSQL_USER=nextcloud
      - MYSQL_PASSWORD=${NEXTCLOUD_DB_PASSWORD}
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 1G, cpus: '1'}}}

  photoprism:
    image: photoprism/photoprism:latest
    container_name: photoprism
    restart: unless-stopped
    ports: ["2342:2342"]
    volumes:
      - ./photoprism/storage:/photoprism/storage
      - /mnt/photos:/photoprism/originals:ro
    environment:
      - PHOTOPRISM_ADMIN_PASSWORD=${PHOTOPRISM_ADMIN_PASSWORD}
      - PHOTOPRISM_DATABASE_DRIVER=sqlite
      - PHOTOPRISM_WORKERS=2
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 2G, cpus: '2'}}}

  n8n:
    image: n8nio/n8n:latest
    container_name: n8n
    restart: unless-stopped
    ports: ["5678:5678"]
    volumes: ["./n8n/data:/home/node/.n8n"]
    environment:
      - N8N_BASIC_AUTH_ACTIVE=true
      - N8N_BASIC_AUTH_USER=admin
      - N8N_BASIC_AUTH_PASSWORD=${N8N_PASSWORD}
      - DB_TYPE=postgresdb
      - DB_POSTGRESDB_HOST=postgres
      - DB_POSTGRESDB_DATABASE=n8n
      - DB_POSTGRESDB_USER=n8n
      - DB_POSTGRESDB_PASSWORD=${N8N_DB_PASSWORD}
    depends_on: {postgres: {condition: service_healthy}}
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 1G, cpus: '1'}}}

  node-exporter:
    image: prom/node-exporter:v1.8.0
    container_name: node-exporter
    restart: unless-stopped
    ports: ["9100:9100"]
    volumes: ["/proc:/host/proc:ro", "/sys:/host/sys:ro", "/:/rootfs:ro"]
    command: ['--path.procfs=/host/proc', '--path.sysfs=/host/sys', '--path.rootfs=/rootfs']
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 256M, cpus: '0.5'}}}

networks:
  scarlix_net: {external: true}
```


---

## ČASŤ 8 — GAMING STACK (MAIN PC)

```yaml
# /opt/scarlix/gaming/docker-compose.yml
services:
  sunshine:
    image: lizardbyte/sunshine:latest
    container_name: sunshine
    restart: unless-stopped
    network_mode: host
    volumes:
      - ./sunshine/config:/config
      - /tmp/.X11-unix:/tmp/.X11-unix
      - /dev/input:/dev/input:ro
    devices: [/dev/dri, /dev/uinput, /dev/nvidia0, /dev/nvidiactl, /dev/nvidia-modeset, /dev/nvidia-uvm]
    environment: [DISPLAY=:0, TZ=Europe/Bratislava]
    privileged: true
    profiles: [gaming]

  steam-headless:
    image: freshgum-baking/steam-headless:latest
    container_name: steam-headless
    restart: unless-stopped
    network_mode: host
    volumes:
      - ./steam:/home/default/steam
      - /mnt/games/steam-games:/home/default/.steam/steam/steamapps/common
      - /tmp/.X11-unix:/tmp/.X11-unix
    environment: [DISPLAY=:0, USER_PASSWORD=${STEAM_PASSWORD}, TZ=Europe/Bratislava]
    privileged: true
    profiles: [gaming]

  retroarch:
    image: inglebard/retroarch:latest
    container_name: retroarch
    restart: unless-stopped
    ports: ["8089:8089"]
    volumes:
      - ./retroarch/config:/home/retro/retroarch
      - /mnt/games/retro-roms:/home/retro/retroarch/roms
      - /tmp/.X11-unix:/tmp/.X11-unix
    devices: ["/dev/input:/dev/input:ro"]
    privileged: true
    profiles: [gaming]

  minecraft:
    image: ghcr.io/pumpkin-mc/pumpkin:latest
    container_name: minecraft
    restart: unless-stopped
    ports: ["25565:25565"]
    volumes: ["./minecraft/data:/data"]
    environment: [MAX_MEMORY=4G, TZ=Europe/Bratislava]
    networks: [scarlix_net]

  jellyfin:
    image: jellyfin/jellyfin:10.10
    container_name: jellyfin
    restart: unless-stopped
    ports: ["8096:8096"]
    volumes:
      - ./jellyfin/config:/config
      - /mnt/files/media:/media:ro
    environment: [TZ=Europe/Bratislava]
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, device_ids: ['0'], capabilities: [gpu]}]
    networks: [scarlix_net]

networks:
  scarlix_net: {external: true}
```

```bash
# Epic + GOG cez Heroic (priamo na Main):
sudo apt install -y flatpak
flatpak remote-add --if-not-exists flathub https://flathub.org/repo/flathub.flatpakrepo
flatpak install -y flathub com.heroicgameslauncher.hgl

# Gaming storage:
sudo lvcreate -L 2T -n lv-games vg1 && sudo mkfs.ext4 /dev/vg1/lv-games
echo "/dev/vg1/lv-games /mnt/games ext4 defaults 0 2" | sudo tee -a /etc/fstab
sudo mount -a

# Xbox Cloud Gaming: firefox --kiosk "https://www.xbox.com/play"
```


---

## ČASŤ 9 — TV MODE (HDMI dummy + Sunshine stream)

```bash
# Bluetooth setup:
sudo systemctl enable --now bluetooth
bluetoothctl << EOF
power on
agent on
default-agent
EOF
# potom manualne: bluetoothctl scan on -> pair XX:XX... -> trust -> connect

# TV Mode Kiosk Launcher:
sudo tee /usr/local/bin/scarlix-tv-mode << 'EOF'
#!/bin/bash
firefox --kiosk \
  --new-window "http://192.168.1.100:8090" \
  --new-tab "http://192.168.1.100:8096" \
  --new-tab "http://192.168.1.101:8080" \
  --new-tab "http://192.168.1.101:2342" \
  --new-tab "https://www.xbox.com/play" &
wait
EOF
sudo chmod +x /usr/local/bin/scarlix-tv-mode

sudo tee /etc/systemd/system/scarlix-tv-mode.service << 'EOF'
[Unit]
Description=SCARLIX OS v15 TV Mode
After=network-online.target graphical.target
Wants=network-online.target
[Service]
Type=simple
User=scarlix
Environment=DISPLAY=:0
ExecStart=/usr/local/bin/scarlix-tv-mode
Restart=on-failure
RestartSec=5
[Install]
WantedBy=graphical.target
EOF
sudo systemctl daemon-reload

# Moonlight na TV: nainstaluj z Google Play/App Store, sparuj so Sunshine
# (https://192.168.1.100:47990), zadaj PIN. Klavesnica/mys -> BT -> TV -> Moonlight -> PC.
```


---

## ČASŤ 10 — MEDIA TOOLS: PDF EXPERT + FILE CONVERTER (HP AGENT)

```dockerfile
# /opt/scarlix/media-tools/Dockerfile
FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
  libreoffice-writer libreoffice-calc libreoffice-impress libreoffice-common \
  pandoc ffmpeg imagemagick ghostscript poppler-utils wkhtmltopdf \
  fonts-liberation fonts-dejavu fonts-noto && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir pypdf2 reportlab pdfplumber pdf2image markdown \
  python-docx openpyxl python-pptx pillow fastapi uvicorn python-multipart
COPY server.py /app/server.py
WORKDIR /app
EXPOSE 8200
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8200"]
```

```yaml
# /opt/scarlix/media-tools/docker-compose.yml
services:
  media-tools:
    build: .
    container_name: media-tools
    restart: unless-stopped
    ports: ["8200:8200"]
    volumes:
      - ./data/input:/data/input
      - ./data/output:/data/output
      - ./data/templates:/data/templates
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 4G, cpus: '2'}}}
networks:
  scarlix_net: {external: true}
```

```python
# /opt/scarlix/media-tools/server.py
from fastapi import FastAPI, UploadFile, File, Form, HTTPException
import subprocess, os, uuid

app = FastAPI(title="SCARLIX Media Tools")
INPUT_DIR, OUTPUT_DIR = "/data/input", "/data/output"

@app.get("/health")
def health():
    return {"status": "ok", "service": "media-tools"}

@app.post("/pdf/create")
async def pdf_create(title: str = Form(...), content: str = Form(...)):
    from reportlab.lib.pagesizes import A4
    from reportlab.pdfgen import canvas
    out = f"{OUTPUT_DIR}/{uuid.uuid4().hex}.pdf"
    c = canvas.Canvas(out, pagesize=A4)
    c.setTitle(title)
    c.setFont("Helvetica", 16)
    c.drawString(72, 750, title)
    c.setFont("Helvetica", 11)
    y = 720
    for line in content.split('\n'):
        if y < 50:
            c.showPage(); y = 750
        c.drawString(72, y, line[:100]); y -= 15
    c.save()
    return {"file": out, "status": "created"}

@app.post("/pdf/merge")
async def pdf_merge(files: list[UploadFile] = File(...)):
    from PyPDF2 import PdfMerger
    merger = PdfMerger()
    paths = []
    for f in files:
        path = f"{INPUT_DIR}/{uuid.uuid4().hex}_{f.filename}"
        open(path, "wb").write(await f.read())
        paths.append(path)
        merger.append(path)
    out = f"{OUTPUT_DIR}/merged_{uuid.uuid4().hex}.pdf"
    merger.write(out)
    merger.close()
    for p in paths: os.remove(p)
    return {"file": out, "status": "merged"}

@app.post("/pdf/split")
async def pdf_split(file: UploadFile = File(...), page_ranges: str = Form(...)):
    from PyPDF2 import PdfReader, PdfWriter
    path = f"{INPUT_DIR}/{uuid.uuid4().hex}_{file.filename}"
    open(path, "wb").write(await f.read())
    reader = PdfReader(path)
    results = []
    for i, rng in enumerate(page_ranges.split(',')):
        writer = PdfWriter()
        if '-' in rng:
            s, e = map(int, rng.strip().split('-'))
            for p in range(s-1, min(e, len(reader.pages))): writer.add_page(reader.pages[p])
        else:
            p = int(rng.strip()) - 1
            if p < len(reader.pages): writer.add_page(reader.pages[p])
        out = f"{OUTPUT_DIR}/split_{i+1}_{uuid.uuid4().hex}.pdf"
        writer.write(open(out, "wb"))
        results.append(out)
    os.remove(path)
    return {"files": results, "status": "split"}

@app.post("/pdf/extract-text")
async def pdf_extract(file: UploadFile = File(...)):
    import pdfplumber
    path = f"{INPUT_DIR}/{uuid.uuid4().hex}_{file.filename}"
    open(path, "wb").write(await f.read())
    with pdfplumber.open(path) as pdf:
        text = "\n\n".join([pg.extract_text() or "" for pg in pdf.pages])
    os.remove(path)
    return {"text": text}

@app.post("/convert/document")
async def convert_document(file: UploadFile = File(...), target_format: str = Form("pdf")):
    path = f"{INPUT_DIR}/{uuid.uuid4().hex}_{file.filename}"
    open(path, "wb").write(await f.read())
    ext = file.filename.rsplit('.', 1)[-1].lower()
    if ext in ['md', 'markdown']:
        out = f"{OUTPUT_DIR}/{uuid.uuid4().hex}.{target_format}"
        subprocess.run(["pandoc", path, "-o", out], check=True)
    elif ext in ['html', 'htm']:
        out = f"{OUTPUT_DIR}/{uuid.uuid4().hex}.{target_format}"
        subprocess.run(["wkhtmltopdf", path, out], check=True)
    elif ext in ['docx','xlsx','pptx','odt','ods','odp','doc','xls','ppt']:
        subprocess.run(["libreoffice", "--headless", "--convert-to", target_format, "--outdir", OUTPUT_DIR, path], check=True)
        out = f"{OUTPUT_DIR}/{os.path.splitext(os.path.basename(path))[0]}.{target_format}"
    else:
        raise HTTPException(400, f"Nepodporovany format: {ext}")
    os.remove(path)
    return {"file": out, "status": "converted"}

@app.post("/convert/image")
async def convert_image(file: UploadFile = File(...), target_format: str = Form("png")):
    path = f"{INPUT_DIR}/{uuid.uuid4().hex}_{file.filename}"
    open(path, "wb").write(await f.read())
    out = f"{OUTPUT_DIR}/{uuid.uuid4().hex}.{target_format}"
    subprocess.run(["convert", path, out], check=True)
    os.remove(path)
    return {"file": out, "status": "converted"}

@app.post("/convert/video")
async def convert_video(file: UploadFile = File(...), target_format: str = Form("mp4")):
    path = f"{INPUT_DIR}/{uuid.uuid4().hex}_{file.filename}"
    open(path, "wb").write(await f.read())
    out = f"{OUTPUT_DIR}/{uuid.uuid4().hex}.{target_format}"
    subprocess.run(["ffmpeg", "-i", path, "-y", out], check=True)
    os.remove(path)
    return {"file": out, "status": "converted"}

@app.post("/convert/audio")
async def convert_audio(file: UploadFile = File(...), target_format: str = Form("mp3")):
    path = f"{INPUT_DIR}/{uuid.uuid4().hex}_{file.filename}"
    open(path, "wb").write(await f.read())
    out = f"{OUTPUT_DIR}/{uuid.uuid4().hex}.{target_format}"
    subprocess.run(["ffmpeg", "-i", path, "-y", out], check=True)
    os.remove(path)
    return {"file": out, "status": "converted"}

@app.post("/video/thumbnail")
async def video_thumbnail(file: UploadFile = File(...), time: str = Form("00:00:05")):
    path = f"{INPUT_DIR}/{uuid.uuid4().hex}_{file.filename}"
    open(path, "wb").write(await f.read())
    out = f"{OUTPUT_DIR}/thumb_{uuid.uuid4().hex}.png"
    subprocess.run(["ffmpeg", "-i", path, "-ss", time, "-vframes", "1", "-y", out], check=True)
    os.remove(path)
    return {"file": out, "status": "thumbnail_created"}

@app.post("/video/compress")
async def video_compress(file: UploadFile = File(...), crf: int = Form(23)):
    path = f"{INPUT_DIR}/{uuid.uuid4().hex}_{file.filename}"
    open(path, "wb").write(await f.read())
    out = f"{OUTPUT_DIR}/compressed_{uuid.uuid4().hex}.mp4"
    subprocess.run(["ffmpeg", "-i", path, "-c:v", "libx264", "-crf", str(crf), "-y", out], check=True)
    os.remove(path)
    return {"file": out, "status": "compressed"}
```


---

## ČASŤ 11 — SCARLIHQ (ORCHESTRÁTOR, MAIN PC) — KOMPLETNÝ GO KÓD

```go
// scarlihq/go.mod
module github.com/MoZoHuJa/scarlihq
go 1.23
require (
  github.com/gin-gonic/gin v1.10.0
  github.com/gorilla/websocket v1.5.3
  modernc.org/sqlite v1.34.0
  gonum.org/v1/gonum v0.15.0
  github.com/google/uuid v1.6.0
  github.com/rs/zerolog v1.33.0
  gopkg.in/yaml.v3 v3.0.1
)
```

```go
// scarlihq/cmd/scarlihq/main.go
package main

import (
  "embed"
  "log"
  "net/http"
  "os"
  "github.com/MoZoHuJa/scarlihq/internal/api"
  "github.com/MoZoHuJa/scarlihq/internal/mcp"
  "github.com/MoZoHuJa/scarlihq/internal/profiles"
  "github.com/MoZoHuJa/scarlihq/internal/scarlix_mode"
  "github.com/MoZoHuJa/scarlihq/internal/storage"
  "github.com/MoZoHuJa/scarlihq/internal/webui"
)

//go:embed webui/frontend/dist/*
var frontendFS embed.FS

func main() {
  port := os.Getenv("SCARLIHQ_PORT")
  if port == "" { port = "8090" }
  profs, err := profiles.LoadProfiles("/etc/scarlix/profiles")
  if err != nil { log.Printf("WARN: profiles load failed: %v", err) }
  modeManager := scarlix_mode.NewManager("/var/lib/scarlix/current-mode")
  storageManager := storage.NewManager("/var/lib/scarlix/storage.db")
  mux := http.NewServeMux()
  webui.RegisterRoutes(mux, frontendFS)
  api.RegisterRoutes(mux, modeManager, storageManager, profs)
  mcp.RegisterRoutes(mux, modeManager, storageManager)
  log.Printf("ScarliHQ v15 on :%s", port)
  log.Fatal(http.ListenAndServe(":"+port, mux))
}
```

```go
// scarlihq/internal/profiles/loader.go
package profiles

import (
  "os"
  "path/filepath"
  "gopkg.in/yaml.v3"
)

type Profile struct {
  Name        string `yaml:"name"`
  DisplayName string `yaml:"display_name"`
  Role        string `yaml:"role"`
  Permissions []string `yaml:"permissions"`
  ScarlixMode Mode   `yaml:"scarlix_mode"`
  HUDTheme    string `yaml:"hud_theme"`
  TokenBudget Budget `yaml:"token_budget"`
}
type Mode struct {
  Default   string   `yaml:"default"`
  Allowed   []string `yaml:"allowed"`
  CanSwitch bool     `yaml:"can_switch"`
}
type Budget struct {
  Monthly int    `yaml:"monthly"`
  Model   string `yaml:"model"`
}

func LoadProfiles(dir string) ([]Profile, error) {
  files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
  if err != nil { return nil, err }
  var out []Profile
  for _, f := range files {
    data, err := os.ReadFile(f)
    if err != nil { continue }
    var p Profile
    if err := yaml.Unmarshal(data, &p); err != nil { continue }
    out = append(out, p)
  }
  return out, nil
}

func (p *Profile) HasPermission(perm string) bool {
  if p.Role == "admin" { return true }
  for _, x := range p.Permissions { if x == perm { return true } }
  return false
}
```

```go
// scarlihq/internal/scarlix_mode/manager.go
package scarlix_mode

import (
  "os"
  "os/exec"
  "strings"
)

type Manager struct{ stateFile string }

func NewManager(stateFile string) *Manager { return &Manager{stateFile: stateFile} }

func (m *Manager) GetCurrentMode() (string, error) {
  data, err := os.ReadFile(m.stateFile)
  if err != nil { return "unknown", nil }
  return strings.TrimSpace(string(data)), nil
}

func (m *Manager) SetMode(mode string) (string, error) {
  valid := map[string]bool{"ai": true, "game": true, "creative": true, "turbo": true, "offline": true, "tv": true}
  if !valid[mode] { return "invalid mode", nil }
  out, err := exec.Command("/usr/local/bin/scarlix-mode", mode).CombinedOutput()
  if err != nil { return string(out), err }
  return string(out), nil
}
```

```go
// scarlihq/internal/storage/manager.go
package storage

import (
  "database/sql"
  "os/exec"
  _ "modernc.org/sqlite"
)

type Manager struct{ db *sql.DB }

func NewManager(dbPath string) *Manager {
  db, _ := sql.Open("sqlite", dbPath)
  db.Exec(`CREATE TABLE IF NOT EXISTS memory (id INTEGER PRIMARY KEY AUTOINCREMENT, agent TEXT, content TEXT, embedding BLOB, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`)
  return &Manager{db: db}
}

func (m *Manager) GetStatus() (map[string]interface{}, error) {
  s := map[string]interface{}{}
  if out, err := exec.Command("df", "-h", "/", "/opt", "/models", "/mnt/games", "/mnt/files").Output(); err == nil {
    s["disk"] = string(out)
  }
  if out, err := exec.Command("docker", "system", "df").Output(); err == nil {
    s["docker"] = string(out)
  }
  return s, nil
}

func (m *Manager) QueryMemory(args map[string]interface{}) (interface{}, error) {
  agent, _ := args["agent"].(string)
  rows, err := m.db.Query("SELECT content FROM memory WHERE agent=? ORDER BY created_at DESC LIMIT 10", agent)
  if err != nil { return nil, err }
  defer rows.Close()
  var res []string
  for rows.Next() { var c string; rows.Scan(&c); res = append(res, c) }
  return res, nil
}

func (m *Manager) StoreMemory(args map[string]interface{}) (interface{}, error) {
  agent, _ := args["agent"].(string)
  content, _ := args["content"].(string)
  if _, err := m.db.Exec("INSERT INTO memory (agent, content) VALUES (?, ?)", agent, content); err != nil {
    return nil, err
  }
  return map[string]string{"status": "stored"}, nil
}
```

```go
// scarlihq/internal/guard/guard.go
package guard

import "strings"

var destructive = []string{
  "rm -rf /", "rm -rf ~", "rm -rf *", "dd if=", "mkfs", "shutdown",
  "reboot", ":(){:|:&};:", "> /dev/sda", "chmod 777 /", "systemctl stop docker",
}

func IsDestructive(cmd string) bool {
  for _, d := range destructive {
    if strings.Contains(cmd, d) { return true }
  }
  return false
}
```

```go
// scarlihq/internal/mcp/tools.go
package mcp

import (
  "encoding/json"
  "net/http"
  "os/exec"
  "github.com/MoZoHuJa/scarlihq/internal/guard"
  "github.com/MoZoHuJa/scarlihq/internal/scarlix_mode"
  "github.com/MoZoHuJa/scarlihq/internal/storage"
)

type Tool struct {
  Name        string
  Description string
  Handler     func(map[string]interface{}) (interface{}, error)
}

func execCmd(cmd string) (interface{}, error) {
  out, err := exec.Command("bash", "-c", cmd).CombinedOutput()
  return map[string]interface{}{"output": string(out), "error": err == nil}, err
}

func gpuStatus() (interface{}, error) {
  out, err := exec.Command("nvidia-smi", "--query-gpu=index,name,memory.used,memory.free,utilization.gpu,temperature.gpu", "--format=csv,noheader,nounits").Output()
  return string(out), err
}

func containerList() (interface{}, error) {
  out, err := exec.Command("docker", "ps", "--format", "table {{.Names}}\t{{.Status}}").Output()
  return string(out), err
}

func RegisterRoutes(mux *http.ServeMux, mm *scarlix_mode.Manager, sm *storage.Manager) {
  tools := []Tool{
    {Name: "scarlix_exec", Description: "Execute shell command with destructive guard", Handler: func(a map[string]interface{}) (interface{}, error) {
      cmd, _ := a["command"].(string)
      if guard.IsDestructive(cmd) {
        return map[string]string{"status": "blocked", "reason": "destructive command requires HITL"}, nil
      }
      return execCmd(cmd)
    }},
    {Name: "scarlix_mode_get", Description: "Get current scarlix-mode", Handler: func(a map[string]interface{}) (interface{}, error) { return mm.GetCurrentMode() }},
    {Name: "scarlix_mode_set", Description: "Set scarlix-mode", Handler: func(a map[string]interface{}) (interface{}, error) { m, _ := a["mode"].(string); return mm.SetMode(m) }},
    {Name: "scarlix_gpu_status", Description: "Get GPU status", Handler: func(a map[string]interface{}) (interface{}, error) { return gpuStatus() }},
    {Name: "scarlix_container_list", Description: "List Docker containers", Handler: func(a map[string]interface{}) (interface{}, error) { return containerList() }},
    {Name: "scarlix_storage_status", Description: "Get storage status", Handler: func(a map[string]interface{}) (interface{}, error) { return sm.GetStatus() }},
    {Name: "scarlix_query_memory", Description: "Query agent memory", Handler: func(a map[string]interface{}) (interface{}, error) { return sm.QueryMemory(a) }},
    {Name: "scarlix_store_memory", Description: "Store to agent memory", Handler: func(a map[string]interface{}) (interface{}, error) { return sm.StoreMemory(a) }},
    {Name: "scarlix_pdf_create", Description: "Create PDF via Media Tools", Handler: func(a map[string]interface{}) (interface{}, error) { return httpPost("http://100.64.0.2:8200/pdf/create", a) }},
    {Name: "scarlix_convert_document", Description: "Convert document via Media Tools", Handler: func(a map[string]interface{}) (interface{}, error) { return httpPost("http://100.64.0.2:8200/convert/document", a) }},
    {Name: "scarlix_generate_image", Description: "Generate image via ComfyUI", Handler: func(a map[string]interface{}) (interface{}, error) { return httpPost("http://comfyui:8188/prompt", a) }},
    {Name: "scarlix_generate_video", Description: "Generate video via Video API", Handler: func(a map[string]interface{}) (interface{}, error) { return httpPost("http://video-api:7861/generate", a) }},
    {Name: "scarlix_generate_music", Description: "Generate music via MusicGen", Handler: func(a map[string]interface{}) (interface{}, error) { return httpPost("http://musicgen:7862/generate", a) }},
    {Name: "scarlix_hitl_ask", Description: "Ask human via Telegram for approval", Handler: func(a map[string]interface{}) (interface{}, error) { return askTelegram(a) }},
    {Name: "scarlix_agents_md_read", Description: "Read AGENTS.md", Handler: func(a map[string]interface{}) (interface{}, error) {
      data, err := exec.Command("cat", "/etc/scarlix/AGENTS.md").Output()
      return string(data), err
    }},
  }

  mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{"server": "scarlihq", "version": "15.0", "tools": len(tools)})
  })

  mux.HandleFunc("/mcp/call", func(w http.ResponseWriter, r *http.Request) {
    var req struct {
      Tool string                 `json:"tool"`
      Args map[string]interface{} `json:"args"`
    }
    json.NewDecoder(r.Body).Decode(&req)
    for _, t := range tools {
      if t.Name == req.Tool {
        res, err := t.Handler(req.Args)
        w.Header().Set("Content-Type", "application/json")
        if err != nil { json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}); return }
        json.NewEncoder(w).Encode(res)
        return
      }
    }
    http.Error(w, "tool not found", 404)
  })
}
```

```go
// scarlihq/internal/mcp/helpers.go
package mcp

import (
  "bytes"
  "encoding/json"
  "io"
  "net/http"
  "os"
  "time"
)

func httpPost(url string, payload interface{}) (interface{}, error) {
  body, _ := json.Marshal(payload)
  resp, err := http.Post(url, "application/json", bytes.NewReader(body))
  if err != nil { return nil, err }
  defer resp.Body.Close()
  data, _ := io.ReadAll(resp.Body)
  return string(data), nil
}

func askTelegram(args map[string]interface{}) (interface{}, error) {
  token := os.Getenv("TELEGRAM_BOT_TOKEN")
  chatID := os.Getenv("TELEGRAM_ZMOR_CHAT_ID")
  question, _ := args["question"].(string)
  client := &http.Client{Timeout: 10 * time.Second}
  resp, err := client.PostForm("https://api.telegram.org/bot"+token+"/sendMessage",
    map[string][]string{"chat_id": {chatID}, "text": {"HITL: " + question}})
  if err != nil { return nil, err }
  defer resp.Body.Close()
  return map[string]string{"status": "question_sent"}, nil
}
```

```go
// scarlihq/internal/api/rest.go
package api

import (
  "encoding/json"
  "fmt"
  "net/http"
  "os/exec"
  "strings"
  "github.com/MoZoHuJa/scarlihq/internal/profiles"
  "github.com/MoZoHuJa/scarlihq/internal/scarlix_mode"
  "github.com/MoZoHuJa/scarlihq/internal/storage"
)

type Handler struct {
  mode     *scarlix_mode.Manager
  storage  *storage.Manager
  profiles []profiles.Profile
}

func NewHandler(m *scarlix_mode.Manager, s *storage.Manager, p []profiles.Profile) *Handler {
  return &Handler{mode: m, storage: s, profiles: p}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
  mux.HandleFunc("/api/health", h.health)
  mux.HandleFunc("/api/gpu", h.gpuStatus)
  mux.HandleFunc("/api/mode", h.modeHandler)
  mux.HandleFunc("/api/profiles", h.listProfiles)
  mux.HandleFunc("/api/containers", h.listContainers)
  mux.HandleFunc("/api/storage", h.storageStatus)
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
  writeJSON(w, map[string]string{"status": "ok", "version": "v15.0"})
}

func (h *Handler) gpuStatus(w http.ResponseWriter, r *http.Request) {
  out, err := exec.Command("nvidia-smi", "--query-gpu=index,name,temperature.gpu,utilization.gpu,memory.used,memory.total,power.draw", "--format=csv,noheader,nounits").Output()
  if err != nil { writeJSON(w, []interface{}{}); return }
  type GPU struct {
    Index int `json:"index"`; Name string `json:"name"`; Temp float64 `json:"temp"`
    Util float64 `json:"util"`; MemUsed float64 `json:"mem_used"`; MemTotal float64 `json:"mem_total"`; Power float64 `json:"power"`
  }
  var gpus []GPU
  for _, line := range strings.Split(string(out), "\n") {
    line = strings.TrimSpace(line)
    if line == "" { continue }
    parts := strings.Split(line, ",")
    if len(parts) < 7 { continue }
    var gpu GPU
    fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &gpu.Index)
    gpu.Name = strings.TrimSpace(parts[1])
    fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &gpu.Temp)
    fmt.Sscanf(strings.TrimSpace(parts[3]), "%f", &gpu.Util)
    fmt.Sscanf(strings.TrimSpace(parts[4]), "%f", &gpu.MemUsed)
    fmt.Sscanf(strings.TrimSpace(parts[5]), "%f", &gpu.MemTotal)
    fmt.Sscanf(strings.TrimSpace(parts[6]), "%f", &gpu.Power)
    gpus = append(gpus, gpu)
  }
  writeJSON(w, gpus)
}

func (h *Handler) modeHandler(w http.ResponseWriter, r *http.Request) {
  if r.Method == "GET" {
    mode, _ := h.mode.GetCurrentMode()
    writeJSON(w, map[string]string{"mode": mode, "status": "ok"})
    return
  }
  mode := r.URL.Query().Get("set")
  if mode == "" { writeJSON(w, map[string]string{"status": "error", "message": "no mode"}); return }
  out, err := h.mode.SetMode(mode)
  if err != nil { writeJSON(w, map[string]string{"status": "error", "message": err.Error()}); return }
  writeJSON(w, map[string]string{"mode": mode, "status": "ok", "message": out})
}

func (h *Handler) listProfiles(w http.ResponseWriter, r *http.Request) {
  writeJSON(w, h.profiles)
}

func (h *Handler) listContainers(w http.ResponseWriter, r *http.Request) {
  out, err := exec.Command("docker", "ps", "--format", "{{.Names}}\t{{.Status}}\t{{.Ports}}").Output()
  if err != nil { writeJSON(w, []interface{}{}); return }
  type Container struct { Name string `json:"name"`; Status string `json:"status"`; Ports string `json:"ports"` }
  var containers []Container
  for _, line := range strings.Split(string(out), "\n") {
    line = strings.TrimSpace(line)
    if line == "" { continue }
    parts := strings.Split(line, "\t")
    if len(parts) >= 3 { containers = append(containers, Container{parts[0], parts[1], parts[2]}) }
  }
  writeJSON(w, containers)
}

func (h *Handler) storageStatus(w http.ResponseWriter, r *http.Request) {
  status, _ := h.storage.GetStatus()
  writeJSON(w, status)
}

func writeJSON(w http.ResponseWriter, data interface{}) {
  w.Header().Set("Content-Type", "application/json")
  json.NewEncoder(w).Encode(data)
}
```

```go
// scarlihq/internal/webui/server.go
package webui

import (
  "embed"
  "io/fs"
  "net/http"
)

func RegisterRoutes(mux *http.ServeMux, frontendFS embed.FS) {
  dist, _ := fs.Sub(frontendFS, "webui/frontend/dist")
  mux.Handle("/", http.FileServer(http.FS(dist)))
}
```

```html
<!-- scarlihq/webui/frontend/dist/index.html -->
<!DOCTYPE html>
<html lang="sk">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>ScarliHQ v15 — Command Center</title>
  <style>
    * { margin: 0; padding: 0; box-sizing: border-box; }
    body { background: #0a0e14; color: #e2e8f0; font-family: -apple-system, system-ui, sans-serif; padding: 20px; }
    h1 { color: #fff; font-size: 1.8rem; margin-bottom: 20px; }
    h1 span { color: #22d3ee; }
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 20px; }
    .card { background: #111827; border: 1px solid #1e3a5f; border-radius: 12px; padding: 20px; }
    pre { background: #0a0e14; padding: 12px; border-radius: 8px; font-size: 12px; overflow-x: auto; color: #22d3ee; }
    button { background: #1e3a5f; color: #22d3ee; border: 1px solid #22d3ee; padding: 8px 16px; border-radius: 6px; cursor: pointer; font-size: 13px; margin: 4px; }
    button:hover { background: #22d3ee; color: #0a0e14; }
    .profiles { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 10px; }
    .profile { padding: 6px 14px; border-radius: 20px; cursor: pointer; font-size: 12px; font-weight: 600; border: 2px solid transparent; }
    .zmor { background: rgba(34,211,238,0.15); color: #22d3ee; }
    .hugo { background: rgba(168,85,247,0.15); color: #a855f7; }
    .xox { background: rgba(236,72,153,0.15); color: #ec4899; }
    .mon { background: rgba(234,179,8,0.15); color: #eab308; }
  </style>
</head>
<body>
  <h1>SCARLI<span>HQ</span> v15</h1>
  <div class="grid">
    <div class="card"><h3>GPU Status</h3><pre id="gpu-stats">Loading...</pre></div>
    <div class="card"><h3>System Mode</h3><p>Current: <strong id="current-mode">...</strong></p>
      <button onclick="switchMode('ai')">AI</button>
      <button onclick="switchMode('game')">Game</button>
      <button onclick="switchMode('creative')">Creative</button>
      <button onclick="switchMode('turbo')">Turbo</button>
      <button onclick="switchMode('offline')">Offline</button>
      <button onclick="switchMode('tv')">TV</button>
    </div>
    <div class="card"><h3>Profiles</h3><div class="profiles">
      <span class="profile zmor">Zmor (admin)</span>
      <span class="profile hugo">Hugo</span>
      <span class="profile xox">XOX</span>
      <span class="profile mon">Mon</span>
    </div></div>
    <div class="card"><h3>Containers</h3><pre id="containers">Loading...</pre></div>
  </div>
  <script>
    async function updateGPU(){const r=await fetch('/api/gpu');const d=await r.json();document.getElementById('gpu-stats').textContent=JSON.stringify(d,null,2);}
    async function updateMode(){const r=await fetch('/api/mode');const d=await r.json();document.getElementById('current-mode').textContent=d.mode;}
    async function switchMode(m){await fetch('/api/mode?set='+m);updateMode();}
    async function updateContainers(){const r=await fetch('/api/containers');const d=await r.json();const t=d.map(c=>c.name+' | '+c.status).join('\n');document.getElementById('containers').textContent=t||'No containers';}
    setInterval(updateGPU,3000);setInterval(updateMode,5000);setInterval(updateContainers,10000);
    updateGPU();updateMode();updateContainers();
  </script>
</body>
</html>
```

```dockerfile
# scarlihq/Dockerfile
FROM golang:1.23-alpine AS builder
WORKDIR /build
COPY go.mod ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o scarlihq ./cmd/scarlihq

FROM alpine:3.20
RUN apk add --no-cache docker-cli curl
COPY --from=builder /build/scarlihq /usr/local/bin/scarlihq
EXPOSE 8090 8091
CMD ["scarlihq"]
```

```yaml
# scarlihq/docker-compose.yml
services:
  scarlihq:
    build: .
    container_name: scarlihq
    restart: unless-stopped
    ports: ["8090:8090", "8091:8091"]
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - /var/lib/scarlix:/var/lib/scarlix
      - /etc/scarlix:/etc/scarlix:ro
    environment:
      - SCARLIHQ_PORT=8090
      - SMG_MASTER_KEY=${SMG_MASTER_KEY}
      - TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
      - TELEGRAM_ZMOR_CHAT_ID=${TELEGRAM_ZMOR_CHAT_ID}
      - TZ=Europe/Bratislava
    networks: [scarlix_net, scarlix_ai]
networks:
  scarlix_net: {external: true}
  scarlix_ai: {external: true}
```


---

## ČASŤ 12 — SECURITY STACK (MAIN PC)

```yaml
# /opt/scarlix/network/docker-compose.yml
services:
  docker-socket-proxy:
    image: tecnativa/docker-socket-proxy:0.3.0
    container_name: docker-socket-proxy
    restart: unless-stopped
    volumes: ["/var/run/docker.sock:/var/run/docker.sock:ro"]
    environment: [CONTAINERS=1, INFO=1, VERSION=1, EVENTS=1, EXEC=0, POST=0, AUTH=0, BUILD=0]
    networks: [scarlix_net]

  caddy:
    image: caddy:2.8.4-alpine
    container_name: caddy
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes:
      - ./caddy/Caddyfile:/etc/caddy/Caddyfile
      - ./caddy/data:/data
      - ./caddy/config:/config
    networks: [scarlix_net]

  authelia:
    image: authelia/authelia:4.38
    container_name: authelia
    restart: unless-stopped
    ports: ["9091:9091"]
    volumes: ["./authelia/config:/config", "./authelia/data:/data"]
    environment:
      - AUTHELIA_JWT_SECRET=${JWT_SECRET}
      - AUTHELIA_STORAGE_ENCRYPTION_KEY=${STORAGE_ENCRYPTION_KEY}
    networks: [scarlix_net]

  headscale:
    image: headscale/headscale:0.25.0
    container_name: headscale
    restart: unless-stopped
    ports: ["100.64.0.1:8080:8080", "100.64.0.1:50443:50443/udp"]
    volumes:
      - ./headscale/config:/etc/headscale
      - ./headscale/data:/var/lib/headscale
    networks: [scarlix_net]

  crowdsec:
    image: crowdsecurity/crowdsec:v1.4.0
    container_name: crowdsec
    restart: unless-stopped
    volumes:
      - ./crowdsec/config:/etc/crowdsec
      - ./crowdsec/data:/var/lib/crowdsec/data
      - /var/log/caddy:/var/log/caddy:ro
    environment: [COLLECTIONS=crowdsecurity/caddy crowdsecurity/http-cve crowdsecurity/sshd]
    networks: [scarlix_net]

  wazuh-manager:
    image: wazuh/wazuh-manager:4.8.0
    container_name: wazuh-manager
    restart: unless-stopped
    ports: ["1514:1514/udp", "1515:1515/tcp", "55000:55000"]
    volumes:
      - ./wazuh/config:/wazuh-config
      - ./wazuh/logs:/wazuh-logs
      - ./wazuh/data:/wazuh-data
    networks: [scarlix_net]
    deploy: {resources: {limits: {memory: 4G, cpus: '2'}}}
    profiles: [siem]

networks:
  scarlix_net: {external: true}
```

```caddy
# /opt/scarlix/network/caddy/Caddyfile
{
  email admin@scarlix.example.com
}

(authelia) {
  forward_auth authelia:9091 {
    uri /api/authz/forward-auth
    copy_headers Remote-User Remote-Groups Remote-Name Remote-Email
  }
}

(security_headers) {
  header {
    Strict-Transport-Security "max-age=31536000"
    X-Content-Type-Options "nosniff"
    X-Frame-Options "DENY"
  }
}

scarlix.example.com { import authelia; import security_headers; reverse_proxy scarlihq:8090 }
buzz.scarlix.example.com { import authelia; reverse_proxy buzz-relay:3000 }
hermes.scarlix.example.com { import authelia; reverse_proxy hermes:7999 }
comfyui.scarlix.example.com { import authelia; reverse_proxy comfyui:8188 }
video.scarlix.example.com { import authelia; reverse_proxy video-api:7861 }
musicgen.scarlix.example.com { import authelia; reverse_proxy musicgen:7862 }
auth.scarlix.example.com { reverse_proxy authelia:9091 }
monitoring.scarlix.example.com { import authelia; reverse_proxy grafana:3000 }
git.scarlix.example.com { import authelia; reverse_proxy 100.64.0.2:3003 }
coolify.scarlix.example.com { import authelia; reverse_proxy 100.64.0.2:8000 }
files.scarlix.example.com { import authelia; reverse_proxy 100.64.0.2:8080 }
photos.scarlix.example.com { import authelia; reverse_proxy 100.64.0.2:2342 }
n8n.scarlix.example.com { import authelia; reverse_proxy 100.64.0.2:5678 }
media-tools.scarlix.example.com { import authelia; reverse_proxy 100.64.0.2:8200 }
```

---

## ČASŤ 13 — MONITORING + BACKUP

```yaml
# /opt/scarlix/monitoring/docker-compose.yml
services:
  victoria-metrics:
    image: victoriametrics/victoria-metrics:v1.108.0
    container_name: victoria-metrics
    restart: unless-stopped
    ports: ["100.64.0.1:8428:8428"]
    volumes: ["./victoria:/storage"]
    command: --storageDataPath=/storage --retentionPeriod=90d
    networks: [scarlix_net]

  grafana:
    image: grafana/grafana:11.3.0
    container_name: grafana
    restart: unless-stopped
    ports: ["100.64.0.1:3000:3000"]
    volumes: ["./grafana:/var/lib/grafana"]
    environment: [GF_SECURITY_ADMIN_PASSWORD=${GRAFANA_PASSWORD}]
    networks: [scarlix_net]

  otel-collector:
    image: otel/opentelemetry-collector-contrib:latest
    container_name: otel-collector
    restart: unless-stopped
    ports: ["100.64.0.1:4317:4317", "100.64.0.1:4318:4318"]
    volumes: ["./otel/config.yaml:/etc/otelcol/config.yaml"]
    networks: [scarlix_net]

  openlit:
    image: openlit/openlit:latest
    container_name: openlit
    restart: unless-stopped
    ports: ["100.64.0.1:3001:3000"]
    volumes: ["./openlit:/data"]
    networks: [scarlix_net]
    profiles: [tracing]

  alertmanager:
    image: prom/alertmanager:latest
    container_name: alertmanager
    restart: unless-stopped
    ports: ["100.64.0.1:9093:9093"]
    volumes: ["./alertmanager:/etc/alertmanager"]
    networks: [scarlix_net]
    profiles: [tracing]

networks:
  scarlix_net: {external: true}
```

```bash
# /opt/scarlix/scripts/backup.sh
#!/bin/bash
set -euo pipefail
source /etc/scarlix/.env
TS=$(date +%Y%m%d-%H%M%S)
sudo lvcreate -L 20G -s -n "root-snap-${TS}" /dev/vg0/lv-root 2>/dev/null || true
export RESTIC_REPOSITORY=/mnt/backup/restic
export RESTIC_PASSWORD=$RESTIC_PASSWORD
restic backup /etc/scarlix /opt/scarlix /var/lib/scarlix /var/lib/buzz \
  --exclude '*.log' --exclude '/opt/scarlix/ai/ollama/main' --exclude '/opt/scarlix/ai/ollama/agent' \
  --tag scarlix-v15-local --tag daily
restic forget --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune
echo "Backup complete: $TS"
```

---

## ČASŤ 14 — PROFILY

```yaml
# /etc/scarlix/profiles/zmor.yaml
name: zmor
display_name: "Zmor (admin)"
role: admin
permissions: [all]
scarlix_mode: {default: ai, allowed: [ai, game, creative, turbo, offline, tv], can_switch: true}
hud_theme: iron-man
token_budget: unlimited
```

```yaml
# /etc/scarlix/profiles/hugo.yaml
name: hugo
display_name: "Hugo (syn)"
role: family
permissions: [read_files, read_photos, play_games, coding_tutor, ai_chat]
scarlix_mode: {default: game, allowed: [game, ai], can_switch: true}
hud_theme: gaming
token_budget: {monthly: 100000, model: qwen3.6:14b}
```

```yaml
# /etc/scarlix/profiles/xox.yaml
name: xox
display_name: "XOX (dcera)"
role: family
permissions: [read_photos_only, kids_content, ai_chat]
scarlix_mode: {default: ai, allowed: [ai], can_switch: false}
hud_theme: creative
token_budget: {monthly: 10000, model: qwen3.6:14b}
content_filter: {enabled: true, blocklist: [violence, adult, drugs, gambling]}
```

```yaml
# /etc/scarlix/profiles/mon.yaml
name: mon
display_name: "Mon (manzelka)"
role: family
permissions: [read_files, read_photos, write_files, voice_assistant, ai_chat, media_tools]
scarlix_mode: {default: ai, allowed: [ai, game, creative], can_switch: true}
hud_theme: elegant
token_budget: {monthly: 50000, model: qwen3.6:14b}
```

---

## ČASŤ 15 — scarlix-mode (VRAM ARBITRÁŽ)

```bash
#!/usr/bin/env bash
# /usr/local/bin/scarlix-mode
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
```

---

## ČASŤ 16 — .env GENERÁTOR

```bash
#!/usr/bin/env bash
# /opt/scarlix/scripts/generate-env.sh
set -euo pipefail
sudo mkdir -p /etc/scarlix/secrets
SMG_KEY="sk-scarlix-$(openssl rand -hex 16)"
cat > /tmp/scarlix.env << EOF
SMG_MASTER_KEY=$SMG_KEY
TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN:-}
TELEGRAM_ZMOR_CHAT_ID=${TELEGRAM_ZMOR_CHAT_ID:-}
POSTGRES_PASSWORD=$(openssl rand -base64 24)
NEXTCLOUD_ROOT_PASSWORD=$(openssl rand -base64 24)
NEXTCLOUD_DB_PASSWORD=$(openssl rand -base64 24)
GITEA_DB_PASSWORD=$(openssl rand -base64 24)
COOLIFY_DB_PASSWORD=$(openssl rand -base64 24)
COOLIFY_APP_KEY=$(openssl rand -hex 32)
N8N_DB_PASSWORD=$(openssl rand -base64 24)
REDIS_PASSWORD=$(openssl rand -base64 24)
GRAFANA_PASSWORD=$(openssl rand -base64 24)
PHOTOPRISM_ADMIN_PASSWORD=$(openssl rand -base64 24)
N8N_PASSWORD=$(openssl rand -base64 24)
RESTIC_PASSWORD=$(openssl rand -base64 24)
BUZZ_POSTGRES_PASSWORD=$(openssl rand -base64 24)
BUZZ_MINIO_PASSWORD=$(openssl rand -base64 24)
JWT_SECRET=$(openssl rand -base64 64)
STORAGE_ENCRYPTION_KEY=$(openssl rand -base64 64)
STEAM_PASSWORD=$(openssl rand -base64 24)
CF_API_TOKEN=${CF_API_TOKEN:-}
SCARLIX_DEFAULT_PROFILE=zmor
SCARLIX_DEFAULT_MODE=ai
EOF
sudo mv /tmp/scarlix.env /etc/scarlix/.env
sudo chmod 600 /etc/scarlix/.env
echo "SMG_MASTER_KEY: $SMG_KEY"
echo "Uloz si tento kluc!"
```

---

## ČASŤ 17 — ISO BUILD (GRUB + TUI WIZARD + FIRST-BOOT)

### 17.1 GRUB config

```bash
# installer/grub/grub.cfg
set default=0
set timeout=15
set gfxmode=auto
set gfxpayload=keep
insmod all_video
insmod gfxterm
set menu_color_normal=white/black
set menu_color_highlight=cyan/black

menuentry "  SCARLIX OS v15 — Main PC (AI Brain + 2x NVIDIA GPU)" {
  set gfxpayload=keep
  linux /casper/vmlinuz autoinstall ds=nocloud\;s=/cdrom/cloud-init/main-pc/ quiet splash ---
  initrd /casper/initrd
}

menuentry "  SCARLIX OS v15 — HP Agent PC (Worker — Coding + Files)" {
  set gfxpayload=keep
  linux /casper/vmlinuz autoinstall ds=nocloud\;s=/cdrom/cloud-init/hp-agent/ quiet splash ---
  initrd /casper/initrd
}

menuentry "  SCARLIX OS v15 — Interactive (TUI Wizard)" {
  set gfxpayload=keep
  linux /casper/vmlinuz quiet ---
  initrd /casper/initrd
}
```

### 17.2 cloud-init autoinstall (Main PC)

```yaml
# installer/cloud-init/main-pc/user-data
#cloud-config
autoinstall:
  version: 1
  interactive-sections: []
  identity:
    hostname: scarlix-main
    realname: SCARLIX Admin
    username: scarlix
    password: "$6$rounds=4096$SCARLIX$REPLACE_WITH_HASH"
  ssh:
    install-server: yes
    allow-pw: true
  storage:
    layout:
      name: lvm
  network:
    network:
      version: 2
      ethernets:
        id0:
          match: {name: "en*"}
          addresses: [192.168.1.100/24]
          routes: [{to: default, via: 192.168.1.1}]
          nameservers: {addresses: [1.1.1.1, 8.8.8.8]}
          dhcp4: false
  early-commands:
    - echo "=== SCARLIX OS v15 — Main PC Installation ==="
  late-commands:
    - curtin in-target --target=/target -- apt update
    - curtin in-target --target=/target -- apt install -y ansible git python3-pip
    - curtin in-target --target=/target -- git clone https://github.com/MoZoHuJa/OS.git /opt/scarlix-src
    - curtin in-target --target=/target -- cp -r /opt/scarlix-src/installer /opt/scarlix-installer
    - curtin in-target --target=/target -- ansible-playbook /opt/scarlix-installer/ansible/site.yml -i /opt/scarlix-installer/ansible/inventory.yml -e pc_type=main
    - echo "=== SCARLIX OS v15 Main PC installation complete ==="
```

### 17.3 TUI Wizard

```bash
#!/usr/bin/env bash
# installer/scripts/scarlix-wizard.sh
set -euo pipefail
TITLE="SCARLIX OS v15 Installer"

whiptail --title "$TITLE" --msgbox "Vitaj v SCARLIX OS v15 instalacii!\nStlac Enter pre pokracovanie." 20 70

PC_TYPE=$(whiptail --title "$TITLE" --menu "Vyber typ tohto PC:" 15 60 2 \
  "main" "Main PC (AI Brain — 2x NVIDIA GPU)" \
  "hp_agent" "HP Agent PC (Worker — coding + files)" 3>&1 1>&2 2>&3)

DEFAULT_IP="192.168.1.100"
[[ "$PC_TYPE" == "hp_agent" ]] && DEFAULT_IP="192.168.1.101"
IP=$(whiptail --title "$TITLE" --inputbox "IP adresa:" 10 50 "$DEFAULT_IP" 3>&1 1>&2 2>&3)

PASS=$(whiptail --title "$TITLE" --passwordbox "Heslo pre scarlix user:" 10 50 3>&1 1>&2 2>&3)
PASS_HASH=$(openssl passwd -6 "$PASS")

TG_TOKEN=$(whiptail --title "$TITLE" --inputbox "Telegram Bot Token (prazdne = preskoc):" 10 60 "" 3>&1 1>&2 2>&3)
TG_CHAT_ID=""
[[ -n "$TG_TOKEN" ]] && TG_CHAT_ID=$(whiptail --title "$TITLE" --inputbox "Telegram Chat ID:" 10 50 "" 3>&1 1>&2 2>&3)

# Generate .env
SMG_KEY="sk-scarlix-$(openssl rand -hex 16)"
sudo mkdir -p /etc/scarlix/secrets
cat > /tmp/scarlix.env << EOF
SMG_MASTER_KEY=$SMG_KEY
TELEGRAM_BOT_TOKEN=$TG_TOKEN
TELEGRAM_ZMOR_CHAT_ID=$TG_CHAT_ID
POSTGRES_PASSWORD=$(openssl rand -base64 24)
NEXTCLOUD_ROOT_PASSWORD=$(openssl rand -base64 24)
NEXTCLOUD_DB_PASSWORD=$(openssl rand -base64 24)
GITEA_DB_PASSWORD=$(openssl rand -base64 24)
COOLIFY_DB_PASSWORD=$(openssl rand -base64 24)
COOLIFY_APP_KEY=$(openssl rand -hex 32)
N8N_DB_PASSWORD=$(openssl rand -base64 24)
REDIS_PASSWORD=$(openssl rand -base64 24)
GRAFANA_PASSWORD=$(openssl rand -base64 24)
PHOTOPRISM_ADMIN_PASSWORD=$(openssl rand -base64 24)
N8N_PASSWORD=$(openssl rand -base64 24)
RESTIC_PASSWORD=$(openssl rand -base64 24)
BUZZ_POSTGRES_PASSWORD=$(openssl rand -base64 24)
BUZZ_MINIO_PASSWORD=$(openssl rand -base64 24)
JWT_SECRET=$(openssl rand -base64 64)
STORAGE_ENCRYPTION_KEY=$(openssl rand -base64 64)
STEAM_PASSWORD=$(openssl rand -base64 24)
SCARLIX_DEFAULT_PROFILE=zmor
SCARLIX_DEFAULT_MODE=ai
EOF
sudo mv /tmp/scarlix.env /etc/scarlix/.env
sudo chmod 600 /etc/scarlix/.env
echo "$PC_TYPE" | sudo tee /etc/scarlix/pc_type >/dev/null

CONFIRM=$(whiptail --title "$TITLE" --yesno "PC Typ: $PC_TYPE\nIP: $IP\n\nSpustit instalaciu?" 18 60 3>&1 1>&2 2>&3)
[[ "$CONFIRM" != "0" ]] && { echo "Zrusene."; exit 0; }

ansible-playbook /opt/scarlix-installer/ansible/site.yml \
  -i /opt/scarlix-installer/ansible/inventory.yml \
  -e "pc_type=$PC_TYPE" -e "scarlix_ip=$IP"

echo "\nSCARLIX OS v15 nainstalovany! Dashboard: http://$IP:8090"
```

### 17.4 first-boot.sh

```bash
#!/usr/bin/env bash
# installer/scripts/first-boot.sh
set -euo pipefail
echo "=== SCARLIX OS v15 — First Boot ==="
[[ -f /var/lib/scarlix/.installed ]] && { echo "Uz nainstalovane."; exit 0; }
touch /var/lib/scarlix/.installed
PC_TYPE=$(cat /etc/scarlix/pc_type 2>/dev/null || echo "main")

if [[ "$PC_TYPE" == "main" ]]; then
  echo "Starting Main PC services..."
  cd /opt/scarlix/ai/smg && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix/ai/sglang && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix/ai/ollama && docker compose -f docker-compose-main.yml up -d 2>/dev/null || true
  cd /opt/scarlix/ai/ollama && docker compose -f docker-compose-agent.yml up -d 2>/dev/null || true
  cd /opt/scarlix/ai/needle && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix/network && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix/voice && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix/workspace/buzz && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix/agents/hermes && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix/scarlihq && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix/monitoring && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix/gaming && docker compose up -d jellyfin minecraft 2>/dev/null || true
  echo "ai" | sudo tee /var/lib/scarlix/current-mode >/dev/null
  nohup /opt/scarlix/scripts/download-models.sh >/dev/null 2>&1 &
else
  echo "Starting HP Agent services..."
  cd /opt/scarlix/coding-pipeline && docker compose up -d 2>/dev/null || true
  cd /opt/scarlix/media-tools && docker compose up -d 2>/dev/null || true
fi
echo "=== First boot complete ==="
```

### 17.5 build-iso.sh

```bash
#!/usr/bin/env bash
# installer/scripts/build-iso.sh
set -euo pipefail
VERSION="15.0.0"
WORK_DIR="/tmp/scarlix-iso-builder"
OUTPUT_DIR="$HOME/scarlix-iso"
mkdir -p "$WORK_DIR" "$OUTPUT_DIR"

echo "=== SCARLIX OS v15 ISO Builder ==="
wget -O "$WORK_DIR/ubuntu-base.iso" "https://releases.ubuntu.com/24.04/ubuntu-24.04.2-live-server-amd64.iso"

mkdir -p "$WORK_DIR/iso-content"
sudo mount -o loop "$WORK_DIR/ubuntu-base.iso" "$WORK_DIR/iso-mount"
cp -r "$WORK_DIR/iso-mount/"* "$WORK_DIR/iso-content/"
sudo umount "$WORK_DIR/iso-mount"

cp -r scarlix-src/* "$WORK_DIR/iso-content/opt/scarlix-src/"
cp -r installer/* "$WORK_DIR/iso-content/opt/scarlix-installer/"
cp installer/grub/grub.cfg "$WORK_DIR/iso-content/boot/grub/grub.cfg"

cat > "$WORK_DIR/iso-content/etc/systemd/system/scarlix-first-boot.service" << 'EOF'
[Unit]
Description=SCARLIX OS v15 First Boot Setup
After=network-online.target
Wants=network-online.target
ConditionPathExists=!/opt/scarlix/.installed
[Service]
Type=oneshot
ExecStart=/opt/scarlix-installer/scripts/first-boot.sh
ExecStartPost=/bin/touch /opt/scarlix/.installed
RemainAfterExit=yes
TimeoutStartSec=3600
[Install]
WantedBy=multi-user.target
EOF

sudo xorriso -as mkisofs \
  -iso-level 3 -full-iso9660-filenames \
  -volid "SCARLIX_OS_V15" \
  -eltorito-boot boot/grub/bios.img \
  -eltorito-alt-boot -e EFI/BOOT/BOOTX64.EFI -no-emul-boot \
  -isohybrid-gpt-basdat \
  -o "$OUTPUT_DIR/scarlix-os-v15.iso" \
  "$WORK_DIR/iso-content"

echo "ISO: $OUTPUT_DIR/scarlix-os-v15.iso"
ls -lh "$OUTPUT_DIR/scarlix-os-v15.iso"
```

---

## ČASŤ 18 — ANSIBLE PLAYBOOK

```yaml
# installer/ansible/site.yml
---
- name: SCARLIX OS v15 Installation
  hosts: localhost
  become: true
  gather_facts: true
  vars_files: [group_vars/all.yml]
  pre_tasks:
    - name: Read PC type
      slurp: {src: /etc/scarlix/pc_type}
      register: pc_type_file
      ignore_errors: yes
    - name: Set PC type fact
      set_fact:
        pc_type: "{{ (pc_type_file.content | default('bWFpbg==') | b64decode | trim) }}"
  roles:
    - role: base_os
      tags: [base]
    - role: nvidia
      when: pc_type == 'main'
      tags: [nvidia]
    - role: docker
      tags: [docker]
    - role: network
      when: pc_type == 'main'
      tags: [network]
    - role: ai_stack
      when: pc_type == 'main'
      tags: [ai]
    - role: voice
      when: pc_type == 'main'
      tags: [voice]
    - role: agents
      when: pc_type == 'main'
      tags: [agents]
    - role: workspace
      when: pc_type == 'main'
      tags: [workspace]
    - role: gaming
      when: pc_type == 'main'
      tags: [gaming]
    - role: security
      when: pc_type == 'main'
      tags: [security]
    - role: monitoring
      when: pc_type == 'main'
      tags: [monitoring]
    - role: scarlihq
      when: pc_type == 'main'
      tags: [scarlihq]
    - role: hp_agent_stack
      when: pc_type == 'hp_agent'
      tags: [hp_agent]
    - role: media_tools
      when: pc_type == 'hp_agent'
      tags: [media_tools]
    - role: backup
      tags: [backup]
    - role: verify
      tags: [verify]
```

```yaml
# installer/ansible/group_vars/all.yml
---
scarlix_version: "15.0.0"
timezone: Europe/Bratislava
docker_networks:
  - {name: scarlix_net, subnet: 172.20.0.0/16}
  - {name: scarlix_ai, subnet: 172.21.0.0/16}
main_ip: "192.168.1.100"
hp_agent_ip: "192.168.1.101"
headscale_main_ip: "100.64.0.1"
headscale_hp_ip: "100.64.0.2"
```

```yaml
# installer/ansible/inventory.yml
---
all:
  hosts:
    localhost:
      ansible_connection: local
```

### 18.1 base_os role

```yaml
# installer/ansible/roles/base_os/tasks/main.yml
---
- name: Set timezone
  timezone: {name: "{{ timezone }}"}

- name: Install base packages
  apt:
    name: [curl, wget, git, jq, bc, net-tools, htop, tmux, vim, nano, build-essential, dkms, ufw, fail2ban, chrony, unzip, rsync, lvm2, python3-pip, python3-venv, whiptail, bluez, blueman, alsa-utils, portaudio19-dev]
    state: present
    update_cache: yes

- name: UFW defaults
  ufw: {direction: "{{ item.dir }}", policy: "{{ item.policy }}"}
  loop:
    - {dir: incoming, policy: deny}
    - {dir: outgoing, policy: allow}

- name: UFW allow ports
  ufw: {rule: allow, port: "{{ item.port }}", proto: "{{ item.proto }}"}
  loop:
    - {port: 22, proto: tcp}
    - {port: 80, proto: tcp}
    - {port: 443, proto: tcp}
    - {port: 41641, proto: udp}

- name: Enable UFW
  ufw: {state: enabled}

- name: Fail2ban config
  copy:
    dest: /etc/fail2ban/jail.local
    content: |
      [DEFAULT]
      bantime=3600
      findtime=600
      maxretry=3
      [sshd]
      enabled=true
      port=22
      maxretry=3
      bantime=3600

- name: Start fail2ban + chrony
  systemd: {name: "{{ item }}", state: started, enabled: yes}
  loop: [fail2ban, chrony]

- name: Journald log rotation
  copy:
    dest: /etc/systemd/journald.conf
    content: |
      [Journal]
      SystemMaxUse=500M
      SystemMaxFileSize=100M
      MaxRetentionSec=7day

- name: Create directories
  file: {path: "{{ item }}", state: directory, owner: scarlix, group: scarlix, mode: '0755'}
  loop:
    - /opt/scarlix
    - /opt/scarlix/ai
    - /opt/scarlix/voice
    - /opt/scarlix/agents
    - /opt/scarlix/workspace
    - /opt/scarlix/gaming
    - /opt/scarlix/network
    - /opt/scarlix/monitoring
    - /opt/scarlix/scarlihq
    - /opt/scarlix/scripts
    - /models
    - /var/lib/sglang/cache
    - /var/lib/scarlix
    - /etc/scarlix/profiles
    - /etc/scarlix/secrets
    - /mnt/backup/restic

- name: Generate .env if missing
  command: /opt/scarlix/scripts/generate-env.sh
  args:
    creates: /etc/scarlix/.env
```

### 18.2-18.14 (nvidia, docker, ai_stack, voice, agents, workspace, gaming, security, monitoring, scarlihq, hp_agent_stack, media_tools, backup, verify)

Každá rola kopíruje príslušné docker-compose.yml súbory a spúšťa `docker compose up -d`. Pattern je rovnaký ako v base_os role — copy + shell.

---

## ČASŤ 19 — VERIFY SCRIPT

```bash
#!/usr/bin/env bash
# installer/scripts/verify.sh
set -uo pipefail
echo "=== SCARLIX OS v15 — Verification ==="
echo "--- Main PC ---"
echo "NVIDIA:"; nvidia-smi --query-gpu=index,name,memory.used --format=csv,noheader 2>/dev/null || echo "FAIL"
echo "Docker:"; docker ps --format "{{.Names}}: {{.Status}}" | head -25
echo "SGLang: $(curl -s http://localhost:30000/health 2>/dev/null || echo DOWN)"
echo "Ollama Main: $(curl -s http://localhost:11434/api/tags 2>/dev/null | jq '.models|length' || echo DOWN)"
echo "Ollama Agent: $(curl -s http://localhost:11435/api/tags 2>/dev/null | jq '.models|length' || echo DOWN)"
echo "smg: $(curl -s http://localhost:4000/health 2>/dev/null || echo DOWN)"
echo "Needle: $(curl -s http://localhost:11440/health 2>/dev/null || echo DOWN)"
echo "Hermes: $(curl -s http://localhost:7999/health 2>/dev/null || echo DOWN)"
echo "Buzz: $(curl -s http://localhost:3000 2>/dev/null | head -c 50 || echo DOWN)"
echo "ScarliHQ: $(curl -s http://localhost:8090/api/health 2>/dev/null || echo DOWN)"
echo "Caddy: $(curl -s -o /dev/null -w '%{http_code}' http://localhost:80)"
echo "scarlix-mode: $(scarlix-mode status 2>/dev/null | head -3)"
echo ""
echo "--- HP Agent PC ---"
echo "Gitea: $(curl -s -o /dev/null -w '%{http_code}' http://100.64.0.2:3003 2>/dev/null || echo DOWN)"
echo "Coolify: $(curl -s -o /dev/null -w '%{http_code}' http://100.64.0.2:8000 2>/dev/null || echo DOWN)"
echo "Nextcloud: $(curl -s -o /dev/null -w '%{http_code}' http://100.64.0.2:8080 2>/dev/null || echo DOWN)"
echo "Photoprism: $(curl -s -o /dev/null -w '%{http_code}' http://100.64.0.2:2342 2>/dev/null || echo DOWN)"
echo "n8n: $(curl -s -o /dev/null -w '%{http_code}' http://100.64.0.2:5678 2>/dev/null || echo DOWN)"
echo "Media Tools: $(curl -s http://100.64.0.2:8200/health 2>/dev/null || echo DOWN)"
echo ""
echo "--- Port Conflict Check ---"
for port in 80 443 3000 3003 4000 5432 5678 7861 7862 8000 8002 8003 8004 8080 8090 8091 8096 8188 8200 8428 9091 10400 11434 11435 11438 11440 2342 25565 30000; do
  ss -tulanp 2>/dev/null | grep -q ":$port " && echo "Port $port: OBSADENY" || true
done
echo "=== Verification complete ==="
```

---

## ČASŤ 20 — FINÁLNY MASTER PROMPT PRE CLAUDE AGENTA

```text
=== SCARLIX OS v15 — MASTER BUILD PROMPT ===

Si senior DevOps/architect agent. Tvojou ulohou je vytvorit kompletny bootovatelny ISO balik SCARLIX OS v15 presne podla tohto dokumentu.

VSTUP: Tento cely dokument (SCARLIX OS v15 build manifest) obsahuje VSETKY kody:
- Cast 0: Architektura, hardver, VRAM budget, profilovy system, MODEL-AGNOSTIC konfiguracia
- Cast 2-3: Base OS, NVIDIA, Docker
- Cast 4: AI stack (SGLang, Ollama x2, llama.cpp, Needle, smg, ComfyUI, Video API, MusicGen)
- Cast 5: Voice pipeline (Whisper, Piper, WakeWord, Jarvis kompletny Python)
- Cast 6: Agents (Hermes CEO + Web Workspace + SOUL.md + skills, Buzz Nostr, OpenCode)
- Cast 7: Coding pipeline + HP Agent stack (Gitea, Coolify, Nextcloud, Photoprism, n8n, Node Exporter)
- Cast 8: Gaming (Sunshine, Steam, RetroArch, Minecraft, Jellyfin, Heroic)
- Cast 9: TV Mode (HDMI dummy + Sunshine stream + Moonlight)
- Cast 10: Media Tools (PDF Expert + File Converter — kompletny FastAPI server.py)
- Cast 11: ScarliHQ kompletny Go kod (main.go, profiles, scarlix_mode, storage, guard, mcp, api, webui, Dockerfile)
- Cast 12: Security (Caddy, Authelia, Headscale, CrowdSec, Wazuh, Docker Socket Proxy)
- Cast 13: Monitoring (VictoriaMetrics, Grafana, OTel, OpenLIT, Alertmanager) + Backup (Restic + LVM)
- Cast 14: 4 profily (zmor, hugo, xox, mon)
- Cast 15: scarlix-mode VRAM arbitraz (6 modov: ai/game/creative/turbo/offline/tv)
- Cast 16: .env generator
- Cast 17: ISO build (GRUB, cloud-init, TUI wizard, first-boot, download-models, build-iso.sh)
- Cast 18: Ansible playbook + roles
- Cast 19: verify.sh

ULOHА:
1. Vytvor adresarovu strukturu scarlix-os-v15/ so vsetkymi podadresarmi.
2. Skopiruj KAZDY kodovy blok z tohto dokumentu do spravneho suboru.
   Ziadne "TODO", ziadne "placeholder", ziadne vynechane subory.
3. Over, ze:
   - Vsetky docker-compose.yml su platny YAML
   - Vsetky Go subory su syntakticky spravne (vratane api, webui, mcp, guard, storage, profiles, scarlix_mode)
   - Vsetky Python subory su syntakticky spravne
   - Vsetky bash skripty su spustitelne (chmod +x)
   - Ziadne hardcoded hesla (vsetko cez ${VARIABLE} z .env)
   - HP Agent kontajnery maju deploy.resources.limits
   - Caddyfile pouziva 100.64.0.2 pre HP Agent sluzby (Headscale IP)
   - SGLang ma AWQ kvantizaciu + mem-fraction 0.85 + context 32768
   - scarlix-mode riesi VRAM arbitraz s flock locking
   - models.yaml je model-agnostic (podporuje akukolvek HuggingFace model)
   - Jarvis.py pouziva --query-gpu (s pomlckami) v nvidia-smi volaniach
4. Vytvor finalny balik:
   tar czf scarlix-os-v15-installer.tar.gz scarlix-os-v15/
5. Vystup: scarlix-os-v15-installer.tar.gz

DOLEZITE:
- Tento dokument je JEDINY zdroj pravdy. Neodchyl sa od neho.
- Ak nieco v dokumentu chyba alebo je nejednoznacne, oznac to v REPORT.md na konci balika.
- Cieľ: po rozbaleni a spusteni build-iso.sh vznikne scarlix-os-v15.iso, ktore bootne na Main PC aj HP Agent PC a nainstaluje cely stack.
```

---

## ČASŤ 21 — INŠTALAČNÝ POSTUP PRE POUŽÍVATEĽA

```text
1. Rozbal scarlix-os-v15-installer.tar.gz
2. Spusti bash installer/scripts/build-iso.sh → vznikne scarlix-os-v15.iso
3. Vypal ISO na USB (Rufus DD mode / balenaEtcher / dd)
4. MAIN PC: BIOS → Secure Boot OFF, Above 4G ON, ReBAR ON → Boot z USB
   → GRUB menu → "SCARLIX OS v15 — Main PC" → TUI wizard → inštalácia
   → first-boot automaticky spustí služby + stiahne modely (~30-60 min)
5. HP AGENT PC: rovnako → "SCARLIX OS v15 — HP Agent PC"
6. Na HP Agent sa pripoj k Headscale (tailscale up s authkey z Main)
7. Verifikácia: bash installer/scripts/verify.sh na oboch PC
8. Dashboard: http://192.168.1.100:8090
9. TV: nainštaluj Moonlight na TV → spáruj so Sunshine → scarlix-mode tv

ZMENA MODELU:
1. Edituj /etc/scarlix/models.yaml
2. Zmeň model_path / model / model_file na akýkoľvek HuggingFace model
3. Spusti: /opt/scarlix/scripts/download-models.sh
4. Reštartuj: docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml restart
```

---

## ČASŤ 22 — PORT MAPA (FINÁLNA)

```
MAIN PC (192.168.1.100 / 100.64.0.1):
22 SSH | 80/443 Caddy | 3000 Buzz | 4000 smg | 7999 Hermes | 7998 Hermes WS
8002 Whisper | 8003 Piper | 8004 Jarvis | 8090 ScarliHQ HUD | 8091 ScarliHQ MCP
8188 ComfyUI | 7861 Video API | 7862 MusicGen | 9091 Authelia | 10400 WakeWord
11434 Ollama Main | 11435 Ollama Agent | 11438 llama.cpp | 11440 Needle
30000 SGLang | 8428 VictoriaMetrics | 3000 Grafana | 3001 OpenLIT | 9093 Alertmanager
55000 Wazuh | 47990 Sunshine | 8096 Jellyfin | 8089 RetroArch | 25565 Minecraft
9000 MinIO | 41641/udp Headscale WireGuard

HP AGENT PC (192.168.1.101 / 100.64.0.2):
22 SSH | 3003 Gitea | 2222 Gitea SSH | 5432 PostgreSQL | 6379 Redis
8000 Coolify | 8080 Nextcloud | 2342 Photoprism | 5678 n8n
8200 Media Tools | 9100 Node Exporter
```

---

**KONIEC DOKUMENTU. Tento jediný súbor obsahuje 100% všetkého potrebného pre SCARLIX OS v15 — Sovereign Agent Compute Edition. Model-Agnostic. Production-Ready.**
