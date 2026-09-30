# SCARLIX OS v18.8 — MusicGen (Creative mode, GPU 0)
# v18.8 P1: Pin ALL pip package versions (was: floating → non-reproducible builds:
#   git commit + identical install → audiocraft A + torch A today, but
#   audiocraft B + torch B next month without any SCARLIX change).
#   Versions verified via PyPI as of 2025-09-30. Update deliberately when upgrading.
FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg && rm -rf /var/lib/apt/lists/*
# v18.8 P1: Exact pins for reproducibility (was: `pip install audiocraft fastapi
#   uvicorn torchaudio` with no versions)
RUN pip install --no-cache-dir \
    audiocraft==0.0.2 \
    fastapi==0.115.0 \
    uvicorn==0.30.6 \
    torchaudio==2.4.0
COPY server.py /app/server.py
WORKDIR /app
EXPOSE 7862
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "7862"]
