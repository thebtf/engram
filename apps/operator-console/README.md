# engram Operator Console — Nuxt runtime

This directory is deployable Nuxt source, not a frozen design-fidelity scaffold. The
authoritative operator outcome is [root `PRODUCT.md`](../../PRODUCT.md); Feature 011 D-A
defines the current Workspace journey: **Home → Workspace → readable Repository/Working
copy/Indexed snapshot → source → direct or reverse relation → released evidence**.

## Ownership

| Boundary | Owner |
|---|---|
| Product outcome, acceptance, scope | Root `PRODUCT.md` and `specs/011-operator-code-console/` |
| Private visual authoring | `.od/` design-source owner |
| Curated tracked snapshot | `design/operator-console/` through [PROMOTION-CONTRACT.md](../../design/operator-console/PROMOTION-CONTRACT.md) |
| Runtime pages, navigation, composables, components, locales, tests | Console implementation owner |
| Shared server routes, UCI composition, migrations | Single integration owner named by Feature 011 tasks |

The runtime owner may change pages, navigation, labels, and component structure when the
accepted product contract requires it. Reuse tokens, accessibility, secret handling, and
honest-state patterns; do not preserve a page, mock seam, route, label, or disabled control
only because it appears in an earlier design snapshot.

## Design and promotion boundary

OpenDesign authoring is private `.od/` and is not tracked or reconstructed here. Promotion
is one-way into `design/operator-console/`; it is not code generation and it does not
overwrite this runtime. `npm run parity` validates the curated snapshot ledger only. It does
not prove product acceptance, navigation discoverability, UCI authority, a real provider,
or installed behavior.

The reviewed 2026.09.17 snapshot is already promoted. `PROMOTION-MANIFEST.json` and `PARITY.json` agree on its design version and snapshot hash; that agreement records the checked design input, not runtime or visual parity. Every active parity row remains `drifted`, and the Code route's Chrome acceptance is pending.

The [responsive-layout receipt](../../specs/011-operator-code-console/acceptance/da-workspace-responsive-layout-receipt.json) proves Workspace shell layout and drawer behavior only. It does not reassert semantic-provider explorer evidence (`embedded_chunks=0 of 64`) or substitute for the installed exact-head normal-homepage DA01–DA04 release proof, which remains pending.

## Runtime invariants

- Source/Checkout/View and explicit grants remain server authority. Human repository,
  branch, device, and snapshot labels are presentation only.
- A registered checkout without a display name appears as a localized unnamed working copy,
  including when its View is published; the label is never a path or an access grant.
- A browser never reads local worktree paths or credentials and never asks for a manually supplied
  context identifier. It stores only the non-authorizing `view_ref` for a candidate; each pin
  uses the catalog's fresh `selection_ref` and requires a server success response. The owner grant
  list comes from paged `GET /api/code/grants` after reload; opaque `grant_ref` is used only for
  the displayed grant's revoke action, never as operator input or durable browser state.
- Failed catalog refresh removes candidate pin authority until a fresh successful catalog
  rebind; an already server-confirmed pin may remain visible in the same binding. After
  a successful grant issue or revoke, failed inventory refresh preserves the mutation
  confirmation but hides stale revoke actions until the inventory is verified again.
- The default D-A path is keyboard-operable, has visible focus and state announcements,
  supports RU/EN task language, and leaves zh navigation intact.
- A relation is evidence-led. It does not become a manual graph editor, and historical
  graph/book records are not relabeled as code facts.
- Manual graph writers and plaintext book intake are retired at UI and executable
  boundaries; their historical documents/provenance/readers remain governed by Feature 011.

## Commands

```bash
npm run build
npm run test:seam
npm run test:parity
npm run test:browser
npm run test:browser:live
```

Run only the focused commands selected by the active Feature 011 task from the exact bound
candidate. Mock browser and parity checks are scoped evidence, not complete D-A acceptance.
