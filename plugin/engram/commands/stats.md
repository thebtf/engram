# Memory Statistics

Display engram memory system health and analytics at a glance.

## Instructions

### 1. System Overview

Call:
```
Tool: admin(action="stats")
```

Display:
```
Memory System Overview:
  Total observations:  {count}
  Active:              {active_count}
  Suppressed:          {suppressed_count}
  Archived:            {archived_count}
  Storage:             {storage_size}
```

### 2. Type Distribution

From the stats response, show observation breakdown by type:

```
Type Distribution:
  decision:    N (X%)
  bugfix:      N (X%)
  feature:     N (X%)
  guidance:    N (X%)
  discovery:   N (X%)
  refactor:    N (X%)
  change:      N (X%)
  other:       N (X%)
```

### 3. Effectiveness Distribution

Fetch:
```bash
curl -s -H "Authorization: Bearer ${ENGRAM_API_TOKEN}" \
  "${ENGRAM_URL%/mcp}/api/learning/effectiveness-distribution"
```

Display:
```
Effectiveness (closed-loop learning):
  High (>70%):     N observations
  Medium (40-70%): N observations
  Low (<40%):      N observations
  Insufficient:    N observations (need more injection data)
```

### 4. Learning Curve

Fetch:
```bash
curl -s -H "Authorization: Bearer ${ENGRAM_API_TOKEN}" \
  "${ENGRAM_URL%/mcp}/api/learning/curve"
```

Show daily trend for last 7 days:
```
Learning Curve (last 7 days):
  Day 1: {avg_effectiveness}% ({sessions} sessions)
  Day 2: {avg_effectiveness}% ({sessions} sessions)
  ...
  Trend: [improving ↑ / stable → / declining ↓]
```

### 5. Search Analytics

Fetch:
```bash
curl -s -H "Authorization: Bearer ${ENGRAM_API_TOKEN}" \
  "${ENGRAM_URL%/mcp}/api/search/analytics"
```

Display:
```
Search Analytics:
  Total searches:    N
  Avg results:       M
  Miss rate:         X%
  Top queries:       [list top 5]
```

### 6. Quality Snapshot

Call:
```
Tool: admin(action="quality")
```

Show top 5 highest-quality and top 5 lowest-quality observations:

```
Top Quality:
  1. [decision] "Use PostgreSQL + pgvector" — quality: 0.95
  2. ...

Needs Attention:
  1. [guidance] "Fix the auth" — quality: 0.23 — suggestion: add context
  2. ...
```

### 7. Report Summary

```
Engram Stats:
- Observations: {total} ({active} active)
- Effectiveness: {high_pct}% high, {med_pct}% medium, {low_pct}% low
- Learning: {trend}
- Quality: {avg_quality}/1.0
```
