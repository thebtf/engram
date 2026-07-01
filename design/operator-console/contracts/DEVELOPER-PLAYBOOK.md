# DEVELOPER PLAYBOOK — wiring the engram operator console onto a real backend

> **Start here:** if you are an AI integration agent (or onboarding one), load
> **`INTEGRATION-AGENT-PROMPT.md`** first — it is the top-level system prompt that resets the
> integration methodology (mockup = living contract, no codegen, continuous Channel-C sync)
> and routes into this playbook. This file is the detailed wiring manual it points to.

**Audience:** the engineer who takes `index.html` (the designed prototype) and makes it
talk to the live engram server at `http://unleashed.lan:37777` without losing the design
decisions baked into it.

**Runtime substrate source:** if you need to know which **existing** API adapters,
composables, auth/session wrappers, or server seams are already present in the
current runtime, use `RUNTIME-SUBSTRATE-MAP.md`.

This playbook is **not** the inventory of the current Vue runtime. It is the
wiring and migration contract for moving the `.od/index.html` design onto the
live server truthfully.

**Framework note (CONFIRMED 2026-06-20):** the new version targets **Vue 3 + Nuxt**.
The current prod app (Vite SPA + shadcn-vue) is under active rewrite and is **not**
canonical — do not mine it. The generic `.od/*.html` files remain the UI contract.
The deployed scaffold lives in **`nuxt-port/`**: full nav (16 pages), the shell
`layouts/default.vue`, `assets/tokens.css` (DESIGN.md tokens), the seven irreducible
`.od`-specific components that have no library equivalent — `HonestyBadge`,
`EntityRow`, `SwitchRow`, `RevealSecret`, `KeyRotationModal`, `BulkBar`, `SectionStub`
— plus composables `useHonesty.ts` / `useNav.ts` and the data seam `useMockData.ts`.
Everything else is built from Nuxt UI / the chosen library. Start at
`nuxt-port/NUXT-PORT-GUIDE.md` (mapping + per-screen recipe) and `nuxt-port/README.md`
(per-track ownership + going-live steps). What must remain stable across the move is the
seam contract, design system, and honesty behavior — not the host framework.

**Staying in sync (the developer follows the mockup):** the `.od` mockup is the contract;
the Nuxt port catches up to it through an **auditable ledger, not codegen**. The single
source of truth for this process is **`DESIGN-SYNC-PROTOCOL.md` §11 (Канал C)** and
**`nuxt-port/PARITY.md`**: a `design_version` stamp in `DESIGN.md`, the per-section
`nuxt-port/PARITY.json` drift ledger (fidelity / sync / `synced_to` / honest `gaps`), and
`npm run parity` (a no-deps detector that fails CI on drift). When the mockup changes,
bump `design_version`, run `npm run parity`, and catch up the rows it flags. Do not
duplicate that process here — follow those two docs.

**Localization (i18n) is wired — strings are not hardcoded.** Translation is a launch
requirement. Launch locales are **`ru` · `en` · `zh`** (Russian contract · English ·
Simplified Chinese); visible strings live in `nuxt-port/i18n/locales/<locale>.json` and
components read them via `t()`. `ru.json` is the contract (verbatim mirror of the mockup)
and the `fallbackLocale`; `en.json` + `zh.json` are the targets. **To translate a screen**,
key every visible string (`t('area.element')`), add the keys to all three dicts, then set
that section's `i18n` field in `PARITY.json` to `keyed`. `pages/secrets.vue` + the shell
(`layouts/default.vue`, `useNav.ts`) are the worked exemplars; the other 15 pages are
`hardcoded` and are ordinary follow-the-mockup work. Never write a literal Russian string
into a template on a page you touch — add a key instead. **Counts go through plural keys**
(`t('key', n)`), never hand-concatenated — per-locale plural rules live in
`i18n/i18n.config.ts` (`ru` Slavic 3-form, `zh` one-form + zero). Process detail:
`DESIGN-SYNC-PROTOCOL.md` §11 → «Локализация» + `nuxt-port/PARITY.md` → «Localization».

**Core principle:** the prototype is a *complete UI contract*. Every screen, state, badge,
and honesty signal is deliberate. Your job is to **replace the mock data sources, not the
product behavior**. If you port the UI into `Nuxt`/Vue components or another structured app
shell, stop first — locate the seam the prototype already gives you, and preserve that seam
while changing the host implementation.

This doc is the map of those seams.

---

## 0. What you are NOT allowed to lose

These are load-bearing design decisions. If a refactor drops one, the refactor is wrong.

| Invariant | Where it lives | Why it must survive |
|---|---|---|
| **Honesty classification** — every surface tagged `live / dormant(flag-gated) / pre-demolition-stale / must-build` | `.bdg`, `--class-*` tokens, `mbp` "нужен backend" pills | The console's reason to exist: operator sees at a glance what is safe to touch. Never render a dead control as operable. |
| **Secrets are write-only, reveal is one-shot** | credential/key inputs are `type=password`, never round-tripped to client state | A leaked key is unrecoverable. Do not bind a secret value into a reactive store. |
| **Vault fingerprint is masked by default** | `wireVaultFingerprint()` renders `••••`, reveals only on explicit click, auto-re-masks after 30s; never plain on first paint | The fingerprint is identifying material for the master key. Showing it plain by default leaks it to anyone glancing at the screen and defeats the point of a fingerprint. Reveal is an operator action, not a default. |
| **Changing the fingerprint = key rotation, never a text edit** | `openKeyRotationModal()` — the fingerprint is a *derivative* of the master key; the UI never writes the fingerprint field directly | Rotation MUST: (1) decrypt every secret with the **current** key, (2) re-encrypt each under the **new** key, (3) derive the new fingerprint from the new key (not typed), (4) destroy the old key + write to the access log. Requires current-key (for decrypt) + new-key ×2 (for encrypt) + explicit ack. If the current key is wrong, decryption is impossible and secrets are lost — so the current key is mandatory input, not optional. Wiring the fingerprint field as an editable string would silently desync it from the key that actually encrypts the data. |
| **Prod is read-only by default** | mutations gated behind explicit operator action | Accidental writes to shared memory poison every workstation. |
| **Single accent, tabular numerals, dark-first** | `:root` token block, `font-variant-numeric` rules | The control-room identity. Don't introduce a second accent or proportional digits in data. |
| **Restart-required ≠ applied** | reload badges on switches | ~60% of flags need a server restart; the badge is the operator's only warning. |
| **Login gate is a client, not an authority** | `doLogin()` stub never validates | The server validates. The client only reflects the result. |

---

## 1. Architecture as shipped

```
index.html  (single self-contained file, ~3900 lines)
├── <style>            design tokens + component CSS   ← KEEP, this is the design system
├── data consts        MEMORIES, CANDIDATES, BOOKS,    ← REPLACE with fetch()
│                       AI_MODELS, FLAGS, AUTH, ...
├── render functions    renderArea(), AREAS{}, ...      ← KEEP shape, feed real data
├── AUTH module        login gate, identity, profile   ← wire to real OAuth (§3)
└── boot               renderNav(); renderArea(); ...   ← add data hydration here
```

The render layer is **data-driven**: `renderArea()` reads the `current` area id, looks up a
function in the `AREAS` map, and that function reads a top-level `const` (e.g. `MEMORIES`).
**Every one of those consts is a fixture you replace with a fetch.** The render functions do
not care where the array came from.

### The one rule that makes this safe
> Keep the **shape** of each data const identical to the fixture. Change the **source**.

If `MEMORIES` is `[{id, title, preview, project, scope, cited, ...}]` today, your
`GET /api/memories` adapter must return the same field names. Map at the adapter boundary,
never by editing 40 render call-sites.

---

## 2. Step-by-step: swap mock → live for one area

Worked example: the Memory Lab list.

1. **Find the fixture.** Search `const MEMORIES =`. Note every field the render code reads
   (grep `m\.` inside `renderMemories`/`memoryRow`).
2. **Write an adapter** that returns that exact shape:
   ```js
   async function fetchMemories(){
     const r = await api('/api/memories?limit=200');   // api() = §4 helper
     return r.items.map(x => ({
       id: x.id, title: x.title, preview: x.summary ?? '',
       project: x.project_id ?? 'global', scope: x.scope,
       cited: x.citation_count > 0, /* ...keep field names ... */
     }));
   }
   ```
3. **Hydrate at boot**, then render:
   ```js
   MEMORIES = await fetchMemories();   // make the const a let, or use a store object
   renderArea();
   ```
4. **Loading + error states already have a home:** render an empty-state row while the
   array is `null`. Don't invent a spinner overlay — the entity grid already reflows.
5. **Leave the component CSS untouched.** You changed the array, nothing else.

Repeat per area: `CANDIDATES → /api/candidates`, `BOOKS → /api/books`,
`AI_MODELS → /api/models`, `FLAGS → /api/flags`, Issues → `/api/issues`. The
`DESIGNER-endpoints-brief.md` and `HANDOFF-data-integration.md` files in this folder map
each area to its real endpoint.

---

## 3. Auth wiring (the part you just received)

The console ships a **complete login UX** with a deliberately thin stub behind it. Replace
the stub; keep the UX.

### 3.1 The seam
Everything funnels through two functions:
```js
function doLogin(via){ /* sets AUTH.user, hides gate, renders chip */ }
function doLogout(){ /* clears AUTH, shows gate */ }
```
`via` is one of: `'password' | 'google' | 'github' | 'yandex' | 'vk' | 'sso' | 'legacy'`.

**Today** `doLogin` trusts the client and picks a `MOCK_IDENTITY[via]`.
**Your job:** make `doLogin` await a real result and only then flip state.

### 3.2 OAuth / OIDC providers (Google, GitHub, Yandex, VK, Authentik SSO)
These are **redirect** flows — the button must leave the page, not call a JS stub.

```js
const OAUTH_START = {
  google: '/auth/google/start',
  github: '/auth/github/start',
  yandex: '/auth/yandex/start',
  vk:     '/auth/vk/start',
  sso:    '/auth/oidc/start',     // Authentik OIDC
};
// in renderAuthPane(), replace doLogin(b.dataset.prov) with:
pane.querySelectorAll('[data-prov]').forEach(b => b.onclick = () => {
  window.location.href = OAUTH_START[b.dataset.prov] + '?return=/';
});
```
Server side: each `/start` redirects to the provider; the provider calls back to
`/auth/<p>/callback`; your server sets an **httpOnly session cookie** and 302s back to `/`.
On load, the console asks "who am I?" (§3.5) and the gate stays hidden if a session exists.

> **Authentik / generic OIDC:** the `sso` button is already the full-width "SSO · Authentik"
> entry. Point `/auth/oidc/start` at your Authentik application's authorization endpoint.
> Add more enterprise IdPs by appending to `AUTH_PROVIDERS` — the gate renders them
> automatically; add a matching `PROVIDER_MARK[id]` SVG.

### 3.3 Password login
```js
$('#agPwForm').onsubmit = async (e) => {
  e.preventDefault();
  const email = $('#agEmail').value.trim(), pw = $('#agPw').value;
  try {
    const u = await api('/auth/password', {method:'POST', body:{email, password:pw}});
    applyIdentity('password', u);   // see §3.6
  } catch(err) { authErr('Неверный email или пароль.'); }
};
```

### 3.4 Legacy admin-key login
The legacy tab posts the key; the **server** compares it to its env var. Never compare on
the client.
```js
$('#agKeyForm').onsubmit = async (e) => {
  e.preventDefault();
  const key = $('#agKey').value.trim();
  try {
    const u = await api('/auth/admin-key', {method:'POST', body:{key}});
    applyIdentity('legacy', u);
  } catch(err) { authErr('Ключ отклонён сервером.'); }
};
```
**The legacy switch (`AUTH.legacyEnabled`) is a UI affordance, not the security boundary.**
Even when the tab is hidden, the server MUST also refuse `/auth/admin-key` when the
operator has disabled legacy login server-side. Mirror `AUTH.legacyEnabled` to a real
server setting (e.g. `ENGRAM_ADMIN_KEY_LOGIN_ENABLED`) via `PATCH /api/settings`. UI-only
disabling is theatre; a determined client could still POST.

### 3.5 Session restore on load
Replace the boot line `if(!AUTH.signedIn) showAuthGate();` with:
```js
try {
  const me = await api('/auth/me');     // 200 + profile, or 401
  applyIdentity(me.via, me);
} catch { showAuthGate(); }
```
This makes the httpOnly cookie the source of truth, not localStorage. Keep localStorage only
for **non-secret** prefs (`AUTH.prefs`, theme) — never the session token.

### 3.6 `applyIdentity` (the success path you add)
```js
function applyIdentity(via, profile){
  AUTH.signedIn = true;
  AUTH.user = { name: profile.name, email: profile.email,
                initials: initialsFrom(profile.name), via };
  // do NOT persist a token; cookie holds the session
  saveAuth();                 // persists prefs + non-secret identity only
  hideAuthGate(); renderIdentityChip();
}
```

### 3.7 Logout
```js
async function doLogout(){
  closePopover();
  try { await api('/auth/logout', {method:'POST'}); } catch{}
  AUTH.signedIn=false; AUTH.user=null; saveAuth();
  renderIdentityChip(); showAuthGate();
}
```

### 3.8 Profile & sessions
- Profile fields → `PATCH /api/profile` (name, prefs, notifications).
- Active sessions list (`AUTH_SESSIONS`) → `GET /api/sessions`; "Завершить" →
  `DELETE /api/sessions/:id`; "Завершить все другие" → `POST /api/sessions/revoke-others`.
- The current session is tagged and not revocable — keep that guard.

---

## 4. The `api()` helper you need to add

One place to centralize base URL, cookies, errors, and the read-only posture.

```js
const API_BASE = 'http://unleashed.lan:37777';
async function api(path, {method='GET', body=null} = {}){
  const res = await fetch(API_BASE + path, {
    method,
    credentials: 'include',                 // send the session cookie
    headers: body ? {'Content-Type':'application/json'} : {},
    body: body ? JSON.stringify(body) : null,
  });
  if(res.status === 401){ showAuthGate(); throw new Error('unauthorized'); }
  if(!res.ok) throw new Error(`${method} ${path} → ${res.status}`);
  return res.status === 204 ? null : res.json();
}
```
- **CORS:** the server must allow the console origin and `Access-Control-Allow-Credentials`.
- **Read-only posture:** keep mutating verbs (POST/PATCH/DELETE) behind explicit operator
  actions that already exist in the UI (the staged-save footer, confirm modals). Don't add
  silent auto-save.

---

## 5. Reusable components — the inventory

`components.html` (shipped alongside this file) is the living gallery: open it in a browser
to see every component rendered with its copy-paste markup. The families:

| Family | Class / fn | Notes for reuse |
|---|---|---|
| Buttons | `.tbtn`, `.tbtn.primary`, `.act` | `.act` carries a `.mbp` "must-build" pill when an action needs backend. |
| Classification badges | `.bdg.live / .gate / .mb / .status` | The honesty vocabulary. Pick by real state, never decoratively. |
| Toggle | `.toggle[aria-checked]`, `.toggle.danger` | Pure CSS; flip `aria-checked`. `.danger` for destructive-when-on. |
| Entity Row | `.erow` + `.ebody/.emeta/.eside` | The list keystone across every area. |
| Identity chip + menu | `.identity`, `.idmenu` | "Who is logged in"; popover via `openPopoverAt()`. |
| Login gate | `.authgate`, `.ag-*` | Two-column: form + connection passport. Providers data-driven. |
| Provider marks | `PROVIDER_MARK{}` | Brand SVGs for Google/GitHub/Yandex/VK + neutral SSO shield. |
| Settings row | `settingRow(title, desc, control)` | Label/desc left, control right; the whole Settings surface is built from it. |
| Profile modal | `openProfileModal()` | Generic operator settings + active sessions. |
| Modal / toast / popover | `openModal()`, `toast()`, `openPopoverAt()` | Reuse these primitives; don't hand-roll new overlays. |
| Detail panel | `.detail`, resizable via `--detail-w` | Width persists to `localStorage`. |

### Helper functions worth knowing
- `el(tag, className, html)` — terse element factory.
- `escapeHtml(v)` — **always** wrap user/server strings with this before templating.
- `$(sel, root)` — querySelector shorthand.
- `settingRow / settingsSection` — compose any settings-like surface.
- `confirmModal(title, body, onOk)` — destructive-action confirmation.

---

## 6. localStorage keys (client state contract)

| Key | Holds | Keep / drop on backend |
|---|---|---|
| `engram.console.auth` | `{signedIn, legacyEnabled, user, prefs}` | Keep `prefs` + `legacyEnabled` (UI mirror). **Drop** `signedIn/user` once `/auth/me` is the source of truth — never store a token. |
| `engram.console.pageSizes` | per-list page size | Keep — pure UI preference. |
| `engram.console.detailW` | detail panel width | Keep — pure UI preference. |

---

## 7. Migration order (lowest risk first)

1. Add `api()` + `/auth/me` session restore. Gate now reflects real sessions. *(No data
   surfaces touched.)*
2. Wire the 5 OAuth buttons + password + legacy. Login works end-to-end.
3. Mirror `legacyEnabled` to the server setting; enforce on `/auth/admin-key`.
4. Swap read-only data consts one area at a time (§2). Memory Lab first — highest traffic.
5. Wire mutations behind the existing staged-save / confirm UI. Candidates promote, rule
   edits, issue transitions.
6. Profile `PATCH` + sessions endpoints.
7. Delete the now-unused `MOCK_IDENTITY` / fixture arrays. Run the §8 checklist.

---

## 8. Pre-merge checklist (don't lose the design)

- [ ] No secret value is ever read back into JS state or localStorage.
- [ ] Every `.bdg` reflects a **real** server state; no badge is hardcoded to a value that
      can drift.
- [ ] `must-build` pills removed only when the backing endpoint actually exists.
- [ ] Restart-required flags still show their reload badge after wiring.
- [ ] Single accent preserved; data digits still `tabular-nums`.
- [ ] Dark theme is still the default; light theme still passes contrast.
- [ ] Login gate appears on 401 from any endpoint, not just at boot.
- [ ] Legacy login refused server-side when disabled, not just hidden client-side.
- [ ] Logout clears the cookie server-side, not just client state.
- [ ] `escapeHtml()` wraps every server string rendered into innerHTML.
- [ ] `npm run parity` passes (no drift vs the mockup): every touched `PARITY.json` row has
      `synced_to` == the current `design_version` and no row is left `drifted`. A page you
      could not fully match stays honestly at a lower `fidelity` with the reason in `gaps` —
      see `DESIGN-SYNC-PROTOCOL.md` §11 / `nuxt-port/PARITY.md`.

---

## 8a. Admin-surface drill-down & action wiring map

`saas-admin.html` and `access-admin.html` use the same no-rail-tab detail pattern as
the operator console: a `<div class="body hide" data-pane="Xdetail">` with **no**
`data-tab` and **no** `id="h-X"` head; `show('Xdetail')` hides every `.head` via the
`h-`+tab fallback, and the renderer injects its own header into the body. Each detail
renderer reads a JS data object → builds innerHTML → calls `show()`.

**saas-admin.html — list → detail / action seams (all live):**

| Surface (pane) | Row affordance | Wired to | Data object |
|---|---|---|---|
| overview / workspaces | "Открыть", name | `openWorkspace(key)` (`.wlogo` 2-letter key) | `WS` |
| workspaces | "Войти как" | `impersonateModal(WS[key])` (logged) | `WS` |
| plans | card, "Изменить" | `openPlan(key)` | `PLANS` |
| invoices | "Открыть", No. | `openInvoice(id)` (`data-inv`) | `INVOICES` |
| users (platform) | "Профиль", name | `openPlatUser(key)` (`data-puser`) | `PUSERS` |
| subs | name → ws, "Счета" → invoices, "Сменить план" → transfers | `show()` / `openWorkspace` | — |
| payments | "Ключи" | inline **write-only** secret panel, reveal one-shot + Audit note | — |
| models | "Изменить" | inline endpoint/key editor, API key write-only | — |
| audit | "В редактор"/"Все счета"/"Управлять" | `show()` jump to referenced surface | — |

`pudetail` membership rows cross-link to `openWorkspace` where a `WS` key exists (one
human ∈ N workspaces). Detail panes: `wsdetail`, `pldetail`, `invdetail`, `pudetail`.

**access-admin.html — action seams (all live):** users rows → `openUser` (name +
"Сессии"); "Роль" prompt; "Отключить"/"Включить"/"Удалить" mutate the row's status
badge with `confirm`; invites "Копировать ссылку"/"Переслать"/"Отозвать"; sessions
"Завершить" + "Завершить все, кроме текущей". Detail pane: `udetail`.

**When wiring to live:** replace the `confirm`/`alert`/inline-mutation stubs with `api()`
calls; the **drill-down render functions and `show()` dispatch stay** — only the data
objects (`WS`/`PLANS`/`INVOICES`/`PUSERS`/`USERS`) swap from consts to fetched state.
Secret inputs in payments/models stay write-only: never read a revealed value back into
JS state or localStorage (§0, §8). Passive single-step demo buttons (PDF, Напомнить,
Продлить, Архивировать, …) are intentionally inert in the mock — wire them to their
endpoint when it exists; do not delete them.

### Vault fingerprint + key rotation (§0 invariants in code)

`renderVault` (Секреты) renders the master-key fingerprint **masked**; `wireVaultFingerprint()`
owns reveal/copy/auto-re-mask. "Изменить" → `openKeyRotationModal()`. When wiring to live:

- **Reveal** is display-only — the fingerprint is non-secret-ish but identifying; read it from
  `GET /api/vault/status` (`key_fingerprint`) and still mask by default. Do not persist it.
- **Rotation** is a dedicated server operation, NOT a PATCH of the fingerprint field. Expected
  endpoint shape: `POST /api/vault/rotate { current_key, new_key }` →
  server decrypts every credential with `current_key`, re-encrypts under `new_key`, recomputes
  and returns the new `key_fingerprint`, destroys the old key, appends an audit entry. The client
  sends the two keys (write-only, never stored) and the explicit ack; it never computes the
  fingerprint and never writes it. On `409 key mismatch` (wrong current key) surface the
  decrypt-impossible warning — do not retry blindly. The modal's checkbox ("резервная копия")
  is the operator's last guard before an unrecoverable re-encrypt.

---

## 9. Where to look when stuck

- **Endpoint map:** `DESIGNER-endpoints-brief.md`, `HANDOFF-data-integration.md` (this folder).
- **Existing adapters/composables/runtime seams:** `RUNTIME-SUBSTRATE-MAP.md`.
- **Design tokens & rules:** `DESIGN.md`.
- **Product intent:** `PRODUCT.md`.
- **Component gallery:** `components.html` (open in browser).
- **The prototype itself:** `index.html` — the contract. When in doubt, match it.
