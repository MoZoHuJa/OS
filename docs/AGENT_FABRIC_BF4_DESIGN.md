# ScarLiX OS — Agent Fabric BF-4 Design (v19.5.x line)

**Branch:** `agent-fabric/bf4-approvals` → trunk `phase-b/agent-fabric` → main as **v19.5.3**
**Status:** BF-4 complete — Agent Fabric theme (v19.5.x) continues
**Phase B plan:** BF-1 command/intent lifecycle (v19.5.0) · BF-2 agent workspaces (v19.5.1) ·
BF-3 coding/research agent runtimes (v19.5.2) · **BF-4 operator approvals (this release)** ·
BF-5 phase closure
(each with green CI + smoke before merge)

---

## 1. Where this fits

BF-3 closed the execution gap: a DECIDED, kernel-ALLOWED command executes
under its closed runtime (research read-only / coding bridged) with a
FRESH kernel evaluation and a terminal, persisted, user-visible record.
But BF-3 executes a coding act on a single POST — the same request that
captures the intent also performs the mutation.

BF-4 inserts the missing human decision: **a coding (mutating) act never
runs without an explicit operator yes.** The execute surface on a coding
capability now CREATES the approval request (params captured, submitter
labeled) and stops; the operator APPROVES (the act runs — the full BF-3
flow composed with the operator's decision) or REJECTS (terminal, no act
ever). Research acts are read-only by construction — they execute
directly, exactly as in BF-3; no approval request is ever created for
them.

The P4 ownership table is unchanged: ScarliBar owns intent capture and
user-visible status; the kernel owns permission evaluation; the fabric
owns lifecycle, topology, execution and now the approval gate. The
bearer token remains the ONLY authentication boundary — the new labels
(submitter, approver) are ATTRIBUTION for the reviewable history, never
credentials.

## 2. BF-4 contract (as implemented in `internal/agentfabric` + `/api/bar/approvals` + the execute gate)

### The approval boundary IS the runtime boundary (`approval.go`)

- `ApprovalRequiredForCapability` is **DERIVED from the closed runtime
  mapping** — it answers exactly the capabilities bound to
  `RuntimeCoding`. This is a derivation, not a second literal map: the
  approval policy and the runtime mapping are ONE policy and cannot
  drift. A capability that needs different treatment is a
  runtime-mapping change, not an approval-map guess. Unknown capabilities
  answer REQUIRE (fail-closed — never a silent auto-execute).
- **Closed state enumeration:** `PENDING` (requested, awaiting the
  operator — the ONLY in-flight state in the whole fabric, deliberately:
  the approval request is the thing a human is holding, the execution
  record stays terminal-by-construction), `APPROVED` (the operator said
  yes; the act ran — `execution_id` references the terminal record,
  ALWAYS, even the audited SKIPPED of a fresh denial: **APPROVED +
  SKIPPED is the honest composition "the operator allowed it, the kernel
  did not"**), `REJECTED` (the operator said no; terminal, no act ever).
- **Deliberately NO withdrawn state.** A PENDING request's command is
  DECIDED — terminal in the command lifecycle, it can never be cancelled
  after the fact — and commands are never deleted. The request therefore
  cannot drift out from under the operator: it stays answerable (approve
  or reject) until the operator decides, and a request nobody answers
  stays PENDING in the reviewable history. A state no surface can reach
  would be a lie in the enumeration.
- `ApprovalID` is "apv-" + 16 crypto-random bytes hex (the cmd-/ws-/afx-
  precedent; RNG failure aborts the record, never a predictable ID).
- **Principal labels** (`ValidatePrincipalLabel`, ≤ 64 runes, no control
  characters): submitter (who captured the intent — rides the command,
  additive + omitempty, the commands schema stays v1) and approver (who
  answered). Labels are attribution, not credentials — single-principal
  deployments, one bearer token.
- Record invariants are enforced at load AND before every persist: the
  state↔fields coherence (PENDING carries no approver/execution/decision
  time; APPROVED requires approver + execution_id + decided_at; REJECTED
  requires approver + decided_at, NEVER an execution), the
  runtime↔capability coherence (the closed mapping is record integrity,
  exactly like execution records), and the params↔capability coherence
  (the closed shape captured at REQUEST time: mode.request carries mode
  only, compute.reserve/release carry allocation_id only — a research
  capability in an approval record is corruption, not policy).

### One approval request per command, ever (`astore.go`)

approvals.json — the commands/workspaces/executions/grants discipline,
exactly:

- explicit schema version 1 (unknown versions rejected — explicit
  migration required); default location `approvals.json` NEXT TO the
  commands file; overridable via `SCARLIHQ_AGENTFABRIC_APPROVALS_FILE`.
- 0600 file / 0700 directory — checked at load AND before every persist;
  atomic temp+rename persistence with fsync; a failed persist rolls back
  the in-memory mutation (state and file stay in sync).
- Bounded: `DefaultMaxApprovals = 256` (coding acts are sparser than
  commands; at capacity Request fails with `ErrApprovalLimit` → HTTP
  503; the operator archives deliberately).
- Approvals are never deleted — the file is the reviewable history of
  every request, approval and rejection.
- **ONE REQUEST PER COMMAND, enforced twice**: Request refuses a second
  request for a command id (`ErrAlreadyRequested` → HTTP 409) and Load
  aborts on a file that carries two approvals for the same command — a
  hand-edited duplicate is corruption, not "some approvals work."
- Terminal decisions are never re-decided: Approve/Reject operate
  exclusively on PENDING records (`ErrApprovalState` → HTTP 409).

### The execute gate (`agentfabric_exec.go`)

`POST /api/bar/commands/execute` runs the full BF-3 preamble (CommandID
shape 400-vs-404, DECIDED+allowed gates, one-execution check, ARCHIVED
tombstone, closed params validation) and THEN gates:

- **coding capability → `barApprovalRequest`**: the ONE approval request
  is created and persisted; **202 Accepted** + the PENDING view (params
  captured, submitter labeled, requested_at). The act has NOT run — no
  execution record exists, the operator holds the decision. An existing
  request answers the honest 409 (PENDING: awaiting the operator;
  REJECTED: the command's execution path is closed, retry is a NEW
  intent at the bar). A missing approval store answers **503** — a
  mutating act never silently bypasses the human gate.
- **research capability → the unchanged BF-3 flow** (fresh evaluation,
  the act, the terminal record — read-only by construction needs no
  operator).

### Approve runs the act (`agentfabric_appr.go`)

`POST /api/bar/approvals/approve` composes the operator's yes with the
FULL BF-3 execute flow — nothing is skipped:

1. ApprovalID **shape first** (400 vs 404), PENDING gate (409 on
   terminal), approver label required (400).
2. The command gates RE-CHECKED (the world moved between request and
   approve): command exists (rotated file → 409, fail-closed), DECIDED +
   allowed (409 otherwise), ARCHIVED scope (409).
3. **Reconciliation:** a terminal execution already exists for the
   command (a crashed approve AFTER the execution persist) → the
   approval closes APPROVED referencing the EXISTING record, no new act
   — `{"approval","execution","reconciled":true}`. **No act runs twice
   through that door.**
4. **The act re-uses the STORED params** — an approval decision cannot
   re-steer the act (the approved thing is the requested thing; the
   approve request carries NO params, unknown fields rejected).
5. **STALE-ALLOW GUARD at approve time** — a FRESH kernel evaluation
   (the world moved since the request): session suspended, grant
   revoked, capability disabled since → the fresh denial is an **audited
   SKIPPED** and the approval closes APPROVED referencing it (APPROVED +
   SKIPPED — the operator allowed it, the kernel did not). A vanished
   session → 409 "session vanished before approval" (fail-closed: an
   unauditable act never runs; the escape hatch is REJECT, which needs
   no kernel).
6. The fresh evaluation is persisted to the AK-4 audit sink BEFORE any
   execution record exists (fail-closed on audit write: 500, no record,
   no act).
7. The act runs (the same runtime acts — mode bridge, compute bridges
   under the registry-declared owner): success → COMPLETED, act error →
   FAILED (honest terminal outcomes, surfaced, never retried).
8. The terminal execution record is persisted BEFORE the approval
   closes; a crash between the two persists takes the reconciliation
   path on retry.
9. **201** + `{"approval","execution"}` (+ bus signals
   `execution.recorded`, `approval.decided`).

### Reject is unconditional (`agentfabric_appr.go`)

`POST /api/bar/approvals/reject` — the operator's no needs NO kernel
evaluation (no act runs, there is nothing to evaluate): approval PENDING
gate (404/409), approver label required, optional bounded reason (the
why, for the history), **201** + the REJECTED view (never an execution
reference — the validate invariant). A REJECTED request closes the
command's execution path: re-execute → 409 (retry is a NEW intent at the
bar). This is also the escape hatch for orphaned PENDING requests (e.g.
the in-memory kernel session vanished in a restart — approve fails
closed, reject always answers).

### The read surface + composed views

- `GET /api/bar/approvals` — bounded newest-first list
  (`?limit=1..200`, default 200), closed filters (`?state=PENDING|
  APPROVED|REJECTED` — unknown → 400; `?command_id=` — malformed → 400,
  unknown command → 404), counts per state always present, store_path.
- The approval view composes the terminal execution exactly when the
  approval is APPROVED and the referenced record exists (nil-safe).
- The command view composes the additive `approval` field (summary —
  id/state/submitter/approver/reason/execution_id/requested_at;
  `omitempty`; present exactly when the command carries a request —
  coding acts only). BF-1/BF-2/BF-3 clients and files are unaffected.

### Bus signals + dashboard

- `approval.requested` / `approval.decided` join the CHANGE namespace
  (`command.*`/`execution.*`); `fabric.*` stays CONTROL
  (subscribed/dropped). The dashboard's `/ws/bar` client refetches the
  bar AND the approvals card on approval.* signals (the LIVE CATCH
  discipline: never refetch on fabric.*).
- Dashboard: the **Operator Approvals card** — the approver label input
  (attribution), the request rows (intent, captured params, agent ·
  capability, submitter, state pill, the composed terminal execution on
  APPROVED rows), approve/reject actions on PENDING rows (44px touch),
  5s polling fallback. The bar's exec button disappears once the
  approval request exists (the decision moved to the approvals card);
  the bar's decision column carries the approval state pill. All
  dynamic fields escaped (XSS convention).

## 3. Honest non-goals (BF-4)

- **No multi-principal authentication.** Labels are attribution;
  deployments stay single-principal (one bearer token). Real approver
  identity lands with a future auth line; the file records what the
  operator DECLARED, honestly labeled as such.
- **No cross-file atomicity is claimed.** Ordering is honest: fresh
  evaluation → audit append (fail-closed) → execution record persist →
  approval close persist → response. A crash between the last two
  persists is the reconciliation path (approve retries close the
  approval against the existing execution; no act runs twice).
- **No timeout/expiry on PENDING requests.** A request the operator
  never answers stays PENDING in the reviewable history — an expiry
  would silently turn a non-decision into a decision. The operator
  closes it (or the file rotates deliberately).
- **No quorum / threshold approvals.** One operator decides; the
  enumeration has no room for a half-decision.
- **No re-steering at approve time.** The approve request carries no
  params — the act re-uses exactly what was captured at request time.
  A changed intent is a NEW command (the whole point of the gate).

## 4. Evidence

- Go: 21 test packages green (incl. `-race` on
  api/agentfabric/webui/agentkernel); gofmt clean, vet 0. 33 new test
  functions (approval model/store/API: closed enumeration + no
  withdrawn, derived boundary, state↔fields coherence, fail-closed
  load ×12+ corruption payloads, atomic rollback, bound, RNG, one
  request per command, 202 capture, approve-act composition, APPROVED+
  SKIPPED, reconciliation, unconditional reject, filters/counts,
  restart persistence, bus signals, submitter validation).
- Smoke check 26 (source-level: closed PENDING/APPROVED/REJECTED
  enumeration with the NO-withdrawn negative, derived
  ApprovalRequiredForCapability, principal labels, state↔fields and
  params coherence, fail-closed store, one-per-command load-enforced,
  the execute gate + 202 + 503-without-store, approve-time stale-allow
  guard + audited SKIPPED + stored-params + reconciliation,
  unconditional reject, SHAPE-first 400/404, closed filters, additive
  submitter + composed approval field, bus signals + no withdrawn
  signal, auth-gated routes, main.go wiring + approvals.json default,
  compose, dashboard card/actions/poll/WS filter) — **39 PASS / 0 FAIL /
  0 WARN** on 26 checks; negative control verified (breaking
  `ConfigureAgentFabricApprovals` in main.go → 1 FAIL → revert → 0
  FAIL).
- E2E on the real binary: **134/134** — auth gates; registry-mode grants
  + session (+ activation); submitter labels (labeled/unlabeled/control-
  char 400/65-runes 400); research executes DIRECTLY (no approval ever);
  coding execute → 202 PENDING (params captured, submitter labeled, no
  decision fields, no execution record yet); re-execute 409s (PENDING/
  REJECTED/already-executed); approve validation (400/404/unknown-field-
  cannot-re-steer); approve runs the act (201; on the real binary the
  mode act honestly FAILEDs — the host bridge owns
  /var/lib/scarlix/bridge-input, not mounted in the sandbox; the
  COMPLETED path is unit-covered); re-approve/reject-after-approved 409s;
  reject unconditional + terminal (no execution record for the rejected
  command; path closed 409); APPROVED+SKIPPED (session SUSPENDED between
  request and approve — the fresh denial audited, reason session-state);
  honest FAILED acts (invalid mode; compute.reserve without the compute
  lifecycle); read surface (filters/counts/400s/404s/store_path);
  composed approval field on command views; audit evaluate lines (≥ 12);
  WS `/ws/bar` (approval.requested + approval.decided push, single-use
  ticket reject); disk discipline (0600/0700/schema v1, reason and
  submitter persisted); RESTART (approvals history + terminal decisions
  survive; the ORPHANED PENDING — kernel sessions are in-memory —
  approve fails closed 409 "session vanished", reject stays the 201
  escape hatch).
