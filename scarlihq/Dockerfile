# ScarliHQ v17.9.9 — Multi-stage Dockerfile (host-bridge architecture)
# v17.9.9 FIXES:
#   P0: Create nonroot user in alpine (was: USER nonroot:nonroot — alpine has no such user → crash)
#   P2: Version injected via -ldflags -X main.Version (single source: VERSION file)
# v17.9.8 ARCHITECTURE: ScarliHQ has NO docker.sock, NO nvidia runtime, NO scarlix-mode.
#   Reads /var/lib/scarlix/host-status.json, writes /var/lib/scarlix/bridge/desired-mode.
#   Runtime = alpine (~20MB).
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Standard Go Docker layer-cache pattern:
COPY go.mod ./
ENV GOFLAGS=-mod=mod
RUN go mod download

COPY . .

# v17.9.9 P2: inject version from VERSION file via -ldflags (single source of truth)
ARG SCARLIX_VERSION=18.0.0
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=${SCARLIX_VERSION}" -o /scarlihq ./cmd/scarlihq

# --- Runtime (minimal — no docker, no nvidia, no scarlix-mode) ---
FROM alpine:3.20

# ca-certificates = TLS, tzdata = correct log timestamps
RUN apk add --no-cache ca-certificates tzdata

# v17.9.9 P0: Create nonroot user (alpine has no nonroot user by default — was crash)
# UID 65532 matches distroless convention; install.sh chowns bridge/ to this UID.
RUN adduser -D -u 65532 -H -g "" nonroot

COPY --from=builder /scarlihq /scarlihq
COPY --from=builder /build/cmd/scarlihq/frontend/dist /frontend/dist

EXPOSE 8090

USER nonroot
ENTRYPOINT ["/scarlihq"]
