# UCI admission checklist: Unified Code Intelligence

**Purpose**: Review the completeness, clarity, consistency, and measurability of UCI-1A+B requirements before task generation.
**Created**: 2026-09-05
**Feature**: [Unified Code Intelligence](../spec.md)
**Approved specification commit**: `c3d41d2575847d8e4e4313cc0a55acfbb1419327`
**Reviewed plan commit**: `489f887d7e0ff25a8a6374a927c5f6dd2eb197e3`
**Constitution**: 1.1.0
**Reviewer**: UCIPlanArchitectReview

**Note**: Prepared from the installed `/speckit.checklist` template and requirements-quality rules. The parent explicitly requested reviewer evaluation of the items. No command was run or repository file written during this review.
**Review Ownership**: This checklist is a reviewer-owned requirements-quality review artifact. An item is marked `[x]` only where the reviewer found its requirements-quality criterion satisfied by the named specification, plan, or adopted contracts.
**Marker Semantics**: `[x]` means the written requirements have been reviewed and satisfy the criterion. It does not mean implementation, testing, installation, or release is complete.

## Requirement completeness

- [x] CHK001 Are the distinct roles of Source, Checkout, View, and optional Space grouping defined, with paths, branches, remotes, labels, and legacy identifiers excluded as code authority? [Completeness; Spec §FR-01, §FR-11; Plan §Constitution Check; Data model §Canonical Context Entities]
- [x] CHK002 Are isolation requirements explicit for two simultaneous clients using real dirty worktrees of the same Source, including third-client connection, context changes, and ambiguous selection without a trustworthy working directory? [Coverage; Spec §FR-02, §FR-03, §SC-01; Plan §First RED/GREEN Proof; Identity §Несколько агентов и один общий daemon]
- [x] CHK003 Does the first-release scope distinguish exact name, qualified-symbol, and path lookup from full-text and real semantic retrieval, while naming the required bounded relationship questions? [Completeness; Spec §FR-05 through §FR-08; Plan §Summary; Retrieval §Search pipeline UCI-1, §Графовые операции]
- [x] CHK004 Is the mandatory Markdown, JSON/YAML, SQL, and OpenAPI minimum specified independently of broader document enrichment and assigned to an implementation checkpoint? [Completeness, Ownership; Spec §In Scope — UCI-1A+B; Plan §Ordered Execution Slices, slice 3; Quickstart §Focused Implementation Gates]

## Requirement clarity and consistency

- [x] CHK005 Are publication requirements precise about complete manifests, expected parent, lease epoch, atomic visibility, and the distinction between a valid empty corpus and an incomplete or failed scan? [Clarity; Spec §FR-04, §FR-09, §FR-10; Plan §Ordered Execution Slices, slice 2; Data model §Publication Transaction and Consistency; Storage §Полнота и empty index]
- [x] CHK006 Do search and graph requirements consistently bind evidence to the same selected View, with explicit traversal limits and non-conclusive outcomes for partial, ambiguous, or capped exploration? [Consistency; Spec §FR-04, §FR-08, §SC-02; Plan §Ordered Execution Slices, slice 3; Retrieval §Графовые операции]
- [x] CHK007 Is semantic success defined as a provider-generated result rather than lexical fallback, with explicit degradation and compatibility rules for embedding profiles and changed source versions? [Clarity; Spec §FR-07, §FR-10; Plan §Ordered Execution Slices, slice 5; Research §R-08; Data model §Semantic and Durable Work Entities]
- [x] CHK008 Are current, historical, stale, partial, unsupported, offline, unavailable, and empty outcomes distinguished without promising an atomic snapshot of a changing filesystem? [Clarity; Spec §FR-10; API §Свежесть и наблюдение, §Ошибки; Storage §Что такое опубликованный view]

## Exception, recovery, and edge-case coverage

- [x] CHK009 Are recovery requirements defined for saved changes, deletion, rename, branch transition, watcher overflow, restart, and offline reconnection, including preservation of the unaffected worktree? [Coverage; Spec §FR-09; Plan §Ordered Execution Slices, slice 7; Storage §Потеря событий, restart и offline]
- [x] CHK010 Are durable work ownership, idempotency, retry, terminal failure, and lost-acknowledgement behavior specified rather than left to timers or process-local state? [Completeness, Recovery; Spec §FR-09; Constitution §V; Plan §Constitution Check; Data model §Job and Lease, §Publication Transaction and Consistency]
- [x] CHK011 Are linked-worktree discovery and identity edge cases documented for detached or unborn HEAD, moves, copies, recreated paths, and Windows path or line-ending differences? [Edge Case Coverage; Spec §Edge Cases; Plan §Target Platform; Identity §Discovery: обычный Git и linked worktree, §Перемещение и пересоздание каталогов; Acceptance §Матрица источников и Git]

## Security requirements

- [x] CHK012 Are source-admission boundaries specified for approved roots, nested repositories, submodules, inaccessible descendants, and symlink or reparse-point escapes, without treating uncertain scans as deletion? [Coverage; Spec §FR-11, §Edge Cases; Data model §Publication Transaction and Consistency; Identity §Monorepo, submodule, nested repository; Storage §Полнота и empty index]
- [x] CHK013 Do authorization requirements cover denied, revoked, private, and mismatched contexts across reads, search, graph traversal, caches, pagination, and historical Views, with a non-disclosing refusal criterion? [Completeness, Measurability; Spec §FR-01, §FR-11, §SC-04; Data model §Context Resolution and Request Invariant, §Retention and Privacy; Acceptance §Безопасность]
- [x] CHK014 Are protections for untrusted source, parser input, secrets, and provider access explicit, including resource bounds, credential custody, exclusion policy, and the prohibition on source-controlled execution or external fetches? [Clarity, Coverage; Spec §FR-11, §Out of Scope for This Admission and UCI-1; Plan §Constraints, §Primary Dependencies; Research §R-05; Retrieval §ADR-PARSE-01; Acceptance §Безопасность]

## Migration and rollback requirements

- [x] CHK015 Does the plan preserve migration 170 and require a current registry recheck before allocating additive forward migrations, with the adopted migration contract defining the bounded census and data-preservation obligations? [Consistency; Spec §FR-11; Plan §Migration and Cutover Plan; Research §R-01; Migration §MIG-0, §MIG-1; Constitution §X]
- [x] CHK016 Are compatibility references and legacy code-index reads defined without guessed Checkout/View provenance, implicit main/latest selection, or authorization widening? [Clarity; Spec §FR-01, §FR-11; Plan §Migration and Cutover Plan; Migration §MIG-2, §MIG-3; Data model §Typed alias, §Migration and Rollback Semantics]
- [x] CHK017 Are rollback and contraction requirements separated, with backup/restore evidence, prior-binary compatibility, observation, and sunset conditions specified rather than assuming that reverting a binary restores authority data? [Completeness, Recovery; Spec §FR-11; Plan §Migration and Cutover Plan, §Single Release Map; Migration §Rollback, §MIG-5; Constitution §Delivery and Evidence Gates]

## Dependencies and ownership

- [x] CHK018 Are first-RED prerequisites separated from later provider, parser, and installed-artifact requirements, with the expected failure tied to worktree isolation rather than disabled tooling, missing APIs, or unfinished indexing? [Dependency Clarity; Spec §FR-02, §SC-01; Plan §First RED/GREEN Proof, §Ordered Execution Slices, slice 0; Quickstart §Slice-0 RED prerequisites, §Later-slice prerequisites, §RED Evidence]
- [x] CHK019 Does the plan assign production of the Windows-safe disposable installation and standard-MCP-client harness before final acceptance, while expressly rejecting package tests or direct service calls as installed proof? [Ownership, Acceptance Clarity; Spec §FR-12, §SC-06; Plan §Ordered Execution Slices, slices 4 and 8; Quickstart §Native Installed Two-Client Gate]
- [x] CHK020 Are parser dependency revisions, license evidence, supported build targets, and bundle identity requirements documented before language support can be claimed, without introducing a new network service? [Dependency Coverage; Spec §In Scope — UCI-1A+B; Plan §Primary Dependencies, §Target Platform, §Ordered Execution Slices, slice 6; Research §R-05; Constitution §Architecture and Data Constraints]

## Acceptance criteria and deferred boundaries

- [x] CHK021 Are performance criteria tied to a documented corpus, workload, healthy operating profile, sample count, and percentile, with degraded results excluded from healthy-path success? [Measurability; Spec §SC-05; Plan §Performance Goals, §Scale/Scope; Acceptance §Начальные SLO и их границы; Quickstart §Semantic, Watcher, and Recovery Gate]
- [x] CHK022 Are the twelve product tasks, baseline timing, correct-source evidence, top-five target, and reduced-reading target defined before tuning, without substituting tool availability for measured developer value? [Acceptance Criteria Quality; Spec §SC-03, §SC-07; Acceptance §Продуктовые задачи; Plan §Single Release Map; Quickstart §Release Evidence Gate]
- [x] CHK023 Are UCI-1A+B defined as one releasable outcome, while UCI-2/3 remain traceable deferred requirements and Code UI requires a separate accepted feature rather than becoming a UCI-1 prerequisite? [Scope Consistency; Spec §FR-12, §SC-08, §Scope Boundaries; Plan §Single Release Map, §Deferred Boundary; Constitution §XII]
- [x] CHK024 Are Memory R1's preserved project/Space and receipt semantics, migration-170 boundary, and required revalidation before resumption explicit, without implying that UCI-1 completes Memory R1 or its later domain migration? [Compatibility, Dependency Clarity; Spec §FR-11; Plan §Migration and Cutover Plan, §Deferred Boundary; Research §R-01; Migration §MIG-4; Constitution §Sync Impact Report]

## Notes

- Review result: 24 requirements-quality questions reviewed, 24 satisfied, and no unresolved requirements-quality findings in this bounded checklist.
- These markers do not claim that any behavior has been implemented, tested, installed, or released. They do not satisfy Memory R1's required revalidation before resumption.
- This review binds the specification and plan commits named above. Changed requirements or planning bytes require review of the affected criteria; an earlier marker does not prove successor content.
- `/speckit.implement` reads checklist checkbox state as a gate and must not modify markers. Leave an item unchecked if later review finds that it needs clarification or correction.
- `checklists/requirements.md` retains its separate built-in lifecycle under `/speckit.specify` and `/speckit.clarify`.
- Spec and Plan citations refer to [spec.md](../spec.md) and [plan.md](../plan.md). Supporting planning references are [research.md](../research.md), [data-model.md](../data-model.md), and [quickstart.md](../quickstart.md).
- Contract citations refer to [Identity](../supporting-contracts/identity-and-worktrees.md), [Storage](../supporting-contracts/storage-and-indexing.md), [Retrieval](../supporting-contracts/retrieval-and-graph.md), [API](../supporting-contracts/api-contracts.md), and [Migration](../supporting-contracts/migration.md).
- Acceptance and Constitution citations refer to [uci-acceptance.md](../acceptance/uci-acceptance.md) and [Constitution 1.1.0](../../../.specify/memory/constitution.md). The Memory R1 feature remains separately defined in [Working Agent Memory R1](../../009-working-agent-memory-r1/spec.md).
