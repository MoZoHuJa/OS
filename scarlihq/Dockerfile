# ScarliHQ v18.5.2 — Multi-stage Dockerfile (Secure Host-Bridge architecture)
# v18.5 FIX: Dockerfile header comments updated (were stale v17.9.9 references)
# v18.0.0 ARCHITECTURE: ScarliHQ has NO docker.sock, NO nvidia runtime, NO scarlix-mode.
#   Reads /var/lib/scarlix/host-status.json, writes /var/lib/scarlix/bridge-input/desired-mode.
#   Runtime = alpine (~20MB).
# v17.9.9: nonroot user created (alpine adduser -D -u 65532), version via -ldflags
# v18.7.4 P1: Also builds scarlix-bridge-reader (host-side atomic file reader).
#   The bridge-reader runs ON THE HOST (not in this container) — extracted from
#   the image by install.sh Phase 4 and copied to /usr/local/bin/scarlix-bridge-reader.
#   Kept in this Dockerfile so the same `docker build` produces both binaries
#   from the same Go toolchain (no need for Go on the host).
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Standard Go Docker layer-cache pattern:
COPY go.mod ./
ENV GOFLAGS=-mod=mod
RUN go mod download

COPY . .

# v18.0.0 P2: inject version via -ldflags -X main.Version (single source: VERSION file)
# CI passes --build-arg SCARLIX_VERSION=$(cat VERSION); default matches VERSION file
# v18.7.8 P1: Bumped default to 18.7.8 (was: 18.7.6 — stale even though install.sh
#   passes --build-arg so runtime was correct. Default matters for manual `docker build`
#   without the arg, and for consistency with VERSION file.)
ARG SCARLIX_VERSION=18.9.4
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=${SCARLIX_VERSION}" -o /scarlihq ./cmd/scarlihq

# v18.7.4 P1: Build the host-side atomic file reader (CGO disabled for static binary).
# This binary is NOT used inside this container — install.sh extracts it from
# the built image (via `docker create` + `docker cp`) and installs it on the host
# at /usr/local/bin/scarlix-bridge-reader so the root host-bridge script can
# call it. Built here so we don't require Go on the host.
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /scarlix-bridge-reader ./cmd/scarlix-bridge-reader

# --- Runtime (minimal — no docker, no nvidia, no scarlix-mode) ---
FROM alpine:3.20

# ca-certificates = TLS, tzdata = correct log timestamps
RUN apk add --no-cache ca-certificates tzdata

# v17.9.9 P0: Create nonroot user (alpine has no nonroot user by default — was crash)
# UID 65532 matches distroless convention; install.sh chowns bridge-input/ to this UID.
RUN adduser -D -u 65532 -H -g "" nonroot

COPY --from=builder /scarlihq /scarlihq
# v18.7.4 P1: bridge-reader installed in /scarlix-bridge-reader inside the image
# so install.sh can `docker create` + `docker cp` it out. Not used by the container
# itself (the container has no need to atomically read its own files).
COPY --from=builder /scarlix-bridge-reader /scarlix-bridge-reader
COPY --from=builder /build/cmd/scarlihq/frontend/dist /frontend/dist

EXPOSE 8090

USER nonroot
ENTRYPOINT ["/scarlihq"]
