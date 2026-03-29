# Feature: Extraction Quality v2

**Slug:** extraction-quality-v2
**Created:** 2026-03-23
**Status:** Implemented
**Author:** AI Agent (reviewed by user)

## Overview

Redesign engram's observation extraction pipeline from a single vague prompt into a structured multi-pass system with category-based extraction, user behavior rule detection, session-level retrospective analysis, and full backfill capability. Transforms engram from "what happened technically" into "what happened + how the user works."

## Context

### Current state
- **Live extraction** (SDK processor): single LLM call per tool output, vague "be generous" prompt → produces ~80% garbage observations (PowerShell errors, tool mechanics, status transitions)
- **Backfill extraction**: chunk-based prompt with "high signal only" instruction → small models can't distinguish routine from important, chunks fragment debugging arcs
- **User behavior**: NOT extracted at all. 25 feedback_*.md files manually created across 6 projects — scattered, not searchable, not cross-project
- **Session summaries**: generated from last assistant message only, not from full session context
- **Patterns**: 16k+ with hardcoded 0.5 confidence, orphaned from deleted observations

### Evidence (from this session's research)
- 4 real sessions analyzed manually (806, 338, 527, 181 messages)
- 25 feedback memories catalogued across 6 projects
- Backfill engine gaps verified: no summary generation, no sdk_sessions creation, no retrospective
- Selective DB purge feasibility verified (preserve manual + credentials)
- 5564 JSONL session files available for backfill

### Research artifacts
- `.agent/specs/tech-debt-sprint/research-prompts.md` — prompt architecture
- `.agent/specs/tech-debt-sprint/research-user-behavior.md` — user behavior category
- `.agent/specs/tech-debt-sprint/research-db-reset.md` — DB reset feasibility

## Functional Requirements

### FR-1: Category-Based Chunk Extraction
The observation extraction prompt must use 6 explicit categories with concrete detection patterns instead of vague "high signal" instructions. Categories:
1. **DECISION** — agent or user chose between alternatives
2. **CORRECTION** — user told agent it was wrong about a fact or approach (in-session event)
3. **DEBUGGING ARC** — error → investigation → fix chain
4. **GOTCHA** — something behaved unexpectedly
5. **PATTERN** — reusable approach that worked well
6. **USER_BEHAVIOR** — extracted behavioral rule from a CORRECTION or repeated preference (output: TRIGGER→RULE→REASON). CORRECTION is the source event; USER_BEHAVIOR is the structured rule for future injection.

Each category must include "look for" detection patterns (textual signals the LLM should match). An explicit DO NOT EXTRACT list must filter noise (routine reads, commits, status checks, tool invocations without meaningful output).

### FR-2: User Behavior Rule Extraction
The system must extract user corrections and workflow preferences as structured behavioral rules with format:
- **TRIGGER**: specific situation (precise enough to serve as scope — avoids false positives in other contexts)
- **RULE**: what the user wants
- **REASON**: why

Trigger specificity IS the scoping mechanism. "Never SSH" is wrong; "To check engram logs → use HTTP API" is right. The extraction must guide the model to formulate triggers specific enough that they naturally limit to the correct context.

### FR-3: Session-Level Retrospective
After chunk extraction, a second LLM pass must process the full session holistically. Inputs: session metadata (project, duration, exchanges, commits, files modified) + already-extracted chunk observations + first and last 3 exchanges. Outputs:
- Session summary (request, completed, learned, next_steps, outcome)
- 0-2 session-level observations invisible at chunk level (direction changes, cascading failures, reversed decisions)

### FR-4: Live Extraction Upgrade
The live SDK processor must use the category-based prompt from FR-1, adapted for single-exchange analysis (per clarification C1). The current whitelist filter (Edit/Write + build/test) remains as pre-filter; the improved prompt replaces the "be generous" extraction logic.

### FR-5: Behavioral Rule Injection
Extracted user behavior rules must be injected into session context as a separate block (`<user-behavior-rules>`) with priority higher than technical observations. These are active steering rules, not passive context.

### FR-6: Backfill Engine — Summary Generation
The backfill pipeline must generate session summaries using the retrospective prompt (FR-3). Summaries must be stored in the existing summaries table and appear in the dashboard timeline.

### FR-7: Backfill Engine — SDK Session Records
The backfill pipeline must create sdk_sessions records for each processed session file. Fields populated from JSONL metadata: claude_session_id, project, started_at, completed_at, prompt_counter. This makes backfilled sessions visible on the Sessions dashboard page.

### FR-8: Feedback Memory Import
The system must provide a one-time import mechanism for existing feedback_*.md files (25 files across 6 projects) into engram observations with type=guidance, memory_type=preference, scope=global. After import, the feedback files serve as backup only; engram becomes the source of truth.

### FR-9: DB Selective Purge + Rebuild
The system must support a selective purge operation that:
1. Truncates derived tables (vectors, patterns, prompts, summaries, injection_log)
2. Deletes auto-extracted observations (preserves source_type=manual AND type=credential)
3. Triggers full backfill from JSONL session history
4. Vectors and patterns auto-regenerate from new observations

### FR-10: Narrative Constraints
All extraction prompts must enforce a maximum narrative length (150 words for chunk observations, 2 sentences for summary fields). Small models ramble without explicit limits.

## Non-Functional Requirements

### NFR-1: Small Model Compatibility
All prompts must produce correct output with 8B-14B parameter local models (via LM Studio). This means: explicit categories over vague instructions, concrete text patterns over abstract concepts, structured XML output, short examples.

### NFR-2: Latency Budget
Live extraction (FR-4) must complete within the existing 3-second timeout. Backfill has no latency constraint (batch processing, local models, $0 cost).

### NFR-3: Backward Compatibility
New observation categories (user_behavior) and summary format must be additive. Existing observations, search, and injection pipelines must continue working unchanged.

### NFR-4: Multilingual Detection
User behavior detection (FR-2) must work regardless of the language the user writes in. No regex-based language-specific patterns — LLM handles intent detection across languages.

## User Stories

### US1: Better Observations from Live Sessions (P1)
**As a** user whose agent sessions produce engram observations, **I want** the extraction to capture decisions, corrections, and debugging arcs instead of routine tool output, **so that** injected context in future sessions is actually useful.

**Acceptance Criteria:**
- [ ] Extraction uses 6 category-based prompt with "look for" patterns
- [ ] DO NOT EXTRACT list filters routine operations
- [ ] Narrative limited to 150 words
- [ ] Output tested with 8B model on 3 real session samples

### US2: Agent Learns My Preferences (P1)
**As a** user who repeatedly corrects agents on the same issues, **I want** my corrections to be automatically extracted and injected into future sessions, **so that** I don't have to teach the same lesson twice.

**Acceptance Criteria:**
- [ ] USER_BEHAVIOR category extracted from sessions
- [ ] Rules have specific triggers (not broad "never X")
- [ ] Rules injected as `<user-behavior-rules>` block
- [ ] Existing 25 feedback_*.md imported into engram

### US3: Session Summaries in Timeline (P2)
**As a** dashboard user, **I want** to see structured session summaries (request/completed/learned/next) in the timeline, **so that** I can quickly understand what each session accomplished.

**Acceptance Criteria:**
- [ ] Retrospective prompt generates summaries from full session context
- [ ] Summaries appear in dashboard timeline as SummaryCard
- [ ] Backfill generates summaries for historical sessions

### US4: Clean Rebuild from History (P2)
**As a** system operator, **I want** to purge garbage observations and rebuild from session history with improved extraction, **so that** the knowledge base reflects quality over quantity.

**Acceptance Criteria:**
- [ ] Selective purge preserves manual observations + credentials
- [ ] Backfill processes available JSONL session files
- [ ] SDK sessions created for each processed file
- [ ] Patterns re-detected from clean observations

## Edge Cases

- Session with 0 user messages (pure agent autopilot): skip USER_BEHAVIOR extraction, still extract technical observations
- Session shorter than 3 exchanges: skip entirely (already implemented)
- User correction in non-English language: LLM handles via intent, not string matching
- Trigger collision: two rules with overlapping triggers — LLM decides with context (time, project, situation). May reformulate both, one may win, one may become obsolete. No explicit collision detection — composite scoring + LLM judgment handles naturally.
- Backfill of corrupted JSONL: skip file, log error, continue with next
- Backfill of session already in DB: semantic dedup prevents duplicate observations (already implemented)
- Empty retrospective (session had no novel content): output `<session_observations/>` (0 observations, still generate summary)
- Model returns invalid XML: existing XML parser handles gracefully, falls back to empty result

## Out of Scope

- Real-time user behavior detection in hooks (deferred — extraction happens in LLM pipeline, not regex hooks)
- Feedback_*.md auto-deletion after import (keep as backup)
- Pattern system overhaul (separate spec — patterns will naturally improve from clean observations)
- ScoreBreakdown modal API fix (separate tech debt item)
- Dashboard UI for managing user behavior rules (future — rules are API/MCP managed)
- Multi-user behavior isolation (single-user system currently)

## Dependencies

- LM Studio with 8B+ model accessible via ENGRAM_LLM_URL (for local backfill)
- v1.5.2+ deployed (migration 046+047 applied)
- 5564 JSONL session files in ~/.claude/projects/

## Success Criteria

- [ ] Live extraction produces <30% garbage (vs current ~80%)
- [ ] USER_BEHAVIOR rules extracted from at least 3 test sessions
- [ ] Session summaries appear in dashboard for backfilled sessions
- [ ] Selective purge + rebuild completes without data loss (manual + credentials preserved)
- [ ] 25 feedback_*.md successfully imported into engram
- [ ] Agent in fresh session receives behavioral rules from previous corrections

## Clarifications

| # | Category | Question | Resolution | Date |
|---|----------|----------|------------|------|
| C1 | Functional | Live extraction: chunk-based or single-exchange? | Adapt category prompt for single-exchange. Categories work at any granularity. Session-level patterns caught by retrospective pass. | 2026-03-23 |
| C2 | Integration | Who injects `<user-behavior-rules>`, how? | Same `user-prompt.js` hook. Query engram for type=user_behavior BEFORE technical observations. Inject as separate block BEFORE `<engram-context>`. Not subject to diversity penalty or LLM filter — always injected if matched. | 2026-03-23 |
| C3 | Domain/Data | Import feedback_*.md as-is or re-process? | Re-process through LLM to produce consistent TRIGGER→RULE→REASON format with specific triggers. Original files kept as backup. | 2026-03-23 |
| C4 | Completion | What constitutes "tested" for 8B model? | Manual review by user. Run extraction on 3 sessions, present results, user judges category accuracy and noise filtering. Pass/fail per session. | 2026-03-23 |
| C5 | Edge Cases | How to handle trigger collision between rules? | Let LLM decide with full context: time, projects, specific situation. Conflicting rules may need reformulation (both updated to be more specific), one may win, one may become obsolete. Like Codex blocker — true yesterday, false today. Don't build collision detection — composite scoring + LLM judgment handles it naturally. | 2026-03-23 |

## Open Questions

None — all research completed and clarifications resolved.
