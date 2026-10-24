# ScarLiX OS — Agent Fabric BF-1 Design (v19.5.x line)

**Branch:** `agent-fabric/bf1-commands` → trunk `phase-b/agent-fabric` → main as **v19.5.0**
**Status:** BF-1 complete — Phase B (ScarliBar MVP + Agent Fabric) is open
**Phase B plan:** BF-1 command/intent lifecycle (this release) · BF-2 agent
workspaces · BF-3 coding/research agent runtimes · BF-4 approvals · BF-5 phase
closure (each with green CI + smoke before merge)

---

## 1. Where this fits

The Agent Kernel line closed at v19.4.6 (AK-1…AK-5: identity, sessions,
persisted admin-owned grants, append-only audit, agent-gated compute intake).
Phase B opens the user-facing line above the kernel:

- **ScarliBar** is the intent entry point — where a user says WHAT they want
  (P4 contract: intent capture + user-visible command status; it does NOT own
  privileged operations or execution).
- **The Agent Fabric** is the machinery that turns a captured intent into a
  routed, kernel-decided, user-visible command.

BF-1 establishes the deterministic core of that loop. There is no LLM in it
anywhere — per the strategy rule *"LLM navrhuje, politika rozhoduje"*, the
LLM layer (Phase C, Universal AI Runtime) will PROPOSE intents and routing
to this fabric; the fabric + kernel remain the deciding, auditable path.

## 2. BF-1 contract (as implemented in `internal/agentfabric` + `/api/bar/*`)

### Command model (`command.go`)

- `CommandID` — "cmd-" + 16 crypto-random bytes hex; RNG failure aborts
  creation (`ErrRNG`, the kernel's aks- precedent — a predictable ID is
  never issued).
- `Intent` — the captured text: trimmed non-empty, ≤ 512 runes
  (`MaxIntentRunes`), NO control characters (single-line intents; the intent
  lands in a JSON file, a REST response and the dashboard DOM — control
  characters have no honest role there; multi-line is a deliberate future
  extension, not an accident).
- `CommandState` — `SUBMITTED → ROUTED → DECIDED` plus `CANCELLED` (from
  SUBMITTED or ROUTED). DECIDED and CANCELLED are terminal. `ROUTED → ROUTED`
  is the re-bind (a new routing attempt for a command whose previous attempt
  did not reach a decision). Illegal moves, terminal violations and
  backwards clocks are rejected (the kernel state-machine discipline).
- `CommandRecord` — id, intent, agent, capability, bound session, state,
  decision (`*agentkernel.Decision` — allow AND deny, with the fired rule)
  and monotone timestamps. Field presence is state-invariant:
  SUBMITTED carries nothing bound; ROUTED carries session+routed_at; DECIDED
  additionally carries decision+decided_at; CANCELLED carries cancelled_at
  (routing history stays if it was routed first). `validate()` enforces all
  of this — at load AND after every mutation.

### Command store (`store.go`)

The durable half, grants.json discipline (AK-3 precedent):

- JSON file (default `/var/lib/scarlix-agentfabric/commands.json`, schema
  version 1 — explicit migration required for anything else).
- 0600 file / 0700 directory — checked at load AND before every persist;
  looser modes are tampering until an operator fixes them deliberately
  (never auto-widened).
- Atomic persistence: temp file in the same directory (0600 by construction,
  fsync) + rename. A failed persist rolls back the in-memory mutation —
  state and file stay in sync.
- Bounded: `DefaultMaxCommands = 512` records in ALL states (the history is
  real state, not free). At capacity Submit fails with `ErrCommandLimit`
  (HTTP 503); rotating the file is a deliberate operator act, like audit
  rotation.
- Commands are never silently deleted — the file IS the reviewable history
  of every captured intent and its outcome.
- Returned records are deep copies (pointer fields re-allocated) — callers
  cannot mutate store state through a view.
- The store does NOT know the kernel: routing/evaluation are composed by
  the API layer (the fabric owns command lifecycle; the kernel owns
  permission evaluation — separate concerns, composed at the boundary).

### REST surface (`api/agentfabric.go`, opt-in)

| Route | Method | Contract |
|---|---|---|
| `/api/bar/commands` | GET | last N commands newest-first (`?limit=1..200`, default 200 — the audit read discipline) + per-state counts + store path; 503 until the fabric is configured |
| `/api/bar/commands` | POST | submit `{intent, agent_id, capability, session_id?}` → 201. With a resolvable ACTIVE session the command routes + evaluates + decides synchronously; without one it is captured SUBMITTED (awaiting routing — **the intent is never lost**) |
| `/api/bar/commands/route` | POST | `{command_id}` → re-attempt routing+evaluation (SUBMITTED/ROUTED only; terminal → 409; no ACTIVE session → 409 retryable) |
| `/api/bar/commands/cancel` | POST | `{command_id}` → CANCELLED (SUBMITTED/ROUTED only; a kernel decision is a fact, not a wish — DECIDED cannot be cancelled) |

All routes behind the standard Bearer auth middleware; bounded one-object
JSON decode (4KB, unknown fields rejected). The fabric REQUIRES the kernel
(503 without `SCARLIHQ_AGENTKERNEL_ENABLED` — it routes intents through
kernel sessions; there is no standalone mode).

### Decision chain (per submit/route)

1. **Capture first:** Submit persists the SUBMITTED command before any
   routing attempt — intent capture must not fail on routability.
2. **Session resolution (deterministic):** the named session (must exist,
   belong to the agent and be ACTIVE) or, when none is named, the NEWEST
   ACTIVE session of the agent (kernel List order: CreatedAt then ID).
3. **Kernel evaluation:** `agentStore.Evaluate` — registry policy applies
   when the grant registry is configured, so runtime kill-switches take
   effect on the next evaluation (no restart). A denial is a DECIDED
   command (`allow=false` + the fired rule) — the denial-is-200 contract
   carried into user-visible status.
4. **Audit before serve (fail-closed):** when the audit sink is configured,
   the evaluate line is appended BEFORE the command transitions to DECIDED.
   An audit failure returns 500 — the command stays ROUTED, re-routable.
5. **DECIDED persist:** the decision + decided_at land on the command.

Cross-file atomicity (commands.json vs audit.jsonl) is honestly NOT claimed
(P4 invariant 4: no false atomicity): an audit line can exist for an
evaluation whose command later failed to persist DECIDED — the command stays
ROUTED and a re-route re-evaluates (appending a fresh audit line). The
reverse (DECIDED without its audit line) cannot occur — audit precedes
DECIDED by construction. Correlation between the two artifacts is the bound
session id (the command view carries it; every evaluate line carries it).

### Dashboard panel (`frontend/dist/index.html`)

ScarliBar card in the existing go:embed dashboard (same token gate, same
XSS escaping convention — every dynamic field goes through `escapeHTML`):

- intent input (≤512 chars) + agent input with datalist (fed from ACTIVE
  kernel sessions) + capability select (the closed enumeration) + Send
  (44px touch targets);
- commands table newest-first: intent (truncated, title for full), agent ·
  capability, state pill (SUBMITTED violet / ROUTED cyan / DECIDED
  up-or-down by allow with the kernel reason / CANCELLED down), updated-at,
  route/cancel actions for live commands (44px touch targets);
- 5s polling (the established REST fallback pattern; WS push for fabric
  events lands with the agent runtime — the hybrid protocol decision is
  honored REST-first, honestly scoped);
- 503 → explicit "Agent Fabric disabled — opt-in via
  SCARLIHQ_AGENTFABRIC_ENABLED=true" empty state.

### Enablement (`main.go`, compose)

`SCARLIHQ_AGENTFABRIC_ENABLED=true` + `SCARLIHQ_AGENTFABRIC_COMMANDS_FILE`
(required — persistence IS the chosen data model) + the kernel enabled
(fail-closed Fatal otherwise). Compose carries the commented opt-in env +
mount block (0700 host-side directory, tamper discipline).

## 3. Honest non-goals (BF-1)

- An ALLOWED command is a kernel DECISION, not an execution — execution
  plumbing arrives with the agent runtime (BF-3+). Agents that need to act
  on compute today use the AK-5 intake directly.
- No LLM integration (Phase C) — deterministic routing only: the capability
  is part of the intent submission (explicit, closed enumeration).
- No submitter identity field: single-principal deployments (one bearer
  token); the commands file is 0600 server-side. Submitter identity lands
  with the approval UX line (BF-4 / ScarliCenter).
- No command deletion, no history rewrite — the file is the history.
- WS push for fabric events: BF-2+ (when there are runtime events worth
  pushing).

## 4. Verification on this branch

- `gofmt` clean, `go vet` OK
- `go test ./internal/agentfabric` — ID/intent validation matrices (incl.
  control characters, rune counting, hex shape), full transition matrix
  (legal/illegal/terminal/backwards-clock), record invariant matrix (19
  invalid shapes), lifecycle with persistence round-trips (submit → route →
  decide → terminal discipline → reload), re-bind, cancel-from-both-states,
  RNG fail-closed injection, capacity bound, load fail-closed (malformed
  JSON, wrong version, 10 bad record shapes, duplicate IDs, over-bound),
  loose file modes (0640/0644/0604/0666) + loose dir, persist-failure
  rollback (read-only dir → rollback → reload shows SUBMITTED), list
  newest-first determinism (20×), counts, deep-copy isolation
- `go test ./internal/api` — fabric routes auth-gated (401), disabled by
  default (503), fabric-without-kernel (503), submit validation matrix
  (empty/control-char intent, bad agent, shell.exec, unknown field,
  malformed JSON, unknown named session, oversized body), captured-without-
  session (201 SUBMITTED), full synchronous flow (201 DECIDED allow +
  reason), denial-is-a-decision (201 DECIDED allow=false +
  rule/capability-not-granted), route endpoint (409 no session → 200 after
  activation → 409 terminal → 404 unknown), named-session routing (newest
  auto-resolution, named wins, foreign-agent session → 400 with the intent
  still SUBMITTED in the list), cancel (200 → 409 double → 404 unknown),
  limit validation, method discipline (405), audit-before-serve (evaluate
  line with the routed session id present in the sink), persistence across
  handler reload
- `go test -race` on both packages — OK
- Smoke check 23 (deterministic, source-level): fail-closed load sentinels,
  schema versioning, atomic persistence, bounds, intent validation, kernel
  evaluation requirement, denial-is-a-decision marker, audit-before-serve
  marker, never-lost marker, auth-gated route registration, main.go opt-in
  wiring (incl. kernel requirement Fatal), compose opt-in block (negative
  control verified — breaking the compose opt-in line FAILS the check,
  rc=1 → revert → rc=0)
- E2E on the real binary: auth 401; session create+activate; synchronous
  DECIDED allow with the kernel reason; SUBMITTED capture without a
  session; route 409; cancel; denial recorded as DECIDED with the fired
  rule; list+counts; commands.json on disk 0600/schema-1; **restart → the
  full command history survives**
