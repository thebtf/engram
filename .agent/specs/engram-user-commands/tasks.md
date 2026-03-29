# Tasks: Engram Plugin User Commands

**Spec:** .agent/specs/engram-user-commands/spec.md
**Plan:** .agent/specs/engram-user-commands/plan.md
**Generated:** 2026-03-28

## Phase 1: Create Command Files

**Goal:** 4 new markdown command files
**Independent Test:** Each command visible in CC `/engram:` menu and produces output

- [x] T001 [P] [US1] Create `/engram:retro` command in `plugin/engram/commands/retro.md`
- [x] T002 [P] [US2] Create `/engram:stats` command in `plugin/engram/commands/stats.md`
- [x] T003 [P] [US3] Create `/engram:cleanup` command in `plugin/engram/commands/cleanup.md`
- [x] T004 [P] [US4] Create `/engram:export` command in `plugin/engram/commands/export.md`

---

**Checkpoint:** All 4 files exist. `go build` not needed (markdown only).

## Phase 2: Release

- [x] T005 Create PR, run review, merge
- [x] T006 Bump version, tag release

## Dependencies

T001-T004 all parallel (different files). T005 depends on all.

## Execution Strategy

- All 4 commands are independent markdown files — create in parallel
- One PR for all 4
- Commit per file or batch commit
