# Feature: Server-Side Summarizer + Investigate Fixes

**Slug:** server-summarizer-and-fixes
**Created:** 2026-03-29
**Status:** Draft
**Audit:** .agent/reports/investigate-summaries-pipeline-*.md

## Overview

Replace client-triggered summarization (broken due to CC bug #19225) with a server-side
periodic summarizer. Also fix 3 P1 bugs from the investigate report.

## Context

Investigation confirmed: summary pipeline WORKS when triggered manually (session #67376
generated summary from observations). The problem is trigger architecture — both client
paths are broken (stop hook doesn't fire, session-start workaround has bugs).

## Functional Requirements

### FR-1: Server-Side Periodic Summarizer
Add a periodic task to the maintenance service that scans for sessions with prompts > 0
and no corresponding summary. For each unsummarized session (capped at 3 per cycle),
call ProcessSummary. Must not re-summarize sessions that already have summaries.

Query: sessions WHERE prompt_counter > 0 AND NOT EXISTS summary AND
started_at older than 30 minutes (avoid active sessions).

### FR-2: Fix Pre-Edit Guardrails Warning Accuracy
Remove `guidance` from warningTypes in pre-tool-use.js. Only `bugfix` observations
and concept-based matches (anti-pattern, gotcha, security, error-handling) should be warnings.
Guidance observations are behavioral rules that apply globally, not file-specific warnings.

### FR-3: Fix Session-Start Summarizer Skip Check
The session-start.js summarizer checks `sess.summary` field which doesn't exist in
the sessions/list API response. Fix: either remove the client-side summarizer (replaced
by server-side FR-1) or fix the skip check to query session_summaries table.

### FR-4: Add Circuit Breaker Recovery Logging
Add log messages for CB transitions: open→half-open and half-open→closed (RecordSuccess).
Currently only "circuit breaker opened" is logged — recovery is silent.

## Non-Functional Requirements

### NFR-1: Summary Dedup
Server summarizer must be idempotent — running multiple times produces at most 1 summary per session.

### NFR-2: LLM Budget
Cap at 3 summaries per maintenance cycle to avoid LLM flooding.

## Success Criteria

- [ ] Sessions with prompts > 0 get summaries without any hook needing to fire
- [ ] Dashboard Summaries tab shows data after maintenance cycle
- [ ] Pre-edit warnings show only file-relevant bugfixes, not global guidance rules
- [ ] Server logs show "circuit breaker recovered" after CB closes
