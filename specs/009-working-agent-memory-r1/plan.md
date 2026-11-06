# Working Agent Memory R1 implementation plan

**Revision:** 2026-11-02.
**Product owner:** [spec.md](spec.md).
**Behavioral acceptance:** [acceptance/agent-use.md](acceptance/agent-use.md).

This plan replaces the old mechanism-first M1–M6 ordering. Historical packets remain evidence. The existing main coordinator owns decomposition, assignments, integration, and continuation; no second roadmap or coordination service is created.

## Current-order revalidation

Ordinary useful memory and code context do not wait for full Feature 011 or Book Context. The existing UCI feature remains the provider for search/graph/source. Project-free external knowledge depends on the minimum Decision 21 context support and usable links, not on all planned assistant capabilities.

Finish an already active corrective delivery through its accepted process. Prepare the next consumer connection in parallel where dependencies permit. Do not widen a frozen candidate or reset an active owner's work to adopt this plan.

## First implementation decisions

At the first normal working checkpoint, main identifies the current candidate and one integration owner. Where the same people or integration capacity are needed for both deliveries, assign them to A first. B can start or continue independently through existing owners; assigning or starting B is not a condition for A. Reuse the existing task/run records and actual current worktrees.

For A, select a real development job and an existing useful note. For each delivery, define the result checks before execution; do not put the expected answer or source filename into the prompt.

When B starts, choose a meaningful external-client follow-up question and record the existing card content owner and transport mappings. Use neutral fixtures and an authorized ordinary client. Personal assistant configuration or data is outside the required implementation. Resolve at most a consequential missing fact before building; do not restart a project-wide audit. Proposed relation names and context fields are design contracts until API_CONTRACTS and consumers are implemented together.

## Delivery A: ordinary development context

### Complete the missing connection

Trace the callback that the installed host actually loads. Replace a call to a retired/unregistered route with a supported adapter over the existing application workflow. Preserve the stdio/daemon/gRPC boundary for MCP and REST for lifecycle hooks.

Make endpoint errors, empty relevance, stale view, and invalid context distinguishable. An error-swallowing callback and a successful mock server are insufficient ordinary-host evidence.

### Align policy and routing

Update MCP initialize instructions, skills, tool descriptions, and relevant adapter routing together. Direct knowledge questions to the intended existing retrieval, document jobs to their real owner, and code questions to UCI. Remove routine mandatory recall/store/outcome demands and stale literal-versus-semantic claims.

Reuse OpenClaw's supported task retrieval patterns where appropriate, without assuming host equivalence or changing the OpenClaw host. Existing HostAdvisor/TaskMemory remains available for its accepted consumers and guarantees; no revival of retired S3 or learned qualification is required.

For an affected HostAdvisor/TaskMemory path, retain the receipt, replay, budget, secret-custody, and authorization guarantees in [the product contract](spec.md#existing-authority-and-supersession). Keep onboarding, revocation, effective-state diagnostics, and data-preserving rollback checks on their existing owners; reuse compatible evidence when those paths are unchanged. Do not make a new console/onboarding campaign a prerequisite for an unrelated ready adapter, or send an existing receipt-governed body around its owner.

### Compose a useful task package

Resolve the actual principal/project/session and native code binding. From existing providers, select relevant memory or note evidence, exact code source, and a bounded set of callers/callees or a short path suited to the task.

Give the agent enough material to avoid predictable follow-up searches, plus stable references for targeted expansion. Keep source bytes and graph on the same View. Preserve current instruction authority and revision/provenance.

Provide the selected notes corpus through its canonical content owner. A metadata-only list or duplicated memory body is not document body search. If this producer needs work, name it as an A dependency while independently completing the ready code/policy components.

### Keep the ordinary path fast

Use deterministic symbol/lexical/graph work for named code. Precompute or incrementally update expensive projections outside the prompt path. Deduplicate repeated deliveries by context/revision, not by treating unrelated tasks as identical.

The existing OMP callback has a 500 ms total limit. Measure actual warm useful-result delivery inside that envelope; do not infer latency from empty queries. A provider that cannot meet the fast path must not silently consume the entire callback. Richer retrieval remains available through an explicit bounded follow-up.

### Execute real work and deliver

Run the applicable A1–A6 checks, including a Russian task and distinct worktrees. Prove automatic delivery through the real loaded host. Direct UCI operations retain their own supported paths and cannot by themselves prove automatic delivery. Fix causes within the same assignment. Preserve comparison evidence and report separately which query classes gained useful context.

Publish/install through the existing authorized process and repeat the promised ordinary path. A package on disk or a compiled helper is not proof of the loaded extension. Do not restart unrelated roots or create another browser/runtime merely to produce evidence.

## Delivery B: own linked knowledge for external clients

### Implement only the necessary context model

Extend the existing canonical principal and domain context so a trusted external client without a real project can operate in self. Keep explicit project behavior and explicit global separate. An invalid requested project is an error, not permission to change context.

Identity survives session and credential changes. Reuse current auth/keycards and visibility semantics. A new persona, provider account, per-session owner, generic IAM, or external assistant runtime is not part of this delivery.

### Use one existing content owner

Choose the current memory or versioned-document owner that satisfies stable references, useful body search, revisions, and lifecycle. Keep the chosen record as content authority; indexes and cross-provider result references are projections.

Expose save/read/revise/find through the supported client path. For uncertainty or retries, use the existing identity/idempotency contract. A separate card body store, dual writes, and copied editable notes are not acceptable shortcuts.

### Deliver usable links

**D2 caller-first decision:** Add a dedicated, narrow owner for user-authored memory-link records and receipts. Memory remains the sole card-body owner. Do not reopen the retired graph writer: `knowledge_edges` lacks revision pins, active logical uniqueness, and bounded authorization-before-limit; historical generic traversal has different direction and visibility semantics.

The first vertical slice serves self-memory cards through HTTP `POST` and `GET /api/memories/{source_id}/links` and `DELETE /api/memory-links/{link_id}`. Derive the enrolled canonical principal from the verified keycard. Create accepts `target_id`, `relation`, optional `target_version` for directed references, optional `request_key`, and optional `source_session_id` as untrusted provenance. Responses include the stable link ID, endpoint IDs, relation, optional version, canonical author, and original `source_session_id`; bounded lists return `links` and `next_cursor`.

Normalize an unordered `related_to` by stable endpoint IDs and reject revision pins for it. `references` is directional; distinct target versions are distinct assertions. A new link returns 201; the same active logical relation returns 200 with its original identity and provenance. Scope keyed receipts by canonical principal kind/principal and normalized operation, including normalized revision pins. Persist a receipt even when a duplicate create is a no-op. A changed payload under the same key conflicts; replay of an original since removed conflicts rather than creating or resurrecting a different link.

Check access and lifecycle for both endpoints before mutation and before applying list limits or cursors. The project-only `WithAuthorizedMemories` wrapper is not self authorization. Acquire stable ascending endpoint locks after the necessary actual table-lock prefix, and preserve workstation-private keycard restrictions. Links never grant access or alter ownership. Hide links with inactive endpoints without exposing their metadata; retain unremoved link records so an explicitly restored endpoint can make them visible after fresh authorization. Explicitly removed links remain removed. Keep these assertions distinct from automatic code relations; do not restore a manual graph editor or add a broad ontology.

Use an additive migration with data-preserving rollback. Select its final number only after B1, Feature012 ordering, and the current target ledger are known; never rewrite an applied migration. Extend only the selected auth-disabled HTTP credential routes for these endpoints; do not change the gRPC realm globally or claim privacy isolation under disabled auth.

Require test-first proof through the real router, including concurrency, retries, authorization, lifecycle, and revision pins, followed by fresh-session natural-query discovery and useful traversal. Versioned-document and other-record link endpoints remain an explicitly unsatisfied portion of AU-07; this slice does not claim their completion. No new IAM, scheduler, or full Feature011 dependency is required.

### Prove external continuity

Run B1–B4 with an ordinary generic client, using self by default. The later session belongs to the same canonical principal; another principal's access and explicit sharing are separate checks.

The client must find content from the follow-up question, follow a useful relation, and apply the result. A successful sequence of storage calls alone is not B acceptance. Update public API/operator documentation when implemented, keeping unshipped design fields clearly distinguished.

## Shared data and release work

Use the existing notes/Vault inventory and recovery owner. Verify C1 when storage, identity, access, or migrations are affected; reuse still-valid conservation evidence for unaffected domains rather than launching a new full-data campaign after every adapter change.

Preserve primary content and supported access. Rebuild derived indexes when necessary. Destructive effects retain their existing exact authority; this plan does not request general production cleanup.

Use focused implementation checks and the existing consolidated release boundary. Sonar follows the current adopted post-release policy. No new per-edit broad reviews, duplicate benchmark framework, scheduler, or acknowledgment ceremony is required.

## Work sizing and continuation

A coherent execution batch is approximately 6–8 hours where useful, not a fixed appointment, promise of completion, or automatic stop. Main identifies missing connections, assigns ready work to existing owners, integrates ready shared outputs, runs ordinary scenarios, and repairs concrete failures.

If a producer contract is stable but its implementation is pending, its consumer may proceed against that contract. Mocks prove only that layer. A blocked release effect holds that effect and preserves its resume condition; independent authorized implementation continues.

After each real accepted result, main advances to the next necessary part of A or B using current owners. A task return, report, commit, or source test is not the product completion boundary.

## Report at a meaningful checkpoint

State the ordinary consumer and task, which context actually arrived and was used, the correct result, saved exploratory work and added cost, installed versus source-only evidence, and the next concrete obstacle. Do not claim a universal token saving, semantic success from fallback, or private isolation from an auth-disabled environment.

## Existing document owners

Update the relevant ordering paragraphs in the constitution, Decision 19/22, and Feature 010. Clarify Decision 21's external-client boundary. Keep PRODUCT.md scoped to the operator console and Feature 011 scoped to its own experience. Update affected current task/checklist pointers through their existing owners; historical evidence is not rewritten.
