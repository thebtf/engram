# Engram Agent Memory Design Research

Date: 2026-06-27
Slug: agent-memory-design
Status: draft

## Executive Summary

Engram already has several strong pieces of a real agent-memory system: principal ownership, guarded privacy scopes, Thompson-sampling-based injection ranking, outcome-modulated feedback, retrieval reconsolidation, transcript capture for dream-cycle crystallization, and an explicit roadmap slot for a `Memory Product Layer`. What it does **not** yet have is a coherent product mechanism for deciding what to remember, what to rank higher, what to forget, what to consolidate, and how session/goal/task/handoff state should move through Engram itself instead of the filesystem.

The biggest design mistake to avoid is turning the operator into a human garbage collector. The operator needs a **control surface**, not a row-by-row janitorial job. Memory must self-sort by utility, freshness, scope, and contradiction risk; the operator should review queue edges, policy defaults, and risky merges — not every memory row.

The ideal Engram direction is a **tiered memory system**:
1. native handoff/state tier for sessions, goals, assignments, and project state;
2. bounded always-hot principal/profile memory;
3. episodic transcript/event stream;
4. semantic fact memory with ranking + decay;
5. temporal/provenance layer for evolving truths;
6. moderation queue for forgetting/consolidation/usefulness decisions.

The best outside ideas are complementary, not copy-paste: Hermes contributes bounded always-on memory and deliberate forgetting, Hindsight contributes world/experience/mental-model separation plus hybrid retrieval and reflection, Graphiti contributes temporal validity windows and provenance, Mem0 contributes production pragmatism for scope keys and multi-signal retrieval, ByteRover contributes agent-native state/version-control ideas for shared context, and RLM/Memento contribute context folding for cheap long-horizon reasoning rather than persistent memory itself.

## 1. Explicit Product Requirements for Engram Memory

Derived from the operator brief, current roadmap, and existing Engram artifacts.

### R1. Operator controls policy, not garbage
The operator must be able to intervene, inspect, approve, reject, or escalate — but must **not** spend their time sorting every memory row. The system itself must pre-rank, pre-filter, and pre-classify memory so the operator sees bounded review queues rather than raw dump surfaces.

### R2. Remembering and forgetting are symmetrical product capabilities
It is not enough to design what to store. The memory product must explicitly define:
- what decays automatically,
- what gets suppressed,
- what expires by retention,
- what merges into stronger semantic memory,
- what is destroyed only by explicit operator action.

### R3. Ranking must be utility-aware, not only similarity-aware
Memory selection must account for:
- semantic relevance,
- recency / retrievability,
- proven usefulness from real downstream use,
- principal/domain match,
- contradiction/noise risk,
- privacy scope.

### R4. Principal/domain identity is load-bearing
Memory is not just global project sludge. The system must reason over:
- principal-owned memory,
- shared/project/global memory,
- domain ownership / specialty,
- safe widening rules,
- per-principal memory behavior.

### R5. Handoff/state transfer must become native
Session state, active goals, assignments, task contracts, and project state should move through Engram as first-class memory/state records. Filesystem artifacts like `current.json` and `CONTINUITY.md` are currently workable scaffolds, but the target is cheap, queryable, audit-backed native handoff.

### R6. Memory must stay auditable and honest
Every mutation that materially changes memory state must leave provenance. The system must be able to answer:
- who/what wrote this,
- why it was promoted/suppressed/forgotten/consolidated,
- what evidence supported the action,
- what changed downstream afterward.

### R7. Releaseable increments beat long invisible memory rewrites
This memory epic must ship in operator-visible slices. The system cannot disappear for days into a hidden backend rewrite with no usable product increment.

## 2. What Engram Already Has

### 2.1 Ranking / retrieval surfaces already present

#### Injection ranking — Thompson Sampling
`internal/injection/injection.go:38-113` implements Thompson Sampling for injection selection. It blends novelty/exploration with observed success and adds newcomer bias for fresh memories.

#### Retrieval scoring — composite + feedback blend
`internal/retrieval/scoring.go:55-104` combines relevance, recency, and importance. The importance term already blends in Thompson posterior mean when evidence exists, falling back to citation-rate reinforcement when priors have not yet matured.

#### Feedback updater — success/partial/failure modulation
`internal/feedback/updater.go:28-103` already modulates memory priors by session outcome. This is a meaningful seed of memory ranking by downstream utility, not just naive popularity.

### 2.2 Reconsolidation / retrieval reinforcement already present
`internal/lifecycle/decay.go:32-41` defines reconsolidation as stability growth after successful retrieval. `internal/mcp/tools_memory.go:1869-1902` and `internal/mcp/tools_memory.go:1321-1338` apply reconsolidation updates on actual retrievals. So Engram already treats retrieval as a learning event.

### 2.3 Forgetting today is weak and fragmented
Current forgetting is mostly operational, not product-grade:
- `suppress_memory` exists as a soft-delete style memory mutation (`internal/mcp/tools_memory.go:2071+`).
- retention cleanup exists for logs/audit and transcript stores (`internal/worker/retention.go:16-90`, `internal/worker/dream_cycle.go:270+`).
- transcript retention is configurable (`internal/config/config.go:97`).

What is **missing** is semantic forgetting policy: no coherent distinction yet between suppression, expiry, archival, consolidation, and deliberate destruction at the product level.

### 2.4 Crystallization / consolidation seeds exist, but are incomplete
`internal/worker/dream_cycle.go:46-219` already runs an adaptive dream-cycle over stored transcripts: list unprocessed transcripts, build digest, run LLM extraction, route decisions into crystallization candidates. This is the strongest seed of long-term semantic memory growth.

But there are big gaps:
- the path is candidate-centric, not an operator memory product workflow;
- consolidation is not yet the mainstream moderation path;
- `DetectLoss` / structural-loss protection is explicitly noted as not yet wired into the dream-cycle update path (`internal/worker/dream_cycle.go:195-205`).

### 2.5 Principal memory substrate exists
`ENG-PIM-1` and `ENG-PMQ-1` are already implemented. The PMQ PRD explicitly says the shipped slice deferred **per-principal forgetting/consolidation** and **principal-scoped `get_memory_brief`** (`.agent/specs/principal-memory-query-domain-registry/prd.md:35-37`). This is critical: the roadmap already knows exactly which memory-governance features are still missing.

### 2.6 `get_memory_brief` exists, but not in the needed form
`internal/mcp/tools_brief.go:20-95` shows `get_memory_brief` is:
- gated by `ENGRAM_ADAPTIVE_ENABLED`;
- project-scoped, not principal-scoped;
- based on `ListForInjection` and injection scoring;
- filtered by privacy scope;
- returns a bounded compact brief for delegation.

This is useful, but it is not yet a principal-aware product primitive. It is an adaptive helper, not a complete memory governance surface.

### 2.7 Native handoff/state is designed, not implemented
Engram has type-level design for native handoff/state:
- `SessionStateSlots` and `ProjectStateRecord` in `pkg/cognitive/types.go:94-119`
- `AttentionEventRecord`, `RawSignal`, `Distilled` in `pkg/cognitive/types.go:121-159`
- `StateWriter` / `DirectiveDistiller` interfaces in `pkg/cognitive/interfaces.go:56-86`

But grep evidence shows only interfaces, tests, and noop implementations — no production write path beyond noop/test stubs. So native handoff/state exists as an architectural intention, not as a live product surface.

### 2.8 Filesystem oracle is the current reality
`D:\Dev\engram\.agent\session-state\current.json` is the actual front-door oracle today. The retro explicitly says the current fix is a tiny state oracle plus bounded continuity, because large continuity files caused expensive resume and compact churn (`.agent/reports/retro-sessions-2026-06-24.html:166-225`).

This means handoff is still mostly filesystem-mediated, not Engram-native.

## 3. Outside Systems — What They Actually Contribute

## 3.1 Microsoft Memento
Sources: local `D:\Dev\_EXTRAS_\memento\README.md`, local `memento.pdf` pages 1-6.

### What it is
Memento teaches the model to compress its own long reasoning into dense state summaries (“mementos”), evicting full reasoning blocks while continuing from compressed state.

### Strong ideas
- model-internal context folding;
- block segmentation + iterative refinement of summaries;
- preserve only what future reasoning needs;
- compression is deliberate, not arbitrary truncation.

### Weak fit for Engram
This is not a persistent agent memory architecture. It is a **context-management** architecture for long reasoning chains. It says almost nothing about user/principal memory governance, operator moderation, or long-term forgetting policy.

### Best use for Engram
Use Memento-style ideas for:
- cheap session/handoff compression,
- compact internal state packets,
- long-run developer/PM continuity folding.

Do **not** mistake it for a complete memory product.

## 3.2 Hermes Agent Memory System
Sources: `glukhov.org` article extract plus provider comparison.

### What it is
Hermes keeps a tiny always-on core (`MEMORY.md`, `USER.md`) in the prompt, bounded by character limits, curated by explicit add/replace/remove, with optional one-at-a-time external providers.

### Strong ideas
- bounded always-on memory is a feature, not a bug;
- forgetting is explicit maintenance (`remove`, `replace`);
- retrieval latency for core memory is zero because it is always active;
- external providers are additive, not the core brain.

### Weak fit for Engram
Too small and too static for Engram’s multi-principal, operator-governed, server-side memory product. Great for agent profile memory; insufficient for operator-scale moderation and temporal fact management.

### Best use for Engram
Adopt Hermes-style rules for the **hot core**:
- small always-on principal profile / active conventions,
- hard size budgets,
- curation pressure,
- memory that is always active rather than always re-searched.

## 3.3 Cognee
Sources: local `README.md`, `CLAUDE.md`, grep across API and config docs.

### What it is
Cognee is a memory platform centered on ingestion → graph extraction → graph/vector retrieval with remember/recall/forget/improve primitives and multi-tenant isolation.

### Strong ideas
- knowledge graph + vector search hybrid;
- explicit `remember`, `recall`, `forget`, `improve` API surface;
- user/dataset isolation as a first-class concern;
- graph-native provenance and ontology grounding.

### Weak fit for Engram
Very ingestion/knowledge-graph heavy. Risk of over-investing in graph infrastructure before operator memory workflows are usable. More “company brain” than “operator-memory governance cockpit.”

### Best use for Engram
Steal the explicit lifecycle verbs and graph/provenance lessons, but keep Engram’s product center on operator workflows, not giant ingestion-first pipelines.

## 3.4 Mem0
Sources: local `README.md`, CLI specification, plugin docs, web docs references.

### What it is
Mem0 is a pragmatic memory layer with scope keys (`user_id`, `agent_id`, `run_id`), entity linking, multi-signal retrieval, and a production emphasis on token-efficient single-pass extraction.

### Strong ideas
- production-grade scope keys for user/agent/run;
- entity linking + temporal reasoning in retrieval;
- multi-signal retrieval (semantic + BM25 + entity);
- very practical ecosystem and integrations.

### Weak fit for Engram
The 2026 algorithm is explicitly **ADD-only** (`README.md:55-60`): one extraction call, no UPDATE/DELETE. Memories accumulate; overwrites disappear from the algorithmic core. That is almost the opposite of the explicit forgetting/unlearning requirement from the operator brief.

### Best use for Engram
Take the scope-key discipline and multi-signal retrieval. Reject the idea that memory should be only additive. Engram needs memory decay, suppression, consolidation, and handoff pruning as first-class operations.

## 3.5 Recursive Language Models (RLM) and Prime Intellect’s framing
Sources: local `RLM/README.md`, arXiv `2512.24601`, Prime Intellect article.

### What it is
RLM treats long context as an external environment, letting the model inspect, chunk, recurse, and aggregate through a REPL and subcalls. Prime Intellect frames it as context folding for long-horizon agents.

### Strong ideas
- huge contexts handled without feeding everything directly to the model;
- recursive decomposition and selective viewing;
- persistent state variables for multi-turn processing;
- good fit for long-horizon task scaffolding and context folding.

### Weak fit for Engram
Again: not a durable memory product. It is a **reasoning scaffold**. It helps with what to keep in active context during long runs, but not with user/principal memory policy by itself.

### Best use for Engram
RLM ideas are perfect for **native handoff/oracle folding**:
- compress current state into machine-usable packets,
- keep long-horizon PM/developer work cheap,
- reduce token burn on recovery and scouting.

This is highly relevant to replacing file-based handoff, but only as one layer.

## 3.6 Graphiti
Sources: local `README.md`, grep across examples/spec.

### What it is
Graphiti builds temporal context graphs where facts have validity windows, are invalidated rather than deleted, and always trace back to episodes.

### Strong ideas
- temporal validity windows for facts;
- provenance through episodes;
- contradiction handling by invalidation with history, not hard overwrite;
- hybrid retrieval across semantics, keywords, and graph traversal.

### Weak fit for Engram
Heavy graph/database substrate. Overkill if applied to every low-value memory. Needs careful scoping or it becomes another infrastructure-first detour.

### Best use for Engram
Use Graphiti-style temporal modeling for **high-value facts** only:
- evolving principal facts,
- domain ownership,
- important operator decisions,
- consolidated semantic memories.

Do not graphify every scratch memory row.

## 3.7 Hindsight
Sources: local `README.md`, local CLAUDE snippets, workflow docs.

### What it is
Hindsight splits memory into world facts, experiences, and mental models. It provides `retain`, `recall`, and `reflect`, with hybrid retrieval and cross-encoder rerank.

### Strong ideas
- three-layer memory ontology: world / experience / mental model;
- `reflect` as synthesis, not only lookup;
- hybrid retrieval + rerank;
- memory banks with metadata for per-user isolation;
- explicit consolidation work in the implementation/project.

### Weak fit for Engram
Less obvious operator-first forgetting policy in the public surface; still relatively system-heavy. The “mental model” layer can become opaque if not kept auditable.

### Best use for Engram
Hindsight gives Engram a strong conceptual split:
- raw experiences / episodes,
- stable facts,
- synthesized mental models / briefs.

This is probably the best external shape for Engram’s mid-to-long-term memory layers.

## 3.8 ByteRover CLI
Sources: local `README.md`, local paper README.

### What it is
ByteRover is a context tree with git-like version control, curate/review workflows, branch/merge/push/pull, and multi-machine sync.

### Strong ideas
- agent-native shared state, not just memory retrieval;
- review queue for memory/context changes;
- version-control semantics on context tree;
- multi-machine / multi-agent context sync.

### Weak fit for Engram
License is Elastic 2.0, not MIT/Apache-friendly. Also more of a complete alternate memory product than a drop-in module. But the conceptual model is extremely relevant.

### Best use for Engram
ByteRover is the clearest inspiration for **native handoff/state transfer**:
- session/goal/task/contracts as a governed context tree,
- review/approve/reject on curated context,
- cheap shared state across machines/agents.

This maps directly to the operator request to move handoff from filesystem into Engram.

## 4. Distilled Ideal Architecture for Engram

## 4.1 Memory should be multi-tiered

### Tier A — Native Handoff / State Plane
Purpose: cheap, first-read machine state.

Stores:
- active goal contracts
- PM/developer assignment state
- session slots
- current execution pointer
- worktree/PR/resume hints
- project phase record

Source of truth: Engram-native records.
Filesystem role: cache / export / emergency fallback only.

### Tier B — Hot Principal Memory
Purpose: bounded always-hot memory for a principal/agent.

Stores:
- preferences
- durable conventions
- tool/environment facts that should always be present
- active working identity/profile facts

Design: Hermes-like bounded curated memory. Always cheap; never row-dump sized.

### Tier C — Episodic Stream
Purpose: raw or lightly processed events/transcripts/tool outcomes.

Stores:
- transcripts
- task outcomes
- tool traces
- explicit attention events
- session state observations

This is the material for distillation, not the thing injected wholesale.

### Tier D — Semantic Fact Memory
Purpose: stable retrievable facts, decisions, constraints, lessons.

Stores:
- promoted memories
- principal/domain-aware facts
- rated/suppressed/validated knowledge

Ranking: semantic + recency + Thompson utility + principal/domain match + contradiction penalties.

### Tier E — Temporal / Provenance Graph
Purpose: represent facts that evolve over time.

Use only for high-value evolving truths:
- ownership,
- versioned facts,
- changing constraints,
- consolidated mental-model statements.

Graphiti-style validity windows and episode provenance belong here.

### Tier F — Moderation Queue
Purpose: keep operator burden bounded.

Queue items are not all memories. They are only:
- low-confidence promotions,
- conflict/contradiction candidates,
- structural-loss consolidation candidates,
- high-impact forgetting actions,
- policy exceptions.

## 4.2 Remembering algorithm

1. Ingest raw episodes/transcripts/tool outcomes into episodic stream.
2. Classify candidate memory by scope:
   - principal
n   - domain
   - project
   - shared/global
3. Decide destination tier:
   - hot principal profile,
   - semantic memory,
   - temporal graph,
   - moderation queue,
   - discard.
4. Store with provenance and confidence.

The memory system should remember only what survives a utility test, not every token that passed through it.

## 4.3 Ranking algorithm

Current Engram already has strong seeds here. Ideal ranking becomes:

`score = semantic relevance + recency/retrievability + proven usefulness + principal/domain match - contradiction/noise risk`

Implementation ingredients:
- keep current Thompson sampling for injection/retrieval,
- keep outcome-modulated updates,
- add principal/domain match term,
- add contradiction/noise penalties,
- separate ranking for operator moderation queues vs retrieval injection.

## 4.4 Forgetting algorithm

Forgetting must not mean “DELETE or nothing”. Use five actions:

1. **Suppress** — hide from retrieval/injection, preserve audit.
2. **Expire** — retention-based removal for low-value episodic traces.
3. **Archive / cold storage** — keep reachable but out of hot retrieval.
4. **Consolidate** — merge redundant items into stronger semantic memory.
5. **Destroy** — explicit hard delete for operator-approved cases.

Default path for low-value memory should be **decay then suppress/archive**, not permanent growth.

## 4.5 Consolidation algorithm

Consolidation should:
- group semantically overlapping memories,
- preserve provenance to source episodes,
- test for structural loss before merge,
- emit a candidate/moderation item when loss risk is non-trivial,
- produce a stronger semantic memory or mental-model item.

This is where Hindsight’s `reflect`, Engram’s crystallization path, and Graphiti-like temporal invalidation can meet.

## 4.6 Native handoff mechanism

The operator request is correct: session state, contracts, tasks, and handoff should move through Engram, not mostly through files.

Target design:
- `SessionStateSlots` and `ProjectStateRecord` become real persisted Engram state surfaces;
- `AttentionEventRecord` / distilled directives become recallable execution-state memory;
- session load reads Engram-native handoff/state first;
- `current.json` becomes a lightweight cache/export of the Engram-native source, not the primary authority.

ByteRover’s context-tree + review model is the best analogy here, but Engram should keep stronger privacy/audit semantics and lighter operator burden.

## 5. What Is Already in Engram Docs / Roadmap vs Missing

## Already represented
- roadmap explicitly names `Memory Product Layer` as next phase (`roadmap-stabilization-marathon.md:49-50`)
- PMQ PRD explicitly defers principal-scoped `get_memory_brief` and per-principal forgetting/consolidation (`principal-memory-query-domain-registry/prd.md:35-37`)
- operator console already has memory/noise/usefulness/queue/domain surfaces
- ranking and reinforcement mechanisms already exist in code
- crystallization / transcript digestion / candidate routing already exist in code
- native state/handoff types and interfaces are designed in `pkg/cognitive/*`
- retro already diagnosed that file-based oracle/continuity is too expensive and should become a cheaper state substrate (`retro-sessions-2026-06-24.html:186-225`)

## Missing
- production implementation of native `StateWriter`
- principal-scoped `get_memory_brief`
- explicit forgetting product flows
- explicit consolidation operator workflow
- structural-loss protection wired into mainstream consolidation paths
- bounded moderation queues for memory governance
- lifecycle policy table for suppress/expire/archive/consolidate/destroy
- first-class native handoff/session/goal/task transfer through Engram itself

## 6. Recommended Phased Roadmap

### Phase 1 — Native Handoff Plane
Ship first:
- Engram-native session/project state write/read surface
- assignment/goal/task/handoff records
- filesystem oracle becomes fallback cache only

Why first: this reduces token waste and makes every later epic cheaper.

### Phase 2 — Principal Memory Explorer + Briefs
Ship:
- principal/domain memory explorer
- principal-scoped brief surface
- privacy-safe widening/audit

Why second: gives the operator real visibility before they mutate anything.

### Phase 3 — Usefulness / Noise Loop
Ship:
- usefulness/noise review workflow
- suppression / ranking feedback loop
- bounded moderation queue

Why third: this is the first true “operator improves memory quality” release.

### Phase 4 — Forgetting / Consolidation
Ship:
- forgetting modes
- consolidation candidates
- structural-loss guard
- audit/export proof

Why fourth: dangerous product power; should come after visibility and review loops.

### Phase 5 — Temporal Fact Graph for High-Value Truths
Ship selectively:
- validity windows
- invalidation instead of overwrite
- provenance-first fact evolution

Why fifth: powerful, but should be scoped to high-value truths only.

## 7. Decision

The best Engram memory architecture is **not**:
- pure vector recall,
- pure knowledge graph,
- pure always-on prompt memory,
- pure additive memory accumulation,
- or pure filesystem handoff.

It is a hybrid:
- Hermes for hot bounded memory,
- Hindsight for world/experience/mental-model split,
- Graphiti for temporal truth/provenance,
- Mem0 for scope keys and practical multi-signal retrieval,
- ByteRover for native shared-state/versioned handoff concepts,
- RLM/Memento for context folding and cheap long-horizon internal state.

Engram already has enough seeds that this is an evolutionary path, not a restart. The next big job is to stop treating handoff and memory quality as side effects, and make them first-class product surfaces.
