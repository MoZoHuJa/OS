FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends git build-essential ffmpeg && rm -rf /var/lib/apt/lists/*
WORKDIR /app
RUN git clone https://github.com/deepbeepmeep/Wan2GP.git . && pip install --no-cache-dir -r requirements.txt
EXPOSE 7861
CMD ["python", "app.py", "--listen", "0.0.0.0", "--port", "7861"]
