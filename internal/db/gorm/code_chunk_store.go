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
	CreatedAt       time.Time        `gorm:"type:timestamptz;not null;default:now()" json:"created_at"`
	UpdatedAt       time.Time        `gorm:"type:timestamptz;not null;default:now()" json:"updated_at"`
	ProjectID       string           `gorm:"column:project_id;type:text;not null" json:"project_id"`
	FilePath        string           `gorm:"column:file_path;type:text;not null" json:"file_path"`
	Language        string           `gorm:"type:text;not null" json:"language"`
	ChunkType       string           `gorm:"column:chunk_type;type:text;not null" json:"chunk_type"`
	Content         string           `gorm:"type:text;not null" json:"content"`
	ContentSHA256   string           `gorm:"column:content_sha256;type:text;not null" json:"content_sha256"`
	IndexSessionID  string           `gorm:"column:index_session_id;type:text;not null" json:"index_session_id"`
	// ContentTsv is a GENERATED ALWAYS AS STORED column; never written by application code.
	ContentTsv      string           `gorm:"column:content_tsv;->"  json:"-"`
	Embedding       *pgvector.Vector `gorm:"type:vector(1536)" json:"embedding,omitempty"`
	ID              int64            `gorm:"primaryKey;autoIncrement" json:"id"`
	ByteStart       int              `gorm:"column:byte_start;not null" json:"byte_start"`
	ByteEnd         int              `gorm:"column:byte_end;not null" json:"byte_end"`
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
// keepKeys elements must be formatted as "filePath\x00byteStart\x00sha256"
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
			  AND (file_path || chr(0) || byte_start::text || chr(0) || content_sha256)
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

// StaleKey constructs the composite key string used by DeleteStaleForProject.
// Format: "filePath\x00byteStart\x00sha256" — matches the DB-side expression.
func StaleKey(filePath string, byteStart int, contentSHA256 string) string {
	return fmt.Sprintf("%s\x00%d\x00%s", filePath, byteStart, contentSHA256)
}
