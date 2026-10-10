# Feature Specification: Operator Workspace — D-A

**Feature Branch**: `feature/operator-workspace-da`

**Created**: 2026-09-10
**Amended**: 2026-10-07
**Status**: Prior D-A source baseline accepted; M1 native first-use integration and installed exact-candidate proof pending

**Input**: Deliver the first independently useful operator workspace over existing Engram authority. The operator must be able to investigate code from the normal homepage without inventing a second authority, a manual graph, or a plaintext-book workflow.

## Outcome and completion boundary

**D-A — Connect a working copy and explain a real code relationship** is the first installable Feature 011 outcome. From the normal homepage, an operator finds Workspace, connects a real repository or another Git worktree through the ordinary installed native host, reads back the server catalog, selects a repository and working copy by human identity, sees the indexed snapshot's freshness and coverage, searches, opens source, follows an automatically derived direct or reverse relation, and opens its evidence. The investigation stays in one authorized immutable Source/Checkout/View context and never asks for a UUID, hidden route, SQL, browser filesystem registration, or a manual graph write.

The former honest-shell/design-fidelity-scaffold result is **not** a D-A completion boundary. Truthful loading, bounded counts, lazy settings work, and state labels remain quality constraints where the D-A surface uses them; they do not substitute for the homepage-to-evidence journey.

D-A also retires active manual knowledge-graph writing and plaintext book intake while preserving historical readers, records, and provenance. Source acceptance does not prove installation: the release gate stays closed until the configured single-user no-auth HTTP LAN Home → Workspace journey is replayed on the actual installed server, console, and plugin.

The [responsive-layout receipt](acceptance/da-workspace-responsive-layout-receipt.json) proves only Workspace shell layout and drawer behavior at its recorded viewports. Its configured embedding provider was unavailable (`embedded_chunks=0 of 64`), so it does not reassert semantic-provider explorer evidence or satisfy installed DA01–DA04 journey acceptance.

## Terms

- **Repository**, **Working copy**, and **Indexed snapshot** are the operator-facing names for the existing Source, Checkout, and immutable View. Labels are locators and presentation only; the server-resolved typed context remains authority.
- **Workspace** is the normal operator surface containing context selection, index state, code search or structure, source, relations, and evidence for one selected snapshot.
- **Evidence** is the actual released source reference attached to a derived relation. An endpoint entity or destination definition is not represented as an exact reference-site proof when that precision is unavailable.
- **Historical graph** means retained `knowledge_nodes`/`knowledge_edges` readers and their established consumers. It is not the UCI code graph and is not a D-A editing surface.

## Actors

- **Operator**: Uses the normal browser console to investigate one permitted code state and understand its readiness.
- **No-auth browser visitor**: In the explicitly configured single-user `ENGRAM_AUTH_DISABLED=true` realm, selects server-authorized Source/Checkout/View context on the HTTP LAN origin without browser sign-in or a read grant.
- **Authenticated browser subject and source owner**: In the separate auth-enabled realm, retains persisted identity and explicit Source/Checkout grants. Grant issuance and revocation still require exact owner authority; this is deferred from no-auth installed acceptance, not removed.
- **Daemon owner**: Owns the workstation that indexes a working copy. The browser can make an authorized request but cannot claim the daemon ran it.

## Clarifications

### Session 2026-09-10

- Q: How does the current no-auth browser visitor receive code access? → A: With `ENGRAM_AUTH_DISABLED=true`, the server selects and reauthorizes a no-auth Source/Checkout/View. No login or browser grant is required. Labels and paths do not authorize access. Auth-enabled browser access continues to require a persisted subject and explicit Source/Checkout grant.
- Q: What releases an authorized code response? → A: Every transport uses the same UCI-owned release operation. It reauthorizes immediately before the response, records non-content exposure, and suppresses contextual content if either step cannot complete.

### Session 2026-09-17

- Q: What is D-A's first usable result? → A: The normal homepage-to-workspace investigation of one named working copy through source, direct or reverse derived relation, and evidence, with old manual graph and plaintext-book writers retired.
- Q: Which browser origin is supported for installed D-A acceptance? → A: The configured single-user HTTP LAN origin with auth disabled. HTTPS, proxy, sign-in, and grant issuance are not prerequisites for this path; auth-enabled behavior remains a separate deferred path.

### Session 2026-10-07 — M1 forward integration

- First use must start with an empty browser catalog, not a source pre-registered by an acceptance fixture. The visible Add / connect repository action supplies an ordinary installed-agent task. The native host resolves its current real Git root and uses existing `codebase_context register`; the browser sends neither a local path nor authority derived from labels.
- A second worktree uses a Source selected from a fresh native server response. Its server-issued handle/current binding supplies the Source; the operator does not enter `source_id`. Ambiguous names never authorize merging Sources.
- Tab connection, empty catalog, registered checkout without View, active indexing, failed/provider-degraded embedding, historical/offline View, and a genuinely empty query remain distinct. Null candidate and null pin cannot produce a pinned success label.
- M0's installed 6.50.3 ordinary memory proof is retained in its accepted scope. M1 is the next main product slice. Useful-data migration/contraction follows as M2, measured quality/language coverage as M3, and Book Context/cognitive evolution as M4. Fresh-host automatic context remains an independent acceptance item.
- Preserve the incumbent static Vue/Vite implementation and visual world. This is Operate refinement, not init, redesign, or wholesale Nuxt donor integration. Existing accepted mechanisms and historical evidence keep their original scope.


## User Scenarios & Testing

### User Story 1 — Find a named working copy from Home (Priority: P1)

An operator starts at the normal homepage, finds Workspace, selects an accessible repository and working copy by readable names, and understands whether its selected snapshot is ready to investigate.

**Why this priority**: A technically valid Code page is not a working surface if the operator must already know its hidden URL, a binding protocol, or an identifier.

**Independent Test**: Starting only at the normal homepage and using visible browser controls, select two linked working copies with distinct human labels. Verify each selected snapshot identifies its revision/time, coverage and readiness state without a UUID, SQL, direct `/code` navigation, or manually supplied context.

**Acceptance Scenarios**:

1. **Given** a configured no-auth source and two working copies, **When** the operator opens Home on the configured HTTP LAN origin, **Then** Workspace offers repository and working-copy choices without login or grants.
2. **Given** a working copy has a published snapshot, **When** the operator selects it, **Then** Workspace shows its snapshot revision/time, indexed coverage where known, supported-language scope, and one truthful state: Ready, Updating, Needs indexing, Failed, or Newer snapshot available.
3. **Given** there are no registered repositories, no published snapshot, or an offline owner, **When** the operator reaches Workspace, **Then** it distinguishes those states and offers the ordinary native connection task plus catalog readback, without a generic binding error, localhost substitution, or empty successful result. An auth-enabled realm continues to distinguish missing grants.

4. **Given** an empty catalog, **When** an independent engineer follows Add / connect repository in an ordinary installed agent from the real Git root, **Then** native registration/index/status yields a server catalog choice without hidden author fixture setup, a local browser path, project-marker rewrite, UUID, or SQL.
5. **Given** two real dirty worktrees, **When** A is saved, renamed, deleted, and the confirmed owning daemon is restarted through supported control, **Then** B and historical pinned Views stay independent, and a fresh native session resolves server-issued contexts again.
---

### User Story 2 — Inspect source, relation, and evidence in one snapshot (Priority: P1)

An operator searches the selected working copy by symbol or ordinary language, opens a source span, follows direct or reverse derived code relations, and opens the selected relation's evidence without losing the selected snapshot.

**Why this priority**: Search or a graph image alone does not answer why code is related or whether the displayed evidence belongs to the source state the operator selected.

**Independent Test**: In a corpus with more candidates than one result page, use a direct or reverse relation to open a neighbor that was absent from the first search page. Verify its evidence opens in the same permitted View and that a second tab's different working copy remains unchanged. The conceptual query must use a real provider over more than 50 eligible candidates; lexical or degraded output is recorded `NOT_PROVEN`, not accepted as semantic evidence.

**Acceptance Scenarios**:

1. **Given** a Ready or partially supported selected snapshot, **When** the operator searches a symbol or a natural-language intent, **Then** result count, shown page, continuation, semantic/lexical mode, and coverage limitations remain distinct; a page length never becomes the corpus total.
2. **Given** the operator opens a source result, **When** the operator chooses Directly calls or Called by, **Then** the relation list and any bounded visual use the same snapshot and offer an accessible non-visual equivalent.
3. **Given** a relation has released evidence, **When** the operator opens Evidence, **Then** the supporting file/span/digest opens through the authorized source-read boundary. If only entity-level evidence exists, the console labels that granularity rather than claiming an exact call-site.
4. **Given** a newer snapshot or stale or mismatched continuation, **When** the operator requests data, **Then** the current View is never silently replaced and the response remains authorized for that View or discloses no unrelated code. Revoked grants remain a separate auth-enabled negative case.

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

- Missing registered sources, a working copy without a published snapshot, an offline owner, a provider failure, and a genuine empty search are distinct states. Missing read grants remain an auth-enabled state, not a no-auth prerequisite.
- Updating retains the last complete selected snapshot with its age. A new publication is offered explicitly and does not replace another tab or an open source span.
- Unsupported languages, dynamic calls, partial extraction, a bounded traversal, unavailable source evidence, and a semantic-provider fallback are limitations, not proof that no relation or result exists.
- In an auth-enabled realm, source owner and browser user can differ; grant administration requires exact owner equality. No-auth first use does not create or infer an authenticated principal.
- Existing pending or processing book jobs do not contain replayable plaintext. In the existing single-container deployment, no old book-writer process may remain live before D-A transitions residual jobs idempotently to `failed` with an interrupted-by-retirement reason; D-A adds no lease or heartbeat subsystem. The transition preserves `source_book_job_id` and partial historical documents, and a rerun remains idempotent.

## Requirements

### Functional Requirements

- **FR-001**: Home and primary navigation MUST expose Workspace and visible Add / connect repository as the normal entry to D-A. The empty-catalog next action MUST be executable in the ordinary installed native host and followed by server catalog readback. Completion MUST NOT require a direct hidden route, UUID, SQL, browser filesystem registration, hidden author setup, manual binding registration, manual node/edge creation, or technical proof entry.
- **FR-002**: Workspace MUST present repository, working-copy, and indexed-snapshot identities in human language. Source/Checkout/View, grants, and opaque references remain server-resolved authority; labels, paths, remotes, branches, and device/location text MUST NOT authorize access.
- **FR-003**: Every contextual code request MUST use one authorized Source, Checkout, and immutable View. Search, structure, graph/relation, evidence, and exact-source responses shown together MUST remain View-pinned; tabs and MCP contexts remain independent.
- **FR-004**: Workspace MUST show selected-snapshot freshness, coverage where available, supported scope, and Ready, Updating, Needs indexing, Failed, or Newer snapshot available distinctly. Requesting indexing is not completion; browser code MUST NOT receive a workstation secret or remote working-copy path.
- **FR-005**: Workspace MUST provide a bounded selected-View structure and result path before or alongside search, with opaque continuation and no fabricated filesystem tree. Search MUST preserve query/filter/View semantics and MUST NOT infer total coverage from a page size.
- **FR-006**: Workspace MUST present automatically derived direct and reverse code relations with their type, limitations, snapshot, and released evidence. It MUST permit an authorized neighbor absent from the current search page to open through relation navigation and MUST retain an accessible relation-list equivalent to any visual graph.
- **FR-007**: For the configured `ENGRAM_AUTH_DISABLED=true` single-user HTTP LAN realm, browser reads MUST use server-resolved no-auth Source/Checkout/View and the existing release checks without browser login or Source/Checkout grant issuance. The auth-enabled realm MUST retain its persistent BrowserSubject, explicit Source/Checkout grant, exact owner checks, and audit; neither mode MAY infer authority from a label, path, or administrator role.
- **FR-008**: The console MUST remain a read-only presentation of code-derived facts. It MUST NOT create or edit AST facts, reconstruct a second code graph, bypass UCI through direct storage, add HTTP MCP, or expose daemon credentials.
- **FR-009**: D-A MUST remove manual knowledge-graph create/delete admission from the console, HTTP route registration, MCP tool list/dispatch, feature-flag resurrection paths, and worker startup paths. The consumer map MUST retain or explicitly disposition `internal/retrieval/hybrid.go` Tier2 `GraphStoreInterface.Traverse`, historical graph read surfaces, and the current `/api/context/search` owner (`service.go` registration and `handleSearchByPrompt`). D-A MUST NOT remove `internal/graph` or the separate UCI code graph.
- **FR-010**: D-A MUST remove plaintext book-job admission, uploader page, runtime pipeline startup, and worker execution path. In the existing single-container deployment, no old book-writer process may be live before it idempotently terminalizes residual nonterminal jobs as `failed` with an interrupted-by-retirement reason; it MUST preserve `source_book_job_id` and partial historical documents, prove rerun idempotence, and MUST NOT add a lease/heartbeat subsystem, replay unavailable plaintext, or invoke compensation that deletes historical rows.
- **FR-011**: D-A MUST preserve PostgreSQL/pgvector, UCI, Source/Checkout/View ACL and release boundaries, versioned Documents, Rules, Issues, historical graph/book data, and named retained readers. It MUST NOT rewrite applied historical migrations, drop storage, or create a universal graph/library replacement.
- **FR-012**: Loading, empty, denied, error, stale, partial, unsupported, timeout, and offline states MUST remain distinct wherever D-A exposes them. The journey MUST be keyboard-operable with visible focus and live status text, usable at 1440, 980, and 390 CSS-pixel widths and at 200% zoom, with complete RU/EN task language and no zh regression.
- **FR-013**: New D-A visual-world work MUST update private `.od` authoring first and promote only its curated reviewed snapshot under `design/operator-console/PROMOTION-CONTRACT.md`. M1 refines the incumbent static Vue/Vite world and does not require init, redesign, assets, or an unrelated promotion program. No raw donor export may overwrite `apps/operator-console/`.
- **FR-014**: Installed D-A acceptance MUST use the configured single-user no-auth HTTP LAN origin without requiring HTTPS, proxy, browser sign-in, or grants. Auth-enabled browser behavior remains deferred, not removed. Source acceptance alone MUST NOT claim installed normal-homepage DA01–DA04 proof.

### Key entities

- **Workspace context**: The selected authorized Source, Checkout, and immutable View presented as Repository, Working copy, and Indexed snapshot.
- **Workspace catalog choice**: Server-filtered, non-authorizing human display metadata plus opaque selection reference for an accessible Source/Checkout/View.
- **Relation evidence reference**: A released View-pinned source descriptor with evidence kind and supported span/digest granularity.
- **Browser read grant**: In the separate auth-enabled realm, explicit permission for a persistent browser subject to read one Source/Checkout pair; not a prerequisite in the selected no-auth realm.
- **Historical book job**: A retained provenance/status record whose old plaintext admission and execution are retired; it does not become a D-A Book Context entity.

## Success Criteria

### Measurable outcomes

- **SC-001**: From the normal homepage and an empty catalog, an independent engineer connects a repository and two real worktrees through the visible ordinary native-host task, reads back readable server labels, and selects each working copy without direct `/code` navigation, UUID entry, SQL, browser paths, project-marker mutation, or hidden author preparation.
- **SC-002**: For a selected snapshot, the operator can read freshness/coverage/limitation state, search a corpus with more than one result page, open source, follow one direct and one reverse derived relation, and open released evidence for an off-page neighbor in that same View.
- **SC-003**: In two dirty worktrees with independent tabs and concurrent MCP contexts, save/rename/delete and a confirmed supported owning-daemon restart do not cross View boundaries. An explicit newer snapshot transition is required. Fresh native sessions re-resolve server-returned contexts. Absent, revoked, expired, ambiguous, or mismatched authority reveals no unrelated contextual source, identifier, count, relation, or evidence.
- **SC-004**: The D-A source-acceptance fixture demonstrates a symbol query and one real-provider non-lexical conceptual query over more than 50 eligible candidates. Lexical or degraded output is `NOT_PROVEN` and cannot satisfy this criterion. Supported-language, dynamic-call, partial-coverage, and unavailable-evidence limitations are labeled honestly.
- **SC-005**: After single-container quiescence, every writer in the approved nonempty manual-graph/book consumer map remains unavailable through visible UI, direct old HTTP/MCP calls, and prior flags after restart. A residual book job becomes `failed` with its retirement reason while preserving `source_book_job_id` and partial historical documents; repeating that transition is idempotent. `internal/retrieval/hybrid.go` Tier2 `Traverse`, historical graph readers, `/api/context/search`, UCI graph, Rules, Issues, and two document versions remain readable under existing ACL.
- **SC-006**: DA01–DA04 work at 1440, 980, and 390 CSS-pixel widths and actual 200% browser zoom in RU and EN with keyboard-only context selection, source, relation, evidence, retry, and explicit newer-snapshot transition. Existing zh navigation receives a regression check.
- **SC-007**: D-A source acceptance may be recorded only after SC-001–SC-006 pass on the exact candidate. The release gate remains closed until an installed normal-homepage DA01–DA04 walkthrough proves discovery, source/relation/evidence, retirement/history, and accessibility on the configured no-auth HTTP LAN origin.

## Scope boundaries

### In scope

- Normal Home → Workspace discovery; human-readable repository/working-copy/snapshot selection; selected-context index state; bounded structure/search; source; direct/reverse relation; released evidence; and explicit newer-snapshot transition.
- Existing UCI/binding/release reuse plus the narrow display, no-auth selection, structure, and evidence seams needed for the D-A journey. Auth-enabled grant onboarding is retained as a separate deferred path.
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
- The selected installed browser path is single-user no-auth HTTP LAN; auth-enabled browser policy remains separate and deferred. This source contract alone does not certify installation.
- This amendment depends on Constitution 3.0.0, the 2026-09-17 operator-workspace audit package, and the accepted D2/UX/QA handoffs. It does not adopt their audit as runtime proof.
