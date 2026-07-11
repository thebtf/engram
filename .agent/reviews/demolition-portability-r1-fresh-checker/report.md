# DEMOLITION-PORTABILITY R1 fresh checker report

Verdict: **ACCEPT**

No CRITICAL or HIGH finding was found. The immutable maker is suitable for root
post-review and later synthesis. This verdict does not authorize merge, push,
tag, release, or any external mutation.

## Immutable boundary

- Maker HEAD: `2c88fed68e0da04b4686940b81f55579a8260919`
- Parent: `0c6269908aa810a2248f2bfaf3fca4f9f5791359`
- Tree: `56454175025fe2f599b1ac9a14b5dc8361b76453`
- Maker paths: 15, all mode `100644`
- Ordinal-LF name digest:
  `996b153680df782d4247b6ef72120fba36bfc7cf3fb1015385817e1035ce700f`
- Ordinal-LF status/name digest:
  `4761d8020de2f6ba49841291eca5996f7ae928559a69228c383f3ede5df71b18`
- Initial checker worktree: clean

The only production files changed by the maker are
`internal/graph/nodes_store.go` and `internal/handlers/loom/workers.go`.
`internal/mcp/tools_memory.go` is byte-identical at base and maker
(`18ba2c5e4e798567bd4d0f9a0f62ff9fc893f2b2`).

## Findings

CRITICAL: none.

HIGH: none.

LOW — permanent dangling-Resolve coverage is weaker after the current-schema
correction. `internal/graph/dangling_test.go` now correctly proves FK rejection
and sentinel shape, but no committed test calls `Store.Resolve` with a missing
endpoint. The anti-stub comments at `internal/graph/store.go:98` and
`internal/graph/nodes_store_test.go:149` still refer to the removed T016
acceptance test. The checker-only DB probe directly verified that current
`Resolve` returns `ErrDangling`, preserves the valid source, and returns a nil
target, so this is not a current product defect and does not block ACCEPT.

## PostgreSQL 17 audit

The checker created only `engram_dp_chk_0711_0715_c7e5` on PostgreSQL 17.10.

- Graph focused tests, count=3: 18 pass events, 0 fail, 0 skip.
- T022/T022b/T022c, count=3: 9 pass events, 0 fail, 0 skip.
- Checker probe proved nil and zero-length metadata normalize to `{}` on both
  Create and Update.
- Invalid JSON was rejected on Create and Update; the failed Update preserved
  the previously persisted value.
- A deliberately closed-DB Create returned an error after applying the same
  caller-object defaulting style already used for timestamps/privacy; metadata
  was `{}`. This is observable mutation-on-error, but it is consistent with the
  existing API style and is not a new independent blocker.
- Dangling insert: SQLSTATE `23503`, constraint
  `knowledge_edges_target_id_fkey`, zero inserted rows.
- Live FK definition remained
  `FOREIGN KEY (target_id) REFERENCES memories(id) ON DELETE CASCADE`.
- `depends_on` was accepted; stale `references` was rejected.
- Direct `Store.Resolve` of a valid source plus deleted target returned
  `ErrDangling`, preserved the source, and returned nil target.
- Before drop: owned memories=0, nodes=0, edges=0, active sessions=0.
- After drop: database count=0.

The reusable checker probe is
`.agent/reviews/demolition-portability-r1-fresh-checker/dbprobe/main.go`.

## Loom portability and Writer contract

Windows:

- package repeat count=3: PASS, 86.9% statement coverage;
- race: PASS;
- JSON run: 47 pass events, 0 fail, 0 skip;
- aligned/unaligned cap crossing, post-cap discard, underlying Writer error,
  zero-error short write, timeout, cancellation, stderr, empty output,
  structured args/CWD/env, missing executable, allowlist, and path separators
  all executed;
- parent PATH lookup remained absent and helper temp residue was zero.

Ubuntu WSL:

- Go `go1.25.12 linux/amd64`;
- package repeat count=3: PASS, 86.9% statement coverage;
- JSON run: 47 pass events, 0 fail, 0 skip.

WSL emitted the nonblocking host-environment warning that it could not translate
`U:\Library\Software\Scripts\nvmdtranscoder`; the Linux test and coverage
commands still exited 0.

Coverage profiles are preserved as `loom-windows.cover` and
`loom-linux.cover` in this checker namespace.

## Independent Prove-It

Go build overlays used temporary checker-only copies; product files were never
edited.

- Parent `nodes_store.go`: both current Create and Update omitted-metadata
  tests failed SQLSTATE `23502`.
- Parent `workers.go`: the direct writer test returned 5 instead of 7, and the
  unaligned end-to-end test failed with `short write`.
- Both hybrid confidence filters disabled: `low beta` returned and T022 failed.
- Temporary overlays/copies were removed.
- Product blobs after Prove-It matched maker HEAD exactly, and current focused
  Graph/T022/Loom replay passed.

## Static and repository gates

- `go build ./...`: PASS
- `go vet ./...`: PASS
- `git diff --check <parent> <maker>`: PASS
- Gitleaks 8.30.0 exact-commit scan: 1 commit, 26.34 KB, no leaks
- Added production lines matching removed-v5 graph retrieval, rerank,
  composite scoring, SDK observation extraction, or server HTTP MCP transport
  patterns: 0
- `t.Skip`/`Skipf`/`SkipNow` markers in `internal/handlers/loom`: 0

No v5-demolished behavior was restored.

## Broad-suite truth

`go test -json ./... -count=1` exited 1 with 13 failed test events in two
packages and 20 skips. Owned Graph/Loom/T022 failures were zero.

The failures were the DB pool/governance candidate tests under
`internal/db/gorm` and `TestEC_F1_TagDerivedBackfill_T007` under `internal/mcp`.
Those neighboring accepted/in-flight candidates are intentionally absent from
this isolated maker base. This is not a blanket waiver: no owned failure was
present.

## Checker execution corrections

1. The first status digest attempt used culture-sensitive `Sort-Object`.
   Rerunning with `[StringComparer]::Ordinal` reproduced the maker digest.
2. The first overlay command passed an unevaluated PowerShell expression as a
   package argument. It was rerun with an explicit absolute `-overlay` value;
   the expected RED evidence then appeared.
3. The first Windows coverprofile argument created a literal `$coverage` file.
   That file was removed and the gate was rerun with an explicit absolute
   profile path.

All correction residue was removed. Reusability candidates: none evaluated as
ready for extraction.
