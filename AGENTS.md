# AGENTS.md

## STACKS

```yaml
STACKS: [GO]
```

## PROJECT OVERVIEW

Persistent shared memory infrastructure for Claude Code workstations.
Single server (Docker on Unraid/NAS) stores memories, behavioral rules, credentials,
issues, and documents in PostgreSQL 17. MCP tools are exposed via the `engram` stdio
client proxy (server-side HTTP MCP transports removed in v5); REST API + gRPC on
port 37777 (cmux multiplexed).

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
- Database: PostgreSQL 17
- Server: `cmd/engram-server/main.go` — HTTP API + gRPC + dashboard on :37777 (cmux)
- Client: `cmd/engram/main.go` — stdio MCP proxy with git-derived project identity
- Hooks: `plugin/hooks/` — JavaScript hooks for Claude Code lifecycle
- Plugin: `plugin/` — Claude Code plugin definition + marketplace

## KEY DIRECTORIES

```
cmd/engram-server/   — server entry point
cmd/engram/          — local client (stdio MCP proxy)
internal/mcp/        — MCP protocol, tool handlers (tools_*.go)
internal/grpcserver/ — gRPC service implementations
internal/worker/     — HTTP handlers, retrieval, session management
internal/db/gorm/    — GORM models + stores (memories, behavioral_rules, credentials, issues, documents)
internal/crypto/     — AES-256-GCM vault for credential encryption
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
