# SECURITY-PROJECT-IDENTITY Verification Manifest

- Base: `dc891b2d72b1fd63b83e4a630a249241fc389151`
- Branch: `work/prc-security-project-identity`
- Worktree: `D:/Dev/engram/.agent/worktrees/prc-security-project-identity`
- Finish target: `review-needed`
- Candidate commit: intentionally recorded in the maker handoff because a
  commit cannot contain its own hash.

## Delivered contract

- Shared Project Identity v2 metadata and vectors for Go, Claude, and OpenClaw.
- Strict high-entropy non-git anchor with explicit opt-in sharing.
- Additive gRPC fields and synchronous canonical resolution before handler
  dispatch on both Initialize and CallTool.
- Synchronous HTTP identity-only registration before retrieval/mutation.
- PostgreSQL convergence using only the existing `projects` table, advisory
  transaction locks, strict ambiguity counting, and no raw-anchor persistence.
- Claude and every OpenClaw consumer enforce a registration barrier before data
  access; OpenClaw deduplicates concurrent and late successful registration.
- Stable HTTP/gRPC error code and upgrade-action contracts with sanitized public
  diagnostics.
- Architecture documentation corrected for current stdio daemon + gRPC and
  HTTP/OpenClaw flows; no v5-demolished path was restored.

## Auditable artifacts

- `maker-contract.md` — boundary, classification, API/versioning decision.
- `openclaw-consumer-map.md` — complete pre-change consumer inventory and
  post-change 22/22 closure.
- `security-review.md` — S3/STRIDE review and residual risks.
- `.agent/specs/security-project-identity/evidence/project-identity-v2-vectors.json`
  — repository-wide cross-language vectors.
- `.agent/specs/security-project-identity/evidence/SECURITY-PROJECT-IDENTITY.red.json`
  — initial and edge RED evidence.
- `.agent/specs/security-project-identity/evidence/SECURITY-PROJECT-IDENTITY.green.json`
  — final executable gates and toolchain.
- `.agent/specs/security-project-identity/evidence/SECURITY-PROJECT-IDENTITY.prove-it.json`
  — mutation failures and restored GREEN.

## Final gate summary

| Gate | Result |
| --- | --- |
| `go test ./... -count=1` | PASS |
| `go vet ./...` | PASS |
| targeted `go test -race` | PASS |
| targeted `-count=10` | PASS, residue 0 |
| fresh PostgreSQL 17 migration + DB/HTTP tests | PASS, table present, residue 0, database dropped |
| OpenClaw build/test | PASS 23/23 |
| Claude hook suite | PASS 72/72 |
| protobuf regeneration parity | PASS, byte-identical |
| consumer barrier map | PASS, 20 files / 22 calls / 22 awaits |
| sentinel/schema/raw-anchor/secret/demolition scans | PASS, zero matches |
| `git diff --check` | PASS |

Status: **READY_FOR_CHECK** after the atomic candidate commit is created. No
push, merge, release, primary-worktree write, role/oracle write, or checker
launch is part of this maker handoff.
