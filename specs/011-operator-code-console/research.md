# Feature 011 Phase 0 Research

**Scope**: Resolve Feature011 implementation choices from the approved specification and current source. This document does not reaccept UCI, claim a release, or claim that a planned route/table exists.

## R-01 — Browser principal and explicit read grants

**Decision**: Introduce a canonical `BrowserSubject` derived only from a real persisted browser user session, and persist an explicit active grant keyed by `(auth_realm, browser_subject, source_id, checkout_id)`. The grant is checked before list, selection, search, graph, source, cursor continuation, and index-intent admission. It is read authority only.

**Rationale**: `middleware.go:464-494` resolves database/authentik users into `auth.Session(user.Role)`, while `identity.go:146-157` gives session and disabled-auth identities no Principal. `uci_context_store.go:177-223` deliberately admits only `checkout.owner_principal`; its source comment says broad grants are unavailable until modeled in PostgreSQL. A browser role, Space, path, label, tab, and legacy project therefore cannot safely stand in for a grant.

**Rejected alternatives**:
- Treat `admin` or `SourceSession` as code authority: would disclose code without an explicit Source+Checkout grant.
- Use `SourceClient`/workstation identity for the browser: would spoof daemon ownership and couple browser sessions to a keycard.
- Grant at Source scope only: conflicts with the requested checkout-specific access and private dirty View boundary.

**Implementation boundary**: Browser subject mapping and grant lifecycle are owned by the auth/grant writer. The UCI authorizer remains default-deny and distinguishes browser read from owner-only daemon/index authority. Disabled-auth and legacy HMAC sessions do not receive new Code browsing access.

## R-02 — Shared UCI response release

**Decision**: Factor the release operation currently implemented by MCP into a UCI-owned transport-independent port. Both MCP and the new ordinary HTTP adapter provide an already-authorized pre-exposure response and receive either a fully released response or the existing closed failure/refusal envelope.

**Rationale**: `UCIApplication` states it returns pre-exposure results (`uci_application.go:19-21`). `releaseCodebaseQueryResponse` (`tools_code_intel.go:780-892`) verifies current epoch, reauthorizes the exact context, validates the pre-exposure response, records exposure, and only then serializes contextual data. Duplicating those steps in a handler risks a cross-transport evidence/security split.

**Rejected alternatives**:
- Serialize `UCIApplication` response in HTTP directly: bypasses reauthorization and exposure append.
- Proxy browser JSON through MCP over HTTP: violates the permanent no-HTTP-MCP boundary.
- Record a browser-specific receipt: creates a competing evidence owner.

**Implementation boundary**: The release owner preserves current MCP vectors and exposes no raw recorder/store detail to HTTP. Recorder failure, epoch change, authorization failure, or response validation failure returns no contextual source/graph body.

## R-03 — Tab-bound browser context

**Decision**: Use a randomly generated opaque tab key stored in browser `sessionStorage`; bind it server-side to the authenticated browser session and an exact selected ContextRef. Context binding is convenience state only: every request is reauthorized against the current grant and View relation. Hard reload retains the same tab key; another tab and any MCP session have a different binding.

**Rationale**: Feature010's `ContextResolver.Authorize` (`internal/uci/context_resolver.go:46-52`) validates an explicit ContextRef without modifying mutable client selection. Its client bindings are scoped to a client session (`identity-and-worktrees.md:79-84`). A normal browser needs equivalent isolation but must not mutate MCP's binding map.

**Rejected alternatives**:
- A global browser “current worktree”: violates two-tab isolation.
- Put a ContextRef only in a URL/local storage and trust it: converts a selector into authority and leaks state across tabs.
- Auto-follow latest View: breaks exact source/search/graph coherence.

**Implementation boundary**: Browser context is lost safely on server session expiry; an authorized hard reload rehydrates it through the tab key and server binding. A newer View is presented as an explicit available transition, never a silent substitution.

## R-04 — Honest mutation result

**Decision**: Replace `success|rollback` with one discriminated `OperatorMutationResult`: `committed_verified`, `committed_verification_pending`, `partial`, `failed`, or `outcome_unknown`. It carries a request reference, per-item outcomes when applicable, and a safe next action. Every current direct caller of `runOperatorMutation` migrates in S1b.

**Rationale**: Current `runOperatorMutation` calls optimistic update, request, and refresh in one try block (`useOperatorApi.ts:410-440`), labels any callback invocation `success`, and invokes a local snapshot rollback when either request or refresh throws. A callback load error after a known commit is not rollback; a network loss after a write does not prove the write failed.

**Rejected alternatives**:
- Keep `rollback` with a clarifying message: still asserts a server effect never observed.
- Add a generic cross-domain mutation engine: would move domain validation/audit ownership out of Rules, Issues, Memory, Queue, and Documents.
- Replay an ambiguous non-idempotent request: can duplicate an already committed action.

**Implementation boundary**: Existing actions without a durable status/readback endpoint remain `outcome_unknown` after ambiguous commitment and are not replayed. New collection actions that need retry/status add a domain-owned operation resource. An authoritative postcondition is operation-specific: updated field/version, authorized absence for delete, or non-disclosing status after access loss.

## R-05 — Pagination, selection, and Rules reorder

**Decision**: Use opaque server cursors for list pages and a typed selection union: none, explicit IDs, current page, or frozen all-filter token with explicit exclusions. The token binds subject, domain, canonical filter/sort fingerprint, target revision snapshot/count, expiry, and authorized target set; it is not a capability. Rules reorder uses one scope-local transaction with expected versions for every target.

**Rationale**: Rules currently request `all=true&limit=200` and reorder with `Promise.all` of individual PATCHes (`useOperatorRules.ts:105-130,222-247`). Issues request `limit=100` and bulk patch each row concurrently (`useOperatorIssues.ts:278-325,488-516`). The Rules store increments Version (`behavioral_rules_store.go:161-188`) but the handler request currently has no expected-version field.

**Rejected alternatives**:
- Download all records before selection: turns a page limit into an unbounded data/ACL risk.
- Treat page select-all as filter select-all: cannot name the intended target set.
- Retry partial reorder until it looks ordered: violates all-or-nothing scope semantics.

**Implementation boundary**: Rules are first. Each later domain retains its action matrix and readback condition; unsupported action requests receive `unsupported`, not a generic fallback. Filter/context/permission changes clear or require reconfirmation of a dangerous selection.

## R-06 — Durable daemon index control

**Decision**: Represent browser demand as a persisted `IndexIntent` with request idempotency and safe state. The daemon owner acknowledges/claims it through its existing private UCI path, and completion points to a new readable View only after publication/readback.

**Rationale**: Feature010 requires durable jobs, leases, current-view publication, and server-authorized context. The browser does not have a safe remote working-copy path or workstation credential, so it cannot index directly. A request accepted by HTTP cannot prove local owner execution.

**Rejected alternatives**:
- Browser calls an arbitrary path/reindex endpoint: exposes a filesystem authority and bypasses daemon ownership.
- Treat HTTP 202 as complete: collapses intent, ACK, execution, and readback.
- Browser-side polling of a daemon: leaks topology/credentials and cannot preserve server authorization.

**Implementation boundary**: `queued` and `unavailable` are honest nonterminal states. Retry uses the persisted intent/request reference; a daemon may publish only through existing fenced UCI view publication.

## R-07 — Live browser/API/DB proof

**Decision**: Keep current Playwright suite as UI interaction evidence and add a separate live configuration that targets a built Nuxt console, real authenticated Go API, and disposable PostgreSQL fixture. The fixture uses a real browser session and explicit grant; it does not enable disabled-auth or inject a master/browser bypass.

**Rationale**: `playwright.config.ts:27-56` starts `scripts/mock-operator-api.mjs`, which cannot establish route registration, Go authorization, persistence, postcondition readback, or daemon ACK. `package.json` has verified `build`, `test:seam`, `test:parity`, and `test:browser` scripts but no live-test script.

**Rejected alternatives**:
- Call a handler/unit test from Vue: cannot prove browser serialization/auth/context isolation.
- Rebrand existing mock suite as end-to-end: contradicts the acceptance boundary.

**Implementation boundary**: S2 adds a named live config/script and fixture under the console's existing test convention. It exercises hard reload, two tabs/two worktrees/MCP, semantic result availability, graph/source View coherence, revocation, and all required truthful UI states. S1a may use the same real route-trace harness for traffic assertions.

## R-08 — Design promotion

**Decision**: Preserve `.od → design/operator-console → apps/operator-console`; accepted flow precedes parity change. Runtime code is developer-owned integration, not a raw design export destination.

**Rationale**: `PROMOTION-CONTRACT.md:3-56` identifies the curated snapshot and says routine promotion never overwrites `apps/operator-console`.

**Implementation boundary**: Design source owner changes the flow and manifest; UI owners add only scoped parity evidence after the promoted design is accepted. Existing global parity drift is not cleared by Feature011 without route-level observed evidence.

## R-09 — r11 mechanism adaptation evidence

**Decision**: Keep native Engram implementation. A separate R-C research handoff records, for each mechanism that changes scope, the user task, pinned reference commit/file/test or issue, observed behavior, known limitation, Engram-specific DB/worktree/UI adaptation, and positive/negative/failure-mode proof. Feature011 does not duplicate this research.

**Rationale**: r11 `04-MECHANISM-ADAPTATION.md` requires source/test/issue-grounded adaptation and prohibits embedding SocratiCode or Graphify as applications, subprocess engines, or mandatory dependencies. It also requires one Source/Checkout/View model across index/graph/agent/browser.

**Implementation boundary**: No pinned upstream finding is asserted here because none was supplied to the Feature011 planner. This blocks parity, new-language, or new-mechanism claims only. S2 presents the already accepted UCI capability and its actual coverage/limitations.

## Resolved Planning Ledger

| Question | Resolution | Contract |
|---|---|---|
| Browser identity versus UCI owner identity | Canonical real-user browser subject plus explicit read grant; no role/Space/path inference. | `browser-context-and-grants.md` |
| How HTTP preserves UCI release semantics | UCI-owned shared release port after application/pre-exposure response. | `operator-code-http.md` |
| Context persistence and isolation | Server binding scoped to authenticated browser session + opaque tab key; `sessionStorage` preserves only that key across reload. | `browser-context-and-grants.md` |
| Mutation status after response loss | Shared truth union; domain operation status only where a domain supports safe inquiry/retry. | `collection-operations.md` |
| All-filter and reorder integrity | Frozen token plus rechecked ACL/version; transactional Rules scope reorder. | `collection-operations.md` |
| Browser-index execution | Durable intent, daemon ACK/claim, new View readback. | `index-intents.md` |
| Reference/upstream strategy | Bounded pinned evidence handoff; native adaptation only. | `mechanism-adaptation.md` |
