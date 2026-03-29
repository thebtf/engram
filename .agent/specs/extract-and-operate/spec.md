# Feature: LLM-Driven Memory Extraction (extract_and_operate)

**Slug:** extract-and-operate
**Created:** 2026-03-29
**Status:** Draft
**ADR:** .agent/arch/decisions/ADR-005-llm-driven-memory-extraction.md

## Overview

Add `store(action="extract")` that accepts raw content and uses LLM to autonomously
extract, classify, and store observations — replacing the manual "agent formats content,
then calls store_memory" workflow.

## Context

Currently agents must explicitly call `store(content="...", title="...")` with
pre-formatted observation content. The SDK processor extracts observations from tool
events automatically, but there's no way for an agent to trigger intelligent extraction
on arbitrary content (e.g., paste a conversation transcript, design doc, or meeting notes).

Cipher's `extract_and_operate_memory` tool does this: raw content → LLM analysis →
extract N observations → dedup check → store/update/delete → return summary.

## Functional Requirements

### FR-1: New Action on Store Tool
Add `action="extract"` to the `store` primary tool. Input: raw content string.
Output: summary of what was extracted and stored.

### FR-2: LLM-Based Extraction
The action must use LLM to analyze raw content and extract 0-N observations,
each with: type, title, narrative, concepts. Uses the existing extraction prompt
pattern from `internal/learning/prompts.go`.

### FR-3: Dedup Before Store
Before storing each extracted observation, check for semantic duplicates using
vector similarity (threshold 0.85). Skip duplicates, log as "already known."

### FR-4: Return Summary
Return structured result: `{ extracted: N, stored: M, duplicates: K }` plus
titles of stored observations.

## Non-Functional Requirements

### NFR-1: No New Tools
Must be an action on existing `store` tool. Constitution #12.

### NFR-2: Token Budget
Max input content: 8000 tokens (~4000 words). Truncate longer content.

## User Stories

### US1: Agent Extracts from Raw Content (P1)
**As an** AI agent with unstructured content, **I want** to extract memories
in one call, **so that** I don't need to manually format each observation.

**Acceptance Criteria:**
- [ ] `store(action="extract", content="long text...")` extracts observations
- [ ] Each observation has type, title, narrative, concepts
- [ ] Duplicates detected and skipped
- [ ] Summary returned with counts

## Edge Cases

- Empty content → return `{ extracted: 0, stored: 0 }`, no LLM call
- Content too short (<50 chars) → return "content too short for extraction"
- LLM returns 0 observations → return `{ extracted: 0 }`
- All observations are duplicates → return `{ extracted: N, stored: 0, duplicates: N }`

## Out of Scope

None.

## Success Criteria

- [ ] `store(action="extract")` works via MCP
- [ ] Extracts 1+ observations from meaningful content
- [ ] Dedup prevents duplicate storage
- [ ] No new MCP tool created
