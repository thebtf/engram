# Engram consumer audit: muxcore v0.27.1

Date: 2026-07-14

## Verdict

`BLOCKED_PROVIDER_CONTRACT_INCOMPLETE`

The `muxcore/v0.27.1` release is real, the Go module resolves, and its tagged
commit matches the module origin. Engram can safely adopt the dependency and
the provider-owned restart/update path while retaining its current persistent,
always-connected transport policy. It cannot yet satisfy the requested
per-host dormant/termination contract without copying provider-private launcher
protocol or asserting notification buffering/replay safety that is not proven.

No Engram patch release, tag, or deployment should be created from this
candidate while the required zero-survivor idle/dormant acceptance remains
blocked.

Machine-readable Windows evidence:
[`muxcore-v0.27.1-windows-lifecycle-evidence.json`](./muxcore-v0.27.1-windows-lifecycle-evidence.json).

## Provider gate

| Evidence | Result |
| --- | --- |
| GitHub release | `v0.27.1`, published 2026-07-14 05:22:20Z |
| Release tag object | `d79bf7e0a3d1311f36a863dd28a7b4a90d2d57a3` |
| Release tag commit | `5f0ebb3c6c51cd6345b21bdf8ef0d0b21dc89338` |
| Module tag object | `32bf1fddd25c249b038cb6e568bcced44e45f760` |
| Module tag commit | `5f0ebb3c6c51cd6345b21bdf8ef0d0b21dc89338` |
| Module sum | `h1:z5ftlF22TRgMkiwWpou/t+vPWtvMy5qkv5jDwedg0lk=` |
| Module Go version | `1.25.4` |
| Newer corrective muxcore module | none found |

Verification commands:

```text
git ls-remote --tags https://github.com/thebtf/mcp-mux refs/tags/v0.27.1 refs/tags/v0.27.1^{}
gh release view v0.27.1 --repo thebtf/mcp-mux --json tagName,targetCommitish,publishedAt,url,name,isDraft,isPrerelease
go list -m -json github.com/thebtf/mcp-mux/muxcore@v0.27.1
git ls-remote --tags https://github.com/thebtf/mcp-mux refs/tags/muxcore/v0.27.* refs/tags/muxcore/v0.28.*
```

Sources:

- [mcp-mux v0.27.1 release](https://github.com/thebtf/mcp-mux/releases/tag/v0.27.1)
- Tagged `muxcore` README at provider commit
  `5f0ebb3c6c51cd6345b21bdf8ef0d0b21dc89338`
- [mcp-mux issue #140](https://github.com/thebtf/mcp-mux/issues/140)

## Current consumer classification

Candidate classification: **`SHIM_LIFECYCLE_CHANGE` plus dependency upgrade**.

- `go.mod` / `go.sum` move Engram from muxcore v0.26.1 to v0.27.1.
- Ordinary host mode initializes only the muxcore shim. It no longer starts the
  Engram module registry, dispatcher, and lifecycle pipeline in every Codex
  subagent process.
- Daemon mode remains the only owner of durable Engram modules and keeps
  `Persistent: true`; ordinary shim configuration explicitly sets
  `Persistent: false`.
- muxcore v0.27.1 can park a downstream data-plane session after daemon safety
  checks, including for a persistent owner. Engram still leaves idle suspend at
  zero because end-to-end notification buffering/replay safety is not proven
  and no released supervisor consumes the private dormant handshake before the
  shim exits.
- Stale daemon reconciliation uses muxcore `RestartWithSuccessor` instead of a
  second Engram-local restart/kill implementation. Concurrent launchers wait for
  the winning replacement; a non-converging replacement fails closed.
- `ensure-binary.js` installs the binary and leaves daemon reconciliation to
  the next normal Engram launch.
- The legacy Engram `graceful-restart` product socket remains available only for
  explicit operator use on supported Unix platforms. The plugin update path no
  longer calls it; it is retained as unrelated recovery behavior rather than
  removed without a dedicated proving test.
- `run-engram.js` needs no current private-frame handling and has not received a
  launcher protocol change.

This is **not** `DEPENDENCY_ONLY`: Engram source changes are needed to separate
shim startup from daemon/module startup and to delegate restart ownership.
It is **not** `LAUNCHER_PROTOCOL_CHANGE`: implementing one now would require an
unreleased reusable provider contract or a forbidden copy of private
`cmd/mcp-mux` protocol.

## Why the full objective is blocked

The tagged README requires an explicit consumer safety decision before a
persistent transport may suspend. Production `ModuleDeps.Notifier` is currently
nil, but Engram has no end-to-end buffering/replay proof for all provider and
module notifications, so this candidate does not assert that safety or enable
idle suspension.

Issue #140 remains open and requests the missing native-consumer boundary:

- a versioned host-stdio supervisor capability;
- private ready/commit/ack/nack frame consumption;
- exactly-once wake with FIFO demand buffering;
- old-launcher/new-engine compatibility;
- client-session dormancy independent from persistent owner/daemon lifetime;
- active-engine resolution hooks compatible with `RestartWithSuccessor`.

The provider's 2026-07-14 closeout comment explicitly permits v0.27.1 adoption
only with persistent always-connected transport and explicitly forbids enabling
persistent client dormancy or copying the private launcher protocol while
#140 is open.

## Windows lifecycle evidence

### Passed

- Eight isolated wrapper/shim pairs initialized concurrently.
- The winning version marker matched daemon PID `75440` after the concurrent
  launch; losing daemon-spawn candidates did not overwrite it.
- Request IDs `31000..31007` and `32000..32007` each produced exactly one
  response.
- Closing host stdin caused all eight wrappers and shims to exit with code `0`;
  daemon PID `75440` remained with one persistent owner and zero sessions.
- No attributable Serena, LSP, or helper descendant remained beyond the
  intended daemon.
- A clean reconnect reused daemon PID `75440` without a redundant restart.
- A replacement binary started daemon PID `66016`; live wrapper PID `87276`
  resumed and request `34002` produced exactly one response.
- Eight simultaneous successor launchers coalesced on one replacement winner:
  daemon PID `69732` became PID `77460`; all eight successor clients returned
  one initialize and one tools response, and predecessor wrapper PID `100536`
  continued with exactly one response to request `44000`.
- The exact customer `ensure-binary.js`/`run-engram.js` flow installed a
  byte-identical `v6.42.0-successor` binary and matching `.version` marker.
  Eight concurrent wrappers then converged daemon PID `88132` to PID `92820`
  with one replacement winner, six joiners, zero redownloads, and one exact
  post-update response from predecessor wrapper PID `97316` to request `54000`.
- Distinct `plugin-data-a` and `plugin-data-b` successor paths raced stale
  daemon PID `82552` after the final review fixes. Path A won as daemon PID
  `103480`; two path-B lock losers joined that current-version/status-PID
  winner, all eight clients reported `v6.42.0-reviewed-new` and returned one
  initialize and one tools response, and predecessor request `84000` returned
  exactly once. Closing all nine clients left only the persistent daemon with
  zero sessions before isolated shutdown.
INS.POST 138:
- Focused and race tests prove a provider-ready replacement is rejected until
  its current-version marker/status PID converges, and parent shutdown
  cancellation interrupts reconciliation instead of waiting for the two-minute
  bound.
- All 24 captured host stdout lines were valid JSON-RPC; no private launcher or
  dormant frame leaked.
- Three additional cycles of three clients each returned to one daemon, zero
  wrappers, zero shims, and zero sessions; owner count did not grow.

### Failed or unproven

- While host stdin remained open, all eight wrapper/shim pairs remained alive.
- Engram's safe configuration is `IdleSuspendDelay=0`, `IdleDormantGrace=0`, and
  `AllowPersistentIdleSuspend=false`; therefore there is no documented
  idle-plus-grace bound to wait for.
- The live update used one provider fallback owner spawn rather than restored
  token refresh: `shim_reconnect_refreshed=0`,
  `shim_reconnect_fallback_spawned=1`, `shim_reconnect_gave_up=0`, and
  `handoff.restored_owner_count=0`.
- `SkipSnapshot=true` remains required by Engram's v6.4.6 stale
  `initialize.serverInfo.version` regression. Removing it would need a new
  regression proving a new host cannot receive stale initialize data after an
  update.

## Migration note for a future safe release

No database migration or server configuration migration is required.

When a tagged provider release closes #140:

1. Upgrade `github.com/thebtf/mcp-mux/muxcore` from v0.27.1 to the first
   released tag that closes #140, then verify tag/module commit parity.
2. Replace the current always-connected host shim with the exported,
   version-negotiated native supervisor contract. Do not copy private frames.
3. Keep the Engram daemon/owner persistent; make only the client-session child
   disposable.
4. Retain old-launcher compatibility: no private dormant frame when capability
   is absent.
5. Re-run the eight-host Windows smoke through the provider-documented idle and
   grace bounds, then verify one wake, FIFO demand, no duplicate/lost response,
   active-engine switch continuity, and zero scoped child survivors.
6. Only after maker/checker/context-blind review and project release gates pass,
   bump all Engram version surfaces and create the PATCH release.

## Rollback instructions

The current worktree candidate is unreleased and has no data migration.
Rollback is source-only:

1. Discard or revert the candidate commit before merge.
2. Restore muxcore v0.26.1 in `go.mod` / `go.sum`.
3. Restore the previous `cmd/engram/main.go`, tests, and
   `plugin/engram/scripts/ensure-binary.js` behavior from the release base.
4. Run the focused Go and Node suites, then build the release-base v6.42.0
   client.
5. Reinstall the prior known-good plugin through the normal consumer update
   flow and start a fresh host session.
6. Verify `engram --version`, `initialize.serverInfo.version`, one persistent
   daemon, and no duplicate owner before considering rollback complete.

If a future PATCH release has already been published, use a normal revert commit
and a new PATCH version. Never move or force-rewrite a published tag.

## Release stop line

Do not create a PR claiming the original lifecycle objective is complete, bump
Engram version surfaces, or publish/deploy this candidate until a released
provider contract closes the blocked acceptance or the user explicitly approves
a narrower hotfix whose release notes state that dormant zero-survivor behavior
remains unsolved.
