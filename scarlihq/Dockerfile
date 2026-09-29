# ScarliHQ v18.5.2 — Multi-stage Dockerfile (Secure Host-Bridge architecture)
# v18.5 FIX: Dockerfile header comments updated (were stale v17.9.9 references)
# v18.0.0 ARCHITECTURE: ScarliHQ has NO docker.sock, NO nvidia runtime, NO scarlix-mode.
#   Reads /var/lib/scarlix/host-status.json, writes /var/lib/scarlix/bridge-input/desired-mode.
#   Runtime = alpine (~20MB).
# v17.9.9: nonroot user created (alpine adduser -D -u 65532), version via -ldflags
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Standard Go Docker layer-cache pattern:
COPY go.mod ./
ENV GOFLAGS=-mod=mod
RUN go mod download

COPY . .

# v18.0.0 P2: inject version via -ldflags -X main.Version (single source: VERSION file)
# CI passes --build-arg SCARLIX_VERSION=$(cat VERSION); default matches VERSION file
ARG SCARLIX_VERSION=18.6
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=${SCARLIX_VERSION}" -o /scarlihq ./cmd/scarlihq

# --- Runtime (minimal — no docker, no nvidia, no scarlix-mode) ---
FROM alpine:3.20

# ca-certificates = TLS, tzdata = correct log timestamps
RUN apk add --no-cache ca-certificates tzdata

# v17.9.9 P0: Create nonroot user (alpine has no nonroot user by default — was crash)
# UID 65532 matches distroless convention; install.sh chowns bridge-input/ to this UID.
RUN adduser -D -u 65532 -H -g "" nonroot

COPY --from=builder /scarlihq /scarlihq
COPY --from=builder /build/cmd/scarlihq/frontend/dist /frontend/dist

EXPOSE 8090

USER nonroot
ENTRYPOINT ["/scarlihq"]
