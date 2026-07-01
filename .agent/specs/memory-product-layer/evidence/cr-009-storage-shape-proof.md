# CR-009 Storage-Shape Proof — Experience + Applicability Layer

Status: PM verified pass  
Observed: 2026-07-01T01:07:46Z  
Developer gate: implementation handback received; PM ran formatting, focused tests, full tests, vet, diff check, and targeted fix review.

## Decision

CR-009 keeps the V1 experience/history storage shape as **projection/materialization-first** over the existing `cognitive.ExperienceProvider` / `internal/experience.Service` substrate.

No dedicated `ExperienceRecord` table family, migration, GORM store, or archive/history browser storage was introduced in this slice.

## Evidence

- `pkg/cognitive/types.go` continues to expose `ExperienceResponse.storage_origin` with `projection`, `materialized`, and `dedicated` values.
- `internal/experience.Service` remains projection-backed: it accepts bounded `[]cognitive.ExperienceResponse` candidates and optional trigger-gated `ArchiveSource` rather than owning a dedicated table.
- `internal/experience/history_surface.go` wraps the existing provider into reusable read/detail response envelopes and echoes `storage_origin`; it does not create persistence.
- `internal/mcp/tools_experience_history.go` and `internal/worker/handlers_experience_history.go` add adapter surfaces only. They call the provider; they do not create storage or hot-memory fallback paths.
- `internal/worker/service.go` now wires a memory-backed projection provider. An unconfigured seam is `gated`; a live query over projected memory rows that matches nothing is `empty_results`. The adapter remains storage-shape-safe because it projects existing memory rows into the CR-009 provider seam without adding a dedicated experience table.
- The targeted fixes added per-request archive evidence, exact detail lookup, canonical detail IDs, and archive-source defensive filtering without changing storage shape.

## Rejected storage expansion

Dedicated storage is rejected for CR-009 because the active acceptance can be satisfied by bounded adapter envelopes over the existing substrate:

1. Retrieval/read/detail shape is provided by adapters and helper response types.
2. Auditability for archive resurfacing is carried by `ArchiveEvidenceEntry` and surfaced as per-request `archive_trace`.
3. Applicability and anti-applicability are first-class envelope fields on the response contract.
4. Storage origin remains explicit; future dedicated storage can appear as `storage_origin=dedicated` without changing adapter consumers.

## Verification

```bash
go test ./pkg/cognitive ./internal/experience ./internal/mcp ./internal/worker -run "ExperienceApplicability|ExperienceResponse|ExperienceHistory|ReadHistory|Archive" -count=1
# pass

go test ./...
# pass

go vet ./...
# pass

git diff --check
# pass; LF-to-CRLF working-copy warnings only
```

Targeted review:

- `CR009CodeReview`: BLOCK — archive trace could borrow another request's global evidence; detail lookup could miss exact IDs beyond `MaxQueryLimit`.
- `CR009FixReview`: APPROVE — both fixes verified, no findings.

## Rollback / migration impact

- Database rollback: none; no migrations added.
- Data migration: none; no dedicated experience rows created.
- Code rollback: remove the CR-009 adapter files/wiring and the applicability envelope extension if PM rejects the surface.

## Residual risk

The production worker now projects existing memory rows into the CR-009 provider seam. Result richness still depends on how much structured context those rows carry, but CR-009 no longer ships an always-empty default provider and still does **not** justify a dedicated table family.
