# DB-EMBEDDING-EVIDENCE-TRANSPORT R5 maker report

R5 starts from exact R4 target
`369951b61ee07cb0c405558e0f677cd1c9e90362`. The R4 checker commit
`24bf45cb773dfbe4e42d662b3c35bd0a65b51f45` is not an ancestor. Product,
source, and product-test blobs remain exact to accepted source
`38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df` (`7/7`).

## ET-R4-001 closure

The covered R4 verifier is preserved exactly:

- Git blob OID: `75bec9c41eb5abc435f13d90848074f6608f7fce`
- LF-byte SHA-256: `a55e59dd870659330add8f840272aa1e8829f8161779db3e9be9e6e014cf1ba4`
- bytes / LF / CR: `25465 / 718 / 0`

The final R5 test harness is captured as:

- Git blob OID: `8e814737c8d5f4437aeb2a97dc52220e115cba0b`
- LF-byte SHA-256: `970a7a4a322b8aa5a0ed434d68ef5ce41c5085c986007f15aadf34d69c0172aa`
- bytes / LF / CR: `20692 / 634 / 0`

`coverage-capture.v1.json` binds both exact Git-index blobs to their filesystem
bytes and declares `core.autocrlf=false`, `2/2 i/lf w/lf`, and LF-only
materialization. `verify-coverage-capture.cjs` rejects missing/unknown capture
fields, mixed EOL, CRLF, bare CR, index/filesystem disagreement, wrong hashes,
and a coverage JSON that disagrees with either canonical transcript. The permanent
24-case suite now proves both undeclared capture and a real mixed-EOL mutation
fail closed. The R5 verifier is launched through a small environment-clearing
wrapper, so it is not included in the coverage target.

## TDD and attack rails

- R5 RED over exact R4 target: `23 pass / 1 fail`, exit `1`; the new mode did
  not exist.
- Historical exact-base RED over
  `d650df5c4271cdb50aa1f443d2f95b2f4b672541` with the R4 final test blob:
  `22 pass / 2 fail`, exit `1`.
- GREEN and post-restore: `24/24`, exit `0`.
- Permanent top-level attack cases: `15/15`.
- `representation=null` and `entries[0]=null`: structured `FAIL`, empty stderr,
  empty entries, reported source access `0/0`, preload-observed Git/source-file
  access `0/0`.
- Prove-It `validateContractSchema`: `9 pass / 15 fail`, exit `1`.
- Prove-It `verifyArtifactFiles`: `15 pass / 9 fail`, exit `1`.
- Prove-It R5 capture verifier forced-PASS sentinel: `23 pass / 1 fail`, exit
  `1`; restored script passes syntax and evidence verification.

## Transcript-backed final LF coverage

Exact command, run twice after final staged covered bytes stopped changing:

```text
node.exe --test --test-concurrency=1 --experimental-test-coverage .agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/verify-manifest.test.cjs
```

Both runs are `24/24`, exit `0`, with identical metrics:

| Scope | Lines | Branches | Functions |
| --- | ---: | ---: | ---: |
| aggregate | `89.28%` | `75.30%` | `95.59%` |
| verifier | `80.08%` | `55.91%` | `81.82%` |
| test harness | `99.68%` | `95.16%` | `100.00%` |

The aggregate line floor is `89.28% >= 80%`, PASS. Canonical transcript SHA-256
(LF TAP; only non-semantic coverage-table trailing padding trimmed):

- run 1: `52a871ca44112dc2d4e7540f7e9548079a05619f967b4c4d9445b999d7a42daf`
- run 2: `d88556d5e8e437eba50505db6ac200e52910353ced62ad4eafbf6195147387a5`

The evidence-side verifier parses the two canonical tables itself, verifies both
transcript hashes, and compares the parsed values exactly with
`coverage-repeat.v1.json`. No maker-only mixed materialization is accepted.

## Representation, checksum, and cleanup rails

- Fresh LF source modes: raw/Git/checkout-LF `7/7`, bare CR `0`.
- Windows CRLF source modes: raw `0/7`, Git `7/7`, checkout-LF `7/7`, bare CR
  `0`.
- Canonical artifact mode: `5/5` in LF and Windows materializations.
- Layered checksum manifests exclude themselves and verify `5/5`, `13/13`,
  `20/20`, and R5 `32/32` using canonical LF bytes.
- Temporary worktrees, task-owned Node processes, access-spy directories,
  matching PostgreSQL databases, and matching PostgreSQL sessions: `0` at
  handoff.

## Handoff

The maker commit is intentionally reported out-of-band after commit to avoid
self-reference. No merge, push, tag, primary-worktree edit, or integration edit
is part of this slice. A fresh checker must independently parse the transcripts,
recompute coverage, replay the mutations, verify all checksum layers, and audit
the exact evidence-only path inventory before acceptance.
