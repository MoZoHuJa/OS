#!/usr/bin/env bash
# SCARLIX OS regression smoke test (version read from VERSION)
#
# Runs the integrity check inventory (check_01..check_NN). v19.4.3 (KPK-04):
# the schedule is verified against the defined check_* functions before
# execution and the summary reports the number of checks actually executed —
# no hardcoded check count. Prints a PASS/FAIL/WARN event line per result;
# one check may emit several events (check_07 probes every pinned image tag).
# Exits 0 if no check FAILed, 1 otherwise. WARNs (missing tool, network
# error, UNVERIFIED registry responses) do NOT fail the run.
#
# Usage:
#   scarlix-smoke-test.sh [--offline] [--help]
#
# Env:
#   SCARLIX_REPO  Override repo root (default: current working directory).
#
# Run from the repo root, e.g.:
#   bash files/usr/local/bin/scarlix-smoke-test.sh
#   bash files/usr/local/bin/scarlix-smoke-test.sh --offline

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
REPO_DIR="${SCARLIX_REPO:-$(pwd)}"
OFFLINE=0

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

PASS_COUNT=0
FAIL_COUNT=0
WARN_COUNT=0
CHECKS_EXECUTED=0

# ---------------------------------------------------------------------------
# v19.4.3 (KPK-02/KPK-04): sourcing seam — tests/test_registry_classification.sh
# sources this file to unit-test the registry classification table and the
# check-inventory guard WITHOUT executing the suite. Everything with a side
# effect (argument parsing, preamble, check execution, summary) is guarded.
# ---------------------------------------------------------------------------
SCARLIX_SMOKE_SOURCED=0
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
    SCARLIX_SMOKE_SOURCED=1
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
usage() {
    cat <<'EOF'
scarlix-smoke-test.sh — SCARLIX OS regression smoke test

Usage:
  scarlix-smoke-test.sh [--offline] [--help]

Options:
  --offline   Skip all Docker Hub / ghcr.io image-tag registry checks.
              Use in CI environments without outbound network access.
  --help, -h   Show this help and exit.

Environment:
  SCARLIX_REPO   Override repo root directory (default: current dir).

Exit status:
  0   All checks PASSed (WARNs are tolerated).
  1   At least one check FAILed (or the check inventory is inconsistent).
  2   Invalid CLI usage.

The check inventory (SMOKE_CHECKS) is verified against the defined check_*
functions before execution; the summary reports the executed count. Do not
hardcode the count in documentation — read it from the summary output.
EOF
}

pass() {
    # shellcheck disable=SC2059  # color escapes use %b semantics intentionally
    printf "${GREEN}PASS${NC} | %s\n" "$*"
    PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
    # shellcheck disable=SC2059
    printf "${RED}FAIL${NC} | %s\n" "$*"
    FAIL_COUNT=$((FAIL_COUNT + 1))
}

warn() {
    # shellcheck disable=SC2059
    printf "${YELLOW}WARN${NC} | %s\n" "$*"
    WARN_COUNT=$((WARN_COUNT + 1))
}

has_tool() {
    command -v "$1" >/dev/null 2>&1
}

# ---------------------------------------------------------------------------
# Argument parsing (skipped when sourced by the test suite)
# ---------------------------------------------------------------------------
if [[ $SCARLIX_SMOKE_SOURCED -eq 0 ]]; then
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --offline)
                OFFLINE=1
                shift
                ;;
            --help|-h)
                usage
                exit 0
                ;;
            *)
                echo "scarlix-smoke-test: unknown option: $1" >&2
                usage >&2
                exit 2
                ;;
        esac
    done
fi

# ---------------------------------------------------------------------------
# Preamble (skipped when sourced by the test suite)
# ---------------------------------------------------------------------------
if [[ $SCARLIX_SMOKE_SOURCED -eq 0 ]]; then
    SMOKE_VERSION="$(cat "$REPO_DIR/VERSION" 2>/dev/null || printf 'unknown')"
    # shellcheck disable=SC2059
    printf "${BOLD}=== SCARLIX OS v%s regression smoke test ===${NC}\n" "$SMOKE_VERSION"
    printf "Repo:   %s\n" "$REPO_DIR"
    printf "Offline: %s\n" "$([[ $OFFLINE -eq 1 ]] && echo yes || echo no)"
    echo
fi

if [[ $SCARLIX_SMOKE_SOURCED -eq 0 ]]; then
    if [[ ! -d "$REPO_DIR" ]]; then
        echo "scarlix-smoke-test: REPO_DIR does not exist: $REPO_DIR" >&2
        exit 2
    fi
    if [[ ! -f "$REPO_DIR/VERSION" ]] || [[ ! -f "$REPO_DIR/install.sh" ]]; then
        echo "scarlix-smoke-test: $REPO_DIR does not look like the SCARLIX OS repo" >&2
        echo "  (expected: VERSION + install.sh at repo root)" >&2
        exit 2
    fi
fi

# ===========================================================================
# Check 1 — bash -n syntax check on all shell scripts
# ===========================================================================
check_01_bash_syntax() {
    echo "--- [01] bash -n syntax check on all shell scripts ---"
    local scripts=() f errors=0 out
    [[ -f "$REPO_DIR/install.sh" ]] && scripts+=("$REPO_DIR/install.sh")
    shopt -s nullglob
    for f in "$REPO_DIR"/files/usr/local/bin/*; do
        scripts+=("$f")
    done
    for f in "$REPO_DIR"/files/etc/systemd/system/*.sh; do
        scripts+=("$f")
    done
    for f in "$REPO_DIR"/files/etc/pacman.d/hooks/*.sh; do
        scripts+=("$f")
    done
    shopt -u nullglob
    if [[ ${#scripts[@]} -eq 0 ]]; then
        fail "no shell scripts found to check"
        return
    fi
    for f in "${scripts[@]}"; do
        if ! out=$(bash -n "$f" 2>&1); then
            fail "syntax error in ${f#$REPO_DIR/}: ${out//$'\n'/ }"
            errors=$((errors + 1))
        fi
    done
    if [[ $errors -eq 0 ]]; then
        pass "bash -n clean on ${#scripts[@]} shell script(s)"
    fi
}

# ===========================================================================
# Check 2 — shellcheck -S warning on the 10 CI-checked scripts
# ===========================================================================
check_02_shellcheck() {
    echo "--- [02] shellcheck -S warning on CI-checked scripts ---"
    local scripts=(
        "install.sh"
        "files/usr/local/bin/scarlix-mode"
        "files/usr/local/bin/scarlix-wizard"
        "files/usr/local/bin/scarlix-doctor"
        "files/usr/local/bin/model-manager.sh"
        "files/usr/local/bin/download-models.sh"
        "files/usr/local/bin/scarlix-host-bridge"
        "files/usr/local/bin/generate-sha256sums.sh"
        "files/usr/local/bin/generate-litellm-config.sh"
        "files/etc/systemd/system/generate-env.sh"
    )
    if ! has_tool shellcheck; then
        warn "shellcheck not installed — skipping static analysis of ${#scripts[@]} script(s)"
        return
    fi
    local missing=0 s errors=0 out
    for s in "${scripts[@]}"; do
        if [[ ! -f "$REPO_DIR/$s" ]]; then
            fail "missing script: $s"
            missing=$((missing + 1))
        fi
    done
    [[ $missing -gt 0 ]] && return
    for s in "${scripts[@]}"; do
        if ! out=$(shellcheck -S warning "$REPO_DIR/$s" 2>&1); then
            fail "shellcheck warning(s) in $s:"
            printf "       %s\n" "${out//$'\n'/$'\n       '}"
            errors=$((errors + 1))
        fi
    done
    if [[ $errors -eq 0 ]]; then
        pass "shellcheck -S warning clean on ${#scripts[@]} script(s)"
    fi
}

# ===========================================================================
# Check 3 — YAML validation (python3 + yaml.safe_load)
# ===========================================================================
check_03_yaml() {
    echo "--- [03] YAML validation on docker-compose.yml + ai/smg/config.yaml ---"
    if ! has_tool python3; then
        warn "python3 not installed — skipping YAML validation"
        return
    fi
    if ! python3 -c 'import yaml' 2>/dev/null; then
        warn "python3 'yaml' module not installed (pip install pyyaml) — skipping YAML validation"
        return
    fi
    local files=() f
    shopt -s nullglob
    while IFS= read -r f; do
        files+=("$f")
    done < <(find "$REPO_DIR" -name 'docker-compose.yml' -not -path '*/.git/*')
    shopt -u nullglob
    if [[ -f "$REPO_DIR/ai/smg/config.yaml" ]]; then
        files+=("$REPO_DIR/ai/smg/config.yaml")
    fi
    if [[ ${#files[@]} -eq 0 ]]; then
        fail "no YAML files found to validate"
        return
    fi
    local errors=0 f_rel out
    for f in "${files[@]}"; do
        f_rel="${f#$REPO_DIR/}"
        if ! out=$(python3 -c 'import sys, yaml; yaml.safe_load(sys.stdin)' < "$f" 2>&1); then
            fail "YAML parse error in $f_rel: ${out//$'\n'/ }"
            errors=$((errors + 1))
        fi
    done
    if [[ $errors -eq 0 ]]; then
        pass "YAML valid on ${#files[@]} file(s)"
    fi
}

# ===========================================================================
# Check 4 — systemd unit validation
# ===========================================================================
check_04_systemd() {
    echo "--- [04] systemd-analyze verify on *.service + *.timer ---"
    if ! has_tool systemd-analyze; then
        warn "systemd-analyze not available — skipping unit verification"
        return
    fi
    local units=() u
    shopt -s nullglob
    for u in "$REPO_DIR"/files/etc/systemd/system/*.service \
             "$REPO_DIR"/files/etc/systemd/system/*.timer; do
        units+=("$u")
    done
    shopt -u nullglob
    if [[ ${#units[@]} -eq 0 ]]; then
        warn "no systemd unit files found under files/etc/systemd/system/"
        return
    fi
    # v19.3.1: deterministic dependency resolution. scarlix-workload-bridge
    # requires docker.service — present on real hosts (install.sh Phase 3) and
    # on CI runners, but NOT in minimal dev sandboxes, which made verify fail
    # on environment, not on the repo. SYSTEMD_UNIT_PATH with a trailing colon
    # appends the default search paths, so ONLY docker.service is stubbed —
    # every other missing dependency still fails the check.
    local stubdir
    stubdir=$(mktemp -d)
    printf '%s\n' \
        '[Unit]' \
        'Description=Docker Engine stub for static unit verification only' \
        '[Service]' \
        'ExecStart=/bin/true' \
        'Type=oneshot' > "$stubdir/docker.service"
    # Filter dev-box noise: "marked executable" / "not marked executable" /
    # "chmod" hints. Remaining output indicates real unit errors.
    # v19.6.0 post-release (the v19.3.2 flake precedent): also filter
    # option-deprecation WARNINGS about units OUTSIDE the repo — the
    # ubuntu-latest runner image ships its own system units (xfs_scrub_all,
    # walinuxagent…) that newer systemd flags with "Support for option
    # CPUAccounting= has been removed". Those units are the runner's, not
    # the repo's deliverable; a deprecation warning in a REPO unit still
    # FAILs (the pattern anchors on /usr/lib//lib system paths only).
    local raw filtered
    raw=$(SYSTEMD_UNIT_PATH="$stubdir:" systemd-analyze verify "${units[@]}" 2>&1 || true)
    rm -rf "$stubdir"
    filtered=$(printf '%s\n' "$raw" \
        | grep -vE 'executable|chmod|Empty|cached' \
        | grep -vE '^/(usr/)?lib/systemd/system/.*: Support for option .* has been removed and it is ignored' \
        || true)
    if [[ -z "$filtered" ]]; then
        pass "systemd-analyze verify clean on ${#units[@]} unit file(s)"
    else
        fail "systemd-analyze verify reported issues:"
        printf "       %s\n" "${filtered//$'\n'/$'\n       '}"
    fi
}

# ===========================================================================
# Check 5 — VERSION consistency across VERSION / install.sh / Dockerfile / README
# ===========================================================================
check_05_version() {
    echo "--- [05] VERSION consistency ---"
    local ver errors=0
    ver=$(cat "$REPO_DIR/VERSION")
    if [[ -z "$ver" ]]; then
        fail "VERSION file is empty"
        return
    fi
    # install.sh: VERSION="<ver>"
    if ! grep -qE "^VERSION=\"${ver}\"$" "$REPO_DIR/install.sh"; then
        fail "install.sh VERSION= does not match VERSION file ($ver)"
        errors=$((errors + 1))
    fi
    # scarlihq/Dockerfile: ARG SCARLIX_VERSION=<ver>
    if ! grep -qE "^ARG SCARLIX_VERSION=${ver}$" "$REPO_DIR/scarlihq/Dockerfile"; then
        fail "scarlihq/Dockerfile ARG SCARLIX_VERSION= does not match ($ver)"
        errors=$((errors + 1))
    fi
    # README.md mentions the version
    if ! grep -q -- "$ver" "$REPO_DIR/README.md"; then
        fail "README.md does not mention version $ver"
        errors=$((errors + 1))
    fi
    if [[ $errors -eq 0 ]]; then
        pass "VERSION $ver consistent across VERSION / install.sh / Dockerfile / README.md"
    fi
}

# ===========================================================================
# Check 6 — Go module path (must be the post-v19.0.3 path, not the v12 one)
# ===========================================================================
check_06_go_module() {
    echo "--- [06] Go module path ---"
    local gomod="$REPO_DIR/scarlihq/go.mod"
    if [[ ! -f "$gomod" ]]; then
        fail "scarlihq/go.mod not found"
        return
    fi
    local line1
    line1=$(sed -n '1p' "$gomod")
    if [[ "$line1" == "module github.com/MoZoHuJa/OS/scarlihq" ]]; then
        pass "go.mod module path is github.com/MoZoHuJa/OS/scarlihq"
    elif [[ "$line1" == *"scarlix-os-v12"* ]]; then
        fail "go.mod has stale v12 module path: $line1"
    else
        fail "go.mod module path unexpected: $line1 (expected: module github.com/MoZoHuJa/OS/scarlihq)"
    fi
}

# ===========================================================================
# Check 7 — Image tag existence via registry API (Docker Hub + ghcr.io)
# ===========================================================================
# v19.4.3 (KPK-02): every probe resolves to exactly one verdict —
#   VERIFIED_EXISTS  HTTP 200 from the manifest endpoint (authoritative answer)
#   MISSING          HTTP 404 — the pin is dead; a clean deploy would fail
#   UNVERIFIED       everything else: transport errors (timeout/DNS/TLS),
#                    HTTP 401/403/429/5xx, malformed or empty auth JSON,
#                    unknown statuses — NEVER reported as existing.
# Only MISSING fails CI (definitive answer); UNVERIFIED is a non-blocking
# WARN so registry flakiness cannot break CI spuriously (KPK-01 acceptance:
# transient failures non-blocking; auth/rate-limit failures never silently
# represented as a verified existing tag).
# v19.4.3 (live finding, sunshine): pins written as tag@sha256 digest are
# pulled BY DIGEST — the tag name is cosmetic for the deploy. Probes are
# digest-aware: a pruned tag with a live digest is VERIFIED_EXISTS (the
# deploy works); a dead digest is MISSING (the real deploy breaker).
# registry_token / registry_manifest_status are the transport seam — the
# deterministic suite (tests/test_registry_classification.sh) overrides them
# with mocked responses; no network needed there.

registry_token() {
    # Fetch an anonymous pull token. Prints the token on stdout (exit 0), or
    # "ERROR:<reason>" (exit 1) — the reason becomes part of the WARN trail.
    local url="$1" out status body token
    if ! out=$(curl -sSL --max-time 15 -w $'\n%{http_code}' "$url" 2>/dev/null); then
        printf 'ERROR:auth-transport-failure'
        return 1
    fi
    status="${out##*$'\n'}"
    body="${out%$'\n'*}"
    if [[ "$status" != "200" ]]; then
        printf 'ERROR:auth-endpoint-HTTP-%s' "$status"
        return 1
    fi
    if ! token=$(printf '%s' "$body" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))' 2>/dev/null); then
        printf 'ERROR:malformed-auth-json'
        return 1
    fi
    if [[ -z "$token" ]]; then
        printf 'ERROR:empty-token'
        return 1
    fi
    printf '%s' "$token"
}

registry_manifest_status() {
    # Manifest probe. Prints the HTTP status ("000" when no HTTP response
    # arrived). Exit code = curl transport exit (0 = authoritative answer).
    local url="$1" auth="$2" status rc
    rc=0
    status=$(curl -sSL -o /dev/null --max-time 20 -w "%{http_code}" \
        -H "Authorization: Bearer $auth" \
        -H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json" \
        "$url" 2>/dev/null) || rc=$?
    : "${status:=000}"
    printf '%s' "$status"
    return "$rc"
}

registry_classify() {
    # $1 = transport exit code, $2 = HTTP status. The exit code wins: a
    # timeout/DNS failure can never confirm NOR deny existence (fail-closed
    # against both false VERIFIED_EXISTS and false MISSING).
    local exit_code="$1" status
    status="${2:-000}"
    : "${status:=000}"
    if [[ "$exit_code" != "0" ]]; then
        printf 'UNVERIFIED'
        return
    fi
    case "$status" in
        200) printf 'VERIFIED_EXISTS' ;;
        404) printf 'MISSING' ;;
        *)   printf 'UNVERIFIED' ;;
    esac
}

probe_image_registry() {
    # $1 = label, $2 = auth url, $3 = manifest base url (no ref),
    # $4 = reference (repo:tag or repo@digest), $5 = ref to append
    local label="$1" auth_url="$2" manifest_base="$3" ref="$4" manifest_ref="$5"
    local token status rc verdict
    token=$(registry_token "$auth_url") || {
        warn "$label $ref -> UNVERIFIED (auth: $token)"
        return
    }
    rc=0
    status=$(registry_manifest_status "${manifest_base}${manifest_ref}" "$token") || rc=$?
    verdict=$(registry_classify "$rc" "$status")
    case "$verdict" in
        VERIFIED_EXISTS)
            pass "$label $ref -> VERIFIED_EXISTS (manifest HTTP $status)"
            ;;
        MISSING)
            fail "$label $ref -> MISSING (manifest HTTP $status — dead pin; a clean deploy would fail)"
            ;;
        *)
            warn "$label $ref -> UNVERIFIED (manifest HTTP $status — neither confirmed nor missing)"
            ;;
    esac
}

check_image_dockerhub() {
    # $1 = repo, $2 = tag (may be empty for digest-only pins), $3 = digest (optional)
    local repo="$1" tag="${2:-}" digest="${3:-}" ref manifest_ref
    if [[ -n "$digest" ]]; then
        ref="${repo}@${digest}"
        manifest_ref="${digest}"
    else
        ref="${repo}:${tag}"
        manifest_ref="${tag}"
    fi
    probe_image_registry "Docker Hub" \
        "https://auth.docker.io/token?service=registry.docker.io&scope=repository:${repo}:pull" \
        "https://registry-1.docker.io/v2/${repo}/manifests/" \
        "$ref" "$manifest_ref"
}

check_image_ghcr() {
    # $1 = repo, $2 = tag (may be empty for digest-only pins), $3 = digest (optional)
    local repo="$1" tag="${2:-}" digest="${3:-}" ref manifest_ref
    if [[ -n "$digest" ]]; then
        ref="${repo}@${digest}"
        manifest_ref="${digest}"
    else
        ref="${repo}:${tag}"
        manifest_ref="${tag}"
    fi
    probe_image_registry "ghcr.io" \
        "https://ghcr.io/token?service=ghcr.io&scope=repository:${repo}:pull" \
        "https://ghcr.io/v2/${repo}/manifests/" \
        "$ref" "$manifest_ref"
}

check_07_registry() {
    echo "--- [07] Image tag existence via registry API ---"
    if [[ $OFFLINE -eq 1 ]]; then
        warn "--offline set — skipping registry image-tag checks"
        return
    fi
    if ! has_tool curl || ! has_tool python3; then
        warn "curl and/or python3 unavailable — skipping registry image-tag checks"
        return
    fi
    # P0 detector (audit §6.1, v19.4.2; digest-aware v19.4.3).
    # Format: registry|repo|tag|digest — digest pins are probed BY DIGEST
    # (what a clean deploy actually pulls); tag-only pins by tag.
    # v19.4.3 (KPK-03): ghcr.io block/buzz REMOVED — the [buzz] profile is
    #   formally disabled (check_19); its dead minio/minio pin is quarantined
    #   in workspace/buzz/docker-compose.yml.disabled and is NOT deployable.
    # v19.4.3 (live catch): sunshine v2026.1020.123939 tag was pruned upstream
    #   within 2 days of v19.4.2; the pinned DIGEST is alive under the rolling
    #   master-ubuntu-24.04 tag — compose re-pinned to the rolling tag name +
    #   same digest (identical content), and the guard list below now covers
    #   EVERY digest-pinned image in active compose files.
    local -a pins=(
        "dockerhub|lmsysorg/sglang|v0.4.9.post6-cu128-b200|"
        "dockerhub|fedirz/faster-whisper-server|sha-307e23f-cuda|"
        "dockerhub|coollabsio/coolify|4.3.1|sha256:c054910edd0b4f56de17bacfbe2a501e06ccdfdb4df2e284ca09d49916c7cf32"
        "dockerhub|lizardbyte/sunshine|master-ubuntu-24.04|sha256:5c790c79df9b680a1c57e3454ed829c09f405eafbafe205675d832e4420aba7b"
        "dockerhub|crowdsecurity/crowdsec|v1.8.1|sha256:0f2523fa61ef507f15d953045cface490cc880670c62f2755ced17524107f71a"
        "dockerhub|ollama/ollama|0.40.7|sha256:2e6cc92ffa43b4b1a2e70e315407e00ac97ec125afc71277d73c7efe01c4f31b"
        "dockerhub|headscale/headscale|0.26.0|sha256:7e6439473c299cde05ff9215fe4e2ae9d83361504876fac0eb8e10f55128838d"
        "dockerhub|gitea/gitea|28.2|"
        "dockerhub|library/nextcloud|30|"
        "dockerhub|nousresearch/hermes-agent|v0.22.2|sha256:36099926da316742423544e055d4734777105ffe6341873af5459b957d0c2919"
        "ghcr|berriai/litellm|main-v1.23.9|"
        "ghcr|openlit/openlit|1.5.0|"
        "ghcr|ggml-org/llama.cpp||sha256:6d607629e3dd5e85f45c43d1494648126cb3f93f2122c9cd53f43242c94cde14"
        "ghcr|pumpkin-mc/pumpkin||sha256:61fd9494f96d58b46b14edd8336c1e5d9aa41f40e973ab3b61474e05e7feef39"
    )
    local p reg repo tag digest
    for p in "${pins[@]}"; do
        IFS='|' read -r reg repo tag digest <<<"$p"
        if [[ "$reg" == "dockerhub" ]]; then
            check_image_dockerhub "$repo" "$tag" "$digest"
        else
            check_image_ghcr "$repo" "$tag" "$digest"
        fi
    done
}

# ===========================================================================
# Check 8 — No 'ollama-main' in active (non-comment) smg config lines
# ===========================================================================
check_08_ollama_main() {
    echo "--- [08] No 'ollama-main' in active ai/smg/config.yaml lines ---"
    local cfg="$REPO_DIR/ai/smg/config.yaml"
    if [[ ! -f "$cfg" ]]; then
        fail "ai/smg/config.yaml not found"
        return
    fi
    # Strip comment-only lines, then look for the dead hostname token.
    local hits
    hits=$(grep -vE '^[[:space:]]*#' "$cfg" | grep -c 'ollama-main' || true)
    if [[ "$hits" -eq 0 ]]; then
        pass "no 'ollama-main' token in active config lines"
    else
        fail "found 'ollama-main' in $hits active (non-comment) line(s) of ai/smg/config.yaml"
    fi
}

# ===========================================================================
# Check 9 — No stale v12 doc headers in active docs/
# ===========================================================================
check_09_docs_v12() {
    echo "--- [09] No stale '# SCARLIX OS v12' headers in active docs/ ---"
    if [[ ! -d "$REPO_DIR/docs" ]]; then
        warn "docs/ directory not found — skipping v12 header check"
        return
    fi
    local hits
    hits=$(grep -rn '# SCARLIX OS v12' "$REPO_DIR/docs" --exclude-dir=archive 2>/dev/null | wc -l || true)
    if [[ "$hits" -eq 0 ]]; then
        pass "no '# SCARLIX OS v12' headers in active docs/ (archive/ excluded)"
    else
        fail "found $hits stale '# SCARLIX OS v12' header(s) in active docs/:"
        grep -rn '# SCARLIX OS v12' "$REPO_DIR/docs" --exclude-dir=archive || true \
            | sed 's/^/       /'
    fi
}

# ===========================================================================
# Check 10 — P0 regression: VLLM_TRUST_FLAG computed before heredoc in scarlix-mode
# v19.1.16 P0: VLLM_TRUST_FLAG was inside cat heredoc → literal text, flag always empty.
# This check ensures the fix is never reverted: the variable must be assigned BEFORE
# the heredoc that writes .env, and referenced as $vllm_trust_flag (not inline case/if).
# ===========================================================================
check_10_vllm_trust_p0() {
    echo "--- [10] P0 regression: VLLM_TRUST_FLAG is computed before and expanded in heredoc ---"
    local mode="$REPO_DIR/files/usr/local/bin/scarlix-mode"
    if [[ ! -f "$mode" ]]; then
        fail "scarlix-mode not found"
        return
    fi

    # Find the exact heredoc that writes the generated .env. The assignment
    # VLLM_TRUST_FLAG=... is EXPECTED inside this unquoted heredoc so Bash expands
    # the variable into the generated file. A grep-only test incorrectly treated
    # that line as a shell assignment and could not detect the original regression.
    local heredoc_start heredoc_end case_line value_line control_line
    heredoc_start=$(grep -nE 'cat > "\$env_tmp".*<<[[:space:]]*EOF[[:space:]]*$' "$mode" | head -1 | cut -d: -f1 || true)
    if [[ -z "$heredoc_start" ]]; then
        fail "scarlix-mode: .env heredoc not found"
        return
    fi
    heredoc_end=$(awk -v start="$heredoc_start" 'NR > start && $0 == "EOF" { print NR; exit }' "$mode")
    if [[ -z "$heredoc_end" ]]; then
        fail "scarlix-mode: .env heredoc has no closing EOF"
        return
    fi

    local decl_line
    decl_line=$(grep -nE '^[[:space:]]*local vllm_trust_flag=' "$mode" | head -1 | cut -d: -f1 || true)
    case_line=$(grep -nE 'true\|1\|yes\).*vllm_trust_flag="--trust-remote-code"' "$mode" | head -1 | cut -d: -f1 || true)
    value_line=$(grep -nE '^VLLM_TRUST_FLAG=\$vllm_trust_flag$' "$mode" | head -1 | cut -d: -f1 || true)

    if [[ -z "$decl_line" || -z "$case_line" || -z "$value_line" ]]; then
        fail "scarlix-mode: trust flag declaration/case/.env key missing"
        return
    fi
    if (( decl_line >= heredoc_start || case_line >= heredoc_start )); then
        fail "scarlix-mode: trust flag logic is not computed before the .env heredoc (P0 regression)"
        return
    fi
    if (( value_line <= heredoc_start || value_line >= heredoc_end )); then
        fail "scarlix-mode: VLLM_TRUST_FLAG key is not inside the .env heredoc"
        return
    fi

    # The previous bug put executable if/case/fi lines inside the heredoc,
    # which emitted invalid non KEY=VALUE lines into .env. The heredoc is UNQUOTED,
    # so $(...) and `...` inside it (even in comments) would EXECUTE, not just emit text.
    # Allow-list: every line must be blank, a comment, or UPPER_CASE KEY=VALUE where
    # VALUE is only [A-Za-z0-9_./:-] plus $var / ${var} expansions; and no line may
    # contain command substitution ($( or backtick) anywhere.
    control_line=$(awk -v start="$heredoc_start" -v end="$heredoc_end" '
        NR > start && NR < end {
            if ($0 ~ /\$\(|`/) { print NR ":" $0; exit }
            if ($0 ~ /^[[:space:]]*$/ || $0 ~ /^#/) next
            if ($0 !~ /^[A-Z][A-Z0-9_]*=([A-Za-z0-9_.\/:-]|\$[a-z_][a-z0-9_]*|\$\{[a-z_][a-z0-9_]*\})*$/) { print NR ":" $0; exit }
        }' "$mode")
    if [[ -n "$control_line" ]]; then
        fail "scarlix-mode: unsafe/non KEY=VALUE line inside .env heredoc (shell logic or command substitution leaked?): $control_line"
        return
    fi

    pass "trust flag logic precedes heredoc; only KEY=VALUE entry is inside heredoc (lines $case_line, $value_line)"
}

# ===========================================================================
# Check 11 — v19.2.12 F-02 (Deep Audit): single-GPU adaptation present
# The vLLM compose pins device_ids ["1"] which cannot exist on single-GPU
# hosts. scarlix-mode must gate vLLM behind GPU_COUNT detection with an
# explicit SCARLIX_ALLOW_SHARED_GPU opt-in. This check verifies the gate
# exists and that on single-GPU hosts the vLLM path is adapted, never a
# raw hardcoded device-1 start.
# ===========================================================================
check_11_single_gpu_adaptation() {
    echo "--- [11] Single-GPU adaptation: vLLM gated by GPU_COUNT + sharing opt-in ---"
    local mode="$REPO_DIR/files/usr/local/bin/scarlix-mode"
    if [[ ! -f "$mode" ]]; then
        fail "scarlix-mode not found"
        return
    fi
    local has_detection has_gate has_optin has_override
    has_detection=$(grep -c 'nvidia-smi -L' "$mode" || true)
    has_gate=$(grep -c 'vllm_allowed_on_this_host' "$mode" || true)
    has_optin=$(grep -c 'SCARLIX_ALLOW_SHARED_GPU' "$mode" || true)
    has_override=$(grep -c 'docker-compose.gpu-adapter.yml' "$mode" || true)
    if (( has_detection == 0 )); then
        fail "scarlix-mode: GPU_COUNT detection (nvidia-smi -L) missing — single-GPU hosts cannot adapt (F-02)"
        return
    fi
    if (( has_gate == 0 )); then
        fail "scarlix-mode: vllm_allowed_on_this_host gate missing — vLLM would hardcode device 1 on single-GPU hosts (F-02)"
        return
    fi
    if (( has_optin == 0 )); then
        fail "scarlix-mode: SCARLIX_ALLOW_SHARED_GPU opt-in missing (F-02)"
        return
    fi
    if (( has_override == 0 )); then
        fail "scarlix-mode: GPU-adapter override generation missing (F-02)"
        return
    fi
    # Live check when nvidia-smi is available: on a single-GPU host the
    # effective compose invocation must not pin device 1 without adaptation.
    if command -v nvidia-smi >/dev/null 2>&1; then
        local gpu_count
        gpu_count=$(nvidia-smi -L 2>/dev/null | wc -l || echo 0)
        if (( gpu_count < 2 )); then
            # KPK-19.2.13-P2-02: use the strict opt-in parser (exact
            # SCARLIX_ALLOW_SHARED_GPU=1 only) instead of a prefix grep.
            local flag_fn=""
            flag_fn=$(awk '/# --- strict env flag parsing \(KPK-19\.2\.13-P2-02\) begin ---/,/# --- strict env flag parsing \(KPK-19\.2\.13-P2-02\) end ---/' "$mode")
            local sharing_enabled=0
            if [ -n "$flag_fn" ]; then
                sharing_enabled=$(bash -c "${flag_fn}
scarlix_env_flag_enabled SCARLIX_ALLOW_SHARED_GPU /etc/scarlix/.env >/dev/null 2>&1 && echo 1 || echo 0")
            fi
            if [[ -f /opt/scarlix/ai/vllm/docker-compose.gpu-adapter.yml ]] \
               || [ "$sharing_enabled" -eq 0 ]; then
                pass "single-GPU host (GPU_COUNT=$gpu_count): vLLM adapted or gated off (F-02)"
            else
                fail "single-GPU host with sharing opt-in but no GPU adapter override generated (F-02)"
            fi
            return
        fi
    fi
    pass "GPU_COUNT detection, vLLM gate, sharing opt-in and override generation present (F-02)"
}

# ===========================================================================
# Check 12 — v19.2.12 F-01 (Deep Audit): Pi-Bolt installer SHA-256 pin
# install.sh must never pipe an unverified remote installer into a shell.
# Requires: the pin file ships with the repo AND install.sh verifies it.
# ===========================================================================
check_12_pibolt_sha256_pin() {
    echo "--- [12] Pi-Bolt installer is SHA-256 pinned (no raw curl|sh) ---"
    local installer="$REPO_DIR/install.sh"
    local pin="$REPO_DIR/files/usr/local/share/scarlix/pi-bolt-install.sha256"
    if [[ ! -f "$installer" ]]; then
        fail "install.sh not found"
        return
    fi
    if grep -q 'pi-bolt.opensec.in/install.sh. | sh' "$installer" \
       || grep -qE "curl -fsSL https://pi-bolt[^']*\| *sh" "$installer"; then
        fail "install.sh still contains an unpinned curl|sh for the Pi-Bolt installer (F-01)"
        return
    fi
    if [[ ! -f "$pin" ]]; then
        fail "Pi-Bolt SHA-256 pin file missing: files/usr/local/share/scarlix/pi-bolt-install.sha256 (F-01)"
        return
    fi
    if ! grep -qE '^[0-9a-f]{64}  pi-bolt-install\.sh$' "$pin"; then
        fail "Pi-Bolt pin file malformed (expected '<64-hex>  pi-bolt-install.sh') (F-01)"
        return
    fi
    if ! grep -q 'pi_bolt_verified' "$installer" || ! grep -q 'sha256sum' "$installer"; then
        fail "install.sh does not verify the Pi-Bolt installer hash before executing it (F-01)"
        return
    fi
    pass "Pi-Bolt installer downloaded to temp, SHA-256 verified against the shipped pin, executed only on match (F-01)"
}

# ===========================================================================
# Check 13 — v19.2.13 KPK-19.2.12-P2-01: Pi-Bolt verified-installer execution
# must hand the root-verified installer to a USER-OWNED private copy before
# `sudo -u $REAL_USER bash` — executing a root-owned 0600 mktemp file as an
# unprivileged user can never work (unreadable). Also verifies the P3-01
# cleanup traps for BOTH temp copies on every success/failure/interrupt path.
# ===========================================================================
check_13_pibolt_user_owned_execution() {
    echo "--- [13] Pi-Bolt installer executes a user-owned verified copy (not a root-owned 0600 temp) ---"
    local installer="$REPO_DIR/install.sh"
    if [[ ! -f "$installer" ]]; then
        fail "install.sh not found"
        return
    fi
    # (a) The root-owned temp must never be the file executed as REAL_USER.
    if grep -q 'sudo -u "\$REAL_USER" bash "\$PI_BOLT_TMP"' "$installer" \
       || grep -Eq 'sudo -u +"?\$REAL_USER"? +bash +"?\$PI_BOLT_TMP"?' "$installer"; then
        fail "install.sh executes the root-owned PI_BOLT_TMP as REAL_USER — the unprivileged user cannot read a root-owned 0600 file (P2-01)"
        return
    fi
    # (b) The verified content must be handed to a user-owned private copy:
    #     mktemp AS the user + install -o/-m 0600 from root.
    if ! grep -q 'sudo -u "\$REAL_USER" mktemp' "$installer"; then
        fail "install.sh does not create the user-side temp file as REAL_USER (P2-01)"
        return
    fi
    if ! grep -Eq 'install +-o +"?\$REAL_USER"? +-m 0600' "$installer"; then
        fail "install.sh does not hand the verified installer to a user-owned copy with install -o -m 0600 (P2-01 — never world-readable)"
        return
    fi
    # (c) The executed script must be the user-owned copy.
    if ! grep -q 'sudo -u "\$REAL_USER" bash "\$PI_BOLT_USER_TMP"' "$installer"; then
        fail "install.sh must execute PI_BOLT_USER_TMP (the user-owned verified copy), not the root temp (P2-01)"
        return
    fi
    # (d) P3-01: both temp copies cleaned up on success/failure/interrupt paths.
    if ! grep -q 'pi_bolt_cleanup' "$installer" \
       || ! grep -Eq 'trap +pi_bolt_cleanup +EXIT' "$installer" \
       || ! grep -Eq "trap +'pi_bolt_cleanup; exit 130' +INT" "$installer" \
       || ! grep -Eq "trap +'pi_bolt_cleanup; exit 143' +TERM" "$installer"; then
        fail "install.sh must clean up BOTH Pi-Bolt temp copies via EXIT/INT/TERM traps (P3-01)"
        return
    fi
    # (e) Runtime mechanic (runs as the CURRENT unprivileged user, mirroring
    #     the handoff): private temp A, hash-pinned content, install -m 0600
    #     copy B, execute B — the verified content is readable and invoked by
    #     a non-root user; a checksum mismatch still prevents execution.
    local tmp_a tmp_b
    tmp_a=$(mktemp /tmp/pibolt-check-a.XXXXXX)
    tmp_b=$(mktemp /tmp/pibolt-check-b.XXXXXX)
    printf '#!/usr/bin/env bash\necho verified-installer-ran\n' > "$tmp_a"
    local pinned actual
    pinned=$(sha256sum "$tmp_a" | awk '{print $1}')
    if ! install -m 0600 "$tmp_a" "$tmp_b" 2>/dev/null; then
        fail "runtime handoff mechanic: install -m 0600 copy failed"
        rm -f "$tmp_a" "$tmp_b"
        return
    fi
    actual=$(sha256sum "$tmp_b" | awk '{print $1}')
    if [[ "$actual" != "$pinned" ]]; then
        fail "runtime handoff mechanic: content hash changed during the copy"
        rm -f "$tmp_a" "$tmp_b"
        return
    fi
    local out
    out=$(bash "$tmp_b" 2>/dev/null)
    if [[ "$out" != "verified-installer-ran" ]]; then
        fail "runtime handoff mechanic: the verified copy was not readable/executable as the unprivileged user (P2-01)"
        rm -f "$tmp_a" "$tmp_b"
        return
    fi
    rm -f "$tmp_a" "$tmp_b"
    pass "verified installer handed to a user-owned 0600 copy before non-root execution; both temps trapped for cleanup (P2-01/P3-01)"
}

# ===========================================================================
# Check 14 — v19.2.13 KPK-19.2.12-P2-02: GPU-sharing opt-in must accept ONLY
# the exact line SCARLIX_ALLOW_SHARED_GPU=1. The v19.2.12 prefix grep also
# matched =10, =1unexpected, "=1 # comment"… This check extracts the PRODUCTION
# parser from scarlix-mode and runs the full value matrix.
# ===========================================================================
check_14_strict_gpu_optin_parsing() {
    echo "--- [14] GPU sharing opt-in accepts only exact SCARLIX_ALLOW_SHARED_GPU=1 ---"
    local mode="$REPO_DIR/files/usr/local/bin/scarlix-mode"
    if [[ ! -f "$mode" ]]; then
        fail "scarlix-mode not found"
        return
    fi
    local flag_fn
    flag_fn=$(awk '/# --- strict env flag parsing \(KPK-19\.2\.13-P2-02\) begin ---/,/# --- strict env flag parsing \(KPK-19\.2\.13-P2-02\) end ---/' "$mode")
    if [[ -z "$flag_fn" ]]; then
        fail "scarlix-mode: strict env flag parser (scarlix_env_flag_enabled) missing (P2-02)"
        return
    fi
    if ! grep -q 'grep -c "^\${key}="' <<<"$flag_fn" && ! grep -q 'grep -c "^\${key}="' "$mode"; then
        fail "scarlix-mode: strict parser must count exact key definitions, not prefix-match them (P2-02)"
        return
    fi
    local td
    td=$(mktemp -d)
    local runner
    runner=$(mktemp)
    {
        printf '%s\n' "$flag_fn"
        cat <<'RUNNER'
run_case() {
    local content="$1" expect="$2" name="$3" file="$4"
    printf '%b' "$content" > "$file"
    if scarlix_env_flag_enabled SCARLIX_ALLOW_SHARED_GPU "$file" 2>/dev/null; then got=1; else got=0; fi
    if [ "$got" != "$expect" ]; then
        echo "FAIL:$name"
    else
        echo "OK:$name"
    fi
}
RUNNER
        cat <<CASES
run_case 'SCARLIX_ALLOW_SHARED_GPU=1\n' 1 exact-one "$td/a"
run_case 'SCARLIX_ALLOW_SHARED_GPU=0\n' 0 zero "$td/a"
run_case 'SCARLIX_ALLOW_SHARED_GPU=10\n' 0 ten "$td/a"
run_case 'SCARLIX_ALLOW_SHARED_GPU=1unexpected\n' 0 one-unexpected "$td/a"
run_case 'SCARLIX_ALLOW_SHARED_GPU=true\n' 0 true "$td/a"
run_case 'SCARLIX_ALLOW_SHARED_GPU=\n' 0 empty "$td/a"
run_case ' SCARLIX_ALLOW_SHARED_GPU=1\n' 0 leading-whitespace "$td/a"
run_case 'SCARLIX_ALLOW_SHARED_GPU=1 # comment\n' 0 trailing-comment "$td/a"
run_case 'SCARLIX_ALLOW_SHARED_GPU=1\nSCARLIX_ALLOW_SHARED_GPU=1\n' 0 duplicate-keys "$td/a"
run_case '# SCARLIX_ALLOW_SHARED_GPU=1\n' 0 commented-out "$td/a"
CASES
    } > "$runner"
    local results bad
    results=$(bash "$runner" 2>/dev/null)
    bad=$(grep -c '^FAIL:' <<<"$results" || true)
    rm -rf "$td" "$runner"
    if (( bad > 0 )); then
        fail "strict opt-in parser rejected valid input or accepted malformed values: $(grep '^FAIL:' <<<"$results" | tr '\n' ' ') (P2-02)"
        return
    fi
    if ! grep -q '^OK:exact-one$' <<<"$results"; then
        fail "the exact documented opt-in SCARLIX_ALLOW_SHARED_GPU=1 must enable sharing (P2-02)"
        return
    fi
    pass "only the exact SCARLIX_ALLOW_SHARED_GPU=1 line enables GPU sharing; malformed/duplicate values fail closed (P2-02)"
}


# ---------------------------------------------------------------------------
# v19.3.1 — Gaming Stack Integration Plan regression checks
# ---------------------------------------------------------------------------
check_15_gaming_manifest_and_installer() {
    echo "--- [15] Gaming manifest well-formed; installer never removes packages ---"
    local manifest="$REPO_DIR/files/usr/local/share/scarlix/gaming-packages.conf"
    local installer="$REPO_DIR/files/usr/local/bin/scarlix-gaming-install"
    if [[ ! -f "$manifest" ]]; then
        fail "gaming manifest missing: files/usr/local/share/scarlix/gaming-packages.conf"
        return
    fi
    if [[ ! -f "$installer" ]]; then
        fail "gaming installer missing: files/usr/local/bin/scarlix-gaming-install"
        return
    fi
    # Manifest format: every non-comment row has exactly 5 pipe-separated
    # fields with valid profile/channel tokens and no duplicate packages.
    local bad_fields bad_tokens dup
    bad_fields=$(grep -vE '^\s*#|^\s*$' "$manifest" | awk -F'|' 'NF != 5 {print NR}' | wc -l)
    if (( bad_fields > 0 )); then
        fail "gaming manifest has $bad_fields malformed row(s) (need exactly 5 fields)"
        return
    fi
    bad_tokens=$(grep -vE '^\s*#|^\s*$' "$manifest" | awk -F'|' '
        { p=$1; c=$2; k=$4;
          gsub(/^ +| +$/, "", p); gsub(/^ +| +$/, "", c); gsub(/^ +| +$/, "", k);
          if (p !~ /^(minimal|full|inventory)$/) print "profile:" p;
          if (c !~ /^(pacman|aur|flatpak|systemd|compose)$/) print "channel:" c;
          if (k !~ /^(launcher|compatibility|performance|streaming|server)$/) print "kind:" k }' | wc -l)
    if (( bad_tokens > 0 )); then
        fail "gaming manifest has $bad_tokens invalid profile/channel/kind token(s)"
        return
    fi
    dup=$(grep -vE '^\s*#|^\s*$' "$manifest" | awk -F'|' '{gsub(/^ +| +$/, "", $2); gsub(/^ +| +$/, "", $3); print $2 "|" $3}' | sort | uniq -d | wc -l)
    if (( dup > 0 )); then
        fail "gaming manifest has $dup duplicate (channel, package) row(s)"
        return
    fi
    # The minimal profile must mirror the packages.x86_64 base gaming block.
    local missing_base
    missing_base=0
    local pkg
    for pkg in steam wine wine-gecko wine-mono lutris; do
        if ! grep -vE '^\s*#|^\s*$' "$manifest" | awk -F'|' '{gsub(/^ +| +$/, "", $1); gsub(/^ +| +$/, "", $2); gsub(/^ +| +$/, "", $3)} $1=="minimal" && $2=="pacman" && $3=="'"$pkg"'" {found=1} END{exit !found}' ; then
            missing_base=$((missing_base + 1))
        fi
    done
    if (( missing_base > 0 )); then
        fail "gaming manifest minimal profile is missing $missing_base base package(s) from the packages.x86_64 gaming block"
        return
    fi
    # The installer must be bash-clean and NEVER remove packages (the plan's
    # no-auto-removal rule — user game data must never be at risk).
    if ! bash -n "$installer" 2>/dev/null; then
        fail "scarlix-gaming-install fails bash -n"
        return
    fi
    # Removal commands must not appear in EXECUTED positions. Documentation
    # strings are filtered out before the check: log/warn/fail/printf/echo
    # lines (headless guidance) AND comment lines — the installer's own
    # safety-contract comment documents the ban and mentions "pacman -R",
    # which is text, not an executed command.
    # v19.3.2 fix (deterministic): the old form piped grep -v | grep -q.
    # With `set -o pipefail`, grep -q's early exit can SIGPIPE the first
    # grep (exit 141) — the pipeline then evaluated non-zero and the check
    # PASSED or FAILED depending on process scheduling (CI flake on the
    # v19.3.2 tag: false positive on the safety-contract comment). Now:
    # comments filtered too, and the match is collected via command
    # substitution WITHOUT grep -q, so no early exit, no SIGPIPE, no race.
    local removal_hits
    removal_hits=$(grep -vE '^[[:space:]]*(#|;)' "$installer" \
        | grep -vE '^[[:space:]]*(log|warn|fail|printf|echo)([[:space:]]|$)' \
        | grep -E '(pacman|yay) +-R' || true)
    if [[ -n "$removal_hits" ]]; then
        fail "scarlix-gaming-install contains package-removal commands (forbidden: never remove user packages)"
        return
    fi
    # AUR installs must refuse root (yay must never run as root).
    if ! grep -q 'id -u.*-eq 0' "$installer" || ! grep -q 'yay refuses root' "$installer"; then
        fail "scarlix-gaming-install must fail closed when AUR installs are attempted as root"
        return
    fi
    pass "gaming manifest well-formed (5 fields, valid tokens, no dupes, base block mirrored); installer never removes packages"
}

check_16_gaming_readonly_api() {
    echo "--- [16] Gaming inventory is read-only (no exec, no launch, auth-gated) ---"
    local rest="$REPO_DIR/scarlihq/internal/api/rest.go"
    local gamingdir="$REPO_DIR/scarlihq/internal/gaming"
    if [[ ! -f "$rest" ]] || [[ ! -d "$gamingdir" ]]; then
        fail "gaming API contract files missing (rest.go / internal/gaming)"
        return
    fi
    # The endpoint must exist AND sit behind the auth middleware.
    if ! grep -q 'mux.HandleFunc("/api/gaming", h.auth(h.gamingInventory))' "$rest"; then
        fail "/api/gaming must be registered via h.auth() — an unauthenticated gaming endpoint is a regression"
        return
    fi
    # Read-only: non-GET methods must be rejected by the handler.
    if ! grep -q 'gaming inventory is read-only' "$rest"; then
        fail "gamingInventory handler must reject non-GET methods (read-only contract)"
        return
    fi
    # The Go gaming package is a pure data contract: no command execution,
    # no docker socket, no arbitrary host access. (Detection is host-side.)
    if grep -rqE 'os/exec|exec.Command|Docker' "$gamingdir"; then
        fail "internal/gaming must stay a pure data contract — no exec/docker usage allowed"
        return
    fi
    # The host bridge must publish the gaming array into host-status.json.
    local bridge="$REPO_DIR/files/usr/local/bin/scarlix-host-bridge"
    if ! grep -q 'STATUS_GAMING=' "$bridge" || ! grep -q '"gaming": json.loads' "$bridge"; then
        fail "scarlix-host-bridge must publish the gaming inventory in host-status.json"
        return
    fi
    # The status contract must expose the typed entries.
    if ! grep -q 'Gaming.*\[\]gaming.Entry' "$REPO_DIR/scarlihq/internal/status/status.go"; then
        fail "HostStatus must expose the typed Gaming entries (single source of truth)"
        return
    fi
    pass "/api/gaming: auth-gated, GET-only, pure data contract, published via host-bridge host-status.json"
}

check_17_reservation_governor() {
    echo "--- [17] Reservation governor (F-04): fail-closed, disabled by default, off the HTTP plane ---"
    local gov_src="$REPO_DIR/scarlihq/cmd/scarlix-reservation/main.go"
    local gov_pkg="$REPO_DIR/scarlihq/internal/reservationops"
    local unit="$REPO_DIR/files/etc/systemd/system/scarlix-reservation.service"
    local tmpl="$REPO_DIR/docs/examples/reservation.json.example"
    local installer="$REPO_DIR/install.sh"

    if [[ ! -f "$gov_src" ]] || [[ ! -d "$gov_pkg" ]] || [[ ! -f "$unit" ]]; then
        fail "reservation governor source/unit missing (F-04 composition root)"
        return
    fi

    # The unit must keep the hardened shape: unprivileged user, strict
    # filesystem protection, dedicated state dir, docker group (read-only
    # consumer), bounded restarts.
    for directive in "User=65532" "ProtectSystem=strict" "StateDirectory=scarlix-reservation" "SupplementaryGroups=docker" "Restart=on-failure" "StartLimitBurst=5"; do
        if ! grep -q "^${directive}$" "$unit"; then
            fail "scarlix-reservation.service must keep hardening directive: $directive"
            return
        fi
    done

    # Store paths in ExecStart must be distinct files (reservation store ≠
    # allocation store) — NewFileBackedManager enforces this at runtime; the
    # unit must not drift from it.
    local exec_line reservations_path allocations_path
    exec_line=$(grep '^ExecStart=' "$unit" | head -1)
    reservations_path=$(echo "$exec_line" | grep -o '\--reservations [^ ]*' | awk '{print $2}')
    allocations_path=$(echo "$exec_line" | grep -o '\--allocations [^ ]*' | awk '{print $2}')
    if [[ -z "$reservations_path" || -z "$allocations_path" ]] || [[ "$reservations_path" == "$allocations_path" ]]; then
        fail "governor ExecStart must use distinct --reservations/--allocations store paths (got '$reservations_path' vs '$allocations_path')"
        return
    fi

    # Off the HTTP plane: the governor reconciles — it must not listen on
    # anything. A socket/API here would create a second privileged surface.
    if grep -qE 'http\.Listen|net\.Listen|ListenUnix|--serve|--socket' "$gov_src"; then
        fail "reservation governor must stay off the HTTP plane (no listeners allowed)"
        return
    fi

    # The template must carry only placeholders — a copied template can
    # never boot the service with a live-looking token.
    if [[ ! -f "$tmpl" ]]; then
        fail "reservation.json.example template missing"
        return
    fi
    if ! grep -q '"CHANGE_ME"' "$tmpl"; then
        fail "reservation.json.example must use CHANGE_ME placeholder tokens (fail-closed template)"
        return
    fi
    if grep -qE '"[0-9a-f]{32,}"' "$tmpl"; then
        fail "reservation.json.example must not contain token-shaped values"
        return
    fi

    # Never enabled automatically: install.sh may copy/build the governor
    # but must never systemctl enable it (operator decision, like the
    # workload bridge).
    if grep -qE 'systemctl +enable.*scarlix-reservation' "$installer"; then
        fail "install.sh must NEVER enable scarlix-reservation automatically"
        return
    fi

    # The compose opt-in must remain commented — compute enablement is an
    # operator decision with documented prerequisites.
    if ! grep -q '#   - SCARLIHQ_COMPUTE_ENABLED=true' "$REPO_DIR/scarlihq/docker-compose.yml"; then
        fail "compose compute opt-in must stay commented out (opt-in contract from v19.3.2)"
        return
    fi

    pass "reservation governor: hardened unit, distinct stores, no listeners, placeholder-only template, never auto-enabled"
}

# ---------------------------------------------------------------------------
# [18] Agent Kernel (AK-2): auth-gated session surface, closed enumeration,
#      bounded store, opt-in enablement
# ---------------------------------------------------------------------------
check_18_agent_kernel() {
    echo "--- [18] Agent Kernel (AK-2): auth-gated surface, closed capabilities, bounded store, opt-in ---"
    local kernel_pkg="$REPO_DIR/scarlihq/internal/agentkernel"
    local api_src="$REPO_DIR/scarlihq/internal/api/agentkernel.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"

    if [[ ! -d "$kernel_pkg" ]] || [[ ! -f "$api_src" ]]; then
        fail "agent kernel package/session surface missing (AK-2)"
        return
    fi

    # The enumeration must stay closed: every declared Capability constant is
    # collected and checked against a forbidden-token list. Static check on
    # declaration lines only (deterministic — the check_15 lesson: never pipe
    # grep -v | grep -q under pipefail).
    local declared
    declared=$(grep -oE 'Capability[A-Za-z]+ +Capability = "[^"]+"' "$kernel_pkg/kernel.go" | grep -oE '"[^"]+"' | tr -d '"' | sort -u)
    if [[ -z "$declared" ]]; then
        fail "no Capability constants found in kernel.go — enumeration shape changed?"
        return
    fi
    local bad_cap
    bad_cap=$(printf '%s\n' "$declared" | grep -Ei 'exec|shell|docker|file|root|sudo|systemctl|raw' || true)
    if [[ -n "$bad_cap" ]]; then
        fail "capability enumeration must never contain exec/shell/docker/file-class tokens (found: $bad_cap)"
        return
    fi
    local cap_count
    cap_count=$(printf '%s\n' "$declared" | wc -l)
    if [[ "$cap_count" -lt 1 || "$cap_count" -gt 16 ]]; then
        fail "capability enumeration size out of sane bounds (1..16): $cap_count — extending the set must be a deliberate reviewed act"
        return
    fi

    # Session routes must be behind the auth middleware (like every route).
    for route in "/api/agent/sessions" "/api/agent/sessions/transition" "/api/agent/sessions/evaluate"; do
        if ! grep -q "mux.HandleFunc(\"${route}\", h.authAdmin(h.agent" "$rest_src"; then
            fail "route ${route} must be registered behind h.authAdmin(...) (v19.6.7 KPK-19.6.6-P1-01: operator surfaces require the ADMIN principal)"
            return
        fi
    done

    # The evaluate endpoint must keep the denial-is-200 contract (a denial is
    # a successful auditable evaluation, not an HTTP error).
    if ! grep -q "denial is a successful" "$api_src"; then
        fail "agent kernel evaluate must document + keep the denial-is-200 contract"
        return
    fi

    # The store must be bounded and fail-closed on RNG.
    if ! grep -q "ErrSessionLimit" "$kernel_pkg/store.go" || ! grep -q "ErrRNG" "$kernel_pkg/store.go"; then
        fail "session store must enforce capacity + fail-closed RNG"
        return
    fi
    if ! grep -qE "DefaultMaxSessions = 128" "$kernel_pkg/store.go"; then
        fail "DefaultMaxSessions must stay an explicit bounded constant"
        return
    fi

    # Opt-in: main.go gates the surface on SCARLIHQ_AGENTKERNEL_ENABLED and
    # the compose opt-in must stay commented (operator decision).
    if ! grep -q "SCARLIHQ_AGENTKERNEL_ENABLED" "$main_src"; then
        fail "main.go must gate the agent kernel surface on SCARLIHQ_AGENTKERNEL_ENABLED"
        return
    fi
    if ! grep -q '#   - SCARLIHQ_AGENTKERNEL_ENABLED=true' "$compose"; then
        fail "compose agent-kernel opt-in must stay commented out (opt-in contract from v19.4.1)"
        return
    fi

    pass "agent kernel: closed enumeration (${cap_count} caps, no exec/shell), auth-gated routes, bounded store, denial-is-200, opt-in enablement"
}

# ===========================================================================
# Check 19 — Buzz profile formally disabled (dead MinIO pin quarantined)
# ===========================================================================
# v19.4.3 (KPK-03, P0): upstream MinIO discontinued ALL official artifact
# channels (independently re-verified 2026-10-22 — see the header of
# workspace/buzz/docker-compose.yml.disabled). A clean `docker compose
# --profile buzz up` would FAIL at image pull. Remediation chosen per the
# KpK review decision tree: the profile is FORMALLY DISABLED (renamed file,
# not picked up by any compose tooling) instead of switching to an
# unverified third-party mirror or a guessed version.
check_19_buzz_profile_disabled() {
    echo "--- [19] Buzz profile formally disabled (dead MinIO pin quarantined) ---"
    local active="$REPO_DIR/workspace/buzz/docker-compose.yml"
    local disabled="$REPO_DIR/workspace/buzz/docker-compose.yml.disabled"
    if [[ -f "$active" ]]; then
        fail "workspace/buzz/docker-compose.yml is ACTIVE — the minio/minio pin is dead upstream (KPK-03); the profile must stay disabled"
        return
    fi
    if [[ ! -f "$disabled" ]]; then
        fail "workspace/buzz/docker-compose.yml.disabled not found — buzz workspace files missing"
        return
    fi
    if grep -q 'image: minio/minio' "$disabled"; then
        pass "buzz profile disabled (docker-compose.yml.disabled) — dead minio/minio pin quarantined, not deployable by any compose tooling"
    else
        fail "disabled buzz compose no longer references minio/minio — update check_19 to match the quarantine contract"
    fi
}

# ===========================================================================
# Check 20 — Agent Kernel grant registry (AK-3, v19.4.4)
# ===========================================================================
# AK-3 makes every capability grant a persisted, admin-owned act: a JSON
# registry (0600 file / 0700 dir, atomic temp+rename writes) with tombstone
# revocations, admin disable switches, and owner principals tied to the
# reservation governor's user_tokens space. This check guards the static
# contract: registry surface behind auth, fail-closed load, bounded registry,
# atomic persistence, and the opt-in wiring in main.go + compose.
check_20_agent_kernel_grants() {
    echo "--- [20] Agent Kernel grant registry (AK-3): admin-owned grants, fail-closed load, bounded, opt-in ---"
    local kernel_pkg="$REPO_DIR/scarlihq/internal/agentkernel"
    local grants_src="$kernel_pkg/grants.go"
    local api_src="$REPO_DIR/scarlihq/internal/api/agentkernel.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"

    if [[ ! -f "$grants_src" ]]; then
        fail "agent kernel grant registry missing (AK-3, v19.4.4)"
        return
    fi

    # Fail-closed load discipline: the sentinel error for loose permissions
    # must exist and the loader must NEVER auto-fix (tamper discipline).
    if ! grep -q "ErrGrantFilePermissions" "$grants_src"; then
        fail "grant registry must fail-closed on loose file/dir permissions (ErrGrantFilePermissions)"
        return
    fi
    if grep -qE "Chmod\(.*(grants|0666|0644)" "$grants_src" && ! grep -q "tmp.Chmod(0600)"; then
        fail "grant registry must never silently widen existing file permissions (only the temp write is 0600-by-construction)"
        return
    fi
    if ! grep -q "os.Rename" "$grants_src"; then
        fail "grant registry must persist atomically (temp file + rename — no half-written registry)"
        return
    fi

    # Bounded registry: explicit constants, same reasoning as the session cap.
    if ! grep -qE "DefaultMaxGrants = 256" "$grants_src" || ! grep -qE "DefaultMaxAgents = 64" "$grants_src"; then
        fail "grant registry bounds must stay explicit constants (DefaultMaxGrants/DefaultMaxAgents)"
        return
    fi

    # Grants are admin-owned acts: every record names its admin.
    if ! grep -q "granted_by is required" "$grants_src" && ! grep -q "granted_by.*required" "$grants_src"; then
        fail "grant records must require granted_by (admin-owned acts — no anonymous grants)"
        return
    fi
    # Tombstones: revocation is the only way a grant stops being in force.
    if ! grep -q "RevokedAt" "$grants_src" || ! grep -q "ErrGrantInactive" "$grants_src"; then
        fail "grant registry must keep tombstones + double-revoke detection"
        return
    fi

    # Grant routes behind the auth middleware (like every route).
    for route in "/api/agent/grants" "/api/agent/grants/revoke" "/api/agent/capabilities/disable" "/api/agent/capabilities/enable"; do
        if ! grep -q "mux.HandleFunc(\"${route}\", h.authAdmin(h.agent" "$rest_src"; then
            fail "route ${route} must be registered behind h.authAdmin(...) (v19.6.7 KPK-19.6.6-P1-01: operator surfaces require the ADMIN principal)"
            return
        fi
    done

    # Registry-mode session creation must reject request-supplied grants.
    if ! grep -q "capabilities are admin-owned" "$api_src"; then
        fail "registry-mode session create must reject the capabilities field (grants are admin-owned)"
        return
    fi

    # Opt-in wiring: grants file + principals env, fail-closed load in main.
    if ! grep -q "SCARLIHQ_AGENT_GRANTS_FILE" "$main_src"; then
        fail "main.go must wire the grant registry on SCARLIHQ_AGENT_GRANTS_FILE"
        return
    fi
    if ! grep -q "SCARLIHQ_AGENT_PRINCIPALS" "$main_src"; then
        fail "main.go must require SCARLIHQ_AGENT_PRINCIPALS (user_tokens principal space)"
        return
    fi
    if ! grep -q "registry load failed" "$main_src"; then
        fail "main.go must log.Fatalf on registry load errors (fail-closed startup, never run on a tampered registry)"
        return
    fi

    # Compose opt-in must stay commented (operator decision, like AK-2).
    if ! grep -q '#   - SCARLIHQ_AGENT_GRANTS_FILE=' "$compose"; then
        fail "compose grant-registry opt-in must stay commented out (operator decision)"
        return
    fi

    pass "agent kernel grants: admin-owned records, fail-closed load, atomic persistence, bounded registry, auth-gated routes, opt-in enablement"
}

# ===========================================================================
# Check 21 — Agent Kernel audit sink (AK-4, v19.4.5)
# ===========================================================================
# AK-4 persists every kernel decision (allow AND deny) and every admin act
# (grant/revoke/disable/enable) as append-only JSONL under
# /var/lib/scarlix-agentkernel/audit.jsonl (0600/0700, flock-serialized).
# This check guards the static contract: fail-closed construction, the
# append-before-serve ordering marker, line validation, bounded reads, and
# the opt-in wiring in main.go + compose.
check_21_agent_kernel_audit() {
    echo "--- [21] Agent Kernel audit sink (AK-4): append-only JSONL, fail-closed, bounded reads, opt-in ---"
    local kernel_pkg="$REPO_DIR/scarlihq/internal/agentkernel"
    local audit_src="$kernel_pkg/audit.go"
    local api_src="$REPO_DIR/scarlihq/internal/api/agentkernel.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"

    if [[ ! -f "$audit_src" ]]; then
        fail "agent kernel audit sink missing (AK-4, v19.4.5)"
        return
    fi

    # Fail-closed construction: loose file/dir permissions are construction
    # errors (tamper discipline — no auto-fix, like the grant registry).
    if ! grep -q "ErrAuditFilePermissions" "$audit_src"; then
        fail "audit sink must fail-closed on loose permissions (ErrAuditFilePermissions)"
        return
    fi
    # Append-before-serve: the evaluate handler persists the decision BEFORE
    # returning it — an unauditable decision is never served.
    if ! grep -q "decision not served" "$api_src"; then
        fail "evaluate must persist the audit line BEFORE serving the decision (fail-closed marker)"
        return
    fi
    # Denials are persisted too (the denial-is-200 persistence twin).
    if ! grep -q "AuditTypeEvaluate" "$audit_src"; then
        fail "audit sink must carry evaluate lines (allow AND deny)"
        return
    fi
    # Line validation: every line names its type and validates before write.
    if ! grep -q "ErrAuditEvent" "$audit_src" || ! grep -q "func (l AuditLine) validate()" "$audit_src"; then
        fail "audit lines must be validated before append (ErrAuditEvent + validate)"
        return
    fi
    # Append-only: JSONL appends under flock, flush + fsync before return.
    if ! grep -q "syscall.Flock" "$audit_src" || ! grep -q "O_APPEND" "$audit_src" || ! grep -q "file.Sync()" "$audit_src"; then
        fail "audit sink must append under flock with flush+fsync (reservation-store discipline)"
        return
    fi
    # Bounded reads: the read surface is capped.
    if ! grep -qE "DefaultMaxAuditRead = 200" "$audit_src"; then
        fail "audit read surface must stay bounded (DefaultMaxAuditRead explicit constant)"
        return
    fi
    # Admin acts are audited after their persist (honest ordering, reported).
    if ! grep -q "AuditTypeGrant" "$audit_src" || ! grep -q "AuditTypeDisable" "$audit_src"; then
        fail "audit sink must carry admin act lines (grant/revoke/disable/enable)"
        return
    fi
    # Audit route behind the auth middleware, read-only.
    if ! grep -q 'mux.HandleFunc("/api/agent/audit", h.authAdmin(h.agentAuditRoute))' "$rest_src"; then
        fail "route /api/agent/audit must be registered behind h.auth(...)"
        return
    fi
    if ! grep -q "audit is read-only" "$api_src"; then
        fail "audit endpoint must stay read-only (GET)"
        return
    fi
    # Malformed lines are reported, not hidden.
    if ! grep -q "audit_read_errors" "$api_src"; then
        fail "malformed audit lines must be REPORTED in the read response (audit_read_errors)"
        return
    fi

    # Opt-in wiring: audit file env + fail-closed init in main.
    if ! grep -q "SCARLIHQ_AGENT_AUDIT_FILE" "$main_src"; then
        fail "main.go must wire the audit sink on SCARLIHQ_AGENT_AUDIT_FILE"
        return
    fi
    if ! grep -q "audit sink initialization failed" "$main_src"; then
        fail "main.go must log.Fatalf on audit sink construction errors (fail-closed startup)"
        return
    fi

    # Compose opt-in must stay commented (operator decision, like AK-3).
    if ! grep -q '#   - SCARLIHQ_AGENT_AUDIT_FILE=' "$compose"; then
        fail "compose audit-sink opt-in must stay commented out (operator decision)"
        return
    fi

    pass "agent kernel audit: append-only JSONL (flock+fsync), fail-closed construction, decision-persisted-before-serve, validated lines, bounded reads, auth-gated read-only surface, opt-in enablement"
}

# ===========================================================================
# Check 22 — Agent Kernel compute intake (AK-5, v19.4.6)
# ===========================================================================
# AK-5 closes the v19.4.x line: agent sessions reserve/release compute
# allocations through the kernel decision + the workload bridge, with the
# agent's registry-declared owner as the acting principal. This check guards
# the static contract: the registry is REQUIRED for intake (spoofable
# caller-supplied owners are exactly what AK-5 prevents), the route is
# auth-gated, denials stay 200, and every attempt is audited.
check_22_agent_kernel_compute() {
    echo "--- [22] Agent Kernel compute intake (AK-5): kernel decides, bridge enforces, registry required ---"
    local api_src="$REPO_DIR/scarlihq/internal/api/agentcompute.go"
    local audit_src="$REPO_DIR/scarlihq/internal/agentkernel/audit.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"

    if [[ ! -f "$api_src" ]]; then
        fail "agent kernel compute intake missing (AK-5, v19.4.6)"
        return
    fi

    # The registry is REQUIRED: legacy-mode owner would be spoofable.
    if ! grep -q "compute intake requires the grant registry" "$api_src"; then
        fail "compute intake must require the grant registry (admin-verified principal)"
        return
    fi
    # The kernel decides (deterministic evaluation), denial-is-200.
    if ! grep -q "Denial-is-200" "$api_src" && ! grep -q "denial-is-200" "$api_src"; then
        fail "compute intake must keep the denial-is-200 contract"
        return
    fi
    # The agent acts as its registry-declared owner (principal mapping).
    if ! grep -q "AgentOwner" "$api_src"; then
        fail "compute intake must map the acting principal to the agent registry owner"
        return
    fi
    # The workload bridge enforces ownership (same error mapping as direct).
    if ! grep -q "ErrOwnerMismatch" "$api_src" || ! grep -q "workload.ErrInvalidState" "$api_src"; then
        fail "compute intake must surface bridge ownership/state errors (same mapping as the direct lifecycle)"
        return
    fi
    # Every attempt is audited with the allocation id + outcome.
    if ! grep -q "AuditTypeComputeReserve" "$audit_src" || ! grep -q "AllocationID" "$audit_src"; then
        fail "audit sink must carry compute intake lines with allocation ids"
        return
    fi
    # Audit failure is never silent (fail-closed for denials; reported for executed acts).
    if ! grep -q "decision not served" "$api_src" && ! grep -q "audit write failed" "$api_src"; then
        fail "compute intake audit failures must be surfaced, never silent"
        return
    fi
    # Route behind the auth middleware.
    if ! grep -q 'mux.HandleFunc("/api/agent/sessions/compute", h.authAdmin(h.agentCompute))' "$rest_src"; then
        fail "route /api/agent/sessions/compute must be registered behind h.authAdmin(...) (v19.6.7 KPK-19.6.6-P1-01)"
        return
    fi
    # Bounded request discipline (the compute-lifecycle contract).
    if ! grep -q "MaxBytesReader" "$api_src" && ! grep -q "decodeOneJSON" "$api_src"; then
        fail "compute intake must use the bounded one-object decode discipline"
        return
    fi
    # 45s bridge timeout parity with the direct lifecycle surface.
    if ! grep -q "45\*time.Second" "$api_src" && ! grep -q "45 \\* time.Second" "$api_src"; then
        fail "compute intake must keep the 45s bridge timeout (lifecycle parity)"
        return
    fi
    # Honest compose note: intake needs BOTH the kernel AND the compute lifecycle.
    if ! grep -q "SCARLIHQ_COMPUTE_ENABLED" "$compose"; then
        fail "compose must keep the compute lifecycle opt-in block (AK-5 depends on it)"
        return
    fi

    pass "agent kernel compute: kernel-gated intake (denial-is-200), registry-required principal mapping, bridge ownership enforcement, audited attempts (allocation+outcome), auth-gated route"
}

check_23_agent_fabric_commands() {
    echo "--- [23] Agent Fabric command surface (BF-1): intent capture, kernel-decided status, persisted history ---"
    local fabric_store="$REPO_DIR/scarlihq/internal/agentfabric/store.go"
    local fabric_cmd="$REPO_DIR/scarlihq/internal/agentfabric/command.go"
    local api_src="$REPO_DIR/scarlihq/internal/api/agentfabric.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"

    if [[ ! -f "$fabric_store" ]]; then
        fail "agent fabric command store missing (BF-1, v19.5.0)"
        return
    fi

    # Fail-closed load: schema version + permissions discipline (grants.json precedent).
    if ! grep -q "commandsFileVersion" "$fabric_store" || ! grep -q "explicit migration required" "$fabric_store"; then
        fail "command store must version its schema and reject unknown versions (fail-closed load)"
        return
    fi
    if ! grep -q "ErrCommandFilePermissions" "$fabric_store"; then
        fail "command store must fail-closed on loose file/dir permissions (tamper discipline)"
        return
    fi
    # Atomic persistence: temp file in the same directory + rename (POSIX atomic).
    if ! grep -q "CreateTemp" "$fabric_store" || ! grep -q "os.Rename" "$fabric_store"; then
        fail "command store must persist atomically (temp + rename, the grants.json discipline)"
        return
    fi
    # Bounded history — no unbounded growth.
    if ! grep -q "DefaultMaxCommands" "$fabric_store" || ! grep -q "ErrCommandLimit" "$fabric_store"; then
        fail "command store must be bounded (DefaultMaxCommands + ErrCommandLimit)"
        return
    fi
    # Intent bounds: single-line, rune-bounded, control chars rejected.
    if ! grep -q "MaxIntentRunes" "$fabric_cmd" || ! grep -q "control character" "$fabric_cmd"; then
        fail "intent validation must bound the text and reject control characters"
        return
    fi
    # The bar NEVER executes: the kernel decides — the decision (allow AND deny)
    # is recorded on the command. Route/cancel/routeAndDecide must go through
    # the kernel evaluation, not a shortcut.
    if ! grep -q "h.agentStore.Evaluate" "$api_src"; then
        fail "fabric route+decide must evaluate through the Agent Kernel (no standalone mode)"
        return
    fi
    # Denial-is-a-decision: a denied intent is DECIDED, not an HTTP error.
    if ! grep -q "denial-is-200" "$api_src" && ! grep -q "Denial-is" "$api_src"; then
        fail "fabric surface must keep the denial-is-a-decision contract (kernel denial-is-200)"
        return
    fi
    # Decision-persisted-before-serve (AK-4 discipline extended): the audit line
    # is appended BEFORE the command transitions to DECIDED.
    if ! grep -q "decision not served" "$api_src" && ! grep -q "audit write failed" "$api_src"; then
        fail "fabric decisions must be audited before they are served (fail-closed)"
        return
    fi
    # An intent is never lost: submission persists BEFORE routing.
    if ! grep -q "never lost" "$api_src" && ! grep -q "never lost" "$fabric_store"; then
        fail "intent capture must persist the command even when nothing is routable (never lost)"
        return
    fi
    # Routes behind the auth middleware.
    if ! grep -q 'mux.HandleFunc("/api/bar/commands", h.authAdmin(h.barCommands))' "$rest_src" \
       || ! grep -q 'mux.HandleFunc("/api/bar/commands/route", h.authAdmin(h.barCommandRoute))' "$rest_src" \
       || ! grep -q 'mux.HandleFunc("/api/bar/commands/cancel", h.authAdmin(h.barCommandCancel))' "$rest_src"; then
        fail "routes /api/bar/commands{,/route,/cancel} must be registered behind h.authAdmin(...) (v19.6.7 KPK-19.6.6-P1-01: the fabric is an operator surface)"
        return
    fi
    # main.go opt-in: fabric requires the kernel + its own commands file.
    if ! grep -q "SCARLIHQ_AGENTFABRIC_ENABLED" "$main_src" || ! grep -q "SCARLIHQ_AGENTFABRIC_COMMANDS_FILE" "$main_src"; then
        fail "main.go must wire the fabric opt-in (SCARLIHQ_AGENTFABRIC_ENABLED + SCARLIHQ_AGENTFABRIC_COMMANDS_FILE)"
        return
    fi
    if ! grep -q "agent fabric requires SCARLIHQ_AGENTKERNEL_ENABLED=true" "$main_src"; then
        fail "main.go must fail-closed when the fabric is enabled without the kernel"
        return
    fi
    # Honest compose note: the fabric opt-in block exists (commented).
    if ! grep -q "SCARLIHQ_AGENTFABRIC_ENABLED" "$compose" || ! grep -q "SCARLIHQ_AGENTFABRIC_COMMANDS_FILE" "$compose"; then
        fail "compose must keep the Agent Fabric opt-in block (env + mount)"
        return
    fi

    pass "agent fabric commands: intent capture (never lost), kernel-decided status (denial-is-a-decision), audit-before-serve, persisted bounded history (fail-closed load), auth-gated routes, opt-in wiring"
}

# ===========================================================================
# [24] Agent Fabric workspaces (BF-2, v19.5.1)
# ===========================================================================
# Deterministic, source-level: the workspace store follows the grants.json/
# commands.json discipline; the isolation contract (one workspace per
# session, member-only routing, ARCHIVED terminal tombstone) is wired end
# to end — store, API surface, main.go wiring, compose opt-in, dashboard.
check_24_agent_fabric_workspaces() {
    echo "--- [24] Agent Fabric workspaces (BF-2): isolation contexts, member-only routing, terminal archive ---"
    local ws_store="$REPO_DIR/scarlihq/internal/agentfabric/wstore.go"
    local ws_model="$REPO_DIR/scarlihq/internal/agentfabric/workspace.go"
    local api_ws="$REPO_DIR/scarlihq/internal/api/agentfabric_ws.go"
    local api_cmd="$REPO_DIR/scarlihq/internal/api/agentfabric.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"
    local dashboard="$REPO_DIR/scarlihq/cmd/scarlihq/frontend/dist/index.html"

    if [[ ! -f "$ws_store" || ! -f "$ws_model" ]]; then
        fail "agent fabric workspace store/model missing (BF-2, v19.5.1)"
        return
    fi

    # Fail-closed load: schema version + permissions discipline (the store precedent).
    if ! grep -q "workspacesFileVersion" "$ws_store" || ! grep -q "explicit migration required" "$ws_store"; then
        fail "workspace store must version its schema and reject unknown versions (fail-closed load)"
        return
    fi
    if ! grep -q "ErrWorkspaceFilePermissions" "$ws_store"; then
        fail "workspace store must fail-closed on loose file/dir permissions (tamper discipline)"
        return
    fi
    # Atomic persistence: temp file in the same directory + rename.
    if ! grep -q "CreateTemp" "$ws_store" || ! grep -q "os.Rename" "$ws_store"; then
        fail "workspace store must persist atomically (temp + rename, the grants.json discipline)"
        return
    fi
    # Bounded stores — no unbounded growth.
    if ! grep -q "DefaultMaxWorkspaces" "$ws_store" || ! grep -q "ErrWorkspaceLimit" "$ws_store"; then
        fail "workspace store must be bounded (DefaultMaxWorkspaces + ErrWorkspaceLimit)"
        return
    fi
    if ! grep -q "MaxSessionsPerWorkspace" "$ws_store" || ! grep -q "ErrSessionLimit" "$ws_store"; then
        fail "workspace membership must be bounded (MaxSessionsPerWorkspace + ErrSessionLimit)"
        return
    fi
    # One workspace per session — the unambiguous routing namespace (load-time
    # cross-record check + join-time claim check).
    if ! grep -q "ErrSessionClaimed" "$ws_store" || ! grep -q "claimed by both" "$ws_store"; then
        fail "workspace store must enforce one workspace per session (claim check + load-time duplicate-claim rejection)"
        return
    fi
    # ARCHIVED is the terminal tombstone: closed enumeration, no membership
    # changes, no new submissions, routing closed.
    if ! grep -q "WorkspaceArchived" "$ws_model" || ! grep -q "terminal" "$ws_model"; then
        fail "workspace state must be a closed enumeration with ARCHIVED terminal"
        return
    fi
    if ! grep -q "ErrWorkspaceState" "$ws_store"; then
        fail "workspace store must reject mutations on ARCHIVED workspaces (ErrWorkspaceState)"
        return
    fi
    # The isolation contract at the API layer: scoped commands resolve ONLY
    # member sessions of an ACTIVE workspace.
    if ! grep -q "resolveWorkspaceScopedSession" "$api_ws" || ! grep -q "ws.IsMember" "$api_ws"; then
        fail "the API layer must resolve scoped routing through workspace membership only"
        return
    fi
    # ARCHIVED refusals: submit/route check the state explicitly (409), and
    # the workspace surface maps the store's ErrWorkspaceState to 409s.
    if ! grep -q "WorkspaceArchived" "$api_cmd"; then
        fail "command submit/route must refuse ARCHIVED workspaces (409 state conflicts — WorkspaceArchived checks)"
        return
    fi
    if ! grep -q "ErrWorkspaceState" "$api_ws"; then
        fail "the workspace surface must map ARCHIVED-state conflicts to 409 (ErrWorkspaceState)"
        return
    fi
    # Submission carries the optional scope (additive — the BF-1 unscoped
    # behavior must stay the default).
    if ! grep -q 'workspace_id,omitempty' "$api_cmd"; then
        fail "command submission must carry the optional workspace scope (workspace_id)"
        return
    fi
    # main.go wiring: the workspace store rides the fabric opt-in with its own
    # overridable file (default next to the commands file).
    if ! grep -q "SCARLIHQ_AGENTFABRIC_WORKSPACES_FILE" "$main_src" || ! grep -q "ConfigureAgentWorkspaces" "$main_src"; then
        fail "main.go must wire the workspace store into the fabric opt-in (SCARLIHQ_AGENTFABRIC_WORKSPACES_FILE + ConfigureAgentWorkspaces)"
        return
    fi
    # Auth-gated routes (4 surfaces).
    for route in '/api/bar/workspaces"' '/api/bar/workspaces/archive"' '/api/bar/workspaces/sessions"' '/api/bar/workspaces/sessions/leave"'; do
        if ! grep -q "$route" "$rest_src"; then
            fail "rest.go must register the auth-gated route $route"
            return
        fi
    done
    # Compose carries the documented opt-in (env + mount note).
    if ! grep -q "SCARLIHQ_AGENTFABRIC_WORKSPACES_FILE" "$compose"; then
        fail "compose must document the workspace store override (SCARLIHQ_AGENTFABRIC_WORKSPACES_FILE)"
        return
    fi
    # Dashboard: the scope selector + the workspaces card.
    if ! grep -q 'id="barWs"' "$dashboard" || ! grep -q 'id="wsbody"' "$dashboard"; then
        fail "the dashboard must carry the workspace scope selector and the workspaces card"
        return
    fi

    pass "agent fabric workspaces: isolation contexts (one workspace per session), member-only scoped routing, ARCHIVED terminal tombstone, persisted bounded stores (fail-closed load), auth-gated routes, opt-in wiring"
}

check_25_agent_fabric_runtimes() {
    echo "--- [25] Agent Fabric execution surface (BF-3): coding/research runtimes, fresh evaluation, terminal records ---"
    local rt_model="$REPO_DIR/scarlihq/internal/agentfabric/agentruntime.go"
    local exec_model="$REPO_DIR/scarlihq/internal/agentfabric/execution.go"
    local exec_store="$REPO_DIR/scarlihq/internal/agentfabric/estore.go"
    local bus_src="$REPO_DIR/scarlihq/internal/agentfabric/fabricbus.go"
    local wsbar_src="$REPO_DIR/scarlihq/internal/webui/wsbar.go"
    local api_exec="$REPO_DIR/scarlihq/internal/api/agentfabric_exec.go"
    local api_cmd="$REPO_DIR/scarlihq/internal/api/agentfabric.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"
    local dashboard="$REPO_DIR/scarlihq/cmd/scarlihq/frontend/dist/index.html"

    if [[ ! -f "$rt_model" || ! -f "$exec_model" || ! -f "$exec_store" ]]; then
        fail "agent fabric runtime/execution model missing (BF-3, v19.5.2)"
        return
    fi

    # Closed, total runtime mapping: every kernel capability maps to exactly
    # one runtime (research read-only / coding act) — load-time invariant.
    if ! grep -q "func RuntimeForCapability" "$rt_model" || ! grep -q "func ValidateRuntimeMapping" "$rt_model"; then
        fail "runtime mapping must be closed and total (RuntimeForCapability + ValidateRuntimeMapping — a drifted enumeration aborts startup)"
        return
    fi
    if ! grep -q "RuntimeResearch" "$rt_model" || ! grep -q "RuntimeCoding" "$rt_model"; then
        fail "the runtime enumeration must be research + coding (closed set)"
        return
    fi
    # Terminal-only execution states: NO in-flight state exists.
    if ! grep -q "ExecutionCompleted" "$exec_model" || ! grep -q "ExecutionFailed" "$exec_model" || ! grep -q "ExecutionSkipped" "$exec_model"; then
        fail "execution states must be COMPLETED/FAILED/SKIPPED (terminal by construction)"
        return
    fi
    if grep -q "ExecutionPending\|ExecutionRunning\|ExecutionInProgress" "$exec_model" "$exec_store" "$api_exec"; then
        fail "an in-flight execution state exists — records are terminal BY CONSTRUCTION (no PENDING/RUNNING)"
        return
    fi
    # State↔allow↔fields coherence enforced at validate (load-time integrity).
    if ! grep -q "COMPLETED with allow=false" "$exec_model" || ! grep -q "SKIPPED with allow=true" "$exec_model"; then
        fail "execution validate must enforce state↔allow coherence (completed needs allow, skipped is a fresh denial)"
        return
    fi
    # Runtime↔capability coherence: the closed mapping is record integrity.
    if ! grep -q "closed mapping coherence" "$exec_model"; then
        fail "execution records must pin runtime == RuntimeForCapability(capability) (closed mapping coherence)"
        return
    fi
    # One execution per command, load-enforced (duplicate = corruption).
    if ! grep -q "executed TWICE" "$exec_store" || ! grep -q "ErrAlreadyExecuted" "$exec_store"; then
        fail "execution store must enforce ONE execution per command (Record refusal + load-time duplicate rejection)"
        return
    fi
    # Fail-closed store: schema version + permissions + atomic persist + bound.
    if ! grep -q "executionsFileVersion" "$exec_store" || ! grep -q "explicit migration required" "$exec_store"; then
        fail "execution store must version its schema and reject unknown versions (fail-closed load)"
        return
    fi
    if ! grep -q "ErrExecutionFilePermissions" "$exec_store"; then
        fail "execution store must fail-closed on loose file/dir permissions (tamper discipline)"
        return
    fi
    if ! grep -q "CreateTemp" "$exec_store" || ! grep -q "os.Rename" "$exec_store"; then
        fail "execution store must persist atomically (temp + rename)"
        return
    fi
    if ! grep -q "DefaultMaxExecutions" "$exec_store" || ! grep -q "ErrExecutionLimit" "$exec_store"; then
        fail "execution store must be bounded (DefaultMaxExecutions + ErrExecutionLimit)"
        return
    fi
    # The stale-allow guard: FRESH kernel evaluation at execute time; a
    # fresh denial is an AUDITED SKIPPED (never an executed act).
    if ! grep -q "STALE-ALLOW GUARD" "$api_exec" || ! grep -q "ExecutionSkipped" "$api_exec"; then
        fail "execute must run a FRESH kernel evaluation (stale-allow guard) and record the fresh denial as SKIPPED"
        return
    fi
    if ! grep -q "audit write failed — execution not recorded" "$api_exec"; then
        fail "the fresh evaluation must be audited BEFORE the execution record exists (fail-closed ordering)"
        return
    fi
    # Closed params shapes (DisallowUnknownFields — a param shape is a contract).
    if ! grep -q "DisallowUnknownFields" "$rt_model" || ! grep -q "func ParseRuntimeParams" "$rt_model"; then
        fail "params must be closed shapes per capability (ParseRuntimeParams + DisallowUnknownFields)"
        return
    fi
    # ARCHIVED closes execution (the tombstone contract).
    if ! grep -q "WorkspaceArchived" "$api_exec"; then
        fail "execute must refuse ARCHIVED workspaces (409 — execution closed with routing)"
        return
    fi
    # CommandID shape BEFORE the lookup (400 vs 404 — the filter precedent).
    if ! grep -q "SHAPE first" "$api_exec"; then
        fail "execute must validate the command id shape BEFORE the lookup (400 shape vs 404 unknown)"
        return
    fi
    # Bounded bus: 16 subscribers × 16 slots, drop-not-block, no replay.
    if ! grep -q "MaxBusSubscribers" "$bus_src" || ! grep -q "BusSubscriberQueue" "$bus_src"; then
        fail "the fabric bus must be bounded (16 subscribers × 16 queues)"
        return
    fi
    if ! grep -q "DROP" "$bus_src" || ! grep -q "no replay\|NO REPLAY\|NoReplay" "$bus_src"; then
        fail "the fabric bus must drop-not-block and never replay (polling covers drops)"
        return
    fi
    # WS /ws/bar: ticket reserved BEFORE upgrade, shared wsSlots, nil bus 503.
    if ! grep -q "api.ReserveWSTicket" "$wsbar_src"; then
        fail "/ws/bar must reserve the single-use ticket BEFORE upgrader.Upgrade (the /ws discipline)"
        return
    fi
    if ! grep -q "wsSlots" "$wsbar_src"; then
        fail "/ws/bar must share the wsSlots budget with /ws (one bound across both surfaces)"
        return
    fi
    if ! grep -q "503" "$wsbar_src" || ! grep -q "bus == nil" "$wsbar_src"; then
        fail "/ws/bar must answer 503 BEFORE upgrade when the bus is nil (fabric off)"
        return
    fi
    # Change signals are command.*/execution.* — fabric.* are control frames.
    if ! grep -q "SignalExecutionRecorded" "$bus_src" || ! grep -q "SignalCommandSubmitted" "$bus_src"; then
        fail "the bus must carry command.*/execution.* change signals (the pinned closed set)"
        return
    fi
    # Auth-gated routes (2 surfaces) + the composed additive field.
    for route in '/api/bar/commands/execute"' '/api/bar/executions"'; do
        if ! grep -q "$route" "$rest_src"; then
            fail "rest.go must register the auth-gated route $route"
            return
        fi
    done
    if ! grep -q 'execution,omitempty' "$api_cmd"; then
        fail "command views must compose the additive execution field (omitempty — BF-1/BF-2 clients unaffected)"
        return
    fi
    # main.go wiring: the execution store + bus ride the fabric opt-in; the
    # runtime mapping invariant aborts startup on drift.
    if ! grep -q "SCARLIHQ_AGENTFABRIC_EXECUTIONS_FILE" "$main_src" || ! grep -q "ConfigureAgentFabricExecutions" "$main_src"; then
        fail "main.go must wire the execution store + bus into the fabric opt-in (SCARLIHQ_AGENTFABRIC_EXECUTIONS_FILE + ConfigureAgentFabricExecutions)"
        return
    fi
    if ! grep -q "ValidateRuntimeMapping" "$main_src" || ! grep -q "RegisterBarWS" "$main_src"; then
        fail "main.go must check the runtime mapping at load AND register /ws/bar (always — 503 with nil bus)"
        return
    fi
    # Compose carries the documented opt-in (env override).
    if ! grep -q "SCARLIHQ_AGENTFABRIC_EXECUTIONS_FILE" "$compose"; then
        fail "compose must document the execution store override (SCARLIHQ_AGENTFABRIC_EXECUTIONS_FILE)"
        return
    fi
    # Dashboard: the exec button + the wsBar push client.
    if ! grep -q 'data-barexec' "$dashboard" || ! grep -q "connectBarWS" "$dashboard"; then
        fail "the dashboard must carry the exec button and the /ws/bar push client (command.*/execution.* refetch)"
        return
    fi

    pass "agent fabric runtimes: closed total mapping (research read-only / coding bridged, load-time invariant), terminal-only execution records (COMPLETED/FAILED/SKIPPED, one per command), stale-allow guard (fresh evaluation, audited SKIPPED), closed params shapes, executions.json store (fail-closed, atomic, bounded 512), bounded bus (16×16 drop-not-block no-replay), /ws/bar ticket+shared-slots+503-nil-bus, auth-gated execute/executions routes, composed additive field, opt-in wiring"
}

check_26_agent_fabric_approvals() {
    echo "--- [26] Agent Fabric operator approvals (BF-4): the human gate for mutating acts, terminal decisions, one request per command ---"
    local apv_model="$REPO_DIR/scarlihq/internal/agentfabric/approval.go"
    local apv_store="$REPO_DIR/scarlihq/internal/agentfabric/astore.go"
    local bus_src="$REPO_DIR/scarlihq/internal/agentfabric/fabricbus.go"
    local cmd_src="$REPO_DIR/scarlihq/internal/agentfabric/command.go"
    local cmd_store="$REPO_DIR/scarlihq/internal/agentfabric/store.go"
    local api_appr="$REPO_DIR/scarlihq/internal/api/agentfabric_appr.go"
    local api_exec="$REPO_DIR/scarlihq/internal/api/agentfabric_exec.go"
    local api_cmd="$REPO_DIR/scarlihq/internal/api/agentfabric.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"
    local dashboard="$REPO_DIR/scarlihq/cmd/scarlihq/frontend/dist/index.html"

    if [[ ! -f "$apv_model" || ! -f "$apv_store" || ! -f "$api_appr" ]]; then
        fail "agent fabric approval model/store/surface missing (BF-4, v19.5.3)"
        return
    fi

    # Closed approval state enumeration: PENDING (the only in-flight state
    # in the whole fabric) + the terminal APPROVED/REJECTED. Deliberately
    # NO withdrawn state — a state no surface can reach would be a lie.
    if ! grep -q "ApprovalPending" "$apv_model" || ! grep -q "ApprovalApproved" "$apv_model" || ! grep -q "ApprovalRejected" "$apv_model"; then
        fail "approval states must be the closed PENDING/APPROVED/REJECTED enumeration"
        return
    fi
    if grep -q "ApprovalWithdrawn\|ApprovalCancelled" "$apv_model" "$apv_store" "$api_appr" "$bus_src"; then
        fail "a withdrawn/cancelled approval state exists — the enumeration is deliberately PENDING/APPROVED/REJECTED (a PENDING request stays answerable)"
        return
    fi
    # The approval boundary IS the runtime boundary — DERIVED, not
    # re-declared (the two policies cannot drift).
    if ! grep -q "func ApprovalRequiredForCapability" "$apv_model" || ! grep -q "RuntimeCoding" "$apv_model"; then
        fail "ApprovalRequiredForCapability must be DERIVED from the closed runtime mapping (the boundary is the runtime boundary)"
        return
    fi
    # Principal labels: bounded attribution, never credentials.
    if ! grep -q "func ValidatePrincipalLabel" "$apv_model" || ! grep -q "MaxPrincipalLabelRunes" "$apv_model"; then
        fail "submitter/approver labels must be bounded and validated (ValidatePrincipalLabel — attribution, not credentials)"
        return
    fi
    # State↔fields coherence: PENDING carries no decision fields; APPROVED
    # requires approver+execution+decided_at; REJECTED never an execution.
    if ! grep -q "PENDING record carries decision fields" "$apv_model" || ! grep -q "REJECTED record carries an execution reference" "$apv_model"; then
        fail "approval validate must enforce state↔fields coherence (PENDING bare, REJECTED never executed, APPROVED references the act)"
        return
    fi
    if ! grep -q "APPROVED requires the execution reference" "$apv_model"; then
        fail "APPROVED must require the terminal execution reference (the act ran — even an audited SKIPPED)"
        return
    fi
    # Runtime↔capability + params↔capability coherence on records.
    if ! grep -q "closed mapping coherence" "$apv_model" || ! grep -q "closed shape was not captured" "$apv_model"; then
        fail "approval records must pin runtime coherence AND the closed params shape captured at request time"
        return
    fi
    # Fail-closed store: schema version, permissions, atomic persist, bound,
    # one-request-per-command (refusal + load-time duplicate rejection).
    if ! grep -q "approvalsFileVersion" "$apv_store" || ! grep -q "explicit migration required" "$apv_store"; then
        fail "approval store must version its schema and reject unknown versions (fail-closed load)"
        return
    fi
    if ! grep -q "ErrApprovalFilePermissions" "$apv_store"; then
        fail "approval store must fail-closed on loose file/dir permissions (tamper discipline)"
        return
    fi
    if ! grep -q "CreateTemp" "$apv_store" || ! grep -q "os.Rename" "$apv_store"; then
        fail "approval store must persist atomically (temp + rename, in-memory rollback)"
        return
    fi
    if ! grep -q "DefaultMaxApprovals" "$apv_store" || ! grep -q "ErrApprovalLimit" "$apv_store"; then
        fail "approval store must be bounded (DefaultMaxApprovals + ErrApprovalLimit)"
        return
    fi
    if ! grep -q "ErrAlreadyRequested" "$apv_store" || ! grep -q "requested TWICE" "$apv_store"; then
        fail "approval store must enforce ONE request per command (Request refusal + load-time duplicate rejection)"
        return
    fi
    if ! grep -q "ErrApprovalState" "$apv_store"; then
        fail "terminal approval decisions must never be re-decided (ErrApprovalState)"
        return
    fi
    if ! grep -q "ErrApprovalRNG" "$apv_store"; then
        fail "approval id generation must be fail-closed on RNG failure (the cmd-/ws-/afx- precedent)"
        return
    fi
    # The operator gate on the execute surface: coding capabilities NEVER
    # execute directly — the ONE approval request is created (202).
    if ! grep -q "ApprovalRequiredForCapability" "$api_exec" || ! grep -q "barApprovalRequest" "$api_exec"; then
        fail "execute must gate on ApprovalRequiredForCapability — a coding act creates the approval request, never a direct execution"
        return
    fi
    if ! grep -q "StatusAccepted" "$api_appr"; then
        fail "the approval request must answer 202 Accepted (PENDING — the operator has not answered yet)"
        return
    fi
    if ! grep -q "a mutating (coding) act cannot execute without the operator approval surface" "$api_appr"; then
        fail "the execute surface must 503 without the approval store — a mutating act never silently bypasses the human gate"
        return
    fi
    # Approve runs the act: the full BF-3 flow (fresh evaluation = the
    # stale-allow guard at APPROVE time, audit-before-record, the terminal
    # record) composed with the operator's yes.
    if ! grep -q "STALE-ALLOW GUARD" "$api_appr" || ! grep -q "APPROVED + SKIPPED" "$api_appr"; then
        fail "approve must run the FRESH kernel evaluation (stale-allow guard at approve time); APPROVED + SKIPPED is the honest composition"
        return
    fi
    if ! grep -q "audit write failed" "$api_appr"; then
        fail "the approve-time fresh evaluation must be audited BEFORE the execution record exists (fail-closed ordering)"
        return
    fi
    # Approve cannot re-steer the act: the STORED params are re-used.
    if ! grep -q "re-uses the STORED params\|re-uses the STORED params" "$api_appr" && ! grep -q "STORED params" "$api_appr"; then
        fail "approve must re-use the params captured at REQUEST time (an approval decision cannot re-steer the act)"
        return
    fi
    # Reconciliation: a crashed approve after the execution persist closes
    # APPROVED against the EXISTING execution — no act runs twice.
    if ! grep -q "RECONCILIATION" "$api_appr"; then
        fail "approve must reconcile against an existing terminal execution (no act runs twice through that door)"
        return
    fi
    # Reject is UNCONDITIONAL (no kernel evaluation gates a rejection) and
    # closes the command's execution path.
    if ! grep -q "UNCONDITIONAL" "$api_appr"; then
        fail "reject must be unconditional — the operator's no is not kernel-evaluated (no act runs, nothing to evaluate)"
        return
    fi
    # ApprovalID SHAPE first (400 vs 404 — the filter precedent).
    if ! grep -q "SHAPE first" "$api_appr"; then
        fail "approve/reject must validate the approval id shape BEFORE the lookup (400 shape vs 404 unknown)"
        return
    fi
    # Closed state filter on the read surface.
    if ! grep -q "closed enumeration" "$api_appr"; then
        fail "the approvals read surface must reject unknown state filters (closed enumeration)"
        return
    fi
    # Submitter attribution: additive + omitempty (schema stays v1 —
    # BF-1/BF-2/BF-3 binaries and files unaffected), Submit stays unlabeled.
    if ! grep -q "Submitter" "$cmd_src" || ! grep -q "submitter,omitempty" "$cmd_src"; then
        fail "command records must carry the additive submitter label (omitempty — the commands schema stays v1)"
        return
    fi
    if ! grep -q "func (s \*CommandStore) SubmitWithSubmitter" "$cmd_store" || ! grep -q "return s.SubmitWithSubmitter" "$cmd_store"; then
        fail "Submit must delegate to SubmitWithSubmitter (BF-1/BF-2/BF-3 callers unaffected — empty label = the BF-1 behavior)"
        return
    fi
    # The composed additive approval field on command views.
    if ! grep -q 'approval,omitempty' "$api_cmd"; then
        fail "command views must compose the additive approval field (omitempty — BF-1..BF-3 clients unaffected)"
        return
    fi
    # Bus signals: approval.requested / approval.decided join the change
    # namespace (command.*/execution.* — fabric.* stays control).
    if ! grep -q "SignalApprovalRequested" "$bus_src" || ! grep -q "SignalApprovalDecided" "$bus_src"; then
        fail "the fabric bus must carry approval.requested/approval.decided change signals"
        return
    fi
    if grep -q "SignalApprovalWithdrawn\|approval.withdrawn" "$bus_src"; then
        fail "an approval.withdrawn signal exists — there is deliberately NO withdrawn state"
        return
    fi
    # Auth-gated routes (3 surfaces).
    for route in '/api/bar/approvals"' '/api/bar/approvals/approve"' '/api/bar/approvals/reject"'; do
        if ! grep -q "$route" "$rest_src"; then
            fail "rest.go must register the auth-gated route $route"
            return
        fi
    done
    # main.go wiring: the approval store rides the fabric opt-in (no
    # separate flag), fail-closed load.
    if ! grep -q "SCARLIHQ_AGENTFABRIC_APPROVALS_FILE" "$main_src" || ! grep -q "ConfigureAgentFabricApprovals" "$main_src"; then
        fail "main.go must wire the approval store into the fabric opt-in (SCARLIHQ_AGENTFABRIC_APPROVALS_FILE + ConfigureAgentFabricApprovals)"
        return
    fi
    if ! grep -q "approvals.json" "$main_src"; then
        fail "the approvals file must default to approvals.json NEXT TO the commands file (same directory discipline)"
        return
    fi
    # Compose carries the documented opt-in (env override).
    if ! grep -q "SCARLIHQ_AGENTFABRIC_APPROVALS_FILE" "$compose"; then
        fail "compose must document the approval store override (SCARLIHQ_AGENTFABRIC_APPROVALS_FILE)"
        return
    fi
    # Dashboard: the approvals card, the approve/reject actions, the
    # approvalsPoll refetch and the approval.* WS push filter.
    if ! grep -q 'id="apvcard"' "$dashboard" || ! grep -q 'data-apvapprove' "$dashboard" || ! grep -q 'data-apvreject' "$dashboard"; then
        fail "the dashboard must carry the Operator Approvals card with approve/reject actions"
        return
    fi
    if ! grep -q "function approvalsPoll" "$dashboard" || ! grep -q "function apvDecide" "$dashboard"; then
        fail "the dashboard must poll /api/bar/approvals and POST the operator decision (approvalsPoll + apvDecide)"
        return
    fi
    if ! grep -q "indexOf('approval.')" "$dashboard"; then
        fail "the /ws/bar client must refetch on approval.* change signals (the LIVE CATCH — never fabric.*)"
        return
    fi

    pass "agent fabric approvals: the operator gate (coding acts never execute directly — execute creates the ONE PENDING request, 202), approve runs the act (full BF-3 flow, FRESH evaluation at approve time, audited SKIPPED = APPROVED+SKIPPED, stored params — no re-steering, reconciliation), unconditional terminal reject, PENDING-only-in-flight closed enumeration (no withdrawn), one request per command (load-enforced), submitter/approver attribution labels (never credentials), approvals.json store (fail-closed, atomic, bounded 256), bus approval.* signals, auth-gated routes, additive composed field, opt-in wiring + compose + dashboard"
}

check_27_agent_fabric_closure() {
    echo "--- [27] Agent Fabric phase closure (BF-5): the phase-level coherence gate — Fáza B closes on proof, not promise ---"
    local accept_doc="$REPO_DIR/docs/PHASE_B_ACCEPTANCE.md"
    local readme="$REPO_DIR/README.md"
    local apv_model="$REPO_DIR/scarlihq/internal/agentfabric/approval.go"
    local apv_store="$REPO_DIR/scarlihq/internal/agentfabric/astore.go"
    local exec_store="$REPO_DIR/scarlihq/internal/agentfabric/estore.go"
    local cmd_src="$REPO_DIR/scarlihq/internal/agentfabric/command.go"
    local cmd_store="$REPO_DIR/scarlihq/internal/agentfabric/store.go"
    local ws_store="$REPO_DIR/scarlihq/internal/agentfabric/wstore.go"
    local rt_src="$REPO_DIR/scarlihq/internal/agentfabric/agentruntime.go"
    local api_cmd="$REPO_DIR/scarlihq/internal/api/agentfabric.go"
    local api_appr="$REPO_DIR/scarlihq/internal/api/agentfabric_appr.go"
    local bus_src="$REPO_DIR/scarlihq/internal/agentfabric/fabricbus.go"

    if [[ ! -f "$accept_doc" ]]; then
        fail "docs/PHASE_B_ACCEPTANCE.md missing — the phase cannot close without its acceptance gate (BF-5, v19.5.4)"
        return
    fi
    # The acceptance doc must carry the full genre: the loop table, the
    # phase-level invariants, automated acceptance, the HONEST open-item
    # ledger and the release decision.
    for section in '## 1. What Phase B delivers' '## 2. Phase-level invariants' '## 3. Automated acceptance' '## 4. Honest open items' '## 5. Release decision'; do
        if ! grep -q "$section" "$accept_doc"; then
            fail "the acceptance doc must carry section '$section' (the PHASE3-G1 acceptance genre)"
            return
        fi
    done
    # All five steps are in the loop table, and the honest ledger keeps
    # KPK-06 (hardware acceptance) open + Phase C is named as next.
    for step in '| BF-1 | v19.5.0 |' '| BF-2 | v19.5.1 |' '| BF-3 | v19.5.2 |' '| BF-4 | v19.5.3 |' '| BF-5 | v19.5.4 |'; do
        if ! grep -q "$step" "$accept_doc"; then
            fail "the acceptance loop table must list $step — a step cannot silently vanish at closure"
            return
        fi
    done
    if ! grep -q "KPK-06" "$accept_doc" || ! grep -q "HARDWARE_ACCEPTANCE_PROTOCOL" "$accept_doc"; then
        fail "the closure ledger must keep KPK-06 hardware acceptance OPEN with its protocol reference (CI green ≠ hardware verified)"
        return
    fi
    if ! grep -q "Universal AI Runtime" "$accept_doc"; then
        fail "the acceptance doc must name the next theme (Phase C — Universal AI Runtime; LLM navrhuje, politika rozhoduje)"
        return
    fi

    # README closure surface: changelog row, What's New section, the theme
    # status flip and the COMPLETE line row (the Agent Kernel precedent).
    if ! grep -q '\*\*v19.5.4\*\*' "$readme" || ! grep -q "What's New in v19.5.4" "$readme"; then
        fail "README must carry the v19.5.4 changelog row + What's New section (the closure is a release like any other)"
        return
    fi
    if ! grep -q 'Agent Fabric theme (v19.5.x) is COMPLETE' "$readme"; then
        fail "README theme status must flip to 'Agent Fabric theme (v19.5.x) is COMPLETE' — five merged, tagged, CI-green releases"
        return
    fi
    if ! grep -q '| Agent Fabric line | \*\*COMPLETE\*\* (v19.5.0–v19.5.4) |' "$readme"; then
        fail "README must carry the 'Agent Fabric line | COMPLETE (v19.5.0–v19.5.4)' row (the Agent Kernel line precedent)"
        return
    fi
    if grep -q 'Agent Fabric theme (v19.5.x) is OPEN' "$readme"; then
        fail "README still says the Agent Fabric theme is OPEN — the closure flip is incomplete"
        return
    fi

    # --- Cross-release coherence: the closure re-asserts the phase-level
    # invariants at source level (each was guarded by its own check; the
    # closure check proves they HOLD TOGETHER in one tree).
    # (1) Closed approval enumeration, deliberately NO withdrawn.
    if grep -q "ApprovalWithdrawn\|ApprovalCancelled\|approval.withdrawn" "$apv_model" "$apv_store" "$api_appr" "$bus_src"; then
        fail "a withdrawn/cancelled approval state/signal exists — the enumeration is PENDING/APPROVED/REJECTED (a PENDING request stays answerable)"
        return
    fi
    # (2) The approval boundary IS the runtime boundary (DERIVED, not
    # re-declared) + the closed total runtime mapping validated at load.
    if ! grep -q "func ApprovalRequiredForCapability" "$apv_model" || ! grep -q "func ValidateRuntimeMapping" "$rt_src"; then
        fail "closure requires the DERIVED approval boundary AND the load-validated runtime mapping (the two policies cannot drift)"
        return
    fi
    # (3) All four stores in the tree, grants.json discipline each.
    for store_file in "$cmd_store" "$ws_store" "$exec_store" "$apv_store"; do
        if [[ ! -f "$store_file" ]]; then
            fail "fabric store missing: $store_file (commands/workspaces/executions/approvals — one-file discipline)"
            return
        fi
    done
    if ! grep -q "ErrApprovalFilePermissions" "$apv_store" || ! grep -q "ErrExecutionFilePermissions" "$exec_store"; then
        fail "execution + approval stores must fail-closed on loose permissions (the grants.json discipline across all four stores)"
        return
    fi
    # (4) ONE execution per command + ONE request per command (both
    # load-enforced — the file is the authority).
    if ! grep -q "ErrAlreadyExecuted" "$exec_store" || ! grep -q "ErrAlreadyRequested" "$apv_store"; then
        fail "one-execution-per-command (ErrAlreadyExecuted) and one-request-per-command (ErrAlreadyRequested) must both hold at closure"
        return
    fi
    # (5) Additive schema discipline: commands.json stays v1 — scope,
    # submitter and both composed view fields are omitempty additions.
    if ! grep -q 'workspace_id,omitempty' "$cmd_src" || ! grep -q 'submitter,omitempty' "$cmd_src"; then
        fail "command records must carry the additive omitempty fields (workspace_id, submitter — the commands schema stays v1)"
        return
    fi
    if ! grep -q 'execution,omitempty' "$api_cmd" || ! grep -q 'approval,omitempty' "$api_cmd"; then
        fail "command views must compose the additive execution + approval fields (omitempty — BF-1..BF-3 clients unaffected)"
        return
    fi

    pass "agent fabric phase closure: acceptance gate docs/PHASE_B_ACCEPTANCE.md (loop BF-1..BF-5, phase-level invariants, automated acceptance, honest ledger w/ KPK-06 open, release decision), README closure (changelog + What's New v19.5.4 + theme status COMPLETE + line row), cross-release coherence held together in one tree (no withdrawn, derived boundary + load-validated runtime mapping, all four stores w/ fail-closed discipline, one-execution + one-request per command, additive v1 schema)"
}

# ===========================================================================
# Check 28 — Universal AI Runtime CF-1 foundation (v19.6.0, Phase C opens)
# ===========================================================================
# Source-level contract gate over internal/airuntime — the UNWIRED AK-1
# precedent: the typed foundation is established and tested first; adapters,
# probing and the REST surface are CF-2+, after review. The network/IO scan
# is COMMENT-STRIPPED (v19.6.0): prose that says "no net/http" cannot mask a
# real import — the scan fires on code, not prose (verified negative
# control: a `var nethttpProbe = "net/http"` line injected into the runtime
# source FAILS this check; the BF-4/BF-5 prefix-edit lesson respected — the
# injected symbol is code, so the comment-stripping catches it). The
# cloud-token scan covers the RUNTIME files only: the tests legitimately
# assert cloud-name rejection — they are the proof, not the violation.
_strip_go_comments() {
    awk '
        BEGIN { inblock = 0 }
        {
            line = $0
            out = ""
            i = 1
            n = length(line)
            while (i <= n) {
                two = substr(line, i, 2)
                if (inblock) {
                    if (two == "*/") { inblock = 0; i += 2 } else { i += 1 }
                } else {
                    if (two == "//") { break }
                    if (two == "/*") { inblock = 1; i += 2 } else {
                        out = out substr(line, i, 1)
                        i += 1
                    }
                }
            }
            print out
        }
    ' "$1"
}

check_28_ai_runtime_foundation() {
    echo "--- [28] Universal AI Runtime CF-1 (v19.6.0, Phase C opens): closed local-stack enumeration, fail-closed health/routing, network/IO-free by construction; v19.6.1 (CF-2): the purity scan covers the WHOLE package, the wiring goes through internal/airt ---"
    local pkg="$REPO_DIR/scarlihq/internal/airuntime"
    local providers="$pkg/provider.go"
    local models="$pkg/model.go"
    local health="$pkg/health.go"
    local chat="$pkg/chat.go"
    local router="$pkg/router.go"
    local design_doc="$REPO_DIR/docs/AI_RUNTIME_CF1_DESIGN.md"
    local readme="$REPO_DIR/README.md"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"

    if [[ ! -d "$pkg" ]] || [[ ! -f "$providers" ]]; then
        fail "internal/airuntime missing (CF-1, v19.6.0 — Phase C opens on typed foundations)"
        return
    fi

    # --- The closed provider enumeration: exactly the REAL local stack. ---
    local declared declared_csv
    declared=$(grep -oE 'Provider[A-Za-z]+ +Provider = "[^"]+"' "$providers" | grep -oE '"[^"]+"' | tr -d '"' | sort -u || true)
    declared_csv=$(printf '%s\n' "$declared" | paste -sd, -)
    if [[ "$declared_csv" != "beellama,ollama,sglang,vllm" ]]; then
        fail "provider enumeration must be exactly the closed local stack (sglang/vllm/beellama/ollama), got: ${declared_csv:-none} — the runtime is not a cloud-LLM gateway"
        return
    fi
    # The v19.0.6 release-locked 4-tier failover order, encoded once.
    local order
    order=$(awk '/var DefaultFailoverOrder/,/^}/' "$providers" | grep -oE 'ProviderSGLang|ProviderVLLM|ProviderBeeLlama|ProviderOllama' | paste -sd' ' - || true)
    if [[ "$order" != "ProviderSGLang ProviderVLLM ProviderBeeLlama ProviderOllama" ]]; then
        fail "DefaultFailoverOrder must stay the v19.0.6-locked 4-tier order (SGLang → vLLM → BeeLlama → Ollama) — not a runtime option, not a request parameter"
        return
    fi

    # --- Network/IO-free by construction: the COMMENT-STRIPPED scan over
    # EVERY .go file in the package (v19.6.1: the CF-2 evolution — the scan
    # covers the WHOLE internal/airuntime package, not just the five CF-1
    # files; a future file cannot carry the network in through the back
    # door). Prose cannot mask code; code cannot hide.
    local f code_hits
    while IFS= read -r f; do
        code_hits=$(_strip_go_comments "$f" | grep -nE 'net/http|\b(net|os|exec|syscall)\.|\bsocket\b' || true)
        if [[ -n "$code_hits" ]]; then
            fail "airuntime must be network/IO-free by construction (the WHOLE package, runtime AND tests) — code-level hit in ${f##*/}: $code_hits"
            return
        fi
    done < <(find "$pkg" -maxdepth 1 -name '*.go' -type f | sort)
    # The cloud-token scan covers the RUNTIME files only (tests legitimately
    # assert rejection — they are the proof, not the violation).
    local runtime_files=("$providers" "$models" "$health" "$chat" "$router")
    for f in "${runtime_files[@]}"; do
        code_hits=$(_strip_go_comments "$f" | grep -niE 'openai|anthropic|azure|api[_-]?key|bearer|secret' || true)
        if [[ -n "$code_hits" ]]; then
            fail "airuntime runtime files must carry no cloud-LLM tokens (the runtime normalizes the LOCAL stack) — hit in ${f##*/}: $code_hits"
            return
        fi
    done

    # --- The normalized chat bounds stay explicit constants. ---
    if ! grep -qE 'MaxContentRunes += +4096' "$chat" || ! grep -qE 'MaxMessages += +32' "$chat"; then
        fail "chat contract bounds must stay explicit constants (MaxContentRunes 4096 runes, MaxMessages 32)"
        return
    fi

    # --- Fail-closed markers: health + routing name their rules. ---
    if ! grep -q "UNKNOWN never serves" "$health"; then
        fail "health must encode the fail-closed marker 'UNKNOWN never serves' (trust is earned by observation, not assumed)"
        return
    fi
    if ! grep -q "ErrUnknownModel" "$router" || ! grep -q "the router never guesses a model" "$router"; then
        fail "router must carry ErrUnknownModel + the never-guesses-a-model marker"
        return
    fi
    if ! grep -q "ErrNoRoute" "$router" || ! grep -q "fail-closed, not fail-blind" "$router"; then
        fail "router must carry ErrNoRoute + the skips-return-with-the-error marker (never an empty plan as success)"
        return
    fi

    # --- Purity + charter, encoded in the package doc. The charter must be
    # on ONE comment line (grep cannot match across lines — the v19.6.0
    # lesson; zalomenie = FAIL).
    if ! grep -q "network/IO-free by construction" "$providers"; then
        fail "the package doc must carry the purity marker 'network/IO-free by construction'"
        return
    fi
    if ! grep -q "LLM navrhuje, politika rozhoduje" "$providers"; then
        fail "the Phase C charter must be encoded in the package doc ON ONE LINE (LLM navrhuje, politika rozhoduje)"
        return
    fi

    # --- The design doc: the AK-1 genre + the CF-1..CF-5 sub-phase plan
    # (a step cannot silently vanish — literal ** must be escaped in BRE).
    if [[ ! -f "$design_doc" ]]; then
        fail "docs/AI_RUNTIME_CF1_DESIGN.md missing (CF-1 contract + plan)"
        return
    fi
    local section
    for section in '## 1. Where this fits' '## 2. Sub-phase plan' '## 3. CF-1 contract' '## 4. Non-goals' '## 5. Verification'; do
        if ! grep -q "$section" "$design_doc"; then
            fail "the design doc must carry section '$section' (the AK-1 design genre)"
            return
        fi
    done
    local step
    for step in CF-1 CF-2 CF-3 CF-4 CF-5; do
        if ! grep -q "| \\*\\*${step}\\*\\* |" "$design_doc"; then
            fail "the CF-1..CF-5 sub-phase plan must list ${step} — a step cannot silently vanish"
            return
        fi
    done
    if ! grep -q 'DONE — merged as v19.6.0' "$design_doc"; then
        fail "the CF-1 plan row must be stamped DONE — merged as v19.6.0"
        return
    fi

    # --- README surface: the CURRENT-release assertions move with the
    # release (v19.7.2 now — the check_27 precedent: current surface
    # tracked, historical content stays asserted below); the v19.6.0,
    # v19.6.1, v19.6.2, v19.6.3, v19.6.6, v19.6.7, v19.6.8, v19.7.0 and
    # v19.7.1 historical surfaces must ALSO stay (the changelog rows +
    # the What's New sections are the releases' permanent record).
    if ! grep -q 'ScarLiXoS v19.7.2 · Obsidian Lattice' "$readme" || ! grep -q 'version-v19.7.2-B36B00' "$readme"; then
        fail "README banner/h1/badge must say v19.7.2"
        return
    fi
    if ! grep -q 'Version:\*\* v19.7.2 (Compute Fabric Phase 3 — backend capability contracts + deterministic execution planner; Phases 0-2 stand)' "$readme"; then
        fail "README version line must say v19.7.2 (Compute Fabric Phase 3 — backend capability contracts + deterministic execution planner)"
        return
    fi
    if ! grep -q "\\*\\*v19.7.2\\*\\*" "$readme" || ! grep -q "What's New in v19.7.2" "$readme"; then
        fail "README must carry the v19.7.2 changelog row + What's New section (the current release's own record)"
        return
    fi
    if ! grep -q "\\*\\*v19.7.1\\*\\*" "$readme" || ! grep -q "What's New in v19.7.1" "$readme"; then
        fail "README must carry the v19.7.1 changelog row + What's New section (the permanent record)"
        return
    fi
    if ! grep -q '\*\*v19.6.0\*\*' "$readme" || ! grep -q "What's New in v19.6.0" "$readme"; then
        fail "README must carry the v19.6.0 changelog row + What's New section"
        return
    fi
    if ! grep -q '\*\*v19.6.1\*\*' "$readme" || ! grep -q "What's New in v19.6.1" "$readme"; then
        fail "README must carry the v19.6.1 changelog row + What's New section (the permanent record)"
        return
    fi
    if ! grep -q '\*\*v19.6.2\*\*' "$readme" || ! grep -q "What's New in v19.6.2" "$readme"; then
        fail "README must carry the v19.6.2 changelog row + What's New section (the permanent record)"
        return
    fi
    if ! grep -q '\*\*v19.6.3\*\*' "$readme" || ! grep -q "What's New in v19.6.3" "$readme"; then
        fail "README must carry the v19.6.3 changelog row + What's New section (the permanent record)"
        return
    fi
    if ! grep -q 'Universal AI Runtime theme (v19.6.x) is OPEN' "$readme"; then
        fail "README theme status must flip to 'Universal AI Runtime theme (v19.6.x) is OPEN'"
        return
    fi
    if grep -q 'Universal AI Runtime theme (v19.6.x) is COMPLETE' "$readme"; then
        fail "README claims the Universal AI Runtime theme is COMPLETE — the theme just OPENED (CF-1 only)"
        return
    fi
    if ! grep -q '| Universal AI Runtime foundation (CF-1) | \*\*TESTED\*\* (v19.6.0) |' "$readme"; then
        fail "README must carry the 'Universal AI Runtime foundation (CF-1) | TESTED (v19.6.0)' roadmap row"
        return
    fi
    if ! grep -q 'One-command install — v19.6.8 Compute Fabric Phase 0 (lifecycle vocabulary, canonical architecture)' "$readme"; then
        fail "README install header must be refreshed to v19.6.8 (the current-release surface — it drifted at v19.4.6 once)"
        return
    fi
    # v19.6.3 (KPK-19.6.1-P2-01, the audit finding): the install PARAGRAPH
    # must name the CURRENT release too — the v19.6.1 audit caught the
    # one-liner still saying 'clones the current v19.6.0 mainline' at a
    # 19.6.1+ tag. The header alone is not enough; the drift guard is
    # literal and moves with every release.
    if ! grep -q 'current v19.6.8 mainline' "$readme"; then
        fail "README install paragraph must say 'clones the current v19.6.8 mainline' (KPK-19.6.1-P2-01: the one-liner drifts when the header is bumped but the paragraph is not)"
        return
    fi
    if ! grep -q "\\*\\*v19.6.4\\*\\*" "$readme" || ! grep -q "What's New in v19.6.4" "$readme"; then
        fail "README must carry the v19.6.4 changelog row + What's New section (the permanent record)"
        return
    fi
    if ! grep -q "\\*\\*v19.6.5\\*\\*" "$readme" || ! grep -q "What's New in v19.6.5" "$readme"; then
        fail "README must carry the v19.6.5 changelog row + What's New section (the permanent record)"
        return
    fi
    if ! grep -q "\\*\\*v19.6.7\\*\\*" "$readme" || ! grep -q "What's New in v19.6.7" "$readme"; then
        fail "README must carry the v19.6.7 changelog row + What's New section (the permanent record)"
        return
    fi
    if ! grep -q "\\*\\*v19.6.8\\*\\*" "$readme" || ! grep -q "What's New in v19.6.8" "$readme"; then
        fail "README must carry the v19.6.8 changelog row + What's New section (the current release's own record)"
        return
    fi
    if ! grep -q "\\*\\*v19.6.6\\*\\*" "$readme" || ! grep -q "What's New in v19.6.6" "$readme"; then
        fail "README must carry the v19.6.6 changelog row + What's New section (the permanent record)"
        return
    fi

    # --- The wiring contract, evolved with CF-2 (v19.6.1): CF-1 was UNWIRED
    # by design (AK-1 precedent); CF-2 is the reviewed act that wires it —
    # THROUGH internal/airt, never inside the contract package. The successor
    # truth: main.go/rest.go/compose may carry airuntime references ONLY via
    # the airt import/env (the transport owns the network); the contract
    # package itself must not be imported for anything but its types (it has
    # no network to offer). check_29 asserts the transport side.
    if ! grep -q 'internal/airt' "$main_src" || ! grep -q 'internal/airt' "$rest_src"; then
        fail "CF-2 wires the runtime THROUGH internal/airt — main.go/rest.go must import it (the transport layer is the only path to the contract)"
        return
    fi

    pass "universal AI runtime CF-1: closed local-stack enumeration (4 providers, v19.0.6-locked failover order encoded once), bounded explicit model registry (no auto-discovery), fail-closed observed health (UNKNOWN never serves, monotone observation time), normalized chat bounds (4096 runes / 32 messages / no error finish), pure deterministic router w/ ErrUnknownModel + ErrNoRoute (never guesses, never executes), network/IO-free by construction — the v19.6.1 evolution: the comment-stripped scan covers the WHOLE package (every .go file, runtime AND tests), cloud-token scan clean over runtime files, charter on one line, design doc + CF-1..CF-5 plan, README current-release surface v19.6.5 (banner/badge/version-line/install header + the v19.6.5 mainline drift guard, KPK-19.6.1-P2-01) + historical v19.6.0/v19.6.1/v19.6.2/v19.6.3 surfaces (changelog + What's New + theme OPEN + roadmap TESTED), wiring contract evolved: CF-2 wires THROUGH internal/airt (the transport owns the network, the contract stays pure)"
}

# ===========================================================================
# check_29 — Universal AI Runtime CF-2 (v19.6.1): the transport layer
# ===========================================================================
check_29_ai_runtime_adapters() {
    echo "--- [29] Universal AI Runtime CF-2 (v19.6.1, Phase C): real adapters + health probing + the auth-gated REST surface (opt-in) BEHIND the pure contract — internal/airt ---"
    local pkg="$REPO_DIR/scarlihq/internal/airt"
    local endpoints="$pkg/endpoints.go"
    local wire="$pkg/wire.go"
    local adapter="$pkg/adapter.go"
    local prober="$pkg/prober.go"
    local runtime="$pkg/runtime.go"
    local airt_doc="$pkg/airt.go"
    local transport="$pkg/transport.go"
    local design_doc="$REPO_DIR/docs/AI_RUNTIME_CF2_DESIGN.md"
    local readme="$REPO_DIR/README.md"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local api_src="$REPO_DIR/scarlihq/internal/api/airuntime.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"

    if [[ ! -d "$pkg" ]] || [[ ! -f "$endpoints" ]]; then
        fail "internal/airt missing (CF-2, v19.6.1 — the transport layer behind the contract)"
        return
    fi
    local f
    for f in "$wire" "$adapter" "$prober" "$runtime" "$airt_doc" "$transport"; do
        if [[ ! -f "$f" ]]; then
            fail "airt source missing: $f"
            return
        fi
    done

    # --- The separation invariant, BOTH directions: the contract package
    # stays pure (check 28 scans it whole); the transport package IMPORTS
    # the contract (it serves the contract, it never copies it).
    if ! grep -q 'internal/airuntime' "$runtime"; then
        fail "internal/airt must compose the CF-1 contract (import internal/airuntime) — the transport serves the contract, it never re-implements it"
        return
    fi
    # And the transport owns the network: net/http lives HERE (that is its
    # purpose), but NOTHING LISTENS in it — no server surface, dial-out only.
    local listener_hits
    listener_hits=$(cat "$adapter" "$prober" "$runtime" "$wire" "$endpoints" "$transport" | grep -nE 'ListenAndServe|http\.Server|net\.Listen' || true)
    if [[ -n "$listener_hits" ]]; then
        fail "internal/airt must not listen (dial-out transport only — /api/ai/* rides the existing ScarliHQ listener): $listener_hits"
        return
    fi

    # --- The charter, on ONE line in the package doc (the check_28 lesson:
    # grep cannot match across lines — zalomenie = FAIL).
    if ! grep -q "LLM navrhuje, politika rozhoduje" "$airt_doc"; then
        fail "the airt package doc must carry the Phase C charter ON ONE LINE (LLM navrhuje, politika rozhoduje)"
        return
    fi

    # --- The API-map endpoints, release-locked: the four documented
    # addresses of the real local stack.
    local port
    for port in '127.0.0.1:30000' '127.0.0.1:8089' '127.0.0.1:11438' '127.0.0.1:11435'; do
        if ! grep -q "http://$port" "$endpoints"; then
            fail "DefaultEndpoints must carry the API-map address $port (the documented local stack, release-locked like DefaultFailoverOrder)"
            return
        fi
    done
    # --- The cloud-gateway guard, BY NAME: the dotted-host rejection and
    # the plain-HTTP rule are contract markers, not accidents.
    if ! grep -q "not a cloud-LLM gateway" "$endpoints"; then
        fail "the endpoint validation must NAME the cloud-gateway rule ('not a cloud-LLM gateway' — dotted hosts rejected by design)"
        return
    fi
    if ! grep -q "plain HTTP" "$endpoints"; then
        fail "the endpoint validation must NAME the plain-HTTP rule (the local stack carries no TLS)"
        return
    fi
    # The env override surface: the four documented variables.
    if ! grep -q 'SCARLIHQ_AIRUNTIME_SGLANG_URL' "$endpoints" || ! grep -q 'SCARLIHQ_AIRUNTIME_BEELLAMA_URL' "$endpoints"; then
        fail "EndpointsFromEnv must carry the documented override variables (SCARLIHQ_AIRUNTIME_*_URL)"
        return
    fi

    # --- The wire discipline: unknown finish reasons are transport
    # failures, never coerced; stream is always false.
    if ! grep -q "never coerced" "$wire"; then
        fail "the wire decode must carry the never-coerced marker (unknown finish reasons are transport failures)"
        return
    fi
    if ! grep -q "Stream   bool" "$wire" && ! grep -q "Stream bool" "$wire"; then
        fail "the wire request must carry the stream flag (CF-2 chat is request/response — a streaming surface would be a separate reviewed act)"
        return
    fi

    # --- The prober honesty matrix, stated in the source.
    if ! grep -q "standing-but-unready server is DEGRADED, a dead one is UNAVAILABLE" "$prober"; then
        fail "the prober must state the honesty matrix on one line (a standing-but-unready server is DEGRADED, a dead one is UNAVAILABLE)"
        return
    fi
    if ! grep -q '/api/tags' "$prober"; then
        fail "the prober must ask ollama on /api/tags (the documented health surface — its healthcheck is 'ollama list')"
        return
    fi

    # --- The executor: attribution + fail-closed markers.
    if ! grep -q "attribution survives failover" "$runtime"; then
        fail "the executor must carry the attribution marker (attribution survives failover)"
        return
    fi
    if ! grep -q "ErrAllAdaptersFailed" "$runtime"; then
        fail "the executor must carry ErrAllAdaptersFailed (every step failed — the attempts return alongside)"
        return
    fi
    if ! grep -q "fail-closed, not fail-blind" "$runtime"; then
        fail "the executor must carry the fail-closed-not-fail-blind marker"
        return
    fi

    # --- The REST surface: all five routes behind the standard auth gate.
    for route in '/api/ai/health' '/api/ai/models' '/api/ai/probe' '/api/ai/chat'; do
        if ! grep -q "\"$route\"" "$rest_src"; then
            fail "the REST surface must register $route (Bearer-gated like every route)"
            return
        fi
    done
    if ! grep -q 'SCARLIHQ_AIRUNTIME_ENABLED' "$api_src"; then
        fail "the API surface must document the opt-in (SCARLIHQ_AIRUNTIME_ENABLED=true — 503 without it)"
        return
    fi

    # --- The opt-in wiring in main.go + the compose env block.
    if ! grep -q 'SCARLIHQ_AIRUNTIME_ENABLED' "$main_src"; then
        fail "main.go must carry the SCARLIHQ_AIRUNTIME_ENABLED opt-in (fail-closed wiring, the compute/kernel/fabric precedent)"
        return
    fi
    if ! grep -q 'ConfigureAIRuntime' "$main_src"; then
        fail "main.go must ConfigureAIRuntime (the opt-in attaches the runtime; nil is rejected — no half-configured surface)"
        return
    fi
    if ! grep -q 'SCARLIHQ_AIRUNTIME_ENABLED=true' "$compose"; then
        fail "the compose file must document the CF-2 opt-in env block (SCARLIHQ_AIRUNTIME_ENABLED=true + the _URL overrides + _MODELS + _PROBE_INTERVAL)"
        return
    fi

    # --- The design doc: the genre + the plan stamped.
    if [[ ! -f "$design_doc" ]]; then
        fail "docs/AI_RUNTIME_CF2_DESIGN.md missing (CF-2 contract + plan)"
        return
    fi
    local section
    for section in '## 1. Where this fits' '## 2. Sub-phase plan' '## 3. CF-2 contract' '## 4. Non-goals' '## 5. Verification'; do
        if ! grep -q "$section" "$design_doc"; then
            fail "the CF-2 design doc must carry section '$section' (the CF-1 design genre)"
            return
        fi
    done
    if ! grep -q 'DONE — merged as v19.6.1' "$design_doc"; then
        fail "the CF-2 plan row must be stamped DONE — merged as v19.6.1"
        return
    fi

    # --- README v19.6.1 surface: the release is a release like any other.
    if ! grep -q "What's New in v19.6.1" "$readme" || ! grep -q '\*\*v19.6.1\*\*' "$readme"; then
        fail "README must carry the v19.6.1 changelog row + What's New section"
        return
    fi
    if ! grep -q '| Universal AI Runtime adapters + REST (CF-2) | \*\*TESTED\*\* (v19.6.1) |' "$readme"; then
        fail "README must carry the 'Universal AI Runtime adapters + REST (CF-2) | TESTED (v19.6.1)' roadmap row"
        return
    fi
    if ! grep -q 'Universal AI Runtime theme (v19.6.x) is OPEN' "$readme"; then
        fail "README theme status must stay OPEN (CF-2 is the second of five steps — the theme is not COMPLETE)"
        return
    fi

    # --- The cloud-token scan over the airt RUNTIME files: the wire format
    # is named in PROSE (comment-stripped away); a real cloud endpoint,
    # cloud-vendor token or secret must not exist in code.
    local code_hits
    for f in "$endpoints" "$wire" "$adapter" "$prober" "$runtime" "$airt_doc" "$transport"; do
        code_hits=$(_strip_go_comments "$f" | grep -niE 'anthropic|azure|openai\.(com|org)|api\.openai|api[_-]?key|bearer|secret' || true)
        if [[ -n "$code_hits" ]]; then
            fail "airt runtime files must carry no cloud-LLM endpoints or tokens in code — hit in ${f##*/}: $code_hits"
            return
        fi
    done

    pass "universal AI runtime CF-2: the transport layer BEHIND the pure contract (internal/airt imports internal/airuntime, the contract stays pure — check 28 scans it whole), closed endpoint shape w/ the API-map defaults (30000/8089/11438/11435) + the cloud-gateway guard BY NAME (dotted hosts, https, userinfo, paths, implicit ports rejected — invalid env overrides abort, never a silent fall-back), LocalAdapter per tier over the OpenAI-compatible wire w/ never-coerced finish reasons + stream:false + 30s/1MiB bounds, the prober honesty matrix (2xx HEALTHY / standing-but-unready DEGRADED-serves / dead UNAVAILABLE / never-probed UNKNOWN; /health + /api/tags; concurrent tier-ordered rounds), the failover executor (pure CF-1 router replanned per request, attribution survives failover, ErrAllAdaptersFailed carries the attempts — fail-closed not fail-blind), no listener in the transport (dial-out only), the REST surface /api/ai/health|models|probe|chat Bearer-gated + 503-until-opt-in, main.go + compose wiring (SCARLIHQ_AIRUNTIME_ENABLED/_URL/_MODELS/_PROBE_INTERVAL), design doc genre + CF-2 stamped DONE, README v19.6.1 surface, cloud-token scan clean"
}

# ===========================================================================
# check_30 — Universal AI Runtime CF-3 (v19.6.2): the propose surface
# ===========================================================================
check_30_ai_propose_surface() {
    echo "--- [30] Universal AI Runtime CF-3 (v19.6.2, Phase C): the propose surface — an LLM response becomes a fabric PROPOSAL, never an auto-submitted command (internal/agentfabric proposal.go/pstore.go + internal/api/aipropose.go) ---"
    local pkg="$REPO_DIR/scarlihq/internal/agentfabric"
    local proposal="$pkg/proposal.go"
    local pstore="$pkg/pstore.go"
    local command_src="$pkg/command.go"
    local store_src="$pkg/store.go"
    local fabricbus="$pkg/fabricbus.go"
    local api_src="$REPO_DIR/scarlihq/internal/api/aipropose.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local design_doc="$REPO_DIR/docs/AI_RUNTIME_CF3_DESIGN.md"
    local readme="$REPO_DIR/README.md"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"

    if [[ ! -f "$proposal" ]] || [[ ! -f "$pstore" ]]; then
        fail "internal/agentfabric proposal model/store missing (CF-3, v19.6.2 — the propose surface's persisted entity)"
        return
    fi
    local f
    for f in "$api_src" "$design_doc"; do
        if [[ ! -f "$f" ]]; then
            fail "CF-3 source missing: $f"
            return
        fi
    done

    # --- The charter, on ONE line in the package doc (the check_28/29 lesson:
    # grep cannot match across lines — zalomenie = FAIL).
    if ! grep -q "never an auto-submitted command" "$proposal"; then
        fail "the proposal package doc must carry the CF-3 charter ON ONE LINE (an LLM response becomes a fabric PROPOSAL — never an auto-submitted command)"
        return
    fi
    if ! grep -q "LLM navrhuje, politika rozhoduje" "$proposal"; then
        fail "the proposal package doc must carry the Phase C charter ON ONE LINE (LLM navrhuje, politika rozhoduje)"
        return
    fi

    # --- The fence tag constant: the model's ONE structured freedom.
    if ! grep -q 'ProposalFenceTag = "```scarlix-proposal"' "$proposal"; then
        fail "the parser must carry the fenced-block tag constant (ProposalFenceTag = the scarlix-proposal fence — column 0, the closed three-field shape)"
        return
    fi
    # --- The closed state enumeration + the no-withdrawn marker (the BF-4
    # genre applied to model intents).
    for state in 'ProposalPending ProposalState = "PENDING"' 'ProposalAccepted ProposalState = "ACCEPTED"' 'ProposalDismissed ProposalState = "DISMISSED"'; do
        if ! grep -q "$state" "$proposal"; then
            fail "the closed proposal enumeration must carry $state"
            return
        fi
    done
    if ! grep -q "NO withdrawn state and NO expiry" "$proposal"; then
        fail "the proposal lifecycle must NAME the no-withdrawn/no-expiry rule (a PENDING proposal stays answerable — an expiry would turn a non-decision into a decision)"
        return
    fi
    # --- The parser discipline markers: the honest null, the malformed
    # refusal, never a guess.
    if ! grep -q "ErrNoProposal" "$proposal" || ! grep -q "ErrMalformedProposal" "$proposal"; then
        fail "the parser must carry ErrNoProposal (the honest null) AND ErrMalformedProposal (the fail-closed refusal)"
        return
    fi
    if ! grep -q "the parser never fills in a blank agent or" "$proposal"; then
        fail "the parser must NAME the never-guesses rule (no defaults — never fills in a blank agent or guesses a capability)"
        return
    fi
    if ! grep -q "ambiguity is never resolved by guessing" "$proposal"; then
        fail "the parser must NAME the two-blocks rule (an example plus a real proposal is ambiguity — never resolved by guessing)"
        return
    fi
    # --- The bar's own validators: no second, weaker standard for
    # model-authored intents.
    if ! grep -q "no second, weaker standard" "$proposal" && ! grep -q "same standard as a bar submission" "$proposal"; then
        fail "the proposal validation must NAME the bar's-own-validators rule (no second, weaker standard for model-authored intents)"
        return
    fi

    # --- The store discipline markers: proposals.json, schema v1, bounded,
    # never deleted, one-command-reference-per-proposal enforced twice.
    if ! grep -q 'DefaultMaxProposals = 256' "$pstore"; then
        fail "the proposal store must carry the bounded bound (DefaultMaxProposals = 256)"
        return
    fi
    if ! grep -q 'proposalsFileVersion = 1' "$pstore"; then
        fail "the proposals file must carry the explicit schema version 1 (unknown versions rejected — explicit migration required)"
        return
    fi
    if ! grep -q "Proposals are never deleted" "$pstore"; then
        fail "the proposal store must NAME the never-deleted rule (the file is the reviewable history)"
        return
    fi
    if ! grep -q "one command reference per proposal" "$pstore" && ! grep -q "one command per proposal, ever" "$pstore"; then
        fail "the proposal store must NAME the one-command-per-proposal invariant (enforced on the mutation side AND at load)"
        return
    fi

    # --- The additive linkage on the command: schema v1 unchanged (the BF-4
    # submitter precedent — omitempty, old binaries unaffected).
    if ! grep -q 'ProposalID  string                `json:"proposal_id,omitempty"`' "$command_src" && ! grep -q 'ProposalID.*json:"proposal_id,omitempty"' "$command_src"; then
        fail "CommandRecord must carry the additive proposal linkage (proposal_id omitempty — the commands schema stays v1)"
        return
    fi
    # --- The durable linkage + the reconciliation lookup on the store side.
    if ! grep -q "func (s \*CommandStore) GetByProposal" "$store_src"; then
        fail "the command store must carry GetByProposal (the durable linkage lookup — accept's crash reconciliation)"
        return
    fi
    if ! grep -q "func (s \*CommandStore) SubmitWithProposal" "$store_src"; then
        fail "the command store must carry SubmitWithProposal (the accept path's capture — the linkage rides the record)"
        return
    fi
    # --- The load-side duplicate-linkage refusal (corruption, never 'some
    # proposals work').
    if ! grep -q "is referenced TWICE" "$store_src"; then
        fail "the command store Load must refuse a duplicate proposal linkage BY NAME (one command per proposal, ever — fail-closed)"
        return
    fi

    # --- The bus signals: proposal.captured / proposal.decided join the
    # CHANGE namespace.
    if ! grep -q 'SignalProposalCaptured SignalType = "proposal.captured"' "$fabricbus" || ! grep -q 'SignalProposalDecided SignalType = "proposal.decided"' "$fabricbus"; then
        fail "the fabric bus must carry proposal.captured + proposal.decided (the CHANGE namespace, extended by two names)"
        return
    fi

    # --- The REST surface: all four routes behind the standard auth gate.
    for route in '/api/ai/propose' '/api/ai/proposals' '/api/ai/proposals/accept' '/api/ai/proposals/dismiss'; do
        if ! grep -q "\"$route\"" "$rest_src"; then
            fail "the REST surface must register $route (Bearer-gated like every route)"
            return
        fi
    done
    # --- The both-opt-ins fail-closed gate + the reconciliation + the
    # never-an-auto-submitted-command markers in the API layer.
    if ! grep -q "SCARLIHQ_AIRUNTIME_ENABLED=true" "$api_src" || ! grep -q "SCARLIHQ_AGENTFABRIC_ENABLED=true" "$api_src"; then
        fail "the propose API must document BOTH opt-ins (SCARLIHQ_AIRUNTIME_ENABLED + SCARLIHQ_AGENTFABRIC_ENABLED — 503 without either)"
        return
    fi
    if ! grep -q "GetByProposal" "$api_src"; then
        fail "the accept handler must call the reconciliation lookup (GetByProposal — no intent is captured twice)"
        return
    fi
    if ! grep -q "never an auto-submitted command" "$api_src"; then
        fail "the propose API doc must carry the CF-3 charter (never an auto-submitted command)"
        return
    fi
    if ! grep -q "StatusUnprocessableEntity" "$api_src"; then
        fail "the propose handler must answer 422 on a malformed proposal block (fail-closed, nothing captured)"
        return
    fi

    # --- The wiring: the proposal store rides the fabric opt-in.
    if ! grep -q 'SCARLIHQ_AGENTFABRIC_PROPOSALS_FILE' "$main_src"; then
        fail "main.go must carry the SCARLIHQ_AGENTFABRIC_PROPOSALS_FILE override (proposals.json next to commands.json — the fabric opt-in, the approvals precedent)"
        return
    fi
    if ! grep -q 'ConfigureAgentFabricProposals' "$main_src"; then
        fail "main.go must ConfigureAgentFabricProposals (the fabric opt-in attaches the proposal store — nil is rejected)"
        return
    fi
    if ! grep -q 'SCARLIHQ_AGENTFABRIC_PROPOSALS_FILE' "$compose"; then
        fail "the compose file must document the proposal store override (SCARLIHQ_AGENTFABRIC_PROPOSALS_FILE + the both-opt-ins note)"
        return
    fi

    # --- The design doc: the genre + the plan stamped.
    local section
    for section in '## 1. Where this fits' '## 2. The contract' '## 3. Honest non-goals' '## 4. Verification'; do
        if ! grep -q "$section" "$design_doc"; then
            fail "the CF-3 design doc must carry section '$section' (the CF-1/CF-2 design genre)"
            return
        fi
    done
    if ! grep -q 'DONE — merged as v19.6.2' "$REPO_DIR/docs/AI_RUNTIME_CF1_DESIGN.md"; then
        fail "the CF-1 plan table must stamp CF-3 DONE — merged as v19.6.2"
        return
    fi

    # --- README v19.6.2 surface: the release is a release like any other.
    if ! grep -q "What's New in v19.6.2" "$readme" || ! grep -q '\*\*v19.6.2\*\*' "$readme"; then
        fail "README must carry the v19.6.2 changelog row + What's New section"
        return
    fi
    if ! grep -q 'Universal AI Runtime propose surface (CF-3) | \*\*TESTED\*\* (v19.6.2)' "$readme"; then
        fail "README must carry the 'Universal AI Runtime propose surface (CF-3) | TESTED (v19.6.2)' roadmap row"
        return
    fi
    if ! grep -q 'Universal AI Runtime theme (v19.6.x) is OPEN' "$readme"; then
        fail "README theme status must stay OPEN (CF-3 is the third of five steps — the theme is not COMPLETE)"
        return
    fi

    pass "universal AI runtime CF-3: the propose surface (an LLM response becomes a fabric PROPOSAL — never an auto-submitted command): the pure parser (the scarlix-proposal fence at column 0, the closed three-field shape, the bar's own validators, the honest null / the 422 malformed refusal w/ attribution + content, never a guess), the proposal store (proposals.json — the astore discipline: schema v1, 0600/0700, atomic+rollback, bounded 256, never deleted, one-command-per-proposal enforced twice), accept = the ONLY door to a command (the BF-1 SubmitWithProposal path, the DURABLE additive proposal_id linkage — commands schema stays v1, the reconciliation GetByProposal — no intent captured twice), dismiss unconditional + terminal, the closed PENDING/ACCEPTED/DISMISSED enumeration (no withdrawn, no expiry), the four Bearer-gated routes w/ BOTH-opt-ins fail-closed, bus proposal.captured/decided signals, main.go + compose wiring, design doc genre + CF-3 stamped DONE, README v19.6.2 surface"
}


# ===========================================================================
# check_31 — Universal AI Runtime transport hardening (v19.6.3, KPK-01/KPK-02):
# the local-only client (no redirects, no ambient proxy, dial-guarded) and
# the end-to-end timeout contract — the v19.6.1 KpK audit remediation
# ===========================================================================
check_31_ai_transport_boundary() {
    echo "--- [31] Universal AI Runtime transport hardening (v19.6.3, KPK-01/KPK-02 remediation): the local-only HTTP client (no redirects, no ambient proxy, dial-guarded) + the end-to-end chat budget (504 on expiry, write path extended beyond it) ---"
    local pkg="$REPO_DIR/scarlihq/internal/airt"
    local transport="$pkg/transport.go"
    local adapter="$pkg/adapter.go"
    local prober="$pkg/prober.go"
    local runtime="$pkg/runtime.go"
    local airt_doc="$pkg/airt.go"
    local endpoints="$pkg/endpoints.go"
    local wire="$pkg/wire.go"
    local api_src="$REPO_DIR/scarlihq/internal/api/airuntime.go"
    local propose_src="$REPO_DIR/scarlihq/internal/api/aipropose.go"
    local api_test="$REPO_DIR/scarlihq/internal/api/airuntime_test.go"
    local transport_test="$pkg/transport_test.go"
    local runtime_test="$pkg/runtime_test.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"
    local secmap="$REPO_DIR/docs/SCARLIX_SECURITY_MAP.md"
    local readme="$REPO_DIR/README.md"
    local notes="$REPO_DIR/docs/RELEASE_NOTES_v19.6.3.md"

    if [[ ! -f "$transport" ]]; then
        fail "internal/airt/transport.go missing (v19.6.3 — the local-only client constructor, KPK-01)"
        return
    fi

    # --- KPK-01: the no-redirect policy, BY MARKER IN CODE (comment-
    # stripped — prose cannot mask code, the check_28 lesson): a 3xx is
    # handed back as the response and classified as the failure it is —
    # Go's default client FOLLOWS redirects and would replay the POST
    # body, the whole private conversation, onto whatever Location a
    # compromised tier answers; the endpoint allowlist constrains the
    # CONFIGURED destination, never a redirect destination.
    local code_hits
    code_hits=$(_strip_go_comments "$transport" | grep -c 'http\.ErrUseLastResponse' || true)
    if [[ "$code_hits" -eq 0 ]]; then
        fail "the local-only client must refuse redirects BY POLICY (http.ErrUseLastResponse in transport.go CODE — KPK-01: the redirect boundary is a confidentiality boundary, not a stylistic concern)"
        return
    fi
    # --- KPK-01: no ambient proxy inheritance (Proxy: nil — HTTP_PROXY /
    # HTTPS_PROXY never steer local AI traffic; proxy support is not a
    # feature of this transport and cannot become one by accident). Also
    # comment-stripped: the marker must live in the CODE.
    code_hits=$(_strip_go_comments "$transport" | grep -cE 'Proxy: *nil' || true)
    if [[ "$code_hits" -eq 0 ]]; then
        fail "the local-only transport must carry an explicit 'Proxy: nil' in CODE (ambient proxy env never steers local AI traffic — KPK-01)"
        return
    fi
    # --- KPK-01: the dial guard (defense in depth — hostname syntax at the
    # endpoint layer does not prove where a connection actually goes; the
    # dial layer does). The guard's refusal text is the contract marker.
    if ! grep -q "the local-only transport dials loopback and the configured endpoint hosts, nothing else" "$transport"; then
        fail "the local-only transport must carry the dial guard w/ its named refusal (loopback + the configured endpoint hosts, nothing else — KPK-01 defense in depth)"
        return
    fi
    # --- The ONE constructor discipline: the whole package builds its
    # http.Client in EXACTLY ONE place (transport.go) — an http.Client
    # anywhere else in the airt runtime files would be a second, unaudited
    # client shape. Comment-stripped: the count must see CODE only.
    local client_count
    client_count=$(for f in "$transport" "$adapter" "$prober" "$runtime" "$airt_doc" "$endpoints" "$wire"; do _strip_go_comments "$f"; done | grep -c 'http\.Client{' || true)
    if [[ "$client_count" != "1" ]]; then
        fail "internal/airt must construct http.Client in EXACTLY ONE place (the shared local-only constructor, transport.go) — found $client_count constructions across the runtime files (KPK-01: one constructor, closed by construction)"
        return
    fi
    # --- Both consumers ride the constructor: adapter AND prober (in
    # CODE, comment-stripped).
    code_hits=$(_strip_go_comments "$adapter" | grep -c 'newLocalClient' || true)
    local prober_hits
    prober_hits=$(_strip_go_comments "$prober" | grep -c 'newLocalClient' || true)
    if [[ "$code_hits" -eq 0 || "$prober_hits" -eq 0 ]]; then
        fail "the adapter and the prober must both build their clients through newLocalClient (the local-only shape is not optional for either — KPK-01)"
        return
    fi

    # --- KPK-02: the budget arithmetic + the named upper bound (in CODE,
    # comment-stripped).
    code_hits=$(_strip_go_comments "$transport" | grep -cF 'MaxAdapterTimeout = 300 * time.Second' || true)
    if [[ "$code_hits" -eq 0 ]]; then
        fail "the per-attempt timeout must carry the named upper bound (MaxAdapterTimeout = 300s — a bounded number, never an unbounded one)"
        return
    fi
    code_hits=$(_strip_go_comments "$adapter" | grep -c 'func ChatBudget' || true)
    if [[ "$code_hits" -eq 0 ]]; then
        fail "the end-to-end budget must be defined ONCE (airt.ChatBudget — per-attempt x the closed four-tier set, KPK-02)"
        return
    fi
    # --- KPK-02: the executor reports the expiry BY NAME (a deliberate
    # timeout, never a socket write deadline artifact, never a generic
    # all-failed masquerade). In CODE, comment-stripped.
    code_hits=$(_strip_go_comments "$airt_doc" | grep -c 'ErrBudgetExpired' || true)
    local runtime_hits
    runtime_hits=$(_strip_go_comments "$runtime" | grep -c 'ErrBudgetExpired' || true)
    if [[ "$code_hits" -eq 0 || "$runtime_hits" -eq 0 ]]; then
        fail "the executor must carry ErrBudgetExpired (the named end-to-end budget expiry — airt.go declares it, runtime.go reports it; KPK-02)"
        return
    fi

    # --- KPK-02: the REST surface — the deliberate 504, the budget
    # context, the write path extended beyond the budget (BOTH handlers:
    # /api/ai/chat AND /api/ai/propose ride the same contract). In CODE,
    # comment-stripped — prose cannot mask a missing mapping.
    code_hits=$(_strip_go_comments "$api_src" | grep -c 'ErrBudgetExpired' || true)
    local propose_hits
    propose_hits=$(_strip_go_comments "$propose_src" | grep -c 'ErrBudgetExpired' || true)
    if [[ "$code_hits" -eq 0 || "$propose_hits" -eq 0 ]]; then
        fail "both AI chat handlers must map ErrBudgetExpired (chat AND propose — the budget is one contract on both surfaces; KPK-02)"
        return
    fi
    code_hits=$(_strip_go_comments "$api_src" | grep -c 'StatusGatewayTimeout' || true)
    propose_hits=$(_strip_go_comments "$propose_src" | grep -c 'StatusGatewayTimeout' || true)
    if [[ "$code_hits" -eq 0 || "$propose_hits" -eq 0 ]]; then
        fail "budget expiry must answer the deliberate 504 on both surfaces (StatusGatewayTimeout — never a generic 502 masquerade; KPK-02)"
        return
    fi
    code_hits=$(_strip_go_comments "$api_src" | grep -c 'SetWriteDeadline' || true)
    propose_hits=$(_strip_go_comments "$propose_src" | grep -c 'SetWriteDeadline' || true)
    if [[ "$code_hits" -eq 0 || "$propose_hits" -eq 0 ]]; then
        fail "the AI write path must extend the per-request write deadline beyond the budget (http.ResponseController SetWriteDeadline — a legitimate slow local generation is WRITTEN, never cut by the 30s server default; KPK-02)"
        return
    fi
    code_hits=$(_strip_go_comments "$api_src" | grep -c 'ChatBudget' || true)
    if [[ "$code_hits" -eq 0 ]]; then
        fail "the API layer must impose the runtime's chat budget as the request context deadline (KPK-02 — one coherent end-to-end number)"
        return
    fi

    # --- The operator surface: the tunable per-attempt timeout, validated
    # fail-closed at startup (the same env discipline as every surface).
    # Comment-stripped on main.go too: the wiring must live in CODE.
    local env_hits bound_hits budget_hits
    env_hits=$(_strip_go_comments "$main_src" | grep -c 'SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT' || true)
    bound_hits=$(_strip_go_comments "$main_src" | grep -c 'MaxAdapterTimeout' || true)
    budget_hits=$(_strip_go_comments "$main_src" | grep -c 'ChatBudget' || true)
    if [[ "$env_hits" -eq 0 || "$bound_hits" -eq 0 || "$budget_hits" -eq 0 ]]; then
        fail "main.go must wire SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT (validated within MaxAdapterTimeout) and construct the runtime with airt.ChatBudget (KPK-02 — fail-closed env, explicit budget)"
        return
    fi
    if ! grep -q 'SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT' "$compose"; then
        fail "the compose env block must document SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT (the operator surface is documented where the operator reads it)"
        return
    fi

    # --- The regression PROOFS exist as tests (the proof, not the claim):
    # redirect refusal (307/308, zero requests + zero prompt bytes at the
    # external destination), the ambient-proxy refusal, the dial guard,
    # the budget expiry, the deliberate 504 through the real REST surface.
    if ! grep -q 'TestLocalAdapterChatRefusesRedirects' "$transport_test" || ! grep -q 'TestProbeDoesNotFollowRedirects' "$transport_test"; then
        fail "the redirect regression tests must exist (adapter 307/308 + prober — KPK-01 acceptance 1-3)"
        return
    fi
    if ! grep -q 'TestLocalTransportIgnoresAmbientProxy' "$transport_test"; then
        fail "the ambient-proxy regression test must exist (KPK-01 acceptance 4)"
        return
    fi
    if ! grep -q 'TestRuntimeChatBudgetExpires' "$runtime_test" || ! grep -q 'TestRuntimeChatBudgetFailoverWithinBudget' "$runtime_test"; then
        fail "the budget regression tests must exist (delayed primary + fallback within budget, expiry by name — KPK-02 acceptance 1-2)"
        return
    fi
    if ! grep -q 'TestAIChatBudgetExpiredReturns504' "$api_test" || ! grep -q 'TestAIChatRedirectNeverFollowed' "$api_test"; then
        fail "the REST-surface regression tests must exist (504 on expiry + redirect refusal end-to-end through the mux)"
        return
    fi

    # --- The security map carries the v19.6.3 posture: the inference-port
    # auth inequality (KPK-19.6.1-P2-02) and the not-durable registry note
    # (KPK-19.6.1-P2-03) — operator-facing truths, stated where the
    # operator reads them.
    if ! grep -q 'inference port auth' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must state the auth inequality ('AI Runtime REST auth ≠ inference port auth' — the Bearer gate protects 8090 ONLY; KPK-19.6.1-P2-02)"
        return
    fi
    if ! grep -q 'NOT durable until' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must state the registry durability truth ('NOT durable until the CF-4/persistence decision' — KPK-19.6.1-P2-03)"
        return
    fi
    if ! grep -q 'KPK-01' "$secmap" || ! grep -q 'KPK-02' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must carry the v19.6.3 hardening bullets by finding name (KPK-01/KPK-02)"
        return
    fi

    # --- README: the release is a release like any other — changelog row,
    # What's New section, roadmap row, and the release notes document in
    # the remediation genre (Scope/Changes/Verification status).
    if ! grep -q "What's New in v19.6.3" "$readme" || ! grep -q '\*\*v19.6.3\*\*' "$readme"; then
        fail "README must carry the v19.6.3 changelog row + What's New section"
        return
    fi
    if ! grep -q '| AI Runtime transport hardening (KPK-01/02) | \*\*TESTED\*\* (v19.6.3) |' "$readme"; then
        fail "README must carry the 'AI Runtime transport hardening (KPK-01/02) | TESTED (v19.6.3)' roadmap row"
        return
    fi
    if [[ ! -f "$notes" ]]; then
        fail "docs/RELEASE_NOTES_v19.6.3.md missing (the KpK remediation release notes — the v19.2.10 genre)"
        return
    fi
    for section in '## Scope' '## Changes' '## Verification status'; do
        if ! grep -q "$section" "$notes"; then
            fail "the release notes must carry section '$section' (the remediation genre)"
            return
        fi
    done

    pass "universal AI runtime transport hardening (v19.6.3, the v19.6.1 KpK audit remediation): the local-only HTTP client closed at THREE layers (no redirects — http.ErrUseLastResponse, a 3xx is the failure it is, the POST body is the private conversation; no ambient proxy — Proxy: nil, env never steers local AI traffic; dial-guarded — loopback + the configured endpoint hosts only, defense in depth), ONE http.Client constructor in the whole package (transport.go, both adapter and prober ride it), the end-to-end timeout contract (airt.ChatBudget = per-attempt x the closed four-tier set, SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT within the 300s named bound, the API imposes the budget as the request deadline, ErrBudgetExpired answers the DELIBERATE 504 w/ attempts on both /api/ai/chat and /api/ai/propose, the write path extends beyond the budget via ResponseController so a legitimate slow local generation is written), the regression proofs as tests (307/308 zero-bytes redirect refusal, ambient-proxy refusal, dial guard, delayed-primary-fallback-within-budget, expiry-by-name, 504 + redirect end-to-end through the mux), the security map truths (REST auth ≠ inference port auth — KPK-19.6.1-P2-02; registry NOT durable until CF-4 — KPK-19.6.1-P2-03), README v19.6.3 surface + release notes genre"
}

# ===========================================================================
# check_32 — Universal AI Runtime transport hardening II (v19.6.4,
# KPK-03/KPK-04): the dial guard resolves, validates and PINS — and a
# cancelled probe round is not evidence about the providers
# ===========================================================================
check_32_ai_dial_pin_probe_cancel() {
    echo "--- [32] Universal AI Runtime transport hardening II (v19.6.4, KPK-03/KPK-04 remediation): the dial guard RESOLVES, VALIDATES and PINS every destination (trusted local networks; the IP that is dialed is the IP that was validated) + a cancelled probe round records NOTHING (last-known-good health stands) ---"
    local pkg="$REPO_DIR/scarlihq/internal/airt"
    local transport="$pkg/transport.go"
    local prober="$pkg/prober.go"
    local adapter="$pkg/adapter.go"
    local transport_test="$pkg/transport_test.go"
    local prober_test="$pkg/prober_test.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"
    local secmap="$REPO_DIR/docs/SCARLIX_SECURITY_MAP.md"
    local readme="$REPO_DIR/README.md"
    local notes="$REPO_DIR/docs/RELEASE_NOTES_v19.6.4.md"

    if [[ ! -f "$transport" ]]; then
        fail "internal/airt/transport.go missing (v19.6.4 — the resolve-validate-pin dial policy, KPK-03)"
        return
    fi

    # --- KPK-03: the resolve-validate-pin pipeline, BY MARKER IN CODE
    # (comment-stripped — prose cannot mask code, the check_28 lesson):
    # the transport RESOLVES the name itself (LookupIP), validates every
    # answered address, and PINS the connection to the validated address
    # (JoinHostPort — the dialer receives an ip:port, never a name).
    local code_hits
    code_hits=$(_strip_go_comments "$transport" | grep -c 'LookupIP' || true)
    if [[ "$code_hits" -eq 0 ]]; then
        fail "the dial guard must RESOLVE configured names itself (net resolver LookupIP in transport.go CODE — KPK-03: hostname checks passing is not destination trust)"
        return
    fi
    code_hits=$(_strip_go_comments "$transport" | grep -c 'JoinHostPort' || true)
    if [[ "$code_hits" -eq 0 ]]; then
        fail "the dial guard must PIN the connection to the validated address (net.JoinHostPort in transport.go CODE — KPK-03: the IP that is dialed is the IP that was validated)"
        return
    fi
    # --- KPK-03: the fail-closed MIXED-answer policy, named in the refusal
    # (the honest policy text — one foreign address refuses the whole set,
    # never a filtered guess).
    if ! grep -q "fail closed on mixed or empty answers" "$transport"; then
        fail "the dial guard must carry the named fail-closed mixed-answer refusal (KPK-03: a mixed resolution is refused WHOLE, never filtered)"
        return
    fi
    # --- KPK-03: the trusted-network boundary functions (the address-level
    # truth table + the whole-set approval policy, in CODE).
    code_hits=$(_strip_go_comments "$transport" | grep -c 'func ipIsTrusted' || true)
    local approved_hits
    approved_hits=$(_strip_go_comments "$transport" | grep -c 'func approvedResolvedIPs' || true)
    if [[ "$code_hits" -eq 0 || "$approved_hits" -eq 0 ]]; then
        fail "transport.go must define the address-level boundary (ipIsTrusted + approvedResolvedIPs — KPK-03: destination validation, not name validation)"
        return
    fi
    # --- KPK-03 + KPK-19.6.4-P2-01 (v19.6.5) + KPK-19.6.5-P2-01 (v19.6.6
    # evolution): the default trust is Docker's DOCUMENTED default address
    # pool, ENUMERATED as fifteen canonical /16 networks. The v19.6.5
    # single-pool marker ("172.17.0.0/12") masked to 172.16.0.0/12 and
    # trusted 172.16.0.0/16 (not a Docker default-pool range) — the
    # independent v19.6.5 audit's finding; the marker is now the
    # enumerated-pool call over dockerDefaultPoolCIDRs (prose cannot mask
    # code), the pool table must carry 172.17.0.0/16 and 172.31.0.0/16 but
    # NEVER a 172.16 entry, and the non-canonical /12 shorthand must be
    # gone from the CODE (its history lives in comments only).
    local pool_hits shorthand_hits
    pool_hits=$(_strip_go_comments "$transport" | grep -cF 'parseCIDRsOrPanic(dockerDefaultPoolCIDRs...)' || true)
    shorthand_hits=$(_strip_go_comments "$transport" | grep -cF 'parseCIDRsOrPanic("172.17.0.0/12")' || true)
    if [[ "$pool_hits" -eq 0 ]]; then
        fail "the default trusted dial networks must be the ENUMERATED Docker default pool (parseCIDRsOrPanic(dockerDefaultPoolCIDRs...) — KPK-19.6.5-P2-01: the non-canonical \"172.17.0.0/12\" shorthand masked to 172.16.0.0/12 and trusted unrelated space)"
        return
    fi
    if [[ "$shorthand_hits" -gt 0 ]]; then
        fail "the v19.6.5 non-canonical single-pool default must be GONE from code (KPK-19.6.5-P2-01 — the shorthand's /12 mask trusted 172.16.0.0/16 by accident of normalization)"
        return
    fi
    if ! grep -q '"172.17.0.0/16"' "$transport" || ! grep -q '"172.31.0.0/16"' "$transport"; then
        fail "dockerDefaultPoolCIDRs must carry the pool's first and last canonical /16 entries (172.17.0.0/16 … 172.31.0.0/16)"
        return
    fi
    local pool_1716_hits
    pool_1716_hits=$(_strip_go_comments "$transport" | grep -cF '"172.16.0.0/16"' || true)
    if [[ "$pool_1716_hits" -gt 0 ]]; then
        fail "172.16.0.0/16 must NOT be a default trusted network (KPK-19.6.5-P2-01: outside Docker's documented default pool — the audit's exact acceptance; the QUOTED literal in code, not the prose that documents its absence)"
        return
    fi
    if ! grep -q '192.168.0.0/16' "$transport"; then
        fail "transport.go must still NAME the LAN range that requires an explicit declaration (the boundary text carries the history)"
        return
    fi
    # --- KPK-03: the operator surface + the fail-closed construction (a
    # service-name endpoint with loopback-only trust never boots).
    if ! grep -q 'SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS' "$transport"; then
        fail "transport.go must name the operator surface (SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS — the trusted dial networks declaration, KPK-03)"
        return
    fi
    code_hits=$(_strip_go_comments "$transport" | grep -c 'func validateNameEndpointDialTrust' || true)
    if [[ "$code_hits" -eq 0 ]]; then
        fail "the construction-time dial-trust validation must exist (validateNameEndpointDialTrust in transport.go CODE — a service-name endpoint with loopback-only trust fails AT CONSTRUCTION, never per-request)"
        return
    fi
    # --- The constructor surfaces carry the DialOption (adapter + prober
    # accept the trusted-network declaration; the stack forwards it).
    code_hits=$(_strip_go_comments "$adapter" | grep -c 'DialOption' || true)
    local prober_hits
    prober_hits=$(_strip_go_comments "$prober" | grep -c 'DialOption' || true)
    if [[ "$code_hits" -eq 0 || "$prober_hits" -eq 0 ]]; then
        fail "NewLocalAdapter and NewProber must both accept the DialOption (the trusted dial networks flow to EVERY client the package constructs — KPK-03)"
        return
    fi

    # --- KPK-04: the cancelled-round guard, BY MARKER IN CODE — the
    # round-end guard in ProbeAll ('if err := ctx.Err()' — distinct from
    # the single-probe guard 'if cerr := ctx.Err()') and the named refusal.
    # Each marker is specific: breaking ONE guard fails ONE marker.
    code_hits=$(_strip_go_comments "$prober" | grep -c 'if err := ctx.Err()' || true)
    if [[ "$code_hits" -eq 0 ]]; then
        fail "ProbeAll must check the caller's context at round end (the 'if err := ctx.Err()' round-end guard in prober.go CODE — KPK-04: a cancelled round aborts, records nothing)"
        return
    fi
    code_hits=$(_strip_go_comments "$prober" | grep -c 'if cerr := ctx.Err()' || true)
    if [[ "$code_hits" -eq 0 ]]; then
        fail "a single Probe must refuse to fabricate an observation for a done context (the 'if cerr := ctx.Err()' guard in prober.go CODE — KPK-04: a cancelled observation is not evidence about the provider)"
        return
    fi
    if ! grep -q "a cancelled round is not evidence about the providers" "$prober"; then
        fail "the probe-round abort must carry its named refusal ('a cancelled round is not evidence about the providers' — KPK-04)"
        return
    fi
    # --- KPK-04: the background loop's round budget carries the 2x margin
    # (the probe's own timeout classifies a slow tier; the round clock only
    # aborts a pathologically stuck round).
    if ! grep -q 'probeRoundBudget' "$prober"; then
        fail "the background loop must carry the named round budget (probeRoundBudget = 2x the per-probe timeout — KPK-04: the two clocks are never ambiguous)"
        return
    fi

    # --- The operator surface is WIRED (main.go parses the env fail-closed
    # and passes the DialOption; compose documents the variable).
    code_hits=$(_strip_go_comments "$main_src" | grep -c 'TrustedDialCIDRsFromEnv' || true)
    local opt_hits
    opt_hits=$(_strip_go_comments "$main_src" | grep -c 'WithTrustedDialCIDRs' || true)
    if [[ "$code_hits" -eq 0 || "$opt_hits" -eq 0 ]]; then
        fail "main.go must wire SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS (TrustedDialCIDRsFromEnv parsed fail-closed + WithTrustedDialCIDRs passed to the stack — KPK-03)"
        return
    fi
    if ! grep -q 'SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS' "$compose"; then
        fail "the compose env block must document SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS (the operator surface is documented where the operator reads it — KPK-03)"
        return
    fi

    # --- The regression PROOFS exist as tests (the proof, not the claim):
    # the pin, the public-resolution refusal (the audit finding), the
    # mixed fail-closed, the TOCTOU resolution change, the cancelled round
    # preserving last-known-good, the genuine-failure counterweight.
    if ! grep -q 'TestDialGuardResolvesValidatesAndPins' "$transport_test" || ! grep -q 'TestDialGuardRefusesPublicResolution' "$transport_test"; then
        fail "the KPK-03 regression tests must exist (resolve-validate-pin + the public-resolution refusal — the audit finding closed)"
        return
    fi
    if ! grep -q 'TestDialGuardFailClosedOnMixedResolution' "$transport_test" || ! grep -q 'TestDialGuardTOCTOUResolutionChange' "$transport_test"; then
        fail "the KPK-03 fail-closed/TOCTOU regression tests must exist (mixed refused whole + a resolver that changes answers cannot bypass the pin)"
        return
    fi
    if ! grep -q 'TestNameEndpointDialTrustConstruction' "$transport_test" || ! grep -q 'TestTrustedDialCIDRsFromEnv' "$transport_test"; then
        fail "the KPK-03 construction/env regression tests must exist (loopback-only trust fails at construction; the env parses fail-closed)"
        return
    fi
    if ! grep -q 'TestProbeAllCancelledRoundPreservesLastKnownGood' "$prober_test" || ! grep -q 'TestProbeAllGenuineFailureOnlyThatProvider' "$prober_test"; then
        fail "the KPK-04 regression tests must exist (a cancelled round preserves last-known-good + a genuine failure still records — the counterweight pair)"
        return
    fi
    if ! grep -q 'TestProbeAllDeadlineExpiredRoundAborts' "$prober_test" || ! grep -q 'TestProberLoopSlowTierStaysGenuine' "$prober_test"; then
        fail "the KPK-04 round-clock tests must exist (deadline-expired aborts + the loop's slow tier stays a genuine UNAVAILABLE)"
        return
    fi

    # --- The security map carries the v19.6.4 posture: the address-level
    # boundary + the cancelled-round truth, by finding name.
    if ! grep -q 'KPK-03' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must state the address-level boundary by finding name (KPK-03: resolve-validate-pin over the trusted local networks)"
        return
    fi
    if ! grep -q 'KPK-04' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must state the cancelled-round truth by finding name (KPK-04: a cancelled probe round records nothing)"
        return
    fi
    if ! grep -q 'SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must name the trusted-network operator surface (SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS — KPK-03)"
        return
    fi
    if ! grep -q '20 minutes' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must state the named maximum wall clock (4 x min(CHAT_TIMEOUT, 300s) = 20 minutes — the SK-audit P2-03 documentation residual)"
        return
    fi

    # --- README: the release is a release like any other — changelog row,
    # What's New section, roadmap row, and the release notes document in
    # the remediation genre (Scope/Changes/Verification status).
    if ! grep -q "What's New in v19.6.4" "$readme" || ! grep -q '\*\*v19.6.4\*\*' "$readme"; then
        fail "README must carry the v19.6.4 changelog row + What's New section (the permanent record — the v19.6.5 remediation built on it; check_33 gates the v19.6.5 surface)"
        return
    fi
    if ! grep -q '| AI Runtime transport hardening II (KPK-03/04) | \*\*TESTED\*\* (v19.6.4) |' "$readme"; then
        fail "README must carry the 'AI Runtime transport hardening II (KPK-03/04) | TESTED (v19.6.4)' roadmap row"
        return
    fi
    if [[ ! -f "$notes" ]]; then
        fail "docs/RELEASE_NOTES_v19.6.4.md missing (the KpK remediation release notes — the v19.6.3 genre)"
        return
    fi
    for section in '## Scope' '## Changes' '## Verification status'; do
        if ! grep -q "$section" "$notes"; then
            fail "the release notes must carry section '$section' (the remediation genre)"
            return
        fi
    done

    pass "universal AI runtime transport hardening II (v19.6.4, the v19.6.3 KpK audit remediation): the dial guard validates DESTINATIONS not just names (a configured name is RESOLVED through the process's own resolver, EVERY answered address validated against the trusted local networks — loopback always, the Docker default bridge pool (v19.6.5: ONE network — the home-LAN range 192.168.0.0/16 is operator territory, declared via SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS, never implicit) or the operator's declaration, fail-closed at startup, no blind RFC1918 — and the connection PINNED to a validated address: the IP that is dialed is the IP that was validated, mixed answer sets refused WHOLE, public resolutions ZERO dials/zero prompt bytes, a service-name endpoint with loopback-only trust fails at construction), a cancelled probe round is not evidence about the providers (a caller-closed context aborts the round — the context error returns errors.Is-identifiable, ProbeAllAndObserve records NOTHING, last-known-good states AND timestamps stand; genuine per-provider failures still record with a live parent; the loop's 2x round-budget margin keeps the probe-timeout vs round-clock classification unambiguous), the regression proofs as tests (pin by recording dialer, public/mixed/empty/TOCTOU refusals, construction fail-closed, env parse truth table, cancelled-round preservation + genuine-failure counterweight + the production path through the real resolver), the security map truths (the address-level boundary + the honest resolver residual + the named 20-minute maximum wall clock), README v19.6.4 surface + release notes genre"
}

# ===========================================================================
# check_33 — Universal AI Runtime trust scope + commit atomicity (v19.6.5,
# KPK-19.6.4-P2-01/P2-02): the default trust is the Docker bridge pool
# ONLY — and a probe round commits atomically at one context-aware
# boundary
# ===========================================================================
check_33_ai_trust_scope_commit_atomicity() {
    echo "--- [33] Universal AI Runtime trust scope + commit atomicity (v19.6.5, KPK-19.6.4-P2-01/P2-02 remediation): the DEFAULT trusted network is the Docker bridge pool ONLY (the home-LAN range is declared, never implicit; universal routes refused) + the probe round's observations commit through ONE atomic, context-aware batch boundary (cancellation before the commit = zero writes) ---"
    local pkg="$REPO_DIR/scarlihq/internal/airt"
    local transport="$pkg/transport.go"
    local prober="$pkg/prober.go"
    local transport_test="$pkg/transport_test.go"
    local prober_test="$pkg/prober_test.go"
    local health="$REPO_DIR/scarlihq/internal/airuntime/health.go"
    local health_test="$REPO_DIR/scarlihq/internal/airuntime/health_test.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local compose="$REPO_DIR/scarlihq/docker-compose.yml"
    local secmap="$REPO_DIR/docs/SCARLIX_SECURITY_MAP.md"
    local readme="$REPO_DIR/README.md"
    local notes="$REPO_DIR/docs/RELEASE_NOTES_v19.6.5.md"

    if [[ ! -f "$transport" ]] || [[ ! -f "$health" ]]; then
        fail "internal/airt/transport.go or internal/airuntime/health.go missing (v19.6.5 — the trust scope + the atomic batch commit, KPK-19.6.4-P2-01/P2-02)"
        return
    fi

    # --- KPK-19.6.4-P2-01 (v19.6.5) + KPK-19.6.5-P2-01 (v19.6.6
    # evolution): the default pool is the ENUMERATED canonical /16 set, BY
    # MARKER IN CODE (comment-stripped — prose cannot mask code): the
    # dockerDefaultPoolCIDRs call; the non-canonical /12 shorthand (both
    # its v19.6.4 two-pool AND v19.6.5 single-pool forms) FAILS the marker.
    local pool_hits33 shorthand_hits33 twopool_hits33
    pool_hits33=$(_strip_go_comments "$transport" | grep -cF 'parseCIDRsOrPanic(dockerDefaultPoolCIDRs...)' || true)
    shorthand_hits33=$(_strip_go_comments "$transport" | grep -cF 'parseCIDRsOrPanic("172.17.0.0/12")' || true)
    twopool_hits33=$(_strip_go_comments "$transport" | grep -cF 'parseCIDRsOrPanic("172.17.0.0/12", "192.168.0.0/16")' || true)
    if [[ "$pool_hits33" -eq 0 ]]; then
        fail "DefaultTrustedDialCIDRs must call parseCIDRsOrPanic over dockerDefaultPoolCIDRs — the ENUMERATED canonical /16 pool (KPK-19.6.5-P2-01: the \"172.17.0.0/12\" shorthand masked to 172.16.0.0/12 and trusted 172.16.0.0/16; KPK-19.6.4-P2-01: the home-LAN range is operator territory, declared via SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS, never implicit)"
        return
    fi
    if [[ "$shorthand_hits33" -gt 0 || "$twopool_hits33" -gt 0 ]]; then
        fail "DefaultTrustedDialCIDRs must NOT carry the non-canonical /12 default (neither the v19.6.5 single-pool nor the v19.6.4 two-pool form) — the shorthand's /12 mask trusted 172.16.0.0/16, the independent v19.6.5 audit's finding"
        return
    fi
    # --- KPK-19.6.4-P2-01 (hardening → policy): the UNIVERSAL-route
    # refusal exists in CODE — the helper + its use in the env parse.
    local universal_hits
    universal_hits=$(_strip_go_comments "$transport" | grep -c 'func isUniversalCIDR' || true)
    if [[ "$universal_hits" -eq 0 ]]; then
        fail "transport.go must define the universal-route detector (func isUniversalCIDR — 0.0.0.0/0 and ::/0 nullify the destination boundary; the audit's hardening note, adopted as policy)"
        return
    fi
    local universal_wired_hits
    universal_wired_hits=$(_strip_go_comments "$transport" | grep -c 'isUniversalCIDR(ipnet)' || true)
    if [[ "$universal_wired_hits" -eq 0 ]]; then
        fail "TrustedDialCIDRsFromEnv must REFUSE universal routes (the isUniversalCIDR check in the env parse — a declaration that would nullify the boundary is a configuration error, not a posture)"
        return
    fi
    if ! grep -q 'UNIVERSAL route' "$transport"; then
        fail "the universal-route refusal must carry its named error text ('UNIVERSAL route' — the operator sees WHY the declaration was refused)"
        return
    fi

    # --- KPK-19.6.4-P2-02: the atomic batch commit, BY MARKER IN CODE —
    # the batch operation itself, the commit-boundary context check
    # (distinct from every other ctx.Err() guard by its named refusal),
    # and the one-lock apply.
    local batch_hits
    batch_hits=$(_strip_go_comments "$health" | grep -c 'func (t \*HealthTable) ObserveBatch' || true)
    if [[ "$batch_hits" -eq 0 ]]; then
        fail "health.go must define the batch commit operation (HealthTable.ObserveBatch — the audit's remediation item 1: validate all, apply under one lock)"
        return
    fi
    if ! grep -q "rejected at the commit boundary" "$health"; then
        fail "ObserveBatch must carry the named commit-boundary refusal ('rejected at the commit boundary' — cancellation between probe completion and the commit records NOTHING, KPK-19.6.4-P2-02)"
        return
    fi
    if ! grep -q "batches are atomic" "$health"; then
        fail "ObserveBatch's stale rejection must name the batch atomicity ('batches are atomic' — a stale member rejects the round WHOLE, never partially applied)"
        return
    fi
    # --- KPK-19.6.4-P2-02: ProbeAllAndObserve commits through the batch —
    # never the per-provider Observe loop the audit found racy.
    local batchcall_hits
    batchcall_hits=$(_strip_go_comments "$prober" | grep -c 'table.ObserveBatch(ctx, observations)' || true)
    if [[ "$batchcall_hits" -eq 0 ]]; then
        fail "ProbeAllAndObserve must commit the round through table.ObserveBatch (the per-provider Observe loop is the check-to-write race the audit found — KPK-19.6.4-P2-02 remediation item 3)"
        return
    fi
    # --- The injectable seam (remediation item 4): the deterministic
    # window between ProbeAll completion and the commit.
    local seam_hits
    seam_hits=$(_strip_go_comments "$prober" | grep -c 'p.preCommit' || true)
    if [[ "$seam_hits" -eq 0 ]]; then
        fail "the Prober must carry the preCommit test seam (p.preCommit between ProbeAll completion and the commit — the deterministic cancellation window the audit's acceptance asks for)"
        return
    fi

    # --- The regression PROOFS exist as tests (the proof, not the claim):
    # the audit's acceptance lists, by name.
    if ! grep -q 'TestDefaultTrustRejectsUnrelatedLANDestinations' "$transport_test" || ! grep -q 'TestDeclaredSubnetMixedWithLANRefusedWhole' "$transport_test"; then
        fail "the KPK-19.6.4-P2-01 regression tests must exist (default trust rejects unrelated LAN destinations + a declared-subnet/LAN mixed set refused whole — the audit's acceptance list)"
        return
    fi
    if ! grep -q 'TestDeclaredSubnetPinsAcrossResolveValidateDial' "$transport_test"; then
        fail "the production-path regression must exist (TestDeclaredSubnetPinsAcrossResolveValidateDial — resolve-validate-pin-dial against the CONFIGURED Docker subnet)"
        return
    fi
    if ! grep -q 'TestObserveBatchCancelledAtCommitBoundary' "$health_test" || ! grep -q 'TestObserveBatchConcurrentRoundsMonotone' "$health_test"; then
        fail "the KPK-19.6.4-P2-02 regression tests must exist (cancelled at the commit boundary preserves everything + concurrent rounds stay monotone and whole — the audit's acceptance list)"
        return
    fi
    if ! grep -q 'TestObserveBatchAtomicOnInvalidMember' "$health_test" || ! grep -q 'TestObserveBatchAtomicOnStaleMember' "$health_test"; then
        fail "the batch atomicity regression tests must exist (an invalid or stale member rejects the batch WHOLE — no partial round is ever visible)"
        return
    fi
    if ! grep -q 'TestProbeAllCancelBetweenRoundAndCommit' "$prober_test" || ! grep -q 'TestProbeAllCommitAfterSeamStillCommits' "$prober_test"; then
        fail "the deterministic window regression must exist (cancel BETWEEN round completion and commit → nothing recorded + the non-cancelling seam counterweight)"
        return
    fi

    # --- The operator guidance is documented where the operator reads it:
    # the compose env block derives the subnet; the security map names the
    # narrowed default and the honest residuals; main.go wires unchanged.
    if ! grep -q 'docker network inspect scarlix-net' "$compose" || ! grep -q 'docker network inspect scarlix-net' "$secmap"; then
        fail "the operator subnet derivation must be documented (docker network inspect scarlix-net — compose AND SECURITY_MAP: the actual network is operator-declared, explicit and auditable)"
        return
    fi
    if ! grep -q 'KPK-19.6.4-P2-01' "$secmap" || ! grep -q 'KPK-19.6.4-P2-02' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must state both v19.6.5 closures by finding name (KPK-19.6.4-P2-01 narrowed default + KPK-19.6.4-P2-02 atomic commit)"
        return
    fi
    if ! grep -q 'dual-stack' "$compose" || ! grep -q 'Dual-stack note' "$secmap"; then
        fail "the IPv6 dual-stack note must be documented (compose + SECURITY_MAP — an unapproved AAAA answer refuses the set whole; declare the IPv6 pool or prefer IPv4 literals)"
        return
    fi
    code_hits=$(_strip_go_comments "$main_src" | grep -c 'TrustedDialCIDRsFromEnv' || true)
    if [[ "$code_hits" -eq 0 ]]; then
        fail "main.go must still wire SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS through TrustedDialCIDRsFromEnv (the narrowed default changed the DEFAULTS, not the wiring)"
        return
    fi

    # --- README: the release is a release like any other — changelog row,
    # What's New section, roadmap row; the release notes document in the
    # remediation genre.
    if ! grep -q "What's New in v19.6.5" "$readme" || ! grep -q '\*\*v19.6.5\*\*' "$readme"; then
        fail "README must carry the v19.6.5 changelog row + What's New section"
        return
    fi
    if ! grep -q '| AI Runtime trust scope + commit atomicity (KPK-19.6.4-P2-01/02) | \*\*TESTED\*\* (v19.6.5) |' "$readme"; then
        fail "README must carry the 'AI Runtime trust scope + commit atomicity (KPK-19.6.4-P2-01/02) | TESTED (v19.6.5)' roadmap row"
        return
    fi
    if [[ ! -f "$notes" ]]; then
        fail "docs/RELEASE_NOTES_v19.6.5.md missing (the KpK remediation release notes — the v19.6.4 genre)"
        return
    fi
    for section in '## Scope' '## Changes' '## Verification status' '## Known limitation'; do
        if ! grep -q "$section" "$notes"; then
            fail "the release notes must carry section '$section' (the remediation genre)"
            return
        fi
    done

    pass "universal AI runtime trust scope + commit atomicity (v19.6.5, the v19.6.4 KpK audit remediation): the DEFAULT trusted dial network is the Docker default bridge pool ONLY (172.17.0.0/12, exactly one network by code marker — the home-LAN range 192.168.0.0/16 is operator territory: a poisoned resolver answering 192.168.1.1 for an approved name is refused with ZERO dials, the operator declares the ACTUAL subnet from 'docker network inspect scarlix-net' via SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS, explicit/visible/auditable, never implicit), universal routes (0.0.0.0/0, ::/0) are REFUSED fail-closed (a declaration that would nullify the boundary is a configuration error — the audit's hardening note adopted as policy), the probe round's observations commit through HealthTable.ObserveBatch (validate all, monotonicity-check the batch, apply under ONE lock — a stale or invalid member rejects the round WHOLE, a partially applied round is never visible), the commit boundary is CONTEXT-AWARE (cancellation between probe completion and the commit records NOTHING, errors.Is-identifiable, every previous state AND timestamp preserved; cancellation after the commit does not retroactively undo a completed round — the linearization rule), ProbeAllAndObserve commits through the batch never a per-provider loop, the preCommit seam makes the audited window deterministically testable, the regression proofs as tests (LAN refusal by default, mixed declared/LAN refused whole, declared-subnet pin, universal rejection, batch atomicity, boundary cancellation, concurrent monotone rounds, the deterministic window + counterweights), the documented truths (compose subnet derivation + SECURITY_MAP finding-name closures + the IPv6 dual-stack note + the inherent in-subnet lateral residual), README v19.6.5 surface + release notes genre"
}

check_34_trust_enumeration_profiles_enforcement_stale_automation() {
    echo "--- [34] v19.6.6 (the v19.6.5 KpK audit remediation + the process remediation): the ENUMERATED default trust pool (fifteen canonical /16s, no non-canonical supernet, the SK-audit startup warning) + the commit boundary UNDER the mutex + the TYPED/ENFORCED family profiles (the P1) + the real media-tools build context + the generated dependency inventory (drift-guarded) + models.yaml identity + the STALE-TREE AUDIT automation with the advisory local-AI triage ---"
    # v19.6.6 — the v19.6.5 KpK audit remediation: KPK-19.6.5-P2-01 (the
    # non-canonical /12 default masked to a wider trust range), KPK-19.6.5-P2-02
    # (the context check before the mutex), KPK-19.6.5-P1-03 (the family profiles
    # schema drift + zero enforcement), KPK-19.6.5-P2-04/P2-05/P3-06 (media-tools
    # build context, stale dependency audit, models.yaml identity) + the process
    # remediation (the stale-tree audit automation with the advisory local-AI
    # triage). Code markers are comment-stripped — prose cannot mask code.
    local transport="$REPO_DIR/scarlihq/internal/airt/transport.go"
    local transport_test="$REPO_DIR/scarlihq/internal/airt/transport_test.go"
    local health="$REPO_DIR/scarlihq/internal/airuntime/health.go"
    local health_test="$REPO_DIR/scarlihq/internal/airuntime/health_test.go"
    local loader="$REPO_DIR/scarlihq/internal/profiles/loader.go"
    local loader_test="$REPO_DIR/scarlihq/internal/profiles/loader_test.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local airuntime_src="$REPO_DIR/scarlihq/internal/api/airuntime.go"
    local mcp_src="$REPO_DIR/scarlihq/internal/mcp/server.go"
    local enforcement_test="$REPO_DIR/scarlihq/internal/api/profiles_enforcement_test.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local readme="$REPO_DIR/README.md"
    local secmap="$REPO_DIR/docs/SCARLIX_SECURITY_MAP.md"
    local inventory_doc="$REPO_DIR/docs/DEPENDENCY_INVENTORY_2026-11-06.md"
    local inventory_test="$REPO_DIR/tests/test_dependency_inventory.sh"
    local historical_audit="$REPO_DIR/docs/DEPENDENCY_AUDIT_2026-10-21.md"
    local models_yaml="$REPO_DIR/models.yaml"
    local stale_audit="$REPO_DIR/files/usr/local/bin/scarlix-stale-audit.sh"
    local stale_service="$REPO_DIR/files/etc/systemd/system/scarlix-stale-audit.service"
    local stale_timer="$REPO_DIR/files/etc/systemd/system/scarlix-stale-audit.timer"
    local stale_policy="$REPO_DIR/docs/STALE_TREE_AUDIT.md"
    local media_dockerfile="$REPO_DIR/media-tools/Dockerfile"
    local media_server="$REPO_DIR/media-tools/server.py"
    local notes="$REPO_DIR/docs/RELEASE_NOTES_v19.6.6.md"

    for f in "$transport" "$health" "$loader" "$rest_src" "$airuntime_src" "$mcp_src"; do
        if [[ ! -f "$f" ]]; then
            fail "v19.6.6 source missing: $f (the KPK-19.6.5 remediation surface)"
            return
        fi
    done

    # --- KPK-19.6.5-P2-01: the ENUMERATED canonical pool, by code marker.
    local enum_hits
    enum_hits=$(_strip_go_comments "$transport" | grep -cF 'parseCIDRsOrPanic(dockerDefaultPoolCIDRs...)' || true)
    if [[ "$enum_hits" -eq 0 ]]; then
        fail "DefaultTrustedDialCIDRs must dial over the ENUMERATED pool (parseCIDRsOrPanic(dockerDefaultPoolCIDRs...) — KPK-19.6.5-P2-01: the non-canonical 172.17.0.0/12 masked to 172.16.0.0/12 and trusted unrelated space)"
        return
    fi
    local pool_entries
    pool_entries=$(grep -cE '"172\.(1[7-9]|2[0-9]|3[01])\.0\.0/16"' "$transport" || true)
    if [[ "$pool_entries" -lt 15 ]]; then
        fail "dockerDefaultPoolCIDRs must enumerate all fifteen canonical /16s of Docker's documented default pool (172.17.0.0/16 … 172.31.0.0/16 — found $pool_entries)"
        return
    fi
    if grep -q '"172\.16\.0\.0/16"' "$transport"; then
        fail "172.16.0.0/16 must NOT be in the default pool (the audit's exact acceptance: outside Docker's documented default pool)"
        return
    fi
    if ! grep -q 'TestDockerDefaultPoolCIDRsCanonical' "$transport_test" || ! grep -q 'TestUndeclaredServiceNameEndpoints' "$transport_test"; then
        fail "the KPK-19.6.5-P2-01 regression tests must exist (canonicality/contiguity proof + the SK-audit warning helper proof)"
        return
    fi
    # --- SK-audit P2-01: the loud startup warning for undeclared service names.
    local undeclared_fn_hits undeclared_wiring_hits
    undeclared_fn_hits=$(_strip_go_comments "$transport" | grep -c 'func UndeclaredServiceNameEndpoints' || true)
    if [[ "$undeclared_fn_hits" -eq 0 ]]; then
        fail "transport.go must define UndeclaredServiceNameEndpoints (the SK-audit P2-01 guidance: service-name endpoints + unset env → a loud warning with the derivation command)"
        return
    fi
    undeclared_wiring_hits=$(_strip_go_comments "$main_src" | grep -c 'UndeclaredServiceNameEndpoints' || true)
    if [[ "$undeclared_wiring_hits" -eq 0 ]]; then
        fail "main.go must wire the undeclared-service-name WARNING (the operator is asked to declare the scarlix-net subnet BEFORE production)"
        return
    fi

    # --- KPK-19.6.5-P2-02: the context check lives UNDER the mutex. The
    # ORDER is the contract: within ObserveBatch, ctx.Err() must appear
    # AFTER t.mu.Lock() (line numbers, comment-stripped).
    local lock_line ctx_line fn_start fn_end
    fn_start=$(_strip_go_comments "$health" | grep -n 'func (t \*HealthTable) ObserveBatch' | head -1 | cut -d: -f1)
    fn_end=$(_strip_go_comments "$health" | awk -v s="$fn_start" 'NR>s && /^}/ {print NR; exit}')
    lock_line=$(_strip_go_comments "$health" | awk -v s="$fn_start" -v e="$fn_end" 'NR>s && NR<e && /t\.mu\.Lock\(\)/ {print NR; exit}')
    ctx_line=$(_strip_go_comments "$health" | awk -v s="$fn_start" -v e="$fn_end" 'NR>s && NR<e && /ctx\.Err\(\)/ {print NR; exit}')
    if [[ -z "$fn_start" || -z "$lock_line" || -z "$ctx_line" ]] || (( ctx_line < lock_line )); then
        fail "ObserveBatch must check ctx.Err() AFTER acquiring t.mu (KPK-19.6.5-P2-02: the v19.6.5 check ran before the lock — a cancellation in the waiting window slipped its batch through)"
        return
    fi
    if ! grep -q 'TestObserveBatchCancelledWhileWaitingForLock' "$health_test" || ! grep -q 'TestObserveBatchDeadlineWhileWaitingForLock' "$health_test"; then
        fail "the deterministic lock-contention regression tests must exist (cancellation AND deadline while blocked on the mutex — the audit's acceptance list)"
        return
    fi

    # --- KPK-19.6.5-P1-03: the typed, strict, enforced family profiles.
    local budget_hits unmarshal_hits
    budget_hits=$(_strip_go_comments "$loader" | grep -c 'type TokenBudget struct' || true)
    unmarshal_hits=$(_strip_go_comments "$loader" | grep -c 'func (b \*TokenBudget) UnmarshalYAML' || true)
    if [[ "$budget_hits" -eq 0 || "$unmarshal_hits" -eq 0 ]]; then
        fail "the profile schema must model the dual-shape TokenBudget with its custom unmarshaler (KPK-19.6.5-P1-03: the STRING token_budget failed on the mapping shape and silently fell back)"
        return
    fi
    local marker_hits
    for marker in 'type ModePolicy struct' 'type ContentFilter struct' 'Permissions'; do
        marker_hits=$(_strip_go_comments "$loader" | grep -c "$marker" || true)
        if [[ "$marker_hits" -eq 0 ]]; then
            fail "the profile schema must model the declared policy ($marker — the audit: the policy fields were represented NOWHERE)"
            return
        fi
    done
    local strict_hits issue_hits
    strict_hits=$(_strip_go_comments "$loader" | grep -c 'KnownFields(true)' || true)
    if [[ "$strict_hits" -eq 0 ]]; then
        fail "the profile load must be STRICT (yaml KnownFields — unknown keys are errors for policy files, a typo cannot silently drop a permission)"
        return
    fi
    issue_hits=$(_strip_go_comments "$loader" | grep -c 'type Issue struct' || true)
    if [[ "$issue_hits" -eq 0 ]]; then
        fail "the loader must surface invalid files as named Issues (never a silent filename-only fallback that looks permissively fine)"
        return
    fi
    for marker in 'func (p \*Profile) AllowsMode' 'func (p \*Profile) RequirePermission' 'func (p \*Profile) ContentBlocked' 'ErrModeSwitchForbidden' 'ErrPermissionDenied' 'ErrContentBlocked'; do
        marker_hits=$(_strip_go_comments "$loader" | grep -c "$marker" || true)
        if [[ "$marker_hits" -eq 0 ]]; then
            fail "the profile policy must be DECIDABLE at the action boundary ($marker — enforcement helpers, not dashboard hiding)"
            return
        fi
    done
    # enforcement wiring: REST mode switch + chat + MCP
    local mode_enf_hits forbidden_hits chat_perm_hits chat_filter_hits chat_header_hits mcp_enf_hits
    mode_enf_hits=$(_strip_go_comments "$rest_src" | grep -c 'p.AllowsMode(mode)' || true)
    forbidden_hits=$(_strip_go_comments "$rest_src" | grep -c 'StatusForbidden' || true)
    if [[ "$mode_enf_hits" -eq 0 || "$forbidden_hits" -eq 0 ]]; then
        fail "the mode-switch handler must enforce the profile policy server-side (AllowsMode + 403 refusals by name — KPK-19.6.5-P1-03)"
        return
    fi
    chat_perm_hits=$(_strip_go_comments "$airuntime_src" | grep -c 'RequirePermission' || true)
    chat_filter_hits=$(_strip_go_comments "$airuntime_src" | grep -c 'ContentBlocked' || true)
    chat_header_hits=$(_strip_go_comments "$airuntime_src" | grep -c 'X-Scarlix-Profile' || true)
    if [[ "$chat_perm_hits" -eq 0 || "$chat_filter_hits" -eq 0 || "$chat_header_hits" -eq 0 ]]; then
        fail "the chat handler must enforce the profile policy server-side (ai_chat permission + the content filter on the input + the header entry path)"
        return
    fi
    mcp_enf_hits=$(_strip_go_comments "$mcp_src" | grep -c 'AllowsMode' || true)
    if [[ "$mcp_enf_hits" -eq 0 ]]; then
        fail "the MCP mode-set tool must enforce the profile policy too (the same action boundary, the same refusals)"
        return
    fi
    if ! grep -q 'TestRepositoryProfilesParseWithoutFallback' "$loader_test" || ! grep -q 'TestNoSilentFallback' "$loader_test" || ! grep -q 'TestValidationNegativeCases' "$loader_test"; then
        fail "the P1-03 regression tests must exist (all four profiles parse without fallback + no silent fallback + the negative schema matrix)"
        return
    fi
    if ! grep -q 'TestModeSwitchProfilePolicyEnforced' "$enforcement_test" || ! grep -q 'TestChatProfilePolicyEnforced' "$enforcement_test"; then
        fail "the enforcement matrix must exist as REST tests (mode-switch refusals + chat refusals with the live counterweights)"
        return
    fi

    # --- KPK-19.6.5-P2-04: the media-tools build context is REAL.
    if [[ ! -f "$media_dockerfile" ]] || [[ ! -f "$media_server" ]]; then
        fail "media-tools must ship the Dockerfile + the service (KPK-19.6.5-P2-04: 'build: .' had no build context — the service could not be built at all)"
        return
    fi
    if ! grep -qE '^FROM python:3\.12-alpine3\.20' "$media_dockerfile"; then
        fail "the media-tools Dockerfile must pin its base image (python:3.12-alpine3.20 — no :latest base)"
        return
    fi
    if ! grep -q 'HEALTHCHECK' "$media_dockerfile"; then
        fail "the media-tools service must carry a HEALTHCHECK (the compose contract has a healthcheck-able service)"
        return
    fi

    # --- KPK-19.6.5-P2-05: the generated inventory + the drift guard + the
    # historical banner.
    if [[ ! -f "$inventory_doc" ]] || [[ ! -f "$inventory_test" ]]; then
        fail "the dependency inventory + drift test must exist (KPK-19.6.5-P2-05: a generated current inventory, not a stale audit posing as one)"
        return
    fi
    if ! grep -q 'HISTORICAL POINT-IN-TIME SNAPSHOT' "$historical_audit" || ! grep -q 'DEPENDENCY_INVENTORY_2026-11-06' "$historical_audit"; then
        fail "the 2026-10-21 audit must be bannered as a historical snapshot pointing at the current inventory"
        return
    fi
    if ! grep -q 'mcp/playwright-server:latest' "$inventory_doc" || ! grep -q 'freetoken/serving:latest' "$inventory_doc"; then
        fail "the inventory must REGISTER the experimental :latest set as named residuals (never silent rides)"
        return
    fi

    # --- KPK-19.6.5-P3-06: models.yaml identity reconciliation.
    if head -n 12 "$models_yaml" | grep -qE '^# SCARLIX OS v[0-9]'; then
        fail "models.yaml must not carry a release identity in its header (the P3-06 defect was the header claiming 'SCARLIX OS v19.2.9' while the release was v19.6.5 — the VERSION file is the single source of truth)"
        return
    fi
    local phantom_hits
    phantom_hits=$(sed -E 's/#.*$//' "$models_yaml" | grep -vE '^[[:space:]]*$' | grep -c 'qwen3\.6' || true)
    phantom_hits=$((phantom_hits + $(cat "$REPO_DIR/profiles/hugo.yaml" "$REPO_DIR/profiles/mon.yaml" "$REPO_DIR/profiles/xox.yaml" 2>/dev/null | grep -c 'qwen3\.6' || true)))
    if [[ "$phantom_hits" -gt 0 ]]; then
        fail "the phantom qwen3.6:14b identifier must be gone from ACTIVE config lines (one canonical Ollama id: qwen2.5:3b — what install.sh actually pulls; comments documenting the removal are honest history)"
        return
    fi

    # --- The process remediation: the stale-tree audit automation.
    for f in "$stale_audit" "$stale_service" "$stale_timer" "$stale_policy"; do
        if [[ ! -f "$f" ]]; then
            fail "the stale-tree automation must exist ($f — the audits' asymmetry finding gets a schedule, not a memory)"
            return
        fi
    done
    if ! grep -q 'ai-summary' "$stale_service" || ! grep -q 'noproxy' "$stale_audit" || ! grep -q 'OnCalendar=Mon' "$stale_timer"; then
        fail "the stale-audit automation must wire the advisory AI triage (loopback, no ambient proxy) on the weekly schedule"
        return
    fi
    if ! grep -q 'LLM navrhuje, politika rozhoduje' "$stale_audit"; then
        fail "the stale-audit AI triage must state the advisory discipline (the local model proposes, the operator decides — nothing auto-mutates)"
        return
    fi
    if ! grep -q 'scarlix-stale-audit.sh' "$REPO_DIR/install.sh" || ! grep -q 'scarlix-stale-audit.timer' "$REPO_DIR/install.sh"; then
        fail "install.sh must install and enable the stale-audit automation (script + units + timer)"
        return
    fi

    # --- README + SECURITY_MAP + release notes: the release is a release.
    if ! grep -q "What's New in v19.6.6" "$readme" || ! grep -q '\*\*v19.6.6\*\*' "$readme"; then
        fail "README must carry the v19.6.6 changelog row + What's New section"
        return
    fi
    if ! grep -q '| AI Runtime trust enumeration + family profile enforcement + stale-tree automation (KPK-19.6.5-P2-01/02, P1-03 + process) | \*\*TESTED\*\* (v19.6.6) |' "$readme"; then
        fail "README must carry the v19.6.6 roadmap row (trust enumeration + profile enforcement + stale-tree automation — TESTED)"
        return
    fi
    if ! grep -q 'KPK-19.6.5-P2-01' "$secmap" || ! grep -q 'KPK-19.6.5-P2-02' "$secmap" || ! grep -q 'KPK-19.6.5-P1-03' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must state the v19.6.6 closures by finding name (KPK-19.6.5-P2-01/P2-02/P1-03)"
        return
    fi
    if ! grep -q '## (r) Family profiles' "$secmap" || ! grep -q '## (s) Stale-tree audit' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must carry the family-profile enforcement section (r) and the stale-tree automation section (s)"
        return
    fi
    if [[ ! -f "$notes" ]]; then
        fail "docs/RELEASE_NOTES_v19.6.6.md missing (the KpK remediation release notes — the genre)"
        return
    fi
    for section in '## Scope' '## Changes' '## Verification status' '## Known limitation'; do
        if ! grep -q "$section" "$notes"; then
            fail "the release notes must carry section '$section' (the remediation genre)"
            return
        fi
    done

    pass "v19.6.6 (the v19.6.5 KpK audit remediation + the process remediation): the DEFAULT trusted dial network is Docker's DOCUMENTED default address pool ENUMERATED as fifteen CANONICAL /16 entries (172.17.0.0/16 … 172.31.0.0/16 — the non-canonical \"172.17.0.0/12\" shorthand masked to 172.16.0.0/12 and trusted 172.16.0.0/16, which is NOT a Docker default-pool range; 172.16.0.1 and 172.32.0.1 rejected by default, regression-tested by name; canonicality + contiguity proven; backward compatible with every user-defined network from the pool), service-name endpoints with unset SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS raise a loud STARTUP WARNING with the exact subnet-derivation command (UndeclaredServiceNameEndpoints, the SK-audit P2-01 guidance), the health-table commit boundary checks the caller's context UNDER THE MUTEX (ObserveBatch acquires t.mu FIRST, ctx.Err() immediately after — the linearization point; the v19.6.5 before-the-lock check let a cancellation in the lock-wait window slip its batch through; deterministic lock-contention cancellation + deadline tests with the live-context counterweight), the family profiles are TYPED/VALIDATED/ENFORCED (the full policy schema with the dual-shape TokenBudget custom unmarshaler and the ContentFilter; strict KnownFields loading with named Issues — never a silent filename fallback; enforcement at the action boundary: mode-switch can_switch/allowed-set 403 refusals by name, chat ai_chat permission + the content-filter blocklist ON THE INPUT, unloadable profiles fail closed; identity stays the caller's declaration until per-profile auth — CF-4+, honestly stated), the media-tools build context is REAL (pinned python:3.12-alpine3.20 + ffmpeg/imagemagick + a stdlib conversion service, non-root, healthchecked, loopback-published), the dependency inventory is GENERATED from the active composes and drift-guarded (44-assertion test; the 2026-10-21 audit bannered as a historical snapshot), models.yaml carries no release identity and one canonical Ollama id (qwen2.5:3b — the phantom qwen3.6:14b is gone from models.yaml AND the profiles), and the process remediation is AUTOMATED (scarlix-stale-audit.sh: the classification registry with thresholds, :latest detection, missing-build-context detection, unclassified-path findings; the weekly systemd timer, persistent; the ADVISORY AI triage over the LOCAL Ollama tier, loopback, no ambient proxy — 'LLM navrhuje, politika rozhoduje', nothing auto-mutates; the policy in STALE_TREE_AUDIT.md; install.sh wires script + units + timer), README v19.6.6 surface (changelog row + What's New + roadmap row) + SECURITY_MAP sections (r)/(s) + release notes in the remediation genre; THREE verified negative controls (reverting the pool to the /12 shorthand FAILs the enumeration marker; moving ctx.Err() above t.mu.Lock() FAILs the order assertion; removing the profile enforcement call from the mode handler FAILs the wiring marker)"
}

# ===========================================================================
# [35] v19.6.7 — the v19.6.6 KpK audit remediation: IDENTITY-BOUND family
# profiles (the P1: per-profile Bearer tokens, one shared Authorize, no
# policy-free path) + the ENFORCED monthly token budget (P2-02: reserve,
# settle, release; durable ledger) + media-tools request boundary (P2-03)
# + strict nested token_budget keys (P3-04) + stale-audit recursion (P3-05)
# + the time-bounded pin allowlist (SK P2-03).
# ===========================================================================
check_35_identity_budgets_media_tools_stale_recursion() {
    echo "--- [35] v19.6.7 (the v19.6.6 KpK audit remediation): IDENTITY-BOUND family profiles (the P1: per-profile tokens + the ONE shared Authorize + operator surfaces admin-gated) + the ENFORCED monthly token budget (P2-02) + the media-tools request boundary (P2-03) + strict nested token_budget keys (P3-04) + stale-audit recursion (P3-05) + the time-bounded pin allowlist (SK P2-03) ---"
    # v19.6.7 — the v19.6.6 KpK audit remediation: KPK-19.6.6-P1-01 (the
    # caller-declared optional profile bypass), KPK-19.6.6-P2-02 (the parsed
    # but never-enforced budget), KPK-19.6.6-P2-03 (unbounded unauthenticated
    # media-tools), KPK-19.6.6-P3-04 (nested key laxity), KPK-19.6.6-P3-05
    # (directory-mtime approximation) + the SK-audit P2-03 pin-allowlist
    # guidance. Code markers are comment-stripped — prose cannot mask code.
    local principals="$REPO_DIR/scarlihq/internal/profiles/principals.go"
    local principal_test="$REPO_DIR/scarlihq/internal/api/principal_test.go"
    local budget="$REPO_DIR/scarlihq/internal/profiles/budget.go"
    local budget_test="$REPO_DIR/scarlihq/internal/profiles/budget_test.go"
    local api_principal="$REPO_DIR/scarlihq/internal/api/principal.go"
    local loader="$REPO_DIR/scarlihq/internal/profiles/loader.go"
    local loader_test="$REPO_DIR/scarlihq/internal/profiles/loader_test.go"
    local mcp_test="$REPO_DIR/scarlihq/internal/mcp/principal_test.go"
    local main_src="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local rest_src="$REPO_DIR/scarlihq/internal/api/rest.go"
    local airuntime_src="$REPO_DIR/scarlihq/internal/api/airuntime.go"
    local media_server="$REPO_DIR/media-tools/server.py"
    local media_test="$REPO_DIR/tests/test_media_tools.sh"
    local media_compose="$REPO_DIR/media-tools/docker-compose.yml"
    local stale_audit="$REPO_DIR/files/usr/local/bin/scarlix-stale-audit.sh"
    local pin_allowlist="$REPO_DIR/docker/image-pin-allowlist.conf"
    local readme="$REPO_DIR/README.md"
    local secmap="$REPO_DIR/docs/SCARLIX_SECURITY_MAP.md"
    local notes="$REPO_DIR/docs/RELEASE_NOTES_v19.6.7.md"
    local zmor_profile="$REPO_DIR/profiles/zmor.yaml"
    local env_gen="$REPO_DIR/files/etc/systemd/system/generate-env.sh"

    for f in "$principals" "$budget" "$api_principal" "$principal_test" "$budget_test" "$mcp_test"; do
        if [[ ! -f "$f" ]]; then
            fail "v19.6.7 source missing: $f (the KPK-19.6.6 remediation surface)"
            return
        fi
    done

    # --- KPK-19.6.6-P1-01: the identity binding (the P1 core).
    local hits
    for marker in 'func NewTokenIndex' 'func (t \*TokenIndex) Resolve' 'func LoadTokenIndex' 'func ParseTokenIndex' 'type Authenticator struct' 'func (a \*Authenticator) Authorize' 'ErrProfileMismatch' 'ErrProfileUnavailable' 'func WithPrincipal' 'func PrincipalFromContext'; do
        hits=$(_strip_go_comments "$principals" | grep -c "$marker" || true)
        if [[ "$hits" -eq 0 ]]; then
            fail "the identity binding must exist ($marker — KPK-19.6.6-P1-01: tokens resolve to principals, one shared Authorize)"
            return
        fi
    done
    # The mismatch refusal is a real sentinel check, and the household token
    # is the admin principal.
    if ! grep -q 'errors.Is(decision.Deny, profiles.ErrProfileMismatch)' "$api_principal"; then
        fail "the REST boundary must map ErrProfileMismatch to its 403 class"
        return
    fi
    if ! grep -q 'ViaHouseholdToken: true' "$principals"; then
        fail "the household token must resolve to the ADMIN principal (the operator identity)"
        return
    fi
    # The shared Authorize is wired at ALL THREE boundaries (REST mode,
    # REST chat, MCP) — one authorization function, not optional checks.
    if ! grep -q 'authorizeProfile(w, r, profile, "mode.switch")' "$rest_src"; then
        fail "the REST mode boundary must decide through the shared Authorize"
        return
    fi
    if ! grep -q 'authorizeProfile(w, r, profileRef, "ai.chat")' "$airuntime_src"; then
        fail "the REST chat boundary must decide through the shared Authorize"
        return
    fi
    if ! grep -q 's.authz.Authorize(principal, profileRef, "mcp.mode_set")' "$REPO_DIR/scarlihq/internal/mcp/server.go"; then
        fail "the MCP mode-set boundary must decide through the SAME shared Authorize"
        return
    fi
    # Operator surfaces behind authAdmin (the admin-principal requirement).
    hits=$(_strip_go_comments "$rest_src" | grep -c 'h.authAdmin(' || true)
    if [[ "$hits" -lt 20 ]]; then
        fail "the operator surfaces must be registered behind h.authAdmin (found $hits — compute, agent sessions/grants/audit, bar commands/workspaces/executions/approvals, ai probe, ai propose/proposals)"
        return
    fi
    # main.go wires the token index (file + inline) and names the modes.
    if ! grep -q 'SCARLIHQ_PROFILE_TOKENS_FILE' "$main_src" || ! grep -q 'SCARLIHQ_PROFILE_TOKENS' "$main_src" || ! grep -q 'SCARLIHQ_SINGLE_USER' "$main_src"; then
        fail "main.go must wire the per-profile token sources + the explicit single-user mode"
        return
    fi
    if ! grep -q 'profile-tokens.conf' "$main_src"; then
        fail "the default token file path must be /etc/scarlix/profile-tokens.conf"
        return
    fi
    # install.sh ships the commented activation template + the budget dir.
    if ! grep -q 'profile-tokens.conf.template' "$REPO_DIR/install.sh"; then
        fail "install.sh must ship the commented profile-tokens activation template (single-user is the honest default)"
        return
    fi
    if ! grep -q '/var/lib/scarlihq-budget' "$REPO_DIR/install.sh"; then
        fail "install.sh must prepare /var/lib/scarlihq-budget (0700, uid 65532 — the ledger's home)"
        return
    fi
    if ! grep -q 'scarlihq-budget' "$REPO_DIR/scarlihq/docker-compose.yml"; then
        fail "the compose must mount /var/lib/scarlihq-budget rw (the enforced ledger)"
        return
    fi
    # The acceptance matrix exists as tests (REST + MCP).
    for tname in 'TestFamilyTokenCannotSelectAdminProfile' 'TestOmittedProfileIsDerivedFromPrincipal' 'TestHouseholdTokenActAsOnlyReduces' 'TestBrokenPrincipalProfileFailsClosed' 'TestOperatorSurfacesRefuseFamilyTokens' 'TestSingleUserNoProfilesKeepsLegacyPath'; do
        if ! grep -q "$tname" "$principal_test"; then
            fail "the acceptance matrix must exist as REST tests ($tname)"
            return
        fi
    done
    for tname in 'TestMCPFamilyTokenCannotSelectAdminProfile' 'TestMCPXoxDerivedPolicyRefusesSwitch'; do
        if ! grep -q "$tname" "$mcp_test"; then
            fail "the acceptance matrix must exist as MCP tests ($tname)"
            return
        fi
    done

    # --- KPK-19.6.6-P2-02: the enforced monthly budget.
    for marker in 'type BudgetLedger struct' 'func (l \*BudgetLedger) Reserve' 'ErrQuotaExceeded' 'ErrLedgerUnavailable' 'EstimateChatTokens' 'budgetMonthsRetained'; do
        hits=$(_strip_go_comments "$budget" | grep -c "$marker" || true)
        if [[ "$hits" -eq 0 ]]; then
            fail "the enforced budget must exist ($marker — reserve/settle/release on a durable ledger)"
            return
        fi
    done
    if ! grep -q 'StatusTooManyRequests' "$api_principal"; then
        fail "quota exhaustion must be the stable 429 carrying the quota state"
        return
    fi
    if ! grep -q 'reservation.Settle(actual, string(resp.Model), nowUTC())' "$airuntime_src"; then
        fail "the chat path must SETTLE the provider-reported usage after the response"
        return
    fi
    if ! grep -q 'reservation.Release()' "$airuntime_src"; then
        fail "failed attempts must RELEASE the reservation whole (not billed)"
        return
    fi
    if ! grep -q 'SCARLIHQ_AIRUNTIME_BUDGET_LEDGER' "$main_src"; then
        fail "main.go must wire the budget ledger env (explicit fail-closed, default degrades loudly)"
        return
    fi
    for tname in 'TestBudgetQuotaExceeded' 'TestBudgetConcurrentReservationsCannotOverspend' 'TestBudgetMonthWindowIsUTCCalendarMonth' 'TestBudgetLedgerPersistenceRoundTrip' 'TestBudgetLedgerTamperDiscipline' 'TestBudgetQuotaExceededIsStable429' 'TestBudgetSettlesProviderReportedUsage'; do
        if ! grep -q "$tname" "$budget_test" && ! grep -q "$tname" "$principal_test"; then
            fail "the budget enforcement tests must exist ($tname)"
            return
        fi
    done

    # --- KPK-19.6.6-P2-03: the media-tools request boundary.
    if [[ ! -f "$media_server" ]] || [[ ! -f "$media_test" ]]; then
        fail "media-tools must ship the hardened server + the test suite (KPK-19.6.6-P2-03)"
        return
    fi
    for marker in 'MEDIA_TOOLS_TOKEN' 'hmac.compare_digest' 'BoundedSemaphore' 'MEDIA_TOOLS_MAX_CONCURRENCY' 'output name busy' 'invalid Content-Length' 'must be a JSON object'; do
        if ! grep -q "$marker" "$media_server"; then
            fail "the media-tools hardening marker missing: $marker (KPK-19.6.6-P2-03)"
            return
        fi
    done
    if ! grep -q 'MEDIA_TOOLS_TOKEN' "$media_compose" || ! grep -q 'MEDIA_TOOLS_MAX_CONCURRENCY' "$media_compose"; then
        fail "the media-tools compose must pass the token + the concurrency bound"
        return
    fi
    if ! grep -q 'MEDIA_TOOLS_TOKEN' "$env_gen"; then
        fail "generate-env.sh must generate MEDIA_TOOLS_TOKEN (fail-closed without it)"
        return
    fi

    # --- KPK-19.6.6-P3-04: strict nested token_budget keys.
    if ! grep -q 'UNKNOWN key' "$loader" || ! grep -q 'DUPLICATE' "$loader"; then
        fail "TokenBudget.UnmarshalYAML must reject unknown/duplicate nested keys BY NAME (KPK-19.6.6-P3-04)"
        return
    fi
    if ! grep -q 'TestTokenBudgetNestedStrictValidation' "$loader_test"; then
        fail "the nested-strictness negative tests must exist"
        return
    fi

    # --- KPK-19.6.6-P3-05 + SK P2-03: the stale-audit pass.
    if ! grep -q '%T@' "$stale_audit" || ! grep -q 'newest-file recursive' "$stale_audit"; then
        fail "the stale-audit must compute the recursive newest-file mtime for installed trees (KPK-19.6.6-P3-05)"
        return
    fi
    if ! grep -q 'compose.yaml' "$stale_audit" || ! grep -q 'compose.yml' "$stale_audit"; then
        fail "the stale-audit must discover the compose short forms"
        return
    fi
    if [[ ! -f "$pin_allowlist" ]] || ! grep -q 'review-by' "$pin_allowlist"; then
        fail "the time-bounded pin allowlist must exist (docker/image-pin-allowlist.conf — SK P2-03)"
        return
    fi
    if ! grep -q 'SCARLIX_STALE_AUDIT_PIN_ALLOWLIST' "$stale_audit"; then
        fail "the stale-audit must honor the allowlist override env"
        return
    fi
    if ! grep -q 'UNPINNED-ALLOWED' "$stale_audit"; then
        fail "allowlisted :latest images must be reported as UNPINNED-ALLOWED info (not findings)"
        return
    fi

    # --- zmor carries the FULL mode enumeration (the derived admin policy
    # must not restrict the operator).
    if ! grep -q 'allowed: \[ai, stop, game, creative, turbo, offline, tv\]' "$zmor_profile"; then
        fail "profiles/zmor.yaml must allow the FULL mode enumeration (the derived admin policy is the operator's own rights)"
        return
    fi

    # --- README + SECURITY_MAP + release notes: the release is a release.
    # (v19.6.8 evolution: the badge is the CURRENT release's surface —
    # asserted by check_28; here the v19.6.7 What's New section stays as the
    # permanent record.)
    if ! grep -q "What's New in v19.6.7" "$readme"; then
        fail "README must carry the v19.6.7 What's New section (the permanent record)"
        return
    fi
    if ! grep -q 'KPK-19.6.6-P1-01' "$secmap" || ! grep -q 'KPK-19.6.6-P2-02' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must state the v19.6.7 closures by finding name (KPK-19.6.6-P1-01/P2-02)"
        return
    fi
    if ! grep -q '## (t) Enforced token budgets' "$secmap"; then
        fail "SCARLIX_SECURITY_MAP.md must carry the enforced-budgets section (t)"
        return
    fi
    if [[ ! -f "$notes" ]]; then
        fail "docs/RELEASE_NOTES_v19.6.7.md missing (the remediation release notes — the genre)"
        return
    fi
    for section in '## Scope' '## Changes' '## Verification status' '## Known limitation'; do
        if ! grep -q "$section" "$notes"; then
            fail "the release notes must carry section '$section' (the remediation genre)"
            return
        fi
    done

    pass "v19.6.7 (the v19.6.6 KpK audit remediation): the family profiles are IDENTITY-BOUND (per-profile Bearer tokens — /etc/scarlix/profile-tokens.conf with the 0400/640 discipline, collisions and household reuse refused at construction; the household SCARLIHQ_TOKEN resolves to the ADMIN principal; the ONE shared profiles.Authenticator.Authorize decides REST /api/mode + /api/ai/chat + MCP scarlix_mode_set — a family token cannot select a foreign profile (403 ErrProfileMismatch, header entry path identical), an OMITTED reference is DERIVED from the principal (no policy-free path in multi-user mode), an admin act-as only REDUCES privileges; operator surfaces behind h.authAdmin refuse family tokens 403; audit events carry principal/profile/action/decision — never tokens; single-user deployments are named as such — explicit SCARLIHQ_SINGLE_USER or the implicit default with the activation pointer; no-profiles deployments keep the legacy path, named), the monthly token budget is ENFORCED (durable ledger 0600/0700 with atomic tmp+rename persist and the grants.json tamper discipline; reserve-before-forward with the runes/4+4096 estimate, settle the provider-reported usage — estimate when the provider reports none, release whole on failure; the UTC calendar-month window with 2-month retention; accounting per profile across models; exhaustion is the stable 429 with the quota state; ledger unavailability fails CLOSED 503; without a writable ledger the budgets degrade to loudly-warned informational metadata — the documented policy), media-tools is AUTHENTICATED AND BOUNDED (MEDIA_TOOLS_TOKEN bearer, hmac.compare_digest, fail-closed 503 without it, /health open; BoundedSemaphore with MEDIA_TOOLS_MAX_CONCURRENCY clamp 1..16 and the 15s queue wait; per-client IP cap; output-name collision 409; invalid Content-Length and non-object JSON root are deliberate 400s), the nested token_budget schema is closed BY HAND (unknown/duplicate keys are named errors), the stale-audit reads the NEWEST FILE mtime recursively for installed trees (caches excluded, plain files keep stat) and discovers the compose short forms, and the experimental :latest rides a TIME-BOUNDED allowlist (image|reason|review-by — expired entries revert to UNPINNED findings, the four images reviewed by 2027-01-31); acceptance: principal_test.go (12 REST) + mcp/principal_test.go (6) + budget_test.go (8) + tests/test_media_tools.sh (42) + loader nested-strictness tests; residual honesty: hardware OPEN, keyword-level content filter, budget = the chat path (propose is admin-gated), IPv6 dual-stack"
}

# ===========================================================================
# [36] v19.6.8 — Compute Fabric Phase 0 (the stabilization roadmap §4):
# the lifecycle-vocabulary correction (the agent compute intake's actions
# are start/release; the legacy "reserve" spelling is rejected BY NAME) +
# the additive state machine (STOP_REQUESTED + RECONCILE_REQUIRED, the
# frozen contract's permitted addition) + the canonical architecture docs
# + the frozen baseline record. Code markers are comment-stripped — prose
# cannot mask code. Negative controls (each break FAILs the check):
#   (a) reverting the action vocabulary — making "reserve" discriminate
#       the workload-start call again — trips the action=="reserve"
#       absence assertion;
#   (b) removing the RECONCILE_REQUIRED wiring from the stop path or the
#       release scanner — trips the ambiguity/scanner markers;
#   (c) removing a canonical architecture doc or the historical banners —
#       trips the documentation reconciliation markers.
# ===========================================================================
check_36_compute_fabric_phase0_lifecycle() {
    echo "--- [36] v19.6.8 (Compute Fabric Phase 0 — the stabilization roadmap §4): the lifecycle-vocabulary correction (agent compute intake start/release, 'reserve' rejected by name) + the additive state machine (STOP_REQUESTED + RECONCILE_REQUIRED) + the reservation release scanner coverage + the canonical architecture docs + the frozen baseline ---"
    local agentcompute="$REPO_DIR/scarlihq/internal/api/agentcompute.go"
    local agentcompute_test="$REPO_DIR/scarlihq/internal/api/agentcompute_test.go"
    local states="$REPO_DIR/scarlihq/internal/allocation/states.go"
    local transitions="$REPO_DIR/scarlihq/internal/allocation/transitions.go"
    local state_test="$REPO_DIR/scarlihq/internal/allocation/state_machine_test.go"
    local manager="$REPO_DIR/scarlihq/internal/workload/manager.go"
    local manager_test="$REPO_DIR/scarlihq/internal/workload/manager_test.go"
    local scanner="$REPO_DIR/scarlihq/internal/reservation/allocation_adapter.go"
    local scanner_test="$REPO_DIR/scarlihq/internal/reservation/interrupted_release_recovery_test.go"
    local audit="$REPO_DIR/scarlihq/internal/agentkernel/audit.go"
    local arch_canon="$REPO_DIR/docs/architecture/SCARLIXOS_CURRENT_ARCHITECTURE.md"
    local arch_cf="$REPO_DIR/docs/architecture/COMPUTE_FABRIC_CONTRACT.md"
    local arch_air="$REPO_DIR/docs/architecture/AI_RUNTIME_CONTRACT.md"
    local baseline="$REPO_DIR/docs/releases/v19.6.8/BASELINE.md"
    local roadmap="$REPO_DIR/docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md"
    local notes="$REPO_DIR/docs/RELEASE_NOTES_v19.6.8.md"
    local hist_arch="$REPO_DIR/docs/SCARLIX_CURRENT_ARCHITECTURE.md"
    local hist_old="$REPO_DIR/docs/ARCHITECTURE.md"
    local hist_gpu="$REPO_DIR/docs/SCARLIX_GPU_MAP.md"
    local hist_runtime="$REPO_DIR/docs/SCARLIX_RUNTIME_MAP.md"

    for f in "$agentcompute" "$agentcompute_test" "$states" "$transitions" "$state_test" \
        "$manager" "$manager_test" "$scanner" "$scanner_test" "$audit"; do
        if [[ ! -f "$f" ]]; then
            fail "v19.6.8 source missing: $f (the Compute Fabric Phase 0 surface)"
            return
        fi
    done

    # --- The lifecycle-vocabulary correction (roadmap §4.3, the §1.3-8
    # semantic gap): the actions are start/release; "reserve" is rejected
    # BY NAME. Negative control (a): the pre-v19.6.8 shape — "reserve"
    # discriminating the workload-start call — must be GONE.
    local stripped36
    stripped36=$(_strip_go_comments "$agentcompute")
    if ! printf '%s\n' "$stripped36" | grep -q 'case "start":'; then
        fail "the agent compute intake must carry the honest start action (roadmap §4.3: do not retain a misleading API action name)"
        return
    fi
    if ! printf '%s\n' "$stripped36" | grep -q 'case "reserve":'; then
        fail "the legacy reserve spelling must be REJECTED BY NAME at the intake (a named 400, never a silent alias)"
        return
    fi
    if printf '%s\n' "$stripped36" | grep -q 'action == "reserve"'; then
        fail "the reserve action must NOT discriminate the workload-start call anymore (the v19.6.8 correction — negative control (a): this exact regression)"
        return
    fi
    if ! grep -q 'not available at this boundary' "$agentcompute" || ! grep -q 'scarlix-reservation' "$agentcompute"; then
        fail "the reserve refusal must name the boundary AND where reservations are created (the reservation service)"
        return
    fi
    if ! grep -q 'TestAgentComputeReserveActionRejectedByName' "$agentcompute_test"; then
        fail "the named-refusal regression test must exist (agentcompute_test.go)"
        return
    fi
    # The start action carries its own honest audit type; the release type
    # is unchanged; the legacy compute.reserve type stays VALID (append-only
    # audit history).
    if ! grep -q 'AuditTypeComputeStart AuditLineType = "compute.start"' "$audit"; then
        fail "the audit contract must carry the honest compute.start type (v19.6.8)"
        return
    fi
    local audit_hits36
    audit_hits36=$(_strip_go_comments "$audit" | grep -c 'AuditTypeComputeReserve, AuditTypeComputeRelease, AuditTypeComputeStart' || true)
    if [[ "$audit_hits36" -eq 0 ]]; then
        fail "the audit validation must gate compute.start identically to the compute lifecycle types"
        return
    fi

    # --- The additive state machine (roadmap §4.4): STOP_REQUESTED +
    # RECONCILE_REQUIRED join the FROZEN table (an addition, never a
    # rename/removal).
    if ! grep -q 'StateStopRequested     State = "STOP_REQUESTED"' "$states" && ! grep -qE 'StateStopRequested +State = "STOP_REQUESTED"' "$states"; then
        fail "the STOP_REQUESTED state must exist in the frozen enumeration (the v19.6.8 additive amendment)"
        return
    fi
    if ! grep -q 'StateReconcileRequired State = "RECONCILE_REQUIRED"' "$states" && ! grep -qE 'StateReconcileRequired +State = "RECONCILE_REQUIRED"' "$states"; then
        fail "the RECONCILE_REQUIRED state must exist in the frozen enumeration (the v19.6.8 additive amendment)"
        return
    fi
    local state_count36
    state_count36=$(_strip_go_comments "$states" | grep -c 'State = "' || true)
    if [[ "$state_count36" -ne 15 ]]; then
        fail "the state enumeration must count exactly 15 states (13 frozen + 2 additive; got $state_count36 — the drift guard)"
        return
    fi
    # The transition table carries the new states with their OUT-edges.
    local trans_stop36 trans_recon36
    trans_stop36=$(_strip_go_comments "$transitions" | grep -c 'StateStopRequested: {' || true)
    trans_recon36=$(_strip_go_comments "$transitions" | grep -c 'StateReconcileRequired: {' || true)
    if [[ "$trans_stop36" -eq 0 || "$trans_recon36" -eq 0 ]]; then
        fail "the transition table must carry the two additive states"
        return
    fi
    # STOP_REQUESTED never jumps to RELEASED (no terminal shortcut — the
    # KPK-01 discipline extends to the new vocabulary).
    local stopreq_block36
    stopreq_block36=$(awk '/StateStopRequested: \{/,/\},$/' "$transitions")
    if printf '%s\n' "$stopreq_block36" | grep -q 'StateReleased'; then
        fail "STOP_REQUESTED must not transition to RELEASED directly (release only through RELEASING + verified cleanup)"
        return
    fi
    # RECONCILE_REQUIRED never jumps to RELEASED either.
    local reconcile_block36
    reconcile_block36=$(awk '/StateReconcileRequired: \{/,/\},$/' "$transitions")
    if printf '%s\n' "$reconcile_block36" | grep -q 'StateReleased'; then
        fail "RECONCILE_REQUIRED must not transition to RELEASED directly (a reconciler decides, then RELEASING + verified cleanup)"
        return
    fi
    if ! grep -q 'expected := 15' "$state_test"; then
        fail "the state-machine test must pin the 15-state count"
        return
    fi

    # --- The workload manager wiring (negative control (b)): the stop
    # request is durable BEFORE the driver call; the ambiguous outcome
    # becomes RECONCILE_REQUIRED; the retry path accepts STOP_REQUESTED.
    if ! grep -q 'allocation.StateStopRequested, "workload stop requested"' "$manager"; then
        fail "Manager.Stop must record STOP_REQUESTED durably before the driver call"
        return
    fi
    if ! grep -q 'StateReconcileRequired, "workload stop outcome ambiguous"' "$manager"; then
        fail "Manager.Stop must map the ambiguous outcome to RECONCILE_REQUIRED (roadmap §4.3: a reconciler decides, never a blind retry)"
        return
    fi
    if ! grep -q 'ambiguous := stopErr == nil' "$manager"; then
        fail "the ambiguity discriminator must exist in the stop failure branch (stopErr == nil with an unverified result)"
        return
    fi
    if ! grep -q 'case allocation.StateStopRequested, allocation.StateReleasing:' "$manager"; then
        fail "the idempotent retry path must accept STOP_REQUESTED (a crash between the request and the outcome resumes)"
        return
    fi
    if ! grep -q 'TestStopRecordsStopRequestedBeforeDriverCall' "$manager_test" || ! grep -q 'TestStopAmbiguousOutcomeBecomesReconcileRequired' "$manager_test" || ! grep -q 'TestStopRetryFromStopRequestedCompletes' "$manager_test"; then
        fail "the v19.6.8 stop-path regression tests must exist (ordering + ambiguity + retry)"
        return
    fi

    # --- The reservation release scanner covers RECONCILE_REQUIRED (the
    # ambiguous allocation is release-lifecycle work — it must never
    # strand) but NOT the transient STOP_REQUESTED.
    local scan_recon36 scan_stop36
    scan_recon36=$(_strip_go_comments "$scanner" | grep -c 'StateReconcileRequired' || true)
    scan_stop36=$(_strip_go_comments "$scanner" | grep -c 'StateStopRequested' || true)
    if [[ "$scan_recon36" -eq 0 ]]; then
        fail "ListReleasingAllocations must scan RECONCILE_REQUIRED allocations (negative control (b): removing this strands ambiguous outcomes forever)"
        return
    fi
    if [[ "$scan_stop36" -gt 0 ]]; then
        fail "ListReleasingAllocations must NOT scan STOP_REQUESTED (the stop is still in flight — release comes after the verified stop)"
        return
    fi
    if ! grep -q 'alloc-reconcile' "$scanner_test"; then
        fail "the release-scan test must cover the RECONCILE_REQUIRED source state"
        return
    fi

    # --- The documentation reconciliation (roadmap §4.2, negative control
    # (c)): the canonical trio + the historical banners + the baseline +
    # the in-repo roadmap.
    for f in "$arch_canon" "$arch_cf" "$arch_air" "$baseline" "$roadmap" "$notes"; do
        if [[ ! -f "$f" ]]; then
            fail "v19.6.8 documentation missing: $f (the Phase 0 documentation reconciliation)"
            return
        fi
    done
    if ! grep -q 'CANONICAL' "$arch_canon" || ! grep -q 'CANONICAL' "$arch_cf" || ! grep -q 'CANONICAL' "$arch_air"; then
        fail "the architecture trio must carry the CANONICAL status banner"
        return
    fi
    if ! grep -q 'start.*release' "$arch_cf" || ! grep -q 'rejected by name' "$arch_cf"; then
        fail "COMPUTE_FABRIC_CONTRACT.md must state the start/release vocabulary and the named reserve refusal"
        return
    fi
    for f in "$hist_arch" "$hist_old" "$hist_gpu" "$hist_runtime"; do
        if ! grep -q 'Historical snapshot' "$f"; then
            fail "$f must carry the Historical snapshot banner (stale architecture docs are marked, roadmap §4.2)"
            return
        fi
    done
    if ! grep -q '292b3d877b4f1f0b727f6286b7972096b07af512' "$baseline"; then
        fail "the v19.6.8 baseline record must freeze the pre-change repository SHA (roadmap §4.1)"
        return
    fi
    for section in '## Scope' '## Changes' '## Verification status'; do
        if ! grep -q "$section" "$notes"; then
            fail "the v19.6.8 release notes must carry section '$section' (the release genre)"
            return
        fi
    done

    pass "v19.6.8 (Compute Fabric Phase 0 — the stabilization roadmap §4): the lifecycle vocabulary is HONEST (the agent compute intake's actions are start/release; the legacy \"reserve\" spelling is rejected BY NAME with the boundary and the reservation service named — it never reserved anything there, it started a workload the reservation service had already reserved; the kernel capability name compute.reserve stays for grants.json compatibility, the audit type is the honest compute.start), the state machine gains the ADDITIVE states STOP_REQUESTED + RECONCILE_REQUIRED (15 states, the frozen contract's permitted addition — no rename, no removal, no redirect: STOP_REQUESTED records the stop request durably BEFORE the driver call and never releases capacity itself; RECONCILE_REQUIRED marks the ambiguous outcome — a reconciler decides, never a blind retry; neither jumps to RELEASED — the KPK-01 verified-cleanup gate is unchanged), Manager.Stop maps verified → RELEASING / explicit failure → CLEANUP_REQUIRED / ambiguous → RECONCILE_REQUIRED with the idempotent retry accepting STOP_REQUESTED, the reservation release scanner covers RECONCILE_REQUIRED (never strands) but not the transient STOP_REQUESTED, and the documentation is RECONCILED (the canonical docs/architecture/ trio + historical-snapshot banners on the four stale architecture docs + the frozen baseline record + the in-repo roadmap to v20); acceptance: the named-refusal + ordering + ambiguity + retry regression tests, the 15-state count guard, the scanner source-state test; residual honesty: hardware acceptance stays OPEN (KPK-06), the workload bridge stays disabled until the P5.1 target-host matrix passes, aggregate admission stays conservative (one allocation per GPU/port) until v19.7.1"
}


# check_37 — Unified resource inventory + model execution metadata
# (v19.7.0, Compute Fabric Phase 1, roadmap §5 of
# docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md):
# the storage inventory package + the modelmeta manifest contract + the v2
# snapshot envelope (freshness + provenance) + the read-only API/CLI
# surface. The release's must-not-do — "do not schedule from unverified
# estimates" — is enforced by the placement gate's existence.
check_37_unified_inventory_v2() {
    echo "--- [37] v19.7.0 (Compute Fabric Phase 1 — the roadmap §5 unified resource inventory): the storage package (read-only discovery, opt-in bounded benchmark) + the modelmeta manifest contract (fail-closed validation + the placement gate) + the v2 snapshot (freshness + provenance) + the read-only API/CLI surface ---"
    local storage_types="$REPO_DIR/scarlihq/internal/storage/storage.go"
    local storage_disc="$REPO_DIR/scarlihq/internal/storage/discovery.go"
    local storage_bench="$REPO_DIR/scarlihq/internal/storage/benchmark.go"
    local storage_disc_test="$REPO_DIR/scarlihq/internal/storage/discovery_test.go"
    local storage_bench_test="$REPO_DIR/scarlihq/internal/storage/benchmark_test.go"
    local modelmeta_types="$REPO_DIR/scarlihq/internal/modelmeta/modelmeta.go"
    local modelmeta_manifest="$REPO_DIR/scarlihq/internal/modelmeta/manifest.go"
    local modelmeta_test="$REPO_DIR/scarlihq/internal/modelmeta/manifest_test.go"
    local inv_v2="$REPO_DIR/scarlihq/internal/inventory/v2.go"
    local inv_v2_test="$REPO_DIR/scarlihq/internal/inventory/v2_test.go"
    local inv_types="$REPO_DIR/scarlihq/internal/inventory/types.go"
    local api_v2="$REPO_DIR/scarlihq/internal/api/inventoryv2.go"
    local api_v2_test="$REPO_DIR/scarlihq/internal/api/inventoryv2_test.go"
    local api_rest="$REPO_DIR/scarlihq/internal/api/rest.go"
    local cli_inv="$REPO_DIR/scarlihq/cmd/scarlix-inventory/main.go"
    local inv_contract="$REPO_DIR/docs/INVENTORY_V2_CONTRACT.md"
    local rel_notes="$REPO_DIR/docs/RELEASE_NOTES_v19.7.0.md"
    local example_manifest="$REPO_DIR/models.d/example-moe-model.manifest.yaml"
    local version_file="$REPO_DIR/VERSION"
    local readme="$REPO_DIR/README.md"

    for f in "$storage_types" "$storage_disc" "$storage_bench" "$storage_disc_test" "$storage_bench_test" \
        "$modelmeta_types" "$modelmeta_manifest" "$modelmeta_test" "$inv_v2" "$inv_v2_test" \
        "$api_v2" "$api_v2_test" "$cli_inv" "$inv_contract" "$rel_notes" "$example_manifest"; do
        if [[ ! -f "$f" ]]; then
            fail "v19.7.0 source missing: $f (the Compute Fabric Phase 1 surface)"
            return
        fi
    done

    # --- The READ-ONLY charter (negative control (a)): discovery must
    # never fork. Comment-stripped — prose cannot mask code.
    local stripped_disc
    stripped_disc=$(_strip_go_comments "$storage_disc")
    if printf '%s\n' "$stripped_disc" | grep -q 'os/exec'; then
        fail "storage discovery must not import os/exec — the read-only charter (no forks, no writes, no benchmarks on the discovery path)"
        return
    fi
    if printf '%s\n' "$stripped_disc" | grep -qE 'RunReadBenchmark|BenchmarkOptions'; then
        fail "discovery must never reference the benchmark — the benchmark is opt-in only (roadmap §5.2 rule 2)"
        return
    fi
    # The ONLY in-repo caller of RunReadBenchmark outside the storage
    # package is the CLI's --storage-benchmark door (negative control
    # (b): an automatic benchmark caller would be a charter break).
    local bench_callers
    bench_callers=$(grep -rl 'RunReadBenchmark' "$REPO_DIR/scarlihq" --include='*.go' | grep -v '_test.go' | grep -v 'internal/storage/' || true)
    if [[ -n "$bench_callers" && "$bench_callers" != "$cli_inv" ]]; then
        fail "RunReadBenchmark has callers beyond the CLI diagnostic door: $bench_callers (the benchmark must be operator-opt-in only)"
        return
    fi
    if ! grep -q 'storage-benchmark' "$cli_inv"; then
        fail "the CLI must carry the explicit --storage-benchmark door (the operator diagnostic)"
        return
    fi

    # --- The benchmark bounds are FAIL-CLOSED (roadmap §5.2 rule 3):
    # named errors, never silent clamps; required options have no defaults.
    local stripped_bench
    stripped_bench=$(_strip_go_comments "$storage_bench")
    if ! printf '%s\n' "$stripped_bench" | grep -q 'MinBenchmarkDuration'; then
        fail "the benchmark must define the duration bounds (roadmap §5.2 rule 3: bounded duration)"
        return
    fi
    if ! printf '%s\n' "$stripped_bench" | grep -q 'MaxBenchmarkFileSize'; then
        fail "the benchmark must define the file-size bounds (roadmap §5.2 rule 3: bounded test file size)"
        return
    fi
    if ! printf '%s\n' "$stripped_bench" | grep -q 'errBound'; then
        fail "the benchmark bounds must fail CLOSED with named errors (never a silent clamp)"
        return
    fi
    if ! grep -q 'TestBenchmarkOptionBounds' "$storage_bench_test" || ! grep -q 'TestBenchmarkRunAndCleanup' "$storage_bench_test" || ! grep -q 'TestBenchmarkSeamIsolation' "$storage_bench_test"; then
        fail "the benchmark regression tests must exist (bounds + run/cleanup + seam isolation)"
        return
    fi

    # --- Identity without serials (roadmap §5.2 rule 5): StableID is a
    # digest; the Device struct carries NO raw serial field.
    if ! printf '%s\n' "$stripped_disc" | grep -q 'func stableDeviceID'; then
        fail "the pseudonymous stable identity function must exist (stable hardware identity where available)"
        return
    fi
    if grep -qE 'Serial[[:space:]]+string[[:space:]]+`json:"serial' "$storage_types"; then
        fail "the Device struct must NOT expose a raw serial field (roadmap §5.2 rule 5 — serials stay out of inventory responses)"
        return
    fi
    # Unknown ≠ zero: temperature is a POINTER (nil = not measured).
    if ! grep -q 'TemperatureC[[:space:]]*\*float64' "$storage_types"; then
        fail "device temperature must be a pointer — nil = NOT MEASURED (unknown ≠ zero, roadmap §5.2 rule 6)"
        return
    fi

    # --- The modelmeta fail-closed contract: the versioned schema, the
    # named refusals, the placement gate (the release's must-not-do:
    # unknown required values never become automatic placement).
    if ! grep -q 'SchemaVersionV2 = "scarlix.model/v2"' "$modelmeta_manifest"; then
        fail "the manifest schema must be versioned scarlix.model/v2"
        return
    fi
    local stripped_mm
    stripped_mm=$(_strip_go_comments "$modelmeta_manifest")
    if ! printf '%s\n' "$stripped_mm" | grep -q 'is not a 64-hex-char'; then
        fail "the checkpoint fingerprint validation must refuse non-64-hex values BY NAME"
        return
    fi
    if ! printf '%s\n' "$stripped_mm" | grep -q 'expert_count > 0'; then
        fail "MoE manifests must require expert geometry (unknown geometry blocks placement)"
        return
    fi
    if ! printf '%s\n' "$stripped_mm" | grep -q 'func PlacementEligible'; then
        fail "the placement-eligibility gate must exist (roadmap §5.5: required unknown values must not be used for automatic placement)"
        return
    fi
    if ! grep -q 'TestValidateManifestFailClosed' "$modelmeta_test" || ! grep -q 'TestPlacementEligible' "$modelmeta_test"; then
        fail "the modelmeta regression tests must exist (validation truth table + eligibility matrix)"
        return
    fi
    # The example manifest is DELIBERATELY invalid (placeholders) — it
    # must carry the honesty note so it can never be mistaken for metadata.
    if ! grep -q 'DELIBERATELY INVALID' "$example_manifest"; then
        fail "the example manifest must declare its deliberate invalidity (placeholder values are documentation, never metadata)"
        return
    fi

    # --- The v2 snapshot envelope: schema version + snapshot ID +
    # observed_at + the freshness contract + per-source provenance.
    local stripped_v2
    stripped_v2=$(_strip_go_comments "$inv_v2")
    if ! printf '%s\n' "$stripped_v2" | grep -q 'SchemaVersionV2 = "scarlix.inventory/v2"'; then
        fail "the v2 snapshot schema must be versioned scarlix.inventory/v2"
        return
    fi
    if ! printf '%s\n' "$stripped_v2" | grep -q 'func (s \*InventorySnapshotV2) FreshWithin'; then
        fail "the freshness policy hook must exist (roadmap §5.6: planners reject or downgrade stale data)"
        return
    fi
    if ! printf '%s\n' "$stripped_v2" | grep -q 'if maxAge <= 0'; then
        fail "a non-positive freshness policy must be always-stale (fail-closed: an unset policy must not bless data)"
        return
    fi
    if ! printf '%s\n' "$stripped_v2" | grep -q 'func BuildSnapshotV2'; then
        fail "the snapshot composer must exist"
        return
    fi
    if ! grep -q 'TestV1FrozenContractIntact' "$inv_v2_test" || ! grep -q 'TestFreshnessMath' "$inv_v2_test"; then
        fail "the v2 regression tests must exist (v1 frozen-key re-pin + freshness math)"
        return
    fi

    # --- The read-only API surface (negative control (c)): three GET
    # routes behind the standard read gate; non-GET is 405 BY NAME; there
    # is NO write surface (no benchmark trigger, no reservation).
    if ! grep -q 'mux.HandleFunc("/api/inventory/v2", h.auth(h.inventoryV2Full))' "$api_rest"; then
        fail "GET /api/inventory/v2 must be registered behind the standard Bearer read gate"
        return
    fi
    if ! grep -q 'mux.HandleFunc("/api/inventory/v2/storage", h.auth(h.inventoryV2Storage))' "$api_rest"; then
        fail "GET /api/inventory/v2/storage must be registered (the storage section)"
        return
    fi
    if ! grep -q 'mux.HandleFunc("/api/inventory/v2/models", h.auth(h.inventoryV2Models))' "$api_rest"; then
        fail "GET /api/inventory/v2/models must be registered (the model execution views)"
        return
    fi
    local stripped_api
    stripped_api=$(_strip_go_comments "$api_v2")
    local method_guards
    method_guards=$(printf '%s\n' "$stripped_api" | grep -c 'r.Method != http.MethodGet' || true)
    if [[ "$method_guards" -lt 3 ]]; then
        fail "all THREE inventory endpoints must guard non-GET with 405 (got $method_guards guards — the read-only negative control)"
        return
    fi
    if ! printf '%s\n' "$stripped_api" | grep -q 'inventory is read-only'; then
        fail "the 405 refusal must be BY NAME (the gaming-inventory precedent)"
        return
    fi
    if ! printf '%s\n' "$stripped_api" | grep -q 'SCARLIHQ_INVENTORY_MAX_AGE'; then
        fail "the freshness policy knob must exist (SCARLIHQ_INVENTORY_MAX_AGE)"
        return
    fi
    if ! grep -q 'TestInventoryV2RejectsNonGET' "$api_v2_test" || ! grep -q 'TestInventoryV2StaleMarking' "$api_v2_test"; then
        fail "the API regression tests must exist (the 405 negative control + stale marking)"
        return
    fi
    # The CLI mirrors must exist.
    if ! grep -q 'v2-storage' "$cli_inv" || ! grep -q 'v2-models' "$cli_inv"; then
        fail "the CLI mirrors (--v2-storage/--v2-models) must exist"
        return
    fi

    # --- The v1 frozen contract is UNTOUCHED (negative control (d)):
    # the frozen types file still carries the FROZEN banner + the
    # internal-UUID exclusion, and the v19.0.7 keys stand.
    if ! grep -q 'FROZEN in v19.0.7' "$inv_types"; then
        fail "the v1 types file must carry the frozen-contract banner (v2 is additive; v1 keys never renamed)"
        return
    fi
    if ! grep -q 'deliberately excluded from frozen public inventory JSON' "$inv_types"; then
        fail "the internal GPU UUID must stay excluded from public inventory JSON"
        return
    fi

    # --- The version bump + the release documentation (v19.7.2 evolution:
    # the current-release guard moved with the release — the v19.7.0
    # record assertions below stay as history).
    if [[ "$(cat "$version_file" | tr -d '[:space:]')" != "19.7.2" ]]; then
        fail "VERSION must be 19.7.2"
        return
    fi
    for f in "$REPO_DIR/install.sh" "$REPO_DIR/scarlihq/Dockerfile" "$REPO_DIR/scarlihq/cmd/scarlihq/main.go" "$api_rest"; do
        if ! grep -q '19\.7\.2' "$f"; then
            fail "version fallback missing in $f (the five-place bump discipline)"
            return
        fi
    done
    for section in '## Scope' '## Changes' '## Verification' '## Known limitations'; do
        if ! grep -q "$section" "$rel_notes"; then
            fail "the v19.7.0 release notes must carry section '$section' (the release genre)"
            return
        fi
    done
    if ! grep -q "What's New in v19.7.0" "$readme" || ! grep -q '| \*\*v19.7.0\*\*' "$readme"; then
        fail "the README must carry the v19.7.0 What's New section + changelog row"
        return
    fi
    if ! grep -q 'scarlix.inventory/v2' "$inv_contract"; then
        fail "the INVENTORY_V2_CONTRACT must document the v2 schema identity"
        return
    fi

    pass "v19.7.0 (Compute Fabric Phase 1 — the roadmap §5 unified resource inventory): the storage package is READ-ONLY (discovery from /proc/self/mounts + /sys/block + statfs: no exec, no writes, no benchmarks — the charter asserted comment-stripped at the source level; the ONLY benchmark door is the CLI's --storage-benchmark with FAIL-CLOSED bounds — duration 1s..30s, size 32MiB..1GiB, block 4KiB..8MiB, named errors, never silent clamps; discovery never references it), identity carries NO raw serials (StableID is a SHA-256 digest of identity material), unknown stays unknown (temperature is a pointer — null = not measured), unmounted devices report RAW capacity with available=0 (an LVM PV can never be mistaken for admission capacity), swap is REPORTED never ADMITTED; the modelmeta package carries the versioned scarlix.model/v2 manifest contract (fail-closed validation with named reasons: schema refusal BY NAME, 64-hex checkpoint fingerprints, MoE geometry requirements, an OPEN backend-slug set — declaring the future infer_ssd is legal, the planner intersects with live runtimes — and expert packs bound to ONE compatible backend) plus the PLACEMENT GATE (the release's must-not-do honored in code: unknown required values NEVER become automatic placement — MoE with unknown expert/shared bytes is ineligible BY NAME; the example manifest is DELIBERATELY invalid documentation); the v2 snapshot envelope (scarlix.inventory/v2) carries schema version + snapshot ID + observed_at + host/storage/GPU/runtime/model views + per-source provenance (ok|partial|empty|unavailable — silence is never an answer) + the SERVE-TIME freshness marker (a non-positive policy is always-stale — fail-closed; consumers must reject/downgrade stale data, never reuse old capacity values); the API surface is three GET-only routes behind the standard Bearer read gate with 405-BY-NAME refusals (no write surface, no benchmark trigger, no reservation — the route set enforces the roadmap's inventory boundary) and the CLI mirrors; the v1 contracts are FROZEN and untouched (the frozen banner + the internal-UUID exclusion stand; the v1 tests pass unmodified); acceptance: the storage/modelmeta/inventory/api regression suites (the read-only charter source assertion, the benchmark bounds fail-closed table + cleanup + seam isolation, the 17-case validation truth table, the eligibility matrix, the freshness math, the v1 frozen-key re-pin, the 401/405 negative controls, stale marking, the real-collector end-to-end); residual honesty: hardware acceptance stays OPEN (KPK-06), no verified manifests ship for the real models yet (manifest_status: none + placement-ineligible — the honest default), LVM transport reports unknown, the v19.7.1 resource vector + v19.7.2 planner are the next milestones"
}

# ===========================================================================
# check_38 — v19.7.1 (Compute Fabric Phase 2): the aggregate admission
# gate (resource vector + atomic reserve + durable ledger) + the
# KPK-19.7.0 audit remediations (P2-01..P2-05, P3-05) + the budget
# crash-safe reservations (KPK-19.6.7-carry-forward-P2-06)
# ===========================================================================
check_38_aggregate_admission_v19_7_1() {
    echo "--- [38] v19.7.1 (Compute Fabric Phase 2 — roadmap §6): the resource vector + the aggregate admission gate (decide + atomically reserve, durable ledger, lease + boot reconciliation) + the KPK-19.7.0 remediations (storage identity, GPU empty-vs-unavailable, benchmark hardening, manifest collision, remount-ro) + the crash-safe budget reservations ---"
    local adm_vector="$REPO_DIR/scarlihq/internal/admission/vector.go"
    local adm_manager="$REPO_DIR/scarlihq/internal/admission/manager.go"
    local adm_ledger="$REPO_DIR/scarlihq/internal/admission/ledger.go"
    local adm_contract="$REPO_DIR/scarlihq/internal/admission/admission.go"
    local adm_prod="$REPO_DIR/scarlihq/internal/admission/production.go"
    local adm_tests="$REPO_DIR/scarlihq/internal/admission/manager_test.go"
    local api_admission="$REPO_DIR/scarlihq/internal/api/admission.go"
    local api_admission_test="$REPO_DIR/scarlihq/internal/api/admission_test.go"
    local api_rest="$REPO_DIR/scarlihq/internal/api/rest.go"
    local main_go="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local storage_disc="$REPO_DIR/scarlihq/internal/storage/discovery.go"
    local storage_bench="$REPO_DIR/scarlihq/internal/storage/benchmark.go"
    local inv_gpu="$REPO_DIR/scarlihq/internal/inventory/gpu.go"
    local inv_prov="$REPO_DIR/scarlihq/internal/inventory/provenance.go"
    local mm_manifest="$REPO_DIR/scarlihq/internal/modelmeta/manifest.go"
    local budget_go="$REPO_DIR/scarlihq/internal/profiles/budget.go"
    local budget_recovery_test="$REPO_DIR/scarlihq/internal/profiles/budget_recovery_test.go"
    local rel_notes="$REPO_DIR/docs/RELEASE_NOTES_v19.7.1.md"
    local readme="$REPO_DIR/README.md"

    for f in "$adm_vector" "$adm_manager" "$adm_ledger" "$adm_contract" "$adm_prod" "$adm_tests" \
        "$api_admission" "$api_admission_test" "$api_rest" "$main_go" "$storage_disc" "$storage_bench" \
        "$inv_gpu" "$inv_prov" "$mm_manifest" "$budget_go" "$budget_recovery_test" "$rel_notes"; do
        if [[ ! -f "$f" ]]; then
            fail "v19.7.1 source missing: $f (the Compute Fabric Phase 2 surface)"
            return
        fi
    done

    # --- The resource VECTOR is multi-dimensional (roadmap §6.1: "Do not
    # model the whole machine as one scalar score") — the three types.
    if ! grep -q 'type ResourceVector struct' "$adm_vector" || ! grep -q 'type GPURequest struct' "$adm_vector" || ! grep -q 'type StorageRequest struct' "$adm_vector"; then
        fail "the resource vector types must exist (ResourceVector + GPURequest + StorageRequest — roadmap §6.2's shape)"
        return
    fi
    # GPU VRAM is per-UUID — no aggregate VRAM arithmetic.
    if ! grep -q 'GPUUUID' "$adm_vector"; then
        fail "GPU requests must be per-UUID (roadmap §6.3: compute capacity per physical GPU UUID, never aggregate)"
        return
    fi

    # --- The admission charter markers (comment-stripped — prose cannot
    # mask code).
    local stripped_mgr
    stripped_mgr=$(_strip_go_comments "$adm_manager")
    if ! printf '%s\n' "$stripped_mgr" | grep -q 'EXCLUSIVE GPU assignment only'; then
        fail "admission must admit EXCLUSIVE GPU assignment only in v19.7.1 (roadmap §6: exclusive mode remains the safe default — sharing without compute-share enforcement is a phantom capability)"
        return
    fi
    if ! printf '%s\n' "$stripped_mgr" | grep -q 'ReasonSnapshotStale'; then
        fail "a STALE snapshot must be HARD-REFUSED with the named reason (KPK-19.7.0-P2-01: admission odmietne freshness.stale=true — never reuse an old free-VRAM value as current capacity)"
        return
    fi
    if ! printf '%s\n' "$stripped_mgr" | grep -q 'ReasonModelUnverified'; then
        fail "an UNVERIFIED artifact must be refused with the named reason (KPK-19.7.0-P2-04: the gate consumes VERIFICATION records, not declared digests)"
        return
    fi
    if ! printf '%s\n' "$stripped_mgr" | grep -q 'stale verification is absent verification'; then
        fail "a STALE verification must be refused as absent verification (KPK-19.7.0-P2-04: the age policy is enforced, not documented)"
        return
    fi
    if ! printf '%s\n' "$stripped_mgr" | grep -q 'ReasonStorageNoReadProfile'; then
        fail "a measured-performance floor without a measurement must be refused (the release's must-not-do: never schedule from unverified estimates — ENFORCED at the gate)"
        return
    fi
    if ! printf '%s\n' "$stripped_mgr" | grep -q 'ReasonLedgerUnavailable'; then
        fail "a ledger persistence failure must reserve NOTHING (fail closed: an unpersisted reservation is not a reservation)"
        return
    fi
    # The deterministic-order marker (roadmap §6.4: deterministic for
    # identical inputs — candidate GPUs iterate in UUID order).
    if ! printf '%s\n' "$stripped_mgr" | grep -q 'sort\.'; then
        fail "candidate iteration must be SORTED (deterministic admission — never map-ordered)"
        return
    fi

    # --- The durable ledger: fail-closed construction + boot
    # reconciliation + lease expiry (roadmap §6.6).
    local stripped_ledger
    stripped_ledger=$(_strip_go_comments "$adm_ledger")
    if ! printf '%s\n' "$stripped_ledger" | grep -q 'group/world-accessible'; then
        fail "the ledger must refuse group/world-accessible files (capacity accounting is not ambient — 0600 enforced at construction)"
        return
    fi
    if ! printf '%s\n' "$stripped_ledger" | grep -q 'SchemaVersion != LedgerSchemaVersion'; then
        fail "the ledger must refuse a foreign schema_version BY NAME (a future migration, never a silent reinterpretation)"
        return
    fi
    if ! printf '%s\n' "$stripped_ledger" | grep -q 'sweepExpired'; then
        fail "the ledger must sweep EXPIRED leases (roadmap §6.6: expiry triggers reconciliation, not blind release)"
        return
    fi

    # --- The regression proofs (roadmap §6.7's admission test list).
    for t in TestAdmitStaleSnapshotHardRefused TestAdmitConcurrentCannotOverspend TestLedgerCrashRecovery TestAdmissionDeterministic TestAdmitFailureReservesNothing TestAdmitIdempotentReplay TestLeaseExpiryReconciliation; do
        if ! grep -q "func $t" "$adm_tests"; then
            fail "the admission regression test $t must exist (roadmap §6.7's checklist)"
            return
        fi
    done
    for t in TestAdmissionRoutesDisabledByDefault TestAdmissionRefusalIsData TestAdmissionIdempotencyReplay TestAdmissionReleaseFlow; do
        if ! grep -q "func $t" "$api_admission_test"; then
            fail "the admission HTTP regression test $t must exist (the surface's own proofs)"
            return
        fi
    done

    # --- The API surface: ADMIN-gated (negative control (a): reserving
    # capacity is strictly stronger than reading the inventory — the
    # routes must sit behind authAdmin, never the read gate).
    if ! grep -q 'h.authAdmin(h.admissionRoute)' "$api_rest" || ! grep -q 'h.authAdmin(h.admissionRelease)' "$api_rest"; then
        fail "the admission routes must be ADMIN-gated (authAdmin — a reservation is a mutating act, not an observation)"
        return
    fi
    # Negative control (b): the principal is stamped SERVER-SIDE from the
    # resolved token identity — a caller cannot attribute its hold to
    # someone else (comment-stripped: the stamp call, not the comment).
    local stripped_api_adm
    stripped_api_adm=$(_strip_go_comments "$api_admission")
    if ! printf '%s\n' "$stripped_api_adm" | grep -q 'PrincipalFromContext'; then
        fail "the admission principal must be stamped server-side (PrincipalFromContext — never caller-supplied)"
        return
    fi
    if printf '%s\n' "$stripped_api_adm" | grep -q 'DisallowUnknownFields'; then
        :
    else
        fail "the admission request decode must DisallowUnknownFields (a typo'd dimension is a refusal, not a silent default)"
        return
    fi

    # --- The main.go opt-in wiring (negative control (c): the surface
    # stays OFF until the operator turns it on — fail-closed env contract).
    if ! grep -q 'SCARLIHQ_ADMISSION_ENABLED' "$main_go" || ! grep -q 'SCARLIHQ_ADMISSION_LEDGER' "$main_go"; then
        fail "main.go must carry the admission opt-in env contract (SCARLIHQ_ADMISSION_ENABLED + SCARLIHQ_ADMISSION_LEDGER)"
        return
    fi
    if ! grep -q 'NewProductionManager' "$main_go"; then
        fail "main.go must construct the production admission manager (the durable-ledger wiring)"
        return
    fi

    # --- The KPK-19.7.0 remediations stand:
    # P2-01 — per-device filesystem collection (the first mount no longer
    # stands in for the whole disk).
    if ! grep -q 'func collectDeviceFilesystems' "$storage_disc"; then
        fail "KPK-19.7.0-P2-01: filesystems must be collected PER DEVICE (a multi-partition disk's mounts never collapse into the first)"
        return
    fi
    # P3-05 — remount-ro is an exact-token comparison, never a substring.
    if ! grep -q 'mountIsReadOnly' "$storage_disc"; then
        fail "KPK-19.7.0-P3-05: read-only detection must be the exact-token comparator (errors=remount-ro is not 'ro')"
        return
    fi
    # P2-03 — EXCLUSIVE temp-file creation + the cleanup error reported.
    local stripped_bench
    stripped_bench=$(_strip_go_comments "$storage_bench")
    if ! printf '%s\n' "$stripped_bench" | grep -q 'os.O_CREATE|os.O_EXCL|os.O_WRONLY'; then
        fail "KPK-19.7.0-P2-03: the benchmark temp file must be created EXCLUSIVELY (O_CREATE|O_EXCL — a same-name stale file is an error)"
        return
    fi
    if ! printf '%s\n' "$stripped_bench" | grep -q 'cleanup failed'; then
        fail "KPK-19.7.0-P2-03: a failed cleanup must be reported to the caller (never swallowed)"
        return
    fi
    # P2-05 — the well-known model-store roots are refused.
    if ! printf '%s\n' "$stripped_bench" | grep -q 'ErrBenchmarkTargetDenied'; then
        fail "KPK-19.7.0-P2-05: benchmarking a well-known production model store must be DENIED by default (the named error + the explicit override)"
        return
    fi
    # P2-02 — empty ≠ unavailable: the collectors report SourceStatus.
    if ! grep -q 'func CollectGPUsWithStatus' "$inv_gpu" && ! grep -q 'func CollectGPUsWithStatus' "$inv_prov"; then
        fail "KPK-19.7.0-P2-02: the GPU collector must carry the SourceStatus seam (empty and unavailable are different facts)"
        return
    fi
    if ! grep -q 'type BackendPresence struct' "$inv_prov"; then
        fail "KPK-19.7.0-P2-02: the declared-vs-running backend presence join must exist (the placement × runtime view)"
        return
    fi
    # P2-04 — duplicate model IDs are rejected as a GROUP + the declared
    # digest is renamed in every EMITTED provenance.
    if ! grep -q 'ALL conflicting manifests are invalid' "$mm_manifest"; then
        fail "KPK-19.7.0-P2-04: duplicate model IDs must be rejected as a GROUP (no deterministic file-order winner)"
        return
    fi
    if ! grep -q 'json:"declared_sha256"' "$mm_manifest"; then
        fail "KPK-19.7.0-P2-04: the emitted digest must be declared_sha256 (declared ≠ verified — every consumer sees the distinction)"
        return
    fi
    # The crash-safe budget reservations (KPK-19.6.7-carry-forward-P2-06).
    if ! grep -q 'budgetLedgerSchemaV2' "$budget_go" || ! grep -q 'BootID' "$budget_go"; then
        fail "KPK-19.6.7-carry-forward-P2-06: budget reservations must be durable RECORDS with boot identity (crash-safe, never anonymous aggregates)"
        return
    fi
    if ! grep -q 'func TestBudgetCrashRecoveryReleasesOrphanedHold' "$budget_recovery_test" || ! grep -q 'func TestBudgetV1LedgerMigration' "$budget_recovery_test"; then
        fail "the budget crash-recovery regression tests must exist (orphaned hold release + the v1 migration)"
        return
    fi

    # --- The release documentation genre + the README record.
    for section in '## Scope' '## Changes' '## Verification' '## Known limitations'; do
        if ! grep -q "$section" "$rel_notes"; then
            fail "the v19.7.1 release notes must carry section '$section' (the release genre)"
            return
        fi
    done
    if ! grep -q "What's New in v19.7.1" "$readme" || ! grep -q '| \*\*v19.7.1\*\*' "$readme"; then
        fail "the README must carry the v19.7.1 What's New section + changelog row"
        return
    fi

    pass "v19.7.1 (Compute Fabric Phase 2 — the roadmap §6 aggregate admission): the resource VECTOR is multi-dimensional (GPU VRAM per UUID + host RAM + CPU threads + KV-cache + storage free bytes + measured read profile + ports — never one scalar score), admission is DETERMINISTIC (sorted candidate iteration, identical inputs → identical decisions) and ATOMIC (all dimensions checked + held under one ledger transaction; any failure reserves NOTHING — the unpersisted reservation is not a reservation), the gate is fail-closed at every join: a STALE snapshot is HARD-REFUSED (KPK-19.7.0-P2-01), an UNVERIFIED or STALE artifact verification is refused (KPK-19.7.0-P2-04 — declared digests are declarations, the gate consumes verification records), a declared-but-not-running backend is refused (the placement × runtime join), a measured-performance floor without a measurement is refused (never schedule from unverified estimates — ENFORCED), pinned RAM is refused by default (the honest no-ledger-yet answer), and GPU assignment is EXCLUSIVE per UUID (the safe v19.7.1 default — sharing without compute-share enforcement is a phantom capability); the LEDGER is durable + fail-closed (0600 enforced, foreign schema refused BY NAME, boot-mismatched holds crash-recovered on the record, expired leases swept — expiry triggers reconciliation, never blind release), leases carry TTL + idempotency keys REPLAY the original decision; the API surface is two ADMIN-gated routes (decide+reserve GET/POST + release POST) with the principal stamped SERVER-SIDE from the resolved token identity and DisallowUnknownFields strict decoding — a refusal is 200 + allowed=false + the machine-readable reason_code (the gate working, never an HTTP error), and the surface is OPT-IN (SCARLIHQ_ADMISSION_ENABLED + SCARLIHQ_ADMISSION_LEDGER, fail-closed construction); the KPK-19.7.0 remediations stand in code: P2-01 per-device filesystem collection (a multi-partition disk never collapses into its first mount), P2-02 empty-vs-unavailable made honest (CollectGPUsWithStatus + BackendPresence — the per-source provenance the v2 snapshot cites), P2-03 the benchmark temp file created EXCLUSIVELY with cleanup failures REPORTED (never swallowed), P2-05 well-known production model-store roots DENIED by default (ErrBenchmarkTargetDenied + the deliberate explicit override), P3-05 remount-ro as the exact-token comparator, P2-04 duplicate model IDs rejected as a GROUP (no file-order winner) + every emitted digest renamed declared_sha256; the budget reservations are CRASH-SAFE (durable records with boot identity, orphaned holds released on the record at startup, month-specific accounting, the v1 aggregate ledger migrated explicitly — KPK-19.6.7-carry-forward-P2-06); acceptance: the admission suite (the §6.7 checklist — concurrent no-overspend, crash recovery, determinism, failure-reserves-nothing, idempotent replay, lease expiry, verified release) + the HTTP suite (503-before-config, 401, 400 strict decode, 405, refusal-is-data, replay, release 409s) + the budget recovery suite (orphaned hold, same-boot survival, month boundary, idempotent recovery, v1 migration); residual honesty: hardware acceptance stays OPEN (KPK-06), no verified manifests ship yet (every admission for a real model refuses model-unverified until an operator records verifications), pinned RAM is refused until a pinned-RAM ledger exists, GPU sharing arrives only with a measured per-backend profile, the v19.7.2 planner is the next milestone; 'LLM navrhuje, politika rozhoduje' — the vector is honest, the reserve is atomic, the ledger survives the crash, and the pen stays with the operator"
}

# ===========================================================================
# check_39 — v19.7.2 (Compute Fabric Phase 3, roadmap §7): the backend
# capability contract (the administrator-owned CLOSED registry in
# internal/airuntime, validated at construction) + the deterministic
# execution planner (internal/planner — pure, side-effect-free, the
# proposal engine behind the READ gate) + the §7.6 boundary (LLM output
# is never selection input — the request structurally carries no
# steering field)
# ===========================================================================
check_39_backend_capability_planner_v19_7_2() {
    echo "--- [39] v19.7.2 (Compute Fabric Phase 3 — roadmap §7): the backend capability contract (closed administrator-owned registry, validated at startup) + the deterministic execution planner (pure, side-effect-free, hard constraints BEFORE optimization, content-hash PlanID determinism) + the §7.6 boundary (LLM output never becomes directly executable) ---"
    local backends_go="$REPO_DIR/scarlihq/internal/airuntime/backends.go"
    local backends_test="$REPO_DIR/scarlihq/internal/airuntime/backends_test.go"
    local planner_go="$REPO_DIR/scarlihq/internal/planner/planner.go"
    local planner_prod="$REPO_DIR/scarlihq/internal/planner/production.go"
    local planner_test="$REPO_DIR/scarlihq/internal/planner/planner_test.go"
    local api_plan="$REPO_DIR/scarlihq/internal/api/plan.go"
    local api_plan_test="$REPO_DIR/scarlihq/internal/api/plan_test.go"
    local api_rest="$REPO_DIR/scarlihq/internal/api/rest.go"
    local main_go="$REPO_DIR/scarlihq/cmd/scarlihq/main.go"
    local rel_notes="$REPO_DIR/docs/RELEASE_NOTES_v19.7.2.md"
    local readme="$REPO_DIR/README.md"
    local roadmap="$REPO_DIR/docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md"

    for f in "$backends_go" "$backends_test" "$planner_go" "$planner_prod" "$planner_test" \
        "$api_plan" "$api_plan_test" "$api_rest" "$main_go" "$rel_notes" "$roadmap"; do
        if [[ ! -f "$f" ]]; then
            fail "v19.7.2 source missing: $f (the Compute Fabric Phase 3 surface)"
            return
        fi
    done

    # --- §7.2: the backend capability contract — BackendID +
    # BackendCapabilities + the CLOSED registry (the roadmap's own const
    # block: sglang/vllm/beellama/ollama/infer_ssd).
    if ! grep -q 'type BackendID string' "$backends_go" || ! grep -q 'type BackendCapabilities struct' "$backends_go"; then
        fail "the §7.2 contract types must exist (BackendID + BackendCapabilities — the field set is the contract)"
        return
    fi
    for b in BackendSGLang BackendVLLM BackendBeeLlama BackendOllama BackendInferSSD; do
        if ! grep -q "$b BackendID" "$backends_go"; then
            fail "the closed capability enumeration must carry $b (roadmap §7.2's own const block)"
            return
        fi
    done
    # The registry is ADMINISTRATOR-OWNED: no registration surface (no
    # HTTP route, no env var, no file can add a row) and VALIDATED at
    # construction (roadmap §7.2's "validated at startup").
    if ! grep -q 'func ValidateBackendRegistry' "$backends_go"; then
        fail "the closed registry must be validated at construction (ValidateBackendRegistry — roadmap §7.2's validated-at-startup rule)"
        return
    fi
    local stripped_backends
    stripped_backends=$(_strip_go_comments "$backends_go")
    # §7.2's registry rule is the package CHARTER (documentation layer —
    # the code enforcement is structural: no registration function
    # exists anywhere in the package; the marker greps the RAW source).
    if ! grep -q 'an HTTP caller or LLM to register' "$backends_go"; then
        fail "the §7.2 registry rule must be encoded in the registry source (administrator-owned — no HTTP caller or LLM registers backends)"
        return
    fi
    # The §7.3 discipline: extending is the ELEVEN-STEP act (the
    # documented procedure; infer_ssd's row is the declared-future
    # exception its v19.8.0 marker names — that one IS in code, the
    # AdapterNote + the alignment error).
    if ! grep -q 'eleven steps' "$backends_go"; then
        fail "§7.3's extension procedure must be encoded at the registry (eleven steps — never merely a string constant)"
        return
    fi
    if ! printf '%s\n' "$stripped_backends" | grep -q 'v19.8.0'; then
        fail "the declared-future infer_ssd row must carry the v19.8.0 marker IN CODE (the AdapterNote + the alignment error — its adapter arrives with roadmap §8)"
        return
    fi
    # The Provider↔BackendID alignment: infer_ssd is the honest named
    # error (no v19.7.2 adapter).
    if ! grep -q 'func ProviderForBackend' "$backends_go" || ! grep -q 'func BackendForProvider' "$backends_go"; then
        fail "the adapter-tier ↔ capability-tier alignment must exist (ProviderForBackend + BackendForProvider)"
        return
    fi
    if ! grep -q 'no runtime adapter in v19.7.2' "$backends_go"; then
        fail "ProviderForBackend(infer_ssd) must be the honest named error (no v19.7.2 adapter — v19.8.0 owns it)"
        return
    fi

    # --- §7.1: the planner is PURE — no network, no exec, no file I/O
    # in the planning path (comment-stripped: prose cannot mask code;
    # production.go is the ONLY seam allowed to touch the environment).
    local stripped_planner
    stripped_planner=$(_strip_go_comments "$planner_go")
    if printf '%s\n' "$stripped_planner" | grep -nE '\bnet/http\b|\bos\.[A-Z]|\bexec\.|\bsyscall\b' >/dev/null; then
        fail "internal/planner/planner.go must stay network/IO-free by construction (the production seams live in production.go — roadmap §7.1: the planner must not launch containers or touch the environment)"
        return
    fi
    # §7.1's charter marker is the package documentation (the code
    # enforcement is the purity scan above; the marker greps the RAW
    # source — the charter IS the marker).
    if ! grep -q 'launch containers' "$planner_go"; then
        fail "the §7.1 charter marker must live in the planner source (the planner consumes capabilities and metadata; it must not launch containers)"
        return
    fi
    # production.go is the documented seam: the env surface exists there
    # and ONLY there.
    if ! grep -q 'SCARLIHQ_PLANNER_IMAGES' "$planner_prod"; then
        fail "the production seam must carry the operator image-registry env surface (SCARLIHQ_PLANNER_IMAGES)"
        return
    fi
    if grep -q 'os\.Getenv\|net/http' "$planner_go"; then
        fail "planner.go must not read the environment or dial the network (production.go is the only seam)"
        return
    fi

    # --- §7.4: the planner contract — the request/plan shapes + the
    # invariants (determinism via the content-hash PlanID, hard
    # constraints, the proposal invariant, image pinning).
    if ! grep -q 'type ExecutionRequest struct' "$planner_go" || ! grep -q 'type ExecutionPlan struct' "$planner_go"; then
        fail "the §7.4 contract types must exist (ExecutionRequest + ExecutionPlan — the field set is the contract)"
        return
    fi
    if ! grep -q 'func (p \*Planner) planContentHash' "$planner_go"; then
        fail "the PlanID must be the CONTENT hash of the decision tuple (identical inputs → identical ID — the determinism proof, not a random identity)"
        return
    fi
    if ! grep -q 'sha256.Sum256' "$planner_go"; then
        fail "the PlanID must be a SHA-256 content hash (never a random UUID — determinism is the contract)"
        return
    fi
    if ! grep -q 'func (p \*Planner) Plan(req ExecutionRequest) PlanResult' "$planner_go"; then
        fail "the Plan entrypoint must exist (the deterministic pipeline)"
        return
    fi
    # The plan EXPIRES and is revalidated at admission (the proposal
    # invariant — the closing §7.4 rule).
    if ! printf '%s\n' "$stripped_planner" | grep -q 'ExpiresAt'; then
        fail "every plan must EXPIRE (ExpiresAt — revalidated at admission; planner output is a proposal, admission is still required)"
        return
    fi
    if ! grep -q 'plan-is-proposal' "$planner_go"; then
        fail "the generated explanation must carry the plan-is-proposal reason (nothing is reserved, bound or launched)"
        return
    fi

    # --- The hard-constraint vocabulary (§7.5: constraints BEFORE
    # optimization; every refusal BY NAME — the closed stable set).
    for code in ReasonPlanSnapshotStale ReasonPlanModelUnverified ReasonPlanBackendNoCandidates ReasonPlanGPUInsufficient ReasonPlanRAMInsufficient ReasonPlanImageUnconfigured ReasonPlanImageNotDigestPinned ReasonPlanContextExceedsLimit; do
        if ! grep -q "$code" "$planner_go"; then
            fail "the closed refusal vocabulary must carry $code (a hard constraint without its named refusal is a silent constraint)"
            return
        fi
    done
    # The image invariant (§7.4): digest-pinned AND operator-declared.
    # The marker is the EXACT enforcement call (a bare '@sha256:' string
    # would also match refusal-message prose — prose cannot prove the
    # gate; the code marker can).
    if ! printf '%s\n' "$stripped_planner" | grep -q 'strings.Contains(ref, "@sha256:")'; then
        fail "the planner must enforce the digest pin BY CODE (strings.Contains(ref, \"@sha256:\") — a reference without a digest is refused BY NAME, roadmap §7.4's invariant)"
        return
    fi

    # --- §7.5: the tie-breaking sequence (policy priority → observed
    # utilization → tightest fit → stable IDs) — hard constraints first.
    if ! grep -q 'func (p \*Planner) less' "$planner_go"; then
        fail "the deterministic candidate comparator must exist (§7.5's tie-break sequence)"
        return
    fi
    if ! grep -q 'func (p \*Planner) priorityOf' "$planner_go"; then
        fail "the policy-priority lookup must exist (tie-break 1: highest policy priority — the operator-owned order, never request-supplied)"
        return
    fi
    if ! printf '%s\n' "$stripped_planner" | grep -q 'sort\.'; then
        fail "candidate iteration must be SORTED (deterministic evaluation — never map-ordered)"
        return
    fi

    # --- §7.6: the boundary — the request structurally carries NO
    # steering field (LLM output never becomes directly executable —
    # this release's must-not-do, enforced by the request's shape). The
    # assertion reads the ExecutionRequest STRUCT BLOCK itself: no
    # Preferred/Explanation/Policy field may appear in it (OUTPUT
    # explanation fields on the plan/result are legitimate — the
    # boundary is the REQUEST's shape).
    local req_block
    req_block=$(sed -n '/^type ExecutionRequest struct {/,/^}/p' "$planner_go")
    if printf '%s\n' "$req_block" | grep -qiE 'Preferred|Explanation|Policy'; then
        fail "ExecutionRequest must NOT carry a selection-steering field (no preferred_backend, no explanation echo — §7.6's boundary is the request's shape)"
        return
    fi
    if ! grep -qE 'PreferredBackend|preferred_backend' "$api_plan_test"; then
        fail "the regression suite must prove the steering-field boundary (the strict-decode steering-payload refusal)"
        return
    fi
    if ! grep -q 'LLM never authors one' "$planner_go"; then
        fail "the §7.5 closing rule must be encoded in the planner source (never let a model-generated explanation alter the deterministic selection — the explanation entries are GENERATED, an LLM never authors one)"
        return
    fi
    if ! grep -q 'LLM OUTPUT IS NEVER SELECTION INPUT' "$planner_go"; then
        fail "the §7.6 charter marker must live in the planner source (the release's must-not-do)"
        return
    fi
    # The principal is stamped SERVER-SIDE (the api layer).
    local stripped_api_plan
    stripped_api_plan=$(_strip_go_comments "$api_plan")
    if ! printf '%s\n' "$stripped_api_plan" | grep -q 'PrincipalFromContext'; then
        fail "the plan principal must be stamped server-side (PrincipalFromContext — never caller-supplied)"
        return
    fi
    if ! printf '%s\n' "$stripped_api_plan" | grep -q 'DisallowUnknownFields'; then
        fail "the plan request decode must DisallowUnknownFields (a steering field in the payload is a 400 refusal — the §7.6 boundary at the decode)"
        return
    fi

    # --- The API surface: the READ gate (negative control (a): the
    # planner reserves nothing — POST /api/compute/plan sits behind
    # h.auth, the same observation-grade gate as the inventory, NOT the
    # admin gate and never unauthenticated).
    if ! grep -q 'h.auth(h.planRoute)' "$api_rest"; then
        fail "the plan route must sit behind the READ gate (h.auth — the planner reserves nothing; admission stays the separately-opt-in mutating act)"
        return
    fi
    if grep -q 'h.authAdmin(h.planRoute)' "$api_rest"; then
        fail "the plan route must NOT take the admin gate (planning is observation-grade — reserving is admission's act)"
        return
    fi
    # The main.go wiring: the registry validated at construction +
    # fail-closed on a malformed image declaration.
    if ! grep -q 'planner.NewProductionPlanner' "$main_go"; then
        fail "main.go must construct the production planner (the §7.2 validated-at-startup wiring)"
        return
    fi
    if ! grep -q 'ValidateBackendRegistry' "$planner_go"; then
        fail "the planner construction must validate the closed capability registry (roadmap §7.2)"
        return
    fi

    # --- The regression proofs (the §7.4/§7.5/§7.6 suites).
    for t in TestPlanDeterministic TestPlanGoldenPathDenseGPU TestPlanCPUExecutionClass TestPlanMoEGPUResident TestPlanRefusesStaleSnapshot TestPlanRefusesUnverifiedModel TestPlanBackendPresenceJoin TestPlanGPUInsufficient TestPlanImageUnconfigured TestPlanImageNotDigestPinned TestPlanPriorityTieBreak TestPlanGPUTieBreakUtilization TestPlanRequestCarriesNoSteeringField TestPlanExplanationIsGenerated; do
        if ! grep -q "func $t" "$planner_test"; then
            fail "the planner regression test $t must exist (the §7.4/§7.5/§7.6 proofs)"
            return
        fi
    done
    for t in TestPlanRouteDisabledWithoutPlanner TestPlanRequiresAuth TestPlanStrictDecodeRefusesSteeringFields TestPlanMethodGuard TestPlanGetRegistryView TestPlanGoldenPath TestPlanRefusalIsData TestPlanDeterministicOverHTTP; do
        if ! grep -q "func $t" "$api_plan_test"; then
            fail "the plan HTTP regression test $t must exist (the surface's own proofs)"
            return
        fi
    done
    for t in TestBackendRegistryClosedAndValidated TestBackendCapabilitiesForEveryMember TestProviderBackendAlignment TestBackendTiers TestBackendRowsAreCopies; do
        if ! grep -q "func $t" "$backends_test"; then
            fail "the backend registry regression test $t must exist (the §7.2/§7.3 proofs)"
            return
        fi
    done

    # --- The release documentation genre + the README record + the
    # roadmap pointer.
    for section in '## Scope' '## Changes' '## Verification' '## Known limitations'; do
        if ! grep -q "$section" "$rel_notes"; then
            fail "the v19.7.2 release notes must carry section '$section' (the release genre)"
            return
        fi
    done
    if ! grep -q "What's New in v19.7.2" "$readme" || ! grep -q '| \*\*v19.7.2\*\*' "$readme"; then
        fail "the README must carry the v19.7.2 What's New section + changelog row"
        return
    fi
    if ! grep -q 'Phase 3 --- v19.7.2: Backend capability contract and deterministic execution planner' "$roadmap"; then
        fail "the roadmap must carry the §7 Phase 3 heading (the release's contract — the roadmap stays the planning document)"
        return
    fi

    pass "v19.7.2 (Compute Fabric Phase 3 — the roadmap §7 backend capability contract + deterministic execution planner): the CAPABILITY TIER is separated from the adapter tier (BackendID + BackendCapabilities in internal/airuntime — the closed administrator-owned registry carrying roadmap §7.2's own five backends incl. the declared-future infer_ssd row with its v19.8.0 marker; validated at construction, no registration surface exists — no HTTP route, no env var, no file can add a row; the Provider↔BackendID alignment makes infer_ssd the honest named error: no v19.7.2 adapter, v19.8.0 owns it; §7.3's eleven-step extension procedure encoded at the registry — never merely a string constant); the PLANNER (internal/planner — NEW) is pure + side-effect-free (comment-stripped: no net/http, no os, no exec, no syscall in planner.go — production.go is the only environment seam, roadmap §7.1's 'the planner must not launch containers'), DETERMINISTIC (the PlanID is the SHA-256 CONTENT hash of the decision tuple — identical inputs → identical ID, 20×; candidate evaluation sorted; the tie-break sequence is policy priority → observed utilization (the honest queue-delay proxy) → tightest fit → stable IDs), and fail-closed at every hard constraint (§7.5 constraints BEFORE optimization): a STALE snapshot is HARD-REFUSED (KPK-19.7.0-P2-01's rule), an UNVERIFIED or STALE artifact verification is refused (KPK-19.7.0-P2-04's rule — declared digests are declarations), a declared-but-not-running backend is excluded by the placement × runtime join (the same join refuses every v19.7.2 infer_ssd plan — the declared-future backend is honestly absent), format/architecture/feature compatibility is enforced against the CLOSED capability rows, an unservable context is a named refusal, the capacity check is admission-shaped (the same safety-headroom discipline), a multi-GPU-sized demand is a NAMED refusal (measured per-GPU overhead profiles are v19.8.0+, never a guessed split), and every image is digest-pinned AND operator-declared (the honest v19.7.2 default: the image registry ships EMPTY — every plan refuses plan-image-unconfigured until SCARLIHQ_PLANNER_IMAGES declares references, the same honesty as v19.7.1's no-verified-manifests default); the STRATEGY vocabulary is closed and resident-only (dense-resident / cpu-resident / moe-gpu-resident / moe-cpu-resident — the deployed backends' actual behavior; expert paging + SSD-backed experts are v19.8.0 territory, no v19.7.2 plan carries a storage demand); §7.6's boundary is ENFORCED BY SHAPE (this release's must-not-do 'do not make LLM output directly executable'): ExecutionRequest carries NO steering field (no preferred_backend, no explanation echo — asserted at the source level), the strict decode refuses any injected field as 400, the principal is stamped SERVER-SIDE, the selection order belongs ONLY to the operator-owned PlannerPolicy, and the plan's Explanation is GENERATED by the planner (machine-readable DecisionReason codes + the numbers), never consumed from model output; the API surface is POST/GET /api/compute/plan behind the READ gate (the planner RESERVES NOTHING — observation-grade, like the inventory; admission stays the separately-opt-in ADMIN-gated mutating act) with a refusal as 200 + selected=false + the machine-readable reason_code (the planner working, never an HTTP error) and GET serving the closed registry view; acceptance: the planner suite (determinism 20×, the golden path, the CPU class, the MoE-resident rule, the full hard-constraint matrix, the tie-break sequence incl. the reversed policy priority, the steering-field absence proof, the generated-explanation proof) + the HTTP suite (503-before-config, 401, 400 strict decode incl. LLM-shaped payloads, 405, the registry view, refusal-is-data, HTTP determinism) + the registry suite (closed+validated, per-row honesty, the alignment errors, the tiers, the copy discipline); residual honesty: hardware acceptance stays OPEN (KPK-06), no verified manifests ship yet (every plan for a real model refuses plan-model-unverified until an operator records verifications), the image registry ships empty (plans refuse image-unconfigured until the operator declares digest-pinned references), the runtime-RAM overhead is the honest 0 (no measured profile exists), the utilization proxy for queue delay is OBSERVED data (a real queue model arrives with workload metrics, v19.8.0+), and the admission→planner revalidation join (a live GPU-health × reservation reconciler) stays named future work; 'LLM navrhuje, politika rozhoduje' — the capability rows are honest, the plan is deterministic, the proposal is expiry-bound, and the pen stays with the operator"
}

# ===========================================================================
# Explicit schedule, verified against the defined check_* functions before
# execution — the summary reports the number of checks actually executed.
# Do NOT hardcode the check count in documentation; read it from the output.
SMOKE_CHECKS=(
    check_01_bash_syntax
    check_02_shellcheck
    check_03_yaml
    check_04_systemd
    check_05_version
    check_06_go_module
    check_07_registry
    check_08_ollama_main
    check_09_docs_v12
    check_10_vllm_trust_p0
    check_11_single_gpu_adaptation
    check_12_pibolt_sha256_pin
    check_13_pibolt_user_owned_execution
    check_14_strict_gpu_optin_parsing
    check_15_gaming_manifest_and_installer
    check_16_gaming_readonly_api
    check_17_reservation_governor
    check_18_agent_kernel
    check_19_buzz_profile_disabled
    check_20_agent_kernel_grants
    check_21_agent_kernel_audit
    check_22_agent_kernel_compute
    check_23_agent_fabric_commands
    check_24_agent_fabric_workspaces
    check_25_agent_fabric_runtimes
    check_26_agent_fabric_approvals
    check_27_agent_fabric_closure
    check_28_ai_runtime_foundation
    check_29_ai_runtime_adapters
    check_30_ai_propose_surface
    check_31_ai_transport_boundary
    check_32_ai_dial_pin_probe_cancel
    check_33_ai_trust_scope_commit_atomicity
    check_34_trust_enumeration_profiles_enforcement_stale_automation
    check_35_identity_budgets_media_tools_stale_recursion
    check_36_compute_fabric_phase0_lifecycle
    check_37_unified_inventory_v2
    check_38_aggregate_admission_v19_7_1
    check_39_backend_capability_planner_v19_7_2
)

run_check() {
    CHECKS_EXECUTED=$((CHECKS_EXECUTED + 1))
    "$@"
}

verify_check_inventory() {
    # KPK-04 acceptance: scheduled inventory == defined functions == what
    # the summary will report. Any drift emits a FAIL event and returns 1.
    local -A defined=() scheduled=()
    local fn c drift=0
    while IFS= read -r fn; do
        defined["$fn"]=1
    done < <(compgen -A function | grep -E '^check_[0-9]+_' || true)
    for c in "${SMOKE_CHECKS[@]}"; do
        if [[ -z "${defined[$c]:-}" ]]; then
            fail "check inventory drift: scheduled $c is not defined"
            drift=1
            continue
        fi
        scheduled["$c"]=1
    done
    for fn in "${!defined[@]}"; do
        if [[ -z "${scheduled[$fn]:-}" ]]; then
            fail "check inventory drift: defined $fn is not scheduled in SMOKE_CHECKS"
            drift=1
        fi
    done
    if [[ $drift -eq 0 ]]; then
        echo "Check inventory: ${#SMOKE_CHECKS[@]} checks scheduled and defined (consistent)"
        return 0
    fi
    return 1
}

# ===========================================================================
# Run all checks (guarded — the test suite sources this file without executing)
# ===========================================================================
if [[ $SCARLIX_SMOKE_SOURCED -eq 0 ]]; then
    verify_check_inventory
    for check_fn in "${SMOKE_CHECKS[@]}"; do
        run_check "$check_fn"
    done

    # =======================================================================
    # Summary
    # =======================================================================
    echo
    # shellcheck disable=SC2059
    printf "${BOLD}=== Smoke test: %d checks executed — %d passed, %d failed, %d warned (events) ===${NC}\n" \
        "$CHECKS_EXECUTED" "$PASS_COUNT" "$FAIL_COUNT" "$WARN_COUNT"
    printf "Checks are inventory functions; events are individual PASS/FAIL/WARN results (one check may emit several).\n"

    if [[ $FAIL_COUNT -gt 0 ]]; then
        exit 1
    fi
    exit 0
fi
