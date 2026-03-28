# Memory Cleanup

Interactive review and curation of low-quality observations. Helps keep the memory system lean and accurate.

## Instructions

### 1. Fetch Quality Report

Call:
```
Tool: admin(action="quality")
```

This returns observations sorted by quality score. Focus on observations with quality < 0.5.

### 2. Fetch Merge Candidates

Call:
```
Tool: admin(action="consolidations")
```

This returns groups of similar observations that could be merged.

### 3. Present Low-Quality Observations

For each low-quality observation (quality < 0.5), present to the user:

```
Observation #{id} — Quality: {score}/1.0
  Title:    {title}
  Type:     {type}
  Age:      {days} days
  Scope:    {scope}
  Injected: {injection_count} times
  Used:     {success_count} times

  Suggestions: {improvement_suggestions from quality report}

  Actions:
    [K] Keep as-is
    [S] Suppress (hide from search, reversible)
    [E] Edit (improve title/narrative)
    [M] Merge with similar observation
    [skip] Skip to next
```

Wait for user to choose an action.

### 4. Execute Actions

Based on user choice:

- **Keep**: No action, proceed to next
- **Suppress**: `feedback(action="suppress", id={id})`
- **Edit**: Ask user for updated title/narrative, then `store(action="edit", id={id}, title="...", narrative="...")`
- **Merge**: Show merge candidates for this observation. If user selects a target: `store(action="merge", source_id={id}, target_id={target})`
- **Skip**: Proceed to next

### 5. Present Merge Candidates

After individual review, show merge groups from step 2:

```
Merge Candidates (similar observations):
  Group 1:
    - #{id1}: "{title1}" (quality: {q1})
    - #{id2}: "{title2}" (quality: {q2})
    Similarity: {similarity}%

    [M] Merge (keep #{id1}, supersede #{id2})
    [skip] Skip
```

### 6. Report Summary

```
Engram Cleanup Results:
  Reviewed:     N observations
  Suppressed:   M
  Edited:       K
  Merged:       J
  Kept:         L

  Quality improvement: {before_avg} → {after_avg} (estimated)
```
