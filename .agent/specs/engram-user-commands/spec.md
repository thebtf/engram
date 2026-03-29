# Feature: Engram Plugin User Commands

**Slug:** engram-user-commands
**Created:** 2026-03-28
**Status:** Implemented
**Author:** AI Agent (reviewed by user)

## Overview

Add 4 user-invocable commands to the engram CC plugin. Currently only admin commands
exist (setup, doctor, restart). Users need commands that leverage engram's knowledge
for active work: analyzing session quality, reviewing memory health, curating
observations, and exporting data.

## Context

### Current Commands (3 — admin only)
- `/engram:setup` — configure URL + token
- `/engram:doctor` — diagnostic health check
- `/engram:restart` — restart server

### Gap
No commands for daily agent workflow. User explicitly requested: "хочется видеть
ещё что-то вроде engram:retro". Agents have 7 MCP tools but no guided workflows
that combine multiple tools into coherent user-facing operations.

### Implementation Pattern
Commands are markdown files in `plugin/engram/commands/`. Each file contains
instructions that tell the agent which MCP tools to call and how to format results.
No code compilation needed — the agent reads the markdown and follows instructions.

## Functional Requirements

### FR-1: `/engram:retro` — Session Retrospective
The command must analyze the current or most recent session's memory interaction:
- What observations were injected into context
- Which were used (referenced in agent responses) vs ignored
- Effectiveness scores of injected observations
- Session outcome (if recorded)
- Actionable recommendations: suppress ineffective observations, boost useful ones

Available data sources:
- `recall(action="get", id=N)` for observation details
- REST: `/api/sessions/{id}/injections` for injection records with effectiveness
- REST: `/api/learning/effectiveness-distribution` for system-wide effectiveness
- REST: `/api/learning/curve` for learning trend over time

### FR-2: `/engram:stats` — Memory Statistics
The command must display personal memory analytics:
- Total observations, by type distribution, by scope (project/global)
- Effectiveness distribution (high/medium/low/insufficient)
- Learning curve trend (improving/stable/declining)
- Search analytics: most common queries, miss rate
- Top 5 most-retrieved observations (most valuable knowledge)
- Top 5 lowest-quality observations (candidates for cleanup)

Available data sources:
- `admin(action="stats")` — system overview
- `admin(action="trends")` — temporal patterns
- `admin(action="quality")` — data quality report
- REST: `/api/learning/effectiveness-distribution`
- REST: `/api/learning/curve`
- REST: `/api/search/analytics`

### FR-3: `/engram:cleanup` — Memory Curation
The command must guide the user through observation quality review:
- Fetch lowest-quality observations (quality score < 0.5)
- For each: show title, type, age, quality score, improvement suggestions
- Ask user action per observation: keep, suppress, edit, merge
- Execute chosen action via MCP tools
- Report summary: N reviewed, M suppressed, K edited

Available data sources:
- `admin(action="quality")` — quality report with low-quality items
- `admin(action="consolidations")` — merge candidates
- `recall(action="get", id=N)` — full observation details
- `feedback(action="suppress", id=N)` — suppress
- `store(action="edit", id=N, ...)` — edit
- `store(action="merge", source_id=N, target_id=M)` — merge

### FR-4: `/engram:export` — Export Observations
The command must export observations in human-readable format:
- Prompt for scope: all, project-only, type filter, concept filter
- Export as markdown (default), JSON, or JSONL
- Save to file or output to console
- Include metadata: type, scope, concepts, importance score

Available data sources:
- `admin(action="export", project="...", format="markdown")` — export tool

## Non-Functional Requirements

### NFR-1: Non-Blocking (Constitution #3)
Commands must not block the host application. All HTTP calls within commands
have implicit timeouts via the MCP tool layer.

### NFR-2: No Code Compilation
Commands are markdown instruction files — no Go or TypeScript compilation required.
Adding a command = adding a .md file to `plugin/engram/commands/`.

### NFR-3: Consolidated Tool API
All commands must use the new consolidated tool names (recall, store, feedback,
admin, vault, docs) not legacy names. This validates the v2.1.0 tool consolidation.

## User Stories

### US1: Session Retrospective (P1)
**As a** developer finishing a session, **I want** to see which memories helped and
which didn't, **so that** I can improve memory quality over time.

**Acceptance Criteria:**
- [ ] `/engram:retro` shows list of injected observations with effectiveness
- [ ] Shows "used" vs "ignored" status for each
- [ ] Provides actionable recommendation (suppress/boost)

### US2: Memory Health Dashboard (P1)
**As a** developer, **I want** to see memory system health at a glance,
**so that** I know if engram is learning effectively.

**Acceptance Criteria:**
- [ ] `/engram:stats` shows observation count by type
- [ ] Shows effectiveness distribution
- [ ] Shows learning curve trend

### US3: Memory Cleanup (P2)
**As a** developer, **I want** to review and curate low-quality observations,
**so that** the memory system stays lean and accurate.

**Acceptance Criteria:**
- [ ] `/engram:cleanup` presents low-quality observations one by one
- [ ] User can choose: keep, suppress, edit, merge
- [ ] Actions execute immediately via MCP tools
- [ ] Summary shows what was done

### US4: Data Export (P2)
**As a** developer, **I want** to export my observations,
**so that** I can back up, share, or analyze them externally.

**Acceptance Criteria:**
- [ ] `/engram:export` supports markdown, JSON, JSONL formats
- [ ] Supports project and type filters
- [ ] Output is complete and well-formatted

## Edge Cases

- No observations exist — show "No observations found" with suggestion to use engram
- No session injections (retro) — show "No injections recorded for this session"
- Server unreachable — show error from `check_system_health` and suggest `/engram:doctor`
- Export of 1000+ observations — warn about output size, suggest filters

## Out of Scope

None — all 4 commands fully specified.

## Dependencies

- MCP tools: recall, store, feedback, admin (all exist in v2.1.0)
- REST endpoints: /api/learning/*, /api/search/* (all exist)
- CC plugin command system (existing — doctor.md, restart.md, setup.md)

## Success Criteria

- [ ] 4 new command files in `plugin/engram/commands/`
- [ ] All 4 commands visible in CC `/engram:` autocomplete
- [ ] Each command produces formatted output when invoked
- [ ] Commands use consolidated tool names (not legacy)
