# RELEASE-GATES-R8 maker report

Verdict: `PASS_PENDING_INDEPENDENT_CHECKER_AND_ROOT_POST_REVIEW`

## Authority

- Direct parent: PLAN-GOVERNANCE-R8 commit `37d185b33b8f9411564fda49cf8b0d58321b62fd`.
- Reconstruction base: `d59d1605969b1f567506e96ded524dfd1e4be08a`.
- Canonical UTF-8/LF plan SHA256: `fd2b223a9a62848efc39e1c33bf739bada191508bccb7ba9a73140185638e43d`.
- Scope-map SHA256: `81093184036672008d6b85dfa88a431998ef70b587ab11475aa2b315f03ddf79`.
- Register freeze provenance remains `AB5F882FA110CA823A317061ECBCA0C62516702735325893A56206F9E7A29415`, `updated_at=2026-07-10T22:46:01.2938194+03:00`, 67/67.
- Live structural checks observed two later normal-progress register states, first `8F099B7564FDE5655541E04E7A075B3441F460FD1A2058AFBEE771736D9F83E0` and then `22F2AF0817F1A525EA4436E95326353617294E02CCF2CDAED1A9C94ADC1997FC`. Both passed 67/67 without rewriting the freeze; the latter advanced DB embedding evidence inside its existing owner.

## Delivered

- Carried forward the R7 wrong-package repair: twelve exact test names emitted by `internal/mcp` now prove zero observed/executed required tests and fail with all twelve exact missing identities.
- Hardened CI AST conformance so the sole live required-test consumer must match both the exact case-sensitive `internal/grpcserver` package and exact test name.
- Bound CI and the ownership runner to the exact R8 plan, ownership state, and scope-map hashes.
- Added structural scope conformance for deleted plan rows, owner/fold integrity, exact live slice parity, new register rows, and explicitly rejected heads presented as accepted.
- Preserved the corrected non-freezing semantics: ordinary same-lane status/head advancement and timestamp, command, artifact, or notes changes remain valid.

## TDD and Prove-It

The initial scope self-test failed RED because `Invoke-ScopeContractAudit` did not exist. GREEN passes both release-runner self-tests. Two final Prove-It mutations fail exactly as required:

1. Disabling the live package predicate enforcement makes the conformance harness report that `remove live session-start package predicate` was accepted.
2. Disabling live unique-slice-set enforcement makes the scope self-test report that a live register slice missing from the map was accepted.

The final extracted CI block passed and rejected all 51 mutations. Machine evidence is under `.agent/specs/release-gates-r8/evidence/release-gates/`.

## Verification

- `actionlint .github/workflows/test.yml`: exit 0.
- `assert-plan-path-ownership.ps1 -SelfTest`: PASS.
- `run-db-suite.ps1 -SelfTest`: PASS.
- Extracted CI conformance: PASS, 51/51 mutations rejected.
- Live structural Ledger: PASS; 57 maker rows, 333 declarations, 34 repeated exact paths, 36/36 epochs, 67/67 live scope rows, zero errors.
- Local six-axis changed-code review: PASS with one overconstraint corrected before final green.

The full fresh-database repeat-3/race suite and Docker dev-stand were not run in this maker turn; they remain root-authorized expensive acceptance gates. A fresh independent checker and separate root post-review are mandatory before integration. Cross-model review was unavailable during the maker turn because all native agent slots were occupied, so this report does not claim independent acceptance, integration, or release authorization.
