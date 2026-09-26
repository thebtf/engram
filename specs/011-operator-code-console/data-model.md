# Feature 011 D-A Data and Authority Model

**Status**: D-A planning model. It names ownership and invariants only; implementation rereads the exact candidate and allocates any additive migration in the single registry owner’s integration step. Existing UCI Source, Checkout, immutable View, exposure, and publication lifecycle remain normative.

## Authority model

- PostgreSQL/pgvector remains authoritative storage. UCI remains authoritative for Source, Checkout, immutable View, code query/structure/relations/source descriptors, exposure evidence, and View publication.
- Repository, Working copy, and Indexed snapshot are safe human-facing presentation labels. They are never identity keys, ACLs, locator disclosure, or substitutes for Source/Checkout/View.
- The authenticated BrowserSubject, exact BrowserReadGrant, server-issued tab binding, and current document proof remain separate constraints. A label, branch, path, administrator role, Space, browser tab, or legacy project is not authority.
- Historical graph/book records retain their existing owner and meaning. A UCI relation, a historical knowledge edge, and a semantic similarity result must remain distinguishable.

## Entities and contracts

| Entity / contract | Identity and fields | Owner and relationships | Invariants |
|---|---|---|---|
| BrowserSubject | Canonical enabled persistent human identity and realm | Auth/grant owner; attached to an authenticated browser session | Not derived from role, label, path, branch, master key, keycard, disabled-auth identity, or tab data. |
| BrowserReadGrant | Opaque grant reference; realm; BrowserSubject; exact Source/Checkout; `active|revoked|expired`; issuer; expiry; audit timestamps | `CodeGrantApplication`; references existing UCI Source/Checkout | Exact tuple only. Issue/revoke and audit commit together. It grants code read, never index ownership, source registration, or View publication. |
| BrowserTabBinding | Opaque binding ID; session; proof/resume/token digests; lease; optional pinned ContextRef | Browser Binding Application owner | Existing binding transition table remains normative. Binding identifies a browser document; it does not replace a grant or ContextRef check. |
| WorkspaceCatalogChoice | Opaque catalog selection reference; readable repository/working-copy/snapshot labels; branch/device/location display; snapshot revision/time; availability summary | Browser onboarding/catalog owner, after grant filtering | Labels are validated display metadata, not raw locators or authority. Existing unnamed checkouts stay unnamed rather than receiving a fabricated identity. |
| ContextRef | Existing Source, Checkout, View, analysis-profile, and generation tuple | UCI owner | Search, structure, relation, source, continuation, and released index result use the same selected immutable View. |
| WorkspaceStructurePage | Current binding proof; exact ContextRef; normalized relative prefix; opaque continuation; bounded limit; display paths/entity references; coverage/limitation state | UCI read-model owner | No local disk fallback, raw locator, fake empty search, or inferred global total. Continuation is subject/binding/grant/View scoped. |
| RelationEvidenceReference | Released relation type/direction; evidence kind; entity/source descriptor; supported span/digest; ContextRef | UCI read-model and release owners | Opens only through the existing authorized source-descriptor/read boundary in the same View. Entity-level evidence is labeled as such, never called a precise reference-site span. |
| HistoricalBookJob | Existing job ID, nonterminal/terminal status, source-book provenance, timestamps, error | Book retirement owner; existing versioned-document store | Old plaintext is not replayable from the row. Residual nonterminal jobs can transition idempotently to existing `failed` with an interrupted-by-retirement reason; document rows/provenance remain. |
| Historical graph records | Existing `knowledge_nodes`/`knowledge_edges` and documented readers | Historical graph owner/retained readers | Manual writer admission retires; retained reads and optional retrieval expansion are changed only through the approved consumer map. They are not UCI code graph facts. |

## State transitions

```text
authenticated BrowserSubject + active exact BrowserReadGrant
  -> server-filtered WorkspaceCatalogChoice
  -> explicit ContextRef pin on one BrowserTabBinding
  -> selected Indexed snapshot status / structure / search / relation / source evidence
  -> release reauthorization before contextual serialization

selected View + newer publication -> explicit newer-snapshot candidate
  -> operator switch -> new selected View

BrowserReadGrant(active) -> revoked|expired -> non-disclosing denial
BrowserTabBinding session|binding expiry -> pin destroyed -> explicit authorized selection
```

```text
old plaintext intake quiesced
  -> old writer instances stopped
  -> pending|processing HistoricalBookJob
  -> failed(interrupted-by-retirement, idempotent)
  -> retained historical documents / provenance remain readable
```

## Privacy, migration, and rollback

- Catalog, continuation, binding, grant, and status records exclude source bodies, raw query text, absolute checkout locators, credentials, and unauthorized IDs.
- Release reauthorization applies before contextual code/edge/source serialization. A refusal reveals no source, identifier, count, edge, evidence, or exposure receipt.
- Any display-metadata persistence is additive and owner-controlled. It cannot change Source/Checkout identity, owner principal, grant semantics, or existing View publication.
- D-A retirement is an admission cutover, not storage contraction. Rollback may revert new presentation/read seams while retaining the writer retirement fence; reopening old writers needs a separately accepted product decision.
- Applied historical migrations, historical data, document versions/comments, Rules, Issues, and UCI projections are preserved. Physical deletion needs separate authorized contraction evidence.
