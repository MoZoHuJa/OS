# ScarLiX OS — Agent Kernel AK-1: Foundation Design (v19.4.x line)

**Branch:** `agent-kernel/ak1-foundation` → merged to main as **v19.4.0**
**Status:** AK-1 complete (incl. pre-merge review fix: enforced capability
grants in `Policy.Evaluate`); wiring (AK-2+) follows
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
| **AK-2** | Session store + WS/REST surface (sessions created/closed via ScarliHQ API, auth-gated) | planned |
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
