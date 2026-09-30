# SCARLIX OS v18.8 — Video (Wan2GP, Creative mode, GPU 0)
# v18.8 P1: Pin Wan2GP git commit (was: `git clone ... .` with no commit/tag/branch
#   → non-reproducible: clone today → version A, clone next month → version B
#   without any SCARLIX change. Build could break when upstream changes.)
#   Commit SHA f3f204e50f6e verified via `git ls-remote` as of 2025-09-30.
#   Update deliberately when upgrading — re-run: git ls-remote https://github.com/deepbeepmeep/Wan2GP.git HEAD
FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends git build-essential ffmpeg && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# v18.8 P1: Shallow clone + checkout specific commit (was: full clone of main)
ARG WAN2GP_COMMIT=f3f204e50f6e
RUN git clone --depth 1 https://github.com/deepbeepmeep/Wan2GP.git . \
    && git fetch --depth 1 origin "$WAN2GP_COMMIT" \
    && git checkout "$WAN2GP_COMMIT" \
    && rm -rf .git
RUN pip install --no-cache-dir -r requirements.txt
EXPOSE 7861
CMD ["python", "app.py", "--listen", "0.0.0.0", "--port", "7861"]
