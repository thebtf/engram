// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// runMigrations runs all database migrations using gormigrate.
func runMigrations(db *gorm.DB) error {
	// Enable pgvector extension before running any migrations.
	// CREATE EXTENSION IF NOT EXISTS is idempotent.
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return fmt.Errorf("enable pgvector extension: %w", err)
	}

	m := gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{
		// Migration 001: Core tables.
		// Keep the historical chain self-contained for fresh databases even though
		// later PR-B drop migrations remove observations/session_summaries again.
		// sdk_sessions must remain because SessionStore is still wired.
		//
		// NOTE: do not rely solely on the current pkg/models structs here. Fresh-install
		// replay must create the historical baseline columns that later ALTER/INDEX steps
		// expect (for example session_summaries.importance_score). Upgrades from older
		// databases already have these columns; fresh DBs need them created up front.
		{
			ID: "001_core_tables",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.AutoMigrate(&SDKSession{}, &models.Observation{}); err != nil {
					return err
				}
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS session_summaries (
						id BIGSERIAL PRIMARY KEY,
						sdk_session_id TEXT NOT NULL,
						project TEXT NOT NULL,
						request TEXT,
						investigated TEXT,
						learned TEXT,
						completed TEXT,
						next_steps TEXT,
						notes TEXT,
						prompt_number BIGINT,
						discovery_tokens BIGINT NOT NULL DEFAULT 0,
						created_at TEXT NOT NULL DEFAULT NOW()::text,
						created_at_epoch BIGINT NOT NULL DEFAULT 0,
						importance_score DOUBLE PRECISION NOT NULL DEFAULT 0
					)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 001: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable("sdk_sessions", "observations", "session_summaries")
			},
		},

		// Migration 002: User prompts table.
		// Keep the base table creation self-contained so historical migration 003 and later
		// project-rewrite migrations can ALTER/UPDATE the table on fresh databases.
		{
			ID: "002_user_prompts",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS user_prompts (
						id BIGSERIAL PRIMARY KEY,
						claude_session_id TEXT NOT NULL,
						sdk_session_id TEXT NOT NULL DEFAULT '',
						project TEXT NOT NULL DEFAULT '',
						prompt_number INTEGER NOT NULL DEFAULT 0,
						prompt_text TEXT NOT NULL,
						matched_observations INTEGER NOT NULL DEFAULT 0,
						created_at TEXT NOT NULL DEFAULT NOW()::text,
						created_at_epoch BIGINT NOT NULL DEFAULT 0
					)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 002: %w", err)
					}
				}
				return nil
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
				sql := `CREATE TABLE IF NOT EXISTS patterns (
					id BIGSERIAL PRIMARY KEY,
					name TEXT NOT NULL,
					type TEXT NOT NULL,
					project TEXT,
					description TEXT,
					recommendation TEXT,
					frequency INTEGER NOT NULL DEFAULT 1,
					confidence REAL NOT NULL DEFAULT 0.5,
					status TEXT NOT NULL DEFAULT 'active',
					created_at TEXT NOT NULL DEFAULT NOW()::text,
					created_at_epoch BIGINT NOT NULL DEFAULT 0,
					last_seen_at TEXT NOT NULL DEFAULT NOW()::text,
					last_seen_at_epoch BIGINT NOT NULL DEFAULT 0
				)`
				return tx.Exec(sql).Error
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
		// Migration 019: Extended relation types + memory type classification
		{
			ID: "019_extended_relation_types",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Drop old CHECK constraint and add new one with all 17 relation types
					`ALTER TABLE observation_relations DROP CONSTRAINT IF EXISTS chk_observation_relations_relation_type`,
					`ALTER TABLE observation_relations ADD CONSTRAINT chk_observation_relations_relation_type CHECK (relation_type IN ('causes','fixes','supersedes','depends_on','relates_to','evolves_from','leads_to','similar_to','contradicts','reinforces','invalidated_by','explains','shares_theme','parallel_context','summarizes','part_of','prefers_over'))`,
					// Drop old detection_source CHECK and add creative_association
					`ALTER TABLE observation_relations DROP CONSTRAINT IF EXISTS chk_observation_relations_detection_source`,
					`ALTER TABLE observation_relations ADD CONSTRAINT chk_observation_relations_detection_source CHECK (detection_source IN ('file_overlap','embedding_similarity','temporal_proximity','narrative_mention','concept_overlap','type_progression','creative_association'))`,
					// Add memory_type column to observations
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS memory_type TEXT`,
					`CREATE INDEX IF NOT EXISTS idx_observations_memory_type ON observations(memory_type)`,
					// Backfill memory_type for existing rows based on type field
					`UPDATE observations SET memory_type = CASE
					WHEN type = 'decision' THEN 'decision'
					WHEN type = 'bugfix' THEN 'insight'
					WHEN type = 'feature' THEN 'context'
					WHEN type = 'refactor' THEN 'pattern'
					WHEN type = 'discovery' THEN 'insight'
					WHEN type = 'change' THEN 'context'
					ELSE 'context'
					END WHERE memory_type IS NULL`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 019: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`DROP INDEX IF EXISTS idx_observations_memory_type`,
					`ALTER TABLE observations DROP COLUMN IF EXISTS memory_type`,
					`ALTER TABLE observation_relations DROP CONSTRAINT IF EXISTS chk_observation_relations_relation_type`,
					`ALTER TABLE observation_relations ADD CONSTRAINT chk_observation_relations_relation_type CHECK (relation_type IN ('causes','fixes','supersedes','depends_on','relates_to','evolves_from'))`,
					`ALTER TABLE observation_relations DROP CONSTRAINT IF EXISTS chk_observation_relations_detection_source`,
					`ALTER TABLE observation_relations ADD CONSTRAINT chk_observation_relations_detection_source CHECK (detection_source IN ('file_overlap','embedding_similarity','temporal_proximity','narrative_mention','concept_overlap','type_progression'))`,
				}
				for _, s := range sqls {
					_ = tx.Exec(s).Error
				}
				return nil
			},
		},
		// Migration 020: Configure pgvector embedding dimensions.
		// No-op as of v5: content_chunks is dropped in 085, and the vector
		// pipeline (internal/embedding, internal/vector) is removed entirely.
		// This entry is retained so gormigrate does not re-run it on existing DBs.
		{
			ID: "020_configurable_vector_dimensions",
			Migrate: func(tx *gorm.DB) error {
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return nil
			},
		},
		// Migration 021: Fix patterns indexes — use status column instead of non-existent is_deprecated.
		{
			ID: "021_fix_patterns_indexes",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`DROP INDEX IF EXISTS idx_patterns_frequency`,
					`DROP INDEX IF EXISTS idx_patterns_type_project`,
					`CREATE INDEX IF NOT EXISTS idx_patterns_frequency
					 ON patterns(frequency DESC, last_seen_at_epoch DESC)
					 WHERE status = 'active'`,
					`CREATE INDEX IF NOT EXISTS idx_patterns_type_project
					 ON patterns(type, frequency DESC)
					 WHERE status = 'active'`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 021: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				if err := tx.Exec(`DROP INDEX IF EXISTS idx_patterns_frequency`).Error; err != nil {
					return fmt.Errorf("migration 021 rollback: %w", err)
				}
				return tx.Exec(`DROP INDEX IF EXISTS idx_patterns_type_project`).Error
			},
		},
		// Migration 022: Raw events table — immutable append-only event log (source of truth).
		{
			ID: "022_raw_events",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS raw_events (
						id BIGSERIAL PRIMARY KEY,
						session_id TEXT NOT NULL,
						tool_name TEXT NOT NULL,
						tool_input JSONB,
						tool_result JSONB,
						created_at_epoch BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW()) * 1000)::BIGINT,
						project TEXT NOT NULL DEFAULT '',
						workstation_id TEXT NOT NULL DEFAULT '',
						processed BOOLEAN NOT NULL DEFAULT FALSE
					)`,
					`CREATE INDEX IF NOT EXISTS idx_raw_events_session_time
					 ON raw_events(session_id, created_at_epoch)`,
					`CREATE INDEX IF NOT EXISTS idx_raw_events_unprocessed
					 ON raw_events(created_at_epoch)
					 WHERE processed = FALSE`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 022: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS raw_events").Error
			},
		},
		// Migration 023: Add enrichment fields to observations for Progressive Refinement.
		{
			ID: "023_observation_enrichment",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS enrichment_level INT NOT NULL DEFAULT 0`,
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS source_event_ids BIGINT[]`,
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS raw_content TEXT`,
					`CREATE INDEX IF NOT EXISTS idx_observations_enrichment
					 ON observations(enrichment_level, created_at_epoch DESC)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 023: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`DROP INDEX IF EXISTS idx_observations_enrichment`,
					`ALTER TABLE observations DROP COLUMN IF EXISTS raw_content`,
					`ALTER TABLE observations DROP COLUMN IF EXISTS source_event_ids`,
					`ALTER TABLE observations DROP COLUMN IF EXISTS enrichment_level`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return err
					}
				}
				return nil
			},
		},
		// Migration 024: Memory blocks schema — structured distilled knowledge per project.
		// Schema only; population logic is Phase 2 (consolidation-driven).
		{
			ID: "024_memory_blocks",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS memory_blocks (
						id BIGSERIAL PRIMARY KEY,
						project TEXT NOT NULL,
						block_type TEXT NOT NULL,
						content TEXT NOT NULL DEFAULT '',
						source_observation_ids BIGINT[],
						version INT NOT NULL DEFAULT 1,
						last_updated_epoch BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW()) * 1000)::BIGINT,
						UNIQUE(project, block_type)
					)`,
					`CREATE INDEX IF NOT EXISTS idx_memory_blocks_project
					 ON memory_blocks(project)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 024: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS memory_blocks").Error
			},
		},
		// Migration 025: Self-learning utility tracking fields
		{
			ID: "025_utility_tracking",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Add utility_score with neutral prior (0.5)
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS utility_score REAL NOT NULL DEFAULT 0.5`,
					// Add injection count
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS injection_count INT NOT NULL DEFAULT 0`,
					// Update type CHECK constraint to include 'guidance'
					`ALTER TABLE observations DROP CONSTRAINT IF EXISTS chk_observations_type`,
					`ALTER TABLE observations ADD CONSTRAINT chk_observations_type
					 CHECK (type IN ('decision', 'bugfix', 'feature', 'refactor', 'discovery', 'change', 'guidance'))`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 025: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE observations DROP COLUMN IF EXISTS utility_score`,
					`ALTER TABLE observations DROP COLUMN IF EXISTS injection_count`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return err
					}
				}
				return nil
			},
		},
		// Migration 026: Telemetry snapshots table for belief revision measurement.
		{
			ID: "026_telemetry_snapshots",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS telemetry_snapshots (
						id BIGSERIAL PRIMARY KEY,
						snapshot_type TEXT NOT NULL,
						project TEXT NOT NULL DEFAULT '',
						data JSONB NOT NULL,
						created_at_epoch BIGINT NOT NULL
					)`,
					`CREATE INDEX IF NOT EXISTS idx_telemetry_type_time ON telemetry_snapshots(snapshot_type, created_at_epoch DESC)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 026: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS telemetry_snapshots").Error
			},
		},

		// Migration 027: Add source_type for provenance tracking (belief revision Phase 1).
		{
			ID: "027_observation_source_type",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS source_type TEXT DEFAULT 'unknown'`,
					`CREATE INDEX IF NOT EXISTS idx_observations_source_type ON observations(source_type)`,
					// Backfill existing observations based on their type field
					`UPDATE observations SET source_type = CASE
						WHEN type IN ('change', 'bugfix') THEN 'tool_verified'
						WHEN type = 'discovery' THEN 'tool_read'
						ELSE 'unknown'
					END WHERE source_type = '' OR source_type IS NULL OR source_type = 'unknown'`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 027: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`DROP INDEX IF EXISTS idx_observations_source_type`,
					`ALTER TABLE observations DROP COLUMN IF EXISTS source_type`,
				}
				for _, s := range sqls {
					_ = tx.Exec(s).Error
				}
				return nil
			},
		},
		// Migration 028: Per-session observation injection tracking for utility signal detection.
		{
			ID: "028_session_observation_injections",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS session_observation_injections (
					id BIGSERIAL PRIMARY KEY,
					session_id BIGINT NOT NULL REFERENCES sdk_sessions(id) ON DELETE CASCADE,
					observation_id BIGINT NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
					injected_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					UNIQUE(session_id, observation_id)
				)`,
					`CREATE INDEX IF NOT EXISTS idx_soi_session_id ON session_observation_injections(session_id)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 028: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS session_observation_injections").Error
			},
		},
		// Migration 029: Add text column to content_chunks for readable search results.
		{
			ID: "029_content_chunks_text",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE content_chunks ADD COLUMN IF NOT EXISTS text TEXT NOT NULL DEFAULT ''`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE content_chunks DROP COLUMN IF EXISTS text`).Error
			},
		},
		// Migration 030: Projects lookup table for stable cross-platform project identity.
		// Maps canonical git-remote-based project IDs to legacy path-based aliases,
		// enabling zero-downtime migration as clients upgrade to git-remote IDs.
		{
			ID: "030_projects_table",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS projects (
					id            TEXT PRIMARY KEY,
					git_remote    TEXT,
					relative_path TEXT,
					legacy_ids    TEXT[],
					display_name  TEXT,
					created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
				)`,
					// Unique constraint on (git_remote, relative_path) for ON CONFLICT upserts.
					// Partial index excludes rows without a git_remote (path-based fallback projects).
					`CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_remote_path
				 ON projects(git_remote, relative_path)
				 WHERE git_remote IS NOT NULL`,
					// GIN index on legacy_ids array for fast alias lookup via @> operator.
					`CREATE INDEX IF NOT EXISTS idx_projects_legacy_ids
				 ON projects USING GIN(legacy_ids)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 030: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS projects CASCADE").Error
			},
		},
		// Migration 031: Add credential storage columns and update type constraint.
		{
			ID: "031_credential_storage",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS encrypted_secret BYTEA`,
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS encryption_key_fingerprint TEXT`,
					// Drop old type constraint and add new one that includes 'credential'
					`ALTER TABLE observations DROP CONSTRAINT IF EXISTS chk_observations_type`,
					`ALTER TABLE observations ADD CONSTRAINT chk_observations_type CHECK (type IN ('decision', 'bugfix', 'feature', 'refactor', 'discovery', 'change', 'guidance', 'credential'))`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 031: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE observations DROP COLUMN IF EXISTS encrypted_secret`,
					`ALTER TABLE observations DROP COLUMN IF EXISTS encryption_key_fingerprint`,
					// Rewrite credential observations to 'discovery' before restoring the old
					// constraint that excludes 'credential'. Without this, ADD CONSTRAINT fails
					// if any credential rows exist, leaving the DB in a broken half-rolled-back state.
					`UPDATE observations SET type = 'discovery' WHERE type = 'credential'`,
					`ALTER TABLE observations DROP CONSTRAINT IF EXISTS chk_observations_type`,
					`ALTER TABLE observations ADD CONSTRAINT chk_observations_type CHECK (type IN ('decision', 'bugfix', 'feature', 'refactor', 'discovery', 'change', 'guidance'))`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						log.Warn().Err(err).Str("sql", s).Msg("migration 031 rollback step failed")
					}
				}
				return nil
			},
		},
		// Migration 032: Agent scoping — add agent_id column and update scope check constraint.
		{
			ID: "032_agent_scoping",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Add agent_id column (nullable; empty string means no agent)
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS agent_id TEXT NOT NULL DEFAULT ''`,
					// Index for agent-scoped lookups
					`CREATE INDEX IF NOT EXISTS idx_observations_agent_id ON observations(agent_id) WHERE agent_id != ''`,
					// Drop old scope check constraint and add new one that includes 'agent'
					`ALTER TABLE observations DROP CONSTRAINT IF EXISTS chk_observations_scope`,
					`ALTER TABLE observations ADD CONSTRAINT chk_observations_scope CHECK (scope IN ('project', 'global', 'agent'))`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 032: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					// Rewrite agent-scoped observations to 'project' before restoring old constraint
					`UPDATE observations SET scope = 'project' WHERE scope = 'agent'`,
					`ALTER TABLE observations DROP CONSTRAINT IF EXISTS chk_observations_scope`,
					`ALTER TABLE observations ADD CONSTRAINT chk_observations_scope CHECK (scope IN ('project', 'global'))`,
					`DROP INDEX IF EXISTS idx_observations_agent_id`,
					`ALTER TABLE observations DROP COLUMN IF EXISTS agent_id`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						log.Warn().Err(err).Str("sql", s).Msg("migration 032 rollback step failed")
					}
				}
				return nil
			},
		},
		{
			ID: "033_create_search_misses",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS search_misses (
					id BIGSERIAL PRIMARY KEY,
					project TEXT NOT NULL,
					query TEXT NOT NULL,
					created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
				)`,
					`CREATE INDEX IF NOT EXISTS idx_search_misses_project ON search_misses (project)`,
					`CREATE INDEX IF NOT EXISTS idx_search_misses_created ON search_misses (created_at)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 033: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS search_misses`).Error
			},
		},
		{
			ID: "034_credential_uniqueness_and_search_miss_index",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE UNIQUE INDEX IF NOT EXISTS idx_observations_credential_unique
					ON observations (project, title) WHERE type = 'credential'`,
					`CREATE INDEX IF NOT EXISTS idx_search_misses_project_query_created
					ON search_misses (project, query, created_at DESC)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 034: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`DROP INDEX IF EXISTS idx_observations_credential_unique`,
					`DROP INDEX IF EXISTS idx_search_misses_project_query_created`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						log.Warn().Err(err).Str("sql", s).Msg("migration 034 rollback step failed")
					}
				}
				return nil
			},
		},
		// Migration 035: Add rejected[] JSONB column to observations for structured decision schema.
		// Stores alternatives that were considered and dismissed, enabling reliable contradiction detection
		// without fragile narrative-text parsing.
		{
			ID: "035_decision_rejected_field",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE observations ADD COLUMN IF NOT EXISTS rejected JSONB DEFAULT '[]'`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE observations DROP COLUMN IF EXISTS rejected`).Error
			},
		},
		// Migration 036: API tokens table for client token authentication.
		// Stores bcrypt-hashed client tokens with prefix-based lookup for the auth middleware.
		{
			ID: "036_api_tokens",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS api_tokens (
					id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
					name TEXT NOT NULL UNIQUE,
					token_hash TEXT NOT NULL,
					token_prefix TEXT NOT NULL,
					scope TEXT NOT NULL DEFAULT 'read-write',
					created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
					last_used_at TIMESTAMPTZ,
					request_count BIGINT NOT NULL DEFAULT 0,
					error_count BIGINT NOT NULL DEFAULT 0,
					revoked BOOLEAN NOT NULL DEFAULT false,
					revoked_at TIMESTAMPTZ
				)`,
					`CREATE INDEX IF NOT EXISTS idx_api_tokens_prefix ON api_tokens (token_prefix) WHERE NOT revoked`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 036: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS api_tokens").Error
			},
		},
		// Migration 037: Persistent search query log for analytics that survive server restarts.
		{
			ID: "037_search_query_log",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS search_query_log (
					id BIGSERIAL PRIMARY KEY,
					project TEXT,
					query TEXT NOT NULL,
					search_type TEXT NOT NULL,
					results INT NOT NULL DEFAULT 0,
					used_vector BOOL NOT NULL DEFAULT false,
					latency_ms REAL,
					created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
				)`,
					`CREATE INDEX IF NOT EXISTS idx_search_query_log_created ON search_query_log (created_at DESC)`,
					`CREATE INDEX IF NOT EXISTS idx_search_query_log_project ON search_query_log (project, created_at DESC)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 037: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS search_query_log`).Error
			},
		},
		// Migration 038: Persistent retrieval stats log for analytics that survive server restarts.
		{
			ID: "038_retrieval_stats_log",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS retrieval_stats_log (
					id BIGSERIAL PRIMARY KEY,
					project TEXT NOT NULL,
					event_type TEXT NOT NULL,
					count INT NOT NULL DEFAULT 1,
					created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
				)`,
					`CREATE INDEX IF NOT EXISTS idx_retrieval_stats_project_type_created ON retrieval_stats_log (project, event_type, created_at DESC)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 038: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS retrieval_stats_log`).Error
			},
		},
		// Migration 039: Add TTL support for verified facts on observations.
		// NULL = no expiration (backwards-compatible). TTL only applies to verified-tagged observations.
		{
			ID: "039_observations_verified_ttl",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ NULL`,
					`ALTER TABLE observations ADD COLUMN IF NOT EXISTS ttl_days INT NULL`,
					`CREATE INDEX IF NOT EXISTS idx_observations_expires ON observations (expires_at) WHERE expires_at IS NOT NULL`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 039: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`DROP INDEX IF EXISTS idx_observations_expires`,
					`ALTER TABLE observations DROP COLUMN IF EXISTS ttl_days`,
					`ALTER TABLE observations DROP COLUMN IF EXISTS expires_at`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						log.Warn().Err(err).Str("sql", s).Msg("migration 039 rollback step failed")
					}
				}
				return nil
			},
		},
		// Migration 040: One-time cleanup of garbage observations created by SDK tool output extraction.
		// Deletes observations with titles matching known garbage patterns (PowerShell errors, auth failures,
		// stdin terminal checks, etc.) and orphan vectors not matching any observation.
		{
			ID: "040_cleanup_garbage_observations",
			Migrate: func(tx *gorm.DB) error {
				// Delete garbage observations by title pattern
				garbagePatterns := []string{
					"PowerShell%Error%",
					"PowerShell%Anomaly%",
					"PowerShell Dot-Source%",
					"Stdin Terminal%",
					"Authorization Header Missing%",
					"FINDSTR%Cannot%",
					"Missing Authentication%",
					"JavaScript Property Setting%",
					"Incorrect FINDSTR%",
					"Invalid Argument in Child%",
					"bufio Over-read%",
					"Stdin Terminal Check%",
					"File Lock Handling%",
					"Upstream Connection%",
					"TRACE Logging Removal%",
					"npm install completion%",
					"Stderr Input Handling%",
					"Status Discrepancy Detection%",
					"Job-Session ID Synchronization%",
					"Incorrect Redirection Syntax%",
					"Rename node_modules%",
					"Case Sensitivity in Format%",
					"Cleanup Function%Parameter%",
					"Cleanup by startedAt%",
					"Null%Numeric Properties%",
					"User Cancellation Handling%",
				}
				var totalDeleted int64
				for _, pattern := range garbagePatterns {
					result := tx.Exec(`DELETE FROM observations WHERE title LIKE ?`, pattern)
					if result.Error != nil {
						log.Warn().Err(result.Error).Str("pattern", pattern).Msg("migration 040: delete pattern failed")
						continue
					}
					totalDeleted += result.RowsAffected
				}

				// Delete orphan vectors: observation_vectors entries whose sqlite_id
				// (stored in metadata) doesn't match any existing observation.
				orphanResult := tx.Exec(`
				DELETE FROM observation_vectors
				WHERE id IN (
					SELECT ov.id FROM observation_vectors ov
					LEFT JOIN observations o ON ov.metadata->>'sqlite_id' = o.id::text
					WHERE o.id IS NULL
				)
			`)
				orphanCount := int64(0)
				if orphanResult.Error != nil {
					// observation_vectors table might not exist or have different schema — not fatal
					log.Warn().Err(orphanResult.Error).Msg("migration 040: orphan vector cleanup failed (non-fatal)")
				} else {
					orphanCount = orphanResult.RowsAffected
				}

				log.Info().
					Int64("garbage_deleted", totalDeleted).
					Int64("orphan_vectors_deleted", orphanCount).
					Msg("migration 040: garbage cleanup complete")
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				// One-time cleanup — no rollback possible
				return nil
			},
		},
		// Migration 041: Purge orphan vectors — correct table name (vectors, not observation_vectors).
		{
			ID: "041_purge_orphan_vectors",
			Migrate: func(tx *gorm.DB) error {
				result := tx.Exec(`DELETE FROM vectors WHERE sqlite_id NOT IN (SELECT id FROM observations)`)
				if result.Error != nil {
					log.Warn().Err(result.Error).Msg("migration 041: orphan vector purge failed (non-fatal)")
					return nil
				}
				log.Info().Int64("orphan_vectors_deleted", result.RowsAffected).Msg("migration 041: orphan vector purge complete")
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return nil
			},
		},
		// Migration 042: Purge low-quality patterns (frequency < 5).
		// With 111K+ patterns accumulated from garbage SDK extraction, most are noise.
		// MinFrequency threshold was raised to 5 in T019 — patterns below this are worthless.
		{
			ID: "042_purge_low_quality_patterns",
			Migrate: func(tx *gorm.DB) error {
				result := tx.Exec(`DELETE FROM patterns WHERE frequency < 5`)
				if result.Error != nil {
					log.Warn().Err(result.Error).Msg("migration 042: pattern purge failed (non-fatal)")
					return nil
				}
				log.Info().Int64("patterns_deleted", result.RowsAffected).Msg("migration 042: low-quality pattern purge complete")
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return nil
			},
		},
		// Migration 043: Radical cleanup of garbage SDK-extracted observations.
		// These observations were created by the SDK tool output extraction pipeline before v1.3.4
		// (whitelist mode). They are trivially discoverable facts, tool errors, status transitions,
		// and cross-project noise that pollute semantic search and degrade agent performance.
		{
			ID: "043_radical_observation_cleanup",
			Migrate: func(tx *gorm.DB) error {
				garbagePatterns := []string{
					// Tool mechanics (trivially discoverable at runtime)
					"Tool%Query Pattern%",
					"Tool%Search%Pattern%",
					"Tool%Naming Convention%",
					"Tool%Selection%Pattern%",
					"Tool Search%Found%",
					"Tool%Match%Found%",
					"Memory Store Tool%",
					"Deferred Tool%",
					"Exact Tool Match%",

					// Task status transitions (repeated 20+ times, zero value)
					"Task Status%Transition%",
					"Task%Completion%Confirmed%",
					"Status Transition%",
					"Status%Discrepancy%",
					"No Work Available%",

					// Job tracking noise
					"Job Status%",
					"Job-Session ID%",

					// Process output artifacts
					"Process Output%",
					"Stderr%Handling%",

					// System prompt meta-observations
					"Claude Anti-Sycophancy%",
					"User Interaction Guidelines%",
					"User Communication Guidelines%",
					"Strict Verification Guidelines%",
					"Copyright Enforcement%",
					"Critical Reminders%",
					"Search Scaling by%",
					"Past Conversation Search%",
					"System Prompt Access%",
					"Anti-Sycophancy%",
					"Keyword Extraction Guidelines%",
					"Tone Consistency%",
					"Zero-confirmation Rule%",
					"Plugin Configuration Warnings%",
					"Prioritize Internal Tools%",

					// Generic discoveries with no behavioral impact
					"Brace%Discrepancy%",
					"Brace%Detection%",
					"Content Structure Pattern%",
					"Severity Classification%",
					"Pre-commit Check%",
					"Commit Message%Convention%",
					"Commit Message Structure%",
					"File Size Monitoring%",

					// iSCSI debug noise (from nvmdfs project)
					"iSCSI%",

					// Timestamp-based titles from subtitle parser
					"00:%",

					// Test observations
					"type test",

					// Robocopy/npm transient noise
					"Robocopy%",
					"npm install completion%",
				}

				var totalDeleted int64
				for _, pattern := range garbagePatterns {
					result := tx.Exec("DELETE FROM observations WHERE title LIKE ?", pattern)
					if result.Error != nil {
						log.Warn().Err(result.Error).Str("pattern", pattern).Msg("migration 043: delete failed")
						continue
					}
					totalDeleted += result.RowsAffected
				}

				log.Info().Int64("total_deleted", totalDeleted).Msg("migration 043: radical observation cleanup complete")
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return nil
			},
		},
		// Migration 044: Add user_feedback column to observations.
		{
			ID: "044_observation_user_feedback",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE observations ADD COLUMN IF NOT EXISTS user_feedback INT NOT NULL DEFAULT 0`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE observations DROP COLUMN IF EXISTS user_feedback`).Error
			},
		},
		// Migration 045: Add is_suppressed column to observations.
		{
			ID: "045_observation_is_suppressed",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE observations ADD COLUMN IF NOT EXISTS is_suppressed BOOLEAN NOT NULL DEFAULT FALSE`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE observations DROP COLUMN IF EXISTS is_suppressed`).Error
			},
		},
		// Migration 046: Create injection_log table for tracking context injections.
		{
			ID: "046_injection_log",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.Exec(`CREATE TABLE IF NOT EXISTS injection_log (
				id BIGSERIAL PRIMARY KEY,
				observation_id BIGINT NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
				project TEXT NOT NULL DEFAULT '',
				task_context TEXT NOT NULL DEFAULT '',
				session_id TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_injection_log_observation_id ON injection_log(observation_id)`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_injection_log_project ON injection_log(project)`).Error; err != nil {
					return err
				}
				return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_injection_log_created_at ON injection_log(created_at)`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS injection_log`).Error
			},
		},
		// Migration 047: Drop unused memory_blocks table (created by migration 024, never populated).
		{
			ID: "047_drop_memory_blocks",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS memory_blocks`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				// Table was unused — no rollback needed
				return nil
			},
		},
		// Migration 048: Convert text JSON columns to jsonb + GIN indexes for concept-tag queries and file-context lookup.
		// Columns concepts, files_modified, files_read were stored as text (JSON strings).
		// PostgreSQL GIN indexes require jsonb type, so we ALTER TYPE first.
		{
			ID: "048_gin_indexes_concepts_files",
			Migrate: func(tx *gorm.DB) error {
				// Convert text columns to jsonb (safe: content is already valid JSON)
				if err := tx.Exec(`ALTER TABLE observations ALTER COLUMN concepts TYPE jsonb USING COALESCE(concepts::jsonb, '[]'::jsonb)`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE observations ALTER COLUMN files_modified TYPE jsonb USING COALESCE(files_modified::jsonb, '[]'::jsonb)`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE observations ALTER COLUMN files_read TYPE jsonb USING COALESCE(files_read::jsonb, '[]'::jsonb)`).Error; err != nil {
					return err
				}
				// Now create GIN indexes on jsonb columns
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_concepts_gin ON observations USING GIN (concepts)`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_files_modified_gin ON observations USING GIN (files_modified)`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_files_read_gin ON observations USING GIN (files_read)`).Error; err != nil {
					return err
				}
				// Composite index for temporal chain lookups
				return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_session_prompt ON observations (sdk_session_id, prompt_number DESC) WHERE COALESCE(is_superseded, 0) = 0`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				tx.Exec(`DROP INDEX IF EXISTS idx_observations_concepts_gin`)
				tx.Exec(`DROP INDEX IF EXISTS idx_observations_files_modified_gin`)
				tx.Exec(`DROP INDEX IF EXISTS idx_observations_files_read_gin`)
				tx.Exec(`DROP INDEX IF EXISTS idx_observations_session_prompt`)
				// Revert jsonb back to text
				tx.Exec(`ALTER TABLE observations ALTER COLUMN concepts TYPE text USING concepts::text`)
				tx.Exec(`ALTER TABLE observations ALTER COLUMN files_modified TYPE text USING files_modified::text`)
				tx.Exec(`ALTER TABLE observations ALTER COLUMN files_read TYPE text USING files_read::text`)
				return nil
			},
		},
		// Migration 049: Create project_settings table for per-project adaptive threshold.
		{
			ID: "049_project_settings",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`CREATE TABLE IF NOT EXISTS project_settings (
				project TEXT PRIMARY KEY,
				relevance_threshold FLOAT NOT NULL DEFAULT 0.3,
				feedback_count INT NOT NULL DEFAULT 0,
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS project_settings`).Error
			},
		},

		// Migration 050: System configuration key-value store.
		// Stores persistent system-level settings such as the current embedding model name.
		// Used by the maintenance service to detect embedding model changes across restarts.
		{
			ID: "050_system_config",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`CREATE TABLE IF NOT EXISTS system_config (
					key        TEXT PRIMARY KEY,
					value      TEXT NOT NULL,
					updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
				)`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS system_config`).Error
			},
		},
		// Migration 051: versioned_documents + versioned_document_comments tables for
		// versioned document storage. Uses distinct table names to avoid collision with
		// the RAG `documents` table created by migration 017.
		// Foundation for AI agent collaboration platform (task/issue management).
		{
			ID: "051_documents",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.Exec(`CREATE TABLE IF NOT EXISTS versioned_documents (
				id BIGSERIAL PRIMARY KEY,
				path TEXT NOT NULL,
				project TEXT NOT NULL,
				version INT NOT NULL DEFAULT 1,
				content TEXT NOT NULL,
				content_hash TEXT NOT NULL,
				doc_type TEXT NOT NULL DEFAULT 'markdown',
				metadata JSONB NOT NULL DEFAULT '{}',
				author TEXT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				UNIQUE(path, project, version)
			)`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_versioned_documents_project_path ON versioned_documents (project, path, version DESC)`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_versioned_documents_doc_type ON versioned_documents (doc_type)`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`CREATE TABLE IF NOT EXISTS versioned_document_comments (
				id BIGSERIAL PRIMARY KEY,
				document_id BIGINT NOT NULL REFERENCES versioned_documents(id),
				author TEXT NOT NULL,
				content TEXT NOT NULL,
				line_start INT,
				line_end INT,
				status TEXT NOT NULL DEFAULT 'open',
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`).Error; err != nil {
					return err
				}
				return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_versioned_document_comments_doc ON versioned_document_comments (document_id)`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				if err := tx.Exec(`DROP TABLE IF EXISTS versioned_document_comments`).Error; err != nil {
					return err
				}
				return tx.Exec(`DROP TABLE IF EXISTS versioned_documents`).Error
			},
		},
		{
			// Migration 052: Delete phantom bulk-import sessions created before PR #65 fix.
			ID: "052_cleanup_phantom_bulk_import_sessions",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`DELETE FROM sdk_sessions WHERE claude_session_id LIKE 'bulk-import-%' AND prompt_counter = 0`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return nil // Cannot restore deleted sessions
			},
		},
		{
			// Migration 053: Delete all vault credentials encrypted with lost key.
			// All 15 existing credentials were encrypted with an auto-generated key stored
			// in Docker ephemeral filesystem. Container recreated = key lost = data unrecoverable.
			// Current vault key (ENGRAM_VAULT_KEY env) is different. No valid credentials exist.
			// Safe to delete all — users will re-create with the current key.
			ID: "053_cleanup_dead_vault_credentials",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`DELETE FROM observations WHERE type = 'credential'`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return nil // Cannot restore encrypted data with lost key
			},
		},
		{
			// Migration 054: Add status lifecycle columns to observations.
			// Introduces status (active/resolved) and status_reason for explicit lifecycle management.
			ID: "054_observation_status_lifecycle",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.Exec(`ALTER TABLE observations ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active'`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE observations ADD COLUMN IF NOT EXISTS status_reason TEXT`).Error; err != nil {
					return err
				}
				return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_status ON observations (status)`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				tx.Exec(`DROP INDEX IF EXISTS idx_observations_status`)
				tx.Exec(`ALTER TABLE observations DROP COLUMN IF EXISTS status_reason`)
				return tx.Exec(`ALTER TABLE observations DROP COLUMN IF EXISTS status`).Error
			},
		},
		{
			// Migration 055: Backfill NULL status to 'active' for all existing observations.
			// ADD COLUMN ... DEFAULT only applies to new rows. Existing rows have NULL.
			// Dashboard status filter uses WHERE status = 'active' which misses NULLs.
			ID: "055_backfill_null_status",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`UPDATE observations SET status = 'active' WHERE status IS NULL`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return nil
			},
		},
		{
			// Migration 056: Backfill memory_type for existing store_memory observations.
			// store_memory creates observations with source_type='manual' but never set memory_type.
			// Classify based on type column and concepts JSONB content, mirroring ClassifyMemoryType() logic.
			ID: "056_backfill_memory_type",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`
				UPDATE observations SET memory_type = CASE
					WHEN type = 'guidance' THEN 'guidance'
					WHEN concepts::text ILIKE '%architecture%' OR concepts::text ILIKE '%design%' OR concepts::text ILIKE '%choice%' THEN 'decision'
					WHEN concepts::text ILIKE '%pattern%' OR concepts::text ILIKE '%best-practice%' OR concepts::text ILIKE '%anti-pattern%' THEN 'pattern'
					WHEN concepts::text ILIKE '%preference%' OR concepts::text ILIKE '%config%' OR concepts::text ILIKE '%setting%' THEN 'preference'
					WHEN concepts::text ILIKE '%style%' OR concepts::text ILIKE '%naming%' OR concepts::text ILIKE '%format%' THEN 'style'
					WHEN concepts::text ILIKE '%workflow%' OR concepts::text ILIKE '%habit%' OR concepts::text ILIKE '%routine%' THEN 'habit'
					WHEN concepts::text ILIKE '%insight%' OR concepts::text ILIKE '%discovery%' OR concepts::text ILIKE '%gotcha%' THEN 'insight'
					ELSE 'context'
				END
				WHERE source_type = 'manual'
				AND (memory_type IS NULL OR memory_type = '')
			`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`UPDATE observations SET memory_type = '' WHERE source_type = 'manual'`).Error
			},
		},
		// Migration 057: Session outcome columns — closed-loop learning Phase 1.
		// Adds outcome tracking to sdk_sessions for self-improvement feedback loop.
		{
			ID: "057_session_outcome_columns",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.Exec(`ALTER TABLE sdk_sessions ADD COLUMN IF NOT EXISTS outcome TEXT`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE sdk_sessions ADD COLUMN IF NOT EXISTS outcome_reason TEXT`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE sdk_sessions ADD COLUMN IF NOT EXISTS outcome_recorded_at TIMESTAMPTZ`).Error; err != nil {
					return err
				}
				return tx.Exec(`ALTER TABLE sdk_sessions ADD COLUMN IF NOT EXISTS injection_strategy TEXT`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				tx.Exec(`ALTER TABLE sdk_sessions DROP COLUMN IF EXISTS injection_strategy`)
				tx.Exec(`ALTER TABLE sdk_sessions DROP COLUMN IF EXISTS outcome_recorded_at`)
				tx.Exec(`ALTER TABLE sdk_sessions DROP COLUMN IF EXISTS outcome_reason`)
				return tx.Exec(`ALTER TABLE sdk_sessions DROP COLUMN IF EXISTS outcome`).Error
			},
		},
		// Migration 058: observation_injections table — tracks which observations were injected per session.
		// Foundation for closed-loop learning: correlates injections with session outcomes.
		{
			ID: "058_observation_injections_table",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.Exec(`CREATE TABLE IF NOT EXISTS observation_injections (
				id BIGSERIAL PRIMARY KEY,
				observation_id BIGINT NOT NULL,
				session_id TEXT NOT NULL,
				injection_section TEXT NOT NULL,
				injected_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_obs_injections_session ON observation_injections (session_id)`).Error; err != nil {
					return err
				}
				return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_obs_injections_obs ON observation_injections (observation_id)`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS observation_injections`).Error
			},
		},
		// Migration 059: effectiveness columns on observations — tracks injection outcome stats per observation.
		// Used by closed-loop learning Phase 2 to compute per-observation effectiveness scores.
		{
			ID: "059_observation_effectiveness_columns",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.Exec(`ALTER TABLE observations ADD COLUMN IF NOT EXISTS effectiveness_score REAL DEFAULT 0`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE observations ADD COLUMN IF NOT EXISTS effectiveness_injections INT DEFAULT 0`).Error; err != nil {
					return err
				}
				return tx.Exec(`ALTER TABLE observations ADD COLUMN IF NOT EXISTS effectiveness_successes INT DEFAULT 0`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				tx.Exec(`ALTER TABLE observations DROP COLUMN IF EXISTS effectiveness_successes`)
				tx.Exec(`ALTER TABLE observations DROP COLUMN IF EXISTS effectiveness_injections`)
				return tx.Exec(`ALTER TABLE observations DROP COLUMN IF EXISTS effectiveness_score`).Error
			},
		},
		// Migration 060: agent_observation_stats table — tracks per-agent effectiveness for each observation.
		// Used by closed-loop learning Phase 4 to personalize injection scoring per agent.
		{
			ID: "060_agent_observation_stats",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`
				CREATE TABLE IF NOT EXISTS agent_observation_stats (
					agent_id TEXT NOT NULL,
					observation_id BIGINT NOT NULL,
					injections INT NOT NULL DEFAULT 0,
					successes INT NOT NULL DEFAULT 0,
					updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					PRIMARY KEY (agent_id, observation_id)
				)
			`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS agent_observation_stats`).Error
			},
		},
		// Migration 061: observation_versions table — stores rewritten guidance narratives for A/B testing.
		// Used by closed-loop learning Phase 5 (APO-lite) to track LLM-rewritten guidance alternatives.
		{
			ID: "061_observation_versions",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.Exec(`CREATE TABLE IF NOT EXISTS observation_versions (
				id BIGSERIAL PRIMARY KEY,
				observation_id BIGINT NOT NULL,
				version INT NOT NULL DEFAULT 1,
				narrative TEXT NOT NULL,
				is_active BOOLEAN NOT NULL DEFAULT TRUE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				source TEXT NOT NULL DEFAULT 'original'
			)`).Error; err != nil {
					return err
				}
				return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_obs_versions_obs ON observation_versions (observation_id)`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS observation_versions`).Error
			},
		},
		{
			// Migration 062: Cleanup remaining phantom bulk-import sessions.
			// PR #65 stopped creating new phantom sessions. Migration 052 cleaned most.
			// This catches any remaining bulk-import-* sessions with 0 prompts.
			ID: "062_cleanup_phantom_bulk_import_sessions",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`DELETE FROM sdk_sessions WHERE claude_session_id LIKE 'bulk-import-%'`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return nil
			},
		},
		{
			ID: "063_backfill_observation_concepts",
			Migrate: func(tx *gorm.DB) error {
				// Backfill concepts for existing observations based on title/narrative keywords.
				// Uses JSONB array format: '["concept1","concept2"]'
				updates := []struct {
					concept  string
					keywords []string
				}{
					{"architecture", []string{"architecture", "design pattern", "system design", "microservice", "monolith"}},
					{"security", []string{"security", "authentication", "authorization", "CSRF", "XSS", "injection", "token", "credential"}},
					{"performance", []string{"performance", "latency", "cache", "timeout", "optimization", "slow"}},
					{"testing", []string{"test", "coverage", "assertion", "mock", "TDD"}},
					{"debugging", []string{"debug", "error", "stack trace", "fix", "bug", "regression"}},
					{"workflow", []string{"workflow", "CI/CD", "pipeline", "deployment", "process", "automation"}},
					{"api", []string{"API", "endpoint", "REST", "GraphQL", "handler", "route"}},
					{"database", []string{"database", "SQL", "migration", "schema", "query", "index", "PostgreSQL"}},
					{"configuration", []string{"config", "environment", "setting", "flag", "parameter"}},
					{"error-handling", []string{"error handling", "panic", "recover", "retry", "fallback", "circuit breaker"}},
				}

				for _, u := range updates {
					for _, kw := range u.keywords {
						// Only update observations with empty/null concepts
						tx.Exec(`
						UPDATE observations
						SET concepts = CASE
							WHEN concepts IS NULL OR concepts = '[]' OR concepts = 'null'
							THEN '["` + u.concept + `"]'::jsonb
							ELSE concepts || '["` + u.concept + `"]'::jsonb
						END
						WHERE (concepts IS NULL OR concepts = '[]' OR concepts = 'null' OR NOT concepts @> '["` + u.concept + `"]'::jsonb)
						AND (COALESCE(title, '') || ' ' || COALESCE(narrative, '')) ILIKE '%` + kw + `%'
					`)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return nil // Cannot undo keyword-based backfill
			},
		},
		{
			ID: "064_backfill_missing_concepts",
			Migrate: func(tx *gorm.DB) error {
				updates := []struct {
					concept  string
					keywords []string
				}{
					{"why-it-exists", []string{"rationale", "reason", "purpose", "motivation", "justification", "because", "in order to"}},
					{"what-changed", []string{"changed", "updated", "modified", "migrated", "upgraded", "deprecated", "removed", "added new"}},
					{"anti-pattern", []string{"anti-pattern", "antipattern", "bad practice", "should not", "forbidden", "prohibited", "never do"}},
					{"gotcha", []string{"gotcha", "unexpected", "surprising", "counterintuitive", "pitfall", "trap", "caveat", "watch out"}},
					{"trade-off", []string{"trade-off", "tradeoff", "versus", "vs ", "pros and cons", "downside", "compromise", "at the cost of"}},
				}

				for _, u := range updates {
					for _, kw := range u.keywords {
						tx.Exec(`
						UPDATE observations
						SET concepts = CASE
							WHEN concepts IS NULL OR concepts = '[]' OR concepts = 'null'
							THEN '["` + u.concept + `"]'::jsonb
							ELSE concepts || '["` + u.concept + `"]'::jsonb
						END
						WHERE (concepts IS NULL OR concepts = '[]' OR concepts = 'null' OR NOT concepts @> '["` + u.concept + `"]'::jsonb)
						AND (COALESCE(title, '') || ' ' || COALESCE(narrative, '')) ILIKE '%` + kw + `%'
					`)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return nil
			},
		},
		{
			ID: "065_reasoning_traces",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS reasoning_traces (
					id BIGSERIAL PRIMARY KEY,
					sdk_session_id TEXT NOT NULL,
					project TEXT NOT NULL DEFAULT '',
					steps JSONB NOT NULL DEFAULT '[]',
					quality_score REAL NOT NULL DEFAULT 0,
					task_context JSONB DEFAULT '{}',
					created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
					created_at_epoch BIGINT NOT NULL DEFAULT 0
				)
			`).Error; err != nil {
					return fmt.Errorf("create reasoning_traces table: %w", err)
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_reasoning_traces_session ON reasoning_traces(sdk_session_id)`).Error; err != nil {
					return fmt.Errorf("create session index: %w", err)
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_reasoning_traces_project ON reasoning_traces(project)`).Error; err != nil {
					return fmt.Errorf("create project index: %w", err)
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_reasoning_traces_quality ON reasoning_traces(quality_score)`).Error; err != nil {
					return fmt.Errorf("create quality index: %w", err)
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS reasoning_traces").Error
			},
		},
		// Migration 066: Add cited column to injection_log for citation-based effectiveness tracking.
		// Learning Memory v3: detectUtilitySignal in stop hook marks which injected observations
		// were actually cited by the agent. This feeds into PropagateCitation → effectiveness_score.
		{
			ID: "066_injection_log_cited_column",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.Exec(`ALTER TABLE injection_log ADD COLUMN IF NOT EXISTS cited BOOLEAN DEFAULT false`).Error; err != nil {
					return fmt.Errorf("add cited column: %w", err)
				}
				return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_injection_log_session_cited ON injection_log(session_id, cited)`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				tx.Exec(`DROP INDEX IF EXISTS idx_injection_log_session_cited`)
				return tx.Exec(`ALTER TABLE injection_log DROP COLUMN IF EXISTS cited`).Error
			},
		},
		{
			ID: "067_relation_temporal_validity",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`
				ALTER TABLE observation_relations
				ADD COLUMN IF NOT EXISTS valid_from TIMESTAMPTZ,
				ADD COLUMN IF NOT EXISTS valid_to TIMESTAMPTZ
			`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				tx.Exec(`ALTER TABLE observation_relations DROP COLUMN IF EXISTS valid_to`)
				return tx.Exec(`ALTER TABLE observation_relations DROP COLUMN IF EXISTS valid_from`).Error
			},
		},
		{
			ID: "068_expand_observation_type_check",
			Migrate: func(tx *gorm.DB) error {
				// Drop old CHECK constraint and create new one with entity, wiki, credential types.
				// PostgreSQL CHECK constraint names are auto-generated by GORM as "chk_observations_type".
				// If the constraint doesn't exist (already dropped), the DROP is a no-op.
				tx.Exec(`ALTER TABLE observations DROP CONSTRAINT IF EXISTS chk_observations_type`)
				return tx.Exec(`ALTER TABLE observations ADD CONSTRAINT chk_observations_type CHECK (type IN ('decision', 'bugfix', 'feature', 'refactor', 'discovery', 'change', 'guidance', 'credential', 'entity', 'wiki'))`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				tx.Exec(`ALTER TABLE observations DROP CONSTRAINT IF EXISTS chk_observations_type`)
				return tx.Exec(`ALTER TABLE observations ADD CONSTRAINT chk_observations_type CHECK (type IN ('decision', 'bugfix', 'feature', 'refactor', 'discovery', 'change', 'guidance'))`).Error
			},
		},
		{
			ID: "069_gstack_insights",
			Migrate: func(tx *gorm.DB) error {
				// Add agent_source column with default 'unknown' and CHECK constraint.
				if err := tx.Exec(`ALTER TABLE observations ADD COLUMN IF NOT EXISTS agent_source TEXT NOT NULL DEFAULT 'unknown'`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE observations ADD CONSTRAINT chk_observations_agent_source CHECK (agent_source IN ('claude-code', 'codex', 'gemini', 'other', 'unknown'))`).Error; err != nil {
					// Constraint may already exist — not fatal
					log.Warn().Err(err).Msg("agent_source CHECK constraint (may already exist)")
				}
				// Index for agent_source filtering.
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_agent_source ON observations (agent_source)`).Error; err != nil {
					return err
				}
				// Expand type CHECK to include pitfall, operational, timeline.
				if err := tx.Exec(`ALTER TABLE observations DROP CONSTRAINT IF EXISTS chk_observations_type`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE observations ADD CONSTRAINT chk_observations_type CHECK (type IN ('decision', 'bugfix', 'feature', 'refactor', 'discovery', 'change', 'guidance', 'credential', 'entity', 'wiki', 'pitfall', 'operational', 'timeline'))`).Error; err != nil {
					return err
				}
				// Backfill: entity/wiki observations with unknown source_type → llm_derived.
				return tx.Exec(`UPDATE observations SET source_type = 'llm_derived' WHERE type IN ('entity', 'wiki') AND source_type = 'unknown'`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				// Restore previous type CHECK constraint
				if err := tx.Exec(`ALTER TABLE observations DROP CONSTRAINT IF EXISTS chk_observations_type`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE observations ADD CONSTRAINT chk_observations_type CHECK (type IN ('decision', 'bugfix', 'feature', 'refactor', 'discovery', 'change', 'guidance', 'credential', 'entity', 'wiki'))`).Error; err != nil {
					return err
				}
				tx.Exec(`ALTER TABLE observations DROP CONSTRAINT IF EXISTS chk_observations_agent_source`)
				tx.Exec(`DROP INDEX IF EXISTS idx_observations_agent_source`)
				// Note: source_type backfill (entity/wiki → llm_derived) is intentionally not rolled back
				// as llm_derived is more accurate than unknown for these types.
				return tx.Exec(`ALTER TABLE observations DROP COLUMN IF EXISTS agent_source`).Error
			},
		},
		{
			ID: "070_agent_issues",
			Migrate: func(tx *gorm.DB) error {
				// Create issues table
				if err := tx.Exec(`CREATE TABLE IF NOT EXISTS issues (
					id BIGSERIAL PRIMARY KEY,
					title TEXT NOT NULL,
					body TEXT,
					status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'acknowledged', 'resolved', 'reopened')),
					priority TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('critical', 'high', 'medium', 'low')),
					source_project TEXT NOT NULL,
					target_project TEXT NOT NULL,
					source_agent TEXT,
					created_by_session TEXT,
					labels JSONB DEFAULT '[]',
					acknowledged_at TIMESTAMPTZ,
					resolved_at TIMESTAMPTZ,
					reopened_at TIMESTAMPTZ,
					created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
				)`).Error; err != nil {
					return err
				}
				// Indexes for issues
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_target_status ON issues (target_project, status)`).Error; err != nil {
					return err
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_source_project ON issues (source_project)`).Error; err != nil {
					return err
				}
				// Create issue_comments table
				if err := tx.Exec(`CREATE TABLE IF NOT EXISTS issue_comments (
					id BIGSERIAL PRIMARY KEY,
					issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
					author_project TEXT NOT NULL,
					author_agent TEXT,
					body TEXT NOT NULL,
					created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
				)`).Error; err != nil {
					return err
				}
				return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_issue_comments_issue_created ON issue_comments (issue_id, created_at)`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				tx.Exec(`DROP TABLE IF EXISTS issue_comments`)
				return tx.Exec(`DROP TABLE IF EXISTS issues`).Error
			},
		},
		{
			ID: "071_issues_lifecycle_v2",
			Migrate: func(tx *gorm.DB) error {
				// Add closed_at column
				if err := tx.Exec(`ALTER TABLE issues ADD COLUMN IF NOT EXISTS closed_at TIMESTAMPTZ`).Error; err != nil {
					return err
				}
				// Update status CHECK constraint to include 'closed' and 'rejected'
				if err := tx.Exec(`ALTER TABLE issues DROP CONSTRAINT IF EXISTS issues_status_check`).Error; err != nil {
					return err
				}
				return tx.Exec(`ALTER TABLE issues ADD CONSTRAINT issues_status_check CHECK (status IN ('open', 'acknowledged', 'resolved', 'reopened', 'closed', 'rejected'))`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				tx.Exec(`ALTER TABLE issues DROP CONSTRAINT IF EXISTS issues_status_check`)
				tx.Exec(`ALTER TABLE issues ADD CONSTRAINT issues_status_check CHECK (status IN ('open', 'acknowledged', 'resolved', 'reopened'))`)
				return tx.Exec(`ALTER TABLE issues DROP COLUMN IF EXISTS closed_at`).Error
			},
		},
		{
			ID: "072_sessions_utility_propagated_at",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE sdk_sessions ADD COLUMN IF NOT EXISTS utility_propagated_at TIMESTAMPTZ`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE sdk_sessions DROP COLUMN IF EXISTS utility_propagated_at`).Error
			},
		},

		// Migration 073: partial index on sdk_sessions.utility_propagated_at.
		// Improves query performance of recordPendingOutcomes maintenance guard which filters
		// on this column (WHERE utility_propagated_at > NOW() - INTERVAL '2 hours').
		// Partial index covers only non-null rows, keeping the index small.
		{
			ID: "073_sessions_utility_propagated_at_index",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_sdk_sessions_utility_propagated_at
ON sdk_sessions (utility_propagated_at)
WHERE utility_propagated_at IS NOT NULL`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`DROP INDEX IF EXISTS idx_sdk_sessions_utility_propagated_at`).Error
			},
		},
		{
			ID: "074_observations_commands_run",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE observations ADD COLUMN IF NOT EXISTS commands_run JSONB NOT NULL DEFAULT '[]'::jsonb`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE observations DROP COLUMN IF EXISTS commands_run`).Error
			},
		},
		// Migration 075: Add type column to issues for categorisation (bug/feature/improvement/task).
		{
			ID: "075_issues_type",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE issues ADD COLUMN IF NOT EXISTS type TEXT NOT NULL DEFAULT 'task'`,
					`ALTER TABLE issues DROP CONSTRAINT IF EXISTS issues_type_check`,
					`ALTER TABLE issues ADD CONSTRAINT issues_type_check CHECK (type IN ('bug', 'feature', 'improvement', 'task'))`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return err
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				if err := tx.Exec(`ALTER TABLE issues DROP CONSTRAINT IF EXISTS issues_type_check`).Error; err != nil {
					return err
				}
				return tx.Exec(`ALTER TABLE issues DROP COLUMN IF EXISTS type`).Error
			},
		},
		// Migration 076: Multilingual FTS — combine english + simple dictionaries.
		{
			ID: "076_observations_fts_multilang",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE observations DROP COLUMN IF EXISTS search_vector`,
					`ALTER TABLE observations ADD COLUMN search_vector tsvector
					 GENERATED ALWAYS AS (
					   to_tsvector('english', COALESCE(title, '') || ' ' || COALESCE(subtitle, '') || ' ' || COALESCE(narrative, ''))
					   || to_tsvector('simple',  COALESCE(title, '') || ' ' || COALESCE(subtitle, '') || ' ' || COALESCE(narrative, ''))
					 ) STORED`,
					`CREATE INDEX IF NOT EXISTS idx_observations_search_vector ON observations USING GIN (search_vector)`,
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
					`DROP INDEX IF EXISTS idx_observations_search_vector`,
					`ALTER TABLE observations DROP COLUMN IF EXISTS search_vector`,
					`ALTER TABLE observations ADD COLUMN search_vector tsvector
					 GENERATED ALWAYS AS (
					   to_tsvector('english', COALESCE(title, '') || ' ' || COALESCE(subtitle, '') || ' ' || COALESCE(narrative, ''))
					 ) STORED`,
					`CREATE INDEX IF NOT EXISTS idx_observations_fts ON observations USING GIN (search_vector)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						continue
					}
				}
				return nil
			},
		},
		{
			ID: "077_relations_constraints_update",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Expand relation_type constraint to include all valid types:
					// original 17 from migration 019 + modifies/reads (file-relation detector)
					// + follows/prompted_by/references/referenced_by (detector.go FR-4,5,36)
					`ALTER TABLE observation_relations DROP CONSTRAINT IF EXISTS chk_observation_relations_relation_type`,
					`ALTER TABLE observation_relations ADD CONSTRAINT chk_observation_relations_relation_type CHECK (relation_type IN ('causes','fixes','supersedes','depends_on','relates_to','evolves_from','leads_to','similar_to','contradicts','reinforces','invalidated_by','explains','shares_theme','parallel_context','summarizes','part_of','prefers_over','modifies','reads','follows','prompted_by','references','referenced_by'))`,
					// Add creative_association back to detection_source constraint (used by consolidation)
					`ALTER TABLE observation_relations DROP CONSTRAINT IF EXISTS chk_observation_relations_detection_source`,
					`ALTER TABLE observation_relations ADD CONSTRAINT chk_observation_relations_detection_source CHECK (detection_source IN ('file_overlap','embedding_similarity','temporal_proximity','narrative_mention','concept_overlap','type_progression','creative_association'))`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 077: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				// Migration 077 is intentionally irreversible.
				//
				// Restoring the narrower CHECK constraints requires that no rows in
				// observation_relations use the new relation_type values
				// ('modifies', 'reads', 'follows', 'prompted_by', 'references',
				// 'referenced_by') or the detection_source value 'creative_association'.
				// Once the server has run with migration 077 applied, such rows may
				// exist, and ALTER TABLE … ADD CONSTRAINT would fail on real data.
				//
				// To roll back manually:
				//   1. DELETE / UPDATE rows using the new types.
				//   2. Re-run the ALTER TABLE statements from the original migration 019.
				return fmt.Errorf("migration 077 rollback: irreversible — rows with expanded relation_type or detection_source values may exist; manual data migration required before restoring constraints")
			},
		},
		{
			// Hotfix: merge duplicate project slugs.
			// Full audit 2026-04-14: filesystem scan + hash verification.
			// Three categories: confirmed merges, worktree merges, orphans.
			ID: "078_merge_duplicate_project_slugs",
			Migrate: func(tx *gorm.DB) error {
				// Category 1: Simple duplicates (dirName_hash → dirName, both exist)
				// Category 2: Worktree/subdir slugs → canonical repo name
				// Category 3: Renamed repos → current name
				pairs := [][2]string{
					// Simple duplicates (confirmed via filesystem + hash match)
					{"aimux_16b1f601", "aimux"},
					{"engram_67e398f8", "engram"},
					{"mcp-mux_a1777ae2", "mcp-mux"},
					{"mcp-aimux_f5ee22ee", "mcp-aimux"},
					{"nvmd-ai-kit_a01eaad6", "nvmd-ai-kit"},
					{"nvmd-devops_4a8aca29", "nvmd-devops"},
					{"pr-review-mcp_b0213bae", "pr-review-mcp"},
					{"media-scripts-parser_0c4985f2", "media-scripts-parser"},
					{"netcoredbg-mcp_9c2553be", "netcoredbg-mcp"},
					{"nvmd-transcoder_8786eaaa", "nvmd-transcoder"},
					{"openclaw_9e472fe0", "openclaw"},
					{"blueprint-any_fd95fb72", "blueprint-any"},
					{"amneziawg-scripts_dd197c", "amneziawg-scripts"},

					// awg-mesh: 3 different remote hashes, all same repo
					{"awg-mesh_67aca97d", "awg-mesh"},
					{"awg-mesh_689ee718", "awg-mesh"},
					{"awg-mesh_7918ee58", "awg-mesh"},

					// awg-mesh worktrees (hash 7918ee58 = thebtf/awg-mesh.git)
					{"phase-1-awg_7918ee58", "awg-mesh"},
					{"transport-overlay_7918ee58", "awg-mesh"},

					// nvmdfs worktrees (hash dcce5a1a = thebtf/nvmdfs.git)
					{"nvmdfs_dcce5a1a", "nvmdfs"},
					{"v08-all-driver-splits_dcce5a1a", "nvmdfs"},
					{"v10-e2e-infra-p1_dcce5a1a", "nvmdfs"},
					{"v10-e2e-phase2_dcce5a1a", "nvmdfs"},

					// engram ui/ subdir (hash f307f9b6 = engram.git + ui/)
					{"ui_f307f9b6", "engram"},

					// Renamed repos: nvmd-ai-kg → media-scripts-parser
					{"parser_522ebee5", "media-scripts-parser"},
					{"parser_eeb282", "media-scripts-parser"},

					// terraform worktree/subdir of nvmd-devops
					{"terraform_3db7e669", "nvmd-devops"},

					// v10-e2e-phase3: likely nvmdfs worktree (different hash = different remote era)
					{"v10-e2e-phase3_288a1664", "nvmdfs"},
				}
				for _, p := range pairs {
					oldSlug, canonical := p[0], p[1]

					// Observations
					if err := tx.Exec("UPDATE observations SET project = ? WHERE project = ?", canonical, oldSlug).Error; err != nil {
						return fmt.Errorf("migration 078: observations %s→%s: %w", oldSlug, canonical, err)
					}
					// Issues: source_project, target_project
					if err := tx.Exec("UPDATE issues SET source_project = ? WHERE source_project = ?", canonical, oldSlug).Error; err != nil {
						return fmt.Errorf("migration 078: issues.source %s→%s: %w", oldSlug, canonical, err)
					}
					if err := tx.Exec("UPDATE issues SET target_project = ? WHERE target_project = ?", canonical, oldSlug).Error; err != nil {
						return fmt.Errorf("migration 078: issues.target %s→%s: %w", oldSlug, canonical, err)
					}
					// Projects: add old slug to legacy_ids of canonical, then remove duplicate row
					tx.Exec(`UPDATE projects SET legacy_ids = array_append(legacy_ids, ?)
					          WHERE id = ? AND NOT (COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[])`,
						oldSlug, canonical, oldSlug)
					tx.Exec("DELETE FROM projects WHERE id = ?", oldSlug)
				}
				// Orphans: repos deleted or on other machines. Strip hash, keep dirName.
				// No canonical to merge into — just clean the slug.
				orphans := []string{
					"simulation_4680c737",
					"skills_658c5076",
					"talos_d9ce186e",
					"workspace_af2a6d",
				}
				for _, slug := range orphans {
					// Extract dirName (everything before last _hexhash)
					clean := slug
					for i := len(slug) - 1; i >= 0; i-- {
						if slug[i] == '_' {
							clean = slug[:i]
							break
						}
					}
					tx.Exec("UPDATE observations SET project = ? WHERE project = ?", clean, slug)
					tx.Exec("UPDATE issues SET source_project = ? WHERE source_project = ?", clean, slug)
					tx.Exec("UPDATE issues SET target_project = ? WHERE target_project = ?", clean, slug)
					tx.Exec("DELETE FROM projects WHERE id = ?", slug)
				}

				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return fmt.Errorf("migration 078 rollback: irreversible — observations already reassociated")
			},
		},
		{
			// Follow-up: missed pairs from 078 + stale project table rows.
			ID: "079_merge_duplicate_projects_followup",
			Migrate: func(tx *gorm.DB) error {
				// Missed merge pairs
				pairs := [][2]string{
					{"novascript_d219e203", "novascript"},
					{"nvmd-ai-kg_76a6d364", "media-scripts-parser"},
					{"nvmd-devops_01be8f28", "nvmd-devops"},
				}
				for _, p := range pairs {
					oldSlug, canonical := p[0], p[1]
					tx.Exec("UPDATE observations SET project = ? WHERE project = ?", canonical, oldSlug)
					tx.Exec("UPDATE issues SET source_project = ? WHERE source_project = ?", canonical, oldSlug)
					tx.Exec("UPDATE issues SET target_project = ? WHERE target_project = ?", canonical, oldSlug)
					tx.Exec(`UPDATE projects SET legacy_ids = array_append(legacy_ids, ?)
					          WHERE id = ? AND NOT (COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[])`,
						oldSlug, canonical, oldSlug)
				}

				// Safe-delete stale project rows: only delete hash-slug row
				// if the canonical row exists (prevents orphaning).
				// Format: [hash-slug, canonical] — delete hash-slug only if canonical exists.
				safeDeletions := [][2]string{
					{"aimux_16b1f601", "aimux"},
					{"novascript_d219e203", "novascript"},
					{"nvmd-ai-kg_76a6d364", "media-scripts-parser"},
					{"nvmd-devops_01be8f28", "nvmd-devops"},
					{"engram_67e398f8", "engram"},
					{"mcp-mux_a1777ae2", "mcp-mux"},
					{"mcp-aimux_f5ee22ee", "mcp-aimux"},
					{"nvmd-ai-kit_a01eaad6", "nvmd-ai-kit"},
					{"nvmd-devops_4a8aca29", "nvmd-devops"},
					{"pr-review-mcp_b0213bae", "pr-review-mcp"},
					{"media-scripts-parser_0c4985f2", "media-scripts-parser"},
					{"netcoredbg-mcp_9c2553be", "netcoredbg-mcp"},
					{"nvmd-transcoder_8786eaaa", "nvmd-transcoder"},
					{"openclaw_9e472fe0", "openclaw"},
					{"blueprint-any_fd95fb72", "blueprint-any"},
					{"amneziawg-scripts_dd197c", "amneziawg-scripts"},
					{"awg-mesh_67aca97d", "awg-mesh"},
					{"awg-mesh_689ee718", "awg-mesh"},
					{"awg-mesh_7918ee58", "awg-mesh"},
					{"phase-1-awg_7918ee58", "awg-mesh"},
					{"transport-overlay_7918ee58", "awg-mesh"},
					{"nvmdfs_dcce5a1a", "nvmdfs"},
					{"v08-all-driver-splits_dcce5a1a", "nvmdfs"},
					{"v10-e2e-infra-p1_dcce5a1a", "nvmdfs"},
					{"v10-e2e-phase2_dcce5a1a", "nvmdfs"},
					{"ui_f307f9b6", "engram"},
					{"parser_522ebee5", "media-scripts-parser"},
					{"parser_eeb282", "media-scripts-parser"},
					{"terraform_3db7e669", "nvmd-devops"},
					{"v10-e2e-phase3_288a1664", "nvmdfs"},
				}
				for _, p := range safeDeletions {
					stale, canonical := p[0], p[1]
					// Only delete stale row if canonical row exists
					tx.Exec(`DELETE FROM projects WHERE id = ? AND EXISTS (SELECT 1 FROM projects p2 WHERE p2.id = ?)`,
						stale, canonical)
				}

				// Orphan rows where no canonical exists: rename in-place (strip hash).
				// These have no clean counterpart — the row IS the only record.
				orphanRenames := [][2]string{
					{"simulation_4680c737", "simulation"},
					{"skills_658c5076", "skills"},
					{"talos_d9ce186e", "talos"},
					{"workspace_af2a6d", "workspace"},
				}
				for _, p := range orphanRenames {
					oldID, newID := p[0], p[1]
					// Rename project row (UPDATE id) — only if newID doesn't already exist
					tx.Exec(`UPDATE projects SET id = ? WHERE id = ? AND NOT EXISTS (SELECT 1 FROM projects p2 WHERE p2.id = ?)`,
						newID, oldID, newID)
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return fmt.Errorf("migration 079 rollback: irreversible")
			},
		},
		{
			ID: "080_create_auth_tables",
			Migrate: func(tx *gorm.DB) error {
				// Users table
				if err := tx.Exec(`CREATE TABLE IF NOT EXISTS users (
					id SERIAL PRIMARY KEY,
					email VARCHAR(255) NOT NULL UNIQUE,
					password_hash VARCHAR(255) NOT NULL DEFAULT '',
					role VARCHAR(20) NOT NULL DEFAULT 'operator',
					disabled BOOLEAN NOT NULL DEFAULT false,
					created_at TIMESTAMP NOT NULL DEFAULT NOW(),
					last_login_at TIMESTAMP
				)`).Error; err != nil {
					return err
				}
				// Invitations table
				if err := tx.Exec(`CREATE TABLE IF NOT EXISTS invitations (
					id SERIAL PRIMARY KEY,
					code VARCHAR(64) NOT NULL UNIQUE,
					created_by INTEGER NOT NULL REFERENCES users(id),
					used_by INTEGER REFERENCES users(id),
					used_at TIMESTAMP,
					created_at TIMESTAMP NOT NULL DEFAULT NOW()
				)`).Error; err != nil {
					return err
				}
				// Sessions table
				return tx.Exec(`CREATE TABLE IF NOT EXISTS sessions (
					id VARCHAR(64) PRIMARY KEY,
					user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					created_at TIMESTAMP NOT NULL DEFAULT NOW(),
					expires_at TIMESTAMP NOT NULL
				)`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				tx.Exec("DROP TABLE IF EXISTS sessions")
				tx.Exec("DROP TABLE IF EXISTS invitations")
				tx.Exec("DROP TABLE IF EXISTS users")
				return nil
			},
		},
		{
			// Migration 081: project identity redesign — pure hash IDs.
			//
			// Background: ResolveProjectSlug previously returned "dirName_hash8" (git) or
			// "dirName_hash6" (non-git). It now returns a pure hash without the dirName prefix.
			// Migrations 078/079 already consolidated most duplicate slugs into clean dirName-only
			// project IDs.  This migration handles any remaining "dirName_hashN" rows that survived
			// (e.g. projects added after 079 but before this upgrade) and adds the display_name
			// column so the dirName is preserved for UI display.
			//
			// Algorithm (explicit mapping, not regex):
			//   1. Find all project rows whose id contains an underscore AND whose last
			//      underscore-separated segment is a 6- or 8-char lowercase hex string.
			//   2. Re-associate observations and issues to the pure hash.
			//   3. Rename the project row (UPDATE projects SET id = hash WHERE ...).
			//   4. Persist the old slug as a legacy_id and the dirName as display_name.
			//
			// Collision guard: if a row with the pure hash ID already exists (e.g. because
			// the new client already wrote one), the old row is merged into it instead.
			ID: "081_project_identity_pure_hash",
			Migrate: func(tx *gorm.DB) error {
				// Add display_name column — idempotent.
				if err := tx.Exec(`ALTER TABLE projects ADD COLUMN IF NOT EXISTS display_name VARCHAR(255)`).Error; err != nil {
					return fmt.Errorf("migration 081: add display_name column: %w", err)
				}

				// Collect all project IDs that look like "dirName_hashN".
				var rows []struct {
					ID string `gorm:"column:id"`
				}
				if err := tx.Raw("SELECT id FROM projects").Scan(&rows).Error; err != nil {
					return fmt.Errorf("migration 081: list projects: %w", err)
				}

				for _, row := range rows {
					lastUnderscore := strings.LastIndex(row.ID, "_")
					if lastUnderscore < 0 {
						continue // no underscore — already a clean name or pure hash
					}
					hashPart := row.ID[lastUnderscore+1:]
					dirPart := row.ID[:lastUnderscore]

					// Validate: hash segment must be exactly 6 or 8 lowercase hex chars.
					if len(hashPart) != 8 && len(hashPart) != 6 {
						continue
					}
					isHex := true
					for _, c := range hashPart {
						if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
							isHex = false
							break
						}
					}
					if !isHex {
						continue
					}

					oldID := row.ID
					newID := hashPart
					dirName := dirPart

					// Check whether a row with the pure hash already exists.
					var existingCount int64
					tx.Raw("SELECT COUNT(*) FROM projects WHERE id = ?", newID).Scan(&existingCount)

					if existingCount > 0 {
						// Pure-hash row already exists — merge old row into it.
						tx.Exec("UPDATE observations SET project = ? WHERE project = ?", newID, oldID)
						tx.Exec("UPDATE issues SET source_project = ? WHERE source_project = ?", newID, oldID)
						tx.Exec("UPDATE issues SET target_project = ? WHERE target_project = ?", newID, oldID)
						tx.Exec("UPDATE raw_events SET project = ? WHERE project = ?", newID, oldID)
						tx.Exec("UPDATE indexed_sessions SET project_id = ? WHERE project_id = ?", newID, oldID)
						tx.Exec("UPDATE user_prompts SET project = ? WHERE project = ?", newID, oldID)
						tx.Exec("UPDATE injection_log SET project = ? WHERE project = ?", newID, oldID)
						tx.Exec("UPDATE reasoning_traces SET project = ? WHERE project = ?", newID, oldID)
						tx.Exec(`UPDATE projects SET
							legacy_ids   = array_append(legacy_ids, ?),
							display_name = COALESCE(NULLIF(display_name, ''), ?)
						WHERE id = ? AND NOT (COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[])`,
							oldID, dirName, newID, oldID)
						tx.Exec("DELETE FROM projects WHERE id = ?", oldID)
					} else {
						// No pure-hash row yet — rename in-place.
						tx.Exec("UPDATE observations SET project = ? WHERE project = ?", newID, oldID)
						tx.Exec("UPDATE issues SET source_project = ? WHERE source_project = ?", newID, oldID)
						tx.Exec("UPDATE issues SET target_project = ? WHERE target_project = ?", newID, oldID)
						// Update all other tables with project column
						tx.Exec("UPDATE raw_events SET project = ? WHERE project = ?", newID, oldID)
						tx.Exec("UPDATE indexed_sessions SET project_id = ? WHERE project_id = ?", newID, oldID)
						tx.Exec("UPDATE user_prompts SET project = ? WHERE project = ?", newID, oldID)
						tx.Exec("UPDATE injection_log SET project = ? WHERE project = ?", newID, oldID)
						tx.Exec("UPDATE reasoning_traces SET project = ? WHERE project = ?", newID, oldID)
						tx.Exec(`UPDATE projects SET
							id           = ?,
							legacy_ids   = array_append(COALESCE(legacy_ids, ARRAY[]::TEXT[]), ?),
							display_name = COALESCE(NULLIF(display_name, ''), ?)
						WHERE id = ?`,
							newID, oldID, dirName, oldID)
					}
				}
				// Phase 2: For already-clean name-only projects (from migrations 078/079),
				// add the clean name to legacy_ids of any matching hash-based project.
				// These projects have no underscore+hash suffix — they were already
				// consolidated. The client now sends pure hashes, so "engram" needs
				// to resolve to the hash. We add "engram" to legacy_ids of the hash
				// row (if one was just created from "engram_67e398f8" above).
				// If no hash row exists, the clean name stays as-is — it will be
				// updated naturally on the next session start when the client sends
				// the hash and UpsertProject creates the mapping.
				for _, row := range rows {
					if strings.Contains(row.ID, "_") {
						continue // already handled above
					}
					if strings.HasPrefix(row.ID, "/") || strings.HasPrefix(row.ID, "D:") || strings.HasPrefix(row.ID, "C:") {
						continue // absolute path — skip
					}
					if len(row.ID) == 6 || len(row.ID) == 8 {
						// Already looks like a pure hash — skip
						isHex := true
						for _, c := range row.ID {
							if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
								isHex = false
								break
							}
						}
						if isHex {
							continue
						}
					}
					// This is a clean name like "engram" — set it as display_name
					// if not already set, and ensure legacy_ids includes it.
					tx.Exec(`UPDATE projects SET
						display_name = COALESCE(NULLIF(display_name, ''), ?)
					WHERE id = ?`, row.ID, row.ID)
				}

				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				// Partial rollback: drops the display_name column only.
				// The project ID normalisation (UUID generation, legacy_ids population)
				// is intentionally not reversed — doing so would require storing the
				// original IDs before the migration ran, which we did not persist.
				tx.Exec("ALTER TABLE projects DROP COLUMN IF EXISTS display_name")
				return nil
			},
		},

		// Migration 082: Project lifecycle columns for soft-delete and heartbeat tracking.
		// Adds removed_at (soft-delete timestamp) and last_heartbeat (daemon activity tracking)
		// to the projects table. Strictly additive — no data is modified.
		{
			ID: "082_projects_lifecycle",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					`ALTER TABLE projects ADD COLUMN IF NOT EXISTS removed_at TIMESTAMPTZ NULL`,
					`ALTER TABLE projects ADD COLUMN IF NOT EXISTS last_heartbeat TIMESTAMPTZ DEFAULT NOW()`,
					`CREATE INDEX IF NOT EXISTS idx_projects_removed_at ON projects(removed_at) WHERE removed_at IS NOT NULL`,
					`CREATE INDEX IF NOT EXISTS idx_projects_last_heartbeat ON projects(last_heartbeat)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 082: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`DROP INDEX IF EXISTS idx_projects_last_heartbeat`,
					`DROP INDEX IF EXISTS idx_projects_removed_at`,
					`ALTER TABLE projects DROP COLUMN IF EXISTS last_heartbeat`,
					`ALTER TABLE projects DROP COLUMN IF EXISTS removed_at`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 082 rollback: %w", err)
					}
				}
				return nil
			},
		},
		// Migration 083: Drop session_observation_injections orphan join table.
		// This table was created by migration 028 for per-session injection tracking but
		// the citation-tracking code path (S16 arch decision) is dead. The table is never
		// queried in production and contains only derived data. Safe to drop unconditionally.
		{
			ID: "083_drop_session_observation_injections",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS session_observation_injections`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS session_observation_injections (
						id BIGSERIAL PRIMARY KEY,
						session_id BIGINT NOT NULL REFERENCES sdk_sessions(id) ON DELETE CASCADE,
						observation_id BIGINT NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
						injected_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
						UNIQUE(session_id, observation_id)
					)`,
					`CREATE INDEX IF NOT EXISTS idx_soi_session_id ON session_observation_injections(session_id)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 083 rollback: %w", err)
					}
				}
				return nil
			},
		},

		// Migration 084: Drop injection_log orphan table.
		// This table was created by migration 046 for citation-based effectiveness tracking.
		// The citation-tracking code path is dead (S16 arch decision — effectiveness_score
		// columns on observations replaced injection_log as the source of truth). The table
		// is never queried in production and contains only derived data.
		{
			ID: "084_drop_injection_log",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS injection_log`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				// Recreate the final-state schema (migration 046 baseline + migration 066 additions).
				// Migration 066 added `cited BOOLEAN` column + `idx_injection_log_session_cited` index
				// for Learning Memory v3 citation-based effectiveness tracking. Rollback must restore
				// both — otherwise a post-rollback database would be missing schema that existed
				// immediately before DROP, breaking downstream migration replay.
				sqls := []string{
					`CREATE TABLE IF NOT EXISTS injection_log (
						id BIGSERIAL PRIMARY KEY,
						observation_id BIGINT NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
						project TEXT NOT NULL DEFAULT '',
						task_context TEXT NOT NULL DEFAULT '',
						session_id TEXT NOT NULL DEFAULT '',
						created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
						cited BOOLEAN DEFAULT false
					)`,
					`CREATE INDEX IF NOT EXISTS idx_injection_log_observation_id ON injection_log(observation_id)`,
					`CREATE INDEX IF NOT EXISTS idx_injection_log_project ON injection_log(project)`,
					`CREATE INDEX IF NOT EXISTS idx_injection_log_created_at ON injection_log(created_at)`,
					`CREATE INDEX IF NOT EXISTS idx_injection_log_session_cited ON injection_log(session_id, cited)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 084 rollback: %w", err)
					}
				}
				return nil
			},
		},

		// Migration 085: Drop content_chunks table (US2 — embeddings storage removal).
		// content_chunks held document chunk embeddings (hash x seq -> vector(384)).
		// The entire embeddings pipeline (internal/vector, internal/embedding) is dropped in v5.
		// The content table (and documents table) are PROTECTED INVARIANTS — they remain untouched.
		// Rollback recreates the DDL-faithful schema from migration 017 + migration 029.
		{
			ID: "085_drop_content_chunks",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`DROP TABLE IF EXISTS content_chunks CASCADE`).Error
			},
			// Rollback is intentionally irreversible: the entire embedding pipeline
			// (internal/vector, internal/embedding, pgvector-go dependency) was removed
			// in v5. Recreating the DDL would produce a schema that no running code can
			// populate, and the correct dimension varied per installation (migration 020
			// allowed overriding it), so vector(384) would be wrong for many deployments.
			// Downgrading past this migration requires restoring from a pre-v5 backup.
			Rollback: func(*gorm.DB) error {
				return fmt.Errorf("migration 085 rollback is not supported: " +
					"content_chunks table and the embedding pipeline were permanently removed in v5; " +
					"restore from a pre-v5 backup to downgrade")
			},
		},
		// Migration 086: drop used_vector column from search_query_log.
		// v5 FTS-only mode means every logged search has used_vector=false; the column
		// is now pure noise diverging from the in-memory RecentSearchQuery contract.
		{
			ID: "086_drop_search_query_log_used_vector",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE search_query_log DROP COLUMN IF EXISTS used_vector`).Error
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Exec(`ALTER TABLE search_query_log ADD COLUMN IF NOT EXISTS used_vector BOOL NOT NULL DEFAULT false`).Error
			},
		},

		// Migration 087: create dedicated credentials table (US3 — vault location correction).
		//
		// Background (spec.md §C5 / §S16): pre-v5 vault credentials were stored as rows
		// in the observations table (type='credential') using two special columns added by
		// migrations 1078–1079 (encrypted_secret BYTEA + encryption_key_fingerprint TEXT).
		// v5 splits observations into purpose-built tables; this migration creates the
		// credentials table that will receive those rows in a later migration (088+).
		//
		// NOTE on migration ID: spec.md text says "077_credentials" but that ID is already
		// taken (077_relations_constraints_update). US1+US2 consumed 083–086 (bumped from
		// original 081–084 because 081+082 were also taken). This PR uses 087 as the
		// next-free ID. Downstream migrations (data migration, observations drop) use 088+.
		//
		// Schema source: spec.md §Data Model §credentials (authoritative).
		// Column names match existing observations.encrypted_secret /
		// observations.encryption_key_fingerprint verbatim so the future data migration
		// (088) can COPY the bytes directly without re-encryption.
		{
			ID: "087_credentials",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Main table — UNIQUE(project, key) prevents duplicate credential names
					// per project. deleted_at NULL = active row; soft-delete sets it to NOW().
					`CREATE TABLE IF NOT EXISTS credentials (
						id                        BIGSERIAL PRIMARY KEY,
						project                   TEXT NOT NULL,
						key                       TEXT NOT NULL,
						encrypted_secret          BYTEA NOT NULL,
						encryption_key_fingerprint TEXT NOT NULL,
						scope                     TEXT,
						version                   INTEGER NOT NULL DEFAULT 1,
						edited_by                 TEXT,
						created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
						updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
						deleted_at                TIMESTAMPTZ,
						UNIQUE(project, key)
					)`,
					// Partial index on project — only active rows (deleted_at IS NULL).
					// Supports per-project list queries efficiently.
					`CREATE INDEX IF NOT EXISTS idx_credentials_project
						ON credentials (project)
						WHERE deleted_at IS NULL`,
					// Partial index on fingerprint — supports vault key rotation checks
					// (count / delete rows whose fingerprint differs from the current key).
					`CREATE INDEX IF NOT EXISTS idx_credentials_fingerprint
						ON credentials (encryption_key_fingerprint)
						WHERE deleted_at IS NULL`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 087_credentials: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`DROP INDEX IF EXISTS idx_credentials_fingerprint`,
					`DROP INDEX IF EXISTS idx_credentials_project`,
					`DROP TABLE IF EXISTS credentials`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 087_credentials rollback: %w", err)
					}
				}
				return nil
			},
		},

		// Migration 088: memories table (CREATE TABLE only — data migration from
		// observations happens in a later commit per US3 scope boundary).
		//
		// NOTE on migration ID: spec.md text says "078_memories" but that ID is already
		// taken. Commit A (087_credentials) consumed the next-free slot after US1+US2.
		// This PR uses 088 as the next-free ID. 089 is behavioral_rules.
		//
		// Schema source: spec.md §Data Model §memories (authoritative — Option C extended).
		// Dual-dictionary search_vector (english + simple) per migration 076 pattern.
		// No importance_score / effectiveness_* / inject_count — per S1 (scoring dropped).
		{
			ID: "088_memories",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Main table — project-scoped, soft-delete via deleted_at.
					// search_vector is a GENERATED ALWAYS AS STORED tsvector using dual
					// dictionary (english + simple) for multilingual support.
					`CREATE TABLE IF NOT EXISTS memories (
						id            BIGSERIAL PRIMARY KEY,
						project       TEXT NOT NULL,
						content       TEXT NOT NULL,
						tags          JSONB NOT NULL DEFAULT '[]',
						source_agent  TEXT,
						version       INTEGER NOT NULL DEFAULT 1,
						edited_by     TEXT,
						created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
						updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
						deleted_at    TIMESTAMPTZ,
						search_vector tsvector GENERATED ALWAYS AS (
							to_tsvector('english', COALESCE(content, '')) ||
							to_tsvector('simple',  COALESCE(content, ''))
						) STORED
					)`,
					// Partial composite index on (project, created_at DESC) — only active rows.
					// Supports per-project recency queries efficiently (session-start top-N).
					`CREATE INDEX IF NOT EXISTS idx_memories_project_created
						ON memories (project, created_at DESC)
						WHERE deleted_at IS NULL`,
					// GIN index on search_vector for full-text search.
					`CREATE INDEX IF NOT EXISTS idx_memories_fts
						ON memories USING GIN (search_vector)`,
					// GIN index on tags JSONB for tag-based filtering.
					`CREATE INDEX IF NOT EXISTS idx_memories_tags
						ON memories USING GIN (tags)`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 088_memories: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`DROP INDEX IF EXISTS idx_memories_tags`,
					`DROP INDEX IF EXISTS idx_memories_fts`,
					`DROP INDEX IF EXISTS idx_memories_project_created`,
					`DROP TABLE IF EXISTS memories`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 088_memories rollback: %w", err)
					}
				}
				return nil
			},
		},

		// Migration 089: behavioral_rules table (CREATE TABLE only — data migration from
		// observations happens in a later commit per US3 scope boundary).
		//
		// NOTE on migration ID: spec.md text says "079_behavioral_rules" but that ID is
		// taken. Uses 089 as the next-free ID after 088_memories.
		//
		// Schema source: spec.md §Data Model §behavioral_rules (authoritative — Option C extended).
		// project is NULLable: NULL = global rule (applies to every session regardless of project).
		// priority determines order in session-start inject (higher first); default 0 = unordered.
		// No FTS — rules are pushed unconditionally at session-start, not searched.
		{
			ID: "089_behavioral_rules",
			Migrate: func(tx *gorm.DB) error {
				sqls := []string{
					// Main table — project NULLable (NULL = global), soft-delete via deleted_at.
					`CREATE TABLE IF NOT EXISTS behavioral_rules (
						id         BIGSERIAL PRIMARY KEY,
						project    TEXT,
						content    TEXT NOT NULL,
						priority   INTEGER NOT NULL DEFAULT 0,
						version    INTEGER NOT NULL DEFAULT 1,
						edited_by  TEXT,
						created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
						updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
						deleted_at TIMESTAMPTZ
					)`,
					// Partial composite index on (project, priority DESC, created_at DESC)
					// for active project-scoped rules — supports session-start inject ordering.
					`CREATE INDEX IF NOT EXISTS idx_behavioral_rules_project_priority
						ON behavioral_rules (project, priority DESC, created_at DESC)
						WHERE deleted_at IS NULL`,
					// Partial index on global rules (project IS NULL) — supports
					// cross-project inject of rules that apply to every session.
					`CREATE INDEX IF NOT EXISTS idx_behavioral_rules_global
						ON behavioral_rules (priority DESC, created_at DESC)
						WHERE project IS NULL AND deleted_at IS NULL`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 089_behavioral_rules: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				sqls := []string{
					`DROP INDEX IF EXISTS idx_behavioral_rules_global`,
					`DROP INDEX IF EXISTS idx_behavioral_rules_project_priority`,
					`DROP TABLE IF EXISTS behavioral_rules`,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 089_behavioral_rules rollback: %w", err)
					}
				}
				return nil
			},
		},

		// Migration 090: 3-way data split observations → credentials + behavioral_rules + memories.
		//
		// NOTE on migration ID: plan.md §Phase 5 originally drafted this as 080_...; actual
		// next-free ID is 090 (US1+US2 consumed 083-086; Commit A=087, Commit B=088+089).
		//
		// NOTE on column mapping: plan.md §Phase 5 Amendment 2026-04-18 corrected column
		// references — the original draft assumed columns that do not exist on observations.
		// See `.agent/specs/engram-v5-baseline/plan.md` §Amendment 2026-04-18 for the full
		// mapping table (content→narrative/title, always_inject→concepts JSONB predicate,
		// tags→concepts, source_agent→agent_source, creation_path filter dropped, etc.).
		//
		// Anti-stub: swap any INSERT body → the sanity-check DO block RAISE EXCEPTION will
		// fire (credential-count mismatch for credentials; 50% floor for memories/rules).
		//
		// SCOPE: this migration copies data. observations table itself is NOT dropped here —
		// that happens in Commit G (migration 091) AFTER Commit F-1 decrypt round-trip gate.
		{
			ID: "090_observations_to_static_entities",
			Migrate: func(tx *gorm.DB) error {
				// 1. Migrate vault credentials FIRST — preserve ciphertext + fingerprint bytes verbatim.
				//    credential name lives in observation.title per ObservationStore.GetCredential.
				//    observations.created_at_epoch is BIGINT (unix milliseconds, set via time.Now().UnixMilli()).
				//    TO_TIMESTAMP expects seconds — divide by 1000.0 for correct conversion.
				if err := tx.Exec(`
					INSERT INTO credentials (project, key, encrypted_secret, encryption_key_fingerprint, scope, created_at, updated_at)
					SELECT
						project,
						title AS key,
						encrypted_secret,
						encryption_key_fingerprint,
						COALESCE(NULLIF(scope, ''), 'project') AS scope,
						TO_TIMESTAMP(created_at_epoch / 1000.0) AS created_at,
						TO_TIMESTAMP(created_at_epoch / 1000.0) AS updated_at
					FROM observations
					WHERE type = 'credential'
					  AND encrypted_secret IS NOT NULL
					  AND encryption_key_fingerprint IS NOT NULL
					  AND title IS NOT NULL AND title != ''
					  AND is_suppressed = false
					  AND COALESCE(is_archived, 0) = 0
					  AND COALESCE(is_superseded, 0) = 0
				`).Error; err != nil {
					return fmt.Errorf("migration 090_observations_to_static_entities: credentials INSERT: %w", err)
				}

				// 2. Migrate always-inject rows (excluding credentials) → behavioral_rules.
				//    "always-inject" is a concepts JSONB array entry, not a boolean column.
				//    priority is derived from importance_score tiers (observations has no priority col).
				if err := tx.Exec(`
					INSERT INTO behavioral_rules (project, content, priority, created_at, updated_at)
					SELECT
						project,
						COALESCE(NULLIF(TRIM(narrative), ''), title, '') AS content,
						CASE
							WHEN importance_score >= 0.8 THEN 10
							WHEN importance_score >= 0.5 THEN 5
							ELSE 0
						END AS priority,
						TO_TIMESTAMP(created_at_epoch / 1000.0) AS created_at,
						TO_TIMESTAMP(created_at_epoch / 1000.0) AS updated_at
					FROM observations
					WHERE concepts @> '["always-inject"]'::jsonb
					  AND type != 'credential'
					  AND COALESCE(NULLIF(TRIM(narrative), ''), NULLIF(TRIM(title), '')) IS NOT NULL
					  AND is_suppressed = false
					  AND COALESCE(is_archived, 0) = 0
					  AND COALESCE(is_superseded, 0) = 0
				`).Error; err != nil {
					return fmt.Errorf("migration 090_observations_to_static_entities: behavioral_rules INSERT: %w", err)
				}

				// 3. Migrate remaining non-credential, non-always-inject observations → memories.
				//    Plan-v1 filtered by creation_path IN (...); column does not exist — amendment drops filter.
				//    memories.project is NOT NULL (per migration 088); legacy rows with NULL project → '' fallback.
				if err := tx.Exec(`
					INSERT INTO memories (project, content, tags, source_agent, created_at, updated_at)
					SELECT
						COALESCE(project, '')                            AS project,
						COALESCE(NULLIF(TRIM(narrative), ''), title, '') AS content,
						COALESCE(concepts, '[]'::jsonb)                  AS tags,
						COALESCE(NULLIF(agent_source, ''), 'unknown')    AS source_agent,
						TO_TIMESTAMP(created_at_epoch / 1000.0)          AS created_at,
						TO_TIMESTAMP(created_at_epoch / 1000.0)          AS updated_at
					FROM observations
					WHERE type != 'credential'
					  AND NOT COALESCE(concepts @> '["always-inject"]'::jsonb, false)
					  AND COALESCE(NULLIF(TRIM(narrative), ''), NULLIF(TRIM(title), '')) IS NOT NULL
					  AND is_suppressed = false
					  AND COALESCE(is_archived, 0) = 0
					  AND COALESCE(is_superseded, 0) = 0
				`).Error; err != nil {
					return fmt.Errorf("migration 090_observations_to_static_entities: memories INSERT: %w", err)
				}

				// 4. Sanity check — two invariants.
				//    Invariant (a): sum of static entities >= 50% of live observations count.
				//    Invariant (b): credentials count == live observations WHERE type='credential' (EXACT — no credential loss).
				//    Anti-stub: swap any INSERT body above → (b) fires for credentials; (a) fires for memories/rules.
				if err := tx.Exec(`
					DO $$
					DECLARE
						src_count       INT;
						dst_count       INT;
						cred_count      INT;
						cred_live_count INT;
					BEGIN
						SELECT COUNT(*) INTO src_count
						FROM observations
						WHERE is_suppressed = false
						  AND COALESCE(is_archived, 0) = 0
						  AND COALESCE(is_superseded, 0) = 0;

						SELECT (SELECT COUNT(*) FROM credentials)
							 + (SELECT COUNT(*) FROM memories)
							 + (SELECT COUNT(*) FROM behavioral_rules)
						INTO dst_count;

						IF src_count > 0 AND dst_count < (src_count / 2) THEN
							RAISE EXCEPTION 'migration 090 sanity check FAILED: only %/% observations migrated across credentials+memories+behavioral_rules', dst_count, src_count;
						END IF;

						SELECT COUNT(*) INTO cred_count FROM credentials;
						SELECT COUNT(*) INTO cred_live_count
						FROM observations
						WHERE type = 'credential'
						  AND encrypted_secret IS NOT NULL
						  AND encryption_key_fingerprint IS NOT NULL
						  AND title IS NOT NULL AND title != ''
						  AND is_suppressed = false
						  AND COALESCE(is_archived, 0) = 0
						  AND COALESCE(is_superseded, 0) = 0;

						IF cred_count != cred_live_count THEN
							RAISE EXCEPTION 'migration 090 credential invariant FAILED: credentials=% != live observations WHERE type=''credential''=% — every vault credential MUST migrate byte-for-byte',
								cred_count, cred_live_count;
						END IF;

						RAISE NOTICE 'migration 090 OK: credentials=% memories=% behavioral_rules=% observations_live=% dst_count=%',
							cred_count,
							(SELECT COUNT(*) FROM memories),
							(SELECT COUNT(*) FROM behavioral_rules),
							src_count,
							dst_count;
					END $$;
				`).Error; err != nil {
					return fmt.Errorf("migration 090_observations_to_static_entities: sanity check: %w", err)
				}

				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				// Rollback is intentionally disabled after cutover.
				//
				// After migration 090 runs successfully, credentials/memories/behavioral_rules
				// contain data that clients may have WRITTEN DIRECTLY (via the new stores) —
				// data that does NOT exist in the observations table. A TRUNCATE would silently
				// destroy that post-migration data with no recovery path.
				//
				// If you genuinely need to roll back pre-cutover (e.g. on a fresh test DB that
				// has never been used after migration), do so manually:
				//   TRUNCATE TABLE memories, behavioral_rules, credentials;
				// and then run gormigrate.RollbackTo("089_...") in a maintenance script.
				return fmt.Errorf("migration 090_observations_to_static_entities: rollback is not safe after cutover — post-migration writes to credentials/memories/behavioral_rules would be destroyed; perform manual rollback if this is a pre-cutover environment")
			},
		},
		{
			// Migration 097_drop_project_settings — US4 (v5 cleanup, plan.md §Phase 4).
			// project_settings held per-project adaptive thresholds (idx_projects_threshold);
			// US4 removes adaptive thresholds entirely, callers use the global default.
			// Per plan.md C3: rollback returns error — project_settings data is not recoverable
			// without a pre-US4 pg_dump (data is derived, can be recomputed from learning signals
			// in a future v5.X if adaptive thresholds return).
			ID: "097_drop_project_settings",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.Exec(`DROP TABLE IF EXISTS project_settings`).Error; err != nil {
					return fmt.Errorf("migration 097_drop_project_settings: DROP: %w", err)
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return fmt.Errorf("migration 097_drop_project_settings rollback is IRREVERSIBLE — project_settings data was derived from learning signals and is not recoverable without a pre-US4 pg_dump snapshot")
			},
		},
		// Migration 098: Drop patterns and pattern_observations tables — US5 (v5 cleanup, plan.md §Phase 5).
		// The pattern subsystem (internal/pattern/) was removed entirely in US5.
		// Both tables are dropped with IF EXISTS so the migration is safe on databases
		// that never had these tables (e.g. fresh installs after US5 branched).
		// Per plan.md C3: rollback is irreversible — pattern data is not recoverable
		// without a pre-US5 pg_dump snapshot.
		{
			ID: "098_drop_patterns",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.Exec(`DROP TABLE IF EXISTS pattern_observations CASCADE`).Error; err != nil {
					return fmt.Errorf("migration 098_drop_patterns: DROP pattern_observations: %w", err)
				}
				if err := tx.Exec(`DROP TABLE IF EXISTS patterns CASCADE`).Error; err != nil {
					return fmt.Errorf("migration 098_drop_patterns: DROP patterns: %w", err)
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return fmt.Errorf("migration 098_drop_patterns rollback is IRREVERSIBLE — pattern data is not recoverable without a pre-US5 pg_dump snapshot")
			},
		},

		// Migration 099: Drop observations table (US3 PR-B — raw log tables sweep).
		// Credential and memory data was migrated to static-entity tables by migration 090.
		// Per plan.md C3: rollback is IRREVERSIBLE — pg_restore required.
		{
			ID: "099_drop_observations",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS observations CASCADE").Error
			},
			Rollback: func(tx *gorm.DB) error {
				return fmt.Errorf("099_drop_observations: IRREVERSIBLE — pg_restore required (C3)")
			},
		},

		// Migration 100: Drop user_prompts table (US3 PR-B).
		// Per plan.md C3: rollback is IRREVERSIBLE — pg_restore required.
		{
			ID: "100_drop_user_prompts",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS user_prompts CASCADE").Error
			},
			Rollback: func(tx *gorm.DB) error {
				return fmt.Errorf("100_drop_user_prompts: IRREVERSIBLE — pg_restore required (C3)")
			},
		},

		// Migration 101: Drop raw_events table (US3 PR-B).
		// Per plan.md C3: rollback is IRREVERSIBLE — pg_restore required.
		{
			ID: "101_drop_raw_event_store",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS raw_events CASCADE").Error
			},
			Rollback: func(tx *gorm.DB) error {
				return fmt.Errorf("101_drop_raw_event_store: IRREVERSIBLE — pg_restore required (C3)")
			},
		},

		// Migration 102: Drop indexed_sessions table (US3 PR-B).
		// Per plan.md C3: rollback is IRREVERSIBLE — pg_restore required.
		{
			ID: "102_drop_indexed_sessions",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS indexed_sessions CASCADE").Error
			},
			Rollback: func(tx *gorm.DB) error {
				return fmt.Errorf("102_drop_indexed_sessions: IRREVERSIBLE — pg_restore required (C3)")
			},
		},

		// Migration 103: Drop session_summaries table (US3 PR-B).
		// Per plan.md C3: rollback is IRREVERSIBLE — pg_restore required.
		{
			ID: "103_drop_summaries",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE IF EXISTS session_summaries CASCADE").Error
			},
			Rollback: func(tx *gorm.DB) error {
				return fmt.Errorf("103_drop_summaries: IRREVERSIBLE — pg_restore required (C3)")
			},
		},

		// Migration 104: Drop sdk_sessions table (US3 PR-B).
		// DISABLED for now: SessionStore is still wired, so dropping sdk_sessions breaks
		// the live code path. Keep the migration ID reserved until the consumer is fully
		// removed in a follow-up chunk. Rollback remains documented as irreversible.
		{
			ID: "104_drop_sdk_sessions",
			Migrate: func(tx *gorm.DB) error {
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return fmt.Errorf("104_drop_sdk_sessions: IRREVERSIBLE — pg_restore required (C3)")
			},
		},
	})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("run gormigrate migrations: %w", err)
	}

	return nil
}
