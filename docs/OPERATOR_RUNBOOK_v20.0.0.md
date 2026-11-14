# ScarLiX OS v20.0.0 — Operator Runbook, Recovery, Rollback, and the Acceptance Matrices

Roadmap §10.3 (the production release's required contents) — the
operator's own document: what to run, what to verify, how to recover,
how to roll back, and the acceptance matrices whose filled-out copies
ARE the target-host acceptance report (the operator's evidence record,
attached to the release's exact SHA).

Status legend: **OPEN** = the operator's physical act (not code; the
release ships the matrix to fill). **READY** = the procedure is wired
and this document walks it.

---

## 1. Enabling the v20.0.0 surfaces (the opt-in map)

Every mutating/opt-in surface stays OFF by default (fail-closed). The
v20.0.0 additions in **bold**.

| Surface | Environment | Notes |
|---|---|---|
| Inventory v2 (GET) | always on | read-only collector |
| Planner (POST /api/compute/plan) | always on | pure; reserves nothing |
| Admission gate | `SCARLIHQ_ADMISSION_ENABLED=true` + `SCARLIHQ_ADMISSION_LEDGER=<0600-writable path>` | the durable ledger |
| Distributed fabric | `SCARLIHQ_FABRIC_ENABLED=true` + `SCARLIHQ_FABRIC_NODES_LEDGER=<0600-writable JSONL>` | node identity is durable |
| Latency classes | `SCARLIHQ_FABRIC_LATENCY=node-a=A,node-b=B` | operator knowledge, never agent self-declaration |
| **Node-tagged admission** | BOTH admission + fabric enabled | main.go attaches the per-node seam automatically; `POST /api/compute/admission` accepts `node_id` |
| **Resource accounting (GET)** | always on (rides the inventory seam) | `/api/compute/resources` — READ gate |
| **Capability summary (GET)** | always on | `/api/controlplane` — READ gate |
| **Production ledger view** | BOTH admission + fabric enabled | the executions push computes REAL reconciliation decisions |
| InferSSD | `SCARLIHQ_INFERSSD_PROFILE=<path>` | §8.8: stays OFF; hardware acceptance OPEN (KPK-06) |

### 1.1 The v20.0.0 startup log lines to expect

```
Admission gate enabled (ledger: …; policy admission/v19.8.1, …)
Distributed fabric enabled (node ledger: …; … nodes known, …)
Node-tagged admission enabled (the per-node seam attached: POST /api/compute/admission accepts node_id — …)
```

The third line appears ONLY when both surfaces are enabled — its
absence is the honest "node-tagged admission: unconfigured" state
(visible on `GET /api/controlplane`).

## 2. The day-to-day operator loop

1. **Watch:** `GET /api/controlplane` — the capability states, the
   fabric census, the admission counts. Anything "unconfigured" is a
   decision, not a failure.
2. **Resources:** `GET /api/compute/resources` — per node: observed
   (with freshness), reserved (the ledger), allocatable (computed),
   estimated (manifest declarations). The labels are the contract:
   stale/unknown nodes carry NULL allocatable — never act on a guess.
3. **Plan:** `POST /api/compute/plan` — the deterministic proposal
   (single- or multi-node when the fabric is enabled).
4. **Reserve:** `POST /api/compute/admission` (± `node_id`) — the
   decision is data: `allowed=false` + `reason_code` is the gate
   working.
5. **Confirm the start:** `POST /api/compute/admission/confirm-started`
   (HELD → ACTIVE; the lease stops applying).
6. **Stop/Release:** `POST /api/compute/stop`, then
   `POST /api/compute/admission/release` (verified cleanup).
7. **Nodes:** `POST /api/fabric/nodes` (invite) → the node agent's
   `enroll` with the ONE-TIME secret → the agent's inventory/health/
   executions pushes arrive authenticated. Rotation: `POST
   /api/fabric/nodes/rotate`. Revocation = quarantine: `POST
   /api/fabric/nodes/revoke`.

## 3. Recovery procedures

### 3.1 Crash / restart of the control plane

- The admission ledger reconstructs on boot: boot-mismatched HELD holds
  are closed AS `CRASH_RECOVERED` (the recorded decision); ACTIVE holds
  survive a restart of the manager (durable), and their capacity still
  counts.
- The node identity registry reloads from its JSONL (durable).
- The observational registry starts EMPTY (observations are
  re-pollable by definition) — nodes reappear as their agents push
  again; until then their capacity is UNKNOWN and admission on them
  refuses BY NAME (never recycled from the retained... there is no
  retained snapshot after a CP restart — reservations stay HELD in the
  ledger and reconcile when the node's reports return).

### 3.2 Node unreachable / partition

The §4.1 semantics (enforced in code):
- capacity UNKNOWN; admission on that node refuses
  (`node-unreachable`);
- existing reservations stay HELD — **never recycled, never
  forgotten**;
- the node's agent cannot admit anything on its own (no second
  ledger);
- when the node returns: the executions push + the reconciliation view
  reconcile FIRST (`reservation-stands` / `reconcile-required` /
  `stop-foreign-execution`); admission resumes only after.

### 3.3 Revoked node (quarantine)

- `node-quarantined` refusals for new work; observations of a
  quarantined node are refused.
- Reservations are NOT auto-released (capacity is not proven free).
- Cleanup: `POST /api/fabric/nodes/reactivate` with a FRESH
  credential (the trusted recovery path) — then reconcile.

### 3.4 Stale inventory (local or remote)

Hard refusal, by name: `snapshot-stale` (local, 60s default) /
`node-inventory-stale` (remote, 30s default — the stricter window).
Remedy: re-run the collector (the local snapshot rebuilds per request;
remote snapshots arrive with the agent's pushes).

## 4. Rollback

- **v20.0.0 → v19.9.0:** no data migration is REQUIRED to read the
  old state (all v20.0.0 record fields are additive: `node_id`
  normalizes empty→local). A v19.9.0 binary reading a v20.0.0 ledger
  ignores the additive field (strict decode of the RECORD schema is
  the ledger's own loader; the JSON `node_id` key is omitempty and
  unknown-key-tolerant in that direction is NOT guaranteed — the
  safe rollback is the documented one: keep the v20.0.0 ledger file
  AS AN ARCHIVE, roll back the binary, and let the fresh ledger start
  from the operator's reconciliation of the holds — see 3.1; the
  honest recommendation: **roll back the binary + archive the ledger
  + re-admit deliberately**).
- **Fabric off:** unset `SCARLIHQ_FABRIC_ENABLED` — the fabric routes
  503, the planner returns to single-node behavior (the v19.9.0
  regression pin proved byte-identity), node-tagged admissions
  refuse `node-source-unconfigured`.
- **Admission off:** unset `SCARLIHQ_ADMISSION_ENABLED` — the
  admission routes 503; the ledger file stays on disk untouched.

## 5. The acceptance matrices (the operator's acts — the evidence records)

Copy the relevant matrix, run it, fill it, attach it to the release's
exact SHA. Until filled: **OPEN** — and the release notes say so.

### 5.1 Single-node target-host acceptance (per host)

| Check | Command | Expected | Result (p50/p95/p99 where noted) |
|---|---|---|---|
| CPU/RAM/GPU enumeration | `GET /api/inventory/v2` | all components, per-source provenance ok | ☐ |
| Storage benchmark (sequential + random-read) | `scarlihq --storage-benchmark …` | recorded ReadProfile (IOPS p50/p95/p99) | ☐ |
| Plan → admit → confirm-started → stop → release | the §2 loop, one model | decision at each stage, capacity returns to allocatable on `GET /api/compute/resources` | ☐ |
| Crash recovery | kill -9 the binary mid-hold, restart | HELD→`CRASH_RECOVERED` on the record; ACTIVE survives | ☐ |
| Lease expiry | hold without confirm-started past TTL | `EXPIRED` — freed by reconciliation | ☐ |
| Resource labels | `GET /api/compute/resources` | observed/reserved/allocatable consistent with the ledger; stale renders NULL allocatable | ☐ |

### 5.2 Multi-node fabric acceptance (the Phase 5/6 hardware gate)

Prerequisites: a real tailnet (or LAN) with ≥2 hosts, the node agent
installed on each (`cmd/scarlix-node-agent`), the control-plane
credential provisioned per design §3.3.

| Check | Procedure | Expected | Result |
|---|---|---|---|
| Enrollment | invite → enroll (one-time secret) | node ACTIVE; credential issued exactly once | ☐ |
| Authenticated pushes | agent inventory/health/executions | accepted with node credential + scope + replay guard; WITHOUT credential → 401 even on reachable transport | ☐ |
| Node-tagged admission | `POST /api/compute/admission` with `node_id` | allowed=true; the record carries the node tag; the local totals untouched | ☐ |
| Isolation | same GPU uuid on both hosts, holds on both | no collision; `GET /api/compute/resources` shows both nodes' own accounting | ☐ |
| Unreachable | disconnect one node | new admissions refuse `node-unreachable`; the hold stays HELD; resources shows capacity UNKNOWN + NULL allocatable | ☐ |
| Return + reconciliation | reconnect; the executions push | reconciliation decisions against the REAL ledger; admission resumes after | ☐ |
| Quarantine + recovery | revoke → reactivate | quarantine refusals; reservations NOT auto-released; cleanup via the trusted path | ☐ |
| Credential rotation | rotate mid-operation | bounded grace window; the previous-previous credential dead | ☐ |
| Remote freshness | pause the agent's pushes ≥30s | `node-inventory-stale` — the stricter window | ☐ |

### 5.3 InferSSD / SSD-backed inference (KPK-06 — unchanged, OPEN)

The §8.7 matrix (HARDWARE_ACCEPTANCE_PROTOCOL.md): CPU/RAM/GPU/CUDA/
driver + the SSD model/firmware/mount + the `RandomReads` benchmark
(IOPS p50/p95/p99) + the comparative SGLang / +pack / InferSSD
measurements. The v19.8.0–v19.8.1 state stands: default OFF, nothing
ships enabled.

## 6. The security review checklist (v20.0.0's own surfaces)

- [x] The new surfaces are READ-gated (`h.auth` — observation grade;
      the view reserves nothing).
- [x] Node-tagged admission is fail-closed (the seam's absence = the
      named refusal; node capacity never guessed locally).
- [x] The seam's refusal vocabulary is CLOSED (six codes + the
      generic; the adapter maps ONE-to-ONE from the fabric's
      sentinels — no string matching, `errors.Is` all the way).
- [x] The idempotency fingerprint covers `node_id` (a reused key
      across nodes = the NAMED conflict, never an inherited hold).
- [x] The ledger stays ONE authority (the node tag is a routing key;
      no per-node stores, no consensus, no second ledger).
- [x] Tailscale remains TRANSPORT ONLY (§9.2 — the per-node credential
      + replay guard gate every push regardless of network
      reachability; the reachability-without-credential test stands).
- [x] The capability rows never claim operator-owned acts.

## 7. Where the numbers live

- The compat matrix: the closed backend registry (`GET /api/compute/plan`).
- The SSD benchmark methodology + format: the storage benchmark's
  recorded ReadProfile (`--storage-benchmark`, the random-read phase).
- The verification records: `--verify-manifests` (the artifact
  digests' recorded answer).
- The release evidence: `docs/RELEASE_NOTES_v20.0.0.md` + CI on the
  tag.
