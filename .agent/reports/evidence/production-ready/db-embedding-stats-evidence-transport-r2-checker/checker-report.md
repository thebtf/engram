# DB-EMBEDDING-EVIDENCE-TRANSPORT R2 independent checker report

Date: 2026-07-10
Role: independent checker/verifier
Status: **DONE_WITH_CONCERNS**
Verdict: **REVISE / NOT_READY**

The target improves the rejected verifier substantially, but it still permits
three independently reproduced false-PASS states. Under the assigned contract,
any false PASS or raw-path trust blocks post-review. This report does not modify
or repair maker, verifier, product, or test artifacts.

## Immutable boundary

- Exact target: `db2cf891dd9c6315fd17220ffe2d02302bea8844`
- Exact target tree: `6a450d3f0b83ce2afd95910da1516411a2134514`
- Exact parent/base: `580b0cd0ff38bb55a5195a8004e60234a824b7a8`
- Accepted product source: `38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df`
- Source branch: `work/prc-db-embedding-evidence-transport-r2`
- Checker branch: `review/prc-db-embedding-evidence-r2-checker`
- Checker worktree:
  `D:/Dev/engram/.agent/worktrees/db-embedding-evidence-r2-checker`
- Target ancestry: exactly one commit from the base; target parent equals base.
- Target delta: nine `.agent/` evidence/spec paths; product/source/test delta is
  zero both from the accepted product source and from the exact base.
- Checker writes: this checker evidence directory only.

The checker commit is reported after commit because a commit cannot embed its
own SHA or tree without changing them.

## Blocking findings

### HIGH ETR2-C001 — the seven-record source manifest is not an exact set

`verify-manifest.cjs:302` requires only a non-empty array. The legacy manifest
is compared to the same mutable contract at `verify-manifest.cjs:528`, and PASS
is computed against the resulting dynamic `total` at lines 584-600. There is no
constant for the seven required paths or required cardinality.

Two independent mutations therefore false-PASS:

1. Remove one content-contract entry and the corresponding legacy entry:
   exit `0`, status `PASS`, `6/6`, structural errors `0`.
2. Replace one required path with a correctly described `go.mod` blob while
   preserving cardinality and the matching legacy entry: exit `0`, status
   `PASS`, `7/7`, structural errors `0`.

This permits a complete-looking evidence result for an incomplete or different
source artifact set.

### HIGH ETR2-C002 — the accepted source commit can be rebound

`verify-manifest.cjs:275` checks only that `representation.source_commit` is a
40-hex value. Lines 532-537 require only that it is some ancestor of `HEAD`.
The accepted source `38d6a4fb...` is not pinned by the executable verifier.

Changing both mutable source-commit declarations to alternate ancestor
`580b0cd0ff38bb55a5195a8004e60234a824b7a8` produced exit `0`, status `PASS`,
`7/7`, structural errors `0`. This is representation/source drift accepted as
valid evidence.

### HIGH ETR2-C003 — invalid contract paths are not gated before raw access

The schema pass computes normalized/contained paths, but source verification at
`verify-manifest.cjs:549-553` still passes raw `entry.path` to Git and then to
`fs.readFileSync(path.join(...entry.path.split('/')))`, even when validation has
already recorded path errors. Artifact-file mode correctly gates reads on the
validated normalized required-path set; source modes do not. Invalid tested
paths return nonzero today because Git rejects them first, but the code does not
honor the claimed “validated before filesystem access” boundary.

### MEDIUM ETR2-C004 — committed aggregate coverage values are stale

The TDD JSON and maker report claim all-files line `88.89%` and branch `65.33%`.
Two fresh exact-target runs with Node `v24.2.0` were identical at line `89.10%`
and branch `66.50%`; verifier line `84.46%` and functions `100%` do match.
Coverage remains above the claimed threshold, so this is not a coverage
regression, but the committed exact aggregate values are not reproducible.

## Acceptance replay

| Rail | Independent result |
| --- | --- |
| One-commit ancestry and exact parent | PASS |
| Product/source/test delta versus source and base | PASS, zero paths |
| Declared contract shape | PASS, seven unique entries |
| Windows EOL materialization | PASS, `i/lf w/crlf` for `7/7` |
| Windows raw/Git-object/checkout-LF | PASS, `0/7`, `7/7`, `7/7` |
| Windows artifact set | PASS, exact `5/5` |
| Permanent maker adversarial suite | PASS, `18/18`, exit `0` |
| Fresh LF materialization | PASS, `i/lf w/lf` for `7/7` |
| Fresh LF raw/Git-object/checkout-LF | PASS, `7/7`, `7/7`, `7/7` |
| Fresh LF artifact set and adversarial suite | PASS, `5/5` and `18/18` |
| RED against exact base | PASS as evidence: exit `1`, pass `1`, fail `17` |
| `validateContractSchema` Prove-It sentinel | PASS as evidence: exit `1`, fail `18` |
| `verifyArtifactFiles` Prove-It sentinel | PASS as evidence: exit `1`, pass `9`, fail `9` |
| Post-sentinel byte restore | PASS, `18/18`, verifier byte-identical |
| Outer/artifact/legacy checksum manifests | PASS, `9/9`, `5/5`, `7/7` |
| Self-reference discipline | PASS: both checksum manifests exclude themselves |
| Node syntax and target diff check | PASS |
| Temporary worktrees/process/DB/session residue | PASS, all zero |

The positive rails above do not override ETR2-C001 or ETR2-C002: the permanent
18-case suite never couples the two mutable source manifests or tries a valid
alternate ancestor, so it cannot detect those false-PASS classes.

## Commands and exits

- `node verify-manifest.cjs --mode=legacy-raw-audit`: exit `0`,
  `AMBIGUOUS_RAW_CHECKOUT_CONFIRMED`, raw/Git/LF `0/7`, `7/7`, `7/7`.
- `node verify-manifest.cjs --mode=git-object`: exit `0`, PASS `7/7`.
- `node verify-manifest.cjs --mode=checkout-lf`: exit `0`, PASS `7/7`.
- `node verify-manifest.cjs --mode=artifact-files`: exit `0`, PASS `5/5`.
- `node --test --test-concurrency=1 verify-manifest.test.cjs`: exit `0`,
  `18/18` in both Windows and LF checkouts.
- `node --test --test-concurrency=1 --experimental-test-coverage
  verify-manifest.test.cjs`: exit `0` twice; line `89.10%`, branch `66.50%`,
  functions `100%` across all files.
- Target test materialized into exact-base detached worktree: exit `1`, pass
  `1`, fail `17`.
- Empty `validateContractSchema` sentinel: exit `1`, fail `18`; empty
  `verifyArtifactFiles` sentinel: exit `1`, pass `9`, fail `9`; post-restore
  exit `0`, pass `18`.
- `node checker-edge-mutations.cjs --repository=<exact-target-temp-worktree>`:
  exit `1`, checker status FAIL, eight cases, five expected outcomes and three
  false-PASS findings.
- `node --check` for verifier, maker test, and checker mutation harness: exit
  `0` each.
- `git diff --check 580b0cd0..db2cf891`: exit `0`.

## Checksums and cleanup

`checksum-audit.json` records every Git blob OID, SHA-256, byte length, raw
checkout hash, canonical-LF hash, and bare-CR count. All committed maker hashes
match. The outer R2 manifest Git blob is
`6fbef4d23f5a647952d694549d3d3f55cf6ab034`, SHA-256
`a723782459ee52b40db3c3105a138347a62354b48f1ec7fab4c3378a58617780`.

The mutation, LF, RED, and Prove-It worktrees were each clean/restored before
removal. Their paths and normalized Git registrations are absent. Checker-owned
Node processes, matching PostgreSQL databases, and matching PostgreSQL sessions
are zero. Shared container `engram-prc-postgres` remains running.

## Code-quality review

- Correctness and validation completeness: blocked by ETR2-C001/C002/C003.
- Readability: the validator is organized into understandable helpers.
- Architecture: no product dependency or source-code coupling was introduced.
- Security: artifact paths use an exact normalized allowlist, but source-mode
  raw path use weakens the intended trust boundary.
- Performance: bounded seven/five-entry work; no material concern.
- Reusability candidates: none; evaluated as an evidence-slice-specific gate.

## Required next action

Keep the target out of post-review. A maker revision must pin the accepted
source commit and exact seven source paths/cardinality in executable validation,
gate all source access on validated canonical paths, add permanent regressions
for the three false-PASS cases, refresh coverage evidence, and return to a fresh
independent checker.
