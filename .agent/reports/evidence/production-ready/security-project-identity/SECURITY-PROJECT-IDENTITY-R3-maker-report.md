# SECURITY-PROJECT-IDENTITY R3 maker report

Verdict: **READY_FOR_FRESH_CHECKER** after the atomic maker commit is created.

- Base: `9e2ce4e58a5cded69660ca9ac532d2167f315bb2`
- Checker evidence reviewed: `774047a3dfcd94de78c9e471fdcf05097aeff532`
- Checker is not an ancestor of this maker branch.
- Branch: `work/prc-security-project-identity-r3`
- Product/test ownership: exactly the nine paths named in the R3 brief.
- Additional writes: existing security-project-identity evidence namespaces plus this report.

## Closed findings

### SPI-R2-CHK-001 — strict transport-independent selector validation

`RegisterAndResolve` now applies one store-local outer-selector validator before
metadata validation and before the nil-database seam. It rejects empty values,
more than 256 bytes, edge whitespace, controls, `..`, internal whitespace, and
characters outside `[A-Za-z0-9_.\\/:-]` as `PROJECT_IDENTITY_INVALID`.

Direct-store tests prove malformed inputs never reach the database branch.
Default gRPC tests prove `a b` and `../x` become `codes.InvalidArgument` with one
`ErrorInfo` carrying `PROJECT_IDENTITY_INVALID`, domain
`engram.project_identity`, and `regenerate_project_identity_v2`; handler calls
remain zero. Colon, backslash, slash, dot, dash, underscore, and legacy alias
internal whitespace remain compatible.

### SPI-R2-CHK-002 — complete atomic no-replace anchor publication

Go, Claude hooks, and OpenClaw now use the same publication protocol:

1. generate and validate the complete JSON payload;
2. create a random same-directory temporary file with mode `0600`;
3. write, sync, and close the temporary file;
4. atomically hard-link it to the final path, which is a no-replace operation;
5. remove the temporary name;
6. on an existing final path, discard the losing temporary file and read the winner.

No code path opens the final path for writing. A valid existing anchor remains
byte-identical. A malformed existing anchor remains fail-closed and is never
overwritten. Any create/write/sync/close/publish/cleanup error returns without
claiming an identity. Filesystems without hard-link support fail closed rather
than falling back to an overwrite-capable rename.

## Verification evidence

| Gate | Result |
| --- | --- |
| RED store/default-gRPC classification | PASS: reproduced wrong unavailable classification |
| RED Go publication race | PASS: reproduced `decode .engram-project-v2.json: EOF` |
| RED Claude child-process race | PASS: failed on stress round 30 |
| RED OpenClaw child-process race | PASS: failed on stress round 6 |
| Focused store/gRPC/proxy GREEN | PASS |
| Go publication ordinary `-count=30` | PASS |
| Go publication `-race -count=30` | PASS |
| Claude true child-process concurrency, 30 rounds | PASS |
| OpenClaw true child-process concurrency, 30 rounds | PASS |
| Linux Go/Claude/OpenClaw mode `0600`, complete JSON, no temp residue | PASS |
| PostgreSQL 17.10 identity race `-count=10` | PASS; residue 0; ephemeral DB dropped |
| `go test ./... -count=1` | PASS |
| `go vet ./...` | PASS |
| Claude complete hooks | PASS 76/76 |
| OpenClaw build/tests/typecheck | PASS 27/27 |
| Prove-It mutations | PASS; zero survivors |
| Exact path parity | PASS; 14 paths; SHA-256 `341175c6056cfc424a252f7c47bc049b14f393bf419bd095df9a79c6ea77fed1` |
| Proto parity | PASS; zero paths |
| Request-path DDL / v5 demolition guards | PASS; zero added product hits |
| Gitleaks staged scan | PASS; 40.60 KB; no leaks |
| `git diff --check` | PASS |

## Security review

Classification: **S3 (high)**. The changed trust boundaries are externally
supplied selectors and workspace-local anchor files shared by concurrent
processes. Relevant risks are malformed/path-like input, partial-file TOCTOU,
silent replacement of persistent identity, unsafe permissions, and cleanup
residue. SQL construction, authorization, credentials, network requests,
cryptographic algorithms, schema, and migrations are unchanged.

OWASP A03/A04/A05/A08/A09 checks pass for this delta: selectors are rejected at
the final store boundary; publication is complete-before-visible and
no-replace; failures stay explicit and fail closed; random anchors continue to
use `crypto/rand` / `crypto.randomBytes`; no secret enters source or evidence.
The expected availability tradeoff is explicit: a filesystem that cannot make
same-filesystem hard links returns an error instead of weakening no-replace
atomicity. Security verdict: **PASS, no open finding**.

## Maker changed-code review

The staged diff was reviewed tests-first and then across correctness,
validation completeness, readability, architecture, security, and performance.
The review enumerated the store/default-gRPC selector path plus all three anchor
read/write paths. It found no blocking defect. Two readability-only fixes were
applied before the final rerun: hard-link no-replace intent is now documented at
each publication call, and child-process assertion failures serialize their
per-process results instead of emitting opaque `[object Object]` messages.

The public protocol change is additive: `ProjectIdentityV2` protobuf metadata
and the REST identity-only registration mode preserve existing field numbers,
routes, and legacy selectors. No new dependency, schema, background retry,
silent clamp, filtered count, raw-vs-normalized branch, or dormant/demolished
call path was introduced. The final full suites passed after these review fixes.
This maker review is not the independent acceptance verdict; a fresh checker
and PM post-run review are
still required.

## V5 demolition classification

The touched code is live Project Identity v2 input/filesystem infrastructure.
No graph retrieval stage, cross-encoder reranker, composite/internal-search
scoring pass, SDK observation extraction, direct session-memory path, or
server-side HTTP MCP transport was introduced or restored.

Fresh independent checker and root post-review remain mandatory. This maker did
not integrate, push, tag, release, modify the primary worktree, or alter role
oracles.
