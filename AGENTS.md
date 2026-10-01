# SCARLIX OS v18.9.3 — Agent Architecture

## Inference Stack

| Tier | Engine | Port | GPU | Use Case |
|------|--------|------|-----|----------|
| 1 | SGLang v0.4.4-cu128 | 30000 | GPU 0 | Agents, RadixAttention, default |
| 2 | vLLM v0.8.0 | 8089 | GPU 1 (TP=1) | High throughput, experimental (--profile experimental) |
| 4 | llama.cpp (official) | 11438 | CPU | Offline fallback, q4_0 KV cache |
| Fallback | Ollama 0.5.4 | 11435 | CPU | Starter model qwen2.5:3b (auto-downloaded) |

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
