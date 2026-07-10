# RELEASE-GATES-R9 maker report

## Identity and scope

- Immutable rejected R8 head: `406fe952c143eb8aaf5895427c568a41d4cec225`.
- Immutable R9 governance commit A: `8cb810095b2bea77ab9812832d9ab8a99c928d18`, direct parent R8.
- This commit B is a direct child of A and changes only the R9 release-gate workflow, the two release-gate scripts, this report, and `.agent/specs/release-gates-r9/evidence/release-gates/**`.
- No product implementation, master-plan, ownership-state, scope-map, active-diff contract, canonical register, primary worktree, integration worktree, HTML report, merge, push, or tag was changed.

## Implemented release authority

`assert-active-candidate-path-authority.ps1` is a self-contained frozen gate over the R9 plan and active-diff contract. The workflow pins the canonical UTF-8/LF SHA-256 values:

- plan: `4388337722e57b48e93515008e4220d6cd2c83de695c4c449387f071c59fb96f`;
- scope map (ownership Ledger): `fb170d59f3072117489402fd347cd1432c40adbc842811f92227498bcbc92693`;
- active-diff contract: `d8e7818d84831f047d30a8493f9c7d2a8cea288d5c381735960d11dd02988ae5`.

The gate validates all nine frozen candidates and 123 frozen paths, exact Git status/path digests, plan ownership, status classes, branch/base/head identities, pending namespaces, and release-acceptance falsehood. Optional pending arrays are normalized: prefix-only R6 emits `allowed_exact_paths: []`, never `[null]`.

Load-bearing metadata is also semantic, not merely hash-pinned: exact authority paths, rejected R8 head `406fe952...`, R8 scope provenance `ab5f882f...`, discovery-only mutable-register provenance, and SHA-256 ordinal/case-sensitive path serialization are required. The artifact explicitly reports `HISTORICAL_DISCOVERY_ONLY`, `used_for_acceptance=false`, and `mutable_register_read=false`.

Pending probes must start at the contract's exact `base_anchor` and prove that base is an ancestor of head. Their effective `.agent` surface is every bounded plan-owned declaration for the slice, not only the newest revision prefix. Product/test exact paths remain optional members of the allowed set:

- R6 accepts all six bounded common/R3/R4/R5/R6/spec evidence families and reports zero exact paths.
- R4 allows either/both exact test paths but does not require an unnecessary `identity_test.go` edit. `identity.go` remains forbidden in the final diff.
- The observed R4 head `ffdbaefb5fb9685899663a40c6b6fef4a08448ba` fails with exactly one violation: `.agent/testing/SECURITY-PROJECT-IDENTITY-R4/behavior-signal.md`. Authority was not broadened; a narrow successor must remove that path.

`assert-plan-path-ownership.ps1` now preserves empty slice/declaration results as typed arrays. The seven-path misbound DEMOLITION diff therefore returns the explicit `found 0` maker-row failure rather than an internal scalar `Count` exception.

## TDD evidence

The committed RED records show five independently observed failure classes before their production fixes:

1. the active-candidate script was absent;
2. prefix-only pending output serialized a null exact-path declaration;
3. a non-anchor pending probe was not rejected explicitly;
4. R9 load-bearing metadata mutations were not semantically rejected;
5. zero-row Diff mode threw an internal scalar `Count` exception.

GREEN self-tests reject missing/extra/wrong-owner/wrong-test/stale-namespace/zero-declaration candidates, undeclared and forbidden pending paths, prefix-only null output, incomplete full pending evidence surfaces, non-anchor bases, wrong R8/AB5F provenance, mutable-register acceptance, and wrong digest serialization/case.

## Verification

| Gate | Result |
| --- | --- |
| PowerShell parser for both changed gates | PASS |
| `assert-active-candidate-path-authority.ps1 -SelfTest` | PASS |
| frozen default audit | PASS: 9 candidates, 123 paths, 2 pending, 0 errors |
| full local Git replay with required objects | PASS: 9/9 verified |
| R4 `38344455..ffdbaefb` pending probe | expected FAIL: one undeclared `.agent/testing/**` path |
| DEMOLITION `4812589b..d59d1605` | expected FAIL: 7 diff entries, 0 maker rows, no internal exception |
| plan ownership self-test + static Ledger | PASS: 57 rows, 351 declarations, 34 repeated exact paths, 36 epochs |
| SECURITY-PROJECT-IDENTITY R3 replay | PASS: 14 paths, 0 violations |
| rejected DB embedding R5 path replay | PASS: 28 paths, 0 violations; acceptance status remains rejected |
| `actionlint .github/workflows/test.yml` | PASS |
| executable workflow conformance | PASS: all predecessor predicates plus 70 mutations rejected |
| synthesized staged-tree RELEASE-GATES exact Diff | PASS: 24 paths, 0 violations |
| `run-db-suite.ps1 -SelfTest` | PASS: exact package-plus-test and 12-test zero-skip false-green rails |
| tracked critical suite | PASS: 7 passed, 0 failed, 0 skipped |
| `go test ./...` | PASS |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| exact staged diff gitleaks | PASS: no leaks |
| native BOM scan over exact staged paths | PASS: 24 paths, no UTF-8/UTF-16 BOM |

The repository has no `tools/check-bom.cjs`; that requested helper invocation failed with `MODULE_NOT_FOUND` and was not treated as proof. A native byte-prefix scan over the exact staged commit-B set passed. Whole-directory gitleaks reports 14 pre-existing findings in five files outside commit-B scope; the exact staged B diff scan passed with no findings.

The heavy Docker-backed fresh-database repeat-three suite was not duplicated in this release-gate-only maker worktree. Its runner and false-green semantics are unchanged and the workflow continues to execute it as the canonical release gate.
