# Nuxt Port Kit — engram Operator Console

**Stack (given):** the new version is **Vue 3 + Nuxt**. The generic `.od/index.html`
is the **UI contract**, not a shippable artifact (`DEVELOPER-PLAYBOOK §0`). This kit
maps that contract onto Nuxt so the design system, honesty behavior, and seam
contract survive the move. Do **not** mine the current prod app — it is under active
rewrite and is not canonical.

## 0. What this kit gives you

This is a **deployed scaffold**, not a seed: full nav (16 pages, no 404s), the shell
layout, the seven `.od`-specific components, the honesty/nav composables, the mock-data
seam, and the parity ledger that keeps it honest against the mockup.

```
nuxt-port/
├── NUXT-PORT-GUIDE.md          ← this file: mapping + per-screen recipe
├── README.md                   ← ownership (design track vs developer) + going-live steps
├── PARITY.json                 ← drift ledger: one row per section, fidelity + sync status
├── PARITY.md                   ← how the ledger works (no codegen — the developer follows the mockup)
├── PARITY.schema.json          ← schema for PARITY.json
├── scripts/parity-check.mjs    ← no-deps drift detector (`npm run parity`, CI-gateable)
├── nuxt.config.ts · app.vue · package.json · app.config.ts.example
├── assets/tokens.css           ← DESIGN.md tokens as CSS vars (dark base + light mirror)
├── layouts/default.vue         ← shell: topbar (logo→home, search, density, theme) + nav rail + status bar
├── composables/
│   ├── useHonesty.ts           ← the live/dormant/stale/mustbuild contract + evidence guard
│   ├── useNav.ts               ← single nav source (sections + classification dots)
│   └── useMockData.ts          ← THE SEAM — swap mock consts for useFetch('/api/...') here
├── components/
│   ├── HonestyBadge.vue        ← classification chip + mandatory evidence
│   ├── EntityRow.vue           ← keystone list row (echk/estate/ebody/eside)
│   ├── SwitchRow.vue           ← runtime flag + restart-badge + secret discipline
│   ├── RevealSecret.vue        ← write-only one-shot secret reveal
│   ├── KeyRotationModal.vue    ← fingerprint change = decrypt→re-encrypt, never a text edit
│   ├── BulkBar.vue             ← sticky bulk-action bar, snapshot-then-apply
│   └── SectionStub.vue         ← honest placeholder for not-yet-built backends
└── pages/                      ← 16 pages = the full nav (index, memory, secrets, settings, health,
                                  issues, rules, projects, access, noise, search + stub-backed sections)
```

The seven `components/` are the `.od`-specific pieces that have **no equivalent** in a
component library. Everything else (cards, tables, dialogs, inputs, tabs, dropdowns)
you build from your chosen library — see §2. How far each page has caught up to the
mockup is tracked honestly in `PARITY.json` — see §7.

## 1. Foundation: wire the tokens first

1. Copy `assets/tokens.css` into the Nuxt app (`~/assets/tokens.css`) and register it:
   ```ts
   // nuxt.config.ts
   export default defineNuxtConfig({
     css: ['~/assets/tokens.css'],
     modules: ['@nuxtjs/color-mode'],
     colorMode: { classSuffix: '', dataValue: 'theme' }, // sets <html data-theme="dark|light">
   })
   ```
   Dark is the base (`DESIGN.md`: wall display / second monitor). Light is the mirror.
2. Bridge the tokens into your Tailwind / Nuxt UI theme so utility classes resolve to the
   same vars — see `app.config.ts.example`. The rule: **components read CSS vars, never
   hard-coded hex.** One accent, one source.
3. Density is a shell preference, not per-page state. Mirror the prod `useConsoleDensity`
   idea: a `data-density="comfortable|compact"` on the shell root, read by row paddings.

## 2. Mapping: .od class → library component

Pick ONE library and stay consistent. Recommended: **Nuxt UI v3** (Reka UI under the
hood, Tailwind v4, first-class Nuxt). The prod app already uses shadcn-vue (also Reka
UI), so either is a clean fit. Map like this:

| .od / DESIGN.md element | Library component | Keep from .od |
|---|---|---|
| `tbtn`, `tbtn.primary` | `UButton` (variant ghost / solid) | accent = `--accent`, primary only for final submit |
| `act`, `act.danger` | `UButton` (variant outline / color red) | hover → accent border |
| `txt`, `sel2` | `UInput`, `USelect` | focus ring = `--accent` |
| `toggle` | **SwitchRow.vue** (not `USwitch`) | restart badge + classification — library switch lacks both |
| `fchip` | `UButton`/`UBadge` pressed-state OR custom | `aria-pressed` → accent fill |
| `bdg`, `rbadge`, classification | **HonestyBadge.vue** | evidence contract |
| `issue-chip` | `UBadge` with tone map | currentColor + 88% tint |
| `card`, `surface-card`, `panel` | `UCard` | flat by default, no shadow at rest |
| `grid` + `erow` | `UTable` OR **EntityRow.vue** | accent inset stripe on sel/open |
| `modal`, `overlay` | `UModal` | fixed overlay, `--elev-raised` |
| `toast` | `useToast()` / `UNotification` | one success tone, undo in accent |
| `secret` / reveal | **RevealSecret.vue** | one-shot, write-only, never persist |
| `gauge`, `instr-band`, donut/segbar/hbars | custom SVG/CSS (see Overview screen) | honest snapshots only |
| `nav`, `topbar`, `statusbar` | `UDashboardSidebar` (Nuxt UI Pro) or custom layout | classification dots on nav items |

**Rule of thumb:** if the element only differs from a library component by *colour and
spacing*, use the library component + token bridge. If it carries a **product invariant**
(classification, restart-required, secret discipline, accent-inset selection), use the
`.od`-specific component from this kit. Don't rebuild the library's primitives.

## 3. Per-screen recipe

Order by lowest risk (`DEVELOPER-PLAYBOOK §7`). For each screen: read the `.od`
contract for the screen, build the shell from library components, drop in the
`.od`-specific components, then wire data to the real Nuxt server routes.

| Screen | Library shell | .od-specific | Data seam (Nuxt) |
|---|---|---|---|
| **Overview** (home) | `UCard` grid | HonestyBadge, attention list, donut/segbar/hbars (SVG) | `GET /api/stats` — snapshots only; trend lines stay must-build until a daily-history endpoint exists |
| **Memory** | `UTable` or EntityRow list, `UInput` search, filter chips | EntityRow, HonestyBadge | `GET /api/memories` |
| **Rules** | EntityRow list, `UModal` editor | EntityRow, SwitchRow (always-inject) | `GET/POST/PATCH/DELETE /api/rules` |
| **Issues** | `UTable`, `UModal` create, detail page | issue-chip (`UBadge`), HonestyBadge | `GET /api/issues`, detail/thread routes |
| **Secrets** | `UTable` | RevealSecret, key-rotation `UModal` | `GET /api/vault/status`, `/credentials`, `POST /api/vault/rotate` |
| **Settings** | `UTabs` + row layout | SwitchRow (runtime flags), credential editor | `GET /api/config` (read), flag writes |
| **Health/State** | `UCard` grid | gauge, subsystem dots, HonestyBadge | `GET /api/selfcheck`, `/api/stats/vnext` |
| **Access** (admin) | `UTable`, `UModal` | HonestyBadge, role ladder | admin routes (gate behind admin) |

**Seam discipline:** the prototype hands you the seam (where mock data plugs in). In
Nuxt that seam is a `composables/useXxx.ts` + `useFetch('/api/...')`. Replace the mock
const with the fetch; keep the component tree, the classification, and the honesty
signals. Never wire a control whose backend endpoint does not exist — show it as
`mustbuild` with the endpoint as evidence instead.

## 4. The invariants you may NOT drop (DEVELOPER-PLAYBOOK §0)

1. **Honesty classification** on every surface — `useHonesty` + HonestyBadge.
2. **Secrets are write-only, reveal is one-shot** — RevealSecret never persists the value.
3. **Vault fingerprint masked by default**; changing it = key **rotation** (decrypt→re-encrypt), never a text edit.
4. **One accent, tabular numerals, dark-first** — token bridge enforces it.
5. **Restart-required ≠ applied** — SwitchRow `reload` badge.
6. **Prod is read-only by default** — mutations behind explicit operator action.
7. **No page-entry animation** — `DESIGN.md` Don't. Load into the task, not choreography.

## 5. What stays generic / what becomes Nuxt

- `.od/index.html`, `access-admin.html`, `saas-admin.html` remain the **contract** —
  the source of truth for every screen, state, and badge. Keep them.
- `DESIGN.md`, `DESIGN-SYNC-PROTOCOL.md`, `DEVELOPER-PLAYBOOK.md`, `ACCESS-ADMIN-spec.md`
  remain canonical product docs.
- This `nuxt-port/` folder is the **deployed scaffold**: tokens + the 7 irreducible
  components + composables + the full pages tree + the parity ledger. When the Nuxt app
  grows, components mature inside the app; the mockup stays the contract they answer to.

## 6. Going live (replacing mock data)

The scaffold renders against `composables/useMockData.ts` — the single seam. To wire a
real screen: open the page, find its `useXxx()` call, and swap the mock const for
`useFetch('/api/...')` in `useMockData.ts`. Keep the component tree, the classification,
and the honesty signals. Never wire a control whose backend endpoint does not exist —
leave it `mustbuild` (via `SectionStub`/`HonestyBadge`) with the endpoint as evidence.
See `README.md` for the per-track ownership table and the ordered going-live steps.

## 7. Staying in sync with the mockup (Channel C)

**The mockup leads; the developer follows. There is no codegen** — `.od/index.html` is
hand-built and so is the Nuxt port, so a generator between them would just create two
sources of truth that rot. Instead the gap is an **auditable ledger**, not a guess.

The single source of truth for this process is **`DESIGN-SYNC-PROTOCOL.md` §11 (Канал C)**
and **`PARITY.md`**. The short version:

- **`design_version`** — a date stamp in `DESIGN.md` frontmatter and at the top of
  `PARITY.json`. Bump it in `DESIGN.md` on any contract change.
- **`PARITY.json`** — one row per section: `fidelity` (full / interactive / structural /
  stub), `sync` (synced / drifted / ahead), `synced_to` (the `design_version` the port
  last caught up to), `components`, and the honest `gaps` list. This file is the record
  of how far each page trails the mockup — keep it truthful, not flattering.
- **`npm run parity`** (`scripts/parity-check.mjs`, no deps) — fails (exit 1) when the
  stamps disagree, a row is `drifted`, `synced_to` lags `design_version`, a page is
  missing, or a page is orphaned. CI-gateable.

**Routine when the mockup changes:** bump `design_version` in `DESIGN.md` → run
`npm run parity` to see which rows now lag → for each, do the catch-up pass on the page,
then set its `synced_to` to the new version and `sync` back to `synced`. A row you can't
fully match yet stays `drifted` (or drops a `fidelity` level) with the reason in `gaps` —
that honesty is the point of the ledger.
