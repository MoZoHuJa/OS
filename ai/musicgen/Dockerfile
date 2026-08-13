FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir audiocraft fastapi uvicorn torchaudio
COPY server.py /app/server.py
WORKDIR /app
EXPOSE 7862
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "7862"]
