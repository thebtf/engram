# Glossary — Memory Product Layer

| Term | Meaning | Avoid / legacy synonym |
| --- | --- | --- |
| Memory | Atomic retained knowledge: fact, rule, preference, constraint, observation. | raw transcript, whole experience |
| Experience | Contextualized trajectory: situation, decision/action, outcome, revision, lesson, applicability conditions. | generic note, plain memory |
| State plane | Native Engram layer for session/goal/task/project handoff and execution state. | continuity file as primary truth |
| Resume protocol | Deterministic first-read state packet plus freshness/drift markers and exact next-action semantics for session recovery. | best-effort restore |
| Hot memory | Small always-hot retained memory that is cheap to inject or brief. | entire archive |
| Episodic stream | Raw transcripts/events/tool outcomes used for synthesis and audit. | final memory product |
| Semantic memory | Consolidated stable knowledge distilled from episodes and repeated use. | transient trace |
| Temporal truth | High-value evolving fact with validity window and provenance. | overwrite-only fact |
| Archive / cold tier | Valuable but low-frequency knowledge kept out of the hot path. | deleted memory |
| Archive resurfacing packet | Evidence card explaining why historical/archive retrieval was triggered and what old knowledge may matter now. | broad archive dump |
| Suppress | Hide from normal retrieval/injection while preserving auditability. | hard delete |
| Expire | Retention-driven removal of low-value or obsolete operational traces. | semantic forgetting |
| Consolidate | Merge overlapping memories into a stronger semantic memory, preserving provenance and checking for structural loss. | dedupe by deletion |
| Destroy | Irreversible removal of knowledge. Rare, explicit, and policy-gated. | default forgetting |
| Applicability | Whether a memory or experience is suitable under the current role/phase/environment/risk envelope. | relevance alone |
| Applicability envelope | Structured set of context fields used to judge whether past knowledge applies here. | loose similarity |
| Anti-applicability | Explicit conditions under which a memory/experience must not be auto-applied. | unstated exception |
| Principal | The identity that owns or is allowed to see certain memory/state. | generic user always |
| Domain | Specialty or ownership area for memory governance. | loose tag only |
| Moderation queue | Bounded operator review queue for risky, conflicting, or policy-sensitive memory decisions. | full memory dump |
| Exception surface | Operator-facing review surface for bounded high-risk or ambiguous cases only. | default operating mode |
| Policy packet | Aggregated decision brief for operator policy changes (thresholds, defaults, risk posture). | ad hoc manual tuning |
| Risky merge packet | Evidence card for a consolidation that may lose meaning or violate scope. | raw diff of rows |
| Rare escalation packet | Evidence card for cases automation cannot safely settle (privacy, destructive change, product-value conflict). | “there is something there” |

## Work-unit mapping

| Layer | Meaning |
| --- | --- |
| Milestone | MPL-1 .. MPL-6 product increments |
| Feature / Spec | `memory-product-layer` feature envelope |
| CR | Architecture and delivery slices generated later |
| Task group | bounded implementation or review package under a CR |
| Task | single executable unit |
