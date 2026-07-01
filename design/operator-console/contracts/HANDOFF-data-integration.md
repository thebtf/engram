# engram Operator Console — Data Integration Brief

**Audience:** a developer or coding agent (Claude Code / Codex) wiring the console
prototype (`index.html`) to a real engram server at `unleashed.lan:37777`.

> **Verified ground truth: `DESIGNER-endpoints-brief.md`** (probed 2026-06-18 against the
> live server, build `v6.25.1-1-g4cae0b9`). Where this brief and that file disagree, the
> verified file wins. Live deltas already folded into the table below:
> - **Use HTTP, not HTTPS** — the HTTPS probe fails TLS; HTTP returns live data.
> - `GET /api/memories/{id}` and `PATCH /api/rules/{id}` are **built** (were must-build).
> - `GET /api/rules` and `POST /api/rules` are now **built in repo** as the dashboard REST bridge for list/create; live-server truth must be reprobed after deploy rather than assumed from the older 2026-06-18 probe.
> - `/api/flags`, `/api/migrations`, `/api/settings/model` still return **404** — still must-build / MCP-only.
> - `/api/stats/vnext` shows `noise_ratio: 0`, `injection/citation: 0` because **95% of sessions are unrecorded** (`unrecorded_fraction ≈ 0.955`) — the empty KPIs are real, not a bug. Embedding coverage is **100%** (2837/2837, dim 1536).
> - `/api/projects` returns a **plain string array** of project ids, not objects.
> - Auth is disabled but `/api/auth/me` (401), `/api/admin/users` (403), token endpoints still require auth — render identity/admin as locked.

**Why this doc exists.** The console today renders from inline mock arrays and a
synchronous `renderArea()` — there is no network layer. This brief gives the
**data contract** (the shared spine) once, then two ways to use it:

- **Part B — Test Surface:** stand up a mock that returns these exact shapes, so the
  console can be developed and demoed against realistic, flag-aware data without the
  prod server.
- **Part C — Wiring Guide:** replace the inline arrays with real `fetch` calls to the
  endpoints the server *already* exposes, at the exact seams named below.

Pick B to unblock UI work without prod; pick C to go live. Both consume Part A — do
not skip it. **The four honesty guardrails in Part D are non-negotiable in both
paths** — they are why this console is trustworthy, and a naive integration breaks them.

**Boundary note:** this file is the **data/endpoint contract**, not the source
of truth for which Vue-side adapters/composables already exist. That runtime
inventory lives separately in `RUNTIME-SUBSTRATE-MAP.md` so the finished UI
never needs to be mined as documentation.

---

## Part A — The Data Contract (the spine)

Every surface in the console maps to exactly one server operation. Columns:

- **Inline seam** — the `const` in `index.html` (all near lines 778–965) that holds the
  mock today. This is what you replace (Part C) or imitate (Part B).
- **Server op** — the live REST route or MCP tool from `operator-console-domain-schema.md`.
- **Class** — `live` (works on prod now) · `dormant:<FLAG>` (real code, returns empty/null
  until that flag is on) · `must-build` (no surface yet — render the placeholder, never fake
  data) · `tombstone` (DO NOT CALL — see Part D).
- **Gate** — feature flag that must be on for the op to return non-empty.

| Console surface | Inline seam | Server op | Class | Gate |
|---|---|---|---|---|
| Memory grid | `MEMORIES` | `GET /api/memories?project=&limit=` | live | scope-filtered under VNEXT_F |
| Memory detail | (opens from `MEMORIES`) | `GET /api/memories/{id}` | **live** (built on v6.25.1; 404 on missing/invisible) | — |
| Memory search | (search pop) | MCP `recall_memory` / `recall(action=search)` | live | hybrid leg needs VNEXT_ENABLED + EMBEDDING_URL |
| Suppress / edit / supersede | row actions | MCP `suppress_memory` · `store(action=edit)` · `store_memory(supersedes=[])` | live | — |
| Tier / confidence ops | `MEM_ACTIONS` (dormant items) | MCP `lifecycle(promote\|demote\|set_confidence\|info)` | dormant:LIFECYCLE | LIFECYCLE |
| Flag / unflag | `MEM_ACTIONS` (MB item) | `PATCH /api/memories/{id}/status` | **must-build P1** | — |
| Candidate queue | `CANDIDATES` | MCP `list_candidates(project,status,limit)` | dormant:VNEXT_F | VNEXT_F |
| Promote / reject / supersede candidate | queue actions | MCP `promote_candidate(dry_run)` · `reject_candidate(reason)` · `supersede_candidate` | dormant:VNEXT_F | VNEXT_F |
| Candidate detail | (opens from `CANDIDATES`) | `GET /api/candidates/{id}` | **must-build P1** | VNEXT_F |
| Noise & citations | (noise cards) | `GET /api/stats/vnext` → `noise_ratio`, injection/citation/uncited, per-project | live | — |
| noise_ratio **trend** | (trend placeholder) | metrics time-series | **must-build P1** | — |
| Behavioral rules | `RULES` | `GET /api/rules?project=&limit=` | live | — |
| Create / delete rule | rule actions | `POST /api/rules` · `DELETE /api/rules/{id}` | live | — |
| Edit rule (content + priority) | rule actions | `PATCH /api/rules/{id}` `{content,priority,edited_by}` | **live** (built on v6.25.1) | — |
| List / create rules | `RULES` seam | `GET /api/rules` / `POST /api/rules` | live | — |
| Enable / disable rule | rule toggle | needs `enabled` column | **must-build** | — |
| Issues board | `ISSUES` | `GET /api/issues` · `GET /api/issues/tracked-projects` | live | — |
| Issue detail + thread | (opens from `ISSUES`) | `GET /api/issues/{id}` (REST adds display names) | live | — |
| Create issue | issue modal | `POST /api/issues` | live | **use REST, not MCP** (MCP create risks FR-13 1s timeout) |
| Issue state machine | issue actions | `PATCH /api/issues/{id}` `{status\|title\|body\|priority\|type\|labels}` | live | close → send `source_project='dashboard'`; reject → mandatory comment + admin; delete → admin, hard-delete |
| Bulk acknowledge | bulk bar | `POST /api/issues/acknowledge {ids}` | live | — |
| Credentials | `CREDS` | `GET /api/vault/credentials?project=` (list) · `GET /api/vault/credentials` (all, REST-only) | live | — |
| Reveal credential | row "Reveal" | `GET /api/vault/credentials/{name}` | live | the one decrypt op; 409 on fingerprint mismatch |
| Create / delete credential | cred actions | `POST` · `DELETE /api/vault/credentials/{name}` | live | delete = hard-delete (rotation) |
| Vault status + orphans | vault card | `GET /api/vault/status` · `DELETE /api/vault/orphaned-credentials` | live | orphan delete irreversible — type-to-confirm |
| Settings — model config | `SETTINGS`, `AI_CREDENTIALS`, `AI_MODELS`, `AI_BINDINGS` | MCP `settings(list/get/set/delete)` | live | REST CRUD = **must-build MB-3** (`GET/PUT/DELETE /api/settings/model`) |
| Settings — flag state | `flags` (line 778) | `GET /api/config` flags group | **must-build MB-7** (alt MB-1 `GET /api/flags`) | — |
| Projects | `PROJECTS` | `GET /api/projects` · `DELETE /api/projects/{id}` (archive) | live | — |
| Purge project | project danger zone | MCP `admin(purge_project, confirm)` | dormant:VNEXT_ENABLED + admin | 9-table hard-delete |
| Sessions | `SESSIONS` | `GET /api/sessions/list` · `GET /api/sessions?claudeSessionId=` | live | — |
| Set session outcome | session action | MCP `feedback(outcome)` / `set_session_outcome` | live | manual rank-6 feed (auto path demolished) |
| Code Intel | `CLIENT_SURFACES`/area | MCP `codebase_status` · `codebase_search` | dormant:CODE_INTEL | CODE_INTEL (V1 FTS-only) |
| Knowledge graph | (graph area) | MCP `graph(get_edges\|traverse\|find_path…)` | dormant:GRAPH | GRAPH (+ VNEXT_F for nodes) |
| Health landing | (health cards) | `GET /api/selfcheck` · MCP `check_system_health` · `GET /api/vector/metrics` | live | — |
| Migration state | (health MB card) | `GET /api/migrations` (goose_db_version) | **must-build MB-2** | — |
| Admin identity chip | topbar | `GET /api/auth/me` → role, `auth_disabled` | live | — |
| Tombstones drawer | `TOMBSTONES` | — (static list, never fetched) | tombstone | — |

**`flags` (line 778) is the master switch for the whole gate cascade.** In the live
build, hydrate it from `GET /api/config`/`GET /api/flags` (MB-7/MB-1). Until that
endpoint exists, keep it operator-set or read from `GET /api/auth/me` + whatever
partial flag info `/api/config` already returns. Every `dormant:<FLAG>` row above must
check `flags[FLAG]` before fetching, and render the existing "inert until ENGRAM_X" gate
state when it's off — **do not fetch a dormant op when its gate is off; you'll get
empty/null and render it as "no data" instead of "feature off."**

---

## Part B — Task: build the Test Surface

> **Prompt to hand a coding agent.** Goal: a local mock that lets the console run against
> realistic, flag-aware data with zero prod dependency.

Build a small mock server (Node + a single file is fine; or a static `fixtures/` dir +
a 30-line dev server) that serves the **Part A endpoints** with the shapes the console
already expects. Requirements:

1. **Match the inline mock shapes.** The arrays in `index.html` (`MEMORIES`, `CANDIDATES`,
   `RULES`, `ISSUES`, `CREDS`, `PROJECTS`, `SESSIONS`, `AI_*`) ARE the response schema —
   copy their field sets verbatim into fixtures. `GET /api/memories` returns the
   `MEMORIES` shape; `GET /api/stats/vnext` returns `{noise_ratio, injection_count,
   citation_count, uncited_count, per_project[]}`; etc.
2. **Be flag-aware.** Expose a mock `GET /api/config` returning the `flags` object. For
   every `dormant:<FLAG>` endpoint, return `[]`/`{}` (empty, not an error) when the flag
   is off, and populated fixtures when on. This lets the console's gate states be tested
   honestly.
3. **Honor must-build as 501, not fake success.** `GET /api/candidates/{id}`,
   `GET /api/flags`, `GET /api/migrations` (both 404 live), the
   `/api/settings/model` CRUD, etc. return **HTTP 501 + `{error:"must-build", id:"MB-N"}`**.
   The console must keep rendering its violet MB placeholder, never invent a detail panel.
4. **Reproduce the two trap behaviors** so the UI guards are exercised:
   - `search_collection` (if surfaced) returns **HTTP 200 with a deprecation STRING body**,
     not an error. The console must detect the string and show "search disabled", not "0 results".
   - Issue create via the MCP path should simulate a >1s delay (FR-13) so the console's
     "use REST" choice is validated.
5. **Secret discipline.** `GET /api/vault/credentials` never includes secret values.
   Only `GET /api/vault/credentials/{name}` returns a decrypted value, and only once per
   call; optionally return 409 sometimes to exercise the fingerprint-mismatch path.
6. **CORS + a single base URL toggle** so the console points at `http://localhost:<port>`
   in dev and `https://unleashed.lan:37777` in prod via one constant.

Deliverable: the mock server/fixtures + a one-line README on how to start it and point the
console at it.

---

## Part C — Task: wire the prototype to the real server

> **Prompt to hand a coding agent.** Goal: replace inline mock with real `fetch`, at the
> seams named below, without breaking the honesty guardrails (Part D).

> **If you are working in the Nuxt scaffold (`nuxt-port/`, the canonical target — see
> `DEVELOPER-PLAYBOOK.md` framework note):** the seams below already exist, lifted into
> `composables/useMockData.ts`. There is no module-scope `const` / synchronous
> `renderArea()` to convert — instead swap each `useXxx()` mock for `useFetch('/api/...')`
> in that one file; pages, classification, and honesty signals stay untouched. The
> Part A data contract and the Part D guardrails apply identically. The port follows the
> mockup through the `nuxt-port/PARITY.json` drift ledger (`npm run parity`); the process
> is `DESIGN-SYNC-PROTOCOL.md` §11 (Канал C) + `nuxt-port/PARITY.md`. The steps below
> describe the original vanilla `index.html` and remain the reference for the data shapes
> and states each seam must produce.

The console is intentionally easy to wire — all data lives in named module-scope `const`s
and one synchronous render path. Steps:

1. **Add a tiny data layer.** Create an `api` object with one method per Part A op, e.g.
   `api.listMemories(project)` → `fetch(`${BASE}/api/memories?project=${project}`)`. Put
   `const BASE` at the top (env-switchable). Centralize auth: send the session cookie /
   bearer per `GET /api/auth/me`. Keep MCP-only ops (candidates, graph, settings, outcome)
   behind the same `api.*` facade even if they tunnel through a small MCP bridge — the
   UI shouldn't care which transport.
2. **Make the render path async.** Today `renderArea()` (≈line 1410) is synchronous and
   reads the inline `const`s directly. Convert each area's data read to `await api.*`, and
   give every area three real states it currently fakes or skips:
   - **loading** — now legitimate: render a skeleton of the entity grid (the prototype
     deliberately has no skeletons because render was instant; with a network they become
     correct, not theater).
   - **error** — `fetch` rejected / non-2xx: render an inline error with a **retry** button
     and the status code; never blank the area.
   - **empty vs gated** — distinguish "endpoint returned `[]`" (empty state: "no memories
     match") from "flag is off" (gate state: "inert until ENGRAM_VNEXT_F_ENABLED"). These
     look different and mean different things — the `flags` check decides which to show
     *before* the fetch.
3. **Replace the seams, one area at a time** (each is independent — ship incrementally):
   `MEMORIES`→`api.listMemories`, `CANDIDATES`→`api.listCandidates` (guard on VNEXT_F),
   `RULES`→`api.listRules`, `ISSUES`→`api.listIssues`, `CREDS`→`api.listCredentials`,
   `PROJECTS`/`SESSIONS`→their list ops, `SETTINGS`/`AI_*`→`api.getSettings`. Hydrate
   `flags` (line 778) from `api.getConfig()` first, since it gates everything else.
4. **Mutations are optimistic + reconciled.** Suppress/resolve/promote apply on confirm with
   a "· Undo" affordance where the server soft-deletes; on error, roll back and toast the
   failure. Bulk ops show the dry-run count, then call the bulk endpoint, then refetch the
   affected list so `noise_ratio` and counts update.
5. **Leave must-build placeholders exactly as they are.** Where Part A says must-build, the
   `api.*` call should return the MB sentinel (or the mock's 501) and the UI keeps its
   violet placeholder. Wiring is done when *live* ops show real data and *must-build* ops
   still honestly show "not built — MB-N".

Deliverable: `index.html` (or split `js/api.js` + `js/render.js`) reading live data, with
loading/error/empty/gated states real, and a short note listing which areas are live vs
still-mocked vs must-build-blocked.

---

## Part D — Honesty guardrails (non-negotiable in B and C)

These are the reason the console is trustworthy. A naive integration breaks all four.

1. **Never call a tombstone.** `rate_memory` / `feedback(rate)`, `POST /api/import/feedback`
   (410), instinct import (501), the `/api/sessions/index|check` shims — these are in
   `TOMBSTONES` and must stay a read-only diagnostics list. No `api.*` method may exist for them.
2. **`search_collection` returns a STRING, not an error.** Check the payload, not just
   `res.ok`. A deprecation string → render "search disabled (removed v5)", never an empty
   result set. This is the one endpoint where `res.ok === true` is a trap.
3. **Restart-required ≠ applied.** Settings writes that the server marks restart-required
   must keep the console's "saved · restart to apply" state. Never flip a restart-required
   switch to a green "applied" just because the PUT returned 200.
4. **Secrets are write-only.** Never store a revealed credential value in client state or
   re-send it. `Reveal` is a one-shot decrypt display; the secret leaves memory when the
   panel closes. List/status responses never carry secret values — if one does, treat it
   as a server bug and drop it.

---

## Recommended sequence

1. **Part B first** (test surface) — unblocks all UI work and makes loading/error/empty
   states demonstrable without prod.
2. **Then Part C** against the mock, area by area.
3. **Flip `BASE` to `:37777`** — only the live-class rows light up; dormant rows wait on
   their flags; must-build rows stay honest placeholders that double as the build backlog
   (Part A's must-build column == the server team's to-do list).

**Source of truth:** `operator-console-domain-schema.md` (every op + class + evidence
`file:line`) and `switches-schema.md` (the Settings-tab config contract). This brief is the
integration layer over both.
