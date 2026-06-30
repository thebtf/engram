# Principal Memory Surface Design Contract

**Feature:** ENG-MPL-1 memory-product-layer  
**CR:** CR-001-initial-scope  
**Task:** T005  
**Surface:** principal-memory-surface  
**Status:** Approved for backend/UI implementation after PM scenario review  
**Reviewed:** 2026-06-28  

## Surface Boundary

This contract covers the touched operator-console flow where an operator inspects principal/domain/project-scoped knowledge and requests a bounded principal-scoped brief.

It does not cover review queue UI, forgetting/consolidation actions, experience/applicability, archive retrieval, temporal truth, or destructive memory mutation.

## User Outcome

An operator can choose a principal and scope, inspect bounded attributed knowledge, request a scoped brief, and see whether each visible capability is live, empty, gated, mustbuild, stale, or error.

## Blocks

| Block | Purpose | Backend binding | State rule |
| --- | --- | --- | --- |
| Scope bar | Select principal, domain, project, and refresh scope | T006 principal/domain query substrate; current project context | Never shows cross-principal data until backend confirms authorization |
| Knowledge summary | Bounded attributed principal/domain/project knowledge list | T006 query response | Empty state is honest; no row-level "keep in prompts" language |
| Brief panel | Principal-scoped brief preview and refresh | T007 `get_memory_brief` principal/domain extension or equivalent | Shows mustbuild/gated until T007 binding exists |
| Scope evidence | Displays source, freshness, and privacy evidence | T003 state plane plus T006/T007 response metadata | Missing evidence becomes gated or error, not silent success |
| State banner | Communicates loading, empty, gated, mustbuild, stale, error, and risky-confirm states | Shared operator-console load-state pattern | Unsupported behavior never appears as an enabled command |

## Controls

| Control | Type | Required behavior | Backend binding |
| --- | --- | --- | --- |
| `principal-select` | Select | Chooses authorized principal scope; defaults to current operator/agent principal when available | T006 principal query substrate |
| `domain-select` | Select/filter | Narrows to domain registry scope; supports all-domain view only when backend marks it authorized | T006 domain filter |
| `project-scope` | Select/filter | Narrows results to current or selected project | T006 project filter |
| `refresh` | Icon/button | Re-reads current scope without changing selection | T006 query and T007 brief refresh |
| `brief-refresh` | Button | Requests bounded principal-scoped brief for selected scope | T007 brief scope path |
| `attribution-toggle` | Toggle | Shows or hides attribution/provenance details already returned by backend | T006/T007 attribution fields |

Controls intentionally excluded: approve/reject/suppress/archive/consolidate/destroy, queue actions, bulk actions, and per-row prompt-retention actions.

## Usage Flow

1. Operator enters the memory surface from the existing operator-console memory area.
2. Surface resolves current project/session state and available principal scope.
3. Operator selects a principal, optional domain, and optional project scope.
4. Surface requests bounded knowledge summary and brief scope evidence.
5. Surface renders live, empty, gated, stale, mustbuild, or error state based on the response.
6. Operator may refresh the same scope or ask for a principal-scoped brief.
7. If backend support is absent, the surface shows a mustbuild/gated state and preserves the selected scope for retry after implementation.

## Backend Binding Map

| Visible element | Required backend field or route | Owner task | State if absent |
| --- | --- | --- | --- |
| Principal options | authorized principal identifiers and labels | T006 | gated |
| Domain options | domain registry entries scoped to principal/project | T006 | gated |
| Knowledge list | bounded records with content summary, scope, attribution, freshness | T006 | mustbuild |
| Empty result message | explicit empty response with scope echo | T006 | error if response shape missing |
| Brief content | bounded brief text with principal/domain/project scope echo | T007 | mustbuild |
| Brief freshness | freshness/source evidence | T007 | gated |
| Privacy evidence | fail-closed visibility metadata | T006/T007 | gated |
| Native resume/source marker | state source/freshness when relevant to current session | T003/G001 | stale or error |

## Honest States

| State | Trigger | Operator result |
| --- | --- | --- |
| loading | Request in flight | Existing content dims only if prior content exists; no fake placeholders imply data exists |
| live | Backend returns supported data for selected scope | Show bounded knowledge and/or brief with scope evidence |
| empty | Backend returns supported empty response | Show empty state tied to selected scope; refresh remains available |
| gated | Backend denies or cannot prove authorization/privacy scope | Show blocked state; do not reveal partial private data |
| mustbuild | Backend seam for a visible block is not implemented yet | Show unavailable state; no enabled control for that block |
| stale | Cached data exists but freshness/source indicates stale | Show stale label and refresh action |
| error | Backend or parse failure | Show retry and keep previous safe selection |
| risky-confirm | Operator attempts a scope widening that may expose private data | Require explicit confirmation or block if backend cannot authorize |

## Scenario Proof

| Branch | Operator-seat thought experiment | Expected result | PM verdict |
| --- | --- | --- | --- |
| happy path | I enter memory, select the current agent principal, choose a domain, and request a brief. | Surface shows bounded attributed knowledge and a scoped brief with freshness/source evidence. | PASS |
| empty state | I select a valid principal/domain pair with no hot memory. | Surface says the selected scope has no matching knowledge and leaves refresh/scope controls usable. | PASS |
| validation failure | I request a brief without a principal or with an invalid domain. | Surface keeps me in place, marks the invalid control, and does not issue a broad unscoped query. | PASS |
| gated path | I select a private principal or domain the backend cannot authorize. | Surface shows gated state and reveals no private content. | PASS |
| risky confirmation | I widen from current principal to another authorized principal. | Surface asks for explicit confirmation or keeps the scope gated until backend returns authorization evidence. | PASS |
| rollback | A refresh fails after a live result was visible. | Surface keeps the previous safe result marked stale/error and lets me retry or narrow scope. | PASS |
| recovery | Backend support lands after a mustbuild/gated state. | Refresh reuses the saved scope and moves the block to live or empty without changing operator intent. | PASS |

## PM Review Result

**Result:** PASS

The contract includes controls, usage flow, backend bindings, honest states, and all required branch scenarios. Every branch ends in an operable or honest blocked state. The backend wiring map covers every visible control on the touched surface. UI implementation remains blocked until T006/T007 provide the backend seams named above.

## Implementation Constraints

- T008 must implement from this contract and its JSON sidecar.
- T008 must not introduce review queue UI or mutation controls.
- T008 must not use per-row "keep in prompts" semantics on the touched flow.
- Private visibility remains fail-closed.
- Missing backend behavior must render as `mustbuild` or `gated`, not as disabled-looking live controls.
- Locale/i18n changes must preserve the same state model in every touched locale.
