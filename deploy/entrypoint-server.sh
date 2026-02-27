#!/bin/sh
# Combined entrypoint: runs both worker and mcp-sse in one container.
# Worker handles hook events + dashboard; MCP SSE provides nia tools.

set -e

echo "[entrypoint] Starting MCP SSE server on :${CLAUDE_MNEMONIC_MCP_SSE_PORT:-37778}..."
mcp-sse &
MCP_PID=$!

echo "[entrypoint] Starting Worker on :${CLAUDE_MNEMONIC_WORKER_PORT:-37777}..."
exec worker
