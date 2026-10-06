# SCARLIX OS v19.1.6 — Agent Architecture

## Inference Stack

| Tier | Engine | Port | GPU | Use Case |
|------|--------|------|-----|----------|
| 1 | SGLang v0.4.9.post6-cu128-b200 (`--disable-flashinfer`) | 30000 | GPU 0 | Agents, RadixAttention, default (Qwen3-14B-AWQ) |
| 2 | vLLM v0.8.5 | 8089 | GPU 1 (TP=1) | High throughput, experimental (`.experimental` flag only) |
| 3 | BeeLlama (llama.cpp, CPU) | 11438 | CPU | Offline fallback, GGUF Q4_K_M |
| Fallback | Ollama | 11435 | CPU | Starter model qwen2.5:3b (auto-downloaded) |

> **v19.0.2:** SGLang bumped v0.4.4 → v0.4.6.post1 (Qwen3 arch support) and vLLM
> v0.8.0 → v0.8.5 in v18.9.6/v0.8.5. BeeLlama is the llama.cpp CPU tier (renamed
> from `llamacpp` in v18.5). Service names on `scarlix-net`: `sglang`, `vllm`,
> `beellama`, `ollama-agent` — NOT `ollama-main`/`llamacpp` (those were removed
> in v19.0.2 P0-6 as dead config references).

All inference endpoints bound to 127.0.0.1 (localhost only).

## Agent Hierarchy

```
User (dashboard :8090, token auth)
└── ScarliHQ (Go binary, REST API + WebSocket + MCP)
    ├── scarlix-mode (mode switcher: ai/stop/game/creative/turbo/offline/tv)
    ├── scarlix-host-bridge (root timer, 5s interval: nvidia-smi + docker ps + scarlix-mode)
    ├── scarlix-bridge-reader (Go binary, atomic O_NOFOLLOW file reader)
    └── scarlix-doctor (self-diagnostic with --fix mode)
```

## Security Architecture (v18.0+)

- **Host-Bridge**: ScarliHQ container has NO docker.sock, NO nvidia runtime, NO scarlix-mode mount
- **bridge-input/** (UID 65532, mode 700): ScarliHQ writes desired-mode here
- **bridge-state/** (root:root, mode 700): Host bridge writes retry/last-transition here
- **scarlix-bridge-reader**: Go binary with O_NOFOLLOW + fstat + read(fd) — no TOCTOU
- **WS ticket**: 30s single-use (ReserveWSTicket atomic delete, no Release on failure)
- **REST/MCP**: Bearer token only (no ?token= in URL)
- **load_env_safe()**: KEY=VALUE parser with `^[A-Z_][A-Z0-9_]*$` whitelist (no shell evaluation)

## Profiles

| Profile | File | Description |
|---------|------|-------------|
| zmor | profiles/zmor.yaml | Admin, unlimited tokens |
| hugo | profiles/hugo.yaml | Son, 100k tokens |
| xox | profiles/xox.yaml | Daughter, 10k, kids-safe |
| mon | profiles/mon.yaml | Wife, 50k tokens |
