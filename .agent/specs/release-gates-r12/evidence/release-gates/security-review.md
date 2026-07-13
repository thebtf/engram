# R12/B17 trusted authority maintenance security review

Captured: 2026-07-13

## Verdict

PASS for the R12/B17 implementation batch; activation is intentionally still
BLOCKED until this bootstrap reaches `main` and the live `main` ruleset is
updated to require the exact `authority-guard` check from GitHub Actions. The
classic branch-protection endpoint currently returns `404 Branch not protected`.
Repository ruleset `13610955` is active, but its live rule set contains only
deletion and non-fast-forward protection, with no required status checks. This
is an explicit release gate, not an implementation PASS claim.

The privileged authority workflow
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
- GitHub rulesets, required status checks and expected GitHub App source:
  https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets#require-status-checks-to-pass-before-merging
- GitHub required-check troubleshooting, latest-head and expected-App behavior:
  https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/collaborating-on-repositories-with-code-quality-features/troubleshooting-required-status-checks
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

Context7 was used for Docker Compose and GitHub Actions contract lookup,
including least-privilege permissions, full-SHA pins, and concurrency groups.
Parallel was used as the independent current-source route for the official
Docker, pgvector, PostgreSQL, GitHub Actions, required-check, and ruleset
sources. Tavily was attempted again during the external-review repair and
remained blocked by `OAuth authorization required`; no training-memory claim
was substituted for that unavailable source.

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
   and plan-governance evidence path. Diff statuses are limited to literal
   regular-file adds and modifications; deletion, rename, symlink, submodule,
   and type changes fail closed. Pending exact and prefix envelopes reject every
   `forbidden_final_paths` member before authorization, and the active-contract
   validator rejects a forbidden exact path covered by an allowed prefix.
4. Maintenance is an owner-only, same-repository, epoch-labelled transition.
   Its exact path/status set, successor epoch, validator blobs, protected head
   blobs, expiration, actor identity, and required-status identity are bound.
5. The committed manifest excludes its own container blob and event head to
   avoid an impossible fixed point. The trusted artifact binds both actual
   values after Git verification.
6. Historical manifests form a contiguous inductive chain from `r12-0001`.
   Every historical validator and protected blob is re-anchored to the current
   trusted Git tree, and stale competing transitions are rejected. Ordinary PR
   validation routes through the trusted-base maintenance validator in
   `-ValidateBaseOnly` mode, so bootstrap-only fixed-point proof is skipped
   after the first epoch without weakening the historical chain.
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
   The candidate privileged workflow must also have the same complete
   non-comment skeleton and `run:` commands as the trusted base; only an
   independently allowlisted immutable action-pin rotation can normalize.
10. Concurrency identity includes PR number, exact head SHA, and event label.
    Unrelated label/synchronize events cannot cancel an owner-approved
    maintenance run, while removal of the same authority label retains the
    revocation/cancellation path.
11. GitHub's current documentation confirms that full commit SHAs are the only
    immutable action reference and that a required check can be bound to its
    expected GitHub App. Live activation therefore requires the GitHub Actions
    integration id `15368`; accepting an identically named status from any
    source is forbidden.

## Independent external review

CodeRabbit, Codex, and Gemini reviewed PR #405 at the original `1dc6b91c`
head. Valid findings closed in this revision are: bootstrap-only ordinary-PR
lockout after epoch one, privileged `run:` injection, numeric-string contract
identity, unprotected plan-governance evidence, concurrency cancellation,
prefix-covered forbidden paths, StrictMode property access, and YAML comment
handling. The stale mutation count is regenerated below.

Two proposed changes were rejected with repository evidence rather than
silently applied. Lowercasing SHA inputs would weaken the explicit
`^[0-9a-f]{40}$` canonical-input contract. Equating replacement predecessor
identity with `required_successor_base_sha` would invalidate the live clean
replacement model: a rejected head is deliberately not the clean successor
base, while a root-selected predecessor may be an ancestor rather than the
same commit. `rework` keeps exact-base semantics; `replacement` keeps distinct
history and successor-base identities.

## Dynamic validation and hostile cases

The `engram:gedankenexperiment` lifecycle model found two HIGH deadlocks that a
one-transition happy-path test missed: the frozen bootstrap hash rejected a
valid successor, and requiring `transition_manifest=null` permanently froze the
second epoch. The design was changed from a bootstrap-only fixed point to an
inductive history whose last manifest is revalidated against Git.

After the external-review repair, two consecutive clean local-Git runs both
passed 22 scenarios: four expected accepts and eighteen expected rejects. The
accepted chain reaches `r12-0003` and proves an ordinary PR still works after
epoch one. Rejected cases cover ordinary protected and plan-governance evidence
mutation, type change, wrong actor, manifest self-reference, wrong protected
blob, top-level contract rewrite, privileged checkout and command injection,
secret use, unpinned and unapproved pinned actions, write permission,
privileged test trigger, numeric-string type confusion, duplicate successor
path, insufficient rotation window, and stale replay. The run also self-tests
exact/prefix `forbidden_final_paths` and executes the trusted parsers against
root-key and permission comments. Both runs verify temporary-fixture cleanup:

- `maintenance-simulation.json`
- `windows-harness.json`

The authoritative critical suite separately passed 203/203 with zero failures,
skips, unexpected skips, malformed lines, or database residue. Detailed final
hashes and gate counts are recorded in `R12-AUTHORITY-MAINTENANCE.tdd.json`.

The original PR push also exposed one unrelated base-equivalent macOS test
failure: `TestCliWorker_ProductionReadinessStructuredArgsAndCWD` compares the
logical `/var/...` temp path with macOS's canonical `/private/var/...` path.
That product-test portability defect is release-blocking and is assigned to a
separate integration-fix batch; it is not hidden inside this governance verdict.
