# Tasks: Graph UX Polish

**Spec:** .agent/specs/graph-ux-polish/spec.md
**Plan:** .agent/specs/graph-ux-polish/plan.md
**Generated:** 2026-03-28

## Phase 1: Local Graph Mode (FR-1)

**Goal:** Click observation → centered graph view
**Independent Test:** Navigate to /graph/123 → shows graph centered on obs #123

- [x] T001 [US1] Add optional `:observationId` route param in `ui/src/router/index.ts`
- [x] T002 [US1] Add local graph fetch mode in `ui/src/views/GraphView.vue` — if observationId, fetch from `/api/observations/{id}/graph?depth=2`
- [x] T003 [US1] Render anchor node as larger (scale 1.5) with distinct border in `ui/src/views/GraphView.vue`
- [x] T004 [US1] Add depth selector (1/2/3) dropdown in graph toolbar in `ui/src/views/GraphView.vue`
- [x] T005 [US1] Add "View in Graph" link to `ui/src/components/ObservationCard.vue`

---

**Checkpoint:** Local graph mode works end-to-end.

## Phase 2: Node Search (FR-2)

**Goal:** Search input highlights matching nodes
**Independent Test:** Type in search → matching nodes glow, Enter focuses

- [x] T006 [US2] Add search input to graph toolbar in `ui/src/views/GraphView.vue`
- [x] T007 [US2] Implement node highlight/dim logic on search input in `ui/src/views/GraphView.vue`
- [x] T008 [US2] Focus camera on first match on Enter key in `ui/src/views/GraphView.vue`

---

**Checkpoint:** Search works within the graph.

## Phase 3: Visual Styling (FR-3)

**Goal:** Polished dark-theme graph
**Independent Test:** Graph looks professional (screenshot verification)

- [x] T009 [US3] Update vis-network node options (shadow, hover glow, border width) in `ui/src/views/GraphView.vue`
- [x] T010 [US3] Update edge options (relation type colors, curved edges, dashes for low confidence) in `ui/src/views/GraphView.vue`
- [x] T011 [US3] Add subtle dot grid background via CSS in `ui/src/views/GraphView.vue`
- [x] T012 Screenshot all changes for visual verification (Constitution #14)

## Phase 4: Release

- [x] T013 Create PR, run review, merge
- [x] T014 Tag v2.1.6 with release notes

## Dependencies

Phase 1 → Phase 2 → Phase 3 (sequential — all same file)

## Execution Strategy

- All work in `ui/src/views/GraphView.vue` + 2 small file changes
- One commit per phase
- Screenshot verification required (Constitution #14)
