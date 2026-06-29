## Task T002 — Implementation Log

### Quoted AC
> AC: caller can query selected facts and receive current truth plus prior validity context.
Source: .agent/specs/memory-product-layer/changes/CR-004-selective-temporal-truth/tasks.md | line 35

> Evidence route: targeted temporal query tests plus a written scope proof showing which facts entered the temporal path and which did not, saved under `.agent/specs/memory-product-layer/evidence/phase-6-*`.
Source: .agent/specs/memory-product-layer/plan.md | line 323

> `temporal_truth_records`, `temporal_truth_events` only if MPL-6 proves need | selected high-value evolving truths
Source: .agent/specs/memory-product-layer/architecture.md | line 283

### Discrepancy Found
This file previously contained CR-003 T002 forgetting-classification evidence while the active CR is `CR-004-selective-temporal-truth`. The task ID collided across CRs. This log is now intentionally rewritten for the active CR-004 bounded temporal query path.

### User Change Enabled
The operator can query a chosen evolving fact and see the current answer together with the earlier answer it replaced.

### Claim Grounding
Claim: query path is selected-fact only. Meaning here: the service accepts selected fact identifiers and refuses unselected scope rather than traversing memories broadly. Evidence: focused tests assert selected facts return answers and unselected facts return an explicit not-selected state.

Claim: prior validity context is queryable. Meaning here: current response includes bounded history with validity start/end, invalidation rationale, and provenance. Evidence: temporal query tests assert current and prior entries.

### Terminology Alignment
"Selected facts" means explicitly allowed high-value evolving fact IDs/classes in the service fixture, not all memories. "Prior validity context" means bounded historical entries, not broad memory search. "No graph traversal" means no new graph DB, no cross-domain inference, and no graph-wide API.

### Implementation Decision
Add a minimal `internal/temporaltruth` service implementing the public provider interface over explicit selected records. It will return current truth plus bounded prior history and explicit not-selected/unknown states without introducing storage migrations or graph traversal in T002.

### Verification Result
AC-by-AC:
  - AC 1: PASS — selected fact query returns current truth and the prior `as_of` truth with provenance and invalidation rationale.
  - AC 2: PASS — unselected fact query returns `not_selected` with no history; Prove-It failed when the selected-fact filter was disabled.

User-observable ACs:
  N/A — service path task, no running operator surface is changed.

Overall: PASS

TASK_T002_PASS
  C1 quoted AC: PASS
  C2 source cited: PASS (.agent/specs/memory-product-layer/changes/CR-004-selective-temporal-truth/tasks.md L35; plan.md L323; architecture.md L283)
  C3 user change named: PASS
  C4 claims grounded: PASS
  C5 terms aligned: PASS
  C6 decision pre-code: PASS
  C7 ACs verified: PASS (2/2 PASS)
  C8 UX re-verified: N/A (no user-observable ACs)
  C9 no unresolved AMBIGUOUS: PASS
  C10 anti-patterns: PASS (0 detected)
