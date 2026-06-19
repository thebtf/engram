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

- [x] T005: Add store tests for lifecycle health counts by candidate status, version state, transition action, snapshot status, arbiter run status, and injection event type.
- [x] T006: Add queue tests for global/kernel escalation, conflict, reject-review, scope risk, canary review, rollback conflict, and stale-cache anomaly groups.
- [x] T007: Add telemetry aggregation tests for project/rule/event windows, including empty `NO DATA` behavior.
- [ ] T008: Add transition-control tests for promote/reject/archive/restore/pin/unpin authority and required evidence.
- [x] T009: Add rule-governance snapshot tests for list, pin, rollback success, transition-created rollback, rollback conflict persistence, conflict queue surfacing, non-committed status refusal, and pinned/protected refusal.
- [ ] T010: Add handler/tool auth tests for read-only health/queues and admin/operator transition controls. PARTIAL: project-scoped read-only MCP health/queue/snapshot/usefulness handler tests, all-project admin gate tests, no-identity rejection, and admin transition/pin/rollback handler tests added; operator archive/restore controls remain.
- [ ] T011: Add dogfood-report fixture test that can serialize a candidate -> shadow/canary -> active_project trace.

## Implementation

- [ ] T012: Add model DTOs for health, queue items, transition requests/results, snapshot summaries, rollback results, and usefulness metrics. PARTIAL: health, queue, snapshot summary, rollback conflict result, and telemetry usefulness DTOs added.
- [x] T013: Add rule governance health aggregate queries with bounded limits/time windows.
- [ ] T014: Add exception queue queries and legal-escape handling. PARTIAL: read-only queue groups now cover candidate escalation/conflict/reject/scope risk, canary review, persisted rollback conflict, and stale-cache anomaly with pre-limit candidate filtering and MCP evidence redaction; explicit legal-escape shaping for future mutation conflicts remains pending.
- [x] T015: Extend rule injection event store with project/rule/event aggregation reads.
- [ ] T016: Add any required additive migration/indexes for RG-3 telemetry event types or query performance.
- [x] T017: Implement rule-governance transition control service methods over the existing state machine.
- [ ] T018: Implement rule-governance snapshot list/pin/rollback/archive/restore support. PARTIAL: list, pin, transition-created rollback success, rollback conflict persistence/queue surfacing, non-committed status guard, and protected/pinned rollback refusal are implemented; archive/restore support remains.
- [ ] T019: Expose backend API/MCP surfaces for health, queues, transition, snapshots, rollback, and usefulness. PARTIAL: MCP surfaces are wired for health, queues, rule-governance snapshots, usefulness, transition, pin, and rollback; separate REST/dashboard API surface remains unimplemented unless explicitly descoped.
- [ ] T020: Add dogfood trace report writer or export function.

## Verification

- [x] T021: Run targeted tests for models, stores, MCP/worker handlers, and router/grpc if event types changed. Evidence: `go test ./internal/mcp ./internal/db/gorm ./internal/worker`.
- [x] T022: Run whole-repo gates: tests, vet, build, vulnerability check, and whitespace diff check. Evidence: `go test ./...`, `go vet ./...`, `go build ./cmd/engram ./cmd/engram-server`, `govulncheck ./...`, `git diff --check`.

## Review And PR

- [x] T023: Run local code review for queue/read-model correctness and no parallel lifecycle model. Evidence: native review agent Dewey found transition-created snapshot and rollback-conflict surfacing gaps; both fixed with regression tests.
- [x] T024: Run local security review for auth, scope leakage, prompt/evidence redaction, and rollback behavior. Evidence: native review agent Sagan found evidence redaction bypass and project-scoped health arbiter-run leakage; both fixed with regression tests.
- [x] T025: Run migration review for additive schema/index and rollback posture. Evidence: no migration files changed in this slice; rollback posture reviewed through persisted conflict status and non-committed snapshot guard tests. Query/index work remains T016.
- [x] T026: Commit coherent implementation checkpoint.
- [x] T027: Open PR and invoke MCP PR review; record `Transport closed` fallback if MCP PR tooling remains unavailable. Evidence: PR #324 opened; `mcp__pr.pr_invoke` returned `Transport closed`; CodeRabbit status reported SUCCESS in GitHub status rollup.
- [ ] T028: Resolve material review findings and merge only after green CI/review closure.

## Milestone Evidence

- [ ] T029: Update continuity and progress artifacts after implementation/PR.
- [ ] T030: Run milestone release if shippable; verify tag/image/server/plugin/local daemon parity as applicable.
- [ ] T031: Run dogfood trace for one real project rule candidate -> shadow/canary -> active_project, with usefulness or honest low-sample evidence.

