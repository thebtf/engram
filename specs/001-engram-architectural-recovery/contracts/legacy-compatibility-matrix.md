# Legacy Project Identity Compatibility Matrix

**Status**: Target compatibility and retirement contract.  
**Authority**: `constitution.md`, `spec.md` FR-001 through FR-007 and FR-025 through FR-026, `data-model.md`, `project-identity-v3.md`, `project-merge-manifest.schema.json`, and `migration-receipt.schema.json`.

## 1. Compatibility boundary

V3 canonical project identity is the only authority for project-scoped data. Compatibility is a temporary server-side translation boundary, not a second identity system. It exists only to preserve a supported client or historical record while V3 adoption, inventory, and migration complete.

The resolver MUST translate a legacy selector only when its normalized value is already present in the `Project Identifier` inventory, has exactly one active or redirecting owner, satisfies normal authorization and privacy checks, and has not reached retirement. Translation MUST produce server-resolved V3 scope. It MUST NOT mint a project, write an identifier, create a cache key, infer ownership from a similar value, or turn the selector into a client-asserted canonical key.

A legacy client that needs a scoped mutation MUST invoke this central translation boundary first. The mutation MAY proceed only with the resulting server-resolved scope and its causal resolution record; sending an unresolved legacy selector directly to a project-bearing mutation is refused.

## 2. Client and identifier matrix

| Input family or client capability | Permitted during the declared compatibility window | Refusal or degradation rule | Sunset disposition |
|---|---|---|---|
| V3-capable adapter with a valid V3 descriptor | Full central resolution for reads and mutations after normal authorization, privacy, and scope checks. The descriptor carries `version: 3`, anchor `project_id` as `anchor_project_id`, `name`, `scope`, normalized credential-stripped remotes, legacy identifiers, and `client_instance_id`. | Missing, invalid, copied, unbound, scope-mismatched, or ambiguous anchors use the V3 typed outcomes and never fall back to a selector. | Permanent supported contract. |
| `anchor_v3` identifier record | Resolver binding and audited redirect lookup only. Its value corresponds to `.engram-project` `project_id` as mapped to `Project.anchor_project_id`. | An unbound, colliding, copied, or retired binding is held for onboarding, identity decision, or merge planning. | Remains as the authoritative portable identifier evidence for active V3 projects. |
| `binding_v2` | Read or central-resolution bridge only when the inventory has one unique owner. A bridge result is V3 server scope, not a V2 tenant. | Unknown, retired, or multiply owned values return `PROJECT_IDENTITY_AMBIGUOUS`; direct selector-only mutation is refused. | Retire after all supported callers submit V3 descriptors and no retained serialized compatibility payload requires translation. |
| `git_remote_relative_v2` | Same bounded bridge as `binding_v2`; values are credential-stripped evidence only. | Remote rename, fork, or collision cannot establish identity. Ambiguity, copies, and unknown values refuse rather than choose by remote text. | Retire after V3 anchor adoption and observed zero bridge use. |
| `git_hash_v2` | Same bounded bridge as `binding_v2` for already inventoried unique values. | A hash cannot mint or select a project, including after move, clone, branch, or remote change. | Retire after V3 anchor adoption and observed zero bridge use. |
| `path_hash_v1` | Read/resolve bridge only for an inventoried unique legacy identifier during the window. | A new, moved, missing, or collision-prone path hash MUST NOT create or select a tenant. | Retire before contraction of path-derived identity fields and caches. |
| `legacy_slug` | Read/resolve bridge only for an inventoried unique value and only where the native adjacent capability still declares a consumer. | Equal display names or slugs are not merge evidence. Unknown or multiple owners refuse. | Retire after all retained consumers use canonical keys or a versioned translation adapter. |
| `non_git_anchor_v2` | Read/resolve bridge only for an inventoried unique value. | It does not authorize automatic directory identity. New non-Git scope requires explicit V3 directory onboarding. | Retire after existing directory projects have V3 directory anchors or explicit quarantine dispositions. |
| `manual_alias` | Read/resolve bridge only if the alias has one active/redirecting owner and provenance. | An alias with missing provenance, competing ownership, or retired status refuses. | Retire or preserve only by an explicit approved adjacent-capability compatibility decision; it is never canonical. |
| Legacy serialized request, cache key, job payload, import, or export field | Translation is allowed only after the field family is inventoried, versioned, and mapped to a known unique identifier. New writes MUST carry a canonical `project_key` and, where a client boundary needs it, a V3 descriptor/resolution reference. | Unknown fields, raw private values, or records without a unique mapping are quarantined. Cache or payload replay MUST NOT recreate identity from legacy text. | Contract only after every inventoried family has a canonical replacement, a readback receipt, and no live producer or consumer. |
| Unsupported or retired descriptor/client version | May receive an actionable upgrade or retirement response only. | No scoped read, write, fallback identifier resolution, or silent downgrade. | Returns `PROJECT_DESCRIPTOR_UNSUPPORTED` or the documented retirement code until the client upgrades. |

The matrix does not assert a current client census. AR-1 establishes the redacted observed client/version and project-bearing-field inventory that is required to declare any compatibility window or sunset.

## 3. Window controls

A compatibility window MUST be declared in a versioned retirement record before the bridge is enabled. The record MUST name all of the following:

| Required declaration | Purpose |
|---|---|
| Supported legacy identifier schemes, client versions, routes, adapters, and serialized field families | Bounds exactly what may translate. Everything not named is refused or quarantined. |
| First supported release, last compatible release, and planned contraction release | Binds compatibility and rollback to installable release boundaries. |
| Named owning team or workflow and support contact | Makes retirement behavior actionable. |
| Per-family resolution, ambiguity, refusal, and mutation metrics with nonzero denominators | Demonstrates actual bridge use and safe decline before removal. |
| Required V3 upgrade path and error-to-remediation mapping | Lets clients upgrade without guessing a selector or key. |
| Backup/export reference, migration receipt requirements, and last compatible read boundary | Preserves rollback capability. |
| Observation window and required evidence receipts | Prevents contraction based only on a planned date or green source scan. |

The window starts only when the declared release is installed with its migration receipt and bridge instrumentation. It ends only when every sunset criterion below passes. A calendar date, repository tag, absence of new writes, or a zero metric denominator alone MUST NOT end the window.

For an approved single-row historical adoption, the compatibility record MUST name the last V2 read boundary and each supported client version, selector, read and mutation route, and serialized field. Preflight does not mint a UUID or modify the old row. Until every affected role is reconciled and both paths switch under the same write fence, V3 refuses and V2 reads remain at the declared compatible boundary; divergent TEXT-only writes are refused. After cutover, a supported V2 selector resolves the adopted row's server-issued UUID before reading or writing canonical stores. A required historical TEXT projection is read-only and versioned; unsupported versions or unclassified fields refuse. An adoption receipt records the old-row evidence reference, plan, approvals, backup, per-role comparisons, quarantine, and rollback boundary without fabricating a source UUID or merge manifest.


## 4. Client behavior during transition

1. A V3-capable client MUST send the intake-defined descriptor fields (`version`, `anchor_project_id`, `name`, `scope`, `normalized_git_remotes`, `legacy_identifiers`, and `client_instance_id`) for every scoped operation and MUST treat a server-returned canonical key as opaque resolved scope.
2. A legacy client MUST send only its versioned selector to the compatibility resolver. It MUST NOT synthesize an anchor, transform a selector into a canonical key, or retry an ambiguity result with a path, name, remote, hash, or alias variant.
3. The compatibility resolver MUST return exactly one of: a V3-resolved scope, a V3 typed refusal, or a retirement response. It MUST not return a guessed project or a list of candidate projects that could disclose another project's existence.
4. Legacy reads, imports, exports, replay, and mutation bridges MUST preserve principal and privacy checks. Translation is not an authorization bypass.
5. A V3 response and any translated legacy response MUST include a version and correlation reference suitable for redacted audit. They MUST NOT include secret values, credentials, raw private content, or raw remote URLs.
6. For a merged project, translation MUST return the recorded active target and redirect reference. It MUST NOT revive the source as a writable tenant.
7. A retry with the same accepted request identity MUST be idempotent. A failed, ambiguous, or quarantined resolution MUST not leave a project, identifier, cache entry, import side effect, job, or mutation behind.

## 5. Sunset gate

Every legacy family may be retired only when all applicable conditions are true:

1. **Inventory closure**: Every project-bearing table column, relationship role, serialized field, cache key, import/export field, job payload, route, hook, tool, and client adapter has a terminal inventory disposition. Unknown or unsafe rows have a visible quarantine item with owner and required decision.
2. **V3 replacement**: Every supported producer and consumer has a tested V3 descriptor or canonical-key boundary. Cross-language descriptor vectors prove equivalent resolution behavior for each supported adapter.
3. **Migration integrity**: For every accepted merge group, dry-run, approved apply, repeat-run idempotency, count comparison, provenance/privacy verification, and rollback rehearsal have receipts. For every accepted single-row adoption, its own approval, exact-role reconciliation, semantic comparison, quarantine, idempotency, and rollback rehearsal have an adoption receipt. Each collision is deterministically resolved or quarantined.
4. **Observed compatibility**: The declared observation window has a nonzero measured denominator for each still-supported legacy family, and its declared upgrade or retirement threshold is satisfied. No unresolved ambiguity, copied-anchor hold, or translation failure may be hidden in an aggregate metric.
5. **No live bridge dependency**: Source/configuration/package/route/tool/hook/client scans and installed behavior show no remaining supported producer or consumer that requires the legacy translation path. Retained historical data remains readable through a declared compatible boundary or explicit quarantine ledger.
6. **Rollback readiness**: Backup/export, migration receipt, last compatible read boundary, and restore or forward-compensation procedure are proven for the exact contraction candidate.
7. **Retirement behavior**: The exact candidate returns an actionable, versioned retirement response to legacy input and does not silently recreate a legacy selector, project, cache, payload, or routing path.
8. **Release evidence**: The independently installable recovery slice has its required migration, installed verification, observation, and security evidence. Architecture-era flags and duplicate orchestration are removed only after those receipts exist.

After the sunset gate, a formerly supported legacy selector MUST receive `PROJECT_DESCRIPTOR_UNSUPPORTED` when its version is retired or `PROJECT_IDENTITY_AMBIGUOUS` when a still-parseable value lacks exactly one known owner. It MUST NOT be re-enabled by a maturity flag, undocumented alias, cache fallback, or ad hoc migration script.

## 6. Rollback and re-entry

Compatibility contraction is reversible only to the last compatible read boundary recorded in the applicable approved merge manifest or single-row adoption receipt. A rollback MUST use that operation's verified backup/export and receipt, preserve later canonical UUID writes through restoration or forward compensation, retain immutable audit/quarantine and privacy/grant evidence, and never revive TEXT as a second tenant authority. It MUST NOT allocate a new project from raw historical identifiers.

Reopening a retired legacy bridge requires a new versioned compatibility record, fresh inventory, a documented risk and owner, a bounded observation plan, and an approved rollback boundary. It is not an automatic consequence of a client error or migration failure.

## 7. FR traceability

| Requirement | Matrix guarantee |
|---|---|
| FR-001 to FR-004 | Legacy input is translated only to server-resolved V3 scope; no selector-derived tenant is created, selected, or mutated. |
| FR-005 | Descriptor versions and a bounded legacy window define cross-client migration without preserving parallel authority. |
| FR-006 | Serialized fields, caches, payloads, exports/imports, relations, and adapters require complete inventory before sunset or contraction. |
| FR-007 | Merge manifests require deterministic conflict handling, dry-run/apply/rollback receipts, idempotency, provenance preservation, privacy non-widening, and quarantine. |
| FR-025 and FR-026 | Expand, backfill, compare, cutover, observe, and contract retain backup/receipt/rollback evidence and end in actionable retirement behavior. |
