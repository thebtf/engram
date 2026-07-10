# SECURITY-PROJECT-IDENTITY R4 maker report

Verdict: **READY FOR FRESH CHECKER**.

R4 closes `SPI-R3-CHK-001` with a permanent Go OS-child acceptance rail. The
revision is test/evidence-only: `internal/proxy/identity.go` remains the exact
target blob `9ecbad17612e7dd4e2ce8c8fed10ee4e041e11c1`, its base-to-R4 diff is empty,
and the useful R3 goroutine tests remain intact.

## Immutable target and scope

- Required base: `38344455754fe503acbd79d2134141f996adff7f`.
- Prior checker commit `0d84047c...` is evidence only and is not an ancestor of
  the R4 maker branch.
- New permanent test: `internal/proxy/identity_process_test.go`.
- Product, auth, database, protobuf, dependency, release, and v5-demolished
  paths are unchanged.
- Phase 0 classification is `CODE_PATH_COVERED`: this is an internal
  process/filesystem contract, not a new user-facing feature.

## Permanent process contract

Each invocation starts 84 real child processes across seven waves:

- three fresh first-use waves x 12 children;
- two existing-valid waves x 12 children;
- one existing-malformed wave x 12 children;
- one open delayed-partial-writer wave x 12 children.

Every child is a distinct `os.Executable()` test process. It emits `READY` and
blocks on stdin. Only after all 12 READY records arrive does the parent close a
single in-process release gate; 12 goroutines then write `G` to the child pipes.
There is no sleep-based synchronization in the committed test. A parent monitor
continuously classifies every visible final file as complete or partial.

The rail proves:

1. every fresh wave converges on one strict 128-bit anchor;
2. the parent sees at least one complete final file and zero EOF/zero/partial or
   unreadable final-file observations;
3. existing valid bytes remain byte-identical across two process waves;
4. existing malformed bytes fail closed and remain byte-identical;
5. an intentionally partial file remains byte-identical while its writer
   descriptor is still open;
6. neither `.engram-project-v2.json.tmp-*` nor `.engram-project.tmp-*` residue
   remains;
7. every fresh Linux anchor is mode `0600`;
8. repeat and race executions are deterministic.

## Anti-stub Prove-It

A temporary, uncommitted mutation recreated the R2 publication window by making
the final name visible after only half the bytes were written. The focused test
failed with **239 partial/unreadable observations** while the preservation
subtests remained green. The inverse patch was applied immediately; the product
blob returned to `9ecbad17...`, product diff became empty, and GREEN passed.

## Final verification

| Gate | Result |
| --- | --- |
| Windows Go 1.25.11 race, exact child-process test, `-count=2` | PASS; 168 children, complete observations 21,377 + 19,801, partial 0 |
| Linux Go 1.25.11, exact child-process test, `-count=2` | PASS; 168 children, complete observations 2,092 + 2,367, partial 0, mode 0600 |
| `go test ./internal/proxy -count=1` | PASS |
| `go test ./... -count=1` with integration DSNs unset | PASS |
| `go vet ./...` | PASS |
| selector compatibility and invalid-selector focused tests | PASS (`:`, `\\`, `/`, `.`, `-`, `_` retained; `..` rejected) |
| `git diff --check` | PASS |
| Prove-It mutation | PASS: expected test failure, 239 partial observations, mutation removed |

Linux used local immutable image
`golang@sha256:b96f24a8d7d010ea0acb9c3ba99064740f02b6b984612b28bd3c9c5ab9453e38`.
The first `bash -lc` invocation reset PATH and could not find Go; the corrected
command used `/usr/local/go/bin/go` in the same image and passed. This was an
invocation discrepancy with no product impact.

Detailed machine-readable evidence:

- `.agent/specs/security-project-identity/evidence/SECURITY-PROJECT-IDENTITY-R4.red.json`
- `.agent/specs/security-project-identity/evidence/SECURITY-PROJECT-IDENTITY-R4.prove-it.json`
- `.agent/specs/security-project-identity/evidence/SECURITY-PROJECT-IDENTITY-R4.verification.json`
- `.agent/testing/SECURITY-PROJECT-IDENTITY-R4/behavior-signal.md`

## Artifact hashes

| Artifact | SHA-256 | Git blob |
| --- | --- | --- |
| `internal/proxy/identity_process_test.go` | `cb85e74ca6b4394b6fd0009418ef765b88dbf8eb1e0376f43ae848e7b18714db` | `6e04140eaf4ce6eff91c3a40010e99ff56460773` |
| `.agent/testing/SECURITY-PROJECT-IDENTITY-R4/behavior-signal.md` | `9f66b18bf2992dd27381f2438711569460b2d564101e71896ef99d8650e482c7` | `c4d80978955130bdee737df06fa8609f340cd2d9` |
| `SECURITY-PROJECT-IDENTITY-R4.red.json` | `18d0cde079ab3613d58a77e77b8012ba895c94d3e6b6bee87d2d144047d4d8a9` | `becfba41abcb5bdc1137667f641cad61f154c8b1` |
| `SECURITY-PROJECT-IDENTITY-R4.prove-it.json` | `53dd6b5b79ef1899b0ae3fecc9c180739b808e8fdaf14c0fca2b8c8d98097089` | `408ce7f5c14fb5b127e9b2ff24c5170a97f55849` |
| `SECURITY-PROJECT-IDENTITY-R4.verification.json` | `b45038d290ed8d5c8f5e2b6f1c49d9ca7ff68fa24181a3e6b1608bec08342628` | `453970a0c56002bb94160b8eb898db702b56ec91` |

The exact staged scope contains six paths. Its LF-sorted, LF-terminated path
list has SHA-256
`bd1ce5c203c1381759c4e8aa69c462b8a50c096699d56d442f9d3e5c2c99bb77`.
`internal/proxy/identity.go` is absent from that list.

No merge, push, tag, release, database mutation, browser action, or worktree
cleanup was performed.
