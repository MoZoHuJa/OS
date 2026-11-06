#!/usr/bin/env bash
# SCARLIX OS v19.6.6 — Stale-tree audit (the repo-hygiene automation)
#
# WHY (the v19.6.5 KpK audits' process finding): the repository's attention
# is ASYMMETRIC — scarlihq/ + docs/ harden daily while profiles/ sat 85 days,
# docker/ 39, media-tools/ 38 with a BROKEN build context, monitoring/ and
# voice/ 35+ — "age alone is not proof of defects, but KpK requires the debt
# NAMED". This script names it AUTOMATICALLY, on a schedule, forever:
#
#   1. STALENESS — every registered path is checked against its
#      classification and its maximum-stale threshold (git last-touch date
#      in a checkout, file mtime on an installed tree);
#   2. UNPINNED IMAGES — every compose file's image references are checked
#      for :latest / missing tags (the experimental-supply-chain residual);
#   3. MISSING BUILD CONTEXTS — every compose `build:` is checked for an
#      existing Dockerfile (the exact KPK-19.6.5-P2-04 class);
#   4. UNCLASSIFIED PATHS — a top-level path absent from the registry is
#      itself a finding (the classification is the contract).
#
# AI LOCAL MODEL AGENT (--ai-summary): after the audit, the report is POSTed
# to the LOCAL Ollama tier (loopback, no ambient proxy — the airt lesson
# applied) for triage: the model prioritizes the findings and proposes the
# maintenance order. ADVISORY ONLY — "LLM navrhuje, politika rozhoduje":
# the summary lands next to the report, nothing in the tree is touched.
#
# Exit codes: 0 = clean, 1 = findings (CI-gate friendly), 2 = usage error.
#
# Usage: scarlix-stale-audit.sh [--root DIR] [--ai-summary] [--quiet]

set -euo pipefail

VERSION_MARKER="v19.6.6"
SCRIPT_NAME="$(basename "$0")"

usage() {
  cat <<EOF
$SCRIPT_NAME $VERSION_MARKER — SCARLIX OS stale-tree audit

  --root DIR     the tree to audit (default: /opt/scarlix or \$PWD when it
                 looks like a checkout — a git repo wins, mtimes otherwise)
  --ai-summary   additionally POST the report to the local Ollama tier for
                 an advisory triage (loopback-only, no ambient proxy)
  --quiet        suppress the report on stdout (the file is still written)
  -h | --help    this help

Environment:
  SCARLIX_STALE_AUDIT_LOG_DIR        default /var/log/scarlix/stale-audit
  SCARLIX_STALE_AUDIT_OLLAMA_URL     default http://127.0.0.1:11435
  SCARLIX_STALE_AUDIT_OLLAMA_MODEL   default qwen2.5:3b
EOF
}

# ---------------------------------------------------------------------------
# The classification registry — THE CONTRACT (edit here, deliberately).
# Columns: path|class|max_stale_days|note
#   ACTIVE       — in the default install path, audited tightly
#   OPTIONAL     — operator-enabled stacks, audited monthly-scale
#   EXPERIMENTAL — non-default, pinned-or-archived policy applies
#   ARCHIVED     — historical evidence, exempt from staleness (documented)
# A path present in the tree but ABSENT here is itself a finding.
# ---------------------------------------------------------------------------
read -r -d '' REGISTRY <<'EOF' || true
profiles|ACTIVE|60|family policy — the v19.6.5 P1 lived here (schema drift, silent fallback)
docker|OPTIONAL|60|daemon/proxy definitions — verify against the current host-bridge model
media-tools|OPTIONAL|60|optional stack — build context + image pins
monitoring|OPTIONAL|60|exporters + dashboards — pins + healthchecks
voice|OPTIONAL|60|voice stack — model/service compatibility
skills|ACTIVE|45|agent instructions — verify against current APIs
README.md|ACTIVE|60|the front door — What's New + security map sections
VERSION|ACTIVE|30|the release identity — moves every release
install.sh|ACTIVE|30|the installer — moves every release
models.yaml|ACTIVE|45|the model registry source of truth (KPK-19.6.5-P3-06 lived here)
AGENTS.md|ACTIVE|60|agent repo instructions
LICENSE|ARCHIVED|0|legal — exempt
.gitignore|ARCHIVED|0|vcs plumbing — exempt
.env.template|ACTIVE|60|the env surface template
packages.x86_64|ACTIVE|60|EndeavourOS package list
render-banner.mjs|ACTIVE|60|release banner renderer
agents|ACTIVE|45|agent integrations
ai|ACTIVE|45|model runtimes + experimental stacks (:latest policy!)
hp-agent|ACTIVE|45|HP agent integration
network|ACTIVE|45|network services
security|ACTIVE|45|security services
gaming|OPTIONAL|45|gaming stack
workspace|OPTIONAL|45|workspace services
tests|ACTIVE|30|the regression inventory itself
scarlihq|ACTIVE|30|the control plane
docs|ACTIVE|30|documentation
files|ACTIVE|30|installed file payloads
EOF

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
ROOT=""
AI_SUMMARY=0
QUIET=0
LOG_DIR="${SCARLIX_STALE_AUDIT_LOG_DIR:-/var/log/scarlix/stale-audit}"
OLLAMA_URL="${SCARLIX_STALE_AUDIT_OLLAMA_URL:-http://127.0.0.1:11435}"
OLLAMA_MODEL="${SCARLIX_STALE_AUDIT_OLLAMA_MODEL:-qwen2.5:3b}"

while [ $# -gt 0 ]; do
  case "$1" in
    --root) ROOT="${2:?--root needs a value}"; shift 2 ;;
    --ai-summary) AI_SUMMARY=1; shift ;;
    --quiet) QUIET=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) printf '%s: unknown argument %s\n' "$SCRIPT_NAME" "$1" >&2; usage >&2; exit 2 ;;
  esac
done

# Resolve the audit root: explicit --root wins; else /opt/scarlix when it
# exists (the installed tree); else the current directory when it carries
# the repo markers (a checkout).
if [ -z "$ROOT" ]; then
  if [ -d /opt/scarlix ] && [ -f /opt/scarlix/docker-compose.yml ] 2>/dev/null; then
    ROOT=/opt/scarlix
  elif [ -f ./VERSION ] && [ -f ./install.sh ]; then
    ROOT="$PWD"
  else
    printf '%s: no audit root (use --root DIR, run from a checkout, or install to /opt/scarlix)\n' "$SCRIPT_NAME" >&2
    exit 2
  fi
fi
[ -d "$ROOT" ] || { printf '%s: root %s is not a directory\n' "$SCRIPT_NAME" "$ROOT" >&2; exit 2; }

# The last-touch date of a path: git in a checkout (the honest history),
# mtime otherwise (the installed-tree approximation — stated in the report).
IN_GIT=0
if git -C "$ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  IN_GIT=1
fi

last_touch() { # → YYYY-MM-DD (empty when undeterminable)
  local path="$1"
  if [ "$IN_GIT" -eq 1 ]; then
    git -C "$ROOT" log -1 --format=%cd --date=short -- "$path" 2>/dev/null || true
  else
    stat -c %y "$path" 2>/dev/null | cut -d' ' -f1 || true
  fi
}

today_epoch() { date +%s; }
date_epoch() { date -d "$1" +%s 2>/dev/null || echo 0; }

FINDINGS=0
REPORT_TMP="$(mktemp)"
trap 'rm -f "$REPORT_TMP"' EXIT

NOW="$(date +%Y-%m-%d)"
HOSTN="$(hostname 2>/dev/null || echo unknown)"
{
  echo "# SCARLIX OS stale-tree audit — $NOW"
  echo
  echo "- root: \`$ROOT\` ($([ "$IN_GIT" -eq 1 ] && echo 'git history' || echo 'mtimes (installed tree — approximate)'))"
  echo "- marker: $VERSION_MARKER, host: $HOSTN"
  echo
  echo "## Findings"
} > "$REPORT_TMP"

add_finding() { # severity|class|path|detail
  FINDINGS=$((FINDINGS + 1))
  printf -- '- **[%s|%s]** `%s` — %s\n' "$1" "$2" "$3" "$4" >> "$REPORT_TMP"
}

# ---------------------------------------------------------------------------
# 1. Staleness against the registry
# ---------------------------------------------------------------------------
REG_COUNT=0
while IFS='|' read -r path klass max_days note; do
  [ -z "$path" ] && continue
  REG_COUNT=$((REG_COUNT + 1))
  full="$ROOT/$path"
  if [ ! -e "$full" ]; then
    add_finding "INFO" "$klass" "$path" "registered but NOT PRESENT in this tree (pruned, or a different install profile — verify the registry)"
    continue
  fi
  [ "$klass" = "ARCHIVED" ] && continue  # exempt, documented
  touched="$(last_touch "$path")"
  if [ -z "$touched" ]; then
    add_finding "WARN" "$klass" "$path" "last-touch date undeterminable ($([ "$IN_GIT" -eq 1 ] && echo 'no git history' || echo 'stat failed'))"
    continue
  fi
  now_e=$(today_epoch)
  touch_e=$(date_epoch "$touched")
  if [ "$touch_e" -eq 0 ]; then
    add_finding "WARN" "$klass" "$path" "last-touch date '$touched' unparseable"
    continue
  fi
  age_days=$(( (now_e - touch_e) / 86400 ))
  if [ "$age_days" -gt "$max_days" ]; then
    add_finding "STALE" "$klass" "$path" "${age_days}d since $touched (threshold ${max_days}d) — $note; keep/archive/update pass REQUIRED (age is not proof of defects, it is NAMED debt)"
  fi
done <<< "$REGISTRY"

# ---------------------------------------------------------------------------
# 2. Unclassified top-level paths — the registry is the contract
# ---------------------------------------------------------------------------
while IFS= read -r entry; do
  name="$(basename "$entry")"
  [ -z "$name" ] && continue
  case "$name" in .|..|.git|.github) continue ;; esac
  if ! grep -qF "$name|" <<< "$REGISTRY"; then
    add_finding "UNCLASSIFIED" "-" "$name" "top-level path absent from the classification registry — classify it (ACTIVE/OPTIONAL/EXPERIMENTAL/ARCHIVED) or archive it; an unregistered path is unaudited by construction"
  fi
done < <(find "$ROOT" -maxdepth 1 -mindepth 1 | sort)

# ---------------------------------------------------------------------------
# 3. Unpinned images + missing build contexts (all compose files)
# ---------------------------------------------------------------------------
while IFS= read -r compose; do
  rel="${compose#"$ROOT"/}"
  # image refs: `image: name:tag` — flag :latest and tagless refs
  while IFS= read -r image; do
    [ -z "$image" ] && continue
    case "$image" in
      *:latest) add_finding "UNPINNED" "-" "$rel" "image \`$image\` rides :latest (KPK supply-chain residual) — pin a tag/digest or archive the stack" ;;
      *:*) : ;;
      *) add_finding "UNPINNED" "-" "$rel" "image \`$image\` carries NO tag — pin one (a floating repo default is :latest in disguise)" ;;
    esac
  done < <(grep -hE '^[[:space:]]*image:[[:space:]]*[^[:space:]]+' "$compose" | sed -E 's/^[[:space:]]*image:[[:space:]]*//; s/[[:space:]]+#.*$//' | sort -u)
  # build contexts: `build: .` or `build:`+`context:` — the context dir must exist
  ctx_dirs="$(grep -hE '^[[:space:]]*build:[[:space:]]*\.' "$compose" | sed -E 's/^[[:space:]]*build:[[:space:]]*//; s/[[:space:]]+#.*$//' | sort -u || true)"
  if grep -qE '^[[:space:]]*build:$' "$compose"; then
    ctx_dirs="$ctx_dirs
$(grep -A1 -E '^[[:space:]]*build:$' "$compose" | grep -E 'context:[[:space:]]*\.' | sed -E 's/.*context:[[:space:]]*//; s/[[:space:]]+#.*$//' || true)"
  fi
  while IFS= read -r ctx; do
    [ -z "$ctx" ] && continue
    ctx="${ctx#./}"
    ctx_full="$ROOT/$(dirname "$rel")/$ctx"
    if [ ! -f "$ctx_full/Dockerfile" ]; then
      add_finding "NO-BUILD-CTX" "-" "$rel" "build context \`$ctx\` has NO Dockerfile (the KPK-19.6.5-P2-04 class) — add the context or replace build: with a pinned image"
    fi
  done <<< "$(printf '%s\n' "$ctx_dirs" | sort -u)"
done < <(find "$ROOT" -name 'docker-compose*.yml' -not -path '*/.git/*' | sort)

if [ "$FINDINGS" -eq 0 ]; then
  echo "- none — every registered path within threshold, every image pinned, every build context present, every top-level path classified" >> "$REPORT_TMP"
fi

{
  echo
  echo "## Classification registry ($REG_COUNT paths)"
  echo
  echo "| path | class | max stale | note |"
  echo "|------|-------|-----------|------|"
  while IFS='|' read -r path klass max_days note; do
    [ -z "$path" ] && continue
    echo "| \`$path\` | $klass | ${max_days}d | $note |"
  done <<< "$REGISTRY"
  echo
  echo "---"
  echo "*Advisory automation — \"LLM navrhuje, politika rozhoduje\": findings are named debt, not auto-mutations.*"
} >> "$REPORT_TMP"

# ---------------------------------------------------------------------------
# Persist + emit
# ---------------------------------------------------------------------------
mkdir -p "$LOG_DIR" 2>/dev/null || true
REPORT_FILE=""
if [ -d "$LOG_DIR" ] && [ -w "$LOG_DIR" ]; then
  REPORT_FILE="$LOG_DIR/stale-audit-$(date +%Y%m%d-%H%M).md"
  cp "$REPORT_TMP" "$REPORT_FILE"
fi

if [ "$QUIET" -eq 0 ]; then
  cat "$REPORT_TMP"
  [ -n "$REPORT_FILE" ] && printf '\n(report saved: %s)\n' "$REPORT_FILE" >&2
fi

# ---------------------------------------------------------------------------
# The AI Local Model agent — ADVISORY triage of the report (loopback only,
# no ambient proxy: the airt transport lesson applied to ops tooling too).
# Failure of the advisory layer NEVER fails the audit itself.
# ---------------------------------------------------------------------------
if [ "$AI_SUMMARY" -eq 1 ]; then
  PROMPT='You are the SCARLIX OS maintenance triage agent. Below is a stale-tree audit report. Prioritize the findings (which blocks release integrity, which is hygiene), propose a concrete remediation order with effort estimates, and name any finding that needs a human security decision. Be concise: a table plus a short "do first / do next" list. You ADVISE — no action is taken automatically.'

  if command -v curl >/dev/null 2>&1; then
    # jq-free JSON assembly via python when present, else a raw --data payload
    if command -v python3 >/dev/null 2>&1; then
      PAYLOAD="$(python3 - "$OLLAMA_MODEL" "$PROMPT" "$REPORT_TMP" <<'PY'
import json, sys
model, prompt, path = sys.argv[1], sys.argv[2], sys.argv[3]
with open(path, encoding="utf-8") as fh:
    report = fh.read()
print(json.dumps({"model": model, "prompt": prompt + "\n\n---\n" + report, "stream": False}))
PY
)"
    else
      # escape minimal JSON by hand (quotes + newlines) — good enough for a prompt
      ESC_REPORT="$(sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' "$REPORT_TMP" | awk '{printf "%s\\n", $0}')"
      PAYLOAD="{\"model\": \"$OLLAMA_MODEL\", \"stream\": false, \"prompt\": \"${PROMPT//$'\n'/\\n}\\n\\n---\\n${ESC_REPORT}\"}"
    fi
    AI_FILE="${REPORT_FILE:+${REPORT_FILE%.md}-ai.md}"
    if RESP="$(curl -sS --noproxy '*' --max-time 300 \
        -H 'Content-Type: application/json' \
        -d "$PAYLOAD" \
        "$OLLAMA_URL/api/generate" 2>/dev/null)" && [ -n "$RESP" ]; then
      SUMMARY="$(printf '%s' "$RESP" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("response","(empty response)"))' 2>/dev/null || true)"
      if [ -z "$SUMMARY" ]; then
        SUMMARY="$RESP"
      fi
      if [ -n "$AI_FILE" ]; then
        {
          echo "# Stale-tree audit — AI triage (advisory)"
          echo
          echo "- model: \`$OLLAMA_MODEL\` at \`$OLLAMA_URL\` (local tier, loopback)"
          echo
          echo "$SUMMARY"
          echo
          echo "---"
          echo "*LLM navrhuje, politika rozhoduje — this triage changes nothing by itself.*"
        } > "$AI_FILE"
        printf '(AI triage saved: %s)\n' "$AI_FILE" >&2
      else
        printf '\n## AI triage (advisory)\n\n%s\n' "$SUMMARY" >&2
      fi
    else
      printf 'WARNING: AI triage unavailable (local Ollama at %s not reachable) — the audit report stands on its own\n' "$OLLAMA_URL" >&2
    fi
  else
    printf 'WARNING: curl unavailable — AI triage skipped (the audit report stands on its own)\n' >&2
  fi
fi

[ "$FINDINGS" -gt 0 ] && exit 1
exit 0
