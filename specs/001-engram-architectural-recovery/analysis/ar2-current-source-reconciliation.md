# AR-2 Current Source Reconciliation

**Release slice:** AR-2 Project Identity V3 Expand
**Candidate baseline:** `cfc0715f021998daea17d0fba56f5cf6191bbf9d`
**Candidate branch:** `ar-2/identity-v3-expand`
**Authority:** the active constitution, `project-identity-v3.md`, `project-anchor-v3.schema.json`, the AR-2 intake package, and `tasks.md` T016–T030 plus T088–T091.

## Reconciliation verdict

AR-2 is a must-build V3 authority. The current implementation contains V2 compatibility behavior only; it has no V3 descriptor, anchored UUID binding, typed `project_key`, resolution intent, or central V3 resolver. The delta does not change AR-2 scope. It corrects task ownership so that every adapter becomes a translation boundary around one new `internal/projectidentity` authority.

The primary baseline and this integration worktree begin with the legacy tracked root anchor:

```json
{
  "name": "engram"
}
```

AR-2 must migrate that exact file in place to the contract's V3 repository anchor with `project_id` `b42c9854-bf02-4d1b-867d-a2a096f9e4d0`. It must not create `.engram-project-v3`, derive a canonical key, or write a client-specific anchor.

## Verified current-state map

| Boundary | Current source fact | AR-2 disposition |
|---|---|---|
| Go client discovery | `internal/proxy/identity.go` defines and validates `ProjectIdentityV2`; `ResolveProjectIdentityV2` derives a path-hash legacy ID and reads or creates untracked `.engram-project-v2.json`. | Retain only as V2 compatibility evidence. New V3 anchor/parser/normalizer belongs in `internal/projectidentity`. |
| Server/store | `internal/db/gorm/project_store.go` duplicates V2 metadata and owns `RegisterAndResolve`, `p2g_`/`p2n_` binding derivation, alias mutation, legacy adoption, and duplicate collapse. | Do not extend this as V3 authority. Add a storage port/implementation consumed by `internal/projectidentity`; preserve V2 behavior only at explicit outer compatibility branches. |
| Current project schema | `Project` has string `ID`, remote/path/display/legacy fields only (`internal/db/gorm/models.go`). Migration registration ends at `161_project_continuity_slots` (`internal/db/gorm/migrations.go`). | Add migration `162+` with nullable UUID V3 columns, identifiers, audits, and selected nullable typed-key columns. Keep legacy string columns and rows readable; no backfill/cutover. |
| Anchor package | `internal/projectidentity/` contains only `legacy_fence_test.go`. | Build the single V3 authority here: anchor parsing/discovery, descriptor validation/normalization, intent/outcome model, resolver workflow, comparison record, and audit-safe refusal facts. |
| gRPC/protobuf | `proto/engram/v1/engram.proto` carries only `ProjectIdentityV2`; generated ownership is `engram.pb.go` and `engram_grpc.pb.go`; `make proto` regenerates both. | T024 owns proto source plus both generated bindings and gRPC translation. Generated bindings are derived artifacts and must never be hand-edited. |
| gRPC call flow | `Initialize` and `CallTool` in `internal/grpcserver/server.go` resolve V2 before invoking MCP and propagate canonical project/session context. | Replace only the V3-capable path with central V3 resolution; retain declared V2 compatibility outer branch. |
| HTTP context intake | `/api/context/inject` validates and synchronously calls V2 `RegisterAndResolve`; `/api/context/session-start` forwards a raw project to gRPC without V2/V3 resolution. | T025 must route both V3-capable HTTP paths through the central workflow and map typed outcomes consistently before retrieval/scoped access. |
| Hook adapter | `plugin/engram/hooks/lib.js` builds/publishes V2 anchors and posts identity-only registration; `session-start.js` caches selector-derived state. | T018 owns hook descriptor construction plus actual registration/cache call sites, not a new independent resolver. |
| OpenClaw adapter | `plugin/openclaw-engram/src/identity.ts` builds/publishes V2 identity; `client.ts` owns registration caches; `config.ts` has no `client_instance_id`; `index.ts` wires the shared client. | T019 owns `identity.ts`, `client.ts`, `config.ts`, `index.ts`, and actual hook consumers. V3 is enabled only with explicitly supplied opaque non-secret `client_instance_id`; no project resolution code may generate or persist it. |
| Daemon/MCP adapter | `internal/handlers/engramcore/slugcache.go`, `module.go`, and `tools.go` cache/transport V2 identity; `grpcpool.go` is connection-only; `cmd/engram/wiring.go` constructs the module. `cmd/engram/main.go` has no identity construction. | T026 owns the daemon cache/module/tools/wiring and MCP context consumption. It must consume centrally resolved scope and preserve named V2 compatibility; it must not make `main.go` an identity owner. |
| Refusal surface | Existing V2 HTTP errors distinguish invalid/ambiguous/unavailable, while session-start's generic gRPC-to-HTTP mapping does not preserve all identity detail. | T023 defines one typed V3 result/outcome set; T024–T026 map it without returning canonical key, candidate enumeration, raw DB errors, credentials, or raw private paths on refusal. |

## Reconciled required interfaces

1. **Descriptor:** `version`, `anchor_project_id`, `name`, `scope`, normalized credential-free remote evidence, typed legacy identifiers/provenance, and opaque `client_instance_id`; never a client-selected `project_key`.
2. **Intent:** `resolve_existing`, `register_anchor`, `read_filter`, and `admin_target` are explicit. Only `register_anchor` may establish a V3 binding.
3. **Outcome:** a successful resolution alone returns server-issued UUID `project_key`; every refusal returns no canonical key and performs no project, identifier, cache, audit-target, or scoped-data mutation except the permitted redacted attempt audit.
4. **Schema:** `projects.project_key`, `projects.anchor_project_id`, `identity_scope`, and `identity_status` are additive/nullable. `project_identifiers` and `project_merge_audits` are new V3 records. No historical merge, backfill, legacy contraction, or authority cutover belongs to AR-2.
5. **Comparison:** V3-vs-V2 records are redacted, idempotent telemetry. Mismatch never assigns, merges, redirects, backfills, or creates a second authority.

## Task ownership corrections

| Task | Corrected owning surfaces |
|---|---|
| T018 | `plugin/engram/hooks/lib.js`, `session-start.js`, and hook V3 tests/call sites. |
| T019 | `plugin/openclaw-engram/src/identity.ts`, `client.ts`, `config.ts`, `index.ts`, hook consumers, and TypeScript tests. |
| T021 | `internal/db/gorm` V3 model/store/migration ownership, migration `162+`, constraints/indexes, and real PostgreSQL migration tests. |
| T022–T023 | New `internal/projectidentity` domain/application authority and its storage/audit ports; adapters do not derive or select canonical keys. |
| T024 | `proto/engram/v1/engram.proto`, `engram.pb.go`, `engram_grpc.pb.go`, `Makefile` generation contract, and `internal/grpcserver` translation. |
| T025 | `internal/worker/handlers_context.go`, related HTTP/hook intake routes, and route-level refusal tests. |
| T026 | `internal/mcp`, `internal/handlers/engramcore/{slugcache,module,tools,grpcpool}.go`, and `cmd/engram/wiring.go`; not `cmd/engram/main.go`. |
| T027–T029 | `internal/projectidentity/comparison.go`, integration behavior matrix, and `internal/recoveryreceipt/ar2_identity_expand.go`. |

## Fixture and rollback boundary

Existing recovery scripts provide isolated-loopback ownership, staged-payload provenance, synthetic export/restore fingerprints, and receipt verification. They do **not** yet prove real V3 migration behavior: the existing synthetic export does not exercise V3 dual representation, and the prior owned fixture used generic `postgres:17`, which lacks pgvector. AR-2 must use an isolated pgvector-capable PostgreSQL fixture, explicit V3/V2 seeds, and exact candidate payload/readback evidence. Rollback returns adapters/readers to the V2 compatibility boundary while preserving all additive V3 schema, anchor, identifier, audit, and comparison evidence.

## Housekeeping binding

`ar1-worktree-branch-manifest.json` records the full AR-1 inventory and protected primary artifact. `ar1-cleanup-execution.json` records removal of the seven mechanically absorbed lines. AR-1 lines without a completed semantic supersession proof remain preservation work, not AR-2 source scope; their disposition must never be used to justify deleting user or historical evidence.

## Delta conclusion

The current source confirms the intake architecture rather than contradicting it. The required Spec Kit changes are path/ownership corrections and explicit dependency/acceptance additions. After those changes, checklist refresh, and a clean AR-2 analysis, implementation may begin directly from this integration branch.