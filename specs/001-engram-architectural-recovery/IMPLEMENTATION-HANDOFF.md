# Implementation Handoff — Engram Architectural Recovery

## Status

AR-0 is complete specification authority only. Final cross-artifact analysis is PASS with zero
CRITICAL/HIGH/MEDIUM/LOW findings. Implementation is **not authorized** by this handoff.

## Authoritative Inputs

Read these in order before any implementation decision:

1. `.specify/memory/constitution.md`
2. `spec.md`
3. `research.md`
4. `plan.md`
5. `data-model.md`
6. `contracts/`
7. `quickstart.md`
8. `evidence/`
9. `analysis/fr-sc-task-traceability.md`
10. `analysis/spec-kit-analysis-r6.md`
11. `analysis/pipeline-receipt.json`
12. `tasks.md`

Historical `.agent/specs`, reports, release claims, comments, flags, and legacy plans remain
research inputs only unless the active artifacts above explicitly adopt them.

## First Session Rule

A fresh implementation session may start only after explicit operator authorization. It must
reconcile its exact current source/installed baseline with these artifacts before changing any file.
If the current baseline contradicts a contract, preserve the evidence, update the owning Spec Kit
artifact and affected checklist/analysis, and do not patch around the contradiction.

## Execution Order

Implement exactly one installed release slice at a time:

- **AR-1**: truthful baseline, dead-loop visibility, source/data/client inventories, fixture/backup.
- **AR-2**: V3 anchor/descriptor and additive identity expansion with dual comparison.
- **AR-3**: approved merge manifests, typed-key backfill/cutover, State Plane continuity migration.
- **AR-4**: durable SessionEvidence/ProcessingJob and automatic honest outcome evidence.
- **AR-5**: KnowledgeProposal/Belief/Revision reconciliation authority.
- **AR-6**: task-aware retrieval, render-budget policy, exposure/outcome learning, continuity
  replacement observation.
- **AR-7**: contraction after observation, including coupled legacy/flag/package/route/tool/docs/UI
  cleanup and final projection decision.

Do not take work from a later release just because it is nearby. The exact task IDs, paths,
dependencies, release gates, and ownership are in `tasks.md`.

## Non-Negotiable Gates

- Resolve V3 project scope before every scoped read, write, job claim, merge, import, export,
  cache write, or retrieval selection.
- Preserve evidence, provenance, privacy, revision history, and auditability; unknown or unsafe
  rows belong in quarantine, never a guessed merge.
- Accept evidence before provider processing; durable job state, not timers or idleness, defines
  work.
- Reconcile proposals before active knowledge; one task-aware retrieval path records exposure.
- Process exit does not prove success. Preserve success/partial/failure/abandoned/unknown with
  source and certainty.
- Treat graphs, summaries, vectors, clusters, and caches as rebuildable projections with no write
  authority.
- Preserve issues, documents, vault, collections, code intelligence, Loom, authentication, and
  State Plane as adjacent contracts unless a released task proves a terminal disposition.
- Do not redesign either `ui/` or `apps/operator-console/`; only remove or honestly retire claims
  tied to an accepted deletion decision.
- Before any tag/publication: exact-head SonarQube analysis, correct every finding, re-run exact
  head, require Quality Gate `OK`, then run fresh behavioral proof on that frozen head.

## Required Validation Surface

Use `quickstart.md` and `scripts/recovery/` command contracts. Every release requires its
independent checker/verifier, clean-main build, relevant fixture/fault tests, installed dogfood,
observation receipt, rollback boundary, and worktree housekeeping. A zero denominator is
`not_computable`, never green.

## Operational Policy Gate

Before AR-4 claims SC-005, activate and receipt a valid `RecoveryAcceptancePolicy` evidence
processing objective. Before AR-6 claims SC-010 or cuts over static context, activate and receipt
a valid render-budget declaration. Missing or scope-mismatched declarations block those claims.

## Stop Conditions

Stop and return to the operator/Spec Kit owner if an action requires live database mutation,
deployment/global-profile write, destructive production merge, secret access, an unmade data/privacy
choice, or a scope expansion into new operator-surface design. Otherwise diagnose ordinary tooling
or artifact blockers inside the repository and continue with the owning task/checklist/analyze loop.
