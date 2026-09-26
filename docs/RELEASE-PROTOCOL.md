# Release Protocol

## Applies When

- Releasing the Engram server, stdio daemon, Claude plugin, Codex plugin, OpenClaw npm plugin, or GitHub release artifacts.
- Any change merged to `main` that operators should receive through the tagged release / Watchtower path.

Repository-only procedure or CI corrections reach `main` through normal review and Actions; they do not require a new product version/tag or consumer binary release unless their accepted scope changes a shipped surface. Source-only scope does not waive required PR rules or proof for the changed procedure.

## Additional Release Surfaces

| Surface | Version source | Publish command | Verification |
| --- | --- | --- | --- |
| Server / daemon | `internal/version/version.go` | annotated `vX.Y.Z` tag triggers `.github/workflows/release.yaml` | `git ls-remote --tags origin refs/tags/vX.Y.Z`; release workflow success |
| Claude plugin | `plugin/engram/.claude-plugin/plugin.json` | included in repository tag / plugin package | JSON `version` equals tag without `v` |
| Codex plugin | `plugin/engram/.codex-plugin/plugin.json` | included in repository tag / plugin package | JSON `version` equals tag without `v` |
| OpenClaw npm plugin | `plugin/openclaw-engram/package.json` + `openclaw.plugin.json` | merge to `main` triggers `.github/workflows/plugin-publish.yml` | both JSON versions match; local stable version is greater than `npm view openclaw-engram version`; publish workflow succeeds; registry serves the new version |
| Changelog | `CHANGELOG.md` | release commit | top entry for `X.Y.Z` exists and names merged PRs |

## Required Gates

| Gate | Command / evidence | Blocks release when |
| --- | --- | --- |
| PR review | PR approval required by this protocol, actual GitHub branch rules and review state, evidenced disposition of findings, zero unresolved required threads, and merge | project-required approval is missing, a causal blocking finding is open, or a required thread remains unresolved; an optional bot delay or wrapper timeout does not override landed GitHub state or permit unilateral resolution |
| CI | `gh pr checks <PR>` and the configured required checks on the reviewed head | any check required by the actual branch rules fails or is missing |
| Go tests | `go test ./...` | non-zero exit |
| Go vet | `go vet ./...` | non-zero exit |
| Vulnerability scan | `govulncheck ./...` | reachable vulnerability reported |
| Build | `go build ./cmd/engram ./cmd/engram-server` | non-zero exit |
| Plugin hooks | `node --test plugin/engram/hooks/*.test.js` | non-zero exit when hook/plugin code changed |
| OpenClaw plugin | `npm test --prefix plugin/openclaw-engram` | non-zero exit when OpenClaw plugin code changed |
| OpenClaw publish version | `node plugin/openclaw-engram/scripts/check-publish-version.mjs <local> <npm>` | local version is malformed, equal to, or older than the registry version |
| Docker image acceptance | `final-image-set.json` retained from the release workflow | manifest is missing, not `status: PASS`, does not cover `server`, `operator-console`, and `postgres`, or lacks exact IDs, zero HIGH/CRITICAL findings in the three canonical-image SARIF files, runtime proof, or cleanup PASS |
| Released-image rescan | post-publication `ScanPublished` evidence: one summary JSON plus per-image SARIF/log for `server`, `operator-console`, and `postgres` | after publication, first run is not started within 24h, later evidence is older than 36h by `started_at`/`completed_at`, evidence is missing, HIGH/CRITICAL findings exist, or scanner/database/tag-resolution errors prevent complete evidence; blocks rollout/continued deployment, not initial digest publication |
| Diff hygiene | `git diff --check` | whitespace/conflict marker errors |
| SonarQube Quality Gate | A designated QA subagent runs `node tools/quality/run-sonarqube.mjs` locally from the clean, frozen exact candidate; retain `.agent/e/sonarqube/<HEAD>.json`; no GitHub Actions workflow performs this gate | exact candidate coverage is incomplete, scanner/CE/QG is non-OK, or requested status publication fails; only for the one-time v6.49.4 PR #531 exception below, Sonar unavailability permits incomplete exact-candidate coverage and `UNAVAILABLE / NOT_PROVEN` instead of fresh analysis and `OK`, without resolving observed findings or waiving any other gate |
| Parser release artifacts | Existing release steps invoke `prepare-bootstrap-policy.sh --check`, `check-bootstrap-policy-artifacts.sh`, `readback-bootstrap-policy-assets.sh`, and `verify-bootstrap-release-assets.js`; these also enforce the generated parser policy, raw parser asset, every package archive, and the draft's uploaded parser digest before publication | parser policy differs from source, raw/archive bytes differ, or remote draft lacks the pinned parser asset |

## CI Compute Boundaries

GitHub-hosted CI is reserved for the smallest lane that can prove the current stage. The 2026-09-19 cost incident exhausted Engram's 2,000 included minutes and attributed $93.07 gross to 902 September runs with no product result; macOS was about 10.33x the Linux rate. Do not create a no-op/amended commit or repeat remote work simply to probe an unavailable external service.

| Stage | Required remote work | Boundary and retry rule |
| --- | --- | --- |
| Local proof | Focused behavior and ordinary host/user-path smoke for the affected consumers | Run before freeze so a real loader/install defect can be repaired before expensive gates; local smoke does not replace mandatory release evidence. |
| Ordinary PR / `main` CI | One cancelable Ubuntu validation lane plus cheap authority checks; no Docker image build | Prevent duplicate push and PR execution for the same candidate. Superseded non-manual runs are cancelled. |
| Frozen-candidate validation | Clean-DB coverage and Ubuntu/Windows/macOS validation | Explicit manual dispatch after inputs are frozen. Retain each required check's actual result or validated compatible proof; do not rerun all suites solely because a doc or test label changed. |
| Image and publication gates | Docker image acceptance for `v*` tags or explicit manual frozen-candidate acceptance; required release/publish gates | These are release-boundary work, never ordinary CI. Release/publication jobs are never cancelled mid-publication. |
| SonarQube gate | A designated QA subagent runs `node tools/quality/run-sonarqube.mjs` locally from the clean, frozen exact candidate and retains `.agent/e/sonarqube/<HEAD>.json`; no GitHub Actions workflow performs this gate | Mandatory before publication: Quality Gate must be `OK` except under the one-time v6.49.4 exception below. A Sonar outage otherwise holds release; repair or prove recovery before one same-candidate rerun, not repeated attempts. |
| Post-publication monitoring | Scheduled published-image rescan only | Daily schedule does not build or accept images; retain the freshness/remediation evidence required below. |

Prevent duplicate push-and-PR work for identical bytes. A changed SHA defines a new candidate identity, not a universal invalidation of prior results. Before expensive validation, compare each gate's test inputs, tool/environment inputs, analysis identity and release artifact bytes under its validated evidence contract; record which checks remain valid and run the affected ones. Never relabel a prior receipt, assume cross-worktree equivalence, or reuse proof for changed inputs. Exact-head Sonar analysis remains required outside the recorded exception. A changed image/tag input cannot inherit an earlier `final-image-set.json`; the accepted three-image set, security scans, runtime and migration proofs stay mandatory where applicable. If the existing tool cannot establish compatibility, run its affected gate or repair that evidence contract separately, rather than declaring an unverified PASS or dispatching every gate. Do not start another heavy run for identical inputs without evidence of recovery or a new relevant risk.
For ordinary PRs, configure required checks only from checks that ordinary PRs emit; dispatch-only `test / windows-latest`, `test / macos-14`, and `migrations / clean-db chain` cannot be required ordinary-PR statuses. Read the actual branch rules and PR status instead of assuming a bot comment or dispatch-only status is mandatory; preserve the protocol's PR approval requirement.

### Operator-approved v6.49.4 Sonar exception (2026-09-25)

For PR #531's `v6.49.4` OMP plugin hotfix only, run exact-candidate Sonar if its runner is available. If it cannot run, do not wait for recovery or clear/retry an occupied runner lock merely to obtain Sonar coverage; record the actual lock/unavailability diagnostics. Sonar status is **UNAVAILABLE / NOT_PROVEN**, not `OK`, not zero findings, and not a silent gate pass. Retain the incomplete campaign diagnostics; any exact-root Sonar findings already observed remain unresolved and cannot be relabeled, suppressed or treated as accepted by this exception. The operator accepts the residual code-quality risk of missing exact-candidate Sonar coverage, conditional on all of the following before tag or publication:

- Record the exact frozen candidate SHA (and accepted base) and review its complete changed-files diff: only OMP plugin cwd/packaging/version work, including `plugin/engram/codex.mcp.json` solely as the necessary Codex archive-packaging artifact already in this candidate (not new Codex runtime/host support or a broader package or Sonar waiver), necessary release metadata/this exception, and the test-only correction in `tests/critical/runtime/image_runtime_contract_test.go` aligning stale workflow-source expectations with current default-branch Docker/promotion behavior are in scope. No other Docker/runtime/workflow changes, new server runtime behavior or migrations; a wider candidate needs a new operator decision, not this exception.
- Retain applicable existing evidence for full `go test ./...`, `go vet ./...`, `govulncheck ./...`, and `go build ./cmd/engram ./cmd/engram-server`; Node hook/plugin behavior; GoReleaser raw asset and plugin policy verification. Revalidate evidence against the final candidate using the existing exact-input rules; do not relabel an earlier PASS.
- Require PR approval, zero unresolved required threads, required CI and authority checks plus frozen-candidate validation, and accepted `server`, `operator-console`, and `postgres` images under the existing `final-image-set.json` contract. Record a rollback/canary plan and dispose explicitly of whether tagging may replace the running Engram server through Watchtower before any irreversible effect.

This one-time operator authorization replaces only the Sonar fresh exact-candidate analysis/coverage and `OK` prerequisites when its runner is unavailable for that bounded candidate; record `UNAVAILABLE / NOT_PROVEN` with actual diagnostics and leave observed findings unresolved. All other release, security, image, review, migration and rollout gates remain mandatory. It is not transferable to PR #508, another tag, a changed product scope, or any later release. For every other release, fresh exact-head Sonar analysis and Quality Gate `OK` remain mandatory.

## Release Convergence

Define one independently useful installable slice and its non-goals before implementation, with the required tests and rollout/rollback boundary in the existing feature/run. A sequence of small commits held for one large integration is not a sequence of small releases. Feature completion on a fixture is not delivery to the ordinary consumer.

Classify findings before adding work. **Fix now** covers a failed mandatory gate, demonstrated accepted-behavior/compatibility failure, or credible safety/data-integrity risk. **Not applicable** needs evidence and proper reviewer/maintainer disposition. **Follow-up** is allowed only for nonblocking work accepted by the authorized maintainer, with its reason and a tracked destination. This is not unilateral approval, thread closure, or waiver authority.

Keep the existing thresholds, security scans, supported-platform obligations, migration/rollback requirements and branch protection. A finding excluded from the numeric gate may still block for substantive risk. Conversely, optional polish is not automatically a new release requirement. An unresolved credible high-impact finding remains blocking while investigated. No blanket scanner suppression, false-positive designation, arbitrary pass-count, or policy loosening.

Finish implementation review, accepted blocking repairs, ordinary host/user-path smoke, version metadata and generated release inputs before the expensive final gate. Review corrections against the delta and affected invariants; do not start a fresh whole-product review merely because a reviewer/model changed or release prose changed. Reopening unchanged accepted code needs new evidence or an affected dependency. Required PR approval and any configured required reviews remain required.

Freeze the candidate and use its own runner. No parallel cherry-pick, rebase, or candidate-file mutation while it is under validation. If a new blocker is accepted, preserve completed artifacts and decide which new-candidate gates need revalidation by the input rules above. Do not prematurely start a gate while already-known same-candidate repairs are still in flight.

For evidence reuse, distinguish candidate SHA, test inputs, tool/environment inputs, analysis identity and package/image bytes. Apply only validated compatibility rules from the existing evidence owner. No manual relabeling, cross-worktree assumption or fabricated exact-head PASS. An overly broad fingerprint is a targeted tool defect, not permission to bypass it.

Each long gate has one technical owner and one durable job identity. Root consumes its terminal evidence promptly and decides the next action; a terminal result needs no independent watcher. A launcher/Task wait timeout is not a reason to terminate a progressing job, start a duplicate, or mark a landed review as absent. Retain phase, last meaningful progress and failure cause in the existing run; resume the same job where supported. Never delete a live lock or kill unrelated processes. Reproduce parser/report failures from retained events and compact tests before considering another product-wide run.

Check GitHub's actual PR reviews, comments, required statuses and head before calling review absent or blocked. Do not invoke a second review while one is active for that head. Root groups remaining causal findings by protected guarantee for one focused repair/recheck; non-causal optional suggestions may be disposed of as a maintainer-accepted follow-up. A concrete unresolved defect, required approval/thread or branch rule still blocks merge. Reviewer/bot delay alone does not add an acceptance gate.

For an unchanged intermittent failure, at most one diagnostic retry without new information is useful; after recurrence, change the diagnosis or environment, not the attempt number. This limits blind retry, not mandatory testing. When the same cause invalidates two candidate attempts, Root records a short causal recovery decision in the existing run; it does not create a new process project or waive the requirement.

When mandatory acceptance is satisfied and effects are authorized, proceed to the existing merge/publish/rollout/smoke steps. Do not wait for a fresh user "continue", an optional audit, or future-feature work. If the scope or an irreversible action requires an external decision, request that decision once with candidate, impact and rollback, and keep independent safe work moving.

At meaningful transitions report source-ready, published, marketplace catalog-available, installed and consumer-proven separately. A verified GitHub publication does not wait for operator-owned installation. Delivery goals still need ordinary browser/client verification on the agreed installed instance before claiming consumer proof, not only a tag, fixture PASS or version string. Diagnosis-only and explicitly source-only tasks retain their smaller completion boundary.

## Release Autonomy

| Mutation class | Autonomy | Approval trigger | Evidence |
| --- | --- | --- | --- |
| Local atomic commits | automatic | sensitive content or impersonation risk | git log + gate output |
| Version bump and release prep | automatic for PATCH/MINOR after green gates | MAJOR, breaking contract, ambiguous version | version diff + changelog |
| Tag and remote release | automatic for private PATCH/MINOR milestones in an active goal when gates are green | public/customer-impact ambiguity, tag collision, force/rewrite | remote tag + GitHub release workflow |
| Watchtower deployment | automatic only for planned server releases whose migrations are forward-only and covered by clean-DB CI | manual production SQL, destructive migration, prod flag activation outside rollout, failed health check | workflow success + post-deploy health/smoke |
| Canonical image publication | exact three-image `final-image-set.json` acceptance manifest is retained and verified before publication; released-digest rescan evidence is retained after publication | acceptance manifest is missing/failed, or post-publication rescan evidence is missing, stale, failed, or cannot be matched to the published release |

Project default: `auto_private_patch_minor` for reviewed PRs and active-goal milestones. The active goal is an authorization envelope for routine push / tag / release / deploy steps named by the goal; it does not bypass evidence, rollback, blast-radius, migration, or health gates.

## Version Alignment

- `internal/version/version.go` stores the daemon/server version with `v` prefix.
- `plugin/engram/.claude-plugin/plugin.json` stores the same version without `v`.
- `plugin/engram/.codex-plugin/plugin.json` stores the same version without `v`.
- `plugin/openclaw-engram/package.json` and `plugin/openclaw-engram/openclaw.plugin.json` store the same independently versioned OpenClaw package release.
- Every OpenClaw source change that triggers the publish workflow must advance that package version beyond the registry version; equality is a failed release gate, not a no-op.
- Release jobs never rewrite source or version pins after a tag is created. Exact pins make a release reproducible; they do not prove that a published image remains fresh against newly disclosed vulnerabilities.
- Git tags use `vX.Y.Z`.

## Published-image freshness and remediation

Exact image pins and digests are required for reproducibility; they do not
prove that a published image remains free of HIGH/CRITICAL vulnerabilities.
The scheduled published-image rescan is read-only and does not rewrite source,
workflow, or version pins.

The rescan is post-publication monitoring. Its first run must start within 24
hours of publication, and later evidence must be no older than 36 hours by the
summary's `started_at` and `completed_at` timestamps. Missing, stale,
finding-bearing, scanner-failing, database-failing, or tag-resolution-failing
evidence blocks rollout and continued deployment, not initial digest
publication.

When a released-digest rescan reports a new HIGH/CRITICAL CVE, the release owner
blocks rollout and follows this bounded loop:

1. Record the affected image/CVE and select the fixed base image, toolchain, Go
   module, or npm package version.
2. Open and merge a reviewed maintenance PR updating the required pin and, where
   applicable, `go.mod`/`go.sum` or `package.json`/`package-lock.json`.
3. Rebuild all three canonical images and rerun aggregate scans, runtime proof,
   and cleanup.
4. Cut a patch release, then rerun the released-digest scan against the published
   immutable digests.
5. Retain the maintenance PR, patch release, `final-image-set.json`, per-image
   SARIF/logs, rescan summary, and timestamp evidence in the release handoff.

If no fixed version exists, remain blocked unless an explicit, scoped, expiring
VEX/operator exception is approved. The automated lane implements no allowlist,
scanner-ignore input, or `--ignore-unfixed` bypass; adding an exception path is
a separate reviewed security change, not an operator-side escape hatch.

## Release Notes

- `CHANGELOG.md` gets a dated top entry before tagging.
- Use PATCH for fixes/hotfixes.
- Use MINOR for coherent feature batches or new public/operator-visible capabilities.
- Use MAJOR only after explicit approval for breaking contracts.

## Publish / Smoke / Handoff

- Create an annotated tag and push it to origin.
- Verify the tag exists remotely.
- Verify the GitHub release workflow succeeds for the tag.
- For an OpenClaw package release, verify the `plugin-publish` workflow, `npm view openclaw-engram@X.Y.Z version`, and both package/descriptor version sources before declaring publication complete.
- For rollout, verify deployed `server` version and at least one server/client MCP smoke before declaring deployment complete.
- Plugin/local daemon consumers must be checked for version parity after release; runtime consumer-home updates remain explicit consumer update flows.
- After a verified GitHub Release for a Claude/Codex/OMP plugin tag, verify the first-party `Sync Plugin Marketplace` run and the actual marketplace catalog version. If the token-produced `release.published` event did not start sync, use the existing `.github/workflows/sync-marketplace.yml` manual dispatch with the **published** tag: `gh workflow run sync-marketplace.yml --ref main -f ref=vX.Y.Z`. Its tag/version and published-release checks, trusted package validator and byte-copy verification still apply; verify the run and catalog before claiming availability. Do not assume a successful release triggered the sync, invent a plugin-only publishing path, or call catalog availability an operator-owned install.
- Canonical image publication is incomplete until the accepted three-image `final-image-set.json` is recorded. The released-digest rescan is post-publication monitoring: its first run is due within 24h and later evidence must be no older than 36h from `started_at`/`completed_at`; missing, stale, finding-bearing, scanner-failing, database-failing, or tag-resolution-failing evidence blocks rollout/continued deployment, not initial digest publication.

## SonarQube Gate Recovery

Outside the one-time v6.49.4 exception above, a designated QA subagent runs `node tools/quality/run-sonarqube.mjs` from the clean, frozen exact candidate. Root accepts its evidence and directs the next delivery action; no GitHub Actions workflow performs this gate. The runner must preserve the exact-head Quality Gate `OK` requirement; successful runs retain the exact-head receipt at `.agent/e/sonarqube/<HEAD>.json`. Human action is required only when the agent lacks the infrastructure, credential, or irreversible-effect authority needed for the next step.

Recovery flags are usable only when the active runner's `node tools/quality/run-sonarqube.mjs --help` advertises every flag and mode-specific behavior invoked. If help errors, has no usable output, or does not advertise a requested flag, that recovery mode is unavailable.

When recovery modes are unavailable, do not guess flags or their semantics. First inspect the ordinary failure and retained diagnostics, including the command, working directory, candidate HEAD/tree, redacted output, exit status and any `.scannerwork/report-task.txt` or `.agent/e/sonarqube/<HEAD>.json`. Missing or failed `--help` alone is not a reason to start the full default campaign. Diagnose the cause with a focused check or retained-event replay where possible. Use the default exact-candidate command when required work remains and its prerequisites are satisfied, preserving completed evidence under existing validated contracts. Do not fabricate completion or a recovery mode.

An advertised recovery mode is never a bypass: it must retain fresh exact-candidate analysis and Quality Gate `OK`, all mandatory coverage, security, review, migration and rollback obligations, and exact evidence. It must not skip required tests, analysis or status publication; alter thresholds or analysis scope; suppress findings; or fabricate an exact-head PASS.

## Terminal Verdict

- `PROJECT_RELEASE_PROTOCOL_PASS`: all mandatory rows have evidence, with only the explicit v6.49.4 PR #531 Sonar exception above permitting incomplete exact-candidate analysis/coverage and `UNAVAILABLE / NOT_PROVEN` instead of fresh analysis and `OK` when Sonar is unavailable; observed findings remain unresolved, and every other gate remains mandatory. Never report Sonar as PASS.
- `PROJECT_RELEASE_PROTOCOL_BLOCKED`: at least one mandatory row is missing, stale, failed, or cannot be verified.
- `PROJECT_RELEASE_PROTOCOL_DRY_RUN`: intended actions are fully described and no mutation was performed.
