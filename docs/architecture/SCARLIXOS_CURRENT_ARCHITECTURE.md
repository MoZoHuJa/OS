# ScarLiX OS — Canonical Current Architecture (v20.0.0)

> **Status:** CANONICAL — this is the one source of truth for the v20.0.0
> architecture. Older architecture documents in this repository carry a
> `Historical snapshot` banner and describe the version named in their
> header, not the current system.
> **Code truth:** verified against the v20.0.0 working tree (the repository
> is the source of truth; where an older document conflicts with this one,
> this one and the code win).
> **Roadmap:** the roadmap's phases are COMPLETE at v20.0.0 (the fabric
> sequence ended here; the next evolution would carry its own document)
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

## 4. ScarliHQ package map (code truth, v20.0.0)

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
                 #   + the v2 unified snapshot (v19.7.0, Phase 1):
                 #   InventorySnapshotV2 — host/storage/GPU/runtime/model
                 #   + model execution views + per-source provenance +
                 #   serve-time freshness (docs/INVENTORY_V2_CONTRACT.md)
  storage/       # v19.7.0 (Phase 1): READ-ONLY storage discovery
                 #   (mounts/sysfs/statfs; no exec, no writes), host
                 #   resources, and the OPT-IN bounded read benchmark
                 #   (the only benchmark door is the CLI's
                 #   --storage-benchmark — never discovery)
  modelmeta/     # v19.7.0 (Phase 1): model execution metadata + the
                 #   verified manifest contract (scarlix.model/v2,
                 #   fail-closed validation, the placement-eligibility
                 #   gate — unknown required values block placement)
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
  admission/     # v19.7.1 (Phase 2) + v20.0.0 (Phase 6): the aggregate
                 #   admission gate — multi-dimensional ATOMIC reserve
                 #   in ONE ledger transaction; the durable 0600 ledger,
                 #   leases + boot reconciliation, renew/confirm-started,
                 #   and (v20.0.0) the per-node NodeSnapshots seam +
                 #   node-tagged admission (node_id = the routing key on
                 #   the ONE authority; per-node capacity totals)
  planner/       # v19.7.2 (Phase 3) + v19.9.0 (Phase 5): the PURE
                 #   deterministic execution planner (content-hash
                 #   PlanID) + multinode.go — the network-aware
                 #   FabricPlanner (locality/transfer-cost/latency/
                 #   sensitivity gates BEFORE optimization; zero remote
                 #   views ≡ the single-node decision)
  infer_ssd/     # v19.8.0 (Phase 4): the InferSSD adapter — a distinct
                 #   SSD-backed MoE expert backend, never Docker-called,
                 #   behind the operator-owned runtime profile (§8.8:
                 #   default OFF — hardware acceptance KPK-06 OPEN)
  nodeidentity/  # v19.9.0 (Phase 5): node identity & trust — invite-based
                 #   admin-approved enrollment, per-node credentials
                 #   (sha256-digest-only), rotation grace, revocation =
                 #   QUARANTINE, the closed scope set, the replay guard
  fabric/        # v19.9.0 (Phase 5): the observational registry
                 #   (unreachable = capacity UNKNOWN, the last snapshot
                 #   retained, reservations HELD) + the PURE ReconcileNode
                 #   engine; v20.0.0 adds SnapshotFor (the §4.1 guards
                 #   under one lock — the admission seam's fabric side)
  nodeagent/     # v19.9.0 (Phase 5): the narrow typed node-agent boundary —
                 #   six operations, structurally no shell/Docker/file
                 #   access; the reservation mirror closes the rogue-start
                 #   case at the node itself
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

## 7. The Compute Fabric control plane (v19.7.1 → v20.0.0, complete)

The fabric sequence's final state (the roadmap's §4–§10, all phases
shipped; design records per phase in `docs/SCARLIX_COMPUTE_FABRIC_*.md`
+ `docs/SCARLIX_COMPUTE_FABRIC_PHASE_5_DISTRIBUTED_DESIGN.md` +
`docs/SCARLIX_COMPUTE_FABRIC_PHASE_6_CONTROL_PLANE_DESIGN.md`):

- **Observed inventory** (v19.7.0): the v2 snapshot, per-source
  provenance, serve-time freshness.
- **Model/artifact metadata** (v19.7.0): the verified manifest contract —
  declared digests are declarations; recorded verification is the
  admission gate's own prerequisite (KPK-19.7.0-P2-04, bound to the
  manifest identity since v19.8.1).
- **Deterministic planner** (v19.7.2, network-aware since v19.9.0):
  pure, side-effect-free, hard constraints BEFORE optimization, the
  PlanID content hash (NodeID rides it), the LLM output never directly
  executable (§7.6 by shape).
- **Aggregate admission** (v19.7.1; the measured contract completed
  v19.8.1): multi-dimensional ATOMIC reserve in one ledger transaction —
  GPU VRAM per UUID (exclusive), host RAM, CPU threads, KV cache
  (counted exactly once), storage with the measured floors, ports.
- **Durable reservations**: 0600 ledger, leases, boot reconciliation,
  crash recovery, ACTIVE-never-expires-by-lease (v19.8.1).
- **Typed workload lifecycle** through the Host Bridge (§5 above stands).
- **The distributed fabric** (v19.9.0): node identity & trust
  (Tailscale = transport, NEVER authorization), the observational
  registry (unreachable = capacity UNKNOWN — never recycled), the
  reconciliation engine, the node-agent boundary.
- **Node-tagged admission + the production control plane** (v20.0.0):
  `node_id` reservations against a node's VERIFIED observation in the
  ONE authoritative ledger (per-node totals — two nodes may share a GPU
  UUID); the executions push computes REAL reconciliation decisions;
  `GET /api/compute/resources` (the observed/estimated/reserved/
  allocatable/unknown/stale label contract) + `GET /api/controlplane`
  (the capability census). The operator's own surface:
  `docs/OPERATOR_RUNBOOK_v20.0.0.md`.

## 8. Known limitations (honest, current)

- **Hardware acceptance OPEN** (KPK-06, since 19.3.x): CI green is never
  hardware verification; the InferSSD runtime profile stays disabled
  until the §8.7 target-host matrix passes (the operator's act — the
  matrix ships in `docs/OPERATOR_RUNBOOK_v20.0.0.md` §5.3).
- **Real multi-host fabric acceptance OPEN** (since v19.9.0): the
  automated suites prove the contracts; a real tailnet with real nodes
  is the operator's Phase 6 acceptance matrix (runbook §5.2).
- **The CP→agent dial-out is not wired in main.go** (v20.0.0's named
  non-goal): the node agent binary, the typed client, and the mirror
  are shipped and tested, but the production dial-out configuration
  (agent endpoints + the control-plane credential distribution) is the
  operator's deployment act.
- Artifact staging (cross-node model transfer) is MODELED, not executed
  (the v19.9.0 design's §4 — the agent's start gate enforces presence).
- Storage transport for LVM/device-mapper mounts reports `unknown`
  (honest); no verified manifests ship for the real models yet (every
  model: `manifest_status: "none"`, placement-ineligible — the honest
  default).
- The content filter is keyword-level; the in-subnet lateral residual is
  inherent (documented since v19.6.5); the four experimental `:latest`
  images are time-bounded allowlisted until 2027-01-31.
- The token budget covers the `/api/ai/chat` path only (propose is
  admin-gated).
- The resource-accounting view's reserved dimension is ZERO without the
  admission gate (nothing can reserve without it — a fact, labeled
  `reservations_configured=false`).

## 9. Document status map

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
