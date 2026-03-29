# Feature: Composite Relevance Scoring for Memory Retrieval

**Slug:** composite-relevance-scoring
**Created:** 2026-03-21
**Status:** Implemented
**Author:** AI Agent (reviewed by user)

## Overview

Replace raw vector similarity ranking with multi-signal composite scoring that accounts for recency, observation type, and importance. The goal: injected context should contain memories that **change agent behavior**, not just semantically similar text.

## Context

### Problem
Before v1.4.0, engram search ranked results purely by cosine similarity (pgvector). This caused ~85% of injected context to be irrelevant:
- Generic tool discoveries ("ToolSearch Query Pattern") dominated over project-specific decisions
- Old observations (weeks/months) competed equally with fresh ones
- All observation types ranked the same (a "Task Status Transition" = an "Architecture Decision")

### Evidence
- User audit (2026-03-20): 10 dashboard bugs, ~30% of injected context was garbage
- System health: 229K orphan vectors, 111K junk patterns, 189 observations (170 garbage)
- Injected `<relevant-memory>` blocks consistently contained cross-project noise and meta-observations

### Research
- **Deep Research** (Gemini, 36 sources): CrewAI, Mem0, Letta, Zep all use composite scoring
- **claude-mnemonic analysis**: upstream fork uses `0.5^(age/7)` recency + type weights + concept weights
- Report: `.agent/reports/deep-research-memory-retrieval-relevance-2026-03-21.md`

## Functional Requirements

### FR-1: Composite Score Computation
The system MUST re-rank search results using a composite score that combines multiple signals:
```
compositeScore = similarity × recencyDecay × typeWeight × importance × sourceBoost
```
Where `sourceBoost = 1.5` for SourceManual (explicit `store_memory`), `1.0` for all other SourceTypes (tool_verified, tool_read, web_fetch, llm_derived, instinct_import, backfill, unknown).
This replaces the raw similarity score for final ranking.

### FR-2: Recency Decay
The system MUST apply exponential time decay: `recencyDecay = 0.5^(age_days / half_life_days)`, floor at 0.05. See FR-6 for per-type half-life values.

### FR-3: Type-Based Weighting
The system MUST apply multipliers based on observation type to prioritize behaviorally impactful types.
- Decisions and bugfixes have higher weight (they constrain future behavior)
- Generic discoveries and changes have lower weight

### FR-4: Importance Floor
The system MUST apply a minimum importance score so that unscored observations (importance=0) are not penalized to zero.

### FR-5: Project Scoping (P1)
The system MUST filter vector search results by project before scoring. Observations from other projects MUST NOT appear in results unless scope = "global". This is the most fundamental missing piece — cross-project noise is the #1 pollution source.

### FR-6: Per-Type Recency Half-Life (P1)
The system MUST apply different decay rates per observation type:
- `store_memory` (explicit saves) → **no decay** (recencyDecay = 1.0 always). Explicit saves are permanent until suppressed/deleted.
- `decision`, `pattern` → 30 days half-life
- `bugfix`, `feature` → 14 days half-life
- `discovery`, `change`, `refactor` → 7 days half-life (current default)
- All other types (guidance, preference, style, habit, insight, context, unknown) → 7 days (default)
Agent MAY lower relevance via `rate_memory(id, "not_useful")` — observation stays in DB but scoring penalizes.

### FR-7: LLM Retrieval Filter (P2)
The system SHOULD add an LLM-based behavioral relevance filter after cross-encoder reranking. The LLM evaluates top-N candidates against the current task and project context:
- LLM evaluates top-15 candidates (after cross-encoder reranking and suppression filter, before injection)
- Prompt: "Given agent is working on [project] doing [task], which memories would change the agent's behavior? Return only relevant IDs."
- Cost: O(1) regardless of DB size (always evaluates 15). Local 8B LLM = ~$0 per query.
- Latency: +1-2s. 3s timeout → fallback to composite scoring only.
- If LLM returns empty set (all candidates rejected) → fallback to composite scoring top-5.
- **Note:** LLM filter runs SERVER-SIDE in `handleSearchByPrompt`, NOT in plugin hooks. Constitution §3 (Non-Blocking Hooks) does not apply.
- See: analysis-retrieval-approaches.md for full evaluation.

### FR-8: Injection Diversity Tracking (P2)
The system SHOULD track where each observation gets injected (per-project). Observations injected across many unrelated projects = generic = penalize. Observations injected consistently in one project = specific = boost.
- `injection_diversity = unique_projects / total_injections`
- High diversity (scope=project) → generic noise → score penalty
- Scope=global observations exempt from diversity penalty
- Zero human/agent feedback required — purely automatic implicit signal.
- Retention: 90 days. Cleanup in maintenance cycle alongside search_query_log.

### FR-9: Background LLM Consolidation (P3)
The system SHOULD periodically merge near-duplicate observations (similarity > 0.95, same project, same type). Safeguards: dry-run mode, approval flow, logging. Start with near-duplicates only — don't attempt "extract atomic facts" with 8B model.

### FR-10: Write-Time Supersession Detection (P3)
The system SHOULD detect contradictions at write time. When a new observation has high similarity (>0.9) to an existing one of the same type and project, mark the old one as `is_superseded`. Uses existing `is_superseded` field and `MarkAsSuperseded()` method — no new data model needed.

### FR-11: Retrospective Evaluation Skill (P2)
The system SHOULD provide a skill/tool that allows the model to retrospectively evaluate observation usefulness. Triggered manually or via periodic reminders (daily/weekly). Evaluates on TWO dimensions:
- **Global usefulness** (0-10): is this observation universally valuable across all projects?
- **Project relevance** (0-10): is this observation relevant to the specific project?

Verdicts map to scoring actions:
- `keep(global)`: scope=global, high importance → always inject
- `keep(project)`: scope=project, high importance → inject only in matching project
- `demote`: lower importance score → inject only if nothing better available
- `suppress`: is_suppressed=true → exclude from injection

Input: set of observations (from `<engram-context>` block, or last N observations, or all for a project).
Output: table of verdicts + batch API calls to update importance/scope/suppression.

This creates a feedback loop without requiring real-time assessment: model evaluates retrospectively based on what it actually did in the session.

### FR-12: Dashboard — Observation Feedback UI (P1)
The dashboard MUST show thumbs up/down buttons on each observation in timeline. Clicking calls `POST /api/observations/{id}/feedback` with `{feedback: 1|-1|0}`. Backend handler already exists in claude-mnemonic at `handlers_scoring.go:handleObservationFeedback`. Recalculates importance score immediately.

### FR-13: Dashboard — Importance Score Badge (P1)
The dashboard MUST show importance score as a numeric badge (e.g., "0.91") on each observation card. Clickable for score breakdown.

### FR-14: Dashboard — Key Facts Display (P1)
The dashboard MUST render `observation.facts` as a structured list with checkmark icons, not hidden in expandable panel.

### FR-15: Dashboard — Session Summaries (P2)
The dashboard SHOULD display session summaries in the timeline. Summary card shows: REQUEST, COMPLETED, LEARNED, NEXT STEPS sections. Backend `SummaryStore` and `SummaryCard` pattern exist in claude-mnemonic. Summaries generated via stop hook → LLM. Summary generation uses `ENGRAM_LLM_URL` (not Claude CLI).

### FR-16: Dashboard — Sidebar with System Health (P3)
The dashboard SHOULD add a collapsible sidebar showing: System Health (per-component status), Memory Contents (obs/prompts/summaries counts), Retrieval Stats, Worker Info. Pattern exists in claude-mnemonic `Sidebar.vue`.

## Non-Functional Requirements

### NFR-1: Retrieval Latency Budget
Full retrieval pipeline (hook → server → vector search → reranking → LLM filter → response) MUST complete within 3 seconds. If LLM filter (FR-7) exceeds budget → fallback to composite scoring only.

## Current Implementation (v1.4.0)

### Parameters (v1 — need tuning)

| Parameter | Value | Source | Confidence | Notes |
|-----------|-------|--------|------------|-------|
| Half-life | 7 days | claude-mnemonic | MEDIUM | Maybe too aggressive? Important decisions shouldn't decay in a week. Consider 14d or per-type half-life |
| Recency floor | 0.05 | Arbitrary | LOW | Prevents old observations from disappearing. Needs validation |
| decision weight | 1.4 | Deep Research (CrewAI) | MEDIUM | Close to claude-mnemonic's 1.1. We boosted more aggressively |
| bugfix weight | 1.3 | Deep Research | MEDIUM | |
| feature weight | 1.2 | Deep Research | MEDIUM | |
| pattern weight | 1.2 | Custom | LOW | Not in any reference system |
| discovery weight | 0.8 | Custom | LOW | Penalizes discoveries — but some discoveries are valuable |
| change weight | 0.7 | Custom | LOW | Most aggressive penalty — may be too harsh |
| refactor weight | 0.9 | Custom | LOW | |
| Importance floor | 0.3 | Arbitrary | LOW | Prevents zero-scored observations from being killed |
| Default similarity | 0.5 | Arbitrary | LOW | Used when no similarity score exists |

### Known Issues (post-clarification)

| # | Issue | Status |
|---|-------|--------|
| ~~1~~ | Half-life too aggressive | RESOLVED: per-type half-life (FR-6, C1) |
| ~~2~~ | No per-type half-life | RESOLVED: FR-6 |
| ~~3~~ | No project scoping | RESOLVED: FR-5 |
| 4 | No retrieval count boost | DEFERRED: addressed partially by injection diversity (FR-8) |
| ~~5~~ | No user feedback | RESOLVED: FR-11 retrospective eval + rate_memory |
| 6 | No concept weight boost | DEFERRED: future enhancement |
| 7 | Multiplicative formula may be fragile | MONITORING: evaluate after data collection |
| ~~8~~ | No store_memory boost | RESOLVED: sourceBoost in FR-1 formula |

## Rejected Alternatives

- **Additive formula** (`W_sim×S + W_rec×R + W_imp×I`): researched in Deep Research. More robust but harder to tune — requires normalized scales. Multiplicative is simpler for v1.
- **LLM-in-the-loop reranking (Letta-scale)**: Agent evaluates EACH candidate individually = O(N) LLM calls. Rejected. FR-7 uses a lighter approach: single LLM call evaluates batch of 15 candidates with 3s timeout fallback.
- **Disable scoring entirely**: Just clean up data (migrations 040-043). Insufficient — new garbage accumulates, and recency is genuinely important.

## Edge Cases

- Observation with importance=0 and age=30 days: score would be near-zero. Floor values prevent this but may still rank too low.
- Brand new observation (age=0): recencyDecay=1.0, full similarity. Correct behavior.
- store_memory with type="decision": gets typeWeight=1.4 + high importance. Should rank highest. Correct.
- SDK-extracted "change" with low importance: gets typeWeight=0.7 × importance≥0.3. Should rank low. Correct.

## Success Criteria

- [ ] Injected `<relevant-memory>` contains >50% behaviorally relevant observations (vs ~15% before)
- [ ] Recent decisions (< 7 days) rank above old generic discoveries
- [ ] Cross-project noise does not appear in injected context (requires project scoping fix separately)
- [ ] Agent performance in other sessions improves (qualitative user assessment)

## Clarifications

| # | Category | Question | Resolution | Date |
|---|----------|----------|------------|------|
| C1 | Functional | Per-type half-life or single? | Per-type: decision=30d, bugfix=14d, change=7d, store_memory=∞ | 2026-03-21 |
| C2 | Functional | store_memory after half-life expires? | No decay. Agent MAY lower relevance via rate_memory("not_useful") | 2026-03-21 |
| C3 | Domain/Data | Per-observation or per-project hit rate? | Per-project. Fallback to global if <3 injections for project | 2026-03-21 |
| C4 | Integration | Who/when reports feedback? | Retrospective evaluation skill (FR-11), not real-time | 2026-03-21 |
| C5 | Non-Functional | Latency budget? | 3 seconds max end-to-end. LLM filter fallback if exceeded | 2026-03-21 |
| C6 | Completion | How to measure success? | Manual spot-check + injection diversity + retrospective eval | 2026-03-21 |

## Open Questions

1. **[NEEDS DATA]** After deploy, measure: what % of injected observations are now useful? Adjust weights based on retrospective evaluation data.
2. **[PENDING RESEARCH]** Deep Research #2 (job 019d10ac) — concrete prompt templates for LLM retrieval filter (FR-7). Results pending.

## Dependencies

- v1.3.4: Whitelist SDK extraction (reduces garbage input)
- v1.3.5-v1.3.7: Garbage cleanup migrations (reduces garbage in DB)
- Project scoping: separate feature needed to boost same-project observations in vector search

## Next Steps

1. ~~Deploy v1.4.0~~ DONE (deployed, migration 043 ran)
2. **Implement Phase 1** (plan.md): per-type half-life, project scoping, sourceBoost, MCP tools
3. **Implement Phase 2**: LLM filter, injection tracking, retrospective eval skill
4. **Collect data**: run retrospective eval weekly, adjust weights based on verdicts
5. **Implement Phase 3**: background consolidation, write-time supersession
