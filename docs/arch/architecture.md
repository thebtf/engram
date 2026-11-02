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

## External assistant identity and record context

[Decision 21](../DECISIONS.md) uses one canonical principal for a durable external
client, independently of keycard, session, provider, host, or display name.
Engram does not host assistant personalities, conversation runtimes, provider
accounts, or planning loops. A small registry describes external-client identity
using the existing principal; it is not another account system.

A memory or versioned note has one assistant/self, project, or explicit global
owning context. Global requires no fake project UUID and grants no public access.
Authenticated authorship, existing privacy ownership, and visibility remain
separate. Notes keep their existing access rules; no document-private policy is
added. Affiliation does not impersonate an author or change access defaults.

For the ordinary own-card scenario, a trusted registered client with no explicit
context or real project resolves to self. An explicit context wins; a real project
remains project; an invalid explicit project/context returns an error. It must not
silently fall back to self or global. A new session or replacement credential for
the same canonical principal does not create a new owner or require copying cards.
No mandatory assistant-mode startup setting is added.

These are design requirements, not a declaration that project-free runtime is
already shipped. [Feature 009](../../specs/009-working-agent-memory-r1/spec.md)
owns the next ordinary consumer acceptance. Reuse existing auth/keycards,
memory/document owners, and retrieval. The private earlier architecture packet is
historical rationale; current public contracts are sufficient to understand the
product boundary. No personal assistant name or private workflow is normative.

## Task context and consumer adapters

The accepted task-context contract is
[Feature 009](../../specs/009-working-agent-memory-r1/spec.md); implementation and
acceptance state remains explicit. The application workflow composes existing
owners rather than adding another memory engine or restoring retired subsystems.

The host supplies the actual current task and its existing session/binding facts.
Resolve principal, project or self context, and any native UCI Source/Checkout/View
before provider selection. Authorize candidates before ranking, reading bodies,
or graph expansion. Use knowledge providers for memory/notes and UCI for code;
a project UUID is not a code-view selector.

A single bounded assembly selects only useful material: record references and
revisions, relevant source excerpts, symbols, and the relationships needed for
the question. Selection reasons, provenance, freshness, and active retrieval
mode remain available. The adapter delivers the material as evidence at the
ordinary task event. Retrieved text does not create new instruction authority.

Explicit follow-up tools use the same underlying authorities and typed
references. They may expand several selected symbols/records together where the
existing interface permits it. Sufficient initial material should remove
predictable follow-up turns; incomplete or ambiguous material permits targeted
verification.

Each host has an explicit total deadline and context budget, including its actual
UTF-8 byte and token constraints. Do not substitute a character count for a
documented token limit. The existing OMP 500 ms callback is the initial fast-path
constraint. Current-symbol/lexical/graph work should not wait on a remote
embedding provider or trigger full reindexing. Semantic retrieval retains its
separate provider and quality evidence.

No relevant match, unavailable provider, stale view, invalid context, and
partial material are distinct conditions. Technical failure must not masquerade
as a proven absence of knowledge. Normal work continues within the host's
bounded failure policy. Deduplicate unchanged delivery without hiding a real
topic shift.

Any cache of scoped results includes the necessary principal/visibility and
context identities, knowledge revisions, and code View/generation. Credential or
policy changes cannot leave unauthorized cached results eligible. Incremental
updates run through the existing work owner; request-time work is bounded.
Source read from a code result must match its citation/View and content identity.

Native MCP retains stdio -> daemon -> gRPC. Lifecycle hooks use REST. Equivalent
product behavior across hosts comes from the shared application workflow and
accurate adapters, not an HTTP MCP endpoint or an assumption that one host
executes another host's callbacks.

## Knowledge cards and explicit relations

A card is a useful form of an existing authoritative memory or versioned
note/document. Implementation selects the existing owner appropriate to content,
history, and access requirements. Typed result references allow common retrieval;
they do not create two independently editable copies of the body. A derived
note index reads the original owner and is rebuildable.

The initial explicit relation contract is related_to (symmetric conceptual
association) and references (directed reference). Record IDs are stable; references
to a specific internal claim name the relevant revision. Link creation/removal,
deduplication, provenance, and bounded neighbor reads belong to a domain use case
with endpoint authorization. A link changes neither ownership nor visibility.

These user-authored assertions are durable knowledge records. A graph index or
visualization of them remains a projection. They are not code calls/imports, do
not permit writing UCI facts, and do not reinstate retired graph writers or the
manual graph editor. The ordinary client must be able to follow useful links;
an opaque metadata string alone does not fulfill the relation contract.

## Primary data and rebuildable projections

Preserve valuable notes and Vault with useful content, required history and
provenance, stable references, and supported authorized read/recovery behavior.
Do not equate ciphertext counts with working Vault access or memory counts with
retention of a different notes corpus.

Code chunks, embeddings, automatically derived graphs, and caches are
rebuildable products of their authoritative inputs. A new index format does not
justify changing original notes or duplicating editable records. Preservation of
derived representations or obsolete clients is not an end in itself; unrelated
owner data is not deleted under this product amendment.

## Feature state must be explicit

Graph, candidate queue, and code intelligence have distinct enablement flags.
They are not public defaults merely because their handlers and pages exist.
A capability required by an accepted ordinary path must nevertheless be wired
by construction when that path ships; a retained disabled endpoint is not an
implementation of the promised behavior.
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
