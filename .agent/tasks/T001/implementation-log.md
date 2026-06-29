## Task T001 — Implementation Log

### Quoted AC
> AC: temporal truth contract exists with bounded fields for `true now`, `true then`, provenance, and invalidation history.
Source: .agent/specs/memory-product-layer/changes/CR-004-selective-temporal-truth/tasks.md | line 26

> Outcome: high-value evolving facts can answer “true now vs then”.
Source: .agent/specs/memory-product-layer/plan.md | line 315

> Broad graphification of all memories.
Source: .agent/specs/memory-product-layer/spec.md | line 200

### Discrepancy Found
This file previously contained CR-003 T001 forgetting-taxonomy evidence while the active CR is `CR-004-selective-temporal-truth`. The task ID collided across CRs. This log is now intentionally rewritten for the active CR-004 temporal-truth contract.

### User Change Enabled
The operator can ask what a selected evolving fact says now and what it said before, without treating all memory as a giant time graph.

### Claim Grounding
Claim: temporal truth is bounded. Meaning here: the public contract names selected fact scope and does not expose general graph traversal. Evidence: RED contract tests fail before the temporal truth symbols exist; GREEN tests pass after adding selected-scope types/interface; Prove-It breaks when selected scope or invalidation/provenance fields are removed.

Claim: provenance is first-class. Meaning here: current and prior truth answers carry evidence handles/source identity instead of bare values. Evidence: contract tests assert provenance fields on current answer and history entries.

### Terminology Alignment
"True now" maps to current selected-fact truth. "True then" maps to prior truth entries with validity windows. "Invalidation history" maps to prior entries carrying invalidation time/rationale and provenance. "Narrow" maps to selected fact scope, not graph-wide inference.

### Implementation Decision
Add temporal truth public types in `pkg/cognitive/types.go`, a `TemporalTruthProvider` interface in `pkg/cognitive/interfaces.go`, and `.agent/specs/memory-product-layer/contracts/temporal-truth.md`. Start with contract tests for current truth, prior truth, validity windows, invalidation rationale, provenance, and selected-fact scope.

### Verification Result
AC-by-AC:
  - AC 1: PASS — public contract exposes `true_now`, `true_then`, `history`, validity windows, invalidation rationale, provenance, and selected scope.
  - AC 2: PASS — Prove-It failed when `not_selected` was changed to `graph_search`, proving the contract guards narrow scope.

User-observable ACs:
  N/A — contract task, no running operator surface is changed.

Overall: PASS

TASK_T001_PASS
  C1 quoted AC: PASS
  C2 source cited: PASS (.agent/specs/memory-product-layer/changes/CR-004-selective-temporal-truth/tasks.md L26; plan.md L315; spec.md L200)
  C3 user change named: PASS
  C4 claims grounded: PASS
  C5 terms aligned: PASS
  C6 decision pre-code: PASS
  C7 ACs verified: PASS (2/2 PASS)
  C8 UX re-verified: N/A (no user-observable ACs)
  C9 no unresolved AMBIGUOUS: PASS
  C10 anti-patterns: PASS (0 detected)
