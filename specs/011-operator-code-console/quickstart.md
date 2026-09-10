# Feature 011 Evidence Quickstart

**Purpose**: This is an implementation and acceptance evidence plan, not evidence that Feature011 is implemented, released, or installed. Run every command from the exact Feature011 candidate worktree, not the primary checkout. The current candidate is bound by plan setup to `specs/011-operator-code-console`.

## Preconditions

1. Confirm the candidate worktree, current HEAD, clean/owned planning paths, Feature011 selector, and Constitution 2.0.0 before a runtime slice begins. Preserve unrelated primary-checkout work.
2. Use an isolated disposable PostgreSQL database and a real authenticated Go server configured only with disposable credentials. Do not set disabled-auth, expose a master token to Playwright, attach production data, or use a browser path/credential to reach a workstation.
3. For S2, bind the exact R-A installed UCI candidate used by the fixture. It must make a real semantic result available for the selected View; lexical/degraded output is valid only for the corresponding truthful-state scenario, not the primary semantic-path acceptance.
4. Use two real linked Git worktrees with divergent saved content and a concurrent ordinary MCP client. Do not replace their topology with copied folders or mocked ContextRefs.
5. Create and revoke explicit browser grants only through the authenticated `CodeGrantApplication` Source-owner routes; seed collection data exceeding 200 Rules and 100 Issues. Fixture setup must not write a grant row directly, use disabled auth, or substitute an administrator role for the exact `ci_checkouts.owner_principal` predicate.

## Existing Console Checks

The current package manifest verifies these commands. They are useful local checks but do not prove real Go/DB behavior:

```powershell
cd D:/Dev/engram/.agent/worktrees/operator-code-console-r1/apps/operator-console
npm ci
npm run build
npm run test:seam
npm run test:parity
npm run test:browser
```

`npm run test:browser` uses the mock server configured by `playwright.config.ts`; record it as interaction evidence only. `npm run parity` validates the curated promotion manifest and does not read `.od`.

## S1a — Honest Shell Evidence

1. Build the console and direct its normal API target to the real disposable Go server.
2. Open Graph, Books, and Rules from clean browser state with Settings closed. Retain the browser network trace.
3. Assert zero memory-body requests made solely for shell labels and zero settings-owned config/domain/model requests while Settings remains closed.
4. Open Settings and confirm its normal authorized requests begin; test keyboard open, Escape, focus restoration, route redirect behavior, and cleanup.
5. Hard reload each page. At 1440, 980, and 390 CSS pixels and 200% zoom, verify unknown count is distinguishable from zero in RU/EN and that zh behavior has not regressed.

## S1b — Mutation Truth Evidence

For each migrated direct caller, exercise at least its ordinary success/readback path. In focused domain fixtures also exercise: one commit plus one failure, a known commit with callback readback error, response loss after commit, response loss before commitment, and loss of access after commitment. Capture request reference, server effect/readback, rendered union state, and retry payload. A test passes only when known successes are not replayed and a local UI reset is never represented as server rollback.

## S2 — First Requested Cross-Surface User Result

The S2 implementation adds a separate live Playwright configuration and documented `test:browser:live` package script. It targets built Nuxt output and a real Go/PostgreSQL fixture; it must not start `scripts/mock-operator-api.mjs`.

1. Create real linked worktrees A and B with divergent saved bytes. In A, export a function through a bounded TS/TSX alias/re-export chain and change that target while its importing caller remains textually unchanged; B retains a different function version. Issue explicit A/B browser grants through `CodeGrantApplication` and retain the corresponding audit records.
2. Start ordinary installed agent session A through the real daemon/server path, browser tabs A/B, and a concurrent MCP client on a distinct client binding. The ordinary agent selects A, waits for complete selected-View embeddings, runs the deliberately non-lexical Russian intent query, follows the bounded declared relation, and exact-reads the cited stored span. Retain agent request → daemon/server → UCI → PostgreSQL/readback evidence.
3. Handshake each browser document for its server-issued binding, then select named Source/Checkout/View in tabs A/B. In each tab run semantic search → bounded graph/relation → exact source. Retain trigger → HTTP handler → UCI application/release → PostgreSQL/readback evidence. Every successful response identifies its same selected View; neither tab affects the other or MCP context.
4. Open or duplicate a Code tab from initialized tab A. Confirm copied `sessionStorage` produces `TAB_BINDING_COLLISION`, a new binding only for the new tab, no inherited selection, and no change to A. Independently hard-reload A and B and confirm their individual contexts resume through their own session/resume proofs.
5. Save the changed alias/re-export target in A and drive the normal watcher/reconcile path—do not manually publish a fixture View. Confirm a new A View, re-resolved unchanged caller relation, no stale A target, and unchanged B View/membership/edges/exact source. Browser A’s previously pinned View remains readable until explicit transition; browser B never changes.
6. Revoke a grant after application work and before release; separately force recorder/release failure, absent/ambiguous selector, expired/mismatched continuation, and all nine presentation states. Each response leaks no contextual body, IDs, counts, relationships, or receipt and the UI remains denied/error/partial/offline rather than empty/ready.

## S3 — Collection Evidence

1. Load cursor pages over more than 200 Rules and more than 100 Issues. Demonstrate explicit IDs, current-page selection, frozen all-filter membership, exclusions, expiry, permission change, and version change after preview.
2. Exercise Rules enable/disable/delete/edit and scope reorder. Inject a conflict during reorder and verify the full declared scope is unchanged.
3. After Rules acceptance, run the stated Issues, Memory, Queue, and Documents action matrices through their actual domain handlers. Capture per-item results, postconditions, and safe retry/status behavior.
4. Verify keyboard/indeterminate selection accessibility, responsive widths/zoom, RU/EN, and zh regression with each truth state distinct.

## S4 — Daemon Intent Evidence

1. In an online owner fixture submit an intent for one selected context. Observe durable submission, daemon ACK, execution, published resulting View, and authorized readback.
2. In an offline owner fixture observe `queued` or `unavailable`; confirm neither HTTP acceptance nor elapsed time renders completion.
3. Hard reload or lose the response after intent persistence. Reconcile by intent reference without duplicate execution and without disclosure of paths/secrets.

## Final Integration Evidence

- Retain candidate identity, server/browser/MCP fixture configuration without secrets, request/handler/UCI/readback trace, and the exact test outputs.
- Re-run only tests affected by the changed seam; the root release owner decides broader release/Sonar execution. This planning task performs no production implementation, deployment, publication, or release.
- Before Book Context planning, evidence must show accepted S2 plus Rules. Before Book implementation, evidence must show full Feature011 including S3 later-domain consumers and S4.
