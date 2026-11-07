# ScarLiX OS — Canonical Current Architecture (v19.6.8)

> **Status:** CANONICAL — this is the one source of truth for the v19.6.8
> architecture. Older architecture documents in this repository carry a
> `Historical snapshot` banner and describe the version named in their
> header, not the current system.
> **Code truth:** verified against the v19.6.8 working tree (the repository
> is the source of truth; where an older document conflicts with this one,
> this one and the code win).
> **Roadmap:** the target architecture (Compute Fabric v19.7.0 → v20.0.0)
> is specified in
> [`docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`](../roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md).
> This document describes what EXISTS; the roadmap describes what is
> PLANNED. Planned capability must never be documented here as present.

------------------------------------------------------------------------

## 1. Install model (frozen since v19.0.0)

- **Base OS:** EndeavourOS (near-vanilla Arch). No ISO — `git clone` +
  `bash install.sh` (5 phases: packages → NVIDIA → Docker → copy files →
  wizard + ScarliHQ build). Source: `install.sh`.
- **Kernel:** `linux` + `linux-lts` (fallback). BTRFS + Snapper. ZRAM
  `min(ram/2, 16GB)`. Pipewire.
- **GPU:** NVIDIA driver + CUDA 12.8 (`cu128` images) +
  `nvidia-container-toolkit`. Source: `docs/HARDWARE.md`, `install.sh`
  Phase 2.

## 2. The 5-layer model (current truth)

| Layer | Components | Purpose |
|---|---|---|
| 1 — Inference + Base OS | EndeavourOS + Docker + NVIDIA driver + 4 inference engines (SGLang GPU0 primary, vLLM GPU1 experimental, BeeLlama CPU, Ollama CPU fallback) | Compute substrate |
| 2 — Workspace + Infrastructure | LiteLLM `:4001`, SMG `:4000`, Headscale VPN, Caddy, CrowdSec | Services that route/observe/persist |
| 3 — Agents | Hermes (CEO gateway), Pi-Bolt (coding agent) | Long-running agents |
| 4 — ScarliHQ | Go binary `:8090` (REST + WS + MCP), go:embed 2D dashboard, Profile Manager + identity-bound family profiles, Agent Kernel, Universal AI Runtime (airt), Compute Fabric control packages | Host-Bridge control plane |
| 5 — Family (Profiles) | Zmor (admin, unlimited), Hugo (100k), XOX (10k, kids-safe), Mon (50k) — per-profile Bearer tokens (v19.6.7) | Per-user policy layer |

## 3. Secure Host-Bridge pattern (the privilege boundary)

The ScarliHQ dashboard container is **untrusted + LAN-facing**. It has NO
direct host capability. Privileged operations are funneled through typed
bridges:

1. **Mode bridge (file-based, v19.0.x):** ScarliHQ writes
   `/var/lib/scarlix/bridge-input/desired-mode` (O_EXCL atomic create);
   `scarlix-host-bridge.timer` (root, every 5s) validates
   UID=65532 + mode + size + S_IFREG and runs `scarlix-mode <mode>` (root,
   flock'd); status flows back through `/var/lib/scarlix/host-status.json`
   (atomic mktemp + mv) → `/api/status` + `/ws` (2s push).
2. **Workload bridge (typed Unix-socket RPC, v19.2.x+):**
   `scarlix-workload-bridge` (root) executes ONLY fixed-profile Docker
   operations behind `internal/hostbridge` — no arbitrary commands, no
   caller-supplied images/paths/ports. **The workload bridge service
   remains DISABLED until the P5.1 target-host acceptance matrix passes**
   (see `docs/SCARLIX_COMPUTE_FABRIC_P5_1_HOST_ACCEPTANCE.md` — status
   OPEN). ScarliHQ never mounts the Docker socket.

## 4. ScarliHQ package map (code truth, v19.6.8)

``` text
scarlihq/internal/
  agentkernel/   # AK-1..AK-5: sessions, persisted grants, audit sink,
                 #   deterministic capability evaluation
  agentfabric/   # BF-1..BF-4: bar commands, workspaces, executions,
                 #   approvals, CF-3 proposals (LLM proposes; operator
                 #   accepts is the only door to a command)
  airuntime/     # Universal AI Runtime domain: CLOSED 4-provider set
                 #   (sglang, vllm, beellama, ollama), model registry,
                 #   health semantics, routing
  airt/          # AI transport: local-only HTTP client (no redirects,
                 #   no ambient proxy, dial-guarded + address-pinned),
                 #   bounded failover chat, probe rounds, ObserveBatch
                 #   atomic health commits, enforced token budgets
  inventory/     # normalized GPU/runtime/model/service/health inventory
  reservation/   # canonical resource IDs, atomic bundles, leases/TTL,
                 #   owner identity, idempotency fingerprints,
                 #   reconciliation, release recovery scan
  allocation/    # THE lifecycle authority: frozen state machine
                 #   (+ v19.6.8 additive STOP_REQUESTED and
                 #   RECONCILE_REQUIRED), JSONL store
  workload/      # start/stop lifecycle through the trusted driver
  hostbridge/    # typed Unix-socket client/server + fixed-profile
                 #   Docker executor (idempotent, ledgered)
  compute/       # composition root: workload manager + hostbridge client
  scheduler/     # dry-run planner path (the pre-CF scheduling surface)
  profiles/      # strict YAML family policies + identity-bound
                 #   authenticator + enforced monthly token budgets
  api/           # authenticated bounded HTTP surfaces (REST + WS + MCP)
  mcp/           # MCP tool surface (same principal/authz as REST)
  guard/ status/ monitor/ telemetry/ registry/ contract/ compat/
  scarlix_mode/ gaming/ webui/ integrationcontract/
```

**One lifecycle authority:** every state change flows through
`allocation.Allocation.Transition` (the frozen table in
`internal/allocation/transitions.go`). No package duplicates the state
machine; the API never invents transitions.

## 5. The allocation lifecycle (v19.6.8 vocabulary)

``` text
DISCOVER → PLAN → RESERVE → START → WAIT_READY → RUN
        → STOP_REQUESTED → CLEANUP_VERIFIED → RELEASE
```

Rules (enforced by code, see
[`COMPUTE_FABRIC_CONTRACT.md`](COMPUTE_FABRIC_CONTRACT.md)):

- `PLAN` is side-effect-free (the scheduler dry-run path).
- `RESERVE` is a separate operation with its own intake — the reservation
  service (`cmd/scarlix-reservation`), governor-stored and
  flock-serialized. **The agent compute intake does NOT reserve**: its
  actions are `start` and `release` (the v19.6.8 lifecycle-vocabulary
  correction — the legacy `reserve` spelling is rejected by name).
- `START` accepts only an existing `RESERVED` allocation; the workload
  manager drives STARTING → READY → RUNNING with fail-closed
  CLEANUP_REQUIRED on uncertain outcomes.
- `STOP_REQUESTED` (v19.6.8): a stop request for a live workload is
  recorded durably BEFORE the driver call; the stop does not release
  capacity by itself.
- `RELEASE` only after verified cleanup (KPK-01: the transition to
  RELEASED requires `CleanupVerified == true`), and only through the
  trusted reservation reconciliation path.
- An ambiguous outcome becomes `RECONCILE_REQUIRED` (v19.6.8) — a
  reconciler must decide; never a blind retry. The reservation release
  scan covers RELEASING, CLEANUP_REQUIRED, FAILED, CANCELLED and
  RECONCILE_REQUIRED.

## 6. Universal AI Runtime (transport + failover)

- **Closed provider set:** sglang (GPU0 primary), vllm (GPU1
  experimental), beellama (CPU), ollama (CPU fallback). Adding a backend
  is a deliberate multi-surface change (registry/router/prober/API/tests),
  never "just a constant".
- **Transport** (`internal/airt`): the ONLY http.Client constructor is
  local-only — no redirects (`http.ErrUseLastResponse`), no ambient proxy,
  dial-guarded with resolve→validate→pin (a MIXED answer set is refused
  whole), loopback always trusted, non-loopback trust = the operator
  declaration `SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS` (fail-closed parse, no
  `0.0.0.0/0`).
- **Failover:** one end-to-end budget across the closed tier set; expiry
  is the deliberate 504; health rounds commit atomically
  (`ObserveBatch`) with the context-aware commit boundary.
- **Budgets:** `/api/ai/chat` reserves-before-forward against the
  profile's enforced monthly ledger (v19.6.7); failures are released
  whole.
- **Proposals:** `/api/ai/propose` captures a well-formed
  `scarlix-proposal` block as a PENDING proposal — the operator's accept
  is the only door into the agent fabric (CF-3).

## 7. Known limitations (honest, current)

- **Hardware acceptance OPEN** (KPK-06, since 19.3.x): CI green is never
  hardware verification; the workload bridge stays disabled until the
  P5.1 target-host matrix passes.
- Host executor is conservatively one-active-allocation per logical GPU
  and per host port (safe, limits concurrency — aggregate admission is
  v19.7.1 work per the roadmap).
- No unified storage inventory yet (v19.7.0 work per the roadmap); no
  model execution metadata/MoE manifests yet (v19.7.0).
- The content filter is keyword-level; the in-subnet lateral residual is
  inherent (documented since v19.6.5); the four experimental `:latest`
  images are time-bounded allowlisted until 2027-01-31.
- The token budget covers the `/api/ai/chat` path only (propose is
  admin-gated).

## 8. Document status map

| Document | Status |
|---|---|
| `docs/architecture/SCARLIXOS_CURRENT_ARCHITECTURE.md` (this file) | **Canonical** |
| `docs/architecture/COMPUTE_FABRIC_CONTRACT.md` | **Canonical** (lifecycle + API contract) |
| `docs/architecture/AI_RUNTIME_CONTRACT.md` | **Canonical** (runtime + transport contract) |
| `docs/SCARLIX_CURRENT_ARCHITECTURE.md` | Historical snapshot (v19.2.9) |
| `docs/ARCHITECTURE.md` | Historical snapshot (pre-v19.0.6 layer map) |
| `docs/SCARLIX_GPU_MAP.md`, `docs/SCARLIX_RUNTIME_MAP.md` | Historical snapshot (v19.2.9) |
| `docs/SCARLIX_SECURITY_MAP.md`, `docs/SCARLIX_API_MAP.md` | Living (sections versioned per release) |
| `docs/SCARLIX_COMPUTE_FABRIC_*` (Phase 2/3/5/6, P5.1, governor, P4/P6 integration) | Living per-phase engineering docs (Phase 2 domain docs are subsumed by COMPUTE_FABRIC_CONTRACT.md) |
| `docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md` | The v19.6.8 → v20.0.0 implementation plan |
