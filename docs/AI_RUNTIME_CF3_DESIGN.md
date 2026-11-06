# ScarLiX OS — Universal AI Runtime CF-3 Design (v19.6.x line)

**Branch:** `ai-runtime/cf3-propose` → main as **v19.6.2**
**Status:** CF-3 complete — Phase C continues (the propose surface: an LLM
response becomes a fabric PROPOSAL — never an auto-submitted command)
**Plan source:** `docs/AI_RUNTIME_CF1_DESIGN.md` §2 (the CF-3 row) · the
CF-2 closing line ("an LLM response becoming a fabric PROPOSAL is CF-3,
behind the fabric's approvals") · the Phase C charter: *"LLM navrhuje,
politika rozhoduje"* — the LLM proposes; policy decides.

---

## 1. Where this fits

CF-1 landed the typed contract (v19.6.0); CF-2 landed the transport —
real local-stack adapters, health probing, the auth-gated failover chat
(v19.6.1). But the CF-2 chat is deliberately TRANSPORT ONLY: it mutates
nothing on the host, and the response text goes back to the caller and
stops there.

CF-3 is the bridge the whole Phase C exists for: **an LLM response can
become a fabric PROPOSAL — and a proposal is never a command.** The
operator reviews it and either ACCEPTS (the proposal becomes a captured
command through the exact BF-1 submission path — routing, the kernel
decision, the BF-4 approval gate all still apply downstream, untouched)
or DISMISSES it (terminal, no command ever). The line is the one the
phase opened with: the LLM proposes; policy decides. The model NEVER
holds a pen that writes into the fabric — the operator's accept is the
only door from a proposal to a command.

The P4 ownership table is unchanged: ScarliBar owns intent capture; the
kernel owns permission evaluation; the fabric owns lifecycle. CF-3 adds
one entity to the fabric — the proposal, the model-authored draft intent
— plus the pure parser that extracts it from a normalized response and
the REST surface that captures, reviews and decides it.

## 2. The contract (as implemented)

### The propose endpoint — chat + capture, nothing more
`POST /api/ai/propose` takes EXACTLY the chat request shape (model +
messages, the same closed decode, the same 1 MiB body bound) and runs it
through the CF-2 runtime unchanged (failover, attribution, the same
error mapping — 404 unknown model, 503+skips no-route, 502+attempts
all-failed, 400 shape). Then the response content is parsed for a
structured proposal (below). Three outcomes, all honest:

- **well-formed proposal** → one PENDING `ProposalRecord` is persisted
  (attribution: which provider AND which model produced it — attribution
  survives failover) → **200** with the full chat response + the
  proposal view;
- **no proposal block in the response** → nothing captured → **200**
  with the chat response + `"proposal": null` (the model answered and
  proposed nothing — null IS the answer, not an error);
- **malformed proposal** (bad JSON, unknown/missing fields, invalid
  values, TWO blocks — ambiguity is refused) → **422**, NOTHING
  captured, with the named error + the provider/model attribution + the
  response content (the caller sees which tier said what and why it was
  refused — fail-closed, not fail-blind).

The propose surface requires BOTH opt-ins fail-closed (503): the AI
runtime (`SCARLIHQ_AIRUNTIME_ENABLED=true`) and the fabric's proposal
store (`SCARLIHQ_AGENTFABRIC_ENABLED=true` — the proposal is a fabric
entity; the store rides the fabric opt-in exactly like approvals.json).

### The parser — pure, deterministic, never a guess
`ParseProposal(content string)` scans the assistant response for exactly
ONE fenced code block tagged `scarlix-proposal`:

    ```scarlix-proposal
    {"intent": "...", "agent_id": "...", "capability": "..."}
    ```

Rules (fail-closed, every one tested):
- the fence line must be exactly ```` ```scarlix-proposal ```` at column
  0 (trailing whitespace tolerated, indentation NOT — a loose fence
  would match code samples ABOUT proposals);
- the block ends at the next ```` ``` ```` line; no terminator →
  malformed;
- the content is ONE JSON object, `DisallowUnknownFields`, the CLOSED
  three-field shape `intent`/`agent_id`/`capability` — every field
  required, NO defaults: the parser never fills in a blank agent or
  guesses a capability;
- values are validated with the BAR'S OWN validators (`ValidateIntent`,
  `AgentID.Validate`, `Capability.IsValid`) — a proposal is held to the
  same standard as a bar submission, there is deliberately no second,
  weaker standard for model-authored intents;
- ZERO blocks → `ErrNoProposal` (not an error — the honest null);
- TWO OR MORE blocks → malformed, refused (an example plus a real
  proposal is AMBIGUITY, and ambiguity is never resolved by guessing
  which one was meant).

The parser is pure text processing — no network, no clock, no
randomness; it lives in `internal/agentfabric` (the proposal is a fabric
entity) and imports the CF-1 contract types only for attribution
validation. The LLM's freedom is exactly the fenced block; everything
else is deterministic discipline.

### The proposal record — the BF-4 genre, applied to model intents
`internal/agentfabric/proposal.go`:

- `ProposalID` is "prp-" + 16 crypto-random bytes hex (the cmd-/ws-/
  afx-/apv- precedent; RNG failure aborts the record).
- Closed states: `PENDING` (captured, awaiting the operator — the only
  in-flight state), `ACCEPTED` (the operator said yes; `command_id`
  references the captured command, ALWAYS), `DISMISSED` (the operator
  said no; terminal, no command ever). Deliberately NO withdrawn state
  and NO expiry: a proposal nobody answers stays PENDING in the
  reviewable history — an expiry would silently turn a non-decision
  into a decision (the BF-4 line, word for word).
- Attribution: `ProposerProvider` + `ProposerModel` — TYPED CF-1
  values (closed provider enumeration, validated model id shape), not
  free text: the record says WHICH tier and WHICH model proposed, using
  the runtime's own typed identity. The operator label (accept/dismiss)
  is a bounded principal label — attribution, never a credential (the
  bearer token remains the only authentication boundary).
- Invariants enforced at load AND before every persist: state↔fields
  coherence (PENDING carries no operator/reason/command/decision time;
  ACCEPTED requires operator + command_id + decided_at; DISMISSED
  requires operator + decided_at, NEVER a command), the bar's own
  intent/agent/capability validators, typed proposer validation,
  monotone timestamps.

### The store — proposals.json, the astore discipline exactly
`internal/agentfabric/pstore.go`:

- explicit schema version 1 (unknown versions rejected); default
  location `proposals.json` NEXT TO the commands file (same 0700
  directory, same tamper discipline); `SCARLIHQ_AGENTFABRIC_PROPOSALS_FILE`
  overrides;
- 0600 file / 0700 directory — checked at load AND before every persist;
  atomic temp+rename persistence with fsync; a failed persist rolls back
  the in-memory mutation;
- bounded: `DefaultMaxProposals = 256`; at capacity Capture fails with
  `ErrProposalLimit` → HTTP 503 (the operator archives deliberately);
- proposals are never deleted — the file is the reviewable history of
  every model proposal, acceptance and dismissal;
- ONE COMMAND REFERENCE PER PROPOSAL, and ONE PROPOSAL REFERENCE PER
  COMMAND — both enforced twice (mutation refusal + load abort on a
  hand-edited duplicate: a duplicate linkage is corruption, not "some
  proposals work");
- terminal decisions are never re-decided: Accept/Dismiss operate
  exclusively on PENDING records (`ErrProposalState` → 409).

### Accept — the ONLY door from a proposal to a command
`POST /api/ai/proposals/accept` (`{proposal_id, operator}` — no params,
no re-steering: the accepted thing is the proposed thing):

1. ProposalID shape first (400 vs 404), PENDING gate (409 on terminal),
   operator label required (400).
2. **Reconciliation (the crash window):** the command store is checked
   for an existing command carrying this proposal's id
   (`GetByProposal` — the linkage is a DURABLE field on the command,
   below). A crashed accept AFTER the command persist but BEFORE the
   proposal close → the proposal closes ACCEPTED referencing the
   EXISTING command, no new command — `"reconciled": true`. **No intent
   is captured twice through that door.**
3. The command is created through the EXACT BF-1 path:
   `SubmitWithProposal` (submitter = the operator's label — the human
   act that captured the intent; the model attribution rides the
   proposal and the composed view). The linkage `proposal_id` is
   ADDITIVE on `CommandRecord` (`omitempty` — the commands schema stays
   v1; BF-1/BF-2/BF-3 binaries and files are unaffected, the BF-4
   submitter precedent, word for word).
4. The routing question is the bar's own: an ACTIVE session of the
   agent resolves → the command routes + the kernel decides
   (synchronously, the `routeAndDecide` composition unchanged); no
   session → the command stays SUBMITTED ("awaiting routing" — the
   intent is never lost). CF-3 adds NO new routing semantics.
5. The proposal closes ACCEPTED referencing the command. Ordering is
   honest: command persist → [route+decide] → proposal close persist →
   response; a crash between the last two takes the reconciliation path.
6. **201** + `{proposal, command, reconciled}` (+ bus signals
   `command.submitted`, `proposal.decided`).

What accept deliberately does NOT skip: the kernel evaluation (a fresh
DECIDED gate at route time), the audit-first ordering, the BF-4 approval
gate for coding capabilities downstream, the closed params validation —
everything after intent capture is the EXISTING fabric, untouched.

### Dismiss — the operator's no, unconditional
`POST /api/ai/proposals/dismiss` (`{proposal_id, operator, reason?}`) —
the operator's no needs no kernel (no act runs, nothing to evaluate):
PENDING gate (404/409), operator label required, optional bounded
reason (the why, for the history), **201** + the DISMISSED view (never
a command reference — the validate invariant). A DISMISSED proposal
never becomes a command — the model can propose again in a NEW chat
response; the history records both.

### The read surface + composed views
- `GET /api/ai/proposals` — bounded newest-first list (`?limit=1..200`,
  default 200), closed filters (`?state=PENDING|ACCEPTED|DISMISSED` —
  unknown → 400), counts per state always present, store_path.
- The proposal view composes nothing (the terminal command is
  referenced by id; the command view composes the proposal).
- The command view composes the additive `proposal` field (summary —
  id/state/proposer/operator/captured_at; `omitempty`; present exactly
  when the command was born from a proposal) — nil-safe when the
  proposal store is not configured. BF-1..BF-4 clients are unaffected.

### Bus signals
`proposal.captured` / `proposal.decided` join the CHANGE namespace
(`command.*` / `execution.*` / `approval.*`): one-line notifications,
the client refetches through the auth-gated REST surface. No state on
the bus, no replay, drop-not-block — the existing contract, extended by
two names.

## 3. Honest non-goals (CF-3)

- **No dashboard card.** The operator's view into the routing layer —
  proposals included — is CF-4 ("audit + dashboard"), by design. CF-3's
  review surface is the REST API; the WS signals are already flowing for
  the CF-4 client.
- **No auto-decision, ever.** The propose endpoint captures; it never
  routes, decides, executes or requests an approval. The accept act is
  the operator's explicit yes, and everything after it is the EXISTING
  fabric flow. This is the whole point of the phase.
- **No proposal editing.** Accept carries no params — the accepted
  thing is the proposed thing, byte for byte (the BF-4 re-steering
  rule, applied at capture). A changed intent is a NEW proposal (a new
  chat) or a direct bar submission.
- **No workspace scoping on the propose path.** Proposals capture
  intent; scoping is an operator topology act at bar submission. An
  accepted proposal becomes an UNSCOPED command (the BF-1 default).
- **No streaming, no tool role, no multi-turn propose sessions.** The
  CF-1 contract's closed shape is the proposal's universe; extensions
  are reviewed acts, not implicit guesses.
- **No cross-file atomicity is claimed.** The reconciliation path
  (GetByProposal at accept) closes the double-command window; a crash
  between the command persist and the proposal close is the documented
  honest intermediate, reconciled on retry.

## 4. Verification on this branch

- `gofmt -l .` clean; `go vet ./...` clean; `go build ./...` OK
- `go test -count=1 ./...` — all packages green; `-race` on
  `agentfabric`, `airt` and `api` green
- Parser matrix (`proposal_test.go`): no block → ErrNoProposal; happy
  block (single/multi-line JSON); trailing whitespace on the fence;
  indented fence refused; two blocks refused; unterminated refused;
  bad JSON refused; unknown field refused; missing field refused;
  empty-after-trim fields refused; invalid intent (control runes,
  over-bound) refused; invalid agent id refused; unknown capability
  refused — every refusal names its reason
- Store matrix (`pstore_test.go`): capture/accept/dismiss lifecycle;
  state↔fields coherence violations refused at load (×corruption
  payloads); duplicate proposal ids refused; duplicate command
  references refused; wrong schema version refused; loose permissions
  refused; over-bound file refused; persist-failure rollback; restart
  persistence; ListLast/ListLastState/Counts; GetByCommand;
  one-per-command enforced on BOTH sides
- Command store (`store_test.go` additions): SubmitWithProposal sets
  the additive linkage; load round-trips it; duplicate linkage refused
  at load; malformed proposal_id on a record refused
- API matrix (`aipropose_test.go`): 401 without token; 503 runtime
  off / proposals off / fabric off (accept); propose happy (fake
  adapter stack, exactly the CF-2 chat test harness) → 200 + PENDING
  proposal with attribution; no-block → 200 + null; malformed → 422 +
  attribution + content; capture limit → 503; chat error mapping rides
  aiChat's own; list + filters + counts + 400s + 404s; accept happy →
  201 + command (SUBMITTED without a session, DECIDED with one — the
  full routeAndDecide composition); reconciliation (pre-linked
  command) → 201 + reconciled, no second command; accept on terminal →
  409; dismiss unconditional → 201, no command ever; composed proposal
  field on command views; method 405s
- Smoke check 30 (`check_30_ai_propose_surface`) — the source-level
  gate: the fence-tag constant, the closed states + no-withdrawn
  marker, the never-a-guess parser markers, the store discipline
  markers, all four routes behind h.auth, the reconciliation marker
  (GetByProposal), the additive proposal_id linkage (omitempty, schema
  stays v1), the main.go wiring (SCARLIHQ_AGENTFABRIC_PROPOSALS_FILE),
  the design-doc genre, the README v19.6.2 surface; three verified
  negative controls (breaking the route registration / the fence
  constant / the reconciliation call each FAILs the check). Inventory
  29 → 30.
- Real-binary E2E (81/81): ScarliHQ boots with fabric + ai runtime + the
  kernel enabled, a fake sglang serving deterministic outcomes by the
  user message: probe round (sglang HEALTHY, dead tiers honestly
  UNAVAILABLE — UNKNOWN never serves) → propose happy (200 + PENDING,
  attribution sglang/qwen3-14b, intent/agent/capability captured
  verbatim) → propose plain (200 + the honest null) → propose malformed
  (422 + the named reason + attribution + the refused content; NOTHING
  captured) → unknown model 404 + unknown field 400 → the review surface
  (newest-first, counts, closed state filter, 400s, 405) → accept
  WITHOUT a session (201 + SUBMITTED + the durable linkage + the
  operator submitter + reconciled=false) → re-accept/dismiss-after-accept
  409 + validation 400s/404 + re-steering refused → grant + ACTIVE
  session → accept WITH the session (201 + DECIDED + allow=true — the
  bar's own routing question) → dismiss (201 + reason; commands count
  proves no command EVER) → composed proposal field on command views →
  disk discipline (0600, schema v1, typed attribution persisted) →
  RESTART (the history survives; a PENDING captured pre-restart is still
  answerable — 201, SUBMITTED: kernel sessions are in-memory, honestly) →
  the no-runtime boot (propose 503 — the both-opt-ins gate; the review
  surface still lists). The crashed-accept reconciliation is
  unit-covered (simulating a mid-accept crash on the real binary would
  be a race, not a test — honestly noted).
