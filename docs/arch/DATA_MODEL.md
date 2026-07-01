# Data Model

## Overview

PostgreSQL 17 with pgvector + pgvectorscale extensions. Schema managed by
gormigrate. Tables are created via raw `CREATE TABLE` DDL or `AutoMigrate`,
followed by explicit DDL for pgvector columns, FTS indexes, and constraints.

The authoritative live-table list and exact migration/table counts are
**generated** from `internal/db/gorm/migrations.go` into the generated-tables
block at the bottom of this file. Regenerate after any schema migration with:

```
go run ./tools/gen-data-model
```

The `datamodel_drift_test.go` guardrail fails if the committed block falls out of
sync with the migration list, so the counts never silently rot.

> **Note (v5 demolition in progress):** several `observation_*` tables and other
> pre-vNext tables still appear as live in the generated block but are slated for
> removal by the `provenance-cleanup` epic (CR-2/CR-3). They are tracked by the
> CR-0 provenance guardrails (`provenance_lint_test.go`, `schema_integrity_test.go`)
> until dropped. Do not build new code against them.

## Key Schema Patterns

- **FTS:** `tsvector` columns with GIN indexes on `memories.content`, search fields.
- **Vector search:** pgvector `vector(N)` columns on `content_chunks` with HNSW index (cosine distance). pgvectorscale for 4096-dim embeddings (beyond HNSW 2000-dim limit).
- **Soft delete:** `is_superseded`, `is_archived` flags on memories. `active` flag on documents.
- **Scoping:** `project` + `scope` columns for multi-tenant isolation. Global scope crosses project boundaries.
- **Timestamps:** dual `created_at` (text/timestamptz) + `created_at_epoch` (bigint). The epoch column is a legacy pattern from the SQLite era; both are maintained for backward compatibility with existing queries and API contracts.

## Migration History

gormigrate migrations run automatically on server startup, broadly spanning:
- Core tables and indexes (001–019)
- FTS and vector search setup (020–040)
- Pattern/graph system (added then removed in v5)
- Session and telemetry tracking (050–070)
- Credential vault and encryption (071–090)
- Issue tracker + auth (070–080)
- v5 table drops and cleanup (083–110)
- vNext: injection/citation logs, knowledge graph, crystallization, transcripts (106–135)

The exact migration count is in the generated block below. Migrations are
irreversible by design — rollback requires manual SQL or a backup restore.

## Tables

The list below is generated. Do not edit it by hand; run `go run ./tools/gen-data-model`.

<!-- BEGIN GENERATED TABLES -->
Generated from `internal/db/gorm/migrations.go`.

Migration count: **136**.

Live table count: **36**.

| Table | Creating migration |
| --- | --- |
| `sdk_sessions` | `001_core_tables` |
| `content` | `017_content_addressable_storage` |
| `documents` | `017_content_addressable_storage` |
| `telemetry_snapshots` | `026_telemetry_snapshots` |
| `projects` | `030_projects_table` |
| `api_tokens` | `036_api_tokens` |
| `search_query_log` | `037_search_query_log` |
| `retrieval_stats_log` | `038_retrieval_stats_log` |
| `system_config` | `050_system_config` |
| `versioned_document_comments` | `051_documents` |
| `versioned_documents` | `051_documents` |
| `issue_comments` | `070_agent_issues` |
| `issues` | `070_agent_issues` |
| `invitations` | `080_create_auth_tables` |
| `sessions` | `080_create_auth_tables` |
| `users` | `080_create_auth_tables` |
| `credentials` | `087_credentials` |
| `memories` | `088_memories` |
| `behavioral_rules` | `089_behavioral_rules` |
| `injection_log` | `106_injection_log` |
| `citation_log` | `107_citation_log` |
| `content_chunks` | `108_content_chunks` |
| `promotion_log` | `112_promotion_log` |
| `knowledge_edges` | `113_knowledge_edges` |
| `audit_log` | `115_audit_log` |
| `session_segments` | `120_session_segments` |
| `knowledge_nodes` | `126_knowledge_nodes_table` |
| `crystallization_candidates` | `132_crystallization_candidates` |
| `bulk_op_snapshots` | `133_bulk_op_snapshots` |
| `session_transcripts` | `135_session_transcripts` |
| `code_chunks` | `139_code_chunks` |
| `code_index_sessions` | `140_code_index_sessions` |
| `model_settings` | `143_model_settings` |
| `rule_injection_events` | `146_rule_injection_events` |
| `agent_project_state` | `152_agent_state_plane` |
| `agent_session_state` | `152_agent_state_plane` |
<!-- END GENERATED TABLES -->
