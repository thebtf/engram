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

`sessionStorage` is a reload convenience, never proof that two browser documents are distinct. The server owns an opaque `tab_binding_id`; the browser must not choose it.

1. On Code Explorer bootstrap, the document creates a fresh in-memory `document_nonce`. On first binding it also creates a cryptographically random `tab_resume_nonce` in its Code Explorer `sessionStorage` namespace. A page opened with an opener clears copied Code Explorer storage before this step; duplicate-tab behavior remains protected by the server collision path below.
2. `POST /api/code/tab-bindings/handshake` requires the authenticated browser session and submits `document_nonce`, and either no resume pair or `(tab_binding_id, tab_resume_nonce)`. The server stores only digests of the nonces, the browser session reference, and a live-document lease.
3. With no valid resume pair, the server issues a fresh `tab_binding_id` and resume nonce. The browser stores both only in `sessionStorage`; neither appears in a URL, log, shared `localStorage`, MCP identity, or ACL record.
4. A hard reload presents the same binding/resume pair but a new document nonce. If the old document lease has ended through the page lifecycle/keepalive channel, the server verifies the browser-session plus resume-nonce proof, replaces only the document lease, and returns the same binding and pinned context.
5. If that resume pair already has a live document lease, the server rejects use of the copied binding for Code requests and returns `TAB_BINDING_COLLISION` with a new server-issued binding/resume pair for the requesting document. The original binding and its selected context remain unchanged. The new tab stores the rotated pair and must explicitly select an authorized context; it never inherits the original tab’s selection.
6. If a crashed document leaves a lease, the server waits only its bounded lease expiry before a valid session/resume proof may resume. During that interval the result is a non-disclosing collision/retry state, not a shared selection.
7. Every subsequent Code request presents `X-Engram-Tab-Binding-ID`. The server resolves the binding to the authenticated browser session, verifies its current document lease, then reauthorizes the exact `ContextRef` on every use. A different browser tab, ordinary agent session, or MCP client cannot read or mutate that binding.

`GET /api/code/contexts` returns only currently grant-authorized safe choices. `PUT /api/code/tabs/{tab_binding_id}/context` validates an explicit UCI `ContextRef`, active grant, and binding, then pins that context only to the requested binding. Session expiry, logout, revocation, binding expiry, nonce/session mismatch, or collision clears or refuses the affected binding without disclosing stale context data. A new View is a separately listed transition and never overwrites a selected historical View.

## Continuations and Deep Links

An opaque continuation binds BrowserSubject, browser session, `tab_binding_id`, exact `ContextRef`, canonical query/graph shape, ACL epoch, and expiry. It is validated before the underlying query/graph operation releases any result. Any mismatch produces the same non-disclosing context/permission failure rather than a stale page or count.

A deep link can retain a non-authoritative display selector only. On opening it, the browser obtains a current authorized context list and must explicitly select when the selector is absent, ambiguous, revoked, or not the same pinned View. A deep link never carries a bearer grant, binding, resume nonce, or proof of access.

## Required Proof

- The live fixture issues and revokes a grant through `CodeGrantApplication`; a matching Source owner is audited, an admin without that exact owner principal is denied, and a granted non-admin human reads only its granted checkout.
- Two tabs bind to two granted worktrees and remain independent through search, graph, source, hard reload, and a concurrent MCP session.
- Opening/duplicating a Code tab after its parent is initialized triggers the collision path: only the new tab receives a new `tab_binding_id`, must reselect, and cannot alter the parent. Independently hard-reloading both tabs preserves their own selected contexts while their session proofs remain valid.
- An absent, revoked, expired, or wrong-checkout grant discloses no context label, ID, count, edge, source body, or exposure receipt. Revocation after an application result but before release produces no contextual HTTP response.
- A newer View is visible only as an explicit candidate while source/search/graph stay coherent on the selected View.
