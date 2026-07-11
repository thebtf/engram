# RELEASE-GATES R10 plan-governance maker report

Status: `A10_READY_FOR_COMMIT`

## Outcome

This governance revision closes the dispatch ambiguity before any R10 guard
implementation is written. It preserves R9 PLAN-GOVERNANCE A and RELEASE-GATES
B as rejected history, starts from exact B head
`f11a77cce88f013839e22662458a3318670445e9`, and forbids checker-only commit
`0354362c427d99eb2993e5acddeb9d5bcd561df7` as a maker base.

The R9 independent verdict is `REVISE`: two HIGH failure classes and one MEDIUM
failure class. R10 therefore owns strict raw-JSON schema/type/null/duplicate/path
validation and trusted-base PR authority enforcement. The exact twelve-path B10
envelope is present in the master plan, active contract, ownership state, and
`path-envelope.json` before implementation.

## Contemporaneous authority

- SECURITY-PROJECT-IDENTITY R4 maker `320f1d80` is accepted by direct-child
  checker `3aa11399`; the exact five-path LF digest is `ff2385aa...e5b5b7d`.
- DB-TEST-POOL-HYGIENE R2 `68242c48` is rejected by checker `a7b2d36b` with four
  HIGH evidence-gate failures. Immutable direct-child R3 `331b5b19` is pending
  fresh checker and root acceptance; maker GitRevision, adversarial 12/12,
  diff-check, and gitleaks evidence is green without changing product paths.
- DB-EMBEDDING-EVIDENCE-TRANSPORT R6 `a1a3bfeb` is immutable and checker-pending.
- Canonical DB repeat-3 remains `0/3`; image HIGH/CRITICAL counts remain
  server/PostgreSQL/operator `5/20/13`. Both remain release blockers.
- The v5 demolition exclusions are unchanged and are not R10 build targets.

## External enforcement truth

Live GitHub inspection found public User-owned repository `thebtf/engram`,
default branch `main`, and active ruleset `13610955`. The ruleset contains only
deletion and non-fast-forward protection. It does not yet require an authority
guard status. Required-status activation is a separate root-owned transition
after the accepted R10 workflow exists on the default branch; no external
mutation was made by this maker.

## Commit contract

A10 must be the direct child of `f11a77cc` and contain only the eight governance
paths in `path-envelope.json`. B10 must be A10's direct child and contain only
the twelve owned implementation/evidence paths. No integration, push, tag,
release, or ruleset edit is authorized here.
