# Feature: MCP Tool API Consolidation (61 → 6+1)

**Slug:** mcp-tool-api-consolidation
**Created:** 2026-03-28
**Status:** Draft
**Author:** AI Agent (reviewed by user)
**Predecessor:** plugin-tool-consolidation (cleanup phase — removed 7 duplicates, expanded OpenClaw)

## Overview

Consolidate engram's 61 MCP tools into 6 primary tools plus `check_system_health`. Each
primary tool absorbs multiple current tools as parameter modes/actions. Backward-compatible
dispatch aliases ensure existing clients continue to work unchanged. This directly implements
Constitution Principle #12 (Tool Count Is a Budget): reducing ~6100 context tokens per session
to ~700 tokens (6 tools × ~100 tokens each).

## Context

### Problem
Agents use 2 of 61 tools (store_memory, search). Root cause: 17 search variants, 12 doc tools,
5 vault tools are separate tools instead of parameters to unified operations. The tool list
overwhelms agent context windows, hiding useful capabilities behind a wall of similar-sounding names.

### Current State (v2.0.9)
- 61 registered MCP tools in 3 tiers: Core (9), Useful (14), Admin (38)
- 7 additional dispatch aliases for removed tools (from plugin-tool-consolidation PR #112)
- Each tool adds ~100 tokens to every MCP `tools/list` response
- Agents default to store_memory + search because they're the most obviously named

### Classification (from audit 2026-03-28)

| Primary Tool | Absorbs | Count |
|-------------|---------|-------|
| `recall` | search, decisions, changes, how_it_works, find_by_file, find_by_concept, find_by_type, find_similar_observations, recall_memory, search_sessions, explain_search_ranking, timeline, find_related_observations, get_observation, get_patterns, list_sessions | 16 |
| `store` | store_memory, edit_observation, merge_observations, import_instincts | 4 |
| `feedback` | rate_memory, suppress_memory, set_session_outcome | 3 |
| `vault` | store_credential, get_credential, list_credentials, delete_credential, vault_status | 5 |
| `docs` | doc_create, doc_read, doc_list, doc_history, doc_comment, list_collections, list_documents, get_document, remove_document, ingest_document, search_collection | 11 |
| `admin` | bulk_delete_observations, bulk_mark_superseded, bulk_boost_observations, tag_observation, get_observations_by_tag, batch_tag_by_pattern, graph_query, get_graph_stats, get_memory_stats, get_temporal_trends, get_data_quality_report, analyze_observation_importance, analyze_search_patterns, get_observation_quality, get_observation_scoring_breakdown, suggest_consolidations, trigger_maintenance, get_maintenance_stats, run_consolidation, export_observations, backfill_status | 21 |
| `check_system_health` | (stays as-is) | 1 |
| **Total** | | **61** |

## Functional Requirements

### FR-1: Create `recall` Tool (absorbs 16 tools)
The system must provide a single `recall` tool that supports all search/retrieval operations
via a required `action` parameter:

| Action | Replaces | Key Parameters |
|--------|----------|----------------|
| `search` | search | query, project, limit |
| `preset` | decisions, changes, how_it_works | query, preset (decisions/changes/how_it_works) |
| `by_file` | find_by_file | files, project |
| `by_concept` | find_by_concept | concept, project |
| `by_type` | find_by_type | type, project |
| `similar` | find_similar_observations | query, min_similarity |
| `timeline` | timeline | mode (recent/anchor/query), anchor_id, query |
| `related` | find_related_observations | id, min_confidence |
| `patterns` | get_patterns | project, type |
| `get` | get_observation | id |
| `sessions` | search_sessions, list_sessions | query (search) or omit (list) |
| `explain` | explain_search_ranking | query, project |

Default action: `search` (when action omitted, treat as `search`).
`recall_memory` alias maps to action `search` with format param.

### FR-2: Create `store` Tool (absorbs 4 tools)
The system must provide a single `store` tool with actions:

| Action | Replaces | Key Parameters |
|--------|----------|----------------|
| `create` | store_memory | content, title, type, tags, scope, always_inject, ttl_days |
| `edit` | edit_observation | id, title, narrative, facts, concepts, status, always_inject |
| `merge` | merge_observations | source_id, target_id, boost |
| `import` | import_instincts | path, project |

Default action: `create`.

### FR-3: Create `feedback` Tool (absorbs 3 tools)
The system must provide a single `feedback` tool with actions:

| Action | Replaces | Key Parameters |
|--------|----------|----------------|
| `rate` | rate_memory | id, useful (boolean) |
| `suppress` | suppress_memory | id |
| `outcome` | set_session_outcome | outcome (success/partial/failure/abandoned), reason |

No default — action required.

### FR-4: Create `vault` Tool (absorbs 5 tools)
The system must provide a single `vault` tool with actions:

| Action | Replaces | Key Parameters |
|--------|----------|----------------|
| `store` | store_credential | name, value, scope, project |
| `get` | get_credential | name |
| `list` | list_credentials | (none) |
| `delete` | delete_credential | name |
| `status` | vault_status | (none) |

No default — action required.

### FR-5: Create `docs` Tool (absorbs 11 tools)
The system must provide a single `docs` tool with actions:

| Action | Replaces | Key Parameters |
|--------|----------|----------------|
| `create` | doc_create | path, project, content, doc_type |
| `read` | doc_read | path, project, version |
| `list` | doc_list | project, doc_type |
| `history` | doc_history | path, project |
| `comment` | doc_comment | path, project, comment, line |
| `collections` | list_collections | (none) |
| `documents` | list_documents | collection |
| `get_doc` | get_document | collection, id |
| `remove` | remove_document | collection, id |
| `ingest` | ingest_document | collection, content, metadata |
| `search_docs` | search_collection | collection, query |

No default — action required.

### FR-6: Create `admin` Tool (absorbs 21 tools)
The system must provide a single `admin` tool with actions:

| Action | Replaces | Key Parameters |
|--------|----------|----------------|
| `bulk_delete` | bulk_delete_observations | ids |
| `bulk_supersede` | bulk_mark_superseded | ids |
| `bulk_boost` | bulk_boost_observations | ids, amount |
| `tag` | tag_observation | id, add, remove |
| `by_tag` | get_observations_by_tag | tag |
| `batch_tag` | batch_tag_by_pattern | pattern, tag, action |
| `graph` | graph_query | id, mode |
| `graph_stats` | get_graph_stats | (none) |
| `stats` | get_memory_stats | (none) |
| `trends` | get_temporal_trends | project, days |
| `quality` | get_data_quality_report | project |
| `importance` | analyze_observation_importance | project |
| `search_analytics` | analyze_search_patterns | project |
| `obs_quality` | get_observation_quality | id |
| `scoring` | get_observation_scoring_breakdown | id |
| `consolidations` | suggest_consolidations | project |
| `maintenance` | trigger_maintenance | (none) |
| `maintenance_stats` | get_maintenance_stats | (none) |
| `consolidation` | run_consolidation | project |
| `export` | export_observations | project, format |
| `backfill_status` | backfill_status | (none) |

No default — action required.

### FR-7: Backward-Compatible Dispatch Aliases
All 61 original tool names must continue to work when called via `callTool`. The dispatch
switch maps old names to the new primary tool + action. Clients calling `find_by_file(files="x")`
get identical results to `recall(action="by_file", files="x")`.

### FR-8: Tiered Registration
Default `tools/list` returns only 6+1 primary tools (recall, store, feedback, vault, docs,
admin, check_system_health). With `cursor=all` or `include_all=true`, also returns all 61
original tools as full Tool objects with their original schemas (same format as pre-consolidation).
Primary tools appear first in the list, aliases after.

## Non-Functional Requirements

### NFR-1: Zero Client Breakage
Every existing MCP client must continue to work without modification. Response formats
must be identical for alias calls vs primary tool calls with equivalent parameters.

### NFR-2: Context Window Reduction
Default `tools/list` response must be under 1000 tokens total (7 tools × ~130 tokens each).
Current: ~6100 tokens for 61 tools.

### NFR-3: Response Time Parity
Primary tool calls must not add measurable latency compared to direct tool calls.
The action routing must be a simple string switch, not a search/lookup.

### NFR-4: Input Schema Completeness
Each primary tool's input schema uses a flat structure: `action` as required enum + all
parameters optional. Each parameter description notes which action(s) it applies to.
Target: under 200 tokens per tool schema. No discriminated unions (they bloat beyond savings).

## User Stories

### US1: Agent Uses 7 Tools Instead of 61 (P1)
**As an** AI agent using engram via MCP, **I want** to see only 7 well-described tools,
**so that** I can quickly identify the right tool for any memory operation.

**Acceptance Criteria:**
- [ ] Default `tools/list` returns exactly 7 tools
- [ ] Each tool description explains ALL available actions
- [ ] Agent can perform any operation available in the old 61-tool set

### US2: Existing Client Continues Working (P1)
**As an** existing MCP client calling old tool names, **I want** my calls to work unchanged,
**so that** I don't need to update any code.

**Acceptance Criteria:**
- [ ] `search(query="x")` returns same result as `recall(action="search", query="x")`
- [ ] `store_memory(content="x")` returns same result as `store(action="create", content="x")`
- [ ] All 61 old tool names dispatch correctly
- [ ] All 7 backward compat aliases from PR #112 still work

### US3: Context Window Savings (P1)
**As a** system operator, **I want** reduced MCP tool payload,
**so that** agents have more context window for actual work.

**Acceptance Criteria:**
- [ ] Default tools/list payload < 1000 tokens
- [ ] Previously: ~6100 tokens → now: ~900 tokens (>80% reduction)

## Edge Cases

- Old tool called with parameters matching new schema (e.g., `search(action="by_file")`) — must work, action param passed through
- New tool called without `action` param — use default action or return clear error
- `recall` called with both `action="search"` and `preset="decisions"` — preset takes precedence (matches current behavior)
- `admin` called with unknown action — return descriptive error listing valid actions
- Alias `doc_update` (removed in PR #112) — still dispatches to `docs(action="create")`
- `cursor=all` returns old tool names alongside new primary tools — no duplicate functionality shown

## Out of Scope

None — this is the complete consolidation. No items deferred.

## Dependencies

- v2.0.9 (PR #112) must be merged — it removed 7 duplicate tools and established dispatch alias pattern
- Constitution Principle #12 — this spec directly implements it

## Success Criteria

- [ ] `tools/list` returns 7 tools (6 primary + health)
- [ ] `tools/list?cursor=all` returns 7 primary + 61 aliases = 68 entries
- [ ] All existing tests pass with zero modification to test assertions about tool behavior (only tool name expectations change)
- [ ] `go test ./internal/mcp/...` passes
- [ ] Context window reduction verified: <1000 tokens for default tools/list

## Clarifications

### Session 2026-03-28

| # | Category | Question | Resolution | Date |
|---|----------|----------|------------|------|
| C1 | UX Flow | How to structure input schema for multi-action tools? | Flat: action enum + all params optional. Descriptions note applicable actions. Target <200 tokens/tool. No discriminated unions. | 2026-03-28 |
| C2 | Integration | How should aliases appear in cursor=all? | Full Tool objects with original schemas (backward compat). Primary tools first, aliases after. | 2026-03-28 |

## Clarification Summary

| Category | Status |
|----------|--------|
| Functional Scope | Clear |
| User Roles | Clear |
| Domain/Data Model | Clear |
| Data Lifecycle | Clear |
| Interaction & UX Flow | Resolved (C1) |
| Non-Functional: Perf/Scale | Clear |
| Non-Functional: Reliability | Clear |
| Non-Functional: Security | Clear |
| Integration | Resolved (C2) |
| Edge Cases | Clear |
| Constraints & Tradeoffs | Clear |
| Terminology | Clear |
| Completion Signals | Clear |
| Miscellaneous | Clear |

**Questions asked/answered:** 2/2
**Spec status:** Ready for planning
**Next:** /speckit-plan

## Open Questions

None.
