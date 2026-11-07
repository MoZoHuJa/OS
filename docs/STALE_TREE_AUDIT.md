# SCARLIX OS — Stale-Tree Audit Policy (v19.6.7)

**Owner:** the operator (the audit runs itself; the DECISIONS stay human)
**Automation:** `files/usr/local/bin/scarlix-stale-audit.sh` + `scarlix-stale-audit.timer` (weekly, Mon 06:30, persistent)
**Origin:** the v19.6.5 KpK audits' process finding — the repository's attention asymmetry

---

## 1. Why this exists

The v19.6.5 audits closed the AI-runtime transport findings and then named the
systemic problem: *scarlihq/ and docs/ harden daily while `profiles/` sat 85
days (accumulating the P1 schema drift the audit found), `docker/` 39,
`media-tools/` 38 with a broken build context, `monitoring/` and `voice/`
35+.* Age is not proof of defects — but unaudited age is where defects live.
The audits' recommendation was a **stale-tree review pass**; this mechanism
makes that pass **automatic, scheduled, and evidence-producing** instead of a
one-off promise.

## 2. What the audit checks

| Check | Class | Example it would have caught |
|---|---|---|
| Staleness vs classification | STALE | `profiles/` at 85d vs a 60d threshold (the P1 lived there) |
| Unpinned images | UNPINNED | `mcp/playwright-server:latest`, `freetoken/serving:latest`, `laya/decision-engine:latest`, `ghcr.io/cactus-compute/needle:latest` |
| Missing build contexts | NO-BUILD-CTX | `media-tools/docker-compose.yml` `build: .` with no Dockerfile (KPK-19.6.5-P2-04) |
| Unclassified top-level paths | UNCLASSIFIED | any new directory nobody registered — unaudited by construction |
| Registered-but-absent paths | INFO | pruned stacks — the registry stays honest |

Last-touch dates come from **git history** in a checkout and from **mtimes**
on an installed tree (`/opt/scarlix`) — the report states which.

## 3. The classification registry (the contract)

The registry lives INSIDE the script (one place, reviewed in code review):

```
path|class|max_stale_days|note
```

- **ACTIVE** — in the default install path; tight thresholds (30–60d).
- **OPTIONAL** — operator-enabled stacks; monthly-scale thresholds.
- **EXPERIMENTAL** — non-default; the pin-or-archive policy applies.
- **ARCHIVED** — historical evidence; exempt from staleness, documented.

Adding a top-level path without classifying it is itself a finding — the
registry is the audited surface, not the whole filesystem.

## 4. The AI Local Model agent (advisory triage)

With `--ai-summary` (the systemd unit passes it), after the audit the report
is POSTed to the **local Ollama tier**:

```
SCARLIX_STALE_AUDIT_OLLAMA_URL=http://127.0.0.1:11435   # the repo's Tier-4 host port
SCARLIX_STALE_AUDIT_OLLAMA_MODEL=qwen2.5:3b              # the canonical starter model
```

The model is asked to triage: which findings block release integrity, which
are hygiene, what order to fix them, what needs a human security decision.
The triage is saved next to the report (`*-ai.md`).

**Discipline (unchanged, deliberately):**

- `curl --noproxy '*'` — no ambient proxy, loopback only; the airt transport
  lesson applied to ops tooling too.
- **ADVISORY ONLY** — "LLM navrhuje, politika rozhoduje": the triage changes
  nothing by itself; no auto-mutation of the tree, no auto-prune.
- The AI step failing (tier down, no model) is a WARNING, never an audit
  failure — the report stands on its own.

## 5. Outputs and exit codes

- Report: `/var/log/scarlix/stale-audit/stale-audit-YYYYmmdd-HHMM.md` (+ `-ai.md`)
- Exit `0` clean · `1` findings (CI-gate friendly: wire it into any gate with
  `|| [ $? -le 1 ]`) · `2` usage/roots error.
- The systemd unit treats exit 1 as success — a timer FAILURE means the audit
  itself broke, not that debt was found.

## 6. Operator runbook

```bash
# manual run, report on stdout
scarlix-stale-audit.sh

# manual run with the AI triage
scarlix-stale-audit.sh --ai-summary

# as the timer runs it (installed tree)
systemctl start scarlix-stale-audit.service
less /var/log/scarlix/stale-audit/stale-audit-*.md
```

When a finding lands: **keep / archive / update**, in that order of honesty —
verify the path is still referenced (`install.sh`, docs, SECURITY_MAP), then
either update it (pins, build contexts, schema tests) or archive it under
`docs/archive/` with a dated note. The stale-age finding CLOSES only when the
path is touched by a real change or reclassified — cosmetic churn does not
count (the script cannot tell, the operator can).

## 7. v19.6.7 — the honest-approximation pass

Three audit findings against the audit itself (KPK-19.6.6-P3-05, the
compose-discovery residual, SK-audit P2-03), closed without breaking a
single behavior contract (exit codes, report shape, timer wiring).

**Installed-tree staleness now reads the NEWEST file mtime RECURSIVELY under
a registered directory.** The old code stat'ed the directory itself — but a
directory's own mtime only moves when entries are created or deleted;
*editing* an existing file inside never touches the parent, so a healthy
installed tree read STALE. The non-git path now walks the tree (excluding
`.git`, `node_modules`, `__pycache__`, `.venv`, `*.pyc` — caches churn
without content change) and takes the newest regular-file mtime, falling
back to the directory's own mtime when the recursion is empty. Plain files
keep the one-level stat; a checkout keeps git history. **The remaining
approximation, stated honestly:** mtime is still not a change record — a
copy, a checkout, or a plain `touch` moves it without any content change,
so the installed-tree date can read FRESHER than reality (never staler than
the old behavior read). Git history in a checkout remains the honest
source; the report header names which mode ran.

**Compose discovery broadened** to `docker-compose*.yaml`, `compose.yml`,
and `compose.yaml` (next to `docker-compose*.yml`, `.git` paths still
excluded). A stack named the modern way was previously invisible to the
unpinned-image and missing-build-context analysis by construction — the
same analysis now runs over every discovered file.

**The pin allowlist (SK-audit P2-03).** The four experimental images riding
`:latest` (`mcp/playwright-server`, `freetoken/serving`,
`laya/decision-engine`, `ghcr.io/cactus-compute/needle`) cannot be
digest-pinned offline — the registries answer NO_ACCESS to anonymous API
probes, and real digest pinning needs network pulls the audit refuses to
make. The honest fix is not silence but a **review-by-dated exception
registry**: `docker/image-pin-allowlist.conf`, format
`image|reason|review-by` (YYYY-MM-DD).

- **Who edits it:** the operator, deliberately, in code review — the same
  discipline as the classification registry: few entries, dated, reasoned;
  never bulk, never evergreen.
- **While `today <= review-by`:** the image is reported as
  `UNPINNED-ALLOWED` (an INFO line carrying the reason and the date) —
  visible debt, NOT a finding, no exit-code effect.
- **When the entry expires** (`review-by < today`): the image reverts to a
  normal UNPINNED finding with an explicit "allowlist entry EXPIRED (was
  allowed until DATE)" note — the audit fails until the entry is
  re-justified with a new date or the image is pinned/archived.
- **No allowlist file:** behavior is unchanged — every `:latest` is a
  finding.
- An explicit override is available via `SCARLIX_STALE_AUDIT_PIN_ALLOWLIST`
  (a missing override file is named on stderr and audited as if no
  allowlist existed).

Pin-a-digest or archive remains the real fix; the allowlist only keeps the
residual NAMED and time-boxed.
