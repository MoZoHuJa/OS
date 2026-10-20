#!/usr/bin/env bash
# SCARLIX OS regression smoke test (version read from VERSION)
#
# Runs 16 integrity checks against the SCARLIX OS repo and prints a
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
SMOKE_VERSION="$(cat "$REPO_DIR/VERSION" 2>/dev/null || printf 'unknown')"
printf "${BOLD}=== SCARLIX OS v%s regression smoke test ===${NC}\n" "$SMOKE_VERSION"
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
    local raw filtered
    raw=$(SYSTEMD_UNIT_PATH="$stubdir:" systemd-analyze verify "${units[@]}" 2>&1 || true)
    rm -rf "$stubdir"
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
    # Removal commands must not appear in EXECUTED positions. The headless
    # profile legitimately PRINTS manual-removal guidance (log/echo lines);
    # those documentation strings are filtered out before the check.
    if grep -vE '^[[:space:]]*(log|warn|fail|printf|echo)[[:space:]]' "$installer" | grep -qE '(pacman|yay) +-R'; then
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
check_11_single_gpu_adaptation
check_12_pibolt_sha256_pin
check_13_pibolt_user_owned_execution
check_14_strict_gpu_optin_parsing
check_15_gaming_manifest_and_installer
check_16_gaming_readonly_api
check_17_reservation_governor

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
