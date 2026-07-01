# Provenance — agent-memory-design

- Topic: Comprehensive research for Engram agent-memory design, including remembering, ranking, forgetting, consolidation, and native handoff/state transfer.
- Date: 2026-06-27
- Plan artifact: `outputs/.plans/agent-memory-design.md`
- Final artifact: `outputs/agent-memory-design.md`

## Evidence Used

### Engram local sources
- `internal/injection/injection.go` — Thompson Sampling selection and injection tracking.
- `internal/retrieval/scoring.go` — composite recall scoring with reinforcement blend.
- `internal/feedback/updater.go` — outcome-modulated posterior updates.
- `internal/lifecycle/decay.go` — reconsolidation / retrievability formulas.
- `internal/mcp/tools_memory.go` — retrieval reconsolidation and suppress path references.
- `internal/mcp/tools_brief.go` — current `get_memory_brief` implementation.
- `internal/worker/dream_cycle.go` — transcript-backed dream-cycle crystallization.
- `internal/worker/retention.go` — log/audit retention cleanup.
- `pkg/cognitive/types.go` — `SessionStateSlots`, `ProjectStateRecord`, `AttentionEventRecord`, directive-distillation types.
- `pkg/cognitive/interfaces.go` + grep results — `StateWriter` / `DirectiveDistiller` still mostly interface/noop/test-level.
- `.agent/goals/2026-06-23-engram-roadmap-stabilization-marathon.md` — roadmap order and Memory Product Layer slot.
- `.agent/specs/principal-memory-query-domain-registry/prd.md` and `spec.md` — existing PMQ scope and explicit deferrals.
- `.agent/specs/operator-console/spec.md` and `user_job_statement.md` — operator-console contract and operator pain.
- `.agent/reports/retro-sessions-2026-06-24.html` — compact-recon/root-cause analysis motivating native handoff.
- `.agent/session-state/current.json` and `.agent/CONTINUITY.md` — current filesystem-based oracle/handoff reality.

### Local external reference repos under `D:\Dev\_EXTRAS_`
- `memento/README.md` and `memento.pdf` pages 1-6.
- `mem0/README.md`, CLI spec/docs greps.
- `cognee/README.md`, `CLAUDE.md`, API/config greps.
- `graphiti/README.md` and repo greps.
- `hindsight/README.md`, `CLAUDE.md`, skill/docs greps.
- `byterover-cli/README.md` and `paper/README.md`.
- `RLM/README.md` (local CLI implementation inspired by RLM ideas, not the original paper).

### Current web sources
- Prime Intellect article: `https://www.primeintellect.ai/blog/rlm`
- RLM paper (arXiv HTML): `https://arxiv.org/html/2512.24601v1`
- Hermes article: `https://www.glukhov.org/ai-systems/hermes/hermes-agent-memory-system`
- Hermes provider comparison: `https://www.glukhov.org/ai-systems/memory/agent-memory-providers`
- Microsoft/AutoGen memory architecture discussion: `https://github.com/microsoft/autogen/discussions/7794`
- Memento repo page: `https://github.com/Memento-Teams/Memento`
- Graphiti paper/repo references from local README
- General memory survey references surfaced by Tavily for cross-checking categories and forgetting competencies.

## Verification Notes

- Claims about current Engram mechanisms are backed by local code/spec reads from this session.
- Claims about external systems are backed by either local repo docs, current public articles, or both.
- Where a system’s README is product/marketing heavy, I treated concrete mechanisms (API verbs, storage model, retrieval modes, memory types) as evidence and avoided unsupported claims.
- The RLM and Memento material was treated as context-management / context-folding architecture, not as drop-in persistent agent-memory products.
- Some comparisons remain single-source where only the vendor README/article expressed the design plainly; those were not escalated into benchmark-grade certainty claims beyond what the source stated.

## Open Gaps

- I did not yet inspect every internal implementation file of `mem0`, `cognee`, `graphiti`, `hindsight`, or `byterover-cli`; this brief is architecture/comparison-focused, not a full code audit of those projects.
- The local `RLM` repo under `D:\Dev\_EXTRAS_\RLM` appears to be a CLI/tooling implementation around the RLM idea rather than the canonical paper repo; I used it only as a supplemental signal.
- For Memento, the contribution is deliberately limited to context-folding / internal state compression because the available sources do not position it as a persistent agent-memory governance system.
