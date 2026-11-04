# API contracts

This is the canonical reference for an agent replacing the UI or implementing an
Engram client. It describes implemented **v7** behavior, including uneven legacy
wire formats; it does not specify an API redesign.

**Source boundary:** the measured released-v7.0.19 baseline is
`8edd95ccb7cdd12049f92c6beaa8981a25335190`, tree
`f7471d2809a46ab0060da00adc793f2cf332cdee`. Current recovery and issue contracts
below are reconciled against corrective source
`3ad28a61256feb7012844aacc13197ff673b0c9d`, composed with the accepted baseline
documentation at integration input `4cc8ccbc2eac3a777e9487ed739e468f0790bf87`.
These are source boundaries, not installed versions or runtime-delivery proof.
The measured coverage below remains the released baseline, not a final-candidate
census. See [Current-source reconciliation](#current-source-reconciliation).

## Authority and coverage

Use these owners together, not a generated client alone:

1. [HTTP route registration](../../internal/worker/service.go), including
   `registerMemoryCollectionOperationRoutes` in
   [handlers_memories.go](../../internal/worker/handlers_memories.go), owns which
   method/path combinations exist and which require database readiness.
2. [Generated Swagger JSON](../swagger.json), [YAML](../swagger.yaml) and the
   served `/api/docs/` describe the annotated subset. Named DTOs below and this
   reference supply the conditional and business semantics Swagger 2 cannot express.
3. The operation tables name exact Go request/response owners. Frontend parsers
   are useful consumers, not a second authority for permissions or payload shape.
4. [MCP schemas](#mcp-contract) and [protobuf](#grpc-contract) own their transports;
   neither is an HTTP client schema.

### Measured released-source inventory

The unit is a unique **method + route template**, not a URL with particular
query values, a page, a handler function, file bytes or an advertised tool count.
The source census includes conditional registrations, not only currently enabled
features. Nested `/api/admin` prefixes and the four delegated Memory selection
routes are included.

| Projection at the released base | Denominator / observed coverage |
| --- | --- |
| HTTP registrations | **209** method/templates: **199** machine/API operations, **8** static/SPA registrations and **2** Swagger UI registrations. `/health` and `/v1/models` are in the machine set. |
| Checked-in Swagger before this documentation change | **79** operations on **69** paths, **63** definitions. Only **77** operations match a registered method/path: the two session-init/summarize annotations omitted `/api`. |
| Promoted `apps/operator-console` client | **103** distinct operations, including its grant chooser and the server-issued document-export download. **38** have a matching base Swagger operation. |
| Separate `apps/operator-web` client | **33** distinct operations. This is an additional source consumer, not the promoted Docker frontend. |
| Combined frontend contract | **110** distinct operations, **42** with matching base Swagger operations. One requested operation, `GET /api/maintenance/stats`, has no registered API handler. |

The frontend census includes actual request seams in console `composables/` and
`components/CodeGrantChooser.vue`, and web `app/composables/`. It excludes mocks,
tests, static bundles, comments, evidence labels and `unsupportedOperatorAction`
strings. The two clients overlap; 103 + 33 is not a union count. Each operation
in that union has a family row below. The separate historical `ui/` build is not
silently included in this denominator.

Concrete base Swagger omissions include all Issues, Operator Code, Access and
collection-selection routes; config/flags/migrations/model-health; browser
email/password authentication; document read/history/comments; principal memory
and domain administration. Presence of a nonempty schema is therefore not full
coverage. This change adds the representable browser-auth, config/flags,
model-health and document-reader annotations, and corrects both session paths.
Unannotated families remain explicitly linked to their maintained DTO/handler
owners below; a generic `object` response is not a complete schema.

For the released baseline after its documentation/annotation regeneration:
**91** operations, **80** paths, **75** definitions; all **91** operations match
the released router. Swagger covers **91 of 199** machine operations. Matching
frontend Swagger coverage is **53 of 110** operations, up from 42. The remaining
57 comprise 56 registered operations documented through the family contracts and
exact maintained owners here, plus the one explicitly unsupported web request.
These are preserved released-source measurements, not a current-candidate
coverage census or a claim that Swagger covers the complete API.

### Swagger production and limitations

[`Makefile`'s `worker` target](../../Makefile) is the existing producer. Lines
89–102 discover local Go dependency directories, put `cmd/engram-server` first,
then run Swag 1.16.6 with `-g main.go --parseInternal --parseGoList=false
--packageName docs -o docs`. Its outputs are `docs/docs.go`, `docs/swagger.json`
and `docs/swagger.yaml`. Regenerate these together; never hand-edit them or add a
parallel schema generator. The generation step can be run without the following
server `go build` when only documentation is changing.
The installed generator's `--version` string reports v1.16.4 even though
`go version -m` binds its module to `github.com/swaggo/swag v1.16.6`; invoking the
v1.16.6 module directly reports the same string. Check module provenance rather
than installing another generator solely because of that banner.

Swagger 2 does not completely express:

- Cookie authentication, server-resolved browser identity and per-domain guards.
- Query-versus-body alternatives, closed scalar query sets, repeated-value AND/OR
  rules, or two conditional success payloads on one method/path.
- Omitted versus null conditional fields in UCI responses, opaque capability
  binding, privacy/existence suppression and stale-selection behavior.
- JSON-versus-SSE content negotiation, bodyless errors/successes, partial
  operations, unknown commit outcomes and the lifecycle of async work.

For example, `/api/projects` retains its ordinary catalog schema and uses
`x-retained-records-response` for its explicitly selected historical envelope.
Auth request maps in Swagger do not make every arbitrary map key a supported
field; the required fields and guards are documented below. An annotation's
`ApiKeyAuth` is not a promise that a worker keycard can call an admin/browser-only
operation. `info.version` is schema metadata, not the server release version.

## Replacement UI and deployment boundary

**Go owns durable data, authentication, authorization, canonical identity,
privacy, mutation transactions, selection versions, opaque refs, async jobs and
readback.** A replacement UI owns presentation, routes/navigation, form drafts,
loading/error/empty states, cancellation and selection display. It must not
reimplement project membership, infer identity from labels, calculate grant or
selection tokens, read local worktree bytes as an indexed View, or turn a failed
read into successful empty content.

The promoted console is **Vue 3 + Vue Router + Vite**, not Nuxt. Its owners are
[package.json](../../apps/operator-console/package.json),
[API wrapper](../../apps/operator-console/composables/useOperatorApi.ts),
[operator config](../../apps/operator-console/operator-config.ts) and
[Node static/API host](../../apps/operator-console/scripts/static-host.mjs).
The retained `.output/` and `/_nuxt/` output names do not imply a Nuxt runtime.

| Deployment/build path | Actual behavior |
| --- | --- |
| [Dockerfile](../../Dockerfile), server target | Builds `apps/operator-console`, then embeds its `.output/public/` in `internal/worker/static/`. Go serves HTML/assets and the API on the server origin. |
| Docker console target / [Compose](../../docker-compose.yml) | Runs the dependency-free Node host on port 3000. `ENGRAM_PUBLIC_API_BASE=/api`; `/api/*` is forwarded to `ENGRAM_OPERATOR_API_TARGET=http://server:37777`. No second business backend is introduced. |
| Go external-console proxy | [static.go](../../internal/worker/static.go) uses `ENGRAM_OPERATOR_CONSOLE_URL` for browser delivery when configured; it does not turn that upstream into the durable API owner. |
| `make dashboard`, and therefore `make build` | Still builds legacy `ui/` and replaces the embedded static directory with `ui/dist/`. This differs from Docker and has **not** been fixed by this documentation change. `make worker` regenerates Swagger and builds Go; it does not select the promoted UI by itself. |
| `apps/operator-web` | A separate Nuxt application, with its own [Nuxt config](../../apps/operator-web/nuxt.config.ts) and [API client](../../apps/operator-web/app/composables/useOperatorApi.ts). It is not selected by the released Docker/Compose recipe. |

Use one configured API origin/base. Console `operatorApiUrl` accepts a base ending
in `/api` and avoids duplicating that segment when a caller supplies `/api/...`;
Code callers also use `/code/...` relative paths. Keep query values URL-encoded.
The separate web client concatenates `public.apiBase` with paths such as
`/projects`; its server proxy configuration is not interchangeable with console
build configuration.

Browser requests use `credentials: 'include'`. The Go server sets, verifies,
expires and revokes HttpOnly cookies; the UI must not copy credentials into
local/session storage or issue a new identity to work around a denial. A proxy
must forward Cookie, Set-Cookie, method, query, body and status faithfully. Do not
assume cross-origin cookies work because a GET is reachable: CORS uses an exact
allowlist, browser SameSite rules still apply, and the shipped CSP has
`connect-src 'self'`. Same-origin `/api` forwarding is the ordinary arrangement.

`GET /health` gives the SPA only for an explicit HTML Accept request; machine
requests receive health JSON. `/api/health` is always the machine endpoint. An
unknown GET may reach the SPA catch-all and return HTML: check content type and
payload, not only HTTP 200. No page filename proves a backend endpoint exists.

## HTTP rules shared by clients

The default server listener is `:37777`; HTTP and gRPC are multiplexed by cmux.
[Middleware](../../internal/worker/middleware.go) and
[rate limiting](../../internal/worker/ratelimit.go) run before handlers.

- Send `Content-Type: application/json` for JSON POST/PUT/PATCH. A nonempty,
  incompatible content type returns 415. Missing content type is not a universal
  rejection in the current middleware. Backup verification is a distinct
  multipart endpoint, not JSON.
- The ordinary body limit is 10 MiB. Operator Code decoding is stricter at
  32 KiB; config PATCH is 1 MiB; session indexing has its own compressed and
  decompressed limits. Do not extrapolate one limit to every operation.
- The readiness group returns 503 until database initialization completes. Some
  recovery/config/update/health/log routes are outside it, but their own store
  dependencies can still return 503. Readiness is not authentication.
  Ordinary ready-group requests have a 30-second handler timeout; consolidation
  operations have their explicit ten-minute timeout. SSE opts out. A deadline
  does not prove that a mutation was rolled back or an async job was cancelled.
- `X-Request-ID` is general request correlation and is echoed; absent values are
  generated. `X-Engram-Request-ID` is the separate operation-bound header required
  by Code/selection protocols. Neither header, `source_agent`, `created_by_session`,
  `session_id`, `client` labels or `cwd` authenticates a caller.
- A project selector is an exact live canonical UUID where the operation requires
  one. A label, directory, repository locator, unresolved reference, Source ID,
  Checkout ID or View ID is not interchangeable with that UUID. Empty project is
  not a universal “all projects” selector.
- Native record IDs are generally positive integers. Browser-session management
  handles, tab IDs, context handles, grant refs, selection refs and cursors belong
  to different namespaces. Preserve their exact bytes and use them only at the
  operation that issued them.

### Response and error handling

There is **no universal envelope or universal JSON-error contract**. Ordinary
`writeJSON` responses use JSON; `http.Error` commonly produces `text/plain` plus a
newline, sometimes with JSON-looking text. Documents/auth use JSON error helpers.
Code/selection boundaries deliberately return many bodyless 400/403/409/412/503
responses. Cookie logout and session initialization also have distinct success
bodies. SSE and export downloads are not JSON snapshots.

| Observation | Required client behavior |
| --- | --- |
| 401 | Re-establish authentication through the supported login; retain the user's draft. Do not replay a failed mutation automatically. |
| 403 | Treat as denied scope/role/identity. A syntactically valid UUID or known ref is not authority. Do not widen project scope to retry. |
| 404 | Often intentionally conflates hidden, wrong-context and absent records. Do not display “exists but private” unless the operation explicitly supplies that information. |
| 409 | State/version/binding conflict, environment-controlled setting, or protocol idempotency mismatch; refresh the appropriate current state before a new intent. The body depends on the family. |
| 412 | Selection precondition is stale or needs reconfirmation; obtain and confirm a new selection, not just a new request ID. |
| 413 / 415 / 429 | Respect body/media/rate boundaries. Do not split a non-idempotent intent into new requests without its operation contract. |
| 500 / 503 / timeout / disconnect | Preserve the failure. A mutation may have committed before its response was lost; outcome is unknown until operation-specific readback. A 503 is not an empty collection. |
| 200 with partial/degraded/error fields | Interpret the body. Examples include failed update checks, retrieval degradation and UCI partial results; HTTP success alone does not mean complete work. |
| 202 | Admission/acknowledgement only; read the async operation status and its terminal result. |
| 204 | Successful bodyless result; do not call `response.json()` unconditionally. |

For a read, use the exact same authorized scope when refreshing. For a mutation,
read back by its issued ID/ref or current selection and distinguish committed,
committed-but-unverified, partial, denied and unknown outcomes. The console's
[mutation parser/executor](../../apps/operator-console/composables/useApi.ts)
implements presentation states, not a general server idempotency guarantee.

## Authentication, attribution and Access

Owners: [TokenAuth middleware](../../internal/worker/middleware.go),
[token validator](../../internal/auth/validator.go),
[master/keycard handlers](../../internal/worker/handlers_auth.go), and
[browser/Access handlers and DTOs](../../internal/worker/auth_handlers.go).

Ordinary machine clients use `Authorization: Bearer <credential>`. The server-host
operator token and database-issued workstation keycards are different
credentials; read-only keycards cannot mutate. Native principal and workstation
identity come from verified authentication, never a request body's actor label.
`ENGRAM_AUTH_DISABLED=true` installs a synthetic identity for supported ordinary
operations. It is not a human browser session or a bypass for Access management.
Trusted-proxy identity is accepted only through the configured proxy/auth chain,
not arbitrary forwarded identity headers.

### Browser login operations (7 frontend-consumed operations)

| Method and path | Input, result and important boundary |
| --- | --- |
| `GET /api/auth/me` | Returns `authenticated`, `role`, `auth_disabled`; a DB browser identity may include `user:{id,email,role}`. Disabled-auth additionally reports `source:"auth-disabled",synthetic:true`. Unauthenticated is 401, not a fabricated user. |
| `GET /api/auth/setup-needed` | Public. Returns 503 if the auth handler is not initialized or the user store is unavailable, and 500 if counting users fails. A successful `{needed:boolean}` has `needed:true` when the initialized user store counts zero users. True does not grant setup authority. |
| `POST /api/auth/setup` | JSON `email`, `password`; requires the server-host operator bearer token even on first run. Returns 201 `{user:safeUser}` without logging in. Existing users/racing setup return 409. |
| `POST /api/auth/user-login` | JSON `email`, `password`; trimmed email, nonempty password. 200 `{user:safeUser}`, plus private `engram_auth` cookie: HttpOnly, Path `/`, SameSite Lax, **7 days**. JSON never returns the credential. 400 malformed input; 401 invalid credentials; 403 disabled account; 429 login throttling. |
| `POST /api/auth/user-logout` | No required body. Revokes that cookie's DB session, clears the cookie, returns `{status:"ok"}`; absent cookie succeeds. Foreign/opaque logout Origin is 403 before mutation; revocation failure is 503, not a successful local sign-out. |
| `POST /api/auth/login` | Separate master-token flow: JSON `token`, 200 `{authenticated:true,role:"admin"}` and HMAC `engram_session` cookie, HttpOnly, SameSite Strict, 30 days; Secure is set when `r.TLS != nil`. It is not a DB user login. |
| `POST /api/auth/logout` | Origin-guarded removal of `engram_session`; returns `{authenticated:false}`. It does not revoke an `engram_auth` DB session. |

`safeUser` is `{id,email,role,disabled,created_at,last_login_at?}`. The current
email/password cookie setter does not set Secure itself; document the deployment
rather than assuming the master-cookie TLS behavior applies to it. The separate
web setup client sends email/password but does not itself supply the operator
bearer; setup-needed does not make that request authorized.

`POST /api/auth/register` is also public but requires JSON
`{email,password,invitation}`. It validates nonempty fields and a minimum password
length of **8 bytes** in the current Go `len` check, then atomically consumes the
invitation and creates a user. 201 returns safe user metadata, not a cookie;
invalid/expired/revoked/email-mismatched invitation is 403, reused invitation or
duplicate email is 409.

**Known implemented limitation (#539):** setup and registration pass password
bytes to bcrypt. Inputs over 72 bytes produce 500 `{"error":"internal error"}`
under the current source, not the desired validation 4xx. Setup does not share
registration's eight-byte minimum. This document does not claim either behavior
has been repaired. Do not send real passwords in examples, logs or bug reports.

### Access administration (11 frontend-consumed operations)

All seven GET templates below require the existing **session-admin reader**
guard. Master/client keycards and synthetic auth-disabled identities do not pass
it. Existing HMAC session-admin read admission remains; mutation guards can be
stricter and require the current enabled DB admin cookie. A generic admin role
string copied into JSON is not a browser identity.

| Operation | Maintained shape / behavior in `auth_handlers.go` |
| --- | --- |
| `GET /api/access/providers` | `accessProvidersResponse`: `{providers:[accessProviderView],auth_disabled,local_login_enabled,authentik_trusted_proxy_count}`. Provider `configured`, `enabled`, `operable`, `honesty`, `evidence` are distinct signals, not a secret or a login proof. |
| `GET /api/access/invitations` | `{invitations:[AccessInvitationView...]}`; optional `limit`. The ordinary Access view omits invitation codes; its native owner is [domain_owner_store.go](../../internal/db/gorm/domain_owner_store.go). |
| `POST /api/access/invitations` | `createInvitationRequest`: required nonblank email; optional validated role/expiry. Future RFC3339 `expires_at` takes precedence over positive `expires_in_hours`; otherwise seven-day default. 201 includes the one-time code/id/email/role/expiry. Treat the issued code as sensitive; do not auto-retry creation. |
| `POST /api/access/invitations/{id}/revoke` | Positive native ID; optional `{reason}`. `{status:"ok",id}`; used invitation is 409, absent 404. |
| `GET /api/access/users` | `{users:[safeUser...]}`; no client-invented paginated total. |
| `GET /api/access/users/{id}` | Positive user ID; maintained Access user-detail DTO with safe sessions/audit, not raw auth credentials. Unknown user is 404. |
| `PATCH /api/access/users/{id}` | `updateUserRequest`: optional pointer fields `disabled`, `role`, at least one update. Returns `{user:safeUser}`. Last-enabled-admin guard and valid roles are enforced; disabling revokes sessions. |
| `GET /api/access/roles` | `{roles:[AccessRoleSummary...]}` from the server's role model, not editable frontend role definitions. |
| `GET /api/access/sessions` | `{sessions:[AccessSession...]}`. Optional `include_revoked=true` includes expired/revoked rows; IDs are public management handles, never cookie credentials. |
| `POST /api/access/sessions/{id}/revoke` | Public management handle and optional `{reason}`. Requires the live DB admin cookie; returns `{status:"ok",id}`. Cookie credentials and unknown handles return 404. |
| `GET /api/access/log` | `{entries:[AccessAuditRecord...]}` with safe browser-session correlation. Optional `limit`. This is not the `/api/logs` process log stream. |

Access limited readers use default 100, cap 500; invalid/nonpositive `limit`
falls back to the default, unlike strict Knowledge pagination. Their safe DTOs
are `AccessInvitationView`, `AccessSessionView`, `AccessAuditEntry` and
`AccessUserDrilldown` in [domain_owner_store.go](../../internal/db/gorm/domain_owner_store.go).
Invitations/sessions order by created_at DESC; audit by created_at DESC. The
user drilldown has its own bounded lists. Do not invent offset/cursor fields.
The older `/api/admin/invitations` GET/POST, `/api/admin/users` GET and
`PUT /api/admin/users/{id}` remain separate registered routes; they delegate to
their named `handleAdmin*` owners, not imaginary `/api/access` aliases.

### Keycards (3 frontend-consumed operations)

`GET /api/auth/tokens` returns `{tokens:[...]}` with metadata only: id, name,
token prefix, scope, principal/kind, created/expiry/last-used/revoked fields.
`POST /api/auth/tokens` decodes `tokenCreateRequest`: required nonempty `name`,
scope (`read-write` default or `read-only`), optional principal/kind and future
RFC3339 `expires_at`. If principal is absent, principal_kind must also be absent;
if principal is present and kind absent, kind defaults to `human`. Explicit kinds
are `human|agent|service`; reserved HAP principals have additional issuance rules.
Success returns the one-time token and metadata; the UI must not persist it as
ordinary page state. `DELETE /api/auth/tokens/{id}` revokes by ID and returns
`{revoked:true}`. These routes apply their session-admin guard, not just the
global bearer check. `GET /api/auth/tokens/{id}/stats` is separately registered
and annotated; it is not a fourth operation used by the current keycard client.
The historical `/api/tokens` paths are not registered aliases.

## Project and SDK-session operations (8 frontend-consumed operations)

The project catalog, legacy resolver, three unresolved readers, project DELETE
and two session GETs make up this consumer family. The canonical/retained reader
contracts are detailed [below](#canonical-project-and-knowledge-readers).

`GET /api/projects/resolve?legacy_id=<encoded-selector>` returns either
`{status:"resolved",project_id,name}` for a live canonical mapping, or
`{status:"unresolved",reference,reason}`. Unresolved is a successful inspection,
not a resolved identity or a grant. The separate repository_locator mode accepts
exactly one actual normalized repository locator and returns its native logical
inspection DTO. The modes are mutually exclusive. Resolve is not bootstrap,
select, migration or marker mutation.

`DELETE /api/projects/{id}` soft-removes a live project by setting removed_at;
200 returns `{id,removed_at}` (RFC3339Nano). Data is not hard-deleted. Already
removed/unknown is 404; a matching label is not the selector. The handler's
existing ID validation is not an excuse for a client to invent a canonical UUID.

[handlers_sessions.go](../../internal/worker/handlers_sessions.go) owns both
session reads. `GET /api/sessions/list` accepts optional project, limit default
50 (valid 1..200), offset default 0, min_prompts default 0, and integer from/to
epoch-millisecond bounds. Invalid limit/offset/min_prompts retains its default;
invalid from/to parses to zero. It returns `{sessions:[SDKSession],total,limit,offset}`
with an array even when empty. Filters are additive before count/page; ordering is
started_at_epoch DESC, id DESC in [session_store.go](../../internal/db/gorm/session_store.go).
`GET /api/sessions?claudeSessionId=<exact-source-id>` returns one SDKSession,
400 when missing, 404 when absent. The released detail reader does not acquire
a project filter merely because the list page selected one. There is no
registered `/api/sessions/{id}/transcript` or `/route` reader. Never pass browser
management handles as SDK/Claude session IDs.

## Memory reads and mutations (8 frontend-consumed operations)

[handlers_memories.go](../../internal/worker/handlers_memories.go) owns
`storeMemoryRequest`, safe audit DTOs and Memory collection operations;
[models](../../pkg/models) own the native Memory fields. The detailed canonical
list/detail/filter semantics are retained in
[Canonical project and knowledge readers](#canonical-project-and-knowledge-readers).

- `GET /api/memories` returns the canonical `{items,total,offset,limit,has_more}`
  page, not a bare array. `GET /api/memories/{id}` and `/audit` require the same
  canonical project. Do not treat missing `omitempty` metrics as a measured zero.
- `POST /api/memories` requires nonempty `project`, `content`; optional `tags`,
  `source_agent`, `privacy_scope`, `session_id`, `agent_visibility`, `domain`.
  Legacy privacy scopes are `private|project|shared|global`; principal visibility
  is the **different** `private|shared` field. Server authentication supplies the
  owner/workstation. A requested principal visibility without an owner is invalid.
  Normal success is 201 with the Memory DTO; a domain-write proposal can use a
  distinct decision response. Inspect `writeStoreMemoryResponse` rather than
  assuming every accepted write creates a memory. This direct write's required
  project check is not the strict canonical-reader parser; do not infer one
  uniform validation contract from the field name.
- `DELETE /api/memories/{id}?project=<UUID>` uses by-ID visibility and domain-manage
  authority; hidden/absent is nondisclosing 404. It returns the action receipt,
  not an empty body. The UI must preserve unknown outcomes on transport failure.
- `GET /api/memories/principal` decodes `principalMemoryQueryResponse` through
  [handlers_principal_memory.go](../../internal/worker/handlers_principal_memory.go):
  required `principal`; `principal_kind` defaults to `human` and also accepts
  `agent|service`; optional `project`, `domain`, `q`, `visibility=private|shared|all`,
  `include_private`, `session_id`, `offset`, `limit`. This family returns
  `{principal,principal_kind,project?,domain?,items,hidden_count,audit,audit_status}`,
  not a Knowledge page. Cross-principal private read can be 403; do not infer a
  hidden count from ordinary Memory-list totals. Limit defaults to 50 and caps
  at 500; offset defaults to 0. Malformed/nonpositive limit or negative offset is
  400. Private inclusion and its audit status remain principal-query policy,
  not ordinary list pagination.
- `POST /api/memories/selection` and `POST /api/memories/operations` are the
  current console's versioned mutation route. Their exact domain-local DTOs are
  in the same file; the common protocol is described below.

Other registered Memory mutations are `POST /api/memories/suppress` with
`{ids,reason?}`, `POST /api/memories/{id}/suppress` with optional `{reason}`, and
`POST /api/memories/{id}/transition` with required `{action}`. Transition actions:
`suppress` active→flagged; `unsuppress` flagged→active; `archive` active/flagged→
archived without erasure. Optional query `project` is one canonical UUID and only
narrows live membership. 404 conflates missing/denied; 409 rolls back conflicting
state/version; success `{id,action,status,version,project}`. No automatic retry is
safe after an unknown outcome. There is no registered `PATCH /api/memories/{id}`,
`/flag` or `/provenance` endpoint.

### Memory-domain administration (3 frontend-consumed operations)

[handlers_memory_domains.go](../../internal/worker/handlers_memory_domains.go)
owns `memoryDomainUpsertRequest`, `memoryDomainResponse` and
`memoryDomainsListResponse`; [domain_owner_store.go](../../internal/db/gorm/domain_owner_store.go)
owns normalized modes, domain ownership and list ordering/caps.

- `GET /api/memory-domains` returns `{domains:[{domain,owner_principal,
  owner_principal_kind,mode,created_at,updated_at}]}`. Optional exact filters:
  `domain`, `owner_principal`, `owner_principal_kind`, `mode`; `limit` defaults to
  100 (capped at 500) and `offset` to 0. Invalid positive/nonnegative pagination
  is 400. Ordering is domain ASC; modes are `off|warn|reject`.
- `PUT /api/memory-domains/{domain}` accepts `{owner_principal,
  owner_principal_kind,mode}`, trims/normalizes as the handler specifies, returns
  the saved domain DTO. It does not make the caller that principal.
- `DELETE /api/memory-domains/{domain}` returns `{deleted:true,domain}`;
  missing owner is 404. All three require admin authority. Registration changes
  domain-write policy, not the ownership of every existing memory.

## Documents (8 frontend-consumed operations)

Owner: [handlers_documents.go](../../internal/worker/handlers_documents.go),
including `documentCreateRequest`, `documentReadResponse`,
`documentHistoryResponse`, `documentCommentsResponse`, `documentOperationResponse`
and the export DTOs. [versioned_document_store.go](../../internal/db/gorm/versioned_document_store.go)
owns version creation, history ordering and stored content. The canonical list
rules appear in the Knowledge section below.

| Operation | Required input / response / semantics |
| --- | --- |
| `GET /api/documents` | Canonical `project`; offset/limit/query/type/prefix filters. Metadata-only `{items,total,offset,limit,has_more}`; latest version per path is selected **before** filtering/counting. |
| `GET /api/documents/read` | Canonical `project`, exact `path`, optional positive `version`; omitted version is latest. `documentReadResponse` has `{id,path,project,content,content_hash,doc_type,metadata,author,version,created_at}`. `metadata` is a JSON-encoded **string**, not a nested object. Missing document is 404. |
| `GET /api/documents/history` | Canonical `project`, exact `path`; optional positive `limit` capped at 200. Omitted limit means all retained versions in this implementation. `{path,project,versions:[{id,version,content_hash,author,created_at}],count}`; count is returned rows, not a cursor total. |
| `GET /api/documents/comments` | Required positive `document_id` for a native version, no project parameter. `{comments:[documentCommentItem],count,document_id}`. This existing reader has the authenticated-route boundary, not the canonical list/detail project parser or an invented per-document ACL. |
| `POST /api/documents/comment` | Required positive `document_id`, nonblank `content`; optional author (default `operator`), positive `line_start`, `line_end` with end≥start when both provided. 201 `{comment_id,document_id,author,message:"comment added"}`. Authorship is attribution, not authentication. |
| `POST /api/documents` | Ordinary creation: required trimmed `path`, nonblank `project`, nonblank content; optional `doc_type` default `markdown`, `metadata` string default `"{}"`, author default `operator`. 201 `{id,path,project,message:"document created"}`. An `action` instead selects the distinct selection-operation/export protocol; request_id/selection without action is 400. Do not expect one uniform 201 DTO for both modes. |
| `POST /api/documents/selection` | Domain-specific selection snapshot; `documentSelection*` DTOs and the common selection rules below. The returned version/token, not browser array indexes, bind an operation. |
| `GET /api/documents/exports/{artifactID}` | Download the exact server-issued artifact URL from an export operation. Owner/session-bound immutable JSON artifact, not a new list/read endpoint. Respect Content-Type/Content-Disposition/length and lifecycle failure; an operation status without ready artifact bytes is not a successful export. |

`/api/documents/selection/current` and `/selection/page` also exist with their
maintained DTOs. No document content, secret or arbitrary metadata becomes
searchable merely because it is present in a read response. Creation/comments do
not advertise generic request deduplication; after a lost response, reconcile the
native version/comment rather than blindly creating another.

Minimal synthetic list/read examples, not production data:

```http
GET /api/documents?project=11111111-1111-4111-8111-111111111111&limit=1&offset=0
```

```json
{"items":[],"total":0,"offset":0,"limit":1,"has_more":false}
```

```http
GET /api/documents/read?project=11111111-1111-4111-8111-111111111111&path=notes%2Fexample.md&version=1
```

```json
{"created_at":"2026-11-04T00:00:00Z","path":"notes/example.md","project":"11111111-1111-4111-8111-111111111111","content":"Synthetic note","content_hash":"950d4ff0dc553dbf53bf3e2089167931f2c2fe4ec94aca17f115793a36740437","doc_type":"markdown","metadata":"{}","author":"operator","id":1,"version":1}
```

The digest is SHA-256 of the exact synthetic content bytes. Actual IDs, timestamp,
versions and hashes come from the store; a requested example path need not exist.

## Issues (10 frontend-consumed operations at the released baseline)

Owners: [HTTP DTOs/handlers](../../internal/worker/handlers_issues.go),
[request metadata validation](../../internal/worker/issue_request_metadata.go),
[issue_store.go](../../internal/db/gorm/issue_store.go),
[attribution and lifecycle history](../../internal/db/gorm/issue_attribution.go)
and [canonical issue readers](../../internal/db/gorm/issue_project_readers.go).
The table describes current source. Source/target project membership, historical
origin, creator keycard, actor label, client, host session and recipient session
are separate facts; none of the attribution fields grants authorization.

| Operation | Current-source request and response |
| --- | --- |
| `GET /api/issues` | Optional canonical target `project`, canonical `source_project`, exact `source_agent`, `source_session`, `target_session`, `participant_session`, CSV `status`, normalized `type`, epoch-ms `resolved_since`, `limit` default 50, `offset` default 0. Invalid/nonpositive limit and invalid/negative offset fall back; invalid resolved_since is ignored. No maximum is imposed by this handler/store list path. `{issues:[issueListReadItem],total,project_names}`. Filters precede count/page; order is critical→high→medium→low, then `created_at DESC, id DESC`. Session-filter validation is stricter; see below. |
| `GET /api/issues/{id}` | Native issue ID; optional single canonical `project` narrows membership. Adds `history_events`, `history_limit`, `history_has_more`, `history_next_after_id` to `{issue,comments,comment_count,source_project_display_name,target_project_display_name}`. Lifecycle history is bounded separately from comments, which remain a full `created_at ASC` read. Hidden/wrong membership is 404. |
| `POST /api/issues` | Required nonempty title; body, priority, type, source_project, target_project, source_agent, **created_by_session**, client, target_session, labels. Target defaults to source when omitted; target must then be nonempty. Store defaults: status `open`, priority `medium`, type `task`; omitted recipient is unknown. 201 `{id,message:"issue created"}`. Creator keycard comes from verified context, not JSON. `created_by_session` already existed at the released baseline. |
| `PATCH /api/issues/{id}` | `status`, `comment`, source_project/source_agent, session_id, client, target_session, title/body/priority/type/labels. 200 `{message:"issue updated"}`; atomic fields/lifecycle history/optional-comment transaction. Empty scalar fields generally mean no change; this is not JSON Merge Patch. Recipient omission preserves; empty string or null clears. Recipient-only updates do not resolve the issue. Field edits, including a mixed field/status PATCH, require creator/operator authority. Target-project rerouting is not in this DTO. |
| `DELETE /api/issues/{id}` | Exact acting `source_project=<canonical UUID>&source_agent=human` for human-console attribution, no body. Operator authorization is required; success is **204**, not JSON. |
| `POST /api/issues/acknowledge` | `issueAcknowledgeRequest`: positive `ids`, acting source_project/source_agent; `{acknowledged:number}`. Not a lifecycle-close operation. |
| `POST /api/issues/selection` | `issueSelectionSnapshotRequest`: selection plus acting source_project/source_agent. Returns `{selection:issueSelectionSnapshot}`. |
| `POST /api/issues/selection/current` | Acting source_project/source_agent; returns the current server-owned selection under that scope. |
| `POST /api/issues/selection/page` | Acting source fields plus filter or issued cursor, limit; returns `{filter_fingerprint,cursor,targets,next_cursor,total}`. |
| `POST /api/issues/operations` | `issueSelectionOperationRequest`: request_id, action, selection_version/kind/token and action-specific status/priority/labels, acting source fields. Returns request_id, operation_state, item_results and authoritative readback where supplied; it is not the direct PATCH envelope. |

Authenticated read-write participants, including creators and other participants,
may comment, mark `resolved`, close or reopen under the existing transition
checks. Readonly and unauthenticated callers remain denied. Unrelated field edits
(including `target_session`) retain creator-keycard/operator authority. Direct
and selected deletion are operator-only; a creator read-write keycard alone cannot
delete. Matching a project or session label does not grant authority. Human admin
operations additionally require explicit canonical acting project and
`source_agent=human`; those labels do not replace the verified actor.

`resolved` means a reported solution awaits verification. `closed` records an
actor's checked-completion claim, not independent evidence that checks happened.
Check the relevant behavior before closing; if unavailable, comment with what
remains unverified. A current or historical status label alone proves no prior
verification. Existing closed rows do not acquire invented verification events.
`acknowledged` remains accepted backlog, not done.

For non-operators, close accepts `resolved` or `reopened`; reopen accepts
`resolved` or `closed`. Operator close retains its wider existing transition
authority, but an already-closed issue cannot be closed again. The HTTP update
path keeps `open`, `acknowledged` and `rejected` operator-only; rejection also
requires a comment. Native MCP `update` accepts only `status="resolved"` or a
recipient update; use explicit `close`/`reopen`, with `comment` for transition
reasons. OpenClaw's separate `engram_issues` reopen input uses `body`, mapped to
the HTTP comment; do not interchange the two tool contracts. Lifecycle events
are recorded even without a user comment, in the same transaction as the action.
Failed authorization, invalid transitions and rollback are not successful writes.

`issueReadProjection` exposes canonical project IDs when lawfully mapped and
preserves historical `project_origin` rather than relabeling provenance.
`GET /api/issues/tracked-projects` is a separate registered reader; it is not an
implicit permission to treat a historical label as a canonical project.

Released attribution example (no credentials or personal data):

```http
POST /api/issues
Content-Type: application/json
```

```json
{"title":"Synthetic client check","type":"task","source_project":"11111111-1111-4111-8111-111111111111","target_project":"11111111-1111-4111-8111-111111111111","source_agent":"human","created_by_session":"example-source-session"}
```

```json
{"id":1,"message":"issue created"}
```

The server must authenticate/authorize the request; the example source-session
string does not establish a browser login or a new visibility boundary. This
example preserves the released POST's existing `created_by_session` field; its
presence is not a newly implemented guarantee. Current-source attribution and
history additions follow below, without claiming installed-client acceptance.

### Per-call attribution, recipients and list selection

HTTP POST takes reported `created_by_session`; PATCH takes reported `session_id`.
Native MCP mutation arguments use `session_id`, `client` and separate
`agent_source`; the canonical acting project comes from verified native caller
context, never the optional compatibility `project` argument. On native list,
`project` filters the target project; get needs only its issue ID. Native list/get
remain project-free authorized reads during a project-identity hold. OpenClaw
get and explicitly target/source-filtered list also remain project-free; its
default inbox still requires the current caller workspace's identity.

A safe request-transport session wins over a reported session, with provenance
`transport` or `reported`. Transport provenance is not authenticated host-session
ownership. Session values may be UUIDs or opaque host conversation IDs, at most
256 UTF-8 bytes, without surrounding whitespace, control/format characters or a
nil UUID. Missing or unsafe optional attribution stays unknown instead of
blocking an otherwise authorized write. Optional `client` recognizes exactly
`omp`, `openclaw`, `claude-code`, `codex`, `cursor`, `gemini-cli`, `human`;
unknown labels stay unknown. Client and agent labels are independent and never
derived from an auth/browser session, keycard, project, lane or connection tag.

Issue JSON exposes `created_by_session`, `created_by_session_source`,
`source_client`, `target_session`. Comments retain `author_session`,
`author_session_source`, `author_client`; lifecycle events retain `session_id`,
`session_source`, `client` beside their own ID, issue ID, status, author project,
author agent and creation timestamp. These are attribution, not private session
content or new session-reader authority.

`target_session` is an explicit recipient reference, not an assignee, delivery
receipt, liveness claim or permission. Create omission leaves it unknown; update
omission preserves it; empty string/null clears it under creator/operator field
authority. Invalid recipient input is refused rather than silently clearing.

List filters are exact full session IDs: `source_session` matches only
`created_by_session`; `target_session` matches only the recipient;
`participant_session` matches creator **OR** comment author **OR** lifecycle-event
author. Each is ANDed with all other supplied filters before count/pagination,
not evaluated on the returned page. None implicitly selects the current
conversation. `client` is provenance, **not a list filter**. HTTP session query
selectors must occur once and be nonempty, bounded, valid identifiers; invalid,
duplicate or empty selectors return 400 instead of broadening the read.

### Bounded lifecycle history

Issue detail accepts optional `history_limit` (default 50, range 1..100) and
`history_after_id` (default 0, nonnegative exclusive event-ID cursor). HTTP
parameters must each occur once; invalid values return 400. Events are ordered
by `id ASC`, selecting `id > history_after_id` with one-row lookahead. The HTTP
detail envelope includes `history_events` (always an array), `history_limit`,
`history_has_more` and `history_next_after_id`. When more rows exist, continue
using the last returned event ID in `history_next_after_id`; otherwise that
field is null. Native MCP/OpenClaw detail output exposes explicit continuation;
the client does not silently fetch every page.

Each page requires the issue's ordinary read authorization. The cursor is a row
position, not a capability or snapshot: later appends can appear on later pages.
Comments are separate and retain their existing full-read behavior; the lifecycle
limit is not a promise that the entire detail response is bounded to 100 records.
No new async job, background completion or private session-history API is implied.

## Rules, Queue and collection operations

### Rules (4 distinct frontend-consumed operations)

[handlers_rules.go](../../internal/worker/handlers_rules.go) owns
`behavioralRuleRequest`, `behavioralRuleEnabledRequest` and selection-operation
responses; the store owns retained versions and priority order.

- `GET /api/rules`: optional project; `all=true` explicitly requests ListAll.
  Otherwise List uses the supplied project/global behavior. Positive `limit`
  defaults to 50; malformed/nonpositive is 400. The response is a **bare array**
  of BehavioralRule, not `{items,total}` and not the separate web client's TS
  expectation if that expectation drifts.
- `POST /api/rules`: ordinary creation requires content; pointer fields project,
  priority, edited_by preserve omission semantics. Returns the created rule with
  201. `action` instead dispatches the existing selection-operation mode.
- `PATCH /api/rules/{id}`: optional pointer content/project/priority/edited_by;
  provided empty content is 400. Returns the saved rule. No generic If-Match or
  CAS guarantee is advertised for this direct route.
- `DELETE /api/rules/{id}` returns `{deleted:id}`, not 204.

`PATCH /api/rules/{id}/enabled` is also registered: required boolean `enabled`
and optional edited_by, returning the rule; it is not called by the two current
frontend seams in the census. Current console selection actions (`enable`,
`disable`, `delete`, `update`, `reorder`) use POST `/api/rules` instead of assuming
direct-route semantics apply. Their body includes the selected projection and,
for reorder, rule_id/expected_version pairs. Global and project rules are not
interchangeable projections.

### Queue (2 frontend-consumed operations)

[handlers_candidates.go](../../internal/worker/handlers_candidates.go) owns
candidate review packet/list DTOs; [handlers_candidate_review.go](../../internal/worker/handlers_candidate_review.go)
owns Queue selection operations. `GET /api/memory/candidates` takes project,
status (default `pending`) and positive limit (default 20, valid 1..100; larger is
400, not clamped). Response: `{candidates:[candidateReviewItem],count,project,status,limit}`;
count is returned items, not a paginated total. This is not a Memory page. The
console reads the runtime flag before loading its queue; a source-level route
registration does not prove every provider/store is configured.

`POST /api/memory/candidates/operations` accepts request_id, action, selection
and optional reason, with matching `X-Engram-Request-ID`. Promote/reject use the
server-selected candidate IDs. `operation_state` can be `partial`; per-item
outcome and candidate_status, when supplied, are the evidence. Do not silently
turn partial promotion into all-or-nothing success. Single-candidate GET and
promote/reject/supersede POST routes plus review-metrics/queue/packet preview/apply
also exist; their exact DTOs remain in `handlers_candidates.go` and
[handlers_review_loop.go](../../internal/worker/handlers_review_loop.go).

### Shared selection (3 frontend-consumed operations)

[handlers_operator_collections.go](../../internal/worker/handlers_operator_collections.go)
provides POST `/api/collections/selection`, `/selection/current`,
`/selection/page`. `operatorCollectionSnapshotRequest`,
`operatorCollectionCurrentRequest`, `operatorCollectionPageRequest` and their
response DTOs are the exact wire owner. This shared adapter **selects, not
authorizes mutations**; domain handlers retain their own guards.

The common kinds are `none`, `explicit`, `page`, `frozen_filter`. Targets are
`{id:string,expected_version?:integer}`; use native IDs as strings in this
protocol. The server assigns selection_version, optional selection_token,
filter_fingerprint, target_count, expiry and reconfirmation fields. Current
bindings include browser subject/session, domain and effective context/filter;
a token from another session/domain/project is not reusable authority.
The store caps selections at **1,000** targets and pages at **200**. Frozen
membership/expiry is server-owned; maximum TTL is one hour. A cursor is opaque,
not a client offset or a database snapshot promise.

For mutation, send the current selection's kind/version and token when issued,
plus the domain's exact action fields. Where a body request_id exists it must
match `X-Engram-Request-ID`. Scope/filter/grant/collection change or expiry can
set `reconfirmation_required`; refresh and reconfirm before sending a new intent.
Code, Issues, Memory, Rules, Queue and Documents have **different** operation
DTOs/results. Memory/document domain-local `/selection/current` and
`/selection/page` do not become aliases of the shared adapter.

There is no repository-wide “same request ID means exactly once” contract.
Domain transactions recheck current authority and expected versions; issue bulk
mutation locks/rechecks the entire selected set and rolls back on a failed
recheck, while Queue explicitly reports partial item outcomes. Consult the named
operation owner before retrying an unknown outcome.

Minimal Rules selection request (requires an admitted browser and a fresh
operation correlation header):

```http
POST /api/collections/selection
Content-Type: application/json
X-Engram-Request-ID: example-selection-request
```

```json
{"domain":"rules","filter":{"scope":"global"},"selection":{"kind":"explicit","targets":[{"id":"1","expected_version":1}]}}
```

IDs, versions and issued refs must come from the current authorized reader; this
synthetic example is not permission to act on record 1.

## Vault (5 frontend-consumed operations)

[handlers_vault.go](../../internal/worker/handlers_vault.go) owns all five shapes.
The console and separate web client retain the exact metadata binding through
[projectCatalog.ts](../../ui/src/utils/projectCatalog.ts); a visible name alone
is not a unique secret selector.

| Operation | Shape and scope |
| --- | --- |
| `GET /api/vault/credentials` | Bare metadata array, never decrypted values. Entries include id/name/scope, `project_id` UUID or null, historical project/origin and optional unresolved_reference/binding_error. A null project_id is not automatically global: check scope. |
| `GET /api/vault/credentials/{name}` | Exact URL-encoded name and either canonical project, explicit `scope=global` with no project, or exact retained `id` plus `unresolved_reference` with neither project nor scope. Returns decrypted value plus binding metadata/current_state. Retained reads revalidate complete membership; stale/wrong binding is not repaired by a same-name fallback. |
| `POST /api/vault/credentials` | `storeCredentialRequest`: nonempty name/value, scope default project (`project|global`), canonical project required for project scope, optional tags. 201 `{id,name,scope,message,current_state:{id,name,scope,project_id}}`; value is not echoed as a receipt. |
| `DELETE /api/vault/credentials/{name}` | Exact name and project or explicit global scope. `{deleted:true,name,current_state:{name,scope,project_id,deleted:true}}`. Retained unresolved credentials are read-only; their reveal selector is not a delete selector. |
| `GET /api/vault/status` | `{key_configured,key_source,fingerprint,credential_count,backup_reminder,mismatch_warning?,mismatch_count?}`. A failed count lookup can leave zero in this current response; do not infer an empty store from that field alone. Key presence/fingerprint is not decryptability of every row. |

An encryption-key mismatch is 409; absent credential 404; malformed/ambiguous
selector 400; denied retained access 403; unavailable store 503. Native
credential-read policy remains in the handler. Reveal returns a real secret:
never cache it in logs, query strings, screenshots, analytics or ordinary
persistent UI state. After mutation, re-read exact metadata/ID, not another
same-name secret. Key rotation endpoints shown by design-only components are not
registered routes.

## Operator Code (18 frontend-consumed operations)

Exact request/response and validation owners:
[OperatorCodeHTTPAdapter](../../internal/worker/handlers_operator_code.go),
[UCI context](../../internal/uci/context.go),
[query response](../../internal/uci/query_response.go),
[versioned reads](../../internal/uci/versioned_read.go),
[index intents](../../internal/uci/index_intent.go), and the maintained
[console parser](../../apps/operator-console/composables/useOperatorCode.ts).
These are not the historical memory graph endpoints.

### Source, Checkout, View and refs

- **Source** identifies the registered repository/source, not a display name or
  a canonical project UUID. **Checkout** identifies an independently owned
  working copy/incarnation. **View** is an immutable published indexed snapshot.
- The authorized ContextRef binds Source, Checkout, View, analysis profile and
  generation (and Space where present). Publication of a newer View does not
  silently change a pinned reader. Labels and working-copy paths are display
  metadata, not permission or context selectors.
- `source_ref`, `checkout_ref`, `view_ref` are server-derived browser references.
  `selection_ref` is a short-lived server-issued chooser selection bound to the
  exact caller/tuple; copy it, do not derive it from IDs. The default chooser
  lifetime is 15 minutes.
- Catalog rows with `project_id:null` and `unresolved_reference` are retained
  provenance, not pin/read/index grants. A registered Checkout without a View
  can offer a distinct first-index target; that does not authorize queries.
- MCP `context_handle` is client-local and opaque. It is not an HTTP tab binding,
  durable global ID, project selector, grant or cross-session replay token.

### Browser binding, catalog and grants

Every Code operation requires `X-Engram-Request-ID`; JSON bodies use the exact
closed request DTO, a 32 KiB limit and no trailing JSON. Under authenticated
browser mode, the adapter derives the private session, checks grants, document
proof and exact context. Worker/master credentials do not acquire that binding
merely by carrying an admin label. The implemented auth-disabled branch uses its
separate local Code document binding; it does not manufacture a DB user or grant
access to `/api/access`.

| Operation | Request / result |
| --- | --- |
| `POST /api/code/tabs/handshake` | `operatorCodeHandshakeRequest`: document_nonce; optional copied_tab_binding_id/copied_resume_nonce pair and ambiguous. Returns transition `{state,tab_binding_id?,document_proof?,resume_nonce?,reload_token?}`. |
| `POST /api/code/tabs/resume` | tab_binding_id, resume_nonce, reload_token, document_nonce. Resume/collision/reload-pending is a state transition, not a forced takeover. |
| `PUT /api/code/tabs/{tab_binding_id}/lease` | JSON document_proof; bodyless 204. Lease renewal is not a read grant. |
| `DELETE /api/code/tabs/{tab_binding_id}` | JSON document_proof; bodyless 204. Close only the caller's binding. |
| `PUT /api/code/tabs/{tab_binding_id}/context` | document_proof and exact catalog selection_ref; bodyless 204 on pin. No raw project/path/context tuple substitute. |
| `POST /api/code/contexts` | tab_binding_id/document_proof; optional **query** project (one canonical UUID). `{contexts:[operatorCodeCatalogEntry]}` with display labels, refs, indexed_snapshot and optional first-index selection. Not an arbitrary filesystem browser. |
| `GET /api/code/grants/choices` | No body; optional target_next_ref for target continuation. `{choices:[{choice_ref,repository,working_copy}],targets:[{target_ref,label}],target_next_ref?}`. |
| `GET /api/code/grants` | No body; optional next_ref. `{grants:[{grant_ref,state,expires_at,repository,working_copy,reader}],next_ref?}`; owner inventory uses pages of 20. Omitted continuation means terminal, not permission to invent a page offset. |
| `POST /api/code/grants` | choice_ref, target_ref, optional expires_at; returns grant_ref/state/expiry. Server derives owner and target identities. |
| `POST /api/code/grants/{grant_ref}/revoke` | No JSON/body required. Exact issued owner grant, reauthorized server-side. Returned grant state is distinct from a refreshed inventory. |

`PATCH /api/code/grants/choices/{choice_ref}` is additionally registered for
`{working_copy}` display-label edits; it is not used by the current chooser.
[CodeGrantChooser.vue](../../apps/operator-console/components/CodeGrantChooser.vue)
shows the owner/target/inventory protocol. Request correlation is not a browser
credential. Binding state `collision`, `ambiguous` or `reload_pending` must be
handled as reported, not collapsed into ready.

### Queries, graph and stored source

These five POSTs include `operatorCodeProofRequest`
`{tab_binding_id,document_proof}`. The selected immutable context is already pinned;
a browser does not submit a replacement Source/Checkout/View tuple to a query.

| Operation | Additional fields / limits / result |
| --- | --- |
| `/api/code/status` | No additional selector. `operatorCodeStatusResponse`: total_chunks, embedded_chunks, embedding state, optional freshness. Chunk counts are not proof of full semantic/vector coverage. |
| `/api/code/structure` | path_prefix, optional limit (default 10, 1..50) and continuation; query bound to the current pin/filter. Returns UCI QueryResponse. |
| `/api/code/search` | query, path_prefix, languages, optional limit and continuation. Default limit 10, max 50; search text max 16 KiB. Returns UCI QueryResponse; degraded lexical results are not fabricated vector success. |
| `/api/code/graph` | action, typed target, optional destination/direction/relations/evidence_kinds, max_depth/max_visited/max_nodes/max_edges/deadline_ms/continuation. Defaults 4/64/32/64/30,000 ms; bounds depth 0..8, visited 1..5,000, nodes 1..200, edges 1..400, deadline 1..30,000 ms. Return includes validated graph/navigation descriptors. |
| `/api/code/source` | entity_key, membership_id, span, content_digest; optional indexed_span, reference_site_id, verify_working_copy, max_bytes. Default/max 8,192 bytes. Reads stored bytes from the selected View, not live disk. Response remains UCI QueryResponse. |

A citation's `membership_id` is its exact temporal file membership. Its
`content_digest` is bare lowercase SHA-256, not a guessed path hash. Spans use
zero-based half-open byte boundaries and one-based line coordinates. For a large
citation, preserve its original span as indexed_span and choose a contained byte
window, retaining the original line coordinates. Preserve entity/ref,
membership_id and digest; continue at the returned byte_end on a UTF-8 boundary.
The indexed-span mode cannot be combined with reference_site_id. A working-copy
verification request returns freshness metadata, never replacement disk bytes.

UCI QueryResponse has `schema:"engram.code-query/1"`, status, conditional
contexts/freshness/retrieval/coverage/items/graph/truncated/warnings/continuation,
`exposure` (nullable) and optional typed error. The maintained validator in
`query_response.go`, not a permissive frontend union, owns required/forbidden
combinations. Statuses include `ok`, `empty`, `partial`, `stale`, `unavailable`,
`context_required`, `forbidden`. Do not turn missing/null continuation or coverage
into a successful complete traversal. Graph `stop_reason` distinguishes complete,
depth_cap, node_cap, deadline and coverage_gap. Retrieval mode and degradation
reasons describe the actual query path. “Observed current” metadata is not proof
that a future working copy remains unchanged.

Malformed/closed-shape requests return bodyless 400; denied context/grant 403;
stale/context/idempotency conflict 409; unavailable backend/release validation
503. Global authentication/readiness can respond before this adapter. There is
no universal `{error:...}` body on these failures. Exact refs and continuations
must be retained only within their issuing authorized context; a retry with a
new tuple or altered request ID is a different intent.

### Async index intents (3 frontend-consumed operations)

- `POST /api/code/index-intents`: proof, request_ref, kind, and optional first-index
  target `{selection_ref}`. 202 is an acknowledgement, not a published View.
- `GET /api/code/index-intents/{intent_ref}`: uses exact
  `X-Engram-Tab-Binding-ID` and `X-Engram-Document-Proof` headers plus request
  correlation, no JSON body. It returns intent_ref/state/attempt/retryable/times
  and optional result `{view_ref,generation}`.
- `POST /api/code/index-intents/{intent_ref}/retry`: the current proof and exact
  issued intent; retry only when the server's state says retryable. Do not create
  a replacement intent merely because polling timed out.

The server reauthorizes the target, and the owning daemon polls/executes work.
Offline producer, queued/running work, terminal failure and ready View are
separate states; a status deadline is not cancellation or completion. Use
`IndexIntentState` in `index_intent.go` and the exact returned status, not an
invented UI state machine. Durable states are `submitted`, `queued`,
`acknowledged`, `running`, `completed`, `unavailable`, `failed`; kinds are
`reindex|reconcile`. These enums do not replace server retry authorization.
A successful index result does not automatically pin the new View: refresh the
catalog and explicitly select it.

## Runtime, search, events and other HTTP surfaces

### Frontend-consumed runtime/search operations (20 in the combined census)

| Methods and paths | Exact maintained owner / meaningful payload boundary |
| --- | --- |
| `GET /api/stats`, `/api/stats/retrieval`, `/api/stats/vnext` | [handlers_data.go](../../internal/worker/handlers_data.go) and [handlers_stats.go](../../internal/worker/handlers_stats.go). Different stats DTOs; project and time-range filters are endpoint-specific. Native counters/ratios, not authorization or an inferred memory count. |
| `GET /api/selfcheck`, `/api/update/check`, `/api/update/status` | [handlers_update.go](../../internal/worker/handlers_update.go): SelfCheckResponse `{overall,version,uptime,components:[{name,status,message?}]}`; update check can return 200 `{available:false,current_version,error,rate_limited}` on upstream failure; status is the updater's current job snapshot. |
| `POST /api/restart`, `/api/update/restart` | Same owner. Explicit process effects, not config validation or generic “apply settings.” Restart-after-update is 400 when no update was applied. A disconnect/timeout does not prove rollback. |
| `GET /api/ready` | [handlers.go](../../internal/worker/handlers.go): readiness, not merely a reachable process or valid login. |
| `GET /api/config`, `PATCH /api/config` | [handlers_system.go](../../internal/worker/handlers_system.go). GET context/memory/storage/features/lifecycle groups. PATCH accepts **only** features.enforce_source_project; unknown/trailing JSON, telemetry_enabled and empty changes are 400; environment ownership is 409. Admin required. Success `{success,applied,audit_logged,changed,restart_required,restart_required_fields,config}`; false audit_logged does not undo a saved change. Save can succeed before reload failure: read back before retry. |
| `GET /api/flags` | `runtimeFlagsResponse` in the same owner: flags/items/summary/read_only/apply/config. Built-in runtime, env and config flags are different. Read-only GET is not a toggle endpoint. |
| `GET /api/migrations` | Same handler owner; migration bookkeeping DTO returned by `GetMigrationState`, not a migration/apply endpoint. Store absent 503; query failure 500. |
| `GET /api/vector/metrics` | [handlers_stats.go](../../internal/worker/handlers_stats.go); real vector subsystem metrics, not proof that every chunk has an embedding. |
| `GET /api/models`, `/api/model-health` | [model registry in handlers_data.go](../../internal/worker/handlers_data.go) and [modelHealthResponse](../../internal/worker/handlers_model_health.go). Health rows expose configured/standby/degraded evidence and secret-presence signals, not credentials or proof of a live successful inference. |
| `GET /api/context/search` | [handlers_context.go](../../internal/worker/handlers_context.go); see the method-specific Context contract below. |
| `GET /api/search/recent`, `/api/search/analytics` | [handlers_data.go](../../internal/worker/handlers_data.go), bounded recent-query/analytics DTOs and endpoint-specific limit/project/since parsing; not a memory list envelope. |
| `GET /api/maintenance/stats` | Requested by the separate web client, but **not registered** in the released router. An unknown GET can produce SPA HTML; this is a client/route gap, not a working API to reproduce in a replacement UI. |

The 20-count projection is 16 runtime operations (including the unsupported web
request) plus four search operations; stats/retrieval is counted in the search
consumer family once, not twice. It does not include every server-only system
route.

### Context and hook-facing HTTP contract

[handlers_context.go](../../internal/worker/handlers_context.go) owns the real
DTOs/annotations, with [file context](../../internal/worker/handlers_context_file.go)
and [session-start delivery](../../internal/worker/session_start_delivery.go).
These routes are HTTP clients' context operations, not HTTP MCP.

- GET `/api/context/inject`, `/search`, `/session-start` read query parameters
  only; GET JSON bodies are ignored. Project must be a canonical UUID; search
  additionally requires nonempty query.
- POST `/api/context/inject` requires JSON `contextInjectRequest`, including
  project; query project/agent_id/session_id/cwd/files_being_edited are not
  fallbacks. `format=compact` remains query-only.
- POST `/api/context/search` allows query project/query as fallback. Nonempty
  JSON fields override, while body session_id overrides even with an empty
  string. Invalid/absent body is tolerated, retaining query values. Limit is
  query-only, default 50/max 200; obs_type is body-only.
- POST `/api/context/session-start` requires valid JSON (use `{}` to retain query
  values). Project/session ID are trimmed and only nonempty body values override.
  Memory/issue limits are body-only; zero delegates to server defaults. Responses
  are the maintained session-start packet, not the search envelope.
- Source session IDs are audit attribution, not authentication; client cwd is
  ignored by these context readers. Retrieval privacy remains server-owned.
- Search returns its observation-shaped response, not a Memory DTO page. Hybrid
  vectors/FTS/literal content/tag matches may use configured reranking, without
  query expansion. Unavailable embeddings degrade to text; memory-store failure
  returns 500, never a false successful empty search.

Minimal query-only request and valid empty-result shape:

```http
GET /api/context/search?project=11111111-1111-4111-8111-111111111111&query=synthetic&limit=1
```

The actual response keys are owned by the search handler and Swagger; do not
reuse the document/list empty envelope here. POST search's cross-location
project/query requirement cannot be represented by making both locations
individually required in Swagger 2.

### Other registered machine/API families

These owners complete the route inventory beyond the frontend subset. They are
not automatically browser workflows, and named request/response DTOs remain the
schema of record rather than a hand-maintained duplicate here.

| Registered surface | Owner / boundary |
| --- | --- |
| `/api/sessions/init`, `/{id}/init`, `/{id}/summarize`, `/list`, base GET `/api/sessions`, `/subagent-complete`, `/index`, `/check` | [handlers_sessions.go](../../internal/worker/handlers_sessions.go). Native `SessionInitRequest/Response`, `SessionStartRequest`, `SummarizeRequest`, `CheckSessionsRequest`; camelCase `claudeSessionId` is intentional. Init/summarize can be bodyless success. List is `{sessions,total,limit,offset}`; GET base requires claudeSessionId and returns one SDKSession. These SDK/source sessions are not browser management handles. The two GETs are the eight-operation Projects/session consumer family above. |
| `/api/hooks/session-end`, `/correction`, `/code-extraction`, `/segment-check`; conditional `/ambient-candidates`; GET/POST `/api/context/reinject` | [handlers_hooks.go](../../internal/worker/handlers_hooks.go), [correction](../../internal/worker/handlers_correction.go), [extraction](../../internal/worker/handlers_extraction.go), [segment](../../internal/worker/handlers_segment.go), [ambient](../../internal/worker/handlers_ambient.go), [reinject](../../internal/worker/handlers_reinject.go). Internal lifecycle/adaptive adapters with their own DTOs, not a general frontend command API. |
| POST `/api/events/ingest`, `/api/memory/triggers`, `/api/decisions/search`; GET `/api/context/count`, `/files`, `/by-file` | [ingest](../../internal/worker/handlers_ingest.go), [triggers](../../internal/worker/handlers_triggers.go), Context owners above. DTO/limit/provenance rules are operation-specific. |
| `/api/state/session/{sessionID}`, `/api/state/project/{project}`, `/api/state/resume` | [handlers_state.go](../../internal/worker/handlers_state.go), state-plane readers; native state shape is not a browser-auth session or an issue continuation. |
| `/api/experience-history`, `/{experienceID}`; `/api/temporal-truth` and POST `/refresh` | [history](../../internal/worker/handlers_experience_history.go), [temporal truth](../../internal/worker/handlers_temporal_truth.go). Bounded, provenance-first readers; archive trigger and provider/feature requirements remain. A registered temporal route can still be unavailable. |
| `/api/graph/nodes`, `/edges`, `/traverse`, `/find-path` | [handlers_graph.go](../../internal/worker/handlers_graph.go). Retained memory graph, not UCI code graph. Its safe empty-on-hidden behavior must not be inferred from Code's bodyless 403. |
| `/api/observations`, `/api/types`, `/api/telemetry/similarity`, POST `/api/analytics/search-misses` | [handlers_data.go](../../internal/worker/handlers_data.go), [handlers_stats.go](../../internal/worker/handlers_stats.go). Observation/analytics adapters, not replacement memory mutation endpoints. |
| `/api/projects/bootstrap`, `/resolve`, `/unresolved/{reference}/content`, `/history`, `/code`; DELETE `/api/projects/{id}` | [bootstrap](../../internal/worker/handlers_project_bootstrap.go), [logical readers](../../internal/worker/handlers_logical_project_readers.go), [projects](../../internal/worker/handlers_projects.go). Registration, resolution, retained provenance and retirement are different operations. See canonical readers below. |
| `/api/projects/consolidation/census`, `/preflight`, `/backup`, `/verify-backup`, `/dry-run`, `/apply` | [handlers_project_consolidation.go](../../internal/worker/handlers_project_consolidation.go). Explicit admin recovery protocol, with typed census/plan/backup/rehearsal receipts, multipart backup verification, distinct limits and authorized apply. Dry-run performs transactional work and rollback; it is not a free GET or automatic migration. |
| POST `/api/backfill/session`, GET `/api/backfill/status`, POST `/api/import/feedback`, `/api/instincts/import` | [backfill](../../internal/worker/handlers_backfill.go), [instincts](../../internal/worker/handlers_instincts.go). Historical import/background work, not normal client session creation. |
| `/api/health`, `/health`, `/api/version`, `/api/mcp/health`, `/api/vectors/health`, `/api/stats/v7/{subsystems,substrate,product}`, `/v1/models`, POST `/api/update/apply` | [handlers.go](../../internal/worker/handlers.go), [v7 stats](../../internal/worker/handlers_stats_v7.go), [data](../../internal/worker/handlers_data.go), [updates](../../internal/worker/handlers_update.go), their registered delegated metrics owners in service.go. Liveness, readiness, version, configured registry and actual successful tool call are separate proofs. |
| POST `/api/sessions/{id}/propagate-outcome` and `/outcome` | [retirement handler](../../internal/worker/handlers_outcome_retirement.go): **410 Gone**, code `outcome_propagation_retired`, explicit-MCP migration message. Do not retry or restore the retired observation writer. |
| GET `/api/books/{id}/status` | [handlers_books.go](../../internal/worker/handlers_books.go): positive retained job ID; 200 `bookJobResponse` with id/status/source_ref/error?/times?/documents_path_prefix/documents_link. Missing job is 404, unavailable store 503. This retained status reader is not an upload/ingestion promise. |

The old `/api/hooks/session-start`, `/user-prompt`, `/pre-tool-use`,
`/pre-compact`, `/stop`, `/subagent-stop` and `/api/status` table was not the
registered released API and has been removed. Host hook events and server HTTP
routes are not a one-to-one naming convention.

### Streams and downloads

`GET /api/events` is the dashboard SSE bus owned by
[internal/worker/sse](../../internal/worker/sse), not an MCP SSE transport.
`GET /api/logs` is a separate [process-log reader](../../internal/worker/handlers_logs.go):
JSON **array** snapshot by default, `lines=100` default/max 10,000, minimum level
and case-insensitive query filters. Invalid/nonpositive lines retains default.
The projected safe filter runs before the latest-lines limit; snapshots remain
chronological. `follow=true` switches to `text/event-stream`, starts with
`: connected`, then sends `data: <LogEntry JSON>` for new matching entries.
It does not replay a snapshot, promise durable event IDs or expose a
Last-Event-ID resume contract. Unsubscribe on close; a broken connection is not
an empty completed stream. Log reads preserve the scoped browser-session
redaction described below, not a general guarantee that arbitrary logs contain
no secrets. Document export is an attachment download with its own artifact
binding, not an SSE/JSON read variant.

## MCP contract

The host launches local `engram` over **JSON-RPC 2.0 stdio**. Server-side HTTP
MCP/SSE/Streamable HTTP and stdio-to-SSE bridging are not registered transports.
The daemon talks to the server over gRPC; an `ENGRAM_URL` server origin is not an
MCP HTTP endpoint.

Maintained schema/dispatch owners:

| Surface | Exact owner |
| --- | --- |
| Primary registration, `tools/list` expansion, routing, read-only allowlist and errors | [internal/mcp/server.go](../../internal/mcp/server.go), `primaryTools`, `handleToolsList`, `callTool` |
| `recall`, `recall_memory`, `store`, `feedback` | [tools_recall.go](../../internal/mcp/tools_recall.go), [tools_memory.go](../../internal/mcp/tools_memory.go), [tools_store_consolidated.go](../../internal/mcp/tools_store_consolidated.go), [tools_feedback.go](../../internal/mcp/tools_feedback.go) |
| Issues/docs/settings/vault | [tools_issues.go](../../internal/mcp/tools_issues.go), [tools_docs_consolidated.go](../../internal/mcp/tools_docs_consolidated.go), [tools_settings.go](../../internal/mcp/tools_settings.go), [tools_vault_consolidated.go](../../internal/mcp/tools_vault_consolidated.go) |
| `codebase_context`, `codebase_search`, `codebase_status`, `codebase_read`, `codebase_graph` | [tools_code_context.go](../../internal/mcp/tools_code_context.go), [tools_code_intel.go](../../internal/mcp/tools_code_intel.go), [tools_code_read.go](../../internal/mcp/tools_code_read.go), [tools_code_graph.go](../../internal/mcp/tools_code_graph.go) |
| Local transport/context resolution and daemon-side index action | [internal/module](../../internal/module), [cmd/engram](../../cmd/engram), [Engram native proxy](../../internal/handlers/engramcore/tools.go). The local [project_context tool](../../internal/handlers/engramcore/project_context.go) is registered before canonical project resolution and is not proxied as a backend mutation. |
| Principal memory/state/continuity/lifecycle/rules/governance/candidates/history | Their named `tools_*.go` implementations in [internal/mcp](../../internal/mcp); the live InputSchema is authoritative for action enums, required and additionalProperties rules. |

The primary entry points are recall, store, feedback, vault, settings, docs, admin,
issues and check_system_health. Discover the live set, not a historical fixed
expanded count: `tools/list` with `include_all:true` (or supported `cursor:"all"`)
returns working provider-backed expansion; advertisement and handler authorization
are different checks. Deferred significance/ambient/directive/meta-memory code
or retained schemas are not proof of live default capabilities.

`tools/call` uses `{name,arguments}` and returns MCP content (often JSON encoded
inside a text item), not an HTTP page envelope. JSON-RPC error and a tool result
with `isError` are distinct; do not parse a text error as a successful object or
infer an empty memory set from transport failure. `CallToolResponse.is_error`
retains this distinction across gRPC. Read-only action allowlisting and admin
requirements apply before dispatch even when a tool is advertised.

For hybrid recall, `tier_filter` means retrieval layers `tier0_exact`,
`tier1_fts`, `tier1_vector`, `tier2_graph`; omitted/empty means all layers. Lifecycle
memory tiers are not synonyms and return `invalid_tier_filter:`. Lifecycle
exact-ID readers apply caller/project/principal/domain/workstation visibility;
hidden and absent IDs have the same tool error. Lifecycle mutation authorization
and history/tier updates remain transactional; SDK session metadata is not an
invented read selector. A content-free sleep status is not a memory payload.

Code context register/resolve/list/select, client-local handles, exact tuple and
no-View checkout selection are defined by `codebaseContextTool` and its DTOs.
Registration uses the actual owned Git worktree; a label is not a locator or an
authorization grant. `codebase_read` requires ref/membership_id/span/content_digest
and uses the same 8,192-byte indexed-window rules as the HTTP reader, with a
closed schema. `project` on code tools is compatibility evidence only, not
context-selection authority. HTTP `selection_ref` and MCP `context_handle` must
not be substituted for each other.

### Supported recovery actions

Owners: [logical recovery](../../internal/projectidentity/logical_recovery.go),
[choices/select](../../internal/projectidentity/project_context.go),
[caller-scoped binding](../../internal/projectidentity/project_context_binding.go)
and [recovery HTTP trust policy](../../internal/projectidentity/recovery_http.go).
These are current-source operations, not claims about a loaded installation.

1. On `CONTEXT_REQUIRED` or `CONTEXT_MISMATCH`, first use
   `codebase_context(action="list")` for project-free inspection. Select only the
   intended authorized Source/Checkout from that fresh response using its
   `context_handle`, verify `action="resolve"`, then retry the original query.
   Code context selection is distinct from logical-project recovery below.
2. Ordinary native resolution automatically recovers a strict historical
   `id`/`name` marker only when the configured server's authorized
   `GET /api/projects/resolve?legacy_id=<encoded-exact-marker-id>` verifies one
   existing canonical mapping. It checks local identity again before using the
   mapping; changed state or a conflicting pinned identity refuses. Recovery
   does not create a project/UUID/alias or rewrite the marker/common cache.
3. If the intended existing project needs an explicit choice, call local native
   `project_context(action="choices")`. The result is
   `{choices:[{project_id,name,repository_qualifier?,selection_ref}]}`; it reads
   the fresh authorized catalog without selecting or writing a binding. Review
   names, full IDs and qualifiers. Never choose first/latest, by name alone or
   merely because one result exists. Preserve ambiguity and ask for the intended
   supported choice rather than guessing or inventing a replacement project.
4. Call `project_context(action="select", selection_ref="<exact-fresh-ref>")`
   in the same actual caller/root/server scope. Native select returns
   `{project_id,name}`. It rechecks the catalog and local state, then persists
   only that caller-scoped context sidecar. Scope includes the actual caller
   root, installation namespace, server and credential fingerprint; the tool
   accepts no root, project, credential or session overrides. Existing pinned
   identity, marker/common cache, project data, permissions and the current
   session are not replaced. A stale reference requires fresh choices, not a
   forged reference or a name/UUID substituted for `selection_ref`.
   A nonempty explicit relative root and its lexical absolute form identify the
   same caller/root/server scope. A missing or invalid caller root is refused;
   it is never replaced with the daemon's working directory. Catalog privacy
   checks reject the full absolute caller root and credential, not a short
   relative spelling that can occur in a legitimate name or qualifier.
5. After confirmed recovery/selection, retry the original operation with its
   original arguments, target project, actor, session and client. Automatic
   verified mapping runs inside ordinary resolution; explicit selection does
   not itself replay a held tool call. A timeout/disconnect after a mutation
   leaves its outcome unknown: establish the original result before replaying.
   Recovery is not evidence that the mutation was never sent or never committed.

When `choices` or `select` is unavailable, hold recovery until connectivity,
authorization or local storage is restored. Reconcile an unconfirmed selection
using the existing [bounded owner inspection](../operating-engram.md#context-recovery).
If the binding or outcome cannot be established safely, it remains unknown and
the operation stays held. Refresh stale choices only after access is restored and
any unknown selection outcome is reconciled; do not replay an unknown write.
Preserve the original operation, target, actor, session and client.

Caller-binding publication does not require or alter the old persistent `.lock`;
atomic no-overwrite preserves existing identity files, including marker and binding.
Ordinary `project resolve` can bootstrap and persist without an established binding.
`project resolve --check` does not prove a caller selection; neither is universal
read-only confirmation of an unknown selection.

OpenClaw exposes `engram_project_context` with the same `choices`/`select`
intent through its native-client adapter and the actual host caller workspace.
Its result formatting is host-specific, not the native MCP text/JSON envelope.
Do not substitute a visible terminal cwd, configured label or session ID for
missing caller-root metadata. Native issue list/get and explicitly scoped
OpenClaw reads remain usable during an identity hold; an error is not an empty
successful inbox. `PROJECT_IDENTITY_INVALID`, conflicts and unknown refusals
retain the original operation and useful files for bounded owner repair.

Recovery readers have a 15-second deadline. Legacy mapping and identity/binding
files have 4096-byte bounds; choices catalog is limited to 1 MiB and 4096 entries.
Malformed/duplicate fields, invalid catalog entries, changed identity and unsafe
identity files refuse rather than yield a partial successful selection. Selection
is bound to current state/catalog, not an advertised wall-clock lease; a saved
binding is revalidated against current authorization on ordinary use.

Both recovery GETs use the configured bearer credential on a fresh direct
transport without redirects. When `ENGRAM_TLS_CA` is set, its PEM roots are
required and TLS 1.2+ is forced even for an `http` configured URL; CA/load/TLS
failure has no plaintext fallback. Without that setting, HTTPS uses ordinary
verified TLS and HTTP remains supported. Do not weaken trust to recover identity.

`project resolve --root <actual-root> --json --check` remains optional read-only
owner diagnostics, not a mandatory user shell step. It does not perform ordinary
legacy mapping recovery or bind a project. Existing consolidation
census/preflight/backup/verification/dry-run/apply is a separate authorized
data-migration protocol, not a prerequisite to choosing an existing context.
Preserve its backup/conservation and explicit apply safeguards when an actual
data move is required; never ask the user to edit the database as recovery.

On stale/retired View, expired grant or code selection, reacquire that exact
catalog context and required authority. Index pending/producer offline is not
solved by selecting another checkout or inventing a successful response.
[Operating guide: context recovery](../operating-engram.md#context-recovery)
supplies the corresponding operator procedure; tool descriptions/errors retain
the essential actions for clients without a checkout-local guide.

## gRPC contract

[`proto/engram/v1/engram.proto`](../../proto/engram/v1/engram.proto) is the single
protobuf source; generated Go bindings are not an independent schema. The
released `EngramService` declares **21 RPCs**:

- MCP transport: CallTool, Initialize, Ping, NegotiateVersion.
- Canonical project/session delivery: SyncProjectState, ProjectEvents (server
  stream), GetSessionStartContext.
- Code producer protocol: CodeIndexNegotiate, CodeIndexUpload (client stream),
  BindCodeContext, PollCodeIndexIntents, UpdateCodeIndexIntent, BeginCodeIndex,
  StageCodeIndex (client stream), FinalizeCodeIndex.
- UCI queries/evidence: QueryCode, ExploreCode, RecordUCICompletion.
- Host advisor: Bind, Advise, Observe.

[internal/grpcserver/server.go](../../internal/grpcserver/server.go) owns service
registration, authentication interceptors, message limits and protocol mapping;
[uci_transport.go](../../internal/grpcserver/uci_transport.go),
[uci_context.go](../../internal/grpcserver/uci_context.go),
[uci_completion.go](../../internal/grpcserver/uci_completion.go) and
[host_advisor.go](../../internal/grpcserver/host_advisor.go) own their typed
admission rules. Bearer/principal/workstation metadata is verified before
service work. Proto fields containing project/session/actor data do not grant
authority. Preserve protobuf field numbers, enum values, oneof/result variants,
stream order and exact ContextRef; an HTTP JSON object is not a substitute.

Initialize returns advertised tool input schemas; CallTool carries name/arguments
and response text/is_error; optional correlation/exposure fields retain their
meaning. QueryCode/ExploreCode carry a versioned UCI response payload, whose
conditional status/freshness/coverage/exposure rules still apply. Completion must
authorize its parent exposure context; knowing exposure_ref is not permission to
append. Staging chunks is not publication: FinalizeCodeIndex owns the atomic
published View outcome. Streaming interruption/deadline is not a successful
receipt or permission to replay an upload as a different owner.

## Host hook interfaces

[hooks.json](../../plugin/engram/hooks/hooks.json) registers host events and invokes
the existing dispatcher; [plugin/engram/hooks](../../plugin/engram/hooks) owns
stdin validation, native-client invocation and host-specific stdout formatting.
The current event keys are SessionStart, UserPromptSubmit, SubagentStop,
PreToolUse (Edit/Write matcher), PreCompact, Stop and SessionEnd. Event names are
not server URL names and hooks do not all emit the same payload.

Host input carries the host's real session/root/transcript/tool metadata; there
is no universal required `{hook_type,project:"slug"}` JSON contract. Canonical
identity is resolved by the existing installed client, not accepted from a
made-up host label. Session-start context and later hook output use the host
adapter's envelope/presentation; diagnostic stderr must not corrupt stdio
JSON-RPC or the host's stdout protocol. A timeout/fail-open host continuation is
not proof that context was delivered or stored. No automatic tool-result memory
capture or retired outcome callback should be inferred from the existence of a
hook file.

Current OMP source registers a per-call adapter in
[engram-memory.mjs](../../plugin/engram/extensions/engram-memory.mjs): it supplies
the actual host session-manager UUID as `session_id` and `client:"omp"` only for
the uniquely matched MCP tool whose advertised schema carries
`x-engram-caller-context:"issues/v1"` and string attribution fields. Missing or
invalid host session metadata leaves the call unchanged; it does not fabricate
a project/lane/daemon ID or rewrite unrelated tools. OpenClaw's
[issues adapter](../../plugin/openclaw-engram/src/tools/engram-issues.ts) sends
its optional actual `ctx.sessionId` and `client:"openclaw"`, with `ctx.agentId`
kept separate. These source paths describe reported provenance, not verified
host ownership, new issue permissions or proof a running host loaded them.

## Current-source reconciliation

The current recovery/issue delta is implemented in corrective source
`3ad28a61256feb7012844aacc13197ff673b0c9d`; the released documentation contribution
was composed at integration input `4cc8ccbc2eac3a777e9487ed739e468f0790bf87`.
These immutable inputs distinguish the historical measurements from the current
contracts above; they are not a claim about a later documentation commit's HEAD.

- Logical-project automatic verified mapping, caller-bound choices/select,
  transport trust and resumption are owned by `internal/projectidentity` and the
  native/OpenClaw adapters linked above, not by a new HTTP MCP endpoint.
- Issue per-call attribution, recipient updates, exact session filters, RW
  lifecycle transitions and bounded lifecycle-event history are current source.
  POST `created_by_session` predates this correction; no historical host identity
  or verification evidence is inferred from its presence or a closed status.
- Issue lifecycle history and reported host conversation references do not
  change browser cookie/session management, grant private transcript access or
  introduce async session-history jobs. Existing session read owners and
  browser-handle protections remain separate contracts.
- The five accepted annotation-bearing Go files remain documentation-only
  additions; their generated Swagger outputs describe the annotated subset.
  Preserved **91/199** machine-operation and **53/110** frontend coverage are
  released-baseline measurements, not a recensus of the corrective candidate.

Source reconciliation and schema generation do not prove installation, host
activation, production/database/browser acceptance or publication. Those require
their own exact-artifact consumer evidence; no such delivery is claimed here.

## Canonical project and knowledge readers

Owners: [handlers_data.go](../../internal/worker/handlers_data.go),
[handlers_knowledge_readers.go](../../internal/worker/handlers_knowledge_readers.go),
[logical project readers](../../internal/worker/handlers_logical_project_readers.go),
[retained discovery](../../internal/worker/handlers_retained_record_query.go),
and the native stores they call. These detailed existing constraints are retained
here so a replacement client need not infer membership or privacy from UI code.

`GET /api/projects` returns `{items:[{project_id,name,repository_qualifier?}],unresolved:[{reference,name,reason,reader_kinds}]}`.
`items` contains live canonical `projects` rows, not session labels or legacy
selectors. `project_id` is the canonical UUID; `name` is a human label that falls
back to `Registered project` when missing or blank. The optional
`repository_qualifier` is a normalized repository locator. Duplicate labels remain
distinct UUIDs. Existing ordinary authenticated readers retain their server-wide project
scope. HAP project/legacy credentials remain forbidden by the existing route
policy. The catalog response is `private, no-store`, never publicly cached.

`unresolved` describes retained data owners without a canonical project assignment.
Each entry has an opaque `reference`, display `name`, `reason`, and `reader_kinds`
identifying retained `memory`, `document`, `issue` or `code` data. This metadata
neither verifies retained content nor grants reader access.

### Bounded retained-record discovery

`GET /api/projects?view=retained_records&attribution_scope=unresolved_history&kind=memory&q=retained&limit=50`
explicitly selects a separate historical projection. Without `view`, the full
catalog above is unchanged. New-mode parameters are closed and scalar: exactly
one `view=retained_records`, `attribution_scope=unresolved_history` and
`kind=memory|document|issue`; optional `q`, `limit`, `cursor`. Unknown, duplicate
or foreign project/reference/offset selectors are `400 INVALID_QUERY`.
`q` is valid UTF-8, at most 1024 bytes, trimmed at its edges and searched as an
escaped case-insensitive literal. `%`, `_` and backslashes are not a language.
Memory searches content; documents search path/content; issues search title/body.
`limit` defaults to 50 and accepts 1..200.

The response is `{view:"retained_records",attribution_scope:"unresolved_history",kind,items:[{reference,id,record}],next_cursor:string|null}`.
`items` is always an array. `record` retains the genuine native family payload,
adds `attribution_scope:"unresolved_history"` and omits `project_key`. The outer
positive ID equals the native record ID. Results are unique by family/native ID
and ordered by native ID DESC, not creation time or group. Document versions
remain separate IDs. The first valid unresolved witness supplies the association;
duplicate roles/groups do not duplicate a record. A separate canonical role does
not disqualify a valid unresolved role. Historical `project`, source/target and
origin fields are provenance, not a canonical owner or automatic-context grant.

There is no `total` or `has_more`. Saved membership-occurrence counts are not
visible-record counts. A cursor means remaining search **work**, not promised
visible results: short/empty pages with a cursor are intermediate. Null means
observed traversal exhaustion. No cross-request snapshot is promised.

Each request selects at most 200 native IDs plus one lookahead in keyset order,
then applies query/privacy within that window without materializing bodies.
One fixed-family membership set proposes at most 200 witnesses plus lookahead.
The shared native validator checks full typed keys, source-key/path/plan and
Vault HMAC references, native liveness, selector and exactly-one-match evidence.
Invalid witnesses are skipped; operational errors abort the entire page.
Validation uses bounded set batches, not per-record/group reads. Only the
consumed prefix advances: an unconfirmed ID and last witness resume before lower
IDs, so the valid witness after a 200-witness boundary is not lost. The sealed
cursor has constant-size prefix/pending-ID/last-witness state. Bodies for at most
the public limit of confirmed IDs are fetched once and restored to ID DESC.
These application bounds do not promise bounded PostgreSQL physical I/O or bytes.

Cursors use retained-specific Vault-derived AES-GCM, binding the normalized query,
limit and actual effective identity/visibility context, including the no-auth
realm. Tampered, different-caller or different-query cursors are refused with
`400 INVALID_CURSOR` before data work. There is no public-checksum, fallback-key,
code-feature or browser-only identity dependency. Vault, randomness and storage
failures return safe `503 RETAINED_RECORDS_UNAVAILABLE`, without partial items or
a false terminal cursor. Success and handler errors are `private, no-store`.
Existing TokenAuth/DB-ready/HAP route restrictions remain; memory read/history
privacy and manage checks remain. Document/issue authority is the existing
authenticated route plus exact validated membership, not a new per-row ACL.

Open the exact issued selection with
`GET /api/projects/unresolved/{reference}/content?attribution_scope=unresolved_history&kind=memory&id=123`;
use `/history` with the same reference/kind/positive ID. These closed readers
accept only the optional historical attribution value. Omitting scope preserves
their old unresolved meaning; omitting content ID preserves whole-group paging.
Responses identify `unresolved_history`; historical records/history omit
`project_key`. The full selected group still fails closed on a stale neighbor,
and a stale/foreign binding is never repaired through another group. Document
history keeps all validated group IDs and the selected historical path/project,
not just one selected version or another group's same path. Keep the search,
cursor, results and selection on return; clear open payload on stale/foreign/
denied/error responses. This HTTP contract is not installed-client/UI acceptance.

Swagger 2 cannot express two conditional 200 schemas on one route. Its ordinary
catalog schema remains intact; the generated `x-retained-records-response`
extension describes the explicitly selected alternate envelope. Conditional
required parameters and native payload variants are stated in route descriptions.


### Canonical memory and document readers

`GET /api/memories` and `GET /api/documents` require one exact existing live
canonical `project` ID and return `{items,total,offset,limit,has_more}`. No legacy
array/`documents`/page-length `count` shape remains. Defaults: `offset=0`,
`limit=50`; bounds: `offset>=0`, `1<=limit<=500`. `items` is always an array.
`total` counts the visible query set before pagination; hidden rows affect neither
count nor page. `has_more` compares the returned page with that visible total.

`query` is a trimmed, case-insensitive literal substring (valid UTF-8, maximum
1024 bytes, no control characters). SQL bind parameters and LIKE escaping keep
`%`, `_`, `\\` and SQL-looking text literal. Memory queries search content, after
the existing active/nondeleted/current-validity, workstation, principal, domain
and flag-controlled legacy privacy filters. Ordering remains
`created_at DESC, id DESC`; status and provenance fields keep their meaning.

Memory accepts repeated exact `filter` values: `all`, `active`, `flagged`,
`stale`, `low`, `chain`. For example, `filter=low&filter=chain` intersects the
selected predicates with **AND before count/page**, preserving the independently
toggleable Memory Lab chips. Omitted filters mean `all`; explicit `all` is neutral
with other selections. Valid duplicates are deduplicated. At most six incoming
values are accepted before deduplication; blank, unknown, differently cased or
CSV values are 400, including an invalid later value. No scalar-first fallback,
JSON/CSV alternative or page-local filtering exists. The existing active-only
eligible corpus is unchanged. Zero-valued `omitempty` confidence/citation metrics
remain unknown, not new ratings. `chain` matches exact tags or a supersession link.

Memory `epistemic_type` repeats exact existing values `fact`, `experience`,
`opinion`, `observation`; document `doc_type` repeats literal type values.
Selected types use **OR within the type set**, AND with every other query,
filter, status and canonical/privacy predicate before count/page. Omission is
unrestricted; one value is the scalar subset. Each parameter accepts at most
six raw values before deduplication and validates every value, including later
ones. Blank/control/malformed UTF-8 values are 400. Document values retain the
128-byte raw UTF-8 bound and Unicode whitespace trimming; comma-containing
types are literal values, not CSV. Types apply after latest-version-per-path
selection, so matching only an older version never admits the document.

`GET /api/memories/:id?project=<exact live canonical ID>` requires the same
fresh project authorization and uniquely owned alias set before loading the ID.
It returns the existing memory DTO with `project` set to that canonical ID.
Wrong project, hidden or unknown ID: nondisclosing 404; invalid ID/missing project:
400; noncanonical/removed project: 403. Detail retains the existing nondeleted-row
semantics; it does not broaden page eligibility or rewrite stored provenance.

`GET /api/memories/:id/audit?project=<exact live canonical ID>` applies that same
project/alias and workstation/principal/domain read policy before fetching audit
rows, and retains the existing manage permission. Missing project is 400;
nonlive/noncanonical project is 403; hidden, cross-project or unknown memory is
nondisclosing 404. No context-free legacy fallback exists. `limit` retains its
default 50 and range 1..200. The unchanged safe DTO includes action, actor,
session reference, reason, timestamp and state-presence flags, never raw states.
Audit validation errors (invalid ID, malformed query, missing/invalid project or
limit) use plain-text 400 bodies; project authorization uses a JSON error object
with status 403, matching the existing OpenAPI response schemas.

Documents select the latest version of each path **before** filtering. Search is
limited to public list metadata (`path`, `doc_type`, `author`), not content,
arbitrary JSON metadata or secrets. `doc_type` (128 bytes) and literal
`path_prefix` (4096 bytes) remain supported; ordering is `created_at DESC, id DESC`.
Document content/version/history shapes and comments are unchanged. Content,
version and history reads use the same exact live canonical project and lawful
stored alias set as list/count. The current document schema has no independent
principal/privacy ACL: its reader boundary is the existing authenticated
project-reader scope.

Missing/blank project or invalid parameters: 400. Noncanonical, missing, removed
project or forbidden credential: nondisclosing 403. Authentication remains 401;
unavailable store: 503; unexpected query failure: 500 without SQL diagnostics.
Offsets are stable only while the dataset is unchanged, not snapshot/cursor
tokens. An explicit UI “All projects” context uses descriptor IDs; empty project
never widens server reads and does not trigger an automatic content scan.

Stored legacy TEXT projects are readable only when the live canonical row's
existing `legacy_ids` maps each selector uniquely back to that same ID. Aliases
claimed by another, removed or merged project are excluded. Canonical descriptors
come from `projects`, including memory-only/document-only registered projects,
not from sessions or label grouping. Unregistered orphan TEXT rows have no lawful
catalog ID and remain untouched; this reader cannot register or infer one.
These canonical readers do not register orphan data or fall back to raw
selectors. The separate unresolved-history readers above expose only their
validated retained memberships; they do not turn those records into canonical
owners or automatic context.

## Browser session handles and safe log correlation

The private `sessions.id` remains the `engram_auth` HttpOnly cookie credential.
Cookie authentication, expiry, revocation and logout continue to use that private
value. It is never a management selector or an `AuthSession` JSON ID.

| Method | Path | Session identity contract |
|--------|------|---------------------------|
| `POST` | `/api/auth/user-login` | Issues the credential only in the cookie; records safe audit correlation. |
| `POST` | `/api/auth/user-logout` | Revokes the cookie's session and clears that cookie; existing origin/idempotency checks remain. |
| `GET` | `/api/access/sessions` | `sessions[].id` is a stable public handle. `include_revoked=true` also includes expired/revoked rows. |
| `GET` | `/api/access/users/{id}` | Session IDs and browser-auth audit references use the same public handles. |
| `GET` | `/api/access/log` | Historical and new browser-auth states expose safe correlation, not credentials. |
| `POST` | `/api/access/sessions/{id}/revoke` | Accepts only the public handle; credential and unknown selectors return 404 without mutation. Success repeats the handle. |

All seven access GET templates (`providers`, `invitations`, `users`, `users/{id}`,
`roles`, `sessions`, `log`) retain their session-admin guard. Master/client keycards
and synthetic auth-disabled identities do not pass it. Existing HMAC session-admin
read admission remains; revocation additionally requires the current enabled DB
admin cookie. A public handle never authenticates as a cookie. Refresh an old
Access view before revoking; cached credential selectors are not accepted as a
compatibility route.

Migration `190_browser_session_public_handle` adds one `sessions.public_handle`
TEXT column and the unique `idx_sessions_public_handle` index. Its value is
`bsh1_` followed by lowercase SHA-256 of the exact UTF-8 bytes of
`engram/browser-session-management/v1:` plus the stored credential. No trimming or
case folding occurs. The transform relies on random credential entropy; it does
not strengthen weak values. The original credential/PK and all lifecycle fields
are retained. Backfill and owned DDL run in an explicit transaction and refuse
incompatible retained handles or index state; collisions fail rather than select
another handle. A BEFORE INSERT / UPDATE OF id, public_handle trigger computes
the same value for old SQL writers. No mapping table or expression-index
immutability assertion is involved. Reapplication is idempotent. Destructive down
migration is refused and retains the additive schema. Old-writer compatibility
does **not** make an older credential-exposing binary a safe behavioral rollback.

New `auth_login` and `auth_session_revoked` events write safe handles. Their
historical before/after states are projected recursively without rewriting stored
audit rows, including orphan references whose session rows no longer exist.
Representable metadata is retained; non-object JSON is safely wrapped instead of
echoed as raw text. Browser-specific Operator Code audit provenance uses the same
handle while tab/chooser/authorization identity remains private and cookie-bound.
SDK/agent session IDs, generic source-session attribution and their APIs are not
converted into browser handles.

The separate `GET /api/logs` JSON snapshot and `GET /api/logs?follow=true` SSE
stream apply scoped browser-session projection to matching log fields, messages
and raw JSON. Browser-session management paths are normalized for logging, and
identified browser credentials in browser-auth diagnostics are hidden. Existing
historical in-memory records are projected on read, not deleted or rewritten;
SSE projects queued/new entries before serialization. Text `query` matching uses
the safe projected message/raw JSON, not hidden credential bytes. Snapshot query
filtering also precedes the latest-`lines` limit; existing level filtering and
chronological ordering remain. Ordinary log-route authentication is unchanged;
this route does not acquire the access-administration session-admin guard.
SDK/agent session correlation and unrelated harmless diagnostic fields remain.
This is scoped browser-session projection, not general secret detection or a
guarantee that arbitrary application logs contain no other secrets.
