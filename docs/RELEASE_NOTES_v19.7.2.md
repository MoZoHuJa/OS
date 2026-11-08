# ScarLiX OS v19.7.2 — Compute Fabric Phase 3: Backend Capability Contracts + Deterministic Execution Planner

**Release:** v19.7.2
**Commit:** (recorded at packaging — the release-notes genre: the hash lands with the tag)
**Baseline commit (pre-change):** `de4896c` (v19.7.1 release-notes hash record)
**Date:** 2026-11-08
**Scope:** Compute Fabric **Phase 3 — Backend Capability Contract and
Deterministic Execution Planner** (roadmap §7 of
`docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`,
the in-repo implementation plan to v20.0.0)

The release sequence toward v20 continues: v19.6.8 (stabilization —
DONE) → v19.7.0 (unified resource inventory — DONE) → v19.7.1
(resource vector + aggregate admission — DONE) → **v19.7.2 (this
release: backend capability contracts + the deterministic execution
planner)** → v19.8.0 (InferSSD + SGLang Expert Pack, experimental) →
v19.9.0 (distributed fabric) → v20.0.0 (production control plane).
Release labels ≥ v19.8.0 remain planning labels, not commitments.

## Scope

This release executes Phase 3 of the Unified Compute Fabric roadmap:
the **backend capability contract** (the administrator-owned, closed,
validated-at-construction registry that separates the CAPABILITY tier
from the ADAPTER tier) and the **deterministic execution planner**
(the pure, side-effect-free proposal engine that joins the capability
rows with the live inventory, the verified manifests and the
operator-owned policy). The release's must-not-do is honored **by the
request's shape**: *do not make LLM output directly executable* —
`ExecutionRequest` carries no selection-steering field, the strict
decode refuses any injected field, the selection order belongs only to
the operator-owned policy, and the plan's explanation is GENERATED,
never consumed from model output.

## Changes

### 1. The backend capability contract (`scarlihq/internal/airuntime/backends.go` — NEW)

Roadmap §7.2/§7.3, field-for-field:

- **`BackendID`** — the capability-tier identity: sglang, vllm,
  beellama, ollama (the four real runtimes, aligned 1:1 with the
  `Provider` adapter enumeration) plus **infer_ssd** — roadmap §7.2's
  own const block; the declared-future backend whose capability row
  exists so a manifest declaring it intersects HONESTLY while the
  placement × runtime join refuses every v19.7.2 infer_ssd plan
  (v19.8.0 owns the adapter, §7.3's steps 4–8).
- **`BackendCapabilities`** — the registry row: model formats,
  architectures, features, SupportsMoE / SupportsSSDExperts /
  SupportsExpertPack / SupportsMultiGPU / SupportsCPUOffload,
  HealthRequired, Experimental, and the documented v19.7.2 addition
  `RequiresGPU` (the typed execution-class fact the vector derivation
  needs — GPU-resident vs. host-RAM-resident weights; a magic feature
  token or a tier-number heuristic would be weaker typing for the
  same claim). The rows are honest about the deployed stack: vllm
  carries `Experimental: true`; the CPU tiers execute gguf (dense and
  MoE — llama.cpp-family engines run MoE ggufs fully resident);
  sglang's `SupportsExpertPack` stays FALSE until v19.8.0's
  pack-engine validation evidence (claiming pack support without
  evidence would be a phantom).
- **The closed registry** — administrator-owned and **validated at
  construction** (`ValidateBackendRegistry`: every enumeration member
  has exactly one row, every row's ID is a valid member, no empty
  format/architecture lists — an inconsistent registry is a STARTUP
  error, never a per-request surprise). **There is NO registration
  surface**: no HTTP route, no env var, no file can add a backend row
  (roadmap §7.2's "Do not allow an HTTP caller or LLM to register
  arbitrary backends" — enforced structurally). Extending the registry
  is §7.3's **eleven-step** reviewed release act (identity → closed
  list + validation → capability metadata → health probing → adapter
  construction → failover policy → budgets → API schemas → config
  validation → tests → documentation), encoded at the registry: never
  merely a string constant.
- **The alignment** — `BackendForProvider` / `ProviderForBackend`
  map the four runtimes 1:1; `ProviderForBackend(infer_ssd)` is the
  honest NAMED error ("no runtime adapter in v19.7.2 — v19.8.0 owns
  the InferSSD adapter"). `BackendTier` mirrors the v19.0.6
  release-locked failover order; infer_ssd carries the
  declared-future priority 99 (never preferred while adapter-less).

### 2. The deterministic execution planner (`scarlihq/internal/planner` — NEW)

Roadmap §7.4/§7.5/§7.6:

- **Pure and side-effect-free (§7.1):** no network, no exec, no file
  I/O anywhere in the planning path (`planner.go` — comment-stripped
  scan clean; `production.go` is the ONLY environment seam: the env
  parsing of `SCARLIHQ_PLANNER_IMAGES` and the reuse of the admission
  production snapshot + model sources — one truth about the machine,
  two gates reading it). The planner never launches containers.
- **Deterministic (§7.4):** the `PlanID` is the SHA-256 **content
  hash** of the decision tuple (policy version + backend + strategy +
  node + GPUs + the artifact binding + the vector + the images + the
  config profile + the ask's provenance) — identical inputs →
  identical ID, 20× proven in the regression suite; the identity IS
  the decision, the expiry clock rides separately. Candidate
  evaluation iterates the DECLARED backends in sorted order; every
  tie-break is lexicographic.
- **Hard constraints BEFORE optimization (§7.5):** model/backend
  compatibility (format + architecture + required features against
  the CLOSED capability rows), placement × runtime presence (declared
  AND observed running + healthy — the join that refuses a
  declared-but-not-running backend and every v19.7.2 infer_ssd plan),
  artifact verification (VERIFIED records only — a declared digest is
  a DECLARATION, KPK-19.7.0-P2-04's rule; a stale verification is
  absent verification), snapshot freshness (HARD refusal,
  KPK-19.7.0-P2-01's rule), revision match, the context limit (an
  unservable context is a phantom capability — refused BY NAME),
  capacity (admission-shaped: observed free minus the same
  safety-headroom fractions; single-GPU — a multi-GPU-sized demand is
  the named `plan-gpu-insufficient`, never a guessed split), host RAM
  for the CPU classes, and the image contract (digest-pinned AND
  operator-declared).
- **The tie-break sequence (§7.5):** (1) policy priority — the
  operator-owned `PriorityBackends` (the default mirrors the v19.0.6
  failover order), (2) lowest observed GPU utilization — the honest
  v19.7.2 queue-delay PROXY (a real queue model arrives with workload
  metrics, v19.8.0+; the proxy is observed data, deterministic for
  the same snapshot), (3) tightest fit (highest demand/allocatable
  ratio = lowest fragmentation), (4) stable lexical backend + GPU
  UUID.
- **The closed refusal vocabulary:** 15 machine-readable `Reason*`
  codes (`plan-request-invalid`, `plan-snapshot-unavailable`,
  `plan-snapshot-stale`, `plan-model-unknown`, `plan-model-ineligible`,
  `plan-model-unverified`, `plan-model-revision-mismatch`,
  `plan-context-exceeds-limit`, `plan-backend-no-candidates`,
  `plan-gpu-unavailable`, `plan-gpu-insufficient`,
  `plan-ram-insufficient`, `plan-image-unconfigured`,
  `plan-image-not-digest-pinned`, `ok`). When exactly one declared
  backend reaches the downstream evaluation, its named refusal IS the
  answer (the common deployed shape); multiple failures aggregate
  honestly into `plan-backend-no-candidates` with the full trail.
- **The strategy vocabulary — closed and RESIDENT-ONLY:**
  `dense-resident` (GPU VRAM), `cpu-resident` (host RAM),
  `moe-gpu-resident` (shared + ALL experts in VRAM — the deployed
  sglang/vllm behavior), `moe-cpu-resident` (llama.cpp-family). Expert
  paging and SSD-backed experts are v19.8.0's InferSSD / Expert Pack
  territory — **no v19.7.2 plan carries a storage demand** (the
  weights are already on disk; the artifact verification proved them
  there).
- **The vector derivation:** VRAM = weights + KV (GPU classes), RAM =
  checkpoint + KV + the honest-0 runtime overhead (CPU classes), KV =
  context tokens × the manifest's bytes-per-token estimate (unknown →
  the named `kv-cache-estimate-unknown` reason and a 0 KV demand —
  runtimes size the KV cache themselves, PlacementEligible's
  documented rule), CPU threads from the policy default, GPU
  assignment exclusive per UUID (the v19.7.1+ safe default). A dense
  model with an unknown weights footprint is refused BY NAME (a
  parameter label is a label, not bytes — never schedule from
  unverified estimates).
- **The plan is a PROPOSAL (§7.4's closing invariant):** nothing is
  reserved, bound, or launched; every plan EXPIRES (TTL 5 minutes)
  and is revalidated at admission time. The generated explanation
  carries the `plan-is-proposal` reason and the full decision trail
  (machine-readable `DecisionReason` codes + the numbers).

### 3. The §7.6 boundary — LLM output never becomes directly executable

- `ExecutionRequest` has **NO selection-steering field**: no
  preferred_backend, no explanation echo, no policy override (asserted
  at the source level by smoke check_39; the API's strict decode
  refuses any injected field as 400 — an LLM-shaped payload carrying
  `preferred_backend` / `explanation` / `policy` fields is a named
  refusal, proven in the HTTP suite).
- The principal is stamped **SERVER-SIDE** from the resolved token
  identity (`PrincipalFromContext`) — a caller cannot attribute its
  plan to someone else.
- The selection order belongs ONLY to the operator-owned
  `PlannerPolicy` — the request structurally cannot steer it.
- The plan's `Explanation` is GENERATED by the planner; a
  model-generated explanation can never enter (they are outputs,
  never inputs — roadmap §7.5's closing rule, enforced by shape).

### 4. The API surface (`scarlihq/internal/api/plan.go` — NEW)

`POST /api/compute/plan` (compute the plan) + `GET /api/compute/plan`
(the operator view: the planner policy + the closed capability
registry), behind the standard Bearer **READ gate** — the planner
RESERVES NOTHING (a strictly weaker act than the ADMIN-gated,
separately-opt-in admission; observation-grade, like the inventory).
The POST response is ALWAYS a result object — a refusal is **200 +
`selected:false` + the machine-readable `reason_code`** (the planner
working, never an HTTP error); malformed JSON and unknown fields are
400; unsupported methods are 405 BY NAME. main.go wires the planner
UNCONDITIONALLY (like the read-only inventory): construction validates
the closed capability registry and fail-closes on a malformed
`SCARLIHQ_PLANNER_IMAGES` declaration.

### 5. The honest defaults (the operator's territory, never guessed)

- **The image registry ships EMPTY**: no digest-pinned image
  references ship with the repository — every plan refuses
  `plan-image-unconfigured` until the operator declares them through
  `SCARLIHQ_PLANNER_IMAGES` (backend=reference pairs, comma-separated,
  parsed fail-closed at construction). The same honesty as v19.7.1's
  "no verified manifests ship yet".
- **The runtime-RAM overhead is the honest 0**: no measured
  runtime-RAM profile exists in v19.7.1/19.7.2 — the operator sets a
  measured value or the plan proposes zero (a DECLARED unknown, not a
  guess; admission still enforces the requested RAM at reserve time).
- **`ConfigProfileID` ships as the declared absence** (""): no v19.7.2
  profile registry exists — never a guessed profile.

## Tests

- **`internal/airuntime/backends_test.go`** (7 tests): the closed
  enumeration (5 members, roadmap §7.2's own const block), the
  validated registry (1:1 with the enumeration, no empty capability
  lists, the honest rows: vllm experimental, the CPU gguf tiers, the
  declared-future infer_ssd row with its v19.8.0 marker), the unknown
  slug refusal (capabilities never guessed or synthesized), the copy
  discipline (the registry is immutable through the returned rows),
  the Provider↔BackendID alignment (infer_ssd's honest named error),
  the tiers, the deterministic sorted registry view.
- **`internal/planner/planner_test.go`** (26 tests): determinism
  (identical inputs → identical PlanID, 20×; different asks →
  different IDs), the golden path (the full plan contract: strategy,
  NodeID=local, the single-GPU exclusive assignment, the
  admission-shaped vector with VRAM = weights + KV, the pinned image,
  the expiry, the artifact binding, the generated explanation with
  the proposal invariant, the snapshot + policy provenance), the CPU
  execution class (the whole checkpoint + KV resident in host RAM, no
  GPU demand), the MoE-resident rule (ALL experts in VRAM, NO storage
  demand — the deployed behavior, paging is v19.8.0), the full
  hard-constraint matrix (stale snapshot HARD, snapshot unavailable,
  unknown model, unverified model, stale verification, revision
  mismatch, context exceeds limit, dense footprint unknown, ineligible
  manifest, request invalid ×4, presence join — vllm declared but not
  running AND infer_ssd declared-future refused by the same join,
  format mismatch, GPU insufficient with the multi-GPU policy named, GPU
  unavailable, RAM insufficient, image unconfigured, image not
  digest-pinned), the tie-break sequence (the policy priority decides
  between eligible backends + the REVERSED policy selects the other —
  the order is operator-owned, structurally unreachable from the
  request; the utilization tie-break beats the lower UUID; the
  tightest fit beats the roomier GPU), the §7.6 boundary (the request
  carries no steering field — asserted by shape; the explanation is
  generated with the planner's own codes), the KV-unknown honesty, the
  construction discipline (nil sources refused, malformed policies
  refused: duplicate priorities, empty references, zero TTL), the
  experimental-backend visibility.
- **`internal/api/plan_test.go`** (9 tests): 503-before-configuration,
  401 auth, the 400 strict decode INCLUDING the LLM-shaped steering
  payloads (preferred_backend / explanation / policy fields are named
  refusals — the §7.6 boundary at the decode), the 405 method guard,
  the GET registry view (5 sorted rows + the policy), the golden path
  (the full plan contract + the server-side household principal
  stamp), refusal-is-data (200 + selected=false + the named
  reason_code, no plan object), HTTP-level determinism (identical
  POSTs → identical PlanIDs), request-invalid-is-data.

## Verification

- `gofmt` clean; `go vet ./...` clean.
- `go test ./...` — **all 29 packages OK** (28 + the new planner).
- `go test -race` (planner + airuntime + api) — clean.
- Registry classification 68/68 (unchanged).
- `smoke --offline` — **39 checks, 0 FAIL** (2 benign WARNs: shellcheck
  absent in this environment, the offline registry skip) — the new
  check_39 with THREE verified negative controls:
  1. removing the READ gate from the plan route (bare
     `h.planRoute`) FAILs check_39 ("must sit behind h.auth");
  2. removing the `@sha256:` digest-pin enforcement from
     `evaluateCapacity` FAILs check_39 (the stripped-source marker
     vanishes with the code);
  3. replacing the content-hash PlanID with a random identity FAILs
     check_39 (`sha256.Sum256` / `planContentHash` markers).
- check_28 evolved (current-release README surface → v19.7.2: banner,
  badge, the version line, the changelog row, What's New; the v19.7.1
  surface stays as the permanent record) + check_37 evolved (the
  current-release VERSION guard moved with the release — 19.7.2 in
  VERSION + the five-place fallback discipline; the v19.7.0 record
  assertions stay as history).

## Known limitations (named, not hidden)

- **Hardware acceptance stays OPEN** (KPK-06): CI green ≠ hardware
  verified; the target-host acceptance matrix remains the operator's
  recorded act.
- **No verified manifests ship for the real models yet** — every plan
  for a real model refuses `plan-model-unverified` until an operator
  records verifications (the honest default, unchanged since v19.7.0).
- **The image registry ships EMPTY** — every plan refuses
  `plan-image-unconfigured` until the operator declares digest-pinned
  references through `SCARLIHQ_PLANNER_IMAGES` (digest pinning needs
  network pulls the offline repository cannot do — the operator pins
  or the plan refuses; the same discipline as the stale-tree audit's
  pin allowlist).
- **The runtime-RAM overhead is the honest 0** — no measured
  runtime-RAM profile exists; the plan proposes zero (admission still
  enforces the requested RAM at reserve time).
- **The queue-delay proxy is OBSERVED GPU utilization** — a real queue
  model arrives with workload metrics (v19.8.0+); the proxy is
  deterministic observed data, never a fabricated prediction.
- **v19.7.2 plans are single-node and single-GPU** — multi-GPU splits
  need measured per-GPU overhead profiles (v19.8.0+), and the
  multi-node contract is v19.9.0's (`NodeID` pins to "local").
- **The admission → planner revalidation join** (a live GPU-health ×
  reservation reconciler — named in the v19.7.1 release notes as
  "v19.7.2+ work") remains future work: this release delivers the
  roadmap §7 contract; the reconciler needs the workload metrics
  surface that does not exist yet.
- **Expert paging / SSD-backed experts are NOT in any v19.7.2 plan**
  (the strategies are resident-only, honestly) — v19.8.0 owns them
  with the InferSSD adapter and the SGLang Expert Pack validation
  evidence.
- **The AI-proposal → typed-request convenience wiring** (an accepted
  CF-3 proposal auto-filling an ExecutionRequest draft for the
  operator) is deliberately NOT in this release: the operator types or
  reviews the request; the boundary is the request's shape.

---

*'LLM navrhuje, politika rozhoduje'* — the capability rows are honest,
the plan is deterministic, the proposal is expiry-bound, and the pen
stays with the operator.
