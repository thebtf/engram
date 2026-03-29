# Investigation Report: Cipher (campfirein/cipher) vs Engram: competitive analysis — what Cipher does better, what to integrate, what to improve

**Generated:** 2026-03-29T19:30:40.093Z
**Project:** D:\Dev\engram
**Iterations:** 1
**Findings:** 10
**Corrections:** 0
**Coverage:** 10/10 areas

## Findings

| ID | Severity | Description | Source | Iter | Status |
|----|----------|-------------|--------|------|--------|
| F-0-1 | P2 | ARCHITECTURE: Cipher has a modular 'brain' architecture with 6 subsystems: embedding (7 backends), llm (compression strategies), memAgent (autonomous agent), memory (lazy loading), reasoning (content detection + search context), tools (unified tool manager). Engram has a monolithic server.go with handler functions. Cipher's modularity allows mix-and-match of backends; engram is hardcoded to PostgreSQL+pgvector+OpenAI-compatible embeddings. | Read src/core/brain/ structure | 0 | active |
| F-0-2 | P1 | DUAL MEMORY (System 1 + System 2): This is Cipher's KEY differentiator. System 1 = regular memories (concepts, business logic, past interactions) stored in vector DB. System 2 = REASONING TRACES — the model's actual thought process (thought/action/observation/decision/conclusion/reflection steps) extracted, evaluated for quality (0-1 score), and stored separately in a 'reflection vector store'. Engram has NOTHING like System 2. Engram stores observation titles+narratives but NOT the reasoning chain that led to them. This is a significant gap — reasoning traces would let engram learn not just WHAT was decided but HOW the agent reasoned about it. | Read def_reflective_memory_tools.ts, store_reasoning_memory.ts | 0 | active |
| F-0-3 | P2 | MEMORY COMPRESSION: Cipher has 3 strategies for conversation history compression: (1) HybridStrategy — intelligently combines middle-removal and oldest-removal based on conversation characteristics, (2) MiddleRemovalStrategy — removes messages from the middle of conversation to keep beginning (context) and end (recent), (3) OldestRemovalStrategy — removes oldest messages first. Messages have priority assignments and compression levels. Engram has NO conversation compression — it relies on CC's native compaction. This is relevant because engram could use compression to better summarize sessions before context compaction wipes them. | Read core/brain/llm/compression/strategies/ | 0 | active |
| F-0-4 | P2 | KNOWLEDGE GRAPH: Cipher supports Neo4j + in-memory graph with rich entity/relationship tools: add-node, add-edge, delete-node, update-node, search-graph, query-graph, get-neighbors, extract-entities, enhanced-search, intelligent-processor, relationship-manager. 12 tools total. Engram has FalkorDB (Redis-based) + PostgreSQL relations table with 17 relation types. Cipher's graph is more sophisticated — it has entity extraction (automatic), intelligent processing, and enhanced search. Engram's graph is passive (relations detected by pattern detector, not actively managed). Key gap: Cipher auto-extracts entities from conversations into the graph; engram only detects relations between existing observations. | Read core/brain/tools/definitions/knowledge_graph/ | 0 | active |
| F-0-5 | P1 | memAgent: Cipher's MemAgent is an autonomous LLM-powered agent that runs as middleware between the user and the IDE. It has its OWN LLM service (gpt-4.1-mini default), manages sessions, routes tools, and makes autonomous decisions about when to store/retrieve memories. It operates in 3 modes: cli (persistent session), mcp (per-connection sessions), api (unique sessions). It has a state manager, session manager, and unified tool manager that combines MCP + internal tools.

Engram has NO autonomous agent — it's a passive server that responds to hook-triggered API calls. Hooks (session-start, user-prompt, post-tool-use) drive all behavior. The server never initiates actions on its own (except maintenance tasks). Cipher's approach: agent actively decides what to remember. Engram's approach: hooks decide what to send, server processes it. | Read core/brain/memAgent/agent.ts | 0 | active |
| F-0-6 | P1 | EMBEDDING RESILIENCE: Cipher has a sophisticated ResilientEmbedder wrapper with: EmbeddingCircuitBreaker (separate from LLM CB), health check intervals, automatic fallback to disabled state, max consecutive failure tracking, recovery intervals, and 4 status states (HEALTHY/DEGRADED/DISABLED/RECOVERING). Plus safe-operations.ts for graceful degradation.

Engram has a single CircuitBreaker shared between LLM and embedding (through the SDK processor), no health check loop, no separate embedding resilience, no DEGRADED/RECOVERING states. When embedding fails, vector search silently falls back to FTS. No automatic recovery probe.

Key insight: Cipher's embedding resilience is MUCH more mature — dedicated circuit breaker, health monitoring, graceful degradation with status tracking. | Read core/brain/embedding/resilient-embedder.ts, circuit-breaker.ts | 0 | active |
| F-0-7 | P2 | SESSION MANAGEMENT: Cipher has ConversationSession + SessionManager with proper lifecycle. Sessions are first-class objects with history, context, and state. CLI mode uses persistent 'default' session; MCP/API generate unique session IDs.

Engram's sessions are DB records (sdk_sessions table) managed by an in-memory SessionManager with 30-min timeout. Sessions are cleaned up aggressively. No persistent default session.

Gap: Cipher sessions persist across restarts (CLI mode). Engram sessions are ephemeral — lost when server restarts or 30-min timeout expires. This directly caused the summaries bug (sessions not in memory when summarizer runs). | Read core/session/, memAgent/agent.ts:47-58 | 0 | active |
| F-0-8 | P1 | MCP TOOLS: Cipher exposes tools via UnifiedToolManager that merges internal tools + MCP tools. In 'default' MCP mode, only `ask_cipher` tool is exposed to external clients (single entry point). In 'aggregator' mode, all tools exposed. Internal tools include: search_memory, extract_and_operate_memory, store_reasoning_memory, extract-knowledge, workspace_search, workspace_store + 12 knowledge graph tools.

Engram exposes 7 primary tools (recall/store/feedback/vault/docs/admin/health). Engram's approach: tools are CRUD operations on observations. Cipher's approach: tools are autonomous operations (extract_and_operate = analyze + decide + store).

Key difference: Cipher's `extract_and_operate_memory` tool uses LLM to analyze input, extract memories, AND decide operations (store/update/delete) — all in one call. Engram requires the agent to explicitly call store_memory with pre-formatted content. | Read core/brain/tools/definitions/memory/extract_and_operate_memory.ts, unified-tool-manager.ts | 0 | active |
| F-0-9 | P2 | TEAM SHARING: Cipher's README mentions 'Easily share coding memories across your dev team in real time' via WebSocket connection manager (event-bridge.ts, event-subscriber.ts, message-router.ts). Multiple developers can connect to the same Cipher server and share memories.

Engram has SSE broadcasting (sseBroadcaster) for dashboard real-time updates, but NO multi-user memory sharing. Engram is single-user by design (one workstation → one server, or multi-workstation → shared server but no user isolation).

This is a PRODUCT gap, not a technical one. Engram could support team sharing with: (1) user/workstation scoping on observations, (2) WebSocket pub/sub for real-time sync, (3) access control per observation scope. | Read README.md features + app/api/websocket/ | 0 | active |
| F-0-10 | P3 | STORAGE BACKENDS: Cipher supports 7 vector stores (Chroma, FAISS, Milvus, pgvector, Pinecone, Qdrant, Redis, Weaviate + in-memory), 5 storage backends (PostgreSQL, SQLite, Redis, cache, in-memory), and 2 graph backends (Neo4j, in-memory). All via factory pattern with interface abstraction.

Engram supports 1 vector store (pgvector), 1 storage (PostgreSQL), 1 graph (FalkorDB). No backend abstraction — Go code directly uses GORM+pgvector.

Cipher's multi-backend support is a competitive advantage for adoption (users choose their stack). Engram's single-stack approach is simpler but limits deployment options. For engram, this is a DESIGN CHOICE not a bug — PostgreSQL+pgvector is the correct choice for a centralized server (Constitution #1: Server-Only Architecture). | Read core/storage/backend/ + core/vector_storage/backend/ | 0 | active |

## Coverage Map

| Area | Status |
|------|--------|
| Architecture: how Cipher structures memory (brain/memory/reasoning/tools) | ✓ checked |
| Dual Memory: System 1 (concepts+logic) vs System 2 (reasoning traces) — does engram have this? | ✓ checked |
| Memory compression: strategies (hybrid, middle-removal, oldest-removal) | ✓ checked |
| Knowledge graph: Cipher's graph implementation vs engram's FalkorDB | ✓ checked |
| memAgent: autonomous memory agent — what it does, how it differs from engram hooks | ✓ checked |
| Embedding resilience: circuit-breaker, safe-operations, resilient-embedder patterns | ✓ checked |
| Session management: Cipher's approach vs engram's sdk_sessions | ✓ checked |
| MCP tools: what tools Cipher exposes vs engram's 7 primary | ✓ checked |
| Team sharing: real-time memory sharing across developers | ✓ checked |
| Storage backends: what databases Cipher supports vs engram's PostgreSQL+pgvector | ✓ checked |

## Convergence History

- Iteration 1: 50%