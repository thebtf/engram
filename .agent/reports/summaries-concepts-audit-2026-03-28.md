# Audit Report: Summaries & Concepts Pipeline

**Date:** 2026-03-28
**Method:** Code audit + aimux scientific_method (inquiry-1774718270980)
**Status:** Root causes identified, solutions proposed

---

## Bug 1: Summaries = 0

### Chain
```
session-start.js → POST /api/sessions/{id}/summarize (empty lastAssistantMsg)
  → handleSummarize → sessionManager.QueueSummarize
  → ProcessSummary(sdkSessionID, "", "")
  → hasMeaningfulContent("") = false
  → [PR #120 fallback] query observations WHERE sdk_session_id = ?
  → 0 rows (observations extracted when circuit breaker was open → none stored)
  → skip summary
```

### Root Cause
**Cascading failure:** Circuit breaker open → LLM extraction fails silently → no observations stored for sessions → observation fallback finds nothing → summary skipped.

Even when CB closes, historical sessions have 0 observations → fallback still returns nothing.

### Fix
**Add user_prompt fallback** in `ProcessSummary` (processor.go:531). The `userPrompt` parameter is already passed but never used as fallback content. When both lastAssistantMsg AND observations are empty, use userPrompt:

```go
// After observation fallback (line 555), add:
if !hasMeaningfulContent(lastAssistantMsg) && userPrompt != "" {
    lastAssistantMsg = "Session started with: " + userPrompt
}
```

This gives the LLM at least the initial user request to summarize.

---

## Bug 2: Semantic Concepts = 0

### Chain
```
post-tool-use.js → POST /api/sessions/observations (tool event)
  → ProcessObservation → callLLM(systemPrompt, toolEventPrompt)
  → LLM generates <observation><concepts><concept>user-preference</concept></concepts>
  → ParseObservations → filterValidConcepts
  → "user-preference" NOT in validConcepts → FILTERED OUT
  → observation stored with concepts = []
```

### Root Cause
**Prompt-filter mismatch.** The extraction system prompt (`processor.go:1212-1257`) does NOT list valid concepts. The only example shows `<concept>user-preference</concept>` which isn't even in the `validConcepts` map (`parser.go:43-67`). The LLM has no guidance on which concept names to use, so it invents arbitrary ones that get filtered out.

Meanwhile, the learning extractor (`prompts.go`) HAS a proper concept list in its prompt — but that's a different code path (extract-learnings, not live SDK processing).

### Fix
**Add valid concept list to systemPrompt** in `processor.go` after the OUTPUT FORMAT section:

```
Valid concepts (use ONLY these in <concept> tags):
how-it-works, why-it-exists, what-changed, problem-solution, gotcha, pattern,
trade-off, best-practice, anti-pattern, architecture, security, performance,
testing, debugging, workflow, tooling, refactoring, api, database,
configuration, error-handling
```

Also fix the example: change `<concept>user-preference</concept>` to `<concept>workflow</concept>`.

---

## Bug 3: Historical Observations (1047) Have Empty Concepts

### Root Cause
All existing observations were extracted with the broken prompt → all have `concepts = []`.

### Fix
**Keyword-based backfill migration.** No LLM needed — match observation title/narrative against concept keywords:

| If title/narrative contains | Assign concept |
|----------------------------|----------------|
| architecture, design, pattern, structure | architecture |
| security, auth, token, CSRF, XSS | security |
| performance, latency, cache, timeout | performance |
| test, coverage, assert, mock | testing |
| debug, error, stack trace, fix | debugging |
| workflow, process, pipeline, CI/CD | workflow |
| API, endpoint, REST, GraphQL | api |
| database, SQL, migration, schema | database |
| config, env, setting, flag | configuration |

Add as migration in `migrations.go`.

---

## Summary of Fixes

| # | Bug | Fix | Complexity | File |
|---|-----|-----|-----------|------|
| 1 | Summaries empty | Add userPrompt fallback in ProcessSummary | T1 | `internal/worker/sdk/processor.go` |
| 2 | Concepts empty (new) | Add valid concept list to systemPrompt | T0 | `internal/worker/sdk/processor.go` |
| 3 | Concepts empty (historical) | Keyword-based backfill migration | T1 | `internal/db/gorm/migrations.go` |
