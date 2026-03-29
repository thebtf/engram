# Feature: Reasoning Traces (System 2 Memory)

**Slug:** reasoning-traces
**Created:** 2026-03-29
**Status:** Draft
**Author:** AI Agent (reviewed by user)
**ADR:** .agent/arch/decisions/ADR-003-reasoning-traces-system2.md
**Source:** Cipher competitive analysis (F-0-2)

## Overview

Add a second memory layer that captures agent reasoning chains — not just WHAT was decided
but HOW the agent reasoned about it. Stores thought→action→observation→decision→conclusion
steps extracted from tool events, evaluated for quality, and retrievable via semantic search.

## Context

### Problem
Engram stores observation results (title + narrative) but loses the reasoning process.
When a similar problem recurs, the agent knows WHAT was decided but not WHY or through
what chain of reasoning. Past mistakes in reasoning repeat because the reasoning itself
isn't persisted.

### What Exists Today
- Observations: title, narrative, type, concepts — the END RESULT of sessions
- LLM extraction: `internal/learning/extractor.go` extracts learnings from transcripts
- SDK processor: `internal/worker/sdk/processor.go` extracts observations from tool events
- Both capture results, neither captures reasoning chains

### What Cipher Does (Inspiration)
Cipher's System 2 memory stores reasoning traces with:
- Step types: thought, action, observation, decision, conclusion, reflection
- Quality score (0-1) via LLM evaluation
- Task context: goal, domain, complexity
- Separate "reflection vector store" for semantic search

### Engram Advantage
Engram already has: PostgreSQL + pgvector (storage), LLM extraction pipeline (analysis),
MCP tool consolidation (7 primary tools), closed-loop learning (effectiveness tracking).
Reasoning traces are an additive feature that leverages existing infrastructure.

## Functional Requirements

### FR-1: Reasoning Trace Data Model
The system must store reasoning traces with structured steps. Each trace must include:
- Session reference (which session produced this trace)
- Ordered steps with type classification (thought/action/observation/decision/conclusion)
- Quality score (0-1) based on LLM evaluation
- Task context (goal extracted from session, domain, estimated complexity)
- Embedding vector for semantic search

### FR-2: Reasoning Extraction from Tool Events
The SDK processor must detect reasoning patterns in tool events and extract structured
traces. Detection triggers when agent output shows multi-step reasoning (if→then→because,
considered→rejected→chose, investigated→found→concluded). Extraction uses LLM with
a reasoning-specific prompt.

### FR-3: Reasoning Quality Evaluation
Each extracted trace must be evaluated for quality. Evaluation criteria:
- Logical coherence (steps follow from each other)
- Evidence-based (decisions reference observations/facts)
- Completeness (has both analysis AND conclusion)
Quality score (0-1) stored with the trace for ranking.

### FR-4: Reasoning Retrieval via MCP Tool
The `recall` primary tool must support a new action `reasoning` that retrieves
relevant past reasoning chains by semantic similarity. Format: structured display
of steps with quality indicator.

### FR-5: Reasoning Context Injection
When the session-start or user-prompt hook detects a task similar to one with
stored reasoning traces, relevant traces should be injected as context.
This enables "here's how a similar problem was reasoned about before."

## Non-Functional Requirements

### NFR-1: Extraction Latency
Reasoning extraction must not add more than 2 seconds to observation processing.
Use async extraction (background, non-blocking) same as current observation pipeline.

### NFR-2: Storage Efficiency
Reasoning traces are verbose. Apply token budgeting: max 500 tokens per trace
(~2-3 sentences per step, max 10 steps). Compress longer traces.

### NFR-3: Quality Threshold
Only store traces with quality score ≥ 0.5. Below threshold = noise.

### NFR-4: No Tool Count Increase
Reasoning retrieval must be an ACTION of the existing `recall` tool, not a new tool.
Constitution #12: Tool Count Is a Budget.

## User Stories

### US1: Agent Recalls Past Reasoning (P1)
**As an** AI agent facing a familiar problem, **I want** to recall how a similar problem
was reasoned about before, **so that** I can follow proven reasoning patterns.

**Acceptance Criteria:**
- [ ] `recall(action="reasoning", query="Redis vs in-memory caching")` returns relevant traces
- [ ] Each trace shows step-by-step reasoning with types
- [ ] Results ranked by relevance + quality score

### US2: Reasoning Automatically Captured (P1)
**As a** system operator, **I want** reasoning chains captured automatically from sessions,
**so that** valuable reasoning is preserved without manual intervention.

**Acceptance Criteria:**
- [ ] Multi-step reasoning in tool events triggers trace extraction
- [ ] Extracted trace has correct step types and task context
- [ ] Quality score assigned via LLM evaluation

### US3: Reasoning Injected as Context (P2)
**As an** AI agent starting a new task, **I want** relevant past reasoning injected,
**so that** I can benefit from prior analytical work.

**Acceptance Criteria:**
- [ ] Context injection includes reasoning traces when task similarity > 0.7
- [ ] Traces displayed in structured format (not raw text)
- [ ] Max 2 traces injected per session to avoid overload

## Edge Cases

- Agent produces no multi-step reasoning (simple tool calls) — no trace extracted, no error
- Reasoning quality below threshold — trace discarded, logged as "low quality reasoning skipped"
- Multiple traces for same task type — most recent + highest quality selected
- LLM extraction fails (circuit breaker) — trace extraction skipped, observation processing continues
- Existing observations about same topic — reasoning traces stored separately, don't conflict

## Out of Scope

None — this is the complete System 2 implementation for v1.

## Dependencies

- Existing LLM extraction pipeline (internal/worker/sdk/processor.go)
- Existing pgvector infrastructure for semantic search
- Existing `recall` tool for retrieval action
- LLM availability for extraction and quality evaluation

## Success Criteria

- [ ] `recall(action="reasoning")` returns structured reasoning traces
- [ ] At least 10% of sessions with multi-step reasoning produce traces
- [ ] Quality scores correlate with reasoning usefulness (measured via feedback)
- [ ] No increase in default MCP tool count (action on existing `recall`)
- [ ] Extraction latency < 2s async (non-blocking)
