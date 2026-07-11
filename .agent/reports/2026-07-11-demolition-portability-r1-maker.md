# Demolition-portability R1 maker report

Status: **READY_FOR_INDEPENDENT_CHECKER** after the staged/frozen candidate gates
listed below. This report describes the bounded candidate based on
`0c6269908aa810a2248f2bfaf3fca4f9f5791359`; it does not authorize synthesis,
merge, push, tag, release, or external mutation.

The staged candidate owns exactly 15 paths. Its ordinal-LF path digest is
`996b153680df782d4247b6ef72120fba36bfc7cf3fb1015385817e1035ce700f` and its
ordinal-LF status/path digest is
`4761d8020de2f6ba49841291eca5996f7ae928559a69228c383f3ede5df71b18`.
Gitleaks 8.30.0 scanned the staged candidate with no findings.

## Outcome

- Graph tests now boot through the real migrated store, clean rows before the
  pool closes, use the live edge enum, and prove the live foreign key rather
  than trying to create an unreachable dangling row.
- `NodesStore.Create` and `NodesStore.Update` normalize omitted optional
  metadata to `{}`, closing the current NOT NULL contract without adding graph
  retrieval behavior.
- T022 parses the live items array, requires a real high-confidence match, and
  rejects low-confidence or empty-result false greens. Retrieval code is
  unchanged.
- Loom uses one compiled cross-platform test helper. Windows and Linux execute
  the worker behaviors with zero skip, and package coverage is 86.9%.
- The portable helper exposed a live `limitedWriter` short-write defect. The
  master plan was amended before the product edit; the fix now reports the
  original input length only after the retained prefix is fully written and
  still propagates real writer failures.

## Evidence

- `plan-amendment.json` — exact ownership and no-resurrection boundary.
- `red.json` — graph, T022, Loom portability, update metadata, and output-cap
  RED observations.
- `prove-it.json` — controlled mutation/restoration proof for all three slices.
- `gates.json` — Windows/Linux, PostgreSQL, race, coverage, build, vet, broad
  suite classification, and cleanup results.
- `demolition-guard.json` — live/stale/dormant classification and explicit
  removed-v5 exclusions.

## Broad-suite truth

The isolated base intentionally does not include the separately accepted T007
head or the in-flight DB-pool/governance stacks. The whole-repository JSON run
therefore remains red: 34 failed test events across `internal/db/gorm`,
`internal/bulkops`, and `internal/mcp`, plus 20 skips routed to their exact
owners. None is in the owned graph/T022/Loom slices; all focused owned gates,
build, and vet pass. This maker must be checked as a disjoint candidate and then
retested in synthesis with the accepted neighboring heads.

## Required checker work

The independent checker must verify exact path closure, replay RED/GREEN and
Prove-It, attack nil/empty metadata, FK enforcement, cleanup ordering, stale
edge enums, T022 empty/high/low behavior, Windows/Linux helper execution, output
cap alignment and underlying Writer errors, race/coverage, broad-suite
classification, zero database/temp-helper residue, and the full demolition
guard. Any HIGH/CRITICAL finding returns `REVISE_HOLD`.
