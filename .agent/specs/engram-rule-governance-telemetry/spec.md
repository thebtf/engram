---
status: ACTIVE
slug: engram-rule-governance-telemetry
created: 2026-06-19
modified: 2026-06-19
type: rule-governance-milestone
parent_spec: engram-rule-governance-arbiter
owning_milestone: engram-rule-governance-arbiter/RG-3
source_prd: .agent/specs/engram-rule-governance-arbiter/prd.md
source_architecture: .agent/specs/engram-rule-governance-arbiter/architecture.md
source_rg0_spec: .agent/specs/engram-rule-domain-contract/spec.md
source_rg1_spec: .agent/specs/engram-rule-arbiter-background/spec.md
source_rg2_spec: .agent/specs/engram-rule-injection-router/spec.md
registry_mode: legacy-slug-only
open_crs: [CR-001]
---

# Feature: Engram Rule Governance Telemetry (RG-3)

## Overview

RG-3 makes the rule governance loop inspectable and reversible. RG-0 introduced rule lifecycle state, RG-1 added proposal-only arbiter evaluation, and RG-2 added bounded rule injection with rule-specific injection events. RG-3 turns those substrates into operator-facing data surfaces and transition controls: lifecycle health, exception queues, promotion evidence, rollback/archive/restore, pin/unpin, canary/usefulness metrics, and dogfood proof for one real project rule moving from candidate to active project rule.

This milestone does not build a broad operator-console redesign. It exposes the truthful backend surfaces that a dashboard can consume later.

## Scope Decisions

- RG-3 owns rule governance read models, exception-oriented queues, bounded transition controls, and dogfood evidence.
- RG-3 consumes `rule_candidates`, `rule_versions`, `rule_transition_log`, `rule_governance_snapshots`, `rule_arbiter_runs`, `rule_arbiter_evaluations`, and `rule_injection_events`.
- RG-3 may extend telemetry/read models additively when existing RG-2 events do not carry enough usefulness signal, but it must not rewrite the router schema without need.
- RG-3 must not let background, LLM, or system actors activate active, global, or kernel states.
- RG-3 must not resurrect v5 graph, cross-encoder, rerank, or server-side MCP HTTP transport paths.

## Current Source Constraints

- `RuleGovernanceStore` already supports candidates, draft creation, transitions, snapshots, arbiter runs/evaluations, renderable rule versions, and legacy behavioral-rule fallback.
- `RuleInjectionEventStore` currently records events and lists by session only. RG-3 needs project/rule/event aggregations for lifecycle health and queues.
- Existing MCP governance tools manage bulk-operation snapshots, not rule-governance snapshots. RG-3 must add rule-governance-specific surfaces rather than overloading the bulk snapshot tools.
- REST rule handlers still operate on legacy `behavioral_rules` update/delete. RG-3 controls must target canonical rule lifecycle state.
- SocratiCode and Serena were unavailable in this session with `Transport closed`; exact file reads and Go docs were used as fallback evidence.

## Functional Requirements

### FR-1: Lifecycle Health Read Model

The system MUST expose lifecycle health for rule governance entities.

Acceptance:

- Health includes counts by candidate status, version state, arbiter run status, transition action, injection event type, and recent snapshot status.
- Health includes stale or missing data as `NO DATA`, not fabricated metrics.
- Health can be filtered by project and bounded by time window/limit.
- Health reads are side-effect free and do not call LLM, router selection, or background workers.

### FR-2: Exception-Oriented Queues

The system MUST expose queues grouped by action reason instead of requiring the operator to rank every rule candidate manually.

Minimum queue groups:

- global/kernel escalation;
- conflicts with active rules;
- reject-review / anti-capture holds;
- unclear scope or private-scope risk;
- canary result review;
- rollback/archive/restore conflicts;
- stale-cache or revoked-rule resurfacing anomalies.

Acceptance:

- Queue items include entity id, entity type, project/scope, reason, evidence handles, last activity timestamp, and recommended next actions.
- Empty queues return an explicit empty list and count.
- Queue reads do not mutate candidate or rule state.

### FR-3: Transition Controls

The system MUST expose authorized transition controls for promote, reject, supersede, archive, restore, pin, unpin, and rollback where the state machine allows them.

Acceptance:

- Active, shared, global, and kernel transitions require actor, actor kind, reason, nonblank evidence handles, and snapshot handle when required by the state machine.
- Background, LLM, and system actors cannot activate active/global/kernel states.
- Global/kernel promotion requires operator/admin authority.
- Invalid or conflict-prone transitions fail atomically with typed reasons.
- Controls are available through backend API/MCP surfaces suitable for later UI.

### FR-4: Rule Governance Snapshots And Rollback

The system MUST make rule-governance snapshots listable, pinnable, restorable, and rollback-conflict aware.

Acceptance:

- Snapshot list uses `rule_governance_snapshots`, not bulk-op snapshots.
- Pinned snapshots cannot be pruned/overwritten by automatic maintenance.
- Rollback refuses when current rule/version state conflicts with the snapshot before-state.
- Rollback result includes restored ids, conflict ids, and audit/transition evidence.

### FR-5: Canary And Usefulness Metrics

The system MUST aggregate canary/usefulness signals from lifecycle and injection events.

Minimum signals:

- emitted kernel/contextual events;
- deferred budget and suppression reasons;
- fallback legacy and router errors;
- cited, ignored, conflicted, superseded, and decayed when available;
- canary samples and explicit operator/agent acknowledgement.

Acceptance:

- Low-sample canary remains pending and is not promoted automatically.
- Missing event classes are reported as `NO DATA`.
- Usefulness metrics are advisory and never directly activate a rule.

### FR-6: Stale Cache And Revocation Safety Queue

The system MUST surface anomalies where plugin cache or legacy fallback could reintroduce archived, rejected, superseded, or prompt-unsafe rule guidance.

Acceptance:

- RG-3 can read router metadata/events that identify stale/fallback behavior.
- Stale-cache anomalies appear in an exception queue.
- The queue does not restore or reactivate the rule automatically.

### FR-7: Auth And Scope Boundaries

Transition controls MUST enforce existing auth tiers and scope boundaries.

Acceptance:

- Admin/operator-only controls reject unauthenticated or non-admin callers.
- Project-level actions must not alter global/kernel state.
- Secret/private evidence handles are redacted or held for review.
- Read-only health/queue surfaces expose no raw secret material.

### FR-8: Dogfood Trace

The milestone MUST produce one real dogfood trace for a project rule lifecycle.

Acceptance:

- Trace includes candidate creation, evaluation/proposal evidence, shadow or canary state, active_project promotion, router/injection telemetry, and rollback/snapshot availability.
- Trace records observable usefulness or a clear `NO DATA`/low-sample state.
- Dogfood evidence is written to `.agent/reports/` and linked from continuity.

## Non-Functional Requirements

- No hot-path LLM calls in health, queue, transition, or session-start reads.
- All schema/API changes are additive and flag-safe.
- Read models are bounded by limit/time window and indexed query paths.
- Transition writes are transactional and audit-backed.
- Empty data is represented honestly through legal escapes.
- Backend truth comes before broad dashboard/UI work.

## User Stories

### US1: Operator Reviews Exceptions

As the operator, I want grouped rule governance queues so I review important exceptions rather than manually ranking every rule.

Acceptance:

- Queues are grouped by reason.
- Queue items include evidence handles and next action hints.
- Empty queues are explicitly empty, not inferred healthy.

### US2: Rule Changes Are Reversible

As the platform steward, I want active/global/kernel rule transitions to have snapshot and rollback evidence, so bad rules can be safely reversed.

Acceptance:

- Active/global/kernel transitions require snapshots.
- Snapshot list and rollback surfaces target rule governance snapshots.
- Rollback conflict tests prove refusal on divergent current state.

### US3: Canary Decisions Use Evidence

As the Creator, I want canary/usefulness metrics before project/global promotion, so durable rules are based on observed value rather than session enthusiasm.

Acceptance:

- Metrics show emitted/deferred/suppressed/cited/ignored signals where available.
- Low-sample canary stays pending.
- Metrics cannot promote rules by themselves.

## Edge Cases

- Empty telemetry store: return `NO DATA` or empty queues with counts.
- Missing snapshot handle for active transition: reject with typed error.
- Rollback requested for pinned/protected or divergent state: refuse with conflict details.
- Stale plugin cache emits revoked rule: queue anomaly; do not reactivate.
- Canary has one positive sample only: remain pending.
- Evidence handle contains private material: redact/hold for review.
- RG-2 telemetry store unavailable: prompt delivery remains unaffected; RG-3 health reports telemetry unavailable.

## Out Of Scope

- Broad operator console redesign.
- Background LLM arbiter changes beyond consuming existing evaluations.
- Router selection changes beyond minimal telemetry/read-model extensions.
- Automatic global/kernel promotion.
- Production flag activation without explicit rollout authorization.
- v5 graph/rerank/cross-encoder resurrection.

## Success Criteria

- [ ] Lifecycle health read model/API exists and is tested.
- [ ] Exception queues are grouped by reason and side-effect free.
- [ ] Transition controls enforce actor, reason, evidence, authority, and snapshot gates.
- [ ] Rule-governance snapshot list/pin/rollback/archive/restore behavior is tested.
- [ ] Canary/usefulness metrics consume rule injection telemetry without automatic promotion.
- [ ] Stale-cache/revocation anomalies are surfaced as queue items.
- [ ] Dogfood trace demonstrates candidate -> shadow/canary -> active_project with observable usefulness or honest low-sample/NO DATA evidence.
- [ ] Targeted tests and whole-repo gates pass or blockers are recorded.
