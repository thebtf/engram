# Implementation Plan: Unified Code Intelligence (UCI-1A+B)

**Branch**: `uci/unified-code-intelligence-r1` | **Date**: 2026-09-05 | **Spec**: [spec.md](spec.md)
**Approved specification commit**: `c3d41d2575847d8e4e4313cc0a55acfbb1419327` | **Constitution**: 1.1.0
**Input**: The accepted UCI feature specification and its adopted supporting contracts under this directory.

## Summary

Deliver UCI-1A and UCI-1B only as one installed native capability: one daemon serves two real saved Git worktrees concurrently, each MCP client remains bound to its own server-authorized `Source` / `Checkout` / immutable `View`, and exact, FTS, semantic-vector, and bounded graph answers all cite that same view.

The implementation replaces the current project-string code-index path rather than extending it. PostgreSQL is the authority for context and rebuildable `ci_*` projections; the daemon's local SQLite state is only a workstation registry, dirty-set, watcher, and recovery journal. The first executable proof is deliberately an adversarial two-real-worktree RED test against the existing public code-intelligence tool seam. UCI-2 and UCI-3, including the Code UI, remain deferred.

## Planning Calibration

**Rung**: D2 — this artifact is the UCI-1A+B subsystem plan, not the wider code-intelligence program. A wrong identity, publication, or migration design would affect durable data and concurrent clients across multiple implementation sessions; UCI-2/3 are constraints on this design, not scope to implement now.

**Inline challenge — GO**: the selected design rejects the three scope-expanding failure modes: continuing to use `project_id` as code authority, creating a second graph/vector service, and treating a future UI as part of core UCI. Its residual risks are enumerated below for implementation ownership. This is a planning conclusion based on the approved review-spec gate, current source, and adopted contracts; it is not implementation or release acceptance.

## Technical Context

**Language/Version**: Go 1.26.6. JavaScript hooks and the TypeScript/OpenClaw consumer are compatibility boundaries. UCI-1 parses Go, JavaScript, TypeScript, and TSX. Its structured/text minimum is Markdown headings/links, JSON/YAML keys and local references, SQL DDL as text only, and OpenAPI paths, operations, and local references without fetching external refs.

**Primary Dependencies**: Existing GORM, pgx, `pgvector-go`, `fsnotify`, protobuf/gRPC, and the existing `internal/embedding` provider client. Go extraction uses the standard-library AST. JS/TS/TSX use the official `github.com/tree-sitter/go-tree-sitter` binding and pinned JavaScript/TypeScript grammars in a bundled local parser worker; their current upstream licenses are MIT. Parser/runtime/grammar/toolchain revisions become one `parser_bundle_digest`; no runtime `latest`, repository plugin, package install, source-script execution, Python daemon, graph server, Qdrant, or new network service is allowed.

**Storage**: PostgreSQL 17 plus pgvector is the authoritative state and projection store. New `ci_*` tables hold code-derived facts. A daemon-local SQLite registry persists only approved roots, local checkout evidence, dirty paths, watcher configuration, and resume checkpoints; it is never a query, graph, or authorization authority.

**Testing**: Slice 0 uses focused Go RED/GREEN tests, real PostgreSQL integration/fault tests, and native Windows Git-worktree tests. Slice 4 adds a Windows-safe disposable installation and standard-MCP-client harness. Final acceptance runs that harness with two concurrent standard MCP clients, the built daemon/server/parser worker, and the existing repository release gates. The current `projectidentity` integration fixture supplies the argument-safe real-`git worktree add` pattern.

**Target Platform**: Native Windows amd64 is mandatory for UCI-1 installation proof. Parser bundle build verification also covers Linux amd64 and supported macOS targets. Paths with spaces, Cyrillic text, long paths, case-sensitive Windows directories, CRLF/LF, linked-worktree `.git` files, detached HEAD, and unborn HEAD are supported fixture cases.

**Project Type**: Existing Go modular monolith: stdio daemon (`cmd/engram`), gRPC/HTTP server (`cmd/engram-server`), MCP tools, PostgreSQL stores, and plugin clients. There is no new deployable service.

**Performance Goals**: On the documented healthy first-release profile, saved structural/FTS updates have p95 <= 2 s over at least 100 batches; embedding readiness p95 <= 10 s when the provider is healthy; warm non-provider search p95 <= 800 ms; bounded search with query embedding p95 <= 2.5 s; and small explain/neighbors/impact p95 <= 1 s at the adopted graph budgets. A degraded result cannot satisfy a healthy-path target.

**Constraints**: Resolve and authorize Source/Checkout/View before candidate selection; publish views atomically under a fenced lease; never mix view generations; preserve project/Space semantics; expose `stale`, `partial`, `offline`, `unsupported`, and vector degradation honestly; preserve existing authorization; keep credentials and excluded source bodies out of artifacts, embeddings, logs, and responses; and do not turn a path, branch, remote, label, or legacy key into authority.

**Scale/Scope**: First-release acceptance profile: approximately 10k text files / 1M LOC, five active worktrees plus inactive registrations, and <=20 changed files totaling <=1 MiB per warm update. UCI-1 covers Go, JS, TS, TSX, Markdown headings/links, JSON/YAML keys and local references, SQL DDL as text only, and OpenAPI paths, operations, and local references without external-ref fetches.

## Constitution Check

### Pre-Design Gate — PASS

| Gate | Result | Evidence and required design posture |
|---|---|---|
| Typed Source / Checkout / View authority | PASS | Every request resolves a server-authorized typed context before candidate selection, read, graph traversal, update, or continuation. Paths and legacy values are evidence only. |
| Project / Space compatibility | PASS | `Space` remains product grouping; typed aliases and a compatibility bridge preserve legacy readers without selecting a checkout. |
| Separate code graph | PASS | Syntax and code relations live in `ci_*`; existing memory/knowledge graph tables remain separate. |
| PostgreSQL projection authority | PASS | PostgreSQL owns views, memberships, artifacts, edges, embeddings, jobs, and lease epochs. SQLite holds only local operational state. |
| Durable async state | PASS | Every index/watch/enrichment workflow has a persisted job, idempotency key, retry state, terminal error, and lease/epoch fence. |
| Versioned identity migration | PASS | Current migration high-water mark is `170_task_memory_context_reference_receipts`; UCI begins with additive migrations after 170 and preserves `legacy_unscoped` evidence. |
| Future Code UI dependency | PASS | Code UI is UCI-2 surface work requiring a separate accepted Spec Kit feature; UCI-1 plans no UI. |
| Release and evidence gates | PASS | Built native server/daemon/parser worker, two simultaneous standard MCP clients, real PostgreSQL, real semantic provider, authorization-negative matrix, product-task baseline, exact-head regression/SonarQube, and rollback evidence are required before a UCI-1 release claim. |

### Post-Design Gate — PASS

`research.md`, `data-model.md`, and `quickstart.md` preserve the pre-design gate: the execution order resolves authority before index data, fences publication before graph/search, profiles vectors, makes async recovery durable, keeps legacy data readable but non-authoritative for UCI, and leaves UCI-2/3 outside the implementation boundary.

## Architecture Decision — UCI-1 Core Shape

| Alternative | Decision | Reason |
|---|---|---|
| Extend `code_chunks` and the raw `project_id` pipeline | Rejected | Current keys deliberately merge worktrees sharing a project string; the server’s EOF sweep can delete by that same scope, and no immutable View binds search and graph. |
| Give each daemon/worktree an independent local graph/vector database | Rejected | It creates a second authority, prevents server-enforced access control and durable cross-client publication, and makes recovery/rollback opaque. |
| Server-authorized Source/Checkout/View registry; PostgreSQL `ci_*` projection; daemon-local SQLite operational state; existing daemon/server transport | Selected | It matches the constitution and adopted contracts, isolates dirty worktrees, supports atomic publish and durable recovery, and does not add a network service. |

**ADR-UCI-1 (recorded here, not as a duplicate contract)**: retain the existing single `EngramService` and codebase tool family, but replace their UCI data path with a typed, View-pinned application workflow. Existing protobuf field numbers and the existing code-index protocol remain readable only for explicit legacy compatibility; new private RPCs are additive and fenced. `internal/handlers/codeintel` remains the daemon adapter, not an authority or global `projectID -> cwd` context cache.

## Phase 0 Research Result

All Phase 0 decisions and their source evidence are consolidated in [research.md](research.md). In particular, it resolves migration numbering, current UCI seams, daemon/client context ownership, PostgreSQL/SQLite roles, parser licensing/build boundary, native Windows worktree testing, protobuf ownership, real-vector requirements, and watcher/recovery posture.

## Phase 1 Contract Boundary

No new contract is created in this plan pass. The feature already adopts its non-duplicative interface and acceptance authorities:

- `contracts/query-response.schema.json` and `contracts/examples.json` define public response shape.
- `supporting-contracts/identity-and-worktrees.md`, `storage-and-indexing.md`, `retrieval-and-graph.md`, `api-contracts.md`, and `migration.md` define technical behavior.
- `acceptance/uci-acceptance.md` and `acceptance/scenarios.json` define acceptance behavior.

[data-model.md](data-model.md) turns those adopted rules into a planner-facing entity, key, state, ownership, and rollback map; it does not supersede them.

## First RED/GREEN Proof

**Test seam**: Add one focused integration test next to the existing daemon code-intelligence adapter (`internal/handlers/codeintel`). Exercise the existing public MCP tool names, `codebase_index`, `codebase_status`, and `codebase_search`, through the actual daemon dispatch/gRPC/server path. Set `ENGRAM_CODE_INTEL_ENABLED=true` for the disposable daemon and server fixture. Do not begin by naming a speculative UCI Go API.

**Slice-0 prerequisites and fixture**:

1. Start the disposable daemon/server fixture with `ENGRAM_CODE_INTEL_ENABLED=true` and the existing code-chunk store wired. Confirm that the current code-intelligence tools can dispatch before evaluating UCI behavior.
2. Create a temporary Git repository and a real linked worktree through argument-vector Git calls, following `internal/projectidentity/resolver_integration_test.go` rather than parsing shell strings.
3. Commit one shared baseline, then give the primary and linked worktree the same relative file path, symbol name, branch label/HEAD, and different **saved dirty** body and callee. Bind two simultaneous daemon client contexts with the same legacy project selector and different `cwd` values.
4. Index A and then B through the existing `codebase_index` path. For each dispatch, assert the normal asynchronous `started` response, poll the existing `codebase_status` to terminal `idle` without an error within the fixture deadline, and then invoke `codebase_search` through its existing server path. Serial indexing keeps the current per-project `already_running` guard from becoming the asserted defect.
5. Only after activation, dispatch, and bounded completion pass, assert that the two contexts cannot retain distinct saved bodies, callees, Source/Checkout/View information, or defaults. Add a third client and assert that the intended UCI behavior would leave the first two defaults unchanged.

The existing implementation is expected to fail the isolation assertion: `internal/handlers/codeintel/module.go` keys mutable state by `p.ID`; `internal/handlers/engramcore/code_index_client.go`, `internal/grpcserver/code_index.go`, `internal/mcp/tools_code_intel.go`, and `internal/db/gorm/code_chunk_store.go` use a raw project string; and the current `code_chunks` unique key intentionally merges same-project worktrees. The RED must therefore prove a production-seam isolation defect, not a disabled flag, missing tool, or incomplete index.

**GREEN condition**: After the minimal registry/mapping and UCI query path land, the same test first passes its Slice-0 dispatch/completion checks, then returns two noninterchangeable ContextRefs/Views, has no cross-tree rows or edges, and leaves A/B defaults unchanged after client C connects. Slice 8 consumes the Slice 4 installed-native harness for the release counterpart; it does not substitute for this focused proof.

## Ordered Execution Slices

These are plan checkpoints, not a generated task file. UCI-1A+B is one first releasable unit; no checkpoint below makes an individual release claim.

| Order | Type | Slice and boundaries | Blocking edge | Focused acceptance checkpoint |
|---|---|---|---|---|
| 0 | Test | Set `ENGRAM_CODE_INTEL_ENABLED=true` in the disposable fixture. Drive existing `codebase_index`, `codebase_status`, and `codebase_search` through bounded successful legacy completion before detecting the shared raw-project collision. | None | Activation, tool dispatch, and bounded completion pass; RED fails for two-worktree isolation rather than a disabled/missing/incomplete index. |
| 1 | Code + Test | Add the minimal typed registry and compatibility mapping: Space/Source/Checkout/incarnation/View, client-scoped binding, and fail-closed context resolution. Begin additive migration `171_uci_context_registry` after migration 170. | Slice 0 establishes the observable defect. | Two worktrees resolve separately; legacy-only ambiguity returns `CONTEXT_REQUIRED` without disclosure. |
| 2 | Code + Test | Add `ci_*` projection storage and fenced publication via additive migration `172_uci_index_projection`: staging, manifest finalization, generation intervals, jobs, and lease epoch. | Slice 1 supplies authorized binding. | Partial upload, lost ACK replay, stale lease, and delete-all-vs-failed-scan scenarios leave current views coherent. |
| 3 | Code + Test | Replace line-block UCI processing with Go AST extraction, shared artifact facts, FTS, and bounded graph resolution over the same published View. This slice also ships the adopted structured/text adapters: Markdown headings/links, JSON/YAML keys and local references, SQL DDL as text only, and OpenAPI paths/operations/local references without external-ref fetches. Migrate every UCI caller from legacy `code_chunks` semantics; retain old rows only as explicit `legacy_unscoped` compatibility/rollback data. | Slice 2 supplies atomic View membership. | Exact/FTS/explain/impact results cite one View. Supported structured/text facts are View-pinned and report coverage. SQL never executes, and OpenAPI never fetches external refs. Malformed Go is current-but-partial, never stale-as-current. |
| 4 | Code + Test | Cut native MCP context isolation through the existing daemon, `EngramService`, gRPC server, and MCP tool surfaces. Introduce the Windows-safe disposable installation and standard-MCP-client harness. Preserve tool names; add `codebase_context`, `codebase_graph`, and `codebase_read` only as adopted APIs. Append protobuf fields/RPCs; do not reuse existing tags or add a second service. | Slices 1–3 establish typed core state. | The harness installs the built daemon/server in a disposable native target and drives standard MCP clients. Two concurrent sessions and a third connect/reconnect without context mutation or arbitrary root reads. |
| 5 | Code + Test | Connect the existing provider/settings/vault-backed embedding client to profile-scoped `ci_embeddings`; use provider-generated query vectors, scoped exact PostgreSQL distance as correctness baseline, and RRF with FTS. | Slice 2 storage and Slice 3 chunks. | Conceptual no-keyword query produces a real semantic hit; provider absence is explicit degraded/lexical-only and cannot pass FR-07. |
| 6 | Code + Test | Ship the bounded parser worker and JS/TS/TSX grammars under pinned MIT-reviewed dependency revisions; add language/profile coverage and alias/re-export behavior. | Slice 3 shared artifact protocol and Slice 4 transport. | Parser bundle verifies target builds; supported JS/TS/TSX facts are View-pinned and unsupported syntax reports partial/unsupported. |
| 7 | Code + Test | Add checkout watcher/reconcile/recovery with native fsnotify signals, local SQLite dirty-set/checkpoint, full-safe rescan, durable jobs, and server lease fencing. Do not repurpose the current deletion-only watcher as the index watcher. | Slices 1–3 identity/publication. | Save/delete/rename/branch/restart/overflow/offline scenarios publish a new A view only, keep B unchanged, and never turn failed scan into empty corpus. |
| 8 | Review + Acceptance | Build and exercise UCI-1A+B through the versioned Slice 4 Windows-safe disposable installation and standard-MCP-client harness: server/daemon/parser worker, two standard MCP clients, real PostgreSQL, real provider, authorization-negative matrix, 12-task baseline, SLO samples, exact-head regression/SonarQube, and rollback receipt. | Slices 0–7 and the completed Slice 4 harness. | All UCI-1 assigned scenarios, SC-01 through SC-08, are evidenced through the installed path without claiming UCI-2/3. |

## Migration and Cutover Plan

1. **Expand** — Verify the current migration registry at implementation start. This plan binds the observed high-water mark to migration 170; reserve `171_uci_context_registry` and `172_uci_index_projection` only if no intervening approved migration is present. Both are additive, idempotent forward migrations.
2. **Map, do not guess** — Create Space/source aliases and source bindings only from authorized V3/registry evidence. Preserve legacy canonical project keys, project records, scope, owner, provenance, and data. Ambiguous mappings stay ambiguous and block mutation.
3. **Rebuild derived code truth** — Existing `code_chunks`/embeddings remain `legacy_unscoped`; do not backfill a checkout, commit, View, or provenance guessed from paths, labels, or hashes. Build UCI views from allowed saved source bytes.
4. **Cut callers together** — Move daemon `codebase_index/status/search` and new read/graph/context calls to the typed workflow in one controlled wave. A legacy project selector can enter only the compatibility resolver; it cannot select a dirty checkout, global latest view, or neighboring tree.
5. **Observe before contraction** — Keep legacy code-index data only for explicitly labeled legacy reads/rollback while installed clients and authorization matrices are observed. UCI-3, not UCI-1, owns broader domain-address migration and legacy code-index retirement.
6. **Rollback** — Before cutover, disable the new UCI route and leave `ci_*` inert; product-domain data and legacy indexes remain untouched. After expansion but before contraction, route back through the prior compatibility path while preserving new registry/projection state. After a future derived-data contraction, rebuild from authorized source rather than fabricating historical Views. No rollback changes crypto-bound identity data or returns secrets to local components.

## Single Release Map

| Release | Closes | Included implementation checkpoints | Required release evidence | Rollback boundary |
|---|---|---|---|---|
| UCI-1 (UCI-1A+B together) | Agents could not safely search/trace exact saved code in two concurrent worktrees; they can now use one installed daemon without cross-worktree context leakage. | 0–8 above | Real PostgreSQL, the versioned Slice 4 Windows-safe disposable installation and standard-MCP-client harness, built native server/daemon/parser worker, two-client installed Windows proof, structured/text coverage, real semantic provider, authorization-negative and recovery evidence, 12-task measurement, exact-head regression/SonarQube. | New route can be disabled before contraction; `ci_*` is rebuildable and legacy data stays explicitly legacy until the UCI-3 retirement gate. |

## Project Structure and Current Seams

### Documentation (this feature)

```text
specs/010-unified-code-intelligence/
├── spec.md
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/                         # adopted response schema/examples
├── supporting-contracts/              # adopted technical authorities
└── acceptance/                        # adopted scenario/acceptance authorities
```

No task artifact is created by this planning pass.

### Source Code (current seams and planned ownership)

```text
cmd/engram/                            # daemon wiring, client-session transport, and Slice 4 Windows-safe disposable installation/standard-MCP-client harness
cmd/engram-server/                     # existing server process
internal/handlers/codeintel/           # current daemon adapter and Slice-0 existing-seam RED fixture; migrate to typed UCI adapter
internal/handlers/engramcore/          # daemon-to-server gRPC client; add scoped private calls
internal/mcp/                           # server MCP schemas and handlers; remove raw project authority from UCI path
internal/grpcserver/                    # existing EngramService implementation; add private UCI RPCs
internal/db/gorm/                       # migration registry, PostgreSQL models/stores, current legacy code_chunks
internal/codeindex/                     # legacy WalkDir/line-block input; replace for UCI path after caller cutover
internal/retrieval/                     # current raw-project hybrid retrieval; replace with View-scoped query service
internal/embedding/                     # existing provider/settings/vault-aware embedding transport
internal/projectidentity/               # V3 resolution and real-Git fixture conventions used as compatibility input
internal/watcher/                       # deletion-only fsnotify helper; not the UCI index watcher
proto/engram/v1/engram.proto            # single private transport service; additive UCI messages only
plugin/engram/hooks/                    # existing host integration boundary; no Code UI work
```

**Structure Decision**: create a focused UCI domain/application package under `internal/` for typed context, publication, extraction, query, graph, and recovery workflows; keep `internal/handlers/codeintel`, `internal/mcp`, `internal/grpcserver`, and `internal/handlers/engramcore` as adapters. Preserve the existing Go modular-monolith shape. The current `internal/codeindex`/`code_chunks` implementation is a migration input, not the UCI authority or scanner contract.

## Requirement-to-Seam Map

| Requirement | Execution slice | Current seams to change or retire from UCI path |
|---|---|---|
| FR-01, FR-02, FR-03, FR-11 | 0–1, 4 | `internal/handlers/codeintel`, `internal/handlers/engramcore`, `internal/mcp/context.go`, `internal/mcp/tools_code_intel.go`, `internal/grpcserver`, `proto/engram/v1/engram.proto`, `internal/projectidentity` |
| FR-04, FR-05, FR-06, FR-08, FR-10 | 2–4 | planned UCI domain package; `internal/db/gorm`; `internal/mcp`; `internal/grpcserver`; replace `internal/retrieval/code_hybrid.go` raw-project path |
| UCI-1 structured/text minimum | 3 | planned UCI extraction adapters; `internal/db/gorm` artifact/membership/FTS stores; View-scoped query and graph paths in the planned UCI domain package |
| FR-07 | 5 | planned UCI query/embedding jobs; `internal/embedding`; `internal/db/gorm`; pgvector/profile migrations |
| FR-09, FR-10 | 2, 7 | planned UCI local registry/watcher; `internal/watcher` only as a reference; `internal/db/gorm` durable job/publication stores |
| FR-12 and SC-01–SC-08 | 4, 8 | `cmd/engram` Windows-safe disposable installation/standard-MCP-client harness; `cmd/engram-server`, `proto/engram/v1`, UCI domain package, build/install harness, acceptance fixtures |

## Explicit Implementation Risks That Must Have Owners

| Risk | Required owner action before acceptance |
|---|---|
| Native Windows installation proof is not supplied by the current POSIX-oriented `make install` path. | Slice 4 must ship and focus-test the Windows-safe disposable installation and standard-MCP-client harness. Slice 8 must consume that same versioned harness; a unit or direct service call cannot substitute. |
| `go-tree-sitter` is CGO-backed and its grammar/bundle supply chain affects every supported target. | Pin module and grammar revisions, retain license/SBOM evidence, build/test Windows amd64/Linux amd64/supported macOS, and record the resulting bundle digest. |
| The UCI-1 structured/text minimum can be skipped while Go and Tree-sitter parser work proceeds. | Slice 3 must ship and gate Markdown headings/links, JSON/YAML keys/local references, SQL DDL as text only, and OpenAPI paths/operations/local references without external-ref fetches. Slice 8 consumes that coverage; it must not add UCI-2 enrichment or Code UI work. |
| Existing V1 index writes/sweeps by raw project string. | Migrate every UCI caller before enabling typed queries; preserve legacy rows only as labeled compatibility data and prohibit mixed-query fallthrough. |
| Old `CodeHybridSearch` silently degrades on leg failures. | Make semantic/profile health and lexical-only fallback explicit in UCI results; use a provider-generated vector for the semantic acceptance case. |
| Watchers can lose events and a path can be recreated. | Persist dirty-set/checkpoint and incarnation evidence; force safe reconcile on overflow/restart/offline and reject stale lease finalization. |
| Migration overlaps with existing V3/project data and future UCI-3. | Recheck the migration registry immediately before editing, use aliases/mapping and an isolated PostgreSQL fixture, preserve legacy data and crypto inputs, and do not perform UCI-3 contraction. |

## Deferred Boundary

UCI-2 retains broader languages/material, richer cross-source analysis, analytics, and the read-only Code UI. UCI-3 retains progressive product-domain address migration and legacy code-index retirement after migration/rollback evidence. Neither is designed, implemented, tested as a prerequisite, or claimed by this UCI-1 plan.
