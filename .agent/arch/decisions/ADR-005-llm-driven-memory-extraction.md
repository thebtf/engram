# ADR-005: LLM-Driven Memory Extraction (extract_and_operate)

## Status

Proposed

## Context

Engram requires agents to explicitly call `store(content="...", title="...")` with
pre-formatted observation content. The agent must decide what to store, how to format it,
and which metadata to attach. This puts the burden on the calling agent.

Cipher's `extract_and_operate_memory` tool takes raw conversation content and uses LLM to:
1. Analyze the input for memorable content
2. Extract structured memories automatically
3. Decide operations (store new / update existing / delete obsolete)
4. Execute all operations in one call

Engram's current approach works but requires hooks to format data before sending.
The SDK processor (`internal/worker/sdk/processor.go`) does LLM extraction from tool events,
but it's triggered by hooks, not by the agent itself.

## Decision

Add an `extract_and_operate` action to the `store` primary tool that accepts raw content
and uses LLM to extract, classify, and store observations autonomously.

### Design

```
store(action="extract", content="<raw conversation or tool output>")
→ LLM analyzes content
→ Extracts 0-N observations with type, title, narrative, concepts
→ Dedup check against existing observations
→ Stores new, updates similar, flags conflicts
→ Returns: { extracted: N, stored: M, updated: K, skipped: J }
```

This replaces the manual `store(action="create", content="...", title="...")` workflow
for bulk extraction scenarios. Manual store remains for explicit single observations.

## Consequences

### Positive
- Agents can dump raw content and let engram decide what's memorable
- Higher quality observations (LLM-extracted vs agent-formatted)
- Automatic dedup and conflict detection
- Reduces hook complexity (hooks send raw events, server decides)

### Negative
- LLM cost per extraction call
- Latency increase (LLM in the critical path)
- Quality depends on extraction prompt quality

## Alternatives Considered

1. **Keep current approach**: Agent formats, server stores. Simple but manual.
2. **Background extraction only**: Server extracts from tool events (current behavior). No agent control.
3. **Hybrid** (chosen): Both explicit store and extract_and_operate available.

## References

- Cipher source: `src/core/brain/tools/definitions/memory/extract_and_operate_memory.ts`
- Engram current: `internal/worker/sdk/processor.go` ProcessObservation
