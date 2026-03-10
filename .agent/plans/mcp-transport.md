# Implementation Plan: MCP Transport (SSE + Streamable HTTP)

**Status:** COMPLETED (SSE + Streamable HTTP both integrated in worker, standalone mcp-sse removed, 2026-03-11)

## Summary

Add two working MCP transports to Engram so Claude Code can connect:
1. Fix protocol core — notification handling (broken in both transports)
2. Fix SSE transport (already exists, needs nil-response fix)
3. Implement Streamable HTTP transport (POST /mcp, stateless)

## Root Cause

handleRequest() in server.go has no notification detection. JSON-RPC 2.0 notifications
(messages without "id" field) MUST NOT receive a response. Current code returns
"Method not found" for `initialized` notification — a protocol violation that causes
Claude Code to disconnect.

## Phases

### Phase 1: Fix MCP Protocol Core

1.1 **Notification detection in handleRequest()**
    - At the TOP of handleRequest(): if req.ID == nil → route to handleNotification()
    - handleNotification: log + return nil (no response sent)
    - Known notifications: "initialized", "notifications/initialized", "cancelled"
    - Unknown notifications: log warning, return nil (NEVER respond)
    - File: server.go:164

1.2 **Graceful fallback for unimplemented methods**
    - resources/list → return empty list (not "Method not found")
    - prompts/list → return empty list
    - completion/complete → return empty result
    - This prevents Claude Code from treating missing features as errors
    - File: server.go:164-182

1.3 **SSE nil-response handling**
    - sse.go:handleMessage() — check if handleRequest returns nil
    - If nil: return 204 No Content (don't send to channel)
    - If non-nil: send to channel + return 202 Accepted (existing behavior)
    - File: sse.go:167

### Phase 2: Implement Streamable HTTP (/mcp)

2.1 **Refine streamable.go**
    - Already created (draft exists)
    - Add: nil response check → return 204 No Content
    - Add: proper Content-Type and CORS headers
    - File: streamable.go (exists, needs nil handling)

2.2 **Wire into worker service (from scratch)**
    - (a) Add `mcpStreamableHandler *mcp.StreamableHandler` field to Service struct
    - (b) Instantiate in initAsync: `mcp.NewStreamableHandler(mcpServer)`
    - (c) Create `handleMCPStreamable()` method (same pattern as handleMCPSSE)
    - (d) Register route: POST /mcp in requireReady group
    - File: service.go

2.3 **Update Claude Code config**
    - ~/.claude.json: type:"http", url:"http://unleashed.lan:37777/mcp"

### Phase 3: Testing

3.1 **Integration test (both transports)**
    - Add to server_test.go or new transport_test.go
    - TestMCPStreamableHTTP: POST /mcp with initialize → tools/list → tools/call
    - TestMCPNotification: POST with initialized (no id) → 204
    - Run against real PostgreSQL (DATABASE_DSN)

3.2 **Live test with Claude Code**
    - Restart Claude Code session
    - Verify "engram: connected"
    - Call an engram tool

3.3 **SSE verification**
    - Test type:"sse" config as well
    - If works → both transports confirmed

## Execution Order

```
Phase 1.1-1.2 (notification + stubs) --+--> Phase 2 (Streamable HTTP) --> Phase 3 (test)
                                        |
Phase 1.3 (SSE nil fix) ---------------+--> Phase 3.3 (SSE verification)
```

Phase 1.1-1.2 and Phase 1.3 can run in parallel.
Phase 2 depends on Phase 1.1 (notification handling).

## Files to Modify

| File | Change |
|------|--------|
| internal/mcp/server.go | Notification detection, initialized handler, empty stubs for resources/prompts |
| internal/mcp/sse.go | Nil-response check in handleMessage |
| internal/mcp/streamable.go | Nil-response check, 204 for notifications |
| internal/worker/service.go | Add StreamableHandler field, initAsync, handleMCPStreamable, /mcp route |
| internal/mcp/transport_test.go | NEW: integration tests for both transports |
| ~/.claude.json | Update engram config to type:"http" |

## Dropped from Scope

- Mcp-Session-Id header (not in spec, not needed for stateless)
- Ping handler (not in MCP spec, verify need first)

## Risks & Mitigations

| Risk | Mitigation |
|------|-----------|
| Claude Code sends undocumented methods | Empty stubs for resources/prompts/completion |
| Nil response causes crash in handlers | Explicit nil check before any response write |
| Auth not applied to /mcp route | Register in same requireReady group as /sse |

## Success Criteria

- [ ] curl POST /mcp with initialize → tools/list returns valid JSON-RPC
- [ ] curl POST /mcp with initialized (notification) returns 204
- [ ] Claude Code shows "engram: connected" with type:"http"
- [ ] Engram MCP tools callable from Claude Code
- [ ] Integration test passes
