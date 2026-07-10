# DB-EMBEDDING-EVIDENCE-TRANSPORT R4 maker report

Date: 2026-07-10
Role: revision maker
Finish state: **READY_FOR_CHECK**

## Immutable boundary

- Exact base and future commit parent:
  `d650df5c4271cdb50aa1f443d2f95b2f4b672541`.
- Accepted product source:
  `38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df`.
- Branch: `work/prc-db-embedding-evidence-transport-r4`.
- Worktree:
  `D:/Dev/engram/.agent/worktrees/db-embedding-evidence-r4-maker`.
- Product/source/test blobs remain identical to the accepted source: `7/7`.
- The R3 checker commit is not an ancestor and is not included.

## Reproduced defects and repair

Final test bytes over exact base reproduced `22 pass / 2 fail`, exit `1`:

1. `representation=null` escaped as raw `TypeError` while reading `kind`.
2. `entries[0]=null` escaped as raw `TypeError` while reading `path`.

The production change is limited to safe shape consumption after schema
validation: entry comparison requires a plain object before dereference, and
representation reads use a validated safe object. Both cases now emit stable
structured `FAIL` JSON with their exact schema error, empty entries, and no raw
exception.

The permanent tests generate a preload observer that records real
`child_process.spawnSync` and `fs.readFileSync` calls. For both null cases:

- reported Git/source-file accesses: `0/0`;
- observed `git cat-file blob`/required-source-file reads: `0/0`.

## Tests, attacks, and mutation proof

Command:

`node.exe --test --test-concurrency=1 .agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/verify-manifest.test.cjs`

- GREEN and post-restore: `24/24`, exit `0`.
- Exact-base RED: `22 pass / 2 fail`, exit `1`.
- Independent checker attack cases: `15/15` pass.
- `validateContractSchema` fail-open sentinel: `9 pass / 15 fail`, exit `1`.
- forced artifact-PASS sentinel: `15 pass / 9 fail`, exit `1`.
- verifier restored to SHA-256
  `525a9cd937e26fb7f38b8b51792f3af0e95eafdbdb2670fa7aee7a15fa914673`;
  post-restore suite `24/24`.

## Reproducible coverage on final verifier/test content

Both Node `v24.2.0` runs were identical:

| Scope | Line | Branch | Functions |
| --- | ---: | ---: | ---: |
| aggregate | `89.23%` | `76.61%` | `95.35%` |
| verifier | `80.92%` | `58.02%` | `81.82%` |
| test harness | `100.00%` | `97.44%` | `100.00%` |

The hard threshold is aggregate line coverage: `89.23% >= 80%`, PASS. The
unreproduced historical coverage values were removed from every duplicate R3
claim surface rather than retained as current evidence.

## Representation rails

Windows `core.autocrlf=true`, all seven source files `i/lf w/crlf`:

| Mode | Exit | Status | Result |
| --- | ---: | --- | --- |
| `legacy-raw-audit` | 0 | `AMBIGUOUS_RAW_CHECKOUT_CONFIRMED` | raw `0/7`, Git `7/7`, LF `7/7` |
| `git-object` | 0 | `PASS` | `7/7` |
| `checkout-lf` | 0 | `PASS` | `7/7`, bare CR `0` |
| `artifact-files` | 0 | `PASS` | exact artifacts `5/5` |
| permanent suite | 0 | `PASS` | `24/24` |

Fresh `core.autocrlf=false`, all seven source files `i/lf w/lf`:

| Mode | Exit | Status | Result |
| --- | ---: | --- | --- |
| `legacy-raw-audit` | 0 | `RAW_CHECKOUT_HAPPENS_TO_MATCH` | raw/Git/LF `7/7` |
| `git-object` | 0 | `PASS` | `7/7` |
| `checkout-lf` | 0 | `PASS` | `7/7`, bare CR `0` |
| `artifact-files` | 0 | `PASS` | exact artifacts `5/5` |
| permanent suite | 0 | `PASS` | `24/24` |

## Integrity and handoff

- Artifact checksum set remains exactly five files and excludes itself.
- Compact R4 packet checksum excludes itself and covers the source manifest,
  executable verifier/test, R4 TDD evidence, reports, and artifact manifest.
- Temporary RED, Prove-It, and LF worktrees are removed.
- Maker Node, matching PostgreSQL database, and matching PostgreSQL session
  residue are zero.
- No merge, push, tag, release, root-report edit, or self-acceptance occurred.

The final commit/tree/checksum identifiers are reported out-of-band after the
single commit to avoid self-reference. A fresh R4 checker must replay the null
attacks, exact-base RED, coverage, representation rails, artifact checksums,
and residue checks.
