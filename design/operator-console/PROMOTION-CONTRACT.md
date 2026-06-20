# Promotion Contract — operator-console

## Purpose

OpenDesign must keep working inside `.od/`, but the public repository and production
deployment must **not** depend on tracking the whole `.od/` workspace.

This contract establishes a **one-way promotion model**:

- `.od/` is the local/private **authoring workspace**
- `design/operator-console/` is the tracked **curated contract snapshot**
- `apps/operator-console/` is the tracked **deployable runtime source**

The model is explicit promotion, **not** code generation and **not** bidirectional sync.

## Source of truth

### Authoring source

The following remain canonical inside `.od/`:

- `.od/PRODUCT.md`
- `.od/DESIGN.md`
- `.od/DESIGN-SYNC-PROTOCOL.md`
- `.od/DEVELOPER-PLAYBOOK.md`
- `.od/HANDOFF-data-integration.md`
- `.od/RUNTIME-SUBSTRATE-MAP.md`
- `.od/INTEGRATION-AGENT-PROMPT.md`
- `.od/index.html`
- `.od/access-admin.html`
- `.od/saas-admin.html`
- `.od/components.html`
- `.od/nuxt-port/`

### Public/tracked outputs

- `design/operator-console/`
  - curated docs and canonical mockups safe for the public repository
- `apps/operator-console/`
  - deployable Nuxt source used by CI/CD and production build/deploy flows

## Non-goals

This contract does **not** make `.od/` public.

This contract does **not** promise that `apps/operator-console/` is edited directly by
OpenDesign.

This contract does **not** introduce a generator between mockup HTML and Vue/Nuxt code.

## Ownership model

### Design-owned, promoted on every sync

Changes to these paths are expected to originate in `.od/nuxt-port/` and can be
overwritten by the promotion script:

- `assets/**`
- `components/**`
- `i18n/**`
- `layouts/**`
- `pages/**`
- `composables/useHonesty.ts`
- `composables/useNav.ts`
- `scripts/parity-check.mjs`
- `PARITY.json`
- `PARITY.md`
- `PARITY.schema.json`
- `README.md`
- `NUXT-PORT-GUIDE.md`
- `app.vue`
- `app.config.ts.example`

### Developer-owned after bootstrap

These files are bootstrapped from `.od/nuxt-port/` if missing, but are **not**
overwritten by routine promotion:

- `composables/useMockData.ts`
- `nuxt.config.ts`
- `package.json`
- `package-lock.json`
- repo-aware helper scripts that adapt `.od`-relative tooling to public repo paths

Rationale:

- `useMockData.ts` becomes the live seam against the real backend
- `nuxt.config.ts` carries runtime/build policy
- `package*.json` belong to the tracked app/runtime lifecycle
- repo-aware helper scripts let the promoted app validate itself against
  `design/operator-console/` without requiring `.od/` to be tracked

If design needs a contract change that affects one of these files, it must be surfaced as
an explicit contract change, not silently pushed through promotion.

## What must never be promoted

The following stay local/private in `.od/`:

- `mq*.png`
- `*.artifact.json`
- `.od-skills/`
- `.od/.agent/`
- critique snapshots and intermediate prompt artifacts
- local build/runtime folders such as `.nuxt/`, `.output/`, `node_modules/`

## GitHub policy

Public GitHub should contain:

- `design/operator-console/` curated contract snapshot
- `apps/operator-console/` tracked runtime source

Public GitHub should **not** contain the full `.od/` working set.

## Production policy

Production receives:

- built artifacts from `apps/operator-console/`
- backend/runtime integration required for that app

Production does **not** receive:

- `.od/`
- raw mockup images
- intermediate OpenDesign artifacts
- `PARITY` authoring noise outside what the tracked app explicitly needs

## Promotion workflow

1. Work on design inside `.od/`
2. When a stable change is ready, run:

```powershell
pwsh -NoProfile -File scripts/promote-od-operator-console.ps1
```

3. Review the promoted diff in:
   - `design/operator-console/`
   - `apps/operator-console/`
4. Build/test the promoted app from `apps/operator-console/`
5. Commit only the promoted public/tracked output

## Safety rules

- Promotion is **one-way**: `.od/` -> tracked repo paths
- Do not edit design-owned files in `apps/operator-console/` and expect those edits to
  survive the next promotion
- Do not treat the promotion script as a generator; it copies a curated subset and
  preserves runtime-owned files
- If a file changes ownership, update this contract first
