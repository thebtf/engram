// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// vectorIndexSQL returns the CREATE INDEX statement for vectors and content_chunks tables
// based on embedding dimensions. Uses tiered strategy:
//   - ≤2000 dims: HNSW (pgvector native, exact recall)
//   - >2000 dims: DiskANN via pgvectorscale (supports up to 16000 dims)
//
// If pgvectorscale is not available for >2000 dims, falls back to IVFFlat with a warning.
func vectorIndexSQL(dims int, db *gorm.DB) (vectorsIdx, chunksIdx string) {
	if dims <= 2000 {
		return "CREATE INDEX idx_vectors_embedding_hnsw ON vectors USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64)",
			"CREATE INDEX idx_content_chunks_embedding_hnsw ON content_chunks USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64)"
	}

	// >2000 dims: try pgvectorscale DiskANN
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vectorscale").Error; err == nil {
		log.Info().Int("dims", dims).Msg("Using pgvectorscale DiskANN index for high-dimensional vectors")
		return "CREATE INDEX idx_vectors_embedding_diskann ON vectors USING diskann (embedding vector_cosine_ops)",
			"CREATE INDEX idx_content_chunks_embedding_diskann ON content_chunks USING diskann (embedding vector_cosine_ops)"
	}

	// Fallback: IVFFlat (no dimension limit, lower recall)
	log.Warn().Int("dims", dims).Msg("pgvectorscale not available — falling back to IVFFlat index (lower recall). Install pgvectorscale for DiskANN support.")
	lists := dims / 10
	if lists < 100 {
		lists = 100
	}
	return fmt.Sprintf("CREATE INDEX idx_vectors_embedding_ivfflat ON vectors USING ivfflat (embedding vector_cosine_ops) WITH (lists = %d)", lists),
		fmt.Sprintf("CREATE INDEX idx_content_chunks_embedding_ivfflat ON content_chunks USING ivfflat (embedding vector_cosine_ops) WITH (lists = %d)", lists)
}

// runMigrations runs all database migrations using gormigrate.
func runMigrations(db *gorm.DB, embeddingDims int) error {
	// Validate embedding dimensions before using in DDL statements.
	if embeddingDims <= 0 {
		return fmt.Errorf("invalid embedding dimensions: %d (must be positive)", embeddingDims)
	}

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
		{
			ID: "020_configurable_vector_dimensions",
			Migrate: func(tx *gorm.DB) error {
				// pgvector stores vector(N) dimension count directly in atttypmod.
			const currentDimQuery = "SELECT atttypmod FROM pg_attribute WHERE attrelid = 'vectors'::regclass AND attname = 'embedding' AND atttypmod > 0"
				var current int
				row := tx.Raw(currentDimQuery).Row()
				if err := row.Scan(&current); err != nil {
					if err == sql.ErrNoRows {
						return nil
					}
					return fmt.Errorf("read current vector dimension: %w", err)
				}
				if current == embeddingDims {
					return nil
				}

				log.Warn().Msgf("Embedding dimension changed from %d to %d, truncating vectors and content_chunks", current, embeddingDims)

				colType := fmt.Sprintf("vector(%d)", embeddingDims)

				// Tiered indexing: HNSW for ≤2000 dims, DiskANN (pgvectorscale) for >2000.
				vectorsIdx, chunksIdx := vectorIndexSQL(embeddingDims, tx)

				sqls := []string{
					// Drop indexes BEFORE altering column type — PostgreSQL validates
					// existing indexes against the new vector size during ALTER TABLE.
					"DROP INDEX IF EXISTS idx_vectors_embedding_hnsw",
					"DROP INDEX IF EXISTS idx_vectors_embedding_diskann",
					"DROP INDEX IF EXISTS idx_vectors_embedding_ivfflat",
					"TRUNCATE vectors",
					fmt.Sprintf("ALTER TABLE vectors ALTER COLUMN embedding TYPE %s", colType),
					vectorsIdx,
					"DROP INDEX IF EXISTS idx_content_chunks_embedding_hnsw",
					"DROP INDEX IF EXISTS idx_content_chunks_embedding_diskann",
					"DROP INDEX IF EXISTS idx_content_chunks_embedding_ivfflat",
					"TRUNCATE content_chunks",
					fmt.Sprintf("ALTER TABLE content_chunks ALTER COLUMN embedding TYPE %s", colType),
					chunksIdx,
				}
				for _, s := range sqls {
					if err := tx.Exec(s).Error; err != nil {
						return fmt.Errorf("migration 020: %w", err)
					}
				}

				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				// Intentionally irreversible: TRUNCATE destroys data, restore from backup to revert.
				return fmt.Errorf("migration 020 is irreversible: dimension change truncated vectors; restore from backup")
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
	})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("run gormigrate migrations: %w", err)
	}

	if err := validateEmbeddingDimension(db, embeddingDims); err != nil {
		return err
	}

	return nil
}

func validateEmbeddingDimension(db *gorm.DB, expected int) error {
	var actual int
	// pgvector stores vector(N) dimension count directly in atttypmod.
	row := db.Raw("SELECT atttypmod FROM pg_attribute WHERE attrelid = 'vectors'::regclass AND attname = 'embedding' AND atttypmod > 0").Row()
	if err := row.Scan(&actual); err != nil {
		return fmt.Errorf("cannot read vector dimension from pg_attribute: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("embedding dimension mismatch: DB has vector(%d) but config says %d", actual, expected)
	}
	return nil
}
