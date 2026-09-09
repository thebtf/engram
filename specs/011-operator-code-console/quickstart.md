# Feature 011 Evidence Quickstart

**Purpose**: This is an implementation and acceptance evidence plan, not evidence that Feature011 is implemented, released, or installed. Run every command from the exact Feature011 candidate worktree, not the primary checkout. The current candidate is bound by plan setup to `specs/011-operator-code-console`.

## Preconditions

1. Confirm the candidate worktree, current HEAD, clean/owned planning paths, Feature011 selector, and Constitution 2.0.0 before a runtime slice begins. Preserve unrelated primary-checkout work.
2. Use an isolated disposable PostgreSQL database and a real authenticated Go server configured only with disposable credentials. Do not set disabled-auth, expose a master token to Playwright, attach production data, or use a browser path/credential to reach a workstation.
3. For S2, bind the exact R-A installed UCI candidate used by the fixture. It must make a real semantic result available for the selected View; lexical/degraded output is valid only for the corresponding truthful-state scenario, not the primary semantic-path acceptance.
4. Use two real linked Git worktrees with divergent saved content and a concurrent ordinary MCP client. Do not replace their topology with copied folders or mocked ContextRefs.
5. Seed explicit browser read grants for the test human subjects through the authorized grant application path and seed collection data exceeding 200 Rules and 100 Issues.

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

The S2 implementation adds a separate live Playwright configuration and documented `test:browser:live` package script. It targets the built Nuxt output and real Go/PostgreSQL fixture; it must not start `scripts/mock-operator-api.mjs`.

1. Start ordinary agent session A in real worktree A and browser tabs A/B with different granted contexts for worktrees A/B. Start one concurrent MCP client on a distinct binding.
2. In each browser tab, select named Source/Checkout/View, issue a semantic natural-language query, open a bounded graph/relation result, and read an exact source span. Retain trigger → HTTP request → registered handler → UCI application/release → PostgreSQL/readback evidence.
3. Assert every successful search/graph/source result in each tab identifies the selected same View; tab A cannot affect tab B or MCP context. Hard reload preserves its own tab context while session remains valid.
4. Revoke a grant after application work and before release in the controlled fixture. Confirm no contextual result/receipt is disclosed. Exercise absent/ambiguous/expired/mismatched continuation and all nine presentation states.
5. Publish a newer View. Confirm open search/source/graph stay pinned and the browser requires an explicit transition.

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
