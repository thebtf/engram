# System overview

Engram separates the agent-host protocol from the shared durable service. The
agent host runs `engram` locally as an MCP stdio process. That daemon calls the
shared server over gRPC; lifecycle hooks call the server's REST API separately.

## Workstation boundary

`ENGRAM_URL` is a server origin such as `http://host:37777`, not an HTTP MCP
endpoint. The plugin launcher's ordinary MCP startup rejects a missing server
URL; it does not reject an empty token. Tokenless transport is supported for a
server configured with authentication disabled. When server authentication is
enabled, the workstation needs an appropriate keycard through `ENGRAM_TOKEN`
(or the launcher's `api_token` configuration); an empty token does not bypass
server authorization.

- Agent-host transport is local stdio/process. Configured discovery returns client-known schemas without waiting for the backend, marking remote support and authorization as unverified.
- Daemon tool execution uses gRPC; logical-project recovery also uses authorized REST readers.
- Hook failures are REST-client problems.
- Browser failures are console/static/proxy problems.

The local catalog is available independently of backend readiness; its schemas do not establish remote support or authorization. Authenticated backend discovery and refresh run asynchronously, with `tools/list_changed` notifications when the catalog refreshes. A backend-dependent original request can wait for authenticated capability discovery within its existing context and budget, then is dispatched at most once. `CAPABILITY_NOT_SENT` means the request was not dispatched; `OUTCOME_UNKNOWN` means it was dispatched but its outcome is unknown, and it must not be automatically replayed.

On Windows, only genuine absence of both daemon marker files permits fail-closed attestation and joining of the same image, or identification of the exact pinned v7.0.19 image for the existing graceful replacement path. Attestation binds process and image handles, mapping, pipe peer, PID and the full generation; unsupported or untrusted cases fail closed. Identifying an old image does not authorize executing its binary. Other platforms retain the existing marker flow.

These describe source behavior, not proof of an installed or released client. The observed installed failure was a marker reconciliation timeout; the cause of earlier daemon EOF remains unproved.

**Accepted requirement — local MCP startup and recovery (not an implementation claim).**
Main session `01a15ebb-8730-7265-b641-a99d23c8e947` relayed the user's wording in native message `aff74417-48b7-494a-8720-71ed92b01610`; the original native USER message ID is unknown:

> «если сервер живой - клиент должен соединяться. если проблемы - клиент должен их решить. если сервер отвалился - клиент пересоединяется. и во всех этих случаях сессия обязана соединяться с клиентским mcp.»

A valid configured client must establish local MCP and expose the complete supported tool and recovery surface during backend unavailability or reconnection. Report genuine backend errors; never substitute fake empty results or present four Loom tools as the complete catalog. Recover transient faults in the existing owner with bounded backoff and cancellation. Never fabricate identity or credentials, or automatically replay mutations with unknown outcomes.

The workstation must never receive `ENGRAM_AUTH_ADMIN_TOKEN`. Workstation
keycards are distinct database-backed credentials; authenticated admin API calls
can create and revoke them. The public browser issuance journey is not yet
accepted, so the README does not turn a route label into a setup instruction.

Current source can recover a strict historical marker through an existing
verified project mapping, or expose caller-bound `project_context` choices and
selection before canonical project resolution. This does not migrate project
data, change a host session or grant access. The recovery readers retain the
configured TLS trust policy; source implementation is not installed-host proof.
See [recovery contracts](API_CONTRACTS.md#supported-recovery-actions).

## Server boundary

The server binds one worker listener and cmux sends gRPC HTTP/2 traffic to the
gRPC server while the remaining HTTP traffic reaches the REST router and browser
console handler. `/health` distinguishes a reachable process from the service's
readiness endpoint, `/api/ready`.

The Docker server image embeds the Vue 3/Vite console built from
`apps/operator-console/`. `serveIndex` can instead proxy browser traffic to
`ENGRAM_OPERATOR_CONSOLE_URL`. Compose also starts the dependency-free Node
console host on port 3000 by default; it forwards `/api/*` to
`ENGRAM_OPERATOR_API_TARGET=http://server:37777`. None is an HTTP MCP transport.

The separate `apps/operator-web/` application uses Nuxt and is another API
consumer, not the promoted Docker frontend. `make dashboard` still builds the
legacy `ui/` bundle, and `make build` invokes it. Docker and Makefile therefore
do not currently select the same UI source.

`GET /health` is content-negotiated: an explicit HTML Accept header receives the
SPA, while a machine request receives health JSON. `/api/health` is the machine
API. The old unconditional embedded `/health` collision description in the
retained ledger does not describe this handler. See [API contracts](API_CONTRACTS.md)
for replacement-UI boundaries and the maintained HTTP/MCP/gRPC owners.

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
