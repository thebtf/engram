---
name: memory
description: Use when you want to explicitly store or recall knowledge across sessions. Prefer engram store_memory/recall_memory over file-based memory for decisions, patterns, insights, and cross-project knowledge. Also use when searching observations from previous coding sessions.
---

# Engram Memory

## Overview

Engram is persistent shared memory for Claude Code. Hooks automatically capture observations from your coding sessions. Your job is to **use** that knowledge — search it, build on it, and keep it clean.

**Core principle:** Hooks handle automatic capture. You handle retrieval, explicit storage, and curation. The 50+ MCP tools exist so you can retrieve, connect, store, and maintain knowledge.

## Connection Check

**Do NOT check environment variables.** The MCP server may be configured in different ways (user settings, plugin config, manual). The only reliable test is calling a tool:

```
Tool: check_system_health()
```

- **Success** → Engram is connected. Use the tools below.
- **Failure / tool not found** → Engram MCP is not available in this session. Hooks still collect data, but retrieval tools are unavailable.

## What Hooks Do Automatically

| Hook | Fires When | Captures |
|------|-----------|----------|
| **SessionStart** | Conversation begins | Injects relevant project memories into context |
| **UserPromptSubmit** | User sends a message | Searches for relevant memories and injects them as `<relevant-memory>` context |
| **PostToolUse** | Any tool completes | Captures tool usage patterns and outcomes |
| **SubagentStop** | Subagent finishes | Notifies system of subagent completion, triggers observation processing |
| **Stop** | Session ends | Generates session summary, stores key observations |

Hooks handle automatic capture. Use `store_memory` when you want to explicitly remember something — decisions, patterns, preferences, or insights. Focus on retrieval, explicit storage, and curation.

## When to Use Engram Tools

```
Session starts → context already injected (automatic)
  │
  ├─ Want to remember something? → store_memory
  ├─ Need to recall knowledge?   → recall_memory / search
  ├─ Need past decisions?        → decisions
  ├─ Need recent context?        → get_recent_context / timeline
  ├─ Working on a file?          → find_by_file
  ├─ Exploring a concept?        → find_by_concept / how_it_works
  ├─ Found duplicate memories?   → suggest_consolidations / merge
  ├─ Memory quality declining?   → get_data_quality_report / trigger_maintenance
  └─ Session ending?             → (automatic via Stop hook)
```

## Credential Management

Engram provides encrypted credential storage via AES-256-GCM vault. Credentials are stored per-project or globally, encrypted at rest, and never appear in search results or context injection.

| Tool | Purpose |
|------|---------|
| `store_credential` | Encrypt and store an API key, password, or token |
| `get_credential` | Retrieve and decrypt a credential by name |
| `list_credentials` | List credential names and metadata (no values) |
| `delete_credential` | Delete a credential by name (scope-aware) |
| `vault_status` | Check encryption status, key source, fingerprint, credential count |

```
Use: "Store my OpenAI API key"
Tool: store_credential(name="openai_api_key", value="sk-...", scope="global")

Use: "Get my OpenAI key"
Tool: get_credential(name="openai_api_key")

Use: "Check vault status"
Tool: vault_status()
```

**Key source priority:** `ENGRAM_ENCRYPTION_KEY` env var > `ENGRAM_ENCRYPTION_KEY_FILE` > auto-generated `vault.key`. Auto-generated keys are saved to `DataDir()/vault.key` — back up this file to avoid losing access to stored credentials.

## Top 12 Tools (90% of Value)

### 1. `store_memory` — Explicitly remember something

Create a memory on demand — decisions, patterns, preferences, insights. Supports hierarchical tags, scope control, and automatic dedup.

```
Use: "Remember that we chose Redis over Memcached for caching"
Tool: store_memory(content="Chose Redis over Memcached for caching layer due to persistence and pub/sub support", title="Redis caching decision", type="decision", tags=["caching", "infrastructure"])
```

### 2. `recall_memory` — Retrieve stored knowledge

Semantic search across all memories with flexible output formats.

```
Use: "What do we know about our caching choices?"
Tool: recall_memory(query="caching strategy decisions", format="text")
```

### 3. `search` — Hybrid semantic + full-text search

The primary retrieval tool. Combines vector similarity, full-text search, and BM25 scoring.

```
Use: "What do we know about authentication in this project?"
Tool: search(query="authentication implementation decisions")
```

### 4. `decisions` — Find architecture and design decisions

Filters for decision-type observations. Use before making architectural choices.

```
Use: "What was decided about the caching strategy?"
Tool: decisions(query="caching strategy")
```

### 5. `timeline` — Browse observations anchored in time

Navigate observations around a specific point or filter by project, type, and concepts.

```
Use: "What happened recently in this project?"
Tool: timeline(query="recent changes", project="my-project")
```

### 6. `find_by_file` — Observations related to a specific file

Before modifying a file, check what's known about it.

```
Use: "What's been noted about server.go?"
Tool: find_by_file(files="internal/mcp/server.go")
```

### 7. `find_by_concept` — Search by concept tags

Observations are auto-tagged with concepts. Search by tag for focused results.

```
Use: "Everything related to 'vector-search'"
Tool: find_by_concept(concepts="vector-search")
```

### 8. `how_it_works` — System understanding queries

Retrieves explanatory observations about how systems work.

```
Use: "How does the consolidation scheduler work?"
Tool: how_it_works(query="consolidation scheduler")
```

### 9. `find_related_observations` — Relation-based retrieval

Follow knowledge graph relations (causes, fixes, explains, contradicts).

```
Use: "What's connected to this bug fix?"
Tool: find_related_observations(id=42)
```

For deeper graph traversal with configurable depth, use `get_observation_relationships(id=42, max_depth=2)`.

### 10. `get_recent_context` — Latest project observations

Quick dump of the most recent observations for a project.

```
Use: "Catch me up on what happened recently"
Tool: get_recent_context(project="my-project", limit=20)
```

### 11. `get_patterns` — Detected recurring patterns

Surfaces patterns the system has identified across observations.

```
Use: "Are there recurring issues or patterns?"
Tool: get_patterns(project="my-project")
```

### 12. `search_sessions` — Full-text search across session logs

Search through indexed Claude Code session transcripts.

```
Use: "When did we discuss the migration plan?"
Tool: search_sessions(query="migration plan", limit=5)
```

## Workflow by Phase

### Starting Work

1. Context is auto-injected by SessionStart hook
2. If you need more: `recall_memory`, `search`, or `get_recent_context`
3. Before architectural decisions: `decisions` to check prior choices
4. To explicitly store knowledge: `store_memory` with title, tags, and scope

### During Active Coding

- Before modifying unfamiliar code: `find_by_file` + `how_it_works`
- When encountering a concept: `find_by_concept`
- When debugging: `find_related_observations` to trace cause chains
- When stuck: `search` with different query angles

### Maintaining Memory Quality

Use these periodically or when memory feels noisy:

| Tool | Purpose |
|------|---------|
| `suggest_consolidations` | Find observations that should be merged |
| `merge_observations` | Combine duplicates into one |
| `bulk_mark_superseded` | Mark outdated observations |
| `get_data_quality_report` | Overall quality metrics |
| `trigger_maintenance` | Run cleanup tasks |
| `run_consolidation` | Trigger full consolidation cycle (decay + associations) |

### Analytics (When Needed)

| Tool | Purpose |
|------|---------|
| `get_memory_stats` | System overview: counts, storage, health |
| `get_temporal_trends` | Activity patterns over time |
| `analyze_observation_importance` | Which observations matter most |
| `check_system_health` | Is the system performing well |

## Other Tools (Quick Reference)

These tools cover specialized use cases beyond the top 10:

| Tool | Purpose |
|------|---------|
| `changes` | Find code modification observations |
| `find_by_type` | Filter by observation type (decision, bugfix, feature, etc.) |
| `find_similar_observations` | Pure vector similarity search |
| `get_context_timeline` | Context organized by time periods |
| `get_timeline_by_query` | Query-filtered timeline view |
| `get_observation` | Fetch a single observation by ID |
| `edit_observation` | Modify observation fields |
| `tag_observation` | Add/remove concept tags |
| `get_observations_by_tag` | List observations with a specific tag |
| `get_observation_quality` | Quality score for one observation |
| `get_observation_relationships` | Graph traversal with configurable depth |
| `get_observation_scoring_breakdown` | Debug scoring formula |
| `batch_tag_by_pattern` | Auto-tag observations matching a pattern |
| `bulk_delete_observations` | Batch delete |
| `bulk_boost_observations` | Boost importance scores in bulk |
| `explain_search_ranking` | Debug why search ranked results a certain way |
| `analyze_search_patterns` | Search usage analytics |
| `export_observations` | Export observations as JSON |
| `get_maintenance_stats` | Maintenance cycle statistics |
| `list_sessions` | List indexed sessions with filtering |

## Engram vs Claude Code File Memory

Claude Code has a built-in file-based memory system (`~/.claude/projects/.../memory/`). Engram is different:

| | Engram (`store_memory`) | Claude Code (file memory) |
|---|---|---|
| **Storage** | PostgreSQL + pgvector (server) | Markdown files (local) |
| **Search** | Semantic + full-text + BM25 hybrid | Loaded into context at session start |
| **Cross-project** | Yes — global scope observations visible everywhere | No — per-project only |
| **Cross-machine** | Yes — shared server | No — local files |
| **Best for** | Decisions, patterns, insights, anything you'd want to find via search | User preferences, project config, behavioral instructions |

**Rule of thumb:** If you'd search for it later → `store_memory`. If it's a static instruction → file memory.

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Checking ENGRAM_URL / ENGRAM_API_TOKEN env vars | Do NOT check env vars. Call `check_system_health()` — if it works, Engram is connected regardless of config method |
| Not using `store_memory` for important insights | Use `store_memory` for decisions, patterns, and preferences you want to persist. Hooks capture automatically, but explicit memories are higher quality |
| Ignoring injected context | Read `<engram-context>` (session start) and `<relevant-memory>` (per prompt) blocks — they contain prior knowledge |
| Not searching before re-exploring code | `search` first — someone (maybe past you) already documented it |
| Never running maintenance | Periodically use `trigger_maintenance` or `run_consolidation` |
| Using only `search` for everything | Use specialized tools: `decisions` for architecture, `find_by_file` for code, `timeline` for history |
