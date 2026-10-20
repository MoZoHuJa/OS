# ScarLiX OS — Agent Kernel AK-1/AK-2 Design (v19.4.x line)

**Branch:** `agent-kernel/ak2-sessions` → merged to main as **v19.4.1**
**Status:** AK-1 + AK-2 complete (AK-1 incl. pre-merge review fix; AK-2 =
bounded session store + auth-gated REST surface); AK-3+ follows
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
| **AK-3** | Policy engine v1: capability grants persisted per agent + admin disable switches; tie into the reservation governor's `user_tokens` principal space | planned |
| **AK-4** | Audit sink: append-only JSONL under `/var/lib/scarlix-agentkernel/` (0600, flock — same store discipline as reservations) | planned |
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
