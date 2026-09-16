# Browser Context and Read-Grant Contract

## Browser Subject and Exact Source Owner

A `BrowserSubject` is a stable, realm-qualified representation of one real persisted authenticated browser user, with kind `human`. The authenticated DB-session/Authentik path must carry that persisted user identity into the request; the current `auth.Session(role)` shape alone is insufficient because it carries no principal. This mapping is a planning addition, not an authorization fallback.

The following never create a BrowserSubject or Code read authority: a master bearer, a client/workstation keycard, a HMAC legacy admin session without a persistent user identity, `ENGRAM_AUTH_DISABLED`, an administrator role, Space membership, a source or checkout label, a path, a branch, a tab binding, or a legacy project identifier. Existing authentication retains its own behavior; this contract does not redesign general IAM.

`ci_checkouts.owner_principal` in the exact source realm is the existing Source-owner authority. A browser subject may administer grants for a tuple only when its canonical source-owner principal exactly equals that recorded `owner_principal`. A role is never substituted for this equality; if the authenticated user cannot be mapped to that principal, issuance and revocation are denied.

## Read Grant and Issuance Seam

A `BrowserReadGrant` permits one BrowserSubject to read one exact `(Source, Checkout)` in one realm. It has an opaque grant reference, `active|revoked|expired` state, issuer principal, expiry, and issue/revocation timestamps. It does not select a View: UCI still validates the selected `ContextRef` and its Source/Checkout/View relationship.

The sole grant-lifecycle writer is the proposed `CodeGrantApplication`, presented through the authenticated normal HTTP administrative family:

| Route | Input and authorization | Effect |
|---|---|---|
| `POST /api/code/grants` | Exact source and checkout IDs, existing target BrowserSubject, optional bounded expiry. The session-derived issuer must equal the exact checkout `owner_principal` in its realm. | Creates or restores the one exact grant and writes `code_grant_issued` in the same transaction. |
| `POST /api/code/grants/{grant_ref}/revoke` | Opaque grant reference. The same exact Source-owner equality is rechecked against the grant tuple. | Transitions the grant to `revoked` and writes `code_grant_revoked` in the same transaction. |

`CodeGrantApplication` verifies that the target is an enabled persisted human subject in the same realm. It calls the authoritative UCI context catalog for the owner predicate and uses `gorm.AuditStore.LogTx` in the grant transaction. Audit failure aborts the grant transition. Audit reason fields contain only opaque grant/tuple/target references; no locator, source body, query, credential, or ungranted label is written. This is the one executable issuer path used by fixtures; it is not an admin-role bypass, broad IAM product, or browser grant-management UI.

Authorization rule:

```text
allowed_code_read =
  real authenticated BrowserSubject
  AND active BrowserReadGrant(realm, subject, source, checkout)
  AND UCI ContextRef belongs to the exact realm/source/checkout
  AND UCI View is an allowed published or retained historical View
```

The rule is applied before safe context-list metadata and again at release immediately before contextual serialization. Revocation takes effect before the next request/release. A grant permits code read only—not index ownership, source registration, local-root inspection, View publication, arbitrary history enumeration, or access to another checkout of the same Source.

## Server-Issued Tab-Binding Protocol

`sessionStorage` is a reload convenience, never proof that two browser documents are distinct. The **Browser Binding Application owner** is the sole server writer of an opaque `tab_binding_id`, its document lease, document-proof digest, resume material, pinned context, expiry, and focused state-machine/persistence tests. It receives the canonical BrowserSubject/session carrier from Auth/grants; gives the HTTP owner a binding-transition/guard port, the composition owner its store/wiring declaration, and the Code UI its documented headers and transition results. It never issues a grant, authorizes a Source/Checkout, serializes an HTTP route, or changes the shared live harness.

On bootstrap a document makes a fresh in-memory `document_nonce`. The binding transition exchanges it for an opaque current `document_proof`; the browser retains that proof only in document memory. The server stores digests only. A first binding also creates a cryptographically random `tab_resume_nonce` in the Code Explorer `sessionStorage` namespace. The server issues a binding-scoped, one-time `reload_resume_token` as an HttpOnly, Secure, SameSite cookie and persists only its digest. That cookie is **not tab-isolated** and never classifies a document: it is consumed only after the bootstrap classifier has selected `resume`.

The Code UI classifies the bootstrap before calling either endpoint. A direct fresh document with no pair calls `handshake`. A fresh opener is one-time: on its first `navigate` bootstrap with `window.opener` present, it clears copied Code Explorer storage, sets `window.opener = null`, and readbacks that it is null before calling fresh `handshake`. If normalization cannot be read back, it clears the pair and returns `TAB_BOOTSTRAP_AMBIGUOUS`; that browser/window is not eligible for automatic resume. After successful normalization, no opener plus `PerformanceNavigationTiming.type === "reload"` and a valid pair selects `resume`; no opener plus `type === "navigate"` and a pair is the browser’s normal Duplicate/copy case and selects `handshake` with that pair. `back_forward`, absent/malformed/unknown navigation metadata, an opener that persists beyond first bootstrap, an unexpected opener/navigation tuple, or an invalid pair is `ambiguous`: it clears the pair, calls fresh `handshake`, returns `TAB_BOOTSTRAP_AMBIGUOUS`, and never resumes a pin. Navigation metadata and opener normalization are client routing signals, not document proof or authorization; the S2 fixture records its actual browser engine/version and observed opener/navigation/readback tuple, and makes no automatic-resume/collision claim for a browser that does not exhibit these stated signals. None of the pair, proof, or token is an ACL, grant, URL value, log field, shared `localStorage` value, or MCP identity.

Except for the bootstrap or resume transition itself, every binding-bound Code request and every lease `renew` or `close` carries both `X-Engram-Tab-Binding-ID` and `X-Engram-Document-Proof`. The binding application matches both to the authenticated browser session and current live lease before HTTP/UCI work. The proof identifies only the current document within that subject; the separate exact BrowserReadGrant and UCI ContextRef checks still authorize every context use and release.

### Binding transition table

| Event | Required proof and condition | Deterministic server transition | Context and failure posture |
|---|---|---|---|
| Fresh direct document or first fresh opener | A direct `navigate` has no pair. A first opener `navigate` clears copied storage, sets `window.opener = null`, and verifies the null readback before `handshake`. | Create a fresh binding, document proof, live lease, resume pair, and one-time reload token. | The fresh binding has no pinned ContextRef; explicit grant-authorized selection is required. A fresh opener does not expect a collision. |
| Explicit duplicate/copy document | The bootstrap records normalized no opener, `PerformanceNavigationTiming.type === "navigate"`, a new `document_nonce`, and an inherited resume pair for an otherwise valid unexpired binding; it calls `handshake`, not `resume` | Retain the existing binding unchanged and return `TAB_BINDING_COLLISION` with a fresh binding, proof, lease, resume pair, and token for the requesting document. | The replacement binding has no ContextRef. The original document and pinned context remain usable only with its current proof. |
| Normal hard reload after acknowledged close or document-lease expiry | The bootstrap records normalized no opener and `PerformanceNavigationTiming.type === "reload"`; `resume` carries authenticated session, binding ID, resume nonce, new `document_nonce`, and the unconsumed server-owned reload token; prior lease is `closed` or `expired` | Atomically retire the old document proof, create the new proof/lease on the same binding, and consume/rotate the reload token. | The server retains the pinned ContextRef; subsequent binding-guarded requests reauthorize it before any metadata or contextual result is returned. |
| Ambiguous bootstrap | `back_forward`, absent/malformed/unknown navigation metadata, opener normalization failure or persistence after first bootstrap, unexpected opener/navigation tuple, or an invalid pair | Clear any copied pair and create a fresh binding through `handshake`; return `TAB_BOOTSTRAP_AMBIGUOUS`. It never selects `resume`. | No inherited ContextRef is returned. This fails closed rather than treating a cookie, opener, or copied pair as tab evidence. |
| Hard reload while `pagehide`/keepalive close is delayed or missing | The same valid `resume` material arrives while the prior document lease is still `live` | Return non-contextual `RELOAD_PENDING`; retain the binding, pin, old proof/lease, and unconsumed token. The client retries the same resume only after close acknowledgement or lease expiry. | No copied binding is admitted and no contextual metadata is disclosed while pending; the retained pin prevents normal reload from losing context. |
| Crash or discarded document; document-lease expiry | No close arrives; the bounded live lease expires | Mark only the document lease `expired`; retain the binding, pinned ContextRef, and valid unconsumed reload token until their own expiry. A later valid `resume` takes the expired-lease normal-reload branch. | Old document proof fails closed. Lease expiry alone never deletes the binding or pin. |
| Document lease renewal/close | Current binding ID and current document proof match the authenticated session and live lease | Renew or close only that document lease. A close makes the normal-resume branch immediately eligible. | Mismatched/stale proof is non-disclosing and cannot affect another document or its binding. |
| Token/proof replay or a foreign, stale, revoked, or expired binding | A consumed/replayed token, non-current proof, session mismatch, grant failure, or binding expiry | Refuse without replacing or disclosing the existing binding. A copied document obtains a fresh binding only through the explicit collision row. | No ContextRef, count, edge, source body, or exposure receipt is released. |
| Browser session, logout, or binding expiry | Session ends or binding TTL elapses | Destroy the binding, pin, document lease/proof, and resume material. It does not mutate the separately owned BrowserReadGrant. | A later fresh binding has no inherited context and requires an explicit currently grant-authorized selection. |

`GET /api/code/contexts` returns only currently grant-authorized safe choices after binding-proof validation. `PUT /api/code/tabs/{tab_binding_id}/context` validates an explicit UCI `ContextRef`, active grant, and current binding/document proof, then pins that context only to the requested binding. A new View is a separately listed transition and never overwrites a selected historical View.

## Continuations and Deep Links

An opaque continuation binds BrowserSubject, browser session, `tab_binding_id`, current document-proof digest, exact `ContextRef`, canonical query/graph shape, ACL epoch, and expiry. It is validated before the underlying query/graph operation releases any result. Any mismatch produces the same non-disclosing context/permission failure rather than a stale page or count.

A deep link can retain a non-authoritative display selector only. On opening it, the browser obtains a current authorized context list and must explicitly select when the selector is absent, ambiguous, revoked, or not the same pinned View. A deep link never carries a bearer grant, binding, resume nonce, or proof of access.

## Required Proof

- The live fixture issues and revokes a grant through `CodeGrantApplication`; a matching Source owner is audited, an admin without that exact owner principal is denied, and a granted non-admin human reads only its granted checkout.
- Every binding-bound request and lease operation proves both its binding ID and its current document proof. The fixture records the browser engine/version, `window.opener`, opener-normalization readback, and `PerformanceNavigationTiming.type` before bootstrap: a first fresh opener clears storage and normalizes opener to null without collision; then opening that child, selecting a context, and hard-reloading that same child must record no opener plus `reload` and retain its own pin. A real browser Duplicate/copy with normalized no opener plus `navigate` receives `TAB_BINDING_COLLISION`; it never selects an endpoint by fixture construction.
- Acknowledged-close hard reload retains each A/B pinned context. A delayed/missing lifecycle close returns `RELOAD_PENDING` without losing the pin, then resumes after close acknowledgement or document-lease expiry; a crash follows the expiry path. Ambiguous bootstrap creates a fresh unselected binding. Replay of a consumed resume token and use of an old document proof fail closed without contextual output. Session or binding expiry destroys the pin and requires explicit re-selection.
- An absent, revoked, expired, or wrong-checkout grant discloses no context label, ID, count, edge, source body, or exposure receipt. Revocation after an application result but before release produces no contextual HTTP response.
- A newer View is visible only as an explicit candidate while source/search/graph stay coherent on the selected View.
