# Runtime components

## `engram-server`

The long-running server owns PostgreSQL-backed stores, REST handlers, gRPC
services, authentication, and browser-console delivery. It binds the configured
worker port and uses cmux to route gRPC separately from other HTTP traffic.
Source: `cmd/engram-server/main.go`, `internal/worker/service.go`.

## `engram`

The local executable is the MCP stdio daemon launched by an agent host. It uses
the workstation `ENGRAM_URL` and `ENGRAM_TOKEN` to reach the server over gRPC.
Its current fatal diagnostic still names the old `/tokens` browser route; that is
a source contradiction, not public setup guidance.
Source: `cmd/engram/main.go`, `internal/config/envnames.go`.

## Lifecycle hooks

Hooks send lifecycle events to REST handlers and are independent of the local
stdio daemon. Diagnose hooks as HTTP clients, not as evidence that server HTTP
MCP exists.
Source: `plugin/engram/hooks/`, `internal/worker/service.go`.

## Operator console

The promoted console source is the Nuxt application in `apps/operator-console/`.
The server embeds its generated static output by default and can proxy it to an
explicit external upstream. The Compose stack also starts that Nuxt application
as an independent service. The current page-file inventory is in
[current-surface.json](current-surface.json).

Nuxt's file-based routing maps its page components to routes; use the inventory
as a source-derived route map, not as proof that every administrative workflow
is accepted for public documentation.

## Feature gates

| Surface | Classification | Activation | Evidence |
| --- | --- | --- |
| Graph | flag-gated | `ENGRAM_GRAPH_ENABLED` | `internal/worker/handlers_graph.go` |
| Candidate queue | flag-gated | `ENGRAM_VNEXT_F_ENABLED` | `internal/worker/handlers_candidates.go` |
| Code intelligence | flag-gated | `ENGRAM_CODE_INTEL_ENABLED` | `cmd/engram/wiring.go`, `internal/mcp/tools_code_intel.go` |
| Reranking | optional-runtime | `ENGRAM_RERANK_URL` | `internal/worker/service.go`, `internal/mcp/tools_memory.go` |

The ledger contains the exact repository-relative evidence for these entries.
