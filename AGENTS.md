# AGENTS.md

## STACKS

```yaml
STACKS: [GO]
```

## PROJECT OVERVIEW

Persistent shared memory infrastructure for Claude Code workstations.
Single server (Docker on Unraid/NAS) stores observations in PostgreSQL 17 + pgvector,
exposes 48 MCP tools via Streamable HTTP / SSE on port 37777.

## RULES

| Rule | Description |
|------|-------------|
| **No stubs** | Complete, working implementations only |
| **No guessing** | Verify with tools before using |
| **Reasoning first** | Document WHY before implementing |
| **No silent patching** | Report every discrepancy found |
| **No time estimates** | Prioritize by value/risk/dependencies, not phantom duration |

## CONVENTIONS

- Language: Go 1.25+
- Build: `make build`
- Test: `go test ./...`
- Database: PostgreSQL 17 + pgvector (HNSW cosine index)
- Embedding: OpenAI-compatible REST API
- Reranking: API-based cross-encoder reranker
- Worker: `cmd/worker/main.go` — HTTP API + MCP SSE + MCP Streamable HTTP + dashboard
- Hooks: `plugin/hooks/` — JavaScript hooks for Claude Code lifecycle
- Plugin: `plugin/` — Claude Code plugin definition + marketplace

## KEY DIRECTORIES

```
cmd/worker/          — server entry point
internal/mcp/        — MCP protocol, 48 tool handlers (tools_*.go)
internal/search/     — hybrid search (tsvector + vector + BM25, RRF fusion)
internal/scoring/    — importance + relevance scoring
internal/embedding/  — OpenAI-compatible REST embedding provider
internal/worker/sdk/ — observation extraction (LLM API or Claude CLI)
internal/learning/   — self-learning, LLM client
plugin/hooks/        — JS hooks (session-start, user-prompt, post-tool-use, stop)
```

## INSTRUCTION HIERARCHY

```
System prompts > Task/delegation > Global rules > Project rules > Defaults
```

## SKILL LOADING

1. Project skills (`.agent/skills/`) override global skills
2. Same-name project skill completely replaces global
3. Skills are loaded by semantic description matching
