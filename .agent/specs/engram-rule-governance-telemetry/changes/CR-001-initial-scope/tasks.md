---
id: CR-001
slug: initial-scope
status: DRAFT
created: 2026-06-19
plan: .agent/specs/engram-rule-governance-telemetry/plan.md
---

# Tasks: CR-001 Governance Telemetry And Escalation

## Setup

- [x] T001: Verify hook hotfix interruption is closed and local plugin/daemon are on `v6.28.2`.
- [x] T002: Confirm implementation worktree is aligned with `origin/main` at `0857433`.
- [x] T003: Remove temporary `.write-test` probe from the RG-3 spec directory.
- [x] T004: Re-check worktree status before source edits and decide whether to force-add `.agent` spec artifacts.

## RED Tests

- [ ] T005: Add store tests for lifecycle health counts by candidate status, version state, transition action, snapshot status, arbiter run status, and injection event type.
- [ ] T006: Add queue tests for global/kernel escalation, conflict, reject-review, scope risk, canary review, rollback conflict, and stale-cache anomaly groups.
- [ ] T007: Add telemetry aggregation tests for project/rule/event windows, including empty `NO DATA` behavior.
- [ ] T008: Add transition-control tests for promote/reject/archive/restore/pin/unpin authority and required evidence.
- [ ] T009: Add rule-governance snapshot tests for list, pin, rollback success, rollback conflict, and pinned/protected refusal.
- [ ] T010: Add handler/tool auth tests for read-only health/queues and admin/operator transition controls.
- [ ] T011: Add dogfood-report fixture test that can serialize a candidate -> shadow/canary -> active_project trace.

## Implementation

- [ ] T012: Add model DTOs for health, queue items, transition requests/results, snapshot summaries, rollback results, and usefulness metrics.
- [ ] T013: Add rule governance health aggregate queries with bounded limits/time windows.
- [ ] T014: Add exception queue queries and legal-escape handling.
- [ ] T015: Extend rule injection event store with project/rule/event aggregation reads.
- [ ] T016: Add any required additive migration/indexes for RG-3 telemetry event types or query performance.
- [ ] T017: Implement rule-governance transition control service methods over the existing state machine.
- [ ] T018: Implement rule-governance snapshot list/pin/rollback/archive/restore support.
- [ ] T019: Expose backend API/MCP surfaces for health, queues, transition, snapshots, rollback, and usefulness.
- [ ] T020: Add dogfood trace report writer or export function.

## Verification

- [ ] T021: Run targeted tests for models, stores, MCP/worker handlers, and router/grpc if event types changed.
- [ ] T022: Run whole-repo gates: tests, vet, build, vulnerability check, and whitespace diff check.

## Review And PR

- [ ] T023: Run local code review for queue/read-model correctness and no parallel lifecycle model.
- [ ] T024: Run local security review for auth, scope leakage, prompt/evidence redaction, and rollback behavior.
- [ ] T025: Run migration review for additive schema/index and rollback posture.
- [ ] T026: Commit coherent implementation checkpoint.
- [ ] T027: Open PR and invoke MCP PR review; record `Transport closed` fallback if MCP PR tooling remains unavailable.
- [ ] T028: Resolve material review findings and merge only after green CI/review closure.

## Milestone Evidence

- [ ] T029: Update continuity and progress artifacts after implementation/PR.
- [ ] T030: Run milestone release if shippable; verify tag/image/server/plugin/local daemon parity as applicable.
- [ ] T031: Run dogfood trace for one real project rule candidate -> shadow/canary -> active_project, with usefulness or honest low-sample evidence.

