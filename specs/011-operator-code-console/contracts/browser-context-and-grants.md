# Browser Context and Read-Grant Contract

## Browser Subject

A `BrowserSubject` is the stable canonical representation of one real persisted authenticated browser user. The auth/grant owner derives it from the database/authentik user identity during session authentication and attaches it to the request identity separately from role. It has a nonempty realm-qualified subject reference and kind `human`.

The following cannot create a BrowserSubject or Code read access: a master bearer, a client/workstation keycard, a HMAC legacy admin session without persistent user identity, `ENGRAM_AUTH_DISABLED`, an administrator role, a Space membership, a source label, a checkout label, a path, a branch, a tab key, or a legacy project identifier. Existing auth surfaces retain their own behavior; this contract does not redesign general IAM.

## Read Grant

A `BrowserReadGrant` permits a single BrowserSubject to read one exact `(Source, Checkout)` in one realm. It is stored durably with an opaque identifier, state (`active`, `revoked`, or `expired`), issuer/audit metadata, and timestamps. It does not directly select a View: UCI still validates the selected ContextRef and its Source/Checkout/View relationship.

Authorization rule:

```text
allowed_code_read =
  real authenticated BrowserSubject
  AND active BrowserReadGrant(realm, subject, source, checkout)
  AND UCI ContextRef belongs to the exact realm/source/checkout
  AND UCI View is an allowed published or retained historical View
```

The rule is applied before context list metadata and again at release immediately before response serialization. Revocation takes effect before the next request/release. It does not permit index ownership, source registration, local root inspection, view publication, arbitrary history enumeration, or access to another checkout of the same Source.

## Tab Context Protocol

1. On first Code Explorer use, the browser generates a cryptographically random opaque `tab_key` and stores it in `sessionStorage` under the Code Explorer namespace. It is not placed in shared `localStorage`, a deep-link query, logs, or a server-wide default.
2. `GET /api/code/contexts` returns only currently grant-authorized safe display choices.
3. `PUT /api/code/tabs/{tab_key}/context` sends an explicit UCI ContextRef selector. The server validates its tuple, grant, and UCI context, then binds the selected pinned ContextRef to `(authenticated browser session, tab_key)` with bounded session lifetime.
4. Search, graph, source, status, continuation, and index-intent requests present the same tab key. The server resolves the binding but reauthorizes the exact ContextRef every time.
5. Hard reload retains the tab key and may rehydrate the same binding while the browser session remains valid. Session expiry, logout, grant revocation, context mismatch, or binding expiry clears the binding and requires a new explicit authorized selection.
6. A different browser tab has a different tab key. No tab update mutates another tab, an ordinary agent session, or an MCP client context. A newer View is a separately listed transition; it never overwrites a selected pinned View.

## Continuations and Deep Links

An opaque continuation cursor binds the BrowserSubject/session/tab, exact ContextRef, canonical query or graph shape, ACL epoch, and expiry. The server validates all bindings before using it. Any mismatch yields the same non-disclosing context/permission failure rather than a stale page or count.

A deep link may retain a non-authoritative display selector only. On opening it, the browser asks for the current authorized context list and requires explicit selection if the selector is absent, ambiguous, revoked, or not the same pinned View. A deep link never carries a bearer grant or proves access.

## Grant Lifecycle and Auditing

Grant creation, revocation, and listing are narrow auth-owner responsibilities that use existing authorization/audit conventions. Feature011 requires durable active/revoked/expired state and auditability, but does not add a broad roles product, group inference, admin-to-code implicit access, or a browser-facing grant administration workflow unless an accepted auth slice specifically supplies it. Test fixtures may create explicit grants using the same authorized application path; a fixture must not substitute disabled auth or untracked direct browser storage for a grant.

## Required Proof

- Two browser tabs bind to two granted worktrees and remain independent through search, graph, source, hard reload, and a concurrent MCP session.
- An absent, revoked, expired, or wrong-checkout grant discloses no context label, ID, count, edge, source body, or exposure receipt.
- Revocation after an application result but before release produces no contextual HTTP response.
- A browser admin with no explicit grant is denied; a non-admin human with an explicit grant can read only its granted checkout.
- A new View is visible as an explicit candidate while source/search/graph remain coherent on the selected View.
