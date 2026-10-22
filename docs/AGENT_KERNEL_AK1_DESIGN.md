# ScarLiX OS — Agent Kernel AK-1…AK-4 Design (v19.4.x line)

**Branch:** `agent-kernel/ak4-audit` → merged to main as **v19.4.5**
**Status:** AK-1 + AK-2 + AK-3 + AK-4 complete (AK-4 = append-only audit
sink); AK-5 follows (compute intake)
**Strategy source:** `docs/ScaRgeN_CuLT_Strategy_Obsidian_Lattice_SK_2026-10.pdf` (pages 13, 15–16, 22, 24)

---

## 1. Where this fits

Compute Fabric is software-complete as of v19.3.2 (reservation governor = F-04
closed). Per the strategy rule — *"Najprv dôveryhodný Compute Fabric. Až potom
bezpečných agenti"* — the Agent Kernel is the next theme, opening the
**v19.4.x** line (the strategy document's "19.3 Agent Kernel" slot shifted by
the Compute Fabric tail; phasing follows verifiable behavior, page 20).

## 2. Sub-phase plan (each with green CI + smoke before merge)

| Phase | Content | Status |
|---|---|---|
| **AK-1** | Typed foundations: agent identity, closed capability enumeration, session state machine, deterministic permission engine, audit events | **DONE — merged as v19.4.0** (incl. review fix: capability grants enforced) |
| **AK-2** | Session store + WS/REST surface (sessions created/closed via ScarliHQ API, auth-gated) | **DONE — merged as v19.4.1** (see §6) |
| **AK-3** | Policy engine v1: capability grants persisted per agent + admin disable switches; tie into the reservation governor's `user_tokens` principal space | **DONE — merged as v19.4.4** (see §7) |
| **AK-4** | Audit sink: append-only JSONL under `/var/lib/scarlix-agentkernel/` (0600, flock — same store discipline as reservations) | **DONE — merged as v19.4.5** (see §8) |
| **AK-5** | Compute intake API meets agent sessions: `compute.reserve`/`release` capabilities flow through the governor stores | planned (jointly with the intake API roadmap row) |

## 3. AK-1 contract (as implemented in `internal/agentkernel`)

### Identity
- `AgentID` — typed, validated `kind.name` (lowercase `[a-z0-9-]` segments,
  ≤64 chars). No anonymous agents.
- `AgentIdentity{ID, Owner, CreatedAt}` — an agent without an **owner
  principal** does not exist (owner is accountable; ties into the governor /
  ScarliHQ principal space).

### Capabilities — a CLOSED enumeration
`status.read`, `gaming.read`, `mode.request`, `compute.plan`,
`compute.reserve`, `compute.release`. Mirrors only existing allowlisted
surfaces. **There is no `shell.exec`, no docker passthrough, no arbitrary
file access** — strategy page 22 ("Žiadny voľný root shell"). Extending the
set is a deliberate, reviewed act (`AllCapabilities` + tests must change
together); unknown capabilities are rejected at construction.

### Sessions — bounded lifecycle
`CREATED → ACTIVE → SUSPENDED → CLOSED` (+ back from SUSPENDED); `CLOSED`
is terminal; illegal transitions and backwards timestamps are rejected —
the same fail-closed state-machine discipline as allocation/reservation.

### Permission engine — deterministic, no LLM
`Policy.Evaluate(agent, session, caps, capability) → Decision{Allow, Reason}`.
Rules in fixed order: session-required → session-owner → session-state
(must be ACTIVE) → capability-unknown → **capability-not-granted** →
capability-disabled → grant. The not-granted rule closes the loop the
original draft left open: the engine now verifies the agent's granted
capability set itself (fail-closed — a nil/empty set denies everything);
before the review fix it only checked the enumeration and the admin switch,
so any ACTIVE session could use any enumerated capability. Every denial
names the fired rule; every allow cites the grant —
*"dôvod výberu aj odmietnutia"* (page 13). The engine is pure: same input,
same output, 100×-repetition determinism test included.

### Audit
`AuditEvent{Timestamp, AgentID, SessionID, Capability, Decision}` — the
traceable-lifecycle building block (page 24). AK-4 persists these.

## 4. Non-goals (honest scope)

- No LLM integration yet — the kernel is the deterministic core the LLM
  layer will *propose to*, never the other way around.
- No HTTP/MCP wiring yet (AK-2) — the contract is established and tested
  first; nothing user-facing changes on this branch.
- No Rust yet — strategy page 18: Rust only for security-critical components
  *if tests justify it*; Go + tests first.

## 5. Verification on this branch

- `gofmt` clean, `go vet` OK
- `go test -count=1 ./internal/agentkernel` — identity validation, closed
  enumeration (incl. `shell.exec` rejection), legal/illegal/terminal
  transitions, backwards-clock rejection, full denial matrix (9 cases,
  incl. not-granted and nil-set fail-closed), determinism loop, audit
  validation
- `go test -race -count=1 ./internal/agentkernel` — OK

---

## 6. AK-2 contract (v19.4.1) — session store + REST surface

### Session store (`internal/agentkernel/store.go`)

- **Bounded by construction:** `NewStore(maxSessions)` rejects ≤0; the cap
  (default 128) counts all records incl. CLOSED ones — an honest, leak-free
  bound. At capacity: `ErrSessionLimit` (caller returns 503).
- **Fail-closed IDs:** session IDs are `aks-` + 16 crypto-random bytes hex;
  RNG failure aborts creation with `ErrRNG` (v18.7.4-P0 WS-ticket precedent —
  a predictable ID is never issued; verified by an RNG-injection test).
- **Explicit grants only:** creating a session with a nil/empty capability
  set is rejected — nothing is granted implicitly; the caller must be
  explicit (grants are fixed for the session's lifetime; mutation is not a
  store operation).
- **State machine owned by the kernel:** all transitions go through
  `Session.Transition` under the store lock — illegal moves, terminal
  violations and backwards clocks rejected.
- **Copies out:** `Get`/`List` return deep copies (grants cannot be mutated
  through a returned record — test-asserted); `List` is sorted by
  `CreatedAt` then ID (deterministic output, no map-order leaks).
- **Attributable evaluation:** `Evaluate` runs under the store lock — a
  decision can never be attributed to a state that no longer exists (no
  TOCTOU between read and evaluate); every decision is stamped with an
  `AuditEvent`.

### REST surface (`internal/api/agentkernel.go`, opt-in via `SCARLIHQ_AGENTKERNEL_ENABLED=true`)

| Route | Method | Contract |
|---|---|---|
| `/api/agent/sessions` | GET | list sessions (deterministic order, capabilities iterated from the closed enumeration) |
| `/api/agent/sessions` | POST | create: `{agent_id, owner, capabilities[]}` → 201 CREATED (agent + capability set validated by the kernel; unknown fields, duplicate objects, oversized bodies rejected) |
| `/api/agent/sessions/transition` | POST | `{session_id, to: ACTIVE\|SUSPENDED\|CLOSED}` → 200 (illegal/terminal/backwards → 409; unknown session → 404; other targets → 400) |
| `/api/agent/sessions/evaluate` | POST | `{session_id, capability}` → **200 with `{allow, reason, …}` for BOTH allow and deny** — a denial is a successful, auditable evaluation; unknown session → 404; unknown capability → 400 |

- Every route is behind the standard Bearer auth middleware (`h.auth`) —
  same trust boundary as `/api/compute/start|stop` (the bearer-token
  holder). No `?token=` URL fallback (v18.6 P2 stays closed).
- The `owner` field in create requests is caller-supplied: the trust
  boundary is the authenticated ScarliHQ principal. AK-3 replaces this with
  admin-owned persisted grants tied to the governor's `user_tokens`
  principal space.
- **Disabled by default** (503 until configured): opt-in wiring in
  `main.go`, commented compose block — the same enablement contract as the
  compute lifecycle (Controlled Accumulation).
- **No delete, no reset, no grant mutation** on the surface — the surface is
  deliberately minimal; anything beyond create/transition/evaluate is a
  separately reviewed act.

### Verification (AK-2)

- `go test ./internal/agentkernel` — store tests: construction validation,
  create happy path + rejections (nil/empty caps, invalid agent, zero
  time), RNG fail-closed injection, capacity incl. closed-records counting,
  copy isolation, list determinism (20× repetition), full transition chain,
  evaluate allow/deny/disabled/unknown matrix
- `go test ./internal/api` — HTTP tests: disabled-by-default (503),
  auth-gating (401 without/wrong token), full lifecycle over HTTP
  (create → evaluate-on-CREATED denial → activate → evaluate allow →
  not-granted denial with rule reason → list → close → terminal 409),
  create validation matrix (shell.exec, made-up capability, empty caps,
  bad agent id, no owner, unknown field, double object, malformed JSON),
  evaluate/transition validation (unknown capability 400, unknown session
  404, illegal target 400, method checks 405)
- `go test -race` on both packages — OK
- Smoke check 18 (deterministic, source-level): closed enumeration scanned
  for forbidden tokens (negative control verified — injected `shell.exec`
  FAILS the check), auth-gated route registration, denial-is-200 contract
  marker, bounded store constants, opt-in wiring + commented compose block

---

## 7. AK-3 contract (v19.4.4) — persisted admin-owned grant registry

### Grant registry (`internal/agentkernel/grants.go`)

Every capability grant is a separate, **persisted, admin-owned act**:

- **File format:** JSON at `/var/lib/scarlix-agentkernel/grants.json`
  (schema `version:1`); one `GrantRecord` per (agent, capability) grant with
  `granted_by`, `owner`, `granted_at` and — after revocation — `revoked_at`/
  `revoked_by` (a tombstone). Records are never deleted: the file itself is
  the reviewable history of who granted what, and re-granting a revoked
  capability appends a NEW record (the tombstones stay).
- **Fail-closed load:** ANY malformed record, unknown capability, owner
  outside the declared principal space, owner mismatch within one agent,
  revoked-without-revoker tombstone, duplicate active grant, bad schema
  version, or looser-than-0600/0700 file/dir permissions **aborts startup**
  (`log.Fatalf` in main.go). A tampered registry must never degrade into
  "some grants work". Unlike the allocation store, loose permissions are NOT
  auto-fixed — a widened mode is treated as tampering until an operator
  fixes it deliberately.
- **Atomic persistence:** writes go to a temp file in the same directory
  (0600 by construction, fsync) followed by rename — atomic on POSIX; a
  crash mid-write can leave a temp file but never a half-written registry.
  Failed persists roll back the in-memory state (state and file stay in sync).
- **Bounded:** `DefaultMaxGrants = 256` records (tombstones count — the
  history is real state) and `DefaultMaxAgents = 64` distinct agent IDs.
  Grants are a reviewed admin act, not a bulk import.
- **One agent, one owner:** the owner principal is declared by the first
  grant and enforced across ALL records (active + revoked) — an agent
  cannot act for two principals.
- **user_tokens tie-in:** the operator declares the allowed principal names
  (`SCARLIHQ_AGENT_PRINCIPALS`, comma-separated, copied from the governor
  config's `user_tokens` values); every grant owner and session owner must
  be part of that set (fail-closed: unknown principal = rejection at grant
  time AND at load time).

### Disable switches (admin kill-switch)

`DisableRecord{capability, disabled_by, disabled_at, reason}` — while a
switch exists, `Policy` denies the capability citing
`rule/capability-disabled`. `Enable` removes the switch; both acts name
their admin. The toggle takes effect on the **NEXT evaluation** (no
restart): evaluate consults `registry.Policy()` when a registry is
configured.

### REST surface (opt-in via `SCARLIHQ_AGENT_GRANTS_FILE` + `SCARLIHQ_AGENT_PRINCIPALS`)

| Route | Method | Contract |
|---|---|---|
| `/api/agent/grants` | GET | list grants (incl. tombstones — the history) + disable switches + counts; deterministic order |
| `/api/agent/grants` | POST | grant: `{agent_id, capability, owner, granted_by}` → 201 (validation: closed enumeration, principal space, owner-consistency, admin named; bound errors → 503) |
| `/api/agent/grants/revoke` | POST | revoke: `{agent_id, capability, revoked_by}` → 200 tombstone (unknown pair → 400; double revoke → 400) |
| `/api/agent/capabilities/disable` | POST | `{capability, disabled_by, reason}` → 200 (unknown capability → 400; duplicate → 400) |
| `/api/agent/capabilities/enable` | POST | `{capability, enabled_by}` → 200 (not disabled → 400) |

### Registry-mode session creation (breaking vs. legacy, by design)

When the registry is configured, `POST /api/agent/sessions` **rejects the
`capabilities` field** (400) — grants are admin-owned, not request-supplied.
The capability set and the owner are derived from the registry; an agent
with no active grants cannot start a session (400 — nothing is granted
implicitly). The AK-2 legacy contract (request-supplied grants) stays in
force when no registry is configured — backward compatible, same binary.

### Verification (AK-3)

- `go test ./internal/agentkernel` — registry tests: construction
  fail-closed, load initializes empty 0600 file, full grant/revoke/re-grant
  lifecycle with persistence round-trips, grant validation matrix
  (unknown capability/principal, empty admin, zero time, owner mismatch,
  duplicate), revoke validation, disable/enable + policy round-trip, load
  fail-closed on 12 malformed shapes + 4 loose file modes + loose dir,
  bounds (agents/grants, tombstones count, load-side), list determinism
  (20×), returned records are copies, runtime disable takes effect on the
  next evaluation
- `go test ./internal/api` — HTTP tests: grant routes auth-gated (401),
  disabled by default (503), full lifecycle over HTTP (grant → list counts →
  validation matrix → revoke → tombstone view → double revoke → disable →
  list → enable → method discipline), registry-mode session create
  (no-capabilities enforcement, owner derivation, grantless agent rejection,
  full kernel flow: activate → allow → not-granted deny → runtime disable →
  deny citing rule/capability-disabled), legacy-mode unchanged
- `go test -race` on both packages — OK
- Smoke check 20 (deterministic, source-level): fail-closed load sentinels,
  no auto-widen of permissions, atomic persistence, explicit bounds, admin-
  owned grants, tombstones, auth-gated route registration, registry-mode
  grant rejection marker, opt-in wiring + commented compose (negative
  control verified — breaking the compose opt-in line FAILS the check)

---

## 8. AK-4 contract (v19.4.5) — append-only audit sink

### Audit sink (`internal/agentkernel/audit.go`)

Every kernel decision and every admin act lands in an **append-only JSONL
file** at `/var/lib/scarlix-agentkernel/audit.jsonl`:

- **Line types:** `evaluate` (one line per permission evaluation — allow
  AND deny, the denial-is-200 persistence twin: session, agent, capability,
  decision, reason), `grant` / `revoke` (admin acts with owner + admin),
  `disable` / `enable` (kill-switch acts with reason). One struct with
  omitempty keeps lines small; `schema:1` envelope version per line.
- **Fail-closed construction:** the sink refuses to start on loose
  permissions (0600 file / 0700 dir, verified after create — umask-proof
  re-stat; `ErrAuditFilePermissions`). Same tamper discipline as the grant
  registry: no auto-fix.
- **Append discipline:** writes are flock-serialized across processes
  (sidecar `.lock` file, LOCK_EX — the reservation-store discipline) and
  mutex-serialized in-process; every line is flushed AND fsynced before
  Append returns — a crash can lose at most the line being written, never
  a torn line. The file is never rewritten or truncated — the history is
  the artifact.
- **Decision-persisted-before-serve (evaluate):** the API handler appends
  the audit line BEFORE returning the decision — if the write fails, the
  caller gets a 500 and no decision. An unauditable allow/deny is never
  served.
- **Honest ordering (admin acts):** grant/revoke/disable/enable append
  their audit line AFTER the successful registry persist — the state
  change stands (the registry file is the source of truth); a write
  failure is REPORTED (500 with the underlying error) so the operator
  knows the trail missed an act. No rollback theater.
- **Line validation:** every line validates before append (type-specific
  mandatory fields — evaluate lines need session/agent/capability/decision/
  reason; admin lines need their admin; zero timestamps rejected). Invalid
  lines are never written.
- **Bounded reads:** `ReadLast(n)` returns the last n lines newest-first,
  clamped to `DefaultMaxAuditRead = 200`; the audit FILE is unbounded in
  time (it IS the history) — only the READ surface is bounded. Malformed
  lines found during a read are reported as errors (visible to the
  operator, never silently skipped) and valid lines are still returned.

### REST surface (opt-in via `SCARLIHQ_AGENT_AUDIT_FILE`)

| Route | Method | Contract |
|---|---|---|
| `/api/agent/audit` | GET | last N audit lines (newest first; `?limit=1..200`, default 200) + count + sink path; `audit_read_errors` in the response when malformed lines were found — honest, not hidden. Read-only (POST → 405). |

Behind the standard Bearer auth, 503 until the sink is configured. The
sink works with OR without the grant registry (audit of legacy-mode
evaluations is legitimate — the sink is a separate opt-in).

### Verification (AK-4)

- `go test ./internal/agentkernel` — sink tests: construction fail-closed
  (loose dir/file), append+read round-trips for all 5 line types, append-
  only semantics (bytes never rewritten, history survives across sinks),
  line validation matrix (14 invalid shapes), read bounds + clamping,
  malformed lines reported-not-hidden, count, close discipline
  (idempotent; append/read after close → ErrAuditSinkClosed),
  event→line conversion + JSON round-trip
- `go test ./internal/api` — HTTP tests: auth-gating (401), 503 default,
  evaluations persisted before serving (allow + deny lines, session +
  reason asserted), admin acts appended in newest-first order with admins
  named, read endpoint (empty sink, limit validation, method discipline,
  tampered file → audit_read_errors + valid lines still returned)
- `go test -race` on both packages — OK
- Smoke check 21 (deterministic, source-level): fail-closed construction
  sentinels, decision-not-served marker, line validation, flock+fsync
  append discipline, bounded reads, admin-act lines, auth-gated read-only
  route, malformed-reported marker, opt-in wiring + commented compose
  (negative control verified — breaking the compose opt-in line FAILS the
  check)
