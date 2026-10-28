# Phase B (Agent Fabric) — Acceptance Gate

Theme: **Agent Fabric (v19.5.x)** — the user-facing line above the Agent Kernel
Candidate line: BF-1…BF-5 (v19.5.0 → v19.5.4)
Closure release: **v19.5.4 (BF-5 — phase closure)**

> **Status:** Phase B is **software-complete in CI** — BF-1 command lifecycle +
> ScarliBar panel (v19.5.0), BF-2 agent workspaces (v19.5.1), BF-3 coding/research
> agent runtimes (v19.5.2), BF-4 operator approvals (v19.5.3) and BF-5 phase
> closure (v19.5.4) are all merged, tagged, CI-green and released. **Target-host
> hardware acceptance per `docs/HARDWARE_ACCEPTANCE_PROTOCOL.md` remains OPEN
> (KPK-06)** — CI green ≠ hardware verified, and the honest statement stays:
> *logically strong, empirically unproven on the target host.* Phase B is
> closed as **deterministic software**; nothing in it executes an LLM (that is
> deliberately Phase C — *"LLM navrhuje, politika rozhoduje"*).

## 1. What Phase B delivers (the complete loop)

Phase A closed the deterministic core (AK-1…AK-5: identity, capabilities,
sessions, grants, audit, compute intake). Phase B built the user-facing fabric
on top of it — five releases, each with green CI + smoke before merge:

| Step | Release | What it added to the loop |
|---|---|---|
| BF-1 | v19.5.0 | Intent capture **persisted first** (never lost), deterministic routing (named or newest ACTIVE session), kernel-decided status (allow AND deny are both DECIDED), audit-before-serve, bounded persisted history, ScarliBar dashboard panel |
| BF-2 | v19.5.1 | **Workspaces** — named, persisted isolation contexts; ONE workspace per session; member-only scoped routing; ARCHIVED terminal tombstone |
| BF-3 | v19.5.2 | **Runtimes** — closed total mapping (research read-only / coding bridged, load-time invariant); execute surface with the STALE-ALLOW GUARD (FRESH kernel evaluation at execute time); terminal-only execution records; FabricBus change signals + `/ws/bar` push |
| BF-4 | v19.5.3 | **Operator approvals** — the human gate for MUTATING acts (boundary DERIVED from the runtime mapping); execute creates the ONE PENDING request (202); approve RUNS THE ACT; reject unconditional + terminal |
| BF-5 | v19.5.4 | **Phase closure** — this acceptance gate: the phase-level coherence check (smoke check 27), the phase-gate E2E (one continuous journey through the whole surface), README closure, honest open-item ledger |

The end-to-end loop a user can now walk: capture an intent in the ScarliBar →
it is persisted before anything else happens → routed deterministically
(optionally through a workspace's member sessions) → the kernel decides with
the fired rule and the decision is audited before it is served → a read-only
(research) act executes directly under its closed runtime → a mutating (coding)
act waits for the ONE operator approval, and approve composes the full BF-3
flow at decide time → every outcome is a terminal, persisted, user-visible
record → the dashboard and `/ws/bar` push reflect all of it.

## 2. Phase-level invariants (the cross-release coherence contract)

These are the properties that must hold ACROSS the five releases — each is
guarded by smoke check 27 and/or the phase-gate E2E:

1. **Additive schema discipline.** `commands.json` stays schema v1 from BF-1
   through BF-4: `workspace_id` (BF-2), `submitter` (BF-4) and the composed
   view fields (`execution`, `approval`) are all additive `omitempty` — a BF-1
   binary loads a BF-4 file and vice versa. No migration was ever claimed.
2. **One file discipline per store.** `commands.json`, `workspaces.json`,
   `executions.json`, `approvals.json` — all grants.json discipline: schema v1,
   0600/0700 fail-closed load, atomic temp+rename persist with in-memory
   rollback, bounded, never deleted (files are reviewable history).
3. **Closed enumerations everywhere.** Command states, workspace states,
   execution records (terminal-only), approval states (deliberately NO
   withdrawn) — a state no surface can reach is a lie.
4. **One execution per command.** One terminal execution record per command,
   enforced at request time AND at load time; approve reconciles against an
   existing record — no act runs twice through any door.
5. **One approval request per command.** Enforced at request time AND at load
   time; terminal decisions are never re-decided.
6. **The approval boundary IS the runtime boundary.**
   `ApprovalRequiredForCapability` is DERIVED from the closed runtime mapping —
   the two policies cannot drift. Unknown capability → approval REQUIRED
   (fail-closed, never silent auto-execution).
7. **Denial-is-200 end to end.** A kernel denial is a DECIDED outcome with the
   fired rule — at submit, at execute (audited SKIPPED), at approve
   (APPROVED+SKIPPED, the honest "operator allowed, kernel did not").
8. **Audit-before-serve / audit-before-record.** The evaluation is persisted
   before the decision is served (BF-1) or the execution record exists (BF-3,
   BF-4) — fail-closed ordering.
9. **Attribution, not credentials.** `submitter`/`approver` labels are bounded
   attribution; the bearer token remains the only authentication boundary.
10. **Isolation is a property you can reason about.** A session belongs to at
    most ONE workspace; scoped routing sees member sessions only — an
    ACTIVE-but-non-member session is invisible (that IS the isolation).

## 3. Automated acceptance (v19.5.4)

Verified on the tagged tree (see the release notes for the stamped SHA):

- [x] `gofmt -l .` returns no files
- [x] `go vet ./...` clean
- [x] all Go binaries build (`go build ./...`)
- [x] `go test ./...` — all packages green (21+ incl. `internal/agentfabric`)
- [x] `go test -race` on api/agentfabric/webui/agentkernel green
- [x] smoke suite offline — **27 checks, 0 FAIL, 0 WARN** (check 27 =
      phase-closure coherence, with a verified negative control)
- [x] phase-gate E2E on the real binary — one continuous journey through the
      full Phase B surface incl. restart persistence (see the release notes
      for the count)
- [x] all GitHub Actions jobs green on the tag and on `main` after merge

## 4. Honest open items (carried forward, NOT silently closed)

| Item | Why it is open | Where it is tracked |
|---|---|---|
| Target-host hardware acceptance (KPK-06) | CI green ≠ hardware verified; the acceptance protocol requires the real host | `docs/HARDWARE_ACCEPTANCE_PROTOCOL.md` |
| Coding-act COMPLETED path on the real binary | The host bridge owns `/var/lib/scarlix/bridge-input` (not mounted in the sandbox); on the real binary the mode act honestly FAILEDs — the COMPLETED path is unit-covered (`NewForTest`) | BF-3/BF-4 design docs, §4 Evidence |
| Kernel sessions are in-memory | Only grants + audit persist (the AK split); an orphaned PENDING approval after a restart: approve fails closed (409 "session vanished"), reject stays the 201 escape hatch | BF-4 design doc, non-goals |
| No LLM anywhere in the fabric | Deliberate — the LLM layer (proposing intents/routing) is Phase C, above the deciding, auditable fabric | Phase C plan (below) |

## 5. Release decision

Phase B (Agent Fabric) is **closed** as of v19.5.4: BF-1…BF-5 merged, tagged,
released with green CI; the phase-level invariants are guarded by smoke check
27 and the phase-gate E2E; the honest open items are ledgered above and carry
into the phase-C plan. The next theme is **C — Universal AI Runtime
(v19.6.x)**: the LLM layer that PROPOSES intents and routing to this fabric —
the fabric + kernel remain the deciding, auditable path.
