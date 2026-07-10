# DB-EMBEDDING-EVIDENCE-TRANSPORT R4 maker summary

R4 starts from exact base `d650df5c4271cdb50aa1f443d2f95b2f4b672541`
and preserves the accepted product source
`38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df` byte-for-byte.

Two new permanent regressions reproduce the checker findings on the exact base:
`representation=null` escaped through a raw `TypeError` reading `kind`, and a
null entry escaped through a raw `TypeError` reading `path`. Exact-base RED was
`22 pass / 2 fail`; GREEN and post-restore are `24/24`.

The repair is deliberately narrow. Entry-shape comparison now rejects a
non-object before dereference, and the already-validated representation is read
through a safe object. Both mutations now return stable JSON `status=FAIL`, a
specific structural error, empty entries, reported source accesses `0/0`, and
preload-observed `git cat-file` plus source-file reads `0/0`.

Independent checker attacks remain `15/15` at the case level. Prove-It mutation
made `validateContractSchema` lose 15 tests and forced artifact PASS lost 9
tests; restoration returned byte-identically to `24/24`.

Node `v24.2.0` coverage was run twice against the final verifier/test blobs and
was identical on both runs:

| Scope | Line | Branch | Functions |
| --- | ---: | ---: | ---: |
| aggregate | `89.23%` | `76.61%` | `95.35%` |
| verifier | `80.92%` | `58.02%` | `81.82%` |
| test harness | `100.00%` | `97.44%` | `100.00%` |

The hard gate is aggregate line coverage: `89.23% >= 80%`.

Windows materialization remains raw/Git/LF `0/7`, `7/7`, `7/7`; fresh LF is
`7/7` in all three views. Both materializations pass `git-object`,
`checkout-lf`, the `24/24` permanent suite, and the exact five-file artifact
set. Product/source/test delta and temporary worktree, Node, PostgreSQL database,
and PostgreSQL session residue are all zero.

Status: **READY_FOR_CHECK**. The maker does not merge, push, tag, or self-accept.
