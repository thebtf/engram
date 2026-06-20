# syntax=docker/dockerfile:1

# --- Dashboard build stage ---
FROM node:22-bookworm-slim AS dashboard

WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ .
RUN npm run build

# --- Operator console build stage ---
FROM node:22-bookworm-slim AS operator-console-build

WORKDIR /workspace/apps/operator-console
COPY apps/operator-console/package.json apps/operator-console/package-lock.json ./
RUN npm ci
COPY apps/operator-console/ ./
COPY design/operator-console/contracts /workspace/design/operator-console/contracts
RUN npm run parity && npm run build

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

# Build server binary
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-X main.Version=${VERSION} -s -w" -o /out/engram-server ./cmd/engram-server

# Build client-side binaries: engram local proxy
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-X main.Version=${VERSION} -X github.com/thebtf/engram/internal/version.Daemon=${VERSION} -s -w" -o /out/engram ./cmd/engram
# --- Server image ---
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

# --- Operator console image ---
FROM node:22-bookworm-slim AS operator-console

WORKDIR /app

COPY --from=operator-console-build /workspace/apps/operator-console/.output ./.output

ENV NITRO_HOST=0.0.0.0
ENV NITRO_PORT=3000
ENV NUXT_PUBLIC_API_BASE=/api

EXPOSE 3000

ENTRYPOINT ["node", ".output/server/index.mjs"]
