# Runtime components

## `engram-server`

The long-running server owns PostgreSQL-backed stores, REST handlers, gRPC
services, authentication, and browser-console delivery. It binds the configured
worker port and uses cmux to route gRPC separately from other HTTP traffic.
Source: `cmd/engram-server/main.go`, `internal/worker/service.go`.

## `engram`

The local executable is the MCP stdio daemon launched by an agent host. It uses
the workstation `ENGRAM_URL` to reach the server over gRPC. The plugin launcher's
ordinary MCP startup rejects a missing URL, not an empty token. Tokenless
transport is supported for an auth-disabled server; an auth-enabled server
requires an appropriate workstation keycard through `ENGRAM_TOKEN`. Current
daemon help and launcher diagnostics no longer direct users to a `/tokens`
browser page. The promoted console's `/access` administration surface is not an
accepted browser keycard-issuance journey.
Source: `cmd/engram/main.go`, `internal/config/envnames.go`,
[`run-engram.js`](../../plugin/engram/scripts/run-engram.js).

The local [project_context tool](../../internal/handlers/engramcore/project_context.go)
is available before canonical project resolution. Its read-only `choices` and
caller-bound `select` use the native
[recovery resolver](../../internal/projectidentity/logical_recovery.go) and
authorized REST catalog/mapping readers; ordinary tool execution remains gRPC.
Selection persists only a caller-scoped local binding, not a historical marker,
project migration or permission grant. The
[recovery contract](API_CONTRACTS.md#supported-recovery-actions) states trust,
limits and original-operation resumption; this is not installed-client acceptance.

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
