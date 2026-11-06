# ScarLiX OS v19.6.6 — Trust Scope Enumerated + Family Profiles Enforced + Stale-Tree Automation

## Scope
This patch remediates the findings of the v19.6.5 KpK audits (the EN
independent audit `ScarLiXOS_v19.6.5_KpK_Findings_and_Fixes_EN.md` —
CONDITIONAL PASS: two new P2 transport findings, one legacy P1, two
legacy P2s and a P3 — and the SK release audit
`SCARLIX_OS_v19.6.5_KPK_AUDIT.md` — APPROVED FOR TAG with the
repo-hygiene process challenge) against v19.6.5 (commit `f85b1c3`).
The remediation order follows the EN audit's own "must fix" list:
KPK-19.6.5-P2-01 (the default CIDR), KPK-19.6.5-P2-02 (the
cancellation/lock-order race), then the legacy findings — with
KPK-19.6.5-P1-03 (the family profiles) as the release's centerpiece —
and closes with the audits' process recommendation made AUTOMATIC: the
stale-tree audit with the advisory local-AI triage.

## Changes

### KPK-19.6.5-P2-01 (P2) — the default CIDR is the ENUMERATED Docker pool, no non-canonical supernet
- `airt/transport.go`: the v19.6.5 default was the string
  `172.17.0.0/12` — a NON-canonical network address. Parsing it masks
  to `172.16.0.0/12`, which covers `172.16.0.0`–`172.31.255.255`
  including `172.16.0.0/16`, a range that is NOT part of Docker's
  documented default address pool (the pool begins at `172.17.0.0`) and
  is commonly corporate-VPN/overlay space. The same class of trust-scope
  mistake v19.6.5 removed for `192.168.0.0/16`, in a different private
  range.
- `DefaultTrustedDialCIDRs()` now returns the pool as FIFTEEN CANONICAL
  /16 entries (`dockerDefaultPoolCIDRs`: `172.17.0.0/16` …
  `172.31.0.0/16`) — the independent audit's backward-compatibility
  option: enumerate the exact documented pool CIDRs as separate
  entries, each canonical, each justifiable, no supernet that includes
  unrelated address space. Every user-defined network carved from the
  default pool still works; `172.16.0.1` (the audit's exact acceptance
  test) and `172.32.0.1` are now REJECTED by default.
- `TestDockerDefaultPoolCIDRsCanonical` proves the enumeration itself:
  exactly fifteen entries, each parses canonically (the literal IS the
  network), each a /16, the pool CONTIGUOUS, spanning
  `172.17.0.0/16`–`172.31.0.0/16`. `TestDefaultTrustedDialCIDRs` was
  evolved: canonicality per entry, the 172.16/172.32 rejections, the
  v19.6.4 LAN-rejection list unchanged.
- SK-audit P2-01 guidance adopted: `airt.UndeclaredServiceNameEndpoints`
  + main.go raise a loud STARTUP WARNING when service-name endpoints are
  configured while `SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS` is unset — the
  warning carries the exact derivation command
  (`docker network inspect scarlix-net -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}'`);
  a scarlix-net outside the default pool fails at DIAL time, not
  silently at boot. The compose env block and the SECURITY_MAP state the
  enumerated pool truth.
- Loopback remains always-trusted; a declared set still REPLACES the
  default; universal routes (`0.0.0.0/0`, `::/0`) remain REFUSED;
  construction-time fail-closed for loopback-only service names is
  unchanged.

### KPK-19.6.5-P2-02 (P2) — the context check lives UNDER the health-table mutex
- `airuntime/health.go`: the v19.6.5 `ObserveBatch` checked `ctx.Err()`
  BEFORE acquiring `t.mu` — if another goroutine held the lock, the call
  passed the check, BLOCKED on the lock, the caller cancelled while it
  waited, and when the lock was handed off the batch committed anyway.
  The context check now runs immediately AFTER `t.mu.Lock()` — the
  successful check under the lock is the linearization/commit acceptance
  point; a cancellation racing the hand-off can no longer slip a write
  through the waiting window.
- The deterministic regression proofs the audit demanded:
  `TestObserveBatchCancelledWhileWaitingForLock` (hold the mutex, start
  the batch, prove it parked, cancel WHILE waiting, release, assert
  `errors.Is(context.Canceled)` + ZERO writes + timestamps preserved)
  and `TestObserveBatchDeadlineWhileWaitingForLock` (the deadline
  expiry variant), each with the live-context counterweight committing
  the same batch through the same contended window.
- Shape validation stays outside the lock (no table state needed);
  monotonicity and the writes stay under the same lock; the all-or-
  nothing batch semantics are unchanged.

### KPK-19.6.5-P1-03 (P1) — the family profiles are typed, validated, and enforced server-side
- The v19.6.5 defect: `internal/profiles/loader.go` modeled five display
  fields and a STRING `token_budget` — but three of the four repository
  profiles (`hugo`, `mon`, `xox`) declare `token_budget` as a MAPPING
  (`monthly` + `model`), so `yaml.Unmarshal` FAILED and the loader
  SILENTLY returned filename-only fallback profiles (DisplayName=name,
  HudTheme=default), discarding the display name, role, theme and EVERY
  policy field. The permissions, `scarlix_mode` policy and XOX's content
  filter were not represented in the Go schema AT ALL, and no runtime
  enforcement existed anywhere in the tree. The audit: "if these
  profiles are intended to enforce user permissions, the current
  behavior is a security boundary failure, not just a display bug."
- The v19.6.6 schema is typed and total: `Permissions`, `ModePolicy`
  (default/allowed/can_switch), `TokenBudget` with a custom unmarshaler
  accepting exactly the two honest shapes (`unlimited` scalar for the
  admin; `{monthly, model}` mapping for family members — every other
  shape refused BY NAME), and `ContentFilter` (enabled + blocklist).
- Loading is STRICT: `yaml` `KnownFields` decoding (unknown keys are
  ERRORS for policy files — a typo'd `permissons:` cannot silently drop
  a permission), a closed validation matrix (role ∈ {admin, family};
  the `all` wildcard is admin-only; `scarlix_mode.default` ∈ `allowed`;
  `can_switch=false` allows exactly one mode; an enabled filter requires
  a non-empty blocklist; duplicates refused), and NO silent fallback:
  an invalid file is EXCLUDED from `List()` and returned as a named
  `Issue` — `/api/profiles` now answers
  `{"profiles": [...], "invalid": [...]}` and MCP `scarlix_profile_list`
  mirrors it. A policy-bearing file that fails to parse never becomes a
  permissive-looking placeholder.
- Enforcement is at the ACTION BOUNDARY, server-side:
  - `/api/mode/switch` (query `profile=`, JSON body field, or
    `X-Scarlix-Profile` header) and MCP `scarlix_mode_set` (the
    `profile` argument) enforce `can_switch=false` (XOX cannot switch
    AT ALL — 403 / JSON-RPC −32005, the refusal names the policy) and
    the allowed-set membership (refused BY NAME with the allowed set in
    the message).
  - `/api/ai/chat` (body field or header) enforces the `ai_chat`
    permission (403 with the declared set riding along) AND the enabled
    content-filter blocklist ON THE CHAT INPUT (403 with the category
    named — keyword-level, case-insensitive, honestly documented: a
    semantic classifier is a CF-4+ item with the local model, never a
    silent claim).
  - An unloadable profile reference FAILS CLOSED (400): a broken policy
    file is never an unenforced bypass.
- The honest residual (stated in the SECURITY_MAP §r): the household
  Bearer token remains the only authentication boundary; the profile
  reference is the caller's DECLARATION selecting which policy applies
  — per-profile authentication is the CF-4+ item that closes identity.
- Regression proofs: `internal/profiles/loader_test.go` (the four
  repository profiles parse WITHOUT fallback — embedded fixtures AND
  the on-disk walk; the dual-shape budget truth table; the negative
  schema matrix; `List` excludes + reports invalid files; the
  enforcement decisions) +
  `internal/api/profiles_enforcement_test.go` (the REST enforcement
  matrix: every refusal with its named error, the 202/200
  counterweights, every entry path, and the no-profile-reference
  compatibility case — a token-only chat keeps the pre-v19.6.6
  behavior).

### KPK-19.6.5-P2-04 (P2) — the media-tools build context is real
- `media-tools/` now ships its build context: a `Dockerfile` (pinned
  `python:3.12-alpine3.20` base + `ffmpeg` + `imagemagick` from the
  pinned alpine repo, non-root user, `HEALTHCHECK`, loopback contract
  left to the compose publish) and `server.py` — a deliberately small
  Python-stdlib conversion service implementing the compose contract
  the stack always declared: port 8200, `/data/input` → `/data/output`
  via JSON ffmpeg/imagemagick templates in `/data/templates`
  (`{input}`/`{output}` substitution, plain-filename sanitization, a
  fixed argv with NO shell, a 600s bound, the template must declare
  `{output}`). `build: .` in the compose file is finally a buildable
  statement. `templates/README.md` documents the template format and
  the no-raw-argv rule.

### KPK-19.6.5-P2-05 (P2) — the dependency inventory is generated and drift-guarded
- `docs/DEPENDENCY_AUDIT_2026-10-21.md` is now bannered as a HISTORICAL
  point-in-time snapshot (it still listed the pre-remediation pins —
  Ollama 0.5.4, CrowdSec v1.4.0, Coolify 4.0.30, Gitea 1.23, Nextcloud
  29, Headscale 0.25.0 — while the composes carried the remediated
  ones) and points at the current inventory.
- `docs/DEPENDENCY_INVENTORY_2026-11-06.md` is GENERATED from the active
  compose files: every image reference with its tag/digest, the
  locally-built services with their base images, the Go modules, the
  experimental `:latest` set REGISTERED as named residuals, and the
  honest limitation (pins evidenced as declared; registry existence is
  the runtime smoke's registry classification).
- `tests/test_dependency_inventory.sh` is the drift guard: every
  compose `image:` must appear in the inventory (a new pin without an
  row FAILs), every `build:` context must carry a Dockerfile (the
  P2-04 class, deterministic here), the historical banner must exist,
  and the experimental register must name its residuals — 44
  assertions, 0 fail at tag.

### KPK-19.6.5-P3-06 (P3) — models.yaml identity reconciled
- The stale `v19.2.9` release identity is gone from the header (the
  VERSION file is the single source of truth for the release identity).
- `qwen2.5:3b` is THE canonical Ollama identifier: the phantom
  `qwen3.6:14b` guidance is removed (the same phantom v17.9.5 caught in
  the GGUF file name — it never existed as a pullable tag), and the
  profiles' `token_budget.model` references the canonical id. One
  canonical identifier per runtime provider; the production tier is
  SGLang's Qwen3-14B-AWQ.

### The process remediation — the stale-tree audit automation (the audits' asymmetry finding)
- The v19.6.5 audits' process verdict: the AI-runtime line
  19.6.1→19.6.5 is a model KpK sequence, BUT the repository's attention
  is asymmetric — `profiles/` sat 85 days (where the P1 lived),
  `docker/` 39, `media-tools/` 38 with the broken build, and the
  experimental `:latest` register relied on v18.8 comments. The
  recommendation was a stale-tree review pass; v19.6.6 makes it
  AUTOMATIC:
  - `files/usr/local/bin/scarlix-stale-audit.sh` — the audit: (1)
    staleness of every REGISTERED path against its classification
    (ACTIVE / OPTIONAL / EXPERIMENTAL / ARCHIVED) and its max-stale
    threshold (git last-touch in a checkout, mtime on an installed
    tree, the report states which); (2) every `:latest`/tagless image
    reference in every compose file; (3) every compose `build:` context
    missing its Dockerfile (the P2-04 class); (4) every UNCLASSIFIED
    top-level path — the registry IS the audited contract, an
    unregistered path is unaudited by construction. Exit codes are
    CI-gate friendly (0 clean / 1 findings / 2 usage).
  - `scarlix-stale-audit.service` + `.timer` (weekly Mon 06:30,
    persistent): the schedule. The unit treats findings (exit 1) as
    SUCCESS — a timer failure means the audit itself broke, not that
    debt was found. Reports land in `/var/log/scarlix/stale-audit/`.
  - `--ai-summary` — the AI Local Model agent, ADVISORY ONLY: after the
    audit, the report is POSTed to the LOCAL Ollama tier (loopback,
    `curl --noproxy '*'` — the airt transport lesson applied to ops
    tooling; the canonical starter model `qwen2.5:3b`) for triage —
    which findings block release integrity, which are hygiene, what
    order to fix. The triage is saved next to the report and changes
    NOTHING: "LLM navrhuje, politika rozhoduje". The AI step failing is
    a WARNING, never an audit failure.
  - `docs/STALE_TREE_AUDIT.md` — the policy: the classification
    contract, the thresholds, the advisory discipline, the operator
    runbook (keep / archive / update, in that order of honesty).
  - `install.sh` copies the script + units and enables the timer.
- The SECURITY_MAP's experimental register (§h) now carries review
  dates and points at the weekly automation; the inventory (§3 of the
  generated doc) mirrors it.

### Housekeeping
- Version bump v19.6.6: VERSION, install.sh, the Dockerfile ARG +
  header, main.go and rest.go vars.
- README: the v19.6.6 changelog row, the What's New section, the
  roadmap row, the current-release surface (banner/h1/badge/version
  line/install header + the mainline drift guard), and the STALE
  KPK-03 trusted-dial row refreshed to the enumerated-pool truth.
- SECURITY_MAP: sections (p) updated (the enumerated pool, the
  under-lock commit boundary), (h) refreshed (register dates + the
  weekly automation), and NEW sections (r) family profiles — typed
  schema, strict load, server-side enforcement, the honest identity
  residual — and (s) the stale-tree automation.
- Smoke test: check_34
  (`check_34_trust_enumeration_profiles_enforcement_stale_automation`,
  inventory 33 → 34) with THREE verified negative controls (reverting
  the pool to the /12 shorthand FAILs the enumeration marker; moving
  `ctx.Err()` above `t.mu.Lock()` FAILs the order assertion; removing
  the profile enforcement call from the mode handler FAILs the wiring
  marker); check_32/check_33 evolved (the single-pool marker → the
  enumerated-pool marker, the v19.6.5 posture kept as history);
  check_28 evolved (current-release → v19.6.6 + the v19.6.4/v19.6.5/
  v19.6.6 What's New permanence).

## Verification status
All of the following were executed in the release environment:

```
cd scarlihq
go build ./...                     # clean
go vet ./internal/... ./cmd/...    # clean
gofmt -l .                         # clean
go test ./...                      # 24 packages ok (profiles package
                                   #   now carries its own suite)
go test -race ./internal/airt ./internal/api ./internal/airuntime ./internal/profiles -count=1   # clean
cd ..
bash files/usr/local/bin/scarlix-smoke-test.sh --offline    # 34 checks, 0 FAIL
bash tests/test_registry_classification.sh                  # 68/68
bash tests/test_dependency_inventory.sh                     # 44/44 (NEW)
bash files/usr/local/bin/scarlix-stale-audit.sh --root .    # findings: the named
                                   # residuals (profiles stale-age history,
                                   # the four experimental :latest) — the
                                   # audit names them every week now
bash -n install.sh media-tools/../files/usr/local/bin/scarlix-stale-audit.sh tests/test_dependency_inventory.sh   # clean
```

Hardware acceptance stays OPEN (KPK-06, unchanged since 19.3.x) — no
"production ready on the full OS" claim is made by this tag. The
AI-runtime transport boundary claims remain scoped to what the tests
prove.

## Known limitation
- Profile identity is the CALLER'S DECLARATION until per-profile
  authentication lands (CF-4+): a family member declaring "zmor"
  carries the admin policy — the enforcement is real, the identity is
  not, and the SECURITY_MAP §r states it as the honest residual.
- The content filter is keyword-level (a blocklist substring match on
  the chat input): honest, testable, and deliberately NOT advertised as
  semantic classification — that is a CF-4+ item with the local model.
- The in-subnet lateral residual is inherent and unchanged (documented
  since v19.6.5): CIDR trust cannot distinguish hosts within a declared
  network.
- The stale-tree audit names debt; it does not fix it. The
  classification registry needs a deliberate edit when a top-level path
  is added — the script makes forgetting a finding, not a habit.
- The four experimental `:latest` images remain unpinned (named
  residual, registered in the inventory + the SECURITY_MAP + the weekly
  audit): pin-or-archive is the next repo-hygiene milestone.
