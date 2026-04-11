# Session Retrospective

Analyze what engram injected during this session, what was useful, and what should be improved.

## Instructions

### 1. Get Session Injection Data

First, determine the current session's DB ID. Call:

```
Tool: check_system_health()
```

Then fetch injected observations for this session using the REST API:

```bash
curl -s -H "Authorization: Bearer ${ENGRAM_API_TOKEN}" \
  "${ENGRAM_URL%/mcp}/api/sessions/${SESSION_ID}/injections"
```

If session ID is not available, use the `/api/sessions/list` endpoint to find the most recent session.

### 2. Display Injection Analysis

For each injected observation, show:

| # | Title | Type | Section | Effectiveness | Status |
|---|-------|------|---------|--------------|--------|
| 1 | {title} | {type} | {injection_section} | {effectiveness_score}% | {used/ignored} |

Group by injection section (always_inject, recent, relevant).

### 3. Effectiveness Summary

Fetch system-wide effectiveness:

```bash
curl -s -H "Authorization: Bearer ${ENGRAM_API_TOKEN}" \
  "${ENGRAM_URL%/mcp}/api/learning/effectiveness-distribution"
```

Display:
```
Session Effectiveness:
  High:         N observations (X%)
  Medium:       N observations (X%)
  Low:          N observations (X%)
  Insufficient: N observations (needs more data)

Learning Trend: [improving/stable/declining]
```

For learning trend, fetch:
```bash
curl -s -H "Authorization: Bearer ${ENGRAM_API_TOKEN}" \
  "${ENGRAM_URL%/mcp}/api/learning/curve"
```

### 4. Recommendations

Based on the analysis:
- **Suppress** observations with effectiveness < 30% and 10+ injections (consistently unhelpful)
- **Boost** observations referenced in agent responses (confirmed useful)
- **Note** observations with "insufficient data" — these need more sessions

For each recommendation, ask the user if they want to act:
- Suppress: `feedback(action="suppress", id=N)`
- Rate useful: `feedback(action="rate", id=N, rating="useful")`
- Rate not useful: `feedback(action="rate", id=N, rating="not_useful")`

### 5. Report Summary

```
Engram Retro Results:
- Observations injected: N
- Used by agent: M (X%)
- Ignored: K (Y%)
- High effectiveness: H
- Recommendations: R actions suggested
```
