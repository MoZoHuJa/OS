#!/usr/bin/env python3
"""SCARLIX OS v19.6.6 — Media Tools conversion service (KPK-19.6.5-P2-04).

A deliberately small Python-stdlib HTTP service implementing the compose
contract the media-tools stack always declared: port 8200, /data/input,
/data/output, /data/templates. No pip packages, no framework, NO network
egress — a conversion reads an input file, writes an output file, nothing
else. Bound to 0.0.0.0 INSIDE the container; the compose file publishes it
loopback-only (127.0.0.1:8200).

Endpoints:
    GET  /health            → {"status":"ok","service":"media-tools"}
    POST /convert           → run an ffmpeg/imagemagick template
         body: {"template": "audio-to-mp3",
                "input": "song.wav",
                "output": "song.mp3",
                "tool": "ffmpeg"}          # optional: "ffmpeg"|"magick"
    POST /templates         → list the templates present

Templates are JSON files in /data/templates: {"args": ["-i", "{input}",
"-vn", "-c:a", "libmp3lame", "-q:a", "2", "{output}"]} — {input} and
{output} are substituted with the SANITIZED absolute paths. A template may
carry "tool": "magick" to run imagemagick instead of ffmpeg.

Security posture (the family-OS baseline):
    * input/output MUST be plain file names (basename-equal, no traversal);
    * the template MUST exist as a file under /data/templates (no inline
      argument injection — an API caller NEVER passes raw argv);
    * subprocess runs with a fixed argv list (no shell), cwd=/tmp;
    * every request is logged to stdout (template, input, output, exit).
"""

import json
import os
import subprocess
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LISTEN = ("0.0.0.0", 8200)
INPUT_DIR = "/data/input"
OUTPUT_DIR = "/data/output"
TEMPLATES_DIR = "/data/templates"
MAX_BODY = 64 * 1024  # a conversion request is tiny — bound it anyway

TOOLS = ("ffmpeg", "magick")


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
    server_version = "scarlix-media-tools/19.6.6"

    def _json(self, code: int, payload: dict) -> None:
        body = json.dumps(payload).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:
        if self.path == "/health":
            self._json(200, {"status": "ok", "service": "media-tools"})
            return
        if self.path == "/templates":
            try:
                names = sorted(
                    f[: -len(".json")]
                    for f in os.listdir(TEMPLATES_DIR)
                    if f.endswith(".json")
                )
            except FileNotFoundError:
                names = []
            self._json(200, {"templates": names})
            return
        self._json(404, {"error": "not found"})

    def do_POST(self) -> None:
        if self.path != "/convert":
            self._json(404, {"error": "not found"})
            return
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0 or length > MAX_BODY:
            self._json(400, {"error": "body required (max 64 KiB)"})
            return
        try:
            req = json.loads(self.rfile.read(length))
        except (ValueError, UnicodeDecodeError):
            self._json(400, {"error": "invalid JSON body"})
            return
        try:
            template = load_template(req.get("template", ""))
            in_name = sanitize_filename(req.get("input", ""))
            out_name = sanitize_filename(req.get("output", ""))
        except ValueError as exc:
            self._json(400, {"error": str(exc)})
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
        try:
            proc = subprocess.run(  # noqa: S603 — fixed argv, no shell
                [template["tool"], *argv],
                capture_output=True,
                text=True,
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

    def log_message(self, fmt: str, *args) -> None:  # structured stdout log
        print(f"[media-tools] {self.address_string()} {fmt % args}", flush=True)


def main() -> None:
    print(
        f"[media-tools] listening on {LISTEN[0]}:{LISTEN[1]} "
        f"(input={INPUT_DIR}, output={OUTPUT_DIR}, templates={TEMPLATES_DIR})",
        flush=True,
    )
    ThreadingHTTPServer(LISTEN, Handler).serve_forever()


if __name__ == "__main__":
    main()
