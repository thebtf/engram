# Feature 011 Phase 0 Research

**Scope**: Resolve Feature011 implementation choices from the approved specification and current source. This document does not reaccept UCI, claim a release, or claim that a planned route/table exists.

## R-01 — Browser principal, Source-owner grant issuance, and explicit reads

**Decision**: Introduce a canonical `BrowserSubject` only from a real persisted browser user session and persist an exact active grant keyed by `(auth_realm, browser_subject, source_id, checkout_id)`. The narrow `CodeGrantApplication` issue/revoke surface is `POST /api/code/grants` and `POST /api/code/grants/{grant_ref}/revoke`; it admits the issuer only when the browser subject’s source-owner principal exactly equals the existing `ci_checkouts.owner_principal` for that exact tuple. Each transition writes `code_grant_issued` or `code_grant_revoked` through the existing transaction-capable audit store.

**Rationale**: `internal/worker/middleware.go:464-494` resolves DB/authentik users but currently creates `auth.Session(user.Role)`, while `internal/auth/identity.go:146-171` has no session principal. `internal/db/gorm/uci_context_store.go:177-223` already constrains UCI authorization to exact `checkout.owner_principal` in a realm, and `internal/db/gorm/audit_store.go:40-53` supplies `LogTx`. The plan extends identity carriage; it never treats a role as the missing principal.

**Rejected alternatives**: administrator-role or SourceSession inference; disabled auth; master/keycard browser authority; Space/path/label/branch/legacy-project implication; a direct fixture DB write; and a broad IAM/grant-management product.

**Implementation boundary**: The auth/grant owner maps enabled persisted user identity to `BrowserSubject`, owns `CodeGrantApplication` and grant/audit transaction, and exposes only exact `CanRead(source, checkout)` to UCI/HTTP. The UCI default remains deny. The live fixture must issue and revoke through this surface.

## R-02 — Shared UCI release with complete caller and metadata mapping

**Decision**: Factor the MCP release operation into a UCI-owned transport-independent release port. Its typed caller maps either existing `mcp_keycard` identity/session or `browser_subject` identity/browser-session/server-issued tab binding; browser attribution is never a synthetic keycard. Contextual status and resulting index View metadata are release categories in addition to search/graph/read; discovery, binding, and non-contextual intent acknowledgement remain ordinary authorization categories.

**Rationale**: `internal/worker/uci_application.go:19-21` returns pre-exposure results. `internal/mcp/tools_code_intel.go:780-892` reauthorizes current context and appends exposure before serialization. `internal/uci/exposure.go:109-121,409-468,552-572,679-685` currently accepts keycard/session fields and only search/graph/read operations, so it cannot silently represent a browser user or new metadata route without an explicit typed extension.

**Rejected alternatives**: direct HTTP serialization; fake browser keycards; an HTTP MCP endpoint; bypassing exposure for code status/resulting View; recording an exposure for refused discovery; or treating a continuation as a cache that can return stale contextual bytes.

**Implementation boundary**: The UCI release owner preserves MCP vectors and adds browser/status/index-result/revocation/recorder-failure vectors. The HTTP owner invokes only that port; detailed route classification is normative in `operator-code-http.md`.

## R-03 — Server-issued tab binding, opener collision, and reload

**Decision**: Use server-issued `tab_binding_id`, a sessionStorage `tab_resume_nonce`, fresh in-memory `document_nonce`, and a server live-document lease. The handshake detects a copied resume pair whose original lease remains live, refuses use of the copied binding, issues a new binding only to the requesting new tab, and requires explicit reselection. A hard reload resumes the same binding only when its browser-session/resume proof is valid and the old lease ended; a bounded lease expiry handles a crashed page.

**Rationale**: `sessionStorage` is copied to an opener-created page (MDN: <https://developer.mozilla.org/en-US/docs/Web/API/Window/sessionStorage>), so a client-chosen stored key cannot prove tab uniqueness. `internal/uci/context_resolver.go:14-16,39-52` shows that mutable selection is client-scoped convenience and explicit authorization must not change a client default. The browser therefore needs its own server binding and cannot mutate MCP’s binding map.

**Rejected alternatives**: global current worktree; `sessionStorage` uniqueness by assertion; a shared localStorage key; silently rotating an existing tab; URL bearer values; or failing a hard reload solely because a fresh document nonce is expected.

**Implementation boundary**: Browser-context owner owns handshake/binding persistence and `X-Engram-Tab-Binding-ID`; console owner clears opener-copied Code storage, stores only the returned resume pair, and handles collision/reselection. The required fixture opens/duplicates a Code tab and independently reloads both A/B tabs.

## R-04 — Honest mutation result

**Decision**: Replace `success|rollback` with `OperatorMutationResult`: `committed_verified`, `committed_verification_pending`, `partial`, `failed`, or `outcome_unknown`. Every direct consumer migrates in one S1b cutover.

**Rationale**: `apps/operator-console/composables/useOperatorApi.ts:410-440` currently joins optimistic update, request, and refresh; a callback load error or response loss cannot prove rollback. The exact twelve consumer inventory and handoff are in `collection-operations.md`.

**Rejected alternatives**: retaining rollback wording; a shared universal domain executor; retrying outcome-unknown writes; or counting helper definition as a consumer.

## R-05 — Pagination, selection, and Rules reorder

**Decision**: Use opaque server cursors plus typed `none|explicit|page|frozen_filter` selection. A frozen filter binds subject, domain, canonical filter/sort/context, permitted target revisions/count, expiry, and exclusions; it is not a capability. Rules reorder is one scope-local expected-version transaction.

**Rationale**: `useOperatorRules.ts:105-130,222-247` uses 200 rows and parallel PATCH; `useOperatorIssues.ts:278-325,488-516` uses 100 rows and parallel PATCH. Existing model `Version` fields do not turn those calls into atomic operations.

**Rejected alternatives**: all-row download; silent bounds clamp; generic cross-domain action; or client-side compensating reorder.

## R-06 — Durable daemon index control

**Decision**: A browser creates an idempotent `IndexIntent`; only daemon owner ACK/claim/execution and an authorized released resulting-View readback complete it. Submission/retry acknowledgement has no exposure; resulting View/status metadata has the explicit index-result release category.

**Rationale**: UCI’s browser cannot safely own a remote working-copy path or workstation credential. HTTP acceptance is not daemon execution and `internal/uci/versioned_read.go:52-55` proves exact reads are stored-View only.

**Rejected alternatives**: browser arbitrary reindex/path endpoint; browser-to-daemon credential transport; treating queued/202 as complete; or disclosing a View when release fails.

## R-07 — Live browser/API/DB proof

**Decision**: Retain mock Playwright as interaction evidence and add a separate live configuration targeting built Nuxt output, real authenticated Go API, disposable PostgreSQL, real agent/daemon, explicit grant path, and real linked worktrees.

**Rationale**: `apps/operator-console/playwright.config.ts:27-56` starts a mock API. `internal/worker/service.go:1677-1807` owns route registration and DB-ready composition. UI-only tests cannot prove identity, grants, release, persistence, normal watcher reconciliation, or source readback.

**Implementation boundary**: The S1a shell/harness owner owns base live config/package script/fixture bootstrap before S1a acceptance. The Code owner owns S2 scenario assertions through that harness; it does not edit shared package script/config.

## R-08 — Design promotion

**Decision**: Preserve `.od → design/operator-console → apps/operator-console`; accepted flow precedes parity changes. Runtime code remains developer-owned integration, never a raw design export destination.

**Rationale**: `design/operator-console/PROMOTION-CONTRACT.md:3-56` makes the curated snapshot authoritative and excludes routine app overwrite.

## R-09 — Pinned current-slice mechanism adaptation

**Decision**: The supplied SocratiCode `78a9eafa3b122c9c8768a3b85cb8c8166a571301` and Graphify `33362d969292b57eda82f3fbd9eb5f3f5bc9bbc2` source handoffs are now represented in the compact current-slice matrix. It covers retrieval fusion, chunk/truncation boundaries, delta/worktree isolation, TS alias/re-export origin/cycle/ambiguity, graph coverage/false-empty, and View-bound viewer behavior. It rejects SocratiCode shared-ID last-writer authority, Graphify main-checkout fallback, static viewer authority, and Graphify benchmark transfer.

**Rationale**: r11 requires pinned behavior → limitation → native adaptation → checks for material delivered mechanisms. The sources are reviewed evidence with stated non-runtime-reproduction qualification, not a new research program. See `mechanism-adaptation.md` for immutable URLs, licenses, labels, exact Engram seams, and proof matrix.

**Gate**: Current-slice matrix and its R-A declared fixture gate S2. Broader R-C source-oracle comparison, extra language/relation support, and optional comparator availability gate only the respective R-C claim; they do not hold the limited S2 current slice.

## R-10 — Finite r11 Residual Ledger

| Residual | Owner | Current classification and closure condition | Preview versus release |
|---|---|---|---|
| Watcher variance | R-A measurement/evidence owner | Bound `dc02236a-r2` evidence reports structural/embedding p95 `1.037/7.459 s`; later installed `a3117803` reports `2.430/17.324 s` against `2/10 s` thresholds. Classify workload, provider, or observer variance—or amend the measurement contract—while retaining both receipts. No SLO is claimed until classification. | Does not block a non-SLO isolated preview; blocks any release freshness/SLO claim. |
| Consumer tuple proof | Integration owner | Freeze and record exact server commit, daemon build, parser bundle digest, resolver/profile, console commit, and fixture identity. Prove fresh ordinary agent, browser A/B, and concurrent MCP against that tuple. | Required for preview acceptance of the cross-surface result and again for release candidate proof. |
| Provider/profile readiness | R-A/UCI owner | Selected View embeds completely under the recorded provider/model/profile before the RU non-lexical semantic step. Failure is visibly lexical/degraded/unavailable, never semantic. | A truthful degraded preview is allowed; semantic preview/release claim requires ready proof. |
| Sonar administrator debt | Release/Sonar administrator | Administrator-owned Sonar DB migration/re-entry, `/api/system/status` UP, fresh exact-candidate `sonar-project.properties`/`coverage.out` analysis, then Quality Gate `OK`. | Controlled isolated preview remains non-release; tag, publication, consumer delivery, and release completion stay blocked. |
| Target-project profile | R-C evaluator | **Engram: supported only for the frozen candidate-declared Go relation plus bounded TS/TSX alias/re-export fixture. nvmd-ai: deferred; NovaScript: deferred.** Each deferred project needs its own declared language/relation/profile, source oracle, A/B result, and exact View-read evidence before a support claim. C#/Vue/Python and generic polyglot claims remain deferred. | No deferred-project parity/support claim is needed for limited Engram S2 preview; any advertised expansion requires its closed evidence packet and release proof. |
| Optional comparators | R-C evaluator | Aligned lexical baseline is required. SocratiCode/Graphify comparators run only with authorized isolated matching corpus/profile; otherwise record `parity_not_checked`. | Comparator absence neither fails limited preview nor permits parity language. |

## Resolved Planning Ledger

| Question | Resolution | Contract |
|---|---|---|
| Browser identity versus UCI owner identity | Real persisted browser subject; exact Source-owner audit issuance; no role/Space/path inference. | `browser-context-and-grants.md` |
| Browser tab cloning versus reload | Server-issued binding plus session/resume proof and document lease; collision rotates only new tab. | `browser-context-and-grants.md` |
| HTTP/MCP release compatibility | Typed caller mapping and endpoint exposure categories after application/pre-exposure response. | `operator-code-http.md` |
| Mutation status after response loss | Shared truth union; domain operation status only where safe inquiry/retry exists. | `collection-operations.md` |
| All-filter and reorder integrity | Frozen token plus rechecked ACL/version; transactional Rules scope reorder. | `collection-operations.md` |
| Browser-index execution/result release | Durable intent, daemon ACK/claim, and released new View readback. | `index-intents.md` |
| Reference/upstream strategy | Fixed current-slice pinned adaptation matrix; broader comparison only for expansion claims. | `mechanism-adaptation.md` |
