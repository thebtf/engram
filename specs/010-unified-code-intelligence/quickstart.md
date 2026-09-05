# UCI-1 Validation Quickstart

This is a future implementation and acceptance guide, not an execution receipt. **No command in this document was run during the Phase 0/1 planning pass.** It must not be used to claim UCI implementation, installation, or release readiness before the described evidence exists.

## Safety Boundary

- Use a disposable native Windows environment, an isolated PostgreSQL 17 fixture/database with pgvector, and disposable Git repositories/worktrees. Never point UCI migration, scanner, or recovery tests at production data.
- Use authorized synthetic/fixture source only. Do not place credentials, `.env` files, session transcripts, or private unapproved material in the corpus.
- Run Git through argument vectors; never build shell command strings from source paths, branch names, remotes, or file content.
- The current `make install` target calls POSIX-oriented lifecycle commands (`pkill`, `lsof`) and is not accepted as a native Windows installation proof. A UCI implementation slice must provide a Windows-safe disposable install/standard-client harness before UCI-1 acceptance.

## Prerequisites

Record the following before running a future validation pass:

1. Candidate source identity: feature branch, exact commit/tree, generated protobuf state, and parser bundle digest.
2. Go 1.26.6, Git, a supported Windows amd64 toolchain, CGO-capable parser build environment, and the project’s normal build dependencies. Typical inspection commands are:

   ```powershell
   go version
   git --version
   go env GOOS GOARCH CGO_ENABLED
   ```

3. Isolated PostgreSQL 17 with pgvector, migrations applied only to the fixture, and an explicit fixture connection configuration. Record schema/migration versions without credentials.
4. A real approved embedding provider/model/profile for semantic acceptance. A fake embedder may validate transport shape only; it cannot satisfy conceptual-search acceptance.
5. Three standard MCP clients capable of connecting to one daemon, plus a real Git repository and a linked worktree that uses a `.git` pointer file. The fixture must support separate saved dirty contents while retaining the same source/path/symbol/HEAD adversarial shape.

## Focused RED/GREEN Gate

The first executable slice adds a focused test next to the existing daemon code-intelligence adapter. The intended command after that test exists is:

```powershell
go test ./internal/handlers/codeintel -run '^TestUCITwoRealWorktreesRemainIsolated$'
```

### Fixture Setup

1. Create a temporary primary repository, commit a common base, and create a linked worktree with Git argument vectors.
2. In both worktrees, retain the same relative file path, symbol name, branch label, and committed HEAD; write different **saved dirty** bodies and different callees.
3. Start one daemon/server fixture backed by the isolated PostgreSQL fixture. Bind two independent client contexts with different CWDs; then attach a third client.
4. Use the existing public `codebase_index` and `codebase_search` tool names. Do not make the test pass through a new test-only UCI helper.

### RED Evidence

Before the registry/mapping cutover, this test must demonstrate the old raw-project collision or absence of required Source/Checkout/View response information. The expected failure is behavioral: legacy code indexes state by raw project string and cannot retain two distinct dirty worktree views. A compile-only missing-symbol failure is not sufficient RED evidence.

### GREEN Evidence

After the relevant slices land, the same test must show all of the following:

- Client A receives its own saved body, callee/edge, Source, Checkout, and View.
- Client B receives only B’s distinct saved body/callee/edge and context.
- No returned row/edge crosses A/B; the fixture records zero cross-worktree results.
- Connecting client C does not alter A or B’s default context.
- Ambiguous/no-CWD selection returns a closed `CONTEXT_REQUIRED` outcome and leaks no other checkout’s content, identifiers, or counts.

## Focused Implementation Gates

Run only the gate appropriate to the completed slice before continuing. The exact test names below are planned UCI test surfaces and become runnable when their implementation slice adds them.

| Slice | Intended future command | Expected evidence |
|---|---|---|
| Registry / compatibility | `go test ./internal/... -run 'TestUCI(Context|Alias|Checkout)'` | Authorized resolution succeeds; mismatched, ambiguous, private, revoked, and legacy-only paths fail before content lookup. |
| Fenced PostgreSQL publication | `go test ./internal/db/gorm/... -run 'TestUCI(Publish|Lease|Replay|DeleteAll)'` | Incomplete upload does not switch current; old epoch is rejected; lost-ACK replay is idempotent; failed scan is not delete-all. |
| Go parser / FTS / graph | `go test ./internal/... -run 'TestUCI(Go|ViewPinned|Graph)'` | Exact/FTS/graph facts cite the same View; malformed syntax is fresh and partial; unchanged caller is re-resolved after callee change. |
| Native MCP isolation | `go test ./cmd/engram/... -run 'TestUCI(ClientContext|TwoClient)'` | Per-client defaults stay isolated through connect/select/reconnect and existing tool names route through typed context. |
| Real vector | `go test ./internal/... -run 'TestUCI(Semantic|VectorProfile)'` | Provider-generated conceptual result is profile/View scoped; disabled provider returns visible lexical-only/degraded state. |
| JS / TS / TSX parser bundle | `go test ./internal/... -run 'TestUCI(TreeSitter|TypeScript|TSX)'` | Pinned bundle records its digest; JS/TS/TSX aliases/re-exports report correct coverage or explicit partial status. |
| Watcher / recovery | `go test ./internal/... -run 'TestUCI(Watcher|Recovery|Reconcile)'` | Save/delete/rename/overflow/restart/offline paths reconcile current bytes, preserve the unaffected worktree, and do not re-embed unchanged input. |

The commands are intentionally scoped placeholders for packages/tests created by their named slices. They are not evidence that those packages or tests exist today. The current repository-wide commands remain `make proto`, `make build-windows`, `make engram`, and `make test`; use them only when the owning implementation/release stage authorizes them.

## Build and Transport Gate

After the protobuf and parser changes exist:

```powershell
make proto
make build-windows
make engram
```

Record:

- Generated protobuf changes derive from the existing `EngramService`; no existing field number was reused and no second service appeared.
- Windows amd64 server/parser bundle build identity and digest; Linux amd64 and supported macOS parser build evidence separately.
- MIT license/SBOM evidence for the pinned Tree-sitter Go binding and JS/TS grammar revisions.
- The daemon/server process identities and the client-to-daemon transport that was actually exercised.

## Native Installed Two-Client Gate

Use the Windows-safe disposable installation harness introduced by implementation, not a direct Go service call or a module fake.

1. Install the built server, daemon, and parser worker in a disposable native target.
2. Connect client A and client B concurrently to one daemon, rooted in the adversarial primary/linked worktrees. Attach client C after both are bound.
3. Run `codebase_context` resolution (or ordinary CWD resolution), index/reconcile, exact/FTS/semantic search, `codebase_graph`, and `codebase_read` for each client.
4. Confirm that every result identifies a consistent Source/Checkout/View, and that A/B never observe the other dirty state.
5. Repeat denied, revoked, mismatched, ambiguous, private-checkout, stale-view, pagination, and graph-hop cases. A refusal must disclose no outside body, identifier, count, relationship, or local Windows path.

Expected installed-path evidence is a redacted receipt containing built artifact hashes, fixture Source/Checkout/View IDs and manifests, two-client/third-client results, authorization-negative outcomes, and the exact command/harness version. This is required for SC-06; unit tests do not substitute for it.

## Semantic, Watcher, and Recovery Gate

1. Run the pre-registered 12-task corpus with the fixed provider/model/profile/budget. The conceptual no-keyword task must show a provider-generated vector result; lexical fallback is recorded separately and cannot count as semantic success.
2. Change only worktree A, then save, delete, rename, switch branch, induce watcher overflow, restart daemon/server, and simulate offline/reconnect. Keep B untouched.
3. Confirm a new coherent A View, unchanged B View, visible `stale`/`partial`/`offline`/`unsupported` or vector-degraded state where applicable, no old embedding under A’s new file version, and no duplicate mutation after lost ACK replay.
4. Collect at least 100 healthy batches under the accepted profile for the saved-update p95 target. Record cold/warm state, machine/DB sizes, provider status, sample count, p95, max, and failures; degraded responses are excluded from healthy latency success.

## Release Evidence Gate

A UCI-1 claim requires one exact-head evidence package, not a collection of unrelated green unit logs:

- real PostgreSQL registry/publication/lease/replay evidence;
- parser bundle and supported-language coverage evidence;
- installed Windows two-client proof;
- actual semantic provider result and separate degraded-mode evidence;
- authorization-negative matrix;
- all assigned UCI-1 acceptance scenarios and the 12-task baseline/value outcome;
- restart/recovery and retention/rollback evidence;
- exact candidate build/regression checks and the repository’s mandatory exact-head SonarQube Quality Gate `OK` before tag/publication;
- known limitations, visible coverage states, and explicit confirmation that UCI-2/3 were not claimed.

If any release-facing byte changes after this evidence is collected, regenerate the affected evidence against the exact final candidate.