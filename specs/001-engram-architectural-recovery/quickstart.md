# Recovery Validation Quickstart

**Purpose**: Acceptance guide for the implementation sessions that execute AR-1 through AR-7.
This is not implementation code and AR-0 does not run these scenarios against live systems.

## Safety Boundary

- Use an isolated PostgreSQL fixture and an isolated data directory for every destructive or
  migration scenario.
- Never use a production credential, raw transcript, raw secret, or unredacted export in a fixture
  or receipt.
- Build and release only from clean current `main` after the assigned slice is merged and reviewed.
- A production migration, merge application, contraction, deployment, or global configuration
  change needs separate explicit operator authorization.

## Shared Prerequisites

The implementation MUST deliver these stable, repository-local command surfaces before AR-1 can
claim acceptance. AR-0 specifies them but does not execute or create implementation scripts:

1. Check out the exact accepted release commit and verify `git status --short` is empty.
2. Run `make build` and `go test ./...` with the release's documented test environment.
3. Create or reset a redacted legacy fixture with:
   `pwsh -NoProfile -File scripts/recovery/prepare-legacy-fixture.ps1 -FixtureRoot .agent/tmp/recovery-fixture`.
   The command MUST emit schema version, redacted selector inventory, and backup/export reference.
4. Start the isolated fixture server and supported adapter fixtures with:
   `pwsh -NoProfile -File scripts/recovery/start-fixture-server.ps1 -FixtureRoot .agent/tmp/recovery-fixture`.
   The command MUST accept only non-secret fixture credentials and emit a health receipt.
5. Execute the AR-1 baseline scenario with:
   `pwsh -NoProfile -File scripts/recovery/run-recovery-scenario.ps1 -Release AR-1 -Scenario baseline -FixtureRoot .agent/tmp/recovery-fixture`.
   Later slices use the same command with their declared release and scenario names.
6. Validate the latest scenario receipt with:
   `pwsh -NoProfile -File scripts/recovery/verify-recovery-receipt.ps1 -Receipt .agent/tmp/recovery-fixture/receipts/latest.json`.
7. Provider degradation is a test condition, not a reason to skip evidence intake.

## AR-1: Truth, Fences, and Baseline

1. Run the read-only identity, flag, route, package, and project-bearing-data inventories.
2. Invoke every supported session completion adapter against the fixture, including the historical
   callback form.
3. Verify dead or retired callback behavior is visible and actionable; no callback may silently
   claim success when no durable outcome state exists.
4. Capture baseline denominators for identity variants, evidence items, processing states,
   candidates/revisions, query/exposure, and outcomes.

**Expected outcome**: One machine-readable baseline receipt names every denominator and every
missing or retired path. No new selector-only project is minted during the observation window.

## AR-2: V3 Identity Expand

1. Create a repository anchor fixture and present it from a primary checkout, worktree, clone,
   nested directory, moved directory, branch, and renamed remote.
2. Confirm each supported adapter receives the same canonical project key and records the V3
   descriptor version.
3. Present missing, malformed, duplicate/forked, unknown, directory-scoped, nested-Git, and
   non-Git anchors.
4. Verify valid writes dual-compare legacy identity and typed key; invalid/ambiguous writes mutate
   neither representation.

**Expected outcome**: Cross-language descriptor vectors pass byte-for-byte; identity receipts show
one key for accepted same-project contexts and zero writes for failed-closed cases.

## AR-3: Identity Merge and Cutover

1. Generate a dry-run merge manifest from the legacy fixture; verify every table/payload/cache/job
   family is counted or quarantined.
2. Review merge groups, conflict classes, privacy result, and target selection before apply.
3. Apply one accepted group, interrupt a bounded backfill, resume it, then rerun the completed
   group.
4. Compare before/after counts, provenance, privacy, foreign-key/null rates, redirects, and
   idempotency; rehearse rollback to the prior compatible read boundary.
5. Run the installed observation release and verify zero new legacy-only writes before any
   contraction task is eligible.

**Expected outcome**: Every accepted source row has a typed key; unresolved rows remain in the
quarantine ledger; the receipt identifies backup/export and rollback references.

## AR-4: Durable Evidence and Outcome

1. Read the active `RecoveryAcceptancePolicy` receipt and confirm its finite positive evidence
   processing objective, eligible-event definition, owner, baseline, approval, and rollback
   reference. If absent or scope-mismatched, record SC-005 as `not_computable` and stop AR-4 exit
   evaluation.

2. Send normal completion, duplicate delivery, abrupt client loss, server restart, malformed,
   oversized/redacted, provider-unavailable, and replayed evidence events.
3. Verify acknowledgment follows durable intake and each item has one idempotent receipt.
4. Drive a worker lease loss and restart; observe retry, terminal failure, and quarantine behavior.
5. Exercise supported-host completion evidence, explicit feedback, timeout, and abrupt termination.

**Expected outcome**: Every accepted item reaches `completed`, `terminal_failure`, or
`quarantined`; outcomes distinguish success, partial, failure, abandoned, and unknown with source
and certainty; backlog and failure metrics have named denominators.

## AR-5: Knowledge Reconciliation

1. Load the controlled evidence corpus for create, strengthen, revise, supersede,
   contradiction-pending, reject-noise, reject-policy, and no-op-duplicate.
2. Process it through the durable job pipeline.
3. Verify one active belief per resolved proposition/applicability context, append-only revisions,
   retained evidence links, policy enforcement, and exception review for contradictions.
4. Migrate selected legacy memory/candidate fixtures and verify each result is mapped or
   quarantined without fabricated confidence or outcomes.

**Expected outcome**: The decision matrix is complete and deterministic; no new knowledge appears
without a recorded reconciliation decision.

## AR-6: Task-Aware Retrieval and Learning

1. Read the active `RecoveryAcceptancePolicy` receipt and confirm its render budget ID, finite
   positive item/token limits, corpus scope, owner, baseline, approval, and rollback reference. If
   absent or scope-mismatched, record SC-010 as `not_computable` and stop AR-6 cutover evaluation.

2. Run the curated positive, negative, stale, superseded, cross-project, privacy, adversarial, and
   empty-result retrieval corpus against a task/query source.
3. Verify filter-before-ranking, bounded render packet, identity/provenance/rationale fields, and
   one exposure record per delivery.
4. Repeat with embeddings/reranking unavailable and verify lexical degraded behavior plus health
   state.
5. Run supported-host completion flows and connect exposures to automatic outcome evidence.
6. Compare the shadow context path before cutover, then prove static recent-memory dumping is no
   longer the normal path.

**Expected outcome**: Relevant top-five, zero-leakage, stale-result, packet-budget, exposure, and
outcome criteria in `spec.md` pass on the measured corpus and installed dogfood.

## AR-7: Contraction and Closure

1. Confirm all prerequisite observation windows and migration receipts are green.
2. Run source/configuration/package/route/hook/tool/UI/documentation scans for legacy identity,
   VNext/V7/maturity flags, duplicate injection, dead callback, old candidate orchestration,
   continuity slot, and projection dispositions.
3. Rebuild every retained projection from authoritative records; verify removed projections have
   export/rollback receipts.
4. Run exact-head SonarQube analysis; correct every finding; rerun the scan on the corrected exact
   head; and require an `OK` Quality Gate before any final behavioral proof.
5. After that `OK` head is frozen, rerun the exact contraction scan and run fresh install, every
   supported upgrade source, backup/restore rehearsal, and installed dogfood verification from
   clean `main`. A source change after Sonar requires repeating steps 4 and 5.

**Expected outcome**: Gates A-I pass; every inventory item has a terminal disposition; there are no
architecture-era runtime branches, legacy-only writes, unresolved identity quarantines, or hidden
working-surface implementation tasks.

## Pause, Resume, and Rollback Rules

- `paused` stops automatic processing after durable acceptance; it does not delete evidence or
  reinterpret job state.
- Retry resumes only eligible jobs using their retained idempotency key and attempt history.
- Before destructive contraction, rollback returns application reads to the last accepted
  compatibility boundary while retaining audit/history.
- After destructive contraction, restore the rehearsed pre-contraction backup/export into the same
  data class and reinstall forward; never issue ad hoc reverse mutation.

## Evidence Receipt Checklist

Every scenario stores a non-sensitive receipt with source commit, built/installed version, schema
set, capability health, input fixture class, row/job counts, terminal states, redacted mismatch or
quarantine counts, rollback reference, dogfood window, verifier verdict, and artifact links.
