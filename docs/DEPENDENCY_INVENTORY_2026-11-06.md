# ScarLiX OS — Dependency Inventory (GENERATED from active compose files)

**Dátum generovania:** 2026-11-06 · **Release:** v19.6.6
**Metóda:** sken všetkých `image:` + `build:` referencií v docker-compose
súboroch repozitára (KPK-19.6.5-P2-05 remediation — "a current dependency
inventory generated from active compose files").
**Drift guard:** `tests/test_dependency_inventory.sh` — každý `image:` ref
z aktívnych compose súborov MUSÍ byť v tomto inventári; nový pin bez
inventárneho riadku FAILne test (a naopak — inventár bez compose ref je
mŕtvy riadok). Toto je dokument **generated-state**, auditná história žije
v `DEPENDENCY_AUDIT_2026-10-21.md` (historický snapshot, banner hore).
**Obmedzenie (čestné):** piny sú evidované tak, ako sú deklarované v
compose súboroch; ich existenciu v registroch overuje runtime smoke
(registry classification, 68 asercií). Digest-piny sú nemenné; tag-piny
sa môžu meniť upstreamom — pin digest pre security-kritické služby je
stále otvorená položka (§3).

---

## 1. Aktívne služby (default inštalácia / control plane)

| Služba | Compose | Image (tag / digest) | Poznámka |
|--------|---------|----------------------|----------|
| ScarliHQ (control plane) | `scarlihq/docker-compose.yml` | `scarlihq:v${SCARLIX_VERSION}` (BUILD) | lokálny build z `scarlihq/Dockerfile` (golang:1.23-alpine → alpine:3.20) |
| SGLang (Tier-1, GPU 0) | `ai/sglang/docker-compose.yml` | `lmsysorg/sglang:v0.4.9.post6-cu128-b200` | tag-pin; Blackwell sm_120 (flashinfer disabled) |
| SMG router | `ai/smg/docker-compose.yml` | `ghcr.io/lightseekorg/smg:v1.4.1.post1-sglang-v0.5.10` | tag-pin |
| Ollama (Tier-4 fallback) | `ai/ollama/docker-compose.yml` | `ollama/ollama:0.40.7@sha256:2e6cc92ffa43b4b1a2e70e315407e00ac97ec125afc71277d73c7efe01c4f31b` | digest-pin (2026-10-21 remediation) |
| llama.cpp / BeeLlama (Tier-3) | `ai/llamacpp/docker-compose.yml` | `ghcr.io/ggml-org/llama.cpp@sha256:6d607629e3dd5e85f45c43d1494648126cb3f93f2122c9cd53f43242c94cde14` | digest-pin |
| LiteLLM | `ai/litellm/docker-compose.yml` | `ghcr.io/berriai/litellm:main-v1.23.9` | tag-pin |
| Hermes agent | `agents/hermes/docker-compose.yml` | `nousresearch/hermes-agent:v0.22.2@sha256:36099926da316742423544e055d4734777105ffe6341873af5459b957d0c2919` | digest-pin |
| CrowdSec | `security/crowdsec/docker-compose.yml` | `crowdsecurity/crowdsec:v1.8.1@sha256:0f2523fa61ef507f15d953045cface490cc880670c62f2755ced17524107f71a` | digest-pin (2026-10-21 remediation) |
| Headscale | `network/headscale/docker-compose.yml` | `headscale/headscale:0.26.0@sha256:7e6439473c299cde05ff9215fe4e2ae9d83361504876fac0eb8e10f55128838d` | digest-pin (2026-10-21 remediation) |

## 2. Voliteľné stacky (operator-enabled)

| Služba | Compose | Image (tag / digest) | Poznámka |
|--------|---------|----------------------|----------|
| vLLM (Tier-2, experimental flag) | `ai/vllm/docker-compose.yml` | `vllm/vllm-openai:v0.8.5` | tag-pin; len s `/etc/scarlix/.experimental` |
| ComfyUI (creative) | `ai/comfyui/docker-compose.yml` | `ghcr.io/ai-dock/comfyui:v2-cuda-12.1.1-base-22.04-v0.2.7` | tag-pin |
| Sunshine | `gaming/docker-compose.yml` | `lizardbyte/sunshine:master-ubuntu-24.04@sha256:5c790c79df9b680a1c57e3454ed829c09f405eafbafe205675d832e4420aba7b` | digest-pin |
| Jellyfin | `gaming/docker-compose.yml` | `jellyfin/jellyfin:12.1` | tag-pin |
| Pumpkin MC | `gaming/docker-compose.yml` | `ghcr.io/pumpkin-mc/pumpkin@sha256:61fd9494f96d58b46b14edd8336c1e5d9aa41f40e973ab3b61474e05e7feef39` | digest-pin |
| PostgreSQL (hp-agent) | `hp-agent/docker-compose.yml` | `postgres:16-alpine` | tag-pin |
| Redis (hp-agent) | `hp-agent/docker-compose.yml` | `redis:7-alpine` | tag-pin |
| Gitea | `hp-agent/docker-compose.yml` | `gitea/gitea:28.2` | tag-pin (2026-10-21 remediation) |
| Coolify | `hp-agent/docker-compose.yml` | `coollabsio/coolify:4.3.1@sha256:c054910edd0b4f56de17bacfbe2a501e06ccdfdb4df2e284ca09d49916c7cf32` | digest-pin (2026-10-21 remediation) |
| Nextcloud | `hp-agent/docker-compose.yml` | `nextcloud:30` | tag-pin (2026-10-21 remediation) |
| PhotoPrism | `hp-agent/docker-compose.yml` | `photoprism/photoprism:260919` | tag-pin |
| n8n | `hp-agent/docker-compose.yml` | `n8nio/n8n:2.42.1` | tag-pin |
| VictoriaMetrics | `monitoring/docker-compose.yml` | `victoriametrics/victoria-metrics:v1.108.0` | tag-pin |
| Grafana | `monitoring/docker-compose.yml` | `grafana/grafana:11.3.0` | tag-pin |
| OTel Collector | `monitoring/docker-compose.yml` | `otel/opentelemetry-collector-contrib:0.161.0` | tag-pin |
| OpenLIT | `monitoring/docker-compose.yml` | `ghcr.io/openlit/openlit:1.5.0` | tag-pin |
| Alertmanager | `monitoring/docker-compose.yml` | `prom/alertmanager:v0.34.1` | tag-pin |
| Piper TTS | `voice/piper/docker-compose.yml` | `rhasspy/wyoming-piper:1.5.0` | tag-pin — compat audit otvorený (stale-tree) |
| OpenWakeWord | `voice/wakeword/docker-compose.yml` | `rhasspy/wyoming-openwakeword:1.10.0` | tag-pin — compat audit otvorený (stale-tree) |
| faster-whisper | `voice/whisper/docker-compose.yml` | `fedirz/faster-whisper-server:sha-307e23f-cuda` | commit-SHA pin |

## 3. Experimental — NEPINNUTÉ (`:latest`, supply-chain residual)

> **Stav: pomenovaný dlh** (KPK v19.6.4 §5.1 / v19.6.5 SK §5.1). Tieto
> stacky NIE SÚ v defaultnej ceste; zapínajú sa explicitne. Politika:
> pin tag/digest ABO archive — najbližší repo-hygiene milestone
> (automatický `scarlix-stale-audit.sh` ich hlás každý týždeň).

| Služba | Compose | Image | Zápis |
|--------|---------|-------|-------|
| browser-mcp | `ai/browser-mcp/docker-compose.yml` | `mcp/playwright-server:latest` | NO_ACCESS pin excuse (v18.8 komentár) |
| freetoken | `ai/freetoken/docker-compose.yml` | `freetoken/serving:latest` | experimental |
| laya | `ai/laya/docker-compose.yml` | `laya/decision-engine:latest` | experimental |
| needle | `ai/needle/docker-compose.yml` | `ghcr.io/cactus-compute/needle:latest` | experimental |

## 4. Lokálne buildy (build kontext = Dockerfile v repu)

| Služba | Compose | Build context | Base image |
|--------|---------|---------------|------------|
| ScarliHQ | `scarlihq/docker-compose.yml` | `./scarlihq` | `golang:1.23-alpine` → `alpine:3.20` |
| Media Tools | `media-tools/docker-compose.yml` | `./media-tools` (v19.6.6: reálny — Dockerfile + server.py) | `python:3.12-alpine3.20` (+ffmpeg, imagemagick) |
| MusicGen | `ai/musicgen/docker-compose.yml` | `./ai/musicgen` | `python:3.11-slim` + requirements.lock |
| Video (Wan2GP) | `ai/video/docker-compose.yml` | `./ai/video` | pinovaný git commit f3f204e50f6e |

## 5. Go moduly (scarlihq)

`gopkg.in/yaml.v3 v3.0.1`, `github.com/gorilla/websocket v1.5.3` —
`go.mod`/`go.sum` pin, overené `go test ./...` (23 balíkov) + `-race`.

---

*Generované z aktívnych compose súborov v19.6.6; drift stráži
`tests/test_dependency_inventory.sh`; história v
`DEPENDENCY_AUDIT_2026-10-21.md` (historický snapshot). "LLM navrhuje,
politika rozhoduje" — inventár je evidencia, nie auto-update.*
