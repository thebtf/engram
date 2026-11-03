# Effective agent use acceptance

**Scope:** Working Agent Memory R1 amendment of 2026-11-02.
**Owner:** [Product contract](../spec.md); [implementation plan](../plan.md).

A is accepted for the promised development-task path only when an ordinary loaded client receives useful knowledge/code in time and uses it in correct work. B is accepted only when an external client's own linked knowledge survives a session boundary and helps the next task. An HTTP response, schema, expected substring, or mock callback is not that result.

Choose cases from real work before inspecting their answers. Keep instructions, source snapshot, model/provider configuration, and tool access comparable. Public fixtures contain neutral synthetic material or redistributable public sources; personal assistant data and secrets stay outside them.

For ordinary manual operator/memory acceptance, use the maintained populated local Docker copy of the owner's actual Engram data under the existing privacy and backup safeguards. A blank or memory-only fixture cannot establish useful retrieval from the selected valuable notes corpus. Synthetic isolated fixtures remain appropriate for deterministic security/error cases; public evidence remains neutral and secret-free. A fresh production export is not required for every run.

## A1: ordinary development task

**Job:** An ordinary coordinator assigns an actual queued code change without revealing the answer, correct file, or expected patch.

**Pass:** The installed session receives relevant project evidence and code/relationships from its own View before exploratory work; uses the material; and produces a correct change with the expected scope. A suitably bounded package removes identifiable searches or reads.

**Evidence:** Original task, context binding, actual delivered package, first useful actions, diff, independent behavior check/review, and a short account of avoided discovery work.

**Failure decision:** Wrong material means selection/context repair. Correct ignored material means delivery/policy repair. A correct patch with no observed benefit proves functionality only, not efficiency.

## A2: natural question without a known symbol

**Job:** Ask a real Russian question about behavior or a needed change without supplying exact identifiers. Include an English counterpart where language coverage is claimed.

**Pass:** The selected supported retrieval helps find the correct area and informs the task. An honest no-match or fallback keeps work usable but does not establish search usefulness for this query class.

**Evidence:** Candidates and sources, actual subsequent actions, answer/change correctness, active retrieval mode, and timing.

**Failure decision:** Fix the language/relevance provider or report that query class as not yet delivered. Do not add the answer's symbol to the prompt to manufacture success.

## A3: an existing note changes a decision

**Job:** An authorized successor session picks up earlier project work. A real preserved note records a constraint, established cause, or rejected approach from that work. Its text and ID are absent from the prompt.

**Pass:** The note is found and delivered before the relevant decision, its currentness/source is usable, and the agent avoids a known mistake or repeated investigation. A newer explicit instruction still takes precedence.

**Evidence:** Note reference/revision, delivery point, and the concrete changed action. A memory fixture does not substitute for the selected valuable note corpus.

**Failure decision:** Repair corpus/context selection, retrieval timing, or revision presentation. Preserve the original content instead of duplicating it into another editable store.

## A4: update and rename in a working copy

**Job:** Make a real small edit/rename affecting a call relation and then ask about that behavior.

**Pass:** After the normal supported update, source and relationships reflect the new version. Before that point, stale/unavailable state is honest. A full repository rebuild is not required for every edit, and source ranges are never silently applied to unrelated changed bytes.

**Evidence:** Change, indexed generation/View, query/read result, actual refresh delay, and ordinary update activity.

**Failure decision:** Repair missed updates, invalidation, or generation selection in the existing path; do not normalize manual full reindexing as routine recovery.

## A5: two independent worktrees

**Job:** Two sessions of one logical project work with intentionally different implementations of the same symbol.

**Pass:** Relevant shared project knowledge follows existing policy while each session receives its own code/View. Identical names cannot cross-contaminate source, graph, or caches.

**Evidence:** Both bindings, differing source, and useful query results in both sessions.

**Failure decision:** Repair native binding or cache/index keys. Do not invent separate logical projects to hide the worktree defect.

## A6: restraint, duplication, and unavailable retrieval

**Job:** Include a task with no useful memory/code context; repeated callbacks on unchanged context; and a bounded unavailable-provider case in an appropriate controlled environment.

**Pass:** Low-value context is omitted. Repeated packets do not accumulate unchanged material. An unavailable dependency does not hold ordinary work beyond the supported deadline. Empty relevance and technical failure remain distinguishable.

**Evidence:** Package sizes, callback timing, absence of duplicate content, and normal continued work.

**Failure decision:** Repair relevance admission, budgeting, deduplication, or fallback. Do not require memory use on every task to improve an exposure count.

## B1: an external client saves its own card without a project

**Job:** A generic existing client principal saves a useful card with source/provenance and no project selector.

**Pass:** The card belongs to that principal's self context, receives a stable reference, and is readable/revisable through the normal client. No fake project or public sharing is created.

**Evidence:** Neutral request/response, actual context/owner, and ordinary readback.

**Failure decision:** Implement or repair minimum self-context resolution and the authoritative writer. A global privacy flag on an arbitrary project is not project-free ownership.

## B2: fresh-session search and useful link traversal

**Job:** One session saves two meaningfully related own cards and links them. A fresh session of the same canonical principal receives a natural follow-up task without IDs, exact titles, or copied answers.

**Pass:** The client finds the useful card, follows the relation to additional relevant material, and applies both appropriately. The task does not require the user to transfer the content manually.

**Evidence:** Different sessions, stable principal, retrieved references and relation, correct result, and identifiable avoided rediscovery.

**Failure decision:** Exact-ID lookup is not task search. An edge present only in storage is not a usable link. Correct retrieval with no useful application requires reviewing card selection and the consumer behavior.

## B3: context and access remain correct

**Job:** Repeat own-card access in a new session and supported credential replacement; attempt access as another principal; pass an explicitly invalid project/context; exercise intentional sharing separately if it is in the release claim.

**Pass:** Own access follows the canonical principal, another principal does not gain unauthorized content or link metadata, and an invalid explicit context returns a defined error instead of falling back. Sharing does not occur merely because a card has a link or global affiliation.

**Evidence:** Neutral principals, ordinary calls, supported ownership/visibility results, and the actual authentication mode.

**Failure decision:** Repair existing principal/context and endpoint authorization. Auth-disabled execution cannot establish private isolation. Do not add a new general IAM to satisfy this case.

## B4: revisions, references, and retries

**Job:** Revise a card, repeat a save/link after an uncertain outcome using its supported request identity, and apply the supported retirement operation. Include a revision-specific reference and a stable conceptual related_to link.

**Pass:** Stable IDs/history survive, the current revision is clear, retries do not duplicate cards/edges, and the reference retains its intended meaning. A revision-specific reference to old text is not silently evidence for new text. Retired material is not presented as current without qualification.

**Evidence:** Supported operation sequence, returned IDs/revisions, link reads, and fresh-session search.

**Failure decision:** Repair the relevant writer/retry/lifecycle contract. Do not accept a second independently editable body as a shortcut.

## C1: valuable notes and Vault remain usable

**Job:** Apply necessary storage/context/update changes using the existing baseline and supported preservation/recovery process.

**Pass:** Previously retained notes and Vault items preserve meaningful content, identity, required history/provenance, and ordinary supported access. Corresponding valid keys still decrypt their records through the authorized path. Unknown historical key state is not a deletion predicate.

**Evidence:** Relevant original references, content/metadata hashes, note readback, and authorized Vault/recovery proof in the appropriate environment. Counts alone are insufficient; secret values do not enter reports, public fixtures, or model context.

**Failure decision:** Repair or use supported recovery before accepting the affected update. Rebuildable code/index/cache data need not retain its previous physical representation.

## Retained security and consumer guarantees

The eleven scenarios above replace the old blanket campaign, not its applicable safety invariants. Select affected checks within A6, B3, B4, and C1 and the existing consumer tests: authorization before ranking/LIMIT, metadata, and body exposure; exact-version/digest/ACL revalidation; revoked or superseded sources; malicious retrieved instructions; and absence of secrets in provider requests, diagnostics, receipts, and context. Current issues retain their own status and authorization; shared/global reuse never expands private access.

For an affected receipt-governed HostAdvisor/TaskMemory consumer, retain atomic first-winner commit-before-delivery, body-free EMIT replay, conflict handling for the same occurrence, and safe concurrent/lost/late-response behavior without same-turn authority fallback or duplicate receipts. Preserve its declared UTF-8 presentation budget and meaningful negations inside the actual host deadline. Source fixtures prove mechanics only; a server EMIT is not host presentation or useful application. Existing keycard issuance/custody/revocation and data-preserving rollback remain required for affected setup or release claims, not universal new prerequisites for unrelated A/B work.

## Measurement and interpretation

For A1, A2, A3, and B2, use a small set of meaningful tasks and a few comparable repetitions where claiming an efficiency difference. Isolate fresh sessions and do not leak answers from an earlier run. The baseline is the ordinary available workflow; never disable required access or correctness checks to make either side look better.

Record: correctness/completeness, time to first correct action and completion, exploratory calls and reads, retrieval latency, injected context, and token/cache/cost data when available. Keep cold indexing and warm queries separate. Model caching and Engram caching are different effects.

Use behavior, source-grounded reasoning, actual changes, and useful linked knowledge to judge results. Expected words or symbol names are insufficient. A useful small corpus supports a bounded claim about its cases, not a universal savings percentage.

## Selection and release evidence

Accept and release A and B independently under existing release policy: each needs ordinary consumer proof of its own full promised behavior. Both must preserve existing notes and Vault. During implementation, run only affected scenarios and relevant existing tests; unchanged preservation evidence may be reused with a stated compatibility basis. This is one acceptance set for these outcomes, not a requirement to repeat every scenario after every edit.

Source-ready, local integrated, published, installed, and observed useful remain separate states. A failed semantic case cannot be relabeled by lexical fallback. A local helper cannot stand in for the loaded host. A blocked independent scenario does not prevent useful work on the other delivery.

Keep observations in existing task/run/review records. Current release policy, including nonblocking post-release Sonar timing and all applicable integrity/authorization checks, remains authoritative.
