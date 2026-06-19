---
status: READY_FOR_TASKS
slug: engram-rule-governance-telemetry
created: 2026-06-19
modified: 2026-06-19
type: implementation-plan
source_spec: .agent/specs/engram-rule-governance-telemetry/spec.md
source_goal: .agent/goals/2026-06-18-rule-governance-arbiter-marathon.md
open_crs: [CR-001]
---

# Plan: RG-3 Governance Telemetry And Escalation

## Recommendation

Build RG-3 as one backend-first CR that adds read models, exception queues, and transition controls over the existing RG-0/RG-1/RG-2 substrate. Do not start a dashboard rebuild in this CR. The value is truthful data and reversible controls; UI can consume these surfaces later.

## Source Audit

- Domain models already define candidates, versions, transitions, snapshots, actor kinds, injection event types, and snapshot requirements.
- The rule governance store already has create/list/transition/snapshot methods but lacks health, queue, transition-control read DTOs, and rollback/archive/restore convenience surfaces.
- The rule injection event store records and lists by session only. RG-3 needs bounded project/rule/event aggregations and canary-usefulness queries.
- Existing bulk governance tools manage bulk-operation snapshots. RG-3 should add rule-governance-specific surfaces rather than overloading those names.
- Existing rule edit endpoints operate on legacy behavioral rules. RG-3 canonical transition controls should target rule versions.

SocratiCode and Serena are unavailable in this session (`Transport closed`), so this plan uses exact file reads, Go docs, and local test runs as fallback evidence.

## CR-001 Scope

CR-001 delivers lifecycle health aggregation, exception queue aggregation, rule injection telemetry aggregation, authorized transition controls, rule-governance snapshot list/pin/rollback surfaces, dogfood trace report support, and tests.

CR-001 does not implement broad UI, automatic promotion, or production flag activation.

## Technical Decisions

- TD-001: backend read models first, before dashboard work.
- TD-002: rule-governance snapshots stay separate from bulk-operation snapshots.
- TD-003: canary/usefulness metrics are advisory and cannot activate a rule by themselves.
- TD-004: RG-3 does not grant new active-transition authority to arbiter, LLM, or background workers.
- TD-005: expose a small backend surface for health, queues, transitions, snapshots, rollback, and usefulness.

## Data Model

No destructive migration is expected. Additive extensions may include extra injection event types for cited, ignored, conflicted, superseded, decayed, and stale-cache anomaly signals if existing events cannot represent RG-3 evidence. Prefer store queries over new schema when sufficient.

## Query Strategy

- Health queries count by bounded windows and indexed columns.
- Queue queries are composed from candidates, versions, transition logs, snapshots, arbiter evaluations, and injection events.
- Queue item DTOs use legal escapes for unavailable fields.
- Rollback reads current version state before mutation and refuses divergent state.
- Telemetry failures do not affect session-start or router delivery.

## Test Strategy

Targeted tests cover models, stores, telemetry aggregation, snapshot list/pin/rollback conflict behavior, and auth-gated handlers/tools. Whole-repo gates remain the project standard: all Go tests, vet, build, vulnerability check for security-sensitive surfaces, and whitespace diff check.

## Review Strategy

Run local code review, security review, migration review, and MCP PR review when available. If MCP PR tooling still returns `Transport closed`, record the outage and use GitHub plus local review evidence as fallback.

## Release And Dogfood

If CR-001 is shippable, open PR, merge only on green CI/review closure, cut a milestone release when gates pass, let Watchtower update the server image, verify deployed server version/commit, verify plugin/local daemon parity only if contracts changed, and run one real project-rule dogfood trace.

Manual production SSH/container mutation, production SQL, or flag activation remains a hard stop unless explicitly authorized for this rollout.

## Open Questions

- Whether CR-001 should expose both MCP and REST surfaces or start with MCP plus internal service methods.
- Exact dogfood candidate to promote. Use a real project-scoped rule from the active Engram workflow, not a synthetic fixture.
- Whether cited/ignored needs explicit user/agent feedback endpoints in RG-3 or can be represented as `NO DATA` until the next effectiveness slice.
