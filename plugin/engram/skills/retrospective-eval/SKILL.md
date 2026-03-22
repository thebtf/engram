---
name: retrospective-eval
description: Retrospective evaluation of engram observations — rate global usefulness and project relevance to improve memory quality over time
---

# Retrospective Evaluation Skill

## Purpose

Evaluate the usefulness of observations that were recently injected into your context.
This creates a feedback loop: observations you find helpful get boosted; irrelevant ones get demoted or suppressed.

## When to Use

- Periodically (daily/weekly) to review recent observations
- After a session where injected context was particularly helpful or unhelpful
- When explicitly asked to evaluate memory quality

## Process

### Step 1: Get Recent Observations

Fetch observations to evaluate. Choose one source:

**Option A — From injected context:**
Review the `<engram-context>` block in your current session. These are the observations that were injected for you.

**Option B — From API (broader review):**
```
GET /api/observations/recently-injected?limit=20&project={current_project}
```
or
```
GET /api/observations/top?limit=20&project={current_project}
```

### Step 2: Evaluate Each Observation

For each observation, assess TWO dimensions:

| Dimension | Scale | Question |
|-----------|-------|----------|
| **Global Usefulness** | 0-10 | Is this observation universally valuable across ALL projects? |
| **Project Relevance** | 0-10 | Is this observation relevant to the CURRENT project? |

### Step 3: Map Verdicts to Actions

| Global | Project | Verdict | Action |
|--------|---------|---------|--------|
| >= 7 | any | `keep(global)` | Set scope=global, boost importance |
| < 7 | >= 7 | `keep(project)` | Keep scope=project, boost importance |
| 3-6 | 3-6 | `demote` | Lower importance score |
| < 3 | < 3 | `suppress` | Mark as suppressed (excluded from injection) |

### Step 4: Apply Actions via MCP Tools

For each verdict, call the appropriate engram MCP tool:

**Boost (keep):**
```
rate_memory(id=<observation_id>, rating="useful")
```

**Demote:**
```
rate_memory(id=<observation_id>, rating="not_useful")
```

**Suppress:**
```
suppress_memory(id=<observation_id>)
```

### Step 5: Report Summary

Output a table of evaluations:

```markdown
| ID | Title | Type | Global | Project | Verdict |
|----|-------|------|--------|---------|---------|
| 123 | Auth middleware pattern | decision | 8 | 9 | keep(global) |
| 456 | npm install completion | discovery | 1 | 1 | suppress |
| 789 | Redis cache config | bugfix | 5 | 7 | keep(project) |
```

## Evaluation Guidelines

**High global usefulness (7-10):**
- Architecture decisions that affect multiple projects
- Security patterns and gotchas
- Cross-project conventions and standards
- Bug patterns that recur across codebases

**High project relevance (7-10):**
- Project-specific decisions and trade-offs
- Bug fixes in the current codebase
- Configuration choices for this project
- Domain-specific patterns

**Low value (0-3) — candidates for suppression:**
- Tool discovery mechanics ("ToolSearch Query Pattern")
- Generic status transitions
- Error messages from other projects
- Trivially re-discoverable facts
- Observations about tools that are always available

## Notes

- Do NOT evaluate observations from the current session (too fresh)
- Focus on observations older than 1 day for meaningful assessment
- If unsure, default to `demote` rather than `suppress` (suppression is harder to undo)
- Run this evaluation weekly for best results
