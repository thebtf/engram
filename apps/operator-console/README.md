# engram Operator Console — Nuxt scaffold

Design-fidelity **Nuxt 3 + Nuxt UI** scaffold of the operator console. Renders the
`.od/index.html` contract as real Vue pages with **mock data behind explicit seams**.

> **Integration agents start at
> `../../design/operator-console/contracts/INTEGRATION-AGENT-PROMPT.md`** - the system
> prompt that sets the whole methodology (contract-follow, no codegen, continuous sync)
> and routes here.

## Ownership (decided 2026-06-20)

This scaffold is owned by the **design track**, per `DESIGN-SYNC-PROTOCOL.md §0`: the
design agent ships a self-contained prototype in shared tokens; the developer does not
rewrite it — they swap the data source behind the seams. Same boundary, real framework
instead of generic HTML.

| Design track owns | Developer owns |
|---|---|
| `pages/`, `layouts/`, `components/`, `assets/tokens.css` | `nuxt.config.ts` deps/build tuning, auth guard |
| `composables/useNav.ts`, `useHonesty.ts` (model + contract) | Go-embed build pipeline (`Makefile dashboard`) |
| `composables/useMockData.ts` (the SEAM, mock values) | replacing each mock fn with `useFetch('/api/...')` |
| design fidelity, classification, honesty signals | live wiring, sessions/auth, server routes |
| `PARITY.json` rows + `design_version` (the drift ledger) | runs `npm run parity` as a merge gate |

They meet at `useMockData.ts` — the single file the developer edits to go live. Pages
never change when data goes real.

The port catches up to the mockup through an **auditable ledger, not codegen** — see
**Staying in sync** below.

## Repository boundary

OpenDesign continues to author inside the local/private `.od/` workspace. GitHub and
production do not track or ship that workspace; `.gitignore` keeps `.od/**` out of the
repo.

Tracked repo state is the promoted subset described in
`../../design/operator-console/PROMOTION-CONTRACT.md`:

- `design/operator-console/contracts/` and `design/operator-console/mockups/` are the
  curated design-contract snapshot safe to review and publish.
- `apps/operator-console/` is the deployable Nuxt source used by CI/CD and the server
  image.
- Promotion is one-way from `.od/` to tracked paths. It is not a generator, and it does
  not make `.od/` public.

## Run

```bash
npm install
npm run dev      # http://localhost:3000
```

## Structure

```
nuxt-port/
├── nuxt.config.ts          developer seam: modules, build, ssr:false
├── app.vue                 <NuxtLayout><NuxtPage/>
├── layouts/default.vue     shell: topbar / status-bearing nav / status bar
├── assets/tokens.css       DESIGN.md tokens (dark base + light mirror)
├── app.config.ts.example   token bridge for Nuxt UI / Tailwind (rename to app.config.ts)
├── composables/
│   ├── useNav.ts           sections + grouping + honesty (single source)
│   ├── useHonesty.ts       live/dormant/stale/mustbuild contract
│   └── useMockData.ts      ★ THE SEAM — replace with useFetch
├── components/
│   ├── HonestyBadge.vue    classification chip + evidence
│   ├── EntityRow.vue       keystone list row
│   ├── SwitchRow.vue       runtime flag + restart badge
│   ├── RevealSecret.vue    write-only one-shot reveal
│   ├── KeyRotationModal.vue fingerprint change = decrypt→re-encrypt
│   ├── BulkBar.vue         sticky bulk-action bar (snapshot-then-apply)
│   └── SectionStub.vue     honest empty-state for must-build/dormant/stale
├── pages/                  16 routes, one per nav section
├── PARITY.json             drift ledger (per section: fidelity / sync / synced_to / gaps)
├── PARITY.md               how the ledger works (no codegen — developer follows the mockup)
├── PARITY.schema.json      schema for PARITY.json
└── scripts/parity-check.mjs  `npm run parity` — no-deps drift detector (CI-gateable)
```

## Going live (developer)

1. `npm i` and confirm the shell renders with mock data.
2. For each section, replace the matching `useMockData.ts` function with `useFetch`
   against the route in `RUNTIME-SUBSTRATE-MAP.md` (e.g. `useMemories` → `/api/memories`).
3. Add the auth guard (route middleware) and wire `useColorMode`/density to persistence.
4. Point the build at the Go-embed path. Keep honesty: a section whose endpoint does not
   exist stays `SectionStub cls="mustbuild"` with the endpoint as evidence — never an
   inert operable control.
5. Before merge, run `npm run parity` — it fails on drift against the mockup. See
   **Staying in sync** below.

## Staying in sync with the mockup

The `.od/index.html` mockup is the **contract**; this port follows it. There is **no
codegen** between them (both are hand-built, so a generator would just rot into two
sources of truth). The gap is tracked as a ledger instead:

- **`PARITY.json`** — one row per section: `fidelity` (full / interactive / structural /
  stub), `sync`, `synced_to` (the `design_version` the page last caught up to), and the
  honest `gaps`. Keep it truthful, not flattering.
- **`design_version`** — date stamp in `DESIGN.md` frontmatter and atop `PARITY.json`;
  bump it in `DESIGN.md` on any contract change.
- **`npm run parity`** — fails (exit 1) on stamp mismatch, a `drifted` row, `synced_to`
  lag, a missing page, or an orphan page. Gate it in CI.

Full process: **`DESIGN-SYNC-PROTOCOL.md` §11 (Канал C)** + **`PARITY.md`** (the source of
truth — this section is only the pointer).

## Invariants (do not drop — DEVELOPER-PLAYBOOK §0)

Honesty classification · secrets write-only / one-shot reveal · fingerprint masked +
rotation-not-edit · one accent / tabular nums / dark-first · restart≠applied ·
prod read-only · no page-entry animation.
