# ── Stage 1: Build ────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Copy module manifests first so the dependency download layer is cached
# independently of source changes — only re-runs when go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code and build
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o k8s-dashboard ./cmd/server

# ── Stage 2: Runtime ──────────────────────────────────────────────────────
FROM alpine:3.21

RUN addgroup -S -g 1001 appgroup && adduser -S -u 1001 -G appgroup appuser

WORKDIR /app

COPY --from=builder /app/k8s-dashboard .
COPY --from=builder /app/config/config.yaml ./config/config.yaml
COPY --from=builder /app/web/ ./web/

RUN chown -R appuser:appgroup /app

USER appuser

EXPOSE 8090

# MOCK_MODE=true → use fake data (no kubeconfig needed)
ENV MOCK_MODE=true

ENTRYPOINT sh -c 'FLAGS=""; [ "$MOCK_MODE" = "true" ] && FLAGS="-mock"; exec /app/k8s-dashboard $FLAGS -config /app/config/config.yaml'
