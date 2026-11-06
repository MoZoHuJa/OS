# ScarLiX OS v19.6.5 — Universal AI Runtime Trust Scope + Commit Atomicity

## Scope
This patch remediates the two residual findings of the v19.6.4 KpK audits
(the EN independent audit `ScarLiXOS_v19.6.4_KpK_Findings_and_Fixes_EN.md`
— CONDITIONAL PASS with two P2 follow-ups — and the SK release audit
`SCARLIX_OS_v19.6.4_KPK_AUDIT.md` — APPROVED FOR TAG with named
residuals) against v19.6.4 (commit `995d3d2`). No new user-facing surface
is added; the default destination trust is narrowed to what it actually
is, the declaration surface is hardened against universal routes, and
the probe-round recording is made atomic and context-aware at a single
commit boundary. The remediation order follows the independent audit's
own release gate: KPK-19.6.4-P2-01 (the default trust scope), then
KPK-19.6.4-P2-02 (the commit-boundary race).

## Changes

### KPK-19.6.4-P2-01 (P2) — the default trusted CIDRs no longer include the home-LAN range
- `airt/transport.go`: `DefaultTrustedDialCIDRs()` returns the Docker
  DEFAULT bridge address pool `172.17.0.0/12` ONLY. The home-LAN range
  `192.168.0.0/16` was REMOVED from the implicit default — it is not a
  per-deployment Docker boundary: it covers every `192.168.x.x`
  destination (typical home routers, NAS devices, workstations,
  printers), and the compose file declares `scarlix-net` as an EXTERNAL
  network whose subnet is not pinned in this repository, so the code
  cannot infer that a `192.168.x.x` address belongs to this deployment.
- The attack path closed: a compromised or poisoned resolver mapping an
  allowed endpoint name (`sglang`) to a LAN address (`192.168.1.1`)
  used to pass the v19.6.4 address policy — the public-IP exploit was
  closed in v19.6.4, but poisoned answers could still direct the POST
  body (the private prompts) to unrelated private-LAN destinations.
  Default trust now rejects `192.168.1.1`, `192.168.0.1` and every
  other unrelated LAN destination.
- Operators whose `scarlix-net` lives outside the default bridge pool
  declare their ACTUAL subnet explicitly: `docker network inspect
  scarlix-net` → `SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS=<that subnet>` —
  explicit, visible, auditable, never implicit. The compose env block,
  the SECURITY_MAP and the README all document exactly this derivation.
- Hardening adopted as policy (the independent audit's recommendation on
  `TrustedDialCIDRsFromEnv`): a UNIVERSAL route declaration
  (`0.0.0.0/0`, `::/0`) is REFUSED fail-closed at startup — a
  declaration that would nullify the destination boundary is a
  configuration error, not a posture. Operators declare the ACTUAL
  networks, never the whole address space.
- Loopback (`127.0.0.0/8`, `::1`) remains ALWAYS trusted and never
  needs declaring; a declared set still REPLACES the default; a
  service-name endpoint with loopback-only trust still fails AT
  CONSTRUCTION (all v19.6.4 semantics unchanged).
- Dual-stack note (the SK-audit P2-03 residual, now documented): a
  dual-stack service name may answer A + AAAA records; an unapproved
  IPv6 answer refuses the set WHOLE (fail-closed). On dual-stack Docker
  networks declare the IPv6 pool too (e.g. `fd00::/64` alongside the
  IPv4 subnet), or prefer IPv4 literals in the endpoints.
- Regression tests: `TestDefaultTrustedDialCIDRs` (the default is ONE
  pool; the audit's acceptance list `192.168.1.1`/`192.168.0.1`/
  `192.168.5.1` rejected), `TestDefaultTrustRejectsUnrelatedLAN
  Destinations` (a poisoned resolver answering a LAN address for an
  approved name → ZERO dials, zero packets, zero prompt bytes; the
  bridge pool still carries approved names — the default keeps working
  for what it actually is), `TestDeclaredSubnetMixedWithLANRefusedWhole`
  (one declared-subnet address + one unrelated LAN address → refused
  WHOLE), `TestDeclaredSubnetPinsAcrossResolveValidateDial` (the
  production-shaped resolve → validate → pin → dial against the
  CONFIGURED Docker subnet still succeeds), `TestTrustedDialCIDRsFromEnv`
  (universal routes rejected by name in both families; the specifically
  declared `172.18.0.0/16` accepted; an unrelated LAN address outside
  the declared subnet not trusted), `TestResolveDialCIDRsOption
  Precedence` (no option → exactly one default network).

### KPK-19.6.4-P2-02 (P2) — the probe-round recording is atomic and context-aware at one commit boundary
- `airuntime/health.go`: new `HealthTable.ObserveBatch(ctx, observations)`
  — the batch operation the audit asked for. Every observation is
  validated BEFORE any state change; the batch is monotonicity-checked
  against the current state under ONE lock; the writes are applied under
  that same lock. Atomicity is all-or-nothing: a batch containing an
  invalid or STALE member is rejected WHOLE — a partially applied round
  is never visible to a concurrent reader.
- The commit boundary is CONTEXT-AWARE: `ctx.Err()` is checked
  immediately before the writes begin. A cancellation arriving after
  the probes completed but before the commit records NOTHING — the
  context error returns `errors.Is`-identifiable (`context.Canceled` /
  `context.DeadlineExceeded`), and the table's previous observations
  AND timestamps stand.
- The linearization rule, documented on both functions: cancellation
  BEFORE the batch commit means ZERO writes; cancellation AFTER the
  commit does not retroactively undo a completed observation round.
- `airt/prober.go`: `ProbeAllAndObserve` commits the whole round through
  `ObserveBatch` — never a per-provider `Observe` loop that a
  cancellation could interrupt halfway (the v19.6.4 check-to-write race,
  closed). The v19.6.4 round-end guard in `ProbeAll` is unchanged and
  still first (a cancelled round never even reaches the commit).
- The injectable test seam (the audit's remediation item 4):
  `Prober.preCommit` runs between `ProbeAll` completion and the batch
  commit — nil in production (zero overhead, zero behavior change), set
  by the deterministic regression test to cancel the caller's context
  in EXACTLY the audited window.
- Regression tests: `TestObserveBatchCommitsWholeRound`,
  `TestObserveBatchAtomicOnInvalidMember` (one bad member → NOTHING
  applied), `TestObserveBatchAtomicOnStaleMember` (one stale member →
  the whole batch discarded, the fresh sibling does NOT land),
  `TestObserveBatchCancelledAtCommitBoundary` (the audit's exact
  acceptance: cancelled at the boundary → context-identifiable error,
  every previous state AND timestamp preserved, plus the live-context
  counterweight committing the same batch normally),
  `TestObserveBatchDeadlineAtCommitBoundary`,
  `TestObserveBatchConcurrentRoundsMonotone` (racing rounds — the newer
  round wins whole, never torn), `TestObserveBatchEdges` (nil table and
  nil context are named errors; an empty batch is a no-op success), and
  in `airt`: `TestProbeAllCancelBetweenRoundAndCommit` (cancel AFTER the
  round-end guard passes, BEFORE the commit → nothing recorded, no
  partial batch visible) + `TestProbeAllCommitAfterSeamStillCommits`
  (the seam's non-cancelling counterweight).

### The SK-audit residuals (P2-02 guidance / P2-03 documentation)
- The compose env block recommends the EXPLICIT narrow declaration
  (`docker network inspect scarlix-net` → `SCARLIHQ_AIRUNTIME_TRUSTED_
  CIDRS=<subnet>`) for production — the default bridge pool stays
  reasonable for zero-config, production narrows it.
- The inherent in-subnet lateral residual is documented in the
  SECURITY_MAP: a compromised resolver returning a different
  container/host in the SAME declared subnet still passes — CIDR trust
  cannot distinguish hosts within the declared net; the operator
  narrows the declaration, and a per-service expected-IP pin remains an
  optional CF-4+ item.

### Gates
- Smoke check 33 (`check_33_ai_trust_scope_commit_atomicity`) — the
  source-level gate over the whole remediation (the single-pool default
  by code marker, the universal-route refusal, the batch-commit
  boundary and the seam in code, the regression proofs as tests, the
  SECURITY_MAP truths, the compose guidance, the README surface, the
  release-notes genre). Inventory 32 → 33.
- check_32 evolved (the default-trust assertion now enforces the
  v19.6.5 single-pool posture; the v19.6.4 KPK-03/KPK-04 surface stays
  asserted as history). check_28 evolved (current-release → v19.6.5 +
  the mainline drift guard).

## Verification status
Verified by execution in the release environment (this repository, the
exact commands and their exit codes):

```
go build ./...                               → exit 0
go vet ./internal/... ./cmd/scarlihq         → exit 0
gofmt -l (scarlihq tree)                     → empty
go test ./... (full suite)                   → ok (all packages)
go test -race ./internal/airt/ ./internal/api/ ./internal/airuntime/ -count=1 → ok (race clean)
bash -n files/usr/local/bin/scarlix-smoke-test.sh → exit 0
scarlix-smoke-test.sh --offline              → 33 checks, 0 FAIL
(check_33 negative controls verified: the single-pool default / the
 commit-boundary guard / the ObserveBatch call — each break FAILs the
 check)
```

The audits' own unverified items remain honestly open where they were:
target-host hardware acceptance (KPK-06) is still OPEN; GitHub commit
statuses / workflow runs for the audited SHA were NOT CONFIRMED by the
independent audit environment (CI runs green on this repository's
mainline after the push, as before).

## Known limitation
- DEGRADED tiers still serve (the honesty-matrix decision, unchanged by
  design); the DEGRADED→fail vs HEALTHY→fail counters remain a CF-4
  audit item.
- The resolver is trusted to ANSWER, not to route: resolution inside
  the DECLARED networks is accepted (fail-closed on anything else).
  Lateral movement INSIDE a declared trusted network remains inherent
  to CIDR trust — the operator narrows the declaration to the smallest
  actual subnet; a per-service expected-IP pin is an optional CF-4+
  item, not a v19.6.5 deliverable.
- The 20-minute maximum wall clock at the widest `CHAT_TIMEOUT` setting
  remains bounded but operationally long; a dedicated concurrency limit
  on `/api/ai/chat` is deliberately NOT added in this remediation
  (scope discipline — it is a CF-4 audit-dashboard item, documented).
- Dual-stack deployments whose service names answer AAAA records must
  declare the IPv6 pool or use IPv4 literals (fail-closed on mixed
  A+AAAA answers is the policy, documented, not a bug).
