# ScarLiX OS v19.6.4 — Universal AI Runtime Transport Hardening II

## Scope
This patch remediates the findings of the two v19.6.3 KpK audits (the EN
deep technical audit `ScarLiXOS_v19.6.3_KpK_Findings_and_Fixes_EN.md` and
the SK release audit `SCARLIX_OS_v19.6.3_KPK_AUDIT.md`) against the
hardened transport as shipped in v19.6.3 (commit `e8181c5`). No new
user-facing surface is added; the local-only boundary is made
address-level honest and the health table is protected from
cancellation poisoning. The remediation order follows the audit's own
recommendation: KPK-04 first (the smallest, most direct availability
fix), then KPK-03 (the hostname allowlist upgraded into a real
destination-address policy).

## Changes

### KPK-04 (P2) — a cancelled probe round is not evidence about the providers
- `airt/prober.go`: `ProbeAll` aborts the round when the caller's context
  is done at round end — the context error is returned (identifiable via
  `errors.Is` for `context.Canceled` / `context.DeadlineExceeded`), NO
  observations are fabricated. `ProbeAllAndObserve` records NOTHING when
  the round aborts: the health table's last-known-good states AND
  timestamps stand.
- A single `Probe` against a done context returns an ERROR, never an
  UNAVAILABLE observation — a cancelled observation is not evidence
  about the provider.
- Genuine per-provider failures (refused, timed out, unreachable) are
  recorded exactly when the parent context is live — the honest matrix
  is unchanged.
- The background loop's round budget is now `probeRoundBudget = 2 ×
  DefaultProbeTimeout`, deliberately beyond the per-probe client timeout
  so the two clocks are never ambiguous: a slow tier is classified by
  the probe's OWN timeout (UNAVAILABLE, genuine), and only a
  pathologically stuck round aborts with nothing recorded.
- Regression tests: `TestProbeCancelledContextRefusesToFabricate`,
  `TestProbeAllCancelledRoundPreservesLastKnownGood` (the audit's
  acceptance path: seed HEALTHY → delayed endpoints + mid-round cancel →
  `context.Canceled`, table unchanged including timestamps),
  `TestProbeAllDeadlineExpiredRoundAborts`,
  `TestProbeAllGenuineFailureOnlyThatProvider` (the counterweight: one
  dead tier with a live parent context → ONLY that tier UNAVAILABLE),
  `TestProberLoopSlowTierStaysGenuine`.

### KPK-03 (P2) — the dial guard resolves, validates and PINS
- `airt/transport.go`: the v19.6.3 guard validated the hostname STRING
  and then let `net.Dialer` resolve it to ANYWHERE. Now:
  - An IP-literal destination is validated directly (loopback literals
    always trusted — they ARE the destination, no resolution, no
    pinning needed; a foreign literal is refused at the dial layer).
  - A NAME (localhost or a configured service name) is RESOLVED through
    the process's own resolver (`net.DefaultResolver` — Docker's
    embedded DNS inside the container, the host resolver on bare metal),
    EVERY answered address is validated against the trusted local
    networks, and the connection is PINNED to a validated address —
    the IP that is dialed is the IP that was validated, with no
    validate-then-resolve TOCTOU gap.
  - A MIXED answer set (one approved + one foreign address) is refused
    WHOLE — the documented fail-closed policy, never a filtered guess.
    Empty and failed resolutions are refused fail-closed.
  - Multiple approved addresses dial in resolver order until one
    connects — every dialed address is a member of the validated set.
  - The Host header of an HTTP request is unaffected by the pinned dial
    address (it comes from the request URL); if TLS were ever introduced
    on the local stack, the TLS server name must be pinned the same way
    (noted in code — the endpoint shape rejects https by design).
- Trusted networks: loopback (`127.0.0.0/8`, `::1`) is ALWAYS trusted;
  the non-loopback trust defaults to Docker's own address pools
  (`172.17.0.0/12` + `192.168.0.0/16`) and is REPLACED by the operator
  declaration `SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS` (comma-separated CIDRs,
  parsed fail-closed at startup — deliberately NOT the whole RFC1918
  space: `10.0.0.0/8` is absent until declared).
- Construction fails closed when a service-name endpoint has no
  non-loopback trusted network to resolve into
  (`validateNameEndpointDialTrust` — a loud boot-time error naming the
  env surface, never a per-request surprise). `localhost` endpoints
  construct under loopback-only trust (they resolve into the
  always-trusted loopback; a poisoned hosts file is caught at dial time).
- The exported constructors (`NewLocalAdapter`, `NewProber`,
  `NewLocalStack`) accept a variadic `DialOption`;
  `WithTrustedDialCIDRs(TrustedDialCIDRsFromEnv(...))` is the main.go
  wiring. All pre-v19.6.4 call sites compile unchanged (no option → the
  Docker default pools).
- Regression tests: `TestDialGuardResolvesValidatesAndPins` (a recording
  dialer receives the PINNED `ip:port`, never the name),
  `TestDialGuardPinsAcrossApprovedAnswers`,
  `TestDialGuardRefusesPublicResolution` (the audit finding closed: an
  approved service name resolving to a public IP → ZERO dials, zero
  packets, zero prompt bytes),
  `TestDialGuardFailClosedOnMixedResolution`,
  `TestDialGuardRefusesEmptyAndFailedResolution`,
  `TestDialGuardTOCTOUResolutionChange` (a resolver that changes its
  answer between dials cannot bypass the pin),
  `TestDialGuardLoopbackLiteralSkipsResolution`,
  `TestDialGuardForeignLiteralRefusedByTrustedSet`, `TestIPIsTrusted`,
  `TestApprovedResolvedIPs`, `TestDefaultTrustedDialCIDRs`,
  `TestTrustedDialCIDRsFromEnv`, `TestResolveDialCIDRsOptionPrecedence`,
  `TestNameEndpointDialTrustConstruction`,
  `TestLocalAdapterLocalhostNameThroughRealResolver` (the production
  path end-to-end through the real resolver: name → resolve → validate →
  pin → dial → real HTTP chat).

### The SK-audit documentation residuals (P2-01 / P2-03)
- `SCARLIX_SECURITY_MAP.md` (p) now states the address-level boundary:
  the dial guard is a hostname allowlist PLUS resolve-validate-pin over
  the trusted local networks; the honest residual is that the resolver
  itself (Docker DNS / host resolv.conf) is trusted only to ANSWER — a
  compromised resolver can no longer steer traffic anywhere it wants,
  only into the declared networks or into a refusal.
- The named maximum wall clock (the audit's "budget = 4 × per-attempt
  always" concern): one `/api/ai/chat` or `/api/ai/propose` request may
  occupy its handler for at most `4 × min(CHAT_TIMEOUT, 300s)` = 20
  minutes at the widest setting, plus the 10s write margin — bounded,
  documented in the SECURITY_MAP, the compose env block and here; a
  dedicated concurrency limit remains a CF-4 audit-dashboard item, not a
  silent default.

### Gates
- Smoke check 32 (`check_32_ai_dial_pin_probe_cancel`) — the
  source-level gate over the whole remediation (resolve-validate-pin
  code markers, the KPK-04 round-abort guard, the trusted-CIDR wiring in
  main.go and compose, the construction-time fail-closed, the regression
  proofs as tests, the SECURITY_MAP truths, the README surface, the
  release-notes genre). Inventory 31 → 32.
- check_28 evolved (current-release → v19.6.4, the mainline drift guard;
  the v19.6.3 surface stays asserted as history).

## Verification status
Verified by execution in the release environment (this repository, the
exact commands and their exit codes):

```
go build ./...                               → exit 0
go vet ./internal/... ./cmd/scarlihq         → exit 0
gofmt -l scarlihq/internal/airt scarlihq/cmd/scarlihq → empty
go test ./... (full suite)                   → ok (all packages)
go test -race ./internal/airt/ ./internal/api/ -count=1 → ok (race clean)
bash -n files/usr/local/bin/scarlix-smoke-test.sh → exit 0
scarlix-smoke-test.sh --offline              → 32 checks, 0 FAIL
(check_32 negative controls verified: the pin marker / the cancel guard /
 the SECURITY_MAP line — each break FAILs the check)
```

The audit's own unverified items remain honestly open where they were:
target-host hardware acceptance (KPK-06) is still OPEN, and the full
online smoke suite's registry checks depend on outbound network reach in
the CI environment (the offline suite is the deterministic gate here).

## Known limitation
- DEGRADED tiers still serve (the honesty-matrix decision, unchanged by
  design); the DEGRADED→fail vs HEALTHY→fail counters remain a CF-4
  audit item.
- The resolver is trusted to ANSWER, not to route: resolution inside the
  declared networks is accepted (fail-closed on anything else). An
  operator whose container network lives outside the Docker default
  pools MUST declare `SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS` — construction
  fails closed otherwise (by design: the runtime never boots
  half-trusting its own dial policy).
- The 20-minute maximum wall clock at the widest `CHAT_TIMEOUT` setting
  remains bounded but operationally long; a dedicated concurrency limit
  on `/api/ai/chat` is deliberately NOT added in this remediation
  (scope discipline — it is a CF-4 audit-dashboard item, documented).
