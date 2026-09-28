# ScarliHQ v17.9.7 — Multi-stage Dockerfile (real dashboard)
# v17.9.7 FIXES:
#   P0: Reordered COPY . . BEFORE go mod download (was: COPY . . overwrote go.sum → build fail)
#   P0: Runtime = nvidia/cuda base (was distroless — no nvidia-smi/docker → APIs returned empty)
#   P0: docker-cli installed so /api/containers works via docker.sock
#
# Builder: golang:1.23-alpine (CGO disabled for static binary)
# Runtime: nvidia/cuda:12.8.0-base-ubuntu22.04 + docker.io + ca-certificates
#   - nvidia-smi present → /api/gpu works (with nvidia device/runtime)
#   - docker CLI installed → /api/containers works (docker.sock mounted in compose)
#   - scarlix-mode mounted in compose → /api/mode set works
FROM golang:1.23-alpine AS builder

WORKDIR /build
# Copy entire source first (go.sum may be empty — go mod download populates it)
COPY . .
# GOFLAGS=-mod=mod lets go mod download + go build update go.sum if needed
ENV GOFLAGS=-mod=mod
RUN go mod download
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /scarlihq ./cmd/scarlihq

# --- Runtime (has nvidia-smi + can install docker-cli) ---
FROM nvidia/cuda:12.8.0-base-ubuntu22.04

# docker.io = docker CLI (talks to host daemon via mounted docker.sock)
# docker-compose-plugin = `docker compose` v2 (scarlix-mode uses it)
# ca-certificates = TLS for any outbound HTTPS (Telegram, etc.)
# nvidia-smi already present in base image
RUN apt-get update && apt-get install -y --no-install-recommends \
      docker.io \
      docker-compose-plugin \
      ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /scarlihq /scarlihq
COPY --from=builder /build/cmd/scarlihq/frontend/dist /frontend/dist

EXPOSE 8090 8091

# Run as root: needs docker.sock access + scarlix-mode exec (scarlix-mode uses docker + systemctl)
ENTRYPOINT ["/scarlihq"]
