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

The promoted console is the Vue 3/Vite application in `apps/operator-console/`,
not Nuxt. `Dockerfile` builds it, embeds `.output/public/` in the Go server and
also packages the dependency-free Node host for the separate Compose service.
The host forwards `/api/*` to `ENGRAM_OPERATOR_API_TARGET`; Go remains the owner
of durable state and authorization. Its `.output/` and `/_nuxt/` names are output
compatibility paths, not evidence of a Nuxt runtime.

`apps/operator-web/` is a separate Nuxt application and additional HTTP consumer;
it is not the source selected by the released Docker/Compose recipe. The legacy
`make dashboard` still builds `ui/` and replaces the embedded static directory;
`make build` calls that target. This build split exists and is not repaired here.
See [API contracts](API_CONTRACTS.md#replacement-ui-and-deployment-boundary) for
base URLs, cookie forwarding and the exact client/Go ownership boundary. The
retained [surface ledger](current-surface.json) is not deployment acceptance.

## Feature gates

| Surface | Classification | Activation | Evidence |
| --- | --- | --- |
| Graph | ordinary retained reader | wired graph store | `internal/worker/handlers_graph.go` |
| Candidate queue | ordinary v7 runtime | initialized candidate store | `internal/worker/handlers_candidates.go` |
| Code intelligence | flag-gated | `ENGRAM_CODE_INTEL_ENABLED` | `cmd/engram/wiring.go`, `internal/mcp/tools_code_intel.go` |
| Reranking | optional-runtime | `ENGRAM_RERANK_URL` | `internal/worker/service.go`, `internal/mcp/tools_memory.go` |

The ledger contains the exact repository-relative evidence for these entries.
