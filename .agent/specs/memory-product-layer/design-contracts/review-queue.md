# Review Queue Surface Design Contract

**Feature:** ENG-MPL-1 memory-product-layer
**CR:** CR-001-initial-scope
**Task:** T011
**Surface:** review-queue
**Status:** Approved as future UI contract after PM scenario review
**Reviewed:** 2026-06-28

## Surface Boundary

This contract covers the future operator-console review queue surface for packet-centric memory-quality decisions. It is design-ready only; CR-001 does not ship queue UI.

The contract is allowed to bind to the backend substrate completed in T009/T010:

- candidate list/read packet projection
- candidate promote/reject/supersede actions
- derived snapshot and audit policy evidence

It does not cover experience/applicability, forgetting taxonomy delivery, archive retrieval, consolidation, temporal truth, or destructive memory mutation. Suppress/archive/consolidate/destroy may appear only as `mustbuild` future action categories until their backend contracts exist.

## User Outcome

An operator can review a bounded exception queue, open one decision packet, understand the candidate, evidence, scope, snapshot requirement, and audit result, then take only actions backed by live seams. The surface must never become a row-by-row memory sorting table.

## Packet Layout

| Region | Purpose | Backend binding | State rule |
| --- | --- | --- | --- |
| Queue header | Shows project/status/limit scope and freshness | `GET /api/memory/candidates`, MCP `list_candidates` | Missing scope echo becomes error |
| Packet list | Shows bounded candidate packets, not raw memory rows | `candidateReviewItem.review_packet` | Empty is honest and still operable |
| Packet detail | Shows proposed content, promotion target, tier, epistemic type, confidence, recurrence, privacy, and source session | REST read `GET /api/memory/candidates/{id}`, MCP `get_candidate` | Missing detail route becomes `mustbuild` |
| Evidence strip | Shows typed evidence handles and source session | `review_packet.evidence`, `review_packet.scope.source_session_id` | Missing evidence becomes gated/stale, not hidden success |
| Snapshot panel | Shows pre-action snapshot requirement and store | `review_packet.snapshot` | Required snapshot must be visible before risky action |
| Audit panel | Shows intended audit channel/status before action and final receipt after action | `review_packet.audit`, action receipt/audit result | Missing audit evidence blocks risky action |
| Action rail | Shows allowed actions for current packet state | `review_packet.decision.allowed_actions` | Only live allowed actions are enabled |
| State banner | Shows loading, empty, gated, mustbuild, stale, error, risky-confirm, rollback-needed, and recovered states | Shared operator-console state pattern | Unsupported behavior never looks live |

## Controls

| Control | Type | Required behavior | Backend binding | State if absent |
| --- | --- | --- | --- | --- |
| `project-filter` | Select/filter | Narrows queue by project or `all` when authorized | `project` query arg | gated |
| `status-filter` | Segmented control | Shows `pending` by default; terminal statuses are read-only | `status` query arg | error |
| `queue-refresh` | Icon/button | Re-reads current list scope without changing selection | candidate list read | error with previous data marked stale |
| `packet-open` | Row/button | Opens bounded packet detail by id | candidate read path | mustbuild |
| `promote-action` | Button | Creates decision memory from pending candidate | existing candidate promote seam | gated unless packet allows `promote` |
| `reject-action` | Button | Rejects candidate with optional reason | existing candidate reject seam | gated unless packet allows `reject` |
| `supersede-action` | Button | Marks candidate superseded | existing candidate supersede seam | gated unless packet allows `supersede` |
| `reason-input` | Text input | Collects rejection reason when reject is chosen | reject request body | optional |
| `snapshot-confirm` | Confirmation control | Confirms operator saw required pre-action snapshot policy | `review_packet.snapshot.required` | risky-confirm or gated |
| `audit-receipt` | Read-only panel | Displays action receipt and audit outcome | action response plus audit seam | error if action succeeded but receipt absent |

Controls intentionally excluded from enabled behavior: suppress, archive, consolidate, destroy, bulk destructive action, and temporal truth mutation. If shown for roadmap context, each must render as `mustbuild` with no submit path.

## Usage Flow

1. Operator enters the future review queue surface from the operator-console governance area.
2. Surface loads `pending` packets for the current project, or `all` only when backend authorization allows it.
3. Operator opens one packet and reads proposed content, decision target, scope, evidence, snapshot policy, and audit policy.
4. Surface enables only actions listed in `review_packet.decision.allowed_actions`.
5. If snapshot is required, operator must pass the `snapshot-confirm` step before an action request is sent.
6. Action request returns a receipt; packet is refreshed and moves to a terminal read-only state or remains pending with an honest error.
7. If an action fails after partial backend work, surface shows rollback-needed/recovery state and re-reads the candidate before accepting another action.

## Backend Binding Map

| Visible element | Required backend field or route | Owner task | State if absent |
| --- | --- | --- | --- |
| Queue count and list | `GET /api/memory/candidates`, `list_candidates`, `count`, `candidates[]` | T009/T010 | mustbuild |
| Packet id and candidate id | `review_packet.packet_id`, `review_packet.candidate_id` | T009 | error |
| Candidate detail | `GET /api/memory/candidates/{id}`, `get_candidate` | T010 | mustbuild |
| Decision target | `review_packet.decision.*` | T009 | error |
| Allowed actions | `review_packet.decision.allowed_actions` | T009 | gated |
| Scope evidence | `review_packet.scope.*`, candidate `privacy_scope`, `affected_projects` | T009 | gated |
| Evidence handles | `review_packet.evidence[]` | T009 | gated |
| Snapshot requirement | `review_packet.snapshot.*` | T009 | risky-confirm |
| Audit policy | `review_packet.audit.*` | T009 | gated |
| Promote receipt | `POST /api/memory/candidates/{id}/promote` or MCP `promote_candidate` | existing candidate seam | gated |
| Reject receipt | `POST /api/memory/candidates/{id}/reject` or MCP `reject_candidate` | existing candidate seam | gated |
| Supersede receipt | `POST /api/memory/candidates/{id}/supersede` or MCP `supersede_candidate` | existing candidate seam | gated |

## Honest States

| State | Trigger | Operator result |
| --- | --- | --- |
| loading | List, read, or action request in flight | Existing safe data stays visible but marked busy |
| live | Backend returns supported packets and allowed actions | Queue and packet detail are usable |
| empty | Supported list returns zero packets | Surface says no packets match the selected scope |
| gated | Authorization, privacy, or feature flag prevents data/action | Surface reveals no private or partial action data |
| mustbuild | A visible future capability lacks a backend contract | Capability is unavailable and cannot be submitted |
| stale | Refresh fails after prior live data | Prior data remains visible with stale/error marker |
| error | Backend or parse failure | Retry is available; action buttons are disabled |
| risky-confirm | Snapshot or scope risk must be confirmed before action | Operator must confirm or cancel |
| rollback-needed | Action outcome is ambiguous or receipt/audit evidence is missing | Surface blocks new actions and asks backend state to be refreshed |
| recovered | Re-read proves terminal state or safe pending state after failure | Surface resumes with live or read-only packet state |

## Scenario Proof

| Branch | Operator-seat thought experiment | Expected result | PM verdict |
| --- | --- | --- | --- |
| happy path | I open pending packets for `engram`, inspect a packet, confirm required snapshot, and promote it. | Surface shows packet evidence, sends one live promote action, returns a receipt, and refreshes into a terminal/read-only state. | PASS |
| empty state | I select a project/status pair with no pending packets. | Surface shows an empty queue tied to the selected scope; refresh and filters remain usable. | PASS |
| validation failure | I open a packet with a missing/invalid id or submit reject with invalid reason shape. | Surface keeps me on the packet, marks the invalid control, and sends no broad or destructive request. | PASS |
| gated path | Feature flag, candidate store, or authorization is absent. | Surface shows gated/mustbuild state and does not reveal candidate content or enabled actions. | PASS |
| risky confirmation | Packet requires a pre-action snapshot before promote/supersede. | Surface shows snapshot store/status and requires confirmation before sending the action. | PASS |
| rollback | Action request times out or succeeds without a usable receipt/audit signal. | Surface disables actions, marks rollback-needed, and re-reads the candidate before allowing further operation. | PASS |
| recovery | Candidate re-read after a failed action shows terminal or still-pending state. | Surface moves to recovered live/read-only state and displays the current packet truth. | PASS |

## PM Review Result

**Result:** PASS

The contract includes packet layout, action classes, usage flow, backend bindings, honest states, and all required branch scenarios. Every branch ends in an operable or honest blocked state. Queue UI implementation remains blocked until a later CR explicitly authorizes it; CR-001 ships only the backend substrate and this future-surface contract.

## Implementation Constraints

- Future UI must implement from this contract and its JSON sidecar.
- Future UI must center packets/queues, not memory rows or prompt-retention copy.
- Future UI must use `review_packet.decision.allowed_actions`; it must not infer actions from status strings alone.
- Private/scoped packets remain fail-closed.
- Snapshot and audit policy must be visible before risky actions.
- Suppress/archive/consolidate/destroy remain `mustbuild` until later backend contracts exist.
