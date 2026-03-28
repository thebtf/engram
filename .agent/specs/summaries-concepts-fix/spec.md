# Feature: Fix Summaries & Concepts Pipeline

**Slug:** summaries-concepts-fix
**Created:** 2026-03-28
**Status:** Draft
**Audit:** .agent/reports/summaries-concepts-audit-2026-03-28.md

## Overview

Fix 3 root causes identified in audit: summaries never generated (cascading CB failure),
concepts always empty on new observations (prompt-filter mismatch), concepts empty on
historical observations (need backfill).

## Functional Requirements

### FR-1: Summary userPrompt Fallback
ProcessSummary must use the session's userPrompt as last-resort content when both
lastAssistantMsg and observation fallback return nothing.

### FR-2: Add Valid Concepts to Extraction Prompt
The SDK extraction system prompt must list all valid concept names so the LLM uses them.
Fix the example that shows an invalid concept name.

### FR-3: Concept Backfill Migration
Add a database migration that assigns concepts to existing observations based on
keyword matching in title and narrative fields.

## Success Criteria

- [ ] New session summaries generated after session-start trigger
- [ ] New extracted observations have semantic concepts (how-it-works, security, etc.)
- [ ] Historical observations gain concepts via migration
