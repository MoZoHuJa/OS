#!/usr/bin/env bash
# SCARLIX OS v19.1.19 — Regression Smoke Test (baseline freeze + P0 checks)
#
# Runs 10 integrity checks against the SCARLIX OS repo and prints a
# PASS/FAIL/WARN line for each. Exits 0 if no check FAILed, 1 otherwise.
# WARNs (e.g. missing tool, network error) do NOT fail the run.
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

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
usage() {
    cat <<'EOF'
scarlix-smoke-test.sh — SCARLIX OS v19.1.19 regression smoke test

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
  1   At least one check FAILed.
  2   Invalid CLI usage.
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
# Argument parsing
# ---------------------------------------------------------------------------
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

# ---------------------------------------------------------------------------
# Preamble
# ---------------------------------------------------------------------------
printf "${BOLD}=== SCARLIX OS v19.1.19 regression smoke test ===${NC}\n"
printf "Repo:   %s\n" "$REPO_DIR"
printf "Offline: %s\n" "$([[ $OFFLINE -eq 1 ]] && echo yes || echo no)"
echo

if [[ ! -d "$REPO_DIR" ]]; then
    echo "scarlix-smoke-test: REPO_DIR does not exist: $REPO_DIR" >&2
    exit 2
fi
if [[ ! -f "$REPO_DIR/VERSION" ]] || [[ ! -f "$REPO_DIR/install.sh" ]]; then
    echo "scarlix-smoke-test: $REPO_DIR does not look like the SCARLIX OS repo" >&2
    echo "  (expected: VERSION + install.sh at repo root)" >&2
    exit 2
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
    # Filter dev-box noise: "marked executable" / "not marked executable" /
    # "chmod" hints. Remaining output indicates real unit errors.
    local raw filtered
    raw=$(systemd-analyze verify "${units[@]}" 2>&1 || true)
    filtered=$(printf '%s\n' "$raw" \
        | grep -vE 'executable|chmod|Empty|cached' || true)
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
check_image_dockerhub() {
    local repo="$1" tag="$2"
    local token_json token status
    # 1. Get auth token from Docker Hub
    if ! token_json=$(curl -sSL --max-time 15 \
        "https://auth.docker.io/token?service=registry.docker.io&scope=repository:${repo}:pull" 2>/dev/null); then
        warn "Docker Hub $repo:$tag — network error fetching auth token"
        return
    fi
    if ! token=$(printf '%s' "$token_json" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))' 2>/dev/null); then
        warn "Docker Hub $repo:$tag — could not parse auth token JSON"
        return
    fi
    if [[ -z "$token" ]]; then
        warn "Docker Hub $repo:$tag — empty auth token"
        return
    fi
    # 2. HEAD the manifest endpoint (use GET with -I for portability)
    if ! status=$(curl -sSL -o /dev/null -w "%{http_code}" --max-time 20 \
        -H "Authorization: Bearer $token" \
        -H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json" \
        "https://registry-1.docker.io/v2/${repo}/manifests/${tag}" 2>/dev/null); then
        warn "Docker Hub $repo:$tag — network error fetching manifest"
        return
    fi
    case "$status" in
        200) pass "Docker Hub $repo:$tag exists (HTTP 200)" ;;
        404) fail "Docker Hub $repo:$tag NOT FOUND (HTTP 404)" ;;
        *)   warn "Docker Hub $repo:$tag — unexpected HTTP $status" ;;
    esac
}

check_image_ghcr() {
    local repo="$1" tag="$2"
    local token_json token status
    if ! token_json=$(curl -sSL --max-time 15 \
        "https://ghcr.io/token?service=ghcr.io&scope=repository:${repo}:pull" 2>/dev/null); then
        warn "ghcr.io $repo:$tag — network error fetching auth token"
        return
    fi
    if ! token=$(printf '%s' "$token_json" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))' 2>/dev/null); then
        warn "ghcr.io $repo:$tag — could not parse auth token JSON"
        return
    fi
    if [[ -z "$token" ]]; then
        warn "ghcr.io $repo:$tag — empty auth token"
        return
    fi
    if ! status=$(curl -sSL -o /dev/null -w "%{http_code}" --max-time 20 \
        -H "Authorization: Bearer $token" \
        -H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json" \
        "https://ghcr.io/v2/${repo}/manifests/${tag}" 2>/dev/null); then
        warn "ghcr.io $repo:$tag — network error fetching manifest"
        return
    fi
    case "$status" in
        200) pass "ghcr.io $repo:$tag exists (HTTP 200)" ;;
        404) fail "ghcr.io $repo:$tag NOT FOUND (HTTP 404)" ;;
        *)   warn "ghcr.io $repo:$tag — unexpected HTTP $status" ;;
    esac
}

check_07_registry() {
    echo "--- [07] Image tag existence via registry API ---"
    if [[ $OFFLINE -eq 1 ]]; then
        warn "--offline set — skipping 5 registry image-tag checks"
        return
    fi
    if ! has_tool curl || ! has_tool python3; then
        warn "curl and/or python3 unavailable — skipping registry image-tag checks"
        return
    fi
    # Docker Hub images
    check_image_dockerhub "lmsysorg/sglang" "v0.4.9.post6-cu128-b200"
    check_image_dockerhub "fedirz/faster-whisper-server" "sha-307e23f-cuda"
    # ghcr.io images
    check_image_ghcr "block/buzz" "latest"
    check_image_ghcr "berriai/litellm" "main-v1.23.9"
    check_image_ghcr "openlit/openlit" "1.5.0"
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
    # which emitted invalid non KEY=VALUE lines into .env. Reject that pattern.
    control_line=$(awk -v start="$heredoc_start" -v end="$heredoc_end" \
        'NR > start && NR < end && $0 ~ /^(if|case|fi|esac)([[:space:]]|$)/ { print NR ":" $0; exit }' "$mode")
    if [[ -n "$control_line" ]]; then
        fail "scarlix-mode: shell control-flow leaked into .env heredoc: $control_line"
        return
    fi

    pass "trust flag logic precedes heredoc; only KEY=VALUE entry is inside heredoc (lines $case_line, $value_line)"
}

# ===========================================================================
# Run all checks
# ===========================================================================
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

# ===========================================================================
# Summary
# ===========================================================================
echo
printf "${BOLD}=== Smoke test: %d passed, %d failed, %d warned ===${NC}\n" \
    "$PASS_COUNT" "$FAIL_COUNT" "$WARN_COUNT"

if [[ $FAIL_COUNT -gt 0 ]]; then
    exit 1
fi
exit 0
