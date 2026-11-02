# Working Agent Memory R1: effective use by agents and external clients

**Status:** Accepted product direction; implementation and installed acceptance remain scenario-specific.
**Revision:** 2026-11-02.
**Authority:** This public specification, the active constitution, and Decisions 18–22. Dated operational packets supply evidence and execution context; they do not create a second live product contract.

## Outcome

Engram improves ordinary work by delivering relevant prior knowledge and current code context when a consumer needs them, and by making useful results reusable in later sessions. An ordinary development agent reaches a correct action with less repeated investigation. An external knowledge client can maintain its own linked knowledge cards across sessions without inventing a project.

A running server, stored record, populated index, successful tool call, or matching symbol name is intermediate evidence. Acceptance concerns correct completed work and useful knowledge reuse, including the costs of retrieval, injected context, stale information, and maintenance.

## Product boundary

Engram supplies memory, knowledge storage and retrieval, code intelligence, and supported consumer adapters. External assistants remain external applications. Engram does not create or host their personalities, prompts, conversation runtimes, provider accounts, planning loops, or private workflows.

An assistant principal is a durable external-client identity for existing access and record-context rules. A registry entry describes that identity; it is not an assistant runtime or a second account system. Public requirements, examples, fixtures, and release notes must be independent of any named personal assistant.

The first reference adapter for automatic development context is the ordinary installed OMP/native integration. The external-client scenario uses a supported public client path. Neither reference choice makes all hosts equivalent or permits changes to an external personal assistant.

## Consumers and ordinary jobs

### Development session

A user request or coordinator assignment starts a real task. The session receives useful project knowledge and a compact code packet from its actual working copy before repeated exploratory searches. It can expand a specific gap through existing tools, make the correct change, and preserve a durable finding when one exists.

### Coordinator or successor session

A session retrieves the relevant accepted decision, its source, and any material superseding information before planning work. A later session can reuse an established cause or decision without the user retelling the history. Retrieved text is evidence and never overrides current instructions.

### External knowledge client

A client saves its own useful cards, begins a fresh session under the same canonical principal, finds relevant cards from a natural working question, follows their links, and reuses the material. Own/self context is the default for this job. Project or global affiliation is an explicit separate choice; it does not make content public or grant another principal access.

### Operator

The existing operator surface makes effective context, backing capability, freshness, and meaningful failures understandable. It remains governed by Feature 011 and PRODUCT.md. A console redesign is not a prerequisite for working agent or external-client paths.

## Contract

### AU-01: context follows the actual task

Automatic retrieval receives the current task, a meaningful new question, or a defined topic change. Session start may supply compact current state and applicable policy; an unfiltered recent-memory dump does not substitute for task retrieval.

Delivery must occur in the actual supported host before the relevant decision. Receipt by a helper script alone does not prove delivery to that host.

### AU-02: identity follows each domain

The authenticated principal, record author, owning context, session, and code working copy are distinct. Existing project routing uses the canonical project UUID. Code selection uses authorized Source, Checkout, and pinned View. Project identity must not silently select a different worktree.

A record has one owning context: self/assistant, project, or explicit global. When no context or real project is selected, a trusted registered external principal may resolve to self. An explicitly invalid project or context returns a defined error; it must not silently become self or global. Existing visibility and authorization policies remain effective.

### AU-03: one useful bounded package

Select the smallest sufficient combination of relevant memories or notes, exact symbols, source excerpts, and relationships for the question. No task is required to use every provider. Include stable references, relevant revisions, provenance, selection reason, and freshness sufficient for targeted follow-up.

For an implementation question, include the relevant definition and source. For impact, prefer affected callers and appropriate tests. For a flow question, provide a short connected path. Do not dump an entire graph or force a separate tool turn for every dependency.

### AU-04: fast code navigation uses existing UCI

Reuse Feature 010 search, automatic relationships, and versioned reads. A named-symbol task may use a deterministic lexical/symbol/graph path without waiting for an embedding provider. Natural-language and semantic scenarios retain their own quality requirements; a lexical fallback must not be reported as semantic success.

Search, graph facts, and source delivered together must refer to the same View. Changes and renames must enter the existing incremental update path. The task request must not trigger a full repository rebuild or read changed files using unchecked stale source ranges.

### AU-05: useful notes are actually discoverable

Versioned notes/documents and memories retain their existing content owners. A successful memory search does not prove search over all notes. Supply task-relevant note discovery over the selected existing content, with revision and source references.

A direct note list/read/history path is a valid explicit consumer operation, but does not establish automatic body search. If a needed corpus provider is unavailable, expose that limitation and retain the record; do not create a second independently editable memory copy to simulate note retrieval.

### AU-06: external cards use one canonical content owner

A knowledge card is a useful product form of an existing memory or versioned note/document, not a third independent body store. Its ordinary public path supports save, read, revise, find, link, and supported retirement.

Each card exposes a stable reference, current revision, useful content, owning context, author/provenance, and lifecycle state. A natural working question must find relevant own cards without exact IDs, filenames, or copied answer strings. English and Russian are required initial acceptance languages; supported-language claims must remain explicit.

The implementer must select and record the existing content owner that satisfies these properties. Other stores keep their own records and can be retrieved through common typed references.

### AU-07: knowledge links are usable records

The initial contract supports related_to, a symmetric conceptual association, and references, a directed reference to another card or record. These are proposed domain relation names; transport/API names are published when implemented.

A link identifies its endpoints and relation kind, preserves creation provenance, and is available through ordinary create/remove/list and bounded neighbor traversal. A citation to a particular internal claim identifies the referenced revision. Conceptual associations may follow stable record IDs. Updating content must not silently transform an old revision-specific reference into evidence for the new text.

Creating the same logical link twice must not produce duplicate edges. A link never changes ownership or grants access. Every returned endpoint and edge is subject to the existing domain policy; inaccessible endpoints must not leak through traversal or result metadata.

These are deliberate knowledge assertions. They are separate from automatic code calls/imports and do not restore the retired manual graph editor or old graph-writer runtime. Broader ontologies, automated truth inference, and supports/contradicts scoring are not prerequisites.

### AU-08: continuity without accidental duplication

A new session and a replacement credential for the same canonical principal retain the intended record access without moving or copying the records. Revisions preserve stable identity, supported history, and usable references. Superseded or archived material must not appear as current knowledge without qualification.

Retries follow the existing write/idempotency contract. An uncertain write is reconciled through its stable identity or supported request mechanism; a blind repeat must not create a second card or relation.

### AU-09: host instructions agree with runtime

MCP initialize instructions, installed skills, tool descriptions, and host adapters must direct each job to a real supported path. Memory tools must not masquerade as code search, and literal matching must not be advertised as semantic retrieval.

Use injected evidence when sufficient and request more only for a real gap. There is no mandatory recall before every file edit, no compulsory memory after every routine task, and no fabricated positive outcome. Capture accepted durable findings deliberately. Record actual supported exposure/outcome evidence without treating task success or absent citations as proof of each item's utility.

### AU-10: bounded failure and recovery

The chosen host has an explicit total retrieval deadline and context-size budget. Preserve the existing ordinary OMP 500 ms callback limit as the initial constraint unless a measured, documented product decision changes it. Do not claim success by silently extending timeouts.

No relevant result, unavailable provider, stale view, and invalid context are distinct observable conditions. Low-value questions receive no unnecessary package. Unavailability does not hold normal work beyond the supported deadline. Repeated callbacks deduplicate unchanged content. An appropriate empty result is successful restraint; it is not a claim that a fact does not exist.

### AU-11: preserve the user's valuable data

Existing notes and Vault are primary retained assets. Preserve useful content, stable identity, necessary revisions/provenance, and ordinary authorized access. Notes must remain readable; Vault must remain usable with its corresponding valid key and supported recovery path. Ciphertext rows or aggregate counts alone do not prove this.

Code chunks, embeddings, automatic graph projections, and caches are rebuildable. Retention of every historical runtime mechanism or derived index is not a product requirement. This specification does not authorize destructive cleanup of other owner data.

### AU-12: measure completed work

Evaluate correct results, time to the first correct useful action and completion, exploratory calls and source reads, retrieval latency, injected context, and actual cost where known. Distinguish model-cache usage from Engram caches, and cold construction from warm queries.

Use a few representative actual tasks and ordinary evidence. Do not force memory participation on every task or create an independent benchmark/telemetry platform as a prerequisite. No external project's percentage saving is an Engram promise.

## Delivery boundaries and dependencies

**A — useful development task context.** Repair the ordinary prompt path and loaded policy; compose existing project knowledge and UCI; connect the selected notes corpus; prove a real development task and later reuse. It depends on working domain resolution and selected providers, not completion of all Assistant, Book Context, Feature 011, or unrelated task-field work.

**B — own linked knowledge across sessions.** Deliver the minimum Decision 21 self-context and principal mapping, an existing content owner with real search, and the knowledge-link contract. Prove save, fresh-session find, link traversal, revise, and reuse. It does not require a new assistant runtime, code graph, or broad IAM.

A and B may proceed independently under existing owners and share stable domain primitives. Each delivered claim requires its ordinary consumer evidence. Partial producer readiness must remain explicit and must not turn an API-only demonstration into complete A or B acceptance.

## Existing authority and supersession

This amendment updates Feature 009's outcome, contract, and delivery order. Feature 010 remains owner of UCI semantics and worktree correctness; Feature 011 remains owner of the operator experience. Decision 21 remains a design contract until its runtime is delivered.

Decision 18's authorization-before-ranking, exact materialization, redaction, bounded delivery, replay where applicable, no same-turn authority fallback, and immutable history remain. A complete learned intervention/qualification pipeline or exclusive use of an unconnected historical adapter is not required for ordinary task context. Existing supported HostAdvisor/TaskMemory consumers retain their own contracts.

For supported HostAdvisor/TaskMemory consumers, `context_reference` remains reference delivery, not `eligible`/`canary` qualification. A retrieval score is neither a probability of truth nor trusted evidence of an intervention's effect. Preserve their typed occurrence/subject/scope, versioned source references, integrity, capability negotiation, and existing receipt owner; do not fabricate policy snapshots, utility evidence, receipts, or a parallel delivery engine.

Authorize before candidate ranking/LIMIT and before exposing metadata or bodies; revalidate exact version, digest, lifecycle, and ACL at materialization. Current issues retain their own domain, revision, status, and access checks, including an authorized issue-only result where supported. Already-authorized shared/global knowledge may be reused without changing the request's canonical project or expanding private access. Retrieved content remains untrusted evidence, cannot execute commands or promote itself into policy, and must preserve meaningful spans, Unicode boundaries, and material negations. Secrets stay out of prompts, provider requests, diagnostics, and receipts; raw master credentials and keycards stay outside the extension.

Where the receipt protocol applies, preserve atomic commit-before-delivery and first-winner semantics. Exact replay after EMIT does not return the body again; changed facts for the same occurrence cannot manufacture a new receipt. Concurrent requests, lost responses, late delivery, and reconnects do not permit double delivery, post-selection fallback, or a second receipt path. UNAVAILABLE need not have a durable receipt when persistence fails; an EMIT receipt proves a server decision, not host presentation. Rollback stops the affected route for future turns while retaining data and immutable history.

Retain the existing `context_reference` consumer's bounded presentation contract: at most one knowledge/experience and one issue section, 2048 UTF-8 bytes including framing, and a 768-byte target per source excerpt. Its 2 s server hard cap never overrides a smaller demonstrated host deadline, including the ordinary OMP 500 ms callback limit. A change to that consumer's limits or wire shape requires an explicit owner amendment and compatible consumer evidence. These limits are not a blanket shape for every A/B package. Retaining this contract is not a claim that this documentation branch implements or installs it.

The former blanket sequence of full Feature 011, then Book Context, then Working Agent Memory R1 is superseded by the consumer dependencies above. The narrow knowledge-link operation is an explicit new outcome and does not revive retired graph products.

## Acceptance and references

[Acceptance scenarios](acceptance/agent-use.md) define behavioral evidence. [Implementation plan](plan.md) assigns coherent deliveries and continuation. Existing release, data integrity, review, installed verification, and current Sonar timing policy remain governed by the active constitution and release protocol.

Design references include the pinned Codedict [hook](https://github.com/Marqueos/codedict/blob/3a047064e61a1cab3a68ec780ba2653ad2d359ae/codedict/claude_code.py), [query package](https://github.com/Marqueos/codedict/blob/3a047064e61a1cab3a68ec780ba2653ad2d359ae/codedict/query.py), and [benchmark/harness](https://github.com/Marqueos/codedict/blob/3a047064e61a1cab3a68ec780ba2653ad2d359ae/bench/ab_test.py). The transferable ideas are same-turn context and fewer discovery steps; its performance claims, tokenizer, index replacement, and completeness assumptions are not adopted.
