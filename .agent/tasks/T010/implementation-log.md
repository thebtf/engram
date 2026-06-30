# T010 Implementation Log

**Task:** Implement queue list/read actions on existing seams
**Date:** 2026-06-28
**Status:** Completed

## Scope

T010 keeps the review-loop substrate on the existing candidate, snapshot, and audit seams. It adds a single-candidate read path next to the packet-centric list path:

- REST: `GET /api/memory/candidates/{id}`
- MCP: `get_candidate`

Both return the same additive `review_packet` projection introduced in T009. No queue UI, new persistence table, forgetting/consolidation workflow, or temporal-truth work was added.

## RED

- `go test ./internal/worker -run TestHandleGetMemoryCandidate_ReturnsReviewPacket -count=1`
  - Failed because `Service.handleGetMemoryCandidate` did not exist.
- `go test ./internal/mcp -run TestHandleGetCandidate_EmptyIDReturnsError -count=1`
  - Failed because `Server.handleGetCandidate` did not exist.

## GREEN

- Added REST route registration in `internal/worker/service.go`.
- Added `handleGetMemoryCandidate` in `internal/worker/handlers_candidates.go`.
- Added MCP `get_candidate` tool definition, handler, shared candidate item projection, and dispatch case.
- Aligned REST and MCP payloads on the same derived `reviewpacket.FromCandidate` projection.

## Verification

- PASS `go test ./internal/worker -run TestHandleGetMemoryCandidate_ReturnsReviewPacket -count=1`
- PASS `go test ./internal/mcp -run TestHandleGetCandidate_EmptyIDReturnsError -count=1`
- PASS `go test ./internal/worker ./internal/mcp ./internal/reviewpacket -count=1`

## Evidence Notes

Snapshot and audit evidence remains policy-level projection over existing seams:

- pending packet snapshot: `bulk_op_snapshots`, `candidate_review_action`, `pre_action_required`
- pending packet audit: `audit_log`, `candidate_review`, `pending_on_action`

The read path does not perform candidate actions; it only exposes the bounded packet needed for a future designed review queue.
