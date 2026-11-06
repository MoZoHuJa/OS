# ScarLiX OS — Universal AI Runtime CF-2 Design (v19.6.x line — OPEN)

**Branch:** `ai-runtime/cf2-adapters` → merged to main as **v19.6.1**
**Status:** CF-2 complete — the transport lands (real adapters, health
probing, the auth-gated REST surface — opt-in). The pure CF-1 contract
(`internal/airuntime`) is UNTOUCHED: the network lives behind it, in a
separate package (`internal/airt`), never inside it.
**Strategy source:** `docs/AI_RUNTIME_CF1_DESIGN.md` §2 (the CF-2 row:
"Real adapters (SGLang/vLLM/BeeLlama/Ollama behind the contract) + health
probing + the auth-gated REST surface (opt-in)") · the pre-v19.2 roadmap
slot "normalized adapters, model router, failover"

---

## 1. Where this fits

CF-1 (v19.6.0) landed the typed foundation network/IO-free by
construction — deliberately unwired (the AK-1 precedent: the contract is
established and tested first). CF-2 is the reviewed act that wires it:
the four real local-stack tiers become callable behind the contract,
observations stop being hypothetical (a real prober produces them), and
the runtime becomes reachable through the standard Bearer-gated REST
surface. The charter is unchanged: *"LLM navrhuje, politika rozhoduje"* —
the LLM proposes, policy decides. Chat is TRANSPORT ONLY: it mutates
nothing on the host. An LLM response becoming a fabric PROPOSAL is CF-3,
behind the fabric's approvals.

The separation is the design: `internal/airuntime` stays pure (smoke
check 28 now scans the WHOLE package, comment-stripped, for net/http and
IO — not just the five CF-1 files); `internal/airt` is the transport
layer that imports it. The contract decides, the transport serves.

## 2. Sub-phase plan (each with green CI + smoke before merge)

| Phase | Content | Status |
|---|---|---|
| **CF-1** | Typed foundations: closed provider enumeration (the REAL local stack, v19.0.6-locked failover order), bounded explicit model registry, observed fail-closed health, normalized chat contract, pure deterministic router | **DONE — merged as v19.6.0** |
| **CF-2** | Real adapters (SGLang/vLLM/BeeLlama/Ollama behind the contract) + health probing + the auth-gated REST surface (opt-in) | **DONE — merged as v19.6.1** |
| **CF-3** | Propose surface: an LLM response becomes a fabric PROPOSAL — never an auto-submitted command | planned |
| **CF-4** | Runtime audit + dashboard (the operator's view into the routing layer) | planned |
| **CF-5** | Phase closure (the acceptance gate: cross-release coherence, honest ledger) | planned |

## 3. CF-2 contract (as implemented in `internal/airt` + `internal/api/airuntime.go`)

### Endpoints — the documented local stack, closed shape
`DefaultEndpoints` are the `SCARLIX_API_MAP.md` addresses (sglang
`127.0.0.1:30000` · vllm `:8089` · beellama `:11438` · ollama `:11435`),
release-locked like `DefaultFailoverOrder`. The closed endpoint shape:
http only (the local stack is plain HTTP, no TLS anywhere in it), no
userinfo, no path/query/fragment, an explicit port, and a host that is
either **loopback** or a **single-label docker service name** — a dotted
host is the cloud-gateway shape, rejected BY NAME (the guard fires before
the cosmetic rules, so a cloud URL is named as a cloud URL). Inside the
ScarliHQ container the stack is reachable by service name on
scarlix-net; that is what the env overrides exist for
(`SCARLIHQ_AIRUNTIME_SGLANG_URL / _VLLM_URL / _BEELLAMA_URL / _OLLAMA_URL`). An invalid
override is a startup ERROR — never a silent fall-back to the default.

### Adapters — one transport per tier, over the wire the stack speaks
`LocalAdapter` POSTs `{endpoint}/v1/chat/completions` — the
OpenAI-compatible shape ALL FOUR tiers serve (sglang, vllm,
beellama/llama.cpp server, ollama). No auth header: the documented local
stack authenticates nobody; the endpoint shape itself is the boundary.
Bounded: 30s per attempt by default (the ai/smg `timeout_ms: 30000`
precedent; v19.6.3: operator-tunable through
`SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT` within the 300s named upper bound —
see section 6), the response body read through a 1 MiB limit (the
content itself is bounded to 4096 runes by the contract; the envelope
leaves room for the servers' own decorations). Normalization is fail-closed, by rule: exactly one
choice; the response role must be `assistant`; finish reasons only
`stop`/`length` — anything else ("content_filter", "tool_calls", "" …)
is an adapter ERROR the plan skips past, never a coerced value; absent
usage is zero accounting, negative usage is a lie and fails closed; the
content must survive `Message` validation at the wire boundary. Every
response carries provider attribution + a UTC timestamp stamped at
decode — attribution survives failover. Streaming is deliberately absent
(`stream:false` always — a streaming surface would be a separate
reviewed act).

### The prober — honesty matrix, stated once
`Prober` asks each tier on its documented health surface (/health for
sglang/vllm/beellama — the compose healthchecks; /api/tags for ollama —
the HTTP shape of `ollama list`). **A 2xx answer is HEALTHY, a
standing-but-unready server is DEGRADED, a dead one is UNAVAILABLE** —
and a never-probed provider stays UNKNOWN (fail-closed; nothing here
fabricates health). DEGRADED serves (the CF-1 contract): a 503-while-
loading tier is tried, and if the chat then fails, the failover
execution handles it — honestly. `ProbeAll` probes all four CONCURRENTLY
(bounded by the closed set) and returns the observations in tier order.
`ProbeAllAndObserve` records into the CF-1 table (the monotone-time
discipline applies). `Start(interval, table)` is the background
freshness loop — CF-1 stated "freshness is the CF-2 prober's job"; this
is that job, running unattended: opt-in via
`SCARLIHQ_AIRUNTIME_PROBE_INTERVAL` (without it, POST /api/ai/probe is
the operator's refresh — the table still never expires entries on its
own).

### The executor — the plan, actually walked
`Runtime` composes the CF-1 registry + health table + router with the
CF-2 adapters + prober. `Chat(ctx, req)`: validate the request shape;
PLAN with the pure CF-1 router (unchanged — it still decides, and it is
replanned from LIVE state every request: a probe recorded between two
requests changes the second one's plan); walk the plan steps in tier
order; the first adapter that answers wins, its attribution rides the
response; every failed step is recorded as an `Attempt` (provider, tier,
plan role, the adapter's own error). Errors, all fail-closed: validation
(400-shape) · `ErrUnknownModel` — the router never guesses a model (404)
· `ErrNoRoute` with the named-rule skips riding along (503) ·
`ErrAllAdaptersFailed` with the attempts riding along (502) — fail-closed,
not fail-blind, on every path. Construction is fail-closed: nil
registry/health/prober rejected by name; the adapter set must cover
EXACTLY the closed four-provider stack (all four tiers always — health
gates routing, construction must not: a missing tier would be a silent
fail-open hole in failover). Requests are handed to adapters as deep
copies (copy isolation, tested in both directions).

### The REST surface — auth-gated, opt-in, 503 until configured
Four routes, all behind the standard Bearer auth middleware, all 503
until `ConfigureAIRuntime` (main.go: `SCARLIHQ_AIRUNTIME_ENABLED=true`):
- `GET /api/ai/health` — the tier-ordered health view (UNKNOWN included,
  last-observed times, serves flags). Read-only.
- `GET /api/ai/models` — the registry listing (tier-ordered providers,
  ascending models). Read-only.
- `POST /api/ai/models` — the operator registration act
  (`{"provider":"sglang","model":"qwen3-14b"}` → 201; duplicate → 409;
  foreign provider / malformed id → 400 — the closed enumerations hold
  through the API; no auto-discovery exists to call).
- `POST /api/ai/probe` — one probe round NOW, recorded (200 with the
  tier-ordered observations; a dead tier is observed UNAVAILABLE, never
  omitted).
- `POST /api/ai/chat` — the normalized failover chat. The request is the
  chat contract verbatim (model + messages; unknown fields rejected;
  body bound to 1 MiB — the 4 KiB `decodeOneJSON` cap fits admin
  surfaces, not chat: the contract itself allows 32 × 4096 runes). The
  response carries attribution, the normalized message, finish reason,
  usage, timestamp — and `attempts[]`, the failed tiers in plan order.
  The no-route 503 carries `skips[]` (named rules); the all-failed 502
  carries `attempts[]`. The statuses are set BEFORE the bodies
  (`writeJSONStatus` — a body-first write would swallow the status).

### main.go wiring — the opt-in, fail-closed
`SCARLIHQ_AIRUNTIME_ENABLED=true` → endpoints (defaults + validated env
overrides) → `NewLocalStack` (four adapters + prober) → registry (bound
64, empty) → `NewRuntime` → operator bootstrap
(`SCARLIHQ_AIRUNTIME_MODELS="provider:model,..."` — split on the FIRST
colon, model ids may carry their own; a malformed declaration aborts
startup: the runtime never boots on a guessed registry) → optional probe
loop → `ConfigureAIRuntime`. No persistence in CF-2: the registry is
in-memory, rebuilt by the env bootstrap + operator acts at boot (an
honest non-goal, carried below).

## 4. Non-goals (honest scope — carried, not hidden)

- **No registry persistence** — the routing surface is rebuilt at boot
  from `SCARLIHQ_AIRUNTIME_MODELS` + operator POSTs. A persisted registry
  is a deliberate future act (it would ride the CF-4 audit/dashboard
  review), not an implicit file that drifts.
- **No LLM in the decision path** — the router stays pure CF-1 code;
  Chat produces normalized text. An LLM response becoming a fabric
  PROPOSAL is CF-3, behind the fabric's approvals — never an
  auto-submitted command.
- **No streaming** — request/response only (`stream:false` on every wire
  call). A streaming surface would be a separate reviewed act.
- **No cross-tier model translation** — failover still walks providers
  that serve the SAME registered model; the LiteLLM-style mapping
  remains a deliberate future act.
- **No chat auth of its own** — the surface rides the standard
  SCARLIHQ_TOKEN Bearer gate (operator-grade), like every other route;
  per-conversation identities are not a CF-2 concept.
- **No tool/function calling** — the closed role enumeration
  (system/user/assistant) is unchanged; a tool role appears when a real
  consumer needs it, as a reviewed act.
- **No new ports** — /api/ai/* rides the existing ScarliHQ listener
  (8090); the adapters DIAL OUT to the documented local stack, nothing
  listens in `internal/airt`.

## 5. Verification on this branch

- `gofmt -l .` clean; `go vet ./...` clean; `go build ./...` OK;
  `go test -count=1 ./...` — **all 23 packages green** (incl. the new
  `internal/airt`); `go test -race -count=1 ./internal/airt
  ./internal/api` green (concurrent register/probe/plan/chat).
- Test matrices in `internal/airt`: endpoints (the API-map defaults;
  accept loopback/service-names incl. `ollama-agent`; reject
  https/userinfo/path/query/fragment/implicit-port/port-0/out-of-range/
  uppercase/leading-trailing-hyphen/dotted-hosts BY NAME — the
  cloud-gateway guard fires before the cosmetic rules; env overrides —
  applied/trimmed/invalid-fails-loudly-naming-the-variable; the
  endpoint-set closedness); wire (happy path w/ attribution + UTC stamp,
  finish stop/length + unknown-finish-errors-never-coerced incl.
  "error"/""/"STOP", zero/two choices, non-assistant role, empty/
  over-bound/control-rune content, absent-usage-zeros vs negative-usage
  errors, malformed JSON, encode 1:1 + stream:false); adapter (happy
  path asserting the WIRE request shape — model/messages/stream:false;
  invalid request never reaches the wire; non-2xx carries the status;
  malformed body; over-bound body names the transport bound; timeout;
  context cancellation; construction rejections; NewLocalStack
  completeness + named missing tier); prober (2xx→HEALTHY, 503→DEGRADED
  which serves, refused→UNAVAILABLE which does not, timeout→UNAVAILABLE,
  ollama asked on /api/tags not /health, invalid provider → no
  observation fabricated, ProbeAll tier-order + completeness — a dead
  tier is observed, never omitted; ProbeAllAndObserve flips Serves;
  Start observes within a deadline; construction rejections); runtime
  (construction nil/missing/mislabeled/foreign rejections; primary
  answers + only-primary-was-asked; one/multi-tier failover w/ attempts
  naming provider+tier+role+error; all-failed → ErrAllAdaptersFailed w/
  attempts; unknown model → the router's error, nothing executed;
  no-route → skips w/ named rules, nothing executed; invalid request
  pre-plan; replan-from-live-state — an UNAVAILABLE observation between
  two requests redirects the second; copy isolation BOTH directions —
  an adapter mutating its request cannot corrupt the caller's; 20×
  concurrent failover; register passthrough duplicates/foreign/tier-
  ordered-ascending listings; health view tier-order/UNKNOWN/empty-time).
- API tests (`internal/api/airuntime_test.go`): 503-until-configured on
  all five routes; 401 without token; the health view (tier order,
  states, serves, tiers); models list/register/duplicate-409/foreign-400/
  malformed-400/unknown-field-400; the probe round (states by the honesty
  matrix incl. DEGRADED-serves, observations recorded — the view flips);
  chat happy path (attribution, message, finish, usage, timestamp, empty
  attempts); chat failover (provider=ollama, attempts[0]=sglang
  tier-1 primary w/ the error); all-tiers-failed → 502 w/ attempts;
  unknown model → 404; no-route → 503 w/ four named-rule skips; request
  validation (no messages, bad role, empty content, bad model id,
  unknown field, two objects, 33-messages bound); oversized body;
  method discipline (405s). All over REAL `airt.Runtime` instances wired
  to live httptest servers — the surface is tested exactly as main.go
  wires it.
- Smoke check 29 (`check_29_ai_runtime_adapters`) — the source-level
  contract gate: the airt package carries the charter + the failover/
  cloud-gateway markers, the API-map endpoint constants, the probe
  honesty matrix, the REST routes, the opt-in wiring in main.go + the
  compose env block, no listener in the transport, the CF-2 design doc
  genre + the CF plan stamped, the README v19.6.1 surface. **Negative
  control verified** (two injections — see the smoke suite output).
  Check 28 evolved with CF-2: the network/IO scan now covers the WHOLE
  `internal/airuntime` package (every .go file, comment-stripped), and
  the UNWIRED assertion became its successor truth — the wiring goes
  through `internal/airt`, the contract package stays pure. Inventory
  28 → 29.
- Real-binary sanity: the ScarliHQ binary builds and boots unchanged
  (CF-2 adds opt-in routes; unconfigured, they answer 503 behind the
  auth gate — the same contract as every opt-in surface before it).
  Configured sanity: `SCARLIHQ_AIRUNTIME_ENABLED=true` with a
  bootstrapped model + a live probe round, exercised on the real binary
  in the release environment.

---

## 6. v19.6.3 remediation — KPK-01/KPK-02 (the v19.6.1 KpK audit)

The audit found two defects in the CF-2 transport as shipped in v19.6.1.
Both are fixed in v19.6.3; this section is the design record.

### KPK-01 — the local-only client is closed at THREE layers

Go's default `http.Client` FOLLOWS redirects. The audit reproduced it: a
local tier answering `307` with an external `Location` received — at that
external destination — the original POST body, i.e. the whole normalized
conversation, i.e. the private prompts. The endpoint allowlist constrains
the CONFIGURED destination; it does not constrain a redirect destination.
That was a confidentiality boundary failure, not a stylistic concern.

The fix is `internal/airt/transport.go` — the ONLY place this package
constructs an `http.Client` (smoke check 31 counts the constructions):

1. **No redirects.** `CheckRedirect` returns `http.ErrUseLastResponse`:
   a 307/308 is handed back AS the response and classified by the
   existing rules — the adapter's 2xx rule (an attempt error), the
   prober's honesty matrix (DEGRADED: standing but unready). No status
   is special; none is followed.
2. **No ambient proxy.** `Transport.Proxy` is explicitly `nil`:
   `HTTP_PROXY`/`HTTPS_PROXY` never steer local AI traffic. Proxy support
   would be a separate, explicit, reviewed act — it cannot become a
   feature by accident.
3. **The dial guard.** The transport's `DialContext` refuses any
   destination outside loopback and the configured endpoint hosts
   (hostname syntax at the endpoint layer does not prove where a
   connection actually goes; the dial layer does). Defense in depth —
   each layer would hold alone.

Regression proofs (tests, not claims): `TestLocalAdapterChatRefusesRedirects`
(307 AND 308 — the external destination receives ZERO requests and ZERO
prompt bytes), `TestProbeDoesNotFollowRedirects`,
`TestLocalTransportIgnoresAmbientProxy` (a counting proxy in the env —
zero traversal), `TestLocalTransportDialGuard` /
`TestIsLocalDialTarget`, and `TestAIChatRedirectNeverFollowed`
(end-to-end through the mux). Empirically verified alongside: the client
closes ITS side of the connection at deadline-abort — no client-side fd
leak in the ScarliHQ process (the audit's reproduction pattern, where
the leak appeared, was the test server sharing the process).

### KPK-02 — one end-to-end timeout contract

As shipped in v19.6.1, every adapter attempt could run 30s while the
HTTP server's `WriteTimeout` was also 30s: a slow-but-valid answer could
outlive the write deadline; a multi-tier walk could exceed it entirely.
Two unrelated numbers pretending to be one budget.

The v19.6.3 contract, stated once:

- **The budget** for one AI request is `airt.ChatBudget(perAttempt)` —
  per-attempt × the closed four-tier set (the complete honest failover
  walk). Sequential attempts SHARE it through the request context; no
  tier carries an independent allowance next to it.
- **The per-attempt bound** is `DefaultAdapterTimeout` (30s, the stack's
  own `timeout_ms` precedent), operator-tunable through
  `SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT` within `MaxAdapterTimeout` (300s — a
  named upper bound; the env is validated fail-closed at startup).
- **The API layer imposes the budget** as the request context deadline
  (`aiChatContext` — `/api/ai/chat` AND `/api/ai/propose`, the same
  contract on both). Every adapter and probe operation propagates it.
- **Expiry is a deliberate answer:** `airt.ErrBudgetExpired` (wrapping
  the context's own error) → **504**, the attempts that consumed the
  budget riding along — never a socket write deadline artifact, never a
  generic 502 masquerade.
- **The write path extends beyond the budget:** both handlers extend the
  per-request write deadline to budget + 10s margin
  (`http.ResponseController.SetWriteDeadline`). A legitimate slow local
  generation is WRITTEN. The server's 30s `WriteTimeout` stays the
  default for every other surface (the slow-client DoS guard unchanged).

Regression proofs: `TestChatBudgetArithmetic`,
`TestRuntimeChatBudgetFailoverWithinBudget` (a delayed primary plus a
successful fallback completes within the budget),
`TestRuntimeChatBudgetSlowButLegit`, `TestRuntimeChatBudgetExpires`
(expiry by name, attempts riding along, DeadlineExceeded preserved
through `errors.Is`), `TestRuntimeChatBudgetAlreadyExpired`,
`TestAIChatBudgetExpiredReturns504` and
`TestAIChatSlowButLegitAnswerSucceeds` (both through the real mux).

### Hygiene findings carried from the SK audit

- **KPK-19.6.1-P2-01** — README install one-liner drift: the paragraph
  now names the current release, and smoke check_28 carries a literal
  drift guard that moves with every release.
- **KPK-19.6.1-P2-02** — the SECURITY_MAP states the auth inequality:
  AI Runtime REST auth ≠ inference port auth; the ports remain
  localhost/scarlix-net only.
- **KPK-19.6.1-P2-03** — the registry durability truth is stated where
  the operator reads it (SECURITY_MAP + compose): NOT durable until the
  CF-4/persistence decision; always declare
  `SCARLIHQ_AIRUNTIME_MODELS` when enabling.
- **KPK-19.6.1-P3-01** — the host-bridge header carries no hardcoded
  release (the VERSION file only).
- **KPK-19.6.1-P3-02** — the timeout contract is documented (this
  section) and tunable (`SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT`, bounded).

Smoke check 31 (`check_31_ai_transport_boundary`) gates all of the
above at the source level; the acceptance of both findings is the test
suite, the smoke suite, and the release notes
(`docs/RELEASE_NOTES_v19.6.3.md`).
