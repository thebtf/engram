# RELEASE-GATES-R8 local changed-code review

Verdict: `PASS_PENDING_INDEPENDENT_CHECKER_AND_ROOT_POST_REVIEW`

This is maker-side post-change review, not independent acceptance.

## Regression detector

- Product runtime, public APIs, data models, migrations, and UI behavior are unchanged.
- CI now fails closed when its exact package-plus-test execution proof is bypassed or when the R8 plan/state/scope authority loses a required row, owner, fold, slice, or rejected-head policy.
- The live register is intentionally not byte-frozen. Two successive live SHAs (`8F099B...`, then `22F2AF...`) with the same 67-slice structure passed against the immutable `AB5F...` freeze provenance; the second change advanced DB embedding evidence inside its existing owner.
- No unrelated caller of `assert-plan-path-ownership.ps1` exists outside the updated workflow and its own self-test/hash-only modes.

## Six-axis review

1. Correctness: the database proof matches both exact Go package and exact test identity at the live consumer. Scope conformance checks unique slice parity, direct owners, fold/historical classification constraints, required plan rows, scope/state hash binding, and explicitly rejected heads.
2. Validation completeness: self-tests cover row-plus-epoch deletion, missing map entry, missing fold owner, new register slice, rejected head falsely accepted, ordinary progress allowed, scope hash mismatch, and wrong-package zero acceptance. The extracted CI harness rejects 51 mutations.
3. Readability: structural parsing is isolated in `Get-PlanRowNames`, status policy helpers, and `Invoke-ScopeContractAudit`; the workflow retains one conformance entry point.
4. Architecture: the scope map remains an immutable structural projection while the JSON register remains the sole mutable progress authority. No demolished v5 runtime scaffold is touched.
5. Security: no credential, input, network, or secret-handling surface changed. Exact case-sensitive package/test matching removes a false proof path.
6. Performance: audits are linear over 67 scope rows and the existing plan ledger; no production hot path or dependency is added.

## Review correction made before final green

An overconstraint was removed: a frozen current head is not required to be one of the historical rejected heads. Only a live row that presents an explicitly rejected head with an accepted status fails. This preserves ordinary successor-head progress while keeping the load-bearing rejection rail.

## Residual gates

- A fresh native independent checker and root post-review remain mandatory.
- Cross-model review streams could not be launched during the maker turn because all native agent slots were occupied; this is recorded as degraded review coverage, not as acceptance.
- The full fresh-database repeat-3/race suite and Docker dev-stand are intentionally left for the root-authorized expensive gate run.
