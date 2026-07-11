# PLAN-GOVERNANCE-R9 maker report

Status: `READY_FOR_COMMIT_A_VERIFICATION`

## Decision

R8 is rejected and immutable at `37d185b33b8f9411564fda49cf8b0d58321b62fd` / `406fe952c143eb8aaf5895427c568a41d4cec225`. R9 preserves its 67/67 structural projection, AB5F provenance, 36 ownership epochs, same-lane progress policy, rejected-head policy, and all predecessor obligations. It adds a tracked exact-diff authority so CI does not need the ignored mutable register or foreign candidate objects.

Path authority never implies product acceptance. Current candidates, historical/rejected candidates, pending namespaces, classification-only rows, and path-conflicting candidates are distinct machine states.

## Full resolvable-row audit

The audit inspected all 14 canonical-register rows whose full base/head objects resolved locally. Exact machine evidence is `resolvable-register-diff-mismatch-inventory.json`.

| Slice/class | R8 result | R9 disposition |
| --- | --- | --- |
| SECURITY-PROJECT-IDENTITY R3/R4 | FAIL: 11 undeclared of 14; subsequent checker `REVISE/HIGH` | Freeze R3's exact 14-path diff as rejected history; remove the wrong `project_store_test.go` declaration; start pending R4 from product head `38344455`, never checker-only `0d84047c`, bounded to existing `identity_test.go` and/or new black-box `identity_process_test.go` plus evidence/report namespaces; temporary `identity.go` RED mutations are forbidden in the final diff. |
| DB-EMBEDDING-EVIDENCE-TRANSPORT R5 | FAIL: 10 undeclared of 28 | Keep R5 rejected, authorize the literal R5 family, and reserve only the literal R6 family for its exact-base successor. |
| DB-AUTH | FAIL: 1 undeclared report of 5 | Add the exact report. |
| DB-EMBEDDING-STATS | FAIL: 4 undeclared of 8 under the R8 Diff namespaces | Add the exact report and both bounded evidence families. |
| DB-REAPER | FAIL: 2 undeclared of 2 plus `service.go` owner conflict | Reject from frozen current authority; do not create a simultaneous writer. A fresh candidate needs an epoch-safe amendment. |
| DB-BULKOPS | FAIL: 4 current-owner conflicts | Preserve as rejected historical evidence; no acceptance. |
| DB-BULKOPS-BEHAVIORAL-EDGE-REWORK | FAIL: partial register base violates the rejected-predecessor lock on 2 paths | Freeze the full `68b2ce58..bd68c05b` candidate and its 38 exact paths. |
| DEMOLITION-SKIP-CLASSIFICATION | 7 misbound R5 paths, no maker row; R8 Diff throws scalar `Count` error | Reject the register/head binding, preserve the checker classification as provenance, and require a clear zero-declarations/non-empty-diff failure. |
| DB-CRYSTALLIZATION | PASS | Freeze unchanged. |
| DB-TEST-POOL-HYGIENE | PASS | Freeze unchanged. |
| SECURITY-TOOLCHAIN | PASS | Freeze unchanged. |
| MASTER-PLAN / PLAN-GOVERNANCE / RELEASE-GATES | R9 base=head at observation | Explicit in-progress empty self rows; A/B receive separate exact Diff proof after commit. |

## Frozen contract

- Candidates: 9.
- Exact frozen paths: 123.
- Pending contracts: literal R6 evidence prefix; R4 exact proxy test plus existing bounded security evidence/report families.
- Path serialization: ordinal, case-sensitive, one normalized UTF-8 path plus LF.
- Excluded conflict: DB-REAPER `service.go` remains AUTH-BOOTSTRAP-SECURITY-owned.
- Corrected lineage: DB-BULKOPS-BEHAVIORAL-EDGE-REWORK starts from rejected predecessor `68b2ce5835c7c6efdf1c68da9eedcb8d9c3837ef`, not the live register's partial `cd098397..bd68c05b` window.

## Authority hashes before commit A

| Artifact | Canonical SHA256 |
| --- | --- |
| master plan | `4388337722e57b48e93515008e4220d6cd2c83de695c4c449387f071c59fb96f` |
| ownership state | `e41f52fbafa317eb1571c76a7d1de9add543da38a1b2471dbe587a849b21c032` |
| scope map | `fb170d59f3072117489402fd347cd1432c40adbc842811f92227498bcbc92693` |
| active-diff contract | `d8e7818d84831f047d30a8493f9c7d2a8cea288d5c381735960d11dd02988ae5` |
| mismatch inventory | `1f42fda4bc8fdd6121ecc894e017c7371cde75c300736b7b067ae89ba94d1a67` |

The mutable register snapshot was discovery input only (`29865adc048cb3f64ec7d133b3bd901c95115e4ae5b95c98927607de889f77d4` at capture). Neither the frozen gate nor CI requires that ignored file.

## Boundaries

No product file, primary checkout, canonical register, HTML report, merge, push, or tag was changed. Commit A contains only plan/state/scope governance plus R9 plan-governance evidence/report. Commit B will add the executable gate/workflow bindings and its own evidence/report.

## Verification before commit A

- `assert-plan-path-ownership.ps1 -SelfTest`: PASS.
- Static Ledger: PASS, 57 maker rows, 351 declarations, 34 repeated exact paths, 36 epochs, 67/67 scope entries.
- Live/current Ledger against the ignored register: PASS with the same structural counts; mutable progress was not used as frozen authority.
- Exact SECURITY-PROJECT-IDENTITY R3 Diff: PASS, 14 paths, 0 violations.
- Exact rejected DB-EMBEDDING-EVIDENCE-TRANSPORT R5 Diff: PASS for path authority, 28 paths, 0 violations; status remains rejected.
- Corrected full DB-BULKOPS-BEHAVIORAL-EDGE-REWORK `68b2ce58..bd68c05b`: PASS, 38 paths, 0 violations, rejected-predecessor epoch satisfied.
- DB-AUTH: PASS, 5 paths, 0 violations.
- DB-EMBEDDING-STATS: PASS, 8 paths, 0 violations.
- Frozen-contract preflight: PASS, 9 candidates / 123 exact paths / 2 pending contracts; all digests, ordinal order, classifications, plan owners, branches, and pending declarations match.
