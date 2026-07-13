# R12/B17 trusted authority maintenance security review

Captured: 2026-07-13

## Verdict

PASS for the R12/B17 implementation batch. The privileged authority workflow
executes only validator bytes exported from the exact trusted base Git object,
treats the pull-request head as data, uses a read-only token, and never checks
out candidate code. Candidate validator and workflow changes are exercised only
by the unprivileged `pull_request` workflow. The trusted maintenance validator
also rejects a candidate authority workflow that checks out code, references
secrets, requests write permissions, introduces `pull_request_target` into the
test workflow, removes the successor harness, or uses an action outside the
exact audited immutable-SHA allowlist.

## Current external sources

- GitHub Actions, secure use of `pull_request_target`:
  https://docs.github.com/en/actions/reference/security/securely-using-pull_request_target
- GitHub Actions, secure-use reference:
  https://docs.github.com/en/actions/reference/security/secure-use
- GitHub Security Lab, preventing pwn requests:
  https://securitylab.github.com/resources/github-actions-preventing-pwn-requests/
- GitHub-hosted runner image inventory and support policy:
  https://github.com/actions/runner-images
- Docker Compose build reference:
  https://docs.docker.com/reference/cli/docker/compose/build/
- pgvector PostgreSQL 17 image source:
  https://github.com/pgvector/pgvector/blob/master/Dockerfile
- PostgreSQL official image entrypoint source:
  https://github.com/docker-library/postgres/blob/master/docker-entrypoint.sh

Context7 was used for Docker Compose and GitHub Actions contract lookup.
Parallel was used as the independent current-source route for the official
Docker, pgvector, PostgreSQL, and GitHub Actions sources. Tavily was attempted
and remained blocked by `OAuth authorization required`; no training-memory
claim was substituted for that unavailable source.

The official action tags were also resolved directly with `git ls-remote` on
2026-07-13. The immutable pins are:

- `actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5`
- `actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff`
- `actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02`

The live tag check caught and removed a malformed 41-character checkout pin
before acceptance.

## Security decisions

1. `pull_request_target` executes the ordinary or maintenance validator
   exported from the event base with `git cat-file`; candidate scripts and
   workflows are never executed by the privileged job.
2. The fetched default-branch ref must equal the event base, the explicit PR
   ref must equal the event head, the base must be an ancestor, and the
   conflict-free merge tree must equal the exact head tree.
3. Ordinary pull requests reject every protected authority, governance, gate,
   and evidence path. Diff statuses are limited to literal regular-file adds
   and modifications; deletion, rename, symlink, submodule, and type changes
   fail closed.
4. Maintenance is an owner-only, same-repository, epoch-labelled transition.
   Its exact path/status set, successor epoch, validator blobs, protected head
   blobs, expiration, actor identity, and required-status identity are bound.
5. The committed manifest excludes its own container blob and event head to
   avoid an impossible fixed point. The trusted artifact binds both actual
   values after Git verification.
6. Historical manifests form a contiguous inductive chain from `r12-0001`.
   Every historical validator and protected blob is re-anchored to the current
   trusted Git tree, and stale competing transitions are rejected.
7. The cross-platform matrix records portable tests and coverage profiles.
   The isolated PostgreSQL release job alone owns DB completeness, zero
   unexpected skips, residue checks, and coverage enforcement. A real Windows
   replay without `DATABASE_DSN` produced 3,486 passes, 427 expected DB skips,
   and 41.14% coverage, proving the former all-OS enforcement was a deterministic
   false red rather than production evidence.
8. Load-bearing JSON booleans, integers, strings, objects, and arrays are checked
   without PowerShell coercion. A numeric string such as `"2"` cannot satisfy an
   integer schema field. Successor path lists are unique by path, irrespective
   of status, so an impossible `A` plus `M` duplicate cannot deadlock the next
   epoch. Every successor retains more than the seven-day rotation window.
9. The privileged workflow permits only the verified `upload-artifact` pin.
   The unprivileged workflow permits only the verified `checkout`, `setup-go`,
   and `upload-artifact` pins; a correctly pinned but unapproved action fails.

## Dynamic validation and hostile cases

The `engram:gedankenexperiment` lifecycle model found two HIGH deadlocks that a
one-transition happy-path test missed: the frozen bootstrap hash rejected a
valid successor, and requiring `transition_manifest=null` permanently froze the
second epoch. The design was changed from a bootstrap-only fixed point to an
inductive history whose last manifest is revalidated against Git.

After the final workflow-safety and strict-type revision, two clean local-Git
runs both passed 19 scenarios: three expected accepts and sixteen expected rejects. The accepted
chain reaches `r12-0003`; rejected cases cover ordinary protected mutation,
type change, wrong actor, manifest self-reference, wrong protected blob,
top-level contract rewrite, privileged checkout, secret use, unpinned and
unapproved pinned actions, write permission, privileged test trigger, numeric
string type confusion, duplicate successor path, insufficient rotation window,
and stale replay. Both runs verify temporary-fixture cleanup:

- `maintenance-simulation.json`
- `windows-harness.json`

The authoritative critical suite separately passed 203/203 with zero failures,
skips, unexpected skips, malformed lines, or database residue. Detailed final
hashes and gate counts are recorded in `R12-AUTHORITY-MAINTENANCE.tdd.json`.
