# ScarLiX OS v19.6.7 — Identity-Bound Family Profiles + Enforced Token Budgets + Media-Tools Hardening

**Release:** v19.6.7
**Commit:** [`8a783cf221f5e9c346474e4dd308aec0e17e21c1`](https://github.com/MoZoHuJa/OS/commit/8a783cf221f5e9c346474e4dd308aec0e17e21c1) — recorded at packaging (the release-notes genre: the hash lands with the tag)
**Date:** 2026-11-07
**Remediated findings:** KPK-19.6.6-P1-01 · KPK-19.6.6-P2-02 ·
KPK-19.6.6-P2-03 · KPK-19.6.6-P3-04 · KPK-19.6.6-P3-05 (the v19.6.6 KpK
audits) + SK-audit P2-03 (the experimental-pin allowlist)

## Scope
This patch remediates the findings of the v19.6.6 KpK audits (the EN
independent audit `ScarLiXOS_v19.6.6_KpK_Findings_and_Fixes_EN.md` —
CONDITIONAL PASS: "prior transport/atomicity findings fixed; profile
schema fixed; family authorization remains P1 OPEN; token-budget and
media-tools hardening remain P2 OPEN; hardware acceptance remains OPEN"
— and the SK release audit `SCARLIX_OS_v19.6.6_KPK_AUDIT.md` — APPROVED
FOR TAG with the profile-declaration residual named) against v19.6.6
(commit `96bfb23`). The remediation order follows the EN audit's own
release-blocking list: KPK-19.6.6-P1-01 first (bind family-profile
identity to authentication and remove the optional policy bypass), then
the enforced budget (P2-02) and the media-tools bounds (P2-03), then the
two P3 hygiene findings — and adopts the SK-audit follow-up (the
time-bounded pin allowlist for the four experimental `:latest` images).

The P1 is the centerpiece: v19.6.6 enforced a real policy schema against
a CALLER-DECLARED profile reference while the authentication boundary
knew one shared household Bearer token — the reference could be omitted
(skip the policy) or pointed at `zmor` (carry the admin policy). v19.6.7
binds identity to authentication: per-profile Bearer tokens, ONE shared
authorization function for REST and MCP, derivation when the reference
is omitted, and admin-only operator surfaces.

## Changes

### KPK-19.6.6-P1-01 (P1) — identity-bound family profiles (the release's centerpiece)
- New `scarlihq/internal/profiles/principals.go`: every profile MAY carry
  its own Bearer token. The token index loads from
  `/etc/scarlix/profile-tokens.conf` (one `profile:token` per line,
  `#` comments skipped) or inline `SCARLIHQ_PROFILE_TOKENS=name:token,...`.
  Loading is fail-closed: a world/group-readable file is refused (secrets
  are not ambient — 0640 or tighter), a malformed line is refused with
  its line number, token COLLISIONS between profiles are refused (one
  Bearer authenticates exactly one identity), and a profile token
  reusing the household `SCARLIHQ_TOKEN` is refused by name (rotate one
  of them). Resolution is constant-time per entry (`crypto/subtle`),
  entries iterated in sorted order.
- The household `SCARLIHQ_TOKEN` resolves to the ADMIN principal — the
  operator's own identity ("household"), never a family member's; a
  profile whose YAML declares `role: admin` carries an admin token too.
- ONE shared authorization function,
  `profiles.Authenticator.Authorize(principal, declared, action)`, decides
  the REST `/api/mode` boundary, `/api/ai/chat`, and MCP
  `scarlix_mode_set` (new `scarlihq/internal/api/principal.go` wires the
  REST side; `internal/mcp/server.go` resolves the same principal from
  the same token and calls the same function — one authorization
  function, not independent optional checks, the audit's requirement 4):
  - a DECLARED reference must load (a broken policy file is a 400-class
    fail-closed — the v19.6.6 contract, preserved), and in multi-user
    mode it must MATCH the authenticated principal's own profile —
    otherwise 403 `ErrProfileMismatch` ("principal %q cannot select
    profile %q"). A family token cannot select a foreign profile —
    neither `zmor` nor another family member's.
  - an OMITTED reference is DERIVED from the principal — there is no
    policy-free path in multi-user mode: the family member gets HER
    policy (permissions, mode policy, content filter), the household
    token derives the `role:admin` profile (a decidable admin path —
    a missing admin profile in multi-user mode is a named configuration
    defect, refused). An admin act-as (declaring a family profile) only
    REDUCES privileges — the target profile's policy still applies
    (`TestHouseholdTokenActAsOnlyReduces`: the household token act-as
    xox still honors xox's `can_switch=false`).
  - a principal whose OWN policy file is broken is refused 403
    (`ErrProfileUnavailable`) — the action is never decided without a
    policy, and never unrestricted either.
  - the `X-Scarlix-Profile` header CANNOT override the authenticated
    principal — the header entry path is checked identically to the
    body field (`TestFamilyTokenCannotSelectAdminProfile` exercises
    both entry paths for the refusal).
- Operator surfaces require the ADMIN principal (`authAdmin` in
  `rest.go`): a family member's per-profile token is refused 403 there
  ("this surface requires the household (admin) token"). The gated
  surfaces: compute start/stop, agent sessions/grants/audit (and the
  agent compute intake), bar commands/workspaces/executions/approvals,
  ai probe, ai propose/proposals/accept/dismiss, and the POST model
  registration on `/api/ai/models` (the GET list stays family-readable —
  the dashboard view). Read-only surfaces (`/api/health`, `/api/gpu`,
  `/api/status`, `/api/profiles`, `/api/ai/health`, `/api/ai/models`
  GET, MCP read-only tools) serve family tokens.
- Audit events (the audit's requirement 6): every authorization decision
  is logged — `AUDIT authz principal=… profile=… declared=… action=…
  decision=ALLOW|DENY reason=…` — NEVER a Bearer token.
- Single-user deployments (no token index): the household token is the
  ONE admin identity — explicit named mode (`SCARLIHQ_SINGLE_USER=true`
  logs it explicitly) or the implicit default (logs a pointer to the
  isolation guide). When an admin profile exists its policy applies
  (equivalent privileges to the legacy path, but DECIDABLE and
  auditable); deployments with NO profiles at all keep the legacy
  unrestricted path — named exactly as that in the audit event
  (`(single-user legacy: no profile policy)`), never mistaken for
  household isolation.
- Acceptance matrix (the audit's own list), now REST tests
  (`internal/api/principal_test.go`, 12 tests) + MCP tests
  (`internal/mcp/principal_test.go`, 6 tests): hugo token +
  `profile=zmor` → 403 (body and header entry paths); xox token +
  `profile=zmor` → 403; zmor token + `profile=zmor` → allowed; omitted
  reference → DERIVED (xox's token with NO reference still gets HER
  content filter and HER `can_switch=false` refusals); hugo token +
  `profile=mon` → 403 (family identities mutually isolated); the
  household act-as still policy-bound; an invalid token → 401; the
  single-user derivation and legacy paths; the operator-surface
  refusals; the same matrix through MCP (`scarlix_mode_set`).
- `profiles/zmor.yaml`: the derived admin policy now allows the FULL
  mode enumeration (`ai, stop, game, creative, turbo, offline, tv` — was
  4): the derivation must not restrict the operator
  (`TestAdminPrincipalDerivesFullModeSet`: the zmor token with NO
  reference derives the admin profile and the `stop` switch is
  accepted).

### KPK-19.6.6-P2-02 (P2) — the monthly token budget is ENFORCED, not displayed
- New durable ledger `scarlihq/internal/profiles/budget.go` (default
  path `/var/lib/scarlihq-budget/token-budget.json`): the grants.json
  discipline — 0600 file / 0700 directory, atomic tmp+rename persist,
  `schema_version 1`, and a fail-closed load (corrupt JSON, a foreign
  schema version, or loose permissions abort — "never run on a tampered
  ledger").
- `/api/ai/chat` (the provider-facing path) now RESERVES-BEFORE-FORWARD:
  the estimate (input runes / 4 per message + the fixed 4096-token
  output reserve) is held against the profile's `monthly` budget BEFORE
  the model is called — concurrent requests cannot collectively
  overspend (`TestBudgetConcurrentReservationsCannotOverspend` proves
  an 11-way concurrent reservation storm cannot exceed the month). After
  the response the reservation is SETTLED to the provider-reported usage
  (input + output; per-model usage recorded for the report); when the
  provider reports none, the estimate stands (the documented fallback).
  FAILED attempts are RELEASED whole — not billed (transport errors,
  refusals, failover attempts that never answered).
- The monthly window is a UTC calendar-month key (`2006-01` — explicit,
  no local-time ambiguity; `TestBudgetMonthWindowIsUTCCalendarMonth`),
  older months pruned to a 2-month rolling window, and accounting is per
  profile ACROSS models (routing a chat through a different model must
  not escape the budget; the declared model is the expected/default).
- Quota exhaustion is a STABLE HTTP 429 carrying the quota state
  `{profile, month, monthly, used, reserved, remaining}`
  (`TestBudgetQuotaExceededIsStable429`). Ledger unavailability fails
  CLOSED (503) when a budget applies — "an unaccountable budget is not
  a budget" (`ErrLedgerUnavailable`; a Reserve whose persist fails is
  rolled back, the hold is not granted).
- Without a ledger (an unwritable DEFAULT path) the declared budgets
  degrade to loudly-warned INFORMATIONAL metadata — the documented
  degraded policy: one startup warning + one request-stream warning say
  so. An EXPLICIT `SCARLIHQ_AIRUNTIME_BUDGET_LEDGER` that fails to
  load/validate ABORTS STARTUP instead (never run on a tampered ledger).
  Mount `/var/lib/scarlihq-budget` rw in the scarlihq compose or set the
  env to a writable path to enable enforcement.
- Tests: `internal/profiles/budget_test.go` (8 tests: reserve/settle
  accounting, release-not-billed, quota refusal, the 11-concurrent
  reservation race, the UTC month window + pruning, the persistence
  round-trip, the tamper discipline, the estimate) + the REST 429/settle
  tests in `principal_test.go`.

### KPK-19.6.6-P2-03 (P2) — media-tools: authenticated, bounded, deliberate
- `media-tools/server.py`: `MEDIA_TOOLS_TOKEN` Bearer authentication
  (`hmac.compare_digest` — the answer does not leak match timing);
  WITHOUT the token the conversion surface fails CLOSED (503, the body
  says the surface is closed) while `/health` stays open (liveness —
  no token, ever). Missing/wrong/non-Bearer Authorization → 401.
- Bounded concurrency: `threading.BoundedSemaphore` sized by
  `MEDIA_TOOLS_MAX_CONCURRENCY` (default 2, clamped 1..16); a request
  that cannot acquire a slot within the 15s queue wait gets a deliberate
  503. A per-client-IP cap of 2 concurrent conversions (429 beyond).
  An output-name collision lock: two parallel conversions writing the
  same output name → one 200, one 409 "output name busy" (the loser
  never clobbers the winner's file).
- Deliberate answers instead of crashes: an invalid `Content-Length`
  → 400 (not a `ValueError` traceback), a non-object JSON root
  (`[1,2,3]` / `42`) → 400 (not a 500), a 30s read timeout, and
  port/dir env seams (`MEDIA_TOOLS_PORT`, `MEDIA_TOOLS_INPUT_DIR` /
  `_OUTPUT_DIR` / `_TEMPLATES_DIR`) so the tests run the REAL server.
- `tests/test_media_tools.sh`: 42 assertions against real server
  instances — a REAL ffmpeg conversion (wav → mp3, SKIPped with a
  warning only when ffmpeg is absent), the fail-closed/401 matrix,
  malformed-header and non-object-root 400s, the saturation storm (5
  parallel conversions at `MAX_CONCURRENCY=1`: exactly one 200, one
  503 queue-full, three 429s, and the server SURVIVES — `/health`
  still 200), and the collision twin (200/409). 42 run, 0 failed,
  0 skipped at tag.

### KPK-19.6.6-P3-04 (P3) — strict NESTED `token_budget` YAML validation
- `TokenBudget.UnmarshalYAML` (loader.go) validates the nested mapping's
  keys BY HAND: only `monthly` and `model` are accepted, duplicate keys
  and unknown keys are named errors ("token_budget carries the UNKNOWN
  key %q — a typo here must fail, not ride along silently"). The outer
  `KnownFields(true)` decode never covered the nested node — a
  `monthly_limit_typo:` key could ride along silently next to a valid
  `monthly:`. `TestTokenBudgetNestedStrictValidation` drives the truth
  table (unknown key, duplicate monthly, duplicate model, non-node
  shapes).

### KPK-19.6.6-P3-05 (P3) — stale-audit: recursive installed-tree staleness + broader compose discovery
- `files/usr/local/bin/scarlix-stale-audit.sh`: on an installed tree
  (outside a Git checkout) a REGISTERED DIRECTORY's last-touch is now
  the NEWEST file mtime found RECURSIVELY under it (caches/generated
  data excluded by the find), not the directory's own mtime — editing
  an existing file inside `profiles/` no longer reads as stale (or as
  fresh-by-directory-entry-accident). Plain files keep the direct stat.
  The report still states which mode produced the age (git history vs
  "mtimes (installed tree — newest-file recursive, approximate)").
- Compose discovery broadened: `docker-compose*.yml` AND
  `docker-compose*.yaml` plus the modern `compose.yml` / `compose.yaml`
  short forms (`.git` still excluded) — future compose files are scanned
  by construction, not by luck.

### SK-audit P2-03 — the time-bounded pin allowlist
- New `docker/image-pin-allowlist.conf` (`image|reason|review-by`
  per line): an experimental image may ride `:latest` ONLY with a
  documented reason AND a review-by date. While today ≤ review-by the
  weekly stale-tree audit reports the image as `UNPINNED-ALLOWED` (an
  INFO line — visible debt, NOT a finding, no exit-code effect); the
  day after, the entry EXPIRES and the image reverts to a normal
  UNPINNED finding that names the expiry. The four known experimental
  `:latest` images (playwright-server, freetoken/serving,
  laya/decision-engine, cactus-compute/needle — all NO_ACCESS
  registries, unverifiable offline) are allowlisted until
  **2027-01-31**. "The default is debt, exceptions are borrowed" —
  pin-a-digest or archive the stack stays the real fix. An explicit
  override path (`SCARLIX_STALE_AUDIT_PIN_ALLOWLIST`) that is missing is
  named on stderr and audited as if no allowlist existed — never
  silently ignored.

### Housekeeping
- `profiles/zmor.yaml` allowed modes → the FULL enumeration (see P1-01
  above).
- Version bump v19.6.7: VERSION, install.sh, the Dockerfile ARG +
  header, main.go/rest.go vars (owned by the respective lanes).
- Docs: this release note; README (changelog row + What's New v19.6.7 +
  roadmap row + the current-release surface + the per-profile token
  story in the auth/known-limitations surface);
  `SCARLIX_SECURITY_MAP.md` (section (r) updated to the identity-bound
  contract, NEW section (t) the enforced token budget, section (s)
  updated for the recursion/discovery/allowlist); `SCARLIX_API_MAP.md`
  (the v19.6.7 identity note at the auth middleware).

## Verification status
Executed in the release environment (2026-11-07), with the parallel
release lanes mid-edit as noted:

```
cd scarlihq
go build ./...                     # PASS
go vet ./internal/... ./cmd/...    # PASS
go test ./...                      # PASS — all packages ok
go test -race ./internal/airt ./internal/api ./internal/airuntime \
                   ./internal/profiles ./internal/mcp -count=1   # PASS
gofmt -l .                         # 10 files listed at the time of
                                   #   writing (the v19.6.7-touched Go
                                   #   sources, space-indented) — the
                                   #   Go lane normalizes at packaging;
                                   #   re-run before tag
cd ..
python3 -m py_compile media-tools/server.py                  # PASS
bash tests/test_registry_classification.sh                   # 68/68
bash tests/test_dependency_inventory.sh                      # 44/44
bash tests/test_media_tools.sh                               # 42/42
bash files/usr/local/bin/scarlix-smoke-test.sh --offline     # 34 checks:
                                   #   mid-flight at the time of writing
                                   #   (27/5/2 before the README surface
                                   #   bump, 26/6/2 after): the failures
                                   #   are the route-registration markers
                                   #   for the OPERATOR surfaces (the code
                                   #   registers them behind h.authAdmin
                                   #   while the check still asserts
                                   #   h.auth) and the check_28
                                   #   current-release marker (asserting
                                   #   the v19.6.6 README surface this
                                   #   release bumps to v19.6.7) — the
                                   #   smoke-test lane's v19.6.7
                                   #   evolution is in flight; re-run at
                                   #   packaging
```

The Go suite, the race runs, the three bash suites and py_compile were
executed against the working tree as documented above. The offline
smoke test is re-run at packaging once the smoke-test lane's v19.6.7
check evolution lands; no smoke result is claimed beyond what is stated.

Hardware acceptance stays OPEN (KPK-06, unchanged since 19.3.x) — no
"production ready on the full OS" claim is made by this tag.

## Known limitation
- **The token budget covers the `/api/ai/chat` path only** (the
  provider-facing family surface). The propose path is admin-gated
  (P1-01) and unaffected by family budgets; a family principal cannot
  reach it at all.
- The content filter is keyword-level (a blocklist substring match on
  the chat input): honest, testable, and deliberately NOT advertised as
  semantic classification — a semantic classifier remains a CF-4+ item
  with the local model.
- Hardware acceptance remains OPEN (KPK-06): CI green is never claimed
  as hardware verification.
- The in-subnet lateral residual is inherent and unchanged (documented
  since v19.6.5): CIDR trust cannot distinguish hosts within a declared
  network. The IPv6 dual-stack note (mixed A+AAAA answer sets refused
  whole until an IPv6 pool is declared) stands as before.
- The four experimental `:latest` images remain unpinned — the
  allowlist makes the debt TIME-BOUNDED and visible, not resolved;
  pin-a-digest or archive the stack is still the next repo-hygiene
  milestone (review-by 2027-01-31).
- The budget's degraded mode is a documented policy, not a silent one:
  without a writable ledger the monthly budgets are informational
  metadata, and the runtime SAYS so (startup + request-stream warnings)
  — but an operator who ignores the warning runs unaccounted usage.
