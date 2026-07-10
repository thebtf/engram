# DB-EMBEDDING-EVIDENCE-TRANSPORT Revision Maker Report

Date: 2026-07-10
Role: revision maker
Finish state: **READY_FOR_CHECK**

## Exact boundary

- Accepted product source commit: `38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df`
- Rejected evidence candidate and exact revision base:
  `580b0cd0ff38bb55a5195a8004e60234a824b7a8`
- Pre-packet verification checkpoint:
  `53b2ef1931c534e27183126a1aad2d46b3a854b2`
- Branch: `work/prc-db-embedding-evidence-transport-r2`
- Worktree:
  `D:/Dev/engram/.agent/worktrees/db-embedding-evidence-transport-r2`
- Allowed writes: this evidence verifier, its permanent self-test, and
  DB-EMBEDDING-EVIDENCE-TRANSPORT evidence/report artifacts.
- Forbidden writes honored: product/source/test bytes, primary and integration
  worktrees, canonical production-readiness register/Markdown/HTML, and
  protected role/session/oracle state.

The revision is based directly on the rejected evidence candidate, not on the
independent checker commit. It changes no product behavior and does not
self-accept.

## Reproduced failure

A permanent Node self-test was written before the verifier changed. Against the
exact rejected candidate it executed 18 test/subtest cases:

- pass: `1`;
- fail: `17`;
- skipped: `0`;
- process exit: `1`.

The rejected verifier returned exit `0` for header-only zero entries, missing
and extra entries, namespace traversal, a non-canonical dot alias, invalid
`checkout_equivalence` values, and unknown schema keys. The existing duplicate
entry rejection was the single passing RED case.

RED evidence:
`.agent/specs/db-embedding-stats-evidence-transport/evidence/DB-EMBEDDING-EVIDENCE-TRANSPORT-R2.red.json`.

## Repair

### Exact artifact set

`artifact-files` now requires exactly these five canonical paths:

1. `.agent/reports/evidence/production-ready/db-embedding-stats/SHA256SUMS.txt`
2. `.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/content-manifest.v1.json`
3. `.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/verify-manifest.cjs`
4. `.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/verification-observations.v1.json`
5. `.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/maker-report.md`

Zero, missing, extra, replaced, and duplicate canonical paths are structural
errors before a PASS can be computed.

### Canonical path boundary

Every manifest path is validated before filesystem access. Paths must be
non-empty repository-relative POSIX paths that are already normalized. Absolute
paths, backslashes, drive/URI separators, empty segments, `.`, `..`, NUL,
normalization changes, repository escapes, and raw/resolved disagreement are
rejected. Exact-set and containment decisions use the validated normalized path,
never a raw prefix.

### Strict schema and semantics

The JSON contract now rejects missing and unknown keys at the top level,
`representation`, `checkout_equivalence`, and each entry. It pins:

- `schema_version=1`;
- `slice=DB-EMBEDDING-EVIDENCE-TRANSPORT`;
- `algorithm=sha256`;
- `representation.kind=git-blob-content`;
- full source commit and blob OIDs;
- exact legacy-manifest and verifier paths;
- `checkout_equivalence.transform=replace each CRLF byte pair with LF`;
- `checkout_equivalence.bare_cr=reject`;
- the exact source-Git-blob result contract.

Legacy and artifact annotated manifests also reject duplicate, missing, and
unknown metadata keys and invalid semantic values.

## Permanent adversarial suite

Command:

`node --test --test-concurrency=1 .agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/verify-manifest.test.cjs`

GREEN result in the normal Windows checkout:

- tests: `18`;
- pass: `18`;
- fail/skipped/cancelled/todo: `0`;
- exit: `0`.

The suite covers header-only zero entries, missing, extra, duplicate, traversal,
dot alias, absolute path, backslash separator, all three checkout-equivalence
values, and unknown keys at every contract object level. Every mutation is
limited to the maker worktree and restored byte-for-byte in `finally` plus a
process-level cleanup hook.

## Positive representation rails

### Normal Windows CRLF checkout

`git ls-files --eol` reported `i/lf w/crlf` for all seven declared source
records.

| Mode | Exit | Status | Result |
| --- | ---: | --- | --- |
| `legacy-raw-audit` | 0 | `AMBIGUOUS_RAW_CHECKOUT_CONFIRMED` | raw `0/7`, Git object `7/7`, checkout-LF `7/7` |
| `git-object` | 0 | `PASS` | `7/7`, structural errors `0` |
| `checkout-lf` | 0 | `PASS` | `7/7`, bare CR `0`, structural errors `0` |
| `artifact-files` | 0 | `PASS` | exact required set `5/5`, structural errors `0` |

### Fresh LF materialization

Materialization:

`git -c core.autocrlf=false worktree add --detach
D:/Dev/engram/.agent/worktrees/db-embedding-evidence-transport-r2-lf-proof
53b2ef1931c534e27183126a1aad2d46b3a854b2`

`git ls-files --eol` reported `i/lf w/lf` for all seven source records.

| Mode | Exit | Status | Result |
| --- | ---: | --- | --- |
| `legacy-raw-audit` | 0 | `RAW_CHECKOUT_HAPPENS_TO_MATCH` | raw/Git-object/checkout-LF `7/7` |
| `git-object` | 0 | `PASS` | `7/7` |
| `checkout-lf` | 0 | `PASS` | `7/7`, bare CR `0` |
| `artifact-files` | 0 | `PASS` | exact required set `5/5` |
| permanent adversarial suite | 0 | `PASS` | `18/18` |

The LF proof worktree was clean before removal; its filesystem path and Git
registration were removed.

## TDD and Prove-It evidence

- RED: `18` total, `1` pass, `17` fail, exit `1`.
- GREEN: `18/18`, exit `0`.
- Prove-It, `validateContractSchema` sentinel: `18` failures, exit `1`.
- Prove-It, `verifyArtifactFiles` sentinel: `9` failures, exit `1`.
- Both sentinels were restored from the clean checkpoint.
- Post-restore: `18/18`, exit `0`.
- Node experimental coverage: all files line `88.89%` and branch `65.33%`;
  verifier line `84.46%`; functions `100%`; exit `0`.

Full TDD evidence:
`.agent/specs/db-embedding-stats-evidence-transport/evidence/DB-EMBEDDING-EVIDENCE-TRANSPORT-R2.tdd.json`.

## No-product-change proof

`git diff --exit-code 38d6a4fb... -- cmd internal plugin tests scripts go.mod
go.sum Makefile Dockerfile docker-compose.yml` exits `0`. In particular:

- `internal/embedding/store.go` remains blob
  `1abaee96b07583f9fd824ed03c40b043c490b567`;
- `internal/embedding/store_stats_test.go` remains blob
  `d381643deadbb42e8a9a07fc9375a6cdfedbdccc`.

No Go product test, PostgreSQL statement, container mutation, integration,
push, tag, release, or protected state write is part of this evidence-only
revision.

## Handoff

The compact summary and revision checksum manifest live under
`.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport-r2/`.
The containing commit and artifact hashes are reported by the maker handoff
after commit, avoiding a self-hash or self-commit paradox.

Finish state: **READY_FOR_CHECK**. A fresh independent checker must reproduce
the adversarial and CRLF/LF rails before post-review or integration.
