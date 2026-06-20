# RUNTIME-SUBSTRATE MAP — current adapters, composables, and server seams

**Register:** implementation substrate (not product UI, not design system).

## What this document is

This file is the lookup table for the **already existing runtime substrate**
behind the operator control plane. It exists so future work does **not** have to
search the legacy dashboard code to answer:

- which API adapters already exist,
- which composables already wrap live server behavior,
- which current views are worth reusing as wiring references,
- which server handlers already back the MVP control plane.

The product/design source of truth still lives in:

- `PRODUCT.md`
- `DESIGN.md`
- `DESIGN-SYNC-PROTOCOL.md` (§11 = **Канал C**, the mockup↔port drift-sync process)
- `DEVELOPER-PLAYBOOK.md`
- `HANDOFF-data-integration.md`
- `nuxt-port/` — the deployed Vue 3 + Nuxt scaffold of the UI contract. Its data seam is
  `composables/useMockData.ts`: swap mock consts for `useFetch('/api/...')` against the
  routes mapped in this file. How far each page trails the mockup is tracked in
  `nuxt-port/PARITY.json`; `npm run parity` gates drift. See `nuxt-port/PARITY.md`.

This document is the **runtime counterpart** to those files.

## What this document is not

- It is **not** the UI contract.
- It is **not** permission to preserve the current dashboard UX.
- It is **not** a signal that the old Vue screens are canonical.
- It is **not** a lock-in to the current frontend framework.

The current server UI is being **fully replaced** by the new `.od`-derived UI.
What survives from the existing runtime is the substrate: adapters, composables,
auth/session flows, build/embed path, and live HTTP/API seams. Port those seams
forward even if the app shell moves to a different frontend foundation.

Current migration posture:

- `ui/**` is the **runtime seam source**.
- `apps/operator-web/**` is the new growth-oriented frontend host substrate.

## Core rule

When wiring the new control plane:

1. read the product/design contract from the canonical `.od` docs;
2. read **this file** to find the runtime substrate;
3. reuse truthful adapters/composables/seams;
4. replace the old UI shell/views freely when they are only presentation.

Do **not** mine `ui/src/views/**` ad hoc for substrate knowledge unless this map
is missing the needed area.

---

## 1. Current client-side substrate

### 1.1 API adapters — `ui/src/utils/api.ts`

This is the main client-side transport layer already present in the repo.

#### Core helpers

- `fetchJson()` — low-level GET helper with timeout handling
- `fetchWithRetry()` — GET with retry/backoff
- `postJson()` — POST helper
- `patchJson()` — PATCH helper
- `deleteJson()` — DELETE helper

#### MVP-relevant live-backed adapters

| Area | Current adapter(s) | Notes |
| --- | --- | --- |
| Stats / summary | `fetchStats(project?)` | Backs current summary/system widgets |
| Projects | `fetchProjects()` | Existing `GET /api/projects` bridge |
| Sessions | `fetchSessions({ project, limit, offset })` | Existing `GET /api/sessions/list` bridge for the first `Projects & Sessions` MVP slice |
| Rules | `fetchRules({ project, limit })`, `createRule()`, `updateRule()`, `deleteRule()` | Current `Rules` CRUD bridge for the first browser-backed rules slice |
| Memories | `fetchMemories(project, limit)` | Added for the MVP port; uses `GET /api/memories` |
| Vault / secrets | `fetchVaultStatus()`, `fetchCredentials()`, `fetchCredential()`, `deleteCredential()` | Strong substrate for Secrets slice |
| Tokens | `fetchTokens()`, `createToken()`, `revokeToken()` | Useful if Tokens remain in first MVP |
| Issues | `fetchIssues()`, `fetchIssue()`, `acknowledgeIssues()`, `deleteIssue()`, `updateIssue()`, `createIssue()`, `fetchTrackedProjects()` | Strongest existing operator workflow substrate |
| Config | `fetchConfig()` | Current read-only config/settings substrate |
| Maintenance / update | `fetchMaintenanceStatus()`, `fetchMaintenanceLogs()`, `triggerMaintenance()`, `fetchMaintenanceStats()`, `checkForUpdate()` | Useful for System/Health and operator ops |

#### Present but not first-MVP-critical

| Area | Adapter(s) | Notes |
| --- | --- | --- |
| Patterns | `fetchPatterns()`, `fetchPatternInsight()`, `generatePatternInsight()`, `deprecatePattern()`, `deletePattern()`, `mergePatterns()` | Relevant later if a pattern screen is revived |
| Search misses / retrieval analytics | `fetchSearchMisses()`, `fetchRetrievalStats()` | Useful for Noise & Usefulness when that slice is promoted |

### 1.2 Composables — `ui/src/composables/`

These are the main reusable client runtime wrappers.

| Composable | Purpose | Reuse guidance |
| --- | --- | --- |
| `useAuth.ts` | login/session/auth-disabled/admin state | Reuse; this is substrate, not legacy UI |
| `useSSE.ts` | live connection state and event stream | Reuse; important for status/reconnect behavior |
| `useHealth.ts` | self-check polling | Reuse for `Health / System` shell signals |
| `useStats.ts` | stats loading + SSE/poll fallback | Reuse for summary/status surfaces |
| `useIssues.ts` | issue list loading with filters | Reuse or extend for new Issues UI |
| `useVault.ts` | secrets/vault list + reveal/delete flow | Reuse or extend for new Secrets UI |
| `useTokens.ts` | token lifecycle | Reuse if Tokens stay in MVP |
| `useUpdate.ts` | update/restart state | Reuse for operator system actions |
| `useColorMode.ts` | dark/light/auto preference | Reuse in the new shell |
| `useConsoleDensity.ts` | shell density preference (`compact` / `comfortable`) | Reuse from shell and settings instead of keeping local page-only state |
| `useUiI18n.ts` | locale/catalog bridge over the app-level `vue-i18n` runtime | Reuse for new MVP surfaces; avoid fresh hardcoded UI copy |
| `ui/src/utils/formatters.ts` | locale-aware relative/absolute date formatting (`formatRelativeTime(..., locale)`, `safeAbsoluteDate(..., locale)`) | Prefer passing the active UI locale on user-facing surfaces instead of relying on English defaults |
| `useMarkdown.ts` | markdown rendering support | Reuse in issue/detail/comment surfaces |
| `usePagination.ts` | generic list paging helper | Reuse where list pagination survives |
| `useLogs.ts`, `useMaintenance.ts`, `usePatterns.ts` | secondary system/support flows | Reuse only if those surfaces enter MVP |

### 1.3 Existing views — treat as replaceable reference implementations

These files are **not** the target UX. They are useful only as substrate
references for already-working data flows.

| View | Role today | Replacement posture |
| --- | --- | --- |
| `ui/src/views/HomeView.vue` | summary cards + recent issues | replace UI, reuse data ideas selectively |
| `ui/src/views/SystemView.vue` | health/config/update controls | replace UI, reuse adapters/composables |
| `ui/src/views/IssuesView.vue` | list/create/filter workflow | strong wiring reference |
| `ui/src/views/IssueDetailView.vue` | detail/thread/edit workflow | strong wiring reference |
| `ui/src/views/VaultView.vue` | secrets list/reveal/delete | strong wiring reference |
| `ui/src/views/TokensView.vue` | token issuance/revoke | medium-strength reference |
| `ui/src/views/ProjectsView.vue` | first live `Projects & Sessions` list slice | use as the new runtime reference for project/session list wiring |
| `ui/src/views/SettingsView.vue` | first live `Settings` slice | use as the runtime reference for shell prefs + read-only config wiring |
| `ui/src/views/RulesView.vue` | first live `Rules` CRUD slice | use as the runtime reference for rule list/create/edit/delete wiring |
| `ui/src/views/AdminView.vue` | admin users/invitations | use only if Access/Admin enters scope |
| `ui/src/views/LoginView.vue`, `RegisterView.vue`, `SetupView.vue` | auth/bootstrap screens | preserve flows, replace presentation as needed |

### 1.4 Shell/runtime entry points

| File | Current role | Reuse guidance |
| --- | --- | --- |
| `ui/src/App.vue` | top-level app shell | replace shell layout to match `.od` |
| `ui/src/router/index.ts` | route registry + auth guard | preserve routing/auth logic, expand/rename routes as needed |
| `ui/src/components/layout/AppSidebar.vue` | current nav shell | replace freely |
| `ui/src/i18n/index.ts` | app-level `vue-i18n` plugin registration | treat as the base locale runtime, not a per-view helper |
| `ui/package.json.tpl` | frontend dependency/template source used by `dashboard` build path | update this together with `ui/package.json` or the next server build will silently drop new UI deps |
| `ui/src/assets/main.css` | current token/theme layer | replace or heavily realign to `.od/DESIGN.md` |
| `ui/src/main.ts` | app bootstrap | preserve |

### 1.5 Frontend build -> server embed path

| File / path | Current role | Reuse guidance |
| --- | --- | --- |
| `Makefile` target `dashboard` | materializes `ui/package.json` from `ui/package.json.tpl`, builds `ui/dist`, then copies it into `internal/worker/static/` | treat this as the build source of truth for embedded UI until a different build pipeline is introduced |
| `internal/worker/static/` | generated embed input directory for server-side Web UI assets | do not hand-edit; it is a generated target populated from `ui/dist` |
| `internal/worker/static.go` | `go:embed static/*` + `/` and `/assets/*` serving path | this is the actual runtime seam that proves whether the new UI reached the server binary |

---

## 2. Current server-side substrate

### 2.1 Worker HTTP handlers already useful for MVP

| File | Surface |
| --- | --- |
| `internal/worker/handlers_memories.go` | `GET /api/memories`, `POST /api/memories`, memory detail/delete paths |
| `internal/worker/handlers_issues.go` | issue list/detail/create/update/delete/ack flows |
| `internal/worker/handlers_vault.go` | secrets/vault endpoints |
| `internal/worker/handlers_auth.go` | login/session/auth/bootstrap flows |
| `internal/worker/handlers_data.go` | projects, stats, models, observations/data helpers |
| `internal/worker/handlers_projects.go` | soft-delete project path |
| `internal/worker/handlers_sessions.go` | `GET /api/sessions/list`, `GET /api/sessions?claudeSessionId=`, hook/session lifecycle paths |
| `internal/worker/handlers_rules.go` | `GET /api/rules`, `POST /api/rules`, `PATCH /api/rules/{id}`, `DELETE /api/rules/{id}` |
| `internal/worker/handlers_system.go` | runtime config surface |
| `internal/worker/handlers_stats.go` / `handlers_stats_v7.go` | stats/health analytics surface |
| `internal/worker/handlers_update.go` | update/restart flows |

### 2.2 Important truth about sessions

- `internal/worker/handlers_sessions.go` is mostly hook/SDK session behavior.
- `internal/worker/handlers_sessions_rest.go` is explicitly the removed indexed
  sessions REST path from v5, not a modern operator workspace.

This means “Projects & Sessions” in the new control plane should **not** assume
there is already a rich ready-made current sessions UI substrate equal in
quality to Issues or Vault.

### 2.3 Rules truth

- `internal/worker/handlers_rules.go` is currently thin on REST.
- `HANDOFF-data-integration.md` is the reliable source for the mixed REST/MCP
  reality of rules.

This means the new `Rules` screen should be built against the actual live
surface mix, not by assuming a complete REST module already exists.

---

## 3. MVP-first reuse guidance

### Reuse directly

- auth/session bootstrap behavior
- issues transport + issue detail flows
- vault/secrets transport + reveal/delete logic
- stats/health/update logic
- projects list adapter
- sessions list adapter
- rules CRUD bridge
- memory list adapter and related server endpoints

### Reuse carefully

- tokens flow
- admin users/invitations
- config/settings read surface

### Do not preserve as UX baseline

- current sidebar grouping
- current topbar/statusbar composition
- current card hierarchy and spacing language
- current English-first labels in the old views
- any current shell/layout choice that conflicts with `.od/index.html`

---

## 4. First files to open for each new control-plane slice

### Shell / navigation

- `.od/index.html`
- `ui/src/App.vue`
- `ui/src/components/layout/AppSidebar.vue`
- `ui/src/router/index.ts`

### Memories

- `.od/index.html`
- `.od/HANDOFF-data-integration.md`
- `ui/src/utils/api.ts`
- `internal/worker/handlers_memories.go`

### Rules

- `.od/index.html`
- `.od/HANDOFF-data-integration.md`
- `ui/src/utils/api.ts`
- `ui/src/views/RulesView.vue`
- `internal/worker/handlers_rules.go`
- `internal/db/gorm/behavioral_rules_store.go`

### Issues

- `.od/index.html`
- `ui/src/composables/useIssues.ts`
- `ui/src/views/IssuesView.vue`
- `ui/src/views/IssueDetailView.vue`
- `internal/worker/handlers_issues.go`

### Secrets

- `.od/index.html`
- `ui/src/composables/useVault.ts`
- `ui/src/views/VaultView.vue`
- `internal/worker/handlers_vault.go`

### System / health

- `.od/index.html`
- `ui/src/composables/useHealth.ts`
- `ui/src/composables/useStats.ts`
- `ui/src/views/SystemView.vue`
- `internal/worker/handlers_system.go`
- `internal/worker/handlers_stats*.go`

### Projects / sessions

- `.od/index.html`
- `.od/HANDOFF-data-integration.md`
- `ui/src/utils/api.ts`
- `ui/src/views/ProjectsView.vue`
- `internal/worker/handlers_projects.go`
- `internal/worker/handlers_sessions.go`

---

## 5. Maintenance rule

When new runtime substrate appears during the port:

- add it here,
- do not bury it only inside a finished view,
- keep the doc focused on reusable runtime seams,
- keep UX decisions in the `.od` contract docs, not here.
