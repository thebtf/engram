# Feature: Closed-Loop Self-Learning (Agent Lightning Integration)

**Slug:** closed-loop-learning
**Created:** 2026-03-27
**Status:** Draft
**Author:** AI Agent (reviewed by user)
**Inspiration:** microsoft/agent-lightning — RL/APO/SFT training framework for AI agents

## Overview

Transform engram from a passive knowledge store (record → inject → hope it helps) into an active learning system with a closed feedback loop (record → inject → measure outcome → adjust what/how to inject). Inspired by Agent Lightning's rollout→reward→optimize cycle, adapted to engram's architecture of observation-based memory for coding agents.

## Context

### Current State (engram v1.8)
Engram records observations, injects them into agent context, and has basic scoring:
- **ImportanceScore** = type_weight × recency_decay + feedback + concept + retrieval + utility
- **UserFeedback** via `rate_memory` tool (+1/-1), but agents rarely call it
- **RetrievalCount** tracks how often an observation is retrieved, but not whether it helped
- **InjectionCount** tracks how often injected, but not the outcome of that session
- **Session status** exists (active/completed/failed) but is not connected to observation scoring
- **Self-learning spec** (14/24 implemented): guidance extraction, pattern detection, feedback tracking — all exist but the loop is open

### Gap (Agent Lightning Comparison)
| Agent Lightning | Engram | Missing |
|----------------|--------|---------|
| Rollout = full action→observation→reward trace | Observation = extracted fact, no action context | Action-outcome binding |
| Explicit reward function per task | ImportanceScore (heuristic, no task outcome) | Outcome-driven scoring |
| APO: text gradient optimizes prompts | Static injection (top-N by score) | Injection strategy optimization |
| Per-agent optimization | Shared memory, no per-agent tuning | Agent-specific adaptation |
| Training loop: collect→evaluate→update | No loop: collect→inject→? | Feedback signal propagation |

### What This Spec Adds
A three-phase closed loop:
1. **Outcome Tracking** — connect session outcomes to injected observations
2. **Reward Signal Propagation** — use outcomes to adjust observation scores
3. **Injection Strategy Optimization** — use accumulated reward data to improve what gets injected

## Functional Requirements

### FR-1: Session Outcome Recording
Record the outcome of each agent session as a structured signal.
- When a session ends (Stop hook or session close), record outcome type: `success`, `partial`, `failure`, `abandoned`
- Outcome signals derived from existing session data (no new counters):
  - `success`: session has ≥1 observation with type `bugfix` or `feature`, or git commits detected by hook
  - `partial`: session has observations stored but no commit-type activity
  - `failure`: session's last 3+ tool calls returned errors (stop hook detects consecutive failures)
  - `abandoned`: session ends with no stored observations and no meaningful tool activity
- Store outcome on `sdk_sessions` table (new column `outcome`)
- Outcome is heuristic — agents or users can override via MCP tool `set_session_outcome(session_id, outcome, reason)`

### FR-2: Injection-Outcome Binding
Link each injected observation to the session outcome it participated in.
- When context injection runs, record which observation IDs were injected into which session (already exists: `injection_count`, `mark-injected` endpoint)
- New: lightweight append-only junction table `observation_injections(observation_id, session_id, injected_at)` with 90-day TTL auto-cleanup. Write is a single batch INSERT per injection (fire-and-forget, same as mark-injected).
- When session outcome is recorded, all observations injected into that session receive an outcome signal
- This creates the rollout equivalent: "observation X was injected → session outcome was Y"

### FR-3: Outcome-Based Score Adjustment
Propagate session outcomes back to observation scores.
- After session outcome is recorded, for each observation injected in that session:
  - `success` → +0.02 to `utility_score` (capped at 1.0)
  - `partial` → +0.005 to `utility_score`
  - `failure` → -0.01 to `utility_score` (floored at 0.0)
  - `abandoned` → no change (insufficient signal)
- Position-weighted adjustment (not uniform across all injected observations):
  - `always_inject` (behavioral rules): 1.0x weight
  - `recent` section: 0.8x weight
  - `relevant` section: 0.5x weight
  - Explicitly cited by agent (referenced in tool output): 2.0x bonus
- Apply echo chamber prevention: max ±0.05 per session per observation (Constitution NFR6 from self-learning spec)
- Adjustment is cumulative across sessions: observations consistently injected in successful sessions rise; those in failed sessions sink
- UtilityScore already feeds into ImportanceScore via `UtilityContrib = (utility_score - 0.5) × weight`

### FR-4: Effectiveness Ranking
Rank observations by proven effectiveness, not just relevance.
- New metric: `effectiveness_score` = success_rate × injection_count (among sessions where this observation was injected, what fraction succeeded?)
- Expose via API: `GET /api/observations/{id}/effectiveness` returns `{ injections: N, successes: M, effectiveness: M/N }`
- Dashboard: show effectiveness badge on observation cards (green/yellow/red based on score)
- Search results optionally weighted by effectiveness (configurable: relevance-only vs relevance+effectiveness blend)

### FR-5: Injection Strategy A/B Testing
Compare injection strategies to optimize what gets injected.
- Define injection strategies as named configurations:
  - `baseline`: current behavior (top-N by ImportanceScore)
  - `effectiveness-weighted`: blend ImportanceScore with effectiveness_score
  - `recency-boosted`: stronger temporal boost for recent observations
  - `diverse`: ensure concept diversity in injected set (avoid N observations about same topic)
- Assign strategy per session (round-robin or configurable)
- Record strategy used in session metadata
- After N sessions (configurable, default 50), compute average outcome per strategy
- Dashboard: strategy comparison view showing outcome rates per strategy

### FR-6: Agent-Specific Learning
Adapt injection to individual agent behavior patterns.
- Track which observations each agent_id uses vs ignores (already partially in place via injection tracking)
- Per-agent effectiveness scores: observation X has 80% success rate for agent A but 40% for agent B
- Inject uses agent-specific effectiveness when available, falls back to global effectiveness
- Store per-agent scores in `agent_observation_stats(agent_id, observation_id, injections, successes)`

### FR-7: Automatic Reward Signal from Hooks
Derive reward signals from observable events without requiring explicit agent calls.
All signals derived from **hook event metadata** (tool name, tool args, exit code, observation types created) — NEVER from transcript/message content (NFR-4 compliance).
- **Positive signals** (detected in post-tool-use / stop hooks via tool call metadata):
  - Git commits: post-tool-use hook sees `Bash` tool with `git commit` in command args
  - PR created/merged: post-tool-use hook sees `Bash` tool with `gh pr create`/`gh pr merge` in args
  - Tests passed: post-tool-use hook sees test runner command with exit code 0
  - Observations created: stop hook counts observations stored during session (from session API)
- **Negative signals** (detected from tool metadata and session state):
  - Same error repeated 3+ times: post-tool-use hook counts consecutive non-zero exit codes
  - Agent suppressed/resolved an injected observation: post-tool-use hook sees `suppress_memory`/`edit_observation` with status=resolved
  - Session ended with no commits after >30 minutes: stop hook checks session duration vs commit signal count
  - User explicitly corrected agent behavior: stop hook detects new guidance-type observations created during session (correction = new rule)
- Signal weights configurable per signal type
- Signals feed into FR-3 (outcome-based score adjustment) as fine-grained partial rewards

### FR-8: Automatic Prompt Optimization (APO-lite)
Optimize HOW observations are presented in context, not just WHICH ones are injected.
Engram controls the injection format through hooks (`session-start.js`, `user-prompt.js`) and server-side formatting. Three optimization surfaces:

**9a. Guidance Rephrasing**
- Guidance observations with low effectiveness (< 0.4 after 15+ injections) are candidates for rewrite
- LLM rewrites the guidance narrative: "This rule was injected 15 times but only helped in 4 sessions. Rewrite it to be more specific, actionable, and context-aware."
- Original observation preserved (version history); rewritten version becomes active injection text
- Rewrite runs asynchronously (maintenance task, not hot path)
- Validation gate: A/B test old vs new version for 20 sessions (50/50 split). Promote if new > original + 10% effectiveness. Revert if worse. Store both versions with `is_active` flag.
- Success metric: effectiveness_score improves after rewrite

**9b. Injection Format Optimization**
- Current format: `## N. [TYPE] Title [SCOPE]\nnarrative\nKey facts: ...`
- Test alternative formats: bullet-only, structured XML, priority-tagged, concise (title+facts, no narrative)
- Assigned per session (A/B test alongside FR-5 strategies)
- Measure: which format produces higher outcome rates

**9c. Context Section Structure**
- Current: `<engram-context>`, `<engram-guidance>`, `<relevant-memory>` — three fixed sections
- Test: merged single section vs current split vs priority-ordered (most effective first regardless of type)
- Measure: information utilization rate (are observations in later sections ignored because agents don't read that far?)

**9d. Observation Condensation**
- After N sessions, generate condensed versions of long observations (keep only facts that correlated with success)
- LLM summarizes: "From this 500-word observation, the agent consistently used facts A and B but never C. Condense to A+B only."
- Reduces token usage per injection while preserving effectiveness
- Store condensed version alongside original; use condensed for injection, original for search

### FR-9: Learning Dashboard
Visualize the learning loop effectiveness.
- **Observation health**: histogram of effectiveness_score distribution (how many observations are proven useful vs untested vs harmful)
- **Learning curve**: outcome rate over time (are sessions getting more successful as engram learns?)
- **Strategy comparison**: A/B test results (which injection strategy produces better outcomes)
- **Agent breakdown**: per-agent effectiveness (which agents benefit most from engram)
- **Stale knowledge**: observations with high injection count but declining effectiveness (knowledge that was useful but isn't anymore)

## Non-Functional Requirements

### NFR-1: Zero Overhead on Hot Path
Outcome recording and score adjustment happen asynchronously — NEVER on the injection hot path. Context injection latency must not increase.

### NFR-2: Gradual Learning
Score adjustments are small (±0.02 per session). No single session can dramatically change the knowledge base. Minimum 10 sessions of data before effectiveness_score is considered reliable.

### NFR-3: Backward Compatibility
With all learning features disabled (config flags), system behaves identically to v1.8. New columns default to neutral values. Injection strategy defaults to `baseline`.

### NFR-4: Privacy Preservation
Outcome signals are derived from metadata (commit count, error count) — never from transcript content. No session transcript is stored or analyzed for outcome determination beyond what hooks already capture.

### NFR-5: Multi-Workstation Consistency
All learning data stored server-side in PostgreSQL. Workstation A's session outcomes improve injection for Workstation B. No local-only learning state.

## User Stories

### US1: Observation Gets Smarter Over Time (P1)
**As a** developer using engram across sessions, **I want** observations that consistently help me to be prioritized, **so that** context injection improves automatically without manual curation.

**Acceptance Criteria:**
- [ ] After 20 successful sessions where observation X was injected, X's importance score is measurably higher than at start
- [ ] After 20 failed sessions where observation Y was injected, Y's importance score is measurably lower
- [ ] Score changes are gradual (±0.02 per session) and capped

### US2: Bad Knowledge Sinks (P1)
**As a** developer, **I want** observations that don't help (or actively mislead) to naturally lose priority, **so that** they stop polluting my context window.

**Acceptance Criteria:**
- [ ] Observation with effectiveness_score < 0.3 after 15+ injections is flagged in dashboard
- [ ] Dashboard shows "declining effectiveness" indicator for knowledge that was once useful
- [ ] `resolved` status can be suggested automatically for observations with very low effectiveness

### US3: See What's Working (P2)
**As a** developer browsing the dashboard, **I want** to see which observations actually help and which don't, **so that** I can manually curate if needed.

**Acceptance Criteria:**
- [ ] Effectiveness badge on observation cards (green ≥0.7, yellow ≥0.4, red <0.4, gray = insufficient data)
- [ ] Learning curve chart shows outcome rate trend over last 30 days
- [ ] Strategy comparison shows which injection strategy performs best

### US4: Agent-Specific Context (P3)
**As a** user running multiple agents (Claude Code, OpenClaw, AI Harbour), **I want** each agent to get context optimized for its behavior patterns, **so that** what works for one agent doesn't pollute another.

**Acceptance Criteria:**
- [ ] Agent A and Agent B can have different effectiveness scores for the same observation
- [ ] Injection uses agent-specific scores when available (10+ sessions of data)
- [ ] Falls back to global scores for new/rare agents

### US5: Knowledge Auto-Improves Its Phrasing (P2)
**As a** user, **I want** guidance observations that aren't working to be automatically rephrased to be more actionable, **so that** the system self-corrects without manual editing.

**Acceptance Criteria:**
- [ ] Guidance with effectiveness < 0.4 after 15+ injections gets flagged for rewrite
- [ ] LLM-generated rewrite preserves intent but improves specificity
- [ ] Original text preserved as version history
- [ ] If rewrite improves effectiveness by >10%, it becomes the active version
- [ ] If rewrite doesn't improve, system reverts to original

## Edge Cases

- Session with 0 injected observations → no outcome signal propagated (skip)
- Session outcome recorded twice → idempotent (last write wins)
- Observation deleted/suppressed after being injected → outcome still recorded (historical data)
- All observations converge to same utility_score → diversity mechanism ensures varied injection (FR-5 diverse strategy)
- New observations have no effectiveness data → treated as neutral (0.5) with exploration bonus
- Agent never seen before → falls back to global scores, no per-agent data

## Out of Scope

- **Text gradient APO**: Agent Lightning's text gradient approach computes loss gradients through the model — requires model access we don't have. FR-9 implements APO-lite: LLM-based rewrite of injection text, not gradient-based optimization.
- **Model fine-tuning (SFT/RL)**: Engram doesn't train models — it trains the knowledge base. Fine-tuning the LLM itself is out of scope.
- **Real-time reward during session**: Rewards are computed at session end, not during. Mid-session feedback is through existing `rate_memory` tool.
- **Cross-project learning transfer**: Learning is per-project. Transferring effectiveness data between projects is a future feature.

## Dependencies

- Existing `utility_score` field in scoring calculator (FR-3)
- Existing `injection_count` and `mark-injected` endpoint (FR-2)
- Existing `sdk_sessions` table with `status` column (FR-1)
- Existing Stop hook for session end extraction (FR-7)
- Existing pattern detection pipeline (compatible, not replaced)

## Success Criteria

- [ ] After 50 sessions on a project, injection quality measurably improves (higher outcome rate in sessions 40-50 vs 1-10)
- [ ] Effectiveness dashboard shows meaningful distribution (not all gray/untested)
- [ ] At least one injection strategy outperforms baseline in A/B testing
- [ ] No regression in injection latency (hot path stays <500ms)
- [ ] System works across multiple workstations with shared learning state

## Clarifications

| # | Category | Question | Resolution | Date |
|---|----------|----------|------------|------|
| C1 | Functional | How to measure error/success counts for outcome heuristics? | Derive from existing data: observation types (bugfix/feature = success), consecutive hook errors (failure). No new counters. | 2026-03-27 |
| C2 | Domain/Data | Per-observation counter vs junction table for injection tracking? | Junction table `observation_injections` — required for rollout binding. Append-only, 90-day TTL, batch INSERT. | 2026-03-27 |
| C3 | Non-Functional | Should APO rewrites be validated before deployment? | Yes — A/B test old vs new for 20 sessions. Promote if +10% effectiveness. Revert if worse. | 2026-03-27 |
| C4 | Edge Cases | Uniform vs weighted score adjustment across 50+ injected observations? | Position-weighted: always_inject=1.0x, recent=0.8x, relevant=0.5x. Agent-cited observations get 2.0x bonus. | 2026-03-27 |
| C5 | Integration | Is "tool output reuse" detection feasible? | Deferred to v2. Too complex for marginal value. Stick to git/PR/test/error signals for v1. | 2026-03-27 |
