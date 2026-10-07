# SCARLIX OS v19.0.6 — Runtime Map

> Purpose: Frozen reference of every inference + gateway runtime in the v19.0.6 tree — image tag, port, GPU binding, config source, healthcheck, and start command. All values cited from the actual `ai/*/docker-compose.yml` files on branch `fix-v19.0.6`.

## Tier 1 — SGLang (primary, GPU 0)

| Field | Value | Source |
|---|---|---|
| Container name | `sglang` | `ai/sglang/docker-compose.yml` |
| Image | **`lmsysorg/sglang:v0.4.9.post6-cu128-b200`** | compose line 22 (v19.0.3 P0-1 fixed from `ghcr.io/sgl-project/sglang:v0.4.6.post1-cu128` which returned 403/DENIED; verified HTTP 200 via registry API) |
| Host port | `127.0.0.1:30000:30000` | compose |
| GPU | device_ids `['0']`, `NVIDIA_VISIBLE_DEVICES=0` | compose env + deploy.resources |
| `shm_size` | `${SGLANG_SHM_SIZE:-8gb}` | from `.env` (models.yaml `.sglang.shm_size`) |
| Config source | `models.yaml` `.sglang.*` → `.env` → compose env interpolation | `files/usr/local/bin/scarlix-mode::generate_env_file()` |
| Healthcheck | `python -c 'import urllib.request; urllib.request.urlopen("http://localhost:30000/health")'` — interval 30s, timeout 10s, retries 3, start_period 180s | compose |
| Start command | `python -m sglang.launch_server --model-path ${SGLANG_MODEL_PATH:-/models/Qwen3-14B-AWQ} --port 30000 --host 0.0.0.0 --mem-fraction-static ${SGLANG_MEM_FRACTION:-0.85} --context-length ${SGLANG_CONTEXT_LENGTH:-32768} --chunked-prefill-size ${SGLANG_CHUNKED_PREFILL:-4096} --max-running-requests ${SGLANG_MAX_RUNNING:-4} --disable-flashinfer` | compose |
| `--disable-flashinfer` rationale | Blackwell sm_120 kernels missing in v0.4.4; preserved through v0.4.9.post6 for stability | comment in compose + `models.yaml` |

## Tier 2 — vLLM (experimental, GPU 1)

| Field | Value | Source |
|---|---|---|
| Container name | `vllm` | `ai/vllm/docker-compose.yml` |
| Image | `vllm/vllm-openai:v0.8.5` | compose line 10 (v18.9.6 P0-02 bumped from v0.8.0; Qwen3 requires vLLM ≥ 0.8.5) |
| Host port | `127.0.0.1:8089:8000` (host:container) | compose |
| GPU | device_ids `["1"]`, `NVIDIA_VISIBLE_DEVICES=1`, TP=1 (mixed-GPU safe — RTX 5060 Ti Blackwell + RTX 4060 Ti Ada) | compose + `models.yaml` |
| Gating | Only starts if `/etc/scarlix/.experimental` exists | `scarlix-mode` line ~800 |
| Config source | `models.yaml` `.vllm.*` → `.env` | `scarlix-mode::generate_env_file()` |
| Healthcheck | `python -c 'import urllib.request; urllib.request.urlopen("http://localhost:8000/health")'` — interval 30s, timeout 10s, retries 5, start_period 180s | compose |
| Start command | `--model ${VLLM_MODEL_PATH:-/models/Qwen3-14B-AWQ} --tensor-parallel-size ${VLLM_TENSOR_PARALLEL:-1} --gpu-memory-utilization ${VLLM_GPU_UTIL:-0.85} --max-model-len ${VLLM_MAX_MODEL_LEN:-32768} --trust-remote-code --host 0.0.0.0 --port 8000` | compose |

## Tier 3 — BeeLlama / llama.cpp (CPU offline fallback)

| Field | Value | Source |
|---|---|---|
| Container name | `beellama` | `ai/llamacpp/docker-compose.yml` |
| Image | `ghcr.io/ggml-org/llama.cpp@sha256:6d607629e3dd5e85f45c43d1494648126cb3f93f2122c9cd53f43242c94cde14` (CPU `server` image, pinned by digest v18.8.2 P1) | compose |
| Host port | `127.0.0.1:11438:8080` | compose |
| GPU | none (CPU image; intentionally NOT `server-cuda`) | comment in compose |
| Config source | `models.yaml` `.beellama.*` → `.env` (`LLAMACPP_*` vars) | `scarlix-mode` |
| Healthcheck | `wget -qO- http://localhost:8080/health` — interval 30s, timeout 10s, retries 3, start_period 60s | compose |
| Start command | `--model ${LLAMACPP_MODEL_PATH:-/models/Qwen3-14B-Q4_K_M.gguf} --ctx-size ${LLAMACPP_CONTEXT_SIZE:-32768} --threads ${LLAMACPP_THREADS:-16} --host 0.0.0.0 --port 8080 --cache-type-k ${LLAMACPP_CACHE_TYPE_K:-q4_0} --cache-type-v ${LLAMACPP_CACHE_TYPE_V:-q4_0}` | compose |

## Tier 4 — Ollama (CPU tertiary fallback)

| Field | Value | Source |
|---|---|---|
| Container name | `ollama-agent` (NOT `ollama-main` — removed v19.0.2 P0-6) | `ai/ollama/docker-compose.yml` |
| Image | `ollama/ollama:0.5.4` | compose |
| Host port | `127.0.0.1:11435:11434` | compose |
| GPU | none (CPU fallback tier — NOT GPU despite the original v17.7 comment; comment says "not GPU, mem_limit 6g") | compose |
| `mem_limit` | `6g` | compose |
| Config source | `models.yaml` `.ollama.*` → `.env` | `scarlix-mode` |
| Healthcheck | `ollama list >/dev/null 2>&1` — interval 30s, timeout 10s, retries 5, start_period 30s | compose |
| Starter model | `qwen2.5:3b` (auto-pulled by `install.sh` Phase 5 + `download-models.sh`) | `models.yaml` `.ollama.model` |
| Env vars | `OLLAMA_HOST=0.0.0.0:11434`, `OLLAMA_NUM_PARALLEL=${OLLAMA_NUM_PARALLEL:-2}`, `OLLAMA_KEEP_ALIVE=${OLLAMA_KEEP_ALIVE:-15m}`, `OLLAMA_FLASH_ATTENTION=0` | compose |

## Gateway — LiteLLM (`:4001`, 3-tier)

| Field | Value | Source |
|---|---|---|
| Container name | `litellm` | `ai/litellm/docker-compose.yml` |
| Image | `ghcr.io/berriai/litellm:main-v1.21.7` (v19.0.1 P1: bumped from main-v1.16.19) | compose |
| Host port | `127.0.0.1:4001:4000` (host:container) | compose |
| GPU | none | compose |
| Config source | `ai/litellm/config.yaml` (regenerated from `models.yaml` by `/usr/local/bin/generate-litellm-config.sh` on every `scarlix-mode ai` start) | compose + `scarlix-mode::generate_env_file()` |
| Healthcheck | `curl -sf http://localhost:4000/health/liveliness` — interval 15s, timeout 5s, retries 5, start_period 30s. `/health/liveliness` is no-auth (v18.8.6 P0: `/health` returns 401 when master_key is set, would mark container unhealthy) | compose |
| Start command | `--config /app/config.yaml --port 4000` | compose |
| Fallback chain | `scarlix-default` (SGLang) → `scarlix-ollama` → `scarlix-beellama` (vLLM excluded — experimental) | `ai/litellm/config.yaml` |
| Auth | `LITELLM_MASTER_KEY` (Bearer) | `ai/litellm/config.yaml` `general_settings.master_key` |

## Gateway — SMG (`:4000`, latency-aware)

| Field | Value | Source |
|---|---|---|
| Container name | `smg` | `ai/smg/docker-compose.yml` |
| Image | `ghcr.io/lightseekorg/smg:v1.4.1.post1-sglang-v0.5.10` | compose |
| Host port | `127.0.0.1:4000:4000` | compose |
| Config source | `ai/smg/config.yaml` (mounted ro) | compose |
| Healthcheck | `curl -sf http://localhost:4000/health` — interval 30s, timeout 5s, retries 3 | compose |
| Routing | `strategy: latency-aware`, `fallback_chain: [sglang-main, ollama-agent, beellama-cpu]`, `retry_count: 2`, `timeout_ms: 30000` | `ai/smg/config.yaml` |
| Auth | `SMG_MASTER_KEY` | `ai/smg/config.yaml` |
| Rate limits | per-profile: zmor 100 rpm / hugo 20 / xox 10 / mon 30 | `ai/smg/config.yaml` |
| **Port drift note** | `docs/ARCHITECTURE.md` + `AGENTS.md` reference SMG as `:4002`; the actual compose binds to host port `4000` (collides with LiteLLM's container port but NOT its host port `4001`). Doc drift, not runtime bug — flag for main agent. | — |

## Inference path failover (code-truth, `scarlix-mode::start_verified_ai`)

```
start_verified_ai():
  1. SGLang up -d  → wait_for_healthy(300s, http://127.0.0.1:30000/health)
       fail → docker stop sglang (free VRAM), fall to (2)
  2. if EXPERIMENTAL: vLLM up -d → wait_for_healthy(300s, http://127.0.0.1:8089/v1/models)
       fail → docker stop vllm, fall to (3)
  3. if (1) AND (2) both failed: BeeLlama up -d → wait_for_healthy(120s, http://127.0.0.1:11438/health)
       fail → fall to (4)
  4. if (1) AND (2) AND (3) all failed: Ollama up -d → wait_for_healthy(60s, http://127.0.0.1:11435/api/tags)
       fail → return 1 (mode switch FAILED, state=failed)
  Also: LiteLLM gateway up -d → wait_for_healthy(60s, http://127.0.0.1:4001/health/liveliness)
        (non-blocking: LiteLLM failure does NOT fail AI mode)
```

## Image tag freeze (do NOT bump in v19.0.x without explicit decision)

| Component | Image | Source |
|---|---|---|
| SGLang | `lmsysorg/sglang:v0.4.9.post6-cu128-b200` | `ai/sglang/docker-compose.yml:22` |
| vLLM | `vllm/vllm-openai:v0.8.5` | `ai/vllm/docker-compose.yml:10` |
| BeeLlama | `ghcr.io/ggml-org/llama.cpp@sha256:6d607629e3dd5e85f45c43d1494648126cb3f93f2122c9cd53f43242c94cde14` | `ai/llamacpp/docker-compose.yml` |
| Ollama | `ollama/ollama:0.5.4` | `ai/ollama/docker-compose.yml` |
| LiteLLM | `ghcr.io/berriai/litellm:main-v1.21.7` | `ai/litellm/docker-compose.yml` |
| SMG | `ghcr.io/lightseekorg/smg:v1.4.1.post1-sglang-v0.5.10` | `ai/smg/docker-compose.yml` |
| Whisper | `fedirz/faster-whisper-server:sha-307e23f-cuda` | `voice/whisper/docker-compose.yml` |
| OpenLit | `ghcr.io/openlit/openlit:1.5.0` | `monitoring/docker-compose.yml:39` |

> **Baseline:** This document is part of the v19.0.6 release baseline freeze. Do not modify content without a version bump.
