# UCI-1 Phase 0 Research

**Scope**: Resolve the implementation-facing questions for UCI-1A+B only. The accepted specification and adopted supporting contracts remain authoritative. This file records current source evidence and decisions; it does not claim that any UCI behavior is implemented.

## R-01 — Migration Baseline and Numbering

**Decision**: Treat `170_task_memory_context_reference_receipts` as the observed current migration high-water mark. UCI starts with additive migration `171_uci_context_registry`, followed by `172_uci_index_projection`, unless an approved intervening migration exists when implementation begins. No UCI plan may reserve or alter migrations 168–170.

**Rationale**: `internal/db/gorm/migrations.go` currently registers 168, 169, and 170, with 170 last in that contiguous current range. The adopted migration contract explicitly forbids planning from presumed 170–172 numbers. UCI needs an expand-first registry before `ci_*` projection tables exist.

**Alternatives considered**:

- Reuse migration 170 or assume 170–172 are free: rejected because the current branch already contains Memory R1 migration 170.
- Add UCI columns into the old `code_chunks` schema first: rejected because raw `project_id` cannot express Source/Checkout/View authority.

**Implementation boundary**: the migration task re-reads the registry immediately before editing and shifts only the new IDs if trunk legitimately advanced; it does not renumber an applied migration.

## R-02 — Existing Code Index Is Migration Input, Not UCI Authority

**Decision**: Replace the UCI query/publication path that relies on legacy `code_chunks` / raw project strings. Keep old rows only as explicit `legacy_unscoped` compatibility/rollback data until the UCI-3 retirement gate.

**Rationale**: `internal/db/gorm/code_chunk_store.go` defines `code_chunks` around `(project_id, file_path, byte_start, content_sha256)` and documents that same-project worktrees share an index. `internal/grpcserver/code_index.go` marks/sweeps by `project_id` and `index_session_id`. `internal/codeindex/walker.go` does a permissive `filepath.WalkDir`; `chunker.go` emits line blocks rather than parser facts. These are precisely the mechanisms that cannot distinguish two dirty worktrees with identical relative locations.

**Alternatives considered**:

- Add a branch or worktree suffix to `project_id`: rejected because a mutable label/path is not Checkout authority and does not produce immutable Views.
- Reuse old embeddings by path/content alone: rejected unless exact bytes, preprocessing, dimension, model/profile, protection domain, and authorized source are proven equal.

## R-03 — Daemon/Client Context Ownership

**Decision**: Context is bound per MCP transport client/session: `client binding -> local checkout evidence -> server-authorized Source/Checkout -> selected View`. The daemon may discover local evidence from that session’s `cwd`; the server, not the client, authorizes the canonical ContextRef. A connection, selection, or reconnect of client B never changes client A.

**Rationale**: Current daemon calls receive `muxcore.ProjectContext` (`ID`, `Cwd`), and `internal/handlers/engramcore/slugcache.go` already recognizes that a project ID can be shared across different CWDs. However, `internal/handlers/codeintel/module.go` keys mutable index state by `p.ID`, while `internal/mcp/tools_code_intel.go` accepts a caller `project` string. That split is unsafe for UCI. The adopted identity contract requires a client/session binding and explicit `CONTEXT_REQUIRED` for ambiguous no-CWD selection.

**Alternatives considered**:

- One daemon-global “current checkout”: rejected because the third-client scenario would mutate another client’s context.
- Treat `ProjectContext.ID`, branch, path, remote, or V3 anchor as Checkout authority: rejected because all are selectors/evidence with different lifetimes.

## R-04 — PostgreSQL and SQLite Have Different Roles

**Decision**: PostgreSQL 17 plus pgvector owns all canonical UCI registry and rebuildable code projections. A fresh daemon-local SQLite registry owns only approved root evidence, local instance/worktree fingerprints, persistent dirty-set, watcher configuration, and recovery checkpoint state.

**Rationale**: The existing project uses PostgreSQL as the authoritative store and already depends on `pgvector-go`. `modernc.org/sqlite` currently supports the Loom tenant’s local `tasks.db`; that store must not be repurposed as shared UCI query/graph state. The adopted storage contract requires server-side `ci_*` tables, atomic publish, and an intentionally non-authoritative local registry.

**Alternatives considered**:

- Local SQLite as the primary source/graph/vector database: rejected because it bypasses server authorization, durable shared publication, and source/view history.
- New graph service, Qdrant, or broker: rejected by the adopted storage contract and constitution’s single modular-monolith constraint.

## R-05 — Parsing, structured/text scope, and licensing boundary

**Decision**: Use the Go standard library for the first Go tracer. For JavaScript, TypeScript, and TSX, ship a bounded local parser worker built with the official `github.com/tree-sitter/go-tree-sitter` binding and pinned `tree-sitter-javascript` / `tree-sitter-typescript` grammars. Slice 3 also owns the UCI-1 structured/text minimum: Markdown headings/links, JSON/YAML keys and local references, SQL DDL parsed as text only, and OpenAPI paths/operations/local references without external-ref fetches. The worker receives bounded source bytes plus a profile and emits facts/diagnostics only; it has no server/admin key, no repository plugin loading, no package installation, no source execution, and no network API.

**Rationale**: The adopted retrieval contract requires Go AST first, Tree-sitter for JS/TS/TSX, and build-verified parser bundles. It also defines the structured/text facts that UCI-1 must extract. The official Go binding documents CGO use and explicit object closure; the core binding and the JavaScript/TypeScript grammar repositories publish MIT licenses. This meets the clean-room and bundle-pinning boundary while avoiding an external parser service. SQL text extraction and local OpenAPI resolution preserve the same boundary because neither executes a query nor fetches an external document.

**Alternatives considered**:

- Parse JS/TS/TSX through `npm install`, TypeScript compiler startup, project scripts, or a remote parser service: rejected because it executes or trusts project-controlled behavior and adds uncontrolled availability/authority.
- Execute SQL or fetch an OpenAPI external reference as part of extraction: rejected because the input is untrusted source material, not a request to access another system or document.
- Treat broad document enrichment, richer navigation, or Code UI as the structured/text minimum: rejected because those belong to UCI-2.
- Claim a loaded grammar equals supported language behavior: rejected because grammar presence does not validate resolver coverage, build targets, or versioned bundle provenance.

**External evidence (accessed 2026-09-05)**:

- Official Go binding documentation: <https://github.com/tree-sitter/go-tree-sitter/blob/master/README.md> (queried through Context7; confirms separate grammar modules, CGO integration, and explicit `Close` lifecycle).
- Core license: <https://raw.githubusercontent.com/tree-sitter/tree-sitter/master/LICENSE> — MIT.
- Go binding license: <https://raw.githubusercontent.com/tree-sitter/go-tree-sitter/master/LICENSE> — MIT.
- JavaScript grammar license: <https://raw.githubusercontent.com/tree-sitter/tree-sitter-javascript/master/LICENSE> — MIT.
- TypeScript grammar license: <https://raw.githubusercontent.com/tree-sitter/tree-sitter-typescript/master/LICENSE> — MIT.

**Implementation boundary**: Pin actual module/grammar revisions and preserve license/SBOM evidence with the parser bundle. Build and run Windows amd64, Linux amd64, and supported macOS checks before claiming support. Gate the structured/text adapters in the Slice 3 extraction test; they do not need the semantic provider, parser bundle, or installed-client prerequisites of later slices.

## R-06 — Native Windows Real-Worktree Test Strategy

**Decision**: Slice 0 creates a real Git repository and a linked worktree under `t.TempDir()` using argument-vector Git invocation, commits a shared base, and then writes divergent saved dirty bodies/callees. It sets `ENGRAM_CODE_INTEL_ENABLED=true`, drives the existing `codebase_index`, `codebase_status`, and `codebase_search` seams, and waits for bounded terminal `idle` status before asserting the two-worktree collision. The first RED has no parser bundle, real semantic provider, or installed-client prerequisite. Slice 4 later adds the Windows-safe disposable installation and standard-MCP-client harness. Slice 8 consumes that versioned harness for installed acceptance.

**Rationale**: `internal/projectidentity/resolver_integration_test.go` already creates a primary repo and `git worktree add` with `os/exec` argument vectors. Its topology matrix demonstrates a reusable native Git fixture pattern. The current code-intelligence path returns from `codebase_index` asynchronously and exposes `codebase_status` for bounded completion. The acceptance contract explicitly says package tests and direct service calls do not replace the installed Windows two-client proof.

**Alternatives considered**:

- Mock a worktree or copy a directory: rejected because it cannot test a linked worktree’s `.git` file/common-dir/private-git-dir semantics.
- Use shell-concatenated Git commands: rejected because paths with spaces/Unicode and untrusted Git metadata require argument-safe plumbing.
- Let disabled tools, a missing tool, or an unfinished index make the RED pass: rejected because that does not prove the raw-project isolation defect.

**First proof shape**: The RED test calls actual existing MCP tool names through the daemon/server seam. It indexes A and B serially so the current `already_running` guard cannot be mistaken for isolation. It uses a common legacy selector on purpose so the old code proves its collision. The later installed test is a distinct release gate that consumes the Slice 4 harness.

## R-07 — Transport and Protobuf Ownership

**Decision**: Extend the existing `proto/engram/v1/engram.proto` `EngramService` with additive, scoped private UCI messages/RPCs (Bind/BeginIndex/StageIndex/FinalizeIndex/QueryCode/ExploreCode or equivalent). Preserve all current field numbers and never add a second protobuf service or MCP HTTP server.

**Rationale**: Existing `CallToolRequest` owns fields 1–6; existing code-index messages use their current fields and raw `project_id`. The approved API contract assigns UCI transport to the existing private service, requires the first upload frame to bind build/source/checkout/epoch, and requires explicit final manifest completion separate from EOF.

**Alternatives considered**:

- Reinterpret existing fields as UCI context: rejected because old clients/servers must retain explicit compatibility behavior.
- New UCI gRPC or HTTP server: rejected because it duplicates auth/identity/receipt paths and violates the no-new-service constraint.

## R-08 — Real Semantic Retrieval

**Decision**: Reuse `internal/embedding` only as a provider/settings/vault-aware transport. UCI stores results under a versioned embedding profile, performs exact vector distance over the authorized current membership as the correctness baseline, and fuses profile-compatible vector and FTS candidates with RRF. Provider/model/preprocessing/dimension changes create a new profile; a failed/unavailable provider produces a visible lexical-only/degraded result.

**Rationale**: The current embedding client already reaches a LiteLLM-compatible endpoint and pins schema-bound dimensions, but `internal/retrieval/code_hybrid.go` is raw-project keyed and intentionally suppresses leg errors. That behavior is insufficient for FR-07 and view-pinned access. The adopted retrieval contract requires a provider-generated vector for semantic success and forbids global top-N then post-filtering invisible rows.

**Alternatives considered**:

- Count a fake embedder, empty query vector, or lexical answer as semantic acceptance: rejected by FR-07 and the acceptance contract.
- Reuse an embedding merely because dimension matches: rejected because semantic space, preprocessing, profile, source, and protection domain remain part of identity.

## R-09 — Watcher, Recovery, and Publication

**Decision**: Implement a dedicated UCI watcher/reconciler. Native fsnotify events are hints; the daemon persists dirty paths, local monotonically increasing sequence, and `rescan_required` in its local registry. The server persists jobs and fences one checkout/incarnation publisher with a monotonic epoch. Only a complete explicit finalization can replace the current View.

**Rationale**: `internal/watcher/watcher.go` only detects deletion of one target and has no file-manifest, worktree, local registry, publication, or recovery semantics. The existing code-index stream sweeps at EOF by project string, which cannot provide a fenced immutable View. The adopted storage contract specifies safe read/hash, staging, transactional finalization, lost-ACK replay, overflow/offline handling, and delete-all discrimination.

**Alternatives considered**:

- Assume no fsnotify event means current: rejected because overflow, lost events, network/WSL boundaries, and restarts are expected.
- Let stream EOF or a failed scan publish an empty result: rejected because only a complete scoped scan may publish delete-all.

## R-10 — Scope Boundary and Deferred Work

**Decision**: UCI-1 ships the adopted Go/JS/TS/TSX scope plus Markdown headings/links, JSON/YAML keys/local references, SQL DDL text extraction without execution, and OpenAPI paths/operations/local references without external fetches. It also ships the native two-client proof, real semantic path, and recovery. UCI-2 retains the Code UI and broader navigation/language work. UCI-3 retains broad product-domain address migration and legacy index retirement.

**Rationale**: The accepted spec makes UCI-1A+B one release and makes Code UI separately accepted UCI-2 surface work. The plan must shape UCI-1 so future Code UI can consume Source/Checkout/View selectors but must not design or implement that surface.

**Alternatives considered**:

- Build the Code UI as acceptance visualization: rejected by Constitution Principle XII and the feature boundary.
- Require UCI-2/3 completion before UCI-1: rejected because it prevents the first useful two-worktree vertical from shipping.

## Resolved Research Ledger

| Question | Resolution | Planning artifact |
|---|---|---|
| Current migration end | 170 is current; UCI begins after it. | `plan.md` migration/cutover section |
| Code-index entry points | Current daemon adapter, gRPC client/server, MCP handlers, legacy walker/store/retrieval are all identified as migration seams. | `plan.md` source map |
| Context ownership | Per transport client/session binding; server authorizes canonical context. | `data-model.md` ownership and invariants |
| PostgreSQL / SQLite roles | PostgreSQL authority; SQLite local operational journal only. | `data-model.md` authority boundary |
| Parser dependency / structured-text scope | Standard Go AST; pinned MIT Tree-sitter binding and JS/TS grammars; Slice 3 owns the defined Markdown/JSON/YAML/SQL/OpenAPI minimum with SQL text-only and no external OpenAPI fetch. | `plan.md`, `quickstart.md`, and this record |
| Slice-0 activation and completion | The RED enables the current capability, proves `codebase_index`/`codebase_search` dispatch, waits for existing `codebase_status` completion, and then asserts isolation. | `plan.md` and `quickstart.md` |
| Windows worktree and installed proof | Slice 0 uses a real linked Git worktree. Slice 4 owns the Windows-safe disposable installation and standard-MCP-client harness. Slice 8 consumes that harness for the installed two-client proof. | `plan.md` and `quickstart.md` |
| Protobuf ownership | Additive messages/RPCs within the existing `EngramService`; no tag reuse or second service. | `plan.md` execution slice 4 |
| Real vector proof | Provider-generated vector, scoped exact PostgreSQL baseline, profile health surfaced. | `quickstart.md` |
| Watcher/recovery | Dedicated watcher, persisted dirty state/jobs, fenced atomic publication. | `data-model.md` transitions |

There are no unresolved planning questions in UCI-1A+B.