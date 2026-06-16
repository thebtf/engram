---
name: memory
description: Use when you want to explicitly store or recall knowledge across sessions. Prefer engram store/recall over file-based memory for decisions, patterns, insights, and cross-project knowledge. Also use when searching memories from previous coding sessions.
---

# Engram Memory

## Overview

Engram is persistent shared memory for Claude Code. Hooks automatically capture observations from your coding sessions. Your job is to **use** that knowledge — search it, build on it, and keep it clean.

**Core principle:** Hooks handle automatic capture. You handle retrieval, explicit storage, and curation.

## Connection Check

**Do NOT check environment variables.** The MCP server may be configured in different ways. The only reliable test is calling a tool:

```
Tool: check_system_health()
```

- **Success** → Engram is connected. Use the tools below.
- **Failure / tool not found** → Engram MCP is not available in this session.

## What Hooks Do Automatically

| Hook | Fires When | Captures |
|------|-----------|----------|
| **SessionStart** | Conversation begins | Injects relevant project memories into context |
| **UserPromptSubmit** | User sends a message | Searches for relevant memories and injects them as `<relevant-memory>` context |
| **PostToolUse** | Any tool completes | Captures tool usage patterns and outcomes |
| **SubagentStop** | Subagent finishes | Notifies system of subagent completion |
| **Stop** | Session ends | Generates session summary, stores key observations |

## Mandatory Workflow

**BEFORE every task:**
1. `recall(query="...")` — check what is already known about this topic.
2. `recall(query="file:path/to/file")` — before modifying any file.
3. `recall(query="prior decisions about ...")` — before architectural decisions.

**AFTER every task:**
4. `store(content="...", title="...", tags="...")` — save decisions, discoveries, patterns, lessons learned.
5. `feedback(action="rate", id=N, rating="useful")` — rate memories you used.
6. `feedback(action="outcome", session_id="<claude-session-id>", outcome="success")` — record session outcome.

**Steps 4-6 are NOT optional.** Every completed task produces knowledge. Store it or it is lost.

## 8 Tools

| Tool | Purpose | Key Actions |
|------|---------|-------------|
| `recall` | **Search & retrieve** memories | search (default) |
| `store` | **Save** memories, edit, merge, import | create (default), edit, merge, import |
| `feedback` | **Rate** quality, suppress, record outcomes | rate, suppress, outcome |
| `issues` | **Cross-project issue tracking** between agents | create, list, get, update, comment, reopen, close |
| `vault` | **Credentials** — encrypted AES-256-GCM | store, get, list, delete, status |
| `docs` | **Documents** — versioned docs & collections | create, read, list, history, comment, collections, documents, get_doc, remove, ingest, search_docs |
| `admin` | **Bulk ops**, analytics | bulk_delete, bulk_supersede, tag, stats, trends, quality, export, ... |
| `check_system_health` | **Health** check of all subsystems | (no params) |

## Credential Management

Engram provides encrypted credential storage via AES-256-GCM vault. Credentials are stored per-project or globally, encrypted at rest, and never appear in search results or context injection.

```
Use: "Store my OpenAI API key"
Tool: vault(action="store", name="openai_api_key", value="sk-...", scope="global")

Use: "Get my OpenAI key"
Tool: vault(action="get", name="openai_api_key")

Use: "Check vault status"
Tool: vault(action="status")
```

**Key source priority:** `ENGRAM_ENCRYPTION_KEY` env var > `ENGRAM_ENCRYPTION_KEY_FILE` > auto-generated `vault.key`.

## Recall Tool

The `recall` tool searches memories using SQL filtering. The `search` action (default) filters by project and optionally applies a substring match on content.

```
Use: "What do we know about our caching choices?"
Tool: recall(query="caching strategy", project="my-project")

Use: "What authentication decisions were made?"
Tool: recall(query="authentication decisions", project="my-project", limit=10)
```

**Note:** `find_by_file` (the legacy file-scoped lookup) was removed in v5. Use `recall(query="file:path/to/file")` to find file-related memories by including the file path in your query.

## Store Tool

The `store` tool creates and manages memories.

```
Use: "Remember that we chose Redis over Memcached for caching"
Tool: store(content="Chose Redis over Memcached for caching layer due to persistence and pub/sub support", title="Redis caching decision", type="decision", tags="caching,infrastructure")

Use: "Edit a memory"
Tool: store(action="edit", id=42, content="Updated content", narrative="Why this changed")

Use: "Import from file"
Tool: store(action="import", path="/path/to/observations.json")
```

**Types:** decision, bugfix, feature, refactor, discovery, change, guidance, credential, entity, wiki, pitfall, operational, timeline.

## Issues — Cross-Project Agent Coordination

The `issues` tool tracks bugs, feature requests, and tasks between agents across projects.
**Do NOT use `store` or `docs` for issues.** Issues have lifecycle management — status, priority, comments, auto-injection.

| Action | Usage |
|--------|-------|
| **Create** | `issues(action="create", title="...", body="...", priority="high", target_project="other-project")` |
| **List** | `issues(action="list", project="<your-project>", status="open,acknowledged,reopened")` |
| **Comment** | `issues(action="comment", id=N, body="...")` |
| **Resolve** | `issues(action="update", id=N, status="resolved", body="Fixed in commit abc123")` |
| **Reopen** | `issues(action="reopen", id=N, body="Still broken after fix")` |

**When to use issues vs store:**
- Bug in the CURRENT project → `store(type="discovery")` — it's knowledge about a fix
- Bug/task for ANOTHER project → `issues(action="create")` — it's an actionable work item

## Compatibility Tools (Legacy Aliases)

31 legacy aliases remain registered for backward compatibility. Each maps to a primary tool action. Prefer the 8 primary tools for new code.

**Memory:** `store_memory`, `recall_memory`, `rate_memory`, `suppress_memory`, `find_by_file` (tombstone — returns error), `find_similar_observations`, `get_memory_stats`, `set_session_outcome`, `import_instincts`, `backfill_status`

**Sessions:** `search_sessions`, `list_sessions`

**Credentials:** `store_credential`, `get_credential`, `list_credentials`, `delete_credential`, `vault_status`

**Documents:** `list_collections`, `list_documents`, `get_document`, `remove_document`, `ingest_document`, `search_collection`

**Versioned Documents:** `doc_create`, `doc_read`, `doc_list`, `doc_history`, `doc_comment`

**Rules:** `store_rule`, `list_rules` (conditional)

**System:** `check_system_health` (also a primary tool)

## Workflow Patterns

**Starting work:** Context is auto-injected by hooks. Use `recall(query="...")` for deeper search.
**Before modifying code:** `recall(query="file:path/to/file")` to find file-related memories.
**After completing a feature:** `store(content="...", title="...", type="decision")` — capture what was built and why.
**After fixing a bug:** `store(content="...", title="...", type="discovery")` — capture root cause and fix.
**After research:** `store(content="...", title="...", type="discovery")` — capture findings.
**Found a bug in another project:** `issues(action="create", title="...", target_project="...", priority="high")` — NOT store.
**Secrets:** `vault(action="store")` for API keys. Never store secrets in observations.

## Engram vs Claude Code File Memory

| | Engram (`store`) | Claude Code (file memory) |
|---|---|---|
| **Storage** | PostgreSQL (server) | Markdown files (local) |
| **Search** | SQL + substring filter | Loaded into context at session start |
| **Cross-project** | Yes — global scope visible everywhere | No — per-project only |
| **Cross-machine** | Yes — shared server | No — local files |
| **Best for** | Decisions, patterns, insights, anything searchable | User preferences, project config, behavioral instructions |

**Rule of thumb:** If you'd search for it later → `store`. If it's a static instruction → file memory.

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Checking ENGRAM_URL / env vars | Call `check_system_health()` instead |
| Not storing important insights | Use `store` for decisions, patterns, and preferences |
| Ignoring injected context | Read `<engram-context>` and `<relevant-memory>` blocks |
| Not searching before re-exploring | `recall(query="...")` first |
| Using `recall(action="by_file", ...)` | That action was removed in v5; use `recall(query="file:...")` |
| Using `store` for cross-project bugs | Use `issues(action="create")` — it has lifecycle and auto-injection |
