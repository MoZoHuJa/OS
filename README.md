# SCARLIX OS v17.5 — EndeavourOS Edition (Working AI Path)

> Sovereign home OS for AI cloud, coding, gaming, creative, and family entertainment.
> **Working AI Path**: model-aware, fail-hard, healthcheck + fallback.
> **Verified**: SGLang (GPU0) + vLLM (GPU1, TP=1) + BeeLlama (CPU) + Ollama (fallback).

**Version:** v17.5.0 | **Base:** EndeavourOS (Arch) | **License:** MIT

## 🚀 Install (NO ISO)

### Primary (safe — review first) ⭐
```bash
git clone https://github.com/MoZoHuJa/OS.git ~/scarlix-os
cd ~/scarlix-os
nano install.sh   # review
bash install.sh
```

### Post-install (3 steps):
```bash
# 1. Reboot (activate NVIDIA driver)
sudo reboot

# 2. Download models (50-150GB, takes hours)
bash /etc/systemd/system/download-models.sh

# 3. Start AI inference
scarlix-mode ai
```

---

## 🆕 What's New in v17.5 (vs v17.4)

**10 P0 fixes — first truly working AI path.**

| # | Fix | v17.4 Problem | v17.5 Solution |
|---|-----|---------------|-----------------|
| Q1a | **AI not started in install.sh** | Phase 5 started containers without models → crash | Phase 5 downloads starter model (qwen2.5:3b), does NOT start AI. User runs `scarlix-mode ai` after `download-models.sh` |
| Q2a | **models.yaml = single source of truth** | Compose hardcoded model path → mismatch if yaml edited | Compose uses `${SGLANG_MODEL_PATH}` env var. scarlix-mode parses yaml → exports → docker compose up |
| Q3a | **.experimental flag by wizard** | vLLM `--profile experimental` but no flag → disappears on reboot | Wizard creates `.experimental` for 2+ GPU. vLLM starts without profile gate. |
| Q4a | **SGLang cu128 image + Blackwell support** | cu124 image didn't support RTX 5060 Ti (sm_120) | cu128 image default. `--disable-flashinfer` auto-applied on Blackwell. |
| Q5a | **nvidia-container-toolkit = crit** | Fail was non-crit → Docker can't see GPU but .installed touched | Fail → `crit` → exit 1, no .installed |
| Q6a | **scarlihq/ copied + first-boot removed** | Dashboard 404, zombie first-boot service | scarlihq copied in Phase 4. first-boot.sh + scarlix-first-boot.service removed. |
| Q7a | **Starter model auto-downloaded** | Ollama fallback had no model | install.sh pulls qwen2.5:3b (~2GB). Ollama fallback works immediately. |
| Q8a | **laya/freetoken compose fixed + ISO removed** | Network mismatch, zombie ISO profile | All compose → scarlix-net. scarlix/ ISO profile + build-iso.sh + qemu-boot.sh removed. |
| Q9a | **Version-aware checkpoint** | Didn't detect nvidia driver update → stale DKMS | Checkpoint stores nvidia-open version. Re-runs Phase 2 if version changes. |
| Q10a | **Fail-hard + always healthcheck** | scarlix-mode ai "already ai" skipped healthcheck. Copy failures non-crit. | All core file copies → crit. scarlix-mode ai ALWAYS healthchecks + restarts unhealthy. |

### 🏗️ Verified AI Path (v17.5)

```
GPU 0: RTX 5060 Ti 16GB (Blackwell sm_120)
└── Tier-1: SGLang (agents, RadixAttention, cu128 image, flashinfer disabled)

GPU 1: RTX 4060 Ti 16GB (Ada sm_89)
└── Tier-2: vLLM (TP=1, no LoRA, separate model — mixed arch safe)

CPU:
└── Tier-4: llama.cpp (official image, q4_0 KV cache, 32k context)

Fallback (always ready):
└── Ollama + qwen2.5:3b (starter model auto-downloaded)
```

### 🔄 Fallback Chain (real, not theoretical)

1. `scarlix-mode ai` → starts SGLang + vLLM + BeeLlama + Ollama
2. If SGLang crashes → vLLM takes over (different GPU)
3. If vLLM crashes → BeeLlama (CPU, offline)
4. If BeeLlama crashes → Ollama (qwen2.5:3b starter, always ready)
5. `scarlix-mode ai` re-run → healthchecks + restarts unhealthy containers

---

## 🎮 GPU Modes

```bash
scarlix-mode ai        # Verified path + healthcheck (always re-checks)
scarlix-mode turbo     # Same as ai (max throughput)
scarlix-mode offline   # BeeLlama.cpp CPU (q4_0 KV, 32k context)
scarlix-mode game      # Native Steam/Sunshine
scarlix-mode creative  # ComfyUI + Video + Music
scarlix-mode tv        # Docker Sunshine
scarlix-mode vram      # VRAM health (shows compute_cap per GPU)
scarlix-mode status    # System summary (shows models + config)
```

---

## 🧪 Model-Agnostic (Single Source of Truth)

Edit `/etc/scarlix/models.yaml`:
```yaml
sglang:
  model_path: "/models/Qwen3-14B-Instruct-AWQ"
  # Change to ANY HuggingFace model:
  # model_path: "/models/Meta-Llama-3.1-8B-Instruct"
```
Then:
```bash
bash /etc/systemd/system/download-models.sh   # download new model
scarlix-mode ai                                 # restart with new model
```
`scarlix-mode` parses `models.yaml` → exports `SGLANG_MODEL_PATH` → compose uses it. **One source of truth.**

---

## 🔧 NVIDIA Rollback

```bash
# Option 1: rollback driver (downgrade via yay)
sudo downgrade nvidia-open

# Option 2: boot LTS kernel (nvidia-open-lts + linux-lts installed atomically)
sudo grub-set-default 1
sudo reboot
```

---

## 📁 File Layout (v17.5 — ISO removed)

```
OS/
├── install.sh                              # v17.5: Bootstrap (5 phases, fail-hard, version-aware)
├── README.md
├── AGENTS.md
├── models.yaml                             # Single source of truth (env vars → compose)
├── packages.x86_64                         # Package list (was scarlix/packages.x86_64)
├── files/                                  # System files (was scarlix/airootfs/)
│   ├── usr/local/bin/
│   │   ├── scarlix-wizard                  # Creates .experimental for 2+ GPU
│   │   ├── scarlix-mode                    # Healthcheck + env var parsing
│   │   └── model-manager.sh
│   └── etc/
│       ├── systemd/
│       │   ├── zram-generator.conf
│       │   └── system/
│       │       ├── model-manager.{service,timer}
│       │       ├── download-models.sh      # User runs manually
│       │       └── generate-env.sh
│       └── pacman.d/hooks/
│           └── scarlix-docker-backup.*
├── ai/                                     # Docker stacks (all scarlix-net)
│   ├── sglang/docker-compose.yml          # cu128, env vars, flashinfer off on Blackwell
│   ├── vllm/docker-compose.yml            # TP=1, no LoRA, no profile gate
│   ├── llamacpp/docker-compose.yml        # Official llama.cpp, q4_0 KV
│   ├── ollama/docker-compose.yml          # Fallback (starter qwen2.5:3b)
│   └── ...
├── scarlihq/                               # Dashboard (copied to /opt/scarlix/ in Phase 4)
└── ...
```

**Removed in v17.5:** scarlix/ ISO profile, installer/scripts/build-iso.sh, tests/qemu-boot.sh, first-boot.sh, scarlix-first-boot.service (all zombie code).

---

## 📜 Version History

| Version | Date | Key Changes |
|---------|------|-------------|
| **v17.5** | 2026-10 | **Working AI Path. 10 P0 fixes: no AI start without models, models.yaml single source, .experimental flag, cu128 Blackwell, toolkit crit, scarlihq copy, starter model, ISO removed, version checkpoint, always healthcheck.** |
| v17.4 | 2026-10 | Unified bootstrap, fail-hard, TP=1 (mixed GPU). 19 fixes. |
| v17.3 | 2026-10 | Bootstrap installer (install.sh). (Broken — curl\|bash, no models) |
| v17.2.1 | 2026-10 | Auto 5-tier for 2+ GPU. |
| v17.2 | 2026-10 | Minimal Working Baseline. 8 P0 fixes. |
| v17.1 | 2026-09 | 5-tier inference. (Over-engineered) |
| v17.0 | 2026-09 | Garuda → EndeavourOS re-base. |
| v16.5 | 2026-08 | 10 review fixes. |
| v16.1 | 2026-07 | Garuda Linux. |
| v15 | 2026-06 | Model-Agnostic. |

---

## ⚠️ Known Limitations

- **No CI/CD**: install.sh manually tested
- **No real HW test**: QEMU doesn't test NVIDIA/CUDA
- **Single maintainer**: One person maintaining full stack
- **SGLang on Arch**: Officially Ubuntu-only, AUR has packaging gaps
- **vLLM TP=1**: Separate model per GPU (less efficient than TP=2 but mixed-arch safe)

## 🗺️ Roadmap

- **v17.6**: AgentVerse merge (944-app store, capabilities)
- **v18.0**: Incus dev workspaces, ScarliHQ Rust refactor
