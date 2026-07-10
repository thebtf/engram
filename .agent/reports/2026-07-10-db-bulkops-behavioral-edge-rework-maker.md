# DB-BULKOPS behavioral-edge rework — maker report

Status: **READY_FOR_INDEPENDENT_CHECK**

This is a maker handoff, not a PASS verdict and not authorization to integrate.
The formal checker must be a fresh agent that did not implement the change or
participate in the maker-side adversarial review.

## Contract and result

Base: `68b2ce5835c7c6efdf1c68da9eedcb8d9c3837ef`

The rework closes two validation classes while preserving the prior H1/M1/M2
behavior:

1. Every public candidate-review `WithSnapshot` seam — promote, preserve,
   reject, suppress, and supersede — now uses one fail-closed binding validator.
   Structural validation runs before DB access for safe legacy preflight, then
   repeats inside the mutation transaction. The transaction acquires
   `SELECT ... FOR UPDATE` on the authoritative candidate before creating the
   snapshot or mutating anything. The validator binds the snapshot/store/audit
   dependencies, op type, operation/action/candidate parameters, normalized
   actor, empty initial affected IDs, exact one-entry initial `BeforeState`,
   candidate key, payload ID/source session, and the complete authoritative
   rollback payload. Only sub-microsecond timestamp differences introduced by
   PostgreSQL storage precision are tolerated. Invalid input produces zero
   candidate, memory, snapshot, or `candidate_review` audit writes. Valid input
   writes exactly one synchronous `candidate_review` audit.
2. `bulk_promote`, `bulk_delete`, and `bulk_supersede` now parse raw JSON
   rather than using lossy `any`/`float64` coercion. Integral JSON forms such
   as `1.0` and `1e0` remain valid; `9007199254740993`, `MinInt64`, and
   `MaxInt64` remain exact. Fractional, overflow, string, boolean, object,
   nested-array, null, malformed, and non-object inputs fail before the facade.
   A present `dry_run` must be a JSON boolean. Missing `dry_run` defaults to
   false. Zero removal, deduplication, and sorting occur only after exact parse.

## Changed-path inventory and source hashes

Only the four authorized product/test paths changed:

| Path | SHA-256 |
|---|---|
| `internal/db/gorm/candidate_store.go` | `e5102fc82df34cc85e2039cf62ce96155b725d3728518a196476eb2351b458c2` |
| `internal/db/gorm/candidate_store_test.go` | `8fb5258f01557ca7991b5a211f51107e69e4acd5740c5b254e63308f8969e4ff` |
| `internal/mcp/tools_bulkops.go` | `89ca7932ccafe7fa52c79099d6ecbca20a04aa7faff94ff5ec92c9d68dc7cefa` |
| `internal/mcp/tools_dryrun_test.go` | `2a4fbe5e29548b3df2eb45f16ab8e08e7938e9e95a8604e4a6f7cfeac05d0a90` |

The evidence namespace and this report are the only other changed paths. The
complete file-hash ledger is
`.agent/reports/evidence/production-ready/db-bulkops-behavioral-edge-rework/SHA256SUMS.txt`.

## Cause-closure reasoning

The original candidate-review bug was not one missing nil check. Each public
seam had enough local behavior to look correct while accepting a snapshot that
was wrong in a different dimension. The closed invariant is therefore evaluated
centrally and twice: a pure structural preflight, followed by an authoritative
transaction-bound check before writes.

The maker-side adversarial reviewer found one additional class hole after the
first GREEN: the snapshot payload and `snapshot.SourceSessionID` could be forged
together and remain internally consistent. That review returned
`REVISE/BLOCK`. A new sibling matrix case proved the bypass on all five seams
in `17-review-red-authoritative-binding.log`. The final implementation locks
and compares the authoritative candidate before snapshot creation or mutation.
The maker-side review is hardening evidence only; it does not substitute for the
fresh formal checker.

The original bulk-input bug likewise was not a single bad example. Once input
was decoded into `any`, JSON numbers had already crossed a lossy `float64`
boundary and wrong types could be coerced. The fix parses each raw array member
as an exact rational number, requires an integral int64 result, and parses
`dry_run` from raw JSON tokens.

## TDD and verification evidence

Every Go test/vet run below used a fresh exact PostgreSQL database named under
`engram_mkr_bedge_*`. The harness records base/head, the exact command, UTC
timestamps, exit code, active sessions before termination, and post-drop
database/session residue.

### RED and Prove-It

| Evidence | Exact command | Exit | Result |
|---|---|---:|---|
| `01-red-behavior.log` | `go test -p=1 ./internal/db/gorm ./internal/mcp -run ^(TestCandidateStore_AllCandidateReviewSnapshotSeamsRejectInvalidBindingsWithoutWrites|TestCandidateStore_AllCandidateReviewSnapshotSeamsCommitExactlyOneAudit|TestBulkOps_PublicDispatchRejectsInvalidStructuredInputs|TestBulkOps_PublicDispatchPreservesExactIntegralIDsBeforeNormalization)$ -count=1 -v` | 1 | Invalid snapshots and lossy structured inputs were accepted. |
| `02-red-spy-seam.log` | `go test -p=1 ./internal/mcp -run ^(TestBulkOps_InvalidStructuredInputsDoNotInvokeWiredFacade|TestBulkOps_WiredFacadeReceivesExactNormalizedIDsAndStrictDryRun)$ -count=1 -v` | 1 | The pre-facade assertion seam did not yet exist. |
| `05-prove-it-candidate.log` | `go test -p=1 ./internal/db/gorm -run ^(TestCandidateStore_AllCandidateReviewSnapshotSeamsRejectInvalidBindingsWithoutWrites|TestCandidateStore_AllCandidateReviewSnapshotSeamsCommitExactlyOneAudit)$ -count=1` | 1 | Temporarily bypassing the validator broke the behavior tests. |
| `06-prove-it-parser.log` | `go test -p=1 ./internal/mcp -run ^(TestBulkOps_PublicDispatchRejectsInvalidStructuredInputs|TestBulkOps_PublicDispatchPreservesExactIntegralIDsBeforeNormalization|TestBulkOps_InvalidStructuredInputsDoNotInvokeWiredFacade|TestBulkOps_WiredFacadeReceivesExactNormalizedIDsAndStrictDryRun)$ -count=1` | 1 | Temporarily bypassing the parser broke the behavior tests. |
| `17-review-red-authoritative-binding.log` | `go test -p=1 ./internal/db/gorm -run ^TestCandidateStore_AllCandidateReviewSnapshotSeamsRejectInvalidBindingsWithoutWrites$ -count=1` | 1 | Forged payload + matching forged source session was accepted by all five seams. |

All RED and Prove-It databases finished with database/session residue `0/0`.

### Final GREEN gates

| Evidence | Exact command | Exit |
|---|---|---:|
| `19-review-green-authoritative-binding.log` | `go test -p=1 ./internal/db/gorm ./internal/mcp -run ^(TestCandidateStore_PromoteWithMemoryAndSnapshot_AmendFailureRollsBackPromotion|TestCandidateStore_PreserveWithMemoryAndSnapshot_RequiresCandidateReviewSnapshotBeforeMutation|TestCandidateStore_AllCandidateReviewSnapshotSeamsRejectInvalidBindingsWithoutWrites|TestCandidateStore_AllCandidateReviewSnapshotSeamsCommitExactlyOneAudit|TestBulkOps_PublicDispatchRejectsInvalidStructuredInputs|TestBulkOps_PublicDispatchPreservesExactIntegralIDsBeforeNormalization|TestBulkOps_InvalidStructuredInputsDoNotInvokeWiredFacade|TestBulkOps_WiredFacadeReceivesExactNormalizedIDsAndStrictDryRun)$ -count=1` | 0 |
| `20-review-repeat20.log` | `go test -p=1 ./internal/db/gorm ./internal/mcp -run ^(TestCandidateStore_AllCandidateReviewSnapshotSeamsRejectInvalidBindingsWithoutWrites|TestCandidateStore_AllCandidateReviewSnapshotSeamsCommitExactlyOneAudit|TestBulkOps_PublicDispatchRejectsInvalidStructuredInputs|TestBulkOps_PublicDispatchPreservesExactIntegralIDsBeforeNormalization|TestBulkOps_InvalidStructuredInputsDoNotInvokeWiredFacade|TestBulkOps_WiredFacadeReceivesExactNormalizedIDsAndStrictDryRun)$ -count=20` | 0 |
| `21-review-race-focused.log` | `go test -race -p=1 ./internal/db/gorm ./internal/mcp -run ^(TestCandidateStore_PromoteWithMemoryAndSnapshot_AmendFailureRollsBackPromotion|TestCandidateStore_PreserveWithMemoryAndSnapshot_RequiresCandidateReviewSnapshotBeforeMutation|TestCandidateStore_AllCandidateReviewSnapshotSeamsRejectInvalidBindingsWithoutWrites|TestCandidateStore_AllCandidateReviewSnapshotSeamsCommitExactlyOneAudit|TestBulkOps_PublicDispatchRejectsInvalidStructuredInputs|TestBulkOps_PublicDispatchPreservesExactIntegralIDsBeforeNormalization|TestBulkOps_InvalidStructuredInputsDoNotInvokeWiredFacade|TestBulkOps_WiredFacadeReceivesExactNormalizedIDsAndStrictDryRun)$ -count=1` | 0 |
| `22-review-vet.log` | `go vet ./internal/db/gorm ./internal/mcp` | 0 |
| `23-review-coverage.log` | `go test -p=1 ./internal/db/gorm ./internal/mcp -run ^(TestCandidateStore_PromoteWithMemoryAndSnapshot_AmendFailureRollsBackPromotion\|TestCandidateStore_PreserveWithMemoryAndSnapshot_RequiresCandidateReviewSnapshotBeforeMutation\|TestCandidateStore_AllCandidateReviewSnapshotSeamsRejectInvalidBindingsWithoutWrites\|TestCandidateStore_AllCandidateReviewSnapshotSeamsCommitExactlyOneAudit\|TestBulkOps_PublicDispatchRejectsInvalidStructuredInputs\|TestBulkOps_PublicDispatchPreservesExactIntegralIDsBeforeNormalization\|TestBulkOps_InvalidStructuredInputsDoNotInvokeWiredFacade\|TestBulkOps_WiredFacadeReceivesExactNormalizedIDsAndStrictDryRun)$ -count=1 -covermode=atomic -coverprofile=.agent/reports/evidence/production-ready/db-bulkops-behavioral-edge-rework/coverage.out` | 0 |
| `25-review-cover-functions.log` | `go tool cover -func=.agent/reports/evidence/production-ready/db-bulkops-behavioral-edge-rework/coverage.out` | 0 |

All final GREEN databases finished with database/session residue `0/0`.

Load-bearing coverage from the final profile:

- `validateCandidateReviewSnapshotBinding`: 87.0%
- `candidateReviewPayloadMatchesAuthoritative`: 75.0%
- `promoteWithMemoryAndSnapshotAction`: 80.6%
- `transitionWithSnapshot`: 63.6%
- `parseBulkStructuredArgs`: 100.0%
- each of `handleBulkPromote`, `handleBulkDelete`, and
  `handleBulkSupersede`: 80.0%

The package percentages are 15.4% for `internal/db/gorm` and 1.8% for
`internal/mcp`; these packages are broad, so the function-level evidence is
the load-bearing measure.

### Full changed-package gates

`26-review-full-gorm.log` ran:

```
go test -p=1 ./internal/db/gorm -count=1
```

It exited 1 with the same six unrelated governance/migration baseline failures
documented by the predecessor sibling-rework report:

- `TestRuleGovernanceStore_AnnotatedCandidateWaitsUntilReviewAfter`
- `TestRuleGovernanceStore_GetLifecycleHealthAggregatesGovernanceTables`
- `TestRuleGovernanceStore_GetLifecycleHealthOmitsGlobalArbiterRunsForProjectScopedReads`
- `TestMigration144_RuleGovernanceRollbackAndReapply`
- `TestMigration144_RuleGovernanceEscapeConstraints`
- `TestMigration144_RuleGovernanceSnapshotStatusesAcceptExtendedStates`

The run later hit PostgreSQL `FATAL: sorry, too many clients already`, producing
four secondary failures in TemporalTruth, TokenStore, and TranscriptStore. No
candidate-review test failed. Cleanup still finished `0/0`.

`27-review-full-mcp.log` ran:

```
go test -p=1 ./internal/mcp -count=1
```

It exited 1 only for the two documented unrelated baseline failures:

- `TestHybridTG3_ConfidenceMin_FloorEnforced_T022`
- `TestEC_F1_TagDerivedBackfill_T007`

No bulk structured-input test failed. Cleanup finished `0/0`. These full
package commands remain WARN/FAIL baseline; this report claims only the scoped
behavioral gates green.

## Discrepancy ledger

Nothing was patched silently:

1. `08-full-packages.log` exposed an old amend-rollback fixture that constructed
   a now-invalid snapshot, plus a preserve error-string compatibility mismatch.
   The fixture now uses the canonical reviewpacket snapshot and the historical
   `candidate_review` wording is preserved. `09-legacy-compat.log` passed.
2. The first authoritative-row fix in
   `18-review-green-authoritative-binding.log` compared raw timestamp JSON
   exactly and moved all validation inside the transaction. That rejected a
   legitimate sub-microsecond timestamp representation and caused the legacy
   nil-DB preflight test to panic. The final form restores structural preflight,
   repeats validation inside the transaction, compares timestamps only within
   PostgreSQL precision, and compares every other candidate field exactly.
   Logs 19, 20, and 21 passed.
3. `24-review-cover-functions.log` failed with exit 2 because PowerShell split
   `-func=<path>` into two arguments. The command was rerun through an explicit
   `GoArgs` array; `25-review-cover-functions.log` exited 0.
4. Fresh migrations continue to emit the pre-existing non-fatal stale
   pattern/relation index, absent `observation_vectors`, and unavailable
   `vectorscale` warnings.
5. Captured Go/GORM logs contained trailing spaces and tab-indented test output
   that made the staged `git diff --check` fail despite a clean source diff.
   Before hashing and commit, every `*.log` in the evidence namespace was
   mechanically normalized by replacing tabs with spaces and removing only
   end-of-line whitespace. Commands, output text, exit codes, and timestamps
   were otherwise unchanged.

## Diagnostics and residual risk

Serena diagnostics reported zero errors and zero warnings in all four changed
source/test files. It reported only modernize hints: existing `interface{}`
uses, a `maps.Copy` suggestion, and Go 1.22 loop-variable copy hints.

The pre-facade tests use the narrow package-level `executeBulkFacade` function
variable. Tests are serial and the focused race gate passed. A future test that
mutates this hook in parallel would need its own synchronization or a server-
scoped injection seam; that is a test-architecture residual risk, not a current
production mutation path.

## Out-of-scope audit node

`MCP-STRUCTURED-INPUT-VALIDATION`: `store_memory.supersedes` still reaches
lossy coercion without explicit raw structured-input type validation. The task
explicitly prohibited edits to `coerce.go` and `tools_memory.go`, so this is
reported only and was not patched.

## Final cleanup

`16-final-residue.log` records the final exact-prefix queries:

```sql
SELECT count(*) FROM pg_database WHERE datname LIKE 'engram_mkr_bedge_%';
SELECT count(*) FROM pg_stat_activity WHERE datname LIKE 'engram_mkr_bedge_%';
```

At `2026-07-10T09:10:54.3826001Z`, both returned zero; exit code was 0.

The structured companion is
`.agent/reports/evidence/production-ready/db-bulkops-behavioral-edge-rework/DB-BULKOPS-BEHAVIORAL-EDGE-REWORK.final.json`.
