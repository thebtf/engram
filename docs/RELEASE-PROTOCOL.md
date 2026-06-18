# Release Protocol

## Applies When

- Releasing the Engram server, stdio daemon, Claude plugin, Codex plugin, or GitHub release artifacts.
- Any change merged to `main` that operators should receive through the tagged release / Watchtower path.

## Additional Release Surfaces

| Surface | Version source | Publish command | Verification |
| --- | --- | --- | --- |
| Server / daemon | `internal/version/version.go` | annotated `vX.Y.Z` tag triggers `.github/workflows/release.yaml` | `git ls-remote --tags origin refs/tags/vX.Y.Z`; release workflow success |
| Claude plugin | `plugin/engram/.claude-plugin/plugin.json` | included in repository tag / plugin package | JSON `version` equals tag without `v` |
| Codex plugin | `plugin/engram/.codex-plugin/plugin.json` | included in repository tag / plugin package | JSON `version` equals tag without `v` |
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
| Diff hygiene | `git diff --check` | whitespace/conflict marker errors |

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
- Git tags use `vX.Y.Z`.

## Release Notes

- `CHANGELOG.md` gets a dated top entry before tagging.
- Use PATCH for fixes/hotfixes.
- Use MINOR for coherent feature batches or new public/operator-visible capabilities.
- Use MAJOR only after explicit approval for breaking contracts.

## Publish / Smoke / Handoff

- Create an annotated tag and push it to origin.
- Verify the tag exists remotely.
- Verify the GitHub release workflow succeeds for the tag.
- For Watchtower/server rollout, verify deployed server version and at least one server/client MCP smoke before declaring deployment complete.
- Plugin/local daemon consumers must be checked for version parity after release; runtime consumer-home updates remain explicit consumer update flows.

## Terminal Verdict

- `PROJECT_RELEASE_PROTOCOL_PASS`: all mandatory rows have evidence.
- `PROJECT_RELEASE_PROTOCOL_BLOCKED`: at least one mandatory row is missing, stale, failed, or cannot be verified.
- `PROJECT_RELEASE_PROTOCOL_DRY_RUN`: intended actions are fully described and no mutation was performed.
