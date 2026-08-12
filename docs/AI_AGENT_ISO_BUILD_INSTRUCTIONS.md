# SCARLIX OS v12 — MASTER BUILD INSTRUCTIONS FOR AI AGENT

## CRITICAL: Read This First

You are an expert DevOps engineer. Your task is to build a **complete bootable ISO installer** for SCARLIX OS v12 — a sovereign Linux AI appliance OS.

The ISO must allow a user to:
1. Write it to a USB drive
2. Boot any PC from USB
3. Select "Main PC" or "HP Agent PC" from a GRUB menu
4. Wait 30 minutes
5. Reboot into a fully working SCARLIX OS v12 system

## Project Context

SCARLIX OS v12 is a 2-server home AI system:
- **Main PC** (192.168.1.100): AI brain, 2× NVIDIA GPU, 14 Docker containers
- **HP Agent PC** (192.168.1.101): Coding/files worker, 7 Docker containers

**Total: 21 containers** running SGLang, Ollama, llama.cpp, LiteLLM, Hermes, OpenCode, Buzz, ScarliHQ, voice pipeline, security stack, monitoring.

**Base OS:** Ubuntu 24.04.2 LTS Server
**Repo:** https://github.com/MoZoHuJa/OS.git (private, ask user for access)

---

## STEP 1: Create Project Structure

Create a working directory and clone the repo:

```bash
mkdir -p ~/scarlix-iso-builder
cd ~/scarlix-iso-builder
git clone https://github.com/MoZoHuJa/OS.git scarlix-src
```

If the repo is private, ask the user for their GitHub Personal Access Token.

Create the installer directory structure:

```
~/scarlix-iso-builder/
├── scarlix-src/              # Cloned repo (existing files)
├── installer/
│   ├── ansible/
│   │   ├── site.yml
│   │   ├── inventory.yml
│   │   ├── group_vars/
│   │   │   ├── main.yml
│   │   │   └── hp_agent.yml
│   │   ├── roles/
│   │   │   ├── base_os/
│   │   │   ├── nvidia/
│   │   │   ├── docker/
│   │   │   ├── ai_stack/
│   │   │   ├── workspace/
│   │   │   ├── agents/
│   │   │   ├── voice/
│   │   │   ├── network/
│   │   │   ├── security/
│   │   │   ├── monitoring/
│   │   │   ├── hp_agent_stack/
│   │   │   ├── scarlihq/
│   │   │   ├── profiles/
│   │   │   └── verify/
│   │   └── templates/
│   ├── cloud-init/
│   │   ├── main-pc/
│   │   │   ├── user-data
│   │   │   └── meta-data
│   │   ├── hp-agent/
│   │   │   ├── user-data
│   │   │   └── meta-data
│   │   └── interactive/
│   │       ├── user-data
│   │       └── meta-data
│   ├── grub/
│   │   └── grub.cfg
│   ├── scripts/
│   │   ├── build-iso.sh
│   │   ├── scarlix-wizard.sh
│   │   ├── download-models.sh
│   │   └── first-boot.sh
│   └── README-INSTALLER.md
└── build-log.md
```

---

## STEP 2: Create Ansible Playbook (site.yml)

File: `installer/ansible/site.yml`

```yaml
---
# SCARLIX OS v12 — Master Ansible Playbook
# Runs locally on the target machine during ISO installation

- name: SCARLIX OS v12 — Base OS Configuration
  hosts: localhost
  connection: local
  become: yes
  gather_facts: yes

  vars:
    scarlix_version: "v12.0"
    scarlix_user: "scarlix"
    scarlix_home: "/opt/scarlix"
    scarlix_config: "/etc/scarlix"

  pre_tasks:
    - name: Detect PC type
      set_fact:
        pc_type: "{{ 'main' if ansible_gpu_count | default(0) | int > 0 else 'hp_agent' }}"
      when: pc_type is not defined

    - name: Set PC-specific variables
      include_vars: "{{ item }}"
      with_first_found:
        - "group_vars/{{ pc_type }}.yml"
        - "group_vars/main.yml"

    - name: Display installation info
      debug:
        msg: |
          ================================================
          SCARLIX OS v12 Installation
          PC Type: {{ pc_type }}
          Hostname: {{ scarlix_hostname }}
          IP: {{ scarlix_ip }}
          GPU Count: {{ ansible_gpu_count | default(0) }}
          ================================================

  roles:
    - role: base_os
      tags: [base, always]

    - role: nvidia
      when: pc_type == 'main'
      tags: [nvidia, gpu]

    - role: docker
      tags: [docker]

    - role: ai_stack
      when: pc_type == 'main'
      tags: [ai, inference]

    - role: workspace
      when: pc_type == 'main'
      tags: [workspace, buzz]

    - role: agents
      when: pc_type == 'main'
      tags: [agents, hermes]

    - role: voice
      when: pc_type == 'main'
      tags: [voice]

    - role: network
      tags: [network, vpn, proxy]

    - role: security
      tags: [security]

    - role: monitoring
      when: pc_type == 'main'
      tags: [monitoring]

    - role: hp_agent_stack
      when: pc_type == 'hp_agent'
      tags: [hp_agent]

    - role: scarlihq
      when: pc_type == 'main'
      tags: [scarlihq]

    - role: profiles
      tags: [profiles]

    - role: verify
      tags: [verify, always]

  post_tasks:
    - name: Installation complete
      debug:
        msg: |
          ================================================
          ✅ SCARLIX OS v12 installation complete!
          Hostname: {{ scarlix_hostname }}
          IP: {{ scarlix_ip }}
          Dashboard: http://{{ scarlix_ip }}:8090
          ================================================
```

---

## STEP 3: Create Inventory

File: `installer/ansible/inventory.yml`

```yaml
---
all:
  hosts:
    localhost:
      ansible_connection: local
      ansible_python_interpreter: /usr/bin/python3
```

File: `installer/ansible/group_vars/main.yml`

```yaml
---
pc_type: main
scarlix_hostname: scarlix-main
scarlix_ip: 192.168.1.100
scarlix_netmask: 255.255.255.0
scarlix_gateway: 192.168.1.1
scarlix_dns: [1.1.1.1, 8.8.8.8]

# GPU config
gpu_0_pci: "0000:01:00.0"
gpu_1_pci: "0000:02:00.0"

# Models
sglang_model: "Qwen3-14B-Instruct"
ollama_model: "qwen3.6:14b"

# Docker
docker_data_root: "/var/lib/docker"

# LiteLLM
litellm_master_key: "sk-scarlix-change-me"

# Telegram (user provides during install)
telegram_bot_token: ""
telegram_chat_id: ""
```

File: `installer/ansible/group_vars/hp_agent.yml`

```yaml
---
pc_type: hp_agent
scarlix_hostname: scarlix-hp
scarlix_ip: 192.168.1.101
scarlix_netmask: 255.255.255.0
scarlix_gateway: 192.168.1.1
scarlix_dns: [1.1.1.1, 8.8.8.8]

# No GPU on HP Agent
gpu_0_pci: ""
gpu_1_pci: ""

# Main PC IP for API access
main_pc_ip: "192.168.1.100"
litellm_url: "http://192.168.1.100:4000/v1"
scarlihq_mcp_url: "http://192.168.1.100:8091/mcp"
```

---

## STEP 4: Create Ansible Roles

### Role: base_os

File: `installer/ansible/roles/base_os/tasks/main.yml`

```yaml
---
- name: Update apt cache
  apt:
    update_cache: yes
    upgrade: dist

- name: Install base packages
  apt:
    name:
      - curl
      - wget
      - git
      - jq
      - bc
      - net-tools
      - htop
      - tmux
      - vim
      - nano
      - build-essential
      - dkms
      - "linux-headers-{{ ansible_kernel }}"
      - ufw
      - fail2ban
      - chrony
      - unzip
      - rsync
      - btrfs-progs
      - lvm2
      - python3-pip
      - htop
      - iotop
      - nethogs
      - restic
      - ansible
    state: present

- name: Set hostname
  hostname:
    name: "{{ scarlix_hostname }}"

- name: Set timezone
  timezone:
    name: Europe/Bratislava

- name: Enable and start chrony
  service:
    name: chrony
    state: started
    enabled: yes

- name: Configure static IP (netplan)
  template:
    src: netplan.yaml.j2
    dest: /etc/netplan/01-scarlix.yaml
    mode: '0600'
  notify: apply netplan

- name: Configure sudoers for scarlix user
  copy:
    dest: /etc/sudoers.d/scarlix
    content: |
      scarlix ALL=(ALL) NOPASSWD: /usr/local/bin/scarlix-mode,/usr/bin/docker,/usr/bin/systemctl,/usr/bin/journalctl,/usr/sbin/ufw,/usr/bin/apt
      scarlix ALL=(ALL) PASSWD: ALL
    mode: '0440'
    validate: /usr/sbin/visudo -csf %s

- name: Configure UFW firewall
  ufw:
    state: enabled
    policy: deny
    direction: incoming

- name: Allow SSH
  ufw:
    rule: allow
    port: '22'
    proto: tcp

- name: Allow HTTP/HTTPS (Main PC only)
  ufw:
    rule: allow
    port: '{{ item }}'
    proto: tcp
  loop: [80, 443]
  when: pc_type == 'main'

- name: Allow Headscale WireGuard
  ufw:
    rule: allow
    port: '41641'
    proto: udp

- name: Configure fail2ban
  copy:
    dest: /etc/fail2ban/jail.local
    content: |
      [DEFAULT]
      bantime = 3600
      findtime = 600
      maxretry = 3
      [sshd]
      enabled = true
      port = 22
      maxretry = 3
      bantime = 3600

- name: Enable and start fail2ban
  service:
    name: fail2ban
    state: started
    enabled: yes

- name: Create SCARLIX directories
  file:
    path: "{{ item }}"
    state: directory
    owner: scarlix
    group: scarlix
    mode: '0755'
  loop:
    - /opt/scarlix
    - /opt/scarlix/ai
    - /opt/scarlix/workspace
    - /opt/scarlix/agents
    - /opt/scarlix/voice
    - /opt/scarlix/network
    - /opt/scarlix/security
    - /opt/scarlix/monitoring
    - /opt/scarlix/scripts
    - /var/lib/scarlix
    - /etc/scarlix
    - /etc/scarlix/profiles
    - /models
    - /var/lib/sglang/cache
    - /var/lib/sglang/scratch

- name: Set default mode
  copy:
    dest: /var/lib/scarlix/current-mode
    content: "ai"
    owner: scarlix
    group: scarlix

- name: Copy SCARLIX source files
  copy:
    src: "{{ playbook_dir }}/../../../scarlix-src/"
    dest: /opt/scarlix/
    owner: scarlix
    group: scarlix
    remote_src: yes
```

File: `installer/ansible/roles/base_os/templates/netplan.yaml.j2`

```yaml
network:
  version: 2
  ethernets:
    {{ ansible_default_ipv4.interface }}:
      addresses:
        - {{ scarlix_ip }}/24
      routes:
        - to: default
          via: {{ scarlix_gateway }}
      nameservers:
        addresses: {{ scarlix_dns }}
      dhcp4: false
```

File: `installer/ansible/roles/base_os/handlers/main.yml`

```yaml
---
- name: apply netplan
  command: netplan apply
```

### Role: nvidia

File: `installer/ansible/roles/nvidia/tasks/main.yml`

```yaml
---
- name: Add graphics-drivers PPA
  apt_repository:
    repo: ppa:graphics-drivers/ppa
    state: present
    update_cache: yes

- name: Install NVIDIA driver 570-open
  apt:
    name: nvidia-driver-570-open
    state: present

- name: Download CUDA keyring
  get_url:
    url: https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64/cuda-keyring_1.1-1_all.deb
    dest: /tmp/cuda-keyring.deb

- name: Install CUDA keyring
  apt:
    deb: /tmp/cuda-keyring.deb

- name: Install CUDA toolkit 12.4
  apt:
    name: cuda-toolkit-12-4
    state: present
    update_cache: yes

- name: Add CUDA to PATH
  copy:
    dest: /etc/profile.d/cuda.sh
    content: |
      export PATH=/usr/local/cuda-12.4/bin:$PATH
      export LD_LIBRARY_PATH=/usr/local/cuda-12.4/lib64:$LD_LIBRARY_PATH

- name: Install cuDNN
  apt:
    name:
      - libcudnn9-cuda-12
      - libcudnn9-dev-cuda-12
    state: present

- name: Add NVIDIA Container Toolkit repo
  shell: |
    curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
    curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' > /etc/apt/sources.list.d/nvidia-container-toolkit.list
  args:
    creates: /etc/apt/sources.list.d/nvidia-container-toolkit.list

- name: Install NVIDIA Container Toolkit
  apt:
    name: nvidia-container-toolkit
    state: present
    update_cache: yes

- name: Configure Docker runtime for NVIDIA
  command: nvidia-ctk runtime configure --runtime=docker
  notify: restart docker

- name: Detect GPU PCI addresses
  shell: lspci | grep -i nvidia | awk '{print $1}'
  register: gpu_pci_list
  changed_when: false

- name: Create udev rules for stable GPU indices
  copy:
    dest: /etc/udev/rules.d/99-nvidia-stable.rules
    content: |
      KERNEL=="nvidia", KERNELS=="0000:{{ gpu_pci_list.stdout_lines[0] }}", SYMLINK+="nvidia-ai"
      KERNEL=="nvidia_modeset", KERNELS=="0000:{{ gpu_pci_list.stdout_lines[0] }}", SYMLINK+="nvidia-ai-modeset"
      KERNEL=="nvidia_uvm", KERNELS=="0000:{{ gpu_pci_list.stdout_lines[0] }}", SYMLINK+="nvidia-ai-uvm"
      {% if gpu_pci_list.stdout_lines | length > 1 %}
      KERNEL=="nvidia", KERNELS=="0000:{{ gpu_pci_list.stdout_lines[1] }}", SYMLINK+="nvidia-game"
      KERNEL=="nvidia_modeset", KERNELS=="0000:{{ gpu_pci_list.stdout_lines[1] }}", SYMLINK+="nvidia-game-modeset"
      KERNEL=="nvidia_uvm", KERNELS=="0000:{{ gpu_pci_list.stdout_lines[1] }}", SYMLINK+="nvidia-game-uvm"
      {% endif %}
  notify: reload udev
```

File: `installer/ansible/roles/nvidia/handlers/main.yml`

```yaml
---
- name: restart docker
  service:
    name: docker
    state: restarted

- name: reload udev
  command: udevadm control --reload-rules && udevadm trigger
```

### Role: docker

File: `installer/ansible/roles/docker/tasks/main.yml`

```yaml
---
- name: Add Docker GPG key
  apt_key:
    url: https://download.docker.com/linux/ubuntu/gpg
    state: present
    keyring: /etc/apt/keyrings/docker.gpg

- name: Add Docker repository
  apt_repository:
    repo: "deb [arch={{ 'amd64' if ansible_architecture == 'x86_64' else 'arm64' }} signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu {{ ansible_distribution_release }} stable"
    state: present
    update_cache: yes

- name: Install Docker CE
  apt:
    name:
      - docker-ce
      - docker-ce-cli
      - containerd.io
      - docker-buildx-plugin
      - docker-compose-plugin
    state: present

- name: Add scarlix user to docker group
  user:
    name: scarlix
    groups: docker
    append: yes

- name: Configure Docker daemon (iptables:false — CRITICAL)
  copy:
    dest: /etc/docker/daemon.json
    content: |
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
  notify: restart docker

- name: Create Docker networks
  shell: |
    docker network create scarlix_net --driver bridge --subnet 172.20.0.0/16 2>/dev/null || true
    docker network create scarlix_ai --driver bridge --subnet 172.21.0.0/16 2>/dev/null || true

- name: Install ufw-docker
  get_url:
    url: https://github.com/chaifeng/ufw-docker/raw/master/ufw-docker
    dest: /usr/local/bin/ufw-docker
    mode: '0755'

- name: Install ufw-docker rules
  command: ufw-docker install
  notify: reload ufw

- name: Deploy Docker Socket Proxy
  copy:
    dest: /opt/scarlix/docker/socket-proxy.yml
    content: |
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

- name: Start Docker Socket Proxy
  shell: docker compose -f /opt/scarlix/docker/socket-proxy.yml up -d
```

File: `installer/ansible/roles/docker/handlers/main.yml`

```yaml
---
- name: restart docker
  service:
    name: docker
    state: restarted

- name: reload ufw
  command: ufw reload
```

### Role: ai_stack

File: `installer/ansible/roles/ai_stack/tasks/main.yml`

```yaml
---
# This role deploys SGLang, Ollama, llama.cpp, and LiteLLM
# It copies docker-compose files from the repo and starts them

- name: Copy AI stack docker-compose files
  copy:
    src: "{{ playbook_dir }}/../../../scarlix-src/ai/"
    dest: /opt/scarlix/ai/
    owner: scarlix
    group: scarlix
    remote_src: yes

- name: Create .env file
  template:
    src: env.j2
    dest: /etc/scarlix/.env
    mode: '0600'

- name: Start SGLang (GPU 0)
  shell: |
    set -a; source /etc/scarlix/.env; set +a
    cd /opt/scarlix/ai/sglang && docker compose up -d
  async: 300
  poll: 0

- name: Start Ollama (GPU 1)
  shell: |
    cd /opt/scarlix/ai/ollama && docker compose up -d

- name: Start LiteLLM gateway
  shell: |
    set -a; source /etc/scarlix/.env; set +a
    cd /opt/scarlix/ai/litellm && docker compose up -d

- name: Wait for LiteLLM to be healthy
  uri:
    url: http://localhost:4000/health
    method: GET
  register: result
  until: result.status == 200
  retries: 30
  delay: 5

- name: Display AI stack status
  debug:
    msg: |
      ✅ SGLang: http://localhost:30000
      ✅ Ollama: http://localhost:11435
      ✅ LiteLLM: http://localhost:4000
```

File: `installer/ansible/roles/ai_stack/templates/env.j2`

```ini
# SCARLIX OS v12 — Environment Variables
LITELLM_MASTER_KEY={{ litellm_master_key }}
TELEGRAM_BOT_TOKEN={{ telegram_bot_token }}
TELEGRAM_ZMOR_CHAT_ID={{ telegram_chat_id }}
BUZZ_POSTGRES_PASSWORD={{ buzz_postgres_password | default('ChangeMeBuzzPG!') }}
BUZZ_MINIO_PASSWORD={{ buzz_minio_password | default('ChangeMeBuzzMinio!') }}
POSTGRES_PASSWORD={{ postgres_password | default('ChangeMeMainPG!') }}
REDIS_PASSWORD={{ redis_password | default('ChangeMeRedis!') }}
GRAFANA_PASSWORD={{ grafana_password | default('ChangeMeGrafana!') }}
GITEA_DB_PASSWORD={{ gitea_db_password | default('ChangeMeGitea!') }}
COOLIFY_APP_KEY={{ coolify_app_key | default('change-me-32-chars-hex') }}
COOLIFY_DB_PASSWORD={{ coolify_db_password | default('ChangeMeCoolify!') }}
NEXTCLOUD_DB_PASSWORD={{ nextcloud_db_password | default('ChangeMeNextcloud!') }}
PHOTOPRISM_ADMIN_PASSWORD={{ photoprism_admin_password | default('ChangeMePhoto!') }}
N8N_PASSWORD={{ n8n_password | default('ChangeMeN8n!') }}
N8N_DB_PASSWORD={{ n8n_db_password | default('ChangeMeN8nDB!') }}
RESTIC_PASSWORD={{ restic_password | default('ChangeMeRestic!') }}
SCARLIX_DEFAULT_PROFILE=zmor
SCARLIX_DEFAULT_MODE=ai
```

### Role: verify

File: `installer/ansible/roles/verify/tasks/main.yml`

```yaml
---
- name: Verify hostname
  command: hostnamectl
  register: hostname_result
  failed_when: scarlix_hostname not in hostname_result.stdout

- name: Verify timezone
  command: timedatectl
  register: tz_result
  failed_when: 'Europe/Bratislava' not in tz_result.stdout

- name: Verify Docker (Main PC)
  command: docker ps --format '{{.Names}}'
  register: docker_ps
  when: pc_type == 'main'
  failed_when: false

- name: Verify SGLang (Main PC)
  uri:
    url: http://localhost:30000/health
    method: GET
  register: sglang_health
  when: pc_type == 'main'
  failed_when: false

- name: Verify LiteLLM (Main PC)
  uri:
    url: http://localhost:4000/health
    method: GET
  register: litellm_health
  when: pc_type == 'main'
  failed_when: false

- name: Display verification results
  debug:
    msg: |
      ================================================
      SCARLIX OS v12 — Verification Results
      ================================================
      Hostname: {{ '✅' if scarlix_hostname in hostname_result.stdout else '❌' }} {{ scarlix_hostname }}
      Timezone: {{ '✅' if 'Europe/Bratislava' in tz_result.stdout else '❌' }} Europe/Bratislava
      {% if pc_type == 'main' %}
      Docker: {{ '✅' if docker_ps.stdout | length > 0 else '❌' }} {{ docker_ps.stdout_lines | length }} containers
      SGLang: {{ '✅' if sglang_health.status == 200 else '⏳ Starting...' }}
      LiteLLM: {{ '✅' if litellm_health.status == 200 else '⏳ Starting...' }}
      {% endif %}
      ================================================
      Dashboard: http://{{ scarlix_ip }}:8090
      ================================================
```

---

## STEP 5: Create cloud-init Autoinstall Configs

### Main PC autoinstall

File: `installer/cloud-init/main-pc/user-data`

```yaml
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
        enp3s0:
          addresses: [192.168.1.100/24]
          routes:
            - to: default
              via: 192.168.1.1
          nameservers:
            addresses: [1.1.1.1, 8.8.8.8]
          dhcp4: false
  early-commands:
    - echo "=== SCARLIX OS v12 — Main PC Installation ==="
  late-commands:
    - curtin in-target --target=/target -- apt update
    - curtin in-target --target=/target -- apt install -y ansible git python3-pip
    - curtin in-target --target=/target -- git clone https://github.com/MoZoHuJa/OS.git /opt/scarlix-src
    - curtin in-target --target=/target -- cp -r /opt/scarlix-src/installer /opt/scarlix-installer
    - curtin in-target --target=/target -- ansible-playbook /opt/scarlix-installer/ansible/site.yml -i /opt/scarlix-installer/ansible/inventory.yml -e pc_type=main
    - echo "=== SCARLIX OS v12 Main PC installation complete ==="
```

File: `installer/cloud-init/main-pc/meta-data`

```yaml
instance-id: scarlix-main
local-hostname: scarlix-main
```

### HP Agent PC autoinstall

File: `installer/cloud-init/hp-agent/user-data`

```yaml
#cloud-config
autoinstall:
  version: 1
  interactive-sections: []
  identity:
    hostname: scarlix-hp
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
        enp3s0:
          addresses: [192.168.1.101/24]
          routes:
            - to: default
              via: 192.168.1.1
          nameservers:
            addresses: [1.1.1.1, 8.8.8.8]
          dhcp4: false
  early-commands:
    - echo "=== SCARLIX OS v12 — HP Agent PC Installation ==="
  late-commands:
    - curtin in-target --target=/target -- apt update
    - curtin in-target --target=/target -- apt install -y ansible git python3-pip
    - curtin in-target --target=/target -- git clone https://github.com/MoZoHuJa/OS.git /opt/scarlix-src
    - curtin in-target --target=/target -- cp -r /opt/scarlix-src/installer /opt/scarlix-installer
    - curtin in-target --target=/target -- ansible-playbook /opt/scarlix-installer/ansible/site.yml -i /opt/scarlix-installer/ansible/inventory.yml -e pc_type=hp_agent
    - echo "=== SCARLIX OS v12 HP Agent installation complete ==="
```

File: `installer/cloud-init/hp-agent/meta-data`

```yaml
instance-id: scarlix-hp
local-hostname: scarlix-hp
```

---

## STEP 6: Create GRUB Boot Menu

File: `installer/grub/grub.cfg`

```grub
set default=0
set timeout=15
set gfxmode=auto
set gfxpayload=keep
insmod all_video
insmod gfxterm
insmod png
set menu_color_normal=white/black
set menu_color_highlight=cyan/black

menuentry "  SCARLIX OS v12 — Main PC (AI Brain + 2x NVIDIA GPU)" {
    set gfxpayload=keep
    linux /casper/vmlinuz autoinstall ds=nocloud\;s=/cdrom/cloud-init/main-pc/ quiet splash ---
    initrd /casper/initrd
}

menuentry "  SCARLIX OS v12 — HP Agent PC (Worker — Coding + Files)" {
    set gfxpayload=keep
    linux /casper/vmlinuz autoinstall ds=nocloud\;s=/cdrom/cloud-init/hp-agent/ quiet splash ---
    initrd /casper/initrd
}

menuentry "  SCARLIX OS v12 — Interactive (TUI Wizard)" {
    set gfxpayload=keep
    linux /casper/vmlinuz ds=nocloud\;s=/cdrom/cloud-init/interactive/ quiet ---
    initrd /casper/initrd
}

menuentry "  Ubuntu 24.04 LTS (Manual — no SCARLIX)" {
    set gfxpayload=keep
    linux /casper/vmlinuz ---
    initrd /casper/initrd
}
```

---

## STEP 7: Create ISO Build Script

File: `installer/scripts/build-iso.sh`

```bash
#!/usr/bin/env bash
set -euo pipefail

echo "================================================"
echo "  SCARLIX OS v12 — ISO Builder"
echo "================================================"

BUILDER_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SCARLIX_SRC="${BUILDER_DIR}/scarlix-src"
INSTALLER_DIR="${BUILDER_DIR}/installer"
UBUNTU_ISO="${BUILDER_DIR}/ubuntu-24.04.2-live-server-amd64.iso"
OUTPUT_ISO="${BUILDER_DIR}/scarlix-os-v12-installer.iso"
WORK_DIR="${BUILDER_DIR}/iso-work"
OVERLAY_DIR="${BUILDER_DIR}/iso-overlay"

# 1. Download Ubuntu ISO if not present
if [ ! -f "$UBUNTU_ISO" ]; then
    echo "[1/8] Downloading Ubuntu 24.04.2 LTS Server ISO..."
    wget -O "$UBUNTU_ISO" \
      "https://releases.ubuntu.com/24.04/ubuntu-24.04.2-live-server-amd64.iso"
else
    echo "[1/8] Ubuntu ISO already exists."
fi

# 2. Extract Ubuntu ISO
echo "[2/8] Extracting Ubuntu ISO..."
mkdir -p "$WORK_DIR"
# Mount and copy
sudo mount -o loop "$UBUNTU_ISO" /mnt/ubuntu-iso
cp -r /mnt/ubuntu-iso/* "$WORK_DIR/"
sudo umount /mnt/ubuntu-iso

# 3. Create overlay directory with SCARLIX files
echo "[3/8] Creating SCARLIX overlay..."
mkdir -p "$OVERLAY_DIR/cloud-init"
mkdir -p "$OVERLAY_DIR/opt/scarlix-src"

# Copy SCARLIX repo
cp -r "$SCARLIX_SRC/"* "$OVERLAY_DIR/opt/scarlix-src/"

# Copy installer
cp -r "$INSTALLER_DIR" "$OVERLAY_DIR/opt/scarlix-installer"

# Copy cloud-init configs
cp -r "$INSTALLER_DIR/cloud-init/"* "$OVERLAY_DIR/cloud-init/"

# 4. Replace GRUB config
echo "[4/8] Configuring GRUB boot menu..."
cp "$INSTALLER_DIR/grub/grub.cfg" "$WORK_DIR/boot/grub/grub.cfg"

# Also configure isolinux if present
if [ -d "$WORK_DIR/isolinux" ]; then
    cat > "$WORK_DIR/isolinux/isolinux.cfg" << 'ISOLINUX'
DEFAULT vesamenu.c32
TIMEOUT 150
PROMPT 0

LABEL main
  MENU LABEL SCARLIX OS v12 — Main PC (AI Brain)
  KERNEL /casper/vmlinuz
  APPEND initrd=/casper/initrd autoinstall ds=nocloud;s=/cdrom/cloud-init/main-pc/ quiet ---

LABEL hp
  MENU LABEL SCARLIX OS v12 — HP Agent PC (Worker)
  KERNEL /casper/vmlinuz
  APPEND initrd=/casper/initrd autoinstall ds=nocloud;s=/cdrom/cloud-init/hp-agent/ quiet ---

LABEL interactive
  MENU LABEL SCARLIX OS v12 — Interactive (TUI Wizard)
  KERNEL /casper/vmlinuz
  APPEND initrd=/casper/initrd ds=nocloud;s=/cdrom/cloud-init/interactive/ quiet ---

LABEL manual
  MENU LABEL Ubuntu 24.04 (Manual)
  KERNEL /casper/vmlinuz
  APPEND initrd=/casper/initrd ---
ISOLINUX
fi

# 5. Merge overlay into work directory
echo "[5/8] Merging SCARLIX overlay..."
cp -r "$OVERLAY_DIR/"* "$WORK_DIR/"

# 6. Regenerate md5sum
echo "[6/8] Regenerating checksums..."
cd "$WORK_DIR"
find . -type f -print0 | xargs -0 md5sum > md5sum.txt

# 7. Build ISO
echo "[7/8] Building ISO..."
xorriso -as mkisofs \
  -r -V "SCARLIX-OS-v12" \
  -J -joliet-long \
  -b boot/grub/i386-pc/eltorito.img \
  -c boot.catalog \
  -no-emul-boot -boot-load-size 4 -boot-info-table \
  -eltorito-alt-boot \
  -e boot/grub/efi.img \
  -no-emul-boot \
  -append_partition 2 0xef boot/grub/efi.img \
  -o "$OUTPUT_ISO" \
  "$WORK_DIR"

# 8. Cleanup
echo "[8/8] Cleaning up..."
rm -rf "$WORK_DIR" "$OVERLAY_DIR"

echo ""
echo "================================================"
echo "  ✅ ISO BUILD COMPLETE"
echo "================================================"
echo "  Output: $OUTPUT_ISO"
echo "  Size: $(du -h "$OUTPUT_ISO" | cut -f1)"
echo ""
echo "  Write to USB:"
echo "    sudo dd if=$OUTPUT_ISO of=/dev/sdX bs=4M status=progress"
echo "    sync"
echo ""
echo "  Or use balenaEtcher / Rufus"
echo "================================================"
```

---

## STEP 8: Create TUI Wizard Script

File: `installer/scripts/scarlix-wizard.sh`

```bash
#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v12 — Interactive Installer Wizard
# Uses whiptail (dialog) for TUI

TITLE="SCARLIX OS v12 Installer"
HEIGHT=20
WIDTH=70

# Welcome
whiptail --title "$TITLE" --msgbox \
  "Vitaj v SCARLIX OS v12 instalácii!\n\n\
Tento wizard ťa prevedie konfiguráciou.\n\
Po dokončení sa systém nainštaluje automaticky.\n\n\
Stlač Enter pre pokračovanie." \
  $HEIGHT $WIDTH

# 1. PC Type
PC_TYPE=$(whiptail --title "$TITLE" --menu \
  "Vyber typ tohto PC:" 15 60 2 \
  "main" "Main PC (AI Brain — 2× NVIDIA GPU)" \
  "hp_agent" "HP Agent PC (Worker — coding + files)" \
  3>&1 1>&2 2>&3)

if [ -z "$PC_TYPE" ]; then
  echo "Inštalácia zrušená."
  exit 1
fi

# 2. IP Address
DEFAULT_IP="192.168.1.100"
if [ "$PC_TYPE" = "hp_agent" ]; then
  DEFAULT_IP="192.168.1.101"
fi

IP=$(whiptail --title "$TITLE" --inputbox \
  "IP adresa pre tento PC:" 10 50 "$DEFAULT_IP" 3>&1 1>&2 2>&3)

# 3. Password
PASS=$(whiptail --title "$TITLE" --passwordbox \
  "Heslo pre scarlix user:" 10 50 3>&1 1>&2 2>&3)

PASS_HASH=$(openssl passwd -6 "$PASS")

# 4. Telegram (optional)
TG_TOKEN=$(whiptail --title "$TITLE" --inputbox \
  "Telegram Bot Token (nechaj prázdne ak nechceš):" 10 60 3>&1 1>&2 2>&3)

TG_CHAT_ID=""
if [ -n "$TG_TOKEN" ]; then
  TG_CHAT_ID=$(whiptail --title "$TITLE" --inputbox \
    "Telegram Chat ID (tvoje ID):" 10 50 3>&1 1>&2 2>&3)
fi

# 5. LiteLLM Master Key
LITELLM_KEY="sk-scarlix-$(openssl rand -hex 16)"

# 6. Confirm
CONFIRM=$(whiptail --title "$TITLE" --yesno \
  "=== Potvrdenie inštalácie ===\n\n\
PC Typ: $PC_TYPE\n\
Hostname: scarlix-${PC_TYPE/main/main}\n\
IP: $IP\n\
Telegram: ${TG_TOKEN:+Áno}${TG_TOKEN:-Nie}\n\n\
Spustiť inštaláciu?" \
  18 60 3>&1 1>&2 2>&3)

if [ "$CONFIRM" != "0" ]; then
  echo "Inštalácia zrušená."
  exit 0
fi

# 7. Run Ansible
echo "Spúšťam SCARLIX OS v12 inštaláciu..."
echo "Toto môže trvať 20-30 minút. Prosím, čakaj."

ansible-playbook /opt/scarlix-installer/ansible/site.yml \
  -i /opt/scarlix-installer/ansible/inventory.yml \
  -e "pc_type=$PC_TYPE" \
  -e "scarlix_ip=$IP" \
  -e "litellm_master_key=$LITELLM_KEY" \
  -e "telegram_bot_token=$TG_TOKEN" \
  -e "telegram_chat_id=$TG_CHAT_ID"

echo ""
echo "========================================"
echo "  ✅ SCARLIX OS v12 — Inštalácia hotová!"
echo "========================================"
echo "  Dashboard: http://$IP:8090"
echo "  SSH: ssh scarlix@$IP"
echo "========================================"
```

---

## STEP 9: Create first-boot.sh

File: `installer/scripts/first-boot.sh`

```bash
#!/usr/bin/env bash
# Runs on first boot after ISO installation
set -euo pipefail

echo "=== SCARLIX OS v12 — First Boot ==="

# Check if already installed
if [ -f /var/lib/scarlix/.installed ]; then
  echo "SCARLIX OS already installed. Skipping."
  exit 0
fi

# Mark as installed
touch /var/lib/scarlix/.installed

# Start all Docker services
echo "Starting Docker services..."
cd /opt/scarlix/ai/sglang && docker compose up -d 2>/dev/null || true
cd /opt/scarlix/ai/ollama && docker compose up -d 2>/dev/null || true
cd /opt/scarlix/ai/litellm && docker compose up -d 2>/dev/null || true

# Download models (background)
echo "Starting model download in background..."
nohup /opt/scarlix/scripts/download-models.sh &

echo "=== First boot complete ==="
echo "Dashboard: http://$(hostname -I | awk '{print $1}'):8090"
```

---

## STEP 10: Create README

File: `installer/README-INSTALLER.md`

```markdown
# SCARLIX OS v12 — ISO Installer

## Build the ISO

### Prerequisites
- Linux machine (Ubuntu 22.04+ recommended)
- 10GB free disk space
- `xorriso`, `wget` installed

### Build

```bash
cd ~/scarlix-iso-builder
bash installer/scripts/build-iso.sh
```

Output: `scarlix-os-v12-installer.iso` (~4GB)

## Write to USB

```bash
# Find USB device
lsblk

# Write (replace /dev/sdX with your USB)
sudo dd if=scarlix-os-v12-installer.iso of=/dev/sdX bs=4M status=progress
sync
```

Or use **balenaEtcher** / **Rufus** on Windows/Mac.

## Install

1. Boot PC from USB
2. Select from GRUB menu:
   - **Main PC** (if this server has NVIDIA GPUs)
   - **HP Agent PC** (if this is the worker server)
   - **Interactive** (for custom settings)
3. Wait 20-30 minutes
4. Reboot
5. Open browser: `http://192.168.1.100:8090`

## Post-Install

### Download AI models (Main PC only):
```bash
bash /opt/scarlix/scripts/download-models.sh
```

### Verify:
```bash
scarlix-mode status
```

## Troubleshooting

If installation fails:
1. Boot from USB again
2. Select "Interactive (TUI Wizard)"
3. Check logs: `/var/log/scarlix-install.log`
```

---

## STEP 11: Build and Test

### Build the ISO:
```bash
cd ~/scarlix-iso-builder
bash installer/scripts/build-iso.sh
```

### Test in VM (optional but recommended):
```bash
# Install QEMU
sudo apt install qemu-system-x86

# Test boot
qemu-system-x86_64 -m 4096 -smp 4 -cdrom scarlix-os-v12-installer.iso -boot d
```

### Test on real hardware:
1. Write ISO to USB
2. Boot Main PC from USB
3. Select "Main PC"
4. Wait for installation
5. Verify dashboard at http://192.168.1.100:8090

---

## SUMMARY FOR CLAUDE

1. **Clone the repo** at https://github.com/MoZoHuJa/OS.git
2. **Create the installer/ directory** with all files above
3. **Run build-iso.sh** to create the ISO
4. **Output**: scarlix-os-v12-installer.iso (~4GB)
5. **User writes to USB** → boots → selects PC type → waits 30 min → done

The ISO contains:
- Ubuntu 24.04.2 LTS base
- GRUB boot menu (Main / HP Agent / Interactive / Manual)
- cloud-init autoinstall configs
- Ansible playbook with 13 roles
- All SCARLIX source files (docker-compose, configs, Go code)
- TUI wizard for interactive install
- First-boot script for auto-start

**IMPORTANT NOTES:**
- The user password hash in cloud-init must be generated: `openssl passwd -6 "desired_password"`
- Model downloads (~40GB) happen post-install, NOT during ISO build
- The ISO should be tested in QEMU before physical deployment
- All secrets (Telegram token, API keys) are collected during install via TUI wizard
