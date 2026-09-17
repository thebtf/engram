# Feature 011 D-A Evidence Quickstart

**Purpose**: This is a later implementation/acceptance guide for the D-A contract, not evidence that D-A is implemented, released, installed, or available at an operator origin. Run every command from the exact D-A candidate worktree, never the dirty primary checkout or a frozen release lane.

## Preconditions

1. Bind the implementation candidate's root, HEAD, tree, branch, and owned paths. Preserve unrelated primary and sibling worktree state.
2. Use a disposable PostgreSQL database, real authenticated Go server, built console, real linked worktrees, and a concurrent ordinary MCP client. Do not use production data, disabled auth, direct grant rows, browser filesystem access, credentials, or hand-written ContextRefs.
3. Create two linked worktrees for one repository with distinct readable branch/device labels and divergent saved source markers. Create an eligible corpus with more than 50 candidates, one direct relation, one reverse relation, one off-page neighbor, and a documented unsupported/dynamic limitation. Provide one real provider for a non-lexical conceptual query; lexical or degraded output is `NOT_PROVEN`.
4. Prepare historical graph rows, a book-produced versioned document with two versions and `source_book_job_id`, an accepted nonterminal legacy book job, Rules, Issues, and approved allowed/denied readers. Before residual-job transition in the existing single-container deployment, stop every old book-writer process; do not introduce a lease/heartbeat. Include `internal/retrieval/hybrid.go` Tier2 `Traverse`, historical graph reads, `/api/context/search`, `internal/graph`, and UCI graph in the retained-reader baseline.
5. The operator scenario starts at the normal homepage. Fixture setup may prepare the disposable corpus through approved application seams, but it must not perform navigation, login, source selection, graph writes, or evidence opening on the operator's behalf.
6. DA01/DA02/DA04 journey proof and DA03 retirement proof may be recorded independently once their own candidate dependencies are green. Composite source acceptance requires both records. The release gate remains closed until the supported browser/origin decision and an installed normal-homepage DA01–DA04 walkthrough are recorded.

## Focused implementation checks

Run only checks affected by the implementation seam. Existing commands are invoked from `apps/operator-console/` in the bound candidate:

```powershell
npm run build
npm run test:browser -- tests/browser/responsive-navigation.spec.ts tests/browser/surface-integrity.spec.ts tests/browser/truth-accessibility.spec.ts tests/browser/code-tab-lease.spec.ts
npm run test:browser:live -- tests/live/operator-code-explorer.spec.ts tests/live/operator-code-s2-topology.spec.ts tests/live/index-intent.spec.ts tests/live/document-operations.spec.ts
```

Mock Playwright evidence proves only interaction. Confirm the live configuration selects the intended files; a test filename outside its explicit `testMatch` is not evidence. Use focused binding/catalog/UCI/store/retirement tests supplied by T002–T005 and record a database `SKIP` as not run.

## DA01 — Homepage discovery and named identity

1. Open the normal homepage in a fresh supported browser session.
2. Complete only the visible, chosen supported sign-in/onboarding flow.
3. Find Workspace through visible Home or navigation controls; do not use direct `/code` navigation.
4. Select one Repository and Working copy by readable identity, then select or confirm its Indexed snapshot through the offered context.
5. Repeat in a second tab with the other working copy and a denied subject.

**Pass**: No UUID, direct hidden route, SQL, raw binding action, synthetic admin, or localhost substitution is needed. Empty source, no grant, no snapshot, and offline owner are distinguishable and do not disclose unavailable source details.

## DA02 — Freshness, coverage, search, source, relation, and evidence

1. Inspect the selected snapshot's revision/time, coverage, supported language scope, and Ready/Updating/Needs indexing/Failed/Newer snapshot state.
2. Search a known symbol and one non-lexical conceptual intent over more than 50 eligible candidates. The conceptual result must come from the configured real provider; record the provider response and returned total/continuation separately from page size. Lexical or degraded output is `NOT_PROVEN` and cannot support source acceptance.
3. Open a source span, then direct and reverse relations. Open an off-page neighbor and its selected evidence through the source boundary.
4. Publish a newer View for working copy A through its normal watcher/reconcile owner. Confirm A's old View remains until explicit switch and working copy B remains unchanged.
5. Revoke a grant after application work but before release; try stale/mismatched continuations and binding evidence.

**Journey evidence pass (independently recordable)**: Source, relation, evidence, and continuation all remain bound to the selected authorized View; the real-provider non-lexical >50-candidate query succeeds; and limitations are explicit. Denied/revoked/mismatched requests disclose no contextual body, identifier, count, edge, or evidence. Lexical/degraded evidence remains `NOT_PROVEN`.

## DA03 — Retirement and historical preservation

1. Start from Home and old Graph/Books bookmarks. Confirm neither exposes a manual graph editor or plaintext uploader.
2. In a separate negative lane invoke every approved old HTTP/MCP writer, inspect tool listing/dispatch, enable prior flags, and repeat after restart.
3. In the existing single-container deployment, first prove no old book-writer process remains live. Then transition the quiesced legacy job to `failed` with the retirement reason, preserving `source_book_job_id` and partial documents; rerun the transition to prove idempotence. Do not introduce a lease/heartbeat.
4. Read historical documents, both versions, comments/provenance, Rules, Issues, `internal/retrieval/hybrid.go` Tier2 `Traverse` behavior, historical graph reads, current `/api/context/search`, `internal/graph`, and UCI code graph through their existing owners.

**Retirement evidence pass (independently recordable)**: A removed menu entry is not sufficient. Every mapped writer remains unavailable; no old writer was live before residual failure; the failed-with-reason transition is idempotent and preserves provenance/partial documents; and each named reader/data item retains its prior ACL and meaning.

## DA04 — Accessibility, language, and acceptance record

1. Repeat DA01–DA02 with keyboard only in RU and EN at 1440, 980, and 390 CSS-pixel widths and actual 200% browser zoom; verify existing zh navigation behavior.
2. Confirm visible focus, live state announcements, accessible repository/working-copy selectors, non-visual relation list, evidence disclosure, Escape/back behavior, and no page-wide horizontal overflow other than local code scrolling.
3. Give an independent operator only the normal origin, chosen supported credentials/onboarding, readable workspace names, and task questions. Record actions, dead ends, and hints needed.

**Pass**: The operator can find an implementation, explain a direct/reverse relation and its evidence, and identify why a selected source is incomplete without author guidance or technical identifiers.

## Evidence boundaries

- Retain the candidate identity, selected focused outputs, browser/daemon/provider configuration without secrets, retired-writer denominator, and observed-versus-expected journey record in the existing Feature 011 acceptance surface.
- T008a records only exact-candidate DA01/DA02/DA04 journey evidence, including the real-provider non-lexical >50 query; it is `NOT_PROVEN` if that query is lexical or degraded. T008b records only DA03 retirement evidence. Composite source acceptance is recorded only after both records are green.
- Do not treat source-built localhost fixtures, synthetic receipt encoders, mock API interaction, or a prior promotion manifest as installed proof. The release gate remains closed until the browser/origin decision and installed normal-homepage DA01–DA04 walkthrough; this contract amendment performs no implementation, deployment, release, publication, or production mutation.
