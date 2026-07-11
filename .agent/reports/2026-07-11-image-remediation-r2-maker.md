# IMAGE-REMEDIATION-R2 immutable publish maker report

Status: **READY_FOR_INDEPENDENT_CHECK after the post-commit cold gate recorded in the maker handoff**.

This is a maker packet, not an acceptance verdict. The report is committed
before the final exact-HEAD cold build by design: a commit cannot embed its own
SHA without changing that SHA. The handoff supplies the exact successor commit,
ignored runtime-evidence paths, and final gate results.

## Boundary

- Exact parent: `0c6269908aa810a2248f2bfaf3fca4f9f5791359`.
- Worktree: `D:\Dev\engram\.agent\worktrees\image-remediation-r2-immutable-publish`.
- Scope: the A11 image build, scan, runtime, and immutable GHCR publication
  boundary plus its operator documentation and executable evidence.
- The rejected R1 maker/checker commits are intentionally absent from ancestry.
- Integration, release, tag creation, package publication, and GitHub ruleset
  mutation are outside this maker handoff.

## Why the old path was not production-safe

The former workflow mixed candidate execution and package-write authority,
relied on moving image tags, did not bind publication to one exact same-run
artifact, and did not prove all three runtime images as one accepted set. That
made a green build an insufficient release authority and left tag overwrite,
artifact substitution, credential exposure, and partial-publication races
under-specified.

## Implemented architecture

1. `.github/workflows/docker.yaml` is verification-only. Main, pull request,
   and manual runs receive `contents: read`; they never log in to GHCR and never
   receive `packages: write`.
2. `.github/workflows/docker-publish.yml` is a trusted `workflow_run` bridge
   with two fresh runners:
   - `prepare-release` checks out trusted default-branch control code, validates
     event/tag/main/ruleset provenance, checks out the exact candidate SHA with
     persisted credentials disabled, builds from a tracked-file-only archive,
     and uploads exactly one immutable five-file payload;
   - `publish-images` checks out trusted default-branch control code only,
     repeats provenance/ruleset validation, obtains a REST census of the current
     run, downloads by exact artifact ID, validates the payload as data, loads
     exact image IDs, and only then receives bounded GHCR credentials.
3. Publication is exact and fail-closed. The three image IDs are mapped to six
   destinations: canonical SemVer plus `sha-<full-commit>` for server,
   operator-console, and PostgreSQL. Every destination is compared before the
   first write, compared again after login, and read back after push. Existing
   mismatched refs abort every write; exact matches are verified no-ops. No
   `latest`, `main`, or other moving alias is emitted.
4. The artifact bridge rejects extra/duplicate/expired/wrong-run artifacts,
   wrong ID/name/digest, extra payload entries, filesystem links/reparse points,
   non-canonical paths, traversal, non-regular tar entries, hash/size drift,
   manifest drift, and loaded-image label/ID drift.
5. Credential handling uses an isolated `DOCKER_CONFIG`. Logout and recursive
   credential removal complete before publication evidence is validated and
   uploaded. Candidate code is never checked out or executed in the privileged
   job.
6. The images form one auditable runtime set:
   - server: pinned Go builder plus pinned distroless Debian 13, UID 65532;
   - operator console: pinned Node builder plus pinned distroless Node 22,
     non-root runtime and semantic health check;
   - PostgreSQL: pinned Wolfi base, PostgreSQL 17.10 and pgvector 0.8.1, including
     legacy-volume ownership migration behavior.
7. Compose now requires exact image values through `ENGRAM_SERVER_IMAGE`,
   `ENGRAM_OPERATOR_IMAGE`, and `ENGRAM_POSTGRES_IMAGE`; no production default
   resolves to a moving repository tag.

## Executable proof and TDD discrepancies

- `TestDockerReleaseRefFreshnessGuard` covers workflow permissions/order,
  trusted-vs-candidate checkout behavior, hostile refs, event/API provenance,
  exact main/tag rulesets, artifact census, payload envelope, archive traversal,
  six-destination planning, mismatch refusal, and idempotent publication.
- The protected-main authority guard is pinned to GitHub Actions integration ID
  `15368`; the only recovery bypass is User ID `7106373` and only for
  `pull_request`. A prove-it mutation to `15369` failed before restoration.
- RED evidence records the rejected one-runner bridge, raw shell/context seams,
  manual-dispatch privilege, publication race, healthcheck runner gap, shared
  builder VERSION gap, immutable-compose assertion mismatch, and commit-identity
  payload mismatch.
- `cmd/engram-healthcheck` has permanent unit coverage and is used instead of
  shipping curl or a shell solely for container health checks.
- A provisional no-cache run built and scanned all three images with zero
  HIGH/CRITICAL Docker Scout findings and empty cleanup residue. It exposed two
  real defects: the operator target did not receive the validated VERSION, and
  the runtime test contradicted the immutable compose contract. Both received
  permanent regressions. The already-scanned images then passed all three exact
  runtime contracts in 225.565 seconds.
- `ValidatePayload`/`LoadPayload` accept both canonical SemVer and the canonical
  `sha-<40 lowercase hex>` identity used by audited commit builds. The actual
  publication planner and publisher remain strictly SemVer-only.

## External fail-closed blocker

Live GitHub evidence found only one active branch ruleset named `main`; it has
no include selector, required status authority, or recovery bypass. No exact
`refs/tags/v*` no-bypass ruleset exists. Therefore publication must and does
stop before registry login. The required state is captured in
`.agent/specs/image-remediation-r2/evidence/LIVE-RULESET-BLOCKER.json`.

## Checker focus

- Re-derive the two-runner trust boundary from workflow permissions and checkout
  order; do not approve from seam presence alone.
- Mutate artifact identity/digest/run, candidate SHA, integration ID, ruleset
  selectors/bypass actors, payload links/traversal, and one of the six remote
  config digests; every mutation must fail before a package write.
- Verify the privileged job never checks out or executes candidate code and
  that credentials are erased before evidence upload.
- Inspect the final ignored acceptance manifest, payload validation/load output,
  runtime JSONL, scanner SARIF, cleanup census, exact commit count, and rejected
  ancestry proof supplied in the maker handoff.

Finish state: **maker freeze only; independent checker and control-plane
ruleset repair are required before integration/publication**.
