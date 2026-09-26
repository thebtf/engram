# Implementation-Readiness Checklist: Operator Workspace — D-A

**Purpose**: Determine whether the D-A requirements, plan, tasks, data model, and contracts are ready for later implementation dispatch. This is a reviewer-owned requirements-quality review, not implementation or release evidence.

**Amended**: 2026-09-17
**Feature**: [spec.md](../spec.md), [plan.md](../plan.md), [tasks.md](../tasks.md), and [contracts/](../contracts/)

## Contract completeness

- [ ] CHK001 Do spec, plan, tasks, quickstart, data model, and HTTP/grant contracts use the same Repository / Working copy / Indexed snapshot vocabulary and preserve Source/Checkout/View authority?
- [ ] CHK002 Does the catalog/onboarding handoff prohibit UUID input, locator disclosure, administrator inference, and fixture-grant onboarding while allowing exact owner-scoped issue/revoke?
- [ ] CHK003 Is bounded selected-View structure explicitly separate from search, with normalized prefix, opaque continuation, limit, coverage/limitations, and no filesystem fallback?
- [ ] CHK004 Does relation navigation retain direct/reverse direction, evidence references, source-descriptor authorization, honest evidence granularity, and the off-page-neighbor path?
- [ ] CHK005 Do existing binding, grant, UCI release, tab/MCP isolation, revocation, and explicit newer-View invariants remain intact after D-A changes?

## Retirement readiness

- [ ] CHK006 Does T005's nonempty consumer map name UI, route/tool, application, writer/job, storage, readers/exports, compatibility, and the retained `internal/retrieval/hybrid.go` Tier2 `Traverse`, historical graph reads, `/api/context/search`, `internal/graph`, and UCI graph?
- [ ] CHK007 Does the existing single-container cutover prove no old book-writer process is live before idempotent `failed`-with-reason transition, preserve `source_book_job_id`/partial documents, rerun identically, and add no lease/heartbeat?
- [ ] CHK008 Do retirement tasks explicitly exclude UCI graph and `internal/graph` removal, historical graph/read consumers without a disposition, storage drop, applied-migration edits, and a flag-controlled writer alias?

## Dispatch and proof readiness

- [ ] CHK009 Are T001–T005 independently dispatchable with disjoint writer zones, including the existing binding subsystem owned by T002, with T002–T004 feeding T006a and T005 feeding T006b?
- [ ] CHK010 Does the same integrator serially own T006a then T006b over `handlers_operator_code.go`, `service.go`, `uci_context.go`, `server.go`, and `migrations.go`, with no producer parallel edit?
- [ ] CHK011 Can T008a record DA01/DA02/DA04 journey evidence after T006a/T007 and T008b record DA03 retirement evidence after T006b without waiting on the other lane, while composite source acceptance requires both approved records?
- [ ] CHK012 Does quickstart require a disposable real server/database/worktree/provider lane, one real-provider non-lexical >50-candidate query, two worktrees/tabs, historical data, actual zoom, and explicit `SKIP`/`NOT_PROVEN` handling rather than mock-only proof?

## Boundaries

- [ ] CHK013 Are D-B and D-C absent from the task graph and implementation prerequisite chain?
- [ ] CHK014 Is the secure-origin/authentication mode an explicit installation decision with no fabricated no-grant/onboarding workaround, composite source acceptance after T008a+T008b distinct from release acceptance, and a closed release gate until installed normal-homepage DA01–DA04 proof exists?
- [ ] CHK015 Does design work require private `.od` source plus curated promotion and keep an unpromoted public snapshot/parity ledger from becoming D-A authority?

## Review notes

- The prior checklist evaluated the superseded broad Feature 011 plan and is retired.
- An independent reviewer marks this checklist only after inspecting the committed D-A amendment and its exact task graph. It does not authorize implementation, deployment, release, or production mutation.