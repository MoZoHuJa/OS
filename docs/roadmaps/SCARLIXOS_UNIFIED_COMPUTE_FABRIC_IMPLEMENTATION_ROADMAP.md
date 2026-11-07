# ScarLiXoS Unified Compute Fabric & SSD-Backed Inference Roadmap

## Architecture remediation, implementation sequence, code patterns, and release gates

**Document status:** Engineering implementation proposal\
**Repository baseline:** `MoZoHuJa/OS`, snapshot `19.6.6`\
**Baseline commit:** `96bfb2328ea759c74d4b093bf61d4ed00ded9d3d`\
**Target direction:** v19.6.7 → v20.0.0 (proposed sequence; release
numbers are planning labels, not existing repository commitments)\
**Primary scope:** Compute Fabric, Universal AI Runtime, resource
inventory and scheduling, InferSSD, SGLang Expert Pack, and future
multi-node execution.

------------------------------------------------------------------------

## 0. How to use this document

This is an implementation plan for the repository baseline above. It is
intentionally explicit about the difference between existing code and
proposed work.

-   **IMPLEMENTED / PRESENT** means the v19.6.6 snapshot contains code
    or documentation for the capability.
-   **PARTIAL / OPT-IN** means code exists, but it is disabled by
    default, has limited semantics, or is not accepted on a target host.
-   **PLANNED** means this document proposes new work. It must not be
    represented as an existing feature.
-   **HARDWARE ACCEPTANCE REQUIRED** means CI alone cannot establish
    correctness; the actual target machine and runtime must be tested.
-   Code blocks are reference patterns. Adapt names and interfaces to
    the actual package APIs before merging. They are not claims that the
    exact code already exists.

The repository is the source of truth for current behavior. Where
documentation is stale or conflicting, implementation and active
configuration must be inspected before changing behavior.

### 0.1 Non-negotiable design principles

1.  **LLM proposes; deterministic policy decides.** AI-generated plans
    never directly become host commands.
2.  **ScarliHQ never receives unrestricted host control.** No Docker
    socket mounted into ScarliHQ, no arbitrary command execution, no
    arbitrary image or path selection.
3.  **Fail closed.** Unknown health, uncertain cleanup, invalid
    identity, stale inventory, or incomplete evidence must not be
    treated as permission to execute.
4.  **Separate engine identity from execution strategy.** InferSSD is a
    backend/engine integration; SGLang Expert Pack is a SGLang
    capability/artifact mode, not a new independent provider.
5.  **Plan before reserve; reserve before start.** A workload cannot
    start unless an immutable resource assignment has been atomically
    reserved.
6.  **Release only after verified cleanup.** A successful stop request
    is not equivalent to released capacity.
7.  **Capacity is multidimensional.** GPU VRAM, host RAM, pinned RAM, KV
    cache, CPU, SSD capacity, SSD I/O, ports, and network constraints
    all matter.
8.  **Do not add VRAM capacities arithmetically across unrelated GPUs.**
    Two 16 GB GPUs do not automatically form one 32 GB memory pool.
9.  **Compatibility is evidence-based.** Do not assume InferSSD, an
    expert pack, a model checkpoint, a CUDA build, or a GPU architecture
    are compatible without explicit validation.
10. **Every phase has a rollback and an acceptance gate.** A version
    number alone is not evidence of readiness.

------------------------------------------------------------------------

# 1. Baseline assessment: v19.6.6

## 1.1 Existing architecture to preserve

The v19.6.6 repository already contains a substantial foundation:

  --------------------------------------------------------------------------------------------
  Area                    Existing baseline                  Status / limitation
  ----------------------- ---------------------------------- ---------------------------------
  GPU inventory           `scarlihq/internal/inventory` has  Present; storage inventory and
                          normalized                         capacity accounting are not yet
                          GPU/runtime/model/service/health   unified
                          structures; NVIDIA discovery       
                          includes UUID-oriented identity in 
                          the implementation path            

  Reservation             `scarlihq/internal/reservation`    Present; resource types are
                          has canonical resource IDs, atomic currently more exclusive than
                          bundles, locking, leases/TTL,      fractional/capacity based
                          owner identity, idempotency        
                          fingerprints, reconciliation       

  Allocation              `scarlihq/internal/allocation`     Present; must remain the durable
                          persists lifecycle state and       lifecycle authority
                          allocation identity                

  Workload lifecycle      `scarlihq/internal/workload`       Present; host lifecycle
                          coordinates start/stop through a   acceptance remains open
                          driver                             

  Host Bridge             `scarlihq/internal/hostbridge`     Partial/opt-in; service must
                          provides a typed Unix-socket       remain disabled until target-host
                          boundary and fixed-profile Docker  acceptance
                          executor                           

  Compute composition     `scarlihq/internal/compute`        Present; public intake and
                          composes lifecycle components      reservation semantics require
                                                             careful alignment

  Scheduler               `scarlihq/internal/scheduler`      Present; do not silently replace
                          contains an older/dry-run          it; define the new
                          scheduling path                    planner/admission contract
                                                             explicitly

  Universal AI Runtime    `scarlihq/internal/airuntime` has  Present; adding InferSSD requires
                          a closed provider enumeration for  deliberate
                          SGLang, vLLM, BeeLlama, Ollama     registry/router/prober/API/test
                                                             changes

  AI transport            `scarlihq/internal/airt` has       Present; it is a chat transport
                          adapters, health/probe/chat        foundation, not a resource-aware
                          execution and bounded failover     execution planner

  AI API                  `scarlihq/internal/api` exposes    Present; preserve proposal-only
                          opt-in AI runtime surfaces         semantics

  Agent Kernel            Agent session capability           Present; keep deterministic
                          evaluation and grant registry      capability checks in front of
                          exist                              compute intake
  --------------------------------------------------------------------------------------------

### 1.2 Existing AI stack assumptions

The v19.6.6 documentation/configuration describes the local stack
approximately as:

-   SGLang on GPU 0 as the primary inference runtime.
-   vLLM on GPU 1 as experimental.
-   BeeLlama / llama.cpp as a CPU path.
-   Ollama as a CPU fallback.
-   LiteLLM as a gateway layer.
-   Model and runtime configuration is represented in active
    Compose/YAML configuration, including `models.yaml`.

The current SGLang example uses a Qwen3-14B-AWQ model path, a 32K
context setting, `mem-fraction-static` around `0.80`, chunked prefill,
and a bounded request count. The actual deployed configuration must be
read from the target host before changing values. The example SGLang
image is pinned to a specific build; do not assume that an InferSSD fork
or Expert Pack can run in that image.

### 1.3 Important baseline limitations

The following are design constraints, not reasons to discard the current
implementation:

1.  `airuntime.Provider` is intentionally closed to four providers. A
    fifth backend cannot be added safely by adding one constant only.
2.  `inventory.Model` has basic model metadata but lacks MoE-specific
    and SSD execution metadata.
3.  There is no unified storage inventory contract equivalent to the GPU
    inventory contract.
4.  Reservations are currently primarily exclusive resource
    reservations. They do not yet represent safe fractional VRAM, RAM,
    KV-cache or SSD-I/O capacity sharing.
5.  The Host Bridge and workload executor have not passed the documented
    target-host acceptance matrix.
6.  The host executor conservatively limits active allocation per
    logical GPU and per host port. This is safe but limits concurrency.
7.  Per-workload CPU/RAM/PID limits do not by themselves provide
    aggregate admission control across all workloads.
8.  The Compute lifecycle must distinguish a reservation action from a
    start action. The current agent compute route maps the `"reserve"`
    action to `computeManager.Start`; do not expand this semantic
    ambiguity.
9.  Stop/cleanup and reservation release are separate operations. Do not
    free resources when a stop endpoint merely returns.
10. Historical architecture/GPU/runtime documents contain older version
    headers. They must be marked historical or replaced with a versioned
    canonical architecture source.

## 1.4 Commit-specific baseline notes

The baseline commit message reports remediation work including canonical
trusted Docker CIDR pools, context cancellation handling in health batch
observation, strict typed profile loading and server-side enforcement,
Compose-derived dependency inventory, model ID reconciliation, and a
stale-tree audit workflow.

Treat those as commit-reported changes. Before release, verify the exact
commit's CI checks and workflow runs in GitHub. Do not infer that
target-host NVIDIA/Docker/model acceptance has passed from unit tests or
a commit message.

------------------------------------------------------------------------

# 2. Target architecture

## 2.1 Logical architecture

``` text
                   ┌──────────────────────────────────────────┐
                   │              ScarLiX UI                   │
                   │ ScarliBar / ScarliCenter / ScarliMonitor  │
                   └─────────────────────┬────────────────────┘
                                         │ authenticated intent
                                         ▼
                   ┌──────────────────────────────────────────┐
                   │ ScarliHQ API + Agent Kernel              │
                   │ identity • capabilities • audit • policy  │
                   └─────────────────────┬────────────────────┘
                                         │ approved proposal
                                         ▼
                   ┌──────────────────────────────────────────┐
                   │ Execution Planner                         │
                   │ model requirements • capability matching │
                   │ placement plan • explainable decisions    │
                   └─────────────────────┬────────────────────┘
                                         │ candidate plan
                                         ▼
                   ┌──────────────────────────────────────────┐
                   │ Resource Admission + Reservation          │
                   │ atomic multidimensional reservation       │
                   │ leases • owner • idempotency • reconcile  │
                   └─────────────────────┬────────────────────┘
                                         │ immutable allocation
                                         ▼
                   ┌──────────────────────────────────────────┐
                   │ Workload Lifecycle Manager                │
                   │ plan → reserve → start → ready → stop     │
                   └─────────────────────┬────────────────────┘
                                         │ typed operations only
                                         ▼
                   ┌──────────────────────────────────────────┐
                   │ Host Bridge (Unix socket)                  │
                   │ fixed profiles • allowlists • audit        │
                   └─────────────────────┬────────────────────┘
                                         │ narrow executor API
                 ┌───────────────────────┼──────────────────────┐
                 ▼                       ▼                      ▼
        ┌────────────────┐     ┌──────────────────┐    ┌────────────────┐
        │ SGLang backend │     │ InferSSD backend │    │ vLLM / llama.cpp│
        │ Expert Pack    │     │ SSD-backed MoE   │    │ CPU/GPU paths  │
        └────────────────┘     └──────────────────┘    └────────────────┘
                 ▲                       ▲                      ▲
                 └───────────────────────┼──────────────────────┘
                                         │
                   ┌──────────────────────────────────────────┐
                   │ Unified Resource Inventory                │
                   │ GPU • CPU • RAM • SSD • network • models  │
                   │ runtime health • observed capacity        │
                   └──────────────────────────────────────────┘
```

The inventory is observational. The planner computes a proposed
placement. Admission validates current capacity and reserves it. The
Host Bridge performs only administrator-approved operations. No layer
should bypass the layer immediately below it.

## 2.2 Separate four concepts

Do not collapse these concepts into a single `provider` field:

1.  **Engine/backend:** SGLang, vLLM, llama.cpp, Ollama, InferSSD.
2.  **Execution strategy:** dense resident, MoE expert paging,
    SSD-backed experts, CPU offload, tensor parallel, pipeline parallel.
3.  **Artifact/storage mode:** ordinary model files, quantized
    checkpoint, SGLang Expert Pack, InferSSD expert store.
4.  **Placement:** host, GPU UUIDs, RAM budget, SSD device/path, network
    locality.

A SGLang model using an Expert Pack is still a SGLang execution. An
InferSSD model using SSD-backed experts is an InferSSD execution. Do not
register the Expert Pack as a fifth runtime provider.

## 2.3 Package ownership proposal

Keep the existing packages and introduce new packages only where a
stable responsibility exists:

``` text
scarlihq/
  internal/
    airuntime/       # backend identities, capabilities, model/runtime contracts
    airt/            # bounded transport adapters and chat/probe execution
    inventory/       # normalized observed hardware/runtime/model inventory
    storage/         # storage device discovery and I/O profiling (NEW)
    modelmeta/       # model execution metadata, MoE and artifact manifest (NEW)
    planner/         # deterministic placement and execution planning (NEW)
    admission/       # multidimensional capacity admission (NEW)
    reservation/     # durable atomic reservations, leases, ownership, reconcile
    allocation/      # allocation lifecycle state and durable identity
    workload/        # start/stop/readiness lifecycle
    hostbridge/      # typed Unix-socket boundary and fixed-profile executor
    compute/         # composition root
    api/             # authenticated, bounded HTTP surfaces
```

Do not create duplicate reservation stores or a second workload
lifecycle manager. Extend the existing packages through versioned
contracts.

------------------------------------------------------------------------

# 3. Release sequence overview

This is a proposed sequence. Adjust release labels to the repository's
actual release policy.

  -----------------------------------------------------------------------
  Proposed release        Main objective          Must not do
  ----------------------- ----------------------- -----------------------
  v19.6.7                 Architecture freeze,    Do not add InferSSD yet
                          documentation           
                          reconciliation,         
                          lifecycle semantics,    
                          Host Bridge acceptance  

  v19.7.0                 Unified resource        Do not schedule from
                          inventory, storage      unverified estimates
                          inventory, model        
                          execution metadata      

  v19.7.1                 Resource Vector,        Do not enable unsafe
                          aggregate admission,    fractional GPU sharing
                          reservation integration 

  v19.7.2                 Backend capability      Do not make LLM output
                          contracts and execution directly executable
                          planner                 

  v19.8.0                 InferSSD adapter and    Do not claim
                          SGLang Expert Pack      cross-engine pack
                          experimental support    compatibility

  v19.9.0                 Multi-node Compute      Do not trust network
                          Fabric and remote       peers without identity
                          inference               and policy

  v20.0.0                 Unified                 Do not release without
                          production-ready AI     end-to-end hardware
                          compute control plane   acceptance
  -----------------------------------------------------------------------

Parallel work: continue AI Runtime CF-4/CF-5 work, dashboards, audit
coverage, documentation and security reviews. Do not abandon those
tracks while building the new execution path.

------------------------------------------------------------------------

# 4. Phase 0 --- v19.6.7: Stabilize and establish a trusted baseline

## 4.1 Objectives

-   Establish one canonical architecture description for v19.6.6+.
-   Close semantic gaps between reservation, allocation, workload
    start/stop and release.
-   Complete target-host Host Bridge acceptance before enabling
    production execution.
-   Make current limitations explicit in API responses and UI.
-   Preserve existing security boundaries and rollback behavior.

## 4.2 Step-by-step work

### Step 0.1 --- Freeze the baseline

1.  Record the exact repository SHA.
2.  Record active Compose files, `models.yaml`, profile files and
    runtime image digests.
3.  Record Go version, kernel, Docker Engine, NVIDIA driver, CUDA
    version and GPU UUIDs on the target host.
4.  Generate the active dependency inventory from the actual Compose
    set.
5.  Run CI for the exact SHA and archive the run URL and result.
6.  Do not edit model/runtime versions during the baseline capture.

**Deliverable:** `docs/releases/v19.6.7/BASELINE.md`.

### Step 0.2 --- Reconcile documentation

Create a canonical architecture document, for example:

-   `docs/architecture/SCARLIXOS_CURRENT_ARCHITECTURE.md`
-   `docs/architecture/COMPUTE_FABRIC_CONTRACT.md`
-   `docs/architecture/AI_RUNTIME_CONTRACT.md`

For each older document: - Add a `Historical snapshot` banner and its
last-valid version, or - Update it and explicitly record the
migration. - Remove ambiguous statements that describe a planned feature
as implemented. - Keep one source of truth for package ownership and
lifecycle semantics.

### Step 0.3 --- Correct the lifecycle vocabulary

Use these distinct operations:

``` text
DISCOVER
  ↓
PLAN
  ↓
RESERVE
  ↓
START
  ↓
WAIT_READY
  ↓
RUN
  ↓
STOP_REQUESTED
  ↓
CLEANUP_VERIFIED
  ↓
RELEASE
```

Rules: - `PLAN` is side-effect-free. - `RESERVE` atomically acquires
capacity and writes a durable allocation. - `START` accepts only an
existing, valid `RESERVED` allocation. - `WAIT_READY` requires positive
health/readiness evidence. - `STOP` requests cleanup; it does not
release capacity by itself. - `RELEASE` is allowed only after trusted
cleanup verification. - A timeout or ambiguous outcome becomes
`RECONCILE_REQUIRED`; never blindly retry creation or release.

**Specific remediation:** rename or change the `"reserve"` action in
agent compute intake so that it does not call `computeManager.Start`.
Until a real reserve operation exists at that boundary, reject
`"reserve"` or route it to a distinct reservation service method. Do not
retain a misleading API action name.

### Step 0.4 --- Define one lifecycle state machine

Reference state set:

``` go
type AllocationState string

const (
    StatePlanned            AllocationState = "PLANNED"
    StateReserved           AllocationState = "RESERVED"
    StateStarting           AllocationState = "STARTING"
    StateReady              AllocationState = "READY"
    StateRunning            AllocationState = "RUNNING"
    StateStopRequested      AllocationState = "STOP_REQUESTED"
    StateCleanupRequired    AllocationState = "CLEANUP_REQUIRED"
    StateReconcileRequired  AllocationState = "RECONCILE_REQUIRED"
    StateReleased           AllocationState = "RELEASED"
    StateFailed             AllocationState = "FAILED"
)
```

Do not add a transition merely because it is convenient for one API
handler. Centralize and test allowed transitions.

``` go
var allowedTransitions = map[AllocationState]map[AllocationState]bool{
    StatePlanned:           {StateReserved: true, StateFailed: true},
    StateReserved:          {StateStarting: true, StateReleased: true, StateFailed: true},
    StateStarting:          {StateReady: true, StateCleanupRequired: true, StateReconcileRequired: true},
    StateReady:             {StateRunning: true, StateStopRequested: true},
    StateRunning:           {StateStopRequested: true, StateReconcileRequired: true},
    StateStopRequested:     {StateReleased: true, StateCleanupRequired: true, StateReconcileRequired: true},
    StateCleanupRequired:  {StateReconcileRequired: true, StateReleased: true},
    StateReconcileRequired:{StateReleased: true, StateCleanupRequired: true},
}
```

The exact transition graph must be reconciled with the existing
`internal/allocation` state model. Do not duplicate it in API code.

### Step 0.5 --- Enforce Host Bridge acceptance

Use the existing P5.1 acceptance runbook as the authoritative starting
checklist.

Required checks: - Dedicated service identity and administrator-owned
configuration. - Unix socket path and permissions verified for intended
client identity. - No Docker socket mounted into ScarliHQ. - Fixed
runtime/model/image/port/GPU/mount/network allowlists. - Digest-pinned
images; no mutable tag resolution at runtime. - Allocation ID and
immutable assignment fingerprint used for idempotency. - Duplicate Start
returns the same workload reference. - Conflicting retry is rejected. -
Stop verifies allocation ownership and actual absence. - Service restart
does not duplicate or orphan workloads. - Ambiguous cleanup persists a
reconciliation state. - Logs and durable ledger survive service
restart. - Only resources positively identified as allocation-owned can
be stopped.

**Important:** the workload bridge service must remain disabled until
this matrix passes on the intended target host. The existing
dashboard/mode bridge is a separate service and must not be repurposed.

### Step 0.6 --- Add aggregate admission gate before concurrency

Even if only one allocation per GPU is supported initially, reject a new
workload if aggregate host limits would be exceeded. Per-container
limits are not aggregate capacity management.

Until Phase 2 is complete: - Keep one active allocation per GPU. - Keep
one allocation per host port. - Keep the existing conservative
behavior. - Do not enable multi-workload GPU sharing.

### Step 0.7 --- API contract corrections

Example request shapes:

``` json
{
  "allocation_id": "alloc_01J...",
  "request_id": "req_01J..."
}
```

Do not accept caller-supplied: - Docker image names or digests, -
command lines, - arbitrary environment maps, - host paths, - GPU
UUIDs, - port numbers, - privilege flags, - arbitrary network names.

Those values must be resolved from administrator-controlled profiles and
planner output.

## 4.3 Phase 0 tests and acceptance

Run from repository root:

``` bash
cd scarlihq
gofmt -w .
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go test -race ./internal/allocation ./internal/reservation \
  ./internal/workload ./internal/hostbridge ./internal/compute
```

Target-host tests must include duplicate start, conflicting retry,
restart recovery, Docker unavailable, unauthorized socket access,
malformed requests, stop verification and cleanup failure.

**Exit criteria:** - Exact candidate SHA has green CI. - Target-host
lifecycle matrix is signed off. - No unsafe Docker socket exposure
exists. - Reservation release is gated by verified cleanup. -
Reserve/start/release semantics are unambiguous. - Canonical
architecture documents match the code.

------------------------------------------------------------------------

# 5. Phase 1 --- v19.7.0: Unified Resource Inventory and Model Execution Metadata

## 5.1 Objectives

Build one normalized, read-only view of all resources required for
planning: - GPUs, - CPU, - host RAM and pinned RAM, - storage devices
and filesystem paths, - network interfaces and remote nodes, -
runtimes, - model artifacts, - health and observed utilization.

Inventory must not execute workloads or reserve capacity.

## 5.2 Storage inventory package

Add `scarlihq/internal/storage`.

Responsibilities: - Enumerate block devices and mount points using safe,
read-only system sources. - Identify devices by stable hardware identity
where available. - Record filesystem, capacity, free space and mount
options. - Record device interface (NVMe/SATA/USB/network). - Measure
read performance only through an explicit diagnostic command or
controlled benchmark. - Record benchmark provenance, timestamp and test
profile. - Never infer that a benchmark result is a guaranteed sustained
rate.

Suggested types:

``` go
package storage

import "time"

type DeviceKind string

const (
    DeviceNVMe    DeviceKind = "nvme"
    DeviceSATA    DeviceKind = "sata"
    DeviceUSB     DeviceKind = "usb"
    DeviceNetwork DeviceKind = "network"
    DeviceUnknown DeviceKind = "unknown"
)

type ReadProfile struct {
    SequentialReadMBps float64   `json:"sequential_read_mbps,omitempty"`
    RandomReadIOPS     float64   `json:"random_read_iops,omitempty"`
    RandomReadP95Ms    float64   `json:"random_read_p95_ms,omitempty"`
    BlockSizeBytes     int       `json:"block_size_bytes,omitempty"`
    QueueDepth         int       `json:"queue_depth,omitempty"`
    MeasuredAt         time.Time `json:"measured_at,omitempty"`
    ProfileID          string    `json:"profile_id,omitempty"`
}

type Device struct {
    ID             string      `json:"id"`
    StableID       string      `json:"stable_id,omitempty"`
    Model          string      `json:"model,omitempty"`
    Kind           DeviceKind  `json:"kind"`
    CapacityBytes  uint64      `json:"capacity_bytes"`
    AvailableBytes uint64      `json:"available_bytes"`
    Filesystem     string      `json:"filesystem,omitempty"`
    MountPoints    []string    `json:"mount_points"`
    ReadOnly       bool        `json:"read_only"`
    TemperatureC   *float64    `json:"temperature_c,omitempty"`
    ReadProfile    ReadProfile `json:"read_profile,omitempty"`
    Healthy        bool        `json:"healthy"`
    ObservedAt     time.Time   `json:"observed_at"`
}
```

Implementation rules: 1. Discovery is read-only. 2. Never run `fio`
automatically during normal boot or routine inventory refresh. 3. An
operator-triggered benchmark must have a bounded duration, bounded test
file size and explicit target path. 4. Do not benchmark a mounted
production model store without approval. 5. Do not store secrets or
serial numbers in public inventory responses unless required and
access-controlled. 6. Mark unknown measurements as unknown, not zero.

## 5.3 Extend normalized host inventory

Preserve frozen public fields already documented in
`internal/inventory/types.go`. Add optional fields or a versioned v2
contract; do not silently rename existing JSON keys.

Example v2 envelope:

``` go
type InventorySnapshotV2 struct {
    SchemaVersion string            `json:"schema_version"`
    SnapshotID    string            `json:"snapshot_id"`
    ObservedAt    time.Time         `json:"observed_at"`
    Host          HostResources     `json:"host"`
    GPUs          []GPU              `json:"gpus"`
    Storage       []storage.Device   `json:"storage"`
    Runtimes      []Runtime          `json:"runtimes"`
    Models        []ModelExecution   `json:"models"`
    Health        []Health           `json:"health"`
}
```

Host resources:

``` go
type HostResources struct {
    CPUThreads       int    `json:"cpu_threads"`
    CPUModel         string `json:"cpu_model,omitempty"`
    RAMTotalBytes    uint64 `json:"ram_total_bytes"`
    RAMAvailableBytes uint64 `json:"ram_available_bytes"`
    RAMPinnedBytes   uint64 `json:"ram_pinned_bytes,omitempty"`
    SwapTotalBytes   uint64 `json:"swap_total_bytes,omitempty"`
    SwapFreeBytes    uint64 `json:"swap_free_bytes,omitempty"`
}
```

Do not treat swap as normal model memory. It can be reported but should
not be admitted as a substitute for RAM unless a backend has an
explicitly tested swap strategy.

## 5.4 Model execution metadata

Add `scarlihq/internal/modelmeta`.

Existing model fields such as ID, path, format, quantization, parameter
count, context length, estimated VRAM, capabilities and supported
runtimes should remain available.

Add optional execution metadata:

``` go
type Architecture string

const (
    ArchitectureDense Architecture = "dense"
    ArchitectureMoE   Architecture = "moe"
    ArchitectureOther Architecture = "other"
)

type ModelExecution struct {
    ID                  string       `json:"id"`
    Revision            string       `json:"revision"`
    CheckpointSHA256    string       `json:"checkpoint_sha256"`
    Architecture        Architecture `json:"architecture"`
    TotalParameters     string       `json:"total_parameters,omitempty"`
    ActiveParameters    string       `json:"active_parameters,omitempty"`
    Quantization        string       `json:"quantization"`
    Format              string       `json:"format"`
    ContextLimit        int          `json:"context_limit"`
    KVCacheBytesPerToken uint64      `json:"kv_cache_bytes_per_token,omitempty"`
    DenseFootprintBytes uint64       `json:"dense_footprint_bytes,omitempty"`
    ExpertCount         int          `json:"expert_count,omitempty"`
    ExpertsPerToken     int          `json:"experts_per_token,omitempty"`
    ExpertBytes         uint64       `json:"expert_bytes,omitempty"`
    SharedBytes         uint64       `json:"shared_bytes,omitempty"`
    SupportedBackends  []string     `json:"supported_backends"`
    RequiredFeatures   []string     `json:"required_features,omitempty"`
}
```

Important: - Total parameters and active parameters are not
interchangeable. - Active parameters per token do not describe total
checkpoint storage. - KV cache depends on architecture, context,
batch/concurrency, precision and runtime implementation. - Metadata must
come from a trusted manifest or verified model metadata, not from an LLM
guess. - Checkpoint fingerprint must bind an expert pack to the exact
model revision.

## 5.5 Model manifest example

``` yaml
schema_version: "scarlix.model/v2"
id: "example-moe-model"
revision: "hf-revision-or-local-build-id"
checkpoint:
  format: "safetensors"
  quantization: "awq"
  sha256: "REPLACE_WITH_VERIFIED_CHECKPOINT_HASH"

architecture:
  kind: "moe"
  total_parameters: "XXB"
  active_parameters: "XB"
  expert_count: 0
  experts_per_token: 0
  expert_bytes: 0
  shared_bytes: 0

runtime:
  context_limit: 32768
  kv_cache:
    precision: "runtime-defined"
    bytes_per_token_estimate: 0
  supported_backends:
    - "sglang"
    - "infer_ssd"

artifacts:
  expert_pack:
    enabled: false
    format: ""
    version: ""
    sha256: ""
    compatible_backend: ""

storage:
  model_path: "/var/lib/scarlix/models/example-moe-model"
  expert_path: ""
  preferred_device_id: ""
```

Zero values above are placeholders, not valid capacity estimates. A
manifest with required unknown values must not be used for automatic
placement.

## 5.6 Inventory API and freshness

Proposed read-only endpoints: - `GET /api/inventory/v2` -
`GET /api/inventory/v2/storage` - `GET /api/inventory/v2/models`

Every snapshot must include: - schema version, - snapshot ID, -
observation timestamp, - source/probe status, - per-component health, -
stale/unknown markers.

Planner policy must reject or downgrade stale data. Do not reuse old
free-VRAM values as current capacity.

**Exit criteria:** - Inventory is read-only and schema-versioned. -
Storage devices and model artifacts have stable identities. - Model
manifests include verified checkpoint fingerprints. - Storage benchmark
is opt-in and bounded. - Unknown metrics remain unknown. - Existing v1
contracts continue to pass compatibility tests.

------------------------------------------------------------------------

# 6. Phase 2 --- v19.7.1: Resource Vector, Aggregate Admission and Reservation

## 6.1 Objectives

Introduce a common resource vector that can describe a workload's
requirements and a node's available capacity.

Resource dimensions: - GPU VRAM per GPU UUID, - GPU compute share or
exclusive-device requirement, - host RAM, - pinned RAM, - CPU threads, -
KV-cache budget, - SSD capacity, - SSD read bandwidth / random IOPS /
latency class, - host ports, - network bandwidth or locality
constraints.

Do not model the whole machine as one scalar score.

## 6.2 Resource vector types

``` go
type ResourceVector struct {
    GPU            []GPURequest `json:"gpu,omitempty"`
    RAMBytes       uint64       `json:"ram_bytes,omitempty"`
    PinnedRAMBytes uint64       `json:"pinned_ram_bytes,omitempty"`
    CPUThreads     float64      `json:"cpu_threads,omitempty"`
    KVCacheBytes   uint64       `json:"kv_cache_bytes,omitempty"`
    Storage        []StorageRequest `json:"storage,omitempty"`
    Ports          []int        `json:"ports,omitempty"`
}

type GPURequest struct {
    GPUUUID              string `json:"gpu_uuid,omitempty"`
    VRAMBytes            uint64 `json:"vram_bytes"`
    Exclusive            bool   `json:"exclusive"`
    MinimumComputeShare  float64 `json:"minimum_compute_share,omitempty"`
}

type StorageRequest struct {
    DeviceID             string  `json:"device_id,omitempty"`
    MinimumFreeBytes     uint64  `json:"minimum_free_bytes"`
    MinimumReadMBps      float64 `json:"minimum_read_mbps,omitempty"`
    MinimumRandomReadIOPS float64 `json:"minimum_random_read_iops,omitempty"`
    MaximumP95LatencyMs  float64 `json:"maximum_p95_latency_ms,omitempty"`
    PathClass            string  `json:"path_class,omitempty"`
}
```

The exact numeric type and precision should match the project's
serialization and accounting policy. Prefer integer bytes and explicitly
defined units. Never mix MB, MiB and bytes without conversion helpers.

## 6.3 Capacity model

Each resource dimension needs: - observed capacity, - currently reserved
capacity, - safety headroom, - allocatable capacity, - freshness
timestamp, - confidence/source.

For a scalar resource:

``` text
allocatable = max(0, observed_capacity - reserved - safety_headroom)
```

For GPU VRAM, compute capacity per physical GPU UUID. Do not aggregate
unrelated GPUs.

For storage bandwidth, use a conservative policy: - measured sustainable
value, - configured safety fraction, - existing admitted demand, -
workload's minimum requirement. Do not sum theoretical SSD interface
bandwidth as if it were measured model-serving throughput.

## 6.4 Admission interface

``` go
type AdmissionRequest struct {
    RequestID      string
    Principal      string
    ModelID        string
    BackendID      string
    Requirements   ResourceVector
    IdempotencyKey string
}

type AdmissionDecision struct {
    Allowed       bool
    ReasonCode    string
    Explanation   string
    SelectedNode  string
    Assignment    ResourceAssignment
    SnapshotID    string
    PolicyVersion string
}
```

Admission must be deterministic for identical inputs and identical
inventory/policy snapshots.

### Atomic reserve pattern

1.  Validate identity and policy.
2.  Verify inventory snapshot freshness.
3.  Verify backend/model compatibility.
4.  Compute a candidate assignment.
5.  Re-read or lock the capacity ledger.
6.  Check all dimensions atomically.
7.  Persist reservation bundle with immutable assignment and request
    fingerprint.
8.  Return allocation ID and assignment.
9.  If any dimension fails, reserve nothing.

Pseudo-code:

``` go
func (m *Manager) Reserve(
    ctx context.Context,
    req AdmissionRequest,
) (Allocation, error) {
    if err := validateAdmissionRequest(req); err != nil {
        return Allocation{}, err
    }

    snapshot, err := m.inventory.Current(ctx)
    if err != nil {
        return Allocation{}, ErrInventoryUnavailable
    }
    if snapshot.IsStale(m.maxInventoryAge) {
        return Allocation{}, ErrInventoryStale
    }

    plan, err := m.planner.Plan(snapshot, req)
    if err != nil {
        return Allocation{}, err
    }

    return m.reservations.ReserveBundle(ctx, ReserveBundleRequest{
        Owner:            req.Principal,
        IdempotencyKey:   req.IdempotencyKey,
        RequestFingerprint: fingerprint(req, plan),
        Assignment:       plan.Assignment,
        Resources:        plan.ResourceClaims,
    })
}
```

The function names above are proposed. Integrate with the existing
`internal/reservation` manager rather than creating a parallel store.

## 6.5 GPU sharing policy

Initial safe policy: - Exclusive GPU assignment remains the default. -
Fractional VRAM is not automatically safe merely because free VRAM
appears available. - A runtime must explicitly declare whether
concurrent model processes are supported. - Admission must include
runtime reserve overhead, CUDA graphs, workspace, allocator
fragmentation, KV cache and safety margin. - GPU sharing requires a
measured profile and per-backend policy. - A GPU reset, driver error or
unexpected VRAM use invalidates affected reservations and triggers
reconciliation.

Do not advertise MIG-style isolation on GPUs that do not support it. Do
not imply that CUDA process isolation guarantees memory isolation.

## 6.6 Lease and release behavior

-   Leases protect against abandoned reservations.
-   Lease expiry must not release a resource while a workload may still
    be running.
-   Expiry triggers reconciliation, not blind release.
-   Only trusted cleanup verification may transition `RELEASING` to
    `RELEASED`.
-   The reconciler must verify workload identity and allocation labels.
-   Idempotency fingerprint must include model revision, backend, GPU
    UUIDs, storage artifact identity and port assignment.

## 6.7 Admission tests

-   Two concurrent requests competing for the same GPU.
-   Multiple requests competing for RAM.
-   SSD capacity exhausted.
-   SSD I/O requirement cannot be met.
-   Inventory stale during admission.
-   Same idempotency key and same fingerprint.
-   Same idempotency key and conflicting fingerprint.
-   Crash after ledger write and before API response.
-   Crash after workload creation and before state update.
-   Lease expiry while workload is still active.
-   Cleanup failure and delayed reconciliation.

**Exit criteria:** - All dimensions are checked atomically. - No
overcommit occurs under concurrent admission. - Reservations survive
process restart. - Release requires positive cleanup evidence. -
Existing single-GPU exclusive mode remains the safe default.

------------------------------------------------------------------------

# 7. Phase 3 --- v19.7.2: Backend capability contract and deterministic execution planner

## 7.1 Objectives

Make the Universal AI Runtime extensible without turning it into an
unsafe dynamic plugin system.

Keep the existing pure `internal/airuntime` package network-free. Keep
transport I/O in `internal/airt` or dedicated backend adapters. The
planner should consume capabilities and metadata; it must not launch
containers.

## 7.2 Backend contract

``` go
type BackendID string

const (
    BackendSGLang   BackendID = "sglang"
    BackendVLLM     BackendID = "vllm"
    BackendBeeLlama BackendID = "beellama"
    BackendOllama   BackendID = "ollama"
    BackendInferSSD BackendID = "infer_ssd"
)

type BackendCapabilities struct {
    BackendID          BackendID `json:"backend_id"`
    ModelFormats       []string  `json:"model_formats"`
    Architectures      []string  `json:"architectures"`
    Features           []string  `json:"features"`
    SupportsMoE        bool      `json:"supports_moe"`
    SupportsSSDExperts bool      `json:"supports_ssd_experts"`
    SupportsExpertPack bool      `json:"supports_expert_pack"`
    SupportsMultiGPU   bool      `json:"supports_multi_gpu"`
    SupportsCPUOffload bool      `json:"supports_cpu_offload"`
    HealthRequired     bool      `json:"health_required"`
    Experimental       bool      `json:"experimental"`
}
```

The backend registry must be administrator-owned and validated at
startup. Do not allow an HTTP caller or LLM to register arbitrary
backends.

## 7.3 Separate provider registry from capability matrix

The existing provider enumeration is intentionally closed. When
extending it: 1. Add a backend identity. 2. Update the closed list and
validation. 3. Update capability metadata. 4. Update health probing and
health semantics. 5. Update adapter construction. 6. Update
failover/selection policy. 7. Update chat/request budgets and limits. 8.
Update API schemas and MCP contracts where exposed. 9. Update
configuration validation. 10. Update unit, integration, race and
compatibility tests. 11. Update documentation and threat model.

Do not merely add a string constant.

## 7.4 Planner contract

``` go
type ExecutionRequest struct {
    ModelID       string
    ModelRevision string
    Task          string
    ContextTokens int
    MaxConcurrent int
    LatencyClass  string
    Principal     string
}

type ExecutionPlan struct {
    PlanID          string
    BackendID       BackendID
    Strategy        string
    NodeID          string
    GPUUUIDs        []string
    ModelArtifactID string
    ResourceVector  ResourceVector
    RequiredImages  []PinnedImage
    ConfigProfileID string
    Explanation     []DecisionReason
    ExpiresAt       time.Time
}
```

Planner invariants: - Same input and same snapshot/policy produce the
same plan. - Every selected backend explicitly supports the model format
and architecture. - Every selected artifact has a verified
fingerprint. - Every image is digest-pinned and allowlisted. - Every
path maps to a configured model root. - Every plan expires and is
revalidated at admission. - Planner output is a proposal; admission is
still required.

## 7.5 Scoring and tie-breaking

A scoring function may rank eligible candidates, but hard constraints
must be applied first.

Hard constraints: - model/backend compatibility, - GPU architecture
compatibility, - required runtime features, - available capacity, -
storage requirements, - identity and policy, - network trust, -
health/freshness.

Only after hard constraints pass should the planner optimize for
latency, throughput, power, SSD wear, cost or locality.

Tie-breaking must be deterministic, e.g.: 1. highest policy priority, 2.
lowest predicted queue delay, 3. lowest resource fragmentation, 4.
stable lexical node/backend ID.

Never let a model-generated explanation alter the actual deterministic
selection.

## 7.6 AI proposal integration

The AI Runtime may propose: - model choice, - context length, - runtime
preference, - resource profile, - user-visible tradeoffs.

It must not: - write reservation records, - select arbitrary host
paths, - create Docker containers, - bypass policy, - override
allowlists, - release allocations.

Operator or policy approval must produce a typed request to the
deterministic planner/admission path.

**Exit criteria:** - Backend capabilities are explicit and validated. -
Planner is pure/deterministic and side-effect-free. - New backends do
not bypass admission or Host Bridge. - AI output remains
proposal-only. - Closed registry, tests, API and docs are updated
together.

------------------------------------------------------------------------

# 8. Phase 4 --- v19.8.0: InferSSD and SGLang Expert Pack

## 8.1 Correct conceptual split

### InferSSD

Treat InferSSD as a distinct inference backend/engine integration for
SSD-backed MoE expert execution. It needs its own: - backend adapter, -
runtime profile, - health probe, - model compatibility declaration, -
storage requirements, - resource estimation, - lifecycle and cleanup
tests.

### SGLang Expert Pack

Treat SGLang Expert Pack as a SGLang-specific artifact/capability mode.
It is not a separate inference engine and must not automatically become
a separate provider.

A model can therefore declare: - backend: `sglang`, - execution
strategy: `moe_expert_pack`, - artifact: a verified SGLang Expert
Pack, - storage location: an approved SSD path.

## 8.2 Compatibility gates before coding

Do not assume compatibility between: - the current SGLang image and an
Expert Pack, - a specific InferSSD fork and a particular model
checkpoint, - CUDA versions, - Blackwell `sm_120` and a prebuilt
extension, - an Expert Pack format and an arbitrary checkpoint revision.

For each candidate combination, record:

  Dimension      Required evidence
  -------------- ------------------------------------------------
  Engine         Exact repository, commit and release
  Container      Exact image digest
  CUDA           Exact runtime/toolkit version
  GPU            Vendor, model, compute capability, driver
  Model          Repository/revision and checkpoint hash
  Architecture   Dense/MoE, expert count and routing metadata
  Quantization   Format and quantization details
  Artifact       Pack format, version and checksum
  Storage        Filesystem, device ID, read profile
  Runtime        Supported launch arguments and health endpoint

If any required compatibility fact is unknown, mark the profile
experimental and disable production scheduling.

## 8.3 InferSSD adapter structure

Suggested package:

``` text
scarlihq/internal/backends/
  infer_ssd/
    adapter.go
    capabilities.go
    config.go
    health.go
    manifest.go
    requirements.go
    adapter_test.go
    integration_test.go
```

Example adapter boundary:

``` go
type BackendAdapter interface {
    ID() airuntime.BackendID
    Capabilities() airuntime.BackendCapabilities
    ValidateModel(model modelmeta.ModelExecution) error
    Estimate(model modelmeta.ModelExecution, req ExecutionRequest) (ResourceVector, error)
    BuildProfile(plan ExecutionPlan) (ApprovedRuntimeProfile, error)
    Probe(ctx context.Context, target ApprovedRuntimeProfile) HealthResult
}
```

The adapter must not call Docker directly. It returns a validated
profile for the workload/Host Bridge path.

## 8.4 Runtime profile example

This is a schema illustration. Values must be populated with verified,
digest-pinned values for the actual tested build.

``` yaml
schema_version: "scarlix.runtime-profile/v1"
profile_id: "infer-ssd-moe-experimental"
backend_id: "infer_ssd"
enabled: false
experimental: true

image:
  reference: "registry.example/infer-ssd@sha256:REPLACE_WITH_VERIFIED_DIGEST"

compatibility:
  model_manifest: "example-moe-model"
  checkpoint_sha256: "REPLACE_WITH_VERIFIED_CHECKPOINT_HASH"
  cuda: "VERIFIED_VERSION"
  driver_minimum: "VERIFIED_DRIVER"
  gpu_compute_capabilities:
    - "VERIFIED_CAPABILITY"

execution:
  strategy: "ssd_backed_moe"
  context_limit: 8192
  max_concurrent_requests: 1

resources:
  gpu:
    mode: "exclusive"
    vram_reserve_bytes: 0
  host_ram_bytes: 0
  pinned_ram_bytes: 0
  cpu_threads: 0
  kv_cache_bytes: 0
  storage:
    device_id: "storage.nvme.verified-id"
    model_path: "/var/lib/scarlix/models/example"
    expert_path: "/var/lib/scarlix/experts/example"
    minimum_free_bytes: 0
    minimum_read_mbps: 0
    minimum_random_read_iops: 0
    maximum_p95_latency_ms: 0

network:
  network_profile: "scarlix-compute-internal"
  published_ports: []

health:
  type: "http"
  path: "/health"
  startup_timeout_seconds: 600
```

Never enable a profile containing placeholder values. The configuration
loader must reject them.

## 8.5 SGLang Expert Pack manifest

``` yaml
schema_version: "scarlix.artifact/v1"
artifact_id: "example-model-sglang-expert-pack"
artifact_type: "sglang_expert_pack"
backend_id: "sglang"

model:
  id: "example-moe-model"
  revision: "VERIFIED_REVISION"
  checkpoint_sha256: "VERIFIED_CHECKPOINT_HASH"

pack:
  format: "VERIFIED_FORMAT"
  version: "VERIFIED_VERSION"
  sha256: "VERIFIED_PACK_HASH"
  total_bytes: 0

compatibility:
  sglang_version: "VERIFIED_VERSION"
  container_digest: "sha256:VERIFIED_DIGEST"
  cuda_version: "VERIFIED_VERSION"
  gpu_compute_capabilities:
    - "VERIFIED_CAPABILITY"
```

The manifest must bind the pack to the exact checkpoint. Reject
mismatches.

## 8.6 SSD performance qualification

SSD-backed expert paging is sensitive to random reads and latency, not
only advertised sequential MB/s.

Measure at least: - sequential read throughput, - random read IOPS at
relevant block sizes, - p50/p95/p99 latency, - queue depth
sensitivity, - thermal throttling, - sustained performance after cache
exhaustion, - filesystem and mount behavior, - contention when other
workloads use the device.

Benchmark procedure: 1. Select an approved scratch file on the intended
SSD. 2. Bound test size and duration. 3. Record device model, firmware,
filesystem, mount options and temperature. 4. Run a repeatable read
profile. 5. Repeat under idle and representative contention. 6. Record
raw output and tool version. 7. Derive a conservative allocatable I/O
budget. 8. Delete only the benchmark scratch file created by the test.

Do not run uncontrolled benchmarks against the live model store.

## 8.7 Model serving acceptance matrix

Test separately: - model load time, - first-token latency, -
steady-state tokens/sec, - prompt processing throughput, - long-context
behavior, - expert cache hit/miss behavior, - SSD read amplification, -
GPU VRAM peak, - host RAM peak, - pinned RAM peak, - CPU saturation, -
thermal behavior, - request concurrency, - quality/regression checks, -
graceful stop and restart, - crash recovery.

Compare at least: 1. Baseline SGLang without Expert Pack where
supported. 2. SGLang with Expert Pack. 3. InferSSD. 4. CPU/GPU fallback
if applicable.

Use the same checkpoint, prompt suite, context length and sampling
settings where technically possible. Record differences rather than
presenting one benchmark as universally superior.

## 8.8 Enablement policy

Initial status must be: - `enabled: false`, - `experimental: true`, -
one allocation per GPU, - one active workload per SSD profile unless
contention tests justify more, - no automatic fallback to an
incompatible backend.

Enable only after all compatibility and target-host tests pass.

**Exit criteria:** - InferSSD is registered as a backend, not a generic
storage mode. - SGLang Expert Pack is modeled as a SGLang
artifact/capability. - Exact compatibility manifests and hashes are
verified. - SSD I/O admission uses measured conservative capacity. -
Runtime profiles are digest-pinned and fixed. - Target hardware
acceptance passes and rollback is documented.

------------------------------------------------------------------------

# 9. Phase 5 --- v19.9.0: Distributed Compute Fabric

## 9.1 Objectives

Extend the resource model from one host to multiple trusted nodes
without weakening the local security model.

Each node must publish: - stable node identity, - authenticated identity
proof, - inventory snapshot and freshness, - backend capabilities, -
model/artifact availability, - resource capacity and current
reservations, - network locality/latency class, - health and maintenance
state.

## 9.2 Node identity and trust

Do not trust a node because it is reachable on LAN or Tailscale.

Requirements: - mutual authentication between control plane and node
agent, - administrator-approved node enrollment, - per-node credentials
and rotation, - explicit node roles, - authorization scopes, - signed or
authenticated inventory, - replay protection and request IDs, - audit
records for placement and execution, - revocation and quarantine
behavior.

Tailscale may provide network connectivity, but network membership alone
is not the application authorization model.

## 9.3 Node agent boundary

The node agent should expose a narrow typed API: - inventory snapshot, -
health, - capability report, - reserve allocation, - start approved
profile, - stop owned allocation, - reconcile allocation.

It must not expose arbitrary shell, arbitrary Docker API, arbitrary file
access or arbitrary port publishing.

## 9.4 Network-aware scheduling

Add network constraints to the planner: - model artifact locality, -
request origin, - inter-node latency, - transfer size, - expected
network throughput, - data sensitivity, - availability and failure
domains.

Prefer local execution when the expected transfer cost exceeds the
benefit of remote compute.

## 9.5 Failure handling

-   Node becomes unreachable: mark capacity unknown; do not reuse
    reservations immediately.
-   Node reappears: reconcile allocations before admitting new
    workloads.
-   Credential revoked: quarantine node and stop new work; cleanup only
    through trusted recovery.
-   Inventory stale: reject or constrain placement.
-   Remote stop ambiguous: preserve reservation until node
    reconciliation proves cleanup.
-   Partition: prevent two controllers from independently assigning the
    same resources.

A single authoritative reservation ledger or a carefully designed
consensus/lease protocol is required. Do not run independent schedulers
with unsynchronized local ledgers.

**Exit criteria:** - Node enrollment and revocation tested. - Remote
allocation ownership is authenticated. - Partition and restart behavior
tested. - Remote reservations reconcile safely. - Network-aware planner
is deterministic. - Tailscale is treated as transport, not as sole
authorization.

------------------------------------------------------------------------

# 10. Phase 6 --- v20.0.0: Unified production control plane

## 10.1 Expected capabilities

A v20.0.0 candidate should provide: - versioned inventory across
GPU/CPU/RAM/storage/network, - verified model and artifact registry, -
backend capability registry, - deterministic execution planner, -
multidimensional aggregate admission, - durable reservations and
leases, - typed workload lifecycle through Host Bridge, - authenticated
single- and multi-node execution, - InferSSD and SGLang Expert Pack
behind tested profiles, - auditable AI proposal workflow, - dashboard
visibility for plans, allocations, resources and failures, - recovery
and rollback procedures.

## 10.2 UI responsibilities

### ScarliCenter

-   approve/reject execution proposals,
-   view resource plans and reasons,
-   view active reservations,
-   request stop/release,
-   manage trusted runtime profiles,
-   inspect audit history.

### ScarliMonitor

-   display observed GPU/CPU/RAM/SSD metrics,
-   show inventory freshness,
-   show runtime and backend health,
-   show allocation lifecycle,
-   distinguish observed, reserved, estimated and allocatable values.

### ScarliBar

-   accept user intent,
-   show concise plan summaries,
-   request confirmation where required,
-   never bypass the policy/admission path.

The UI must label values clearly: - **Observed:** read from
hardware/runtime. - **Estimated:** derived from model metadata. -
**Reserved:** held by an allocation. - **Allocatable:** capacity
remaining after policy headroom. - **Unknown/Stale:** not safe for
automatic admission.

## 10.3 Release acceptance

A production release must include: - exact source SHA, - green CI and
race tests, - security review, - target-host acceptance report, -
model/backend compatibility matrix, - SSD performance report, -
benchmark methodology and raw results, - failure/recovery evidence, -
migration and rollback guide, - updated canonical architecture docs, -
operator runbook, - release notes listing known limitations.

------------------------------------------------------------------------

# 11. API and data contract patterns

## 11.1 Plan endpoint

``` http
POST /api/compute/plan
Authorization: Bearer <token>
Content-Type: application/json
```

``` json
{
  "model_id": "example-moe-model",
  "model_revision": "verified-revision",
  "task": "chat",
  "context_tokens": 8192,
  "max_concurrent": 1,
  "latency_class": "interactive"
}
```

Response is a proposal, not an allocation:

``` json
{
  "plan_id": "plan_...",
  "status": "proposed",
  "backend_id": "infer_ssd",
  "strategy": "ssd_backed_moe",
  "resource_vector": {},
  "snapshot_id": "snapshot_...",
  "expires_at": "RFC3339_TIMESTAMP",
  "reasons": []
}
```

## 11.2 Reserve endpoint

``` http
POST /api/compute/reservations
Authorization: Bearer <token>
```

``` json
{
  "plan_id": "plan_...",
  "idempotency_key": "client-generated-stable-key"
}
```

The server must revalidate plan expiry, policy, inventory freshness and
capacity. A plan is not a reservation.

## 11.3 Start endpoint

``` http
POST /api/compute/allocations/{allocation_id}/start
Authorization: Bearer <token>
```

Only an allocation in a valid reserved state can start. The client
cannot override the immutable assignment.

## 11.4 Stop and release

``` http
POST /api/compute/allocations/{allocation_id}/stop
POST /api/compute/allocations/{allocation_id}/reconcile
```

Stop requests cleanup. Reconciliation verifies outcome. Release is a
trusted internal transition after cleanup proof. Do not expose a public
endpoint that lets a caller assert cleanup succeeded.

## 11.5 Error contract

Use stable machine-readable error codes:

``` json
{
  "error": {
    "code": "INVENTORY_STALE",
    "message": "The resource snapshot is too old for admission.",
    "retryable": true,
    "request_id": "req_..."
  }
}
```

Suggested codes: - `IDENTITY_REQUIRED` - `POLICY_DENIED` -
`MODEL_UNKNOWN` - `MODEL_REVISION_MISMATCH` - `BACKEND_UNSUPPORTED` -
`INVENTORY_UNAVAILABLE` - `INVENTORY_STALE` - `CAPACITY_INSUFFICIENT` -
`STORAGE_PROFILE_UNQUALIFIED` - `PLAN_EXPIRED` - `ALLOCATION_CONFLICT` -
`ALLOCATION_NOT_OWNED` - `LIFECYCLE_RECONCILIATION_REQUIRED` -
`HOST_BRIDGE_UNAVAILABLE`

Do not leak secrets, filesystem internals or raw Docker errors in public
messages. Preserve detailed diagnostic context in access-controlled
logs.

------------------------------------------------------------------------

# 12. Configuration and schema evolution

## 12.1 Configuration principles

-   Strict YAML decoding; reject unknown fields where the schema is
    intended to be closed.
-   Version every public configuration schema.
-   Validate all paths against administrator-owned roots.
-   Require image digests, not mutable tags.
-   Require explicit CPU, RAM, PID, GPU, shared memory, ports, network
    and health settings.
-   Reject placeholders.
-   Separate operator config from user requests.
-   Keep experimental profiles disabled by default.

## 12.2 Example top-level configuration

``` yaml
schema_version: "scarlix.compute/v1"

policy:
  default_gpu_mode: "exclusive"
  inventory_max_age_seconds: 15
  vram_headroom_fraction: 0.15
  ram_headroom_fraction: 0.20
  allow_experimental_backends: false

storage:
  model_roots:
    - "/var/lib/scarlix/models"
  expert_roots:
    - "/var/lib/scarlix/experts"
  benchmark:
    enabled: false
    max_duration_seconds: 60
    max_file_bytes: 1073741824

backends:
  sglang:
    enabled: true
    experimental: false
  vllm:
    enabled: true
    experimental: true
  beellama:
    enabled: true
    experimental: false
  ollama:
    enabled: true
    experimental: false
  infer_ssd:
    enabled: false
    experimental: true

host_bridge:
  enabled: false
  socket: "/run/scarlix-workload-bridge/bridge.sock"
  approved_network: "scarlix-compute"
```

This is an illustrative future schema. It must not be copied into
production without matching loader code, schema tests and actual
verified values.

------------------------------------------------------------------------

# 13. Security and threat model

## 13.1 Trust boundaries

  -----------------------------------------------------------------------
  Component               Trust level             Allowed responsibility
  ----------------------- ----------------------- -----------------------
  LLM / agent proposal    Untrusted input         Suggest model and
                                                  execution preferences

  ScarliBar/UI            Authenticated user      Submit intent and
                          surface                 display decisions

  Agent Kernel            Policy enforcement      Evaluate session
                                                  capabilities and
                                                  principal

  Planner                 Deterministic but       Produce candidate plan
                          non-authoritative       

  Admission/reservation   Authoritative state     Reserve capacity and
                                                  ownership

  Host Bridge             Privileged narrow       Execute fixed, approved
                          executor                profiles

  Runtime container       Workload isolation      Run inference workload
                          boundary                

  Inventory collector     Read-only observer      Report actual observed
                                                  state

  Remote node             Untrusted until         Publish authenticated
                          enrolled                state and accept scoped
                                                  operations
  -----------------------------------------------------------------------

## 13.2 Mandatory controls

-   Least-privilege service accounts.
-   Unix socket permissions restricted to intended identities.
-   Docker API treated as host-root-equivalent.
-   No arbitrary host command execution.
-   No arbitrary mounts or path traversal.
-   No caller-controlled runtime image or command.
-   Digest-pinned images and verified model/artifact hashes.
-   Request size limits and strict JSON decoding.
-   Authentication, authorization and owner checks.
-   Durable idempotency and reconciliation.
-   Audit records for allow and deny decisions.
-   Secrets redacted from logs.
-   Fail-closed behavior on audit write failure for privileged actions.
-   Rate limits on expensive plan/probe operations.
-   Explicit opt-in for benchmarks and experimental backends.

## 13.3 Abuse cases to test

1.  Caller tries to start another principal's allocation.
2.  Caller reuses allocation ID with a different model/GPU.
3.  Caller sends unknown JSON fields or multiple JSON objects.
4.  Caller attempts path traversal in model or expert path.
5.  Caller attempts arbitrary image, command, environment or port.
6.  Stale inventory reports free VRAM that is no longer free.
7.  Runtime starts but client times out.
8.  Host Bridge restarts while workload is running.
9.  Docker is unavailable during stop.
10. Malicious or compromised node reports fabricated capacity.
11. Model manifest points to a different checkpoint than expert pack.
12. Audit store is unavailable during a privileged operation.

------------------------------------------------------------------------

# 14. Testing strategy

## 14.1 Test layers

### Unit tests

-   schema validation,
-   resource vector arithmetic,
-   model metadata validation,
-   capability matching,
-   deterministic planning,
-   transition legality,
-   idempotency fingerprints,
-   canonical resource IDs.

### Property and fuzz tests

-   no negative capacity,
-   no integer overflow in byte conversion,
-   no partial reservation after failed bundle admission,
-   invalid state transitions always rejected,
-   unknown backend/config values rejected,
-   malformed manifests never accepted.

### Concurrency and race tests

-   concurrent reservations on same GPU,
-   concurrent requests on shared SSD budget,
-   reserve/release races,
-   duplicate start/stop,
-   reconciler racing with stop,
-   inventory refresh racing with admission.

### Integration tests

-   planner → admission → reservation,
-   reservation → workload manager,
-   workload manager → Host Bridge,
-   Host Bridge → runtime container,
-   health probe → READY state,
-   stop → cleanup verifier → release.

### Hardware acceptance

-   exact target GPUs and driver,
-   actual model files,
-   actual storage device,
-   actual Docker/runtime images,
-   actual network and socket permissions,
-   thermal and sustained load behavior.

## 14.2 Suggested test commands

``` bash
cd scarlihq

gofmt -w .
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go test -race ./internal/allocation ./internal/reservation \
  ./internal/workload ./internal/hostbridge ./internal/compute \
  ./internal/inventory ./internal/airuntime ./internal/airt
```

Add new packages to race tests when they contain shared mutable state.

## 14.3 Benchmark report format

Every benchmark report must include: - hardware and firmware, -
OS/kernel, - driver/CUDA, - source SHA, - image digest, - model revision
and checkpoint hash, - quantization, - context length, -
batch/concurrency, - prompt/output token counts, - warm-up policy, - run
count, - p50/p95/p99, - throughput, - peak GPU VRAM, - peak host RAM, -
SSD read profile, - temperature and power, - errors and failed requests.

Do not compare results with different model revisions, context lengths
or quantization as if they were equivalent.

------------------------------------------------------------------------

# 15. Migration and rollout plan

## 15.1 Compatibility strategy

1.  Keep existing v1 inventory fields stable.
2.  Introduce v2 inventory as a parallel schema.
3.  Add new model metadata fields as optional until manifests are
    complete.
4.  Keep current SGLang/vLLM/BeeLlama/Ollama profiles unchanged during
    inventory rollout.
5.  Keep exclusive GPU scheduling as default.
6.  Add the new planner in dry-run mode first.
7.  Compare dry-run plans with operator decisions.
8.  Enable admission for one approved profile.
9.  Add InferSSD as disabled experimental backend.
10. Enable one model/checkpoint/host combination only after acceptance.
11. Add multi-node scheduling only after single-node reconciliation is
    proven.

## 15.2 Rollout flags

Suggested feature flags: - `SCARLIHQ_INVENTORY_V2_ENABLED` -
`SCARLIHQ_STORAGE_INVENTORY_ENABLED` - `SCARLIHQ_PLANNER_DRY_RUN` -
`SCARLIHQ_ADMISSION_ENABLED` - `SCARLIHQ_INFER_SSD_ENABLED` -
`SCARLIHQ_SGLANG_EXPERT_PACK_ENABLED` -
`SCARLIHQ_DISTRIBUTED_COMPUTE_ENABLED`

Flags must default to the safe state. A flag must not bypass
authentication, policy, admission or Host Bridge checks.

## 15.3 Rollback

-   Stop accepting new plans/reservations first.
-   Preserve reservation/allocation ledgers.
-   Reconcile active workloads before disabling lifecycle components.
-   Disable only the newly introduced backend/profile.
-   Do not remove model/artifact data during rollback.
-   Do not broadly delete containers by image or name.
-   Keep existing CPU/GPU fallback paths intact.
-   Revert schema consumers only after compatibility checks.
-   Record unresolved allocations and manually review before releasing
    capacity.

------------------------------------------------------------------------

# 16. Repository work breakdown and suggested ownership

  -----------------------------------------------------------------------------------------
  Workstream              Primary package/docs            Main deliverable
  ----------------------- ------------------------------- ---------------------------------
  Baseline and docs       `docs/architecture`,            Canonical v19.6.7 architecture
                          `docs/releases`                 and evidence

  Lifecycle semantics     `allocation`, `reservation`,    Correct
                          `workload`, `api`               plan/reserve/start/stop/release
                                                          flow

  Host execution          `hostbridge`, systemd packaging Accepted narrow executor

  Storage inventory       `internal/storage`              Read-only storage snapshot and
                                                          benchmark profile

  Model metadata          `internal/modelmeta`, model     Verified dense/MoE/artifact
                          manifests                       metadata

  Resource accounting     `internal/admission`,           Atomic multidimensional admission
                          `reservation`                   

  Backend registry        `airuntime`, `airt`, `backends` Explicit backend capability
                                                          contracts

  Planner                 `internal/planner`              Deterministic explainable
                                                          placement

  InferSSD                `internal/backends/infer_ssd`   Experimental adapter and tested
                                                          runtime profile

  Expert Pack             SGLang profile/artifact         Verified SGLang-specific artifact
                          manifests                       path

  Distributed fabric      node agent / compute packages   Authenticated remote node
                                                          execution

  UI                      ScarliCenter / ScarliMonitor /  Approval, visibility and audit
                          ScarliBar                       views
  -----------------------------------------------------------------------------------------

------------------------------------------------------------------------

# 17. Definition of Done by phase

## v19.6.7 --- Stabilization

-   [ ] Canonical architecture and lifecycle docs published.
-   [ ] Reserve/start/release semantics corrected.
-   [ ] Host Bridge target-host acceptance passed.
-   [ ] Docker socket and service identity reviewed.
-   [ ] Aggregate resource guard remains conservative.
-   [ ] CI and race tests green for exact SHA.
-   [ ] Rollback evidence recorded.

## v19.7.0 --- Inventory

-   [ ] Storage inventory package added.
-   [ ] Host RAM/CPU metrics normalized.
-   [ ] Model metadata schema extended compatibly.
-   [ ] Checkpoint hashes verified.
-   [ ] Inventory freshness and unknown values represented.
-   [ ] Storage benchmarks opt-in and bounded.
-   [ ] v1 compatibility tests pass.

## v19.7.1 --- Admission

-   [ ] Resource Vector implemented.
-   [ ] Aggregate admission is atomic.
-   [ ] GPU capacity tracked per UUID.
-   [ ] RAM/KV/SSD capacity accounted.
-   [ ] Exclusive GPU mode remains default.
-   [ ] Reconciliation and lease expiry tested.
-   [ ] No release without verified cleanup.

## v19.7.2 --- Planner and backends

-   [ ] Backend capability contract implemented.
-   [ ] Closed registry updated comprehensively.
-   [ ] Planner deterministic and side-effect-free.
-   [ ] Plan expiry and revalidation enforced.
-   [ ] AI remains proposal-only.
-   [ ] API/MCP/config/docs/tests updated together.

## v19.8.0 --- SSD-backed inference

-   [ ] InferSSD compatibility matrix completed.
-   [ ] Exact model and pack hashes verified.
-   [ ] InferSSD adapter integrated behind Host Bridge.
-   [ ] SGLang Expert Pack modeled as SGLang artifact/capability.
-   [ ] SSD performance qualified under contention.
-   [ ] Comparative benchmark and quality suite passed.
-   [ ] Experimental flags default off.

## v19.9.0 --- Distributed fabric

-   [ ] Node enrollment/authentication implemented.
-   [ ] Per-node identity and scoped authorization enforced.
-   [ ] Remote inventory authenticated and freshness-aware.
-   [ ] Partition/restart reconciliation tested.
-   [ ] Network-aware planning deterministic.
-   [ ] No split-brain reservation behavior.

## v20.0.0 --- Production control plane

-   [ ] End-to-end lifecycle proven on target hardware.
-   [ ] Single- and multi-node acceptance passed.
-   [ ] Security review complete.
-   [ ] UI distinguishes observed/estimated/reserved/allocatable.
-   [ ] Operator, recovery and rollback docs complete.
-   [ ] Release evidence attached to exact SHA.

------------------------------------------------------------------------

# 18. Recommended immediate implementation order

Do not start by writing the InferSSD adapter. The safest and fastest
path is:

1.  **Freeze and verify v19.6.6 baseline.**
2.  **Fix reserve/start/release semantics and complete Host Bridge
    acceptance.**
3.  **Publish canonical architecture and mark stale documents
    historical.**
4.  **Add storage inventory and versioned model execution metadata.**
5.  **Implement Resource Vector and aggregate admission on top of the
    existing reservation manager.**
6.  **Add deterministic backend capability contracts and execution
    planner.**
7.  **Integrate InferSSD as an experimental backend.**
8.  **Add SGLang Expert Pack as a SGLang artifact/capability mode.**
9.  **Benchmark SSD-backed execution on the real target host.**
10. **Extend the same contracts to authenticated remote nodes.**
11. **Close UI, audit, recovery and v20.0.0 production acceptance.**

This order prevents the project from accumulating another runtime that
cannot be safely scheduled, observed, admitted, stopped or recovered.

------------------------------------------------------------------------

# 19. Open decisions that must be resolved before implementation

These are genuine engineering decisions; this document does not pretend
they are already settled.

1.  Which exact InferSSD repository/fork and commit will be supported?
2.  Which exact SGLang Expert Pack format and SGLang release will be
    supported?
3.  Which model/checkpoint is the first supported MoE test target?
4.  Which SSD is the first qualified expert store, and what
    filesystem/mount options will be used?
5.  Will initial GPU placement remain exclusive for all backends?
    Recommended: yes.
6.  What inventory freshness window is acceptable for local admission
    and remote admission?
7.  Which component is the authoritative allocation/reservation store
    after distributed execution is introduced?
8.  What identity mechanism maps human users, agents and remote nodes to
    principals?
9.  Which performance targets define acceptable first-token latency and
    throughput?
10. Which UI surface owns approval and which surface owns operational
    monitoring?

Each decision must have an owner, evidence, date and recorded rationale.

------------------------------------------------------------------------

# 20. Final architectural recommendation

The central investment should be the **Resource and Execution Control
Plane**, not a standalone InferSSD integration.

ScarLiXoS already has important building blocks: inventory, reservation,
allocation, workload lifecycle, Host Bridge, agent policy and a
Universal AI Runtime foundation. The next step is to connect them
through strict contracts:

**Observed Inventory → Model Requirements → Deterministic Plan → Atomic
Admission → Durable Allocation → Typed Host Execution → Verified Health
→ Verified Cleanup → Resource Release.**

InferSSD and SGLang Expert Pack then become controlled execution options
inside this architecture. They do not create a parallel control plane.

The result should be a system that can explain not only *which* model it
selected, but also: - why that backend is compatible, - which exact
checkpoint and artifact will run, - which GPU UUID and SSD device will
be used, - how much VRAM/RAM/KV/CPU/storage/I/O is reserved, - which
policy approved the placement, - how health is verified, - how cleanup
is proven, - and why the resources are safe to reuse.

That is the foundation required for ScarLiXoS to evolve from a
collection of local AI runtimes into a reliable, auditable,
multi-resource and eventually multi-node AI operating system.

------------------------------------------------------------------------

## Appendix A --- Suggested commit slicing

Prefer small, reviewable commits over one large implementation branch.

1.  `docs(v19.6.7): reconcile compute fabric architecture and lifecycle`
2.  `fix(compute): separate reservation from workload start`
3.  `test(hostbridge): complete target-host lifecycle acceptance`
4.  `feat(inventory): add versioned host and storage inventory`
5.  `feat(modelmeta): add verified MoE and artifact manifests`
6.  `feat(admission): add multidimensional resource vectors`
7.  `feat(reservation): atomically reserve resource capacity`
8.  `feat(airuntime): add backend capability contracts`
9.  `feat(planner): add deterministic execution planning`
10. `feat(infer-ssd): add experimental backend adapter`
11. `feat(sglang): add expert-pack artifact profile`
12. `feat(compute): add authenticated remote node protocol`
13. `feat(ui): expose plans allocations and resource accounting`
14. `docs(v20): publish production acceptance and operations guide`

Every commit should include tests and documentation for the behavior it
changes.

## Appendix B --- Pull request checklist

-   [ ] Scope matches one phase and one coherent behavior.
-   [ ] Existing package ownership is respected.
-   [ ] No duplicate inventory, reservation or lifecycle authority is
    introduced.
-   [ ] All new configuration is strictly validated.
-   [ ] All images are digest-pinned.
-   [ ] All model/artifact hashes are verified.
-   [ ] No arbitrary host path, command, image, mount or environment
    input is accepted.
-   [ ] All state transitions are centralized and tested.
-   [ ] Idempotency and restart recovery are tested.
-   [ ] Cleanup is verified before release.
-   [ ] Race tests cover shared mutable state.
-   [ ] Logs and errors do not expose secrets.
-   [ ] Experimental functionality defaults off.
-   [ ] CI evidence corresponds to the exact candidate SHA.
-   [ ] Hardware acceptance is explicitly marked passed, pending or not
    applicable.
-   [ ] Rollback instructions are included.
-   [ ] Canonical docs and changelog are updated.

## Appendix C --- Evidence record template

``` yaml
schema_version: "scarlix.acceptance-evidence/v1"
release: "v19.x"
commit_sha: "FULL_SHA"
host:
  os: ""
  kernel: ""
  cpu: ""
  ram_bytes: 0
  gpus:
    - name: ""
      uuid: ""
      driver: ""
      compute_capability: ""
  storage:
    - device_id: ""
      model: ""
      firmware: ""
      filesystem: ""
ci:
  workflow_url: ""
  result: "pending"
tests:
  unit: "pending"
  race: "pending"
  integration: "pending"
  host_lifecycle: "pending"
  model_quality: "pending"
  performance: "pending"
security_review:
  result: "pending"
  reviewer: ""
known_limitations: []
rollback_tested: false
accepted_by: ""
accepted_at: ""
```

**End of document.**
