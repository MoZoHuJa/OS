# SCARLIX OS — OpenCode → Pi-Bolt Migration

**Version:** v19.0.10
**Date:** 2026-10-06
**Status:** Repo-level migration complete (binary test pending on GPU host)

---

## Summary

Replaced the OpenCode coding-agent layer with [Pi-Bolt](https://github.com/opensec-git/Pi-Bolt) — a fork of Pi compiled AOT to native code. The migration is an **isolated agent-layer replacement**: no inference, security, dashboard, Docker, or host-bridge architecture was changed.

## Before → After

```
BEFORE                              AFTER
─────                              ─────
Hermes                              Hermes
   +                                   +
OpenCode                            Pi-Bolt
   +                                   +
LiteLLM                             LiteLLM
   +                                   +
SGLang/vLLM/fallback               SGLang/vLLM/fallback
   +                                   +
ScarliHQ/security layer             ScarliHQ/security layer
```

## Changed files

| Path | Reason |
|------|--------|
| `agents/pi-bolt/config.template.json` (NEW) | Pi-Bolt config template — preserves OpenCode functional intent (LiteLLM gateway, ScarliHQ MCP, plan/build agents) with security fixes (localhost-only, env-var API key) |
| `agents/pi-bolt/README.md` (NEW) | Pi-Bolt documentation — install, config, security model, workflow, rollback |
| `agents/opencode/config.json` → `agents/opencode/config.json.DEPRECATED` (RENAMED) | Quarantined for rollback — not deleted per migration guide section 39 ("Keep rollback possible") |
| `agents/opencode/DEPRECATED.md` (NEW) | Marker explaining deprecation + rollback procedure |
| `install.sh` (MODIFIED) | Added Pi-Bolt install block in Phase 5 (non-root user, curl installer, LITELLM_MASTER_KEY env export to user profile) |
| `docs/SCARLIX_CURRENT_ARCHITECTURE.md` (MODIFIED) | Layer 3 table: "OpenCode (coding manager)" → "Pi-Bolt (coding agent)" |

## Removed files

None. OpenCode config is quarantined (renamed to `.DEPRECATED`), not deleted — preserving rollback.

## Pi-Bolt version

- **Repository:** https://github.com/opensec-git/Pi-Bolt
- **Installer:** `curl -fsSL https://pi-bolt.opensec.in/install.sh | sh`
- **Target:** Linux x86-64 / glibc
- **Build:** AVX2 (if CPU supports) or baseline x86-64

## Model path

| Setting | Value |
|---------|-------|
| Provider | `openai` (OpenAI-compatible) |
| Base URL | `http://127.0.0.1:4001/v1` (LiteLLM gateway, localhost-only) |
| Model alias | `scarlix-default` (LiteLLM routes: SGLang → Ollama → BeeLlama) |
| API key | `${LITELLM_MASTER_KEY}` (env var from `/etc/scarlix/.env`, never hardcoded) |

## MCP

| Setting | Value |
|---------|-------|
| Server | `scarlihq` |
| Transport | HTTP |
| Endpoint | `http://127.0.0.1:8090/mcp` (ScarliHQ JSON-RPC 2.0) |
| Authentication | Existing ScarliHQ bearer-token auth (unchanged) |

## Security

| Check | Status |
|-------|--------|
| docker.sock exposure | ✅ NO — Pi-Bolt has no Docker access |
| root privilege | ✅ NO — installed + runs as non-root user |
| NVIDIA runtime | ✅ NO — Pi-Bolt is a coding agent, not inference |
| bridge-state write | ✅ NO — Pi-Bolt has no host-bridge access |
| .env secret leak | ✅ NO — API key is env var reference, not committed |
| inference endpoint exposed publicly | ✅ NO — localhost-only (127.0.0.1) |
| hard-coded production secrets | ✅ NO — `${LITELLM_MASTER_KEY}` env var pattern |

## Regression (unchanged)

All of these are **completely unchanged** by the migration:

- Hermes (agents/hermes/) — config + compose untouched
- ScarliHQ (scarlihq/) — Go application untouched
- Dashboard (:8090) — untouched
- LiteLLM (:4001) — untouched
- SGLang (:30000) — untouched
- vLLM (:8089) — untouched
- BeeLlama (:11438) — untouched
- Ollama (:11435) — untouched
- scarlix-mode — untouched
- GPU configuration — untouched
- Docker stack — untouched
- models.yaml — untouched
- host-bridge + bridge-reader — untouched
- /etc/scarlix/.env generation — untouched

## Tests (pending GPU host)

The migration guide section 21 defines 10 functional tests (A–J) that require a real Pi-Bolt binary on the target hardware:

- TEST A: startup (`pi-bolt --version`)
- TEST B: model connectivity (reaches LiteLLM gateway)
- TEST C: read-only repository inspection
- TEST D: file read (AGENTS.md, VERSION, models.yaml)
- TEST E: planning (plan only, no changes)
- TEST F: controlled write (temp file)
- TEST G: Git awareness (no commit)
- TEST H: Git diff (read + explain, no commit)
- TEST I: MCP (harmless read-only ScarliHQ MCP op)
- TEST J: subagent (if opensec-pi-subagents installed)

**These tests require a GPU host with Pi-Bolt installed — not verifiable in repo-only sandbox.**

## Rollback procedure

If Pi-Bolt fails acceptance:

```bash
# 1. Revert the migration commit
git revert <migration-commit>

# 2. Restore OpenCode config
mv agents/opencode/config.json.DEPRECATED agents/opencode/config.json

# 3. Remove Pi-Bolt (if installed)
rm -rf ~/.pi-bolt ~/.local/bin/pi-bolt

# 4. Remove LITELLM_MASTER_KEY export from .bashrc
#    (delete the line with "SCARLIX LITELLM_MASTER_KEY for Pi-Bolt")
```

## Final decision

```
MIGRATION ACCEPTED (repo-level)
```

The repo-level migration is complete and verified:
- No secrets committed
- No security boundaries weakened
- No regression to existing architecture
- Rollback path preserved
- OpenCode quarantined (not deleted)

**Remaining gate:** real Pi-Bolt binary test on GPU host (tests A–J per migration guide section 21). Once those pass on hardware, OpenCode can be fully deleted (remove `agents/opencode/` directory).

## References

- Pi-Bolt repository: https://github.com/opensec-git/Pi-Bolt
- Pi package ecosystem: https://pi.dev/packages
- OpenSec Pi subagents: https://pi.dev/packages/opensec-pi-subagents
- Hermes Agent: https://github.com/NousResearch/hermes-agent
- Migration guide (full): `upload/SCARLIX_OS_OpenCode_to_Pi-Bolt_Migration_Guide.md`
