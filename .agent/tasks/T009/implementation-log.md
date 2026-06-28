## Task T009 - Implementation Log

### Quoted AC
> - AC: packet contract maps existing candidate/snapshot/audit data into bounded review payloads.
Source: `.agent/specs/memory-product-layer/changes/CR-001-initial-scope/tasks.md`

Supporting boundary:
> Review queue UI remains a later delivery behind a reviewed design contract.
Source: `.agent/specs/memory-product-layer/changes/CR-001-initial-scope/tasks.md`

### User Change Enabled
Backend candidate list payloads now carry an additive `review_packet` projection that tells an operator or future queue client which decision actions are valid, what scope/evidence the candidate belongs to, and what snapshot/audit policy applies before mutation.

### Claim Grounding
- Claim: packets reuse existing candidate data. Evidence: `reviewpacket.FromCandidate` derives packet id, status, decision policy, scope, and evidence handles from `models.CrystallizationCandidate`.
- Claim: packets map snapshot/audit seams without new persistence. Evidence: packet `snapshot.store=bulk_op_snapshots` and `audit.store=audit_log` are policy fields over existing substrates; no new table/store was added.
- Claim: REST and MCP stay aligned. Evidence: `candidateReviewItemFromDomain` and `handleListCandidates` both call the same `reviewpacket.FromCandidate` helper.
- Claim: terminal candidates are not exposed as actionable packets. Evidence: `internal/reviewpacket` tests assert terminal packets have no allowed actions and do not require snapshots.

### Terminology Alignment
- "Review packet" means a bounded derived payload on candidate list/read surfaces, not a queue UI.
- "Snapshot policy" means pre-action snapshot requirement metadata; T009 does not create a snapshot row during reads.
- "Audit policy" means the action/audit channel a later mutation must use; T009 does not execute review actions.

### Implementation Decision
Add a small `internal/reviewpacket` projection helper and use it from both REST and MCP candidate list paths. This avoids duplicating packet-shaping logic and keeps the change additive for existing operator-console clients.

### Verification Result
AC-by-AC:
  - AC 1: [PASS] - candidate list responses now include `review_packet` with decision, scope, evidence, snapshot, and audit fields.

Commands:
  - RED: `go test ./internal/worker -run TestHandleListMemoryCandidates_ReturnsProjectScopedPayload -count=1` failed because `candidateReviewItem` had no `ReviewPacket` field.
  - GREEN: `go test ./internal/worker -run TestHandleListMemoryCandidates_ReturnsProjectScopedPayload -count=1` - PASS.
  - GREEN: `go test ./internal/reviewpacket -count=1` - PASS.
  - GREEN: `go test ./internal/mcp -run TestHandleListCandidates -count=1` - PASS.
  - GREEN: `go test ./internal/worker ./internal/mcp ./internal/reviewpacket -count=1` - PASS.

Overall: [PASS]

### NEEDS_CLARIFICATION (if AMBIGUOUS result)
N/A
