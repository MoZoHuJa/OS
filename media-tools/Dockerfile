# SCARLIX OS v19.6.6 — Media Tools (optional stack, loopback-published)
# v19.6.6 (KPK-19.6.5-P2-04): this build context was MISSING — the compose
#   file declared `build: .` while media-tools/ carried no Dockerfile and no
#   build sources, so `docker compose build` could not build the service at
#   all (the independent audit's exact finding). Now the context is real:
#   a pinned base image, pinned alpine packages (ffmpeg/imagemagick/py3),
#   and a small Python-stdlib conversion service that implements the
#   compose contract: port 8200, /data/input + /data/output + /data/templates.
#   The service is loopback-published by the compose file (127.0.0.1:8200)
#   and carries NO network egress — conversions read input, write output,
#   nothing else.
# Image pin discipline: base pinned by tag (python:3.12-alpine3.20); the
#   alpine packages are pinned by the alpine 3.20 repository state at build
#   time — reproducible per base release, digest-pinning is the next
#   stale-tree milestone (SECURITY_MAP §experimental).
FROM python:3.12-alpine3.20

# ffmpeg: the conversion engine; imagemagick: image/thumbnail conversions;
# both from the pinned alpine repo. No package manager at runtime.
RUN apk add --no-cache \
        ffmpeg \
        imagemagick \
        tzdata

# The stdlib service: no pip packages, no framework, no egress. Templates
# are ffmpeg argument lists (JSON) in /data/templates; conversions run
# ffmpeg with {input}/{output} substituted from /data/input → /data/output.
WORKDIR /app
COPY server.py /app/server.py

# Non-root, writable only where the compose volumes mount.
RUN addgroup -S media && adduser -S media -G media \
        && mkdir -p /data/input /data/output /data/templates \
        && chown -R media:media /data /app
USER media

EXPOSE 8200
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
        CMD wget -q -O /dev/null http://127.0.0.1:8200/health || exit 1

CMD ["python3", "/app/server.py"]
