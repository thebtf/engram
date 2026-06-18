// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

const staleKeyDelimiter = "\x1f"

// CodeChunk is the GORM model for the code_chunks table (migration 139).
// Stores AST-chunked source code with optional embeddings for hybrid code search.
//
// Design notes:
//   - project_id is a git-remote-derived TEXT slug (proxy.ResolveProjectSlug),
//     intentionally FK-free — it is not a row reference to the projects table.
//     Two worktrees of the same repo share one index via identical project_id.
//     See ADR-001 §3.3.
//   - embedding is nullable: rows may be inserted before the server-side embedding
//     pipeline (CR-004) has computed the vector. Matches content_chunks convention.
//   - content_tsv is a GENERATED ALWAYS AS STORED column in PostgreSQL; the gorm
//     "->" read-only tag prevents GORM from ever writing it in INSERT/UPDATE.
//   - The UNIQUE constraint (project_id, file_path, byte_start, content_sha256)
//     makes re-indexing idempotent: an unchanged chunk upserts to the same row
//     (no-op on conflict); a changed chunk has a new sha256 → new row.
//
// CLEAN-ROOM: no AGPL source referenced during implementation.
// ADR: .agent/specs/engram-absorption/adr/ADR-001-ci-a-topology.md §4.
type CodeChunk struct {
	CreatedAt      time.Time `gorm:"type:timestamptz;not null;default:now()" json:"created_at"`
	UpdatedAt      time.Time `gorm:"type:timestamptz;not null;default:now()" json:"updated_at"`
	ProjectID      string    `gorm:"column:project_id;type:text;not null" json:"project_id"`
	FilePath       string    `gorm:"column:file_path;type:text;not null" json:"file_path"`
	Language       string    `gorm:"type:text;not null" json:"language"`
	ChunkType      string    `gorm:"column:chunk_type;type:text;not null" json:"chunk_type"`
	Content        string    `gorm:"type:text;not null" json:"content"`
	ContentSHA256  string    `gorm:"column:content_sha256;type:text;not null" json:"content_sha256"`
	IndexSessionID string    `gorm:"column:index_session_id;type:text;not null" json:"index_session_id"`
	// ContentTsv is a GENERATED ALWAYS AS STORED column; never written by application code.
	ContentTsv string           `gorm:"column:content_tsv;->"  json:"-"`
	Embedding  *pgvector.Vector `gorm:"type:vector(1536)" json:"embedding,omitempty"`
	ID         int64            `gorm:"primaryKey;autoIncrement" json:"id"`
	ByteStart  int              `gorm:"column:byte_start;not null" json:"byte_start"`
	ByteEnd    int              `gorm:"column:byte_end;not null" json:"byte_end"`
}

// TableName returns the PostgreSQL table name for CodeChunk.
func (CodeChunk) TableName() string { return "code_chunks" }

// CodeChunkStore provides persistence operations for the code_chunks table.
// It is the persistence layer for CI-A (CR-001); search and index pipeline
// wiring is added by CR-003 through CR-006.
//
// Immutability contract: store methods return new values and never mutate
// caller-owned input. Callers may freely reuse or discard input pointers.
type CodeChunkStore struct {
	db *gorm.DB
}

// NewCodeChunkStore creates a CodeChunkStore backed by the given GORM DB.
func NewCodeChunkStore(db *gorm.DB) *CodeChunkStore {
	return &CodeChunkStore{db: db}
}

// Upsert inserts a code chunk row or updates it on conflict with the UNIQUE key
// (project_id, file_path, byte_start, content_sha256). An unchanged chunk
// re-uploaded during a re-index is a no-op (DO NOTHING). A chunk whose content
// changed has a different content_sha256 and inserts as a new row; the stale
// old row is removed by DeleteByProjectFile or DeleteStaleForProject.
//
// Upsert never mutates the caller-owned chunk pointer.
func (s *CodeChunkStore) Upsert(ctx context.Context, chunk *CodeChunk) error {
	if chunk == nil {
		return fmt.Errorf("code_chunk_store upsert: chunk must not be nil")
	}
	// Use ON CONFLICT DO NOTHING: an identical row (same project+file+byteStart+sha256)
	// is a pure no-op; re-indexing the same content should be free.
	//
	// NOTE for CR-004 (embed pipeline): the conflict path is DO NOTHING, so calling
	// Upsert again to set an embedding on an existing row is a SILENT no-op. CR-004
	// must add a dedicated UpdateEmbedding(ctx, id, vec) method rather than re-Upserting.
	// (A changed chunk has a new content_sha256 → a new row, not a conflict, so the
	// re-index path itself is unaffected.)
	result := s.db.WithContext(ctx).
		Exec(`
			INSERT INTO code_chunks
				(project_id, file_path, byte_start, byte_end, language, chunk_type,
				 content, content_sha256, embedding, index_session_id, created_at, updated_at)
			VALUES
				(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, now(), now())
			ON CONFLICT (project_id, file_path, byte_start, content_sha256)
			DO NOTHING
		`,
			chunk.ProjectID,
			chunk.FilePath,
			chunk.ByteStart,
			chunk.ByteEnd,
			chunk.Language,
			chunk.ChunkType,
			chunk.Content,
			chunk.ContentSHA256,
			chunk.Embedding,
			chunk.IndexSessionID,
		)
	if result.Error != nil {
		return fmt.Errorf("code_chunk_store upsert: %w", result.Error)
	}
	return nil
}

// DeleteByProjectFile removes all code chunks for the given project and file path.
// Used when a file is deleted from the working tree or moves to a new path.
func (s *CodeChunkStore) DeleteByProjectFile(ctx context.Context, projectID, filePath string) error {
	if projectID == "" {
		return fmt.Errorf("code_chunk_store delete_by_project_file: projectID must not be empty")
	}
	if filePath == "" {
		return fmt.Errorf("code_chunk_store delete_by_project_file: filePath must not be empty")
	}
	result := s.db.WithContext(ctx).
		Where("project_id = ? AND file_path = ?", projectID, filePath).
		Delete(&CodeChunk{})
	if result.Error != nil {
		return fmt.Errorf("code_chunk_store delete_by_project_file %q %q: %w", projectID, filePath, result.Error)
	}
	return nil
}

// DeleteStaleForProject removes chunks for the given project whose
// (file_path, byte_start, content_sha256) triple is NOT in the keepKeys set.
// keepKeys elements must be formatted as "filePath<US>byteStart<US>sha256"
// using the StaleKey helper. This is the delta-negotiation cleanup path:
// after a full re-index, stale rows (chunks the client no longer reports) are
// removed in one bulk delete per project.
//
// When keepKeys is empty the call is a no-op (safety guard: deleting everything
// on an empty keep-set would wipe a project index on a client-side bug).
func (s *CodeChunkStore) DeleteStaleForProject(ctx context.Context, projectID string, keepKeys []string) error {
	if projectID == "" {
		return fmt.Errorf("code_chunk_store delete_stale: projectID must not be empty")
	}
	if len(keepKeys) == 0 {
		// Safety guard: never wipe a project on an empty keep-set.
		return nil
	}
	// Build a set of (file_path, byte_start, content_sha256) tuples to keep.
	// Use a NOT IN subquery expressed as a raw DELETE for portability.
	// The keepKeys are composite strings; the DB-side equivalent is computed
	// inline so no separate parsing is needed.
	result := s.db.WithContext(ctx).
		Exec(`
			DELETE FROM code_chunks
			WHERE project_id = ?
			  AND (file_path || E'\x1f' || byte_start::text || E'\x1f' || content_sha256)
			      NOT IN (SELECT unnest(?::text[]))
		`, projectID, pq.Array(keepKeys))
	if result.Error != nil {
		return fmt.Errorf("code_chunk_store delete_stale %q: %w", projectID, result.Error)
	}
	return nil
}

// ListByProject returns up to limit code chunks for the given project,
// ordered by file_path and byte_start (stable, deterministic order).
// limit <= 0 defaults to 100.
func (s *CodeChunkStore) ListByProject(ctx context.Context, projectID string, limit int) ([]*CodeChunk, error) {
	if projectID == "" {
		return nil, fmt.Errorf("code_chunk_store list_by_project: projectID must not be empty")
	}
	if limit <= 0 {
		limit = 100
	}
	var rows []CodeChunk
	if err := s.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("file_path ASC, byte_start ASC").
		Limit(limit).
		Find(&rows).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("code_chunk_store list_by_project %q: %w", projectID, err)
	}
	out := make([]*CodeChunk, len(rows))
	for i := range rows {
		cp := rows[i] // copy so caller owns each pointer independently
		out[i] = &cp
	}
	return out, nil
}

// CountByProject returns the number of code chunks stored for the given project.
// Used by the codebase_status MCP tool (CR-006) to report index size.
// Returns 0 (no error) when the project has no chunks.
func (s *CodeChunkStore) CountByProject(ctx context.Context, projectID string) (int64, error) {
	if projectID == "" {
		return 0, fmt.Errorf("code_chunk_store count_by_project: projectID must not be empty")
	}
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&CodeChunk{}).
		Where("project_id = ?", projectID).
		Count(&count).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("code_chunk_store count_by_project %q: %w", projectID, err)
	}
	return count, nil
}

// UpdateEmbedding sets the embedding vector for the code chunk identified by id.
// This is the ONLY correct way to persist an embedding on an existing code_chunk
// row: Upsert uses ON CONFLICT DO NOTHING, so re-Upserting to set an embedding
// is a silent no-op.
//
// id <= 0 is rejected as a guard against zero-valued struct fields.
// RowsAffected == 0 is not treated as an error: the row may have been swept
// concurrently (e.g. by DeleteStaleForProject or DeleteBySessionMismatch).
func (s *CodeChunkStore) UpdateEmbedding(ctx context.Context, id int64, vec pgvector.Vector) error {
	if id <= 0 {
		return fmt.Errorf("code_chunk_store update_embedding: id must be > 0, got %d", id)
	}
	result := s.db.WithContext(ctx).
		Exec("UPDATE code_chunks SET embedding = ?, updated_at = now() WHERE id = ?", vec, id)
	if result.Error != nil {
		return fmt.Errorf("code_chunk_store update_embedding id=%d: %w", id, result.Error)
	}
	return nil
}

// ListUnembedded returns up to limit code chunks whose embedding IS NULL,
// ordered by id ASC for deterministic batching. This is the work-query for the
// code backfill loop (CR-004): it finds rows that were inserted before the
// embedding pipeline was active (or when ENGRAM_EMBEDDING_URL was unset) and
// have never received a vector.
//
// limit <= 0 defaults to 50. Returns an empty (non-nil) slice when no unembedded
// rows exist — the caller treats an empty result as "backfill complete".
func (s *CodeChunkStore) ListUnembedded(ctx context.Context, limit int) ([]*CodeChunk, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []CodeChunk
	if err := s.db.WithContext(ctx).
		Where("embedding IS NULL").
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("code_chunk_store list_unembedded: %w", err)
	}
	out := make([]*CodeChunk, len(rows))
	for i := range rows {
		cp := rows[i]
		out[i] = &cp
	}
	return out, nil
}

// StaleKey constructs the composite key string used by DeleteStaleForProject,
// ListIdentityKeysByProject, TouchSession, and DeleteBySessionMismatch.
// Format: "filePath<US>byteStart<US>sha256" — matches the DB-side expression.
// PostgreSQL text cannot contain NUL bytes, so this uses ASCII Unit Separator.
func StaleKey(filePath string, byteStart int, contentSHA256 string) string {
	return fmt.Sprintf("%s%s%d%s%s", filePath, staleKeyDelimiter, byteStart, staleKeyDelimiter, contentSHA256)
}

// ChunkIdentity carries the three fields that form the chunk's identity key.
// Used by CodeIndexNegotiate (CR-003) to diff the server's stored chunks
// against the client's manifest without loading content or embeddings.
type ChunkIdentity struct {
	FilePath      string
	ByteStart     int
	ContentSHA256 string
}

// ListIdentityKeysByProject returns the (file_path, byte_start, content_sha256)
// triples for every chunk belonging to projectID. Content and embeddings are
// intentionally excluded — this is the lightweight diff source for
// CodeIndexNegotiate (CR-003).
func (s *CodeChunkStore) ListIdentityKeysByProject(ctx context.Context, projectID string) ([]ChunkIdentity, error) {
	if projectID == "" {
		return nil, fmt.Errorf("code_chunk_store list_identity_keys: projectID must not be empty")
	}
	type row struct {
		FilePath      string `gorm:"column:file_path"`
		ByteStart     int    `gorm:"column:byte_start"`
		ContentSHA256 string `gorm:"column:content_sha256"`
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Raw("SELECT file_path, byte_start, content_sha256 FROM code_chunks WHERE project_id = ?", projectID).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("code_chunk_store list_identity_keys %q: %w", projectID, err)
	}
	out := make([]ChunkIdentity, len(rows))
	for i, r := range rows {
		out[i] = ChunkIdentity{
			FilePath:      r.FilePath,
			ByteStart:     r.ByteStart,
			ContentSHA256: r.ContentSHA256,
		}
	}
	return out, nil
}

// TouchSession sets index_session_id = sessionID and updated_at = now() for
// every chunk in keepKeys (StaleKey format). This marks the chunks that the
// client still has as belonging to the current index session so that
// DeleteBySessionMismatch can sweep any rows left on an old session id.
//
// keepKeys must be formatted as "filePath<US>byteStart<US>sha256" using
// StaleKey. Empty keepKeys is a no-op — returns (0, nil) safely.
// Returns the number of rows updated.
func (s *CodeChunkStore) TouchSession(ctx context.Context, projectID, sessionID string, keepKeys []string) (int64, error) {
	if projectID == "" {
		return 0, fmt.Errorf("code_chunk_store touch_session: projectID must not be empty")
	}
	if sessionID == "" {
		return 0, fmt.Errorf("code_chunk_store touch_session: sessionID must not be empty")
	}
	if len(keepKeys) == 0 {
		return 0, nil
	}
	result := s.db.WithContext(ctx).
		Exec(`
			UPDATE code_chunks
			SET index_session_id = ?,
			    updated_at       = now()
			WHERE project_id = ?
			  AND (file_path || E'\x1f' || byte_start::text || E'\x1f' || content_sha256)
			      IN (SELECT unnest(?::text[]))
	`, sessionID, projectID, pq.Array(keepKeys))
	if result.Error != nil {
		return 0, fmt.Errorf("code_chunk_store touch_session %q %q: %w", projectID, sessionID, result.Error)
	}
	return result.RowsAffected, nil
}

// RegisterSession records that a CodeIndexNegotiate cycle ran for
// (projectID, sessionID) by inserting a row into code_index_sessions. This
// authorization record — not the presence of stamped chunks — is what
// DeleteBySessionMismatch consults to decide whether the stale-sweep may run.
//
// Why a separate table rather than counting chunks: a delete-all re-index
// (the client dropped every file) legitimately marks zero surviving chunks
// with the new session id, so a "count chunks carrying this session" guard
// cannot tell that case apart from a stray upload that never negotiated. The
// negotiate cycle ALWAYS writes this row, so the sweep authorization is
// independent of how many chunks survived.
//
// ON CONFLICT DO NOTHING makes a repeated negotiate for the same session a
// no-op. Returns nil on success.
func (s *CodeChunkStore) RegisterSession(ctx context.Context, projectID, sessionID string) error {
	if projectID == "" {
		return fmt.Errorf("code_chunk_store register_session: projectID must not be empty")
	}
	if sessionID == "" {
		return fmt.Errorf("code_chunk_store register_session: sessionID must not be empty")
	}
	result := s.db.WithContext(ctx).Exec(`
		INSERT INTO code_index_sessions (project_id, index_session_id, created_at)
		VALUES (?, ?, now())
		ON CONFLICT (project_id, index_session_id) DO NOTHING
	`, projectID, sessionID)
	if result.Error != nil {
		return fmt.Errorf("code_chunk_store register_session %q %q: %w", projectID, sessionID, result.Error)
	}
	return nil
}

// DeleteBySessionMismatch removes every chunk for projectID whose
// index_session_id differs from sessionID. This is the stale-sweep step
// called at CodeIndexUpload EOF: after the upload stream closes, any chunk
// that was NOT uploaded in this session (i.e. still carries an old session id)
// is no longer part of the current codebase and should be removed.
//
// SAFETY GUARD: before deleting, we verify a code_index_sessions row exists for
// (projectID, sessionID) — i.e. CodeIndexNegotiate ran and called
// RegisterSession for this exact session. If no such row exists (a stray Upload
// without a prior Negotiate), the delete is skipped and (0, nil) is returned.
// This authorization is independent of chunk presence, so it correctly permits
// a delete-all re-index (zero surviving chunks) to sweep while still rejecting
// an un-negotiated upload — closing the hole a chunk-count guard left open.
//
// Returns the number of rows deleted.
func (s *CodeChunkStore) DeleteBySessionMismatch(ctx context.Context, projectID, sessionID string) (int64, error) {
	if projectID == "" {
		return 0, fmt.Errorf("code_chunk_store delete_by_session_mismatch: projectID must not be empty")
	}
	if sessionID == "" {
		return 0, fmt.Errorf("code_chunk_store delete_by_session_mismatch: sessionID must not be empty")
	}

	// Safety guard: the sweep is authorized only when CodeIndexNegotiate
	// registered this session. A registered session means the client completed
	// a real negotiate cycle, so its absence-of-surviving-chunks is meaningful
	// (delete-all) rather than accidental (stray upload).
	var registered int64
	if err := s.db.WithContext(ctx).
		Raw("SELECT COUNT(*) FROM code_index_sessions WHERE project_id = ? AND index_session_id = ?", projectID, sessionID).
		Scan(&registered).Error; err != nil {
		return 0, fmt.Errorf("code_chunk_store delete_by_session_mismatch authcheck %q %q: %w", projectID, sessionID, err)
	}
	if registered == 0 {
		// No negotiate cycle registered this session; skip sweep to protect the
		// existing index against a mis-routed or un-negotiated upload.
		return 0, nil
	}

	result := s.db.WithContext(ctx).
		Exec("DELETE FROM code_chunks WHERE project_id = ? AND index_session_id <> ?", projectID, sessionID)
	if result.Error != nil {
		return 0, fmt.Errorf("code_chunk_store delete_by_session_mismatch %q %q: %w", projectID, sessionID, result.Error)
	}
	return result.RowsAffected, nil
}

// CodeSearchResult holds a single code chunk match returned by SearchCodeFTS
// or FindSimilarCode. Score is ts_rank_cd for FTS results and cosine similarity
// (1 - distance) for vector results. It is used for explain/debug output and
// for building the CodeHit map in the hybrid orchestrator; RRF itself only
// uses the ID ordering.
type CodeSearchResult struct {
	ID        int64
	FilePath  string
	ByteStart int
	ByteEnd   int
	Language  string
	Content   string
	Score     float64
}

// SearchCodeFTS runs a full-text search against the code_chunks table for the
// given projectID and query string. It uses the 'simple' text-search
// configuration on BOTH the query side and the stored content_tsv column
// (which is generated with to_tsvector('simple', content)). Using 'english'
// here would silently return zero rows because the configs would not match.
//
// The CTE parses the query with websearch_to_tsquery first (supports quoted
// phrases, AND/OR/- operators) and falls back to plainto_tsquery when
// websearch_to_tsquery produces an empty tsquery (e.g. input with only
// stop-words or special characters that websearch fails to parse).
//
// limit <= 0 defaults to 20; capped at 200. Returns an empty (non-nil) slice
// when the query matches no rows — that is not an error.
func (s *CodeChunkStore) SearchCodeFTS(ctx context.Context, projectID, query string, limit int) ([]CodeSearchResult, error) {
	if projectID == "" {
		return nil, fmt.Errorf("code_chunk_store search_code_fts: projectID must not be empty")
	}
	if query == "" {
		return nil, fmt.Errorf("code_chunk_store search_code_fts: query must not be empty")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	// The CTE builds a parsed tsquery once and reuses it across SELECT and WHERE.
	// COALESCE(NULLIF(wsq,''::tsquery), ptq) falls back to plainto_tsquery when
	// websearch_to_tsquery returns an empty result for the given input.
	const sql = `
		WITH parsed AS (
			SELECT websearch_to_tsquery('simple', ?) wsq,
			       plainto_tsquery('simple', ?)       ptq
		)
		SELECT id,
		       file_path,
		       byte_start,
		       byte_end,
		       language,
		       content,
		       ts_rank_cd(content_tsv, COALESCE(NULLIF(parsed.wsq, ''::tsquery), parsed.ptq)) AS score
		FROM code_chunks, parsed
		WHERE project_id = ?
		  AND content_tsv @@ COALESCE(NULLIF(parsed.wsq, ''::tsquery), parsed.ptq)
		ORDER BY score DESC
		LIMIT ?
	`

	type row struct {
		ID        int64   `gorm:"column:id"`
		FilePath  string  `gorm:"column:file_path"`
		ByteStart int     `gorm:"column:byte_start"`
		ByteEnd   int     `gorm:"column:byte_end"`
		Language  string  `gorm:"column:language"`
		Content   string  `gorm:"column:content"`
		Score     float64 `gorm:"column:score"`
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Raw(sql, query, query, projectID, limit).
		Scan(&rows).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("code_chunk_store search_code_fts %q: %w", projectID, err)
	}

	out := make([]CodeSearchResult, len(rows))
	for i, r := range rows {
		out[i] = CodeSearchResult{
			ID:        r.ID,
			FilePath:  r.FilePath,
			ByteStart: r.ByteStart,
			ByteEnd:   r.ByteEnd,
			Language:  r.Language,
			Content:   r.Content,
			Score:     r.Score,
		}
	}
	return out, nil
}

// FindSimilarCode queries the code_chunks table for rows whose embedding is
// closest (cosine similarity) to queryVec, scoped to projectID.
//
// The WHERE clause requires embedding IS NOT NULL because CR-004 inserts rows
// before computing the vector; a NULL embedding produces a NULL distance which
// breaks ORDER BY and silently poisons the result set.
//
// limit <= 0 defaults to 10. threshold <= 0 defaults to 0.7.
// Returns an empty (non-nil) slice when no rows meet the threshold.
func (s *CodeChunkStore) FindSimilarCode(ctx context.Context, projectID string, queryVec []float32, limit int, threshold float64) ([]CodeSearchResult, error) {
	if projectID == "" {
		return nil, fmt.Errorf("code_chunk_store find_similar_code: projectID must not be empty")
	}
	if len(queryVec) == 0 {
		return nil, fmt.Errorf("code_chunk_store find_similar_code: queryVec must not be empty")
	}
	if limit <= 0 {
		limit = 10
	}
	if threshold <= 0 {
		threshold = 0.7
	}

	vec := pgvector.NewVector(queryVec)

	// The cosine operator <=> returns distance (0 = identical, 2 = opposite).
	// Similarity = 1 - distance. We filter on the similarity expression and order
	// by raw distance so the index (HNSW idx_code_chunks_hnsw) is usable.
	const sql = `
		SELECT id,
		       file_path,
		       byte_start,
		       byte_end,
		       language,
		       content,
		       1 - (embedding <=> ?::vector) AS score
		FROM code_chunks
		WHERE project_id = ?
		  AND embedding IS NOT NULL
		  AND 1 - (embedding <=> ?::vector) >= ?
		ORDER BY embedding <=> ?::vector
		LIMIT ?
	`

	type row struct {
		ID        int64   `gorm:"column:id"`
		FilePath  string  `gorm:"column:file_path"`
		ByteStart int     `gorm:"column:byte_start"`
		ByteEnd   int     `gorm:"column:byte_end"`
		Language  string  `gorm:"column:language"`
		Content   string  `gorm:"column:content"`
		Score     float64 `gorm:"column:score"`
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Raw(sql, vec, projectID, vec, threshold, vec, limit).
		Scan(&rows).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("code_chunk_store find_similar_code %q: %w", projectID, err)
	}

	out := make([]CodeSearchResult, len(rows))
	for i, r := range rows {
		out[i] = CodeSearchResult{
			ID:        r.ID,
			FilePath:  r.FilePath,
			ByteStart: r.ByteStart,
			ByteEnd:   r.ByteEnd,
			Language:  r.Language,
			Content:   r.Content,
			Score:     r.Score,
		}
	}
	return out, nil
}

// CountEmbeddedByProject returns the number of code chunks for the given project
// whose embedding is NOT NULL (i.e. the embedding pipeline has processed them).
// Used by codebase_status to report how many chunks have vector coverage.
// Returns 0 (no error) when the project has no embedded chunks.
func (s *CodeChunkStore) CountEmbeddedByProject(ctx context.Context, projectID string) (int64, error) {
	if projectID == "" {
		return 0, fmt.Errorf("code_chunk_store count_embedded_by_project: projectID must not be empty")
	}
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&CodeChunk{}).
		Where("project_id = ? AND embedding IS NOT NULL", projectID).
		Count(&count).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("code_chunk_store count_embedded_by_project %q: %w", projectID, err)
	}
	return count, nil
}

// MaxUpdatedAtByProject returns the maximum updated_at timestamp across all code
// chunks for the given project. The second return value reports whether any row
// exists (false means the project has never been indexed). Used by codebase_status
// to report when the index was last updated.
func (s *CodeChunkStore) MaxUpdatedAtByProject(ctx context.Context, projectID string) (time.Time, bool, error) {
	if projectID == "" {
		return time.Time{}, false, fmt.Errorf("code_chunk_store max_updated_at_by_project: projectID must not be empty")
	}
	var result struct {
		MaxAt *time.Time `gorm:"column:max_at"`
	}
	if err := s.db.WithContext(ctx).
		Raw("SELECT MAX(updated_at) AS max_at FROM code_chunks WHERE project_id = ?", projectID).
		Scan(&result).Error; err != nil {
		return time.Time{}, false, fmt.Errorf("code_chunk_store max_updated_at_by_project %q: %w", projectID, err)
	}
	if result.MaxAt == nil {
		return time.Time{}, false, nil
	}
	return *result.MaxAt, true, nil
}

// DeleteSession removes the authorization record for (projectID, sessionID)
// after a completed upload cycle so code_index_sessions does not accumulate
// rows unboundedly. Best-effort: callers may ignore the error (the row is
// harmless if it lingers and is overwritten on the next negotiate). Returns
// the number of rows deleted.
func (s *CodeChunkStore) DeleteSession(ctx context.Context, projectID, sessionID string) (int64, error) {
	if projectID == "" {
		return 0, fmt.Errorf("code_chunk_store delete_session: projectID must not be empty")
	}
	if sessionID == "" {
		return 0, fmt.Errorf("code_chunk_store delete_session: sessionID must not be empty")
	}
	result := s.db.WithContext(ctx).
		Exec("DELETE FROM code_index_sessions WHERE project_id = ? AND index_session_id = ?", projectID, sessionID)
	if result.Error != nil {
		return 0, fmt.Errorf("code_chunk_store delete_session %q %q: %w", projectID, sessionID, result.Error)
	}
	return result.RowsAffected, nil
}
