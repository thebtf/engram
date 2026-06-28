## Task T008 - Implementation Log

### Quoted AC
> - AC: touched UI matches contract controls and state behavior; no per-row "keep in prompts" semantics remain on touched principal-memory flow.
Source: `.agent/specs/memory-product-layer/changes/CR-001-initial-scope/tasks.md`

Supporting design contract:
> The surface can show live, empty, gated, stale, mustbuild, or error state based on the response.
Source: `.agent/specs/memory-product-layer/design-contracts/principal-memory-surface.md`

### User Change Enabled
Operators now have a contract-governed principal-memory surface on the memory page. It can select a principal/domain/project scope, refresh the live T006 `/api/memories/principal` substrate, inspect attributed knowledge, and see the principal-scoped brief panel as an honest MCP-only `mustbuild` gap until a browser REST bridge exists.

### Claim Grounding
- Claim: the touched UI uses the approved controls. Evidence: `memory.vue` renders stable controls for `principal-select`, `domain-select`, `project-scope`, `refresh`, `brief-refresh`, and `attribution-toggle`.
- Claim: the knowledge summary is live and scope-safe. Evidence: `useOperatorPrincipalMemorySurface` constructs `/api/memories/principal` queries with principal, principal kind, visibility, optional domain/project, private-read flag, and bounded `limit=10`.
- Claim: the brief panel is honest. Evidence: browser state for the brief uses `mustBuildState<OperatorPrincipalMemoryBrief>` with `MCP get_memory_brief` evidence; no browser fetch is attempted for a non-existent REST bridge.
- Claim: per-row prompt-retention copy was removed from the touched memory surface. Evidence: EN/RU/ZH locale text no longer contains the old "keep this record in prompts" / equivalent phrasing, and the seam contract checks that regression.

### Terminology Alignment
- "Principal memory surface" means the touched operator-console browser surface, not a new storage model.
- "Scoped brief" means the T007 MCP `get_memory_brief` path; browser UI marks it `mustbuild` because T008 did not add a REST bridge.
- "Risky confirmation" means the UI requires one confirmation before widening from one confirmed principal to another.

### Implementation Decision
Keep the existing row-centric memory workflow intact and add a separate principal-memory section above it. This preserves existing live delete/suppress/audit behavior while giving T008 a contract-clean surface with no queue UI, no forgetting/consolidation controls, and no mutation controls inside the principal-memory flow.

### Verification Result
AC-by-AC:
  - AC 1: [PASS] - touched UI renders the required controls, state banner, knowledge summary, scope evidence, and honest brief panel.
  - AC 2: [PASS] - EN/RU/ZH prompt-retention copy was removed and tested.

Commands:
  - RED: `npm run test:seam` failed on missing `PrincipalMemoryScope` / principal surface contract.
  - GREEN: `npm run test:seam` - PASS.
  - GREEN: `npm run build` - PASS.
  - GREEN: `npm run test:browser -- tests/browser/memory-actions.spec.ts` - PASS.

Overall: [PASS]

### NEEDS_CLARIFICATION (if AMBIGUOUS result)
N/A
