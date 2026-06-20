# syntax=docker/dockerfile:1

# --- Legacy dashboard build stage ---
FROM node:22-bookworm-slim AS dashboard

WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ .
RUN npm run build

# --- Operator web build stage ---
FROM node:22-bookworm-slim AS operator-web-builder

WORKDIR /operator-web
COPY apps/operator-web/package.json apps/operator-web/package-lock.json ./
RUN npm ci
COPY apps/operator-web/ .
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

# --- Operator web image ---
FROM node:22-bookworm-slim AS operator-web

WORKDIR /app

ENV NODE_ENV=production
ENV NITRO_PORT=3000
ENV NITRO_HOST=0.0.0.0
ENV NUXT_PUBLIC_API_BASE=/api
ENV NUXT_ENGRAM_API_TARGET=http://server:37777

COPY --from=operator-web-builder /operator-web/.output/ ./.output/

EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:3000/login || exit 1

ENTRYPOINT ["node", ".output/server/index.mjs"]
