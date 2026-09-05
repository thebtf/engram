# UCI-1 Validation Quickstart

This is a future implementation and acceptance guide, not an execution receipt. **No command in this document was run during the Phase 0/1 planning pass.** It must not be used to claim UCI implementation, installation, or release readiness before the described evidence exists.

## Safety Boundary

- Use a disposable native Windows environment, an isolated PostgreSQL 17 fixture/database with pgvector, and disposable Git repositories/worktrees. Never point UCI migration, scanner, or recovery tests at production data.
- Use authorized synthetic/fixture source only. Do not place credentials, `.env` files, session transcripts, or private unapproved material in the corpus.
- Run Git through argument vectors; never build shell command strings from source paths, branch names, remotes, or file content.
- Slice 4 owns a Windows-safe disposable installation and standard-MCP-client harness. Slice 8 uses the same versioned harness. The current `make install` target calls POSIX-oriented lifecycle commands (`pkill`, `lsof`) and cannot prove a native Windows installation.

## Slice-0 RED prerequisites

Record the following before running the first RED fixture:

1. Candidate source identity: feature branch and exact commit/tree.
2. Go 1.26.6, Git, a supported Windows amd64 toolchain, and the project dependencies that exercise the existing daemon/server path. Typical inspection commands are:

   ```powershell
   go version
   git --version
   go env GOOS GOARCH CGO_ENABLED
   ```

3. An isolated PostgreSQL 17 fixture/database with the existing code-chunk store wired to the disposable server. Apply migrations only to the fixture and record schema/migration versions without credentials.
4. A real approved Git repository and a linked worktree that uses a `.git` pointer file. The fixture must retain separate saved dirty contents while preserving the same source/path/symbol/HEAD adversarial shape.
5. `ENGRAM_CODE_INTEL_ENABLED=true` for the disposable daemon and server fixture before tool discovery or tool calls.

## Later-slice prerequisites

1. Slice 4 creates and focus-tests the Windows-safe disposable installation and standard-MCP-client harness. It supplies three standard MCP clients for the installed two-client/third-client acceptance path.
2. Slice 5 requires a real approved embedding provider/model/profile for semantic acceptance. A fake embedder can validate transport shape only and cannot satisfy conceptual-search acceptance.
3. Slice 6 requires a CGO-capable parser build environment, generated parser-bundle identity, and pinned grammar/license/SBOM evidence.
4. The protobuf-generated state and built native artifacts belong to their Slice 4/6 build and transport gates. They are not Slice-0 prerequisites.
5. A completion callback is optional only where the installed host does not support it. The no-callback case is a required explicit `unknown` assertion. A verified supported-host callback must also prove a `partial` completion outcome without treating retrieval result state or coverage as host completion.

## Focused RED/GREEN Gate

Slice 0 adds a focused test next to the existing daemon code-intelligence adapter. Run the intended command only after that slice creates the test:

```powershell
go test ./internal/handlers/codeintel -run '^TestUCITwoRealWorktreesRemainIsolated$'
```

### Fixture Setup

1. Start the disposable daemon/server fixture with `ENGRAM_CODE_INTEL_ENABLED=true` and the existing code-chunk store wired.
2. Create a temporary primary repository, commit a common base, and create a linked worktree with Git argument vectors.
3. In both worktrees, retain the same relative file path, symbol name, branch label, and committed HEAD. Write different **saved dirty** bodies and different callees.
4. Bind two independent client contexts with different CWDs. For A and then B, invoke the existing `codebase_index`, assert the asynchronous `started` dispatch, and poll the existing `codebase_status` to `idle` without an error before its fixture deadline.
5. Invoke the existing `codebase_search` from both contexts after the bounded index completions. Attach a third client after the first two contexts are bound.
6. Evaluate two-worktree isolation only after the activation, index dispatch, status completion, and search dispatch checks pass.

### RED Evidence

Before the registry/mapping cutover, the activation, tool-dispatch, and bounded-completion assertions must pass. The test then demonstrates the old raw-project collision or absence of required Source/Checkout/View response information. The expected RED is behavioral: legacy code indexes state by raw project string and cannot retain two distinct dirty worktree views. A disabled flag, missing tool, incomplete index, or compile-only missing-symbol failure is not sufficient RED evidence.

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
| Slice 0 existing-seam RED | `go test ./internal/handlers/codeintel -run '^TestUCITwoRealWorktreesRemainIsolated$'` | `ENGRAM_CODE_INTEL_ENABLED=true`; existing `codebase_index` and `codebase_search` dispatch; bounded `codebase_status` reaches `idle`; then the legacy path fails on worktree isolation. |
| Registry / compatibility | `go test ./internal/... -run 'TestUCI(Context|Alias|Checkout)'` | Authorized resolution succeeds; mismatched, ambiguous, private, revoked, and legacy-only paths fail before content lookup. |
| Fenced PostgreSQL publication | `go test ./internal/db/gorm/... -run 'TestUCI(Publish|Lease|Replay|DeleteAll)'` | Incomplete upload does not switch current; old epoch is rejected; lost-ACK replay is idempotent; failed scan is not delete-all. |
| Go AST, structured/text extraction, FTS, and graph | `go test ./internal/... -run 'TestUCI(Go|StructuredText|ViewPinned|Graph)'` | Go facts plus Markdown headings/links, JSON/YAML keys/local references, SQL DDL text facts without execution, and OpenAPI paths/operations/local references without external fetches cite the same View. Malformed syntax is fresh and partial. |
| Native MCP isolation and installation harness | `go test ./cmd/engram/... -run 'TestUCI(Install|StandardClient|ClientContext|TwoClient)'` | Slice 4's Windows-safe disposable installation and standard-MCP-client harness uses the built daemon/server and standard clients. Per-client defaults stay isolated through connect/select/reconnect and existing tool names route through typed context. |
| Authorization, exposure, and completion | `go test ./tests/uci/acceptance -run '^TestUCIAuthorizationMatrix$'` | Every authorized search, graph result, and versioned read records one idempotent opaque exposure after context authorization. Denials record nothing. Unsupported/no-callback completion remains `unknown`; a verified supported host records `partial` independently of retrieval result state and coverage. |
| Real vector | `go test ./internal/... -run 'TestUCI(Semantic|VectorProfile)'` | Provider-generated conceptual result is profile/View scoped; disabled provider returns visible lexical-only/degraded state. |
| JS / TS / TSX parser bundle | `go test ./internal/... -run 'TestUCI(TreeSitter|TypeScript|TSX)'` | Pinned bundle records its digest; JS/TS/TSX aliases/re-exports report correct coverage or explicit partial status. |
| Watcher / recovery | `go test ./internal/... -run 'TestUCI(Watcher|Recovery|Reconcile)'` | Save/delete/rename/overflow/restart/offline paths reconcile current bytes, preserve the unaffected worktree, and do not re-embed unchanged input. |

Each command pattern belongs to the named slice. A pattern becomes runnable when that slice adds its package and test. It does not prove that the package, test, or release evidence exists today. The current repository-wide commands remain `make proto`, `make build-windows`, `make engram`, and `make test`; use them only when the owning implementation/release stage authorizes them.

## Build and Transport Gate

After the relevant Slice 4 transport and Slice 6 parser changes exist:

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

Use the versioned Windows-safe disposable installation and standard-MCP-client harness that Slice 4 introduces. Do not use a direct Go service call or a module fake.

1. Install the built server, daemon, and parser worker in a disposable native target.
2. Connect client A and client B concurrently to one daemon, rooted in the adversarial primary/linked worktrees. Attach client C after both are bound.
3. Run `codebase_context` resolution (or ordinary CWD resolution), index/reconcile, exact/FTS/semantic search, `codebase_graph`, and `codebase_read` for each client.
4. Confirm that every result identifies a consistent Source/Checkout/View, and that A/B never observe the other dirty state. Confirm that each authorized search, graph result, and versioned read returns one opaque exposure ref.
5. Run a verified supported-host `partial` callback case and an unsupported/no-callback case. The callback binds `partial` completion to its exposure ref; the other case remains `unknown`. Neither completion state changes retrieval result state or coverage, and neither case exposes source content through the record.
6. Repeat denied, revoked, mismatched, ambiguous, private-checkout, stale-view, pagination, and graph-hop cases. A refusal must disclose no outside body, identifier, count, relationship, local Windows path, or exposure reference.

Expected installed-path evidence is a redacted receipt containing built artifact hashes, fixture Source/Checkout/View IDs and manifests, two-client/third-client results, authorization-negative outcomes, opaque exposure refs with explicit completion state, and the exact command/harness version. This is required for SC-06; unit tests do not substitute for it.

## Semantic, Watcher, and Recovery Gate

1. Run the pre-registered 12-task corpus with the fixed provider/model/profile/budget. The conceptual no-keyword task must show a provider-generated vector result; lexical fallback is recorded separately and cannot count as semantic success.
2. Change only worktree A, then save, delete, rename, switch branch, induce watcher overflow, restart daemon/server, and simulate offline/reconnect. Keep B untouched.
3. Confirm a new coherent A View, unchanged B View, visible `stale`/`partial`/`offline`/`unsupported` or vector-degraded state where applicable, no old embedding under A’s new file version, and no duplicate mutation after lost ACK replay.
4. Collect at least 100 healthy batches under the accepted profile for the saved-update p95 target. Record cold/warm state, machine/DB sizes, provider status, sample count, p95, max, and failures; degraded responses are excluded from healthy latency success.

## Release Evidence Gate

A UCI-1 claim requires one exact-head evidence package, not a collection of unrelated green unit logs:

- real PostgreSQL registry/publication/lease/replay evidence;
- parser bundle, supported-language coverage, and structured/text minimum coverage evidence;
- installed Windows two-client proof from the Slice 4 harness;
- actual semantic provider result and separate degraded-mode evidence;
- authorization-negative matrix;
- authorized search/graph/read exposure evidence, including idempotent replay, no-record denial, a verified supported-host `partial` completion callback, and an explicit `unknown` no-callback case;
- all assigned UCI-1 acceptance scenarios and the 12-task baseline/value outcome;
- restart/recovery and retention/rollback evidence;
- exact candidate build/regression checks and the repository’s mandatory exact-head SonarQube Quality Gate `OK` before tag/publication;
- known limitations, visible coverage states, and explicit confirmation that UCI-2/3 were not claimed.

If any release-facing byte changes after this evidence is collected, regenerate the affected evidence against the exact final candidate.