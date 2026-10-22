#!/usr/bin/env bash
# tests/test_registry_classification.sh — deterministic registry-classification
# tests for scarlix-smoke-test.sh (KPK-01/KPK-02 remediation, v19.4.3).
#
# NO NETWORK ACCESS NEEDED: the registry transport seam
# (registry_token / registry_manifest_status) is overridden with mocked
# responses covering the full classification table:
#
#   manifest HTTP 200            -> VERIFIED_EXISTS  (PASS event)
#   manifest HTTP 404            -> MISSING          (FAIL event)
#   manifest 401/403/429/5xx/000 -> UNVERIFIED       (WARN event)
#   transport errors (28/6)      -> UNVERIFIED       (WARN event)
#   auth: non-200 / malformed JSON / empty token     -> UNVERIFIED (WARN)
#
# Acceptance criteria asserted (KpK review, finding KPK-02):
#   - CI output distinguishes EXISTS / MISSING / UNVERIFIED.
#   - An UNVERIFIED tag is NEVER reported as confirmed to exist.
#   - Transient failures do not create flaky failures (WARN, not FAIL).
#   - Only a definitive 404 is MISSING/FAIL.
#
# Also validates the KPK-04 check-inventory guard:
#   scheduled SMOKE_CHECKS == defined check_* functions == executed inventory.
#
# Usage: bash tests/test_registry_classification.sh   (from repo root)
# Exit:  0 all assertions pass, 1 otherwise.

set -uo pipefail

TESTS_RUN=0
TESTS_FAILED=0

t_ok() {
    TESTS_RUN=$((TESTS_RUN + 1))
    printf 'ok   %s\n' "$*"
}

t_fail() {
    TESTS_RUN=$((TESTS_RUN + 1))
    TESTS_FAILED=$((TESTS_FAILED + 1))
    printf 'FAIL %s\n' "$*"
}

assert_eq() {
    # $1 actual, $2 expected, $3 label
    if [[ "$1" == "$2" ]]; then
        t_ok "$3"
    else
        t_fail "$3 — expected '$2', got '$1'"
    fi
}

assert_contains() {
    # $1 haystack, $2 needle, $3 label
    if [[ "$1" == *"$2"* ]]; then
        t_ok "$3"
    else
        t_fail "$3 — missing '$2' in: $1"
    fi
}

assert_not_contains() {
    # $1 haystack, $2 forbidden needle, $3 label
    if [[ "$1" != *"$2"* ]]; then
        t_ok "$3"
    else
        t_fail "$3 — '$2' must NOT appear in: $1"
    fi
}

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

echo "=== registry-classification tests (deterministic, mocked transport) ==="
echo "repo: $REPO_ROOT"
echo

# Source the smoke script — the sourcing guard prevents suite execution.
# shellcheck source=../files/usr/local/bin/scarlix-smoke-test.sh
source files/usr/local/bin/scarlix-smoke-test.sh
set +e  # the smoke script enables -e; this harness asserts explicitly

# ---------------------------------------------------------------------------
# Mocked transport seam (overrides the real curl-backed functions)
# ---------------------------------------------------------------------------
MOCK_TOKEN_OUT="mock-token"
MOCK_TOKEN_EXIT=0
MOCK_STATUS="200"
MOCK_STATUS_EXIT=0

registry_token() {
    printf '%s' "$MOCK_TOKEN_OUT"
    return "$MOCK_TOKEN_EXIT"
}

registry_manifest_status() {
    printf '%s' "$MOCK_STATUS"
    return "$MOCK_STATUS_EXIT"
}

# ---------------------------------------------------------------------------
# 1) Unit tests: registry_classify — the pure classification table
# ---------------------------------------------------------------------------
echo "--- registry_classify (pure function) ---"
assert_eq "$(registry_classify 0 200)" "VERIFIED_EXISTS" "classify(0,200)   = VERIFIED_EXISTS"
assert_eq "$(registry_classify 0 404)" "MISSING" "classify(0,404)   = MISSING"
for s in 401 403 429 500 503 000 302 999; do
    assert_eq "$(registry_classify 0 "$s")" "UNVERIFIED" "classify(0,$s)  = UNVERIFIED"
done
assert_eq "$(registry_classify 0 "")" "UNVERIFIED" "classify(0,empty) = UNVERIFIED"
# Transport exit code wins — a timeout/DNS failure can never confirm NOR deny.
assert_eq "$(registry_classify 28 200)" "UNVERIFIED" "classify(28,200)  = UNVERIFIED (timeout wins over status)"
assert_eq "$(registry_classify 6 404)" "UNVERIFIED" "classify(6,404)   = UNVERIFIED (DNS error wins over status)"
assert_eq "$(registry_classify 60 200)" "UNVERIFIED" "classify(60,200)  = UNVERIFIED (TLS error wins)"

# ---------------------------------------------------------------------------
# 2) Probe scenarios with the mocked transport (no network)
# ---------------------------------------------------------------------------
probe_scenario() {
    # $1 = description, $2 = expected PASS:FAIL:WARN counters,
    # $3 = verdict label that MUST appear in the output
    #
    # Uses the PROBE_REPO / PROBE_TAG / PROBE_DIGEST globals (set below) so
    # both tag-only and digest pin scenarios go through the same runner.
    #
    # NOTE: the check is invoked with stdout redirected to a temp file (NOT via
    # command substitution) so the PASS/FAIL/WARN counters — which the pass/
    # fail/warn helpers increment as globals — update in THIS shell.
    local desc="$1" want_events="$2" want_label="$3" out tmpf
    tmpf="$(mktemp)"
    PASS_COUNT=0
    FAIL_COUNT=0
    WARN_COUNT=0
    check_image_dockerhub "$PROBE_REPO" "$PROBE_TAG" "$PROBE_DIGEST" >"$tmpf" 2>&1
    out="$(cat "$tmpf")"
    rm -f "$tmpf"
    assert_contains "$out" "$want_label" "$desc: verdict label"
    assert_eq "$PASS_COUNT:$FAIL_COUNT:$WARN_COUNT" "$want_events" "$desc: event counters"
    if [[ "$want_label" == "UNVERIFIED" ]]; then
        # KPK-02 acceptance: an unverified tag is NEVER reported as existing.
        assert_not_contains "$out" "VERIFIED_EXISTS" "$desc: never claims existence"
    fi
}

echo
echo "--- probe scenarios (mocked Docker Hub registry) ---"
MOCK_TOKEN_OUT="mock-token"
MOCK_TOKEN_EXIT=0
MOCK_STATUS_EXIT=0
PROBE_REPO="acme/widget"
PROBE_TAG="v1"
PROBE_DIGEST=""

MOCK_STATUS="200"
probe_scenario "manifest 200"            "1:0:0" "VERIFIED_EXISTS"
MOCK_STATUS="404"
probe_scenario "manifest 404 (dead pin)" "0:1:0" "MISSING"
for s in 401 403 429 500 503 000; do
    MOCK_STATUS="$s"
    probe_scenario "manifest $s" "0:0:1" "UNVERIFIED"
done

# Transport failures: timeout (curl exit 28), DNS failure (exit 6)
MOCK_STATUS="000"
MOCK_STATUS_EXIT=28
probe_scenario "manifest timeout (curl 28)"   "0:0:1" "UNVERIFIED"
MOCK_STATUS_EXIT=6
probe_scenario "manifest DNS failure (curl 6)" "0:0:1" "UNVERIFIED"
MOCK_STATUS_EXIT=0

# Auth-side failures — each reason must surface in the WARN trail.
MOCK_TOKEN_EXIT=1
MOCK_TOKEN_OUT="ERROR:auth-transport-failure"
probe_scenario "auth transport failure" "0:0:1" "UNVERIFIED (auth: ERROR:auth-transport-failure)"
MOCK_TOKEN_OUT="ERROR:auth-endpoint-HTTP-403"
probe_scenario "auth endpoint 403" "0:0:1" "UNVERIFIED (auth: ERROR:auth-endpoint-HTTP-403)"
MOCK_TOKEN_OUT="ERROR:auth-endpoint-HTTP-429"
probe_scenario "auth endpoint 429" "0:0:1" "UNVERIFIED (auth: ERROR:auth-endpoint-HTTP-429)"
MOCK_TOKEN_OUT="ERROR:malformed-auth-json"
probe_scenario "auth malformed JSON" "0:0:1" "UNVERIFIED (auth: ERROR:malformed-auth-json)"
MOCK_TOKEN_OUT="ERROR:empty-token"
probe_scenario "auth empty token" "0:0:1" "UNVERIFIED (auth: ERROR:empty-token)"
MOCK_TOKEN_OUT="mock-token"
MOCK_TOKEN_EXIT=0

# ghcr.io wrapper gets the same classification path.
MOCK_STATUS="200"
ghcr_out=$(check_image_ghcr "acme/widget" "v1" 2>&1)
assert_contains "$ghcr_out" "ghcr.io" "ghcr wrapper keeps registry label"
assert_contains "$ghcr_out" "VERIFIED_EXISTS" "ghcr wrapper classifies 200 as VERIFIED_EXISTS"

# v19.4.3 digest-aware probes: pins written tag@sha256 are pulled BY DIGEST —
# the probe must target the digest endpoint and report repo@digest.
echo
echo "--- digest-aware probe scenarios (tag@digest pins) ---"
PROBE_TAG="master-ubuntu-24.04"
PROBE_DIGEST="sha256:5c790c79df9b680a1c57e3454ed829c09f405eafbafe205675d832e4420aba7b"
MOCK_STATUS_EXIT=0

MOCK_STATUS="200"
digest_out="$(check_image_dockerhub "$PROBE_REPO" "$PROBE_TAG" "$PROBE_DIGEST" 2>&1)"
assert_contains "$digest_out" "acme/widget@sha256:5c790c79" "digest probe reports repo@digest reference"
assert_contains "$digest_out" "VERIFIED_EXISTS" "digest probe: live digest = VERIFIED_EXISTS"

MOCK_STATUS="404"
digest_out="$(check_image_dockerhub "$PROBE_REPO" "$PROBE_TAG" "$PROBE_DIGEST" 2>&1)"
assert_contains "$digest_out" "MISSING" "digest probe: dead digest = MISSING"

MOCK_STATUS="429"
digest_out="$(check_image_dockerhub "$PROBE_REPO" "$PROBE_TAG" "$PROBE_DIGEST" 2>&1)"
assert_contains "$digest_out" "UNVERIFIED" "digest probe: rate-limited = UNVERIFIED"
assert_not_contains "$digest_out" "VERIFIED_EXISTS" "digest probe: rate-limited never claims existence"

# Digest-only pins (no tag) are valid — reference is repo@digest alone.
MOCK_STATUS="200"
digest_only_out="$(check_image_ghcr "acme/widget" "" "$PROBE_DIGEST" 2>&1)"
assert_contains "$digest_only_out" "acme/widget@sha256:5c790c79" "digest-only pin reports repo@digest"
assert_contains "$digest_only_out" "VERIFIED_EXISTS" "digest-only pin: live digest = VERIFIED_EXISTS"

PROBE_TAG="v1"
PROBE_DIGEST=""

# ---------------------------------------------------------------------------
# 3) KPK-04: check-inventory guard
# ---------------------------------------------------------------------------
echo
echo "--- check inventory guard (KPK-04) ---"

PASS_COUNT=0
FAIL_COUNT=0
WARN_COUNT=0
if verify_check_inventory; then
    t_ok "inventory guard: default schedule is consistent"
else
    t_fail "inventory guard: default schedule must be consistent"
fi
assert_eq "$FAIL_COUNT" "0" "inventory guard: no FAIL events on consistent schedule"

# Negative: a defined check_* function that is NOT scheduled must be detected.
check_99_stray() { :; }
PASS_COUNT=0
FAIL_COUNT=0
WARN_COUNT=0
if verify_check_inventory; then
    t_fail "inventory guard: stray defined check must be detected"
else
    t_ok "inventory guard: stray defined check detected"
fi
assert_eq "$FAIL_COUNT" "1" "inventory guard: drift emits exactly one FAIL event"
unset -f check_99_stray

# Negative: a scheduled entry that is NOT defined must be detected.
SMOKE_CHECKS_BACKUP=("${SMOKE_CHECKS[@]}")
SMOKE_CHECKS+=("check_98_undefined")
PASS_COUNT=0
FAIL_COUNT=0
WARN_COUNT=0
if verify_check_inventory; then
    t_fail "inventory guard: undefined scheduled check must be detected"
else
    t_ok "inventory guard: undefined scheduled check detected"
fi
assert_eq "$FAIL_COUNT" "1" "inventory guard: undefined entry emits exactly one FAIL event"
SMOKE_CHECKS=("${SMOKE_CHECKS_BACKUP[@]}")
unset SMOKE_CHECKS_BACKUP

# run_check increments the executed-check counter (summary consistency).
CHECKS_EXECUTED=0
t_dummy_check() { :; }
run_check t_dummy_check
run_check t_dummy_check
assert_eq "$CHECKS_EXECUTED" "2" "run_check increments CHECKS_EXECUTED"
unset -f t_dummy_check

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo
if [[ $TESTS_FAILED -eq 0 ]]; then
    printf '=== registry-classification tests: %d run, 0 failed ===\n' "$TESTS_RUN"
    exit 0
fi
printf '=== registry-classification tests: %d run, %d FAILED ===\n' "$TESTS_RUN" "$TESTS_FAILED"
exit 1
