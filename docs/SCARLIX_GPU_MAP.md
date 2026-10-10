# SCARLIX OS v19.2.1 — GPU Map

> Purpose: The dual-NVIDIA GPU architecture, the `nvidia-smi` query patterns used across host scripts, VRAM allocation per `scarlix-mode`, and the `--disable-flashinfer` rationale for Blackwell sm_120. All values cited from files on branch `fix-v19.0.6`.

## Physical GPU layout (target hardware)

| GPU | Model | Architecture | Compute cap | VRAM | Primary role |
|---|---|---|---|---|---|
| GPU 0 | NVIDIA RTX 5060 Ti 16 GB | Blackwell | sm_120 | 16 GB | SGLang (primary inference, AWQ) |
| GPU 1 | NVIDIA RTX 4060 Ti 16 GB | Ada | sm_89 (per `nvidia-smi --query-gpu=compute_cap`) | 16 GB | vLLM (`.experimental` only), Whisper, Ollama-concurrent when needed |

Source: `docs/HARDWARE.md` "Main Server" table; verified at install time by `install.sh` line 220 + 416–417 via `nvidia-smi --query-gpu=compute_cap --format=csv,noheader`.

## nvidia-smi query patterns

### `install.sh` — install-time detection

```bash
# line 220-221 (Phase 1, before driver)
GPU_COMPUTE_CAPS=$(nvidia-smi --query-gpu=compute_cap --format=csv,noheader 2>/dev/null | tr '\n' ';' | sed 's/;$//')

# line 415-417 (Phase 2, after driver) — re-query now that driver is loaded
GPU_COMPUTE_CAPS=$(nvidia-smi --query-gpu=compute_cap --format=csv,noheader 2>/dev/null | tr '\n' ';' | sed 's/;$//')

# line 513 (Phase 3) — Docker GPU smoke test
docker run --rm --gpus all nvidia/cuda:12.8.1-base-ubuntu24.04 nvidia-smi

# line 530 — GPU topology saved for scarlix-doctor
nvidia-smi --query-gpu=index,name,pci.bus_id,compute_cap,memory.total \
           --format=csv,noheader > /var/lib/scarlix/gpu-layout
```

### `scarlix-mode` — `show_vram()` (user-facing VRAM report)

```bash
# files/usr/local/bin/scarlix-mode line 421
nvidia-smi --query-gpu=index,name,memory.total,memory.used,memory.free,utilization.gpu,compute_cap \
           --format=csv,noheader,nounits
```
Renders a per-GPU bar with `█` blocks; flags `⚠ CRITICAL` at ≥90% used.

### `scarlix-host-bridge` — status JSON (every 5s, root)

```bash
# files/usr/local/bin/scarlix-host-bridge line 293
timeout 10s nvidia-smi \
  --query-gpu=index,name,temperature.gpu,utilization.gpu,memory.used,memory.total,power.draw,compute_cap \
  --format=csv,noheader,nounits
```
Wrapped in `timeout 10s` (v18.7.7 P1) so a hung GPU reset/driver hang cannot stall the 5s timer. Parsed by an inline Python3 script into the `gpus` array of `host-status.json`.

### `scarlix-doctor` — diagnostic (line 1219)

```bash
nvidia-smi --query-gpu=index,name,memory.used,memory.free,compute_cap \
           --format=csv,noheader 2>/dev/null || echo "  nvidia-smi not available"
```

## `gpu-layout` file

- **Path:** `/var/lib/scarlix/gpu-layout`
- **Owner/perm:** root:root 644 (implicit from `install.sh` running as root with default umask)
- **Written once:** `install.sh` Phase 3, line 530 (`nvidia-smi --query-gpu=index,name,pci.bus_id,compute_cap,memory.total --format=csv,noheader`)
- **Read by:** `scarlix-doctor` (for topology display)
- **Format:** CSV rows: `0,NVIDIA RTX 5060 Ti,00000000:01:00.0,12.0,16384 MiB`

## `NVIDIA_VISIBLE_DEVICES` assignments per compose

| Engine | `NVIDIA_VISIBLE_DEVICES` | `device_ids` | Driver capabilities | Source |
|---|---|---|---|---|
| SGLang | `0` | `['0']` | `compute,utility` | `ai/sglang/docker-compose.yml` |
| vLLM | `1` | `["1"]` | (default) | `ai/vllm/docker-compose.yml` |
| Whisper | (none, but `device_ids: ['1']`) | `['1']` | (default) | `voice/whisper/docker-compose.yml` |
| BeeLlama | (none — CPU image) | — | — | `ai/llamacpp/docker-compose.yml` |
| Ollama | (none — CPU) | — | — | `ai/ollama/docker-compose.yml` |

## VRAM allocation per `scarlix-mode`

| Mode | GPU 0 | GPU 1 | CPU engines | Notes |
|---|---|---|---|---|
| `ai` | SGLang (AWQ, `--mem-fraction-static 0.85` → ~13.6 GB of 16 GB) | vLLM TP=1, `--gpu-memory-utilization 0.85` (only if `.experimental`) | BeeLlama + Ollama on standby (started only as fallback) | Default working path |
| `turbo` | SGLang | (SGLang + vLLM both up — max throughput) | BeeLlama + Ollama on standby | `dump_vram` first, then full `start_verified_ai` |
| `creative` | SGLang + ComfyUI (`flux1-dev-fp8.safetensors`) + Video/Musicgen | — | BeeLlama | `dump_vram` before start; Sunshine stopped |
| `game` | Sunshine (native host service) | — | — | `dump_vram` first; AI stack stopped |
| `stop` | — | — | — | All AI containers `docker stop sglang vllm beellama ollama-agent litellm` |
| `tv` | — | — | — | TV dashboard via `scarlix-tv-mode.service` (Sunshine-tv container) |
| `offline` | — | — | BeeLlama (q4_0 KV cache) only | No GPU; `--threads ${LLAMACPP_THREADS:-16}` |
| `status` / `vram` / `vram-check` | (read-only — no GPU change) | — | — | Shared flock only (`-s`), no exclusive lock |

Source: `scarlix-mode` `case "$MODE"` branches (lines ~910–1190); `show_vram()`; `dump_vram()` (which calls Ollama `keep_alive:0` on both `:11435` and `:11434` to free RAM/VRAM, then `docker stop sglang vllm beellama ollama-agent litellm`).

## Blackwell sm_120 considerations

- **`--disable-flashinfer`** is hardcoded in the SGLang compose `command:` line. FlashInfer kernels for Blackwell sm_120 were missing in SGLang v0.4.4; the flag is preserved through v0.4.9.post6 for stability (documented in `models.yaml` `.sglang.flashinfer` removed-key block + `ai/sglang/docker-compose.yml` v17.9.7 comment).
- The `flashinfer` key was **removed** from `models.yaml` (v18.8.7 P1-05) because it is NOT wired to compose — having it in YAML gave the false impression it was configurable. To enable FlashInfer, upgrade SGLang image + remove `--disable-flashinfer` from compose.
- `vLLM TP=1` (not TP=2): mixed-GPU architectures (Blackwell sm_120 + Ada sm_89) are not safely compatible with tensor parallelism across devices — TP=1 keeps each engine on its own GPU. Source: `models.yaml` v17.5 comment.
- The Ollama compose comment "v17.7: CPU only, mem_limit 6g" — Ollama is intentionally CPU-only (the GPU is reserved for SGLang/vLLM/Whisper); `OLLAMA_FLASH_ATTENTION=0` is set explicitly.

## Docker GPU smoke test

`install.sh` Phase 3 (lines 508–525) runs a smoke test that the NVIDIA Container Toolkit actually passes the GPU through:

```bash
docker run --rm --gpus all nvidia/cuda:12.8.1-base-ubuntu24.04 nvidia-smi
```

If the pull fails (network), but `docker info` shows the `nvidia` runtime AND host `nvidia-smi` works, the install warns + proceeds. Otherwise it `crit`s (critical fail, abort). This is the regression gate referenced in `docs/SCARLIX_RELEASE_BASELINE.md` regression matrix item #2.

> **Baseline:** This document is part of the v19.0.6 release baseline freeze. Do not modify content without a version bump.
