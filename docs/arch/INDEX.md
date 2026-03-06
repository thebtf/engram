# Architecture Documentation Index

## Project Summary

`engram` is a Go plugin for Claude Code that gives it persistent memory backed by PostgreSQL + pgvector. It runs as three binaries (worker daemon, MCP stdio server, MCP SSE server) plus six Claude Code lifecycle hooks. Memory is stored as typed "observations" with hybrid full-text + vector search, a knowledge graph of relations between observations, and an automated consolidation lifecycle (relevance decay, creative association discovery, forgetting).

This is a fork of [engram](https://github.com/thebtf/engram) that replaced the SQLite/sqlite-vec backend with PostgreSQL and added multi-workstation support, memory consolidation, session indexing, and collections.

## Documents

| Document | Description |
|----------|-------------|
| [OVERVIEW.md](OVERVIEW.md) | System overview, architecture diagram, key design decisions, fork comparison |
| [COMPONENTS.md](COMPONENTS.md) | All binaries and internal packages — purpose, interfaces, startup sequences, interactions |
| [DATA_MODEL.md](DATA_MODEL.md) | All 13 PostgreSQL tables, full schema, ERD, FTS setup, vector embedding dimensions, migration history |
| [API_CONTRACTS.md](API_CONTRACTS.md) | All 37 MCP tools, Worker HTTP endpoints, SSE protocol, hook input/output interfaces |
| [CONFIGURATION.md](CONFIGURATION.md) | All settings.json keys, env-only variables, loading precedence, collections YAML schema |
| [GOTCHAS.md](GOTCHAS.md) | Non-obvious behaviors, operational risks, integration issues — read before deploying |
| [QUICKSTART.md](QUICKSTART.md) | Prerequisites, PostgreSQL setup, build, run, Claude Code integration, troubleshooting |

## Quick Navigation

- **First time?** Start with [OVERVIEW.md](OVERVIEW.md) then [QUICKSTART.md](QUICKSTART.md).
- **Debugging search behavior?** See [API_CONTRACTS.md](API_CONTRACTS.md) and [GOTCHAS.md](GOTCHAS.md#bm25-short-circuit-can-skip-vector-search).
- **Changing embedding provider?** See [GOTCHAS.md](GOTCHAS.md#critical-embedding-dimension-mismatch) first.
- **Multi-workstation setup?** See [QUICKSTART.md](QUICKSTART.md#multi-workstation-setup).
- **Understanding the DB schema?** See [DATA_MODEL.md](DATA_MODEL.md).
- **Adding a new config option?** See [CONFIGURATION.md](CONFIGURATION.md) for the loading pattern.
