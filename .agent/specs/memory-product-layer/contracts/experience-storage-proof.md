# Experience Storage Proof - ENG-MPL-1 CR-002

Status: CR-002 storage-shape decision  
Decision date: 2026-06-29

## Decision

CR-002 keeps V1 experience retrieval on a projection/materialization shape.

The live implementation is a first-class experience contract plus
`internal/experience.Service`, which operates over `cognitive.ExperienceResponse`
envelopes and an explicit archive source seam. The service proves bounded
retrieval, applicability, anti-applicability, finite archive triggers, and
trigger-gated archive resurfacing without introducing dedicated
`ExperienceRecord` tables.

## Chosen Shape

- Public contract: `pkg/cognitive.ExperienceQueryRequest`,
  `ExperienceResponse`, and `ExperienceProvider`.
- Retrieval seam: `internal/experience.Service`.
- Archive seam: `internal/experience.ArchiveSource`, invoked only for valid
  archive trigger classes.
- Evidence model: `ArchiveEvidenceEntry` plus CR evidence files under
  `.agent/specs/memory-product-layer/evidence/`.
- Storage posture: projection/materialization over existing evidence/source
  seams until a future CR proves dedicated persistence is required.

## Rejected Shape For CR-002

CR-002 rejects a dedicated `experience_records` table family and GORM store for
this slice.

Reason: T002-T005 proved the required CR-002 behavior without a dedicated table:
bounded query output, explicit applicability states, anti-applicability blocking,
finite archive triggers, ordinary-path archive skip, and bounded archive
resurfacing. No test or workflow evidence showed projection/materialization to be
failing or prohibitive.

This is a deferral, not a permanent ban. Dedicated storage remains valid in a
later CR if evidence shows projection cannot satisfy durability, query cost,
provenance, or archive indexing requirements.

## Evidence

- T001: first-class experience contract passed `go test ./pkg/cognitive`.
- T002/T003: bounded experience query and applicability gate passed
  `go test ./internal/experience`; prove-it mutation failed the targeted
  blocked-state test; coverage was 87.4%.
- G001: `go test ./...` passed and saved
  `.agent/specs/memory-product-layer/evidence/phase-4-gate.json`.
- T004/T005: finite archive triggers and trigger-gated resurfacing passed
  `go test ./internal/experience`; prove-it mutation failed the ordinary-path
  archive-call test; coverage was 89.0%.
- G002: `go test ./...` passed and saved
  `.agent/specs/memory-product-layer/evidence/phase-4-archive-gate.json`.
- Changed-file review found no `internal/db/gorm/*experience*` or
  `internal/db/gorm/*archive*` migration/store files in this CR-002 slice.

## Next-Slice Consequence

The next CR may add one of two bounded extensions:

1. projection adapters that materialize experience envelopes from current
   memory/session/audit evidence into the `ArchiveSource` seam;
2. a dedicated `ExperienceRecord` table family only after a failing test or
   measured workflow proves projection cannot carry required durability,
   attribution, query cost, or archive-index semantics.

The next CR must not start from a vague "experience needs tables" assumption.
It must name the failing projection case first.

## Explicit Boundary

Still out of CR-002:

- forgetting / consolidation
- selective temporal truth
- learned applicability model
- broad graph projection
- broad operator-console redesign
- generic hot-memory ranking changes
