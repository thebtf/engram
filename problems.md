# US9 Chunk 1 — Cascade Compile Errors (do-not-touch files)

These files were explicitly excluded from chunk 1 scope. They import `internal/search`
or `internal/search/expansion` and will fail to compile after the search package is
deleted. They are tracked here as known cascade errors for chunk 2.

## Files with cascade errors

### internal/mcp/coerce.go

Imports `"github.com/thebtf/engram/internal/search"`.

Affected symbols:
- `buildSearchParams(m map[string]any) search.SearchParams` — builds SearchParams from MCP args
- `buildTimelineParams(m map[string]any) TimelineParams` — references TimelineParams which was
  removed from server.go in this chunk

Fix in chunk 2: remove `buildSearchParams` and `buildTimelineParams` functions; remove `search` import.

### internal/mcp/tools_memory.go

Imports `"github.com/thebtf/engram/internal/search"`, uses `s.searchMgr`.

Affected symbols:
- `handleRecallMemory` — calls `s.searchMgr.UnifiedSearch` with `search.SearchParams`

Fix in chunk 2: rewrite `handleRecallMemory` to use `observationStore.SearchObservationsFTS`
or `memoryStore.List` as appropriate; remove `search` import.

### internal/worker/retrieval.go

Imports `"github.com/thebtf/engram/internal/search/expansion"`, uses `s.queryExpander`.

Affected symbols:
- `expandQueries(ctx, query)` — calls `s.queryExpander.Expand` (returns `[]expansion.ExpandedQuery`)
- Several retrieval functions call `expandQueries` and pass expanded query slices

Fix in chunk 2: remove `expandQueries` function body (replace with trivial return of original
query); remove `expansion` import.

### internal/worker/retrieval_helpers.go

Uses `s.llmFilter` (field was `*search.LLMFilter`, removed from Service struct in this chunk).

Affected symbols:
- `filterByRelevance` helper — calls `s.llmFilter.FilterByRelevance`

Fix in chunk 2: remove the `s.llmFilter` branch; leave only `s.retrievalHooks.filterByRelevance`
path and the else fallback.

### internal/worker/handlers_context.go

References `s.searchMgr` (field removed from Service struct in this chunk).

Fix in chunk 2: audit for search.Manager references and replace with direct store calls.

### internal/worker/handlers_tags.go

References `s.searchMgr` (field removed from Service struct in this chunk).

Fix in chunk 2: audit for search.Manager references and replace with direct store calls.

## Build status after chunk 1

`go build ./internal/mcp/...` — FAILS (coerce.go + tools_memory.go import deleted package)
`go build ./internal/worker/...` — FAILS (retrieval.go + retrieval_helpers.go + handlers_* import deleted package)
`go build ./...` — FAILS (all of the above)

These are expected cascade errors scoped to chunk 2.
