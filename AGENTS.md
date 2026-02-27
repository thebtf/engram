# AGENTS.md

## STACKS

```yaml
STACKS: [GO]
```

## PROJECT OVERVIEW

Fork of [claude-mnemonic](https://github.com/thebtf/claude-mnemonic-plus) — a memory/observation system for Claude Code.
Captures observations from Claude conversations, stores them in SQLite with vector search (sqlite-vec),
provides an MCP server (`nia`) and a worker HTTP API. Hooks integrate with Claude Code lifecycle events.
Goal of this fork: extend functionality (the "plus" variant).

## RULES

| Rule | Description |
|------|-------------|
| **No stubs** | Complete, working implementations only |
| **No guessing** | Verify with tools before using |
| **Reasoning first** | Document WHY before implementing |
| **No silent patching** | Report every discrepancy found |
| **No time estimates** | Prioritize by value/risk/dependencies, not phantom duration |

## PRIORITIZATION FRAMEWORK

AI-assisted coding is **10-100x faster** than solo human development. Decisions based on time estimates ("2 weeks") use pre-AI timelines and lead to poor prioritization.

**Decision factors (in order):**
1. **Value**: High/Medium/Low user/business impact
2. **Risk**: Known/Unknown complexity, security concerns
3. **Dependencies**: What blocks what (ordering)
4. **Reversibility**: Can we undo if wrong?
5. **Context**: Relevant files already open?

**Forbidden:** "Quick win", "too much work", "complex so defer"
**Required:** "High value", "Unknown risk", "Blocked by X"

<!-- Quality Gates: See .agent/rules/quality-gates.md -->

## CONVENTIONS

- Language: Go 1.21+
- Build: `make build` (uses goreleaser for releases)
- Test: `go test ./...`
- Database: SQLite via GORM + sqlite-vec extension for vector search
- Embedding: ONNX model (BGE) bundled in `internal/embedding/assets/`
- Reranking: ONNX reranker model in `internal/reranking/assets/`
- MCP server: `cmd/mcp/main.go` → serves as `nia` MCP tool
- Worker: `cmd/worker/main.go` → HTTP API + SSE
- Hooks: `cmd/hooks/` → Claude Code lifecycle hooks (session-start, post-tool-use, etc.)
- Plugin: `plugin/` → Claude Code plugin definition

## INSTRUCTION HIERARCHY

When multiple instructions conflict, priority order:

```
System prompts > Task/delegation > Global rules > Project rules > Defaults
```

**Key principle:** Task-specific prompts can override general rules by design.

## SKILL LOADING

1. Project skills (`.agent/skills/`) override global skills
2. Same-name project skill completely replaces global
3. Skills are loaded by semantic description matching
