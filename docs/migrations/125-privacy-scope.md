# Migration 125 — `privacy_scope` column on `memories` (engram vNext Milestone F TG1)

**Feature:** engram vNext Milestone F TG1 / CR-F1
**Migration ID:** `125_privacy_scope_addition`
**Source:** `internal/db/gorm/migrations.go`
**First touched in:** T001 (initial column + CHECK), T001 fix-forward (idempotent constraint + test cleanup), T006 (tag-derived backfill)
**Spec anchor:** `.agent/specs/engram-vnext-milestone-f/spec.md` §FR-F1 + §FR-F1 AMEND 2026-05-25
**Release target:** v6.5.0

## Schema delta

Migration 125 adds two columns and one CHECK constraint to the `memories` table:

| Column | Type | Constraint | Default |
|--------|------|------------|---------|
| `privacy_scope` | `TEXT` | `NOT NULL`, `CHECK (privacy_scope IN ('private', 'project', 'shared', 'global'))` | `'project'` |
| `source_sessions` | `TEXT[]` | `NOT NULL` | `ARRAY[]::TEXT[]` |

Both are added with `ADD COLUMN IF NOT EXISTS` so the migration is retry-safe. The CHECK constraint is dropped first (`DROP CONSTRAINT IF EXISTS memories_privacy_scope_chk`) and then re-added — matching the idempotency pattern of prior migrations such as `077_extended_relation_types`.

## Tag-derived backfill (T006)

After the column additions and the CHECK constraint, migration 125 runs a single `UPDATE` statement against rows that already exist in the table:

```sql
UPDATE memories
   SET privacy_scope = 'global'
 WHERE privacy_scope <> 'global'
   AND tags ? 'scope:global';
```

### Mapping rule

| Pre-migration row state | Post-migration `privacy_scope` |
|-------------------------|--------------------------------|
| `tags @> '["scope:global"]'`   | `'global'`  (set by the backfill UPDATE) |
| `tags @> '["scope:project"]'`  | `'project'` (column DEFAULT — no UPDATE needed) |
| `tags` is empty / no `scope:*` entry | `'project'` (column DEFAULT) |
| Pre-v5 row (no scope tag at all) | `'project'` (column DEFAULT) |

The legacy `scope:project` value matches the column DEFAULT, so it is intentionally NOT touched by the UPDATE. The reverse — promoting `scope:global` to `privacy_scope='global'` — is the only data movement migration 125 performs.

### Why tag-derived (not column-derived)

The legacy `Memory.Scope` field does NOT exist as a struct field or DB column. Verified against live source:

- `pkg/models/memory.go` — `Memory` struct has no `Scope` field (only the new `PrivacyScope` added in T002).
- `internal/db/gorm/models.go` — GORM-layer `Memory` struct same shape, no `Scope` column.
- `internal/mcp/tools_memory.go` — the MCP `store` create handler appends a `scope:<project|global>` entry to `Memory.Tags` for post-v5 writes.
- The response synthesizes a top-level `scope` field from the same tag for backward compatibility.

The scope identity has lived in tags + response synthesis since v5. Migration 125 maps from this tag surface to the new column once, on migrate-up.

### Idempotency

The backfill UPDATE is safe to run multiple times:

1. After the first run, every `scope:global`-tagged row has `privacy_scope='global'`.
2. The `WHERE privacy_scope <> 'global'` clause means the second run matches zero rows.
3. Re-running the migration is a gormigrate no-op anyway because the migration ID is recorded in the `migrations` tracking table — but the SQL itself is idempotent at the row level, which protects against manual re-execution and migration-tracking resets.

### Coverage

`internal/db/gorm/migrations_integration_test.go::TestMigration125_TagDerivedBackfill_T006` exercises the backfill end-to-end:

1. Seeds three rows (global-tagged, project-tagged, untagged), then forces the global-tagged row to `privacy_scope='project'` so the UPDATE has work to do.
2. Runs the backfill statement.
3. Asserts the three final `privacy_scope` values.
4. Re-runs the backfill statement.
5. Asserts the row state is identical (idempotency).

The test is gated on `DATABASE_DSN` per existing convention; CI must set the env var to exercise the assertion.

## RI-F2 dual-field response semantics

Per spec §FR-F1 REVISE + Release Invariants RI-F2:

> Legacy `Memory.Scope` field present in API responses (computed from `PrivacyScope`) for minimum 2 minor versions (earliest removal v6.7.0).

After migration 125 has run + the application is upgraded to v6.5.x, MCP/REST responses carry BOTH:

| Response field | Source | Notes |
|----------------|--------|-------|
| `scope`         | synthesized — `Memory.Tags ?  scope:*` or fallback `'project'` | Legacy 2-tier surface; preserved for 2 minor versions |
| `privacy_scope` | `Memory.PrivacyScope` column | New 4-tier authoritative source (when `ENGRAM_VNEXT_F_ENABLED=true`) |

Mapping at runtime (T004 wire-up):

- `privacy_scope = 'private'` — synthesized `scope` falls back to `'project'` because no legacy 2-tier representation exists for the private tier.
- `privacy_scope = 'project'` — synthesized `scope = 'project'`.
- `privacy_scope = 'shared'` — synthesized `scope = 'global'` (broadest visible value the legacy 2-tier enum supports).
- `privacy_scope = 'global'` — synthesized `scope = 'global'`.

The dual-field representation lets v6.4.x clients continue working unchanged while v6.5.x+ clients adopt the 4-tier vocabulary.

## Rollback

Migration 125's `Rollback` drops the constraint then both columns:

```sql
ALTER TABLE memories DROP CONSTRAINT IF EXISTS memories_privacy_scope_chk;
ALTER TABLE memories DROP COLUMN IF EXISTS source_sessions;
ALTER TABLE memories DROP COLUMN IF EXISTS privacy_scope;
```

The tag-derived UPDATE has no separate rollback: dropping the `privacy_scope` column removes the data the UPDATE produced. Legacy `scope:*` tags are NOT modified by the UPDATE and remain in `Memory.Tags` regardless of rollback.

## NFR-F2-bis lock duration

Per spec NFR-F2-bis: ≤ 50ms p95 `AccessExclusiveLock` on a 30M-row fixture under PostgreSQL 17. The ALTER TABLE column-add statements use catalog-only DDL on PG 11+ (literal DEFAULT, no row rewrite). The CHECK constraint addition does scan the table once but completes quickly on indexed-default values. The tag-derived UPDATE acquires a row-level lock on matched rows only — bounded by the count of `scope:global`-tagged rows in the seed data.

Pre-flight verification command (staging, before merge):

```sql
EXPLAIN (ANALYZE, BUFFERS)
ALTER TABLE memories
    ADD COLUMN IF NOT EXISTS privacy_scope TEXT NOT NULL DEFAULT 'project';
```

If the observed lock duration exceeds 50ms p95, the IF-WRONG directive on T001 (`tasks.md`) mandates splitting into three atomic steps (ADD COLUMN without DEFAULT + UPDATE batches + ADD CHECK).

## Related migrations

| Migration | Adds | Touched in |
|-----------|------|------------|
| `120_session_segments` | `session_segments` table | pre-existing — predecessor slot to Milestone F migrations |
| `125_privacy_scope_addition` | `privacy_scope`, `source_sessions`, CHECK, tag-derived backfill | T001 + T001 fix-forward + T006 |
| `130_source_workstation_id` | `source_workstation_id` | T001b (AMEND 2026-05-25 for FR-F1 keycard identity invariant) |

## Audit & evidence

- Spec amendment: `.agent/specs/engram-vnext-milestone-f/spec.md` §FR-F1 + AMEND block.
- Plan task entry: `.agent/specs/engram-vnext-milestone-f/plan.md` Phase 1 T-F1.6 (tag-derived inference).
- Tasks entry: `.agent/specs/engram-vnext-milestone-f/tasks.md` T006.
- Implementation log: `.agent/tasks/engram-vnext-milestone-f/T006/implementation-log.md`.
- Engram decisions: 2701 (T005); 2702 (T006).
