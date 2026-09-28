# ScarliHQ v17.9.8 — Multi-stage Dockerfile (host-bridge architecture)
# v17.9.8 ARCHITECTURE: ScarliHQ has NO docker.sock, NO nvidia runtime, NO scarlix-mode.
#   It reads /var/lib/scarlix/host-status.json (written by scarlix-host-bridge timer)
#   and writes /var/lib/scarlix/desired-mode (applied by same timer).
#   Runtime = alpine (~15MB) instead of nvidia/cuda + docker.io (~2.5GB).
#
# v17.9.8 FIXES:
#   P0: Build order = COPY go.mod → go mod download → COPY . . → go build
#       (was: COPY . . first → go mod download overwrote go.sum → build fragile)
#   P0: Runtime slimmed (was nvidia/cuda + docker.io = privilege escalation risk)
#   P0: No host script mount (was: scarlix-mode:ro — needed yq/sudo/systemctl in container)
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Standard Go Docker layer-cache pattern:
# 1. Copy ONLY go.mod (go.sum may not exist yet on fresh checkout — go mod download creates it)
COPY go.mod ./
# 2. Download deps (populates go.sum if missing). -mod=mod allows go.sum generation.
ENV GOFLAGS=-mod=mod
RUN go mod download

# 3. NOW copy full source (does NOT clobber go.sum — it's already correct from step 2)
COPY . .

# 4. Build static binary (CGO disabled → static → runs on distroless/alpine)
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /scarlihq ./cmd/scarlihq

# --- Runtime (minimal — no docker, no nvidia, no scarlix-mode) ---
FROM alpine:3.20

# ca-certificates = TLS for outbound HTTPS (none currently, but keep for future)
# tzdata = correct timestamps in logs
RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /scarlihq /scarlihq
COPY --from=builder /build/cmd/scarlihq/frontend/dist /frontend/dist

EXPOSE 8090

# Run as nonroot (container only reads files + writes desired-mode — no root needed)
USER nonroot:nonroot
ENTRYPOINT ["/scarlihq"]
