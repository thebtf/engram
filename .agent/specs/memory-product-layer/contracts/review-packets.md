# Review Packet Projection Contract

**Feature:** ENG-MPL-1 memory-product-layer  
**CR:** CR-001-initial-scope  
**Task:** T009/T010
**Status:** Implemented as derived backend projection  

## Boundary

Review packets are bounded projections over existing candidate, snapshot, and audit seams. They do not create a new review persistence table, queue UI, forgetting workflow, consolidation workflow, temporal-truth model, or experience/applicability layer.

## Source Seams

| Source | Current seam | Packet use |
| --- | --- | --- |
| Candidate row | `models.CrystallizationCandidate` via candidate store list/read | Candidate id, status, proposed content metadata, confidence, recurrence, affected projects, privacy, evidence handles, promoted memory id |
| Snapshot policy | existing `bulk_op_snapshots` substrate used by bulk governance operations | Declares whether a pending review action requires a pre-action snapshot |
| Audit policy | existing `audit_log` / candidate transition audit behavior | Declares which audit channel/status applies when a packet action is executed |

## Packet Shape

Each candidate list item and single-candidate read response carries an additive `review_packet` object:

```json
{
  "packet_id": "candidate:42:abc123",
  "kind": "candidate_review",
  "candidate_id": 42,
  "status": "pending",
  "decision": {
    "promotion_target": "semantic",
    "tier": "semantic",
    "epistemic_type": "decision",
    "default_action": "promote",
    "allowed_actions": ["promote", "reject", "supersede"]
  },
  "scope": {
    "projects": ["engram"],
    "privacy_scope": "project",
    "source_session_id": "sess-42"
  },
  "evidence": [
    { "handle": "session:sess-42", "kind": "session" }
  ],
  "snapshot": {
    "store": "bulk_op_snapshots",
    "operation": "candidate_review_action",
    "status": "pre_action_required",
    "required": true
  },
  "audit": {
    "store": "audit_log",
    "action": "candidate_review",
    "status": "pending_on_action"
  }
}
```

## Rules

- Packet IDs are deterministic: `candidate:<id>:<fingerprint>`, falling back to `candidate:<id>:<id>` when no fingerprint exists.
- Pending candidates expose exactly `promote`, `reject`, and `supersede` as allowed actions.
- Terminal candidates expose no allowed actions, no required snapshot, and `audit.status=terminal_record`.
- Evidence handles stay bounded and typed by their prefix before `:`, falling back to `handle`.
- Packet shape is additive on existing REST/MCP candidate list and read payloads; older clients can ignore it.

## Read Surfaces

- REST list: `GET /api/memory/candidates?project=<project>&status=pending`.
- REST read: `GET /api/memory/candidates/{id}`.
- MCP list: `list_candidates`.
- MCP read: `get_candidate`.

The read surfaces use the same candidate-to-packet projection as list surfaces. They do not add new persistence, queue UI state, or alternate audit/snapshot stores.

## Verification

- REST: `internal/worker` candidate handler tests assert `review_packet` appears in list responses with decision, scope, evidence, snapshot, and audit fields.
- Projection: `internal/reviewpacket` tests assert pending and terminal packet behavior.
- MCP: `list_candidates` and `get_candidate` use the same projection helper as REST so payload semantics stay aligned.
