# SCARLIX OS v19 — Architecture

> **v19.0.2 note:** This document was previously stale (described the abandoned
> v12 Ubuntu-ISO workflow). It has been rewritten to reflect the actual v19
> EndeavourOS/Arch `git clone + bash install.sh` architecture. For the
> step-by-step install procedure see `README.md`; for the obsolete v12 Ubuntu
> guide see `docs/archive/`.

## Install Model (v19+)

- **Base:** EndeavourOS (near-vanilla Arch). NO ISO — `git clone` + `bash install.sh`.
- **Installer:** 5 phases — packages → NVIDIA → Docker → copy files → wizard + ScarliHQ build.
- **Kernel:** `linux` + `linux-lts` (fallback). BTRFS + Snapper. ZRAM min(ram/2, 16GB). Pipewire.
- **GPU:** NVIDIA driver + CUDA 12.8 (`cu128` container images) + nvidia-container-toolkit.

## 5-Layer Architecture

### Layer 1: Inference + Base OS
- **Base OS:** EndeavourOS (Arch rolling) + Docker
- **NVIDIA:** Driver (linux driver) + CUDA 12.8 (`cu128` images) + Container Toolkit
- **SGLang** (GPU 0, `--disable-flashinfer`): Primary inference, AWQ, Qwen3-14B-AWQ
- **vLLM** (GPU 1, TP=1, `.experimental` only): High-throughput, experimental
- **BeeLlama** (CPU, llama.cpp): Offline fallback, GGUF Q4_K_M
- **Ollama** (CPU): Tertiary fallback, starter model qwen2.5:3b

### Layer 2: Workspace + Infrastructure
- **Buzz** (Nostr relay): Signed audit trail, channels, git events
- **LiteLLM** (gateway `:4001`): Unified OpenAI-compatible API, 3-tier failover (SGLang → Ollama → BeeLlama; vLLM excluded — experimental only)
- **SMG** (Scarlix Model Gateway, `:4000`, profile-gated): Latency-aware routing with `fallback_chain: [sglang-main, ollama-agent, beellama-cpu]`
- **Headscale** (VPN): WireGuard mesh, MagicDNS
- **Caddy** (proxy): Auto-HTTPS, reverse proxy
- **CrowdSec** (WAF): Intrusion prevention

### Layer 3: Agents
- **Hermes** (CEO): Multi-platform agent gateway — image `nousresearch/hermes-agent:v0.22.2`
  (digest-pinned; was v2026.9.24/v0.21.5). Runs as a supervised s6 gateway
  (`command: gateway run`); user data at `./config` → `/opt/data`; readiness via
  `GET /health` on container-loopback **8642** (API server, no host port published;
  the old 7999 publish is gone — nothing consumed it). Telegram auth = bot token +
  `TELEGRAM_ALLOWED_USERS` (user IDs, fail-closed). LiteLLM contract unchanged
  (`http://litellm:4000/v1` over `scarlix-net`, model `qwen3.6:14b` via top-level
  `base_url`/`api_key` config keys). ScarliHQ MCP = `mcp_servers.scarlihq` with a
  **read-only tool allowlist** (`scarlix_mode_set` deliberately excluded — mode
  switches stay behind Host-Bridge authorization). Migration/rollback:
  `agents/hermes/migrate-to-v0.22.2.sh`, full analysis in
  `agents/hermes/UPGRADE_REPORT.md`.
- **OpenCode** (Coding Manager): Build/plan agents
- **gstack**: Senior manager tools (Designer, Eng Manager, CFO, Security, etc.)

### Layer 4: ScarliHQ (Host-Bridge)
- **Go binary** (`:8090`) with go:embed 2D dashboard, token auth
- **REST API** + **WebSocket** + **MCP Server**
- **Profile Manager** (4 profiles: Zmor/Hugo/XOX/Mon)
- **Guard** (destructive_command_guard)
- **Memory** (SQLite + cosine similarity)
- **Model Swap** (SGLang ↔ Ollama ↔ BeeLlama via scarlix-mode)
- **Secure Host-Bridge architecture:** ScarliHQ container has NO docker.sock, NO nvidia runtime, NO scarlix-mode mount. Desired mode written to `bridge-input/` (UID 65532, mode 700); root `scarlix-host-bridge` (5s timer) reads it via `scarlix-bridge-reader` (Go, `O_NOFOLLOW` + fstat — no TOCTOU).

### Layer 5: Family (Profiles)
- **Zmor** (admin): Unlimited, Iron Man theme, all modes
- **Hugo** (son): 100k tokens/month, Gaming theme, game+ai
- **XOX** (daughter): 10k tokens, Creative theme, ai only, kids-safe
- **Mon** (wife): 50k tokens, Elegant theme, ai+game

## Gaming Stack (v19.3.1)

Gaming is a **native desktop layer** — game launchers are host packages, not
containers. Docker remains for `sunshine-tv` (TV streaming), Jellyfin (media
server) and Minecraft (game server); those are services, not launchers.

```
┌─────────────────────────────────────────────────────────────────┐
│ Desktop session (user)                                          │
│  Steam · Lutris · Heroic · Bottles · itch · RetroArch           │
│  Wine(+Gecko/Mono) · Vulkan loaders (64/32-bit)                 │
│  Gamescope · MangoHud · GameMode      ← full profile (opt-in)   │
├─────────────────────────────────────────────────────────────────┤
│ scarlix-mode game → native Sunshine (systemd, user-managed)     │
│ scarlix-mode tv   → sunshine-tv (Docker, compose profile tv)    │
├─────────────────────────────────────────────────────────────────┤
│ One manifest: files/usr/local/share/scarlix/gaming-packages.conf│
│  ├─ scarlix-gaming-install (profiles: minimal|full|headless)    │
│  └─ scarlix-host-bridge inventory detection (read-only)         │
│        pacman -Q · systemctl · docker ps · flatpak list         │
│        → host-status.json ["gaming"] → GET /api/gaming (auth)   │
└─────────────────────────────────────────────────────────────────┘
```

- **Profiles:** `minimal` (base set, mirrors the `packages.x86_64` gaming
  block — default, unchanged), `full` (opt-in extras via
  `scarlix-gaming-install --profile full`), `headless` (report-only; AI/server
  hosts comment out the gaming block in `packages.x86_64` before install).
- **Read-only inventory:** ScarliHQ never launches/installs/removes games.
  Detection is host-side in the host bridge; the API is GET-only behind the
  auth middleware. Full details: `gaming/README.md`.
- **AI/game arbitration stays with scarlix-mode:** no second GPU manager;
  `game` stops the AI path and starts Sunshine, `ai` returns (fail-closed,
  stale-state repair).

## Host Bridge workload lifecycle (v19.3.1, P5.1+P6 unified)

The allowlisted privileged-operations path from the dashboard gained a typed
workload control surface (previously the `19.2.14.P6` branch):

```
ScarliHQ (container, non-root, no docker.sock)
   │ POST /api/compute/start|stop (auth, typed JSON, unknown fields rejected)
   ▼
workload.Manager (allocation store transitions: RESERVED→STARTING→READY→RUNNING,
   uncertain starts fail closed into CLEANUP_REQUIRED / ErrReconciliationRequired)
   ▼
hostbridge.Client (Unix socket, 0600, admin-owned parent dir)
   ▼
scarlix-host-workload-bridge (host systemd service, User=65532 + docker group,
   hardened: NoNewPrivileges, ProtectSystem=strict, …)
   ▼
hostbridge.Server (2 endpoints only: /v1/workloads/start|stop; 16 KiB request cap)
   ▼
IdempotentExecutor + Ledger → docker_executor (compose project ops)
```

The service ships **disabled by default** with a config template
(`/usr/local/share/scarlix/workload-bridge.json.example`); enabling it is an
explicit operator decision (see
`docs/SCARLIX_COMPUTE_FABRIC_P5_1_HOST_BRIDGE.md`). The ScarliHQ container
never receives the Docker socket — the docker-group bridge binary runs on
the host and exposes only the two typed operations. Integration contract:
`scarlihq/internal/integrationcontract/integration-contract.v1.json`.

## Reservation governor (v19.3.2, F-04 — Compute Fabric closure)

The reservation engine's production composition root — the service that owns
`NewFileBackedManager`, runs `Recover()` at startup and the lease reaper in
steady state (test-only through v19.3.1):

```
startup:  /etc/scarlix/reservation.json (0640 root:65532, tokens inside)
   │       → TokenIdentityProvider (reconciler + optional user principals)
   │       → HostInventoryProvider (read-only collectors + managed ports)
   │       → DockerCleanupVerifier (docker ps, scarlix.managed labels)
   ▼
reservation.NewFileBackedManager(reservations.jsonl, allocations.jsonl, …)
   ▼
Recover(reconciler token)   [finalize RESERVING · reap expired · complete
   │                         interrupted releases · repair RESERVED leases]
   ▼
RunLeaseReaper(ctx, 30s)    [same convergence, periodically, until SIGTERM]
```

Deployment: `scarlix-reservation.service` (User=65532, docker group as a
**read-only** consumer, `ProtectSystem=strict`, `StateDirectory=scarlix-reservation`),
**disabled by default** — enablement is an operator runbook with a fail-closed
`CHANGE_ME` template. The governor is deliberately **off the HTTP plane**
(no socket, no API): it reconciles; workload start/stop stays on the workload
bridge above, and future Reserve/Release intake reuses the same stores under
the same flock. Full design + runbook:
`docs/SCARLIX_COMPUTE_FABRIC_RESERVATION_GOVERNOR.md`.

The allocation store is shared with the ScarliHQ compute lifecycle (both
sides UID 65532, flock-serialized) — one consistent record set, documented in
the compose opt-in block (`SCARLIHQ_ALLOCATION_STORE=/var/lib/scarlix-reservation/allocations.jsonl`).

## Agent Kernel foundation + sessions + grants + audit (v19.4.x, AK-1…AK-4)

The deterministic core every future agent capability flows through —
**"LLM navrhuje, politika rozhoduje"** (strategy p. 13). The kernel owns
identity, capabilities, sessions, permission evaluation and audit events;
AK-2 (v19.4.1) makes it reachable through an opt-in, auth-gated REST
surface:

```
AgentID (typed "kind.name", no anonymous agents)
   │        AgentIdentity{ID, Owner, CreatedAt} — owner principal mandatory
   ▼
CapabilitySet (closed enumeration: status.read · gaming.read · mode.request
   │           compute.plan · compute.reserve · compute.release — NO shell.exec)
   ▼
Session (CREATED → ACTIVE → SUSPENDED → CLOSED; CLOSED terminal;
   │      illegal transitions + backwards clocks rejected)
   ▼
Policy.Evaluate(agent, session, caps, capability) → Decision{Allow, Reason}
   │      fixed rule order: session-required → session-owner → session-state
   │      → capability-unknown → capability-not-granted → capability-disabled
   ▼
AuditEvent{Timestamp, AgentID, SessionID, Capability, Decision} (AK-4 persists)
```

Pure, deterministic evaluation (same input → same decision + reason, no LLM
in the decision path); every denial names the fired rule, every allow cites
the grant. Extending the capability set is a deliberate reviewed act
(`AllCapabilities` + tests change together). Package:
`scarlihq/internal/agentkernel`. Design + AK-1…AK-5 sub-phase plan:
`docs/AGENT_KERNEL_AK1_DESIGN.md`.

AK-2 runtime half: a **bounded session store** (default 128 records — the
cap counts CLOSED too; fail-closed `aks-` crypto-random IDs; explicit
grants only; deep-copy reads; evaluation under the store lock — no TOCTOU)
and the REST surface `/api/agent/sessions` (create / list / transition /
evaluate; denial-is-200) behind the standard Bearer auth middleware.
**Opt-in** via `SCARLIHQ_AGENTKERNEL_ENABLED=true` (503 until configured;
commented compose block) — same enablement contract as the compute
lifecycle. AK-3 (v19.4.4) makes grants **persisted, admin-owned acts**: a
JSON grant registry at `/var/lib/scarlix-agentkernel/grants.json` (0600/
0700, atomic temp+rename writes, tombstone revocations — the file is the
reviewable history) with owner principals tied to the governor's
`user_tokens` space (`SCARLIHQ_AGENT_PRINCIPALS`), plus admin disable
switches that take effect on the next evaluation. In registry mode,
session creation derives grants + owner from the registry and rejects
request-supplied `capabilities`; the legacy AK-2 contract stays in force
without a registry. Fail-closed load: a malformed/tampered/loose-permission
registry aborts startup. REST: `/api/agent/grants` (GET/POST),
`/api/agent/grants/revoke`, `/api/agent/capabilities/disable|enable` —
all behind Bearer auth, 503 until opted in. AK-4 (v19.4.5) persists every
decision and admin act: an **append-only JSONL audit sink** at
`/var/lib/scarlix-agentkernel/audit.jsonl` (0600/0700, flock-serialized,
flush+fsync per line) — evaluations are persisted BEFORE the decision is
served (fail-closed: an unauditable decision is never returned; admin acts
are appended after their persist with failures reported). Read surface
`GET /api/agent/audit` (last N lines newest-first, malformed lines reported
not hidden). Opt-in via `SCARLIHQ_AGENT_AUDIT_FILE`. AK-5 flows
`compute.reserve`/`compute.release` through the governor stores.

## GPU Arbitration (scarlix-mode)

| Mode | GPU 0 | GPU 1 | CPU | Notes |
|------|-------|-------|-----|-------|
| ai | SGLang (AWQ) | vLLM (TP=1, `.experimental` only) | BeeLlama + Ollama | Default working path |
| stop | — | — | — | All inference stopped |
| creative | SGLang | — | BeeLlama | + ComfyUI / Video (Wan2GP) |
| game | Sunshine | — | — | Gaming streaming |
| turbo | SGLang | SGLang | — | Max throughput (2 GPU) |
| tv | — | — | — | TV dashboard only |
| offline | — | — | BeeLlama | No GPU, CPU-only fallback |

## Inference Failover (scarlix-mode direct AI path — 4-tier)

```
Request → scarlix-mode direct path
              ↓
         SGLang (GPU 0, AWQ)        ← primary (tier-1)
              ↓ (if fail)
         vLLM (GPU 1, .experimental) ← high-throughput (tier-2, skipped if no .experimental)
              ↓ (if fail)
         BeeLlama (CPU, GGUF)       ← fallback (tier-3)
              ↓ (if fail)
         Ollama (CPU)               ← tertiary fallback (tier-4, qwen2.5:3b starter)
```

## LiteLLM Gateway failover (external clients — 3-tier, vLLM excluded)

```
Request → LiteLLM :4001 (OpenAI-compatible)
              ↓
         SGLang (scarlix-default)   ← primary
              ↓ (if fail)
         Ollama (scarlix-ollama)    ← fallback
              ↓ (if fail)
         BeeLlama (scarlix-beellama) ← offline
```

vLLM is excluded from the LiteLLM 3-tier because it is experimental (`.experimental` flag only). scarlix-mode's direct AI path keeps the full 4-tier including vLLM.
