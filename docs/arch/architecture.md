# Architecture rationale

## Local MCP, shared durable service

The agent-facing protocol is local stdio. Keeping the host-to-daemon boundary
local and the daemon-to-server boundary gRPC avoids inventing an HTTP MCP route
on the server. Lifecycle hooks use REST because they are not MCP clients.

## One listener, more than one deployment form

cmux splits gRPC HTTP/2 from other HTTP traffic on the worker listener. It does
not turn a browser URL into a gRPC address or an MCP URL. The server can serve an
embedded generated Nuxt console or proxy browser requests to
`ENGRAM_OPERATOR_CONSOLE_URL`; the Compose stack also exposes the Nuxt application
as a dedicated service. Each form consumes the server's HTTP API.

## Separate credentials

The server-host operator credential and workstation keycard are different
credentials. The shared validator recognizes the configured operator key and
database-backed client tokens, while token creation/revocation routes require
administrative authorization. This lets an operator revoke a workstation without
putting the operator secret in a local agent configuration.

## Feature state must be explicit

Graph, candidate queue, and code intelligence have distinct enablement flags.
They are not public defaults merely because their handlers and pages exist.
The optional reranker is a source-wired recall path activated by
`ENGRAM_RERANK_URL`; it is classified as `optional-runtime`, not as an advertised
cross-encoder feature. This deliberately resolves the conflict between the v5
demolition guidance and the remaining initialized call path: source proves the
optional path, but not a production deployment or a supported public journey.

## Tombstones

Do not resurrect server HTTP MCP, `/sse`, Streamable HTTP MCP, or a stdio-to-SSE
bridge from legacy text. The current listener registers REST and gRPC; the local
`engram` binary supplies the stdio MCP boundary. The machine ledger records the
call-path evidence and is the place to update when source changes.

## Project identity routing

Every project-scoped request resolves a versioned workspace identity to the
canonical project before data access. The local stdio daemon carries the outer
legacy selector plus `ProjectIdentityV2` over gRPC; lifecycle hooks and OpenClaw
use the REST identity-only registration endpoint and wait for `canonical_project`
before issuing context, memory, issue, vault, or session requests. Registration
failure short-circuits the downstream request.

Project identity is routing metadata, not authentication. Supplying a selector,
Git remote/path, or non-Git anchor never grants access; existing bearer and
principal-visibility checks still govern endpoints that require them. The
protobuf field is additive,
so legacy clients may continue only while their selector resolves unambiguously.
Ambiguous legacy selectors fail closed with `PROJECT_IDENTITY_AMBIGUOUS` and the
`send_project_identity_v2` upgrade action. Non-Git workspaces use 16 random bytes
encoded as 32 lowercase hexadecimal characters in `.engram-project-v2.json`,
with sharing enabled only by an explicit opt-in flag.
