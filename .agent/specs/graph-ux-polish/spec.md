# Feature: Graph UX Polish

**Slug:** graph-ux-polish
**Created:** 2026-03-28
**Status:** Draft

## Overview

Enhance the existing GraphView.vue knowledge graph with local graph mode, node search,
and visual styling improvements. vis-network v9.1.9 already installed and working in
two components (GraphView.vue full-page + RelationGraph.vue modal).

## Context

### What Exists
- `ui/src/views/GraphView.vue` (443 lines) — full-page force-directed graph
  - Loads top 50 observations, fetches their relations in parallel batches
  - barnesHut physics, dot-shaped nodes sized by importance
  - Relation type filter sidebar, hover tooltips, click navigation
- `ui/src/components/RelationGraph.vue` — modal view for single observation
  - forceAtlas2Based physics, box nodes color-coded by type
  - Edge labels by relation type, double-click navigation
- 17 relation types in PostgreSQL with confidence scores
- `/api/observations/{id}/graph?depth=N` endpoint with multi-hop BFS

### Gap
Current graph is functional but basic. No node search, no local-graph-centered mode,
no visual polish (glow effects, dark theme optimization).

## Functional Requirements

### FR-1: Local Graph Mode
When user clicks an observation in any dashboard view, show a centered graph around
that observation with configurable depth (1-3 hops). Use existing `/api/observations/{id}/graph`
endpoint. Radial or force layout with the anchor node visually distinct (larger, highlighted).

### FR-2: Node Search Within Graph
Add a search input in the graph toolbar. As user types, matching nodes highlight (pulse/glow)
and non-matching nodes dim. Search matches against observation title and type.

### FR-3: Visual Styling
- Nodes: glow effect on hover, pulsing anchor node, smooth transitions
- Edges: animated dash pattern for active relations, color by relation type
- Dark theme optimization: brighter node colors, subtle grid background
- prefers-reduced-motion: disable animations

## Non-Functional Requirements

### NFR-1: Performance
Graph must render smoothly with up to 200 nodes. Use vis-network clustering for >200.

### NFR-2: No New Dependencies
Use vis-network (already installed). No new npm packages.

## User Stories

### US1: Local Graph Navigation (P1)
**As a** user viewing an observation, **I want** to see its knowledge graph neighborhood,
**so that** I can explore related observations visually.

**Acceptance Criteria:**
- [ ] Click observation anywhere in dashboard → graph view centered on it
- [ ] Anchor node visually distinct (larger, highlighted border)
- [ ] Depth selector (1/2/3 hops)

### US2: Search in Graph (P1)
**As a** user exploring a large graph, **I want** to search for specific nodes,
**so that** I can find observations without scrolling/zooming.

**Acceptance Criteria:**
- [ ] Search input in graph toolbar
- [ ] Matching nodes highlight, non-matching dim
- [ ] Enter key focuses camera on first match

### US3: Visual Polish (P2)
**As a** user, **I want** the graph to look polished and professional,
**so that** it's pleasant to use.

**Acceptance Criteria:**
- [ ] Hover glow effect on nodes
- [ ] Edge colors match relation type
- [ ] Dark theme optimized colors

## Edge Cases

- Graph with 0 relations → show isolated node with "No relations found" message
- Search with 0 matches → show "No matches" indicator, don't change graph
- Very deep graph (depth 5) → warn about load time, cap at 500 nodes

## Out of Scope

None.

## Dependencies

- vis-network v9.1.9 (already installed)
- `/api/observations/{id}/graph` endpoint (already exists)

## Success Criteria

- [ ] Local graph mode works from observation click
- [ ] Search highlights matching nodes
- [ ] Visual styling improved (verified via screenshot — Constitution #14)
