# Behavioral Signal Declarations — OPS-RECOVERY-DATA-R1

Phase: 0 (Behavior-Confirming Tester)
Task: OPS-RECOVERY-DATA-R1
Anchor: `.agent/plans/2026-07-10-engram-production-ready-master-plan.md:155,500,540`
Generated: 2026-07-13

## User Behaviors in Scope

- UB-1: An operator can restore a supported Engram backup into a clean PostgreSQL 17 instance and users can still recall the same memories, rules, credentials, issues, documents, and indexed-code records.
- UB-2: An operator receives a failing result before unsafe or incomplete recovery can be mistaken for success, and a retry after cleanup succeeds without residue.

## Test Declarations

### TEST-001

Test ID: `tests/critical/recovery/postgres_backup_restore_test.go:TestOperatorCanRestorePostgresBackup_RecoversDurableEngramDataAndRejectsUnsafeRestores`
User behavior: UB-1 and UB-2
Declaration type: A (Full Signal)
Signal name: user-task-completion-rate
Measurement window: one complete release-candidate recovery run
Target delta: 100% of required restored entity and negative-safety assertions pass
Measurement method: the critical test drives isolated PostgreSQL 17 source and target containers, restores real archives, queries restored application state, decrypts the restored credential with the original key, and verifies each unsafe-input scenario returns non-zero without a success marker or residual Docker resources
Evidence source: master-plan RECOVERY-DATA row and M7 backup/restore customer-readback gate
Critical suite: YES — `// @critical` annotation and `critical` build tag are present
Rename required: NO
AP violations: none

## Critical Suite Gap Analysis

- PostgreSQL logical recovery: COVERED by TEST-001 after GREEN; happy path, failure, retry, idempotency, and cleanup are part of the same real-container user flow.

## Phase 0 Exit Status

Tests in scope: 1
User-facing tests: 1
- With full signal (A): 1
- CODE-CONTRACT-ONLY (B): 0
- PROXY (C): 0
- Undeclared: 0
Non-user-facing tests: 0
Critical-suite gaps: 0
Rename-required flags: 0
AP violations detected: none

Behavioral verification tally:
- BEHAVIOR_VERIFIED: 1 feature
- CODE_PATH_COVERED: 0 features
- BEHAVIOR_UNCONFIRMED: 0 features

Exit: PASS
