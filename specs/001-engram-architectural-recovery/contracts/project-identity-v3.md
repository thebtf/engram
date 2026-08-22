# Project Identity V3 Contract

**Status**: Target contract for AR-2 and later recovery slices.  
**Authority**: `constitution.md`, `spec.md` FR-001 through FR-007, and `data-model.md`.  
**Compatibility owner**: The central project-resolution workflow.

## 1. Normative identity model

| Term | Contract |
|---|---|
| Canonical project key | The immutable server-issued UUID in `Project.project_key`. It is the sole tenant key for every project-scoped authoritative read, write, event, job, cache, audit record, import, and export. A client MUST NOT derive, mint, select, or overwrite it. |
| V3 anchor project ID | The immutable UUID in `.engram-project` field `project_id`. After registration, the server maps it to `Project.anchor_project_id` and binds it to one canonical project key. It is portable identity evidence, not a tenant key, and MUST NOT be used as a database foreign key in place of `project_key`. |
| Project identifier | A `Project Identifier` row such as a legacy binding, normalized remote-relative identifier, hash, path hash, slug, non-Git anchor, or manual alias. It is evidence or a bounded compatibility selector, never a tenant key. One `(scheme, normalized_value)` has at most one owner at a time. |
| Descriptor | A versioned request envelope containing a validated V3 anchor and non-authoritative discovery evidence. It asks the server to resolve scope; it does not assert a project key. |
| Resolution | The central server operation that validates the descriptor, applies merge redirects, and returns one active canonical project key or a typed refusal before project-scoped access. |
| Scope root | The repository root for `repository` scope or the explicitly selected directory root for `directory` scope. The root locates an anchor; its local path is not identity. |

A merged project MUST redirect through a recorded merge audit to its active target. It MUST NOT be silently reused as a new project. A global record may omit a project key only when its own native contract explicitly permits that omission.

## 2. V3 anchor document

The repository anchor is the tracked root file `.engram-project`. It MUST validate against `project-anchor-v3.schema.json`; its complete allowed field set is `version`, `project_id`, `name`, and `scope`. Existing `.engram-project` files MUST be migrated in place. A legacy name-only file is evidence of intent, not a complete identity, until the V3 fields validate and authorized registration accepts it.

| Field | Requirement |
|---|---|
| `version` | MUST equal the integer `3`. |
| `project_id` | MUST be a random UUID created once. It maps to the server-side `Project.anchor_project_id`, which is unique for active projects. It is not the canonical `Project.project_key`. |
| `name` | Mutable display metadata. It MUST NOT be an identifier or merge authorization evidence. |
| `scope` | MUST be `repository` or `directory`. It is explicit and immutable after acceptance. |

| Rule | Requirement |
|---|---|
| Canonical location | The anchor file MUST be named `.engram-project` directly beneath its scope root. |
| Repository scope | The root is the selected Git repository root, not the current working directory. The file MUST be tracked by that repository. Branches, worktrees, clones, directory moves, and remote renames retain the same anchor when they represent the same logical project. |
| Directory scope | The root MUST be explicitly selected by the adapter or onboarding workflow. A non-Git directory MAY use this scope. Discovery MUST NOT infer a directory root from a path, name, remote, or hash. |
| Immutability | `project_id` and `scope` MUST NOT change after acceptance. Changing either requires new-anchor resolution; it is not an edit to the existing project. `name` MAY change without changing identity. |
| Content safety | An anchor MUST NOT contain a canonical project key, path, remote, branch, commit hash, credential, token, principal, secret, or private payload. Unknown fields make the anchor invalid. |
| Anchor ownership | A valid `project_id` maps to exactly one `Project.anchor_project_id` and thereby one canonical project key while active. A V3 anchor collision is a merge-plan or explicit identity-decision matter, never an overwrite. |

## 3. Descriptor and response contract

Every V3-capable project-scoped adapter MUST submit the same versioned descriptor before project-scoped access. Transport encodings MAY differ, but field names, meanings, normalization, and validation rules MUST be identical across clients and covered by cross-language vectors.

| Descriptor field | Requirement |
|---|---|
| `version` | MUST equal the integer `3`. Unsupported versions are refused before scoped access. |
| `anchor_project_id` | MUST equal the validated anchor `project_id`. It is the client-side anchor identifier, not a client assertion of `Project.project_key`. |
| `name` | MUST equal the anchor `name` when the anchor is present. It is mutable display metadata only. |
| `scope` | MUST equal the anchor `scope`. |
| `normalized_git_remotes` | MAY contain normalized, credential-stripped remote evidence for migration and diagnostics. Remote text is not identity or merge authorization by itself. |
| `legacy_identifiers` | MAY contain normalized known identifier evidence with scheme and provenance. A value is a compatibility input only; it cannot mint identity. |
| `client_instance_id` | MUST be an opaque, non-secret installation reference. It distinguishes an installation, not a project. |

A descriptor MUST NOT contain a client-asserted `project_key`, a credential, a raw secret, or unredacted private content. A server response MUST contain the resolution status, a correlation reference, and, only on successful resolution, the server-resolved active canonical `project_key`, resolved scope, and any recorded redirect reference. It MUST NOT return a canonical key on a refusal.

## 4. Resolution procedure

1. An adapter MUST select the scope root before resolution. For repository scope it MUST locate `.engram-project` at the selected Git repository root, not the current working directory. A nested Git repository is a distinct selection boundary: its parent anchor MUST NOT be inherited. A non-repository subdirectory uses the selected enclosing repository root.
2. The adapter MUST validate the root anchor before constructing a descriptor. For directory scope, the adapter MUST have an explicit selected directory root; upward search is forbidden.
3. The server MUST validate descriptor `version`, anchor shape, `project_id`/`anchor_project_id` agreement, name and scope agreement, and authorization before any project-scoped data access or mutation.
4. The server MUST resolve the V3 anchor binding first. It MAY use normalized remotes and known identifiers only as corroborating evidence, compatibility translation, copied-anchor detection, or audit provenance. It MUST NOT derive a canonical key from those values.
5. If the anchor has one active binding and no copied-anchor or collision hold, the server MUST return that active canonical key. A valid new anchor MAY create a project only through an authorized registration use case. If the binding is merged, the server MUST return the recorded active target and redirect reference.
6. A scoped mutation MUST begin only after successful central resolution. Resolution and the first mutation MUST be causally bound to prevent a stale, refused, or ambiguous descriptor from being reused.
7. A resolution result MUST be recorded with redacted provenance sufficient to audit the anchor, descriptor `version`, result, and refusal or redirect reason. Raw credentials, secrets, and private payloads MUST NOT be recorded or fingerprinted.

## 4.1 Explicit Filter and Administrative Target Exceptions

A caller-supplied project value is never a general exception to canonical resolution. The only
permitted exceptions are a declared read-only filter or an explicitly privileged administrative
target operation. Each exception MUST be named in the public contract, validate the caller's
authorization before evaluating the target, retain principal/privacy restrictions, emit a redacted
audit correlation, and return no candidate-project enumeration.

- A read-only filter MAY use an inventoried unique legacy identifier only through the compatibility
  resolver; it cannot create, redirect, cache, export, or mutate a project.
- An administrative target MAY act on a named canonical project only under an explicit
  administrative authorization and mutation audit that records actor, purpose, target, decision,
  and rollback or retention boundary.
- An exception MUST NOT bypass copied-anchor, ambiguity, privacy, principal, or source validation;
  it MUST NOT select a project from path/name/remote/hash similarity.
- Every exception has an owner, supported operation list, compatibility sunset, and source/route
  scan obligation. An undocumented current caller is refused rather than grandfathered.
## 5. Required resolution outcomes

| Code | Meaning | Read behavior | Mutation behavior | Required next action |
|---|---|---|---|---|
| `PROJECT_RESOLVED` | One active canonical project key was resolved from a valid binding. | Continue in the resolved scope. | Permitted only after normal authorization and privacy checks. | None. |
| `PROJECT_REDIRECTED` | The anchor belongs to a merged project with one audited active target. | Continue only against the returned target. | Permitted only against the returned target after normal checks. | Client refreshes its resolved scope. |
| `PROJECT_ONBOARDING_REQUIRED` | The anchor is valid but unbound, or no anchor exists at the explicit scope root. | Read-only diagnostics MAY report non-scoped onboarding candidates; they MUST NOT return project-scoped data. | Refuse; create no project, identifier, cache key, or fallback tenant. | Complete explicit onboarding or authorized registration. |
| `PROJECT_ANCHOR_INVALID` | The anchor is missing required fields, has unknown fields, has an invalid UUID/version/scope, or is at an invalid location. | Refuse. | Refuse before mutation. | Repair or replace the anchor through onboarding. |
| `PROJECT_SCOPE_MISMATCH` | Descriptor scope, selected root, or anchor scope disagree. | Refuse. | Refuse before mutation. | Re-select the correct scope root. |
| `PROJECT_NESTED_REPOSITORY_UNRESOLVED` | The selected nested repository lacks its own acceptable anchor. | Return actionable onboarding information without inheriting the parent. | Refuse before mutation. | Onboard the nested repository or explicitly select the parent repository context where applicable. |
| `PROJECT_ANCHOR_DECISION_REQUIRED` | The anchor is copied, has competing lineage evidence, or would bind to more than one logical project. | Refuse project-scoped data to prevent disclosure across the disputed boundary. | Refuse before mutation. | Record an explicit same-project or rotate-anchor decision in a merge/onboarding audit. |
| `PROJECT_IDENTITY_AMBIGUOUS` | A compatibility selector maps to zero or multiple active/redirected owners. | Refuse scoped access. | Refuse before mutation. | Use a V3 anchor or resolve through an approved merge plan. |
| `PROJECT_DESCRIPTOR_UNSUPPORTED` | The descriptor `version` is not supported or has retired. | Return retirement behavior only. | Refuse before mutation. | Upgrade to V3 or use the declared temporary compatibility path. |
| `PROJECT_DESCRIPTOR_INVALID` | The descriptor asserts forbidden fields, malformed discovery facts, credentials, secrets, or invalid non-authoritative evidence. | Refuse. | Refuse before mutation. | Send a valid redacted descriptor. |
| `PROJECT_KEY_CLIENT_ASSERTION_FORBIDDEN` | A client attempts to choose scope by supplying a canonical key rather than obtaining resolution. | Refuse. | Refuse before mutation. | Resolve a V3 descriptor centrally. |

No outcome may create a path-derived, name-derived, remote-derived, hash-derived, or legacy-selector-derived project. Legacy translation may resolve an already inventoried unique identifier only as specified in `legacy-compatibility-matrix.md`.

## 6. Copy, collision, and merge boundary

A copied anchor is not proof that two repositories are one project. When discovery context, inventory evidence, or an operator report indicates a copied anchor, the resolver MUST place the anchor in `PROJECT_ANCHOR_DECISION_REQUIRED` until an explicit recorded decision chooses exactly one of the following:

| Decision | Result |
|---|---|
| `same_project` | The approved repositories share the existing canonical project key. The decision records corroborating evidence and does not create a second tenant. |
| `rotate_anchor` | The copied repository receives a new V3 anchor through onboarding and a separately bound canonical project key. Existing records remain with their original project unless an approved merge manifest moves them. |
| `merge_plan_required` | Existing canonical projects require convergence. No row reassignment occurs until a valid dry-run and approved apply manifest resolve all collision or quarantine requirements. |

A merge MAY rewrite a project-bearing record only through an approved manifest that preserves counts, provenance, revision history, privacy scope, and an append-only merge audit. Unknown rows, unresolved identifiers, conflicting credentials, and unsafe privacy decisions MUST be quarantined; they MUST NOT be assigned by similarity or timestamp.

## 7. Cross-cutting invariants

| Requirement | Contractual effect |
|---|---|
| FR-001 and FR-002 | `project_key` is the only scoped authority; anchors, descriptors, paths, names, remotes, hashes, branches, and historical selectors remain distinct evidence or metadata. |
| FR-003 and FR-004 | Repository, worktree, clone, branch, move, remote rename, nested repository, directory, non-Git, missing, malformed, copied, and ambiguous cases use the procedure and refusal codes above. No unresolved request may read scoped data or mutate it. |
| FR-005 | V3 uses one descriptor contract across supported clients. Legacy behavior is bounded and versioned by the compatibility matrix. |
| FR-006 | Every project-bearing column, relation role, serialized field, cache key, import/export field, and job payload requires inventory coverage before contraction or merge. |
| FR-007 | Deterministic merge planning requires evidence, collision disposition, dry-run receipt, idempotency, privacy non-widening, provenance preservation, quarantine, and rollback evidence. |

## 8. Validation and rollback boundary

Implementations MUST prove the same V3 resolution result for a primary checkout, worktree, clone, nested ordinary directory, branch, moved directory, and renamed remote of an accepted anchored repository. They MUST also prove zero scoped mutations for copied, malformed, missing, unbound, nested-repository-unresolved, and ambiguous cases.

Identity expansion is additive. A rollback MAY restore the last compatible read boundary only from the backup/export and migration receipt recorded by an approved merge manifest. Rollback MUST preserve merge audit, quarantine, provenance, and privacy evidence; it MUST NOT recreate selector-derived identity or silently delete records that became visible after migration.
