// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// runMigrations runs all database migrations using gormigrate.
func runMigrations(db *gorm.DB, sqlDB *sql.DB) error {
	// Enable pgvector extension before running any migrations.
	// CREATE EXTENSION IF NOT EXISTS is idempotent.
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return fmt.Errorf("enable pgvector extension: %w", err)
	}

	m := gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{
		// Migration 001: Core tables (SDKSession, Observation, SessionSummary)
		{
			ID: "001_core_tables",
			Migrate: func(tx *gorm.DB) error {
				// AutoMigrate creates tables with all indexes from struct tags
				if err := tx.AutoMigrate(&SDKSession{}); err != nil {
					return err
				}
				if err := tx.AutoMigrate(&Observation{}); err != nil {
					return err
				}
				return tx.AutoMigrate(&SessionSummary{})
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable("sdk_sessions", "observations", "session_summaries")
			},
		},

		// Migration 002: User prompts table
		{
			ID: "002_user_prompts",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&UserPrompt{})
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable("user_prompts")
			},
		},

		// Migration 003: Full-text search for user prompts via tsvector (PostgreSQL).
		// Replaces SQLite FTS5 virtual table with a generated tsvector column + GIN index.
		{
			ID: "003_user_prompts_fts",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE user_prompts
					 ADD COLUMN IF NOT EXISTS search_vector tsvector
					 GENERATED ALWAYS AS (
					   to_tsvector('english', COALESCE(prompt_text, ''))
					 ) STORED`,
					`CREATE INDEX IF NOT EXISTS idx_user_prompts_fts
					 ON user_prompts USING GIN(search_vector)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return err
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					"DROP INDEX IF EXISTS idx_user_prompts_fts",
					"ALTER TABLE user_prompts DROP COLUMN IF EXISTS search_vector",
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						continue
					}
				}
				return nil
			},
		},

		// Migration 004: Full-text search for observations via tsvector (PostgreSQL).
		{
			ID: "004_observations_fts",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE observations
					 ADD COLUMN IF NOT EXISTS search_vector tsvector
					 GENERATED ALWAYS AS (
					   to_tsvector('english',
					     COALESCE(title, '') || ' ' ||
					     COALESCE(subtitle, '') || ' ' ||
					     COALESCE(narrative, '')
					   )
					 ) STORED`,
					`CREATE INDEX IF NOT EXISTS idx_observations_fts
					 ON observations USING GIN(search_vector)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return err
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					"DROP INDEX IF EXISTS idx_observations_fts",
					"ALTER TABLE observations DROP COLUMN IF EXISTS search_vector",
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						continue
					}
				}
				return nil
			},
		},

		// Migration 005: Full-text search for session summaries via tsvector (PostgreSQL).
		{
			ID: "005_session_summaries_fts",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE session_summaries
					 ADD COLUMN IF NOT EXISTS search_vector tsvector
					 GENERATED ALWAYS AS (
					   to_tsvector('english',
					     COALESCE(request, '') || ' ' ||
					     COALESCE(investigated, '') || ' ' ||
					     COALESCE(learned, '') || ' ' ||
					     COALESCE(completed, '') || ' ' ||
					     COALESCE(next_steps, '') || ' ' ||
					     COALESCE(notes, '')
					   )
					 ) STORED`,
					`CREATE INDEX IF NOT EXISTS idx_session_summaries_fts
					 ON session_summaries USING GIN(search_vector)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return err
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					"DROP INDEX IF EXISTS idx_session_summaries_fts",
					"ALTER TABLE session_summaries DROP COLUMN IF EXISTS search_vector",
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						continue
					}
				}
				return nil
			},
		},

		// Migration 006: pgvector vectors table.
		// Replaces sqlite-vec VIRTUAL TABLE with a standard PostgreSQL table using vector(384).
		// An HNSW index is created for efficient cosine similarity search.
		{
			ID: "006_sqlite_vec_vectors",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS vectors (
						doc_id       TEXT PRIMARY KEY,
						embedding    vector(384) NOT NULL,
						sqlite_id    BIGINT,
						doc_type     TEXT,
						field_type   TEXT,
						project      TEXT,
						scope        TEXT,
						model_version TEXT
					)`,
					// HNSW index for fast approximate nearest-neighbor search with cosine distance.
					`CREATE INDEX IF NOT EXISTS idx_vectors_embedding_hnsw
					 ON vectors USING hnsw (embedding vector_cosine_ops)
					 WITH (m = 16, ef_construction = 64)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return err
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS vectors").Error
			},
		},

		// Migration 007: Concept weights table with seed data
		{
			ID: "007_concept_weights",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.AutoMigrate(&ConceptWeight{}); err != nil {
					return err
				}

				// Seed default concept weights
				now := time.Now().Format(time.RFC3339)
				weights := []ConceptWeight{
					{Concept: "security", Weight: 0.30, UpdatedAt: now},
					{Concept: "gotcha", Weight: 0.25, UpdatedAt: now},
					{Concept: "best-practice", Weight: 0.20, UpdatedAt: now},
					{Concept: "anti-pattern", Weight: 0.20, UpdatedAt: now},
					{Concept: "architecture", Weight: 0.15, UpdatedAt: now},
					{Concept: "performance", Weight: 0.15, UpdatedAt: now},
					{Concept: "error-handling", Weight: 0.15, UpdatedAt: now},
					{Concept: "pattern", Weight: 0.10, UpdatedAt: now},
					{Concept: "testing", Weight: 0.10, UpdatedAt: now},
					{Concept: "debugging", Weight: 0.10, UpdatedAt: now},
					{Concept: "workflow", Weight: 0.05, UpdatedAt: now},
					{Concept: "tooling", Weight: 0.05, UpdatedAt: now},
				}

				// INSERT OR IGNORE equivalent in GORM
				return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&weights).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable("concept_weights")
			},
		},

		// Migration 008: Observation conflicts table
		{
			ID: "008_observation_conflicts",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&ObservationConflict{})
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable("observation_conflicts")
			},
		},

		// Migration 009: Patterns table
		{
			ID: "009_patterns",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&Pattern{})
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable("patterns")
			},
		},

		// Migration 010: Full-text search for patterns via tsvector (PostgreSQL).
		{
			ID: "010_patterns_fts",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE patterns
					 ADD COLUMN IF NOT EXISTS search_vector tsvector
					 GENERATED ALWAYS AS (
					   to_tsvector('english',
					     COALESCE(name, '') || ' ' ||
					     COALESCE(description, '') || ' ' ||
					     COALESCE(recommendation, '')
					   )
					 ) STORED`,
					`CREATE INDEX IF NOT EXISTS idx_patterns_fts
					 ON patterns USING GIN(search_vector)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return err
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					"DROP INDEX IF EXISTS idx_patterns_fts",
					"ALTER TABLE patterns DROP COLUMN IF EXISTS search_vector",
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						continue
					}
				}
				return nil
			},
		},

		// Migration 011: Observation relations table
		{
			ID: "011_observation_relations",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&ObservationRelation{})
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable("observation_relations")
			},
		},

		// Migration 012: Query optimization indexes
		// Adds covering and composite indexes for common query patterns
		{
			ID: "012_query_optimization_indexes",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Composite index for observation queries by project + scope + importance
					// Covers the common pattern: WHERE project = ? OR scope = 'global' ORDER BY importance_score DESC
					`CREATE INDEX IF NOT EXISTS idx_observations_project_scope_importance
					 ON observations(project, scope, importance_score DESC, created_at_epoch DESC)`,

					// Covering index for observation retrieval (includes most used columns)
					// Allows index-only scans for listing queries
					`CREATE INDEX IF NOT EXISTS idx_observations_project_covering
					 ON observations(project, scope, is_superseded, importance_score DESC)
					 WHERE is_superseded = 0 OR is_superseded IS NULL`,

					// Index for session summary lookups
					`CREATE INDEX IF NOT EXISTS idx_summaries_project_importance
					 ON session_summaries(project, importance_score DESC, created_at_epoch DESC)`,

					// Index for prompt retrieval by session
					`CREATE INDEX IF NOT EXISTS idx_prompts_session_number
					 ON user_prompts(claude_session_id, prompt_number)`,

					// Index for pattern queries by frequency
					`CREATE INDEX IF NOT EXISTS idx_patterns_frequency
					 ON patterns(frequency DESC, last_seen_at_epoch DESC)
					 WHERE is_deprecated = 0`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						// Non-fatal: index may already exist or fail for benign reasons
						continue
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					"DROP INDEX IF EXISTS idx_observations_project_scope_importance",
					"DROP INDEX IF EXISTS idx_observations_project_covering",
					"DROP INDEX IF EXISTS idx_summaries_project_importance",
					"DROP INDEX IF EXISTS idx_prompts_session_number",
					"DROP INDEX IF EXISTS idx_patterns_frequency",
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						continue
					}
				}
				return nil
			},
		},

		// Migration 013: Add archival columns to observations
		{
			ID: "013_observation_archival",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Add archival columns (IF NOT EXISTS: PostgreSQL 9.6+, idempotent)
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS is_archived INTEGER DEFAULT 0`,
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS archived_at_epoch INTEGER`,
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS archived_reason TEXT`,
					// Index for archived observations
					`CREATE INDEX IF NOT EXISTS idx_observations_archived ON observations(is_archived)`,
					// Composite index for filtering active (non-archived) observations
					`CREATE INDEX IF NOT EXISTS idx_observations_active
					 ON observations(project, is_archived, is_superseded, importance_score DESC)
					 WHERE (is_archived = 0 OR is_archived IS NULL)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						// Non-fatal: column may already exist
						continue
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				// SQLite doesn't support DROP COLUMN in older versions
				// but for newer versions we can try
				sqls := []string{
					"DROP INDEX IF EXISTS idx_observations_active",
					"DROP INDEX IF EXISTS idx_observations_archived",
				}
				for _, s := range sqls {
					_ = tx.Exec(s).Error
				}
				return nil
			},
		},

		// Migration 014: Add performance-critical indexes for common query patterns
		{
			ID: "014_performance_indexes",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Index for batch ID lookups (IN queries)
					`CREATE INDEX IF NOT EXISTS idx_observations_id_covering
					 ON observations(id, project, scope, importance_score)`,

					// Index for vector search result fetching
					`CREATE INDEX IF NOT EXISTS idx_vectors_doc_type_project
					 ON vectors(doc_type, project, scope)`,

					// Index for session summaries by project
					`CREATE INDEX IF NOT EXISTS idx_summaries_project_created
					 ON session_summaries(project, created_at_epoch DESC)`,

					// Index for user prompts by session
					`CREATE INDEX IF NOT EXISTS idx_prompts_session_created
					 ON user_prompts(claude_session_id, created_at_epoch DESC)`,

					// Index for patterns by type and project
					`CREATE INDEX IF NOT EXISTS idx_patterns_type_project
					 ON patterns(type, project, frequency DESC)
					 WHERE is_deprecated = 0`,

					// Index for observation relations
					`CREATE INDEX IF NOT EXISTS idx_relations_source_type
					 ON observation_relations(source_observation_id, relation_type)`,
					`CREATE INDEX IF NOT EXISTS idx_relations_target_type
					 ON observation_relations(target_observation_id, relation_type)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						// Non-fatal: index may already exist
						continue
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					"DROP INDEX IF EXISTS idx_observations_id_covering",
					"DROP INDEX IF EXISTS idx_vectors_doc_type_project",
					"DROP INDEX IF EXISTS idx_summaries_project_created",
					"DROP INDEX IF EXISTS idx_prompts_session_created",
					"DROP INDEX IF EXISTS idx_patterns_type_project",
					"DROP INDEX IF EXISTS idx_relations_source_type",
					"DROP INDEX IF EXISTS idx_relations_target_type",
				}
				for _, s := range sqls {
					_ = tx.Exec(s).Error
				}
				return nil
			},
		},

		// Migration 015: Add optimized composite indexes for common query patterns
		{
			ID: "015_optimized_composite_indexes",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Composite index for GetRecentObservations with project+scope filtering
					// Covers: WHERE (project = ? OR scope = 'global') ORDER BY importance_score DESC, created_at_epoch DESC
					`CREATE INDEX IF NOT EXISTS idx_observations_project_scope_created
					 ON observations(project, scope, created_at_epoch DESC, importance_score DESC)`,

					// Index for scope='global' queries (common pattern in search)
					`CREATE INDEX IF NOT EXISTS idx_observations_global_scope
					 ON observations(scope, importance_score DESC, created_at_epoch DESC)
					 WHERE scope = 'global'`,

					// Index for vector search result deduplication by observation
					`CREATE INDEX IF NOT EXISTS idx_vectors_observation_lookup
					 ON vectors(doc_type, sqlite_id, project)
					 WHERE doc_type = 'observation'`,

					// Index for FTS search result ordering
					`CREATE INDEX IF NOT EXISTS idx_observations_fts_ordering
					 ON observations(project, importance_score DESC)
					 WHERE (is_archived = 0 OR is_archived IS NULL) AND (is_superseded = 0 OR is_superseded IS NULL)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						// Non-fatal: index may already exist
						continue
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					"DROP INDEX IF EXISTS idx_observations_project_scope_created",
					"DROP INDEX IF EXISTS idx_observations_global_scope",
					"DROP INDEX IF EXISTS idx_vectors_observation_lookup",
					"DROP INDEX IF EXISTS idx_observations_fts_ordering",
				}
				for _, s := range sqls {
					_ = tx.Exec(s).Error
				}
				return nil
			},
		},

		// Migration 016: Add covering indexes for relation joins and active observations
		{
			ID: "016_relation_and_active_indexes",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Covering index for observation relation joins (common JOIN patterns)
					// Speeds up queries like: JOIN observation_relations ON source_id = obs.id WHERE relation_type = ?
					`CREATE INDEX IF NOT EXISTS idx_relations_source_type_target
					 ON observation_relations(source_observation_id, relation_type, target_observation_id)`,

					// Covering index for reverse relation lookups
					`CREATE INDEX IF NOT EXISTS idx_relations_target_type_source
					 ON observation_relations(target_observation_id, relation_type, source_observation_id)`,

					// Partial index for active (non-archived, non-superseded) observations
					// Optimizes activeObservationFilter queries
					`CREATE INDEX IF NOT EXISTS idx_observations_active
					 ON observations(project, importance_score DESC, created_at_epoch DESC)
					 WHERE (is_archived = 0 OR is_archived IS NULL) AND (is_superseded = 0 OR is_superseded IS NULL)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						// Non-fatal: index may already exist
						continue
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					"DROP INDEX IF EXISTS idx_relations_source_type_target",
					"DROP INDEX IF EXISTS idx_relations_target_type_source",
					"DROP INDEX IF EXISTS idx_observations_active",
				}
				for _, s := range sqls {
					_ = tx.Exec(s).Error
				}
				return nil
			},
		},

		// Migration 017: Content-addressable storage for collections
		{
			ID: "017_content_addressable_storage",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Content table (hash -> document body)
					`CREATE TABLE IF NOT EXISTS content (
                hash       TEXT PRIMARY KEY,
                doc        TEXT NOT NULL,
                created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
            )`,
					// Documents table (collection x path -> content hash)
					`CREATE TABLE IF NOT EXISTS documents (
                id         BIGSERIAL PRIMARY KEY,
                collection TEXT NOT NULL,
                path       TEXT NOT NULL,
                title      TEXT,
                hash       TEXT REFERENCES content(hash) ON DELETE SET NULL,
                active     BOOLEAN NOT NULL DEFAULT true,
                created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                UNIQUE(collection, path)
            )`,
					`CREATE INDEX IF NOT EXISTS idx_documents_collection ON documents(collection)`,
					`CREATE INDEX IF NOT EXISTS idx_documents_hash ON documents(hash)`,
					`CREATE INDEX IF NOT EXISTS idx_documents_active ON documents(active) WHERE active = true`,
					// FTS tsvector on documents for BM25 search
					`ALTER TABLE documents
             ADD COLUMN IF NOT EXISTS search_vector tsvector
             GENERATED ALWAYS AS (
               to_tsvector('english',
                 COALESCE(path, '') || ' ' ||
                 COALESCE(title, '')
               )
             ) STORED`,
					`CREATE INDEX IF NOT EXISTS idx_documents_fts ON documents USING GIN(search_vector)`,
					// Content chunks table (hash x seq -> embedding)
					// Vector dimension 384 matches default BGE model
					`CREATE TABLE IF NOT EXISTS content_chunks (
                hash       TEXT NOT NULL REFERENCES content(hash) ON DELETE CASCADE,
                seq        INTEGER NOT NULL,
                pos        INTEGER NOT NULL,
                model      TEXT NOT NULL,
                embedding  vector(384),
                created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                PRIMARY KEY (hash, seq)
            )`,
					`CREATE INDEX IF NOT EXISTS idx_content_chunks_hash ON content_chunks(hash)`,
					`CREATE INDEX IF NOT EXISTS idx_content_chunks_embedding_hnsw
             ON content_chunks USING hnsw (embedding vector_cosine_ops)
             WITH (m = 16, ef_construction = 64)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 017: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				for _, t := range []string{"content_chunks", "documents", "content"} {
					if err := tx.Exec("DROP TABLE IF EXISTS " + t + " CASCADE").Error; err != nil {
						return err
					}
				}
				return nil
			},
		},
		// Migration 018: Indexed sessions table for Phase 4 JSONL session indexing
		{
			ID: "018_session_indexing",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS indexed_sessions (
    id TEXT PRIMARY KEY,
    workstation_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    project_path TEXT,
    git_branch TEXT,
    first_msg_at TIMESTAMPTZ,
    last_msg_at TIMESTAMPTZ,
    exchange_count INTEGER DEFAULT 0,
    tool_counts JSONB,
    topics JSONB,
    content TEXT,
    file_mtime TIMESTAMPTZ,
    indexed_at TIMESTAMPTZ DEFAULT NOW(),
    tsv TSVECTOR GENERATED ALWAYS AS (
        to_tsvector('english', coalesce(content, ''))
    ) STORED
)`,
					`CREATE INDEX IF NOT EXISTS idx_sessions_ws ON indexed_sessions(workstation_id)`,
					`CREATE INDEX IF NOT EXISTS idx_sessions_proj ON indexed_sessions(project_id)`,
					`CREATE INDEX IF NOT EXISTS idx_sessions_ws_proj ON indexed_sessions(workstation_id, project_id)`,
					`CREATE INDEX IF NOT EXISTS idx_sessions_last_msg ON indexed_sessions(last_msg_at DESC)`,
					`CREATE INDEX IF NOT EXISTS idx_sessions_tsv ON indexed_sessions USING GIN(tsv)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 018: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS indexed_sessions CASCADE`).Error
			},
		},
	})

	if err := m.Migrate(); err != nil {
		return fmt.Errorf("run gormigrate migrations: %w", err)
	}

	return nil
}
