# Engram Re-Genesis: Architecture Decomposition

## 1. Key Problem (Decomposition)

Claude Code sessions are ephemeral: each conversation starts with zero context about what happened in previous sessions. An agent working on a codebase today has no memory of the architectural decisions, bug fixes, discovered patterns, or gotchas from yesterday's session. This forces repeated codebase exploration, redundant discoveries, and lost institutional knowledge — effectively making every session a "first day on the job."

The core problem is **automated knowledge extraction and cross-session retrieval**: intercept meaningful events during a coding session (file edits, command results, architectural decisions), distill them into structured observations via LLM analysis, store them durably, and inject relevant observations into future sessions — all without manual effort from the user.

The original assumption was that this requires an LLM-in-the-loop pipeline: raw tool events (Edit file X, Bash output Y) are meaningless data until an LLM classifies them. **This assumption is wrong.** ~80% of classification is rule-based, ~70% of concept tagging is keyword extraction, and semantic embedding (BGE/OpenAI) makes raw events searchable without any LLM processing. The consumer of observations is Claude (an LLM) — it can interpret raw context directly. LLM enrichment improves quality (adds narrative, refined facts) but is not a functional requirement. The system must work at Level 0 (deterministic + embedding, no LLM) and optionally upgrade to Level 1 (batch LLM enrichment) when Claude CLI is available.

The system must operate as a transparent background process: hooks capture events automatically, extraction happens asynchronously, and relevant memories surface at session start and per-prompt via semantic search. The user never manually "saves" or "tags" anything — the system observes, distills, remembers, and recalls.

**Hard constraint**: LLM calls MUST go through Claude CLI (free with subscription). Anthropic API is separately billed and therefore unacceptable.

## 2. Current Implementation (As-If-From-Scratch Spec)

### Components

```
┌─────────────────────────────────────────────────────────┐
│                    CLIENT (Windows)                      │
│                                                          │
│  Claude Code ──hooks──► Hook Binaries (Go)               │
│    session-start    → GET /api/context/inject             │
│    user-prompt      → GET /api/context/search             │
│                       POST /api/sessions/init             │
│    post-tool-use    → POST /api/sessions/observations     │
│    subagent-stop    → POST /api/sessions/subagent-complete│
│    stop             → POST /sessions/{id}/summarize       │
│                                                          │
│  All HTTP calls go to ENGRAM_WORKER_HOST:37777            │
└──────────────────────────┬──────────────────────────────┘
                           │ HTTP
                           ▼
┌─────────────────────────────────────────────────────────┐
│                   SERVER (Docker/Linux)                   │
│                                                          │
│  engram-server (single binary, port 37777)               │
│    ├── Worker HTTP API (session mgmt, observation store)  │
│    ├── MCP SSE Transport (GET /sse, POST /message)        │
│    ├── MCP Streamable HTTP (POST /mcp)                    │
│    ├── Session Manager (in-memory queue)                  │
│    └── SDK Processor ← REQUIRES `claude` CLI binary!     │
│            │                                              │
│            ├── callClaudeCLI("claude --print -p <prompt>")│
│            │      ↓ LLM extracts observations             │
│            ├── StoreObservation → PostgreSQL               │
│            └── SyncVector → pgvector                      │
│                                                          │
│  PostgreSQL + pgvector (observations, vectors, sessions)  │
│  ONNX BGE embeddings (builtin) or OpenAI REST             │
│  Hybrid search: vector + FTS + BM25 (RRF fusion)          │
└─────────────────────────────────────────────────────────┘
```

### Data Flow

1. **Capture**: Claude Code fires post-tool-use hook → hook binary sends raw `{tool_name, tool_input, tool_response}` to server
2. **Queue**: Server receives, finds/creates session, queues observation in memory
3. **Process**: Background goroutine drains queue every 2s → SDK Processor calls `claude --print --model haiku` with system prompt + raw data → LLM returns structured `<observation>` XML
4. **Store**: Parsed observations → PostgreSQL (with embedding → pgvector)
5. **Retrieve**: Next session's hooks call `/api/context/inject` and `/api/context/search` → server performs hybrid search → returns relevant observations as injected context

### Processing Pipeline Detail

```
Raw tool event
  → Hook filter (skip 14 tools: Task, TaskOutput, Glob, ListDir, LS,
    KillShell, AskUserQuestion, EnterPlanMode, ExitPlanMode, Skill,
    SlashCommand, Read, Grep, WebSearch)
  → HTTP POST to server
  → Server filter (skip 12 tools: TodoWrite, Task, TaskOutput, Glob,
    ListDir, LS, KillShell, AskUserQuestion, EnterPlanMode,
    ExitPlanMode, Skill, SlashCommand)
    NOTE: Server does NOT skip Read/Grep/Bash — handles them via
    heuristic filtering in shouldSkipTrivialOperation()
  → Trivial filter (output <50 chars, errors, lock files, configs,
    boring Read targets, git-status-only Bash)
  → Deduplication (hash-based, TTL window)
  → Circuit breaker check
  → Semaphore (max concurrent CLI calls)
  → claude --print --model haiku -p "<system_prompt>\n<raw_data>"
  → Parse <observation> XML from response
  → Similarity dedup against recent observations (threshold 0.4)
  → StoreObservation (PostgreSQL)
  → SyncVector (pgvector, non-blocking)
```

~10-30 observations survive all filters per session. Each requires one LLM call (~10-30s).

### Summary Pipeline

On session stop: hook sends POST /sessions/{id}/summarize → server calls `claude --print` with transcript context → stores session summary → deletes in-memory session.

## 3. Pros, Cons, and Blockers

### Pros (What Works Well)

- **Single port architecture**: Worker + MCP SSE + MCP Streamable HTTP all on port 37777. Simple deployment, single Docker container.
- **Multi-layer filtering**: 3 filter stages effectively reduce ~100+ tool events to ~10-30 meaningful ones. No wasted LLM calls.
- **Hybrid search**: Vector + FTS + BM25 with RRF fusion provides high-quality retrieval.
- **Hook-based capture**: Transparent, no user effort. Claude Code's hook system provides structured events automatically.
- **Session isolation**: Workstation-aware sessions prevent cross-machine contamination.
- **Deduplication**: Hash + similarity prevent storing redundant observations.
- **MCP surface is read-only**: MCP tools only search/retrieve/manage. Observation creation is exclusively hooks → HTTP → processor. Clean separation.
- **Import endpoint exists**: `POST /api/observations/import` stores pre-structured observations directly, bypassing the SDK Processor entirely.

### Cons (What's Wrong)

- **Fatal dependency on Claude CLI in Docker**: The SDK Processor calls `claude --print` as a subprocess. Claude CLI is a Node.js desktop application requiring an Anthropic subscription tied to a user profile. It cannot run inside a headless Docker container — there's no login, no subscription, no Node.js runtime. Dockerfile confirms: only `ca-certificates` and `curl` installed.
- **All intelligence on the wrong side**: The LLM extraction (the ONLY part that creates value) lives on the server, but the LLM access (Claude CLI) lives on the client. The system puts the brain where there are no eyes, and the eyes where there is no brain.
- **Raw data shipped over the network**: Hooks send full tool_input + tool_response (potentially large) to the server for processing that will never happen. Wasted bandwidth, wasted queue memory.
- **Silent memory leak**: When processor=nil, observations accumulate in in-memory `ActiveSession.pendingMessages` until session timeout (30 min). No explicit discard, no warning. Proportional to tool event volume.
- **No health signal for processor**: The `/health` endpoint returns `{"status": "ready"}` with no indication that the processing pipeline is disabled. Docker HEALTHCHECK passes. System appears fully operational while producing zero observations.
- **Monolithic binary conflates storage and processing**: The server binary merges two fundamentally different concerns — stateless LLM processing (needs CLI) and stateful storage (needs DB).

### Blockers (Real Constraints for Client-Server)

| # | Blocker | Status | Notes |
|---|---------|--------|-------|
| B1 | **LLM access is client-only** | HARD | Claude CLI = subscription-only. API = separate billing (rejected). No server-side LLM without cost. |
| B2 | **Processing is async and slow** | HARD | 10-30s per CLI call. Hooks must return <1s. Cannot block Claude Code. |
| B3 | **Hook process lifetime** | HARD | Short-lived Go processes. Execute → stdout → exit. No persistent state. |
| B4 | **~~No client-side daemon~~** | SOFT | `pkg/hooks/worker.go` has `EnsureWorkerRunning()` — infrastructure for local daemon already exists. Can run local worker for processing + remote server for storage. |
| B5 | **~~Session state coupling~~** | SOFT | Sessions are accessible via HTTP API. Client processor doesn't need in-process access to session manager. Can fetch needed context from server. |
| B6 | **Fire-and-forget fragility** | MEDIUM | Orphan processes on Windows. Mitigated if local worker acts as process supervisor. |

### Existing Infrastructure That Helps

1. **`EnsureWorkerRunning()`** (`pkg/hooks/worker.go`): Already finds and starts a local worker binary. Can be repurposed for a local processing daemon.
2. **Import endpoint** (`handlers_import_export.go`): `StoreObservation` directly without SDK Processor. Client can extract observations and POST them pre-structured.
3. **`ENGRAM_INTERNAL=1` env var**: Hooks already skip when this is set, preventing recursive processing loops.
4. **Embedding client pattern** (`internal/embedding/openai.go`): HTTP client for external services — reusable pattern, but NOT for LLM calls (API billing constraint).

---

## 4. Challenge Review Results

Challenge-plans agent verified all claims against codebase. Key corrections applied:
- Filter lists now precise (14 tools in hook, 12 in server — different in both directions)
- B4 and B5 downgraded from HARD to SOFT (existing infrastructure)
- Memory leak behavior documented (no explicit discard, session timeout cleanup)
- Health endpoint correctly described (no health_score field in /health)
- Import endpoint identified as viable receiving mechanism

---

## 5. New Architecture: Progressive Refinement (Composition)

### Design Principle

**LLM is optional enrichment, not a functional requirement.**

The previous architecture (v1, engram-processor daemon) tried to move the LLM to the client. This solved the blocker but introduced complexity: new binary, WAL, daemon management, two ports. The root insight we missed: **the consumer of observations is Claude (an LLM)**. It doesn't need human-readable narrative to find relevant context. Raw tool events + semantic embedding = functional search.

Progressive Refinement: three levels of observation quality, each building on the previous:
- **Level 0**: Instant, deterministic, server-side, no LLM. Solves the Docker blocker.
- **Level 1**: Batch LLM enrichment, client-side, optional. Upgrades quality when CLI available.
- **Level 2**: Consolidation across observations. Already implemented.

### Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                      CLIENT (Windows)                         │
│                                                               │
│  Claude Code ──hooks──► Hook Binaries (Go)                    │
│    session-start   → GET /api/context/inject from SERVER      │
│    user-prompt     → GET /api/context/search from SERVER      │
│                      POST /api/sessions/init to SERVER        │
│    post-tool-use   → POST /api/events/ingest to SERVER        │
│    subagent-stop   → POST /api/sessions/subagent-complete     │
│    stop            → POST /api/sessions/{id}/finalize         │
│                                                               │
│  [OPTIONAL] Enrichment trigger (user-prompt hook):            │
│    IF >30min since last enrichment AND unprocessed events:    │
│    → spawn background `claude --print` with batch prompt      │
│    → POST enriched observations to SERVER                     │
│                                                               │
│  [OPTIONAL] Session-end enrichment (stop hook):               │
│    → spawn background `claude --print` with session batch     │
│    → POST enriched observations to SERVER                     │
│                                                               │
│  Claude CLI available (free with subscription)                │
└──────────────────────────┬───────────────────────────────────┘
                           │ HTTP (all traffic to single server)
                           ▼
┌──────────────────────────────────────────────────────────────┐
│                    SERVER (Docker/Linux)                       │
│                                                               │
│  engram-server (single binary, port 37777)                    │
│    ├── HTTP API                                               │
│    │     ├── POST /api/events/ingest ← raw tool events (NEW)  │
│    │     │     → Pre-filter (skip trivial, dedup)             │
│    │     │     → Deterministic classify + title + concepts    │
│    │     │     → BGE/OpenAI embed raw content                 │
│    │     │     → Store as Level 0 observation                 │
│    │     │     → pgvector sync (immediate searchability)      │
│    │     ├── POST /api/observations/enrich ← LLM-enriched (NEW)│
│    │     │     → Merge LLM fields into existing Level 0 obs  │
│    │     │     → Re-embed with enriched content               │
│    │     │     → Upgrade to Level 1                           │
│    │     ├── GET  /api/context/inject ← session start          │
│    │     ├── GET  /api/context/search ← per-prompt search      │
│    │     ├── POST /api/sessions/init                           │
│    │     ├── POST /api/sessions/{id}/finalize ← session end    │
│    │     │     → Finalize session, mark events processed       │
│    │     └── ... (existing endpoints unchanged)               │
│    ├── MCP SSE + Streamable HTTP (unchanged)                  │
│    ├── Deterministic Pipeline (NEW)                           │
│    │     ├── Event pre-filter (shouldSkipTool, trivial check) │
│    │     ├── Rule-based classifier (~80% accuracy)            │
│    │     ├── Template title generator                         │
│    │     ├── Keyword concept extractor                        │
│    │     └── Diff-based fact extractor                        │
│    ├── Embedding (builtin ONNX BGE 384d / OpenAI REST)        │
│    ├── Hybrid Search (vector + FTS + BM25, RRF fusion)        │
│    └── Storage (PostgreSQL + pgvector)                        │
│                                                               │
│  NO Claude CLI. NO SDK Processor. NO daemon.                  │
│  Level 0 = fully autonomous. Level 1 = client enrichment.     │
└──────────────────────────────────────────────────────────────┘
```

### Progressive Refinement Levels

#### Level 0: Deterministic + Embedding (Server-side, no LLM)

Raw tool event arrives at server. Server applies deterministic pipeline:

| Transformation | Method | Quality vs LLM |
|---|---|---|
| **Classify** (type) | Rule-based: Edit/Write → "change", Bash error → "bugfix", architecture keywords → "decision" | ~80% match |
| **Title** | Template: `"{tool_name}: {primary_file_or_command}"` | Functional, not elegant |
| **Facts** | Extract from diff: `"Modified lines 45-67 in server.go"`, from Bash: exit code + key output | ~60% of LLM facts |
| **Concepts** | Keyword extraction from file paths + tool output: `["authentication", "middleware"]` | ~70% match |
| **Filter** | Same as current: shouldSkipTool + shouldSkipTrivial + hash dedup | 100% (already deterministic) |
| **Narrative** | NOT generated (requires LLM). Field left NULL. | 0% — but consumer is Claude, which reads raw content anyway |

Embedding: `FormatEmbeddingText()` constructs a compact representation — `"{tool_name}: {file_path}\n{key_content}"` — to fit within BGE's 512-token `MaxSequenceLength`. Raw event JSON would overflow and silently truncate, losing the actual change. `EmbedBatch()` then produces vectors. BGE-small-en-v1.5 handles technical English well. Vector goes to pgvector immediately via modified `formatObservationDocs` (which must handle NULL narrative for Level 0).

**Result**: Searchable observation within <100ms of event. No LLM, no CLI, no external dependency. Docker server fully autonomous.

#### Level 1: Batch LLM Enrichment (Client-side, optional)

When Claude CLI is available (workstation with subscription), hooks trigger batch enrichment:

**Trigger conditions:**
- **User-prompt hook**: If >30min since last enrichment AND >5 unprocessed Level 0 events → spawn background `claude --print`
- **Stop hook**: At session end, batch all remaining Level 0 events

**Batch processing** (single LLM call for N events, not N calls for N events):
1. Fetch unprocessed Level 0 events from server (GET /api/events/unprocessed?session_id=X)
2. Pre-filter for content rot: keep first+last Edit per file, collapse redundant Bash
3. Build batch prompt with full event sequence (cross-event context)
4. `claude --print --model haiku` → structured observations with narrative, refined facts, concepts
5. POST enriched observations to server (POST /api/observations/enrich)
6. Server merges LLM fields into Level 0 observations, re-embeds, upgrades to Level 1

**Content rot handling in batch:**
- Code rot: batch sees first+last state → produces "final state" observation
- Decision rot: batch sees full sequence → produces "considered X, chose Y because Z"
- Approach rot: batch links failed approach to resolution
- Discovery rot: batch revises early conclusions with later corrections

**Cost**: 1-4 LLM calls per session (vs 10-30 in current architecture). Better quality (full context).

#### Level 2: Consolidation (Already implemented)

Existing consolidation system runs periodically. Merges related observations, detects patterns, builds knowledge graph. No changes needed — it operates on stored observations regardless of their level.

### Key Design Decisions

**D1: No new binary, no daemon, no WAL**
Level 0 runs inside existing `engram-server`. Level 1 runs as fire-and-forget background `claude --print` spawned by hooks. No persistent daemon. No WAL (events stored in PostgreSQL immediately). Dramatically simpler than v1 architecture.

**D2: All hooks talk to single server**
No split routing. All hooks → server:37777. Simplifies config, deployment, debugging. One `ENGRAM_WORKER_HOST` variable, one endpoint.

**D3: Raw events stored as first-class entities**
New table `raw_events` stores every ingested tool event. Level 0 observations reference their source events. Level 1 enrichment can re-process events later. Events are the source of truth; observations are derived views.

**D4: Embedding on raw text works**
`embedding.Service.EmbedBatch([]string)` is model-agnostic. BGE-small creates meaningful vectors from `"Edit file_path: internal/mcp/server.go, old_string: ..., new_string: ..."` — enough for semantic search by Claude. LLM-generated narrative improves recall but isn't required for functionality.

**D5: Dual embedding models retained**
- BGE-small-en-v1.5 (384d, ONNX bundled): Default. Zero external dependencies. Sufficient for Level 0.
- OpenAI REST (configurable): Optional upgrade for higher quality. Already implemented and tested.

**D6: SQLite-vec dead code removed**
`internal/vector/sqlitevec/` — 5 of 6 files have `//go:build ignore`; `helpers.go` compiles normally (type aliases delegating to `internal/vector/`). `internal/db/gorm/sqlite_build.go` has `//go:build ignore`. Dead code since Phase 2 migration. Verify no imports of `sqlitevec` package exist before deletion. PostgreSQL+pgvector is the only backend.

**D7: Health reports pipeline status**
Server `/health` includes: `level_0_enabled: true`, `events_ingested_24h: N`, `observations_created_24h: N`, `level_1_enriched_24h: N`. No more silent failure.

**D8: Fire-and-forget enrichment is acceptable**
If `claude --print` fails or times out during Level 1, observations remain at Level 0. System degrades gracefully. Level 0 is always the baseline. Level 1 is a quality upgrade, not a functional requirement.

### What Changes

| Component | Before | After |
|-----------|--------|-------|
| post-tool-use hook | POST raw data → server SDK Processor | POST raw data → server deterministic pipeline |
| stop hook | POST summarize → server CLI call | POST finalize → server; optionally spawn `claude --print` for batch enrichment |
| user-prompt hook | GET search from server | Same + optionally trigger mid-session enrichment |
| SDK Processor | `callClaudeCLI` on server (broken in Docker) | REMOVED from server entirely |
| Server processing | LLM-dependent (broken) | Deterministic Level 0 (always works) |
| Observation creation | 0 in production (CLI missing) | Immediate Level 0 for every meaningful event |
| Enrichment | Required for ANY observation | Optional upgrade (Level 0 → Level 1) |
| Network payload | Raw events (large) | Same (but server processes them, no relay needed) |
| New binaries | N/A | NONE (no engram-processor) |
| New infrastructure | N/A | NONE (no WAL, no daemon, no second port) |
| Dead code | sqlitevec (build-ignored) | Deleted |

### What Stays the Same

- MCP server (all 40 tools, unchanged)
- PostgreSQL + pgvector schema (extended, not replaced)
- Embedding pipeline (server-side, same Service/Model interfaces)
- Hybrid search (server-side, vector + FTS + BM25)
- Hook registration in Claude Code (same events, same hooks.json)
- Session tracking (server-side)
- Context injection format (`<engram-context>`, `<relevant-memory>`)
- `ENGRAM_INTERNAL=1` loop prevention
- Consolidation system (Level 2)
- Single port architecture (37777)
- Single binary (`engram-server`)

---

## 6. Roadmap (Decomposition)

### Phase 1: Deterministic Pipeline (Level 0)

**Goal**: Server creates searchable observations from raw events without LLM. Solves the production 0-observations blocker.

1. Create `raw_events` table (PostgreSQL migration):
   - id, session_id, project, tool_name, tool_input, tool_output, cwd, created_at_epoch
   - Index on (session_id, created_at_epoch)
2. Add `EnrichmentLevel int` and `SourceEventIDs JSONStringArray` fields to `Observation` struct (`pkg/models/observation.go`) + database migration
3. Create `POST /api/events/ingest` endpoint:
   - Receives raw tool event from hook
   - Stores in `raw_events` table
   - Applies pre-filter (shouldSkipTool, shouldSkipTrivial, hash dedup)
   - If passes: runs deterministic pipeline → creates Level 0 observation
4. Implement deterministic pipeline (`internal/pipeline/deterministic.go`):
   - `ClassifyEvent(event) → ObservationType` — rule-based classification
   - `GenerateTitle(event) → string` — template-based title
   - `ExtractFacts(event) → []string` — diff parsing, output extraction
   - `ExtractConcepts(event) → []string` — keyword extraction from paths/output
   - `FormatEmbeddingText(event) → string` — compact representation for embedding: `"{tool_name}: {file_path}\n{truncated_new_content}"` (stays within BGE's 512-token limit; raw event JSON would overflow)
   - `DetermineScope(concepts) → ObservationScope` — existing function
   - `ClassifyMemoryType(concepts) → MemoryType` — existing function (adapted)
5. Level 0 observation stored with `enrichment_level: 0`, `source_event_ids: [...]`
6. Modify `formatObservationDocs` (`internal/vector/pgvector/sync.go`) to handle Level 0: when `obs.Narrative` is NULL, create vector document from `FormatEmbeddingText` content instead. Without this, Level 0 observations produce ZERO vectors and are invisible to search.
7. Embedding + pgvector sync on Level 0 content (via modified formatObservationDocs)
8. Update `POST /api/sessions/{id}/finalize` to mark session events as processed
9. Rewire `stop` hook from `/sessions/{id}/summarize` to `/api/sessions/{id}/finalize` (current summarize endpoint calls broken CLI)
10. Update `/health` to report pipeline status
11. Rewire `post-tool-use` hook to call `/api/events/ingest` instead of current observation endpoint
12. **Early validation**: Run deterministic classifier against 50-100 sample events from existing sessions, measure agreement with LLM-classified observations. Validates the "~80% accuracy" claim that underpins Level 0 value proposition.

**Risk**: Low. Additive. Existing endpoints unchanged. Can deploy and immediately get observations in production.

**Verification**: Deploy to Docker → perform tool operations → check observations in DB → search returns results.

**Challenge review corrections applied (2026-03-02):**
- Added step 2: Observation model migration (enrichment_level, source_event_ids)
- Added step 4.5: FormatEmbeddingText for BGE 512-token limit
- Added step 6: formatObservationDocs modification for Level 0 (CRITICAL — without this, zero vectors)
- Added step 9: Stop hook rewire from broken /summarize to /finalize
- Added step 12: Early validation of 80% classification accuracy claim
- Fixed D6: `sqlitevec/helpers.go` lacks `//go:build ignore` tag — verify no imports before deletion

### Phase 2: Content Pre-filter and Event Quality

**Goal**: Reduce noise in Level 0 observations. Handle content rot at collection time.

1. Implement event pre-filter (`internal/pipeline/prefilter.go`):
   - Track Edit events per file per session — keep only first + last
   - Skip consecutive Read events on same file
   - Collapse sequential Bash commands into single event when related
   - Skip trivial operations (shouldSkipTrivialOperation, adapted from processor.go)
2. Improve deterministic classifier accuracy:
   - Use tool output patterns (error messages → bugfix, test results → testing)
   - Use file path patterns (test files → testing, config files → configuration)
   - Use git diff structure for better fact extraction
3. Add `event_status` field to raw_events: `ingested`, `filtered`, `processed`, `enriched`
4. Tests for pre-filter correctness (unit tests with real event samples)

**Risk**: Low. Quality improvement, no architectural changes.

### Phase 3: Batch LLM Enrichment (Level 1)

**Goal**: Client-side optional enrichment upgrades Level 0 → Level 1 when Claude CLI is available.

1. Create `GET /api/events/unprocessed` — returns Level 0 events for a session, pre-filtered
2. Create `POST /api/observations/enrich` — merges LLM-enriched fields into existing Level 0 observations:
   - Updates narrative, refined_title, refined_facts, refined_concepts
   - Re-embeds with enriched content (better vectors)
   - Sets `enrichment_level: 1`
3. Build batch enrichment prompt (`internal/pipeline/enrichment_prompt.go`):
   - Takes N events as input sequence
   - Instructs LLM: "focus on final state for code, capture evolution for decisions"
   - Content rot handling: first+last Edit per file, collapse approach sequences
   - Returns structured observations (reuse existing XML format)
4. Client-side enrichment logic in hooks:
   - `user-prompt` hook: check if >30min since last enrichment, >5 unprocessed events
   - `stop` hook: batch all remaining unprocessed events
   - Spawn `claude --print --model haiku` as background process (fire-and-forget)
   - Parse response, POST to `/api/observations/enrich`
5. Handle enrichment failures gracefully (Level 0 observations remain functional)

**Risk**: Medium. Client-side CLI spawning + parsing. But failure is non-fatal (Level 0 baseline).

### Phase 4: Cleanup and Hardening

**Goal**: Remove dead code, improve reliability.

1. Delete `internal/vector/sqlitevec/` (dead code, `//go:build ignore`)
2. Delete `internal/db/gorm/sqlite_build.go` (dead code, `//go:build ignore`)
3. Remove SDK Processor from server (`internal/worker/sdk/processor.go` CLI dependency)
4. Remove `callClaudeCLI` and CLI path detection from server
5. Remove in-memory observation queue from session manager (replaced by raw_events table)
6. Update goreleaser config (no new binary, just cleanup)
7. Update Dockerfile (no changes needed — but verify health endpoint)
8. Integration tests: Level 0 pipeline end-to-end against real PostgreSQL

**Risk**: Low. Deletion and simplification.

### Phase 5: Multi-workstation Refinement

**Goal**: Multiple clients enriching observations on the same server.

1. Workstation ID in raw_events and observations metadata
2. Server deduplicates cross-workstation observations (same event from different sessions)
3. Enrichment status per-workstation (one workstation's enrichment doesn't block another)
4. Token-based auth (reuse existing ENGRAM_API_TOKEN)

**Risk**: Low. Architecture naturally supports this — all state is in PostgreSQL.

### Verification Gates

**After Phase 1** (critical — solves production blocker):
1. Deploy engram-server to Docker (unleashed.lan)
2. Start Claude Code session on workstation
3. Perform tool operations (Edit, Write, Bash)
4. Check: `raw_events` table has entries
5. Check: `observations` table has Level 0 entries
6. Check: `vectors` table has embeddings
7. Start new session → verify observations injected via session-start hook
8. Search via MCP tools → verify relevant results returned

**After Phase 3** (enrichment working):
1. Perform a coding session (>30 min)
2. Verify user-prompt hook triggers mid-session enrichment
3. Check: some observations upgraded to Level 1 (have narrative)
4. Verify enriched observations have better search relevance
5. End session → verify stop hook triggers final batch enrichment
