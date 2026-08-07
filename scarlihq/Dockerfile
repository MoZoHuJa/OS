# ScarliHQ v12 — Multi-stage Dockerfile
FROM golang:1.23-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY cmd/scarlihq/frontend/dist/index.html cmd/scarlihq/frontend/dist/index.html
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /scarlihq ./cmd/scarlihq

# --- Runtime ---
FROM gcr.io/distroless/static-debian12

COPY --from=builder /scarlihq /scarlihq
COPY --from=builder /build/cmd/scarlihq/frontend/dist /frontend/dist

EXPOSE 8090 8091

USER nonroot:nonroot
ENTRYPOINT ["/scarlihq"]
