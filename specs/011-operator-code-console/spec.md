# Feature Specification: Operator Workspace — D-A

**Feature Branch**: `feature/operator-workspace-da`

**Created**: 2026-09-10
**Amended**: 2026-09-17
**Status**: Implemented and source-accepted; installed exact-head D-A release proof is pending

**Input**: Deliver the first independently useful operator workspace over existing Engram authority. The operator must be able to investigate code from the normal homepage without inventing a second authority, a manual graph, or a plaintext-book workflow.

## Outcome and completion boundary

**D-A — Select a working copy and explain a real code relationship** is the first installable Feature 011 outcome. From the normal homepage, an operator finds Workspace, selects an authorized repository and working copy by human identity, sees the selected indexed snapshot's freshness and coverage, searches, opens source, follows an automatically derived direct or reverse relation, and opens that relation's evidence. The investigation stays in one authorized immutable Source/Checkout/View context and never asks for a UUID, hidden route, SQL, or a manual graph write.

The former honest-shell/design-fidelity-scaffold result is **not** a D-A completion boundary. Truthful loading, bounded counts, lazy settings work, and state labels remain quality constraints where the D-A surface uses them; they do not substitute for the homepage-to-evidence journey.

D-A also retires the active manual knowledge-graph writer and plaintext book-intake workflows at their executable boundaries while preserving their historical readers, records, and provenance. It does not certify every historical Feature 011 collection action complete. D-A has two completion states: the exact source candidate is source-accepted; the release gate remains closed until the supported browser/origin decision and installed normal-homepage DA01–DA04 proof are recorded.

The [responsive-layout receipt](acceptance/da-workspace-responsive-layout-receipt.json) proves only Workspace shell layout and drawer behavior at its recorded viewports. Its configured embedding provider was unavailable (`embedded_chunks=0 of 64`), so it does not reassert semantic-provider explorer evidence or satisfy installed DA01–DA04 journey acceptance.

## Terms

- **Repository**, **Working copy**, and **Indexed snapshot** are the operator-facing names for the existing Source, Checkout, and immutable View. Labels are locators and presentation only; the server-resolved typed context remains authority.
- **Workspace** is the normal operator surface containing context selection, index state, code search or structure, source, relations, and evidence for one selected snapshot.
- **Evidence** is the actual released source reference attached to a derived relation. An endpoint entity or destination definition is not represented as an exact reference-site proof when that precision is unavailable.
- **Historical graph** means retained `knowledge_nodes`/`knowledge_edges` readers and their established consumers. It is not the UCI code graph and is not a D-A editing surface.

## Actors

- **Operator**: Uses the normal browser console to investigate one permitted code state and understand its readiness.
- **Authenticated browser subject**: A persistent browser identity with an explicit Source/Checkout read grant. An administrator role, Space label, branch, path, or tab does not substitute for a grant.
- **Source owner**: May issue or revoke read grants only through exact source-owner authority.
- **Daemon owner**: Owns the workstation that indexes a working copy. The browser can make an authorized request but cannot claim the daemon ran it.

## Clarifications

### Session 2026-09-10

- Q: How does a browser user receive code access? → A: A persistent authenticated browser subject receives an explicit Source and Checkout read grant. The console never infers authority from an administrator role, Space membership, a path, or a legacy project identifier.
- Q: What releases an authorized code response? → A: Every transport uses the same UCI-owned release operation. It reauthorizes immediately before the response, records non-content exposure, and suppresses contextual content if either step cannot complete.

### Session 2026-09-17

- Q: What is D-A's first usable result? → A: The normal homepage-to-workspace investigation of one named working copy through source, direct or reverse derived relation, and evidence, with old manual graph and plaintext-book writers retired.
- Q: Does D-A decide the supported secure browser origin? → A: No. The secure-origin/authenticated-browser versus deliberately supported single-user HTTP/no-auth deployment decision remains install-bound. D-A source work preserves exact grant and UCI authority and does not invent a bypass.

## User Scenarios & Testing

### User Story 1 — Find a named working copy from Home (Priority: P1)

An operator starts at the normal homepage, finds Workspace, selects an accessible repository and working copy by readable names, and understands whether its selected snapshot is ready to investigate.

**Why this priority**: A technically valid Code page is not a working surface if the operator must already know its hidden URL, a binding protocol, or an identifier.

**Independent Test**: Starting only at the normal homepage and using visible browser controls, select two linked working copies with distinct human labels. Verify each selected snapshot identifies its revision/time, coverage and readiness state without a UUID, SQL, direct `/code` navigation, or manually supplied context.

**Acceptance Scenarios**:

1. **Given** an authenticated subject has an accessible repository and two working copies, **When** the operator opens Home, **Then** a visible Workspace entry leads to dependent repository and working-copy choices with human-readable repository, branch, and device/location labels.
2. **Given** a working copy has a published snapshot, **When** the operator selects it, **Then** Workspace shows its snapshot revision/time, indexed coverage where known, supported-language scope, and one truthful state: Ready, Updating, Needs indexing, Failed, or Newer snapshot available.
3. **Given** there are no accessible repositories, no grant, no published snapshot, or an offline owner, **When** the operator reaches Workspace, **Then** the console distinguishes those states and offers only a real supported next action; it does not present a generic binding error, localhost substitution, synthetic administrator, or empty successful result.

---

### User Story 2 — Inspect source, relation, and evidence in one snapshot (Priority: P1)

An operator searches the selected working copy by symbol or ordinary language, opens a source span, follows direct or reverse derived code relations, and opens the selected relation's evidence without losing the selected snapshot.

**Why this priority**: Search or a graph image alone does not answer why code is related or whether the displayed evidence belongs to the source state the operator selected.

**Independent Test**: In a corpus with more candidates than one result page, use a direct or reverse relation to open a neighbor that was absent from the first search page. Verify its evidence opens in the same permitted View and that a second tab's different working copy remains unchanged. The conceptual query must use a real provider over more than 50 eligible candidates; lexical or degraded output is recorded `NOT_PROVEN`, not accepted as semantic evidence.

**Acceptance Scenarios**:

1. **Given** a Ready or partially supported selected snapshot, **When** the operator searches a symbol or a natural-language intent, **Then** result count, shown page, continuation, semantic/lexical mode, and coverage limitations remain distinct; a page length never becomes the corpus total.
2. **Given** the operator opens a source result, **When** the operator chooses Directly calls or Called by, **Then** the relation list and any bounded visual use the same snapshot and offer an accessible non-visual equivalent.
3. **Given** a relation has released evidence, **When** the operator opens Evidence, **Then** the supporting file/span/digest opens through the authorized source-read boundary. If only entity-level evidence exists, the console labels that granularity rather than claiming an exact call-site.
4. **Given** a newer snapshot appears, a grant is revoked, or a continuation is stale or mismatched, **When** the operator requests data, **Then** the current snapshot is never silently replaced and the response either remains authorized in that snapshot or discloses no source, identifier, count, edge, or evidence outside the grant.

---

### User Story 3 — Retire obsolete writers without erasing history (Priority: P1)

An operator no longer encounters manual graph construction or plaintext book intake as normal work, while historical documents, provenance, historical graph readers, Rules, Issues, and versioned data remain available under their existing authority.

**Why this priority**: Hiding an old form leaves the unsafe workflow executable through its route, tool, flag, or worker. Deleting its data would break historical meaning and named readers.

**Independent Test**: In a disposable pre-retirement dataset, attempt every approved retired HTTP/MCP writer and enable prior flags after restart; each must remain unavailable. Then read historical document versions and provenance, exercise the retained graph/read consumers, and verify nonterminal old book jobs receive the agreed non-destructive terminal disposition.

**Acceptance Scenarios**:

1. **Given** an old Graph or Books bookmark, **When** the operator opens it after D-A, **Then** it cannot reveal a manual editor or uploader and gives a clear non-mutating transition or retirement state.
2. **Given** direct legacy HTTP/MCP writer calls or old flags, **When** they are exercised after cutover and restart, **Then** none can create/delete manual graph entities or begin plaintext book processing.
3. **Given** historical graph rows, book-produced versioned documents, comments, source-book provenance, Rules, or Issues, **When** authorized readers access them, **Then** their existing readable meaning and ACLs remain intact; no historical code relationship is relabeled as automatic code evidence.

## Edge cases and failure states

- Missing accessible sources, a missing read grant, a working copy without a published snapshot, an offline owner, a provider failure, and a genuine empty search are different states with different safe next actions.
- Updating retains the last complete selected snapshot with its age. A new publication is offered explicitly and does not replace another tab or an open source span.
- Unsupported languages, dynamic calls, partial extraction, a bounded traversal, unavailable source evidence, and a semantic-provider fallback are limitations, not proof that no relation or result exists.
- A source owner may differ from the browser user. Exact owner equality is required for grant administration; an unaccepted enrollment/link is not substituted by an administrator or a fixture grant.
- Existing pending or processing book jobs do not contain replayable plaintext. In the existing single-container deployment, no old book-writer process may remain live before D-A transitions residual jobs idempotently to `failed` with an interrupted-by-retirement reason; D-A adds no lease or heartbeat subsystem. The transition preserves `source_book_job_id` and partial historical documents, and a rerun remains idempotent.

## Requirements

### Functional Requirements

- **FR-001**: Home and primary navigation MUST expose Workspace as the normal entry to D-A. Completion MUST NOT require a direct hidden route, UUID, SQL, manual binding registration, manual node/edge creation, or technical proof entry.
- **FR-002**: Workspace MUST present repository, working-copy, and indexed-snapshot identities in human language. Source/Checkout/View, grants, and opaque references remain server-resolved authority; labels, paths, remotes, branches, and device/location text MUST NOT authorize access.
- **FR-003**: Every contextual code request MUST use one authorized Source, Checkout, and immutable View. Search, structure, graph/relation, evidence, and exact-source responses shown together MUST remain View-pinned; tabs and MCP contexts remain independent.
- **FR-004**: Workspace MUST show selected-snapshot freshness, coverage where available, supported scope, and Ready, Updating, Needs indexing, Failed, or Newer snapshot available distinctly. Requesting indexing is not completion; browser code MUST NOT receive a workstation secret or remote working-copy path.
- **FR-005**: Workspace MUST provide a bounded selected-View structure and result path before or alongside search, with opaque continuation and no fabricated filesystem tree. Search MUST preserve query/filter/View semantics and MUST NOT infer total coverage from a page size.
- **FR-006**: Workspace MUST present automatically derived direct and reverse code relations with their type, limitations, snapshot, and released evidence. It MUST permit an authorized neighbor absent from the current search page to open through relation navigation and MUST retain an accessible relation-list equivalent to any visual graph.
- **FR-007**: Browser read authority MUST require a persistent authenticated BrowserSubject and an explicit Source/Checkout grant. Owner-scoped grant issuance/revocation MUST use exact principal equality and audit; it MUST NOT infer authority from an administrator role, Space, branch, path, tab, or label.
- **FR-008**: The console MUST remain a read-only presentation of code-derived facts. It MUST NOT create or edit AST facts, reconstruct a second code graph, bypass UCI through direct storage, add HTTP MCP, or expose daemon credentials.
- **FR-009**: D-A MUST remove manual knowledge-graph create/delete admission from the console, HTTP route registration, MCP tool list/dispatch, feature-flag resurrection paths, and worker startup paths. The consumer map MUST retain or explicitly disposition `internal/retrieval/hybrid.go` Tier2 `GraphStoreInterface.Traverse`, historical graph read surfaces, and the current `/api/context/search` owner (`service.go` registration and `handleSearchByPrompt`). D-A MUST NOT remove `internal/graph` or the separate UCI code graph.
- **FR-010**: D-A MUST remove plaintext book-job admission, uploader page, runtime pipeline startup, and worker execution path. In the existing single-container deployment, no old book-writer process may be live before it idempotently terminalizes residual nonterminal jobs as `failed` with an interrupted-by-retirement reason; it MUST preserve `source_book_job_id` and partial historical documents, prove rerun idempotence, and MUST NOT add a lease/heartbeat subsystem, replay unavailable plaintext, or invoke compensation that deletes historical rows.
- **FR-011**: D-A MUST preserve PostgreSQL/pgvector, UCI, Source/Checkout/View ACL and release boundaries, versioned Documents, Rules, Issues, historical graph/book data, and named retained readers. It MUST NOT rewrite applied historical migrations, drop storage, or create a universal graph/library replacement.
- **FR-012**: Loading, empty, denied, error, stale, partial, unsupported, timeout, and offline states MUST remain distinct wherever D-A exposes them. The journey MUST be keyboard-operable with visible focus and live status text, usable at 1440, 980, and 390 CSS-pixel widths and at 200% zoom, with complete RU/EN task language and no zh regression.
- **FR-013**: D-A design work MUST update private `.od` authoring first and promote only its curated reviewed snapshot under `design/operator-console/PROMOTION-CONTRACT.md`. The old search → graph → Source shape and scaffold-fidelity instruction MUST NOT constrain the D-A runtime; no raw export may overwrite `apps/operator-console/`.
- **FR-014**: The supported secure-origin/authenticated-browser versus single-user HTTP/no-auth deployment policy remains an install-bound decision. Neutral source work MUST preserve current authority and truthful recovery states; it MUST NOT fabricate a connect/request-access action, relax origin checks, or create a synthetic administrator. D-A source acceptance MAY record the bounded candidate result, but the release gate remains closed until that decision and installed normal-homepage DA01–DA04 proof are recorded.

### Key entities

- **Workspace context**: The selected authorized Source, Checkout, and immutable View presented as Repository, Working copy, and Indexed snapshot.
- **Workspace catalog choice**: Server-filtered, non-authorizing human display metadata plus opaque selection reference for an accessible Source/Checkout/View.
- **Relation evidence reference**: A released View-pinned source descriptor with evidence kind and supported span/digest granularity.
- **Browser read grant**: Explicit permission for one persistent browser subject to read one Source/Checkout pair; it does not grant index ownership or View publication.
- **Historical book job**: A retained provenance/status record whose old plaintext admission and execution are retired; it does not become a D-A Book Context entity.

## Success Criteria

### Measurable outcomes

- **SC-001**: From the normal homepage, an operator finds Workspace and selects two different accessible working copies by repository, branch, and device/location identity without direct `/code` navigation, UUID entry, SQL, or manual graph setup.
- **SC-002**: For a selected snapshot, the operator can read freshness/coverage/limitation state, search a corpus with more than one result page, open source, follow one direct and one reverse derived relation, and open released evidence for an off-page neighbor in that same View.
- **SC-003**: In a two-worktree, two-tab, concurrent-MCP fixture, no result crosses a selected View boundary; an explicit newer snapshot transition is required. Absent, revoked, expired, ambiguous, or mismatched authority reveals no contextual source, identifier, count, relation, or evidence.
- **SC-004**: The D-A source-acceptance fixture demonstrates a symbol query and one real-provider non-lexical conceptual query over more than 50 eligible candidates. Lexical or degraded output is `NOT_PROVEN` and cannot satisfy this criterion. Supported-language, dynamic-call, partial-coverage, and unavailable-evidence limitations are labeled honestly.
- **SC-005**: After single-container quiescence, every writer in the approved nonempty manual-graph/book consumer map remains unavailable through visible UI, direct old HTTP/MCP calls, and prior flags after restart. A residual book job becomes `failed` with its retirement reason while preserving `source_book_job_id` and partial historical documents; repeating that transition is idempotent. `internal/retrieval/hybrid.go` Tier2 `Traverse`, historical graph readers, `/api/context/search`, UCI graph, Rules, Issues, and two document versions remain readable under existing ACL.
- **SC-006**: DA01–DA04 work at 1440, 980, and 390 CSS-pixel widths and actual 200% browser zoom in RU and EN with keyboard-only context selection, source, relation, evidence, retry, and explicit newer-snapshot transition. Existing zh navigation receives a regression check.
- **SC-007**: D-A source acceptance may be recorded only after SC-001–SC-006 pass on the exact candidate. The release gate remains closed until the supported browser/origin decision is recorded and an installed normal-homepage DA01–DA04 walkthrough proves discovery, source/relation/evidence, retirement/history, and accessibility in that chosen mode.

## Scope boundaries

### In scope

- Normal Home → Workspace discovery; human-readable repository/working-copy/snapshot selection; selected-context index state; bounded structure/search; source; direct/reverse relation; released evidence; and explicit newer-snapshot transition.
- Existing UCI/grant/binding/release reuse plus only the narrow display, owner-onboarding, structure, and evidence seams needed to make the D-A journey ordinary-user operable.
- Executable retirement of manual graph writers and plaintext book intake while preserving data, historical readers, and an honest residual-job lifecycle.
- Design-source amendment/promotion, task-language localization, accessibility, and D-A-specific ordinary-user and negative retirement proof.

### Out of scope

- **D-B**: collection organization, broad typed-data search, provenance inspector across memories/documents, editorial correction authority, selection/bulk semantics, or changed Rules/Issues/Memory/Queue/Document action matrices.
- **D-C**: quality-decision queue redesign, Book Context catalog/source/reader/application workflow, new book extraction, concept graph, or cognitive-memory platform work.
- A universal graph, manual relation editing, new ontology, replacement index/database, raw SQL workflow, direct storage access, HTTP MCP, broad IAM, secret delivery, UCI reacceptance, destructive migration, deployment, release, or publication.

## Ownership boundaries

- **Design-source owner** updates private `.od` and performs curated promotion; the existing public snapshot is not silently edited or treated as a D-A parity claim.
- **Browser onboarding/catalog owner** owns human display metadata and exact owner-scoped grant administration.
- **UCI read-model owner** owns bounded View-pinned structure and relation-evidence read contracts; **UCI release owner** retains authorization/exposure release.
- **Console workspace owner** owns Home/NAV/Workspace presentation, keyboard and localization behavior, and removal of obsolete operator forms.
- **Retirement owner** owns manual graph/book admission cutover and nonterminal book-job disposition; **single integrator** exclusively changes shared registration and migration files.
- **D-B/D-C owners** receive no task from this D-A plan.

## Assumptions and dependencies

- Existing UCI remains the sole authority for Source, Checkout, View, query, relation, source descriptor, and release semantics; PostgreSQL/pgvector remains authoritative storage.
- Existing versioned Documents, Rules, Issues, historical graph/book records, and UCI code graph are retained surfaces, not reimplemented D-A substitutes.
- The implementation owner re-reads the exact candidate before touching source because the audit's primary checkout was stale and dirty.
- The unresolved supported-browser/origin policy is recorded above as an install-bound decision, not a source-work blocker.
- This amendment depends on Constitution 3.0.0, the 2026-09-17 operator-workspace audit package, and the accepted D2/UX/QA handoffs. It does not adopt their audit as runtime proof.
