# DB-EMBEDDING-EVIDENCE-TRANSPORT R6 Fresh Checker Report

## Verdict

**ACCEPT**

No CRITICAL or HIGH finding was found. Every mandatory lineage, committed-suite,
mutation, Prove-It, immutable replay, ordinary-clone, checksum-closure, product
parity, syntax, secret-scan, and cleanup gate passed. One MEDIUM audit-presentation
observation is recorded below; it does not change the actual execution topology or
create a false PASS.

## Reviewed target

- Target: `a1a3bfeb6546d1f3f24192b1c9f057402b6249a2`
- Required direct parent: `a538f6224ef31f612152470a4ecd45e78ff9d0f2`
- Target tree: `f723f22cd8c740b6efb52e169b0875ccc8274a9c`
- Prior R5 checker: `3c2912d93c657a22db2e0fa54ad3b42532c8fd2f`
  (read directly; not an ancestor of the target)
- Delta: exactly 32 paths, all under `.agent/`; product-path delta is zero.
- Changed-path digest (ordered `path NUL blob_oid LF`):
  `4921b53b82c00536f052436567aa198ddceced1e507aa2207c53c2d0c8ea5089`.

## Findings

### ET-R6-OBS-001 — MEDIUM — replay result labels the host root as `command.cwd`

`verify-final-commit-replay.cjs:294` actually launches the replay with
`cloneRoot` as the child working directory. The result correctly exposes that
temporary path in `canonical_execution.cwd`, but `verify-final-commit-replay.cjs:354`
sets `command.cwd` to `repoRoot`. Thus two fields in the same output describe
different CWDs without naming one as the caller/host CWD.

This is not a false execution proof: source inspection, three immutable replays,
exact metrics, the `.git`-directory topology check, and a forced-error cleanup probe
all confirm that execution occurs in the bounded canonical clone. The discrepancy is
an audit-presentation defect only. A later evidence-only cleanup should either set
`command.cwd` to the actual clone CWD or rename it to `invocation_host_root`.

### Prefix traversal defense-in-depth observation

The raw `isAllowedChangedPath()` helper accepts a textual
`allowed-prefix/../internal/...` string. Its only production input is Git
`diff-tree` output, and an independent alternate-index probe confirmed that Git
rejects that path as `Invalid path` before it can enter a tree. Prefix collisions,
backslashes, and case changes were rejected. This creates no reachable authority
widening in the current Git-backed call path, but the helper must not be reused for
arbitrary user-supplied paths without canonical path validation.

## Independent path and product proof

- Recomputed base-to-target closure: 32 changed paths.
- Direct checksum entries: 30 changed paths.
- Exact self-exclusions: only `checksum-layers.v1.json` and
  `R6-SHA256SUMS.txt`.
- Separately classified unchanged load-bearing entry: only
  `verify-manifest.cjs`.
- Missing, extra, duplicate, and non-ordinal entries: zero.
- Independent checksum-path digest: `5560d68cff17499f55d71a9abfedaa4d1b10821c3b33faa294f1cad72cd7b4d4`.
- Coherent omission of the changed R5 verifier remained FAIL after the attacker
  refreshed its local manifest count, path digest, and sums.
- All seven accepted product-source blobs at
  `38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df` are byte-identical at the R6
  target. Product verifier parity was `7/7` Git objects, `7/7` checkout-LF, and
  `5/5` artifact files.

## Committed and adversarial suites

- Direct R6 verifier: PASS, zero structural errors.
- Exact committed base suite: `24/24`, exit 0.
- Exact committed R6 tamper suite: `12/12`, exit 0.
- Prove-It sentinels: schema release `9/15`, forced artifact PASS `15/9`, forced
  R6 status PASS `0/12`; every sentinel exited 1.
- Post-Prove-It restoration: base `24/24`, R6 `12/12`, zero tracked residue.
- Fifteen independent attacks all returned structured nonzero FAIL:
  wrong-type argv, null representation, stale source OID, string-coerced exit
  status, synthetic signal, synthetic parse error, forged nonempty stderr,
  null dimensions, string-coerced counts, null self-exclusion, scalar checksum
  entries, duplicate entry, unknown entry key, coherently refreshed changed-path
  omission, and coherently forged two-run metrics/transcripts/envelopes.
- Baseline after all attacks: PASS with zero tracked residue.

## Immutable replay and representation

The replay passed in three independent environments:

1. checker linked worktree (host checkout CRLF-equivalent),
2. ordinary `.git`-directory clone with `core.autocrlf=false`, and
3. ordinary `.git`-directory clone with `core.autocrlf=true`.

Every run bound the exact target/tree/path digest, exited 0, emitted empty stderr,
reported `24/24`, and produced the same nine metrics:

| Scope | Lines | Branches | Functions | Contract |
| --- | ---: | ---: | ---: | --- |
| aggregate | 89.96 | 75.86 | 95.80 | only line is normative: `89.96 >= 80`, margin `+9.96` |
| verifier | 80.08 | 55.91 | 81.82 | all observed, non-normative |
| test harness | 99.72 | 94.78 | 100.00 | all observed, non-normative |

The `55.91` verifier branch value is not labelled as satisfying an invented floor.
Both ordinary clones also ran the exact committed base suite at `24/24` with empty
stderr and left no tracked residue.

## Process-launch and cleanup review

- Node and Git are launched with fixed argument arrays through `spawnSync`; no
  shell, command-string interpolation, `cmd.exe`, or PowerShell execution exists.
- The coverage argv is exact and immutable:
  `--test --test-concurrency=1 --experimental-test-coverage <exact test path>`.
- The canonical clone is created with `mkdtemp` below `os.tmpdir()`, uses a real
  `.git` directory, reads an exact tree, forces `core.autocrlf=false`, and checks
  the materialized LF bytes before launching Node.
- Recursive cleanup is guarded by an absolute temp-root prefix check and runs in
  `finally`.
- A checker-injected exception after clone creation exited 1 and left the before
  and after `engram-r6c-*` directory sets both empty.
- Final hygiene: zero matching Node processes and zero R6/clone temporary
  directories.

## Static and repository gates

- Node version: `v24.2.0`.
- `node --check`: PASS for all 10 changed CJS files and both checker probes.
- JSON parse: PASS for all 11 changed JSON files.
- `git diff --check`: PASS.
- Gitleaks `8.30.0`: one target commit scanned, zero leaks.
- R6 checksum rebuild run twice: byte-identical manifest and sums, zero residue.

## Changed-code review and reusability

Correctness, validation completeness, readability, architecture, security, and
performance were reviewed as one evidence component. The producer, verifier, and
final replay intentionally repeat canonicalization and clone materialization logic.
Although this is a three-occurrence shape, extracting a shared helper would create a
common-mode producer/verifier failure and weaken independence. It is therefore
classified as deliberate verification duplication, not a reusable-component
candidate. No product regression or dead product code was introduced.

## Scope discipline

The checker did not modify the maker commit, product code, PostgreSQL, browser state,
primary/integration worktrees, tags, remotes, or release state. The checker commit
contains only this report, machine evidence, two checker probes, and their checksum
manifest under `.agent/reviews/db-embedding-evidence-r6-fresh-checker/`.
