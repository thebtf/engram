# System overview

Engram separates the agent-host protocol from the shared durable service. The
agent host runs `engram` locally as an MCP stdio process. That daemon calls the
shared server over gRPC; lifecycle hooks call the server's REST API separately.

## Workstation boundary

`ENGRAM_URL` is a server origin such as `http://host:37777`. It is not an HTTP
MCP endpoint. The local daemon receives `ENGRAM_URL` and its own
`ENGRAM_TOKEN`; it fails at startup when a URL is configured without that token.

- Agent-host discovery problems are local stdio/process problems.
- Daemon failures are on the gRPC path.
- Hook failures are REST-client problems.
- Browser failures are console/static/proxy problems.

The workstation must never receive `ENGRAM_AUTH_ADMIN_TOKEN`. Workstation
keycards are distinct database-backed credentials; authenticated admin API calls
can create and revoke them. The public browser issuance journey is not yet
accepted, so the README does not turn a route label into a setup instruction.

## Server boundary

The server binds one worker listener and cmux sends gRPC HTTP/2 traffic to the
gRPC server while the remaining HTTP traffic reaches the REST router and browser
console handler. `/health` distinguishes a reachable process from the service's
readiness endpoint, `/api/ready`.

The server image includes a generated Nuxt static bundle. `serveIndex` can instead
proxy browser traffic to `ENGRAM_OPERATOR_CONSOLE_URL`. The supplied Compose file
also runs the promoted Nuxt console as a separate service; it uses
`NUXT_OPERATOR_API_TARGET=http://server:37777` and exposes port 3000 by default.
These are deployment forms of the same browser application, not an HTTP MCP
transport.

The embedded-server deployment form has one known route collision: the machine
`/health` handler registers ahead of the SPA catch-all, so a direct browser load
of `/health` at the server's own origin returns machine health JSON instead of
`pages/health.vue`. The standalone Nuxt console (port 3000) has no such
collision. See [current-surface.json](current-surface.json) for the full
per-deployment-form ledger entry; do not teach the embedded `/health` page as
working until this is repaired.

## Persistence proof

Store a non-secret marker through the host-discovered memory tool, then start a
new daemon or agent session and recall the marker. Reading the result in the
Memory console route, when available, adds a browser check. This crosses the
local stdio, gRPC, persistence, and retrieval boundaries; a healthy HTTP process
does not prove those paths.

## Current exclusions

The server's current setup paths do not include HTTP MCP, SSE/Streamable-HTTP
MCP, or a stdio-to-SSE bridge. Stale comments and historical test strings are not
route registrations. The current source also contains an optional reranker behind
`ENGRAM_RERANK_URL`; it is not a default core capability and no document should
infer that a deployment has enabled it.

See [current-surface.json](current-surface.json) for the source paths backing
these statements.
