# AGENTS.md

## STACKS

```yaml
STACKS: [GO]
```

## PROJECT OVERVIEW

Persistent shared memory infrastructure for Claude Code workstations.
Single server (Docker on Unraid/NAS) stores memories, behavioral rules, credentials,
issues, and documents in PostgreSQL 17. MCP tools are exposed via the `engram` stdio
client proxy (server-side HTTP MCP transports removed in v5 — permanent architectural shift to the
stdio daemon + gRPC model); REST API + gRPC on port 37777 (cmux multiplexed).

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

## CODE INTELLIGENCE (SocratiCode replacement, v6.13.0+)

Set `ENGRAM_CODE_INTEL_ENABLED=true` to activate three drop-in SocratiCode replacement tools:

| Tool | Side | Description |
|------|------|-------------|
| `codebase_index` | daemon | Triggers async code index for the current project root. Returns `{status:"started",run_id}` immediately. |
| `codebase_status` | daemon+server | Reports index liveness, chunk counts, and last-indexed timestamp. |
| `codebase_search` | server | Hybrid FTS+vector search over indexed code chunks. V1: FTS-only (no embedding required). |

Flag-off: all three tools are absent from `tools/list` (byte-identical to pre-v6.13.0 surface).

Key directories:
- `internal/handlers/codeintel/` — daemon-side module (codebase_index, codebase_status liveness)
- `internal/mcp/tools_code_intel.go` — server-side codebase_search + codebase_status counts
- `internal/db/gorm/code_chunk_store.go` — CountEmbeddedByProject, MaxUpdatedAtByProject

## INSTRUCTION HIERARCHY

```
System prompts > Task/delegation > Global rules > Project rules > Defaults
```

## SKILL LOADING

1. Project skills (`.agent/skills/`) override global skills
2. Same-name project skill completely replaces global
3. Skills are loaded by semantic description matching
