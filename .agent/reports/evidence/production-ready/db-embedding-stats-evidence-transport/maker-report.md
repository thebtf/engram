# DB-EMBEDDING-EVIDENCE-TRANSPORT Maker Report

Date: 2026-07-10
Role: maker
Finish state: **READY_FOR_CHECK**

## Exact boundary

- Accepted product source commit: `38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df`
- Product parent: `dc891b2d72b1fd63b83e4a630a249241fc389151`
- Evidence implementation commit: `462c97dd889b4afbc84d1ddc07613d748604afee`
- Branch: `work/prc-db-embedding-evidence-transport`
- Worktree: `D:/Dev/engram/.agent/worktrees/db-embedding-evidence-transport`
- Allowed writes: embedding evidence/report namespace only
- Forbidden writes honored: product/source/test bytes, canonical register/Markdown/HTML, integration branch, protected role/session/oracle state

This slice repairs evidence transport only. It neither changes nor re-accepts product behavior.

## Defect reproduced

The original seven-line `SHA256SUMS.txt` contained correct SHA-256 values for the exact source commit Git blob contents but did not name that representation. A normal Windows checkout inherited `core.autocrlf=true`; `git ls-files --eol` reported `i/lf w/crlf` for all seven entries. Therefore:

- raw checkout hashes matched `0/7`;
- exact Git object hashes matched `7/7`;
- strict CRLF-to-LF canonical checkout hashes matched `7/7`;
- no file contained a bare carriage return.

The old generic fresh-checkout interpretation was therefore ambiguous, not corrupt.

## Repair

The existing manifest now carries parseable metadata:

- `algorithm=sha256`
- `representation=git-blob-content`
- full `source-commit`
- contract and verifier paths
- checkout equivalence: replace CRLF byte pairs with LF and reject bare CR

`content-manifest.v1.json` is the authoritative machine-readable contract. Every one of its seven entries binds:

- repository-relative path;
- exact SHA-1 Git blob OID at the accepted source commit;
- exact Git blob byte length;
- SHA-256 of the Git blob content.

`verify-manifest.cjs` fails closed on metadata disagreement, entry/order disagreement, duplicate paths, non-ancestor execution, blob OID/length/SHA disagreement, checkout content disagreement, or a bare CR. It supports:

- `--mode=git-object` — reads exact bytes with `git cat-file blob <source-commit>:<path>`;
- `--mode=checkout-lf` — canonicalizes only CRLF pairs, rejects bare CR, then requires byte identity with the Git blob;
- `--mode=legacy-raw-audit` — exposes raw-checkout mismatch without treating raw bytes as the contract.

## Two-checkout proof

### Normal Windows checkout

Materialization:

```text
git worktree add -b work/prc-db-embedding-evidence-transport .agent/worktrees/db-embedding-evidence-transport 38d6a4fb7ff5f5ae3b6c0066c0a1b806421137df
```

Observed `core.autocrlf=true` and `w/crlf` for all seven paths.

| Command mode | Exit | Status | Result |
| --- | ---: | --- | --- |
| `legacy-raw-audit` | 0 | `AMBIGUOUS_RAW_CHECKOUT_CONFIRMED` | raw `0/7`, Git object `7/7`, canonical LF `7/7` |
| `git-object` | 0 | `PASS` | declared representation `7/7` |
| `checkout-lf` | 0 | `PASS` | declared checkout equivalence `7/7`, bare CR `0` |

### LF-materialized checkout

Materialization:

```text
git -c core.autocrlf=false worktree add --detach .agent/worktrees/db-embedding-evidence-lf-proof 462c97dd889b4afbc84d1ddc07613d748604afee
```

`git ls-files --eol` reported `i/lf w/lf` for all seven paths.

| Command mode | Exit | Status | Result |
| --- | ---: | --- | --- |
| `git-object` | 0 | `PASS` | declared representation `7/7`; raw and canonical views also `7/7` |
| `checkout-lf` | 0 | `PASS` | declared checkout equivalence `7/7`, bare CR `0` |

The LF proof worktree was clean before removal. Its path and Git worktree registration were removed.

Machine-readable observations: `verification-observations.v1.json`.

## No-product-change proof

The accepted product files retain their exact source-commit blobs and SHA-256 values:

| Path | Git blob OID | Git-blob SHA-256 |
| --- | --- | --- |
| `internal/embedding/store.go` | `1abaee96b07583f9fd824ed03c40b043c490b567` | `7bfb06dfc0dda792147d5e2df9d2fe68b59edaac55d2396dece1b8a8a09eee5f` |
| `internal/embedding/store_stats_test.go` | `d381643deadbb42e8a9a07fc9375a6cdfedbdccc` | `a35a234eb167c58bf201afc50954e43926a69ba2294536f2d0fabf4e015b12a4` |

`git diff 38d6a4fb... -- internal/embedding/store.go internal/embedding/store_stats_test.go` is empty. The full branch delta is restricted to the augmented embedding manifest and the new embedding evidence-transport namespace.

No Go test or PostgreSQL mutation was needed because this follow-up changes no executable product/test byte. Verification is the executable Node verifier, exact Git ancestry/diff/blob checks, both checkout forms, and artifact hashing.

## Reproduction commands

Run from either checkout form:

```text
node --check .agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/verify-manifest.cjs
node .agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/verify-manifest.cjs --mode=git-object
node .agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/verify-manifest.cjs --mode=checkout-lf
node .agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport/verify-manifest.cjs --mode=artifact-files
```

The independent checker must run these against the exact branch head in a fresh checkout, verify the two product blob OIDs and SHA-256 values remain unchanged, challenge the manifest parser/fail-closed behavior, and confirm no residue or out-of-bound path exists.
