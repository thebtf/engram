# Feature Specification: Operator Code Console

**Feature Branch**: `ui/operator-code-console-r1`

**Created**: 2026-09-10

**Status**: Clarified, ready for planning

**Input**: Deliver the separately accepted operator working surface that makes existing Engram capability usable without inventing a second authority: an honest shell, truthful mutations, an authorized read-only Code Explorer, safe collection operations, and later daemon-owned index job control.

## Outcome and completion boundary

An operator can see what the console knows, inspect authorized code from one chosen source state, and make supported collection changes without mistaking browser state for server truth. The console shows uncertainty, denial, partial work, and unavailable ownership plainly.

The first installable slice is an honest shell. It removes eager closed-dialog and cross-domain memory loading, uses a bounded summary when one exists, and shows `unknown` instead of a truncated list length presented as a total. It does not complete this feature.

Feature completion requires the honest shell, truthful mutation outcomes, the read-only Code Explorer, and Rules-first collection operations. Later daemon job control remains a feature outcome, but an HTTP request alone never proves a workstation action occurred. The next separate feature is Book Context. Working Agent Memory R1 remains retained until Book Context reaches its own product result.

## Actors

- **Operator**: Uses the browser console to inspect a chosen code state and perform supported collection work.
- **Authenticated browser subject**: Has a persistent identity and explicit read grants. A deployment administrator role, a Space label, or a browser tab cannot substitute for a grant.
- **Source owner**: Grants access to a Source and Checkout and may revoke access before a request or continuation completes.
- **Daemon owner**: Owns the workstation that performs index work. The browser can request authorized work but cannot claim the daemon ran it.

## Clarifications

### Session 2026-09-10

- Q: How does a browser user receive code access? -> A: A persistent authenticated browser subject receives an explicit Source and Checkout read grant. The console never infers authority from an administrator role, Space membership, a path, or a legacy project identifier.
- Q: What releases an authorized code response? -> A: Every transport uses the same UCI-owned release operation. It reauthorizes immediately before the response, records non-content exposure, and suppresses contextual content if either step cannot complete.
- Q: What separates the first slice from feature completion? -> A: The first slice is the honest shell. The feature cannot complete until Code Explorer and Rules-first collection operations are accepted.

### Remaining product decisions

No unresolved product decision blocks this specification. Grant storage, response-release helper placement, rendering technology, and exact pagination limits are planning decisions. They must preserve the requirements below and cannot become an administrator bypass, a second code graph, or HTTP MCP.

## User Scenarios & Testing

### User Story 1 - Open an honest console shell (Priority: P1)

An operator opens Graph, Books, or Rules and sees a responsive shell whose counts state only what the server actually knows. Closed settings do not load unrelated domain data.

**Why this priority**: The current shell creates background requests that are unrelated to the page the operator opened. Removing that work gives every later console workflow a truthful base.

**Independent Test**: Open each page against an authorized candidate backend with closed settings. Confirm that the page works without memory-body requests or settings-owned requests. Open settings and confirm that its normal authorized load begins then.

**Acceptance Scenarios**:

1. **Given** an operator opens Graph, Books, or Rules, **When** the shell loads, **Then** it does not fetch memory bodies only to render a shell count.
2. **Given** the server has no authoritative total for a collection, **When** the shell renders a count, **Then** it shows `unknown` rather than zero or a page length as a total.
3. **Given** settings are closed, **When** the operator navigates between pages, **Then** settings-owned data does not load until the operator opens settings.

---

### User Story 2 - Trust the result of a collection change (Priority: P1)

An operator makes a supported collection change and can distinguish a verified change from a partial change, a failed request, and an outcome whose server state is still unknown.

**Why this priority**: A client-side snapshot cannot undo a server write. The operator needs an honest result before using multi-item operations.

**Independent Test**: Exercise one successful item and one rejected item in the same action. Repeat with a lost response after a commit and a failed readback. Confirm that previously committed rows remain visible, uncertain rows remain marked, and retry targets only unfinished work.

**Acceptance Scenarios**:

1. **Given** one supported item succeeds and one fails, **When** the action returns, **Then** the console reports a partial result for each item without claiming a rollback.
2. **Given** the server may have committed but the browser loses the response, **When** the operator returns to the collection, **Then** the console reports `outcome_unknown` until a status check or readback resolves it.
3. **Given** a retry is available, **When** the operator retries, **Then** the console retries only items that are not known to have succeeded.

---

### User Story 3 - Explore one authorized source state (Priority: P1)

An authorized browser subject selects a named Source, Checkout, and View, searches it, examines a bounded relationship graph or accessible relation list, and reads the exact source span that supports the result.

**Why this priority**: Code navigation is useful only when the operator can trust that search, graph, and source text describe the same authorized source state.

**Independent Test**: Use two saved worktrees and two browser tabs with different authorized choices. Search, open a relationship, and read a source span in each tab. Confirm that every result stays with the selected View and that one tab does not change the other tab or an MCP client.

**Acceptance Scenarios**:

1. **Given** the browser subject has an explicit grant, **When** the operator selects one Source and Checkout, **Then** the console offers only Views that subject may read.
2. **Given** the operator searches a pinned View, **When** the operator follows a graph result to its source, **Then** the graph fact and source span identify that same View.
3. **Given** a newer View becomes available, **When** an operator has an older View open, **Then** the console notifies the operator and does not silently replace the pinned source.
4. **Given** a grant is revoked, a continuation is mismatched, or a selector is ambiguous, **When** the operator requests data, **Then** the console reveals no source body, identifier, count, or relationship fact outside the grant.

---

### User Story 4 - Apply safe Rules-first collection operations (Priority: P1)

An operator selects Rules across pages or across a frozen filter, reviews the intended operation, and receives an item-by-item result that respects current authorization and versions. Later collection consumers use the same selection truth but retain their own allowed actions.

**Why this priority**: Rules are the first high-value collection that needs group operations. Their ordering and injection semantics make false success and partial reorder dangerous.

**Independent Test**: Use more than 200 Rules, select a page, selected identifiers across pages, and all matching a filter with exclusions. Change a version or grant after preview. Confirm that the console distinguishes these selections and that a reorder succeeds entirely or leaves the scope unchanged.

**Acceptance Scenarios**:

1. **Given** an operator selects all results for a filter, **When** the selection is created, **Then** the server freezes the authorized target set, reports its count, accepts explicit exclusions, and expires the selection without granting new authority.
2. **Given** the filter, code context, or permissions change, **When** the operator returns to the collection, **Then** the console clears or requires reconfirmation of a dangerous selection.
3. **Given** a Rule enable, disable, delete, or supported field change has stale versions or denied rows, **When** the operator applies it, **Then** the result identifies each success, conflict, denial, failure, or unknown outcome.
4. **Given** an operator reorders Rules inside one scope, **When** a version conflict occurs, **Then** the order remains unchanged rather than being partially renumbered.

---

### User Story 5 - Request daemon-owned index work honestly (Priority: P2)

An authorized operator asks the daemon owner to reindex or reconcile a selected code context and sees whether the request is queued, acknowledged, unavailable, failed, or completed with a new readable View.

**Why this priority**: A browser action cannot index a remote working copy. The console must expose owner execution without pretending that a submitted request finished the work.

**Independent Test**: Submit one authorized request while the owner is online and one while it is offline. Confirm that the first records acknowledgement and a new View only after readback, and that the second stays queued or unavailable without a false success.

**Acceptance Scenarios**:

1. **Given** the daemon owner is offline, **When** the operator requests index work, **Then** the console shows `queued` or `unavailable` and offers only an authorized retry path.
2. **Given** the daemon owner acknowledges the request, **When** the work finishes, **Then** the console identifies the resulting View and its readback state.

### Edge cases and failure states

- A Source label, Checkout label, path, branch, legacy project identifier, or Space is ambiguous. The console asks for an authorized explicit selection and reveals no candidate details that the subject cannot read.
- A search is partial, its traversal reaches a budget, or a source span is unavailable. The console labels the limitation and never presents it as proof that no dependency exists.
- A response release or exposure record cannot complete. The console receives no contextual code body or relationship facts.
- A filter token expires, a version changes, or a grant changes after preview. The console resolves the selection again and reports conflicts or denials per item.
- A network loss happens after a mutation may have committed. The console keeps the operation identity and requires status or readback before replaying a non-idempotent action.
- The daemon owner is offline, rejects a request, or produces no new View. The console shows the recorded state and does not expose workstation credentials, paths, or a fake completed result.

## Requirements

### Functional Requirements

- **FR-001**: The console MUST load an honest shell without fetching collection bodies that the current page and visible shell do not need.
- **FR-002**: The console MUST render an authoritative bounded count when available and MUST render `unknown` when no authoritative total exists. It MUST NOT present a loaded page size as a server total.
- **FR-003**: The console MUST defer settings-owned domain requests until settings opens and preserve existing keyboard, focus, and error behavior when it does.
- **FR-004**: Code browsing MUST require a persistent authenticated browser subject and an explicit read grant for the requested Source and Checkout. Administrator role, Space membership, a browser tab, a path, and a legacy project identifier MUST NOT grant code access.
- **FR-005**: The console MUST resolve every Code Explorer request to one authorized Source, Checkout, and immutable View before it searches, traverses, reads a span, or continues a result.
- **FR-006**: Search, graph, and exact-source results shown together MUST identify the same View. Browser tabs and MCP clients MUST retain independent contexts.
- **FR-007**: Before returning a contextual code response, the shared UCI release operation MUST reauthorize the request and record non-content exposure. If either action fails, the response MUST not contain contextual code or relationship facts.
- **FR-008**: The console MUST remain a read-only view of code-derived facts. It MUST NOT create or edit AST facts, become a second code graph, bypass UCI through direct storage access, expose daemon credentials, or add HTTP MCP.
- **FR-009**: The primary Code Explorer workflow MUST show query, graph or relation-list, exact source, provenance, confidence, and limitation states. It MUST keep legacy graph data distinct from code-derived facts.
- **FR-010**: Collection selection MUST distinguish no selection, explicit typed identifiers, current page, and all results for one frozen server filter with explicit exclusions.
- **FR-011**: An all-filter selection MUST bind the authorized target set, query fingerprint, count, expiry, and expected versions. Changing filter, context, or permissions MUST clear or require reconfirmation of dangerous selection.
- **FR-012**: Rules MUST be the first collection operation consumer. The console MUST support only authorized enable, disable, delete, and supported field changes by selection. Rule reorder MUST be atomic within one scope and version-checked.
- **FR-013**: Each mutation result MUST distinguish `committed_verified`, `committed_verification_pending`, `partial`, `failed`, and `outcome_unknown`, with an item-level result when more than one item was targeted.
- **FR-014**: The client MUST NOT describe a local snapshot reset as a server rollback. It MUST retain operation identity for uncertain work and retry only targets not known to have succeeded.
- **FR-015**: Later collection consumers MAY reuse the selection and result contracts only for their own supported action matrices. The feature MUST NOT add bulk secret reveal, arbitrary manual graph edits, or unsupported bulk actions for symmetry.
- **FR-016**: An authorized index request MUST create durable owner-visible work with acknowledgement, retry, terminal state, and resulting View readback. HTTP acceptance alone MUST NOT mean the workstation completed the work.
- **FR-017**: The browser MUST NOT receive a workstation secret or directly access a remote working-copy path to control indexing.
- **FR-018**: Feature 011 MUST preserve UCI authority, evidence, and release boundaries. It MUST NOT reopen UCI technical acceptance, core recovery outcomes, or Sonar policy.
- **FR-019**: Feature 011 MUST finish its Code Explorer and Rules-first collection outcome before the separate Book Context feature. Working Agent Memory R1 remains retained until Book Context reaches its own product result.

### Key entities

- **Browser subject**: A persistent authenticated user identity that receives grants and owns browser requests.
- **Read grant**: Explicit permission for one browser subject to read a Source and Checkout. It does not grant access through labels, roles, or context aliases.
- **Code context**: One authorized Source, Checkout, and immutable View used for a Code Explorer request.
- **Pinned View**: The immutable source state identified by the open Code Explorer result. A newer View is a separate choice.
- **Selection**: No items, explicit typed identifiers, one page, or a frozen all-filter target set with exclusions and expiry.
- **Mutation result**: The durable truth of one requested change, including each target outcome and any required readback.
- **Index work request**: A durable request that an authorized daemon owner may acknowledge, execute, retry, or report as unavailable.

## Success Criteria

### Measurable outcomes

- **SC-001**: Against an authorized candidate backend, each fresh Graph, Books, and Rules load makes zero memory-body requests for shell counts. With settings closed, each makes zero settings-owned domain requests.
- **SC-002**: In a two-worktree, two-tab, concurrent-MCP fixture, 100% of successful Code Explorer search, graph, and source-span results identify the selected View. Zero results cross a browser-tab or MCP context boundary.
- **SC-003**: In the same fixture, 100% of denied, revoked, ambiguous, expired, or continuation-mismatched Code Explorer requests reveal no unauthorized source body, identifier, count, or relationship fact.
- **SC-004**: With more than 200 Rules and more than 100 Issues, the console distinguishes page, explicit-ID, and frozen all-filter selection with exclusions. A changed permission or version after preview produces per-item denial or conflict rather than a false full success.
- **SC-005**: In a mixed mutation and lost-response scenario, every committed item remains visible after readback, every uncertain item remains `outcome_unknown` until resolved, and retry submits no known-success item again.
- **SC-006**: A Rule reorder either updates every Rule in its declared scope or leaves that scope unchanged.
- **SC-007**: For an online daemon-owner fixture, a completed index request identifies a new readable View. For an offline fixture, the console reports `queued` or `unavailable` and never reports completion without owner acknowledgement and readback.
- **SC-008**: Each advertised capability has retained proof from trigger through browser request, registered handler, domain action, and observed effect or readback. Mock-only browser tests are labeled interaction evidence, not connection proof.

## Scope boundaries

### In scope

- Honest shell loading, bounded or unknown counts, and lazy settings-owned data.
- Truthful mutation and readback results for every caller that adopts the shared mutation contract.
- Authorized read-only Code Explorer context selection, search, graph or relation-list navigation, and exact source inspection from one View.
- Rules-first safe collection selection and operations, with later domain-specific consumers only where their action matrices are accepted.
- Authorized durable requests for daemon-owned reindex or reconcile work and their truthful status.

### Out of scope

- UCI core repair, UCI technical reacceptance, a second code index or graph, direct storage bypass, browser administrator inference, HTTP MCP, or daemon credential delivery.
- Manual editing of system-derived code facts, deletion of legacy graph data, unsaved-editor indexing, and arbitrary source-path scanning.
- Book catalog, editions, source reader, extraction, concepts, attachments, or agent book application. Those form the next separate Book Context feature.
- Further Working Agent Memory R1 implementation, cancellation, or reprioritization.
- Production deployment, release publication, secret access, destructive migration, or Book backend work.

## Ownership boundaries

- **Feature 011 console owner** owns the browser journeys, honest shell, context presentation, selection behavior, and result presentation.
- **UCI owner** owns Source, Checkout, View, query, graph, exact-source authority, reauthorization, and exposure release. The console only presents that application outcome.
- **Auth owner** owns browser-subject identity and explicit Source and Checkout read grants. The console cannot add an implied administrator or Space-based grant.
- **Collection domain owners** own allowed operations, validation, versions, audit, and readback for Rules, Issues, Memory, Queue, Documents, Books, Access, and Secrets. A shared selection component does not enlarge any domain's authority.
- **Daemon owner** owns index execution and actual View publication. The console owns the authorized request and displayed state only.
- **Book Context owner** owns the next catalog-to-application feature. **Working Agent Memory R1 owner** resumes only after the accepted Book Context result.

## Assumptions

- The existing UCI application remains the only authority for code context and code-derived facts.
- Operators use a supported browser with a persistent authenticated session. A denied or absent grant is a normal failure state.
- Existing Pages, documents, access controls, secrets, localization, and legacy graph records remain unless a separately accepted change says otherwise.
- The installed UCI technical acceptance recorded as `PASS_WITH_EXPLICIT_SONAR_WAIVER` permits this separately accepted feature under Constitution 2.0.0. It does not waive release evidence or Sonar requirements for a later release.

## Dependencies

- Engram Constitution 2.0.0, especially Principles III, VII, XI, XII, and XIII.
- The UCI technical acceptance boundary and its Source, Checkout, View, authorization, evidence, and release contracts.
- The Web UI recovery packet at `.agent/intake/engram-webui-recovery-2026-09-09-r1/`.
- The separate future Book Context feature, which follows Feature 011, and the retained Working Agent Memory R1 feature, which follows Book Context.
