#!/usr/bin/env bash
# tests/test_media_tools.sh — media-tools hardening tests
# (KPK-19.6.6-P2-03 remediation, v19.6.7).
#
# Starts REAL instances of media-tools/server.py on a random free loopback
# port, pointed by the server's env test seams (MEDIA_TOOLS_PORT,
# MEDIA_TOOLS_INPUT_DIR / _OUTPUT_DIR / _TEMPLATES_DIR) at temp dirs, and
# exercises the v19.6.7 posture end-to-end:
#
#   - /health is OPEN (liveness — no token, ever);
#   - fail-closed: with NO MEDIA_TOOLS_TOKEN, /convert and /templates
#     answer 503 and the body says the surface is closed;
#   - with a token: missing/wrong/non-bearer Authorization → 401;
#   - unknown template → 400; traversal input name (../etc/passwd) → 400;
#   - malformed Content-Length → 400 (a deliberate answer, not a crash);
#   - non-object JSON root ([1,2,3] / 42) → 400 (not a 500);
#   - a REAL ffmpeg conversion (wav → libmp3lame mp3) → 200 + the output
#     file exists — SKIPped with a warning when ffmpeg is unavailable;
#   - saturation: 5 truly-parallel conversions with MAX_CONCURRENCY=1 and
#     a genuinely slow template (~24s) → exactly one 200, one 503 (queue
#     full after the 15s wait) and three 429s (per-client cap 2), and the
#     server SURVIVES (/health still 200, no 5xx, no transport failures);
#   - output-name collision: two parallel conversions writing the SAME
#     output name → one 200, one 409 ("output name busy").
#
# All HTTP goes through `curl --noproxy '*'` (the repo's no-ambient-proxy
# discipline) EXCEPT the malformed Content-Length case, which needs a
# deliberately broken header that curl versions may normalize — that one
# request is emitted from a raw python3 socket (raw sockets have no proxy
# layer to be ambient about).
#
# Usage: bash tests/test_media_tools.sh   (from repo root)
# Exit:  0 all assertions pass (skips are allowed), 1 otherwise.
# Runtime: ~45s (the saturation phase waits out one ~24s ffmpeg template).

set -euo pipefail

TESTS_RUN=0
TESTS_FAILED=0
TESTS_SKIPPED=0

t_ok()   { TESTS_RUN=$((TESTS_RUN + 1)); printf 'ok   %s\n' "$*"; }
t_fail() { TESTS_RUN=$((TESTS_RUN + 1)); TESTS_FAILED=$((TESTS_FAILED + 1)); printf 'FAIL %s\n' "$*"; }
t_skip() { TESTS_SKIPPED=$((TESTS_SKIPPED + 1)); printf 'skip %s\n' "$*"; }

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

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVER="$REPO_ROOT/media-tools/server.py"

# ---------------------------------------------------------------------------
# Graceful skips — the suite is only meaningful where python3 + curl + a
# writable temp dir exist; it exits 0 (SKIP, not FAIL) without them.
# ---------------------------------------------------------------------------
if ! command -v python3 >/dev/null 2>&1; then
    echo "SKIP: python3 is unavailable — media-tools/server.py cannot run here."
    exit 0
fi
if ! command -v curl >/dev/null 2>&1; then
    echo "SKIP: curl is unavailable — the HTTP assertions cannot run here."
    exit 0
fi
if ! command -v ffmpeg >/dev/null 2>&1; then
    FFMPEG_PRESENT=0
else
    FFMPEG_PRESENT=1
fi
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/scarlix-media-tools-test.XXXXXX" 2>/dev/null)" || {
    echo "SKIP: no usable temp dir (${TMPDIR:-/tmp}) — cannot host the test dirs."
    exit 0
}
if [[ ! -f "$SERVER" ]]; then
    echo "FAIL: $SERVER is missing (the v19.6.7 hardening surface)."
    exit 1
fi

IN_DIR="$TMP_ROOT/input"
OUT_DIR="$TMP_ROOT/output"
TPL_DIR="$TMP_ROOT/templates"
mkdir -p "$IN_DIR" "$OUT_DIR" "$TPL_DIR"

# ---------------------------------------------------------------------------
# Server lifecycle: every phase restarts the server with the env it needs.
# ---------------------------------------------------------------------------
declare -a SERVER_PIDS=()
SERVER_PORT=""

cleanup() {
    local pid
    for pid in ${SERVER_PIDS[@]+"${SERVER_PIDS[@]}"}; do
        kill "$pid" 2>/dev/null || true
    done
    for pid in ${SERVER_PIDS[@]+"${SERVER_PIDS[@]}"}; do
        wait "$pid" 2>/dev/null || true
    done
    rm -rf "$TMP_ROOT"
}
trap cleanup EXIT INT TERM

free_port() {
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

# start_server <logfile> [KEY=VAL ...] — launch with the temp-dir seams plus
# the caller's extra env (token / max concurrency), then wait for /health.
start_server() {
    local log="$1"; shift
    local port i
    port="$(free_port)"
    : >"$log"
    env \
        MEDIA_TOOLS_PORT="$port" \
        MEDIA_TOOLS_INPUT_DIR="$IN_DIR" \
        MEDIA_TOOLS_OUTPUT_DIR="$OUT_DIR" \
        MEDIA_TOOLS_TEMPLATES_DIR="$TPL_DIR" \
        "$@" \
        python3 "$SERVER" >>"$log" 2>&1 &
    SERVER_PIDS+=("$!")
    SERVER_PORT="$port"
    for i in $(seq 1 60); do
        if curl --noproxy '*' -s -o /dev/null --max-time 2 \
                "http://127.0.0.1:$port/health" 2>/dev/null; then
            return 0
        fi
        sleep 0.2
    done
    echo "FATAL: the test server never became healthy on port $port" >&2
    cat "$log" >&2 || true
    exit 1
}

stop_servers() {
    local pid
    for pid in ${SERVER_PIDS[@]+"${SERVER_PIDS[@]}"}; do
        kill "$pid" 2>/dev/null || true
    done
    for pid in ${SERVER_PIDS[@]+"${SERVER_PIDS[@]}"}; do
        wait "$pid" 2>/dev/null || true
    done
    SERVER_PIDS=()
}

# http <METHOD> <path> [extra curl args…] — STATUS gets the response code,
# the body lands in $BODY_FILE. Every request rides curl --noproxy '*'.
BODY_FILE="$TMP_ROOT/body.txt"
STATUS=""
http() {
    local method="$1" path="$2"
    shift 2
    STATUS="$(curl --noproxy '*' -s -o "$BODY_FILE" -w '%{http_code}' \
        --max-time 90 -X "$method" "$@" \
        "http://127.0.0.1:$SERVER_PORT$path" 2>/dev/null || true)"
}

body() { cat "$BODY_FILE" 2>/dev/null || true; }

# A raw-socket emitter for the ONE request curl cannot be trusted to relay
# verbatim on every version (a deliberately broken Content-Length header).
RAWREQ="$TMP_ROOT/rawreq.py"
cat > "$RAWREQ" <<'PYEOF'
#!/usr/bin/env python3
# rawreq.py — test helper: send the raw HTTP request from stdin to
# 127.0.0.1:<port>; print the status code on line 1, the body after it.
import socket
import sys

port = int(sys.argv[1])
req = sys.stdin.buffer.read()
s = socket.create_connection(("127.0.0.1", port), timeout=10)
s.sendall(req)
s.settimeout(10)
chunks = []
try:
    while True:
        chunk = s.recv(65536)
        if not chunk:
            break
        chunks.append(chunk)
except socket.timeout:
    pass
s.close()
head, _, body = b"".join(chunks).partition(b"\r\n\r\n")
line = head.decode("latin-1", "replace").split("\r\n")[0]
parts = line.split()
print(parts[1] if len(parts) > 1 else "???")
sys.stdout.write(body.decode("utf-8", "replace"))
PYEOF

TOKEN_VALUE="test-token-123"
AUTH_HDR="Authorization: Bearer $TOKEN_VALUE"
REQ='{"template":"audio-to-mp3","input":"tone.wav","output":"tone.mp3"}'

# The conversion template the phases share (the repo's documented example,
# libmp3lame VBR quality 9).
cat > "$TPL_DIR/audio-to-mp3.json" <<'EOF'
{
  "tool": "ffmpeg",
  "args": ["-i", "{input}", "-vn", "-c:a", "libmp3lame", "-q:a", "9", "{output}"]
}
EOF

echo "=== media-tools hardening tests (KPK-19.6.6-P2-03, v19.6.7) ==="
echo "repo: $REPO_ROOT   server: $SERVER   tmp: $TMP_ROOT"
echo

# ---------------------------------------------------------------------------
# Phase 1 — fail-closed: no MEDIA_TOOLS_TOKEN configured at all.
# ---------------------------------------------------------------------------
echo "--- phase 1: fail-closed (MEDIA_TOOLS_TOKEN unset) ---"
start_server "$TMP_ROOT/server-noauth.log"

http GET /health
assert_eq "$STATUS" "200" "/health is open without a token (liveness)"
assert_contains "$(body)" '"ok"' "/health body says ok"

http POST /convert --data-binary "$REQ"
assert_eq "$STATUS" "503" "/convert without MEDIA_TOOLS_TOKEN → 503 fail-closed"
assert_contains "$(body)" "MEDIA_TOOLS_TOKEN not configured" \
    "503 body names the missing token"
assert_contains "$(body)" "conversion surface is closed" \
    "503 body states the surface is closed"

http POST /templates --data-binary ''
assert_eq "$STATUS" "503" "POST /templates without a token → 503 fail-closed"
http GET /templates
assert_eq "$STATUS" "503" "GET /templates without a token → 503 fail-closed"

assert_contains "$(cat "$TMP_ROOT/server-noauth.log")" "FAIL-CLOSED" \
    "startup log announces the fail-closed mode"

stop_servers

# ---------------------------------------------------------------------------
# Phase 2 — bearer auth + input shape discipline.
# ---------------------------------------------------------------------------
echo "--- phase 2: bearer-token auth ($TOKEN_VALUE) ---"
start_server "$TMP_ROOT/server-auth.log" "MEDIA_TOOLS_TOKEN=$TOKEN_VALUE"

http GET /health
assert_eq "$STATUS" "200" "/health stays open with the token set (liveness)"

http POST /convert --data-binary "$REQ"
assert_eq "$STATUS" "401" "/convert without Authorization → 401"
assert_contains "$(body)" '"error"' "401 body is a JSON error"

http POST /convert -H "Authorization: Bearer wrong-token" --data-binary "$REQ"
assert_eq "$STATUS" "401" "/convert with a wrong bearer → 401"

http POST /convert -H "Authorization: Basic dXNlcjpwYXNz" --data-binary "$REQ"
assert_eq "$STATUS" "401" "/convert with a non-bearer Authorization → 401"

http GET /templates
assert_eq "$STATUS" "401" "GET /templates without a token → 401"
http GET /templates -H "$AUTH_HDR"
assert_eq "$STATUS" "200" "GET /templates with the right token → 200"
assert_contains "$(body)" "audio-to-mp3" "listing shows the template"

REQ_MISSING='{"template":"no-such-template","input":"tone.wav","output":"x.mp3"}'
http POST /convert -H "$AUTH_HDR" --data-binary "$REQ_MISSING"
assert_eq "$STATUS" "400" "right token + unknown template → 400"
assert_contains "$(body)" "does not exist" "400 body says the template does not exist"

REQ_TRAV='{"template":"audio-to-mp3","input":"../etc/passwd","output":"stolen.mp3"}'
http POST /convert -H "$AUTH_HDR" --data-binary "$REQ_TRAV"
assert_eq "$STATUS" "400" "right token + traversal input name → 400"
assert_contains "$(body)" "not a plain file name" \
    "400 body refuses the traversal name"

# Malformed Content-Length: raw socket (see header comment for why not curl).
RAW_OUT="$(printf 'POST /convert HTTP/1.1\r\nHost: scarlix-test\r\nAuthorization: Bearer %s\r\nContent-Length: abc\r\n\r\n' \
    "$TOKEN_VALUE" | python3 "$RAWREQ" "$SERVER_PORT")"
RAW_STATUS="$(printf '%s\n' "$RAW_OUT" | head -1)"
RAW_BODY="$(printf '%s\n' "$RAW_OUT" | tail -n +2)"
assert_eq "$RAW_STATUS" "400" "malformed Content-Length → 400 (a deliberate answer)"
assert_contains "$RAW_BODY" "invalid Content-Length" \
    "400 body names the invalid Content-Length"

http POST /convert -H "$AUTH_HDR" --data-binary '[1,2,3]'
assert_eq "$STATUS" "400" "non-object JSON root ([1,2,3]) → 400"
assert_contains "$(body)" "request body must be a JSON object" \
    "400 body demands a JSON object"

http POST /convert -H "$AUTH_HDR" --data-binary '42'
assert_eq "$STATUS" "400" "non-object JSON root (42) → 400"

http POST /convert -H "$AUTH_HDR" --data-binary 'not-json-at-all'
assert_eq "$STATUS" "400" "invalid JSON body → 400"
assert_contains "$(body)" "invalid JSON body" "400 body says invalid JSON"

# ---------------------------------------------------------------------------
# Phase 3 — a real conversion, if this machine has a capable ffmpeg.
# ---------------------------------------------------------------------------
echo "--- phase 3: a real ffmpeg conversion ---"
CONV_OK=0
if [[ "$FFMPEG_PRESENT" == 1 ]]; then
    # NOTE: grep WITHOUT -q — with `set -o pipefail`, `grep -q` exits on the
    # first match, ffmpeg then takes SIGPIPE (141) and the pipeline reads
    # as "no such encoder" even though the match succeeded.
    if ffmpeg -hide_banner -loglevel error -y \
            -f lavfi -i 'sine=frequency=440:duration=1' \
            -c:a pcm_s16le "$IN_DIR/tone.wav" 2>"$TMP_ROOT/genwav.err" \
        && ffmpeg -hide_banner -encoders 2>/dev/null | grep libmp3lame >/dev/null \
        && ffmpeg -hide_banner -filters 2>/dev/null | grep anoisesrc >/dev/null; then
        CONV_OK=1
        t_ok "ffmpeg present: input wav generated + libmp3lame + lavfi anoisesrc available"
    else
        t_skip "ffmpeg present but lacks libmp3lame/lavfi or input generation failed — real-conversion sub-tests skipped"
    fi
else
    t_skip "ffmpeg not on PATH — real-conversion sub-tests skipped (the auth/shape posture above is still fully asserted)"
fi

if [[ "$CONV_OK" == 1 ]]; then
    http POST /convert -H "$AUTH_HDR" --data-binary "$REQ"
    assert_eq "$STATUS" "200" "real conversion (wav → libmp3lame mp3) → 200"
    if [[ -f "$OUT_DIR/tone.mp3" ]]; then
        t_ok "the converted output file exists ($OUT_DIR/tone.mp3)"
    else
        t_fail "the converted output file is missing ($OUT_DIR/tone.mp3)"
    fi
fi

# ---------------------------------------------------------------------------
# Phase 4 — saturation: MAX_CONCURRENCY=1, five truly-parallel slow
# conversions. Expected outcome (deterministic by design): the per-client
# cap (2) 429s three requests immediately; of the two admitted, one holds
# the single conversion slot for ~24s (200) and the other exhausts the 15s
# queue wait (503). The point under test: the server SURVIVES the storm —
# bounded answers everywhere, never a 500, /health still 200 afterwards.
# ---------------------------------------------------------------------------
echo "--- phase 4: saturation (MAX_CONCURRENCY=1, slow template) ---"
if [[ "$CONV_OK" == 1 ]]; then
    # -re paces the lavfi noise source at its native rate → ~24s of wall
    # clock on purpose: the 15s queue bound must fire BEFORE it finishes.
    cat > "$TPL_DIR/slow-noise.json" <<'EOF'
{
  "tool": "ffmpeg",
  "args": ["-re", "-f", "lavfi", "-i", "anoisesrc=d=25", "-q:a", "9", "{output}"]
}
EOF
    stop_servers
    start_server "$TMP_ROOT/server-sat.log" \
        "MEDIA_TOOLS_TOKEN=$TOKEN_VALUE" "MEDIA_TOOLS_MAX_CONCURRENCY=1"

    : >"$TMP_ROOT/sat.codes"
    # Wait ONLY for the curl subshells — a bare `wait` would also wait for
    # the SERVER (a background job that never exits) and hang the phase.
    SAT_CURL_PIDS=()
    for i in 1 2 3 4 5; do
        (
            code="$(curl --noproxy '*' -s -o "$TMP_ROOT/sat-$i.body" \
                -w '%{http_code}' --max-time 90 -X POST \
                -H "$AUTH_HDR" \
                --data-binary "{\"template\":\"slow-noise\",\"input\":\"tone.wav\",\"output\":\"sat-$i.mp3\"}" \
                "http://127.0.0.1:$SERVER_PORT/convert" 2>/dev/null || true)"
            printf '%s\n' "$code" >>"$TMP_ROOT/sat.codes"
        ) &
        SAT_CURL_PIDS+=("$!")
    done
    for p in "${SAT_CURL_PIDS[@]}"; do
        wait "$p" 2>/dev/null || true
    done

    n200=0 n429=0 n503=0 nother=0
    for c in $(cat "$TMP_ROOT/sat.codes"); do
        case "$c" in
            200) n200=$((n200 + 1)) ;;
            429) n429=$((n429 + 1)) ;;
            503) n503=$((n503 + 1)) ;;
            *)   nother=$((nother + 1)) ;;
        esac
    done
    assert_eq "$nother" "0" \
        "saturation: no unexpected status (no 500s, no transport failures)"
    assert_eq "$n200" "1" \
        "saturation: exactly one conversion ran to completion (slot bound = 1)"
    assert_eq "$n429" "3" \
        "saturation: the per-client cap (2) refused the three requests beyond it (429)"
    assert_eq "$n503" "1" \
        "saturation: the 15s queue wait expired into a deliberate 503"
    if compgen -G "$OUT_DIR/sat-*.mp3" >/dev/null; then
        t_ok "saturation: the completed conversion produced its output file"
    else
        t_fail "saturation: no sat-*.mp3 output was produced"
    fi
    http GET /health
    assert_eq "$STATUS" "200" "saturation: /health still 200 after the storm"
else
    t_skip "saturation + collision phases need a capable ffmpeg — skipped"
fi

# ---------------------------------------------------------------------------
# Phase 5 — output-name collision: two parallel conversions writing the
# SAME output name with MAX_CONCURRENCY=2 (both fit the slot bound, so the
# NAME is the only contention left). One must win (200), the twin must be
# refused with 409 "output name busy" — never two subprocesses interleaving
# writes into one file, and never a 500.
# ---------------------------------------------------------------------------
echo "--- phase 5: output-name collision (MAX_CONCURRENCY=2) ---"
if [[ "$CONV_OK" == 1 ]]; then
    # ~6s of wall clock — slow enough that both requests overlap for sure.
    cat > "$TPL_DIR/medium-noise.json" <<'EOF'
{
  "tool": "ffmpeg",
  "args": ["-re", "-f", "lavfi", "-i", "anoisesrc=d=6", "-q:a", "9", "{output}"]
}
EOF
    stop_servers
    start_server "$TMP_ROOT/server-coll.log" \
        "MEDIA_TOOLS_TOKEN=$TOKEN_VALUE" "MEDIA_TOOLS_MAX_CONCURRENCY=2"

    : >"$TMP_ROOT/coll.codes"
    # Same discipline: wait only for the two curl subshells, never the server.
    COLL_CURL_PIDS=()
    for i in 1 2; do
        (
            code="$(curl --noproxy '*' -s -o "$TMP_ROOT/coll-$i.body" \
                -w '%{http_code}' --max-time 90 -X POST \
                -H "$AUTH_HDR" \
                --data-binary '{"template":"medium-noise","input":"tone.wav","output":"collide.mp3"}' \
                "http://127.0.0.1:$SERVER_PORT/convert" 2>/dev/null || true)"
            printf '%s\n' "$code" >>"$TMP_ROOT/coll.codes"
        ) &
        COLL_CURL_PIDS+=("$!")
    done
    for p in "${COLL_CURL_PIDS[@]}"; do
        wait "$p" 2>/dev/null || true
    done

    c200=0 c409=0 cother=0
    for c in $(cat "$TMP_ROOT/coll.codes"); do
        case "$c" in
            200) c200=$((c200 + 1)) ;;
            409) c409=$((c409 + 1)) ;;
            *)   cother=$((cother + 1)) ;;
        esac
    done
    assert_eq "$cother" "0" "collision: no unexpected status (no 500s)"
    assert_eq "$c200" "1" "collision: exactly one conversion won the name (200)"
    assert_eq "$c409" "1" "collision: the twin was refused — output name busy (409)"
    assert_contains "$(cat "$TMP_ROOT/coll-1.body" "$TMP_ROOT/coll-2.body" 2>/dev/null || true)" \
        "output name busy" "the 409 body names the busy output"
    if [[ -f "$OUT_DIR/collide.mp3" ]]; then
        t_ok "collision: the winning conversion wrote collide.mp3"
    else
        t_fail "collision: collide.mp3 was never written"
    fi
    http GET /health
    assert_eq "$STATUS" "200" "collision: /health still 200 (the server survived)"
fi

stop_servers

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo
if [[ $TESTS_FAILED -eq 0 ]]; then
    printf '=== media-tools hardening tests: %d run, 0 failed, %d skipped ===\n' \
        "$TESTS_RUN" "$TESTS_SKIPPED"
    exit 0
fi
printf '=== media-tools hardening tests: %d run, %d FAILED, %d skipped ===\n' \
    "$TESTS_RUN" "$TESTS_FAILED" "$TESTS_SKIPPED"
exit 1
