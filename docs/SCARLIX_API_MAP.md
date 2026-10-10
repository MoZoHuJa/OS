# SCARLIX OS v19.2.1 — API Map

> Purpose: Every HTTP / WebSocket / JSON-RPC endpoint exposed by the v19.0.6 stack — ScarliHQ REST + WS + MCP, plus the four inference backends and the two gateways. Method, path, auth, purpose, and source line. All values cited from files on branch `fix-v19.0.6`.

## ScarliHQ REST API (`127.0.0.1:8090`)

All `/api/*` routes are registered in `scarlihq/internal/api/rest.go::RegisterRoutes()` and wrapped by the `auth` middleware (Bearer token only — `crypto/subtle.ConstantTimeCompare` against `SCARLIHQ_TOKEN`).

| Method | Path | Auth | Body / Query | Purpose | Source |
|---|---|---|---|---|---|
| GET | `/` | none | — | Serve embedded dashboard HTML (`go:embed frontend/dist/index.html`). The HTML is a shell — it cannot act without a token (auth gate). | `cmd/scarlihq/main.go:42` |
| GET | `/api/health` | Bearer | — | `{"status":"ok","version":"<ver>"}` (version from `-ldflags -X main.Version`) | `rest.go::health` |
| GET | `/api/gpu` | Bearer | — | `{"gpus":[...],"timestamp":"..."}` or `{"gpus":[],"stale":true,"message":"host-status.json not found..."}` | `rest.go::gpuStatus` |
| GET | `/api/containers` | Bearer | — | `{"containers":[{name,status,ports}],"timestamp":"..."}` | `rest.go::listContainers` |
| GET | `/api/mode` | Bearer | — | `{"mode":"<current>","status":"ok","requested_mode":"...","transition_state":"none\|applied\|retrying\|failed\|rejected","retry_count":N,"last_error":"...","last_timestamp":"..."}` | `rest.go::modeHandler` |
| POST | `/api/mode?set=<mode>` | Bearer | `?set=ai\|stop\|game\|creative\|turbo\|offline\|tv` OR JSON body `{"mode":"ai"}` (max 4096 bytes) | Reserve mode switch via `Mode.Request()` (checks transition state, then atomic `O_EXCL` write to `desired-mode`). Returns 202 `{"mode":"...","status":"accepted","message":"mode switch requested — host bridge will apply within 5s"}`. Returns 409 if a transition is pending/in-progress, 400 if invalid mode, 503 on FS error. | `rest.go::modeHandler` |
| GET | `/api/profiles` | Bearer | — | `[{name,display_name,role,...}]` from `/etc/scarlix/profiles/*.yaml` | `rest.go::listProfiles` |
| GET | `/api/status` | Bearer | — | Full `host-status.json` (timestamp, version, scarlix_version, mode, experimental, mode_transition{...}, gpus[], containers[], disk{models_free_mb, models_total_mb}) | `rest.go::fullStatus` |
| POST | `/api/ws-ticket` | Bearer | — | `{"ticket":"<64 hex>","expires_in":30}`. Issues a 30s single-use WS ticket. Returns 503 if `maxWSTickets=1024` reached (rate limit), 500 on RNG failure (`ErrTicketRNG` — fail-closed, never issues predictable all-zeros ticket). | `rest.go::issueWSTicket` |

### Auth middleware behavior

- If `SCARLIHQ_TOKEN` env is unset → 503 `"SCARLIHQ_TOKEN not configured on server"`.
- If `Authorization: Bearer <token>` header is missing or doesn't start with `Bearer ` → 401 `"invalid or missing token (use Authorization: Bearer)"`.
- `crypto/subtle.ConstantTimeCompare([]byte(token), []byte(h.authToken)) != 1` → 401 `"invalid or missing token"`.
- No `?token=` URL fallback (v18.6 P2 removed — was leaking tokens into logs/referrer/proxy).

## ScarliHQ WebSocket (`ws://127.0.0.1:8090/ws`)

| Method | Path | Auth | Purpose | Source |
|---|---|---|---|---|
| GET (Upgrade) | `/ws?ticket=<one-time>` | WS ticket (30s single-use, obtained via POST `/api/ws-ticket`) | Real-time `host-status.json` push every 2s. Max 16 concurrent WS clients (`wsSlots = make(chan struct{}, 16)`, v18.5 P1). `WriteTimeout` 10s per message (v18.5 P1). | `scarlihq/internal/webui/ws.go::RegisterWS` |

### WS ticket lifecycle (the privilege boundary for sockets)

1. Dashboard JS `fetch('/api/ws-ticket', {headers:{Authorization:'Bearer '+TOKEN}, method:'POST'})` → `{ticket, expires_in:30}`.
2. Dashboard opens `new WebSocket('ws://'+host+'/ws?ticket='+encodeURIComponent(ticket))`.
3. Server calls `api.ReserveWSTicket(ticket)` — atomically deletes the ticket under `wsTicketsMu` lock. If missing/expired → 401 `"invalid or expired ticket"`. Single-use replay-resistance guarantee: only the first concurrent caller sees `true`.
4. If `wsSlots` channel is empty → 503 `"too many WebSocket connections"` (ticket is NOT re-added — consumed for good, v18.7.6 P0).
5. If `upgrader.Upgrade()` fails → ticket stays consumed (no `ReleaseWSTicket` — v18.7.6 P0: was re-adding with fresh 30s TTL → attacker-renewable lifetime).
6. Origin check via `checkOrigin()`: `net.IPNet.Contains` against `127.0.0.1/32`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`. Empty Origin allowed (non-browser clients like curl, still need ticket). IPv6 `[::1]` correctly handled (v18.7.5 P1).
7. `conn.SetWriteDeadline(time.Now().Add(10 * time.Second))` before each `WriteMessage` — prevents goroutine leak from blocked clients.

## ScarliHQ MCP / JSON-RPC 2.0 (`127.0.0.1:8090/rpc` and `/mcp`)

| Method | Path | Auth | Body | Purpose | Source |
|---|---|---|---|---|---|
| GET | `/mcp` | Bearer | — | `{"protocol":"jsonrpc/2.0","server":"scarlihq","version":"<ver>","endpoint":"/rpc","note":"HTTP JSON-RPC 2.0 endpoint (initialize, tools/list, tools/call)"}` (informational — v17.9.8 P0: was fake static MCP) | `scarlihq/internal/mcp/server.go::handleMCPInfo` |
| POST | `/rpc` | Bearer | JSON-RPC 2.0 request (max 64KB body, v18.7 P1) | Real JSON-RPC 2.0 server: `initialize`, `tools/list`, `tools/call`. Returns `{jsonrpc:"2.0", id, result/error:{code,message}}`. | `server.go::handleRPC` |

Note: This is an HTTP transport for MCP, not the stdio transport. A full stdio MCP server would be a separate binary.

## Inference backend endpoints (all bound to `127.0.0.1`)

### SGLang (`127.0.0.1:30000`)

| Method | Path | Auth | Purpose | Source |
|---|---|---|---|---|
| GET | `/health` | none | Liveness + ready (returns 200 once model loaded). Used by `wait_for_healthy("sglang", 300, http://127.0.0.1:30000/health)` in `scarlix-mode::start_verified_ai`. | `ai/sglang/docker-compose.yml` healthcheck |
| POST | `/v1/chat/completions` | none (SGLang has no built-in auth) | OpenAI-compatible inference. Model ID = `Qwen3-14B-AWQ` (basename of `--model-path`). | SGLang docs |
| GET | `/v1/models` | none | List loaded models. | SGLang docs |

### vLLM (`127.0.0.1:8089` → container `:8000`)

| Method | Path | Auth | Purpose | Source |
|---|---|---|---|---|
| GET | `/health` | none | Used by Docker healthcheck (container-internal `localhost:8000/health`). | `ai/vllm/docker-compose.yml` |
| GET | `/v1/models` | none | Used by `wait_for_healthy("vllm", 300, http://127.0.0.1:8089/v1/models)` in `scarlix-mode`. | vLLM docs |
| POST | `/v1/chat/completions` | none | OpenAI-compatible. Started with `--trust-remote-code`. | compose `command:` |

### Ollama (`127.0.0.1:11435` → container `:11434`)

| Method | Path | Auth | Purpose | Source |
|---|---|---|---|---|
| GET | `/api/tags` | none | List installed models. Used by `wait_for_healthy("ollama-agent", 60, http://127.0.0.1:11435/api/tags)`. | `ai/ollama/docker-compose.yml` healthcheck |
| POST | `/api/generate` | none | Generate completion. Used by `scarlix-mode::dump_vram` to send `keep_alive:0` (unload model from RAM). Tries `:11435` first, falls back to `:11434` (v18.8.5 P0). `--max-time 10` (v18.8.6 P1). | Ollama API |
| POST | `/api/chat` | none | Chat completion (OpenAI-style). | Ollama API |
| GET | `/v1/models` | none | OpenAI-compatible model list (Ollama serves both APIs). | Ollama API |

### BeeLlama / llama.cpp (`127.0.0.1:11438` → container `:8080`)

| Method | Path | Auth | Purpose | Source |
|---|---|---|---|---|
| GET | `/health` | none | Used by `wait_for_healthy("beellama", 120, http://127.0.0.1:11438/health)`. | `ai/llamacpp/docker-compose.yml` healthcheck |
| GET | `/v1/models` | none | Returns the GGUF filename as the model ID. | llama.cpp docs |
| POST | `/v1/chat/completions` | none | OpenAI-compatible. `--cache-type-k q4_0 --cache-type-v q4_0` (CPU offline). | compose `command:` |

## Gateway endpoints

### LiteLLM (`127.0.0.1:4001` → container `:4000`)

| Method | Path | Auth | Purpose | Source |
|---|---|---|---|---|
| GET | `/health/liveliness` | **NONE** (v18.8.6 P0: `/health` returns 401 when master_key is set → would mark container unhealthy → recreates every cycle) | Liveness probe for Docker healthcheck. Used by `wait_for_healthy("litellm", 60, http://127.0.0.1:4001/health/liveliness)` in `scarlix-mode::start_verified_ai`. | `ai/litellm/docker-compose.yml` healthcheck |
| GET | `/health` | Bearer `LITELLM_MASTER_KEY` | Full health (backend connectivity). | LiteLLM docs |
| POST | `/v1/chat/completions` | Bearer `LITELLM_MASTER_KEY` | OpenAI-compatible. Body `{"model":"scarlix-default",...}` triggers the 3-tier fallback chain (scarlix-default → scarlix-ollama → scarlix-beellama). | `ai/litellm/config.yaml` |

### SMG (`127.0.0.1:4000`)

| Method | Path | Auth | Purpose | Source |
|---|---|---|---|---|
| GET | `/health` | none (Docker healthcheck) | Liveness probe — `curl -sf http://localhost:4000/health` interval 30s. | `ai/smg/docker-compose.yml` healthcheck |
| POST | `/v1/chat/completions` | Bearer `SMG_MASTER_KEY` | Latency-aware routing. `fallback_chain: [sglang-main, ollama-agent, beellama-cpu]`, `retry_count: 2`, `timeout_ms: 30000`. Per-profile rate limits: zmor 100 rpm, hugo 20, xox 10, mon 30. | `ai/smg/config.yaml` |

> **Note:** `docs/ARCHITECTURE.md` and `AGENTS.md` reference SMG on `:4002`. The actual compose binds to host port `4000`. Doc drift — flag for main agent.

## Endpoint port map (quick reference)

| Port | Service | Bound to | Auth |
|---|---|---|---|
| 8090 | ScarliHQ (REST + WS + MCP) | 127.0.0.1 | Bearer / WS ticket |
| 30000 | SGLang | 127.0.0.1 | none |
| 8089 | vLLM (host) / 8000 (container) | 127.0.0.1 | none |
| 11435 | Ollama (host) / 11434 (container) | 127.0.0.1 | none |
| 11438 | BeeLlama (host) / 8080 (container) | 127.0.0.1 | none |
| 4001 | LiteLLM (host) / 4000 (container) | 127.0.0.1 | Bearer (LITELLM_MASTER_KEY) |
| 4000 | SMG | 127.0.0.1 | Bearer (SMG_MASTER_KEY) |
| 8002 | Whisper | 127.0.0.1 | (upstream default) |

> **Baseline:** This document is part of the v19.0.6 release baseline freeze. Do not modify content without a version bump.
