# Behavioral Signal Declarations — SECURITY-PROJECT-IDENTITY-R4

Phase: 0 (Behavior-Confirming Tester)

Task: SECURITY-PROJECT-IDENTITY-R4

Anchor: explicit R4 maker contract plus immutable R2/R3 checker findings; no feature-local `spec.md` or `user_job_statement.md` exists
Generated: 2026-07-11T00:44:39.6108161+03:00

## Scope classification

The change adds permanent process-boundary regression coverage for an already
implemented filesystem publication algorithm. It does not add or modify a
user-facing feature. The orchestrator's R4 contract explicitly limits this
revision to test and evidence files unless a new product defect is proven.

Phase 0 classification: `CODE_PATH_COVERED`.

## Test declarations

### TEST-001

Test ID: `internal/proxy/identity_process_test.go:TestResolveProjectIdentityV2_ChildProcessPublicationContract`

Tag: `CODE-CONTRACT-ONLY`

Justification: proves that independent Go processes cannot observe or create a partial project-identity anchor and cannot repair an existing invalid anchor.

Behavioral gap: none claimed; customer-mode identity behavior remains owned by the existing production-readiness and critical-suite flows.

Rename required: no

AP violations: none

### TEST-002

Test ID: `internal/proxy/identity_process_test.go:TestResolveProjectIdentityV2_ProcessHelper`

Tag: `CODE-CONTRACT-ONLY`

Justification: provides the isolated child-process endpoint used only by TEST-001; it makes no independent behavioral claim.

Behavioral gap: none; this helper is load-bearing test infrastructure for TEST-001.

Rename required: no

AP violations: none

## Critical-suite gap analysis

This task changes no user-facing feature, so it creates no new critical-suite
obligation. The existing `tests/critical/` inventory contains only the auth
two-tier user flow and does not duplicate this internal process-publication
contract.

## Phase 0 exit status

Tests in scope: 2

User-facing tests: 0

Non-user-facing tests: 2

Missing declarations: 0

Critical-suite gaps introduced: 0

Rename flags: 0

AP violations detected: none

Behavioral verification tally: `CODE_PATH_COVERED` (1 internal contract)

Exit: PASS
