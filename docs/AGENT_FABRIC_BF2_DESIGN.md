# ScarLiX OS — Agent Fabric BF-2 Design (v19.5.x line)

**Branch:** `phase-b/agent-fabric/bf2-workspaces` → trunk `phase-b/agent-fabric` → main as **v19.5.1**
**Status:** BF-2 complete — Agent Fabric theme (v19.5.x) continues
**Phase B plan:** BF-1 command/intent lifecycle (v19.5.0) · **BF-2 agent workspaces (this release)** ·
BF-3 coding/research agent runtimes · BF-4 approvals · BF-5 phase closure (each with green CI +
smoke before merge)

---

## 1. Where this fits

BF-1 (v19.5.0) established the ScarliBar command lifecycle: an intent is
captured (persisted FIRST — never lost), routed deterministically to an
ACTIVE kernel session, kernel-decided (allow AND deny are both DECIDED with
the fired rule) and audited before the decision is served.

BF-2 adds the missing half of the Agent Fabric plan: **workspaces — named,
persisted isolation contexts.** A workspace is where agent sessions and
commands live together in isolation:

- a kernel session attaches to at most ONE workspace (**unambiguous routing
  namespace** — isolation is a property you can reason about, not a
  filter you hope is right);
- a command submitted with a `workspace_id` routes ONLY through that
  workspace's member sessions;
- a command submitted without one keeps the exact BF-1 behavior (unscoped —
  the additive-field contract: the surface is strictly backward compatible).

The P4 ownership table is unchanged: ScarliBar owns intent capture and
user-visible status; the kernel owns permission evaluation; the fabric owns
lifecycle and now topology. No execution anywhere — that lands with the
agent runtimes (BF-3+).

## 2. BF-2 contract (as implemented in `internal/agentfabric` + `/api/bar/workspaces*`)

### Workspace model (`workspace.go`)

- `WorkspaceID` — "ws-" + 16 crypto-random bytes hex (35 chars); RNG failure
  aborts creation (`ErrWorkspaceRNG`, the cmd-/aks- precedent).
- `WorkspaceState` — closed enumeration: `ACTIVE → ARCHIVED`. **ARCHIVED is
  a terminal tombstone**: the workspace and its command history stay
  readable (live commands stay cancellable — a user act on visible state),
  but no new submissions, no routing, no membership changes. Re-activation
  is deliberately impossible — a fresh workspace is a fresh context (the
  "file is the history" discipline: states never move backwards).
- `WorkspaceRecord` — id, name (≤ 64 runes), description (≤ 256 runes,
  optional), state, membership (`Sessions []string`, join order,
  deduplicated, every member an aks- shape), created/archived/updated
  timestamps. `validate()` enforces all invariants at load AND after every
  mutation; text fields reject control characters (JSON file, REST
  response, dashboard DOM — same rule as the intent).
- One workspace per session is a CROSS-RECORD invariant: checked at join
  time (`ErrSessionClaimed`) and re-validated at load ("claimed by both" —
  a hand-edited file that double-claims a session aborts startup).

### Workspace store (`wstore.go`)

The commands.json/grants.json discipline, exactly:

- JSON file, explicit schema version 1 (unknown versions rejected —
  explicit migration required); default location is `workspaces.json` NEXT
  TO the commands file (same 0700 directory); overridable via
  `SCARLIHQ_AGENTFABRIC_WORKSPACES_FILE`.
- 0600 file / 0700 directory, checked at load AND before every persist;
  atomic temp+rename persistence with fsync; a failed persist rolls back
  the in-memory mutation (state and file stay in sync).
- Bounded: `DefaultMaxWorkspaces = 64` records (all states — archived
  workspaces are real state), `MaxSessionsPerWorkspace = 32` members.
- Workspaces are never deleted — ARCHIVED is the reviewable tombstone.
- Returned records are deep copies (Sessions slice re-allocated) — callers
  cannot mutate store state through a view.
- The store does NOT know the kernel: session existence and ACTIVITY are
  resolved by the API layer at join/route time (the store checks only the
  aks- shape at load — integrity, not semantics).

### Command scoping (`command.go` / `store.go` changes)

- `CommandRecord.WorkspaceID` — optional field, `omitempty`. Empty =
  unscoped (BF-1); non-empty = the isolation scope, validated for shape at
  submit and at load.
- **The commands file schema stays version 1** — deliberately: the added
  field is optional in BOTH directions (a v19.5.0 binary reads a BF-2 file
  — the field is ignored; a BF-2 binary reads a v19.5.0 file — the field
  is empty). Nothing existing changes meaning, so no migration is needed
  and none is claimed. Test-asserted in both directions (BF-1-format file
  loads; scoped file round-trips).
- New read methods: `ListLastWorkspace(n, ws)` (newest-first, one scope;
  empty ws = the unscoped namespace), `WorkspaceCounts()` (commands per
  workspace for the list surface), `CountsWorkspace(ws)` / `CountWorkspace(ws)`
  (scoped state counts and totals).

### REST surface (`api/agentfabric_ws.go`, routes in `rest.go`)

| Route | Method | Contract |
|---|---|---|
| `/api/bar/workspaces` | GET | list newest-first (creation order reversed) + per-workspace `commands_count` (read surface composes the two stores) + store path; 503 until the fabric is configured |
| `/api/bar/workspaces` | POST | `{name, description?}` → 201 ACTIVE view (name/description bounds validated early for clean 400s; the store re-validates) |
| `/api/bar/workspaces/archive` | POST | `{workspace_id}` → ARCHIVED (terminal); 409 when already archived; 404 unknown |
| `/api/bar/workspaces/sessions` | POST | `{workspace_id, session_id}` → join. The session must EXIST in the kernel (400 unknown — existence is the kernel's business); 409 already-member / claimed-by-another-workspace / archived; 503 membership bound |
| `/api/bar/workspaces/sessions/leave` | POST | `{workspace_id, session_id}` → detach (409 non-member / archived) |

Scoped submission and routing (changes in `api/agentfabric.go`):

- `POST /api/bar/commands` accepts optional `workspace_id`. A scoped
  submission requires a REAL, ACTIVE workspace — checked BEFORE the intent
  is captured (a bad scope is a client error, like a bad capability; the
  never-lost contract covers routability, not malformed requests): 404
  unknown workspace, 409 ARCHIVED, 400 malformed shape, 400 named session
  that is not a member.
- Routing (`resolveWorkspaceScopedSession`): the named session must be a
  MEMBER and ACTIVE for the agent, or — when none is named — the NEWEST
  ACTIVE member session. A session that is ACTIVE in the kernel but NOT a
  member is invisible to scoped routing (that is the isolation). No
  routable member → the intent is captured SUBMITTED (never lost).
- `POST /api/bar/commands/route` honors the scope: ARCHIVED workspace →
  409 (routing closed, the command stays visible and cancellable); no
  ACTIVE member → 409 retryable (join or activate first). A hand-edited
  commands file referencing a vanished workspace gets a fail-closed 409 —
  routing into a nonexistent scope is refused, never guessed.
- `GET /api/bar/commands?workspace_id=<id>` filters to one scope (scoped
  counts/totals); the literal `unscoped` selects the unscoped namespace;
  an unknown id is a 404 (a typo is a client error, not a silently-empty
  list). Without the parameter the BF-1 surface (all commands) is served
  unchanged.
- **Workspace acts (create/archive/join/leave) are NOT kernel audit
  events** — they are persisted topology, reviewable in workspaces.json
  itself. The kernel audit sink stays what it is: evaluations and admin
  acts on grants. No mixed semantics, honestly scoped.

### Dashboard panel (`frontend/dist/index.html`)

- The ScarliBar row gains a workspace selector (`— unscoped —` + the
  live workspace list, kept in sync by the 5s poll; selecting a scope
  filters the commands table and scopes the next submission).
- A new **Agent Workspaces** card: create row (name + description +
  Create, 44px touch targets), workspace table newest-first (name +
  description, state pill ACTIVE/ARCHIVED, member session pills with ✕
  detach buttons, commands count, join picker fed from ACTIVE kernel
  sessions + join + archive actions). ARCHIVED rows show "terminal —
  history visible" instead of actions.
- Every dynamic field goes through `escapeHTML` (the XSS convention);
  503 → explicit "workspaces disabled" empty state (the fabric opt-in
  carries them).

### Enablement (`main.go`, compose)

The workspace store **rides the fabric opt-in — no separate flag**:
whenever `SCARLIHQ_AGENTFABRIC_ENABLED=true` and the commands file are
set, the workspace store is constructed, loaded (fail-closed Fatal on a
tampered file) and configured. Its file defaults to `workspaces.json`
next to the commands file; `SCARLIHQ_AGENTFABRIC_WORKSPACES_FILE`
overrides the location for operators who want the stores on separate
volumes. The compose opt-in block documents the env + the shared mount.

## 3. Honest non-goals (BF-2)

- No execution — an ALLOWED scoped command is still a kernel DECISION.
  Execution plumbing arrives with the agent runtimes (BF-3+).
- No workspace deletion, no history rewrite, no re-activation of ARCHIVED
  workspaces — the file is the history.
- No multi-principal submitter identity (unchanged from BF-1;
  single-principal bearer token; both store files are 0600 server-side).
- No per-workspace grant policy: the kernel evaluates capabilities the
  same way inside and outside workspaces — isolation narrows ROUTING
  (which sessions may act for an intent), not PERMISSIONS (what the kernel
  allows). Scoped grant policy would be a kernel-line feature, proposed
  separately if ever needed.
- Workspace acts are not audit-sink events (see above — persisted topology
  is its own review trail).

## 4. Verification on this branch

- `gofmt` clean, `go vet` OK, `go test ./...` — all 20+ packages green;
  `go test -race` on agentfabric + api — OK.
- `internal/agentfabric` tests: workspace ID/name/description validation
  matrices (shapes, bounds, control characters), record invariant matrix
  (state/field presence, monotone clocks, member shape/duplicates),
  lifecycle round-trip (create → join → cross-claim refusal → leave →
  re-claim → archive terminal: membership mutations on archived refused),
  membership bound (32), store capacity bound, RNG fail-closed injection,
  load fail-closed (malformed JSON, wrong version, bad records, unknown
  state, duplicate ids, session claimed by two workspaces, over-bound,
  loose file modes 0640/0644/0666, loose directory), persist-failure
  rollback (read-only dir → rollback → reload shows the pre-failure
  state), deep-copy isolation (Get/List), first-run file initialization
  (0600), scoped commands (scope persisted, filters, counts, newest-first
  per scope, reload round-trip), BF-1-format file loads unchanged
  (additive-field contract).
- `internal/api` tests: workspace routes auth-gated (401) and 503 without
  the store; create validation matrix (empty/oversized/control-char name,
  oversized description, unknown field, malformed JSON); list newest-first;
  join/leave/duplicate/cross-claim/unknown-session/unknown-workspace;
  archive + double-archive + join-into-archived (409s); method discipline
  (405); scoped submission routed through the MEMBER while a NEWER ACTIVE
  outsider exists (and unscoped submission routed through the NEWEST —
  both contracts in one fixture); named non-member submission → 400;
  scope filter totals; unknown filter → 404; route endpoint (409 with no
  member despite an ACTIVE unscoped session → 200 DECIDED through the
  member after join; archived workspace closes routing but cancellation
  still works); persistence across a handler reload on the same files
  (workspaces + membership + scoped commands + composed counts).
- Smoke check 24 (deterministic, source-level): fail-closed load
  sentinels, schema versioning, atomic persistence, both bounds, one-
  workspace-per-session enforcement, ARCHIVED terminality, member-only
  routing resolver, ARCHIVED refusals at submit/route, the optional scope
  field, main.go wiring, 4 auth-gated routes, compose documentation,
  dashboard selector + card. Negative control verified: breaking the
  main.go wiring line FAILS the check (rc=1) → revert → rc=0.
- E2E on the real binary (auth 401; create/join/cross-claim 409; scoped
  submit DECIDED through the member with the scope recorded; unscoped
  submit unchanged; filter totals; unknown filter 404; archive terminal —
  submit 409, double archive 409; workspaces.json 0600/schema-1 with the
  membership persisted; **restart → workspaces, membership, scopes and
  the archived tombstone all survive**): 25/25 PASS, no panics/fatals in
  the server log.
