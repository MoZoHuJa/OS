# SCARLIX OS v17.1 — Agent Architecture

## 5-Tier Inference Stack

| Tier | Engine | Port | GPU | Use Case |
|------|--------|------|-----|----------|
| 1 | SGLang | 30000 | GPU 0 | Agents, RadixAttention, default |
| 2 | vLLM | 8089 | GPU 0+1 (TP=2) | High throughput, Multi-LoRA, batch [NEW] |
| 3a | Ollama Main | 11434 | GPU 0 | GGUF concurrent (4 parallel) |
| 3b | Ollama Agent | 11435 | GPU 1 | Persistent agent inference |
| 4 | BeeLlama.cpp | 11438 | CPU | Offline, KVarN long context [NEW] |
| 5 | FreeToken | 8090 | GPU 0+1 | MoE (experimental --profile moe) [NEW] |
| S1 | Laya | 11440 | GPU 1 | System-1 router, 33ms decisions [NEW] |
| MCP | Browser MCP | 8095 | — | Playwright browser automation [NEW] |

## Agent Hierarchy

```
Hermes (CEO, Python, port 7999)
├── Laya (System-1 Triage) — 33ms yes/no/choice/score
│   └── If safe → execute directly
│   └── If risky → request Telegram HITL approval
├── omp / oh-my-pi (Coding Manager, LSP+DAP)
│   ├── Frontend specialist
│   ├── Backend specialist
│   ├── DevOps specialist
│   └── Security specialist
├── Browser MCP (web automation)
│   └── Scraping, form filling, screenshots
└── ScarliHQ (dashboard, guard, memory, profiles)
```

## Hermes ↔ omp Delegation Protocol

1. Hermes receives task (Telegram / cron / voice)
2. Laya triages: is this safe? (33ms decision)
   - Safe → Hermes delegates to omp
   - Risky → Telegram HITL approval request
3. If coding task → Hermes calls omp via MCP:
   ```
   omp --task "refactor auth module" --lsp --backend vllm
   ```
4. omp uses LSP (go-to-def, find-refs, rename) + DAP (debug, breakpoint)
5. omp reports back to Hermes with diff + test results
6. Hermes logs to Buzz (Nostr audit trail) + Kanban board (ScarliHQ)

## Backend Selection

```bash
# Use SGLang (default — RadixAttention for agent loops)
scarlix-mode ai --backend sglang

# Use vLLM (high throughput, Multi-LoRA, batch)
scarlix-mode ai --backend vllm

# Turbo mode = both SGLang + vLLM + Ollama
scarlix-mode turbo
```

## Browser Automation (Playwright MCP)

Hermes can ask: "Go to Alza, find cheapest RTX 5090, screenshot to Telegram"

```python
# Hermes calls Browser MCP:
mcp.browser.navigate("https://www.alza.sk")
mcp.browser.fill("search", "RTX 5090")
mcp.browser.click("search-button")
mcp.browser.screenshot("/tmp/gpu-search.png")
# Hermes sends screenshot to Telegram
```

## Kanban Board (ScarliHQ built-in)

Issues created by Hermes, assigned to omp specialists:
- TODO → IN_PROGRESS → REVIEW → DONE
- Each issue has: description, assignee, branch, PR link
- Review gates: omp cannot merge without Hermes approval
