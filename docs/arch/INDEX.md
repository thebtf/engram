# Architecture documentation

These documents describe the current v5+ runtime from executable source and
deployment artifacts. They do not treat old README prose, Swagger descriptions,
comments, or a surviving symbol as proof that a feature is active.

## Start here

1. [README](../../README.md) — operator/developer front door and safe setup.
2. [System overview](OVERVIEW.md) — boundaries and data paths.
3. [Runtime components](COMPONENTS.md) — owners, configuration, and deployment forms.
4. [Architecture rationale](architecture.md) — durable decisions and tombstones.
5. [Current surface ledger](current-surface.json) — machine-readable claims,
   feature gates, and a source-derived Nuxt page inventory.

The canonical architecture diagram is in the README. Keeping one diagram avoids
parallel descriptions drifting apart.

## Classification vocabulary

- `live` — a current registration, executable path, or tracked page proves it.
- `flag-gated` — source has a path, but its named runtime flag must enable it.
- `optional-runtime` — source can initialize it from configuration; source alone
  cannot prove a particular deployment enabled it.
- `convergence-in-progress` — a route or backend capability exists, but the
  end-to-end operator journey is not yet accepted as public setup guidance.
- `tombstone` — explicitly excluded from current setup despite stale remnants.

## Scope and language status

The English README is canonical. Existing Russian and Chinese README variants
are intentionally not presented as synchronized instructions until their
structure and technical claims have been refreshed from the accepted English
source.

The ledger records only repository-relative evidence. It is a claim inventory,
not a user journey: a page file proves a route exists in the Nuxt application,
not that every operation on that page is ready to teach as a setup workflow.
Each console route entry separately records `page_source`, `deployment_forms`
with a per-form `direct_load_status`, `capability_status`, and `journey_status`;
`page_source=present` never implies `journey_status=accepted`.
