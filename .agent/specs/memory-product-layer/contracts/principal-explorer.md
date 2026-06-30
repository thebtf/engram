# Principal Explorer Contract

Feature: ENG-MPL-1
CR: CR-001-initial-scope
Task: T006
Status: live-substrate-classified

## Purpose

The principal explorer substrate lets an authorized operator or agent query principal/domain/project-scoped memory through bounded, attributed read paths. It is a backend contract for the later touched UI surface and principal-scoped brief path; it is not a queue UI, forgetting engine, consolidation workflow, or temporal-truth layer.

## Live Surfaces

| Surface | Entry point | Authority |
| --- | --- | --- |
| MCP | `query_principal_memory` | `internal/mcp/tools_principal_memory.go` |
| REST | `GET /api/memories/principal` | `internal/worker/handlers_principal_memory.go` |
| Shared service | `principalmemory.PrincipalMemoryQueryService` | `internal/principalmemory/query_service.go` |
| Storage seam | `ListPrincipalMemory(ctx, project, opts)` | `internal/principalmemory/query_service.go` |
| Audit seam | `AuditLogger.Log` | `internal/principalmemory/query_service.go` |

The service is live in server wiring: `initializeAsync` creates `principalmemory.NewPrincipalMemoryQueryService(memoryStore, auditStore)` and wires it into MCP with `SetPrincipalMemoryQueryService`. The REST route is registered under the ready memory route group as `/api/memories/principal`.

## Request Contract

| Field | Required | Meaning |
| --- | --- | --- |
| `principal` | yes | Principal identifier to inspect, such as `agent/alice`. |
| `principal_kind` | no | `human`, `agent`, or `service`; defaults to `human`. |
| `project` | no | Project scope filter. |
| `domain` | no | Domain scope filter. |
| `q` / `query` | no | Content substring filter; MCP prefers `query` as the alias. |
| `visibility` | no | `shared`, `private`, or `all`; empty and `all` do not force private widening. |
| `include_private` | no | Explicit private widening request. Cross-principal private widening requires admin identity and durable audit. |
| `limit` | no | Bounded result count; default `50`, maximum `500`. |
| `offset` | no | Non-negative page offset. |
| `session_id` | no | Audit provenance for private widening. |

## Response Contract

The response returns:

- `principal`, `principal_kind`, `project`, and `domain` echoing the bounded scope.
- `items`, always present and bounded.
- `hidden_count`, counting rows withheld by visibility, domain policy, or private-widening gates.
- `audit_status`, with `not_required` or `written`.
- `audit`, including `action=principal_memory_query` and whether a durable audit entry was written.

Each item includes attribution fields required by the operator surface:

- `id`
- `project`
- `content`
- `tags`
- `owner_principal`
- `owner_principal_kind`
- `agent_visibility`
- `domain`
- `confidence`
- `created_at`

The shared service also preserves backend-only enrichment fields on query items (`status`, `tier`, `source_agent`, `updated_at`, `version`) for follow-on use cases.

## Privacy And Domain Rules

Private visibility is fail-closed:

- shared or empty visibility may be read when the domain policy allows it;
- self-private reads are allowed for the same principal;
- cross-principal private widening is denied for non-admin callers before the store is queried;
- admin cross-principal private widening requires `include_private=true`;
- admin private widening writes a durable audit entry before returning private data;
- audit failure returns an error and emits no private data.

Domain reads are filtered through `scope.DomainOwnershipPolicy`. Rows that fail domain policy are hidden and counted; they are not returned.

## Classification

T006 originally described the explorer substrate as missing. Current-code inspection classified it as `live`, not `must-build`:

- the shared query service already layers over the existing privacy visibility and domain ownership seams;
- MCP and REST already expose bounded principal/domain/project query surfaces;
- tests already cover attribution, route/tool exposure, hidden-row paging, private fail-closed behavior, and audit failure handling.

The source artifact named in T006, `.agent/specs/principal-memory-query-domain-registry/architecture.md`, is absent from this worktree and absent from `HEAD`. This contract therefore records current code truth and does not assume a finished explorer from missing PMQ docs.

## Verification

Focused T006 verification:

```powershell
go test ./internal/principalmemory ./internal/mcp ./internal/worker -run "PrincipalMemory|QueryPrincipal" -count=1
```

Result: PASS for all three packages on 2026-06-28.

