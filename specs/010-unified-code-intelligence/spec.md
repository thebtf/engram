# Feature Specification: Unified Code Intelligence

**Feature Branch**: `uci/unified-code-intelligence-r1`
**Created**: 2026-09-05
**Status**: Ready for planning
**Input**: Establish Unified Code Intelligence (UCI) as Engram’s first releasable code-intelligence outcome: agents can safely understand, search, and trace the exact source state they are working on without crossing worktree or authorization boundaries.

## Outcome and Release Boundary

UCI-1A and UCI-1B together are the first releasable UCI scope. They deliver one coherent, installed code-intelligence capability for real saved working copies: two real worktrees can be served by one daemon at the same time, separate MCP clients retain separate contexts, and each request is tied to the correct Source, Checkout, and immutable View.

The release must provide exact lookup, full-text retrieval, and a real semantic-vector retrieval path; bounded graph exploration must agree with the same View as search. It must update itself after observed saved changes and recover honestly after interruption. Native installation proof and preservation of existing authorization boundaries are part of the release outcome, not optional follow-up work.

UCI-2 and UCI-3 remain explicit deferred requirements. In particular, the future Code UI is UCI-2 surface work and requires its own accepted Spec Kit feature before it can be designed or delivered.

## Actors

- **Coding agent**: Uses native MCP tools to locate an implementation, inspect a versioned excerpt, and understand direct or transitive code relationships before making a change.
- **Operator**: Runs one authorized Engram installation and needs clear status, recovery behavior, and evidence that separate working copies and client sessions remain isolated.
- **Authorized source owner**: Grants access to a source or private checkout and expects that the capability neither discovers nor exposes code outside that existing authority.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Work safely in parallel worktrees (Priority: P1)

A coding agent works in one of two real, saved, dirty worktrees of the same source while another MCP client works in the other. Both use one daemon, yet each agent sees only its own implementation and relationships.

**Why this priority**: Correct worktree isolation is the foundation for every code answer; an otherwise useful answer is harmful if it comes from a neighboring working copy.

**Independent Test**: Bind two simultaneous native MCP clients to two worktrees whose relative path, symbol name, and recorded revision are the same but whose saved bodies and callees differ. The same query returns the correct distinct result and graph for each client, with no cross-worktree rows.

**Acceptance Scenarios**:

1. **Given** two authorized worktrees of one source with different saved implementations, **When** each bound client runs the same search, **Then** each receives only the result from its selected working copy and its response identifies the source state used.
2. **Given** two clients already bound to different worktrees, **When** a third client connects with another current directory, **Then** the first two clients keep their existing default contexts.
3. **Given** a client cannot establish a trustworthy current working copy while more than one authorized checkout is available, **When** it requests code intelligence, **Then** it receives an explicit context-selection outcome rather than a result from an arbitrary worktree.

---

### User Story 2 - Find and trace the exact source state (Priority: P1)

A coding agent asks where behavior lives, who uses it, or what a change may affect. It receives concise, source-grounded results from the same recorded source state, including exact matches, text matches, semantic matches, and bounded relationship context.

**Why this priority**: The product earns its value by replacing broad repository reading with trustworthy, explainable navigation.

**Independent Test**: Use a curated source corpus containing exact-name, lexical, conceptual, and relationship questions. Confirm that returned excerpts, references, and graph context all identify one selected View and that a conceptual query without matching keywords is answered through the real semantic path.

**Acceptance Scenarios**:

1. **Given** a selected current source state, **When** an agent searches for a known symbol or path, **Then** it receives an exact, versioned excerpt from that state.
2. **Given** a conceptual request whose expected code has no direct keyword match, **When** semantic retrieval is available, **Then** the relevant current source appears through the semantic retrieval path with its source evidence.
3. **Given** an agent expands a returned result into explain, neighbors, path, impact, or flow context, **When** the exploration completes or reaches a declared limit, **Then** the result reports source-grounded relationships and its coverage or stopping condition without claiming more certainty than the available evidence supports.

---

### User Story 3 - Stay current and recover without manual reindexing (Priority: P1)

After an agent saves, deletes, renames, switches branches, or restarts a local component, the relevant working copy catches up automatically. A temporary interruption never silently replaces a current answer with a mixed or falsely current result.

**Why this priority**: Manual update rituals make code intelligence unreliable during ordinary development and invite agents to reason from stale code.

**Independent Test**: Change one worktree while keeping the other unchanged; introduce an interrupted update, a restart, and an offline/reconnect interval. Verify that the changed worktree reaches a new coherent state, the unchanged worktree remains unchanged, and recovery reports stale or partial state rather than inventing success.

**Acceptance Scenarios**:

1. **Given** published source states for worktrees A and B, **When** a saved file in A changes or is removed, **Then** A receives a new coherent state while B’s search and graph results remain unchanged.
2. **Given** an update is incomplete or a watcher loses events, **When** the product resumes, **Then** it reconciles the current saved source state and does not publish a partial update as the new current state.
3. **Given** a daemon or server restart after unchanged files were indexed, **When** clients reconnect, **Then** their correct contexts recover and unchanged source material is not treated as newly changed work.

---

### User Story 4 - Use an installed capability without widening access (Priority: P2)

An operator installs the native UCI capability and connects at least two real MCP clients to one daemon. Authorized users can use the capability, while denied, revoked, ambiguous, or stale selections do not reveal code, identifiers, counts, or private dirty work.

**Why this priority**: A product-only demonstration is insufficient if installation changes authority or only works through a test-only path.

**Independent Test**: In a disposable native installation, connect two standard MCP clients to one daemon and exercise the UCI-1 acceptance set using real Git worktrees. Repeat authorization-negative cases and confirm that each refusal preserves the pre-existing access boundary.

**Acceptance Scenarios**:

1. **Given** a fresh supported native environment, **When** the operator installs the approved UCI-1 components and connects two concurrent MCP clients, **Then** both complete the UCI-1 workflows through the installed path rather than a test-only transport.
2. **Given** a principal lacks access, has been revoked, or supplies a mismatched context, **When** it requests search, read, pagination, or graph traversal, **Then** the request is refused without disclosing out-of-scope source information.
3. **Given** existing project or Space-based product records and a legacy compatibility reference, **When** UCI resolves code context, **Then** it preserves their existing semantics and never treats a label, path, branch, or legacy identifier as authority to select a working copy.

---

### User Story 5 - Demonstrate a useful, bounded improvement (Priority: P2)

A coding agent uses UCI for representative navigation and impact questions and can answer more of them with less unnecessary source reading, while known coverage limitations remain visible.

**Why this priority**: The first release must establish measured developer value, not merely expose a new set of tools.

**Independent Test**: Run the pre-registered twelve-task product set against a real checkout and a recorded baseline, retaining the expected source spans or paths and all failures.

**Acceptance Scenarios**:

1. **Given** the approved twelve-task product set, **When** agents use UCI-1, **Then** results retain the recorded source evidence and distinguish success, partial coverage, and failure.
2. **Given** a simple exact lookup is already best served by ordinary file reading, **When** an agent uses that direct path, **Then** UCI does not require a ceremonial graph or semantic step to claim value.

---

### Edge Cases

- Two dirty worktrees share a source, branch label, relative path, symbol name, and recorded revision but have different saved file bodies or relationships.
- A client changes its current directory, has no trustworthy current directory, or reconnects while multiple authorized checkouts are available.
- A working copy switches branches, uses detached or unborn history, moves, becomes temporarily unavailable, or is deleted and recreated at the same path.
- A save, rename, delete-all operation, incomplete update, lost acknowledgement, watcher overflow, restart, or offline interval occurs while indexing is in progress.
- A file has malformed syntax, unsupported content, nested ignore rules, inaccessible descendants, symlink or reparse-point escape risk, unusual paths, or protected secret-bearing content.
- A new source version has no corresponding semantic representation yet, a semantic provider is unavailable, or the candidate set is so selective that a global result would be misleading.
- A relationship is ambiguous, dynamic, partial, or exceeds a declared traversal budget; absence of a displayed relationship must not be represented as proof that none exists.
- A request uses revoked access, a mismatched source/checkout/View selection, a legacy identifier with more than one possible meaning, or a private checkout that must remain private.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-01**: The product MUST resolve every UCI request to one server-authorized **Source**, **Checkout**, and immutable **View** before it selects candidates, reads source, traverses relationships, updates state, or continues a result. **Acceptance**: A missing, ambiguous, mismatched, or unauthorized selection returns an explicit non-disclosing outcome before source content or relationship facts are returned.
- **FR-02**: The product MUST support two real saved worktrees of one source through one daemon at the same time, including different dirty saved states, without treating them as one mutable index. **Acceptance**: The two-worktree fixture returns the correct distinct snippets and relationships for the same relative path and symbol name, with zero cross-worktree results.
- **FR-03**: The product MUST retain a separate current code context for each MCP client session. **Acceptance**: Connecting, selecting, moving, or reconnecting one client cannot change any other client’s default checkout or View.
- **FR-04**: The product MUST return search results and graph facts from the same selected View and identify that source state in each result. **Acceptance**: A combined search-and-graph acceptance probe finds no result whose excerpt and relationship evidence refer to different Views.
- **FR-05**: The product MUST support exact name, qualified-symbol, and path retrieval against the authorized selected View. **Acceptance**: Each documented exact lookup in the acceptance corpus returns its expected current excerpt or an honest no-result/coverage outcome.
- **FR-06**: The product MUST support full-text retrieval against the authorized selected View. **Acceptance**: Each documented lexical query in the acceptance corpus searches only the selected View and returns its expected current source evidence or an honest no-result/coverage outcome.
- **FR-07**: The product MUST support a real semantic-vector retrieval path for conceptual queries, distinct from exact and full-text retrieval. **Acceptance**: A documented conceptual query with no direct keyword match returns its expected current source through a provider-generated semantic result; an unavailable provider is visibly degraded and cannot be counted as semantic success.
- **FR-08**: The product MUST provide bounded, evidence-labeled code relationship exploration for explain, neighbors, path, impact, and flow questions. **Acceptance**: The relationship fixture distinguishes established, partial, ambiguous, and capped outcomes rather than fabricating a unique dependency or a conclusive absence.
- **FR-09**: The product MUST automatically observe and reconcile saved changes for authorized worktrees, including save, delete, rename, branch transition, restart, overflow, and reconnect recovery. **Acceptance**: The recovery scenarios publish only a complete new state for the affected worktree and preserve the other worktree’s previously published state.
- **FR-10**: The product MUST make currentness, partial coverage, unsupported content, offline state, and absence of results distinguishable to clients. **Acceptance**: Each relevant failure or degradation scenario reports its applicable state and does not label old, incomplete, or unobserved source material as current.
- **FR-11**: The product MUST preserve existing Source, Checkout, project, Space, privacy, and principal boundaries without widening authorization. **Acceptance**: The authorization-negative matrix confirms that denied, revoked, private, cross-source, and ambiguous cases disclose no out-of-scope body, identifier, count, or relationship.
- **FR-12**: UCI-1A and UCI-1B MUST be released only as one installed, native, end-to-end UCI-1 capability with installation proof and measured developer value. **Acceptance**: The installed acceptance exercise covers the first-release UCI-1 scenarios, and the twelve-task value set records results against its baseline before a release claim is made.

### Key Entities

- **Space**: A stable logical grouping for product knowledge and work; it may group sources but does not by itself grant access to code.
- **Source**: An independently authorized body of code or approved contextual material that can have one or more working copies.
- **Checkout**: One local working copy of a Source, including a linked worktree, with its own lifecycle and access boundary.
- **View**: One immutable, reproducible observed version of a Checkout’s indexed saved files and declared coverage.
- **Client Context**: The current, client-scoped selection that allows an MCP request to resolve a Source, Checkout, and View without silently borrowing another client’s location.
- **Code Result**: A bounded, source-grounded excerpt or relationship answer that identifies its Source and View, match mode, and any coverage limitation.
- **Compatibility Reference**: A typed historical reference that may aid controlled resolution but cannot itself grant access or choose an arbitrary Checkout.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-01**: In the two-worktree adversarial fixture, 100% of valid requests return only their selected worktree’s saved source state, and 0 requests return a neighboring worktree’s body or relationship.
- **SC-02**: In every acceptance response that combines search and graph information, 100% of excerpts and relationships identify the same selected View.
- **SC-03**: On the pre-registered twelve-task product set, a relevant source appears in the top five results for at least 10 of 12 tasks, including all required exact and worktree-comparison tasks.
- **SC-04**: In the authorization-negative acceptance matrix, 100% of denied, revoked, private, mismatched, and ambiguous-context requests reveal no out-of-scope source body, identifier, count, or relationship.
- **SC-05**: Under the documented healthy first-release acceptance profile, saved changes become visible to their authorized client with p95 ≤2 seconds over at least 100 observed batches. Separately, every catching-up, partial, offline, or unavailable response reports its lag or degradation truthfully. A degraded response cannot satisfy the latency target.
- **SC-06**: A fresh supported native installation connects at least two simultaneous standard MCP clients to one daemon and completes every UCI-1 acceptance scenario assigned to the first release through that installed path.
- **SC-07**: For at least 9 of the 12 product tasks, agents reach the recorded correct source or relationship answer while reading less source material than the recorded baseline, without reducing correctness.
- **SC-08**: No UCI-1 release claim asserts completion of a deferred UCI-2 or UCI-3 requirement; every deferred requirement remains traceable in the feature’s scope boundary and supporting contracts.

## Scope Boundaries

### In Scope — UCI-1A+B, the First Releasable Scope

- Server-resolved Source, Checkout, and View context for authorized code-intelligence requests, while preserving existing project and Space semantics.
- One daemon serving two real worktrees concurrently with separate MCP client contexts and without cross-worktree state leakage.
- Exact, full-text, and real semantic-vector retrieval of versioned saved source, with bounded evidence-labeled relationship navigation from the same View.
- Automatic update, reconciliation, recovery, currentness, and coverage visibility for saved working-copy changes.
- The first-release language and contextual-material scope defined by the adopted contracts: Go; JavaScript, TypeScript, and TSX; plus the defined structured/text minimum for Markdown, JSON/YAML, SQL, and OpenAPI.
- Native installation, two-client MCP proof, authorization-negative proof, and measured acceptance against the approved product-task set.

### Deferred — Retained Requirements, Not Removed Scope

- **UCI-2**: Broader language and contextual-material support, richer navigation and analysis, cross-source behavior, and the future read-only Code UI. The Code UI is surface work and requires a separate accepted Spec Kit feature under Constitution Principle XII.
- **UCI-3**: Progressive migration of the remaining Engram domain addresses to Space and typed source references, plus legacy code-index retirement after the specified migration and rollback evidence.
- Full support for C# and Python resolution, expansive document enrichment, community analytics, and media adapters remain later capability work; they are not implied by a UCI-1 success claim.

### Out of Scope for This Admission and UCI-1

- Editing application source, changing roles or leases, production deployment, release publication, or authorization-policy expansion.
- Unsaved editor-buffer indexing, arbitrary source-path scanning, source-script execution, or automatic ingestion of private material outside approved scope.
- A new operator working surface, a second code-intelligence product, or a claim of literal compatibility with upstream tools without its own contract evidence.

## Assumptions

- A coding agent normally supplies a trustworthy current working directory through its MCP client context; otherwise it can make an explicit authorized selection.
- UCI-1 indexes saved working-copy content; unsaved editor buffers remain outside this release scope.
- The operator can provide a supported native environment with at least two real Git worktrees and existing authorization for the acceptance fixture.
- A real approved semantic provider is available when the semantic acceptance case is exercised. Structural and lexical capability may degrade honestly when it is not available, but that does not satisfy FR-07.
- Existing project and Space records retain their present product meanings while code intelligence introduces the more specific Source, Checkout, and View context.
- Detailed behavior, limits, data handling, migration, and acceptance evidence are resolved by the adopted supporting contracts; no additional product decision is needed for this specify-stage admission.

## Dependencies

- Engram Constitution 1.1.0, especially its requirements for server-resolved typed context, View-pinned retrieval, preserved project/Space semantics, and separate accepted surface work.
- The existing authorized Engram server, daemon, MCP integration boundary, and native Git worktree environment.
- The adopted identity, storage, retrieval/graph, API, migration, and acceptance contracts under this feature directory.
- The machine-readable response schema, examples, acceptance scenarios, and document-validation artifact adopted with this feature.
- A curated, authorized acceptance corpus and recorded twelve-task baseline suitable for the first-release value measurement.

## Supporting Contract Boundary

This product specification deliberately states user outcomes, scope, and acceptance intent rather than implementation design. The following adopted artifacts are the working technical contracts for later planning and implementation:

- `supporting-contracts/identity-and-worktrees.md`
- `supporting-contracts/storage-and-indexing.md`
- `supporting-contracts/retrieval-and-graph.md`
- `supporting-contracts/api-contracts.md`
- `supporting-contracts/migration.md`
- `acceptance/uci-acceptance.md`
- `contracts/query-response.schema.json`
- `contracts/examples.json`
- `acceptance/scenarios.json`
- `acceptance/document-validation.json`

All material product questions covered by the intake are resolved by these contracts. Remaining clarification topics: none.
