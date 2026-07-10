# SECURITY-PROJECT-IDENTITY Maker Contract

Observed at: 2026-07-10T14:28:51.0969752Z

## Exact implementation boundary

- Base: `dc891b2d72b1fd63b83e4a630a249241fc389151`
- Branch: `work/prc-security-project-identity`
- Worktree: `D:/Dev/engram/.agent/worktrees/prc-security-project-identity`
- Workspace classification: `already-isolated`
- Finish target: `review-needed`
- Product writes are limited to the owner paths named in the maker brief.
- Primary/integration worktrees, role/session/oracle state, release state, and
  non-owned product paths are forbidden.

## Pre-edit scaffold classification

| Surface | Classification | Live-code evidence and decision |
| --- | --- | --- |
| `internal/proxy/identity.go:ResolveProjectSlug` | `live` | Called by `internal/handlers/engramcore/slugcache.go`, which is used by both `ProxyTools` and `ProxyHandleTool`; existing Go tests execute git, non-git, worktree, and anchor paths. Preserve the legacy selector contract while adding a separate v2 resolver. |
| Legacy `.engram-project` `{name,id}` handling | `live` | Non-git calls read/create it today. Its six-hex path-derived `id` is not a high-entropy anchor and must never be reclassified as one. Preserve it only as a legacy selector/display-name compatibility surface. |
| Versioned high-entropy non-git anchor | `must-build` | No version, cryptographic random anchor, strict validator, or explicit sharing bit exists in Go, Claude, or OpenClaw. Build an additive `.engram-project-v2.json` contract; old binaries ignore it on rollback. |
| `internal/handlers/engramcore/tools.go` request construction | `live` | Both first `tools/list` and first `tools/call` synchronously reach gRPC. Add full v2 metadata to both without depending on a lifecycle hook. |
| `proto/engram/v1/engram.proto` `Initialize*` / `CallTool*` | `live` | These are the current daemon/server wire messages. V2 metadata and canonical resolution fields are absent and therefore `must-build` additions to live messages. Existing tags remain untouched. |
| `internal/grpcserver/server.go` `Initialize` / `CallTool` | `live` | `CallTool` currently injects the raw selector directly into MCP context before any identity registration. `Initialize` ignores identity entirely. Both must synchronously call `RegisterAndResolve` before handler/data access. |
| `internal/db/gorm/project_store.go:UpsertProject` | `live` | Called by the HTTP context path. It creates a canonical row and appends an alias, but has no full-identity contradiction guard. Keep as a compatibility wrapper, not as the v2 convergence primitive. |
| `internal/db/gorm/project_store.go:ResolveProjectID` | `live` | Called by HTTP and issue paths. Its `LIMIT 1` makes duplicate aliases nondeterministic. V2 uses a new fail-closed resolver that counts all canonical candidates; the legacy wrapper remains for non-v2 callers outside this owner slice. |
| `internal/worker/handlers_context.go` async alias upsert | `live` | The handler resolves first, then launches `UpsertProject` in a goroutine after access has begun. Replace this owned path with synchronous resolve-before-access and an identity-only registration mode. |
| `plugin/engram/hooks/lib.js` identity helpers and `RunHook` | `live` | Every Claude hook constructs this context. Add a v2 generator and make `RunHook` complete identity registration before invoking a handler, so even late hooks are idempotent. |
| `plugin/openclaw-engram/src/identity.ts` | `live` | Current IDs include a repo-name prefix and agent-only fallback and therefore diverge from Go/Claude. Preserve the existing `projectId` as a compatibility selector; add the same full v2 metadata and shared vectors. |
| Proposed `project_identities_v2` persistence | `rejected/unowned` | Current schema governance is `gormigrate` in `internal/db/gorm/migrations.go`, which is outside this maker boundary. Request-time DDL or `AutoMigrate` would make readiness and rollback claims false. V2 therefore uses only the existing `projects` rows and their `git_remote`, `relative_path`, and `legacy_ids` relation. |
| Identity-only HTTP registration | `must-build` | No dedicated route exists in the allowed owner set. Add an `identity_only` request mode to the live `/api/context/inject` route and return the resolved canonical selector before retrieval/mutation. |
| `docs/arch/architecture.md` identity/data-flow description | `pre-demolition-stale` | It still describes a GET hook flow and omits current stdio-daemon/gRPC identity convergence. Use only as a documentation correction target, never as implementation evidence. |
| v5 graph/rerank/server-side HTTP MCP remnants | `pre-demolition-stale` | None is on the selected call path. No graph stage, reranker, removed `internal/search` pass, SDK extraction, or HTTP MCP transport will be restored. |
| `ENGRAM_ENFORCE_SOURCE_PROJECT` authorization checks | `live` | The flag is read in Go and defaults true. It remains an authorization/data-scope guard independent of identity convergence; a selector or resolved canonical ID never creates an authenticated principal or private-access grant. |

No selected identity scaffold is dormant behind an unset feature flag.

## Consumer map

| Consumer | Input dependency | Output dependency |
| --- | --- | --- |
| stdio daemon | `muxcore.ProjectContext` plus local git/non-git metadata | Additive protobuf `ProjectIdentityV2`; resolved canonical is server-owned |
| Claude hooks | hook `cwd` plus v2 anchor file for non-git workspaces | Synchronous identity-only HTTP response updates `context.Project` before the hook handler |
| OpenClaw | `workspaceDir` when available; agent ID remains an agent identity, not a project proof | Existing `projectId` compatibility selector plus optional v2 metadata |
| gRPC server | selector plus optional v2 message | Canonical selector or stable gRPC status details before MCP handler access |
| HTTP context API | selector plus optional v2 JSON object | Canonical selector or stable JSON error before retrieval/mutation |
| PostgreSQL | existing `projects` namespace and metadata/alias relation | Transactionally consistent identity-to-canonical mapping; no request-path DDL |

## Versioned input contract

`ProjectIdentityV2` is version `2` and has exactly one source form:

- Git: non-empty normalized `git_remote` and normalized repository-relative
  POSIX `relative_path`; `non_git_anchor` absent and `anchor_shared` absent.
- Non-git: `non_git_anchor` is exactly 32 lowercase hexadecimal characters
  generated from 16 bytes of `crypto/rand` / `crypto.randomBytes`; git fields
  are absent; `anchor_shared` has explicit presence and is `false` unless a
  human intentionally opts into sharing.

Both forms carry the existing client selector as the outer request `project`,
an optional legacy path alias, and a display name. Empty selectors, unknown
versions, mixed source forms, malformed paths/remotes, weak anchors, and absent
non-git sharing presence are invalid before database mutation.

Anchor convergence is explicit:

- identical non-git anchors with `anchor_shared=true` may converge;
- copied anchors with `anchor_shared=false` remain distinct by selector;
- an unshared binding is never silently promoted to shared;
- agent IDs are not accepted as high-entropy project anchors.

## RegisterAndResolve semantics

1. Validate the complete request before opening a write transaction.
2. Take transaction-scoped advisory locks for the normalized full identity and
   every supplied selector; do not create or migrate schema on the request path.
3. Git identities resolve by the existing `git_remote` + `relative_path`
   relation. Non-git identities use a deterministic canonical ID derived from
   `(v2, anchor, anchor_shared, selector-when-unshared)` and persist as ordinary
   `projects` rows with nil git fields; the raw anchor is not stored.
4. A shared non-git anchor may return an existing explicitly shared binding.
5. A single existing legacy namespace remains canonical when it has not already
   been bound to a contradictory full identity.
6. A contradictory full identity receives a separate deterministic v2
   canonical namespace and never merges with the existing binding.
7. A legacy-only request resolves only when zero-or-one canonical candidate
   exists. Zero creates the legacy namespace for old-client compatibility; more
   than one fails before tenant data mutation.
8. Registration and alias/binding writes commit atomically. Repeated and late
   calls are idempotent; concurrent calls converge through database constraints
   and transaction locks.

## Stable error contract

| Code | Transport | Meaning | Upgrade action |
| --- | --- | --- | --- |
| `PROJECT_IDENTITY_INVALID` | HTTP 400 / gRPC `InvalidArgument` | V2 metadata is malformed or unsupported | `regenerate_project_identity_v2` |
| `PROJECT_IDENTITY_AMBIGUOUS` | HTTP 409 / gRPC `FailedPrecondition` | A legacy-only selector maps to multiple canonical identities | `send_project_identity_v2` |
| `PROJECT_IDENTITY_UNAVAILABLE` | HTTP 503 / gRPC `Unavailable` | Resolver database is unavailable before access | `retry_project_identity_registration` |

HTTP errors use `{error:{code,message,upgrade_action}}`. gRPC errors use
`google.rpc.ErrorInfo` with the same reason and `upgrade_action` metadata.
Messages are diagnostic; consumers branch on code/reason and action.

## Authorization invariant and threat model

Security classification: **S3 (High)** because this change selects tenant data
and can affect private-memory isolation.

- Spoofing: project metadata never authenticates a principal; existing HTTP and
  gRPC bearer validation remains authoritative.
- Tampering: strict source-form/path/anchor validation and transactional binding
  prevent malformed or partial convergence.
- Repudiation: stable error codes/actions and persisted binding timestamps make
  conflict outcomes observable without logging secrets.
- Information disclosure: the resolver returns only a canonical selector; it
  does not enumerate conflicting private projects.
- Denial of service: bounded metadata sizes and indexed/locked keys prevent
  unbounded input or process-local race loops.
- Elevation of privilege: selecting, guessing, or resolving a project never
  grants private access. Authorization and principal visibility filters run
  independently after identity resolution.

## Compatibility and versioning decision

- Protobuf classification: `ADDITIVE` and binary wire-safe. Existing field
  numbers are unchanged; new fields/messages receive new tags only.
- New client -> old server: old protobuf runtimes ignore unknown fields; the
  existing selector remains populated.
- Old client -> new server: absent-v2 resolution remains compatible only while
  the selector is unambiguous; ambiguity intentionally fails closed with an
  upgrade action.
- New client -> new server: full identity is registered synchronously before
  first handler/data access.
- Database: no schema expansion and no request-path DDL. Existing `projects`
  rows provide the binding relation; restart reuses their metadata/aliases and
  rollback sees ordinary compatible project rows plus ignored protobuf fields.
- Anchor file: `.engram-project-v2.json` is additive; old clients continue using
  `.engram-project` and ignore the v2 file.

Primary-source lookup on 2026-07-10:

- `https://protobuf.dev/programming-guides/proto3/` returned HTTP 200 and states
  that adding fields is binary wire-safe, old binaries ignore new fields, new
  binaries parse old messages, and unknown fields are preserved in binary form.
- `https://protobuf.dev/best-practices/dos-donts/` returned HTTP 200 and states
  that tag numbers must never be reused and deleted tags should be reserved.
- `https://grpc.io/docs/languages/go/generated-code/` returned HTTP 200 and
  documents the `protoc` plus `protoc-gen-go-grpc` generated client/server
  interface contract.
- A guessed gRPC versioning guide URL returned HTTP 404. No compatibility claim
  is grounded on that missing page; mixed-version behavior will be proven with
  this repository's `protoc 34.0`, `protoc-gen-go v1.36.11`,
  `protoc-gen-go-grpc v1.6.1`, generated bindings, and executable tests.

## Verification contract

- One shared JSON vector corpus is consumed by Go, Claude Node, and OpenClaw
  TypeScript tests.
- RED precedes production edits and is recorded under
  `.agent/specs/security-project-identity/evidence/`.
- GREEN covers collision/concurrency, contradictory identities, explicit
  anchor sharing, first CallTool-before-hook, late/repeated hook registration,
  old/new mixed versions, restart, rollback, and fresh migrated DB behavior.
- Prove-It substitutes resolver/generator bodies with sentinels and must produce
  failures before restoration.
- Final gates include Go unit/integration/race/repeat, Node tests, TypeScript
  build/tests, protobuf regeneration parity, coverage, residue, secret scan,
  exact owned-path diff, and a clean atomic commit.

## Maker self-review corrections

The post-GREEN behavioral-edge pass found and fixed issues that the first
structural implementation did not prove:

- old HTTP metadata initially resolved `legacy_project` first and could make it
  canonical on a fresh database; the outer `project` is now always canonical and
  `legacy_project` is only a conflict-checked alias;
- selector/metadata whitespace and control characters were initially normalized
  or accepted; they now fail invalid before database access;
- a soft-deleted deterministic binding could initially receive aliases; active
  row predicates now make that collision fail closed without mutation;
- raw PostgreSQL diagnostics initially reached HTTP/gRPC errors; transports now
  serialize stable public messages only;
- JavaScript anchor readers initially ignored unknown fields; Go, Claude, and
  OpenClaw now enforce the same exact three-field versioned file;
- Claude initially treated every non-HTTP exception as offline; only explicit
  network/timeout failures retain offline fallback, while malformed
  reached-server responses skip the handler.

Each correction was preceded by a focused failing regression and is recorded in
the RED/GREEN/Prove-It evidence files.
