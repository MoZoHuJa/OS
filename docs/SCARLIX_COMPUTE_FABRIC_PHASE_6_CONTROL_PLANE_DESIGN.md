# ScarLiX Compute Fabric — Phase 6 Design: The Unified Production Control Plane (v20.0.0)

Roadmap §10 (`docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`),
the final phase of the Unified Compute Fabric implementation sequence:
"Close UI, audit, recovery and v20.0.0 production acceptance" (§18 step 11).

Owner: operator (MoZoHuJa). Design date: 2026-11-14. Status: implemented
in v20.0.0.

---

## 1. What Phase 6 closes

Phase 5 (v19.9.0) shipped the distributed fabric's contracts and left
three named residuals, each pointing HERE:

1. **Node-tagged admission did not exist** — the FabricPlanner could
   PRODUCE multi-node plans, but the admission gate (the reservation
   authority) had no per-node routing: a reservation against a remote
   node's capacity was impossible ("authenticated single- and
   multi-node execution", §10.1, was plan-only).
2. **The FabricLedgerViewProvider production wiring** — the executions
   push computed reconciliation decisions against a report-only
   fallback, not the real ledger.
3. **Dashboard visibility** (§10.2) — no surface exposed the resource
   accounting with the labeled-value contract the UI trio requires.

Plus the §10.3 release-acceptance deliverables (runbook, recovery,
rollback, acceptance matrices, release evidence) — documentation the
operator's acts hang on.

## 2. The architecture decisions (§19 discipline: owner + evidence + date + rationale)

### #11 Node-tagged admission — the ledger's routing key, not a second ledger

- **Owner:** operator (MoZoHuJa).
- **Decision:** `AdmissionRequest.node_id` (empty or `"local"` = the
  control plane's host — the v19.9.0 behavior byte-identical) routes the
  capacity gates to THAT node's **VERIFIED fabric observation** through
  the `admission.NodeSnapshots` seam. The reservation lands in THE one
  authoritative control-plane ledger with the node tag (`Record.node_id`).
  **Per-node capacity totals key on the normalized node id** — two nodes
  may carry the same GPU UUID without colliding; a local hold never
  consumes a remote node's allocatable and vice versa.
- **Evidence/rationale:** Phase 5 design §1 (hub-and-spoke, one ledger,
  split-brain prevented by construction) — the node tag is a routing key
  ON the single ledger, never a second store. The seam is attached by
  the operator's act (`SetNodeSnapshots` — main.go wires it when BOTH
  `SCARLIHQ_ADMISSION_ENABLED` and `SCARLIHQ_FABRIC_ENABLED` are set);
  a node request without it refuses BY NAME
  (`node-source-unconfigured`) — node capacity is never guessed from the
  local host's snapshot.
- **Date:** 2026-11-14.

### #12 The refusal vocabulary — the fabric's §4.1 semantics as admission reason codes

- **Owner:** operator (MoZoHuJa).
- **Decision:** the seam's typed refusals surface VERBATIM as the
  admission decision's `reason_code`: `node-unknown`,
  `node-quarantined`, `node-unreachable`, `node-inventory-absent`,
  `node-inventory-stale`, plus the generic fail-closed
  `node-snapshot-unavailable`. The fabric Registry's `SnapshotFor`
  (one lock — the guard and the observation cannot drift apart
  mid-read) evaluates the §4.1 order: quarantined → unreachable →
  absent → stale-remote-window.
- **Evidence/rationale:** the §4.1 semantics table is the normative
  contract; unreachable = capacity UNKNOWN with reservations HELD —
  the refusal must reach the caller as DATA (the refusal-is-data
  genre), never an HTTP error.
- **Date:** 2026-11-14.

### #13 The value-label vocabulary is a MACHINE contract (§10.2's DoD row)

- **Owner:** operator (MoZoHuJa).
- **Decision:** `GET /api/compute/resources` (READ gate) carries the
  closed label vocabulary — `observed`, `estimated`, `reserved`,
  `allocatable`, `unknown`, `stale` — with the per-node views computed
  from their named sources: observed (the inventory snapshot + its
  freshness + per-source provenance), estimated (manifest
  DECLARATIONS — never conflated with observed capacity), reserved
  (the ledger's HELD/ACTIVE records per node), allocatable (COMPUTED:
  observed free − policy headroom − reserved; the KV cache counted
  EXACTLY once, the admission gate's own accounting).
  **allocatable is NULL (omitted) for stale/unreachable/quarantined
  nodes — never a guess from aged or unknown figures.** Without the
  admission gate the reserved view is the honest ZERO
  (`reservations_configured=false` — nothing can reserve without the
  gate; zero is a fact, not a guess).
- **Evidence/rationale:** roadmap §10.2 "the UI must label values
  clearly" — the DoD row "UI distinguishes
  observed/estimated/reserved/allocatable" is proven at the CONTRACT
  level: the labels are machine-readable on one surface the UI trio
  renders (ScarliMonitor), and ScarliCenter's approval/stop/release and
  ScarliBar's intent/summary/confirmation surfaces already exist
  (v19.5.x: `/api/bar/*`, `/api/ai/proposals`) and CONSUME this
  contract without a second vocabulary.
- **Date:** 2026-11-14.

### #14 The capability summary — honest states, never claims

- **Owner:** operator (MoZoHuJa).
- **Decision:** `GET /api/controlplane` (READ gate) answers "which
  §10.1 capability is configured": one row per capability with state
  `configured | unconfigured | experimental | documented` + detail,
  the fabric census (per-node states + the stricter remote window),
  the admission census (held/active/node-tagged/policy version), and
  the freshness windows. **The rows never claim an operator-owned
  act** (hardware acceptance KPK-06, real multi-host acceptance —
  those live in the acceptance matrices; the runbook row is
  `documented`, pointing at the operator's file).
- **Date:** 2026-11-14.

## 3. The production ledger view (the v19.9.0 residual closed)

`api.FabricAdmissionLedger` implements `FabricLedgerViewProvider` over
the admission Manager's ledger: `HeldReservations(nodeID)` maps the
node's HELD/ACTIVE records onto the reconciliation view. main.go wires
it into the FabricConfig whenever both surfaces are enabled — the
`/api/fabric/agent/executions` push now computes REAL decisions
(`reservation-stands` / `reconcile-required` /
`stop-foreign-execution`) against the actual ledger. An unknown node
honestly returns the EMPTY view (every agent-reported execution is
then foreign work — exactly the §4.2 contract).

## 4. New files and changed files

| File | What |
|---|---|
| `internal/admission/admission.go` | `AdmissionRequest.NodeID` + the six node reason codes (additive) |
| `internal/admission/ledger.go` | `Record.NodeID` (additive; pre-v20.0.0 records normalize to local) |
| `internal/admission/manager.go` | the `NodeSnapshots` seam + `NodeSnapshotRefusal` + `SetNodeSnapshots`/`HasNodeSnapshots` + `normalizeNodeID`/`nodeHeld` + the routing + per-node totals + the node_id fingerprint binding |
| `internal/fabric/registry.go` | `Registry.SnapshotFor` — the §4.1 guards under ONE lock |
| `internal/api/fabric.go` | `FabricNodeSnapshots` (the sentinel→refusal adapter) + `FabricAdmissionLedger` (the production ledger view) + `fabricNodeKey` |
| `internal/api/resources.go` | NEW — `GET /api/compute/resources` (the labeled accounting) |
| `internal/api/controlplane.go` | NEW — `GET /api/controlplane` (the capability summary) |
| `internal/api/rest.go` | the two route registrations (READ gate) |
| `cmd/scarlihq/main.go` | the seam + ledger-view wiring (both-enabled), the hoisted admission manager |
| `docs/OPERATOR_RUNBOOK_v20.0.0.md` | NEW — the operator/recovery/rollback runbook + the acceptance matrices |
| smoke check_43 | the v20.0.0 surface's own check |

## 5. Implementation slices (commit order)

1. `internal/admission` — the seam + routing + per-node totals + the
   node_id fingerprint (the local path regression-pinned: byte-identical
   decision with/without the seam).
2. `internal/fabric` — `SnapshotFor` (the one-lock §4.1 guards).
3. `internal/api` — the adapters + the resources/controlplane surfaces.
4. `cmd/scarlihq` — the production wiring.
5. docs + README + VERSION bump + smoke check_43 + tag `v20.0.0`.

## 6. Definition of done (roadmap §17 v20.0.0, verbatim mapping)

| §17 row | Where it is proven |
|---|---|
| End-to-end lifecycle proven on target hardware | the operator's act — the runbook's single-node acceptance matrix (OPEN by design, like KPK-06 before it) |
| Single- and multi-node acceptance passed | the runbook's matrices; the code-side proofs: the node admission suite + the HTTP suite + the isolated-totals proof |
| Security review complete | the release notes' security section (the v20.0.0 surfaces' review: READ gates, fail-closed seams, the §9.2 inheritance) |
| UI distinguishes observed/estimated/reserved/allocatable | `GET /api/compute/resources` — the closed label vocabulary as a machine contract (TestResourcesLabelsContract) + the allocatable math suite |
| Operator, recovery and rollback docs complete | `docs/OPERATOR_RUNBOOK_v20.0.0.md` |
| Release evidence attached to exact SHA | the release notes + CI green on the tag |

## 7. Explicit non-goals for v20.0.0

- No real multi-host deployment claim (the operator's acceptance
  matrices — same discipline as every release since v19.7.0).
- No second ledger, no consensus, no per-node schedulers (§1 of the
  Phase 5 design stands).
- The CP→agent dial-out (the notify/start/stop client calls from the
  control plane to node agents) is NOT wired in main.go: the agent
  binary, the typed client, and the mirror are shipped and tested
  (v19.9.0), but the production dial-out configuration (agent endpoints
  + the control-plane credential distribution) is the operator's
  deployment act — named here so the residual stays honest.
- No InferSSD enablement (§8.8 stands: nothing ships enabled; hardware
  acceptance KPK-06 stays OPEN).
