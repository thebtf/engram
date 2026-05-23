# v6.3.0 baseline fixtures

These JSON files anchor the FR-9 byte-identity CI gate. Two snapshots are
captured at the v6.3.0 release:

- **session_start_response.json** — payload returned by `GetSessionStartContext`
  for a curated test session with three memories, two observations, and two
  behavioral rules.
- **tools_list_response.json** — MCP `tools/list` response with three tool
  declarations (`recall_memory`, `store_memory`, `get_rules`) and the v6.3.0
  version string.

Both files are post-`NormalizeForDiff` (see `pkg/cognitive/normalize.go`):
volatile fields (`generated_at`, `server_version`, `session_id`, `log_ts`)
are stripped at every depth, arrays of objects with `memory_id` are sorted
ascending, and map keys are emitted in canonical alphabetical order.

## Synthetic vs captured

The current files are **representative synthetic fixtures** with the exact
shape v6.3.0 produces but populated with curated example data rather than
captured from a live v6.3.0 binary. The synthetic-vs-captured tradeoff is
intentional and documented in TD-003 + TD-004:

- The fixtures still enforce the FR-9 gate property: any change to
  `NormalizeForDiff` semantics or to the v7 response shape that produces a
  different normalized byte sequence will fail `TestNormalizedByteIdentity_v6_3_0_Baseline`.
- The fixtures do NOT yet prove v7-master-off produces bit-identical output
  to the actual v6.3.0 binary; that requires the worktree-based capture
  pipeline below (T021 `make rebaseline-v6`).

Promotion path: when the CI infrastructure can spin up v6.3.0 binary + a
PostgreSQL test stand, run `make rebaseline-v6` once and commit the
captured fixtures in their place. The test (T020) does not change.

## Capture procedure (real v6.3.0 baseline)

Per post-tasks-review Fix #6, the capture is non-destructive and runs in an
isolated worktree to keep the current branch untouched:

```bash
# 1) Materialise v6.3.0 in an isolated worktree (does NOT mutate current branch)
git worktree add --detach /tmp/engram-v6.3.0 v6.3.0

# 2) Build the v6.3.0 binary inside that worktree
(cd /tmp/engram-v6.3.0 && go build -o /tmp/engram-v6-binary ./cmd/engram-server)

# 3) Spin up a clean PostgreSQL test stand, ENGRAM_AUTH_DISABLED=true, etc.
#    Run the binary and exercise the curated test session.

# 4) Capture the session-start gRPC response and the MCP tools/list response.

# 5) Apply NormalizeForDiff to each payload (pkg/cognitive/normalize.go) and
#    write the normalized bytes back into this directory.

# 6) Clean up the worktree
git worktree remove /tmp/engram-v6.3.0
```

The `make rebaseline-v6` target (T021) automates steps 1-6.

## When to rebaseline

ONLY rebaseline with an explicit ADR amendment plus PR review (per Clarify
C3). Drift between fixtures and code is a regression signal, not a fixture
problem — investigate the divergence first.

## Required fixture properties

- Start with `{`
- Contain no `generated_at`, `server_version`, `session_id`, or `log_ts`
  keys at any depth
- Have `memories` arrays sorted by `memory_id` ascending
- Stay under 50 KB
- End with a trailing newline (POSIX text-file convention)

`TestNormalizedByteIdentity_v6_3_0_Baseline` (T020) verifies all of these.

## Curated test session shape

For reproducibility when capturing real v6.3.0 fixtures, the session uses:

- **Project name**: `engram-baseline-test`
- **Memory count**: 3 (`mem-001` through `mem-003`)
- **Observation count**: 2 (`id=2670, 2671`)
- **Behavioral rule count**: 2 (`prefer-context-aware-errors`, `verify-source-before-recommending`)

These IDs and contents are deterministic seeds so the captured response is
byte-stable across runs of the same v6.3.0 binary.
