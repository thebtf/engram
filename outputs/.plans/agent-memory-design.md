# Research Plan: agent-memory-design

## Questions
1. What explicit product requirements for Engram memory follow from the operator brief?
2. What mechanisms for remembering, ranking, forgetting, consolidation, and handoff are already present in Engram code/docs/roadmap?
3. How do Memento, Hermes memory, Cognee, Mem0, RLM, Graphiti, Hindsight, and ByteRover differ in memory architecture?
4. Which ideas from those systems are compatible with Engram, and which conflict with Engram’s honesty, auditability, and operator-light governance goals?
5. What phased ideal architecture should Engram adopt for agent memory and native handoff?

## Strategy
- Round 1: extract Engram requirements and current-state mechanisms from repo specs, code, and roadmap.
- Round 2: analyze local reference repos and papers under D:\Dev\_EXTRAS_ plus targeted current web sources.
- Round 3: synthesize comparison matrix, contradictions, and recommended phased architecture.

## Acceptance Criteria
- [ ] Explicit memory-product requirements are stated and grouped by capability.
- [ ] Current Engram mechanisms for ranking/forgetting/consolidation/handoff are mapped.
- [ ] Each external reference contributes concrete mechanisms, not generic praise.
- [ ] Critical claims have at least two independent sources or are labeled single-source.
- [ ] Final brief recommends a phased memory architecture and native handoff direction.

## Task Ledger
| ID | Owner | Task | Status | Output |
|---|---|---|---|---|
| T1 | lead | Extract product requirements from operator brief + roadmap | in_progress | requirement set |
| T2 | lead | Audit current Engram memory mechanisms | todo | current-state map |
| T3 | lead | Analyze local reference systems | todo | per-system notes |
| T4 | lead | Analyze papers / external articles | todo | source-backed claims |
| T5 | lead | Synthesize ideal architecture and phased roadmap | todo | final brief |

## Verification Log
| Item | Method | Status | Evidence |
|---|---|---|---|
| Memory requirements | operator brief + roadmap cross-read | pending | chat brief, roadmap goal |
| Existing Engram mechanisms | code/spec grep + targeted reads | pending | internal code/spec paths |
| External reference claims | repo README/pdf + web sources | pending | _EXTRAS_ paths + URLs |
| Final architecture recommendation | multi-source synthesis | pending | final report |
