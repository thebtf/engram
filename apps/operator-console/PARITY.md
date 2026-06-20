# Parity — keeping the port in step with the mockup

**The decision (2026-06-20):** there is **no codegen** from mockup to Vue. The mockup
(`.od/index.html`, `access-admin.html`, `saas-admin.html`) is the **contract**; the
developer follows it using the class map (`DEVELOPER-PLAYBOOK §8a`, `NUXT-PORT-GUIDE`)
and the ready components. What we maintain instead is **a drift ledger**, not an
automated translator — because mockup (vanilla JS) and port (Vue SFC + Nuxt UI) are
different enough that 1:1 generation is fragile and creates two byte-identical sources
of truth that rot. A ledger is cheap, honest, and humans/agents both read it.

## How sync actually works (answer to "do we need to sync?")

1. **The mockup leads.** Any visual/behavioural decision lands in the `.od` files first.
2. **`DESIGN.md → design_version` is the stamp.** Bump it on any token / component /
   screen change.
3. **`PARITY.json` is the ledger.** One row per section: which mockup section, which port
   page, fidelity level, sync status, the `design_version` it was last reconciled to, and
   the named gaps to full fidelity.
4. **`npm run parity` is the detector.** It fails when the stamps diverge, when a section
   is flagged `drifted`, when a `synced_to` lags the current stamp, or when a page is
   missing / unrowed. Wire it into CI to make drift loud instead of silent.
5. **Channel C in `DESIGN-SYNC-PROTOCOL.md`** is the human process around it (below).

So: the developer **follows the mockup** — your second option is correct. The ledger is
what makes "follows" auditable instead of trust-based.

## Fidelity levels

| level | meaning |
|---|---|
| `full` | layout + states + interactions + honesty signals all reproduced |
| `interactive` | core list/detail + primary actions; secondary modals/edge-states pending |
| `structural` | layout + classification + mock data; rich interactions stubbed |
| `stub` | honest `SectionStub` — backend not built/enabled; no operable controls |

## Sync status

| status | meaning | action |
|---|---|---|
| `synced` | matches mockup at `synced_to` | none |
| `drifted` | mockup changed after `synced_to` | catch-up pass, then re-stamp |
| `ahead` | port intentionally exceeds mockup (rare) | note why in `gaps` |

## Current state

Run `npm run parity` for the live count. As of `design_version 2026.06.20`:
**2 full · 4 interactive · 5 structural · 5 stub**, 35 named gaps. i18n readiness:
**1 keyed · 15 hardcoded** (secrets + the shell are wired; the rest await keying). The
ledger IS the backlog — every gap is a concrete next task for the developer, ordered by
section priority (`DEVELOPER-PLAYBOOK §7`).

## When a mockup change must propagate — and when it must NOT

| mockup change | propagate to port? |
|---|---|
| token value (colour/spacing/radius) | **auto** — port reads `assets/tokens.css`; just bump `design_version` |
| component behaviour (e.g. reveal timing) | yes — update the matching `.vue` in `components/`, re-stamp its sections |
| new screen / new section | yes — add a page + a `PARITY.json` row (`stub` is a valid first state) |
| copy / label wording | yes — but the string lives in `i18n/locales/ru.json`, not inline in the `.vue`. Edit the dictionary key; the port renders it. See **Localization** below. |
| a gap being closed in the mockup that was already `mustbuild` | only when the backend endpoint exists — never wire an inert control (honesty invariant) |

The point of the ledger: a token tweak should **not** trigger a 16-screen review, but a
component-behaviour change **should** flag exactly the sections that use it. The
`components` array in each row is how you find them.

## Routine

- Designer changes a section → bump `design_version` → set affected rows `sync: "drifted"`.
- `npm run parity` goes red → developer does the catch-up → sets rows back to `synced`,
  `synced_to: <new version>`, trims closed `gaps`.
- New section ordered via Channel A → add the page + a `stub` row immediately, raise
  fidelity over time.

## Localization (i18n)

Translation is a launch requirement, so strings are **not** hardcoded in templates —
they live in `i18n/locales/<locale>.json` and components read them via `t()`. The dict is
the **localization seam**, exactly as `useMockData.ts` is the data seam.

- **Launch locales: `ru` · `en` · `zh`** (Russian contract · English · Simplified Chinese).
  Each is a dict in `i18n/locales/`; all carry the same key set. Add a locale = add a JSON
  file + register it in `nuxt.config.ts` `i18n.locales` (+ a plural rule in
  `i18n.config.ts` if the language's number grammar differs).
- **`ru.json` is the contract.** It mirrors the `.od` mockup strings verbatim. When the
  mockup changes a label, change the matching key here (not the `.vue`), then re-stamp the
  affected `PARITY.json` rows. `ru` is also the `fallbackLocale`, so a missing `en`/`zh` key
  never shows a raw key id — it shows the Russian contract string.
- **`en.json` + `zh.json` are the worked targets.** They prove "translate a screen" and are
  extended one screen at a time as pages get keyed. A half-translated `zh.json` still
  renders (missing keys fall back to `ru`).
- **A page with hardcoded strings is not translation-ready.** The `i18n` field on each
  `PARITY.json` row tracks this (it's the dedicated dimension — don't also duplicate it in
  `gaps[]`, which stays about fidelity):
  - `keyed` — every visible string resolves through `t()`; all 3 locales have the keys.
  - `partial` — some strings keyed, some still inline.
  - `hardcoded` — strings are literal in the template (the pre-i18n default).
- **Exemplars wired so far:** the shell (`layouts/default.vue` + `useNav.ts`) and
  `pages/secrets.vue` are `keyed`. The remaining 15 pages are `hardcoded` — keying them is
  ordinary developer follow-the-mockup work, tracked per row.
- **Plurals are per-locale and registered in `i18n/i18n.config.ts`** (`pluralRules`):
  - **`ru`** — Slavic 3-form (one/few/many) with the 11–14 / 111–114 "teen" exception,
    `% 100`-correct (the canonical vue-i18n docs example is wrong past 100). Message:
    `"{n} секрет | {n} секрета | {n} секретов"`. So "5 секретов", never "5 секрет(ов)".
  - **`en`** — built-in 2-form (one/other); no custom rule needed.
  - **`zh`** — one grammatical form, so a 1-slot message (`"{n} 个密钥"`) is the norm; a
    custom rule adds an optional zero form (`"没有密钥 | {n} 个密钥"`) because the default
    2-slot rule would render "0 个密钥" instead.
  Usage everywhere: a pipe-delimited message + `t('key', n)`. `secrets.nSecrets` /
  `secrets.countPlural` are the worked examples in all 3 dicts (see `KeyRotationModal.vue`).
  **Never hand-concatenate a number and a noun** — always route a count through a plural key.

The point mirrors the no-codegen decision: we don't machine-translate the UI, we give the
developer one honest seam (the dict) and one honest ledger (the `i18n` field) so "which
screens are translation-ready" is auditable, not guessed.
