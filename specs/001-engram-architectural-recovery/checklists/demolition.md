# Deletion and Demolition Requirements Checklist: Engram Architectural Recovery

**Purpose**: Review whether legacy removal requirements preserve accepted outcomes and prevent
false architecture from surviving source deletion.
**Created**: 2026-08-22
**Feature**: `../plan.md`, `../research.md`, `../evidence/`
**Review Ownership**: Requirements-quality reviewer. `[x]` never means removal has executed.

## Disposition Completeness

- [x] CHK001 Does every recovery-relevant flag, package, route, hook, tool, projection, schema
      family, test/mock/doc claim, and UI/navigation claim have a terminal or temporary
      disposition? [Completeness, FR-021]
- [x] CHK002 Does every temporary or quarantine disposition name an owner, evidence, target
      release, replacement/decision condition, and closure rule? [Clarity, Evidence inventories]
- [x] CHK003 Are absent UI consumers distinguished from absent server/daemon consumers before
      deletion is authorized? [Safety, Package/Route/Hook Inventory]

## Replacement and Coupling

- [x] CHK004 Is every deletion tied to an accepted replacement outcome or an explicit reason no
      replacement is needed? [Completeness, Constitution IX]
- [x] CHK005 Are flag removal requirements coupled to reader, default, manifest, docs, tests,
      mocks, routes, tool advertisement, cache, and UI cleanup? [Coverage, FR-022]
- [x] CHK006 Are dead callback/410/stub behaviors classified as retirement seams rather than
      mistakenly preserved as supported products? [Consistency, Research R-05/R-11]

## Evidence and Rollback

- [x] CHK007 Are consumer inventory, export/backup, compatibility, observation, and rollback
      conditions required before each destructive contraction? [Ordering, FR-025/FR-026]
- [x] CHK008 Are graph, summaries, clusters, vectors, and meta-memory required to choose keep as
      rebuildable projection or delete, with no third unowned state? [Completeness, Constitution IV]
- [x] CHK009 Does the plan prohibit copying demolished implementations while allowing recovery of
      their required outcomes through new contracts? [Consistency, Constitution XIV]

## Notes

- [x] CHK010 Are obsolete current-documentation claims required to be archived or removed rather
      than left beside current guidance without status? [Coverage, Source of Truth]

## Review Notes

- Review iteration: AR-0 requirements-quality review, 2026-08-22.
- Result: **FAIL** — 6/10 checked; 4 findings remain unchecked.
- CHK001 finding: the AR-0 inventories cover flags, source packages, routes, hooks, tools, projections, project-bearing families, and several UI consumers, but no itemized test/mock/doc-claim ledger is present. `FR-021` therefore cannot be shown complete for every named unit.
- CHK002 finding: inventory entries have `owner`, `evidence`, `target_release`, and notes/required proof, but the temporary and quarantine entries do not carry a measurable per-entry closure rule or verifier. The feature-flag exception metadata is not a complete closure contract for the other inventories.
- CHK005 finding: `feature-flag-disposition.json` and `plan.md` name readers, schemas/docs/tests/mocks/routes/tools/UI in some cleanup boundaries, but no requirement couples **each** flag's removal to manifest and cache cleanup (and several group entries do not enumerate the full set). The requested reader/default/manifest/docs/tests/mocks/routes/tool/cache/UI coupling is therefore incomplete.
- CHK010 finding: the AR-0 artifacts use documentation paths as source evidence and mention documentation cleanup, but no normative rule requires each obsolete current-documentation claim to be archived or removed with an explicit status. Stale guidance can remain beside current guidance without a required disposition.


## R2 Review Notes

- Review iteration: AR-0 R2 requirements-quality re-review, 2026-08-22.
- Result: **PASS** — 10/10 checked; no remaining unmet demolition IDs.
- CHK001: The added `legacy-claim-disposition.json` itemizes recovery-relevant test, mock, generated-API, and current-documentation claims; together with the existing flag, package/route/hook/tool/projection, project-bearing schema-family, and UI/current-surface inventories, every named class now has a terminal or temporary disposition.
- CHK002: Temporary and quarantine entries now carry the required owner, evidence, target release, closure rule, and independent closure verifier fields; the shared zero-evidence rule prevents target-release dates, empty denominators, or silence from closing them.
- CHK005: The plan now makes each flag deletion a coupled removal of reader/default, configuration and manifests, cache or payload key, tests, mocks, current docs, routes, tools, UI/navigation claims, package seams, and release assertions; the flag inventory groups enumerate readers and defaults for the controlled flags.
- CHK010: The plan requires every obsolete current-documentation statement to be removed or moved to a clearly labeled historical archive with source/consumer status, and the documentation ledger entries carry matching closure rules and verifiers.


## Final Review Notes

- Review iteration: AR-0 final requirements-quality recheck after R1 remediation and task generation, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked the final source/package/route/hook/flag/project-data/identity-merge/legacy-claim inventories and AR-7 tasks. Test/mock/documentation claims, both `ui/` and `apps/operator-console/`, full coupled flag-removal scope, projection decisions, continuity-slot contraction, rollback prerequisites, and closure/verifier rules are covered.
- Exact regression IDs: none.
- Per-file checked/total: `demolition.md` 10/10.


## R4 Review Notes

- Review iteration: AR-0 R4 requirements-quality regression recheck after R2 task/inventory remediation, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked the temporary `ENGRAM_INJECT_UNIFIED` exception, T074/T079 branch/deletion ownership, T078/T082/T084 dual-surface scans, and closure/verifier fields across all temporary/quarantine ledgers; contraction remains coupled to replacement and rollback evidence.
- Exact regression IDs: none.
- Per-file checked/total: `demolition.md` 10/10.
