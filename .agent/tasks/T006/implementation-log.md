## Task T006 - Implementation Log

### Quoted AC
> AC: proof artifact states chosen shape, rejected shape, evidence, and next-slice consequence.
Source: `.agent/specs/memory-product-layer/changes/CR-002-experience-applicability/tasks.md` | line 132

Related requirements:
> Start with first-class experience contract, prove storage shape before dedicated tables.
Source: `.agent/specs/memory-product-layer/architecture.md` | line 368

> V1 implementation may begin as projection/materialization over existing evidence, and promote to dedicated `ExperienceRecord` storage only if retrieval/workflow proof demands it.
Source: `.agent/specs/memory-product-layer/architecture.md` | line 371

> If dedicated storage still not justified — keep projection/materialization and defer tables.
Source: `.agent/specs/memory-product-layer/plan.md` | line 295

### Discrepancy Found
This file previously contained CR-001 T006 principal-memory evidence while the active CR is `CR-002-experience-applicability`. The task ID collided across CRs. This log is now intentionally rewritten for the active CR-002 storage-shape proof.

### User Change Enabled
No direct runtime user change. This task prevents premature schema commitment by making the V1 experience storage boundary explicit for the next CR.

### Claim Grounding
- Claim: projection/materialization is the chosen V1 shape. Meaning here: CR-002 keeps the experience service in-process over first-class `ExperienceResponse` envelopes and an archive source seam, with no dedicated `experience_records` table family. Evidence: `internal/experience` tests and proof artifact.
- Claim: dedicated storage is rejected for this CR. Meaning here: CR-002 did not find concrete failing/prohibitive evidence that projection/materialization cannot satisfy bounded retrieval, applicability, or archive-trigger behavior. Evidence: T002-T005 TDD evidence, G001/G002 `go test ./...`, and changed-file review.
- Claim: schema work is deferred, not denied forever. Meaning here: a later CR may introduce dedicated storage if adapter proof shows durability/query/provenance requirements projection cannot meet. Evidence: next-slice consequences in `experience-storage-proof.md`.
- Claim: no forbidden scope leaked in. Meaning here: T006 does not begin forgetting/consolidation, temporal truth, learned applicability, broad graph projection, or broad operator-console redesign. Evidence: proof artifact boundary and changed-file review.

### Terminology Alignment
- "Chosen shape" means the V1 storage implementation strategy for CR-002, not the final product architecture for all future slices.
- "Rejected shape" means "not in CR-002"; dedicated `ExperienceRecord` storage remains a later conditional option.
- "Projection/materialization" means experience records are represented as typed envelopes over existing evidence/source seams before dedicated tables exist.
- "Next-slice consequence" means the exact condition under which a future CR should add adapters or dedicated storage.

### Implementation Decision
Create `.agent/specs/memory-product-layer/contracts/experience-storage-proof.md` and `.agent/specs/memory-product-layer/evidence/experience-storage-proof.json`. Choose projection/materialization for CR-002 V1, reject dedicated `ExperienceRecord` tables for this CR, and tie the decision to T002-T005 evidence and G001/G002 gates. No production code, migrations, REST/MCP exposure, forgetting, consolidation, temporal truth, learned applicability, broad graph, or broad UI work is added in T006.

### Verification Result
AC-by-AC:
  - AC 1: PASS - proof artifact states chosen projection/materialization shape.
  - AC 2: PASS - proof artifact states rejected dedicated-table shape for CR-002.
  - AC 3: PASS - proof artifact cites T002-T005 TDD evidence plus G001/G002 gates.
  - AC 4: PASS - proof artifact names next-slice consequences and out-of-scope boundaries.

TDD evidence:
  - N/A - proof artifact task. Current proof depends on existing T002-T005 TDD and G001/G002 gates.

Overall: PASS

### NEEDS_CLARIFICATION (if AMBIGUOUS result)
N/A
