# Engram — agent instructions

## Goal and scope

These instructions apply inside Engram; a parent workspace janitor role is not this project's task. For the operator-selected clean restart, read `D:/Dev/engram/.agent/continuity/RESTART.md`, then the latest saved session handoff. Read the current task, continuation and accepted feature contract in the actual worktree. Follow the host instruction hierarchy and governing `.specify/memory/constitution.md`; load only relevant skills. Reconcile stale state with code rather than replaying historical intake or restarting accepted work.

Deliver one useful installable slice. Restore any broken shipped Engram plugin/memory path before feature expansion. Then deliver code search, automatic graph, independent worktrees, operator Workspace and operating documentation. Book Context and new cognitive-memory development follow; this does not defer regressions in existing memory or OpenClaw-plugin compatibility. Updating the OpenClaw host is outside this task. New capabilities and contract changes use the applicable Spec Kit pipeline. A repair within an accepted contract needs focused implementation and verification, not a fresh product specification.

## Ownership and decisions

Root Solo decides, delegates, accepts results and advances delivery. Subagents author architecture, code, technical documents/tests, integrate changes and execute checks, including local Sonar. Root may read status and maintain continuation; it does not become the implementer or test operator. No peer PM/Developer hierarchy.

Assign coherent outcomes, not individual edits. The implementer owns diagnosis, implementation and focused repairs within scope without a new Root assignment for each failing test. Use independent review/QA of the result. Shared files have one writer and one integrator owns the candidate. Parallelize independent tasks, not competing heavy runs on shared resources.

Inspect available facts before asking. Ask for missing product decisions or authority, not resolvable engineering details. A blocked deployment lane does not block implementation or isolated validation. Consume ready results and continue safe independent work; use a nonblocking decision request when supported. Do not enter a blocking `ask` while Root has decision-independent work to accept or dispatch. If none remains, report the exact dependency and wait. Never invent consent.

## Review and evidence

Review changed behavior and affected invariants. Keep related repairs in one assignment; recheck unresolved blockers and impacted behavior, not the whole product after every fix or reviewer/model change.

Fix mandatory gate failures, accepted-behavior violations and credible security/data-integrity defects. Resolve findings in existing review state through the authorized maintainer. Optional suggestions may share one reasoned, tracked follow-up; each does not require a new report, task or user approval. Do not unilaterally close required threads or waive approvals. Additional review needs a concrete unresolved question; no pass-count overrides a real blocker.

Run focused tests during implementation and existing mandatory integration/release checks at their boundary. Report selection, failures and required skips honestly. Test doubles prove their layer, not installed behavior. Product acceptance uses the ordinary browser/client journey and promised corpus/language scope; flags, routes, receipts and version strings are insufficient.

Keep evidence in existing task/review/run records. Add an ADR, manifest or checklist only for a governing requirement or concrete consumer. Reuse only validated, provenance-preserving results. Repeated identical failure requires a different diagnosis, not another full run. Debug report classification from saved events and small tests; do not weaken assertions or inflate timeouts to get green.

## Release and waiting

`docs/RELEASE-PROTOCOL.md` is the single operational source for commands, CI compute limits, release authority and rollback. Mandatory gates remain, including exact-candidate Sonar `OK`, security, supported-platform and migration requirements, except for explicitly recorded operator-approved exceptions with their existing narrow scope. The recorded v6.49.4 PR #531 Sonar exception is `NOT_PROVEN`, never a PASS and never transferable to Workspace. Routine already-authorized actions need no renewed approval.

Finish known repairs, ordinary host/user-path smoke and release inputs before freezing. Use that checkout's tools; never mutate a candidate under validation. One QA owner runs each durable gate job and its recovery; Root consumes terminal evidence and directs delivery, not duplicate jobs or rapid polling of unchanged logs. A short Task wait must not cancel a longer job.

After interruption inspect saved state and use supported recovery. Do not launch a duplicate or full default campaign merely because a wait or `--help` failed. A new candidate requires an evidence-impact decision under existing compatibility rules, not a renamed historical PASS. Changed SHA alone does not require rerunning unaffected suites; changed test inputs, tool environments, analyzer identity or release artifact bytes require their applicable proof.

When mandatory gates pass and actions are authorized, proceed to release and ordinary-consumer verification, not optional polish. Source-ready, published, catalog-available, installed and consumer-proven are distinct states. Publication does not wait for an operator-owned reinstall; installation and consumer proof remain open until observed. Research-only and explicitly source-only tasks retain their smaller requested outcome.

## Invariants and continuation

Preserve Go/PostgreSQL/pgvector, the stdio-daemon/gRPC boundary, typed Source/Checkout/pinned View, authorization, privacy/history, bounded resources and independent dirty worktrees. Do not embed upstream products or hide required capabilities behind incomplete flags. Configuration and emergency stops follow the constitution.

Verify current paths and consumers; do not infer readiness or obsolescence from names. Retire rejected workflows in UI and executable entry points while preserving required data/readers and rollback. One complete change-level consumer map and its tests can cover related removals; no per-symbol paperwork is required. Never reset/clean/stage/overwrite unrelated work, alter another session's authority, expose secrets or force-move published tags.

During a requested session save, finish or hand off owned jobs without starting a new wave; record active writers and uncommitted work. Keep one continuation: candidate, installed state, owners, causal blockers and next delivery action. Update at meaningful transitions, not each poll. Report what the operator can use and what remains; estimate only from evidence with uncertainty. Replace contradictory policy at its source through normal adoption, without new incident ledgers or modifying a running frozen checkout.
