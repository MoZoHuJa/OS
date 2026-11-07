#!/usr/bin/env python3
"""SCARLIX OS v19.6.7 — Media Tools conversion service (KPK-19.6.6-P2-03 hardening).

A deliberately small Python-stdlib HTTP service implementing the compose
contract the media-tools stack always declared: port 8200, /data/input,
/data/output, /data/templates. No pip packages, no framework, NO network
egress — a conversion reads an input file, writes an output file, nothing
else. Bound to 0.0.0.0 INSIDE the container; the compose file publishes it
loopback-only (127.0.0.1:8200). Loopback publishing does NOT stop OTHER
containers on the shared external scarlix-net from reaching port 8200 —
which is exactly why v19.6.7 puts an auth token in front of the conversion
surface (the independent audit's finding: the service used to accept
conversion requests from ANY peer with a route to the port).

Endpoints:
    GET  /health            → {"status":"ok","service":"media-tools"}
                              OPEN — liveness, no token (the healthcheck
                              and the operator must tell "running" from
                              "closed").
    POST /convert           → run an ffmpeg/imagemagick template
         body: {"template": "audio-to-mp3",
                "input": "song.wav",
                "output": "song.mp3",
                "tool": "ffmpeg"}          # optional: "ffmpeg"|"magick"
    GET/POST /templates     → list the templates present. The listing sits
                              behind the SAME auth gate as /convert — the
                              template inventory is part of the surface,
                              not a freebie for unauthenticated peers.

Templates are JSON files in /data/templates: {"args": ["-i", "{input}",
"-vn", "-c:a", "libmp3lame", "-q:a", "2", "{output}"]} — {input} and
{output} are substituted with the SANITIZED absolute paths. A template may
carry "tool": "magick" to run imagemagick instead of ffmpeg.

Security posture (the family-OS baseline; [v19.6.7] marks the
KPK-19.6.6-P2-03 hardening):
    * AUTH TOKEN, FAIL-CLOSED [v19.6.7]: MEDIA_TOOLS_TOKEN from the env
      (generate-env.sh writes it into /etc/scarlix/.env; the compose file
      interpolates it). With the token set, /convert and /templates demand
      "Authorization: Bearer <token>", compared with hmac.compare_digest
      (constant time — no early-exit timing leak); wrong or missing → 401
      with a JSON error body. With NO token configured both answer 503 and
      say so: "no token" must mean CLOSED, never OPEN. This mirrors the
      SCARLIHQ_TOKEN discipline one stack over.
    * BOUNDED CONCURRENCY [v19.6.7]: at most MEDIA_TOOLS_MAX_CONCURRENCY
      conversions run at once (default 2, validated and clamped 1..16).
      Every accepted conversion may hold an ffmpeg/magick subprocess for
      up to 600s; the old unbounded ThreadingHTTPServer let ONE reachable
      peer saturate the container with parallel conversions. A request
      that cannot get a slot within 15s answers 503 "queue full" instead
      of queueing forever.
    * PER-CLIENT CAP [v19.6.7]: at most 2 in-flight conversions per client
      IP → 429 beyond. One peer cannot eat the whole concurrency budget;
      the rest of the family keeps converting.
    * OUTPUT COLLISION POLICY [v19.6.7]: the output name of an in-flight
      conversion is reserved; a second conversion to the same name answers
      409 instead of two subprocesses interleaving writes into one file
      (the winner is whatever reserved the name first — no favorites).
    * READ TIMEOUT [v19.6.7]: handler sockets carry a 30s read timeout —
      a client that stalls mid-request cannot pin a thread forever.
    * input/output MUST be plain file names (basename-equal, no traversal);
    * the template MUST exist as a file under /data/templates (no inline
      argument injection — an API caller NEVER passes raw argv);
    * subprocess runs with a fixed argv list (no shell), cwd=/tmp, with
      the 600s bound;
    * malformed Content-Length → a DELIBERATE 400 (it used to raise
      ValueError out of the handler — a crash, not an answer); a decoded
      JSON root that is not an object → 400 (it used to AttributeError
      into a 500);
    * every request is logged to stdout (client, path, status).

Env overrides (test seams — the container paths are the compose contract,
production sets none of the first four):
    MEDIA_TOOLS_PORT               listen port (default 8200)
    MEDIA_TOOLS_INPUT_DIR          default /data/input
    MEDIA_TOOLS_OUTPUT_DIR         default /data/output
    MEDIA_TOOLS_TEMPLATES_DIR      default /data/templates
    MEDIA_TOOLS_TOKEN              auth token (unset/empty = fail-closed)
    MEDIA_TOOLS_MAX_CONCURRENCY    default 2, clamped 1..16
They exist so tests/test_media_tools.sh can run REAL server instances on a
random port against temp dirs; a non-integer MEDIA_TOOLS_PORT fails loudly
at startup (config errors are never silently papered over here).
"""

import hmac
import json
import os
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LISTEN = ("0.0.0.0", int(os.environ.get("MEDIA_TOOLS_PORT", "8200")))
INPUT_DIR = os.environ.get("MEDIA_TOOLS_INPUT_DIR", "/data/input")
OUTPUT_DIR = os.environ.get("MEDIA_TOOLS_OUTPUT_DIR", "/data/output")
TEMPLATES_DIR = os.environ.get("MEDIA_TOOLS_TEMPLATES_DIR", "/data/templates")
MAX_BODY = 64 * 1024  # a conversion request is tiny — bound it anyway

TOOLS = ("ffmpeg", "magick")

# --- v19.6.7 (KPK-19.6.6-P2-03) hardening knobs -----------------------------
# The token is stripped so a stray trailing newline in a hand-edited .env
# cannot silently break every comparison — whitespace-padded secrets are a
# configuration error, and an empty value means fail-closed like unset.
TOKEN = os.environ.get("MEDIA_TOOLS_TOKEN", "").strip()
QUEUE_WAIT_S = 15  # how long a request may wait for a conversion slot
MAX_PER_CLIENT = 2  # in-flight conversions per client IP


def _env_clamped_int(name: str, default: int, lo: int, hi: int) -> int:
    """Read an int env override: invalid → the default, the value clamped."""
    raw = os.environ.get(name)
    if raw is None or not raw.strip():
        return default
    try:
        val = int(raw.strip())
    except ValueError:
        print(
            f"[media-tools] WARN: {name}={raw!r} is not an integer — "
            f"using the default {default}",
            flush=True,
        )
        return default
    clamped = max(lo, min(hi, val))
    if clamped != val:
        print(
            f"[media-tools] WARN: {name}={val} outside {lo}..{hi} — "
            f"clamped to {clamped}",
            flush=True,
        )
    return clamped


MAX_CONCURRENCY = _env_clamped_int("MEDIA_TOOLS_MAX_CONCURRENCY", 2, 1, 16)
# The conversion slots. BoundedSemaphore (not a plain Semaphore): an
# over-release is a bookkeeping bug and MUST blow up instead of quietly
# growing the cap. Threads acquire with a bounded wait — see QUEUE_WAIT_S.
SLOTS = threading.BoundedSemaphore(MAX_CONCURRENCY)

# One lock guards the two small bookkeeping structures below: per-client
# in-flight counts and the active output-name set. Every critical section
# is a couple of dict/set operations — the subprocess NEVER runs under this
# lock, so a 600s ffmpeg cannot block anybody's accounting.
_state_lock = threading.Lock()
_inflight_by_client: dict = {}
_active_outputs: set = set()


def _client_slot_take(client: str) -> bool:
    """Atomically take one in-flight slot for the client. False = capped."""
    with _state_lock:
        n = _inflight_by_client.get(client, 0)
        if n >= MAX_PER_CLIENT:
            return False
        _inflight_by_client[client] = n + 1
        return True


def _client_slot_release(client: str) -> None:
    with _state_lock:
        n = _inflight_by_client.get(client, 1) - 1
        if n > 0:
            _inflight_by_client[client] = n
        else:
            _inflight_by_client.pop(client, None)


def _output_reserve(name: str) -> bool:
    """Atomically reserve the output name. False = someone is writing it."""
    with _state_lock:
        if name in _active_outputs:
            return False
        _active_outputs.add(name)
        return True


def _output_release(name: str) -> None:
    with _state_lock:
        _active_outputs.discard(name)


def sanitize_filename(name: str) -> str:
    """A plain file name only: no paths, no traversal, no empty string."""
    if not isinstance(name, str) or not name or len(name) > 255:
        raise ValueError("input/output must be a file name (1-255 chars)")
    if os.path.basename(name) != name or name in (".", ".."):
        raise ValueError(f"{name!r} is not a plain file name (no paths)")
    if "\x00" in name:
        raise ValueError("NUL byte in file name")
    return name


def load_template(name: str) -> dict:
    if not isinstance(name, str) or not name or len(name) > 128:
        raise ValueError("template must be a name (1-128 chars)")
    if os.path.basename(name) != name or name in (".", ".."):
        raise ValueError(f"{name!r} is not a plain template name (no paths)")
    path = os.path.join(TEMPLATES_DIR, name + ".json")
    if not os.path.isfile(path):
        raise ValueError(f"template {name!r} does not exist")
    with open(path, encoding="utf-8") as fh:
        spec = json.load(fh)
    args = spec.get("args")
    if not isinstance(args, list) or not args or not all(
        isinstance(a, str) for a in args
    ):
        raise ValueError(f"template {name!r} must carry a non-empty args list")
    tool = spec.get("tool", "ffmpeg")
    if tool not in TOOLS:
        raise ValueError(f"template {name!r} tool must be one of {TOOLS}")
    return {"tool": tool, "args": args}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "scarlix-media-tools/19.6.7"
    # v19.6.7: a 30s read timeout on the handler socket — a client that
    # stalls mid-request (or parks a keep-alive connection) cannot pin a
    # thread forever; BaseHTTPRequestHandler turns the timeout into a quiet
    # connection close.
    timeout = 30

    def _json(self, code: int, payload: dict, close: bool = False) -> None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        if close:
            # "close" is set on every early refusal whose request body was
            # NOT fully read — closing beats desyncing a keep-alive pipe
            # the caller may still be writing into.
            self.send_header("Connection", "close")
            self.close_connection = True
        self.end_headers()
        self.wfile.write(body)

    def _authorized(self) -> bool:
        """Auth gate for the conversion surface; sends the refusal itself.

        Fail-closed both ways: no token configured → the surface is CLOSED
        (503, named reason — an unauthenticated converter on the shared
        scarlix-net is the audit's finding, so "no token" can never degrade
        to open). Token configured → the bearer must match, compared with
        hmac.compare_digest so the answer does not leak match timing.
        """
        if not TOKEN:
            self._json(
                503,
                {
                    "error": (
                        "MEDIA_TOOLS_TOKEN not configured — the conversion "
                        "surface is closed (set it in /etc/scarlix/.env and "
                        "restart the stack)"
                    )
                },
                close=True,
            )
            return False
        supplied = self.headers.get("Authorization", "")
        expected = "Bearer " + TOKEN
        if not hmac.compare_digest(
            supplied.encode("utf-8"), expected.encode("utf-8")
        ):
            self._json(
                401,
                {"error": "missing or invalid Authorization bearer token"},
                close=True,
            )
            return False
        return True

    def _list_templates(self) -> None:
        try:
            names = sorted(
                f[: -len(".json")]
                for f in os.listdir(TEMPLATES_DIR)
                if f.endswith(".json")
            )
        except FileNotFoundError:
            names = []
        self._json(200, {"templates": names})

    def do_GET(self) -> None:
        if self.path == "/health":
            # Liveness stays OPEN — no token: the healthcheck (and any
            # operator) must be able to tell "running" from "closed".
            self._json(200, {"status": "ok", "service": "media-tools"})
            return
        if self.path == "/templates":
            if not self._authorized():
                return
            self._list_templates()
            return
        self._json(404, {"error": "not found"})

    def do_POST(self) -> None:
        if self.path == "/templates":
            # A listing through POST is the same listing as GET — behind
            # the same auth gate. The body (if any) is drained so the
            # keep-alive pipe stays in sync.
            if not self._authorized():
                return
            try:
                length = int(self.headers.get("Content-Length") or 0)
            except ValueError:
                self._json(400, {"error": "invalid Content-Length"}, close=True)
                return
            if length <= 0 or length > MAX_BODY:
                # No body is expected for a listing; a huge one is refused
                # outright rather than drained.
                self._json(
                    400, {"error": "listing takes no body (max 64 KiB)"}, close=True
                )
                return
            try:
                self.rfile.read(length)
            except OSError:
                self.close_connection = True
            self._list_templates()
            return
        if self.path != "/convert":
            self._json(404, {"error": "not found"}, close=True)
            return
        if not self._authorized():
            return
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            # v19.6.7: a malformed Content-Length used to raise straight
            # out of the handler (an unhandled ValueError — a crash, not
            # an answer). It is a bad request; answer it as one.
            self._json(400, {"error": "invalid Content-Length"}, close=True)
            return
        if length <= 0 or length > MAX_BODY:
            self._json(400, {"error": "body required (max 64 KiB)"}, close=True)
            return
        try:
            req = json.loads(self.rfile.read(length))
        except (ValueError, UnicodeDecodeError, OSError):
            # OSError covers a read timeout on a client that stalls
            # mid-body — the 30s handler timeout turns it into a refusal
            # instead of a pinned thread.
            self._json(400, {"error": "invalid JSON body"}, close=True)
            return
        if not isinstance(req, dict):
            # v19.6.7: a JSON list/number/string at the root used to die
            # on req.get() with AttributeError → a 500. It is a shape
            # error; answer 400 and say what the body must be.
            self._json(
                400, {"error": "request body must be a JSON object"}, close=True
            )
            return
        try:
            template = load_template(req.get("template", ""))
            in_name = sanitize_filename(req.get("input", ""))
            out_name = sanitize_filename(req.get("output", ""))
        except ValueError as exc:
            self._json(400, {"error": str(exc)}, close=True)
            return
        in_path = os.path.join(INPUT_DIR, in_name)
        out_path = os.path.join(OUTPUT_DIR, out_name)
        if not os.path.isfile(in_path):
            self._json(404, {"error": f"input {in_name!r} not found"})
            return
        argv = [
            a.replace("{input}", in_path).replace("{output}", out_path)
            for a in template["args"]
        ]
        if not any(a == out_path or a.endswith(out_path) for a in argv):
            # The template must actually declare the output — a conversion
            # that never writes {output} is a broken template, refuse it.
            self._json(
                400,
                {"error": "template carries no {output} argument"},
            )
            return

        # --- v19.6.7 resource guards: take exactly what you release -----
        # Order: per-client slot → output-name reservation → conversion
        # slot. Every refusal below is a DELIBERATE status code, and the
        # finally block releases exactly what was taken — no leaks, no
        # over-releases (BoundedSemaphore would call the latter out).
        client = self.client_address[0]
        if not _client_slot_take(client):
            self._json(
                429, {"error": "too many concurrent conversions from one client"}
            )
            return
        reserved_output = False
        got_slot = False
        try:
            reserved_output = _output_reserve(out_name)
            if not reserved_output:
                self._json(
                    409, {"error": "output name busy — wait or choose another"}
                )
                return
            if not SLOTS.acquire(timeout=QUEUE_WAIT_S):
                self._json(
                    503, {"error": "conversion queue full — retry later"}
                )
                return
            got_slot = True
            try:
                proc = subprocess.run(  # noqa: S603 — fixed argv, no shell
                    [template["tool"], *argv],
                    capture_output=True,
                    text=True,
                    errors="replace",  # odd bytes in tool stderr — never a 500
                    timeout=600,
                    cwd="/tmp",
                )
            except subprocess.TimeoutExpired:
                self._json(504, {"error": "conversion exceeded the 600s bound"})
                return
            except FileNotFoundError:
                self._json(500, {"error": f"{template['tool']} is unavailable"})
                return
            ok = proc.returncode == 0 and os.path.isfile(out_path)
            self._json(
                200 if ok else 422,
                {
                    "template": req.get("template"),
                    "input": in_name,
                    "output": out_name,
                    "exit": proc.returncode,
                    "stderr_tail": proc.stderr[-512:],
                },
            )
        finally:
            if got_slot:
                SLOTS.release()
            if reserved_output:
                _output_release(out_name)
            _client_slot_release(client)

    def log_message(self, fmt: str, *args) -> None:  # structured stdout log
        print(f"[media-tools] {self.address_string()} {fmt % args}", flush=True)


def main() -> None:
    print(
        f"[media-tools] listening on {LISTEN[0]}:{LISTEN[1]} "
        f"(input={INPUT_DIR}, output={OUTPUT_DIR}, templates={TEMPLATES_DIR})",
        flush=True,
    )
    if TOKEN:
        print(
            "[media-tools] auth: MEDIA_TOOLS_TOKEN set — /convert and "
            "/templates require 'Authorization: Bearer <token>' "
            "(constant-time compare)",
            flush=True,
        )
    else:
        print(
            "[media-tools] auth: MEDIA_TOOLS_TOKEN NOT set — FAIL-CLOSED: "
            "/convert and /templates answer 503 until the token is "
            "configured in /etc/scarlix/.env and the stack restarted "
            "(/health stays open)",
            flush=True,
        )
    print(
        f"[media-tools] concurrency: max {MAX_CONCURRENCY} simultaneous "
        f"conversions (queue wait {QUEUE_WAIT_S}s), max {MAX_PER_CLIENT} "
        f"in-flight per client IP, 30s read timeout",
        flush=True,
    )
    ThreadingHTTPServer(LISTEN, Handler).serve_forever()


if __name__ == "__main__":
    main()
