# Behavioral Signal Declarations — MB1 DB governance and isolation

Phase: 0 (Behavior-Confirming Tester)
Task: MB1-DB-GOVERNANCE-ISOLATION
Anchor: operator directive for `MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY` plus
`macro-batch-diagnostic-freeze-20260711-1340/summary.json`
Generated: 2026-07-11

## User Behaviors in Scope

- UB-1: A workstation starting a session sees issues only for the requested
  project, even when another project has a deceptively similar identifier.
- UB-2: An operator listing documents by a literal path prefix receives only
  paths that actually begin with those literal characters.
- UB-3: A reviewed rule candidate becomes eligible again at its declared review
  time instead of remaining permanently held by an old processing run.

## Test Declarations

### TEST-001

Test ID: `tests/critical/data_isolation_test.go:TestSessionStartAndDocumentQueries_DoNotExposeSiblingProjectsOrPaths`

Signal name: user-task-completion-rate
Measurement window: one disposable PostgreSQL-backed dev-stand scenario per
adversarial project/path fixture
Target delta: zero sibling issue or document rows in every decoded response
Measurement method: capture and decode the real session-start and document-list
responses while the database contains literal underscore, percent, backslash,
hyphen-sibling, canonical-suffix, and near-prefix rows
Evidence source: UB-1 and UB-2 from the operator-owned MB1 contract
Critical suite: YES — `@critical` annotation is required

Rename required: NO
AP violations: none

### TEST-002

Test ID: `internal/db/gorm/issue_store_isolation_test.go:TestIssueStore_ListIssuesExTreatsProjectSelectorsAsLiteralIdentities`

Tag: CODE-CONTRACT-ONLY
Justification: verifies the store-level literal identity predicate and both
target/source selector branches independently of transport rendering.
Behavioral gap: TEST-001 covers the end-user session-start impact.

Rename required: NO
AP violations: none

### TEST-003

Test ID: `internal/db/gorm/versioned_document_store_test.go:TestVersionedDocumentStore_ListTreatsPathPrefixAsLiteralText`

Tag: CODE-CONTRACT-ONLY
Justification: verifies the shared SQL-LIKE escaping contract against real
PostgreSQL for every metacharacter and ordinary prefix.
Behavioral gap: TEST-001 covers the operator-visible document-list impact.

Rename required: NO
AP violations: none

### TEST-004

Test ID: `internal/db/gorm/rule_arbiter_store_test.go:TestRuleGovernanceStore_AnnotationAtomicallyReleasesMatchingClaimOnly`

Tag: CODE-CONTRACT-ONLY
Justification: verifies transaction-level claim ownership and release semantics;
the user-visible effect is eventual rule re-evaluation rather than a direct API
response.
Behavioral gap: NONE — the path has no immediate user-facing response.

Rename required: NO
AP violations: none

### TEST-005

Test ID: `internal/db/gorm/rule_governance_store_test.go:TestMigration144_RollbackAndReapplyPreservesDependentMigrationOrder`

Tag: CODE-CONTRACT-ONLY
Justification: verifies historical migration test sequencing and final schema
integrity without changing migration 144 rollback semantics.
Behavioral gap: NONE — this is upgrade/schema infrastructure.

Rename required: NO
AP violations: none

## Critical Suite Gap Analysis

- Cross-project session-start and literal document listing: COVERED by TEST-001.
- Claim release and migration sequencing: not user-facing; CODE-CONTRACT-ONLY.

## Phase 0 Exit Status

Tests in scope: 5 total
  User-facing tests: 1
    - With full signal (A): 1
    - CODE-CONTRACT-ONLY (B): 0
    - PROXY (C): 0
    - UNDECLARED: 0
  Non-user-facing tests: 4
Critical-suite gaps: 0
Rename-required flags: 0
AP violations detected: none

Behavioral verification tally:
  BEHAVIOR_VERIFIED: 2 user behaviors
  CODE_PATH_COVERED: 2 infrastructure behaviors
  BEHAVIOR_UNCONFIRMED: 0

Exit: PASS
