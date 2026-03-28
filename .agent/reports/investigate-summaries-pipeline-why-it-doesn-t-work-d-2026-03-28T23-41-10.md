# Investigation Report: Summaries pipeline: why it doesn't work despite code being in place — architecture analysis

**Generated:** 2026-03-28T23:41:10.381Z
**Project:** D:\Dev\engram
**Iterations:** 1
**Findings:** 7
**Corrections:** 0
**Coverage:** 6/6 areas

## Findings

| ID | Severity | Description | Source | Iter | Status |
|----|----------|-------------|--------|------|--------|
| F-0-1 | P1 | ARCHITECTURE PROBLEM: Summary triggering has TWO paths, BOTH broken.

PATH 1 (stop.js:293): Posts to /sessions/{sessionID}/summarize with lastUser + lastAssistant from transcript parsing. This is the DESIGNED path. BUT: stop hook doesn't fire reliably (CC bug #19225). The stop.js is registered in global settings.json as workaround, but even then it may not fire on session end.

PATH 2 (session-start.js:223): Workaround — on new session start, summarize the most recent unsummarized session. Posts to /api/sessions/{sess.id}/summarize with EMPTY lastUserMessage/lastAssistant. BUT: (a) checks sess.summary field which doesn't exist in API response → skip check always false → would re-summarize repeatedly, (b) passes empty strings → ProcessSummary needs fallback chain to work.

NEITHER PATH produces summaries because: Path 1 never fires. Path 2 fires but passes no data, and the fallback chain (observations → userPrompt) fails because most sessions had circuit breaker open during extraction → 0 observations stored. | Read stop.js:275-299, session-start.js:203-235 | 0 | active |
| F-0-2 | P1 | DATA FLOW PROBLEM: The summarize endpoint (POST /sessions/{id}/summarize) accepts SummarizeRequest with lastUserMessage + lastAssistantMessage. Handler parses numeric session DB ID from URL, queues to sessionManager.QueueSummarize. Then service.go processQueue calls ProcessSummary with (sessionDBID, sdkSessionID, project, userPrompt, lastUserMsg, lastAssistantMsg).

The sdkSessionID and userPrompt come from the in-memory ActiveSession object. But sessions expire from memory after 30 minutes (CleanupInterval). So for OLD sessions triggered by session-start.js, InitializeSession re-creates from DB. The DB session has claude_session_id and user_prompt fields.

PROBLEM: user_prompt in sdk_sessions table is set during /api/sessions/init call from user-prompt.js hook. It stores the FIRST user prompt of the session. But many sessions have user_prompt = '' (empty) because the init call didn't include a prompt, or the prompt was filtered as internal/private.

So the fallback chain is: lastAssistantMsg(empty) → observations(0 due to CB) → userPrompt(empty from DB) → ALL FAIL → skip. | Read handlers_sessions.go:495-530, service.go:2343-2354, session/manager.go:275-310 | 0 | active |
| F-0-3 | P2 | PRODUCTION DATA: Sessions DO have user_prompt data (non-empty). Session #67376 has 9 prompts + user_prompt '/nvmd-platform:autopilot...'. Session #67421 has full subagent context. So the userPrompt fallback in ProcessSummary SHOULD have data to work with.

BUT: user_prompt is sql.NullString in Go, serialized as {String:'...',Valid:true} in JSON. In the SDKSession Go model, it's stored as sql.NullString. When ProcessSummary receives userPrompt param, it comes from sess.UserPrompt which is the NullString.String value (extracted by session manager). This should be the actual string content, not the JSON wrapper.

The REAL question: does the session-start summarizer actually FIRE? And when it fires, does it reach ProcessSummary with the right session? | curl /api/sessions/list production data | 0 | active |
| F-0-4 | P1 | CONFIRMED: Summary pipeline WORKS when triggered correctly. Manual POST /api/sessions/67376/summarize with empty lastUserMessage/lastAssistant succeeded — ProcessSummary's observation fallback found 10+ observations for this session, built content, LLM generated a real summary. Dashboard now shows 1 summary.

The ENTIRE problem is TRIGGER RELIABILITY:
1. stop.js: CC bug #19225 means stop hook rarely fires
2. session-start.js: fires but has bugs (sess.summary field doesn't exist, always picks same session)
3. No server-side trigger: engram server has NO autonomous mechanism to summarize sessions

The correct fix is NOT more client-side workarounds. It's a SERVER-SIDE scheduled summarizer — like the maintenance cycle but for summaries. The server knows which sessions have prompts > 0 and no summary. It should periodically summarize them without any hook needing to fire. | Manual test: POST /api/sessions/67376/summarize → success | 0 | active |
| F-0-5 | P1 | SESSION LIFECYCLE MISMATCH: CC sessions are ephemeral (start → work → end). Server sessions are persistent (DB records). The gap: server never knows when a CC session ENDS. It only knows when sessions START (via /api/sessions/init). There's no reliable 'session ended' signal because:
- stop.js: CC bug #19225, doesn't fire
- session timeout: server cleans up in-memory sessions after 30min, but this is memory cleanup, not a summarization trigger
- no heartbeat: CC doesn't send periodic status, so server can't detect inactive sessions

The DESIGN FLAW: summarization was designed as CLIENT-TRIGGERED (stop hook fires → sends transcript → server summarizes). When the client can't trigger reliably, the entire feature breaks.

CORRECT DESIGN: SERVER-TRIGGERED. Server should:
1. Periodically scan sdk_sessions for prompt_counter > 0 AND no matching summary in session_summaries
2. For each, call ProcessSummary (observation fallback works)
3. Mark as summarized to avoid re-processing

This is analogous to how maintenance already works — periodic server-side task, no client dependency. | Architecture analysis: client-server responsibility | 0 | active |
| F-0-6 | P2 | STORAGE: Summaries are stored in session_summaries table with fields: completed, request, investigated, learned, next_steps, notes, sdk_session_id, project. Dashboard fetches via GET /api/summaries. The data model is good — structured summary with sections.

LLM DEPENDENCY: ProcessSummary calls callLLM() which goes through circuit breaker. If CB is open, LLM call is skipped, summary fails. The observation fallback runs BEFORE the LLM call (gathering content), but the actual LLM generation is the bottleneck.

When LLM is available (as tested), summaries generate correctly from observation data alone (no transcript needed). This means a server-side summarizer would work reliably as long as LLM is up — it doesn't need stop.js or any client hook. | curl /api/summaries + ProcessSummary code | 0 | active |
| F-0-7 | P2 | STORAGE GAP: There's no link between sdk_sessions and session_summaries for 'has summary' checks. session_summaries.sdk_session_id = sdk_sessions.claude_session_id (string match). To check if a session has a summary, you need a JOIN or subquery. The session-start.js workaround tried to check sess.summary field on the session list response — but that field doesn't exist because summaries are in a separate table.

For a server-side summarizer, the query would be: SELECT s.* FROM sdk_sessions s WHERE s.prompt_counter > 0 AND NOT EXISTS (SELECT 1 FROM session_summaries ss WHERE ss.sdk_session_id = s.claude_session_id) AND s.started_at_epoch < (now - 30min). The 30min buffer ensures we don't summarize active sessions. | DB schema analysis: sdk_sessions vs session_summaries | 0 | active |

## Coverage Map

| Area | Status |
|------|--------|
| Architecture: who is responsible for triggering summarization | ✓ checked |
| Data flow: what data reaches the summarize endpoint | ✓ checked |
| Session lifecycle: when sessions start/end in CC vs server | ✓ checked |
| LLM dependency: what happens when LLM unavailable | ✓ checked |
| Storage: where summaries go, how dashboard reads them | ✓ checked |
| Production state: actual DB data for real sessions | ✓ checked |

## Convergence History

- Iteration 1: 50%