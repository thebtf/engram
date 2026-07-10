# DB-EMBEDDING-EVIDENCE-TRANSPORT R3 maker report

Date: 2026-07-10
Role: revision maker
Finish state: **READY_FOR_CHECK**

## Immutable boundary

- Accepted product source: `38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df`.
- Rejected R2 target: `db2cf891dd9c6315fd17220ffe2d02302bea8844`.
- Independent R2 checker and exact R3 parent:
  `8dac7910de52d2744fcf67f79a0a1597beebac72`.
- Branch: `work/prc-db-embedding-evidence-transport-r3`.
- Worktree:
  `D:/Dev/engram/.agent/worktrees/db-embedding-evidence-r3-maker`.
- Product/source/test code is byte-identical to the accepted product source.
- The R2 checker directory is inherited unchanged. This maker does not merge,
  push, edit the root readiness report, or self-accept.

## Checker findings reproduced before repair

The existing `18/18` R2 suite passed first. Four permanent tests were then
added while the R2 verifier remained unchanged. The RED run was `18 pass / 4
fail`, exit `1`:

1. deleting one required source and the matching legacy line false-PASSED `6/6`;
2. replacing a required source with the valid `go.mod` blob false-PASSED `7/7`;
3. rebinding both source-commit declarations to ancestor `580b0cd0...`
   false-PASSED `7/7`;
4. an invalid raw path reached `git cat-file` instead of returning a structured
   pre-access rejection.

The same new test bytes against exact parent verifier blob
`9f8424f1ea8ed5accac11ff6f019efdad9573cf9` reproduced `18 pass / 4 fail`.

## Repair

The executable verifier now pins:

- `EXPECTED_SOURCE_COMMIT` exactly to
  `38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df`;
- exact cardinality `7`;
- exactly the seven accepted source paths declared in
  `content-manifest.v1.json` and the legacy manifest.

Schema validation produces canonical contained paths and absolute paths as one
validated record set. Source Git object lookup and checkout-file reads consume
only that validated set. Any schema, source-lock, metadata, or shape error
leaves the validated set empty and returns `FAIL` with
`source_accesses.git_objects=0`, `source_accesses.checkout_files=0`, and no
verified entries. Raw contract paths never reach source Git or filesystem APIs.

## Permanent adversarial suite and TDD

Command:

`node --test --test-concurrency=1 .agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/verify-manifest.test.cjs`

- GREEN and post-restore: `22/22`, exit `0`.
- Exact-parent RED: `18 pass / 4 fail`, exit `1`.
- `validateContractSchema` sentinel: `10 pass / 12 fail`, exit `1`.
- `verifyArtifactFiles` sentinel: `13 pass / 9 fail`, exit `1`.
- Both sentinels restored byte-identically; post-restore `22/22`.

Two fresh Node `v24.2.0` coverage runs were identical:

| Scope | Line | Branch | Functions |
| --- | ---: | ---: | ---: |
| aggregate | `87.27%` | `71.75%` | `94.87%` |
| verifier | `79.55%` | `51.59%` | `81.82%` |
| test harness | `100.00%` | `97.94%` | `100.00%` |

The `80%` TDD threshold is explicitly evaluated against aggregate line
coverage. No verifier-only or aggregate metric is relabeled as another scope.

## Representation rails

In the normal Windows checkout, all seven source records are `i/lf w/crlf`:

| Mode | Exit | Status | Result |
| --- | ---: | --- | --- |
| `legacy-raw-audit` | 0 | `AMBIGUOUS_RAW_CHECKOUT_CONFIRMED` | raw `0/7`, Git `7/7`, LF `7/7` |
| `git-object` | 0 | `PASS` | `7/7`, source accesses `7/7`, errors `0` |
| `checkout-lf` | 0 | `PASS` | `7/7`, bare CR `0`, errors `0` |
| `artifact-files` | 0 | `PASS` | exact required artifacts `5/5` |

In a fresh `core.autocrlf=false` materialization, all seven records are
`i/lf w/lf`:

| Mode | Exit | Status | Result |
| --- | ---: | --- | --- |
| `legacy-raw-audit` | 0 | `RAW_CHECKOUT_HAPPENS_TO_MATCH` | raw/Git/LF `7/7` |
| `git-object` | 0 | `PASS` | `7/7`, errors `0` |
| `checkout-lf` | 0 | `PASS` | `7/7`, bare CR `0` |
| `artifact-files` | 0 | `PASS` | exact required artifacts `5/5` |
| permanent suite | 0 | `PASS` | `22/22` |

All temporary RED, Prove-It, and LF worktrees were restored/clean before
removal; their paths and Git registrations were removed.

## Integrity and product preservation

- The legacy source manifest remains seven Git-blob records from the accepted
  source commit.
- The artifact manifest remains an exact five-path canonical-LF set and
  explicitly excludes itself to avoid recursion.
- `internal/embedding/store.go` remains blob
  `1abaee96b07583f9fd824ed03c40b043c490b567`.
- `internal/embedding/store_stats_test.go` remains blob
  `d381643deadbb42e8a9a07fc9375a6cdfedbdccc`.
- Node syntax checks, JSON parsing, checksum/self-reference checks, diff checks,
  and process/worktree/DB/session residue checks pass.

The compact R3 packet is under
`.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport-r3/`.
The final commit, tree, and packet hashes are reported out-of-band after the
single evidence-only commit to avoid self-reference.

Finish state: **READY_FOR_CHECK**. A fresh independent checker must replay all
four repaired false-PASS classes and both EOL materializations.
