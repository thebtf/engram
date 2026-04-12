# syntax=docker/dockerfile:1

# --- Dashboard build stage ---
FROM node:22-bookworm-slim AS dashboard

WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ .
RUN npm run build

# --- Go build stage ---
FROM golang:1.25-bookworm AS builder

WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git build-essential \
    && rm -rf /var/lib/apt/lists/*

ENV CGO_ENABLED=1
ENV GOFLAGS=""

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Copy built dashboard into static directory for go:embed
COPY --from=dashboard /ui/dist/ internal/worker/static/

# Inject version from git tags
ARG VERSION=dev

# Build server binary (worker with integrated MCP SSE)
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-X main.Version=${VERSION} -s -w" -o /out/engram-server ./cmd/worker

# Build client-side binaries: engram local proxy
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-X main.Version=${VERSION} -s -w" -o /out/engram ./cmd/engram
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-X main.Version=${VERSION} -s -w" -o /out/engram-mcp ./cmd/mcp

# --- Server image: worker + MCP SSE ---
FROM debian:bookworm-slim AS server

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/engram-server /usr/local/bin/engram-server

ENV ENGRAM_WORKER_HOST=0.0.0.0
ENV ENGRAM_WORKER_PORT=37777

EXPOSE 37777

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:37777/health || exit 1

ENTRYPOINT ["engram-server"]

# --- Client image: hooks + MCP proxy (for extracting binaries) ---
FROM debian:bookworm-slim AS client

WORKDIR /app

COPY --from=builder /out/engram /app/engram
COPY --from=builder /out/engram-mcp /app/engram-mcp
COPY plugin/engram/hooks/ /app/hooks/
COPY plugin/engram/commands/ /app/commands/
COPY plugin/.claude-plugin/ /app/.claude-plugin/

ENTRYPOINT ["/bin/bash"]
