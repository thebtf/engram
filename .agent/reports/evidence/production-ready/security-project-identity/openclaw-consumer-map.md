# OpenClaw Project Identity v2 propagation gap

Status update: root authorized the bounded ownership amendment after this map
was produced. The client barrier and all consumers listed below are now wired;
the table remains the pre-change evidence and review checklist. Post-change
residue proof is `22 resolveIdentity call sites == 22 awaited
registerAndResolveProject call sites`, with transport tests covering ordering,
canonical substitution, in-flight/completed dedupe, stable error short-circuit,
config override, session-start, and invalid-bearer negative behavior.

Observed on base `dc891b2d72b1fd63b83e4a630a249241fc389151` plus the maker's
owned `identity.ts` metadata implementation. This is a read-only consumer map;
no path below was edited outside the accepted ownership slice.

## Exact gap

`resolveIdentity()` now returns `projectIdentityV2`, but every live consumer
extracts only `identity.projectId`. `EngramRestClient` has no identity-only
registration method, no canonical-project cache, and no in-flight registration
dedupe. Therefore OpenClaw's first HTTP data access can precede
`RegisterAndResolve` even though the same invariant is already enforced for the
stdio daemon/gRPC path and Claude hooks.

The earliest normal OpenClaw access is `hooks/session-start.ts`: it resolves the
identity and immediately schedules `client.initSession(...)` without awaiting a
registration. `hooks/before-agent-start.ts` later calls `getContextInject`, but
that request currently sends only `{agent_id,cwd}` and also performs retrieval;
it is not a registration barrier.

## Client endpoint and request-shape map

| Client method | Endpoint | Current project/identity shape | Data class |
| --- | --- | --- | --- |
| `getContextInject` | `POST /api/context/inject` | `{agent_id,cwd?}`; no `project`, no v2 metadata | project context read |
| `searchContext` | `POST /api/context/search` | `{project,query,...}`; selector only | project context read |
| `searchDecisions` | `POST /api/decisions/search` | `{project,query,limit?}`; selector only | project decision read |
| `trackSearchMiss` | `POST /api/analytics/search-misses` | `{project,query}`; selector only | project telemetry write |
| `ingestEvent` | `POST /api/events/ingest` | `{session_id,project,tool_*}`; selector only | project event write |
| `backfillSession` | `POST /api/backfill/session` | `{session_id,project,content}`; selector only | project memory write |
| `initSession` | `POST /api/sessions/init` | `{claudeSessionId,project,prompt?}`; selector only | project session write |
| `bulkImport` | `POST /api/observations/bulk-import` | `project` copied from first observation; selector only | project memory write |
| `getFileContext` | `GET /api/context/by-file` | `project` query parameter; selector only | project context read |
| `getTimeline` | `POST /api/context/search` | `{project,mode,...}`; selector only | project context read |
| `storeCredential` | `POST /api/vault/credentials` | `{...,scope,project}`; selector only | private credential write |
| issue methods | `/api/issues...` | optional project/source-project selectors; no v2 | cross-project private read/write |

`getCredential`, observation-by-ID mutation helpers, session-outcome helpers,
health, and self-check do not accept a project selector. They still rely on
bearer/principal authorization; project identity must not be presented as an
authorization substitute.

## Every live `resolveIdentity` consumer

| Consumer | First HTTP operation after identity resolution | Ordering defect |
| --- | --- | --- |
| `src/hooks/session-start.ts:38` | `initSession` (`POST /api/sessions/init`) | fire-and-forget write is scheduled immediately |
| `src/hooks/before-agent-start.ts:47` | `getContextInject` (`POST /api/context/inject`) | retrieval request has only agent ID/cwd |
| `src/hooks/before-prompt-build.ts:60` | `searchContext` | read before registration |
| `src/hooks/before-tool-call.ts:77` | `getFileContext` | read before registration; 500 ms path |
| `src/hooks/after-tool-call.ts:74` | `ingestEvent`, then possible `searchDecisions` | fire-and-forget write before registration |
| `src/hooks/before-compaction.ts:40` | `backfillSession` | fire-and-forget write before registration |
| `src/hooks/session-end.ts:74` | `backfillSession` or `setSessionOutcome` | write before registration |
| `src/commands/remember.ts:38` | `bulkImport` | write before registration |
| `src/index.ts:195` | CLI `searchContext` | read before registration |
| `src/index.ts:218` | CLI `bulkImport` | write before registration |
| `src/services/file-watcher.ts:40` | later `bulkImport` in flush | constructor keeps selector only; metadata is discarded |
| `src/tools/engram-decisions.ts:43` | `searchDecisions` | read before registration |
| `src/tools/engram-find-by-file.ts:49` | `getFileContext` | read before registration |
| `src/tools/engram-issues.ts:190` | create/list/get/update issue by action | private read/write before registration |
| `src/tools/engram-presets.ts:47` | `searchContext` | read before registration |
| `src/tools/engram-remember.ts:77` | `bulkImport` | write before registration |
| `src/tools/engram-search.ts:47` | `searchContext` | read before registration |
| `src/tools/engram-timeline.ts:55` | `getTimeline` | read before registration |
| `src/tools/engram-vault.ts:61` | `storeCredential` or `getCredential` | private access before registration; get has no selector |
| `src/tools/memory-get.ts:58` | optional local-file `bulkImport` | write before registration |
| `src/tools/memory-get.ts:134` | remote `searchContext` | read before registration |
| `src/tools/memory-migrate.ts:169` | batched `bulkImport` | write before registration |

## Bounded successor/amendment shape

The smallest end-to-end amendment is:

1. `src/client.ts`: add an awaited `registerAndResolveProject(identity,
   selector)` barrier that sends
   `{project,project_identity,identity_only:true}` to
   `POST /api/context/inject`, returns `canonical_project`, and deduplicates
   concurrent registration with an in-flight promise keyed by full identity.
   Stable HTTP error code/action must propagate; no downstream request is sent
   after registration failure.
2. Change `getContextInject` to accept the resolved canonical project and send
   it explicitly; it remains a retrieval call, not the registration primitive.
3. At every consumer above, await the barrier immediately after
   `resolveIdentity()` and before the first client call. Replace every
   fire-and-forget first access with `await barrier; void dataCall(...)`.
4. `file-watcher.ts`: retain the full identity in the service and await the
   barrier in `start()` before installing/flush-enabling the watcher.
5. Configured `config.project` is the outer compatibility selector. When a
   workspace v2 identity exists it is sent with that selector; when workspace
   metadata is unavailable, send legacy-only and fail closed if ambiguous.
6. Tests: a fake fetch sequence must prove registration is request 1 and data
   access request 2; canonical substitution; concurrent dedupe; late-hook
   idempotence; first session-start access; registration-error short circuit;
   config-project override; missing-workspace legacy behavior; and that selector
   knowledge without a bearer does not authorize private access.

Likely amended owned paths: `src/client.ts`, the 20 consumer files above,
`test/project-identity-transport.test.mjs`, and any existing hook/client tests
whose request fixtures require the additive registration call. No server schema,
migration, auth, or unrelated OpenClaw behavior needs expansion.
