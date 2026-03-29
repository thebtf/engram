# Feature: Engram v2 — Universal Memory Infrastructure

**Slug:** engram-v2
**Created:** 2026-03-24
**Status:** Implemented
**Author:** AI Agent (reviewed by user)
**ADR:** ADR-001-universal-memory-infrastructure.md (same directory)

## Overview

Transform engram from observation storage for Claude Code into universal memory infrastructure for any AI agent. Four P0/P1 capabilities close the self-learning loop: always-inject rules ensure agents don't repeat corrected mistakes, PreCompact hook captures full session context before it's lost, PreToolUse hook surfaces file-specific knowledge before edits, and causal chain linking connects isolated events into learnable dialog sequences.

## Context

Engram v1.6.5 extracts observations (B_fewshot prompt, 6 categories) and injects via similarity search. Three critical gaps remain:
1. **Universal rules never reach agents** — similarity between "update llm.go" and "no hardcoded values" is too low. 33 observations returned for every query regardless of relevance.
2. **Full session context lost at compaction** — per-tool extraction misses causal chains (tried A → failed → user corrected → tried B → succeeded).
3. **File-specific knowledge not automatic** — `find_by_file` exists but agents must call it manually.

These gaps prevent the self-learning loop from closing: extraction improved but delivery is the bottleneck.

## Functional Requirements

### FR-1: Three-Tier Injection System
The system must support three injection tiers, each with independent query mechanisms:
- **always-inject**: Observations tagged for unconditional injection appear in EVERY session regardless of prompt similarity. Used for behavioral rules, security policies. **Bloat protection:** maximum 20 observations in always-inject tier. If more exist, ranked by importance score (highest first); lowest-ranked observations fall through to similarity tier. Configurable via `ENGRAM_ALWAYS_INJECT_LIMIT` (default: 20).
- **project-inject**: Observations scoped to a project appear in every prompt within that project without similarity matching. Same bloat protection: maximum 15 per project (configurable via `ENGRAM_PROJECT_INJECT_LIMIT`).
- **similarity-inject**: Current behavior — observations matched by cosine similarity to prompt text.

Tiers are rendered in priority order: always-inject first (highest priority), then project, then similarity. Quality-first within tier limits. If total context exceeds budget, similarity tier (lowest priority) truncates. Overflow from capped tiers participates in similarity matching normally.

### FR-2: PreCompact Full Session Extraction
Before context compaction, the system must capture the full session transcript and process it through server-side LLM extraction. This produces:
- Category-based observations from the full dialog (not per-tool fragments)
- Session retrospective (request/completed/learned/next_steps/outcome)
- Causal chain relations between events in the dialog
- USER_BEHAVIOR rules extracted from corrections visible in full context

The PreCompact extraction supplements (not replaces) per-tool extraction. Deduplication via semantic similarity prevents duplicate observations.

### FR-3: PreToolUse File-Context Injection
Before Edit or Write tool execution, the system must automatically query for observations related to the file being modified and inject them as system context. The agent sees known gotchas, decisions, and patterns for that file BEFORE making changes.

The injection must include:
- Observations with `files_modified` or `files_read` matching the target file
- Decisions about the module/package containing the file
- Known gotchas and behavioral rules relevant to the file's domain

### FR-4: Temporal Chain Linking
When an observation is created within a session, the system must create a `follows` relation to the previous observation in the same session. Ordering uses `sdk_session_id` + `prompt_number` (both already stored in DB).

### FR-5: Prompt-Observation Linking
Each observation extracted from a tool call must be linked to the user prompt that triggered that tool call via a `prompted_by` relation. Matching: same `sdk_session_id`, closest `prompt_number` less than or equal to observation's prompt_number.

### FR-6: Always-Inject Tag
Observations must support an `always_inject` boolean field (or tag). When set, the observation is included in tier-1 injection regardless of similarity score. Imported behavioral rules (feedback_*.md) should have this flag set automatically.

## Non-Functional Requirements

### NFR-1: Injection Latency
Three-tier injection (FR-1) must complete within 500ms total. Always-inject tier: < 50ms (cached DB query). Project-inject: < 100ms. Similarity: < 300ms (current).

### NFR-2: PreCompact Overhead
PreCompact hook (FR-2) must not block compaction for more than 30 seconds. If server-side extraction takes longer, the hook should fire-and-forget (async POST) and allow compaction to proceed.

### NFR-3: PreToolUse Latency
File-context injection (FR-3) must return within 200ms. If the query takes longer, return empty (graceful degradation, not blocking).

### NFR-4: Causal Link Cost
Temporal chain (FR-4) and prompt-observation linking (FR-5) must add < 10ms overhead per observation creation. Pure DB queries, no LLM calls.

### NFR-5: Backward Compatibility
All changes must be additive. Existing hooks, API endpoints, and MCP tools continue to work unchanged. New hooks are opt-in via plugin hooks.json.

## User Stories

### US1: Agent Doesn't Repeat Corrected Mistakes (P0)
**As a** user who corrected an agent to "use Tavily not WebFetch", **I want** that rule injected in ALL future sessions automatically, **so that** I never have to correct the same mistake twice.

**Acceptance Criteria:**
- [ ] Behavioral rules with `always_inject=true` appear in every session context
- [ ] Injection happens without similarity match to prompt
- [ ] Rules appear in `<user-behavior-rules>` block BEFORE technical observations
- [ ] Works across projects (global scope)

### US2: Full Session Captured Before Compaction (P0)
**As a** user running long sessions, **I want** the complete dialog sent to engram before context is compacted, **so that** causal chains and corrections from the full conversation are preserved.

**Acceptance Criteria:**
- [ ] PreCompact hook reads transcript JSONL and sends to server
- [ ] Server extracts observations using B_fewshot prompt
- [ ] Session retrospective generated (summary)
- [ ] Compaction proceeds regardless of extraction success (non-blocking)
- [ ] Duplicate observations prevented via semantic dedup

### US3: File-Specific Context Before Editing (P1)
**As an** agent editing handlers_context.go, **I want** to see known gotchas and past decisions about that file, **so that** I avoid known pitfalls.

**Acceptance Criteria:**
- [ ] PreToolUse hook fires for Edit and Write tools
- [ ] File path extracted from tool input
- [ ] Observations with matching files_modified/files_read returned
- [ ] Context appears as systemMessage (`<file-context>` XML block) before tool execution
- [ ] Returns empty within 200ms if no observations found

### US4: Events Connected in Dialog Sequence (P1)
**As a** future agent reviewing past sessions, **I want** observations linked in temporal order within a session, **so that** I can follow the causal chain of decisions and corrections.

**Acceptance Criteria:**
- [ ] New observation gets `follows` relation to previous in same session
- [ ] New observation gets `prompted_by` relation to triggering user prompt
- [ ] Relations created via DB query (< 10ms), no LLM
- [ ] Relations visible in knowledge graph (dashboard)

## Edge Cases

- PreCompact with no transcript_path in input: skip gracefully, log warning
- PreCompact with enormous transcript (>50MB): chunk at 50 messages per request (resolved in plan.md)
- PreToolUse for non-file tools (Bash, Grep): skip — only Edit/Write
- PreToolUse with file path outside project: skip
- Always-inject with 0 tagged observations: tier-1 block empty, tiers 2-3 work normally
- Temporal chain with first observation in session: no `follows` relation (no predecessor)
- Two observations with same prompt_number in session: both get `prompted_by` to same prompt
- Server unreachable during PreCompact: fire-and-forget, compaction proceeds
- Server unreachable during PreToolUse: return empty, tool proceeds normally

## Out of Scope

- OpenClaw memory plugin (separate spec — D8 in ADR)
- Document storage layer (D9 in ADR — future)
- Causal chain phases 3-4: error→fix and correction→corrected linking (P2, next iteration)
- Knowledge graph intentional links (`[[obs:1234]]` syntax) (P2)
- Consistency engine / self-healing (P2)
- Search acceleration / query cache (P3)
- Conversation persistence (P3)
- Re-benchmark with max_tokens: 4096 (separate task)

## Dependencies

- Claude Code hooks API supports PreCompact and PreToolUse events (verified via Context7)
- PreCompact hook receives `input.transcript_path` (NEEDS VERIFICATION — stop hook has it, PreCompact may too)
- PreToolUse hook receives tool input with file path (verified from docs)
- Engram server v1.6.5+ deployed with B_fewshot prompt and backfill session endpoint

## Success Criteria

- [ ] "33 memory matches" replaced by meaningful injection (always-inject + similarity)
- [ ] Behavioral rules appear in `<user-behavior-rules>` without similarity dependency
- [ ] At least one full session extracted via PreCompact with retrospective summary
- [ ] File-specific context surfaces automatically before Edit (not manual find_by_file)
- [ ] Knowledge graph shows temporal chains (follows relations) within sessions
- [ ] No regression in existing hooks (session-start, user-prompt, post-tool-use, stop)

## Clarifications

| # | Category | Question | Resolution | Date |
|---|----------|----------|------------|------|
| C1 | Integration | Does PreCompact input have transcript_path? | Testing empirically — pre-compact.js hook installed, will log input keys at next compaction. Fallback: locate JSONL via ctx.SessionID at known path. | 2026-03-24 |
| C2 | Non-Functional | Token budgets per injection tier? | Quality-first, not token-counting. No artificial limits. Tiers rendered in priority order. If total context is too large, similarity tier truncates (lowest priority). | 2026-03-24 |
| C3 | Domain/Data | always_inject as DB column or concept tag? | Concept tag `always-inject` — clean, consistent with existing tag system (user-preference, gotcha). No schema migration. Query via PostgreSQL array contains. | 2026-03-24 |
| C4 | Edge Cases | How to dedup PreCompact vs per-tool extraction? | Needs empirical modeling on real sessions. Defer to implementation — test with 3 sessions, measure duplicate rate, then decide threshold/strategy. | 2026-03-24 |
| C5 | Non-Functional | Always-inject bloat risk — unlimited tagged observations = context explosion | Capped at 20 observations (configurable). Overflow ranked by importance, lowest fall through to similarity tier. Periodic consolidation merges similar rules. Maintains quality without unbounded growth. | 2026-03-24 |

## Open Questions

C1 testing in progress — autocompact fired, checking hook output for transcript_path discovery.
