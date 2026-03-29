# Implementation Plan: Engram Plugin User Commands

**Spec:** .agent/specs/engram-user-commands/spec.md
**Created:** 2026-03-28
**Status:** Draft

## Tech Stack

No compilation. Pure markdown files following existing pattern (doctor.md, restart.md).

## Architecture

Each command = a markdown file in `plugin/engram/commands/`.
The CC plugin system reads these files and the agent follows the instructions within.
Commands reference MCP tools by consolidated names (recall, store, feedback, admin).

## Phases

### Phase 1: Create 4 Command Files (all parallel)

| File | Command | Primary MCP Tools | REST Endpoints |
|------|---------|-------------------|----------------|
| `retro.md` | `/engram:retro` | recall, feedback | /api/sessions/{id}/injections, /api/learning/* |
| `stats.md` | `/engram:stats` | admin(action=stats/trends/quality) | /api/learning/*, /api/search/analytics |
| `cleanup.md` | `/engram:cleanup` | admin(action=quality/consolidations), feedback, store | — |
| `export.md` | `/engram:export` | admin(action=export) | — |

### Phase 2: Verify + Release

- Verify all 4 commands appear in `/engram:` autocomplete
- PR + review + merge
- Bump version

## Constitution Compliance

| Principle | Status |
|-----------|--------|
| #3 Non-Blocking | OK — MCP tools handle timeouts |
| #7 Version Bump | OK — plugin files changed |
| #12 Tool Budget | OK — commands use existing tools, no new tools |
