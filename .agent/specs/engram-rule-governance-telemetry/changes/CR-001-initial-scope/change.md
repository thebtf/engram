---
id: CR-001
slug: initial-scope
status: DRAFT
created: 2026-06-19
spec: .agent/specs/engram-rule-governance-telemetry/spec.md
plan: .agent/specs/engram-rule-governance-telemetry/plan.md
---

# Change: RG-3 Initial Governance Telemetry And Escalation

## Summary

Add the first backend RG-3 surfaces for lifecycle health, exception queues, transition controls, rule-governance snapshots, rollback/archive/restore, and canary/usefulness metrics. Keep the change backend-first and flag-safe. Do not build a broad dashboard or grant background/LLM actors new authority.

## In Scope

- Health aggregation over rule candidates, versions, transitions, snapshots, arbiter runs/evaluations, and injection events.
- Exception queues grouped by escalation, conflict, reject-review, scope, canary, rollback, and stale-cache reasons.
- Auth-gated transition controls for lifecycle actions.
- Rule-governance snapshot list, pin, and rollback surfaces.
- Telemetry aggregation for canary/usefulness evidence.
- Dogfood trace report.

## Out Of Scope

- Broad operator console redesign.
- Automatic active/global/kernel promotion.
- Router rewrite.
- Background arbiter authority expansion.
- Production flag activation without explicit rollout authorization.

## Acceptance Gates

- Health gate: empty data is `NO DATA` or empty counts, never fabricated.
- Queue gate: operator reviews grouped exceptions, not every candidate.
- Authority gate: background, LLM, and system actors cannot activate active/global/kernel states.
- Snapshot gate: active/global/kernel transitions require snapshot evidence.
- Rollback gate: conflict tests refuse divergent current state.
- Usefulness gate: metrics are advisory and low-sample canary does not auto promote.
- Dogfood gate: one real project rule lifecycle trace is recorded.
- Release gate: local gates, PR review evidence, CI, and release/dogfood evidence when shippable.

## Risk Notes

- Existing bulk snapshot tools are easy to confuse with rule-governance snapshots. Keep names and docs explicit.
- Existing RG-2 telemetry may not yet include cited/ignored events. Use `NO DATA` rather than inventing usefulness.
- `.agent/` is ignored in this repo; force-add spec artifacts only when they are intentionally part of the PR checkpoint.
