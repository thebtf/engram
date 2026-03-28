# Implementation Plan: Graph UX Polish

**Spec:** .agent/specs/graph-ux-polish/spec.md
**Created:** 2026-03-28

## Tech Stack

| Component | Choice | Rationale |
|-----------|--------|-----------|
| Graph library | vis-network v9.1.9 | Already installed, 2 working components |
| Styling | Tailwind CSS + vis-network options | Existing stack |
| API | `/api/observations/{id}/graph` | Already exists, multi-hop BFS |

## Phases

### Phase 1: Local Graph Mode (FR-1)

1. Add route parameter: `/graph/:observationId?` to vue-router
2. In GraphView.vue: if `observationId` param exists, fetch from `/api/observations/{id}/graph?depth=2`
3. Render anchor node as larger (scale 1.5) with highlighted border
4. Add depth selector dropdown (1/2/3) in toolbar — re-fetches on change
5. Add "Open in Graph" button/link to ObservationCard.vue

### Phase 2: Node Search (FR-2)

1. Add search input to GraphView toolbar (above the graph canvas)
2. On input: iterate vis-network DataSet, update node colors:
   - Matching: original color + highlight border
   - Non-matching: dimmed (opacity 0.3)
3. On Enter: `network.focus(firstMatchId, { scale: 1.5, animation: true })`
4. On clear: reset all node colors

### Phase 3: Visual Styling (FR-3)

1. Node options: `shadow: true`, `borderWidth: 2`, hover: `borderWidth: 3, color.border: '#fff'`
2. Edge options: `color` mapped from relation type, `smooth: { type: 'curvedCCW' }`, `dashes` for low-confidence
3. Background: subtle dot grid via CSS on container
4. Anchor node: pulsing animation via CSS `@keyframes` on overlay element (not vis-network — canvas-based)

## Files Modified

| File | Change |
|------|--------|
| `ui/src/views/GraphView.vue` | Local graph mode, search, styling |
| `ui/src/router/index.ts` | Optional `:observationId` param on graph route |
| `ui/src/components/ObservationCard.vue` | "Open in Graph" link |

## Constitution Compliance

| Principle | Status |
|-----------|--------|
| #14 Visual Verification | Required — screenshot each change |
| #12 Tool Budget | N/A — no MCP tool changes |
