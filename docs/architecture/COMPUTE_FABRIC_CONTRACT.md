# ScarLiX OS — Compute Fabric Contract (v19.6.8)

> **Status:** CANONICAL — the lifecycle vocabulary, state machine and API
> contract of the Compute Fabric control packages, as implemented in
> v19.6.8. Source of truth: `scarlihq/internal/{allocation,reservation,
> workload,hostbridge,compute,api}`. Phase 2's domain guide is subsumed
> here; the per-phase engineering docs remain as history.

------------------------------------------------------------------------

## 1. The lifecycle vocabulary

Nine distinct operations — no two may collapse into one name:

``` text
DISCOVER → PLAN → RESERVE → START → WAIT_READY → RUN
        → STOP_REQUESTED → CLEANUP_VERIFIED → RELEASE
```

| Operation | Owner | Side effects | Notes |
|---|---|---|---|
| DISCOVER | inventory / registry | none (read-only observation) | GPU/runtime/model/service/health |
| PLAN | scheduler (dry-run) | none | Side-effect-free by construction; the future deterministic planner (roadmap v19.7.2) keeps this property |
| RESERVE | reservation service (`cmd/scarlix-reservation`) | durable atomic bundle | Canonical resource IDs, leases/TTL, owner identity, idempotency fingerprint; governor stores, flock-serialized |
| START | workload manager | driver start via Host Bridge | Accepts ONLY an existing `RESERVED` allocation |
| WAIT_READY | workload manager | none (health/readiness evidence) | Positive evidence required; uncertainty fails closed |
| RUN | workload manager | none | READY → RUNNING transition recorded |
| STOP_REQUESTED | workload manager | durable stop request | Recorded BEFORE the driver call; does NOT release capacity |
| CLEANUP_VERIFIED | workload manager + driver | verified cleanup evidence | `CleanupVerified = true`; the reservation bundle stays RELEASING |
| RELEASE | reservation reconciliation | bundle release, final RELEASED | Only after verified cleanup; never caller-asserted |

**The v19.6.8 correction:** the agent compute intake
(`POST /api/agent/sessions/compute`) formerly labeled its workload-start
operation `"reserve"` — it called `computeManager.Start` on an allocation
the reservation service had already reserved (the documented semantic
gap, roadmap §1.3-8). As of v19.6.8 the actions are **`start`** and **`release`**; the `"reserve"` spelling is rejected by name (400) with a pointer to this contract. The kernel capability `compute.reserve`
(grants.json compatibility) still gates the start action — the capability
NAME is historical; the audit type for start actions is the new honest
`compute.start`.

## 2. The state machine (frozen + the v19.6.8 additive amendment)

Authority: `internal/allocation/transitions.go` — the ONLY place
transitions are decided. The table is FROZEN since v19.2.2; v19.6.8 adds
two states (an addition the frozen contract explicitly permits), renames
nothing, removes nothing:

``` text
REQUESTED → VALIDATING → PLANNED → RESERVING → RESERVED
RESERVED → STARTING → READY → RUNNING
READY/RUNNING/STARTING → STOP_REQUESTED          (v19.6.8)
STOP_REQUESTED → RELEASING | CLEANUP_REQUIRED | RECONCILE_REQUIRED | FAILED | CANCELLED
RELEASING → RELEASED (CleanupVerified) | CLEANUP_REQUIRED | FAILED | RECONCILE_REQUIRED
CLEANUP_REQUIRED → RELEASING | RELEASED (CleanupVerified)
RECONCILE_REQUIRED → RELEASING | CLEANUP_REQUIRED | FAILED | CANCELLED   (v19.6.8)
FAILED/CANCELLED → RELEASING | CLEANUP_REQUIRED
RELEASED — terminal
```

State semantics:

- **STOP_REQUESTED** (v19.6.8) — a stop has been requested for a live
  workload (READY/RUNNING/STARTING) and the driver stop has not yet been
  verified. Durable BEFORE the driver call: a crash between request and
  outcome leaves an honest marker; the retry path accepts
  STOP_REQUESTED and RELEASING idempotently.
- **CLEANUP_REQUIRED** — cleanup is affirmatively needed: an explicit
  stop failure (the workload may still exist) or an uncertain start (a
  workload may exist externally).
- **RECONCILE_REQUIRED** (v19.6.8) — an operation's outcome is AMBIGUOUS
  (the driver answered but could not verify). A reconciler must decide;
  NEVER a blind retry. The reservation release scan
  (`ListReleasingAllocations`) covers RELEASING, CLEANUP_REQUIRED,
  FAILED, CANCELLED and RECONCILE_REQUIRED — an ambiguous allocation is
  release-lifecycle work and must never strand.
- **RELEASED** — terminal; reachable only with `CleanupVerified == true`
  (KPK-01 record-level guard on EVERY transition into RELEASED).

Manager.Stop outcome mapping (v19.6.8):

| Driver outcome | Live-workload path | Already-down path (FAILED/CANCELLED/CLEANUP_REQUIRED) |
|---|---|---|
| verified | STOP_REQUESTED → RELEASING, `CleanupVerified=true` | RELEASING, `CleanupVerified=true` |
| explicit failure (`stopErr != nil`) | STOP_REQUESTED → CLEANUP_REQUIRED | RELEASING → CLEANUP_REQUIRED |
| ambiguous (`stopErr == nil`, unverified) | STOP_REQUESTED → RECONCILE_REQUIRED | RELEASING → CLEANUP_REQUIRED |

## 3. API contract (what callers may never supply)

Both lifecycle intakes (`/api/compute/start|stop` and
`/api/agent/sessions/compute`) accept exactly one JSON object with
unknown fields rejected. The only caller-supplied values are
`allocation_id` (and, on the agent surface, `session_id` + `action`).

Callers must NEVER supply — and the intakes never accept — Docker image
names or digests, command lines, environment maps, host paths, GPU UUIDs,
port numbers, privilege flags or network names. Those values are resolved
from administrator-controlled state: the reservation bundle's immutable
assignment (`AssignedRuntime/Model/GPU/Port`) and the Host Bridge's
fixed-profile allowlists. The workload manager validates the assignment
against the closed runtime enumeration before any driver call.

Error mapping (both surfaces): `ErrOwnerMismatch` → 403;
`ErrInvalidState`/`ErrInvalidAllocation` → 409;
`ErrReconciliationRequired` → 503 with "do not retry blindly".

## 4. Aggregate admission guard (current, conservative)

Until the roadmap's v19.7.1 Resource Vector + aggregate admission lands:
one active allocation per logical GPU, one allocation per host port
(enforced by the host executor). Per-container CPU/RAM/PID limits are NOT
aggregate capacity management — the guard stays conservative by design.

## 5. Host Bridge acceptance (status: OPEN)

The workload bridge service (`scarlix-workload-bridge.service`) remains
DISABLED until the P5.1 target-host acceptance matrix passes
(`docs/SCARLIX_COMPUTE_FABRIC_P5_1_HOST_ACCEPTANCE.md`): dedicated service
identity, socket permissions, no Docker socket in ScarliHQ, fixed
allowlists, digest-pinned images, idempotency (duplicate start → same
workload reference; conflicting retry → rejected), stop verification,
restart recovery, ledger durability. The mode bridge is a separate
service and must not be repurposed. **Hardware acceptance overall stays
OPEN (KPK-06) — CI green is never hardware verification.**

## 6. Roadmap alignment

This contract is the Phase 0 (stabilization) deliverable of
[`docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`](../roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md).
The subsequent phases — unified inventory v2 (v19.7.0), resource vectors +
atomic admission (v19.7.1), backend capability contracts + deterministic
planner (v19.7.2), InferSSD + Expert Pack behind tested profiles
(v19.8.0), distributed fabric (v19.9.0), production control plane
(v20.0.0) — extend this contract version-by-version; they do not replace
the lifecycle authority.
