# SCARLIX OS v18.8.2 — MusicGen (Creative mode, GPU 0)
# v18.8 P1: Pin ALL pip package versions (was: floating → non-reproducible builds)
# v18.8.2 P2: Use requirements.lock for transitive deps (was: only direct deps pinned
#   → dependency tree could still drift. Now: major transitive deps also pinned.)
FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg && rm -rf /var/lib/apt/lists/*
# v18.8.2 P2: Install from lock file (pinned direct + major transitive deps)
COPY requirements.lock /app/requirements.lock
RUN pip install --no-cache-dir -r /app/requirements.lock
COPY server.py /app/server.py
WORKDIR /app
EXPOSE 7862
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "7862"]
