# SCARLIX OS Skills

This directory contains replication/automation skills for SCARLIX OS.

## scarlix-os-v19-replica

A self-contained skill specification that enables an AI coding agent to reconstruct, audit, or set up the SCARLIX OS v19.2.1 local-AI platform from its pinned public source or from the embedded specification.

### What the skill contains

- **Architecture & repository structure** — 5-layer topology, directory layout, Go packages
- **EndeavourOS/Arch installer** — 5 phases, checkpoints, system prerequisites
- **ScarliHQ control plane** — Go service, REST/WebSocket/MCP APIs, secure Host-Bridge
- **Inference stack** — SGLang, vLLM, BeeLlama, Ollama, LiteLLM, SMG (versions, ports, fallback)
- **Resource Contract v1** — schema, validation, scheduler dry-run limitation
- **Security requirements** — command injection tests, secret handling, privilege boundaries
- **Reconstruction procedure** — test matrix, acceptance criteria, mandatory `REPLICATION_REPORT.md`

### Pinned source

- **Repository:** https://github.com/MoZoHuJa/OS
- **Target version:** `19.2.1` (ScaRgeN_Zero_alpha)
- **Target commit:** `406d6929f511b3d00b26a5538ad04096f1fe2d9e`
- **VERSION file:** must contain exactly `19.2.1`

### How to use

**Option 1 — Direct agent input:** Give `SKILL.md` to a coding agent (Claude, GPT, etc.) with a task to reproduce the system.

**Option 2 — Vercel Skills CLI:** Install via the [vercel-labs/skills](https://github.com/vercel-labs/skills) framework:

```bash
# The skill lives at skills/scarlix-os-v19-replica/SKILL.md in this repo
# Install it into your Vercel Skills environment per the framework documentation
```

### Technical note

The `SKILL.md` is **not** a complete copy of the source code. It is a detailed specification with instructions for an agent to reproduce the system. For the most faithful reproduction, the skill prefers the exact pinned commit. If an agent has no repository access, it can create an implementation from the specification, but byte-for-byte reproduction cannot be guaranteed.

This boundary is intentional — the goal is a **reproducible and verifiable** SCARLIX OS v19.2.1, not merely a project with a similar name and README.
