# Architecture rationale

## Local MCP, shared durable service

The agent-facing protocol is local stdio. Keeping the host-to-daemon boundary
local and the daemon-to-server boundary gRPC avoids inventing an HTTP MCP route
on the server. Lifecycle hooks use REST because they are not MCP clients.

## One listener, more than one deployment form

cmux splits gRPC HTTP/2 from other HTTP traffic on the worker listener. It does
not turn a browser URL into a gRPC address or an MCP URL. The server can serve an
embedded generated Vue console or proxy browser requests to
`ENGRAM_OPERATOR_CONSOLE_URL`; the Compose stack also exposes the Vue application
as a dedicated service. Each form consumes the server's HTTP API.

## Separate credentials

The server-host operator credential and workstation keycard are different
credentials. The shared validator recognizes the configured operator key and
database-backed client tokens, while token creation/revocation routes require
administrative authorization. This lets an operator revoke a workstation without
putting the operator secret in a local agent configuration.

## Assistant identity and record context

[Decision 21](../DECISIONS.md) separates a durable Assistant's canonical agent
principal from replaceable keycards and record affiliation. The design uses a
small Assistant registry keyed by that same principal, not a second ID. Each
memory or versioned note has one assistant, project, or global context; global
needs no project UUID. Authenticated authorship, existing memory privacy
ownership, and visibility remain independent. Notes keep their existing access
rules; no document-private policy is added. Affiliation neither impersonates an
author nor changes visibility defaults.

A trusted registered assistant can supply default self when no explicit context
or real project is selected. Explicit context wins; explicit project remains
project; failed project resolution never silently falls back. No mandatory
assistant-mode startup setting is added.

This is a design contract, not current project-free runtime support. Its private
record is `specs/assistant-memory-context/architecture.md` in agent-state.
Execution enters the existing Spec Kit feature path and extends current
memory/document owners and shared retrieval. Preserve project behavior,
historical privacy, and the stdio/gRPC versus REST boundary; do not add fake
projects or a new IAM/ACL layer.

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

The supported Go logical-project resolver pins one portable UUID in
`.engram-project`, shared locally through the real Git common directory. Current
native, hook, OMP and OpenClaw consumers pass that UUID in `Project` or
`project_id`. Normal tool and context requests do not register or resolve V1/V2/V3
descriptors, derive an identity from authentication, or emit identity-comparison
observations. The retired protobuf fields reserve both their numbers and names;
the registration and ambient-relay RPC variants are removed.

Project identity is routing metadata, not authentication. Existing bearer,
actor, principal-visibility and session checks still govern data access. UCI
keeps independent Source, Checkout and pinned View contracts; its installation
client-instance metadata is not a project selector.

HostAdvisor task authority still consumes `ProjectIdentityV3` as evidence through
its existing authenticated task-memory boundary. Its read-filter authorization,
anchor-binding lookup, typed refusals and audit remain separate from normal
project routing, without the retired comparison-writer bridge. This retained
consumer does not restore registration or descriptor selection on ordinary RPCs.

Historical project rows, migration metadata, comparison records, receipt and
snapshot readers, and cryptographic provenance remain intact. Compatibility for
this single-install cutover preserves useful data, not executable old clients.
The opt-in AR2 five-callable comparison runtime and its obsolete OpenClaw driver
are retired, not replaced by a four-callable claim. Independent snapshot
preflight, daemon parsing and historical five-callable receipt formats remain.
