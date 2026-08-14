# Release Protocol

## Applies When

- Releasing the Engram server, stdio daemon, Claude plugin, Codex plugin, OpenClaw npm plugin, or GitHub release artifacts.
- Any change merged to `main` that operators should receive through the tagged release / Watchtower path.

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
| PR review | PR merged with review approval and zero unresolved review threads | review not approved or unresolved threads remain |
| CI | `gh pr checks <PR>` or checks on release commit | any required check fails |
| Go tests | `go test ./...` | non-zero exit |
| Go vet | `go vet ./...` | non-zero exit |
| Vulnerability scan | `govulncheck ./...` | reachable vulnerability reported |
| Build | `go build ./cmd/engram ./cmd/engram-server` | non-zero exit |
| Plugin hooks | `node --test plugin/engram/hooks/*.test.js` | non-zero exit when hook/plugin code changed |
| OpenClaw plugin | `npm test --prefix plugin/openclaw-engram` | non-zero exit when OpenClaw plugin code changed |
| OpenClaw publish version | `node plugin/openclaw-engram/scripts/check-publish-version.mjs <local> <npm>` | local version is malformed, equal to, or older than the registry version |
| Docker image acceptance | The exact `final-image-set.json` for the release workflow plus its three zero-finding SARIF files, with server, operator-console, and postgres identities, runtime proof, and cleanup PASS | the manifest or any SARIF is missing, stale, incomplete, failed, non-zero, or cannot be verified |
| Released-digest freshness rescan | The latest scheduled published-image rescan status for all three released digests; clean runs retain per-image SARIF/log, failed attempts retain per-image error/log, and every run has an aggregate summary | any image has a finding/error, required PASS evidence is missing/stale, or the rescan cannot be verified |
| Diff hygiene | `git diff --check` | whitespace/conflict marker errors |

The Docker acceptance manifest, its three SARIF files, and latest
released-digest rescan status are mandatory evidence for Docker, server, and
Watchtower publication. A failed rescan blocks the release owner named by the
active release run; that owner must complete and record the remediation loop
below before retrying publication or rollout. The scan job reports evidence and
fails closed; it does not silently create, merge, or release the maintenance PR.

## Release Autonomy

| Mutation class | Autonomy | Approval trigger | Evidence |
| --- | --- | --- | --- |
| Local atomic commits | automatic | sensitive content or impersonation risk | git log + gate output |
| Version bump and release prep | automatic for PATCH/MINOR after green gates | MAJOR, breaking contract, ambiguous version | version diff + changelog |
| Tag and remote release | automatic for private PATCH/MINOR milestones in an active goal when gates are green | public/customer-impact ambiguity, tag collision, force/rewrite | remote tag + GitHub release workflow |
| Watchtower deployment | automatic only for planned server releases whose migrations are forward-only and covered by clean-DB CI | manual production SQL, destructive migration, prod flag activation outside rollout, failed health check | workflow success + post-deploy health/smoke |

Project default: `auto_private_patch_minor` for reviewed PRs and active-goal milestones. The active goal is an authorization envelope for routine push / tag / release / deploy steps named by the goal; it does not bypass evidence, rollback, blast-radius, migration, or health gates.

## Version Alignment

- `internal/version/version.go` stores the daemon/server version with `v` prefix.
- `plugin/engram/.claude-plugin/plugin.json` stores the same version without `v`.
- `plugin/engram/.codex-plugin/plugin.json` stores the same version without `v`.
- `plugin/openclaw-engram/package.json` and `plugin/openclaw-engram/openclaw.plugin.json` store the same independently versioned OpenClaw package release.
- Every OpenClaw source change that triggers the publish workflow must advance that package version beyond the registry version; equality is a failed release gate, not a no-op.
- Git tags use `vX.Y.Z`.

## Published-image freshness and remediation

Exact image pins and digests are required for reproducibility; they do not
prove that a published image remains free of vulnerabilities. The scheduled
published-image rescan is read-only and does not rewrite source, workflow, or
version pins. Release jobs never rewrite pins after a tag.

When a released-digest rescan reports a newly fixed CVE, the release owner
blocks Docker/server/Watchtower publication and follows this bounded loop:

1. Record the affected image/CVE and select the fixed base or toolchain version.
2. Open and merge a reviewed maintenance PR containing the required pin change.
3. Rebuild, run the aggregate three-image scan, and rerun the runtime evidence.
4. Cut a patch release and publish only with a new passing
   `final-image-set.json`, its three SARIF files, and released-digest rescan
   status.
5. Record the maintenance PR, patch release, and clean rescan artifact in the
   release handoff.

If no fixed version exists, publication remains blocked. The current automated
lane implements no VEX record, allowlist, scanner-ignore input, or
`--ignore-unfixed` bypass; adding an exception path is a separate reviewed
security change, not an operator-side escape hatch.

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
- For Watchtower/server rollout, verify deployed server version and at least one server/client MCP smoke before declaring deployment complete.
- Plugin/local daemon consumers must be checked for version parity after release; runtime consumer-home updates remain explicit consumer update flows.

## Terminal Verdict

- `PROJECT_RELEASE_PROTOCOL_PASS`: all mandatory rows have evidence.
- `PROJECT_RELEASE_PROTOCOL_BLOCKED`: at least one mandatory row is missing, stale, failed, or cannot be verified.
- `PROJECT_RELEASE_PROTOCOL_DRY_RUN`: intended actions are fully described and no mutation was performed.
