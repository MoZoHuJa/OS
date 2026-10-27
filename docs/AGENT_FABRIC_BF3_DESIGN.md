# ScarLiX OS — Agent Fabric BF-3 Design (v19.5.x line)

**Branch:** `agent-fabric/bf3-runtimes` → trunk `phase-b/agent-fabric` → main as **v19.5.2**
**Status:** BF-3 complete — Agent Fabric theme (v19.5.x) continues
**Phase B plan:** BF-1 command/intent lifecycle (v19.5.0) · BF-2 agent workspaces (v19.5.1) ·
**BF-3 coding/research agent runtimes (this release)** · BF-4 approvals · BF-5 phase closure
(each with green CI + smoke before merge)

---

## 1. Where this fits

BF-1 established the ScarliBar command lifecycle: an intent is captured
(persisted FIRST — never lost), routed deterministically to an ACTIVE
kernel session, kernel-decided (allow AND deny are both DECIDED with the
fired rule) and audited before the decision is served. BF-2 added
workspaces — named, persisted isolation contexts that narrow WHICH
sessions a scoped intent routes through.

One thing no fabric release has done yet: **execute anything.** BF-1's
honest non-goal was explicit — "an ALLOWED command is a kernel DECISION,
not an execution." BF-3 closes that gap with the coding/research agent
runtimes: the machinery that turns a DECIDED, kernel-ALLOWED command into
an executed act with a terminal, persisted, user-visible outcome.

The P4 ownership table is unchanged: ScarliBar owns intent capture and
user-visible status; the kernel owns permission evaluation; the fabric
owns lifecycle, topology and now execution. No free shell anywhere — the
runtimes execute through the SAME bridged surfaces the REST API uses.

## 2. BF-3 contract (as implemented in `internal/agentfabric` + `/api/bar/commands/execute` + `/ws/bar`)

### The closed runtime mapping (`agentruntime.go`)

- `RuntimeID` — closed enumeration: **research** and **coding**. No
  generic runtime: a capability that fits neither is a kernel change, not
  a mapping guess.
- `RuntimeForCapability` — THE closed mapping (a literal, not a
  computation — policy must be readable in one place):
  - research: `status.read`, `gaming.read`, `compute.plan` (the planner
    is side-effect free BY CONSTRUCTION — a dry-run never allocates);
  - coding: `mode.request`, `compute.reserve`, `compute.release`.
- `ValidateRuntimeMapping` — the load-time invariant: the mapping is
  TOTAL over the kernel's closed capability enumeration and neither
  runtime is empty. Called by main.go when the fabric is enabled and by
  the execution store's Load — **a drifted enumeration aborts startup,
  never a silent "some capabilities execute."**
- Closed params shapes (`ParseRuntimeParams`,
  `json.DisallowUnknownFields`): research capabilities take NO params
  (an act that only observes takes no steering); `mode.request` takes
  `{"mode"}` (1..32 runes, no control chars); `compute.reserve`/
  `compute.release` take `{"allocation_id"}` (1..128 chars, the AK-5
  intake bound). Unknown fields are rejected — a param shape is a
  contract, not a suggestion.

### Terminal execution records (`execution.go`)

An execution record is **TERMINAL BY CONSTRUCTION** — there is no
in-flight state, no PENDING, no RUNNING. The execute surface is
synchronous and bounded; the record is persisted exactly once, at the
end, in one of three states:

- **COMPLETED** — the fresh kernel evaluation allowed the act AND the act
  reported success. `Outcome` carries the bounded act summary (the
  status snapshot line, the mode-request acceptance, …). `Allow` is true.
- **FAILED** — the fresh evaluation allowed the act but the act itself
  returned an error (bridge rejection, disabled dependency, invalid
  argument at act time). `Reason` carries the act error. `Allow` is true
  (the act RAN — a fresh denial is a different state). A failed act is an
  honest terminal outcome, surfaced, not retried by the fabric: retry is
  a NEW intent at the bar (the file is the history).
- **SKIPPED** — the **stale-allow guard** fired: the command carries an
  old kernel decision, but the FRESH evaluation at execute time DENIED.
  The act never runs. `Reason` carries the fresh denial rule. `Allow` is
  false.

Record invariants are enforced at load AND before every persist (the
store discipline): state↔allow↔fields coherence (COMPLETED requires a
non-empty outcome and allow=true; FAILED requires a non-empty reason,
empty outcome, allow=true; SKIPPED requires a non-empty reason, empty
outcome, allow=false — a SKIPPED record with allow=true is a corrupt
file, not a degraded success) and **runtime↔capability coherence** (the
record's runtime must equal `RuntimeForCapability(capability)` — a
hand-edited file that relabels a coding execution as research aborts the
load).

`ExecutionID` is "afx-" + 16 crypto-random bytes hex (the cmd-/ws-/aks-
precedent; RNG failure aborts the record, never a predictable ID).

### The execution store (`estore.go`)

executions.json — the commands.json/workspaces.json/grants.json
discipline, exactly:

- explicit schema version 1 (unknown versions rejected — explicit
  migration required); default location `executions.json` NEXT TO the
  commands file; overridable via `SCARLIHQ_AGENTFABRIC_EXECUTIONS_FILE`.
- 0600 file / 0700 directory — checked at load AND before every persist;
  atomic temp+rename persistence with fsync; a failed persist rolls back
  the in-memory append (state and file stay in sync).
- Bounded: `DefaultMaxExecutions = 512` (at capacity Record fails with
  `ErrExecutionLimit` → HTTP 503; the operator archives deliberately).
- Executions are never deleted — the file is the reviewable history.
- **ONE EXECUTION PER COMMAND, enforced twice**: Record refuses a second
  execution for a command id (`ErrAlreadyExecuted` → HTTP 409) and Load
  aborts on a file that carries two executions for the same command — a
  hand-edited duplicate is corruption, not "some executions work."

### The stale-allow guard (the BF-3 heart)

`POST /api/bar/commands/execute` never trusts the command's stored
decision alone. The flow:

1. CommandID **shape first** (400 malformed vs 404 unknown — the filter
   precedent; a malformed id is a client error, a well-formed unknown id
   is a miss).
2. State gates: only DECIDED commands execute (SUBMITTED/ROUTED/CANCELLED
   → 409); a DECIDED **denial** is not executed (409 — a denial is a
   fact, not a wish); already-executed → 409 (terminal).
3. An **ARCHIVED workspace closes execution** for its scoped commands
   (409 — the same tombstone contract that closes routing; live commands
   stay visible and cancellable, never executable inside a closed
   context).
4. Closed params validation (400s).
5. **FRESH kernel evaluation** — the registry policy when configured
   (runtime kill-switches apply NOW: a capability disabled since the
   decision, a grant revoked since, a session suspended since — the world
   moved, and the fresh evaluation is the one that counts).
6. The fresh evaluation (allow AND deny) is persisted to the AK-4 audit
   sink BEFORE any execution record exists — an unauditable execution
   decision is never recorded (fail-closed on audit write: 500, no
   record, no act).
7. Fresh deny → audited **SKIPPED** (201 — the execution IS the result,
   the kernel's denial-is-200 contract carried to executions). Fresh
   allow → the act runs; success → **COMPLETED**, act error → **FAILED**
   (201 both — a failed act is an honest terminal outcome the bar
   surfaces, never a 5xx).
8. The terminal record is persisted BEFORE the response is served — a
   served view always exists on disk.

### The runtime acts (deterministic — no LLM in the decision OR act path)

- **research acts observe and report** (read-only by construction):
  `status.read` → a status snapshot line (mode, timestamp, stale flag);
  `gaming.read` → a gaming inventory snapshot line (entry count);
  `compute.plan` → a plan dry-run line (GPUs visible, mode — "no
  allocation created").
- **coding acts perform through the EXISTING bridged surfaces**:
  `mode.request` → the mode bridge (`Mode.Request` — the atomic
  desired-mode reservation; the host bridge applies it on its next
  tick); `compute.reserve`/`compute.release` → the workload bridge under
  the agent's **registry-declared owner** (the AK-5 composition — a
  caller-supplied principal is exactly what it prevents; the bridge
  enforces allocation ownership; requires the compute lifecycle AND the
  grant registry, honest errors otherwise).

### The fabric event bus (`fabricbus.go`) + `/ws/bar` (`webui/wsbar.go`)

- **Change signals, NOT state**: the bus carries one-line notifications
  (`command.submitted`, `command.routed`, `command.decided`,
  `command.cancelled`, `execution.recorded`) — type + reference + time,
  never the object. The WS client refetches through the auth-gated REST
  surface (the state authority). A signal that carried state would be a
  second, drift-prone copy of the state — deliberately not built.
- **Bounded 16×16, drop-not-block**: at most 16 subscribers, each with a
  16-slot buffered channel. Publish NEVER blocks the caller (REST
  mutation handlers must not stall on a slow WS client); a full queue
  silently drops and counts (`Dropped()` — surfaced by the disconnect
  control frame). A dropped signal costs a delayed refetch; the
  dashboard's 5s polling fallback already covers that failure mode.
- **No replay**: a new subscriber receives nothing that happened before
  it subscribed. Its first authoritative state comes from the REST
  refetch cued by the `fabric.subscribed` control frame (sent by the WS
  layer, never by the bus — `fabric.*` is the CONTROL namespace,
  `command.*`/`execution.*` are the CHANGE signals).
- `/ws/bar` shares the `/ws` ticket discipline (single-use ticket
  reserved BEFORE `upgrader.Upgrade`, never re-added on failure) AND the
  **wsSlots budget (16 concurrent WS clients across BOTH surfaces** — the
  bound is real state, not per-route decoration). A nil bus (fabric off)
  answers **503 before the upgrade** — the route is always registered,
  never a silently-dead push surface.

### REST + dashboard surface

- `POST /api/bar/commands/execute` — the flow above; 201 + terminal view.
- `GET /api/bar/executions` — bounded newest-first list
  (`?limit=1..200`), closed filters (`?state=COMPLETED|FAILED|SKIPPED`
  — unknown → 400; `?command_id=` — malformed → 400, unknown command →
  404, a real-but-never-executed command → the honest empty list), counts
  per state always present.
- The command view composes the execution (`execution` field, additive,
  `omitempty` — BF-1/BF-2 files and clients are unaffected; the view
  never lies about what exists).
- Dashboard: the exec button (DECIDED+allowed+not-yet-executed rows —
  it disappears once the terminal record exists), the execution pill +
  outcome/reason line under the decision, and the `/ws/bar` push client
  (refetch on `command.*`/`execution.*`; polling stays the reliable
  fallback — the push only makes the status FRESHER).

## 3. Honest non-goals (BF-3)

- **No LLM.** The runtimes are deterministic executors of kernel-allowed
  intents. The model arrives with Phase C (Universal AI Runtime); the
  fabric is deterministic first, like the kernel beneath it.
- **No multi-principal submitter identity.** Deployments are
  single-principal (one bearer token); the executions file is 0600
  server-side. Submitter identity lands with the approval UX line (BF-4).
- **No retry/queueing of failed acts.** One execution per command, ever;
  retry is a NEW intent at the bar. The file is the history.
- **No cross-file atomicity is claimed.** Ordering is honest: fresh
  evaluation → audit append (fail-closed) → execution record persist →
  response. A persist failure after a successful audit append leaves the
  evaluation audited and no execution recorded (500) — no partial
  records, no silent degradation.
- **No per-workspace grant policy.** Workspaces narrow ROUTING and (with
  ARCHIVED) execution membership; permission evaluation is the kernel's,
  inside and outside scopes, identically.
- **compute.plan is a snapshot dry-run.** The full planner integration
  (allocation shape proposals) arrives with the compute fabric's own
  line; BF-3's dry-run is side-effect free and says so in its outcome.

## 4. Evidence

- Go: 21 test packages green (incl. `-race` on api/agentfabric/webui);
  gofmt clean, vet 0.
- Smoke check 25 (source-level: closed mapping, terminal states,
  state↔allow↔runtime coherence, fail-closed store, fresh evaluation,
  audited SKIPPED, ARCHIVED, params, bounded bus, WS
  ticket/slots/503-nil-bus, routes, wiring, compose, dashboard) —
  **38 PASS / 0 FAIL / 0 WARN** on 25 checks; negative control verified
  (breaking `SCARLIHQ_AGENTFABRIC_EXECUTIONS_FILE` in main.go → 1 FAIL →
  revert → 0 FAIL).
- E2E on the real binary: **86/86** — auth gates, registry-mode grants +
  sessions (+ the CREATED→ACTIVE activation step), sync-DECIDED submit,
  research execution (COMPLETED, snapshot outcome), compute.plan dry-run,
  composed view, executions list+counts+filters, 400 vs 404 shapes,
  SUBMITTED/CANCELLED/DENIED/already-executed 409s, params 400s, the
  stale-allow guard (session SUSPENDED between decide and execute → 201
  SKIPPED allow=false, audited), mode.request act (COMPLETED + honest
  FAILED on invalid mode), compute.reserve honest FAILED without the
  compute lifecycle, audit evaluate lines, WS ticket + `/ws/bar`
  (fabric.subscribed + command.submitted push + single-use ticket
  rejection), disk discipline (0600/0700/schema v1), RESTART (executions
  history, composed views, command states, the one-execution-per-command
  invariant — all survive).
