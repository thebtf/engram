# UCI-1 Data Model

**Status**: Phase 1 planning model. It is a readable entity/ownership map for implementation; the adopted storage, identity, migration, and API contracts remain normative for exact schema and transport details.

## Authority Boundary

- **PostgreSQL 17 is authoritative** for code-context registry records and all durable, rebuildable `ci_*` projections. A source file is still external source material; PostgreSQL records the authorized, observed, versioned facts used to answer a request.
- **The daemon-local SQLite registry is operational only**: approved root and filesystem evidence, common/private Git-dir fingerprints, local instance identity, dirty-set, watcher state, reconciliation checkpoints, and bounded offline recovery metadata. It neither grants access nor answers search/graph queries.
- **The daemon owns local discovery; the server owns authorization and publication**. Parser output is untrusted computational input. The server validates bounds, ownership, relation vocabulary, source/profile membership, content digests, ACL epoch, and lease epoch before persisting it.
- **Existing product records remain separate**. A Space can retain product knowledge; the memory graph/knowledge edges do not become the syntax graph, and legacy `code_chunks` remain `legacy_unscoped` rather than acquiring invented Checkout/View history.

## Canonical Context Entities

| Entity | Identity and key fields | Ownership and relationships | Invariants |
|---|---|---|---|
| **Space** | Server UUID `space_id`; `auth_realm`, mutable `display_name`, lifecycle `state`. | Product grouping owned by the server. Many-to-many with Source through `space_sources`. | It may exist with no Git source. Membership is not a grant and never selects a Checkout/View. |
| **Source** | Server UUID `source_id`; `auth_realm`, `kind` (`git`, `directory`, `document_set`), mutable display metadata, state. | Server-authorized independently addressable code/material body. Has zero or more Spaces, Checkouts, artifacts, profiles, and grants. | Path, remote, branch, label, content hash, and alias are evidence/locators, never `source_id`. A fork is a new Source unless verified registry mapping authorizes otherwise. |
| **Space–Source membership** | `(space_id, source_id)` with display/order metadata. | Server-owned grouping relation. | It does not widen source or private-checkout access. A Source may belong to multiple Spaces. |
| **Checkout** | Server UUID `checkout_id`; `source_id`, `workstation_id`, `owner_principal`, protected `locator_ref`, `kind`, state, `current_view_id`, current lease metadata. | Represents exactly one registered local working copy or explicit `commit_reader`. A working-tree Checkout can have many Views and one current pointer. | Source grant and optional checkout grant are both enforced. A remote client does not receive a raw Windows locator. `commit_reader` is separate, read-only, watcher-free, and not a UCI-1 prerequisite. |
| **Incarnation** | Server UUID `incarnation_id` stored with the Checkout and every View. | Lifecycle identity for one continuous local checkout instance. It is not a user-facing tenant and does not need a second generic registry table. | A verified move may keep it; copy, lost continuity, workstation change, or delete/recreate at the same path issues a new one. Old Views remain historical but cannot be presented as current for the new instance. |
| **Analysis profile** | Server UUID `profile_id`; parser bundle digest, resolver/chunker/ignore/secret policy revisions, build context. | Defines the interpretation environment of a View. | It is a View parameter, not a user identity. Only inputs affecting the relevant product enter each cache key. |
| **View** | Server UUID `view_id`; `(checkout_id, source_id, incarnation_id, generation, profile_id)`, manifest digest, observed Git facts, dirty flag, filesystem sequence/window, coverage, state, publish timestamp. | Immutable observed file-manifest version of one Checkout. Owns temporal memberships and resolved edges by generation. | Unique `(checkout_id, generation)`; exactly one current published pointer per Checkout; a request pins one View per Source. Two dirty worktrees at the same commit have distinct Views. |
| **Client binding** | Transport-session-local binding to a resolved ContextRef; opaque context handle may reference it. | Daemon/session lifecycle, server-validated on every UCI request. | Client B cannot mutate A. A changed CWD requires re-resolution. Missing/ambiguous context fails closed; client input is selector/evidence, not authority. |
| **Typed alias** | `legacy_context_aliases` key: `(auth_realm, legacy_domain, scheme, value, optional_client_namespace)` plus target Space/Source role, mapping state, revision, provenance. | Server-owned compatibility/migration mapping. | States are `resolved`, `ambiguous`, `unmapped`, or `retired`. An alias may assist controlled resolution but cannot select an arbitrary Checkout/View or widen access. |

## Code-Derived Artifacts and Membership

| Entity | Identity and key fields | Relationship | Invariants |
|---|---|---|---|
| **Blob** | `blob_id`; `(source_id, protection_domain, content_digest)` unique; byte length, encoding, storage state, optional safe content. | Immutable source-byte unit shared only inside its permitted Source/protection domain. | Excluded/secret-bearing content may be metadata-only. A blob hash is not a public existence oracle and not a read capability. |
| **Parse artifact** | `artifact_id`; `(source_id, blob_id, language, parser_revision, grammar_digest, extraction_profile_digest)` cache key; status and diagnostics. | One immutable parse/extraction product derives definitions, reference sites, and chunks. | Parser upgrades produce a new artifact even for identical bytes. Status is `complete`, `partial`, `unsupported`, or `excluded`; the latter states are visible in coverage rather than silently omitted. |
| **Definition** | `(artifact_id, local_symbol_key)`; kind, names, signature, byte/line span. | Content-derived declaration attached to one parse artifact. | `entity_key` derives from Source, relative-file identity, artifact, and local symbol key. Line number is a locator, not identity. |
| **Reference site** | `(artifact_id, site_key)`; owner symbol, raw target, relation, syntax span, resolver hints. | Observed source fact; input to resolution. | An unresolved/ambiguous site stays unresolved; it never receives a fabricated target. |
| **Chunk** | `chunk_id`; artifact, optional symbol, kind, ordinal, byte span, text/search digest, `content_tsv`. | Searchable, source-grounded excerpt fragment. | Chunk span lies inside its blob. AST-aware chunks use declaration boundaries; fallback text chunks carry explicit coverage. Presentation truncation never deletes stored source content. |
| **Membership** | `(checkout_id, path_key, valid_from_generation)` with display path, artifact reference, file state/mode, `valid_to_generation`. | Maps one Checkout’s path to the artifact visible in its Views. | Intervals are half-open `[from,to)`; only one current row per Checkout/path; no overlap. This is why shared artifact reuse does not merge two worktrees. |
| **Resolved edge** | `(checkout_id, edge_key, valid_from_generation)` with source/target membership references, relation, evidence kind, resolver revision, evidence, resolution state, `valid_to_generation`. | View-scoped directed multigraph over memberships. | Both endpoints belong to the selected View. Evidence kinds are closed (`EXTRACTED`, `RESOLVED`, `HEURISTIC`, `SEMANTIC`, `UNRESOLVED`). Multiple sites/relations between a pair remain distinct. |

## Semantic and Durable Work Entities

| Entity | Identity and key fields | Ownership | Invariants |
|---|---|---|---|
| **Embedding profile** | `embedding_profile_id`; provider reference, model, dimension, preprocessing revision, relative-path inclusion policy. | Server-owned versioned semantic-space declaration. | Same dimension alone is insufficient for reuse. Credentials remain in existing settings/vault custody, not this row. |
| **Embedding** | `(embedding_profile_id, embedding_input_digest, source_id, protection_domain)` plus vector, completion sequence, status. | Rebuildable projection; chunks join through `ci_chunk_embeddings`. | Input digest includes exact approved preprocessing/input bytes. It never makes stale bytes current and never crosses protection domains. |
| **Chunk–embedding relation** | `(chunk_id, relative_path_fingerprint, embedding_profile_id, embedding_input_digest)`. | Links a current chunk to compatible reusable semantic work. | Path display/projection can change without treating a different source/checkout as the same authorized membership. |
| **Job** | `job_id`; source/optional checkout, job kind, input fingerprint, target generation, state, attempt, retry timestamp, error/counts. | PostgreSQL durable workflow record for initial index, reconcile, parser, embedding, enrichment, GC, or recovery work. | Idempotency key/input fingerprint is required. Timers wake work but never establish its existence. Terminal errors and degraded states survive process restart. |
| **Lease** | The Job/Checkout carries `lease_owner`, expiry, and monotonic `lease_epoch`; the authorization tuple is `(checkout_id, incarnation_id, lease_epoch)`. | Server-issued fence, not a daemon-local mutex. | Only the current epoch may finalize. Lease expiry never makes an old writer valid. Another Checkout does not wait on this lease. |
| **Analysis** | `analysis_id`; pinned `view_id`, kind, algorithm revision, input digest, artifact references, result/state. | Optional rebuildable derived analysis. | It cannot become source/graph authority; UCI-2 analytics do not block UCI-1 publication. |

## Context Resolution and Request Invariant

1. A client presents its transport session plus either no selector, an opaque context handle, or authorized local evidence.
2. The daemon resolves only local checkout evidence from that client’s CWD using read-only, argument-safe Git plumbing and local registry evidence.
3. The server verifies principal/realm, Source grant, Checkout grant, incarnation continuity, alias mapping, and requested View relation; it produces the canonical ContextRef.
4. Only then may exact lookup, FTS, semantic candidate generation, graph traversal, source read, job enqueue, cache lookup, or pagination execute.
5. Search and graph facts returned together share the same pinned View; any ACL epoch change before response causes reauthorization or refusal.

A failed/mismatched/ambiguous resolution produces a closed, non-disclosing result. It does not reveal another source’s path, identifier, count, content, embedding, or edge.

## State Transitions

### Checkout and Incarnation

```text
configured -> registered -> watching <-> catching_up
                            |              |
                            v              v
offline ----------------> reconcile ----> published current View
registered/watching -> unregistered        (historical Views retained by policy)
move with verified continuity -> same incarnation, locator update
copy / recreated path / lost continuity -> new incarnation, old View never current
```

- `offline` means unavailable, not deleted.
- A checkout unregister stops watch and clears the current pointer; it does not delete its Source, Space membership, or product data.
- `commit_reader` has an explicit read-only lifecycle and no watcher/current-working-tree pointer.

### View and Publication

```text
none or published parent
  -> staging(build_id, expected parent, lease epoch)
  -> published(generation + 1, atomic pointer switch)
  -> superseded (newer published View)
  -> retired (retention/explicit policy only)
```

- Staging rows are not queryable.
- Parse/FTS/membership/resolved-edge prerequisites must be complete for the scoped manifest before publication.
- Vector/enrichment work may lag after structural publication, but must be profile/view pinned and reported as coverage/watermark; it cannot surface an old body under the new View.
- Incomplete upload, EOF alone, timeout, failed scan, bad lease, or stale expected parent leaves the prior published View unchanged.

### Job and Lease

```text
queued -> leased/running -> succeeded
                      |-> retry_scheduled -> queued
                      |-> failed_terminal
                      |-> cancelled/obsolete
```

- Replaying the same build/job id after lost ACK returns the durable previous result rather than creating duplicate membership/edge mutations.
- An old epoch finalization returns `LEASE_STALE`; it cannot roll a Checkout backward.
- Overflow, restart, move, Git transition, watcher-registration failure, or missing local state sets `rescan_required`; safe reconciliation reads current bytes rather than replaying every historical event.

## Publication Transaction and Consistency

1. The daemon obtains a server lease for one Checkout/incarnation and builds a candidate manifest from the last published membership plus the dirty delta.
2. It reads files root-relatively with type/size/ignore/secret checks, stat-read-stat/hash protection, and bounded retry for changing files.
3. It stages idempotent artifacts/facts tagged with `build_id`, profile, expected parent, sequence, and digests. Parser facts remain untrusted until server validation.
4. The server verifies authorization, Source/Checkout/incarnation/profile relation, lease epoch, manifest completeness, bounds, and digest consistency.
5. A short transaction locks the Checkout, validates expected parent and current epoch, closes old temporal intervals, inserts the new memberships/edges, creates the published View, and advances the current pointer together.
6. Only after durable acknowledgement may the daemon remove dirty entries through the accepted sequence. Later events remain pending.

This transaction is the only path that makes a new current View visible. A global `DELETE WHERE index_session_id <> ...` is not a UCI publication mechanism.

## Keys, Foreign Keys, and Query Rules

- UUIDs are server-assigned; digests are complete SHA-256 values, never tenant keys.
- A View references exactly one Source, Checkout, Incarnation, and analysis profile. Composite FKs or equivalent checked invariants prevent cross-source joins.
- Unique `(checkout_id, generation)`, exactly one current View pointer, unique blob `(source_id, protection_domain, content_digest)`, and interval no-overlap are required.
- Definitions/reference sites/chunks must reference an existing parse artifact. An edge endpoint must resolve into the query View’s membership.
- B-tree indexes cover checkout/path/generation membership, edges in both directions, views by checkout/generation, and jobs by state/retry. GIN serves FTS; exact names/symbol keys use B-tree. Additional JSONB indexes need acceptance-corpus evidence.
- Vector search first computes the authorized View candidate universe, then exact scoped distance or an ANN strategy proved against that baseline. It never takes global top-N and filters inaccessible results afterward.

## Migration and Rollback Semantics

- Add Space/Source/Checkout/View/alias registry state before UCI projections. The observed migration baseline is 170; UCI allocates forward migrations after it.
- One unambiguous legacy canonical project maps to one Space with preserved legacy routing facts. A verified source binding may be added later. Ambiguity does not manufacture a source or mutate UCI state.
- Existing `code_chunks` and embeddings retain `legacy_unscoped` provenance. They are not assigned an invented checkout, HEAD, View, or privacy relationship.
- Before UCI cutover, disable the new route and retain `ci_*` inert; product-domain records remain untouched. After expand/cutover but before contraction, restore the prior compatibility route while keeping added schema/history.
- A future derived-data contraction may rebuild code projections from authorized sources only after backup/restore, migration receipt, zero-drift observation, compatibility sunset, and rollback evidence. UCI-1 does not own that contraction.

## Retention and Privacy

- Keep the current View, recent generations, short-lived pins, and artifacts reachable from retained Views; never GC an artifact required by a valid historical View.
- A privacy/access revocation is enforced at the query boundary for current and historical Views, pins, pagination, cache lookup, and graph hops.
- Excluded source and detected secrets remain metadata-only by default; raw bodies, local absolute paths, prompt text, tokens, provider keys, and opaque private locators do not enter status, logs, exports, or embedding input.
- A user-facing code URI is a locator of the form `engram://source/<source>/view/<view>/entity/<entity>`; it is not a bearer capability.
