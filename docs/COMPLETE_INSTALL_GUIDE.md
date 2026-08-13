# SCARLIX OS v12 — KOMPLETNÝ INŠTALAČNÝ NÁVOD
## Od BIOSu po Dashboard na TV — Krok za Krokom

**Verzia:** v12.0 | **Dátum:** August 2026 | **Autor:** MoZoHuJa
**Stack:** ScarliHQ · Hermes · OpenCode · Buzz · SGLang · LiteLLM · Docker
**Hardvér:** 2× PC (Main + HP Agent) · 2× NVIDIA GPU · Ubuntu 24.04 LTS

> ⚠️ **Dôležité:** Tento návod je finálny pred nasadením. Postupuj KROK ZA KROKOM,
> nepreskakuj žiadny krok. Každý krok má overenie — ak overenie zlyhá, NEPOKRAČUJ.

---

## 📋 OBSAH

1. [Pre-flight Checklist](#1--pre-flight-checklist)
2. [BIOS Nastavenie — Oba PC](#2--bios-nastavenie--oba-pc)
3. [Inštalácia Ubuntu 24.04 LTS — Main PC](#3--inštalácia-ubuntu-2404-lts--main-pc)
4. [Inštalácia Ubuntu 24.04 LTS — HP Agent PC](#4--inštalácia-ubuntu-2404-lts--hp-agent-pc)
5. [Post-install: Základné balíky — Oba PC](#5--post-install-základné-balíky--oba-pc)
6. [NVIDIA Driver + CUDA + Container Toolkit — Main PC](#6--nvidia-driver--cuda--container-toolkit--main-pc)
7. [Docker Inštalácia — Oba PC](#7--docker-inštalácia--oba-pc)
8. [Docker Konfigurácia — Main PC](#8--docker-konfigurácia--main-pc)
9. [Sieť a Firewall — Oba PC](#9--sieť-a-firewall--oba-pc)
10. [AI Stack: SGLang + Ollama + llama.cpp + LiteLLM](#10--ai-stack-sglang--ollama--llamacpp--litellm)
11. [Model Download](#11--model-download)
12. [Buzz Workspace (Nostr Relay)](#12--buzz-workspace-nostr-relay)
13. [Hermes Agent (CEO)](#13--hermes-agent-ceo)
14. [OpenCode (Coding Manager) — HP Agent](#14--opencode-coding-manager--hp-agent)
15. [Voice Pipeline](#15--voice-pipeline)
16. [Headscale VPN](#16--headscale-vpn)
17. [Caddy + Authelia (Proxy + SSO)](#17--caddy--authelia-proxy--sso)
18. [CrowdSec WAF](#18--crowdsec-waf)
19. [Monitoring (Grafana + VictoriaMetrics)](#19--monitoring-grafana--victoriametrics)
20. [HP Agent Stack (Gitea, Coolify, Nextcloud, Photoprism, n8n)](#20--hp-agent-stack)
21. [ScarliHQ Build + Deploy](#21--scarlihq-build--deploy)
22. [Profily (4 users)](#22--profily-4-users)
23. [scarlix-mode Script](#23--scarlix-mode-script)
24. [Backup Konfigurácia](#24--backup-konfigurácia)
25. [TV Dashboard Setup](#25--tv-dashboard-setup)
26. [Finálna Verifikácia](#26--finálna-verifikácia)
27. [Troubleshooting](#27--troubleshooting)
28. [Port Referencia](#28--port-referencia)
29. [Čo Robí Každý Program](#29--čo-robí-každý-program)

---

## 1. 📋 PRE-FLIGHT CHECKLIST

### Čo potrebuješ mať pripravené:

| Položka | Detail |
|---------|--------|
| **2× USB flash disk** | Min. 8GB, na Ubuntu ISO inštaláciu |
| **Ubuntu 24.04.2 LTS Server ISO** | Stiahni z: https://ubuntu.com/download/server |
| **Rufus / balenaEtcher** | Na napálenie ISO na USB |
| **Monitor + klávesnica** | Pripoj na každý PC počas inštalácie |
| **LAN káble** | 2× — pripoj oba PC do routra |
| **Router prístup** | Poznaj admin heslo (pre DHCP/ port forwarding) |
| **GitHub účet** | Pre Gitea + Coolify |
| **Telegram Bot Token** | Pre Hermes (voliteľné, ale odporúčané) |
| **Cloudflare účet** | Pre Caddy DNS (voliteľné, ak máš doménu) |
| **Tvoj SCARLIX repozitár** | `scarlix-os-v12.tar.gz` alebo `git clone` |

### Hardvér — Main PC (AI Brain):

| Komponent | Minimál | Odporúčané |
|-----------|---------|------------|
| CPU | 8 jadier | AMD Ryzen 7 7700X |
| RAM | 32 GB | 64 GB DDR5 |
| GPU 0 | 16GB VRAM | RTX 5060 Ti 16GB (Blackwell) |
| GPU 1 | 16GB VRAM | RTX 4060 Ti 16GB (Ada) |
| Storage | 500GB SSD | 2TB NVMe + 4TB NVMe + 8TB HDD |
| Sieť | 1 GbE | 2.5 GbE + WiFi 6E |
| Zdroj | 750W | 850W 80+ Gold (ATX 3.0) |

### Hardvér — HP Agent PC (Worker):

| Komponent | Minimál | Odporúčané |
|-----------|---------|------------|
| CPU | 4 jadier | Intel i5-12400 |
| RAM | 16 GB | 32 GB DDR4 |
| GPU | — | Voliteľné (T4) |
| Storage | 250GB SSD | 1TB NVMe + 2TB SSD |
| Sieť | 1 GbE | 2.5 GbE |

✅ **Overenie:** Skontroluj, že máš všetok hardvér a káble pripravené.

---

## 2. 🔧 BIOS NASTAVENIE — OBA PC

### Main PC (AMD Ryzen):

1. Zapni PC, stlač `DEL` alebo `F2` pre BIOS
2. Nastav nasledovné:

| Nastavenie | Hodnota | Kde (typicky) |
|------------|---------|---------------|
| **Boot Mode** | UEFI (nie Legacy) | Boot → Boot Mode |
| **Above 4G Decoding** | **Enabled** ⚠️ | Settings → PCI Subsystem |
| **Resizable BAR** | **Enabled** ⚠️ | Settings → PCI Subsystem |
| **Secure Boot** | **Disabled** ⚠️ | Boot → Secure Boot |
| **CSM** | Disabled | Boot → CSM |
| **SVM Mode** | Enabled | CPU Configuration |
| **IOMMU** | Enabled | CPU Configuration |
| **Boot Order** | USB prvý | Boot → Boot Priority |
| **XMP/EXPO** | Enabled (RAM profil) | Memory/Tweaker |
| **PCIe Link Speed** | Auto/Gen4 | PCI Subsystem |

3. Ulož a reštart: `F10` → Save & Exit

### HP Agent PC (Intel):

1. Zapni PC, stlač `F2` pre BIOS
2. Nastav nasledovné:

| Nastavenie | Hodnota | Kde (typicky) |
|------------|---------|---------------|
| **Boot Mode** | UEFI | Boot → Boot Configuration |
| **Secure Boot** | **Disabled** ⚠️ | Boot → Secure Boot |
| **CSM** | Disabled | Boot → CSM |
| **VT-d / Virtualization** | **Enabled** | CPU Configuration |
| **Boot Order** | USB prvý | Boot → Boot Priority |
| **XMP** | Enabled (ak podporované) | Memory |

3. Ulož a reštart: `F10` → Save & Exit

✅ **Overenie:** Oba PC nabootujú z USB pri ďalšom štarte.

> ⚠️ **Kritické:** Above 4G Decoding + Resizable BAR sú **povinné** pre NVIDIA GPU na Main PC.
> Bez nich GPU nebude fungovať správne v Docker kontajneroch.

---

## 3. 🐧 INŠTALÁCIA UBUNTU 24.04 LTS — MAIN PC

### Krok 3.1: Boot z USB

1. Pripoj USB s Ubuntu 24.04 LTS Server ISO do Main PC
2. Zapni PC → nabootuje z USB
3. Vyber jazyk: **English** (odporúčané pre server)
4. Vyber klávesnicu: **Slovak** (alebo English US)

### Krok 3.2: Inštalácia

5. Vyber typ inštalácie: **Ubuntu Server (minimized)**
6. Network: Nastav statickú IP (pozri Krok 3.3)
7. Proxy: nechaj prázdne
8. Mirror: `http://sk.archive.ubuntu.com/ubuntu` (alebo default)
9. Storage: **Custom storage layout** (pozri Krok 3.4)

### Krok 3.3: Statická IP

10. Vyber sieťovú kartu (napr. `enp3s0`)
11. Nastav:
    - Subnet: `192.168.1.0/24`
    - Address: `192.168.1.100`
    - Gateway: `192.168.1.1`
    - Name servers: `1.1.1.1,8.8.8.8`
    - Search domains: (prázdne)

### Krok 3.4: LVM Layout

12. Vyber disk a nastav **LVM**:

```
VG: vg0 (NVMe 2TB — systém)
  lv-root    40GB  ext4  /
  lv-home    50GB  ext4  /home
  lv-opt    200GB  ext4  /opt
  lv-var    100GB  ext4  /var
  lv-tmp     20GB  ext4  /tmp
  lv-swap    32GB  swap

VG: vg1 (NVMe 4TB — dáta, ak máš druhý disk)
  lv-models   500GB  ext4  /models
  lv-buzz     200GB  ext4  /var/lib/buzz
  lv-files      1TB  ext4  /mnt/files
  lv-backup   500GB  ext4  /mnt/backup
```

### Krok 3.5: User a SSH

13. Profile setup:
    - Your name: `SCARLIX Admin`
    - Server name: `scarlix-main`
    - Pick username: `scarlix`
    - Password: _(silné heslo)_
14. SSH Setup:
    - ✅ Install OpenSSH server
    - ✅ Import SSH key (ak máš) alebo preskoč
15. Skip Ubuntu Pro
16. Vyber **nie** žiadne snap balíky (zruč všetky)

### Krok 3.6: Dokončenie

17. **NEinštaluj** žiadne dodatočné balíky (nechaj prázdne)
18. Install → počkaj na dokončenie (~10-15 min)
19. Reboot → vyber USB
20. Prihlás sa: `scarlix` / _(heslo)_

✅ **Overenie:**
```bash
ip addr show | grep "192.168.1.100"
hostnamectl | grep "scarlix-main"
```
Oba príkazy musia vrátiť očakávané hodnoty.

---

## 4. 🐧 INŠTALÁCIA UBUNTU 24.04 LTS — HP AGENT PC

Opakuj Kroky 3.1-3.6 s týmito rozdielmi:

| Parameter | Main PC | HP Agent PC |
|-----------|---------|-------------|
| Server name | scarlix-main | **scarlix-hp** |
| IP address | 192.168.1.100 | **192.168.1.101** |
| LVM | 2 VG (systém + dáta) | 1 VG (systém + dáta) |
| GPU | 2× NVIDIA | Žiadne |

HP Agent LVM:
```
VG: vg0 (NVMe 1TB)
  lv-root    40GB  ext4  /
  lv-home    30GB  ext4  /home
  lv-opt    100GB  ext4  /opt
  lv-var    100GB  ext4  /var
  lv-swap    16GB  swap

VG: vg1 (SSD 2TB)
  lv-files    500GB  ext4  /mnt/files
  lv-photos   500GB  ext4  /mnt/photos
  lv-backup   500GB  ext4  /mnt/backup
```

✅ **Overenie:**
```bash
ip addr show | grep "192.168.1.101"
hostnamectl | grep "scarlix-hp"
```

---

## 5. 📦 POST-INSTALL: ZÁKLADNÉ BALÍKY — OBA PC

Spusti na **oboch PC** (Main + HP Agent):

```bash
# Update
sudo apt update && sudo apt upgrade -y

# Základné balíky
sudo apt install -y curl wget git jq bc net-tools htop tmux vim nano \
  build-essential dkms linux-headers-$(uname -r) \
  ufw fail2ban chrony unzip rsync btrfs-progs lvm2 \
  python3-pip htop iotop nethogs

# Timezone
sudo timedatectl set-timezone Europe/Bratislava

# Chrony (NTP)
sudo systemctl enable --now chrony
```

✅ **Overenie:**
```bash
timedatectl | grep "Time zone"
# Musí ukázať: Europe/Bratislava (CET, +0100)
chronyc tracking
# Musí ukázať: synchronized
```

---

## 6. 🎮 NVIDIA DRIVER + CUDA + CONTAINER TOOLKIT — MAIN PC

> ⚠️ **Len Main PC** (HP Agent nemá GPU)

### Krok 6.1: NVIDIA Driver 570-open

```bash
# Pridaj graphics drivers PPA
sudo add-apt-repository ppa:graphics-drivers/ppa -y
sudo apt update

# Inštaluj driver
sudo apt install -y nvidia-driver-570-open

# Reboot
sudo reboot
```

Po reboote:
```bash
nvidia-smi
```

✅ **Overenie:** Musí ukázať 2× GPU, Driver Version 570.xx, CUDA Version 12.8

```bash
# Over open kernel module
cat /proc/driver/nvidia/version | head -1
# Musí obsahovať: "NVIDIA UNIX Open Kernel Module"
```

### Krok 6.2: CUDA Toolkit 12.4

```bash
# Stiahni CUDA keyring
wget https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64/cuda-keyring_1.1-1_all.deb
sudo dpkg -i cuda-keyring_1.1-1_all.deb

# Inštaluj CUDA
sudo apt update
sudo apt install -y cuda-toolkit-12-4

# Pridaj do PATH
echo 'export PATH=/usr/local/cuda-12.4/bin:$PATH' | sudo tee /etc/profile.d/cuda.sh
echo 'export LD_LIBRARY_PATH=/usr/local/cuda-12.4/lib64:$LD_LIBRARY_PATH' | sudo tee -a /etc/profile.d/cuda.sh
source /etc/profile.d/cuda.sh

# Over
nvcc --version
```

✅ **Overenie:** `nvcc --version` ukáže CUDA 12.4

### Krok 6.3: cuDNN 9.x

```bash
sudo apt install -y libcudnn9-cuda-12 libcudnn9-dev-cuda-12
```

### Krok 6.4: NVIDIA Container Toolkit

```bash
# Pridaj repozitár
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
  | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg

curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
  | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
  | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list

sudo apt update
sudo apt install -y nvidia-container-toolkit

# Konfiguruj Docker runtime (PO inštalácii Docker — pozri Krok 7)
sudo nvidia-ctk runtime configure --runtime=docker
```

✅ **Overenie (PO Docker inštalácii v Kroku 7):**
```bash
docker run --rm --gpus all nvidia/cuda:12.4.0-base-ubuntu24.04 nvidia-smi
# Musí ukázať 2× GPU zvnútra kontajnera
```

### Krok 6.5: Stabilné GPU indexy (udev rules)

```bash
# Zisti PCI adresy oboch GPU
lspci | grep -i nvidia
# Príklad výstupu:
# 01:00.0 VGA compatible controller: NVIDIA ... [GPU 0]
# 02:00.0 VGA compatible controller: NVIDIA ... [GPU 1]

# Vytvor udev rules (nahraď PCI adresy podľa tvojich)
sudo tee /etc/udev/rules.d/99-nvidia-stable.rules << 'EOF'
KERNEL=="nvidia", KERNELS=="0000:01:00.0", SYMLINK+="nvidia-ai"
KERNEL=="nvidia_modeset", KERNELS=="0000:01:00.0", SYMLINK+="nvidia-ai-modeset"
KERNEL=="nvidia_uvm", KERNELS=="0000:01:00.0", SYMLINK+="nvidia-ai-uvm"
KERNEL=="nvidia", KERNELS=="0000:02:00.0", SYMLINK+="nvidia-game"
KERNEL=="nvidia_modeset", KERNELS=="0000:02:00.0", SYMLINK+="nvidia-game-modeset"
KERNEL=="nvidia_uvm", KERNELS=="0000:02:00.0", SYMLINK+="nvidia-game-uvm"
EOF

sudo udevadm control --reload-rules
sudo udevadm trigger
```

✅ **Overenie:**
```bash
ls -la /dev/nvidia-ai /dev/nvidia-game
# Musia existovať symbolické linky
```

---

## 7. 🐳 DOCKER INŠTALÁCIA — OBA PC

Spusti na **oboch PC**:

```bash
# Pridaj Docker oficiálny GPG key
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
  | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg

# Pridaj repozitár
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
  https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

# Inštaluj Docker
sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io \
  docker-buildx-plugin docker-compose-plugin

# Pridaj usera do docker skupiny
sudo usermod -aG docker $USER
newgrp docker

# Over
docker --version
docker compose version
```

✅ **Overenie:**
```bash
docker run hello-world
# Musí vypísať "Hello from Docker!"
```

---

## 8. 🐳 DOCKER KONFIGURÁCIA — MAIN PC

### Krok 8.1: daemon.json (KRITICKÉ)

```bash
sudo tee /etc/docker/daemon.json << 'EOF'
{
  "iptables": false,
  "bridge": "none",
  "data-root": "/var/lib/docker",
  "storage-driver": "overlay2",
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" },
  "live-restore": true,
  "userland-proxy": false,
  "no-new-privileges": true,
  "default-runtime": "runc",
  "runtimes": {
    "nvidia": {
      "path": "nvidia-container-runtime",
      "runtimeArgs": []
    }
  }
}
EOF

sudo systemctl restart docker
```

> ⚠️ **`iptables: false` je Kritické:** Bez toho Docker obíde UFW firewall a otvorí porty na všetky interfaces.

✅ **Overenie:**
```bash
cat /etc/docker/daemon.json | jq .
docker info | grep -i runtime
# Musí ukázať: nvidia v zozname runtimes
```

### Krok 8.2: Docker siete

```bash
# Vytvor siete
docker network create scarlix_net --driver bridge --subnet 172.20.0.0/16
docker network create scarlix_ai --driver bridge --subnet 172.21.0.0/16
```

✅ **Overenie:**
```bash
docker network ls | grep scarlix
# Musí ukázať scarlix_net a scarlix_ai
```

### Krok 8.3: ufw-docker (Firewall routing fix)

```bash
# Inštaluj ufw-docker
sudo curl -fsSL https://github.com/chaifeng/ufw-docker/raw/master/ufw-docker \
  -o /usr/local/bin/ufw-docker
sudo chmod +x /usr/local/bin/ufw-docker

# Nainštaluj do UFW
sudo ufw-docker install
sudo ufw reload
```

✅ **Overenie:**
```bash
sudo ufw status | grep DOCKER
# Musí ukázať Docker pravidlá
```

### Krok 8.4: Docker Socket Proxy (Bezpečnosť)

```bash
mkdir -p /opt/scarlix/docker
cat > /opt/scarlix/docker/socket-proxy.yml << 'YAML'
services:
  docker-socket-proxy:
    image: tecnativa/docker-socket-proxy:0.3.0
    container_name: docker-socket-proxy
    restart: unless-stopped
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    environment:
      - CONTAINERS=1
      - INFO=1
      - VERSION=1
      - EVENTS=1
      - EXEC=0
      - POST=0
      - AUTH=0
      - BUILD=0
    networks: [scarlix_net]
networks:
  scarlix_net: { external: true }
YAML

docker compose -f /opt/scarlix/docker/socket-proxy.yml up -d
```

✅ **Overenie:**
```bash
docker ps | grep docker-socket-proxy
# Musí bežať
```

---

## 9. 🔥 SIEŤ A FIREWALL — OBA PC

### Main PC:

```bash
# UFW pravidlá
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow 22/tcp comment 'SSH'
sudo ufw allow 80/tcp comment 'HTTP'
sudo ufw allow 443/tcp comment 'HTTPS'
sudo ufw allow 41641/udp comment 'Headscale WireGuard'
sudo ufw --force enable

# Over
sudo ufw status verbose
```

### HP Agent PC:

```bash
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow 22/tcp comment 'SSH'
sudo ufw allow 41641/udp comment 'Headscale WireGuard'
sudo ufw --force enable
```

### Fail2ban (oba PC):

```bash
sudo tee /etc/fail2ban/jail.local << 'EOF'
[DEFAULT]
bantime = 3600
findtime = 600
maxretry = 3
[sshd]
enabled = true
port = 22
maxretry = 3
bantime = 3600
EOF

sudo systemctl enable --now fail2ban
```

✅ **Overenie:**
```bash
sudo ufw status
sudo fail2ban-client status
```

---

## 10. 🧠 AI STACK: SGLang + OLLAMA + LLAMA.CPP + LITELLM

> Len **Main PC** (HP Agent nemá GPU)

### Krok 10.1: Priprav adresáre

```bash
sudo mkdir -p /opt/scarlix/ai/{sglang,ollama,llamacpp,litellm}
sudo mkdir -p /models /var/lib/sglang/{cache,scratch}
sudo chown -R $USER:$USER /opt/scarlix /models /var/lib/sglang
```

### Krok 10.2: SGLang (GPU 0 — primárny inference)

```bash
cat > /opt/scarlix/ai/sglang/docker-compose.yml << 'YAML'
services:
  sglang:
    image: ghcr.io/sgl-project/sglang:v0.5.5-cu124
    container_name: sglang
    restart: unless-stopped
    ports:
      - "100.64.0.1:30000:30000"
      - "127.0.0.1:30000:30000"
    volumes:
      - /var/lib/sglang/cache:/root/.cache/huggingface
      - /models:/models:ro
    environment:
      - NVIDIA_VISIBLE_DEVICES=0
      - NVIDIA_DRIVER_CAPABILITIES=compute,utility
    command: >
      python -m sglang.launch_server
      --model-path /models/Qwen3-14B-Instruct
      --port 30000
      --host 0.0.0.0
      --mem-fraction-static 0.90
      --context-length 32768
      --enable-flashinfer
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              device_ids: ['0']
              capabilities: [gpu]
    networks: [scarlix_ai]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:30000/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 180s
networks:
  scarlix_ai: { external: true }
YAML
```

> ⚠️ **NEspúšťaj ešte** — najprv treba stiahnuť modely (Krok 11).

### Krok 10.3: Ollama (GPU 1 — concurrent/fallback)

```bash
cat > /opt/scarlix/ai/ollama/docker-compose.yml << 'YAML'
services:
  ollama-agent:
    image: ollama/ollama:0.5.4
    container_name: ollama-agent
    restart: unless-stopped
    ports:
      - "11435:11434"
    volumes:
      - ./data:/root/.ollama
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
          devices:
            - driver: nvidia
              device_ids: ['1']
              capabilities: [gpu]
    networks: [scarlix_ai]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:11434/api/tags"]
      interval: 30s
      timeout: 10s
      retries: 3
networks:
  scarlix_ai: { external: true }
YAML
```

### Krok 10.4: llama.cpp (CPU — offline fallback)

```bash
cat > /opt/scarlix/ai/llamacpp/docker-compose.yml << 'YAML'
services:
  llamacpp:
    image: ghcr.io/ggml-org/llama.cpp:server-cuda
    container_name: llamacpp
    restart: unless-stopped
    ports:
      - "127.0.0.1:11438:8080"
    volumes:
      - /models:/models:ro
    command: >
      --model /models/qwen3.6-14b-instruct-q4_k_m.gguf
      --ctx-size 8192
      --threads 16
      --host 0.0.0.0
      --port 8080
    networks: [scarlix_ai]
networks:
  scarlix_ai: { external: true }
YAML
```

### Krok 10.5: LiteLLM (Gateway — 3-tier failover)

```bash
cat > /opt/scarlix/ai/litellm/config.yaml << 'EOF'
model_list:
  - model_name: qwen3.6:14b
    litellm_params:
      model: openai/qwen3.6:14b
      api_base: http://sglang:30000/v1
      api_key: not-needed
  - model_name: qwen3.6:14b-ollama
    litellm_params:
      model: ollama/qwen3.6:14b
      api_base: http://ollama-agent:11434
  - model_name: qwen3.6:14b-cpu
    litellm_params:
      model: openai/qwen3.6:14b
      api_base: http://llamacpp:8080/v1
      api_key: not-needed

router_settings:
  routing_strategy: simple-shuffle
  num_retries: 2
  fallbacks:
    - qwen3.6:14b: ["qwen3.6:14b-ollama"]
    - qwen3.6:14b-ollama: ["qwen3.6:14b-cpu"]

general_settings:
  master_key: os.environ/LITELLM_MASTER_KEY
  database_url: sqlite:///litellm.db
EOF

cat > /opt/scarlix/ai/litellm/docker-compose.yml << 'YAML'
services:
  litellm:
    image: ghcr.io/berriai/litellm:main-latest
    container_name: litellm
    restart: unless-stopped
    ports:
      - "100.64.0.1:4000:4000"
      - "127.0.0.1:4000:4000"
    volumes:
      - ./config.yaml:/app/config.yaml
      - ./data:/app/data
    command: --config /app/config.yaml --port 4000
    environment:
      - LITELLM_MASTER_KEY=${LITELLM_MASTER_KEY}
    networks: [scarlix_ai, scarlix_net]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:4000/health"]
      interval: 30s
      timeout: 5s
      retries: 3
networks:
  scarlix_ai: { external: true }
  scarlix_net: { external: true }
YAML
```

### Krok 10.6: .env súbor

```bash
sudo mkdir -p /etc/scarlix
sudo tee /etc/scarlix/.env << 'EOF'
# === LiteLLM ===
LITELLM_MASTER_KEY=sk-scarlix-CHANGE-THIS-KEY

# === Telegram ===
TELEGRAM_BOT_TOKEN=123456:ABC-DEF...
TELEGRAM_ZMOR_CHAT_ID=123456789

# === Buzz ===
BUZZ_POSTGRES_PASSWORD=ChangeMeBuzzPG!
BUZZ_MINIO_PASSWORD=ChangeMeBuzzMinio!

# === Databases ===
POSTGRES_PASSWORD=ChangeMeMainPG!
REDIS_PASSWORD=ChangeMeRedis!
GRAFANA_PASSWORD=ChangeMeGrafana!
GITEA_DB_PASSWORD=ChangeMeGitea!
COOLIFY_APP_KEY=change-me-32-chars-hex
COOLIFY_DB_PASSWORD=ChangeMeCoolify!
NEXTCLOUD_DB_PASSWORD=ChangeMeNextcloud!
PHOTOPRISM_ADMIN_PASSWORD=ChangeMePhoto!
N8N_PASSWORD=ChangeMeN8n!
N8N_DB_PASSWORD=ChangeMeN8nDB!
RESTIC_PASSWORD=ChangeMeRestic!

# === Profiles ===
SCARLIX_DEFAULT_PROFILE=zmor
SCARLIX_DEFAULT_MODE=ai
EOF

sudo chmod 600 /etc/scarlix/.env
```

> ⚠️ **Zmeň všetky heslá!** Vygeneruj silné heslá: `openssl rand -base64 24`

---

## 11. 📥 MODEL DOWNLOAD

```bash
# Inštaluj huggingface-cli
pip3 install huggingface_hub

# 1. SGLang model (safetensors, ~28GB)
huggingface-cli download Qwen/Qwen3-14B-Instruct \
  --local-dir /models/Qwen3-14B-Instruct

# 2. Ollama models (GGUF)
cd /opt/scarlix/ai/ollama
docker compose up -d
# Počkaj kým Olloma nabootuje (~10s)
docker exec ollama-agent ollama pull qwen3.6:14b
docker exec ollama-agent ollama pull nomic-embed-text

# 3. llama.cpp model (GGUF Q4_K_M, ~8GB)
wget -O /models/qwen3.6-14b-instruct-q4_k_m.gguf \
  "https://huggingface.co/Qwen/Qwen3-14B-Instruct-GGUF/resolve/main/qwen3.6-14b-instruct-q4_k_m.gguf"
```

✅ **Overenie:**
```bash
ls -la /models/
# Musí ukázať: Qwen3-14B-Instruct/ a qwen3.6-14b-instruct-q4_k_m.gguf
docker exec ollama-agent ollama list
# Musí ukázať: qwen3.6:14b a nomic-embed-text
```

### Krok 11.1: Štart AI stack

```bash
# Načítaj environment
set -a; source /etc/scarlix/.env; set +a

# Štart SGLang
cd /opt/scarlix/ai/sglang
docker compose up -d
echo "SGLang štartuje (180s startup)... počkaj"

# Štart Ollama
cd /opt/scarlix/ai/ollama
docker compose up -d

# Štart LiteLLM
cd /opt/scarlix/ai/litellm
docker compose up -d

# Počkaj 3 minúty na SGLang startup
sleep 180
```

✅ **Overenie:**
```bash
# SGLang
curl http://localhost:30000/v1/models
# Musí vrátiť JSON s Qwen3-14B-Instruct

# Ollama
curl http://localhost:11435/api/tags
# Musí vrátiť JSON s qwen3.6:14b

# LiteLLM
curl http://localhost:4000/health
# Musí vrátiť 200 OK

# Test inferencie
curl -X POST http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-scarlix-CHANGE-THIS-KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen3.6:14b","messages":[{"role":"user","content":"Ahoj, odpovedz po slovensky."}],"max_tokens":100}'
# Musí vrátiť odpoveď v slovenčine
```

---

## 12. 🐝 BUZZ WORKSPACE (NOSTR RELAY)

```bash
mkdir -p /opt/scarlix/workspace/buzz/{data,postgres,redis,minio}

cat > /opt/scarlix/workspace/buzz/docker-compose.yml << 'YAML'
services:
  buzz-relay:
    image: ghcr.io/block/buzz-relay:latest
    container_name: buzz-relay
    restart: unless-stopped
    ports:
      - "100.64.0.1:3000:3000"
    volumes:
      - ./data:/data
    environment:
      - BUZZ_RELAY_URL=ws://100.64.0.1:3000
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
    volumes:
      - ./postgres/data:/var/lib/postgresql/data
    environment:
      - POSTGRES_PASSWORD=${BUZZ_POSTGRES_PASSWORD}
      - POSTGRES_DB=buzz
    networks: [scarlix_net]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 10s
      timeout: 5s
      retries: 5

  buzz-redis:
    image: redis:7-alpine
    container_name: buzz-redis
    restart: unless-stopped
    networks: [scarlix_net]

  buzz-minio:
    image: minio/minio:latest
    container_name: buzz-minio
    restart: unless-stopped
    ports:
      - "127.0.0.1:9000:9000"
    volumes:
      - ./minio/data:/data
    environment:
      - MINIO_ROOT_USER=scarlix
      - MINIO_ROOT_PASSWORD=${BUZZ_MINIO_PASSWORD}
    command: server /data
    networks: [scarlix_net]
networks:
  scarlix_net: { external: true }
YAML

set -a; source /etc/scarlix/.env; set +a
cd /opt/scarlix/workspace/buzz
docker compose up -d
```

✅ **Overenie:**
```bash
docker ps | grep buzz
# Musia bežať 4 kontajnery: buzz-relay, buzz-postgres, buzz-redis, buzz-minio
curl http://localhost:3000
# Musí vrátiť Buzz relay info
```

---

## 13. 🤖 HERMES AGENT (CEO)

```bash
mkdir -p /opt/scarlix/agents/hermes/{config,workspace}

cat > /opt/scarlix/agents/hermes/config.yaml << 'EOF'
provider: custom
model: qwen3.6:14b
custom_provider:
  base_url: http://litellm:4000/v1
  api_key: ${LITELLM_MASTER_KEY}

memory:
  enabled: true
  nudge_interval: 300

skills:
  enabled: true
  auto_create: true

cron:
  enabled: true

gateway:
  telegram:
    enabled: true
    bot_token: ${TELEGRAM_BOT_TOKEN}
    allowed_users:
      - ${TELEGRAM_ZMOR_CHAT_ID}
  discord:
    enabled: false

mcp:
  servers:
    scarlihq:
      url: http://scarlihq:8091/mcp
      transport: http
EOF

cat > /opt/scarlix/agents/hermes/docker-compose.yml << 'YAML'
services:
  hermes:
    image: nousresearch/hermes-agent:latest
    container_name: hermes
    restart: unless-stopped
    ports:
      - "127.0.0.1:7999:7999"
    volumes:
      - ./config:/root/.hermes
      - ./workspace:/workspace
      - /opt/scarlix:/opt/scarlix:ro
    environment:
      - HERMES_HOME=/root/.hermes
      - OPENAI_API_BASE=http://litellm:4000/v1
      - OPENAI_API_KEY=${LITELLM_MASTER_KEY}
      - TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
      - TELEGRAM_CHAT_ID=${TELEGRAM_ZMOR_CHAT_ID}
      - TZ=Europe/Bratislava
    networks: [scarlix_ai, scarlix_net]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:7999/health"]
      interval: 30s
      timeout: 5s
      retries: 3
networks:
  scarlix_ai: { external: true }
  scarlix_net: { external: true }
YAML

set -a; source /etc/scarlix/.env; set +a
cd /opt/scarlix/agents/hermes
docker compose up -d
```

✅ **Overenie:**
```bash
docker ps | grep hermes
curl http://localhost:7999/health
# Musí vrátiť 200 OK
```

> 💡 **Telegram test:** Pošli správu svojmu Telegram botovi — Hermes by mal odpovedať.

---

## 14. 💻 OPENCODE (CODING MANAGER) — HP AGENT

> Na **HP Agent PC** (192.168.1.101)

```bash
# Inštaluj OpenCode
curl -fsSL https://opencode.ai/install | bash

# Konfigurácia
mkdir -p ~/.opencode
cat > ~/.opencode/config.json << 'EOF'
{
  "provider": "openai",
  "model": "qwen3.6:14b",
  "openai": {
    "baseURL": "http://100.64.0.1:4000/v1",
    "apiKey": "sk-scarlix-CHANGE-THIS-KEY"
  },
  "mcp": {
    "scarlihq": {
      "url": "http://100.64.0.1:8091/mcp",
      "transport": "http"
    }
  },
  "agents": {
    "build": { "model": "qwen3.6:14b" },
    "plan": { "model": "qwen3.6:14b" }
  }
}
EOF

# Over
opencode --version
```

✅ **Overenie:**
```bash
opencode "Hello, are you working?"
# Musí odpovedať
```

---

## 15. 🎙️ VOICE PIPELINE

> Na **Main PC** (GPU 1 pre Whisper)

### Krok 15.1: Whisper SK (STT)

```bash
mkdir -p /opt/scarlix/voice/whisper

cat > /opt/scarlix/voice/whisper/docker-compose.yml << 'YAML'
services:
  whisper-sk:
    image: fedirz/faster-whisper-server:0.10.0
    container_name: whisper-sk
    restart: unless-stopped
    ports:
      - "8002:8000"
    volumes:
      - ./data:/root/.cache
    environment:
      - WHISPER__MODEL=Systran/faster-whisper-large-v3
      - WHISPER__INFERENCE_DEVICE=cuda
      - WHISPER__COMPUTE_TYPE=int8_float16
      - WHISPER__LANGUAGE=sk
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              device_ids: ['1']
              capabilities: [gpu]
    networks: [scarlix_ai, scarlix_net]
networks:
  scarlix_ai: { external: true }
  scarlix_net: { external: true }
YAML

cd /opt/scarlix/voice/whisper
docker compose up -d
```

### Krok 15.2: Piper SK (TTS)

```bash
mkdir -p /opt/scarlix/voice/piper/models
cd /opt/scarlix/voice/piper

# Stiahni SK hlas
wget -O models/sk_SK-lili-medium.onnx \
  https://huggingface.co/rhasspy/piper-voices/resolve/main/sk/sk_SK/lili/medium/sk_SK-lili-medium.onnx
wget -O models/sk_SK-lili-medium.onnx.json \
  https://huggingface.co/rhasspy/piper-voices/resolve/main/sk/sk_SK/lili/medium/sk_SK-lili-medium.onnx.json

cat > docker-compose.yml << 'YAML'
services:
  piper-sk:
    image: rhasspy/wyoming-piper:1.5.0
    container_name: piper-sk
    restart: unless-stopped
    ports:
      - "8003:10200"
    volumes:
      - ./models:/voices:ro
    command: --voice sk_SK-lili-medium
    networks: [scarlix_net]
networks:
  scarlix_net: { external: true }
YAML

docker compose up -d
```

### Krok 15.3: WakeWord

```bash
mkdir -p /opt/scarlix/voice/wakeword
cat > /opt/scarlix/voice/wakeword/docker-compose.yml << 'YAML'
services:
  openwakeword:
    image: rhasspy/wyoming-openwakeword:1.10.0
    container_name: openwakeword
    restart: unless-stopped
    ports:
      - "10400:10400"
    command: |
      --uri 'tcp://0.0.0.0:10400'
      --wake-word hey_jarvis
      --threshold 0.5
    networks: [scarlix_net]
networks:
  scarlix_net: { external: true }
YAML

cd /opt/scarlix/voice/wakeword
docker compose up -d
```

✅ **Overenie:**
```bash
docker ps | grep -E "whisper|piper|wakeword"
# Všetky 3 musia bežať
curl http://localhost:8002/health
# Whisper health check
```

---

## 16. 🔒 HEADSCALE VPN

```bash
mkdir -p /opt/scarlix/network/headscale/{config,data}
cd /opt/scarlix/network/headscale

cat > docker-compose.yml << 'YAML'
services:
  headscale:
    image: headscale/headscale:0.25.0
    container_name: headscale
    restart: unless-stopped
    ports:
      - "100.64.0.1:8080:8080"
      - "100.64.0.1:50443:50443/udp"
    volumes:
      - ./config:/etc/headscale
      - ./data:/var/lib/headscale
    environment:
      - HEADSCALE_LISTEN_ADDR=0.0.0.0:8080
    networks: [scarlix_net]
networks:
  scarlix_net: { external: true }
YAML

docker compose up -d

# Vytvor usera
docker exec headscale headscale users create scarlix

# Vygeneruj preauth key
docker exec headscale headscale preauthkeys create --user scarlix --reusable --expiration 24h
# Ulož si tento key!
```

### Registrácia klientov:

Na každom zariadení (HP Agent, Admin PC, mobil, atď.):
```bash
# Nainštaluj Tailscale klienta
curl -fsSL https://tailscale.com/install.sh | sh

# Pripoj na Headscale
sudo tailscale up --login-server http://192.168.1.100:8080 \
  --authkey <PREAUTH_KEY> --hostname <DEVICE_NAME>
```

✅ **Overenie:**
```bash
# Na Main PC:
docker exec headscale headscale nodes list
# Musí ukázať všetky pripojené zariadenia
```

---

## 17. 🌐 CADDY + AUTHELIA (PROXY + SSO)

### Krok 17.1: Authelia

```bash
mkdir -p /opt/scarlix/network/authelia/{config,data}

# Vygeneruj secrets
JWT_SECRET=$(openssl rand -base64 64)
SESSION_SECRET=$(openssl rand -base64 64)

cat > /opt/scarlix/network/authelia/config/configuration.yml << EOF
server:
  address: 'tcp://0.0.0.0:9091'

log:
  level: info

authentication_backend:
  file:
    path: /config/users_database.yml

access_control:
  default_policy: deny
  rules:
    - domain: 'scarlix.example.com'
      policy: one_factor
    - domain: '*.scarlix.example.com'
      policy: one_factor

session:
  name: scarlix_session
  secret: '${SESSION_SECRET}'
  expiration: '360h'
  inactivity: '30m'

storage:
  local:
    path: /data/db.sqlite3

totp:
  issuer: SCARLIX OS
EOF

cat > /opt/scarlix/network/authelia/config/users_database.yml << 'EOF'
users:
  zmor:
    displayname: "Zmor (Admin)"
    password: "$argon2id$v=19$m=65536,t=3,p=4$REPLACE_WITH_HASH"
    email: zmor@scarlix.local
    groups:
      - admins
      - users
EOF

# Vygeneruj heslo pre zmor usera
docker run --rm authelia/authelia:4.38 authelia hash-password 'TvojeHeslo123'
# Skopíruj hash a nahraď REPLACE_WITH_HASH vyššie

cat > /opt/scarlix/network/authelia/docker-compose.yml << 'YAML'
services:
  authelia:
    image: authelia/authelia:4.38
    container_name: authelia
    restart: unless-stopped
    ports:
      - "9091:9091"
    volumes:
      - ./config:/config
      - ./data:/data
    environment:
      - AUTHELIA_JWT_SECRET=${JWT_SECRET}
    networks: [scarlix_net]
networks:
  scarlix_net: { external: true }
YAML

cd /opt/scarlix/network/authelia
docker compose up -d
```

### Krok 17.2: Caddy

```bash
mkdir -p /opt/scarlix/network/caddy
cat > /opt/scarlix/network/caddy/Caddyfile << 'EOF'
{
  email admin@scarlix.example.com
}

(authelia) {
  forward_auth authelia:9091 {
    uri /api/authz/forward-auth
    copy_headers Remote-User Remote-Groups Remote-Name Remote-Email
  }
}

scarlix.example.com {
  import authelia
  reverse_proxy scarlihq:8090
}

buzz.scarlix.example.com {
  import authelia
  reverse_proxy buzz-relay:3000
}

hermes.scarlix.example.com {
  import authelia
  reverse_proxy hermes:7999
}

git.scarlix.example.com {
  import authelia
  reverse_proxy 100.64.0.2:3000
}

coolify.scarlix.example.com {
  import authelia
  reverse_proxy 100.64.0.2:8000
}

files.scarlix.example.com {
  import authelia
  reverse_proxy 100.64.0.2:8080
}

photos.scarlix.example.com {
  import authelia
  reverse_proxy 100.64.0.2:2342
}

auth.scarlix.example.com {
  reverse_proxy authelia:9091
}

monitoring.scarlix.example.com {
  import authelia
  reverse_proxy grafana:3000
}
EOF

cat > /opt/scarlix/network/caddy/docker-compose.yml << 'YAML'
services:
  caddy:
    image: caddy:2.8.4-alpine
    container_name: caddy
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
      - ./data:/data
      - ./config:/config
    networks: [scarlix_net]
networks:
  scarlix_net: { external: true }
YAML

cd /opt/scarlix/network/caddy
docker compose up -d
```

> 💡 **DNS:** Nastav `*.scarlix.example.com` A record na `192.168.1.100` (alebo použi Headscale MagicDNS + split-horizon DNS).

✅ **Overenie:**
```bash
curl -k https://scarlix.example.com
# Musí presmerovať na Authelia login
```

---

## 18. 🛡️ CROWDSEC WAF

```bash
mkdir -p /opt/scarlix/security/crowdsec/{config,data}

cat > /opt/scarlix/security/crowdsec/docker-compose.yml << 'YAML'
services:
  crowdsec:
    image: crowdsecurity/crowdsec:v1.4.0
    container_name: crowdsec
    restart: unless-stopped
    volumes:
      - ./config:/etc/crowdsec
      - ./data:/var/lib/crowdsec/data
      - /var/log/caddy:/var/log/caddy:ro
      - /var/log/authelia:/var/log/authelia:ro
    environment:
      - COLLECTIONS=crowdsecurity/caddy crowdsecurity/http-cve crowdsecurity/sshd
    networks: [scarlix_net]
networks:
  scarlix_net: { external: true }
YAML

cd /opt/scarlix/security/crowdsec
docker compose up -d
```

✅ **Overenie:**
```bash
docker exec crowdsec cscli metrics
# Musí ukázať metriky
```

---

## 19. 📊 MONITORING (GRAFANA + VICTORIAMETRICS)

```bash
mkdir -p /opt/scarlix/monitoring/{victoria,grafana,otel}

cat > /opt/scarlix/monitoring/docker-compose.yml << 'YAML'
services:
  victoria-metrics:
    image: victoriametrics/victoria-metrics:v1.108.0
    container_name: victoria-metrics
    restart: unless-stopped
    ports:
      - "100.64.0.1:8428:8428"
    volumes:
      - ./victoria:/storage
    command: --storageDataPath=/storage --retentionPeriod=90d
    networks: [scarlix_net]

  grafana:
    image: grafana/grafana:11.3.0
    container_name: grafana
    restart: unless-stopped
    ports:
      - "100.64.0.1:3000:3000"
    volumes:
      - ./grafana:/var/lib/grafana
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=${GRAFANA_PASSWORD}
      - GF_SERVER_ROOT_URL=https://monitoring.scarlix.example.com
    networks: [scarlix_net]

  otel-collector:
    image: otel/opentelemetry-collector-contrib:latest
    container_name: otel-collector
    restart: unless-stopped
    ports:
      - "100.64.0.1:4317:4317"
      - "100.64.0.1:4318:4318"
    volumes:
      - ./otel/config.yaml:/etc/otelcol/config.yaml
    networks: [scarlix_net]
networks:
  scarlix_net: { external: true }
YAML

# OTel config
cat > /opt/scarlix/monitoring/otel/config.yaml << 'EOF'
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

exporters:
  prometheusremotewrite:
    endpoint: http://victoria-metrics:8428/api/v1/write

service:
  pipelines:
    metrics:
      receivers: [otlp]
      exporters: [prometheusremotewrite]
    traces:
      receivers: [otlp]
      exporters: [logging]
    logs:
      receivers: [otlp]
      exporters: [logging]
EOF

set -a; source /etc/scarlix/.env; set +a
cd /opt/scarlix/monitoring
docker compose up -d
```

✅ **Overenie:**
```bash
curl http://localhost:8428/health
# VictoriaMetrics health
curl http://localhost:3000/api/health
# Grafana health (admin / GRAFANA_PASSWORD)
```

---

## 20. 🖥️ HP AGENT STACK

> Na **HP Agent PC** (192.168.1.101)

```bash
# Docker siete
docker network create scarlix_net --driver bridge --subnet 172.20.0.0/16

mkdir -p /opt/scarlix/hp-agent
cd /opt/scarlix/hp-agent

cat > docker-compose.yml << 'YAML'
services:
  postgres:
    image: postgres:16-alpine
    container_name: postgres
    restart: unless-stopped
    volumes:
      - ./postgres/data:/var/lib/postgresql/data
    environment:
      - POSTGRES_PASSWORD=${POSTGRES_PASSWORD}
    networks: [scarlix_net]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 10s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    container_name: redis
    restart: unless-stopped
    command: redis-server --requirepass ${REDIS_PASSWORD}
    networks: [scarlix_net]

  gitea:
    image: gitea/gitea:1.23
    container_name: gitea
    restart: unless-stopped
    ports:
      - "3000:3000"
    volumes:
      - ./gitea/data:/data
    environment:
      - USER_UID=1000
      - USER_GID=1000
      - GITEA__database__DB_TYPE=postgres
      - GITEA__database__HOST=postgres:5432
      - GITEA__database__NAME=gitea
      - GITEA__database__USER=gitea
      - GITEA__database__PASSWD=${GITEA_DB_PASSWORD}
    depends_on:
      postgres: { condition: service_healthy }
    networks: [scarlix_net]

  coolify:
    image: coollabsio/coolify:4.0.30
    container_name: coolify
    restart: unless-stopped
    ports:
      - "8000:8000"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - ./coolify/data:/data
    environment:
      - APP_ID=scarlix-coolify
      - APP_KEY=${COOLIFY_APP_KEY}
      - DB_CONNECTION=pgsql
      - DB_HOST=postgres
      - DB_PORT=5432
      - DB_DATABASE=coolify
      - DB_USERNAME=coolify
      - DB_PASSWORD=${COOLIFY_DB_PASSWORD}
      - REDIS_HOST=redis
      - REDIS_PASSWORD=${REDIS_PASSWORD}
    depends_on:
      postgres: { condition: service_healthy }
    networks: [scarlix_net]

  nextcloud:
    image: nextcloud:29
    container_name: nextcloud
    restart: unless-stopped
    ports:
      - "8080:80"
    volumes:
      - ./nextcloud/data:/var/www/html/data
      - ./nextcloud/config:/var/www/html/config
    environment:
      - POSTGRES_HOST=postgres
      - POSTGRES_DB=nextcloud
      - POSTGRES_USER=nextcloud
      - POSTGRES_PASSWORD=${NEXTCLOUD_DB_PASSWORD}
      - REDIS_HOST=redis
      - REDIS_HOST_PASSWORD=${REDIS_PASSWORD}
    depends_on:
      postgres: { condition: service_healthy }
    networks: [scarlix_net]

  photoprism:
    image: photoprism/photoprism:latest
    container_name: photoprism
    restart: unless-stopped
    ports:
      - "2342:2342"
    volumes:
      - ./photoprism/storage:/photoprism/storage
      - /mnt/photos:/photoprism/originals:ro
    environment:
      - PHOTOPRISM_ADMIN_PASSWORD=${PHOTOPRISM_ADMIN_PASSWORD}
      - PHOTOPRISM_DATABASE_DRIVER=sqlite
      - PHOTOPRISM_HTTP_HOST=0.0.0.0
      - PHOTOPRISM_HTTP_PORT=2342
    networks: [scarlix_net]

  n8n:
    image: n8nio/n8n:latest
    container_name: n8n
    restart: unless-stopped
    ports:
      - "5678:5678"
    volumes:
      - ./n8n/data:/home/node/.n8n
    environment:
      - N8N_BASIC_AUTH_ACTIVE=true
      - N8N_BASIC_AUTH_USER=admin
      - N8N_BASIC_AUTH_PASSWORD=${N8N_PASSWORD}
      - DB_TYPE=postgresdb
      - DB_POSTGRESDB_HOST=postgres
      - DB_POSTGRESDB_PORT=5432
      - DB_POSTGRESDB_DATABASE=n8n
      - DB_POSTGRESDB_USER=n8n
      - DB_POSTGRESDB_PASSWORD=${N8N_DB_PASSWORD}
    depends_on:
      postgres: { condition: service_healthy }
    networks: [scarlix_net]
networks:
  scarlix_net: { external: true }
YAML

# Vytvor .env
cat > /opt/scarlix/hp-agent/.env << 'EOF'
POSTGRES_PASSWORD=ChangeMeHP_PG!
REDIS_PASSWORD=ChangeMeHP_Redis!
GITEA_DB_PASSWORD=ChangeMeHP_Gitea!
COOLIFY_APP_KEY=change-me-32-chars-hex-here
COOLIFY_DB_PASSWORD=ChangeMeHP_Coolify!
NEXTCLOUD_DB_PASSWORD=ChangeMeHP_Nextcloud!
PHOTOPRISM_ADMIN_PASSWORD=ChangeMeHP_Photo!
N8N_PASSWORD=ChangeMeHP_N8n!
N8N_DB_PASSWORD=ChangeMeHP_N8nDB!
EOF

set -a; source .env; set +a
docker compose up -d
```

✅ **Overenie:**
```bash
docker ps
# Musí bežať 7 kontajnerov
curl http://localhost:3000  # Gitea
curl http://localhost:8000  # Coolify
curl http://localhost:8080  # Nextcloud
curl http://localhost:2342  # Photoprism
curl http://localhost:5678  # n8n
```

---

## 21. 🧠 SCARLIHQ BUILD + DEPLOY

> Na **Main PC**

### Krok 21.1: Build z repozitára

```bash
# Ak máš repozitár naklonovaný
cd /opt/scarlix
git clone https://github.com/MoZoHuJa/scarlix-os-v12.git scarlihq-src
cd scarlihq-src/scarlihq

# Build Go binary
CGO_ENABLED=0 go build -ldflags="-s -w" -o scarlihq ./cmd/scarlihq

# Build Docker image
docker build -f Dockerfile -t localhost/scarlihq:v12 .
```

### Krok 21.2: Deploy

```bash
cat > /opt/scarlix/scarlihq/docker-compose.yml << 'YAML'
services:
  scarlihq:
    image: localhost/scarlihq:v12
    container_name: scarlihq
    restart: unless-stopped
    ports:
      - "8090:8090"
      - "8091:8091"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - /var/lib/scarlix:/var/lib/scarlix
      - /etc/scarlix:/etc/scarlix:ro
      - /opt/scarlix:/opt/scarlix:ro
    environment:
      - SCARLIHQ_PORT=8090
      - TZ=Europe/Bratislava
    networks: [scarlix_net, scarlix_ai]
networks:
  scarlix_net: { external: true }
  scarlix_ai: { external: true }
YAML

cd /opt/scarlix/scarlihq
docker compose up -d
```

✅ **Overenie:**
```bash
curl http://localhost:8090/api/health
# Musí vrátiť {"status":"ok","version":"v12.0"}
curl http://localhost:8090/api/gpu
# Musí vrátiť GPU status JSON
```

---

## 22. 👥 PROFILY (4 USERS)

```bash
sudo mkdir -p /etc/scarlix/profiles

# Zmor (admin)
sudo tee /etc/scarlix/profiles/zmor.yaml << 'EOF'
name: zmor
display_name: "Zmor (admin)"
role: admin
permissions: [all]
scarlix_mode:
  default: ai
  allowed: [ai, game, turbo, offline]
  can_switch: true
hud_theme: iron-man
token_budget: unlimited
EOF

# Hugo (syn)
sudo tee /etc/scarlix/profiles/hugo.yaml << 'EOF'
name: hugo
display_name: "Hugo (syn)"
role: family
permissions: [read_files, read_photos, play_games, coding_tutor, ai_chat]
scarlix_mode:
  default: game
  allowed: [game, ai]
  can_switch: true
hud_theme: gaming
token_budget:
  monthly: 100000
  model: qwen3.6:14b-ollama
EOF

# XOX (dcéra)
sudo tee /etc/scarlix/profiles/xox.yaml << 'EOF'
name: xox
display_name: "XOX (dcera)"
role: family
permissions: [read_photos_only, kids_content, ai_chat]
scarlix_mode:
  default: ai
  allowed: [ai]
  can_switch: false
hud_theme: creative
token_budget:
  monthly: 10000
  model: qwen3.6:14b-ollama
content_filter:
  enabled: true
  blocklist: [violence, adult, drugs, gambling]
EOF

# Mon (manželka)
sudo tee /etc/scarlix/profiles/mon.yaml << 'EOF'
name: mon
display_name: "Mon (manzelka)"
role: family
permissions: [read_files, read_photos, write_files, voice_assistant, ai_chat]
scarlix_mode:
  default: ai
  allowed: [ai, game]
  can_switch: true
hud_theme: elegant
token_budget:
  monthly: 50000
  model: qwen3.6:14b-ollama
EOF
```

### LiteLLM Virtual Keys (per profil):

```bash
# Zmor (unlimited)
curl -X POST http://localhost:4000/key/generate \
  -H "Authorization: Bearer $LITELLM_MASTER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"key_alias":"zmor","max_budget":0,"models":["qwen3.6:14b"]}'

# Hugo (100k tokens/mesiac)
curl -X POST http://localhost:4000/key/generate \
  -H "Authorization: Bearer $LITELLM_MASTER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"key_alias":"hugo","max_budget":100000,"budget_duration":"30d","models":["qwen3.6:14b-ollama"]}'

# XOX (10k tokens/mesiac)
curl -X POST http://localhost:4000/key/generate \
  -H "Authorization: Bearer $LITELLM_MASTER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"key_alias":"xox","max_budget":10000,"budget_duration":"30d","models":["qwen3.6:14b-ollama"]}'

# Mon (50k tokens/mesiac)
curl -X POST http://localhost:4000/key/generate \
  -H "Authorization: Bearer $LITELLM_MASTER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"key_alias":"mon","max_budget":50000,"budget_duration":"30d","models":["qwen3.6:14b-ollama"]}'
```

> 💡 **Ulož si vygenerované API keys** pre každý profil!

---

## 23. 🎮 SCARLIX-MODE SCRIPT

```bash
sudo tee /usr/local/bin/scarlix-mode << 'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
MODE="${1:-status}"

case "$MODE" in
  ai)
    echo "🧠 AI mode: SGLang GPU 0, Ollama GPU 1"
    docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml up -d
    docker compose -f /opt/scarlix/ai/ollama/docker-compose.yml up -d
    docker stop sunshine 2>/dev/null || true
    docker stop llamacpp 2>/dev/null || true
    echo "ai" | sudo tee /var/lib/scarlix/current-mode
    ;;
  game)
    echo "🎮 Game mode: Sunshine GPU 0, Ollama GPU 1"
    ACTIVE=$(docker exec hermes curl -s http://localhost:7999/api/active-tasks 2>/dev/null | jq '.count // 0' || echo 0)
    if [ "$ACTIVE" -gt 0 ] 2>/dev/null; then
      echo "⚠️  Hermes has $ACTIVE active tasks. Waiting 30s..."
      sleep 30
    fi
    docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml stop
    docker compose -f /opt/scarlix/gaming/docker-compose.yml up -d sunshine 2>/dev/null || echo "Sunshine not configured"
    echo "game" | sudo tee /var/lib/scarlix/current-mode
    ;;
  turbo)
    echo "⚡ Turbo mode: dual inference"
    docker compose -f /opt/scarlix/ai/sglang/docker-compose.yml up -d
    docker stop sunshine 2>/dev/null || true
    echo "turbo" | sudo tee /var/lib/scarlix/current-mode
    ;;
  offline)
    echo "🔌 Offline mode: llama.cpp CPU only"
    docker stop sglang ollama-agent sunshine 2>/dev/null || true
    docker compose -f /opt/scarlix/ai/llamacpp/docker-compose.yml up -d
    echo "offline" | sudo tee /var/lib/scarlix/current-mode
    ;;
  status)
    echo "=== SCARLIX OS v12 Status ==="
    echo "Mode: $(cat /var/lib/scarlix/current-mode 2>/dev/null || echo unknown)"
    echo ""
    nvidia-smi --query-gpu=index,name,memory.used,memory.free,utilization.gpu --format=csv,noheader
    echo ""
    docker ps --format "table {{.Names}}\t{{.Status}}" | head -25
    ;;
  *)
    echo "Usage: scarlix-mode {ai|game|turbo|offline|status}"
    exit 1
    ;;
esac
SCRIPT

sudo chmod +x /usr/local/bin/scarlix-mode
sudo mkdir -p /var/lib/scarlix
echo "ai" | sudo tee /var/lib/scarlix/current-mode
```

✅ **Overenie:**
```bash
scarlix-mode status
# Musí ukázať current mode + GPU status + kontajnery
```

---

## 24. 💾 BACKUP KONFIGURÁCIA

```bash
# Inštaluj Restic
sudo apt install -y restic

# Init backup repo
sudo mkdir -p /mnt/backup/restic
export RESTIC_REPOSITORY=/mnt/backup/restic
export RESTIC_PASSWORD=$(grep RESTIC_PASSWORD /etc/scarlix/.env | cut -d= -f2)
restic init

# Backup script
sudo tee /opt/scarlix/scripts/backup.sh << 'EOF'
#!/bin/bash
set -euo pipefail
source /etc/scarlix/.env

export RESTIC_REPOSITORY=/mnt/backup/restic
export RESTIC_PASSWORD=$RESTIC_PASSWORD

TS=$(date +%Y%m%d-%H%M%S)
echo "=== SCARLIX OS v12 Backup ==="

# LVM snapshot
if sudo lvdisplay /dev/vg0/lv-root &>/dev/null; then
  echo "[1/3] Creating LVM snapshot..."
  sudo lvcreate -L 20G -s -n "root-snap-${TS}" /dev/vg0/lv-root
fi

# Restic backup
echo "[2/3] Running Restic backup..."
restic backup \
  /etc/scarlix \
  /opt/scarlix \
  /var/lib/scarlix \
  /var/lib/buzz \
  /var/lib/sglang \
  --tag daily --tag scarlix-v12

# Cleanup
echo "[3/3] Cleaning old backups..."
restic forget --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune

echo "=== Backup complete: $TS ==="
EOF

sudo chmod +x /opt/scarlix/scripts/backup.sh

# Cron (denne o 3:00)
echo "0 3 * * * scarlix /opt/scarlix/scripts/backup.sh >> /var/log/scarlix-backup.log 2>&1" | sudo tee /etc/cron.d/scarlix-backup
```

✅ **Overenie:**
```bash
sudo /opt/scarlix/scripts/backup.sh
restic snapshots
# Musí ukázať aspoň 1 snapshot
```

---

## 25. 📺 TV DASHBOARD SETUP

### Krok 25.1: Pripoj TV k sieti

1. Pripoj televízor k rovnakej LAN ako Main PC (WiFi alebo kábel)
2. Otvor browser na TV (alebo pripoj mini PC / Raspberry Pi k TV)

### Krok 25.2: Nastav DNS (voliteľné, ale odporúčané)

Na tvojom routeri nastav:
- `scarlix.local` → `192.168.1.100`
- `*.scarlix.local` → `192.168.1.100`

Alebo použi Headscale MagicDNS.

### Krok 25.3: Otvor Dashboard

1. Na TV browseri otvor: `http://192.168.1.100:8090`
   - Alebo: `http://scarlix.local:8090`
   - Alebo (ak máš Caddy + doménu): `https://scarlix.example.com`

2. Mal by si vidieť **ScarliHQ 2D Dashboard** s:
   - GPU Status (2× NVIDIA)
   - System Mode (ai/game/turbo/offline)
   - Profiles (Zmor/Hugo/XOX/Mon)
   - Containers list
   - Agent Status (Hermes/OpenCode)
   - Buzz Workspace status

### Krok 25.4: TV Browser Optimization

Ak TV browser nefunguje dobre, použi:
- **Raspberry Pi 4** + Chromium kiosk mode
- **Intel NUC** + Firefox
- **Mini PC** pripojený cez HDMI

**Kiosk mode (Raspberry Pi):**
```bash
# Na Raspberry Pi:
sudo apt install -y chromium-browser x11-xserver-utils unclutter

# Autostart script
mkdir -p ~/.config/autostart
cat > ~/.config/autostart/scarlix-dashboard.desktop << 'EOF'
[Desktop Entry]
Type=Application
Name=SCARLIX Dashboard
Exec=chromium-browser --kiosk --noerrdialogs --disable-translate --no-first-run --fast --fast-start http://192.168.1.100:8090
Terminal=false
EOF

# Skryj cursor po 3s nečinnosti
echo "unclutter -idle 3 &" >> ~/.config/lxsession/LXDE-pi/autostart
```

### Krok 25.5: Voice ovládanie (voliteľné)

Pre voice ovládanie z obývačky:
1. Pripoj USB mikrofón k TV/Raspberry Pi
2. Nainštaluj wake word klienta
3. "Jarvis, aký je stav systému?" → odpoveď cez Piper TTS

✅ **Overenie:**
- Dashboard sa zobrazí na TV
- GPU status sa aktualizuje každé 3s
- Môžeš prepínať profily
- Môžeš prepínať scarlix-mode (ai/game/turbo/offline)

---

## 26. ✅ FINÁLNA VERIFIKÁCIA

Spusti tento kompletný checklist:

```bash
#!/bin/bash
echo "=== SCARLIX OS v12 — Final Verification ==="

echo ""
echo "=== 1. System ==="
echo "Hostname: $(hostnamectl | grep Static | awk '{print $3}')"
echo "Timezone: $(timedatectl | grep 'Time zone' | awk '{print $3}')"
echo "UFW: $(sudo ufw status | head -1)"
echo "Fail2ban: $(sudo fail2ban-client status | head -1)"

echo ""
echo "=== 2. NVIDIA ==="
nvidia-smi --query-gpu=index,name,driver_version,memory.total --format=csv,noheader

echo ""
echo "=== 3. Docker ==="
echo "Docker: $(docker --version)"
echo "Compose: $(docker compose version --short)"
echo "Networks: $(docker network ls | grep scarlix | awk '{print $2}' | tr '\n' ' ')"
echo "Containers: $(docker ps --format '{{.Names}}' | wc -l) running"

echo ""
echo "=== 4. AI Stack ==="
echo "SGLang: $(curl -s http://localhost:30000/health 2>/dev/null || echo 'DOWN')"
echo "Ollama: $(curl -s http://localhost:11435/api/tags 2>/dev/null | jq -r '.models[0].name' 2>/dev/null || echo 'DOWN')"
echo "LiteLLM: $(curl -s http://localhost:4000/health 2>/dev/null || echo 'DOWN')"

echo ""
echo "=== 5. Agents ==="
echo "Hermes: $(curl -s http://localhost:7999/health 2>/dev/null || echo 'DOWN')"
echo "Buzz: $(curl -s http://localhost:3000 2>/dev/null | head -c 50 || echo 'DOWN')"

echo ""
echo "=== 6. Voice ==="
echo "Whisper: $(docker ps --format '{{.Names}}' | grep whisper || echo 'DOWN')"
echo "Piper: $(docker ps --format '{{.Names}}' | grep piper || echo 'DOWN')"
echo "WakeWord: $(docker ps --format '{{.Names}}' | grep wakeword || echo 'DOWN')"

echo ""
echo "=== 7. Network ==="
echo "Headscale: $(docker ps --format '{{.Names}}' | grep headscale || echo 'DOWN')"
echo "Caddy: $(docker ps --format '{{.Names}}' | grep caddy || echo 'DOWN')"
echo "Authelia: $(curl -s http://localhost:9091/api/health 2>/dev/null || echo 'DOWN')"

echo ""
echo "=== 8. Security ==="
echo "CrowdSec: $(docker ps --format '{{.Names}}' | grep crowdsec || echo 'DOWN')"
echo "Socket Proxy: $(docker ps --format '{{.Names}}' | grep socket-proxy || echo 'DOWN')"

echo ""
echo "=== 9. Monitoring ==="
echo "VictoriaMetrics: $(curl -s http://localhost:8428/health 2>/dev/null || echo 'DOWN')"
echo "Grafana: $(curl -s http://localhost:3000/api/health 2>/dev/null | jq -r '.database' 2>/dev/null || echo 'DOWN')"

echo ""
echo "=== 10. ScarliHQ ==="
echo "Dashboard: $(curl -s http://localhost:8090/api/health 2>/dev/null || echo 'DOWN')"
echo "MCP: $(curl -s http://localhost:8090/mcp 2>/dev/null | jq -r '.server' 2>/dev/null || echo 'DOWN')"

echo ""
echo "=== 11. scarlix-mode ==="
scarlix-mode status

echo ""
echo "=== VERIFICATION COMPLETE ==="
```

### Úspešné kritériá:

- [ ] Všetkých 14 kontajnerov beží na Main PC
- [ ] Všetkých 7 kontajnerov beží na HP Agent PC
- [ ] SGLang vracia modely na `http://localhost:30000/v1/models`
- [ ] LiteLLM failover funguje (zastav SGLang → Ollama prevezme)
- [ ] Hermes odpovedá na Telegram
- [ ] Buzz relay beží na porte 3000
- [ ] Dashboard sa zobrazí na `http://192.168.1.100:8090`
- [ ] `scarlix-mode status` funguje
- [ ] `scarlix-mode ai/game/turbo/offline` prepína GPU
- [ ] TV zobrazuje dashboard
- [ ] Backup beží (restic snapshots existujú)

---

## 27. 🔧 TROUBLESHOOTING

| Problém | Riešenie |
|---------|----------|
| `nvidia-smi` zlyhá | `sudo apt purge 'nvidia-*' && sudo apt install nvidia-driver-570-open && sudo reboot` |
| SGLang OOM | Zníž `--mem-fraction-static 0.80` v docker-compose |
| LiteLLM connection refused | `docker logs litellm` → skontroluj config.yaml |
| Buzz relay crash | `docker logs buzz-relay` → skontroluj Postgres/Redis/MinIO |
| Hermes neodpovedá | `docker logs hermes` → skontroluj LiteLLM URL a API key |
| OpenCode error | `opencode doctor` → skontroluj provider config |
| Caddy 502 | Backend nebeží → `docker ps` → reštartuj |
| Authelia loop | Vymaž cookies pre scarlix.example.com |
| GPU handoff zlyhá | `scarlix-mode status` → manuálne `docker stop/start` |
| Whisper CUDA error | GPU 1 plná → zatvor iné GPU 1 procesy |
| Docker network conflict | `docker network rm scarlix_net scarlix_ai && bash docker/networks.sh` |
| Port už používaný | `sudo lsof -i :PORT` → nájdi a zabij proces |
| Disk plný | `df -h` → `docker system prune -a` (opatrne!) |
| Kontajner sa nespustí | `docker logs CONTAINER_NAME` → pozri chybu |

---

## 28. 📡 PORT REFERENCIA

### Main PC (192.168.1.100 / 100.64.0.1)

| Port | Služba | Účel |
|------|--------|------|
| 22 | SSH | Vzdialený prístup |
| 80 | Caddy | HTTP (presmeruje na 443) |
| 443 | Caddy | HTTPS (auto-cert) |
| 3000 | Buzz Relay | Nostr workspace |
| 4000 | LiteLLM | LLM Gateway (OpenAI API) |
| 5432 | Buzz Postgres | Buzz databáza (interné) |
| 6379 | Buzz Redis | Buzz cache (interné) |
| 7999 | Hermes Agent | CEO agent API |
| 8090 | ScarliHQ | 2D Dashboard + REST API |
| 8091 | ScarliHQ | WebSocket + MCP |
| 8428 | VictoriaMetrics | Metrics storage |
| 9000 | Buzz MinIO | S3 media (interné) |
| 9091 | Authelia | SSO + 2FA |
| 9090 | Grafana | Monitoring dashboards |
| 30000 | SGLang | Inference (GPU 0) |
| 41641/udp | Headscale | WireGuard VPN |
| 4317 | OTel Collector | gRPC traces |
| 4318 | OTel Collector | HTTP traces |
| 8002 | Whisper SK | Slovak STT |
| 8003 | Piper SK | Slovak TTS |
| 10400 | WakeWord | "Jarvis" detection |
| 11435 | Ollama | Inference (GPU 1) |
| 11438 | llama.cpp | CPU fallback (offline) |
| 47990 | Sunshine | Game streaming (game mode) |

### HP Agent PC (192.168.1.101 / 100.64.0.2)

| Port | Služba | Účel |
|------|--------|------|
| 22 | SSH | Vzdialený prístup |
| 3000 | Gitea | Git server |
| 5678 | n8n | Workflows |
| 8000 | Coolify | PaaS (auto-deploy) |
| 8080 | Nextcloud | Súbory |
| 2342 | Photoprism | Fotky |
| 5432 | PostgreSQL | Zdieľaná databáza |
| 6379 | Redis | Cache |

---

## 29. 📖 ČO ROBÍ KAŽDÝ PROGRAM

### AI Stack

| Program | Čo robí | Prečo ho potrebuješ |
|---------|---------|---------------------|
| **SGLang** | High-performance LLM inference server s RadixAttention | Primárny AI engine — 3-5x rýchlejší ako Ollama, používa safetensors |
| **Ollama** | GGUF inference server | Concurrent/fallback — beží na GPU 1, jednoduchšie na modely |
| **llama.cpp** | CPU-only inference | Offline fallback — funguje bez GPU (WW3 scenario) |
| **LiteLLM** | LLM gateway s failover | Jediný API endpoint pre všetky modely + virtual keys + rate limiting |

### Agents

| Program | Čo robí | Prečo ho potrebuješ |
|---------|---------|---------------------|
| **Hermes** | CEO agent — multi-platform (Telegram, Discord), self-improving | Tvoj hlavný AI asistent — prijíma príkazy, deleguje, učí sa |
| **OpenCode** | Coding Manager — build/plan agents, git worktree | Autonómne píše kód, testuje, commituje, deploynuje |
| **gstack** | 23 senior manager tools pre Hermes | Designer, Eng Manager, CFO, Security, atď. — virtuálny tím |

### Workspace

| Program | Čo robí | Prečo ho potrebuješ |
|---------|---------|---------------------|
| **Buzz** | Nostr relay workspace | Signed audit trail — každá agentská akcia je kryptograficky podpísaná |
| **Postgres** | Databáza pre Buzz | Ukladá Nostr eventy |
| **Redis** | Cache pre Buzz | Rýchly prístup k aktívnym eventom |
| **MinIO** | S3-compatible storage pre Buzz | Media súbory (obrázky, dokumenty) |

### Voice

| Program | Čo robí | Prečo ho potrebuješ |
|---------|---------|---------------------|
| **Whisper** | Slovak speech-to-text | "Jarvis, stav systému" → text pre AI |
| **Piper** | Slovak text-to-speech | AI odpoveď → hlas (sk_SK-lili) |
| **WakeWord** | Detekcia "Jarvis" | Aktivuje voice pipeline bez klikania |

### Network

| Program | Čo robí | Prečo ho potrebuješ |
|---------|---------|---------------------|
| **Headscale** | WireGuard mesh VPN | Prístup zo všetkých zariadení bez ohľadu na sieť |
| **Caddy** | Reverse proxy s auto-HTTPS | Jediný vstupný bod (port 443) pre všetky služby |
| **Authelia** | SSO + 2FA (TOTP/WebAuthn) | Jeden login pre všetky služby |

### Security

| Program | Čo robí | Prečo ho potrebuješ |
|---------|---------|---------------------|
| **UFW** | Firewall | Blokuje neoprávnený prístup |
| **ufw-docker** | Docker firewall fix | Zabraňuje Dockeru obísť UFW (iptables:false) |
| **CrowdSec** | Intrusion prevention | Detekuje a blokuje útoky (brute force, CVE) |
| **Docker Socket Proxy** | Obmedzený prístup k Docker API | Agenty môžu čítať kontajnery, ale nemôžu mazať |
| **Fail2ban** | SSH brute force protection | Banuje IP po 3 neúspešných prihláseniach |

### Monitoring

| Program | Čo robí | Prečo ho potrebuješ |
|---------|---------|---------------------|
| **VictoriaMetrics** | Time-series databáza | Ukladá metriky (CPU, RAM, GPU, kontajnery) |
| **Grafana** | Dashboard pre metriky | Vizuálne prehľady systému |
| **OpenTelemetry** | Tracing + metrics collection | Štandardizovaný tracing pre aplikácie |

### HP Agent Stack

| Program | Čo robí | Prečo ho potrebuješ |
|---------|---------|---------------------|
| **Gitea** | Git server | Lokálny GitHub — kód, PRs, issues |
| **Coolify** | PaaS (auto-deploy) | Git push → auto-build → auto-deploy (ako Heroku) |
| **Nextcloud** | Súborový server | Rodinné súbory, kalendár, kontakty |
| **Photoprism** | Photo management | Rodinné fotky s AI tagovaním |
| **n8n** | Workflow automation | Vizuálne automation (ako Zapier, ale self-hosted) |

### ScarliHQ

| Program | Čo robí | Prečo ho potrebuješ |
|---------|---------|---------------------|
| **ScarliHQ** | Go binary — mozog OS | Orchestrátor: 2D dashboard, API, MCP, profily, guard |

---

## 🎉 DOKONČENÉ!

Ak si prešiel všetkými krokmi a verifikácia prešla, **SCARLIX OS v12 je pripravený!**

```
  ___  ___ ___ ___ _  _ _____ ___ ___ ___ ___
 / __|/ __| __| _ \ || |_   _| __| _ \ __| _ \
 \__ \ (__| _||   / __ | | | | _||   / _||   /
 |___/\___|___|_|_\_||_| |_| |___|_|_\___|_|_\

  v12 "Sovereign Agent Compute Edition"
```

**Tvoj systém teraz:**
- ✅ Beží na 2 serveroch (Main + HP Agent)
- ✅ 21 kontajnerov pracuje spolu
- ✅ 3-tier AI inference (SGLang → Ollama → llama.cpp)
- ✅ Hermes (CEO) prijíma príkazy cez Telegram
- ✅ OpenCode (Coding Manager) píše a deploynuje kód
- ✅ Buzz Nostr workspace zaznamenáva všetko (audit trail)
- ✅ 4 profily s token budgetmi
- ✅ Voice pipeline ("Jarvis, ...")
- ✅ Dashboard na TV v obývačke
- ✅ 100% offline-capable
- ✅ Automatický backup
- ✅ Monitoring + security

**Ďalšie kroky:**
1. Otvor dashboard na TV: `http://192.168.1.100:8090`
2. Otestuj Telegram: pošli správu Hermesovi
3. Otestuj voice: "Jarvis, aký je stav systému?"
4. Nastav Coolify auto-deploy pre tvoje projekty
5. Pridaj fotky do Photoprism
6. Nastav Nextcloud pre rodinu

**Vitaj v SCARLIX OS v12! 🚀**

---

*Dokument vytvoril: MoZoHuJa + Z.ai Code*
*Verzia: v12.0 | Dátum: August 2026*
*Licencia: MIT*
