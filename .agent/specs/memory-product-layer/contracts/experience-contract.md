# Experience Contract — ENG-MPL-1 CR-002

Status: T004 contract draft  
Scope: first-class experience response contract, V1 envelope, and archive-triggered resurfacing rules.

## Purpose

Experience retrieval is separate from hot-memory retrieval. A returned
experience describes a historical lesson and carries applicability evidence
before it may influence the current request.

## V1 Payloads

`ExperienceQueryRequest` scopes an explicit experience/history read:

- `project`
- `principal`
- `domain`
- `query`
- `current_context`
- `archive_trigger_classes`
- `limit`

`ExperienceResponse` returns a bounded historical lesson:

- `source` — `projection`, `materialized`, or `dedicated`
- `lesson`
- `applicability.state` — `applies`, `uncertain`, or `blocked`
- `applicability.rationale`
- `anti_applicability[]`
- `source_attribution[]`
- `archive_trigger_classes[]`

## Archive Trigger Taxonomy

Archive resurfacing is disabled unless the request names one or more finite
trigger classes:

- `why_changed`
- `regression`
- `rollback`
- `old_decision_revisit`
- `similar_failure`

Unknown trigger strings are invalid and must fail before archive lookup.
Duplicate trigger classes are deduped into canonical order.

## Archive Resurfacing Packet Behavior

Archive resurfacing runs on the experience path only when
`archive_trigger_classes` is non-empty and valid. Ordinary experience queries
must not touch the archive source.

Archive results are bounded by `MaxArchiveResurfacingLimit` and returned as
`ExperienceResponse` values tagged with the triggering classes. Each trigger
run records evidence with:

- `trigger_classes`
- `requested_limit`
- `returned`
- `status`
- optional `reason`

## Applicability Semantics

- `applies`: the lesson can influence the current request.
- `uncertain`: the lesson is relevant but must not be silently automated.
- `blocked`: anti-applicability matches strongly enough to block silent reuse.

## Anti-Applicability

Each anti-applicability item names:

- `condition`
- `rationale`

This keeps the block/downrank reason explicit instead of relying on semantic
similarity alone.

## Storage Boundary

T001 does not choose dedicated storage. V1 may use projection/materialization
over existing evidence until T006 proves whether a dedicated `ExperienceRecord`
family is required.

## Out of Scope

- MCP/REST handlers
- dedicated persistence
- learned applicability model
- forgetting/consolidation
- temporal truth
