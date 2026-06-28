## Task T007 - Implementation Log

### Quoted AC
> - AC: principal-scoped brief or equivalent exists, remains bounded, and returns scope/freshness evidence.
Source: `.agent/specs/memory-product-layer/changes/CR-001-initial-scope/tasks.md` | line 123

Supporting feature contract:
> The system must let an authorized operator inspect principal/domain/project-scoped knowledge surfaces without tag archaeology, including honest attribution and live/gated/error states.
Source: `.agent/specs/memory-product-layer/spec.md` | line 59

> Principal-private memory and state must remain fail-closed. Cross-principal visibility or widening requires explicit authorized paths and audit.
Source: `.agent/specs/memory-product-layer/spec.md` | line 104

> Principal/domain/project-scoped views are bounded and attributed.
> Private visibility remains fail-closed.
Source: `.agent/specs/memory-product-layer/spec.md` | lines 154 and 156

### User Change Enabled
Agents can now request a compact `get_memory_brief` for a principal/domain/project scope before delegation, instead of only receiving a project-level adaptive injection brief.

### Claim Grounding
- Claim: existing `get_memory_brief` was project-scoped only. Evidence: RED `TestGetMemoryBrief_PrincipalScopeSchemaAdvertised` failed because the schema exposed only `topic`, `project`, and `limit`.
- Claim: principal/domain scoping is now routed through the live T006 privacy substrate. Evidence: `handleGetMemoryBrief` branches to `handlePrincipalMemoryBrief` when scoped fields are present and calls `PrincipalMemoryQueryService.Query`.
- Claim: scoped briefs stay bounded. Evidence: handler keeps the existing default `limit=5`, max `10`, and passes the normalized limit to the query service.
- Claim: scope/freshness evidence is returned. Evidence: scoped responses include `source=principal_query`, `freshness=live`, `generated_at`, and a `scope` object with project/principal/domain, hidden count, audit status, and audit metadata.

### Terminology Alignment
- "Principal-scoped brief" means a compact `get_memory_brief` response filtered by principal and optional domain/project, not a new prompt-injection engine.
- "Equivalent" means the brief may reuse the T006 principal query service rather than duplicating storage or recall logic.
- "Freshness evidence" means the response tells callers this was a live principal query and when the packet was generated.

### Implementation Decision
Keep the legacy project brief path intact for calls without principal/domain scope. For scoped calls, require the T006 query service and reuse its privacy, domain, hidden-count, and audit behavior. This keeps T007 small and avoids creating a second principal filtering implementation.

### Verification Result
AC-by-AC:
  - AC 1: [PASS] - `get_memory_brief` accepts principal/domain fields, returns bounded attributed memories, and includes scope/freshness evidence.

Commands:
  - RED: `go test ./internal/mcp -run "TestGetMemoryBrief" -count=1` failed because `get_memory_brief` did not advertise principal fields and scoped args fell through to the legacy project-injection path.
  - GREEN: `go test ./internal/mcp -run "TestGetMemoryBrief" -count=1` - PASS.
  - GREEN: `go test ./internal/mcp ./internal/principalmemory -count=1` - PASS.
  - GREEN: `go test ./internal/mcp ./internal/principalmemory ./internal/worker -count=1` - PASS.

Overall: [PASS]

### NEEDS_CLARIFICATION (if AMBIGUOUS result)
N/A
