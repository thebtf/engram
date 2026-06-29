## Task T003 — Implementation Log

### Quoted AC
> AC: truth changes expose prior value, current value, invalidation rationale, and provenance chain.
Source: .agent/specs/memory-product-layer/changes/CR-004-selective-temporal-truth/tasks.md | line 64

> Scope:
> - narrow temporal truth schema
> - validity/invalidation history
> - provenance-first retrieval
Source: .agent/specs/memory-product-layer/plan.md | lines 317-320

### Discrepancy Found
This file previously contained CR-003 T003 structural-loss evidence while the active CR is `CR-004-selective-temporal-truth`. The task ID collided across CRs. This log is now intentionally rewritten for the active CR-004 validity/invalidation history contract.

### User Change Enabled
The operator can inspect how a selected fact changed, including the old answer, the new answer, why the old one stopped being true, and the evidence trail.

### Claim Grounding
Claim: invalidation keeps rationale and provenance, not just timestamps. Meaning here: the response exposes prior/current entries and a provenance chain derived from the history. Evidence: RED test fails before `provenance_chain` exists; GREEN test asserts old value, current value, invalidation rationale, and both provenance handles survive.

Claim: history remains narrow. Meaning here: this only replays selected-fact fixtures already accepted by G001; it does not add graph traversal. Evidence: focused service test and G002 full suite.

### Terminology Alignment
"Prior value" maps to a history entry with `valid_until`/`invalidated_at`. "Current value" maps to `true_now`. "Invalidation rationale" maps to the prior entry rationale. "Provenance chain" maps to ordered provenance from historical and current entries.

### Implementation Decision
Extend `TemporalTruthResponse` with `provenance_chain` and centralize chain construction in `internal/temporaltruth`. Update the temporal truth contract to name the chain. No storage migration or graph API is introduced.

### Verification Result
AC-by-AC:
  - AC 1: PASS — truth-change fixture exposes prior `v6`, current `v7`, invalidation rationale, and ordered provenance chain.
  - AC 2: PASS — Prove-It failed when `provenanceChain` returned nil, proving the test is load-bearing.

User-observable ACs:
  N/A — service history task, no running operator surface is changed.

Overall: PASS

TASK_T003_PASS
  C1 quoted AC: PASS
  C2 source cited: PASS (.agent/specs/memory-product-layer/changes/CR-004-selective-temporal-truth/tasks.md L64; plan.md L317-L320)
  C3 user change named: PASS
  C4 claims grounded: PASS
  C5 terms aligned: PASS
  C6 decision pre-code: PASS
  C7 ACs verified: PASS (2/2 PASS)
  C8 UX re-verified: N/A (no user-observable ACs)
  C9 no unresolved AMBIGUOUS: PASS
  C10 anti-patterns: PASS (0 detected)
