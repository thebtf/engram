# syntax=docker/dockerfile:1
FROM golang:1.24-bookworm AS builder

WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git build-essential \
    && rm -rf /var/lib/apt/lists/*

ENV CGO_ENABLED=1
ENV GOFLAGS=""

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build server-side binaries: worker + MCP SSE server
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-s -w" -o /out/worker ./cmd/worker
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-s -w" -o /out/mcp-sse ./cmd/mcp-sse

# Build client-side binaries: MCP stdio proxy + hooks
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-s -w" -o /out/mcp-stdio-proxy ./cmd/mcp-stdio-proxy
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-s -w" -o /out/mcp-server ./cmd/mcp
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-s -w" -o /out/hooks/session-start ./cmd/hooks/session-start
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-s -w" -o /out/hooks/user-prompt ./cmd/hooks/user-prompt
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-s -w" -o /out/hooks/post-tool-use ./cmd/hooks/post-tool-use
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-s -w" -o /out/hooks/subagent-stop ./cmd/hooks/subagent-stop
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-s -w" -o /out/hooks/stop ./cmd/hooks/stop
RUN CGO_ENABLED=1 go build -tags fts5 -ldflags "-s -w" -o /out/hooks/statusline ./cmd/hooks/statusline

# --- Server image: worker + MCP SSE ---
FROM debian:bookworm-slim AS server

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/worker /usr/local/bin/worker
COPY --from=builder /out/mcp-sse /usr/local/bin/mcp-sse
COPY deploy/entrypoint-server.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV CLAUDE_MNEMONIC_WORKER_HOST=0.0.0.0
ENV CLAUDE_MNEMONIC_WORKER_PORT=37777
ENV CLAUDE_MNEMONIC_MCP_SSE_PORT=37778

EXPOSE 37777
EXPOSE 37778

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:37777/health || exit 1

# Default: run both worker + mcp-sse via combined entrypoint.
# Override CMD to run a single service: CMD ["worker"] or CMD ["mcp-sse"]
ENTRYPOINT ["entrypoint.sh"]

# --- Client image: hooks + MCP proxy (for extracting binaries) ---
FROM debian:bookworm-slim AS client

WORKDIR /app

COPY --from=builder /out/mcp-stdio-proxy /app/mcp-stdio-proxy
COPY --from=builder /out/mcp-server /app/mcp-server
COPY --from=builder /out/hooks/ /app/hooks/
COPY plugin/hooks/hooks.json /app/hooks/hooks.json
COPY plugin/commands/ /app/commands/
COPY plugin/.claude-plugin/ /app/.claude-plugin/

ENTRYPOINT ["/bin/bash"]
