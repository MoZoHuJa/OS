# ScarLiX OS v19.6.3 — Universal AI Runtime Transport Hardening

## Scope
This patch remediates the findings of the two v19.6.1 KpK audits (the EN
deep technical audit and the SK release audit) against the CF-2 transport
as shipped in v19.6.1 (commit `2ce777c`). No new user-facing surface is
added; the existing `/api/ai/*` surface is made honest at its boundaries.

## Changes

### KPK-01 (P1) — redirect/proxy boundary closed at three layers
- New `scarlihq/internal/airt/transport.go`: the ONLY `http.Client`
  constructor in the package. `CheckRedirect` returns
  `http.ErrUseLastResponse` (a 307/308 is handed back as the response and
  classified as the failure it is — the POST body is the private
  conversation, never replayed onto a redirect `Location`);
  `Transport.Proxy` is explicitly `nil` (ambient `HTTP_PROXY` /
  `HTTPS_PROXY` never steer local AI traffic); the `DialContext` dial
  guard refuses any destination outside loopback and the configured
  endpoint hosts (defense in depth).
- `LocalAdapter` and `Prober` both build their clients through
  `newLocalClient` (per-tier and per-stack allowed-dial sets).
- Regression tests: `TestLocalAdapterChatRefusesRedirects` (307 + 308 —
  zero requests, zero prompt bytes at the external destination),
  `TestLocalAdapterChatRefusesCrossOriginRedirect`,
  `TestProbeDoesNotFollowRedirects`, `TestLocalTransportIgnoresAmbientProxy`,
  `TestLocalTransportDialGuard`, `TestIsLocalDialTarget`,
  `TestEndpointHostSets`, `TestNewLocalClientShape`,
  `TestLocalStackShareNoClientState`, `TestAIChatRedirectNeverFollowed`
  (end-to-end through the mux).
- Verified alongside: the client closes its side of the connection at
  deadline-abort — no client-side fd leak in the ScarliHQ process (the
  apparent leak in the reproduction pattern was the test server sharing
  the process; the production tier owns its side).

### KPK-02 (P2) — one end-to-end timeout contract
- `airt.ChatBudget(perAttempt)`: the budget for one AI request is
  per-attempt × the closed four-tier set (the complete honest failover
  walk). Sequential attempts share it through the request context.
- `airt.MaxAdapterTimeout = 300s`: the named upper bound of the
  operator-tunable per-attempt timeout (`SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT`,
  validated fail-closed at startup; default 30s = the stack's own
  `timeout_ms: 30000` precedent).
- `airt.ErrBudgetExpired` (wrapping the context's own error): the
  executor reports the budget expiry BY NAME; the API maps it to a
  deliberate **504** with the attempts that consumed the budget riding
  along — on `/api/ai/chat` AND `/api/ai/propose`.
- The AI write path extends the per-request write deadline to budget +
  10s margin (`http.ResponseController.SetWriteDeadline`): a legitimate
  slow local generation is written, never cut by the server's 30s default
  `WriteTimeout` (which keeps serving every other surface unchanged).
- `Runtime` carries its budget (`ChatBudget()`); `NewRuntime` requires a
  positive one (fail-closed); `NewLocalStack` takes the per-attempt
  timeout explicitly.
- Regression tests: `TestChatBudgetArithmetic`,
  `TestRuntimeChatBudgetFailoverWithinBudget`,
  `TestRuntimeChatBudgetSlowButLegit`, `TestRuntimeChatBudgetExpires`,
  `TestRuntimeChatBudgetAlreadyExpired`, `TestRuntimeChatBudgetAccessor`,
  `TestAIChatBudgetExpiredReturns504`,
  `TestAIChatSlowButLegitAnswerSucceeds`, `TestLocalAdapterTimeoutUpperBound`.

### Hygiene findings from the SK audit
- **P2-01** — README install one-liner now says "clones the current
  v19.6.3 mainline"; smoke check_28 carries a literal drift guard that
  moves with every release.
- **P2-02** — `SCARLIX_SECURITY_MAP.md` (p) states the auth inequality:
  AI Runtime REST auth ≠ inference port auth; the inference ports remain
  localhost/scarlix-net only; never add cloud keys to the adapter.
- **P2-03** — the registry durability truth is stated in the SECURITY_MAP
  and the compose env block: NOT durable until the CF-4/persistence
  decision; always declare `SCARLIHQ_AIRUNTIME_MODELS` when enabling.
- **P3-01** — the host-bridge header carries no hardcoded release (the
  VERSION file only; the stale v19.2.9 remnant removed).
- **P3-02** — the timeout contract is documented (compose env block,
  SECURITY_MAP, `docs/AI_RUNTIME_CF2_DESIGN.md` section 6, this file)
  and tunable (`SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT`, bounded).

### Gates
- Smoke check 31 (`check_31_ai_transport_boundary`) — the source-level
  gate over the whole hardening (redirect policy, explicit no-proxy,
  dial guard marker, exactly ONE `http.Client` construction in the
  package, both consumers on the shared constructor, budget markers, 504
  mapping + write-deadline extension in BOTH handlers, main.go wiring,
  compose documentation, SECURITY_MAP truths, regression proofs as
  tests, README surface, release-notes genre). Inventory 30 → 31.
- check_28 evolved (current-release → v19.6.3, the mainline drift guard,
  the v19.6.2 historical surface); check_29 now scans `transport.go`
  (no-listener + cloud-token scans).

## Verification status
Verified by execution in the release environment (this repository, the
exact commands and their exit codes):

```
go build ./internal/... ./cmd/scarlihq   → exit 0
go vet ./internal/... ./cmd/scarlihq     → exit 0
gofmt -l scarlihq/internal/airt scarlihq/internal/api scarlihq/cmd/scarlihq → empty
go test ./scarlihq/internal/airt/ ./scarlihq/internal/api/ ./scarlihq/internal/airuntime/ -count=1 → ok (all)
go test -race ./scarlihq/internal/airt/ ./scarlihq/internal/api/ -count=1 → ok (race clean)
go test ./... (full suite)                → see the release verification log
bash -n files/usr/local/bin/scarlix-smoke-test.sh → exit 0
bash tests/test_registry_classification.sh → 68 assertions, 0 failed
scarlix-smoke-test.sh --offline          → 31 checks, 0 FAIL
(check_31 negative controls verified: redirect policy / 504 mapping /
 SECURITY_MAP line — each break FAILs the check)
```

The audit's own unverified items remain honestly open where they were:
target-host hardware acceptance (KPK-06) is still OPEN, and the full
online smoke suite's registry checks depend on outbound network reach in
the CI environment (the offline suite is the deterministic gate here).

## Known limitation
- DEGRADED tiers still serve (the honesty-matrix decision, unchanged by
  design): a 503-while-loading tier can receive a chat attempt and fail
  into failover — the budget (KPK-02) now bounds that walk end-to-end,
  and the 502/504 bodies carry the attempts for exactly that monitoring
  (the DEGRADED→fail vs HEALTHY→fail counters remain a CF-4 audit item).
- The dial guard allows the CONFIGURED hosts; it does not (and cannot,
  portably) verify that a single-label service name resolves into a
  specific container network range — that remains a deployment invariant
  (scarlix-net isolation, `127.0.0.1:` port binds), now stated as such in
  the SECURITY_MAP.
