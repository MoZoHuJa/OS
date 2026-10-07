# Pi-Bolt — Coding Agent (v19.0.10)

> **Status:** Active — replaces OpenCode as the SCARLIX OS coding agent.

## Overview

[Pi-Bolt](https://github.com/opensec-git/Pi-Bolt) is a fork of Pi compiled ahead-of-time to native code. It retains Pi's commands, keys, sessions, settings, extensions and providers while changing the runtime to an AOT executable.

Pi-Bolt shares Pi's configuration directory (`~/.pi/agent`), avoiding a second incompatible configuration ecosystem.

## Architecture (post-migration)

```
                    SCARLIX OS
                        │
             ┌──────────┴──────────┐
             │                     │
          HERMES                PI-BOLT
        autonomous AI          coding agent
             │                     │
       memory / skills        git / shell / files
       web / automation       planning / coding
       MCP / subagents        subagents
             │                     │
             └──────────┬──────────┘
                        │
                   existing API
                        │
                    LiteLLM
                        │
             ┌──────────┼──────────┐
             │          │          │
           SGLang      vLLM     fallbacks
```

## Installation

Pi-Bolt is installed as a **non-root user** (per security policy) via the official installer:

```bash
curl -fsSL https://pi-bolt.opensec.in/install.sh | sh
```

The installer:
- Places Pi-Bolt under `~/.pi-bolt`
- Links `~/.local/bin/pi-bolt`
- Shares Pi's config at `~/.pi/agent`

**Never install as root:** `sudo curl ... | sh` is forbidden per the migration guide section 8.

`install.sh` Phase 5 runs this installer as the real user (not root).

## Configuration

The config template is at [`config.template.json`](config.template.json). Key settings:

| Setting | Value | Notes |
|---------|-------|-------|
| Provider | `openai` | OpenAI-compatible (LiteLLM gateway) |
| Model | `scarlix-default` | LiteLLM alias (routes SGLang→Ollama→BeeLlama) |
| Base URL | `http://127.0.0.1:4001/v1` | LiteLLM gateway (localhost-only) |
| API Key | `${LITELLM_MASTER_KEY}` | Env var from `/etc/scarlix/.env` (never hardcoded) |
| MCP | `http://127.0.0.1:8090/mcp` | ScarliHQ MCP endpoint (JSON-RPC 2.0) |

### Security model
- **No real secrets committed** — API key is an env var reference (`${LITELLM_MASTER_KEY}`)
- **localhost-only endpoints** — no inference or MCP exposed to LAN/WAN
- **No docker.sock** — Pi-Bolt has no Docker access
- **No NVIDIA runtime** — Pi-Bolt is a coding agent, not an inference engine
- **Non-root** — runs as the real user

## Workflow

Pi-Bolt preserves the functional distinction from OpenCode:

```
PLAN mode (read-only)
  ↓
inspect → analyze → propose changes
  ↓
user/coder approval
  ↓
BUILD mode
  ↓
edit → test → git diff
  ↓
destructive git actions require approval
```

## Extensions (minimal required)

Start with the minimum:
- **Pi-Bolt core** (required)
- **opensec-pi-subagents** (optional — multi-agent workflows)

Do NOT install every available extension. Treat extensions as executable code (source review required per migration guide section 20).

## Rollback

If Pi-Bolt fails acceptance:
1. The previous OpenCode config is preserved at `agents/opencode/config.json.DEPRECATED`
2. Restore via `git revert <migration-commit>`
3. Rename `config.json.DEPRECATED` back to `config.json`

## Migration documentation

Full migration guide: [`docs/SCARLIX_AGENT_MIGRATION_OpenCode_to_PiBolt.md`](../docs/SCARLIX_AGENT_MIGRATION_OpenCode_to_PiBolt.md)

## References

- Pi-Bolt repository: https://github.com/opensec-git/Pi-Bolt
- Pi package ecosystem: https://pi.dev/packages
- OpenSec Pi subagents: https://pi.dev/packages/opensec-pi-subagents
