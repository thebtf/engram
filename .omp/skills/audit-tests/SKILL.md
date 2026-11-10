---
name: audit-tests
description: "Use when auditing a test corpus for actual value: 'audit all tests', 'why does each test exist?', or 'clean up useless tests'; also 'составь чеклист всех тестов и проверь, какие из них полезны'. Create and work through an ordered per-test checklist, keeping, fixing, merging or deleting tests as authorized. Don't use for writing one new test or debugging one failure; use tdd or debug instead."
metadata:
  task_classes: [codebase-inventory, test-writing, code-review-correctness, implementation-slice]
  effect_class: write-local
  surfaces: [omp]
  refusal: allowed
---

# Audit Tests

## Purpose

Prevent a test corpus from becoming unmaintainable by establishing what each test protects and whether it actually detects the relevant failure. A small sample, a passing suite or a coverage number is not an audit of the rest.

## Workflow

1. Bind the requested scope, checkout and baseline revision. Preserve other work and identify the test runners, languages, fixtures and generated or parameterized cases in scope. Reuse an existing audit and its checklist when continuing; do not restart it or silently change its baseline.
2. Inventory all tests in scope from source and available runner discovery. Reconcile duplicate declarations and cases; distinguish static declarations, expanded cases and executed tests. Record dynamic cases, discovery failures, skipped profiles and other gaps explicitly. Append subsequently discovered tests without renumbering existing entries.
3. Create one persistent Markdown or JSON checklist under the project's existing task-record owner, or the user-specified path. For every entry record a stable number, name, location and purpose. Leave purpose unknown and status pending until assessed; never manufacture a reason from the name. Record the baseline, discovery method, limitations and progress. Keep the user's existing checklist as the authority on continuation.
4. Review entries sequentially. Read the test and relevant implementation, fixtures and neighboring tests. For each entry record: the concrete regression or contract and beneficiary; the actual setup, stimulus and oracle and whether a plausible defect makes it fail; overlap and meaningful differences in behavior, layer, security boundary or input; isolation, cost, reliability, skips and execution prerequisites. State what was observed and what remains unverified.
5. Decide **keep** when it protects distinct useful behavior; **fix** when the intent is useful but the test is weak or misleading; **merge** when another test protects the same behavior at the same boundary and its useful assertions can be preserved; **delete** when no defensible unique value remains. Compare the implementation and assertions before declaring duplication. Apply edits within the user's authority; an explicit request to clean up the corpus authorizes ordinary test deletions, fixes and merges, not unrelated or unsafe effects.
6. Run focused checks appropriate to each changed behavior, confirming the intended tests actually ran. Preserve assertion strength and isolation. Where a check cannot safely run, record the precise limitation and leave the affected verification or integration pending rather than claiming a pass. Group related changes into coherent delivery slices under the project's existing integration rules.
7. Mark each checklist entry with its reason, decision, evidence and status after review; distinguish a reviewed decision from a completed edit, verified change and integrated change. Keep stable links to replacements for merged or deleted tests. Track unreviewed entries and inventory gaps; do not describe the corpus as audited until every in-scope entry and gap has been dispositioned.
8. Report counts by actual status, the next entry, meaningful changes and proof limits. Continue until the accepted audit outcome is complete; follow the existing project owner for integration, delivery and any subsequent assignment.

## Gotchas

- A mocked response or mocked authorization verifies the consumer of that mock, not the real server's enforcement. Keep that boundary in the checklist.
- An HTTP 408 fixture is not proof that a timeout elapsed. Match test names and assertions to the exercised behavior.
- A successful runner exit with zero matching tests is not a pass. For asynchronous UI tests, a settled network request alone may precede application processing; prove the relevant processing has settled before asserting the final state.
- Similar inputs are not automatically duplicates: negative, authorization, serialization and integration cases can protect different contracts even if they share code. Conversely, a test repeating the same oracle at the same boundary may add no value.

## Boundaries and refusal

- Respect report-only scope: record proposed changes without editing tests when edits were not authorized.
- Do not erase shared data, alter credentials, bypass authorization or disturb another writer to make a test pass. Hold only the unsafe action and continue independent work.
- Do not invent an audit result for an undiscovered, skipped or unexamined test. Do not weaken assertions or inflate timeouts merely to get green.
- If exclusive ownership, a required effect authorization or a safe verification environment is unavailable, record the exact blocker and re-entry; do not claim completion.

## Output

The project task record contains one numbered checklist (Markdown or JSON) with inventory provenance, each test's name, location, purpose, evaluation, decision, evidence and status, plus explicit gaps. Changed tests remain in their owning source files. The final report identifies the checklist, status counts, verified behavior and remaining limits.

## Reference Routing

- Read `evals/pressure.md` when validating that this workflow resists premature completion or superficial test-value judgments.
- Read `evals/trigger-eval.json` when checking routing examples.
