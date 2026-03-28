# Specification: MCP Tools Refactoring

**Status:** Closed — FR1-FR6 implemented. FR7/FR8 (namespace prefix) moved to TECHNICAL_DEBT.md (2026-03-28). Remaining work covered by `plugin-tool-consolidation` spec.

## Overview

Refactor engram's 57 MCP tools to fix type safety bugs, reduce tool count visible to agents, consolidate overlapping tools, and separate concerns. Current state: agents see all 57 tools equally, ~12 used regularly, zero type coercion causes runtime errors.

## Functional Requirements

- FR1: All numeric parameters (62 fields across 57 tools) must accept both JSON number and string representations without runtime errors
- FR2: Default tool listing returns only primary tools (T1+T2, ~15 tools); full set available via explicit request
- FR3: Search variants (search, find_by_type, find_by_concept, find_by_file, decisions, changes, how_it_works) consolidated into single `search` tool with filter parameters and preset modes
- FR4: Timeline variants (timeline, get_context_timeline, get_timeline_by_query, get_recent_context) consolidated into single `timeline` tool with mode parameter
- FR5: Graph variants (find_related_observations, get_observation_relationships, get_graph_neighbors) consolidated into single `graph_query` tool
- FR6: Backward compatibility — old tool names work as aliases during transition period
- FR7: Vault tools (store_credential, get_credential, list_credentials, delete_credential, vault_status) grouped under distinct namespace prefix
- FR8: Document tools (list_collections, list_documents, get_document, ingest_document, search_collection, remove_document) grouped under distinct namespace prefix

## Non-Functional Requirements

- NFR1: Zero breaking changes for existing MCP clients — old tool names must continue to work
- NFR2: Type coercion must handle: int, int64, float64, string→int, float64→int, string→float64
- NFR3: Default tool set must fit comfortably in agent context window (<4KB of tool descriptions)

## Acceptance Criteria

- [ ] AC1: `search` tool called with `limit: "5"` (string) returns results without error
- [ ] AC2: `search` tool called with `limit: 5.0` (float64) returns results without error
- [ ] AC3: Default `tools/list` response contains ≤20 tools
- [ ] AC4: `tools/list` with `include_all: true` returns full 57+ tool set
- [ ] AC5: Old tool name `find_by_file` routes to consolidated `search` with `files` filter
- [ ] AC6: Old tool name `decisions` routes to consolidated `search` with `preset: "decisions"`
- [ ] AC7: All existing tests pass after refactoring
- [ ] AC8: `go vet ./internal/mcp/...` clean

## Out of Scope

- Removing any tool permanently (only hiding from default listing)
- Changing tool response formats
- Adding new tools
- Modifying hook behavior
- Database schema changes

## Dependencies

- None — all changes are in `internal/mcp/` package
