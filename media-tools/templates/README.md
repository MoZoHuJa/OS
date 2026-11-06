# Media Tools templates (v19.6.6, KPK-19.6.5-P2-04)

A template is a JSON file naming an ffmpeg (or imagemagick) argument list.
`{input}` and `{output}` are substituted with the sanitized absolute paths
(`/data/input/<name>` → `/data/output/<name>`); the API caller NEVER passes
raw argv — only a template name plus plain file names.

Example `audio-to-mp3.json`:

```json
{
  "tool": "ffmpeg",
  "args": ["-i", "{input}", "-vn", "-c:a", "libmp3lame", "-q:a", "2", "{output}"]
}
```

The volume ships intentionally EMPTY of templates: the operator drops the
family's conversion templates here — the service lists them at
`GET /templates` and refuses any name that is not a file in this directory.
