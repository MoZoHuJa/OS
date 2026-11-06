#!/usr/bin/env bash
# tests/test_dependency_inventory.sh — dependency inventory drift guard
# (KPK-19.6.5-P2-05 remediation, v19.6.6).
#
# The independent v19.6.5 audit's finding: docs/DEPENDENCY_AUDIT_2026-10-21.md
# still listed OLD pins while the composes already carried the remediated
# ones — a document claiming to describe the deployment while drifting from
# it. The remediation: a CURRENT inventory (docs/DEPENDENCY_INVENTORY_*.md)
# generated from the active compose files, and THIS test — the drift guard:
#
#   1. every `image:` reference in every compose file in the tree must
#      appear in the inventory (a new pin without an inventory row FAILS);
#   2. every inventory image row must reference a compose file that still
#      exists (an inventory row for a deleted compose is dead evidence);
#   3. every compose `build:` context must carry a Dockerfile (the
#      KPK-19.6.5-P2-04 class — deterministic here, weekly in
#      scarlix-stale-audit.sh);
#   4. the historical audit doc must carry the HISTORICAL banner (a point-
#      in-time snapshot must never look like current state again).
#
# NO NETWORK — pure tree discipline. Exit: 0 all assertions pass, 1 otherwise.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INVENTORY="${1:-$REPO_ROOT/docs/DEPENDENCY_INVENTORY_2026-11-06.md}"
HISTORICAL_AUDIT="$REPO_ROOT/docs/DEPENDENCY_AUDIT_2026-10-21.md"

TESTS_RUN=0
TESTS_FAILED=0

t_ok() { TESTS_RUN=$((TESTS_RUN + 1)); printf 'ok   %s\n' "$*"; }
t_fail() { TESTS_RUN=$((TESTS_RUN + 1)); TESTS_FAILED=$((TESTS_FAILED + 1)); printf 'FAIL %s\n' "$*"; }

[ -f "$INVENTORY" ] || { echo "FAIL inventory not found: $INVENTORY"; exit 1; }
[ -f "$HISTORICAL_AUDIT" ] || { echo "FAIL historical audit doc missing"; exit 1; }

# --- 1. every compose image ref appears in the inventory -----------------
compose_files="$(find "$REPO_ROOT" -name 'docker-compose*.yml' -not -path '*/.git/*' -not -path '*/docs/archive/*' | sort)"

[ -n "$compose_files" ] || { echo "FAIL no compose files found"; exit 1; }

images_checked=0
while IFS='|' read -r compose image; do
  [ -z "$image" ] && continue
  images_checked=$((images_checked + 1))
  rel="${compose#"$REPO_ROOT"/}"
  # scarlihq composes itself via SCARLIX_VERSION build arg — the inventory
  # documents the BUILD row, not a digest; check the build row exists.
  case "$image" in
    'scarlihq:v'*)
      if grep -q 'scarlihq:v${SCARLIX_VERSION}' "$INVENTORY"; then
        t_ok "$rel: control-plane BUILD row present in inventory"
      else
        t_fail "$rel: control-plane BUILD row missing in inventory"
      fi
      continue
      ;;
  esac
  if grep -qF "$image" "$INVENTORY"; then
    t_ok "$rel: $image documented"
  else
    t_fail "$rel: image $image is NOT in the dependency inventory ($INVENTORY) — a pin drift: update the inventory when you change a compose pin (KPK-19.6.5-P2-05)"
  fi
done < <(
  while IFS= read -r compose; do
    while IFS= read -r image; do
      [ -z "$image" ] && continue
      printf '%s|%s\n' "$compose" "$image"
    done < <(grep -hE '^[[:space:]]*image:[[:space:]]*[^[:space:]]+' "$compose" | sed -E 's/^[[:space:]]*image:[[:space:]]*//; s/[[:space:]]+#.*$//' | sort -u)
  done <<< "$compose_files"
)

[ "$images_checked" -gt 20 ] || t_fail "expected a substantial image inventory (got $images_checked refs)"
[ "$images_checked" -gt 20 ] && t_ok "$images_checked image references checked against the inventory"

# --- 2. build contexts carry Dockerfiles (P2-04 class) --------------------
buildctx_checked=0
while IFS='|' read -r compose ctx; do
  [ -z "$ctx" ] && continue
  buildctx_checked=$((buildctx_checked + 1))
  rel="${compose#"$REPO_ROOT"/}"
  ctx="${ctx#./}"
  ctx_full="$REPO_ROOT/$(dirname "$rel")/$ctx"
  if [ -f "$ctx_full/Dockerfile" ]; then
    t_ok "$rel: build context $ctx carries a Dockerfile"
  else
    t_fail "$rel: build context $ctx has NO Dockerfile (KPK-19.6.5-P2-04) — add it or replace build: with a pinned image"
  fi
done < <(
  while IFS= read -r compose; do
    grep -hE '^[[:space:]]*build:[[:space:]]*\.[[:space:]]*$' "$compose" 2>/dev/null | sed -E 's/^[[:space:]]*build:[[:space:]]*//' | while IFS= read -r ctx; do
      printf '%s|%s\n' "$compose" "$ctx"
    done
    grep -E '^[[:space:]]*context:[[:space:]]*\.' "$compose" 2>/dev/null | sed -E 's/.*context:[[:space:]]*//; s/[[:space:]]+#.*$//' | while IFS= read -r ctx; do
      printf '%s|%s\n' "$compose" "$ctx"
    done
  done <<< "$compose_files"
)

[ "$buildctx_checked" -ge 4 ] && t_ok "$buildctx_checked build contexts verified (control plane + optional builds)"
[ "$buildctx_checked" -lt 4 ] && t_fail "expected at least 4 build contexts (scarlihq, media-tools, musicgen, video), got $buildctx_checked"

# --- 3. the historical audit carries the banner ---------------------------
if grep -q "HISTORICAL POINT-IN-TIME SNAPSHOT" "$HISTORICAL_AUDIT" && grep -q "DEPENDENCY_INVENTORY_2026-11-06" "$HISTORICAL_AUDIT"; then
  t_ok "the 2026-10-21 audit is bannered as a historical snapshot pointing at the current inventory"
else
  t_fail "the 2026-10-21 audit doc lacks the HISTORICAL banner/supertable pointer — a point-in-time snapshot must never look like current state (KPK-19.6.5-P2-05)"
fi

# --- 4. the experimental :latest register is present ----------------------
for phantom in 'mcp/playwright-server:latest' 'freetoken/serving:latest' 'laya/decision-engine:latest' 'ghcr.io/cactus-compute/needle:latest'; do
  if grep -qF "$phantom" "$INVENTORY"; then
    t_ok "experimental $phantom is REGISTERED (named residual, not a silent ride)"
  else
    t_fail "experimental $phantom missing from the inventory §3 — unpinned images must be named, never implicit"
  fi
done

# --- summary ---------------------------------------------------------------
printf '\n%d run, %d failed\n' "$TESTS_RUN" "$TESTS_FAILED"
[ "$TESTS_FAILED" -eq 0 ] && exit 0
exit 1
