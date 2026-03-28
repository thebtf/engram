# Implementation Plan: MCP Tool API Consolidation (61 → 6+1)

**Spec:** .agent/specs/mcp-tool-api-consolidation/spec.md
**Created:** 2026-03-28
**Status:** Draft

## Tech Stack

No new dependencies. Pure refactoring of `internal/mcp/server.go`.

| Component | Choice | Rationale |
|-----------|--------|-----------|
| Tool dispatch | Go switch statement | Existing pattern, zero overhead |
| Schema definition | `map[string]any` literals | Existing pattern in server.go |
| Backward compat | Dual-path dispatch | Old names → extract action → call primary handler |

## Architecture

```
tools/list (default)     tools/list (cursor=all)
       │                         │
       ▼                         ▼
  7 primary tools        7 primary + 61 alias Tool objects
       │                         │
       └─────────┬───────────────┘
                 │
            callTool(name, args)
                 │
        ┌────────┴─────────┐
        │ Primary name?     │ Alias name?
        │ (recall, store,   │ (search, find_by_file,
        │  feedback, ...)   │  store_memory, ...)
        ▼                   ▼
   handleRecall(args)   inject action param
   handleStore(args)    ────────────────────►  handleRecall(args')
   handleFeedback(args)                        handleStore(args')
   handleVault(args)                           ...
   handleDocs(args)
   handleAdmin(args)
```

Each primary handler: reads `action` from args → switch → delegates to existing handler function.
Alias dispatch: sets `action` in args map → calls primary handler.

## File Structure

```
internal/mcp/
  server.go              — modified: primary tool registrations + dispatch
  tools_recall.go              — NEW: handleRecall (routes to existing search handlers)
  tools_store_consolidated.go  — NEW: handleStore (routes to existing store handlers)
  tools_feedback.go            — NEW: handleFeedback (routes to existing feedback handlers)
  tools_vault_consolidated.go  — NEW: handleVault (routes to existing vault handlers)
  tools_docs_consolidated.go   — NEW: handleDocs (routes to existing doc handlers)
  tools_admin.go               — NEW: handleAdmin (routes to existing admin handlers)
  server_test.go         — modified: test expectations for 7 primary tools
  tools_memory.go        — unchanged (handler implementations stay)
  tools_search.go        — unchanged
  tools_observations.go  — unchanged
  ...                    — all other tools_*.go unchanged
```

## Phases

### Phase 1: Create 6 Primary Handler Files (FR-1 through FR-6)

For each primary tool, create a `tools_<name>.go` file containing:
1. A handler function: `handleRecall(ctx, args) (string, error)`
2. An `action` switch that routes to existing handler functions
3. Validation: unknown action → descriptive error listing valid actions

**Order:** recall → store → feedback → vault → docs → admin (largest first, validates pattern)

Each handler is a thin routing layer — NO new business logic. Example:

```go
func (s *Server) handleRecall(ctx context.Context, args json.RawMessage) (string, error) {
    m, err := parseArgs(args)
    if err != nil {
        return "", err
    }
    action := coerceString(m["action"], "search") // default: search
    switch action {
    case "search":
        return s.handleSearch(ctx, args)
    case "preset":
        // inject preset into args, call handleSearch
    case "by_file":
        return s.handleFindByFile(ctx, args)
    // ... etc
    default:
        return "", fmt.Errorf("unknown recall action: %q (valid: search, preset, by_file, ...)", action)
    }
}
```

### Phase 2: Register Primary Tools in server.go (FR-8)

Replace the 61-entry `tools` slice with 7 entries:
- `recall`, `store`, `feedback`, `vault`, `docs`, `admin`, `check_system_health`
- Each with flat schema: `action` enum + all params optional
- Keep the old 61-entry slice as `aliasTools` for `cursor=all`

### Phase 3: Update Dispatch Switch (FR-7)

In `callTool`, replace the current 80+ case switch with:
1. Primary tool names → call primary handler directly
2. Alias names → inject `action` param → call primary handler
3. Legacy aliases (doc_update, get_recent_context, etc.) → same routing

### Phase 4: Update Tests

- Update `TestHandleToolsList` to expect 7 tools
- Update `TestCallTool_ToolNameRecognition` to test primary + alias
- Add tests: each primary tool with each action
- Add tests: alias dispatch produces same results as primary call

### Phase 5: Version Bump + Release

- Bump to v2.1.0 (MINOR — new API surface, no breaking changes)
- Update openclaw-engram to 2.1.0
- Update Constitution #12 rationale with new tool count

## Library Decisions

| Component | Library | Rationale |
|-----------|---------|-----------|
| All | Custom (Go stdlib) | Pure routing logic, no external deps needed |

## Unknowns and Risks

| Unknown | Impact | Resolution |
|---------|--------|------------|
| Schema token count for 6 primary tools | MED | Measure after Phase 2 — each tool ~150-200 tokens with flat enum schema |
| Some handlers accept args differently (parseArgs vs json.Unmarshal) | LOW | Survey in Phase 1, normalize in handler |
| `search` handler has special preset logic | LOW | Recall handler delegates directly, preset param already supported |

## Constitution Compliance

| Principle | Compliance |
|-----------|-----------|
| #1 Server-Only | OK — server-side only refactoring |
| #3 Non-Blocking Hooks | N/A — no hook changes |
| #7 Bump Plugin Version | OK — openclaw bumped to match |
| #8 Complete Implementations | OK — every action fully implemented via existing handlers |
| #12 Tool Count Budget | **DIRECTLY IMPLEMENTS** — 61 → 7 tools |
| #15 Version Tracking | OK — unified bump to 2.1.0 |
