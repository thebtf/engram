# Feature: OpenClaw + Engram Integration (v2)

**Slug:** openclaw-engram-plugin
**Created:** 2026-03-24
**Status:** Research Phase (FR-4 memory interface done; FR-1 partial; FR-2/FR-3 not started)
**Author:** AI Agent (reviewed by user)

## Overview

Redesign the engram plugin for OpenClaw from "just connected" to "actually useful." v1 solved connectivity (hooks fire, observations store). v2 must solve: what value does engram provide to an always-on personal AI assistant that lives across 20+ messaging channels?

## Context

### What is OpenClaw
A **personal AI assistant** running on user's devices. Key properties:
- **Multi-channel**: WhatsApp, Telegram, Slack, Discord, Google Chat, Signal, iMessage, IRC, Matrix, etc.
- **Always-on**: not session-based like Claude Code — continuous conversation across channels
- **Agent workspace**: `~/.openclaw/workspace/` with Markdown files as memory
- **Plugin system**: memory, channels, tools, skills — npm-distributed
- **Memory slot**: only ONE memory plugin active at a time (memory-core default)

### How OpenClaw memory works today (memory-core)
- **Storage**: plain Markdown files in workspace (`memory/YYYY-MM-DD.md`, `MEMORY.md`)
- **Search**: `memory_search` tool — semantic recall over indexed Markdown chunks (sqlite-vec or QMD)
- **Write**: `memory_get` + direct file writes by agent
- **Embedding**: builtin (sqlite-vec) or QMD backend with various providers
- **Auto-flush**: before compaction, agent is prompted to write durable memories
- **106 source files** in `src/memory/` — substantial codebase

### What engram plugin does today (v1)
- Hooks into OpenClaw's session lifecycle (session-start, user-prompt, stop)
- Sends prompts/observations to engram server via HTTP
- Context injection: `<engram-context>` block in user-prompt hook
- **Problems observed:**
  - Heartbeat messages stored as prompts (now filtered)
  - Telegram metadata stored as prompts (now filtered)
  - "Active Sessions = 0" despite active sessions
  - Empty sessions (0 messages) from heartbeat
  - No understanding of OpenClaw's continuous conversation model

### Fundamental architecture mismatch
Claude Code: **session-based** (start → work → stop → new session)
OpenClaw: **continuous** (always-on, messages arrive anytime across channels)

Engram was built for Claude Code sessions. OpenClaw doesn't have "sessions" in the same way — it has **conversations** that may last days/weeks with intermittent messages.

## Research Questions (must answer before FR)

### RQ-1: What is the "unit of work" in OpenClaw?
In Claude Code: session (start→stop). In OpenClaw: ???
- One Telegram message? One conversation thread? One task? One day?
- How does openclaw-engram decide "this conversation produced a decision worth remembering"?

### RQ-2: What can engram do that memory-core can't?
memory-core: local Markdown + sqlite-vec embeddings
engram: PostgreSQL + pgvector + graph + composite scoring + cross-project + behavioral rules

Potential unique value:
- **Cross-channel memory**: Telegram conversation informs Slack response (memory-core is per-workspace)
- **Semantic search quality**: pgvector + 4096-dim + composite scoring vs sqlite-vec
- **Behavioral rules**: user preferences extracted and injected (memory-core doesn't have this)
- **Knowledge graph**: relations between facts (memory-core doesn't have this)
- **Multi-agent memory**: multiple OpenClaw instances share one engram server

### RQ-3: Should engram REPLACE memory-core or COMPLEMENT it?
Option A: Replace — engram IS the memory plugin (implements MemoryProvider interface)
Option B: Complement — engram runs alongside memory-core (additional context via hooks)
Option C: Hybrid — engram provides search + cross-channel, memory-core provides local write

### RQ-4: What does "useful" look like for an always-on assistant?
- "Remember I told you on Telegram about X" → agent recalls from engram across channels
- "My preference is Y" → stored, injected in ALL future conversations
- "What did we discuss last week about Z?" → semantic search over all channels
- "Don't do X in this context" → behavioral rule, applied everywhere

### RQ-5: How does OpenClaw's hook system work?
Need to audit: what hooks exist, when they fire, what ctx contains.
This determines what engram can capture and when.

## Functional Requirements (preliminary — pending research)

### FR-1: Message Classification
The hook must classify incoming messages BEFORE processing:
- **User prompt** (real human message) → full engram pipeline
- **Heartbeat** (system polling) → skip entirely
- **Agent-to-agent** (Telegram metadata, inter-agent) → skip or minimal
- **Automated trigger** (cron, webhook) → context needed, not a "prompt"

Classification should be based on `ctx` metadata, not content regex.

### FR-2: Conversation-Aware Context Injection
Instead of session-based injection (Claude Code model), use conversation-aware injection:
- Track conversation threads across channels
- Inject relevant history from OTHER channels into current conversation
- "I mentioned on Telegram..." → engram finds it and injects

### FR-3: Behavioral Rules Across Channels
USER_BEHAVIOR rules extracted from any channel apply to ALL channels:
- "Don't forward my messages" (said on Telegram) → respected on Slack
- "Always summarize in Russian" → applied everywhere

### FR-4: OpenClaw Memory Provider Interface
If replacing memory-core (RQ-3 = Option A), engram must implement:
- `memory_search(query)` → semantic search
- `memory_get(path, startLine, endLine)` → targeted read (how?)
- Status reporting (files, chunks, backend info)

## Edge Cases
- Message arrives while no LLM backend available (offline mode)
- Same message forwarded to multiple channels (dedup)
- Group chat vs DM — different privacy expectations
- Long-running conversation (weeks) — what to extract, when?

## Out of Scope
- Implementing new OpenClaw channels
- Modifying OpenClaw core code
- Building a custom OpenClaw UI

## Dependencies
- Understanding of OpenClaw hook system (RQ-5)
- Decision on replace vs complement (RQ-3)
- OpenClaw docs: `docs/tools/plugin.md`, `docs/concepts/memory.md`

## Research Findings (from Tavily + code analysis)

### RF-1: Plugin slot system is EXCLUSIVE
`plugins.slots.memory` allows only ONE `kind: "memory"` plugin. If engram declares `kind: "memory"`, it replaces memory-core entirely. This is BY DESIGN — OpenClaw's architecture.

### RF-2: Issue #38874 — community wants supplementary memory
Users want graph/knowledge plugins alongside vector memory. Current workaround: declare `kind: "knowledge-graph"` (not "memory") to avoid slot conflict. But this loses auto-recall/auto-capture lifecycle that memory plugins get for free.

### RF-3: `kind: "context-engine"` exists
`PluginKind = "memory" | "context-engine"` — context engines own compaction and context injection. This might be the right slot for engram — it's about CONTEXT, not just memory.

### RF-4: Memory provider hooks
Memory plugins implement: `memory_search(query)`, `memory_get(path, startLine, endLine)`. memory-core uses Markdown files + sqlite-vec/QMD embeddings. Engram would need to implement these interfaces OR use a different plugin slot.

### RF-5: Provider hook system has 21+ hooks
Plugin architecture has extensive provider hooks (catalog, resolveDynamicModel, prepareDynamicModel, etc). Memory/context hooks are separate — need deeper audit.

## Decision: RQ-3 — FULL REPLACEMENT is feasible

**Option A (Replace) IS viable.** Engram can fully replace memory-core:

| memory-core interface | Engram equivalent | Gap? |
|----------------------|-------------------|------|
| `memory_search(query)` | `search` API | No |
| `memory_get(path, from, lines)` | Observations by date → virtual file | Small — need adapter |
| Auto-capture (write daily MD) | Hooks + LLM extraction | No |
| Auto-recall (inject at start) | user-prompt hook injection | No |
| `store_memory(content)` | `store_memory` MCP tool | No |
| Memory flush (pre-compaction) | stop hook | No |
| Status reporting | `check_system_health` | Need adapter |
| Embeddings | pgvector 4096-dim | Better than sqlite-vec |

**`memory_get` gap is minor:** Either emit observations grouped by date as virtual Markdown, or deprecate `memory_get` entirely (agents use `memory_search` for everything, `memory_get` is legacy convenience).

**Advantages of full replacement:**
- Cross-channel memory (Telegram→Slack recall)
- Behavioral rules injection
- Composite scoring (recency × importance × type)
- Knowledge graph
- Shared across multiple OpenClaw instances
- pgvector > sqlite-vec for semantic search quality

**Option C (Hybrid) also remains viable** if we don't want to own the memory slot lifecycle.

## Open Questions
- [NEEDS CLARIFICATION] RQ-1: Unit of work in OpenClaw (session vs conversation vs message)
- [NEEDS CLARIFICATION] RQ-5: Hook system audit for memory/context lifecycle hooks
- [RESOLVED] RQ-3: Hybrid approach — engram as context-engine, not memory replacement

## Next Steps
1. Deep-dive into OpenClaw plugin SDK (`src/plugin-sdk/`)
2. Audit OpenClaw hook/event system
3. Study memory-core implementation (`extensions/memory-core/index.ts`)
4. Study existing openclaw-engram plugin in engram repo (`plugin/openclaw-engram/`)
5. Answer RQ-1 through RQ-5
6. Then: `/speckit-clarify` → `/speckit-plan`
